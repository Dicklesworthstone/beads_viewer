package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/x/ansi"
)

// layoutFitTestIssues is sidebarViewTestIssues with longer labels, so board
// cards carry a blocker badge, a blocks count and three labels on their
// third line: wider than a card at typical column widths.
func layoutFitTestIssues() []model.Issue {
	issues := sidebarViewTestIssues()
	for i := range issues {
		issues[i].Labels = []string{"critical", "data-integrity", "gh-issue", "product"}
	}
	return issues
}

// TestViewsFitTerminalWithoutSidebar: with the shortcuts sidebar off (the
// default), each view must draw inside the rows above the footer, so the
// footer stays on the last row. Board (120x40) and insights (80x24; ~67 rows
// in a 30-row terminal) used to overflow and push the footer off-screen.
func TestViewsFitTerminalWithoutSidebar(t *testing.T) {
	views := []struct {
		name      string
		key       string
		wantFocus focus
		// strict: the view itself must fit (not merely be clipped by the
		// safety clamp in renderBody).
		strict bool
	}{
		{"list", "", focusList, true},
		{"board", "b", focusBoard, true},
		{"graph", "g", focusGraph, false},
		{"insights", "i", focusInsights, true},
		{"actionable", "a", focusActionable, false},
		{"history", "h", focusHistory, false},
		{"tree", "E", focusTree, true},
		{"label_dashboard", "[", focusLabelDashboard, false},
		{"attention", "]", focusAttention, false},
		{"flow_matrix", "f", focusFlowMatrix, false},
	}
	sizes := []struct{ w, h int }{
		{220, 60}, {160, 45}, {120, 40}, {100, 30}, {80, 30}, {80, 24}, {60, 20},
	}
	for _, sz := range sizes {
		for _, v := range views {
			t.Run(fmt.Sprintf("%s_%dx%d", v.name, sz.w, sz.h), func(t *testing.T) {
				m := sizedModel(t, layoutFitTestIssues(), sz.w, sz.h)
				if v.key != "" {
					m = sendRunes(t, m, v.key)
				}
				if m.focused != v.wantFocus {
					t.Fatalf("key %q: focused=%v, want %v", v.key, m.focused, v.wantFocus)
				}
				if m.sidebarVisible() {
					t.Fatalf("sidebar should be off by default")
				}
				if v.strict {
					raw, overlay := m.renderMainView()
					if overlay {
						t.Fatalf("unexpected overlay")
					}
					if n := len(strings.Split(raw, "\n")); n > m.height-1 {
						t.Errorf("view draws %d rows, more than the %d above the footer", n, m.height-1)
					}
					if sz.w >= 80 {
						if w := maxLineWidthOf(raw); w > m.width {
							t.Errorf("view draws %d cells wide in a %d-wide terminal", w, m.width)
						}
					}
				}
				assertViewKeepsFooter(t, m)
			})
		}
	}
}

// TestInsightsKeepsFocusedPanelVisibleWhenShort: when not all four panel rows
// fit, the rows shown must follow the focused panel.
func TestInsightsKeepsFocusedPanelVisibleWhenShort(t *testing.T) {
	m := sizedModel(t, layoutFitTestIssues(), 100, 24)
	m = sendRunes(t, m, "i")
	for p := MetricPanel(0); p < PanelCount; p++ {
		m.insightsPanel.focusedPanel = p
		body := ansi.Strip(m.renderBody())
		title := metricDescriptions[p].Title
		if p == PanelPriority {
			title = metricDescriptions[PanelPriority].Title
		}
		if !strings.Contains(body, title) {
			t.Errorf("focused panel %q not visible at 100x24:\n%s", title, body)
		}
		assertViewKeepsFooter(t, m)
	}
}

func TestInsightsRowLayout(t *testing.T) {
	cases := []struct {
		height, focusedRow       int
		rows, first, panelHeight int
	}{
		{58, 0, 4, 0, 12},
		{43, 3, 4, 0, 8},
		{22, 0, 3, 0, 5},
		{22, 3, 3, 1, 5},
		{13, 2, 1, 2, 11},
		{3, 1, 1, 1, 1},
	}
	for _, tc := range cases {
		rows, first, ph := insightsRowLayout(tc.height, tc.focusedRow)
		if rows != tc.rows || first != tc.first || ph != tc.panelHeight {
			t.Errorf("insightsRowLayout(%d, %d) = (%d, %d, %d), want (%d, %d, %d)",
				tc.height, tc.focusedRow, rows, first, ph, tc.rows, tc.first, tc.panelHeight)
		}
		if rows*(ph+2) > max(tc.height, 3) {
			t.Errorf("insightsRowLayout(%d, %d): %d rows of %d lines exceed the height", tc.height, tc.focusedRow, rows, ph+2)
		}
	}
}

func TestPanelListRows(t *testing.T) {
	expl := []string{"a", "b", "c", "d"}
	cases := []struct {
		avail, total  int
		keep, visible int
		indicator     bool
	}{
		{20, 5, 4, 5, false}, // everything fits
		{10, 50, 4, 5, true}, // explanation kept, list scrolls
		{5, 50, 1, 3, true},  // explanation cut to leave 3 items + indicator
		{3, 50, 0, 2, true},  // no room for the explanation
		{6, 2, 4, 2, false},  // short list keeps the whole explanation
	}
	for _, tc := range cases {
		keep, visible, ind := panelListRows(tc.avail, expl, tc.total)
		if len(keep) != tc.keep || visible != tc.visible || ind != tc.indicator {
			t.Errorf("panelListRows(%d, 4 lines, %d) = (%d, %d, %v), want (%d, %d, %v)",
				tc.avail, tc.total, len(keep), visible, ind, tc.keep, tc.visible, tc.indicator)
		}
	}
}

// A board card is always 6 rows (3 content lines, border, margin): its
// labels/blocker line is cut to the card width instead of wrapping.
func TestBoardCardHeightIsFixed(t *testing.T) {
	issues := layoutFitTestIssues()
	b := NewBoardModel(issues, DefaultTheme(nil))
	for _, width := range []int{8, 14, 20, 26, 40} {
		for i := range issues {
			card := b.renderCard(issues[i], width, i == 0, 0, i)
			if n := len(strings.Split(card, "\n")); n != 6 {
				t.Fatalf("card for %s at width %d is %d rows, want 6:\n%s", issues[i].ID, width, n, ansi.Strip(card))
			}
		}
	}
}

// The footer must fit the terminal and lose whole hints, not be cut
// mid-hint, as it was at 100 columns and below.
func TestFooterDropsWholeHintsWhenNarrow(t *testing.T) {
	for _, key := range []string{"", "b", "i", "g", "a"} {
		for _, w := range []int{60, 80, 90, 100, 120, 160} {
			t.Run(fmt.Sprintf("key%q_%d", key, w), func(t *testing.T) {
				m := sizedModel(t, layoutFitTestIssues(), w, 30)
				if key != "" {
					m = sendRunes(t, m, key)
				}
				m.statusMsg = ""
				footer := m.renderFooter()
				if strings.Contains(footer, "\n") {
					t.Fatalf("footer spans more than one row")
				}
				if fw := ansi.StringWidth(footer); fw > w {
					t.Fatalf("footer is %d cells wide in a %d-wide terminal", fw, w)
				}
				plain := strings.TrimRight(ansi.Strip(footer), " ")
				if strings.HasSuffix(plain, "…") {
					t.Errorf("footer was cut instead of dropping whole hints: %q", plain)
				}
				if w >= 80 && !strings.Contains(plain, "help") && !strings.Contains(plain, "nav") {
					t.Errorf("footer at %d columns lost all of its key hints: %q", w, plain)
				}
			})
		}
	}
}

func assertViewKeepsFooter(t *testing.T, m *Model) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Errorf("View() has %d rows, want %d", len(lines), m.height)
	}
	if mw := maxLineWidthOf(view); mw > m.width {
		t.Errorf("View() is %d cells wide in a %d-wide terminal", mw, m.width)
	}
	wantFooter := strings.TrimSpace(ansi.Strip(m.renderFooter()))
	if last := strings.TrimSpace(ansi.Strip(lines[len(lines)-1])); last == "" || last != wantFooter {
		t.Errorf("footer not on the last row\n last row: %q\n footer:   %q", last, wantFooter)
	}
}
