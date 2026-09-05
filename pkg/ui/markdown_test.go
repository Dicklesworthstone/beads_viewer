package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestNewMarkdownRenderer(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	if mr == nil {
		t.Fatal("NewMarkdownRenderer returned nil")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
	if mr.useTheme {
		t.Error("expected useTheme to be false for NewMarkdownRenderer")
	}
	if mr.theme != nil {
		t.Error("expected theme to be nil for NewMarkdownRenderer")
	}
}

func TestNewMarkdownRendererWithTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)
	if mr == nil {
		t.Fatal("NewMarkdownRendererWithTheme returned nil")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("expected useTheme to be true for NewMarkdownRendererWithTheme")
	}
	if mr.theme == nil {
		t.Error("expected theme to be stored")
	}
}

func TestMarkdownRenderer_Render(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	result, err := mr.Render("# Hello\n\nWorld")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	// Should contain "Hello" somewhere in the rendered output
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected result to contain 'Hello', got: %s", result)
	}
}

func TestMarkdownRenderer_RepeatedExactContentDoesNotRenderAgain(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	content := "# Selected issue\n\n" + strings.Repeat("日本語 🚀 café **Markdown** and dependency details.\n\n", 30)
	want, err := mr.Render(content)
	if err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(3, func() {
		got, err := mr.Render(content)
		if err != nil || got != want {
			t.Fatalf("identical detail changed: error=%v", err)
		}
	})
	if allocations != 0 {
		t.Fatalf("unchanged selected detail allocated %.0f objects; want zero repeated rendering work", allocations)
	}
}

func TestMarkdownRenderer_ReusedOutputMatchesFreshAfterChanges(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)
	base := "# Original selected issue\n\n" + strings.Repeat("日本語 café Markdown text for wrapping. ", 10)
	if _, err := mr.Render(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		width         int
		changeTheme   bool
	}{
		{"changed-selected-body", base + "\n\nNew dependency and changed body", 80, false},
		{"new-selection", "# Different issue\n\nCompletely different description", 80, false},
		{"narrower-width", base, 37, false},
		{"same-width-new-theme", base, 37, true},
		{"return-to-previous-body", base, 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.changeTheme {
				theme.Primary = lipgloss.AdaptiveColor{Light: "#ff0000", Dark: "#00ff00"}
				mr.SetWidthWithTheme(tc.width, theme)
			} else {
				mr.SetWidth(tc.width)
			}
			fresh := NewMarkdownRendererWithTheme(tc.width, theme)
			want, err := fresh.Render(tc.content)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mr.Render(tc.content)
			if err != nil || got != want {
				t.Fatalf("output differs from a fresh renderer after %s: error=%v", tc.name, err)
			}
		})
	}
}

func TestMarkdownRenderer_LastOutputMemoryIsBounded(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	if _, err := mr.Render("# Small selected issue"); err != nil {
		t.Fatal(err)
	}
	if mr.lastRenderer == nil {
		t.Fatal("small exact output was not retained")
	}
	large := strings.Repeat("large description ", maxRememberedMarkdownBytes/10)
	if _, err := mr.Render(large); err != nil {
		t.Fatal(err)
	}
	if mr.lastRenderer != nil || len(mr.lastMarkdown)+len(mr.lastRendered) != 0 {
		t.Fatal("oversized detail retained additional cache memory")
	}
}

func TestSelectedDependencyTreeUsesPlainTextWithoutChangingOutput(t *testing.T) {
	for _, kind := range []string{"realistic", "unicode"} {
		t.Run(kind, func(t *testing.T) {
			issues, err := testutil.PerformanceIssues(kind, 64, 20260904)
			if err != nil {
				t.Fatal(err)
			}
			m := settledPerformanceModel(t, issues)
			for i, value := range m.list.Items() {
				if item, ok := value.(IssueItem); ok && len(item.Issue.Dependencies) > 0 {
					m.list.Select(i)
					break
				}
			}
			m.updateViewportContent()
			markdown, rendered := m.renderer.lastMarkdown, m.renderer.lastRendered
			if !strings.Contains(markdown, "```text\n") {
				t.Fatal("generated dependency tree still asks the highlighter to infer a language")
			}
			before := strings.Replace(markdown, "```text\n", "```\n", 1)
			want, err := m.renderer.renderer.Render(before)
			if err != nil || rendered != want {
				t.Fatalf("explicit plain-text dependency tree changed terminal output: error=%v", err)
			}
		})
	}
}

func TestMarkdownRenderer_RenderNilRenderer(t *testing.T) {
	mr := &MarkdownRenderer{
		renderer: nil,
		width:    80,
	}
	result, err := mr.Render("# Test")
	if err != nil {
		t.Fatalf("Render with nil renderer should not error: %v", err)
	}
	if result != "# Test" {
		t.Errorf("expected raw markdown when renderer is nil, got: %s", result)
	}
}

func TestMarkdownRenderer_SetWidth(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	originalRenderer := mr.renderer

	// Same width should not recreate renderer
	mr.SetWidth(80)
	if mr.renderer != originalRenderer {
		t.Error("SetWidth with same width should not recreate renderer")
	}

	// Invalid width should not change anything
	mr.SetWidth(0)
	if mr.width != 80 {
		t.Error("SetWidth with 0 should not change width")
	}
	mr.SetWidth(-1)
	if mr.width != 80 {
		t.Error("SetWidth with negative should not change width")
	}

	// Different width should update
	mr.SetWidth(100)
	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
}

func TestMarkdownRenderer_SetWidthPreservesTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)

	if !mr.useTheme {
		t.Fatal("expected useTheme to be true")
	}

	// SetWidth should preserve theme
	mr.SetWidth(100)
	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("SetWidth should preserve useTheme flag")
	}
	if mr.theme == nil {
		t.Error("SetWidth should preserve theme")
	}
}

func TestMarkdownRenderer_SetWidthWithTheme(t *testing.T) {
	mr := NewMarkdownRenderer(80)

	if mr.useTheme {
		t.Fatal("expected useTheme to be false initially")
	}

	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr.SetWidthWithTheme(100, theme)

	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("SetWidthWithTheme should set useTheme to true")
	}
	if mr.theme == nil {
		t.Error("SetWidthWithTheme should store theme")
	}
}

func TestMarkdownRenderer_SetWidthWithThemeSameWidth(t *testing.T) {
	// SetWidthWithTheme should allow updating theme even with same width
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)

	originalRenderer := mr.renderer

	// Same width but (conceptually) different theme should recreate renderer
	mr.SetWidthWithTheme(80, theme)

	// Renderer should be recreated (different instance)
	if mr.renderer == originalRenderer {
		t.Error("SetWidthWithTheme with same width should still recreate renderer")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
}

func TestMarkdownRenderer_SetWidthWithThemeInvalidWidth(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	originalRenderer := mr.renderer

	mr.SetWidthWithTheme(0, DefaultTheme(lipgloss.DefaultRenderer()))
	if mr.width != 80 {
		t.Error("SetWidthWithTheme with width 0 should not change width")
	}
	if mr.renderer != originalRenderer {
		t.Error("SetWidthWithTheme with width 0 should not change renderer")
	}

	mr.SetWidthWithTheme(-1, DefaultTheme(lipgloss.DefaultRenderer()))
	if mr.width != 80 {
		t.Error("SetWidthWithTheme with negative width should not change width")
	}
}

func TestMarkdownRenderer_IsDarkMode(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	// Just verify it returns a boolean without panicking
	_ = mr.IsDarkMode()
}

func TestExtractHex(t *testing.T) {
	ac := lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#000000"}

	lightHex := extractHex(ac, false)
	if lightHex != "#ffffff" {
		t.Errorf("expected #ffffff for light mode, got %s", lightHex)
	}

	darkHex := extractHex(ac, true)
	if darkHex != "#000000" {
		t.Errorf("expected #000000 for dark mode, got %s", darkHex)
	}
}

func TestBuildStyleFromTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())

	// Test dark mode
	darkConfig := buildStyleFromTheme(theme, true)
	if darkConfig.Document.Color == nil {
		t.Error("expected Document.Color to be set")
	}
	if *darkConfig.Document.Color != "#f8f8f2" {
		t.Errorf("expected dark mode doc color #f8f8f2, got %s", *darkConfig.Document.Color)
	}
	// Dark mode background should be nil (transparent) to avoid Solarized/16-color
	// terminal issues where hex colors get downconverted to wrong ANSI slots (#101)
	if darkConfig.Document.BackgroundColor != nil {
		t.Errorf("expected dark mode BackgroundColor to be nil (transparent), got %v", *darkConfig.Document.BackgroundColor)
	}

	// Test light mode
	lightConfig := buildStyleFromTheme(theme, false)
	if *lightConfig.Document.Color != "#000000" {
		t.Errorf("expected light mode doc color #000000, got %s", *lightConfig.Document.Color)
	}
	// Light mode should have nil background (use terminal default)
	if lightConfig.Document.BackgroundColor != nil {
		t.Errorf("expected light mode BackgroundColor to be nil, got %v", lightConfig.Document.BackgroundColor)
	}
}
