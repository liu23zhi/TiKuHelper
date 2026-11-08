package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
)

func TestFasttrackHiddenSiblingKeywordVisibility(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	visibleKey := seedBillingAccessToken(t, db, owner.ID, "public-key")
	hiddenKey := seedBillingAccessToken(t, db, owner.ID, "private-key-marker")
	records := []entity.BillingRecord{
		{UserID: owner.ID, AccessTokenID: visibleKey.ID, Type: normalSearchBillingRecordType, Provider: "icodef", RequestID: "fixture-keyword-group", Amount: 1.25, QuestionText: "visible-question"},
		{UserID: owner.ID, AccessTokenID: hiddenKey.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "fixture-keyword-group", Amount: 0.75, HiddenFromStatement: true, QuestionText: "private-question-marker", Remark: "private-remark-marker"},
	}
	for i := range records {
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, word := range []string{"private-question-marker", "private-remark-marker", "private-key-marker"} {
		t.Run(word, func(t *testing.T) {
			own, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: word}, 1, 10)
			if err != nil {
				t.Fatal(err)
			}
			if own.Total != 0 || len(own.Data) != 0 || own.Summary.TotalAmount != 0 {
				t.Errorf("hidden-only keyword influenced personal results: rows=%d total=%d amount=%v", len(own.Data), own.Total, own.Summary.TotalAmount)
			}
			all, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{Keyword: word}, 1, 10)
			if err != nil {
				t.Fatal(err)
			}
			if all.Total != 1 || len(all.Data) != 1 || all.Data[0].Amount != 2 {
				t.Errorf("admin visibility/aggregation changed: %+v", all)
			}
		})
	}
	own, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "visible-question"}, 1, 10)
	if err != nil || own.Total != 1 || len(own.Data) != 1 || own.Data[0].Amount != 1.25 {
		t.Fatalf("visible positive control changed: %+v %v", own, err)
	}
}
