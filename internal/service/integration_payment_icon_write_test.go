package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
)

func TestIntegrationPaymentIconPersistence(t *testing.T) {
	bad := `<svg onload="INERT_ONLY"><foreignObject/></svg>`
	good := `<svg viewBox="0 0 24 24"><path d="M0 0h1"/></svg>`
	for _, kind := range []string{"invalid_create", "invalid_update", "legacy_read", "static_valid"} {
		t.Run(kind, func(t *testing.T) {
			db := newPaymentMethodTestDB(t)
			sql, _ := db.DB()
			defer sql.Close()
			legacy := entity.PaymentMethod{Name: "legacy", IconSVG: bad, EPayType: "fixture", Enabled: true}
			if e := db.Create(&legacy).Error; e != nil {
				t.Fatal(e)
			}
			// Compare two persisted representations, not time.Time location metadata from Create.
			if e := db.First(&legacy, legacy.ID).Error; e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "invalid_create":
				_, e := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "new", IconSVG: bad, EPayType: "fixture"})
				if e == nil {
					t.Fatal("unsupported SVG must be rejected before insert")
				}
				var count int64
				db.Model(&entity.PaymentMethod{}).Count(&count)
				if count != 1 {
					t.Fatal("rejected write persisted")
				}
			case "invalid_update":
				_, e := UpdatePaymentMethodWithDB(db, legacy.ID, UpdatePaymentMethodInput{Name: "changed", IconSVG: bad, EPayType: "other"})
				if e == nil {
					t.Fatal("unsupported SVG must be rejected before update")
				}
				var after entity.PaymentMethod
				if e := db.First(&after, legacy.ID).Error; e != nil || after != legacy {
					t.Fatal("invalid update changed historical row")
				}
			case "legacy_read":
				got, e := ListPaymentMethodsWithDB(db, true)
				if e != nil || len(got) != 1 || got[0].IconSVG != "" {
					t.Fatal("API read must hide unsafe historical icon")
				}
				var after entity.PaymentMethod
				if e := db.First(&after, legacy.ID).Error; e != nil || after != legacy {
					t.Fatal("API filtering must not rewrite historical DB row")
				}
			case "static_valid":
				_, e := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "safe", IconSVG: good, EPayType: "fixture"})
				if e != nil {
					t.Fatal("finite static SVG rejected", e)
				}
			}
		})
	}
}
