package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func attentionFixture(n int) analysis.LabelAttentionResult {
	now := time.Now().UTC()
	var issues []model.Issue
	for i := 1; i <= n; i++ {
		label := "l" + pad2(i)
		issues = append(issues, model.Issue{
			ID:        "ISSUE-" + label,
			Title:     "Issue " + label,
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Priority:  2,
			Labels:    []string{label},
			CreatedAt: now.Add(-24 * time.Hour),
			UpdatedAt: now.Add(-1 * time.Hour),
		})
	}
	return analysis.ComputeLabelAttentionScores(issues, analysis.DefaultLabelHealthConfig(), now)
}

func TestAttentionModel_EmptyRendersHeaderOnly(t *testing.T) {
	m := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(80, 10)
	out := m.View()
	if !strings.Contains(out, "Rank") || !strings.Contains(out, "Label") || !strings.Contains(out, "Attention") || !strings.Contains(out, "Reason") {
		t.Fatalf("expected header columns, got:\n%s", out)
	}
	if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != 1 {
		t.Fatalf("expected header only, got %d lines:\n%s", len(lines), out)
	}
	if m.SelectedLabel() != "" || m.Len() != 0 {
		t.Fatalf("empty model must have no selection")
	}
	if label, handled := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); label != "" || !handled {
		t.Fatalf("enter on empty view: label=%q handled=%v", label, handled)
	}
}

func TestAttentionModel_HeaderRespectsWidth(t *testing.T) {
	m := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(80, 10)
	line := strings.Split(m.View(), "\n")[0]
	if got := runewidth.StringWidth(line); got != 80 {
		t.Fatalf("expected header width 80, got %d: %q", got, line)
	}
}

func TestAttentionModel_LimitsToTop10AndIsDeterministic(t *testing.T) {
	m := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(120, 20)
	m.SetData(attentionFixture(11))
	if m.Len() != 10 {
		t.Fatalf("expected top 10 labels, got %d", m.Len())
	}
	out := m.View()
	if !strings.Contains(out, "l01") || !strings.Contains(out, "l10") || strings.Contains(out, "l11") {
		t.Fatalf("expected l01..l10 and not l11:\n%s", out)
	}
	if !strings.Contains(out, "pr=") || !strings.Contains(out, "closed30=") {
		t.Fatalf("reason column must show the formula components:\n%s", out)
	}
	again := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	again.SetSize(120, 20)
	again.SetData(attentionFixture(11))
	if again.View() != out {
		t.Fatal("render is not deterministic for the same input")
	}
}

func TestAttentionModel_NavigationAndEnter(t *testing.T) {
	m := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(100, 4) // header + 3 visible rows forces scrolling
	m.SetData(attentionFixture(6))

	key := func(s string) (string, bool) {
		if s == "enter" {
			return m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		}
		return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	}
	if m.Cursor() != 0 {
		t.Fatalf("cursor starts at 0, got %d", m.Cursor())
	}
	key("j")
	key("j")
	if m.Cursor() != 2 {
		t.Fatalf("after j j cursor = %d, want 2", m.Cursor())
	}
	key("k")
	if m.Cursor() != 1 {
		t.Fatalf("after k cursor = %d, want 1", m.Cursor())
	}
	key("G")
	if m.Cursor() != 5 {
		t.Fatalf("after G cursor = %d, want 5", m.Cursor())
	}
	// The selected (last) row must be visible after scrolling.
	if !strings.Contains(m.View(), m.SelectedLabel()) {
		t.Fatalf("selected label %q not rendered after scroll:\n%s", m.SelectedLabel(), m.View())
	}
	key("g")
	if m.Cursor() != 0 {
		t.Fatalf("after g cursor = %d, want 0", m.Cursor())
	}
	label, handled := key("enter")
	if !handled || label != m.LabelAt(0) {
		t.Fatalf("enter must return the selected label: got %q handled=%v", label, handled)
	}
	if _, handled := key("x"); handled {
		t.Fatal("unknown keys must not be consumed")
	}
}

func TestAttentionModel_TruncatesCells(t *testing.T) {
	now := time.Now().UTC()
	longLabel := "this-is-a-very-very-long-label-name"
	issues := []model.Issue{{
		ID: "A", Title: "A", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2,
		Labels: []string{longLabel}, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour),
	}}
	m := NewAttentionModel(DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(40, 5)
	m.SetData(analysis.ComputeLabelAttentionScores(issues, analysis.DefaultLabelHealthConfig(), now))
	out := m.View()
	if !strings.Contains(out, truncate(longLabel, 18)) {
		t.Fatalf("expected truncated label in output:\n%s", out)
	}
	if strings.Contains(out, longLabel) {
		t.Fatalf("label must be truncated to its column:\n%s", out)
	}
}

// Model integration: ] opens the attention view as a focus, Enter opens the
// label drilldown, 1-9 filter the list by rank, and ]/Esc return to the list.
func TestModel_AttentionViewFocusDrilldownAndFilter(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "A", Title: "Alpha", Status: model.StatusOpen, Priority: 1, Labels: []string{"backend"}, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "B", Title: "Beta", Status: model.StatusOpen, Priority: 2, Labels: []string{"frontend"}, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour)},
	}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 30

	press := func(s string) {
		var msg tea.KeyMsg
		switch s {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
		}
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	press("]")
	if m.focused != focusAttention {
		t.Fatalf("] must focus the attention view, got %v", m.focused)
	}
	if m.attentionView.Len() != 2 {
		t.Fatalf("expected 2 ranked labels, got %d", m.attentionView.Len())
	}
	if !strings.Contains(m.View(), "Rank") {
		t.Fatalf("attention view not rendered:\n%s", m.View())
	}

	press("j")
	selected := m.attentionView.SelectedLabel()
	press("enter")
	if !m.showLabelDrilldown || m.labelDrilldownLabel != selected {
		t.Fatalf("enter must open the drilldown for %q, got show=%v label=%q", selected, m.showLabelDrilldown, m.labelDrilldownLabel)
	}
	m.showLabelDrilldown = false

	press("1")
	if !strings.HasPrefix(m.currentFilter, "label:") {
		t.Fatalf("1 must filter by the top label, got filter %q", m.currentFilter)
	}

	press("]")
	if m.focused != focusList {
		t.Fatalf("] must return to the list, got %v", m.focused)
	}
	press("]")
	press("esc")
	if m.focused != focusList {
		t.Fatalf("esc must return to the list, got %v", m.focused)
	}
}

func TestModel_AttentionViewStyledCursorAndPinnedClock(t *testing.T) {
	pinnedNow := time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC)
	closedNearPinned := pinnedNow.Add(-5 * 24 * time.Hour)

	issues := []model.Issue{
		{
			ID:        "PINNED-CLOSED",
			Title:     "Closed near pinned reference clock",
			Status:    model.StatusClosed,
			Priority:  1,
			Labels:    []string{"alpha"},
			CreatedAt: pinnedNow.Add(-20 * 24 * time.Hour),
			UpdatedAt: closedNearPinned,
			ClosedAt:  &closedNearPinned,
		},
		{
			ID:        "PINNED-OPEN-1",
			Title:     "Open alpha issue",
			Status:    model.StatusOpen,
			Priority:  1,
			Labels:    []string{"alpha"},
			CreatedAt: pinnedNow.Add(-10 * 24 * time.Hour),
			UpdatedAt: pinnedNow.Add(-2 * 24 * time.Hour),
		},
		{
			ID:        "PINNED-OPEN-2",
			Title:     "Open beta issue",
			Status:    model.StatusOpen,
			Priority:  2,
			Labels:    []string{"beta"},
			CreatedAt: pinnedNow.Add(-3 * 24 * time.Hour),
			UpdatedAt: pinnedNow.Add(-1 * 24 * time.Hour),
		},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.analyzer.SetNow(pinnedNow)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Open attention view via ']'
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	m = updated.(*Model)
	if m.focused != focusAttention {
		t.Fatalf("expected focusAttention, got %v", m.focused)
	}

	// Verify NewModel wired DefaultTheme into m.attentionView so the selected row
	// renders the thick left selection border ('┃') and respects mainContentWidth.
	rawView := m.attentionView.View()
	lines := strings.Split(strings.TrimSuffix(rawView, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines:\n%s", len(lines), rawView)
	}
	if !strings.Contains(lines[1], "┃") || strings.Contains(lines[2], "┃") {
		t.Fatalf("expected row 1 to carry Selected '┃' border and row 2 not to:\nrow1=%q\nrow2=%q", lines[1], lines[2])
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.mainContentWidth() {
			t.Fatalf("line %d width %d exceeds mainContentWidth %d: %q", i, w, m.mainContentWidth(), line)
		}
	}

	// Verify pinned reference clock: closedNearPinned is within 30 days of pinnedNow
	// (2025-03-15), so alpha's reason must report closed30=1 rather than 0 from wall-clock time.Now().
	if !strings.Contains(rawView, "closed30=1") {
		t.Fatalf("expected refreshAttentionView to use pinned analysisReferenceTime (closed30=1), got:\n%s", rawView)
	}

	// Move cursor down with 'j' and verify selection highlight moves to row 2.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = updated.(*Model)
	movedLines := strings.Split(strings.TrimSuffix(m.attentionView.View(), "\n"), "\n")
	if strings.Contains(movedLines[1], "┃") || !strings.Contains(movedLines[2], "┃") {
		t.Fatalf("expected '┃' cursor highlight to move to row 2:\nrow1=%q\nrow2=%q", movedLines[1], movedLines[2])
	}
}

func pad2(i int) string {
	return fmt.Sprintf("%02d", i)
}
