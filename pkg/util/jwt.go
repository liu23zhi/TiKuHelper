package util

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"os"
	"strconv"
	"strings"
)

const defaultJWTSecret = "tiku-adapter-dev-secret"

var jwtKey []byte

func init() {
	if err := ConfigureJWT(""); err != nil {
		panic(err)
	}
}

func ConfigureJWT(secret string) error {
	resolved := strings.TrimSpace(secret)
	if resolved == "" {
		resolved = strings.TrimSpace(os.Getenv("JWT_SECRET"))
	}
	if resolved == "" {
		resolved = defaultJWTSecret
	}
	jwtKey = []byte(resolved)
	return nil
}

// GenerateJwt 生成一个jwt
func GenerateJwt(id int32) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": strconv.Itoa(int(id)),
	})
	jwtStr, err := token.SignedString(jwtKey)
	if err != nil {
		logger.SysLog(err.Error())
		return ""
	}
	return jwtStr
}

// ParseJwtWithClaims 解析 jwt 字符串，返回 Claims 对象
func ParseJwtWithClaims(jwtStr string, options ...jwt.ParserOption) (jwt.Claims, error) {
	mc := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(jwtStr, mc, func(token *jwt.Token) (interface{}, error) {
		return jwtKey, nil
	}, options...)
	if err != nil {
		return nil, err
	}
	// 校验 Claims 对象是否有效，基于 exp（过期时间），nbf（不早于），iat（签发时间）等进行判断（如果有这些声明的话）。
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return token.Claims, nil
}
