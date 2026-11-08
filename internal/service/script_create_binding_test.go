package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const scriptCreationBindingMessage = "创建脚本前请绑定手机号和微信"

func TestScriptCreateRequiresBothFreshAccountBindings(t *testing.T) {
	phone, wechat, blank := "13800000000", "synthetic-script-wechat", " "
	for _, tc := range []struct {
		name, role         string
		phone, wechat      *string
		operatorClaimsBoth bool
		wantCreated        bool
	}{
		{name: "neither", role: "user"},
		{name: "phone-only", role: "user", phone: &phone},
		{name: "wechat-only", role: "user", wechat: &wechat},
		{name: "blank-wechat", role: "user", phone: &phone, wechat: &blank},
		{name: "blank-phone", role: "user", phone: &blank, wechat: &wechat},
		{name: "stale-operator", role: "user", phone: &phone, operatorClaimsBoth: true},
		{name: "admin-not-exempt", role: "admin", phone: &phone, operatorClaimsBoth: true},
		{name: "both-in-db", role: "user", phone: &phone, wechat: &wechat, wantCreated: true},
		{name: "both-in-db-admin", role: "admin", phone: &phone, wechat: &wechat, wantCreated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, input := newScriptCreationBindingFixture(t)
			owner := entity.User{Username: "owner", Role: tc.role, Balance: 50, Phone: tc.phone, WechatID: tc.wechat}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			operator := entity.User{ID: owner.ID, Role: tc.role}
			if tc.operatorClaimsBoth {
				operator.Phone, operator.WechatID = &phone, &wechat
			}
			if !tc.wantCreated {
				expectNoScriptCreationWrites(t, db)
			}
			created, err := CreateUserScriptWithDB(db, operator, input)
			if tc.wantCreated {
				if err != nil || created.ID == 0 || created.AccessTokenID == 0 {
					t.Fatalf("fresh bound owner could not create a script: id=%d, err=%v", created.ID, err)
				}
				assertScriptCreationState(t, db, owner.ID, 40, 1)
				return
			}
			if err == nil || err.Error() != scriptCreationBindingMessage || created.ID != 0 {
				t.Errorf("want stable binding rejection and no created script; id=%d, err=%v", created.ID, err)
			}
			assertScriptCreationState(t, db, owner.ID, 50, 0)
		})
	}
}

func TestScriptCreateUnboundOwnerMayReadCompletedReceiptOnly(t *testing.T) {
	db, input := newScriptCreationBindingFixture(t)
	owner := entity.User{Username: "owner", Role: "user", Balance: 50}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	created, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&owner).Updates(map[string]any{"wechat_id": nil, "wechat_bound_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	other := entity.User{Username: "other", Role: "user", Balance: 50}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	expectNoScriptCreationWrites(t, db)
	replay, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil || replay.ID != created.ID {
		t.Fatalf("own completed receipt should replay: id=%d, err=%v", replay.ID, err)
	}
	conflict := input
	conflict.Name = "different input"
	if _, err := CreateUserScriptWithDB(db, owner, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("changed payload must not replay, got %v", err)
	}
	newKey := input
	newKey.IdempotencyKey = "new-create"
	for _, attempt := range []struct {
		actor entity.User
		input CreateUserScriptInput
	}{{owner, newKey}, {other, input}} {
		if _, err := CreateUserScriptWithDB(db, attempt.actor, attempt.input); err == nil || err.Error() != scriptCreationBindingMessage {
			t.Errorf("fresh attempt or other-owner receipt must be rejected, got %v", err)
		}
	}
	assertScriptCreationState(t, db, owner.ID, 40, 1)
}

func TestScriptCreateExpiredReceiptCannotRenewWhileUnbound(t *testing.T) {
	db, input := newScriptCreationBindingFixture(t)
	owner := entity.User{Username: "owner", Role: "user", Balance: 50}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUserScriptWithDB(db, owner, input); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&owner).Updates(map[string]any{"phone": nil, "phone_bound_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.IdempotencyRecord{}).Where("operation = ?", "script-create").Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	expectNoScriptCreationWrites(t, db)
	if _, err := CreateUserScriptWithDB(db, owner, input); err == nil || err.Error() != scriptCreationBindingMessage {
		t.Errorf("expired receipt cannot authorize a new script: %v", err)
	}
	assertScriptCreationState(t, db, owner.ID, 40, 1)
}

func newScriptCreationBindingFixture(t *testing.T) (*gorm.DB, CreateUserScriptInput) {
	t.Helper()
	db := newScriptTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{Username: "uploader", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "fixture", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	return db, CreateUserScriptInput{Name: "fixture", BaseDomain: "fixture.test", VersionID: version.ID, IdempotencyKey: "create-fixture"}
}

func bindSyntheticScriptOwner(owner *entity.User) {
	phone, wechat, now := "13800000000", "synthetic-script-wechat", time.Now()
	owner.Phone, owner.WechatID = &phone, &wechat
	owner.PhoneBoundAt, owner.WechatBoundAt = &now, &now
}

func expectNoScriptCreationWrites(t *testing.T, db *gorm.DB) {
	t.Helper()
	writes := 0
	record := func(tx *gorm.DB) { writes++ }
	callbacks := db.Callback()
	for _, err := range []error{
		callbacks.Create().Before("gorm:create").Register("test:script-create-no-write", record),
		callbacks.Update().Before("gorm:update").Register("test:script-update-no-write", record),
		callbacks.Delete().Before("gorm:delete").Register("test:script-delete-no-write", record),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if writes != 0 {
			t.Errorf("creation rejection or receipt replay attempted %d writes", writes)
		}
	})
}

func assertScriptCreationState(t *testing.T, db *gorm.DB, userID int32, balance float64, count int64) {
	t.Helper()
	var owner entity.User
	if err := db.First(&owner, userID).Error; err != nil {
		t.Fatal(err)
	}
	if owner.Balance != balance {
		t.Errorf("wallet = %g, want %g", owner.Balance, balance)
	}
	for _, table := range []any{&entity.UserScript{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.IdempotencyRecord{}} {
		var got int64
		if err := db.Model(table).Count(&got).Error; err != nil || got != count {
			t.Errorf("%T count = %d, want %d; err=%v", table, got, count, err)
		}
	}
}
