package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var generationName = regexp.MustCompile(`^generation-[0-9]+$`)
var generationMarker = regexp.MustCompile(`(?m)^# tiku-gateway-generation: (generation-[0-9]+)\r?$`)
var certificateID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readGeneration accepts only generated directories and content-addressed PEM
// files. Metadata supplies logical source names only; all cryptographic facts
// are recovered from freshly validated private bytes.
func readGeneration(root string, now time.Time, report func(string)) (string, []Certificate, int, error) {
	config, err := os.ReadFile(filepath.Join(root, "active.conf"))
	if os.IsNotExist(err) {
		return "", nil, 0, nil
	}
	if err != nil {
		return "", nil, 0, err
	}
	marker := generationMarker.FindSubmatch(config)
	if len(marker) != 2 {
		return "", nil, 0, fmt.Errorf("active Nginx configuration has no gateway generation marker")
	}
	directory := filepath.Join(root, string(marker[1]))
	info, err := os.Lstat(directory)
	if err != nil {
		return "", nil, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, 0, fmt.Errorf("active gateway generation is not a private directory")
	}
	manifest, err := os.ReadFile(filepath.Join(directory, "certificates.json"))
	if err != nil {
		return "", nil, 0, err
	}
	var metadata []Certificate
	if err = json.Unmarshal(manifest, &metadata); err != nil {
		return "", nil, 0, err
	}
	scanned, err := ScanCertificates(directory, now, report)
	if err != nil {
		return "", nil, 0, err
	}
	byID := map[string]Certificate{}
	for _, pair := range scanned {
		byID[pair.ID] = pair
	}
	var valid []Certificate
	seen := map[string]bool{}
	for _, accepted := range metadata {
		if !certificateID.MatchString(accepted.ID) || seen[accepted.ID] {
			return "", nil, 0, fmt.Errorf("invalid certificate snapshot metadata")
		}
		seen[accepted.ID] = true
		if pair, ok := byID[accepted.ID]; ok {
			pair.CertPath = accepted.CertPath
			pair.KeyPath = accepted.KeyPath
			valid = append(valid, pair)
		}
	}
	return directory, valid, len(metadata), nil
}

func cleanupGenerations(root, active, rollback string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !generationName.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path != active && path != rollback {
			_ = os.RemoveAll(path)
		}
	}
}

type TLSStatus struct {
	Ready         bool
	Active, Valid int
}

func (status TLSStatus) Message() string {
	switch {
	case status.Active == 0:
		return "HTTP ready; HTTPS 未启用：未找到有效证书"
	case status.Ready:
		return "HTTP ready; HTTPS certificate snapshots active"
	case status.Valid > 0:
		return fmt.Sprintf("HTTP ready; HTTPS 未就绪：部分证书已过期或不可用（%d/%d 有效）", status.Valid, status.Active)
	default:
		return "HTTP ready; HTTPS 未就绪：活动证书已过期或不可用"
	}
}
func CurrentTLSStatus(root string, now time.Time) (TLSStatus, error) {
	directory, valid, total, err := readGeneration(root, now, nil)
	if err != nil {
		return TLSStatus{}, err
	}
	if directory == "" {
		return TLSStatus{}, fmt.Errorf("no active gateway configuration")
	}
	return TLSStatus{Ready: total > 0 && len(valid) == total, Active: total, Valid: len(valid)}, nil
}
