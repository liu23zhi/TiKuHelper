package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

type d03FollowerJoin struct {
	key    string
	shared *flightCall[fallbackFlightPayload]
}

type d03RejectNetwork struct{ attempts *atomic.Int32 }

func (r d03RejectNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	r.attempts.Add(1)
	return nil, errors.New("D03 synthetic fixture forbids provider network")
}

// This test runs all mutable package globals in a fresh test-binary child.
// Only runAIFallbackService is replaced; handler, task, flight and billing stay real.
func TestD03FallbackSharedFlightBillsOnce(t *testing.T) {
	if os.Getenv("D03_FOLLOWER_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestD03FallbackSharedFlightBillsOnce$", "-test.v", "-test.timeout=25s")
		cmd.Dir = t.TempDir()
		// ⚠️ 这里是**整体替换** Env（不是追加），所以必须把平台必需项给全。
		//
		// Windows 的 GetTempPath 按 TMP → TEMP → USERPROFILE 顺序取临时目录，
		// **完全不看 TMPDIR**（TMPDIR 是 POSIX 的名字）。只给 TMPDIR 时子进程的
		// os.TempDir() 退化成 C:\Users\<user> 甚至 C:\WINDOWS，于是 t.TempDir()
		// 报 mkdir C:\WINDOWS\...: Access is denied —— 那不是被测行为，是夹具缺陷。
		// 实测：只设 TMPDIR 时 os.TempDir() = "C:\Users\Zelly"，TMPDIR 被完全忽略。
		childHome := t.TempDir()
		childEnv := []string{
			"D03_FOLLOWER_CHILD=1", "TIKU_RUN_PROVIDER_NETWORK_TESTS=0", "HOME=" + childHome,
		}
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
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		t.Logf("D03_CHILD_STDOUT_BEGIN\n%sD03_CHILD_STDOUT_END", stdout.String())
		t.Logf("D03_CHILD_STDERR_BEGIN\n%sD03_CHILD_STDERR_END", stderr.String())
		if ctx.Err() != nil {
			t.Fatalf("child deadline: %v", ctx.Err())
		}
		if err != nil {
			t.Fatalf("isolated child failed: %v", err)
		}
		return
	}
	gin.SetMode(gin.TestMode)
	// A fresh child must never initialize manager/config/real DB. Cache's UAPI
	// key check returns before DB access while manager is nil.
	if manager.GetManager() != nil {
		t.Fatal("fresh child unexpectedly has manager; refuse any real config/DB")
	}
	if observeFallbackFlightFollower != nil {
		t.Fatal("production follower observer is not nil by default")
	}
	var networkAttempts atomic.Int32
	http.DefaultTransport = d03RejectNetwork{attempts: &networkAttempts}
	http.DefaultClient = &http.Client{Transport: http.DefaultTransport}
	syntheticPath := filepath.Join(t.TempDir(), "d03-synthetic.sqlite")
	db, err := gorm.Open(sqlite.Open(syntheticPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open synthetic SQLite: %v", err)
	}
	syntheticUnderlyingDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	syntheticUnderlyingDB.SetMaxOpenConns(1)
	defer syntheticUnderlyingDB.Close()
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.AIModelPrice{}, &entity.BillingRecord{}, &entity.Log{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	dao.SetDefault(db)
	resolvedDB := resolveSearchPersistenceDB()
	if resolvedDB == nil || dao.Log == nil {
		t.Fatal("synthetic DAO binding missing")
	}
	resolvedUnderlyingDB, err := resolvedDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	daoUnderlyingDB, err := dao.Log.UnderlyingDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	if resolvedUnderlyingDB != syntheticUnderlyingDB || daoUnderlyingDB != syntheticUnderlyingDB {
		t.Fatal("real persistence resolver escaped syntheticUnderlyingDB")
	}
	var databases []struct {
		Seq  int
		Name string
		File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
		t.Fatal(err)
	}
	if len(databases) != 1 || databases[0].Name != "main" || databases[0].File != syntheticPath {
		t.Fatalf("not exact synthetic SQLite: %+v", databases)
	}
	user := entity.User{ID: 2, Username: "d03_synthetic_owner", Password: "synthetic-not-a-secret", Role: "user", Status: "enabled", Balance: 100, RegisterSource: "public"}
	token := entity.AccessToken{ID: 1, UserID: 2, Name: "d03_synthetic_token", TokenHash: strings.Repeat("d", 64), TokenPrefix: "synthetic", TokenValue: "synthetic-not-a-real-token", Status: "enabled", TotalQuotaLimit: 0, TotalAmountUsed: 0}
	price := entity.AIModelPrice{ModelName: "d03-fallback-integration-priced-model", DisplayName: "D03 synthetic priced model", InputPrice: 5, CachedPrice: 0.5, CacheWritePrice: 0, OutputPrice: 30, Multiplier: 1, Status: "enabled"}
	for _, row := range []any{&user, &token, &price} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	usage := searchpkg.AISearchUsage{InputTokens: 1000000, CachedInputTokens: 400000, CacheWriteTokens: 0, OutputTokens: 200000, TotalTokens: 1200000}
	req := model.SearchRequest{Question: "D03 isolated fallback shared question", Type: 2, Options: []string{"A", "B"}}
	quote, err := resolveCompletedAIBillingCharge(db, resolvedSearchRequestContext{AIConfig: service.AISearchRequestConfig{Model: price.ModelName}}, req, usage)
	if err != nil || quote.UnpricedModel || math.Abs(quote.Charge.Amount-9.2) > 0.0000001 {
		t.Fatalf("real quote must be priced 9.2: %+v err=%v", quote, err)
	}
	readRows := func(table string) []map[string]any {
		var rows []map[string]any
		if err := db.Table(table).Find(&rows).Error; err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		return rows
	}
	before := map[string]any{"users": readRows("users"), "access_tokens": readRows("access_tokens"), "billing_records": readRows("billing_records")}
	if len(before["billing_records"].([]map[string]any)) != 0 {
		t.Fatal("fresh DB has ledger")
	}
	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	fallbackResultCache = newResultCache[fallbackCachedValue](time.Minute, time.Now)
	fallbackFlights = newFlightGroup[fallbackFlightPayload]()
	flightKey := buildFallbackFlightKey(user.ID, req)
	providerEntered := make(chan d03FollowerJoin, 2)
	followerJoined := make(chan d03FollowerJoin, 2)
	releaseProvider := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	defer release()
	var providerCalls, joins atomic.Int32
	observeFallbackFlightFollower = func(key string, shared *flightCall[fallbackFlightPayload]) {
		joins.Add(1)
		followerJoined <- d03FollowerJoin{key: key, shared: shared}
	}
	runAIFallbackService = func(input model.SearchRequest) service.AIFallbackResult {
		question, questionType, options := input.Question, input.Type, input.Options
		providerCalls.Add(1)
		if question != req.Question || questionType != req.Type || !reflect.DeepEqual(options, req.Options) {
			return service.AIFallbackResult{FailureDetail: "unexpected synthetic provider input"}
		}
		fallbackFlights.mu.Lock()
		call := fallbackFlights.calls[flightKey]
		fallbackFlights.mu.Unlock()
		providerEntered <- d03FollowerJoin{key: flightKey, shared: call}
		select {
		case <-releaseProvider:
		case <-time.After(8 * time.Second):
			return service.AIFallbackResult{FailureDetail: "synthetic provider release deadline"}
		}
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Question: question, Answer: "D03 synthetic answer"}, Model: price.ModelName, Usage: usage}
	}
	create := func() aiFallbackTaskResponse {
		body, err := json.Marshal(map[string]any{"question": req.Question, "type": req.Type, "options": req.Options, "skipCache": false})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("user", &user)
		c.Set("access_token", &token)
		c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.RemoteAddr = "192.0.2.3:40000"
		CreateAIFallbackTask(c)
		var response aiFallbackTaskResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusAccepted || response.TaskID == "" || response.Status != aiFallbackTaskStatusRunning {
			t.Fatalf("real create failed: code=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return response
	}
	leader := create()
	var started d03FollowerJoin
	select {
	case started = <-providerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never entered local blocked provider")
	}
	if started.shared == nil || started.key != flightKey {
		t.Fatal("leader identity unavailable")
	}
	follower := create()
	if leader.TaskID == follower.TaskID {
		t.Fatal("real handler returned same task ID twice")
	}
	var ack d03FollowerJoin
	select {
	case ack = <-followerJoined:
	case <-time.After(5 * time.Second):
		t.Fatal("coverage gap: no actual follower JOIN_ACK; do not infer a billing bug")
	}
	if ack.key != flightKey || ack.shared == nil || ack.shared != started.shared {
		t.Fatal("JOIN_ACK is not exact leader shared identity/key")
	}
	select {
	case <-ack.shared.done:
		t.Fatal("flight finished before follower ACK barrier")
	default:
	}
	if joins.Load() != 1 || providerCalls.Load() != 1 {
		t.Fatalf("not one real follower before release: joins=%d calls=%d", joins.Load(), providerCalls.Load())
	}
	for _, id := range []string{leader.TaskID, follower.TaskID} {
		task, err := defaultAIFallbackTaskStore.get(id, user.ID)
		if err != nil || task.Status != aiFallbackTaskStatusRunning || task.UserID != user.ID {
			t.Fatalf("both real tasks must be running before release: %+v %v", task, err)
		}
	}
	var ledgerBeforeRelease int64
	if err := db.Model(&entity.BillingRecord{}).Count(&ledgerBeforeRelease).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerBeforeRelease != 0 {
		t.Fatal("billing happened before provider release")
	}
	release()
	// This ticker observes real stored terminal state only. Follower overlap was
	// already proved by JOIN_ACK above; elapsed time is never the join oracle.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	completed := make(map[string]aiFallbackTask)
	for len(completed) != 2 {
		for _, id := range []string{leader.TaskID, follower.TaskID} {
			task, err := defaultAIFallbackTaskStore.get(id, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != aiFallbackTaskStatusRunning {
				if task.Status != aiFallbackTaskStatusSucceeded || !task.Result.Success {
					t.Fatalf("real async task failed: %+v", task)
				}
				completed[id] = task
			}
		}
		if len(completed) == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("unjoined real async task at completion deadline")
		}
	}
	leaderResult, followerResult := completed[leader.TaskID].Result, completed[follower.TaskID].Result
	if !reflect.DeepEqual(leaderResult, followerResult) || leaderResult.Result == nil || leaderResult.Result.Answer != "D03 synthetic answer" || leaderResult.Model != price.ModelName || !reflect.DeepEqual(leaderResult.Usage, usage) {
		t.Fatalf("real task results differ or hidden fields lost: leader=%+v follower=%+v", leaderResult, followerResult)
	}
	for _, id := range []string{leader.TaskID, follower.TaskID} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("user", &user)
		c.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+id, nil)
		GetAIFallbackTaskStatus(c)
		var response aiFallbackTaskResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !response.Success || response.Status != aiFallbackTaskStatusSucceeded || response.TaskID != id || !reflect.DeepEqual(response.Result, leaderResult.Result) {
			t.Fatalf("real status handler mismatch: %s", recorder.Body.String())
		}
	}
	var afterUser entity.User
	var afterToken entity.AccessToken
	var ledger []entity.BillingRecord
	var audits []entity.Log
	for _, query := range []struct {
		dest  any
		table string
	}{{&afterUser, "users"}, {&afterToken, "access_tokens"}, {&ledger, "billing_records"}, {&audits, "logs"}} {
		if err := db.Table(query.table).Find(query.dest).Error; err != nil {
			t.Fatal(err)
		}
	}
	if afterUser.ID != 2 || afterToken.ID != 1 || afterToken.UserID != 2 || math.Abs(afterUser.Balance-90.8) > 0.0000001 || math.Abs(afterToken.TotalAmountUsed-9.2) > 0.0000001 {
		t.Fatalf("actual wallet/token mismatch: user=%+v token=%+v", afterUser, afterToken)
	}
	if len(ledger) != 1 {
		t.Fatalf("actual ledger count=%d, want one", len(ledger))
	}
	row := ledger[0]
	if row.ID != 1 || row.UserID != 2 || row.AccessTokenID != 1 || row.Type != searchBillingRecordTypeNormal || !row.UsedDefaultAccount || row.Provider != "ai" || row.ModelName != price.ModelName || row.InputTokens != usage.InputTokens || row.CachedInputTokens != usage.CachedInputTokens || row.OutputTokens != usage.OutputTokens || row.TotalTokens != usage.TotalTokens || math.Abs(row.Amount-9.2) > 0.0000001 || math.Abs(row.BalanceBefore-100) > 0.0000001 || math.Abs(row.BalanceAfter-90.8) > 0.0000001 || row.QuestionText != req.Question || row.UnitPriceSnapshot != quote.Charge.Snapshot || row.CreatedAt.IsZero() {
		t.Fatalf("actual ledger fields mismatch: %+v", row)
	}
	// The integrated async path preserves the exact synthetic request IP.
	if row.ClientIP != "192.0.2.3" {
		t.Fatalf("unexpected inherited billing ClientIP %q", row.ClientIP)
	}
	requestByTask := make(map[string]string)
	for _, audit := range audits {
		if audit.Module != aiFallbackAuditModule || audit.UserID != 2 || audit.Username != user.Username || audit.ClientIP != "192.0.2.3" || audit.CreateTime.IsZero() {
			t.Fatalf("actual audit identity mismatch: %+v", audit)
		}
		fields := make(map[string]string)
		for _, field := range strings.Fields(audit.Description) {
			if key, value, ok := strings.Cut(field, "="); ok {
				fields[key] = value
			}
		}
		if fields["taskId"] == "" || fields["requestId"] == "" || fields["status"] != "success" {
			t.Fatalf("actual audit task/request unavailable: %+v", audit)
		}
		requestByTask[fields["taskId"]] = fields["requestId"]
	}
	if len(audits) != 2 || len(requestByTask) != 2 || requestByTask[leader.TaskID] == "" || requestByTask[follower.TaskID] == "" || requestByTask[leader.TaskID] == requestByTask[follower.TaskID] || row.RequestID != requestByTask[leader.TaskID] || row.RequestID == requestByTask[follower.TaskID] {
		t.Fatalf("ledger not attributable only to actual leader request: ledger=%+v taskRequests=%v", row, requestByTask)
	}
	if providerCalls.Load() != 1 || joins.Load() != 1 || networkAttempts.Load() != 0 {
		t.Fatalf("calls/joins/network=%d/%d/%d", providerCalls.Load(), joins.Load(), networkAttempts.Load())
	}
	if manager.GetManager() != nil {
		t.Fatal("manager initialized during synthetic fixture")
	}
	after := map[string]any{"users": readRows("users"), "access_tokens": readRows("access_tokens"), "billing_records": readRows("billing_records"), "logs": readRows("logs")}
	evidence := map[string]any{"before": before, "after": after, "quote": quote, "providerCalls": providerCalls.Load(), "followerJoins": joins.Load(), "networkAttempts": networkAttempts.Load(), "joinKey": ack.key, "sameSharedIdentity": ack.shared == started.shared, "leaderTaskID": leader.TaskID, "followerTaskID": follower.TaskID, "taskRequests": requestByTask, "bothAnswersSame": reflect.DeepEqual(leaderResult, followerResult), "syntheticSQLite": databases, "resolverUsesSyntheticUnderlyingDB": resolvedUnderlyingDB == syntheticUnderlyingDB, "billingClientIPInherited": row.ClientIP, "httpAuthMiddlewareQualified": false}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("D03_EVIDENCE %s\n", raw)
}
