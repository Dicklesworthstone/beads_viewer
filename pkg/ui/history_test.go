package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/charmbracelet/lipgloss"
)

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

func TestHistoryModel_TimelineOffsetResetsAcrossBeadChanges(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	commits := func(prefix string, count int) []correlation.CorrelatedCommit {
		result := make([]correlation.CorrelatedCommit, count)
		for i := range result {
			result[i] = correlation.CorrelatedCommit{
				SHA:       fmt.Sprintf("%s-%02d", prefix, i),
				ShortSHA:  fmt.Sprintf("%s-%02d", prefix, i),
				Message:   fmt.Sprintf("%s commit %d", prefix, i),
				Timestamp: now.Add(time.Duration(i) * time.Minute),
			}
		}
		return result
	}
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"LONG":  {BeadID: "LONG", Title: "Long history", Commits: commits("long", 10)},
		"SHORT": {BeadID: "SHORT", Title: "Shorter history", Commits: commits("short", 6)},
	}}
	h := NewHistoryModel(report, testTheme())
	h.SetSize(180, 12) // Four visible timeline rows.
	if h.SelectedBeadID() != "LONG" {
		t.Fatalf("initial bead = %q, want LONG", h.SelectedBeadID())
	}
	h.focused = historyFocusTimeline
	h.MoveDown()
	h.MoveDown()
	if h.timelineScrollOffset != 2 {
		t.Fatalf("long-history offset = %d, want 2", h.timelineScrollOffset)
	}

	h.focused = historyFocusList
	h.MoveDown()
	if h.SelectedBeadID() != "SHORT" || h.timelineScrollOffset != 0 {
		t.Fatalf("short selection = %q offset=%d, want SHORT offset 0", h.SelectedBeadID(), h.timelineScrollOffset)
	}
	h.focused = historyFocusTimeline
	h.MoveDown()
	if h.timelineScrollOffset != 1 {
		t.Fatalf("short-history first MoveDown offset = %d, want 1", h.timelineScrollOffset)
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
	for _, commit := range h.histories[0].Commits {
		if commit.Author != "Dev One" {
			t.Fatalf("author filter retained commit by %q", commit.Author)
		}
	}

	// Clear filter
	h.SetAuthorFilter("")
	if len(h.histories) != 3 {
		t.Errorf("histories after clearing filter = %d, want 3", len(h.histories))
	}
}

func TestHistoryModel_AuthorFilterNarrowsGitCommitList(t *testing.T) {
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{
				{SHA: "alice", Author: "Alice", AuthorEmail: "alice@example.com"},
				{SHA: "bob", Author: "Bob", AuthorEmail: "bob@example.com"},
			},
		},
	}}
	h := NewHistoryModel(report, testTheme())
	h.ToggleViewMode()
	h.SetAuthorFilter("alice@example.com")

	commits := h.GetFilteredCommitList()
	if len(commits) != 1 || commits[0].SHA != "alice" {
		t.Fatalf("git author filter returned %+v, want only alice", commits)
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

func TestHistoryModel_GitAllSearchIncludesBeadTitles(t *testing.T) {
	report := createTestHistoryReport()
	h := NewHistoryModel(report, testTheme())
	h.ToggleViewMode()
	h.StartSearch()
	h.searchInput.SetValue("authentication")
	h.applySearchFilter()

	commits := h.GetFilteredCommitList()
	if len(commits) != 2 {
		t.Fatalf("git all-field title search returned %d commits, want 2", len(commits))
	}
	for _, commit := range commits {
		if !slices.Contains(commit.BeadIDs, "bv-1") {
			t.Fatalf("git all-field title search returned unrelated commit %+v", commit)
		}
	}
}

func TestHistoryModel_GitModeComposesSearchConfidenceAndFileFilters(t *testing.T) {
	report := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"A": {
				BeadID: "A",
				Commits: []correlation.CorrelatedCommit{
					{
						SHA:        "keep-target",
						ShortSHA:   "keep",
						Message:    "keep this change",
						Author:     "Alice",
						Confidence: 0.9,
						Files:      []correlation.FileChange{{Path: "target.go"}},
					},
					{
						SHA:        "low-target",
						ShortSHA:   "low",
						Message:    "keep low confidence",
						Author:     "Alice",
						Confidence: 0.2,
						Files:      []correlation.FileChange{{Path: "target.go"}},
					},
				},
			},
			"B": {
				BeadID: "B",
				Commits: []correlation.CorrelatedCommit{{
					SHA:        "keep-other",
					ShortSHA:   "other",
					Message:    "keep unrelated file",
					Author:     "Bob",
					Confidence: 0.95,
					Files:      []correlation.FileChange{{Path: "other.go"}},
				}},
			},
		},
	}

	h := NewHistoryModel(report, testTheme())
	h.ToggleViewMode()
	h.StartSearch()
	h.searchInput.SetValue("keep")
	h.applySearchFilter()
	h.FinishSearch()
	h.SetMinConfidence(0.5)
	h.fileFilter = "target.go"
	h.applySearchFilter()

	commits := h.GetFilteredCommitList()
	if len(commits) != 1 || commits[0].SHA != "keep-target" {
		t.Fatalf("composed git filters returned %+v, want only keep-target", commits)
	}

	refreshed := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": report.Histories["A"],
		"C": {
			BeadID: "C",
			Commits: []correlation.CorrelatedCommit{{
				SHA:        "new-wrong-file",
				ShortSHA:   "new",
				Message:    "keep new change",
				Author:     "Alice",
				Confidence: 0.99,
				Files:      []correlation.FileChange{{Path: "elsewhere.go"}},
			}},
		},
	}}
	h.SetReport(refreshed)

	commits = h.GetFilteredCommitList()
	if len(commits) != 1 || commits[0].SHA != "keep-target" {
		t.Fatalf("refreshed composed git filters returned %+v, want only keep-target", commits)
	}
	if got := h.SearchQuery(); got != "keep" {
		t.Fatalf("refreshed search query=%q, want keep", got)
	}
}

func TestHistoryModel_TimelineUsesFilteredCommitSlice(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	commits := []correlation.CorrelatedCommit{{
		SHA:        "visible",
		ShortSHA:   "visible",
		Message:    "visible-marker",
		Author:     "Alice",
		Timestamp:  now,
		Confidence: 0.9,
	}}
	for i := 0; i < 5; i++ {
		commits = append(commits, correlation.CorrelatedCommit{
			SHA:        fmt.Sprintf("hidden-%d", i),
			ShortSHA:   fmt.Sprintf("hide-%d", i),
			Message:    fmt.Sprintf("hidden-marker-%d", i),
			Author:     "Bob",
			Timestamp:  now.Add(time.Duration(i+1) * time.Minute),
			Confidence: 0.9,
		})
	}
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {BeadID: "A", Title: "Filtered timeline", Status: "open", Commits: commits},
	}}

	h := NewHistoryModel(report, testTheme())
	h.SetSize(100, 9)
	h.timelineScrollOffset = 4
	h.SetAuthorFilter("Alice")
	if h.timelineScrollOffset != 0 {
		t.Fatalf("filtered timeline retained stale offset %d, want 0", h.timelineScrollOffset)
	}
	if selected := h.SelectedHistory(); selected == nil || len(selected.Commits) != 1 {
		t.Fatalf("filtered selected history = %+v, want one Alice commit", selected)
	}

	rendered := h.renderTimelinePanel(100, 8)
	if !strings.Contains(rendered, "visible-marker") {
		t.Fatalf("filtered timeline omitted visible commit: %q", rendered)
	}
	if strings.Contains(rendered, "hidden-marker") {
		t.Fatalf("filtered timeline leaked a commit excluded by the author filter: %q", rendered)
	}

	h.focused = historyFocusTimeline
	h.MoveDown()
	if h.timelineScrollOffset != 0 {
		t.Fatalf("timeline scroll offset = %d, want 0 for the one filtered entry", h.timelineScrollOffset)
	}
}

func TestHistoryModel_TimelineBudgetsRenderedRowsAndShowsFinalEntry(t *testing.T) {
	const finalSentinel = "FINAL-TIMELINE"
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	commits := make([]correlation.CorrelatedCommit, 9)
	for i := range commits {
		message := fmt.Sprintf("timeline message %d", i)
		if i == len(commits)-1 {
			message = finalSentinel
		}
		commits[i] = correlation.CorrelatedCommit{
			SHA:        fmt.Sprintf("commit-%02d", i),
			ShortSHA:   fmt.Sprintf("c-%02d", i),
			Message:    message,
			Timestamp:  now.Add(time.Duration(i) * time.Minute),
			Confidence: 0.9,
		}
	}
	cycle := 48 * time.Hour
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID:    "A",
			Title:     "Rendered row accounting",
			Commits:   commits,
			CycleTime: &correlation.CycleTime{CreateToClose: &cycle},
		},
	}}

	h := NewHistoryModel(report, testTheme())
	h.SetSize(220, 22)
	_ = h.StartSearch() // The bordered search box makes the rendered header taller.
	headerHeight := lipgloss.Height(h.renderHeader())
	if headerHeight <= 4 {
		t.Fatalf("search header height=%d, want a multi-row header fixture", headerHeight)
	}
	panelWidth, panelHeight := h.timelinePanelSize()
	if want := h.height - headerHeight; panelHeight != want {
		t.Fatalf("timeline panel height=%d, want terminal budget %d", panelHeight, want)
	}

	layout := h.layoutTimeline(h.SelectedHistory(), panelWidth, panelHeight)
	if got := lipgloss.Height(layout.entries[0]); got != 2 {
		t.Fatalf("rendered commit entry height=%d, want 2 rows", got)
	}
	if !layout.showScroll {
		t.Fatal("multi-row fixture unexpectedly fits without scrolling")
	}

	h.focused = historyFocusTimeline
	for i := 0; i < len(commits)+1; i++ {
		h.MoveDown()
	}
	if h.timelineScrollOffset == 0 {
		t.Fatal("timeline did not advance to its final rendered-row window")
	}
	panel := h.renderTimelinePanel(panelWidth, panelHeight)
	if got := lipgloss.Height(panel); got != panelHeight {
		t.Fatalf("rendered timeline panel height=%d, want %d", got, panelHeight)
	}
	if !strings.Contains(panel, finalSentinel) {
		t.Fatalf("final timeline sentinel was not visible at max scroll: %q", panel)
	}
	if !strings.Contains(panel, "Cycle: 2d") {
		t.Fatalf("timeline summary footer was not visible: %q", panel)
	}

	view := h.View()
	if got := lipgloss.Height(view); got != h.height {
		t.Fatalf("rendered History height=%d, want configured height %d", got, h.height)
	}
}

func TestHistoryModel_TimelinePrefersEntryOverSummaryWhenRowsAreTight(t *testing.T) {
	const entrySentinel = "VISIBLE-TIMELINE-EVENT"
	cycle := 24 * time.Hour
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{{
				SHA:        "commit-a",
				ShortSHA:   "c-a",
				Message:    entrySentinel,
				Timestamp:  time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
				Confidence: 0.9,
			}},
			CycleTime: &correlation.CycleTime{CreateToClose: &cycle},
		},
	}}

	h := NewHistoryModel(report, testTheme())
	const panelWidth, panelHeight = 80, 6 // Four inner rows: title plus one two-row entry.
	layout := h.layoutTimeline(h.SelectedHistory(), panelWidth, panelHeight)
	entryRows := lipgloss.Height(layout.entries[0])
	if entryRows != 2 {
		t.Fatalf("fixture entry height=%d, want 2", entryRows)
	}
	if layout.summary != "" {
		t.Fatalf("tight layout retained optional summary and left entry budget=%d", layout.entryRowBudget)
	}
	if layout.entryRowBudget < entryRows {
		t.Fatalf("entry row budget=%d, want at least %d", layout.entryRowBudget, entryRows)
	}
	if end := timelineWindowEnd(layout.entries, 0, layout.entryRowBudget); end != 1 {
		t.Fatalf("tight layout rendered %d entries, want the primary event", end)
	}

	panel := h.renderTimelinePanel(panelWidth, panelHeight)
	if got := lipgloss.Height(panel); got != panelHeight {
		t.Fatalf("tight timeline panel height=%d, want %d", got, panelHeight)
	}
	if !strings.Contains(panel, entrySentinel) {
		t.Fatalf("tight timeline omitted the event sentinel: %q", panel)
	}
	if strings.Contains(panel, "Cycle:") {
		t.Fatalf("tight timeline rendered optional summary ahead of its event: %q", panel)
	}
}

func TestHistoryModel_NegativeSelectionsAreUnavailable(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReportWithFiles(), testTheme())
	h.ToggleFileTree()
	h.selectedFileIdx = -1
	if got := h.SelectedFileName(); got != "" {
		t.Fatalf("SelectedFileName with negative index = %q, want empty", got)
	}
	if got := h.SelectedFileNode(); got != nil {
		t.Fatalf("SelectedFileNode with negative index = %+v, want nil", got)
	}
	h.CollapseFileNode()
	h.ToggleExpandFile()
	h.MoveDownFileTree()
	if h.selectedFileIdx != 0 {
		t.Fatalf("MoveDownFileTree did not recover negative selection: got %d, want 0", h.selectedFileIdx)
	}

	h.selectedBead = -1
	if got := h.SelectedBeadID(); got != "" {
		t.Fatalf("SelectedBeadID with negative index = %q, want empty", got)
	}
	if got := h.SelectedHistory(); got != nil {
		t.Fatalf("SelectedHistory with negative index = %+v, want nil", got)
	}
	h.NextCommit()
	h.ToggleExpand()
	h.focused = historyFocusMiddle
	h.MoveDown()
	h.focused = historyFocusTimeline
	_ = h.renderTimelinePanel(80, 10)

	h.ToggleViewMode()
	h.selectedGitCommit = -1
	if got := h.SelectedGitCommit(); got != nil {
		t.Fatalf("SelectedGitCommit with negative index = %+v, want nil", got)
	}
	h.focused = historyFocusMiddle
	h.MoveDownGit()
	h.NextRelatedBead()
}

func TestHistoryModel_FilterSetterPreservesActiveSearch(t *testing.T) {
	h := NewHistoryModel(createTestHistoryReport(), testTheme())
	h.StartSearch()
	h.searchInput.SetValue("indexes")
	h.applySearchFilter()

	h.SetMinConfidence(0.5)
	if got := h.beadIDs; len(got) != 1 || got[0] != "bv-3" {
		t.Fatalf("confidence update discarded active search: bead IDs=%v, want [bv-3]", got)
	}

	h.SetAuthorFilter("Dev Two")
	if got := h.beadIDs; len(got) != 1 || got[0] != "bv-3" {
		t.Fatalf("author update discarded active search: bead IDs=%v, want [bv-3]", got)
	}
}

func TestHistoryModel_FiltersPreserveSurvivingSelectionIdentity(t *testing.T) {
	beadMode := NewHistoryModel(createTestHistoryReport(), testTheme())
	for i, beadID := range beadMode.beadIDs {
		if beadID == "bv-2" {
			beadMode.selectedBead = i
			break
		}
	}
	beadMode.SetAuthorFilter("Dev Two")
	if got := beadMode.SelectedBeadID(); got != "bv-2" {
		t.Fatalf("bead filter moved surviving selection to %q, want bv-2", got)
	}

	now := time.Now()
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{
				{SHA: "new-low", Timestamp: now, Confidence: 0.1},
				{SHA: "middle-low", Timestamp: now.Add(-time.Minute), Confidence: 0.2},
				{SHA: "old-high", Timestamp: now.Add(-2 * time.Minute), Confidence: 0.9},
			},
		},
	}}
	gitMode := NewHistoryModel(report, testTheme())
	gitMode.ToggleViewMode()
	for i, commit := range gitMode.GetFilteredCommitList() {
		if commit.SHA == "old-high" {
			gitMode.selectedGitCommit = i
			break
		}
	}
	gitMode.SetMinConfidence(0.5)
	if selected := gitMode.SelectedGitCommit(); selected == nil || selected.SHA != "old-high" {
		t.Fatalf("git filter lost surviving commit selection: %+v", selected)
	}
}

func TestHistoryModel_GitCommitListSortsByFullTimestamp(t *testing.T) {
	minute := time.Date(2026, 8, 26, 12, 34, 0, 0, time.UTC)
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{
				// The SHA order deliberately disagrees with chronological order.
				{SHA: "zzz-older", Timestamp: minute.Add(5 * time.Second)},
				{SHA: "aaa-newer", Timestamp: minute.Add(50 * time.Second)},
			},
		},
	}}

	h := NewHistoryModel(report, testTheme())
	h.ToggleViewMode()
	commits := h.GetFilteredCommitList()
	if len(commits) != 2 {
		t.Fatalf("git commit count = %d, want 2", len(commits))
	}
	if commits[0].SHA != "aaa-newer" || commits[1].SHA != "zzz-older" {
		t.Fatalf("git commit order = [%s %s], want [aaa-newer zzz-older]", commits[0].SHA, commits[1].SHA)
	}
	if commits[0].Timestamp != commits[1].Timestamp {
		t.Fatalf("fixture display timestamps differ: %q vs %q", commits[0].Timestamp, commits[1].Timestamp)
	}
}

func TestHistoryModel_SearchDoesNotResurrectConfidenceFilteredCommit(t *testing.T) {
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{
				{SHA: "high", Message: "retained", Confidence: 0.9},
				{SHA: "low", Message: "excluded needle", Confidence: 0.2},
			},
		},
	}}
	h := NewHistoryModel(report, testTheme())
	h.StartSearch()
	h.searchInput.SetValue("excluded needle")
	h.applySearchFilter()
	h.SetMinConfidence(0.5)

	if len(h.beadIDs) != 0 || len(h.histories) != 0 {
		t.Fatalf("search resurrected confidence-filtered commit: IDs=%v histories=%+v", h.beadIDs, h.histories)
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
	expected := 40 - 3 // height - header reserve
	if intsDiffer(h.listHeight(), expected) {
		t.Errorf("listHeight() = %d, want %d", h.listHeight(), expected)
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

	// Should not panic
	h.MoveUp()
	h.MoveDown()
	h.NextCommit()
	h.PrevCommit()
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
