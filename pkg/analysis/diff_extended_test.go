package analysis

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TestDiff_TextFieldChanges tests detection of changes in text fields
// like Description, Design, AcceptanceCriteria, and Notes
func TestDiff_TextFieldChanges(t *testing.T) {
	fromIssues := []model.Issue{
		{
			ID:                 "A",
			Title:              "Issue A",
			Description:        "Original description",
			Design:             "Original design",
			AcceptanceCriteria: "Original AC",
			Notes:              "Original notes",
			Status:             model.StatusOpen,
		},
	}
	toIssues := []model.Issue{
		{
			ID:                 "A",
			Title:              "Issue A",
			Description:        "Updated description",
			Design:             "Updated design",
			AcceptanceCriteria: "Updated AC",
			Notes:              "Updated notes",
			Status:             model.StatusOpen,
		},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("Expected 1 modified issue, got %d", len(diff.ModifiedIssues))
	}

	mod := diff.ModifiedIssues[0]
	changes := make(map[string]bool)
	for _, c := range mod.Changes {
		changes[c.Field] = true
	}

	if !changes["description"] {
		t.Error("Description change not detected")
	}
	if !changes["design"] {
		t.Error("Design change not detected")
	}
	if !changes["acceptance_criteria"] {
		t.Error("AcceptanceCriteria change not detected")
	}
	if !changes["notes"] {
		t.Error("Notes change not detected")
	}
}

// TestDiff_DependencyChanges tests detection of added/removed dependencies
func TestDiff_DependencyChanges(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("Expected 1 modified issue, got %d", len(diff.ModifiedIssues))
	}

	mod := diff.ModifiedIssues[0]
	if mod.IssueID != "A" {
		t.Errorf("Expected modification in A, got %s", mod.IssueID)
	}

	foundDepChange := false
	for _, c := range mod.Changes {
		if c.Field == "dependencies" {
			foundDepChange = true
			break
		}
	}
	if !foundDepChange {
		t.Error("Dependency change not detected")
	}
}

// TestDiff_LabelChanges tests detection of added/removed labels
func TestDiff_LabelChanges(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "A", Labels: []string{"bug"}},
	}
	toIssues := []model.Issue{
		{ID: "A", Labels: []string{"bug", "urgent"}},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("Expected 1 modified issue, got %d", len(diff.ModifiedIssues))
	}

	mod := diff.ModifiedIssues[0]
	foundLabelChange := false
	for _, c := range mod.Changes {
		if c.Field == "labels" {
			foundLabelChange = true
			break
		}
	}
	if !foundLabelChange {
		t.Error("Label change not detected")
	}
}

// TestDiff_ComplexScenario tests a realistic scenario with multiple changes
func TestDiff_ComplexScenario(t *testing.T) {
	ts := time.Now()
	fromIssues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Task B", Status: model.StatusBlocked},
		{ID: "C", Title: "Task C", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusClosed, Priority: 1}, // Closed
		{ID: "B", Title: "Task B", Status: model.StatusOpen},                // Unblocked
		// C removed
		{ID: "D", Title: "Task D", Status: model.StatusOpen}, // Added
	}

	from := NewSnapshotAt(fromIssues, ts.Add(-1*time.Hour), "rev1")
	to := NewSnapshotAt(toIssues, ts, "rev2")
	diff := CompareSnapshots(from, to)

	// Check counts
	if len(diff.ClosedIssues) != 1 || diff.ClosedIssues[0].ID != "A" {
		t.Error("Failed to detect closed issue A")
	}
	if len(diff.NewIssues) != 1 || diff.NewIssues[0].ID != "D" {
		t.Error("Failed to detect new issue D")
	}
	if len(diff.RemovedIssues) != 1 || diff.RemovedIssues[0].ID != "C" {
		t.Error("Failed to detect removed issue C")
	}
	if len(diff.ModifiedIssues) != 1 || diff.ModifiedIssues[0].IssueID != "B" {
		t.Errorf("Expected status change on B, got %d mods", len(diff.ModifiedIssues))
	}

	// Check summary stats
	if diff.Summary.IssuesAdded != 1 {
		t.Errorf("Summary: expected 1 added, got %d", diff.Summary.IssuesAdded)
	}
	if diff.Summary.IssuesClosed != 1 {
		t.Errorf("Summary: expected 1 closed, got %d", diff.Summary.IssuesClosed)
	}
	if diff.Summary.IssuesRemoved != 1 {
		t.Errorf("Summary: expected 1 removed, got %d", diff.Summary.IssuesRemoved)
	}
	if diff.Summary.IssuesModified != 1 {
		t.Errorf("Summary: expected 1 modified, got %d", diff.Summary.IssuesModified)
	}

	// Net change: +1 added, -1 removed = 0
	if diff.Summary.NetIssueChange != 0 {
		t.Errorf("Summary: expected net change 0, got %d", diff.Summary.NetIssueChange)
	}
}
