package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// =============================================================================
// FlowMatrixView Tests (Legacy Summary Function)
// =============================================================================
// Note: The primary UI is now FlowMatrixModel (interactive). FlowMatrixView
// is kept for backward compatibility and returns a simple summary.

func TestFlowMatrixViewEmptyLabels(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:     []string{},
		FlowMatrix: [][]int{},
	}

	result := ui.FlowMatrixView(flow, 80)
	expected := "No cross-label dependencies found"
	if result != expected {
		t.Errorf("FlowMatrixView() = %q, want %q", result, expected)
	}
}

func TestFlowMatrixViewNilLabels(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:     nil,
		FlowMatrix: nil,
	}

	result := ui.FlowMatrixView(flow, 80)
	expected := "No cross-label dependencies found"
	if result != expected {
		t.Errorf("FlowMatrixView() with nil labels = %q, want %q", result, expected)
	}
}

func TestFlowMatrixViewSingleLabel(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:     []string{"bug"},
		FlowMatrix: [][]int{{0}},
	}

	result := ui.FlowMatrixView(flow, 80)

	// Should contain header
	if !strings.Contains(result, "DEPENDENCY FLOW SUMMARY") {
		t.Errorf("FlowMatrixView() should contain header, got: %q", result)
	}

	// Should contain labels count
	if !strings.Contains(result, "Labels: 1") {
		t.Errorf("FlowMatrixView() should contain 'Labels: 1', got: %q", result)
	}
}

func TestFlowMatrixViewMultipleLabels(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels: []string{"bug", "feat", "docs"},
		FlowMatrix: [][]int{
			{0, 2, 1}, // bug blocks feat 2 times, docs 1 time
			{1, 0, 3}, // feat blocks bug 1 time, docs 3 times
			{0, 0, 0}, // docs blocks nothing
		},
		TotalCrossLabelDeps: 7,
	}

	result := ui.FlowMatrixView(flow, 80)

	// Should contain header
	if !strings.Contains(result, "DEPENDENCY FLOW SUMMARY") {
		t.Errorf("FlowMatrixView() should contain header, got: %q", result)
	}

	// Should contain labels count
	if !strings.Contains(result, "Labels: 3") {
		t.Errorf("FlowMatrixView() should contain 'Labels: 3', got: %q", result)
	}

	// Should contain cross-label deps count
	if !strings.Contains(result, "Cross-label dependencies: 7") {
		t.Errorf("FlowMatrixView() should contain 'Cross-label dependencies: 7', got: %q", result)
	}
}

func TestFlowMatrixViewWithBottlenecks(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels: []string{"api", "web"},
		FlowMatrix: [][]int{
			{0, 5},
			{2, 0},
		},
		BottleneckLabels:    []string{"api"},
		TotalCrossLabelDeps: 7,
	}

	result := ui.FlowMatrixView(flow, 80)

	// Should contain bottleneck info
	if !strings.Contains(result, "Bottleneck labels: [api]") {
		t.Errorf("FlowMatrixView() should contain bottleneck labels, got: %q", result)
	}
}

func TestFlowMatrixViewNotEmpty(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels: []string{"a", "b"},
		FlowMatrix: [][]int{
			{0, 1},
			{0, 0},
		},
	}

	result := ui.FlowMatrixView(flow, 80)

	if result == "" {
		t.Error("FlowMatrixView() should not return empty string for valid input")
	}

	if len(result) < 20 {
		t.Errorf("FlowMatrixView() output seems too short: %q", result)
	}
}

func TestFlowMatrixViewZeroWidth(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:     []string{"test"},
		FlowMatrix: [][]int{{0}},
	}

	result := ui.FlowMatrixView(flow, 0)
	if result == "" {
		t.Error("FlowMatrixView(width=0) should not return empty string")
	}
}

func TestFlowMatrixViewNegativeWidth(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:     []string{"test"},
		FlowMatrix: [][]int{{0}},
	}

	result := ui.FlowMatrixView(flow, -10)
	if result == "" {
		t.Error("FlowMatrixView(width=-10) should not return empty string")
	}
}

func TestFlowMatrixViewLargeMatrix(t *testing.T) {
	labels := []string{"api", "web", "db", "auth", "core"}
	n := len(labels)
	matrix := make([][]int, n)
	for i := range matrix {
		matrix[i] = make([]int, n)
		for j := range matrix[i] {
			if i != j {
				matrix[i][j] = i + j
			}
		}
	}

	flow := analysis.CrossLabelFlow{
		Labels:     labels,
		FlowMatrix: matrix,
	}

	result := ui.FlowMatrixView(flow, 120)

	// Should contain labels count
	if !strings.Contains(result, "Labels: 5") {
		t.Errorf("FlowMatrixView() should contain 'Labels: 5', got: %q", result)
	}
}

func TestFlowMatrixViewOutput(t *testing.T) {
	flow := analysis.CrossLabelFlow{
		Labels:              []string{"bug", "feat"},
		FlowMatrix:          [][]int{{0, 1}, {2, 0}},
		TotalCrossLabelDeps: 3,
		BottleneckLabels:    []string{},
	}

	result := ui.FlowMatrixView(flow, 80)

	// Verify structure
	lines := strings.Split(result, "\n")
	if len(lines) < 4 {
		t.Errorf("FlowMatrixView() should produce at least 4 lines, got %d", len(lines))
	}

	// First line should be header
	if !strings.Contains(lines[0], "DEPENDENCY FLOW SUMMARY") {
		t.Errorf("First line should be header, got: %q", lines[0])
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkFlowMatrixViewSmall(b *testing.B) {
	flow := analysis.CrossLabelFlow{
		Labels:     []string{"a", "b", "c"},
		FlowMatrix: [][]int{{0, 1, 2}, {0, 0, 1}, {1, 0, 0}},
	}
	for i := 0; i < b.N; i++ {
		_ = ui.FlowMatrixView(flow, 80)
	}
}

func BenchmarkFlowMatrixViewMedium(b *testing.B) {
	n := 10
	labels := make([]string, n)
	matrix := make([][]int, n)
	for i := 0; i < n; i++ {
		labels[i] = string(rune('a' + i))
		matrix[i] = make([]int, n)
		for j := 0; j < n; j++ {
			if i != j {
				matrix[i][j] = (i + j) % 5
			}
		}
	}
	flow := analysis.CrossLabelFlow{
		Labels:     labels,
		FlowMatrix: matrix,
	}
	for i := 0; i < b.N; i++ {
		_ = ui.FlowMatrixView(flow, 120)
	}
}

func BenchmarkFlowMatrixViewLarge(b *testing.B) {
	n := 20
	labels := make([]string, n)
	matrix := make([][]int, n)
	for i := 0; i < n; i++ {
		labels[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
		matrix[i] = make([]int, n)
		for j := 0; j < n; j++ {
			if i != j {
				matrix[i][j] = (i * j) % 10
			}
		}
	}
	flow := analysis.CrossLabelFlow{
		Labels:     labels,
		FlowMatrix: matrix,
	}
	for i := 0; i < b.N; i++ {
		_ = ui.FlowMatrixView(flow, 200)
	}
}

// =============================================================================
// FlowMatrixModel Tests (Interactive Dashboard) - bv-w4l0
// =============================================================================

func testFlowTheme() ui.Theme {
	return ui.DefaultTheme(nil)
}

func flowDrilldownFixture() []model.Issue {
	return []model.Issue{
		{ID: "blocker", Title: "Database migration", Status: model.StatusOpen, Labels: []string{"database", "storage"}},
		{ID: "unrelated", Title: "Unrelated database task", Status: model.StatusOpen, Labels: []string{"database"}},
		{ID: "dependent", Title: "API rollout", Status: model.StatusOpen, Labels: []string{"api", "service"}, Dependencies: []*model.Dependency{{DependsOnID: "blocker", Type: model.DepBlocks}}},
	}
}

func TestFlowMatrixDrilldownRelationships(t *testing.T) {
	issues := flowDrilldownFixture()
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m := ui.NewFlowMatrixModel(testFlowTheme())
	m.SetData(&flow, issues)
	m.SetSize(100, 24)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := m.View()
	t.Logf("real flow=%+v; rendered drilldown:\n%s", flow.Dependencies, view)
	for _, want := range []string{"blocker", "dependent", "Database migration", "API rollout", "blocks"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing relationship content %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "unrelated") || strings.Count(view, "API rollout") != 1 {
		t.Errorf("unrelated or duplicate pair in drilldown:\n%s", view)
	}
	for _, id := range []string{"blocker", "dependent", "dependent"} {
		if got := m.SelectedDrilldownIssue(); got == nil || got.ID != id {
			t.Fatalf("selected=%+v, want %s", got, id)
		}
		m.MoveDown()
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.GoToEnd() // Incoming-only label must show the same direction.
	m.OpenDrilldown()
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != "blocker" {
		t.Fatalf("incoming label inverted or lost blocker: %+v", got)
	}
}

func TestFlowMatrixDrilldownRefresh(t *testing.T) {
	issues := flowDrilldownFixture()
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m := ui.NewFlowMatrixModel(testFlowTheme())
	m.SetData(&flow, issues)
	m.SetSize(100, 24)
	m.OpenDrilldown()
	m.MoveDown()
	issues[2].Title = "REFRESHED API rollout"
	flow = analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != "dependent" || got.Title != issues[2].Title {
		t.Fatalf("refresh did not preserve selected endpoint/current title: %+v\n%s", got, m.View())
	}
	issues[0].Status = model.StatusClosed
	flow = analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	if m.SelectedDrilldownIssue() != nil || strings.Contains(m.View(), "API rollout") {
		t.Fatalf("closed relationship retained after refresh:\n%s", m.View())
	}
	m.SetData(nil, nil)
	if m.SelectedLabel() != "" || m.SelectedDrilldownIssue() != nil {
		t.Fatal("empty source retained label or endpoint selection")
	}
}

func TestFlowMatrixDrilldownNarrowUnicode(t *testing.T) {
	issues := flowDrilldownFixture()
	issues[0].ID = strings.Repeat("界", 40)
	issues[2].Dependencies[0].DependsOnID = issues[0].ID
	issues[2].Title = strings.Repeat("🧪 rollout ", 20)
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m := ui.NewFlowMatrixModel(testFlowTheme())
	m.SetData(&flow, issues)
	m.SetSize(24, 12)
	m.OpenDrilldown()
	for _, line := range strings.Split(m.View(), "\n") {
		if width := lipgloss.Width(line); width > 24 {
			t.Errorf("line exceeds terminal width (%d): %q", width, line)
		}
	}
	m.MoveDown()
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != "dependent" {
		t.Fatalf("clipping lost endpoint identity: %+v", got)
	}
}

func TestFlowMatrixDrilldownExcludesNonRelationships(t *testing.T) {
	for _, kind := range []string{"closed-blocker", "tombstone-dependent", "nonblocking", "missing-blocker", "missing-detail"} {
		t.Run(kind, func(t *testing.T) {
			issues := flowDrilldownFixture()
			switch kind {
			case "closed-blocker":
				issues[0].Status = model.StatusClosed
			case "tombstone-dependent":
				issues[2].Status = model.StatusTombstone
			case "nonblocking":
				issues[2].Dependencies[0].Type = model.DepRelated
			case "missing-blocker":
				issues[2].Dependencies[0].DependsOnID = "not-loaded"
			}
			flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
			if kind == "missing-detail" {
				issues = issues[1:] // Omit one endpoint at the consumer boundary.
			}
			m := ui.NewFlowMatrixModel(testFlowTheme())
			m.SetData(&flow, issues)
			m.SetSize(100, 24)
			m.OpenDrilldown()
			if m.SelectedDrilldownIssue() != nil || strings.Contains(m.View(), "API rollout") {
				t.Fatalf("%s fabricated a blocking relationship:\n%s", kind, m.View())
			}
		})
	}
}

func TestFlowMatrixDrilldownSelectionSurvivesReordering(t *testing.T) {
	issues := flowDrilldownFixture()
	m := ui.NewFlowMatrixModel(testFlowTheme())
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	m.SetSize(100, 12)
	m.OpenDrilldown()
	m.MoveDown()
	issues = append(issues,
		model.Issue{ID: "aaa", Status: model.StatusOpen, Labels: []string{"database", "a-new-label"}},
		model.Issue{ID: "bbb", Status: model.StatusOpen, Labels: []string{"api"}, Dependencies: []*model.Dependency{{DependsOnID: "aaa", Type: model.DepBlocks}}},
	)
	flow = analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	if got := m.SelectedDrilldownIssue(); m.SelectedLabel() != "database" || got == nil || got.ID != "dependent" {
		t.Fatalf("reordering lost selected label/pair/endpoint: label=%s endpoint=%+v", m.SelectedLabel(), got)
	}
	m.GoToEnd()
	m.SetSize(100, 30)
	if !strings.Contains(m.View(), "aaa") {
		t.Fatalf("resize retained an obsolete scroll offset:\n%s", m.View())
	}
	issues[0].Labels = []string{"storage"}
	issues[1].Labels = nil
	issues[3].Labels = []string{"a-new-label"}
	flow = analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	if m.SelectedDrilldownIssue() != nil || m.SelectedLabel() == "database" {
		t.Fatal("removed label retained its drilldown")
	}
}

func TestNewFlowMatrixModel(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	// Should be able to call View() without panic
	view := m.View()
	if view == "" {
		t.Error("NewFlowMatrixModel().View() should not return empty string")
	}

	// SelectedLabel should return empty string for empty model
	if label := m.SelectedLabel(); label != "" {
		t.Errorf("SelectedLabel() = %q, want empty string for new model", label)
	}
}

func TestFlowMatrixModelSetData(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels: []string{"api", "web", "db"},
		FlowMatrix: [][]int{
			{0, 2, 1},
			{1, 0, 3},
			{0, 0, 0},
		},
		TotalCrossLabelDeps: 7,
		BottleneckLabels:    []string{"api"},
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	view := m.View()
	if strings.Contains(view, "No cross-label dependencies found") {
		t.Error("View() should not show 'no dependencies' after SetData with valid flow")
	}

	if label := m.SelectedLabel(); label == "" {
		t.Error("SelectedLabel() should return a label after SetData")
	}
}

func TestFlowMatrixModelSetDataEmpty(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	m.SetData(nil, nil)
	m.SetSize(80, 24)

	view := m.View()
	if !strings.Contains(view, "No cross-label dependencies found") {
		t.Error("View() should show 'no dependencies' for nil flow data")
	}
}

func TestFlowMatrixModelNavigation(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels: []string{"api", "web", "db", "auth", "core"},
		FlowMatrix: [][]int{
			{0, 2, 1, 0, 1},
			{1, 0, 3, 1, 0},
			{0, 0, 0, 0, 0},
			{2, 1, 0, 0, 1},
			{0, 0, 1, 0, 0},
		},
		TotalCrossLabelDeps: 13,
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	initialLabel := m.SelectedLabel()
	if initialLabel == "" {
		t.Fatal("SelectedLabel() should return a label after SetData")
	}

	m.MoveDown()
	newLabel := m.SelectedLabel()
	if newLabel == initialLabel {
		t.Error("MoveDown() should change selected label")
	}

	m.MoveUp()
	backLabel := m.SelectedLabel()
	if backLabel != initialLabel {
		t.Errorf("MoveUp() should restore selection, got %q want %q", backLabel, initialLabel)
	}

	m.GoToEnd()
	if m.SelectedLabel() == "" {
		t.Error("GoToEnd() should select a label")
	}

	m.GoToStart()
	if m.SelectedLabel() != initialLabel {
		t.Errorf("GoToStart() should select first label")
	}
}

func TestFlowMatrixModelBoundary(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels:     []string{"only-one"},
		FlowMatrix: [][]int{{0}},
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	// Should not panic on boundary
	m.MoveDown()
	m.MoveDown()
	m.MoveUp()
	m.MoveUp()
	m.MoveUp()

	if m.SelectedLabel() != "only-one" {
		t.Errorf("SelectedLabel() = %q, want %q", m.SelectedLabel(), "only-one")
	}
}

func TestFlowMatrixModelTogglePanel(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels:     []string{"a", "b"},
		FlowMatrix: [][]int{{0, 1}, {1, 0}},
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	m.TogglePanel()
	view1 := m.View()
	m.TogglePanel()
	view2 := m.View()

	if view1 == "" || view2 == "" {
		t.Error("View() should not be empty after TogglePanel")
	}
}

func TestFlowMatrixModelDrilldown(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels:     []string{"api"},
		FlowMatrix: [][]int{{0}},
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	if m.SelectedDrilldownIssue() != nil {
		t.Error("SelectedDrilldownIssue() should return nil before OpenDrilldown")
	}

	m.OpenDrilldown()
	view := m.View()
	if view == "" {
		t.Error("View() should not be empty after OpenDrilldown")
	}
}

func TestFlowMatrixModelViewRendersContent(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels: []string{"backend", "frontend"},
		FlowMatrix: [][]int{
			{0, 5},
			{2, 0},
		},
		TotalCrossLabelDeps: 7,
		BottleneckLabels:    []string{"backend"},
	}

	m.SetData(flow, nil)
	m.SetSize(100, 30)

	view := m.View()
	if len(view) < 100 {
		t.Errorf("View() seems too short: %d chars", len(view))
	}
}

func TestFlowMatrixModelInvalidMatrix(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	flow := &analysis.CrossLabelFlow{
		Labels:     []string{"a", "b", "c"},
		FlowMatrix: [][]int{{0}}, // Invalid: 1 row for 3 labels
	}

	m.SetData(flow, nil)
	m.SetSize(80, 24)

	// Should handle gracefully without panic
	_ = m.View()
}

func TestFlowMatrixModelEmptyOperations(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	// Should not panic on empty model
	m.GoToEnd()
	m.GoToStart()
	m.MoveUp()
	m.MoveDown()
	m.TogglePanel()
	m.OpenDrilldown()

	if m.SelectedLabel() != "" {
		t.Errorf("SelectedLabel() = %q, want empty for empty model", m.SelectedLabel())
	}
}

func TestFlowMatrixRightPaneFocusAndScroll(t *testing.T) {
	theme := testFlowTheme()
	m := ui.NewFlowMatrixModel(theme)

	issues := []model.Issue{
		{ID: "B-1", Title: "Auth middleware", Status: model.StatusOpen, Priority: 1, Labels: []string{"backend"}},
		{ID: "B-2", Title: "DB schema", Status: model.StatusOpen, Priority: 2, Labels: []string{"backend"}},
		{ID: "B-3", Title: "Cache layer", Status: model.StatusInProgress, Priority: 2, Labels: []string{"backend"}},
		{
			ID: "F-1", Title: "Login screen", Status: model.StatusOpen, Priority: 1, Labels: []string{"frontend"},
			Dependencies: []*model.Dependency{
				{IssueID: "F-1", DependsOnID: "B-1", Type: model.DepBlocks},
				{IssueID: "F-1", DependsOnID: "B-2", Type: model.DepBlocks},
				{IssueID: "F-1", DependsOnID: "B-3", Type: model.DepBlocks},
			},
		},
	}
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	m.SetSize(120, 28)

	// Left pane focused initially: SelectedDrilldownIssue is nil before TogglePanel / OpenDrilldown.
	if m.SelectedDrilldownIssue() != nil {
		t.Fatalf("expected nil SelectedDrilldownIssue while left pane is focused, got %+v", m.SelectedDrilldownIssue())
	}
	leftFocusedView := m.View()

	// Tab switches focus to right pane (focusPanel == 1), highlights border, and exposes inline RELATED ISSUES.
	_ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	rightFocusedView := m.View()
	if rightFocusedView == leftFocusedView {
		t.Fatal("expected Tab to visually update focused pane border and indicator")
	}
	if !strings.Contains(rightFocusedView, "RELATED ISSUES") {
		t.Fatalf("expected inline RELATED ISSUES in right pane:\n%s", rightFocusedView)
	}
	first := m.SelectedDrilldownIssue()
	if first == nil {
		t.Fatal("expected non-nil SelectedDrilldownIssue when right pane has focus")
	}
	firstID := first.ID

	// j / down in right pane scrolls drilldownCursor without moving left-pane SelectedLabel.
	_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	second := m.SelectedDrilldownIssue()
	if second == nil || second.ID == firstID {
		t.Fatalf("expected 'j' in right pane to advance SelectedDrilldownIssue from %q, got %+v", firstID, second)
	}
	if m.SelectedLabel() != "backend" {
		t.Fatalf("right-pane scroll must not change SelectedLabel(), got %q", m.SelectedLabel())
	}

	// G jumps to last related issue; g jumps back to first.
	_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	last := m.SelectedDrilldownIssue()
	if last == nil || last.ID == firstID {
		t.Fatalf("expected 'G' in right pane to jump to last issue, got %+v", last)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	backToFirst := m.SelectedDrilldownIssue()
	if backToFirst == nil || backToFirst.ID != firstID {
		t.Fatalf("expected 'g' in right pane to return to %q, got %+v", firstID, backToFirst)
	}
}

func TestFlowMatrixViewportPairAlignmentAndPageKeys(t *testing.T) {
	theme := ui.DefaultTheme(lipgloss.NewRenderer(nil))
	m := ui.NewFlowMatrixModel(theme)

	var issues []model.Issue
	// Build 7 blocking pairs from "backend" -> "frontend" plus extra labels so both panes scroll.
	for i := 1; i <= 7; i++ {
		blockerID := fmt.Sprintf("B-%02d", i)
		blockedID := fmt.Sprintf("F-%02d", i)
		issues = append(issues,
			model.Issue{
				ID:     blockerID,
				Title:  fmt.Sprintf("Backend blocker %d", i),
				Status: model.StatusOpen,
				Labels: []string{"backend"},
			},
			model.Issue{
				ID:     blockedID,
				Title:  fmt.Sprintf("Frontend dependent %d", i),
				Status: model.StatusBlocked,
				Labels: []string{"frontend"},
				Dependencies: []*model.Dependency{
					{IssueID: blockedID, DependsOnID: blockerID, Type: model.DepBlocks},
				},
			},
		)
	}
	for i := 1; i <= 8; i++ {
		lbl := fmt.Sprintf("team-%02d", i)
		src := fmt.Sprintf("T-%02d", i)
		dst := fmt.Sprintf("U-%02d", i)
		issues = append(issues,
			model.Issue{ID: src, Title: "Src " + src, Status: model.StatusOpen, Labels: []string{lbl}},
			model.Issue{
				ID:     dst,
				Title:  "Dst " + dst,
				Status: model.StatusBlocked,
				Labels: []string{"sink"},
				Dependencies: []*model.Dependency{
					{IssueID: dst, DependsOnID: src, Type: model.DepBlocks},
				},
			},
		)
	}

	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m.SetData(&flow, issues)
	m.SetSize(120, 18)

	// 1. Left pane (focusPanel == 0): pgdown/pgup and ctrl+d/ctrl+u move SelectedLabel.
	startLabel := m.SelectedLabel()
	if startLabel != "backend" {
		t.Fatalf("expected top label 'backend', got %q", startLabel)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	afterPgDn := m.SelectedLabel()
	if afterPgDn == startLabel {
		t.Fatalf("expected pgdown in left pane to advance label from %q", startLabel)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.SelectedLabel() != startLabel {
		t.Fatalf("expected pgup in left pane to return to %q, got %q", startLabel, m.SelectedLabel())
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.SelectedLabel() == startLabel {
		t.Fatalf("expected ctrl+d in left pane to advance label from %q", startLabel)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.SelectedLabel() != startLabel {
		t.Fatalf("expected ctrl+u in left pane to return to %q, got %q", startLabel, m.SelectedLabel())
	}

	// 2. Switch to inline right pane (focusPanel == 1) and test pgdown/pgup/ctrl+d/ctrl+u.
	_ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	firstIssue := m.SelectedDrilldownIssue()
	if firstIssue == nil {
		t.Fatal("expected non-nil SelectedDrilldownIssue after Tab")
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID == firstIssue.ID {
		t.Fatalf("expected pgdown in right pane to advance from %q, got %+v", firstIssue.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != firstIssue.ID {
		t.Fatalf("expected pgup in right pane to return to %q, got %+v", firstIssue.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID == firstIssue.ID {
		t.Fatalf("expected ctrl+d in right pane to advance from %q, got %+v", firstIssue.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != firstIssue.ID {
		t.Fatalf("expected ctrl+u in right pane to return to %q, got %+v", firstIssue.ID, got)
	}

	// 3. Across odd and even heights, stepping j from 0..end and k back to 0 must keep the
	// selected issue visible inside View() AND never orphan "↳ blocks" at the first row after RELATED ISSUES.
	assertInlineViewportAligned := func(height, step int, dir string) {
		t.Helper()
		sel := m.SelectedDrilldownIssue()
		if sel == nil {
			t.Fatalf("height=%d %s step=%d: nil SelectedDrilldownIssue", height, dir, step)
		}
		plain := ansi.Strip(m.View())
		if !strings.Contains(plain, sel.ID) {
			t.Fatalf("height=%d %s step=%d: selected issue %q clipped out of View():\n%s", height, dir, step, sel.ID, plain)
		}
		lines := strings.Split(plain, "\n")
		foundHeader := false
		for idx, ln := range lines {
			if strings.Contains(ln, "RELATED ISSUES (") {
				foundHeader = true
				if idx+1 < len(lines) && strings.Contains(lines[idx+1], "↳ blocks") {
					t.Fatalf("height=%d %s step=%d: orphaned '↳ blocks' at first related-issue row: %q\n%s",
						height, dir, step, lines[idx+1], plain)
				}
				break
			}
		}
		if !foundHeader {
			t.Fatalf("height=%d %s step=%d: RELATED ISSUES header missing from View():\n%s", height, dir, step, plain)
		}
	}

	for _, h := range []int{18, 19, 20, 21, 23, 25} {
		m.SetSize(120, h)
		_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
		assertInlineViewportAligned(h, 0, "init")
		for step := 1; step < 14; step++ {
			_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
			assertInlineViewportAligned(h, step, "down")
		}
		for step := 12; step >= 0; step-- {
			_ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
			assertInlineViewportAligned(h, step, "up")
		}
	}

	// 4. Full-screen drilldown: Enter opens drilldown, pgdown/pgup/ctrl+d/ctrl+u move cursor cleanly.
	m.SetSize(120, 19)
	_ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.View()), "Dependencies involving:") {
		t.Fatalf("expected Enter to open full-screen drilldown:\n%s", ansi.Strip(m.View()))
	}
	ddFirst := m.SelectedDrilldownIssue()
	if ddFirst == nil {
		t.Fatal("expected non-nil SelectedDrilldownIssue in full-screen drilldown")
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID == ddFirst.ID {
		t.Fatalf("expected pgdown in full-screen drilldown to advance from %q, got %+v", ddFirst.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != ddFirst.ID {
		t.Fatalf("expected pgup in full-screen drilldown to return to %q, got %+v", ddFirst.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID == ddFirst.ID {
		t.Fatalf("expected ctrl+d in full-screen drilldown to advance from %q, got %+v", ddFirst.ID, got)
	}
	_ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.SelectedDrilldownIssue(); got == nil || got.ID != ddFirst.ID {
		t.Fatalf("expected ctrl+u in full-screen drilldown to return to %q, got %+v", ddFirst.ID, got)
	}
}
