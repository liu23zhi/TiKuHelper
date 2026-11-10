package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 手机号 / 微信黑名单的控制器侧接线（46-黑名单设计.md §3.1，BL-3）。
//
// 判定本体在 service.BlacklistPhoneGate / BlacklistWechatGate（单一实现，
// 语义与 IP 中间件逐条对齐）；本文件只负责两件事：
//
//  1. 从 gin.Context 取判定所需的上下文（数据库、ClientIP、请求路径）；
//  2. **命中时写 403 响应**，且只回 reason、绝不回 Pattern。
//
// 为什么必须在「消费验证码 / 写库建用户」**之前**判定：否则被拉黑的人
//   - 还能消耗掉一条短信验证码与一次图形验证码（都是要花钱的）；
//   - 还能留下半成品记录（例如已消费的 captcha token 行）。
//
// 拉黑判定本身**不产生任何数据库写**（除命中留痕），开关关闭时连读都没有
// —— 见 service.blacklistValueGate 的第一道闸。

// blacklistGateRecorder 是命中的写库聚合器。
//
// 做成包级变量而不是直接调 service.DefaultBlacklistGateHitRecorder()：
// 聚合槽位是**进程级**的（key 只含 entryID/clientIP/action，不含数据库），
// 而控制器的端到端用例每个都新建一个内存库 —— 上一个用例留下的槽位会让
// 下一个用例的「命中」变成对旧 rowID 的 UPDATE，于是新库里一行都看不到。
// 换成每用例一个新聚合器，断言才真的在测「这次命中写了什么」。
var blacklistGateRecorder = func() *service.BlacklistHitRecorder {
	return service.DefaultBlacklistGateHitRecorder()
}

// rejectBlacklistedPhone 判定手机号；命中则写 403 并返回 true（调用方须立刻 return）。
//
// 调用位置（都在消费验证码/写库之前）：
//
//	UserRegister            公开注册
//	UserSMSLogin            短信登录
//	UpdateCurrentUserPhone  登录用户换绑手机（判定**新号**，不是旧号）
func rejectBlacklistedPhone(c *gin.Context, phone string) bool {
	match, hit := service.BlacklistPhoneGateWith(
		service.DefaultBlacklistCache(), blacklistGateRecorder(),
		dao.User.UnderlyingDB(), phone, blacklistGateMeta(c))
	if !hit {
		return false
	}
	respondBlacklistBlocked(c, match)
	return true
}

// rejectBlacklistedWechat 判定微信 OpenID（TrimSpace 后、大小写敏感）。
//
// 微信链路的判定发生在 service 层（OpenID 要等上游解析/扫码回调才知道），
// 那边的命中会以 *service.BlacklistBlockedError 返回；控制器用
// respondBlacklistBlockedError 统一映射成 403。本函数供「客户端已带 wechatId」的
// 场景直接判定（当前仅管理端改绑，走的是 service 层，故此处保留为对称接口）。
func rejectBlacklistedWechat(c *gin.Context, wechatID string) bool {
	match, hit := service.BlacklistWechatGateWith(
		service.DefaultBlacklistCache(), blacklistGateRecorder(),
		dao.User.UnderlyingDB(), wechatID, blacklistGateMeta(c))
	if !hit {
		return false
	}
	respondBlacklistBlocked(c, match)
	return true
}

// blacklistGateMeta 取命中留痕需要的请求上下文。
//
// Path 优先用 c.FullPath()（路由模板，如 /adapter-service/user/register）：
// 真实 URL 里可能带路径参数。拿不到模板时（单测直接调用 handler，没经过路由器）
// 回落到 URL.Path —— 与 IP 中间件写的是同一个字段，排障时两条链路的记录可对齐。
func blacklistGateMeta(c *gin.Context) service.BlacklistGateMeta {
	if c == nil || c.Request == nil {
		return service.BlacklistGateMeta{}
	}
	pathLabel := strings.TrimSpace(c.FullPath())
	if pathLabel == "" {
		pathLabel = strings.TrimSpace(c.Request.URL.Path)
	}
	return service.BlacklistGateMeta{
		ClientIP: strings.TrimSpace(c.ClientIP()),
		Path:     pathLabel,
	}
}

// respondBlacklistBlocked 写 403（只含 reason，不含 Pattern / 网段 / entry id）。
//
// 响应体形态对齐 IP 中间件：`{"code":403,"message":"<reason>"}`。
func respondBlacklistBlocked(c *gin.Context, match service.BlacklistMatch) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"code":    http.StatusForbidden,
		"message": service.BlacklistBlockedMessage(match),
	})
}

// respondBlacklistBlockedError 把 service 层返回的命中错误映射成 403。
//
// 返回 true 表示「这个错误是黑名单命中，已写 403」，调用方须立刻 return；
// 返回 false 表示与黑名单无关，调用方按原有分支处理。
func respondBlacklistBlockedError(c *gin.Context, err error) bool {
	match, blocked := service.ResolveBlacklistBlocked(err)
	if !blocked {
		return false
	}
	respondBlacklistBlocked(c, match)
	return true
}
