package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestContextHelpContentMap(t *testing.T) {
	// Verify all expected contexts have help content
	expectedContexts := []Context{
		ContextList,
		ContextGraph,
		ContextBoard,
		ContextInsights,
		ContextHistory,
		ContextDetail,
		ContextSplit,
		ContextFilter,
		ContextLabelPicker,
		ContextRecipePicker,
		ContextHelp,
		ContextTimeTravel,
		ContextLabelDashboard,
		ContextAttention,
		ContextAgentPrompt,
	}

	for _, ctx := range expectedContexts {
		content, ok := ContextHelpContent[ctx]
		if !ok {
			t.Errorf("ContextHelpContent missing entry for context: %v", ctx)
			continue
		}
		if content == "" {
			t.Errorf("ContextHelpContent has empty content for context: %v", ctx)
		}
	}
}

func TestGetContextHelp(t *testing.T) {
	tests := []struct {
		name     string
		ctx      Context
		contains string // expected substring in the result
	}{
		{
			name:     "list context",
			ctx:      ContextList,
			contains: "List View",
		},
		{
			name:     "graph context",
			ctx:      ContextGraph,
			contains: "Graph View",
		},
		{
			name:     "board context",
			ctx:      ContextBoard,
			contains: "Board View",
		},
		{
			name:     "insights context",
			ctx:      ContextInsights,
			contains: "Insights Panel",
		},
		{
			name:     "history context",
			ctx:      ContextHistory,
			contains: "History View",
		},
		{
			name:     "detail context",
			ctx:      ContextDetail,
			contains: "Detail View",
		},
		{
			name:     "split context",
			ctx:      ContextSplit,
			contains: "Split View",
		},
		{
			name:     "filter context",
			ctx:      ContextFilter,
			contains: "Filter Mode",
		},
		{
			name:     "unknown context falls back to generic",
			ctx:      Context("unknown-context"), // Invalid context
			contains: "Quick Reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetContextHelp(tt.ctx)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("GetContextHelp(%v) should contain %q, got: %s", tt.ctx, tt.contains, result)
			}
		})
	}
}

func TestGetContextHelpFallback(t *testing.T) {
	// Test that unknown contexts fall back to generic content
	unknownCtx := Context("nonexistent-context")
	result := GetContextHelp(unknownCtx)

	if result != contextHelpGeneric {
		t.Errorf("GetContextHelp for unknown context should return contextHelpGeneric")
	}

	// Generic content should contain basic navigation
	if !strings.Contains(result, "Global Keys") {
		t.Error("Generic help should contain Global Keys section")
	}
}

func TestContextHelpContentQuality(t *testing.T) {
	// Verify each help content has expected structure
	for ctx, content := range ContextHelpContent {
		t.Run(fmt.Sprintf("context_%s", ctx), func(t *testing.T) {
			// Should have a heading
			if !strings.Contains(content, "##") {
				t.Errorf("Context %v help should have markdown heading", ctx)
			}

			// Most should have Navigation/Actions/Input/Focus/Search section (except generic)
			if ctx != ContextHelp && !strings.Contains(content, "Navigation") &&
				!strings.Contains(content, "Actions") &&
				!strings.Contains(content, "Input") &&
				!strings.Contains(content, "Focus") &&
				!strings.Contains(content, "Search") {
				t.Errorf("Context %v help should have Navigation/Actions/Input/Focus/Search section", ctx)
			}

			// Should not be too short (at least 100 chars of useful content)
			if len(content) < 100 {
				t.Errorf("Context %v help content too short (%d chars)", ctx, len(content))
			}

			// Should not be too long (compact modal, aim for ~20 lines)
			lines := strings.Count(content, "\n")
			if lines > 30 {
				t.Errorf("Context %v help has %d lines (should be <=30 for compact display)", ctx, lines)
			}
		})
	}
}

func TestRenderContextHelp(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	width, height := 80, 40

	result := RenderContextHelp(ContextList, theme, width, height)

	// Should have modal border
	if !strings.Contains(result, "╭") || !strings.Contains(result, "╮") {
		t.Error("RenderContextHelp should render with rounded border")
	}

	// Should have title
	if !strings.Contains(result, "Quick Reference") {
		t.Error("RenderContextHelp should show 'Quick Reference' title")
	}

	// Should have footer hint
	if !strings.Contains(result, "Esc to close") {
		t.Error("RenderContextHelp should show close hint")
	}

	// Should have context-specific content
	if !strings.Contains(result, "List View") {
		t.Error("RenderContextHelp should include context-specific content")
	}
}

func TestRenderContextHelpNarrowWidth(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	narrowWidth := 50
	height := 40

	result := RenderContextHelp(ContextList, theme, narrowWidth, height)

	// Should adapt to narrow width (modal width = width - 4)
	// Just verify it renders without panicking
	if result == "" {
		t.Error("RenderContextHelp should produce output even for narrow width")
	}
}

func TestContextHelpKeyboardShortcuts(t *testing.T) {
	// Verify essential shortcuts are documented in relevant contexts
	tests := []struct {
		ctx      Context
		shortcut string
	}{
		{ContextList, "j/k"},
		{ContextList, "Enter"},
		{ContextGraph, "h/l"},
		{ContextGraph, "f"},
		{ContextBoard, "h/l"},
		{ContextDetail, "Esc"},
		{ContextDetail, "y"},
		{ContextDetail, "C"},
		{ContextDetail, "D"},
		{ContextSplit, "Tab"},
		{ContextFilter, "/"},
	}

	for _, tt := range tests {
		content := GetContextHelp(tt.ctx)
		if !strings.Contains(content, tt.shortcut) {
			t.Errorf("Context %v help should document shortcut %q", tt.ctx, tt.shortcut)
		}
	}
}

// =============================================================================
// ADDITIONAL TESTS FOR BV-WE18: Context Help Content Coverage
// =============================================================================

func TestContextHelpExitHints(t *testing.T) {
	// Each context should mention how to exit/close
	exitPatterns := []string{"Esc", "Return", "Close", "Exit", "back", "cancel", "quit"}

	for ctx, content := range ContextHelpContent {
		t.Run(fmt.Sprintf("context_%s_has_exit_hint", ctx), func(t *testing.T) {
			hasExit := false
			contentLower := strings.ToLower(content)
			for _, pattern := range exitPatterns {
				if strings.Contains(contentLower, strings.ToLower(pattern)) {
					hasExit = true
					break
				}
			}
			if !hasExit {
				t.Errorf("Context %v help should mention how to exit/close (e.g., Esc, Return, Close)", ctx)
			}
		})
	}
}

func TestContextHelpNoPlaceholders(t *testing.T) {
	// Verify no placeholder text like "TODO", "Coming soon", "TBD", etc.
	placeholders := []string{"TODO", "FIXME", "Coming soon", "TBD", "placeholder", "not implemented"}

	for ctx, content := range ContextHelpContent {
		t.Run(fmt.Sprintf("context_%s_no_placeholders", ctx), func(t *testing.T) {
			contentLower := strings.ToLower(content)
			for _, placeholder := range placeholders {
				if strings.Contains(contentLower, strings.ToLower(placeholder)) {
					t.Errorf("Context %v help contains placeholder text %q", ctx, placeholder)
				}
			}
		})
	}
}

func TestContextHelpCompactWidth(t *testing.T) {
	// Verify content lines fit within modal width (60 chars)
	maxLineWidth := 60

	for ctx, content := range ContextHelpContent {
		t.Run(fmt.Sprintf("context_%s_compact_width", ctx), func(t *testing.T) {
			lines := strings.Split(content, "\n")
			for i, line := range lines {
				// Skip markdown headers - they're bold and short
				if strings.HasPrefix(strings.TrimSpace(line), "##") {
					continue
				}
				// Skip section headers like **Navigation**
				if strings.HasPrefix(strings.TrimSpace(line), "**") {
					continue
				}
				if len(line) > maxLineWidth {
					t.Errorf("Context %v line %d exceeds %d chars (%d): %q",
						ctx, i+1, maxLineWidth, len(line), line)
				}
			}
		})
	}
}

func TestContextHelpListShortcutsMatchModel(t *testing.T) {
	// Verify list context shortcuts are accurate
	// These are the documented shortcuts that should work in list view
	content := GetContextHelp(ContextList)

	requiredShortcuts := []struct {
		shortcut    string
		description string
	}{
		{"j/k", "vertical navigation"},
		{"Enter", "view details"},
		{"g/G", "jump to top/bottom"},
		{"/", "search"},
	}

	for _, rs := range requiredShortcuts {
		if !strings.Contains(content, rs.shortcut) {
			t.Errorf("List context help missing %s for %s", rs.shortcut, rs.description)
		}
	}
}

func TestContextHelpBoardShortcutsMatchModel(t *testing.T) {
	// Verify board context shortcuts are accurate
	content := GetContextHelp(ContextBoard)

	requiredShortcuts := []struct {
		shortcut    string
		description string
	}{
		{"h/l", "column navigation"},
		{"j/k", "within column"},
		{"/", "search"},
		{"Tab", "detail panel"},
		{"Esc", "exit to list"},
	}

	for _, rs := range requiredShortcuts {
		if !strings.Contains(content, rs.shortcut) {
			t.Errorf("Board context help missing %s for %s", rs.shortcut, rs.description)
		}
	}
}

func TestContextHelpHistoryShortcutsMatchModel(t *testing.T) {
	// Verify history context shortcuts are accurate
	content := GetContextHelp(ContextHistory)

	requiredShortcuts := []struct {
		shortcut    string
		description string
	}{
		{"j/k", "primary navigation"},
		{"J/K", "secondary navigation"},
		{"Tab", "toggle focus"},
		{"v", "toggle view mode"},
		{"/", "search"},
		{"y", "copy SHA"},
	}

	for _, rs := range requiredShortcuts {
		if !strings.Contains(content, rs.shortcut) {
			t.Errorf("History context help missing %s for %s", rs.shortcut, rs.description)
		}
	}
}

func TestContextHelpGraphShortcutsMatchModel(t *testing.T) {
	// Verify graph context shortcuts are accurate
	content := GetContextHelp(ContextGraph)

	requiredShortcuts := []struct {
		shortcut    string
		description string
	}{
		{"j/k", "vertical navigation"},
		{"h/l", "sibling navigation"},
		{"Enter", "view issue"},
		{"f", "focus subgraph"},
		{"Esc", "exit"},
	}

	for _, rs := range requiredShortcuts {
		if !strings.Contains(content, rs.shortcut) {
			t.Errorf("Graph context help missing %s for %s", rs.shortcut, rs.description)
		}
	}
}

func TestRenderContextHelpVeryNarrow(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	veryNarrowWidth := 30
	height := 40

	// Should not panic with very narrow width
	result := RenderContextHelp(ContextList, theme, veryNarrowWidth, height)
	if result == "" {
		t.Error("RenderContextHelp should produce output for very narrow width")
	}
}

func TestRenderContextHelpVeryShort(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	width := 80
	veryShortHeight := 10

	// Should not panic with very short height
	result := RenderContextHelp(ContextList, theme, width, veryShortHeight)
	if result == "" {
		t.Error("RenderContextHelp should produce output for very short height")
	}
}

func TestRenderContextHelpMinimalDimensions(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	// Test minimal dimensions without panicking
	result := RenderContextHelp(ContextList, theme, 10, 5)
	if result == "" {
		t.Error("RenderContextHelp should produce output for minimal dimensions")
	}
}

func TestContextHelpUnicodeRendering(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	width, height := 80, 40

	// Test that unicode characters in content are preserved
	result := RenderContextHelp(ContextBoard, theme, width, height)

	// Border should have unicode box drawing characters
	if !strings.Contains(result, "╭") || !strings.Contains(result, "─") {
		t.Error("RenderContextHelp should render unicode border characters")
	}
}

func TestContextHelpAllContextsRender(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	width, height := 80, 40

	// Verify all contexts render without error
	for ctx := range ContextHelpContent {
		t.Run(fmt.Sprintf("render_%s", ctx), func(t *testing.T) {
			result := RenderContextHelp(ctx, theme, width, height)
			if result == "" {
				t.Errorf("RenderContextHelp(%v) should produce non-empty output", ctx)
			}
			// Should contain context-specific content
			if !strings.Contains(result, "##") {
				t.Errorf("RenderContextHelp(%v) should include content with heading", ctx)
			}
		})
	}
}

func TestContextHelpFilterModeComplete(t *testing.T) {
	// Filter mode help should document all status filter keys
	content := GetContextHelp(ContextFilter)

	filterShortcuts := []string{"o", "c", "r", "a"}
	for _, key := range filterShortcuts {
		if !strings.Contains(content, key) {
			t.Errorf("Filter context help should document %q filter key", key)
		}
	}
}

func TestContextHelpInsightsComplete(t *testing.T) {
	// Insights panel help should explain what the metrics mean
	content := GetContextHelp(ContextInsights)

	// Should mention key metrics concepts
	concepts := []string{"Priority", "Blocked", "Attention"}
	for _, concept := range concepts {
		if !strings.Contains(content, concept) {
			t.Errorf("Insights context help should explain %q concept", concept)
		}
	}
}

func TestContextHelpGenericFallback(t *testing.T) {
	// Verify generic fallback contains universal shortcuts
	generic := contextHelpGeneric

	universalShortcuts := []string{"?", "Esc", "q", "j/k"}
	for _, shortcut := range universalShortcuts {
		if !strings.Contains(generic, shortcut) {
			t.Errorf("Generic context help should include universal shortcut %q", shortcut)
		}
	}
}
