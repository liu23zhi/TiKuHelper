package service

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const normalSearchBillingRecordType = "normal_search"

type BillingRecordListItem struct {
	ID                int32     `json:"id"`
	UserID            int32     `json:"-"`
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

type billingSearchGroupKey struct {
	UserID    int32
	RequestID string
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
		return normalSearchBillingRecordType
	}
	return normalized
}

func buildBillingRecordTypeLabel(recordType string) string {
	switch strings.TrimSpace(recordType) {
	case rechargeBillingRecordType:
		return "充值入账"
	case normalSearchBillingRecordType:
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

func buildBillingRecordDisplayQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	baseQuery := db.Table(entity.TableNameBillingRecord + " AS billing_records")
	baseQuery = baseQuery.Where(`
		NOT EXISTS (
			SELECT 1
			FROM billing_records AS newer_records
			WHERE newer_records.user_id = billing_records.user_id
				AND newer_records.request_id = billing_records.request_id
				AND newer_records.type = ?
				AND billing_records.type = ?
				AND COALESCE(billing_records.request_id, '') <> ''
				AND newer_records.id > billing_records.id
		)
	`, normalSearchBillingRecordType, normalSearchBillingRecordType)
	if !isAdminUser(operator) {
		baseQuery = baseQuery.Where("billing_records.user_id = ?", operator.ID)
	}
	return baseQuery
}

func decorateBillingRecord(record BillingRecordListItem) BillingRecordListItem {
	record.Type = normalizeBillingRecordType(record.Type)
	record.TypeLabel = buildBillingRecordTypeLabel(record.Type)
	record.Direction = buildBillingRecordDirection(record.Type)
	record.DirectionLabel = buildBillingRecordDirectionLabel(record.Direction)
	record.ReferenceNo = strings.TrimSpace(record.RequestID)
	if meta, ok := getSearchAccountProviderMeta(record.Provider); ok {
		record.ProviderLabel = meta.ProviderLabel
	}
	if cachedLabel := buildCachedLocalProviderLabel(record.Provider, record.UnitPriceSnapshot); cachedLabel != "" {
		record.ProviderLabel = cachedLabel
	}
	if strings.TrimSpace(record.ProviderLabel) == "" {
		record.ProviderLabel = strings.TrimSpace(record.Provider)
	}
	record.AnswerSourceLabel = buildBillingAnswerSourceLabel(record.Provider, record.ProviderLabel, record.UnitPriceSnapshot)
	record.QuestionText = extractBillingSnapshotField(record.UnitPriceSnapshot, "question")
	record.Subject = buildBillingRecordSubject(record)
	record.Detail = buildBillingRecordDetail(record)
	return record
}

func loadBillingSearchGroups(db *gorm.DB, records []BillingRecordListItem) (map[billingSearchGroupKey][]BillingRecordListItem, error) {
	requestIDs := make([]string, 0, len(records))
	userIDs := make([]int32, 0, len(records))
	seenRequestIDs := make(map[string]struct{}, len(records))
	seenUserIDs := make(map[int32]struct{}, len(records))
	for _, record := range records {
		if normalizeBillingRecordType(record.Type) != normalSearchBillingRecordType || strings.TrimSpace(record.RequestID) == "" {
			continue
		}
		if _, exists := seenRequestIDs[record.RequestID]; !exists {
			seenRequestIDs[record.RequestID] = struct{}{}
			requestIDs = append(requestIDs, record.RequestID)
		}
		if _, exists := seenUserIDs[record.UserID]; !exists {
			seenUserIDs[record.UserID] = struct{}{}
			userIDs = append(userIDs, record.UserID)
		}
	}
	if len(requestIDs) == 0 {
		return map[billingSearchGroupKey][]BillingRecordListItem{}, nil
	}

	var groupRecords []BillingRecordListItem
	if err := db.
		Table(entity.TableNameBillingRecord+" AS billing_records").
		Select("billing_records.id, billing_records.user_id, billing_records.type, billing_records.provider, billing_records.amount, billing_records.created_at, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
		Joins("LEFT JOIN "+entity.TableNameAccessToken+" AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Where("billing_records.type = ?", normalSearchBillingRecordType).
		Where("billing_records.request_id IN ?", requestIDs).
		Where("billing_records.user_id IN ?", userIDs).
		Order("billing_records.id asc").
		Find(&groupRecords).Error; err != nil {
		return nil, err
	}

	groups := make(map[billingSearchGroupKey][]BillingRecordListItem)
	for _, record := range groupRecords {
		record = decorateBillingRecord(record)
		key := billingSearchGroupKey{UserID: record.UserID, RequestID: strings.TrimSpace(record.RequestID)}
		groups[key] = append(groups[key], record)
	}
	return groups, nil
}

func appendUniqueBillingLabel(labels []string, label string) []string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return labels
	}
	for _, existing := range labels {
		if existing == trimmed {
			return labels
		}
	}
	return append(labels, trimmed)
}

func buildMergedSearchBillingSubject(questionText string) string {
	question := strings.TrimSpace(questionText)
	if question == "" {
		return "搜索答案"
	}
	return "搜索答案，题目：" + question
}

func mergeBillingSearchGroup(representative BillingRecordListItem, charges []BillingRecordListItem) BillingRecordListItem {
	if len(charges) <= 1 {
		return representative
	}

	totalAmount := float64(0)
	questionText := strings.TrimSpace(representative.QuestionText)
	providerLabels := make([]string, 0, len(charges))
	answerSourceLabels := make([]string, 0, len(charges))
	for _, charge := range charges {
		totalAmount += charge.Amount
		if questionText == "" {
			questionText = strings.TrimSpace(charge.QuestionText)
		}
		providerLabels = appendUniqueBillingLabel(providerLabels, charge.ProviderLabel)
		answerSourceLabels = appendUniqueBillingLabel(answerSourceLabels, charge.AnswerSourceLabel)
	}

	representative.Amount = math.Round(totalAmount*100) / 100
	representative.QuestionText = questionText
	representative.ProviderLabel = strings.Join(providerLabels, "、")
	representative.AnswerSourceLabel = strings.Join(answerSourceLabels, "、")
	representative.Subject = buildMergedSearchBillingSubject(questionText)
	representative.Detail = buildBillingRecordDetail(representative)
	return representative
}

func ListBillingRecordsWithDB(db *gorm.DB, operator entity.User, page int, pageSize int) (BillingRecordListResult, error) {
	baseQuery := buildBillingRecordDisplayQuery(db, operator)

	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	var records []BillingRecordListItem
	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("billing_records.id, billing_records.user_id, billing_records.type, billing_records.provider, billing_records.amount, billing_records.created_at, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
		Joins("LEFT JOIN " + entity.TableNameAccessToken + " AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Order("billing_records.id desc").
		Limit(pageSize).
		Offset(offset).
		Find(&records).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	searchGroups, err := loadBillingSearchGroups(db, records)
	if err != nil {
		return BillingRecordListResult{}, err
	}

	for i := range records {
		records[i] = decorateBillingRecord(records[i])
		if normalizeBillingRecordType(records[i].Type) != normalSearchBillingRecordType || strings.TrimSpace(records[i].RequestID) == "" {
			continue
		}
		key := billingSearchGroupKey{UserID: records[i].UserID, RequestID: strings.TrimSpace(records[i].RequestID)}
		records[i] = mergeBillingSearchGroup(records[i], searchGroups[key])
	}

	return BillingRecordListResult{
		Data:     records,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}
