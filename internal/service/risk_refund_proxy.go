package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R13 · refund_abuse（97 §5.1 R13）
//
// 判据：单用户 **30 天内成功退款 ≥N 次，且每次都接近满额**。
//
// # ⚠️ 97 号的数据源写错了
//
// 97 号 L322 写的是 `billing_records`。**那是错的**：
// 账单表只有收支金额，没有「退款申请 → 审核 → 终态」这套状态机。
// 退款主表是 `ticket_refunds`（entity.TicketRefund）。
//
// # 为什么按实际结算口径
//
// `ticket_refunds.status` 有 7 个取值（entity/ticket_refund.go:9-15）：
//
//	pending_review / rejected / frozen / processing / unknown / succeeded / failed
//
// 只有成功的整笔退款或部分成功申请中已成功的逐笔明细代表钱真的退出去了。
// `ticket_refund.go:359` 明确拒绝从 succeeded/failed 再改状态。
//
// 算错 status 的后果：把「申请 3 次全被驳回」的用户误报成退款滥用。
// 那类用户恰恰是**被系统保护了**的人，不该被当成嫌疑人。
//
// # 「接近满额」的定义与数字来源
//
// 比例 = 同一申请实际退款 / 对应已退款订单实付，要求 ≥ riskRefundNearFullRatio（90%）。
//
// ⚠️ **90% 是本仓自定的经验值**（97 号只说「接近满额」，没给数字）。
// 取 90% 的理由：正常退款常因「部分未使用/按比例退」而明显低于满额；
// 而套利行为通常是「用掉大部分价值后全额退」，比例贴近 100%。
// 90% 在这两者之间留出缓冲，运营可通过阈值键调整次数但对比例无配置入口 ——
// 比例是判据形态的一部分，不宜频繁改（若需可调，应升级为独立键）。
// ════════════════════════════════════════════════════════════════════

// riskRefundAbuseWindow 是 R13 的判定窗口（30 天**滚动**）。
//
// 用滚动而不是自然月：R9（30min）/R2（10min）/R14（24h）全部用滚动，
// 自然月会让「月初 + 月末」的一次套利被拆成两个月、各自都不到阈值。
const riskRefundAbuseWindow = 30 * 24 * time.Hour

// riskRefundNearFullRatio 是「接近满额」的比例下限（0.9 = 90%）。
//
// ⚠️ **本仓自定**，97 号未给数字。见文件顶部注释里的取值理由。
const riskRefundNearFullRatio = 0.9

// One row per application, even when multiple original payments funded it.
// Cents on original-channel lines are allocations; manual marker lines contain
// no allocation, so the successful parent amount is the only payout numerator.
type refundAbuseRow struct {
	RefundID                                            int64
	UserID, ParentOrderID                               int32
	Kind, Status                                        string
	ApprovedCents, OrderPaidCents                       int64
	Lines, SucceededLines, InvalidLines, IncludesParent int64
	SettledCents, SettledPaidCents                      int64
}

func (r refundAbuseRow) settledAmounts() (refunded, paid int64) {
	switch {
	case r.Lines == 0: // Legacy single-order payout, no new-format line exists.
		if r.Status != TicketRefundStatusSucceeded {
			return 0, 0
		}
		refunded, paid = r.ApprovedCents, r.OrderPaidCents
	case r.InvalidLines != 0 || r.SucceededLines == 0:
		return 0, 0 // Never substitute the parent estimate for a failed/unknown payout.
	case r.Kind == TicketRefundKindOriginal:
		refunded, paid = r.SettledCents, r.SettledPaidCents
	case r.Kind == TicketRefundKindManual && r.Status == TicketRefundStatusSucceeded && r.Lines == r.SucceededLines:
		refunded, paid = r.ApprovedCents, r.SettledPaidCents
		if r.ParentOrderID > 0 && r.IncludesParent == 0 {
			if r.OrderPaidCents <= 0 {
				return 0, 0
			}
			paid += r.OrderPaidCents
		}
	}
	if refunded <= 0 || paid <= 0 || refunded > paid || refunded > r.ApprovedCents {
		return 0, 0
	}
	return refunded, paid
}

// Only terminal applications enter the rolling window. In a partially
// successful application, only confirmed lines contribute to either sum.
func collectRefundAbuseCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 || !db.Migrator().HasTable(&entity.TicketRefund{}) {
		return nil, nil
	}
	windowStart := now.Add(-riskRefundAbuseWindow)
	// Old rows may not have a settlement timestamp; retain their prior window.
	timeExpr := riskWindowColumnExpr(db, "COALESCE(r.resolved_at, r.created_at)")
	query := db.Session(&gorm.Session{NewDB: true}).Table("ticket_refunds AS r").
		Where("r.status IN ? AND r.user_id > 0 AND r.approved_cents > 0", []string{TicketRefundStatusSucceeded, TicketRefundStatusPartial}).
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?", windowStart.Format("2006-01-02 15:04:05"), now.Format("2006-01-02 15:04:05"))
	columns := "r.id AS refund_id, r.user_id, COALESCE(r.order_id, 0) AS parent_order_id, r.kind, r.status, r.approved_cents, r.order_paid_cents"
	if db.Migrator().HasTable(&entity.TicketRefundOrderLine{}) {
		query = query.Joins("LEFT JOIN ticket_refund_order_lines l ON l.refund_id = r.id").
			Group("r.id, r.user_id, r.order_id, r.kind, r.status, r.approved_cents, r.order_paid_cents")
		columns += `, COUNT(l.id) AS lines,
			SUM(CASE WHEN l.status = 'succeeded' THEN 1 ELSE 0 END) AS succeeded_lines,
			SUM(CASE WHEN l.status = 'succeeded' AND (l.paid_cents <= 0 OR (r.kind = 'original' AND (l.cents <= 0 OR l.cents > l.paid_cents))) THEN 1 ELSE 0 END) AS invalid_lines,
			MAX(CASE WHEN l.status = 'succeeded' AND l.order_id = r.order_id THEN 1 ELSE 0 END) AS includes_parent,
			SUM(CASE WHEN l.status = 'succeeded' THEN l.cents ELSE 0 END) AS settled_cents,
			SUM(CASE WHEN l.status = 'succeeded' THEN l.paid_cents ELSE 0 END) AS settled_paid_cents`
	}
	var rows []refundAbuseRow
	if err := query.Select(columns).Order("r.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}

	// 按 user_id 聚合「接近满额的退款」。
	type refundAggregate struct {
		NearFullCount  int
		TotalRefunds   int
		TotalApproved  int64
		TotalOrderPaid int64
		SampleRefundID int64
	}
	aggregates := make(map[int32]*refundAggregate, 8)
	order := make([]int32, 0, 8)
	for _, row := range rows {
		refunded, paid := row.settledAmounts()
		if refunded == 0 {
			continue
		}
		aggregate, ok := aggregates[row.UserID]
		if !ok {
			aggregate = &refundAggregate{SampleRefundID: row.RefundID}
			aggregates[row.UserID] = aggregate
			order = append(order, row.UserID)
		}
		aggregate.TotalRefunds++
		aggregate.TotalApproved += refunded
		aggregate.TotalOrderPaid += paid
		ratio := float64(refunded) / float64(paid)
		if ratio >= riskRefundNearFullRatio {
			aggregate.NearFullCount++
		}
	}

	windowStartForSubject, _ := riskLoginWindow(now)
	candidates := make([]riskWarningCandidate, 0, 4)
	for _, userID := range order {
		aggregate := aggregates[userID]
		// 判据的两个条件都要满足：次数够 **且** 每一笔都接近满额。
		//
		// ⚠️ 这里是「全部计数的退款都接近满额」还是「接近满额的退款达到 N 次」？
		// 97 号原文「退款 ≥3 次且每次都接近满额」→ 取**后者**（nearFullCount ≥ N），
		// 因为前者会把「2 次满额 + 1 次部分退」这种有正常行为的用户排除掉，
		// 而那种用户恰恰是套利后偶尔正常退一次的典型形态。
		if float64(aggregate.NearFullCount) < threshold {
			continue
		}
		subject := riskBehaviorSubject(rule.RuleID,
			"user:"+strconv.FormatInt(int64(userID), 10), windowStartForSubject)
		detail, err := json.Marshal(map[string]any{
			"ruleId":         rule.RuleID,
			"subjectKey":     subject,
			"userId":         userID,
			"username":       riskUsernamesByID(db, []int32{userID})[userID],
			"threshold":      threshold,
			"value":          aggregate.NearFullCount,
			"nearFullCount":  aggregate.NearFullCount,
			"totalRefunds":   aggregate.TotalRefunds,
			"nearFullRatio":  riskRefundNearFullRatio,
			"approvedTotal":  aggregate.TotalApproved,
			"orderPaidTotal": aggregate.TotalOrderPaid,
			"sampleRefundId": aggregate.SampleRefundID,
			"windowDays":     int(riskRefundAbuseWindow / (24 * time.Hour)),
			"windowKind":     "rolling",
			"windowStart":    windowStart.Format(time.RFC3339),
			"windowEnd":      now.Format(time.RFC3339),
			"evaluatedAt":    now.Format(time.RFC3339),
			"evidenceNote":   "按终态申请去重：已退金额 / 对应订单实付 ≥ 90%。部分成功只计成功明细；人工核销按实际打款总额计一次，无实付依据不计。",
		})
		if err != nil {
			return nil, err
		}
		userIDCopy := userID
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userIDCopy,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// riskProxyASNDefault 是 R10 名单的**代码默认值**（空 —— 必须由运营显式配置）。
//
// ⚠️ 刻意留空：97 号说「ASN 在名单内」，但**没给名单**。
// 硬编码一批（Google/AWS/DigitalOcean…）会让新部署环境**开箱就告警**，
// 而那些 IP 也可能属于本仓的正常用户（例如通过 Cloudflare/云主机访问）。
// 空名单 = 规则静默，运营按自己环境的实际情况填 —— 这与「宁可漏报也不要误报」一致。
const riskProxyASNDefault = ""

// ParseProxyASNList 解析 ASN 名单文本（逗号 / 分号 / 换行 / 空格分隔，形如 AS15169）。
//
// 形态照抄 `ParseLoginRateLimitExemptIPs`（login_rate_limit.go）：
// **读取时宽容跳过坏项**（老库可能有手工写入的坏值），
// 保存时由 ValidateProxyASNList 严格校验（坏项 → 400）。
//
// 归一化：统一大写、去掉 `AS` 前缀后的数字也接受（`15169` 与 `AS15169` 等价）。
func ParseProxyASNList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	seen := make(map[string]struct{}, len(fields))
	list := make([]string, 0, len(fields))
	for _, field := range fields {
		normalized := normalizeProxyASN(field)
		if normalized == "" {
			continue
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		list = append(list, normalized)
	}
	return list
}

// normalizeProxyASN 把 `AS15169` / `as15169` / `15169` 统一成 `AS15169`。
//
// 返回空串表示这一项不合法（非纯数字、或数字为空）。
func normalizeProxyASN(item string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(item))
	trimmed = strings.TrimPrefix(trimmed, "AS")
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "AS" + trimmed
}

// ValidateProxyASNList 校验名单文本（**保存时**用，坏项直接拒绝）。
//
// 与「读取时跳过坏项」是刻意的双层设计（照抄 login_rate_limit_exempt_ips）：
//   - 保存时严格 → 运营填错会收到 400，不会以为生效了其实没生效；
//   - 读取时宽容 → 老库里的坏值不会让整条规则 panic 或静默失效。
func ValidateProxyASNList(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		// 空名单是**合法**配置（= 关闭本规则）。
		return nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			continue
		}
		if normalizeProxyASN(field) == "" {
			return ErrRiskThresholdInvalid
		}
	}
	return nil
}

// proxyASNListWithDB 读取并解析 ASN 名单（读不到 / 全是坏项 → 空列表 = 不告警）。
func proxyASNListWithDB(db *gorm.DB) []string {
	if db == nil {
		return nil
	}
	raw := getSystemSettingStringWithDefault(db, RiskThresholdSettingKeyProxyASNList, riskProxyASNDefault)
	list := ParseProxyASNList(raw)
	if len(list) == 0 {
		return nil
	}
	return list
}

// collectProxyIPLoginCandidates 收集 R10 的命中（97 §5.1）。
//
// # 判据链路
//
//	logs（action=10 + status=success）取成功登录
//	  → 私网 IP 跳过（IsPrivateOrReservedIP）
//	  → 按 (user_id, client_ip) 去重
//	  → FindIPSegmentWithDB 查段
//	  → 段记录无 ASN 或段表未覆盖 → 跳过（**不能把「未知」当成「代理」**）
//	  → ASN 在运营名单内 → 命中
//
// # 名单为空 → 完全不告警
//
// 这是**最重要的一条边界**：97 号没给名单，若硬编码或对空名单也告警，
// 新部署环境会全量误报。空名单 = 规则静默，等运营配置。
func collectProxyIPLoginCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	asnList := proxyASNListWithDB(db)
	if len(asnList) == 0 {
		// 名单为空：直接返回，连查询都不做（省一次全表扫）。
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return nil, nil
	}
	windowStart, windowEnd := riskLoginWindow(now)
	timeExpr := riskWindowColumnExpr(db, "create_time")
	// 只回溯最近 7 天（与 R3 一致）：本规则判据是「是否用了代理」，
	// 不需要长期历史；7 天足够覆盖「今天登录过」的场景，又不会扫全表。
	since := now.Add(-7 * 24 * time.Hour)
	if windowStart.Before(since) {
		since = windowStart
	}

	type loginRow struct {
		UserID   int32
		ClientIP string
	}
	var rows []loginRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Log{}).
		Select("user_id, client_ip").
		Where("action = ?", riskLoginAuditAction).
		Where("module = ?", riskLoginAuditModule).
		Where("client_ip <> ''").
		Where("user_id > 0").
		Where("new_answer LIKE ?", `%"status":"success"%`).
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?",
			since.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")).
		Group("user_id, client_ip").
		Order("user_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	asnSet := make(map[string]struct{}, len(asnList))
	for _, asn := range asnList {
		asnSet[asn] = struct{}{}
	}

	type proxyHit struct {
		ASNList   []string
		SampleIPs []string
	}
	hits := make(map[int32]*proxyHit, 4)
	order := make([]int32, 0, 4)
	for _, row := range rows {
		ip := strings.TrimSpace(row.ClientIP)
		if ip == "" {
			continue
		}
		// 私网 / 保留地址一律跳过：它们永远不在公网段表里，
		// 且把内网 IP 当「代理」会让所有容器/内网部署全量误报（R3 同款教训）。
		if IsPrivateOrReservedIP(ip) {
			continue
		}
		segment, err := FindIPSegmentWithDB(db, ip)
		if err != nil {
			// 单条查询失败不该让整条规则失败（审计是旁路增强）。
			continue
		}
		// **段表查不到 → 跳过**。绝不能把「未知」当成「代理」。
		if segment == nil {
			continue
		}
		asn := normalizeProxyASN(segment.ASN)
		// 段记录没有 ASN → 跳过（见文件顶部关于 asn 空但 isp 有值的说明）。
		if asn == "" {
			continue
		}
		if _, matched := asnSet[asn]; !matched {
			continue
		}
		hit, ok := hits[row.UserID]
		if !ok {
			hit = &proxyHit{ASNList: make([]string, 0, 2), SampleIPs: make([]string, 0, 2)}
			hits[row.UserID] = hit
			order = append(order, row.UserID)
		}
		if !containsString(hit.ASNList, asn) {
			hit.ASNList = append(hit.ASNList, asn)
		}
		if len(hit.SampleIPs) < 2 && !containsString(hit.SampleIPs, ip) {
			hit.SampleIPs = append(hit.SampleIPs, ip)
		}
	}

	candidates := make([]riskWarningCandidate, 0, 4)
	for _, userID := range order {
		hit := hits[userID]
		subject := riskBehaviorSubject(rule.RuleID,
			"user:"+strconv.FormatInt(int64(userID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":     rule.RuleID,
			"subjectKey": subject,
			"userId":     userID,
			"username":   riskUsernamesByID(db, []int32{userID})[userID],
			// threshold 是**名单的规模**（配置值），不是命中数 ——
			// 与「detail.threshold 必须是配置值」的约定一致。
			"threshold":      len(asnList),
			"value":          len(hit.ASNList),
			"matchedASNs":    hit.ASNList,
			"matchedIPCount": len(hit.SampleIPs),
			"sampleIPs":      hit.SampleIPs,
			"asnList":        asnList,
			"windowStart":    windowStart.Format(time.RFC3339),
			"evaluatedAt":    now.Format(time.RFC3339),
			"evidenceNote": "登录 IP 的 ASN 落在运营配置的数据中心/代理名单内。" +
				"私网 IP、段表未覆盖、段记录无 ASN 的登录均已跳过（不把未知当代理）；" +
				"名单为空时本规则完全不告警",
		})
		if err != nil {
			return nil, err
		}
		userIDCopy := userID
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userIDCopy,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// containsString 判断切片里是否已有该字符串（小规模线性查找足够）。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
