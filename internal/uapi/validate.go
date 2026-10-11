package uapi

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"
)

const (
	// SourceCommercial ipinfo 接口唯一合法的 source 取值
	SourceCommercial = "commercial"
	// phoneLength 中国大陆手机号长度
	phoneLength = 11
	// maxIPQueryLength 查询参数最大长度（域名最长 253，IPv6 最长 45）
	maxIPQueryLength = 253
)

var (
	// 11 位、以 1 开头、全为 ASCII 数字
	phonePattern = regexp.MustCompile(`^1[0-9]{10}$`)
	// 域名：字母/数字开头结尾，中间允许字母、数字、连字符、下划线、点
	domainPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$`)
)

// NormalizePhone 校验并规范化手机号：去除首尾空白后必须为 11 位、
// 以 1 开头、全为数字，否则返回包装了 ErrInvalidArgument 的错误。
func NormalizePhone(phone string) (string, error) {
	normalized := strings.TrimSpace(phone)
	if normalized == "" {
		return "", fmt.Errorf("%w: 手机号不能为空", ErrInvalidArgument)
	}
	if len(normalized) != phoneLength || !phonePattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: 手机号必须为 %d 位、以 1 开头的数字", ErrInvalidArgument, phoneLength)
	}
	return normalized, nil
}

// NormalizeIPQuery 校验并规范化 IP 查询参数：去除首尾空白，拒绝空值、
// 超长、含空白或非法字符的输入；允许 IPv4、IPv6 与域名。
// 参数不合法时返回包装了 ErrInvalidArgument 的错误，调用方不得再发起请求。
func NormalizeIPQuery(ip string) (string, error) {
	normalized := strings.TrimSpace(ip)
	if normalized == "" {
		return "", fmt.Errorf("%w: 查询的 IP 不能为空", ErrInvalidArgument)
	}
	if len(normalized) > maxIPQueryLength {
		return "", fmt.Errorf("%w: 查询的 IP 长度不能超过 %d", ErrInvalidArgument, maxIPQueryLength)
	}
	for _, r := range normalized {
		if unicode.IsSpace(r) {
			return "", fmt.Errorf("%w: 查询的 IP 不能包含空白字符", ErrInvalidArgument)
		}
	}
	// IPv4 / IPv6 直接交给标准库判断
	if net.ParseIP(normalized) != nil {
		return normalized, nil
	}
	// 其余按域名处理（不做 DNS 解析，也不限制是否为公网地址）
	if !domainPattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: 查询的 IP 必须为合法的 IPv4/IPv6 或域名", ErrInvalidArgument)
	}
	return normalized, nil
}
