package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	JWTIssuer     = "tiku-adapter"
	JWTAudience   = "tiku-adapter-web"
	JWTTTL        = 24 * time.Hour
	minJWTSecret  = 32
	knownDevValue = "tiku-adapter-dev-secret"
)

var jwtKey []byte

func init() {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	jwtKey = key
}

func ConfigureJWT(secret string) error {
	resolved := strings.TrimSpace(secret)
	if resolved == "" {
		resolved = strings.TrimSpace(os.Getenv("JWT_SECRET"))
	}
	if err := ValidateJWTSecret(resolved); err != nil {
		return err
	}
	jwtKey = []byte(resolved)
	return nil
}

// ValidateJWTSecret validates a JWT signing secret.
func ValidateJWTSecret(secret string) error {
	resolved := strings.TrimSpace(secret)
	if resolved == "" {
		return errors.New("JWT_SECRET is required")
	}
	if resolved == knownDevValue {
		return errors.New("JWT_SECRET must not use the known development value")
	}
	if len([]byte(resolved)) < minJWTSecret {
		return errors.New("JWT_SECRET must be at least 32 bytes")
	}
	return nil
}

// GenerateJWTSecret creates a cryptographically random signing secret.
func GenerateJWTSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateJwt 生成一个jwt
func GenerateJwt(id int32) string {
	return GenerateJwtForUser(id, "")
}

// GenerateJwtForUser binds the token to the current password hash so password
// changes and resets invalidate previously issued sessions.
func GenerateJwtForUser(id int32, passwordHash string) string {
	return GenerateJwtForUserSession(id, passwordHash, "")
}

// GenerateJwtForUserSession also binds the token to the user's current login
// session. A new login replaces this value and invalidates tokens from older
// devices without changing the user's password.
func GenerateJwtForUserSession(id int32, passwordHash, sessionID string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": strconv.Itoa(int(id)),
		"ver": credentialVersion(passwordHash),
		"iss": JWTIssuer,
		"aud": JWTAudience,
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(JWTTTL).Unix(),
		"jti": randomToken(16),
	}
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		claims["sid"] = sessionID
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	jwtStr, err := token.SignedString(jwtKey)
	if err != nil {
		logger.SysLog(err.Error())
		return ""
	}
	return jwtStr
}

// JWTRefreshThreshold 是滑动续期的触发线：剩余寿命少于它才重发令牌。
//
// 取 JWTTTL 的一半（12 小时）。含义：只要用户每 12 小时至少访问一次，
// 令牌就会被持续续期，永远不会掉线；而完全不用满一天的人才会真正过期。
const JWTRefreshThreshold = JWTTTL / 2

// RefreshJwtIfNeeded 判断一枚已通过校验的令牌是否需要续期，需要则返回新令牌。
//
// 入参必须是**已经验签成功**的令牌：本函数只负责读 exp 并决定要不要重发，
// 不重复做签名/issuer/audience 校验（那些在 ParseJwtWithClaims 里已做完）。
// 若把未校驗的令牌喂进来，等于绕过鉴权，所以调用方只能在校验之后调用。
//
// 返回值语义："" = 不需要续期（或无法续期），调用方据此跳过 Set-Cookie。
func RefreshJwtIfNeeded(rawToken string, userID int32, passwordHash, sessionID string) string {
	claims, err := ParseJwtWithClaims(rawToken)
	if err != nil {
		// 解析失败不代表该续期；让上层原有的鉴权结果说话，这里静默跳过。
		return ""
	}
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		// 没有 exp 的令牌一律不续：给它续期等于把「永不过期」的令牌固化下来。
		return ""
	}
	remaining := time.Until(expiresAt.Time)
	if remaining > JWTRefreshThreshold {
		return ""
	}
	// 已过期就不续 —— 那种情况上层鉴权本来就该失败，续期会掩盖真实过期。
	if remaining <= 0 {
		return ""
	}
	return GenerateJwtForUserSession(userID, passwordHash, sessionID)
}

func credentialVersion(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

// CredentialVersion returns the session version for a stored password hash.
func CredentialVersion(passwordHash string) string {
	return credentialVersion(passwordHash)
}

// ParseJwtWithClaims 解析 jwt 字符串，返回 Claims 对象
func ParseJwtWithClaims(jwtStr string, options ...jwt.ParserOption) (jwt.Claims, error) {
	mc := jwt.MapClaims{}
	parserOptions := append([]jwt.ParserOption{}, options...)
	parserOptions = append(parserOptions,
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(JWTIssuer),
		jwt.WithAudience(JWTAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	token, err := jwt.ParseWithClaims(jwtStr, mc, func(token *jwt.Token) (interface{}, error) {
		if token.Method == nil || token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected jwt signing method")
		}
		return jwtKey, nil
	}, parserOptions...)
	if err != nil {
		return nil, err
	}
	// 校验 Claims 对象是否有效，基于 exp（过期时间），nbf（不早于），iat（签发时间）等进行判断（如果有这些声明的话）。
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return token.Claims, nil
}

func randomToken(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
