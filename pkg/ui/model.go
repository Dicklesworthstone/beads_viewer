package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/debug"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/instance"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"
	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// View width thresholds for adaptive layout
const (
	SplitViewThreshold     = 100
	WideViewThreshold      = 140
	UltraWideViewThreshold = 180
)

// focus represents which UI element has keyboard focus
type focus int

const (
	focusList focus = iota
	focusDetail
	focusBoard
	focusGraph
	focusTree // Hierarchical tree view (bv-gllx)
	focusLabelDashboard
	focusInsights
	focusActionable
	focusRecipePicker
	focusRepoPicker
	focusHelp
	focusQuitConfirm
	focusTimeTravelInput
	focusHistory
	focusAttention
	focusLabelPicker
	focusSprint      // Sprint dashboard view (bv-161)
	focusAgentPrompt // AGENTS.md integration prompt (bv-i8dk)
	focusFlowMatrix  // Cross-label flow matrix view
	focusTutorial    // Interactive tutorial (bv-8y31)
	focusCassModal   // Cass session preview modal (bv-5bqh)
	focusUpdateModal // Self-update modal (bv-182)
)

// SortMode represents the current list sorting mode (bv-3ita)
type SortMode int

const (
	SortDefault     SortMode = iota // Priority asc, then created desc (original default)
	SortCreatedAsc                  // By creation date, oldest first
	SortCreatedDesc                 // By creation date, newest first
	SortPriority                    // By priority only (ascending)
	SortUpdated                     // By last update, newest first
	numSortModes                    // Keep this last - used for cycling
)

// String returns a human-readable label for the sort mode
func (s SortMode) String() string {
	switch s {
	case SortCreatedAsc:
		return "Created ↑"
	case SortCreatedDesc:
		return "Created ↓"
	case SortPriority:
		return "Priority"
	case SortUpdated:
		return "Updated"
	default:
		return "Default"
	}
}

// LabelGraphAnalysisResult holds label-specific graph analysis results (bv-109)
type LabelGraphAnalysisResult struct {
	Label        string
	Subgraph     analysis.LabelSubgraph
	PageRank     analysis.LabelPageRankResult
	CriticalPath analysis.LabelCriticalPathResult
}

// UpdateMsg is sent when a new version is available
type UpdateMsg struct {
	TagName string
	URL     string
}

// Phase2ReadyMsg is sent when async graph analysis Phase 2 completes
type Phase2ReadyMsg struct {
	Stats    *analysis.GraphStats // The stats that completed, to detect stale messages
	Insights analysis.Insights    // Precomputed insights for Phase 2 metrics
}

// WaitForPhase2Cmd returns a command that waits for Phase 2 and sends Phase2ReadyMsg
func WaitForPhase2Cmd(stats *analysis.GraphStats) tea.Cmd {
	return func() tea.Msg {
		if stats == nil {
			return Phase2ReadyMsg{}
		}
		stats.WaitForPhase2()
		ins := stats.GenerateInsights(stats.NodeCount)
		return Phase2ReadyMsg{Stats: stats, Insights: ins}
	}
}

// FileChangedMsg is sent when the beads file changes on disk
type FileChangedMsg struct{}

// semanticDebounceTickMsg is sent after debounce delay to trigger semantic computation
type semanticDebounceTickMsg struct{}

// workerPollTickMsg drives a small background-mode status refresh (spinner + freshness) (bv-9nfy).
type workerPollTickMsg struct{}

var workerSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	freshnessErrorRetries = 3
)

func freshnessWarnThreshold() time.Duration {
	return envDurationSeconds("BV_FRESHNESS_WARN_S", 30*time.Second)
}

func freshnessStaleThreshold() time.Duration {
	return envDurationSeconds("BV_FRESHNESS_STALE_S", 2*time.Minute)
}

func workerPollTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
		return workerPollTickMsg{}
	})
}

// ReadyTimeoutMsg is sent after a short delay to ensure the UI becomes ready
// even if the terminal doesn't send WindowSizeMsg promptly (bv-7wl7)
type ReadyTimeoutMsg struct{}

// ReadyTimeoutCmd returns a command that sends ReadyTimeoutMsg after 100ms.
// This ensures the TUI doesn't hang on "Initializing..." if the terminal
// is slow to report its size (common in tmux, SSH, some terminal emulators).
func ReadyTimeoutCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return ReadyTimeoutMsg{}
	})
}

// WatchFileCmd returns a command that waits for file changes and sends FileChangedMsg
func WatchFileCmd(w *watcher.Watcher) tea.Cmd {
	return func() tea.Msg {
		<-w.Changed()
		return FileChangedMsg{}
	}
}

// StartBackgroundWorkerCmd starts the background worker and triggers an initial refresh.
func StartBackgroundWorkerCmd(w *BackgroundWorker) tea.Cmd {
	return func() tea.Msg {
		if w == nil {
			return nil
		}
		if err := w.Start(); err != nil {
			return SnapshotErrorMsg{Err: fmt.Errorf("starting background worker: %w", err), Recoverable: false}
		}
		w.TriggerRefresh()
		return nil
	}
}

// WaitForBackgroundWorkerMsgCmd waits for the next BackgroundWorker message.
func WaitForBackgroundWorkerMsgCmd(w *BackgroundWorker) tea.Cmd {
	return func() tea.Msg {
		if w == nil {
			return nil
		}
		select {
		case msg := <-w.Messages():
			return msg
		case <-w.Done():
			return nil
		}
	}
}

// CheckUpdateCmd returns a command that checks for updates
func CheckUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		tag, url, err := updater.CheckForUpdates()
		if err == nil && tag != "" {
			return UpdateMsg{TagName: tag, URL: url}
		}
		return nil
	}
}

// HistoryLoadedMsg is sent when background history loading completes
type HistoryLoadedMsg struct {
	Report *correlation.HistoryReport
	Error  error
}

// AgentFileCheckMsg is sent after checking for AGENTS.md integration (bv-i8dk)
type AgentFileCheckMsg struct {
	ShouldPrompt bool
	FilePath     string
	FileType     string
}

// CheckAgentFileCmd returns a command that checks if we should prompt for AGENTS.md
func CheckAgentFileCmd(workDir string) tea.Cmd {
	return func() tea.Msg {
		if workDir == "" {
			return AgentFileCheckMsg{ShouldPrompt: false}
		}

		// Check if we should prompt based on preferences
		if !agents.ShouldPromptForAgentFile(workDir) {
			return AgentFileCheckMsg{ShouldPrompt: false}
		}

		// Detect agent file
		detection := agents.DetectAgentFile(workDir)

		// Only prompt if file exists but doesn't have our blurb
		if detection.Found() && detection.NeedsBlurb() {
			return AgentFileCheckMsg{
				ShouldPrompt: true,
				FilePath:     detection.FilePath,
				FileType:     detection.FileType,
			}
		}

		return AgentFileCheckMsg{ShouldPrompt: false}
	}
}

// LoadHistoryCmd returns a command that loads history data in the background
func LoadHistoryCmd(issues []model.Issue, beadsPath string) tea.Cmd {
	return func() tea.Msg {
		var repoPath string
		var err error

		if beadsPath != "" {
			// If beadsPath is provided (single-repo mode), derive repo root from it.
			// Try to resolve absolute path first.
			if absPath, e := filepath.Abs(beadsPath); e == nil {
				dir := filepath.Dir(absPath)
				// Standard layout: <repo_root>/.beads/<file.jsonl>
				if filepath.Base(dir) == ".beads" {
					repoPath = filepath.Dir(dir)
				} else {
					// Legacy/Flat layout: <repo_root>/<file.jsonl>
					repoPath = dir
				}
			}
		}

		// Fallback to CWD if beadsPath is empty (workspace mode) or Abs failed
		if repoPath == "" {
			repoPath, err = os.Getwd()
			if err != nil {
				return HistoryLoadedMsg{Error: err}
			}
		}

		// Convert model.Issue to correlation.BeadInfo
		beads := make([]correlation.BeadInfo, len(issues))
		for i, issue := range issues {
			beads[i] = correlation.BeadInfo{
				ID:     issue.ID,
				Title:  issue.Title,
				Status: string(issue.Status),
			}
		}

		correlator := correlation.NewCorrelator(repoPath, beadsPath)
		opts := correlation.CorrelatorOptions{
			Limit: 500, // Reasonable limit for TUI performance
		}

		report, err := correlator.GenerateReport(beads, opts)
		return HistoryLoadedMsg{Report: report, Error: err}
	}
}

func cloneIssuesForAsync(issues []model.Issue) []model.Issue {
	if len(issues) == 0 {
		return nil
	}
	clones := make([]model.Issue, len(issues))
	for i := range issues {
		clones[i] = issues[i].Clone()
	}
	return clones
}

// Model is the main Bubble Tea model for the beads viewer
type Model struct {
	// Data
	issues       []model.Issue
	pooledIssues []*model.Issue // Issue pool refs for sync reloads (return to pool on replace)
	issueMap     map[string]*model.Issue
	analyzer     *analysis.Analyzer
	analysis     *analysis.GraphStats
	beadsPath    string           // Path to beads.jsonl for reloading
	watcher      *watcher.Watcher // File watcher for live reload
	instanceLock *instance.Lock   // Multi-instance coordination lock

	// Background Worker (Phase 2 architecture - bv-m7v8)
	// snapshot is the current immutable data snapshot from BackgroundWorker.
	// Access is safe without locks because Bubble Tea ensures Update() and View()
	// don't run concurrently. When nil, the UI uses legacy m.issues/m.issueMap fields.
	snapshot *DataSnapshot
	// snapshotInitPending is true until we receive the first BackgroundWorker snapshot
	// (or an error), allowing a polished cold-start loading screen (bv-tspo).
	snapshotInitPending bool
	// backgroundWorker manages async data loading (nil if background mode disabled)
	backgroundWorker *BackgroundWorker
	workerSpinnerIdx int // Spinner frame for background worker activity (bv-9nfy)
	lastForceRefresh time.Time

	// UI Components
	list               list.Model
	viewport           viewport.Model
	renderer           *MarkdownRenderer
	board              BoardModel
	labelDashboard     LabelDashboardModel
	velocityComparison VelocityComparisonModel // bv-125
	shortcutsSidebar   ShortcutsSidebar        // bv-3qi5
	graphView          GraphModel
	tree               TreeModel // Hierarchical tree view (bv-gllx)
	insightsPanel      InsightsModel
	flowMatrix         FlowMatrixModel // Cross-label flow matrix
	theme              Theme

	// Update State
	updateAvailable bool
	updateTag       string
	updateURL       string

	// Focus and View State
	focused                  focus
	focusBeforeHelp          focus // Stores focus before opening help overlay
	isSplitView              bool
	splitPaneRatio           float64 // Ratio of list pane width (0.2-0.8), default 0.4
	isBoardView              bool
	isGraphView              bool
	isActionableView         bool
	isHistoryView            bool
	showDetails              bool
	showHelp                 bool
	helpScroll               int // Scroll offset for help overlay
	showQuitConfirm          bool
	ready                    bool
	width                    int
	height                   int
	showLabelHealthDetail    bool
	showLabelDrilldown       bool
	labelHealthDetail        *analysis.LabelHealth
	labelHealthDetailFlow    labelFlowSummary
	labelDrilldownLabel      string
	labelDrilldownIssues     []model.Issue
	labelDrilldownCache      map[string][]model.Issue
	showLabelGraphAnalysis   bool
	labelGraphAnalysisResult *LabelGraphAnalysisResult
	showAttentionView        bool
	showShortcutsSidebar     bool // bv-3qi5 toggleable shortcuts sidebar
	labelHealthCached        bool
	labelHealthCache         analysis.LabelAnalysisResult
	attentionCached          bool
	attentionCache           analysis.LabelAttentionResult

	// Actionable view
	actionableView ActionableModel

	// History view
	historyView       HistoryModel
	historyLoading    bool // True while history is being loaded in background
	historyLoadFailed bool // True if history loading failed

	// Filter and sort state
	currentFilter          string
	sortMode               SortMode // bv-3ita: current sort mode
	semanticSearchEnabled  bool
	semanticIndexBuilding  bool
	semanticSearch         *SemanticSearch
	semanticHybridEnabled  bool
	semanticHybridPreset   search.PresetName
	semanticHybridBuilding bool
	semanticHybridReady    bool
	lastSearchTerm         string

	// Stats (cached)
	countOpen    int
	countReady   int
	countBlocked int
	countClosed  int

	// Priority hints
	showPriorityHints bool
	priorityHints     map[string]*analysis.PriorityRecommendation // issueID -> recommendation

	// Triage insights (bv-151)
	triageScores  map[string]float64                // issueID -> triage score
	triageReasons map[string]analysis.TriageReasons // issueID -> reasons
	unblocksMap   map[string][]string               // issueID -> IDs that would be unblocked
	quickWinSet   map[string]bool                   // issueID -> true if quick win
	blockerSet    map[string]bool                   // issueID -> true if significant blocker

	// Recipe picker
	showRecipePicker bool
	recipePicker     RecipePickerModel
	activeRecipe     *recipe.Recipe
	recipeLoader     *recipe.Loader

	// Label picker (bv-126)
	showLabelPicker bool
	labelPicker     LabelPickerModel

	// Repo picker (workspace mode)
	showRepoPicker bool
	repoPicker     RepoPickerModel

	// Time-travel mode
	timeTravelMode   bool
	timeTravelDiff   *analysis.SnapshotDiff
	timeTravelSince  string
	newIssueIDs      map[string]bool // Issues in diff.NewIssues
	closedIssueIDs   map[string]bool // Issues in diff.ClosedIssues
	modifiedIssueIDs map[string]bool // Issues in diff.ModifiedIssues

	// Time-travel input prompt
	timeTravelInput      textinput.Model
	showTimeTravelPrompt bool

	// Status message (for temporary feedback)
	statusMsg     string
	statusIsError bool

	// Workspace mode state
	workspaceMode    bool            // True when viewing multiple repos
	availableRepos   []string        // List of repo prefixes available
	activeRepos      map[string]bool // Which repos are currently shown (nil = all)
	workspaceSummary string          // Summary text for footer (e.g., "3 repos")

	// Alerts panel (bv-168)
	alerts          []drift.Alert
	alertsCritical  int
	alertsWarning   int
	alertsInfo      int
	showAlertsPanel bool
	alertsCursor    int
	dismissedAlerts map[string]bool

	// Sprint view (bv-161)
	sprints        []model.Sprint
	selectedSprint *model.Sprint
	isSprintView   bool
	sprintViewText string

	// AGENTS.md integration (bv-i8dk)
	showAgentPrompt  bool
	agentPromptModal AgentPromptModal
	workDir          string // Working directory for agent file detection

	// Tutorial integration (bv-8y31)
	showTutorial  bool
	tutorialModel TutorialModel

	// Cass session preview modal (bv-5bqh)
	showCassModal  bool
	cassModal      CassSessionModal
	cassCorrelator *cass.Correlator

	// Self-update modal (bv-182)
	showUpdateModal bool
	updateModal     UpdateModal
}

// labelCount is a simple label->count pair for display
type labelCount struct {
	Label string
	Count int
}

type labelFlowSummary struct {
	Incoming []labelCount
	Outgoing []labelCount
}

// getCrossFlowsForLabel returns outgoing cross-label dependency counts for a label
func (m Model) getCrossFlowsForLabel(label string) labelFlowSummary {
	cfg := analysis.DefaultLabelHealthConfig()
	flow := analysis.ComputeCrossLabelFlow(m.issues, cfg)
	out := labelFlowSummary{}
	inCounts := make(map[string]int)
	outCounts := make(map[string]int)

	for _, dep := range flow.Dependencies {
		if dep.ToLabel == label {
			inCounts[dep.FromLabel] += dep.IssueCount
		}
		if dep.FromLabel == label {
			outCounts[dep.ToLabel] += dep.IssueCount
		}
	}

	for lbl, c := range inCounts {
		out.Incoming = append(out.Incoming, labelCount{Label: lbl, Count: c})
	}
	for lbl, c := range outCounts {
		out.Outgoing = append(out.Outgoing, labelCount{Label: lbl, Count: c})
	}

	sort.Slice(out.Incoming, func(i, j int) bool {
		if out.Incoming[i].Count == out.Incoming[j].Count {
			return out.Incoming[i].Label < out.Incoming[j].Label
		}
		return out.Incoming[i].Count > out.Incoming[j].Count
	})
	sort.Slice(out.Outgoing, func(i, j int) bool {
		if out.Outgoing[i].Count == out.Outgoing[j].Count {
			return out.Outgoing[i].Label < out.Outgoing[j].Label
		}
		return out.Outgoing[i].Count > out.Outgoing[j].Count
	})

	return out
}

// filterIssuesByLabel returns issues that contain the given label (case-sensitive match)
func (m Model) filterIssuesByLabel(label string) []model.Issue {
	if m.labelDrilldownCache != nil {
		if cached, ok := m.labelDrilldownCache[label]; ok {
			return cached
		}
	}

	var out []model.Issue
	for _, iss := range m.issues {
		for _, l := range iss.Labels {
			if l == label {
				out = append(out, iss)
				break
			}
		}
	}

	if m.labelDrilldownCache != nil {
		m.labelDrilldownCache[label] = out
	}
	return out
}

// extractLabelCounts converts LabelStats map to a simple count map for the label picker
func extractLabelCounts(stats map[string]*analysis.LabelStats) map[string]int {
	counts := make(map[string]int)
	for label, stat := range stats {
		if stat != nil {
			counts[label] = stat.TotalCount
		}
	}
	return counts
}

// WorkspaceInfo contains workspace loading metadata for TUI display
type WorkspaceInfo struct {
	Enabled      bool
	RepoCount    int
	FailedCount  int
	TotalIssues  int
	RepoPrefixes []string
}

func (m *Model) updateSemanticIDs(items []list.Item) {
	if m.semanticSearch == nil {
		return
	}
	ids := make([]string, 0, len(items))
	docs := make(map[string]string, len(items))
	for _, it := range items {
		if issueItem, ok := it.(IssueItem); ok {
			id := issueItem.Issue.ID
			ids = append(ids, id)
			docs[id] = search.IssueDocument(issueItem.Issue)
		}
	}
	m.semanticSearch.SetIDs(ids)
	m.semanticSearch.SetDocs(docs)
}

func (m *Model) shouldShowSearchScores() bool {
	if !m.semanticSearchEnabled || !m.semanticHybridEnabled || m.semanticSearch == nil {
		return false
	}
	if m.list.FilterState() == list.Unfiltered {
		return false
	}
	if strings.TrimSpace(m.list.FilterInput.Value()) == "" {
		return false
	}
	return true
}

func (m *Model) updateListDelegate() {
	m.list.SetDelegate(IssueDelegate{
		Theme:             m.theme,
		ShowPriorityHints: m.showPriorityHints,
		PriorityHints:     m.priorityHints,
		WorkspaceMode:     m.workspaceMode,
		ShowSearchScores:  m.shouldShowSearchScores(),
	})
}

func (m *Model) applySemanticScores(term string) {
	if m.semanticSearch == nil {
		return
	}
	scores, ok := m.semanticSearch.Scores(term)
	if !ok {
		return
	}
	items := m.list.Items()
	for i := range items {
		issueItem, ok := items[i].(IssueItem)
		if !ok {
			continue
		}
		if score, ok := scores[issueItem.Issue.ID]; ok {
			issueItem.SearchScore = score.Score
			issueItem.SearchTextScore = score.TextScore
			issueItem.SearchComponents = score.Components
			issueItem.SearchScoreSet = true
		} else {
			issueItem.SearchScore = 0
			issueItem.SearchTextScore = 0
			issueItem.SearchComponents = nil
			issueItem.SearchScoreSet = false
		}
		items[i] = issueItem
	}
}

func (m *Model) clearSemanticScores() {
	items := m.list.Items()
	changed := false
	for i := range items {
		issueItem, ok := items[i].(IssueItem)
		if !ok {
			continue
		}
		if issueItem.SearchScoreSet || issueItem.SearchComponents != nil {
			issueItem.SearchScore = 0
			issueItem.SearchTextScore = 0
			issueItem.SearchComponents = nil
			issueItem.SearchScoreSet = false
			items[i] = issueItem
			changed = true
		}
	}
	if changed && m.list.FilterState() != list.Unfiltered {
		prevState := m.list.FilterState()
		currentTerm := m.list.FilterInput.Value()
		// Reset cursor before SetFilterText to avoid panic on out-of-bounds access
		m.list.Select(0)
		m.list.SetFilterText(currentTerm)
		if prevState == list.Filtering {
			m.list.SetFilterState(list.Filtering)
		}
	}
}

func (m *Model) issuesForAsync() []model.Issue {
	if m == nil {
		return nil
	}
	if (m.snapshot != nil && len(m.snapshot.pooledIssues) > 0) || len(m.pooledIssues) > 0 {
		return cloneIssuesForAsync(m.issues)
	}
	return m.issues
}

// NewModel creates a new Model from the given issues
// beadsPath is the path to the beads.jsonl file for live reload support
func NewModel(issues []model.Issue, activeRecipe *recipe.Recipe, beadsPath string) Model {
	// Graph Analysis - Phase 1 is instant, Phase 2 runs in background
	analyzer := analysis.NewAnalyzer(issues)
	graphStats := analyzer.AnalyzeAsync(context.Background())

	// Sort issues
	if activeRecipe != nil && activeRecipe.Sort.Field != "" {
		r := activeRecipe
		descending := r.Sort.Direction == "desc"

		sort.Slice(issues, func(i, j int) bool {
			less := false
			switch r.Sort.Field {
			case "priority":
				less = issues[i].Priority < issues[j].Priority
			case "created", "created_at":
				less = issues[i].CreatedAt.Before(issues[j].CreatedAt)
			case "updated", "updated_at":
				less = issues[i].UpdatedAt.Before(issues[j].UpdatedAt)
			case "impact":
				less = graphStats.GetCriticalPathScore(issues[i].ID) < graphStats.GetCriticalPathScore(issues[j].ID)
			case "pagerank":
				less = graphStats.GetPageRankScore(issues[i].ID) < graphStats.GetPageRankScore(issues[j].ID)
			default:
				less = issues[i].Priority < issues[j].Priority
			}
			if descending {
				return !less
			}
			return less
		})
	} else {
		// Default Sort: Open first, then by Priority (ascending), then by date (newest first)
		sort.Slice(issues, func(i, j int) bool {
			iClosed := isClosedLikeStatus(issues[i].Status)
			jClosed := isClosedLikeStatus(issues[j].Status)
			if iClosed != jClosed {
				return !iClosed // Open issues first
			}
			if issues[i].Priority != issues[j].Priority {
				return issues[i].Priority < issues[j].Priority // Lower priority number = higher priority
			}
			return issues[i].CreatedAt.After(issues[j].CreatedAt) // Newer first
		})
	}

	// Build lookup map
	issueMap := make(map[string]*model.Issue, len(issues))

	// Build list items - scores may be 0 until Phase 2 completes
	items := make([]list.Item, len(issues))
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]

		items[i] = IssueItem{
			Issue:      issues[i],
			GraphScore: graphStats.GetPageRankScore(issues[i].ID),
			Impact:     graphStats.GetCriticalPathScore(issues[i].ID),
			RepoPrefix: ExtractRepoPrefix(issues[i].ID),
		}
	}

	// Compute stats
	cOpen, cReady, cBlocked, cClosed := 0, 0, 0, 0
	for i := range issues {
		issue := &issues[i]
		if isClosedLikeStatus(issue.Status) {
			cClosed++
			continue
		}

		cOpen++
		if issue.Status == model.StatusBlocked {
			cBlocked++
			continue
		}

		// Check if blocked by open dependencies
		isBlocked := false
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if blocker, exists := issueMap[dep.DependsOnID]; exists && !isClosedLikeStatus(blocker.Status) {
				isBlocked = true
				break
			}
		}
		if !isBlocked {
			cReady++
		}
	}

	// Theme
	theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))

	// Default dimensions for immediate ready state (updated when WindowSizeMsg arrives)
	// This eliminates the "Initializing..." phase entirely, fixing slow startup issues
	// in tmux, SSH, and slow terminal emulators where the terminal may delay sending size.
	const defaultWidth = 120
	const defaultHeight = 40

	// List setup - initialize with default dimensions so UI is immediately usable
	delegate := IssueDelegate{Theme: theme, WorkspaceMode: false}
	l := list.New(items, delegate, defaultWidth, defaultHeight-3)
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()
	// Clear all default styles that might add extra lines
	l.Styles.Title = lipgloss.NewStyle()
	l.Styles.TitleBar = lipgloss.NewStyle()
	l.Styles.FilterPrompt = lipgloss.NewStyle().Foreground(theme.Primary)
	l.Styles.FilterCursor = lipgloss.NewStyle().Foreground(theme.Primary)
	l.Styles.StatusBar = lipgloss.NewStyle()
	l.Styles.StatusEmpty = lipgloss.NewStyle()
	l.Styles.StatusBarActiveFilter = lipgloss.NewStyle()
	l.Styles.StatusBarFilterCount = lipgloss.NewStyle()
	l.Styles.NoItems = lipgloss.NewStyle()
	l.Styles.PaginationStyle = lipgloss.NewStyle()
	l.Styles.HelpStyle = lipgloss.NewStyle()

	// Theme-aware markdown renderer
	renderer := NewMarkdownRendererWithTheme(80, theme)

	// Initialize viewport with default dimensions
	vp := viewport.New(defaultWidth, defaultHeight-2)

	// Initialize sub-components
	board := NewBoardModel(issues, theme)
	labelDashboard := NewLabelDashboardModel(theme)
	labelDashboard.SetSize(defaultWidth, defaultHeight-1)
	velocityComparison := NewVelocityComparisonModel(theme) // bv-125
	shortcutsSidebar := NewShortcutsSidebar(theme)          // bv-3qi5
	ins := graphStats.GenerateInsights(len(issues))         // allow UI to show as many as fit
	insightsPanel := NewInsightsModel(ins, issueMap, theme)
	insightsPanel.SetSize(defaultWidth, defaultHeight-1)
	graphView := NewGraphModel(issues, &ins, theme)

	// Priority hints are generated asynchronously when Phase 2 completes
	// This avoids blocking startup on expensive graph analysis
	priorityHints := make(map[string]*analysis.PriorityRecommendation)

	// Compute triage insights (bv-151) - reuse existing analyzer/stats (bv-runn.12)
	triageResult := analysis.ComputeTriageFromAnalyzer(analyzer, graphStats, issues, analysis.TriageOptions{}, time.Now())
	triageScores := make(map[string]float64, len(triageResult.Recommendations))
	triageReasons := make(map[string]analysis.TriageReasons, len(triageResult.Recommendations))
	quickWinSet := make(map[string]bool, len(triageResult.QuickWins))
	blockerSet := make(map[string]bool, len(triageResult.BlockersToClear))
	unblocksMap := make(map[string][]string, len(triageResult.Recommendations))

	for _, rec := range triageResult.Recommendations {
		triageScores[rec.ID] = rec.Score
		if len(rec.Reasons) > 0 {
			triageReasons[rec.ID] = analysis.TriageReasons{
				Primary:    rec.Reasons[0],
				All:        rec.Reasons,
				ActionHint: rec.Action,
			}
		}
		unblocksMap[rec.ID] = rec.UnblocksIDs
	}
	for _, qw := range triageResult.QuickWins {
		quickWinSet[qw.ID] = true
	}
	for _, bl := range triageResult.BlockersToClear {
		blockerSet[bl.ID] = true
	}

	// Update items with triage data
	for i := range items {
		if issueItem, ok := items[i].(IssueItem); ok {
			issueItem.TriageScore = triageScores[issueItem.Issue.ID]
			if reasons, exists := triageReasons[issueItem.Issue.ID]; exists {
				issueItem.TriageReason = reasons.Primary
				issueItem.TriageReasons = reasons.All
			}
			issueItem.IsQuickWin = quickWinSet[issueItem.Issue.ID]
			issueItem.IsBlocker = blockerSet[issueItem.Issue.ID]
			issueItem.UnblocksCount = len(unblocksMap[issueItem.Issue.ID])
			items[i] = issueItem
		}
	}

	// Initialize recipe loader
	recipeLoader := recipe.NewLoader()
	_ = recipeLoader.Load() // Load recipes (errors are non-fatal, will just show empty)
	recipePicker := NewRecipePickerModel(recipeLoader.List(), theme)

	// Initialize label picker (bv-126)
	labelExtraction := analysis.ExtractLabels(issues)
	labelCounts := extractLabelCounts(labelExtraction.Stats)
	labelPicker := NewLabelPickerModel(labelExtraction.Labels, labelCounts, theme)

	// Initialize time-travel input
	ti := textinput.New()
	ti.Placeholder = "HEAD~5, main, v1.0.0, 2024-01-01..."
	ti.CharLimit = 100
	ti.Width = 40
	ti.Prompt = "⏱️  Revision: "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	ti.TextStyle = lipgloss.NewStyle().Foreground(theme.Base.GetForeground())

	// Initialize file watcher for live reload
	var fileWatcher *watcher.Watcher
	var watcherErr error
	var backgroundWorker *BackgroundWorker
	var backgroundModeErr error
	backgroundModeRequested := false
	if v := strings.TrimSpace(os.Getenv("BV_BACKGROUND_MODE")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			backgroundModeRequested = true
		case "0", "false", "no", "off":
			backgroundModeRequested = false
		}
	}

	if beadsPath != "" && backgroundModeRequested {
		bw, err := NewBackgroundWorker(WorkerConfig{
			BeadsPath:     beadsPath,
			DebounceDelay: 200 * time.Millisecond,
		})
		if err != nil {
			backgroundModeErr = err
		} else {
			backgroundWorker = bw
		}
	}

	if beadsPath != "" && backgroundWorker == nil {
		w, err := watcher.NewWatcher(beadsPath,
			watcher.WithDebounceDuration(200*time.Millisecond),
		)
		if err != nil {
			watcherErr = err
		} else if err := w.Start(); err != nil {
			watcherErr = err
		} else {
			fileWatcher = w
		}
	}

	// Initialize instance lock for multi-instance coordination (bv-vrvn)
	var instLock *instance.Lock
	if beadsPath != "" {
		beadsDir := filepath.Dir(beadsPath)
		lock, err := instance.NewLock(beadsDir)
		if err == nil {
			instLock = lock
		}
		// Lock creation failure is non-fatal - we just won't have coordination
	}

	// Semantic search (bv-9gf.3): initialized lazily on first toggle.
	semanticSearch := NewSemanticSearch()
	semanticIDs := make([]string, 0, len(items))
	for _, it := range items {
		if issueItem, ok := it.(IssueItem); ok {
			semanticIDs = append(semanticIDs, issueItem.Issue.ID)
		}
	}
	semanticSearch.SetIDs(semanticIDs)

	// Build initial status message if watcher failed
	var initialStatus string
	var initialStatusErr bool
	if backgroundWorker != nil {
		initialStatus = "Background mode enabled"
		initialStatusErr = false
	} else if backgroundModeRequested && backgroundModeErr != nil {
		initialStatus = fmt.Sprintf("Background mode unavailable: %v (using sync reload)", backgroundModeErr)
		initialStatusErr = true
	} else if watcherErr != nil {
		initialStatus = fmt.Sprintf("Live reload unavailable: %v", watcherErr)
		initialStatusErr = true
	}

	// Precompute drift/health alerts (bv-168)
	alerts, alertsCritical, alertsWarning, alertsInfo := computeAlerts(issues, graphStats, analyzer)

	// Load sprints from the same directory as beadsPath (bv-161)
	var sprints []model.Sprint
	if beadsPath != "" {
		beadsDir := filepath.Dir(beadsPath)
		if loaded, err := loader.LoadSprintsFromFile(filepath.Join(beadsDir, loader.SprintsFileName)); err == nil {
			sprints = loaded
		}
	}

	// Tree view state should persist alongside the beads directory (e.g. BEADS_DIR overrides).
	treeModel := NewTreeModel(theme)
	if beadsPath != "" {
		treeModel.SetBeadsDir(filepath.Dir(beadsPath))
	}

	return Model{
		issues:                 issues,
		issueMap:               issueMap,
		analyzer:               analyzer,
		analysis:               graphStats,
		beadsPath:              beadsPath,
		watcher:                fileWatcher,
		snapshotInitPending:    backgroundWorker != nil,
		backgroundWorker:       backgroundWorker,
		instanceLock:           instLock,
		list:                   l,
		viewport:               vp,
		renderer:               renderer,
		board:                  board,
		labelDashboard:         labelDashboard,
		velocityComparison:     velocityComparison,
		shortcutsSidebar:       shortcutsSidebar,
		graphView:              graphView,
		tree:                   treeModel,
		insightsPanel:          insightsPanel,
		theme:                  theme,
		currentFilter:          "all",
		semanticSearch:         semanticSearch,
		semanticHybridEnabled:  false,
		semanticHybridPreset:   search.PresetDefault,
		semanticHybridBuilding: false,
		semanticHybridReady:    false,
		lastSearchTerm:         "",
		focused:                focusList,
		splitPaneRatio:         0.4, // Default: list pane gets 40% of width
		// Initialize as ready with default dimensions to eliminate "Initializing..." phase
		ready:               true,
		width:               defaultWidth,
		height:              defaultHeight,
		countOpen:           cOpen,
		countReady:          cReady,
		countBlocked:        cBlocked,
		countClosed:         cClosed,
		priorityHints:       priorityHints,
		showPriorityHints:   false, // Off by default, toggle with 'p'
		triageScores:        triageScores,
		triageReasons:       triageReasons,
		unblocksMap:         unblocksMap,
		quickWinSet:         quickWinSet,
		blockerSet:          blockerSet,
		recipeLoader:        recipeLoader,
		recipePicker:        recipePicker,
		activeRecipe:        activeRecipe,
		labelPicker:         labelPicker,
		labelDrilldownCache: make(map[string][]model.Issue),
		timeTravelInput:     ti,
		statusMsg:           initialStatus,
		statusIsError:       initialStatusErr,
		historyLoading:      len(issues) > 0, // Will be loaded in Init()
		// Alerts panel (bv-168)
		alerts:          alerts,
		alertsCritical:  alertsCritical,
		alertsWarning:   alertsWarning,
		alertsInfo:      alertsInfo,
		dismissedAlerts: make(map[string]bool),
		// Sprint view (bv-161)
		sprints: sprints,
		// AGENTS.md integration (bv-i8dk) - workDir derived from beadsPath
		workDir: func() string {
			if beadsPath != "" {
				// beadsPath is like /path/to/project/.beads/beads.jsonl
				// workDir is /path/to/project
				return filepath.Dir(filepath.Dir(beadsPath))
			}
			return ""
		}(),
		// Tutorial integration (bv-8y31)
		tutorialModel: NewTutorialModel(theme),
	}
}

func (m Model) Init() tea.Cmd {
	// Note: ReadyTimeoutCmd is no longer needed since the model is now
	// initialized as ready with default dimensions in NewModel().
	// This eliminates the "Initializing..." phase entirely.
	cmds := []tea.Cmd{
		CheckUpdateCmd(),
		WaitForPhase2Cmd(m.analysis),
	}
	if m.backgroundWorker != nil {
		cmds = append(cmds, StartBackgroundWorkerCmd(m.backgroundWorker))
		cmds = append(cmds, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker))
		cmds = append(cmds, workerPollTickCmd())
	} else if m.watcher != nil {
		cmds = append(cmds, WatchFileCmd(m.watcher))
	}
	// Start loading history in background
	if len(m.issues) > 0 {
		cmds = append(cmds, LoadHistoryCmd(m.issuesForAsync(), m.beadsPath))
	}
	// Check for AGENTS.md integration prompt (bv-i8dk)
	if m.workDir != "" && !m.workspaceMode {
		cmds = append(cmds, CheckAgentFileCmd(m.workDir))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	if m.backgroundWorker != nil {
		switch msg.(type) {
		case tea.KeyMsg, tea.MouseMsg:
			m.backgroundWorker.recordActivity()
		}
	}

	switch msg := msg.(type) {
	case UpdateMsg:
		m.updateAvailable = true
		m.updateTag = msg.TagName
		m.updateURL = msg.URL

	case UpdateCompleteMsg:
		// Forward to the update modal
		if m.showUpdateModal {
			m.updateModal, cmd = m.updateModal.Update(msg)
			cmds = append(cmds, cmd)
		}

	case UpdateProgressMsg:
		// Forward to the update modal
		if m.showUpdateModal {
			m.updateModal, cmd = m.updateModal.Update(msg)
			cmds = append(cmds, cmd)
		}

	case ReadyTimeoutMsg:
		// bv-7wl7: Legacy fallback handler (no longer used).
		// The model is now initialized as ready with default dimensions in NewModel(),
		// so this handler should never execute. Kept for backwards compatibility.
		if !m.ready {
			m.width = 120
			m.height = 40
			m.ready = true
			m.list.SetSize(m.width, m.height-3)
			m.viewport = viewport.New(m.width, m.height-2)
			m.insightsPanel.SetSize(m.width, m.height-1)
			m.labelDashboard.SetSize(m.width, m.height-1)
		}

	case SemanticIndexReadyMsg:
		m.semanticIndexBuilding = false
		if msg.Error != nil {
			// If indexing fails, revert to fuzzy mode for predictable behavior.
			m.semanticSearchEnabled = false
			m.list.Filter = list.DefaultFilter
			m.statusMsg = fmt.Sprintf("Semantic search unavailable: %v", msg.Error)
			m.statusIsError = true
			break
		}
		if m.semanticSearch != nil {
			m.semanticSearch.SetIndex(msg.Index, msg.Embedder)
		}
		if !msg.Loaded {
			m.statusMsg = fmt.Sprintf("Semantic index built (%d embedded)", msg.Stats.Embedded)
		} else if msg.Stats.Changed() {
			m.statusMsg = fmt.Sprintf("Semantic index updated (+%d ~%d -%d)", msg.Stats.Added, msg.Stats.Updated, msg.Stats.Removed)
		} else {
			m.statusMsg = "Semantic index up to date"
		}
		m.statusIsError = false

		// Refresh current filter view if the user is actively searching.
		if m.semanticSearchEnabled && m.list.FilterState() != list.Unfiltered {
			prevState := m.list.FilterState()
			filterText := m.list.FilterInput.Value()
			m.list.SetFilterText(filterText)
			if prevState == list.Filtering {
				m.list.SetFilterState(list.Filtering)
			}
		}

	case HybridMetricsReadyMsg:
		m.semanticHybridBuilding = false
		if msg.Error != nil {
			m.semanticHybridEnabled = false
			m.semanticHybridReady = false
			if m.semanticSearch != nil {
				m.semanticSearch.SetMetricsCache(nil)
				m.semanticSearch.SetHybridConfig(false, m.semanticHybridPreset)
			}
			m.statusMsg = fmt.Sprintf("Hybrid search unavailable: %v", msg.Error)
			m.statusIsError = true
			break
		}
		if m.semanticSearch != nil && msg.Cache != nil {
			m.semanticSearch.SetMetricsCache(msg.Cache)
		}
		m.semanticHybridReady = msg.Cache != nil
		m.statusMsg = fmt.Sprintf("Hybrid search ready (%s)", m.semanticHybridPreset)
		m.statusIsError = false

		// Recompute semantic results if hybrid is enabled and search is active.
		if m.semanticHybridEnabled && m.semanticSearchEnabled && m.list.FilterState() != list.Unfiltered {
			currentTerm := m.list.FilterInput.Value()
			if currentTerm != "" {
				m.semanticSearch.ResetCache()
				cmds = append(cmds, ComputeSemanticFilterCmd(m.semanticSearch, currentTerm))
			}
		}

	case SemanticFilterResultMsg:
		// Async semantic filter results arrived - cache and refresh list
		if m.semanticSearch != nil && msg.Results != nil {
			m.semanticSearch.SetCachedResults(msg.Term, msg.Results)

			// Refresh list if still filtering with the same term
			currentTerm := m.list.FilterInput.Value()
			if m.semanticSearchEnabled && currentTerm == msg.Term {
				m.applySemanticScores(msg.Term)
				prevState := m.list.FilterState()
				m.list.SetFilterText(currentTerm)
				if prevState == list.Filtering {
					m.list.SetFilterState(list.Filtering)
				}
			}
		}

	case semanticDebounceTickMsg:
		// Debounce timer expired - check if we should trigger semantic computation
		if m.semanticSearchEnabled && m.semanticSearch != nil && m.list.FilterState() != list.Unfiltered {
			pendingTerm := m.semanticSearch.GetPendingTerm()
			if pendingTerm != "" && time.Since(m.semanticSearch.GetLastQueryTime()) >= 150*time.Millisecond {
				return m, ComputeSemanticFilterCmd(m.semanticSearch, pendingTerm)
			}
		}

	case workerPollTickMsg:
		if m.backgroundWorker != nil {
			state := m.backgroundWorker.State()
			if state == WorkerProcessing {
				m.workerSpinnerIdx = (m.workerSpinnerIdx + 1) % len(workerSpinnerFrames)
			} else {
				m.workerSpinnerIdx = 0
			}
			if state != WorkerStopped {
				cmds = append(cmds, workerPollTickCmd())
			}
		}

	case Phase2ReadyMsg:
		// Ignore stale Phase2 completions (from before a file reload)
		if msg.Stats != m.analysis {
			return m, nil
		}

		// Mark snapshot as Phase 2 ready for consistency with Phase2UpdateMsg (bv-e3ub)
		if m.snapshot != nil {
			m.snapshot.Phase2Ready = true
		}

		// Phase 2 analysis complete - update insights with full data (computed off-thread).
		ins := msg.Insights
		if m.snapshot != nil {
			m.snapshot.Insights = ins
		}
		m.insightsPanel.SetInsights(ins)
		m.insightsPanel.issueMap = m.issueMap
		bodyHeight := m.height - 1
		if bodyHeight < 5 {
			bodyHeight = 5
		}
		m.insightsPanel.SetSize(m.width, bodyHeight)
		if m.snapshot != nil {
			if m.snapshot.GraphLayout != nil {
				m.snapshot.GraphLayout.UpdatePhase2Ranks(msg.Stats)
			}
			m.graphView.SetSnapshot(m.snapshot)
		} else {
			m.graphView.SetIssues(m.issues, &ins)
		}

		// Generate triage for priority panel (bv-91) - reuse existing analyzer/stats (bv-runn.12)
		triage := analysis.ComputeTriageFromAnalyzer(m.analyzer, m.analysis, m.issues, analysis.TriageOptions{}, time.Now())
		triageScores := make(map[string]float64, len(triage.Recommendations))
		triageReasons := make(map[string]analysis.TriageReasons, len(triage.Recommendations))
		quickWinSet := make(map[string]bool, len(triage.QuickWins))
		blockerSet := make(map[string]bool, len(triage.BlockersToClear))
		unblocksMap := make(map[string][]string, len(triage.Recommendations))

		for _, rec := range triage.Recommendations {
			triageScores[rec.ID] = rec.Score
			if len(rec.Reasons) > 0 {
				triageReasons[rec.ID] = analysis.TriageReasons{
					Primary:    rec.Reasons[0],
					All:        rec.Reasons,
					ActionHint: rec.Action,
				}
			}
			unblocksMap[rec.ID] = rec.UnblocksIDs
		}
		for _, qw := range triage.QuickWins {
			quickWinSet[qw.ID] = true
		}
		for _, bl := range triage.BlockersToClear {
			blockerSet[bl.ID] = true
		}

		m.triageScores = triageScores
		m.triageReasons = triageReasons
		m.quickWinSet = quickWinSet
		m.blockerSet = blockerSet
		m.unblocksMap = unblocksMap

		m.insightsPanel.SetTopPicks(triage.QuickRef.TopPicks)

		// Set full recommendations with breakdown for priority radar (bv-93)
		dataHash := fmt.Sprintf("v%s@%s#%d", triage.Meta.Version, triage.Meta.GeneratedAt.Format("15:04:05"), triage.Meta.IssueCount)
		m.insightsPanel.SetRecommendations(triage.Recommendations, dataHash)

		// Generate priority recommendations now that Phase 2 is ready
		recommendations := m.analyzer.GenerateRecommendations()
		m.priorityHints = make(map[string]*analysis.PriorityRecommendation, len(recommendations))
		for i := range recommendations {
			m.priorityHints[recommendations[i].IssueID] = &recommendations[i]
		}

		// Refresh alerts now that full Phase 2 metrics (cycles, etc.) are available
		m.alerts, m.alertsCritical, m.alertsWarning, m.alertsInfo = computeAlerts(m.issues, m.analysis, m.analyzer)

		// Invalidate label health cache since we have new graph metrics (criticality)
		m.labelHealthCached = false
		if m.focused == focusLabelDashboard {
			cfg := analysis.DefaultLabelHealthConfig()
			m.labelHealthCache = analysis.ComputeAllLabelHealth(m.issues, cfg, time.Now().UTC(), m.analysis)
			m.labelHealthCached = true
			m.labelDashboard.SetData(m.labelHealthCache.Labels)
			m.statusMsg = fmt.Sprintf("Labels: %d total • critical %d • warning %d", m.labelHealthCache.TotalLabels, m.labelHealthCache.CriticalCount, m.labelHealthCache.WarningCount)
		}

		// Re-sort issues if sorting by Phase 2 metrics (impact/pagerank)
		if m.activeRecipe != nil {
			switch m.activeRecipe.Sort.Field {
			case "impact", "pagerank":
				field := m.activeRecipe.Sort.Field
				descending := m.activeRecipe.Sort.Direction == "desc"
				sort.Slice(m.issues, func(i, j int) bool {
					ii := m.issues[i]
					jj := m.issues[j]

					var iScore, jScore float64
					if m.analysis != nil {
						if field == "impact" {
							iScore = m.analysis.GetCriticalPathScore(ii.ID)
							jScore = m.analysis.GetCriticalPathScore(jj.ID)
						} else {
							iScore = m.analysis.GetPageRankScore(ii.ID)
							jScore = m.analysis.GetPageRankScore(jj.ID)
						}
					}

					var cmp int
					switch {
					case iScore < jScore:
						cmp = -1
					case iScore > jScore:
						cmp = 1
					}
					if cmp == 0 {
						return ii.ID < jj.ID
					}
					if descending {
						return cmp > 0
					}
					return cmp < 0
				})
				// Rebuild issueMap after re-sort (pointers become stale after sorting)
				for i := range m.issues {
					m.issueMap[m.issues[i].ID] = &m.issues[i]
				}
			}
		}

		// Re-apply recipe filter if active (to update scores while preserving filter)
		// Otherwise, update list respecting current filter (open/ready/etc.)
		if m.activeRecipe != nil {
			m.applyRecipe(m.activeRecipe)
		} else if m.currentFilter == "" || m.currentFilter == "all" {
			m.refreshListItemsPhase2()
		} else {
			m.applyFilter()
		}

	case Phase2UpdateMsg:
		// BackgroundWorker notifies that Phase 2 analysis is complete (bv-e3ub)
		// Verify this update matches the current snapshot using DataHash
		if m.snapshot == nil || m.snapshot.DataHash != msg.DataHash {
			// Stale update - ignore
			if m.backgroundWorker != nil {
				return m, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker)
			}
			return m, nil
		}

		// Mark snapshot as Phase 2 ready
		m.snapshot.Phase2Ready = true

		// Note: Phase2ReadyMsg handler (via WaitForPhase2Cmd) already handles
		// all the UI updates (insights, graph view, alerts, etc.). This message
		// is a complementary notification from the BackgroundWorker that Phase 2
		// completed. If Phase2ReadyMsg hasn't fired yet, it will handle the full
		// UI refresh. If it already fired (race condition), this is a no-op.
		if m.backgroundWorker != nil {
			return m, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker)
		}
		return m, nil

	case HistoryLoadedMsg:
		// Background history loading completed
		m.historyLoading = false
		if msg.Error != nil {
			m.historyLoadFailed = true
			m.statusMsg = fmt.Sprintf("History load failed: %v", msg.Error)
			m.statusIsError = true
		} else if msg.Report != nil {
			m.historyView = NewHistoryModel(msg.Report, m.theme)
			m.historyView.SetSize(m.width, m.height-1)
			// Refresh detail pane if visible
			if m.isSplitView || m.showDetails {
				m.updateViewportContent()
			}
		}

	case AgentFileCheckMsg:
		// AGENTS.md integration check (bv-i8dk)
		if msg.ShouldPrompt && msg.FilePath != "" {
			m.showAgentPrompt = true
			m.agentPromptModal = NewAgentPromptModal(msg.FilePath, msg.FileType, m.theme)
			m.focused = focusAgentPrompt
		}

	case SnapshotReadyMsg:
		// Background worker has a new snapshot ready (bv-m7v8)
		// This is the atomic pointer swap - O(1), sub-microsecond
		if msg.Snapshot == nil {
			if m.backgroundWorker != nil {
				return m, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker)
			}
			return m, nil
		}

		firstSnapshot := m.snapshotInitPending && m.snapshot == nil
		m.snapshotInitPending = false

		// Clear ephemeral overlays tied to old data
		m.clearAttentionOverlay()

		// Exit time-travel mode if active (file changed, show current state)
		if m.timeTravelMode {
			m.timeTravelMode = false
			m.timeTravelDiff = nil
			m.timeTravelSince = ""
			m.newIssueIDs = nil
			m.closedIssueIDs = nil
			m.modifiedIssueIDs = nil
		}

		// Store selected issue ID to restore position after swap
		var selectedID string
		if sel := m.list.SelectedItem(); sel != nil {
			if item, ok := sel.(IssueItem); ok {
				selectedID = item.Issue.ID
			}
		}

		// Preserve board selection by issue ID (bv-6n4c).
		var boardSelectedID string
		if m.focused == focusBoard {
			if sel := m.board.SelectedIssue(); sel != nil {
				boardSelectedID = sel.ID
			}
		}

		oldSnapshot := m.snapshot

		// Swap snapshot pointer
		m.snapshot = msg.Snapshot
		if m.backgroundWorker != nil {
			latencyStart := msg.FileChangeAt
			if latencyStart.IsZero() {
				latencyStart = msg.SentAt
			}
			if !latencyStart.IsZero() {
				m.backgroundWorker.recordUIUpdateLatency(time.Since(latencyStart))
			}
		}
		if oldSnapshot != nil && len(oldSnapshot.pooledIssues) > 0 {
			go loader.ReturnIssuePtrsToPool(oldSnapshot.pooledIssues)
		}

		// Update legacy fields for backwards compatibility during migration
		// Eventually these will be removed when all code reads from snapshot
		m.issues = msg.Snapshot.Issues
		m.issueMap = msg.Snapshot.IssueMap
		m.analyzer = msg.Snapshot.Analyzer
		m.analysis = msg.Snapshot.Analysis
		m.countOpen = msg.Snapshot.CountOpen
		m.countReady = msg.Snapshot.CountReady
		m.countBlocked = msg.Snapshot.CountBlocked
		m.countClosed = msg.Snapshot.CountClosed
		if len(m.pooledIssues) > 0 {
			go loader.ReturnIssuePtrsToPool(m.pooledIssues)
			m.pooledIssues = nil
		}
		// Preserve existing triage data unless the snapshot has Phase 2 results.
		// Avoid flicker when Phase 1 snapshots arrive without triage data.
		if msg.Snapshot.Phase2Ready || len(msg.Snapshot.TriageScores) > 0 {
			m.triageScores = msg.Snapshot.TriageScores
			m.triageReasons = msg.Snapshot.TriageReasons
			m.unblocksMap = msg.Snapshot.UnblocksMap
			m.quickWinSet = msg.Snapshot.QuickWinSet
			m.blockerSet = msg.Snapshot.BlockerSet
		}

		// Clear caches that need recomputation
		m.labelHealthCached = false
		m.attentionCached = false
		m.priorityHints = make(map[string]*analysis.PriorityRecommendation)
		m.labelDrilldownCache = make(map[string][]model.Issue)

		// Recompute alerts for refreshed dataset
		m.alerts, m.alertsCritical, m.alertsWarning, m.alertsInfo = computeAlerts(m.issues, m.analysis, m.analyzer)
		m.dismissedAlerts = make(map[string]bool)
		m.showAlertsPanel = false

		// Reset semantic caches for the new dataset.
		if m.semanticSearch != nil {
			m.semanticSearch.ResetCache()
			m.semanticSearch.SetMetricsCache(nil)
		}
		m.semanticHybridReady = false
		m.semanticHybridBuilding = false
		if m.semanticHybridEnabled {
			m.semanticHybridBuilding = true
			cmds = append(cmds, BuildHybridMetricsCmd(m.issuesForAsync()))
		}

		// Regenerate sub-views (Phase 1 data; Phase 2 will update via Phase2ReadyMsg)
		m.insightsPanel.SetInsights(m.snapshot.Insights)
		m.insightsPanel.issueMap = m.issueMap
		bodyHeight := m.height - 1
		if bodyHeight < 5 {
			bodyHeight = 5
		}
		m.insightsPanel.SetSize(m.width, bodyHeight)

		// Update list/board/graph views while preserving the current recipe/filter state.
		if m.activeRecipe != nil {
			// If the snapshot already includes recipe filtering/sorting, use it directly (bv-cwwd).
			if msg.Snapshot.RecipeName == m.activeRecipe.Name && msg.Snapshot.RecipeHash == recipeFingerprint(m.activeRecipe) {
				filteredItems := make([]list.Item, 0, len(msg.Snapshot.ListItems))
				filteredIssues := make([]model.Issue, 0, len(msg.Snapshot.ListItems))

				for _, item := range msg.Snapshot.ListItems {
					issue := item.Issue

					// Workspace repo filter (nil = all repos)
					if m.workspaceMode && m.activeRepos != nil {
						repoKey := strings.ToLower(item.RepoPrefix)
						if repoKey != "" && !m.activeRepos[repoKey] {
							continue
						}
					}

					filteredItems = append(filteredItems, item)
					filteredIssues = append(filteredIssues, issue)
				}

				m.list.SetItems(filteredItems)
				m.updateSemanticIDs(filteredItems)
				m.board.SetIssues(filteredIssues)

				recipeIns := analysis.Insights{}
				if m.analysis != nil {
					recipeIns = m.analysis.GenerateInsights(len(filteredIssues))
				}
				m.graphView.SetIssues(filteredIssues, &recipeIns)

				m.currentFilter = "recipe:" + m.activeRecipe.Name

				// Keep selection in bounds
				if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
					m.list.Select(0)
				}
			} else {
				m.applyRecipe(m.activeRecipe)
			}
		} else {
			var filteredItems []list.Item
			var filteredIssues []model.Issue

			filteredItems = make([]list.Item, 0, len(msg.Snapshot.ListItems))
			filteredIssues = make([]model.Issue, 0, len(msg.Snapshot.ListItems))

			for _, item := range msg.Snapshot.ListItems {
				issue := item.Issue

				// Workspace repo filter (nil = all repos)
				if m.workspaceMode && m.activeRepos != nil {
					repoKey := strings.ToLower(item.RepoPrefix)
					if repoKey != "" && !m.activeRepos[repoKey] {
						continue
					}
				}

				include := false
				switch m.currentFilter {
				case "all":
					include = true
				case "open":
					include = !isClosedLikeStatus(issue.Status)
				case "closed":
					include = isClosedLikeStatus(issue.Status)
				case "ready":
					// Ready = Open/InProgress AND NO Open Blockers
					// Exclude draft/deferred - not ready for execution
					if !isClosedLikeStatus(issue.Status) && issue.Status != model.StatusBlocked &&
						issue.Status != model.StatusDraft && issue.Status != model.StatusDeferred {
						isBlocked := false
						for _, dep := range issue.Dependencies {
							if dep == nil || !dep.Type.IsBlocking() {
								continue
							}
							if blocker, exists := m.issueMap[dep.DependsOnID]; exists && !isClosedLikeStatus(blocker.Status) {
								isBlocked = true
								break
							}
						}
						include = !isBlocked
					}
				default:
					if strings.HasPrefix(m.currentFilter, "label:") {
						label := strings.TrimPrefix(m.currentFilter, "label:")
						for _, l := range issue.Labels {
							if l == label {
								include = true
								break
							}
						}
					}
				}

				if include {
					filteredItems = append(filteredItems, item)
					filteredIssues = append(filteredIssues, issue)
				}
			}

			m.sortFilteredItems(filteredItems, filteredIssues)
			m.list.SetItems(filteredItems)
			m.updateSemanticIDs(filteredItems)
			if m.snapshot != nil && m.snapshot.BoardState != nil && (!m.workspaceMode || m.activeRepos == nil) && len(filteredIssues) == len(m.snapshot.Issues) {
				m.board.SetSnapshot(m.snapshot)
			} else {
				m.board.SetIssues(filteredIssues)
			}
			if m.snapshot != nil && m.snapshot.GraphLayout != nil && len(filteredIssues) == len(m.snapshot.Issues) {
				m.graphView.SetSnapshot(m.snapshot)
			} else {
				m.graphView.SetIssues(filteredIssues, &m.snapshot.Insights)
			}

			// Restore selection if possible
			if selectedID != "" {
				for i, it := range filteredItems {
					if item, ok := it.(IssueItem); ok && item.Issue.ID == selectedID {
						m.list.Select(i)
						break
					}
				}
			}

			// Keep selection in bounds
			if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
				m.list.Select(0)
			}
		}

		// Restore selection in recipe mode (applyRecipe rebuilds list items)
		if m.activeRecipe != nil && selectedID != "" {
			items := m.list.Items()
			for i := range items {
				if item, ok := items[i].(IssueItem); ok && item.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
		}

		// Restore board selection after SetIssues/applyRecipe rebuilds columns (bv-6n4c).
		if boardSelectedID != "" {
			_ = m.board.SelectIssueByID(boardSelectedID)
		}

		// If the tree view is active, rebuild it from the new snapshot while preserving
		// user state (selection + persisted expand/collapse) (bv-6n4c).
		if m.focused == focusTree {
			m.tree.BuildFromSnapshot(m.snapshot)
			m.tree.SetSize(m.width, m.height-2)
		}

		// Refresh detail pane if visible
		if m.isSplitView || m.showDetails {
			m.updateViewportContent()
		}

		// Keep semantic index current when enabled.
		if m.semanticSearchEnabled && !m.semanticIndexBuilding {
			m.semanticIndexBuilding = true
			cmds = append(cmds, BuildSemanticIndexCmd(m.issuesForAsync()))
		}

		// Reload sprints (bv-161)
		if m.beadsPath != "" {
			beadsDir := filepath.Dir(m.beadsPath)
			if loaded, err := loader.LoadSprintsFromFile(filepath.Join(beadsDir, loader.SprintsFileName)); err == nil {
				m.sprints = loaded
				// If we have a selected sprint, try to refresh it
				if m.selectedSprint != nil {
					found := false
					for i := range m.sprints {
						if m.sprints[i].ID == m.selectedSprint.ID {
							m.selectedSprint = &m.sprints[i]
							m.sprintViewText = m.renderSprintDashboard()
							found = true
							break
						}
					}
					if !found {
						m.selectedSprint = nil
						m.sprintViewText = "Sprint not found"
					}
				}
			}
		}

		if firstSnapshot {
			// For the initial background snapshot, avoid flashing "Reloaded" at startup.
			if msg.Snapshot.LoadWarningCount > 0 {
				m.statusMsg = fmt.Sprintf("Loaded %d issues (%d warnings)", len(m.issues), msg.Snapshot.LoadWarningCount)
			} else {
				m.statusMsg = ""
			}
		} else if msg.Snapshot.LoadWarningCount > 0 {
			m.statusMsg = fmt.Sprintf("Reloaded %d issues (%d warnings)", len(m.issues), msg.Snapshot.LoadWarningCount)
		} else {
			m.statusMsg = fmt.Sprintf("Reloaded %d issues", len(m.issues))
		}
		m.statusIsError = false

		// Wait for Phase 2 if not ready
		if msg.Snapshot.Analysis != nil {
			cmds = append(cmds, WaitForPhase2Cmd(msg.Snapshot.Analysis))
		}

		if m.backgroundWorker != nil {
			cmds = append(cmds, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker))
		}

		return m, tea.Batch(cmds...)

	case SnapshotErrorMsg:
		// Background worker encountered an error loading/processing data
		// If recoverable, we'll try again on next file change.
		if m.snapshotInitPending && m.snapshot == nil {
			m.snapshotInitPending = false
		}
		if msg.Err != nil {
			if msg.Recoverable {
				m.statusMsg = fmt.Sprintf("Background reload error (will retry): %v", msg.Err)
			} else {
				m.statusMsg = fmt.Sprintf("Background reload error: %v", msg.Err)
			}
			m.statusIsError = true
		}
		if m.backgroundWorker != nil {
			cmds = append(cmds, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker))
		}
		return m, tea.Batch(cmds...)

	case FileChangedMsg:
		// File changed on disk - reload issues and recompute analysis
		// In background mode the BackgroundWorker owns file watching and snapshot building.
		if m.backgroundWorker != nil {
			if m.watcher != nil {
				cmds = append(cmds, WatchFileCmd(m.watcher))
			}
			return m, tea.Batch(cmds...)
		}
		if m.beadsPath == "" {
			// Re-start watch for next change
			if m.watcher != nil {
				cmds = append(cmds, WatchFileCmd(m.watcher))
			}
			return m, tea.Batch(cmds...)
		}
		reloadStart := time.Now()
		profileRefresh := debug.Enabled()
		var refreshTimings map[string]time.Duration
		recordTiming := func(name string, d time.Duration) {
			if !profileRefresh {
				return
			}
			if refreshTimings == nil {
				refreshTimings = make(map[string]time.Duration, 12)
			}
			refreshTimings[name] = d
			debug.LogTiming("refresh."+name, d)
		}
		if profileRefresh {
			debug.Log("refresh: file change detected path=%s", m.beadsPath)
		}

		// Clear ephemeral overlays tied to old data
		m.clearAttentionOverlay()

		// Exit time-travel mode if active (file changed, show current state)
		if m.timeTravelMode {
			m.timeTravelMode = false
			m.timeTravelDiff = nil
			m.timeTravelSince = ""
			m.newIssueIDs = nil
			m.closedIssueIDs = nil
			m.modifiedIssueIDs = nil
		}

		// Reload issues from disk
		// Use custom warning handler to prevent stderr pollution during TUI render (bv-fix)
		var reloadWarnings []string
		if _, err := loader.PrepareBeadsDirForRead(filepath.Dir(m.beadsPath), true, func(msg string) {
			reloadWarnings = append(reloadWarnings, msg)
		}); err != nil {
			m.statusMsg = fmt.Sprintf("Refresh error: %v", err)
			m.statusIsError = true
			if m.watcher != nil {
				cmds = append(cmds, WatchFileCmd(m.watcher))
			}
			return m, tea.Batch(cmds...)
		}
		var loadStart time.Time
		if profileRefresh {
			loadStart = time.Now()
		}
		loadedIssues, err := loader.LoadIssuesFromFileWithOptionsPooled(m.beadsPath, loader.ParseOptions{
			WarningHandler: func(msg string) {
				reloadWarnings = append(reloadWarnings, msg)
			},
			BufferSize: envMaxLineSizeBytes(),
		})
		if profileRefresh {
			recordTiming("load_issues", time.Since(loadStart))
		}
		if err != nil {
			m.statusMsg = fmt.Sprintf("Reload error: %v", err)
			m.statusIsError = true
			// Re-start watch for next change
			if m.watcher != nil {
				cmds = append(cmds, WatchFileCmd(m.watcher))
			}
			return m, tea.Batch(cmds...)
		}
		if len(m.pooledIssues) > 0 {
			loader.ReturnIssuePtrsToPool(m.pooledIssues)
		}
		m.pooledIssues = loadedIssues.PoolRefs
		newIssues := loadedIssues.Issues

		// Store selected issue ID to restore position after reload
		var selectedID string
		if sel := m.list.SelectedItem(); sel != nil {
			if item, ok := sel.(IssueItem); ok {
				selectedID = item.Issue.ID
			}
		}

		// Apply default sorting (Open first, Priority, Date)
		var sortStart time.Time
		if profileRefresh {
			sortStart = time.Now()
		}
		sort.Slice(newIssues, func(i, j int) bool {
			iClosed := isClosedLikeStatus(newIssues[i].Status)
			jClosed := isClosedLikeStatus(newIssues[j].Status)
			if iClosed != jClosed {
				return !iClosed
			}
			if newIssues[i].Priority != newIssues[j].Priority {
				return newIssues[i].Priority < newIssues[j].Priority
			}
			return newIssues[i].CreatedAt.After(newIssues[j].CreatedAt)
		})
		if profileRefresh {
			recordTiming("sort_issues", time.Since(sortStart))
		}

		// Recompute analysis (async Phase 1/Phase 2) with caching
		m.issues = newIssues
		var analysisStart time.Time
		if profileRefresh {
			analysisStart = time.Now()
		}
		cachedAnalyzer := analysis.NewCachedAnalyzer(newIssues, nil)
		m.analyzer = cachedAnalyzer.Analyzer
		m.analysis = cachedAnalyzer.AnalyzeAsync(context.Background())
		cacheHit := cachedAnalyzer.WasCacheHit()
		if profileRefresh {
			recordTiming("phase1_setup", time.Since(analysisStart))
			debug.Log("refresh.phase1_cache_hit=%t issues=%d", cacheHit, len(newIssues))
		}
		m.labelHealthCached = false
		m.attentionCached = false

		// Rebuild lookup map
		var mapStart time.Time
		if profileRefresh {
			mapStart = time.Now()
		}
		m.issueMap = make(map[string]*model.Issue, len(newIssues))
		for i := range m.issues {
			m.issueMap[m.issues[i].ID] = &m.issues[i]
		}
		if profileRefresh {
			recordTiming("issue_map", time.Since(mapStart))
		}

		// Clear stale priority hints (will be repopulated after Phase 2)
		m.priorityHints = make(map[string]*analysis.PriorityRecommendation)

		// Recompute stats
		var statsStart time.Time
		if profileRefresh {
			statsStart = time.Now()
		}
		m.countOpen, m.countReady, m.countBlocked, m.countClosed = 0, 0, 0, 0
		for i := range m.issues {
			issue := &m.issues[i]
			if isClosedLikeStatus(issue.Status) {
				m.countClosed++
				continue
			}
			m.countOpen++
			if issue.Status == model.StatusBlocked {
				m.countBlocked++
				continue
			}
			isBlocked := false
			for _, dep := range issue.Dependencies {
				if dep == nil || !dep.Type.IsBlocking() {
					continue
				}
				if blocker, exists := m.issueMap[dep.DependsOnID]; exists && !isClosedLikeStatus(blocker.Status) {
					isBlocked = true
					break
				}
			}
			if !isBlocked {
				m.countReady++
			}
		}
		if profileRefresh {
			recordTiming("counts", time.Since(statsStart))
		}

		// Recompute alerts for refreshed dataset
		var alertsStart time.Time
		if profileRefresh {
			alertsStart = time.Now()
		}
		m.alerts, m.alertsCritical, m.alertsWarning, m.alertsInfo = computeAlerts(m.issues, m.analysis, m.analyzer)
		if profileRefresh {
			recordTiming("alerts", time.Since(alertsStart))
		}
		m.dismissedAlerts = make(map[string]bool)
		m.showAlertsPanel = false

		// Rebuild list items (preserve triage data to avoid flicker)
		var listStart time.Time
		if profileRefresh {
			listStart = time.Now()
		}
		items := make([]list.Item, len(m.issues))
		for i := range m.issues {
			item := IssueItem{
				Issue:      m.issues[i],
				GraphScore: m.analysis.GetPageRankScore(m.issues[i].ID),
				Impact:     m.analysis.GetCriticalPathScore(m.issues[i].ID),
				RepoPrefix: ExtractRepoPrefix(m.issues[i].ID),
			}
			item.TriageScore = m.triageScores[m.issues[i].ID]
			if reasons, exists := m.triageReasons[m.issues[i].ID]; exists {
				item.TriageReason = reasons.Primary
				item.TriageReasons = reasons.All
			}
			item.IsQuickWin = m.quickWinSet[m.issues[i].ID]
			item.IsBlocker = m.blockerSet[m.issues[i].ID]
			item.UnblocksCount = len(m.unblocksMap[m.issues[i].ID])
			items[i] = item
		}
		if profileRefresh {
			recordTiming("list_items", time.Since(listStart))
		}
		m.updateSemanticIDs(items)
		m.clearSemanticScores()
		if m.semanticSearch != nil {
			m.semanticSearch.ResetCache()
			m.semanticSearch.SetMetricsCache(nil)
		}
		m.semanticHybridReady = false
		m.semanticHybridBuilding = false
		if m.semanticHybridEnabled {
			m.semanticHybridBuilding = true
			cmds = append(cmds, BuildHybridMetricsCmd(m.issuesForAsync()))
		}
		m.list.SetItems(items)

		// Restore selection position
		if selectedID != "" {
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
		}

		// Regenerate sub-views (with Phase 1 data; Phase 2 will update via Phase2ReadyMsg)
		// Preserve triage data already computed to avoid UI flicker.
		needsInsights := m.focused == focusInsights && !m.showAttentionView
		needsGraph := m.isGraphView
		var ins analysis.Insights
		if needsInsights || needsGraph {
			var insightsStart time.Time
			if profileRefresh {
				insightsStart = time.Now()
			}
			ins = m.analysis.GenerateInsights(len(m.issues))
			if profileRefresh {
				recordTiming("insights_generate", time.Since(insightsStart))
			}
		}
		if needsInsights {
			oldTopPicks := m.insightsPanel.topPicks
			oldRecs := m.insightsPanel.recommendations
			oldRecMap := m.insightsPanel.recommendationMap
			oldHash := m.insightsPanel.triageDataHash

			m.insightsPanel = NewInsightsModel(ins, m.issueMap, m.theme)
			m.insightsPanel.topPicks = oldTopPicks
			m.insightsPanel.recommendations = oldRecs
			m.insightsPanel.recommendationMap = oldRecMap
			m.insightsPanel.triageDataHash = oldHash
			bodyHeight := m.height - 1
			if bodyHeight < 5 {
				bodyHeight = 5
			}
			m.insightsPanel.SetSize(m.width, bodyHeight)
		}
		if m.showAttentionView {
			var attentionStart time.Time
			if profileRefresh {
				attentionStart = time.Now()
			}
			cfg := analysis.DefaultLabelHealthConfig()
			m.attentionCache = analysis.ComputeLabelAttentionScores(m.issues, cfg, time.Now().UTC())
			m.attentionCached = true
			attText, _ := ComputeAttentionView(m.issues, max(40, m.width-4))
			m.insightsPanel = NewInsightsModel(analysis.Insights{}, m.issueMap, m.theme)
			m.insightsPanel.labelAttention = m.attentionCache.Labels
			m.insightsPanel.extraText = attText
			panelHeight := m.height - 2
			if panelHeight < 3 {
				panelHeight = 3
			}
			m.insightsPanel.SetSize(m.width, panelHeight)
			if profileRefresh {
				recordTiming("attention_view", time.Since(attentionStart))
			}
		}
		if needsGraph || m.isBoardView {
			var graphStart time.Time
			if profileRefresh {
				graphStart = time.Now()
			}
			m.refreshBoardAndGraphForCurrentFilter()
			if profileRefresh {
				recordTiming("board_graph", time.Since(graphStart))
			}
		}

		// Re-apply recipe filter if active
		if m.activeRecipe != nil {
			m.applyRecipe(m.activeRecipe)
		}

		// Reload sprints (bv-161)
		if m.beadsPath != "" {
			beadsDir := filepath.Dir(m.beadsPath)
			if loaded, err := loader.LoadSprintsFromFile(filepath.Join(beadsDir, loader.SprintsFileName)); err == nil {
				m.sprints = loaded
				// If we have a selected sprint, try to refresh it
				if m.selectedSprint != nil {
					found := false
					for i := range m.sprints {
						if m.sprints[i].ID == m.selectedSprint.ID {
							m.selectedSprint = &m.sprints[i]
							m.sprintViewText = m.renderSprintDashboard()
							found = true
							break
						}
					}
					if !found {
						m.selectedSprint = nil
						m.sprintViewText = "Sprint not found"
					}
				}
			}
		}

		// Keep semantic index current when enabled.
		if m.semanticSearchEnabled && !m.semanticIndexBuilding {
			m.semanticIndexBuilding = true
			cmds = append(cmds, BuildSemanticIndexCmd(m.issuesForAsync()))
		}

		if cacheHit {
			m.statusMsg = fmt.Sprintf("Reloaded %d issues (cached)", len(newIssues))
		} else {
			m.statusMsg = fmt.Sprintf("Reloaded %d issues", len(newIssues))
		}
		if len(reloadWarnings) > 0 {
			m.statusMsg += fmt.Sprintf(" (%d warnings)", len(reloadWarnings))
		}
		reloadDuration := time.Since(reloadStart)
		if profileRefresh {
			recordTiming("total", reloadDuration)
		}
		if reloadDuration >= 500*time.Millisecond {
			m.statusMsg += fmt.Sprintf(" in %s", formatReloadDuration(reloadDuration))
		}
		if profileRefresh && len(refreshTimings) > 0 {
			addTiming := func(label, key string) {
				if d, ok := refreshTimings[key]; ok && d > 0 {
					m.statusMsg += fmt.Sprintf(" %s=%s", label, formatReloadDuration(d))
				}
			}
			m.statusMsg += " [debug"
			addTiming("load", "load_issues")
			addTiming("sort", "sort_issues")
			addTiming("phase1", "phase1_setup")
			addTiming("alerts", "alerts")
			addTiming("list", "list_items")
			addTiming("graph", "board_graph")
			addTiming("total", "total")
			m.statusMsg += "]"
		}
		// Auto-enable background mode after slow sync reloads (opt-out via BV_BACKGROUND_MODE=0).
		autoEnabled := false
		slowReload := reloadDuration >= time.Second
		if slowReload && m.backgroundWorker == nil && m.beadsPath != "" {
			autoAllowed := true
			if v := strings.TrimSpace(os.Getenv("BV_BACKGROUND_MODE")); v != "" {
				switch strings.ToLower(v) {
				case "0", "false", "no", "off":
					autoAllowed = false
				}
			}
			if autoAllowed {
				bw, err := NewBackgroundWorker(WorkerConfig{
					BeadsPath:     m.beadsPath,
					DebounceDelay: 200 * time.Millisecond,
				})
				if err == nil {
					if m.watcher != nil {
						m.watcher.Stop()
					}
					m.watcher = nil
					m.backgroundWorker = bw
					m.snapshotInitPending = true
					autoEnabled = true
					cmds = append(cmds, StartBackgroundWorkerCmd(m.backgroundWorker))
					cmds = append(cmds, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker))
				} else {
					m.statusMsg += fmt.Sprintf("; background mode unavailable: %v", err)
				}
			}
		}
		if slowReload {
			if autoEnabled {
				m.statusMsg += "; background mode auto-enabled"
			} else {
				m.statusMsg += "; consider BV_BACKGROUND_MODE=1"
			}
		}
		m.statusIsError = false
		// Invalidate label-derived caches
		m.labelHealthCached = false
		m.labelDrilldownCache = make(map[string][]model.Issue)
		m.updateViewportContent()

		// Re-start watching for next change + wait for Phase 2
		if m.watcher != nil && !autoEnabled {
			cmds = append(cmds, WatchFileCmd(m.watcher))
		}
		cmds = append(cmds, WaitForPhase2Cmd(m.analysis))
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Clear status message on any keypress
		m.statusMsg = ""
		m.statusIsError = false

		// Handle AGENTS.md prompt modal (bv-i8dk)
		if m.showAgentPrompt {
			m.agentPromptModal, cmd = m.agentPromptModal.Update(msg)
			cmds = append(cmds, cmd)

			// Check if user made a decision
			switch m.agentPromptModal.Result() {
			case AgentPromptAccept:
				// User accepted - add blurb to file
				filePath := m.agentPromptModal.FilePath()
				if err := agents.AppendBlurbToFile(filePath); err != nil {
					m.statusMsg = "Failed to update " + filepath.Base(filePath) + ": " + err.Error()
					m.statusIsError = true
				} else {
					m.statusMsg = "✓ Added beads instructions to " + filepath.Base(filePath)
					// Record acceptance
					_ = agents.RecordAccept(m.workDir)
				}
				m.showAgentPrompt = false
				m.focused = focusList
			case AgentPromptDecline:
				// User declined - just dismiss, may ask again next time
				m.showAgentPrompt = false
				m.focused = focusList
			case AgentPromptNeverAsk:
				// User chose "don't ask again" - save preference
				_ = agents.RecordDecline(m.workDir, true)
				m.showAgentPrompt = false
				m.focused = focusList
			}
			return m, tea.Batch(cmds...)
		}

		// Handle cass session modal (bv-5bqh)
		if m.showCassModal {
			m.cassModal, cmd = m.cassModal.Update(msg)
			cmds = append(cmds, cmd)

			// Check for dismiss keys
			switch msg.String() {
			case "V", "esc", "enter", "q":
				m.showCassModal = false
				m.focused = focusList
				return m, tea.Batch(cmds...)
			}
			return m, tea.Batch(cmds...)
		}

		// Handle self-update modal (bv-182)
		if m.showUpdateModal {
			m.updateModal, cmd = m.updateModal.Update(msg)
			cmds = append(cmds, cmd)

			// Handle modal state changes
			switch msg.String() {
			case "esc", "q":
				// Always allow escape to close
				if !m.updateModal.IsInProgress() {
					m.showUpdateModal = false
					m.focused = focusList
					return m, tea.Batch(cmds...)
				}
			case "enter":
				// Close on enter if complete or if cancelled
				if m.updateModal.IsComplete() {
					m.showUpdateModal = false
					m.focused = focusList
					return m, tea.Batch(cmds...)
				}
				// If confirming and cancelled, close
				if m.updateModal.IsConfirming() && m.updateModal.IsCancelled() {
					m.showUpdateModal = false
					m.focused = focusList
					return m, tea.Batch(cmds...)
				}
			case "n", "N":
				// Quick cancel
				if m.updateModal.IsConfirming() {
					m.showUpdateModal = false
					m.focused = focusList
					return m, tea.Batch(cmds...)
				}
			}
			return m, tea.Batch(cmds...)
		}

		// Close label health detail modal if open
		if m.showLabelHealthDetail {
			s := msg.String()
			if s == "esc" || s == "q" || s == "enter" || s == "h" {
				m.showLabelHealthDetail = false
				m.labelHealthDetail = nil
				return m, nil
			}
			if s == "d" && m.labelHealthDetail != nil {
				// open drilldown from detail modal
				m.labelDrilldownLabel = m.labelHealthDetail.Label
				m.labelDrilldownIssues = m.filterIssuesByLabel(m.labelDrilldownLabel)
				m.showLabelDrilldown = true
				m.showLabelHealthDetail = false
				return m, nil
			}
		}

		// Handle label drilldown modal if open
		if m.showLabelDrilldown {
			s := msg.String()
			switch s {
			case "enter":
				// Apply label filter to main list and close drilldown
				if m.labelDrilldownLabel != "" {
					m.currentFilter = "label:" + m.labelDrilldownLabel
					m.applyFilter()
					m.focused = focusList
				}
				m.showLabelDrilldown = false
				m.labelDrilldownLabel = ""
				m.labelDrilldownIssues = nil
				return m, nil
			case "g":
				// Show graph analysis sub-view (bv-109)
				if m.labelDrilldownLabel != "" {
					sg := analysis.ComputeLabelSubgraph(m.issues, m.labelDrilldownLabel)
					pr := analysis.ComputeLabelPageRank(sg)
					cp := analysis.ComputeLabelCriticalPath(sg)
					m.labelGraphAnalysisResult = &LabelGraphAnalysisResult{
						Label:        m.labelDrilldownLabel,
						Subgraph:     sg,
						PageRank:     pr,
						CriticalPath: cp,
					}
					m.showLabelGraphAnalysis = true
				}
				return m, nil
			case "esc", "q", "d":
				m.showLabelDrilldown = false
				m.labelDrilldownLabel = ""
				m.labelDrilldownIssues = nil
				return m, nil
			}
		}

		// Handle label graph analysis sub-view (bv-109)
		if m.showLabelGraphAnalysis {
			s := msg.String()
			switch s {
			case "esc", "q", "g":
				m.showLabelGraphAnalysis = false
				m.labelGraphAnalysisResult = nil
				return m, nil
			}
		}

		// Handle attention view quick jumps (bv-117)
		if m.showAttentionView {
			s := msg.String()
			switch {
			case s == "esc" || s == "q" || s == "d":
				m.showAttentionView = false
				m.insightsPanel.extraText = ""
				return m, nil
			case len(s) == 1 && s[0] >= '1' && s[0] <= '9':
				if len(m.attentionCache.Labels) == 0 {
					return m, nil
				}
				idx := int(s[0] - '1')
				if idx >= 0 && idx < len(m.attentionCache.Labels) {
					label := m.attentionCache.Labels[idx].Label
					m.currentFilter = "label:" + label
					m.applyFilter()
					m.statusMsg = fmt.Sprintf("Filtered to label %s (attention #%d)", label, idx+1)
					m.statusIsError = false
				}
				return m, nil
			}
		}

		// Handle alerts panel modal if open (bv-168)
		if m.showAlertsPanel {
			// Build list of active (non-dismissed) alerts
			var activeAlerts []drift.Alert
			for _, a := range m.alerts {
				if !m.dismissedAlerts[alertKey(a)] {
					activeAlerts = append(activeAlerts, a)
				}
			}
			s := msg.String()
			switch s {
			case "j", "down":
				if m.alertsCursor < len(activeAlerts)-1 {
					m.alertsCursor++
				}
				return m, nil
			case "k", "up":
				if m.alertsCursor > 0 {
					m.alertsCursor--
				}
				return m, nil
			case "enter":
				// Jump to the issue referenced by the selected alert
				if m.alertsCursor < len(activeAlerts) {
					issueID := activeAlerts[m.alertsCursor].IssueID
					if issueID != "" {
						// Find the issue in the list and select it
						for i, item := range m.list.Items() {
							if it, ok := item.(IssueItem); ok && it.Issue.ID == issueID {
								m.list.Select(i)
								break
							}
						}
					}
				}
				m.showAlertsPanel = false
				return m, nil
			case "d":
				// Dismiss the selected alert
				if m.alertsCursor < len(activeAlerts) {
					key := alertKey(activeAlerts[m.alertsCursor])
					m.dismissedAlerts[key] = true
					// Adjust cursor if needed
					remaining := 0
					for _, a := range m.alerts {
						if !m.dismissedAlerts[alertKey(a)] {
							remaining++
						}
					}
					if m.alertsCursor >= remaining {
						m.alertsCursor = remaining - 1
					}
					if m.alertsCursor < 0 {
						m.alertsCursor = 0
					}
					// Close panel if no alerts left
					if remaining == 0 {
						m.showAlertsPanel = false
					}
				}
				return m, nil
			case "esc", "q", "!":
				m.showAlertsPanel = false
				return m, nil
			}
			return m, nil
		}

		// Handle repo picker overlay (workspace mode) before global keys (esc/q/etc.)
		if m.showRepoPicker {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m = m.handleRepoPickerKeys(msg)
			return m, nil
		}

		// Handle recipe picker overlay before global keys (esc/q/etc.)
		if m.showRecipePicker {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m = m.handleRecipePickerKeys(msg)
			return m, nil
		}

		// Handle quit confirmation first
		if m.showQuitConfirm {
			switch msg.String() {
			case "esc", "y", "Y":
				return m, tea.Quit
			default:
				m.showQuitConfirm = false
				m.focused = focusList
				return m, nil
			}
		}

		// Handle help overlay toggle (? or F1)
		if (msg.String() == "?" || msg.String() == "f1") && m.list.FilterState() != list.Filtering {
			m.showHelp = !m.showHelp
			if m.showHelp {
				m.focusBeforeHelp = m.focused // Store current focus before switching to help
				m.focused = focusHelp
				m.helpScroll = 0 // Reset scroll position when opening help
			} else {
				m.focused = m.restoreFocusFromHelp()
			}
			return m, nil
		}

		// Handle tutorial toggle (backtick `) - bv-8y31
		if msg.String() == "`" && m.list.FilterState() != list.Filtering {
			m.showTutorial = !m.showTutorial
			if m.showTutorial {
				m.showHelp = false // Close help if open
				m.tutorialModel.SetSize(m.width, m.height)
				m.focused = focusTutorial
			} else {
				m.focused = focusList
			}
			return m, nil
		}

		// Force refresh (bv-4auz): Ctrl+R / F5 triggers an immediate reload.
		if (msg.String() == "ctrl+r" || msg.String() == "f5") && m.list.FilterState() != list.Filtering {
			now := time.Now()
			if !m.lastForceRefresh.IsZero() && now.Sub(m.lastForceRefresh) < time.Second {
				return m, nil
			}
			m.lastForceRefresh = now

			m.statusMsg = "Refreshing…"
			m.statusIsError = false

			if m.backgroundWorker != nil {
				m.backgroundWorker.ForceRefresh()
				cmds = append(cmds, WaitForBackgroundWorkerMsgCmd(m.backgroundWorker))
				return m, tea.Batch(cmds...)
			}

			if m.beadsPath == "" && m.watcher == nil {
				m.statusMsg = "Refresh unavailable"
				m.statusIsError = true
				return m, nil
			}

			cmds = append(cmds, func() tea.Msg { return FileChangedMsg{} })
			return m, tea.Batch(cmds...)
		}

		// Handle shortcuts sidebar toggle (; or F2) - bv-3qi5
		if (msg.String() == ";" || msg.String() == "f2") && m.list.FilterState() != list.Filtering {
			m.showShortcutsSidebar = !m.showShortcutsSidebar
			if m.showShortcutsSidebar {
				m.shortcutsSidebar.ResetScroll()
				m.statusMsg = "Shortcuts sidebar: ; hide | ctrl+j/k scroll"
				m.statusIsError = false
			} else {
				m.statusMsg = ""
			}
			return m, nil
		}

		// Handle shortcuts sidebar scrolling (Ctrl+j/k when sidebar visible) - bv-3qi5
		if m.showShortcutsSidebar && m.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "ctrl+j":
				m.shortcutsSidebar.ScrollDown()
				return m, nil
			case "ctrl+k":
				m.shortcutsSidebar.ScrollUp()
				return m, nil
			}
		}

		// Hybrid search toggle/preset cycle (bv-xbar.6)
		if m.focused == focusList && m.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "H":
				m.statusIsError = false
				m.semanticHybridEnabled = !m.semanticHybridEnabled
				if m.semanticSearch == nil {
					m.semanticHybridEnabled = false
					m.statusMsg = "Hybrid search unavailable"
					m.statusIsError = true
					return m, nil
				}
				m.semanticSearch.SetHybridConfig(m.semanticHybridEnabled, m.semanticHybridPreset)
				m.semanticSearch.ResetCache()
				m.clearSemanticScores()
				if m.semanticHybridEnabled && !m.semanticHybridReady && !m.semanticHybridBuilding {
					m.semanticHybridBuilding = true
					m.statusMsg = "Hybrid search: computing metrics…"
					cmds = append(cmds, BuildHybridMetricsCmd(m.issuesForAsync()))
				} else if m.semanticHybridEnabled {
					m.statusMsg = fmt.Sprintf("Hybrid search enabled (%s)", m.semanticHybridPreset)
				} else {
					m.statusMsg = "Semantic search: text-only"
				}
				if m.semanticSearchEnabled && m.list.FilterState() != list.Unfiltered {
					currentTerm := m.list.FilterInput.Value()
					if currentTerm != "" && !m.semanticHybridBuilding {
						cmds = append(cmds, ComputeSemanticFilterCmd(m.semanticSearch, currentTerm))
					}
				}
				m.updateListDelegate()
				return m, tea.Batch(cmds...)
			case "alt+h", "alt+H":
				m.statusIsError = false
				m.semanticHybridPreset = nextHybridPreset(m.semanticHybridPreset)
				if m.semanticSearch != nil {
					m.semanticSearch.SetHybridConfig(m.semanticHybridEnabled, m.semanticHybridPreset)
					m.semanticSearch.ResetCache()
				}
				m.clearSemanticScores()
				if m.semanticHybridEnabled {
					m.statusMsg = fmt.Sprintf("Hybrid preset: %s", m.semanticHybridPreset)
				} else {
					m.statusMsg = fmt.Sprintf("Hybrid preset set (%s)", m.semanticHybridPreset)
				}
				if m.semanticSearchEnabled && m.semanticHybridEnabled && m.list.FilterState() != list.Unfiltered {
					currentTerm := m.list.FilterInput.Value()
					if currentTerm != "" && !m.semanticHybridBuilding {
						cmds = append(cmds, ComputeSemanticFilterCmd(m.semanticSearch, currentTerm))
					}
				}
				m.updateListDelegate()
				return m, tea.Batch(cmds...)
			}
		}

		// Semantic search toggle (bv-9gf.3)
		if msg.String() == "ctrl+s" && m.focused == focusList {
			m.statusIsError = false
			m.semanticSearchEnabled = !m.semanticSearchEnabled
			if m.semanticSearchEnabled {
				if m.semanticSearch != nil {
					m.list.Filter = m.semanticSearch.Filter
					if !m.semanticSearch.Snapshot().Ready && !m.semanticIndexBuilding {
						m.semanticIndexBuilding = true
						m.statusMsg = "Semantic search: building index…"
						cmds = append(cmds, BuildSemanticIndexCmd(m.issuesForAsync()))
					} else if !m.semanticSearch.Snapshot().Ready && m.semanticIndexBuilding {
						m.statusMsg = "Semantic search: indexing…"
					} else {
						m.statusMsg = "Semantic search enabled"
					}
				} else {
					m.semanticSearchEnabled = false
					m.list.Filter = list.DefaultFilter
					m.statusMsg = "Semantic search unavailable"
					m.statusIsError = true
				}
				if m.semanticHybridEnabled && !m.semanticHybridReady && !m.semanticHybridBuilding {
					m.semanticHybridBuilding = true
					cmds = append(cmds, BuildHybridMetricsCmd(m.issuesForAsync()))
				}
			} else {
				m.list.Filter = list.DefaultFilter
				m.statusMsg = "Fuzzy search enabled"
				m.clearSemanticScores()
			}

			// Refresh the current list filter results immediately.
			prevState := m.list.FilterState()
			filterText := m.list.FilterInput.Value()
			if prevState != list.Unfiltered {
				m.list.SetFilterText(filterText)
				if prevState == list.Filtering {
					m.list.SetFilterState(list.Filtering)
				}
			}

			m.updateListDelegate()
			return m, tea.Batch(cmds...)
		}

		// If help is showing, handle navigation keys for scrolling
		if m.focused == focusHelp {
			m = m.handleHelpKeys(msg)
			return m, nil
		}

		// If tutorial is showing, route input to tutorial model (bv-8y31)
		if m.focused == focusTutorial && m.showTutorial {
			var tutorialCmd tea.Cmd
			m.tutorialModel, tutorialCmd = m.tutorialModel.Update(msg)
			// Check if tutorial wants to close
			if m.tutorialModel.ShouldClose() {
				m.showTutorial = false
				m.focused = focusList
				m.tutorialModel = NewTutorialModel(m.theme) // Reset for next time
			}
			return m, tutorialCmd
		}

		// Handle time-travel input first (before global keys intercept letters)
		// But allow ctrl+c to always quit
		if m.focused == focusTimeTravelInput {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m = m.handleTimeTravelInputKeys(msg)
			return m, nil
		}

		// Handle keys when not filtering
		if m.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit

			case "q":
				// q closes current view or quits if at top level
				if m.showDetails && !m.isSplitView {
					m.showDetails = false
					m.focused = focusList
					return m, nil
				}
				if m.focused == focusInsights {
					m.focused = focusList
					return m, nil
				}
				if m.focused == focusFlowMatrix {
					if m.flowMatrix.showDrilldown {
						m.flowMatrix.showDrilldown = false
						return m, nil
					}
					m.focused = focusList
					return m, nil
				}
				if m.isGraphView {
					m.isGraphView = false
					m.focused = focusList
					return m, nil
				}
				if m.isBoardView {
					m.isBoardView = false
					m.focused = focusList
					return m, nil
				}
				return m, tea.Quit

			case "esc":
				// Escape closes modals and goes back
				if m.showDetails && !m.isSplitView {
					m.showDetails = false
					m.focused = focusList
					return m, nil
				}
				if m.focused == focusInsights {
					m.focused = focusList
					return m, nil
				}
				if m.focused == focusFlowMatrix {
					if m.flowMatrix.showDrilldown {
						m.flowMatrix.showDrilldown = false
						return m, nil
					}
					m.focused = focusList
					return m, nil
				}
				if m.isGraphView {
					m.isGraphView = false
					m.focused = focusList
					return m, nil
				}
				if m.isBoardView {
					m.isBoardView = false
					m.focused = focusList
					return m, nil
				}
				if m.isActionableView {
					m.isActionableView = false
					m.focused = focusList
					return m, nil
				}
				if m.isHistoryView {
					m.isHistoryView = false
					m.focused = focusList
					return m, nil
				}
				// Close label picker if open (bv-126 fix)
				if m.showLabelPicker {
					m.showLabelPicker = false
					m.focused = focusList
					return m, nil
				}
				// Close label dashboard if open
				if m.focused == focusLabelDashboard {
					m.focused = focusList
					return m, nil
				}
				// At main list - first ESC clears filters, second shows quit confirm
				if m.hasActiveFilters() {
					m.clearAllFilters()
					return m, nil
				}
				// No filters active - show quit confirmation
				m.showQuitConfirm = true
				m.focused = focusQuitConfirm
				return m, nil

			case "tab":
				if m.isSplitView && !m.isBoardView {
					if m.focused == focusList {
						m.focused = focusDetail
					} else {
						m.focused = focusList
					}
				}

			case "<":
				// Shrink list pane (move divider left)
				if m.isSplitView {
					m.splitPaneRatio -= 0.05
					if m.splitPaneRatio < 0.2 {
						m.splitPaneRatio = 0.2
					}
					m.recalculateSplitPaneSizes()
				}

			case ">":
				// Expand list pane (move divider right)
				if m.isSplitView {
					m.splitPaneRatio += 0.05
					if m.splitPaneRatio > 0.8 {
						m.splitPaneRatio = 0.8
					}
					m.recalculateSplitPaneSizes()
				}

			case "b":
				m.clearAttentionOverlay()
				m.isBoardView = !m.isBoardView
				m.isGraphView = false
				m.isActionableView = false
				m.isHistoryView = false
				if m.isBoardView {
					m.focused = focusBoard
					m.refreshBoardAndGraphForCurrentFilter()
				} else {
					m.focused = focusList
				}
				return m, nil

			case "g":
				// Toggle graph view
				m.clearAttentionOverlay()
				m.isGraphView = !m.isGraphView
				m.isBoardView = false
				m.isActionableView = false
				m.isHistoryView = false
				if m.isGraphView {
					m.focused = focusGraph
					m.refreshBoardAndGraphForCurrentFilter()
				} else {
					m.focused = focusList
				}
				return m, nil

			case "a":
				// Toggle actionable view
				m.clearAttentionOverlay()
				m.isActionableView = !m.isActionableView
				m.isGraphView = false
				m.isBoardView = false
				m.isHistoryView = false
				if m.isActionableView {
					// Build execution plan
					analyzer := analysis.NewAnalyzer(m.issues)
					plan := analyzer.GetExecutionPlan()
					m.actionableView = NewActionableModel(plan, m.theme)
					m.actionableView.SetSize(m.width, m.height-2)
					m.focused = focusActionable
				} else {
					m.focused = focusList
				}
				return m, nil

			case "E":
				// Toggle hierarchical tree view (bv-gllx)
				m.clearAttentionOverlay()
				if m.focused == focusTree {
					m.focused = focusList
				} else {
					m.isGraphView = false
					m.isBoardView = false
					m.isActionableView = false
					m.isHistoryView = false
					// Build tree from snapshot when available (bv-t435)
					if m.snapshot != nil {
						m.tree.BuildFromSnapshot(m.snapshot)
					} else {
						m.tree.Build(m.issues)
					}
					m.tree.SetSize(m.width, m.height-2)
					m.focused = focusTree
				}
				return m, nil

			case "i":
				m.clearAttentionOverlay()
				if m.focused == focusInsights {
					m.focused = focusList
				} else {
					m.isGraphView = false
					m.isBoardView = false
					m.isActionableView = false
					m.isHistoryView = false
					m.focused = focusInsights
					// Refresh insights using the current snapshot when available (bv-mpqz).
					var ins analysis.Insights
					hasInsights := false
					if m.snapshot != nil {
						ins = m.snapshot.Insights
						hasInsights = true
					} else if m.analysis != nil {
						ins = m.analysis.GenerateInsights(len(m.issues))
						hasInsights = true
					}
					if hasInsights {
						m.insightsPanel = NewInsightsModel(ins, m.issueMap, m.theme)
						// Include priority triage (bv-91) - reuse existing analyzer/stats (bv-runn.12)
						triage := analysis.ComputeTriageFromAnalyzer(m.analyzer, m.analysis, m.issues, analysis.TriageOptions{}, time.Now())
						m.insightsPanel.SetTopPicks(triage.QuickRef.TopPicks)
						// Set full recommendations with breakdown for priority radar (bv-93)
						dataHash := fmt.Sprintf("v%s@%s#%d", triage.Meta.Version, triage.Meta.GeneratedAt.Format("15:04:05"), triage.Meta.IssueCount)
						m.insightsPanel.SetRecommendations(triage.Recommendations, dataHash)
						panelHeight := m.height - 2
						if panelHeight < 3 {
							panelHeight = 3
						}
						m.insightsPanel.SetSize(m.width, panelHeight)
					}
				}
				return m, nil

			case "p":
				// Toggle priority hints
				m.showPriorityHints = !m.showPriorityHints
				// Update delegate with new state
				m.updateListDelegate()
				// Show explanatory status message
				if m.showPriorityHints {
					count := len(m.priorityHints)
					if count > 0 {
						m.statusMsg = fmt.Sprintf("Priority hints: ↑ increase ↓ decrease (%d suggestions)", count)
					} else {
						m.statusMsg = "Priority hints: No misalignments detected (analysis ongoing)"
					}
				} else {
					m.statusMsg = ""
				}
				return m, nil

			case "h":
				// Toggle history view
				m.clearAttentionOverlay()
				m.isHistoryView = !m.isHistoryView
				m.isGraphView = false
				m.isBoardView = false
				m.isActionableView = false
				if m.isHistoryView {
					// Ensure history model has latest sizing
					bodyHeight := m.height - 1
					if bodyHeight < 5 {
						bodyHeight = 5
					}
					m.historyView.SetSize(m.width, bodyHeight)
					m.focused = focusHistory
				} else {
					m.focused = focusList
				}
				return m, nil

			case "[", "f3":
				// Open label dashboard (phase 1: table view)
				m.clearAttentionOverlay()
				m.isGraphView = false
				m.isBoardView = false
				m.isActionableView = false
				m.isHistoryView = false
				m.focused = focusLabelDashboard
				// Compute label health (fast; phase1 metrics only needed) with caching
				if !m.labelHealthCached {
					cfg := analysis.DefaultLabelHealthConfig()
					m.labelHealthCache = analysis.ComputeAllLabelHealth(m.issues, cfg, time.Now().UTC(), m.analysis)
					m.labelHealthCached = true
				}
				m.labelDashboard.SetData(m.labelHealthCache.Labels)
				m.labelDashboard.SetSize(m.width, m.height-1)
				m.statusMsg = fmt.Sprintf("Labels: %d total • critical %d • warning %d", m.labelHealthCache.TotalLabels, m.labelHealthCache.CriticalCount, m.labelHealthCache.WarningCount)
				m.statusIsError = false
				return m, nil

			case "]", "f4":
				// Attention view: compute attention scores (cached) and render as text
				if !m.attentionCached {
					cfg := analysis.DefaultLabelHealthConfig()
					m.attentionCache = analysis.ComputeLabelAttentionScores(m.issues, cfg, time.Now().UTC())
					m.attentionCached = true
				}
				attText, _ := ComputeAttentionView(m.issues, max(40, m.width-4))
				m.isGraphView = false
				m.isBoardView = false
				m.isActionableView = false
				m.isHistoryView = false
				m.focused = focusInsights
				m.showAttentionView = true
				m.insightsPanel = NewInsightsModel(analysis.Insights{}, m.issueMap, m.theme)
				m.insightsPanel.labelAttention = m.attentionCache.Labels
				m.insightsPanel.extraText = attText
				panelHeight := m.height - 2
				if panelHeight < 3 {
					panelHeight = 3
				}
				m.insightsPanel.SetSize(m.width, panelHeight)
				return m, nil

			case "f":
				// Flow matrix view (cross-label dependencies)
				m.clearAttentionOverlay()
				cfg := analysis.DefaultLabelHealthConfig()
				flow := analysis.ComputeCrossLabelFlow(m.issues, cfg)
				m.isGraphView = false
				m.isBoardView = false
				m.isActionableView = false
				m.isHistoryView = false
				m.focused = focusFlowMatrix
				m.flowMatrix = NewFlowMatrixModel(m.theme)
				m.flowMatrix.SetData(&flow, m.issues)
				panelHeight := m.height - 2
				if panelHeight < 3 {
					panelHeight = 3
				}
				m.flowMatrix.SetSize(m.width, panelHeight)
				return m, nil

			case "!":
				// Toggle alerts panel (bv-168)
				// Only show if there are active alerts
				activeCount := 0
				for _, a := range m.alerts {
					if !m.dismissedAlerts[alertKey(a)] {
						activeCount++
					}
				}
				if activeCount > 0 {
					m.showAlertsPanel = !m.showAlertsPanel
					m.alertsCursor = 0 // Reset cursor when opening
				} else {
					m.statusMsg = "No active alerts"
					m.statusIsError = false
				}
				return m, nil

			case "'":
				// Toggle recipe picker overlay
				m.showRecipePicker = !m.showRecipePicker
				if m.showRecipePicker {
					m.recipePicker.SetSize(m.width, m.height-1)
					m.focused = focusRecipePicker
				} else {
					m.focused = focusList
				}
				return m, nil

			case "w":
				// Toggle repo picker overlay (workspace mode)
				if !m.workspaceMode || len(m.availableRepos) == 0 {
					m.statusMsg = "Repo filter available only in workspace mode"
					m.statusIsError = false
					return m, nil
				}
				m.showRepoPicker = !m.showRepoPicker
				if m.showRepoPicker {
					m.repoPicker = NewRepoPickerModel(m.availableRepos, m.theme)
					m.repoPicker.SetActiveRepos(m.activeRepos)
					m.repoPicker.SetSize(m.width, m.height-1)
					m.focused = focusRepoPicker
				} else {
					m.focused = focusList
				}
				return m, nil

			case "x":
				// Export to Markdown file
				m.exportToMarkdown()
				return m, nil

			case "l":
				// Open label picker for quick filter (bv-126)
				if len(m.issues) == 0 {
					return m, nil
				}
				// Update labels in case they changed
				labelExtraction := analysis.ExtractLabels(m.issues)
				labelCounts := extractLabelCounts(labelExtraction.Stats)
				m.labelPicker.SetLabels(labelExtraction.Labels, labelCounts)
				m.labelPicker.Reset()
				m.labelPicker.SetSize(m.width, m.height-1)
				m.showLabelPicker = true
				m.focused = focusLabelPicker
				return m, nil

			}

			// Focus-specific key handling
			switch m.focused {
			case focusRecipePicker:
				m = m.handleRecipePickerKeys(msg)

			case focusRepoPicker:
				m = m.handleRepoPickerKeys(msg)

			case focusLabelPicker:
				m = m.handleLabelPickerKeys(msg)

			case focusInsights:
				m = m.handleInsightsKeys(msg)

			case focusBoard:
				m = m.handleBoardKeys(msg)

			case focusLabelDashboard:
				if selectedLabel, cmd := m.labelDashboard.Update(msg); selectedLabel != "" {
					// Filter list by selected label and jump back to list view
					m.currentFilter = "label:" + selectedLabel
					m.applyFilter()
					m.focused = focusList
					return m, cmd
				}
				// Open detail modal on 'h'
				if msg.String() == "h" && len(m.labelDashboard.labels) > 0 {
					idx := m.labelDashboard.cursor
					if idx >= 0 && idx < len(m.labelDashboard.labels) {
						lh := m.labelDashboard.labels[idx]
						m.showLabelHealthDetail = true
						m.labelHealthDetail = &lh
						// Precompute cross-label flows for this label
						m.labelHealthDetailFlow = m.getCrossFlowsForLabel(lh.Label)
						return m, nil
					}
				}
				// Open drilldown overlay on 'd'
				if msg.String() == "d" && len(m.labelDashboard.labels) > 0 {
					idx := m.labelDashboard.cursor
					if idx >= 0 && idx < len(m.labelDashboard.labels) {
						lh := m.labelDashboard.labels[idx]
						m.labelDrilldownLabel = lh.Label
						m.labelDrilldownIssues = m.filterIssuesByLabel(lh.Label)
						m.showLabelDrilldown = true
						return m, nil
					}
				}

			case focusGraph:
				m = m.handleGraphKeys(msg)

			case focusTree:
				m = m.handleTreeKeys(msg)

			case focusActionable:
				m = m.handleActionableKeys(msg)

			case focusHistory:
				m = m.handleHistoryKeys(msg)

			case focusSprint:
				m = m.handleSprintKeys(msg)

			case focusFlowMatrix:
				m = m.handleFlowMatrixKeys(msg)

			case focusList:
				m = m.handleListKeys(msg)

			case focusDetail:
				m.viewport, cmd = m.viewport.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	case tea.MouseMsg:
		// Handle mouse wheel scrolling
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			// Scroll up based on current focus
			switch m.focused {
			case focusList:
				if m.list.Index() > 0 {
					m.list.Select(m.list.Index() - 1)
					// Sync detail panel in split view mode
					if m.isSplitView {
						m.updateViewportContent()
					}
				}
			case focusDetail:
				m.viewport.ScrollUp(3)
			case focusInsights:
				m.insightsPanel.MoveUp()
			case focusBoard:
				m.board.MoveUp()
			case focusGraph:
				m.graphView.PageUp()
			case focusTree:
				m.tree.MoveUp()
			case focusActionable:
				m.actionableView.MoveUp()
			case focusHistory:
				m.historyView.MoveUp()
			case focusFlowMatrix:
				m.flowMatrix.MoveUp()
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			// Scroll down based on current focus
			switch m.focused {
			case focusList:
				if m.list.Index() < len(m.list.Items())-1 {
					m.list.Select(m.list.Index() + 1)
					// Sync detail panel in split view mode
					if m.isSplitView {
						m.updateViewportContent()
					}
				}
			case focusDetail:
				m.viewport.ScrollDown(3)
			case focusInsights:
				m.insightsPanel.MoveDown()
			case focusBoard:
				m.board.MoveDown()
			case focusGraph:
				m.graphView.PageDown()
			case focusTree:
				m.tree.MoveDown()
			case focusActionable:
				m.actionableView.MoveDown()
			case focusHistory:
				m.historyView.MoveDown()
			case focusFlowMatrix:
				m.flowMatrix.MoveDown()
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.isSplitView = msg.Width > SplitViewThreshold
		m.ready = true
		bodyHeight := m.height - 1 // keep 1 row for footer
		if bodyHeight < 5 {
			bodyHeight = 5
		}

		if m.isSplitView {
			// Calculate dimensions accounting for 2 panels with borders(2)+padding(2) = 4 overhead each
			// Total overhead = 8
			availWidth := msg.Width - 8
			if availWidth < 10 {
				availWidth = 10
			}

			// Use configurable split ratio (default 0.4, adjustable via [ and ])
			listInnerWidth := int(float64(availWidth) * m.splitPaneRatio)
			detailInnerWidth := availWidth - listInnerWidth

			// listHeight fits header (1) + page line (1) inside a panel with Border (2)
			listHeight := bodyHeight - 4
			if listHeight < 3 {
				listHeight = 3
			}

			m.list.SetSize(listInnerWidth, listHeight)
			m.viewport = viewport.New(detailInnerWidth, bodyHeight-2) // Account for border

			m.renderer.SetWidthWithTheme(detailInnerWidth, m.theme)
		} else {
			listHeight := bodyHeight - 2
			if listHeight < 3 {
				listHeight = 3
			}
			m.list.SetSize(msg.Width, listHeight)
			m.viewport = viewport.New(msg.Width, bodyHeight-1)

			// Update renderer for full width
			m.renderer.SetWidthWithTheme(msg.Width, m.theme)
		}

		m.updateListDelegate()

		// Resize label dashboard table and modal overlay sizing
		m.labelDashboard.SetSize(m.width, bodyHeight)

		m.insightsPanel.SetSize(m.width, bodyHeight)
		m.updateViewportContent()
	}

	// Update list for navigation, but NOT for WindowSizeMsg
	// (we handle sizing ourselves to account for header/footer)
	// Only forward keyboard messages to list when list has focus (bv-hmkz fix)
	// This prevents j/k keys in detail view from changing list selection
	if m.focused == focusList {
		if _, isWindowSize := msg.(tea.WindowSizeMsg); !isWindowSize {
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)
		}
		currentTerm := m.list.FilterInput.Value()
		if currentTerm != m.lastSearchTerm {
			m.lastSearchTerm = currentTerm
			if m.semanticSearchEnabled {
				m.clearSemanticScores()
			}
		}
		if m.semanticSearchEnabled && m.semanticHybridEnabled && m.list.FilterState() != list.Unfiltered {
			if strings.TrimSpace(currentTerm) != "" {
				m.applySemanticScores(currentTerm)
			}
		}
		m.updateListDelegate()
	}

	// Update viewport if list selection changed in split view
	if m.isSplitView && m.focused == focusList {
		m.updateViewportContent()
	}

	// Trigger async semantic computation if needed (debounced)
	if m.semanticSearchEnabled && m.semanticSearch != nil && m.list.FilterState() != list.Unfiltered {
		pendingTerm := m.semanticSearch.GetPendingTerm()
		if pendingTerm != "" {
			// Debounce: only compute if 150ms since last query change
			if time.Since(m.semanticSearch.GetLastQueryTime()) >= 150*time.Millisecond {
				cmds = append(cmds, ComputeSemanticFilterCmd(m.semanticSearch, pendingTerm))
			} else {
				// Schedule a tick to check again after debounce period
				cmds = append(cmds, tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
					return semanticDebounceTickMsg{}
				}))
			}
		}
	}

	return m, tea.Batch(cmds...)
}

// handleBoardKeys handles keyboard input when the board is focused (bv-yg39)
func (m Model) handleBoardKeys(msg tea.KeyMsg) Model {
	key := msg.String()

	// ═══════════════════════════════════════════════════════════════════════════
	// Search mode input handling (bv-yg39)
	// ═══════════════════════════════════════════════════════════════════════════
	if m.board.IsSearchMode() {
		switch key {
		case "esc":
			m.board.CancelSearch()
		case "enter":
			// Keep search results but exit search mode
			m.board.FinishSearch()
		case "backspace":
			m.board.BackspaceSearch()
		case "n":
			m.board.NextMatch()
		case "N":
			m.board.PrevMatch()
		default:
			// Append printable characters to search query
			if len(key) == 1 {
				m.board.AppendSearchChar(rune(key[0]))
			}
		}
		return m
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// Vim 'gg' combo handling (bv-yg39)
	// ═══════════════════════════════════════════════════════════════════════════
	if m.board.IsWaitingForG() {
		m.board.ClearWaitingForG()
		if key == "g" {
			m.board.MoveToTop()
			return m
		}
		// Not a second 'g', fall through to normal handling
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// Normal key handling (bv-yg39 enhanced)
	// ═══════════════════════════════════════════════════════════════════════════
	switch key {
	// Basic navigation (existing)
	case "h", "left":
		m.board.MoveLeft()
	case "l", "right":
		m.board.MoveRight()
	case "j", "down":
		m.board.MoveDown()
	case "k", "up":
		m.board.MoveUp()
	case "home":
		m.board.MoveToTop()
	case "G", "end":
		m.board.MoveToBottom()
	case "ctrl+d":
		m.board.PageDown(m.height / 3)
	case "ctrl+u":
		m.board.PageUp(m.height / 3)

	// Column jumping (bv-yg39)
	case "1":
		m.board.JumpToColumn(ColOpen)
	case "2":
		m.board.JumpToColumn(ColInProgress)
	case "3":
		m.board.JumpToColumn(ColBlocked)
	case "4":
		m.board.JumpToColumn(ColClosed)
	case "H":
		m.board.JumpToFirstColumn()
	case "L":
		m.board.JumpToLastColumn()

	// Vim-style navigation (bv-yg39)
	case "g":
		m.board.SetWaitingForG() // Wait for second 'g'
	case "0":
		m.board.MoveToTop() // First item in column
	case "$":
		m.board.MoveToBottom() // Last item in column

	// Search (bv-yg39)
	case "/":
		m.board.StartSearch()

	// Search navigation when not in search mode (bv-yg39)
	case "n":
		if m.board.SearchMatchCount() > 0 {
			m.board.NextMatch()
		}
	case "N":
		if m.board.SearchMatchCount() > 0 {
			m.board.PrevMatch()
		}

	// Copy ID to clipboard (bv-yg39)
	case "y":
		if selected := m.board.SelectedIssue(); selected != nil {
			if err := clipboard.WriteAll(selected.ID); err != nil {
				m.statusMsg = fmt.Sprintf("❌ Clipboard error: %v", err)
				m.statusIsError = true
			} else {
				m.statusMsg = fmt.Sprintf("📋 Copied %s to clipboard", selected.ID)
				m.statusIsError = false
			}
		}

	// Global filter keys (bv-naov) - consistent with list view
	case "o":
		m.currentFilter = "open"
		m.applyFilter()
		m.statusMsg = "Filter: Open issues"
		m.statusIsError = false
	case "c":
		m.currentFilter = "closed"
		m.applyFilter()
		m.statusMsg = "Filter: Closed issues"
		m.statusIsError = false
	case "r":
		m.currentFilter = "ready"
		m.applyFilter()
		m.statusMsg = "Filter: Ready (no blockers)"
		m.statusIsError = false

	// Swimlane mode cycling (bv-wjs0)
	case "s":
		m.board.CycleSwimLaneMode()
		modeName := m.board.GetSwimLaneModeName()
		m.statusMsg = fmt.Sprintf("🔀 Swimlane: %s", modeName)
		m.statusIsError = false

	// Empty column visibility toggle (bv-tf6j)
	case "e":
		m.board.ToggleEmptyColumns()
		visMode := m.board.GetEmptyColumnVisibilityMode()
		hidden := m.board.HiddenColumnCount()
		if hidden > 0 {
			m.statusMsg = fmt.Sprintf("👁 Empty columns: %s (%d hidden)", visMode, hidden)
		} else {
			m.statusMsg = fmt.Sprintf("👁 Empty columns: %s", visMode)
		}
		m.statusIsError = false

	// Inline card expansion (bv-i3ii)
	case "d":
		m.board.ToggleExpand()
		if m.board.HasExpandedCard() {
			m.statusMsg = "📋 Card expanded (d=collapse, j/k=auto-collapse)"
		} else {
			m.statusMsg = "📋 Card collapsed"
		}
		m.statusIsError = false

	// Detail panel (bv-r6kh)
	case "tab":
		m.board.ToggleDetail()
	case "ctrl+j":
		if m.board.IsDetailShown() {
			m.board.DetailScrollDown(3)
		}
	case "ctrl+k":
		if m.board.IsDetailShown() {
			m.board.DetailScrollUp(3)
		}

	// Exit to detail view
	case "enter":
		if selected := m.board.SelectedIssue(); selected != nil {
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selected.ID {
					m.list.Select(i)
					break
				}
			}
			m.isBoardView = false
			m.focused = focusList
			if m.isSplitView {
				m.focused = focusDetail
			} else {
				m.showDetails = true
				m.focused = focusDetail
				m.viewport.GotoTop()
			}
			m.updateViewportContent()
		}
	}
	return m
}

// handleGraphKeys handles keyboard input when the graph view is focused
func (m Model) handleGraphKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "h", "left":
		m.graphView.MoveLeft()
	case "l", "right":
		m.graphView.MoveRight()
	case "j", "down":
		m.graphView.MoveDown()
	case "k", "up":
		m.graphView.MoveUp()
	case "ctrl+d", "pgdown":
		m.graphView.PageDown()
	case "ctrl+u", "pgup":
		m.graphView.PageUp()
	case "H":
		m.graphView.ScrollLeft()
	case "L":
		m.graphView.ScrollRight()
	case "enter":
		if selected := m.graphView.SelectedIssue(); selected != nil {
			// Find and select in list
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selected.ID {
					m.list.Select(i)
					break
				}
			}
			m.isGraphView = false
			m.focused = focusList
			if m.isSplitView {
				m.focused = focusDetail
			} else {
				m.showDetails = true
				m.focused = focusDetail
				m.viewport.GotoTop()
			}
			m.updateViewportContent()
		}
	}
	return m
}

// handleTreeKeys handles keyboard input when tree view is focused (bv-gllx)
func (m Model) handleTreeKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "j", "down":
		m.tree.MoveDown()
	case "k", "up":
		m.tree.MoveUp()
	case "enter", " ":
		m.tree.ToggleExpand()
	case "h", "left":
		m.tree.CollapseOrJumpToParent()
	case "l", "right":
		m.tree.ExpandOrMoveToChild()
	case "g":
		// Jump to top (vim-style)
		m.tree.JumpToTop()
	case "G":
		m.tree.JumpToBottom()
	case "o":
		m.tree.ExpandAll()
	case "O":
		m.tree.CollapseAll()
	case "ctrl+d", "pgdown":
		m.tree.PageDown()
	case "ctrl+u", "pgup":
		m.tree.PageUp()
	case "E", "esc":
		// Return to list view
		m.focused = focusList
	case "tab":
		// Toggle detail panel (sync selection and jump to detail)
		if m.isSplitView {
			if selected := m.tree.SelectedIssue(); selected != nil {
				// Sync detail panel with tree selection
				for i, item := range m.list.Items() {
					if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selected.ID {
						m.list.Select(i)
						break
					}
				}
				m.updateViewportContent()
				m.focused = focusDetail
			}
		}
	}
	return m
}

// handleActionableKeys handles keyboard input when actionable view is focused
func (m Model) handleActionableKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "j", "down":
		m.actionableView.MoveDown()
	case "k", "up":
		m.actionableView.MoveUp()
	case "enter":
		// Jump to selected issue in list view
		selectedID := m.actionableView.SelectedIssueID()
		if selectedID != "" {
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
			m.isActionableView = false
			m.focused = focusList
			if m.isSplitView {
				m.focused = focusDetail
			} else {
				m.showDetails = true
				m.focused = focusDetail
				m.viewport.GotoTop()
			}
			m.updateViewportContent()
		}
	}
	return m
}

// handleHistoryKeys handles keyboard input when history view is focused
func (m Model) handleHistoryKeys(msg tea.KeyMsg) Model {
	// Handle search input when active (bv-nkrj)
	if m.historyView.IsSearchActive() {
		switch msg.String() {
		case "esc":
			m.historyView.CancelSearch()
			m.statusMsg = "🔍 Search cancelled"
			m.statusIsError = false
			return m
		case "enter":
			// Confirm search (just blur input, keep filter active)
			m.historyView.CancelSearch() // For now, just close search
			return m
		default:
			// Forward to search input
			m.historyView.UpdateSearchInput(msg)
			query := m.historyView.SearchQuery()
			if query != "" {
				m.statusMsg = fmt.Sprintf("🔍 Filtering: %s", query)
			} else {
				m.statusMsg = "🔍 Type to search..."
			}
			m.statusIsError = false
			return m
		}
	}

	// Handle file tree navigation when file tree has focus (bv-190l)
	if m.historyView.FileTreeHasFocus() {
		switch msg.String() {
		case "j", "down":
			m.historyView.MoveDownFileTree()
			return m
		case "k", "up":
			m.historyView.MoveUpFileTree()
			return m
		case "enter", "l":
			// Expand directory or select file for filtering
			node := m.historyView.SelectedFileNode()
			if node != nil {
				if node.IsDir {
					m.historyView.ToggleExpandFile()
				} else {
					m.historyView.SelectFile()
					name := m.historyView.SelectedFileName()
					m.statusMsg = fmt.Sprintf("📁 Filtering by: %s", name)
					m.statusIsError = false
				}
			}
			return m
		case "h":
			// Collapse directory
			m.historyView.CollapseFileNode()
			return m
		case "esc":
			// If filter is active, clear it; otherwise close file tree
			if m.historyView.GetFileFilter() != "" {
				m.historyView.ClearFileFilter()
				m.statusMsg = "📁 File filter cleared"
			} else {
				m.historyView.SetFileTreeFocus(false)
				m.statusMsg = "📁 File tree: press Tab to return focus"
			}
			m.statusIsError = false
			return m
		case "tab":
			// Switch focus away from file tree
			m.historyView.SetFileTreeFocus(false)
			return m
		}
	}

	switch msg.String() {
	case "/":
		// Start search (bv-nkrj)
		m.historyView.StartSearch()
		m.statusMsg = "🔍 Type to search commits, beads, authors..."
		m.statusIsError = false
	case "v":
		// Toggle between Bead mode and Git mode (bv-tl3n)
		m.historyView.ToggleViewMode()
		if m.historyView.IsGitMode() {
			m.statusMsg = "🔀 Git Mode: commits on left, related beads on right"
		} else {
			m.statusMsg = "📦 Bead Mode: beads on left, commits on right"
		}
		m.statusIsError = false
	case "j", "down":
		if m.historyView.IsGitMode() {
			m.historyView.MoveDownGit()
		} else {
			m.historyView.MoveDown()
		}
	case "k", "up":
		if m.historyView.IsGitMode() {
			m.historyView.MoveUpGit()
		} else {
			m.historyView.MoveUp()
		}
	case "J":
		// In git mode: navigate to next related bead; in bead mode: next commit
		if m.historyView.IsGitMode() {
			m.historyView.NextRelatedBead()
		} else {
			m.historyView.NextCommit()
		}
	case "K":
		// In git mode: navigate to prev related bead; in bead mode: prev commit
		if m.historyView.IsGitMode() {
			m.historyView.PrevRelatedBead()
		} else {
			m.historyView.PrevCommit()
		}
	case "tab":
		// Cycle focus: list -> detail -> file tree (if visible) -> list (bv-190l)
		if m.historyView.IsFileTreeVisible() {
			if m.historyView.FileTreeHasFocus() {
				// File tree -> list
				m.historyView.SetFileTreeFocus(false)
			} else if m.historyView.IsDetailFocused() {
				// Detail -> file tree
				m.historyView.SetFileTreeFocus(true)
			} else {
				// List -> detail
				m.historyView.ToggleFocus()
			}
		} else {
			m.historyView.ToggleFocus()
		}
	case "enter":
		// Jump to selected bead in main list
		var selectedID string
		if m.historyView.IsGitMode() {
			selectedID = m.historyView.SelectedRelatedBeadID()
		} else {
			selectedID = m.historyView.SelectedBeadID()
		}
		if selectedID != "" {
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
			m.isHistoryView = false
			m.focused = focusList
			if m.isSplitView {
				m.focused = focusDetail
			} else {
				m.showDetails = true
				m.focused = focusDetail
				m.viewport.GotoTop()
			}
			m.updateViewportContent()
		}
	case "y":
		// Copy selected commit SHA to clipboard
		var sha, shortSHA string
		if m.historyView.IsGitMode() {
			if commit := m.historyView.SelectedGitCommit(); commit != nil {
				sha = commit.SHA
				shortSHA = commit.ShortSHA
			}
		} else {
			if commit := m.historyView.SelectedCommit(); commit != nil {
				sha = commit.SHA
				shortSHA = commit.ShortSHA
			}
		}
		if sha != "" {
			if err := clipboard.WriteAll(sha); err != nil {
				m.statusMsg = fmt.Sprintf("❌ Clipboard error: %v", err)
				m.statusIsError = true
			} else {
				m.statusMsg = fmt.Sprintf("📋 Copied %s to clipboard", shortSHA)
				m.statusIsError = false
			}
		} else {
			m.statusMsg = "❌ No commit selected"
			m.statusIsError = true
		}
	case "c":
		// Cycle confidence threshold (only in bead mode)
		if !m.historyView.IsGitMode() {
			m.historyView.CycleConfidence()
			conf := m.historyView.GetMinConfidence()
			if conf == 0 {
				m.statusMsg = "🔍 Showing all commits"
			} else {
				m.statusMsg = fmt.Sprintf("🔍 Confidence filter: ≥%.0f%%", conf*100)
			}
			m.statusIsError = false
		}
	case "f", "F":
		// Toggle file tree panel (bv-190l)
		m.historyView.ToggleFileTree()
		if m.historyView.IsFileTreeVisible() {
			m.statusMsg = "📁 File tree: j/k navigate, Enter select, Esc close"
		} else {
			m.statusMsg = "📁 File tree hidden"
		}
		m.statusIsError = false
	case "o":
		// Open commit in browser (bv-xf4p)
		var sha string
		if m.historyView.IsGitMode() {
			if commit := m.historyView.SelectedGitCommit(); commit != nil {
				sha = commit.SHA
			}
		} else {
			if commit := m.historyView.SelectedCommit(); commit != nil {
				sha = commit.SHA
			}
		}
		if sha != "" {
			url := m.getCommitURL(sha)
			if url != "" {
				if err := openBrowserURL(url); err != nil {
					m.statusMsg = fmt.Sprintf("❌ Could not open browser: %v", err)
					m.statusIsError = true
				} else {
					// Safely truncate SHA for display (bv-xf4p fix)
					shortSHA := sha
					if len(sha) > 7 {
						shortSHA = sha[:7]
					}
					m.statusMsg = fmt.Sprintf("🌐 Opened %s in browser", shortSHA)
					m.statusIsError = false
				}
			} else {
				m.statusMsg = "❌ No git remote configured"
				m.statusIsError = true
			}
		} else {
			m.statusMsg = "❌ No commit selected"
			m.statusIsError = true
		}
	case "g":
		// Jump to graph view for selected bead (bv-xf4p)
		var selectedID string
		if m.historyView.IsGitMode() {
			selectedID = m.historyView.SelectedRelatedBeadID()
		} else {
			selectedID = m.historyView.SelectedBeadID()
		}
		if selectedID != "" {
			// Find and select the bead in the main list
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
			// Switch to graph view focused on this bead
			m.isHistoryView = false
			m.graphView.SelectByID(selectedID)
			m.focused = focusGraph
			m.statusMsg = fmt.Sprintf("📊 Graph view: %s", selectedID)
			m.statusIsError = false
		} else {
			m.statusMsg = "❌ No bead selected"
			m.statusIsError = true
		}
	case "h", "esc":
		// Exit history view
		m.isHistoryView = false
		m.focused = focusList
	}
	return m
}

// getCommitURL returns the GitHub/GitLab commit URL for a SHA (bv-xf4p)
func (m Model) getCommitURL(sha string) string {
	// Get git remote URL
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = m.workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	remoteURL := strings.TrimSpace(string(output))
	if remoteURL == "" {
		return ""
	}

	// Convert to web URL
	webURL := gitRemoteToWebURL(remoteURL)
	if webURL == "" {
		return ""
	}

	return webURL + "/commit/" + sha
}

// gitRemoteToWebURL converts a git remote URL to a web URL (bv-xf4p)
func gitRemoteToWebURL(remote string) string {
	// Handle SSH URLs: git@github.com:user/repo.git
	if strings.HasPrefix(remote, "git@") {
		// Remove git@ prefix and .git suffix
		remote = strings.TrimPrefix(remote, "git@")
		remote = strings.TrimSuffix(remote, ".git")
		// Replace : with /
		remote = strings.Replace(remote, ":", "/", 1)
		return "https://" + remote
	}

	// Handle HTTPS URLs: https://github.com/user/repo.git
	if strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "http://") {
		remote = strings.TrimSuffix(remote, ".git")
		return remote
	}

	return ""
}

// openBrowserURL opens a URL in the default browser (bv-xf4p)
// Set BV_NO_BROWSER=1 to suppress browser opening (useful for tests).
func openBrowserURL(url string) error {
	// Skip browser opening in test mode or when explicitly disabled
	if os.Getenv("BV_NO_BROWSER") != "" || os.Getenv("BV_TEST_MODE") != "" {
		return nil
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// handleFlowMatrixKeys handles keyboard input when flow matrix view is focused
func (m Model) handleFlowMatrixKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "f", "q", "esc":
		// If in drilldown mode, close drilldown first
		if m.flowMatrix.showDrilldown {
			m.flowMatrix.showDrilldown = false
			return m
		}
		// Close flow matrix view
		m.focused = focusList
	case "j", "down":
		m.flowMatrix.MoveDown()
	case "k", "up":
		m.flowMatrix.MoveUp()
	case "tab":
		m.flowMatrix.TogglePanel()
	case "enter":
		// Open drilldown or jump to issue
		if m.flowMatrix.showDrilldown {
			// Jump to selected issue from drilldown
			if selectedIssue := m.flowMatrix.SelectedDrilldownIssue(); selectedIssue != nil {
				for i, item := range m.list.Items() {
					if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedIssue.ID {
						m.list.Select(i)
						break
					}
				}
				m.focused = focusList
				if m.isSplitView {
					m.focused = focusDetail
				} else {
					m.showDetails = true
					m.focused = focusDetail
					m.viewport.GotoTop()
				}
				m.updateViewportContent()
			}
		} else {
			// Open drilldown for selected label
			m.flowMatrix.OpenDrilldown()
		}
	case "G", "end":
		m.flowMatrix.GoToEnd()
	case "g", "home":
		m.flowMatrix.GoToStart()
	}
	return m
}

// handleRecipePickerKeys handles keyboard input when recipe picker is focused
func (m Model) handleRecipePickerKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "j", "down":
		m.recipePicker.MoveDown()
	case "k", "up":
		m.recipePicker.MoveUp()
	case "esc":
		m.showRecipePicker = false
		m.focused = focusList
	case "enter":
		// Apply selected recipe
		if selected := m.recipePicker.SelectedRecipe(); selected != nil {
			m.setActiveRecipe(selected)
			m.applyRecipe(selected)
		}
		m.showRecipePicker = false
		m.focused = focusList
	}
	return m
}

// handleRepoPickerKeys handles keyboard input when repo picker is focused (workspace mode).
func (m Model) handleRepoPickerKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "j", "down":
		m.repoPicker.MoveDown()
	case "k", "up":
		m.repoPicker.MoveUp()
	case " ", "space":
		m.repoPicker.ToggleSelected()
	case "a":
		m.repoPicker.SelectAll()
	case "esc", "q":
		m.showRepoPicker = false
		m.focused = focusList
	case "enter":
		selected := m.repoPicker.SelectedRepos()

		// Normalize: nil means "all repos" (no filter). Also treat empty as "all" to avoid hiding everything.
		if len(selected) == 0 || len(selected) == len(m.availableRepos) {
			m.activeRepos = nil
			m.statusMsg = "Repo filter: all repos"
		} else {
			m.activeRepos = selected
			m.statusMsg = fmt.Sprintf("Repo filter: %s", formatRepoList(sortedRepoKeys(selected), 3))
		}
		m.statusIsError = false

		// Apply filter to views
		if m.activeRecipe != nil {
			m.applyRecipe(m.activeRecipe)
		} else {
			m.applyFilter()
		}

		m.showRepoPicker = false
		m.focused = focusList
	}
	return m
}

// handleLabelPickerKeys handles keyboard input when label picker is focused (bv-126)
func (m Model) handleLabelPickerKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "esc":
		m.showLabelPicker = false
		m.focused = focusList
	case "j", "down", "ctrl+n":
		m.labelPicker.MoveDown()
	case "k", "up", "ctrl+p":
		m.labelPicker.MoveUp()
	case "enter":
		if selected := m.labelPicker.SelectedLabel(); selected != "" {
			m.currentFilter = "label:" + selected
			m.applyFilter()
			m.statusMsg = fmt.Sprintf("Filtered by label: %s", selected)
			m.statusIsError = false
		}
		m.showLabelPicker = false
		m.focused = focusList
	default:
		// Pass other keys to text input for fuzzy search
		m.labelPicker.UpdateInput(msg)
	}
	return m
}

// handleInsightsKeys handles keyboard input when insights panel is focused
func (m Model) handleInsightsKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "esc":
		m.focused = focusList
	case "j", "down":
		m.insightsPanel.MoveDown()
	case "k", "up":
		m.insightsPanel.MoveUp()
	case "ctrl+j":
		// Scroll detail panel down
		m.insightsPanel.ScrollDetailDown()
	case "ctrl+k":
		// Scroll detail panel up
		m.insightsPanel.ScrollDetailUp()
	case "h", "left":
		m.insightsPanel.PrevPanel()
	case "l", "right", "tab":
		m.insightsPanel.NextPanel()
	case "e":
		// Toggle explanations
		m.insightsPanel.ToggleExplanations()
	case "x":
		// Toggle calculation details
		m.insightsPanel.ToggleCalculation()
	case "m":
		// Toggle heatmap view (bv-95) - "m" for heatMap
		m.insightsPanel.ToggleHeatmap()
	case "enter":
		// Jump to selected issue in list view
		selectedID := m.insightsPanel.SelectedIssueID()
		if selectedID != "" {
			for i, item := range m.list.Items() {
				if issueItem, ok := item.(IssueItem); ok && issueItem.Issue.ID == selectedID {
					m.list.Select(i)
					break
				}
			}
			m.focused = focusList
			if m.isSplitView {
				m.focused = focusDetail
			} else {
				m.showDetails = true
				m.focused = focusDetail
				m.viewport.GotoTop()
			}
			m.updateViewportContent()
		}
	}
	return m
}

// handleListKeys handles keyboard input when the list is focused
func (m Model) handleListKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "enter":
		if !m.isSplitView {
			m.showDetails = true
			m.focused = focusDetail
			m.viewport.GotoTop() // Reset scroll position for new issue
			m.updateViewportContent()
		}
	case "home":
		m.list.Select(0)
	case "G", "end":
		if len(m.list.Items()) > 0 {
			m.list.Select(len(m.list.Items()) - 1)
		}
	case "ctrl+d":
		// Page down
		itemCount := len(m.list.Items())
		if itemCount > 0 {
			currentIdx := m.list.Index()
			newIdx := currentIdx + m.height/3
			if newIdx >= itemCount {
				newIdx = itemCount - 1
			}
			m.list.Select(newIdx)
		}
	case "ctrl+u":
		// Page up
		if len(m.list.Items()) > 0 {
			currentIdx := m.list.Index()
			newIdx := currentIdx - m.height/3
			if newIdx < 0 {
				newIdx = 0
			}
			m.list.Select(newIdx)
		}
	case "o":
		m.currentFilter = "open"
		m.applyFilter()
	case "c":
		m.currentFilter = "closed"
		m.applyFilter()
	case "r":
		m.currentFilter = "ready"
		m.applyFilter()
	case "a":
		m.currentFilter = "all"
		m.applyFilter()
	case "t":
		// Toggle time-travel mode off, or show prompt for custom revision
		if m.timeTravelMode {
			m.exitTimeTravelMode()
		} else {
			// Show input prompt for revision
			m.showTimeTravelPrompt = true
			m.timeTravelInput.SetValue("")
			m.timeTravelInput.Focus()
			m.focused = focusTimeTravelInput
		}
	case "T":
		// Quick time-travel with default HEAD~5
		if m.timeTravelMode {
			m.exitTimeTravelMode()
		} else {
			m.enterTimeTravelMode("HEAD~5")
		}
	case "C":
		// Copy selected issue to clipboard
		m.copyIssueToClipboard()
	case "O":
		// Open beads.jsonl in editor
		m.openInEditor()
	case "h":
		// Toggle history view
		if !m.isHistoryView {
			m.enterHistoryView()
		}
	case "S":
		// Apply triage recipe - sort by triage score (bv-151)
		if r := m.recipeLoader.Get("triage"); r != nil {
			m.setActiveRecipe(r)
			m.applyRecipe(r)
		}
	case "s":
		// Cycle sort mode (bv-3ita)
		m.cycleSortMode()
	case "V":
		// Show cass session preview modal (bv-5bqh)
		m.showCassSessionModal()
	case "U":
		// Show self-update modal (bv-182)
		m.showSelfUpdateModal()
	case "y":
		// Copy ID to clipboard (consistent with board view - bv-yg39)
		selectedItem := m.list.SelectedItem()
		if selectedItem == nil {
			m.statusMsg = "❌ No issue selected"
			m.statusIsError = true
		} else if issueItem, ok := selectedItem.(IssueItem); ok {
			if err := clipboard.WriteAll(issueItem.Issue.ID); err != nil {
				m.statusMsg = fmt.Sprintf("❌ Clipboard error: %v", err)
				m.statusIsError = true
			} else {
				m.statusMsg = fmt.Sprintf("📋 Copied %s to clipboard", issueItem.Issue.ID)
				m.statusIsError = false
			}
		}
	}
	return m
}

// handleTimeTravelInputKeys handles keyboard input for the time-travel revision prompt
func (m Model) handleTimeTravelInputKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "enter":
		// Submit the revision
		revision := strings.TrimSpace(m.timeTravelInput.Value())
		if revision == "" {
			revision = "HEAD~5" // Default if empty
		}
		m.showTimeTravelPrompt = false
		m.timeTravelInput.Blur()
		m.focused = focusList
		m.enterTimeTravelMode(revision)
	case "esc":
		// Cancel
		m.showTimeTravelPrompt = false
		m.timeTravelInput.Blur()
		m.focused = focusList
	default:
		// Update the textinput
		m.timeTravelInput, _ = m.timeTravelInput.Update(msg)
	}
	return m
}

// restoreFocusFromHelp returns the appropriate focus based on current view state.
// This fixes the bug where dismissing help would always return to focusList,
// even when the user was in a specialized view (graph, board, insights, etc.).
func (m Model) restoreFocusFromHelp() focus {
	// Full-screen detail view (not split mode)
	if m.showDetails && !m.isSplitView {
		return focusDetail
	}
	// Specialized views take precedence
	if m.isGraphView {
		return focusGraph
	}
	if m.isBoardView {
		return focusBoard
	}
	if m.isActionableView {
		return focusActionable
	}
	if m.isHistoryView {
		return focusHistory
	}
	// Check for other focus states using stored focusBeforeHelp
	// (m.focused is focusHelp while help is open, so we use the saved value)
	if m.focusBeforeHelp == focusInsights {
		return focusInsights
	}
	if m.focusBeforeHelp == focusLabelDashboard {
		return focusLabelDashboard
	}
	if m.focusBeforeHelp == focusSprint {
		return focusSprint
	}
	if m.focusBeforeHelp == focusFlowMatrix {
		return focusFlowMatrix
	}
	if m.focusBeforeHelp == focusAttention {
		return focusAttention
	}
	if m.focusBeforeHelp == focusLabelPicker {
		return focusLabelPicker
	}
	if m.focusBeforeHelp == focusTimeTravelInput {
		return focusTimeTravelInput
	}
	// Default: return to list
	return focusList
}

// handleHelpKeys handles keyboard input when the help overlay is focused
func (m Model) handleHelpKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "j", "down":
		m.helpScroll++
	case "k", "up":
		if m.helpScroll > 0 {
			m.helpScroll--
		}
	case "ctrl+d":
		m.helpScroll += 10
	case "ctrl+u":
		m.helpScroll -= 10
		if m.helpScroll < 0 {
			m.helpScroll = 0
		}
	case "home", "g":
		m.helpScroll = 0
	case "G", "end":
		// Will be clamped in render
		m.helpScroll = 999
	case "q", "esc", "?", "f1":
		// Close help overlay and restore previous focus
		m.showHelp = false
		m.helpScroll = 0
		m.focused = m.restoreFocusFromHelp()
	case " ": // Space opens interactive tutorial (bv-0trk, bv-8y31)
		m.showHelp = false
		m.helpScroll = 0
		m.showTutorial = true
		m.tutorialModel.SetSize(m.width, m.height)
		m.focused = focusTutorial
	default:
		// Any other key dismisses help and restores previous focus
		m.showHelp = false
		m.helpScroll = 0
		m.focused = m.restoreFocusFromHelp()
	}
	return m
}

func (m Model) renderLoadingScreen() string {
	frame := workerSpinnerFrames[0]
	if m.backgroundWorker != nil && m.backgroundWorker.State() == WorkerProcessing {
		frame = workerSpinnerFrames[m.workerSpinnerIdx%len(workerSpinnerFrames)]
	}

	spinnerStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	lines := []string{
		spinnerStyle.Render(frame),
		"",
		titleStyle.Render("Loading beads..."),
	}
	if m.beadsPath != "" {
		lines = append(lines, "", subStyle.Render(m.beadsPath))
	}

	content := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var body string

	// Quit confirmation overlay takes highest priority
	if m.showQuitConfirm {
		body = m.renderQuitConfirm()
	} else if m.showAgentPrompt {
		// AGENTS.md prompt modal (bv-i8dk)
		body = m.agentPromptModal.CenterModal(m.width, m.height-1)
	} else if m.showCassModal {
		// Cass session preview modal (bv-5bqh)
		body = m.cassModal.CenterModal(m.width, m.height-1)
	} else if m.showUpdateModal {
		// Self-update modal (bv-182)
		body = m.updateModal.CenterModal(m.width, m.height-1)
	} else if m.showLabelHealthDetail && m.labelHealthDetail != nil {
		body = m.renderLabelHealthDetail(*m.labelHealthDetail)
	} else if m.showLabelGraphAnalysis && m.labelGraphAnalysisResult != nil {
		body = m.renderLabelGraphAnalysis()
	} else if m.showLabelDrilldown && m.labelDrilldownLabel != "" {
		body = m.renderLabelDrilldown()
	} else if m.showAlertsPanel {
		body = m.renderAlertsPanel()
	} else if m.showTimeTravelPrompt {
		body = m.renderTimeTravelPrompt()
	} else if m.showRecipePicker {
		body = m.recipePicker.View()
	} else if m.showRepoPicker {
		body = m.repoPicker.View()
	} else if m.showLabelPicker {
		body = m.labelPicker.View()
	} else if m.showHelp {
		body = m.renderHelpOverlay()
	} else if m.showTutorial {
		// Interactive tutorial (bv-8y31) - full screen overlay
		body = m.tutorialModel.View()
	} else if m.snapshotInitPending && m.snapshot == nil {
		body = m.renderLoadingScreen()
	} else if m.focused == focusInsights {
		m.insightsPanel.SetSize(m.width, m.height-1)
		body = m.insightsPanel.View()
	} else if m.focused == focusFlowMatrix {
		m.flowMatrix.SetSize(m.width, m.height-1)
		body = m.flowMatrix.View()
	} else if m.focused == focusTree {
		// Hierarchical tree view (bv-gllx)
		m.tree.SetSize(m.width, m.height-1)
		body = m.tree.View()
	} else if m.isGraphView {
		body = m.graphView.View(m.width, m.height-1)
	} else if m.isBoardView {
		body = m.board.View(m.width, m.height-1)
	} else if m.isActionableView {
		m.actionableView.SetSize(m.width, m.height-2)
		body = m.actionableView.Render()
	} else if m.isHistoryView {
		m.historyView.SetSize(m.width, m.height-1)
		body = m.historyView.View()
	} else if m.isSprintView {
		body = m.sprintViewText
	} else if m.isSplitView {
		body = m.renderSplitView()
	} else if m.focused == focusLabelDashboard {
		m.labelDashboard.SetSize(m.width, m.height-1)
		body = m.labelDashboard.View()
	} else {
		// Mobile view
		if m.showDetails {
			body = m.viewport.View()
		} else {
			body = m.renderListWithHeader()
		}
	}

	// Add shortcuts sidebar if enabled (bv-3qi5)
	if m.showShortcutsSidebar {
		// Update sidebar context based on current focus
		m.shortcutsSidebar.SetContext(ContextFromFocus(m.focused))
		m.shortcutsSidebar.SetSize(m.shortcutsSidebar.Width(), m.height-2)
		sidebar := m.shortcutsSidebar.View()
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, sidebar)
	}

	footer := m.renderFooter()

	// Ensure the final output fits exactly in the terminal height
	// This prevents the header from being pushed off the top
	finalStyle := lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		MaxHeight(m.height)

	return finalStyle.Render(lipgloss.JoinVertical(lipgloss.Left, body, footer))
}

func (m Model) renderQuitConfirm() string {
	t := m.theme

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Blocked).
		Padding(1, 3).
		Align(lipgloss.Center)

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Blocked).
		Bold(true)

	textStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	keyStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	content := titleStyle.Render("Quit bv?") + "\n\n" +
		textStyle.Render("Press ") + keyStyle.Render("Esc") + textStyle.Render(" or ") + keyStyle.Render("Y") + textStyle.Render(" to quit\n") +
		textStyle.Render("Press any other key to cancel")

	box := boxStyle.Render(content)

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		box,
	)
}

func (m Model) renderListWithHeader() string {
	t := m.theme

	// Calculate dimensions based on actual list height set in sizing
	availableHeight := m.list.Height()
	if availableHeight == 0 {
		availableHeight = m.height - 3 // fallback
	}

	// Render column header
	headerStyle := t.Renderer.NewStyle().
		Background(t.Primary).
		Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#282A36"}).
		Bold(true).
		Width(m.width - 2)

	headerText := "  TYPE PRI STATUS      ID                                   TITLE"
	if m.workspaceMode {
		// Account for repo badges like [API] shown in workspace mode.
		headerText = "  REPO TYPE PRI STATUS      ID                               TITLE"
	}
	header := headerStyle.Render(headerText)

	// Page info
	totalItems := len(m.list.Items())
	currentIdx := m.list.Index()
	itemsPerPage := availableHeight
	if itemsPerPage < 1 {
		itemsPerPage = 1
	}
	currentPage := (currentIdx / itemsPerPage) + 1
	totalPages := (totalItems + itemsPerPage - 1) / itemsPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	startItem := 0
	endItem := 0
	if totalItems > 0 {
		startItem = (currentPage-1)*itemsPerPage + 1
		endItem = startItem + itemsPerPage - 1
		if endItem > totalItems {
			endItem = totalItems
		}
	}

	pageInfo := fmt.Sprintf(" Page %d of %d (items %d-%d of %d) ", currentPage, totalPages, startItem, endItem, totalItems)
	pageStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Align(lipgloss.Right).
		Width(m.width - 2)

	// Combine header with page info on the right
	headerLine := lipgloss.JoinHorizontal(lipgloss.Top,
		header,
	)

	// List view - just render it normally since bubbles handles scrolling
	listView := m.list.View()

	// Page indicator line
	pageLine := pageStyle.Render(pageInfo)

	// Combine all elements and force exact height
	// bodyHeight = m.height - 1 (1 for footer)
	bodyHeight := m.height - 1
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	// Build content with explicit height constraint
	// Header (1) + List + PageLine (1) must fit in bodyHeight
	content := lipgloss.JoinVertical(lipgloss.Left, headerLine, listView, pageLine)

	// Force exact height to prevent overflow
	return lipgloss.NewStyle().
		Width(m.width).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Render(content)
}

func (m Model) renderSplitView() string {
	t := m.theme

	var listStyle, detailStyle lipgloss.Style

	if m.focused == focusList {
		listStyle = FocusedPanelStyle
		detailStyle = PanelStyle
	} else {
		listStyle = PanelStyle
		detailStyle = FocusedPanelStyle
	}

	// m.list.Width() is the inner width (set in Update)
	listInnerWidth := m.list.Width()
	panelHeight := m.height - 1

	// Create header row for list
	headerStyle := t.Renderer.NewStyle().
		Background(t.Primary).
		Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#282A36"}).
		Bold(true).
		Width(listInnerWidth)

	header := headerStyle.Render("  TYPE PRI STATUS      ID                     TITLE")

	// Page info for list
	totalItems := len(m.list.Items())
	currentIdx := m.list.Index()
	listHeight := m.list.Height()
	if listHeight == 0 {
		listHeight = panelHeight - 3 // fallback
	}
	if listHeight < 1 {
		listHeight = 1
	}
	currentPage := (currentIdx / listHeight) + 1
	totalPages := (totalItems + listHeight - 1) / listHeight
	if totalPages < 1 {
		totalPages = 1
	}
	startItem := 0
	endItem := 0
	if totalItems > 0 {
		startItem = (currentPage-1)*listHeight + 1
		endItem = startItem + listHeight - 1
		if endItem > totalItems {
			endItem = totalItems
		}
	}

	pageInfo := fmt.Sprintf("Page %d/%d (%d-%d of %d) ", currentPage, totalPages, startItem, endItem, totalItems)
	pageStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Width(listInnerWidth).
		Align(lipgloss.Center)

	pageLine := pageStyle.Render(pageInfo)

	// Combine header + list + page indicator
	listContent := lipgloss.JoinVertical(lipgloss.Left, header, m.list.View(), pageLine)

	// List Panel Width: Inner + 2 (Padding). Border adds another 2.
	// Use MaxHeight to ensure content doesn't overflow
	listView := listStyle.
		Width(listInnerWidth + 2).
		Height(panelHeight).
		MaxHeight(panelHeight).
		Render(listContent)

	// Detail Panel Width: Inner + 2 (Padding). Border adds another 2.
	detailView := detailStyle.
		Width(m.viewport.Width + 2).
		Height(panelHeight).
		MaxHeight(panelHeight).
		Render(m.viewport.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, listView, detailView)
}

func (m *Model) renderHelpOverlay() string {
	t := m.theme

	// Determine layout based on terminal width
	// 3 columns for wide (≥120), 2 columns for medium (≥80), 1 column for narrow
	numCols := 3
	if m.width < 120 {
		numCols = 2
	}
	if m.width < 80 {
		numCols = 1
	}

	// Calculate column width (accounting for gaps and outer padding)
	totalPadding := 8 // outer padding
	gapWidth := 2     // gap between columns
	availableWidth := m.width - totalPadding - (gapWidth * (numCols - 1))
	colWidth := availableWidth / numCols
	if colWidth < 28 {
		colWidth = 28
	}

	// Define color palette (Dracula-inspired gradient)
	colors := []lipgloss.AdaptiveColor{
		{Light: "#7D56F4", Dark: "#BD93F9"}, // Purple
		{Light: "#FF79C6", Dark: "#FF79C6"}, // Pink
		{Light: "#8BE9FD", Dark: "#8BE9FD"}, // Cyan
		{Light: "#50FA7B", Dark: "#50FA7B"}, // Green
		{Light: "#FFB86C", Dark: "#FFB86C"}, // Orange
		{Light: "#F1FA8C", Dark: "#F1FA8C"}, // Yellow
	}

	// Helper to render a section panel
	renderPanel := func(title string, icon string, colorIdx int, shortcuts []struct{ key, desc string }) string {
		color := colors[colorIdx%len(colors)]

		headerStyle := t.Renderer.NewStyle().
			Foreground(color).
			Bold(true).
			BorderStyle(lipgloss.Border{Bottom: "─"}).
			BorderBottom(true).
			BorderForeground(color).
			Width(colWidth-4).
			Padding(0, 1)

		keyStyle := t.Renderer.NewStyle().
			Foreground(color).
			Bold(true).
			Width(10)

		descStyle := t.Renderer.NewStyle().
			Foreground(t.Base.GetForeground()).
			Width(colWidth - 16)

		var content strings.Builder
		content.WriteString(headerStyle.Render(icon + " " + title))
		content.WriteString("\n")

		for _, s := range shortcuts {
			content.WriteString(keyStyle.Render(s.key))
			content.WriteString(descStyle.Render(s.desc))
			content.WriteString("\n")
		}

		panelStyle := t.Renderer.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(color).
			Padding(0, 1).
			Width(colWidth)

		return panelStyle.Render(content.String())
	}

	// Define all sections
	navSection := []struct{ key, desc string }{
		{"j / ↓", "Move down"},
		{"k / ↑", "Move up"},
		{"G/end", "Go to last"},
		{"Ctrl+d", "Page down"},
		{"Ctrl+u", "Page up"},
		{"Tab", "Switch focus"},
		{"Enter", "View details"},
		{"Esc", "Back / close"},
	}

	viewsSection := []struct{ key, desc string }{
		{"b", "Kanban board"},
		{"g", "Graph view"},
		{"i", "Insights"},
		{"h", "History view"},
		{"a", "Actionable"},
		{"f", "Flow matrix"},
		{"[", "Label dashboard"},
		{"]", "Attention view"},
	}

	globalSection := []struct{ key, desc string }{
		{"?", "This help"},
		{";", "Shortcuts bar"},
		{"!", "Alerts panel"},
		{"'", "Recipes"},
		{"w", "Repo picker"},
		{"q", "Back / Quit"},
		{"Ctrl+c", "Force quit"},
	}

	filterSection := []struct{ key, desc string }{
		{"/", "Fuzzy search"},
		{"Ctrl+S", "Semantic search"},
		{"H", "Hybrid ranking"},
		{"Alt+H", "Hybrid preset"},
		{"o", "Open issues"},
		{"c", "Closed issues"},
		{"r", "Ready (unblocked)"},
		{"l", "Filter by label"},
		{"s", "Cycle sort"},
		{"S", "Triage sort"},
	}

	graphSection := []struct{ key, desc string }{
		{"hjkl", "Navigate nodes"},
		{"H/L", "Scroll left/right"},
		{"PgUp/Dn", "Scroll up/down"},
		{"Enter", "Jump to issue"},
	}

	insightsSection := []struct{ key, desc string }{
		{"h/l/Tab", "Switch panels"},
		{"j/k", "Navigate items"},
		{"e", "Explanations"},
		{"x", "Calc details"},
		{"m", "Toggle heatmap"},
		{"Enter", "Jump to issue"},
	}

	historySection := []struct{ key, desc string }{
		{"j/k", "Navigate beads"},
		{"J/K", "Navigate commits"},
		{"Tab", "Toggle focus"},
		{"y", "Copy SHA"},
		{"c", "Confidence filter"},
	}

	actionsSection := []struct{ key, desc string }{
		{"p", "Priority hints"},
		{"Ctrl+R", "Force refresh"},
		{"F5", "Force refresh"},
		{"t", "Time-travel"},
		{"T", "Quick time-travel"},
		{"x", "Export markdown"},
		{"C", "Copy to clipboard"},
		{"O", "Open in editor"},
	}

	statusSection := []struct{ key, desc string }{
		{"◌ metrics", "Phase 2 metrics computing"},
		{"⚠ age", "Snapshot getting stale"},
		{"⚠ STALE", "Snapshot is stale"},
		{"✗ bg", "Background worker errors"},
		{"↻ recov", "Worker self-healed"},
		{"⚠ dead", "Worker unresponsive"},
		{"polling", "Live reload uses polling"},
	}

	// Build panels
	panels := []string{
		renderPanel("Navigation", "🧭", 0, navSection),
		renderPanel("Views", "👁", 1, viewsSection),
		renderPanel("Global", "🌐", 2, globalSection),
		renderPanel("Filters & Sort", "🔍", 3, filterSection),
		renderPanel("Graph View", "📊", 4, graphSection),
		renderPanel("Insights", "💡", 5, insightsSection),
		renderPanel("Status", "🩺", 2, statusSection),
		renderPanel("History", "📜", 0, historySection),
		renderPanel("Actions", "⚡", 1, actionsSection),
	}

	// Arrange panels into columns
	var columns []string
	panelsPerCol := (len(panels) + numCols - 1) / numCols

	for col := 0; col < numCols; col++ {
		start := col * panelsPerCol
		end := start + panelsPerCol
		if end > len(panels) {
			end = len(panels)
		}
		if start >= len(panels) {
			break
		}

		colPanels := panels[start:end]
		columns = append(columns, lipgloss.JoinVertical(lipgloss.Left, colPanels...))
	}

	// Join columns horizontally
	body := lipgloss.JoinHorizontal(lipgloss.Top, columns...)

	// Title bar
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		Padding(0, 2)

	subtitleStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Italic(true)

	title := titleStyle.Render("⌨️  Keyboard Shortcuts")
	subtitle := subtitleStyle.Render("Space: Tutorial │ ? or Esc to close")
	titleBar := lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", subtitle)

	// Combine title and body
	content := lipgloss.JoinVertical(lipgloss.Center, titleBar, "", body)

	// Outer container
	containerStyle := t.Renderer.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2)

	helpBox := containerStyle.Render(content)

	// Center in viewport
	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		helpBox,
	)
}

func (m Model) renderLabelHealthDetail(lh analysis.LabelHealth) string {
	t := m.theme
	innerWidth := m.width - 10
	if innerWidth < 20 {
		innerWidth = 20
	}

	// 1. Define styles first so closures can capture them
	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2)

	labelStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Bold(true)
	valStyle := t.Renderer.NewStyle().Foreground(t.Base.GetForeground())

	// 2. Define helper functions
	bar := func(score int) string {
		lvl := analysis.HealthLevelFromScore(score)
		fill := innerWidth * score / 100
		if fill < 0 {
			fill = 0
		}
		if fill > innerWidth {
			fill = innerWidth
		}
		filled := strings.Repeat("█", fill)
		blank := strings.Repeat("░", innerWidth-fill)
		style := t.Base
		switch lvl {
		case analysis.HealthLevelHealthy:
			style = style.Foreground(t.Open)
		case analysis.HealthLevelWarning:
			style = style.Foreground(t.Feature)
		default:
			style = style.Foreground(t.Blocked)
		}
		return style.Render(filled + blank)
	}

	flowList := func(title string, items []labelCount, arrow string) string {
		if len(items) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString(labelStyle.Render(title))
		b.WriteString("\n")
		limit := len(items)
		if limit > 6 {
			limit = 6
		}
		for i := 0; i < limit; i++ {
			lc := items[i]
			line := fmt.Sprintf("  %s %-16s %3d", arrow, lc.Label, lc.Count)
			b.WriteString(valStyle.Render(line))
			b.WriteString("\n")
		}
		if len(items) > limit {
			b.WriteString(valStyle.Render(fmt.Sprintf("  … +%d more", len(items)-limit)))
			b.WriteString("\n")
		}
		return b.String()
	}

	// 3. Build content
	var sb strings.Builder
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).MarginBottom(1).
		Render(fmt.Sprintf("Label Health: %s", lh.Label)))
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("Overall: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d/100 (%s)", lh.Health, lh.HealthLevel)))
	sb.WriteString("\n")
	sb.WriteString(bar(lh.Health))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("Issues: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d total (%d open, %d blocked, %d closed)", lh.IssueCount, lh.OpenCount, lh.Blocked, lh.ClosedCount)))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("Velocity: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d/100 (7d=%d, 30d=%d, avg_close=%.1fd, trend=%s %.1f%%)", lh.Velocity.VelocityScore, lh.Velocity.ClosedLast7Days, lh.Velocity.ClosedLast30Days, lh.Velocity.AvgDaysToClose, lh.Velocity.TrendDirection, lh.Velocity.TrendPercent)))
	sb.WriteString("\n")
	sb.WriteString(bar(lh.Velocity.VelocityScore))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("Freshness: "))
	oldest := "n/a"
	if !lh.Freshness.OldestOpenIssue.IsZero() {
		oldest = lh.Freshness.OldestOpenIssue.Format("2006-01-02")
	}
	mostRecent := "n/a"
	if !lh.Freshness.MostRecentUpdate.IsZero() {
		mostRecent = lh.Freshness.MostRecentUpdate.Format("2006-01-02")
	}
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d/100 (stale=%d, oldest_open=%s, most_recent=%s)", lh.Freshness.FreshnessScore, lh.Freshness.StaleCount, oldest, mostRecent)))
	sb.WriteString("\n")
	sb.WriteString(bar(lh.Freshness.FreshnessScore))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("Flow: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d/100 (in=%d from %v, out=%d to %v, external blocked=%d blocking=%d)", lh.Flow.FlowScore, lh.Flow.IncomingDeps, lh.Flow.IncomingLabels, lh.Flow.OutgoingDeps, lh.Flow.OutgoingLabels, lh.Flow.BlockedByExternal, lh.Flow.BlockingExternal)))
	sb.WriteString("\n")
	sb.WriteString(bar(lh.Flow.FlowScore))
	sb.WriteString("\n\n")

	// Cross-Label Flow Table (incoming/outgoing dependencies)
	if len(m.labelHealthDetailFlow.Incoming) > 0 || len(m.labelHealthDetailFlow.Outgoing) > 0 {
		sb.WriteString(labelStyle.Render("Cross-label deps:"))
		sb.WriteString("\n")

		if in := flowList("  Incoming", m.labelHealthDetailFlow.Incoming, "←"); in != "" {
			sb.WriteString(in)
			sb.WriteString("\n")
		}
		if out := flowList("  Outgoing", m.labelHealthDetailFlow.Outgoing, "→"); out != "" {
			sb.WriteString(out)
			sb.WriteString("\n")
		}
	}

	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true).Render("Press Esc to close"))

	content := boxStyle.Render(sb.String())

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		content,
	)
}

// renderLabelDrilldown shows a compact drilldown for the selected label
func (m Model) renderLabelDrilldown() string {
	t := m.theme

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Align(lipgloss.Left)

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	labelStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground()).
		Bold(true)

	valStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	// Locate cached health for this label (if available)
	var lh *analysis.LabelHealth
	for i := range m.labelHealthCache.Labels {
		if m.labelHealthCache.Labels[i].Label == m.labelDrilldownLabel {
			lh = &m.labelHealthCache.Labels[i]
			break
		}
	}

	issues := m.labelDrilldownIssues
	total := len(issues)
	open, blocked, inProgress, closed := 0, 0, 0, 0
	for _, is := range issues {
		if isClosedLikeStatus(is.Status) {
			closed++
			continue
		}
		switch is.Status {
		case model.StatusBlocked:
			blocked++
		case model.StatusInProgress:
			inProgress++
		default:
			open++
		}
	}

	// Top issues by PageRank (fallback to ID sort)
	type scored struct {
		issue model.Issue
		score float64
	}
	var scoredIssues []scored
	for _, is := range issues {
		scoredIssues = append(scoredIssues, scored{issue: is, score: m.analysis.GetPageRankScore(is.ID)})
	}
	sort.Slice(scoredIssues, func(i, j int) bool {
		if scoredIssues[i].score == scoredIssues[j].score {
			return scoredIssues[i].issue.ID < scoredIssues[j].issue.ID
		}
		return scoredIssues[i].score > scoredIssues[j].score
	})
	maxRows := m.height - 12
	if maxRows < 3 {
		maxRows = 3
	}
	if len(scoredIssues) > maxRows {
		scoredIssues = scoredIssues[:maxRows]
	}

	bar := func(score int) string {
		width := 20
		fill := int(float64(width) * float64(score) / 100.0)
		if fill < 0 {
			fill = 0
		}
		if fill > width {
			fill = width
		}
		filled := strings.Repeat("█", fill)
		blank := strings.Repeat("░", width-fill)
		style := t.Base
		if lh != nil {
			switch lh.HealthLevel {
			case analysis.HealthLevelHealthy:
				style = style.Foreground(t.Open)
			case analysis.HealthLevelWarning:
				style = style.Foreground(t.Feature)
			default:
				style = style.Foreground(t.Blocked)
			}
		}
		return style.Render(filled + blank)
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Label Drilldown: %s", m.labelDrilldownLabel)))
	sb.WriteString("\n\n")

	if lh != nil {
		sb.WriteString(labelStyle.Render("Health: "))
		sb.WriteString(valStyle.Render(fmt.Sprintf("%d/100 (%s)", lh.Health, lh.HealthLevel)))
		sb.WriteString("\n")
		sb.WriteString(bar(lh.Health))
		sb.WriteString("\n\n")
	}

	sb.WriteString(labelStyle.Render("Issues: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d total (open %d, blocked %d, in-progress %d, closed %d)", total, open, blocked, inProgress, closed)))
	sb.WriteString("\n\n")

	if len(scoredIssues) > 0 {
		sb.WriteString(labelStyle.Render("Top issues by PageRank:"))
		sb.WriteString("\n")
		for _, si := range scoredIssues {
			line := fmt.Sprintf("  %s  %-10s  PR=%.3f  %s", getStatusIcon(si.issue.Status), si.issue.ID, si.score, si.issue.Title)
			sb.WriteString(valStyle.Render(line))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Cross-label flows summary
	flow := m.getCrossFlowsForLabel(m.labelDrilldownLabel)
	if len(flow.Incoming) > 0 || len(flow.Outgoing) > 0 {
		sb.WriteString(labelStyle.Render("Cross-label deps:"))
		sb.WriteString("\n")
		renderFlowList := func(title string, items []labelCount, arrow string) {
			if len(items) == 0 {
				return
			}
			sb.WriteString(valStyle.Render(title))
			sb.WriteString("\n")
			limit := len(items)
			if limit > 5 {
				limit = 5
			}
			for i := 0; i < limit; i++ {
				lc := items[i]
				line := fmt.Sprintf("  %s %-14s %3d", arrow, lc.Label, lc.Count)
				sb.WriteString(valStyle.Render(line))
				sb.WriteString("\n")
			}
			if len(items) > limit {
				sb.WriteString(valStyle.Render(fmt.Sprintf("  … +%d more", len(items)-limit)))
				sb.WriteString("\n")
			}
		}
		renderFlowList("  Incoming", flow.Incoming, "←")
		renderFlowList("  Outgoing", flow.Outgoing, "→")
		sb.WriteString("\n")
	}

	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true).Render("Press Esc to close • g for graph analysis"))

	content := boxStyle.Render(sb.String())

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		content,
	)
}

// renderLabelGraphAnalysis shows label-specific graph metrics (bv-109)
func (m Model) renderLabelGraphAnalysis() string {
	t := m.theme
	r := m.labelGraphAnalysisResult

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Align(lipgloss.Left)

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	labelStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground()).
		Bold(true)

	valStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	subtextStyle := t.Renderer.NewStyle().
		Foreground(t.Subtext).
		Italic(true)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Graph Analysis: %s", r.Label)))
	sb.WriteString("\n")
	sb.WriteString(subtextStyle.Render("PageRank & Critical Path computed on label subgraph"))
	sb.WriteString("\n\n")

	// Subgraph stats
	sb.WriteString(labelStyle.Render("Subgraph: "))
	sb.WriteString(valStyle.Render(fmt.Sprintf("%d issues (%d core, %d dependencies), %d edges",
		r.Subgraph.IssueCount, r.Subgraph.CoreCount,
		r.Subgraph.IssueCount-r.Subgraph.CoreCount, r.Subgraph.EdgeCount)))
	sb.WriteString("\n\n")

	// Critical Path section
	sb.WriteString(labelStyle.Render("🛤️  Critical Path"))
	if r.CriticalPath.HasCycle {
		sb.WriteString(valStyle.Render(" ⚠️  (cycle detected - path unreliable)"))
	}
	sb.WriteString("\n")
	if r.CriticalPath.PathLength == 0 {
		sb.WriteString(subtextStyle.Render("  No dependency chains found"))
	} else {
		sb.WriteString(valStyle.Render(fmt.Sprintf("  Length: %d issues (max height: %d)",
			r.CriticalPath.PathLength, r.CriticalPath.MaxHeight)))
		sb.WriteString("\n")

		// Show the path with titles
		maxRows := m.height - 20
		if maxRows < 3 {
			maxRows = 3
		}
		showCount := len(r.CriticalPath.Path)
		if showCount > maxRows {
			showCount = maxRows
		}

		for i := 0; i < showCount; i++ {
			issueID := r.CriticalPath.Path[i]
			title := r.CriticalPath.PathTitles[i]
			if title == "" {
				title = "(no title)"
			}
			arrow := "  →"
			if i == 0 {
				arrow = "  ●" // root
			}
			if i == len(r.CriticalPath.Path)-1 {
				arrow = "  ◆" // leaf
			}

			// Truncate title if needed
			maxTitleLen := m.width/2 - 20
			if maxTitleLen < 20 {
				maxTitleLen = 20
			}
			if len(title) > maxTitleLen {
				title = title[:maxTitleLen-1] + "…"
			}

			height := r.CriticalPath.AllHeights[issueID]
			line := fmt.Sprintf("%s %-12s [h=%d] %s", arrow, issueID, height, title)
			sb.WriteString(valStyle.Render(line))
			sb.WriteString("\n")
		}
		if len(r.CriticalPath.Path) > showCount {
			sb.WriteString(subtextStyle.Render(fmt.Sprintf("  … +%d more in path", len(r.CriticalPath.Path)-showCount)))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// PageRank section
	sb.WriteString(labelStyle.Render("📊 PageRank (Top Issues)"))
	sb.WriteString("\n")
	if len(r.PageRank.TopIssues) == 0 {
		sb.WriteString(subtextStyle.Render("  No issues to rank"))
	} else {
		maxPRRows := 8
		showPRCount := len(r.PageRank.TopIssues)
		if showPRCount > maxPRRows {
			showPRCount = maxPRRows
		}

		for i := 0; i < showPRCount; i++ {
			item := r.PageRank.TopIssues[i]
			title := ""
			statusIcon := "○"
			if iss, ok := r.Subgraph.IssueMap[item.ID]; ok {
				title = iss.Title
				statusIcon = getStatusIcon(iss.Status)
			}
			if title == "" {
				title = "(no title)"
			}

			// Truncate title if needed
			maxTitleLen := m.width/2 - 30
			if maxTitleLen < 15 {
				maxTitleLen = 15
			}
			if len(title) > maxTitleLen {
				title = title[:maxTitleLen-1] + "…"
			}

			normalized := r.PageRank.Normalized[item.ID]
			line := fmt.Sprintf("  %s %-12s PR=%.4f (%.0f%%) %s",
				statusIcon, item.ID, item.Score, normalized*100, title)
			sb.WriteString(valStyle.Render(line))
			sb.WriteString("\n")
		}
		if len(r.PageRank.TopIssues) > showPRCount {
			sb.WriteString(subtextStyle.Render(fmt.Sprintf("  … +%d more ranked", len(r.PageRank.TopIssues)-showPRCount)))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true).Render("Press Esc/q/g to close"))

	content := boxStyle.Render(sb.String())

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		content,
	)
}

func (m *Model) renderFooter() string {
	// ══════════════════════════════════════════════════════════════════════════
	// POLISHED FOOTER - Stripe-level status bar with visual hierarchy
	// ══════════════════════════════════════════════════════════════════════════

	// If there's a status message, show it prominently with polished styling
	if m.statusMsg != "" {
		var msgStyle lipgloss.Style
		if m.statusIsError {
			msgStyle = lipgloss.NewStyle().
				Background(ColorPrioCriticalBg).
				Foreground(ColorPrioCritical).
				Bold(true).
				Padding(0, 2)
		} else {
			msgStyle = lipgloss.NewStyle().
				Background(ColorStatusOpenBg).
				Foreground(ColorSuccess).
				Bold(true).
				Padding(0, 2)
		}
		prefix := "✓ "
		if m.statusIsError {
			prefix = "✗ "
		}
		msgSection := msgStyle.Render(prefix + m.statusMsg)
		remaining := m.width - lipgloss.Width(msgSection)
		if remaining < 0 {
			remaining = 0
		}
		filler := lipgloss.NewStyle().Width(remaining).Render("")
		return lipgloss.JoinHorizontal(lipgloss.Bottom, msgSection, filler)
	}

	// ─────────────────────────────────────────────────────────────────────────
	// FILTER BADGE - Current view/filter state + quick hint for label dashboard
	// ─────────────────────────────────────────────────────────────────────────
	var filterTxt string
	var filterIcon string
	if m.focused == focusLabelDashboard {
		filterTxt = "LABELS: j/k nav • h detail • d drilldown • enter filter"
		filterIcon = "🏷️"
	} else if m.showLabelGraphAnalysis && m.labelGraphAnalysisResult != nil {
		filterTxt = fmt.Sprintf("GRAPH %s: esc/q/g close", m.labelGraphAnalysisResult.Label)
		filterIcon = "📊"
	} else if m.showLabelDrilldown && m.labelDrilldownLabel != "" {
		filterTxt = fmt.Sprintf("LABEL %s: enter filter • g graph • esc/q/d close", m.labelDrilldownLabel)
		filterIcon = "🏷️"
	} else {
		switch m.currentFilter {
		case "all":
			filterTxt = "ALL"
			filterIcon = "📋"
		case "open":
			filterTxt = "OPEN"
			filterIcon = "📂"
		case "closed":
			filterTxt = "CLOSED"
			filterIcon = "✅"
		case "ready":
			filterTxt = "READY"
			filterIcon = "🚀"
		default:
			if strings.HasPrefix(m.currentFilter, "recipe:") {
				filterTxt = strings.ToUpper(m.currentFilter[7:])
				filterIcon = "📑"
			} else {
				filterTxt = m.currentFilter
				filterIcon = "🔍"
			}
		}
	}

	filterBadge := lipgloss.NewStyle().
		Background(ColorPrimary).
		Foreground(ColorText).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", filterIcon, filterTxt))

	// Search mode badge when filtering
	searchBadge := ""
	if m.list.FilterState() != list.Unfiltered {
		mode := "fuzzy"
		if m.semanticSearchEnabled {
			mode = "semantic"
			if m.semanticIndexBuilding {
				mode = "semantic (indexing)"
			}
			if m.semanticHybridEnabled {
				mode = fmt.Sprintf("hybrid/%s", m.semanticHybridPreset)
				if m.semanticHybridBuilding {
					mode = fmt.Sprintf("hybrid/%s (metrics)", m.semanticHybridPreset)
				}
			}
		}
		searchBadge = lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorSecondary).
			Padding(0, 1).
			Render(fmt.Sprintf("🔎 %s", mode))
	}

	// Sort badge - only show when not default (bv-3ita)
	sortBadge := ""
	if m.sortMode != SortDefault {
		sortBadge = lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorSecondary).
			Padding(0, 1).
			Render(fmt.Sprintf("↕ %s", m.sortMode.String()))
	}

	labelHint := lipgloss.NewStyle().
		Foreground(ColorFooterHint).
		Padding(0, 1).
		Render("L:labels • h:detail")

	// Board-specific hints (bv-yg39, bv-naov)
	if m.isBoardView {
		if m.board.IsSearchMode() {
			// Search mode active - show search hints
			matchInfo := ""
			if m.board.SearchMatchCount() > 0 {
				matchInfo = fmt.Sprintf(" [%d/%d]", m.board.SearchCursorPos(), m.board.SearchMatchCount())
			}
			labelHint = lipgloss.NewStyle().
				Foreground(ColorFooterHint).
				Padding(0, 1).
				Render(fmt.Sprintf("/%s%s • n/N:match • enter:done • esc:cancel", m.board.SearchQuery(), matchInfo))
		} else {
			// Normal board mode - show navigation hints with filter indicator (bv-naov)
			filterInfo := ""
			if m.currentFilter != "all" && m.currentFilter != "" {
				shown := m.board.TotalCount()
				total := len(m.issues)
				filterInfo = fmt.Sprintf("[%s:%d/%d] ", m.currentFilter, shown, total)
			}
			labelHint = lipgloss.NewStyle().
				Foreground(ColorFooterHint).
				Padding(0, 1).
				Render(fmt.Sprintf("%s1-4:col • o/c/r:filter • L:labels • /:search • ?:help", filterInfo))
		}
	} else if m.showAttentionView {
		labelHint = lipgloss.NewStyle().
			Foreground(ColorFooterHint).
			Padding(0, 1).
			Render("A:attention • 1-9 filter • esc close")
	}

	// ─────────────────────────────────────────────────────────────────────────
	// STATS SECTION - Issue counts with visual indicators
	// ─────────────────────────────────────────────────────────────────────────
	var statsSection string
	if m.timeTravelMode && m.timeTravelDiff != nil {
		d := m.timeTravelDiff.Summary
		timeTravelStyle := lipgloss.NewStyle().
			Background(ColorPrioHighBg).
			Foreground(ColorWarning).
			Padding(0, 1)
		statsSection = timeTravelStyle.Render(fmt.Sprintf("⏱ %s: +%d ✅%d ~%d",
			m.timeTravelSince, d.IssuesAdded, d.IssuesClosed, d.IssuesModified))
	} else {
		// Polished stats with mini indicators
		statsStyle := lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorText).
			Padding(0, 1)

		openStyle := lipgloss.NewStyle().Foreground(ColorStatusOpen)
		readyStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
		blockedStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		closedStyle := lipgloss.NewStyle().Foreground(ColorMuted)

		statsContent := fmt.Sprintf("%s%d %s%d %s%d %s%d",
			openStyle.Render("○"),
			m.countOpen,
			readyStyle.Render("◉"),
			m.countReady,
			blockedStyle.Render("◈"),
			m.countBlocked,
			closedStyle.Render("●"),
			m.countClosed)
		statsSection = statsStyle.Render(statsContent)
	}

	// ─────────────────────────────────────────────────────────────────────────
	// FRESHNESS / WORKER BADGE - Staleness + errors + background worker activity (bv-h305)
	// ─────────────────────────────────────────────────────────────────────────
	workerSection := ""
	if m.backgroundWorker != nil {
		formatAge := func(d time.Duration) string {
			switch {
			case d < time.Second:
				return "<1s"
			case d < time.Minute:
				return fmt.Sprintf("%ds", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("%dm", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd", int(d.Hours()/24))
			}
		}

		var snapshotAge time.Duration
		hasSnapshotAge := false
		if m.snapshot != nil && !m.snapshot.CreatedAt.IsZero() {
			snapshotAge = time.Since(m.snapshot.CreatedAt)
			hasSnapshotAge = true
		}

		state := m.backgroundWorker.State()
		health := m.backgroundWorker.Health()
		lastErr := m.backgroundWorker.LastError()

		var style lipgloss.Style
		var text string
		switch {
		case health.Started && !health.Alive:
			style = lipgloss.NewStyle().
				Background(ColorPrioCriticalBg).
				Foreground(ColorPrioCritical).
				Bold(true).
				Padding(0, 1)
			text = "⚠ worker unresponsive"

		case state == WorkerProcessing && m.backgroundWorker.ProcessingDuration() >= 250*time.Millisecond:
			// Only show spinner after grace period to avoid flicker for quick dedup operations
			style = lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorInfo).
				Bold(true).
				Padding(0, 1)
			frame := workerSpinnerFrames[m.workerSpinnerIdx%len(workerSpinnerFrames)]
			text = fmt.Sprintf("%s refreshing", frame)

		case lastErr != nil && lastErr.Retries >= freshnessErrorRetries:
			style = lipgloss.NewStyle().
				Background(ColorPrioCriticalBg).
				Foreground(ColorPrioCritical).
				Bold(true).
				Padding(0, 1)
			text = fmt.Sprintf("✗ bg %s (%dx)", lastErr.Phase, lastErr.Retries)

		case lastErr != nil:
			style = lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorWarning).
				Bold(true).
				Padding(0, 1)
			text = fmt.Sprintf("⚠ bg %s (%s)", lastErr.Phase, formatAge(time.Since(lastErr.Time)))

		case hasSnapshotAge && snapshotAge >= freshnessStaleThreshold():
			style = lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorDanger).
				Bold(true).
				Padding(0, 1)
			text = fmt.Sprintf("⚠ STALE: %s ago", formatAge(snapshotAge))

		case hasSnapshotAge && snapshotAge >= freshnessWarnThreshold():
			style = lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorWarning).
				Padding(0, 1)
			text = fmt.Sprintf("⚠ %s ago", formatAge(snapshotAge))

		default:
			if health.RecoveryCount > 0 {
				style = lipgloss.NewStyle().
					Background(ColorBgHighlight).
					Foreground(ColorWarning).
					Padding(0, 1)
				text = fmt.Sprintf("↻ recovered x%d", health.RecoveryCount)
			} else {
				// Fresh: no indicator.
				text = ""
			}
		}

		if text != "" {
			workerSection = style.Render(text)
		}
	}

	// ─────────────────────────────────────────────────────────────────────────
	// PHASE 2 PROGRESS - show while metrics are still computing (bv-tspo)
	// ─────────────────────────────────────────────────────────────────────────
	phase2Section := ""
	if m.snapshot != nil && !m.snapshot.Phase2Ready {
		phase2Style := lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorInfo).
			Padding(0, 1)
		phase2Section = phase2Style.Render("◌ metrics…")
	}

	// ─────────────────────────────────────────────────────────────────────────
	// WATCHER MODE - show polling mode when fsnotify isn't reliable (bv-3zwy)
	// ─────────────────────────────────────────────────────────────────────────
	watcherSection := ""
	{
		var (
			polling      bool
			fsType       watcher.FilesystemType
			pollInterval time.Duration
		)

		switch {
		case m.backgroundWorker != nil:
			polling, fsType, pollInterval = m.backgroundWorker.WatcherInfo()
		case m.watcher != nil:
			polling = m.watcher.IsPolling()
			fsType = m.watcher.FilesystemType()
			pollInterval = m.watcher.PollInterval()
		}

		if polling {
			watcherStyle := lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorMuted).
				Padding(0, 1)
			label := "polling"
			if fsType != watcher.FSTypeUnknown && fsType != watcher.FSTypeLocal {
				label = fmt.Sprintf("polling %s", fsType.String())
			}
			if pollInterval > 0 {
				label = fmt.Sprintf("%s %s", label, pollInterval.String())
			}
			watcherSection = watcherStyle.Render(label)
		}
	}

	// ─────────────────────────────────────────────────────────────────────────
	// UPDATE BADGE - New version available
	// ─────────────────────────────────────────────────────────────────────────
	updateSection := ""
	if m.updateAvailable {
		updateStyle := lipgloss.NewStyle().
			Background(ColorTypeFeature).
			Foreground(ColorBg).
			Bold(true).
			Padding(0, 1)
		updateSection = updateStyle.Render(fmt.Sprintf("⭐ %s", m.updateTag))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// LARGE DATASET WARNING - Tiered performance mode (bv-9thm)
	// ─────────────────────────────────────────────────────────────────────────
	datasetSection := ""
	if m.snapshot != nil && m.snapshot.LargeDatasetWarning != "" {
		bg := ColorPrioHighBg
		fg := ColorWarning
		if m.snapshot.DatasetTier == datasetTierHuge {
			bg = ColorPrioCriticalBg
			fg = ColorPrioCritical
		}
		datasetStyle := lipgloss.NewStyle().
			Background(bg).
			Foreground(fg).
			Bold(true).
			Padding(0, 1)
		datasetSection = datasetStyle.Render(m.snapshot.LargeDatasetWarning)
	}

	// ─────────────────────────────────────────────────────────────────────────
	// ALERTS BADGE - Project health alerts (bv-168)
	// ─────────────────────────────────────────────────────────────────────────
	alertsSection := ""
	// Count active (non-dismissed) alerts
	activeAlerts := 0
	activeCritical := 0
	activeWarning := 0
	for _, a := range m.alerts {
		if !m.dismissedAlerts[alertKey(a)] {
			activeAlerts++
			switch a.Severity {
			case drift.SeverityCritical:
				activeCritical++
			case drift.SeverityWarning:
				activeWarning++
			}
		}
	}
	if activeAlerts > 0 {
		var alertStyle lipgloss.Style
		var alertIcon string
		if activeCritical > 0 {
			alertStyle = lipgloss.NewStyle().
				Background(ColorPrioCriticalBg).
				Foreground(ColorPrioCritical).
				Bold(true).
				Padding(0, 1)
			alertIcon = "⚠"
		} else if activeWarning > 0 {
			alertStyle = lipgloss.NewStyle().
				Background(ColorPrioHighBg).
				Foreground(ColorWarning).
				Bold(true).
				Padding(0, 1)
			alertIcon = "⚡"
		} else {
			alertStyle = lipgloss.NewStyle().
				Background(ColorBgHighlight).
				Foreground(ColorInfo).
				Padding(0, 1)
			alertIcon = "ℹ"
		}
		alertsSection = alertStyle.Render(fmt.Sprintf("%s %d alerts (!)", alertIcon, activeAlerts))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// INSTANCE WARNING - Secondary instance indicator (bv-vrvn)
	// ─────────────────────────────────────────────────────────────────────────
	instanceSection := ""
	if m.instanceLock != nil && !m.instanceLock.IsFirstInstance() {
		instanceStyle := lipgloss.NewStyle().
			Background(ColorPrioHighBg).
			Foreground(ColorWarning).
			Bold(true).
			Padding(0, 1)
		instanceSection = instanceStyle.Render(fmt.Sprintf("⚠ PID %d", m.instanceLock.HolderPID()))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// SESSION INDICATOR - Cass coding sessions for selected bead (bv-y836)
	// ─────────────────────────────────────────────────────────────────────────
	sessionSection := ""
	if sessionCount := m.getCassSessionCount(); sessionCount > 0 {
		sessionStyle := lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorInfo).
			Padding(0, 1)
		countStr := fmt.Sprintf("%d", sessionCount)
		if sessionCount > 9 {
			countStr = "9+"
		}
		sessionSection = sessionStyle.Render(fmt.Sprintf("📎%s", countStr))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// WORKSPACE BADGE - Multi-repo mode indicator
	// ─────────────────────────────────────────────────────────────────────────
	workspaceSection := ""
	if m.workspaceMode && m.workspaceSummary != "" {
		workspaceStyle := lipgloss.NewStyle().
			Background(ThemeBg("#45B7D1")).
			Foreground(ColorBg).
			Bold(true).
			Padding(0, 1)
		workspaceSection = workspaceStyle.Render(fmt.Sprintf("📦 %s", m.workspaceSummary))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// REPO FILTER BADGE - Active repo selection (workspace mode)
	// ─────────────────────────────────────────────────────────────────────────
	repoFilterSection := ""
	if m.workspaceMode && m.activeRepos != nil && len(m.activeRepos) > 0 {
		active := sortedRepoKeys(m.activeRepos)
		label := formatRepoList(active, 3)
		repoStyle := lipgloss.NewStyle().
			Background(ColorBgHighlight).
			Foreground(ColorInfo).
			Bold(true).
			Padding(0, 1)
		repoFilterSection = repoStyle.Render(fmt.Sprintf("🗂 %s", label))
	}

	// ─────────────────────────────────────────────────────────────────────────
	// KEYBOARD HINTS - Context-aware navigation help
	// ─────────────────────────────────────────────────────────────────────────
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorFooterKey).
		Bold(true).
		Padding(0, 0)
	sepStyle := lipgloss.NewStyle().Foreground(ColorFooterSep)
	sep := sepStyle.Render(" │ ")

	var keyHints []string
	if m.showHelp {
		keyHints = append(keyHints, "Press any key to close")
	} else if m.showRecipePicker {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("⏎")+" apply", keyStyle.Render("esc")+" cancel")
	} else if m.showRepoPicker {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("space")+" toggle", keyStyle.Render("⏎")+" apply", keyStyle.Render("esc")+" cancel")
	} else if m.showLabelPicker {
		keyHints = append(keyHints, "type to filter", keyStyle.Render("j/k")+" nav", keyStyle.Render("⏎")+" apply", keyStyle.Render("esc")+" cancel")
	} else if m.focused == focusInsights {
		keyHints = append(keyHints, keyStyle.Render("h/l")+" panels", keyStyle.Render("e")+" explain", keyStyle.Render("⏎")+" jump", keyStyle.Render("?")+" help")
		keyHints = append(keyHints, keyStyle.Render("A")+" attention", keyStyle.Render("F")+" flow")
	} else if m.focused == focusFlowMatrix {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("tab")+" panel", keyStyle.Render("⏎")+" drill", keyStyle.Render("esc")+" back", keyStyle.Render("f")+" close")
	} else if m.isGraphView {
		keyHints = append(keyHints, keyStyle.Render("hjkl")+" nav", keyStyle.Render("H/L")+" scroll", keyStyle.Render("⏎")+" view", keyStyle.Render("g")+" list")
	} else if m.isBoardView {
		keyHints = append(keyHints, keyStyle.Render("hjkl")+" nav", keyStyle.Render("G")+" bottom", keyStyle.Render("⏎")+" view", keyStyle.Render("b")+" list")
	} else if m.isActionableView {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("⏎")+" view", keyStyle.Render("a")+" list", keyStyle.Render("?")+" help")
	} else if m.isHistoryView {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("tab")+" focus", keyStyle.Render("⏎")+" jump", keyStyle.Render("H")+" close")
	} else if m.list.FilterState() == list.Filtering {
		mode := "fuzzy"
		if m.semanticSearchEnabled {
			mode = "semantic"
			if m.semanticIndexBuilding {
				mode = "semantic (indexing)"
			}
		}
		keyHints = append(keyHints, keyStyle.Render("esc")+" cancel", keyStyle.Render("ctrl+s")+" "+mode, keyStyle.Render("⏎")+" select")
		if m.semanticSearchEnabled {
			keyHints = append(keyHints, keyStyle.Render("H")+" hybrid", keyStyle.Render("alt+h")+" preset")
		}
	} else if m.showTimeTravelPrompt {
		keyHints = append(keyHints, keyStyle.Render("⏎")+" compare", keyStyle.Render("esc")+" cancel")
	} else {
		if m.timeTravelMode {
			keyHints = append(keyHints, keyStyle.Render("t")+" exit diff", keyStyle.Render("C")+" copy", keyStyle.Render("abgi")+" views", keyStyle.Render("?")+" help")
		} else if m.isSplitView {
			keyHints = append(keyHints, keyStyle.Render("tab")+" focus", keyStyle.Render("C")+" copy", keyStyle.Render("x")+" export", keyStyle.Render("Ctrl+R")+" refresh", keyStyle.Render("?")+" help")
		} else if m.showDetails {
			keyHints = append(keyHints, keyStyle.Render("esc")+" back", keyStyle.Render("C")+" copy", keyStyle.Render("O")+" edit", keyStyle.Render("Ctrl+R")+" refresh", keyStyle.Render("?")+" help")
		} else {
			keyHints = append(keyHints, keyStyle.Render("⏎")+" details", keyStyle.Render("t")+" diff", keyStyle.Render("S")+" triage", keyStyle.Render("l")+" labels", keyStyle.Render("Ctrl+R")+" refresh", keyStyle.Render("?")+" help")
			if m.workspaceMode {
				keyHints = append(keyHints, keyStyle.Render("w")+" repos")
			}
		}
	}

	keysSection := lipgloss.NewStyle().
		Foreground(ColorFooterHint).
		Padding(0, 1).
		Render(strings.Join(keyHints, sep))

	// ─────────────────────────────────────────────────────────────────────────
	// COUNT BADGE - Total issues displayed
	// ─────────────────────────────────────────────────────────────────────────
	countBadge := lipgloss.NewStyle().
		Foreground(ColorFooterDim).
		Padding(0, 1).
		Render(fmt.Sprintf("%d issues", len(m.list.Items())))

	// ─────────────────────────────────────────────────────────────────────────
	// ASSEMBLE FOOTER with proper spacing
	// ─────────────────────────────────────────────────────────────────────────
	leftWidth := lipgloss.Width(filterBadge) + lipgloss.Width(labelHint) + lipgloss.Width(statsSection)
	if phase2Section != "" {
		leftWidth += lipgloss.Width(phase2Section) + 1
	}
	if watcherSection != "" {
		leftWidth += lipgloss.Width(watcherSection) + 1
	}
	if workerSection != "" {
		leftWidth += lipgloss.Width(workerSection) + 1
	}
	if searchBadge != "" {
		leftWidth += lipgloss.Width(searchBadge) + 1
	}
	if sortBadge != "" {
		leftWidth += lipgloss.Width(sortBadge) + 1
	}
	if alertsSection != "" {
		leftWidth += lipgloss.Width(alertsSection) + 1
	}
	if instanceSection != "" {
		leftWidth += lipgloss.Width(instanceSection) + 1
	}
	if sessionSection != "" {
		leftWidth += lipgloss.Width(sessionSection) + 1
	}
	if workspaceSection != "" {
		leftWidth += lipgloss.Width(workspaceSection) + 1
	}
	if repoFilterSection != "" {
		leftWidth += lipgloss.Width(repoFilterSection) + 1
	}
	if updateSection != "" {
		leftWidth += lipgloss.Width(updateSection) + 1
	}
	if datasetSection != "" {
		leftWidth += lipgloss.Width(datasetSection) + 1
	}
	rightWidth := lipgloss.Width(countBadge) + lipgloss.Width(keysSection)

	remaining := m.width - leftWidth - rightWidth - 1
	if remaining < 0 {
		remaining = 0
	}
	filler := lipgloss.NewStyle().Width(remaining).Render("")

	// Build the footer
	var parts []string
	parts = append(parts, filterBadge)
	if searchBadge != "" {
		parts = append(parts, searchBadge)
	}
	if sortBadge != "" {
		parts = append(parts, sortBadge)
	}
	parts = append(parts, labelHint)
	if alertsSection != "" {
		parts = append(parts, alertsSection)
	}
	if instanceSection != "" {
		parts = append(parts, instanceSection)
	}
	if sessionSection != "" {
		parts = append(parts, sessionSection)
	}
	if workspaceSection != "" {
		parts = append(parts, workspaceSection)
	}
	if repoFilterSection != "" {
		parts = append(parts, repoFilterSection)
	}
	if updateSection != "" {
		parts = append(parts, updateSection)
	}
	if datasetSection != "" {
		parts = append(parts, datasetSection)
	}
	parts = append(parts, statsSection)
	if phase2Section != "" {
		parts = append(parts, phase2Section)
	}
	if watcherSection != "" {
		parts = append(parts, watcherSection)
	}
	if workerSection != "" {
		parts = append(parts, workerSection)
	}
	parts = append(parts, filler, countBadge, keysSection)

	return lipgloss.JoinHorizontal(lipgloss.Bottom, parts...)
}

func nextHybridPreset(current search.PresetName) search.PresetName {
	presets := search.ListPresets()
	if len(presets) == 0 {
		return search.PresetDefault
	}
	for i, preset := range presets {
		if preset == current {
			return presets[(i+1)%len(presets)]
		}
	}
	return presets[0]
}

// getDiffStatus returns the diff status for an issue if time-travel mode is active
func (m Model) getDiffStatus(id string) DiffStatus {
	if !m.timeTravelMode {
		return DiffStatusNone
	}
	if m.newIssueIDs[id] {
		return DiffStatusNew
	}
	if m.closedIssueIDs[id] {
		return DiffStatusClosed
	}
	if m.modifiedIssueIDs[id] {
		return DiffStatusModified
	}
	return DiffStatusNone
}

// hasActiveFilters returns true if any filter is currently applied
// (status filter, label filter, recipe filter, or fuzzy search)
func (m *Model) hasActiveFilters() bool {
	// Check status/label/recipe filter
	if m.currentFilter != "all" {
		return true
	}
	// Check if fuzzy search filter is active
	if m.list.FilterState() == list.Filtering || m.list.FilterState() == list.FilterApplied {
		return true
	}
	return false
}

// clearAllFilters resets all filters to their default state
func (m *Model) clearAllFilters() {
	m.currentFilter = "all"
	m.setActiveRecipe(nil) // Clear any active recipe filter
	// Reset the fuzzy search filter by resetting the filter state
	m.list.ResetFilter()
	m.applyFilter()
}

func (m *Model) setActiveRecipe(r *recipe.Recipe) {
	m.activeRecipe = r
	if m.backgroundWorker != nil {
		m.backgroundWorker.SetRecipe(r)
	}
}

func (m *Model) matchesCurrentFilter(issue model.Issue) bool {
	// Workspace repo filter (nil = all repos)
	if m.workspaceMode && m.activeRepos != nil {
		repoKey := strings.ToLower(ExtractRepoPrefix(issue.ID))
		if repoKey != "" && !m.activeRepos[repoKey] {
			return false
		}
	}

	switch m.currentFilter {
	case "all":
		return true
	case "open":
		return !isClosedLikeStatus(issue.Status)
	case "closed":
		return isClosedLikeStatus(issue.Status)
	case "ready":
		// Ready = Open/InProgress AND NO Open Blockers
		// Exclude draft/deferred - not ready for execution
		if isClosedLikeStatus(issue.Status) || issue.Status == model.StatusBlocked ||
			issue.Status == model.StatusDraft || issue.Status == model.StatusDeferred {
			return false
		}
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if blocker, exists := m.issueMap[dep.DependsOnID]; exists && !isClosedLikeStatus(blocker.Status) {
				return false
			}
		}
		return true
	default:
		if strings.HasPrefix(m.currentFilter, "label:") {
			label := strings.TrimPrefix(m.currentFilter, "label:")
			for _, l := range issue.Labels {
				if l == label {
					return true
				}
			}
		}
		return false
	}
}

func (m *Model) filteredIssuesForActiveView() []model.Issue {
	filtered := make([]model.Issue, 0, len(m.issues))
	recipeFilterActive := m.activeRecipe != nil && strings.HasPrefix(m.currentFilter, "recipe:")
	if recipeFilterActive {
		for _, issue := range m.issues {
			if m.workspaceMode && m.activeRepos != nil {
				repoKey := strings.ToLower(ExtractRepoPrefix(issue.ID))
				if repoKey != "" && !m.activeRepos[repoKey] {
					continue
				}
			}
			if issueMatchesRecipe(issue, m.issueMap, m.activeRecipe) {
				filtered = append(filtered, issue)
			}
		}
		sortIssuesByRecipe(filtered, m.analysis, m.activeRecipe)
		return filtered
	}
	for _, issue := range m.issues {
		if m.matchesCurrentFilter(issue) {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func (m *Model) refreshBoardAndGraphForCurrentFilter() {
	if !m.isBoardView && !m.isGraphView {
		return
	}

	filteredIssues := m.filteredIssuesForActiveView()
	recipeFilterActive := m.activeRecipe != nil && strings.HasPrefix(m.currentFilter, "recipe:")
	if m.isBoardView {
		useSnapshot := m.snapshot != nil && m.snapshot.BoardState != nil && (!m.workspaceMode || m.activeRepos == nil) && len(filteredIssues) == len(m.snapshot.Issues)
		if useSnapshot {
			if recipeFilterActive {
				useSnapshot = m.snapshot.RecipeName == m.activeRecipe.Name && m.snapshot.RecipeHash == recipeFingerprint(m.activeRecipe)
			} else {
				useSnapshot = m.currentFilter == "all"
			}
		}
		if useSnapshot {
			m.board.SetSnapshot(m.snapshot)
		} else {
			m.board.SetIssues(filteredIssues)
		}
	}

	if m.isGraphView {
		useSnapshot := m.snapshot != nil && m.snapshot.GraphLayout != nil && len(filteredIssues) == len(m.snapshot.Issues)
		if useSnapshot {
			if recipeFilterActive {
				useSnapshot = m.snapshot.RecipeName == m.activeRecipe.Name && m.snapshot.RecipeHash == recipeFingerprint(m.activeRecipe)
			} else {
				useSnapshot = m.currentFilter == "all"
			}
		}
		if useSnapshot {
			m.graphView.SetSnapshot(m.snapshot)
		} else {
			filterIns := m.analysis.GenerateInsights(len(filteredIssues))
			m.graphView.SetIssues(filteredIssues, &filterIns)
		}
	}
}

func (m *Model) applyFilter() {
	var filteredItems []list.Item
	var filteredIssues []model.Issue

	for _, issue := range m.issues {
		if m.matchesCurrentFilter(issue) {
			// Use pre-computed graph scores (avoid redundant calculation)
			item := IssueItem{
				Issue:      issue,
				GraphScore: m.analysis.GetPageRankScore(issue.ID),
				Impact:     m.analysis.GetCriticalPathScore(issue.ID),
				DiffStatus: m.getDiffStatus(issue.ID),
				RepoPrefix: ExtractRepoPrefix(issue.ID),
			}
			// Add triage data (bv-151)
			item.TriageScore = m.triageScores[issue.ID]
			if reasons, exists := m.triageReasons[issue.ID]; exists {
				item.TriageReason = reasons.Primary
				item.TriageReasons = reasons.All
			}
			item.IsQuickWin = m.quickWinSet[issue.ID]
			item.IsBlocker = m.blockerSet[issue.ID]
			item.UnblocksCount = len(m.unblocksMap[issue.ID])
			filteredItems = append(filteredItems, item)
			filteredIssues = append(filteredIssues, issue)
		}
	}

	// Apply sort mode (bv-3ita)
	m.sortFilteredItems(filteredItems, filteredIssues)

	m.list.SetItems(filteredItems)
	m.updateSemanticIDs(filteredItems)
	if m.snapshot != nil && m.snapshot.BoardState != nil && m.currentFilter == "all" && (!m.workspaceMode || m.activeRepos == nil) && len(filteredIssues) == len(m.snapshot.Issues) {
		m.board.SetSnapshot(m.snapshot)
	} else {
		m.board.SetIssues(filteredIssues)
	}
	if m.snapshot != nil && m.snapshot.GraphLayout != nil && m.currentFilter == "all" && len(filteredIssues) == len(m.snapshot.Issues) {
		m.graphView.SetSnapshot(m.snapshot)
	} else {
		// Generate insights for graph view (for metric rankings and sorting)
		filterIns := m.analysis.GenerateInsights(len(filteredIssues))
		m.graphView.SetIssues(filteredIssues, &filterIns)
	}

	// Keep selection in bounds
	if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
		m.list.Select(0)
	}
	m.updateViewportContent()
}

// refreshListItemsPhase2 updates visible items with Phase 2 scores and triage data
// without rebuilding the filtered set.
func (m *Model) refreshListItemsPhase2() {
	items := m.list.Items()
	if len(items) == 0 {
		return
	}

	selectedIdx := m.list.Index()
	for i := range items {
		item, ok := items[i].(IssueItem)
		if !ok {
			continue
		}
		issueID := item.Issue.ID
		if m.analysis != nil {
			item.GraphScore = m.analysis.GetPageRankScore(issueID)
			item.Impact = m.analysis.GetCriticalPathScore(issueID)
		}
		item.TriageScore = m.triageScores[issueID]
		if reasons, exists := m.triageReasons[issueID]; exists {
			item.TriageReason = reasons.Primary
			item.TriageReasons = reasons.All
		} else {
			item.TriageReason = ""
			item.TriageReasons = nil
		}
		item.IsQuickWin = m.quickWinSet[issueID]
		item.IsBlocker = m.blockerSet[issueID]
		item.UnblocksCount = len(m.unblocksMap[issueID])
		items[i] = item
	}

	m.list.SetItems(items)
	if selectedIdx >= 0 && selectedIdx < len(items) {
		m.list.Select(selectedIdx)
	}
	m.updateViewportContent()
}

// cycleSortMode cycles through available sort modes (bv-3ita)
func (m *Model) cycleSortMode() {
	m.sortMode = (m.sortMode + 1) % numSortModes
	m.applyFilter() // Re-apply filter with new sort
}

// sortFilteredItems sorts the filtered items based on current sortMode (bv-3ita)
func (m *Model) sortFilteredItems(items []list.Item, issues []model.Issue) {
	if len(items) == 0 {
		return
	}

	// Sort indices to keep items and issues in sync
	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}

	sort.Slice(indices, func(i, j int) bool {
		iItem := items[indices[i]].(IssueItem)
		jItem := items[indices[j]].(IssueItem)

		switch m.sortMode {
		case SortCreatedAsc:
			// Oldest first
			return iItem.Issue.CreatedAt.Before(jItem.Issue.CreatedAt)
		case SortCreatedDesc:
			// Newest first
			return iItem.Issue.CreatedAt.After(jItem.Issue.CreatedAt)
		case SortPriority:
			// Priority ascending (P0 first)
			return iItem.Issue.Priority < jItem.Issue.Priority
		case SortUpdated:
			// Most recently updated first
			return iItem.Issue.UpdatedAt.After(jItem.Issue.UpdatedAt)
		default:
			// Default: Open first, then priority, then newest
			iClosed := isClosedLikeStatus(iItem.Issue.Status)
			jClosed := isClosedLikeStatus(jItem.Issue.Status)
			if iClosed != jClosed {
				return !iClosed
			}
			if iItem.Issue.Priority != jItem.Issue.Priority {
				return iItem.Issue.Priority < jItem.Issue.Priority
			}
			return iItem.Issue.CreatedAt.After(jItem.Issue.CreatedAt)
		}
	})

	// Reorder items and issues based on sorted indices
	sortedItems := make([]list.Item, len(items))
	sortedIssues := make([]model.Issue, len(issues))
	for newIdx, oldIdx := range indices {
		sortedItems[newIdx] = items[oldIdx]
		sortedIssues[newIdx] = issues[oldIdx]
	}
	copy(items, sortedItems)
	copy(issues, sortedIssues)
}

func matchesRecipeStatus(status model.Status, filter string) bool {
	normalized := strings.ToLower(strings.TrimSpace(filter))
	statusKey := strings.ToLower(string(status))
	switch normalized {
	case string(model.StatusClosed):
		return isClosedLikeStatus(status)
	case string(model.StatusTombstone):
		return status == model.StatusTombstone
	case string(model.StatusOpen):
		return status == model.StatusOpen
	case string(model.StatusInProgress):
		return status == model.StatusInProgress
	case string(model.StatusBlocked):
		return status == model.StatusBlocked
	default:
		return statusKey == normalized
	}
}

// applyRecipe applies a recipe's filters and sort to the current view
func (m *Model) applyRecipe(r *recipe.Recipe) {
	if r == nil {
		return
	}

	var filteredItems []list.Item
	var filteredIssues []model.Issue

	for _, issue := range m.issues {
		include := true

		// Workspace repo filter (nil = all repos)
		if m.workspaceMode && m.activeRepos != nil {
			repoKey := strings.ToLower(ExtractRepoPrefix(issue.ID))
			if repoKey != "" && !m.activeRepos[repoKey] {
				include = false
			}
		}

		// Apply status filter
		if len(r.Filters.Status) > 0 {
			statusMatch := false
			for _, s := range r.Filters.Status {
				if matchesRecipeStatus(issue.Status, s) {
					statusMatch = true
					break
				}
			}
			include = include && statusMatch
		}

		// Apply priority filter
		if include && len(r.Filters.Priority) > 0 {
			prioMatch := false
			for _, p := range r.Filters.Priority {
				if issue.Priority == p {
					prioMatch = true
					break
				}
			}
			include = include && prioMatch
		}

		// Apply tags filter (must have ALL specified tags)
		if include && len(r.Filters.Tags) > 0 {
			labelSet := make(map[string]bool)
			for _, l := range issue.Labels {
				labelSet[l] = true
			}
			for _, required := range r.Filters.Tags {
				if !labelSet[required] {
					include = false
					break
				}
			}
		}

		// Apply actionable filter
		if include && r.Filters.Actionable != nil && *r.Filters.Actionable {
			// Check if issue is blocked
			isBlocked := false
			for _, dep := range issue.Dependencies {
				if dep == nil || !dep.Type.IsBlocking() {
					continue
				}
				if blocker, exists := m.issueMap[dep.DependsOnID]; exists && !isClosedLikeStatus(blocker.Status) {
					isBlocked = true
					break
				}
			}
			include = !isBlocked
		}

		if include {
			item := IssueItem{
				Issue:      issue,
				GraphScore: m.analysis.GetPageRankScore(issue.ID),
				Impact:     m.analysis.GetCriticalPathScore(issue.ID),
				DiffStatus: m.getDiffStatus(issue.ID),
				RepoPrefix: ExtractRepoPrefix(issue.ID),
			}
			// Add triage data (bv-151)
			item.TriageScore = m.triageScores[issue.ID]
			if reasons, exists := m.triageReasons[issue.ID]; exists {
				item.TriageReason = reasons.Primary
				item.TriageReasons = reasons.All
			}
			item.IsQuickWin = m.quickWinSet[issue.ID]
			item.IsBlocker = m.blockerSet[issue.ID]
			item.UnblocksCount = len(m.unblocksMap[issue.ID])
			filteredItems = append(filteredItems, item)
			filteredIssues = append(filteredIssues, issue)
		}
	}

	// Apply sort
	field := r.Sort.Field
	descending := r.Sort.Direction == "desc"
	if field != "" {
		compare := func(a, b model.Issue) int {
			switch field {
			case "priority":
				switch {
				case a.Priority < b.Priority:
					return -1
				case a.Priority > b.Priority:
					return 1
				default:
					return 0
				}
			case "created", "created_at":
				switch {
				case a.CreatedAt.Before(b.CreatedAt):
					return -1
				case a.CreatedAt.After(b.CreatedAt):
					return 1
				default:
					return 0
				}
			case "updated", "updated_at":
				switch {
				case a.UpdatedAt.Before(b.UpdatedAt):
					return -1
				case a.UpdatedAt.After(b.UpdatedAt):
					return 1
				default:
					return 0
				}
			case "impact":
				if m.analysis == nil {
					switch {
					case a.Priority < b.Priority:
						return -1
					case a.Priority > b.Priority:
						return 1
					default:
						return 0
					}
				}
				aScore := m.analysis.GetCriticalPathScore(a.ID)
				bScore := m.analysis.GetCriticalPathScore(b.ID)
				switch {
				case aScore < bScore:
					return -1
				case aScore > bScore:
					return 1
				default:
					return 0
				}
			case "pagerank":
				if m.analysis == nil {
					switch {
					case a.Priority < b.Priority:
						return -1
					case a.Priority > b.Priority:
						return 1
					default:
						return 0
					}
				}
				aScore := m.analysis.GetPageRankScore(a.ID)
				bScore := m.analysis.GetPageRankScore(b.ID)
				switch {
				case aScore < bScore:
					return -1
				case aScore > bScore:
					return 1
				default:
					return 0
				}
			default:
				switch {
				case a.Priority < b.Priority:
					return -1
				case a.Priority > b.Priority:
					return 1
				default:
					return 0
				}
			}
		}

		sort.Slice(filteredItems, func(i, j int) bool {
			iItem := filteredItems[i].(IssueItem)
			jItem := filteredItems[j].(IssueItem)

			cmp := compare(iItem.Issue, jItem.Issue)
			if cmp == 0 {
				return iItem.Issue.ID < jItem.Issue.ID
			}
			if descending {
				return cmp > 0
			}
			return cmp < 0
		})

		// Re-sort issues list too
		sort.Slice(filteredIssues, func(i, j int) bool {
			ii := filteredIssues[i]
			jj := filteredIssues[j]

			cmp := compare(ii, jj)
			if cmp == 0 {
				return ii.ID < jj.ID
			}
			if descending {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	m.list.SetItems(filteredItems)
	m.updateSemanticIDs(filteredItems)
	m.board.SetIssues(filteredIssues)
	// Generate insights for graph view (for metric rankings and sorting)
	recipeIns := m.analysis.GenerateInsights(len(filteredIssues))
	m.graphView.SetIssues(filteredIssues, &recipeIns)

	// Update filter indicator
	m.currentFilter = "recipe:" + r.Name

	// Keep selection in bounds
	if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
		m.list.Select(0)
	}
	m.updateViewportContent()
}

// recalculateSplitPaneSizes updates list and viewport dimensions after pane ratio changes
func (m *Model) recalculateSplitPaneSizes() {
	if !m.isSplitView {
		return
	}

	bodyHeight := m.height - 1
	if bodyHeight < 5 {
		bodyHeight = 5
	}

	// Calculate dimensions accounting for 2 panels with borders(2)+padding(2) = 4 overhead each
	availWidth := m.width - 8
	if availWidth < 10 {
		availWidth = 10
	}

	listInnerWidth := int(float64(availWidth) * m.splitPaneRatio)
	detailInnerWidth := availWidth - listInnerWidth

	listHeight := bodyHeight - 4
	if listHeight < 3 {
		listHeight = 3
	}

	m.list.SetSize(listInnerWidth, listHeight)
	m.viewport = viewport.New(detailInnerWidth, bodyHeight-2)
	m.renderer.SetWidthWithTheme(detailInnerWidth, m.theme)
	m.updateViewportContent()
}

func (m *Model) updateViewportContent() {
	selectedItem := m.list.SelectedItem()
	if selectedItem == nil {
		m.viewport.SetContent("No issues selected")
		return
	}

	// Safe type assertion
	issueItem, ok := selectedItem.(IssueItem)
	if !ok {
		m.viewport.SetContent("Error: invalid item type")
		return
	}
	item := issueItem.Issue

	var sb strings.Builder

	if m.updateAvailable {
		sb.WriteString(fmt.Sprintf("⭐ **Update Available:** [%s](%s)\n\n", m.updateTag, m.updateURL))
	}

	// Title Block
	sb.WriteString(fmt.Sprintf("# %s %s\n", GetTypeIconMD(string(item.IssueType)), item.Title))

	// Meta Table
	sb.WriteString("| ID | Status | Priority | Assignee | Created |\n|---|---|---|---|---|\n")
	sb.WriteString(fmt.Sprintf("| **%s** | **%s** | %s | @%s | %s |\n\n",
		item.ID,
		strings.ToUpper(string(item.Status)),
		GetPriorityIcon(item.Priority),
		item.Assignee,
		item.CreatedAt.Format("2006-01-02"),
	))

	// Labels (bv-f103 fix: display labels in detail view)
	if len(item.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels:** %s\n\n", strings.Join(item.Labels, ", ")))
	}

	// Triage Insights (bv-151)
	if issueItem.TriageScore > 0 || issueItem.TriageReason != "" || issueItem.UnblocksCount > 0 || issueItem.IsQuickWin || issueItem.IsBlocker {
		sb.WriteString("### 🎯 Triage Insights\n")

		// Score with visual indicator
		scoreIcon := "🔵"
		if issueItem.TriageScore >= 0.7 {
			scoreIcon = "🔴"
		} else if issueItem.TriageScore >= 0.4 {
			scoreIcon = "🟠"
		}
		sb.WriteString(fmt.Sprintf("- **Triage Score:** %s %.2f/1.00\n", scoreIcon, issueItem.TriageScore))

		// Special flags
		if issueItem.IsQuickWin {
			sb.WriteString("- **⭐ Quick Win** — Low effort, high impact opportunity\n")
		}
		if issueItem.IsBlocker {
			sb.WriteString("- **🔴 Critical Blocker** — Completing this unblocks significant downstream work\n")
		}

		// Unblocks count
		if issueItem.UnblocksCount > 0 {
			sb.WriteString(fmt.Sprintf("- **🔓 Unblocks:** %d downstream items when completed\n", issueItem.UnblocksCount))
		}

		// Primary reason
		if issueItem.TriageReason != "" {
			sb.WriteString(fmt.Sprintf("- **Primary Reason:** %s\n", issueItem.TriageReason))
		}

		// All reasons (if multiple)
		if len(issueItem.TriageReasons) > 1 {
			sb.WriteString("- **All Reasons:**\n")
			for _, reason := range issueItem.TriageReasons {
				sb.WriteString(fmt.Sprintf("  - %s\n", reason))
			}
		}

		sb.WriteString("\n")
	}

	// Search Scores (hybrid mode)
	if m.semanticSearchEnabled && m.semanticHybridEnabled && issueItem.SearchScoreSet && m.list.FilterState() != list.Unfiltered {
		sb.WriteString("### 🔎 Search Scores\n")
		sb.WriteString(fmt.Sprintf("- **Hybrid Score:** %.3f\n", issueItem.SearchScore))
		sb.WriteString(fmt.Sprintf("- **Text Score:** %.3f\n", issueItem.SearchTextScore))
		if len(issueItem.SearchComponents) > 0 {
			sb.WriteString("- **Components:**\n")
			order := []string{"pagerank", "status", "impact", "priority", "recency"}
			for _, key := range order {
				if val, ok := issueItem.SearchComponents[key]; ok {
					sb.WriteString(fmt.Sprintf("  - %s: %.3f\n", key, val))
				}
			}
		}
		sb.WriteString("\n")
	}

	// Graph Analysis (using thread-safe accessors)
	pr := m.analysis.GetPageRankScore(item.ID)
	bt := m.analysis.GetBetweennessScore(item.ID)
	imp := m.analysis.GetCriticalPathScore(item.ID)
	ev := m.analysis.GetEigenvectorScore(item.ID)
	hub := m.analysis.GetHubScore(item.ID)
	auth := m.analysis.GetAuthorityScore(item.ID)

	sb.WriteString("### Graph Analysis\n")
	sb.WriteString(fmt.Sprintf("- **Impact Depth**: %.0f (downstream chain length)\n", imp))
	sb.WriteString(fmt.Sprintf("- **Centrality**: PR %.4f • BW %.4f • EV %.4f\n", pr, bt, ev))
	sb.WriteString(fmt.Sprintf("- **Flow Role**: Hub %.4f • Authority %.4f\n\n", hub, auth))

	// Description
	if item.Description != "" {
		sb.WriteString("### Description\n")
		sb.WriteString(item.Description + "\n\n")
	}

	// Design Notes
	if item.Design != "" {
		sb.WriteString("### Design Notes\n")
		sb.WriteString(item.Design + "\n\n")
	}

	// Acceptance Criteria
	if item.AcceptanceCriteria != "" {
		sb.WriteString("### Acceptance Criteria\n")
		sb.WriteString(item.AcceptanceCriteria + "\n\n")
	}

	// Notes
	if item.Notes != "" {
		sb.WriteString("### Notes\n")
		sb.WriteString(item.Notes + "\n\n")
	}

	// Dependency Graph (Tree)
	if len(item.Dependencies) > 0 {
		rootNode := BuildDependencyTree(item.ID, m.issueMap, 3) // Max depth 3
		treeStr := RenderDependencyTree(rootNode)
		sb.WriteString("```\n" + treeStr + "```\n\n")
	}

	// Comments
	if len(item.Comments) > 0 {
		sb.WriteString(fmt.Sprintf("### Comments (%d)\n", len(item.Comments)))
		for _, comment := range item.Comments {
			sb.WriteString(fmt.Sprintf("> **%s** (%s)\n> \n> %s\n\n",
				comment.Author,
				FormatTimeRel(comment.CreatedAt),
				strings.ReplaceAll(comment.Text, "\n", "\n> ")))
		}
	}

	// History Section (if data is loaded)
	if m.historyView.HasReport() {
		historyMD := m.renderBeadHistoryMD(item.ID)
		if historyMD != "" {
			sb.WriteString(historyMD)
		}
	}

	rendered, err := m.renderer.Render(sb.String())
	if err != nil {
		m.viewport.SetContent(fmt.Sprintf("Error rendering markdown: %v", err))
	} else {
		m.viewport.SetContent(rendered)
	}
}

// renderBeadHistoryMD generates markdown for a bead's history
func (m *Model) renderBeadHistoryMD(beadID string) string {
	hist := m.historyView.GetHistoryForBead(beadID)
	if hist == nil || len(hist.Commits) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### 📜 History\n\n")

	// Lifecycle milestones from events
	if len(hist.Events) > 0 {
		sb.WriteString("**Lifecycle:**\n")
		for _, event := range hist.Events {
			icon := getEventIcon(event.EventType)
			sb.WriteString(fmt.Sprintf("- %s **%s** %s by %s\n",
				icon,
				event.EventType,
				event.Timestamp.Format("Jan 02 15:04"),
				event.Author,
			))
		}
		sb.WriteString("\n")
	}

	// Correlated commits
	sb.WriteString(fmt.Sprintf("**Related Commits (%d):**\n", len(hist.Commits)))
	for i, commit := range hist.Commits {
		if i >= 5 {
			sb.WriteString(fmt.Sprintf("  ... and %d more commits\n", len(hist.Commits)-5))
			break
		}

		// Confidence indicator
		confIcon := "🟢"
		if commit.Confidence < 0.5 {
			confIcon = "🟡"
		} else if commit.Confidence < 0.8 {
			confIcon = "🟠"
		}

		sb.WriteString(fmt.Sprintf("- %s **%.0f%%** `%s` %s\n",
			confIcon,
			commit.Confidence*100,
			commit.ShortSHA,
			truncateString(commit.Message, 40),
		))

		// Show files for high-confidence commits
		if commit.Confidence >= 0.8 && len(commit.Files) > 0 && len(commit.Files) <= 3 {
			for _, f := range commit.Files {
				sb.WriteString(fmt.Sprintf("  - `%s` (+%d, -%d)\n", f.Path, f.Insertions, f.Deletions))
			}
		}
	}

	sb.WriteString("\n*Press H for full history view*\n\n")
	return sb.String()
}

// getEventIcon returns an icon for bead event types
func getEventIcon(eventType correlation.EventType) string {
	switch eventType {
	case correlation.EventCreated:
		return "🟢"
	case correlation.EventClaimed:
		return "🔵"
	case correlation.EventClosed:
		return "⚫"
	case correlation.EventReopened:
		return "🟡"
	case correlation.EventModified:
		return "📝"
	default:
		return "•"
	}
}

// truncateString truncates a string to maxLen runes with ellipsis.
// Uses rune-based counting to safely handle UTF-8 multi-byte characters.
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

// GetTypeIconMD returns the emoji icon for an issue type (for markdown)
func GetTypeIconMD(t string) string {
	switch t {
	case "bug":
		return "🐛"
	case "feature":
		return "✨"
	case "task":
		return "📋"
	case "epic":
		return "🚀" // Use rocket instead of mountain - VS-16 variation selector causes width issues
	case "chore":
		return "🧹"
	default:
		return "•"
	}
}

// SetFilter sets the current filter and applies it (exposed for testing)
func (m *Model) SetFilter(f string) {
	m.currentFilter = f
	m.applyFilter()
}

// FilteredIssues returns the currently visible issues (exposed for testing)
func (m Model) FilteredIssues() []model.Issue {
	items := m.list.Items()
	issues := make([]model.Issue, 0, len(items))
	for _, item := range items {
		if issueItem, ok := item.(IssueItem); ok {
			issues = append(issues, issueItem.Issue)
		}
	}
	return issues
}

// EnableWorkspaceMode configures the model for workspace (multi-repo) view
func (m *Model) EnableWorkspaceMode(info WorkspaceInfo) {
	m.workspaceMode = info.Enabled
	m.availableRepos = normalizeRepoPrefixes(info.RepoPrefixes)
	m.activeRepos = nil // nil means all repos are active

	if info.RepoCount > 0 {
		if info.FailedCount > 0 {
			m.workspaceSummary = fmt.Sprintf("%d/%d repos", info.RepoCount-info.FailedCount, info.RepoCount)
		} else {
			m.workspaceSummary = fmt.Sprintf("%d repos", info.RepoCount)
		}
	}

	// Update delegate to show repo badges
	m.updateListDelegate()
}

// IsWorkspaceMode returns whether workspace mode is active
func (m Model) IsWorkspaceMode() bool {
	return m.workspaceMode
}

// enterHistoryView loads correlation data and shows the history view
func (m *Model) enterHistoryView() {
	cwd, err := os.Getwd()
	if err != nil {
		m.statusMsg = "Cannot get working directory for history"
		m.statusIsError = true
		return
	}

	// Convert model.Issue to correlation.BeadInfo
	beads := make([]correlation.BeadInfo, len(m.issues))
	for i, issue := range m.issues {
		beads[i] = correlation.BeadInfo{
			ID:     issue.ID,
			Title:  issue.Title,
			Status: string(issue.Status),
		}
	}

	// Load correlation data
	correlator := correlation.NewCorrelator(cwd, m.beadsPath)
	opts := correlation.CorrelatorOptions{
		Limit: 500, // Reasonable limit for TUI performance
	}

	report, err := correlator.GenerateReport(beads, opts)
	if err != nil {
		m.statusMsg = fmt.Sprintf("History load failed: %v", err)
		m.statusIsError = true
		return
	}

	// Initialize or update history view
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyView.SetSize(m.width, m.height-1)
	m.isHistoryView = true
	m.focused = focusHistory

	m.statusMsg = fmt.Sprintf("Loaded history: %d beads with commits", report.Stats.BeadsWithCommits)
	m.statusIsError = false
}

// enterTimeTravelMode loads historical data and computes diff
func (m *Model) enterTimeTravelMode(revision string) {
	cwd, err := os.Getwd()
	if err != nil {
		m.statusMsg = "❌ Time-travel failed: cannot get working directory"
		m.statusIsError = true
		return
	}

	gitLoader := loader.NewGitLoader(cwd)

	// Check if we're in a git repo first
	if _, err := gitLoader.ResolveRevision("HEAD"); err != nil {
		m.statusMsg = "❌ Time-travel requires a git repository"
		m.statusIsError = true
		return
	}

	// Check if beads files exist at the revision
	hasBeads, err := gitLoader.HasBeadsAtRevision(revision)
	if err != nil || !hasBeads {
		m.statusMsg = fmt.Sprintf("❌ No beads history at %s (try fewer commits back)", revision)
		m.statusIsError = true
		return
	}

	// Load historical issues
	historicalIssues, err := gitLoader.LoadAt(revision)
	if err != nil {
		m.statusMsg = fmt.Sprintf("❌ Time-travel failed: %v", err)
		m.statusIsError = true
		return
	}

	// Create snapshots and compute diff
	fromSnapshot := analysis.NewSnapshot(historicalIssues)
	toSnapshot := analysis.NewSnapshot(m.issues)
	diff := analysis.CompareSnapshots(fromSnapshot, toSnapshot)

	// Build lookup sets for badges
	m.newIssueIDs = make(map[string]bool)
	for _, issue := range diff.NewIssues {
		m.newIssueIDs[issue.ID] = true
	}

	m.closedIssueIDs = make(map[string]bool)
	for _, issue := range diff.ClosedIssues {
		m.closedIssueIDs[issue.ID] = true
	}

	m.modifiedIssueIDs = make(map[string]bool)
	for _, mod := range diff.ModifiedIssues {
		m.modifiedIssueIDs[mod.IssueID] = true
	}

	m.timeTravelMode = true
	m.timeTravelDiff = diff
	m.timeTravelSince = revision

	// Success feedback
	m.statusMsg = fmt.Sprintf("⏱️ Time-travel: comparing with %s (+%d ✅%d ~%d)",
		revision, diff.Summary.IssuesAdded, diff.Summary.IssuesClosed, diff.Summary.IssuesModified)
	m.statusIsError = false

	// Rebuild list items with diff info
	m.rebuildListWithDiffInfo()
}

// exitTimeTravelMode clears time-travel state
func (m *Model) exitTimeTravelMode() {
	m.timeTravelMode = false
	m.timeTravelDiff = nil
	m.timeTravelSince = ""
	m.newIssueIDs = nil
	m.closedIssueIDs = nil
	m.modifiedIssueIDs = nil

	// Feedback
	m.statusMsg = "⏱️ Time-travel mode disabled"
	m.statusIsError = false

	// Rebuild list without diff info
	m.rebuildListWithDiffInfo()
}

// rebuildListWithDiffInfo recreates list items with current diff state
func (m *Model) rebuildListWithDiffInfo() {
	if m.activeRecipe != nil {
		m.applyRecipe(m.activeRecipe)
	} else {
		m.applyFilter()
	}
}

// IsTimeTravelMode returns whether time-travel mode is active
func (m Model) IsTimeTravelMode() bool {
	return m.timeTravelMode
}

// TimeTravelDiff returns the current diff (nil if not in time-travel mode)
func (m Model) TimeTravelDiff() *analysis.SnapshotDiff {
	return m.timeTravelDiff
}

// FocusState returns the current focus state as a string for testing (bv-5e5q).
// This enables testing focus transitions without exposing the internal focus type.
func (m Model) FocusState() string {
	switch m.focused {
	case focusList:
		return "list"
	case focusDetail:
		return "detail"
	case focusBoard:
		return "board"
	case focusGraph:
		return "graph"
	case focusTree:
		return "tree"
	case focusLabelDashboard:
		return "label_dashboard"
	case focusInsights:
		return "insights"
	case focusActionable:
		return "actionable"
	case focusRecipePicker:
		return "recipe_picker"
	case focusRepoPicker:
		return "repo_picker"
	case focusHelp:
		return "help"
	case focusQuitConfirm:
		return "quit_confirm"
	case focusTimeTravelInput:
		return "time_travel_input"
	case focusHistory:
		return "history"
	case focusAttention:
		return "attention"
	case focusLabelPicker:
		return "label_picker"
	case focusSprint:
		return "sprint"
	case focusAgentPrompt:
		return "agent_prompt"
	case focusFlowMatrix:
		return "flow_matrix"
	case focusTutorial:
		return "tutorial"
	case focusCassModal:
		return "cass_modal"
	case focusUpdateModal:
		return "update_modal"
	default:
		return "unknown"
	}
}

// IsBoardView returns true if the board view is active (bv-5e5q).
func (m Model) IsBoardView() bool {
	return m.isBoardView
}

// IsGraphView returns true if the graph view is active (bv-5e5q).
func (m Model) IsGraphView() bool {
	return m.isGraphView
}

// IsActionableView returns true if the actionable view is active (bv-5e5q).
func (m Model) IsActionableView() bool {
	return m.isActionableView
}

// IsHistoryView returns true if the history view is active (bv-5e5q).
func (m Model) IsHistoryView() bool {
	return m.isHistoryView
}

// exportToMarkdown exports all issues to a Markdown file with auto-generated filename
func (m *Model) exportToMarkdown() {
	// Generate smart filename: beads_report_<project>_YYYY-MM-DD.md
	filename := m.generateExportFilename()

	// Export the issues
	err := export.SaveMarkdownToFile(m.issues, filename)
	if err != nil {
		m.statusMsg = fmt.Sprintf("❌ Export failed: %v", err)
		m.statusIsError = true
		return
	}

	m.statusMsg = fmt.Sprintf("✅ Exported %d issues to %s", len(m.issues), filename)
	m.statusIsError = false
}

// generateExportFilename creates a smart filename based on project and date
func (m *Model) generateExportFilename() string {
	// Get project name from current directory
	projectName := "beads"
	if cwd, err := os.Getwd(); err == nil {
		projectName = filepath.Base(cwd)
		// Sanitize: replace spaces and special chars with underscores
		projectName = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				return r
			}
			return '_'
		}, projectName)
	}

	// Format: beads_report_<project>_YYYY-MM-DD.md
	timestamp := time.Now().Format("2006-01-02")
	return fmt.Sprintf("beads_report_%s_%s.md", projectName, timestamp)
}

// renderTimeTravelPrompt renders the time-travel revision input overlay
func (m Model) renderTimeTravelPrompt() string {
	t := m.theme

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 3).
		Align(lipgloss.Center)

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	subtitleStyle := t.Renderer.NewStyle().
		Foreground(t.Subtext).
		Italic(true)

	exampleStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary)

	keyStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	textStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	// Build content
	content := titleStyle.Render("⏱️  Time-Travel Mode") + "\n\n" +
		subtitleStyle.Render("Compare current state with a historical revision") + "\n\n" +
		m.timeTravelInput.View() + "\n\n" +
		exampleStyle.Render("Examples: HEAD~5, main, v1.0.0, 2024-01-01, abc123") + "\n\n" +
		textStyle.Render("Press ") + keyStyle.Render("Enter") + textStyle.Render(" to compare, ") +
		keyStyle.Render("Esc") + textStyle.Render(" to cancel")

	box := boxStyle.Render(content)

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		box,
	)
}

// copyIssueToClipboard copies the selected issue to clipboard as Markdown
func (m *Model) copyIssueToClipboard() {
	selectedItem := m.list.SelectedItem()
	if selectedItem == nil {
		m.statusMsg = "❌ No issue selected"
		m.statusIsError = true
		return
	}

	issueItem, ok := selectedItem.(IssueItem)
	if !ok {
		m.statusMsg = "❌ Invalid item type"
		m.statusIsError = true
		return
	}
	issue := issueItem.Issue

	// Format issue as Markdown
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s %s\n\n", GetTypeIconMD(string(issue.IssueType)), issue.Title))
	sb.WriteString(fmt.Sprintf("**ID:** %s  \n", issue.ID))
	sb.WriteString(fmt.Sprintf("**Status:** %s  \n", strings.ToUpper(string(issue.Status))))
	sb.WriteString(fmt.Sprintf("**Priority:** P%d  \n", issue.Priority))
	if issue.Assignee != "" {
		sb.WriteString(fmt.Sprintf("**Assignee:** @%s  \n", issue.Assignee))
	}
	sb.WriteString(fmt.Sprintf("**Created:** %s  \n", issue.CreatedAt.Format("2006-01-02")))

	if len(issue.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels:** %s  \n", strings.Join(issue.Labels, ", ")))
	}

	if issue.Description != "" {
		sb.WriteString(fmt.Sprintf("\n## Description\n\n%s\n", issue.Description))
	}

	if issue.AcceptanceCriteria != "" {
		sb.WriteString(fmt.Sprintf("\n## Acceptance Criteria\n\n%s\n", issue.AcceptanceCriteria))
	}

	// Dependencies
	if len(issue.Dependencies) > 0 {
		sb.WriteString("\n## Dependencies\n\n")
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("- %s (%s)\n", dep.DependsOnID, dep.Type))
		}
	}

	// Copy to clipboard
	err := clipboard.WriteAll(sb.String())
	if err != nil {
		m.statusMsg = fmt.Sprintf("❌ Clipboard error: %v", err)
		m.statusIsError = true
		return
	}

	m.statusMsg = fmt.Sprintf("📋 Copied %s to clipboard", issue.ID)
	m.statusIsError = false
}

// showCassSessionModal shows the cass session preview modal for the selected issue (bv-5bqh)
func (m *Model) showCassSessionModal() {
	// Get the currently selected issue
	selectedItem := m.list.SelectedItem()
	if selectedItem == nil {
		return
	}

	issueItem, ok := selectedItem.(IssueItem)
	if !ok {
		return
	}
	issue := issueItem.Issue

	// Check if cass is available
	if m.cassCorrelator == nil {
		// Initialize correlator lazily
		detector := cass.NewDetector()
		if detector.Check() != cass.StatusHealthy {
			m.statusMsg = "⚠️ cass not available (install it for session correlation)"
			m.statusIsError = false
			return
		}
		searcher := cass.NewSearcher(detector)
		cache := cass.NewCache()
		m.cassCorrelator = cass.NewCorrelator(searcher, cache, m.workDir)
	}

	// Run correlation
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := m.cassCorrelator.Correlate(ctx, &issue)

	// If no sessions found, just show a status message
	if len(result.TopSessions) == 0 {
		m.statusMsg = "No correlated sessions found for " + issue.ID
		m.statusIsError = false
		return
	}

	// Create and show the modal
	m.cassModal = NewCassSessionModal(issue.ID, result, m.theme)
	m.cassModal.SetSize(m.width, m.height)
	m.showCassModal = true
	m.focused = focusCassModal
}

// showSelfUpdateModal shows the self-update modal (bv-182)
func (m *Model) showSelfUpdateModal() {
	// Check if an update is available
	if !m.updateAvailable || m.updateTag == "" {
		m.statusMsg = "No update available - you're running the latest version"
		m.statusIsError = false
		return
	}

	// Create and show the modal
	m.updateModal = NewUpdateModal(m.updateTag, m.updateURL, m.theme)
	m.updateModal.SetSize(m.width, m.height)
	m.showUpdateModal = true
	m.focused = focusUpdateModal
}

// getCassSessionCount returns the cached session count for the selected bead (bv-y836)
// Returns 0 if no sessions found, cass not available, or no bead selected.
// This method only checks the cache - it never triggers new correlation requests.
func (m *Model) getCassSessionCount() int {
	if m.cassCorrelator == nil {
		return 0
	}

	// Get the currently selected issue
	selectedItem := m.list.SelectedItem()
	if selectedItem == nil {
		return 0
	}

	issueItem, ok := selectedItem.(IssueItem)
	if !ok {
		return 0
	}

	// Check the cache for this bead
	if hint := m.cassCorrelator.GetCached(issueItem.Issue.ID); hint != nil {
		return hint.ResultCount
	}
	return 0
}

func parseCommandLine(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		args = append(args, current.String())
		current.Reset()
	}

	for i := 0; i < len(input); {
		ch := input[i]
		if inSingle {
			if ch == '\'' {
				inSingle = false
				i++
				continue
			}
			current.WriteByte(ch)
			i++
			continue
		}
		if inDouble {
			switch ch {
			case '"':
				inDouble = false
				i++
				continue
			case '\\':
				if i+1 >= len(input) {
					return nil, fmt.Errorf("unterminated escape")
				}
				next := input[i+1]
				// In double quotes, only treat \" and \\ as escapes; otherwise preserve backslash.
				if next == '"' || next == '\\' {
					current.WriteByte(next)
					i += 2
					continue
				}
				current.WriteByte('\\')
				i++
				continue
			default:
				current.WriteByte(ch)
				i++
				continue
			}
		}

		switch ch {
		case ' ', '\t', '\n', '\r':
			flush()
			i++
		case '\'':
			inSingle = true
			i++
		case '"':
			inDouble = true
			i++
		case '\\':
			if i+1 >= len(input) {
				return nil, fmt.Errorf("unterminated escape")
			}
			next := input[i+1]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' || next == '\\' || next == '"' || next == '\'' {
				current.WriteByte(next)
				i += 2
				continue
			}
			current.WriteByte('\\')
			i++
		default:
			current.WriteByte(ch)
			i++
		}
	}

	if inSingle {
		return nil, fmt.Errorf("unterminated single quote")
	}
	if inDouble {
		return nil, fmt.Errorf("unterminated double quote")
	}
	flush()
	return args, nil
}

type editorCommandKind int

const (
	editorCommandOK editorCommandKind = iota
	editorCommandEmpty
	editorCommandTerminal
	editorCommandForbidden
)

type allowlistedGUIEditorKind int

const (
	allowlistedGUIEditorUnknown allowlistedGUIEditorKind = iota
	allowlistedGUIEditorOpenText
	allowlistedGUIEditorXdgOpen
	allowlistedGUIEditorCode
	allowlistedGUIEditorCodeInsiders
	allowlistedGUIEditorCursor
	allowlistedGUIEditorGedit
	allowlistedGUIEditorKate
	allowlistedGUIEditorXed
	allowlistedGUIEditorNotepad
)

var terminalEditorExecutables = map[string]bool{
	"vim":   true,
	"vi":    true,
	"nvim":  true,
	"nano":  true,
	"emacs": true,
	"pico":  true,
	"joe":   true,
	"ne":    true,
}

var forbiddenEditorExecutables = map[string]bool{
	// Shells and command interpreters.
	"sh":         true,
	"bash":       true,
	"zsh":        true,
	"fish":       true,
	"cmd":        true,
	"powershell": true,
	"pwsh":       true,
}

func normalizeExecutableBase(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return ""
	}
	base := executable
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.ToLower(base)
	return strings.TrimSuffix(base, ".exe")
}

func classifyEditorCommand(editorArgs []string) (string, editorCommandKind) {
	if len(editorArgs) == 0 {
		return "", editorCommandEmpty
	}
	base := normalizeExecutableBase(editorArgs[0])
	if base == "" {
		return "", editorCommandEmpty
	}
	if terminalEditorExecutables[base] {
		return base, editorCommandTerminal
	}
	if forbiddenEditorExecutables[base] {
		return base, editorCommandForbidden
	}
	return base, editorCommandOK
}

func allowlistedGUIEditorKindForBase(base string) allowlistedGUIEditorKind {
	switch base {
	case "open":
		return allowlistedGUIEditorOpenText
	case "xdg-open":
		return allowlistedGUIEditorXdgOpen
	case "code":
		return allowlistedGUIEditorCode
	case "code-insiders":
		return allowlistedGUIEditorCodeInsiders
	case "cursor":
		return allowlistedGUIEditorCursor
	case "gedit":
		return allowlistedGUIEditorGedit
	case "kate":
		return allowlistedGUIEditorKate
	case "xed":
		return allowlistedGUIEditorXed
	case "notepad":
		return allowlistedGUIEditorNotepad
	default:
		return allowlistedGUIEditorUnknown
	}
}

func allowlistedGUIEditorDisplayName(kind allowlistedGUIEditorKind) string {
	switch kind {
	case allowlistedGUIEditorOpenText:
		return "default text editor"
	case allowlistedGUIEditorXdgOpen:
		return "default app"
	case allowlistedGUIEditorCode:
		return "code"
	case allowlistedGUIEditorCodeInsiders:
		return "code-insiders"
	case allowlistedGUIEditorCursor:
		return "cursor"
	case allowlistedGUIEditorGedit:
		return "gedit"
	case allowlistedGUIEditorKate:
		return "kate"
	case allowlistedGUIEditorXed:
		return "xed"
	case allowlistedGUIEditorNotepad:
		return "notepad"
	default:
		return "editor"
	}
}

func startAllowlistedGUIEditor(kind allowlistedGUIEditorKind, targetFile string) (allowlistedGUIEditorKind, error) {
	switch kind {
	case allowlistedGUIEditorOpenText:
		return kind, exec.Command("open", "-t", targetFile).Start()
	case allowlistedGUIEditorXdgOpen:
		return kind, exec.Command("xdg-open", targetFile).Start()
	case allowlistedGUIEditorCode:
		if runtime.GOOS == "darwin" {
			// Prefer launching the app directly so we don't depend on the `code` CLI being installed in PATH.
			if err := exec.Command("open", "-a", "Visual Studio Code", targetFile).Start(); err == nil {
				return kind, nil
			}
		}
		if _, err := exec.LookPath("code"); err == nil {
			return kind, exec.Command("code", targetFile).Start()
		}
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("xdg-open"); err == nil {
				return allowlistedGUIEditorXdgOpen, exec.Command("xdg-open", targetFile).Start()
			}
		}
		return kind, fmt.Errorf("code not found in PATH")
	case allowlistedGUIEditorCodeInsiders:
		if runtime.GOOS == "darwin" {
			// Prefer launching the app directly so we don't depend on the `code-insiders` CLI being installed in PATH.
			if err := exec.Command("open", "-a", "Visual Studio Code - Insiders", targetFile).Start(); err == nil {
				return kind, nil
			}
		}
		if _, err := exec.LookPath("code-insiders"); err == nil {
			return kind, exec.Command("code-insiders", targetFile).Start()
		}
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("xdg-open"); err == nil {
				return allowlistedGUIEditorXdgOpen, exec.Command("xdg-open", targetFile).Start()
			}
		}
		return kind, fmt.Errorf("code-insiders not found in PATH")
	case allowlistedGUIEditorCursor:
		if runtime.GOOS == "darwin" {
			// Prefer launching the app directly so we don't depend on the `cursor` CLI being installed in PATH.
			if err := exec.Command("open", "-a", "Cursor", targetFile).Start(); err == nil {
				return kind, nil
			}
		}
		if _, err := exec.LookPath("cursor"); err == nil {
			return kind, exec.Command("cursor", targetFile).Start()
		}
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("xdg-open"); err == nil {
				return allowlistedGUIEditorXdgOpen, exec.Command("xdg-open", targetFile).Start()
			}
		}
		return kind, fmt.Errorf("cursor not found in PATH")
	case allowlistedGUIEditorGedit:
		return kind, exec.Command("gedit", targetFile).Start()
	case allowlistedGUIEditorKate:
		return kind, exec.Command("kate", targetFile).Start()
	case allowlistedGUIEditorXed:
		return kind, exec.Command("xed", targetFile).Start()
	case allowlistedGUIEditorNotepad:
		return kind, exec.Command("notepad", targetFile).Start()
	default:
		return kind, fmt.Errorf("unsupported editor")
	}
}

// openInEditor opens the beads file in the user's preferred editor
// Uses m.beadsPath which respects issues.jsonl (canonical per beads upstream)
func (m *Model) openInEditor() {
	// Use the configured beadsPath instead of hardcoded path
	beadsFile := m.beadsPath
	if beadsFile == "" {
		cwd, _ := os.Getwd()
		if found, err := loader.FindJSONLPath(filepath.Join(cwd, ".beads")); err == nil {
			beadsFile = found
		}
	}
	if beadsFile == "" {
		m.statusMsg = "❌ No .beads directory or beads.jsonl found"
		m.statusIsError = true
		return
	}
	if _, err := os.Stat(beadsFile); os.IsNotExist(err) {
		m.statusMsg = fmt.Sprintf("❌ Beads file not found: %s", beadsFile)
		m.statusIsError = true
		return
	}

	// Determine editor - prefer GUI editors that work in background
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}

	ignoredEditorBase := ""
	var requestedEditorKind allowlistedGUIEditorKind
	if editor != "" {
		editorArgs, err := parseCommandLine(editor)
		if err != nil {
			m.statusMsg = fmt.Sprintf("❌ Invalid $EDITOR/$VISUAL: %v", err)
			m.statusIsError = true
			return
		}

		editorBase, kind := classifyEditorCommand(editorArgs)
		switch kind {
		case editorCommandTerminal:
			m.statusMsg = fmt.Sprintf("⚠️ %s is a terminal editor - set $EDITOR to a GUI editor or quit first", editorBase)
			m.statusIsError = true
			return
		case editorCommandForbidden:
			m.statusMsg = fmt.Sprintf("❌ Refusing to run %s as editor (shell/interpreter). Set $EDITOR to a GUI editor", editorBase)
			m.statusIsError = true
			return
		case editorCommandEmpty:
			m.statusMsg = "❌ Invalid $EDITOR/$VISUAL: empty command"
			m.statusIsError = true
			return
		default:
			requestedEditorKind = allowlistedGUIEditorKindForBase(editorBase)
			if requestedEditorKind == allowlistedGUIEditorUnknown {
				ignoredEditorBase = editorBase
				editor = ""
			}
		}
	}

	// If no editor set, try platform-specific GUI options
	if editor == "" && requestedEditorKind == allowlistedGUIEditorUnknown {
		switch runtime.GOOS {
		case "darwin":
			requestedEditorKind = allowlistedGUIEditorOpenText
		case "windows":
			requestedEditorKind = allowlistedGUIEditorNotepad
		case "linux":
			// Try xdg-open first, then common GUI editors
			for _, tryEditor := range []string{"xdg-open", "code", "code-insiders", "cursor", "gedit", "kate", "xed"} {
				if _, err := exec.LookPath(tryEditor); err == nil {
					requestedEditorKind = allowlistedGUIEditorKindForBase(tryEditor)
					break
				}
			}
		}
	}

	if requestedEditorKind == allowlistedGUIEditorUnknown {
		m.statusMsg = "❌ No GUI editor found. Set $EDITOR to a GUI editor"
		m.statusIsError = true
		return
	}

	actualKind, err := startAllowlistedGUIEditor(requestedEditorKind, beadsFile)
	if err != nil {
		m.statusMsg = fmt.Sprintf("❌ Failed to open editor: %v", err)
		m.statusIsError = true
		return
	}
	requestedEditorKind = actualKind

	if ignoredEditorBase != "" {
		m.statusMsg = fmt.Sprintf("📝 Opened in %s (ignored $EDITOR=%s)", allowlistedGUIEditorDisplayName(requestedEditorKind), ignoredEditorBase)
	} else {
		m.statusMsg = fmt.Sprintf("📝 Opened in %s", allowlistedGUIEditorDisplayName(requestedEditorKind))
	}
	m.statusIsError = false
}

// Stop cleans up resources (file watcher, instance lock, background worker, etc.)
// Should be called when the program exits
func (m *Model) Stop() {
	if m.backgroundWorker != nil {
		m.backgroundWorker.Stop()
	}
	if m.watcher != nil {
		m.watcher.Stop()
	}
	if m.instanceLock != nil {
		m.instanceLock.Release()
	}
	if len(m.pooledIssues) > 0 {
		loader.ReturnIssuePtrsToPool(m.pooledIssues)
		m.pooledIssues = nil
	}
}

// clearAttentionOverlay hides the attention overlay and clears its rendered text.
func (m *Model) clearAttentionOverlay() {
	if m.showAttentionView {
		m.showAttentionView = false
		m.insightsPanel.extraText = ""
	}
}

// ════════════════════════════════════════════════════════════════════════════
// ALERTS PANEL (bv-168)
// ════════════════════════════════════════════════════════════════════════════

// computeAlerts calculates drift alerts for the current issues using the
// already-computed graph stats/analyzer to avoid redundant work.
func computeAlerts(issues []model.Issue, stats *analysis.GraphStats, analyzer *analysis.Analyzer) ([]drift.Alert, int, int, int) {
	if len(issues) == 0 || stats == nil || analyzer == nil {
		return nil, 0, 0, 0
	}

	projectDir, _ := os.Getwd()
	driftConfig, err := drift.LoadConfig(projectDir)
	if err != nil {
		driftConfig = drift.DefaultConfig()
	}

	openCount, closedCount, blockedCount := 0, 0, 0
	for _, issue := range issues {
		switch {
		case isClosedLikeStatus(issue.Status):
			closedCount++
		case issue.Status == model.StatusBlocked:
			blockedCount++
		default:
			openCount++
		}
	}

	curStats := baseline.GraphStats{
		NodeCount:       stats.NodeCount,
		EdgeCount:       stats.EdgeCount,
		Density:         stats.Density,
		OpenCount:       openCount,
		ClosedCount:     closedCount,
		BlockedCount:    blockedCount,
		CycleCount:      len(stats.Cycles()),
		ActionableCount: len(analyzer.GetActionableIssues()),
	}

	bl := &baseline.Baseline{Stats: curStats}
	cur := &baseline.Baseline{Stats: curStats, Cycles: stats.Cycles()}

	calc := drift.NewCalculator(bl, cur, driftConfig)
	calc.SetIssues(issues)
	result := calc.Calculate()

	critical, warning, info := 0, 0, 0
	for _, a := range result.Alerts {
		switch a.Severity {
		case drift.SeverityCritical:
			critical++
		case drift.SeverityWarning:
			warning++
		case drift.SeverityInfo:
			info++
		}
	}

	return result.Alerts, critical, warning, info
}

// alertKey generates a unique key for an alert (for dismissal tracking)
func alertKey(a drift.Alert) string {
	return fmt.Sprintf("%s:%s:%s", a.Type, a.Severity, a.IssueID)
}

// renderAlertsPanel renders the alerts overlay panel
func (m Model) renderAlertsPanel() string {
	t := m.theme

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Width(min(80, m.width-4)).
		MaxHeight(m.height - 4)

	titleStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		MarginBottom(1)

	// Filter out dismissed alerts
	var visibleAlerts []drift.Alert
	for _, a := range m.alerts {
		if !m.dismissedAlerts[alertKey(a)] {
			visibleAlerts = append(visibleAlerts, a)
		}
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("🔔 Alerts Panel"))
	sb.WriteString("\n\n")

	if len(visibleAlerts) == 0 {
		sb.WriteString(t.Renderer.NewStyle().Foreground(ColorSuccess).Render("✓ No active alerts"))
		sb.WriteString("\n\n")
	} else {
		// Summary line
		summaryStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
		summary := fmt.Sprintf("%d total", len(visibleAlerts))
		if m.alertsCritical > 0 {
			summary += fmt.Sprintf(" • %d critical", m.alertsCritical)
		}
		if m.alertsWarning > 0 {
			summary += fmt.Sprintf(" • %d warning", m.alertsWarning)
		}
		if m.alertsInfo > 0 {
			summary += fmt.Sprintf(" • %d info", m.alertsInfo)
		}
		sb.WriteString(summaryStyle.Render(summary))
		sb.WriteString("\n\n")

		// Render each alert
		for i, a := range visibleAlerts {
			selected := i == m.alertsCursor

			// Severity indicator
			var severityStyle lipgloss.Style
			var severityIcon string
			switch a.Severity {
			case drift.SeverityCritical:
				severityStyle = t.Renderer.NewStyle().Foreground(t.Blocked).Bold(true)
				severityIcon = "⚠"
			case drift.SeverityWarning:
				severityStyle = t.Renderer.NewStyle().Foreground(t.Feature)
				severityIcon = "⚡"
			default:
				severityStyle = t.Renderer.NewStyle().Foreground(t.Secondary)
				severityIcon = "ℹ"
			}

			// Cursor indicator
			cursor := "  "
			if selected {
				cursor = "▸ "
			}

			// Alert line
			line := fmt.Sprintf("%s%s %s", cursor, severityIcon, a.Message)
			if selected {
				line = t.Renderer.NewStyle().Bold(true).Render(line)
			}
			sb.WriteString(severityStyle.Render(line))
			sb.WriteString("\n")

			// Show issue ID if available and selected
			if selected && a.IssueID != "" {
				issueHint := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true).Render(
					fmt.Sprintf("     Issue: %s (press Enter to jump)", a.IssueID))
				sb.WriteString(issueHint)
				sb.WriteString("\n")
			}

			// Show unblocks info for blocking cascade alerts
			if selected && a.UnblocksCount > 0 {
				unblockHint := t.Renderer.NewStyle().Foreground(t.Open).Render(
					fmt.Sprintf("     Unblocks %d items (priority sum: %d)", a.UnblocksCount, a.DownstreamPrioritySum))
				sb.WriteString(unblockHint)
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString("\n")
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Muted).Italic(true).Render(
		"j/k: navigate • Enter: jump to issue • d: dismiss • Esc: close"))

	content := boxStyle.Render(sb.String())

	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		content,
	)
}

// RenderDebugView renders a specific view for debugging purposes.
// This is used by --debug-render to capture TUI output without running interactively.
func (m *Model) RenderDebugView(viewName string, width, height int) string {
	m.width = width
	m.height = height
	m.ready = true

	switch viewName {
	case "insights":
		m.insightsPanel.SetSize(width, height-1)
		return m.insightsPanel.View()
	case "board":
		return m.board.View(width, height-1)
	case "history":
		m.historyView.SetSize(width, height-1)
		return m.historyView.View()
	default:
		return "Unknown view: " + viewName
	}
}

func formatReloadDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
