package registry

import (
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
)

func TestIntegrationBootstrapByteCAS(t *testing.T) {
	cases := []struct {
		name                 string
		old                  any
		original, concurrent string
		change, fail, inject bool
	}{
		{name: "exact_original", old: "Legacy-Password-A1!", original: "Legacy-Password-A1!"},
		{name: "case_only_concurrent_change", old: "Legacy-Password-A1!", original: "Legacy-Password-A1!", concurrent: "legacy-password-a1!", change: true, fail: true},
		{name: "different_concurrent_change", old: "Legacy-Password-A1!", original: "Legacy-Password-A1!", concurrent: "Different-Password-B2!", change: true, fail: true},
		{name: "empty_original", old: "", original: ""}, {name: "null_original", old: nil, original: ""},
		{name: "null_concurrent_change", old: nil, original: "", concurrent: "Concurrent-Password-C3!", change: true, fail: true},
		{name: "write_error", old: "Legacy-Password-A1!", original: "Legacy-Password-A1!", fail: true, inject: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, e := gorm.Open(sqlite.Open(fmt.Sprintf("file:bootstrap-cas-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
			if e != nil {
				t.Fatal(e)
			}
			sql, e := d.DB()
			if e != nil {
				t.Fatal(e)
			}
			defer sql.Close()
			// 这张表要配合 applyAdminBootstrap 的 CAS 语义（password 用
			// COLLATE NOCASE 才有「字节身份 vs 排序规则」的对照），
			// 因此不能直接用 createMinimalUsersTable；但同样必须带机制列
			// deleted_at，否则后续任何 entity.User 查询会被软删除条件打挂。
			if e := d.Exec("CREATE TABLE users(id INTEGER PRIMARY KEY,password TEXT COLLATE NOCASE,deleted_at datetime)").Error; e != nil {
				t.Fatal(e)
			}
			if e := d.Exec("INSERT INTO users(id,password) VALUES(1,?)", c.old).Error; e != nil {
				t.Fatal(e)
			}
			plan := adminBootstrapPlan{exists: true, original: c.original, password: "synthetic-upgraded-hash"}
			want := plan.password
			if c.change {
				if e := d.Exec("UPDATE users SET password=? WHERE id=1", c.concurrent).Error; e != nil {
					t.Fatal(e)
				}
			}
			if c.inject {
				if e := d.Callback().Update().Before("gorm:update").Register("synthetic-write-error", func(tx *gorm.DB) { tx.AddError(errors.New("synthetic write failure")) }); e != nil {
					t.Fatal(e)
				}
			}
			e = applyAdminBootstrap(d, plan)
			if (e != nil) != c.fail {
				t.Errorf("expected rejection=%v got=%v", c.fail, e)
			}
			if c.fail {
				want = c.original
				if c.change {
					want = c.concurrent
				}
			}
			var got string
			if e := d.Raw("SELECT COALESCE(password,'') FROM users WHERE id=1").Scan(&got).Error; e != nil {
				t.Fatal(e)
			}
			if got != want {
				t.Errorf("credential byte identity overwritten; case=%s", c.name)
			}
		})
	}
}
