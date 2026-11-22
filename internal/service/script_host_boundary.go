package service

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// Deployment aliases are exact names (never wildcards). Snapshot once at startup;
// the independently editable script-domain policy is read from the database.
var siteHostConfig atomic.Pointer[map[string]bool]

func ConfigureSiteHosts(values []string) error {
	hosts := make(map[string]bool, len(values))
	for _, v := range values {
		host := normalizeScriptLauncherHost(strings.TrimSpace(v))
		if host == "" {
			return fmt.Errorf("siteHosts 包含无效主机名；只允许精确的 DNS 名称或 IP")
		}
		hosts[normalizePublicScriptHost(host)] = true
	}
	siteHostConfig.Store(&hosts)
	return nil
}
func declaredPlatformHost(host string) bool {
	h := siteHostConfig.Load()
	return h != nil && (*h)[host]
}

// IsLocalSiteAuthority recognizes the actual Host only, never a forwarding header.
func IsLocalSiteAuthority(authority string) bool {
	h := normalizePublicScriptHost(normalizeScriptLauncherHost(authority))
	return h == "localhost" || net.ParseIP(h).IsLoopback()
}

// Explicit aliases and built-in local names retain their platform roots.
func IsReservedScriptHostRoot(host, path string) bool {
	if path != "" && path != "/" {
		return false
	}
	host = normalizePublicScriptHost(host)
	return declaredPlatformHost(host) || isBuiltinReservedScriptHostname(host)
}

type ScriptWebHostRole uint8

const (
	RejectScriptWebHost ScriptWebHostRole = iota
	PlatformWebHost
	EntryOnlyWebHost
	StandaloneWebHost
)

// Only a dedicated address actually assigned to a script owns its hostname.
// Domain formats and the script denylist do not reserve otherwise unused hosts
// or prevent them from serving the ordinary site.
func ScriptWebHostRoleWithDB(db *gorm.DB, authority string) (ScriptWebHostRole, error) {
	canonical := normalizeScriptLauncherHost(authority)
	if canonical == "" {
		return RejectScriptWebHost, nil
	}
	host := normalizePublicScriptHost(canonical)
	if declaredPlatformHost(host) || isBuiltinReservedScriptHostname(host) {
		return PlatformWebHost, nil
	}
	if db == nil {
		return RejectScriptWebHost, errors.New("数据库未初始化")
	}
	occupied, err := scriptWebHostOccupiedWithDB(db, canonical)
	if err != nil {
		return RejectScriptWebHost, err
	}
	if occupied {
		return EntryOnlyWebHost, nil
	}
	return StandaloneWebHost, nil
}

// Occupancy is permanent routing metadata, independent of whether a script can
// still launch. Never join reservations to live scripts or filter retired rows:
// a replaced, deleted, disabled, expired or spent address must not expose WebUI.
func scriptWebHostOccupiedWithDB(db *gorm.DB, authority string) (bool, error) {
	host := normalizePublicScriptHost(authority)
	keys := []string{shortEntryAddressKey(authority, "")}
	if host != authority {
		keys = append(keys, shortEntryAddressKey(host, ""))
	}
	fresh := db.Session(&gorm.Session{NewDB: true})
	var reserved int64
	if err := fresh.Model(&entity.ScriptAddressReservation{}).Where("address_key IN ?", keys).Count(&reserved).Error; err != nil {
		return false, err
	}
	if reserved > 0 {
		return true, nil
	}
	// Pre-reservation rows retain their original root addresses. Restrict the
	// candidate query by root alias/locator or explicit short-address hostname,
	// then normalize stored URLs to distinguish roots from shared path entries.
	label, _, _ := strings.Cut(host, ".")
	var roots []string
	for _, scheme := range []string{"https://", "http://"} {
		roots = append(roots, scheme+host, scheme+host+"/")
	}
	var scripts []entity.UserScript
	err := fresh.Select("short_code", "base_domain", "scheme", "use_wildcard_domain", "launcher_locator", "launcher_bearer_enabled", "short_entry_address").
		Where("use_wildcard_domain = ? AND (short_code = ? OR launcher_locator = ?)", true, label, label).
		Or("short_entry_address IN ? OR short_entry_address LIKE ? OR short_entry_address LIKE ?", roots, "https://"+host+":%", "http://"+host+":%").
		Find(&scripts).Error
	if err != nil {
		return false, err
	}
	rootMatches := func(address string) bool {
		stored, path, _ := parseScriptReservedAddress(address)
		return stored != "" && path == "" && normalizePublicScriptHost(stored) == host
	}
	for _, script := range scripts {
		if rootMatches(BuildScriptCommandURL(script)) || rootMatches(script.ShortEntryAddress) {
			return true, nil
		}
		// UUID launchers also retain a distinct short-code API origin.
		if script.UseWildcardDomain && script.LauncherLocator != nil && script.LauncherBearerEnabled {
			script.LauncherLocator = nil
			if rootMatches(BuildScriptCommandURL(script)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ScriptCodeOwnsWebHostWithDB is only a router predicate; it never issues or
// consumes a grant. The protocol controller still verifies the original ticket
// or lease, its generation and deadlines for the requested operation.
func ScriptCodeOwnsWebHostWithDB(db *gorm.DB, authority, code string) (bool, error) {
	if db == nil || !isScriptShortCodeSegment(code) {
		return false, nil
	}
	host := normalizeScriptLauncherHost(authority)
	if host == "" {
		return false, nil
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND enabled = ? AND deleted_at IS NULL", code, true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return scriptOwnsWebHost(db, host, script)
}

func ScriptSessionOwnsWebHostWithDB(db *gorm.DB, authority, authorization, identity, ip string) bool {
	if db == nil || identity == "" {
		return false
	}
	_, lease, err := ResolveScriptRunSessionWithDB(db, authorization, identity, ip)
	if err != nil || lease.LeaseSlot == nil {
		return false
	}
	host := normalizeScriptLauncherHost(authority)
	if host == "" {
		return false
	}
	var script entity.UserScript
	if db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND enabled = ? AND deleted_at IS NULL", lease.ScriptID, true).First(&script).Error != nil {
		return false
	}
	ok, _ := scriptOwnsWebHost(db, host, script)
	return ok
}
func scriptOwnsWebHost(db *gorm.DB, host string, script entity.UserScript) (bool, error) {
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		return false, err
	}
	if settings.validateHostNotDenied(host) != nil || settings.validateLauncherDomain(script) != nil {
		return false, nil
	}
	sameHost := func(address string) bool {
		u, e := url.Parse(address)
		return e == nil && normalizeScriptLauncherHost(u.Host) == host
	}
	if sameHost(BuildScriptCommandURL(script)) {
		return true, nil
	}
	// A spent entry may have a live, individually authenticated lease. Losing the
	// remaining start budget does not invalidate that lease's original API origin.
	if script.ShortEntryAddress != "" && sameHost(script.ShortEntryAddress) {
		_, _, e := validateScriptShortAddressForUseWithDB(db, script.ShortEntryAddress, host, true)
		if e == nil {
			return true, nil
		}
	}
	// The existing one-time owner exchange returns the non-bearer API alias in
	// wildcard deployments; never enable a public entry at that alias.
	if script.LauncherLocator != nil && script.LauncherBearerEnabled && script.UseWildcardDomain {
		script.LauncherLocator = nil
		return sameHost(BuildScriptCommandURL(script)), nil
	}
	return false, nil
}

// ScriptWebHostHasAssignmentWithDB answers only a CORS metadata question. It
// cannot issue a ticket, claim a device, consume a use or authorize a request.
func ScriptWebHostHasAssignmentWithDB(db *gorm.DB, authority string) (bool, error) {
	host := normalizeScriptLauncherHost(authority)
	if db == nil || host == "" {
		return false, nil
	}
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		return false, err
	}
	if settings.validateHostNotDenied(host) != nil {
		return false, nil
	}
	fresh := db.Session(&gorm.Session{NewDB: true})
	// Indexed root reservation covers both current short aliases and generated roots.
	_, _, key := parseScriptReservedAddress("https://" + host)
	var owner entity.UserScript
	err = fresh.Table("user_scripts").Select("user_scripts.*").Joins("JOIN script_address_reservations r ON r.script_id = user_scripts.id").Where("r.address_key = ? AND user_scripts.enabled = ? AND user_scripts.deleted_at IS NULL", key, true).Limit(1).Find(&owner).Error
	if err != nil {
		return false, err
	}
	if owner.ID > 0 {
		return scriptOwnsWebHost(db, host, owner)
	}
	label, _, _ := strings.Cut(host, ".")
	if ok, err := ScriptCodeOwnsWebHostWithDB(db, host, label); ok || err != nil {
		return ok, err
	}
	// Shared path-style origins are considered only if explicitly configured.
	// An arbitrary hostname cannot trigger a scan for path aliases.
	for _, f := range settings.Formats() {
		if scriptDomainPathCovers(f, host) {
			var one int
			result := fresh.Model(&entity.UserScript{}).Select("1").Where("enabled = ? AND deleted_at IS NULL AND ((base_domain = ? AND launcher_locator IS NOT NULL AND use_wildcard_domain = ?) OR short_entry_address LIKE ?)", true, host, false, "https://"+host+"/%").Limit(1).Scan(&one)
			return one == 1, result.Error
		}
	}
	return false, nil
}
