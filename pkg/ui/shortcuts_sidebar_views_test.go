package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// sidebarViewTestIssues returns a small workspace with mixed statuses, types,
// labels, and blocking dependencies so every view (board columns, graph edges,
// insights, actionable tracks, label views) has real content to lay out.
func sidebarViewTestIssues() []model.Issue {
	statuses := []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusBlocked, model.StatusClosed, model.StatusOpen}
	types := []model.IssueType{model.TypeBug, model.TypeFeature, model.TypeTask, model.TypeEpic, model.TypeChore}
	labels := [][]string{{"ui", "perf"}, {"backend"}, {"infra", "docs"}, {"backend", "ui"}, {"perf"}}
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	issues := make([]model.Issue, 0, 36)
	for i := 0; i < 36; i++ {
		iss := model.Issue{
			ID:          fmt.Sprintf("demo-%03d", i),
			Title:       fmt.Sprintf("Synthetic issue %d with a reasonably long title to exercise truncation", i),
			Description: "Synthetic issue used to check the shortcuts sidebar layout (GH #209).",
			Status:      statuses[i%len(statuses)],
			Priority:    i % 5,
			IssueType:   types[i%len(types)],
			Labels:      labels[i%len(labels)],
			CreatedAt:   base.Add(time.Duration(i) * time.Hour),
			UpdatedAt:   base.Add(time.Duration(i+1) * time.Hour),
		}
		if i >= 4 && i%3 == 0 {
			iss.Dependencies = []*model.Dependency{{
				IssueID:     iss.ID,
				DependsOnID: fmt.Sprintf("demo-%03d", i-3),
				Type:        model.DepBlocks,
			}}
		}
		issues = append(issues, iss)
	}
	return issues
}

// TestShortcutsSidebarFitsEveryView is the regression test for GH #209, the
// follow-up to #168. #168 made the list/split layout reserve the shortcuts
// sidebar's column, but the other views (board, graph, insights, actionable,
// history, tree, label dashboard, attention, flow matrix) were still laid out
// at the full terminal width and the sidebar was appended to them anyway. The
// composed rows were then wider than the terminal; the final full-screen clamp
// wrapped them, interleaving sidebar fragments with the view's rows and
// pushing the footer off the bottom.
//
// For each view, driven through the real key path at several terminal sizes
// with the sidebar opened via `;`, it asserts:
//   - the body (everything above the footer) is at most m.width cells wide and
//     m.height-1 rows tall, i.e. nothing needs wrapping;
//   - the sidebar is pinned to the right edge (first row ends in its corner);
//   - the full View() is exactly m.height rows, none wider than m.width, and
//     its last row is the footer.
func TestShortcutsSidebarFitsEveryView(t *testing.T) {
	views := []struct {
		name      string
		key       string
		wantFocus focus
	}{
		{"list", "", focusList},
		{"board", "b", focusBoard},
		{"graph", "g", focusGraph},
		{"insights", "i", focusInsights},
		{"actionable", "a", focusActionable},
		{"history", "h", focusHistory},
		{"tree", "E", focusTree},
		{"label_dashboard", "[", focusLabelDashboard},
		{"attention", "]", focusAttention},
		{"flow_matrix", "f", focusFlowMatrix},
	}
	sizes := []struct{ w, h int }{
		{160, 45},
		{100, 30},
		{220, 60},
		{80, 24},
		{60, 20},
	}

	for _, sz := range sizes {
		for _, v := range views {
			// Both orders: open the view then the sidebar (the issue's repro),
			// and open the sidebar first then switch views.
			for _, sidebarFirst := range []bool{false, true} {
				order := "view_then_sidebar"
				if sidebarFirst {
					order = "sidebar_then_view"
				}
				t.Run(fmt.Sprintf("%s_%dx%d_%s", v.name, sz.w, sz.h, order), func(t *testing.T) {
					m := sizedModel(t, sidebarViewTestIssues(), sz.w, sz.h)
					if sidebarFirst {
						m = sendRunes(t, m, ";")
					}
					if v.key != "" {
						m = sendRunes(t, m, v.key)
					}
					if m.focused != v.wantFocus {
						t.Fatalf("key %q: focused=%v, want %v", v.key, m.focused, v.wantFocus)
					}
					if !sidebarFirst {
						m = sendRunes(t, m, ";")
					}
					if !m.showShortcutsSidebar || !m.sidebarVisible() {
						t.Fatalf("`;` did not show the shortcuts sidebar (show=%v visible=%v)", m.showShortcutsSidebar, m.sidebarVisible())
					}
					assertSidebarLayoutFits(t, m)
				})
			}
		}
	}
}

// TestShortcutsSidebarFitsAfterResize checks the WindowSizeMsg path: with the
// sidebar open in a full-screen view, shrinking or growing the terminal must
// reflow the view into the new reserved width.
func TestShortcutsSidebarFitsAfterResize(t *testing.T) {
	for _, key := range []string{"b", "i", "E", "a", "f"} {
		t.Run("key_"+key, func(t *testing.T) {
			m := sizedModel(t, sidebarViewTestIssues(), 160, 45)
			m = sendRunes(t, m, key)
			m = sendRunes(t, m, ";")
			for _, sz := range []struct{ w, h int }{{100, 30}, {220, 60}, {160, 45}} {
				updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
				m = updated.(*Model)
				assertSidebarLayoutFits(t, m)
			}
		})
	}
}

// TestShortcutsSidebarFitsSprintView covers the pre-rendered sprint dashboard,
// which must be re-rendered at the reserved width when the sidebar toggles.
func TestShortcutsSidebarFitsSprintView(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{160, 45}, {220, 60}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := sizedModel(t, sidebarViewTestIssues(), sz.w, sz.h)
			now := time.Now()
			m.sprints = []model.Sprint{{
				ID:        "sprint-1",
				Name:      "Sprint with a long enough name to matter",
				StartDate: now.Add(-7 * 24 * time.Hour),
				EndDate:   now.Add(7 * 24 * time.Hour),
				BeadIDs:   []string{"demo-000", "demo-001", "demo-002", "demo-003"},
			}}
			m = sendRunes(t, m, "P")
			if !m.isSprintView {
				t.Fatalf("P did not open the sprint view")
			}
			m = sendRunes(t, m, ";")
			assertSidebarLayoutFits(t, m)
		})
	}
}

// TestShortcutsSidebarHiddenWhenTooNarrow checks the degraded mode: when the
// terminal cannot fit the sidebar next to a minimally wide body, the sidebar
// is not drawn (rather than overflowing), and the body gets the full width.
func TestShortcutsSidebarHiddenWhenTooNarrow(t *testing.T) {
	for _, key := range []string{"", "b", "i"} {
		t.Run("key_"+key, func(t *testing.T) {
			m := sizedModel(t, sidebarViewTestIssues(), 50, 20)
			if key != "" {
				m = sendRunes(t, m, key)
			}
			m = sendRunes(t, m, ";")
			if !m.showShortcutsSidebar {
				t.Fatalf("`;` should still toggle the sidebar preference on")
			}
			if m.sidebarVisible() {
				t.Fatalf("sidebar should not be drawn at width %d", m.width)
			}
			if got := m.mainContentWidth(); got != m.width {
				t.Errorf("mainContentWidth()=%d, want full width %d when sidebar hidden", got, m.width)
			}
			view := m.View()
			if strings.Contains(ansi.Strip(view), "│           Shortcuts") {
				t.Errorf("sidebar rendered despite being too narrow:\n%s", ansi.Strip(view))
			}
			if !strings.Contains(m.statusMsg, "needs a terminal at least") {
				t.Errorf("status should explain why the sidebar is not shown, got %q", m.statusMsg)
			}
			if mw := maxLineWidthOf(view); mw > m.width {
				t.Errorf("max line width %d exceeds terminal width %d", mw, m.width)
			}
		})
	}
}

// TestShortcutsSidebarNotAppendedToOverlays checks that full-screen overlays
// (drawn at full width) do not get the sidebar appended beside them.
func TestShortcutsSidebarNotAppendedToOverlays(t *testing.T) {
	m := sizedModel(t, sidebarViewTestIssues(), 160, 45)
	m = sendRunes(t, m, ";")
	m = sendRunes(t, m, "?")
	if !m.showHelp {
		t.Fatalf("? did not open the help overlay")
	}
	body := m.renderBody()
	if mw := maxLineWidthOf(body); mw > m.width {
		t.Errorf("help overlay body width %d exceeds terminal width %d", mw, m.width)
	}
	if n := len(strings.Split(m.View(), "\n")); n != m.height {
		t.Errorf("View() has %d rows, want %d", n, m.height)
	}
}

func sendRunes(t *testing.T, m *Model, key string) *Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated.(*Model)
}

func maxLineWidthOf(s string) int {
	mx := 0
	for _, ln := range strings.Split(s, "\n") {
		if w := lipgloss.Width(ln); w > mx {
			mx = w
		}
	}
	return mx
}

func assertSidebarLayoutFits(t *testing.T, m *Model) {
	t.Helper()

	// On roomy terminals every view must genuinely reflow into the reserved
	// width, not merely be clipped by the safety clamp in renderBody. (On
	// narrow ones some views have fixed minimum widths they exceed even
	// without the sidebar, and there the clamp is the intended fallback.)
	if m.width >= 160 {
		raw, overlay := m.renderMainView()
		if overlay {
			t.Fatalf("unexpected overlay while checking the main view")
		}
		if rw := maxLineWidthOf(raw); rw > m.mainContentWidth() {
			t.Errorf("view rendered %d cells wide, not reflowed into the reserved %d", rw, m.mainContentWidth())
		}
	}

	body := m.renderBody()
	bodyLines := strings.Split(body, "\n")
	if mw := maxLineWidthOf(body); mw > m.width {
		t.Errorf("body+sidebar width %d exceeds terminal width %d (GH #209 overflow)", mw, m.width)
	}
	if len(bodyLines) > m.height-1 {
		t.Errorf("body+sidebar has %d rows, more than the %d above the footer", len(bodyLines), m.height-1)
	}
	sidebarTop := "╭" + strings.Repeat("─", m.shortcutsSidebar.Width()) + "╮"
	if first := ansi.Strip(bodyLines[0]); !strings.HasSuffix(first, sidebarTop) {
		t.Errorf("sidebar not pinned to the right edge; first row: %q", first)
	}
	// Every row above the footer must end in the sidebar's right border, and
	// the last one in its bottom corner: an over-wide body row would push the
	// sidebar right (and get it clipped), and an over-tall sidebar box would
	// lose its bottom border to the height clamp.
	if len(bodyLines) != m.height-1 {
		t.Errorf("body+sidebar has %d rows, want exactly %d", len(bodyLines), m.height-1)
	}
	for i, ln := range bodyLines {
		want := "│"
		switch i {
		case 0:
			want = "╮"
		case len(bodyLines) - 1:
			want = "╯"
		}
		if got := strings.TrimRight(ansi.Strip(ln), " "); !strings.HasSuffix(got, want) {
			t.Errorf("body row %d does not end in the sidebar border %q: %q", i, want, got)
			break
		}
	}
	if !strings.Contains(ansi.Strip(body), "Shortcuts") {
		t.Errorf("sidebar title missing from body")
	}

	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Errorf("View() has %d rows, want exactly %d", len(lines), m.height)
	}
	if mw := maxLineWidthOf(view); mw > m.width {
		t.Errorf("View() max line width %d exceeds terminal width %d", mw, m.width)
	}
	// The footer must start on the last row. (Compare as a prefix: on narrower
	// terminals the footer's own hint text can be wider than the terminal and
	// is cut by the final clamp independently of the sidebar.)
	wantFooter := strings.TrimSpace(ansi.Strip(m.renderFooter()))
	if wantFooter == "" {
		t.Fatalf("empty footer; cannot check its placement")
	}
	if last := strings.TrimSpace(ansi.Strip(lines[len(lines)-1])); last == "" || !strings.HasPrefix(wantFooter, last) {
		t.Errorf("footer not on the last row (pushed off-screen?)\n last row: %q\n footer:   %q", last, wantFooter)
	}
}
