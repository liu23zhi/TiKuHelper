package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R5 · register_burst（96 §2.1 L110）
//
// 判据：同一 /24 段在 1h 滚动窗口内**自助注册** ≥N 个账号（默认 3，每段 6h 节流）。
//
// 本文件最关键的判据是**私网跳过**：
// 生产库两个账号的 register_ip 都是 `172.18.0.1`（同一个 Docker bridge）。
// 不跳过的话，同一台宿主机上的账号天然同段 —— 再多一个管理员建号就炸。
// ════════════════════════════════════════════════════════════════════

// seedRegisterUser 造一个注册用户（register_ip + register_source + created_at）。
func seedRegisterUser(t *testing.T, db *gorm.DB, username, registerIP, source string, at time.Time) entity.User {
	t.Helper()
	user := entity.User{
		Username:       username,
		Password:       "x",
		Status:         "enabled",
		Role:           "user",
		RegisterIP:     registerIP,
		RegisterSource: source,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	// created_at 由 GORM autoCreateTime 填当前时间，回写目标时间以控制窗口。
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).
		UpdateColumn("created_at", at).Error; err != nil {
		t.Fatalf("backdate user: %v", err)
	}
	return user
}

// 判据①（**最关键**）：私网 IP 必须跳过。
//
// 变异方向：去掉 `IsPrivateOrReservedIP` 判断 → 本用例必红。
func TestRegisterBurstSkipsPrivateIP(t *testing.T) {
	cases := []string{"172.18.0.1", "10.0.0.5", "192.168.1.10", "127.0.0.1"}
	for _, ip := range cases {
		t.Run(ip, func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "rb-private-"+ip)
			seedRiskRules(t, db)
			now := riskR1TestTime()
			// 5 个账号全部来自同一个私网 IP。
			for i := 0; i < 5; i++ {
				seedRegisterUser(t, db, "priv"+itoaRisk(int32(i)), ip, "public",
					now.Add(-time.Duration(i+1)*time.Minute))
			}
			if _, err := EvaluateDebtAlerts(db, now); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
				t.Fatalf("私网 IP %s 必须跳过（否则同一宿主机上的账号天然同段），实际告警 %d 条",
					ip, got)
			}
		})
	}
}

// 判据②：**管理员/系统建号不计入**。
//
// 依据：`user.go:1290` 建的账号，register_ip 是**管理员当时的 IP**
// （`entity/users.gen.go` 字段注释原话）。算进去会让「管理员批量建号」直接触发。
//
// 变异方向：去掉 register_source 过滤 → 本用例必红。
func TestRegisterBurstIgnoresAdminCreatedAccounts(t *testing.T) {
	cases := []string{"admin", "system", "legacy"}
	for _, source := range cases {
		t.Run(source+"_not_counted", func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "rb-src-"+source)
			seedRiskRules(t, db)
			now := riskR1TestTime()
			// 5 个账号全部来自同一个公网 /24，但 register_source 不是 public。
			for i := 0; i < 5; i++ {
				seedRegisterUser(t, db, "admin"+itoaRisk(int32(i)), "203.0.113.10", source,
					now.Add(-time.Duration(i+1)*time.Minute))
			}
			if _, err := EvaluateDebtAlerts(db, now); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
				t.Fatalf("register_source=%s 的账号不计入 R5（其 register_ip 是操作者地址），"+
					"实际告警 %d 条", source, got)
			}
		})
	}
}

// 判据③：公网 /24 + 自助注册 3 个 → 告警（正向主路径）。
func TestRegisterBurstDetectsPublicBurst(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-hit")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 个账号，同一 /24 段的不同 IP。
	for i, ip := range []string{"203.0.113.11", "203.0.113.12", "203.0.113.13"} {
		seedRegisterUser(t, db, "burst"+itoaRisk(int32(i)), ip, "public",
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 1 {
		t.Fatalf("同 /24 段 3 个自助注册必须告警，实际 %d 条", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "register_burst").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["segment"].(string); got != "203.0.113.0/24" {
		t.Fatalf("detail.segment = %q, want 203.0.113.0/24", got)
	}
	if got, _ := detail["registerCount"].(float64); got != 3 {
		t.Fatalf("detail.registerCount = %v, want 3", detail["registerCount"])
	}
	// 硬要求：threshold 是配置值（3），不是实际计数。
	if got, _ := detail["threshold"].(float64); got != 3 {
		t.Fatalf("detail.threshold = %v, want 3（配置值）", detail["threshold"])
	}
	// 管理员要能看出「这个段谁注册了」。
	samples, ok := detail["sampleUsers"].([]any)
	if !ok || len(samples) == 0 {
		t.Fatalf("detail.sampleUsers 必须给出样本用户名（管理员据此判断谁注册了），实际 %v",
			detail["sampleUsers"])
	}
}

// 判据④：**不同 /24 段不合并**（段粒度必须是 /24，不是 /16）。
//
// 变异方向：段粒度从 /24 改成 /16 → 本用例必红（两个段的计数会被合并）。
func TestRegisterBurstSeparatesSegments(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-seg")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 两个**不同 /24** 段各 2 个账号 —— 单段都不到 3。
	for i := 0; i < 2; i++ {
		seedRegisterUser(t, db, "sega"+itoaRisk(int32(i)), "203.0.113."+itoaRisk(int32(20+i)),
			"public", now.Add(-time.Duration(i+1)*time.Minute))
		seedRegisterUser(t, db, "segb"+itoaRisk(int32(i)), "203.0.114."+itoaRisk(int32(20+i)),
			"public", now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
		t.Fatalf("两个不同 /24 段各 2 个，单段都不到阈值 3，不得告警，实际 %d 条"+
			"（若为 1 说明段粒度比 /24 更粗，把两段合并了）", got)
	}
}

// 判据⑤：**同一 /24 段的不同 IP 要合并**（这是「段」的核心语义）。
//
// 变异方向：段粒度改成 /32（单 IP）→ 本用例必红。
func TestRegisterBurstMergesSameSegment(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-merge")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 个账号，IP 各不相同但同属 203.0.113.0/24。
	for i, ip := range []string{"203.0.113.30", "203.0.113.100", "203.0.113.250"} {
		seedRegisterUser(t, db, "merge"+itoaRisk(int32(i)), ip, "public",
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 1 {
		t.Fatalf("同 /24 段 3 个不同 IP 必须合并成 1 条告警，实际 %d 条"+
			"（若为 0 说明段粒度是 /32，不是「段」）", got)
	}
}

// 判据⑥：`register_ip` 空 → 跳过（不能把「未知」当成一个段）。
func TestRegisterBurstSkipsEmptyIP(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-empty")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 5; i++ {
		seedRegisterUser(t, db, "noip"+itoaRisk(int32(i)), "", "public",
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
		t.Fatalf("register_ip 为空的账号不得聚成一段凭空告警，实际 %d 条", got)
	}
}

// 判据⑦：IPv6 → 跳过（/24 对 IPv6 无意义）。
func TestRegisterBurstSkipsIPv6(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-ipv6")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 5 个账号全部来自同一个 IPv6 前缀。
	for i := 0; i < 5; i++ {
		seedRegisterUser(t, db, "v6user"+itoaRisk(int32(i)), "2001:db8::"+itoaRisk(int32(i+1)),
			"public", now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
		t.Fatalf("IPv6 不参与 R5（/24 对 IPv6 无意义），实际告警 %d 条", got)
	}
}

// 判据⑧：1 小时滚动窗口 —— 窗口外不计入。
func TestRegisterBurstRollingWindow(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-window")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 个账号，都在 61–63 分钟前（窗口外）。
	for i := 0; i < 3; i++ {
		seedRegisterUser(t, db, "old"+itoaRisk(int32(i)), "203.0.113."+itoaRisk(int32(40+i)),
			"public", now.Add(-time.Duration(61+i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
		t.Fatalf("61 分钟前的注册不在 1h 窗口内，不得告警，实际 %d 条", got)
	}
}

// 判据⑨：阈值边界 —— 2 个不告警（默认阈值 3），3 个告警。
func TestRegisterBurstThresholdBoundary(t *testing.T) {
	t.Run("two_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "rb-two")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 2; i++ {
			seedRegisterUser(t, db, "two"+itoaRisk(int32(i)), "203.0.113."+itoaRisk(int32(50+i)),
				"public", now.Add(-time.Duration(i+1)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
			t.Fatalf("2 个 < 阈值 3 必须不告警，实际 %d 条", got)
		}
	})

	// 恰好等于阈值必须告警（判据是 `>=` 不是 `>`）。
	//
	// ⚠️ 这条补一个真实缺口：只写「2 个不告警」时，把 `count < threshold`
	// 改成 `count <= threshold` 用例**仍绿**（2 < 3 与 2 <= 3 行为相同）。
	// 实测过：那个变异下 `DetectsPublicBurst` 与 `MergesSameSegment` 红了，
	// 但边界用例本身没红 —— 因为缺「恰好等于」的构造。
	t.Run("exactly_threshold_alerts", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "rb-edge")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		// 恰好 3 个（= 默认阈值 3）。
		for i := 0; i < 3; i++ {
			seedRegisterUser(t, db, "edge"+itoaRisk(int32(i)),
				"203.0.113."+itoaRisk(int32(55+i)), "public",
				now.Add(-time.Duration(i+1)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "register_burst"); got != 1 {
			t.Fatalf("恰好 3 个 = 阈值 3 必须告警（判据是 >= 不是 >），实际 %d 条", got)
		}
	})
}

// 判据⑩：阈值可配（双向）—— 这正是「N=3 会不会太敏感」的运营出口。
func TestRegisterBurstThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-cfg")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 3; i++ {
		seedRegisterUser(t, db, "cfg"+itoaRisk(int32(i)), "203.0.113."+itoaRisk(int32(60+i)),
			"public", now.Add(-time.Duration(i+1)*time.Minute))
	}
	// 阈值抬到 10（运营若觉得 /24 + 3 太敏感，就走这里，不需要改代码）。
	putRiskSetting(t, db, "risk_rule_register_burst_threshold", "10")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 0 {
		t.Fatalf("阈值 10 时 3 个必须不告警，实际 %d 条", got)
	}
	// 阈值降到 2：必须告警。
	putRiskSetting(t, db, "risk_rule_register_burst_threshold", "2")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 1 {
		t.Fatalf("阈值 2 时 3 个必须告警，实际 %d 条", got)
	}
}

// 判据⑪：只告警不处置（C1）。
func TestRegisterBurstNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "rb-noaction")
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	var created []entity.User
	for i := 0; i < 3; i++ {
		created = append(created, seedRegisterUser(t, db,
			"victim"+itoaRisk(int32(i)), "203.0.113."+itoaRisk(int32(70+i)), "public",
			now.Add(-time.Duration(i+1)*time.Minute)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "register_burst"); got != 1 {
		t.Fatalf("前置条件：必须已告警，实际 %d 条", got)
	}
	for _, user := range created {
		var after entity.User
		if err := db.First(&after, user.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if after.Status != "enabled" {
			t.Fatalf("C1：风控不得改用户状态（%s 变成 %q）", after.Username, after.Status)
		}
	}
	var blacklist int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklist).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}

// 判据⑫：`riskRegisterSegmentOf` 的单元真值表。
func TestRegisterSegmentOf(t *testing.T) {
	cases := []struct {
		ip   string
		want string
	}{
		{"203.0.113.1", "203.0.113.0/24"},
		{"203.0.113.254", "203.0.113.0/24"},
		{"203.0.114.1", "203.0.114.0/24"},
		{"8.8.8.8", "8.8.8.0/24"},
		// 私网 / 保留 / 回环 → 空
		{"172.18.0.1", ""},
		{"10.1.2.3", ""},
		{"192.168.0.1", ""},
		{"127.0.0.1", ""},
		// IPv6 → 空
		{"2001:db8::1", ""},
		{"::1", ""},
		// 空 / 非法 → 空
		{"", ""},
		{"   ", ""},
		{"not-an-ip", ""},
		// 带端口的形态（c.ClientIP() 不会带，但防御）
		{"203.0.113.5:8080", ""},
	}
	for _, tc := range cases {
		if got := riskRegisterSegmentOf(tc.ip); got != tc.want {
			t.Fatalf("riskRegisterSegmentOf(%q) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}
