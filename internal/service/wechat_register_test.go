package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWechatRegisterFlowStoreCreateOrResumeIsIdempotent(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	created, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(created.Token) == "" || created.Status != WechatRegisterStatusPending {
		t.Fatalf("unexpected created flow: %+v", created)
	}
	resumed, err := store.CreateOrResume(created.Token, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Token != created.Token || resumed.FlowID != created.FlowID {
		t.Fatalf("expected same flow on resume, got created=%+v resumed=%+v", created, resumed)
	}
	if resumed.ExpiresAtUnix <= created.ExpiresAtUnix {
		t.Fatalf("expected idle expiry to extend, got created=%+v resumed=%+v", created, resumed)
	}
}

func TestWechatRegisterFlowStoreUnknownTokenDoesNotCreateFlow(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	if _, err := store.CreateOrResume("unknown-token", time.Unix(100, 0)); !errors.Is(err, ErrWechatRegisterFlowInvalid) {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}

func TestWechatRegisterFlowStoreExpiredTokenDoesNotCreateReplacementFlow(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	stored := store.flows[hashWechatRegisterToken(flow.Token)]
	store.mu.Unlock()
	stored.mu.Lock()
	stored.expiresAt = now.Add(-time.Second)
	stored.mu.Unlock()
	if _, err := store.CreateOrResume(flow.Token, now); !errors.Is(err, ErrWechatRegisterFlowExpired) {
		t.Fatalf("expected expired token error, got %v", err)
	}
}

func TestWechatRegisterQRCodeDoesNotCreateOrdinaryLoginSession(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/wechat/create_login_qrcode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"scene_id":       "register-scene",
					"qrcode_url":     "http://" + r.Host + "/qr.png",
					"login_token":    "register-login-token",
					"expire_seconds": 180,
				},
			})
		case "/qr.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNGBytes(t))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result, err := CreateWechatRegisterQRCodeWithDB(db, WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{Client: WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()}, LoginToken: result.LoginToken}); err == nil {
		t.Fatal("ordinary login status must reject registration QR session")
	}
}

func TestWechatRegisterFlowStoreReusesCurrentQRCodeAndRefreshInvalidatesOldSession(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	factory := func() (WechatRegisterQRCode, error) {
		created++
		return WechatRegisterQRCode{SessionID: "session-" + string(rune('0'+created)), DisplayData: "image", ExpiresAt: now.Add(2 * time.Minute)}, nil
	}
	first, err := store.GetOrCreateQRCode(flow.Token, now, false, factory)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := store.GetOrCreateQRCode(flow.Token, now.Add(time.Second), false, factory)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != reused.SessionID || created != 1 {
		t.Fatalf("expected current QR reuse, first=%+v reused=%+v created=%d", first, reused, created)
	}
	refreshed, err := store.GetOrCreateQRCode(flow.Token, now.Add(2*time.Second), true, factory)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.SessionID == first.SessionID || created != 2 {
		t.Fatalf("expected refreshed QR, first=%+v refreshed=%+v created=%d", first, refreshed, created)
	}
	if ok := store.StageByQRCodeSession(first.SessionID, "stale-wechat", now.Add(3*time.Second)); ok {
		t.Fatal("expected stale QR callback to be ignored")
	}
	if status, err := store.Status(flow.Token, now.Add(3*time.Second)); err != nil || status.Status != WechatRegisterStatusPending {
		t.Fatalf("expected pending flow after stale callback, status=%+v err=%v", status, err)
	}
}

func TestWechatRegisterFlowStoreResumeThenQRCodeReusesCurrentSession(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	created, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	createdQR, err := store.GetOrCreateQRCode(created.Token, now, false, func() (WechatRegisterQRCode, error) {
		return WechatRegisterQRCode{SessionID: "resume-session", DisplayData: "image", ExpiresAt: now.Add(time.Minute)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store.CreateOrResume(created.Token, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resumedQR, err := store.GetOrCreateQRCode(resumed.Token, now.Add(2*time.Second), false, func() (WechatRegisterQRCode, error) {
		t.Fatal("QR factory must not run when resuming a flow with a current session")
		return WechatRegisterQRCode{}, nil
	})
	if err != nil || resumedQR.SessionID != createdQR.SessionID {
		t.Fatalf("expected resumed flow to reuse current QR, created=%+v resumed=%+v err=%v", createdQR, resumedQR, err)
	}
}

func TestWechatRegisterFlowStoreConcurrentQRCodeCreationCallsFactoryOnce(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	continueFactory := make(chan struct{})
	var calls atomic.Int32
	factory := func() (WechatRegisterQRCode, error) {
		calls.Add(1)
		close(started)
		<-continueFactory
		return WechatRegisterQRCode{SessionID: "concurrent-session", ExpiresAt: now.Add(time.Minute)}, nil
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = store.GetOrCreateQRCode(flow.Token, now, false, factory)
	}()
	<-started
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_, _ = store.GetOrCreateQRCode(flow.Token, now, false, factory)
	}()
	select {
	case <-secondDone:
		t.Fatal("second QR creation should wait for the per-flow lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(continueFactory)
	<-firstDone
	<-secondDone
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one factory call, got %d", got)
	}
}

func TestWechatRegisterFlowStoreQRCodeExpiryKeepsFlowForRefresh(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GetOrCreateQRCode(flow.Token, now, false, func() (WechatRegisterQRCode, error) {
		return WechatRegisterQRCode{SessionID: "expired-session", ExpiresAt: now.Add(time.Second)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.StatusByQRCodeSession(first.SessionID, now.Add(2*time.Second))
	if err != nil || status.Status != WechatRegisterStatusExpired {
		t.Fatalf("expected expired QR status while retaining flow, status=%+v err=%v", status, err)
	}
	refreshed, err := store.GetOrCreateQRCode(flow.Token, now.Add(2*time.Second), false, func() (WechatRegisterQRCode, error) {
		return WechatRegisterQRCode{SessionID: "fresh-session", ExpiresAt: now.Add(3 * time.Minute)}, nil
	})
	if err != nil || refreshed.SessionID != "fresh-session" {
		t.Fatalf("expected same flow to refresh QR, refreshed=%+v err=%v", refreshed, err)
	}
}

func TestWechatRegisterFlowStoreStagedStatusSurvivesQRCodeExpiry(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	qr, err := store.GetOrCreateQRCode(flow.Token, now, false, func() (WechatRegisterQRCode, error) {
		return WechatRegisterQRCode{SessionID: "staged-expiring-session", ExpiresAt: now.Add(time.Second)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(flow.Token, "staged-wechat", now); err != nil {
		t.Fatal(err)
	}
	status, err := store.StatusByQRCodeSession(qr.SessionID, now.Add(2*time.Second))
	if err != nil || status.Status != WechatRegisterStatusStaged {
		t.Fatalf("expected staged status after QR expiry, status=%+v err=%v", status, err)
	}
}

func TestWechatRegisterFlowStoreAllowsSameWechatIDInIndependentFlows(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	first, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(first.Token, "same-wechat", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(second.Token, "same-wechat", now); err != nil {
		t.Fatal(err)
	}
	firstStatus, _ := store.Status(first.Token, now)
	secondStatus, _ := store.Status(second.Token, now)
	if firstStatus.Status != WechatRegisterStatusStaged || secondStatus.Status != WechatRegisterStatusStaged {
		t.Fatalf("expected independent staged flows, first=%+v second=%+v", firstStatus, secondStatus)
	}
}

func TestWechatRegisterFlowStoreReleaseAndExpiryCleanupAreIdempotent(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(flow.Token); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(flow.Token); err != nil {
		t.Fatalf("release should be idempotent: %v", err)
	}
	if _, err := store.Status(flow.Token, now); err == nil {
		t.Fatal("expected released flow to be unavailable")
	}
	expired, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if removed := store.CleanupExpired(now.Add(wechatRegisterFlowIdleTTL + time.Second)); removed != 1 {
		t.Fatalf("expected one expired flow removed, got %d", removed)
	}
	if err := store.Release(expired.Token); err != nil {
		t.Fatalf("expired release should be idempotent: %v", err)
	}
}

func TestWechatRegisterFlowStoreBackgroundCleanupRunsOnceAndRemovesExpiredSession(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Now()
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreateQRCode(flow.Token, now, false, func() (WechatRegisterQRCode, error) {
		return WechatRegisterQRCode{SessionID: "cleanup-session", ExpiresAt: now.Add(time.Hour)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	flowHash := hashWechatRegisterToken(flow.Token)
	store.mu.Lock()
	storedFlow := store.flows[flowHash]
	store.mu.Unlock()
	storedFlow.mu.Lock()
	storedFlow.expiresAt = now.Add(-time.Second)
	storedFlow.mu.Unlock()
	stop := store.StartCleanup(5 * time.Millisecond)
	secondStop := store.StartCleanup(5 * time.Millisecond)
	if stop == nil || secondStop == nil {
		t.Fatal("expected idempotent cleanup stop functions")
	}
	defer stop()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.StatusByQRCodeSession("cleanup-session", time.Now()); err != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background cleanup did not remove expired flow/session")
}

func TestWechatRegisterFlowStoreConsumeIsLockScopedAndRetainsOnFailure(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(flow.Token, "wechat-consume", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Consume(flow.Token, now, func(wechatID string) error {
		if wechatID != "wechat-consume" {
			t.Fatalf("unexpected staged ID %q", wechatID)
		}
		return errors.New("consume failed")
	}); err == nil {
		t.Fatal("expected consume callback failure")
	}
	if status, err := store.Status(flow.Token, now); err != nil || status.Status != WechatRegisterStatusStaged {
		t.Fatalf("expected failed consume to retain flow, status=%+v err=%v", status, err)
	}
	if err := store.Consume(flow.Token, now, func(wechatID string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(flow.Token, now); err == nil {
		t.Fatal("expected successful consume to release flow")
	}
}

func TestWechatRegisterFlowStoreStageByCodeSerializesAndPreservesFirstResult(t *testing.T) {
	store := NewWechatRegisterFlowStore()
	now := time.Unix(100, 0)
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	continueResolve := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.StageByCode(flow.Token, now, func() (string, error) {
			close(started)
			<-continueResolve
			return "first-wechat", nil
		})
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.StageByCode(flow.Token, now, func() (string, error) {
			return "second-wechat", nil
		})
	}()
	select {
	case <-secondDone:
		t.Fatal("second code request must wait for the per-flow lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(continueResolve)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, ErrWechatRegisterAlreadyStaged) {
		t.Fatalf("expected second code request to preserve first result, got %v", err)
	}
	status, err := store.Status(flow.Token, now)
	if err != nil || status.Status != WechatRegisterStatusStaged {
		t.Fatalf("expected staged status, status=%+v err=%v", status, err)
	}
}

func TestNormalizeWechatUpstreamStatusMapsExpiredValues(t *testing.T) {
	for _, value := range []string{"expired", "timeout", "timed_out", "cancelled", "canceled", "qrcode_expired"} {
		if got := normalizeWechatUpstreamStatus(value); got != WechatLoginStatusExpired {
			t.Fatalf("normalizeWechatUpstreamStatus(%q) = %q, want %q", value, got, WechatLoginStatusExpired)
		}
	}
}
