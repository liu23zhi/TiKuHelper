package service

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 工单详情必须带退款摘要。
//
// 背景（2026-11-18 重构）：退款工单的「提交 → 审核 → 打款」操作入口从
// 独立面板（TicketRefundPanel.vue）并入了工单详情弹窗 —— 前端只保留一个列表。
// 详情不带退款摘要，管理员点开退款单就看不到金额与状态，也无从操作。
//
// 结论前置：这个字段不是"多返回一点数据"，而是**操作入口的唯一数据来源**。
// 它一旦缺失，前端退款区只能整个隐藏，功能等于消失。

func TestTicketDetailCarriesRefundSummary(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	refund, err := CreateTicketRefundWithDB(db, user, CreateTicketRefundInput{
		Kind:           entity.TicketRefundKindManual,
		RequestedCents: 1200,
		Reason:         "多充了",
	}, nil)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}
	if refund.TicketID == nil {
		t.Fatal("退款行必须挂到一张工单上")
	}

	detail, err := GetTicketWithDB(db, &user, *refund.TicketID)
	if err != nil {
		t.Fatalf("get ticket detail: %v", err)
	}
	if detail.Refund == nil {
		t.Fatal("退款工单的详情必须带 Refund 摘要，否则前端退款区无数据可渲染")
	}
	// 字面量期望值：与列表用的 TicketRefundSummary 同源，但这里刻意写字面量，
	// 这样字段名/取值漂移时必红（不跟着实现一起漂）。
	if detail.Refund.RefundID != refund.ID {
		t.Fatalf("Refund.RefundID want %d got %d", refund.ID, detail.Refund.RefundID)
	}
	if detail.Refund.RequestedCents != 1200 {
		t.Fatalf("Refund.RequestedCents want 1200 got %d", detail.Refund.RequestedCents)
	}
	if detail.Refund.Kind != "manual" {
		t.Fatalf("Refund.Kind want %q got %q", "manual", detail.Refund.Kind)
	}
	if detail.Refund.Status == "" {
		t.Fatal("Refund.Status 不能为空 —— 前端据此决定显示哪些操作按钮")
	}
}

// 普通工单不能凭空多出一个退款摘要（否则前端会把每张单都渲染成退款单）。
func TestTicketDetailOmitsRefundForGeneralTicket(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	user := seedRefundActor(t, db, 1, "alice")

	detail, err := CreateTicketWithDB(db, &user, CreateTicketInput{
		Subject: "登录不上", Body: "点登录没反应",
	})
	if err != nil {
		t.Fatalf("create general ticket: %v", err)
	}
	got, err := GetTicketWithDB(db, &user, detail.Ticket.ID)
	if err != nil {
		t.Fatalf("get ticket detail: %v", err)
	}
	if got.Refund != nil {
		t.Fatalf("普通工单不应带 Refund，实际 %+v", got.Refund)
	}
}

// 管理员看别人的退款单，详情同样要带摘要（审核入口）。
func TestTicketDetailRefundVisibleToAdmin(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	owner := seedRefundActor(t, db, 1, "alice")
	// 角色是普通字符串 "admin"（entity 里没有 UserRoleAdmin 常量）。
	admin := entity.User{ID: 2, Username: "root", Role: "admin", Status: "enabled"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	refund, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind:           entity.TicketRefundKindManual,
		RequestedCents: 500,
		Reason:         "误充",
	}, nil)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}

	detail, err := GetTicketWithDB(db, &admin, *refund.TicketID)
	if err != nil {
		t.Fatalf("admin get detail: %v", err)
	}
	if detail.Refund == nil {
		t.Fatal("管理员查看退款单详情必须带 Refund（否则没有审核入口）")
	}
	if detail.Refund.RequestedCents != 500 {
		t.Fatalf("RequestedCents want 500 got %d", detail.Refund.RequestedCents)
	}
}
