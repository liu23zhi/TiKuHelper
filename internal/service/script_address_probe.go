package service

import (
	"errors"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ScriptAddressAvailableWithDB checks an exact HTTPS launcher address without
// issuing a ticket, changing counters, or acquiring a lease. The launch
// handlers must still recheck authorization when a client actually starts.
func ScriptAddressAvailableWithDB(db *gorm.DB, authority, requestPath string) bool {
	if db == nil || IsReservedScriptHostRoot(authority, requestPath) {
		return false
	}
	host := normalizeScriptLauncherHost(authority)
	path, err := cleanScriptRequestPath(requestPath)
	if host == "" || err != nil || (path != requestPath && requestPath != "/") {
		return false
	}
	if scriptReservedPrefix(scriptFirstPathSegment(path)) {
		return false
	}
	fresh := db.Session(&gorm.Session{NewDB: true})
	key := shortEntryAddressKey(host, path)
	var script entity.UserScript
	err = fresh.Where("short_entry_address_key = ? AND enabled = ? AND deleted_at IS NULL", key, true).Take(&script).Error
	short, address := err == nil, "https://"+host+path
	switch {
	case short:
		normalized, _, err := validateScriptShortAddressForUseWithDB(fresh, script.ShortEntryAddress, host, true)
		if err != nil || normalized != address {
			return false
		}
		var blocked int64
		if err := fresh.Model(&entity.ScriptAddressReservation{}).Where("address_key = ? AND script_id = ?", key, blockedScriptAddressOwnerID).Count(&blocked).Error; err != nil || blocked > 0 {
			return false
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		script, err = ResolvePublicScriptWithDB(fresh, host, path)
		// Legacy addresses requiring a separate header cannot be tested by URL alone.
		if err != nil || script.LauncherLocator == nil || !script.LauncherBearerEnabled || BuildScriptCommandURL(script) != address {
			return false
		}
	default:
		return false
	}
	now := time.Now()
	status, _ := storedScriptAuthorizationStatus(script, short, now)
	if status != "active" || validateScriptLaunchActorWithDB(fresh, script, address) != nil {
		return false
	}
	var session entity.ScriptRunSession
	err = fresh.Where("lease_slot = ?", script.ID).Take(&session).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) && (err != nil || ScriptRunSessionUsableAt(session, now)) {
		return false
	}
	var pending int64
	return fresh.Model(&entity.ScriptExchangeToken{}).Where("script_id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", script.ID, "exchange", now).Count(&pending).Error == nil && pending == 0
}
