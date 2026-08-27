package correlation

import (
	"testing"
	"time"
)

// Helper to create test timestamps
func testTime(offsetHours int) time.Time {
	base := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(offsetHours) * time.Hour)
}

func TestBuildCausalityChain_BasicChain(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(2)},
					{EventType: EventClosed, Timestamp: testTime(10)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: "Fix bug", Timestamp: testTime(5)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-test", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check chain structure
	if result.Chain.BeadID != "bv-test" {
		t.Errorf("Expected bead ID 'bv-test', got '%s'", result.Chain.BeadID)
	}

	if result.Chain.Status != "closed" {
		t.Errorf("Expected status 'closed', got '%s'", result.Chain.Status)
	}

	if !result.Chain.IsComplete {
		t.Error("Expected IsComplete to be true for closed bead")
	}

	// Should have 4 events: created, claimed, commit, closed
	if len(result.Chain.Events) != 4 {
		t.Errorf("Expected 4 events, got %d", len(result.Chain.Events))
	}

	// Check event order (should be sorted by timestamp)
	expectedOrder := []CausalEventType{CausalCreated, CausalClaimed, CausalCommit, CausalClosed}
	for i, expected := range expectedOrder {
		if result.Chain.Events[i].Type != expected {
			t.Errorf("Event %d: expected type '%s', got '%s'", i, expected, result.Chain.Events[i].Type)
		}
	}
}

func TestBuildCausalityChainAtPinsOpenDurationAndTieOrder(t *testing.T) {
	pinned := testTime(24)
	start := testTime(0)
	report := &HistoryReport{
		DataHash: "pinned-hash",
		Histories: map[string]BeadHistory{
			"bv-open": {
				BeadID: "bv-open",
				Title:  "Open work",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventClaimed, Timestamp: start},
					{EventType: EventCreated, Timestamp: start},
				},
				Commits: []CorrelatedCommit{{ShortSHA: "abc1234", Message: "work", Timestamp: start}},
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-open", CausalityOptions{IncludeCommits: true}, pinned)
	if result == nil {
		t.Fatal("expected causality result")
	}
	if !result.GeneratedAt.Equal(pinned) || !result.Chain.EndTime.Equal(pinned) {
		t.Fatalf("pinned times = generated %v, end %v; want %v", result.GeneratedAt, result.Chain.EndTime, pinned)
	}
	if got, want := result.Chain.TotalTime, pinned.Sub(start); got != want {
		t.Fatalf("total time = %v, want %v", got, want)
	}
	wantTypes := []CausalEventType{CausalCreated, CausalClaimed, CausalCommit}
	for i, want := range wantTypes {
		if result.Chain.Events[i].Type != want {
			t.Fatalf("event %d type = %s, want %s", i, result.Chain.Events[i].Type, want)
		}
	}

	zeroResult := report.BuildCausalityChainAt("bv-open", CausalityOptions{IncludeCommits: true}, time.Time{})
	if !zeroResult.GeneratedAt.IsZero() {
		t.Fatalf("zero generated_at was replaced with %v", zeroResult.GeneratedAt)
	}
	if !zeroResult.Chain.EndTime.Equal(start) || zeroResult.Chain.TotalTime != 0 {
		t.Fatalf("pre-event zero instant should clamp deterministically: end=%v total=%v", zeroResult.Chain.EndTime, zeroResult.Chain.TotalTime)
	}
}

func TestBuildCausalityChainAtNormalizesStatusWithoutEvents(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-empty": {
				BeadID: "bv-empty",
				Status: "  TOMBSTONE\t",
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-empty", CausalityOptions{}, testTime(10))
	if result == nil {
		t.Fatal("expected causality result")
	}
	if got, want := result.Chain.Status, "tombstone"; got != want {
		t.Fatalf("normalized status = %q, want %q", got, want)
	}
	if !result.Chain.IsComplete {
		t.Fatal("tombstoned history with no events must still be complete")
	}
	if result.Chain.TotalTime != 0 || !result.Chain.StartTime.IsZero() || !result.Chain.EndTime.IsZero() {
		t.Fatalf("empty chain times = start %v, end %v, total %v; want zero values",
			result.Chain.StartTime, result.Chain.EndTime, result.Chain.TotalTime)
	}
	if got, want := result.Insights.Summary, "Completed; transition timing unavailable from history"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	wantRecommendation := "Completion-duration metrics are unavailable because history has no terminal close transition"
	if got := result.Insights.Recommendations; len(got) == 0 || got[0] != wantRecommendation {
		t.Fatalf("recommendations = %q, want first recommendation %q", got, wantRecommendation)
	}
}

func TestBuildCausalityChainClosedStatusAfterReopenDoesNotInventCompletionTime(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-inconsistent": {
			BeadID: "bv-inconsistent",
			Status: "closed",
			Events: []BeadEvent{
				{EventType: EventCreated, Timestamp: testTime(0)},
				{EventType: EventClosed, Timestamp: testTime(2)},
				{EventType: EventReopened, Timestamp: testTime(3)},
			},
		},
	}}

	result := report.BuildCausalityChainAt("bv-inconsistent", CausalityOptions{}, testTime(10))
	if result == nil || !result.Chain.IsComplete {
		t.Fatalf("closed status result = %+v, want complete current state", result)
	}
	if got, want := result.Insights.Summary, "Completed; transition timing unavailable from history"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if !result.Chain.EndTime.IsZero() || result.Chain.TotalTime != 0 || result.Insights.TotalDuration != 0 || result.Insights.ActiveDuration != 0 {
		t.Fatalf("unknown completion timing leaked numeric duration: chain=%+v insights=%+v", result.Chain, result.Insights)
	}
}

func TestBuildCausalityChainClosedStatusWithCommitButNoCloseDoesNotInventCompletionTime(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-missing-close": {
			BeadID:  "bv-missing-close",
			Status:  "closed",
			Events:  []BeadEvent{{EventType: EventCreated, Timestamp: testTime(0)}},
			Commits: []CorrelatedCommit{{SHA: "full-sha", Timestamp: testTime(8)}},
		},
	}}

	result := report.BuildCausalityChainAt("bv-missing-close", DefaultCausalityOptions(), testTime(10))
	if result == nil || len(result.Chain.Events) != 2 {
		t.Fatalf("result = %+v, want two observed events", result)
	}
	if !result.Chain.EndTime.IsZero() || result.Chain.TotalTime != 0 || result.Insights.TotalDuration != 0 {
		t.Fatalf("last commit was presented as completion timing: chain=%+v insights=%+v", result.Chain, result.Insights)
	}
	if result.Insights.EstimatedWithout != nil || result.Insights.ActiveDuration != 0 || result.Insights.BlockedPercentage != 0 {
		t.Fatalf("unknown completion duration produced derived metrics: %+v", result.Insights)
	}
}

func TestBuildCausalityChainUsesTerminalCloseRatherThanLaterCommit(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-closed": {
			BeadID: "bv-closed",
			Status: "closed",
			Events: []BeadEvent{
				{EventType: EventCreated, Timestamp: testTime(0)},
				{EventType: EventClosed, Timestamp: testTime(4)},
			},
			Commits: []CorrelatedCommit{{SHA: "post-close", Timestamp: testTime(7)}},
		},
	}}

	result := report.BuildCausalityChainAt("bv-closed", DefaultCausalityOptions(), testTime(10))
	if result == nil {
		t.Fatal("expected causality result")
	}
	if got, want := result.Chain.EndTime, testTime(4); !got.Equal(want) {
		t.Fatalf("completion end = %v, want terminal close %v", got, want)
	}
	if got, want := result.Chain.TotalTime, 4*time.Hour; got != want {
		t.Fatalf("completion duration = %v, want %v", got, want)
	}
	if got := result.Insights.CommitCount; got != 0 {
		t.Fatalf("causal commit count = %d, want 0 because the only commit is post-close", got)
	}
	if got, want := result.Insights.CriticalPathDesc, "created → closed"; got != want {
		t.Fatalf("causal path = %q, want %q", got, want)
	}
	if result.Insights.LongestGap == nil || *result.Insights.LongestGap != 4*time.Hour {
		t.Fatalf("longest causal gap = %v, want 4h before close", result.Insights.LongestGap)
	}
}

func TestBuildCausalityChainCountsCodeCoCommittedWithTerminalClose(t *testing.T) {
	const closingSHA = "0123456789abcdef0123456789abcdef01234567"
	closedAt := testTime(4)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-close-with-code": {
			BeadID: "bv-close-with-code",
			Status: "closed",
			Events: []BeadEvent{
				{EventType: EventCreated, Timestamp: testTime(0), CommitSHA: "created-sha"},
				{EventType: EventClosed, Timestamp: closedAt, CommitSHA: closingSHA},
			},
			Commits: []CorrelatedCommit{{
				SHA:       closingSHA,
				ShortSHA:  closingSHA[:7],
				Message:   "finish implementation",
				Timestamp: closedAt,
			}},
		},
	}}

	result := report.BuildCausalityChainAt("bv-close-with-code", DefaultCausalityOptions(), testTime(10))
	if result == nil || result.Chain == nil {
		t.Fatal("expected causality result")
	}
	wantTypes := []CausalEventType{CausalCreated, CausalCommit, CausalClosed}
	if len(result.Chain.Events) != len(wantTypes) {
		t.Fatalf("events = %+v, want created/commit/closed", result.Chain.Events)
	}
	for i, want := range wantTypes {
		if got := result.Chain.Events[i].Type; got != want {
			t.Fatalf("event %d = %q, want %q", i, got, want)
		}
	}
	if result.Insights.CommitCount != 1 {
		t.Fatalf("co-committed close commit count = %d, want 1", result.Insights.CommitCount)
	}
	if got, want := result.Insights.CriticalPathDesc, "created → commit → closed"; got != want {
		t.Fatalf("co-committed causal path = %q, want %q", got, want)
	}
}

func TestBuildCausalityChainAtNeverEndsBeforeLatestEvent(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-future": {
				BeadID: "bv-future",
				Status: "open",
				Events: []BeadEvent{{EventType: EventCreated, Timestamp: testTime(0)}},
				Commits: []CorrelatedCommit{{
					ShortSHA:  "future1",
					Message:   "future commit",
					Timestamp: testTime(5),
				}},
			},
		},
	}

	result := report.BuildCausalityChainAt(
		"bv-future",
		CausalityOptions{IncludeCommits: true},
		testTime(2),
	)
	if result == nil {
		t.Fatal("expected causality result")
	}
	if got, want := result.Chain.EndTime, testTime(5); !got.Equal(want) {
		t.Fatalf("end time = %v, want latest event %v", got, want)
	}
	if got, want := result.Chain.TotalTime, 5*time.Hour; got != want {
		t.Fatalf("total time = %v, want %v", got, want)
	}
}

func TestBuildCausalityChainAtNilReport(t *testing.T) {
	var report *HistoryReport
	if result := report.BuildCausalityChainAt("bv-test", CausalityOptions{}, testTime(0)); result != nil {
		t.Fatalf("nil report result = %+v, want nil", result)
	}
}

func TestBuildCausalityChain_CausalLinks(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
					{EventType: EventClosed, Timestamp: testTime(2)},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}
	result := report.BuildCausalityChain("bv-test", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check causal links
	// Event 0 (created) should enable event 1 (claimed)
	if len(result.Chain.Events[0].EnablesIDs) != 1 || result.Chain.Events[0].EnablesIDs[0] != 1 {
		t.Errorf("Event 0 should enable event 1, got enables: %v", result.Chain.Events[0].EnablesIDs)
	}

	// Event 1 (claimed) should be caused by event 0 and enable event 2
	if result.Chain.Events[1].CausedByID == nil || *result.Chain.Events[1].CausedByID != 0 {
		t.Error("Event 1 should be caused by event 0")
	}
	if len(result.Chain.Events[1].EnablesIDs) != 1 || result.Chain.Events[1].EnablesIDs[0] != 2 {
		t.Errorf("Event 1 should enable event 2, got enables: %v", result.Chain.Events[1].EnablesIDs)
	}

	// Event 2 (closed) should be caused by event 1
	if result.Chain.Events[2].CausedByID == nil || *result.Chain.Events[2].CausedByID != 1 {
		t.Error("Event 2 should be caused by event 1")
	}
}

func TestBuildCausalityChain_NotFound(t *testing.T) {
	report := &HistoryReport{
		DataHash:  "test-hash",
		Histories: map[string]BeadHistory{},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("nonexistent", opts)

	if result != nil {
		t.Error("Expected nil result for nonexistent bead")
	}
}

func TestBuildCausalityChain_WithCommits(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: "First commit", Timestamp: testTime(2)},
					{ShortSHA: "def5678", Message: "Second commit", Timestamp: testTime(3)},
				},
			},
		},
	}

	// With commits
	optsWithCommits := CausalityOptions{IncludeCommits: true}
	resultWith := report.BuildCausalityChain("bv-test", optsWithCommits)

	if resultWith.Insights.CommitCount != 2 {
		t.Errorf("Expected 2 commits, got %d", resultWith.Insights.CommitCount)
	}

	// Without commits
	optsNoCommits := CausalityOptions{IncludeCommits: false}
	resultWithout := report.BuildCausalityChain("bv-test", optsNoCommits)

	if resultWithout.Insights.CommitCount != 0 {
		t.Errorf("Expected 0 commits when IncludeCommits=false, got %d", resultWithout.Insights.CommitCount)
	}
}

func TestBuildCausalityChainUsesFullSHAWhenShortSHAIsMissing(t *testing.T) {
	const fullSHA = "0123456789abcdef0123456789abcdef01234567"
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-sha": {
				BeadID: "bv-sha",
				Status: "closed",
				Events: []BeadEvent{{EventType: EventCreated, Timestamp: testTime(0)}},
				Commits: []CorrelatedCommit{{
					SHA:       fullSHA,
					Message:   "linked work",
					Timestamp: testTime(1),
				}},
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-sha", DefaultCausalityOptions(), testTime(2))
	if result == nil {
		t.Fatal("expected causality result")
	}
	for _, event := range result.Chain.Events {
		if event.Type == CausalCommit {
			if event.CommitSHA != fullSHA {
				t.Fatalf("commit SHA = %q, want full SHA fallback %q", event.CommitSHA, fullSHA)
			}
			return
		}
	}
	t.Fatal("expected commit event")
}

func TestBuildCausalityChainKeepsDistinctFullSHAsWithCollidingShortSHAs(t *testing.T) {
	const (
		sharedShort = "0123456"
		firstFull   = "0123456aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		secondFull  = "0123456bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-collision": {
			BeadID: "bv-collision",
			Status: "in_progress",
			Events: []BeadEvent{{EventType: EventCreated, Timestamp: testTime(0)}},
			Commits: []CorrelatedCommit{
				{SHA: firstFull, ShortSHA: sharedShort, Message: "first", Timestamp: testTime(1)},
				{SHA: secondFull, ShortSHA: sharedShort, Message: "second", Timestamp: testTime(2)},
			},
		},
	}}

	result := report.BuildCausalityChainAt("bv-collision", DefaultCausalityOptions(), testTime(3))
	if result == nil {
		t.Fatal("expected causality result")
	}
	var got []string
	for _, event := range result.Chain.Events {
		if event.Type == CausalCommit {
			got = append(got, event.CommitSHA)
		}
	}
	want := []string{firstFull, secondFull}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("serialized commit SHAs = %v, want distinct full identities %v", got, want)
	}
}

func TestBuildCausalityChain_InProgress(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-test", opts)

	if result.Chain.IsComplete {
		t.Error("Expected IsComplete to be false for in_progress bead")
	}

	// EndTime should be after StartTime for in-progress beads
	if !result.Chain.EndTime.After(result.Chain.StartTime) {
		t.Error("EndTime should be after StartTime")
	}
}

func TestBuildCausalityChainBlockedWithoutTransitionIsHonest(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-blocked": {
				BeadID: "bv-blocked",
				Status: " BLOCKED ",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					// The extractor currently represents open -> blocked as modified,
					// which must not be upgraded to a timed block transition.
					{EventType: EventModified, Timestamp: testTime(5)},
				},
			},
		},
	}

	result := report.BuildCausalityChainAt(
		"bv-blocked",
		CausalityOptions{IncludeCommits: false},
		testTime(10),
	)
	if result == nil {
		t.Fatal("expected causality result")
	}
	if got, want := result.Chain.Status, "blocked"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := result.Insights.Summary, "Blocked; transition timing unavailable from history"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	wantRecommendation := "Blocked-duration metrics are unavailable because history has no explicit block transition"
	if got := result.Insights.Recommendations; len(got) == 0 || got[0] != wantRecommendation {
		t.Fatalf("recommendations = %q, want first recommendation %q", got, wantRecommendation)
	}
	if result.Insights.BlockedDuration != 0 || len(result.Insights.BlockedPeriods) != 0 {
		t.Fatalf("missing transition must not fabricate blocked time: duration=%v periods=%+v",
			result.Insights.BlockedDuration, result.Insights.BlockedPeriods)
	}
	for _, event := range result.Chain.Events {
		if event.Type == CausalBlocked || event.Type == CausalUnblocked {
			t.Fatalf("modified lifecycle event fabricated block transition: %+v", event)
		}
	}
}

func TestCausalInsights_BlockedPercentage(t *testing.T) {
	// Test the blocked percentage calculation
	insights := CausalInsights{
		TotalDuration:   10 * time.Hour,
		BlockedDuration: 5 * time.Hour,
	}

	// Recalculate active duration and blocked percentage
	insights.ActiveDuration = insights.TotalDuration - insights.BlockedDuration
	if insights.TotalDuration > 0 {
		insights.BlockedPercentage = float64(insights.BlockedDuration) / float64(insights.TotalDuration) * 100
	}

	if insights.BlockedPercentage != 50 {
		t.Errorf("Expected 50%% blocked, got %.1f%%", insights.BlockedPercentage)
	}

	if insights.ActiveDuration != 5*time.Hour {
		t.Errorf("Expected 5h active, got %v", insights.ActiveDuration)
	}
}

// These two buildInsights tests exercise its explicit-chain contract directly.
// They do not claim that BuildCausalityChainAt can recover block timestamps from
// the current EventModified-only Git history.
func TestBuildInsightsSyntheticTransitionsTrackRepeatedBlockAndOpenPeriod(t *testing.T) {
	chain := &CausalChain{
		Status: "blocked",
		Events: []CausalEvent{
			{ID: 0, Type: CausalCreated, Timestamp: testTime(0)},
			{ID: 1, Type: CausalBlocked, Timestamp: testTime(1), BlockerID: "bv-a"},
			{ID: 2, Type: CausalBlocked, Timestamp: testTime(2), BlockerID: "bv-a"},
			{ID: 3, Type: CausalBlocked, Timestamp: testTime(4), BlockerID: "bv-b"},
			{ID: 4, Type: CausalUnblocked, Timestamp: testTime(6), BlockerID: "bv-b"},
			{ID: 5, Type: CausalBlocked, Timestamp: testTime(8), BlockerID: "bv-c"},
		},
		StartTime: testTime(0),
		EndTime:   testTime(10),
		TotalTime: 10 * time.Hour,
	}

	insights := buildInsights(chain)
	if got, want := len(insights.BlockedPeriods), 2; got != want {
		t.Fatalf("blocked period count = %d, want %d: %+v", got, want, insights.BlockedPeriods)
	}
	wantPeriods := []BlockedPeriod{
		{StartTime: testTime(1), EndTime: testTime(6), Duration: 5 * time.Hour, BlockerID: "bv-a"},
		{StartTime: testTime(8), EndTime: testTime(10), Duration: 2 * time.Hour, BlockerID: "bv-c"},
	}
	for i, want := range wantPeriods {
		if got := insights.BlockedPeriods[i]; got != want {
			t.Errorf("blocked period %d = %+v, want %+v", i, got, want)
		}
	}
	if got, want := insights.BlockedDuration, 7*time.Hour; got != want {
		t.Errorf("blocked duration = %v, want %v", got, want)
	}
	if got, want := insights.ActiveDuration, 3*time.Hour; got != want {
		t.Errorf("active duration = %v, want %v", got, want)
	}
	if got, want := insights.BlockedPercentage, 70.0; got != want {
		t.Errorf("blocked percentage = %.1f, want %.1f", got, want)
	}
	if insights.EstimatedWithout == nil || *insights.EstimatedWithout != 3*time.Hour {
		t.Errorf("estimated duration without blocks = %v, want 3h", insights.EstimatedWithout)
	}
	if got, want := insights.Summary, "Blocked now (10h total, 7h blocked)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestBuildInsightsSyntheticTransitionClosesWhenWorkCloses(t *testing.T) {
	chain := &CausalChain{
		Events: []CausalEvent{
			{ID: 0, Type: CausalCreated, Timestamp: testTime(0)},
			{ID: 1, Type: CausalBlocked, Timestamp: testTime(2), BlockerID: "bv-a"},
			{ID: 2, Type: CausalClosed, Timestamp: testTime(7)},
			{ID: 3, Type: CausalCommit, Timestamp: testTime(8)},
		},
		StartTime:  testTime(0),
		EndTime:    testTime(8),
		TotalTime:  8 * time.Hour,
		IsComplete: true,
	}

	insights := buildInsights(chain)
	if got, want := insights.BlockedDuration, 5*time.Hour; got != want {
		t.Fatalf("blocked duration = %v, want %v", got, want)
	}
	if got, want := len(insights.BlockedPeriods), 1; got != want {
		t.Fatalf("blocked period count = %d, want %d", got, want)
	}
	if got, want := insights.BlockedPeriods[0].EndTime, testTime(7); !got.Equal(want) {
		t.Fatalf("blocked period end = %v, want close event %v", got, want)
	}
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Minute, "30m"},
		{90 * time.Minute, "1h"},
		{5 * time.Hour, "5h"},
		{25 * time.Hour, "1d"},
		{3 * 24 * time.Hour, "3d"},
		{10 * 24 * time.Hour, "1w"},
		{28 * 24 * time.Hour, "4w"},
		{29 * 24 * time.Hour, "4w"},
		{30 * 24 * time.Hour, "1mo"},
		{35 * 24 * time.Hour, "1mo"},
	}

	for _, tt := range tests {
		result := formatDurationShort(tt.duration)
		if result != tt.expected {
			t.Errorf("formatDurationShort(%v) = '%s', expected '%s'", tt.duration, result, tt.expected)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	tests := []struct {
		pct      float64
		expected string
	}{
		{0, "0%"},
		{50, "50%"},
		{100, "100%"},
		{33.7, "33%"}, // Truncates to int
	}

	for _, tt := range tests {
		result := formatPercent(tt.pct)
		if result != tt.expected {
			t.Errorf("formatPercent(%.1f) = '%s', expected '%s'", tt.pct, result, tt.expected)
		}
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		n        int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{123, "123"},
		{-5, "-5"},
	}

	for _, tt := range tests {
		result := formatInt(tt.n)
		if result != tt.expected {
			t.Errorf("formatInt(%d) = '%s', expected '%s'", tt.n, result, tt.expected)
		}
	}

	minInt := -int(^uint(0)>>1) - 1
	if result := formatInt(minInt); len(result) < 2 || result == "-" {
		t.Errorf("formatInt(minInt) = %q, want full decimal representation", result)
	}
}

func TestBuildSummary_Completed(t *testing.T) {
	chain := &CausalChain{
		IsComplete: true,
		TotalTime:  6 * time.Hour,
	}
	insights := &CausalInsights{
		TotalDuration:     6 * time.Hour,
		CommitCount:       3,
		BlockedPercentage: 10,
	}

	summary := buildSummary(chain, insights)

	// Should mention completion and commit count
	if summary == "" {
		t.Error("Expected non-empty summary")
	}
}

func TestFormatCommitCountUsesSingularOnlyForOne(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  string
	}{
		{count: 0, want: "0 commits"},
		{count: 1, want: "1 commit"},
		{count: 2, want: "2 commits"},
	} {
		if got := formatCommitCount(tc.count); got != tc.want {
			t.Fatalf("formatCommitCount(%d) = %q, want %q", tc.count, got, tc.want)
		}
	}
}

func TestBuildSummary_InProgress(t *testing.T) {
	chain := &CausalChain{
		IsComplete: false,
		TotalTime:  2 * 24 * time.Hour,
	}
	insights := &CausalInsights{
		TotalDuration:     2 * 24 * time.Hour,
		CommitCount:       5,
		BlockedPercentage: 0,
	}

	summary := buildSummary(chain, insights)

	if summary == "" {
		t.Error("Expected non-empty summary")
	}
}

func TestGenerateRecommendations_HighBlockedPercentage(t *testing.T) {
	chain := &CausalChain{IsComplete: false}
	insights := &CausalInsights{
		TotalDuration:     24 * time.Hour,
		BlockedPercentage: 60,
	}

	recs := generateRecommendations(chain, insights)

	found := false
	for _, rec := range recs {
		if rec != "" && len(rec) > 10 {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected at least one meaningful recommendation for high blocked percentage")
	}
}

func TestGenerateRecommendations_LongGap(t *testing.T) {
	chain := &CausalChain{IsComplete: true}
	longGap := 10 * 24 * time.Hour
	insights := &CausalInsights{
		TotalDuration:     14 * 24 * time.Hour,
		BlockedPercentage: 0,
		LongestGap:        &longGap,
	}

	recs := generateRecommendations(chain, insights)

	found := false
	for _, rec := range recs {
		if rec != "" && len(rec) > 10 {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected at least one recommendation for long gap")
	}
}

func TestGenerateRecommendations_NoIssues(t *testing.T) {
	chain := &CausalChain{IsComplete: true}
	insights := &CausalInsights{
		TotalDuration:     2 * 24 * time.Hour,
		BlockedPercentage: 0,
		CommitCount:       5,
	}

	recs := generateRecommendations(chain, insights)

	// Should have the "no issues" recommendation
	hasNoIssues := false
	for _, rec := range recs {
		if rec == "No significant issues detected in the causal flow" {
			hasNoIssues = true
			break
		}
	}

	if !hasNoIssues {
		t.Error("Expected 'no issues' recommendation for healthy flow")
	}
}

func TestCausalEventTypes(t *testing.T) {
	// Verify all event types are distinct
	types := []CausalEventType{
		CausalCreated,
		CausalClaimed,
		CausalCommit,
		CausalBlocked,
		CausalUnblocked,
		CausalClosed,
		CausalReopened,
	}

	seen := make(map[CausalEventType]bool)
	for _, et := range types {
		if seen[et] {
			t.Errorf("Duplicate event type: %s", et)
		}
		seen[et] = true
	}
}

func TestChainDurations(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(2)},
					{EventType: EventClosed, Timestamp: testTime(10)},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}
	result := report.BuildCausalityChain("bv-test", opts)

	// Check duration calculations
	// Created at hour 0, claimed at hour 2 = 2 hours between
	if result.Chain.Events[0].DurationNext == nil {
		t.Error("Expected non-nil DurationNext for first event")
	} else if *result.Chain.Events[0].DurationNext != 2*time.Hour {
		t.Errorf("Expected 2h between created and claimed, got %v", *result.Chain.Events[0].DurationNext)
	}

	// Claimed at hour 2, closed at hour 10 = 8 hours between
	if result.Chain.Events[1].DurationNext == nil {
		t.Error("Expected non-nil DurationNext for second event")
	} else if *result.Chain.Events[1].DurationNext != 8*time.Hour {
		t.Errorf("Expected 8h between claimed and closed, got %v", *result.Chain.Events[1].DurationNext)
	}

	// Total time should be 10 hours
	if result.Chain.TotalTime != 10*time.Hour {
		t.Errorf("Expected total time of 10h, got %v", result.Chain.TotalTime)
	}
}

func TestDefaultCausalityOptions(t *testing.T) {
	opts := DefaultCausalityOptions()

	if !opts.IncludeCommits {
		t.Error("Expected IncludeCommits to be true by default")
	}
}

// TestBuildCausalityChain_SameTimestamps tests the edge case where all events
// have the same timestamp (gap = 0 between all events). This previously caused
// an array index out of bounds panic.
func TestBuildCausalityChain_SameTimestamps(t *testing.T) {
	// All events at the same timestamp
	sameTime := testTime(0)
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-same": {
				BeadID: "bv-same",
				Title:  "Same Timestamp Test",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: sameTime},
					{EventType: EventClaimed, Timestamp: sameTime},
					{EventType: EventClosed, Timestamp: sameTime},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}

	// This should not panic (previously it would cause index out of bounds)
	result := report.BuildCausalityChain("bv-same", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check insights were computed without panic
	if result.Insights == nil {
		t.Fatal("Expected non-nil insights")
	}

	// With same timestamps, all gaps are 0
	if result.Insights.LongestGap != nil && *result.Insights.LongestGap != 0 {
		t.Errorf("Expected longest gap of 0, got %v", *result.Insights.LongestGap)
	}

	// LongestGapDesc should be computed without error
	if result.Insights.LongestGapDesc == "" {
		t.Error("Expected non-empty LongestGapDesc even with 0 gap")
	}
}

func TestBuildCausalityChain_PreservesSameTimestampLifecycleOrder(t *testing.T) {
	createdAt := testTime(0)
	transitionAt := testTime(1)
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-tied-transitions": {
				BeadID: "bv-tied-transitions",
				Title:  "Tied lifecycle transitions",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: createdAt},
					{EventType: EventClosed, Timestamp: transitionAt},
					{EventType: EventReopened, Timestamp: transitionAt},
					{EventType: EventClosed, Timestamp: transitionAt},
				},
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-tied-transitions", CausalityOptions{IncludeCommits: false}, transitionAt)
	if result == nil || result.Chain == nil {
		t.Fatal("expected a causal chain")
	}
	wantTypes := []CausalEventType{CausalCreated, CausalClosed, CausalReopened, CausalClosed}
	if len(result.Chain.Events) != len(wantTypes) {
		t.Fatalf("events = %#v, want %d lifecycle events", result.Chain.Events, len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := result.Chain.Events[i].Type; got != want {
			t.Fatalf("event %d type = %q, want %q; tied lifecycle order changed", i, got, want)
		}
	}
	if !result.Chain.IsComplete || !result.Chain.EndTime.Equal(transitionAt) {
		t.Fatalf("terminal close was lost: complete=%v end=%v", result.Chain.IsComplete, result.Chain.EndTime)
	}
}

// TestBuildCausalityChain_UnicodeCommitMessage tests that commit messages
// with Unicode characters are truncated correctly by runes, not bytes.
func TestBuildCausalityChain_UnicodeCommitMessage(t *testing.T) {
	// Unicode message that would be broken if truncated by bytes
	unicodeMsg := "修复中文测试问题，这是一个很长的提交消息，需要被正确截断" // Chinese characters

	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-unicode": {
				BeadID: "bv-unicode",
				Title:  "Unicode Test",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClosed, Timestamp: testTime(1)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: unicodeMsg, Timestamp: testTime(0)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-unicode", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Find the commit event
	var commitEvent *CausalEvent
	for i := range result.Chain.Events {
		if result.Chain.Events[i].Type == CausalCommit {
			commitEvent = &result.Chain.Events[i]
			break
		}
	}

	if commitEvent == nil {
		t.Fatal("Expected to find commit event")
	}

	// Description should be valid UTF-8 (not broken by mid-byte truncation)
	desc := commitEvent.Description
	if !isValidUTF8(desc) {
		t.Errorf("Commit description has invalid UTF-8: %q", desc)
	}

	// Should end with "..." if truncated
	if len([]rune(unicodeMsg)) > 50 && !endsWithEllipsis(desc) {
		t.Error("Expected truncated description to end with '...'")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' { // Replacement character indicates invalid UTF-8
			return false
		}
	}
	return true
}

func endsWithEllipsis(s string) bool {
	runes := []rune(s)
	if len(runes) < 3 {
		return false
	}
	last3 := string(runes[len(runes)-3:])
	return last3 == "..."
}
