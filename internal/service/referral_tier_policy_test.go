package service

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestT11AutomaticPolicyRequiresMatchAndActualCredit(t *testing.T) {
	const rules = "{\"enabled\":true,\"rules\":[{\"enabled\":false,\"lowerCents\":0,\"upperCents\":null,\"rateUnits\":50000000},{\"enabled\":true,\"lowerCents\":0,\"upperCents\":1000,\"rateUnits\":2000000},{\"enabled\":true,\"lowerCents\":1000,\"upperCents\":2000,\"rateUnits\":0},{\"enabled\":true,\"lowerCents\":0,\"upperCents\":null,\"rateUnits\":3000000}]}"
	cases := []struct {
		name, policy string
		paid         float64
		credited     bool
		want         float64
	}{
		{"no legacy fallback", "", 9.99, true, 0},
		{"empty", "{\"enabled\":true,\"rules\":[]}", 9.99, true, 0},
		{"switch off", "{\"enabled\":false,\"rules\":[{\"enabled\":true,\"lowerCents\":0,\"upperCents\":null,\"rateUnits\":2000000}]}", 9.99, true, 0},
		{"gap", "{\"enabled\":true,\"rules\":[{\"enabled\":true,\"lowerCents\":1000,\"upperCents\":2000,\"rateUnits\":2000000}]}", 9.99, true, 0},
		{"low tier shadows later overlap", rules, 9.99, true, 0.1998},
		{"zero tier shadows later overlap", rules, 10, true, 0},
		{"upper boundary exclusive", rules, 20, true, 0.60},
		{"paid label is not a credit", rules, 20, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newReferralTestDB(t)
			// Old scalar config must not opt in to the new rule policy.
			if err := saveSystemSettingValue(db, ReferralRebateEnabledSettingKey, "true", ""); err != nil {
				t.Fatal(err)
			}
			if err := saveSystemSettingValue(db, ReferralRebateRateSettingKey, "0.25", ""); err != nil {
				t.Fatal(err)
			}
			if c.policy != "" {
				if err := saveSystemSettingValue(db, "referral_auto_rebate_rules", c.policy, ""); err != nil {
					t.Fatal(err)
				}
			}
			seedReferralUser(t, db, 201, "tier-inviter", 0)
			invitee := seedReferralUser(t, db, 202, "tier-invitee", 201)
			order := seedReferralPaidOrder(t, db, "T11-TIER-ORDER", invitee.ID, c.paid)
			if c.credited {
				record := entity.BillingRecord{UserID: invitee.ID, RelatedOrderID: order.ID, RequestID: order.OrderNo, Type: "recharge", Amount: c.paid}
				if err := db.Create(&record).Error; err != nil {
					t.Fatal(err)
				}
			}
			var got *entity.ReferralRebate
			if err := db.Transaction(func(tx *gorm.DB) error {
				var err error
				got, err = CreateReferralRebateInTx(tx, order)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if c.want == 0 {
				if got != nil {
					t.Fatalf("expected no automatic rebate, got amount=%v", got.Amount)
				}
			} else if got == nil || got.Amount != c.want || got.BaseAmount != c.paid || got.Status != entity.ReferralRebateStatusPending {
				t.Fatalf("expected a pending rebate of %.6f for this credited order, got %+v", c.want, got)
			}
		})
	}
}
