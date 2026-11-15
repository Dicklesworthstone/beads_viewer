package analysis

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// atRiskFixture builds one sprint whose beads each trip exactly one signal,
// plus planted negatives: a healthy bead, a closed bead with ancient activity,
// a badly stalled bead that is not in the sprint, a duplicated bead ID, and a
// bead ID with no matching issue.
func atRiskFixture(now time.Time) ([]model.Issue, *model.Sprint) {
	issues := []model.Issue{
		{ID: "s-blocked", Title: "Blocked a while", Status: model.StatusBlocked, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -3)},
		{ID: "s-idle", Title: "Idle", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -5)},
		{
			ID: "s-critical", Title: "Critical blocked", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -1),
			Dependencies: []*model.Dependency{{IssueID: "s-critical", DependsOnID: "blk-fresh", Type: model.DepBlocks, CreatedAt: now.AddDate(0, 0, -1)}},
		},
		{
			ID: "s-stalled", Title: "Waiting on stale blocker", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -1),
			Dependencies: []*model.Dependency{{IssueID: "s-stalled", DependsOnID: "blk-stale", Type: model.DepBlocks, CreatedAt: now.AddDate(0, 0, -1)}},
		},
		{ID: "s-healthy", Title: "Healthy", Status: model.StatusInProgress, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.Add(-time.Hour)},
		{ID: "s-closed", Title: "Closed long ago", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -30)},
		{ID: "outside", Title: "Not in sprint", Status: model.StatusBlocked, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -30)},
		{ID: "blk-fresh", Title: "Fresh blocker", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "blk-stale", Title: "Stale blocker", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -6)},
	}
	sprint := &model.Sprint{
		ID: "sp", Name: "Sprint", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, 7),
		BeadIDs: []string{"s-blocked", "s-idle", "s-critical", "s-stalled", "s-healthy", "s-closed", "s-closed", "missing"},
	}
	return issues, sprint
}

func TestDetectAtRisk_EachSignalFiresOnce(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	issues, sprint := atRiskFixture(now)

	items := DetectAtRisk(issues, sprint, now, DefaultAtRiskThresholds())

	want := map[string][]AtRiskSignal{
		"s-blocked":  {AtRiskBlockedTooLong},
		"s-critical": {AtRiskCriticalBlocked},
		"s-idle":     {AtRiskNoActivity},
		"s-stalled":  {AtRiskBlockersNotClosing},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d at-risk items, want %d: %+v", len(items), len(want), items)
	}
	// Sorted by ID so robot output is stable across runs.
	for i := 1; i < len(items); i++ {
		if items[i-1].ID >= items[i].ID {
			t.Fatalf("items not sorted by ID: %s before %s", items[i-1].ID, items[i].ID)
		}
	}
	for _, item := range items {
		wantSignals, ok := want[item.ID]
		if !ok {
			t.Fatalf("unexpected at-risk item %q (planted negative leaked): %+v", item.ID, item)
		}
		if len(item.Signals) != len(wantSignals) {
			t.Fatalf("%s signals=%v, want %v (detail: %s)", item.ID, item.Signals, wantSignals, item.Detail)
		}
		for i := range wantSignals {
			if item.Signals[i] != wantSignals[i] {
				t.Fatalf("%s signals=%v, want %v", item.ID, item.Signals, wantSignals)
			}
		}
		if item.Detail == "" || item.Title == "" || item.Status == "" {
			t.Fatalf("%s is missing detail/title/status: %+v", item.ID, item)
		}
	}

	byID := make(map[string]AtRiskItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if got, want := byID["s-blocked"].Since, now.AddDate(0, 0, -3); !got.Equal(want) {
		t.Fatalf("s-blocked since=%s, want %s", got, want)
	}
	if got, want := byID["s-idle"].Since, now.AddDate(0, 0, -5); !got.Equal(want) {
		t.Fatalf("s-idle since=%s, want %s", got, want)
	}
	if got, want := byID["s-stalled"].Since, now.AddDate(0, 0, -6); !got.Equal(want) {
		t.Fatalf("s-stalled since should be the blocker's last activity %s, got %s", want, got)
	}
	if got := byID["s-critical"].Detail; got != "P0 blocked by blk-fresh" {
		t.Fatalf("s-critical detail=%q", got)
	}
}

func TestDetectAtRisk_ThresholdsRaiseTheBar(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	issues, sprint := atRiskFixture(now)

	items := DetectAtRisk(issues, sprint, now, AtRiskThresholds{BlockedDays: 10, InactiveDays: 10})

	// Only critical_blocked has no duration threshold.
	if len(items) != 1 || items[0].ID != "s-critical" {
		t.Fatalf("with 10-day thresholds want only s-critical, got %+v", items)
	}
	if len(items[0].Signals) != 1 || items[0].Signals[0] != AtRiskCriticalBlocked {
		t.Fatalf("signals=%v, want [%s]", items[0].Signals, AtRiskCriticalBlocked)
	}

	// Non-positive thresholds fall back to the defaults instead of flagging everything.
	loose := DetectAtRisk(issues, sprint, now, AtRiskThresholds{})
	strict := DetectAtRisk(issues, sprint, now, DefaultAtRiskThresholds())
	if len(loose) != len(strict) {
		t.Fatalf("zero thresholds should equal defaults: got %d vs %d items", len(loose), len(strict))
	}
}

func TestDetectAtRisk_AccumulatesMultipleSignals(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	stale := now.AddDate(0, 0, -9)
	issues := []model.Issue{
		{
			ID: "hot", Title: "P1 stuck behind a dead blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: stale,
			Dependencies: []*model.Dependency{{IssueID: "hot", DependsOnID: "dead", Type: model.DepBlocks, CreatedAt: stale}},
		},
		{ID: "dead", Title: "Dead blocker", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, UpdatedAt: stale},
		// A closed blocker does not count as an open blocker.
		{
			ID: "freed", Title: "Blocker already closed", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "freed", DependsOnID: "done", Type: model.DepBlocks, CreatedAt: stale}},
		},
		{ID: "done", Title: "Closed", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now},
		// A non-blocking dependency never makes a bead "blocked".
		{
			ID: "related", Title: "Related only", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "related", DependsOnID: "dead", Type: model.DepRelated, CreatedAt: stale}},
		},
	}
	sprint := &model.Sprint{ID: "sp", Name: "Sprint", BeadIDs: []string{"hot", "freed", "related"}}

	items := DetectAtRisk(issues, sprint, now, DefaultAtRiskThresholds())
	if len(items) != 1 || items[0].ID != "hot" {
		t.Fatalf("want only 'hot' flagged, got %+v", items)
	}
	got := items[0].Signals
	want := []AtRiskSignal{AtRiskBlockedTooLong, AtRiskNoActivity, AtRiskCriticalBlocked, AtRiskBlockersNotClosing}
	if len(got) != len(want) {
		t.Fatalf("signals=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("signals=%v, want %v", got, want)
		}
	}
	if !items[0].Since.Equal(stale) {
		t.Fatalf("since=%s, want %s", items[0].Since, stale)
	}
}

func TestDetectAtRisk_EmptyInputsYieldEmptySlice(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	issues, sprint := atRiskFixture(now)

	cases := map[string][]AtRiskItem{
		"nil sprint":   DetectAtRisk(issues, nil, now, DefaultAtRiskThresholds()),
		"empty sprint": DetectAtRisk(issues, &model.Sprint{ID: "e"}, now, DefaultAtRiskThresholds()),
		"no issues":    DetectAtRisk(nil, sprint, now, DefaultAtRiskThresholds()),
	}
	for name, got := range cases {
		if got == nil {
			t.Fatalf("%s: want non-nil empty slice so JSON encodes as [] not null", name)
		}
		if len(got) != 0 {
			t.Fatalf("%s: want no items, got %+v", name, got)
		}
	}
}

func TestAllAtRiskSignals(t *testing.T) {
	got := AllAtRiskSignals()
	want := []AtRiskSignal{AtRiskBlockedTooLong, AtRiskNoActivity, AtRiskCriticalBlocked, AtRiskBlockersNotClosing}
	if len(got) != len(want) {
		t.Fatalf("AllAtRiskSignals=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllAtRiskSignals=%v, want %v", got, want)
		}
	}
}

func TestDetectAtRisk_SkipsParkedTemplatePinnedAndParentChild(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ancient := now.AddDate(0, 0, -20)
	future := now.Add(48 * time.Hour)
	issues := []model.Issue{
		{ID: "tmpl", Title: "Template", Status: model.StatusOpen, IsTemplate: true, Priority: 0, UpdatedAt: ancient},
		{ID: "pin-flag", Title: "Pinned flag", Status: model.StatusOpen, Pinned: true, Priority: 0, UpdatedAt: ancient},
		{ID: "pin-status", Title: "Pinned status", Status: model.StatusPinned, Priority: 0, UpdatedAt: ancient},
		{ID: "deferred", Title: "Deferred", Status: model.StatusDeferred, Priority: 0, UpdatedAt: ancient},
		{ID: "ephemeral", Title: "Ephemeral", Status: model.StatusOpen, Ephemeral: true, Priority: 0, UpdatedAt: ancient},
		{ID: "defer-until", Title: "Snoozed", Status: model.StatusOpen, DeferUntil: &future, Priority: 0, UpdatedAt: ancient},
		{ID: "parent-epic", Title: "Parent epic", Status: model.StatusOpen, Priority: 1, UpdatedAt: ancient},
		{
			ID: "child-fresh", Title: "Child with parent-child link", Status: model.StatusOpen, Priority: 0, UpdatedAt: now.Add(-time.Hour),
			Dependencies: []*model.Dependency{
				{IssueID: "child-fresh", DependsOnID: "parent-epic", Type: model.DepParentChild, CreatedAt: ancient},
			},
		},
	}
	sp := &model.Sprint{
		ID:      "sprint-1",
		BeadIDs: []string{"tmpl", "pin-flag", "pin-status", "deferred", "ephemeral", "defer-until", "child-fresh"},
	}

	if got := DetectAtRisk(issues, sp, now, DefaultAtRiskThresholds()); len(got) != 0 {
		t.Fatalf("expected 0 at-risk items for parked/template/pinned/parent-child beads, got %+v", got)
	}
}
