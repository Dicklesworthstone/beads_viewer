package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// makeHistoryReportCurrent installs a report on the model's history view and
// marks it current for the model's active dataset. Key handling in the history
// view is gated on historyReportIsCurrent(), so tests that drive history keys
// through Model.Update must install a current report first (in production the
// async HistoryLoadedMsg completion path does this).
func makeHistoryReportCurrent(m *Model, report *correlation.HistoryReport) {
	m.cancelHistoryLoad()
	m.historyLoading = false
	m.historyLoadFailed = false
	m.historyView.SetReport(report)
	m.historyReportDataGeneration = m.semanticDataGeneration
}

func createTestHistoryReport() *correlation.HistoryReport {
	now := time.Now()

	return &correlation.HistoryReport{
		GeneratedAt: now,
		Stats: correlation.HistoryStats{
			TotalBeads:       3,
			BeadsWithCommits: 3,
			TotalCommits:     5,
			UniqueAuthors:    2,
		},
		Histories: map[string]correlation.BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Fix authentication bug",
				Status: "closed",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "abc123def456",
						ShortSHA:   "abc123d",
						Message:    "fix: auth bug",
						Author:     "Dev One",
						Timestamp:  now,
						Method:     correlation.MethodCoCommitted,
						Confidence: 0.95,
					},
					{
						SHA:        "def456ghi789",
						ShortSHA:   "def456g",
						Message:    "test: add auth tests",
						Author:     "Dev One",
						Timestamp:  now.Add(-time.Hour),
						Method:     correlation.MethodExplicitID,
						Confidence: 0.90,
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "Add logging",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "abc123def456",
						ShortSHA:   "abc123d",
						Message:    "fix: auth bug",
						Author:     "Dev Two",
						Timestamp:  now,
						Method:     correlation.MethodTemporalAuthor,
						Confidence: 0.60,
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "Refactor database",
				Status: "in_progress",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "ghi789abc123",
						ShortSHA:   "ghi789a",
						Message:    "refactor: db layer",
						Author:     "Dev Two",
						Timestamp:  now.Add(-2 * time.Hour),
						Method:     correlation.MethodCoCommitted,
						Confidence: 0.92,
					},
					{
						SHA:        "jkl012mno345",
						ShortSHA:   "jkl012m",
						Message:    "refactor: db indexes",
						Author:     "Dev Two",
						Timestamp:  now.Add(-3 * time.Hour),
						Method:     correlation.MethodCoCommitted,
						Confidence: 0.88,
					},
				},
			},
		},
		CommitIndex: correlation.CommitIndex{
			"abc123def456": {"bv-1", "bv-2"},
			"def456ghi789": {"bv-1"},
			"ghi789abc123": {"bv-3"},
			"jkl012mno345": {"bv-3"},
		},
	}
}

func testTheme() Theme {
	return DefaultTheme(lipgloss.NewRenderer(nil))
}

func intsDiffer(got, want int) bool {
	return got != want
}

func stringsDiffer(got, want string) bool {
	return strings.Compare(got, want) != 0
}

func sameTimelineEntryType(got, want timelineEntryType) bool {
	return got == want
}

func fileTreeNodeIsNamed(node *FileTreeNode, name string) bool {
	return node != nil && strings.Compare(node.Name, name) == 0
}

func fileTreeNodeIsDirNamed(node *FileTreeNode, name string) bool {
	return fileTreeNodeIsNamed(node, name) && node.IsDir
}

func fileTreeNodeIsFileNamed(node *FileTreeNode, name string) bool {
	return fileTreeNodeIsNamed(node, name) && !node.IsDir
}

func TestNewHistoryModel(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()

	h := NewHistoryModel(report, theme)

	if h.report != report {
		t.Error("report not set correctly")
	}

	// Should have 3 histories with commits
	if len(h.histories) != 3 {
		t.Errorf("histories count = %d, want 3", len(h.histories))
	}

	if len(h.beadIDs) != 3 {
		t.Errorf("beadIDs count = %d, want 3", len(h.beadIDs))
	}
}

func TestHistoryModel_SetReport(t *testing.T) {
	theme := testTheme()
	h := NewHistoryModel(nil, theme)

	if len(h.histories) != 0 {
		t.Error("should have no histories with nil report")
	}

	report := createTestHistoryReport()
	h.SetReport(report)

	if len(h.histories) != 3 {
		t.Errorf("histories count after SetReport = %d, want 3", len(h.histories))
	}
}

func TestHistoryModel_SetReportPreservesVisibleFileTreeState(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReportWithFiles(), testTheme())
	h.ToggleFileTree()
	findVisible := func(path string) int {
		for i, node := range h.flatFileList {
			if node.Path == path {
				return i
			}
		}
		return -1
	}

	h.selectedFileIdx = findVisible("pkg")
	if h.selectedFileIdx < 0 {
		t.Fatal("pkg directory is not visible")
	}
	h.ToggleExpandFile()
	h.selectedFileIdx = findVisible("pkg/auth")
	if h.selectedFileIdx < 0 {
		t.Fatal("pkg/auth directory is not visible after expanding pkg")
	}
	h.ToggleExpandFile()
	h.selectedFileIdx = findVisible("pkg/auth/token.go")
	if h.selectedFileIdx < 0 {
		t.Fatal("token.go is not visible after expanding pkg/auth")
	}

	h.SetReport(createTestHistoryReportWithFiles())
	selected := h.SelectedFileNode()
	if selected == nil || selected.Path != "pkg/auth/token.go" {
		t.Fatalf("selected file after refresh=%v, want pkg/auth/token.go", selected)
	}
	for _, path := range []string{"pkg", "pkg/auth"} {
		idx := findVisible(path)
		if idx < 0 || !h.flatFileList[idx].Expanded {
			t.Fatalf("expanded directory %q was not preserved", path)
		}
	}
}

func TestHistoryModel_SetReportPreservesGitSelectionByIdentity(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReport(), testTheme())
	h.ToggleViewMode()
	commits := h.GetFilteredCommitList()
	for i := range commits {
		if commits[i].SHA == "abc123def456" {
			h.selectedGitCommit = i
			break
		}
	}
	selected := h.SelectedGitCommit()
	if selected == nil || selected.SHA != "abc123def456" || len(selected.BeadIDs) < 2 {
		t.Fatalf("shared commit fixture is unavailable: %+v", selected)
	}
	h.selectedRelatedBead = 1
	wantRelated := h.SelectedRelatedBeadID()

	refreshed := createTestHistoryReport()
	history := refreshed.Histories["bv-2"]
	history.Commits = append(history.Commits, correlation.CorrelatedCommit{
		SHA:       "newer-than-selection",
		ShortSHA:  "newer",
		Message:   "newer commit",
		Timestamp: time.Now().Add(time.Hour),
	})
	refreshed.Histories["bv-2"] = history
	h.SetReport(refreshed)

	selected = h.SelectedGitCommit()
	if selected == nil || selected.SHA != "abc123def456" {
		t.Fatalf("selected commit after refresh=%+v, want abc123def456", selected)
	}
	if got := h.SelectedRelatedBeadID(); got != wantRelated {
		t.Fatalf("selected related bead after refresh=%q, want %q", got, wantRelated)
	}
}

func TestHistoryModel_SetReportPreservesBeadAndCommitSelectionByIdentity(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReport(), testTheme())
	for i, beadID := range h.beadIDs {
		if beadID == "bv-1" {
			h.selectedBead = i
			break
		}
	}
	selectedHistory := h.SelectedHistory()
	if selectedHistory == nil || selectedHistory.BeadID != "bv-1" || len(selectedHistory.Commits) < 2 {
		t.Fatalf("multi-commit bead fixture is unavailable: %+v", selectedHistory)
	}
	h.selectedCommit = 1
	wantSHA := h.SelectedCommit().SHA

	refreshed := createTestHistoryReport()
	history := refreshed.Histories["bv-1"]
	history.Commits = append([]correlation.CorrelatedCommit{{
		SHA:       "newer-than-selection",
		ShortSHA:  "newer",
		Message:   "newer commit",
		Timestamp: time.Now().Add(time.Hour),
	}}, history.Commits...)
	refreshed.Histories["bv-1"] = history
	h.SetReport(refreshed)

	if got := h.SelectedBeadID(); got != "bv-1" {
		t.Fatalf("selected bead after refresh=%q, want bv-1", got)
	}
	if selected := h.SelectedCommit(); selected == nil || selected.SHA != wantSHA {
		t.Fatalf("selected bead commit after refresh=%+v, want SHA %s", selected, wantSHA)
	}
}

func TestHistoryModel_SetReportClampsDerivedScrollOffsets(t *testing.T) {
	report := createTestHistoryReport()
	h := NewHistoryModel(report, DefaultTheme(nil))
	h.SetSize(100, 20)
	h.middleScrollOffset = 1_000
	h.timelineScrollOffset = 1_000

	refreshed := createTestHistoryReport()
	for id, history := range refreshed.Histories {
		if len(history.Commits) > 1 {
			history.Commits = history.Commits[:1]
			refreshed.Histories[id] = history
		}
	}
	h.SetReport(refreshed)

	if h.middleScrollOffset != 0 {
		t.Fatalf("middleScrollOffset=%d after shorter report, want 0", h.middleScrollOffset)
	}
	if h.timelineScrollOffset != 0 {
		t.Fatalf("timelineScrollOffset=%d after refreshed report, want 0", h.timelineScrollOffset)
	}
}

func TestHistoryModel_SetReportRefreshesHiddenFileTree(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReportWithFiles(), testTheme())
	h.ToggleFileTree()
	h.ToggleFileTree()
	if h.showFileTree || h.fileTree == nil {
		t.Fatal("fixture did not leave a built but hidden file tree")
	}

	refreshed := createTestHistoryReportWithFiles()
	for beadID, history := range refreshed.Histories {
		for i := range history.Commits {
			history.Commits[i].Files = []correlation.FileChange{{Path: "new/tree.go"}}
		}
		refreshed.Histories[beadID] = history
	}
	h.SetReport(refreshed)
	h.ToggleFileTree()

	visibleRoots := make(map[string]bool)
	for _, node := range h.flatFileList {
		visibleRoots[node.Path] = true
	}
	if !visibleRoots["new"] {
		t.Fatalf("reopened file tree omitted refreshed root: %v", visibleRoots)
	}
	if visibleRoots["pkg"] {
		t.Fatalf("reopened file tree retained stale root: %v", visibleRoots)
	}
}

func TestHistoryModel_Navigation(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(100, 40)

	// Start at first bead
	if h.selectedBead != 0 {
		t.Errorf("initial selectedBead = %d, want 0", h.selectedBead)
	}

	// Move down
	h.MoveDown()
	if h.selectedBead != 1 {
		t.Errorf("selectedBead after MoveDown = %d, want 1", h.selectedBead)
	}

	// Move up
	h.MoveUp()
	if h.selectedBead != 0 {
		t.Errorf("selectedBead after MoveUp = %d, want 0", h.selectedBead)
	}

	// Can't move up past 0
	h.MoveUp()
	if h.selectedBead != 0 {
		t.Errorf("selectedBead should stay at 0, got %d", h.selectedBead)
	}
}

func TestHistoryModel_ToggleFocus(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Start with list focus
	if h.focused != historyFocusList {
		t.Errorf("initial focus = %v, want historyFocusList", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusDetail {
		t.Errorf("focus after toggle = %v, want historyFocusDetail", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusList {
		t.Errorf("focus after second toggle = %v, want historyFocusList", h.focused)
	}
}

func TestHistoryModel_Selection(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Get selected bead ID
	beadID := h.SelectedBeadID()
	if beadID == "" {
		t.Error("SelectedBeadID() returned empty string")
	}

	// Get selected history
	hist := h.SelectedHistory()
	if hist == nil {
		t.Fatal("SelectedHistory() returned nil")
	}
	if hist.BeadID != beadID {
		t.Errorf("SelectedHistory().BeadID = %s, want %s", hist.BeadID, beadID)
	}

	// Get selected commit
	commit := h.SelectedCommit()
	if commit == nil {
		t.Fatal("SelectedCommit() returned nil")
	}
	if commit.SHA == "" {
		t.Error("SelectedCommit().SHA is empty")
	}
}

func TestHistoryModel_AuthorFilter(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initially all 3 beads
	if len(h.histories) != 3 {
		t.Errorf("initial histories count = %d, want 3", len(h.histories))
	}

	// Filter by "Dev One"
	h.SetAuthorFilter("Dev One")
	if len(h.histories) != 1 {
		t.Errorf("histories after 'Dev One' filter = %d, want 1", len(h.histories))
	}

	// Check the remaining bead is bv-1
	if len(h.beadIDs) != 1 || h.beadIDs[0] != "bv-1" {
		t.Errorf("filtered beadID = %v, want [bv-1]", h.beadIDs)
	}

	// Clear filter
	h.SetAuthorFilter("")
	if len(h.histories) != 3 {
		t.Errorf("histories after clearing filter = %d, want 3", len(h.histories))
	}
}

func TestHistoryModel_ConfidenceFilter(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Filter by high confidence (>=0.85)
	h.SetMinConfidence(0.85)

	// bv-1 has commits at 0.95 and 0.90 - should be included
	// bv-2 has only 0.60 - should be excluded
	// bv-3 has 0.92 and 0.88 - should be included
	if len(h.histories) != 2 {
		t.Errorf("histories after confidence filter = %d, want 2", len(h.histories))
	}

	// Reset filter
	h.SetMinConfidence(0)
	if len(h.histories) != 3 {
		t.Errorf("histories after clearing confidence filter = %d, want 3", len(h.histories))
	}
}

func TestHistoryModel_EmptyReport(t *testing.T) {
	theme := testTheme()

	// Test with nil report
	h := NewHistoryModel(nil, theme)
	if h.SelectedBeadID() != "" {
		t.Error("SelectedBeadID() should return empty for nil report")
	}
	if h.SelectedHistory() != nil {
		t.Error("SelectedHistory() should return nil for nil report")
	}
	if h.SelectedCommit() != nil {
		t.Error("SelectedCommit() should return nil for nil report")
	}

	// Test with empty histories
	emptyReport := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{},
	}
	h2 := NewHistoryModel(emptyReport, theme)
	if len(h2.histories) != 0 {
		t.Errorf("histories count = %d, want 0", len(h2.histories))
	}
}

func TestHistoryModel_DetailNavigation(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Find the bead with 2 commits (bv-1 or bv-3)
	var beadWithTwoCommits int
	for i, hist := range h.histories {
		if len(hist.Commits) >= 2 {
			beadWithTwoCommits = i
			break
		}
	}
	h.selectedBead = beadWithTwoCommits

	// Switch to detail focus
	h.ToggleFocus()
	if h.focused != historyFocusDetail {
		t.Fatal("should be in detail focus")
	}

	// Initial commit selection
	if h.selectedCommit != 0 {
		t.Errorf("initial selectedCommit = %d, want 0", h.selectedCommit)
	}

	// Move down in commits
	h.MoveDown()
	if h.selectedCommit != 1 {
		t.Errorf("selectedCommit after MoveDown = %d, want 1", h.selectedCommit)
	}

	// Move up in commits
	h.MoveUp()
	if h.selectedCommit != 0 {
		t.Errorf("selectedCommit after MoveUp = %d, want 0", h.selectedCommit)
	}
}

func TestHistoryModel_View(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(120, 40)

	// Should render without panic
	view := h.View()
	if view == "" {
		t.Error("View() returned empty string")
	}

	// Should contain header
	if len(view) < 100 {
		t.Errorf("View() seems too short: %d chars", len(view))
	}
}

func TestHistoryModel_ViewEmpty(t *testing.T) {
	theme := testTheme()

	// Test with nil report
	h := NewHistoryModel(nil, theme)
	h.SetSize(80, 24)

	view := h.View()
	if view == "" {
		t.Error("View() for nil report returned empty")
	}
}

func TestHistoryModel_ViewSmallWidth(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Test various small widths - should not panic
	smallWidths := []int{5, 6, 7, 10, 15, 20}
	for _, w := range smallWidths {
		h.SetSize(w, 10)
		// This should not panic
		view := h.View()
		if view == "" {
			t.Errorf("View() with width %d returned empty", w)
		}
	}
}

func TestHistoryModel_SortByCommitCount(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Verify histories are sorted by commit count (descending)
	// bv-1 has 2 commits, bv-3 has 2 commits, bv-2 has 1 commit
	// Ties are broken by bead ID
	if len(h.histories) < 2 {
		t.Fatal("not enough histories for sort test")
	}

	// The last bead should have fewest commits
	lastHist := h.histories[len(h.histories)-1]
	if len(lastHist.Commits) > 1 {
		t.Errorf("last bead has %d commits, expected 1 (bv-2)", len(lastHist.Commits))
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is a…"},
		{"abc", 5, "abc"},
		{"hello", 5, "hello"},
		{"hello!", 5, "hell…"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestMethodLabel(t *testing.T) {
	tests := []struct {
		method correlation.CorrelationMethod
		want   string
	}{
		{correlation.MethodCoCommitted, "(co-committed)"},
		{correlation.MethodExplicitID, "(explicit ID)"},
		{correlation.MethodTemporalAuthor, "(temporal)"},
		{correlation.CorrelationMethod("unknown"), ""},
	}

	for _, tt := range tests {
		got := methodLabel(tt.method)
		if got != tt.want {
			t.Errorf("methodLabel(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}

func TestHistoryModel_GetHistoryForBead(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Get existing bead history
	hist := h.GetHistoryForBead("bv-1")
	if hist == nil {
		t.Fatal("GetHistoryForBead(bv-1) returned nil")
	}
	if hist.BeadID != "bv-1" {
		t.Errorf("GetHistoryForBead(bv-1).BeadID = %s, want bv-1", hist.BeadID)
	}

	// Get non-existent bead history
	histNone := h.GetHistoryForBead("bv-nonexistent")
	if histNone != nil {
		t.Error("GetHistoryForBead(bv-nonexistent) should return nil")
	}
}

func TestHistoryModel_HasReport(t *testing.T) {
	theme := testTheme()

	// Without report
	h := NewHistoryModel(nil, theme)
	if h.HasReport() {
		t.Error("HasReport() should return false with nil report")
	}

	// With report
	report := createTestHistoryReport()
	h2 := NewHistoryModel(report, theme)
	if !h2.HasReport() {
		t.Error("HasReport() should return true with report")
	}
}

func TestHistoryModel_CommitNavigation(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Find a bead with 2 commits
	for i, hist := range h.histories {
		if len(hist.Commits) >= 2 {
			h.selectedBead = i
			break
		}
	}

	// Start at first commit
	if h.selectedCommit != 0 {
		t.Errorf("initial selectedCommit = %d, want 0", h.selectedCommit)
	}

	// NextCommit moves to next
	h.NextCommit()
	if h.selectedCommit != 1 {
		t.Errorf("selectedCommit after NextCommit = %d, want 1", h.selectedCommit)
	}

	// PrevCommit moves back
	h.PrevCommit()
	if h.selectedCommit != 0 {
		t.Errorf("selectedCommit after PrevCommit = %d, want 0", h.selectedCommit)
	}

	// PrevCommit at 0 stays at 0
	h.PrevCommit()
	if h.selectedCommit != 0 {
		t.Errorf("selectedCommit after PrevCommit at 0 = %d, want 0", h.selectedCommit)
	}
}

func TestHistoryModel_CycleConfidence(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initial confidence is 0
	if h.GetMinConfidence() != 0 {
		t.Errorf("initial confidence = %f, want 0", h.GetMinConfidence())
	}

	// Cycle through thresholds
	h.CycleConfidence()
	if h.GetMinConfidence() != 0.5 {
		t.Errorf("confidence after first cycle = %f, want 0.5", h.GetMinConfidence())
	}

	h.CycleConfidence()
	if h.GetMinConfidence() != 0.75 {
		t.Errorf("confidence after second cycle = %f, want 0.75", h.GetMinConfidence())
	}

	h.CycleConfidence()
	if h.GetMinConfidence() != 0.9 {
		t.Errorf("confidence after third cycle = %f, want 0.9", h.GetMinConfidence())
	}

	// Wrap around to 0
	h.CycleConfidence()
	if h.GetMinConfidence() != 0 {
		t.Errorf("confidence after fourth cycle = %f, want 0", h.GetMinConfidence())
	}
}

// =============================================================================
// VIEW MODE SWITCHING TESTS (bv-tl3n)
// =============================================================================

func TestHistoryModel_ToggleViewMode(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initial mode is Bead mode
	if h.IsGitMode() {
		t.Error("initial mode should be Bead mode, not Git mode")
	}

	// Toggle to Git mode
	h.ToggleViewMode()
	if !h.IsGitMode() {
		t.Error("should be in Git mode after toggle")
	}

	// Verify commit list was built
	if len(h.commitList) == 0 {
		t.Error("commitList should be built when switching to Git mode")
	}

	// Toggle back to Bead mode
	h.ToggleViewMode()
	if h.IsGitMode() {
		t.Error("should be back in Bead mode after second toggle")
	}
}

func TestHistoryModel_BuildCommitList(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Switch to Git mode to build commit list
	h.ToggleViewMode()

	// Should have commits from all beads
	// Test data has: bv-1 with 2 commits, bv-2 with 1, bv-3 with 2
	// abc123def456 is shared by bv-1 and bv-2
	// Total unique commits: 4 (abc123, def456, ghi789, jkl012)
	if len(h.commitList) < 1 {
		t.Error("commitList should have commits")
	}

	// Verify commits have bead associations
	for _, commit := range h.commitList {
		if len(commit.BeadIDs) == 0 {
			t.Errorf("commit %s should have at least one associated bead", commit.ShortSHA)
		}
	}
}

func TestHistoryModel_SelectedGitCommit(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// In bead mode, SelectedGitCommit returns nil
	h.ToggleViewMode() // Switch to git mode

	commit := h.SelectedGitCommit()
	if commit == nil {
		t.Fatal("SelectedGitCommit() should return a commit in git mode")
	}

	if commit.SHA == "" {
		t.Error("SelectedGitCommit().SHA should not be empty")
	}
}

func TestHistoryModel_SelectedRelatedBeadID(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode

	beadID := h.SelectedRelatedBeadID()
	if beadID == "" {
		t.Error("SelectedRelatedBeadID() should return a bead ID")
	}
}

// =============================================================================
// GIT MODE NAVIGATION TESTS
// =============================================================================

func TestHistoryModel_MoveUpDownGit(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(100, 40)

	h.ToggleViewMode() // Git mode

	// Start at first commit
	if h.selectedGitCommit != 0 {
		t.Errorf("initial selectedGitCommit = %d, want 0", h.selectedGitCommit)
	}

	// Move down
	h.MoveDownGit()
	if h.selectedGitCommit != 1 {
		t.Errorf("selectedGitCommit after MoveDownGit = %d, want 1", h.selectedGitCommit)
	}

	// Move up
	h.MoveUpGit()
	if h.selectedGitCommit != 0 {
		t.Errorf("selectedGitCommit after MoveUpGit = %d, want 0", h.selectedGitCommit)
	}

	// Can't go below 0
	h.MoveUpGit()
	if h.selectedGitCommit != 0 {
		t.Errorf("selectedGitCommit should stay at 0, got %d", h.selectedGitCommit)
	}
}

func TestHistoryModel_NextPrevRelatedBead(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode

	// Find a commit with multiple beads
	var commitWithMultipleBeads int = -1
	for i, commit := range h.commitList {
		if len(commit.BeadIDs) >= 2 {
			commitWithMultipleBeads = i
			break
		}
	}

	if commitWithMultipleBeads < 0 {
		t.Skip("No commit with multiple beads for related bead navigation test")
	}

	h.selectedGitCommit = commitWithMultipleBeads
	h.selectedRelatedBead = 0

	// Move to next related bead
	h.NextRelatedBead()
	if h.selectedRelatedBead != 1 {
		t.Errorf("selectedRelatedBead after NextRelatedBead = %d, want 1", h.selectedRelatedBead)
	}

	// Move back
	h.PrevRelatedBead()
	if h.selectedRelatedBead != 0 {
		t.Errorf("selectedRelatedBead after PrevRelatedBead = %d, want 0", h.selectedRelatedBead)
	}

	// Can't go below 0
	h.PrevRelatedBead()
	if h.selectedRelatedBead != 0 {
		t.Errorf("selectedRelatedBead should stay at 0, got %d", h.selectedRelatedBead)
	}
}

// =============================================================================
// SEARCH AND FILTER TESTS (bv-nkrj)
// =============================================================================

func TestHistoryModel_StartCancelSearch(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initially search is inactive
	if h.IsSearchActive() {
		t.Error("search should not be active initially")
	}

	// Start search
	h.StartSearch()
	if !h.IsSearchActive() {
		t.Error("search should be active after StartSearch()")
	}

	// Cancel search
	h.CancelSearch()
	if h.IsSearchActive() {
		t.Error("search should not be active after CancelSearch()")
	}
}

func TestHistoryModel_SearchQuery(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.StartSearch()

	// Initially empty
	if h.SearchQuery() != "" {
		t.Error("search query should be empty initially")
	}
}

func TestHistoryModel_GetSearchModeName(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	tests := []struct {
		mode historySearchMode
		want string
	}{
		{searchModeAll, "all"},
		{searchModeCommit, "msg"},
		{searchModeSHA, "sha"},
		{searchModeBead, "bead"},
		{searchModeAuthor, "author"},
	}

	for _, tt := range tests {
		h.searchMode = tt.mode
		if got := h.GetSearchModeName(); got != tt.want {
			t.Errorf("GetSearchModeName() for mode %d = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestHistoryModel_StartSearchWithMode(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	modes := []historySearchMode{
		searchModeCommit,
		searchModeSHA,
		searchModeBead,
		searchModeAuthor,
	}

	for _, mode := range modes {
		h.CancelSearch() // Reset
		h.StartSearchWithMode(mode)

		if !h.IsSearchActive() {
			t.Errorf("search should be active after StartSearchWithMode(%d)", mode)
		}
		if h.searchMode != mode {
			t.Errorf("searchMode after StartSearchWithMode(%d) = %d, want %d", mode, h.searchMode, mode)
		}
	}
}

func TestHistoryModel_ClearSearch(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.StartSearch()
	h.searchInput.SetValue("test")

	h.ClearSearch()

	if h.SearchQuery() != "" {
		t.Error("search query should be empty after ClearSearch()")
	}
	// Search mode should still be active (unlike CancelSearch)
	if !h.IsSearchActive() {
		t.Error("search should still be active after ClearSearch()")
	}
}

func TestHistoryModel_GetFilteredCommitList(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode

	// Without filter, should return full commit list
	filtered := h.GetFilteredCommitList()
	if len(filtered) != len(h.commitList) {
		t.Errorf("GetFilteredCommitList() without filter should return full list")
	}
}

func TestHistoryModel_GitSearchWithNoMatchesStaysEmpty(t *testing.T) {
	report := createTestHistoryReport()
	h := NewHistoryModel(report, testTheme())
	h.ToggleViewMode()
	if len(h.commitList) == 0 {
		t.Fatal("fixture has no commits")
	}

	h.StartSearch()
	h.searchInput.SetValue("definitely-no-such-history-entry")
	h.applySearchFilter()

	if h.filteredCommits == nil {
		t.Fatal("active zero-result query used nil no-filter sentinel")
	}
	if got := h.GetFilteredCommitList(); len(got) != 0 {
		t.Fatalf("zero-result query exposed %d unfiltered commits", len(got))
	}
}

func TestHistoryModel_ToggleViewModePreservesFinishedSearch(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.StartSearch()
	h.searchInput.SetValue("indexes")
	h.applySearchFilter()
	h.FinishSearch()

	if h.IsSearchActive() {
		t.Fatal("search should be inactive after FinishSearch()")
	}
	if got := h.beadIDs; len(got) != 1 || got[0] != "bv-3" {
		t.Fatalf("filtered bead IDs before mode toggle = %v, want [bv-3]", got)
	}
	if filterLine := h.renderFilterLine(); !strings.Contains(filterLine, "\"indexes\"") {
		t.Fatalf("filter line after finished search = %q, want query badge", filterLine)
	}

	h.ToggleViewMode()

	filteredCommits := h.GetFilteredCommitList()
	if len(filteredCommits) != 1 {
		t.Fatalf("filtered commits after finished-search mode toggle = %d, want 1", len(filteredCommits))
	}
	if filteredCommits[0].Message != "refactor: db indexes" {
		t.Fatalf("filtered commit after mode toggle = %q, want %q", filteredCommits[0].Message, "refactor: db indexes")
	}
}

// =============================================================================
// LAYOUT CALCULATION TESTS (bv-xrfh)
// =============================================================================

func TestHistoryModel_DetermineLayout(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	tests := []struct {
		width  int
		layout historyLayout
	}{
		{80, layoutNarrow},    // < 100 = narrow
		{99, layoutNarrow},    // < 100 = narrow
		{100, layoutStandard}, // >= 100 < 150 = standard
		{120, layoutStandard}, // >= 100 < 150 = standard
		{149, layoutStandard}, // >= 100 < 150 = standard
		{150, layoutWide},     // >= 150 = wide
		{200, layoutWide},     // >= 150 = wide
	}

	for _, tt := range tests {
		h.SetSize(tt.width, 40)
		got := h.determineLayout()
		if got != tt.layout {
			t.Errorf("determineLayout() at width %d = %d, want %d", tt.width, got, tt.layout)
		}
	}
}

func TestHistoryModel_PaneCount(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Narrow width = 2 panes
	h.SetSize(80, 40)
	if h.paneCount() != 2 {
		t.Errorf("paneCount() at narrow width = %d, want 2", h.paneCount())
	}

	// Standard width = 3 panes
	h.SetSize(120, 40)
	if h.paneCount() != 3 {
		t.Errorf("paneCount() at standard width = %d, want 3", h.paneCount())
	}

	// Wide non-git history view = 4 panes
	h.SetSize(160, 40)
	if h.paneCount() != 4 {
		t.Errorf("paneCount() at wide width = %d, want 4", h.paneCount())
	}

	// Wide git view keeps the 3-pane commit layout
	h.viewMode = historyModeGit
	if h.paneCount() != 3 {
		t.Errorf("paneCount() at wide width in git mode = %d, want 3", h.paneCount())
	}
}

func TestHistoryModel_ToggleFocusThreePane(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Set to 3-pane layout
	h.SetSize(120, 40)

	// Start at list
	if h.focused != historyFocusList {
		t.Errorf("initial focus = %d, want historyFocusList", h.focused)
	}

	// Toggle to middle
	h.ToggleFocus()
	if h.focused != historyFocusMiddle {
		t.Errorf("focus after first toggle = %d, want historyFocusMiddle", h.focused)
	}

	// Toggle to detail
	h.ToggleFocus()
	if h.focused != historyFocusDetail {
		t.Errorf("focus after second toggle = %d, want historyFocusDetail", h.focused)
	}

	// Toggle back to list
	h.ToggleFocus()
	if h.focused != historyFocusList {
		t.Errorf("focus after third toggle = %d, want historyFocusList", h.focused)
	}
}

func TestHistoryModel_ToggleFocusFourPane(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.SetSize(160, 40)

	if h.focused != historyFocusList {
		t.Errorf("initial focus = %d, want historyFocusList", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusTimeline {
		t.Errorf("focus after first toggle = %d, want historyFocusTimeline", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusMiddle {
		t.Errorf("focus after second toggle = %d, want historyFocusMiddle", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusDetail {
		t.Errorf("focus after third toggle = %d, want historyFocusDetail", h.focused)
	}

	h.ToggleFocus()
	if h.focused != historyFocusList {
		t.Errorf("focus after fourth toggle = %d, want historyFocusList", h.focused)
	}
}

func TestHistoryModel_ListHeight(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.SetSize(100, 40)
	expected := h.currentPanelHeight() - 5 // panel height minus bordered panel header/separator/borders
	if intsDiffer(h.listHeight(), expected) {
		t.Errorf("listHeight() = %d, want %d", h.listHeight(), expected)
	}
	if gotHeight := lipgloss.Height(h.View()); gotHeight > 40 {
		t.Errorf("View() height = %d, want <= 40", gotHeight)
	}
}

// =============================================================================
// HELPER FUNCTION TESTS
// =============================================================================

func TestAuthorInitials(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"John Doe", "JD"},
		{"Alice", "AL"},
		{"Bob Smith Jr", "BJ"},
		{"", "??"},
		{"X", "X"},
		{"张三", "张三"}, // Unicode support
	}

	for _, tt := range tests {
		got := authorInitials(tt.name)
		if got != tt.want {
			t.Errorf("authorInitials(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		time     time.Time
		contains string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "m ago"},
		{now.Add(-3 * time.Hour), "h ago"},
		{now.Add(-2 * 24 * time.Hour), "d ago"},
		{now.Add(-2 * 7 * 24 * time.Hour), "w ago"},
		{now.Add(-2 * 30 * 24 * time.Hour), "mo ago"},
		{now.Add(-2 * 365 * 24 * time.Hour), "y ago"},
	}

	for _, tt := range tests {
		got := relativeTime(tt.time)
		if !strings.Contains(got, tt.contains) {
			t.Errorf("relativeTime(%v) = %q, want to contain %q", tt.time, got, tt.contains)
		}
	}
}

func TestRelativeTimeFuture(t *testing.T) {
	futureTime := time.Now().Add(1 * time.Hour)
	got := relativeTime(futureTime)
	if got != "in future" {
		t.Errorf("relativeTime(future) = %q, want 'in future'", got)
	}
}

func TestParseConventionalCommit(t *testing.T) {
	tests := []struct {
		msg          string
		wantConv     bool
		wantType     string
		wantScope    string
		wantBreaking bool
		wantSubject  string
	}{
		{"feat: add new feature", true, "feat", "", false, "add new feature"},
		{"fix(auth): resolve login bug", true, "fix", "auth", false, "resolve login bug"},
		{"feat!: breaking change", true, "feat", "", true, "breaking change"},
		{"feat(api)!: breaking api change", true, "feat", "api", true, "breaking api change"},
		{"chore: update deps", true, "chore", "", false, "update deps"},
		{"regular commit message", false, "", "", false, "regular commit message"},
		{"Merge branch 'main'", false, "", "", false, "Merge branch 'main'"},
	}

	for _, tt := range tests {
		cc := parseConventionalCommit(tt.msg)
		if cc.IsConventional != tt.wantConv {
			t.Errorf("parseConventionalCommit(%q).IsConventional = %v, want %v",
				tt.msg, cc.IsConventional, tt.wantConv)
		}
		if cc.Type != tt.wantType {
			t.Errorf("parseConventionalCommit(%q).Type = %q, want %q",
				tt.msg, cc.Type, tt.wantType)
		}
		if cc.Scope != tt.wantScope {
			t.Errorf("parseConventionalCommit(%q).Scope = %q, want %q",
				tt.msg, cc.Scope, tt.wantScope)
		}
		if cc.Breaking != tt.wantBreaking {
			t.Errorf("parseConventionalCommit(%q).Breaking = %v, want %v",
				tt.msg, cc.Breaking, tt.wantBreaking)
		}
		if cc.Subject != tt.wantSubject {
			t.Errorf("parseConventionalCommit(%q).Subject = %q, want %q",
				tt.msg, cc.Subject, tt.wantSubject)
		}
	}
}

func TestCommitTypeIndicator(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"feat: new feature", "✨"},
		{"fix: bug fix", "🐛"},
		{"docs: update readme", "📝"},
		{"refactor: clean up", "♻"},
		{"test: add tests", "🧪"},
		{"chore: update deps", "🔧"},
		{"perf: optimize", "⚡"},
		{"Merge branch 'main'", "⊕"},
		{"Revert 'some commit'", "↩"},
		{"regular message", ""},
	}

	for _, tt := range tests {
		got := commitTypeIndicator(tt.msg)
		if got != tt.want {
			t.Errorf("commitTypeIndicator(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestFormatCycleTime(t *testing.T) {
	tests := []struct {
		days float64
		want string
	}{
		{0.01, "14m"}, // 0.01 * 24 = 0.24 hours < 1, so minutes: 0.24 * 60 = 14.4 → "14m"
		{0.1, "2.4h"}, // 0.1 * 24 = 2.4 hours >= 1, so "2.4h"
		{2.5, "2.5d"}, // 2.5 days < 7, so "2.5d"
		{10, "1.4w"},  // 10 days >= 7, so weeks: 10/7 = 1.43 → "1.4w"
	}

	for _, tt := range tests {
		got := formatCycleTime(tt.days)
		if got != tt.want {
			t.Errorf("formatCycleTime(%v) = %q, want %q", tt.days, got, tt.want)
		}
	}
}

func TestFileActionIcon(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{"A", "+"},
		{"D", "-"},
		{"M", "~"},
		{"R", "→"},
		{"X", "?"},
	}

	for _, tt := range tests {
		got := fileActionIcon(tt.action)
		if got != tt.want {
			t.Errorf("fileActionIcon(%q) = %q, want %q", tt.action, got, tt.want)
		}
	}
}

func TestGroupFilesByDirectory(t *testing.T) {
	files := []correlation.FileChange{
		{Path: "pkg/ui/model.go"},
		{Path: "pkg/ui/view.go"},
		{Path: "cmd/main.go"},
		{Path: "README.md"},
	}

	groups := groupFilesByDirectory(files)

	if len(groups) != 3 {
		t.Errorf("groupFilesByDirectory returned %d groups, want 3", len(groups))
	}

	// First group should be pkg/ui with 2 files
	pkgUIFound := false
	for _, g := range groups {
		if g.Dir == "pkg/ui" && len(g.Files) == 2 {
			pkgUIFound = true
			break
		}
	}
	if !pkgUIFound {
		t.Error("expected pkg/ui group with 2 files")
	}
}

func TestEventTypeIcon(t *testing.T) {
	tests := []struct {
		et   correlation.EventType
		want string
	}{
		{correlation.EventCreated, "🆕"},
		{correlation.EventClaimed, "👤"},
		{correlation.EventClosed, "✓"},
		{correlation.EventReopened, "↺"},
		{correlation.EventModified, "✎"},
		{correlation.EventType("unknown"), "•"},
	}

	for _, tt := range tests {
		got := eventTypeIcon(tt.et)
		if got != tt.want {
			t.Errorf("eventTypeIcon(%q) = %q, want %q", tt.et, got, tt.want)
		}
	}
}

func TestEventTypeLabel(t *testing.T) {
	tests := []struct {
		et   correlation.EventType
		want string
	}{
		{correlation.EventCreated, "Created"},
		{correlation.EventClaimed, "Claimed"},
		{correlation.EventClosed, "Closed"},
		{correlation.EventReopened, "Reopened"},
		{correlation.EventModified, "Modified"},
	}

	for _, tt := range tests {
		got := eventTypeLabel(tt.et)
		if got != tt.want {
			t.Errorf("eventTypeLabel(%q) = %q, want %q", tt.et, got, tt.want)
		}
	}
}

// =============================================================================
// LAYOUT RENDERING TESTS
// =============================================================================

func TestHistoryModel_ViewNarrowLayout(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Narrow width (< 100)
	h.SetSize(80, 24)
	view := h.View()

	if view == "" {
		t.Error("View() with narrow layout returned empty")
	}
}

func TestHistoryModel_ViewStandardLayout(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Standard width (100-150)
	h.SetSize(120, 30)
	view := h.View()

	if view == "" {
		t.Error("View() with standard layout returned empty")
	}
}

func TestHistoryModel_ViewWideLayout(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Wide width (>= 150)
	h.SetSize(160, 35)
	view := h.View()

	if view == "" {
		t.Error("View() with wide layout returned empty")
	}
}

func TestHistoryModel_ViewGitModeNarrow(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode
	h.SetSize(80, 24)

	view := h.View()
	if view == "" {
		t.Error("View() in Git mode with narrow layout returned empty")
	}
}

func TestHistoryModel_ViewGitModeWide(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode
	h.SetSize(160, 35)

	view := h.View()
	if view == "" {
		t.Error("View() in Git mode with wide layout returned empty")
	}
}

func TestHistoryModel_ViewNoCommitsInGitMode(t *testing.T) {
	theme := testTheme()

	// Create report with beads but no commits
	emptyReport := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"bv-1": {BeadID: "bv-1", Title: "No commits", Commits: nil},
		},
	}
	h := NewHistoryModel(emptyReport, theme)
	h.SetSize(100, 30)
	h.ToggleViewMode() // Git mode

	view := h.View()
	if view == "" {
		t.Error("View() with no commits should show empty message")
	}
	if !strings.Contains(view, "No commits") {
		t.Error("View() should indicate no commits with correlations")
	}
}

func TestHistoryModel_EnsureBeadVisible(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Set very small height so scrolling is needed
	h.SetSize(100, 8)

	// Select last bead
	h.selectedBead = len(h.histories) - 1
	h.ensureBeadVisible()

	// Scroll offset should be adjusted
	if h.scrollOffset < 0 {
		t.Error("scrollOffset should be >= 0")
	}
}

func TestHistoryModel_EnsureGitCommitVisible(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	h.ToggleViewMode() // Git mode
	h.SetSize(100, 8)

	// Select last commit
	if len(h.commitList) > 0 {
		h.selectedGitCommit = len(h.commitList) - 1
		h.ensureGitCommitVisible()

		if h.gitScrollOffset < 0 {
			t.Error("gitScrollOffset should be >= 0")
		}
	}
}

func TestHistoryModel_ToggleExpand(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initially not expanded
	if len(h.expandedBeads) != 0 {
		t.Error("expandedBeads should be empty initially")
	}

	// Toggle expand
	h.ToggleExpand()
	beadID := h.SelectedBeadID()
	if !h.expandedBeads[beadID] {
		t.Error("bead should be expanded after ToggleExpand()")
	}

	// Toggle again to collapse
	h.ToggleExpand()
	if h.expandedBeads[beadID] {
		t.Error("bead should be collapsed after second ToggleExpand()")
	}
}

// =============================================================================
// EDGE CASE TESTS
// =============================================================================

func TestHistoryModel_NavigationEmptyList(t *testing.T) {
	theme := testTheme()

	emptyReport := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{},
	}
	h := NewHistoryModel(emptyReport, theme)

	// Navigation on empty history keeps cursors at zero and returns empty selections
	h.MoveUp()
	h.MoveDown()
	h.NextCommit()
	h.PrevCommit()

	if h.selectedBead != 0 || h.selectedCommit != 0 {
		t.Fatalf("empty history cursors drifted: selectedBead=%d selectedCommit=%d", h.selectedBead, h.selectedCommit)
	}
	if id := h.SelectedBeadID(); id != "" {
		t.Errorf("expected empty SelectedBeadID on empty history, got %q", id)
	}
	if commit := h.SelectedCommit(); commit != nil {
		t.Errorf("expected nil SelectedCommit on empty history, got %+v", commit)
	}
	h.SetSize(100, 30)
	if strings.TrimSpace(h.View()) == "" {
		t.Error("expected empty history View() to render non-empty empty-state")
	}
}

func TestHistoryModel_GitModeEmptyCommitList(t *testing.T) {
	theme := testTheme()

	emptyReport := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{},
	}
	h := NewHistoryModel(emptyReport, theme)

	h.ToggleViewMode() // Git mode

	// Should not panic
	h.MoveUpGit()
	h.MoveDownGit()
	h.NextRelatedBead()
	h.PrevRelatedBead()

	if h.SelectedGitCommit() != nil {
		t.Error("SelectedGitCommit() should return nil for empty list")
	}
}

// =============================================================================
// TIMELINE TESTS (bv-1x6o)
// =============================================================================

func TestBuildTimeline(t *testing.T) {
	theme := testTheme()
	now := time.Now()

	tests := []struct {
		name              string
		history           correlation.BeadHistory
		wantEntries       int
		wantEventTypes    []string
		wantCommitEntries int
	}{
		{
			name: "full lifecycle with commits",
			history: correlation.BeadHistory{
				Title:  "Test Bead",
				Status: "closed",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{
						Timestamp: now.Add(-72 * time.Hour),
					},
					Claimed: &correlation.BeadEvent{
						Timestamp: now.Add(-48 * time.Hour),
						Author:    "alice",
					},
					Closed: &correlation.BeadEvent{
						Timestamp: now,
					},
				},
				Commits: []correlation.CorrelatedCommit{
					{
						ShortSHA:   "abc1234",
						Message:    "Initial fix",
						Timestamp:  now.Add(-36 * time.Hour),
						Confidence: 0.95,
					},
					{
						ShortSHA:   "def5678",
						Message:    "Follow-up",
						Timestamp:  now.Add(-24 * time.Hour),
						Confidence: 0.75,
					},
				},
			},
			wantEntries:       5, // created, claimed, 2 commits, closed
			wantEventTypes:    []string{"created", "claimed", "closed"},
			wantCommitEntries: 2,
		},
		{
			name: "only created event",
			history: correlation.BeadHistory{
				Title:  "New Bead",
				Status: "open",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{
						Timestamp: now.Add(-24 * time.Hour),
					},
				},
				Commits: []correlation.CorrelatedCommit{},
			},
			wantEntries:       1,
			wantEventTypes:    []string{"created"},
			wantCommitEntries: 0,
		},
		{
			name: "with reopened event",
			history: correlation.BeadHistory{
				Title:  "Reopened Bead",
				Status: "open",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{
						Timestamp: now.Add(-96 * time.Hour),
					},
					Closed: &correlation.BeadEvent{
						Timestamp: now.Add(-48 * time.Hour),
					},
					Reopened: &correlation.BeadEvent{
						Timestamp: now.Add(-24 * time.Hour),
					},
				},
				Commits: []correlation.CorrelatedCommit{},
			},
			wantEntries:       3, // created, closed, reopened
			wantEventTypes:    []string{"created", "closed", "reopened"},
			wantCommitEntries: 0,
		},
		{
			name: "empty history",
			history: correlation.BeadHistory{
				Title:      "Empty",
				Status:     "open",
				Milestones: correlation.BeadMilestones{},
				Commits:    []correlation.CorrelatedCommit{},
			},
			wantEntries:       0,
			wantEventTypes:    []string{},
			wantCommitEntries: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &correlation.HistoryReport{
				Histories: map[string]correlation.BeadHistory{
					"bv-test": tt.history,
				},
			}
			h := NewHistoryModel(report, theme)

			entries := h.buildTimeline(tt.history)

			// Check total count
			if len(entries) != tt.wantEntries {
				t.Errorf("buildTimeline() returned %d entries, want %d", len(entries), tt.wantEntries)
			}

			// Count event types
			eventCount := 0
			commitCount := 0
			foundEvents := make(map[string]bool)
			for _, e := range entries {
				if sameTimelineEntryType(e.EntryType, timelineEntryEvent) {
					eventCount++
					foundEvents[e.EventType] = true
				} else if sameTimelineEntryType(e.EntryType, timelineEntryCommit) {
					commitCount++
				}
			}

			if commitCount != tt.wantCommitEntries {
				t.Errorf("buildTimeline() returned %d commit entries, want %d", commitCount, tt.wantCommitEntries)
			}

			// Check expected event types
			for _, et := range tt.wantEventTypes {
				if !foundEvents[et] {
					t.Errorf("buildTimeline() missing expected event type: %s", et)
				}
			}

			// Verify chronological order
			for i := 1; i < len(entries); i++ {
				if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
					t.Errorf("buildTimeline() entries not in chronological order at index %d", i)
				}
			}
		})
	}
}

// Tests for session integration (bv-pr1l)

func TestHistoryModel_SessionCache(t *testing.T) {
	theme := testTheme()
	report := createTestHistoryReport()
	h := NewHistoryModel(report, theme)

	// Initially no sessions cached
	if h.HasSessionsForBead("bv-1") {
		t.Error("HasSessionsForBead should return false when no sessions cached")
	}

	if sessions := h.GetSessionsForBead("bv-1"); sessions != nil {
		t.Error("GetSessionsForBead should return nil when no sessions cached")
	}

	// Set sessions for a bead
	testSessions := []cass.ScoredResult{
		{
			SearchResult: cass.SearchResult{
				SourcePath: "/path/to/session1.md",
				Agent:      "claude",
				Title:      "Working on auth",
				Timestamp:  time.Now(),
			},
			FinalScore: 85.0,
		},
		{
			SearchResult: cass.SearchResult{
				SourcePath: "/path/to/session2.md",
				Agent:      "cursor",
				Title:      "Debugging tokens",
				Timestamp:  time.Now().Add(-time.Hour),
			},
			FinalScore: 65.0,
		},
	}

	h.SetSessionsForBead("bv-1", testSessions)

	// Should now be cached
	if !h.HasSessionsForBead("bv-1") {
		t.Error("HasSessionsForBead should return true after setting sessions")
	}

	retrieved := h.GetSessionsForBead("bv-1")
	if len(retrieved) != 2 {
		t.Errorf("GetSessionsForBead returned %d sessions, want 2", len(retrieved))
	}

	// Other beads should still have no sessions
	if h.HasSessionsForBead("bv-2") {
		t.Error("HasSessionsForBead should return false for different bead")
	}

	// Clear cache
	h.ClearSessionCache()
	if h.HasSessionsForBead("bv-1") {
		t.Error("HasSessionsForBead should return false after clearing cache")
	}
}

func TestBuildTimelineWithSessions(t *testing.T) {
	theme := testTheme()
	now := time.Now()

	history := correlation.BeadHistory{
		BeadID: "bv-test",
		Title:  "Test Bead",
		Status: "in_progress",
		Milestones: correlation.BeadMilestones{
			Created: &correlation.BeadEvent{
				Timestamp: now.Add(-48 * time.Hour),
			},
			Claimed: &correlation.BeadEvent{
				Timestamp: now.Add(-36 * time.Hour),
				Author:    "alice",
			},
		},
		Commits: []correlation.CorrelatedCommit{
			{
				ShortSHA:   "abc1234",
				Message:    "Initial fix",
				Timestamp:  now.Add(-24 * time.Hour),
				Confidence: 0.95,
			},
		},
	}

	report := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"bv-test": history,
		},
	}
	h := NewHistoryModel(report, theme)

	// Add sessions to cache
	sessions := []cass.ScoredResult{
		{
			SearchResult: cass.SearchResult{
				SourcePath: "/path/to/session.md",
				Agent:      "claude",
				Title:      "Working on fix",
				Timestamp:  now.Add(-12 * time.Hour),
			},
			FinalScore: 90.0,
		},
	}
	h.SetSessionsForBead("bv-test", sessions)

	entries := h.buildTimeline(history)

	// Should have: created, claimed, 1 commit, 1 session = 4 entries
	if len(entries) != 4 {
		t.Errorf("buildTimeline() with sessions returned %d entries, want 4", len(entries))
	}

	// Count entry types
	var eventCount, commitCount, sessionCount int
	for _, e := range entries {
		switch e.EntryType {
		case timelineEntryEvent:
			eventCount++
		case timelineEntryCommit:
			commitCount++
		case timelineEntrySession:
			sessionCount++
		}
	}

	if eventCount != 2 {
		t.Errorf("Expected 2 events, got %d", eventCount)
	}
	if commitCount != 1 {
		t.Errorf("Expected 1 commit, got %d", commitCount)
	}
	if sessionCount != 1 {
		t.Errorf("Expected 1 session, got %d", sessionCount)
	}

	// Verify chronological order
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
			t.Errorf("Entries not in chronological order at index %d", i)
		}
	}
}

func TestBuildTimelineSessionOrderingOnTimeTie(t *testing.T) {
	theme := testTheme()
	now := time.Now()

	// Create a history where commit and session have the same timestamp
	sameTime := now.Add(-24 * time.Hour)

	history := correlation.BeadHistory{
		BeadID: "bv-tie",
		Title:  "Time Tie Test",
		Status: "open",
		Milestones: correlation.BeadMilestones{
			Created: &correlation.BeadEvent{
				Timestamp: now.Add(-48 * time.Hour),
			},
		},
		Commits: []correlation.CorrelatedCommit{
			{
				ShortSHA:   "abc1234",
				Message:    "Fix applied",
				Timestamp:  sameTime,
				Confidence: 0.90,
			},
		},
	}

	report := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"bv-tie": history,
		},
	}
	h := NewHistoryModel(report, theme)

	// Add session with same timestamp
	sessions := []cass.ScoredResult{
		{
			SearchResult: cass.SearchResult{
				SourcePath: "/path/to/session.md",
				Agent:      "cursor",
				Title:      "Same time session",
				Timestamp:  sameTime,
			},
			FinalScore: 75.0,
		},
	}
	h.SetSessionsForBead("bv-tie", sessions)

	entries := h.buildTimeline(history)

	// Should have: created, commit, session = 3 entries
	if len(entries) != 3 {
		t.Errorf("buildTimeline() returned %d entries, want 3", len(entries))
	}

	// Find the commit and session with same timestamp
	var commitIdx, sessionIdx int
	for i, e := range entries {
		if sameTimelineEntryType(e.EntryType, timelineEntryCommit) && e.Timestamp.Equal(sameTime) {
			commitIdx = i
		}
		if sameTimelineEntryType(e.EntryType, timelineEntrySession) && e.Timestamp.Equal(sameTime) {
			sessionIdx = i
		}
	}

	// Commit should come before session on timestamp tie
	if commitIdx >= sessionIdx {
		t.Errorf("Commit (idx %d) should come before session (idx %d) on timestamp tie", commitIdx, sessionIdx)
	}
}

func TestBuildTimelineWithoutSessions(t *testing.T) {
	theme := testTheme()
	now := time.Now()

	history := correlation.BeadHistory{
		BeadID: "bv-nosess",
		Title:  "No Sessions",
		Status: "open",
		Milestones: correlation.BeadMilestones{
			Created: &correlation.BeadEvent{
				Timestamp: now.Add(-24 * time.Hour),
			},
		},
		Commits: []correlation.CorrelatedCommit{
			{
				ShortSHA:   "abc1234",
				Message:    "Some work",
				Timestamp:  now,
				Confidence: 0.80,
			},
		},
	}

	report := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"bv-nosess": history,
		},
	}
	h := NewHistoryModel(report, theme)

	// Don't set any sessions - graceful degradation

	entries := h.buildTimeline(history)

	// Should still work: created, commit = 2 entries
	if len(entries) != 2 {
		t.Errorf("buildTimeline() without sessions returned %d entries, want 2", len(entries))
	}

	// No session entries
	for _, e := range entries {
		if sameTimelineEntryType(e.EntryType, timelineEntrySession) {
			t.Error("Should not have session entries when none cached")
		}
	}
}

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "Claude"},
		{"cursor", "Cursor"},
		{"windsurf", "Windsurf"},
		{"", ""},
		{"A", "A"},
		{"abc123", "Abc123"},
	}

	for _, tt := range tests {
		got := capitalizeFirst(tt.input)
		if got != tt.want {
			t.Errorf("capitalizeFirst(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "minutes",
			duration: 30 * time.Minute,
			want:     "30m",
		},
		{
			name:     "hours",
			duration: 5 * time.Hour,
			want:     "5h",
		},
		{
			name:     "one day",
			duration: 24 * time.Hour,
			want:     "1d",
		},
		{
			name:     "multiple days",
			duration: 7 * 24 * time.Hour,
			want:     "7d",
		},
		{
			name:     "less than hour",
			duration: 45 * time.Minute,
			want:     "45m",
		},
		{
			name:     "exactly one day",
			duration: 25 * time.Hour, // 1 day + 1 hour rounds to 1d
			want:     "1d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestRenderCompactTimeline(t *testing.T) {
	theme := testTheme()
	now := time.Now()

	tests := []struct {
		name           string
		history        correlation.BeadHistory
		maxWidth       int
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "full lifecycle",
			history: correlation.BeadHistory{
				Title:  "Test",
				Status: "closed",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{Timestamp: now.Add(-72 * time.Hour)},
					Claimed: &correlation.BeadEvent{Timestamp: now.Add(-48 * time.Hour)},
					Closed:  &correlation.BeadEvent{Timestamp: now},
				},
				Commits: []correlation.CorrelatedCommit{
					{ShortSHA: "abc", Timestamp: now.Add(-24 * time.Hour)},
				},
				CycleTime: &correlation.CycleTime{
					CreateToClose: durationPtr(72 * time.Hour),
				},
			},
			maxWidth:     100,
			wantContains: []string{"○", "●", "✓", "├", "3d cycle", "1 commit"},
		},
		{
			name: "many commits truncated",
			history: correlation.BeadHistory{
				Title:  "Test",
				Status: "open",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
				},
				Commits: []correlation.CorrelatedCommit{
					{ShortSHA: "a1", Timestamp: now.Add(-20 * time.Hour)},
					{ShortSHA: "a2", Timestamp: now.Add(-16 * time.Hour)},
					{ShortSHA: "a3", Timestamp: now.Add(-12 * time.Hour)},
					{ShortSHA: "a4", Timestamp: now.Add(-8 * time.Hour)},
					{ShortSHA: "a5", Timestamp: now.Add(-4 * time.Hour)},
					{ShortSHA: "a6", Timestamp: now.Add(-2 * time.Hour)},
					{ShortSHA: "a7", Timestamp: now.Add(-1 * time.Hour)},
				},
			},
			maxWidth:     100,
			wantContains: []string{"○", "├", "…", "7 commits"},
		},
		{
			name: "empty history",
			history: correlation.BeadHistory{
				Title:      "Empty",
				Status:     "open",
				Milestones: correlation.BeadMilestones{},
				Commits:    []correlation.CorrelatedCommit{},
			},
			maxWidth:     100,
			wantContains: []string{"no timeline data"},
		},
		{
			name: "single commit",
			history: correlation.BeadHistory{
				Title:  "Single",
				Status: "open",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
				},
				Commits: []correlation.CorrelatedCommit{
					{ShortSHA: "xyz", Timestamp: now.Add(-12 * time.Hour)},
				},
			},
			maxWidth:     100,
			wantContains: []string{"1 commit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &correlation.HistoryReport{
				Histories: map[string]correlation.BeadHistory{
					"bv-test": tt.history,
				},
			}
			h := NewHistoryModel(report, theme)

			result := h.renderCompactTimeline(tt.history, tt.maxWidth)

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("renderCompactTimeline() = %q, want to contain %q", result, want)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(result, notWant) {
					t.Errorf("renderCompactTimeline() = %q, should NOT contain %q", result, notWant)
				}
			}
		})
	}
}

// Helper to create duration pointer
func durationPtr(d time.Duration) *time.Duration {
	return &d
}

// ═══════════════════════════════════════════════════════════════════════════════
// View Mode Toggle Animation Tests (bv-kvlx)
// ═══════════════════════════════════════════════════════════════════════════════

func TestHistoryModel_ToggleViewMode_SetsTimestamp(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)

	// Initially modeChangedAt should be zero
	if !h.modeChangedAt.IsZero() {
		t.Error("Expected modeChangedAt to be zero initially")
	}

	// Toggle view mode
	before := time.Now()
	h.ToggleViewMode()
	after := time.Now()

	// modeChangedAt should be set
	if h.modeChangedAt.IsZero() {
		t.Error("Expected modeChangedAt to be set after toggle")
	}

	// Should be between before and after
	if h.modeChangedAt.Before(before) || h.modeChangedAt.After(after) {
		t.Errorf("modeChangedAt %v should be between %v and %v", h.modeChangedAt, before, after)
	}
}

func TestHistoryModel_ModeIndicator_UsesIcons(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.width = 120
	h.height = 40

	// Test bead mode indicator
	h.viewMode = historyModeBead
	h.modeChangedAt = time.Time{} // Clear to avoid flash
	header := h.renderHeader()

	if !strings.Contains(header, "◈") {
		t.Error("Expected ◈ icon for bead mode in header")
	}
	if !strings.Contains(header, "Beads") {
		t.Error("Expected 'Beads' label in header")
	}

	// Test git mode indicator
	h.viewMode = historyModeGit
	h.modeChangedAt = time.Time{} // Clear to avoid flash
	header = h.renderHeader()

	if !strings.Contains(header, "◉") {
		t.Error("Expected ◉ icon for git mode in header")
	}
	if !strings.Contains(header, "Git") {
		t.Error("Expected 'Git' label in header")
	}
}

func TestHistoryModel_ModeTransition_FlashEffect(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.width = 120
	h.height = 40

	// Set recent mode change (within 150ms)
	h.modeChangedAt = time.Now()
	header1 := h.renderHeader()

	// Set old mode change (outside 150ms window)
	h.modeChangedAt = time.Now().Add(-200 * time.Millisecond)
	header2 := h.renderHeader()

	// The headers should be different (flash vs no flash)
	// Both contain the same text, but styling differs
	// We verify by checking that both contain mode indicator
	if !strings.Contains(header1, "◈") && !strings.Contains(header1, "◉") {
		t.Error("Expected mode icon in header during transition")
	}
	if !strings.Contains(header2, "◈") && !strings.Contains(header2, "◉") {
		t.Error("Expected mode icon in header after transition")
	}
}

func TestHistoryModel_ViewModeToggle_PreservesIcon(t *testing.T) {
	report := createTestHistoryReport()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.width = 120
	h.height = 40

	// Start in bead mode
	if h.IsGitMode() {
		t.Error("Expected to start in bead mode")
	}

	// Toggle to git mode
	h.ToggleViewMode()
	if !h.IsGitMode() {
		t.Error("Expected to be in git mode after toggle")
	}

	// Clear transition flash
	h.modeChangedAt = time.Time{}
	header := h.renderHeader()
	if !strings.Contains(header, "◉") {
		t.Error("Expected ◉ icon for git mode")
	}

	// Toggle back to bead mode
	h.ToggleViewMode()
	if h.IsGitMode() {
		t.Error("Expected to be in bead mode after second toggle")
	}

	// Clear transition flash
	h.modeChangedAt = time.Time{}
	header = h.renderHeader()
	if !strings.Contains(header, "◈") {
		t.Error("Expected ◈ icon for bead mode")
	}
}

// =============================================================================
// FILE TREE TESTS (bv-qr24)
// =============================================================================

// createTestHistoryReportWithFiles creates a test report with file changes
func createTestHistoryReportWithFiles() *correlation.HistoryReport {
	now := time.Now()

	return &correlation.HistoryReport{
		GeneratedAt: now,
		Stats: correlation.HistoryStats{
			TotalBeads:       3,
			BeadsWithCommits: 3,
			TotalCommits:     5,
			UniqueAuthors:    2,
		},
		Histories: map[string]correlation.BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Fix authentication bug",
				Status: "closed",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "abc123",
						ShortSHA:   "abc123",
						Message:    "fix: auth bug",
						Author:     "Dev One",
						Timestamp:  now,
						Method:     correlation.MethodCoCommitted,
						Confidence: 0.95,
						Files: []correlation.FileChange{
							{Path: "pkg/auth/token.go", Action: "M", Insertions: 10, Deletions: 5},
							{Path: "pkg/auth/session.go", Action: "M", Insertions: 5, Deletions: 2},
						},
					},
					{
						SHA:        "def456",
						ShortSHA:   "def456",
						Message:    "test: add auth tests",
						Author:     "Dev One",
						Timestamp:  now.Add(-time.Hour),
						Method:     correlation.MethodExplicitID,
						Confidence: 0.90,
						Files: []correlation.FileChange{
							{Path: "pkg/auth/token_test.go", Action: "A", Insertions: 50},
							{Path: "pkg/auth/session_test.go", Action: "A", Insertions: 30},
						},
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "Add logging",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "ghi789",
						ShortSHA:   "ghi789",
						Message:    "feat: add logging",
						Author:     "Dev Two",
						Timestamp:  now.Add(-2 * time.Hour),
						Method:     correlation.MethodTemporalAuthor,
						Confidence: 0.60,
						Files: []correlation.FileChange{
							{Path: "pkg/logging/logger.go", Action: "A", Insertions: 100},
							{Path: "pkg/auth/token.go", Action: "M", Insertions: 3, Deletions: 1},
						},
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "Refactor database",
				Status: "in_progress",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "jkl012",
						ShortSHA:   "jkl012",
						Message:    "refactor: db layer",
						Author:     "Dev Two",
						Timestamp:  now.Add(-3 * time.Hour),
						Method:     correlation.MethodCoCommitted,
						Confidence: 0.92,
						Files: []correlation.FileChange{
							{Path: "pkg/db/connection.go", Action: "M", Insertions: 20, Deletions: 30},
							{Path: "pkg/db/query.go", Action: "M", Insertions: 15, Deletions: 10},
							{Path: "README.md", Action: "M", Insertions: 5, Deletions: 2},
						},
					},
				},
			},
		},
		CommitIndex: correlation.CommitIndex{
			"abc123": {"bv-1"},
			"def456": {"bv-1"},
			"ghi789": {"bv-2"},
			"jkl012": {"bv-3"},
		},
	}
}

func TestFileTree_Toggle(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	// Initially not visible
	if h.IsFileTreeVisible() {
		t.Error("File tree should be hidden initially")
	}

	// Toggle to show
	h.ToggleFileTree()
	if !h.IsFileTreeVisible() {
		t.Error("File tree should be visible after toggle")
	}

	// Tree should be built
	if h.fileTree == nil {
		t.Error("File tree should be built after showing")
	}

	// Toggle to hide
	h.ToggleFileTree()
	if h.IsFileTreeVisible() {
		t.Error("File tree should be hidden after second toggle")
	}
}

func TestFileTree_BuildsCorrectStructure(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Should have root level directories/files
	if len(h.fileTree) == 0 {
		t.Fatal("File tree should have root nodes")
	}

	// Check for expected structure: pkg/ and README.md
	var foundPkg, foundReadme bool
	for _, node := range h.fileTree {
		if fileTreeNodeIsDirNamed(node, "pkg") {
			foundPkg = true
		}
		if fileTreeNodeIsFileNamed(node, "README.md") {
			foundReadme = true
		}
	}

	if !foundPkg {
		t.Error("Expected pkg/ directory in file tree")
	}
	if !foundReadme {
		t.Error("Expected README.md in file tree")
	}

	// Directories should be sorted before files
	if len(h.fileTree) >= 2 && !h.fileTree[0].IsDir && h.fileTree[len(h.fileTree)-1].IsDir {
		t.Error("Directories should come before files in tree")
	}
}

func TestFileTree_DirectoryChangeCounts(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	findNode := func(path string) *FileTreeNode {
		var walk func(nodes []*FileTreeNode) *FileTreeNode
		walk = func(nodes []*FileTreeNode) *FileTreeNode {
			for _, node := range nodes {
				if strings.Compare(node.Path, path) == 0 {
					return node
				}
				if found := walk(node.Children); found != nil {
					return found
				}
			}
			return nil
		}
		return walk(h.fileTree)
	}

	tests := []struct {
		path string
		want int
	}{
		{"pkg", 4},
		{"pkg/auth", 3},
		{"pkg/auth/token.go", 2},
		{"pkg/db", 1},
		{"README.md", 1},
	}

	for _, tc := range tests {
		node := findNode(tc.path)
		if node == nil {
			t.Fatalf("expected file tree node %q", tc.path)
		}
		if node.ChangeCount < tc.want || node.ChangeCount > tc.want {
			t.Errorf("node %q ChangeCount = %d, want %d", tc.path, node.ChangeCount, tc.want)
		}
	}
}

func TestFileTreePathPrefixes(t *testing.T) {
	tests := []struct {
		name string
		path string
		want []string
	}{
		{name: "file in nested directory", path: "pkg/auth/token.go", want: []string{"pkg", "pkg/auth", "pkg/auth/token.go"}},
		{name: "root file", path: "README.md", want: []string{"README.md"}},
		{name: "leading and repeated separators", path: "/pkg//auth/token.go", want: []string{"pkg", "pkg/auth", "pkg/auth/token.go"}},
		{name: "empty", path: "", want: nil},
		{name: "only separators", path: "///", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fileTreePathPrefixes(tc.path)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("fileTreePathPrefixes(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestFileTree_Navigation(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Initial position should be 0
	if h.selectedFileIdx != 0 {
		t.Errorf("Initial selectedFileIdx = %d, want 0", h.selectedFileIdx)
	}

	// Move down
	h.MoveDownFileTree()
	if h.selectedFileIdx != 1 {
		t.Errorf("selectedFileIdx after MoveDown = %d, want 1", h.selectedFileIdx)
	}

	// Move up
	h.MoveUpFileTree()
	if h.selectedFileIdx != 0 {
		t.Errorf("selectedFileIdx after MoveUp = %d, want 0", h.selectedFileIdx)
	}

	// Should not go below 0
	h.MoveUpFileTree()
	if h.selectedFileIdx != 0 {
		t.Error("selectedFileIdx should stay at 0 when moving up at start")
	}

	// Move to end
	for i := 0; i < 100; i++ {
		h.MoveDownFileTree()
	}
	lastIdx := h.selectedFileIdx

	// Should not exceed list length - 1
	if lastIdx >= len(h.flatFileList) {
		t.Errorf("selectedFileIdx %d should be less than flatFileList length %d", lastIdx, len(h.flatFileList))
	}

	// Should not go past end
	h.MoveDownFileTree()
	if h.selectedFileIdx != lastIdx {
		t.Error("selectedFileIdx should not exceed list bounds")
	}
}

func TestFileTree_ExpandCollapse(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Find pkg directory (should be first since dirs come before files)
	var pkgIdx int
	for i, node := range h.flatFileList {
		if fileTreeNodeIsDirNamed(node, "pkg") {
			pkgIdx = i
			break
		}
	}

	// Navigate to pkg
	h.selectedFileIdx = pkgIdx
	node := h.SelectedFileNode()
	if node == nil || !node.IsDir {
		t.Fatal("Should have selected pkg directory")
	}

	initialCount := len(h.flatFileList)

	// Expand
	if node.Expanded {
		t.Error("Node should not be expanded initially")
	}
	h.ToggleExpandFile()
	node = h.flatFileList[pkgIdx]
	if !node.Expanded {
		t.Error("Node should be expanded after toggle")
	}

	// List should now include children
	expandedCount := len(h.flatFileList)
	if expandedCount <= initialCount {
		t.Errorf("flatFileList should grow after expand: was %d, now %d", initialCount, expandedCount)
	}

	// Collapse
	h.ToggleExpandFile()
	if h.flatFileList[pkgIdx].Expanded {
		t.Error("Node should be collapsed after second toggle")
	}

	// List should return to original size
	collapsedCount := len(h.flatFileList)
	if collapsedCount != initialCount {
		t.Errorf("flatFileList should return to original size: was %d, now %d", initialCount, collapsedCount)
	}
}

func TestFileTree_CollapseNode(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Find and expand pkg directory
	var pkgIdx int
	for i, node := range h.flatFileList {
		if fileTreeNodeIsDirNamed(node, "pkg") {
			pkgIdx = i
			break
		}
	}
	h.selectedFileIdx = pkgIdx
	h.ToggleExpandFile()

	// Navigate into expanded directory
	h.MoveDownFileTree()

	// Collapse parent from child
	h.selectedFileIdx = pkgIdx
	h.CollapseFileNode()
	if h.flatFileList[pkgIdx].Expanded {
		t.Error("CollapseFileNode should collapse the directory")
	}
}

func TestFileTree_SelectFile(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Expand to reach a file
	for i, node := range h.flatFileList {
		if fileTreeNodeIsDirNamed(node, "pkg") {
			h.selectedFileIdx = i
			h.ToggleExpandFile()
			break
		}
	}

	// Find auth directory and expand it
	for i, node := range h.flatFileList {
		if fileTreeNodeIsDirNamed(node, "auth") {
			h.selectedFileIdx = i
			h.ToggleExpandFile()
			break
		}
	}

	// Find a .go file
	var fileIdx int
	var filePath string
	for i, node := range h.flatFileList {
		if !node.IsDir && strings.HasSuffix(node.Name, ".go") {
			fileIdx = i
			filePath = node.Path
			break
		}
	}

	if filePath == "" {
		t.Fatal("Should have found a .go file")
	}

	h.selectedFileIdx = fileIdx

	// No filter initially
	if h.GetFileFilter() != "" {
		t.Error("Filter should be empty initially")
	}

	// Select file to set filter
	h.SelectFile()
	if stringsDiffer(h.GetFileFilter(), filePath) {
		t.Errorf("Filter should be %q, got %q", filePath, h.GetFileFilter())
	}

	// Select same file again to clear filter
	h.SelectFile()
	if h.GetFileFilter() != "" {
		t.Error("Filter should be cleared after selecting same file again")
	}

	// Set filter again and clear with ClearFileFilter
	h.SelectFile()
	h.ClearFileFilter()
	if h.GetFileFilter() != "" {
		t.Error("ClearFileFilter should clear the filter")
	}
}

func TestFileTree_Focus(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Initially no focus
	if h.FileTreeHasFocus() {
		t.Error("File tree should not have focus initially")
	}

	// Set focus
	h.SetFileTreeFocus(true)
	if !h.FileTreeHasFocus() {
		t.Error("File tree should have focus after SetFileTreeFocus(true)")
	}

	// Clear focus
	h.SetFileTreeFocus(false)
	if h.FileTreeHasFocus() {
		t.Error("File tree should not have focus after SetFileTreeFocus(false)")
	}
}

func TestFileTree_SelectedFileName(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Should have a selected name
	name := h.SelectedFileName()
	if name == "" {
		t.Error("SelectedFileName should return a name when tree has nodes")
	}

	// Navigate down and check name changes
	h.MoveDownFileTree()
	name2 := h.SelectedFileName()
	if name2 == "" {
		t.Error("SelectedFileName should return name after navigation")
	}
}

func TestFileTree_SelectedFileNode(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	node := h.SelectedFileNode()
	if node == nil {
		t.Fatal("SelectedFileNode should return a node when tree has nodes")
	}

	if node.Name == "" {
		t.Error("Selected node should have a name")
	}

	if node.Path == "" {
		t.Error("Selected node should have a path")
	}
}

func TestFileTree_EmptyReport(t *testing.T) {
	theme := testTheme()

	// Test with nil report
	h := NewHistoryModel(nil, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	if h.fileTree != nil {
		t.Error("File tree should be nil for nil report")
	}

	// SelectedFileName and SelectedFileNode should handle gracefully
	if h.SelectedFileName() != "" {
		t.Error("SelectedFileName should return empty for nil tree")
	}
	if h.SelectedFileNode() != nil {
		t.Error("SelectedFileNode should return nil for nil tree")
	}
}

func TestFileTree_StatePreservedOnToggle(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Expand a directory
	for i, node := range h.flatFileList {
		if node.IsDir {
			h.selectedFileIdx = i
			h.ToggleExpandFile()
			break
		}
	}

	// Move to a specific position
	h.MoveDownFileTree()
	h.MoveDownFileTree()
	savedIdx := h.selectedFileIdx

	// Hide and show file tree
	h.ToggleFileTree()
	h.ToggleFileTree()

	// Position should be preserved
	if h.selectedFileIdx != savedIdx {
		t.Errorf("selectedFileIdx should be preserved: was %d, now %d", savedIdx, h.selectedFileIdx)
	}
}

func TestFileTree_RenderPanel(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// renderFileTreePanel should produce non-empty output
	panel := h.renderFileTreePanel(40, 20)
	if panel == "" {
		t.Error("renderFileTreePanel should produce output")
	}

	// Should contain "FILES" header
	if !strings.Contains(panel, "FILES") {
		t.Error("File tree panel should contain 'FILES' header")
	}
}

func TestFileTree_CursorResetOnToggle(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Move cursor to non-zero position
	h.MoveDownFileTree()
	h.MoveDownFileTree()
	h.MoveDownFileTree()
	savedIdx := h.selectedFileIdx

	// Hide file tree
	h.ToggleFileTree()

	// Show file tree again
	h.ToggleFileTree()

	// Cursor should be preserved (state is maintained)
	if h.selectedFileIdx != savedIdx {
		t.Errorf("Cursor should be preserved after toggle: was %d, now %d", savedIdx, h.selectedFileIdx)
	}
}

func TestFileTree_MultipleFilesPerCommit(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// createTestHistoryReportWithFiles has commits with multiple files
	// bv-1 has commit abc123 with 2 files: pkg/auth/token.go, pkg/auth/session.go
	// Verify both files appear in the tree

	// Expand all directories to find files
	// NOTE: Must use a while-loop because expanding a directory rebuilds flatFileList,
	// and for-range captures the original slice which doesn't include newly added nodes
	expandedAny := true
	for expandedAny {
		expandedAny = false
		for i := 0; i < len(h.flatFileList); i++ {
			node := h.flatFileList[i]
			if node.IsDir && !node.Expanded {
				h.selectedFileIdx = i
				h.ToggleExpandFile()
				expandedAny = true
				break // restart loop since flatFileList changed
			}
		}
	}

	// Look for token.go and session.go
	var foundToken, foundSession bool
	for _, node := range h.flatFileList {
		if fileTreeNodeIsNamed(node, "token.go") {
			foundToken = true
		}
		if fileTreeNodeIsNamed(node, "session.go") {
			foundSession = true
		}
	}

	if !foundToken {
		t.Error("Expected token.go in file tree (from commit with multiple files)")
	}
	if !foundSession {
		t.Error("Expected session.go in file tree (from commit with multiple files)")
	}
}

func TestFileTree_CommitsGroupedByPath(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// pkg/auth/token.go is modified in 2 commits (abc123 in bv-1 and ghi789 in bv-2)
	// When we select token.go, it should filter to show beads that touched it

	// Expand tree to find token.go
	// Use while-loop since flatFileList changes when directories expand
	expandedAny := true
	for expandedAny {
		expandedAny = false
		for i := 0; i < len(h.flatFileList); i++ {
			node := h.flatFileList[i]
			if node.IsDir && !node.Expanded {
				h.selectedFileIdx = i
				h.ToggleExpandFile()
				expandedAny = true
				break
			}
		}
	}

	// Find and select token.go
	for i, node := range h.flatFileList {
		if fileTreeNodeIsFileNamed(node, "token.go") {
			h.selectedFileIdx = i
			h.SelectFile()
			break
		}
	}

	// Verify filter is set
	filter := h.GetFileFilter()
	if filter == "" {
		t.Error("File filter should be set after selecting token.go")
	}

	if !strings.HasSuffix(filter, "token.go") {
		t.Errorf("File filter should end with 'token.go', got %s", filter)
	}
}

func TestFileTree_DirectoryContainsChildFiles(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Find pkg directory
	var pkgIdx int
	for i, node := range h.flatFileList {
		if fileTreeNodeIsDirNamed(node, "pkg") {
			pkgIdx = i
			break
		}
	}

	// Expand pkg
	h.selectedFileIdx = pkgIdx
	h.ToggleExpandFile()

	// Should now have child directories (auth, db, logging)
	pkgNode := h.flatFileList[pkgIdx]
	if len(pkgNode.Children) == 0 {
		t.Error("pkg directory should have child directories")
	}

	// Verify subdirectories exist
	subDirs := make(map[string]bool)
	for _, child := range pkgNode.Children {
		if child.IsDir {
			subDirs[child.Name] = true
		}
	}

	expected := []string{"auth", "db", "logging"}
	for _, dir := range expected {
		if !subDirs[dir] {
			t.Errorf("Expected %s/ subdirectory under pkg/", dir)
		}
	}
}

func TestFileTree_JumpToFile(t *testing.T) {
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)

	h.ToggleFileTree()

	// Expand all directories
	for i := 0; i < len(h.flatFileList); i++ {
		node := h.flatFileList[i]
		if node.IsDir && !node.Expanded {
			h.selectedFileIdx = i
			h.ToggleExpandFile()
			i = 0 // Reset to catch newly added directories
		}
	}

	// Count .go files
	goFileCount := 0
	for _, node := range h.flatFileList {
		if !node.IsDir && strings.HasSuffix(node.Name, ".go") {
			goFileCount++
		}
	}

	if goFileCount < 4 {
		t.Errorf("Expected at least 4 .go files in test data, got %d", goFileCount)
	}
}

func TestHistoryModel_CycleSearchModeAndAuthorToggle(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Fix authentication bug", Status: model.StatusClosed, Priority: 1},
		{ID: "bv-2", Title: "Add logging", Status: model.StatusOpen, Priority: 2},
		{ID: "bv-3", Title: "Refactor database", Status: model.StatusInProgress, Priority: 2},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 35})

	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	// Switch to History view and install a deterministic test report.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.focused != focusHistory {
		t.Fatalf("expected focusHistory, got %v", m.focused)
	}
	makeHistoryReportCurrent(m, createTestHistoryReport())

	// 1. Start search ('/'), enter "Dev Two", and press Tab to cycle search modes:
	// All -> Commit -> Author -> Bead -> SHA -> All while preserving the query and re-filtering.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "Dev Two" {
		send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.historyView.SearchQuery(); got != "Dev Two" {
		t.Fatalf("expected SearchQuery 'Dev Two', got %q", got)
	}
	if m.historyView.searchMode != searchModeAll {
		t.Fatalf("expected initial searchModeAll, got %v", m.historyView.searchMode)
	}

	// Tab -> Commit mode ("Dev Two" appears in no commit message -> 0 filtered beads)
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyView.searchMode != searchModeCommit || m.historyView.SearchQuery() != "Dev Two" {
		t.Fatalf("after 1st Tab: mode=%v query=%q", m.historyView.searchMode, m.historyView.SearchQuery())
	}
	if len(m.historyView.histories) != 0 {
		t.Fatalf("expected 0 beads matching 'Dev Two' in Commit mode, got %d", len(m.historyView.histories))
	}

	// Tab -> Author mode ("Dev Two" matches bv-2 and bv-3 -> 2 filtered beads)
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyView.searchMode != searchModeAuthor || m.historyView.SearchQuery() != "Dev Two" {
		t.Fatalf("after 2nd Tab: mode=%v query=%q", m.historyView.searchMode, m.historyView.SearchQuery())
	}
	if len(m.historyView.histories) != 2 {
		t.Fatalf("expected 2 beads matching 'Dev Two' in Author mode, got %d", len(m.historyView.histories))
	}

	// Tab -> Bead -> SHA -> All
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyView.searchMode != searchModeBead {
		t.Fatalf("after 3rd Tab: want searchModeBead, got %v", m.historyView.searchMode)
	}
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyView.searchMode != searchModeSHA {
		t.Fatalf("after 4th Tab: want searchModeSHA, got %v", m.historyView.searchMode)
	}
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyView.searchMode != searchModeAll {
		t.Fatalf("after 5th Tab: want wrap to searchModeAll, got %v", m.historyView.searchMode)
	}

	// Clear search via CancelSearch.
	m.historyView.CancelSearch()

	// 2. Press 'A' in Bead mode to toggle @author filter for the currently selected commit.
	selectedCommit := m.historyView.SelectedCommit()
	if selectedCommit == nil {
		t.Fatal("expected selected commit in Bead mode")
	}
	expectedAuthor := selectedCommit.Author
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if got := m.historyView.GetAuthorFilter(); got != expectedAuthor {
		t.Fatalf("expected 'A' to filter by %q, got %q (status=%q)", expectedAuthor, got, m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, expectedAuthor) {
		t.Fatalf("expected statusMsg to mention author filter %q, got %q", expectedAuthor, m.statusMsg)
	}
	if len(m.historyView.histories) == 0 || len(m.historyView.histories) >= 3 {
		t.Fatalf("expected Bead mode to filter down to matching author beads, got %d beads", len(m.historyView.histories))
	}

	// Verify Git mode ('v') also respects the active @author filter!
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !m.historyView.IsGitMode() {
		t.Fatal("expected Git mode after 'v'")
	}
	filteredGitCommits := m.historyView.GetFilteredCommitList()
	if len(filteredGitCommits) == 0 {
		t.Fatal("expected non-empty filtered commit list in Git mode")
	}
	for _, entry := range filteredGitCommits {
		if !strings.Contains(strings.ToLower(entry.Author), strings.ToLower(expectedAuthor)) {
			t.Fatalf("Git mode commit list leaked non-matching author %q (want %q)", entry.Author, expectedAuthor)
		}
	}

	// Pressing 'A' again on a "Dev One" commit clears the @author filter.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if got := m.historyView.GetAuthorFilter(); got != "" {
		t.Fatalf("expected second 'A' to clear author filter, got %q", got)
	}
	if !strings.Contains(strings.ToLower(m.statusMsg), "cleared") {
		t.Fatalf("expected cleared statusMsg, got %q", m.statusMsg)
	}
}

func TestHistoryModel_FileTreeViewLayoutFilterChipGitModeAndCass(t *testing.T) {
	now := time.Now()
	report := &correlation.HistoryReport{
		GeneratedAt: now,
		Stats: correlation.HistoryStats{
			TotalBeads:       3,
			BeadsWithCommits: 3,
			TotalCommits:     3,
			UniqueAuthors:    2,
		},
		Histories: map[string]correlation.BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Auth token validation",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "1111111aaaaaaa",
						ShortSHA:   "1111111",
						Message:    "feat(auth): validate JWT token",
						Author:     "Alice",
						Timestamp:  now,
						Method:     correlation.MethodExplicitID,
						Confidence: 0.95,
						Files: []correlation.FileChange{
							{Path: "pkg/auth/token.go", Action: "M", Insertions: 20, Deletions: 4},
						},
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "DB pool tuning",
				Status: "in_progress",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "2222222bbbbbbb",
						ShortSHA:   "2222222",
						Message:    "perf(db): tune pool connections",
						Author:     "Bob",
						Timestamp:  now.Add(-time.Hour),
						Method:     correlation.MethodTemporalAuthor,
						Confidence: 0.60,
						Files: []correlation.FileChange{
							{Path: "pkg/db/pool.go", Action: "M", Insertions: 12, Deletions: 3},
						},
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "DB migration runner",
				Status: "closed",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "3333333ccccccc",
						ShortSHA:   "3333333",
						Message:    "feat(db): add migration runner",
						Author:     "Alice",
						Timestamp:  now.Add(-2 * time.Hour),
						Method:     correlation.MethodExplicitID,
						Confidence: 0.90,
						Files: []correlation.FileChange{
							{Path: "pkg/db/migrate.go", Action: "A", Insertions: 80, Deletions: 0},
						},
					},
				},
			},
		},
		CommitIndex: correlation.CommitIndex{
			"1111111aaaaaaa": []string{"bv-1"},
			"2222222bbbbbbb": []string{"bv-2"},
			"3333333ccccccc": []string{"bv-3"},
		},
	}

	issues := []model.Issue{
		{ID: "bv-1", Title: "Auth token validation", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "DB pool tuning", Status: model.StatusInProgress, Priority: 2},
		{ID: "bv-3", Title: "DB migration runner", Status: model.StatusClosed, Priority: 2},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	// Attach cassLookup returning 2 sessions for bv-1
	m.historyView.SetCassLookup(func(beadID string) []cass.ScoredResult {
		if beadID == "bv-1" {
			return []cass.ScoredResult{
				{SearchResult: cass.SearchResult{Title: "Session 1"}, FinalScore: 0.9},
				{SearchResult: cass.SearchResult{Title: "Session 2"}, FinalScore: 0.8},
			}
		}
		return nil
	})

	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	makeHistoryReportCurrent(m, report)
	// Re-install our deterministic test cassLookup after enterHistoryView's attachHistoryCassLookup
	m.historyView.SetCassLookup(func(beadID string) []cass.ScoredResult {
		if beadID == "bv-1" {
			return []cass.ScoredResult{
				{SearchResult: cass.SearchResult{Title: "Session 1"}, FinalScore: 0.9},
				{SearchResult: cass.SearchResult{Title: "Session 2"}, FinalScore: 0.8},
			}
		}
		return nil
	})

	// 1. Verify Cass session badge 🤖 renders in View() when cassLookup returns sessions for bv-1
	if !m.historyView.HasSessionsForBead("bv-1") || m.historyView.HasSessionsForBead("bv-2") {
		t.Fatal("HasSessionsForBead did not consult cassLookup properly")
	}
	baseView := m.historyView.View()
	if !strings.Contains(baseView, "🤖") {
		t.Fatalf("expected Cass session badge 🤖 in HistoryModel.View():\n%s", baseView)
	}

	// 2. Toggle file tree ('f') and verify View() wires renderFileTreePanel across widths
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if !m.historyView.showFileTree || !m.historyView.FileTreeRendered() {
		t.Fatalf("expected file tree visible and rendered at width 120")
	}
	wideTreeView := m.historyView.View()
	if !strings.Contains(wideTreeView, "FILES") || !strings.Contains(wideTreeView, "pkg") {
		t.Fatalf("expected FILES tree panel in wide HistoryModel.View():\n%s", wideTreeView)
	}

	// Compact 2-pane width (72 cols): still rendered, never panics
	m.historyView.SetSize(72, 26)
	if !m.historyView.FileTreeRendered() {
		t.Fatal("expected FileTreeRendered() == true at compact width 72")
	}
	compactView := m.historyView.View()
	if !strings.Contains(compactView, "FILES") {
		t.Fatalf("expected FILES panel in compact 72-col layout:\n%s", compactView)
	}

	// Narrow width (<60 cols): suppressed with clear notice and focus released (never trapped invisibly!)
	m.historyView.SetSize(50, 24)
	if m.historyView.FileTreeRendered() || !m.historyView.FileTreeSuppressedForWidth() {
		t.Fatalf("expected file tree suppressed at width 50: rendered=%v suppressed=%v",
			m.historyView.FileTreeRendered(), m.historyView.FileTreeSuppressedForWidth())
	}
	if m.historyView.FileTreeHasFocus() {
		t.Fatal("fileTreeFocus must be cleared when file tree is hidden on narrow terminal")
	}
	narrowView := m.historyView.View()
	if !strings.Contains(narrowView, "File tree hidden") {
		t.Fatalf("expected narrow-width file tree notice in filter line:\n%s", narrowView)
	}

	// Restore wide width, focus file tree, navigate to "pkg/db" directory, and press Enter
	m.historyView.SetSize(120, 32)
	m.historyView.SetFileTreeFocus(true)
	// Expand root/pkg if needed and select pkg/db
	for guard := 0; guard < 10; guard++ {
		node := m.historyView.SelectedFileNode()
		if node == nil {
			break
		}
		if node.Path == "pkg/db" {
			break
		}
		if node.IsDir && !node.Expanded {
			m.historyView.ToggleExpandFile()
		}
		m.historyView.MoveDownFileTree()
	}
	send(tea.KeyMsg{Type: tea.KeyEnter})
	if m.historyView.GetFileFilter() != "pkg/db" {
		t.Fatalf("expected Enter on directory node to set fileFilter='pkg/db', got %q", m.historyView.GetFileFilter())
	}
	chipView := m.historyView.View()
	if !strings.Contains(chipView, "File: pkg/db") {
		t.Fatalf("expected 'File: pkg/db' chip in filter line:\n%s", chipView)
	}

	// Switch to Git Mode ('v')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !m.historyView.IsGitMode() {
		t.Fatal("expected Git Mode after 'v'")
	}
	// Directory filter "pkg/db" in Git Mode should keep only the 2 pkg/db commits (2222222, 3333333)
	if got := len(m.historyView.GetFilteredCommitList()); got != 2 {
		t.Fatalf("expected 2 commits in Git Mode for directory filter pkg/db, got %d", got)
	}

	// Press 'c' in Git Mode to cycle minConfidence to 0.50 -> 0.80 (press twice: 0 -> 0.5 -> 0.75/0.8)
	m.historyView.SetMinConfidence(0.80)
	if got := len(m.historyView.GetFilteredCommitList()); got != 1 || m.historyView.GetFilteredCommitList()[0].ShortSHA != "3333333" {
		t.Fatalf("expected Git Mode confidence >=0.80 + pkg/db to leave only commit 3333333, got %+v", m.historyView.GetFilteredCommitList())
	}

	// 1) In Git Mode when beadIDs has a different stale Bead-Mode selection,
	// focusedIssueForSessions() must resolve the Git-Mode commit's SelectedRelatedBeadID().
	m.historyView.beadIDs = []string{"bv-1"}
	m.historyView.selectedBead = 0
	if got := m.focusedIssueForSessions(); got == nil || got.ID != "bv-3" {
		t.Fatalf("expected Git Mode focusedIssueForSessions() to resolve SelectedRelatedBeadID bv-3 over stale beadIDs[0], got %+v", got)
	}

	// 2) In Git Mode when SelectedBeadID() is empty, focusedIssueForSessions() MUST still resolve SelectedRelatedBeadID() ("bv-3")!
	m.historyView.beadIDs = nil
	if m.historyView.SelectedBeadID() != "" {
		t.Fatalf("expected empty SelectedBeadID() when beadIDs is nil, got %q", m.historyView.SelectedBeadID())
	}
	sessIssue := m.focusedIssueForSessions()
	if sessIssue == nil || sessIssue.ID != "bv-3" {
		t.Fatalf("expected focusedIssueForSessions() in Git Mode to resolve related bead bv-3 when SelectedBeadID() is empty, got %+v", sessIssue)
	}
}

func TestHistoryModel_TabFocusCycleNoTrapAndFileTreeToggleUX(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Auth bug", Status: model.StatusOpen, Priority: 0},
		{ID: "bv-2", Title: "DB migration", Status: model.StatusInProgress, Priority: 1},
		{ID: "bv-3", Title: "Query optimizer", Status: model.StatusClosed, Priority: 2},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(*Model)

	send := func(msg tea.KeyMsg) {
		u, _ := m.Update(msg)
		m = u.(*Model)
	}

	report := createTestHistoryReportWithFiles()
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyView.SetSize(120, 30)
	makeHistoryReportCurrent(m, report)

	// Pressing 'f' opens AND focuses the file tree so j/k/l/h/Enter/Esc behave as advertised.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if !m.historyView.IsFileTreeVisible() || !m.historyView.FileTreeHasFocus() {
		t.Fatalf("expected 'f' to open and focus file tree: visible=%v hasFocus=%v",
			m.historyView.IsFileTreeVisible(), m.historyView.FileTreeHasFocus())
	}
	if m.historyView.IsListFocused() || m.historyView.IsDetailFocused() {
		t.Fatalf("main panes must not simultaneously report focus while fileTreeHasFocus: list=%v detail=%v",
			m.historyView.IsListFocused(), m.historyView.IsDetailFocused())
	}

	// Verify 12-step Tab cycle across compact (75), 2-main-pane boundary (95 in 80..102),
	// 3-main-pane standard (130), and 4-main-pane timeline wide (175).
	for _, tc := range []struct {
		name      string
		width     int
		wantPanes int
		wantCycle []string
	}{
		{
			name:      "compact_75",
			width:     75,
			wantPanes: 2,
			wantCycle: []string{"list", "detail", "filetree"},
		},
		{
			name:      "boundary_95",
			width:     95,
			wantPanes: 2,
			wantCycle: []string{"list", "detail", "filetree"},
		},
		{
			name:      "standard_130",
			width:     130,
			wantPanes: 3,
			wantCycle: []string{"list", "middle", "detail", "filetree"},
		},
		{
			name:      "wide_timeline_175",
			width:     175,
			wantPanes: 4,
			wantCycle: []string{"list", "timeline", "middle", "detail", "filetree"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.historyView.SetSize(tc.width, 30)
			if !m.historyView.IsFileTreeVisible() {
				m.historyView.ToggleFileTree()
			}
			m.historyView.SetFileTreeFocus(true)

			if gotPanes := m.historyView.paneCount(); gotPanes != tc.wantPanes {
				t.Fatalf("width=%d: paneCount()=%d, want %d", tc.width, gotPanes, tc.wantPanes)
			}

			focusState := func() string {
				if m.historyView.FileTreeHasFocus() {
					if m.historyView.IsDetailFocused() || m.historyView.IsListFocused() {
						t.Fatalf("width=%d: dual focus detected alongside fileTree", tc.width)
					}
					return "filetree"
				}
				switch m.historyView.focused {
				case historyFocusList:
					return "list"
				case historyFocusTimeline:
					return "timeline"
				case historyFocusMiddle:
					return "middle"
				case historyFocusDetail:
					return "detail"
				default:
					return "unknown"
				}
			}

			for step := 0; step < 12; step++ {
				send(tea.KeyMsg{Type: tea.KeyTab})
				got := focusState()
				want := tc.wantCycle[step%len(tc.wantCycle)]
				if got != want {
					t.Fatalf("width=%d step=%d: focus=%q, want %q", tc.width, step+1, got, want)
				}
				_ = m.historyView.View()
			}
		})
	}

	// Esc while focused on file tree with no filter closes file tree and stays in History view.
	m.historyView.SetSize(120, 30)
	if !m.historyView.IsFileTreeVisible() {
		m.historyView.ToggleFileTree()
	}
	m.historyView.ClearFileFilter()
	m.historyView.SetFileTreeFocus(true)
	send(tea.KeyMsg{Type: tea.KeyEsc})
	if m.focused != focusHistory || m.historyView.IsFileTreeVisible() || m.historyView.FileTreeHasFocus() {
		t.Fatalf("expected Esc on unfiltered file tree to close tree and stay in History: focused=%v visible=%v hasFocus=%v",
			m.focused, m.historyView.IsFileTreeVisible(), m.historyView.FileTreeHasFocus())
	}
}

func TestHistoryModel_GitModeZeroBeadNoFallbackAndCassBadges(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Bead Mode Stale Selection", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Git Related With Cass", Status: model.StatusInProgress, Priority: 1},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = updated.(*Model)

	report := createTestHistoryReportWithFiles()
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyView.SetSize(140, 30)
	makeHistoryReportCurrent(m, report)
	m.historyView.SetCassLookup(func(beadID string) []cass.ScoredResult {
		if beadID == "bv-2" {
			return []cass.ScoredResult{{SearchResult: cass.SearchResult{Title: "Git Session"}, FinalScore: 0.95}}
		}
		return nil
	})

	// Switch to Git Mode; verify 🤖 session badge renders in Git Mode commit row & detail/related beads!
	m.historyView.ToggleViewMode()
	if !m.historyView.IsGitMode() {
		t.Fatal("expected Git Mode")
	}
	gitView := m.historyView.View()
	if !strings.Contains(gitView, "🤖1") {
		t.Fatalf("expected 🤖1 session indicator in Git Mode view:\n%s", gitView)
	}

	// Case A: Selected Git commit has 0 related beads while hidden Bead-Mode SelectedBeadID() == "bv-1".
	m.historyView.beadIDs = []string{"bv-1"}
	m.historyView.selectedBead = 0
	m.historyView.commitList = []CommitListEntry{
		{SHA: "deadbeef0000", ShortSHA: "deadbee", Message: "Orphan commit", Author: "Dev", BeadIDs: nil},
	}
	m.historyView.selectedGitCommit = 0
	if got := m.focusedIssueForSessions(); got != nil {
		t.Fatalf("Git Mode commit with 0 related beads must NOT fall back to Bead-Mode selection, got %+v", got)
	}
	if iss, reason := m.selectedIssueForActionWithReason(); iss != nil || reason == "" {
		t.Fatalf("Git Mode commit with 0 related beads must return nil issue + reason for O, got iss=%+v reason=%q", iss, reason)
	}

	// Case B: Filter leaves 0 matching commits in Git Mode while Bead-Mode SelectedBeadID() == "bv-1".
	m.historyView.commitList = nil
	if got := m.focusedIssueForSessions(); got != nil {
		t.Fatalf("Git Mode with 0 matching commits must NOT fall back to Bead-Mode selection, got %+v", got)
	}
	if iss, reason := m.selectedIssueForActionWithReason(); iss != nil || reason == "" {
		t.Fatalf("Git Mode with 0 matching commits must return nil issue + reason for O, got iss=%+v reason=%q", iss, reason)
	}
}

func TestRound2HistoryViewModeFocusClampAndFileTreeGlobalKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	GetTutorialProgressManager().Reset()
	t.Cleanup(func() { GetTutorialProgressManager().Reset() })

	// (a) ToggleViewMode from historyFocusTimeline at width >= 150 clamps focus to a visible Git-Mode pane.
	report := createTestHistoryReportWithFiles()
	theme := testTheme()
	h := NewHistoryModel(report, theme)
	h.SetSize(160, 40)
	if h.paneCount() != 4 {
		t.Fatalf("expected 4 panes in wide Bead Mode, got %d", h.paneCount())
	}
	h.ToggleFocus()
	if h.focused != historyFocusTimeline {
		t.Fatalf("expected historyFocusTimeline after first Tab in 4-pane Bead Mode, got %v", h.focused)
	}
	h.ToggleViewMode()
	if !h.IsGitMode() {
		t.Fatal("expected Git Mode after ToggleViewMode")
	}
	if h.focused == historyFocusTimeline || !h.IsListFocused() {
		t.Fatalf("expected ToggleViewMode from historyFocusTimeline at width=160 to clamp focus cleanly (got focused=%v IsListFocused=%v)",
			h.focused, h.IsListFocused())
	}

	// Also verify narrow 2-pane mode: ToggleViewMode from historyFocusTimeline/Middle clamps to visible pane.
	h2 := NewHistoryModel(report, theme)
	h2.SetSize(80, 40)
	h2.focused = historyFocusTimeline
	h2.ToggleViewMode()
	if h2.focused != historyFocusList || !h2.IsListFocused() {
		t.Fatalf("expected narrow Git-Mode ToggleViewMode to clamp focus to historyFocusList, got %v", h2.focused)
	}

	// (b) Pressing ?, ;, ~, v, V, q while history file tree is open and focused triggers global/history actions!
	issues := []model.Issue{
		{ID: "bv-1", Title: "Token Auth", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Session Store", Status: model.StatusInProgress, Priority: 2},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 150, Height: 35})
	m = updated.(*Model)

	m.isHistoryView = true
	m.focused = focusHistory
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyView.SetSize(150, 35)
	makeHistoryReportCurrent(m, report)
	m.historyView.ToggleFileTree()
	m.historyView.SetFileTreeFocus(true)
	if !m.historyView.FileTreeHasFocus() {
		t.Fatal("precondition: expected history file tree open and focused")
	}

	send := func(msg tea.KeyMsg) tea.Cmd {
		t.Helper()
		var cmd tea.Cmd
		updated, cmd = m.Update(msg)
		m = updated.(*Model)
		return cmd
	}

	// '?' opens Help overlay
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !m.showHelp || m.focused != focusHelp {
		t.Fatalf("expected '?' in focused file tree to open Help, got showHelp=%v focused=%v", m.showHelp, m.focused)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showHelp || m.focused != focusHistory || !m.historyView.FileTreeHasFocus() {
		t.Fatalf("expected Esc from Help to return to focused file tree, got showHelp=%v focused=%v treeFocus=%v",
			m.showHelp, m.focused, m.historyView.FileTreeHasFocus())
	}

	// ';' toggles Shortcuts sidebar
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";")})
	if !m.showShortcutsSidebar {
		t.Fatal("expected ';' in focused file tree to enable Shortcuts sidebar")
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";")})
	if m.showShortcutsSidebar {
		t.Fatal("expected second ';' to hide Shortcuts sidebar")
	}

	// '~' opens context Tutorial
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("~")})
	if !m.showTutorial || m.focused != focusTutorial {
		t.Fatalf("expected '~' in focused file tree to open Tutorial, got showTutorial=%v focused=%v", m.showTutorial, m.focused)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTutorial || m.focused != focusHistory || !m.historyView.FileTreeHasFocus() {
		t.Fatalf("expected Esc from Tutorial to return to focused file tree, got showTutorial=%v focused=%v treeFocus=%v",
			m.showTutorial, m.focused, m.historyView.FileTreeHasFocus())
	}

	// 'v' toggles Git/Bead mode even while file tree has focus
	if m.historyView.IsGitMode() {
		t.Fatal("precondition: expected Bead Mode before pressing 'v'")
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !m.historyView.IsGitMode() {
		t.Fatal("expected 'v' while file tree focused to switch History into Git Mode")
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.historyView.IsGitMode() {
		t.Fatal("expected second 'v' while file tree focused to switch back to Bead Mode")
	}

	// 'V' initiates Cass session preview for the focused history row
	vCmd := send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	if vCmd == nil && m.cassRequest == nil && !m.showCassModal && m.statusMsg == "" {
		t.Fatal("expected 'V' while file tree focused to trigger Cass session action, not be swallowed")
	}
	if m.cassRequest != nil {
		m.cancelCassLookup()
	}

	// File-tree nav keys ('j', 'enter') still operate on the file tree
	prevIdx := m.historyView.selectedFileIdx
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if len(m.historyView.flatFileList) > 1 && m.historyView.selectedFileIdx == prevIdx {
		t.Fatal("expected 'j' in focused file tree to move file tree selection")
	}

	// 'q' exits History view instead of being swallowed by file tree; second 'q' quits
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.isHistoryView || m.focused != focusList {
		t.Fatalf("expected 'q' while file tree focused to exit History view to focusList, got isHistoryView=%v focused=%v",
			m.isHistoryView, m.focused)
	}
	quitCmd := send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if quitCmd == nil {
		t.Fatal("expected 'q' at top-level list after exiting History view to return quit cmd")
	}
}

func TestHistoryTimelineWindowMultiLineCommitsAnd24Rows(t *testing.T) {
	now := time.Now().UTC()
	commits := make([]correlation.CorrelatedCommit, 0, 12)
	for i := 0; i < 12; i++ {
		commits = append(commits, correlation.CorrelatedCommit{
			SHA:        fmt.Sprintf("deadbeef%02d", i),
			ShortSHA:   fmt.Sprintf("deadb%02d", i),
			Message:    fmt.Sprintf("Multi-line commit subject %02d\nExtra body line that must not break 1-line commit list", i),
			Author:     "Dev",
			Timestamp:  now.Add(time.Duration(-12+i) * time.Hour),
			Confidence: 0.90,
		})
	}
	dur := 12 * time.Hour
	report := &correlation.HistoryReport{
		GeneratedAt: now,
		Histories: map[string]correlation.BeadHistory{
			"BEAD-TL": {
				BeadID: "BEAD-TL",
				Title:  "Timeline 24-row test",
				Status: "closed",
				Milestones: correlation.BeadMilestones{
					Created: &correlation.BeadEvent{Timestamp: now.Add(-14 * time.Hour)},
					Closed:  &correlation.BeadEvent{Timestamp: now},
				},
				Commits:   commits,
				CycleTime: &correlation.CycleTime{CreateToClose: &dur},
			},
		},
		CommitIndex: correlation.CommitIndex{},
	}

	h := NewHistoryModel(report, DefaultTheme(lipgloss.NewRenderer(nil)))
	h.SetSize(120, 22) // 24-row terminal -> 22-row history body

	// Verify 3-column Bead commit list sanitizes multi-line commit messages to 1 line.
	commitListOut := h.View()
	if strings.Contains(commitListOut, "Extra body line") {
		t.Fatalf("expected multi-line commit message sanitized to first line in Bead commit list:\n%s", commitListOut)
	}

	// Switch to 4-pane layout (width 160 so each commit renders 2 lines in Timeline!), focus Timeline, and scroll to bottom:
	// Cycle summary and final Closed milestone must BOTH be visible & unclipped on 24-row terminal (22-row body)!
	h.SetSize(160, 22)
	if !h.TimelineVisible() {
		h.ToggleTimeline()
	}
	h.ToggleFocus() // List -> Timeline
	for i := 0; i < 20; i++ {
		h.MoveDown()
	}
	out := h.View()
	if lines := strings.Split(out, "\n"); len(lines) > 22 {
		t.Fatalf("expected 24-row HistoryModel.View() <= 22 body lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "Closed") || !strings.Contains(out, "Cycle:") {
		t.Fatalf("expected scrolled Bead timeline on 24-row terminal to reach Closed milestone and keep Cycle footer unclipped:\n%s", out)
	}
}
