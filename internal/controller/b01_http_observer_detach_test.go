package controller

// Six finite real HTTP-entry vectors. Each runs in a fresh copy of this test
// process: never alias an existing manager/DAO database or another test's seams.
// This file compiles against the original source without new owner-tracker APIs.
// Fixture coordination limits are NOT product timeout/cancellation policies.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
)

const b01DetachGuard = 2 * time.Second
const b01DetachSyntheticAnswer = "synthetic-shared-answer"

type b01DetachCheck struct {
	Label     string `json:"label"`
	Qualified bool   `json:"qualified"`
	Passed    bool   `json:"passed"`
}

type b01DetachLedger struct {
	Vector        string           `json:"vector"`
	Checks        []b01DetachCheck `json:"checks"`
	CompletedFlow bool             `json:"completedFlow"`
	OwnerDrained  bool             `json:"ownerDrained"`
	Calls         int32            `json:"mockWorkCalls"`
	Completions   int32            `json:"mockWorkCompletions"`
	Reports       int32            `json:"completionReports"`
	Unexpected    int32            `json:"unexpectedCalls"`
	FlightCount   int              `json:"finalFlightCount"`
	BillRows      int64            `json:"billRows"`
	WalletOnce    bool             `json:"syntheticWalletChargedOnce"`
	QuotaOnce     bool             `json:"syntheticQuotaIncrementedOnce"`
	SetupIssue    string           `json:"setupIssue,omitempty"`
}

type b01DetachCall struct {
	done     chan struct{}
	status   int
	body     []byte
	response model.SearchResponse
	decode   error
	panic    bool
}

// No t.Parallel: even isolated children use real package-global dependency seams.
func TestB01HTTPObserverDetach(t *testing.T) {
	vectors := []string{"leader_own_cancel", "leader_own_deadline", "follower_own_cancel", "follower_own_deadline", "keyed_own_cancel", "keyed_own_deadline"}
	child := os.Getenv("B01_OBSERVER_CHILD")
	if child != "" {
		for _, name := range vectors {
			if child == name {
				b01RunDetachVector(t, child)
				return
			}
		}
		t.Fatal("SETUP_INCONCLUSIVE: invalid fixed child vector")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("SETUP_INCONCLUSIVE: current test executable unavailable")
	}
	for _, vector := range vectors {
		t.Run(vector, func(t *testing.T) {
			// 12s child test bound/15s process guard are fixture deadlock protection,
			// not replacements for existing search/shared/idempotency budgets.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			home := t.TempDir()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestB01HTTPObserverDetach$", "-test.parallel=1", "-test.timeout=12s", "-test.v")
			command.Dir = home
			// 子进程环境必须按平台构造：原来写死 Linux 路径（PATH=/usr/bin:/bin、
			// TMPDIR=home）在 Windows 上会让子进程完全丢失 TEMP/TMP，于是 Go 的
			// t.TempDir() 回退到 C:\WINDOWS 并因权限被拒 —— 那不是被测行为，是夹具缺陷。
			// 注意：这里必须**整体替换** Env（不能追加），否则子进程会继承父测试的
			// 全局状态；但两组变量都要给全，覆盖各自平台的必需项。
			childEnv := []string{
				"HOME=" + home, "TZ=UTC", "GOMAXPROCS=2", "B01_OBSERVER_CHILD=" + vector,
			}
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
			command.Env = childEnv
			if dir := os.Getenv("B01_NATIVE_LEDGER_DIR"); dir != "" {
				if !filepath.IsAbs(dir) {
					t.Fatal("SETUP_INCONCLUSIVE: ledger directory must be absolute")
				}
				command.Env = append(command.Env, "B01_NATIVE_LEDGER_DIR="+dir)
			}
			output, err := command.CombinedOutput()
			t.Logf("B01_OWNED_CHILD vector=%s\n%s", vector, output)
			if err != nil {
				t.Errorf("real isolated HTTP vector failed: %s, fixtureProcessDeadline=%t", vector, ctx.Err() != nil)
			}
		})
	}
}

func b01RunDetachVector(t *testing.T, vector string) {
	ledger := b01DetachLedger{Vector: vector}
	check := func(label string, passed bool) {
		ledger.Checks = append(ledger.Checks, b01DetachCheck{Label: label, Qualified: true, Passed: passed})
		if !passed {
			t.Errorf("B01_CONTRACT_RED: %s", label)
		}
	}
	if dao.Log != nil || (manager.GetManager() != nil && manager.GetManager().GetDB() != nil) || searchFlights.inFlight() != 0 {
		t.Fatal("SETUP_INCONCLUSIVE: fresh child must not inherit a database or active flight")
	}
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "synthetic.db")), &gorm.Config{})
	if err != nil {
		t.Fatal("SETUP_INCONCLUSIVE: owned SQLite open")
	}
	if err = db.AutoMigrate(&entity.SearchAccountConfig{}, &entity.AISearchConfig{}, &entity.AIModelPrice{}, &entity.SystemSetting{}, &entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.Log{}, &entity.Tiku{}, &entity.IdempotencyRecord{}); err != nil {
		t.Fatal("SETUP_INCONCLUSIVE: owned synthetic schema")
	}
	dao.SetDefault(db) // Only this fresh child owns this newly created handle.
	actor := entity.User{Username: "synthetic-b01-observer", Password: "synthetic-unused-password", Role: "user", Status: "enabled", Balance: 12.5, RegisterSource: "public"}
	if db.Create(&actor).Error != nil {
		t.Fatal("SETUP_INCONCLUSIVE: synthetic user")
	}
	plainToken := "synthetic-b01-not-a-real-credential"
	tokenHash := sha256.Sum256([]byte(plainToken))
	token := entity.AccessToken{UserID: actor.ID, Name: "synthetic-b01", TokenHash: hex.EncodeToString(tokenHash[:]), TokenPrefix: "synthetic", TokenValue: plainToken, Status: "enabled"}
	provider := entity.SearchAccountConfig{Provider: "icodef", TokenKey: "icodefToken", TokenValue: "synthetic-not-a-provider-credential", Enabled: true, FlatPrice: 2.5}
	if db.Create(&token).Error != nil || db.Create(&provider).Error != nil {
		t.Fatal("SETUP_INCONCLUSIVE: synthetic token/provider fixture")
	}
	oldResolver, oldLocal, oldBindings, oldAI, oldReporter := resolveSearchRequestContext, defaultDBSearchProvider, buildSearchClientBindings, searchAIAnswer, searchBillingReporter
	searchResultCache.clear()
	searchFlights.reset()
	var work, completed, reports, unexpected atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWork := func() { releaseOnce.Do(func() { close(release) }) }
	var handlers sync.WaitGroup
	calls := []*b01DetachCall{}
	stops := []context.CancelFunc{}
	// Expected early-detach failures never Fatal before release/drain/tails.
	defer func() {
		for _, stop := range stops {
			stop()
		}
		releaseWork()
		handlers.Wait()
		// Matches old Search and new executeSearch/owner goroutines by actual
		// production source frames, not the original HTTP goroutine identity.
		ledger.OwnerDrained = b01DetachUntil(func() bool {
			return searchFlights.inFlight() == 0 && work.Load() == completed.Load() && b01DetachStacks("production") == 0
		})
		check("all real production owners drain before globals/pool teardown", ledger.OwnerDrained)
		ledger.Calls, ledger.Completions, ledger.Reports, ledger.Unexpected, ledger.FlightCount = work.Load(), completed.Load(), reports.Load(), unexpected.Load(), searchFlights.inFlight()
		if ledger.OwnerDrained {
			resolveSearchRequestContext, defaultDBSearchProvider, buildSearchClientBindings, searchAIAnswer, searchBillingReporter = oldResolver, oldLocal, oldBindings, oldAI, oldReporter
			searchResultCache.clear()
			searchFlights.reset()
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		// No new tracker symbol is linked into this baseline-compatible file.
		// If owner drain failed, never restore its globals/close its pool; the
		// failed isolated child ends, without exposing handles to other tests.
		data, _ := json.Marshal(ledger)
		t.Logf("B01_NATIVE_LEDGER %s", data)
		if dir := os.Getenv("B01_NATIVE_LEDGER_DIR"); dir != "" {
			file, err := os.OpenFile(filepath.Join(dir, vector+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Error("owned fresh ledger cannot be created")
			} else {
				_, _ = file.Write(append(data, '\n'))
				_ = file.Close()
			}
		}
	}()
	resolveSearchRequestContext = func(context.Context, string, string, url.Values) (resolvedSearchRequestContext, error) {
		return resolvedSearchRequestContext{SearchMode: searchModeNormal, UserID: actor.ID, AccessTokenID: token.ID, UsedDefaultSearch: true, UsedDefaultSearchProviders: []string{"icodef"}, Tokens: map[string]string{"icodef": provider.TokenValue}, RequestID: "synthetic-b01", AccessTokenCtx: &service.AccessTokenAuthContext{User: actor, AccessToken: token}}, nil
	}
	defaultDBSearchProvider = func() func(model.SearchRequest) ([][]string, error) {
		return func(model.SearchRequest) ([][]string, error) {
			unexpected.Add(1)
			return nil, errors.New("synthetic fixture denies local DB search")
		}
	}
	searchAIAnswer = func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
		unexpected.Add(1)
		return nil, searchpkg.AISearchUsage{}, errors.New("synthetic fixture denies models")
	}
	buildSearchClientBindings = func(resolvedSearchRequestContext, string) []searchClientBinding {
		return []searchClientBinding{{Provider: "icodef", Search: func(model.SearchRequest) ([][]string, error) {
			work.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			completed.Add(1)
			return [][]string{{b01DetachSyntheticAnswer}}, nil
		}}}
	}
	searchBillingReporter = func(searchBillingContext) { reports.Add(1) }
	launch := func(ctx context.Context, keyed bool) *b01DetachCall {
		call := &b01DetachCall{done: make(chan struct{})}
		calls = append(calls, call)
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer close(call.done)
			defer func() {
				if recover() != nil {
					call.panic = true
				}
			}()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			// The synthetic provider answer must be an actual option: strict
			// objective matching correctly rejects an unrelated string.
			body := `{"question":"synthetic-` + vector + `","options":["` + b01DetachSyntheticAnswer + `","synthetic-distractor"]}`
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", strings.NewReader(body)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+plainToken)
			if keyed {
				request.Header.Set("Idempotency-Key", "synthetic-stable-key")
			}
			c.Request = request
			SearchWithIdempotency(c) // Real production entry; never a fake wait wrapper.
			call.status, call.body = recorder.Code, append([]byte(nil), recorder.Body.Bytes()...)
			call.decode = json.Unmarshal(call.body, &call.response)
		}()
		return call
	}
	newOwn := func() (context.Context, context.CancelFunc) {
		var ctx context.Context
		var stop context.CancelFunc
		if strings.HasSuffix(vector, "own_deadline") {
			ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(time.Second))
		} else {
			ctx, stop = context.WithCancel(context.Background())
		}
		stops = append(stops, stop)
		return ctx, stop
	}
	stage := func(label string, predicate func() bool) bool {
		if b01DetachUntil(predicate) {
			check(label, true)
			return true
		}
		ledger.SetupIssue = label
		t.Errorf("SETUP_INCONCLUSIVE: %s", label)
		return false
	}
	own, stopOwn := newOwn()
	keyed := strings.HasPrefix(vector, "keyed_")
	leaderOwned := strings.HasPrefix(vector, "leader_") || keyed
	leaderContext := context.Background()
	if leaderOwned {
		leaderContext = own
	}
	leader := launch(leaderContext, keyed)
	if !stage("real original/new producer entered channel-only work and registered flight", func() bool { return work.Load() == 1 && searchFlights.inFlight() == 1 }) {
		return
	}
	select {
	case <-entered:
	default:
		ledger.SetupIssue = "missing actual provider entry"
		t.Error("SETUP_INCONCLUSIVE: provider handshake")
		return
	}
	var retiring, processing, live *b01DetachCall
	var processingOwn context.Context
	var stopProcessing context.CancelFunc
	if !leaderOwned {
		retiring = launch(own, false)
		if !stage("real keyless follower occupies production sharedwait before own event", func() bool { return b01DetachStacks("shared") == 1 }) {
			return
		}
	}
	if keyed {
		if !stage("actual newly claimed idempotency record processing before duplicate observer", func() bool {
			var row entity.IdempotencyRecord
			return db.Where("operation = ?", "search").First(&row).Error == nil && row.Status == service.IdempotencyStatusProcessing
		}) {
			return
		}
		processingOwn, stopProcessing = newOwn()
		processing = launch(processingOwn, true)
		if !stage("same-key observer reached actual processing wait without second producer", func() bool { return b01DetachStacks("processing") == 1 && work.Load() == 1 }) {
			return
		}
	}
	live = launch(context.Background(), false)
	wantWaits := 1
	if !leaderOwned {
		wantWaits = 2
	}
	if !stage("independent live observer joined actual sharedwait, not fake HTTP wait", func() bool { return b01DetachStacks("shared") == wantWaits }) {
		return
	}
	if own.Err() != nil || (processingOwn != nil && processingOwn.Err() != nil) {
		ledger.SetupIssue = "context expired before actual joins"
		t.Error("SETUP_INCONCLUSIVE: pre-admission deadline")
		return
	}
	if strings.HasSuffix(vector, "own_cancel") {
		stopOwn()
		if stopProcessing != nil {
			stopProcessing()
		}
	}
	wantErr := context.Canceled
	if strings.HasSuffix(vector, "own_deadline") {
		wantErr = context.DeadlineExceeded
	}
	check("own original HTTP context reaches intended cancel/deadline", b01DetachUntil(func() bool { return errors.Is(own.Err(), wantErr) }))
	if processingOwn != nil {
		check("processing observer has independent own context event", b01DetachUntil(func() bool { return errors.Is(processingOwn.Err(), wantErr) }))
	}
	retired := retiring
	if leaderOwned {
		retired = leader
	}
	check("retired actual HTTP caller returns while shared work remains held", b01DetachReturned(retired, b01DetachGuard))
	if processing != nil {
		check("existing processing HTTP observer independently returns", b01DetachReturned(processing, b01DetachGuard))
	}
	if !leaderOwned {
		check("retiring follower leaves real sharedwait rather than leaking its worker", b01DetachUntil(func() bool { return b01DetachStacks("shared") == 1 }))
	}
	check("shared producer still single and unfinished; live observer not canceled", work.Load() == 1 && completed.Load() == 0 && reports.Load() == 0 && searchFlights.inFlight() == 1 && !b01DetachReturned(live, 0))
	if keyed {
		var row entity.IdempotencyRecord
		check("observer detach never persists empty successful idempotency result", db.Where("operation = ?", "search").First(&row).Error == nil && row.Status == service.IdempotencyStatusProcessing && row.ResponseJSON == "")
	}
	// All expected detach REDs above are Error, not Fatal: complete every tail.
	releaseWork()
	check("live real HTTP observer receives executed shared answer", b01DetachReturned(live, b01DetachGuard) && b01DetachAnswer(live))
	if !leaderOwned {
		check("stable real leader receives executed answer", b01DetachReturned(leader, b01DetachGuard) && b01DetachAnswer(leader))
	}
	for _, call := range calls {
		check("all launched HTTP handlers return without panic after release", b01DetachReturned(call, b01DetachGuard) && !call.panic)
	}
	check("all production work/body owners complete before tail assertions", b01DetachUntil(func() bool {
		return completed.Load() == 1 && searchFlights.inFlight() == 0 && b01DetachStacks("production") == 0
	}))
	var storedUser entity.User
	var storedToken entity.AccessToken
	_ = db.Model(&entity.BillingRecord{}).Where("user_id = ?", actor.ID).Count(&ledger.BillRows).Error
	ledger.WalletOnce = db.First(&storedUser, actor.ID).Error == nil && storedUser.Balance == 10
	ledger.QuotaOnce = db.First(&storedToken, token.ID).Error == nil && storedToken.TotalAmountUsed == 2.5
	check("one actual synthetic bill and unchanged-price wallet/quota charge", ledger.BillRows == 1 && ledger.WalletOnce && ledger.QuotaOnce)
	if keyed {
		var row entity.IdempotencyRecord
		ok := db.Where("operation = ?", "search").First(&row).Error == nil && row.Status == service.IdempotencyStatusSucceeded && row.HTTPStatus == http.StatusOK && row.ResponseJSON != ""
		var response model.SearchResponse
		ok = ok && json.Unmarshal([]byte(row.ResponseJSON), &response) == nil && response.Answer.AnswerText != ""
		check("admitted producer persists complete nonempty business result, not empty recorder200", ok)
		replay := launch(context.Background(), true)
		check("fresh same-key paid replay is byte-exact stored response", b01DetachReturned(replay, b01DetachGuard) && b01DetachAnswer(replay) && replay.status == row.HTTPStatus && string(replay.body) == row.ResponseJSON)
	}
	retrieval := launch(context.Background(), false)
	check("same real no-header entry retrieves cache with full answer", b01DetachReturned(retrieval, b01DetachGuard) && b01DetachAnswer(retrieval))
	var finalBills int64
	_ = db.Model(&entity.BillingRecord{}).Where("user_id = ?", actor.ID).Count(&finalBills).Error
	check("detached/replay/cache paths never create new upstream/report/bill", work.Load() == 1 && completed.Load() == 1 && reports.Load() == 1 && finalBills == 1 && unexpected.Load() == 0)
	ledger.CompletedFlow = true
}

func b01DetachReturned(call *b01DetachCall, guard time.Duration) bool {
	if guard == 0 {
		select {
		case <-call.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(guard)
	defer timer.Stop()
	select {
	case <-call.done:
		return true
	case <-timer.C:
		return false
	}
}

func b01DetachAnswer(call *b01DetachCall) bool {
	// Empty httptest default200 after a canceled observer is NOT business success.
	return !call.panic && call.status == http.StatusOK && call.decode == nil &&
		call.response.Answer.AnswerText == b01DetachSyntheticAnswer &&
		len(call.response.Answer.AnswerIndex) == 1 && call.response.Answer.AnswerIndex[0] == 0
}

func b01DetachUntil(predicate func() bool) bool {
	deadline := time.NewTimer(b01DetachGuard)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if predicate() {
			return true
		}
		select {
		case <-deadline.C:
			return predicate()
		case <-ticker.C:
		}
	}
}

func b01DetachStacks(kind string) int {
	// Each child exclusively owns one synthetic key/flight and launches requests
	// serially through real SearchWithIdempotency. New worker-GID need not equal
	// HTTP-GID. Count actual production body/sharedwait frames, never wrapper waits.
	for size := 64 << 10; size <= 2<<20; size *= 2 {
		buffer := make([]byte, size)
		n := runtime.Stack(buffer, true)
		if n == len(buffer) {
			continue
		}
		count := 0
		for _, stack := range strings.Split(string(buffer[:n]), "\n\n") {
			body := strings.Contains(stack, ".Search(") || strings.Contains(stack, ".executeSearch(") || strings.Contains(stack, ".searchWithObserverContext(")
			shared := strings.Contains(stack, ".waitSearchFlightResult") || (strings.Contains(stack, ".beginSearchFlight") && strings.Contains(stack, ".waitContext("))
			switch kind {
			case "shared":
				if body && shared && strings.Contains(stack, "/internal/controller/search") {
					count++
				}
			case "processing":
				if strings.Contains(stack, ".waitForIdempotentSearchResult(") {
					count++
				}
			case "production":
				if strings.Contains(stack, "/internal/controller/search") {
					count++
				}
			}
		}
		return count
	}
	return -1 // Saturation is inconclusive, never a fake zero or qualification.
}
