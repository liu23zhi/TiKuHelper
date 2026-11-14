package service

// ════════════════════════════════════════════════════════════════════
// R2 · login_bruteforce（96 §2.1 L107）
//
// 本文件与 risk_alert.go 里的 R9（credential_stuffing）是**同一张表的两种聚合**。
// 两条规则的边界是**深度 vs 广度**，97 号 L318 原话：「撞库特征，区别于 R2 的
// 『同账号多次』」。
//
//	R2 = 对**同一个目标**反复试（深度）
//	     · 账号维度：同一个 username 失败 N 次（user_id 必须 > 0）
//	     · IP 维度：同一个 client_ip 失败 N 次（user_id 不参与）
//	R9 = 从同一 IP 试**很多不同账号**（广度）
//
// # 对称性对照（这是两条规则唯一正确的形态）
//
//	|        | 账号维度          | IP 维度      | 是否区分不同账号 |
//	| R2     | user_id > 0       | 全算（含 0） | **否**（只看次数）|
//	| R9     | —                 | 全算         | **是**（≥N 个不同）|
//
// 写反了就是「R2 写成 R9」：账号维度漏掉 `user_id > 0` 会把「撞库」重复算成
// 「对 0 号账号的爆破」；R2 若去数「不同账号」就变成 R9。
//
// # 为什么必须用 login_failure_events 而不是 login_attempts
//
// 96 号 L116 说「R2 依赖 login_attempt」，98 号 L117 说「聚合表正好够用
// （同 subject 的 fail_count）」。**这两个说法都实现不了 R2**：
//
// `service.RecordLoginFailureWithDB` 有**三重**重置：
//  1. 窗口过期 → `row.FailCount = 1`（重新开始，不是累加）
//  2. 触发锁定 → `row.FailCount = 0` 且 `WindowStart = now`
//  3. 登录成功 → `succeed()` 清零
//
// ⇒ `login_attempts` 只保留「当前窗口的实时计数」。攻击者等锁定期过后再打，
// 实时表看到的永远是 0→10→0→10，**记不下「这个账号一天被打了 300 次」**。
// R2 的价值正是这个**跨窗口的长期视角** —— 实时闸门负责「当场拦住」，
// R2 负责「留下长期证据给管理员看」。
// ════════════════════════════════════════════════════════════════════

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// riskBruteforceWindow 是 R2 的判定窗口（10 分钟**滑动**）。
//
// ⚠️ 这个数字**来自 96 号文档 L107，不是拍的**：
//
//	| R2 | login_bruteforce | 登录爆破 | 同一账号或 IP 在 10min 内失败 ≥N 次（N 默认 10）|
//
// 用滑动窗口而不是自然日：爆破是「几分钟内打完」的短时爆发，自然日会把
// 23:55 与 00:05 拆到两天、各自都不到阈值 —— 双双漏报。这与 R9 同理。
const riskBruteforceWindow = 10 * time.Minute

// riskBruteforceDetailKind 区分 R2 的两个命中维度（写进 SubjectKey 与 Detail）。
const (
	riskBruteforceKindAccount = "account"
	riskBruteforceKindIP      = "ip"
)

// riskBruteforceHit 是一个命中的聚合结果。
type riskBruteforceHit struct {
	Kind       string // account / ip
	ScopeValue string // username 或 client_ip
	UserID     int32  // 账号维度填真实 user_id；IP 维度为 0
	Count      int    // 窗口内失败次数
	IPCount    int    // 账号维度：涉及多少个不同 IP；IP 维度：涉及多少个不同账号名
	SampleIP   string // 账号维度：样本 IP（最多 3 个，逗号分隔）
}

// collectLoginBruteforceCandidates 收集 R2 的命中（96 §2.1）。
//
// 两个维度都做，且**都只看次数**（不数不同账号）：
//  1. **账号维度**：`user_id > 0` 且同一 user_id 失败 ≥N 次。
//     `user_id = 0`（账号不存在）必须排除 —— 不存在的账号没有「账号」可爆破，
//     那些行属于 R9 的撞库范畴。若不排除，撞库会被重复算成「对 0 号账号的爆破」。
//  2. **IP 维度**：同一 client_ip 失败 ≥N 次，`user_id` 不参与（80% 是假账号名
//     也该算「该 IP 在爆破」）。
//
// # 与 R9 的去重（本仓决定，96/97 未明说）
//
// IP 维度会**跳过「已然是撞库形态」的 IP** —— 即该 IP 在窗口内失败涉及的
// 不同账号名数已 ≥ R9 的阈值。理由是 97 号 L318 明确要求 R9「区别于 R2 的
// 『同账号多次』」：若 R2 的 IP 维度也报同一批数据，两条告警就无法区分，
// 违背原文意图，也会让管理员以为系统在乱报。
//
// 账号维度不做这个去重：一个真实账号被反复试本身就是 R2 的正确命中，
// 哪怕同时也在撞库（那个账号恰好存在）。
func collectLoginBruteforceCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		return nil, nil
	}
	windowStart, windowEnd := now.Add(-riskBruteforceWindow), now
	timeExpr := riskWindowColumnExpr(db, "created_at")

	// 一次查询取窗口内全部失败明细，在 Go 侧做两个维度的聚合。
	//
	// ⚠️ 这里**与 R9 的查询刻意不同**：R9 的 `Select("client_ip, username")` 只取
	// 两列（它只数不同 username）；R2 必须多取 `user_id`（账号维度要用）——
	// 这是「R2 依赖 user_id、R9 忽略 user_id」在代码里的直接体现。
	type failureRow struct {
		UserID   int32
		ClientIP string
		Username string
	}
	var rows []failureRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.LoginFailureEvent{}).
		Select("user_id, client_ip, username").
		Where("client_ip <> ''").
		// ⚠️ 与 R9 的**关键区别**：R9 没有这一行（它必须把 user_id=0 也算进去）。
		// R2 在两个维度上都不按 user_id 过滤，但账号维度的分组键是 user_id，
		// 所以 user_id=0 的行会在下面的聚合里被显式跳过（而不是在这里过滤）——
		// 因为 IP 维度**需要** user_id=0 的行参与计数。
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?",
			windowStart.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// ── 维度一：账号（user_id > 0）──
	accountCounts := make(map[int32]int, 8)
	accountIPs := make(map[int32]map[string]struct{}, 8)
	accountOrder := make([]int32, 0, 8)
	// ── 维度二：IP（全算，含 user_id=0）──
	ipCounts := make(map[string]int, 8)
	ipUsernames := make(map[string]map[string]struct{}, 8)
	ipOrder := make([]string, 0, 8)

	for _, row := range rows {
		ip := strings.TrimSpace(row.ClientIP)
		username := strings.TrimSpace(row.Username)

		// 账号维度：只有真实存在的账号才可「被爆破」。
		if row.UserID > 0 {
			if _, ok := accountCounts[row.UserID]; !ok {
				accountOrder = append(accountOrder, row.UserID)
				accountIPs[row.UserID] = make(map[string]struct{}, 4)
			}
			accountCounts[row.UserID]++
			if ip != "" {
				accountIPs[row.UserID][ip] = struct{}{}
			}
		}

		// IP 维度：`user_id` 不参与（R9 已覆盖「IP 试多账号」的形态，
		// 但那不影响这里计数 —— 去重放在后面按「是否撞库形态」整组跳过）。
		if ip != "" {
			if _, ok := ipCounts[ip]; !ok {
				ipOrder = append(ipOrder, ip)
				ipUsernames[ip] = make(map[string]struct{}, 4)
			}
			ipCounts[ip]++
			if username != "" {
				ipUsernames[ip][username] = struct{}{}
			}
		}
	}

	candidates := make([]riskWarningCandidate, 0, 8)

	// ── 输出账号维度命中 ──
	for _, userID := range accountOrder {
		count := accountCounts[userID]
		if float64(count) < threshold {
			continue
		}
		sampleIPs := make([]string, 0, 3)
		for ip := range accountIPs[userID] {
			if len(sampleIPs) >= 3 {
				break
			}
			sampleIPs = append(sampleIPs, ip)
		}
		sortStrings(sampleIPs)
		subject := riskBehaviorSubject(rule.RuleID,
			riskBruteforceKindAccount+":"+strconv.FormatInt(int64(userID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":        rule.RuleID,
			"subjectKey":    subject,
			"dimension":     riskBruteforceKindAccount,
			"userId":        userID,
			"username":      riskUsernamesByID(db, []int32{userID})[userID],
			"threshold":     threshold,
			"value":         count,
			"failCount":     count,
			"ipCount":       len(accountIPs[userID]),
			"sampleIps":     sampleIPs,
			"windowMinutes": int(riskBruteforceWindow / time.Minute),
			"windowKind":    "rolling",
			"windowStart":   windowStart.Format(time.RFC3339),
			"windowEnd":     windowEnd.Format(time.RFC3339),
			"evaluatedAt":   now.Format(time.RFC3339),
			"evidenceNote": "同一账号在 10 分钟滑动窗口内失败达到阈值次数（96 §2.1 R2）；" +
				"所有失败原因同等计入（排除任一类都会产生绕过面）；" +
				"这是**深度**攻击，与撞库（广度）由不同规则覆盖",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}

	// ── 输出 IP 维度命中（跳过撞库形态的 IP，交给 R9）──
	// R9 的阈值：读同一套 system_settings，缺省用 R9 的代码默认值。
	//
	// 这里构造一个「只用于读阈值」的临时 RiskRule（`riskRuleThreshold` 的签名要求），
	// 不查库取规则行 —— 因为 R9 可能被管理员禁用（`enabled=false`），但**去重逻辑
	// 不该因此失效**：即便 R9 关着，R2 的 IP 维度仍应避开撞库形态的数据，
	// 否则「两条规则都关着但 R2 在报撞库数据」会让管理员困惑。
	stuffingThreshold := riskRuleThreshold(db, entity.RiskRule{
		RuleID:    entity.RiskRuleIDCredentialStuffing,
		Threshold: riskDefaultThreshold(entity.RiskRuleIDCredentialStuffing),
	}, riskDefaultThreshold(entity.RiskRuleIDCredentialStuffing))
	// IP 触达的代表账号，取第一个真实 user_id 以便告警能关联到人（IP 维度本身不要求）。
	ipOwnerID := make(map[string]int32, len(ipOrder))
	for _, row := range rows {
		ip := strings.TrimSpace(row.ClientIP)
		if ip == "" || row.UserID <= 0 {
			continue
		}
		if _, ok := ipOwnerID[ip]; !ok {
			ipOwnerID[ip] = row.UserID
		}
	}
	for _, ip := range ipOrder {
		count := ipCounts[ip]
		if float64(count) < threshold {
			continue
		}
		// ⚠️ 本仓决定（96/97 未明说）：若该 IP 已构成撞库形态，交给 R9，R2 不重复报。
		// 依据 97 号 L318「区别于 R2 的『同账号多次』」。
		if stuffingThreshold > 0 && float64(len(ipUsernames[ip])) >= stuffingThreshold {
			continue
		}
		sampleUsernames := make([]string, 0, 3)
		for username := range ipUsernames[ip] {
			if len(sampleUsernames) >= 3 {
				break
			}
			sampleUsernames = append(sampleUsernames, username)
		}
		sortStrings(sampleUsernames)
		subject := riskBehaviorSubject(rule.RuleID, riskBruteforceKindIP+":"+ip, windowStart)
		detailMap := map[string]any{
			"ruleId":        rule.RuleID,
			"subjectKey":    subject,
			"dimension":     riskBruteforceKindIP,
			"ip":            ip,
			"threshold":     threshold,
			"value":         count,
			"failCount":     count,
			"accountCount":  len(ipUsernames[ip]),
			"sampleUsers":   sampleUsernames,
			"windowMinutes": int(riskBruteforceWindow / time.Minute),
			"windowKind":    "rolling",
			"windowStart":   windowStart.Format(time.RFC3339),
			"windowEnd":     windowEnd.Format(time.RFC3339),
			"evaluatedAt":   now.Format(time.RFC3339),
			"evidenceNote": "同一 IP 在 10 分钟滑动窗口内失败达到阈值次数（96 §2.1 R2）；" +
				"user_id 不参与计数（假账号名也算该 IP 在爆破）；" +
				"已跳过撞库形态的 IP（不同账号数已达 R9 阈值），那些由 credential_stuffing 覆盖",
		}
		var candidateUserID *int32
		if ownerID, ok := ipOwnerID[ip]; ok && ownerID > 0 {
			owner := ownerID
			candidateUserID = &owner
		}
		detail, err := json.Marshal(detailMap)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   candidateUserID,
			Severity: rule.Severity,
			Detail:   string(detail),
			IP:       ip,
		})
	}
	return candidates, nil
}

// sortStrings 是 sort.Strings 的薄包装（避免本文件额外 import sort 与既有命名冲突）。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
