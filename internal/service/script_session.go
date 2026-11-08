package service

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrScriptRunIdentityRequired = errors.New("脚本身份码不能为空")

type ScriptRunSessionGrant struct {
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ScriptID      int32     `json:"scriptId"`
	AccessTokenID int32     `json:"accessTokenId"`
	HeartbeatURL  string    `json:"heartbeatUrl,omitempty"`
}

// ActivateScriptRunSessionWithDB consumes the short-lived activation ticket
// and binds a new 150-minute session to this executable identity.
func ActivateScriptRunSessionWithDB(db *gorm.DB, shortCode, activationToken, identity string) (ScriptRunSessionGrant, error) {
	return activateScriptRunSessionWithDB(db, shortCode, activationToken, identity, nil)
}

func activateScriptRunSessionWithDB(db *gorm.DB, shortCode, activationToken, identity string, lease *scriptRunLeaseRequest) (ScriptRunSessionGrant, error) {
	shortCode = strings.TrimSpace(shortCode)
	activationToken = strings.TrimSpace(activationToken)
	identity = strings.TrimSpace(identity)
	if shortCode == "" || activationToken == "" {
		return ScriptRunSessionGrant{}, ErrScriptTokenRequired
	}
	if identity == "" {
		return ScriptRunSessionGrant{}, ErrScriptRunIdentityRequired
	}
	if len(identity) > 256 {
		return ScriptRunSessionGrant{}, ErrScriptRunIdentityRequired
	}
	if db == nil {
		return ScriptRunSessionGrant{}, errors.New("数据库不可用")
	}

	var plainSessionToken string
	if lease != nil {
		plainSessionToken = scriptLeaseToken(activationToken, identity)
	} else {
		secret, err := randomScriptSecret(32)
		if err != nil {
			return ScriptRunSessionGrant{}, err
		}
		plainSessionToken = "sr_" + secret
	}
	var result ScriptRunSessionGrant
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var activation entity.ScriptExchangeToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ? AND purpose = ?", hashScriptSecret(activationToken), "activation").First(&activation).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		now := time.Now()
		if !now.Before(activation.ExpiresAt) || (lease == nil && activation.UsedAt != nil) {
			return ErrScriptTokenInvalid
		}
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND short_code = ? AND enabled = ? AND deleted_at IS NULL", activation.ScriptID, shortCode, true).First(&script).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if err := ValidateScriptDomainWithDB(tx, script.BaseDomain, script.UseWildcardDomain); err != nil {
			return err
		}
		if script.AccessTokenID <= 0 {
			return ErrScriptTokenInvalid
		}
		var accessToken entity.AccessToken
		if err := tx.Where("id = ? AND script_id = ? AND user_id = ? AND status = ?", script.AccessTokenID, script.ID, script.UserID, "enabled").First(&accessToken).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if !AccessTokenQuotaLimitValid(accessToken.TotalQuotaLimit) {
			return ErrScriptTokenInvalid
		}
		var user entity.User
		if err := tx.Where("id = ? AND status = ?", script.UserID, "enabled").First(&user).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		ttl := ScriptRunSessionTTL
		var zoneHash string
		if lease != nil {
			ttl = lease.ttl
			var err error
			zoneHash, err = scriptSourceZoneHash(tx, lease.clientIP)
			if err != nil {
				return err
			}
			if activation.UsedAt != nil {
				var prior entity.ScriptRunSession
				if err := tx.Where("token_hash = ? AND script_id = ? AND access_token_id = ? AND user_id = ?",
					hashScriptSecret(plainSessionToken), script.ID, accessToken.ID, user.ID).First(&prior).Error; err != nil ||
					!ScriptRunSessionUsableAt(prior, now) || prior.LeaseSlot == nil ||
					subtle.ConstantTimeCompare([]byte(prior.SourceZoneHash), []byte(zoneHash)) != 1 ||
					subtle.ConstantTimeCompare([]byte(prior.IdentityHash), []byte(hashScriptSecret(identity))) != 1 {
					return ErrScriptTokenInvalid
				}
				result = scriptRunLeaseGrant(prior, plainSessionToken, shortCode)
				return nil
			}
			if err := claimScriptLeaseSlot(tx, script.ID, now); err != nil {
				return err
			}
		}
		if err := consumeScriptExchangeTokenWithDB(tx, activation.ID, now); err != nil {
			return err
		}
		expiresAt := now.Add(ttl)
		session := entity.ScriptRunSession{
			ScriptID:      script.ID,
			UserID:        user.ID,
			AccessTokenID: accessToken.ID,
			TokenHash:     hashScriptSecret(plainSessionToken),
			IdentityHash:  hashScriptSecret(identity),
			ActivatedAt:   now,
			ExpiresAt:     expiresAt,
		}
		if lease != nil {
			session.LeaseSlot = &session.ScriptID
			session.SourceZoneHash = zoneHash
			session.LastHeartbeatAt = &now
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		result = ScriptRunSessionGrant{Token: plainSessionToken, ExpiresAt: expiresAt, ScriptID: script.ID, AccessTokenID: accessToken.ID}
		if lease != nil {
			result = scriptRunLeaseGrant(session, plainSessionToken, shortCode)
		}
		return nil
	})
	return result, err
}

// ResolveScriptRunSessionWithDB validates the bearer session and identity and
// returns the bound user and dedicated access-token context for search billing.
func ResolveScriptRunSessionWithDB(db *gorm.DB, authorization, identity string, clientIP ...string) (AccessTokenAuthContext, entity.ScriptRunSession, error) {
	tokenValue, err := parseBasicToken(authorization)
	if err != nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本运行会话令牌不能为空")
	}
	identity = strings.TrimSpace(identity)
	if identity == "" || len(identity) > 256 {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth(ErrScriptRunIdentityRequired.Error())
	}
	if db == nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("数据库不可用")
	}

	freshDB := db.Session(&gorm.Session{NewDB: true})
	var session entity.ScriptRunSession
	if err := freshDB.Where("token_hash = ?", hashScriptSecret(tokenValue)).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本运行会话不存在")
		}
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, err
	}
	now := time.Now()
	if !ScriptRunSessionUsableAt(session, now) {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本运行会话已过期或撤销")
	}
	identityHash := hashScriptSecret(identity)
	if subtle.ConstantTimeCompare([]byte(session.IdentityHash), []byte(identityHash)) != 1 {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本身份码不匹配")
	}
	if session.SourceZoneHash != "" {
		if len(clientIP) != 1 || !validScriptLeaseIdentity(identity) {
			return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本来源无效")
		}
		zone, err := scriptSourceZoneHash(freshDB, clientIP[0])
		if err != nil || subtle.ConstantTimeCompare([]byte(session.SourceZoneHash), []byte(zone)) != 1 {
			return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本来源不匹配")
		}
	}
	var script entity.UserScript
	if err := freshDB.Where("id = ? AND user_id = ? AND access_token_id = ? AND enabled = ? AND deleted_at IS NULL", session.ScriptID, session.UserID, session.AccessTokenID, true).First(&script).Error; err != nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本已停用或删除")
	}
	if err := ValidateScriptDomainWithDB(freshDB, script.BaseDomain, script.UseWildcardDomain); err != nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本域名不可用")
	}
	var accessToken entity.AccessToken
	if err := freshDB.Where("id = ? AND script_id = ? AND user_id = ? AND status = ?", session.AccessTokenID, session.ScriptID, session.UserID, "enabled").First(&accessToken).Error; err != nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本访问令牌已停用")
	}
	if !AccessTokenQuotaLimitValid(accessToken.TotalQuotaLimit) {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本访问令牌限额无效")
	}
	var user entity.User
	if err := freshDB.Where("id = ? AND status = ?", session.UserID, "enabled").First(&user).Error; err != nil {
		return AccessTokenAuthContext{}, entity.ScriptRunSession{}, invalidScriptSessionAuth("脚本所属用户已停用")
	}
	return AccessTokenAuthContext{User: user, AccessToken: accessToken}, session, nil
}

// ResolveSearchTokenWithScriptIdentity keeps credential selection uniform across
// the sync, task and AI fallback routes. A supplied identity selects the
// script-session credential explicitly; never fall back to a raw Key.
func ResolveSearchTokenWithScriptIdentity(db *gorm.DB, authorization, identity string, clientIP ...string) (AccessTokenAuthContext, entity.ScriptRunSession, error) {
	if strings.TrimSpace(identity) != "" {
		return ResolveScriptRunSessionWithDB(db, authorization, identity, clientIP...)
	}
	auth, err := ResolveAccessTokenFromBasicAuth(db, authorization)
	return auth, entity.ScriptRunSession{}, err
}

func DownloadScriptVersionByRunSessionWithDB(db *gorm.DB, shortCode, authorization, identity string) (entity.ScriptVersion, error) {
	_, session, err := ResolveScriptRunSessionWithDB(db, authorization, identity)
	if err != nil {
		return entity.ScriptVersion{}, err
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND short_code = ? AND enabled = ? AND deleted_at IS NULL", session.ScriptID, strings.TrimSpace(shortCode), true).First(&script).Error; err != nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	return DownloadScriptVersionWithDB(db, script.VersionID)
}

// DownloadScriptVersionByActivationTokenWithDB validates a one-time activation
// ticket without consuming it. The executable must redeem the ticket after it
// starts, once it has generated and can present its identity code.
func DownloadScriptVersionByActivationTokenWithDB(db *gorm.DB, shortCode, activationToken string) (entity.ScriptVersion, error) {
	shortCode = strings.TrimSpace(shortCode)
	activationToken = strings.TrimSpace(activationToken)
	if shortCode == "" || activationToken == "" {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	if db == nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	var activation entity.ScriptExchangeToken
	if err := db.Session(&gorm.Session{NewDB: true}).Where("token_hash = ? AND purpose = ?", hashScriptSecret(activationToken), "activation").First(&activation).Error; err != nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	if activation.UsedAt != nil || !time.Now().Before(activation.ExpiresAt) {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND short_code = ? AND enabled = ? AND deleted_at IS NULL", activation.ScriptID, shortCode, true).First(&script).Error; err != nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	if err := validateScriptLaunchActorWithDB(db, script); err != nil {
		return entity.ScriptVersion{}, err
	}
	versionID := activation.VersionID
	if versionID <= 0 {
		versionID = script.VersionID
	}
	return DownloadScriptVersionWithDB(db, versionID)
}

func invalidScriptSessionAuth(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccessTokenAuthorization, reason)
}
