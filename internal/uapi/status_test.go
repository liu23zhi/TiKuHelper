package uapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetStatusUsageUsesDocumentedEndpointAndOptionalPath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer uapi-test-key" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		gotPath = r.URL.Query().Get("path")
		w.Header().Set("Content-Type", "application/json")
		if gotPath != "" {
			_, _ = w.Write([]byte(`{"path":"/api/v1/search/aggregate","count":12}`))
			return
		}
		_, _ = w.Write([]byte(`{"endpoints":[{"path":"/api/v1/search/aggregate","count":12}],"unaggregated":{"count":2,"oldest_log":"2026-10-16T00:00:00+08:00"}}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetStatusUsageURL(server.URL)
	result, err := client.GetStatusUsage(context.Background(), "/api/v1/search/aggregate")
	if err != nil {
		t.Fatalf("GetStatusUsage returned error: %v", err)
	}
	if gotPath != "/api/v1/search/aggregate" {
		t.Fatalf("expected path query, got %q", gotPath)
	}
	if result.Path != "/api/v1/search/aggregate" || result.Count != 12 || result.RawJSON == "" {
		t.Fatalf("unexpected status usage result: %+v", result)
	}
	result, err = client.GetStatusUsage(context.Background(), "")
	if err != nil || len(result.Endpoints) != 1 || result.Endpoints[0].Count != 12 || result.Unaggregated.Count != 2 {
		t.Fatalf("unexpected aggregate status usage result: err=%v result=%+v", err, result)
	}
}

func TestGetStatusUsageRejectsEmptyPathOnlyWhenResponseIsMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/api/v1/search/aggregate"}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetStatusUsageURL(server.URL)
	if _, err := client.GetStatusUsage(context.Background(), "/api/v1/search/aggregate"); err == nil {
		t.Fatal("expected malformed status usage response to fail")
	}
}
