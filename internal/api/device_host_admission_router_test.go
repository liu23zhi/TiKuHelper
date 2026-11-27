package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// The explicit first-host operation is deliberately a different authenticated
// command from the read-only quota and from the bound-device heartbeat.
func TestHostAdmissionRouteIsExplicitAndProtected(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	router, _ := newHostAdmissionRouter(t)

	const path = "/api/user/device-admission"
	found := 0
	for _, route := range router.Routes() {
		if route.Path != path {
			continue
		}
		if route.Method != http.MethodPost {
			t.Errorf("device admission registered with unexpected method: %s", route.Method)
		} else {
			found++
		}
	}
	if found != 1 {
		t.Errorf("expected exactly one POST host admission route, got %d", found)
	}

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("anonymous host admission should be rejected by auth with 401, got %d", res.Code)
	}
}

// Actual router middleware uses application singletons. Isolate it in a child
// with disposable local state, following the search-health device route fixture.
func hostAdmissionChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("HOST_ADMISSION_FIXTURE_CHILD") == "1" {
		return true
	}
	dir := t.TempDir()
	config, err := json.Marshal(map[string]any{
		"database": map[string]any{"path": filepath.Join(dir, "host-admission.db")},
		"limit":    map[string]any{"enable": true, "duration": 60, "requests": 120},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=45s", "-test.v")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOST_ADMISSION_FIXTURE_CHILD=1", "SQL_DSN=", "JWT_SECRET=HostAdmissionContractSynthetic246!ABC",
		"TIKU_ADMIN_PASSWORD=HostAdmissionFixture246!", "TIKU_APP_MODE=production", "EXTENSION_UPDATE_ENABLED=false", "EXTENSION_EDGE_ID=", "EXTENSION_CHROME_ID=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated real host route: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "commit_failed_http=") {
			t.Log(strings.TrimSpace(line))
		}
	}
	return false
}

func newHostAdmissionRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	manager, err := manager.CreateManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.CloseManager(); err != nil {
			t.Error(err)
		}
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	return router, manager.GetDB()
}

func seedHostAccount(t *testing.T, db *gorm.DB, id int32, key string, limit int32) int32 {
	t.Helper()
	user := entity.User{ID: id, Username: fmt.Sprintf("fixture-host-%d", id), Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	level := entity.UserLevel{Rank: id, Name: user.Username, Enabled: true, DeviceLimit: limit}
	if err := db.Create(&level).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("users").Where("id = ?", id).Updates(map[string]any{"level_id": level.ID, "device_limit_override": -1}).Error; err != nil {
		t.Fatal(err)
	}
	return seedHostToken(t, db, id, key, 0)
}

func seedHostToken(t *testing.T, db *gorm.DB, userID int32, key string, scriptID int32) int32 {
	t.Helper()
	digest := sha256.Sum256([]byte(key))
	token := entity.AccessToken{UserID: userID, ScriptID: scriptID, Name: key, TokenHash: hex.EncodeToString(digest[:]), Status: "enabled"}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	return token.ID
}

func hostFixtureRequest(router *gin.Engine, method, path, authorization, identity, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.21:14001"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorization)
	if identity != "" {
		req.Header.Set("X-Script-Identity", identity)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestHostAdmissionRealRouteCreatesOneZeroSessionSlotThenOwnHeartbeatWorks(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const base = "/api/user/device-admission"
	tokenID := seedHostAccount(t, db, 913, "local-fixture-913", 4)
	seedHostAccount(t, db, 914, "local-fixture-914", 2)
	const auth = "Bearer local-fixture-913"
	const heartbeat = "/api/user/device-sessions"
	if out := hostFixtureRequest(r, http.MethodGet, "/api/user/device-quota", auth, "", ""); out.Code != http.StatusOK {
		t.Fatalf("quota before: %d", out.Code)
	}
	if out := hostFixtureRequest(r, http.MethodPost, heartbeat, auth, "", `{"deviceId":"exe:fixture","count":0}`); out.Code != http.StatusNotFound {
		t.Fatalf("unknown heartbeat: %d", out.Code)
	}
	if used, err := service.CountDevices(db, 913); err != nil || used != 0 {
		t.Fatalf("read/unknown report changed slots: %d %v", used, err)
	}
	const input = `{"deviceId":" exe:fixture ","clientInstance":" fixture-instance "}`
	accept := func() {
		t.Helper()
		out := hostFixtureRequest(r, http.MethodPost, base, auth, "", input)
		var receipt struct {
			DeviceID string
			Admitted bool
		}
		if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &receipt) != nil || receipt.DeviceID != "exe:fixture" || !receipt.Admitted {
			t.Fatalf("expected committed normalized host receipt: %d %s", out.Code, out.Body)
		}
	}
	accept()
	accept()
	var row entity.AccountDeviceBinding
	if err := db.Where("user_id = ? AND device_id = ?", 913, "exe:fixture").First(&row).Error; err != nil ||
		row.TokenID != tokenID || row.OwnerKind != "token" || row.ClientInstance != "fixture-instance" ||
		row.SessionCount != 0 || row.SessionReportedAt != nil || row.FirstIP != "192.0.2.21" {
		t.Fatalf("host commit missing its trusted quota/identity fields: %+v %v", row, err)
	}
	if used, err := service.CountDevices(db, 913); err != nil || used != 1 {
		t.Fatalf("zero-browser host should occupy 1: %d %v", used, err)
	}
	if out := hostFixtureRequest(r, http.MethodPost, heartbeat, "Bearer local-fixture-914", "", `{"deviceId":"exe:fixture","count":2}`); out.Code != http.StatusNotFound {
		t.Fatalf("foreign heartbeat: %d", out.Code)
	}
	if out := hostFixtureRequest(r, http.MethodPost, heartbeat, auth, "", `{"deviceId":"exe:fixture","count":3}`); out.Code != http.StatusNoContent {
		t.Fatalf("own heartbeat: %d %s", out.Code, out.Body)
	}
	accept()
	var latest entity.AccountDeviceBinding
	if err := db.First(&latest, row.ID).Error; err != nil || latest.SessionCount != 3 || latest.SessionReportedAt == nil {
		t.Fatalf("valid retry changed active browser count: %+v %v", latest, err)
	}
	var count int64
	for model, want := range map[any]int64{&entity.AccountDeviceBinding{}: 1, &entity.BillingRecord{}: 0, &entity.Log{}: 0, &entity.ScriptRunSession{}: 0} {
		if err := db.Model(model).Count(&count).Error; err != nil || count != want {
			t.Fatalf("unrelated state changed: %T count=%d err=%v", model, count, err)
		}
	}
}

func TestHostAdmissionRealRouteDenialsDoNotPersist(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const path = "/api/user/device-admission"
	seedHostAccount(t, db, 915, "local-fixture-915", 1)
	seedHostAccount(t, db, 916, "local-fixture-916", 1)
	scriptTokenID := seedHostToken(t, db, 915, "local-fixture-script-token", 5)
	if scriptTokenID <= 0 {
		t.Fatal("missing script token fixture")
	}
	seedHostAccount(t, db, 917, "local-fixture-banned", 1)
	if err := db.Model(&entity.User{}).Where("id = ?", 917).Update("status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	const body = `{"deviceId":"exe:fixture-host","clientInstance":"fixture-one"}`
	test := func(auth, identity, body string, status int, code string) {
		t.Helper()
		out := hostFixtureRequest(r, http.MethodPost, path, auth, identity, body)
		var receipt struct{ Code string }
		if json.Unmarshal(out.Body.Bytes(), &receipt) != nil || out.Code != status || (code != "" && receipt.Code != code) {
			t.Errorf("expected %d %s, got %d %s", status, code, out.Code, out.Body)
		}
	}
	test("Bearer local-fixture-915", "fixture-script-identity", body, http.StatusForbidden, "")
	test("Bearer local-fixture-script-token", "", body, http.StatusUnauthorized, "")
	test("Bearer local-fixture-banned", "", body, http.StatusForbidden, "ACCOUNT_BANNED")
	test("Bearer local-fixture-915", "", `{"deviceId":"exe:fixture-host"}`, http.StatusBadRequest, "")
	test("Bearer local-fixture-915", "", `{"deviceId":"exe:fixture-host","clientInstance":"fixture-one","userId":916}`, http.StatusBadRequest, "")
	test("Bearer local-fixture-915", "", `{"deviceId":"exe:fixture-host","clientInstance":"fixture-one","tokenId":777}`, http.StatusBadRequest, "")
	test("Bearer local-fixture-915", "", `{"deviceId":"exe:\u0000fixture","clientInstance":"fixture-one"}`, http.StatusBadRequest, "")
	test("Bearer local-fixture-915", "", body, http.StatusOK, "")
	test("Bearer local-fixture-916", "", `{"deviceId":"exe:fixture-host","clientInstance":"fixture-two"}`, http.StatusConflict, "DEVICE_ID_CONFLICT")
	test("Bearer local-fixture-915", "", `{"deviceId":"exe:fixture-second","clientInstance":"fixture-one"}`, http.StatusForbidden, "DEVICE_LIMIT_EXCEEDED")
	var locks int64
	if err := db.Model(&entity.DeviceLifecycleLock{}).Count(&locks).Error; err != nil || locks != 1 {
		t.Fatalf("rejected admissions must not leave storage locks: %d %v", locks, err)
	}
	if err := db.Create(&entity.UserDeviceBlock{UserID: 916, DeviceID: "exe:fixture-account-banned"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.BanDevice(db, service.DeviceBanRequest{DeviceID: "exe:fixture-global-banned", Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ id, reason string }{{"exe:fixture-account-banned", "user-blocked"}, {"exe:fixture-global-banned", "device-blacklisted"}} {
		body := fmt.Sprintf(`{"deviceId":%q,"clientInstance":"fixture"}`, entry.id)
		out := hostFixtureRequest(r, http.MethodPost, path, "Bearer local-fixture-916", "", body)
		var payload struct{ Reason string }
		if out.Code != http.StatusForbidden || json.Unmarshal(out.Body.Bytes(), &payload) != nil || payload.Reason != entry.reason {
			t.Errorf("existing block reason %s: %d %s", entry.reason, out.Code, out.Body)
		}
	}
	if err := db.Migrator().DropTable(&entity.UserDeviceBlock{}); err != nil {
		t.Fatal(err)
	}
	test("Bearer local-fixture-916", "", `{"deviceId":"exe:fixture-unready","clientInstance":"fixture-two"}`, http.StatusServiceUnavailable, "")
	if used, err := service.CountDevices(db, 916); err != nil || used != 0 {
		t.Fatalf("denial persisted binding: %d %v", used, err)
	}
}

func TestHostAdmissionRebindResetsOnlyRevokedHeartbeatOnLastSlot(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const admission = "/api/user/device-admission"
	const heartbeat = "/api/user/device-sessions"
	const auth = "Bearer local-fixture-rebind"
	seedHostAccount(t, db, 919, "local-fixture-rebind", 1)
	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	old := entity.AccountDeviceBinding{UserID: 919, DeviceID: "exe:revoked-fixture", FirstSeenAt: stamp, LastSeenAt: stamp,
		RevokedAt: &stamp, RevokedReason: "idle_timeout", SessionCount: 5, SessionReportedAt: &stamp}
	occupying := entity.AccountDeviceBinding{UserID: 919, DeviceID: "exe:occupying", LastSeenAt: time.Now()}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&occupying).Error; err != nil {
		t.Fatal(err)
	}
	const input = `{"deviceId":"exe:revoked-fixture","clientInstance":"new-fixture-process"}`
	send := func(want int) {
		t.Helper()
		out := hostFixtureRequest(r, http.MethodPost, admission, auth, "", input)
		if out.Code != want {
			t.Fatalf("registration: expected %d got %d %s", want, out.Code, out.Body)
		}
	}
	read := func() entity.AccountDeviceBinding {
		t.Helper()
		var latest entity.AccountDeviceBinding
		if err := db.First(&latest, old.ID).Error; err != nil {
			t.Fatal(err)
		}
		return latest
	}
	send(http.StatusForbidden)
	if latest := read(); latest.RevokedAt == nil || !latest.RevokedAt.Equal(stamp) || latest.RevokedReason != "idle_timeout" || latest.SessionCount != 5 {
		t.Fatalf("failed capacity check changed revoked history: %+v", latest)
	}
	if _, _, err := service.RemoveAccountDevice(db, occupying.ID, service.DeviceHistoryActor{UserID: 919}); err != nil {
		t.Fatal(err)
	}
	send(http.StatusOK)
	if latest := read(); latest.RevokedAt != nil || latest.RevokedReason != "" || latest.SessionCount != 0 || latest.SessionReportedAt != nil ||
		!latest.FirstSeenAt.Equal(stamp) {
		t.Fatalf("rebind did not start fresh on original identity: %+v", latest)
	}
	if used, err := service.CountDevices(db, 919); err != nil || used != 1 {
		t.Fatalf("rebind must use the one remaining slot: %d %v", used, err)
	}
	var archived int64
	if err := db.Model(&entity.Log{}).Where("module = ? AND description = ?", "device", "保全历史设备撤销证据").Count(&archived).Error; err != nil || archived != 1 {
		t.Fatalf("missing durable revoke audit: %d %v", archived, err)
	}
	if out := hostFixtureRequest(r, http.MethodPost, heartbeat, auth, "", `{"deviceId":"exe:revoked-fixture","count":2}`); out.Code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", out.Code)
	}
	send(http.StatusOK)
	if latest := read(); latest.SessionCount != 2 || latest.SessionReportedAt == nil {
		t.Fatalf("idempotent retry cleared a valid heartbeat: %+v", latest)
	}
}

func TestHostAdmissionConcurrentRetrySharesOnlyOnePersistentSlot(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	seedHostAccount(t, db, 920, "local-fixture-concurrent-retry", 1)
	seedHostAccount(t, db, 921, "local-fixture-concurrent-different", 1)
	for _, test := range []struct {
		key      string
		uid      int
		ids      []string
		expected map[int]int
	}{
		{"local-fixture-concurrent-retry", 920, []string{"exe:same", "exe:same"}, map[int]int{http.StatusOK: 2}},
		{"local-fixture-concurrent-different", 921, []string{"exe:one", "exe:two"}, map[int]int{http.StatusOK: 1, http.StatusForbidden: 1}},
	} {
		start := make(chan struct{})
		results := make(chan int, 2)
		for _, id := range test.ids {
			go func() {
				<-start
				body := fmt.Sprintf(`{"deviceId":%q,"clientInstance":"concurrent-fixture"}`, id)
				out := hostFixtureRequest(r, http.MethodPost, "/api/user/device-admission", "Bearer "+test.key, "", body)
				results <- out.Code
			}()
		}
		close(start)
		got := map[int]int{}
		for range test.ids {
			got[<-results]++
		}
		if !maps.Equal(got, test.expected) {
			t.Fatalf("unexpected competing/retry responses: %+v", got)
		}
		var n int64
		if err := db.Model(&entity.AccountDeviceBinding{}).Where("user_id = ?", test.uid).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("non-idempotent committed binding count: %d %v", n, err)
		}
	}
}

func TestHostAdmissionRebindDoesNotSucceedOrEraseHistoryWhenWriteFails(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	seedHostAccount(t, db, 922, "local-fixture-failed-write", 1)
	stamp := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	row := entity.AccountDeviceBinding{UserID: 922, DeviceID: "exe:fixture-failed-write", FirstSeenAt: stamp, LastSeenAt: stamp,
		RevokedAt: &stamp, RevokedReason: "idle_timeout", SessionCount: 4, SessionReportedAt: &stamp}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	failedWrite := false
	if err := db.Callback().Update().After("gorm:update").Register("test:host-fail-after-rebind", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameAccountDeviceBinding && tx.RowsAffected > 0 {
			failedWrite = true
			tx.AddError(fmt.Errorf("synthetic rebind failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	out := hostFixtureRequest(r, http.MethodPost, "/api/user/device-admission", "Bearer local-fixture-failed-write", "",
		`{"deviceId":"exe:fixture-failed-write","clientInstance":"fixture"}`)
	var after entity.AccountDeviceBinding
	if err := db.First(&after, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	var audit int64
	if err := db.Model(&entity.Log{}).Where("module = ?", "device").Count(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if !failedWrite || out.Code != http.StatusServiceUnavailable || after.RevokedAt == nil || !after.RevokedAt.Equal(stamp) ||
		after.RevokedReason != "idle_timeout" || after.SessionCount != 4 || audit != 0 {
		t.Fatalf("write error must roll back registration, legacy audit and heartbeat: fired=%v response=%d row=%+v audit=%d", failedWrite, out.Code, after, audit)
	}
}

func TestHostAdmissionOrdinaryCredentialForms(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	r, db := newHostAdmissionRouter(t)
	const id = int32(1591)
	const key = "ordinary-format-fixture"
	seedHostAccount(t, db, id, key, 4)
	var user entity.User
	if e := db.First(&user, id).Error; e != nil {
		t.Fatal(e)
	}
	jwt, e := service.IssueUserLoginJWTWithDB(db, &user, "192.0.2.19", "")
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/user/device-admission"
	for i, form := range []string{"Bearer " + key, "Basic " + key, key} {
		out := hostFixtureRequest(r, "POST", path, form, "", fmt.Sprintf("{\"deviceId\":\"fixture-%d\",\"clientInstance\":\"local\"}", i))
		if out.Code != 200 {
			t.Fatalf("supported format[%d] status=%d", i, out.Code)
		}
	}
	probes := []struct {
		kind, auth, script, cookie string
		want                       int
	}{{"jwt", "Bearer " + jwt, "", "", 401}, {"cookie", "", "", jwt, 401}, {"script header", "Basic " + key, "synthetic-only", "", 403}}
	for _, p := range probes {
		req := httptest.NewRequest("POST", path, strings.NewReader("{\"deviceId\":\"fixture-unused\",\"clientInstance\":\"local\"}"))
		req.Header.Set("Authorization", p.auth)
		if p.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: p.cookie})
		}
		if p.script != "" {
			req.Header.Set("X-Script-Token", p.script)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != p.want {
			t.Fatalf("%s status=%d", p.kind, w.Code)
		}
	}
	var rows []entity.AccountDeviceBinding
	if e := db.Where("user_id=?", id).Find(&rows).Error; e != nil || len(rows) != 3 {
		t.Fatalf("unexpected registered count=%d err=%v", len(rows), e)
	}
	h := sha256.Sum256([]byte(key))
	var tok entity.AccessToken
	if e := db.Where("token_hash=?", hex.EncodeToString(h[:])).Take(&tok).Error; e != nil {
		t.Fatal(e)
	}
	for _, b := range rows {
		if b.UserID != id || b.TokenID != tok.ID || b.OwnerKind != "token" || b.SessionCount != 0 {
			t.Fatal("bad trusted attribution or session count")
		}
	}
	var c int64
	for _, m := range []any{&entity.ScriptRunSession{}, &entity.BillingRecord{}} {
		if e := db.Model(m).Count(&c).Error; e != nil || c != 0 {
			t.Fatal("unrelated rows")
		}
	}
	data, _ := json.Marshal(map[string]any{"accepted_forms": 3, "committed_bindings": len(rows), "jwt": 401, "cookie": 401, "script_header": 403})
	t.Log(string(data))
}

func TestHostAdmissionRealCommitFailureLeavesNoUncommittedStateAndPoolRecovers(t *testing.T) {
	if !hostAdmissionChild(t) {
		return
	}
	router, db := newHostAdmissionRouter(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Make reuse of the physical connection deterministic. This is a
	// fixture setting only, not an application pool change.
	pool.SetMaxOpenConns(1)
	seedHostAccount(t, db, 1592, "fixture-commit", 1)
	for _, q := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE review_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE review_deferred(id INTEGER, FOREIGN KEY(id) REFERENCES review_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TRIGGER review_after_valid_insert AFTER INSERT ON account_device_bindings WHEN NEW.device_id='fixture-commit-device' BEGIN INSERT INTO review_deferred(id) VALUES (919191); END",
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}

	const auth = "Bearer fixture-commit"
	const path = "/api/user/device-admission"
	failed := hostFixtureRequest(router, http.MethodPost, path, auth, "", `{"deviceId":"fixture-commit-device","clientInstance":"test-owned"}`)
	var poolCount int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("user_id=1592").Count(&poolCount).Error; err != nil {
		t.Fatal(err)
	}
	var database struct{ File string }
	if err := db.Raw("PRAGMA database_list").Scan(&database).Error; err != nil || database.File == "" {
		t.Fatal("missing SQLite fixture path")
	}
	independent, err := sql.Open("sqlite", database.File)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	var before, after, deferredCount int64
	if err := independent.QueryRow("SELECT COUNT(*) FROM account_device_bindings WHERE user_id=1592").Scan(&before); err != nil {
		t.Fatal(err)
	}
	next := hostFixtureRequest(router, http.MethodPost, path, auth, "", `{"deviceId":"fixture-healthy","clientInstance":"test-owned"}`)
	if err := independent.QueryRow("SELECT COUNT(*) FROM account_device_bindings WHERE user_id=1592 AND device_id='fixture-healthy'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := independent.QueryRow("SELECT COUNT(*) FROM review_deferred").Scan(&deferredCount); err != nil {
		t.Fatal(err)
	}
	t.Logf("commit_failed_http=%d former_pool_rows=%d independent_before=%d healthy_retry_http=%d healthy_committed=%d deferred=%d",
		failed.Code, poolCount, before, next.Code, after, deferredCount)
	if failed.Code != 503 || poolCount != 0 || before != 0 || next.Code != 200 || after != 1 || deferredCount != 0 {
		t.Fatal("failed commit must leave no binding or deferred row and the next valid request must succeed")
	}
}
