package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newPaymentMethodTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:payment-method-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.PaymentMethod{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func TestCreatePaymentMethodWithDBTrimsFieldsAndPersists(t *testing.T) {
	db := newPaymentMethodTestDB(t)

	created, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{
		Name:    " 支付宝 ",
		IconSVG: " <svg>alipay</svg> ",
		EPayType: " alipay ",
		Enabled: true,
		Sort:    20,
		Remark:  " 常用支付方式 ",
	})
	if err != nil {
		t.Fatalf("create payment method: %v", err)
	}

	if created.Name != "支付宝" {
		t.Fatalf("expected trimmed name, got %q", created.Name)
	}
	if created.IconSVG != "<svg>alipay</svg>" {
		t.Fatalf("expected trimmed icon svg, got %q", created.IconSVG)
	}
	if created.EPayType != "alipay" {
		t.Fatalf("expected trimmed epay type, got %q", created.EPayType)
	}
	if !created.Enabled {
		t.Fatal("expected created payment method enabled")
	}
	if created.Sort != 20 {
		t.Fatalf("expected sort 20, got %d", created.Sort)
	}
	if created.Remark != "常用支付方式" {
		t.Fatalf("expected trimmed remark, got %q", created.Remark)
	}

	var stored entity.PaymentMethod
	if err := db.Where("id = ?", created.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored payment method: %v", err)
	}
	if stored.Name != "支付宝" || stored.EPayType != "alipay" || stored.IconSVG != "<svg>alipay</svg>" {
		t.Fatalf("unexpected stored payment method: %+v", stored)
	}
}

func TestCreatePaymentMethodWithDBValidatesRequiredFields(t *testing.T) {
	db := newPaymentMethodTestDB(t)

	if _, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "", EPayType: "alipay"}); err != ErrPaymentMethodNameRequired {
		t.Fatalf("expected ErrPaymentMethodNameRequired, got %v", err)
	}
	if _, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "支付宝", EPayType: ""}); err != ErrPaymentMethodEPayTypeRequired {
		t.Fatalf("expected ErrPaymentMethodEPayTypeRequired, got %v", err)
	}
}

func TestListPaymentMethodsWithDBFiltersDisabledAndSortsAscending(t *testing.T) {
	db := newPaymentMethodTestDB(t)

	items := []entity.PaymentMethod{
		{Name: "微信支付", EPayType: "wxpay", Enabled: true, Sort: 30},
		{Name: "银行卡", EPayType: "bank", Enabled: false, Sort: 10},
		{Name: "支付宝", EPayType: "alipay", Enabled: true, Sort: 10},
	}
	for _, item := range items {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("seed payment method: %v", err)
		}
	}

	adminList, err := ListPaymentMethodsWithDB(db, true)
	if err != nil {
		t.Fatalf("list all payment methods: %v", err)
	}
	if len(adminList) != 3 {
		t.Fatalf("expected 3 payment methods, got %d", len(adminList))
	}
	if adminList[0].Name != "银行卡" || adminList[1].Name != "支付宝" || adminList[2].Name != "微信支付" {
		t.Fatalf("unexpected admin order: %+v", adminList)
	}

	enabledList, err := ListPaymentMethodsWithDB(db, false)
	if err != nil {
		t.Fatalf("list enabled payment methods: %v", err)
	}
	if len(enabledList) != 2 {
		t.Fatalf("expected 2 enabled payment methods, got %d", len(enabledList))
	}
	if enabledList[0].Name != "支付宝" || enabledList[1].Name != "微信支付" {
		t.Fatalf("unexpected enabled order: %+v", enabledList)
	}
}

func TestUpdatePaymentMethodWithDBUpdatesFields(t *testing.T) {
	db := newPaymentMethodTestDB(t)
	created, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "支付宝", EPayType: "alipay", Enabled: true, Sort: 10})
	if err != nil {
		t.Fatalf("create payment method: %v", err)
	}

	updated, err := UpdatePaymentMethodWithDB(db, created.ID, UpdatePaymentMethodInput{
		Name:    " 微信支付 ",
		IconSVG: " <svg>wx</svg> ",
		EPayType: " wxpay ",
		Enabled: false,
		Sort:    99,
		Remark:  " 已停用 ",
	})
	if err != nil {
		t.Fatalf("update payment method: %v", err)
	}

	if updated.Name != "微信支付" || updated.EPayType != "wxpay" || updated.IconSVG != "<svg>wx</svg>" {
		t.Fatalf("unexpected updated payment method: %+v", updated)
	}
	if updated.Enabled {
		t.Fatal("expected updated payment method disabled")
	}
	if updated.Sort != 99 || updated.Remark != "已停用" {
		t.Fatalf("unexpected updated sort/remark: %+v", updated)
	}
}

func TestDeletePaymentMethodWithDBRemovesRecord(t *testing.T) {
	db := newPaymentMethodTestDB(t)
	created, err := CreatePaymentMethodWithDB(db, CreatePaymentMethodInput{Name: "支付宝", EPayType: "alipay", Enabled: true})
	if err != nil {
		t.Fatalf("create payment method: %v", err)
	}

	if err := DeletePaymentMethodWithDB(db, created.ID); err != nil {
		t.Fatalf("delete payment method: %v", err)
	}

	var count int64
	if err := db.Model(&entity.PaymentMethod{}).Where("id = ?", created.ID).Count(&count).Error; err != nil {
		t.Fatalf("count payment methods: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected payment method deleted, count=%d", count)
	}
}
