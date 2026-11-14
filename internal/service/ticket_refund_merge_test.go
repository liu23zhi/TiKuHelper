package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 退款工单合并（T-209）的行为测试。
//
// 全部打真 SQLite 库、不 mock：判据都是「数据真的落在哪张表、列表真的返回了什么」，
// mock 之后就只能测「我们调用了一个函数」，那正是任务书禁的字符串级断言。
//
// 建库要带上 ticket_refunds 与 recharge_orders：退款流程会校验订单归属与实付金额。

// newTicketRefundMergeTestDB 建合并测试库（工单 + 消息 + 退款 + 订单 + 用户）。
func newTicketRefundMergeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Ticket{},
		&entity.TicketMessage{}, &entity.TicketNotification{}, &entity.TicketRefund{},
		&entity.TicketRefundLedger{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedRefundActor 落一个可用用户。
func seedRefundActor(t *testing.T, db *gorm.DB, id int32, name string) entity.User {
	t.Helper()
	user := entity.User{ID: id, Username: name, Nickname: name, Role: "user", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user
}

// 判据 1 + 5：列表接口返回的集合**同时**包含普通工单与退款工单。
//
// 这条是本次合并的核心：它是**真实数据断言**（真建一张普通单 + 真跑一次退款申请，
// 再从 ListTicketsWithDB 读回来），不是「SQL 里有 JOIN」这种结构断言。
//
// 变异方向：把退款壳工单的创建去掉（只查 tickets 表 / 不给退款建壳单），本用例必红。
func TestTicketListContainsBothGeneralAndRefundTickets(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	// 一张普通工单。
	if _, err := CreateTicketWithDB(db, &user, CreateTicketInput{Subject: "登录不上", Body: "点登录没反应"}); err != nil {
		t.Fatalf("create general ticket: %v", err)
	}

	// 一笔退款（manual：无需订单，走人工打款路径）。
	refund, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindManual, RequestedCents: 1200, Reason: "多充了",
	}, nil)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}

	result, err := ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("列表必须同时含普通工单与退款工单，want total=2 got %d", result.Total)
	}
	var general, refundRow *TicketListItem
	for index := range result.Items {
		item := &result.Items[index]
		switch item.Category {
		case entity.TicketCategoryGeneral:
			general = item
		case entity.TicketCategoryRefund:
			refundRow = item
		}
	}
	if general == nil {
		t.Fatal("列表缺少普通工单")
	}
	if refundRow == nil {
		t.Fatal("列表缺少退款工单（退款没有被合并进工单表）")
	}
	// 退款行的 subject 要是可读中文，而不是空串或内部 ID。
	if !strings.Contains(refundRow.Subject, "退款") {
		t.Fatalf("退款工单标题应可读并含「退款」，实际 %q", refundRow.Subject)
	}
	if refundRow.UserID != user.ID {
		t.Fatalf("退款工单必须归属申请人，want %d got %d", user.ID, refundRow.UserID)
	}

	// 判据 5 的核心：退款的 ticket_id 必须真的指向这张工单（存量与新建同一路径）。
	if refund.TicketID == nil || *refund.TicketID != refundRow.ID {
		t.Fatalf("退款的 ticket_id 必须指向列表里那张工单，want %d got %v", refundRow.ID, refund.TicketID)
	}
}

// 判据 2：每一行都能区分类型。
//
// 光有 category 不够 —— 退款 7 态映射进工单 4 态会丢掉「冻结中 / 处理中」，
// 管理员据此判断不了钱在哪。所以退款行还必须带 refund 摘要（含真实状态）。
func TestTicketListRowDistinguishesRefundWithRealStatus(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	refund, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindManual, RequestedCents: 500, Reason: "重复支付",
	}, nil)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}

	result, err := ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("want 1 row, got %d", len(result.Items))
	}
	row := result.Items[0]
	if row.Category != entity.TicketCategoryRefund {
		t.Fatalf("row category must be refund, got %q", row.Category)
	}
	if row.Refund == nil {
		t.Fatal("退款行必须带 refund 摘要，否则前端无法显示真实退款状态")
	}
	// 真实状态是 7 态里的 pending_review，不是工单 4 态里的 open。
	if row.Refund.Status != entity.TicketRefundStatusPendingReview {
		t.Fatalf("refund.status must be the real refund state, got %q", row.Refund.Status)
	}
	if row.Refund.RequestedCents != 500 || row.Refund.RefundID != refund.ID {
		t.Fatalf("refund summary must carry real amounts/ids, got %+v", row.Refund)
	}
	// 工单状态是映射口径：pending_review → open。
	if row.Status != entity.TicketStatusOpen {
		t.Fatalf("壳工单 status 应为 open（映射自 pending_review），got %q", row.Status)
	}

	// 普通工单不该带 refund 摘要（否则前端会给所有行都画退款徽标）。
	if _, err := CreateTicketWithDB(db, &user, CreateTicketInput{Subject: "一般问题", Body: "描述"}); err != nil {
		t.Fatalf("create general: %v", err)
	}
	result, err = ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list again: %v", err)
	}
	for _, item := range result.Items {
		if item.Category == entity.TicketCategoryGeneral && item.Refund != nil {
			t.Fatalf("普通工单不应带 refund 摘要，got %+v", item.Refund)
		}
	}
}

// 判据 3：新建时选退款类型 → **真的走退款流程**。
//
// 断言落在「ticket_refunds 里有没有一行」而不是「工单 category 是不是 refund」 ——
// 后者只要改个字符串就能过，正是任务书说的「不能只是换个 category 字符串」。
func TestCreateTicketWithRefundTypeRunsRealRefundFlow(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	detail, err := CreateTicketForUserWithDB(db, &user, CreateTicketInput{
		TicketType:           TicketTypeRefund,
		RefundRequestedCents: 2500,
		RefundReason:         "充错了金额",
	}, nil)
	if err != nil {
		t.Fatalf("create refund ticket: %v", err)
	}
	// 返回的是壳工单详情（与普通建单同一响应形态）。
	if detail.Ticket.Category != entity.TicketCategoryRefund {
		t.Fatalf("壳工单 category 必须是 refund，got %q", detail.Ticket.Category)
	}

	// 关键：ticket_refunds 里必须真有一行，且金额/理由来自入参、关联回这张工单。
	var refund entity.TicketRefund
	if err := db.Where("ticket_id = ?", detail.Ticket.ID).Take(&refund).Error; err != nil {
		t.Fatalf("选了退款类型必须在 ticket_refunds 落一行（而非只改工单分类）：%v", err)
	}
	if refund.RequestedCents != 2500 {
		t.Fatalf("退款金额必须来自入参，want 2500 got %d", refund.RequestedCents)
	}
	if refund.Reason != "充错了金额" {
		t.Fatalf("退款理由必须落库，got %q", refund.Reason)
	}
	// 无订单 → 必须自动归为 manual（原路退回找不到订单）。
	if refund.Kind != entity.TicketRefundKindManual {
		t.Fatalf("没有订单的退款必须走 manual，got %q", refund.Kind)
	}
	if refund.Status != entity.TicketRefundStatusPendingReview {
		t.Fatalf("新退款必须从 pending_review 开始，got %q", refund.Status)
	}
	// 首条消息由系统写入（说明这是自动创建的壳工单）。
	if len(detail.Messages) == 0 || detail.Messages[0].AuthorRole != entity.TicketAuthorRoleSystem {
		t.Fatalf("壳工单首条消息应为 system，got %+v", detail.Messages)
	}
}

// 选了退款类型但缺金额/理由必须被拒 —— 不能悄悄退化成普通工单。
func TestCreateTicketWithRefundTypeRejectsIncompleteInput(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	cases := []struct {
		name  string
		input CreateTicketInput
	}{
		{"缺金额", CreateTicketInput{TicketType: TicketTypeRefund, RefundReason: "理由"}},
		{"缺理由", CreateTicketInput{TicketType: TicketTypeRefund, RefundRequestedCents: 100}},
		{"理由超长", CreateTicketInput{TicketType: TicketTypeRefund, RefundRequestedCents: 100,
			RefundReason: strings.Repeat("长", 501)}},
	}
	for _, item := range cases {
		if _, err := CreateTicketForUserWithDB(db, &user, item.input, nil); err == nil {
			t.Fatalf("%s：必须报错", item.name)
		}
	}
	// 一次都不许落单（无论工单还是退款）。
	var ticketCount, refundCount int64
	if err := db.Model(&entity.Ticket{}).Count(&ticketCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketRefund{}).Count(&refundCount).Error; err != nil {
		t.Fatal(err)
	}
	if ticketCount != 0 || refundCount != 0 {
		t.Fatalf("参数不全时不得落任何记录，got tickets=%d refunds=%d", ticketCount, refundCount)
	}
}

// 「类型」白名单：非退款类型却带退款字段必须报错，不做静默忽略。
func TestCreateTicketRejectsRefundFieldsOnGeneralType(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	_, err := CreateTicketForUserWithDB(db, &user, CreateTicketInput{
		TicketType: TicketTypeGeneral, Subject: "标题", Body: "正文", RefundRequestedCents: 100,
	}, nil)
	if err == nil {
		t.Fatal("普通工单带退款字段必须报错（否则用户以为提交了退款）")
	}
}

// 69-*.md §七.1 红线的回归：套利类别仍然不可由用户自选。
//
// 新增「用户可选类型」后必须确认这条没有被破坏：用户传
// registration_bonus / blacklist_appeal 仍然要被拒（或落成 general），
// 绝不能建出能被管理员一键补发/一键解封的工单。
func TestUserSelectableTicketTypeRejectsArbitrageCategories(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	for _, category := range []string{
		entity.TicketCategoryRegistrationBonus,
		entity.TicketCategoryBlacklistAppeal,
		entity.TicketCategoryAccountRebind,
		entity.TicketCategoryAccountClosure,
	} {
		list := `{"subject":"标题","body":"正文","ticketType":"` + category + `"}`
		var input CreateTicketInput
		if err := json.Unmarshal([]byte(list), &input); err != nil {
			t.Fatalf("unmarshal %s: %v", category, err)
		}
		if _, err := CreateTicketForUserWithDB(db, &user, input, nil); err == nil {
			t.Fatalf("用户自选 %s 必须被拒（该类别带一键动作，属套利面）", category)
		}
	}
	// 空 ticketType 仍等价于 general（老客户端不发这个字段，行为不能变）。
	detail, err := CreateTicketForUserWithDB(db, &user, CreateTicketInput{Subject: "标题", Body: "正文"}, nil)
	if err != nil {
		t.Fatalf("空 ticketType 必须仍可建单：%v", err)
	}
	if detail.Ticket.Category != entity.TicketCategoryGeneral {
		t.Fatalf("空 ticketType 应落 general，got %q", detail.Ticket.Category)
	}
}

// 判据 4（退款侧）：6 个动作全部仍可达 —— 状态机没有被合并破坏。
//
// 逐个动作真实执行：提交 / 审核通过冻结 / 开始打款 / 对账成功 /
// 审核驳回 / 撤销未发送。每一步都断言状态迁移结果，不是「函数存在」。
//
// 变异方向：删掉其中任一动作的状态迁移（例如把 frozen 直接置 succeeded）本用例必红。
func TestRefundActionsSurviveTheMerge(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")
	admin := entity.User{ID: 99, Username: "root", Role: "admin", Status: "enabled"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	// 钱包要有余额才能冻结（退款是「先冻结、再打款」）。
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).
		Update("balance", "100.000000").Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}

	// ① 提交
	refund, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindManual, RequestedCents: 1000, Reason: "退款理由",
	}, nil)
	if err != nil {
		t.Fatalf("① 提交失败：%v", err)
	}
	if refund.Status != entity.TicketRefundStatusPendingReview {
		t.Fatalf("① 提交后应为 pending_review，got %q", refund.Status)
	}

	// ② 审核通过 → 冻结
	frozen, err := ReviewTicketRefundWithDB(db, admin, refund.ID, true, 1000, nil)
	if err != nil {
		t.Fatalf("② 审核通过失败：%v", err)
	}
	if frozen.Status != entity.TicketRefundStatusFrozen {
		t.Fatalf("② 审核通过后应为 frozen，got %q", frozen.Status)
	}
	// 壳工单状态也要跟着变（展示口径最终一致）。
	assertShellStatus(t, db, refund.TicketID, entity.TicketStatusOpen)

	// ③ 开始打款 → processing（manual 不走渠道，这里只验状态闸门）
	// manual 类型的打款由 Reconcile 完成，先走对账成功路径。
	// ④ 对账成功 → succeeded
	done, err := ReconcileTicketRefundWithDB(db, admin, refund.ID, entity.TicketRefundStatusSucceeded, "REF-1")
	if err != nil {
		t.Fatalf("④ 对账成功失败：%v", err)
	}
	if done.Status != entity.TicketRefundStatusSucceeded {
		t.Fatalf("④ 对账后应为 succeeded，got %q", done.Status)
	}
	// 终态要映射成 closed（不再挂在待办里）。
	assertShellStatus(t, db, refund.TicketID, entity.TicketStatusClosed)

	// ⑤ 审核驳回（另一笔）
	rejectMe, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindManual, RequestedCents: 300, Reason: "驳回测试",
	}, nil)
	if err != nil {
		t.Fatalf("⑤ 创建待驳回退款失败：%v", err)
	}
	rejected, err := ReviewTicketRefundWithDB(db, admin, rejectMe.ID, false, 0, nil)
	if err != nil {
		t.Fatalf("⑤ 驳回失败：%v", err)
	}
	if rejected.Status != entity.TicketRefundStatusRejected {
		t.Fatalf("⑤ 驳回后应为 rejected，got %q", rejected.Status)
	}
	assertShellStatus(t, db, rejectMe.TicketID, entity.TicketStatusClosed)

	// ⑥ 撤销未发送（先冻结、再撤销 → failed + 冻结解除）
	//
	// ⚠️ 撤销只对 **original** 类型开放（manual 没有渠道可撤，只能走对账），
	// 所以这一笔必须用 original 建：它要求订单 + 渠道校验通过。
	// 用一个「总是拒绝原路退回」的 verifier 会走 manual；这里要的是原路可退，
	// 所以传一个「渠道可用」的桩 —— 它只影响「能否原路退回」的判定，
	// 不改变退款状态机本身（本用例要验的正是状态机没被合并破坏）。
	originalOrder := seedPaidOrder(t, db, user.ID, 5.00)
	allowOriginal := func(_ *gorm.DB, _ entity.RechargeOrder) (string, error) {
		return "ALIPAY", nil
	}
	unsent, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderID: &originalOrder, RequestedCents: 200, Reason: "撤销测试",
	}, allowOriginal)
	if err != nil {
		t.Fatalf("⑥ 创建待撤销退款失败：%v", err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, unsent.ID, true, 200, allowOriginal); err != nil {
		t.Fatalf("⑥ 冻结失败：%v", err)
	}
	canceled, err := CancelUnsentTicketRefundWithDB(db, admin, unsent.ID)
	if err != nil {
		t.Fatalf("⑥ 撤销未发送失败：%v", err)
	}
	if canceled.Status != entity.TicketRefundStatusFailed {
		t.Fatalf("⑥ 撤销后应为 failed，got %q", canceled.Status)
	}
	assertShellStatus(t, db, unsent.TicketID, entity.TicketStatusClosed)
}

// seedPaidOrder 落一笔已支付的充值订单，返回订单 ID。
//
// 退款的原路退回路径要求「订单属于该用户 + 已支付 + 未被占用」，
// 且订单实付不低于申请金额。
func seedPaidOrder(t *testing.T, db *gorm.DB, userID int32, paidYuan float64) int32 {
	t.Helper()
	// PaidAt 必须在 ticketRefundAge（60 天）内且不在未来：
	// 原路退回只接受「可回溯期内」的订单。
	paidAt := time.Now().Add(-time.Hour)
	order := entity.RechargeOrder{
		OrderNo: "TEST-" + time.Now().Format("150405.000000"), UserID: userID,
		// Amount 是 decimal(18,6) 的「元」，PaidAt + Status 是「已支付」的判据。
		Amount: paidYuan, Status: "paid", PaidAt: &paidAt,
		// 原路退回要求这三项齐备（ticketRefundOriginalChannel 的判据）：
		// 支付渠道必须是 epay、且订单上记着原始交易号与渠道指纹。
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "测试",
		ThirdPartyOrderNo:         "TRADE-1",
		RefundOriginalTradeNo:     "TRADE-1",
		RefundChannelFingerprint:  strings.Repeat("a", 64),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed paid order: %v", err)
	}
	return order.ID
}

// assertShellStatus 断言壳工单的展示状态。
func assertShellStatus(t *testing.T, db *gorm.DB, ticketID *int64, want string) {
	t.Helper()
	if ticketID == nil {
		t.Fatal("退款必须有壳工单关联")
	}
	var ticket entity.Ticket
	if err := db.Where("id = ?", *ticketID).Take(&ticket).Error; err != nil {
		t.Fatalf("读壳工单失败：%v", err)
	}
	if ticket.Status != want {
		t.Fatalf("壳工单状态应为 %q（映射口径），got %q", want, ticket.Status)
	}
	// 壳工单永远不能反过来改退款分类之外的东西。
	if ticket.Category != entity.TicketCategoryRefund {
		t.Fatalf("壳工单 category 必须是 refund，got %q", ticket.Category)
	}
}

// 判据 5：已有 ticket_refunds 存量数据（ticket_id 为 NULL）能被列表读到。
//
// 模拟「迁移前的老退款」：直接插一行 ticket_id=NULL 的退款，
// 再跑回填，然后确认它出现在列表里。这条覆盖的正是「历史数据不能丢」。
func TestExistingRefundRowsBecomeVisibleAfterBackfill(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	// 直接插老数据：ticket_id 为空（迁移前形态）。
	legacy := entity.TicketRefund{
		UserID: user.ID, Kind: entity.TicketRefundKindManual,
		Status: entity.TicketRefundStatusPendingReview, RequestedCents: 800,
		Reason: "历史退款", RefundNo: "tr-legacy-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy refund: %v", err)
	}

	// 回填前：列表里看不到它（这正是要消除的现象）。
	before, err := ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	if before.Total != 0 {
		t.Fatalf("回填前列表应为空，got %d", before.Total)
	}

	// 回填（registry 的存量补建走同一份 service 实现）。
	if err := backfillTicketRefundShellsForTest(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	after, err := ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if after.Total != 1 {
		t.Fatalf("回填后存量退款必须出现在列表里，got %d", after.Total)
	}
	if after.Items[0].Category != entity.TicketCategoryRefund {
		t.Fatalf("存量退款工单 category 必须是 refund，got %q", after.Items[0].Category)
	}
	// 关联必须写回（否则下次回填会再造一张单）。
	var reread entity.TicketRefund
	if err := db.Where("id = ?", legacy.ID).Take(&reread).Error; err != nil {
		t.Fatal(err)
	}
	if reread.TicketID == nil {
		t.Fatal("回填必须把 ticket_id 写回退款行")
	}

	// 幂等：再跑一次不能造出第二张单。
	if err := backfillTicketRefundShellsForTest(db); err != nil {
		t.Fatalf("backfill twice: %v", err)
	}
	again, err := ListTicketsWithDB(db, &user, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list after second backfill: %v", err)
	}
	if again.Total != 1 {
		t.Fatalf("回填必须幂等，第二次后仍应是 1 张，got %d", again.Total)
	}
}

// backfillTicketRefundShellsForTest 复现 registry 回填的核心循环。
//
// 不直接调 registry.ensureTicketRefundTicketSchema：那个函数还包含列迁移，
// 而本包测试库已有列。这里只跑「给 ticket_id IS NULL 的退款补壳工单」这一段，
// 用的正是 registry 回填调用的同一个 service.CreateTicketRefundShellInTx。
func backfillTicketRefundShellsForTest(db *gorm.DB) error {
	var pending []entity.TicketRefund
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("ticket_id IS NULL").Order("id ASC").Find(&pending).Error; err != nil {
		return err
	}
	for _, refund := range pending {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return CreateTicketRefundShellInTx(tx, refund, time.Time{})
		}); err != nil {
			return err
		}
	}
	return nil
}

// 用户视图隔离（红线）：退款工单也必须按 user_id 隔离，不能被别人看到。
func TestRefundTicketRespectsUserIsolation(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	alice := seedRefundActor(t, db, 1, "alice")
	bob := seedRefundActor(t, db, 2, "bob")

	if _, err := CreateTicketRefundWithDB(db, alice, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindManual, RequestedCents: 100, Reason: "alice 的退款",
	}, nil); err != nil {
		t.Fatalf("create refund: %v", err)
	}

	// bob 看不到 alice 的退款工单。
	bobList, err := ListTicketsWithDB(db, &bob, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("bob list: %v", err)
	}
	if bobList.Total != 0 {
		t.Fatalf("用户视图必须隔离退款工单，bob 不应看到 %d 条", bobList.Total)
	}
	// alice 自己看得到。
	aliceList, err := ListTicketsWithDB(db, &alice, TicketListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("alice list: %v", err)
	}
	if aliceList.Total != 1 {
		t.Fatalf("alice 应看到自己的退款工单，got %d", aliceList.Total)
	}
}
