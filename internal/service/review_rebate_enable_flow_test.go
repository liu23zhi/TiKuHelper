package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestReviewRebateExistingEnabledActivityIgnoresLegacySwitch(t *testing.T) {
	db := newReviewRebateTestDB(t)
	user := seedReviewRebateUser(t, db, 52, "rebate-existing", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	setReviewRebateSwitch(t, db, false)
	view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil || !view.Program.Enabled || view.Program.ID != program.ID || !GetReviewRebateEnabledWithDB(db) {
		t.Fatalf("existing enabled activity must open without re-saving legacy settings: %+v %v", view, err)
	}
	if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{Enabled: boolPointer(false)}); err != nil {
		t.Fatal(err)
	}
	setReviewRebateSwitch(t, db, true)
	if GetReviewRebateEnabledWithDB(db) {
		t.Fatal("legacy true must not open a disabled activity")
	}
	if _, err := GetReviewRebateProgramViewWithDB(db, user.ID); !errors.Is(err, ErrReviewRebateDisabled) {
		t.Fatalf("disabled activity must close the user view: %v", err)
	}
}

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
			// A stale hidden switch cannot undo an explicitly enabled activity.
			setReviewRebateSwitch(t, db, false)
			title := "仅改活动名称"
			if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{Name: &title}); err != nil {
				t.Fatal(err)
			}
			if current, err := GetReviewRebateProgramWithDB(db); err != nil || current.ID != program.ID {
				t.Fatalf("stale hidden switch closed an enabled activity: %+v %v", current, err)
			}
		})
	}
}
