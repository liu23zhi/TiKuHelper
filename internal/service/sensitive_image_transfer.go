package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ImportResourceSensitiveImages = "sensitive_images"

// Bound aggregate bytes as well as record count: 1000 large pictures must not
// turn one asynchronous job into tens of GiB of base64/spreadsheet allocations.
const maxSensitiveImageTransferBytes = 64 << 20

func sensitiveImageTransferHeaders() []string {
	return []string{"image_sha256", "status", "reason", "content_type", "image_base64", "updated_at"}
}

func init() {
	columns := make([]ExportColumn, 0, 6)
	for _, name := range sensitiveImageTransferHeaders() {
		columns = append(columns, exportColumn(name))
	}
	exportResourceSpecs[ImportResourceSensitiveImages] = exportResourceSpec{resource: ImportResourceSensitiveImages, columns: columns, maxRows: 1000,
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var entries []SensitiveImageHistoryEntry
			if err := exportLimit(SensitiveImageHistoryQuery(db).Order("updated_at DESC, image_sha256"), limit).Find(&entries).Error; err != nil {
				return nil, err
			}
			rows := make([][]string, 0, len(entries))
			var imageBytes int64
			for _, entry := range entries {
				encoded := ""
				plain, _, err := LoadSensitiveImageBytesWithDB(db, entry.ImageSHA256)
				if err == nil {
					imageBytes += int64(len(plain))
					if imageBytes > maxSensitiveImageTransferBytes {
						return nil, fmt.Errorf("图片导出内容超过 64 MiB，请减少本次导出条数后重试")
					}
					encoded = base64.StdEncoding.EncodeToString(plain)
				}
				rows = append(rows, []string{entry.ImageSHA256, entry.Status, entry.Reason, entry.ContentType, encoded, entry.UpdatedAt.Format(time.RFC3339Nano)})
			}
			return rows, nil
		}}
}

// Spreadsheets carry real images anchored to their corresponding row. JSON
// carries the same bytes as base64, so both representations can round-trip.
func parseSensitiveImageTransfer(format string, content []byte) ([]string, [][]string, error) {
	if format != ImportFormatXlsx {
		return parseManagementRows(format, content, sensitiveImageTransferHeaders())
	}
	file, err := excelize.OpenReader(bytes.NewReader(content), excelize.Options{UnzipSizeLimit: maxSensitiveImageTransferBytes, UnzipXMLSizeLimit: 8 << 20})
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	sheet := file.GetSheetName(0)
	iterator, err := file.Rows(sheet)
	if err != nil {
		return nil, nil, err
	}
	defer iterator.Close()
	var header []string
	var rows [][]string
	for iterator.Next() {
		cells, readErr := iterator.Columns()
		if readErr != nil {
			return nil, nil, readErr
		}
		if header == nil {
			header = cells
			continue
		}
		rows = append(rows, cells)
		if len(rows) > 1000 {
			return nil, nil, fmt.Errorf("%w：图片记录单次最多 1000 条", ErrImportRowLimitExceeded)
		}
	}
	if err := iterator.Error(); err != nil {
		return nil, nil, err
	}
	if len(header) == 0 || len(rows) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	index := importColumnIndex(header)
	imageColumn, ok := index["image_base64"]
	if !ok {
		imageColumn = len(header)
		header = append(header, "image_base64")
	}
	var pictureBytes int64
	for i := range rows {
		for len(rows[i]) <= imageColumn {
			rows[i] = append(rows[i], "")
		}
		axis, _ := excelize.CoordinatesToCellName(imageColumn+1, i+2)
		pictures, pictureErr := file.GetPictures(sheet, axis)
		if pictureErr != nil {
			return nil, nil, pictureErr
		}
		if len(pictures) > 1 {
			return nil, nil, fmt.Errorf("第 %d 行图片单元格只能包含一张图片", i+2)
		}
		if len(pictures) == 1 {
			pictureBytes += int64(len(pictures[0].File))
			if len(pictures[0].File) > MaxTicketAttachmentBytes || pictureBytes > maxSensitiveImageTransferBytes {
				return nil, nil, fmt.Errorf("图片导入内容超出单图或总大小限制，请分批上传")
			}
			rows[i][imageColumn] = base64.StdEncoding.EncodeToString(pictures[0].File)
		}
	}
	return header, rows, nil
}

func importSensitiveImages(db *gorm.DB, opts ImportDataOptions, format string) (ImportResult, error) {
	header, rows, err := parseSensitiveImageTransfer(format, opts.Content)
	if err != nil {
		return ImportResult{}, err
	}
	if len(rows) > 1000 {
		return ImportResult{}, fmt.Errorf("%w：图片记录单次最多 1000 条", ErrImportRowLimitExceeded)
	}
	index := importColumnIndex(header)
	if err := requireImportColumns(index, []string{"image_base64", "status"}); err != nil {
		return ImportResult{}, err
	}
	// Check the entire job before committing its first row. A late size failure
	// must not leave a partially applied import behind an overall job failure.
	if err := validateSensitiveImageTransferBudget(index, rows); err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Total: len(rows)}
	for i, row := range rows {
		value := func(key string) string {
			col, ok := index[key]
			if !ok || col >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[col])
		}
		detail := ImportRowResult{Row: i + 2, Key: value("image_sha256")}
		status, valid := normalizeImageTransferStatus(value("status"))
		if !valid {
			detail.Reason = "检测状态必须为 clean、sensitive 或 error"
		} else if value("image_base64") == "" {
			skipped, restoreErr := restoreSensitiveImageMetadata(db, opts.DryRun, value("image_sha256"), status, value("content_type"), value("reason"), value("updated_at"))
			if restoreErr != nil {
				detail.Reason = restoreErr.Error()
			} else if skipped {
				detail.Result, detail.Reason = ImportRowSkipped, "已有较新或同一结论，或图片仍在检测中，保留现有记录"
				result.Skipped++
				result.Details = append(result.Details, detail)
				continue
			}
		} else {
			encoded := sensitiveImageTransferEncodedValue(value("image_base64"))
			var plain []byte
			var decodeErr error
			if len(encoded) > base64.StdEncoding.EncodedLen(MaxTicketAttachmentBytes) {
				decodeErr = fmt.Errorf("encoded picture too large")
			} else {
				plain, decodeErr = base64.StdEncoding.DecodeString(encoded)
			}
			if decodeErr != nil || len(plain) == 0 || len(plain) > MaxTicketAttachmentBytes {
				detail.Reason = "图片 base64 无效或超出图片大小限制"
			} else {
				mime, safe, safeErr := safeTicketAttachmentImage(plain)
				cfg, kind, imageErr := image.DecodeConfig(bytes.NewReader(plain))
				if safeErr != nil || imageErr != nil || (kind != "png" && kind != "jpeg" && kind != "gif" && kind != "webp") || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > MaxTicketAttachmentPixels {
					detail.Reason = "图片格式或尺寸不符合要求"
				} else {
					sum := sha256.Sum256(plain)
					sha := hex.EncodeToString(sum[:])
					detail.Key = sha
					if declared := value("image_sha256"); declared != "" && !strings.EqualFold(declared, sha) {
						detail.Reason = "图片内容与 SHA256 不一致"
					} else {
						// Store only the normalized image; the source may include EXIF,
						// GPS or trailing payload that the attachment upload path strips.
						plain = safe
						safeSum := sha256.Sum256(plain)
						sha = hex.EncodeToString(safeSum[:])
						detail.Key = sha
						if !opts.DryRun {
							now := time.Now()
							if opts.Now != nil {
								now = opts.Now()
							}
							writeErr := db.Transaction(func(tx *gorm.DB) error {
								if _, _, e := StoreTicketAttachmentBlobWithDB(tx, sha, mime, plain); e != nil {
									return e
								}
								entry := entity.SensitiveImageCacheEntry{ImageSHA256: sha, Status: status, ContentType: mime, FileSize: int64(len(plain)), Reason: value("reason"), Provider: "admin-import", FirstSeenAt: now, UpdatedAt: now}
								if e := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "image_sha256"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "reason", "content_type", "file_size", "provider", "updated_at"})}).Create(&entry).Error; e != nil {
									return e
								}
								_, syncErr := SyncSensitiveImageScanStateWithDB(tx, sha, status)
								return syncErr
							})
							if writeErr != nil {
								detail.Reason = "保存图片检测记录失败：" + writeErr.Error()
							}
						}
					}
				}
			}
		}
		if detail.Reason != "" {
			detail.Result = "failed"
			result.Failed++
		} else {
			detail.Result = "updated"
			result.Succeeded++
		}
		result.Details = append(result.Details, detail)
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	return result, nil
}

// A backup without bytes restores history only. It cannot claim that an upload
// passed a scan, change a blob, or replace an equally recent/newer verdict.
func restoreSensitiveImageMetadata(db *gorm.DB, dryRun bool, sha, status, contentType, reason, updated string) (bool, error) {
	sha = strings.ToLower(sha)
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != sha256.Size {
		return false, fmt.Errorf("无原图的记录必须提供有效的 64 位 image_sha256")
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil || checkedAt.IsZero() {
		return false, fmt.Errorf("无原图的记录必须提供有效的 RFC3339 updated_at")
	}
	checkedAt = checkedAt.UTC()
	if contentType != "" && contentType != "image/png" && contentType != "image/jpeg" && contentType != "image/gif" && contentType != "image/webp" {
		return false, fmt.Errorf("content_type 必须是受支持的图片类型")
	}
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 255 {
		return false, fmt.Errorf("检测原因不能超过 255 字")
	}
	skipped := false
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var current entity.SensitiveImageCacheEntry
		read := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_sha256 = ?", sha).Take(&current)
		found := read.Error == nil
		if read.Error != nil && !errors.Is(read.Error, gorm.ErrRecordNotFound) {
			return read.Error
		}
		if found && (current.Status == entity.SensitiveImageCacheStatusPending || !current.UpdatedAt.Before(checkedAt)) {
			skipped = true
			return nil
		}
		// Pending attachment/blob state is an active detection, not historical
		// evidence. Inserting a clean cache here could let its worker reuse it.
		if tx.Migrator().HasTable(&entity.TicketAttachment{}) {
			var pending int64
			if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{}).Where("sha256 = ? AND scan_state IN ?", sha, []string{entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning}).Count(&pending).Error; err != nil {
				return err
			}
			if pending > 0 {
				skipped = true
				return nil
			}
			var history []SensitiveImageHistoryEntry
			if err := SensitiveImageHistoryQuery(tx).Where("image_sha256 = ?", sha).Find(&history).Error; err != nil {
				return err
			}
			for _, row := range history {
				if !row.UpdatedAt.Before(checkedAt) {
					skipped = true
					return nil
				}
			}
		}
		if tx.Migrator().HasTable(&entity.TicketAttachmentBlob{}) {
			var blob entity.TicketAttachmentBlob
			read := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("sha256 = ?", sha).Take(&blob)
			if read.Error != nil && !errors.Is(read.Error, gorm.ErrRecordNotFound) {
				return read.Error
			}
			if read.Error == nil && (blob.ScanState == entity.TicketAttachmentScanPending || blob.ScanState == entity.TicketAttachmentScanScanning || (blob.CheckedAt != nil && !blob.CheckedAt.Before(checkedAt))) {
				skipped = true
				return nil
			}
		}
		if dryRun {
			return nil
		}
		if found {
			// Include the observed version so a concurrent detector never loses
			// its result, even on databases where FOR UPDATE is unavailable.
			write := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.SensitiveImageCacheEntry{}).
				Where("image_sha256 = ? AND status = ? AND updated_at = ?", sha, current.Status, current.UpdatedAt).
				Updates(map[string]any{"status": status, "reason": reason, "provider": "admin-backup", "updated_at": checkedAt})
			skipped = write.RowsAffected == 0
			return write.Error
		}
		entry := entity.SensitiveImageCacheEntry{ImageSHA256: sha, Status: status, Reason: reason, ContentType: contentType, Provider: "admin-backup", FirstSeenAt: checkedAt, UpdatedAt: checkedAt}
		write := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
		skipped = write.RowsAffected == 0
		return write.Error
	})
	return skipped, err
}

func sensitiveImageTransferEncodedValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data:") {
		if comma := strings.Index(value, ","); comma >= 0 {
			return value[comma+1:]
		}
	}
	return value
}

func validateSensitiveImageTransferBudget(index map[string]int, rows [][]string) error {
	column, ok := index["image_base64"]
	if !ok {
		return nil
	}
	var total int64
	for _, row := range rows {
		if column >= len(row) {
			continue
		}
		encoded := sensitiveImageTransferEncodedValue(row[column])
		// Oversized individual rows are rejected without decoding by the row
		// validator. Bound memory for the remaining potentially valid rows here.
		if len(encoded) > base64.StdEncoding.EncodedLen(MaxTicketAttachmentBytes) {
			continue
		}
		size := base64.StdEncoding.DecodedLen(len(encoded))
		if strings.HasSuffix(encoded, "==") {
			size -= 2
		} else if strings.HasSuffix(encoded, "=") {
			size--
		}
		if size > 0 {
			total += int64(size)
		}
		if total > maxSensitiveImageTransferBytes {
			return fmt.Errorf("图片导入内容超过 64 MiB，请分批上传")
		}
	}
	return nil
}

func addSpreadsheetImage(file *excelize.File, sheet, axis, encoded string, row int) error {
	if encoded == "" {
		return nil
	}
	plain, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	_, kind, err := image.DecodeConfig(bytes.NewReader(plain))
	if err != nil {
		return err
	}
	if kind == "jpeg" {
		kind = "jpg"
	}
	if err := file.AddPictureFromBytes(sheet, axis, &excelize.Picture{Extension: "." + kind, File: plain, Format: &excelize.GraphicOptions{AutoFit: true, Positioning: "oneCell", AltText: "检测图片"}}); err != nil {
		return err
	}
	col, _, _ := excelize.CellNameToCoordinates(axis)
	letter, _ := excelize.ColumnNumberToName(col)
	_ = file.SetColWidth(sheet, letter, letter, 22)
	_ = file.SetRowHeight(sheet, row, 96)
	return file.SetCellValue(sheet, axis, "")
}
