package service

import (
	"encoding/json"
	"testing"
)

func TestDisabledInvalidQuotaPolicyDirectGoValidHardCapPresence(t *testing.T) {
	for _, status := range []string{"disabled", ""} {
		t.Run("omitted_"+status, func(t *testing.T) {
			db, u, k := dqServiceFixture(t, 25, "disabled")
			before := dqStored(t, db, k.ID)
			_, err := UpdateAccessTokenWithDB(db, u, k.ID, UpdateAccessTokenInput{Name: k.Name, Status: status, Remark: "metadata"})
			if err != nil {
				t.Fatal(err)
			}
			after := dqStored(t, db, k.ID)
			if after.TotalQuotaLimit != 25 || after.Status != before.Status || after.TotalAmountUsed != before.TotalAmountUsed {
				t.Error("omitted Go limit/status reset a valid disabled hard cap")
			}
		})
	}
	t.Run("explicit_zero_wire_intent", func(t *testing.T) {
		db, u, k := dqServiceFixture(t, 25, "disabled")
		var in UpdateAccessTokenInput
		if err := json.Unmarshal([]byte(`{"name":"disabled_bad","status":"disabled","totalQuotaLimit":0}`), &in); err != nil {
			t.Fatal(err)
		}
		_, err := UpdateAccessTokenWithDB(db, u, k.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		after := dqStored(t, db, k.ID)
		if after.TotalQuotaLimit != 0 || after.Status != "disabled" {
			t.Error("explicit legal zero intent was lost")
		}
	})
}

func TestDisabledInvalidQuotaPolicyRawUnderflowPresence(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		reject    bool
	}{{"tiny_positive", "1e-400", true}, {"tiny_negative", "-1e-400", true}, {"zero", "0", false}, {"negative_zero", "-0", false}, {"fraction_zero", "0.0", false}, {"exponent_zero", "0e-400", false}} {
		t.Run(c.name, func(t *testing.T) {
			db, u, k := dqServiceFixture(t, -2, "disabled")
			before := dqStored(t, db, k.ID)
			var in UpdateAccessTokenInput
			err := json.Unmarshal([]byte(`{"name":"disabled_bad","status":"enabled","totalQuotaLimit":`+c.raw+`}`), &in)
			if err == nil {
				_, err = UpdateAccessTokenWithDB(db, u, k.ID, in)
			}
			after := dqStored(t, db, k.ID)
			if c.reject {
				if err == nil {
					t.Error("nonzero JSON underflow became unlimited")
				}
				if after.TotalQuotaLimit != before.TotalQuotaLimit || after.Status != before.Status || after.TotalAmountUsed != before.TotalAmountUsed {
					t.Error("underflow mutated disabled key")
				}
			} else if err != nil || after.TotalQuotaLimit != 0 || after.Status != "enabled" {
				t.Error("mathematical explicit zero intent was rejected")
			}
		})
	}
}
