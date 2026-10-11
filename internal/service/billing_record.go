package service

import (
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const normalSearchBillingRecordType = "normal_search"
const registrationBonusBillingRecordType = "registration_bonus"

type BillingRecordFeeDetail struct {
	Provider      string  `json:"provider"`
	ProviderLabel string  `json:"providerLabel"`
	Amount        float64 `json:"amount"`
}

type BillingRecordListItem struct {
	ID                int32                    `json:"id"`
	UserID            int32                    `json:"-"`
	Username          string                   `json:"username"`
	Type              string                   `json:"type"`
	TypeLabel         string                   `json:"typeLabel"`
	Direction         string                   `json:"direction"`
	DirectionLabel    string                   `json:"directionLabel"`
	Subject           string                   `json:"subject"`
	Detail            string                   `json:"detail"`
	ReferenceNo       string                   `json:"referenceNo"`
	Provider          string                   `json:"provider"`
	ProviderLabel     string                   `json:"providerLabel"`
	AnswerSourceLabel string                   `json:"answerSourceLabel"`
	QuestionText      string                   `json:"questionText"`
	Amount            float64                  `json:"amount"`
	FeeDetails        []BillingRecordFeeDetail `gorm:"-" json:"feeDetails"`
	AccessTokenName   string                   `json:"accessTokenName"`
	CreatedAt         time.Time                `json:"createdAt"`
	UnitPriceSnapshot string                   `json:"-"`
	RequestID         string                   `json:"-"`
	Remark            string                   `json:"-"`
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

	sourceName := ExtractBillingSnapshotField(snapshot, "sourceName")
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
	case registrationBonusBillingRecordType:
		return "新用户赠送"
	case normalSearchBillingRecordType:
		return "题目消费"
	default:
		return strings.TrimSpace(recordType)
	}
}

func buildBillingRecordDirection(recordType string) string {
	if strings.TrimSpace(recordType) == rechargeBillingRecordType ||
		strings.TrimSpace(recordType) == registrationBonusBillingRecordType {
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
	if record.Type == registrationBonusBillingRecordType {
		if remark := strings.TrimSpace(record.Remark); remark != "" {
			return remark
		}
		return "新用户赠送"
	}
	if question := strings.TrimSpace(record.QuestionText); question != "" {
		return buildSearchBillingSubject(question)
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
	if record.Type == registrationBonusBillingRecordType {
		if remark := strings.TrimSpace(record.Remark); remark != "" {
			return remark
		}
		return "新用户赠送"
	}
	if source := strings.TrimSpace(record.AnswerSourceLabel); source != "" {
		return source
	}
	return "-"
}

// ExtractBillingSnapshotField 从账单的 unit_price_snapshot 里取出某个字段的值（已 URL 解码）。
func ExtractBillingSnapshotField(snapshot string, key string) string {
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

// buildBillingRecordDisplayQuery 构造账单展示用的基础查询。
//
// JOIN 必须在这里完成：关键词筛选会用到 access_tokens.name 与 billing_users.username，
// 若只在取数据时才 JOIN，Count 就会因为缺列而报错（或得出与数据不一致的 total）。
func buildBillingRecordDisplayQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	baseQuery := db.Table(entity.TableNameBillingRecord + " AS billing_records").
		Joins("LEFT JOIN " + entity.TableNameAccessToken + " AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Joins("LEFT JOIN " + entity.TableNameUser + " AS billing_users ON billing_users.id = billing_records.user_id")
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
	if strings.TrimSpace(record.Provider) == "system" {
		record.ProviderLabel = "系统赠送"
	}
	if cachedLabel := buildCachedLocalProviderLabel(record.Provider, record.UnitPriceSnapshot); cachedLabel != "" {
		record.ProviderLabel = cachedLabel
	}
	if strings.TrimSpace(record.ProviderLabel) == "" {
		record.ProviderLabel = strings.TrimSpace(record.Provider)
	}
	record.AnswerSourceLabel = buildBillingAnswerSourceLabel(record.Provider, record.ProviderLabel, record.UnitPriceSnapshot)
	// 优先用持久化的 question_text；旧行或新写入前为空时回退到快照解析。
	if questionText := strings.TrimSpace(record.QuestionText); questionText == "" {
		record.QuestionText = ExtractBillingSnapshotField(record.UnitPriceSnapshot, "question")
	}
	record.Subject = buildBillingRecordSubject(record)
	record.Detail = buildBillingRecordDetail(record)
	record.FeeDetails = []BillingRecordFeeDetail{{
		Provider:      strings.TrimSpace(record.Provider),
		ProviderLabel: strings.TrimSpace(record.ProviderLabel),
		Amount:        record.Amount,
	}}
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
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.amount, billing_records.created_at, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
		Joins("LEFT JOIN "+entity.TableNameAccessToken+" AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Joins("LEFT JOIN "+entity.TableNameUser+" AS billing_users ON billing_users.id = billing_records.user_id").
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

func buildSearchBillingSubject(questionText string) string {
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
	feeDetails := make([]BillingRecordFeeDetail, 0, len(charges))
	for _, charge := range charges {
		totalAmount += charge.Amount
		if questionText == "" {
			questionText = strings.TrimSpace(charge.QuestionText)
		}
		providerLabels = appendUniqueBillingLabel(providerLabels, charge.ProviderLabel)
		answerSourceLabels = appendUniqueBillingLabel(answerSourceLabels, charge.AnswerSourceLabel)
		feeDetails = append(feeDetails, charge.FeeDetails...)
	}

	representative.Amount = util.RoundMoney(totalAmount)
	representative.QuestionText = questionText
	representative.ProviderLabel = strings.Join(providerLabels, "、")
	representative.AnswerSourceLabel = strings.Join(answerSourceLabels, "、")
	if len(feeDetails) > 0 {
		representative.FeeDetails = feeDetails
	}
	representative.Subject = buildSearchBillingSubject(questionText)
	representative.Detail = buildBillingRecordDetail(representative)
	return representative
}

// BillingRecordListQuery 是账单列表的筛选条件。
type BillingRecordListQuery struct {
	// Keyword 同时匹配题目、用户名、渠道、模型、请求 ID、令牌名与备注。
	Keyword string
	// Type 精确匹配账单类型（题目消费 / 充值入账 / 新用户赠送）。
	Type string
}

// ListBillingRecordsWithDB 返回账单列表。
//
// 分页与筛选都在数据库侧完成：先 JOIN 出展示需要的用户 / 令牌列，
// 再把全部筛选条件应用到同一个 query 上，最后分别 Count 与 Find，
// 避免出现「total 与当前页数据口径不一致」。
func ListBillingRecordsWithDB(db *gorm.DB, operator entity.User, query BillingRecordListQuery, page int, pageSize int) (BillingRecordListResult, error) {
	baseQuery := applyBillingRecordFilters(buildBillingRecordDisplayQuery(db, operator), query)

	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	var records []BillingRecordListItem
	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.model_name, billing_records.amount, billing_records.created_at, billing_records.unit_price_snapshot, billing_records.question_text, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
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

// applyBillingRecordFilters 把关键词与类型筛选应用到账单查询上。
func applyBillingRecordFilters(query *gorm.DB, filter BillingRecordListQuery) *gorm.DB {
	if recordType := strings.TrimSpace(filter.Type); recordType != "" {
		query = query.Where("billing_records.type = ?", recordType)
	}

	pattern := util.BuildLikePattern(filter.Keyword)
	if pattern == "" {
		return query
	}

	// 用 ESCAPE '\' 配合转义后的模式，用户输入的 % 与 _ 按字面量处理。
	condition := `(
		LOWER(COALESCE(billing_users.username, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(billing_records.provider, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(billing_records.model_name, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(billing_records.question_text, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(billing_records.request_id, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(billing_records.remark, '')) LIKE LOWER(?) ESCAPE '\'
		OR LOWER(COALESCE(access_tokens.name, '')) LIKE LOWER(?) ESCAPE '\'
	)`
	return query.Where(condition, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
}
