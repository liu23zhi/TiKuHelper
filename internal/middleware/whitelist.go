package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// WhitelistContextKey 是「本次请求命中了白名单」在 gin.Context 上的标记键。
//
// 值取 service.WhitelistSourceEntry（表规则）或 service.WhitelistSourceExemptIPs
// （blacklist_exempt_ips 文本域）。它只做诊断：handler 与日志能一眼看出
// 「这个 IP 为什么没被黑名单处理」，不必去猜是哪一层放行的。
const WhitelistContextKey = "whitelist_match_source"

// WhitelistIP 是「按 IP 白名单」中间件（task_doc/123 T-04）。
//
// ## 为什么它**包着**黑名单判定，而不是与 BlacklistIP 并列挂在链上
//
// 判据（本项存在的理由）：**白名单命中 → 直接放行，完全不查黑名单**。
// 这不是「两条都查再取反」——后者会带来两个可观察的错误：
//
//  1. 命中白名单的请求仍会写 blacklist_hits 行（留痕污染，判据①）；
//  2. 黑名单的 fail-open / 地区归属地查询 / 聚合器槽位照常被触碰，
//     于是「放行」这件事仍然产生副作用与延迟。
//
// 若把两者并列注册（`Use(WhitelistIP())` 后 `Use(BlacklistIP())`），
// gin 无法在中间件内部「跳过后面某个中间件但仍让最终 handler 执行」——
// `c.Abort()` 会把路由 handler 一起停掉。所以唯一干净的做法是：白名单这一段
// **内部持有**黑名单判定，命中白名单时根本不调用它。
// 路由侧因此只需要一条：把 `middleware.BlacklistIP()` 换成
// `middleware.WhitelistIP()`（黑名单行为在未命中白名单时逐字不变）。
//
// ## 与黑名单的语义差异（方向相反，必须相反）
//
//	黑名单：开关默认 false；缓存不可用 → fail-open 放行（宁可漏拦）
//	白名单：没有开关；快照不可用  → 按「零白名单」处理，**不放行任何人**
//
// 白名单是「给特定 IP 免除拦截」的特权，拿不到规则时凭空给出豁免是错的方向；
// 两者最终都落到同一条铁律上：**配置读不出来时，全站行为与未引入该功能时一致**。
func WhitelistIP() gin.HandlerFunc {
	return whitelistHandler(whitelistDeps{
		whitelist: service.DefaultWhitelistCache(),
		blacklist: blacklistHandler(blacklistDeps{
			cache:    service.DefaultBlacklistCache(),
			recorder: defaultBlacklistHitRecorder(),
		}),
	})
}

// WhitelistIPWith 返回使用指定白名单缓存 / 黑名单缓存 / 聚合器 / 数据库的中间件。
//
// db 显式传入而不是走 manager 单例，是为了让「白名单命中不留黑名单命中日志」
// 这条判据能被单测直接驱动（manager.GetManager() 在测试里是 nil）。
func WhitelistIPWith(whitelist *service.WhitelistCache, blacklist *service.BlacklistCache,
	recorder *service.BlacklistHitRecorder, db *gorm.DB) gin.HandlerFunc {
	return whitelistHandler(whitelistDeps{
		whitelist: whitelist,
		blacklist: blacklistHandler(blacklistDeps{cache: blacklist, recorder: recorder, db: db}),
	})
}

// WhitelistIPWithRegionLooker 与 WhitelistIPWith 相同，但可注入地区归属地查表器（BL-4）。
//
// 生产路径不需要显式传（黑名单那段会按 db 懒构造）；测试用它断言
// 「白名单命中时一次归属地读都没有」。
func WhitelistIPWithRegionLooker(whitelist *service.WhitelistCache, blacklist *service.BlacklistCache,
	recorder *service.BlacklistHitRecorder, db *gorm.DB,
	regionLooker service.BlacklistRegionLooker) gin.HandlerFunc {
	return whitelistHandler(whitelistDeps{
		whitelist: whitelist,
		blacklist: blacklistHandler(blacklistDeps{
			cache:        blacklist,
			recorder:     recorder,
			db:           db,
			regionLooker: regionLooker,
		}),
	})
}

type whitelistDeps struct {
	// whitelist 为 nil 视为「零白名单」（不放行任何人）。
	whitelist *service.WhitelistCache
	// blacklist 是**已经装配好的黑名单判定闭包**（blacklistHandler 的返回值）。
	//
	// 以闭包而不是黑名单依赖结构体的形式持有：这样本文件不需要知道
	// blacklistDeps 的任何字段语义，黑名单那套（开关 / fail-open / 地区 / 聚合）
	// 一行都不用改，也不会因为它的内部调整而漂移。
	blacklist gin.HandlerFunc
}

func whitelistHandler(deps whitelistDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := strings.TrimSpace(c.ClientIP())
		// 白名单判定：**无条件先做**，与黑名单的开关无关。
		//
		// 为什么不能先判黑名单开关再判白名单：黑名单关闭时白名单确实无事可做，
		// 但那需要读黑名单缓存（一次 atomic load，虽便宜却把两层的耦合写进代码）。
		// 更重要的是顺序本身就是要被断言的东西 —— 见 TestWhitelistMiddleware
		// 的「同一 IP 同时在黑白名单 → 放行」用例。
		if snapshot := whitelistSnapshot(deps); snapshot != nil {
			if match, hit := snapshot.MatchIP(clientIP); hit {
				if match.Source != "" {
					c.Set(WhitelistContextKey, match.Source)
				}
				// 直接放行：**完全不调用 blacklist 闭包**。
				// 于是黑名单开关、缓存快照、地区归属地查表、命中聚合器
				// 一个都不会被触碰 —— 这也是判据①（不写 blacklist_hits）的机制。
				c.Next()
				return
			}
		}
		// 未命中白名单：交给黑名单判定。它未命中时会自己 c.Next()，
		// 命中时会 AbortWithStatusJSON —— 行为与只挂 BlacklistIP 时逐字一致。
		deps.blacklist(c)
	}
}

// whitelistSnapshot 返回白名单快照；nil 表示「按零白名单处理」。
//
// 缓存未初始化 / 从未成功加载 → 返回 nil（不放行）；生产路径在
// router 装配时由 service.EnsureDefaultWhitelistCacheStarted 同步加载一次。
func whitelistSnapshot(deps whitelistDeps) *service.WhitelistSnapshot {
	if deps.whitelist == nil {
		return nil
	}
	return deps.whitelist.Snapshot()
}

// WhitelistDB 返回当前数据库句柄（供需要显式装配的调用方使用）。
//
// 与 blacklistDeps.database() 同构：manager 未初始化时返回 nil，不 panic。
func WhitelistDB() *gorm.DB {
	mgr := manager.GetManager()
	if mgr == nil {
		return nil
	}
	return mgr.GetDB()
}
