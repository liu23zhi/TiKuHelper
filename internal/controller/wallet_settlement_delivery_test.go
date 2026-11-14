package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func walletSettlementAssertState(t *testing.T, db *gorm.DB, userID, tokenID int32, count int64, balance, used float64) {
	t.Helper()
	var user entity.User
	var token entity.AccessToken
	var rows int64
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&token, tokenID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ?", userID).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != count || user.Balance != balance || token.TotalAmountUsed != used {
		t.Fatalf("WALLET_SETTLEMENT_STATE: rows=%d wallet=%.6f used=%.6f want=%d/%.6f/%.6f", rows, user.Balance, token.TotalAmountUsed, count, balance, used)
	}
}

// The mutable provider/store globals stay in one owned, environment-cleared child,
// reusing the same isolation pattern as the existing D03 real async billing test.
func TestWalletSettlementAdmittedFallbackTasksDeliverAndReplayOnce(t *testing.T) {
	if os.Getenv("WALLET_SETTLEMENT_HANDLER_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestWalletSettlementAdmittedFallbackTasksDeliverAndReplayOnce$", "-test.v", "-test.timeout=35s")
		cmd.Dir = t.TempDir()
		// ⚠️ 整体替换 Env，所以平台必需项要给全。
		//
		// Windows 的 GetTempPath 只认 TMP → TEMP → USERPROFILE，**不看 TMPDIR**
		// （那是 POSIX 的名字）。只给 TMPDIR 时子进程的 t.TempDir() 会退化成
		// `mkdir C:\WINDOWS\...: Access is denied`，那不是被测行为而是夹具缺陷。
		// 实测：只设 TMPDIR 时 os.TempDir() = "C:\Users\<user>"。
		childHome := t.TempDir()
		childEnv := []string{"WALLET_SETTLEMENT_HANDLER_CHILD=1", "TIKU_RUN_PROVIDER_NETWORK_TESTS=0", "GOMAXPROCS=2", "HOME=" + childHome}
		if runtime.GOOS == "windows" {
			childEnv = append(childEnv,
				"TEMP="+childHome, "TMP="+childHome,
				"PATH="+os.Getenv("PATH"),
				"SystemRoot="+os.Getenv("SystemRoot"),
				"USERPROFILE="+childHome,
			)
		} else {
			childEnv = append(childEnv, "TMPDIR="+childHome)
		}
		cmd.Env = childEnv
		var out, errout bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errout
		err = cmd.Run()
		t.Logf("WALLET_HANDLER_CHILD_STDOUT_BEGIN\n%sWALLET_HANDLER_CHILD_STDOUT_END", out.String())
		t.Logf("WALLET_HANDLER_CHILD_STDERR_BEGIN\n%sWALLET_HANDLER_CHILD_STDERR_END", errout.String())
		if err != nil || ctx.Err() != nil {
			t.Fatalf("wallet handler child: run=%v context=%v", err, ctx.Err())
		}
		if !strings.Contains(out.String(), "WALLET_SETTLEMENT_HANDLER_EVIDENCE") {
			t.Fatal("wallet handler child did not reach its actual financial assertion")
		}
		return
	}
	if manager.GetManager() != nil {
		t.Fatal("WALLET_HANDLER_INFRA: refuse initialized manager or real configuration")
	}
	db, user, key := integrationQuotaFixture(t, .1, .020001, .01)
	seedControllerAIModelPrice(t, db, "wallet_settlement_model", .030003, 0, 0)
	staged := false
	if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &staged, nil); err != nil {
		t.Fatal(err)
	}
	originalStore, originalRun := defaultAIFallbackTaskStore, runAIFallbackService
	store := newAIFallbackTaskStore(time.Minute, time.Now)
	defaultAIFallbackTaskStore = store
	t.Cleanup(func() { defaultAIFallbackTaskStore = originalStore; runAIFallbackService = originalRun })
	fallbackResultCache.clear()
	fallbackFlights.reset()
	entered := make(chan string, 4)
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var onceA, onceB sync.Once
	release := func(q string) {
		if q == "wallet_A" {
			onceA.Do(func() { close(releaseA) })
		} else {
			onceB.Do(func() { close(releaseB) })
		}
	}
	joined := false
	defer func() {
		release("wallet_A")
		release("wallet_B")
		if !joined {
			fmt.Fprintln(os.Stderr, "WALLET_HANDLER_INFRA_UNJOINED: stopping child before synthetic database cleanup")
			os.Exit(32)
		}
	}()
	var calls atomic.Int32
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		calls.Add(1)
		select {
		case entered <- req.Question:
		default:
		}
		var ready <-chan struct{}
		if req.Question == "wallet_A" {
			ready = releaseA
		} else if req.Question == "wallet_B" {
			ready = releaseB
		}
		if ready != nil {
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				return service.AIFallbackResult{FailureDetail: "WALLET_HANDLER_INFRA provider release deadline"}
			}
		}
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Question: req.Question, Answer: "wallet-answer-" + req.Question}, Model: "wallet_settlement_model", Usage: searchpkg.AISearchUsage{InputTokens: 1000000, TotalTokens: 1000000}}
	}
	router := gin.New()
	router.POST("/api/ai-fallback/tasks", AuthOrAccessToken, CreateAIFallbackTask)
	router.GET("/api/ai-fallback/status", AuthOrAccessToken, GetAIFallbackTaskStatus)
	submit := func(q, idem string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"question":%q,"type":4}`, q)
		req := httptest.NewRequest(http.MethodPost, "/api/ai-fallback/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key.TokenValue)
		req.Header.Set("Idempotency-Key", idem)
		router.ServeHTTP(w, req)
		return w
	}
	waitEntered := func(want string) {
		t.Helper()
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("WALLET_HANDLER_INFRA entered=%q want%q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WALLET_HANDLER_INFRA actual provider entry not reached")
		}
	}
	a := submit("wallet_A", "wallet-paid-A")
	var ca, cb aiFallbackTaskResponse
	if a.Code != http.StatusAccepted || json.Unmarshal(a.Body.Bytes(), &ca) != nil || ca.TaskID == "" {
		t.Fatalf("WALLET_HANDLER_INFRA first create: %d %s", a.Code, a.Body.String())
	}
	waitEntered("wallet_A")
	b := submit("wallet_B", "wallet-paid-B")
	if b.Code != http.StatusAccepted || json.Unmarshal(b.Body.Bytes(), &cb) != nil || cb.TaskID == "" || cb.TaskID == ca.TaskID {
		t.Fatalf("WALLET_HANDLER_INFRA second create: %d %s", b.Code, b.Body.String())
	}
	waitEntered("wallet_B")
	if calls.Load() != 2 {
		t.Fatalf("WALLET_HANDLER_INFRA provider entries=%d", calls.Load())
	}
	walletSettlementAssertState(t, db, user.ID, key.ID, 0, .01, .020001)
	await := func(id string) aiFallbackTask {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			task, err := store.get(id, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != aiFallbackTaskStatusRunning {
				return task
			}
			if time.Now().After(deadline) {
				t.Fatal("WALLET_HANDLER_INFRA task completion deadline")
			}
			time.Sleep(time.Millisecond)
		}
	}
	release("wallet_A")
	first := await(ca.TaskID)
	var firstUser entity.User
	if err := db.First(&firstUser, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	release("wallet_B")
	second := await(cb.TaskID)
	joined = true
	// Both models had already entered while wallet was positive. The second only
	// returns after the first actual charge has taken that wallet below zero.
	if first.Status != aiFallbackTaskStatusSucceeded || second.Status != aiFallbackTaskStatusSucceeded || !first.Result.Success || !second.Result.Success || first.Result.Result == nil || second.Result.Result == nil || first.Result.Result.Answer != "wallet-answer-wallet_A" || second.Result.Result.Answer != "wallet-answer-wallet_B" {
		t.Fatalf("WALLET_SETTLEMENT_DELIVERY: admitted called tasks lost answers: first=%+v second=%+v", first, second)
	}
	if firstUser.Balance != -.020003 {
		t.Fatalf("WALLET_SETTLEMENT_DELIVERY: first actual charge wallet=%.6f", firstUser.Balance)
	}
	walletSettlementAssertState(t, db, user.ID, key.ID, 2, -.050006, .080007)
	var rows []entity.BillingRecord
	if err := db.Where("user_id = ?", user.ID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RequestID == "" || rows[0].RequestID == rows[1].RequestID || rows[0].Amount != .030003 || rows[1].Amount != .030003 || rows[0].BalanceBefore != .01 || rows[0].BalanceAfter != -.020003 || rows[1].BalanceBefore != -.020003 || rows[1].BalanceAfter != -.050006 {
		t.Fatalf("WALLET_SETTLEMENT_DELIVERY: distinct completed charge chain lost: %+v", rows)
	}
	next := submit("wallet_C", "wallet-new-C")
	if strings.Contains(next.Body.String(), "taskId") || !strings.Contains(next.Body.String(), "INSUFFICIENT_BALANCE") || calls.Load() != 2 {
		t.Fatalf("WALLET_NEW_EXECUTION: negative wallet started another provider: %d %s calls=%d", next.Code, next.Body.String(), calls.Load())
	}
	replay := submit("wallet_A", "wallet-paid-A")
	var replayed aiFallbackTaskResponse
	if replay.Code != http.StatusAccepted || json.Unmarshal(replay.Body.Bytes(), &replayed) != nil || replayed.TaskID != ca.TaskID {
		t.Fatalf("WALLET_PAID_REPLAY: original paid result lost: %d %s", replay.Code, replay.Body.String())
	}
	for _, item := range []struct{ id, answer string }{{ca.TaskID, "wallet-answer-wallet_A"}, {cb.TaskID, "wallet-answer-wallet_B"}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ai-fallback/status?taskId="+item.id, nil)
		req.Header.Set("Authorization", "Bearer "+key.TokenValue)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), item.answer) {
			t.Fatalf("WALLET_PAID_STATUS: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("WALLET_PAID_REPLAY: duplicate provider calls=%d", calls.Load())
	}
	walletSettlementAssertState(t, db, user.ID, key.ID, 2, -.050006, .080007)
	evidence := map[string]any{"bothProviderEntriesBeforeFirstRelease": true, "providerCalls": calls.Load(), "firstWallet": firstUser.Balance, "finalWallet": -.050006, "finalUsed": .080007, "ledgerRows": 2, "chargeEach": .030003, "nextNegativeWalletRequestRejected": true, "samePaidTaskReplayedWithoutCallOrCharge": true, "ownerStatusRetrievalAtNegativeWallet": true, "syntheticSQLiteOnly": true, "actualExternalProviderCalled": false}
	raw, _ := json.Marshal(evidence)
	fmt.Printf("WALLET_SETTLEMENT_HANDLER_EVIDENCE %s\n", raw)
}
