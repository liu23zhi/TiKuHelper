package service

import (
	"encoding/json"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R5 · register_burst（96 §2.1 L110）
//
// 判据：同一 IP 段在 1h 滚动窗口内注册 ≥N 个账号（N 默认 3，节流每段 6h）。
//
// # 关于「N=3 配 /24 是否合理」（t197 的核心设计判断）
//
// **结论：按 96 号原样实现（/24 + N=3），但必须排除管理员/系统建的账号。**
//
// 理由分三层，前两层是文档给定，第三层是本仓必须加的技术限定：
//
//	① 96 号 L110 明确给了「同一 IP 段 / 1h / N 默认 3 / 每段 6h」，参数齐备。
//	   没有「实现者可以自行调整」的余地 —— 偏离文档需要更强的理由。
//	② 97 号 L279 说「同段 1h 注册 3 个，**误伤风险低**」。但**那句话的语境是
//	   「可否自动拉黑 IP」**（该表列头是「可自动处置」），不是「告警频率低」。
//	   ⇒ 不能拿它当作「N=3 不会频繁误报」的依据。这一点必须说清，否则会误导。
//	③ 真正的问题不在 N，而在**数据源混入了非「注册」语义的账号**：
//	   `users.register_ip` 有三处写入路径，其中 `user.go:1290` 是**管理员建号**，
//	   它记的是「管理员当时的 IP」（`entity/users.gen.go` 的字段注释原话：
//	   「父/子账号由管理员创建时记录管理员当时的 IP」）。
//	   ⇒ 管理员在后台批量建 5 个账号，会**立刻**触发 R5；
//	   而这 5 个账号来自同一个操作者，不是「撞库注册」。
//
// 所以本实现的取舍是：**粒度和阈值完全按 96 号（/24 + N=3）**，
// 只把「管理员/系统建号」排除掉 —— 那不是「优化阈值」，而是**修正数据源语义**
// （R5 要的是「有多少人在这个段注册」，不是「管理员在这个段建了多少号」）。
//
// 如果运营觉得仍然太敏感，可以调阈值键（含上界 10000），不需要改代码。
//
// # 段粒度：/24，IPv6 跳过
//
// /24 = 256 个地址。/28 或 /32 会让「段」失去意义（/32 就是单 IP，
// 那是 R1 的粒度）；而 /16 会把整个运营商片区混成一段、噪声更大。
// 96 号说「IP 段」，`/24` 是对「一个机房/一栋楼/一个企业出口」最贴近的粒度。
//
// IPv6 不做 /24（/24 对 IPv6 无意义，前缀长度语义完全不同）。
// 本实现**跳过 IPv6**：`register_ip` 若有 IPv6，不参与 R5。
// 理由：IPv6 的「段」至少是 /64，一个 /64 通常是**单个用户**的地址池 ——
// 按 /64 聚合会把「同一个人的多个设备」当成「多人注册」，
// 而 IPv6 地址轮换（隐私扩展）会让「同一个人的两次注册」落到两个 /64。
// 两种方向都会误判，所以宁可不做（如实写进 Description）。
// ════════════════════════════════════════════════════════════════════

// riskRegisterBurstWindow 是 R5 的判定窗口（1 小时**滚动**，96 号 L110）。
const riskRegisterBurstWindow = 60 * time.Minute

// riskRegisterBurstSourceAdmin / System 是**不计入** R5 的 register_source 取值。
//
// 依据（实测 + 代码）：
//   - `controller/user.go:1290`（管理员建号）会把 `payload.RegisterSource` 原样写入；
//   - 生产库实测取值是 `admin` / `system`（另有 `public`）；
//   - `entity/users.gen.go` 的字段注释写明「父/子账号由管理员创建时记录管理员当时的 IP」。
//
// ⇒ 这些账号的 register_ip 是**操作者的地址**，不是「注册者来源」。
// 把它们算进 R5，会让「管理员批量建号」直接触发告警（一次建 5 个就够）。
//
// 用**白名单**而不是黑名单：只统计 `public`（真正的自助注册）。
// 将来新增 register_source 取值时默认**不计入** —— 与「宁可漏报不要误报」一致。
const riskRegisterBurstSourcePublic = "public"

// riskRegisterSegmentCIDR 是段粒度（96 号说「IP 段」，取 /24）。
const riskRegisterSegmentCIDR = 24

// riskRegisterSegmentOf 把 IPv4 归一到所属 /24 段（CIDR 文本）。
//
// 返回空串表示**不参与 R5**，三种情形：
//   - 非 IPv4（含 IPv6、非法值、空值）—— /24 对 IPv6 无意义
//   - 私网 / 保留地址 —— 生产库两个账号的 register_ip 都是 172.18.0.1（Docker bridge），
//     不跳过会让「同一台宿主机上的所有账号」天然同段（详见文件顶部的误报分析）
func riskRegisterSegmentOf(rawIP string) string {
	trimmed := strings.TrimSpace(rawIP)
	if trimmed == "" {
		return ""
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return ""
	}
	ipv4 := ip.To4()
	// IPv6 / IPv4-mapped 之外的一律跳过：/24 语义只对 IPv4 成立。
	if ipv4 == nil {
		return ""
	}
	// 私网 / 回环 / 保留地址：不是「公网来源」，按段聚合没有意义。
	if IsPrivateOrReservedIP(trimmed) {
		return ""
	}
	// 用 mask 统一清零主机位 —— **不能**手写 `net.IPv4(ipv4[0], ipv4[1], ipv4[2], 0)`：
	// 那样只对 /24 成立，改粒度时 IP 与 mask 会自相矛盾（例如 /32 却把地址清零成 .0）。
	// 这段写法由 mutation「段粒度 /24 → /32」验证过：手写清零时改常量不生效，用例拷不动。
	mask := net.CIDRMask(riskRegisterSegmentCIDR, 32)
	if mask == nil {
		return ""
	}
	network := net.IPNet{
		IP:   ipv4.Mask(mask),
		Mask: mask,
	}
	return network.String()
}

// collectRegisterBurstCandidates 收集 R5 的命中（96 §2.1）。
//
// # 边界（每条都有用例）
//
//   - **私网 IP 跳过**：生产库两个账号的 register_ip 都是 172.18.0.1，
//     不跳过会让同一台宿主机上的账号天然同段。
//   - **register_ip 空 → 跳过**：不能把「未知」当成一个段（否则所有无 IP 的账号
//     会被聚成一段，凭空造出一条告警）。
//   - **IPv6 → 跳过**（见文件顶部说明）。
//   - **管理员/系统建号不计入**：只统计 `register_source = public`。
//   - 同一 IP 多次注册不同账号 → 正常累计（这正是要抓的形态）。
func collectRegisterBurstCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil, nil
	}
	windowStart, windowEnd := now.Add(-riskRegisterBurstWindow), now
	timeExpr := riskWindowColumnExpr(db, "created_at")

	type registerRow struct {
		Username       string
		RegisterIP     string
		RegisterSource string
	}
	var rows []registerRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Select("username, register_ip, register_source").
		Where("register_ip <> ''").
		// 只统计自助注册：管理员/系统建号的 register_ip 是**操作者**地址，不是注册者来源。
		Where("register_source = ?", riskRegisterBurstSourcePublic).
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?",
			windowStart.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	type segmentGroup struct {
		Count     int
		Usernames []string
		IPs       map[string]struct{}
	}
	groups := make(map[string]*segmentGroup, 8)
	order := make([]string, 0, 8)
	for _, row := range rows {
		segment := riskRegisterSegmentOf(row.RegisterIP)
		// 私网 / IPv6 / 非法 / 空 —— 一律不参与。
		if segment == "" {
			continue
		}
		group, ok := groups[segment]
		if !ok {
			group = &segmentGroup{Usernames: make([]string, 0, 4), IPs: make(map[string]struct{}, 4)}
			groups[segment] = group
			order = append(order, segment)
		}
		group.Count++
		group.IPs[strings.TrimSpace(row.RegisterIP)] = struct{}{}
		username := strings.TrimSpace(row.Username)
		if username != "" && len(group.Usernames) < 5 {
			group.Usernames = append(group.Usernames, username)
		}
	}

	subjectWindow, _ := riskLoginWindow(now)
	candidates := make([]riskWarningCandidate, 0, 4)
	for _, segment := range order {
		group := groups[segment]
		// 「≥ 阈值」：低于阈值跳过。
		if float64(group.Count) < threshold {
			continue
		}
		sort.Strings(group.Usernames)
		subject := riskBehaviorSubject(rule.RuleID, "segment:"+segment, subjectWindow)
		detail, err := json.Marshal(map[string]any{
			"ruleId":     rule.RuleID,
			"subjectKey": subject,
			"segment":    segment,
			// threshold 是**配置值**，不是实际计数（后者在 registerCount）。
			"threshold":     threshold,
			"value":         group.Count,
			"registerCount": group.Count,
			"ipCount":       len(group.IPs),
			"sampleUsers":   group.Usernames,
			"windowMinutes": int(riskRegisterBurstWindow / time.Minute),
			"windowKind":    "rolling",
			"windowStart":   windowStart.Format(time.RFC3339),
			"windowEnd":     windowEnd.Format(time.RFC3339),
			"evaluatedAt":   now.Format(time.RFC3339),
			"evidenceNote": "同一 /24 段在 1 小时滑动窗口内的**自助注册**账号数（register_source=public）。" +
				"已跳过：私网/保留 IP（如 172.18.0.1 Docker bridge）、IPv6、空 register_ip；" +
				"管理员与系统建号不计入（它们的 register_ip 是操作者地址）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
			// 纯段维度，没有单个用户可归属 —— UserID 留空（与 R1 的纯 IP 维度一致）。
		})
	}
	return candidates, nil
}
