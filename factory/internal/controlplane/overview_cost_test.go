package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// terminalWorkWithCost drives one Work item to a terminal attempt carrying the
// given reported cost, or no cost at all when costUSD is nil.
func terminalWorkWithCost(t *testing.T, store *Store, key string, costUSD *float64) string {
	t.Helper()
	registerTestRepository(t, store, admissionRepositoryIdentity)
	worker := eligibleWorkerForAdmission(t, store, workerA)
	response, err := admitWorkForPauseTest(t, store, key)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID,
		protocol.ClaimRequest{RequestID: key, LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID,
		protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	complete := protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "Done.", CostUSD: costUSD,
	}
	if costUSD != nil {
		complete.Models = map[string]protocol.ModelUsage{"sonnet": {CostUSD: *costUSD, OutputTokens: 10}}
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, complete); err != nil {
		t.Fatal(err)
	}
	return response.WorkIDs[0]
}

func TestOverviewCostSeparatesUnavailableFromZero(t *testing.T) {
	store := newTestStore(t)
	terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000001", nil)

	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A runtime that reported nothing must leave no total behind. Reporting
	// $0.00 here would tell an operator the day was free.
	if overview.Cost.TotalUSD != nil {
		t.Fatalf("total = %v, want nil when nothing reported", *overview.Cost.TotalUSD)
	}
	if overview.Cost.UnavailableWork != 1 || overview.Cost.MeasuredWork != 0 {
		t.Fatalf("cost = %+v, want one unavailable Work item", overview.Cost)
	}
	if overview.Cost.AverageUSD != nil || overview.Cost.HighestUSD != nil {
		t.Fatalf("derived figures appeared without any measurement: %+v", overview.Cost)
	}
}

func TestOverviewCostReportsMeasuredSpendAndItsBlindSpot(t *testing.T) {
	store := newTestStore(t)
	cheap, expensive := 0.12, 0.44
	terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000002", &cheap)
	highest := terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000003", &expensive)
	terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000004", nil)

	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cost := overview.Cost
	if cost.TotalUSD == nil || *cost.TotalUSD < 0.559 || *cost.TotalUSD > 0.561 {
		t.Fatalf("total = %v, want the sum of the two measured items", cost.TotalUSD)
	}
	if cost.MeasuredWork != 2 || cost.UnavailableWork != 1 {
		t.Fatalf("cost = %+v, want 2 measured and 1 unavailable", cost)
	}
	// The average divides by what was measured, never by everything: including
	// the unmeasured item would understate the real average.
	if cost.AverageUSD == nil || *cost.AverageUSD < 0.279 || *cost.AverageUSD > 0.281 {
		t.Fatalf("average = %v, want the mean of the measured items", cost.AverageUSD)
	}
	if cost.HighestUSD == nil || *cost.HighestUSD != expensive || cost.HighestWorkID != highest {
		t.Fatalf("highest = %+v, want the dearest Work item", cost)
	}
	if len(cost.ByModel) != 1 || cost.ByModel[0].Model != "sonnet" || cost.ByModel[0].Attempts != 2 {
		t.Fatalf("by model = %+v", cost.ByModel)
	}
}

// A measured zero is a fact and must survive as one.
func TestOverviewCostKeepsAMeasuredZero(t *testing.T) {
	store := newTestStore(t)
	free := 0.0
	terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000005", &free)

	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Cost.TotalUSD == nil {
		t.Fatal("a measured zero was discarded as unavailable")
	}
	if *overview.Cost.TotalUSD != 0 || overview.Cost.MeasuredWork != 1 ||
		overview.Cost.UnavailableWork != 0 {
		t.Fatalf("cost = %+v", overview.Cost)
	}
}

// TestOverviewCostCoversEveryWorkItemEverRun is the point of the change: a
// day-scoped total resets before an operator has necessarily looked at it, so
// spend from last week still has to appear.
func TestOverviewCostCoversEveryWorkItemEverRun(t *testing.T) {
	store := newTestStore(t)
	old, recent := 1.50, 0.25
	aged := terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000010", &old)
	terminalWorkWithCost(t, store, "cost-00000000-0000-4000-8000-000000000011", &recent)

	// Age the first Work item and its attempt well past any daily window.
	monthAgo := store.now().AddDate(0, 0, -30).UnixMilli()
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE sessions SET terminal_at = ? WHERE id = ?`, monthAgo, aged); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE attempts SET completed_at = ?
		WHERE execution_id IN (SELECT id FROM executions WHERE session_id = ?)
	`, monthAgo, aged); err != nil {
		t.Fatal(err)
	}

	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cost := overview.Cost
	if cost.TotalUSD == nil || *cost.TotalUSD < 1.749 || *cost.TotalUSD > 1.751 {
		t.Fatalf("total = %v, want month-old spend counted too", cost.TotalUSD)
	}
	if cost.MeasuredWork != 2 {
		t.Fatalf("measured work = %d, want both items", cost.MeasuredWork)
	}
	// The dearest Work is the month-old one, so the lifetime figures are not
	// quietly scoped to a recent window either.
	if cost.HighestUSD == nil || *cost.HighestUSD != old || cost.HighestWorkID != aged {
		t.Fatalf("dearest Work = %+v, want the aged item", cost)
	}
	// The trailing window is the one figure that does exclude it.
	if cost.RecentUSD == nil || *cost.RecentUSD < 0.249 || *cost.RecentUSD > 0.251 {
		t.Fatalf("recent = %v, want only the fresh item", cost.RecentUSD)
	}
	if cost.RecentDays != overviewRecentCostDays {
		t.Fatalf("recent days = %d, want %d", cost.RecentDays, overviewRecentCostDays)
	}
}

// Work that never reached an Attempt still counts against the blind spot,
// rather than vanishing from the denominator.
func TestOverviewCostCountsWorkThatNeverRan(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	response, err := admitWorkForPauseTest(t, store, "cost-00000000-0000-4000-8000-000000000012")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE sessions SET state = 'cancelled', terminal_at = ? WHERE id = ?`,
		store.now().UnixMilli(), response.WorkIDs[0]); err != nil {
		t.Fatal(err)
	}
	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Cost.UnavailableWork != 1 || overview.Cost.MeasuredWork != 0 {
		t.Fatalf("cost = %+v, want one unmeasured item", overview.Cost)
	}
}

// TestOverviewCostByModelNeverExceedsTheTotal keeps the panel internally
// consistent. Every figure is scoped to terminal Work, so a by-model table
// that counted in-flight attempts too could sum to more than the total printed
// directly above it.
func TestOverviewCostByModelNeverExceedsTheTotal(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	worker := eligibleWorkerForAdmission(t, store, workerA)

	settled := 1.00
	terminalWorkWithCost(t, store, "model-00000000-0000-4000-8000-000000000001", &settled)

	// A second Work item whose attempt reported a large cost but whose Work is
	// still running, so it belongs in neither the total nor the breakdown.
	if _, err := admitWorkForPauseTest(t, store, "model-00000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "model-00000000-0000-4000-8000-000000000003", LeaseToken: tokenB,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID,
		protocol.StartAttemptRequest{LeaseToken: tokenB}); err != nil {
		t.Fatal(err)
	}
	// Record an expensive attempt directly, leaving its Work non-terminal.
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE attempts SET cost_usd = 9.0, completed_at = ?, models = ?
		WHERE id = ?
	`, store.now().UnixMilli(), `{"opus":{"cost_usd":9.0,"output_tokens":10}}`, claim.Attempt.ID); err != nil {
		t.Fatal(err)
	}

	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cost := overview.Cost
	if cost.TotalUSD == nil {
		t.Fatal("the settled Work reported no total")
	}
	var byModel float64
	for _, entry := range cost.ByModel {
		byModel += entry.CostUSD
	}
	if byModel > *cost.TotalUSD+0.001 {
		t.Fatalf("by-model sums to %v, above the total %v printed above it", byModel, *cost.TotalUSD)
	}
	for _, entry := range cost.ByModel {
		if entry.Model == "opus" {
			t.Fatalf("an in-flight Work item's attempt reached the breakdown: %+v", entry)
		}
	}
}
