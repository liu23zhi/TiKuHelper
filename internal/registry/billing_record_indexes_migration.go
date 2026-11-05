package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const billingRecordIndexesMigrationKey = "billing_record_indexes_v2"

type billingRecordIndexSpec struct {
	name    string
	columns []string
}

// ensureBillingRecordIndexes adds the indexes used by billing-record
// deduplication and request aggregation, including a covering usage index so
// current-page amount/token aggregation can avoid reading complete table rows.
// Existing v1 markers do not skip index checks: old databases must still gain
// the usage index, and rerunning this migration only reads index metadata.
// Raw CREATE INDEX is used after the
// dialect-neutral HasIndex check because GORM's CreateIndex cannot express a
// composite index's columns independently of model tags. Backtick quoting is
// accepted by both SQLite and MySQL.
func ensureBillingRecordIndexes(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&entity.BillingRecord{}) {
		return nil
	}
	indexes := []billingRecordIndexSpec{
		{name: "idx_billing_records_dedup", columns: []string{"user_id", "request_id", "type", "hidden_from_statement", "id"}},
		{name: "idx_billing_records_aggregation", columns: []string{"type", "request_id", "user_id", "hidden_from_statement"}},
		{name: "idx_billing_records_usage", columns: []string{"type", "user_id", "request_id", "hidden_from_statement", "provider", "input_tokens", "cached_input_tokens", "amount"}},
	}
	for _, index := range indexes {
		hasIndex := db.Migrator().HasIndex(&entity.BillingRecord{}, index.name)
		if hasIndex {
			columns, err := billingRecordIndexColumns(db, index.name)
			if err != nil {
				return err
			}
			if strings.Join(columns, ",") == strings.Join(index.columns, ",") {
				continue
			}
			if err := db.Migrator().DropIndex(&entity.BillingRecord{}, index.name); err != nil {
				return fmt.Errorf("删除旧账单索引 %s 失败: %w", index.name, err)
			}
		}
		quotedColumns := make([]string, len(index.columns))
		for i, column := range index.columns {
			quotedColumns[i] = quoteBillingIdentifier(column)
		}
		statement := fmt.Sprintf("CREATE INDEX %s ON %s (%s)", quoteBillingIdentifier(index.name), quoteBillingIdentifier(entity.TableNameBillingRecord), strings.Join(quotedColumns, ", "))
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("创建账单复合索引 %s 失败: %w", index.name, err)
		}
	}
	return setSystemSettingFlag(db, billingRecordIndexesMigrationKey)
}

func billingRecordIndexColumns(db *gorm.DB, name string) ([]string, error) {
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		var rows []struct {
			Seq  int    `gorm:"column:seqno"`
			Name string `gorm:"column:name"`
		}
		if err := db.Raw("PRAGMA index_info(" + quoteBillingIdentifier(name) + ")").Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取账单索引 %s 失败: %w", name, err)
		}
		columns := make([]string, len(rows))
		for _, row := range rows {
			if row.Seq >= 0 && row.Seq < len(columns) {
				columns[row.Seq] = row.Name
			}
		}
		return columns, nil
	}
	var rows []struct {
		Seq  int    `gorm:"column:SEQ_IN_INDEX"`
		Name string `gorm:"column:COLUMN_NAME"`
	}
	if err := db.Raw("SELECT SEQ_IN_INDEX, COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX", entity.TableNameBillingRecord, name).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取账单索引 %s 失败: %w", name, err)
	}
	columns := make([]string, len(rows))
	for _, row := range rows {
		if row.Seq > 0 && row.Seq <= len(columns) {
			columns[row.Seq-1] = row.Name
		}
	}
	return columns, nil
}

func quoteBillingIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
