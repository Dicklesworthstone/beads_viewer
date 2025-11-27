package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
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
	focusInsights
	focusActionable
	focusRecipePicker
	focusHelp
	focusQuitConfirm
	focusTimeTravelInput
)

// UpdateMsg is sent when a new version is available
type UpdateMsg struct {
	TagName string
	URL     string
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

// Model is the main Bubble Tea model for the beads viewer
type Model struct {
	// Data
	issues   []model.Issue
	issueMap map[string]*model.Issue
	analysis analysis.GraphStats

	// UI Components
	list          list.Model
	viewport      viewport.Model
	renderer      *glamour.TermRenderer
	board         BoardModel
	graphView     GraphModel
	insightsPanel InsightsModel
	theme         Theme

	// Update State
	updateAvailable bool
	updateTag       string
	updateURL       string

	// Focus and View State
	focused          focus
	isSplitView      bool
	isBoardView      bool
	isGraphView      bool
	isActionableView bool
	showDetails      bool
	showHelp         bool
	showQuitConfirm  bool
	ready            bool
	width            int
	height           int

	// Actionable view
	actionableView ActionableModel

	// Filter state
	currentFilter string
	searchTerm    string

	// Stats (cached)
	countOpen    int
	countReady   int
	countBlocked int
	countClosed  int

	// Priority hints
	showPriorityHints bool
	priorityHints     map[string]*analysis.PriorityRecommendation // issueID -> recommendation

	// Recipe picker
	showRecipePicker bool
	recipePicker     RecipePickerModel
	activeRecipe     *recipe.Recipe
	recipeLoader     *recipe.Loader

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
}

// NewModel creates a new Model from the given issues
func NewModel(issues []model.Issue, activeRecipe *recipe.Recipe) Model {
	// Graph Analysis (single pass for performance)
	analyzer := analysis.NewAnalyzer(issues)
	graphStats := analyzer.Analyze()

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
				less = graphStats.CriticalPathScore[issues[i].ID] < graphStats.CriticalPathScore[issues[j].ID]
			case "pagerank":
				less = graphStats.PageRank[issues[i].ID] < graphStats.PageRank[issues[j].ID]
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
			iClosed := issues[i].Status == model.StatusClosed
			jClosed := issues[j].Status == model.StatusClosed
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

	// Build list items with pre-computed graph scores
	items := make([]list.Item, len(issues))
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]

		items[i] = IssueItem{
			Issue:      issues[i],
			GraphScore: graphStats.PageRank[issues[i].ID],
			Impact:     graphStats.CriticalPathScore[issues[i].ID],
		}
	}

	// Compute stats
	cOpen, cReady, cBlocked, cClosed := 0, 0, 0, 0
	for i := range issues {
		issue := &issues[i]
		if issue.Status == model.StatusClosed {
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
			if dep.Type != model.DepBlocks {
				continue
			}
			if blocker, exists := issueMap[dep.DependsOnID]; exists && blocker.Status != model.StatusClosed {
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

	// List setup
	delegate := IssueDelegate{Theme: theme}
	l := list.New(items, delegate, 0, 0)
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

	// Glamour markdown renderer
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)

	// Initialize sub-components
	board := NewBoardModel(issues, theme)
	ins := graphStats.GenerateInsights(len(issues)) // allow UI to show as many as fit
	insightsPanel := NewInsightsModel(ins, issueMap, theme)
	graphView := NewGraphModel(issues, &ins, theme)

	// Generate priority recommendations for hints
	recommendations := analyzer.GenerateRecommendations()
	priorityHints := make(map[string]*analysis.PriorityRecommendation, len(recommendations))
	for i := range recommendations {
		priorityHints[recommendations[i].IssueID] = &recommendations[i]
	}

	// Initialize recipe loader
	recipeLoader := recipe.NewLoader()
	_ = recipeLoader.Load() // Load recipes (errors are non-fatal, will just show empty)
	recipePicker := NewRecipePickerModel(recipeLoader.List(), theme)

	// Initialize time-travel input
	ti := textinput.New()
	ti.Placeholder = "HEAD~5, main, v1.0.0, 2024-01-01..."
	ti.CharLimit = 100
	ti.Width = 40
	ti.Prompt = "⏱️  Revision: "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	ti.TextStyle = lipgloss.NewStyle().Foreground(theme.Base.GetForeground())

	return Model{
		issues:            issues,
		issueMap:          issueMap,
		analysis:          graphStats,
		list:              l,
		renderer:          renderer,
		board:             board,
		graphView:         graphView,
		insightsPanel:     insightsPanel,
		theme:             theme,
		currentFilter:     "all",
		focused:           focusList,
		countOpen:         cOpen,
		countReady:        cReady,
		countBlocked:      cBlocked,
		countClosed:       cClosed,
		priorityHints:     priorityHints,
		showPriorityHints: false, // Off by default, toggle with 'p'
		recipeLoader:      recipeLoader,
		recipePicker:      recipePicker,
		activeRecipe:      activeRecipe,
		timeTravelInput:   ti,
	}
}

func (m Model) Init() tea.Cmd {
	return CheckUpdateCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case UpdateMsg:
		m.updateAvailable = true
		m.updateTag = msg.TagName
		m.updateURL = msg.URL

	case tea.KeyMsg:
		// Clear status message on any keypress
		m.statusMsg = ""
		m.statusIsError = false

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
				m.focused = focusHelp
			} else {
				m.focused = focusList
			}
			return m, nil
		}

		// If help is showing, any key (except ?/F1) dismisses it
		if m.focused == focusHelp {
			m.showHelp = false
			m.focused = focusList
			return m, nil
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
					return m, nil
				}
				if m.focused == focusInsights {
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
					return m, nil
				}
				if m.focused == focusInsights {
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
				// At main list - show quit confirmation
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

			case "b":
				m.isBoardView = !m.isBoardView
				m.isGraphView = false
				m.isActionableView = false
				if m.isBoardView {
					m.focused = focusBoard
				} else {
					m.focused = focusList
				}

			case "g":
				// Toggle graph view
				m.isGraphView = !m.isGraphView
				m.isBoardView = false
				m.isActionableView = false
				if m.isGraphView {
					m.focused = focusGraph
				} else {
					m.focused = focusList
				}
				return m, nil

			case "a":
				// Toggle actionable view
				m.isActionableView = !m.isActionableView
				m.isGraphView = false
				m.isBoardView = false
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

			case "i":
				if m.focused == focusInsights {
					m.focused = focusList
				} else {
					m.focused = focusInsights
					m.isGraphView = false
					m.isBoardView = false
					m.isActionableView = false
				}

			case "p":
				// Toggle priority hints
				m.showPriorityHints = !m.showPriorityHints
				// Update delegate with new state
				m.list.SetDelegate(IssueDelegate{
					Theme:             m.theme,
					ShowPriorityHints: m.showPriorityHints,
					PriorityHints:     m.priorityHints,
				})
				return m, nil

			case "R":
				// Toggle recipe picker overlay
				m.showRecipePicker = !m.showRecipePicker
				if m.showRecipePicker {
					m.recipePicker.SetSize(m.width, m.height-1)
					m.focused = focusRecipePicker
				} else {
					m.focused = focusList
				}
				return m, nil

			case "E":
				// Export to Markdown file
				m.exportToMarkdown()
				return m, nil
			}

			// Focus-specific key handling
			switch m.focused {
			case focusRecipePicker:
				m = m.handleRecipePickerKeys(msg)

			case focusInsights:
				m = m.handleInsightsKeys(msg)

			case focusBoard:
				m = m.handleBoardKeys(msg)

			case focusGraph:
				m = m.handleGraphKeys(msg)

			case focusActionable:
				m = m.handleActionableKeys(msg)

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
				m.viewport.LineUp(3)
			case focusInsights:
				m.insightsPanel.MoveUp()
			case focusBoard:
				m.board.MoveUp()
			case focusGraph:
				m.graphView.PageUp()
			case focusActionable:
				m.actionableView.MoveUp()
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
				m.viewport.LineDown(3)
			case focusInsights:
				m.insightsPanel.MoveDown()
			case focusBoard:
				m.board.MoveDown()
			case focusGraph:
				m.graphView.PageDown()
			case focusActionable:
				m.actionableView.MoveDown()
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

			listInnerWidth := int(float64(availWidth) * 0.4)
			detailInnerWidth := availWidth - listInnerWidth

			// listHeight fits header (1) + page line (1) inside a panel with Border (2)
			listHeight := bodyHeight - 4
			if listHeight < 3 {
				listHeight = 3
			}

			m.list.SetSize(listInnerWidth, listHeight)
			m.viewport = viewport.New(detailInnerWidth, bodyHeight-2) // Account for border

			if r, err := glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
				glamour.WithWordWrap(detailInnerWidth),
			); err == nil {
				m.renderer = r
			}
		} else {
			listHeight := bodyHeight - 2
			if listHeight < 3 {
				listHeight = 3
			}
			m.list.SetSize(msg.Width, listHeight)
			m.viewport = viewport.New(msg.Width, bodyHeight-1)

			// Update renderer for full width
			if r, err := glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
				glamour.WithWordWrap(msg.Width),
			); err == nil {
				m.renderer = r
			}
		}

		m.list.SetDelegate(IssueDelegate{
			Theme:             m.theme,
			ShowPriorityHints: m.showPriorityHints,
			PriorityHints:     m.priorityHints,
		})

		m.insightsPanel.SetSize(m.width, bodyHeight)
		m.updateViewportContent()
	}

	// Update list for filtering input, but NOT for WindowSizeMsg
	// (we handle sizing ourselves to account for header/footer)
	if _, isWindowSize := msg.(tea.WindowSizeMsg); !isWindowSize {
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Update viewport if list selection changed in split view
	if m.isSplitView && m.focused == focusList {
		m.updateViewportContent()
	}

	return m, tea.Batch(cmds...)
}

// handleBoardKeys handles keyboard input when the board is focused
func (m Model) handleBoardKeys(msg tea.KeyMsg) Model {
	switch msg.String() {
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
	case "enter":
		if selected := m.board.SelectedIssue(); selected != nil {
			// Find and select in list
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
			}
			m.updateViewportContent()
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
			}
			m.updateViewportContent()
		}
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
			m.activeRecipe = selected
			m.applyRecipe(selected)
		}
		m.showRecipePicker = false
		m.focused = focusList
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

func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var body string

	// Quit confirmation overlay takes highest priority
	if m.showQuitConfirm {
		body = m.renderQuitConfirm()
	} else if m.showTimeTravelPrompt {
		body = m.renderTimeTravelPrompt()
	} else if m.showRecipePicker {
		body = m.recipePicker.View()
	} else if m.showHelp {
		body = m.renderHelpOverlay()
	} else if m.focused == focusInsights {
		body = m.insightsPanel.View()
	} else if m.isGraphView {
		body = m.graphView.View(m.width, m.height-1)
	} else if m.isBoardView {
		body = m.board.View(m.width, m.height-1)
	} else if m.isActionableView {
		m.actionableView.SetSize(m.width, m.height-2)
		body = m.actionableView.Render()
	} else if m.isSplitView {
		body = m.renderSplitView()
	} else {
		// Mobile view
		if m.showDetails {
			body = m.viewport.View()
		} else {
			body = m.renderListWithHeader()
		}
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

	header := headerStyle.Render("  TYPE PRI STATUS      ID                                   TITLE")

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

	pageInfo := fmt.Sprintf("Page %d/%d (%d-%d of %d)", currentPage, totalPages, startItem, endItem, totalItems)
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

func (m Model) renderHelpOverlay() string {
	t := m.theme

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		MarginBottom(1)

	sectionStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Bold(true).
		MarginTop(1)

	keyStyle := t.Renderer.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#BD93F9"}).
		Bold(true).
		Width(12)

	descStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	var sb strings.Builder

	sb.WriteString(titleStyle.Render("⌨️  Keyboard Shortcuts"))
	sb.WriteString("\n\n")

	// Navigation
	sb.WriteString(sectionStyle.Render("Navigation"))
	sb.WriteString("\n")
	shortcuts := []struct{ key, desc string }{
		{"j / ↓", "Move down"},
		{"k / ↑", "Move up"},
		{"home", "Go to first item"},
		{"G / end", "Go to last item"},
		{"Ctrl+d", "Page down"},
		{"Ctrl+u", "Page up"},
		{"Tab", "Switch focus (split view)"},
		{"Enter", "View details"},
		{"Esc", "Back / close"},
	}
	for _, s := range shortcuts {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	// Views
	sb.WriteString("\n")
	sb.WriteString(sectionStyle.Render("Views"))
	sb.WriteString("\n")
	views := []struct{ key, desc string }{
		{"a", "Toggle Actionable view"},
		{"b", "Toggle Kanban board"},
		{"g", "Toggle Graph view"},
		{"i", "Toggle Insights dashboard"},
		{"R", "Open Recipe picker"},
		{"?", "Toggle this help"},
	}
	for _, s := range views {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	// Graph view keys
	sb.WriteString("\n")
	sb.WriteString(sectionStyle.Render("Graph View"))
	sb.WriteString("\n")
	graphKeys := []struct{ key, desc string }{
		{"h/j/k/l", "Navigate nodes"},
		{"H/L", "Scroll canvas left/right"},
		{"PgUp/PgDn", "Scroll canvas up/down"},
		{"Enter", "Jump to selected issue"},
	}
	for _, s := range graphKeys {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	// Insights (when in insights view)
	sb.WriteString("\n")
	sb.WriteString(sectionStyle.Render("Insights Panel"))
	sb.WriteString("\n")
	insightsKeys := []struct{ key, desc string }{
		{"h/l/Tab", "Switch metric panels"},
		{"j/k", "Navigate items"},
		{"e", "Toggle explanations"},
		{"x", "Toggle calculation details"},
		{"Enter", "Jump to issue"},
	}
	for _, s := range insightsKeys {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	// Filters
	sb.WriteString("\n")
	sb.WriteString(sectionStyle.Render("Filters"))
	sb.WriteString("\n")
	filters := []struct{ key, desc string }{
		{"o", "Show Open issues"},
		{"c", "Show Closed issues"},
		{"r", "Show Ready (unblocked)"},
		{"a", "Show All issues"},
		{"/", "Fuzzy search"},
	}
	for _, s := range filters {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	// General
	sb.WriteString("\n")
	sb.WriteString(sectionStyle.Render("General"))
	sb.WriteString("\n")
	general := []struct{ key, desc string }{
		{"t", "Time-travel (custom revision)"},
		{"T", "Time-travel (HEAD~5)"},
		{"E", "Export to Markdown"},
		{"C", "Copy issue to clipboard"},
		{"O", "Open in editor"},
		{"q", "Back / Quit"},
		{"Ctrl+c", "Force quit"},
	}
	for _, s := range general {
		sb.WriteString(keyStyle.Render(s.key) + descStyle.Render(s.desc) + "\n")
	}

	sb.WriteString("\n")
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true).Render("Press any key to close"))

	// Center the help content
	helpContent := sb.String()
	helpBox := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 3).
		Render(helpContent)

	// Center in viewport
	return lipgloss.Place(
		m.width,
		m.height-1,
		lipgloss.Center,
		lipgloss.Center,
		helpBox,
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
		msgSection := msgStyle.Render("✓ " + m.statusMsg)
		remaining := m.width - lipgloss.Width(msgSection)
		if remaining < 0 {
			remaining = 0
		}
		filler := lipgloss.NewStyle().Background(ColorBgDark).Width(remaining).Render("")
		return lipgloss.JoinHorizontal(lipgloss.Bottom, msgSection, filler)
	}

	// ─────────────────────────────────────────────────────────────────────────
	// FILTER BADGE - Current view/filter state
	// ─────────────────────────────────────────────────────────────────────────
	var filterTxt string
	var filterIcon string
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

	filterBadge := lipgloss.NewStyle().
		Background(ColorPrimary).
		Foreground(ColorText).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", filterIcon, filterTxt))

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
	// KEYBOARD HINTS - Context-aware navigation help
	// ─────────────────────────────────────────────────────────────────────────
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Background(ColorBgSubtle).
		Padding(0, 0)
	sepStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	sep := sepStyle.Render(" │ ")

	var keyHints []string
	if m.showHelp {
		keyHints = append(keyHints, "Press any key to close")
	} else if m.showRecipePicker {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("⏎")+" apply", keyStyle.Render("esc")+" cancel")
	} else if m.focused == focusInsights {
		keyHints = append(keyHints, keyStyle.Render("h/l")+" panels", keyStyle.Render("e")+" explain", keyStyle.Render("⏎")+" jump", keyStyle.Render("?")+" help")
	} else if m.isGraphView {
		keyHints = append(keyHints, keyStyle.Render("hjkl")+" nav", keyStyle.Render("H/L")+" scroll", keyStyle.Render("⏎")+" view", keyStyle.Render("g")+" list")
	} else if m.isBoardView {
		keyHints = append(keyHints, keyStyle.Render("hjkl")+" nav", keyStyle.Render("G")+" bottom", keyStyle.Render("⏎")+" view", keyStyle.Render("b")+" list")
	} else if m.isActionableView {
		keyHints = append(keyHints, keyStyle.Render("j/k")+" nav", keyStyle.Render("⏎")+" view", keyStyle.Render("a")+" list", keyStyle.Render("?")+" help")
	} else if m.list.FilterState() == list.Filtering {
		keyHints = append(keyHints, keyStyle.Render("esc")+" cancel", keyStyle.Render("⏎")+" select")
	} else if m.showTimeTravelPrompt {
		keyHints = append(keyHints, keyStyle.Render("⏎")+" compare", keyStyle.Render("esc")+" cancel")
	} else {
		if m.timeTravelMode {
			keyHints = append(keyHints, keyStyle.Render("t")+" exit diff", keyStyle.Render("C")+" copy", keyStyle.Render("abgi")+" views", keyStyle.Render("?")+" help")
		} else if m.isSplitView {
			keyHints = append(keyHints, keyStyle.Render("tab")+" focus", keyStyle.Render("C")+" copy", keyStyle.Render("E")+" export", keyStyle.Render("?")+" help")
		} else if m.showDetails {
			keyHints = append(keyHints, keyStyle.Render("esc")+" back", keyStyle.Render("C")+" copy", keyStyle.Render("O")+" edit", keyStyle.Render("?")+" help")
		} else {
			keyHints = append(keyHints, keyStyle.Render("⏎")+" details", keyStyle.Render("t")+" diff", keyStyle.Render("ECO")+" actions", keyStyle.Render("?")+" help")
		}
	}

	keysSection := lipgloss.NewStyle().
		Foreground(ColorSubtext).
		Padding(0, 1).
		Render(strings.Join(keyHints, sep))

	// ─────────────────────────────────────────────────────────────────────────
	// COUNT BADGE - Total issues displayed
	// ─────────────────────────────────────────────────────────────────────────
	countBadge := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Padding(0, 1).
		Render(fmt.Sprintf("%d issues", len(m.list.Items())))

	// ─────────────────────────────────────────────────────────────────────────
	// ASSEMBLE FOOTER with proper spacing
	// ─────────────────────────────────────────────────────────────────────────
	leftWidth := lipgloss.Width(filterBadge) + lipgloss.Width(statsSection)
	if updateSection != "" {
		leftWidth += lipgloss.Width(updateSection) + 1
	}
	rightWidth := lipgloss.Width(countBadge) + lipgloss.Width(keysSection)

	remaining := m.width - leftWidth - rightWidth - 1
	if remaining < 0 {
		remaining = 0
	}
	filler := lipgloss.NewStyle().Background(ColorBgDark).Width(remaining).Render("")

	// Build the footer
	var parts []string
	parts = append(parts, filterBadge)
	if updateSection != "" {
		parts = append(parts, updateSection)
	}
	parts = append(parts, statsSection, filler, countBadge, keysSection)

	return lipgloss.JoinHorizontal(lipgloss.Bottom, parts...)
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

func (m *Model) applyFilter() {
	var filteredItems []list.Item
	var filteredIssues []model.Issue

	for _, issue := range m.issues {
		include := false
		switch m.currentFilter {
		case "all":
			include = true
		case "open":
			include = issue.Status != model.StatusClosed
		case "closed":
			include = issue.Status == model.StatusClosed
		case "ready":
			// Ready = Open/InProgress AND No Open Blockers
			if issue.Status != model.StatusClosed && issue.Status != model.StatusBlocked {
				isBlocked := false
				for _, dep := range issue.Dependencies {
					if dep.Type == model.DepBlocks {
						if blocker, exists := m.issueMap[dep.DependsOnID]; exists && blocker.Status != model.StatusClosed {
							isBlocked = true
							break
						}
					}
				}
				include = !isBlocked
			}
		}

		if include {
			// Use pre-computed graph scores (avoid redundant calculation)
			filteredItems = append(filteredItems, IssueItem{
				Issue:      issue,
				GraphScore: m.analysis.PageRank[issue.ID],
				Impact:     m.analysis.CriticalPathScore[issue.ID],
				DiffStatus: m.getDiffStatus(issue.ID),
			})
			filteredIssues = append(filteredIssues, issue)
		}
	}

	m.list.SetItems(filteredItems)
	m.board.SetIssues(filteredIssues)
	m.graphView.SetIssues(filteredIssues, nil) // nil insights since we use pre-computed

	// Keep selection in bounds
	if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
		m.list.Select(0)
	}
	m.updateViewportContent()
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

		// Apply status filter
		if len(r.Filters.Status) > 0 {
			statusMatch := false
			for _, s := range r.Filters.Status {
				if string(issue.Status) == s {
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
				if dep.Type == model.DepBlocks {
					if blocker, exists := m.issueMap[dep.DependsOnID]; exists && blocker.Status != model.StatusClosed {
						isBlocked = true
						break
					}
				}
			}
			include = !isBlocked
		}

		if include {
			filteredItems = append(filteredItems, IssueItem{
				Issue:      issue,
				GraphScore: m.analysis.PageRank[issue.ID],
				Impact:     m.analysis.CriticalPathScore[issue.ID],
				DiffStatus: m.getDiffStatus(issue.ID),
			})
			filteredIssues = append(filteredIssues, issue)
		}
	}

	// Apply sort
	descending := r.Sort.Direction == "desc"
	if r.Sort.Field != "" {
		sort.Slice(filteredItems, func(i, j int) bool {
			iItem := filteredItems[i].(IssueItem)
			jItem := filteredItems[j].(IssueItem)
			less := false

			switch r.Sort.Field {
			case "priority":
				less = iItem.Issue.Priority < jItem.Issue.Priority
			case "created", "created_at":
				less = iItem.Issue.CreatedAt.Before(jItem.Issue.CreatedAt)
			case "updated", "updated_at":
				less = iItem.Issue.UpdatedAt.Before(jItem.Issue.UpdatedAt)
			case "impact":
				less = iItem.Impact < jItem.Impact
			case "pagerank":
				less = iItem.GraphScore < jItem.GraphScore
			default:
				less = iItem.Issue.Priority < jItem.Issue.Priority
			}

			if descending {
				return !less
			}
			return less
		})

		// Re-sort issues list too
		sort.Slice(filteredIssues, func(i, j int) bool {
			less := false
			switch r.Sort.Field {
			case "priority":
				less = filteredIssues[i].Priority < filteredIssues[j].Priority
			case "created", "created_at":
				less = filteredIssues[i].CreatedAt.Before(filteredIssues[j].CreatedAt)
			case "updated", "updated_at":
				less = filteredIssues[i].UpdatedAt.Before(filteredIssues[j].UpdatedAt)
			case "impact":
				// Use analysis map for sort
				less = m.analysis.CriticalPathScore[filteredIssues[i].ID] < m.analysis.CriticalPathScore[filteredIssues[j].ID]
			case "pagerank":
				// Use analysis map for sort
				less = m.analysis.PageRank[filteredIssues[i].ID] < m.analysis.PageRank[filteredIssues[j].ID]
			default:
				less = filteredIssues[i].Priority < filteredIssues[j].Priority
			}
			if descending {
				return !less
			}
			return less
		})
	}

	m.list.SetItems(filteredItems)
	m.board.SetIssues(filteredIssues)
	m.graphView.SetIssues(filteredIssues, nil)

	// Update filter indicator
	m.currentFilter = "recipe:" + r.Name

	// Keep selection in bounds
	if len(filteredItems) > 0 && m.list.Index() >= len(filteredItems) {
		m.list.Select(0)
	}
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

	// Graph Analysis
	pr := m.analysis.PageRank[item.ID]
	bt := m.analysis.Betweenness[item.ID]
	imp := m.analysis.CriticalPathScore[item.ID]
	ev := m.analysis.Eigenvector[item.ID]
	hub := m.analysis.Hubs[item.ID]
	auth := m.analysis.Authorities[item.ID]

	sb.WriteString("### Graph Analysis\n")
	sb.WriteString(fmt.Sprintf("- **Impact Depth**: %.0f (downstream chain length)\n", imp))
	sb.WriteString(fmt.Sprintf("- **Centrality**: PR %.4f • BW %.4f • EV %.4f\n", pr, bt, ev))
	sb.WriteString(fmt.Sprintf("- **Flow Role**: Hub %.4f • Authority %.4f\n\n", hub, auth))

	// Description
	if item.Description != "" {
		sb.WriteString("### Description\n")
		sb.WriteString(item.Description + "\n\n")
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

	rendered, err := m.renderer.Render(sb.String())
	if err != nil {
		m.viewport.SetContent(fmt.Sprintf("Error rendering markdown: %v", err))
	} else {
		m.viewport.SetContent(rendered)
	}
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
		return "🏔️"
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

// openInEditor opens the beads.jsonl file in the user's preferred editor
func (m *Model) openInEditor() {
	cwd, err := os.Getwd()
	if err != nil {
		m.statusMsg = "❌ Cannot get working directory"
		m.statusIsError = true
		return
	}

	beadsFile := filepath.Join(cwd, ".beads", "beads.jsonl")
	if _, err := os.Stat(beadsFile); os.IsNotExist(err) {
		m.statusMsg = "❌ No .beads/beads.jsonl file found"
		m.statusIsError = true
		return
	}

	// Determine editor - prefer GUI editors that work in background
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}

	// Check if it's a terminal editor (won't work well with TUI)
	terminalEditors := map[string]bool{
		"vim": true, "vi": true, "nvim": true, "nano": true,
		"emacs": true, "pico": true, "joe": true, "ne": true,
	}
	editorBase := filepath.Base(editor)
	if terminalEditors[editorBase] {
		m.statusMsg = fmt.Sprintf("⚠️ %s is a terminal editor - set $EDITOR to a GUI editor or quit first", editorBase)
		m.statusIsError = true
		return
	}

	// If no editor set, try platform-specific GUI options
	if editor == "" {
		switch runtime.GOOS {
		case "darwin":
			// Use 'open' to launch default app for .jsonl files
			cmd := exec.Command("open", "-t", beadsFile)
			if err := cmd.Start(); err == nil {
				m.statusMsg = "📝 Opened in default text editor"
				m.statusIsError = false
				return
			}
		case "windows":
			editor = "notepad"
		case "linux":
			// Try xdg-open first, then common GUI editors
			for _, tryEditor := range []string{"xdg-open", "code", "gedit", "kate", "xed"} {
				if _, err := exec.LookPath(tryEditor); err == nil {
					editor = tryEditor
					break
				}
			}
		}
	}

	if editor == "" {
		m.statusMsg = "❌ No GUI editor found. Set $EDITOR to a GUI editor"
		m.statusIsError = true
		return
	}

	// Launch GUI editor in background
	cmd := exec.Command(editor, beadsFile)
	err = cmd.Start()
	if err != nil {
		m.statusMsg = fmt.Sprintf("❌ Failed to open editor: %v", err)
		m.statusIsError = true
		return
	}

	m.statusMsg = fmt.Sprintf("📝 Opened in %s", filepath.Base(editor))
	m.statusIsError = false
}
