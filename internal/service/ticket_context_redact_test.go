package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件钉住 Context 脱敏的**语义边界**：区分「凭据」与「编码」。
//
// 历史缺陷（本次修复的目标）：ticketContextSecretKeyFragments 里含裸 "code"，
// 而匹配用 strings.Contains 子串匹配，于是 error_code / status_code / area_code /
// payment_code 全被抹成 [已隐藏] —— 而这些恰恰是管理员排查系统单最需要的字段。
//
// 判据设计原则：**每个用例都同时断言"该脱的脱了"和"该留的留着"**。
// 只断言一侧会漏掉两类反向错误：把编码也脱掉（信息丢失）、把凭据也保留（泄露）。

// decodeRedactedContext 跑真实脱敏 + 真实序列化，再解回 map 供断言。
//
// 不手搓 struct 自比：这里走的是 marshalTicketContext —— 生产落库的同一条路径
// （脱敏 → JSON 序列化 → 体积校验）。若哪天有人绕过 marshalTicketContext，
// 本文件测不到；但那是另一层的问题，不靠这里的断言兜。
func decodeRedactedContext(t *testing.T, raw map[string]interface{}) map[string]interface{} {
	t.Helper()
	payload, err := marshalTicketContext(raw)
	if err != nil {
		t.Fatalf("marshalTicketContext: %v", err)
	}
	if payload == "" {
		t.Fatal("marshalTicketContext 不得返回空串（输入非空）")
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("落库内容是非法 JSON：%v / payload=%s", err, payload)
	}
	return decoded
}

// assertRedacted 断言某个键的值被整段替换。
func assertRedacted(t *testing.T, decoded map[string]interface{}, key string) {
	t.Helper()
	if got := decoded[key]; got != ticketContextRedacted {
		t.Fatalf("键 %q 必须被脱敏成 %q，实际 %#v", key, ticketContextRedacted, got)
	}
}

// assertPreserved 断言某个键的值**原样保留**。
func assertPreserved(t *testing.T, decoded map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, ok := decoded[key]
	if !ok {
		t.Fatalf("键 %q 不应被删除（脱敏只替换值，不改结构）", key)
	}
	// JSON 数字统一解成 float64，比较时归一。
	if wantNumber, isNumber := want.(int); isNumber {
		number, isFloat := got.(float64)
		if !isFloat || number != float64(wantNumber) {
			t.Fatalf("键 %q 必须保留原值 %v，实际 %#v", key, want, got)
		}
		return
	}
	if got != want {
		t.Fatalf("键 %q 必须保留原值 %#v，实际 %#v", key, want, got)
	}
}

// ---------------------------------------------------------------------------
// 第 1 组：四个编码类键必须保留（本次修复的核心目标）
// ---------------------------------------------------------------------------

func TestTicketContextKeepsCodeLikeFields(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"error_code":   "UAPI_TIMEOUT",
		"status_code":  500,
		"area_code":    "442000",
		"payment_code": "alipay",
	})
	// 这四项曾全部被抹成 [已隐藏]，是本缺陷的直接复现点。
	assertPreserved(t, decoded, "error_code", "UAPI_TIMEOUT")
	assertPreserved(t, decoded, "status_code", 500)
	assertPreserved(t, decoded, "area_code", "442000")
	assertPreserved(t, decoded, "payment_code", "alipay")
}

// ---------------------------------------------------------------------------
// 第 2 组：真凭据键必须继续脱敏（不能因为修 error_code 而漏掉它们）
// ---------------------------------------------------------------------------

func TestTicketContextRedactsCredentialCodeKeys(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"code":              "123456",
		"sms_code":          "123456",
		"otp":               "654321",
		"verify_code":       "abcdef",
		"verification_code": "abcdef",
		"captcha_code":      "XY7Z",
		"backup_code":       "8f2a-9c1b",
		"recovery_code":     "rec-0001",
		"auth_code":         "auth-xyz",
		"access_code":       "acc-xyz",
	})
	for _, key := range []string{
		"code", "sms_code", "otp", "verify_code", "verification_code",
		"captcha_code", "backup_code", "recovery_code", "auth_code", "access_code",
	} {
		assertRedacted(t, decoded, key)
	}
}

// ---------------------------------------------------------------------------
// 第 3 组：password / token 等子串类凭据仍走子串匹配（回归保护）
// ---------------------------------------------------------------------------

func TestTicketContextRedactsFragmentCredentials(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"password":      "p@ss",
		"db_password":   "p@ss",
		"token":         "abc",
		"access_token":  "abc",
		"client_secret": "shh",
		"authorization": "Bearer x",
		"cookie":        "sid=1",
		"credential":    "cred",
		"密码":            "p@ss",
		"令牌":            "abc",
	})
	for _, key := range []string{
		"password", "db_password", "token", "access_token", "client_secret",
		"authorization", "cookie", "credential", "密码", "令牌",
	} {
		assertRedacted(t, decoded, key)
	}
}

// ---------------------------------------------------------------------------
// 第 4 组：手机号仍只留后四位（且不能被当成 code 类处理）
// ---------------------------------------------------------------------------

func TestTicketContextMasksPhoneToLastFour(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"phone":     "13800138000",
		"手机号":       "13900139000",
		"mobile":    "13700137000",
		"tel":       "13600136000",
		"电话":        "13500135000",
		"area_code": "442000", // 同组对照：area_code 不是手机号、也不是凭据
	})
	for _, key := range []string{"phone", "手机号", "mobile", "tel", "电话"} {
		got, ok := decoded[key].(string)
		if !ok {
			t.Fatalf("键 %q 必须是字符串，实际 %#v", key, decoded[key])
		}
		if !strings.HasPrefix(got, "****") || len([]rune(got)) != 8 {
			t.Fatalf("键 %q 必须只留后四位（****XXXX），实际 %q", key, got)
		}
	}
	// 反向保护：area_code 含 "code" 但既不是凭据也不是手机号，必须原样保留。
	assertPreserved(t, decoded, "area_code", "442000")
}

// ---------------------------------------------------------------------------
// 第 5 组：反向保护 —— code 与 error_code 在同一 map 里各自成立
// ---------------------------------------------------------------------------

func TestTicketContextCodeAndErrorCodeCoexist(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"code":          "123456",       // 裸 code = 凭据 → 脱敏
		"error_code":    "UAPI_TIMEOUT", // 编码 → 保留
		"sms_code":      "123456",       // 凭据 → 脱敏
		"status_code":   500,            // 编码 → 保留
		"failureReason": "uapi timeout", // 普通文本 → 保留
	})
	// 这是本次修复最关键的一条：同一个 map、同一批 code 结尾的键，
	// 必须按语义分开处理，而不是一刀切。
	assertRedacted(t, decoded, "code")
	assertRedacted(t, decoded, "sms_code")
	assertPreserved(t, decoded, "error_code", "UAPI_TIMEOUT")
	assertPreserved(t, decoded, "status_code", 500)
	assertPreserved(t, decoded, "failureReason", "uapi timeout")
}

// ---------------------------------------------------------------------------
// 第 6 组：嵌套结构 —— 错误上下文里的 code 是错误码（保留）
// ---------------------------------------------------------------------------

func TestTicketContextNestedErrorCodeIsPreserved(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "UAPI_TIMEOUT", // 父键 error → 错误码，保留
			"message": "upstream timeout",
		},
		"response": map[string]interface{}{
			"code":    "OK", // 父键 response → 响应码，保留
			"message": "success",
		},
	})
	for _, parent := range []string{"error", "response"} {
		nested, ok := decoded[parent].(map[string]interface{})
		if !ok {
			t.Fatalf("%q 必须是嵌套对象，实际 %#v", parent, decoded[parent])
		}
		if _, ok := nested["code"]; !ok {
			t.Fatalf("%q.code 不应被删除", parent)
		}
		if nested["code"] == ticketContextRedacted {
			t.Fatalf("%q 上下文里的 code 是错误/响应码，必须保留，实际被脱敏", parent)
		}
		if !strings.Contains(parent, "error") && nested["message"] == ticketContextRedacted {
			t.Fatalf("%q.message 不是凭据，必须保留", parent)
		}
	}
	// 具体值也要核，防止「留着键但值是空的」这种假通过。
	errorNode := decoded["error"].(map[string]interface{})
	if errorNode["code"] != "UAPI_TIMEOUT" {
		t.Fatalf("error.code = %#v，want UAPI_TIMEOUT", errorNode["code"])
	}
}

// ---------------------------------------------------------------------------
// 第 7 组：嵌套结构 —— 非错误上下文里的 code 仍是凭据（脱敏）
// ---------------------------------------------------------------------------

func TestTicketContextNestedCredentialCodeIsRedacted(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"wechat": map[string]interface{}{
			"code": "081AbCdEf", // 父键 wechat → 微信授权码，凭据 → 脱敏
		},
		"callback": map[string]interface{}{
			"code":     "123456", // 父键 callback → 回调验证码，凭据 → 脱敏
			"sms_code": "123456",
		},
		// 错误容器里夹带真凭据：不能因为父键是 error 就整片放行。
		"error_detail": map[string]interface{}{
			"sms_code": "123456",
		},
	})
	wechat := decoded["wechat"].(map[string]interface{})
	if wechat["code"] != ticketContextRedacted {
		t.Fatalf("wechat.code 是授权码，必须脱敏，实际 %#v", wechat["code"])
	}
	callback := decoded["callback"].(map[string]interface{})
	if callback["code"] != ticketContextRedacted {
		t.Fatalf("callback.code 是验证码，必须脱敏，实际 %#v", callback["code"])
	}
	if callback["sms_code"] != ticketContextRedacted {
		t.Fatalf("callback.sms_code 必须脱敏，实际 %#v", callback["sms_code"])
	}
	// 关键对照：错误容器里的 sms_code 仍然脱敏（父键只对裸 code 放行）。
	detail := decoded["error_detail"].(map[string]interface{})
	if detail["sms_code"] != ticketContextRedacted {
		t.Fatalf("即使父键是错误容器，sms_code 仍是凭据必须脱敏，实际 %#v", detail["sms_code"])
	}
}

// ---------------------------------------------------------------------------
// 第 8 组：数组与多层嵌套（递归不能漏、也不能把结构压平）
// ---------------------------------------------------------------------------

func TestTicketContextNestedArraysAndDeepMaps(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"attempts": []interface{}{
			map[string]interface{}{"error_code": "E1", "phone": "13800138000"},
			map[string]interface{}{"error_code": "E2", "sms_code": "999999"},
		},
		"deep": map[string]interface{}{
			"level1": map[string]interface{}{
				"level2": map[string]interface{}{
					"password":    "p@ss",
					"status_code": 502,
				},
			},
		},
	})
	attempts, ok := decoded["attempts"].([]interface{})
	if !ok || len(attempts) != 2 {
		t.Fatalf("attempts 必须是长度 2 的数组，实际 %#v", decoded["attempts"])
	}
	first := attempts[0].(map[string]interface{})
	if first["error_code"] != "E1" {
		t.Fatalf("数组元素里的 error_code 必须保留，实际 %#v", first["error_code"])
	}
	if first["phone"] != "****8000" {
		t.Fatalf("数组元素里的 phone 必须只留后四位，实际 %#v", first["phone"])
	}
	second := attempts[1].(map[string]interface{})
	if second["sms_code"] != ticketContextRedacted {
		t.Fatalf("数组元素里的 sms_code 必须脱敏，实际 %#v", second["sms_code"])
	}

	// 多层嵌套：递归必须到底。
	deep := decoded["deep"].(map[string]interface{})["level1"].(map[string]interface{})["level2"].(map[string]interface{})
	if deep["password"] != ticketContextRedacted {
		t.Fatalf("三层嵌套里的 password 必须脱敏，实际 %#v", deep["password"])
	}
	if deep["status_code"] != float64(502) {
		t.Fatalf("三层嵌套里的 status_code 必须保留，实际 %#v", deep["status_code"])
	}
}

// ---------------------------------------------------------------------------
// 第 9 组：大小写/空格归一 + map[string]string 分支
// ---------------------------------------------------------------------------

func TestTicketContextKeyNormalizationAndStringMap(t *testing.T) {
	// 大小写与空格：ERROR_CODE 不是凭据、CODE 是凭据 —— 归一化后仍按语义分开。
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"ERROR_CODE":  "UAPI_TIMEOUT",
		"  Code  ":    "123456",
		"Status_Code": 500,
		" PASSWORD ":  "p@ss",
	})
	assertPreserved(t, decoded, "ERROR_CODE", "UAPI_TIMEOUT")
	assertPreserved(t, decoded, "Status_Code", 500)
	if decoded["  Code  "] != ticketContextRedacted {
		t.Fatalf("「  Code  」归一化后是裸 code，必须脱敏，实际 %#v", decoded["  Code  "])
	}
	if decoded[" PASSWORD "] != ticketContextRedacted {
		t.Fatalf("「 PASSWORD 」归一化后是 password，必须脱敏，实际 %#v", decoded[" PASSWORD "])
	}

	// map[string]string 走另一条分支（值类型是 string，不可能是 map/array）。
	stringMap := decodedForStringMap(t, map[string]string{
		"error_code": "E1",
		"sms_code":   "123456",
		"code":       "123456",
	})
	if stringMap["error_code"] != "E1" {
		t.Fatalf("map[string]string 里的 error_code 必须保留，实际 %q", stringMap["error_code"])
	}
	if stringMap["sms_code"] != ticketContextRedacted {
		t.Fatalf("map[string]string 里的 sms_code 必须脱敏，实际 %q", stringMap["sms_code"])
	}
	if stringMap["code"] != ticketContextRedacted {
		t.Fatalf("map[string]string 里的 code 必须脱敏，实际 %q", stringMap["code"])
	}
}

// decodedForStringMap 覆盖 map[string]string 这条分支。
func decodedForStringMap(t *testing.T, raw map[string]string) map[string]string {
	t.Helper()
	// 直接走脱敏内核：marshalTicketContext 的入参是 map[string]interface{},
	// 但嵌套值可能是 map[string]string，这里单独驱动同一条路径。
	redacted, ok := redactTicketContextValue(raw).(map[string]string)
	if !ok {
		t.Fatalf("map[string]string 必须原类型返回，实际 %T", redactTicketContextValue(raw))
	}
	return redacted
}

// ---------------------------------------------------------------------------
// 第 10 组：非凭据的普通字段一个都不能被误伤（防止过度脱敏回潮）
// ---------------------------------------------------------------------------

func TestTicketContextPreservesOrdinaryFields(t *testing.T) {
	decoded := decodeRedactedContext(t, map[string]interface{}{
		"failureReason":  "uapi timeout",
		"retryCount":     3,
		"firstFailureAt": "2026-11-10 10:00:00",
		"lastFailureAt":  "2026-11-10 10:05:00",
		"region":         "云南",
		"llc":            "移动",
		"asn":            "AS9808",
		"trace_code":     "tr-0001", // 编码类：保留
		"return_code":    "0",       // 编码类：保留
		"country_code":   "CN",      // 编码类：保留
		"zip_code":       "650000",  // 编码类：保留
		"course_code":    "CS101",   // 编码类：保留
	})
	for key, want := range map[string]interface{}{
		"failureReason":  "uapi timeout",
		"retryCount":     3,
		"firstFailureAt": "2026-11-10 10:00:00",
		"lastFailureAt":  "2026-11-10 10:05:00",
		"region":         "云南",
		"llc":            "移动",
		"asn":            "AS9808",
		"trace_code":     "tr-0001",
		"return_code":    "0",
		"country_code":   "CN",
		"zip_code":       "650000",
		"course_code":    "CS101",
	} {
		assertPreserved(t, decoded, key, want)
	}
}
