package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 脚本域名设置的读写与脚本自身域名的校验。
//
// 语法重写后这里的判据：只有 `*.a.com` / `a.com/*` 两种格式可写入；
// 旧格式在**写入**路径显式报错，在**读取**路径被丢弃（GET 不 500）。
// 匹配边界的正反例见 script_domain_format_test.go —— 那边是主战场，
// 本文件只钉「设置这一层」的读写语义，不重复边界用例。

func TestScriptDomainSettingsNormalizeAndValidate(t *testing.T) {
	db := newScriptTestDB(t)
	settings, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains: []string{" *.Example.COM ", "*.example.com", "foo.example.net/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Domains) != 2 || settings.Domains[0] != "*.example.com" || settings.Domains[1] != "foo.example.net/*" {
		t.Fatalf("unexpected normalized settings: %+v", settings)
	}
	// `*.example.com` 覆盖子域。
	if err := ValidateScriptDomainWithDB(db, "https://a.example.com"); err != nil {
		t.Fatalf("allowed subdomain rejected: %v", err)
	}
	// 但**不覆盖**裸域 example.com —— 这是新语法明确定下的边界。
	if err := ValidateScriptDomainWithDB(db, "example.com"); !errors.Is(err, ErrScriptDomainUnavailable) {
		t.Fatalf("bare domain must be rejected by *.example.com, got %v", err)
	}
	// 路径规则只校验主机维度。
	if err := ValidateScriptDomainWithDB(db, "foo.example.net"); err != nil {
		t.Fatalf("path rule host should be allowed: %v", err)
	}
	if err := ValidateScriptDomainWithDB(db, "other.com"); !errors.Is(err, ErrScriptDomainUnavailable) {
		t.Fatal("unlisted domain should be rejected")
	}
	if err := db.Create(&entity.SystemSetting{Key: "unrelated", Value: "keep"}).Error; err != nil {
		t.Fatal(err)
	}
	var preserved entity.SystemSetting
	if err := db.Where("key = ?", "unrelated").First(&preserved).Error; err != nil || preserved.Value != "keep" {
		t.Fatalf("unrelated settings were not preserved: %+v err=%v", preserved, err)
	}
}

// 上一版允许「列表里有条目但 allowWildcard 关着」这种自相矛盾的状态。
// 语法重写后该字段被删除，这里钉住「读旧 JSON 不会崩、旧 allowWildcard 被忽略」。
func TestScriptDomainSettingsIgnoreRemovedWildcardFlag(t *testing.T) {
	db := newScriptTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: ScriptDomainSettingsKey, Value: `{"domains":["*.example.com"],"allowWildcard":false}`}).Error; err != nil {
		t.Fatal(err)
	}
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	// allowWildcard:false 不再有任何语义 —— 列表里有 `*.example.com` 就代表允许泛域名。
	if len(settings.Domains) != 1 || settings.Domains[0] != "*.example.com" {
		t.Fatalf("legacy allowWildcard must not affect the list: %+v", settings)
	}
	if err := ValidateScriptDomainWithDB(db, "x.example.com"); err != nil {
		t.Fatalf("wildcard entry must be usable regardless of the removed flag: %v", err)
	}
}
