package controller

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// logBillingProjectMeta 是「项目」列派生需要的那几个账单字段。
//
// 只在 logListItemsWithBillingIDs 的那**一次**批量查询里填充：
// buildLogProject 是纯函数，禁止任何 DB 调用（AGENTS.md 第 18b 条）。
type logBillingProjectMeta struct {
	Type         string
	QuestionText string
	Remark       string
}

// logBillingProjectSubject 按账单明细的「项目」口径，从关联账单派生项目名。
//
// 与 service.buildBillingRecordSubject 的分层一致：搜题类账单看题目（含工具测试题归一化），
// 其他类型（充值 / 赠送 / 调整）看备注。这里只覆盖日志能关联到的那部分，
// 不复制整张表，避免两处口径分叉。
func logBillingProjectSubject(billing *logBillingProjectMeta) string {
	if billing == nil {
		return ""
	}
	if billingType := strings.TrimSpace(billing.Type); billingType == "" || billingType == "normal_search" {
		if question := strings.TrimSpace(billing.QuestionText); question != "" {
			return service.BuildBillingSearchSubject(question)
		}
		return ""
	}
	return strings.TrimSpace(billing.Remark)
}

// buildLogProject 在读取时派生操作日志的「项目」列（方案 B：不落库、不加列）。
//
// 输入只有已 SELECT 出来的行字段，以及同一页那次批量关联账单得到的元数据，
// 因此：
//   - 历史行与新行走同一条路径，不需要任何回填迁移；
//   - 零额外查询，一页一次（既有测试 TestLogListQueriesBillingIDsOnceForCurrentPage 守着）；
//   - 口径集中在函数里，将来改口径只需改这里。
//
// 取值优先级按 module 分派，与账单明细的「项目」同源：
//   - search：关联账单的 question_text → service.BuildBillingSearchSubject（与账单完全同口径，
//     工具测试题自动归一化成项目名）；没有账单时退到 requestId；再兜底「搜题」；
//   - 账户类模块（auth/user/sms/recharge/access_token/billing）优先读白名单结构化快照，
//     再退到说明里的 ID；
//   - 题目类日志（module 为空）只能给「题目 #qid」，不回查 tiku（题库行可能已删，且会多一次查询）。
//
// 返回值永不为空字符串：一切取不到时统一 "-"（与账单 :338 的兜底一致）。
func buildLogProject(entry *entity.Log, billing *logBillingProjectMeta) string {
	if entry == nil {
		return "-"
	}
	module := strings.ToLower(strings.TrimSpace(entry.Module))
	description := strings.TrimSpace(entry.Description)
	snapshot := logProjectSnapshot(entry.NewAnswer)
	if len(snapshot) == 0 {
		snapshot = logProjectSnapshot(entry.OldAnswer)
	}

	switch module {
	case "search":
		if project := logBillingProjectSubject(billing); project != "" {
			return project
		}
		if requestID := firstLogProjectValue(logRequestID(description), logSnapshotText(snapshot, "requestId")); requestID != "" {
			return "搜题 requestId=" + requestID
		}
		return "搜题"
	case "ai_fallback":
		if id := firstLogProjectValue(logSnapshotText(snapshot, "requestId"), logRequestID(description), logSnapshotText(snapshot, "taskId")); id != "" {
			return "AI 兜底 " + id
		}
		return "AI 兜底"
	case "recharge":
		if orderNo := firstLogProjectValue(logSnapshotText(snapshot, "orderNo"), strings.TrimSpace(entry.CorrelationID), logRequestID(description)); orderNo != "" {
			return "充值订单 " + orderNo
		}
		return "充值"
	case "billing":
		if id := firstLogProjectValue(logSnapshotID(snapshot, "billingRecordId"), logDescriptionHashNumber(description)); id != "" {
			return "账单 #" + id
		}
		return "账单流水"
	case "user":
		if id := firstLogProjectValue(logSnapshotID(snapshot, "targetUserId"), logSnapshotID(snapshot, "userId"), logDescriptionUserID(description)); id != "" {
			return "用户 #" + id
		}
		return "用户"
	case "access_token":
		if name := firstLogProjectValue(logSnapshotNestedText(snapshot, "after", "name"), logSnapshotText(snapshot, "name")); name != "" {
			return "访问令牌 " + name
		}
		if id := logSnapshotID(snapshot, "tokenId"); id != "" {
			return "令牌 #" + id
		}
		return "访问令牌"
	case "sms":
		if id := logSnapshotID(snapshot, "smsRecordId"); id != "" {
			return "短信 #" + id
		}
		return "短信"
	case "captcha":
		if correlation := strings.TrimSpace(entry.CorrelationID); correlation != "" {
			if project := logCaptchaProject(correlation); project != "" {
				return project
			}
		}
		if id := logDescriptionField(description, "captchaId"); id != "" {
			return "验证码 " + id
		}
		return "验证码"
	case "auth":
		if label := logAuthProjectLabel(entry.Action); label != "" {
			return label
		}
		return "账号"
	case "review_rebate":
		if id := logSnapshotID(snapshot, "claimId"); id != "" {
			return "评价返现申请 #" + id
		}
		if id := logSnapshotID(snapshot, "ruleId"); id != "" {
			return "评价返现规则 #" + id
		}
		if id := logSnapshotID(snapshot, "programId"); id != "" {
			return "评价返现活动 #" + id
		}
		return "评价返现"
	case "userexam_tool":
		if name := logSnapshotText(snapshot, "name"); name != "" {
			return "客户端数据库：" + name
		}
		return "客户端数据库"
	}

	// 题目增删改的 module 一直是空字符串（tiku.go 四处都没有赋值），
	// 唯一的结构化依据是 qid。未知 module 带 qid 时同样降级到这一档。
	if entry.Qid > 0 {
		return "题目 #" + strconv.FormatInt(int64(entry.Qid), 10)
	}
	return "-"
}

// fillLogProjects 给整页行填 project。必须在 resolveLogUsernames 与
// logListItemsWithBillingIDs 之后调用（search 分支要读 billingIds）。
func fillLogProjects(items []logListItem, billingMeta map[int32]logBillingProjectMeta) {
	for i := range items {
		var meta *logBillingProjectMeta
		if ids := items[i].BillingIDs; len(ids) > 0 {
			if value, ok := billingMeta[ids[0]]; ok {
				meta = &value
			}
		}
		items[i].Project = buildLogProject(items[i].Log, meta)
	}
}

func logProjectSnapshot(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(trimmed), &snapshot); err != nil {
		return nil
	}
	return snapshot
}

func logSnapshotValue(snapshot map[string]any, key string) any {
	if len(snapshot) == 0 {
		return nil
	}
	return snapshot[key]
}

func logSnapshotText(snapshot map[string]any, key string) string {
	value, ok := logSnapshotValue(snapshot, key).(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// logSnapshotID 只接受正整数 ID；账户类快照的 ID 既可能是 JSON 数字，
// 也可能是字符串（旧客户端 / 手写数据），两种都认。
func logSnapshotID(snapshot map[string]any, key string) string {
	switch value := logSnapshotValue(snapshot, key).(type) {
	case float64:
		if value <= 0 || float64(int64(value)) != value {
			return ""
		}
		return strconv.FormatInt(int64(value), 10)
	case int:
		if value <= 0 {
			return ""
		}
		return strconv.Itoa(value)
	case int32:
		if value <= 0 {
			return ""
		}
		return strconv.FormatInt(int64(value), 10)
	case int64:
		if value <= 0 {
			return ""
		}
		return strconv.FormatInt(value, 10)
	case json.Number:
		id, err := value.Int64()
		if err != nil || id <= 0 {
			return ""
		}
		return strconv.FormatInt(id, 10)
	case string:
		trimmed := strings.TrimSpace(value)
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || id <= 0 {
			return ""
		}
		return strconv.FormatInt(id, 10)
	}
	return ""
}

func logSnapshotNestedText(snapshot map[string]any, outer string, inner string) string {
	nested, ok := logSnapshotValue(snapshot, outer).(map[string]any)
	if !ok {
		return ""
	}
	return logSnapshotText(nested, inner)
}

// logDescriptionField 从说明里的 `key=value` 取一个值（与 logRequestID 同一套扫描方式）。
func logDescriptionField(description string, key string) string {
	prefix := key + "="
	for _, field := range strings.Fields(description) {
		if value, ok := strings.CutPrefix(field, prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// logDescriptionHashNumber 取说明里第一个 `#数字`（账单可见性审计形如
// 「… 的账单 #42 流水展示：true → false」）。
func logDescriptionHashNumber(description string) string {
	index := strings.Index(description, "#")
	if index < 0 {
		return ""
	}
	rest := description[index+1:]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return rest[:end]
}

var logProjectUserIDPattern = regexp.MustCompile(`用户\s*[^()\s]*\s*\(ID=(\d+)\)`)

// logDescriptionUserID 只取「用户 …(ID=n)」里的目标用户 ID。
// 余额调整说明里先出现的是操作管理员「管理员 a(ID=1)」，不能拿来当项目。
func logDescriptionUserID(description string) string {
	match := logProjectUserIDPattern.FindStringSubmatch(description)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func logCaptchaProject(correlation string) string {
	label, id := "验证码", correlation
	if rest, ok := strings.CutPrefix(correlation, "remote:"); ok {
		label, id = "远程验证码", rest
	} else if rest, ok := strings.CutPrefix(correlation, "local:"); ok {
		label, id = "本地验证码", rest
	}
	if id = strings.TrimSpace(id); id == "" {
		return ""
	}
	return label + " " + id
}

func logAuthProjectLabel(action int32) string {
	switch action {
	case logActionLogin:
		return "登录"
	case 12:
		return "注册"
	case 13:
		return "退出登录"
	case 14:
		return "密码重置"
	case 15:
		return "手机号修改"
	case 16:
		return "资料修改"
	}
	return ""
}

func firstLogProjectValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
