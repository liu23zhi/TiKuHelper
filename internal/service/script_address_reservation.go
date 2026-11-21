package service

import (
	"errors"
	"net/url"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	scriptAddressFull           = "full"
	scriptAddressShort          = "short"
	blockedScriptAddressOwnerID = 0
	scriptAddressBackfillKey    = "script_address_reservations_backfilled_v1"
)

// BackfillScriptAddressReservationsWithDB runs at startup before an exchange
// request can clean up old tickets. Conflicting historical owners leave a
// reservation with no owner, so neither can claim the address again.
func BackfillScriptAddressReservationsWithDB(db *gorm.DB) error {
	_, done, err := getSystemSettingByKey(db, scriptAddressBackfillKey)
	if err != nil || done {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		preserve := func(address string, scriptID int32) error {
			_, _, key := parseScriptReservedAddress(address)
			if key == "" {
				return nil
			}
			err := reserveScriptAddress(tx, address, scriptAddressShort, scriptID)
			if errors.Is(err, ErrScriptShortAddressInUse) {
				return tx.Model(&entity.ScriptAddressReservation{}).Where("address_key = ?", key).
					Updates(map[string]any{"script_id": blockedScriptAddressOwnerID, "kind": scriptAddressShort}).Error
			}
			return err
		}
		var scripts []entity.UserScript
		result := tx.Select("id", "short_entry_address").Where("short_entry_address <> ''").
			FindInBatches(&scripts, 500, func(_ *gorm.DB, _ int) error {
				for _, script := range scripts {
					if err := preserve(script.ShortEntryAddress, script.ID); err != nil {
						return err
					}
				}
				return nil
			})
		if result.Error != nil {
			return result.Error
		}
		var tickets []entity.ScriptExchangeToken
		result = tx.Select("id", "script_id", "launch_address").Where("launch_address <> ''").
			FindInBatches(&tickets, 500, func(_ *gorm.DB, _ int) error {
				for _, ticket := range tickets {
					if err := preserve(ticket.LaunchAddress, ticket.ScriptID); err != nil {
						return err
					}
				}
				return nil
			})
		if result.Error != nil {
			return result.Error
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.SystemSetting{Key: scriptAddressBackfillKey, Value: "1"}).Error
	})
}

func parseScriptReservedAddress(address string) (host, path, key string) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return "", "", ""
	}
	host = normalizeScriptLauncherHost(parsed.Host)
	path, err = cleanScriptRequestPath(parsed.Path)
	if host == "" || err != nil {
		return "", "", ""
	}
	return host, path, shortEntryAddressKey(host, path)
}

func checkScriptAddressAvailable(db *gorm.DB, address, kind string, scriptID int32) error {
	host, path, key := parseScriptReservedAddress(address)
	if key == "" {
		return ErrScriptShortAddressInvalid
	}
	var reservation entity.ScriptAddressReservation
	err := db.Where("address_key = ?", key).Take(&reservation).Error
	if err == nil && (reservation.ScriptID == blockedScriptAddressOwnerID || reservation.ScriptID != scriptID || reservation.Kind != kind) {
		return ErrScriptShortAddressInUse
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// Existing installations may have addresses assigned before reservations
	// were introduced. Keep checking the current script rows, including deleted
	// scripts. Replacement saves the old address before writing the new one.
	short := db.Model(&entity.UserScript{}).Where("short_entry_address = ?", "https://"+host+path)
	if kind == scriptAddressShort {
		short = short.Where("id <> ?", scriptID)
	}
	var count int64
	if err := short.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrScriptShortAddressInUse
	}
	var locator string
	if path == "" {
		locator, _, _ = strings.Cut(host, ".")
	} else {
		segments := scriptPathSegments(path)
		locator = segments[len(segments)-1]
	}
	if isScriptLauncherLocator(locator) {
		existing, err := findGeneratedScriptLauncherWithDB(db, host, path, locator)
		if err == nil && (kind != scriptAddressFull || existing.ID != scriptID) {
			return ErrScriptShortAddressInUse
		}
		if err != nil && !errors.Is(err, ErrScriptNotFound) {
			return err
		}
		return nil
	}
	if !isScriptShortCodeSegment(locator) {
		return nil
	}
	var existing entity.UserScript
	err = db.Where("short_code = ? AND launcher_locator IS NULL AND use_wildcard_domain = ?", locator, path == "").Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if kind == scriptAddressFull && existing.ID == scriptID {
		return nil
	}
	existingHost, _, _ := parseScriptReservedAddress(BuildScriptCommandURL(existing))
	if existingHost == host {
		return ErrScriptShortAddressInUse
	}
	return nil
}

// Insert or keep the reservation under the unique key inside the script write
// transaction. The locking read also sees a concurrent winner on MySQL, even
// if the earlier availability check used an older transaction snapshot.
func reserveScriptAddress(db *gorm.DB, address, kind string, scriptID int32) error {
	_, _, key := parseScriptReservedAddress(address)
	if key == "" {
		return ErrScriptShortAddressInvalid
	}
	reservation := entity.ScriptAddressReservation{AddressKey: key, ScriptID: scriptID, Kind: kind}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&reservation).Error; err != nil {
		return err
	}
	var stored entity.ScriptAddressReservation
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("address_key = ?", key).Take(&stored).Error; err != nil {
		return err
	}
	if stored.ScriptID != scriptID || stored.Kind != kind {
		return ErrScriptShortAddressInUse
	}
	return nil
}

func preservePreviousScriptAddress(db *gorm.DB, address, kind string, scriptID int32) error {
	if _, _, key := parseScriptReservedAddress(address); key == "" {
		return nil
	}
	// Some old rows could have already shared one address. Keeping the
	// existing reservation is enough to prevent it being handed out again.
	err := reserveScriptAddress(db, address, kind, scriptID)
	if errors.Is(err, ErrScriptShortAddressInUse) {
		return nil
	}
	return err
}
