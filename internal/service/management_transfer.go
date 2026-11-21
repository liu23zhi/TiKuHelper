package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func managementLocationHeaders() []string {
	return []string{"lookup_key", "source", "address", "raw_json", "fetched_at"}
}

func managementLocationExportSpec(kind string) exportResourceSpec {
	resource, mask := ExportResourceIPLocations, MaskExportIP
	if kind == entity.LocationKindPhone {
		resource, mask = ExportResourcePhoneLocations, MaskExportPhone
	}
	return exportResourceSpec{
		resource: resource,
		columns: []ExportColumn{
			maskedColumn("lookup_key", mask), exportColumn("source"), exportColumn("address"),
			// 上游原始响应也可能带完整 IP / 手机号。脱敏文件不携带原始响应。
			maskedColumn("raw_json", func(string) string { return "" }), exportColumn("fetched_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var records []entity.LocationCache
			if err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Where("kind = ?", kind).
				Order("lookup_key ASC, source ASC"), limit).Find(&records).Error; err != nil {
				return nil, err
			}
			rows := make([][]string, 0, len(records))
			for _, record := range records {
				rows = append(rows, []string{record.LookupKey, record.Source, record.Address, record.RawJSON, record.FetchedAt.Format(time.RFC3339Nano)})
			}
			return rows, nil
		},
	}
}

func managementBindingExportSpec(kind string) exportResourceSpec {
	resource, mask := ExportResourceWechatBindings, MaskExportWechatID
	if kind == entity.BindingKindPhone {
		resource, mask = ExportResourcePhoneBindings, MaskExportPhone
	}
	return exportResourceSpec{
		resource: resource,
		columns: []ExportColumn{
			exportColumn("id"), exportColumn("user_id"), exportColumn("username_snapshot"), exportColumn("kind"),
			maskedColumn("value", mask), exportColumn("state"), exportColumn("source"), exportColumn("bound_at"),
			exportColumn("released_at"), exportColumn("release_reason"), exportColumn("release_source"),
			exportColumn("operator_user_id"), exportColumn("void_reason"), exportColumn("voided_by_user_id"),
			exportColumn("voided_at"), exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var records []entity.UserBindingRecord
			if err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Where("kind = ?", kind).
				Order("id ASC"), limit).Find(&records).Error; err != nil {
				return nil, err
			}
			rows := make([][]string, 0, len(records))
			for _, record := range records {
				rows = append(rows, []string{
					strconv.FormatInt(record.ID, 10), strconv.FormatInt(int64(record.UserID), 10), record.UsernameSnapshot, record.Kind,
					record.Value, record.State, record.Source, formatExportTimePtr(record.BoundAt), formatExportTimePtr(record.ReleasedAt),
					record.ReleaseReason, record.ReleaseSource, strconv.FormatInt(int64(record.OperatorUserID), 10), record.VoidReason,
					strconv.FormatInt(int64(record.VoidedByUserID), 10), formatExportTimePtr(record.VoidedAt), formatExportTime(record.CreatedAt),
				})
			}
			return rows, nil
		},
	}
}

// 缓存 JSON 是可回灌的记录数组；题库 JSON 解析器有专用字段，不能混用。
func parseManagementLocationRows(format string, content []byte) ([]string, [][]string, error) {
	return parseManagementRows(format, content, managementLocationHeaders())
}

func parseManagementRows(format string, content []byte, header []string) ([]string, [][]string, error) {
	if format != ImportFormatJSON {
		return ParseImportRows(format, content)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(bytesWithoutBOM(content), &records); err != nil {
		return nil, nil, fmt.Errorf("JSON 必须是记录数组：%w", err)
	}
	if len(records) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	rows := make([][]string, 0, len(records))
	for _, record := range records {
		row := make([]string, len(header))
		for i, key := range header {
			value := record[key]
			if len(value) == 0 || string(value) == "null" {
				continue
			}
			if err := json.Unmarshal(value, &row[i]); err != nil {
				// 原始响应允许对象；其它列只接受字符串，让非法键进入逐行校验。
				if key == "raw_json" || key == "enabled" || key == "user_id" {
					row[i] = string(value)
				} else {
					row[i] = "\x00"
				}
			}
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

func bytesWithoutBOM(content []byte) []byte {
	return []byte(strings.TrimPrefix(strings.TrimSpace(string(content)), "\uFEFF"))
}

func importManagementLocations(db *gorm.DB, opts ImportDataOptions, resource, format string) (ImportResult, error) {
	header, rows, err := parseManagementLocationRows(format, opts.Content)
	if err != nil {
		return ImportResult{}, err
	}
	if limit := importResolveLimit(resource, opts.Limit); limit > 0 && len(rows) > limit {
		return ImportResult{}, fmt.Errorf("%w：单次最多 %d 行，当前 %d 行", ErrImportRowLimitExceeded, limit, len(rows))
	}
	index := importColumnIndex(header)
	if err := requireImportColumns(index, []string{"lookup_key", "address"}); err != nil {
		return ImportResult{}, err
	}
	kind := entity.LocationKindIP
	if resource == ImportResourcePhoneLocations {
		kind = entity.LocationKindPhone
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	result := ImportResult{Total: len(rows), Details: make([]ImportRowResult, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	for i, row := range rows {
		entry := ImportRowResult{Row: i + 2, Key: importCell(row, index, "lookup_key")}
		_, key, rowErr := normalizeLocationAdminKey(kind, entry.Key)
		source, sourceErr := normalizeLocationRecordSource(kind, importCell(row, index, "source"))
		address, raw := importCell(row, index, "address"), importCell(row, index, "raw_json")
		if rowErr == nil {
			rowErr = sourceErr
		}
		if rowErr == nil && (utf8.RuneCountInString(address) > 255 || strings.ContainsRune(address, 0)) {
			rowErr = fmt.Errorf("address 必须是不超过 255 字的文本")
		}
		if rowErr == nil && raw != "" && !json.Valid([]byte(raw)) {
			rowErr = fmt.Errorf("raw_json 必须是有效 JSON")
		}
		fetchedAt := now()
		if value := importCell(row, index, "fetched_at"); rowErr == nil && value != "" {
			fetchedAt, rowErr = time.Parse(time.RFC3339Nano, value)
			if rowErr != nil {
				rowErr = fmt.Errorf("fetched_at 必须是 RFC3339 时间")
			}
		}
		if rowErr == nil && seen[key+"\x00"+source] {
			entry.Result, entry.Reason = ImportRowSkipped, "本批中已出现相同查询键和来源"
			result.Skipped++
			result.Details = append(result.Details, entry)
			continue
		}
		if rowErr == nil {
			var count int64
			rowErr = db.Session(&gorm.Session{NewDB: true}).Model(&entity.LocationCache{}).
				Where("kind = ? AND lookup_key = ? AND source = ?", kind, key, source).Count(&count).Error
			entry.Result = ImportRowCreated
			if count > 0 {
				entry.Result = ImportRowUpdated
			}
			if rowErr == nil && !opts.DryRun {
				record := entity.LocationCache{Kind: kind, LookupKey: key, Source: source, Address: address, RawJSON: raw, FetchedAt: fetchedAt, UpdatedAt: now()}
				rowErr = db.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "kind"}, {Name: "lookup_key"}, {Name: "source"}},
					DoUpdates: clause.AssignmentColumns([]string{"address", "raw_json", "fetched_at", "updated_at"}),
				}).Create(&record).Error
			}
		}
		if rowErr != nil {
			entry.Result, entry.Reason = ImportRowFailed, rowErr.Error()
			result.Failed++
		} else {
			seen[key+"\x00"+source] = true
			result.Succeeded++
		}
		result.Details = append(result.Details, entry)
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	return result, nil
}
