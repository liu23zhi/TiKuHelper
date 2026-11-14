package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R10 proxy_ip_login（97 §5.1）
//
// 判据：登录 IP 的 ASN 落在运营配置的名单内。
//
// 名单方案选 **(b) system_settings**，理由见 risk_refund_proxy.go 顶部注释：
// 名单会变（新云厂商、新被滥用的 ASN），硬编码进代码意味着每次都要发版。
//
// 本文件的核心判据是**两条「必须不告警」**：
//  1. 名单为空 → 完全不告警（否则新部署环境全量误报）
//  2. 段表查不到 → 跳过（不能把「未知」当成「代理」）
// ════════════════════════════════════════════════════════════════════

// seedProxySegment 造一条带 ASN 的 IP 段。
func seedProxySegment(t *testing.T, db *gorm.DB, beginText, endText, asn, isp string) {
	t.Helper()
	begin, ok := ipv4ToUint32(beginText)
	if !ok {
		t.Fatalf("bad begin ip %q", beginText)
	}
	end, ok := ipv4ToUint32(endText)
	if !ok {
		t.Fatalf("bad end ip %q", endText)
	}
	segment := entity.IPSegment{
		BeginIP: begin, EndIP: end,
		BeginText: beginText, EndText: endText, SampleIP: beginText,
		ASN: asn, ISP: isp, Source: "test",
		FetchedAt: time.Now(),
	}
	if err := db.Create(&segment).Error; err != nil {
		t.Fatalf("create ip segment: %v", err)
	}
}

// 判据①（**最重要**）：名单为空 → 完全不告警。
//
// 变异方向：去掉「名单为空直接返回」→ 本用例必红。
func TestProxyIPLoginEmptyListDoesNotAlert(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-empty")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 段表里有 Google 的 ASN，登录也来自那个段 —— 但名单为空。
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	seedRiskLoginAudit(t, db, 9901, "proxied", "8.8.8.8", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("ASN 名单为空时本规则必须完全不告警（否则新部署环境全量误报），实际 %d 条", got)
	}
}

// 判据②：名单非空 + ASN 命中 → 告警。
func TestProxyIPLoginDetectsListedASN(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-hit")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169,AS16509")
	now := riskR1TestTime()
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	seedRiskLoginAudit(t, db, 9902, "proxied", "8.8.8.8", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 1 {
		t.Fatalf("ASN AS15169 在名单内必须告警，实际 %d 条", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "proxy_ip_login").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	// threshold 是**名单规模**（配置值 2），不是命中数（1）。
	if got, _ := detail["threshold"].(float64); got != 2 {
		t.Fatalf("detail.threshold = %v, want 2（名单规模，不是命中数）", detail["threshold"])
	}
	if got, _ := detail["value"].(float64); got != 1 {
		t.Fatalf("detail.value = %v, want 1（命中的 ASN 个数）", detail["value"])
	}
}

// 判据③：ASN 不在名单 → 不告警（反向对照）。
func TestProxyIPLoginIgnoresUnlistedASN(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-miss")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS16509")
	now := riskR1TestTime()
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	seedRiskLoginAudit(t, db, 9903, "proxied", "8.8.8.8", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("AS15169 不在名单（名单只有 AS16509）不该告警，实际 %d 条", got)
	}
}

// 判据④（**第二关键**）：段表查不到 → 当成未知，**不告警**。
//
// 变异方向：把 segment == nil 当成命中 → 本用例必红。
func TestProxyIPLoginSkipsUnknownSegment(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-unknown")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	// **不建任何段记录** —— 该 IP 归属地未知。
	seedRiskLoginAudit(t, db, 9904, "unknown-ip", "1.2.3.4", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("段表查不到的 IP 必须跳过（不能把「未知」当成「代理」），实际 %d 条", got)
	}
}

// 判据⑤：私网 IP 必须跳过（即使段表里恰好有记录）。
func TestProxyIPLoginSkipsPrivateIP(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-private")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	// 脏数据：段表里给私网段配了一个名单内的 ASN。
	seedProxySegment(t, db, "172.18.0.0", "172.18.0.255", "AS15169", "FAKE")
	seedRiskLoginAudit(t, db, 9905, "internal", "172.18.0.1", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("私网 IP 必须跳过（即使段表有脏记录），实际 %d 条", got)
	}
}

// 判据⑥：**段记录有 isp 但 asn 为空 → 跳过**（不猜）。
//
// 这是任务书要求我判断的一条：`asn` 空但 `isp` 有值时怎么办。
//
// 我的判断：**跳过，不用 isp 兜底**。理由：
//   - `isp` 是自由文本（"GOOGLE"/"谷歌"/"Google LLC" 都可能），关键词匹配会漂移；
//   - 更重要的是**误报代价**：`isp` 含 "Amazon" 的可能是亚马逊中国的正常企业网络，
//     而 ASN 是精确的注册标识。宁可漏报（等段表补全 ASN），也不要因为文本匹配误伤。
func TestProxyIPLoginSkipsSegmentWithoutASN(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-no-asn")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	// 段记录有 isp（"GOOGLE"）但 asn 为空。
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "", "GOOGLE")
	seedRiskLoginAudit(t, db, 9906, "isp-only", "8.8.8.8", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("段记录 asn 为空时必须跳过（不用 isp 文本兜底，避免关键词匹配误伤），实际 %d 条", got)
	}
}

// 判据⑦：ASN 名单解析 —— 多种分隔符、`AS` 前缀可省、重复去重、坏项跳过。
func TestParseProxyASNList(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  []string
		wantN int
	}{
		{"comma", "AS15169,AS16509", []string{"AS15169", "AS16509"}, 2},
		{"semitonewlinespace", "AS15169;AS16509\nAS14061 AS16276", []string{"AS15169", "AS16509", "AS14061", "AS16276"}, 4},
		{"without_prefix", "15169, 16509", []string{"AS15169", "AS16509"}, 2},
		{"lowercase", "as15169", []string{"AS15169"}, 1},
		{"dedup", "AS15169,15169,As15169", []string{"AS15169"}, 1},
		{"bad_items_skipped", "AS15169,notanasn,AS16509,", []string{"AS15169", "AS16509"}, 2},
		{"empty", "", nil, 0},
		{"only_whitespace", "   \n\t ", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseProxyASNList(tc.raw)
			if len(got) != tc.wantN {
				t.Fatalf("ParseProxyASNList(%q) 长度 = %d, want %d（实际 %v）",
					tc.raw, len(got), tc.wantN, got)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Fatalf("ParseProxyASNList(%q)[%d] = %q, want %q", tc.raw, i, got[i], want)
				}
			}
		})
	}
}

// 判据⑧：保存时严格校验（坏项拒绝），与「读取时宽容跳过」形成双层。
func TestValidateProxyASNList(t *testing.T) {
	valid := []string{"", "AS15169", "AS15169,AS16509", "15169 16509", "as15169;as16509"}
	for _, raw := range valid {
		if err := ValidateProxyASNList(raw); err != nil {
			t.Fatalf("ValidateProxyASNList(%q) 不该拒绝：%v", raw, err)
		}
	}
	invalid := []string{"AS15169,notanasn", "abc", "AS", "AS15169,AS-16509"}
	for _, raw := range invalid {
		if err := ValidateProxyASNList(raw); err == nil {
			t.Fatalf("ValidateProxyASNList(%q) 必须拒绝（保存时严格，否则运营以为生效了）", raw)
		}
	}
}

// 判据⑨：同一用户从多个数据中心 IP 登录 → 一条告警（按用户聚合，不按 IP）。
func TestProxyIPLoginAggregatesPerUser(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-agg")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	// 同一用户从 3 个不同的 Google IP 登录。
	for i, ip := range []string{"8.8.8.1", "8.8.8.2", "8.8.8.3"} {
		seedRiskLoginAudit(t, db, 9907, "multi-proxy", ip, now.Add(-time.Duration(i+1)*time.Hour), "success")
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 1 {
		t.Fatalf("同一用户从 3 个数据中心 IP 登录只应产生 1 条告警（按用户聚合），实际 %d 条", got)
	}
}

// 判据⑩：非成功登录不计入（只看 action=10 + status=success）。
func TestProxyIPLoginIgnoresFailedLogins(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-failed")
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	// 失败登录（status=denied）。
	seedRiskLoginAudit(t, db, 9908, "failed-proxy", "8.8.8.8", now.Add(-time.Hour), "denied")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 0 {
		t.Fatalf("失败的登录不算「登录成功来自代理」，实际 %d 条", got)
	}
}

// 判据⑪：只告警不处置（C1）。
func TestProxyIPLoginNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "proxy-noaction")
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskThresholdSettingKeyProxyASNList, "AS15169")
	now := riskR1TestTime()
	seedProxySegment(t, db, "8.8.8.0", "8.8.8.255", "AS15169", "GOOGLE")
	user := entity.User{Username: "proxy-target", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedRiskLoginAudit(t, db, user.ID, user.Username, "8.8.8.8", now.Add(-time.Hour), "success")

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "proxy_ip_login"); got != 1 {
		t.Fatalf("前置条件：必须已告警，实际 %d 条", got)
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "enabled" {
		t.Fatalf("C1：风控不得改用户状态，实际 %q", after.Status)
	}
	var blacklist int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklist).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}
