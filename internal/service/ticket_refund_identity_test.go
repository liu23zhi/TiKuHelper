package service

import (
	"bytes"
	"errors"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func withTestRefundIdentity(t *testing.T, db *gorm.DB, owner entity.User, input CreateTicketRefundInput) CreateTicketRefundInput {
	t.Helper()
	qr := seedPayoutQrDraft(t, db, owner.ID)
	signature := seedPayoutQrDraft(t, db, owner.ID)
	input.PayoutQrAttachmentID, input.SignatureAttachmentID = &qr.ID, &signature.ID
	input.AgreementText = GetRefundAgreementTextWithDB(db)
	return input
}

func TestManualRefundRequiresFreshOwnedSignatureAndCurrentAgreement(t *testing.T) {
	db, owner, other, _ := newTicketRefundCoreTestDB(t)
	input := withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{Kind: TicketRefundKindManual, RequestedCents: 100})
	missing := input
	missing.SignatureAttachmentID = nil
	if _, err := CreateTicketRefundWithDB(db, owner, missing, nil); !errors.Is(err, ErrRefundAgreementRequired) {
		t.Fatalf("missing signature: %v", err)
	}
	foreign := seedPayoutQrDraft(t, db, other.ID)
	missing.SignatureAttachmentID = &foreign.ID
	if _, err := CreateTicketRefundWithDB(db, owner, missing, nil); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("foreign signature: %v", err)
	}
	missing = input
	missing.AgreementText += " changed"
	if _, err := CreateTicketRefundWithDB(db, owner, missing, nil); !errors.Is(err, ErrRefundAgreementRequired) {
		t.Fatalf("stale text: %v", err)
	}
	row, err := CreateTicketRefundWithDB(db, owner, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.AgreementText != input.AgreementText || row.AgreementSignedAt == nil {
		t.Fatal("agreement snapshot missing")
	}
	if _, err := CreateTicketRefundWithDB(db, owner, input, nil); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("reused signature: %v", err)
	}
	if err := SetRefundAgreementTextWithDB(db, "新的协议"); err != nil {
		t.Fatal(err)
	}
	stored, err := GetTicketRefundWithDB(db, owner, row.ID)
	if err != nil || stored.AgreementText != input.AgreementText {
		t.Fatal("admin edit changed the signed snapshot")
	}
}

func TestManualRefundSMSConsumedOnlyWithSuccessfulSubmission(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	seedSMSSettings(t, db)
	phone := "13800138000"
	if err := db.Model(&owner).Update("phone", phone).Error; err != nil {
		t.Fatal(err)
	}
	status, err := RefundPhoneVerificationStatusWithDB(db, owner)
	if err != nil || !status.Required || status.PhoneMasked != "138****8000" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	input := withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{Kind: TicketRefundKindManual, RequestedCents: 100})
	if _, err := CreateTicketRefundWithDB(db, owner, input, nil); !errors.Is(err, ErrRefundPhoneVerification) {
		t.Fatalf("without code: %v", err)
	}
	code := entity.SMSVerificationCode{Phone: phone, Scene: SMSSceneRefund, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	input.PhoneSMSCode = "123456"
	input.RequestedCents = 100_000
	if _, err := CreateTicketRefundWithDB(db, owner, input, nil); !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("overdraw: %v", err)
	}
	if err := db.First(&code, code.ID).Error; err != nil || code.Status != SMSCodeStatusPending {
		t.Fatal("failed submission consumed the SMS")
	}
	input.RequestedCents = 100
	if _, err := CreateTicketRefundWithDB(db, owner, input, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&code, code.ID).Error; err != nil || code.Status != SMSCodeStatusUsed {
		t.Fatal("successful submission did not consume SMS")
	}
}

func TestRefundPhoneSMSUsesBoundPhoneAndDedicatedScene(t *testing.T) {
	db, owner, other, _ := newTicketRefundCoreTestDB(t)
	if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	phone := "13800138000"
	if err := db.Model(&owner).Update("phone", phone).Error; err != nil {
		t.Fatal(err)
	}
	if status, err := RefundPhoneVerificationStatusWithDB(db, owner); err != nil || status.Required {
		t.Fatalf("unconfigured SMS should be optional: %+v %v", status, err)
	}
	seedSMSSettings(t, db)
	original := smsSender
	t.Cleanup(func() { smsSender = original })
	sent := 0
	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(gotPhone, content string) error {
			if gotPhone != phone {
				t.Fatalf("unexpected destination: %s", gotPhone)
			}
			sent++
			return nil
		})
	}
	if err := SendRefundPhoneSMSCodeWithDB(db, owner, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := SendRefundPhoneSMSCodeWithDB(db, owner, "127.0.0.1"); err == nil {
		t.Fatal("cooldown bypassed")
	}
	if err := SendRefundPhoneSMSCodeWithDB(db, other, "127.0.0.1"); !errors.Is(err, ErrRefundPhoneVerification) {
		t.Fatalf("unbound actor: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent %d SMS", sent)
	}
	var row entity.SMSVerificationCode
	if err := db.Last(&row).Error; err != nil || row.Scene != SMSSceneRefund || row.Phone != phone {
		t.Fatalf("wrong SMS record: %+v %v", row, err)
	}
}

func TestRefundPayoutQrPreviewDoesNotExposeSourceAndBlocksGenericRead(t *testing.T) {
	db, owner, other, admin := newTicketRefundCoreTestDB(t)
	useTicketAttachmentTestKey(t)
	if err := db.AutoMigrate(&entity.TicketAttachment{}, &entity.Ticket{}, &entity.TicketMessage{}); err != nil {
		t.Fatal(err)
	}
	body := ticketAttachmentTestPNG(t, 100, 80)
	qr, err := StoreTicketAttachmentWithDB(db, &owner, 0, body)
	if err != nil {
		t.Fatal(err)
	}
	input := withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{Kind: TicketRefundKindManual, RequestedCents: 100})
	input.PayoutQrAttachmentID = &qr.ID
	row, err := CreateTicketRefundWithDB(db, owner, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", qr.ID).Update("scan_state", entity.TicketAttachmentScanApproved).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, &owner, qr.ID); !errors.Is(err, ErrTicketForbidden) {
		t.Fatalf("generic endpoint bypasses password: %v", err)
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, &admin, qr.ID); err != nil {
		t.Fatalf("admin read: %v", err)
	}
	hasPrevious, preview, err := RefundPayoutQrPreviewWithDB(db, owner)
	if err != nil || !hasPrevious || len(preview) == 0 || bytes.Equal(preview, body) {
		t.Fatalf("preview invalid: %v", err)
	}
	image, err := png.Decode(bytes.NewReader(preview))
	if err != nil || image.Bounds().Dx() != 240 || image.Bounds().Dy() != 240 {
		t.Fatal("invalid preview dimensions")
	}
	// Tiny high-frequency source details must disappear from the thumbnail.
	r0, _, _, _ := image.At(100, 100).RGBA()
	r1, _, _, _ := image.At(101, 100).RGBA()
	if r0 > r1+2000 || r1 > r0+2000 {
		t.Fatal("preview retained sharp source details")
	}
	id, _, recovered, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || id != qr.ID || len(recovered) == 0 {
		t.Fatalf("authorized recovery failed: %v", err)
	}
	if exists, bytes, err := RefundPayoutQrPreviewWithDB(db, other); err != nil || exists || len(bytes) > 0 {
		t.Fatalf("foreign history leaked: %v", err)
	}
	if row.TicketID == nil {
		t.Fatal("missing shell")
	}
}

func TestRefundCeilFreezesActualBalanceAndFinalizesExactlyOnce(t *testing.T) {
	for _, outcome := range []string{TicketRefundStatusSucceeded, TicketRefundStatusFailed} {
		t.Run(outcome, func(t *testing.T) {
			db, owner, _, admin := newTicketRefundCoreTestDB(t)
			if err := db.Model(&owner).Update("balance", 10.004).Error; err != nil {
				t.Fatal(err)
			}
			order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 20, true)
			row, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 1001}, testVerifyTicketRefundChannel)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ReviewTicketRefundWithDB(db, admin, row.ID, true, 1001, testVerifyTicketRefundChannel); err != nil {
				t.Fatal(err)
			}
			requireTicketRefundWallet(t, db, owner.ID, 0)
			if _, err = BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err = ReconcileTicketRefundWithDB(db, admin, row.ID, outcome, "rounding-result"); err != nil {
					t.Fatal(err)
				}
			}
			wantBalance := int64(0)
			if outcome == TicketRefundStatusFailed {
				wantBalance = 10_004_000
			}
			requireTicketRefundWallet(t, db, owner.ID, wantBalance)
			var rows []entity.TicketRefundLedger
			if err := db.Where("refund_id = ?", row.ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0].AmountMicros != 10_004_000 || rows[1].AmountMicros != 10_004_000 {
				t.Fatalf("incorrect hold/settlement: %+v", rows)
			}
		})
	}
}

func TestGeneralTicketsRejectRefundFields(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	owner := seedRefundActor(t, db, 1, "mixed-refund-fields")
	attachmentID := int64(5)
	cases := map[string]CreateTicketInput{
		"signature":   {RefundSignatureAttachmentID: &attachmentID},
		"agreement":   {RefundAgreementText: "agreed"},
		"sms":         {RefundPhoneSMSCode: "123456"},
		"method":      {RefundMethod: "original"},
		"orders":      {RefundOrderIDs: []int32{1}},
		"allocations": {RefundOrderAllocations: []TicketRefundOrderAllocation{{OrderID: 1, Cents: 1}}},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			input.Subject, input.Body, input.TicketType = "ordinary subject", "ordinary body", TicketTypeGeneral
			if _, err := CreateTicketWithDB(db, &owner, input); !errors.Is(err, ErrTicketInvalidCategory) {
				t.Fatalf("direct creation ignored refund field: %v", err)
			}
			if _, err := CreateTicketForUserWithDB(db, &owner, input, nil); !errors.Is(err, ErrTicketInvalidCategory) {
				t.Fatalf("public creation ignored refund field: %v", err)
			}
		})
	}
}

func TestRefundBatchRejectsInconsistentTotalAndOverflow(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	for name, input := range map[string]CreateTicketRefundInput{
		"negative request": {RequestedCents: -1, Allocations: []TicketRefundOrderAllocation{{OrderID: 1, Cents: 100}}},
		"missing total":    {Allocations: []TicketRefundOrderAllocation{{OrderID: 1, Cents: 100}}},
		"different total":  {RequestedCents: 200, Allocations: []TicketRefundOrderAllocation{{OrderID: 1, Cents: 100}}},
		"over maximum":     {RequestedCents: ticketRefundMaxCents, Allocations: []TicketRefundOrderAllocation{{OrderID: 1, Cents: ticketRefundMaxCents}, {OrderID: 2, Cents: ticketRefundMaxCents}}},
	} {
		t.Run(name, func(t *testing.T) {
			input.Kind = TicketRefundKindOriginal
			if _, err := CreateTicketRefundsForOrdersWithDB(db, owner, input, nil); err == nil {
				t.Fatal("inconsistent batch accepted")
			}
		})
	}
}

func TestConcurrentRoundedRefundsCannotFreezeTheSameBalance(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	if err := db.Model(&owner).Update("balance", 10.004).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]entity.TicketRefund, 0, 2)
	for id := int32(1); id <= 2; id++ {
		order := seedTicketRefundOrder(t, db, id, owner.ID, "paid", time.Now().Add(-time.Duration(id)*time.Hour), 20, true)
		row, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 1001}, testVerifyTicketRefundChannel)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for _, row := range rows {
		workers.Add(1)
		go func(id int64) {
			defer workers.Done()
			_, err := ReviewTicketRefundWithDB(db, admin, id, true, 1001, testVerifyTicketRefundChannel)
			results <- err
		}(row.ID)
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrTicketRefundExceedsRefundablePrincipal) {
			t.Fatalf("unexpected approval error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("want one approved refund, got %d", succeeded)
	}
	requireTicketRefundWallet(t, db, owner.ID, 0)
	var holds []entity.TicketRefundLedger
	if err := db.Where("kind = ?", ticketRefundLedgerFreeze).Find(&holds).Error; err != nil {
		t.Fatal(err)
	}
	if len(holds) != 1 || holds[0].AmountMicros != 10_004_000 {
		t.Fatalf("duplicated or incorrect hold: %+v", holds)
	}
}
