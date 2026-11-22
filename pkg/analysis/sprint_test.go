package analysis

import (
	"strings"
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

func TestSprintAtRiskHonorsReadinessIndex(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	stalledUpdated := now.AddDate(0, 0, -8)
	blockedLinkCreated := now.AddDate(0, 0, -5)
	unknownLinkCreated := now.AddDate(0, 0, -4)
	ancient := now.AddDate(0, 0, -15)

	issues := []model.Issue{
		{
			ID: "stalled-root", Title: "Stalled root blocker", Status: model.StatusOpen,
			Priority: 2, IssueType: model.TypeTask, UpdatedAt: stalledUpdated,
		},
		{
			ID: "blocked-epic", Title: "Blocked parent epic", Status: model.StatusOpen,
			Priority: 1, IssueType: model.TypeEpic, UpdatedAt: now.Add(-time.Hour),
			Dependencies: []*model.Dependency{
				{IssueID: "blocked-epic", DependsOnID: "stalled-root", Type: model.DepBlocks, CreatedAt: blockedLinkCreated},
			},
		},
		{
			// Transitive parent-child readiness: has NO direct DepBlocks, only DepParentChild to blocked-epic!
			// UpdatedAt is 3d ago (< 4d InactiveDays so no_activity stays quiet, >= 2d BlockedDays).
			ID: "child-of-blocked-epic", Title: "Child of blocked epic", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -3),
			Dependencies: []*model.Dependency{
				{IssueID: "child-of-blocked-epic", DependsOnID: "blocked-epic", Type: model.DepParentChild, CreatedAt: blockedLinkCreated},
			},
		},
		{
			// Unknown external blocker ID missing from loaded slice -> ReadinessIndex marks DependenciesUnknown (unsatisfied).
			ID: "dangling-blocker", Title: "Depends on unloaded external ID", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -3),
			Dependencies: []*model.Dependency{
				{IssueID: "dangling-blocker", DependsOnID: "MISSING-EXT-99", Type: model.DepBlocks, CreatedAt: unknownLinkCreated},
			},
		},
		// Planted negative controls:
		{
			ID: "ready-epic", Title: "Open unblocked epic", Status: model.StatusOpen,
			Priority: 1, IssueType: model.TypeEpic, UpdatedAt: ancient,
		},
		{
			ID: "child-of-ready-epic", Title: "Child of open unblocked epic", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.Add(-time.Hour),
			Dependencies: []*model.Dependency{
				{IssueID: "child-of-ready-epic", DependsOnID: "ready-epic", Type: model.DepParentChild, CreatedAt: ancient},
			},
		},
		{
			ID: "tomb-blocker", Title: "Deleted blocker", Status: model.StatusTombstone,
			Priority: 1, IssueType: model.TypeTask, UpdatedAt: ancient,
		},
		{
			ID: "tomb-satisfied", Title: "Depends on tombstoned blocker", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: now.Add(-time.Hour),
			Dependencies: []*model.Dependency{
				{IssueID: "tomb-satisfied", DependsOnID: "tomb-blocker", Type: model.DepBlocks, CreatedAt: ancient},
			},
		},
	}

	sp := &model.Sprint{
		ID: "sprint-readiness",
		BeadIDs: []string{
			"child-of-blocked-epic",
			"dangling-blocker",
			"child-of-ready-epic",
			"tomb-satisfied",
		},
	}

	got := DetectAtRisk(issues, sp, now, DefaultAtRiskThresholds())
	byID := make(map[string]AtRiskItem, len(got))
	for _, item := range got {
		byID[item.ID] = item
	}

	if len(byID) != 2 {
		t.Fatalf("DetectAtRisk items = %+v, want exactly [child-of-blocked-epic, dangling-blocker]", got)
	}

	childItem, ok := byID["child-of-blocked-epic"]
	if !ok {
		t.Fatalf("missing child-of-blocked-epic in %+v", got)
	}
	wantChildSignals := map[AtRiskSignal]bool{
		AtRiskBlockedTooLong:     true,
		AtRiskCriticalBlocked:    true,
		AtRiskBlockersNotClosing: true,
	}
	for _, sig := range childItem.Signals {
		delete(wantChildSignals, sig)
	}
	if len(wantChildSignals) != 0 {
		t.Fatalf("child-of-blocked-epic signals = %v, missing %v", childItem.Signals, wantChildSignals)
	}
	if !strings.Contains(childItem.Detail, "blocked for 3d") || !strings.Contains(childItem.Detail, "stalled-root") {
		t.Fatalf("child-of-blocked-epic Detail = %q, want 'blocked for 3d' and 'stalled-root'", childItem.Detail)
	}

	danglingItem, ok := byID["dangling-blocker"]
	if !ok {
		t.Fatalf("missing dangling-blocker in %+v", got)
	}
	wantDanglingSignals := map[AtRiskSignal]bool{
		AtRiskBlockedTooLong:  true,
		AtRiskCriticalBlocked: true,
	}
	for _, sig := range danglingItem.Signals {
		delete(wantDanglingSignals, sig)
	}
	if len(wantDanglingSignals) != 0 {
		t.Fatalf("dangling-blocker signals = %v, missing %v", danglingItem.Signals, wantDanglingSignals)
	}
	if !strings.Contains(danglingItem.Detail, "blocked for 3d") {
		t.Fatalf("dangling-blocker Detail = %q, want 'blocked for 3d'", danglingItem.Detail)
	}

	for _, neg := range []string{"child-of-ready-epic", "tomb-satisfied"} {
		if item, exists := byID[neg]; exists {
			t.Fatalf("negative control %s falsely flagged at-risk: %+v", neg, item)
		}
	}
}

func TestDetectAtRisk_ParentChildEpicDoesNotTriggerBlockersNotClosing(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	tenDaysAgo := now.AddDate(0, 0, -10)
	oneHourAgo := now.Add(-time.Hour)

	issues := []model.Issue{
		// Active blocker R updated 1h ago gating 10d-old epic E:
		// E stays open until its children finish, so E's 10d age must NOT fire blockers_not_closing on C.
		{
			ID: "R", Title: "Active blocker", Status: model.StatusOpen,
			Priority: 2, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
		},
		{
			ID: "E", Title: "Ten-day-old parent epic", Status: model.StatusOpen,
			Priority: 1, IssueType: model.TypeEpic, UpdatedAt: tenDaysAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "E", DependsOnID: "R", Type: model.DepBlocks, CreatedAt: oneHourAgo},
			},
		},
		{
			ID: "C", Title: "Child of E with active blocker", Status: model.StatusOpen,
			Priority: 2, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "C", DependsOnID: "E", Type: model.DepParentChild, CreatedAt: oneHourAgo},
			},
		},
		{
			ID: "C-crit", Title: "Critical child of E", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "C-crit", DependsOnID: "E", Type: model.DepParentChild, CreatedAt: oneHourAgo},
			},
		},
		// Planted positive control: 10d-idle blocker R2 gating fresh epic E2 MUST flag C2 with blockers_not_closing.
		{
			ID: "R2", Title: "Stalled blocker", Status: model.StatusOpen,
			Priority: 2, IssueType: model.TypeTask, UpdatedAt: tenDaysAgo,
		},
		{
			ID: "E2", Title: "Parent epic gated by stalled R2", Status: model.StatusOpen,
			Priority: 1, IssueType: model.TypeEpic, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "E2", DependsOnID: "R2", Type: model.DepBlocks, CreatedAt: oneHourAgo},
			},
		},
		{
			ID: "C2", Title: "Child of E2 gated by stalled R2", Status: model.StatusOpen,
			Priority: 2, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "C2", DependsOnID: "E2", Type: model.DepParentChild, CreatedAt: oneHourAgo},
			},
		},
	}

	sp := &model.Sprint{
		ID:      "sprint-parent-epic",
		BeadIDs: []string{"C", "C-crit", "C2"},
	}
	got := DetectAtRisk(issues, sp, now, DefaultAtRiskThresholds())
	byID := make(map[string]AtRiskItem, len(got))
	for _, item := range got {
		byID[item.ID] = item
	}

	if item, flagged := byID["C"]; flagged {
		t.Fatalf("C must NOT be flagged with blockers_not_closing from 10d-old parent epic E, got %+v", item)
	}

	critItem, ok := byID["C-crit"]
	if !ok {
		t.Fatalf("expected C-crit to be flagged critical_blocked, got %+v", got)
	}
	if len(critItem.Signals) != 1 || critItem.Signals[0] != AtRiskCriticalBlocked {
		t.Fatalf("C-crit signals = %v, want only [critical_blocked] (detail=%q)", critItem.Signals, critItem.Detail)
	}
	if !strings.Contains(critItem.Detail, "blocked by R") {
		t.Fatalf("C-crit detail = %q, want 'blocked by R'", critItem.Detail)
	}

	c2Item, ok := byID["C2"]
	if !ok {
		t.Fatalf("expected C2 to be flagged blockers_not_closing from 10d-idle R2, got %+v", got)
	}
	if len(c2Item.Signals) != 1 || c2Item.Signals[0] != AtRiskBlockersNotClosing {
		t.Fatalf("C2 signals = %v, want [blockers_not_closing]", c2Item.Signals)
	}
	if !strings.Contains(c2Item.Detail, "R2") || strings.Contains(c2Item.Detail, "E2") {
		t.Fatalf("C2 detail = %q, want R2 cited and E2 excluded", c2Item.Detail)
	}
}

func TestDetectAtRiskWithReadiness_HonorsTombstoneIDsAndCrossRepoBlockers(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	oneHourAgo := now.Add(-time.Hour)

	repoIssues := []model.Issue{
		{
			ID: "api:tomb-gated", Title: "Depends on stripped tombstone ID", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "api:tomb-gated", DependsOnID: "api:deleted-1", Type: model.DepBlocks, CreatedAt: oneHourAgo},
			},
		},
		{
			ID: "api:xrepo-gated", Title: "Depends on sibling repo open blocker", Status: model.StatusOpen,
			Priority: 0, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
			Dependencies: []*model.Dependency{
				{IssueID: "api:xrepo-gated", DependsOnID: "web:open-1", Type: model.DepBlocks, CreatedAt: oneHourAgo},
			},
		},
	}
	allWorkspaceIssues := append([]model.Issue{}, repoIssues...)
	allWorkspaceIssues = append(allWorkspaceIssues,
		model.Issue{ID: "api:deleted-1", Status: model.StatusTombstone},
		model.Issue{
			ID: "web:open-1", Title: "Open blocker in web repo", Status: model.StatusOpen,
			Priority: 1, IssueType: model.TypeTask, UpdatedAt: oneHourAgo,
		},
	)

	readiness := model.NewReadinessIndex(allWorkspaceIssues)
	sp := &model.Sprint{
		ID:      "sprint-ws",
		BeadIDs: []string{"api:tomb-gated", "api:xrepo-gated"},
	}

	got := DetectAtRiskWithReadiness(repoIssues, sp, readiness, now, DefaultAtRiskThresholds())
	if len(got) != 1 || got[0].ID != "api:xrepo-gated" {
		t.Fatalf("DetectAtRiskWithReadiness = %+v, want only [api:xrepo-gated] (tombstone blocker must be satisfied)", got)
	}
	if !strings.Contains(got[0].Detail, "web:open-1") {
		t.Fatalf("api:xrepo-gated detail = %q, want cross-repo blocker web:open-1", got[0].Detail)
	}
}

func TestComputeSprintBurndown_DailyClosureCurveAndProjection(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) // 10 days total
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) // day 5 elapsed
	closedDay1 := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	closedDay3 := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	sprint := &model.Sprint{
		ID:        "sp-curve",
		Name:      "Curve Sprint",
		StartDate: start,
		EndDate:   end,
		BeadIDs:   []string{"B1", "B2", "B3", "B4"},
	}
	issues := []model.Issue{
		{ID: "B1", Status: model.StatusClosed, ClosedAt: &closedDay1},
		{ID: "B2", Status: model.StatusClosed, ClosedAt: &closedDay3},
		{ID: "B3", Status: model.StatusOpen},
		{ID: "B4", Status: model.StatusOpen},
	}

	bd := ComputeSprintBurndown(sprint, issues, now)
	if bd.TotalDays != 10 || bd.ElapsedDays != 5 || bd.RemainingDays != 5 {
		t.Fatalf("days = (%d,%d,%d), want (10,5,5)", bd.TotalDays, bd.ElapsedDays, bd.RemainingDays)
	}
	if bd.TotalIssues != 4 || bd.CompletedIssues != 2 || bd.RemainingIssues != 2 {
		t.Fatalf("counts = (%d,%d,%d), want (4,2,2)", bd.TotalIssues, bd.CompletedIssues, bd.RemainingIssues)
	}
	if len(bd.DailyPoints) != 5 {
		t.Fatalf("expected 5 daily points, got %d: %+v", len(bd.DailyPoints), bd.DailyPoints)
	}
	wantRem := []int{3, 3, 2, 2, 2}
	for i, want := range wantRem {
		if bd.DailyPoints[i].Remaining != want {
			t.Fatalf("day %d remaining = %d, want %d (all=%+v)", i, bd.DailyPoints[i].Remaining, want, bd.DailyPoints)
		}
	}
	if bd.ProjectedComplete == nil || bd.ActualBurnRate <= 0 || bd.IdealBurnRate <= 0 {
		t.Fatalf("expected positive burn rates and projected completion: %+v", bd)
	}
}
