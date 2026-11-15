package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/lipgloss"
)

func TestNewVelocityComparisonModel(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)

	if m.cursor != 0 {
		t.Errorf("Expected cursor 0, got %d", m.cursor)
	}
	if len(m.data) != 0 {
		t.Errorf("Expected empty data, got %d items", len(m.data))
	}
}

func TestVelocityComparisonSetData(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "issue-1", Title: "Issue 1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-1 * 24 * time.Hour))},
		{ID: "issue-2", Title: "Issue 2", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-2 * 24 * time.Hour))},
		{ID: "issue-3", Title: "Issue 3", Labels: []string{"frontend"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-3 * 24 * time.Hour))},
		{ID: "issue-4", Title: "Issue 4", Labels: []string{"api"}, Status: model.StatusOpen},
		{ID: "issue-5", Title: "Issue 5", Labels: []string{"backend"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-14 * 24 * time.Hour))},
	}

	m.SetData(issues)

	// Should have labels sorted by average velocity descending
	if len(m.data) == 0 {
		t.Error("Expected data to be populated")
	}
}

func TestVelocityComparisonNavigation(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "issue-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-1 * 24 * time.Hour))},
		{ID: "issue-2", Labels: []string{"frontend"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-2 * 24 * time.Hour))},
		{ID: "issue-3", Labels: []string{"backend"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-3 * 24 * time.Hour))},
	}

	m.SetData(issues)

	initialCursor := m.cursor

	// Move down
	m.MoveDown()
	if m.cursor <= initialCursor {
		t.Error("Expected cursor to move down")
	}

	// Move up should return to initial
	m.MoveUp()
	if m.cursor != initialCursor {
		t.Errorf("Expected cursor %d, got %d", initialCursor, m.cursor)
	}

	// Move up at top should stay at top
	m.MoveUp()
	if m.cursor < 0 {
		t.Error("Cursor should not be negative")
	}
}

func TestVelocityComparisonSetDataClampsStaleScrollOffset(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)
	m.SetSize(80, 8) // One visible data row (height 8 - 7 chrome lines).

	now := time.Now().UTC()
	var many []model.Issue
	for i, label := range []string{"api", "backend", "cli", "docs", "frontend", "infra"} {
		closedAt := now.Add(-time.Duration(i+1) * time.Hour)
		many = append(many, model.Issue{
			ID:       label,
			Labels:   []string{label},
			Status:   model.StatusClosed,
			ClosedAt: &closedAt,
		})
	}
	m.SetData(many)
	for range many {
		m.MoveDown()
	}
	if m.scrollOffset == 0 {
		t.Fatal("test setup did not create a non-zero scroll offset")
	}

	closedAt := now.Add(-time.Hour)
	m.SetData([]model.Issue{{
		ID:       "api-only",
		Labels:   []string{"api"},
		Status:   model.StatusClosed,
		ClosedAt: &closedAt,
	}})

	if m.cursor != 0 || m.scrollOffset != 0 {
		t.Fatalf("refresh coordinates = cursor %d, offset %d; want 0, 0", m.cursor, m.scrollOffset)
	}
	if view := m.View(); !containsString(view, "api") {
		t.Fatalf("refreshed non-empty dataset was not rendered:\n%s", view)
	}
}

func TestVelocityComparisonResizeKeepsCursorVisible(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)
	m.SetSize(80, 13) // Six visible data rows (height 13 - 7 chrome lines).

	now := time.Now().UTC()
	var issues []model.Issue
	for i, label := range []string{"api", "backend", "cli", "docs", "frontend", "infra"} {
		closedAt := now.Add(-time.Duration(i+1) * time.Hour)
		issues = append(issues, model.Issue{
			ID:       label,
			Labels:   []string{label},
			Status:   model.StatusClosed,
			ClosedAt: &closedAt,
		})
	}
	m.SetData(issues)
	for range issues {
		m.MoveDown()
	}
	if m.cursor != len(issues)-1 || m.scrollOffset != 0 {
		t.Fatalf("test setup = cursor %d, offset %d", m.cursor, m.scrollOffset)
	}

	m.SetSize(80, 8) // One visible data row.
	if got, want := m.scrollOffset, m.cursor; got != want {
		t.Fatalf("resize offset = %d, want cursor %d to remain visible", got, want)
	}
}

func TestVelocityComparisonSelectedLabel(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)

	// Empty data
	if selected := m.SelectedLabel(); selected != "" {
		t.Errorf("Expected empty string for empty data, got %q", selected)
	}

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "issue-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-1 * 24 * time.Hour))},
	}

	m.SetData(issues)

	if selected := m.SelectedLabel(); selected == "" {
		t.Error("Expected a label to be selected")
	}
}

func TestVelocityComparisonView(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Base:      lipgloss.NewStyle(),
	}
	m := NewVelocityComparisonModel(theme)
	m.SetSize(80, 24)

	view := m.View()
	if view == "" {
		t.Error("Expected non-empty view")
	}

	// Should contain title
	if !containsString(view, "Velocity Comparison") {
		t.Error("Expected view to contain title")
	}

	// Should show no data message when empty
	if !containsString(view, "No velocity data") {
		t.Error("Expected view to show no data message")
	}
}

func TestVelocityComparisonWithData(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Base:      lipgloss.NewStyle(),
	}
	m := NewVelocityComparisonModel(theme)
	m.SetSize(80, 24)

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "issue-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-1 * 24 * time.Hour))},
		{ID: "issue-2", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-8 * 24 * time.Hour))},
	}

	m.SetData(issues)

	view := m.View()
	if view == "" {
		t.Error("Expected non-empty view")
	}

	// Should contain header
	if !containsString(view, "Label") {
		t.Error("Expected view to contain Label header")
	}
}

func TestBuildSparkline(t *testing.T) {
	tests := []struct {
		name    string
		values  []int
		maxVal  int
		wantLen int
	}{
		{
			name:    "empty values",
			values:  []int{0, 0, 0, 0},
			maxVal:  0,
			wantLen: 4, // 4 spaces
		},
		{
			name:    "uniform values",
			values:  []int{5, 5, 5, 5},
			maxVal:  5,
			wantLen: 4,
		},
		{
			name:    "increasing values",
			values:  []int{1, 2, 3, 4},
			maxVal:  4,
			wantLen: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSparkline(tt.values, tt.maxVal)
			// Sparkline should have correct number of characters
			if len([]rune(result)) != tt.wantLen {
				t.Errorf("Expected %d runes, got %d", tt.wantLen, len([]rune(result)))
			}
		})
	}
}

func TestVelocityComparisonDataCount(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	m := NewVelocityComparisonModel(theme)

	if count := m.DataCount(); count != 0 {
		t.Errorf("Expected 0, got %d", count)
	}

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "issue-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-1 * 24 * time.Hour))},
		{ID: "issue-2", Labels: []string{"frontend"}, Status: model.StatusClosed, ClosedAt: timePtr(now.Add(-2 * 24 * time.Hour))},
	}

	m.SetData(issues)

	count := m.DataCount()
	if count != 2 {
		t.Errorf("Expected 2 labels, got %d", count)
	}
}

// Helper function
func timePtr(t time.Time) *time.Time {
	return &t
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || containsString(s[1:], substr)))
}

func TestVelocityComparison24RowNoFooterClip(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	vc := NewVelocityComparisonModel(theme)
	vc.SetSize(100, 18) // 24-row terminal passes inner modal height ~18 to VelocityComparisonModel

	now := time.Now().UTC()
	var issues []model.Issue
	for i := 0; i < 20; i++ {
		lbl := "label-" + string(rune('a'+i))
		closedAt := now.Add(-time.Duration(i+1) * 24 * time.Hour)
		issues = append(issues, model.Issue{
			ID:       "V-" + lbl,
			Labels:   []string{lbl},
			Status:   model.StatusClosed,
			ClosedAt: &closedAt,
		})
	}
	vc.SetData(issues)

	for step := 0; step < 19; step++ {
		vc.MoveDown()
	}
	out := vc.View()
	lines := strings.Split(out, "\n")
	if len(lines) > 18 {
		t.Fatalf("expected VelocityComparisonModel.View() <= 18 lines on 24-row terminal, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "> label-t") {
		t.Fatalf("expected final selected row '> label-t' visible after scrolling to bottom:\n%s", out)
	}
	if !strings.Contains(out, "esc: back") {
		t.Fatalf("expected footer 'esc: back' unclipped on 24-row terminal:\n%s", out)
	}
}
