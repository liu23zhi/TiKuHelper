package controller

import (
	"context"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/diagnostics"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// MODIFIED-only ownership/compatibility guards; not extra passes in main B/M/R.
func TestSearchExecutedTerminalClonesPublicFieldsWithoutCacheHit(t *testing.T) {
	searchFlights.reset()
	defer searchFlights.reset()
	leader, shared := searchFlights.begin("owned-terminal")
	if leader == nil || shared != nil {
		t.Fatal("fresh leader missing")
	}
	public := &model.SearchError{Code: "UPSTREAM_ERROR", Message: "fixed public"}
	warnings := []string{"fixed warning"}
	before := diagnostics.Default.Snapshot().Cache.Search
	finishSearchFlightOutcome("owned-terminal", leader, nil, &searchFlightTerminal{status: "failed", publicError: public, warnings: warnings})
	public.Message = "later owner mutation"
	warnings[0] = "later warning mutation"
	answers, first, err := waitSearchFlightOutcomeWithContext(context.Background(), "owned-terminal", leader, time.Second)
	if err != nil || answers != nil || first == nil || first.publicError.Message != "fixed public" || first.warnings[0] != "fixed warning" {
		t.Fatal("finish exposed mutable owner fields")
	}
	first.publicError.Message = "first borrower mutation"
	first.warnings[0] = "first borrower warning"
	_, second, err := waitSearchFlightOutcomeWithContext(context.Background(), "owned-terminal", leader, time.Second)
	if err != nil || second == nil || second.publicError.Message != "fixed public" || second.warnings[0] != "fixed warning" {
		t.Fatal("borrowers share mutable response fields")
	}
	after := diagnostics.Default.Snapshot().Cache.Search
	if after.Shared != before.Shared || after.Hits != before.Hits || after.Misses != before.Misses {
		t.Fatal("no-answer outcome fabricated usable/cache metrics")
	}
}

func TestSearchExecutedTerminalNilErrorAndFreshExecution(t *testing.T) {
	searchFlights.reset()
	defer searchFlights.reset()
	leader, _ := searchFlights.begin("no-answer")
	finishSearchFlightOutcome("no-answer", leader, nil, &searchFlightTerminal{status: "no_answer"})
	answers, terminal, err := waitSearchFlightOutcomeWithContext(context.Background(), "no-answer", leader, time.Second)
	if err != nil || answers != nil || terminal == nil || terminal.publicError != nil {
		t.Fatal("legitimate executed no-answer lost terminal presence")
	}
	newLeader, _, newTerminal, call, err := beginSearchFlightOutcomeWithContext(context.Background(), "no-answer")
	if err != nil || !newLeader || call == nil || newTerminal != nil {
		t.Fatal("completed outcome became a failure cache or blocked a fresh later execution")
	}
	finishSearchFlight("no-answer", call, nil)
}

func TestSearchUnexecutedFailureKeepsLegacyContinue(t *testing.T) {
	searchFlights.reset()
	defer searchFlights.reset()
	leader, _ := searchFlights.begin("no-usage")
	finishSearchFlight("no-usage", leader, nil)
	answers, terminal, err := waitSearchFlightOutcomeWithContext(context.Background(), "no-usage", leader, time.Second)
	if err != nil || answers != nil || terminal != nil {
		t.Fatal("unexecuted failure was changed into a terminal outcome")
	}
}
