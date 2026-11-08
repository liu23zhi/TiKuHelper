package service

import (
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFuturePolicyNoKeyLocationExternalCallsZero(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("phone") != "" {
			_, _ = w.Write([]byte(`{"province":"广东","city":"深圳","sp":"移动"}`))
		} else {
			_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"fake-only"}`))
		}
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "")
	db := newLocationTestDB(t)
	for _, key := range []string{"", "   "} {
		for _, source := range []string{"", "commercial"} {
			before := calls.Load()
			_, err := LookupIPLocationsDetailedWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: key, Source: source})
			if !errors.Is(err, ErrLocationNotConfigured) || calls.Load() != before {
				t.Errorf("IP source=%q key=%q err=%v requests=%d want not-configured/0", source, key, err, calls.Load()-before)
			}
			for _, kind := range []string{"ip", "phone"} {
				if kind == "phone" && source != "" {
					continue
				}
				lookup := "8.8.8.8"
				if kind == "phone" {
					lookup = "13800138000"
				}
				opts := LocationQueryOptions{UAPIKey: key, Source: source}
				_, err = RefreshLocationCacheWithDB(db, kind, lookup, opts)
				if !errors.Is(err, ErrLocationNotConfigured) || calls.Load() != before {
					t.Errorf("single kind=%s source=%q err=%v requests=%d", kind, source, err, calls.Load()-before)
				}
				_, err = ManualLookupLocationCacheWithDB(db, kind, lookup, opts)
				if !errors.Is(err, ErrLocationNotConfigured) || calls.Load() != before {
					t.Errorf("manual kind=%s source=%q err=%v requests=%d", kind, source, err, calls.Load()-before)
				}
				_, err = RefreshLocationCacheBatchWithDB(db, LocationCacheBatchRefreshRequest{Kind: kind, QuerySource: source, Items: []LocationCacheDeleteItem{{LookupKey: lookup}}}, opts)
				if !errors.Is(err, ErrLocationNotConfigured) || calls.Load() != before {
					t.Errorf("batch kind=%s source=%q err=%v requests=%d", kind, source, err, calls.Load()-before)
				}
			}
		}
		before := calls.Load()
		_, err := LookupPhoneLocationsDetailedWithDB(db, []string{"13900139000"}, LocationQueryOptions{UAPIKey: key})
		if !errors.Is(err, ErrLocationNotConfigured) || calls.Load() != before {
			t.Errorf("phone lookup key=%q err=%v requests=%d", key, err, calls.Load()-before)
		}
	}
	var count int64
	db.Model(&entity.LocationCache{}).Count(&count)
	t.Logf("no-key requests=%d new-cache-rows=%d", calls.Load(), count)
	if calls.Load() != 0 || count != 0 {
		t.Errorf("no-key total must be zero")
	}
}

func TestFuturePolicyNoKeyCacheAndSyntheticStillAvailable(t *testing.T) {
	db := newLocationTestDB(t)
	rows := []entity.LocationCache{{Kind: "phone", LookupKey: "13800138000", Source: "", Address: "saved-phone"}, {Kind: "ip", LookupKey: "8.8.8.8", Source: "", Address: "saved-ip"}, {Kind: "ip", LookupKey: "1.1.1.1", Source: "commercial", Address: "saved-commercial"}}
	if err := storeLocationCacheRows(db, rows); err != nil {
		t.Fatal(err)
	}
	phones, err := LookupPhoneLocationsDetailedWithDB(db, []string{"13800138000"}, LocationQueryOptions{})
	if err != nil || phones["13800138000"].Address != "saved-phone" {
		t.Fatalf("cache phone=%v err=%v", phones, err)
	}
	for _, x := range []struct{ key, source, address string }{{"8.8.8.8", "", "saved-ip"}, {"1.1.1.1", "commercial", "saved-commercial"}} {
		ips, err := LookupIPLocationsDetailedWithDB(db, []string{x.key}, LocationQueryOptions{Source: x.source})
		if err != nil || ips[x.key].Address != x.address {
			t.Fatalf("cache ip=%v err=%v", ips, err)
		}
	}
	ips, err := LookupIPLocationsDetailedWithDB(db, []string{"127.0.0.1", "192.168.0.1"}, LocationQueryOptions{})
	if err != nil || len(ips) != 2 {
		t.Fatalf("synthetic=%v err=%v", ips, err)
	}
	t.Logf("cached-phone=1 cached-ip=2 synthetic=2")
}

func TestFuturePolicyRechargeAuditFailureAckAfterFinancialCommit(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	user, order, values := seedRechargeAuditNotify(t, db)
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 0.123456).Error; err != nil {
		t.Fatal(err)
	}
	oldLogDir := *logger.LogDir
	*logger.LogDir = t.TempDir()
	if err := logger.SetupGinLog(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close(); *logger.LogDir = oldLogDir })
	attempts := 0
	committedAtAudit := false
	financialVisible := "unobserved"
	financePool := ""
	if err := db.Callback().Update().Before("gorm:update").Register("future:finance_pool", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameUser {
			financePool = fmt.Sprintf("%p", tx.Statement.ConnPool)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove("future:finance_pool") })
	name := "future:audit_fail"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		entry, ok := tx.Statement.Dest.(*entity.Log)
		if !ok || entry.Module != "recharge" || entry.Action != 41 {
			return
		}
		attempts++
		// Observe transaction separation without a nested SQLite read that can deadlock.
		committedAtAudit = financePool != "" && fmt.Sprintf("%p", tx.Statement.ConnPool) != financePool
		financialVisible = fmt.Sprintf("separate-transaction=%v", committedAtAudit)
		tx.AddError(errors.New("synthetic audit unavailable"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	ack, notifyErr := HandleRechargePaymentNotifyWithDB(db, values)
	var got entity.User
	var paid entity.RechargeOrder
	var bills, logs int64
	db.First(&got, user.ID)
	db.First(&paid, order.ID)
	db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&bills)
	db.Model(&entity.Log{}).Where("module = ? AND action = ?", "recharge", 41).Count(&logs)
	t.Logf("audit-fail ack=%v err=%v balance=%.6f status=%s bills=%d audits=%d attempts=%d financial-visible-at-audit=%s", ack, notifyErr, got.Balance, paid.Status, bills, logs, attempts, financialVisible)
	if notifyErr != nil || !ack || got.Balance != 9.373456 || paid.Status != rechargeOrderStatusPaid || bills != 1 || logs != 0 || attempts != 1 || !committedAtAudit {
		t.Errorf("audit must not block committed finance")
	}
	if err := db.Callback().Create().Remove(name); err != nil {
		t.Fatal(err)
	}
	retryAck, retryErr := HandleRechargePaymentNotifyWithDB(db, values)
	db.First(&got, user.ID)
	db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&bills)
	db.Model(&entity.Log{}).Where("module = ? AND action = ?", "recharge", 41).Count(&logs)
	t.Logf("repeat ack=%v err=%v balance=%.6f bills=%d audits=%d attempts=%d", retryAck, retryErr, got.Balance, bills, logs, attempts)
	if retryErr != nil || !retryAck || got.Balance != 9.373456 || bills != 1 || logs != 0 || attempts != 1 {
		t.Errorf("repeat callback must not credit or retry missing audit")
	}
	_ = logger.Close()
	warning, _ := os.ReadFile(filepath.Join(*logger.LogDir, "Tiku-warning.txt"))
	errorLog, _ := os.ReadFile(filepath.Join(*logger.LogDir, "Tiku-error.txt"))
	t.Logf("audit-warning-observed=%v error-entry=%v", strings.Contains(string(warning), "充值到账审计写入失败"), strings.Contains(string(errorLog), "充值到账审计写入失败"))
	if !strings.Contains(string(warning), "充值到账审计写入失败") || strings.Contains(string(errorLog), "充值到账审计写入失败") {
		t.Error("audit failure must be warning")
	}
}

func TestFuturePolicyRechargeFinancialFailureStillRollsBack(t *testing.T) {
	for _, site := range []string{"order", "wallet", "billing"} {
		t.Run(site, func(t *testing.T) {
			db := newRechargeOrderTestDB(t)
			user, order, values := seedRechargeAuditNotify(t, db)
			var attempts int
			name := "future:finance_fail"
			inject := func(tx *gorm.DB) {
				if site == "order" && tx.Statement.Table == entity.TableNameRechargeOrder {
					tx.AddError(errors.New("synthetic order failure"))
				}
				if site == "wallet" && tx.Statement.Table == entity.TableNameUser {
					tx.AddError(errors.New("synthetic wallet failure"))
				}
				if site == "billing" && tx.Statement.Table == entity.TableNameBillingRecord {
					tx.AddError(errors.New("synthetic billing failure"))
				}
			}
			if site == "billing" {
				db.Callback().Create().Before("gorm:create").Register(name, inject)
				t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
			} else {
				db.Callback().Update().Before("gorm:update").Register(name, inject)
				t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
			}
			db.Callback().Create().Before("gorm:create").Register("future:count_audit", func(tx *gorm.DB) {
				if v, ok := tx.Statement.Dest.(*entity.Log); ok && v.Module == "recharge" && v.Action == 41 {
					attempts++
				}
			})
			t.Cleanup(func() { _ = db.Callback().Create().Remove("future:count_audit") })
			ack, err := HandleRechargePaymentNotifyWithDB(db, values)
			var got entity.User
			var pending entity.RechargeOrder
			var bills int64
			db.First(&got, user.ID)
			db.First(&pending, order.ID)
			db.Model(&entity.BillingRecord{}).Count(&bills)
			t.Logf("financial-%s ack=%v err=%v balance=%.6f status=%s bills=%d audit-attempts=%d", site, ack, err, got.Balance, pending.Status, bills, attempts)
			if err == nil || ack || got.Balance != 0 || pending.Status != rechargeOrderStatusPending || bills != 0 || attempts != 0 {
				t.Fatal("financial writes must stay atomic")
			}
		})
	}
}
