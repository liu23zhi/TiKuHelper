package controller

import "testing"

// 账单模式取值只有 deep / normal 两种，界面据此显示「深度思考 / 普通搜索」。
func TestBillingSearchMode(t *testing.T) {
	if got := billingSearchMode(true); got != "deep" {
		t.Fatalf("expected deep for deep thinking, got %q", got)
	}
	if got := billingSearchMode(false); got != "normal" {
		t.Fatalf("expected normal for ordinary search, got %q", got)
	}
}
