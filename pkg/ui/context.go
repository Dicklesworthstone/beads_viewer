package ui

import (
	"github.com/charmbracelet/bubbles/list"
)

// Context represents the current UI context for context-sensitive help
type Context string

const (
	// Overlays (highest priority)
	ContextLabelPicker        Context = "label-picker"
	ContextRecipePicker       Context = "recipe-picker"
	ContextHelp               Context = "help"
	ContextQuitConfirm        Context = "quit-confirm"
	ContextDeleteConfirm      Context = "delete-confirm"
	ContextLabelHealthDetail  Context = "label-health-detail"
	ContextLabelDrilldown     Context = "label-drilldown"
	ContextLabelGraphAnalysis Context = "label-graph-analysis"
	ContextTimeTravelInput    Context = "time-travel-input"
	ContextAlerts             Context = "alerts"
	ContextRepoPicker         Context = "repo-picker"
	ContextAgentPrompt        Context = "agent-prompt"
	ContextCassSession        Context = "cass-session"

	// Views
	ContextInsights       Context = "insights"
	ContextFlowMatrix     Context = "flow-matrix"
	ContextGraph          Context = "graph"
	ContextBoard          Context = "board"
	ContextActionable     Context = "actionable"
	ContextHistory        Context = "history"
	ContextSprint         Context = "sprint"
	ContextLabelDashboard Context = "label-dashboard"
	ContextAttention      Context = "attention"

	// Detail states
	ContextSplit      Context = "split"
	ContextDetail     Context = "detail"
	ContextTimeTravel Context = "time-travel"

	// Filter state
	ContextFilter Context = "filter"

	// Default
	ContextList Context = "list"
)

// CurrentContext returns the current UI context identifier.
// This is used for context-sensitive help (e.g., double-tap CapsLock).
// Priority order: overlays → views → detail states → filter → default
func (m Model) CurrentContext() Context {
	// === Overlays (most specific - check first) ===

	// Cass session modal (bv-qi94)
	if m.showCassModal {
		return ContextCassSession
	}

	// Agent prompt modal
	if m.showAgentPrompt {
		return ContextAgentPrompt
	}

	// Help overlay
	if m.showHelp {
		return ContextHelp
	}

	// Quit confirmation
	if m.showQuitConfirm {
		return ContextQuitConfirm
	}

	// Delete confirmation
	if m.showDeleteConfirm {
		return ContextDeleteConfirm
	}

	// Label picker overlay
	if m.showLabelPicker {
		return ContextLabelPicker
	}

	// Recipe picker overlay
	if m.showRecipePicker {
		return ContextRecipePicker
	}

	// Label health detail modal
	if m.showLabelHealthDetail {
		return ContextLabelHealthDetail
	}

	// Label drilldown overlay
	if m.showLabelDrilldown {
		return ContextLabelDrilldown
	}

	// Label graph analysis sub-view
	if m.showLabelGraphAnalysis {
		return ContextLabelGraphAnalysis
	}

	// Time-travel input prompt
	if m.showTimeTravelPrompt {
		return ContextTimeTravelInput
	}

	// Alerts panel
	if m.showAlertsPanel {
		return ContextAlerts
	}

	// Repo picker overlay (workspace mode)
	if m.showRepoPicker {
		return ContextRepoPicker
	}

	// === Views (based on focus or view flags) ===

	// Insights panel
	if m.focused == focusInsights {
		// Check if in attention sub-view
		if m.showAttentionView {
			return ContextAttention
		}
		return ContextInsights
	}

	// Flow matrix view
	if m.focused == focusFlowMatrix {
		return ContextFlowMatrix
	}

	// Label dashboard
	if m.focused == focusLabelDashboard {
		return ContextLabelDashboard
	}

	// Graph view
	if m.isGraphView {
		return ContextGraph
	}

	// Board view
	if m.isBoardView {
		return ContextBoard
	}

	// Actionable view
	if m.isActionableView {
		return ContextActionable
	}

	// History view
	if m.isHistoryView {
		return ContextHistory
	}

	// Sprint view
	if m.isSprintView {
		return ContextSprint
	}

	// === Detail states ===

	// Time-travel mode (comparing snapshots)
	if m.timeTravelMode {
		return ContextTimeTravel
	}

	// Split view (list + detail side by side)
	if m.isSplitView {
		return ContextSplit
	}

	// Detail view (single issue detail)
	if m.showDetails {
		return ContextDetail
	}

	// === Filter state ===

	// Active filtering/search
	if m.list.FilterState() != list.Unfiltered {
		return ContextFilter
	}

	// === Default ===
	return ContextList
}

// ContextDescription returns a human-readable description of the context.
// Useful for status messages or debugging.
func (c Context) Description() string {
	descriptions := map[Context]string{
		ContextLabelPicker:        "Label picker",
		ContextRecipePicker:       "Recipe picker",
		ContextHelp:               "Help overlay",
		ContextQuitConfirm:        "Quit confirmation",
		ContextDeleteConfirm:      "Delete confirmation",
		ContextLabelHealthDetail:  "Label health detail",
		ContextLabelDrilldown:     "Label drilldown",
		ContextLabelGraphAnalysis: "Label graph analysis",
		ContextTimeTravelInput:    "Time-travel input",
		ContextAlerts:             "Alerts panel",
		ContextRepoPicker:         "Repo picker",
		ContextAgentPrompt:        "Agent prompt",
		ContextCassSession:        "Cass session preview",
		ContextInsights:           "Insights panel",
		ContextFlowMatrix:         "Flow matrix",
		ContextGraph:              "Dependency graph",
		ContextBoard:              "Kanban board",
		ContextActionable:         "Actionable view",
		ContextHistory:            "History view",
		ContextSprint:             "Sprint view",
		ContextLabelDashboard:     "Label dashboard",
		ContextAttention:          "Attention view",
		ContextSplit:              "Split view",
		ContextDetail:             "Issue detail",
		ContextTimeTravel:         "Time-travel mode",
		ContextFilter:             "Filter/search mode",
		ContextList:               "Issue list",
	}
	if desc, ok := descriptions[c]; ok {
		return desc
	}
	return string(c)
}

// IsOverlay returns true if the context is an overlay (modal/popup)
func (c Context) IsOverlay() bool {
	switch c {
	case ContextLabelPicker, ContextRecipePicker, ContextHelp, ContextQuitConfirm, ContextDeleteConfirm,
		ContextLabelHealthDetail, ContextLabelDrilldown, ContextLabelGraphAnalysis,
		ContextTimeTravelInput, ContextAlerts, ContextRepoPicker, ContextAgentPrompt,
		ContextCassSession:
		return true
	}
	return false
}

// IsView returns true if the context is a full view (not overlay or default list)
func (c Context) IsView() bool {
	switch c {
	case ContextInsights, ContextFlowMatrix, ContextGraph, ContextBoard,
		ContextActionable, ContextHistory, ContextSprint, ContextLabelDashboard,
		ContextAttention, ContextSplit, ContextDetail, ContextTimeTravel:
		return true
	}
	return false
}

// TutorialPages returns the recommended tutorial page IDs for this context.
// Used to provide context-sensitive help.
func (c Context) TutorialPages() []int {
	// Map contexts to relevant tutorial page indices
	pageMap := map[Context][]int{
		ContextList:               {0, 1, 2}, // Intro, Navigation, List View
		ContextFilter:             {2, 3},    // List View, Filtering
		ContextDetail:             {4},       // Detail View
		ContextSplit:              {4, 2},    // Detail View, List View
		ContextBoard:              {5},       // Board View
		ContextGraph:              {6},       // Graph View
		ContextInsights:           {7},       // Insights
		ContextHistory:            {8},       // History View
		ContextActionable:         {9},       // Actionable View
		ContextTimeTravel:         {10},      // Time-Travel
		ContextLabelDashboard:     {11},      // Labels
		ContextFlowMatrix:         {11, 12},  // Labels, Advanced
		ContextHelp:               {13},      // Keyboard Reference
		ContextSprint:             {14},      // Sprints
		ContextAttention:          {7},       // Insights (attention is part of insights)
		ContextAlerts:             {15},      // Alerts
		ContextLabelPicker:        {11, 3},   // Labels, Filtering
		ContextRecipePicker:       {3, 12},   // Filtering, Advanced
		ContextRepoPicker:         {12},      // Advanced (workspace)
		ContextAgentPrompt:        {16},      // AI Agent Integration
		ContextLabelHealthDetail:  {11},      // Labels
		ContextLabelDrilldown:     {11},      // Labels
		ContextLabelGraphAnalysis: {6, 11},   // Graph, Labels
		ContextTimeTravelInput:    {10},      // Time-Travel
		ContextQuitConfirm:        {1},       // Navigation basics
		ContextDeleteConfirm:      {4},       // Detail view actions
		ContextCassSession:        {8},       // History (cass integrates with history)
	}
	if pages, ok := pageMap[c]; ok {
		return pages
	}
	return []int{0} // Default to intro page
}
