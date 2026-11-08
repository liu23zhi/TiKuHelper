package controller

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const s02LegacyIcon = `<svg onload="S02_INERT_LEGACY"/>`
const s02SafeIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="currentColor" d="M2 2h20v20H2z"/></svg>`

type s02BridgeEvent struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Actor  string `json:"actor"`
	Status int    `json:"status"`
}

type s02BridgeSnapshot struct {
	Rows         []entity.PaymentMethod `json:"rows"`
	Count        int                    `json:"count"`
	EnabledCount int                    `json:"enabledCount"`
	SHA256       string                 `json:"sha256"`
	ServiceCalls map[string]int         `json:"serviceCalls"`
	Events       []s02BridgeEvent       `json:"events"`
}

type s02PaymentIconBridge struct {
	db     *gorm.DB
	server *httptest.Server
	nonce  string
	stop   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	events []s02BridgeEvent
	calls  map[string]int
}

// TestS02PaymentIconNativeBridge is both a normal, offline backend regression and
// an optional loopback bridge for the native Vue/Antd integration runner. It never
// reads SQL_DSN, application DBs, manager state, credentials, or a payment provider.
// The only mocked boundary is identity: X-S02-Actor injects a synthetic user;
// these tests do not qualify real JWT, Cookie, middleware, or provider auth.
func TestS02PaymentIconNativeBridge(t *testing.T) {
	t.Run("backend-selfchecks", s02PaymentIconBackendSelfchecks)
	if t.Failed() {
		return
	}
	mode := os.Getenv("TIKU_S02_BRIDGE_MODE")
	if mode == "" {
		return
	}
	if mode != "ui" {
		t.Fatalf("unsupported TIKU_S02_BRIDGE_MODE %q", mode)
	}
	readyPath := os.Getenv("TIKU_S02_BRIDGE_READY_FILE")
	if readyPath == "" || !filepath.IsAbs(readyPath) {
		t.Fatal("UI mode requires an absolute TIKU_S02_BRIDGE_READY_FILE")
	}
	bridge := newS02PaymentIconBridge(t)
	ready, err := json.Marshal(map[string]string{"url": bridge.server.URL, "nonce": bridge.nonce})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, append(ready, '\n'), 0600); err != nil {
		t.Fatalf("write bridge readiness: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(readyPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove bridge readiness: %v", err)
		}
	})
	t.Logf("UI bridge ready at loopback URL %s; fixture identity only", bridge.server.URL)
	timer := time.NewTimer(180 * time.Second)
	defer timer.Stop()
	select {
	case <-bridge.stop:
		t.Log("UI bridge shutdown acknowledged")
	case <-timer.C:
		t.Fatal("UI bridge exceeded 180s without fixture shutdown")
	}
}

func newS02PaymentIconBridge(t *testing.T) *s02PaymentIconBridge {
	t.Helper()
	// t.TempDir is external disposable storage, not a repository/preview DB.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "s02-fixture.sqlite")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close fixture SQLite: %v", err)
		}
	})
	if err := db.AutoMigrate(&entity.PaymentMethod{}); err != nil {
		t.Fatal(err)
	}
	// Only this deliberate legacy row bypasses today's write validator. Its raw
	// value must survive list projection and the fixture disable-all operation.
	seed := []entity.PaymentMethod{
		{ID: 1, Name: "legacy-dangerous", IconSVG: s02LegacyIcon, EPayType: "alipay", Enabled: true, Sort: 10},
		{ID: 2, Name: "fixture-alipay", EPayType: "alipay", Enabled: true, Sort: 20},
		{ID: 3, Name: "fixture-disabled", EPayType: "wxpay", Enabled: false, Sort: 30},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	b := &s02PaymentIconBridge{
		db: db, nonce: hex.EncodeToString(nonce), stop: make(chan struct{}),
		calls: map[string]int{"list": 0, "create": 0, "update": 0, "delete": 0},
	}
	// Hooks only select the disposable DB. Every CRUD operation still executes
	// the real production service and validator, never an echoed payload.
	oldList, oldCreate := listPaymentMethods, createPaymentMethod
	oldUpdate, oldDelete := updatePaymentMethod, deletePaymentMethod
	t.Cleanup(func() {
		listPaymentMethods, createPaymentMethod = oldList, oldCreate
		updatePaymentMethod, deletePaymentMethod = oldUpdate, oldDelete
	})
	listPaymentMethods = func(user entity.User) ([]entity.PaymentMethod, error) {
		b.called("list")
		return service.ListPaymentMethodsWithDB(db, isAdminUser(&user))
	}
	createPaymentMethod = func(_ entity.User, input service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
		b.called("create")
		return service.CreatePaymentMethodWithDB(db, input)
	}
	updatePaymentMethod = func(_ entity.User, id int32, input service.UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
		b.called("update")
		return service.UpdatePaymentMethodWithDB(db, id, input)
	}
	deletePaymentMethod = func(_ entity.User, id int32) error {
		b.called("delete")
		return service.DeletePaymentMethodWithDB(db, id)
	}
	// Do not change global Gin mode or run in parallel: private hooks are global.
	router := gin.New()
	router.Use(func(c *gin.Context) {
		actor := c.GetHeader("X-S02-Actor")
		switch actor {
		case "admin":
			c.Set("user", &entity.User{ID: 9001, Role: "admin", Status: "enabled"})
		case "user":
			c.Set("user", &entity.User{ID: 9002, Role: "user", Status: "enabled"})
		default:
			actor = "anonymous"
		}
		c.Next()
		b.mu.Lock()
		b.events = append(b.events, s02BridgeEvent{c.Request.Method, c.Request.URL.Path, actor, c.Writer.Status()})
		b.mu.Unlock()
	})
	router.GET("/adapter-service/payment-methods", PaymentMethodList)
	router.POST("/adapter-service/payment-methods", CreatePaymentMethod)
	router.PUT("/adapter-service/payment-methods/:id", UpdatePaymentMethod)
	router.DELETE("/adapter-service/payment-methods/:id", DeletePaymentMethod)
	fixture := router.Group("/__fixture__")
	fixture.Use(func(c *gin.Context) {
		if c.GetHeader("X-S02-Nonce") != b.nonce {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "fixture nonce required"})
		}
	})
	fixture.GET("/snapshot", func(c *gin.Context) {
		snapshot, err := b.snapshot()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "fixture snapshot failed"})
			return
		}
		c.JSON(http.StatusOK, snapshot)
	})
	fixture.POST("/disable-all", func(c *gin.Context) {
		// UpdateColumn intentionally preserves icon_svg AND updated_at.
		result := db.Model(&entity.PaymentMethod{}).Where("enabled = ?", true).UpdateColumn("enabled", false)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "fixture disable failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"disabled": result.RowsAffected})
	})
	fixture.POST("/shutdown", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "fixture shutdown"})
		b.once.Do(func() { close(b.stop) })
	})
	// No order/pay/provider handlers are mounted. Unexpected writes are trapped,
	// and the event ledger makes an attempted payment write observable.
	router.NoRoute(func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			c.JSON(http.StatusTeapot, gin.H{"message": "no payment/provider writes in fixture bridge"})
		default:
			c.JSON(http.StatusNotFound, gin.H{"message": "fixture route not mounted"})
		}
	})
	b.server = httptest.NewServer(router) // net/http/httptest binds 127.0.0.1:0.
	t.Cleanup(b.server.Close)
	return b
}

func (b *s02PaymentIconBridge) called(operation string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[operation]++
}

func (b *s02PaymentIconBridge) snapshot() (s02BridgeSnapshot, error) {
	s := s02BridgeSnapshot{Rows: []entity.PaymentMethod{}, ServiceCalls: map[string]int{}, Events: []s02BridgeEvent{}}
	if err := b.db.Order("id asc").Find(&s.Rows).Error; err != nil {
		return s, err
	}
	raw, err := json.Marshal(s.Rows)
	if err != nil {
		return s, err
	}
	hash := sha256.Sum256(raw)
	s.SHA256 = hex.EncodeToString(hash[:])
	s.Count = len(s.Rows)
	for _, row := range s.Rows {
		if row.Enabled {
			s.EnabledCount++
		}
	}
	b.mu.Lock()
	for key, value := range b.calls {
		s.ServiceCalls[key] = value
	}
	s.Events = append(s.Events, b.events...)
	b.mu.Unlock()
	return s, nil
}

func s02PaymentIconBackendSelfchecks(t *testing.T) {
	b := newS02PaymentIconBridge(t)
	client := b.server.Client()
	client.Timeout = 5 * time.Second
	request := func(actor, method, path string, input any, want int) []byte {
		t.Helper()
		var body []byte
		if input != nil {
			var err error
			body, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, b.server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-S02-Actor", actor)
		req.Header.Set("X-S02-Nonce", b.nonce)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		output, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s actor=%s status=%d body=%s", method, path, actor, response.StatusCode, output)
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d", response.StatusCode, want)
		}
		return output
	}
	snapshot := func() s02BridgeSnapshot {
		t.Helper()
		s, err := b.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	list := func(actor string, wantCount int) []entity.PaymentMethod {
		t.Helper()
		var rows []entity.PaymentMethod
		if err := json.Unmarshal(request(actor, http.MethodGet, "/adapter-service/payment-methods", nil, 200), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != wantCount {
			t.Fatalf("list actor=%s count=%d want=%d", actor, len(rows), wantCount)
		}
		return rows
	}
	initial := snapshot()
	if initial.Count != 3 || initial.EnabledCount != 2 || initial.Rows[0].IconSVG != s02LegacyIcon {
		t.Fatalf("unexpected initial fixture: %+v", initial)
	}
	for _, actor := range []string{"admin", "user"} {
		want := 3
		if actor == "user" {
			want = 2
		}
		for _, row := range list(actor, want) {
			if row.ID == 1 && row.IconSVG != "" {
				t.Fatal("legacy dangerous SVG leaked through service projection")
			}
			if actor == "user" && !row.Enabled {
				t.Fatal("disabled row leaked to ordinary user")
			}
		}
	}
	request("anonymous", http.MethodGet, "/adapter-service/payment-methods", nil, 401)
	if snapshot().SHA256 != initial.SHA256 {
		t.Fatal("list projection mutated raw DB")
	}
	input := service.CreatePaymentMethodInput{
		Name: "native-safe-icon", EPayType: "alipay", IconSVG: s02SafeIcon, Enabled: true, Sort: 40, Remark: "isolated fixture",
	}
	var created entity.PaymentMethod
	if err := json.Unmarshal(request("admin", http.MethodPost, "/adapter-service/payment-methods", input, 200), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID <= 3 || created.IconSVG != s02SafeIcon {
		t.Fatalf("create did not return persisted safe icon: %+v", created)
	}
	path := "/adapter-service/payment-methods/" + strconv.Itoa(int(created.ID))
	if rows := list("user", 3); rows[len(rows)-1].IconSVG != s02SafeIcon {
		t.Fatal("safe SVG failed actual HTTP read-after-write")
	}
	persisted := snapshot()
	if persisted.Count != 4 || persisted.Rows[3].IconSVG != s02SafeIcon {
		t.Fatal("safe HTTP create not persisted in fixture SQLite")
	}
	for _, unsafe := range []string{
		`<svg onload="S02_INERT_NEW"/>`,
		`<svg><script>S02_INERT_NEW</script></svg>`,
		`<svg><foreignObject><div/></foreignObject></svg>`,
	} {
		bad := input
		bad.IconSVG = unsafe
		for _, verb := range []string{http.MethodPost, http.MethodPut} {
			target := "/adapter-service/payment-methods"
			if verb == http.MethodPut {
				target = path
			}
			request("admin", verb, target, bad, 400)
			if snapshot().SHA256 != persisted.SHA256 {
				t.Fatalf("unsafe %s changed raw DB", verb)
			}
		}
	}
	beforeUnauthorized := snapshot()
	for _, actor := range []string{"user", "anonymous"} {
		want := http.StatusForbidden
		if actor == "anonymous" {
			want = http.StatusUnauthorized
		}
		for _, verb := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			target := path
			if verb == http.MethodPost {
				target = "/adapter-service/payment-methods"
			}
			request(actor, verb, target, input, want)
		}
	}
	afterUnauthorized := snapshot()
	if afterUnauthorized.SHA256 != beforeUnauthorized.SHA256 ||
		!reflect.DeepEqual(afterUnauthorized.ServiceCalls, beforeUnauthorized.ServiceCalls) {
		t.Fatal("unauthorized requests entered a service write or changed DB")
	}
	updated := service.UpdatePaymentMethodInput{
		Name: "native-safe-updated", EPayType: input.EPayType, IconSVG: input.IconSVG,
		Enabled: true, Sort: 41, Remark: input.Remark,
	}
	request("admin", http.MethodPut, path, updated, 200)
	if row := snapshot().Rows[3]; row.Name != updated.Name || row.IconSVG != s02SafeIcon {
		t.Fatal("safe update did not persist")
	}
	request("admin", http.MethodDelete, path, nil, 200)
	if snapshot().SHA256 != initial.SHA256 {
		t.Fatal("delete did not restore original three fixture rows")
	}
	// Nonce gates fixture controls, independently of synthetic application role.
	req, err := http.NewRequest(http.MethodPost, b.server.URL+"/__fixture__/disable-all", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 403 {
		t.Fatalf("fixture control without nonce status=%d", response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot().SHA256 != initial.SHA256 {
		t.Fatal("unauthorized fixture control mutated DB")
	}
	request("admin", http.MethodPost, "/__fixture__/disable-all", nil, 200)
	disabled := snapshot()
	if disabled.EnabledCount != 0 || disabled.Rows[0].IconSVG != s02LegacyIcon {
		t.Fatal("disable-all changed legacy raw icon or left an enabled row")
	}
	list("user", 0)
	for i, row := range disabled.Rows {
		pristine := initial.Rows[i]
		pristine.Enabled = false
		if row != pristine {
			t.Fatal("disable-all changed a field other than enabled")
		}
	}
	request("admin", http.MethodPost, "/adapter-service/recharge/orders", nil, http.StatusTeapot)
	request("admin", http.MethodGet, "/__fixture__/snapshot", nil, 200)
	request("admin", http.MethodPost, "/__fixture__/shutdown", nil, 200)
	select {
	case <-b.stop:
	default:
		t.Fatal("shutdown endpoint did not release bridge")
	}
	t.Logf("S02_BACKEND_PASS initialRows=%d safeCreateID=%d unsafeWrites=6 unauthorizedWrites=6 rawLegacyPreserved=true disabledUserRows=0 fixtureOnlyAuth=true", initial.Count, created.ID)
	t.Logf("raw row hash after disabled state: %s", disabled.SHA256)
}
