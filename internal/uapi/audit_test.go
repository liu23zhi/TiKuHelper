package uapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

func TestUAPIAuditAnchorsAndRedactsActualCalls(t *testing.T) {
	const marker = "uapi_call "
	const key = "uapi-synthetic-credential-excluded"
	const rawID = "synthetic-upstream-id-excluded"
	const rawRegion = "synthetic-raw-region-excluded"
	const ip = "192.0.2.25"
	const rawPath = "/synthetic-private-path-excluded"
	var captured bytes.Buffer
	old := gin.DefaultWriter
	gin.DefaultWriter = &captured
	t.Cleanup(func() { gin.DefaultWriter = old })
	logger.SysLog("audit fixture writer ready")
	if !strings.Contains(captured.String(), "audit fixture writer ready") {
		t.Fatal("existing diagnostic writer missing in fixture")
	}

	var ipCalls, usageCalls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", rawID)
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/ip":
			ipCalls.Add(1)
			if r.URL.Query().Get("ip") != ip {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"ip":%q,"region":%q}`, ip, rawRegion)
		case "/usage":
			if usageCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"message":"synthetic-raw-throttle-excluded"}`)
				return
			}
			fmt.Fprintf(w, `{"path":%q,"count":0}`, rawPath)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()
	c := NewClient(key).SetIPInfoURL(s.URL + "/ip").SetStatusUsageURL(s.URL + "/usage").SetMaxRetries(1)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if v, err := c.GetIPInfo(context.Background(), ip, ""); err != nil || v.IP != ip || v.Region != rawRegion {
		t.Fatalf("existing IP response was changed: err=%v", err)
	}
	if v, err := c.GetStatusUsage(context.Background(), rawPath); err != nil || v.Path != rawPath || v.Count != 0 {
		t.Fatalf("existing status response was changed: err=%v", err)
	}
	if ipCalls.Load() != 1 || usageCalls.Load() != 2 {
		t.Fatalf("existing attempts changed: ip=%d usage=%d", ipCalls.Load(), usageCalls.Load())
	}

	for _, private := range []string{key, rawID, rawRegion, ip, rawPath, "synthetic-raw-throttle-excluded"} {
		if strings.Contains(captured.String(), private) {
			t.Fatal("audit writer contains private synthetic input or upstream value")
		}
	}
	type event struct {
		Phase      string         `json:"phase"`
		Category   string         `json:"category"`
		ID         string         `json:"request_id"`
		UpstreamID string         `json:"upstream_id_hash"`
		Status     int            `json:"http_status"`
		Attempts   int            `json:"attempts"`
		Retries    int            `json:"retries"`
		Result     string         `json:"outcome"`
		Summary    map[string]int `json:"summary"`
	}
	events := make([]event, 0, 4)
	for _, line := range strings.Split(captured.String(), "\n") {
		at := strings.Index(line, marker)
		if at < 0 {
			continue
		}
		var v event
		if err := json.Unmarshal([]byte(line[at+len(marker):]), &v); err != nil {
			t.Fatalf("expected one-line structured audit: %v", err)
		}
		events = append(events, v)
	}
	if len(events) != 4 {
		t.Fatalf("two local UAPI calls (wire attempts 1 and 2), expected 4 audit events, got %d", len(events))
	}
	for i, k := range []string{"ip_lookup", "usage_status"} {
		begin, end := events[i*2], events[i*2+1]
		if begin.Phase != "start" || end.Phase != "finish" || begin.Category != k || end.Category != k ||
			begin.ID == "" || begin.ID != end.ID || end.UpstreamID == "" || end.UpstreamID == rawID ||
			end.Status != 200 || end.Attempts != i+1 || end.Retries != i || end.Result != "success" || len(end.Summary) == 0 {
			t.Errorf("invalid %s lifecycle: start=%+v finish=%+v", k, begin, end)
		}
	}
	if events[0].ID == events[2].ID {
		t.Error("unrelated logical provider calls share the same locally scoped ID")
	}
}
