package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

const s01CompositionSchema = "S01_PRODUCTION_AUTHENTICATION_COMPOSITION_V1"

var s01CompositionNames = []string{
	"anonymous_user_info", "login_issues_cookie", "cookiejar_same_origin_user_info",
	"cookiejar_no_metadata_user_info", "cookiejar_foreign_origin_rejected",
	"bearer_access_token_foreign_health", "invalid_bearer_has_no_cookie_fallback",
	"quota_exhausted_search", "enabled_limiter_exhaustion", "preflight_after_limiter_exhaustion",
}

type s01CompositionNoDial struct{ calls *atomic.Int64 }

func (n s01CompositionNoDial) RoundTrip(*http.Request) (*http.Response, error) {
	n.calls.Add(1)
	return nil, errors.New("S01_COMPOSITION_BUSINESS_NETWORK_FORBIDDEN")
}

type s01CompositionLoopback struct {
	host           string
	transport      *http.Transport
	calls          int
	cookieRequests int
}

func (n *s01CompositionLoopback) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != n.host || r.Host != n.host {
		return nil, errors.New("S01_COMPOSITION_CLIENT_TARGET_FORBIDDEN")
	}
	n.calls++
	if r.Header.Get("Cookie") != "" {
		n.cookieRequests++
	}
	return n.transport.RoundTrip(r)
}

func s01CompositionWrite(path string, value any) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errors.New("S01_COMPOSITION_LEDGER_MUST_BE_ABSOLUTE")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("S01_COMPOSITION_LEDGER_ENCODING_FAILED")
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func s01CompositionSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Only this test's worker initializes application singletons. The parent never
// calls manager/JWT/Viper to inspect them; process isolation preserves them.
func TestS01ProductionAuthenticationComposition(t *testing.T) {
	if os.Getenv("S01_COMPOSITION_WORKER") == "1" {
		s01CompositionWorker(t)
		return
	}
	priorQ := dao.Q
	var priorQuery dao.Query
	if priorQ != nil {
		priorQuery = *priorQ
	}
	priorLog, priorTiku, priorUser := dao.Log, dao.Tiku, dao.User
	priorTransport, priorMode := http.DefaultTransport, gin.Mode()
	priorWriter, priorErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	priorEnv := os.Environ()
	priorCWD, err := os.Getwd()
	if err != nil {
		t.Fatal("cannot inspect parent working directory")
	}
	workerRoot := t.TempDir()
	workerLedger := filepath.Join(workerRoot, "worker-ledger.json")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("cannot locate isolated test executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestS01ProductionAuthenticationComposition$", "-test.count=1", "-test.timeout=65s")
	cmd.Dir = workerRoot
	// No inherited business environment, proxy, database DSN, provider keys or .env.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + workerRoot, "TMPDIR=" + workerRoot,
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "GOMAXPROCS=2",
		"S01_COMPOSITION_WORKER=1", "S01_COMPOSITION_ROOT=" + workerRoot,
		"S01_COMPOSITION_LEDGER=" + workerLedger,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	parentChecks := map[string]bool{
		"daoPointer":                             dao.Q == priorQ,
		"daoQuery":                               dao.Q == nil && priorQ == nil || dao.Q != nil && priorQ != nil && reflect.DeepEqual(*dao.Q, priorQuery),
		"daoAliases":                             dao.Log == priorLog && dao.Tiku == priorTiku && dao.User == priorUser,
		"transport":                              reflect.DeepEqual(http.DefaultTransport, priorTransport),
		"ginMode":                                gin.Mode() == priorMode,
		"ginWriters":                             reflect.DeepEqual(gin.DefaultWriter, priorWriter) && reflect.DeepEqual(gin.DefaultErrorWriter, priorErrorWriter),
		"environment":                            reflect.DeepEqual(os.Environ(), priorEnv),
		"appSingletonFunctionsNotCalledInParent": true,
	}
	cwd, cwdErr := os.Getwd()
	parentChecks["cwd"] = cwdErr == nil && cwd == priorCWD
	parentUnchanged := true
	for _, ok := range parentChecks {
		parentUnchanged = parentUnchanged && ok
	}
	ledger := map[string]any{"schema": s01CompositionSchema, "passed": false, "syntheticOnly": true, "nativeBrowser": false, "CSRFProof": false}
	data, readErr := os.ReadFile(workerLedger)
	if readErr == nil {
		readErr = json.Unmarshal(data, &ledger)
	}
	resources, ok := ledger["resources"].(map[string]any)
	if !ok {
		resources = map[string]any{}
		ledger["resources"] = resources
	}
	resources["parentUnchanged"] = parentUnchanged
	resources["parentChecks"] = parentChecks
	resources["workerExited"] = runErr == nil
	ledger["workerOutput"] = map[string]any{
		"stdoutBytes": stdout.Len(), "stderrBytes": stderr.Len(),
		"stdoutSHA256": s01CompositionSHA(stdout.Bytes()), "stderrSHA256": s01CompositionSHA(stderr.Bytes()),
		"rawOutputNotPrinted": true,
	}
	workerPassed, _ := ledger["passed"].(bool)
	cases, casesOK := ledger["cases"].([]any)
	exactCases := casesOK && len(cases) == len(s01CompositionNames)
	if exactCases {
		for i, item := range cases {
			c, ok := item.(map[string]any)
			exactCases = exactCases && ok && c["name"] == s01CompositionNames[i] && c["passed"] == true
		}
	}
	passed := readErr == nil && runErr == nil && workerPassed && parentUnchanged && exactCases
	ledger["passed"] = passed
	if err := s01CompositionWrite(os.Getenv("S01_COMPOSITION_LEDGER"), ledger); err != nil {
		t.Error("cannot write absolute composition result ledger")
	}
	t.Logf("S01_COMPOSITION cases=%d exactCases=%t parentUnchanged=%t workerPassed=%t rawOutputNotPrinted=true", len(cases), exactCases, parentUnchanged, workerPassed)
	if !passed {
		t.Error("S01 composition or isolated cleanup failed; inspect redacted boolean ledger, never raw credential responses")
	}
}

func s01CompositionWorker(t *testing.T) {
	root := os.Getenv("S01_COMPOSITION_ROOT")
	cwd, err := os.Getwd()
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(cwd) != filepath.Clean(root) {
		t.Fatal("worker is not in its explicit owned temporary directory")
	}
	networkCalls := new(atomic.Int64)
	priorTransport, priorMode := http.DefaultTransport, gin.Mode()
	priorWriter, priorErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	http.DefaultTransport = s01CompositionNoDial{networkCalls}
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter, gin.DefaultErrorWriter = io.Discard, io.Discard
	ledger := map[string]any{
		"schema": s01CompositionSchema, "passed": false, "syntheticOnly": true, "nativeBrowser": false, "CSRFProof": false,
		"cases": []any{}, "network": map[string]any{}, "resources": map[string]any{},
	}
	resources := ledger["resources"].(map[string]any)
	ledger["lastStage"] = "owned_configuration"
	var mgr *manager.Manager
	var sqlPing func() error
	var server *httptest.Server
	var loopback *s01CompositionLoopback
	var transport *http.Transport
	defer func() {
		resources["clientTransportIdleConnectionsClosed"] = transport == nil
		if transport != nil {
			transport.CloseIdleConnections()
			resources["clientTransportIdleConnectionsClosed"] = true
		}
		resources["TLSHTTPServerClosed"] = server == nil
		if server != nil {
			server.Close()
			// Probe only the exact owned loopback listener after synchronous close.
			conn, closeProbeErr := net.DialTimeout("tcp", server.Listener.Addr().String(), 100*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
			}
			resources["TLSHTTPServerClosed"] = closeProbeErr != nil
		}
		resources["managerClosed"] = mgr == nil
		resources["SQLClosed"] = mgr == nil
		if mgr != nil {
			resources["managerClosed"] = mgr.CloseManager() == nil
			resources["SQLClosed"] = sqlPing != nil && sqlPing() != nil
		}
		resources["loggerClosed"] = logger.Close() == nil
		http.DefaultTransport = priorTransport
		gin.SetMode(priorMode)
		gin.DefaultWriter, gin.DefaultErrorWriter = priorWriter, priorErrorWriter
		resources["workerTransportRestored"] = reflect.DeepEqual(http.DefaultTransport, priorTransport)
		resources["workerGinRestored"] = gin.Mode() == priorMode && reflect.DeepEqual(gin.DefaultWriter, priorWriter) && reflect.DeepEqual(gin.DefaultErrorWriter, priorErrorWriter)
		netState := ledger["network"].(map[string]any)
		netState["businessOutboundCalls"] = networkCalls.Load()
		netState["clientExactLoopbackOnly"] = true
		if loopback != nil {
			netState["clientRequests"] = loopback.calls
			netState["cookieJarRequests"] = loopback.cookieRequests
		}
		passed := !t.Failed() && networkCalls.Load() == 0 && len(ledger["cases"].([]any)) == len(s01CompositionNames)
		for _, v := range resources {
			if b, ok := v.(bool); ok {
				passed = passed && b
			}
		}
		ledger["passed"] = passed
		if err := s01CompositionWrite(os.Getenv("S01_COMPOSITION_LEDGER"), ledger); err != nil {
			t.Error("worker redacted ledger could not be written")
		}
	}()
	dbPath := filepath.Join(root, "fixture.db")
	configPath := filepath.Join(root, "config.yaml")
	configText := fmt.Sprintf("database:\n  path: %q\nmysql: \"\"\napi: []\nlimit:\n  enable: true\n  duration: 3600\n  requests: 12\njwtSecret: \"S01_COMPOSITION_ONLY_PUBLIC_SYNTHETIC_KEY_20261029\"\nelasticsearch:\n  addresses: []\noss: {}\nsqpProxy:\n  enabled: false\nrecordEmptyAnswer: false\n", dbPath)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal("cannot create owned worker configuration")
	}
	cfg, err := registry.ConfigWithError()
	if err != nil {
		t.Fatal("cannot parse owned worker configuration")
	}
	resolvedPath, pathErr := filepath.Abs(cfg.Database.Path)
	if pathErr != nil || resolvedPath != dbPath || cfg.Mysql != "" || os.Getenv("SQL_DSN") != "" || len(cfg.API) != 0 || len(cfg.Elasticsearch.Addresses) != 0 || cfg.OSS.EndPoint != "" || cfg.OSS.AccessKeyID != "" || cfg.OSS.AccessKeySecret != "" || cfg.OSS.BucketName != "" || !cfg.Limit.Enable || cfg.Limit.Requests != 12 {
		t.Fatal("worker configuration is not isolated SQLite with external providers absent")
	}
	for _, e := range os.Environ() {
		name := strings.ToUpper(strings.SplitN(e, "=", 2)[0])
		if strings.HasPrefix(name, "TIKU_") || name == "SQL_DSN" || strings.Contains(name, "MYSQL") || strings.Contains(name, "UAPI") || strings.Contains(name, "PROXY") {
			t.Fatal("unexpected business environment in isolated worker")
		}
	}
	// A preseeded synthetic admin satisfies normal startup policy without changing
	// application mode or reading any production bootstrap environment.
	file, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal("cannot create new owned SQLite file")
	}
	if err := file.Close(); err != nil {
		t.Fatal("cannot close SQLite placeholder")
	}
	seed, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: glog.Default.LogMode(glog.Silent)})
	if err != nil {
		t.Fatal("cannot open owned bootstrap database")
	}
	seedSQL, err := seed.DB()
	if err != nil {
		t.Fatal("cannot access owned bootstrap database")
	}
	if err := seed.AutoMigrate(&entity.User{}); err != nil {
		_ = seedSQL.Close()
		t.Fatal("cannot migrate synthetic bootstrap user")
	}
	password := "S01-Fixture-Password-123!"
	passwordHash, err := util.HashPassword(password)
	if err != nil {
		_ = seedSQL.Close()
		t.Fatal("cannot prepare synthetic password")
	}
	admin := entity.User{ID: 1, Username: "s01-bootstrap-only", Password: passwordHash, Role: "admin", Status: "enabled", RegisterSource: "system", Balance: 10}
	if err := seed.Create(&admin).Error; err != nil {
		_ = seedSQL.Close()
		t.Fatal("cannot seed isolated bootstrap user")
	}
	if err := seedSQL.Close(); err != nil {
		t.Fatal("cannot close bootstrap SQL")
	}
	resources["bootstrapSQLClosed"] = seedSQL.Ping() != nil
	ledger["lastStage"] = "application_manager"
	*logger.LogDir = filepath.Join(root, "logs")
	mgr, err = manager.CreateManager()
	if err != nil || mgr == nil {
		t.Fatal("cannot initialize isolated application manager")
	}
	db := mgr.GetDB()
	if db == nil || db.Dialector.Name() != "sqlite" || mgr.GetConfig().Database.Path != dbPath {
		t.Fatal("application manager did not bind the owned SQLite")
	}
	resources["ownedConfigAndSQLiteOnly"] = true
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("cannot access application SQL")
	}
	sqlPing = sqlDB.Ping
	db = db.Session(&gorm.Session{Logger: glog.Default.LogMode(glog.Silent)})
	user := entity.User{ID: 1101, Username: "s01-composition-user", Password: passwordHash, Role: "user", Status: "enabled", RegisterSource: "system", Balance: 10}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal("cannot seed composition user")
	}
	positiveValue, exhaustedValue := "S01_SYNTHETIC_BEARER_POSITIVE", "S01_SYNTHETIC_BEARER_EXHAUSTED"
	makeToken := func(id int32, value string, used float64) entity.AccessToken {
		sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
		return entity.AccessToken{ID: id, UserID: user.ID, Name: "s01-composition-key", TokenHash: hex.EncodeToString(sum[:]), Status: "enabled", TotalQuotaLimit: 1, TotalAmountUsed: used}
	}
	positive, exhausted := makeToken(1201, positiveValue, 0), makeToken(1202, exhaustedValue, 1)
	if err := db.Create(&positive).Error; err != nil {
		t.Fatal("cannot seed allowed synthetic token")
	}
	if err := db.Create(&exhausted).Error; err != nil {
		t.Fatal("cannot seed exhausted synthetic token")
	}
	ledger["lastStage"] = "actual_captcha_verification"
	answer := "AB12CD"
	answerHash := sha256.Sum256([]byte(answer))
	challenge := entity.CaptchaChallenge{ChallengeID: "s01-composition-challenge", Scene: service.CaptchaSceneLoginPassword, AnswerHash: hex.EncodeToString(answerHash[:]), Status: service.CaptchaChallengeStatusPending, RequestIP: "127.0.0.1", ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&challenge).Error; err != nil {
		t.Fatal("cannot seed synthetic captcha challenge")
	}
	captcha, err := service.VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, service.CaptchaSceneLoginPassword, answer, "127.0.0.1")
	if err != nil {
		t.Fatal("actual synthetic captcha verification failed")
	}
	ledger["lastStage"] = "TLS_same_application_requests"
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal("cannot disable fixture proxy trust")
	}
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Vary", "Accept-Encoding")
		c.Next()
	})
	SetAPIRouter(r)
	server = httptest.NewTLSServer(r)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("cannot parse exact TLS loopback endpoint")
	}
	baseTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected TLS fixture client transport")
	}
	transport = baseTransport.Clone()
	transport.DisableKeepAlives = true
	transport.Proxy, transport.DialTLS, transport.DialTLSContext = nil, nil, nil
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || address != endpoint.Host || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("S01_COMPOSITION_DIAL_TARGET_FORBIDDEN")
		}
		return dialer.DialContext(ctx, network, address)
	}
	loopback = &s01CompositionLoopback{host: endpoint.Host, transport: transport}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("cannot create standard cookie jar")
	}
	client := &http.Client{Transport: loopback, Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, origin, authorization string, payload []byte) (int, http.Header, []byte) {
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal("cannot construct bounded fixture request")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, Idempotency-Key")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("bounded TLS fixture request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal("cannot consume bounded fixture response")
		}
		return response.StatusCode, response.Header, body
	}
	cors := func(h http.Header, hasOrigin bool) bool {
		want := ""
		if hasOrigin {
			want = "*"
		}
		return h.Get("Access-Control-Allow-Origin") == want && h.Get("Access-Control-Allow-Credentials") == "" && h.Get("Access-Control-Allow-Headers") == "Authorization, Content-Type, Idempotency-Key" && h.Get("Access-Control-Expose-Headers") == "X-Request-ID, Retry-After" && h.Get("Access-Control-Allow-Methods") == "GET, POST, PUT, DELETE, OPTIONS" && h.Get("Vary") == "Accept-Encoding"
	}
	record := func(name string, status, want int, checks bool) {
		passed := status == want && checks
		ledger["cases"] = append(ledger["cases"].([]any), map[string]any{"name": name, "status": status, "passed": passed})
		if !passed {
			t.Errorf("composition case %s failed at HTTP%d", name, status)
		}
	}
	status, headers, _ := request("GET", "/adapter-service/user/info", "", "", nil)
	record("anonymous_user_info", status, 401, cors(headers, false))
	payload, err := json.Marshal(map[string]string{"username": user.Username, "password": password, "captchaToken": captcha.Token})
	if err != nil {
		t.Fatal("cannot encode synthetic login")
	}
	status, headers, _ = request("POST", "/adapter-service/user/login", server.URL, "", payload)
	issued := (&http.Response{Header: headers}).Cookies()
	cookiePolicy := len(issued) == 1 && issued[0].Name == "tiku_token" && issued[0].HttpOnly && issued[0].Secure && issued[0].SameSite == http.SameSiteLaxMode && issued[0].Path == "/" && issued[0].Domain == "" && issued[0].MaxAge == 86400
	record("login_issues_cookie", status, 200, cors(headers, true) && cookiePolicy && len(jar.Cookies(endpoint)) == 1)
	isUser := func(body []byte) bool {
		var decoded struct {
			ID int32 `json:"id"`
		}
		return json.Unmarshal(body, &decoded) == nil && decoded.ID == user.ID
	}
	beforeCookie := loopback.cookieRequests
	status, headers, body := request("GET", "/adapter-service/user/info", server.URL, "", nil)
	record("cookiejar_same_origin_user_info", status, 200, cors(headers, true) && isUser(body) && loopback.cookieRequests == beforeCookie+1)
	status, headers, body = request("GET", "/adapter-service/user/info", "", "", nil)
	record("cookiejar_no_metadata_user_info", status, 200, cors(headers, false) && isUser(body))
	foreign := "https://s01-foreign.fixture.invalid"
	status, headers, _ = request("GET", "/adapter-service/user/info", foreign, "", nil)
	record("cookiejar_foreign_origin_rejected", status, 403, cors(headers, true))
	status, headers, _ = request("GET", "/adapter-service/search/health", foreign, "Bearer "+positiveValue, nil)
	record("bearer_access_token_foreign_health", status, 200, cors(headers, true))
	status, headers, _ = request("GET", "/adapter-service/search/health", server.URL, "Bearer S01_SYNTHETIC_INVALID", nil)
	record("invalid_bearer_has_no_cookie_fallback", status, 401, cors(headers, true))
	var billsBefore, billsAfter int64
	if err := db.Model(&entity.BillingRecord{}).Count(&billsBefore).Error; err != nil {
		t.Fatal("cannot count synthetic billing baseline")
	}
	status, headers, body = request("POST", "/adapter-service/search", foreign, "Bearer "+exhaustedValue, []byte(`{"question":"S01_SYNTHETIC_QUOTA","type":4,"options":[]}`))
	var denial struct {
		Code string `json:"code"`
	}
	codeMatches := json.Unmarshal(body, &denial) == nil && denial.Code == "ACCESS_TOKEN_QUOTA_EXCEEDED"
	var currentUser entity.User
	var currentToken entity.AccessToken
	if err := db.First(&currentUser, user.ID).Error; err != nil {
		t.Fatal("cannot inspect synthetic wallet state")
	}
	if err := db.First(&currentToken, exhausted.ID).Error; err != nil {
		t.Fatal("cannot inspect synthetic quota state")
	}
	if err := db.Model(&entity.BillingRecord{}).Count(&billsAfter).Error; err != nil {
		t.Fatal("cannot count synthetic billing outcome")
	}
	unchangedFinance := billsAfter == billsBefore && currentUser.Balance == user.Balance && currentToken.TotalAmountUsed == exhausted.TotalAmountUsed
	record("quota_exhausted_search", status, 401, cors(headers, true) && codeMatches && unchangedFinance && networkCalls.Load() == 0)
	limited := false
	attempts := 0
	for attempts < 14 {
		attempts++
		status, headers, _ = request("GET", "/adapter-service/search/health", foreign, "Bearer "+positiveValue, nil)
		if status == 429 {
			limited = true
			break
		}
		if status != 200 {
			t.Error("bounded limiter control had unexpected non-200 before limit")
			break
		}
	}
	record("enabled_limiter_exhaustion", status, 429, limited && cors(headers, true))
	ledger["limiterAttempts"] = attempts
	status, headers, body = request("OPTIONS", "/adapter-service/search", foreign, "", nil)
	record("preflight_after_limiter_exhaustion", status, 204, cors(headers, true) && len(body) == 0)
	ledger["financeUnchangedOnRejectedNewSpending"] = unchangedFinance
	ledger["cookiePolicyObservedWithoutValues"] = cookiePolicy
	ledger["credentialsNeverCopiedToOutput"] = true
}
