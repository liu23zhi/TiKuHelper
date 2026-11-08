// Command s04-fixture is test-only. It never starts the service or applies its
// bootstrap policy: the actual production ELF must perform those transitions.
// Database access requires an exclusively owned, explicitly marked synthetic
// root; the original/preview database filenames cannot pass these checks.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	fixtureMarker       = "tikuhelper-s04-synthetic-only\n"
	legacyPassword      = "S04-Old-Fixture!2026"
	environmentPassword = "S04-New-Fixture!2026"
	legacyBalance       = 12.345678
)

var legacyCreatedAt = time.Date(2025, time.January, 2, 3, 4, 5, 0, time.FixedZone("S04+08", 8*3600))

type endpointObservation struct {
	Path           string            `json:"path"`
	Status         int               `json:"status"`
	Body           string            `json:"body"`
	ResponseStatus string            `json:"responseStatus"`
	CacheControl   string            `json:"cacheControl"`
	RequestID      string            `json:"requestID"`
	RequestHeaders map[string]string `json:"requestHeaders"`
	Error          string            `json:"error,omitempty"`
}
type databaseObservation struct {
	OK                         bool   `json:"ok"`
	Operation                  string `json:"operation"`
	QuickCheck                 string `json:"quickCheck"`
	Bcrypt                     bool   `json:"bcrypt"`
	ExpectedPasswordMatches    bool   `json:"expectedPasswordMatches"`
	EnvironmentPasswordMatches bool   `json:"environmentPasswordMatches"`
	UnrelatedFieldsPreserved   bool   `json:"unrelatedFieldsPreserved"`
	RWProbe                    []int  `json:"rwProbe"`
	UserCount                  int64  `json:"userCount"`
	ReadonlyInspection         bool   `json:"readonlyInspection"`
}

func validateFixturePath(p string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || filepath.Base(p) != "fixture.db" || filepath.Base(filepath.Dir(p)) != "data" {
		return "", errors.New("database must be an absolute owned data/fixture.db")
	}
	root := filepath.Dir(filepath.Dir(p))
	for _, q := range []string{root, filepath.Dir(p)} {
		info, err := os.Lstat(q)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("fixture directory missing or symlinked")
		}
		resolved, err := filepath.EvalSymlinks(q)
		if err != nil || resolved != q {
			return "", errors.New("fixture ancestor is symlinked")
		}
	}
	marker := filepath.Join(root, ".s04-fixture")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("synthetic fixture marker missing or invalid")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(raw, []byte(fixtureMarker)) {
		return "", errors.New("synthetic fixture marker mismatch")
	}
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("fixture database must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect fixture database")
	}
	return p, nil
}

func openFixtureDB(p string, readonly bool) (*gorm.DB, func() error, error) {
	p, err := validateFixturePath(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("existing synthetic fixture database required")
	}
	mode := "rw"
	if readonly {
		mode = "ro"
	}
	dsn := (&url.URL{Scheme: "file", Path: p}).String() + "?mode=" + mode
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, errors.New("cannot open synthetic fixture database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return db, sqlDB.Close, nil
}
func syntheticLegacy() entity.User {
	return entity.User{ID: 1, Username: "S04_SYNTHETIC_ADMIN", Password: legacyPassword, Perms: "s04-synthetic-preserve", ParentID: 37, Nickname: "S04_SYNTHETIC_KEEP", Role: "admin", Status: "enabled", Balance: legacyBalance, RegisterSource: "fixture", CreatedAt: legacyCreatedAt, LastLoginIP: "198.51.100.9", RegisterIP: "192.0.2.4"}
}
func seedLegacy(p string) (map[string]any, error) {
	p, err := validateFixturePath(p)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("seed requires a new synthetic database, never overwrites")
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, closeDB, err := openFixtureDB(p, false)
	if err != nil {
		return nil, err
	}
	if err = db.AutoMigrate(&entity.User{}); err == nil {
		row := syntheticLegacy()
		err = db.Create(&row).Error
	}
	closeErr := closeDB()
	if err != nil {
		return nil, errors.New("cannot seed synthetic legacy row")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return map[string]any{"ok": true, "operation": "seed-legacy", "legacyPlaintextSeeded": true, "userCount": 1}, nil
}
func inspectDatabase(p, kind string) (databaseObservation, error) {
	report := databaseObservation{Operation: "verify-" + kind, ReadonlyInspection: true}
	db, closeDB, err := openFixtureDB(p, true)
	if err != nil {
		return report, err
	}
	var row entity.User
	if err = db.Raw("PRAGMA quick_check").Scan(&report.QuickCheck).Error; err == nil {
		err = db.Model(&entity.User{}).Count(&report.UserCount).Error
	}
	if err == nil {
		err = db.First(&row, 1).Error
	}
	closeErr := closeDB()
	if err != nil {
		return report, errors.New("cannot inspect synthetic fixture state")
	}
	if closeErr != nil {
		return report, closeErr
	}
	report.Bcrypt = strings.HasPrefix(row.Password, "$2a$") || strings.HasPrefix(row.Password, "$2b$") || strings.HasPrefix(row.Password, "$2y$")
	expected := environmentPassword
	if kind == "legacy" {
		expected = legacyPassword
	}
	report.ExpectedPasswordMatches, _ = util.VerifyPassword(row.Password, expected)
	report.EnvironmentPasswordMatches, _ = util.VerifyPassword(row.Password, environmentPassword)
	if kind == "legacy" {
		before := syntheticLegacy()
		report.UnrelatedFieldsPreserved = row.ID == before.ID && row.Username == before.Username && row.Perms == before.Perms && row.ParentID == before.ParentID && row.Nickname == before.Nickname && row.Role == before.Role && row.Status == before.Status && row.Balance == before.Balance && row.RegisterSource == before.RegisterSource && row.CreatedAt.Equal(before.CreatedAt) && row.LastLoginIP == before.LastLoginIP && row.RegisterIP == before.RegisterIP && row.Phone == nil && row.WechatID == nil && row.PhoneBoundAt == nil && row.WechatBoundAt == nil && row.LastLoginAt == nil
	} else {
		report.UnrelatedFieldsPreserved = row.ID == 1 && row.Username == "admin" && row.Nickname == "管理员" && row.Role == "admin" && row.Status == "enabled" && row.Balance == 0 && row.RegisterSource == "system"
	}
	if report.QuickCheck != "ok" || report.UserCount != 1 || !report.Bcrypt || !report.ExpectedPasswordMatches || !report.UnrelatedFieldsPreserved || (kind == "legacy" && report.EnvironmentPasswordMatches) {
		return report, errors.New("synthetic credential or preserved-state verification failed")
	}
	return report, nil
}
func readWriteProbe(p string) ([]int, error) {
	db, closeDB, err := openFixtureDB(p, false)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		_ = closeDB()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		_ = closeDB()
		return nil, err
	}
	key := "_s04_fixture_probe_" + hex.EncodeToString(token[:])
	counts := make([]int, 3)
	if err = sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&counts[0]); err != nil {
		_ = closeDB()
		return nil, errors.New("fixture probe initial read failed")
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		_ = closeDB()
		return nil, err
	}
	_, insertErr := tx.ExecContext(ctx, "INSERT INTO system_settings(key,value,created_at,updated_at) VALUES (?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", key, "synthetic-rollback-only")
	if insertErr == nil {
		insertErr = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&counts[1])
	}
	rollbackErr := tx.Rollback()
	if insertErr == nil && rollbackErr == nil {
		err = sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&counts[2])
	}
	closeErr := closeDB()
	if insertErr != nil || rollbackErr != nil || err != nil || closeErr != nil || counts[0] != 0 || counts[1] != 1 || counts[2] != 0 {
		return counts, errors.New("fixture rollback probe failed")
	}
	return counts, nil
}
func verifyDatabase(p, kind string) (databaseObservation, error) {
	if kind != "new" && kind != "legacy" && kind != "development" {
		return databaseObservation{}, errors.New("unknown synthetic verification mode")
	}
	report, err := inspectDatabase(p, kind)
	if err != nil {
		return report, err
	}
	report.RWProbe, err = readWriteProbe(p)
	if err != nil {
		return report, err
	}
	report.OK = true
	return report, nil
}
func probeEndpoint(base, path, expected string) (endpointObservation, error) {
	report := endpointObservation{Path: path}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (path != "/healthz" && path != "/readyz") {
		return report, errors.New("probe requires fixed loopback health routes")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return report, errors.New("probe only permits numeric loopback")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return report, errors.New("cannot construct fixed loopback probe request")
	}
	// Distinct, fixed documentation-range sentinels exercise actual main's trusted
	// proxy wiring. They are fixture input, never a claimed observed ClientIP.
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	request.Header.Set("X-Real-IP", "198.51.100.9")
	report.RequestHeaders = map[string]string{"X-Forwarded-For": request.Header.Get("X-Forwarded-For"), "X-Real-IP": request.Header.Get("X-Real-IP")}
	response, err := client.Do(request)
	if err != nil {
		report.Error = "loopback HTTP request failed"
		return report, errors.New(report.Error)
	}
	report.Status = response.StatusCode
	report.CacheControl = response.Header.Get("Cache-Control")
	report.RequestID = response.Header.Get("X-Request-ID")
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	closeErr := response.Body.Close()
	report.Body = string(raw)
	var body struct {
		Status string `json:"status"`
	}
	decodeErr := json.Unmarshal(raw, &body)
	report.ResponseStatus = body.Status
	if readErr != nil || closeErr != nil || len(raw) > 4096 || decodeErr != nil || response.StatusCode != http.StatusOK || body.Status != expected {
		report.Error = "health status or literal body mismatch"
		return report, errors.New(report.Error)
	}
	// Public routes explicitly prevent probe responses from being cached.
	if report.CacheControl != "no-store" {
		report.Error = "health cache policy mismatch"
		return report, errors.New(report.Error)
	}
	return report, nil
}

// ownershipEntries fully validates the tiny marked tree before any ownership
// change. Only the literal /fixture mount is accepted by the public CLI. Tests
// supply their own t.TempDir as fixedRoot to exercise this same preflight.
type ownershipEntry struct {
	path string
	info os.FileInfo
}

func unixStatValue(info os.FileInfo, name string) (uint64, error) {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, errors.New("fixture ownership metadata unsupported")
	}
	field := value.FieldByName(name)
	if !field.IsValid() || !field.CanUint() {
		return 0, errors.New("fixture ownership metadata unavailable")
	}
	return field.Uint(), nil
}
func managedFixtureLog(name string) bool {
	for _, prefix := range []string{"Tiku", "Tiku-warning", "Tiku-error", "Tiku-thinking"} {
		if name == prefix+".txt" {
			return true
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		if strings.HasSuffix(suffix, ".txt.gz") {
			suffix = strings.TrimSuffix(suffix, ".txt.gz")
		} else if strings.HasSuffix(suffix, ".txt") {
			suffix = strings.TrimSuffix(suffix, ".txt")
		} else {
			continue
		}
		if suffix != "" && strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return true
		}
	}
	return false
}
func ownershipEntries(p string, uid, gid int, fixedRoot string) ([]ownershipEntry, error) {
	if _, err := validateFixturePath(p); err != nil {
		return nil, err
	}
	root := filepath.Dir(filepath.Dir(p))
	if root != fixedRoot || uid < 0 || gid < 0 || uint64(uid) > 2147483647 || uint64(gid) > 2147483647 {
		return nil, errors.New("fixture-owner requires fixed mount and valid owner IDs")
	}
	ownerPath := filepath.Join(root, ".s04-host-owner.json")
	ownerInfo, err := os.Lstat(ownerPath)
	if err != nil || !ownerInfo.Mode().IsRegular() {
		return nil, errors.New("fixture host-owner record required")
	}
	nlink, err := unixStatValue(ownerInfo, "Nlink")
	if err != nil || nlink != 1 {
		return nil, errors.New("fixture host-owner record may not be hardlinked")
	}
	raw, err := os.ReadFile(ownerPath)
	if err != nil || len(raw) > 128 {
		return nil, errors.New("fixture host-owner record unreadable")
	}
	var host struct {
		UID *int `json:"uid"`
		GID *int `json:"gid"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&host); err != nil || host.UID == nil || host.GID == nil || *host.UID < 0 || *host.GID < 0 || uint64(*host.UID) > 2147483647 || uint64(*host.GID) > 2147483647 {
		return nil, errors.New("invalid fixture host-owner record")
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("fixture host-owner record has trailing data")
	}
	if !(uid == 1000 && gid == 1000) && !(uid == *host.UID && gid == *host.GID) {
		return nil, errors.New("fixture-owner target is neither fixed service user nor recorded host owner")
	}
	entries := []ownershipEntry{}
	err = filepath.WalkDir(root, func(current string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot enumerate fixture tree")
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return errors.New("fixture tree escaped fixed root")
		}
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("cannot inspect fixture entry")
		}
		if (!info.IsDir() && !info.Mode().IsRegular()) || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("fixture tree contains link, special file or special mode")
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		allowed := false
		switch {
		case relative == ".":
			allowed = info.IsDir()
		case len(parts) == 1:
			switch relative {
			case ".s04-fixture", ".s04-host-owner.json", "config.yaml":
				allowed = info.Mode().IsRegular()
			case "data", "logs":
				allowed = info.IsDir()
			}
		case len(parts) == 2 && parts[0] == "data":
			switch parts[1] {
			case "fixture.db", "fixture.db-wal", "fixture.db-shm", "fixture.db-journal":
				allowed = info.Mode().IsRegular()
			}
		case len(parts) == 2 && parts[0] == "logs":
			if parts[1] == "archive" {
				allowed = info.IsDir()
			} else {
				allowed = info.Mode().IsRegular() && managedFixtureLog(parts[1])
			}
		case len(parts) == 3 && parts[0] == "logs" && parts[1] == "archive":
			allowed = info.Mode().IsRegular() && strings.HasSuffix(parts[2], ".zip")
		}
		if !allowed {
			return errors.New("fixture tree contains unexpected path")
		}
		if info.Mode().IsRegular() {
			links, err := unixStatValue(info, "Nlink")
			if err != nil || links != 1 {
				return errors.New("fixture regular file may not be hardlinked")
			}
		}
		entries = append(entries, ownershipEntry{path: current, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}
func fixtureOwner(p string, uid, gid int) (map[string]any, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("fixture-owner requires the isolated root helper")
	}
	entries, err := ownershipEntries(p, uid, gid, "/fixture")
	if err != nil {
		return nil, err
	}
	// Lchown deliberately never follows a last-component symlink; each inode and
	// mode is rechecked after the all-tree preflight and again after changing it.
	for _, entry := range entries {
		current, err := os.Lstat(entry.path)
		if err != nil || !os.SameFile(entry.info, current) || current.Mode() != entry.info.Mode() {
			return nil, errors.New("fixture entry changed after preflight")
		}
		if err = os.Lchown(entry.path, uid, gid); err != nil {
			return nil, errors.New("fixture ownership change failed")
		}
	}
	for _, entry := range entries {
		current, err := os.Lstat(entry.path)
		if err != nil || !os.SameFile(entry.info, current) || current.Mode() != entry.info.Mode() {
			return nil, errors.New("fixture identity or permissions changed")
		}
		actualUID, uidErr := unixStatValue(current, "Uid")
		actualGID, gidErr := unixStatValue(current, "Gid")
		if uidErr != nil || gidErr != nil || actualUID != uint64(uid) || actualGID != uint64(gid) {
			return nil, errors.New("fixture owner verification failed")
		}
	}
	return map[string]any{"ok": true, "operation": "fixture-owner", "uid": uid, "gid": gid, "allOwned": true, "symlinksRejected": true, "hardlinksRejected": true, "permissionsPreserved": true, "entries": len(entries)}, nil
}

func run(args []string, out io.Writer) int {
	emit := func(value any) int {
		if err := json.NewEncoder(out).Encode(value); err != nil {
			return 1
		}
		return 0
	}
	fail := func(operation string, err error) int {
		_ = emit(map[string]any{"ok": false, "operation": operation, "error": err.Error()})
		return 1
	}
	if len(args) == 0 {
		return fail("usage", errors.New("operation required"))
	}
	operation := args[0]
	if operation == "health" {
		if len(args) != 1 {
			return fail(operation, errors.New("health accepts no arguments"))
		}
		results := make([]endpointObservation, 0, 2)
		var firstErr error
		for _, route := range []struct{ path, expected string }{{"/healthz", "ok"}, {"/readyz", "ready"}} {
			observed, err := probeEndpoint("http://127.0.0.1:8060", route.path, route.expected)
			results = append(results, observed)
			if firstErr == nil {
				firstErr = err
			}
		}
		code := emit(map[string]any{"ok": firstErr == nil, "operation": operation, "endpoints": results})
		if firstErr != nil {
			return 1
		}
		return code
	}
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	db := flags.String("db", "", "synthetic fixture database")
	uid := flags.Int("uid", -1, "isolated fixture target UID")
	gid := flags.Int("gid", -1, "isolated fixture target GID")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return fail(operation, errors.New("invalid fixture command arguments"))
	}
	if operation == "fixture-owner" {
		report, err := fixtureOwner(*db, *uid, *gid)
		if err != nil {
			return fail(operation, err)
		}
		return emit(report)
	}
	if *uid != -1 || *gid != -1 {
		return fail(operation, errors.New("owner flags require fixture-owner operation"))
	}
	if operation == "seed-legacy" {
		report, err := seedLegacy(*db)
		if err != nil {
			return fail(operation, err)
		}
		return emit(report)
	}
	switch operation {
	case "verify-new", "verify-legacy", "verify-development":
		report, err := verifyDatabase(*db, strings.TrimPrefix(operation, "verify-"))
		if err != nil {
			return fail(operation, err)
		}
		return emit(report)
	default:
		return fail(operation, errors.New("unknown fixture operation"))
	}
}
func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
