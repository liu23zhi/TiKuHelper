package service

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
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
	Category      string  `json:"category,omitempty"`
	CallIndex     int     `json:"callIndex,omitempty"`
	Sequence      int     `json:"sequence,omitempty"`
	Amount        float64 `json:"amount"`
	Detail        string  `json:"detail,omitempty"`
	// URL is populated for web-read charges so clients do not need to parse the
	// human-readable detail string. It is intentionally omitted for other fees.
	URL string `json:"url,omitempty"`
}

type BillingRecordListItem struct {
	ID                int32  `json:"id"`
	UserID            int32  `json:"-"`
	Username          string `json:"username"`
	Type              string `json:"type"`
	TypeLabel         string `json:"typeLabel"`
	Direction         string `json:"direction"`
	DirectionLabel    string `json:"directionLabel"`
	Subject           string `json:"subject"`
	Detail            string `json:"detail"`
	ReferenceNo       string `json:"referenceNo"`
	Provider          string `json:"provider"`
	ProviderLabel     string `json:"providerLabel"`
	AnswerSourceLabel string `json:"answerSourceLabel"`
	// SearchMode 是这笔消费的搜索模式：deep=深度思考，normal=普通搜索。
	// 非搜题账单与加列之前的历史行为空，界面降级显示占位符。
	SearchMode          string                   `json:"searchMode"`
	SearchModeLabel     string                   `json:"searchModeLabel"`
	QuestionText        string                   `json:"questionText"`
	Amount              float64                  `json:"amount"`
	BalanceBefore       float64                  `json:"balanceBefore"`
	BalanceAfter        float64                  `json:"balanceAfter"`
	OperatorID          int32                    `json:"operatorId"`
	OperatorUsername    string                   `json:"operatorUsername"`
	AdjustmentMode      string                   `json:"adjustmentMode"`
	HiddenFromStatement bool                     `json:"-"`
	ShowInStatement     bool                     `gorm:"-" json:"showInStatement"`
	FinalMultiplier     string                   `gorm:"-" json:"finalMultiplier,omitempty"`
	EffectiveMultiplier string                   `gorm:"-" json:"effectiveMultiplier,omitempty"`
	FeeDetails          []BillingRecordFeeDetail `gorm:"-" json:"feeDetails"`
	AccessTokenName     string                   `json:"accessTokenName"`
	CreatedAt           time.Time                `json:"createdAt"`
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

// buildBillingSearchModeLabel 把账单里的搜索模式映射成界面标签。
//
// deep=深度思考，normal=普通搜索；空值（非搜题账单、或加列之前的历史行）
// 返回空串，由前端降级为占位符。
func buildBillingSearchModeLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deep":
		return "深度思考"
	case "deep_native":
		return "深度思考 原生工具"
	case "deep_mcp":
		return "深度思考 MCP编排"
	case "normal":
		return "普通搜索"
	default:
		return ""
	}
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
	case balanceAdjustmentBillingRecordType:
		return "管理员调整余额"
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
	if record.Type == balanceAdjustmentBillingRecordType {
		return record.Remark
	}
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
	if record.Type == balanceAdjustmentBillingRecordType {
		return fmt.Sprintf("操作管理员：%s (ID=%d)；余额 ¥ %s → ¥ %s", record.OperatorUsername, record.OperatorID, balanceMoneyText(record.BalanceBefore), balanceMoneyText(record.BalanceAfter))
	}
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

func formatBillingMultiplier(value string) string {
	multiplier, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || multiplier < 0 {
		return ""
	}
	return strconv.FormatFloat(multiplier, 'f', -1, 64) + "x"
}

func extractBillingSnapshotFinalMultipliers(snapshot string) []string {
	labels := make([]string, 0, 2)
	for index := 1; index <= 20; index++ {
		call := ExtractBillingSnapshotField(snapshot, fmt.Sprintf("stageBillingCall%d", index))
		if call == "" {
			continue
		}
		for _, field := range strings.Split(call, ",") {
			key, value, ok := strings.Cut(field, "=")
			if !ok || strings.TrimSpace(key) != "finalMultiplier" {
				continue
			}
			if label := formatBillingMultiplier(value); label != "" {
				labels = appendUniqueBillingLabel(labels, label)
			}
		}
	}
	return labels
}

func buildBillingRecordFinalMultiplier(recordType string, snapshot string) string {
	if normalizeBillingRecordType(recordType) != normalSearchBillingRecordType {
		return "-"
	}
	if labels := extractBillingSnapshotFinalMultipliers(snapshot); len(labels) > 0 {
		return strings.Join(labels, "、")
	}
	if label := formatBillingMultiplier(ExtractBillingSnapshotField(snapshot, "multiplier")); label != "" {
		return label
	}
	if ExtractBillingSnapshotField(snapshot, "pricing") == "unpriced_model" {
		return "-"
	}
	return "1x"
}

func parseNonnegativeBillingSnapshotFloat(snapshot string, key string) (float64, bool) {
	value, err := strconv.ParseFloat(ExtractBillingSnapshotField(snapshot, key), 64)
	return value, err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func parseNonnegativeBillingSnapshotInt(snapshot string, key string) (int64, bool) {
	value, err := strconv.ParseInt(ExtractBillingSnapshotField(snapshot, key), 10, 64)
	return value, err == nil && value >= 0
}

func billingAIUnmultipliedBaseline(snapshot string) (float64, bool) {
	if ExtractBillingSnapshotField(snapshot, "pricing") != "model_price_table" {
		return 0, false
	}
	totalTokens, ok := parseNonnegativeBillingSnapshotInt(snapshot, "totalTokens")
	if !ok || totalTokens == 0 {
		return 0, false
	}

	baseline := 0.0
	for _, category := range []struct{ tokens, price string }{
		{"inputTokens", "inputPrice"},
		{"cachedInputTokens", "cachedPrice"},
		{"cacheWriteTokens", "cacheWritePrice"},
		{"outputTokens", "outputPrice"},
	} {
		tokens, ok := parseNonnegativeBillingSnapshotInt(snapshot, category.tokens)
		if !ok {
			return 0, false
		}
		if tokens == 0 {
			continue
		}
		price, ok := parseNonnegativeBillingSnapshotFloat(snapshot, category.price)
		if !ok {
			return 0, false
		}
		baseline += float64(tokens) * price / 1_000_000
	}
	return baseline, baseline > 0 && !math.IsNaN(baseline) && !math.IsInf(baseline, 0)
}

func formatKnownBillingMultiplier(value string) string {
	multiplier, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return ""
	}
	return strconv.FormatFloat(multiplier, 'f', -1, 64) + "x"
}

func knownHistoricalAIMultiplier(snapshot string) string {
	if ExtractBillingSnapshotField(snapshot, "pricing") == "unpriced_model" ||
		parseBillingSnapshotInt(snapshot, "stageBillingCallsOmitted") > 0 {
		return ""
	}
	callCount := 0
	label := ""
	for index := 1; index <= 20; index++ {
		encoded := ExtractBillingSnapshotField(snapshot, fmt.Sprintf("stageBillingCall%d", index))
		if encoded == "" {
			continue
		}
		callCount++
		fields := parseBillingSnapshotPairs(encoded)
		callLabel := formatKnownBillingMultiplier(fields["finalMultiplier"])
		if callLabel == "" || (label != "" && label != callLabel) {
			return ""
		}
		label = callLabel
	}
	if expectedText := ExtractBillingSnapshotField(snapshot, "stageBillingModelCallCount"); expectedText != "" {
		expected, err := strconv.Atoi(expectedText)
		if err != nil || expected != callCount {
			return ""
		}
	}
	if callCount > 0 {
		return label
	}
	return formatKnownBillingMultiplier(ExtractBillingSnapshotField(snapshot, "multiplier"))
}

// 列表倍率只比较 AI 模型费用与同一批 Token 按落账价格快照计算的未乘倍率金额。
func buildBillingSearchEffectiveMultiplier(charges []BillingRecordListItem) string {
	if len(charges) == 0 || normalizeBillingRecordType(charges[0].Type) != normalSearchBillingRecordType {
		return "-"
	}
	var actual, baseline float64
	hasAI := false
	completeBaseline := true
	historicalLabel := ""
	hasSingleHistoricalLabel := true
	for _, charge := range charges {
		if strings.TrimSpace(charge.Provider) != "ai" {
			continue
		}
		hasAI = true
		if amount, ok := billingAIUnmultipliedBaseline(charge.UnitPriceSnapshot); ok && charge.Amount >= 0 && !math.IsNaN(charge.Amount) && !math.IsInf(charge.Amount, 0) {
			actual += charge.Amount
			baseline += amount
		} else {
			completeBaseline = false
		}
		label := knownHistoricalAIMultiplier(charge.UnitPriceSnapshot)
		if label == "" || (historicalLabel != "" && historicalLabel != label) {
			hasSingleHistoricalLabel = false
		}
		historicalLabel = label
	}
	if !hasAI {
		return "-"
	}
	if completeBaseline && baseline > 0 {
		ratio := actual / baseline
		if !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
			return strconv.FormatFloat(math.Round(ratio*10_000)/10_000, 'f', -1, 64) + "x"
		}
	}
	if hasSingleHistoricalLabel && historicalLabel != "" {
		return historicalLabel
	}
	return "-"
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
		query = query.Where("billing_records.user_id = ? AND billing_records.hidden_from_statement = ?", operator.ID, false)
	}
	return query
}

// applyBillingRecordSearchDedup 保留每个 request_id 最新的一条明细。
//
// 一次搜题命中多个渠道会写成多条 normal_search，列表只展示最新的一条（代表整笔）。
func applyBillingRecordSearchDedup(query *gorm.DB, includeHidden bool) *gorm.DB {
	condition := `
		NOT EXISTS (
			SELECT 1
			FROM billing_records AS newer_records
			WHERE newer_records.user_id = billing_records.user_id
				AND newer_records.request_id = billing_records.request_id
				AND newer_records.type = ?
				AND billing_records.type = ?
				AND COALESCE(billing_records.request_id, '') <> ''
				AND newer_records.id > billing_records.id`
	args := []interface{}{normalSearchBillingRecordType, normalSearchBillingRecordType}
	if !includeHidden {
		condition += `
				AND newer_records.hidden_from_statement = ?`
		args = append(args, false)
	}
	condition += `
		)
	`
	return query.Where(condition, args...)
}

// buildBillingRecordDisplayQuery 构造账单展示用的基础查询（含同请求去重）。
func buildBillingRecordDisplayQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	return applyBillingRecordSearchDedup(buildBillingRecordScopeQuery(db, operator), isAdminUser(operator))
}

func decorateBillingRecord(record BillingRecordListItem) BillingRecordListItem {
	record.Type = normalizeBillingRecordType(record.Type)
	record.TypeLabel = buildBillingRecordTypeLabel(record.Type)
	record.Direction = buildBillingRecordDirection(record.Type)
	if record.Type == balanceAdjustmentBillingRecordType && record.BalanceAfter >= record.BalanceBefore {
		record.Direction = "income"
	}
	record.ShowInStatement = !record.HiddenFromStatement
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
	if record.Type == balanceAdjustmentBillingRecordType {
		record.ProviderLabel = "管理员调整"
	}
	if cachedLabel := buildCachedLocalProviderLabel(record.Provider, record.UnitPriceSnapshot); cachedLabel != "" {
		record.ProviderLabel = cachedLabel
	}
	if strings.TrimSpace(record.ProviderLabel) == "" {
		record.ProviderLabel = strings.TrimSpace(record.Provider)
	}
	record.AnswerSourceLabel = buildBillingAnswerSourceLabel(record.Provider, record.ProviderLabel, record.UnitPriceSnapshot)
	record.SearchModeLabel = buildBillingSearchModeLabel(record.SearchMode)
	record.FinalMultiplier = buildBillingRecordFinalMultiplier(record.Type, record.UnitPriceSnapshot)
	record.EffectiveMultiplier = buildBillingSearchEffectiveMultiplier([]BillingRecordListItem{record})
	// 优先用持久化的 question_text；旧行或新写入前为空时回退到快照解析。
	if questionText := strings.TrimSpace(record.QuestionText); questionText == "" {
		record.QuestionText = ExtractBillingSnapshotField(record.UnitPriceSnapshot, "question")
	}
	record.Subject = buildBillingRecordSubject(record)
	record.Detail = buildBillingRecordDetail(record)
	record.FeeDetails = buildBillingRecordFeeDetails(record)
	return record
}

func buildBillingRecordFeeDetails(record BillingRecordListItem) []BillingRecordFeeDetail {
	provider := strings.TrimSpace(record.Provider)
	snapshot := record.UnitPriceSnapshot
	if provider == "ai" {
		if details := buildAIModelFeeDetails(snapshot, record.ProviderLabel); len(details) > 0 {
			return details
		}
	}
	category := ""
	callIndex := 0
	switch provider {
	case "ai_search":
		category = "联网搜索"
		callIndex = parseBillingSnapshotInt(snapshot, "searchCall")
	case "web_read":
		category = "网页转Markdown"
		callIndex = parseBillingSnapshotInt(snapshot, "webReadCall")
	case "ai":
		category = "AI模型调用"
	}
	if record.Type == balanceAdjustmentBillingRecordType {
		category = "余额调整"
	}
	detail := buildBillingRecordFeeDetail(provider, snapshot)
	if record.Type == balanceAdjustmentBillingRecordType {
		detail = record.Detail
	}
	webReadURL := ""
	if provider == "web_read" {
		webReadURL = ExtractBillingSnapshotField(snapshot, "webReadURL")
	}
	return []BillingRecordFeeDetail{{
		Provider:      provider,
		ProviderLabel: strings.TrimSpace(record.ProviderLabel),
		Category:      category,
		CallIndex:     callIndex,
		Sequence:      parseBillingSnapshotInt(snapshot, "feeSequence"),
		Amount:        record.Amount,
		Detail:        detail,
		URL:           webReadURL,
	}}
}

func buildAIModelFeeDetails(snapshot string, providerLabel string) []BillingRecordFeeDetail {
	details := make([]BillingRecordFeeDetail, 0, 4)
	for index := 1; index <= 20; index++ {
		encoded := ExtractBillingSnapshotField(snapshot, fmt.Sprintf("stageBillingCall%d", index))
		if encoded == "" {
			continue
		}
		fields := parseBillingSnapshotPairs(encoded)
		amount, err := strconv.ParseFloat(fields["amount"], 64)
		if err != nil {
			continue
		}
		callIndex := index
		if parsed, parseErr := strconv.Atoi(fields["index"]); parseErr == nil && parsed > 0 {
			callIndex = parsed
		}
		sequence := 0
		if parsed, parseErr := strconv.Atoi(fields["feeSequence"]); parseErr == nil && parsed > 0 {
			sequence = parsed
		}
		parts := make([]string, 0, 6)
		for _, key := range []string{"model", "round", "attempt", "totalTokens", "stageMultiplier", "finalMultiplier"} {
			if value := strings.TrimSpace(fields[key]); value != "" && value != "na" {
				parts = append(parts, key+"="+value)
			}
		}
		details = append(details, BillingRecordFeeDetail{
			Provider:      "ai",
			ProviderLabel: strings.TrimSpace(providerLabel),
			Category:      "AI模型调用",
			CallIndex:     callIndex,
			Sequence:      sequence,
			Amount:        util.RoundMoney(amount),
			Detail:        strings.Join(parts, "；"),
		})
	}
	return details
}

func parseBillingSnapshotPairs(value string) map[string]string {
	decoded, err := url.QueryUnescape(strings.TrimSpace(value))
	if err != nil {
		decoded = strings.TrimSpace(value)
	}
	fields := make(map[string]string)
	for _, part := range strings.Split(decoded, ",") {
		key, itemValue, ok := strings.Cut(part, "=")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(itemValue)
		}
	}
	return fields
}

func parseBillingSnapshotInt(snapshot string, key string) int {
	value, err := strconv.Atoi(ExtractBillingSnapshotField(snapshot, key))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func buildBillingRecordFeeDetail(provider string, snapshot string) string {
	if strings.TrimSpace(provider) != "ai_search" && strings.TrimSpace(provider) != "web_read" {
		return ""
	}
	parts := make([]string, 0, 7)
	appendField := func(label string, key string) {
		if value := ExtractBillingSnapshotField(snapshot, key); value != "" {
			parts = append(parts, label+"："+value)
		}
	}
	if strings.TrimSpace(provider) == "ai_search" {
		appendField("搜索词", "searchQuery")
		appendField("API", "searchAPI")
		appendField("状态", "searchStatus")
		appendField("结果", "searchResultCount")
		appendField("总结果", "searchTotalResults")
		appendField("来源", "searchSources")
		appendField("耗时", "searchDurationMs")
		appendField("错误", "searchError")
	} else {
		appendField("API", "webReadAPI")
		appendField("网页", "webReadURL")
		appendField("状态", "webReadStatus")
		appendField("Markdown字节", "webReadMarkdownBytes")
		appendField("耗时", "webReadDurationMs")
		appendField("错误", "webReadError")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
}

func loadBillingSearchGroups(db *gorm.DB, records []BillingRecordListItem, includeHidden bool) (map[billingSearchGroupKey][]BillingRecordListItem, error) {
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
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.amount, billing_records.hidden_from_statement, billing_records.created_at, billing_records.client_ip, billing_records.unit_price_snapshot, billing_records.request_id, billing_records.search_mode, billing_records.remark, access_tokens.name AS access_token_name").
		Joins("LEFT JOIN "+entity.TableNameAccessToken+" AS access_tokens ON access_tokens.id = billing_records.access_token_id").
		Joins("LEFT JOIN "+entity.TableNameUser+" AS billing_users ON billing_users.id = billing_records.user_id").
		Where("billing_records.type = ?", normalSearchBillingRecordType).
		Where("billing_records.request_id IN ?", requestIDs).
		Where("billing_records.user_id IN ?", userIDs).
		Scopes(func(query *gorm.DB) *gorm.DB {
			if includeHidden {
				return query
			}
			return query.Where("billing_records.hidden_from_statement = ?", false)
		}).
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
	finalMultipliers := make([]string, 0, len(charges))
	feeDetails := make([]BillingRecordFeeDetail, 0, len(charges))
	for _, charge := range charges {
		totalAmount += charge.Amount
		if strings.TrimSpace(representative.ClientIP) == "" {
			representative.ClientIP = strings.TrimSpace(charge.ClientIP)
		}
		if questionText == "" {
			questionText = strings.TrimSpace(charge.QuestionText)
		}
		providerLabels = appendUniqueBillingLabel(providerLabels, charge.ProviderLabel)
		answerSourceLabels = appendUniqueBillingLabel(answerSourceLabels, charge.AnswerSourceLabel)
		if multiplier := strings.TrimSpace(charge.FinalMultiplier); multiplier != "" && multiplier != "-" {
			finalMultipliers = appendUniqueBillingLabel(finalMultipliers, multiplier)
		}
		feeDetails = append(feeDetails, charge.FeeDetails...)
	}

	representative.Amount = util.RoundMoney(totalAmount)
	representative.QuestionText = questionText
	representative.ProviderLabel = strings.Join(providerLabels, "、")
	representative.AnswerSourceLabel = strings.Join(answerSourceLabels, "、")
	if len(finalMultipliers) > 0 {
		representative.FinalMultiplier = strings.Join(finalMultipliers, "、")
	}
	representative.EffectiveMultiplier = buildBillingSearchEffectiveMultiplier(charges)
	if len(feeDetails) > 0 {
		applyBillingFeeSequenceFallback(feeDetails)
		sort.SliceStable(feeDetails, func(left, right int) bool {
			leftSequence := feeDetails[left].Sequence
			rightSequence := feeDetails[right].Sequence
			if leftSequence <= 0 || rightSequence <= 0 {
				return false
			}
			return leftSequence < rightSequence
		})
		representative.FeeDetails = feeDetails
	}
	representative.Subject = buildSearchBillingSubject(questionText)
	representative.Detail = buildBillingRecordDetail(representative)
	return representative
}

func redactPersonalBillingRecord(record BillingRecordListItem) BillingRecordListItem {
	record.FinalMultiplier = ""
	record.EffectiveMultiplier = ""
	for index := range record.FeeDetails {
		if strings.TrimSpace(record.FeeDetails[index].Provider) == "ai" ||
			record.FeeDetails[index].Category == "AI模型调用" {
			record.FeeDetails[index].Detail = ""
		}
	}
	return record
}

func applyBillingFeeSequenceFallback(details []BillingRecordFeeDetail) {
	hasAI := false
	for _, detail := range details {
		if detail.Provider == "ai" || detail.Category == "AI模型调用" {
			hasAI = true
			break
		}
	}
	if !hasAI {
		return
	}
	for index := range details {
		if details[index].Sequence > 0 || details[index].CallIndex <= 0 {
			continue
		}
		switch {
		case details[index].Provider == "ai" || details[index].Category == "AI模型调用":
			details[index].Sequence = details[index].CallIndex * 2
		case details[index].Provider == "ai_search" || details[index].Provider == "web_read":
			details[index].Sequence = details[index].CallIndex*2 + 1
		}
	}
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
	searchGroups, err := loadBillingSearchGroups(db, records, isAdminUser(operator))
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
	if !isAdminUser(operator) {
		for i := range records {
			records[i] = redactPersonalBillingRecord(records[i])
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
		Select("billing_records.id, billing_records.user_id, COALESCE(billing_users.username, '') AS username, billing_records.type, billing_records.provider, billing_records.model_name, billing_records.amount, billing_records.balance_before, billing_records.balance_after, billing_records.operator_id, billing_records.operator_username, billing_records.adjustment_mode, billing_records.hidden_from_statement, billing_records.created_at, billing_records.client_ip, billing_records.unit_price_snapshot, billing_records.question_text, billing_records.request_id, billing_records.search_mode, billing_records.remark, access_tokens.name AS access_token_name").
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
		if row.Direction == "income" || row.Type == balanceAdjustmentBillingRecordType {
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

	groupCondition, groupArgs := util.BuildInstrCondition(filter.Keyword,
		"COALESCE(matching_billing_users.username, '')",
		"COALESCE(matching_records.provider, '')",
		"COALESCE(matching_records.model_name, '')",
		"COALESCE(matching_records.question_text, '')",
		"COALESCE(matching_records.request_id, '')",
		"COALESCE(matching_records.remark, '')",
		"COALESCE(matching_access_tokens.name, '')",
	)
	if groupCondition == "" {
		return query.Where(condition, args...)
	}

	// A normal_search row represents the whole request group. A keyword matching
	// any sibling charge must retain the representative and aggregate every charge.
	groupMatch := `billing_records.type = ?
		AND COALESCE(billing_records.request_id, '') <> ''
		AND EXISTS (
			SELECT 1
			FROM billing_records AS matching_records
			LEFT JOIN ` + entity.TableNameAccessToken + ` AS matching_access_tokens ON matching_access_tokens.id = matching_records.access_token_id
			LEFT JOIN ` + entity.TableNameUser + ` AS matching_billing_users ON matching_billing_users.id = matching_records.user_id
			WHERE matching_records.user_id = billing_records.user_id
				AND matching_records.request_id = billing_records.request_id
				AND matching_records.type = ?
				AND ` + groupCondition + `
		)`
	groupArgs = append([]interface{}{normalSearchBillingRecordType, normalSearchBillingRecordType}, groupArgs...)
	args = append(args, groupArgs...)
	return query.Where("("+condition+") OR ("+groupMatch+")", args...)
}
