package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const clipboardCopyTimeout = 2 * time.Second

type cassClipboardCopyMsg struct {
	modalToken *byte
	err        error
}

type clipboardStatusMsg struct {
	requestID uint64
	success   string
	err       error
}

// CassSessionModal displays correlated cass sessions for a bead.
// It shows session previews with agent name, timestamp, match reason, and snippet.
type CassSessionModal struct {
	beadID     string              // The bead this modal is showing sessions for
	sessions   []cass.ScoredResult // Correlated sessions to display
	strategy   cass.CorrelationStrategy
	keywords   []string // Keywords used for correlation (for display)
	selected   int      // Currently selected session (for keyboard nav)
	searchCmd  string   // Command to run for more results
	theme      Theme
	width      int
	height     int
	copied     bool      // Flash feedback for clipboard copy
	copiedAt   time.Time // When copy happened
	copyToken  *byte     // Reject late results from a dismissed modal instance (non-zero-size so each allocation is unique)
	maxDisplay int       // Max sessions to show (rest are summarized)
}

// NewCassSessionModal creates a modal from correlation results.
func NewCassSessionModal(beadID string, result cass.CorrelationResult, theme Theme) CassSessionModal {
	searchCmd := fmt.Sprintf("cass search %q", beadID)
	if len(result.Keywords) > 0 {
		searchCmd = fmt.Sprintf("cass search %q", strings.Join(result.Keywords, " "))
	}

	return CassSessionModal{
		beadID:     beadID,
		sessions:   result.TopSessions,
		strategy:   result.Strategy,
		keywords:   result.Keywords,
		selected:   0,
		searchCmd:  searchCmd,
		theme:      theme,
		width:      70,
		height:     25,
		copyToken:  new(byte),
		maxDisplay: 3,
	}
}

// Update handles input for the modal.
func (m CassSessionModal) Update(msg tea.Msg) (CassSessionModal, tea.Cmd) {
	// Calculate the number of sessions actually displayed (capped by maxDisplay)
	displayCount := len(m.sessions)
	if displayCount > m.maxDisplay {
		displayCount = m.maxDisplay
	}

	switch msg := msg.(type) {
	case cassClipboardCopyMsg:
		if msg.modalToken == m.copyToken && msg.err == nil {
			m.copied = true
			m.copiedAt = time.Now()
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if displayCount > 1 && m.selected < displayCount-1 {
				m.selected++
			}
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "y":
			// Clipboard helpers are external processes and may stall. Bubble Tea
			// runs commands off the Update loop, preserving UI responsiveness.
			return m, copyToClipboardCmd(m.searchCmd, m.copyToken)
		}
	}
	return m, nil
}

// View renders the modal.
func (m CassSessionModal) View() string {
	r := m.theme.Renderer

	// Check if copy flash should be shown (within 2 seconds of copy)
	showCopied := m.copied && time.Since(m.copiedAt) <= 2*time.Second

	// Modal container style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(1, 2).
		Width(m.width)

	// Header style
	headerStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	beadIDStyle := r.NewStyle().
		Foreground(m.theme.Subtext)

	// Session card styles
	sessionHeaderStyle := r.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#F8F8F2"})

	selectedSessionStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	matchReasonStyle := r.NewStyle().
		Foreground(m.theme.Subtext).
		Italic(true)

	snippetBoxStyle := r.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.theme.Border).
		Padding(0, 1).
		Width(m.width - 10)

	footerStyle := r.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)

	// Build content
	var b strings.Builder

	// Header
	b.WriteString(headerStyle.Render("📎 Related Coding Sessions"))
	b.WriteString("  ")
	b.WriteString(beadIDStyle.Render(m.beadID))
	b.WriteString("\n\n")

	// Sessions
	if len(m.sessions) == 0 {
		b.WriteString(matchReasonStyle.Render("No correlated sessions found."))
		b.WriteString("\n\n")
	} else {
		displayCount := len(m.sessions)
		if displayCount > m.maxDisplay {
			displayCount = m.maxDisplay
		}

		for i := 0; i < displayCount; i++ {
			session := m.sessions[i]

			// Session number with selection indicator
			numPrefix := fmt.Sprintf("[%d] ", i+1)
			if i == m.selected {
				b.WriteString(selectedSessionStyle.Render(numPrefix))
			} else {
				b.WriteString(sessionHeaderStyle.Render(numPrefix))
			}

			// Agent and timestamp
			agentStr := session.Agent
			if agentStr == "" {
				agentStr = "Unknown"
			}
			timeStr := formatRelativeTime(session.Timestamp)

			sessionInfo := fmt.Sprintf("%s • %s", agentStr, timeStr)
			if i == m.selected {
				b.WriteString(selectedSessionStyle.Render(sessionInfo))
			} else {
				b.WriteString(sessionHeaderStyle.Render(sessionInfo))
			}
			b.WriteString("\n")

			// Match reason
			matchReason := m.formatMatchReason(session)
			b.WriteString("    ")
			b.WriteString(matchReasonStyle.Render(matchReason))
			b.WriteString("\n")

			// Snippet box
			snippet := m.formatSnippet(session.Snippet)
			b.WriteString(snippetBoxStyle.Render(snippet))
			b.WriteString("\n\n")
		}

		// Show count of additional sessions
		if len(m.sessions) > m.maxDisplay {
			extra := len(m.sessions) - m.maxDisplay
			moreText := fmt.Sprintf("(%d more session", extra)
			if extra > 1 {
				moreText += "s"
			}
			moreText += fmt.Sprintf(" - run: %s)", m.searchCmd)
			b.WriteString(matchReasonStyle.Render(moreText))
			b.WriteString("\n\n")
		}
	}

	// Footer with keybindings
	footerText := "[j/k] Navigate    [y] Copy search cmd    [V/Esc] Close"
	if showCopied {
		footerText = "[j/k] Navigate    ✓ Copied!              [V/Esc] Close"
	}
	b.WriteString(footerStyle.Render(footerText))

	return modalStyle.Render(b.String())
}

// formatMatchReason creates a human-readable match reason string.
func (m CassSessionModal) formatMatchReason(session cass.ScoredResult) string {
	switch session.Strategy {
	case cass.StrategyIDMention:
		return fmt.Sprintf("Matched via: bead ID mentioned (%s)", m.beadID)
	case cass.StrategyKeywords:
		if len(session.Keywords) > 0 {
			return fmt.Sprintf("Matched via: keywords %q", strings.Join(session.Keywords, ", "))
		}
		return "Matched via: keyword search"
	case cass.StrategyTimestamp:
		return "Matched via: recent activity timeframe"
	case cass.StrategyCombined:
		return "Matched via: multiple signals"
	default:
		return fmt.Sprintf("Matched via: %s", session.Strategy)
	}
}

// formatSnippet cleans and truncates a snippet for display.
func (m CassSessionModal) formatSnippet(snippet string) string {
	if snippet == "" {
		return "(no preview available)"
	}

	// Clean up the snippet
	lines := strings.Split(snippet, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Truncate long lines (UTF-8 safe)
		maxLineLen := m.width - 14 // Account for box padding
		line = truncateRunesHelper(line, maxLineLen, "...")
		cleaned = append(cleaned, line)
		if len(cleaned) >= 3 {
			break
		}
	}

	if len(cleaned) == 0 {
		return "(no preview available)"
	}
	return strings.Join(cleaned, "\n")
}

// formatRelativeTime formats a timestamp as a relative time string.
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}

	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 48*time.Hour:
		return "yesterday"
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		return fmt.Sprintf("%d days ago", days)
	case diff < 30*24*time.Hour:
		weeks := int(diff.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		return t.Format("Jan 2, 2006")
	}
}

// SetSize sets the modal dimensions based on terminal size.
func (m *CassSessionModal) SetSize(width, height int) {
	// Constrain width
	maxWidth := width - 10
	if maxWidth < 50 {
		maxWidth = 50
	}
	if maxWidth > 80 {
		maxWidth = 80
	}
	m.width = maxWidth
	m.height = height
}

// HasSessions returns true if there are sessions to display.
func (m CassSessionModal) HasSessions() bool {
	return len(m.sessions) > 0
}

// CenterModal returns the modal view centered in the given dimensions.
func (m CassSessionModal) CenterModal(termWidth, termHeight int) string {
	modal := m.View()

	// Get actual rendered dimensions
	modalWidth := lipgloss.Width(modal)
	modalHeight := lipgloss.Height(modal)

	// Calculate padding
	padTop := (termHeight - modalHeight) / 2
	padLeft := (termWidth - modalWidth) / 2

	if padTop < 0 {
		padTop = 0
	}
	if padLeft < 0 {
		padLeft = 0
	}

	r := m.theme.Renderer

	// Create centered version
	centered := r.NewStyle().
		MarginTop(padTop).
		MarginLeft(padLeft).
		Render(modal)

	return centered
}

func copyToClipboardCmd(text string, modalToken *byte) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardCopyTimeout)
		defer cancel()

		return cassClipboardCopyMsg{
			modalToken: modalToken,
			err:        copyToClipboard(ctx, text),
		}
	}
}

func copyToClipboardStatusCmd(text, success string, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardCopyTimeout)
		defer cancel()

		return clipboardStatusMsg{
			requestID: requestID,
			success:   success,
			err:       copyToClipboard(ctx, text),
		}
	}
}

// copyToClipboard copies text to the system clipboard with a bounded helper
// lifetime. It uses platform-specific commands and reports failures to its
// Bubble Tea command caller.
func copyToClipboard(ctx context.Context, text string) error {
	cmd, err := clipboardCommand(ctx)
	if err != nil {
		// No helper can run here — a display-less SSH session is the usual
		// reason. Ask the terminal itself to set the clipboard (#201).
		if osc52Err := osc52Copy(text); osc52Err != nil {
			return fmt.Errorf("%w (OSC 52 fallback: %w)", err, osc52Err)
		}
		return nil
	}

	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("clipboard helper timed out: %w", ctxErr)
		}
		// The helper looked usable and still failed. The copy landing matters
		// more than which mechanism landed it, so try the terminal before
		// reporting nothing happened.
		if osc52Err := osc52Copy(text); osc52Err == nil {
			return nil
		}
		return fmt.Errorf("run clipboard helper: %w", err)
	}
	return nil
}

// osc52Limit bounds the base64 payload of an OSC 52 write. Terminals and
// multiplexers cap how much they will accept and several drop an oversized
// sequence outright rather than truncating it, so a copy that cannot land is
// reported as failed instead of sent and silently lost.
const osc52Limit = 74994

// osc52Sequence builds the OSC 52 escape sequence for text, wrapped for the
// multiplexer in use.
func osc52Sequence(text string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if len(encoded) > osc52Limit {
		return "", fmt.Errorf("selection too large for terminal clipboard (%d bytes encoded, limit %d)",
			len(encoded), osc52Limit)
	}
	sequence := "\x1b]52;c;" + encoded + "\x07"

	// tmux implements OSC 52 itself: it sets its own paste buffer and forwards
	// the sequence to the outer terminal. Wrapping it in a `\ePtmux;` DCS
	// passthrough instead tells tmux *not* to interpret it, which additionally
	// requires `allow-passthrough`; verified against a live tmux, the wrapped
	// form leaves the clipboard untouched while the raw form sets it. So tmux
	// gets it raw.
	//
	// GNU screen does not implement OSC 52, so there the DCS passthrough is the
	// only way to reach the outer terminal. tmux sets TERM to screen-* as well,
	// hence the explicit $TMUX test first.
	if os.Getenv("TMUX") == "" && strings.HasPrefix(os.Getenv("TERM"), "screen") {
		sequence = "\x1bP" + sequence + "\x1b\\"
	}
	return sequence, nil
}

// osc52Copy asks the terminal to set the system clipboard with OSC 52.
//
// This is the only mechanism that reaches the clipboard of the machine the user
// is sitting at: the escape sequence travels back over SSH and is executed by
// the local terminal emulator. It is written to the controlling terminal rather
// than stdout so it does not pass through Bubble Tea's frame buffer, and the
// sequence is emitted in a single write so a concurrent repaint cannot split it.
func osc52Copy(text string) error {
	sequence, err := osc52Sequence(text)
	if err != nil {
		return err
	}

	// The controlling terminal is the right sink even when stdout is a pipe.
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		if _, writeErr := tty.WriteString(sequence); writeErr != nil {
			return fmt.Errorf("write OSC 52 to terminal: %w", writeErr)
		}
		return nil
	}
	if _, err := os.Stdout.WriteString(sequence); err != nil {
		return fmt.Errorf("write OSC 52 to stdout: %w", err)
	}
	return nil
}

func clipboardCommand(ctx context.Context) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "pbcopy"), nil
	case "windows":
		return exec.CommandContext(ctx, "clip.exe"), nil
	case "linux", "freebsd", "netbsd", "openbsd", "dragonfly", "solaris":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if _, err := exec.LookPath("wl-copy"); err == nil {
				return exec.CommandContext(ctx, "wl-copy"), nil
			}
		}
		// xclip and xsel are X clients: without a display they exit 1 with
		// "Can't open display", which is the common case on a server you SSH
		// into. Being on PATH is not evidence they can run (#201), so gate them
		// the way wl-copy is already gated and let the caller fall back to
		// OSC 52, which reaches the clipboard at the *local* end of the session.
		if os.Getenv("DISPLAY") != "" {
			if _, err := exec.LookPath("xclip"); err == nil {
				return exec.CommandContext(ctx, "xclip", "-in", "-selection", "clipboard"), nil
			}
			if _, err := exec.LookPath("xsel"); err == nil {
				return exec.CommandContext(ctx, "xsel", "--input", "--clipboard"), nil
			}
		}
		if _, err := exec.LookPath("termux-clipboard-set"); err == nil {
			return exec.CommandContext(ctx, "termux-clipboard-set"), nil
		}
		if _, err := exec.LookPath("clip.exe"); err == nil {
			return exec.CommandContext(ctx, "clip.exe"), nil
		}
		return nil, fmt.Errorf("no clipboard utility found")
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
