package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Issue #191: a bead with a future defer_until is withheld from every "ready"
// surface (actionable set, claimable top picks, track/label top picks, robot
// reasons) exactly as `br ready` hides it, while an elapsed deferral and an
// absent deferral leave the bead fully claimable.
func TestComputeTriage_FutureDeferUntilWithheldUntilLapsed(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(45 * 24 * time.Hour)
	elapsed := now.Add(-time.Second)
	issues := []model.Issue{
		// Highest priority and would otherwise win every pick.
		{ID: "FUTURE-1", Title: "Parked for later", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, DeferUntil: &future, Labels: []string{"lane"}, UpdatedAt: now},
		{ID: "ELAPSED-1", Title: "Deferral over", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, DeferUntil: &elapsed, Labels: []string{"lane"}, UpdatedAt: now},
		{ID: "READY-1", Title: "Plain ready work", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"lane"}, UpdatedAt: now},
	}

	got := ComputeTriageWithOptionsAndTime(issues, TriageOptions{
		WaitForPhase2: true,
		GroupByTrack:  true,
		GroupByLabel:  true,
	}, now)

	// Claimable top picks: FUTURE-1 excluded, the other two present.
	seen := map[string]bool{}
	for _, pick := range got.QuickRef.TopPicks {
		seen[pick.ID] = true
	}
	if seen["FUTURE-1"] {
		t.Fatalf("future-deferred bead leaked into top_picks: %#v", got.QuickRef.TopPicks)
	}
	if !seen["ELAPSED-1"] || !seen["READY-1"] {
		t.Fatalf("expected ELAPSED-1 and READY-1 in top_picks, got %#v", got.QuickRef.TopPicks)
	}
	if got.QuickRef.TopPicks[0].ID != "ELAPSED-1" {
		t.Fatalf("expected ELAPSED-1 (P1) to lead once FUTURE-1 is withheld, got %q", got.QuickRef.TopPicks[0].ID)
	}

	// Health counts: the deferred bead is open but not actionable.
	if got.QuickRef.ActionableCount != 2 {
		t.Fatalf("actionable_count = %d, want 2 (FUTURE-1 withheld)", got.QuickRef.ActionableCount)
	}
	if got.QuickRef.NotActionableCount != 1 {
		t.Fatalf("not_actionable_count = %d, want 1", got.QuickRef.NotActionableCount)
	}

	// Grouped top picks never surface the deferred bead either.
	for _, group := range got.RecommendationsByTrack {
		if group.TopPick != nil && group.TopPick.ID == "FUTURE-1" {
			t.Fatalf("future-deferred bead leaked into track top pick: %#v", group)
		}
	}
	for _, group := range got.RecommendationsByLabel {
		if group.TopPick != nil && group.TopPick.ID == "FUTURE-1" {
			t.Fatalf("future-deferred bead leaked into label top pick: %#v", group)
		}
	}

	// The recommendation itself stays visible (agents may want to see parked
	// work) but carries the deferral and never claims availability.
	var futureRec *Recommendation
	for i := range got.Recommendations {
		if got.Recommendations[i].ID == "FUTURE-1" {
			futureRec = &got.Recommendations[i]
		}
	}
	if futureRec == nil {
		t.Fatalf("FUTURE-1 should still appear in recommendations: %#v", got.Recommendations)
	}
	if futureRec.DeferUntil == nil || !futureRec.DeferUntil.Equal(future) {
		t.Fatalf("recommendation defer_until = %v, want %v", futureRec.DeferUntil, future)
	}
	if !strings.Contains(futureRec.Action, "Deferred until "+future.UTC().Format(time.RFC3339)) {
		t.Fatalf("action = %q, want deferral guidance", futureRec.Action)
	}
	joined := strings.Join(futureRec.Reasons, " | ")
	if !strings.Contains(joined, "Deferred until") {
		t.Fatalf("reasons = %v, want a deferral reason", futureRec.Reasons)
	}
	if strings.Contains(joined, "available for work") {
		t.Fatalf("reasons = %v must not claim availability for a deferred bead", futureRec.Reasons)
	}

	// Once the clock passes the deferral the same data becomes claimable.
	later := ComputeTriageWithOptionsAndTime(issues, TriageOptions{WaitForPhase2: true}, future)
	if len(later.QuickRef.TopPicks) == 0 || later.QuickRef.TopPicks[0].ID != "FUTURE-1" {
		t.Fatalf("after the deferral lapses FUTURE-1 (P0) should lead, got %#v", later.QuickRef.TopPicks)
	}
	if later.QuickRef.ActionableCount != 3 {
		t.Fatalf("actionable_count after lapse = %d, want 3", later.QuickRef.ActionableCount)
	}
}

// GetActionableIssues is the analyzer-level "ready" set behind --robot-plan and
// the health counts; it must honour the analyzer clock for defer_until.
func TestGetActionableIssues_ExcludesFutureDeferred(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	issues := []model.Issue{
		{ID: "F", Title: "future", Status: model.StatusOpen, IssueType: model.TypeTask, DeferUntil: &future},
		{ID: "P", Title: "past", Status: model.StatusOpen, IssueType: model.TypeTask, DeferUntil: &past},
		{ID: "N", Title: "none", Status: model.StatusOpen, IssueType: model.TypeTask},
	}

	analyzer := NewAnalyzer(issues)
	analyzer.SetNow(now)
	ids := func(list []model.Issue) []string {
		out := make([]string, 0, len(list))
		for _, issue := range list {
			out = append(out, issue.ID)
		}
		return out
	}
	if got := ids(analyzer.GetActionableIssues()); strings.Join(got, ",") != "N,P" {
		t.Fatalf("actionable at now = %v, want [N P]", got)
	}

	// Moving the clock past the deferral admits F.
	analyzer.SetNow(future)
	if got := ids(analyzer.GetActionableIssues()); strings.Join(got, ",") != "F,N,P" {
		t.Fatalf("actionable at deferral instant = %v, want [F N P]", got)
	}

	// Go's zero time is a valid explicit reproducible epoch.
	analyzer.SetNow(time.Time{})
	if !analyzer.Now().IsZero() {
		t.Fatalf("SetNow(zero) clock = %v, want zero", analyzer.Now())
	}

	// Plan output (what --robot-plan serves) follows the same set.
	analyzer.SetNow(now)
	plan := analyzer.GetExecutionPlan()
	if plan.TotalActionable != 2 {
		t.Fatalf("plan.total_actionable = %d, want 2", plan.TotalActionable)
	}
	for _, track := range plan.Tracks {
		for _, item := range track.Items {
			if item.ID == "F" {
				t.Fatalf("future-deferred bead leaked into execution plan: %#v", plan)
			}
		}
	}
}

func TestTriageUnblocksMap_ExcludesFutureDeferredSuccessor(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "BLOCKER", Status: model.StatusOpen},
		{ID: "PARKED", Status: model.StatusBlocked, DeferUntil: &future, Dependencies: []*model.Dependency{
			{DependsOnID: "BLOCKER", Type: model.DepBlocks},
		}},
	}

	analyzer := NewAnalyzer(issues)
	analyzer.SetNow(now)
	if got := NewTriageContext(analyzer).Unblocks("BLOCKER"); len(got) != 0 {
		t.Fatalf("future-deferred successor leaked into unblocks: %v", got)
	}

	analyzer.SetNow(future)
	if got := NewTriageContext(analyzer).Unblocks("BLOCKER"); len(got) != 1 || got[0] != "PARKED" {
		t.Fatalf("elapsed successor should be unblocked, got %v", got)
	}
}

func TestIsClaimableRecommendation_DeferUntil(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	base := Recommendation{ID: "X", Status: string(model.StatusOpen), Type: string(model.TypeTask)}

	if !isClaimableRecommendation(base, now, nil, nil) {
		t.Fatal("open unassigned unblocked leaf must be claimable")
	}
	deferred := base
	deferred.DeferUntil = &future
	if isClaimableRecommendation(deferred, now, nil, nil) {
		t.Fatal("future defer_until must withhold claimability")
	}
	if !isClaimableRecommendation(deferred, future, nil, nil) {
		t.Fatal("defer_until reached exactly must restore claimability")
	}
}
