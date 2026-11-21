package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

func managementRuleHeaders() []string {
	return []string{"kind", "value", "match_type", "reason", "enabled", "expires_at"}
}

func managementWhitelistExportSpec() exportResourceSpec {
	return exportResourceSpec{
		resource: ExportResourceWhitelist,
		columns: []ExportColumn{
			exportColumn("kind"), maskedColumn("value", MaskExportBlacklistValue), exportColumn("match_type"),
			exportColumn("reason"), exportColumn("enabled"), exportColumn("expires_at"), exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var records []entity.WhitelistEntry
			if err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Order("id ASC"), limit).Find(&records).Error; err != nil {
				return nil, err
			}
			rows := make([][]string, 0, len(records))
			for _, row := range records {
				rows = append(rows, []string{row.Kind, row.Pattern, row.MatchType, row.Reason, formatExportBool(row.Enabled), formatRuleTransferTime(row.ExpiresAt), formatExportTime(row.CreatedAt)})
			}
			return rows, nil
		},
	}
}

func formatRuleTransferTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func parseRuleTransferTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, logger.ShanghaiLocation()); err == nil {
		return &parsed, nil
	}
	return parseImportExpiresAt(value)
}

func importManagementRules(db *gorm.DB, opts ImportDataOptions, resource, format string) (ImportResult, error) {
	header, rows, err := parseManagementRows(format, opts.Content, managementRuleHeaders())
	if err != nil {
		return ImportResult{}, err
	}
	if limit := importResolveLimit(resource, opts.Limit); limit > 0 && len(rows) > limit {
		return ImportResult{}, fmt.Errorf("%w：单次最多 %d 行", ErrImportRowLimitExceeded, limit)
	}
	index := importColumnIndex(header)
	if err := requireImportColumns(index, []string{"value"}); err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Total: len(rows), Details: make([]ImportRowResult, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	for i, row := range rows {
		value := importCell(row, index, "value")
		entry := ImportRowResult{Row: i + 2, Key: value}
		kind, match := strings.ToLower(importCell(row, index, "kind")), importCell(row, index, "match_type")
		reason := importCell(row, index, "reason")
		if reason == "" {
			reason = "批量导入"
		}
		var rowErr error
		if kind == "" {
			if resource == ImportResourceWhitelist {
				kind = WhitelistKindIP
			} else {
				kind, rowErr = inferBlacklistKindForImport(value)
			}
		}
		pattern, hash, table := "", "", entity.TableNameBlacklistEntry
		if rowErr == nil {
			if resource == ImportResourceWhitelist {
				var normalized NormalizedWhitelistValue
				normalized, rowErr = NormalizeWhitelistValue(kind, value, match)
				kind, pattern, hash, match, table = normalized.Kind, normalized.Pattern, normalized.ValueHash, normalized.MatchType, entity.TableNameWhitelistEntry
			} else {
				var normalized NormalizedBlacklistValue
				normalized, rowErr = NormalizeBlacklistValue(kind, value, match)
				kind, pattern, hash, match = normalized.Kind, normalized.Pattern, normalized.ValueHash, normalized.MatchType
			}
		}
		enabled := true
		if enabledText := importCell(row, index, "enabled"); rowErr == nil && enabledText != "" {
			enabled, rowErr = strconv.ParseBool(enabledText)
			if rowErr != nil {
				rowErr = fmt.Errorf("enabled 必须是 true / false / 1 / 0")
			}
		}
		expiresAt, timeErr := parseRuleTransferTime(importCell(row, index, "expires_at"))
		if rowErr == nil {
			rowErr = timeErr
		}
		key := kind + "\x00" + hash + "\x00" + match
		var count int64
		if rowErr == nil {
			rowErr = db.Session(&gorm.Session{NewDB: true}).Table(table).Where("kind = ? AND value_hash = ? AND match_type = ?", kind, hash, match).Count(&count).Error
		}
		if rowErr == nil && (seen[key] || count > 0) {
			entry.Result, entry.Reason = ImportRowSkipped, "已存在相同规则"
			if seen[key] {
				entry.Reason = "本批内重复"
			}
			result.Skipped++
			result.Details = append(result.Details, entry)
			continue
		}
		if rowErr == nil && !opts.DryRun {
			if resource == ImportResourceWhitelist {
				_, rowErr = CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{Kind: kind, Pattern: pattern, MatchType: match, Reason: reason, Enabled: enabled, ExpiresAt: expiresAt, CreatedBy: opts.Actor.ID})
			} else {
				_, rowErr = CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{Kind: kind, Pattern: pattern, MatchType: match, Reason: reason, Enabled: enabled, ExpiresAt: expiresAt, CreatedBy: opts.Actor.ID})
			}
		}
		if rowErr != nil {
			entry.Result, entry.Reason = ImportRowFailed, rowErr.Error()
			result.Failed++
		} else {
			entry.Result = ImportRowCreated
			if opts.DryRun {
				entry.Reason = "预览模式，未落库"
			}
			result.Succeeded++
			seen[key] = true
		}
		result.Details = append(result.Details, entry)
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	if !opts.DryRun && result.Succeeded > 0 {
		if resource == ImportResourceWhitelist {
			_ = ReloadDefaultWhitelistCache(db)
		} else {
			_ = ReloadDefaultBlacklistCache(db)
		}
	}
	return result, nil
}
