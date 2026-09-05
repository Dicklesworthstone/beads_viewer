package ui

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
)

// ANSI styles can expand a few KiB of Markdown into hundreds of KiB. Keep
// one output within a fixed half-MiB budget, independent of issue count.
const maxRememberedMarkdownBytes = 512 * 1024

// MarkdownRenderer provides theme-aware markdown rendering using glamour.
// It detects the terminal's color scheme and uses appropriate styles.
type MarkdownRenderer struct {
	renderer *glamour.TermRenderer
	width    int
	isDark   bool
	theme    *Theme // nil if using built-in styles, non-nil if using custom theme
	useTheme bool   // true if created with NewMarkdownRendererWithTheme

	// Remember only the last exact render. Snapshot delivery often refreshes
	// unchanged selected details; a following key still renders its new issue.
	// Renderer identity includes width and theme, so their replacement cannot
	// reuse stale styling. Both strings together are bounded by the limit above.
	lastRenderer *glamour.TermRenderer
	lastMarkdown string
	lastRendered string
}

// NewMarkdownRenderer creates a new markdown renderer using built-in styles.
// It uses Dracula style for dark terminals and a light style for light terminals.
// Prefer NewMarkdownRendererWithTheme for consistent styling with the bv Theme.
func NewMarkdownRenderer(width int) *MarkdownRenderer {
	isDark := lipgloss.HasDarkBackground()

	var styleName string
	if isDark {
		styleName = "dracula"
	} else {
		styleName = "light"
	}

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylePath(styleName),
		glamour.WithWordWrap(width),
	)

	return &MarkdownRenderer{
		renderer: renderer,
		width:    width,
		isDark:   isDark,
		theme:    nil,
		useTheme: false,
	}
}

// NewMarkdownRendererWithTheme creates a markdown renderer using custom colors
// that match the provided Theme for visual consistency.
func NewMarkdownRendererWithTheme(width int, theme Theme) *MarkdownRenderer {
	isDark := lipgloss.HasDarkBackground()
	styleConfig := buildStyleFromTheme(theme, isDark)

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styleConfig),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Fall back to built-in style if custom theme fails
		var styleName string
		if isDark {
			styleName = "dracula"
		} else {
			styleName = "light"
		}
		renderer, _ = glamour.NewTermRenderer(
			glamour.WithStylePath(styleName),
			glamour.WithWordWrap(width),
		)
	}

	return &MarkdownRenderer{
		renderer: renderer,
		width:    width,
		isDark:   isDark,
		theme:    &theme,
		useTheme: true,
	}
}

// Render converts markdown content to styled terminal output.
func (mr *MarkdownRenderer) Render(markdown string) (string, error) {
	if mr.renderer == nil {
		return markdown, nil
	}
	if mr.lastRenderer == mr.renderer && mr.lastMarkdown == markdown {
		return mr.lastRendered, nil
	}
	rendered, err := mr.renderer.Render(markdown)
	mr.lastRenderer = nil
	mr.lastMarkdown = ""
	mr.lastRendered = ""
	if err == nil && len(markdown)+len(rendered) <= maxRememberedMarkdownBytes {
		mr.lastRenderer = mr.renderer
		mr.lastMarkdown = markdown
		mr.lastRendered = rendered
	}
	return rendered, err
}

// SetWidth updates the word wrap width and recreates the renderer.
// If the renderer was created with a theme, the theme is preserved.
// Width is only updated if the new renderer is created successfully.
func (mr *MarkdownRenderer) SetWidth(width int) {
	if width == mr.width || width <= 0 {
		return
	}

	// If created with a theme, preserve it
	if mr.useTheme && mr.theme != nil {
		styleConfig := buildStyleFromTheme(*mr.theme, mr.isDark)
		if r, err := glamour.NewTermRenderer(
			glamour.WithStyles(styleConfig),
			glamour.WithWordWrap(width),
		); err == nil {
			mr.renderer = r
			mr.width = width
		}
		return
	}

	// Otherwise use built-in styles
	var styleName string
	if mr.isDark {
		styleName = "dracula"
	} else {
		styleName = "light"
	}

	if r, err := glamour.NewTermRenderer(
		glamour.WithStylePath(styleName),
		glamour.WithWordWrap(width),
	); err == nil {
		mr.renderer = r
		mr.width = width
	}
}

// SetWidthWithTheme updates width and recreates renderer with theme colors.
// This also updates the stored theme for future SetWidth calls.
// If width is the same but theme differs, the renderer is still recreated with the new theme.
// Falls back to built-in styles if custom theme fails.
func (mr *MarkdownRenderer) SetWidthWithTheme(width int, theme Theme) {
	if width <= 0 {
		return
	}

	// Allow recreation even if width is the same (theme might have changed)
	styleConfig := buildStyleFromTheme(theme, mr.isDark)

	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styleConfig),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Fall back to built-in style if custom theme fails
		var styleName string
		if mr.isDark {
			styleName = "dracula"
		} else {
			styleName = "light"
		}
		r, _ = glamour.NewTermRenderer(
			glamour.WithStylePath(styleName),
			glamour.WithWordWrap(width),
		)
	}
	if r != nil {
		mr.renderer = r
		mr.width = width
		mr.theme = &theme
		mr.useTheme = true
	}
}

// IsDarkMode returns whether the renderer is using dark mode styling.
func (mr *MarkdownRenderer) IsDarkMode() bool {
	return mr.isDark
}

// buildStyleFromTheme creates a glamour StyleConfig that matches the bv Theme.
func buildStyleFromTheme(theme Theme, isDark bool) ansi.StyleConfig {
	// Extract hex colors from adaptive colors
	primaryColor := extractHex(theme.Primary, isDark)
	secondaryColor := extractHex(theme.Secondary, isDark)
	openColor := extractHex(theme.Open, isDark)
	featureColor := extractHex(theme.Feature, isDark)
	inProgressColor := extractHex(theme.InProgress, isDark)
	mutedColor := extractHex(theme.Muted, isDark)
	blockedColor := extractHex(theme.Blocked, isDark)

	// Base document style - use terminal default background for both modes.
	// Previously dark mode set an explicit Dracula background (#282a36), but
	// terminals that remap ANSI color slots (e.g., Solarized Dark) could
	// render it as vivid green (ANSI #2) when the hex value is downconverted
	// to the 16-color palette. Using nil lets the terminal's own background
	// show through, which is correct for every theme. (fixes #101)
	var docBgPtr *string // nil = terminal default background
	var docFg string
	if isDark {
		docFg = "#f8f8f2"
	} else {
		docFg = "#000000"
	}

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:           stringPtr(docFg),
				BackgroundColor: docBgPtr,
			},
			Margin: uintPtr(0),
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  stringPtr(featureColor),
				Italic: boolPtr(true),
			},
			Indent: uintPtr(2),
			Margin: uintPtr(0),
		},
		Paragraph: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(docFg),
			},
			Margin: uintPtr(0),
		},
		List: ansi.StyleList{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: stringPtr(docFg),
				},
				Margin: uintPtr(0),
			},
			LevelIndent: 2,
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(primaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(primaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(primaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(primaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H4: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(secondaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H5: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(secondaryColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		H6: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(mutedColor),
				Bold:  boolPtr(true),
			},
			Margin: uintPtr(0),
		},
		Strong: ansi.StylePrimitive{
			Color: stringPtr(featureColor),
			Bold:  boolPtr(true),
		},
		Emph: ansi.StylePrimitive{
			Color:  stringPtr(inProgressColor),
			Italic: boolPtr(true),
		},
		Strikethrough: ansi.StylePrimitive{
			CrossedOut: boolPtr(true),
		},
		HorizontalRule: ansi.StylePrimitive{
			Color:  stringPtr(mutedColor),
			Format: "─────────────────────────────────────────",
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: "• ",
		},
		Enumeration: ansi.StylePrimitive{
			Color:       stringPtr(inProgressColor),
			BlockPrefix: ". ",
		},
		Task: ansi.StyleTask{
			Ticked:   "[✓] ",
			Unticked: "[ ] ",
		},
		Link: ansi.StylePrimitive{
			Color:     stringPtr(inProgressColor),
			Underline: boolPtr(true),
		},
		LinkText: ansi.StylePrimitive{
			Color: stringPtr(primaryColor),
		},
		Image: ansi.StylePrimitive{
			Color:     stringPtr(inProgressColor),
			Underline: boolPtr(true),
		},
		ImageText: ansi.StylePrimitive{
			Color:  stringPtr(docFg),
			Format: "Image: {{.text}} →",
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(openColor),
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: stringPtr(featureColor),
				},
				Margin: uintPtr(0),
			},
			Chroma: &ansi.Chroma{
				Text: ansi.StylePrimitive{
					Color: stringPtr(docFg),
				},
				Error: ansi.StylePrimitive{
					Color: stringPtr(blockedColor),
				},
				Comment: ansi.StylePrimitive{
					Color:  stringPtr(mutedColor),
					Italic: boolPtr(true),
				},
				CommentPreproc: ansi.StylePrimitive{
					Color: stringPtr(inProgressColor),
				},
				Keyword: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				KeywordReserved: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				KeywordNamespace: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				KeywordType: ansi.StylePrimitive{
					Color: stringPtr(inProgressColor),
				},
				Operator: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				Punctuation: ansi.StylePrimitive{
					Color: stringPtr(docFg),
				},
				Name: ansi.StylePrimitive{
					Color: stringPtr(inProgressColor),
				},
				NameBuiltin: ansi.StylePrimitive{
					Color: stringPtr(inProgressColor),
				},
				NameTag: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				NameAttribute: ansi.StylePrimitive{
					Color: stringPtr(openColor),
				},
				NameClass: ansi.StylePrimitive{
					Color: stringPtr(inProgressColor),
				},
				NameConstant: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				NameDecorator: ansi.StylePrimitive{
					Color: stringPtr(openColor),
				},
				NameException: ansi.StylePrimitive{
					Color: stringPtr(blockedColor),
				},
				NameFunction: ansi.StylePrimitive{
					Color: stringPtr(openColor),
				},
				NameOther: ansi.StylePrimitive{
					Color: stringPtr(docFg),
				},
				Literal: ansi.StylePrimitive{
					Color: stringPtr(featureColor),
				},
				LiteralNumber: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				LiteralDate: ansi.StylePrimitive{
					Color: stringPtr(featureColor),
				},
				LiteralString: ansi.StylePrimitive{
					Color: stringPtr(featureColor),
				},
				LiteralStringEscape: ansi.StylePrimitive{
					Color: stringPtr(primaryColor),
				},
				GenericDeleted: ansi.StylePrimitive{
					Color: stringPtr(blockedColor),
				},
				GenericEmph: ansi.StylePrimitive{
					Italic: boolPtr(true),
				},
				GenericInserted: ansi.StylePrimitive{
					Color: stringPtr(openColor),
				},
				GenericStrong: ansi.StylePrimitive{
					Bold: boolPtr(true),
				},
				GenericSubheading: ansi.StylePrimitive{
					Color: stringPtr(mutedColor),
				},
				Background: ansi.StylePrimitive{
					BackgroundColor: docBgPtr,
				},
			},
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: stringPtr(docFg),
				},
				Margin: uintPtr(0),
			},
			CenterSeparator: stringPtr("┼"),
			ColumnSeparator: stringPtr("│"),
			RowSeparator:    stringPtr("─"),
		},
		DefinitionDescription: ansi.StylePrimitive{
			Color:       stringPtr(docFg),
			BlockPrefix: "\n→ ",
		},
	}
}

// extractHex gets the hex color string from an AdaptiveColor.
func extractHex(ac lipgloss.AdaptiveColor, isDark bool) string {
	if isDark {
		return ac.Dark
	}
	return ac.Light
}

// Helper functions for pointer creation
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func uintPtr(u uint) *uint {
	return &u
}
