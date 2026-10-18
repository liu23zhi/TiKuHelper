package service

import (
	"errors"
	"strings"
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
