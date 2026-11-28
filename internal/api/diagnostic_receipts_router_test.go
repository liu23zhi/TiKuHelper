package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

const diagnosticFixtureBody = `{"mode":"extension","version":"1.2.3","device_id":"owned-diagnostic-device","launch_id":"11111111-1111-4111-8111-111111111111","batch_id":"22222222-2222-4222-8222-222222222222","entries":[{"event_id":"33333333-3333-4333-8333-333333333333","event_seq":1,"time":"2026-01-01T00:00:00.000Z","event":"runtime.error","level":"error"}]}`

func diagnosticFixtureRequest(r *gin.Engine, method, path, key, proof, body string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, "https://fixture.invalid"+path, strings.NewReader(body))
	q.RemoteAddr = "127.0.0.1:4491"
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Authorization", key)
	q.Header.Set("X-Agreement-Device", proof)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}

func TestDiagnosticReceiptRoutesDoNotTreatOldOrLookalikeAgreementAsConsent(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const key = "diagnostic-owned-plain-key"
	seedHostAccount(t, db, 927, key, 4)
	const base = "/api/extension-logs"
	authorization := "Bearer " + key
	s := service.AgreementStore{DB: db}
	_, proof, err := s.Enroll()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := diagnosticFixtureRequest(r, method, path, authorization, proof, body)
		if res.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res
	}
	wantDisabled := func() {
		t.Helper()
		policy := call("GET", base+"/receipt-policy?mode=extension", "", 200)
		var response struct {
			Enabled   bool
			Transport string
		}
		if err := json.Unmarshal(policy.Body.Bytes(), &response); err != nil || response.Enabled || response.Transport != "receipt-v1" {
			t.Fatalf("expected verified false policy, got %s", policy.Body)
		}
		call("POST", base+"/batches", diagnosticFixtureBody, 403)
	}
	wantDisabled()
	// Simulate an older archive containing a v1.0 whose text predates the
	// new publication check; new attempts to publish it are already refused.
	other := entity.AgreementRelease{DraftID: 7001, Revision: 1, Version: "v1.0", Summary: "Archive", Body: "private fixture lookalike", PublishedAt: time.Now().UTC()}
	if err = db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&entity.AgreementHead{}).Where("id = ?", 1).Update("release_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(service.AgreementCredentials{Secret: proof, Authorization: authorization, IP: "127.0.0.1"}, "v1.0"); err != nil {
		t.Fatal(err)
	}
	wantDisabled()
	legacy := diagnosticFixtureRequest(r, "GET", base+"/policy?mode=extension", authorization, "", "")
	if legacy.Code != 200 || !strings.Contains(legacy.Body.String(), `"enabled":false`) {
		t.Fatalf("legacy writer still enabled without current proof: %d %s", legacy.Code, legacy.Body)
	}
	if _, err = os.Stat("logs/client"); !os.IsNotExist(err) {
		t.Fatalf("no journal or log file should be created for a denied upload; stat error=%v", err)
	}
}

func TestDiagnosticReceiptAdminRoutesProtectBothListAndDownload(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, _ := newHostAdmissionRouter(t)
	for _, path := range []string{"/api/admin/extension-logs/receipts", "/api/admin/extension-logs/receipts/export"} {
		if w := diagnosticFixtureRequest(r, "GET", path, "", "", ""); w.Code != 401 {
			t.Errorf("anonymous %s got %d", path, w.Code)
		}
	}
}

func TestDiagnosticReceiptRoutesApprovedAgreementAndReplay(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const uploaderKey = "diagnostic-owned-writer-key"
	const otherKey = "diagnostic-other-owned-account"
	const password = "synthetic-owned-admin-password"
	seedHostAccount(t, db, 927, uploaderKey, 4)
	seedHostAccount(t, db, 928, otherKey, 4)
	if e := db.Create(&entity.User{ID: 929, Username: "diagnostic-owned-admin", Role: "admin", Status: "enabled", Password: password}).Error; e != nil {
		t.Fatal(e)
	}
	jwt := util.GenerateJwtForUser(929, password)
	const base = "/api/extension-logs"
	const admin = "/api/admin/extension-logs"
	s := service.AgreementStore{DB: db}
	if _, err := s.SetPublicContacts(service.AgreementContacts{Channels: []service.AgreementContact{{Kind: "email", Value: "fixture@example.invalid"}}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.SaveDraft(0, 0, service.ApprovedDiagnosticAgreementTemplate())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(d.ID, d.Revision); err != nil {
		t.Fatal(err)
	}
	_, proof, err := s.Enroll()
	if err != nil {
		t.Fatal(err)
	}
	auth := "Bearer " + uploaderKey
	check := func(method, path, key, secret, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := diagnosticFixtureRequest(r, method, path, key, secret, body)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	policy := func(want string) {
		t.Helper()
		res := check("GET", base+"/receipt-policy?mode=extension", auth, proof, "", 200)
		if res.Body.String() != want {
			t.Fatalf("policy got %s", res.Body)
		}
	}
	policy(`{"enabled":false,"transport":"receipt-v1"}`)
	check("POST", base+"/batches", auth, proof, diagnosticFixtureBody, 403)
	if _, err = os.Stat("logs/client"); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed upload created logs: %v", err)
	}
	check("POST", "/api/agreements/consents", auth, proof, `{"version":"v1.0"}`, 200)
	policy(`{"enabled":true,"transport":"receipt-v1"}`)
	check("POST", base+"/batches", auth, "", diagnosticFixtureBody, 401)
	check("POST", base+"/batches", "Bearer "+otherKey, proof, diagnosticFixtureBody, 401)
	check("POST", base+"/batches", auth, proof, strings.Replace(diagnosticFixtureBody, `"entries":[{`, `"entries":[{"raw":"DO_NOT_COLLECT",`, 1), 400)
	check("POST", base, auth, "", diagnosticFixtureBody, 403)
	first := check("POST", base+"/batches", auth, proof, diagnosticFixtureBody, 200).Body.String()
	retry := check("POST", base+"/batches", auth, proof, diagnosticFixtureBody, 200).Body.String()
	if first != retry || !strings.Contains(first, `"accepted_event_ids":["33333333-3333-4333-8333-333333333333"]`) ||
		!strings.Contains(first, `"batch_id":"22222222-2222-4222-8222-222222222222"`) || strings.Contains(first, proof) {
		t.Fatal("server did not return same full scoped proof")
	}
	check("POST", base+"/batches", auth, proof, strings.Replace(diagnosticFixtureBody, "runtime.error", "network.offline", 1), 409)
	laterBatch := strings.NewReplacer(
		"22222222-2222-4222-8222-222222222222", "44444444-4444-4444-8444-444444444444",
		"33333333-3333-4333-8333-333333333333", "55555555-5555-4555-8555-555555555555",
		`"event_seq":1`, `"event_seq":2`,
		"2026-01-01T00:00:00.000Z", "2025-12-31T23:00:00.000Z",
		"runtime.error", "network.offline",
	).Replace(diagnosticFixtureBody)
	check("POST", base+"/batches", auth, proof, laterBatch, 200)
	check("GET", admin+"/receipts", auth, "", "", 401)
	firstPage := check("GET", admin+"/receipts?user_id=927&limit=1", jwt, "", "", 200)
	var page struct {
		Entries []struct{ Event, Time string }
		Next    string `json:"next_cursor"`
	}
	if err = json.Unmarshal(firstPage.Body.Bytes(), &page); err != nil || len(page.Entries) != 1 || page.Entries[0].Event != "network.offline" ||
		page.Entries[0].Time != "2025-12-31T23:00:00.000Z" || page.Next == "" {
		t.Fatalf("query not sorted before pagination: %s", firstPage.Body)
	}
	next := check("GET", admin+"/receipts?user_id=927&limit=1&cursor="+page.Next, jwt, "", "", 200)
	if !strings.Contains(next.Body.String(), "runtime.error") || strings.Contains(next.Body.String(), "network.offline") {
		t.Fatal("unexpected cursor continuation")
	}
	check("GET", admin+"/receipts?user_id=928&limit=1&cursor="+page.Next, jwt, "", "", 410)
	mime := check("GET", admin+"/receipts/export?user_id=927", jwt, "", "", 200)
	rows := strings.Split(strings.TrimSpace(mime.Body.String()), "\n")
	if len(rows) != 2 || !strings.Contains(rows[0], "network.offline") || !strings.Contains(rows[1], "runtime.error") ||
		strings.Contains(mime.Body.String(), "event_id") || strings.Contains(mime.Body.String(), "owned-diagnostic-device") || strings.Contains(mime.Body.String(), proof) ||
		!strings.Contains(mime.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatal("export differed from paging, leaked fields, or duplicated rows")
	}
	check("PUT", admin+"/settings", jwt, "", `{"enabled":false,"retention_days":8,"max_gb":10}`, 400)
	check("PUT", admin+"/settings", jwt, "", `{"enabled":false,"retention_days":7,"max_gb":10}`, 200)
	policy(`{"enabled":false,"transport":"receipt-v1"}`)
	check("POST", base+"/batches", auth, proof, diagnosticFixtureBody, 403)
	check("PUT", admin+"/settings", jwt, "", `{"enabled":true,"retention_days":7,"max_gb":10}`, 200)
	nextDraft, err := s.SaveDraft(0, 0, entity.AgreementContent{Version: "v1.1", Summary: "rotation", Body: "rotation is not approved T06 content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(nextDraft.ID, nextDraft.Revision); err != nil {
		t.Fatal(err)
	}
	policy(`{"enabled":false,"transport":"receipt-v1"}`)
	check("POST", base+"/batches", auth, proof, diagnosticFixtureBody, 403)
	var bound, bills int64
	if e := db.Model(&entity.AccountDeviceBinding{}).Count(&bound).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&entity.BillingRecord{}).Count(&bills).Error; e != nil {
		t.Fatal(e)
	}
	if bound != 0 || bills != 0 {
		t.Fatalf("diagnostics mutated devices or billing: %d %d", bound, bills)
	}
	t.Logf("actual router dialect=%s: pre-consent/accepted/disabled/rotated, exact retry 1x, pages/export share 2 sorted rows; business bindings=0, bills=0", db.Dialector.Name())
}

func TestDiagnosticReceiptPreflightUsesOnlyScopedAgreementHeaders(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, _ := newHostAdmissionRouter(t)
	for _, item := range []struct {
		path    string
		allowed bool
	}{
		{"/api/extension-logs/receipt-policy", true}, {"/api/extension-logs/batches", true},
		{"/api/extension-logs", false}, {"/api/agreements/contacts", false},
	} {
		req := httptest.NewRequest("OPTIONS", "https://fixture.invalid"+item.path, nil)
		req.RemoteAddr = "127.0.0.1:4491"
		req.Header.Set("Origin", "chrome-extension://abcdefghijklmnopabcdefghijklmnop")
		req.Header.Set("Access-Control-Request-Headers", "Authorization, X-Agreement-Device")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 204 || strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Agreement-Device") != item.allowed {
			t.Errorf("preflight %s status=%d allowed=%q", item.path, w.Code, w.Header().Get("Access-Control-Allow-Headers"))
		}
	}
}
