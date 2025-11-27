package analysis

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// loadSampleIssues loads the shared fixture used for integration-style tests.
// Skips the test if the fixture file doesn't exist (e.g., in CI).
func loadSampleIssues(t *testing.T) []model.Issue {
	t.Helper()
	path := filepath.Join("..", "..", "beads_reference", ".beads", "beads.jsonl")
	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Skipf("skipping: sample fixture not available (%v)", err)
	}
	return issues
}

func TestExecutionPlan_OnSampleFixture(t *testing.T) {
	issues := loadSampleIssues(t)
	an := NewAnalyzer(issues)

	plan := an.GetExecutionPlan()

	// From real beads_reference dataset: 19 actionable, 4 blocked
	if plan.TotalActionable != 19 {
		t.Fatalf("expected 19 actionable issues, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 4 {
		t.Fatalf("expected 4 blocked issues, got %d", plan.TotalBlocked)
	}

	// Spot-check a known actionable issue
	wantIDs := []string{"bd-1pj6"}
	var got []string
	for _, tr := range plan.Tracks {
		for _, it := range tr.Items {
			got = append(got, it.ID)
		}
	}

	for _, id := range wantIDs {
		if !slices.Contains(got, id) {
			t.Fatalf("expected actionable item %s in plan, got %v", id, got)
		}
	}
}

func TestSnapshotDiff_OnSampleFixture(t *testing.T) {
	fromIssues := loadSampleIssues(t)
	var toIssues []model.Issue
	for _, iss := range loadSampleIssues(t) {
		switch iss.ID {
		case "bd-1pj6": // close an open issue
			iss.Status = model.StatusClosed
		case "bd-3gc": // change title
			iss.Title = iss.Title + " (updated)"
		case "bd-bt6y": // remove from new snapshot
			continue
		}
		toIssues = append(toIssues, iss)
	}
	toIssues = append(toIssues,
		model.Issue{ID: "bd-new", Title: "New item", Status: model.StatusOpen, Priority: 1},
	)

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.NewIssues) != 1 || diff.NewIssues[0].ID != "bd-new" {
		t.Fatalf("expected one new issue 'bd-new', got %+v", diff.NewIssues)
	}
	if len(diff.ClosedIssues) != 1 || diff.ClosedIssues[0].ID != "bd-1pj6" {
		t.Fatalf("expected bd-1pj6 closed, got %+v", diff.ClosedIssues)
	}
	if len(diff.RemovedIssues) != 1 || diff.RemovedIssues[0].ID != "bd-bt6y" {
		t.Fatalf("expected bd-bt6y removed, got %+v", diff.RemovedIssues)
	}
	// bd-3gc is modified. bd-1pj6 is closed (not in modified).
	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("expected one modified issue (title), got %d: %+v", len(diff.ModifiedIssues), diff.ModifiedIssues)
	}
}

func TestGraphMetrics_OnSampleFixture(t *testing.T) {
	issues := loadSampleIssues(t)
	an := NewAnalyzer(issues)
	stats := an.Analyze()

	// Known dependency: bd-4h3 depends on bd-ge7; ensure InDegree/OutDegree reflect it
	if stats.InDegree["bd-ge7"] == 0 {
		t.Fatalf("expected bd-ge7 to have dependents (in-degree > 0)")
	}
	if stats.OutDegree["bd-4h3"] == 0 {
		t.Fatalf("expected bd-4h3 to have out-degree (depends on bd-ge7)")
	}

	// PageRank map should contain all issue IDs
	if len(stats.PageRank) != len(issues) {
		t.Fatalf("pagerank entries (%d) != issue count (%d)", len(stats.PageRank), len(issues))
	}
}

func TestActionableTracksContainRealIDs(t *testing.T) {
	issues := loadSampleIssues(t)
	an := NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// bd-9e23 (open chore) is actionable in the reference set; ensure it appears
	found := false
	for _, tr := range plan.Tracks {
		for _, it := range tr.Items {
			if it.ID == "bd-9e23" {
				found = true
				if it.Status != "open" {
					t.Fatalf("expected bd-9e23 status open in plan, got %s", it.Status)
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected actionable plan to include bd-9e23")
	}
}
