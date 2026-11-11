package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// TestTicketDefaultPriorityByCategory 钉住 6 类工单各自的默认 priority（69-*.md §2.3）。
//
// 这条是「按类别给默认档」的唯一判据：改坏映射（例如把 blacklist_appeal 从 high
// 降成 normal）必须变红，否则线上「被封用户排到最后」不会有任何测试发现。
func TestTicketDefaultPriorityByCategory(t *testing.T) {
	want := map[string]string{
		entity.TicketCategoryGeneral:           entity.TicketPriorityNormal,
		entity.TicketCategoryRegistrationBonus: entity.TicketPriorityHigh,
		entity.TicketCategoryBlacklistAppeal:   entity.TicketPriorityHigh,
		entity.TicketCategoryAccountRebind:     entity.TicketPriorityNormal,
		entity.TicketCategoryAccountClosure:    entity.TicketPriorityNormal,
		entity.TicketCategoryRefund:            entity.TicketPriorityNormal,
	}
	if len(want) != 6 {
		t.Fatalf("6 类工单必须都有默认 priority，实际 %d 类", len(want))
	}
	for category, expected := range want {
		if got := defaultTicketPriorityForCategory(category); got != expected {
			t.Fatalf("category=%s 默认 priority 期望 %s，实际 %s", category, expected, got)
		}
	}
	// 未知分类不能静默升级成 high（那会让脏数据插队）。
	if got := defaultTicketPriorityForCategory("unknown_category"); got != entity.TicketPriorityNormal {
		t.Fatalf("未知分类应退化为 normal，实际 %s", got)
	}
	// 大小写/空格不应影响映射。
	if got := defaultTicketPriorityForCategory(" Registration_Bonus "); got != entity.TicketPriorityHigh {
		t.Fatalf("分类应大小写无关，实际 %s", got)
	}
}

// TestTicketPriorityWhitelistAndUrgentGate 验证四档白名单与「urgent 仅管理员可设」。
func TestTicketPriorityWhitelistAndUrgentGate(t *testing.T) {
	for _, value := range []string{"low", "normal", "high", " HIGH ", "Low"} {
		normalized, err := normalizeTicketPriority(value, false)
		if err != nil {
			t.Fatalf("priority %q 应被放行，得到 %v", value, err)
		}
		if normalized != strings.ToLower(strings.TrimSpace(value)) {
			t.Fatalf("priority %q 归一化错误：%q", value, normalized)
		}
	}
	// 空值 → normal（既有行为不能变）。
	if normalized, err := normalizeTicketPriority("  ", false); err != nil || normalized != entity.TicketPriorityNormal {
		t.Fatalf("空 priority 应回落到 normal，得到 %q/%v", normalized, err)
	}
	// 用户/系统建单（allowUrgent=false）不能设 urgent。
	if _, err := normalizeTicketPriority(entity.TicketPriorityUrgent, false); !errors.Is(err, ErrTicketInvalidPriority) {
		t.Fatalf("非管理员设 urgent 必须被拒，得到 %v", err)
	}
	if normalized, err := normalizeTicketPriority(entity.TicketPriorityUrgent, true); err != nil || normalized != entity.TicketPriorityUrgent {
		t.Fatalf("管理员应可设 urgent，得到 %q/%v", normalized, err)
	}
	// 白名单之外一律报错，不做静默降级。
	for _, value := range []string{"critical", "p0", "1", "normal_high"} {
		if _, err := normalizeTicketPriority(value, true); !errors.Is(err, ErrTicketInvalidPriority) {
			t.Fatalf("非法 priority %q 必须报错，得到 %v", value, err)
		}
	}

	// 端到端：管理员把工单设成 urgent 必须真的落库。
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin"}
	for _, account := range []*entity.User{user, admin} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "需要加速", Body: "请尽快处理"})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Ticket.Priority != entity.TicketPriorityNormal {
		t.Fatalf("用户建单 priority 必须是 normal，实际 %s", detail.Ticket.Priority)
	}
	if _, err := UpdateTicketWithDB(db, user, detail.Ticket.ID, UpdateTicketInput{Priority: entity.TicketPriorityUrgent}); !errors.Is(err, ErrTicketForbidden) {
		t.Fatalf("非管理员更新 priority 必须被拒，得到 %v", err)
	}
	updated, err := UpdateTicketWithDB(db, admin, detail.Ticket.ID, UpdateTicketInput{Priority: entity.TicketPriorityUrgent})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Ticket.Priority != entity.TicketPriorityUrgent {
		t.Fatalf("管理员应能把 priority 设为 urgent，实际 %s", updated.Ticket.Priority)
	}
}

// TestTicketCategoryCannotBeChosenByUser 是「category 不得由用户自选」的钉子。
//
// 攻击形态：用户在建单请求体里多传 category=registration_bonus（可一键补发赠金的类别）、
// origin=system（想让自己不可见）、priority=urgent。这些字段必须被反序列化阶段丢弃，
// 落库只能是 user / general / normal。
//
// ⚠️ 如果哪天给 CreateTicketInput 加上 Category 并原样透传，本用例必红。
func TestTicketCategoryCannotBeChosenByUser(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Nickname: "Alice", Role: "user"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	var input CreateTicketInput
	payload := `{"subject":"请给我补发赠金","body":"补发一下","category":"registration_bonus","origin":"system","priority":"urgent"}`
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		t.Fatalf("请求体反序列化失败：%v", err)
	}
	detail, err := CreateTicketWithDB(db, user, input)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Ticket.Origin != entity.TicketOriginUser {
		t.Fatalf("用户建单 origin 必须是 user，实际 %q", detail.Ticket.Origin)
	}
	if detail.Ticket.Category != entity.TicketCategoryGeneral {
		t.Fatalf("用户建单 category 必须是 general（不可自选），实际 %q", detail.Ticket.Category)
	}
	if detail.Ticket.Priority != entity.TicketPriorityNormal {
		t.Fatalf("用户建单 priority 必须是 normal，实际 %q", detail.Ticket.Priority)
	}
	// 落库值也要核（防止响应体与数据库两套口径）。
	var stored entity.Ticket
	if err := db.Where("id = ?", detail.Ticket.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Origin != entity.TicketOriginUser || stored.Category != entity.TicketCategoryGeneral ||
		stored.Priority != entity.TicketPriorityNormal {
		t.Fatalf("落库值必须是 user/general/normal，实际 %s/%s/%s",
			stored.Origin, stored.Category, stored.Priority)
	}
}

// TestCreateSystemTicketRecordsAffectedUserAndDefaults 验证系统建单的记录口径：
// UserID 记**被涉及用户**（不是 0）、AuthorRole=system、按 category 取默认 priority、
// Context 落库前脱敏。
func TestCreateSystemTicketRecordsAffectedUserAndDefaults(t *testing.T) {
	db := newTicketTestDB(t)
	affected := &entity.User{ID: 7, Username: "alice", Nickname: "Alice", Role: "user"}
	if err := db.Create(affected).Error; err != nil {
		t.Fatal(err)
	}
	result, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID:   7,
		Subject:  "注册赠金判定失败（自动建单）",
		Body:     "注册成功但赠金判定连续失败 3 次，请人工核对后补发。",
		Category: entity.TicketCategoryRegistrationBonus,
		Context: map[string]interface{}{
			"retryCount":     3,
			"failureReason":  "uapi timeout",
			"firstFailureAt": "2026-11-10 10:00:00",
			"lastFailureAt":  "2026-11-10 10:05:00",
			"phone":          "13800001234",
			"sms_code":       "123456",
			"token":          "super-secret-token",
			"password":       "p@ssw0rd",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("首次系统建单必须 Created=true")
	}
	ticket := result.Ticket.Ticket
	if ticket.Origin != entity.TicketOriginSystem {
		t.Fatalf("系统单 origin 必须是 system，实际 %q", ticket.Origin)
	}
	if ticket.Category != entity.TicketCategoryRegistrationBonus {
		t.Fatalf("系统单 category 必须是调用方指定的类别，实际 %q", ticket.Category)
	}
	if ticket.Priority != entity.TicketPriorityHigh {
		t.Fatalf("registration_bonus 默认 priority 必须是 high，实际 %q", ticket.Priority)
	}
	// 最关键的一条：UserID 必须是被涉及用户，不能是 0 —— 0 会让「按用户查工单」失效。
	if ticket.UserID != 7 {
		t.Fatalf("系统单 UserID 必须记被涉及用户 7，实际 %d", ticket.UserID)
	}
	if ticket.UserName != "Alice" {
		t.Fatalf("系统单 UserName 应从 users 表补齐，实际 %q", ticket.UserName)
	}
	if len(result.Ticket.Messages) != 1 {
		t.Fatalf("系统建单必须写首条消息，实际 %d 条", len(result.Ticket.Messages))
	}
	message := result.Ticket.Messages[0]
	if message.AuthorRole != entity.TicketAuthorRoleSystem {
		t.Fatalf("系统消息 AuthorRole 必须是 system，实际 %q", message.AuthorRole)
	}
	if message.UserID != 7 {
		t.Fatalf("系统消息 UserID 也记被涉及用户，实际 %d", message.UserID)
	}

	// Context 红线：不存凭据 —— 手机号只留后四位，验证码/token/password 一律不落库。
	if ticket.Context == "" {
		t.Fatal("系统单必须落 Context 快照")
	}
	if !strings.Contains(ticket.Context, "****1234") {
		t.Fatalf("手机号必须只保留后四位，实际 Context=%s", ticket.Context)
	}
	for _, forbidden := range []string{"13800001234", "123456", "super-secret-token", "p@ssw0rd"} {
		if strings.Contains(ticket.Context, forbidden) {
			t.Fatalf("Context 不得存凭据，发现 %q：%s", forbidden, ticket.Context)
		}
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(ticket.Context), &snapshot); err != nil {
		t.Fatalf("Context 必须是合法 JSON：%v", err)
	}
	// 非敏感字段必须原样保留（否则快照就没用了）。
	if snapshot["failureReason"] != "uapi timeout" {
		t.Fatalf("Context 应保留失败原因，实际 %v", snapshot["failureReason"])
	}
	if retryCount, ok := snapshot["retryCount"].(float64); !ok || retryCount != 3 {
		t.Fatalf("Context 应保留重试次数，实际 %v", snapshot["retryCount"])
	}
}

// TestCreateSystemTicketIdempotentPerCategory 钉住「同一用户同 category 只允许一张
// status != closed 的单」的幂等判据。
func TestCreateSystemTicketIdempotentPerCategory(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 5, Username: "bob", Role: "user"}
	admin := &entity.User{ID: 6, Username: "admin", Role: "admin"}
	for _, account := range []*entity.User{user, admin} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	input := CreateSystemTicketInput{
		UserID:   5,
		Subject:  "注册赠金判定失败（自动建单）",
		Body:     "请人工补发。",
		Category: entity.TicketCategoryRegistrationBonus,
	}
	first, err := CreateSystemTicketWithDB(db, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("首次必须 Created=true")
	}
	// 幂等：重复调用（后台重试场景）不得再建单。
	second, err := CreateSystemTicketWithDB(db, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Fatal("同一用户同 category 未关闭时重复调用必须命中幂等")
	}
	if second.Ticket.Ticket.ID != first.Ticket.Ticket.ID {
		t.Fatalf("幂等应返回既有工单，期望 #%d 实际 #%d", first.Ticket.Ticket.ID, second.Ticket.Ticket.ID)
	}
	if count := countTickets(t, db, "user_id = 5"); count != 1 {
		t.Fatalf("幂等后应只有 1 张单，实际 %d", count)
	}
	// 幂等键只按 category 分桶：同一用户的另一个类别必须能单独建单。
	closureInput := input
	closureInput.Category = entity.TicketCategoryAccountClosure
	closureInput.Subject = "账号注销申请（自动建单）"
	closure, err := CreateSystemTicketWithDB(db, closureInput)
	if err != nil {
		t.Fatal(err)
	}
	if !closure.Created {
		t.Fatal("不同 category 必须各自成单")
	}
	if count := countTickets(t, db, "user_id = 5"); count != 2 {
		t.Fatalf("两个类别应各一张，实际 %d", count)
	}
	// 关闭后同一 category 可以再建一张（幂等条件是 status != closed）。
	if _, err := UpdateTicketWithDB(db, admin, first.Ticket.Ticket.ID, UpdateTicketInput{Status: entity.TicketStatusClosed}); err != nil {
		t.Fatal(err)
	}
	third, err := CreateSystemTicketWithDB(db, input)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Created {
		t.Fatal("上一张已 closed 时应允许新建")
	}
	if third.Ticket.Ticket.ID == first.Ticket.Ticket.ID {
		t.Fatal("新建的工单不能复用已关闭的那张")
	}
	if count := countTickets(t, db, "user_id = 5 AND status <> 'closed'"); count != 2 {
		t.Fatalf("未关闭的系统单应为 2 张（另一个类别 + 新建），实际 %d", count)
	}
}

// TestCreateSystemTicketRejectsInvalidInput 验证系统建单的入参闸门。
func TestCreateSystemTicketRejectsInvalidInput(t *testing.T) {
	db := newTicketTestDB(t)
	// UserID=0 必须被拒：它会让「按用户查工单」失效，而「用户不可见」应由 origin 判定。
	if _, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 0, Subject: "x", Body: "y", Category: entity.TicketCategoryRegistrationBonus,
	}); !errors.Is(err, ErrTicketSystemUserRequired) {
		t.Fatalf("UserID=0 必须被拒，得到 %v", err)
	}
	if _, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 1, Subject: "x", Body: "y", Category: "not_a_category",
	}); !errors.Is(err, ErrTicketInvalidCategory) {
		t.Fatalf("非法 category 必须被拒，得到 %v", err)
	}
	// 系统建单缺 category：不能静默落到 general（general 是用户自由提问的兜底，
	// 系统单挂 general 会让按 category 的一键动作无从判断）。
	if _, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 1, Subject: "x", Body: "y",
	}); !errors.Is(err, ErrTicketInvalidCategory) {
		t.Fatalf("系统建单缺 category 必须被拒，得到 %v", err)
	}
	// 系统建单不得自行升到 urgent。
	if _, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 1, Subject: "x", Body: "y",
		Category: entity.TicketCategoryRegistrationBonus, Priority: entity.TicketPriorityUrgent,
	}); !errors.Is(err, ErrTicketInvalidPriority) {
		t.Fatalf("系统建单不得设 urgent，得到 %v", err)
	}
}

// TestTicketSystemTicketVisibleToAffectedUserAndIsolated 钉住 Q69-2 定案
// （45-*.md §0.23.2：**被涉及用户可见**，隔离只靠 user_id）：
//
//	A. 被涉及用户能看到发给自己的系统单（列表 / 分类筛选 / 详情 / 回复）；
//	B. 但看不到**别人**的系统单 —— 去掉 origin 过滤后这是唯一的隔离护栏；
//	C. 用户不能改系统单的 category / priority（那些是系统定的）。
//
// ⚠️ B 不能少：只写 A 的话，把 user_id 条件弄丢也照样全绿。
func TestTicketSystemTicketVisibleToAffectedUserAndIsolated(t *testing.T) {
	db := newTicketTestDB(t)
	owner := &entity.User{ID: 3, Username: "carol", Role: "user"}
	other := &entity.User{ID: 4, Username: "dave", Role: "user"}
	admin := &entity.User{ID: 9, Username: "admin", Role: "admin"}
	for _, account := range []*entity.User{owner, other, admin} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	systemResult, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 3, Subject: "注册赠金判定失败（自动建单）", Body: "请人工核对后补发。",
		Category: entity.TicketCategoryRegistrationBonus,
	})
	if err != nil {
		t.Fatal(err)
	}
	systemTicketID := systemResult.Ticket.Ticket.ID

	// ---- A：被涉及用户可见（这就是 Q69-2 推翻「保密」的那一条）----
	ownList, err := ListTicketsWithDB(db, owner, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if ownList.Total != 1 || len(ownList.Items) != 1 {
		t.Fatalf("被涉及用户必须能在列表里看到发给自己的系统单，实际 total=%d items=%+v", ownList.Total, ownList.Items)
	}
	if ownList.Items[0].ID != systemTicketID ||
		ownList.Items[0].Origin != entity.TicketOriginSystem ||
		ownList.Items[0].Category != entity.TicketCategoryRegistrationBonus ||
		ownList.Items[0].Priority != entity.TicketPriorityHigh {
		t.Fatalf("列表项必须是那张系统单且带 origin/category/priority，实际 %+v", ownList.Items[0])
	}
	// 按分类筛选也要能筛出来（前端分类筛选对同一份数据生效）。
	if filtered, err := ListTicketsWithDB(db, owner, TicketListQuery{Category: entity.TicketCategoryRegistrationBonus}, 1, 20); err != nil || filtered.Total != 1 {
		t.Fatalf("按 category 筛选应命中自己的系统单，total=%d err=%v", filtered.Total, err)
	}
	ownDetail, err := GetTicketWithDB(db, owner, systemTicketID)
	if err != nil {
		t.Fatalf("被涉及用户必须能读系统单详情，得到 %v", err)
	}
	if ownDetail.Ticket.Origin != entity.TicketOriginSystem || len(ownDetail.Messages) != 1 {
		t.Fatalf("详情必须是系统单且含首条系统消息，实际 origin=%q messages=%d", ownDetail.Ticket.Origin, len(ownDetail.Messages))
	}
	// 可见 ≠ 只读：用户回复系统单必须成功（45-*.md §0.23.2 的补充结论）。
	if _, err := AddTicketMessageWithDB(db, owner, systemTicketID, ReplyTicketInput{Body: "我的赠金到账了吗？"}); err != nil {
		t.Fatalf("用户回复自己的系统单必须成功，得到 %v", err)
	}

	// ---- B：别人的系统单看不到（隔离只靠 user_id）----
	otherList, err := ListTicketsWithDB(db, other, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if otherList.Total != 0 {
		t.Fatalf("另一个用户不得看到别人的系统单，实际 total=%d items=%+v", otherList.Total, otherList.Items)
	}
	if _, err := GetTicketWithDB(db, other, systemTicketID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("另一个用户读别人的系统单必须 not found，得到 %v", err)
	}
	if _, err := AddTicketMessageWithDB(db, other, systemTicketID, ReplyTicketInput{Body: "插一句"}); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("另一个用户回复别人的系统单必须 not found，得到 %v", err)
	}
	// 别人自己的单照常可见（隔离不能误伤）。
	if _, err := CreateTicketWithDB(db, other, CreateTicketInput{Subject: "我的问题", Body: "登录失败"}); err != nil {
		t.Fatal(err)
	}
	if otherList, err = ListTicketsWithDB(db, other, TicketListQuery{}, 1, 20); err != nil || otherList.Total != 1 {
		t.Fatalf("另一个用户应只看到自己那张单，total=%d err=%v", otherList.Total, err)
	}
	// 管理员仍然全量可见，并能按 origin 精确筛。
	adminList, err := ListTicketsWithDB(db, admin, TicketListQuery{Origin: entity.TicketOriginSystem}, 1, 20)
	if err != nil || adminList.Total != 1 {
		t.Fatalf("管理员按 origin=system 应命中 1 张，total=%d err=%v", adminList.Total, err)
	}

	// ---- C：用户不能改系统单的 category / priority ----
	// category 根本没有更新入口（UpdateTicketInput 只有 Status / Priority），
	// priority 只有管理员能改；这里两条都核，并回读落库值确认没被偷改。
	if _, err := UpdateTicketWithDB(db, owner, systemTicketID, UpdateTicketInput{Priority: entity.TicketPriorityLow}); !errors.Is(err, ErrTicketForbidden) {
		t.Fatalf("用户改系统单 priority 必须被拒，得到 %v", err)
	}
	if _, err := UpdateTicketWithDB(db, owner, systemTicketID, UpdateTicketInput{Priority: entity.TicketPriorityUrgent}); !errors.Is(err, ErrTicketForbidden) {
		t.Fatalf("用户把系统单升到 urgent 必须被拒，得到 %v", err)
	}
	var stored entity.Ticket
	if err := db.Where("id = ?", systemTicketID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Priority != entity.TicketPriorityHigh || stored.Category != entity.TicketCategoryRegistrationBonus ||
		stored.Origin != entity.TicketOriginSystem {
		t.Fatalf("系统单三维度不得被用户改动，实际 origin=%q category=%q priority=%q",
			stored.Origin, stored.Category, stored.Priority)
	}
}

// TestTicketAttachmentPathsFollowUserIsolation 把同一条隔离规则钉到附件路径上：
// 系统单可见 ⇒ 被涉及用户能走附件路径；隔离只靠 user_id ⇒ 别人走不通。
func TestTicketAttachmentPathsFollowUserIsolation(t *testing.T) {
	db := newTicketTestDB(t)
	owner := &entity.User{ID: 4, Username: "dave", Role: "user"}
	other := &entity.User{ID: 8, Username: "erin", Role: "user"}
	admin := &entity.User{ID: 10, Username: "admin", Role: "admin"}
	for _, account := range []*entity.User{owner, other, admin} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	systemResult, err := CreateSystemTicketWithDB(db, CreateSystemTicketInput{
		UserID: 4, Subject: "注册赠金判定失败（自动建单）", Body: "请人工补发。",
		Category: entity.TicketCategoryRegistrationBonus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketForAttachmentWithDB(db, owner, systemResult.Ticket.Ticket.ID); err != nil {
		t.Fatalf("被涉及用户必须能通过附件路径访问自己的系统单，得到 %v", err)
	}
	if _, err := ticketForAttachmentWithDB(db, other, systemResult.Ticket.Ticket.ID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("另一个用户不得通过附件路径访问别人的系统单，得到 %v", err)
	}
	if _, err := ticketForAttachmentWithDB(db, admin, systemResult.Ticket.Ticket.ID); err != nil {
		t.Fatalf("管理员必须能通过附件路径访问系统单，得到 %v", err)
	}
	// 用户自己的普通单照常（闸门不能误伤正常上传）。
	own, err := CreateTicketWithDB(db, owner, CreateTicketInput{Subject: "我的问题", Body: "登录失败"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketForAttachmentWithDB(db, owner, own.Ticket.ID); err != nil {
		t.Fatalf("用户访问自己的工单附件路径必须正常，得到 %v", err)
	}
}

// TestTicketListFiltersRejectInvalidOriginAndCategory 验证筛选参数不做静默降级。
func TestTicketListFiltersRejectInvalidOriginAndCategory(t *testing.T) {
	db := newTicketTestDB(t)
	admin := &entity.User{ID: 1, Username: "admin", Role: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ListTicketsWithDB(db, admin, TicketListQuery{Origin: "root"}, 1, 20); !errors.Is(err, ErrTicketInvalidOrigin) {
		t.Fatalf("非法 origin 必须报错，得到 %v", err)
	}
	if _, err := ListTicketsWithDB(db, admin, TicketListQuery{Category: "bogus"}, 1, 20); !errors.Is(err, ErrTicketInvalidCategory) {
		t.Fatalf("非法 category 必须报错，得到 %v", err)
	}
	// 合法值（含大小写混写）必须放行。
	if _, err := ListTicketsWithDB(db, admin, TicketListQuery{Origin: "SYSTEM", Category: "Registration_Bonus"}, 1, 20); err != nil {
		t.Fatalf("合法筛选值应放行，得到 %v", err)
	}
}

// countTickets 数满足条件的工单数；条件由调用方给（同一用户 / 未关闭等）。
func countTickets(t *testing.T, db *gorm.DB, where string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&entity.Ticket{}).Where(where).Count(&count).Error; err != nil {
		t.Fatalf("统计工单失败（where=%s）：%v", where, err)
	}
	return count
}
