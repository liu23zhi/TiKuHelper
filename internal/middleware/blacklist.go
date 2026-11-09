package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// BlacklistIP 是 IP 黑名单中间件（46-黑名单设计.md §3）。
//
// 处置：取 c.ClientIP() → 查进程内缓存 → 命中则 403。
//
// 四条不可协商的性质：
//
//  1. **开关默认 false**：`blacklist_enabled` 读不到（缺表/缺记录/解析失败）一律按 false。
//     关闭时本中间件**零查询、零写日志**直接 c.Next()，全站行为与未引入黑名单时完全一致。
//  2. **fail-open**：缓存不可用（从未成功加载）时**放行**并打 SysWarn，
//     同时写一条 action='logged_only' 的命中记录留痕。这与赠金判定的 fail-closed 相反：
//     发的是钱 → 宁可误拒；拦的是服务 → 查不到就放行（46-*.md §3.4）。
//  3. **管理员豁免**：选「路径豁免」而不是 isAdminUser 豁免，
//     理由见 BlacklistExemptPath 的注释（核心：自锁时管理员还没登录，拿不到 user）。
//  4. **响应体不泄漏规则细节**：只回
//     `{"code":403,"message":"当前访问受限，如有疑问请联系管理员"}`，
//     绝不回「你的 IP 在 203.0.113.0/24 里」—— 那等于告诉攻击者规则的精确边界。
//
// 命中日志走 60 秒窗口聚合（service.BlacklistHitRecorder）：同一 entry + 同一 IP
// 在窗口内只落一行并累加 hit_count，被刷 1000 次不会产生 1000 行。
//
// 挂载位置见 internal/api/router.go：必须在 middleware.Auth **之后**、业务 handler **之前**，
// 且挂在 /adapter-service 的 API 链而不是全局链，理由写在 router.go 的挂载点注释里。
func BlacklistIP() gin.HandlerFunc {
	return blacklistHandler(blacklistDeps{
		cache:    service.DefaultBlacklistCache(),
		recorder: defaultBlacklistHitRecorder(),
	})
}

// BlacklistIPWith 返回使用指定缓存/聚合器/数据库的中间件（测试与显式装配用）。
//
// db 显式传入而不是走 manager 单例，是为了让「命中了要写日志」这条路径能被
// 单测直接驱动（manager.GetManager() 在测试里是 nil）。
func BlacklistIPWith(cache *service.BlacklistCache, recorder *service.BlacklistHitRecorder, db *gorm.DB) gin.HandlerFunc {
	return blacklistHandler(blacklistDeps{cache: cache, recorder: recorder, db: db})
}

type blacklistDeps struct {
	cache    *service.BlacklistCache
	recorder *service.BlacklistHitRecorder
	// db 为 nil 时回落到 manager 单例（生产路径）。
	db *gorm.DB
}

func (d blacklistDeps) database() *gorm.DB {
	if d.db != nil {
		return d.db
	}
	mgr := manager.GetManager()
	if mgr == nil {
		return nil
	}
	return mgr.GetDB()
}

func blacklistHandler(deps blacklistDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 第一道闸：开关。关闭（默认）时零查询、零写日志。
		if deps.cache == nil || !deps.cache.Enabled() {
			c.Next()
			return
		}
		if BlacklistExemptPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		clientIP := strings.TrimSpace(c.ClientIP())

		snapshot := deps.cache.Snapshot()
		if snapshot == nil {
			// 开关为真却没有快照：不该发生的状态。按 fail-open 处理并留痕，绝不因此拦人。
			blacklistFailOpen(c, deps, clientIP, "黑名单缓存快照缺失")
			c.Next()
			return
		}

		match, hit := snapshot.MatchIP(clientIP)
		if !hit {
			c.Next()
			return
		}

		// 先写日志再拒绝：若先 Abort 再写，写库失败时请求已经被拒，
		// 用户来投诉仍然查不到记录。
		recordBlacklistHit(deps, entity.BlacklistHit{
			EntryID:     match.EntryID,
			Kind:        match.Kind,
			ValueMasked: match.ValueMasked,
			ClientIP:    clientIP,
			Path:        c.Request.URL.Path,
			Action:      service.BlacklistActionBlocked,
		})

		// 响应体严格按 46-*.md §3.2：不泄漏规则细节（不含 kind / pattern / 网段）。
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"code":    http.StatusForbidden,
			"message": "当前访问受限，如有疑问请联系管理员",
		})
	}
}

// blacklistFailOpen 是 fail-open 的统一出口：打 SysWarn + 写 logged_only 留痕 + 由调用方放行。
//
// 抽出来是为了让「fail-open 必须留痕」（46-*.md §3.4）只有一处实现，
// 不会在某个分支上漏掉留痕。
func blacklistFailOpen(c *gin.Context, deps blacklistDeps, clientIP string, reason string) {
	logger.SysWarn("黑名单判定降级放行（fail-open）：" + reason)
	recordBlacklistHit(deps, entity.BlacklistHit{
		EntryID:     0,
		Kind:        service.BlacklistKindIP,
		ValueMasked: service.MaskBlacklistValue(service.BlacklistKindIP, clientIP, service.BlacklistMatchExact),
		ClientIP:    clientIP,
		Path:        c.Request.URL.Path,
		Action:      service.BlacklistActionLoggedOnly,
	})
}

// recordBlacklistHit 写命中日志；失败只记日志，绝不影响请求处置。
func recordBlacklistHit(deps blacklistDeps, hit entity.BlacklistHit) {
	if deps.recorder == nil {
		return
	}
	db := deps.database()
	if db == nil {
		return
	}
	if err := deps.recorder.Record(db, hit); err != nil {
		logger.SysWarn("写入黑名单命中日志失败（不影响本次请求处置）：" + err.Error())
	}
}

// blacklistHitRecorderSingleton 是进程级聚合器（槽位表跨请求共享，必须唯一）。
var blacklistHitRecorderSingleton *service.BlacklistHitRecorder

func defaultBlacklistHitRecorder() *service.BlacklistHitRecorder {
	if blacklistHitRecorderSingleton == nil {
		blacklistHitRecorderSingleton = service.NewBlacklistHitRecorder()
	}
	return blacklistHitRecorderSingleton
}

// BlacklistExemptPath 判断路径是否豁免 IP 黑名单（46-*.md §3.3 防自锁）。
//
// **选了「路径豁免」而不是「isAdminUser 豁免」**，理由：
//
//   - 自锁场景是「管理员把一个 CIDR 拉黑，自己也在里面」。此时他**还没登录**，
//     Auth 拿不到 user，isAdminUser 无从判断 —— 只有路径豁免能救。
//   - 登录页本身必须可达（46-*.md §3.1 与 §8「被拉黑用户无法登录 → 无法提工单申诉」）。
//   - isAdminUser 豁免还有隐患：管理员会话被冒用时，豁免范围会跟着会话走。
//
// 豁免范围只覆盖「管理接口」与「登录入口」，不覆盖业务接口与公开注册：
// 运营要能进后台解封，但业务请求该拦还是拦。
// 「保存规则时命中当前操作者 IP 的二次确认」（§3.3 第 3 条）属管理页 BL-5，本期不做。
func BlacklistExemptPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	// 去掉 query 与结尾斜杠，避免 `/adapter-service/user/login/` 绕过豁免表。
	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return false
	}

	// 1) 管理接口：/adapter-service/admin、/adapter-service/admin/*，
	//    以及 /adapter-service/<段>/admin/*（覆盖 /adapter-service/admin/userexam/* 那组）。
	//    ⚠️ 必须按「路径段」判定而不是裸前缀：`/adapter-service/administrator` 不是管理接口，
	//    裸 HasPrefix 会把它一起豁免（实测踩到过）。
	if isBlacklistAdminSegment(trimmed) {
		return true
	}

	// 2) 登录入口：管理员被自己的 CIDR 规则命中后必须还能登进来。
	switch trimmed {
	case "/adapter-service/user/login", "/adapter-service/user/login/sms":
		return true
	}
	return false
}

// isBlacklistAdminSegment 判断路径是否落在管理接口段内。
//
// 按**路径段**比较，不做裸字符串前缀比较：`/adapter-service/administrator`
// 不是管理接口，裸 HasPrefix("/adapter-service/admin") 会把它误豁免
// （这与「不要用字符串前缀判断 IP 段」是同一条纪律的另一种形态）。
func isBlacklistAdminSegment(path string) bool {
	const prefix = "/adapter-service/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(segments) == 0 || segments[0] == "" {
		return false
	}
	// /adapter-service/admin、/adapter-service/admin/*。
	if segments[0] == "admin" {
		return true
	}
	// /adapter-service/<段>/admin[/...]：与 /adapter-service/admin/userexam/* 同组的管理接口。
	for _, segment := range segments[1:] {
		if segment == "admin" {
			return true
		}
	}
	return false
}
