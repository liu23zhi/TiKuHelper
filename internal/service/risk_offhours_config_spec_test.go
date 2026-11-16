package service

import (
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 凌晨时段的两项配置必须登记进 riskConfigSpecs 白名单，
// 否则写接口会以 ErrRiskConfigUnknown 拒绝 —— 表现为「界面上有这一项但保存报错」。
//
// 这是「代码都在、只差接线」在本项目的又一次重演形态：
// reader 写好了但没进白名单，功能等于不存在。

func TestRiskOffhoursSpecsRegistered(t *testing.T) {
	specs := riskConfigSpecs()
	byKey := map[string]riskConfigSpec{}
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}

	for _, key := range []string{riskOffhoursStartSettingKey, riskOffhoursEndSettingKey} {
		spec, ok := byKey[key]
		if !ok {
			t.Fatalf("%s 必须登记进 riskConfigSpecs，否则写接口会以未知配置拒绝", key)
		}
		if spec.Kind != RiskConfigKindHourOfDay {
			t.Fatalf("%s 的 Kind 必须是 hour_of_day，实际 %q", key, spec.Kind)
		}
		if spec.RuleID != entity.RiskRuleIDOffhoursActivity {
			t.Fatalf("%s 必须归属 R16（offhours_activity），实际 %q", key, spec.RuleID)
		}
		if spec.DefaultValue == "" {
			t.Fatalf("%s 必须有默认值（界面据此提示「留空回到默认」）", key)
		}
		// 默认值必须与代码默认常量一致，否则界面显示一个已经不对的默认值。
		var want string
		switch key {
		case riskOffhoursStartSettingKey:
			want = "2"
		case riskOffhoursEndSettingKey:
			want = "5"
		}
		if spec.DefaultValue != want {
			t.Fatalf("%s 的默认值应为代码默认 %s，实际 %q", key, want, spec.DefaultValue)
		}
		// Help 必须说明「跨零点合法」这件事 —— 那是本配置最容易填错的地方。
		if key == riskOffhoursEndSettingKey && !strings.Contains(spec.Help, "跨零点") {
			t.Fatalf("终点项的 Help 必须说明跨零点合法，实际 %q", spec.Help)
		}
	}
}

func TestRiskOffhoursSpecValidation(t *testing.T) {
	startSpec, ok := riskConfigSpecByKey(riskOffhoursStartSettingKey)
	if !ok {
		t.Fatal("起点 spec 必须存在")
	}
	endSpec, ok := riskConfigSpecByKey(riskOffhoursEndSettingKey)
	if !ok {
		t.Fatal("终点 spec 必须存在")
	}

	valid := []string{"0", "6", "23", ""}
	for _, value := range valid {
		if err := validateRiskConfigValue(startSpec, value); err != nil {
			t.Fatalf("起点 %q 应合法，实际 %v", value, err)
		}
	}
	invalid := []string{"24", "-1", "1.5", "1e3", "+5", "abc", " 12x "}
	for _, value := range invalid {
		if err := validateRiskConfigValue(startSpec, value); err == nil {
			t.Fatalf("起点 %q 应被拒绝（越界或非纯整数），实际通过了", value)
		}
	}

	// ⚠️ 终点**小于**起点必须在**校验层**就放行：跨零点（22→6）是最常用配置。
	// 注意 spec 校验只管「单个值是否 0-23 的整数」，
	// 两者的大小关系由 riskOffhoursWindow 在读取时判断（那里才看得到两个值）。
	if err := validateRiskConfigValue(endSpec, "6"); err != nil {
		t.Fatalf("终点 6 应合法，实际 %v", err)
	}
}

// 白名单查表必须能命中这两项（写接口的入口判据）。
func TestRiskOffhoursKeysAreWritable(t *testing.T) {
	for _, key := range []string{riskOffhoursStartSettingKey, riskOffhoursEndSettingKey} {
		if _, ok := riskConfigSpecByKey(key); !ok {
			t.Fatalf("%s 必须能通过白名单查表，否则 PUT /risk/config/%s 会 400", key, key)
		}
	}
}
