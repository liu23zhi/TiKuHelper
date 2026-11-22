package service

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const MaxScriptAuthorizationWindows = 32

type ScriptAuthorizationWindow struct {
	StartsAt  time.Time `json:"startsAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func normalizeScriptAuthorizationWindows(windows []ScriptAuthorizationWindow, short bool, now time.Time) ([]ScriptAuthorizationWindow, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	if len(windows) > MaxScriptAuthorizationWindows {
		return nil, fmt.Errorf("%w：最多设置 %d 个时段", ErrScriptAuthorizationWindowInvalid, MaxScriptAuthorizationWindows)
	}
	normalized := slices.Clone(windows)
	for i := range normalized {
		normalized[i].StartsAt = normalized[i].StartsAt.UTC()
		normalized[i].ExpiresAt = normalized[i].ExpiresAt.UTC()
	}
	slices.SortFunc(normalized, func(a, b ScriptAuthorizationWindow) int {
		return a.StartsAt.Compare(b.StartsAt)
	})
	if err := validateStoredScriptAuthorizationWindows(normalized, short); err != nil {
		return nil, err
	}
	last := normalized[len(normalized)-1]
	if last.StartsAt.After(now.Add(365*24*time.Hour)) || !last.ExpiresAt.After(now) {
		return nil, ErrScriptAuthorizationWindowInvalid
	}
	return normalized, nil
}

func validateStoredScriptAuthorizationWindows(windows []ScriptAuthorizationWindow, short bool) error {
	if len(windows) == 0 || len(windows) > MaxScriptAuthorizationWindows {
		return ErrScriptAuthorizationWindowInvalid
	}
	limit := 365 * 24 * time.Hour
	if short {
		limit = ScriptShortAuthorizationMaxMinutes * time.Minute
	}
	for i, window := range windows {
		if window.StartsAt.IsZero() || window.ExpiresAt.IsZero() ||
			!window.ExpiresAt.After(window.StartsAt) || window.ExpiresAt.Sub(window.StartsAt) > limit {
			return ErrScriptAuthorizationWindowInvalid
		}
		if i > 0 && window.StartsAt.Before(windows[i-1].ExpiresAt) {
			return fmt.Errorf("%w：时段不能重叠", ErrScriptAuthorizationWindowInvalid)
		}
	}
	return nil
}

func encodeScriptAuthorizationWindows(windows []ScriptAuthorizationWindow) (string, error) {
	if len(windows) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(windows)
	return string(encoded), err
}

func decodeScriptAuthorizationWindows(raw string, short bool) ([]ScriptAuthorizationWindow, error) {
	if raw == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var windows []ScriptAuthorizationWindow
	if err := decoder.Decode(&windows); err != nil {
		return nil, ErrScriptAuthorizationWindowInvalid
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, ErrScriptAuthorizationWindowInvalid
	}
	if err := validateStoredScriptAuthorizationWindows(windows, short); err != nil {
		return nil, err
	}
	return windows, nil
}

func scriptAuthorizationWindowsStatus(enabled bool, windows []ScriptAuthorizationWindow, maximum, used int64, now time.Time) string {
	if len(windows) == 0 {
		return "disabled"
	}
	first, last := windows[0], windows[len(windows)-1]
	status := scriptAuthorizationStatus(enabled, &first.StartsAt, &last.ExpiresAt, maximum, used, now)
	if status != "active" {
		return status
	}
	for _, window := range windows {
		if now.Before(window.StartsAt) {
			return "paused"
		}
		if now.Before(window.ExpiresAt) {
			return "active"
		}
	}
	return "expired"
}
