package service

import (
	"strconv"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	ExportResourceRechargeOrders = "recharge_orders"
	ExportResourceDevices        = "devices"
	ExportResourceSensitiveWords = "sensitive_words"
)

func init() {
	exportResourceSpecs[ExportResourceRechargeOrders] = exportResourceSpec{
		resource: ExportResourceRechargeOrders,
		columns:  []ExportColumn{exportColumn("id"), exportColumn("user_id"), exportColumn("username"), maskedColumn("order_no", MaskExportToken), exportColumn("payment_provider"), exportColumn("payment_method"), exportColumn("amount"), exportColumn("status"), maskedColumn("third_party_order_no", MaskExportToken), exportColumn("paid_at"), exportColumn("created_at"), exportColumn("updated_at"), exportColumn("refund_marked"), exportColumn("refund_reserved"), exportColumn("settlement_needs_review"), exportColumn("refund_order_cap_cents")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var rows []RechargeOrderListItem
			_, columns := rechargeOrderReadProjection(db)
			query := rechargeOrderRows(db.Session(&gorm.Session{NewDB: true})).Select(columns).Order("recharge_orders.id ASC")
			err := exportLimit(query, limit).Find(&rows).Error
			if err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			now := time.Now()
			for _, row := range rows {
				row.setRefundOrderCap(now)
				out = append(out, []string{strconv.FormatInt(int64(row.ID), 10), strconv.FormatInt(int64(row.UserID), 10), row.Username, row.OrderNo, row.PaymentProvider, row.PaymentMethodNameSnapshot, strconv.FormatFloat(row.Amount, 'f', 6, 64), row.Status, row.ThirdPartyOrderNo, formatExportTimePtr(row.PaidAt), formatExportTime(row.CreatedAt), formatExportTime(row.UpdatedAt), strconv.FormatBool(row.RefundMarked), strconv.FormatBool(row.RefundReserved), strconv.FormatBool(row.SettlementNeedsReview), strconv.FormatInt(row.RefundOrderCapCents, 10)})
			}
			return out, nil
		},
	}
	exportResourceSpecs[ExportResourceDevices] = exportResourceSpec{
		resource: ExportResourceDevices,
		columns:  []ExportColumn{exportColumn("id"), exportColumn("user_id"), exportColumn("username"), maskedColumn("device_id", MaskExportToken), exportColumn("device_label"), exportColumn("owner_kind"), exportColumn("token_id"), maskedColumn("first_ip", MaskExportIP), maskedColumn("last_ip", MaskExportIP), exportColumn("session_count"), exportColumn("first_seen_at"), exportColumn("last_seen_at"), exportColumn("session_reported_at"), exportColumn("revoked_at"), exportColumn("revoked_reason")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var rows []struct {
				entity.AccountDeviceBinding
				Username string
			}
			err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Model(&entity.AccountDeviceBinding{}).Select("account_device_bindings.*, users.username AS username").Joins("LEFT JOIN users ON users.id = account_device_bindings.user_id").Order("account_device_bindings.id ASC"), limit).Find(&rows).Error
			if err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{strconv.FormatInt(row.ID, 10), strconv.FormatInt(int64(row.UserID), 10), row.Username, row.DeviceID, row.DeviceLabel, row.OwnerKind, strconv.FormatInt(int64(row.TokenID), 10), row.FirstIP, row.LastIP, strconv.FormatInt(int64(row.SessionCount), 10), formatExportTime(row.FirstSeenAt), formatExportTime(row.LastSeenAt), formatExportTimePtr(row.SessionReportedAt), formatExportTimePtr(row.RevokedAt), row.RevokedReason})
			}
			return out, nil
		},
	}
	exportResourceSpecs[ExportResourceSensitiveWords] = exportResourceSpec{
		resource: ExportResourceSensitiveWords,
		columns:  []ExportColumn{exportColumn("text_hash"), exportColumn("sensitive"), maskedColumn("words", func(string) string { return "[已脱敏]" }), maskedColumn("masked_text", func(string) string { return "[已脱敏]" }), exportColumn("api_status"), maskedColumn("error", func(string) string { return "[已脱敏]" }), exportColumn("attempt_count"), exportColumn("checked_at")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var rows []entity.SensitiveWordCache
			if err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Order("checked_at ASC, text_hash ASC"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{row.TextHash, strconv.FormatBool(row.Sensitive), row.Words, row.MaskedText, row.APIStatus, row.Error, strconv.Itoa(row.AttemptCount), formatExportTime(row.CheckedAt)})
			}
			return out, nil
		},
	}
}

func managementArchivedUsersExportSpec() exportResourceSpec {
	return exportResourceSpec{resource: ExportResourceArchivedUsers,
		columns: []ExportColumn{exportColumn("id"), exportColumn("username"), exportColumn("archived_reason"), exportColumn("archived_at"), maskedColumn("phone", MaskExportPhone), exportColumn("archived_by_user_id")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			// 同一条 Unscoped 查询读取 users 上的扩展列，避免 UserArchive 的默认软删除过滤
			// 丢掉归档原因，也避免为十万条记录拼出超过数据库绑定上限的 IN 查询。
			var rows []struct {
				ID               int32
				Username         string
				Phone            *string
				ArchivedReason   string
				ArchivedAt       *time.Time
				DeletedAt        *time.Time
				ArchivedByUserID int32
			}
			err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Unscoped().Table(entity.TableNameUser).Select("id, username, phone, archived_reason, archived_at, deleted_at, archived_by_user_id").Where("deleted_at IS NOT NULL").Order("id ASC"), limit).Scan(&rows).Error
			if err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				at := row.ArchivedAt
				if at == nil {
					at = row.DeletedAt
				}
				phone := ""
				if row.Phone != nil {
					phone = *row.Phone
				}
				out = append(out, []string{strconv.FormatInt(int64(row.ID), 10), row.Username, row.ArchivedReason, formatExportTimePtr(at), phone, strconv.FormatInt(int64(row.ArchivedByUserID), 10)})
			}
			return out, nil
		},
	}
}
