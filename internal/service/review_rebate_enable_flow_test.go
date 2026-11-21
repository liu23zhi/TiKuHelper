package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestReviewRebateSavingEnabledActivityOpensUserEntry(t *testing.T) {
	for _, create := range []bool{true, false} {
		name := "update"
		if create {
			name = "create"
		}
		t.Run(name, func(t *testing.T) {
			db := newReviewRebateTestDB(t)
			user := seedReviewRebateUser(t, db, 51, "rebate-opening", "user")
			setReviewRebateSwitch(t, db, false)
			program := entity.ReviewRebateProgram{Name: "开放活动", Enabled: true, MaxClaimsPerUser: 3, MaxAmountPerUser: 5.04, MinAgentConfidence: 0.6}
			var err error
			if create {
				program, err = CreateReviewRebateProgramWithDB(db, program)
			} else {
				program = seedReviewRebateProgram(t, db, false, 3, 5.04, 0.6)
				enabled := true
				program, err = UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{Enabled: &enabled})
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
			if err != nil || !view.Program.Enabled || view.Program.ID != program.ID {
				t.Fatalf("saving enabled activity must open the real user view: %+v %v", view, err)
			}
			// An explicit global shutdown still takes effect until an administrator
			// deliberately enables the activity again; unrelated edits cannot reopen it.
			setReviewRebateSwitch(t, db, false)
			title := "仅改活动名称"
			if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{Name: &title}); err != nil {
				t.Fatal(err)
			}
			if _, err := GetReviewRebateProgramWithDB(db); !errors.Is(err, ErrReviewRebateDisabled) {
				t.Fatalf("unrelated edit bypassed shutdown: %v", err)
			}
		})
	}
}
