package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// newTestModel creates a minimal Model for testing context detection
func newTestModel() Model {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	return Model{
		theme:   theme,
		focused: focusList,
		list:    list.New(nil, list.NewDefaultDelegate(), 80, 20),
	}
}

func TestCurrentContext_Default(t *testing.T) {
	m := newTestModel()
	if ctx := m.CurrentContext(); ctx != ContextList {
		t.Errorf("Default context should be ContextList, got %q", ctx)
	}
}

func TestCurrentContext_Overlays(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Model)
		expected Context
	}{
		{
			name:     "agent prompt",
			setup:    func(m *Model) { m.showAgentPrompt = true },
			expected: ContextAgentPrompt,
		},
		{
			name:     "help overlay",
			setup:    func(m *Model) { m.showHelp = true },
			expected: ContextHelp,
		},
		{
			name:     "quit confirm",
			setup:    func(m *Model) { m.showQuitConfirm = true },
			expected: ContextQuitConfirm,
		},
		{
			name:     "label picker",
			setup:    func(m *Model) { m.showLabelPicker = true },
			expected: ContextLabelPicker,
		},
		{
			name:     "recipe picker",
			setup:    func(m *Model) { m.showRecipePicker = true },
			expected: ContextRecipePicker,
		},
		{
			name:     "label health detail",
			setup:    func(m *Model) { m.showLabelHealthDetail = true },
			expected: ContextLabelHealthDetail,
		},
		{
			name:     "label drilldown",
			setup:    func(m *Model) { m.showLabelDrilldown = true },
			expected: ContextLabelDrilldown,
		},
		{
			name:     "label graph analysis",
			setup:    func(m *Model) { m.showLabelGraphAnalysis = true },
			expected: ContextLabelGraphAnalysis,
		},
		{
			name:     "time travel input",
			setup:    func(m *Model) { m.showTimeTravelPrompt = true },
			expected: ContextTimeTravelInput,
		},
		{
			name:     "alerts panel",
			setup:    func(m *Model) { m.showAlertsPanel = true },
			expected: ContextAlerts,
		},
		{
			name:     "repo picker",
			setup:    func(m *Model) { m.showRepoPicker = true },
			expected: ContextRepoPicker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			tt.setup(&m)
			if ctx := m.CurrentContext(); ctx != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, ctx)
			}
		})
	}
}

func TestCurrentContext_Views(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Model)
		expected Context
	}{
		{
			name:     "insights panel",
			setup:    func(m *Model) { m.focused = focusInsights },
			expected: ContextInsights,
		},
		{
			name:     "attention view",
			setup:    func(m *Model) { m.focused = focusAttention },
			expected: ContextAttention,
		},
		{
			name:     "flow matrix",
			setup:    func(m *Model) { m.focused = focusFlowMatrix },
			expected: ContextFlowMatrix,
		},
		{
			name:     "label dashboard",
			setup:    func(m *Model) { m.focused = focusLabelDashboard },
			expected: ContextLabelDashboard,
		},
		{
			name:     "velocity comparison",
			setup:    func(m *Model) { m.focused = focusVelocityComparison },
			expected: ContextVelocityComparison,
		},
		{
			name:     "tree view",
			setup:    func(m *Model) { m.focused = focusTree },
			expected: ContextTree,
		},
		{
			name:     "graph view",
			setup:    func(m *Model) { m.isGraphView = true },
			expected: ContextGraph,
		},
		{
			name:     "board view",
			setup:    func(m *Model) { m.isBoardView = true },
			expected: ContextBoard,
		},
		{
			name:     "actionable view",
			setup:    func(m *Model) { m.isActionableView = true },
			expected: ContextActionable,
		},
		{
			name:     "history view",
			setup:    func(m *Model) { m.isHistoryView = true },
			expected: ContextHistory,
		},
		{
			name:     "sprint view",
			setup:    func(m *Model) { m.isSprintView = true },
			expected: ContextSprint,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			tt.setup(&m)
			if ctx := m.CurrentContext(); ctx != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, ctx)
			}
		})
	}
}

func TestCurrentContext_DetailStates(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Model)
		expected Context
	}{
		{
			name:     "time travel mode",
			setup:    func(m *Model) { m.timeTravelMode = true },
			expected: ContextTimeTravel,
		},
		{
			name:     "split view",
			setup:    func(m *Model) { m.isSplitView = true },
			expected: ContextSplit,
		},
		{
			name:     "detail view",
			setup:    func(m *Model) { m.showDetails = true },
			expected: ContextDetail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			tt.setup(&m)
			if ctx := m.CurrentContext(); ctx != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, ctx)
			}
		})
	}
}

func TestCurrentContext_FilterState(t *testing.T) {
	m := newTestModel()
	// Simulate active filter by setting filter state
	m.list.SetFilterState(list.Filtering)

	if ctx := m.CurrentContext(); ctx != ContextFilter {
		t.Errorf("Expected ContextFilter when filtering, got %q", ctx)
	}
}

func TestCurrentContext_Priority(t *testing.T) {
	// Test that overlays take priority over views
	m := newTestModel()
	m.showHelp = true       // Overlay
	m.isGraphView = true    // View
	m.timeTravelMode = true // Detail state

	// Overlay should win
	if ctx := m.CurrentContext(); ctx != ContextHelp {
		t.Errorf("Overlay should take priority, got %q", ctx)
	}

	// Remove overlay, view should win over detail state
	m.showHelp = false
	if ctx := m.CurrentContext(); ctx != ContextGraph {
		t.Errorf("View should take priority over detail state, got %q", ctx)
	}

	// Remove view, detail state should win
	m.isGraphView = false
	if ctx := m.CurrentContext(); ctx != ContextTimeTravel {
		t.Errorf("Detail state should take priority over default, got %q", ctx)
	}
}

func TestContext_Description(t *testing.T) {
	tests := []struct {
		context     Context
		expected    string
		shouldMatch bool
	}{
		{ContextList, "Issue list", true},
		{ContextGraph, "Dependency graph", true},
		{ContextHelp, "Help overlay", true},
		{Context("unknown"), "unknown", true}, // Fallback to string value
	}

	for _, tt := range tests {
		t.Run(string(tt.context), func(t *testing.T) {
			desc := tt.context.Description()
			if tt.shouldMatch && desc != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, desc)
			}
		})
	}
}

func TestContext_IsOverlay(t *testing.T) {
	overlays := []Context{
		ContextLabelPicker, ContextRecipePicker, ContextHelp, ContextQuitConfirm,
		ContextLabelHealthDetail, ContextLabelDrilldown, ContextLabelGraphAnalysis,
		ContextTimeTravelInput, ContextAlerts, ContextRepoPicker, ContextAgentPrompt,
	}

	for _, c := range overlays {
		if !c.IsOverlay() {
			t.Errorf("%q should be an overlay", c)
		}
	}

	nonOverlays := []Context{
		ContextList, ContextGraph, ContextBoard, ContextInsights, ContextHistory,
	}

	for _, c := range nonOverlays {
		if c.IsOverlay() {
			t.Errorf("%q should not be an overlay", c)
		}
	}
}

func TestContext_IsView(t *testing.T) {
	views := []Context{
		ContextInsights, ContextFlowMatrix, ContextGraph, ContextBoard,
		ContextTree, ContextActionable, ContextHistory, ContextSprint,
		ContextLabelDashboard, ContextAttention, ContextVelocityComparison,
		ContextSplit, ContextDetail, ContextTimeTravel,
	}

	for _, c := range views {
		if !c.IsView() {
			t.Errorf("%q should be a view", c)
		}
	}

	nonViews := []Context{
		ContextList, ContextHelp, ContextQuitConfirm, ContextFilter,
	}

	for _, c := range nonViews {
		if c.IsView() {
			t.Errorf("%q should not be a view", c)
		}
	}
}

func TestContext_TutorialPages(t *testing.T) {
	tests := []struct {
		context  Context
		minPages int // Minimum expected pages
	}{
		{ContextList, 1},
		{ContextGraph, 1},
		{ContextBoard, 1},
		{ContextFilter, 1},
		{ContextHelp, 1},
		{Context("unknown"), 1}, // Should return default
	}

	for _, tt := range tests {
		t.Run(string(tt.context), func(t *testing.T) {
			pages := tt.context.TutorialPages()
			if len(pages) < tt.minPages {
				t.Errorf("Expected at least %d pages for %q, got %d", tt.minPages, tt.context, len(pages))
			}
		})
	}
}
