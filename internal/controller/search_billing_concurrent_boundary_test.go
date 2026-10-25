package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	billingBoundaryChildEnv    = "TIKU_TEST_BILLING_BOUNDARY_CHILD"
	billingBoundaryScenarioEnv = "TIKU_TEST_BILLING_BOUNDARY_SCENARIO"
	billingBoundaryWorkdirEnv  = "TIKU_TEST_BILLING_BOUNDARY_WORKDIR"
	billingBoundaryComplete    = "SEARCH_BILLING_BOUNDARY_COMPLETE "
	billingBoundarySnapshot    = "provider=icodef unitPrice=0.030003 question=D03%20quota%20boundary"
)

// Each profile owns a child and temporary file. A database driver that does not
// drain on cancellation must not keep SQL alive during temporary-file cleanup.
func TestPersistSearchBillingRecordsWithDBConcurrentBoundaries(t *testing.T) {
	if os.Getenv(billingBoundaryChildEnv) != "" {
		t.Fatal("parent must not run in child mode")
	}
	for _, scenario := range []string{"last_unit_quota", "unlimited_two_debits", "allowed_negative_balance"} {
		t.Run(scenario, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable,
				"-test.run=^TestPersistSearchBillingRecordsWithDBConcurrentBoundariesChild$",
				"-test.v", "-test.timeout=16s")
			cmd.Env = append(os.Environ(), billingBoundaryChildEnv+"=1", billingBoundaryScenarioEnv+"="+scenario,
				billingBoundaryWorkdirEnv+"="+t.TempDir())
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			t.Logf("child stdout:\n%s", stdout.Bytes())
			if stderr.Len() != 0 {
				t.Logf("child stderr:\n%s", stderr.Bytes())
			}
			if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
				t.Fatalf("billing boundary child failed: run=%v context=%v", err, ctx.Err())
			}
			if !bytes.Contains(stdout.Bytes(), []byte(billingBoundaryComplete+scenario)) {
				t.Fatal("child did not reach this profile's final complete-state assertion")
			}
		})
	}
}

type billingBoundaryProfile struct {
	name                                            string
	limit, initialBalance, finalUsage, finalBalance float64
	finalRows                                       int
}

func billingBoundaryProfileFor(name string) (billingBoundaryProfile, bool) {
	switch name {
	case "last_unit_quota":
		return billingBoundaryProfile{name, 0.050004, 5.25, 0.050004, 5.219997, 1}, true
	case "unlimited_two_debits":
		return billingBoundaryProfile{name, 0, 5.25, 0.080007, 5.189994, 2}, true
	case "allowed_negative_balance":
		return billingBoundaryProfile{name, 0.1, 0.01, 0.080007, -0.050006, 2}, true
	default:
		return billingBoundaryProfile{}, false
	}
}

// This entry point is skipped in an ordinary suite and exercised by its parent.
func TestPersistSearchBillingRecordsWithDBConcurrentBoundariesChild(t *testing.T) {
	if os.Getenv(billingBoundaryChildEnv) != "1" {
		t.Skip("owned billing-boundary child; exercised by parent")
	}
	profile, ok := billingBoundaryProfileFor(os.Getenv(billingBoundaryScenarioEnv))
	if !ok {
		t.Fatal("unknown billing boundary profile")
	}
	workdir := os.Getenv(billingBoundaryWorkdirEnv)
	if !filepath.IsAbs(workdir) {
		t.Fatal("child requires parent-owned absolute temporary directory")
	}
	for current := workdir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatal("unsafe parent-owned temporary directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	path := filepath.Join(workdir, "billing-boundary.db")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("synthetic database must not already exist")
	}
	watchdog := time.AfterFunc(12*time.Second, func() { fmt.Fprintln(os.Stderr, "billing boundary child watchdog"); os.Exit(24) })
	defer watchdog.Stop()
	a, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
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
		t.Fatal("independent SQL handles required")
	}
	defer sqlA.Close()
	defer sqlB.Close()
	if searchModeNormal != "normal" || searchBillingRecordTypeNormal != "normal_search" {
		t.Fatal("bound billing constants changed")
	}
	initial := seedBillingBoundary(t, a, profile)
	logBillingBoundaryState(t, "initial", initial)
	ready, release := make(chan billingBoundaryRead, 2), make(chan struct{})
	dbs, names := []*gorm.DB{a, b}, []string{"A", "B"}
	for i, db := range dbs {
		var first atomic.Bool
		arm := names[i]
		if err := db.Callback().Query().After("gorm:query").Register("billing_boundary_token_"+arm, func(tx *gorm.DB) {
			token, ok := tx.Statement.Dest.(*entity.AccessToken)
			if tx.Error == nil && ok && token.ID == 1 && first.CompareAndSwap(false, true) {
				_, isTx := tx.Statement.ConnPool.(*sql.Tx)
				ready <- billingBoundaryRead{arm, billingBoundaryRequestID(arm), isTx, token.ID, token.UserID, token.TotalAmountUsed, token.TotalQuotaLimit}
				<-release
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	done, finished := make(chan billingBoundaryResult, 2), make(chan string, 2)
	joined := false
	defer func() {
		if !joined {
			fmt.Fprintln(os.Stderr, "billing boundary SQL not joined; stopping child before database cleanup")
			os.Exit(25)
		}
	}()
	for i, db := range dbs {
		arm := names[i]
		go func(db *gorm.DB, arm string) { done <- callBillingBoundary(db, arm, 8*time.Second); finished <- arm }(db, arm)
	}
	reads := map[string]billingBoundaryRead{}
	barrier := time.NewTimer(2 * time.Second)
	defer barrier.Stop()
	for len(reads) < 2 {
		select {
		case got := <-ready:
			if _, duplicate := reads[got.Arm]; duplicate || (got.Arm != "A" && got.Arm != "B") || got.RequestID != billingBoundaryRequestID(got.Arm) || !got.SQLTx || got.TokenID != 1 || got.UserID != 2 || got.Used != 0.020001 || got.Limit != profile.limit {
				t.Fatalf("invalid independent transaction read: %+v", got)
			}
			reads[got.Arm] = got
		case <-barrier.C:
			t.Fatalf("both transactions did not reach the token barrier: %+v", reads)
		}
	}
	logBillingBoundaryJSON(t, "both independent token transactions", reads)
	close(release)
	results := []billingBoundaryResult{}
	ended := map[string]bool{}
	join := time.NewTimer(6 * time.Second)
	defer join.Stop()
	for len(results) < 2 {
		select {
		case got := <-done:
			results = append(results, got)
		case <-join.C:
			t.Fatal("initial billing calls did not return")
		}
	}
	for len(ended) < 2 {
		select {
		case arm := <-finished:
			if ended[arm] || (arm != "A" && arm != "B") {
				t.Fatal("invalid finished billing arm")
			}
			ended[arm] = true
		case <-join.C:
			t.Fatal("initial billing goroutines did not join")
		}
	}
	joined = true
	logBillingBoundaryJSON(t, "initial billing results", results)
	state := readBillingBoundary(t, b)
	logBillingBoundaryState(t, "after concurrent calls", state)
	successful := map[string]bool{}
	successfulCalls := map[string]int{}
	seen := map[string]bool{}
	for _, result := range results {
		if seen[result.Arm] || (result.Arm != "A" && result.Arm != "B") || result.RequestID != billingBoundaryRequestID(result.Arm) {
			t.Fatal("invalid initial request attribution")
		}
		seen[result.Arm] = true
		if result.Classification == "other" {
			t.Fatalf("unexpected initial billing error: %+v", result)
		}
		if result.Success {
			successful[result.RequestID] = true
			successfulCalls[result.RequestID]++
		}
	}
	assertBillingBoundaryState(t, state, initial, profile)
	assertBillingBoundaryAttribution(t, state, successful, successfulCalls)
	for _, result := range results {
		if result.QuotaExceeded && (profile.name != "last_unit_quota" || state.Rows != 1) {
			t.Fatal("initial quota rejection without exhausting this profile's budget")
		}
	}
	for i, db := range dbs {
		arm, requestID := names[i], billingBoundaryRequestID(names[i])
		already := successful[requestID]
		before := state
		if profile.name != "last_unit_quota" && already {
			// RequestID is attribution, not a global idempotency guarantee. Never call
			// a successful unlimited/negative-balance request again to manufacture two debits.
			logBillingBoundaryJSON(t, "retry decision", struct {
				Arm, RequestID, Action string
				AlreadySuccessful      bool
			}{arm, requestID, "skipped-successful-request", true})
			continue
		}
		result := callBillingBoundary(db, arm, time.Second)
		logBillingBoundaryJSON(t, "retry result", struct {
			AlreadySuccessful bool
			BeforeRows        int
			Result            billingBoundaryResult
		}{already, before.Rows, result})
		state = readBillingBoundary(t, b)
		logBillingBoundaryState(t, "after retry "+arm, state)
		if result.Classification == "other" || result.Classification == "busy" {
			t.Fatalf("unexpected bounded retry error: %+v", result)
		}
		assertBillingBoundaryState(t, state, initial, profile)
		if profile.name == "last_unit_quota" && before.Rows == 1 {
			if !result.QuotaExceeded || result.Success {
				t.Fatal("spent quota retry did not return quota exceeded")
			}
			if !reflect.DeepEqual(state, before) {
				t.Fatal("quota-rejected retry changed complete persisted state")
			}
		} else {
			if already || !result.Success || state.Rows != before.Rows+1 {
				t.Fatal("failed request retry did not create exactly one new attributable debit")
			}
			successful[requestID] = true
			successfulCalls[requestID]++
		}
		assertBillingBoundaryAttribution(t, state, successful, successfulCalls)
	}
	final := readBillingBoundary(t, b)
	logBillingBoundaryState(t, "final", final)
	successfulIDs := []string{}
	for id := range successful {
		successfulIDs = append(successfulIDs, id)
	}
	sort.Strings(successfulIDs)
	logBillingBoundaryJSON(t, "successful request attribution", struct {
		Requests        []string
		SuccessfulCalls map[string]int
	}{successfulIDs, successfulCalls})
	assertBillingBoundaryState(t, final, initial, profile)
	assertBillingBoundaryAttribution(t, final, successful, successfulCalls)
	if final.Rows != profile.finalRows || final.Token.TotalAmountUsed != profile.finalUsage || final.User.Balance != profile.finalBalance || !reflect.DeepEqual(final, state) {
		t.Fatalf("final profile financial boundary mismatch: profile=%s rows=%d usage=%.6f wallet=%.6f", profile.name, final.Rows, final.Token.TotalAmountUsed, final.User.Balance)
	}
	t.Log(billingBoundaryComplete + profile.name)
}

type billingBoundaryRead struct {
	Arm       string  `json:"arm"`
	RequestID string  `json:"request_id"`
	SQLTx     bool    `json:"sql_tx"`
	TokenID   int32   `json:"token_id"`
	UserID    int32   `json:"user_id"`
	Used      float64 `json:"used"`
	Limit     float64 `json:"limit"`
}
type billingBoundaryResult struct {
	Arm            string `json:"arm"`
	RequestID      string `json:"request_id"`
	Success        bool   `json:"success"`
	Classification string `json:"classification"`
	Error          string `json:"error"`
	ErrorType      string `json:"error_type"`
	SQLiteCode     *int   `json:"sqlite_code"`
	QuotaExceeded  bool   `json:"quota_exceeded"`
}
type billingBoundaryState struct {
	User            entity.User            `json:"user"`
	UserPasswordSHA string                 `json:"user_password_sha256"`
	Token           entity.AccessToken     `json:"token"`
	Ledger          []entity.BillingRecord `json:"ledger"`
	Rows            int                    `json:"rows"`
	Total           float64                `json:"total"`
}

func billingBoundaryRequestID(arm string) string {
	switch arm {
	case "A":
		return "d03_quota_charge_A"
	case "B":
		return "d03_quota_charge_B"
	default:
		return ""
	}
}
func billingBoundaryHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func logBillingBoundaryJSON(t *testing.T, label string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", label, encoded)
}
func logBillingBoundaryState(t *testing.T, label string, state billingBoundaryState) {
	t.Helper()
	logBillingBoundaryJSON(t, label, state)
}
func readBillingBoundary(t *testing.T, db *gorm.DB) billingBoundaryState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state := billingBoundaryState{Ledger: []entity.BillingRecord{}}
	if err := db.WithContext(ctx).First(&state.User, int32(2)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).First(&state.Token, int32(1)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Order("id").Find(&state.Ledger).Error; err != nil {
		t.Fatal(err)
	}
	state.UserPasswordSHA = billingBoundaryHash(state.User.Password)
	state.Rows = len(state.Ledger)
	for _, row := range state.Ledger {
		state.Total += row.Amount
	}
	state.Total = util.RoundMoney(state.Total)
	return state
}
func seedBillingBoundary(t *testing.T, db *gorm.DB, profile billingBoundaryProfile) billingBoundaryState {
	t.Helper()
	fixed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	last := fixed.Add(-time.Hour)
	user := entity.User{ID: 2, Username: "quota-fixture-user", Password: "synthetic-quota-password", Perms: "search", Nickname: "Quota fixture", Role: "user", Status: "enabled", Balance: profile.initialBalance, RegisterSource: "admin", CreatedAt: fixed, LastLoginAt: &last, LastLoginIP: "192.0.2.11", RegisterIP: "192.0.2.12"}
	token := entity.AccessToken{ID: 1, UserID: 2, Name: "quota-fixture-token", TokenHash: billingBoundaryHash("tk_synthetic_quota_charge_only"), TokenPrefix: "tk_synthetic", TokenValue: "tk_synthetic_quota_charge_only", Status: "enabled", TotalQuotaLimit: profile.limit, TotalAmountUsed: 0.020001, LastUsedAt: &last, LastUsedIP: "192.0.2.13", Remark: "quota charge fixture", CreatedAt: fixed, UpdatedAt: fixed}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	initial := readBillingBoundary(t, db)
	if initial.User.LastLoginAt == nil || !initial.User.LastLoginAt.Equal(last) || !initial.User.CreatedAt.Equal(fixed) || initial.Token.LastUsedAt == nil || !initial.Token.LastUsedAt.Equal(last) || !initial.Token.CreatedAt.Equal(fixed) || !initial.Token.UpdatedAt.Equal(fixed) {
		t.Fatal("synthetic seed timestamps differ")
	}
	seededUser := initial.User
	seededUser.CreatedAt = user.CreatedAt
	seededUser.LastLoginAt = user.LastLoginAt
	seededToken := initial.Token
	seededToken.CreatedAt = token.CreatedAt
	seededToken.UpdatedAt = token.UpdatedAt
	seededToken.LastUsedAt = token.LastUsedAt
	if !reflect.DeepEqual(seededUser, user) || !reflect.DeepEqual(seededToken, token) || initial.Rows != 0 || initial.Total != 0 || initial.UserPasswordSHA != billingBoundaryHash(user.Password) {
		t.Fatal("complete synthetic billing seed mismatch")
	}
	return initial
}
func callBillingBoundary(db *gorm.DB, arm string, budget time.Duration) billingBoundaryResult {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	err := persistSearchBillingRecordsWithDB(db.WithContext(ctx), searchBillingContext{UserID: 2, AccessTokenID: 1, RequestID: billingBoundaryRequestID(arm), SearchMode: searchModeNormal, UsedDefaultAccount: true, Billable: true, ClientIP: "192.0.2.23"}, []searchBillingCharge{{Provider: "icodef", Amount: 0.030003, Snapshot: billingBoundarySnapshot}})
	cancel()
	result := billingBoundaryResult{Arm: arm, RequestID: billingBoundaryRequestID(arm), Success: err == nil, Classification: "success"}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	result.ErrorType = fmt.Sprintf("%T", err)
	result.QuotaExceeded = errors.Is(err, errAccessTokenQuotaExceeded)
	var code interface{ Code() int }
	if errors.As(err, &code) {
		n := code.Code()
		result.SQLiteCode = &n
	}
	switch {
	case result.QuotaExceeded:
		result.Classification = "quota"
	case result.SQLiteCode != nil && (*result.SQLiteCode&255 == 5 || *result.SQLiteCode&255 == 6):
		result.Classification = "busy"
	default:
		result.Classification = "other"
	}
	return result
}
func assertBillingBoundaryAttribution(t *testing.T, state billingBoundaryState, successful map[string]bool, calls map[string]int) {
	t.Helper()
	if len(successful) != state.Rows {
		t.Fatal("successful request identities do not match ledger rows")
	}
	seen := map[string]bool{}
	for _, row := range state.Ledger {
		if seen[row.RequestID] || !successful[row.RequestID] || calls[row.RequestID] != 1 {
			t.Fatalf("duplicate or unattributed successful request debit: request=%s successfulCalls=%d", row.RequestID, calls[row.RequestID])
		}
		seen[row.RequestID] = true
	}
}
func assertBillingBoundaryState(t *testing.T, state, initial billingBoundaryState, profile billingBoundaryProfile) {
	t.Helper()
	if state.Rows != len(state.Ledger) || state.Rows < 0 || state.Rows > profile.finalRows {
		t.Fatal("more completed debits than this profile allows")
	}
	expectedUsage := util.RoundMoney(0.020001 + float64(state.Rows)*0.030003)
	expectedBalance := util.RoundMoney(profile.initialBalance - float64(state.Rows)*0.030003)
	expectedTotal := util.RoundMoney(float64(state.Rows) * 0.030003)
	if state.Token.TotalAmountUsed != expectedUsage || state.User.Balance != expectedBalance || state.Total != expectedTotal {
		t.Fatal("wallet/usage/ledger not one complete effect per debit")
	}
	user := state.User
	user.Balance = initial.User.Balance
	if !reflect.DeepEqual(user, initial.User) || state.UserPasswordSHA != initial.UserPasswordSHA {
		t.Fatal("user identity or non-balance fields changed")
	}
	token := state.Token
	token.TotalAmountUsed = initial.Token.TotalAmountUsed
	token.UpdatedAt = initial.Token.UpdatedAt
	if !reflect.DeepEqual(token, initial.Token) {
		t.Fatal("token identity/limit/last-used fields changed")
	}
	if state.Token.UpdatedAt.IsZero() || state.Token.UpdatedAt.Before(initial.Token.UpdatedAt) {
		t.Fatal("token timestamp invalid")
	}
	if state.Rows == 0 && !state.Token.UpdatedAt.Equal(initial.Token.UpdatedAt) {
		t.Fatal("no-debit state changed update timestamp")
	}
	balance := profile.initialBalance
	for _, row := range state.Ledger {
		before := util.RoundMoney(balance)
		balance -= 0.030003
		after := util.RoundMoney(balance)
		validRequest := row.RequestID == billingBoundaryRequestID("A") || row.RequestID == billingBoundaryRequestID("B")
		if row.ID <= 0 || row.UserID != 2 || row.AccessTokenID != 1 || row.Type != searchBillingRecordTypeNormal || !row.UsedDefaultAccount || row.Provider != "icodef" || row.ModelName != "" || row.InputTokens != 0 || row.CachedInputTokens != 0 || row.OutputTokens != 0 || row.TotalTokens != 0 || row.UnitPriceSnapshot != billingBoundarySnapshot || row.QuestionText != "D03 quota boundary" || row.Amount != 0.030003 || row.BalanceBefore != before || row.BalanceAfter != after || !validRequest || row.ClientIP != "192.0.2.23" || row.RelatedOrderID != 0 || row.Remark != "搜索答题消耗" || row.CreatedAt.IsZero() || row.CreatedAt.Before(initial.User.CreatedAt) {
			t.Fatalf("complete ledger row or balance chain mismatch: %+v", row)
		}
	}
}
