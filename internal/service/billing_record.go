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
	Detail        string  `json:"detail,omitempty"`
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
	// ClientIP 是这笔账单的来源 IP，前端据此展示归属地（形如 `1.2.3.4 （广东深圳）`）。
	//
	// 历史行（加列之前落账的）为空，前端降级为只显示 `-`。
	ClientIP          string `json:"clientIp"`
	UnitPriceSnapshot string `json:"-"`
	RequestID         string `json:"-"`
	Remark            string `json:"-"`
}

type billingSearchGroupKey struct {
	UserID    int32
	RequestID string
}

// BillingRecordSummary 是账单列表的金额统计。
//
// 口径说明：
//   - 只统计「支出」（题目消费），充值入账与新用户赠送属于收入，不计入消费金额；
//   - 与列表一样按 (user_id, request_id) 去重，避免一次搜题的多个渠道被重复累加；
//   - TotalAmount 覆盖**全部符合筛选条件**的账单，CurrentPageAmount 只覆盖当前页。
type BillingRecordSummary struct {
	TotalAmount       float64 `json:"totalAmount"`
	CurrentPageAmount float64 `json:"currentPageAmount"`
}

type BillingRecordListResult struct {
	Data     []BillingRecordListItem `json:"data"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
	Summary  BillingRecordSummary    `json:"summary"`
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

// buildBillingRecordScopeQuery 构造账单查询的公共范围：JOIN + 操作者权限。
//
// JOIN 必须在这里完成：关键词筛选会用到 access_tokens.name 与 billing_users.username，
// 若只在取数据时才 JOIN，Count 就会因为缺列而报错（或得出与数据不一致的 total）。
func buildBillingRecordScopeQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	query := db.Table(entity.TableNameBillingRecord + " AS billing_records").
		Joins("LEFT JOIN " + entity.TableNameAccessToken + " AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Joins("LEFT JOIN " + entity.TableNameUser + " AS billing_users ON billing_users.id = billing_records.user_id")
	if !isAdminUser(operator) {
		query = query.Where("billing_records.user_id = ?", operator.ID)
	}
	return query
}

// applyBillingRecordSearchDedup 保留每个 request_id 最新的一条明细。
//
// 一次搜题命中多个渠道会写成多条 normal_search，列表只展示最新的一条（代表整笔）。
func applyBillingRecordSearchDedup(query *gorm.DB) *gorm.DB {
	return query.Where(`
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
}

// buildBillingRecordDisplayQuery 构造账单展示用的基础查询（含同请求去重）。
func buildBillingRecordDisplayQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	return applyBillingRecordSearchDedup(buildBillingRecordScopeQuery(db, operator))
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
	if strings.TrimSpace(record.Provider) == "ai_search" {
		record.ProviderLabel = "联网搜索"
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
		Detail:        buildBillingRecordFeeDetail(record.Provider, record.UnitPriceSnapshot),
	}}
	return record
}

func buildBillingRecordFeeDetail(provider string, snapshot string) string {
	if strings.TrimSpace(provider) != "ai_search" {
		return ""
	}
	parts := make([]string, 0, 7)
	appendField := func(label string, key string) {
		if value := ExtractBillingSnapshotField(snapshot, key); value != "" {
			parts = append(parts, label+"："+value)
		}
	}
	appendField("搜索词", "searchQuery")
	appendField("API", "searchAPI")
	appendField("状态", "searchStatus")
	appendField("结果", "searchResultCount")
	appendField("总结果", "searchTotalResults")
	appendField("来源", "searchSources")
	appendField("耗时", "searchDurationMs")
	appendField("错误", "searchError")
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
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
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.amount, billing_records.created_at, billing_records.client_ip, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
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
//
// 金额统计（Summary）覆盖**全部筛选结果**，不随分页变化；同时给出当前页小计。
// 一次搜题命中多个渠道会写成多条 normal_search，同一 request_id 视为**一笔消费**，
// 其金额为组内明细之和，避免重复累加，也避免只算最新一条而漏掉其他渠道。
func ListBillingRecordsWithDB(db *gorm.DB, operator entity.User, query BillingRecordListQuery, page int, pageSize int) (BillingRecordListResult, error) {
	baseQuery := applyBillingRecordFilters(buildBillingRecordDisplayQuery(db, operator), query)

	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return BillingRecordListResult{}, err
	}

	// 取出全部筛选结果：既要算跨页总额，也要按 id desc 排序后切出当前页，
	// 保证「列表分页」与「统计口径」用的是同一份数据。
	allRows, err := findAllBillingRecordAmountRows(baseQuery)
	if err != nil {
		return BillingRecordListResult{}, err
	}

	// 每笔请求的真实消费总额：同一 request_id 的多个渠道相加。
	// 必须同样带上筛选条件（否则「累计」不会跟随搜索收窄），
	// 但不能带展示层的同请求去重（否则会漏掉其他渠道的金额）。
	requestExpenses, err := loadBillingRequestExpenseTotals(
		applyBillingRecordFilters(buildBillingRecordScopeQuery(db, operator), query),
	)
	if err != nil {
		return BillingRecordListResult{}, err
	}

	for i := range allRows {
		allRows[i] = decorateBillingRecord(allRows[i])
		// 列表里的代表行金额改成整笔请求的合计，避免「只显示最新渠道金额」造成误读，
		// 也让「累计消费金额」与逐行金额相加的结果一致。
		if key, grouped := billingRecordExpenseKey(allRows[i]); grouped {
			if total, ok := requestExpenses[key]; ok {
				allRows[i].Amount = total
			}
		}
	}

	offset := (page - 1) * pageSize
	pageRows := sliceBillingRecordAmountRows(allRows, offset, pageSize)

	records := make([]BillingRecordListItem, 0, len(pageRows))
	records = append(records, pageRows...)

	// 当前页的代表行还需要兄弟明细，才能展示渠道构成与费用明细。
	searchGroups, err := loadBillingSearchGroups(db, records)
	if err != nil {
		return BillingRecordListResult{}, err
	}
	for i := range records {
		if normalizeBillingRecordType(records[i].Type) != normalSearchBillingRecordType || strings.TrimSpace(records[i].RequestID) == "" {
			continue
		}
		key := billingSearchGroupKey{UserID: records[i].UserID, RequestID: strings.TrimSpace(records[i].RequestID)}
		records[i] = mergeBillingSearchGroup(records[i], searchGroups[key])
		// mergeBillingSearchGroup 会依据明细重算金额，这里保持与整笔合计一致。
		if total, ok := requestExpenses[key]; ok {
			records[i].Amount = total
		}
	}

	return BillingRecordListResult{
		Data:     records,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Summary: BillingRecordSummary{
			TotalAmount:       sumBillingExpenseAmount(allRows),
			CurrentPageAmount: sumBillingExpenseAmount(pageRows),
		},
	}, nil
}

// billingRecordExpenseKey 返回用于「按请求聚合消费金额」的键；非搜题消费返回 grouped=false。
func billingRecordExpenseKey(row BillingRecordListItem) (billingSearchGroupKey, bool) {
	if normalizeBillingRecordType(row.Type) != normalSearchBillingRecordType {
		return billingSearchGroupKey{}, false
	}
	if strings.TrimSpace(row.RequestID) == "" {
		return billingSearchGroupKey{}, false
	}
	return billingSearchGroupKey{UserID: row.UserID, RequestID: strings.TrimSpace(row.RequestID)}, true
}

// loadBillingRequestExpenseTotals 按 (user_id, request_id) 汇总每笔请求的消费总额。
//
// 入参 query 必须已经带上「操作者范围 + 全部筛选条件」，但**不能带展示层的同请求去重**：
// 那个 NOT EXISTS 只保留每个请求最新的一条明细，用它求和会漏掉同一笔请求的其他渠道
// （例如 icodef 1.25 + ai 0.75 只会算成 1.25）。
//
// 只按「类型」筛出题目消费：充值入账与新用户赠送分别是 recharge / registration_bonus。
// 不能改用 provider 过滤 —— 新用户赠送的 provider 正是 "system"（界面标签「系统赠送」），
// 用 provider <> 'system' 会把它误排除，导致「逐行金额相加」与「累计」对不上。
func loadBillingRequestExpenseTotals(query *gorm.DB) (map[billingSearchGroupKey]float64, error) {
	query = query.
		Select("billing_records.user_id, billing_records.request_id, COALESCE(SUM(billing_records.amount), 0) AS total_amount").
		Where("billing_records.type = ?", normalSearchBillingRecordType).
		Where("COALESCE(billing_records.request_id, '') <> ''").
		Group("billing_records.user_id, billing_records.request_id")

	var rows []struct {
		UserID      int32   `gorm:"column:user_id"`
		RequestID   string  `gorm:"column:request_id"`
		TotalAmount float64 `gorm:"column:total_amount"`
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	totals := make(map[billingSearchGroupKey]float64, len(rows))
	for _, row := range rows {
		totals[billingSearchGroupKey{UserID: row.UserID, RequestID: strings.TrimSpace(row.RequestID)}] = util.RoundMoney(row.TotalAmount)
	}
	return totals, nil
}

// findAllBillingRecordAmountRows 取出符合筛选条件的全部账单。
//
// 这里不复用分页查询，是因为「全部累计消费」必须覆盖所有页。
// 返回的行会走与列表完全相同的装饰逻辑（渠道标签、题目、用户名等），
// 因此必须 Select 齐这些展示依赖列，否则会退化成「总数对了但字段是空的」：
//   - username / access_token_name 供列表展示；
//   - unit_price_snapshot 供题目快照回退与「（缓存）」渠道标签。
func findAllBillingRecordAmountRows(baseQuery *gorm.DB) ([]BillingRecordListItem, error) {
	var rows []BillingRecordListItem
	if err := baseQuery.
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.model_name, billing_records.amount, billing_records.created_at, billing_records.client_ip, billing_records.unit_price_snapshot, billing_records.question_text, billing_records.request_id, billing_records.remark, access_tokens.name AS access_token_name").
		Order("billing_records.id desc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func sliceBillingRecordAmountRows(rows []BillingRecordListItem, offset int, pageSize int) []BillingRecordListItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) || pageSize <= 0 {
		return nil
	}
	end := offset + pageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end]
}

// sumBillingExpenseAmount 累加「支出」金额；充值入账与新用户赠送属于收入，不计入消费。
func sumBillingExpenseAmount(rows []BillingRecordListItem) float64 {
	total := 0.0
	for _, row := range rows {
		if row.Direction == "income" {
			continue
		}
		if amount := row.Amount; amount > 0 {
			total += amount
		}
	}
	return util.RoundMoney(total)
}

// applyBillingRecordFilters 把关键词与类型筛选应用到账单查询上。
func applyBillingRecordFilters(query *gorm.DB, filter BillingRecordListQuery) *gorm.DB {
	if recordType := strings.TrimSpace(filter.Type); recordType != "" {
		query = query.Where("billing_records.type = ?", recordType)
	}

	// 关键词按「纯子串」匹配（不使用 LIKE 通配符），见 util.BuildInstrCondition。
	condition, args := util.BuildInstrCondition(filter.Keyword,
		"COALESCE(billing_users.username, '')",
		"COALESCE(billing_records.provider, '')",
		"COALESCE(billing_records.model_name, '')",
		"COALESCE(billing_records.question_text, '')",
		"COALESCE(billing_records.request_id, '')",
		"COALESCE(billing_records.remark, '')",
		"COALESCE(access_tokens.name, '')",
	)
	if condition == "" {
		return query
	}
	return query.Where(condition, args...)
}
