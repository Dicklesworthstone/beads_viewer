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
	ContextLabelHealthDetail  Context = "label-health-detail"
	ContextLabelDrilldown     Context = "label-drilldown"
	ContextLabelGraphAnalysis Context = "label-graph-analysis"
	ContextTimeTravelInput    Context = "time-travel-input"
	ContextAlerts             Context = "alerts"
	ContextRepoPicker         Context = "repo-picker"
	ContextAgentPrompt        Context = "agent-prompt"
	ContextCassSession        Context = "cass-session"

	// Views
	ContextInsights           Context = "insights"
	ContextFlowMatrix         Context = "flow-matrix"
	ContextGraph              Context = "graph"
	ContextBoard              Context = "board"
	ContextTree               Context = "tree"
	ContextActionable         Context = "actionable"
	ContextHistory            Context = "history"
	ContextSprint             Context = "sprint"
	ContextLabelDashboard     Context = "label-dashboard"
	ContextAttention          Context = "attention"
	ContextVelocityComparison Context = "velocity-comparison"

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

	// Velocity comparison sub-view (opened from Label Dashboard via v)
	if m.showVelocityComparison || m.focused == focusVelocityComparison {
		return ContextVelocityComparison
	}

	// Attention view (bv-117) is its own focus, opened with ]
	if m.focused == focusAttention {
		return ContextAttention
	}

	// Insights panel
	if m.focused == focusInsights {
		return ContextInsights
	}

	// Flow matrix view
	if m.focused == focusFlowMatrix {
		return ContextFlowMatrix
	}

	// Hierarchical tree view
	if m.focused == focusTree {
		return ContextTree
	}

	// Label dashboard
	if m.focused == focusLabelDashboard {
		return ContextLabelDashboard
	}

	// Graph view
	if m.isGraphView || m.focused == focusGraph {
		return ContextGraph
	}

	// Board view
	if m.isBoardView || m.focused == focusBoard {
		return ContextBoard
	}

	// Actionable view
	if m.isActionableView || m.focused == focusActionable {
		return ContextActionable
	}

	// History view
	if m.isHistoryView || m.focused == focusHistory {
		return ContextHistory
	}

	// Sprint view
	if m.isSprintView || m.focused == focusSprint {
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
		ContextTree:               "Tree view",
		ContextActionable:         "Actionable view",
		ContextHistory:            "History view",
		ContextSprint:             "Sprint view",
		ContextLabelDashboard:     "Label dashboard",
		ContextAttention:          "Attention view",
		ContextVelocityComparison: "Velocity comparison",
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
	case ContextLabelPicker, ContextRecipePicker, ContextHelp, ContextQuitConfirm,
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
		ContextTree, ContextActionable, ContextHistory, ContextSprint,
		ContextLabelDashboard, ContextAttention, ContextVelocityComparison,
		ContextSplit, ContextDetail, ContextTimeTravel:
		return true
	}
	return false
}

// AllContexts returns every UI Context emitted by Model.CurrentContext().
func AllContexts() []Context {
	return []Context{
		ContextLabelPicker,
		ContextRecipePicker,
		ContextHelp,
		ContextQuitConfirm,
		ContextLabelHealthDetail,
		ContextLabelDrilldown,
		ContextLabelGraphAnalysis,
		ContextTimeTravelInput,
		ContextAlerts,
		ContextRepoPicker,
		ContextAgentPrompt,
		ContextCassSession,
		ContextInsights,
		ContextFlowMatrix,
		ContextGraph,
		ContextBoard,
		ContextTree,
		ContextActionable,
		ContextHistory,
		ContextSprint,
		ContextLabelDashboard,
		ContextAttention,
		ContextVelocityComparison,
		ContextSplit,
		ContextDetail,
		ContextTimeTravel,
		ContextFilter,
		ContextList,
	}
}

// TutorialPages returns the recommended tutorial page indices for this context.
// Indices match defaultTutorialPages(), falling back to page 0 when no page is tagged.
func (c Context) TutorialPages() []int {
	target := string(c)
	pages := defaultTutorialPages()
	var matches []int
	for i, page := range pages {
		for _, ctx := range page.Contexts {
			if ctx == target {
				matches = append(matches, i)
				break
			}
		}
	}
	if len(matches) > 0 {
		return matches
	}
	return []int{0}
}
