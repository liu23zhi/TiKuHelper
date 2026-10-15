package uapi

import (
	"encoding/json"
	"strconv"
	"strings"
)

// PhoneInfo 手机归属地查询结果（GET https://uapis.cn/api/v1/misc/phoneinfo）
type PhoneInfo struct {
	City     string `json:"city"`      // 城市
	Province string `json:"province"`  // 省份
	SP       string `json:"sp"`        // 运营商
	AreaCode string `json:"area_code"` // 区号
	// RawJSON 本次 200 响应的完整原始 body（原样不改写，不参与序列化）。
	// 官方文档未列出的字段需要由上层持久化，避免丢数据。
	RawJSON string `json:"-"`
}

// IPInfo IP 查询结果（GET https://uapis.cn/api/v1/network/ipinfo）
//
// ASN 统一用字符串承载（接口可能返回数字或字符串）；
// Latitude/Longitude 用 float64（接口可能返回数字或数字字符串）；
// source=commercial 时官方文档说明会返回更完整的地理位置信息，
// 其具体字段名未公开，统一由 RawJSON 保留。
type IPInfo struct {
	IP        string  `json:"ip"`        // 查询的 IP
	Region    string  `json:"region"`    // 地区
	ISP       string  `json:"isp"`       // 运营商
	LLC       string  `json:"llc"`       // 运营商（公司主体）
	ASN       string  `json:"asn"`       // ASN
	Latitude  float64 `json:"latitude"`  // 纬度
	Longitude float64 `json:"longitude"` // 经度
	BeginIP   string  `json:"beginip"`   // 网段起始 IP
	EndIP     string  `json:"endip"`     // 网段结束 IP
	// RawJSON 本次 200 响应的完整原始 body（原样不改写，不参与序列化）。
	RawJSON string `json:"-"`
}

// decodePhoneInfo 解析手机归属地 200 响应，同时保留完整原始 body。
// 响应字段用 map 逐字段容错读取，多余的未知字段不影响解析。
func decodePhoneInfo(body []byte) (PhoneInfo, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return PhoneInfo{}, err
	}
	return PhoneInfo{
		City:     rawString(fields["city"]),
		Province: rawString(fields["province"]),
		SP:       rawString(fields["sp"]),
		AreaCode: rawString(fields["area_code"]),
		RawJSON:  string(body),
	}, nil
}

// ParsePhoneInfo 解析并保留手机号归属地响应，供 HTTP 调用和持久化缓存复用。
func ParsePhoneInfo(body []byte) (PhoneInfo, error) {
	return decodePhoneInfo(body)
}

// decodeIPInfo 解析 IP 查询 200 响应，同时保留完整原始 body。
func decodeIPInfo(body []byte) (IPInfo, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return IPInfo{}, err
	}
	return IPInfo{
		IP:        rawString(fields["ip"]),
		Region:    rawString(fields["region"]),
		ISP:       rawString(fields["isp"]),
		LLC:       rawString(fields["llc"]),
		ASN:       rawString(fields["asn"]),
		Latitude:  rawFloat(fields["latitude"]),
		Longitude: rawFloat(fields["longitude"]),
		BeginIP:   rawString(fields["beginip"]),
		EndIP:     rawString(fields["endip"]),
		RawJSON:   string(body),
	}, nil
}

// ParseIPInfo 解析并保留 IP 归属地响应，供 HTTP 调用和持久化缓存复用。
func ParseIPInfo(body []byte) (IPInfo, error) {
	return decodeIPInfo(body)
}

// rawString 容错取字符串：JSON 字符串原样返回，数字转字符串，其它类型返回空串。
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// rawFloat 容错取浮点数：兼容数字与数字字符串两种写法。
func rawFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return v
		}
	}
	return 0
}
