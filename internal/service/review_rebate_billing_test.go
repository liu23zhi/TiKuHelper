package service

import "testing"

func TestReviewRebateClawbackHasChineseLabelAndDoesNotCountAsConsumption(t *testing.T) {
	if label := buildBillingRecordTypeLabel(ReviewRebateClawbackBillingType); label != "评价返现追缴" {
		t.Fatalf("missing label: %s", label)
	}
	rows := []BillingRecordListItem{{Type: ReviewRebateClawbackBillingType, Direction: "expense", Amount: 3}, {Type: normalSearchBillingRecordType, Direction: "expense", Amount: 2}}
	if total := sumBillingExpenseAmount(rows); total != 2 {
		t.Fatalf("clawback counted as consumption: %v", total)
	}
}
