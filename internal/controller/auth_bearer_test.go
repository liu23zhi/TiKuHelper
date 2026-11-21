package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 本文件钉住「controller.Auth 同时接受裸 token 与 Bearer 前缀」这条兼容契约
// （2026-11-18 用户需求）。
//
// # 为什么需要兼容
//
// 仓内原本有**两条**鉴权链，对 `Bearer ` 的处理相反：
//
//	controller.Auth（Web UI JWT）      → 不剥离，整头交给 ParseJwtWithClaims
//	AuthAccessToken → parseBasicToken  → 剥离（strings.Fields 拆空格取第 2 段）
//
// 于是「带 Bearer 的 JWT 打 /api/scripts」必然 401，报
//
//	token is malformed: could not base64 decode header: illegal base64 data at input byte 6
//
// ——`"Bearer "` 恰好 7 字节，下标 6 是 `r`，不是合法 base64。
//
// 这个错误信息指向「token 格式坏」，与真实原因（多了个前缀）很容易看岔，
// 2026-11-17 排查 401 时就栽在这里。现在让 Auth 也接受带前缀的形式。
//
// # 兼容不等于放松
//
// 三个必须同时成立的判据（缺一条就是安全缺陷）：
//
//  1. 裸 token 仍然可用（现有前端/Cookie 路径完全不受影响）
//  2. `Bearer <token>` 也可用（本次新增）
//  3. **非法前缀仍然被拒绝** —— 不能写成「按空格切最后一段」，
//     那会让 `Whatever <token>`、`Basic <token>` 也通过。
//     只认 `Bearer`（大小写不敏感，与 HTTP 头惯例一致），其余一律拒绝。

// authTestRouter 组一条只有 Auth 的路由，返回 recorder。
func authTestRouter(t *testing.T) (*gin.Engine, func(authorization string) *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/probe", Auth, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "reached"})
	})
	return router, func(authorization string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		router.ServeHTTP(rec, req)
		return rec
	}
}

// seedAuthSessionUser 造一个带 login_session_id 的用户，返回其裸 JWT。
//
// 用真实的 util.GenerateJwtForUserSession，不手搓 claim：
// 这样测试同时验证了「签发出的 token 能被 Auth 接受」这条真实契约。
func seedAuthSessionUser(t *testing.T, db *gorm.DB, username string) (entity.User, string) {
	t.Helper()
	user := entity.User{
		Username:       username,
		Password:       "synthetic-password-hash",
		Status:         "enabled",
		LoginSessionID: "session-" + username,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user, util.GenerateJwtForUserSession(user.ID, user.Password, user.LoginSessionID)
}

// TestAuthAcceptsBareAndBearerToken 是主判据。
func TestAuthAcceptsBareAndBearerToken(t *testing.T) {
	db := newUserControllerTestDB(t)
	_, jwt := seedAuthSessionUser(t, db, "auth-both-forms")
	_, do := authTestRouter(t)

	cases := []struct {
		name          string
		authorization string
		wantOK        bool
	}{
		// 判据 1：裸 token（现有前端与 Cookie 路径的形态）
		{name: "裸 token", authorization: jwt, wantOK: true},
		// 判据 2：Bearer 前缀（本次新增的兼容）
		{name: "Bearer + token", authorization: "Bearer " + jwt, wantOK: true},
		// HTTP 头方案名大小写不敏感是惯例（RFC 7235 的 scheme 是 case-insensitive）
		{name: "bearer 小写", authorization: "bearer " + jwt, wantOK: true},
		{name: "BEARER 大写", authorization: "BEARER " + jwt, wantOK: true},
		// 头值前后的空白必须容忍：不同客户端/代理会有
		{name: "Bearer 前导空白", authorization: "  Bearer " + jwt, wantOK: true},
		// 裸 token 带前后空白也要容忍。这一条与上一条走的是**不同分支**：
		// 上一条 `strings.Fields` 就得 2 段、根本不经过 TrimSpace；
		// 这一条是 1 段，只有 TrimSpace 能把多余的空白去掉。
		// 变异「去掉 TrimSpace」只有这一条会红 —— 2026-11-18 实测确认。
		{name: "裸 token 带前后空白", authorization: "  " + jwt + "  ", wantOK: true},
		// 判据 3：**非法前缀必须拒绝**。这一组是安全边界，
		// 没有它，「按空格取最后一段」的粗暴实现也能让前两条绿。
		{name: "Basic 前缀必须拒绝", authorization: "Basic " + jwt, wantOK: false},
		{name: "任意单词前缀必须拒绝", authorization: "Whatever " + jwt, wantOK: false},
		{name: "空头必须拒绝", authorization: "", wantOK: false},
		{name: "只有 Bearer 没有 token 必须拒绝", authorization: "Bearer ", wantOK: false},
		{name: "垃圾串必须拒绝", authorization: "not-a-jwt", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(tc.authorization)
			if tc.wantOK {
				if rec.Code != http.StatusOK {
					t.Fatalf("应放行，实际 %d body=%s", rec.Code, rec.Body.String())
				}
				var payload map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["message"] != "reached" {
					t.Fatalf("必须真正走到 handler，实际 body=%s", rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("应 401 拒绝，实际 %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuthStillAcceptsCookieToken 是回归对照：Cookie 路径存的是裸 token，
// 本次改动不能把它弄坏。
//
// 为什么单独一条：Auth 的 header 与 cookie 是两个来源，剥离逻辑只该作用于
// header。若实现成「对 tokenValue 统一剥离」，cookie 里的 token 恰好以
// `Bearer` 开头时（理论上可能）会被误剥 —— 这条用例固定住 cookie 通路。
func TestAuthStillAcceptsCookieToken(t *testing.T) {
	db := newUserControllerTestDB(t)
	_, jwt := seedAuthSessionUser(t, db, "auth-cookie-form")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/probe", Auth, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "reached"})
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: jwt})
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Cookie 里的裸 token 必须仍被接受，实际 %d body=%s", rec.Code, rec.Body.String())
	}
}
