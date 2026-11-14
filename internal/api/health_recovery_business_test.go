package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

const b03RecoveryChildKey = "B03_HEALTH_RECOVERY_CHILD"
const b03RecoveryRootKey = "B03_HEALTH_RECOVERY_ROOT"
const b03RecoveryMarker = "B03_SAFE_OBSERVATION "
const b03RecoveryPassword = "B03-Only-Synthetic-Strong-Password!29"
const b03RecoverySite = "SYNTHETIC_B03_PUBLIC_SITE"

type b03RecoveryStep struct {
	Phase           string `json:"phase"`
	Kind            string `json:"kind"`
	HTTPStatus      int    `json:"httpStatus"`
	PublicStatus    string `json:"publicStatus,omitempty"`
	RegisterEnabled bool   `json:"registerEnabled,omitempty"`
	SiteMatches     bool   `json:"siteMatches,omitempty"`
}

type b03RecoveryObservation struct {
	Contract            string            `json:"contract"`
	Stage               string            `json:"stage"`
	Passed              bool              `json:"passed"`
	SyntheticProbeFault bool              `json:"syntheticProbeFault"`
	SameRouter          bool              `json:"sameRouter"`
	SameSQLPool         bool              `json:"sameSQLPool"`
	SeedClosed          bool              `json:"seedClosed"`
	ManagerClosed       bool              `json:"managerClosed"`
	LoggerClosed        bool              `json:"loggerClosed"`
	ProbeCalls          int64             `json:"probeCalls"`
	RealSQLPings        int64             `json:"realSQLPings"`
	BoundedDeadlines    int64             `json:"boundedDeadlines"`
	NetworkDialAttempts int64             `json:"networkDialAttempts"`
	Steps               []b03RecoveryStep `json:"steps"`
}

// This one new qualifier is CI-independent: child globals/cwd/env are isolated,
// and transport Dial is denied even when no container network namespace is used.
func TestTasklistAPIHealthRecoveryWithRealPublicBusiness(t *testing.T) {
	if marker := os.Getenv(b03RecoveryChildKey); marker != "" {
		if marker != t.Name() {
			t.Fatal("unexpected isolated-child marker")
		}
		b03RecoveryRunChild(t)
		return
	}
	dir := t.TempDir()
	for _, name := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal("cannot create owned child directories")
		}
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal("cannot prepare synthetic JWT material")
	}
	jwt := hex.EncodeToString(seed)
	dbPath := filepath.Join(dir, "b03-only-fixture.sqlite")
	config := fmt.Sprintf("jwtSecret: %q\nmysql: \"\"\ndatabase:\n  path: %q\nelasticsearch:\n  addresses: []\noss:\n  endpoint: \"\"\n  accessKeyID: \"\"\n  accessKeySecret: \"\"\n  bucketName: \"\"\napi: []\nsqpProxy:\n  enabled: false\nlimit:\n  enable: true\n  duration: 60\n  requests: 120\n", jwt, dbPath)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal("cannot write owned synthetic configuration")
	}
	if err := os.WriteFile(filepath.Join(dir, ".b03-owned"), []byte("B03_OWNED_ONLY\n"), 0600); err != nil {
		t.Fatal("cannot mark owned child directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot resolve current public test binary")
	}
	child := exec.CommandContext(ctx, executable, "-test.run=^"+t.Name()+"$", "-test.v")
	child.Dir = dir
	child.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(dir, "home"), "LANG=C.UTF-8", "TZ=UTC",
		b03RecoveryChildKey + "=" + t.Name(), b03RecoveryRootKey + "=" + dir,
	}
	// ⚠️ 整体替换 Env，所以平台必需项要给全。
	//
	// Windows 的 GetTempPath 只认 TMP → TEMP → USERPROFILE，**不看 TMPDIR**
	// （那是 POSIX 的名字）。本行原来只给 TMPDIR，于是子进程的 t.TempDir()
	// 退化成 `mkdir C:\WINDOWS\...: Access is denied`，子进程在 owned_setup
	// 阶段就失败（观测里 loggerClosed:true、probeCalls:0）—— 那不是被测行为，
	// 是夹具缺陷。实测：只设 TMPDIR 时 os.TempDir() = "C:\Users\<user>"。
	if runtime.GOOS == "windows" {
		child.Env = append(child.Env,
			"TEMP="+filepath.Join(dir, "tmp"), "TMP="+filepath.Join(dir, "tmp"),
			"SystemRoot="+os.Getenv("SystemRoot"),
			"USERPROFILE="+filepath.Join(dir, "home"),
		)
	} else {
		child.Env = append(child.Env, "TMPDIR="+filepath.Join(dir, "tmp"))
	}
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	err = child.Run()
	for _, stream := range [][]byte{stdout.Bytes(), stderr.Bytes()} {
		if bytes.Contains(stream, []byte(b03RecoveryPassword)) || bytes.Contains(stream, []byte(jwt)) {
			t.Fatal("child stream exposed fixture-only private material; raw stream suppressed")
		}
	}
	var observation b03RecoveryObservation
	reports := 0
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, b03RecoveryMarker) {
			if decodeErr := json.Unmarshal([]byte(strings.TrimPrefix(line, b03RecoveryMarker)), &observation); decodeErr != nil {
				t.Fatal("invalid safe child observation")
			}
			reports++
		}
	}
	if reports == 1 {
		body, _ := json.Marshal(observation)
		t.Logf("%s%s", b03RecoveryMarker, body)
	}
	if err != nil || ctx.Err() != nil || reports != 1 || !observation.Passed {
		a, b := sha256.Sum256(stdout.Bytes()), sha256.Sum256(stderr.Bytes())
		t.Fatalf("isolated qualifier failed; safeStage=%s stdoutSHA=%x stderrSHA=%x", observation.Stage, a, b)
	}
}

func b03RecoveryRunChild(t *testing.T) {
	t.Helper()
	obs := b03RecoveryObservation{Contract: "B03-health-public-flags-v1", Stage: "owned_setup", SyntheticProbeFault: true}
	var mgr *manager.Manager
	var seedSQL *sql.DB
	var probeCalls, sqlPings, deadlines, dialAttempts atomic.Int64
	complete := false
	defer func() {
		if seedSQL != nil {
			obs.SeedClosed = seedSQL.Close() == nil
		}
		if mgr != nil {
			obs.ManagerClosed = mgr.CloseManager() == nil
		}
		obs.LoggerClosed = logger.Close() == nil
		obs.ProbeCalls, obs.RealSQLPings = probeCalls.Load(), sqlPings.Load()
		obs.BoundedDeadlines, obs.NetworkDialAttempts = deadlines.Load(), dialAttempts.Load()
		if !obs.SeedClosed || !obs.ManagerClosed || !obs.LoggerClosed || obs.NetworkDialAttempts != 0 {
			t.Error("owned child cleanup or network boundary failed")
		}
		obs.Passed = complete && !t.Failed()
		body, _ := json.Marshal(obs)
		fmt.Printf("%s%s\n", b03RecoveryMarker, body)
	}()
	root := os.Getenv(b03RecoveryRootKey)
	wd, err := os.Getwd()
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(wd) != filepath.Clean(root) {
		t.Fatal("child cwd is not the owned absolute directory")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("owned root is invalid")
	}
	marker, err := os.ReadFile(filepath.Join(root, ".b03-owned"))
	if err != nil || string(marker) != "B03_OWNED_ONLY\n" {
		t.Fatal("owned marker is missing")
	}
	// ⚠️ 白名单必须与父侧实际下发的 env **逐项对齐**。
	//
	// Windows 那几项不是可有可无：整体替换 Env 之后，Windows 的 GetTempPath
	// 只认 TMP → TEMP → USERPROFILE（不看 TMPDIR），缺了它们子进程的
	// t.TempDir() 会退化成 `mkdir C:\WINDOWS\...: Access is denied`；
	// SystemRoot 缺失还会让一些系统调用找不到 DLL。
	//
	// 但这不削弱本用例的判据：白名单仍然是**封闭集合**，任何一项多出来的
	// 环境变量（真实配置、凭据、父测试的全局状态）依旧会被拒绝。
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "TZ": true,
		b03RecoveryChildKey: true, b03RecoveryRootKey: true,
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"TEMP", "TMP", "SystemRoot", "USERPROFILE"} {
			allowed[name] = true
		}
	}
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if !allowed[name] {
			t.Fatal("unexpected environment in isolated child")
		}
	}
	if manager.GetManager() != nil {
		t.Fatal("child manager was not fresh")
	}
	denyDial := func(context.Context, string, string) (net.Conn, error) {
		dialAttempts.Add(1)
		return nil, errors.New("B03_REAL_NETWORK_FORBIDDEN")
	}
	transport := &http.Transport{Proxy: nil, DialContext: denyDial, DialTLSContext: denyDial}
	http.DefaultTransport = transport
	http.DefaultClient = &http.Client{Transport: transport}
	// Keep deny installed until child exit; do not reopen a cleanup network window.
	gin.SetMode(gin.TestMode)
	viper.Reset()
	*logger.LogDir = filepath.Join(root, "logs")
	dbPath := filepath.Join(root, "b03-only-fixture.sqlite")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal("cannot create exclusive owned SQLite")
	}
	if err := f.Close(); err != nil {
		t.Fatal("cannot close owned SQLite placeholder")
	}
	seedDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: glog.Default.LogMode(glog.Silent)})
	if err != nil {
		t.Fatal("cannot open synthetic bootstrap SQLite")
	}
	seedSQL, err = seedDB.DB()
	if err != nil || seedDB.AutoMigrate(&entity.User{}) != nil {
		t.Fatal("cannot prepare owned bootstrap schema")
	}
	hash, err := util.HashPassword(b03RecoveryPassword)
	if err != nil {
		t.Fatal("cannot prepare synthetic bcrypt")
	}
	if err := seedDB.Create(&entity.User{ID: 1, Username: "b03-bootstrap-only", Password: hash, Role: "admin", Status: "enabled", RegisterSource: "system"}).Error; err != nil {
		t.Fatal("cannot seed isolated administrator")
	}
	if err := seedSQL.Close(); err != nil {
		t.Fatal("cannot close bootstrap SQL")
	}
	seedSQL = nil
	obs.SeedClosed = true
	obs.Stage = "application_manager"
	mgr, err = manager.CreateManager()
	if err != nil || mgr == nil {
		t.Fatal("cannot initialize isolated application manager")
	}
	db := mgr.GetDB()
	if db == nil || db.Dialector.Name() != "sqlite" || mgr.GetConfig().Database.Path != dbPath {
		t.Fatal("manager did not bind owned SQLite")
	}
	quiet := db.Session(&gorm.Session{NewDB: true, Logger: glog.Default.LogMode(glog.Silent)})
	if _, err := service.SetRegisterEnabledWithDB(quiet, true); err != nil {
		t.Fatal("cannot set owned public register flag")
	}
	setting := entity.SystemSetting{Key: service.SiteNameSettingKey}
	if err := quiet.Where("key = ?", setting.Key).Assign(entity.SystemSetting{Value: b03RecoverySite}).FirstOrCreate(&setting).Error; err != nil {
		t.Fatal("cannot set owned public site marker")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("cannot access owned application SQL")
	}
	b03RecoveryPhases(t, mgr, sqlDB, &obs, &probeCalls, &sqlPings, &deadlines)
	if dialAttempts.Load() != 0 {
		t.Fatal("unexpected network dial attempt")
	}
	obs.Stage = "complete"
	complete = true
}

func b03RecoveryPhases(t *testing.T, mgr *manager.Manager, sqlDB *sql.DB, obs *b03RecoveryObservation, calls, pings, deadlines *atomic.Int64) {
	t.Helper()
	var fault atomic.Bool
	probe := func(ctx context.Context) error {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > time.Second {
			return errors.New("B03_PROBE_DEADLINE_INVALID")
		}
		deadlines.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if fault.Load() {
			return errors.New("B03_SYNTHETIC_DEPENDENCY_UNAVAILABLE")
		}
		pings.Add(1)
		return sqlDB.PingContext(ctx)
	}
	router := gin.New()
	SetAPIRouter(router)
	SetHealthRouter(router, probe)
	index := []byte("<html><body>B03-owned-SPA-fixture</body></html>")
	SetWebRouter(embed.FS{}, index, router)
	identity := router
	for _, phase := range []struct {
		name  string
		fault bool
	}{{"healthy", false}, {"synthetic_fault", true}, {"recovered", false}} {
		obs.Stage = phase.name
		fault.Store(phase.fault)
		currentSQL, err := mgr.GetDB().DB()
		if err != nil || currentSQL != sqlDB || router != identity {
			t.Fatal("same router and owned SQL pool were not retained")
		}
		obs.SameRouter, obs.SameSQLPool = true, true
		before := calls.Load()
		b03RecoveryHealth(t, router, "/healthz", http.StatusOK, "ok")
		if calls.Load() != before {
			t.Fatal("liveness unexpectedly invoked readiness dependency")
		}
		obs.Steps = append(obs.Steps, b03RecoveryStep{Phase: phase.name, Kind: "liveness", HTTPStatus: http.StatusOK, PublicStatus: "ok"})
		code, status := http.StatusOK, "ready"
		if phase.fault {
			code, status = http.StatusServiceUnavailable, "not_ready"
		}
		b03RecoveryHealth(t, router, "/readyz", code, status)
		if calls.Load() != before+1 || deadlines.Load() != calls.Load() {
			t.Fatal("probe call count or cooperative deadline was invalid")
		}
		obs.Steps = append(obs.Steps, b03RecoveryStep{Phase: phase.name, Kind: "readiness", HTTPStatus: code, PublicStatus: status})
		if !phase.fault {
			b03RecoveryBusiness(t, router)
			obs.Steps = append(obs.Steps, b03RecoveryStep{Phase: phase.name, Kind: "real_public_business", HTTPStatus: http.StatusOK, RegisterEnabled: true, SiteMatches: true})
		}
	}
	if calls.Load() != 3 || pings.Load() != 2 || deadlines.Load() != 3 {
		t.Fatal("unexpected three-phase dependency accounting")
	}
	spa := b03RecoveryServe(router, "/adapter/tiku")
	if spa.Code != http.StatusOK || !strings.HasPrefix(spa.Header().Get("Content-Type"), "text/html") || !bytes.Equal(spa.Body.Bytes(), index) {
		t.Fatal("minimal existing SPA fallback positive failed")
	}
	obs.Steps = append(obs.Steps, b03RecoveryStep{Phase: "recovered", Kind: "synthetic_SPA_index_only", HTTPStatus: http.StatusOK})
}

func b03RecoveryServe(router *gin.Engine, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func b03RecoveryHealth(t *testing.T, router *gin.Engine, path string, wantCode int, wantStatus string) {
	t.Helper()
	response := b03RecoveryServe(router, path)
	if response.Code != wantCode || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("health response status/content/cache contract failed")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body) != 1 {
		t.Fatal("health response was not sanitized single-status JSON")
	}
	var status string
	if err := json.Unmarshal(body["status"], &status); err != nil || status != wantStatus {
		t.Fatal("health public status did not match phase")
	}
}

func b03RecoveryBusiness(t *testing.T, router *gin.Engine) {
	t.Helper()
	response := b03RecoveryServe(router, "/api/public/register-status")
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatal("real public business route did not return JSON200")
	}
	var public service.PublicAuthSettings
	var shape map[string]json.RawMessage
	if json.Unmarshal(response.Body.Bytes(), &public) != nil || json.Unmarshal(response.Body.Bytes(), &shape) != nil {
		t.Fatal("real public business JSON could not decode")
	}
	if !public.RegisterEnabled || public.SiteName != b03RecoverySite || len(shape) < 2 {
		t.Fatal("real public business response did not read seeded owned settings")
	}
	for _, name := range []string{"apiKey", "wechatApiBearerToken", "smsbaoSecret", "epayMerchantPrivateKey", "jwtSecret", "mysql", "database", "status"} {
		if _, found := shape[name]; found {
			t.Fatal("public business JSON exposed forbidden fields or masqueraded as health")
		}
	}
}
