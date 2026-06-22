package service

import (
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type BillingRecordListItem struct {
	ID                int32     `json:"id"`
	Type              string    `json:"type"`
	TypeLabel         string    `json:"typeLabel"`
	Direction         string    `json:"direction"`
	DirectionLabel    string    `json:"directionLabel"`
	Subject           string    `json:"subject"`
	Detail            string    `json:"detail"`
	ReferenceNo       string    `json:"referenceNo"`
	Provider          string    `json:"provider"`
	ProviderLabel     string    `json:"providerLabel"`
	AnswerSourceLabel string    `json:"answerSourceLabel"`
	QuestionText      string    `json:"questionText"`
	Amount            float64   `json:"amount"`
	AccessTokenName   string    `json:"accessTokenName"`
	CreatedAt         time.Time `json:"createdAt"`
	UnitPriceSnapshot string    `json:"-"`
	RequestID         string    `json:"-"`
	Remark            string    `json:"-"`
}

type BillingRecordListResult struct {
	Data     []BillingRecordListItem `json:"data"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
}

func buildCachedLocalProviderLabel(provider string, snapshot string) string {
	if strings.TrimSpace(provider) != "local" {
		return ""
	}

	sourceName := extractBillingSnapshotField(snapshot, "sourceName")
	if sourceName == "" || sourceName == "人工导入" {
		return ""
	}
	return sourceName + "（缓存）"
}

func buildBillingAnswerSourceLabel(provider string, providerLabel string, snapshot string) string {
	if cachedLabel := buildCachedLocalProviderLabel(provider, snapshot); cachedLabel != "" {
		return cachedLabel
	}

	if strings.TrimSpace(providerLabel) != "" {
		return strings.TrimSpace(providerLabel)
	}

	if strings.TrimSpace(provider) == "local" {
		return "本地题库"
	}

	return strings.TrimSpace(provider)
}

func normalizeBillingRecordType(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "normal_search"
	}
	return normalized
}

func buildBillingRecordTypeLabel(recordType string) string {
	switch strings.TrimSpace(recordType) {
	case rechargeBillingRecordType:
		return "充值入账"
	case "normal_search":
		return "题目消费"
	default:
		return strings.TrimSpace(recordType)
	}
}

func buildBillingRecordDirection(recordType string) string {
	if strings.TrimSpace(recordType) == rechargeBillingRecordType {
		return "income"
	}
	return "expense"
}

func buildBillingRecordDirectionLabel(direction string) string {
	if strings.TrimSpace(direction) == "income" {
		return "收入"
	}
	return "支出"
}

func buildBillingRecordSubject(record BillingRecordListItem) string {
	if record.Type == rechargeBillingRecordType {
		if remark := strings.TrimSpace(record.Remark); remark != "" {
			return remark
		}
		return "账户充值"
	}
	if question := strings.TrimSpace(record.QuestionText); question != "" {
		return question
	}
	if source := strings.TrimSpace(record.AnswerSourceLabel); source != "" {
		return source
	}
	return "-"
}

func buildBillingRecordDetail(record BillingRecordListItem) string {
	if record.Type == rechargeBillingRecordType {
		if remark := strings.TrimSpace(record.Remark); remark != "" {
			return remark
		}
		return "账户充值"
	}
	if source := strings.TrimSpace(record.AnswerSourceLabel); source != "" {
		return source
	}
	return "-"
}

func extractBillingSnapshotField(snapshot string, key string) string {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return ""
	}

	prefix := trimmedKey + "="
	idx := strings.Index(snapshot, prefix)
	if idx < 0 {
		return ""
	}

	value := snapshot[idx+len(prefix):]
	if end := strings.IndexAny(value, " \t\r\n"); end >= 0 {
		value = value[:end]
	}
	decoded, err := url.QueryUnescape(strings.TrimSpace(value))
	if err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(value)
}

func ListBillingRecordsWithDB(db *gorm.DB, operator entity.User, page int, pageSize int) (BillingRecordListResult, error) {
	baseQuery := db.Table(entity.TableNameBillingRecord + " AS billing_records")
	if !isAdminUser(operator) {
		baseQuery = baseQuery.Where("billing_records.user_id = ?", operator.ID)
	}

	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	var records []BillingRecordListItem
	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("billing_records.id, billing_records.type, billing_records.provider, billing_records.amount, billing_records.created_at, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
		Joins("LEFT JOIN " + entity.TableNameAccessToken + " AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Order("billing_records.id desc").
		Limit(pageSize).
		Offset(offset).
		Find(&records).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	for i := range records {
		records[i].Type = normalizeBillingRecordType(records[i].Type)
		records[i].TypeLabel = buildBillingRecordTypeLabel(records[i].Type)
		records[i].Direction = buildBillingRecordDirection(records[i].Type)
		records[i].DirectionLabel = buildBillingRecordDirectionLabel(records[i].Direction)
		records[i].ReferenceNo = strings.TrimSpace(records[i].RequestID)
		if meta, ok := getSearchAccountProviderMeta(records[i].Provider); ok {
			records[i].ProviderLabel = meta.ProviderLabel
		}
		if cachedLabel := buildCachedLocalProviderLabel(records[i].Provider, records[i].UnitPriceSnapshot); cachedLabel != "" {
			records[i].ProviderLabel = cachedLabel
		}
		if strings.TrimSpace(records[i].ProviderLabel) == "" {
			records[i].ProviderLabel = strings.TrimSpace(records[i].Provider)
		}
		records[i].AnswerSourceLabel = buildBillingAnswerSourceLabel(records[i].Provider, records[i].ProviderLabel, records[i].UnitPriceSnapshot)
		records[i].QuestionText = extractBillingSnapshotField(records[i].UnitPriceSnapshot, "question")
		records[i].Subject = buildBillingRecordSubject(records[i])
		records[i].Detail = buildBillingRecordDetail(records[i])
	}

	return BillingRecordListResult{
		Data:     records,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}
