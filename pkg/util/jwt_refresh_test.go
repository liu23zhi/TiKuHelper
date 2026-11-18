package util

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 滑动续期的回归钉子（2026-11-19）。
//
// 背景：JWTTTL 是 24 小时且没有 refresh 接口，用户正常每天使用也会在登录满
// 24 小时那一刻被踢回登录页，表现为「莫名其妙掉登录」。修法是每次鉴权通过后，
// 若令牌剩余寿命不足一半就重发一次。

// 剩余寿命充足时不得续期 —— 否则每个请求都会写 Set-Cookie。
func TestRefreshJwtSkipsWhenRemainingIsLong(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	if err := ConfigureJWT(secret); err != nil {
		t.Fatalf("configure: %v", err)
	}
	token := GenerateJwtForUserSession(7, "hash-a", "sid-a")

	if got := RefreshJwtIfNeeded(token, 7, "hash-a", "sid-a"); got != "" {
		t.Fatalf("刚签发的令牌不该续期，实际返回了新令牌")
	}
}

// 剩余寿命少于一半时必须续期，且新令牌仍绑定同一 sid 与 credential version。
func TestRefreshJwtRenewsWhenPastHalfLife(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	if err := ConfigureJWT(secret); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// 直接构造「已走过 3/4 寿命」的令牌：剩余 6 小时 < 阈值 12 小时。
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": "7",
		"ver": credentialVersion("hash-a"),
		"iss": JWTIssuer,
		"aud": JWTAudience,
		"iat": now.Add(-JWTTTL * 3 / 4).Unix(),
		"nbf": now.Add(-JWTTTL * 3 / 4).Unix(),
		"exp": now.Add(JWTTTL / 4).Unix(),
		"jti": randomToken(16),
		"sid": "sid-a",
	}
	old, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	fresh := RefreshJwtIfNeeded(old, 7, "hash-a", "sid-a")
	if fresh == "" {
		t.Fatal("剩余寿命不足一半时必须续期，实际没有续")
	}
	if fresh == old {
		t.Fatal("续期必须签发新令牌，不能原样返回旧令牌")
	}

	parsed, err := ParseJwtWithClaims(fresh)
	if err != nil {
		t.Fatalf("新令牌必须能通过校验: %v", err)
	}
	// 关键：续期不能悄悄改变会话绑定，否则等于绕过单设备登录。
	if sid, _ := parsed.GetSubject(); sid != "7" {
		t.Fatalf("sub 必须保持为 7，实际 %q", sid)
	}
	// ParseJwtWithClaims 返回的是接口类型，取值要断言成 jwt.MapClaims。
	mapClaims, ok := parsed.(jwt.MapClaims)
	if !ok {
		t.Fatalf("期望 MapClaims，实际 %T", parsed)
	}
	if v, has := mapClaims["sid"]; !has || v != "sid-a" {
		t.Fatalf("sid 必须保持为 sid-a，实际 %v", v)
	}
	// 新令牌的寿命必须重新拉满（剩余 > 阈值），否则下次请求会重复续期。
	exp, err := parsed.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatalf("新令牌必须有 exp: %v", err)
	}
	if remain := time.Until(exp.Time); remain <= JWTRefreshThreshold {
		t.Fatalf("新令牌寿命必须重新拉满，实际剩余 %v", remain)
	}
}

// 已过期的令牌不续期 —— 否则会把「本该失效」的会话永久续下去。
func TestRefreshJwtDoesNotReviveExpiredToken(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	if err := ConfigureJWT(secret); err != nil {
		t.Fatalf("configure: %v", err)
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": "7", "iss": JWTIssuer, "aud": JWTAudience,
		"iat": now.Add(-2 * time.Hour).Unix(),
		"nbf": now.Add(-2 * time.Hour).Unix(),
		"exp": now.Add(-time.Minute).Unix(),
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if got := RefreshJwtIfNeeded(expired, 7, "hash-a", "sid-a"); got != "" {
		t.Fatal("已过期的令牌不得续期")
	}
}
