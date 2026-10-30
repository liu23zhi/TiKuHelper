package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func clearExtensionSettingsEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"EXTENSION_CACHE_DIR", "EXTENSION_UPDATE_INTERVAL", "EXTENSION_UPDATE_ENABLED", "EXTENSION_EDGE_ID", "EXTENSION_CHROME_ID"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtensionSettingsPersistDatabaseAndRestart(t *testing.T) {
	clearExtensionSettingsEnvironment(t)
	db := newSearchAccountConfigTestDB(t)
	directory := t.TempDir()
	t.Setenv("EXTENSION_CACHE_DIR", directory)
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: directory, Interval: time.Hour, IDs: map[string]string{"edge": DefaultEdgeExtensionID}})
	if err != nil {
		t.Fatal(err)
	}
	want := ExtensionUpdateSettings{AutoUpdate: false, IntervalMinutes: 7, EdgeID: strings.Repeat("b", 32), ChromeID: strings.Repeat("c", 32)}
	configuration, err := store.UpdateSettingsWithDB(db, want)
	if err != nil || configuration.Config != want {
		t.Fatalf("save: %+v %v", configuration, err)
	}
	restarted, err := newConfiguredExtensionStore(db)
	if err != nil || restarted.Configuration().Config != want {
		t.Fatalf("restart database fallback: %v %v", restarted, err)
	}
	var count int64
	db.Model(&entity.SystemSetting{}).Where("key = ?", extensionSettingsKey).Count(&count)
	if count != 1 {
		t.Fatalf("not one atomic system-setting document: %d", count)
	}
	before := store.Configuration()
	for _, invalid := range []ExtensionUpdateSettings{{IntervalMinutes: 0}, {IntervalMinutes: int(maxExtensionIntervalMinutes + 1)}, {IntervalMinutes: 5, EdgeID: "private.invalid"}, {IntervalMinutes: 5, ChromeID: strings.Repeat("z", 32)}} {
		if _, err := store.UpdateSettingsWithDB(db, invalid); !errors.Is(err, ErrInvalidExtensionSettings) {
			t.Fatalf("invalid settings accepted: %+v %v", invalid, err)
		}
	}
	if !reflect.DeepEqual(before, store.Configuration()) {
		t.Fatal("invalid save changed live settings")
	}
	if err := db.Migrator().DropTable(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	want.IntervalMinutes = 8
	if _, err := store.UpdateSettingsWithDB(db, want); err == nil {
		t.Fatal("database failure accepted")
	}
	if !reflect.DeepEqual(before, store.Configuration()) {
		t.Fatal("database failure changed live settings")
	}
}

func TestExtensionSettingsAcceptsIntervalsAboveTenDays(t *testing.T) {
	for _, minutes := range []int{14401, 43200, 525600, int(maxExtensionIntervalMinutes)} {
		settings, err := normalizeExtensionSettings(ExtensionUpdateSettings{IntervalMinutes: minutes})
		if err != nil {
			t.Fatalf("interval %d should be accepted: %v", minutes, err)
		}
		config := ExtensionStoreConfig{}
		applyExtensionSettings(&config, settings)
		if got := int(config.Interval / time.Minute); got != minutes {
			t.Fatalf("interval %d converted to %d minutes", minutes, got)
		}
	}
}

func TestExtensionSettingsEnvironmentOverridesEachFieldAndSurvivesSave(t *testing.T) {
	baseline := ExtensionUpdateSettings{AutoUpdate: false, IntervalMinutes: 9, EdgeID: strings.Repeat("b", 32), ChromeID: strings.Repeat("c", 32)}
	updated := ExtensionUpdateSettings{AutoUpdate: true, IntervalMinutes: 13, EdgeID: strings.Repeat("e", 32), ChromeID: strings.Repeat("f", 32)}
	for _, test := range []struct {
		name      string
		env       map[string]string
		overrides ExtensionEnvironmentOverrides
	}{
		{"explicit false", map[string]string{"EXTENSION_UPDATE_ENABLED": "false"}, ExtensionEnvironmentOverrides{AutoUpdate: true}},
		{"explicit true", map[string]string{"EXTENSION_UPDATE_ENABLED": "true"}, ExtensionEnvironmentOverrides{AutoUpdate: true}},
		{"interval only", map[string]string{"EXTENSION_UPDATE_INTERVAL": "17m"}, ExtensionEnvironmentOverrides{IntervalMinutes: true}},
		{"empty edge disables", map[string]string{"EXTENSION_EDGE_ID": ""}, ExtensionEnvironmentOverrides{EdgeID: true}},
		{"empty chrome disables", map[string]string{"EXTENSION_CHROME_ID": ""}, ExtensionEnvironmentOverrides{ChromeID: true}},
		{"chrome only", map[string]string{"EXTENSION_CHROME_ID": strings.Repeat("d", 32)}, ExtensionEnvironmentOverrides{ChromeID: true}},
		{"mixed fields", map[string]string{"EXTENSION_UPDATE_ENABLED": "false", "EXTENSION_UPDATE_INTERVAL": "17m", "EXTENSION_EDGE_ID": strings.Repeat("d", 32)}, ExtensionEnvironmentOverrides{AutoUpdate: true, IntervalMinutes: true, EdgeID: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearExtensionSettingsEnvironment(t)
			t.Setenv("EXTENSION_CACHE_DIR", t.TempDir())
			db := newSearchAccountConfigTestDB(t)
			store, err := newConfiguredExtensionStore(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateSettingsWithDB(db, baseline); err != nil {
				t.Fatal(err)
			}
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			store, err = newConfiguredExtensionStore(db)
			if err != nil {
				t.Fatal(err)
			}
			wantEffective := func(saved ExtensionUpdateSettings) ExtensionUpdateSettings {
				if test.overrides.AutoUpdate {
					saved.AutoUpdate = test.env["EXTENSION_UPDATE_ENABLED"] == "true"
				}
				if test.overrides.IntervalMinutes {
					saved.IntervalMinutes = 17
				}
				if test.overrides.EdgeID {
					saved.EdgeID = test.env["EXTENSION_EDGE_ID"]
				}
				if test.overrides.ChromeID {
					saved.ChromeID = test.env["EXTENSION_CHROME_ID"]
				}
				return saved
			}
			assertConfiguration := func(config ExtensionConfiguration, saved ExtensionUpdateSettings) {
				t.Helper()
				if config.Config != wantEffective(saved) || config.DatabaseConfig != saved || config.EnvironmentOverrides != test.overrides {
					t.Fatalf("incorrect field precedence: %+v; database=%+v overrides=%+v", config, saved, test.overrides)
				}
			}
			assertConfiguration(store.Configuration(), baseline)
			configuration, err := store.UpdateSettingsWithDB(db, updated)
			if err != nil {
				t.Fatal(err)
			}
			assertConfiguration(configuration, updated)
			restarted, err := newConfiguredExtensionStore(db)
			if err != nil {
				t.Fatal(err)
			}
			assertConfiguration(restarted.Configuration(), updated)
			for name := range test.env {
				if err := os.Unsetenv(name); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err = newConfiguredExtensionStore(db)
			if err != nil || restarted.Configuration().Config != updated || restarted.Configuration().EnvironmentOverrides != (ExtensionEnvironmentOverrides{}) {
				t.Fatalf("removed environment did not restore database: %+v %v", restarted, err)
			}
		})
	}
}

func TestExtensionSettingsInvalidEnvironmentCannotBeHiddenByDatabase(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"EXTENSION_UPDATE_ENABLED", ""}, {"EXTENSION_UPDATE_ENABLED", "unknown"},
		{"EXTENSION_UPDATE_INTERVAL", ""}, {"EXTENSION_UPDATE_INTERVAL", "not-a-duration"}, {"EXTENSION_UPDATE_INTERVAL", "153722868m"},
		{"EXTENSION_EDGE_ID", "invalid"}, {"EXTENSION_CHROME_ID", strings.Repeat("z", 32)},
	} {
		t.Run(test.name+"="+test.value, func(t *testing.T) {
			clearExtensionSettingsEnvironment(t)
			t.Setenv("EXTENSION_CACHE_DIR", t.TempDir())
			db := newSearchAccountConfigTestDB(t)
			store, err := newConfiguredExtensionStore(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateSettingsWithDB(db, ExtensionUpdateSettings{IntervalMinutes: 5}); err != nil {
				t.Fatal(err)
			}
			t.Setenv(test.name, test.value)
			if store, err := newConfiguredExtensionStore(db); err == nil || store != nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("invalid environment hidden by saved settings: store=%v error=%v", store, err)
			}
		})
	}
}

func TestExtensionSettingsDefaultsWithoutDatabase(t *testing.T) {
	clearExtensionSettingsEnvironment(t)
	t.Setenv("EXTENSION_CACHE_DIR", t.TempDir())
	t.Setenv("EXTENSION_UPDATE_ENABLED", "false")
	t.Setenv("EXTENSION_EDGE_ID", "")
	store, err := newConfiguredExtensionStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration := store.Configuration()
	wantDatabase := ExtensionUpdateSettings{AutoUpdate: true, IntervalMinutes: 14400, EdgeID: DefaultEdgeExtensionID}
	wantEffective := ExtensionUpdateSettings{IntervalMinutes: 14400}
	if configuration.Config != wantEffective || configuration.DatabaseConfig != wantDatabase || configuration.EnvironmentOverrides != (ExtensionEnvironmentOverrides{AutoUpdate: true, EdgeID: true}) {
		t.Fatalf("default precedence: %+v", configuration)
	}
}

func TestExtensionSettingsEnvironmentIdentitySurvivesDatabaseSave(t *testing.T) {
	clearExtensionSettingsEnvironment(t)
	t.Setenv("EXTENSION_CACHE_DIR", t.TempDir())
	fixture := newExtensionFixture(t)
	t.Setenv("EXTENSION_EDGE_ID", fixture.id)
	db := newSearchAccountConfigTestDB(t)
	store, err := newConfiguredExtensionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	data := fixture.build(t, "1.0", nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		response := data
		if r.URL.Path != "/package" {
			close(entered)
			<-release
			response = []byte(fmt.Sprintf(`<gupdate><app appid="%s" status="ok"><updatecheck status="ok" version="1.0" codebase="https://edge.microsoft.com/package" hash_sha256="%s"/></app></gupdate>`, fixture.id, extensionSHA256(data)))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response)), Request: r}, nil
	})
	done := make(chan error, 1)
	go func() { done <- store.Refresh(context.Background(), "edge") }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("environment identity check did not start")
	}
	saved := ExtensionUpdateSettings{IntervalMinutes: 5, EdgeID: strings.Repeat("b", 32)}
	configuration, err := store.UpdateSettingsWithDB(db, saved)
	if err != nil || configuration.Config.EdgeID != fixture.id || configuration.DatabaseConfig != saved || !configuration.Sources[0].Updating {
		t.Fatalf("database replaced an environment-owned in-flight identity: %+v %v", configuration, err)
	}
	unblock.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatalf("environment-owned check was cancelled: %v", err)
	}
	current, err := store.Latest("edge")
	if err != nil || current.ExtensionID != fixture.id {
		t.Fatalf("published database identity instead of environment: %+v %v", current, err)
	}
	restarted, err := newConfiguredExtensionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if cached, err := restarted.Latest("edge"); err != nil || cached.ExtensionID != fixture.id {
		t.Fatalf("environment identity did not survive cache reload: %+v %v", cached, err)
	}
}

func TestExtensionSettingsCancelOldIdentityBeforePublishing(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	oldFixture, newFixture := newExtensionFixture(t), newExtensionFixture(t)
	oldPackage, newPackage := oldFixture.build(t, "1.0", nil), newFixture.build(t, "2.0", nil)
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": oldFixture.id}})
	if err != nil {
		t.Fatal(err)
	}
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		data := newPackage
		if r.URL.Path != "/old" && r.URL.Path != "/new" {
			if strings.Contains(r.URL.Query().Get("x"), oldFixture.id) {
				once.Do(func() { close(oldEntered) })
				// Deliberately ignore cancellation to verify the final publication guard.
				<-oldRelease
				data = []byte(fmt.Sprintf(`<gupdate><app appid="%s" status="ok"><updatecheck status="ok" version="1.0" codebase="https://edge.microsoft.com/old" hash_sha256="%s"/></app></gupdate>`, oldFixture.id, extensionSHA256(oldPackage)))
			} else {
				data = []byte(fmt.Sprintf(`<gupdate><app appid="%s" status="ok"><updatecheck status="ok" version="2.0" codebase="https://edge.microsoft.com/new" hash_sha256="%s"/></app></gupdate>`, newFixture.id, extensionSHA256(newPackage)))
			}
		} else if r.URL.Path == "/old" {
			data = oldPackage
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})
	oldDone := make(chan error, 1)
	go func() { oldDone <- store.Refresh(context.Background(), "edge") }()
	select {
	case <-oldEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("old check did not start")
	}
	config := ExtensionUpdateSettings{IntervalMinutes: 10, EdgeID: newFixture.id}
	if _, err := store.UpdateSettingsWithDB(db, config); err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	close(oldRelease)
	if err := <-oldDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("old owner should be cancelled: %v", err)
	}
	release, err := store.Latest("edge")
	if err != nil || release.ExtensionID != newFixture.id || release.Version != "2.0" {
		t.Fatalf("old worker replaced new identity: %+v %v", release, err)
	}
	restarted, err := NewExtensionStore(store.config)
	if err != nil {
		t.Fatal(err)
	}
	if persisted, err := restarted.Latest("edge"); err != nil || persisted.ExtensionID != newFixture.id {
		t.Fatalf("old worker replaced disk metadata: %+v %v", persisted, err)
	}
	config.EdgeID = ""
	if _, err := store.UpdateSettingsWithDB(db, config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Latest("edge"); !errors.Is(err, ErrExtensionSourceNotConfigured) {
		t.Fatalf("disabled source remains public: %v", err)
	}
	if status := store.Configuration().Sources[0]; status.Available || status.Updating || status.Configured {
		t.Fatalf("disabled status: %+v", status)
	}
}

func TestExtensionSettingsWakeDisabledSchedulerWithoutRestart(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	fixture := newExtensionFixture(t)
	data := fixture.build(t, "1.0", nil)
	upstream := &extensionHTTPFixture{id: fixture.id, version: "1.0", data: data, hash: extensionSHA256(data), entered: make(chan struct{})}
	store := newExtensionHTTPStore(t, upstream, t.TempDir())
	store.config.AutoUpdate = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); store.Run(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-upstream.entered:
		t.Fatal("disabled scheduler started network traffic")
	case <-time.After(30 * time.Millisecond):
	}
	settings := ExtensionUpdateSettings{AutoUpdate: true, IntervalMinutes: 5, EdgeID: fixture.id}
	if _, err := store.UpdateSettingsWithDB(db, settings); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstream.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("live settings did not wake scheduler")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := store.Configuration().Sources[0]
		if status.Available && !status.Updating && status.LastCheckedAt != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("live scheduled refresh did not publish a ready status")
}
