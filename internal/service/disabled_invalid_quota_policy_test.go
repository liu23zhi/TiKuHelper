package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"math"
	"reflect"
	"testing"
)

func dqServiceFixture(t *testing.T, limit float64, status string) (*gorm.DB, entity.User, entity.AccessToken) {
	t.Helper()
	db := newAccessTokenTestDB(t)
	u := seedAccessTokenOwner(t, db, 701, "user")
	k := entity.AccessToken{UserID: u.ID, Name: "disabled_bad", Status: status, TotalQuotaLimit: limit, TotalAmountUsed: 0.123456, TokenHash: hashAccessTokenValue("tk_synthetic_disabled_quota"), TokenValue: "tk_synthetic_disabled_quota", TokenPrefix: "tk_synthetic", Remark: "before"}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	return db, u, k
}
func dqDecode(t *testing.T, raw string) (UpdateAccessTokenInput, error) {
	t.Helper()
	var in UpdateAccessTokenInput
	err := json.Unmarshal([]byte(raw), &in)
	return in, err
}
func dqStored(t *testing.T, db *gorm.DB, id int32) entity.AccessToken {
	t.Helper()
	var k entity.AccessToken
	if err := db.First(&k, id).Error; err != nil {
		t.Fatal(err)
	}
	return k
}
func TestDisabledInvalidQuotaPolicyMetadataAndEnable(t *testing.T) {
	cases := []struct {
		name, raw                  string
		wantError, repair, enabled bool
	}{
		{"metadata_missing", `{"name":"disabled_bad","remark":"after"}`, false, false, false},
		{"enable_missing", `{"name":"disabled_bad","status":"enabled"}`, true, false, false},
		{"enable_null", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":null}`, true, false, false},
		{"explicit_zero_fix", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":0}`, false, true, true},
		{"explicit_positive_fix", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":2.345678}`, false, true, true},
		{"negative_fix_rejected", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":-0.00000001}`, true, false, false},
		{"upper_round_rejected", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":999999999999.9999999}`, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, u, k := dqServiceFixture(t, -2, "disabled")
			before := dqStored(t, db, k.ID)
			in, err := dqDecode(t, c.raw)
			if err == nil {
				_, err = UpdateAccessTokenWithDB(db, u, k.ID, in)
			}
			after := dqStored(t, db, k.ID)
			if c.wantError {
				if err == nil {
					t.Error("unsafe update accepted")
				}
				if !reflect.DeepEqual(before, after) {
					t.Error("rejected update changed complete token")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !c.repair {
				if after.TotalQuotaLimit != before.TotalQuotaLimit || after.Status != before.Status || after.TotalAmountUsed != before.TotalAmountUsed {
					t.Error("metadata omitted quota/status silently changed disabled legacy quota")
				}
			} else {
				if after.Status != "enabled" || after.TotalQuotaLimit != in.TotalQuotaLimit || after.TotalAmountUsed != before.TotalAmountUsed {
					t.Error("explicit valid repair+enable was not atomic")
				}
			}
		})
	}
}
func TestDisabledInvalidQuotaPolicyDirectCallAndFreshTX(t *testing.T) {
	t.Run("direct_default_zero_not_repair", func(t *testing.T) {
		db, u, k := dqServiceFixture(t, -2, "disabled")
		before := dqStored(t, db, k.ID)
		_, err := UpdateAccessTokenWithDB(db, u, k.ID, UpdateAccessTokenInput{Name: k.Name, Status: "enabled"})
		if err == nil {
			t.Error("service default zero enabled malformed key")
		}
		if !reflect.DeepEqual(before, dqStored(t, db, k.ID)) {
			t.Error("service default zero mutated key")
		}
	})
	t.Run("fresh_row_not_initial_snapshot", func(t *testing.T) {
		db, u, k := dqServiceFixture(t, 5, "disabled")
		changed := false
		if err := db.Callback().Query().After("gorm:query").Register("dq:concurrent-before-tx", func(tx *gorm.DB) {
			v, ok := tx.Statement.Dest.(*entity.AccessToken)
			if !ok || changed || v.ID != k.ID {
				return
			}
			changed = true
			if err := db.Model(&entity.AccessToken{}).Where("id = ?", k.ID).Updates(map[string]any{"total_quota_limit": -2, "status": "disabled"}).Error; err != nil {
				tx.AddError(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		in, err := dqDecode(t, `{"name":"disabled_bad","status":"enabled"}`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = UpdateAccessTokenWithDB(db, u, k.ID, in)
		if err == nil {
			t.Error("stale initial limit bypassed fresh TX")
		}
		after := dqStored(t, db, k.ID)
		if !changed || after.TotalQuotaLimit != -2 || after.Status != "disabled" {
			t.Error("concurrent raw limit/state was overwritten")
		}
	})
	for i, limit := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprintf("nonfinite_%d", i), func(t *testing.T) {
			db, u, k := dqServiceFixture(t, -2, "disabled")
			_, err := UpdateAccessTokenWithDB(db, u, k.ID, UpdateAccessTokenInput{Name: k.Name, Status: "enabled", TotalQuotaLimit: limit})
			if !errors.Is(err, ErrAccessTokenQuotaLimitInvalid) {
				t.Error("nonfinite Go quota accepted")
			}
		})
	}
}
func TestDisabledInvalidQuotaPolicyRuntimeAuth(t *testing.T) {
	for i, limit := range []float64{-2, 1e24, math.Inf(1)} {
		t.Run(fmt.Sprintf("invalid_%d", i), func(t *testing.T) {
			db, _, k := dqServiceFixture(t, limit, "disabled")
			if _, err := ResolveAccessTokenFromBasicAuth(db, "Bearer "+k.TokenValue); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
				t.Error("disabled key authenticated")
			}
			if err := db.Model(&entity.AccessToken{}).Where("id = ?", k.ID).Update("status", "enabled").Error; err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveAccessTokenFromBasicAuth(db, "Bearer "+k.TokenValue); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
				t.Error("direct DB re-enable bypassed invalid quota guard")
			}
		})
	}
}
