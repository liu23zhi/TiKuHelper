package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func refundAgreementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:refund-agreement-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestSetRefundAgreementTextWithDBAcceptsLongText(t *testing.T) {
	db := refundAgreementTestDB(t)
	text := strings.Repeat("退款条款", 6251) // 25,004 runes; exceeds the removed limit.
	if err := SetRefundAgreementTextWithDB(db, text); err != nil {
		t.Fatalf("save long agreement: %v", err)
	}
	if got := GetRefundAgreementTextWithDB(db); got != text {
		t.Fatalf("saved agreement length = %d runes, want %d", len([]rune(got)), len([]rune(text)))
	}
}

func TestSetRefundAgreementTextWithDBRejectsEmptyText(t *testing.T) {
	db := refundAgreementTestDB(t)
	for _, text := range []string{"", " \r\n \t"} {
		if err := SetRefundAgreementTextWithDB(db, text); !errors.Is(err, ErrRefundAgreementInvalid) {
			t.Fatalf("SetRefundAgreementTextWithDB(%q) error = %v, want ErrRefundAgreementInvalid", text, err)
		}
	}
}

func TestSetRefundAgreementTextWithDBNormalizesLineEndingsAndReplacesCurrentText(t *testing.T) {
	db := refundAgreementTestDB(t)
	if err := SetRefundAgreementTextWithDB(db, " first\r\nsecond "); err != nil {
		t.Fatal(err)
	}
	if got, want := GetRefundAgreementTextWithDB(db), "first\nsecond"; got != want {
		t.Fatalf("agreement = %q, want %q", got, want)
	}
	if err := SetRefundAgreementTextWithDB(db, "latest"); err != nil {
		t.Fatal(err)
	}
	if got := GetRefundAgreementTextWithDB(db); got != "latest" {
		t.Fatalf("current agreement = %q, want latest", got)
	}
}
