package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// Build a minimal issue item used across delegate tests.
func newTestIssueItem(id string) IssueItem {
	now := time.Now().Add(-2 * time.Hour) // deterministic-ish age string (e.g. "2h")
	return IssueItem{
		Issue: model.Issue{
			ID:        id,
			Title:     "Short title for testing",
			Status:    model.StatusOpen,
			IssueType: model.TypeFeature,
			Priority:  1,
			Assignee:  "alice",
			Labels:    []string{"one", "two"},
			Comments: []*model.Comment{
				{ID: "1", IssueID: id, Author: "bob", Text: "hello", CreatedAt: now},
			},
			CreatedAt: now,
		},
		DiffStatus: DiffStatusNone,
		RepoPrefix: "",
	}
}

func TestIssueDelegate_RenderWorkspaceWithPriorityHints(t *testing.T) {
	item := newTestIssueItem("api-123")
	item.RepoPrefix = "api"         // exercise workspace badge branch
	item.DiffStatus = DiffStatusNew // exercise diff badge branch
	theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))

	delegate := IssueDelegate{
		Theme:             theme,
		ShowPriorityHints: true,
		PriorityHints: map[string]*analysis.PriorityRecommendation{
			item.Issue.ID: {IssueID: item.Issue.ID, Direction: "increase"},
		},
		WorkspaceMode: true,
	}

	items := []list.Item{item}
	l := list.New(items, delegate, 0, 0)
	l.SetWidth(120) // wide enough to render right-side columns

	var buf bytes.Buffer
	delegate.Render(&buf, l, 0, item)
	out := buf.String()

	if !strings.Contains(out, "api-123") {
		t.Fatalf("render output missing issue id: %q", out)
	}
	if !strings.Contains(out, "↑") {
		t.Fatalf("render output missing priority hint arrow: %q", out)
	}
	if !strings.Contains(out, "[API]") {
		t.Fatalf("render output missing repo badge [API]: %q", out)
	}
	if !strings.Contains(out, "🆕") {
		t.Fatalf("render output missing diff badge for new item: %q", out)
	}
	if !strings.Contains(out, "💬1") {
		t.Fatalf("render output missing comment count badge: %q", out)
	}
}

func TestIssueDelegate_RenderFallsBackWidthAndNoPanic(t *testing.T) {
	item := newTestIssueItem("TASK-1")
	theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))
	delegate := IssueDelegate{Theme: theme}

	l := list.New([]list.Item{item}, delegate, 0, 0) // width defaults to 0 → delegate fallback

	var buf bytes.Buffer
	delegate.Render(&buf, l, 0, item)
	out := buf.String()

	if out == "" {
		t.Fatal("render output should not be empty")
	}
	if !strings.Contains(out, "TASK-1") {
		t.Fatalf("render output missing id after fallback width handling: %q", out)
	}
}

func TestIssueDelegate_RenderUltraWide(t *testing.T) {
	item := newTestIssueItem("WIDE-1")
	// Assignee and Labels require width thresholds >100 and >140
	theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))
	delegate := IssueDelegate{Theme: theme}

	l := list.New([]list.Item{item}, delegate, 0, 0)
	l.SetWidth(160) // Ultra-wide

	var buf bytes.Buffer
	delegate.Render(&buf, l, 0, item)
	out := buf.String()

	if !strings.Contains(out, "@alice") {
		t.Fatalf("ultra-wide output missing assignee @alice: %q", out)
	}
	if !strings.Contains(out, "one,two") { // joined labels
		t.Fatalf("ultra-wide output missing labels 'one,two': %q", out)
	}
}

func TestIssueDelegate_RenderNarrow(t *testing.T) {
	item := newTestIssueItem("NARROW-1")
	theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))
	delegate := IssueDelegate{Theme: theme}

	l := list.New([]list.Item{item}, delegate, 0, 0)
	l.SetWidth(50) // Very narrow

	var buf bytes.Buffer
	delegate.Render(&buf, l, 0, item)
	out := buf.String()

	if !strings.Contains(out, "NARROW-1") {
		t.Fatalf("narrow output missing id: %q", out)
	}
	// Should NOT contain right-side metadata
	if strings.Contains(out, "@alice") {
		t.Fatalf("narrow output should hide assignee: %q", out)
	}
	if strings.Contains(out, "💬") {
		t.Fatalf("narrow output should hide comments count: %q", out)
	}
}

func TestIssueDelegate_RecipeColumnsAndWidth(t *testing.T) {
	item := newTestIssueItem("view-1")
	item.Issue.Title = "界界界界界 Alpha"
	item.Issue.CreatedAt = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	item.Issue.UpdatedAt = item.Issue.CreatedAt.Add(24 * time.Hour)
	want := map[string]string{"id": "view-1", "title": "界界…", "status": "open", "priority": "P1", "created": "2026-01-02", "updated": "2026-01-03", "tags": "one,two", "blockers": "blockers:2"}
	for _, column := range recipe.ViewColumns {
		t.Run(column, func(t *testing.T) {
			d := IssueDelegate{Theme: DefaultTheme(lipgloss.NewRenderer(nil)), Columns: []string{column}, TruncateTitle: 5, BlockerCounts: map[string]int{"view-1": 2}}
			l := list.New([]list.Item{item}, d, 80, 10)
			var out bytes.Buffer
			d.Render(&out, l, 0, item)
			if !strings.Contains(out.String(), want[column]) {
				t.Fatalf("column %s = %q, want %q", column, out.String(), want[column])
			}
		})
	}
	for _, width := range []int{1, 8, 20, 40, 80, 150} {
		d := IssueDelegate{Theme: DefaultTheme(lipgloss.NewRenderer(nil)), Columns: []string{"priority", "title", "id", "updated"}, TruncateTitle: 5}
		l := list.New([]list.Item{item}, d, width, 10)
		var out bytes.Buffer
		d.Render(&out, l, 0, item)
		if actual := lipgloss.Width(out.String()); actual > width {
			t.Errorf("width=%d rendered=%d: %q", width, actual, out.String())
		}
		if width >= 80 {
			row := out.String()
			if !(strings.Index(row, "P1") < strings.Index(row, "界界…") && strings.Index(row, "界界…") < strings.Index(row, "view-1") && strings.Index(row, "view-1") < strings.Index(row, "2026-01-03")) {
				t.Fatalf("configured column order lost: %q", row)
			}
		}
	}
}
