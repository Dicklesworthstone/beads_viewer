package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Basic helpers and tiny behaviours that were previously uncovered.
func TestIssueItemBasicsAndPrefix(t *testing.T) {
	issue := model.Issue{
		ID:        "api-AUTH-123",
		Title:     "Auth plumbing",
		Status:    model.StatusOpen,
		IssueType: model.TypeFeature,
		Assignee:  "alice",
		Labels:    []string{"backend", "security"},
	}
	item := IssueItem{Issue: issue, RepoPrefix: ExtractRepoPrefix(issue.ID)}

	if got := item.Title(); got != issue.Title {
		t.Fatalf("Title() = %s, want %s", got, issue.Title)
	}
	desc := item.Description()
	if !strings.Contains(desc, issue.ID) || !strings.Contains(desc, string(issue.Status)) {
		t.Fatalf("Description() missing pieces: %s", desc)
	}
	filter := item.FilterValue()
	for _, want := range []string{issue.Title, issue.ID, "backend", "security", "api"} {
		if !strings.Contains(filter, want) {
			t.Fatalf("FilterValue missing %q in %q", want, filter)
		}
	}
	if got := ExtractRepoPrefix("web:UI-9"); got != "web" {
		t.Fatalf("ExtractRepoPrefix wrong for colon sep: %s", got)
	}
	if got := ExtractRepoPrefix("noprefix"); got != "" {
		t.Fatalf("ExtractRepoPrefix expected empty, got %s", got)
	}
	if isAlphanumeric("abc123") == false || isAlphanumeric("nope-") {
		t.Fatalf("isAlphanumeric behaviour unexpected")
	}
}

func TestRecipePickerIndexesAndCounts(t *testing.T) {
	loader := recipe.NewLoader()
	if err := loader.Load(); err != nil {
		t.Skipf("recipes not available: %v", err)
	}
	picker := NewRecipePickerModel(loader.List(), DefaultTheme(lipgloss.NewRenderer(nil)))
	if picker.SelectedIndex() != 0 {
		t.Fatalf("initial SelectedIndex = %d, want 0", picker.SelectedIndex())
	}
	picker.MoveDown()
	if picker.SelectedIndex() == 0 {
		t.Fatalf("MoveDown did not change selection")
	}
	if picker.RecipeCount() != len(loader.List()) {
		t.Fatalf("RecipeCount mismatch")
	}
}

func TestRenderSubtleDivider(t *testing.T) {
	if out := RenderSubtleDivider(10); len(strings.TrimSpace(out)) == 0 {
		t.Fatalf("RenderSubtleDivider returned empty output")
	}
}

func TestHandleListKeysFiltersAndTimeTravelPrompt(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "One", Status: model.StatusOpen},
		{ID: "2", Title: "Two", Status: model.StatusOpen},
		{ID: "3", Title: "Three", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")
	m.height = 30
	m.width = 80
	m.focused = focusList
	m.isSplitView = false

	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if m.currentFilter != "open" {
		t.Fatalf("expected filter 'open', got %s", m.currentFilter)
	}
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.currentFilter != "closed" {
		t.Fatalf("expected filter 'closed', got %s", m.currentFilter)
	}
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.currentFilter != "ready" {
		t.Fatalf("expected filter 'ready', got %s", m.currentFilter)
	}

	// Paging up/down
	m.list.Select(0)
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.list.Index() == 0 {
		t.Fatalf("ctrl+d should move selection down")
	}
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.list.Index() != 0 {
		t.Fatalf("ctrl+u should move selection up")
	}

	// Enter should flip showDetails in mobile view
	m.showDetails = false
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.showDetails {
		t.Fatalf("enter should show details when not split view")
	}

	// Time-travel prompt toggling
	m.timeTravelMode = false
	m = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.showTimeTravelPrompt || m.focused != focusTimeTravelInput {
		t.Fatalf("time-travel prompt not activated")
	}
	// Cancel via Esc to avoid git dependency
	m = m.handleTimeTravelInputKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTimeTravelPrompt {
		t.Fatalf("prompt should close on esc")
	}
	if m.focused != focusList {
		t.Fatalf("focus should return to list after esc")
	}
}

func TestViewTogglesGraphBoardInsightsActionable(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Alpha", Status: model.StatusOpen},
		{ID: "B", Title: "Beta", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")
	// Prime layout so width/height are non-zero
	_, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})

	// Graph toggle
	modelAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = modelAny.(Model)
	if !m.isGraphView || m.focused != focusGraph {
		t.Fatalf("graph view not activated")
	}

	// Board toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = modelAny.(Model)
	if !m.isBoardView || m.focused != focusBoard {
		t.Fatalf("board view not activated")
	}

	// Insights toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = modelAny.(Model)
	if m.focused != focusInsights {
		t.Fatalf("insights not focused after toggle")
	}

	// Actionable toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = modelAny.(Model)
	if !m.isActionableView || m.focused != focusActionable {
		t.Fatalf("actionable view not activated")
	}

	// Priority hints toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m = modelAny.(Model)
	if !m.showPriorityHints {
		t.Fatalf("priority hints should toggle on with 'p'")
	}

	// Recipe picker toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	m = modelAny.(Model)
	if !m.showRecipePicker || m.focused != focusRecipePicker {
		t.Fatalf("recipe picker not opened correctly")
	}
}

func TestHandleGraphBoardActionableKeys(t *testing.T) {
	issues := []model.Issue{
		{ID: "X", Title: "Cross", Status: model.StatusOpen},
		{ID: "Y", Title: "Why", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 30

	// Focus graph and exercise navigation + enter selection logic
	m.isGraphView = true
	m.focused = focusGraph
	m = m.handleGraphKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")}) // ScrollLeft
	m = m.handleGraphKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")}) // ScrollRight
	// force select first node then enter to sync list
	m.graphView.MoveDown()
	m = m.handleGraphKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.isGraphView {
		t.Fatalf("enter should exit graph view")
	}

	// Focus board navigation paths
	m.isBoardView = true
	m.focused = focusBoard
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	// Enter should exit board when selection exists
	m.board.MoveToTop()
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.isBoardView {
		t.Fatalf("enter should exit board view")
	}

	// Actionable view enter selects matching issue in list
	plan := analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{{
			TrackID: "t1",
			Items:   []analysis.PlanItem{{ID: "X", Title: "Cross", Status: "open"}},
		}},
		TotalActionable: 1,
	}
	m.isActionableView = true
	m.focused = focusActionable
	m.actionableView = NewActionableModel(plan, m.theme)
	m = m.handleActionableKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.isActionableView {
		t.Fatalf("enter should exit actionable view")
	}
}

func TestHandleRecipePickerAndInsightsKeys(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "One", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")
	m.width, m.height = 100, 20

	// Seed insights with a selected item
	ins := analysis.Insights{
		Bottlenecks: []analysis.InsightItem{{ID: "1", Value: 1}},
		Stats:       m.analysis,
	}
	m.insightsPanel = NewInsightsModel(ins, m.issueMap, m.theme)
	m.focused = focusInsights
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEnter})

	// Recipe picker escape path
	m.showRecipePicker = true
	m.focused = focusRecipePicker
	m = m.handleRecipePickerKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = m.handleRecipePickerKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = m.handleRecipePickerKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showRecipePicker {
		t.Fatalf("recipe picker should close on esc")
	}

	// Enter applies selection
	m.showRecipePicker = true
	m.focused = focusRecipePicker
	m = m.handleRecipePickerKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.activeRecipe == nil || m.showRecipePicker {
		t.Fatalf("enter should apply recipe and close picker")
	}
}

func TestWaitForPhase2CmdCompletes(t *testing.T) {
	stats := analysis.NewGraphStatsForTest(
		map[string]float64{"A": 1},
		map[string]float64{"A": 1},
		map[string]float64{"A": 1},
		map[string]float64{"A": 1},
		map[string]float64{"A": 1},
		map[string]float64{"A": 1},
		map[string]int{"A": 0},
		map[string]int{"A": 0},
		nil,
		0,
		nil,
	)
	cmd := WaitForPhase2Cmd(stats)
	if msg := cmd(); msg == nil {
		t.Fatalf("expected Phase2ReadyMsg")
	}
}

func TestDiffStatusAndExitTimeTravel(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.timeTravelMode = true
	m.newIssueIDs = map[string]bool{"N": true}
	m.closedIssueIDs = map[string]bool{"C": true}
	m.modifiedIssueIDs = map[string]bool{"M": true}

	if got := m.getDiffStatus("N"); got != DiffStatusNew {
		t.Fatalf("expected DiffStatusNew, got %v", got)
	}
	if got := m.getDiffStatus("C"); got != DiffStatusClosed {
		t.Fatalf("expected DiffStatusClosed, got %v", got)
	}
	if got := m.getDiffStatus("M"); got != DiffStatusModified {
		t.Fatalf("expected DiffStatusModified, got %v", got)
	}
	if got := m.getDiffStatus("X"); got != DiffStatusNone {
		t.Fatalf("expected DiffStatusNone, got %v", got)
	}

	// exit path should clear maps and status
	m.exitTimeTravelMode()
	if m.timeTravelMode || m.newIssueIDs != nil || m.closedIssueIDs != nil || m.modifiedIssueIDs != nil {
		t.Fatalf("exitTimeTravelMode should clear state")
	}
	if m.statusMsg == "" {
		t.Fatalf("exitTimeTravelMode should set status message")
	}
}

func TestRenderFooterStatusAndBadges(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.width = 80

	// status message branch
	m.statusMsg = "Saved"
	m.statusIsError = false
	footer := m.renderFooter()
	if !strings.Contains(footer, "Saved") {
		t.Fatalf("footer should include status message")
	}

	// badges branch
	m.statusMsg = ""
	m.currentFilter = "ready"
	m.countOpen, m.countReady, m.countBlocked, m.countClosed = 1, 2, 3, 4
	m.updateAvailable = true
	m.updateTag = "v9.9.9"
	m.workspaceMode = true
	m.workspaceSummary = "2 repos"
	footer = m.renderFooter()
	for _, expect := range []string{"READY", "◉", "⭐", "📦"} {
		if !strings.Contains(footer, expect) {
			t.Fatalf("footer missing %s: %s", expect, footer)
		}
	}
}

func TestExportToMarkdownSmoke(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	issues := []model.Issue{{
		ID:        "R-1",
		Title:     "Report me",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		CreatedAt: time.Now(),
	}}
	m := NewModel(issues, nil, "")
	m.exportToMarkdown()

	files, _ := os.ReadDir(".")
	if len(files) == 0 {
		t.Fatalf("expected export file to be written")
	}
	if m.statusIsError {
		t.Fatalf("exportToMarkdown should succeed, got error status")
	}
}

func TestGraphConnectorDown(t *testing.T) {
	renderer := lipgloss.NewRenderer(nil)
	theme := DefaultTheme(renderer)
	g := &GraphModel{theme: theme}

	if out := g.renderConnectorDown(0, 20, theme); out != "" {
		t.Fatalf("count 0 should return empty string")
	}
	if out := g.renderConnectorDown(1, 10, theme); !strings.Contains(out, "▼") {
		t.Fatalf("single connector missing arrow")
	}
	if out := g.renderConnectorDown(3, 20, theme); !strings.Contains(out, "┼") {
		t.Fatalf("multi connector should include fan pattern, got %q", out)
	}
}

func TestCopyIssueToClipboardNoSelection(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.copyIssueToClipboard()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No issue selected") {
		t.Fatalf("expected error status for missing selection")
	}
}

func TestOpenInEditorTerminalEditorGuard(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	_ = os.MkdirAll(filepath.Join(tmp, ".beads"), 0755)
	beadsFile := filepath.Join(tmp, ".beads", "issues.jsonl")
	_ = os.WriteFile(beadsFile, []byte("{}"), 0644)
	_ = os.Chdir(tmp)

	origEditor := os.Getenv("EDITOR")
	defer os.Setenv("EDITOR", origEditor)
	_ = os.Setenv("EDITOR", "vim") // triggers terminal-editor guard, no exec

	m := NewModel(nil, nil, beadsFile)
	m.openInEditor()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "terminal editor") {
		t.Fatalf("expected terminal editor warning, got %q", m.statusMsg)
	}
}

func TestGraphPageDownAndScrollEmpty(t *testing.T) {
	renderer := lipgloss.NewRenderer(nil)
	g := NewGraphModel(nil, nil, DefaultTheme(renderer))
	g.PageDown()   // len=0 branch
	g.ScrollLeft() // no-op branches
	g.ScrollRight()
	g.ensureVisible()
}

func TestRenderFooterErrorStatus(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.width = 40
	m.statusMsg = "boom"
	m.statusIsError = true
	out := m.renderFooter()
	if !strings.Contains(out, "boom") {
		t.Fatalf("footer should show error status")
	}
}

func TestRenderSplitAndListViews(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Alpha", Status: model.StatusOpen},
		{ID: "2", Title: "Beta", Status: model.StatusClosed},
	}
	m := NewModel(issues, nil, "")

	// Prime layout into split view
	modelAny, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	m = modelAny.(Model)
	m.isSplitView = true
	out := m.renderSplitView()
	if !strings.Contains(out, "Alpha") || !strings.Contains(out, "Beta") {
		t.Fatalf("renderSplitView missing issue titles: %s", out)
	}

	// Mobile/list-only view path
	m.isSplitView = false
	m.showDetails = false
	m.ready = true
	m.width = 90
	m.height = 30
	listOut := m.renderListWithHeader()
	if !strings.Contains(listOut, "Alpha") {
		t.Fatalf("renderListWithHeader missing content: %s", listOut)
	}
}

func TestInitAndStopNoWatcher(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "1", Title: "x", Status: model.StatusOpen}}, nil, "")
	if cmd := m.Init(); cmd == nil {
		t.Fatalf("Init should return a command batch")
	}
	// Stop should be safe when watcher is nil
	m.Stop()

	// Stop with real watcher
	tmp := t.TempDir()
	f := filepath.Join(tmp, "beads.jsonl")
	_ = os.WriteFile(f, []byte("{}"), 0o644)
	w, err := watcher.NewWatcher(f, watcher.WithForcePoll(true), watcher.WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("watcher create: %v", err)
	}
	_ = w.Start()
	m.watcher = w
	m.Stop()
	if w.IsStarted() {
		t.Fatalf("watcher should be stopped")
	}
}

func TestBoardAndInsightsExtraKeys(t *testing.T) {
	issues := []model.Issue{{ID: "1", Title: "One", Status: model.StatusOpen}}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 30

	// Board page up/down coverage
	m.isBoardView = true
	m.focused = focusBoard
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyHome})
	m = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyEnd})

	// Insights escape and tab navigation
	m.focused = focusInsights
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyTab})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.focused != focusList {
		t.Fatalf("Esc should return focus to list")
	}

	// Time-travel input enter path (will fail gracefully without git)
	origWD, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	m.showTimeTravelPrompt = true
	m.focused = focusTimeTravelInput
	m.timeTravelInput.SetValue("HEAD~1")
	m = m.handleTimeTravelInputKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.statusIsError && m.statusMsg == "" {
		t.Fatalf("expected status message after attempting time-travel without git")
	}
}

func TestOpenInEditorMissingAndGUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("openInEditor GUI path is unreliable on headless Windows CI")
	}
	// Empty beadsPath branch
	tmp := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	_ = os.Chdir(tmp)

	m := NewModel([]model.Issue{{ID: "1", Title: "x", Status: model.StatusOpen}}, nil, "")
	m.openInEditor()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No beads file") {
		t.Fatalf("expected missing beads error, got %q", m.statusMsg)
	}

	// Success branch with GUI-ish editor
	beadsDir := filepath.Join(tmp, ".beads")
	_ = os.Mkdir(beadsDir, 0o755)
	beadsFile := filepath.Join(beadsDir, "issues.jsonl")
	_ = os.WriteFile(beadsFile, []byte(`{}`), 0o644)

	origEditor := os.Getenv("EDITOR")
	t.Cleanup(func() { _ = os.Setenv("EDITOR", origEditor) })
	_ = os.Setenv("EDITOR", "true") // present on POSIX; not in terminal editor block

	m = NewModel([]model.Issue{{ID: "1", Title: "x", Status: model.StatusOpen}}, nil, beadsFile)
	m.openInEditor()
	if m.statusIsError || !strings.Contains(m.statusMsg, "Opened in") {
		t.Fatalf("expected success opening editor, got %q", m.statusMsg)
	}
}

func TestExportToMarkdownCreatesFile(t *testing.T) {
	tmp := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	_ = os.Chdir(tmp)

	issues := []model.Issue{{ID: "1", Title: "Alpha", Status: model.StatusOpen}}
	m := NewModel(issues, nil, "")
	filename := m.generateExportFilename()

	m.exportToMarkdown()

	if _, err := os.Stat(filepath.Join(tmp, filename)); err != nil {
		t.Fatalf("expected export file to exist: %v", err)
	}
	if m.statusIsError {
		t.Fatalf("export should succeed, got error %q", m.statusMsg)
	}
}

func TestWatchFileCmdDetectsChange(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "file.txt")
	_ = os.WriteFile(file, []byte("hi"), 0o644)

	w, err := watcher.NewWatcher(file,
		watcher.WithForcePoll(true),
		watcher.WithPollInterval(20*time.Millisecond),
		watcher.WithDebounceDuration(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Stop()

	// Modify the file after a short delay
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = os.WriteFile(file, []byte("bye"), 0o644)
	}()

	cmd := WatchFileCmd(w)
	msg := cmd()
	if _, ok := msg.(FileChangedMsg); !ok {
		t.Fatalf("expected FileChangedMsg, got %T", msg)
	}
}

func TestRenderFooterVariantsAndDiffStatus(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "One", Status: model.StatusOpen},
		{ID: "2", Title: "Two", Status: model.StatusClosed},
	}
	m := NewModel(issues, nil, "")
	m.width = 120
	m.height = 20
	m.ready = true

	// Status message branch
	m.statusMsg = "All good"
	m.statusIsError = false
	out := m.renderFooter()
	if !strings.Contains(out, "All good") {
		t.Fatalf("status footer missing message: %s", out)
	}

	// Time-travel + update + workspace branch
	m.statusMsg = "" // disable status override
	m.timeTravelMode = true
	m.timeTravelSince = "HEAD~1"
	m.timeTravelDiff = &analysis.SnapshotDiff{
		Summary: analysis.DiffSummary{
			IssuesAdded:    2,
			IssuesClosed:   1,
			IssuesModified: 3,
		},
	}
	m.updateAvailable = true
	m.updateTag = "v9.9.9"
	m.workspaceMode = true
	m.workspaceSummary = "2 repos"
	m.countOpen = 1
	m.countReady = 1
	m.countBlocked = 0
	m.countClosed = 1

	out = m.renderFooter()
	for _, want := range []string{"⏱", "v9.9.9", "2 repos"} {
		if !strings.Contains(out, want) {
			t.Fatalf("footer missing %q in %q", want, out)
		}
	}

	// Diff status mapping
	m.newIssueIDs = map[string]bool{"n": true}
	m.closedIssueIDs = map[string]bool{"c": true}
	m.modifiedIssueIDs = map[string]bool{"m": true}
	m.timeTravelMode = true
	if got := m.getDiffStatus("n"); got != DiffStatusNew {
		t.Fatalf("new diff status mismatch: %v", got)
	}
	if got := m.getDiffStatus("c"); got != DiffStatusClosed {
		t.Fatalf("closed diff status mismatch: %v", got)
	}
	if got := m.getDiffStatus("m"); got != DiffStatusModified {
		t.Fatalf("modified diff status mismatch: %v", got)
	}
	if got := m.getDiffStatus("z"); got != DiffStatusNone {
		t.Fatalf("none diff status mismatch: %v", got)
	}
}

func TestGraphRenderBlocksAndDependents(t *testing.T) {
	issues := []model.Issue{
		{ID: "EGO", Title: "Center", Status: model.StatusOpen},
	}
	ins := analysis.Insights{Stats: analysis.NewGraphStatsForTest(
		nil, nil, nil, nil, nil, nil,
		map[string]int{"EGO": 0}, map[string]int{"EGO": 0},
		nil, 0, nil,
	)}
	g := NewGraphModel(issues, &ins, DefaultTheme(lipgloss.NewRenderer(nil)))

	blockers := []string{"B1", "B2", "B3", "B4", "B5", "B6"}
	dependents := []string{"D1", "D2", "D3"}
	blockOut := g.renderBlockersVisual(blockers, 80, g.theme)
	if !strings.Contains(blockOut, "+1 more") {
		t.Fatalf("blockers visual should include + more badge")
	}
	depOut := g.renderDependentsVisual(dependents, 80, g.theme)
	if !strings.Contains(depOut, "D1") || !strings.Contains(depOut, "D3") {
		t.Fatalf("dependents visual missing entries: %s", depOut)
	}
}

func TestViewVariantsCoverBranches(t *testing.T) {
	issues := []model.Issue{{ID: "1", Title: "One", Status: model.StatusOpen}}
	m := NewModel(issues, nil, "")
	m.ready = true
	m.width, m.height = 120, 30

	// Quit confirm
	m.showQuitConfirm = true
	_ = m.View()

	// Time-travel prompt
	m.showQuitConfirm = false
	m.showTimeTravelPrompt = true
	_ = m.View()

	// Recipe picker
	m.showTimeTravelPrompt = false
	m.showRecipePicker = true
	_ = m.View()

	// Help
	m.showRecipePicker = false
	m.showHelp = true
	_ = m.View()

	// Insights view
	m.showHelp = false
	m.focused = focusInsights
	_ = m.View()

	// Graph view
	m.focused = focusGraph
	m.isGraphView = true
	_ = m.View()

	// Board view
	m.isGraphView = false
	m.isBoardView = true
	_ = m.View()

	// Actionable view
	m.isBoardView = false
	m.isActionableView = true
	_ = m.View()

	// Split view
	m.isActionableView = false
	m.isSplitView = true
	_ = m.View()
}

func TestUpdateMouseAndResize(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "One", Status: model.StatusOpen},
		{ID: "2", Title: "Two", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")

	// Window size
	_, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})

	// Mouse wheel up/down in list focus
	m.focused = focusList
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})

	// Switch focus and scroll other components
	m.focused = focusDetail
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m.focused = focusInsights
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m.focused = focusBoard
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m.focused = focusGraph
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m.focused = focusActionable
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
}

func TestOverlaysAndWorkspaceHelpers(t *testing.T) {
	issues := []model.Issue{
		{ID: "W-1", Title: "Workspace", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")
	if updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30}); updated != nil {
		m = updated.(Model)
	}

	// Workspace state
	m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
	if !m.IsWorkspaceMode() {
		t.Fatalf("workspace mode should be enabled")
	}

	// Quit confirm overlay
	m.showQuitConfirm = true
	if !strings.Contains(m.View(), "Quit bv?") {
		t.Fatalf("quit overlay should render")
	}
	m.showQuitConfirm = false

	// Help overlay
	m.showHelp = true
	if !strings.Contains(m.View(), "Keyboard") {
		t.Fatalf("help overlay should render")
	}
	m.showHelp = false

	// Time-travel prompt render path (no git calls)
	m.showTimeTravelPrompt = true
	m.timeTravelInput.SetValue("HEAD~1")
	if out := m.renderTimeTravelPrompt(); !strings.Contains(out, "Time-Travel Mode") {
		t.Fatalf("time-travel prompt text missing")
	}
	m.showTimeTravelPrompt = false

	// Export filename helper (no filesystem writes)
	name := m.generateExportFilename()
	if !strings.HasPrefix(name, "beads_report_") || !strings.HasSuffix(name, ".md") {
		t.Fatalf("generateExportFilename unexpected: %s", name)
	}
}

func TestGraphIconsAndTruncation(t *testing.T) {
	if getTypeIcon(model.TypeBug) == "" || getPriorityIcon(1) == "" {
		t.Fatalf("graph icons should not be empty")
	}
	if got := smartTruncateID("very_long_identifier_with_parts", 8); len([]rune(got)) > 8 {
		t.Fatalf("smartTruncateID should respect max length, got %s", got)
	}
	if smartTruncateID("id", 0) != "" {
		t.Fatalf("smartTruncateID should return empty when maxLen<=0")
	}
}
