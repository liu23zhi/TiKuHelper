package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
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
	d, err := s.SaveDraft(0, 0, entity.AgreementContent{Version: "v1.0", Summary: "Not the approved T06 text", Body: "private fixture lookalike"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(d.ID, d.Revision); err != nil {
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
