package service

import (
	"encoding/json"
	"errors"
	"github.com/itihey/tikuAdapter/internal/entity"
	"os"
	"testing"
)

func TestPaymentIconSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/utils/paymentIconContract.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		ID    string `json:"id"`
		SVG   string `json:"svg"`
		Valid bool   `json:"valid"`
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.ID, func(t *testing.T) {
			err := ValidatePaymentMethodIcon(v.SVG)
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v error=%v", v.Valid, err)
			}
		})
	}
}
func TestPaymentIconRejectBeforePersistenceAndPreserveLegacy(t *testing.T) {
	db := newPaymentMethodTestDB(t)
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	raw := " \n<svg onload=\"INERT_ONLY\"><foreignObject/></svg> \n"
	legacy := entity.PaymentMethod{Name: "legacy", IconSVG: raw, EPayType: "fixture", Enabled: true, Sort: 1, Remark: "original"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	reads, err := ListPaymentMethodsWithDB(db, true)
	if err != nil || len(reads) != 1 || reads[0].IconSVG != "" {
		t.Fatalf("unsafe legacy must be hidden only in API response: %+v %v", reads, err)
	}
	if _, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "new", IconSVG: raw, EPayType: "fixture"}); !errors.Is(err, ErrPaymentMethodIconUnsafe) {
		t.Fatalf("create=%v", err)
	}
	if _, err := UpdatePaymentMethodWithDB(db, legacy.ID, UpdatePaymentMethodInput{Name: "changed", IconSVG: raw, EPayType: "changed", Enabled: false, Sort: 9, Remark: "changed"}); !errors.Is(err, ErrPaymentMethodIconUnsafe) {
		t.Fatalf("update=%v", err)
	}
	var count int64
	if err := db.Model(&entity.PaymentMethod{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("persisted %d %v", count, err)
	}
	var after entity.PaymentMethod
	if err := db.First(&after, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after != legacy {
		t.Fatalf("unsafe update changed row: %+v %+v", legacy, after)
	}
	if _, err := UpdatePaymentMethodWithDB(db, legacy.ID, UpdatePaymentMethodInput{Name: "legacy", IconSVG: "<svg><path d=\"M0 0h1\"/></svg>", EPayType: "fixture"}); err != nil {
		t.Fatal(err)
	}
}
