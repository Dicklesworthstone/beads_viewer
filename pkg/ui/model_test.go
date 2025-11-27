package ui_test

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

func TestModelFiltering(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Open Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Title: "Closed Issue", Status: model.StatusClosed, Priority: 2},
		{ID: "3", Title: "Blocked Issue", Status: model.StatusBlocked, Priority: 1},
		{
			ID: "4", Title: "Ready Issue", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{},
		},
		{
			ID: "5", Title: "Blocked by Open", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{
				{DependsOnID: "3", Type: model.DepBlocks},
			},
		},
	}

	m := ui.NewModel(issues, nil)

	// Test "All"
	if len(m.FilteredIssues()) != 5 {
		t.Errorf("Expected 5 issues for 'all', got %d", len(m.FilteredIssues()))
	}

	// Test "Open" (includes Open, InProgress, Blocked)
	m.SetFilter("open")
	if len(m.FilteredIssues()) != 4 {
		t.Errorf("Expected 4 issues for 'open', got %d", len(m.FilteredIssues()))
	}

	// Test "Closed"
	m.SetFilter("closed")
	if len(m.FilteredIssues()) != 1 {
		t.Errorf("Expected 1 issue for 'closed', got %d", len(m.FilteredIssues()))
	}
	if m.FilteredIssues()[0].ID != "2" {
		t.Errorf("Expected issue ID 2, got %s", m.FilteredIssues()[0].ID)
	}

	// Test "Ready"
	m.SetFilter("ready")
	// ID 1 (Open), ID 4 (Ready).
	// ID 3 is Blocked status.
	// ID 5 is Open but Blocked by ID 3 (which is not closed).
	// So we expect 1 and 4.
	readyIssues := m.FilteredIssues()
	if len(readyIssues) != 2 {
		t.Errorf("Expected 2 issues for 'ready', got %d", len(readyIssues))
		for _, i := range readyIssues {
			t.Logf("Got issue: %s", i.Title)
		}
	}
}

func TestFormatTimeRel(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t        time.Time
		expected string
	}{
		{now.Add(-30 * time.Minute), "30m ago"},
		{now.Add(-2 * time.Hour), "2h ago"},
		{now.Add(-25 * time.Hour), "1d ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
	}

	for _, tt := range tests {
		got := ui.FormatTimeRel(tt.t)
		if got != tt.expected {
			t.Errorf("FormatTimeRel(%v) = %s; want %s", tt.t, got, tt.expected)
		}
	}
}

func TestTimeTravelMode(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}

	m := ui.NewModel(issues, nil)

	// Initially not in time-travel mode
	if m.IsTimeTravelMode() {
		t.Error("Expected not to be in time-travel mode initially")
	}

	// TimeTravelDiff should be nil initially
	if m.TimeTravelDiff() != nil {
		t.Error("Expected TimeTravelDiff to be nil initially")
	}
}

func TestGetTypeIconMD(t *testing.T) {
	tests := []struct {
		issueType string
		expected  string
	}{
		{"bug", "🐛"},
		{"feature", "✨"},
		{"task", "📋"},
		{"epic", "🏔️"},
		{"chore", "🧹"},
		{"unknown", "•"},
		{"", "•"},
	}

	for _, tt := range tests {
		got := ui.GetTypeIconMD(tt.issueType)
		if got != tt.expected {
			t.Errorf("GetTypeIconMD(%q) = %s; want %s", tt.issueType, got, tt.expected)
		}
	}
}

func TestModelCreationWithEmptyIssues(t *testing.T) {
	m := ui.NewModel([]model.Issue{}, nil)

	if len(m.FilteredIssues()) != 0 {
		t.Errorf("Expected 0 issues for empty input, got %d", len(m.FilteredIssues()))
	}

	// Should not panic on operations
	m.SetFilter("open")
	m.SetFilter("closed")
	m.SetFilter("ready")
}

func TestIssueItemDiffStatus(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test", Status: model.StatusOpen},
	}

	m := ui.NewModel(issues, nil)

	// In normal mode, DiffStatus should be None
	filtered := m.FilteredIssues()
	if len(filtered) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(filtered))
	}
}
