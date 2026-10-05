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
	if resolved == "" {
		return errors.New("JWT_SECRET is required")
	}
	if resolved == knownDevValue {
		return errors.New("JWT_SECRET must not use the known development value")
	}
	if len([]byte(resolved)) < minJWTSecret {
		return errors.New("JWT_SECRET must be at least 32 bytes")
	}
	jwtKey = []byte(resolved)
	return nil
}

// GenerateJwt 生成一个jwt
func GenerateJwt(id int32) string {
	return GenerateJwtForUser(id, "")
}

// GenerateJwtForUser binds the token to the current password hash so password
// changes and resets invalidate previously issued sessions.
func GenerateJwtForUser(id int32, passwordHash string) string {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": strconv.Itoa(int(id)),
		"ver": credentialVersion(passwordHash),
		"iss": JWTIssuer,
		"aud": JWTAudience,
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(JWTTTL).Unix(),
		"jti": randomToken(16),
	})
	jwtStr, err := token.SignedString(jwtKey)
	if err != nil {
		logger.SysLog(err.Error())
		return ""
	}
	return jwtStr
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
