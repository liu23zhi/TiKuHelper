package service

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type refundEPayFixture struct {
	db              *gorm.DB
	cfg             TicketRefundEPayConfig
	merchantPublic  *rsa.PublicKey
	platformPrivate *rsa.PrivateKey
	order           entity.RechargeOrder
	refund          entity.TicketRefund
}

func newRefundEPayFixture(t *testing.T) refundEPayFixture {
	t.Helper()
	db := newRechargeOrderTestDB(t)
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	platformPublic, platformPrivatePEM, _ := generateRechargeRSAKeys(t)
	_, merchantPrivate, merchantPublic := generateRechargeRSAKeys(t)
	platformPrivate, err := parseRechargePrivateKey(platformPrivatePEM)
	if err != nil {
		t.Fatal(err)
	}
	seedRechargePaymentSettings(t, db, platformPublic, merchantPrivate)
	t.Setenv("TIKU_REFUND_EPAY_PROFILE", "")
	if _, err := LoadTicketRefundEPayConfigWithDB(db); !errors.Is(err, ErrTicketRefundChannelDisabled) {
		t.Fatalf("original-channel refund must require an explicit profile: %v", err)
	}
	t.Setenv("TIKU_REFUND_EPAY_PROFILE", "rsa-pid-money-v2")
	cfg, err := LoadTicketRefundEPayConfigWithDB(db)
	if err != nil {
		t.Fatalf("load synthetic refund profile: %v", err)
	}
	order := entity.RechargeOrder{
		ID: 27, UserID: 91, OrderNo: "synthetic-order-27", Status: rechargeOrderStatusPaid,
		PaymentProvider: DefaultPaymentProvider, Amount: 50.25,
		ThirdPartyOrderNo:     "mutable-callback-trade-not-a-refund-reference",
		RefundOriginalTradeNo: "synthetic-proven-trade-27", RefundChannelFingerprint: cfg.fingerprint,
	}
	refund := entity.TicketRefund{
		ID: 31, UserID: order.UserID, OrderID: &order.ID,
		Kind: entity.TicketRefundKindOriginal, Status: entity.TicketRefundStatusProcessing,
		OrderNo: order.OrderNo, OriginalTradeNo: order.RefundOriginalTradeNo, ChannelRef: cfg.fingerprint,
		RefundNo: "synthetic-stable-refund-31", ApprovedCents: 1501, OrderPaidCents: 5025,
	}
	return refundEPayFixture{db, cfg, merchantPublic, platformPrivate, order, refund}
}

type refundEPayTestTransport func(*http.Request) (*http.Response, error)

func (f refundEPayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func installRefundEPayTransport(t *testing.T, f refundEPayTestTransport) {
	t.Helper()
	previous := refundEPayHTTPClient
	replacement := *previous
	replacement.Transport = f
	refundEPayHTTPClient = &replacement
	t.Cleanup(func() { refundEPayHTTPClient = previous })
}

func refundEPayResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
}

func (f refundEPayFixture) replyFields() url.Values {
	return url.Values{
		"code": {"0"}, "status": {"1"}, "pid": {f.cfg.partnerID},
		"money": {"15.01"}, "trade_no": {f.order.RefundOriginalTradeNo},
		"out_trade_no": {f.order.OrderNo}, "out_refund_no": {f.refund.RefundNo},
		"refund_no": {"synthetic-channel-refund-31"},
		"timestamp": {strconv.FormatInt(time.Now().Unix(), 10)},
	}
}

func signSyntheticRefundEPay(t *testing.T, values url.Values, key *rsa.PrivateKey) {
	t.Helper()
	digest := sha256.Sum256([]byte(buildRechargeSignContentForTest(values)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", base64.StdEncoding.EncodeToString(sig))
	values.Set("sign_type", rechargeOrderSignType)
}

func refundEPayJSON(t *testing.T, values url.Values) []byte {
	t.Helper()
	// Exercise the gateway's mixed string and number JSON without converting
	// the exact money or timestamp through a floating-point value.
	fields := make(map[string]any, len(values))
	for key, values := range values {
		switch key {
		case "money", "timestamp", "code", "status":
			fields[key] = json.Number(values[0])
		default:
			fields[key] = values[0]
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f refundEPayFixture) assertRequest(t *testing.T, r *http.Request, path string, expected map[string]string) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.String() != f.cfg.gatewayURL+path ||
		r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("wrong method/origin/path/content type: %s %s", r.Method, r.URL)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != len(expected)+4 {
		t.Fatalf("unexpected field count %d", len(values))
	}
	for name, want := range expected {
		if got := values.Get(name); got != want {
			t.Fatalf("field %s = %q, want %q", name, got, want)
		}
	}
	for name, fields := range values {
		if len(fields) != 1 {
			t.Fatalf("duplicate request field %s", name)
		}
	}
	ts, err := strconv.ParseInt(values.Get("timestamp"), 10, 64)
	if err != nil || ts < time.Now().Unix()-10 || ts > time.Now().Unix()+10 {
		t.Fatal("request timestamp not fresh")
	}
	if values.Get("pid") != f.cfg.partnerID || values.Get("sign_type") != rechargeOrderSignType {
		t.Fatal("request not bound to the configured merchant and RSA profile")
	}
	sig, err := base64.StdEncoding.DecodeString(values.Get("sign"))
	digest := sha256.Sum256([]byte(buildRechargeSignContentForTest(values)))
	if err != nil || rsa.VerifyPKCS1v15(f.merchantPublic, crypto.SHA256, digest[:], sig) != nil {
		t.Fatal("request signature not verifiable by the synthetic merchant public key")
	}
}

func TestTicketRefundEPaySignedSubmitOneAttemptAndDoesNotFinalize(t *testing.T) {
	f := newRefundEPayFixture(t)
	// Persist a synthetic processing record. A terminal-looking submit reply
	// must not close it or synthesize a refund ledger entry.
	if err := f.db.AutoMigrate(&entity.TicketRefund{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&f.refund).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	installRefundEPayTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		f.assertRequest(t, r, "/api/pay/refund", map[string]string{
			"trade_no": f.order.RefundOriginalTradeNo, "out_refund_no": f.refund.RefundNo, "money": "15.01",
		})
		reply := f.replyFields()
		signSyntheticRefundEPay(t, reply, f.platformPrivate)
		return refundEPayResponse(http.StatusOK, refundEPayJSON(t, reply)), nil
	})
	if err := SubmitTicketRefundEPay(context.Background(), f.cfg, f.refund, f.order); err != nil {
		t.Fatalf("signed acknowledgement was rejected: %v", err)
	}
	if calls != 1 {
		t.Fatalf("submit executed %d POSTs", calls)
	}
	var persisted entity.TicketRefund
	if err := f.db.First(&persisted, f.refund.ID).Error; err != nil ||
		persisted.Status != entity.TicketRefundStatusProcessing || persisted.ResolvedAt != nil || persisted.OutcomeReference != "" {
		t.Fatalf("submit acknowledgement must not settle the refund: status=%q err=%v", persisted.Status, err)
	}
	var ledger int64
	if err := f.db.Model(&entity.TicketRefundLedger{}).Count(&ledger).Error; err != nil || ledger != 0 {
		t.Fatalf("submit changed the refund ledger: count=%d err=%v", ledger, err)
	}
}

func TestTicketRefundEPaySubmitDoesNotFollowRedirectOrRetryTimeout(t *testing.T) {
	f := newRefundEPayFixture(t)
	for _, mode := range []string{"redirect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			installRefundEPayTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				f.assertRequest(t, r, "/api/pay/refund", map[string]string{
					"trade_no": f.order.RefundOriginalTradeNo, "out_refund_no": f.refund.RefundNo, "money": "15.01",
				})
				if mode == "timeout" {
					return nil, context.DeadlineExceeded
				}
				resp := refundEPayResponse(http.StatusTemporaryRedirect, nil)
				resp.Header.Set("Location", "https://different.example.invalid/refund")
				return resp, nil
			})
			err := SubmitTicketRefundEPay(context.Background(), f.cfg, f.refund, f.order)
			if !errors.Is(err, ErrTicketRefundChannelUnknown) || calls != 1 {
				t.Fatalf("%s must remain unknown after one POST: calls=%d err=%v", mode, calls, err)
			}
		})
	}
}

func TestTicketRefundEPayQueryRequiresSignedExactOriginalAndRefundProof(t *testing.T) {
	f := newRefundEPayFixture(t)
	f.refund.Status = entity.TicketRefundStatusUnknown
	_, forgedKeyText, _ := generateRechargeRSAKeys(t)
	forgedKey, err := parseRechargePrivateKey(forgedKeyText)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{name: "success", want: entity.TicketRefundStatusSucceeded},
		{name: "failed", key: "status", value: "0", want: entity.TicketRefundStatusFailed},
		{name: "pending", key: "status", value: "2"},
		{name: "wrong-trade", key: "trade_no", value: "another-trade"},
		{name: "wrong-order", key: "out_trade_no", value: "another-order"},
		{name: "wrong-amount", key: "money", value: "15.02"},
		{name: "sub-cent-amount", key: "money", value: "15.010"},
		{name: "wrong-request-ref", key: "out_refund_no", value: "different-refund"},
		{name: "invalid-provider-ref", key: "refund_no", value: "untrusted/refund"},
		{name: "wrong-merchant", key: "pid", value: "other-merchant"},
		{name: "stale-timestamp", key: "timestamp", value: strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)},
		{name: "future-timestamp", key: "timestamp", value: strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)},
		{name: "not-found", key: "code", value: "-1"},
		{name: "forged-signature"},
		{name: "tampered-after-signing"},
		{name: "missing-signature"},
		{name: "wrong-sign-type"},
		{name: "duplicate-money-key"},
		{name: "trailing-json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			installRefundEPayTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				f.assertRequest(t, r, "/api/pay/refundquery", map[string]string{"out_refund_no": f.refund.RefundNo})
				reply := f.replyFields()
				if tc.key != "" {
					reply.Set(tc.key, tc.value)
				}
				signer := f.platformPrivate
				if tc.name == "forged-signature" {
					signer = forgedKey
				}
				signSyntheticRefundEPay(t, reply, signer)
				switch tc.name {
				case "tampered-after-signing":
					reply.Set("status", "0")
				case "missing-signature":
					reply.Del("sign")
				case "wrong-sign-type":
					reply.Set("sign_type", "MD5")
				}
				body := refundEPayJSON(t, reply)
				switch tc.name {
				case "duplicate-money-key":
					body = append([]byte(`{"money":15.01,`), body[1:]...)
				case "trailing-json":
					body = append(body, []byte(`{}`)...)
				}
				return refundEPayResponse(http.StatusOK, body), nil
			})
			status, ref, err := QueryTicketRefundEPay(context.Background(), f.cfg, f.refund, f.order)
			if calls != 1 {
				t.Fatalf("query executed %d requests", calls)
			}
			if tc.want == "" {
				if !errors.Is(err, ErrTicketRefundChannelUnknown) || status != "" || ref != "" {
					t.Fatalf("ambiguous reply became terminal: status=%q ref=%q err=%v", status, ref, err)
				}
			} else if err != nil || status != tc.want || ref != "synthetic-channel-refund-31" {
				t.Fatalf("verified proof did not settle as expected: status=%q ref=%q err=%v", status, ref, err)
			}
		})
	}
}

func TestTicketRefundEPayFingerprintFailClosedAndRejectBeforeTransport(t *testing.T) {
	f := newRefundEPayFixture(t)
	if f.cfg.fingerprint == "" || f.cfg.fingerprint != refundChannelFingerprint("https://PAY.example.com:443/", f.cfg.partnerID, f.cfg.platformKey) {
		t.Fatal("canonical verified fingerprint missing or unstable")
	}
	if fp, err := f.cfg.VerifyOriginalOrder(nil, f.order); err != nil || fp != f.cfg.fingerprint {
		t.Fatalf("verified order did not match configured channel: %v", err)
	}
	for _, address := range []string{"http://pay.example.com", "https://127.0.0.1", "https://pay.example.com:8443",
		"https://pay.example.com?unsafe=yes", "https://user:password@pay.example.com", "https://pay.example.com#fragment"} {
		if refundChannelFingerprint(address, f.cfg.partnerID, f.cfg.platformKey) != "" {
			t.Fatalf("unsafe synthetic gateway admitted: %q", address)
		}
	}
	installRefundEPayTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unverified or mismatched input must not reach the transport")
		return nil, errors.New("transport forbidden")
	})
	for _, change := range []struct {
		name string
		edit func(*entity.RechargeOrder)
	}{
		{"legacy-unverified", func(o *entity.RechargeOrder) { o.RefundChannelFingerprint = "" }},
		{"wrong-merchant", func(o *entity.RechargeOrder) {
			o.RefundChannelFingerprint = refundChannelFingerprint(f.cfg.gatewayURL, "different-pid", f.cfg.platformKey)
		}},
		{"only-mutable-callback-reference", func(o *entity.RechargeOrder) { o.RefundOriginalTradeNo = "" }},
		{"invalid-trade-reference", func(o *entity.RechargeOrder) { o.RefundOriginalTradeNo = "untrusted/trade" }},
		{"foreign-order", func(o *entity.RechargeOrder) { o.UserID++ }},
		{"wrong-order-id", func(o *entity.RechargeOrder) { o.ID++ }},
		{"other-provider", func(o *entity.RechargeOrder) { o.PaymentProvider = "other" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			order := f.order
			change.edit(&order)
			if err := SubmitTicketRefundEPay(context.Background(), f.cfg, f.refund, order); !errors.Is(err, ErrTicketRefundChannelDisabled) {
				t.Fatalf("mismatched submit error: %v", err)
			}
			if status, ref, err := QueryTicketRefundEPay(context.Background(), f.cfg, f.refund, order); !errors.Is(err, ErrTicketRefundChannelDisabled) || status != "" || ref != "" {
				t.Fatalf("mismatched query error: status=%q ref=%q err=%v", status, ref, err)
			}
		})
	}
}

func TestTicketRefundEPayVerifiedPaidSnapshotSurvivesSignedCallbackReplay(t *testing.T) {
	f := newRefundEPayFixture(t)
	user := seedRechargeOrderUser(t, f.db, f.order.UserID, "user")
	method := seedRechargePaymentMethod(t, f.db, entity.PaymentMethod{Name: "synthetic", EPayType: "alipay", Enabled: true})
	_, pending, _, err := prepareRechargeOrderWithDB(f.db, user, CreateRechargeOrderInput{PaymentMethodID: method.ID, Amount: 50.25})
	if err != nil {
		t.Fatalf("prepare order without any transport: %v", err)
	}
	if pending.RefundChannelFingerprint != f.cfg.fingerprint || pending.RefundOriginalTradeNo != "" {
		t.Fatal("order creation must snapshot channel identity but not an unverified trade")
	}
	if err := f.db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	callback := url.Values{
		"pid": {f.cfg.partnerID}, "type": {"alipay"}, "out_trade_no": {pending.OrderNo},
		"trade_no": {f.order.RefundOriginalTradeNo}, "trade_status": {rechargeOrderTradeStatusSuccess}, "money": {"50.25"},
	}
	signSyntheticRefundEPay(t, callback, f.platformPrivate)
	if ack, err := HandleRechargePaymentNotifyWithDB(f.db, callback); err != nil || !ack {
		t.Fatalf("verified callback rejected: ack=%v err=%v", ack, err)
	}
	var before entity.RechargeOrder
	if err := f.db.First(&before, pending.ID).Error; err != nil || before.PaidAt == nil ||
		before.Status != rechargeOrderStatusPaid || before.RefundOriginalTradeNo != f.order.RefundOriginalTradeNo {
		t.Fatalf("paid snapshot missing: %v", err)
	}
	if _, err := f.cfg.VerifyOriginalOrder(nil, before); err != nil {
		t.Fatalf("settled snapshot could not be proven: %v", err)
	}
	callback.Set("trade_no", "synthetic-signed-replay-different-trade")
	signSyntheticRefundEPay(t, callback, f.platformPrivate)
	if ack, err := HandleRechargePaymentNotifyWithDB(f.db, callback); err != nil || !ack {
		t.Fatalf("signed replay was not acknowledged: ack=%v err=%v", ack, err)
	}
	var after entity.RechargeOrder
	if err := f.db.First(&after, pending.ID).Error; err != nil || after.PaidAt == nil || !after.PaidAt.Equal(*before.PaidAt) ||
		after.RefundOriginalTradeNo != before.RefundOriginalTradeNo || after.ThirdPartyOrderNo != before.ThirdPartyOrderNo ||
		after.CallbackPayload != before.CallbackPayload {
		t.Fatalf("signed replay changed immutable paid order: %v", err)
	}
	var billing int64
	if err := f.db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", pending.ID).Count(&billing).Error; err != nil || billing != 1 {
		t.Fatalf("signed replay must not credit again: count=%d err=%v", billing, err)
	}
}
