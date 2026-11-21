package service

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func rotateScriptLocatorInTx(tx *gorm.DB, script entity.UserScript) (string, error) {
	if err := preservePreviousScriptAddress(tx, BuildScriptCommandURL(script), scriptAddressFull, script.ID); err != nil {
		return "", err
	}
	for range 10 {
		id, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		value := id.String()
		candidate := script
		candidate.LauncherLocator = &value
		address := BuildScriptCommandURL(candidate)
		if err := checkScriptAddressAvailable(tx, address, scriptAddressFull, 0); err != nil {
			if errors.Is(err, ErrScriptShortAddressInUse) {
				continue
			}
			return "", err
		}
		if err := reserveScriptAddress(tx, address, scriptAddressFull, script.ID); err != nil {
			if errors.Is(err, ErrScriptShortAddressInUse) {
				continue
			}
			return "", err
		}
		return value, nil
	}
	return "", ErrScriptShortAddressInUse
}

// IssuePublicScriptExchangeTokenByLocatorWithDB admits only the exact generated
// UUID address; legacy short codes still require the old header capability.
func IssuePublicScriptExchangeTokenByLocatorWithDB(db *gorm.DB, requestHost, requestPath string, ttl time.Duration) (ScriptExchangeGrant, error) {
	if db == nil {
		return ScriptExchangeGrant{}, ErrScriptNotFound
	}
	if ttl <= 0 || ttl > ScriptExchangeTokenTTL {
		ttl = ScriptExchangeTokenTTL
	}
	var result ScriptExchangeGrant
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		resolved, err := ResolvePublicScriptWithDB(tx, requestHost, requestPath)
		if err != nil {
			return err
		}
		if resolved.LauncherLocator == nil || !resolved.LauncherBearerEnabled {
			return ErrScriptNotFound
		}
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND launcher_locator = ? AND launcher_bearer_enabled = ? AND deleted_at IS NULL AND enabled = ?", resolved.ID, *resolved.LauncherLocator, true, true).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		if err := consumeScriptLauncherAuthorizationInTx(tx, script, time.Now()); err != nil {
			return err
		}
		grant, err := IssueScriptExchangeTokenWithDB(tx, entity.User{ID: script.UserID}, script.ID, ttl)
		if err != nil {
			return err
		}
		if err := tx.Model(&entity.ScriptExchangeToken{}).Where("token_hash = ?", hashScriptSecret(grant.Token)).Updates(map[string]any{"authorization_kind": "full", "authorization_generation": script.LauncherGeneration}).Error; err != nil {
			return err
		}
		grant.LaunchOrigin = ScriptCommandOrigin(script)
		result = grant
		return nil
	})
	return result, err
}
