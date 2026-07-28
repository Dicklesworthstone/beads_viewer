package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ContextHelpContent contains compact help content for each context.
// This is used when user triggers context-specific help (e.g., double-tap backtick).
// Content should fit on one screen (~20 lines) without scrolling.
var ContextHelpContent = map[Context]string{
	ContextList:           contextHelpList,
	ContextGraph:          contextHelpGraph,
	ContextBoard:          contextHelpBoard,
	ContextInsights:       contextHelpInsights,
	ContextHistory:        contextHelpHistory,
	ContextDetail:         contextHelpDetail,
	ContextSplit:          contextHelpSplit,
	ContextFilter:         contextHelpFilter,
	ContextLabelPicker:    contextHelpLabelPicker,
	ContextRecipePicker:   contextHelpRecipePicker,
	ContextHelp:           contextHelpHelp,
	ContextTimeTravel:     contextHelpTimeTravel,
	ContextLabelDashboard: contextHelpLabelDashboard,
	ContextAttention:      contextHelpAttention,
	ContextAgentPrompt:    contextHelpAgentPrompt,
	ContextCassSession:    contextHelpCassSession,
}

// GetContextHelp returns the help content for a given context.
// Falls back to generic help if the context has no specific content.
func GetContextHelp(ctx Context) string {
	if content, ok := ContextHelpContent[ctx]; ok {
		return content
	}
	return contextHelpGeneric
}

// RenderContextHelp renders the context-specific help modal.
// This is a compact modal (~60 chars wide) that shows quick reference info.
func RenderContextHelp(ctx Context, theme Theme, width, height int) string {
	content := GetContextHelp(ctx)

	r := theme.Renderer

	// Modal dimensions - compact
	modalWidth := 60
	if modalWidth > width-4 {
		modalWidth = width - 4
	}
	if modalWidth < 0 {
		modalWidth = 0
	}

	// Title
	titleStyle := r.NewStyle().
		Bold(true).
		Foreground(theme.Primary)

	// Content style
	contentStyle := r.NewStyle().
		Foreground(theme.Subtext)

	// Footer hint
	footerStyle := r.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)

	// Build content
	var b strings.Builder
	b.WriteString(titleStyle.Render("Quick Reference"))
	b.WriteString("\n")
	b.WriteString(r.NewStyle().Foreground(theme.Border).Render(strings.Repeat("─", max(modalWidth-4, 0))))
	b.WriteString("\n\n")
	b.WriteString(contentStyle.Render(content))
	b.WriteString("\n\n")
	b.WriteString(footerStyle.Render("Press ` for full tutorial │ Esc to close"))

	// Wrap in modal style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Secondary).
		Padding(1, 2).
		Width(modalWidth)

	return modalStyle.Render(b.String())
}

// =============================================================================
// CONTEXT-SPECIFIC HELP CONTENT (bv-4swd)
// =============================================================================

const contextHelpList = `## List View

**Navigation**
  j/k       Move up/down
  Enter     View issue details
  g/G       Jump to top/bottom

**Filtering**
  o         Open issues only
  c         Closed issues only
  r         Ready (no blockers)
  /         Fuzzy search
  Ctrl+S    Semantic search (AI)
  H         Hybrid ranking
  Alt+H     Hybrid preset

**Switch Views**
  a         Actionable view
  b         Board view
  g         Graph view
  i         Insights panel
  h         History view

**Actions**
  U         Self-update bv
  V         Preview cass sessions`

const contextHelpGraph = `## Graph View

**Navigation**
  j/k       Navigate nodes vertically
  h/l       Navigate siblings
  Enter     View selected issue
  f         Focus on subgraph
  Esc       Exit to list

**Understanding the Graph**
• Arrows point TO what's blocked
  (A → B means A blocks B)
• Node size = priority
• Color = status
  Green=closed, Blue=in_progress`

const contextHelpBoard = `## Board View

**Navigation**
  h/l       Move between columns
  j/k       Move within column
  1-4       Jump to column by number
  H/L       Jump to first/last column
  gg/G      Go to top/bottom of column

**Filtering**
  o/c/r     Filter: open/closed/ready

**Search**
  /         Start search
  n/N       Next/prev match

**Grouping**
  s         Cycle: Status/Priority/Type

**Visual Indicators** (card borders)
  🔴 Red     Has blockers
  🟡 Yellow  High-impact (blocks others)
  🟢 Green   Ready to work

**Actions**
  Tab       Toggle detail panel
  Ctrl+j/k  Scroll detail panel
  V         Preview cass sessions
  y         Copy issue ID
  Enter     View issue details
  Esc       Return to List view`

const contextHelpInsights = `## Insights Panel

**Navigation**
  h/l       Switch between panels
  j/k       Move within panel
  Ctrl+j/k  Scroll detail section
  Tab       Next panel

**Heatmap** (Priority × Depth grid)
  m         Toggle heatmap view
  Arrows    Navigate cells
  Enter     Drill into cell

**Details**
  e         Toggle explanations
  x         Toggle calculations

**Attention Indicators**
• Stale: Open too long
• Blocked chains: Bottlenecks
• Priority inversions: Low blocking high

  Enter     View selected issue
  Esc       Return to list`

const contextHelpHistory = `## History View

**Navigation**
  j/k       Navigate primary pane
  J/K       Navigate secondary pane
  Tab       Cycle focus (list→detail→files)
  Enter     Jump to selected bead

**View Modes**
  v         Toggle Bead/Git mode
  f         Toggle file tree panel
  /         Search commits/beads
  c         Cycle confidence filter

**Causality Markers**
  🎯 Direct   Commit mentions bead ID
  🔗 Temporal Within time window
  📁 File     Touches associated files

**Actions**
  y         Copy commit SHA
  o         Open commit in browser
  Esc       Return to list`

const contextHelpDetail = `## Detail View

**Navigation**
  j/k       Scroll content
  Esc       Return to list
  Tab       Switch to split view

**Actions**
  O         Open in editor
  y         Copy issue ID
  C         Copy full issue
  D         Delete issue (asks first)

**Info Shown**
• Full description (markdown)
• Dependencies
• Labels and metadata`

const contextHelpSplit = `## Split View

**Focus**
  Tab       Switch panes
  <         Shrink list pane
  >         Expand list pane

**Left Pane (List)**
  j/k       Navigate issues

**Right Pane (Detail)**
  j/k       Scroll content

**Exit**
  Esc       Return to list view
  Enter     Open full detail

Tip: Detail updates as you navigate`

const contextHelpFilter = `## Filter Mode

**Status Filters**
  o         Open only
  c         Closed only
  r         Ready (no blockers)
  a         All (clear filter)

**Search**
  /         Start fuzzy search
  Ctrl+S    Semantic search (AI)
  H         Hybrid ranking
  Alt+H     Hybrid preset
  n/N       Next/prev match
  Esc       Clear search

**Label Filters**
  l         Open label picker`

const contextHelpLabelPicker = `## Label Picker

**Navigation**
  j/k       Move selection
  Enter     Apply label
  Space     Toggle multi-select
  Esc       Cancel

**Search**
  /         Filter labels

**Actions**
  n         Create new label
  d         Delete label
  e         Edit label`

const contextHelpRecipePicker = `## Recipe Picker

**Navigation**
  j/k       Move selection
  Enter     Apply recipe
  Esc       Cancel

**Recipes**
Pre-configured filters and sorts:
• Sprint Ready
• Blocked Items
• By Priority
• Recently Updated`

const contextHelpHelp = `## Help Overlay

You're looking at the help overlay!

**Navigation**
  j/k       Scroll help content
  Space     Open full tutorial
  Esc/?     Close this overlay

**Other Help**
  ` + "`" + `         Full tutorial (any time)
  ;         Toggle shortcuts sidebar`

const contextHelpTimeTravel = `## Time Travel Mode

**Currently Viewing**: Past state

This is read-only - you're viewing
how the project looked at a specific
point in history.

**Navigation**
  j/k       Navigate issues
  Enter     View issue detail

**Exit**
  Esc       Return to present

Tip: Use History view (h) to pick
different points in time`

const contextHelpLabelDashboard = `## Label Dashboard

**Overview**
Shows all labels with:
• Issue counts per label
• Health indicators
• Usage trends

**Navigation**
  j/k       Move selection
  Enter     Drill into label
  h         View label health
  g         Label graph analysis
  Esc       Return to list

**Filtering**
  /         Search labels`

const contextHelpAttention = `## Attention View

**Issues Needing Attention**

Sorted by attention score based on:
• Age (older = more attention)
• Priority mismatches
• Blocking factor
• Stale status

**Navigation**
  j/k       Move selection
  Enter     View issue
  s         Change status

Press 1 to return to List view`

const contextHelpAgentPrompt = `## AI Agent Prompt

**Input**
Type your question or request
for the AI agent.

**Actions**
  Enter     Submit prompt
  Esc       Cancel
  Ctrl+C    Clear input

**Examples**
• "Triage these issues"
• "What should I work on?"
• "Summarize blocked items"`

const contextHelpGeneric = `## Quick Reference

**Global Keys**
  ?         Help overlay
  ` + "`" + `         Full tutorial
  Esc       Close/back
  q         Quit

**Navigation**
  j/k       Move up/down
  h/l       Move left/right
  Enter     Select/open

**Views**
  b/g/i/h   Switch views
  ;         Shortcuts sidebar`

const contextHelpCassSession = `## Cass Session Preview

Shows coding sessions correlated with
the selected bead via cass search.

**Navigation**
  j/k       Move between sessions
  Enter     Expand session details
  Esc       Close modal

**Actions**
  y         Copy cass command
  o         Open session file

**Match Types**
  ID        Direct bead ID match
  File      Modified same files
  Title     Keyword similarity

Sessions ranked by relevance score.
Only shown when cass is installed.`
