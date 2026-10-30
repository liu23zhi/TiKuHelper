package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const extensionSettingsKey = "extension_update_config"

const maxExtensionIntervalMinutes = int64(1<<63-1) / int64(time.Minute)

var ErrInvalidExtensionSettings = errors.New("扩展更新设置无效")

type ExtensionUpdateSettings struct {
	AutoUpdate      bool   `json:"autoUpdate"`
	IntervalMinutes int    `json:"intervalMinutes"`
	EdgeID          string `json:"edgeId"`
	ChromeID        string `json:"chromeId"`
}

type ExtensionEnvironmentOverrides struct {
	AutoUpdate      bool `json:"autoUpdate"`
	IntervalMinutes bool `json:"intervalMinutes"`
	EdgeID          bool `json:"edgeId"`
	ChromeID        bool `json:"chromeId"`
}

type ExtensionSourceStatus struct {
	Source        string            `json:"source"`
	Configured    bool              `json:"configured"`
	ExtensionID   string            `json:"extensionId"`
	Available     bool              `json:"available"`
	Updating      bool              `json:"updating"`
	Release       *ExtensionRelease `json:"release,omitempty"`
	LastCheckedAt *time.Time        `json:"lastCheckedAt,omitempty"`
	LastError     string            `json:"lastError,omitempty"`
}

type ExtensionConfiguration struct {
	Config               ExtensionUpdateSettings       `json:"config"`
	DatabaseConfig       ExtensionUpdateSettings       `json:"databaseConfig"`
	EnvironmentOverrides ExtensionEnvironmentOverrides `json:"environmentOverrides"`
	Sources              []ExtensionSourceStatus       `json:"sources"`
}

func normalizeExtensionSettings(settings ExtensionUpdateSettings) (ExtensionUpdateSettings, error) {
	settings.EdgeID = strings.TrimSpace(settings.EdgeID)
	settings.ChromeID = strings.TrimSpace(settings.ChromeID)
	if settings.IntervalMinutes < 1 || int64(settings.IntervalMinutes) > maxExtensionIntervalMinutes ||
		(settings.EdgeID != "" && !validExtensionID(settings.EdgeID)) ||
		(settings.ChromeID != "" && !validExtensionID(settings.ChromeID)) {
		return settings, ErrInvalidExtensionSettings
	}
	return settings, nil
}

func loadExtensionSettings(db *gorm.DB) (ExtensionUpdateSettings, bool, error) {
	var settings ExtensionUpdateSettings
	if db == nil {
		return settings, false, nil
	}
	var row entity.SystemSetting
	err := db.Where("key = ?", extensionSettingsKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return settings, false, nil
	}
	if err != nil {
		return settings, false, err
	}
	if json.Unmarshal([]byte(row.Value), &settings) != nil {
		return settings, true, ErrInvalidExtensionSettings
	}
	settings, err = normalizeExtensionSettings(settings)
	return settings, true, err
}

func applyExtensionSettings(config *ExtensionStoreConfig, settings ExtensionUpdateSettings) {
	config.AutoUpdate = settings.AutoUpdate
	config.Interval = time.Duration(settings.IntervalMinutes) * time.Minute
	config.IDs = map[string]string{"edge": settings.EdgeID, "chrome": settings.ChromeID}
}

func extensionSettingsFromConfig(config ExtensionStoreConfig) ExtensionUpdateSettings {
	return ExtensionUpdateSettings{AutoUpdate: config.AutoUpdate, IntervalMinutes: int(config.Interval / time.Minute), EdgeID: config.IDs["edge"], ChromeID: config.IDs["chrome"]}
}

func applyExtensionEnvironment(config *ExtensionStoreConfig, environment ExtensionStoreConfig, overrides ExtensionEnvironmentOverrides) {
	if overrides.AutoUpdate {
		config.AutoUpdate = environment.AutoUpdate
	}
	if overrides.IntervalMinutes {
		config.Interval = environment.Interval
	}
	if overrides.EdgeID {
		config.IDs["edge"] = environment.IDs["edge"]
	}
	if overrides.ChromeID {
		config.IDs["chrome"] = environment.IDs["chrome"]
	}
}

func (s *ExtensionStore) Configuration() ExtensionConfiguration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := ExtensionConfiguration{Config: extensionSettingsFromConfig(s.config), DatabaseConfig: s.databaseConfig, EnvironmentOverrides: s.environmentOverrides}
	for _, source := range []string{"edge", "chrome"} {
		status := s.statuses[source]
		status.Source, status.ExtensionID = source, s.config.IDs[source]
		status.Configured, status.Updating = status.ExtensionID != "", s.checks[source] != nil
		if cached, ok := s.cache[source]; ok {
			release := cached.release
			status.Available, status.Release = true, &release
		}
		result.Sources = append(result.Sources, status)
	}
	return result
}

// Save the complete configuration before changing the live scheduler. A failed
// database write leaves both active settings and the verified cache untouched.
func (s *ExtensionStore) UpdateSettingsWithDB(db *gorm.DB, settings ExtensionUpdateSettings) (ExtensionConfiguration, error) {
	settings, err := normalizeExtensionSettings(settings)
	if err != nil {
		return ExtensionConfiguration{}, err
	}
	if db == nil {
		return ExtensionConfiguration{}, errors.New("extension settings database unavailable")
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return ExtensionConfiguration{}, err
	}
	s.mu.Lock()
	if err := saveSystemSettingValue(db, extensionSettingsKey, string(data), "浏览器扩展自动更新设置"); err != nil {
		s.mu.Unlock()
		return ExtensionConfiguration{}, err
	}
	oldIDs := s.config.IDs
	s.databaseConfig = settings
	applyExtensionSettings(&s.config, settings)
	applyExtensionEnvironment(&s.config, s.environmentConfig, s.environmentOverrides)
	for _, source := range []string{"edge", "chrome"} {
		if oldIDs[source] == s.config.IDs[source] {
			continue
		}
		if check := s.checks[source]; check != nil {
			check.cancel()
			delete(s.checks, source)
		}
		delete(s.cache, source)
		delete(s.statuses, source)
		if s.config.IDs[source] != "" {
			if cached, err := s.load(source); err == nil {
				s.cache[source] = cached
			}
		}
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return s.Configuration(), nil
}
