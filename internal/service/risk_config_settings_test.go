package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 风控**非数值**配置读写路径的回归钉。
//
// 背景（本次审计发现的真实缺口，见 risk_config_settings.go 顶部注释）：
// `risk_proxy_asn_list` 与 `risk_evaluate_interval_minutes` 在此之前
// **都没有任何写入路径**，而它们的缺失会让规则静默失效：
//   - 名单为空时 R10 连查询都不做 ⇒ 永远不告警；
//   - 评估间隔只能靠改库，越界值还会「反向生效」（回落成更频繁）。
//
// 这些用例的核心判据是「**写入后判定链真的读到了新值**」，而不是
// 「写进 system_settings 了」—— 后者在本仓反复被证明不等于接线成功
// （t193 / 121 号里成批的「实现完了但没接」）。

func newRiskConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := t.Name()
	db, err := gorm.Open(sqlite.Open("file:risk-config-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 逐个模型分次迁移（本项目既有纪律：一次传多个会让 GORM 合并建表语句，
	// 报错完全不会提到 AutoMigrate）。
	for _, model := range []any{&entity.SystemSetting{}, &entity.RiskRule{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func riskConfigItemValue(t *testing.T, items []RiskConfigItem, key string) RiskConfigItem {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("配置清单里找不到 %q（实际 %d 项）", key, len(items))
	return RiskConfigItem{}
}

// ① 读写往返：写入的 ASN 名单必须被判定链（proxyASNListWithDB）读到。
//
// 「写进 system_settings 就算成功」是假钉子：真正的判据是
// collectProxyIPLoginCandidates 用的那个读取函数能拿到归一化后的名单。
func TestRiskConfigASNListRoundTripReachesDecisionChain(t *testing.T) {
	db := newRiskConfigTestDB(t)

	// 前置：未配置时读取侧必须拿到空名单（= 规则静默）。
	if got := proxyASNListWithDB(db); len(got) != 0 {
		t.Fatalf("未配置时名单必须为空（R10 静默），实际 %v", got)
	}

	// 写入时用小写 + 裸数字 + 混合分隔符，验证归一化落库。
	result, err := UpdateRiskConfigWithDB(db, RiskThresholdSettingKeyProxyASNList, "as15169; 16509\nAS15169")
	if err != nil {
		t.Fatalf("保存 ASN 名单失败: %v", err)
	}
	// 字面量断言落库形态（不引用常量拼期望串，否则常量漂移时用例恒绿）。
	if result.Item.Value != "AS15169,AS16509" {
		t.Fatalf("落库形态 = %q, want %q（归一化 + 去重 + 统一分隔符）", result.Item.Value, "AS15169,AS16509")
	}
	if !result.Item.Configured {
		t.Fatalf("显式配置后 Configured 必须为 true")
	}
	if result.Reset {
		t.Fatalf("显式写入不该被标记成重置")
	}

	// 关键判据：判定链的读取函数必须读到同一份名单。
	got := proxyASNListWithDB(db)
	if len(got) != 2 || got[0] != "AS15169" || got[1] != "AS16509" {
		t.Fatalf("判定链读到的名单 = %v, want [AS15169 AS16509]", got)
	}

	// 读接口也要一致（同源）。
	items, err := RiskConfigs(db)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	item := riskConfigItemValue(t, items, RiskThresholdSettingKeyProxyASNList)
	if item.Value != "AS15169,AS16509" || item.Source != "system_settings" {
		t.Fatalf("读接口 item = %+v, want value=AS15169,AS16509 source=system_settings", item)
	}
	if item.RuleID != entity.RiskRuleIDProxyIPLogin {
		t.Fatalf("ASN 名单必须归属 R10，实际 %q", item.RuleID)
	}
}

// ② 空名单是**合法**值，且等价于「清空配置」。
//
// 这条与数值阈值完全相反（阈值必须 > 0）。若把空串当成非法，
// 运营就无法关闭 R10；若把空串存成一行空值，`Configured` 会恒 true
// 且重置按钮不再出现 —— 界面卡在「已配置但没有值」。
func TestRiskConfigASNListEmptyIsLegalAndResets(t *testing.T) {
	db := newRiskConfigTestDB(t)
	if _, err := UpdateRiskConfigWithDB(db, RiskThresholdSettingKeyProxyASNList, "AS15169"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := UpdateRiskConfigWithDB(db, RiskThresholdSettingKeyProxyASNList, "   ")
	if err != nil {
		t.Fatalf("空名单必须被接受（= 关闭 R10），实际 %v", err)
	}
	if !result.Reset {
		t.Fatalf("空值必须走重置语义（删行），而不是存一个空串行")
	}
	if result.Item.Configured {
		t.Fatalf("清空后 Configured 必须为 false（否则重置按钮永远消失）")
	}
	if result.Item.Value != "" {
		t.Fatalf("清空后生效值必须为空串，实际 %q", result.Item.Value)
	}
	// 判定链侧：空名单 → 规则静默（不查询、不告警）。
	if got := proxyASNListWithDB(db); len(got) != 0 {
		t.Fatalf("清空后判定链必须拿到空名单，实际 %v", got)
	}
	// system_settings 里也不该残留空行。
	var count int64
	if err := db.Model(&entity.SystemSetting{}).
		Where("key = ?", RiskThresholdSettingKeyProxyASNList).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("清空后不得残留行（实际 %d 行）：空行会让 Configured 恒为 true", count)
	}
}

// ③ 坏 ASN 必须拒绝，且**不落库**。
//
// `ValidateProxyASNList` 在本次改动之前零生产调用方（只有 _test.go 调它）——
// 「保存时严格校验」这条设计根本没接线。本用例把接线钉住。
func TestRiskConfigASNListRejectsBadItemWithoutWriting(t *testing.T) {
	db := newRiskConfigTestDB(t)
	if _, err := UpdateRiskConfigWithDB(db, RiskThresholdSettingKeyProxyASNList, "AS15169"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	badValues := []string{"AS15169,不是AS号", "AS15169,AS", "AS15169;12ab34", "cloudflare"}
	for _, bad := range badValues {
		t.Run(bad, func(t *testing.T) {
			if _, err := UpdateRiskConfigWithDB(db, RiskThresholdSettingKeyProxyASNList, bad); !errors.Is(err, ErrRiskConfigInvalid) {
				t.Fatalf("坏名单 %q 必须返回 ErrRiskConfigInvalid，实际 %v", bad, err)
			}
			// 拒绝之后必须完全没变（不能「报了错但还是写了」）。
			items, err := RiskConfigs(db)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			item := riskConfigItemValue(t, items, RiskThresholdSettingKeyProxyASNList)
			if item.Value != "AS15169" {
				t.Fatalf("被拒后值必须保持 AS15169，实际 %q", item.Value)
			}
		})
	}
}

// ④ 评估间隔：写入必须被 resolveRiskEvaluateInterval 读到，且越界/非整数被拒。
//
// 这条规则的方向很容易搞反：填 2880（想两天一次）**不是**变得「更少」，
// 而是解析失败回落默认 10 分钟 = 频率高 288 倍。所以越界必须在写入前挡住。
func TestRiskConfigEvaluateIntervalRoundTripAndBounds(t *testing.T) {
	db := newRiskConfigTestDB(t)

	// 未配置 → 默认间隔。
	if got := resolveRiskEvaluateInterval(db); got.Minutes() != float64(DefaultRiskEvaluateIntervalMinutes) {
		t.Fatalf("未配置时间隔 = %v 分钟, want %d", got.Minutes(), DefaultRiskEvaluateIntervalMinutes)
	}

	if _, err := UpdateRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey, " 30 "); err != nil {
		t.Fatalf("保存 30 分钟失败: %v", err)
	}
	// 关键判据：判定任务用的解析函数必须读到 30 分钟。
	if got := resolveRiskEvaluateInterval(db); got.Minutes() != 30 {
		t.Fatalf("判定任务读到的间隔 = %v 分钟, want 30", got.Minutes())
	}

	// 边界值：1 与 1440 必须接受（它们是合法区间端点）。
	for _, ok := range []string{"1", "1440"} {
		if _, err := UpdateRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey, ok); err != nil {
			t.Fatalf("边界值 %q 必须被接受，实际 %v", ok, err)
		}
	}

	// 非法值：0 / 负数 / 超上限 / 小数 / 科学计数 / 带符号一律拒绝。
	illegal := []string{"0", "-5", "1441", "2880", "1.5", "1e3", "+5", "abc", "10m"}
	for _, bad := range illegal {
		t.Run("reject_"+bad, func(t *testing.T) {
			// 先写成 30，才能证明「被拒后没变」。
			if _, err := UpdateRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey, "30"); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := UpdateRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey, bad); !errors.Is(err, ErrRiskConfigInvalid) {
				t.Fatalf("非法间隔 %q 必须返回 ErrRiskConfigInvalid，实际 %v", bad, err)
			}
			if got := resolveRiskEvaluateInterval(db); got.Minutes() != 30 {
				t.Fatalf("被拒后间隔必须保持 30 分钟，实际 %v（说明写了库）", got.Minutes())
			}
		})
	}
}

// ⑤ 重置：删行回到代码默认值，且**不留**显式配置痕迹。
func TestRiskConfigResetReturnsToCodeDefault(t *testing.T) {
	db := newRiskConfigTestDB(t)
	if _, err := UpdateRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey, "60"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := ResetRiskConfigWithDB(db, RiskEvaluateIntervalSettingKey)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !result.Reset {
		t.Fatalf("重置结果必须标记 Reset=true")
	}
	// 字面量断言默认值（不引用常量，常量漂移时必红）。
	if result.Item.Value != "10" {
		t.Fatalf("重置后生效值 = %q, want %q", result.Item.Value, "10")
	}
	if result.Item.Configured {
		t.Fatalf("重置后 Configured 必须为 false")
	}
	if got := resolveRiskEvaluateInterval(db); got.Minutes() != 10 {
		t.Fatalf("重置后判定任务应回到 10 分钟，实际 %v", got.Minutes())
	}
}

// ⑥ 白名单：未知键必须拒绝（防手滑往 system_settings 写任意键）。
func TestRiskConfigRejectsUnknownKey(t *testing.T) {
	db := newRiskConfigTestDB(t)
	for _, key := range []string{"wecom_risk_webhook_url", "risk_rule_debt_user_threshold", "", "anything"} {
		if _, err := UpdateRiskConfigWithDB(db, key, "1"); !errors.Is(err, ErrRiskConfigUnknown) {
			t.Fatalf("键 %q 必须返回 ErrRiskConfigUnknown（不得写入任意 system_settings 键），实际 %v", key, err)
		}
		if _, err := ResetRiskConfigWithDB(db, key); !errors.Is(err, ErrRiskConfigUnknown) {
			t.Fatalf("重置 %q 必须返回 ErrRiskConfigUnknown，实际 %v", key, err)
		}
	}
}

// ⑦ 登记表自检：非数值配置与数值阈值**不得重叠**。
//
// 两张表都写同一个 system_settings 键，会让「读哪一张表的默认值」变成
// 一个取决于调用顺序的隐式约定 —— 而这正是本仓反复出现的「两份真相」。
// 同时钉住：R10 只能出现在非数值表里（它没有数值阈值）。
func TestRiskConfigSpecsDoNotOverlapThresholdSpecs(t *testing.T) {
	thresholdKeys := map[string]bool{}
	for _, spec := range riskThresholdSpecs() {
		thresholdKeys[spec.Key] = true
	}
	// 前置条件：数值表不能是空的，否则下面的循环恒不触发（假绿）。
	if len(thresholdKeys) == 0 {
		t.Fatalf("前置条件失效：riskThresholdSpecs() 返回空")
	}
	configKeys := map[string]bool{}
	for _, spec := range riskConfigSpecs() {
		if thresholdKeys[spec.Key] {
			t.Errorf("键 %q 同时出现在 riskThresholdSpecs() 与 riskConfigSpecs()："+
				"同一个键有两份默认值与两套校验，必然漂移", spec.Key)
		}
		configKeys[spec.Key] = true
	}
	// R10 的配置对象是 ASN 名单，必须在非数值表里（数值表里没有它）。
	if !configKeys[RiskThresholdSettingKeyProxyASNList] {
		t.Fatalf("R10 的 ASN 名单必须登记在 riskConfigSpecs()（否则运营无从配置，规则永久静默）")
	}
	// 评估间隔是全局项，不归属任何规则。
	for _, spec := range riskConfigSpecs() {
		if spec.Key == RiskEvaluateIntervalSettingKey && spec.RuleID != "" {
			t.Fatalf("评估间隔是全局项，RuleID 必须为空，实际 %q", spec.RuleID)
		}
	}
	// 每个 Key 都必须能通过自己的校验（默认值本身必须合法）。
	for _, key := range SortedRiskConfigKeys() {
		spec, ok := riskConfigSpecByKey(key)
		if !ok {
			t.Fatalf("SortedRiskConfigKeys 返回了查不到的键 %q", key)
		}
		if err := validateRiskConfigValue(spec, spec.DefaultValue); err != nil {
			t.Errorf("配置 %q 的默认值 %q 通不过自己的校验：%v", key, spec.DefaultValue, err)
		}
	}
}
