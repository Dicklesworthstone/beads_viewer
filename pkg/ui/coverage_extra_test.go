package ui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	// item.ComputeFilterValue() // Removed call to undefined method

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

func TestParseCommandLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{
			name:  "empty",
			input: "",
			want:  nil,
		},
		{
			name:  "simple",
			input: "code",
			want:  []string{"code"},
		},
		{
			name:  "args",
			input: "code --wait",
			want:  []string{"code", "--wait"},
		},
		{
			name:  "double_quoted_path",
			input: "\"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code\" --wait",
			want:  []string{"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code", "--wait"},
		},
		{
			name:  "single_quoted_arg",
			input: "open -a 'Visual Studio Code'",
			want:  []string{"open", "-a", "Visual Studio Code"},
		},
		{
			name:  "escaped_space",
			input: "open Visual\\ Studio",
			want:  []string{"open", "Visual Studio"},
		},
		{
			name:  "windows_path_in_quotes_preserves_backslashes",
			input: "\"C:\\Program Files\\VS Code\\Code.exe\" --wait",
			want:  []string{"C:\\Program Files\\VS Code\\Code.exe", "--wait"},
		},
		{
			name:    "unterminated_single_quote",
			input:   "open 'oops",
			wantErr: true,
		},
		{
			name:    "unterminated_double_quote",
			input:   "open \"oops",
			wantErr: true,
		},
		{
			name:    "trailing_escape",
			input:   "open \\",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommandLine(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (got=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCommandLine(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
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

	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if m.currentFilter != "open" {
		t.Fatalf("expected filter 'open', got %s", m.currentFilter)
	}
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.currentFilter != "closed" {
		t.Fatalf("expected filter 'closed', got %s", m.currentFilter)
	}
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.currentFilter != "ready" {
		t.Fatalf("expected filter 'ready', got %s", m.currentFilter)
	}

	// Paging up/down
	m.list.Select(0)
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.list.Index() == 0 {
		t.Fatalf("ctrl+d should move selection down")
	}
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.list.Index() != 0 {
		t.Fatalf("ctrl+u should move selection up")
	}

	// Enter should flip showDetails in mobile view
	m.showDetails = false
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.showDetails {
		t.Fatalf("enter should show details when not split view")
	}

	// Time-travel prompt toggling
	m.timeTravelMode = false
	var focusCmd tea.Cmd
	m, focusCmd = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.showTimeTravelPrompt || m.focused != focusTimeTravelInput {
		t.Fatalf("time-travel prompt not activated")
	}
	if focusCmd == nil {
		t.Fatal("time-travel prompt discarded its initial focus command")
	}
	// Cancel via Esc to avoid git dependency
	m, _ = m.handleTimeTravelInputKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTimeTravelPrompt {
		t.Fatalf("prompt should close on esc")
	}
	if m.focused != focusList {
		t.Fatalf("focus should return to list after esc")
	}
}

func TestClassifyEditorCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantBase string
		wantKind editorCommandKind
	}{
		{
			name:     "empty",
			args:     nil,
			wantBase: "",
			wantKind: editorCommandEmpty,
		},
		{
			name:     "terminal_editor",
			args:     []string{"VIM"},
			wantBase: "vim",
			wantKind: editorCommandTerminal,
		},
		{
			name:     "forbidden_shell_bash",
			args:     []string{"bash", "-lc", "echo hi"},
			wantBase: "bash",
			wantKind: editorCommandForbidden,
		},
		{
			name:     "forbidden_shell_pwsh_exe",
			args:     []string{"pwsh.exe", "-NoProfile"},
			wantBase: "pwsh",
			wantKind: editorCommandForbidden,
		},
		{
			name:     "gui_editor",
			args:     []string{"code", "--reuse-window"},
			wantBase: "code",
			wantKind: editorCommandOK,
		},
		{
			name:     "windows_path_gui_editor",
			args:     []string{`C:\Program Files\VS Code\Code.exe`, "--wait"},
			wantBase: "code",
			wantKind: editorCommandOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBase, gotKind := classifyEditorCommand(tt.args)
			if gotBase != tt.wantBase || gotKind != tt.wantKind {
				t.Fatalf("classifyEditorCommand(%v) = (%q, %v), want (%q, %v)", tt.args, gotBase, gotKind, tt.wantBase, tt.wantKind)
			}
		})
	}
}

func TestAllowlistedGUIEditorKindForBase(t *testing.T) {
	tests := []struct {
		base string
		want allowlistedGUIEditorKind
	}{
		{base: "code", want: allowlistedGUIEditorCode},
		{base: "code-insiders", want: allowlistedGUIEditorCodeInsiders},
		{base: "cursor", want: allowlistedGUIEditorCursor},
		{base: "xdg-open", want: allowlistedGUIEditorXdgOpen},
		{base: "notepad", want: allowlistedGUIEditorNotepad},
		{base: "open", want: allowlistedGUIEditorOpenText},
		{base: "unknown", want: allowlistedGUIEditorUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := allowlistedGUIEditorKindForBase(tt.base); got != tt.want {
				t.Fatalf("allowlistedGUIEditorKindForBase(%q)=%v, want %v", tt.base, got, tt.want)
			}
		})
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
	m = modelAny.(*Model)
	if !m.isGraphView || m.focused != focusGraph {
		t.Fatalf("graph view not activated")
	}

	// Board toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = modelAny.(*Model)
	if !m.isBoardView || m.focused != focusBoard {
		t.Fatalf("board view not activated")
	}

	// Insights toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = modelAny.(*Model)
	if m.focused != focusInsights {
		t.Fatalf("insights not focused after toggle")
	}

	// Actionable toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = modelAny.(*Model)
	if !m.isActionableView || m.focused != focusActionable {
		t.Fatalf("actionable view not activated")
	}

	// Priority hints toggle
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m = modelAny.(*Model)
	if !m.showPriorityHints {
		t.Fatalf("priority hints should toggle on with 'p'")
	}

	// Recipe picker toggle (' key)
	modelAny, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\''}})
	m = modelAny.(*Model)
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
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	// Navigate back to Open column (with items) - Status mode shows all columns (bv-tf6j)
	m.board.JumpToFirstColumn()
	// Enter should exit board when selection exists
	m.board.MoveToTop()
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyEnter})
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
	if !strings.Contains(footer, "✓") {
		t.Fatalf("footer should include success icon")
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

func TestRenderFooter_FreshnessIndicatorLevels(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.width = 140
	m.currentFilter = "all"

	m.backgroundWorker = &BackgroundWorker{}
	m.snapshot = &DataSnapshot{CreatedAt: time.Now()}

	// Fresh (<30s): no indicator
	out := m.renderFooter()
	if strings.Contains(out, "⚠") || strings.Contains(out, "STALE") || strings.Contains(out, "✗") {
		t.Fatalf("expected no freshness indicator when fresh, got: %q", out)
	}

	// Warn (>=30s)
	m.snapshot.CreatedAt = time.Now().Add(-45 * time.Second)
	out = m.renderFooter()
	if !strings.Contains(out, "⚠") || strings.Contains(out, "STALE") {
		t.Fatalf("expected warning freshness indicator, got: %q", out)
	}

	// Stale (>=2m)
	m.snapshot.CreatedAt = time.Now().Add(-3 * time.Minute)
	out = m.renderFooter()
	if !strings.Contains(out, "STALE") {
		t.Fatalf("expected stale freshness indicator, got: %q", out)
	}

	// Error (>=3 consecutive errors)
	m.backgroundWorker.lastError = &WorkerError{
		Phase:   "load",
		Time:    time.Now().Add(-5 * time.Second),
		Retries: 3,
	}
	out = m.renderFooter()
	if !strings.Contains(out, "✗") || !strings.Contains(out, "3x") {
		t.Fatalf("expected error freshness indicator, got: %q", out)
	}
}

func TestView_LoadingScreen_TransitionsOnFirstSnapshotOrError(t *testing.T) {
	issues := []model.Issue{{
		ID:        "L-1",
		Title:     "Loading Test",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		CreatedAt: time.Now(),
	}}

	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 30
	m.backgroundWorker = &BackgroundWorker{state: WorkerProcessing}
	m.snapshot = nil
	m.snapshotInitPending = true

	if out := m.View(); !strings.Contains(out, "Loading beads") {
		t.Fatalf("expected loading screen before first snapshot, got: %q", out)
	}

	// Error should exit the loading screen (we already have initial data).
	modelAny, _ := m.Update(SnapshotErrorMsg{Err: errors.New("boom"), Recoverable: true})
	mErr := modelAny.(*Model)
	if out := mErr.View(); strings.Contains(out, "Loading beads") {
		t.Fatalf("expected loading screen to clear on error, got: %q", out)
	}

	// Snapshot should exit the loading screen.
	m.snapshotInitPending = true
	snap := NewSnapshotBuilder(issues).Build()
	modelAny, _ = m.Update(SnapshotReadyMsg{Snapshot: snap})
	mOK := modelAny.(*Model)
	if out := mOK.View(); strings.Contains(out, "Loading beads") {
		t.Fatalf("expected loading screen to clear on first snapshot, got: %q", out)
	}
}

func TestRenderFooter_ShowsPhase2ProgressBadge(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.width = 80
	m.snapshot = &DataSnapshot{phase2Ready: false}

	out := m.renderFooter()
	if !strings.Contains(out, "metrics") {
		t.Fatalf("expected phase 2 progress badge, got: %q", out)
	}
}

func TestRenderFooter_ShowsWorkerHealthIndicators(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.width = 140
	m.currentFilter = "all"
	m.snapshot = &DataSnapshot{CreatedAt: time.Now()}

	m.backgroundWorker = &BackgroundWorker{
		started:          true,
		heartbeatTimeout: time.Second,
		lastHeartbeat:    time.Now().Add(-2 * time.Second),
	}
	out := m.renderFooter()
	if !strings.Contains(out, "unresponsive") {
		t.Fatalf("expected worker unresponsive indicator, got: %q", out)
	}

	m.backgroundWorker = &BackgroundWorker{
		started:          true,
		heartbeatTimeout: time.Second,
		lastHeartbeat:    time.Now(),
		recoveryCount:    2,
	}
	out = m.renderFooter()
	if !strings.Contains(out, "recovered x2") {
		t.Fatalf("expected worker recovered indicator, got: %q", out)
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
	_ = m.copyIssueToClipboard()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No issue selected") {
		t.Fatalf("expected error status for missing selection")
	}
}

func TestOpenInEditorTerminalEditorGuard(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	_ = os.MkdirAll(filepath.Join(tmp, ".beads"), 0755)
	_ = os.WriteFile(filepath.Join(tmp, ".beads", "beads.jsonl"), []byte("{}"), 0644)
	_ = os.Chdir(tmp)

	origEditor := os.Getenv("EDITOR")
	defer os.Setenv("EDITOR", origEditor)
	_ = os.Setenv("EDITOR", "vim") // terminal editor — now dispatches via TUI suspend (bv-134)

	// With no issues selected, terminal editor dispatch returns "No issue selected"
	m := NewModel(nil, nil, "")
	m.openInEditor()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No issue selected") {
		t.Fatalf("expected 'No issue selected' for terminal editor with no selection, got %q", m.statusMsg)
	}
}

func TestOpenInEditorWithArguments(t *testing.T) {
	// Test that EDITOR with arguments (e.g., "cursor -w") works correctly
	// This tests the fix for GitHub issue #47
	if runtime.GOOS == "windows" {
		t.Skip("shell execution test unreliable on Windows CI")
	}
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	_ = os.MkdirAll(filepath.Join(tmp, ".beads"), 0755)
	_ = os.WriteFile(filepath.Join(tmp, ".beads", "beads.jsonl"), []byte("{}"), 0644)
	_ = os.Chdir(tmp)

	origEditor := os.Getenv("EDITOR")
	defer os.Setenv("EDITOR", origEditor)

	// Test with EDITOR containing arguments - "true" is a POSIX command that just exits 0
	// Using "true --" simulates EDITOR with arguments like "cursor -w"
	_ = os.Setenv("EDITOR", "true --")

	m := NewModel(nil, nil, "")
	m.openInEditor()
	// Should succeed - the shell should parse "true --" correctly
	if m.statusIsError {
		t.Fatalf("expected success with EDITOR containing arguments, got error: %q", m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, "Opened in") {
		t.Fatalf("expected 'Opened in' message, got %q", m.statusMsg)
	}

	// Also test terminal editor detection with arguments (e.g., "vim -u NONE")
	// Now dispatches via TUI suspend (bv-134); with no issue selected, returns error
	_ = os.Setenv("EDITOR", "vim -u NONE")
	m2 := NewModel(nil, nil, "")
	m2.openInEditor()
	if !m2.statusIsError || !strings.Contains(m2.statusMsg, "No issue selected") {
		t.Fatalf("expected 'No issue selected' for 'vim -u NONE', got %q", m2.statusMsg)
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
	if !strings.Contains(out, "✗") {
		t.Fatalf("footer should show error icon")
	}
}

func TestRenderFooter_CombinedIndicators(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "beads.jsonl")
	if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w, err := watcher.NewWatcher(
		f,
		watcher.WithForcePoll(true),
		watcher.WithPollInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("watcher start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	m := NewModel(nil, nil, "")
	m.width = 160
	m.currentFilter = "ready"
	m.countOpen, m.countReady, m.countBlocked, m.countClosed = 1, 2, 3, 4
	m.updateAvailable = true
	m.updateTag = "v9.9.9"
	m.snapshot = &DataSnapshot{CreatedAt: time.Now(), phase2Ready: false}
	m.backgroundWorker = &BackgroundWorker{
		started:          true,
		state:            WorkerIdle,
		lastHeartbeat:    time.Now(),
		heartbeatTimeout: 5 * time.Second,
		recoveryCount:    2,
		watcher:          w,
	}

	out := m.renderFooter()
	for _, expect := range []string{"READY", "metrics", "recovered x2", "polling", "⭐"} {
		if !strings.Contains(out, expect) {
			t.Fatalf("footer missing %q: %q", expect, out)
		}
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
	m = modelAny.(*Model)
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
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyCtrlU})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyHome})
	m, _ = m.handleBoardKeys(tea.KeyMsg{Type: tea.KeyEnd})

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
	m, _ = m.handleTimeTravelInputKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.statusIsError && m.statusMsg == "" {
		t.Fatalf("expected status message after attempting time-travel without git")
	}
}

func TestOpenInEditorMissingAndGUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("openInEditor GUI path is unreliable on headless Windows CI")
	}
	// Missing beads file branch
	tmp := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	_ = os.Chdir(tmp)

	m := NewModel([]model.Issue{{ID: "1", Title: "x", Status: model.StatusOpen}}, nil, "")
	m.openInEditor()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No .beads") {
		t.Fatalf("expected missing beads error, got %q", m.statusMsg)
	}

	// Success branch with GUI-ish editor
	beadsDir := filepath.Join(tmp, ".beads")
	_ = os.Mkdir(beadsDir, 0o755)
	_ = os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{}`), 0o644)

	origEditor := os.Getenv("EDITOR")
	t.Cleanup(func() { _ = os.Setenv("EDITOR", origEditor) })
	_ = os.Setenv("EDITOR", "true") // present on POSIX; not in terminal editor block

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

func TestWatchFileCmdReturnsWhenWatcherStops(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	w, err := watcher.NewWatcher(file, watcher.WithForcePoll(true))
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}

	result := make(chan tea.Msg, 1)
	go func() {
		result <- WatchFileCmd(w)()
	}()
	w.Stop()

	select {
	case msg := <-result:
		if msg != nil {
			t.Fatalf("stopped watcher returned unexpected message %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("WatchFileCmd remained blocked after watcher stop")
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
		m = updated.(*Model)
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

func TestHelpOverlayScroll(t *testing.T) {
	issues := []model.Issue{{ID: "1", Title: "One", Status: model.StatusOpen}}
	m := NewModel(issues, nil, "")
	m.width, m.height = 80, 20 // Small terminal to force scroll
	m.showHelp = true
	m.focused = focusHelp
	m.helpScroll = 0

	// Test scroll down
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.helpScroll != 1 {
		t.Fatalf("expected helpScroll=1 after j, got %d", m.helpScroll)
	}

	// Test scroll up
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 after k, got %d", m.helpScroll)
	}

	// Test scroll up at top (should stay at 0)
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 at top, got %d", m.helpScroll)
	}

	// Test page down
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.helpScroll != 10 {
		t.Fatalf("expected helpScroll=10 after ctrl+d, got %d", m.helpScroll)
	}

	// Test page up
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 after ctrl+u, got %d", m.helpScroll)
	}

	// Test home
	m.helpScroll = 5
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 after g, got %d", m.helpScroll)
	}

	// Test end
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.helpScroll < 10 {
		t.Fatalf("expected helpScroll>10 after G, got %d", m.helpScroll)
	}

	// Test q closes help
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.showHelp {
		t.Fatalf("expected showHelp=false after q")
	}
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 after closing, got %d", m.helpScroll)
	}

	// Test any other key closes help
	m.showHelp = true
	m.focused = focusHelp
	m.helpScroll = 5
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.showHelp {
		t.Fatalf("expected showHelp=false after x")
	}

	// Test render help overlay
	m.showHelp = true
	m.focused = focusHelp
	m.helpScroll = 0
	out := m.renderHelpOverlay()
	if !strings.Contains(out, "Keyboard Shortcuts") {
		t.Fatalf("help overlay should render shortcuts")
	}
	// Should show close hint
	if !strings.Contains(out, "close") && !strings.Contains(out, "Esc") {
		t.Fatalf("help overlay should show close hint")
	}
	// Should show tutorial hint (bv-0trk)
	if !strings.Contains(out, "Tutorial") {
		t.Fatalf("help overlay should show Tutorial hint")
	}

	// Test Space key closes help for tutorial entry (bv-0trk)
	m.showHelp = true
	m.focused = focusHelp
	m.helpScroll = 5
	m = m.handleHelpKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if m.showHelp {
		t.Fatalf("expected showHelp=false after Space")
	}
	if m.helpScroll != 0 {
		t.Fatalf("expected helpScroll=0 after Space, got %d", m.helpScroll)
	}
}

// Tests for terminal editor dispatch (bv-134)

func TestExportIssueFrontmatter(t *testing.T) {
	issue := model.Issue{
		ID:          "bv-134",
		Title:       "Smart editor dispatch",
		Priority:    2,
		Status:      model.StatusOpen,
		Assignee:    "alice",
		IssueType:   model.TypeFeature,
		Labels:      []string{"ui", "editor"},
		Description: "Implement terminal editor support.",
	}

	content := exportIssueFrontmatter(issue)

	// Verify frontmatter structure
	if !strings.HasPrefix(content, "---\n") {
		t.Fatal("expected frontmatter to start with ---")
	}
	if !strings.Contains(content, "title: Smart editor dispatch\n") {
		t.Fatalf("missing title in frontmatter: %s", content)
	}
	if !strings.Contains(content, "priority: 2\n") {
		t.Fatalf("missing priority in frontmatter: %s", content)
	}
	if !strings.Contains(content, "status: open\n") {
		t.Fatalf("missing status in frontmatter: %s", content)
	}
	if !strings.Contains(content, "assignee: alice\n") {
		t.Fatalf("missing assignee in frontmatter: %s", content)
	}
	if !strings.Contains(content, "type: feature\n") {
		t.Fatalf("missing type in frontmatter: %s", content)
	}
	if !strings.Contains(content, "# labels (read-only): [ui, editor]\n") {
		t.Fatalf("missing labels comment in frontmatter: %s", content)
	}
	if !strings.Contains(content, "Implement terminal editor support.") {
		t.Fatalf("missing description body: %s", content)
	}
}

func TestExportIssueFrontmatterEmptyAssignee(t *testing.T) {
	issue := model.Issue{
		ID:        "bv-135",
		Title:     "No assignee",
		Priority:  1,
		Status:    model.StatusOpen,
		IssueType: model.TypeBug,
	}

	content := exportIssueFrontmatter(issue)
	if !strings.Contains(content, "assignee:\n") {
		t.Fatalf("expected empty assignee line, got: %s", content)
	}
}

func TestExportIssueFrontmatterSpecialChars(t *testing.T) {
	issue := model.Issue{
		ID:        "bv-136",
		Title:     `Fix: "colon" and #hash problems`,
		Priority:  3,
		Status:    model.StatusInProgress,
		IssueType: model.TypeBug,
	}

	content := exportIssueFrontmatter(issue)
	// Title with special chars should be quoted
	if !strings.Contains(content, `title: "Fix`) {
		t.Fatalf("expected quoted title for special chars, got: %s", content)
	}
}

func TestParseIssueFrontmatter(t *testing.T) {
	content := `---
title: Updated title
priority: 3
status: in_progress
assignee: bob
type: bug
---

New description here.
`

	fields := parseIssueFrontmatter(content)
	if fields["title"] != "Updated title" {
		t.Fatalf("expected title 'Updated title', got %q", fields["title"])
	}
	if fields["priority"] != "3" {
		t.Fatalf("expected priority '3', got %q", fields["priority"])
	}
	if fields["status"] != "in_progress" {
		t.Fatalf("expected status 'in_progress', got %q", fields["status"])
	}
	if fields["assignee"] != "bob" {
		t.Fatalf("expected assignee 'bob', got %q", fields["assignee"])
	}
	if fields["type"] != "bug" {
		t.Fatalf("expected type 'bug', got %q", fields["type"])
	}
}

func TestParseIssueFrontmatterNoFrontmatter(t *testing.T) {
	content := "Just plain text without frontmatter."
	fields := parseIssueFrontmatter(content)
	if len(fields) != 0 {
		t.Fatalf("expected no fields, got %v", fields)
	}
}

func TestParseIssueFrontmatterQuotedValues(t *testing.T) {
	content := `---
title: "Title with: colon"
assignee: "someone"
---
`

	fields := parseIssueFrontmatter(content)
	if fields["title"] != "Title with: colon" {
		t.Fatalf("expected unquoted title, got %q", fields["title"])
	}
}

func TestParseBodyFromFrontmatter(t *testing.T) {
	content := `---
title: test
---

This is the body.
With multiple lines.
`
	body := parseBodyFromFrontmatter(content)
	if !strings.Contains(body, "This is the body.") {
		t.Fatalf("expected body content, got %q", body)
	}
	if !strings.Contains(body, "With multiple lines.") {
		t.Fatalf("expected multiline body, got %q", body)
	}
}

func TestParseBodyFromFrontmatterEmpty(t *testing.T) {
	content := `---
title: test
---
`
	body := parseBodyFromFrontmatter(content)
	if body != "" {
		t.Fatalf("expected empty body, got %q", body)
	}
}

func TestYamlEscapeString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"", `""`},
		{"has: colon", `"has: colon"`},
		{`has "quotes"`, `"has \"quotes\""`},
		{"has\nnewline", `"has\nnewline"`},
		{" leading space", `" leading space"`},
	}
	for _, tt := range tests {
		got := yamlEscapeString(tt.input)
		if got != tt.want {
			t.Errorf("yamlEscapeString(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFrontmatterRoundTrip(t *testing.T) {
	issue := model.Issue{
		ID:          "bv-rt",
		Title:       "Round trip test",
		Priority:    2,
		Status:      model.StatusOpen,
		Assignee:    "charlie",
		IssueType:   model.TypeTask,
		Description: "Original description.",
	}

	content := exportIssueFrontmatter(issue)
	fields := parseIssueFrontmatter(content)
	body := parseBodyFromFrontmatter(content)

	if fields["title"] != issue.Title {
		t.Errorf("title roundtrip: got %q, want %q", fields["title"], issue.Title)
	}
	if fields["priority"] != "2" {
		t.Errorf("priority roundtrip: got %q, want %q", fields["priority"], "2")
	}
	if fields["status"] != string(issue.Status) {
		t.Errorf("status roundtrip: got %q, want %q", fields["status"], issue.Status)
	}
	if fields["assignee"] != issue.Assignee {
		t.Errorf("assignee roundtrip: got %q, want %q", fields["assignee"], issue.Assignee)
	}
	if fields["type"] != string(issue.IssueType) {
		t.Errorf("type roundtrip: got %q, want %q", fields["type"], issue.IssueType)
	}
	if body != issue.Description {
		t.Errorf("body roundtrip: got %q, want %q", body, issue.Description)
	}
}

func TestTerminalEditorClassification(t *testing.T) {
	// Verify all new terminal editors are correctly classified (bv-134)
	newEditors := []string{"helix", "hx", "micro", "kakoune", "kak", "jed", "mg", "mcedit"}
	for _, editor := range newEditors {
		_, kind := classifyEditorCommand([]string{editor})
		if kind != editorCommandTerminal {
			t.Errorf("classifyEditorCommand(%q) = %v, want editorCommandTerminal", editor, kind)
		}
	}
}

func TestLaunchTerminalEditorNoSelection(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	_ = os.MkdirAll(filepath.Join(tmp, ".beads"), 0755)
	_ = os.WriteFile(filepath.Join(tmp, ".beads", "beads.jsonl"), []byte("{}"), 0644)
	_ = os.Chdir(tmp)

	origEditor := os.Getenv("EDITOR")
	defer os.Setenv("EDITOR", origEditor)
	_ = os.Setenv("EDITOR", "helix")

	m := NewModel(nil, nil, "")
	cmd := m.openInEditor()
	if cmd != nil {
		t.Fatal("expected nil cmd when no issue is selected")
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "No issue selected") {
		t.Fatalf("expected 'No issue selected' error, got %q", m.statusMsg)
	}
}

func TestEditorExitMsgNoChanges(t *testing.T) {
	original := `---
title: Test
priority: 1
status: open
assignee:
type: task
---

Description.
`
	// Write temp file with identical content
	tmpFile, err := os.CreateTemp("", "bv-test-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString(original)
	tmpFile.Close()

	m := NewModel([]model.Issue{{ID: "t1", Title: "Test", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
	msg := editorExitMsg{
		issueID:  "t1",
		tmpFile:  tmpFile.Name(),
		original: original,
		err:      nil,
	}

	result, _ := m.Update(msg)
	resultModel := result.(*Model)
	if resultModel.statusIsError {
		t.Fatalf("expected no error, got %q", resultModel.statusMsg)
	}
	if !strings.Contains(resultModel.statusMsg, "No changes") {
		t.Fatalf("expected 'No changes' message, got %q", resultModel.statusMsg)
	}
}

func TestEditorExitMsgRunsBRUpdateAsynchronously(t *testing.T) {
	original := `---
title: Test
priority: 1
status: open
assignee:
type: task
---

Description.
`
	edited := strings.Replace(original, "title: Test", "title: Updated", 1)
	tmpFile, err := os.CreateTemp("", "bv-test-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(edited); err != nil {
		t.Fatal(err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatal(err)
	}

	m := NewModel([]model.Issue{{
		ID:        "t1",
		Title:     "Test",
		Priority:  1,
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
	}}, nil, "")
	result, cmd := m.Update(editorExitMsg{
		issueID:  "t1",
		tmpFile:  tmpFile.Name(),
		original: original,
	})
	if cmd == nil {
		t.Fatal("changed editor content should schedule br update asynchronously")
	}
	resultModel := result.(*Model)
	if resultModel.statusIsError || !strings.Contains(resultModel.statusMsg, "Updating 1 field") {
		t.Fatalf("expected in-progress update status, got %q", resultModel.statusMsg)
	}
}

func TestEditorExitMsgWithError(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "bv-test-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	m := NewModel(nil, nil, "")
	msg := editorExitMsg{
		issueID:  "t1",
		tmpFile:  tmpFile.Name(),
		original: "original",
		err:      errors.New("editor crashed"),
	}

	result, _ := m.Update(msg)
	resultModel := result.(*Model)
	if !resultModel.statusIsError {
		t.Fatal("expected error status")
	}
	if !strings.Contains(resultModel.statusMsg, "Editor exited with error") {
		t.Fatalf("expected editor error message, got %q", resultModel.statusMsg)
	}
}

func TestExtractAssigneeCounts(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Assignee: "alice"},
		{ID: "2", Assignee: "bob"},
		{ID: "3", Assignee: "alice"},
		{ID: "4", Assignee: ""},
	}

	names, counts := extractAssigneeCounts(issues)

	if len(names) != 2 {
		t.Fatalf("expected 2 distinct assignees, got %d (%v)", len(names), names)
	}
	if counts["alice"] != 2 {
		t.Errorf("expected alice count 2, got %d", counts["alice"])
	}
	if counts["bob"] != 1 {
		t.Errorf("expected bob count 1, got %d", counts["bob"])
	}
	if _, ok := counts[""]; ok {
		t.Error("expected unassigned issues to be excluded from counts")
	}
}

func TestExtractIssueTypeCounts(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", IssueType: model.TypeBug},
		{ID: "2", IssueType: model.TypeFeature},
		{ID: "3", IssueType: model.TypeBug},
		{ID: "4", IssueType: "molecule"}, // custom Gastown-style type
	}

	names, counts := extractIssueTypeCounts(issues)

	if len(names) != 3 {
		t.Fatalf("expected 3 distinct types, got %d (%v)", len(names), names)
	}
	if counts["bug"] != 2 {
		t.Errorf("expected bug count 2, got %d", counts["bug"])
	}
	if counts["molecule"] != 1 {
		t.Errorf("expected custom type 'molecule' to be counted, got %d", counts["molecule"])
	}
}
