package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	rechargeNotifyChildEnv    = "TIKU_TEST_RECHARGE_NOTIFY_CHILD"
	rechargeNotifyWorkdirEnv  = "TIKU_TEST_RECHARGE_NOTIFY_WORKDIR"
	rechargeNotifyKeysFileEnv = "TIKU_TEST_RECHARGE_NOTIFY_KEYS_FILE"
	rechargeNotifyKeysSHAEnv  = "TIKU_TEST_RECHARGE_NOTIFY_KEYS_SHA256"
	rechargeNotifyComplete    = "RECHARGE_CONCURRENT_NOTIFY_COMPLETE"
)

// Run the concurrent database work in an owned child. A driver that does not
// drain on context cancellation must not leave goroutines using a removed DB.
func TestHandleRechargePaymentNotifyWithDBConcurrentDuplicate(t *testing.T) {
	if os.Getenv(rechargeNotifyChildEnv) != "" {
		t.Fatal("parent must not run in child mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe,
		"-test.run=^TestHandleRechargePaymentNotifyWithDBConcurrentDuplicateChild$",
		"-test.v", "-test.timeout=16s")
	cmd.Env = append(os.Environ(), rechargeNotifyChildEnv+"=1",
		rechargeNotifyWorkdirEnv+"="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	t.Logf("child stdout:\n%s", stdout.Bytes())
	if stderr.Len() != 0 {
		t.Logf("child stderr:\n%s", stderr.Bytes())
	}
	if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("concurrent notify child failed: run=%v context=%v", err, ctx.Err())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(rechargeNotifyComplete)) {
		t.Fatal("child did not reach the complete state assertion")
	}
}

// This entry point is skipped in an ordinary suite. Only the parent supplies
// its private flag and temporary directory; no real settings or database is used.
func TestHandleRechargePaymentNotifyWithDBConcurrentDuplicateChild(t *testing.T) {
	if os.Getenv(rechargeNotifyChildEnv) != "1" {
		t.Skip("owned concurrent-notify child; exercised by parent")
	}
	workdir := os.Getenv(rechargeNotifyWorkdirEnv)
	if !filepath.IsAbs(workdir) {
		t.Fatal("child requires parent-owned temporary directory")
	}
	watch := time.AfterFunc(12*time.Second, func() { os.Exit(24) })
	defer watch.Stop()

	path := filepath.Join(workdir, "notify.db")
	a, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.PaymentMethod{},
		&entity.RechargeOrder{}, &entity.BillingRecord{}, &entity.PaymentEvent{}); err != nil {
		t.Fatal(err)
	}
	b, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlA, err := a.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlB, err := b.DB()
	if err != nil {
		t.Fatal(err)
	}
	if sqlA == sqlB {
		t.Fatal("independent database handles required")
	}
	defer sqlA.Close()
	defer sqlB.Close()

	user := seedRechargeOrderUser(t, a, 7, "user")
	if err = a.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 5.25).Error; err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, recheckKeys := rechargeNotifyTestKeys(t)
	seedRechargePaymentSettings(t, a, publicKey, privateKey)
	order := entity.RechargeOrder{UserID: user.ID, OrderNo: "rcg_notify_success",
		PaymentProvider: DefaultPaymentProvider, PaymentMethodNameSnapshot: "支付宝",
		Amount: 12.35, Status: rechargeOrderStatusPending}
	if err = a.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	values := url.Values{"pid": {"10001"}, "type": {"alipay"}, "out_trade_no": {order.OrderNo},
		"trade_no": {"trade_20260506001"}, "trade_status": {"TRADE_SUCCESS"}, "money": {"12.35"}}
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
	payload := values.Encode()
	t.Logf("signed input sha256=%s", rechargeNotifyHash(payload))

	type pendingRead struct {
		arm         string
		transaction bool
		orderID     int32
	}
	ready, release := make(chan pendingRead, 2), make(chan struct{})
	dbs, names := []*gorm.DB{a, b}, []string{"A", "B"}
	for i, db := range dbs {
		var once atomic.Bool
		arm := names[i]
		err := db.Callback().Query().After("gorm:query").Register("notify_pending_"+arm, func(tx *gorm.DB) {
			got, ok := tx.Statement.Dest.(*entity.RechargeOrder)
			if ok && got.OrderNo == order.OrderNo && got.Status == rechargeOrderStatusPending && once.CompareAndSwap(false, true) {
				_, isTransaction := tx.Statement.ConnPool.(*sql.Tx)
				ready <- pendingRead{arm, isTransaction, got.ID}
				<-release
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	type outcome struct {
		arm string
		ack bool
		err error
	}
	done := make(chan outcome, 2)
	joined := false
	defer func() {
		if !joined {
			fmt.Fprintln(os.Stderr, "concurrent callbacks not joined; stopping child before database cleanup")
			os.Exit(25)
		}
	}()
	for i, db := range dbs {
		arm, input := names[i], rechargeNotifyClone(values)
		go func(db *gorm.DB, arm string, input url.Values) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ack, err := HandleRechargePaymentNotifyWithDB(db.WithContext(ctx), input)
			done <- outcome{arm, ack, err}
		}(db, arm, input)
	}
	seen := make(map[string]pendingRead, 2)
	barrierDeadline := time.NewTimer(2 * time.Second)
	defer barrierDeadline.Stop()
	for len(seen) < 2 {
		select {
		case got := <-ready:
			if _, duplicate := seen[got.arm]; duplicate || !got.transaction || got.orderID != order.ID || (got.arm != "A" && got.arm != "B") {
				t.Fatalf("invalid independent pending transaction: %+v", got)
			}
			seen[got.arm] = got
		case <-barrierDeadline.C:
			t.Fatalf("both pending transactions not reached: %+v", seen)
		}
	}
	recheckKeys()
	t.Logf("independent pending transactions: A=%d B=%d", seen["A"].orderID, seen["B"].orderID)
	close(release)
	results := make([]outcome, 0, 2)
	joinDeadline := time.NewTimer(6 * time.Second)
	defer joinDeadline.Stop()
	for len(results) < 2 {
		select {
		case got := <-done:
			results = append(results, got)
		case <-joinDeadline.C:
			t.Fatal("concurrent callbacks did not join")
		}
	}
	joined = true
	for _, got := range results {
		var code interface{ Code() int }
		transient := errors.As(got.err, &code) && (code.Code()&255 == 5 || code.Code()&255 == 6)
		sqliteCode := 0
		if code != nil {
			sqliteCode = code.Code()
		}
		t.Logf("initial arm=%s ack=%t error_type=%T sqlite_code=%d transient=%t error=%v",
			got.arm, got.ack, got.err, sqliteCode, transient, got.err)
		if (got.err == nil && !got.ack) || (got.err != nil && (!transient || got.ack)) {
			t.Fatalf("unexpected initial outcome: %+v", got)
		}
	}
	beforeRetry := rechargeNotifyAssertState(t, a, user.ID, order.ID, payload, false)
	for i, db := range dbs {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ack, err := HandleRechargePaymentNotifyWithDB(db.WithContext(ctx), rechargeNotifyClone(values))
		cancel()
		if err != nil || !ack {
			t.Fatalf("bounded post-join duplicate %s: ack=%t error=%v", names[i], ack, err)
		}
		t.Logf("post-join duplicate %s acknowledged", names[i])
	}
	final := rechargeNotifyAssertState(t, a, user.ID, order.ID, payload, true)
	if beforeRetry.Status == rechargeOrderStatusPaid && !reflect.DeepEqual(beforeRetry, final) {
		t.Fatal("duplicate changed previously settled order/wallet/ledger")
	}
	recheckKeys()
	t.Log(rechargeNotifyComplete)
}

func rechargeNotifyClone(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, values := range values {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func rechargeNotifyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type rechargeNotifyLedgerState struct {
	ID, UserID, OrderID                    int32
	RequestID, Provider, Type, SnapshotSHA string
	Amount, Before, After                  float64
}
type rechargeNotifyState struct {
	Status, TradeNo, PayloadSHA string
	HasPaidAt                   bool
	Balance, Total              float64
	Ledger                      []rechargeNotifyLedgerState
}

func rechargeNotifyAssertState(t *testing.T, db *gorm.DB, userID, orderID int32, payload string, final bool) rechargeNotifyState {
	t.Helper()
	var order entity.RechargeOrder
	var user entity.User
	var rows []entity.BillingRecord
	if err := db.First(&order, orderID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got := rechargeNotifyState{Status: order.Status, TradeNo: order.ThirdPartyOrderNo, PayloadSHA: rechargeNotifyHash(order.CallbackPayload),
		HasPaidAt: order.PaidAt != nil, Balance: user.Balance, Ledger: []rechargeNotifyLedgerState{}}
	for _, row := range rows {
		got.Total += row.Amount
		got.Ledger = append(got.Ledger, rechargeNotifyLedgerState{row.ID, row.UserID, row.RelatedOrderID, row.RequestID,
			row.Provider, row.Type, rechargeNotifyHash(row.UnitPriceSnapshot), row.Amount, row.BalanceBefore, row.BalanceAfter})
	}
	stateJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("observed final=%t state=%s", final, stateJSON)
	if order.UserID != userID || order.Amount != 12.35 || order.PaymentProvider != DefaultPaymentProvider {
		t.Fatal("order identity or amount changed")
	}
	for _, row := range rows {
		if row.UserID != userID || row.RelatedOrderID != orderID || row.RequestID != "rcg_notify_success" ||
			row.Provider != DefaultPaymentProvider || row.Type != rechargeBillingRecordType || row.UnitPriceSnapshot != payload ||
			row.Amount != 12.35 || row.BalanceBefore != 5.25 || row.BalanceAfter != 17.6 {
			t.Fatal("complete ledger row mismatch")
		}
	}
	if order.Status == rechargeOrderStatusPaid {
		if order.CallbackPayload != payload || order.ThirdPartyOrderNo != "trade_20260506001" || order.PaidAt == nil ||
			user.Balance != 17.6 || len(rows) != 1 || got.Total != 12.35 {
			t.Fatal("settled order/wallet/ledger mismatch")
		}
	} else if final || order.Status != rechargeOrderStatusPending || order.CallbackPayload != "" ||
		order.ThirdPartyOrderNo != "" || order.PaidAt != nil || user.Balance != 5.25 || len(rows) != 0 {
		t.Fatal("unexpected pending state or duplicate credit")
	}
	return got
}

// The default suite generates synthetic keys in memory using the existing test
// helper. Offline B/M/R can opt into one explicitly pinned synthetic JSON input;
// both variables are required, and its bytes must remain unchanged throughout.
func rechargeNotifyTestKeys(t *testing.T) (string, string, func()) {
	t.Helper()
	path, expected := os.Getenv(rechargeNotifyKeysFileEnv), os.Getenv(rechargeNotifyKeysSHAEnv)
	if path == "" && expected == "" {
		publicKey, privateKey, _ := generateRechargeRSAKeys(t)
		return publicKey, privateKey, func() {}
	}
	digest, err := hex.DecodeString(expected)
	if !filepath.IsAbs(path) || err != nil || len(digest) != sha256.Size {
		t.Fatal("synthetic key file and exact SHA256 must be supplied together")
	}
	read := func() []byte {
		for current := path; ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("unsafe synthetic key path")
			}
			if filepath.Dir(current) == current {
				break
			}
		}
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 16384 {
			t.Fatal("invalid synthetic key file")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal("cannot open synthetic key file")
		}
		opened, statErr := file.Stat()
		data, readErr := io.ReadAll(io.LimitReader(file, 16385))
		closeErr := file.Close()
		after, afterErr := os.Lstat(path)
		if statErr != nil || readErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(before, opened) ||
			!os.SameFile(opened, after) || int64(len(data)) != before.Size() || rechargeNotifyHash(string(data)) != expected {
			t.Fatal("synthetic key input changed or unreadable")
		}
		return data
	}
	frozen := read()
	var keys struct{ Public, Private string }
	if json.Unmarshal(frozen, &keys) != nil || keys.Public == "" || keys.Private == "" {
		t.Fatal("invalid synthetic key JSON")
	}
	return keys.Public, keys.Private, func() {
		t.Helper()
		if !bytes.Equal(frozen, read()) {
			t.Fatal("synthetic key input changed")
		}
	}
}
