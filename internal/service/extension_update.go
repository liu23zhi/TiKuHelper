package service

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var (
	ErrExtensionSourceNotConfigured = errors.New("extension source is not configured")
	ErrExtensionUnavailable         = errors.New("no verified extension package is cached")
	ErrExtensionChanged             = errors.New("extension version changed; request latest metadata again")
)

const DefaultEdgeExtensionID = "lcmeenlnjihhjcjoakekikoaldigonac"

type ExtensionStoreConfig struct {
	Directory  string
	Interval   time.Duration
	AutoUpdate bool
	IDs        map[string]string
}

type ExtensionRelease struct {
	Source      string    `json:"source"`
	ExtensionID string    `json:"extensionId"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	DownloadURL string    `json:"downloadUrl"`
	CheckedAt   time.Time `json:"checkedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type cachedExtension struct {
	release ExtensionRelease
	data    []byte
}

type extensionCheck struct {
	done   chan struct{}
	err    error
	cancel context.CancelFunc
}

type ExtensionStore struct {
	config               ExtensionStoreConfig
	databaseConfig       ExtensionUpdateSettings
	environmentConfig    ExtensionStoreConfig
	environmentOverrides ExtensionEnvironmentOverrides
	client               *http.Client
	mu                   sync.RWMutex
	cache                map[string]cachedExtension
	checks               map[string]*extensionCheck
	statuses             map[string]ExtensionSourceStatus
	wake                 chan struct{}
}

func ExtensionStoreConfigFromEnv() (ExtensionStoreConfig, error) {
	config, _, err := extensionStoreConfigFromEnv()
	return config, err
}

func extensionStoreConfigFromEnv() (ExtensionStoreConfig, ExtensionEnvironmentOverrides, error) {
	config := ExtensionStoreConfig{
		Directory:  strings.TrimSpace(os.Getenv("EXTENSION_CACHE_DIR")),
		Interval:   240 * time.Hour,
		AutoUpdate: true,
		IDs:        map[string]string{"edge": DefaultEdgeExtensionID},
	}
	var overrides ExtensionEnvironmentOverrides
	if config.Directory == "" {
		config.Directory = filepath.Join("data", "extensions")
	}
	if id, ok := os.LookupEnv("EXTENSION_EDGE_ID"); ok {
		overrides.EdgeID = true
		config.IDs["edge"] = strings.TrimSpace(id)
		if config.IDs["edge"] != "" && !validExtensionID(config.IDs["edge"]) {
			return config, overrides, errors.New("EXTENSION_EDGE_ID must be 32 a-p letters or empty to disable")
		}
	}
	if id, ok := os.LookupEnv("EXTENSION_CHROME_ID"); ok {
		overrides.ChromeID = true
		config.IDs["chrome"] = strings.TrimSpace(id)
		if config.IDs["chrome"] != "" && !validExtensionID(config.IDs["chrome"]) {
			return config, overrides, errors.New("EXTENSION_CHROME_ID must be 32 a-p letters or empty to disable")
		}
	}
	if value, ok := os.LookupEnv("EXTENSION_UPDATE_INTERVAL"); ok {
		overrides.IntervalMinutes = true
		interval, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || interval < time.Minute {
			return config, overrides, errors.New("EXTENSION_UPDATE_INTERVAL must be at least 1m and fit in time.Duration")
		}
		config.Interval = interval
	}
	if value, ok := os.LookupEnv("EXTENSION_UPDATE_ENABLED"); ok {
		overrides.AutoUpdate = true
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return config, overrides, errors.New("EXTENSION_UPDATE_ENABLED must be a boolean")
		}
		config.AutoUpdate = enabled
	}
	return config, overrides, nil
}

func NewExtensionStore(config ExtensionStoreConfig) (*ExtensionStore, error) {
	if config.Directory == "" || config.Interval <= 0 {
		return nil, errors.New("invalid extension cache configuration")
	}
	ids := make(map[string]string)
	for source, id := range config.IDs {
		if source != "edge" && source != "chrome" {
			return nil, errors.New("unsupported extension source")
		}
		if id != "" && !validExtensionID(id) {
			return nil, fmt.Errorf("invalid %s extension ID", source)
		}
		if id != "" {
			ids[source] = id
		}
	}
	config.IDs = ids
	store := &ExtensionStore{config: config, databaseConfig: extensionSettingsFromConfig(config), cache: make(map[string]cachedExtension), checks: make(map[string]*extensionCheck), statuses: make(map[string]ExtensionSourceStatus), wake: make(chan struct{}, 1)}
	store.client = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedExtensionURL(req.URL, "") {
			return errors.New("untrusted extension redirect")
		}
		if req.URL.Scheme == "http" && (len(via) == 0 || via[0].URL.Scheme != "http" || via[0].URL.Host != req.URL.Host) {
			return errors.New("extension redirect downgrade rejected")
		}
		return nil
	}}
	for source := range ids {
		// Invalid or incomplete on-disk caches never become public; a future refresh
		// replaces them. This does not block startup or remove previous artifacts.
		if cached, err := store.load(source); err == nil {
			store.cache[source] = cached
		}
	}
	return store, nil
}

func (s *ExtensionStore) Latest(source string) (ExtensionRelease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config.IDs[source] == "" {
		return ExtensionRelease{}, ErrExtensionSourceNotConfigured
	}
	cached, ok := s.cache[source]
	if !ok {
		return ExtensionRelease{}, ErrExtensionUnavailable
	}
	return cached.release, nil
}

func (s *ExtensionStore) Download(source, expectedHash string) (ExtensionRelease, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config.IDs[source] == "" {
		return ExtensionRelease{}, nil, ErrExtensionSourceNotConfigured
	}
	cached, ok := s.cache[source]
	if !ok {
		return ExtensionRelease{}, nil, ErrExtensionUnavailable
	}
	if expectedHash != "" && expectedHash != cached.release.SHA256 {
		return ExtensionRelease{}, nil, ErrExtensionChanged
	}
	return cached.release, bytes.Clone(cached.data), nil
}

// Concurrent callers share exactly one bounded upstream operation per source.
func (s *ExtensionStore) Refresh(ctx context.Context, source string) error {
	s.mu.Lock()
	id := s.config.IDs[source]
	if id == "" {
		s.mu.Unlock()
		return ErrExtensionSourceNotConfigured
	}
	if check := s.checks[source]; check != nil {
		s.mu.Unlock()
		select {
		case <-check.done:
			return check.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	check := &extensionCheck{done: make(chan struct{}), cancel: cancel}
	s.checks[source] = check
	s.mu.Unlock()
	defer cancel()
	err := s.refresh(ctx, source, id)
	s.mu.Lock()
	check.err = err
	if s.checks[source] == check {
		delete(s.checks, source)
		now := time.Now()
		status := ExtensionSourceStatus{LastCheckedAt: &now}
		if err != nil {
			status.LastError = "扩展检查失败，请重试或查看服务日志"
		}
		s.statuses[source] = status
	}
	close(check.done)
	s.mu.Unlock()
	return err
}

func (s *ExtensionStore) Run(ctx context.Context, report func(string, error)) {
	check := func() {
		for _, source := range []string{"edge", "chrome"} {
			if ctx.Err() != nil {
				return
			}
			s.mu.RLock()
			configured := s.config.AutoUpdate && s.config.IDs[source] != ""
			s.mu.RUnlock()
			if !configured {
				continue
			}
			err := s.Refresh(ctx, source)
			if report != nil {
				report(source, err)
			}
		}
	}
	for {
		select {
		case <-s.wake:
		default:
		}
		s.mu.RLock()
		enabled, interval := s.config.AutoUpdate, s.config.Interval
		s.mu.RUnlock()
		if enabled {
			check()
		}
		var timer *time.Timer
		var next <-chan time.Time
		if enabled {
			timer = time.NewTimer(interval)
			next = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.wake:
		case <-next:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

type extensionUpdateResponse struct {
	XMLName xml.Name `xml:"gupdate"`
	Apps    []struct {
		ID     string `xml:"appid,attr"`
		Status string `xml:"status,attr"`
		Update struct {
			Status  string `xml:"status,attr"`
			URL     string `xml:"codebase,attr"`
			Version string `xml:"version,attr"`
			Hash    string `xml:"hash_sha256,attr"`
		} `xml:"updatecheck"`
	} `xml:"app"`
}

func extensionUpdateURL(source, id, version string) string {
	base := "https://edge.microsoft.com/extensionwebstorebase/v1/crx"
	if source == "chrome" {
		base = "https://clients2.google.com/service/update2/crx"
	}
	query := url.Values{"response": {"updatecheck"}, "prodversion": {"130.0.0.0"}, "acceptformat": {"crx3"}, "x": {"id=" + id + "&v=" + version + "&uc"}}
	return base + "?" + query.Encode()
}

func allowedExtensionURL(u *url.URL, source string) bool {
	if u == nil || u.User != nil || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "http" {
		return (source == "" || source == "edge") && host == "msedgeextensions.b.tlu.dl.delivery.mp.microsoft.com" && (u.Port() == "" || u.Port() == "80")
	}
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	edge := host == "edge.microsoft.com" || host == "msedgeextensions.b.tlu.dl.delivery.mp.microsoft.com"
	chrome := host == "clients2.google.com" || host == "clients2.googleusercontent.com"
	if source == "edge" {
		return edge
	}
	if source == "chrome" {
		return chrome
	}
	return edge || chrome
}

func (s *ExtensionStore) fetch(ctx context.Context, target string, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || !allowedExtensionURL(u, "") {
		return nil, errors.New("untrusted extension URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tikuAdapter-extension-updater/1")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("extension upstream status %d", res.StatusCode)
	}
	if res.ContentLength > maxBytes {
		return nil, errors.New("extension response is too large")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("extension response is too large")
	}
	return data, nil
}

func (s *ExtensionStore) refresh(ctx context.Context, source, id string) error {
	previous, previousErr := s.Latest(source)
	version := "0.0.0.0"
	if previousErr == nil {
		version = previous.Version
	}
	data, err := s.fetch(ctx, extensionUpdateURL(source, id, version), 1<<20)
	if err != nil {
		return err
	}
	var response extensionUpdateResponse
	if err := xml.Unmarshal(data, &response); err != nil {
		return errors.New("invalid extension update response")
	}
	if len(response.Apps) != 1 || response.Apps[0].ID != id || response.Apps[0].Status != "ok" {
		return errors.New("extension update identity mismatch")
	}
	update := response.Apps[0].Update
	now := time.Now()
	if update.Status == "noupdate" {
		if previousErr != nil {
			return ErrExtensionUnavailable
		}
		return s.markChecked(ctx, source, id, now)
	}
	if update.Status != "ok" || compareExtensionVersions(update.Version, update.Version) != 0 {
		return errors.New("invalid extension update version")
	}
	if previousErr == nil {
		comparison := compareExtensionVersions(update.Version, previous.Version)
		if comparison < 0 {
			return errors.New("extension update downgrade rejected")
		}
		if comparison == 0 {
			if update.Hash != "" && !strings.EqualFold(update.Hash, previous.SHA256) {
				return errors.New("extension hash changed without version update")
			}
			return s.markChecked(ctx, source, id, now)
		}
	}
	u, err := url.Parse(update.URL)
	if err != nil {
		return errors.New("invalid extension package URL")
	}
	if !allowedExtensionURL(u, source) {
		return errors.New("untrusted extension package URL")
	}
	// Edge's signed-package CDN advertises HTTP and does not reliably serve a
	// certificate for that hostname. HTTPS update metadata must supply a SHA256
	// anchor before that exact CDN is used; never disable TLS verification.
	if u.Scheme == "http" && (len(update.Hash) != 64 || strings.Trim(strings.ToLower(update.Hash), "0123456789abcdef") != "") {
		return errors.New("HTTP extension package requires trusted SHA256 metadata")
	}
	packageData, err := s.fetch(ctx, u.String(), maxExtensionBytes)
	if err != nil {
		return err
	}
	hash := extensionSHA256(packageData)
	if update.Hash != "" && !strings.EqualFold(hash, update.Hash) {
		return errors.New("extension download hash mismatch")
	}
	manifest, err := validateExtensionCRX(packageData, id)
	if err != nil {
		return extensionPackageError(err)
	}
	if compareExtensionVersions(manifest.Version, update.Version) != 0 {
		return errors.New("extension manifest version mismatch")
	}
	release := ExtensionRelease{Source: source, ExtensionID: id, Name: manifest.Name, Version: manifest.Version, Size: int64(len(packageData)), SHA256: hash, DownloadURL: "/adapter-service/extensions/" + source + "/download?sha256=" + hash, CheckedAt: now, UpdatedAt: now}
	cached := cachedExtension{release: release, data: packageData}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.IDs[source] != id || ctx.Err() != nil {
		return context.Canceled
	}
	if err := s.persist(source, cached); err != nil {
		return err
	}
	s.cache[source] = cached
	if previousErr == nil && previous.SHA256 != hash {
		_ = os.Remove(filepath.Join(s.config.Directory, source+"-"+previous.SHA256+".crx"))
	}
	return nil
}

func (s *ExtensionStore) markChecked(ctx context.Context, source, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.IDs[source] != id || ctx.Err() != nil {
		return context.Canceled
	}
	cached := s.cache[source]
	cached.release.CheckedAt = now
	if err := s.persistMetadata(source, cached.release); err != nil {
		return err
	}
	s.cache[source] = cached
	return nil
}

func (s *ExtensionStore) persist(source string, cached cachedExtension) error {
	if err := os.MkdirAll(s.config.Directory, 0o700); err != nil {
		return err
	}
	if err := writeExtensionAtomic(s.config.Directory, source+"-"+cached.release.SHA256+".crx", cached.data); err != nil {
		return err
	}
	return s.persistMetadata(source, cached.release)
}

func (s *ExtensionStore) persistMetadata(source string, release ExtensionRelease) error {
	data, err := json.Marshal(release)
	if err != nil {
		return err
	}
	return writeExtensionAtomic(s.config.Directory, source+"-latest.json", data)
}

func writeExtensionAtomic(directory, name string, data []byte) error {
	tmp, err := os.CreateTemp(directory, ".extension-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceExtensionFile(tmp.Name(), filepath.Join(directory, name))
}

func (s *ExtensionStore) load(source string) (cachedExtension, error) {
	var cached cachedExtension
	metadata, err := os.ReadFile(filepath.Join(s.config.Directory, source+"-latest.json"))
	if err != nil {
		return cached, err
	}
	if len(metadata) > 1<<20 || json.Unmarshal(metadata, &cached.release) != nil {
		return cached, errors.New("invalid extension cache metadata")
	}
	r := cached.release
	if r.Source != source || r.ExtensionID != s.config.IDs[source] || len(r.SHA256) != 64 || strings.Trim(r.SHA256, "0123456789abcdef") != "" {
		return cached, errors.New("extension cache identity mismatch")
	}
	file, err := os.Open(filepath.Join(s.config.Directory, source+"-"+r.SHA256+".crx"))
	if err != nil {
		return cached, err
	}
	defer file.Close()
	cached.data, err = io.ReadAll(io.LimitReader(file, maxExtensionBytes+1))
	if err != nil || len(cached.data) > maxExtensionBytes || extensionSHA256(cached.data) != r.SHA256 || int64(len(cached.data)) != r.Size {
		return cached, errors.New("invalid extension cached package")
	}
	manifest, err := validateExtensionCRX(cached.data, r.ExtensionID)
	if err != nil || manifest.Version != r.Version {
		return cached, errors.New("invalid extension cached manifest")
	}
	cached.release.Name = manifest.Name
	cached.release.DownloadURL = "/adapter-service/extensions/" + source + "/download?sha256=" + r.SHA256
	return cached, nil
}

var defaultExtensionStore struct {
	once  sync.Once
	store *ExtensionStore
	err   error
}

func DefaultExtensionStore() (*ExtensionStore, error) {
	return DefaultExtensionStoreWithDB(nil)
}

func DefaultExtensionStoreWithDB(db *gorm.DB) (*ExtensionStore, error) {
	defaultExtensionStore.once.Do(func() {
		defaultExtensionStore.store, defaultExtensionStore.err = newConfiguredExtensionStore(db)
	})
	return defaultExtensionStore.store, defaultExtensionStore.err
}

func newConfiguredExtensionStore(db *gorm.DB) (*ExtensionStore, error) {
	config, overrides, err := extensionStoreConfigFromEnv()
	if err != nil {
		return nil, err
	}
	environmentConfig := config
	settings, found, err := loadExtensionSettings(db)
	if err != nil {
		return nil, err
	}
	if !found {
		settings = ExtensionUpdateSettings{AutoUpdate: true, IntervalMinutes: 14400, EdgeID: DefaultEdgeExtensionID}
	}
	applyExtensionSettings(&config, settings)
	applyExtensionEnvironment(&config, environmentConfig, overrides)
	store, err := NewExtensionStore(config)
	if err != nil {
		return nil, err
	}
	store.databaseConfig = settings
	// Environment values are fixed for this process. Database saves may change
	// their fallback values without replacing the active deployment overrides.
	store.environmentConfig, store.environmentOverrides = environmentConfig, overrides
	return store, nil
}
