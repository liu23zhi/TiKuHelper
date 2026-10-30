package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestExtensionSettingsPersistOverrideEnvironmentAndRestart(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	directory := t.TempDir()
	t.Setenv("EXTENSION_CACHE_DIR", directory)
	t.Setenv("EXTENSION_UPDATE_INTERVAL", "invalid-env-but-database-wins")
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
		t.Fatalf("restart database priority: %v %v", restarted, err)
	}
	var count int64
	db.Model(&entity.SystemSetting{}).Where("key = ?", extensionSettingsKey).Count(&count)
	if count != 1 {
		t.Fatalf("not one atomic system-setting document: %d", count)
	}
	before := store.Configuration()
	for _, invalid := range []ExtensionUpdateSettings{{IntervalMinutes: 0}, {IntervalMinutes: 10081}, {IntervalMinutes: 5, EdgeID: "private.invalid"}, {IntervalMinutes: 5, ChromeID: strings.Repeat("z", 32)}} {
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
