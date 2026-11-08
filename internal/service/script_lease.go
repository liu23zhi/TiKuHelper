package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const ScriptHeartbeatTimeout = 5 * time.Minute

var (
	ErrScriptDeviceBusy        = errors.New("该脚本已有设备在线")
	ErrScriptSourceUnavailable = errors.New("无法确认脚本来源归属地")
	ErrScriptDurationInvalid   = errors.New("脚本会话时长必须在 1 至 150 分钟之间")
)

type scriptRunLeaseRequest struct {
	clientIP string
	ttl      time.Duration
}

func validScriptLeaseIdentity(identity string) bool {
	if len(identity) != 64 || strings.ToLower(identity) != identity {
		return false
	}
	decoded, err := hex.DecodeString(identity)
	return err == nil && len(decoded) == 32
}

func ActivateLeasedScriptRunSessionWithDB(db *gorm.DB, shortCode, ticket, identity, clientIP string, durationMinutes int) (ScriptRunSessionGrant, error) {
	if len(ticket) > 256 || !validScriptLeaseIdentity(strings.TrimSpace(identity)) {
		return ScriptRunSessionGrant{}, ErrScriptRunIdentityRequired
	}
	if durationMinutes < 0 || durationMinutes > int(ScriptRunSessionTTL/time.Minute) {
		return ScriptRunSessionGrant{}, ErrScriptDurationInvalid
	}
	ttl := ScriptRunSessionTTL
	if durationMinutes > 0 {
		ttl = time.Duration(durationMinutes) * time.Minute
	}
	return activateScriptRunSessionWithDB(db, shortCode, ticket, identity, &scriptRunLeaseRequest{clientIP, ttl})
}

// A response-lost retry with the exact same ticket and run identity gets
// the same capability, without storing the plaintext ticket or token.
func scriptLeaseToken(ticket, identity string) string {
	mac := hmac.New(sha256.New, []byte(ticket))
	_, _ = mac.Write([]byte("tikuhelper/script-lease/v1\x00" + identity))
	return "sr_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func scriptRunLeaseGrant(session entity.ScriptRunSession, token, shortCode string) ScriptRunSessionGrant {
	return ScriptRunSessionGrant{
		Token: token, ExpiresAt: session.ExpiresAt, ScriptID: session.ScriptID, AccessTokenID: session.AccessTokenID,
		HeartbeatURL: "/adapter-service/script-command/" + shortCode + "/heartbeat",
	}
}

// scriptSourceZoneHash binds to the region from the existing trusted location
// cache, not to a region asserted by the client. Cache misses cannot cause
// outbound calls. Private addresses have no public region; bind them to the
// exact normalized address instead of treating all private networks alike.
func scriptSourceZoneHash(db *gorm.DB, clientIP string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(clientIP))
	if err != nil || !ip.IsValid() || ip.Zone() != "" {
		return "", ErrScriptSourceUnavailable
	}
	ip = ip.Unmap()
	if ip.IsPrivate() || ip.IsLoopback() {
		return hashScriptSecret("local-ip:" + ip.String()), nil
	}
	if !ip.IsGlobalUnicast() {
		return "", ErrScriptSourceUnavailable
	}
	key := ip.String()
	source, err := normalizeLocationSource(entity.LocationKindIP, "")
	if err != nil {
		return "", ErrScriptSourceUnavailable
	}
	cached := loadLocationCacheRows(db, entity.LocationKindIP, source, []string{key})
	row, ok := cached[locationCacheLookupKey(key)]
	if !ok {
		return "", ErrScriptSourceUnavailable
	}
	region := strings.ToLower(strings.Join(strings.Fields(ipLocationFromCacheRow(row).Region), " "))
	switch region {
	case "", "未知", "unknown", "-", "本地地址", "私有地址":
		return "", ErrScriptSourceUnavailable
	}
	return hashScriptSecret("geo-region:" + region), nil
}

func ScriptRunSessionUsableAt(session entity.ScriptRunSession, now time.Time) bool {
	if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return false
	}
	if session.SourceZoneHash == "" {
		return session.LeaseSlot == nil && session.LastHeartbeatAt == nil
	}
	return session.LeaseSlot != nil && *session.LeaseSlot == session.ScriptID &&
		session.LastHeartbeatAt != nil && now.Before(session.LastHeartbeatAt.Add(ScriptHeartbeatTimeout))
}

func claimScriptLeaseSlot(tx *gorm.DB, scriptID int32, now time.Time) error {
	if err := tx.Model(&entity.ScriptRunSession{}).Where(
		"lease_slot = ? AND (revoked_at IS NOT NULL OR expires_at <= ? OR last_heartbeat_at IS NULL OR last_heartbeat_at <= ?)",
		scriptID, now, now.Add(-ScriptHeartbeatTimeout)).
		Updates(map[string]any{"lease_slot": nil, "revoked_at": now}).Error; err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&entity.ScriptRunSession{}).Where("lease_slot = ?", scriptID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrScriptDeviceBusy
	}
	return nil // The unique index is the final guard if another writer raced.
}

func HeartbeatScriptRunSessionWithDB(db *gorm.DB, shortCode, authorization, identity, clientIP string) (time.Time, error) {
	_, session, err := ResolveScriptRunSessionWithDB(db, authorization, identity, clientIP)
	if err != nil {
		return time.Time{}, err
	}
	if session.LeaseSlot == nil {
		return time.Time{}, ErrScriptTokenInvalid
	}
	var script entity.UserScript
	if err := db.Select("id").Where("id = ? AND short_code = ? AND enabled = ? AND deleted_at IS NULL",
		session.ScriptID, strings.TrimSpace(shortCode), true).First(&script).Error; err != nil {
		return time.Time{}, ErrScriptTokenInvalid
	}
	now := time.Now()
	updated := db.Model(&entity.ScriptRunSession{}).
		Where("id = ? AND lease_slot = ? AND revoked_at IS NULL AND expires_at > ? AND last_heartbeat_at > ?",
			session.ID, session.ScriptID, now, now.Add(-ScriptHeartbeatTimeout)).
		UpdateColumn("last_heartbeat_at", gorm.Expr("CASE WHEN last_heartbeat_at < ? THEN ? ELSE last_heartbeat_at END", now, now))
	if updated.Error != nil {
		return time.Time{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		// A concurrent heartbeat at the same DB timestamp may already have
		// refreshed the row. A no-op is safe only while the same lease is live.
		var current entity.ScriptRunSession
		if err := db.Where("id = ? AND lease_slot = ?", session.ID, session.ScriptID).First(&current).Error; err != nil ||
			!ScriptRunSessionUsableAt(current, now) {
			return time.Time{}, ErrScriptTokenInvalid
		}
	}
	return session.ExpiresAt, nil
}
