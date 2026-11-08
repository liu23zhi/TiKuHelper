package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

const priceHistoryChildEnv = "TIKU_TEST_AI_PRICE_HISTORY_CHILD"
const priceHistoryWorkdirEnv = "TIKU_TEST_AI_PRICE_HISTORY_WORKDIR"
const priceHistoryModel = "d03-snapshot-history-model"
const priceHistoryQuestion = "历史价格快照题"
const priceHistoryRequestID = "d03_price_history_old"
const priceHistoryComplete = "PRICE_SNAPSHOT_HISTORY_ROUNDTRIP_COMPLETE"

func TestAIModelPriceChangePreservesHistoricalBilling(t *testing.T) {
	if os.Getenv(priceHistoryChildEnv) != "" {
		t.Fatal("parent received child mode")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAIModelPriceChangePreservesHistoricalBillingChild$", "-test.v", "-test.timeout=16s")
	cmd.Env = append(os.Environ(), priceHistoryChildEnv+"=1", priceHistoryWorkdirEnv+"="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	t.Logf("child stdout:\n%s", stdout.Bytes())
	if stderr.Len() != 0 {
		t.Logf("child stderr:\n%s", stderr.Bytes())
	}
	if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("price-history child failed: run=%v context=%v", err, ctx.Err())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(priceHistoryComplete)) {
		t.Fatal("child did not reach complete conservation assertion")
	}
}

type priceHistoryState struct {
	User            entity.User                     `json:"user"`
	UserPasswordSHA string                          `json:"user_password_sha256"`
	Token           entity.AccessToken              `json:"token"`
	Ledger          []entity.BillingRecord          `json:"ledger"`
	Listing         service.BillingRecordListResult `json:"listing"`
	ListingSnapshot string                          `json:"listing_snapshot"`
}

func priceHistoryLog(t *testing.T, label string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", label, b)
}
func priceHistoryHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func priceHistoryRead(t *testing.T, db *gorm.DB, operator entity.User) priceHistoryState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := priceHistoryState{Ledger: []entity.BillingRecord{}}
	if err := db.WithContext(ctx).First(&out.User, int32(2)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).First(&out.Token, int32(1)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Order("id").Find(&out.Ledger).Error; err != nil {
		t.Fatal(err)
	}
	out.UserPasswordSHA = priceHistoryHash(out.User.Password)
	var err error
	out.Listing, err = service.ListBillingRecordsWithDB(db.WithContext(ctx), operator, service.BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Listing.Data) == 1 {
		out.ListingSnapshot = out.Listing.Data[0].UnitPriceSnapshot
	}
	return out
}
func priceHistoryResolved(t *testing.T, db *gorm.DB) service.ResolvedAIModelTokenPrice {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	price, found, err := service.ResolveAIModelTokenPriceWithDB(db.WithContext(ctx), priceHistoryModel)
	if err != nil || !found {
		t.Fatalf("resolve real synthetic model price: found=%t error=%v", found, err)
	}
	return price
}
func priceHistorySnapshotCheck(t *testing.T, snapshot string, priceID int32, newPrice bool) {
	t.Helper()
	want := map[string]string{"provider": "ai", "model": priceHistoryModel, "pricing": "model_price_table", "priceId": fmt.Sprint(priceID), "multiplier": "1.0000", "cacheWritePrice": "0.0000", "cacheWriteUnitPrice": "0.00000000", "inputTokens": "600000", "cachedInputTokens": "400000", "cacheWriteTokens": "0", "outputTokens": "200000", "totalTokens": "1200000", "question": url.QueryEscape(priceHistoryQuestion)}
	if newPrice {
		for key, value := range map[string]string{"inputPrice": "10.0000", "cachedPrice": "1.0000", "outputPrice": "60.0000", "inputUnitPrice": "10.00000000", "cachedUnitPrice": "1.00000000", "outputUnitPrice": "60.00000000", "baseAmount": "18.400000", "amount": "18.400000"} {
			want[key] = value
		}
	} else {
		for key, value := range map[string]string{"inputPrice": "5.0000", "cachedPrice": "0.5000", "outputPrice": "30.0000", "inputUnitPrice": "5.00000000", "cachedUnitPrice": "0.50000000", "outputUnitPrice": "30.00000000", "baseAmount": "9.200000", "amount": "9.200000"} {
			want[key] = value
		}
	}
	got := map[string]string{}
	for _, part := range strings.Fields(snapshot) {
		pair := strings.SplitN(part, "=", 2)
		if len(pair) != 2 {
			t.Fatal("malformed actual snapshot")
		}
		if _, exists := got[pair[0]]; exists {
			t.Fatal("duplicate actual snapshot field")
		}
		got[pair[0]] = pair[1]
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actual builder snapshot differs: got=%v want=%v", got, want)
	}
}

func TestAIModelPriceChangePreservesHistoricalBillingChild(t *testing.T) {
	if os.Getenv(priceHistoryChildEnv) != "1" {
		t.Skip("private bounded price-history child")
	}
	workdir := os.Getenv(priceHistoryWorkdirEnv)
	if !filepath.IsAbs(workdir) {
		t.Fatal("parent-owned absolute temporary directory required")
	}
	path := filepath.Join(workdir, "price-history.db")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("synthetic database must not already exist")
	}
	watchdog := time.AfterFunc(12*time.Second, func() { fmt.Fprintln(os.Stderr, "price-history child watchdog"); os.Exit(24) })
	defer watchdog.Stop()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err = db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.AIModelPrice{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	owner := entity.User{ID: 2, Username: "snapshot-history-fixture", Password: "synthetic-price-history-password", Role: "user", Status: "enabled", Balance: 100, RegisterSource: "admin", CreatedAt: fixed}
	token := entity.AccessToken{ID: 1, UserID: 2, Name: "snapshot-history-token", TokenHash: priceHistoryHash("tk_synthetic_price_history_only"), TokenPrefix: "tk_synthetic", TokenValue: "tk_synthetic_price_history_only", Status: "enabled", TotalQuotaLimit: 0, TotalAmountUsed: 0, CreatedAt: fixed, UpdatedAt: fixed}
	if err = db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	// Real startup seeds built-in prices before request-scoped one-second budgets.
	// Exercise the same initializer in fixture setup; all getter/settlement budgets and conservation assertions stay unchanged.
	if err = service.EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatal(err)
	}
	initial := priceHistoryRead(t, db, owner)
	priceHistoryLog(t, "initial financial state", initial)
	if len(initial.Ledger) != 0 || initial.User.Balance != 100 || initial.Token.TotalAmountUsed != 0 || initial.Listing.Total != 0 {
		t.Fatal("unexpected synthetic initial state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	created, err := service.CreateAIModelPriceWithDB(db.WithContext(ctx), service.CreateAIModelPriceInput{ModelName: priceHistoryModel, DisplayName: "D03 snapshot fixture", InputPrice: 5, CachedPrice: 0.5, CacheWritePrice: 0, OutputPrice: 30, Multiplier: 1, Status: "enabled", Sort: 0, Remark: "old synthetic price"})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	old := priceHistoryResolved(t, db)
	priceHistoryLog(t, "old price through actual getter", old)
	if old.PriceID != created.ID || old.ModelName != priceHistoryModel || old.InputUnitPrice != 5 || old.CachedUnitPrice != 0.5 || old.CacheWriteUnitPrice != 0 || old.OutputUnitPrice != 30 || old.Multiplier != 1 {
		t.Fatal("old price getter values differ")
	}
	usage := searchpkg.AISearchUsage{InputTokens: 1000000, CachedInputTokens: 400000, CacheWriteTokens: 0, OutputTokens: 200000, TotalTokens: 1200000}
	req := model.SearchRequest{Question: priceHistoryQuestion}
	resolved := resolvedSearchRequestContext{AIConfig: service.AISearchRequestConfig{Model: priceHistoryModel}}
	oldBase, oldAmount := calculateAIModelPriceTableAmount(old, usage)
	oldSnapshot := buildAIModelPriceTableBillingSnapshot(resolved, req, usage, old, oldBase, oldAmount)
	priceHistoryLog(t, "old actual quote", struct {
		Base, Amount float64
		Snapshot     string
	}{oldBase, oldAmount, oldSnapshot})
	if oldBase != 9.2 || oldAmount != 9.2 {
		t.Fatal("old real calculator amount must be9.2")
	}
	priceHistorySnapshotCheck(t, oldSnapshot, created.ID, false)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = persistSearchBillingRecordsWithDB(db.WithContext(ctx), searchBillingContext{UserID: 2, AccessTokenID: 1, RequestID: priceHistoryRequestID, SearchMode: searchModeAI, UsedDefaultAccount: true, Billable: true, ClientIP: "192.0.2.31"}, []searchBillingCharge{{Provider: searchModeAI, ModelName: priceHistoryModel, InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, Amount: oldAmount, Snapshot: oldSnapshot}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	before := priceHistoryRead(t, db, owner)
	priceHistoryLog(t, "old persisted row and listing before price change", before)
	assertPriceHistoryOldState(t, before, initial, oldSnapshot)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	updated, err := service.UpdateAIModelPriceWithDB(db.WithContext(ctx), created.ID, service.UpdateAIModelPriceInput{ModelName: priceHistoryModel, DisplayName: "D03 snapshot fixture", InputPrice: 10, CachedPrice: 1, CacheWritePrice: 0, OutputPrice: 60, Multiplier: 1, Status: "enabled", Sort: 0, Remark: "changed synthetic price"})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	current := priceHistoryResolved(t, db)
	priceHistoryLog(t, "updated row from actual setter", updated)
	priceHistoryLog(t, "new price through actual getter", current)
	if updated.ID != created.ID || !updated.IsUserModified || current.PriceID != created.ID || current.ModelName != priceHistoryModel || current.InputUnitPrice != 10 || current.CachedUnitPrice != 1 || current.CacheWriteUnitPrice != 0 || current.OutputUnitPrice != 60 || current.Multiplier != 1 {
		t.Fatal("actual price setter/getter did not apply new units")
	}
	newBase, newAmount := calculateAIModelPriceTableAmount(current, usage)
	newSnapshot := buildAIModelPriceTableBillingSnapshot(resolved, req, usage, current, newBase, newAmount)
	priceHistoryLog(t, "new actual quote only; not charged", struct {
		Base, Amount float64
		Snapshot     string
	}{newBase, newAmount, newSnapshot})
	if newBase != 18.4 || newAmount != 18.4 || newSnapshot == oldSnapshot {
		t.Fatal("new real calculator must reflect changed price as18.4")
	}
	priceHistorySnapshotCheck(t, newSnapshot, created.ID, true)
	// No second persistence call: calculating a new quote must not reprice or debit history.
	after := priceHistoryRead(t, db, owner)
	priceHistoryLog(t, "old persisted row and listing after price change", after)
	assertPriceHistoryOldState(t, after, initial, oldSnapshot)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("price configuration change mutated or reinterpreted complete old financial state")
	}
	t.Log(priceHistoryComplete)
}
func assertPriceHistoryOldState(t *testing.T, state, initial priceHistoryState, snapshot string) {
	t.Helper()
	if len(state.Ledger) != 1 {
		t.Fatal("historical row count must remain exactly one")
	}
	row := state.Ledger[0]
	if row.ID <= 0 || row.UserID != 2 || row.AccessTokenID != 1 || row.Type != searchBillingRecordTypeNormal || !row.UsedDefaultAccount || row.Provider != searchModeAI || row.ModelName != priceHistoryModel || row.InputTokens != 1000000 || row.CachedInputTokens != 400000 || row.OutputTokens != 200000 || row.TotalTokens != 1200000 || row.Amount != 9.2 || row.BalanceBefore != 100 || row.BalanceAfter != 90.8 || row.RequestID != priceHistoryRequestID || row.UnitPriceSnapshot != snapshot || row.QuestionText != priceHistoryQuestion || row.ClientIP != "192.0.2.31" || row.RelatedOrderID != 0 || row.Remark != "搜索答题消耗" || row.CreatedAt.IsZero() || row.CreatedAt.Before(initial.User.CreatedAt) {
		t.Fatal("complete old ledger or exact stored snapshot differs")
	}
	if state.User.Balance != 90.8 || state.Token.TotalAmountUsed != 9.2 || state.Token.TotalQuotaLimit != 0 {
		t.Fatal("old financial totals differ")
	}
	user := state.User
	user.Balance = initial.User.Balance
	if !reflect.DeepEqual(user, initial.User) || state.UserPasswordSHA != initial.UserPasswordSHA {
		t.Fatal("historical user identity changed")
	}
	token := state.Token
	token.TotalAmountUsed = initial.Token.TotalAmountUsed
	token.UpdatedAt = initial.Token.UpdatedAt
	if !reflect.DeepEqual(token, initial.Token) || state.Token.UpdatedAt.IsZero() || state.Token.UpdatedAt.Before(initial.Token.UpdatedAt) {
		t.Fatal("historical token identity changed")
	}
	list := state.Listing
	if list.Total != 1 || len(list.Data) != 1 || list.Page != 1 || list.PageSize != 10 || list.Summary.TotalAmount != 9.2 || list.Summary.CurrentPageAmount != 9.2 {
		t.Fatal("old row/page/all-page totals changed")
	}
	item := list.Data[0]
	if item.ID != row.ID || item.UserID != 2 || item.RequestID != priceHistoryRequestID || item.ReferenceNo != priceHistoryRequestID || item.Amount != 9.2 || item.Direction != "expense" || item.QuestionText != priceHistoryQuestion || item.UnitPriceSnapshot != snapshot || state.ListingSnapshot != snapshot || len(item.FeeDetails) != 1 || item.FeeDetails[0].Amount != 9.2 {
		t.Fatal("old snapshot listing interpretation changed")
	}
}
