package service

// 端到端回归：脚本**按管理员配置的域名格式创建后，公开入口必须能解析到它**。
//
// 这组用例来自本轮实测发现的两个真实 bug，都属于「单元层面全绿、端到端全断」：
//
//  1. `a.com/*` 路径格式：脚本默认 useWildcard=true ⇒ 公开地址是根路径 "",
//     而路径规则要求 cleanPath 至少一段 ⇒ 该类脚本永远 ErrScriptNotFound。
//  2. 域名模板匹配的第二个参数一度被当成「请求完整 host」，
//     导致 `*.a.com` 需要 host=<code>.<baseDomain> 才命中、而 `a.com/*` 要求
//     host 恰好等于 baseDomain —— 两者口径不一致。现在统一按 **baseDomain** 判。
//
// 判据核心：走**真实创建路径**（含 ValidateScriptDomainWithDB），再走**真实解析路径**
// （ResolvePublicScriptWithDB），断言能拿到脚本。手搓 entity 会绕过创建期校验，
// 测出来的场景在线上根本不存在（这正是最初误判的来源）。

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// seedDomainCreationFixture 造一个能真实创建脚本的环境：
// 一个已绑定手机/微信的用户、一个管理员、一个可用的原生版本。
// 必须走 UploadScriptVersionWithDB 与 CreateUserScriptWithDB 的真实路径 ——
// 手搓 entity 会绕过创建期校验，测出来的场景线上不存在。
func seedDomainCreationFixture(t *testing.T, db *gorm.DB, tag string) (entity.User, entity.User, entity.ScriptVersion) {
	t.Helper()
	owner := entity.User{Username: "domain-owner-" + tag, Role: "user", Balance: 50}
	admin := entity.User{Username: "domain-admin-" + tag, Role: "admin"}
	phone, wechat := "1380000"+tag[:2]+"000", "synthetic-domain-wechat-"+tag
	owner.Phone, owner.WechatID = &phone, &wechat
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	version, err := UploadScriptVersionWithDB(db, admin, "domain-v-"+tag, "domain.exe", []byte("MZdomain"))
	if err != nil {
		t.Fatal(err)
	}
	return owner, admin, version
}

// ────────────── 场景 A：`*.a.com` 泛域名格式 ──────────────

func TestScriptDomainEndToEndWildcardFormatResolves(t *testing.T) {
	db := newScriptTestDB(t)
	owner, _, version := seedDomainCreationFixture(t, db, "wild")
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.a.com"}}); err != nil {
		t.Fatal(err)
	}
	// 用户按 *.a.com 格式填 slot=x → 具体域名 x.a.com，baseDomain 提交其主机部分时
	// 由 ValidateScriptDomainWithDB 决定可否创建；这里按 UI 实际提交的**完整域名**建。
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "wild", VersionID: version.ID, BaseDomain: "x.a.com",
	})
	if err != nil {
		t.Fatalf("按 *.a.com 创建脚本失败：%v", err)
	}
	if !item.UseWildcardDomain {
		t.Fatalf("*.a.com 格式下脚本应走独立主机形态（useWildcard=true），实际 %v", item.UseWildcardDomain)
	}
	// 公开地址：host = <shortCode>.x.a.com，根路径。
	host := item.ShortCode + ".x.a.com"
	got, err := ResolvePublicScriptWithDB(db, host, "")
	if err != nil {
		t.Fatalf("ResolvePublicScript(%q, /) = %v；该格式创建的脚本必须能解析", host, err)
	}
	if got.ID != item.ID {
		t.Fatalf("解析到错误的脚本：got=%d want=%d", got.ID, item.ID)
	}
	// 反例：baseDomain 本身（x.a.com）不是脚本主机，不能解析。
	if _, err := ResolvePublicScriptWithDB(db, "x.a.com", ""); err == nil {
		t.Fatal("baseDomain 本身不该被当成脚本主机")
	}
	// 反例：别的子域也不行。
	if _, err := ResolvePublicScriptWithDB(db, "other.x.a.com", ""); err == nil {
		t.Fatal("未分配的 shortCode 不该解析")
	}
}

// ────────────── 场景 B：`a.com/*` 路径格式 ──────────────

func TestScriptDomainEndToEndPathFormatResolves(t *testing.T) {
	db := newScriptTestDB(t)
	owner, _, version := seedDomainCreationFixture(t, db, "path")
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"a.com/*"}}); err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "path", VersionID: version.ID, BaseDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("按 a.com/* 创建脚本失败：%v", err)
	}
	// ⚠️ 本用例的核心：路径格式下脚本必须走**路径形态**（useWildcard=false）。
	// 若默认恒为 true，公开地址是根路径 ""，而路径规则要求至少一段 ⇒ 永远解析不到。
	if item.UseWildcardDomain {
		t.Fatal("a.com/* 格式下脚本必须走路径形态（useWildcard=false），否则公开地址是根路径、永远解析不到")
	}
	host := item.ShortCode + ".a.com"
	got, err := ResolvePublicScriptWithDB(db, host, "/"+item.ShortCode)
	if err != nil {
		t.Fatalf("ResolvePublicScript(%q, /%s) = %v；该格式创建的脚本必须能解析", host, item.ShortCode, err)
	}
	if got.ID != item.ID {
		t.Fatalf("解析到错误的脚本：got=%d want=%d", got.ID, item.ID)
	}
	// 根路径在路径格式下不是脚本入口（Q3：`a.com/*` 不含根路径）。
	if _, err := ResolvePublicScriptWithDB(db, host, ""); err == nil {
		t.Fatal("路径格式下根路径不该解析到脚本")
	}
}

// ────────────── 场景 C：两种格式并存 ──────────────

func TestScriptDomainEndToEndMixedFormatsBothResolve(t *testing.T) {
	db := newScriptTestDB(t)
	owner, _, version := seedDomainCreationFixture(t, db, "mixed")
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains: []string{"*.b.net", "c.org/*"},
	}); err != nil {
		t.Fatal(err)
	}
	// 泛域名格式下建的脚本
	wild, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "wild", VersionID: version.ID, BaseDomain: "x.b.net",
	})
	if err != nil {
		t.Fatalf("混合配置下按 *.b.net 创建失败：%v", err)
	}
	// 路径格式下建的脚本
	path, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "path", VersionID: version.ID, BaseDomain: "c.org",
	})
	if err != nil {
		t.Fatalf("混合配置下按 c.org/* 创建失败：%v", err)
	}
	if !wild.UseWildcardDomain {
		t.Fatal("*.b.net 覆盖的 baseDomain 应走独立主机形态")
	}
	if path.UseWildcardDomain {
		t.Fatal("c.org/* 覆盖的 baseDomain 应走路径形态")
	}
	// 两个都能解析（多格式并存不能互相干扰）。
	if _, err := ResolvePublicScriptWithDB(db, wild.ShortCode+".x.b.net", ""); err != nil {
		t.Fatalf("泛域名脚本解析失败：%v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, path.ShortCode+".c.org", "/"+path.ShortCode); err != nil {
		t.Fatalf("路径脚本解析失败：%v", err)
	}
}

// ────────────── 场景 D：不在允许格式内的域名建不出来 ──────────────

func TestScriptDomainEndToEndRejectsUnlistedDomainOnCreate(t *testing.T) {
	db := newScriptTestDB(t)
	owner, _, version := seedDomainCreationFixture(t, db, "reject")
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.a.com"}}); err != nil {
		t.Fatal(err)
	}
	// `a.com` 本身不在 `*.a.com` 的覆盖范围内（Q2）。
	if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "bare", VersionID: version.ID, BaseDomain: "a.com",
	}); err == nil {
		t.Fatal("只有 *.a.com 时，baseDomain=a.com 必须被拒绝（Q2：泛域名不含裸域）")
	}
	// 完全无关的域名同样拒绝。
	if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "other", VersionID: version.ID, BaseDomain: "other.test",
	}); err == nil {
		t.Fatal("未列出的域名必须被拒绝")
	}
	// 被覆盖的子域可以建。
	if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "ok", VersionID: version.ID, BaseDomain: "y.a.com",
	}); err != nil {
		t.Fatalf("被 *.a.com 覆盖的子域应当可以创建：%v", err)
	}
}
