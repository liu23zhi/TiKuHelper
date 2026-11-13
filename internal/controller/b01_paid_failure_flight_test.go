package controller

// Actual keyless HTTP Search entry + actual sharedflight and SQL settlement.
// Only the model/provider dependency seams are synthetic. Each vector owns a
// fresh subprocess and SQLite; no original DB, real model, or account is used.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

const b01PaidGuard = 2 * time.Second // Fixture deadlock bound, not a product budget.
const b01PaidUnsafeError = "synthetic-private-paid-model-failure"

type b01PaidCheck struct {
	Label     string `json:"label"`
	Qualified bool   `json:"qualified"`
	Passed    bool   `json:"passed"`
}
type b01PaidLedger struct {
	Vector        string         `json:"vector"`
	Checks        []b01PaidCheck `json:"checks"`
	CompletedFlow bool           `json:"completedFlow"`
	OwnerDrained  bool           `json:"ownerDrained"`
	SetupIssue    string         `json:"setupIssue,omitempty"`
	Calls         int32          `json:"actualMockEntries"`
	Reports       int32          `json:"reports"`
	Resolves      int32          `json:"resolvedRequests"`
	Unexpected    int32          `json:"unexpectedCalls"`
	Rows          int64          `json:"actualBillRows"`
	AmountMicro   int64          `json:"actualAmountMicro"`
	WalletMicro   int64          `json:"actualWalletMicro"`
	QuotaMicro    int64          `json:"actualQuotaMicro"`
}
type b01PaidHTTP struct {
	done     chan struct{}
	status   int
	body     []byte
	result   model.SearchResponse
	decode   error
	panicked bool
}

func TestB01PaidFailureFlight(t *testing.T) {
	child := os.Getenv("B01_PAID_FAILURE_CHILD")
	if child != "" {
		if child != "paid_usage" && child != "zero_usage" {
			t.Fatal("invalid fixed child vector")
		}
		b01PaidVector(t, child)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	for _, vector := range []string{"paid_usage", "zero_usage"} {
		t.Run(vector, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			home := t.TempDir()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestB01PaidFailureFlight$", "-test.parallel=1", "-test.timeout=12s", "-test.v")
			cmd.Dir = home
			// ⚠️ 整体替换 Env，所以平台必需项要给全。
			//
			// Windows 的 GetTempPath 只认 TMP → TEMP → USERPROFILE，**不看 TMPDIR**
			// （那是 POSIX 的名字）。只给 TMPDIR 时子进程的 t.TempDir() 会退化成
			// `mkdir C:\WINDOWS\...: Access is denied`，那不是被测行为而是夹具缺陷。
			// 实测：只设 TMPDIR 时 os.TempDir() = "C:\Users\<user>"。
			childEnv := []string{"HOME=" + home, "TZ=UTC", "GOMAXPROCS=2", "B01_PAID_FAILURE_CHILD=" + vector}
			if runtime.GOOS == "windows" {
				childEnv = append(childEnv,
					"TEMP="+home, "TMP="+home,
					"PATH="+os.Getenv("PATH"),
					"SystemRoot="+os.Getenv("SystemRoot"),
					"USERPROFILE="+home,
				)
			} else {
				childEnv = append(childEnv,
					"PATH=/usr/bin:/bin", "TMPDIR="+home,
					"LANG=C.UTF-8", "LC_ALL=C.UTF-8",
				)
			}
			cmd.Env = childEnv
			if dir := os.Getenv("B01_PAID_FAILURE_LEDGER_DIR"); dir != "" {
				if !filepath.IsAbs(dir) {
					t.Fatal("absolute owned ledger directory required")
				}
				cmd.Env = append(cmd.Env, "B01_PAID_FAILURE_LEDGER_DIR="+dir)
			}
			output, err := cmd.CombinedOutput()
			t.Logf("B01_PAID_CHILD vector=%s\n%s", vector, output)
			if err != nil {
				t.Errorf("owned real HTTP vector failed; processGuard=%t", ctx.Err() != nil)
			}
		})
	}
}

func b01PaidUsage() searchpkg.AISearchUsage {
	// Exact existing failedModelUsage amounts/fields, uniquely prefixed helper.
	return searchpkg.AISearchUsage{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020, ModelCalls: []searchpkg.AIModelCallUsage{{Model: "failed-usage-model", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}}}
}

func b01PaidVector(t *testing.T, vector string) {
	paid := vector == "paid_usage"
	ledger := b01PaidLedger{Vector: vector}
	check := func(label string, passed bool) {
		ledger.Checks = append(ledger.Checks, b01PaidCheck{label, true, passed})
		if !passed {
			t.Errorf("B01_PAID_CONTRACT_RED: %s", label)
		}
	}
	if dao.Log != nil || (manager.GetManager() != nil && manager.GetManager().GetDB() != nil) || searchFlights.inFlight() != 0 {
		t.Fatal("SETUP_INCONCLUSIVE: child must start without DB/flight")
	}
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "owned.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("owned SQLite open")
	}
	if err = db.AutoMigrate(&entity.SearchAccountConfig{}, &entity.AISearchConfig{}, &entity.AIModelPrice{}, &entity.SystemSetting{}, &entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.Log{}, &entity.Tiku{}); err != nil {
		t.Fatal("owned schema")
	}
	dao.SetDefault(db)
	actor := entity.User{Username: "synthetic-paid-flight", Password: "synthetic-unused", Role: "user", Status: "enabled", Balance: .0001, RegisterSource: "public"}
	if db.Create(&actor).Error != nil {
		t.Fatal("owned user seed")
	}
	plain := "synthetic-paid-flight-no-real-credential"
	hash := sha256.Sum256([]byte(plain))
	token := entity.AccessToken{UserID: actor.ID, Name: "synthetic-paid-flight", TokenHash: hex.EncodeToString(hash[:]), TokenPrefix: "synthetic", TokenValue: plain, Status: "enabled", TotalQuotaLimit: 0}
	price := entity.AIModelPrice{ModelName: "failed-usage-model", InputPrice: 1, CachedPrice: .1, OutputPrice: 2, Multiplier: 1, Status: "enabled", IsUserModified: true}
	setting := entity.SystemSetting{Key: "enable_ai_staged_token_billing", Value: "false"}
	if db.Create(&token).Error != nil || db.Create(&price).Error != nil || db.Create(&setting).Error != nil {
		t.Fatal("owned financial seed")
	}
	oldResolver, oldLocal, oldBindings, oldAI, oldReporter := resolveSearchRequestContext, defaultDBSearchProvider, buildSearchClientBindings, searchAIAnswer, searchBillingReporter
	searchResultCache.clear()
	searchFlights.reset()
	var calls, reports, resolves, unexpected atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	releaseWork := func() { once.Do(func() { close(release) }) }
	var handlers sync.WaitGroup
	defer func() {
		releaseWork()
		handlers.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), b01PaidGuard)
		ledger.OwnerDrained = WaitForSearchExecutions(ctx) == nil
		cancel()
		check("all f06 real execution owners drained before seam/pool teardown", ledger.OwnerDrained)
		ledger.Calls, ledger.Reports, ledger.Resolves, ledger.Unexpected = calls.Load(), reports.Load(), resolves.Load(), unexpected.Load()
		if ledger.OwnerDrained {
			resolveSearchRequestContext, defaultDBSearchProvider, buildSearchClientBindings, searchAIAnswer, searchBillingReporter = oldResolver, oldLocal, oldBindings, oldAI, oldReporter
			searchResultCache.clear()
			searchFlights.reset()
			if pool, e := db.DB(); e == nil {
				_ = pool.Close()
			}
		}
		encoded, _ := json.Marshal(ledger)
		t.Logf("B01_PAID_FAILURE_LEDGER %s", encoded)
		if dir := os.Getenv("B01_PAID_FAILURE_LEDGER_DIR"); dir != "" {
			f, e := os.OpenFile(filepath.Join(dir, vector+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				t.Error("fresh owned ledger write failed")
			} else {
				_, _ = f.Write(append(encoded, '\n'))
				_ = f.Close()
			}
		}
	}()
	resolved := func(id int32) resolvedSearchRequestContext {
		return resolvedSearchRequestContext{SearchMode: searchModeAI, UsedDefaultAI: true, UserID: actor.ID, AccessTokenID: token.ID, RequestID: fmt.Sprintf("synthetic-flight-%d", id), AIConfig: service.AISearchRequestConfig{BaseURL: "https://example.invalid/v1", APIKey: "synthetic-never-called", Model: "failed-usage-model"}, AccessTokenCtx: &service.AccessTokenAuthContext{User: actor, AccessToken: token}}
	}
	resolveSearchRequestContext = func(context.Context, string, string, url.Values) (resolvedSearchRequestContext, error) {
		return resolved(resolves.Add(1)), nil
	}
	defaultDBSearchProvider = func() func(model.SearchRequest) ([][]string, error) {
		return func(model.SearchRequest) ([][]string, error) {
			unexpected.Add(1)
			return nil, errors.New("unexpected local search")
		}
	}
	buildSearchClientBindings = func(resolvedSearchRequestContext, string) []searchClientBinding { unexpected.Add(1); return nil }
	searchBillingReporter = func(searchBillingContext) { reports.Add(1) }
	searchAIAnswer = func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
		entry := calls.Add(1)
		if entry == 1 {
			entered <- struct{}{}
			<-release
		}
		usage := searchpkg.AISearchUsage{}
		if paid {
			usage = b01PaidUsage()
		}
		return nil, usage, errors.New(b01PaidUnsafeError)
	}
	question := model.SearchRequest{Question: "synthetic-" + vector, Type: 4, Options: []string{"A", "B"}}
	q1, q2 := question, question
	q1.RequestID = resolved(1).RequestID
	q2.RequestID = resolved(2).RequestID
	k1, e1, ok1 := buildSearchResultCacheKey(resolved(1), q1, "ai")
	k2, e2, ok2 := buildSearchResultCacheKey(resolved(2), q2, "ai")
	if !ok1 || !ok2 || k1 != k2 || e1 != e2 {
		ledger.SetupIssue = "distinct requestIDs must have same real cachekey"
		t.Error("SETUP_INCONCLUSIVE: cachekey")
		return
	}
	check("distinct real requestID contexts share actual cache key/epoch", true)
	launch := func() *b01PaidHTTP {
		call := &b01PaidHTTP{done: make(chan struct{})}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer close(call.done)
			defer func() {
				if recover() != nil {
					call.panicked = true
				}
			}()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			body, _ := json.Marshal(question)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+plain)
			// Deliberately no Idempotency-Key and no skipCache override.
			c.Request = request
			SearchWithIdempotency(c)
			call.status = w.Code
			call.body = append([]byte(nil), w.Body.Bytes()...)
			call.decode = json.Unmarshal(call.body, &call.result)
		}()
		return call
	}
	leader := launch()
	if !b01PaidUntil(func() bool { return calls.Load() == 1 && searchFlights.inFlight() == 1 }) {
		ledger.SetupIssue = "actual leader work/flight not entered"
		t.Error("SETUP_INCONCLUSIVE: leader")
		return
	}
	select {
	case <-entered:
	default:
		ledger.SetupIssue = "mock handshake missing"
		t.Error("SETUP_INCONCLUSIVE: mock")
		return
	}
	follower := launch()
	if !b01PaidUntil(func() bool { return resolves.Load() == 2 && calls.Load() == 1 && b01PaidActualWaits() == 1 }) {
		ledger.SetupIssue = "actual follower sharedwait missing"
		t.Error("SETUP_INCONCLUSIVE: follower overlap")
		return
	}
	check("both live requests resolved before charge and follower is in actual production flight wait", true)
	releaseWork()
	leaderDone, followerDone := b01PaidReturned(leader), b01PaidReturned(follower)
	check("both actual HTTP requests fully return without panic", leaderDone && followerDone && !leader.panicked && !follower.panicked)
	if !leaderDone || !followerDone {
		return
	}
	for _, call := range []*b01PaidHTTP{leader, follower} {
		check("each observer receives nonempty structured safe upstream failure, not empty200", call.status == http.StatusOK && len(call.body) > 0 && call.decode == nil && call.result.Error != nil && call.result.Error.Code == "UPSTREAM_ERROR" && call.result.Error.Message != "" && !strings.Contains(string(call.body), b01PaidUnsafeError) && call.result.Answer.AnswerText == "")
	}
	wantCalls := int32(2)
	if paid {
		wantCalls = 1
	}
	check("cohort model entries follow paid-sharing versus zero-usage legacy contract", calls.Load() == wantCalls)
	// Do not Fatal on the cohort RED: ALL actual model entries must be charged.
	check("real completed flight registration is released", b01PaidUntil(func() bool { return searchFlights.inFlight() == 0 }))
	var bills []entity.BillingRecord
	readErr := db.Where("user_id = ?", actor.ID).Find(&bills).Error
	ledger.Rows = int64(len(bills))
	requestIDs := map[string]bool{}
	dimensions := true
	for _, bill := range bills {
		ledger.AmountMicro += int64(math.Round(bill.Amount * 1e6))
		requestIDs[bill.RequestID] = true
		dimensions = dimensions && bill.ModelName == "failed-usage-model" && bill.InputTokens == 1000 && bill.CachedInputTokens == 100 && bill.OutputTokens == 20 && bill.TotalTokens == 1020 && int64(math.Round(bill.Amount*1e6)) == 950
	}
	var userRow entity.User
	var tokenRow entity.AccessToken
	userErr, tokenErr := db.First(&userRow, actor.ID).Error, db.First(&tokenRow, token.ID).Error
	ledger.WalletMicro = int64(math.Round(userRow.Balance * 1e6))
	ledger.QuotaMicro = int64(math.Round(tokenRow.TotalAmountUsed * 1e6))
	actualN := int64(calls.Load())
	wantRows, wantAmount, wantWallet, wantUsed := int64(0), int64(0), int64(100), int64(0)
	if paid {
		wantRows, wantAmount, wantWallet, wantUsed = actualN, actualN*950, 100-actualN*950, actualN*950
	}
	check("actual SQL accounts for ALL executed usage including baseline duplicate calls", readErr == nil && userErr == nil && tokenErr == nil && ledger.Rows == wantRows && ledger.AmountMicro == wantAmount && ledger.WalletMicro == wantWallet && ledger.QuotaMicro == wantUsed)
	check("paid model dimensions/prices/request IDs persist without hiding double execution", !paid || (actualN > 0 && dimensions && int64(len(requestIDs)) == actualN))
	check("zero actual usage produces zero charge and retains legacy continuation", paid || (calls.Load() == 2 && ledger.Rows == 0 && ledger.AmountMicro == 0 && ledger.WalletMicro == 100 && ledger.QuotaMicro == 0))
	check("every actual model entry reported once with no other provider/local call", reports.Load() == calls.Load() && unexpected.Load() == 0)
	ledger.CompletedFlow = true
}

func b01PaidReturned(call *b01PaidHTTP) bool {
	timer := time.NewTimer(b01PaidGuard)
	defer timer.Stop()
	select {
	case <-call.done:
		return true
	case <-timer.C:
		return false
	}
}
func b01PaidUntil(predicate func() bool) bool {
	timer := time.NewTimer(b01PaidGuard)
	ticker := time.NewTicker(time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		if predicate() {
			return true
		}
		select {
		case <-timer.C:
			return predicate()
		case <-ticker.C:
		}
	}
}
func b01PaidActualWaits() int {
	for size := 64 << 10; size <= 2<<20; size *= 2 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			continue
		}
		count := 0
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			body := strings.Contains(stack, ".Search(") || strings.Contains(stack, ".executeSearch(") || strings.Contains(stack, ".searchWithObserverContext(")
			wait := strings.Contains(stack, ".waitSearchFlightResult") || (strings.Contains(stack, ".beginSearchFlight") && strings.Contains(stack, ".waitContext("))
			if body && wait && strings.Contains(stack, "/internal/controller/search") {
				count++
			}
		}
		return count
	}
	return -1
}
