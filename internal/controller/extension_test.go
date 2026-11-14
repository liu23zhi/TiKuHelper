package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type extensionControllerFixture struct {
	release      service.ExtensionRelease
	data         []byte
	err          error
	refreshCalls int
}

func TestExtensionConfigurationRequiresAdminAndPersistsLiveSettings(t *testing.T) {
	db := newUserControllerTestDB(t)
	users := []entity.User{{ID: 751, Username: "extension-settings-admin", Role: "admin", Status: "enabled", Password: "hash-a"}, {ID: 752, Username: "extension-settings-user", Role: "user", Status: "enabled", Password: "hash-b"}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	admin, user := util.GenerateJwtForUser(users[0].ID, users[0].Password), util.GenerateJwtForUser(users[1].ID, users[1].Password)
	store, err := service.NewExtensionStore(service.ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": service.DefaultEdgeExtensionID}})
	if err != nil {
		t.Fatal(err)
	}
	originalStore, originalDB := extensionSettingsStore, extensionSettingsDB
	extensionSettingsStore = func() (*service.ExtensionStore, error) { return store, nil }
	extensionSettingsDB = func() *gorm.DB { return db }
	t.Cleanup(func() { extensionSettingsStore, extensionSettingsDB = originalStore, originalDB })
	router := gin.New()
	router.GET("/extensions/config", Auth, middleware.RequireAdmin, ExtensionConfiguration)
	router.PUT("/extensions/config", Auth, middleware.RequireAdmin, UpdateExtensionConfiguration)
	body := `{"autoUpdate":true,"intervalMinutes":20160,"edgeId":"","chromeId":"abcdefghijklmnopabcdefghijklmnop"}`
	request := func(method, authorization, payload string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/extensions/config", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authorization)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, test := range []struct {
			token  string
			status int
		}{{"", 401}, {user, 403}, {"Bearer " + admin, 401}} {
			if got := request(method, test.token, body); got.Code != test.status {
				t.Fatalf("%s unauthorized code=%d body=%s", method, got.Code, got.Body.String())
			}
		}
	}
	if got := request(http.MethodPut, admin, body); got.Code != 200 {
		t.Fatalf("save %d %s", got.Code, got.Body.String())
	}
	got := request(http.MethodGet, admin, "")
	var configuration service.ExtensionConfiguration
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &configuration) != nil || configuration.Config.IntervalMinutes != 20160 || configuration.Config.EdgeID != "" || len(configuration.Sources) != 2 || configuration.Sources[0].Configured || !configuration.Sources[1].Configured {
		t.Fatalf("saved contract %d %s", got.Code, got.Body.String())
	}
	if configuration.DatabaseConfig != configuration.Config || configuration.EnvironmentOverrides != (service.ExtensionEnvironmentOverrides{}) || !strings.Contains(got.Body.String(), `"environmentOverrides":{"autoUpdate":false,"intervalMinutes":false,"edgeId":false,"chromeId":false}`) || strings.Contains(got.Body.String(), "Directory") || strings.Contains(got.Body.String(), "directory") {
		t.Fatalf("configuration origin contract leaked paths or lost explicit override flags: %s", got.Body.String())
	}
	for _, invalid := range []string{`{}`, `{"autoUpdate":true,"intervalMinutes":1.5,"edgeId":"","chromeId":""}`, `{"autoUpdate":true,"intervalMinutes":0,"edgeId":"","chromeId":""}`, `{"autoUpdate":true,"intervalMinutes":5,"edgeId":"https://private.invalid","chromeId":""}`} {
		if got := request(http.MethodPut, admin, invalid); got.Code != 400 {
			t.Fatalf("invalid accepted %d %s", got.Code, got.Body.String())
		}
	}
	if !store.Configuration().Config.AutoUpdate || store.Configuration().Config.IntervalMinutes != 20160 {
		t.Fatal("invalid request mutated live settings")
	}
}

func (store *extensionControllerFixture) Latest(source string) (service.ExtensionRelease, error) {
	if store.err != nil {
		return service.ExtensionRelease{}, store.err
	}
	if source != "edge" {
		return service.ExtensionRelease{}, service.ErrExtensionSourceNotConfigured
	}
	return store.release, nil
}

func (store *extensionControllerFixture) Download(source, hash string) (service.ExtensionRelease, []byte, error) {
	release, err := store.Latest(source)
	if err != nil {
		return release, nil, err
	}
	if hash != "" && hash != release.SHA256 {
		return release, nil, service.ErrExtensionChanged
	}
	return release, store.data, nil
}

func (store *extensionControllerFixture) Refresh(ctx context.Context, source string) error {
	store.refreshCalls++
	_, err := store.Latest(source)
	return err
}

func extensionControllerRouter(t *testing.T) (*gin.Engine, *extensionControllerFixture) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fixture := &extensionControllerFixture{data: []byte("test-crx"), release: service.ExtensionRelease{Source: "edge", ExtensionID: service.DefaultEdgeExtensionID, Name: "超星学习助手", Version: "6.2.0", Size: 8, SHA256: strings.Repeat("a", 64), DownloadURL: "/api/extensions/edge/download?sha256=" + strings.Repeat("a", 64), CheckedAt: time.Now(), UpdatedAt: time.Now()}}
	original := extensionStore
	extensionStore = func() (extensionPackageStore, error) { return fixture, nil }
	t.Cleanup(func() { extensionStore = original })
	router := gin.New()
	router.GET("/extensions/:source/latest", ExtensionLatest)
	router.GET("/extensions/:source/download", ExtensionDownload)
	router.POST("/extensions/:source/refresh", Auth, middleware.RequireAdmin, RefreshExtension)
	return router, fixture
}

func TestExtensionPublicMetadataDownloadContract(t *testing.T) {
	router, fixture := extensionControllerRouter(t)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/extensions/edge/latest", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata: %d %s", recorder.Code, recorder.Body.String())
	}
	var release service.ExtensionRelease
	if err := json.Unmarshal(recorder.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	expectedJSON, _ := json.Marshal(fixture.release)
	actualJSON, _ := json.Marshal(release)
	if string(expectedJSON) != string(actualJSON) {
		t.Fatalf("metadata lost fields: %+v", release)
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/extensions/edge/download?sha256="+release.SHA256, nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(fixture.data) || recorder.Header().Get("Content-Type") != "application/x-chrome-extension" || recorder.Header().Get("ETag") != `"`+release.SHA256+`"` || recorder.Header().Get("X-Extension-Id") != release.ExtensionID || recorder.Header().Get("X-Extension-Version") != release.Version {
		t.Fatalf("download contract: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	for target, want := range map[string]int{"/extensions/edge/download?sha256=old": 409, "/extensions/chrome/latest": 404, "/extensions/unknown/download": 404} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != want {
			t.Errorf("%s: %d want %d", target, recorder.Code, want)
		}
	}
	if fixture.refreshCalls != 0 {
		t.Fatal("public GET triggered upstream side effects")
	}
}

func TestExtensionControllerErrorsArePrivate(t *testing.T) {
	router, fixture := extensionControllerRouter(t)
	for _, test := range []struct {
		err    error
		status int
	}{{service.ErrExtensionUnavailable, 503}, {errors.New(`open C:\private\data\secret.json: permission denied`), 502}} {
		fixture.err = test.err
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/extensions/edge/latest", nil))
		if recorder.Code != test.status || strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), test.err.Error()) {
			t.Fatalf("leaked diagnostic: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestExtensionRefreshRequiresRealAdministratorJWT(t *testing.T) {
	router, fixture := extensionControllerRouter(t)
	db := newUserControllerTestDB(t)
	users := []entity.User{{ID: 741, Username: "extension-admin", Role: "admin", Status: "enabled", Password: "not-a-password"}, {ID: 742, Username: "extension-user", Role: "user", Status: "enabled", Password: "not-a-password"}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	admin := util.GenerateJwtForUser(users[0].ID, users[0].Password)
	user := util.GenerateJwtForUser(users[1].ID, users[1].Password)
	for _, test := range []struct {
		name, authorization string
		status              int
	}{{"anonymous", "", 401}, {"ordinary user", user, 403}, {"bearer is not account JWT", "Bearer " + admin, 401}, {"administrator", admin, 200}} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/extensions/edge/refresh", nil)
			request.Header.Set("Authorization", test.authorization)
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("auth: %d want %d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
	if fixture.refreshCalls != 1 {
		t.Fatalf("unauthorized upstream refreshes: %d", fixture.refreshCalls)
	}
}
