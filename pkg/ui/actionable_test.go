package ui

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"

	"github.com/charmbracelet/lipgloss"
)

func newTestTheme() Theme {
	return DefaultTheme(lipgloss.NewRenderer(nil))
}

func TestActionableRenderEmpty(t *testing.T) {
	m := NewActionableModel(analysis.ExecutionPlan{}, newTestTheme())
	m.SetSize(80, 20)

	out := m.Render()
	if !strings.Contains(out, "No actionable items") {
		t.Fatalf("expected empty state message, got:\n%s", out)
	}
}

func TestActionableNavigationAcrossTracks(t *testing.T) {
	plan := analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{
			{TrackID: "track-A", Items: []analysis.PlanItem{{ID: "A1", Title: "First"}}},
			{TrackID: "track-B", Items: []analysis.PlanItem{{ID: "B1", Title: "Second"}}},
		},
	}

	m := NewActionableModel(plan, newTestTheme())
	m.SetSize(80, 20)

	if got := m.SelectedIssueID(); got != "A1" {
		t.Fatalf("expected initial selection A1, got %s", got)
	}

	m.MoveDown() // should move to next track/item
	if got := m.SelectedIssueID(); got != "B1" {
		t.Fatalf("expected selection B1 after MoveDown, got %s", got)
	}

	m.MoveUp()
	if got := m.SelectedIssueID(); got != "A1" {
		t.Fatalf("expected selection back to A1 after MoveUp, got %s", got)
	}
}

func TestActionableRenderShowsSummary(t *testing.T) {
	plan := analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{
			{
				TrackID: "track-A",
				Items:   []analysis.PlanItem{{ID: "ROOT", Title: "Root", Priority: 1, UnblocksIDs: []string{"X", "Y"}}},
			},
		},
		Summary: analysis.PlanSummary{
			HighestImpact: "ROOT",
			ImpactReason:  "Unblocks multiple tasks",
			UnblocksCount: 2,
		},
	}

	m := NewActionableModel(plan, newTestTheme())
	m.SetSize(100, 30)

	out := m.Render()
	if !strings.Contains(out, "Start with ROOT") {
		t.Fatalf("expected summary callout for ROOT, got:\n%s", out)
	}
	if !strings.Contains(out, "→2") {
		t.Fatalf("expected unblocks count badge, got:\n%s", out)
	}
}
