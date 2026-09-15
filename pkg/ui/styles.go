package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ══════════════════════════════════════════════════════════════════════════════
// DESIGN TOKENS - Consistent spacing, colors, and visual language
// ══════════════════════════════════════════════════════════════════════════════

// Spacing constants for consistent layout (in characters)
const (
	SpaceXS = 1
	SpaceSM = 2
	SpaceMD = 3
	SpaceLG = 4
	SpaceXL = 6
)

// ══════════════════════════════════════════════════════════════════════════════
// COLOR PALETTE - Adaptive colors for light and dark terminals
// Light mode colors tuned for WCAG AA compliance (contrast ratio >= 4.5:1)
// ══════════════════════════════════════════════════════════════════════════════

var (
	// Base colors - Light mode uses darker colors for contrast on white backgrounds
	ColorBg          = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#282A36"}
	ColorBgDark      = lipgloss.AdaptiveColor{Light: "#F5F5F5", Dark: "#1E1F29"}
	ColorBgSubtle    = lipgloss.AdaptiveColor{Light: "#E8E8E8", Dark: "#363949"}
	ColorBgHighlight = lipgloss.AdaptiveColor{Light: "#D0D0D0", Dark: "#44475A"}
	ColorText        = lipgloss.AdaptiveColor{Light: "#1A1A1A", Dark: "#F8F8F2"}
	ColorSubtext     = lipgloss.AdaptiveColor{Light: "#555555", Dark: "#BFBFBF"}
	ColorMuted       = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#6272A4"}

	// Primary accent colors
	ColorPrimary   = lipgloss.AdaptiveColor{Light: "#6B47D9", Dark: "#BD93F9"}
	ColorSecondary = lipgloss.AdaptiveColor{Light: "#555555", Dark: "#6272A4"}
	ColorInfo      = lipgloss.AdaptiveColor{Light: "#006080", Dark: "#8BE9FD"}
	ColorSuccess   = lipgloss.AdaptiveColor{Light: "#007700", Dark: "#50FA7B"}
	ColorWarning   = lipgloss.AdaptiveColor{Light: "#B06800", Dark: "#FFB86C"}
	ColorDanger    = lipgloss.AdaptiveColor{Light: "#CC0000", Dark: "#FF5555"}

	// Status colors
	ColorStatusOpen       = lipgloss.AdaptiveColor{Light: "#007700", Dark: "#50FA7B"}
	ColorStatusInProgress = lipgloss.AdaptiveColor{Light: "#006080", Dark: "#8BE9FD"}
	ColorStatusBlocked    = lipgloss.AdaptiveColor{Light: "#CC0000", Dark: "#FF5555"}
	ColorStatusDeferred   = lipgloss.AdaptiveColor{Light: "#B06800", Dark: "#FFB86C"} // Orange - on ice
	ColorStatusPinned     = lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#6699FF"} // Blue - persistent
	ColorStatusHooked     = lipgloss.AdaptiveColor{Light: "#008080", Dark: "#00CED1"} // Teal - agent-attached
	ColorStatusReview     = lipgloss.AdaptiveColor{Light: "#6B47D9", Dark: "#BD93F9"} // Purple - awaiting review
	ColorStatusClosed     = lipgloss.AdaptiveColor{Light: "#555555", Dark: "#6272A4"}
	ColorStatusTombstone  = lipgloss.AdaptiveColor{Light: "#888888", Dark: "#44475A"} // Muted gray - deleted

	// Status background colors (for badges) - subtle backgrounds
	ColorStatusOpenBg       = lipgloss.AdaptiveColor{Light: "#D4EDDA", Dark: "#1A3D2A"}
	ColorStatusInProgressBg = lipgloss.AdaptiveColor{Light: "#D1ECF1", Dark: "#1A3344"}
	ColorStatusBlockedBg    = lipgloss.AdaptiveColor{Light: "#F8D7DA", Dark: "#3D1A1A"}
	ColorStatusDeferredBg   = lipgloss.AdaptiveColor{Light: "#FFE8CC", Dark: "#3D2A1A"} // Orange bg
	ColorStatusPinnedBg     = lipgloss.AdaptiveColor{Light: "#CCE5FF", Dark: "#1A2A44"} // Blue bg
	ColorStatusHookedBg     = lipgloss.AdaptiveColor{Light: "#CCFFFF", Dark: "#1A3D3D"} // Teal bg
	ColorStatusReviewBg     = lipgloss.AdaptiveColor{Light: "#E8DDFF", Dark: "#2A1A44"} // Purple bg
	ColorStatusClosedBg     = lipgloss.AdaptiveColor{Light: "#E2E3E5", Dark: "#2A2A3D"}
	ColorStatusTombstoneBg  = lipgloss.AdaptiveColor{Light: "#D0D0D0", Dark: "#1E1F29"} // Dark bg

	// Priority colors
	ColorPrioCritical = lipgloss.AdaptiveColor{Light: "#CC0000", Dark: "#FF5555"}
	ColorPrioHigh     = lipgloss.AdaptiveColor{Light: "#B06800", Dark: "#FFB86C"}
	ColorPrioMedium   = lipgloss.AdaptiveColor{Light: "#808000", Dark: "#F1FA8C"}
	ColorPrioLow      = lipgloss.AdaptiveColor{Light: "#007700", Dark: "#50FA7B"}

	// Priority background colors
	ColorPrioCriticalBg = lipgloss.AdaptiveColor{Light: "#F8D7DA", Dark: "#3D1A1A"}
	ColorPrioHighBg     = lipgloss.AdaptiveColor{Light: "#FFE8CC", Dark: "#3D2A1A"}
	ColorPrioMediumBg   = lipgloss.AdaptiveColor{Light: "#FFF3CD", Dark: "#3D3D1A"}
	ColorPrioLowBg      = lipgloss.AdaptiveColor{Light: "#D4EDDA", Dark: "#1A3D2A"}

	// Type colors
	ColorTypeBug     = lipgloss.AdaptiveColor{Light: "#CC0000", Dark: "#FF5555"}
	ColorTypeFeature = lipgloss.AdaptiveColor{Light: "#B06800", Dark: "#FFB86C"}
	ColorTypeTask    = lipgloss.AdaptiveColor{Light: "#808000", Dark: "#F1FA8C"}
	ColorTypeEpic    = lipgloss.AdaptiveColor{Light: "#6B47D9", Dark: "#BD93F9"}
	ColorTypeChore   = lipgloss.AdaptiveColor{Light: "#006080", Dark: "#8BE9FD"}

	// Footer colors - higher contrast for legibility across terminal themes (bv-128)
	// Uses brighter values than body text so the footer status bar is always readable.
	// On dark backgrounds: brighter text; on light backgrounds: darker text.
	ColorFooterHint = lipgloss.AdaptiveColor{Light: "#444444", Dark: "#C8C8D0"} // Navigation hints
	ColorFooterKey  = lipgloss.AdaptiveColor{Light: "#333333", Dark: "#E0E0E8"} // Key labels (bold)
	ColorFooterSep  = lipgloss.AdaptiveColor{Light: "#888888", Dark: "#8888A0"} // Separators between hints
	ColorFooterDim  = lipgloss.AdaptiveColor{Light: "#555555", Dark: "#A0A0B8"} // Issue count, secondary info
)

// ══════════════════════════════════════════════════════════════════════════════
// PANEL STYLES - For split view layouts
// ══════════════════════════════════════════════════════════════════════════════

var (
	// PanelStyle is the default style for unfocused panels
	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(paletteColor(ColorBgHighlight))

	// FocusedPanelStyle is the style for focused panels
	FocusedPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(paletteColor(ColorPrimary))
)

// ══════════════════════════════════════════════════════════════════════════════
// BADGE RENDERING - Polished, consistent badge styles
// ══════════════════════════════════════════════════════════════════════════════

// RenderPriorityBadge returns a styled priority badge
// Priority values: 0=Critical, 1=High, 2=Medium, 3=Low, 4=Backlog
func RenderPriorityBadge(priority int) string {
	var fg, bg lipgloss.AdaptiveColor
	var label string

	switch priority {
	case 0:
		fg, bg, label = ColorPrioCritical, ColorPrioCriticalBg, "P0"
	case 1:
		fg, bg, label = ColorPrioHigh, ColorPrioHighBg, "P1"
	case 2:
		fg, bg, label = ColorPrioMedium, ColorPrioMediumBg, "P2"
	case 3:
		fg, bg, label = ColorPrioLow, ColorPrioLowBg, "P3"
	case 4:
		fg, bg, label = ColorMuted, ColorBgSubtle, "P4"
	default:
		fg, bg, label = ColorMuted, ColorBgSubtle, "P?"
	}

	return lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Bold(true).
		Padding(0, 0).
		Render(label)
}

// RenderStatusBadge returns a styled status badge
func RenderStatusBadge(status string) string {
	var fg, bg lipgloss.AdaptiveColor
	var label string

	switch status {
	case "open":
		fg, bg, label = ColorStatusOpen, ColorStatusOpenBg, "OPEN"
	case "in_progress":
		fg, bg, label = ColorStatusInProgress, ColorStatusInProgressBg, "PROG"
	case "blocked":
		fg, bg, label = ColorStatusBlocked, ColorStatusBlockedBg, "BLKD"
	case "deferred":
		fg, bg, label = ColorStatusDeferred, ColorStatusDeferredBg, "DEFR"
	case "draft":
		fg, bg, label = ColorStatusDeferred, ColorStatusDeferredBg, "DRFT"
	case "pinned":
		fg, bg, label = ColorStatusPinned, ColorStatusPinnedBg, "PIN"
	case "hooked":
		fg, bg, label = ColorStatusHooked, ColorStatusHookedBg, "HOOK"
	case "review":
		fg, bg, label = ColorStatusReview, ColorStatusReviewBg, "REVW"
	case "closed":
		fg, bg, label = ColorStatusClosed, ColorStatusClosedBg, "DONE"
	case "tombstone":
		fg, bg, label = ColorStatusTombstone, ColorStatusTombstoneBg, "TOMB"
	default:
		fg, bg, label = ColorMuted, ColorBgSubtle, "????"
	}

	return lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Padding(0, 0).
		Render(label)
}

// ══════════════════════════════════════════════════════════════════════════════
// METRIC VISUALIZATION - Mini-bars and rank badges
// ══════════════════════════════════════════════════════════════════════════════

// RenderMiniBar renders a mini horizontal bar for a value between 0 and 1
func RenderMiniBar(value float64, width int, t Theme) string {
	if width <= 0 {
		return ""
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}

	filled := int(value * float64(width))
	if filled > width {
		filled = width
	}

	// Choose color based on value
	var barColor lipgloss.AdaptiveColor
	if value >= 0.75 {
		barColor = t.Open // Green/Success
	} else if value >= 0.5 {
		barColor = t.Feature // Orange/Warning
	} else if value >= 0.25 {
		barColor = t.InProgress // Cyan/Info
	} else {
		barColor = t.Secondary // Muted
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return t.Renderer.NewStyle().Foreground(barColor).Render(bar)
}

// RenderRankBadge renders a rank badge like "#1" with color based on percentile
func RenderRankBadge(rank, total int) string {
	if total == 0 {
		return lipgloss.NewStyle().Foreground(ColorMuted).Render("#?")
	}

	percentile := float64(rank) / float64(total)

	var color lipgloss.AdaptiveColor
	if percentile <= 0.1 {
		color = ColorSuccess // Top 10%
	} else if percentile <= 0.25 {
		color = ColorInfo // Top 25%
	} else if percentile <= 0.5 {
		color = ColorWarning // Top 50%
	} else {
		color = ColorMuted // Bottom 50%
	}

	return lipgloss.NewStyle().
		Foreground(color).
		Render(fmt.Sprintf("#%d", rank))
}

// ══════════════════════════════════════════════════════════════════════════════
// DIVIDERS AND SEPARATORS
// ══════════════════════════════════════════════════════════════════════════════

// RenderDivider renders a horizontal divider line
func RenderDivider(width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(ColorBgHighlight).
		Render(strings.Repeat("─", width))
}

// RenderSubtleDivider renders a more subtle divider using dots
func RenderSubtleDivider(width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(ColorMuted).
		Render(strings.Repeat("·", width))
}
