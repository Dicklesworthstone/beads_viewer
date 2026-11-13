package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AgentPromptResult represents the user's choice on the AGENTS.md prompt.
type AgentPromptResult int

const (
	AgentPromptPending AgentPromptResult = iota
	AgentPromptAccept
	AgentPromptDecline
	AgentPromptNeverAsk
)

// AgentPromptModal is a modal dialog for the AGENTS.md prompt.
type AgentPromptModal struct {
	selection       int    // 0=yes, 1=no, 2=never
	filePath        string // Which file we're offering to modify
	fileType        string // AGENTS.md or CLAUDE.md
	isUpgrade       bool   // True when offering to update an outdated blurb
	existingVersion int    // Existing blurb version (0 for legacy/unversioned)
	result          AgentPromptResult
	theme           Theme
	width           int
	height          int
}

// NewAgentPromptModal creates a new AGENTS.md prompt modal.
func NewAgentPromptModal(filePath, fileType string, theme Theme) AgentPromptModal {
	return AgentPromptModal{
		selection: 0, // Default to "Yes"
		filePath:  filePath,
		fileType:  fileType,
		result:    AgentPromptPending,
		theme:     theme,
		width:     60,
		height:    20,
	}
}

// NewAgentUpgradePromptModal creates an AGENTS.md prompt modal configured to
// offer an in-place upgrade of an outdated blurb version.
func NewAgentUpgradePromptModal(filePath, fileType string, existingVersion int, theme Theme) AgentPromptModal {
	m := NewAgentPromptModal(filePath, fileType, theme)
	m.isUpgrade = true
	m.existingVersion = existingVersion
	return m
}

// Update handles input for the modal.
func (m AgentPromptModal) Update(msg tea.Msg) (AgentPromptModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h", "shift+tab":
			m.selection--
			if m.selection < 0 {
				m.selection = 2
			}
		case "right", "l", "tab":
			m.selection++
			if m.selection > 2 {
				m.selection = 0
			}
		case "enter", " ":
			switch m.selection {
			case 0:
				m.result = AgentPromptAccept
			case 1:
				m.result = AgentPromptDecline
			case 2:
				m.result = AgentPromptNeverAsk
			}
		case "y", "Y":
			m.result = AgentPromptAccept
		case "n", "N":
			m.result = AgentPromptDecline
		case "d", "D":
			m.result = AgentPromptNeverAsk
		case "esc", "q":
			m.result = AgentPromptDecline
		}
	}
	return m, nil
}

// View renders the modal.
func (m AgentPromptModal) View() string {
	r := m.theme.Renderer

	// Modal container style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(1, 2).
		Width(m.width)

	// Title
	titleStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary).
		MarginBottom(1)

	// Body text
	bodyStyle := r.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#F8F8F2"})

	// Preview box
	previewBoxStyle := r.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.theme.Border).
		Padding(0, 1).
		Width(m.width - 8).
		MaxHeight(8)

	previewHeaderStyle := r.NewStyle().
		Foreground(m.theme.Subtext).
		Italic(true)

	// Buttons
	buttonBase := r.NewStyle().
		Padding(0, 2).
		MarginRight(1)

	selectedButton := buttonBase.
		Background(m.theme.Primary).
		Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#282A36"}).
		Bold(true)

	unselectedButton := buttonBase.
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.theme.Border)

	muteButton := buttonBase.
		Foreground(m.theme.Subtext)

	// Build content
	var b strings.Builder

	// Title & Body
	if m.isUpgrade {
		b.WriteString(titleStyle.Render("🔄 Update AI Agent Instructions?"))
		b.WriteString("\n\n")

		existingDesc := "a legacy unversioned format"
		if m.existingVersion > 0 {
			existingDesc = fmt.Sprintf("v%d", m.existingVersion)
		}
		b.WriteString(bodyStyle.Render(fmt.Sprintf("We found %s with outdated beads_viewer instructions (%s).", m.fileType, existingDesc)))
		b.WriteString("\n")
		b.WriteString(bodyStyle.Render(fmt.Sprintf("Would you like to update the instructions blurb to v%d?", agents.BlurbVersion)))
		b.WriteString("\n\n")
		b.WriteString(bodyStyle.Render("Updating ensures AI coding agents have the latest"))
		b.WriteString("\n")
		b.WriteString(bodyStyle.Render("robot flags and workflow guidance."))
		b.WriteString("\n\n")

		b.WriteString(previewHeaderStyle.Render(fmt.Sprintf("Preview of updated content (v%d):", agents.BlurbVersion)))
		b.WriteString("\n")
	} else {
		b.WriteString(titleStyle.Render("📝 Enhance AI Agent Integration?"))
		b.WriteString("\n\n")

		b.WriteString(bodyStyle.Render("We found " + m.fileType + " in this project but it"))
		b.WriteString("\n")
		b.WriteString(bodyStyle.Render("doesn't include beads_viewer instructions."))
		b.WriteString("\n\n")
		b.WriteString(bodyStyle.Render("Adding these helps AI coding agents understand"))
		b.WriteString("\n")
		b.WriteString(bodyStyle.Render("how to use your issue tracking workflow."))
		b.WriteString("\n\n")

		b.WriteString(previewHeaderStyle.Render("Preview of content to add:"))
		b.WriteString("\n")
	}

	preview := getBlurbPreview()
	b.WriteString(previewBoxStyle.Render(preview))
	b.WriteString("\n\n")

	// Buttons
	var buttons []string

	// Yes button
	yesLabel := "Yes, add it"
	if m.isUpgrade {
		yesLabel = "Yes, update it"
	}
	if m.selection == 0 {
		buttons = append(buttons, selectedButton.Render(yesLabel))
	} else {
		buttons = append(buttons, unselectedButton.Render(yesLabel))
	}

	// No button
	noLabel := "No thanks"
	if m.selection == 1 {
		buttons = append(buttons, selectedButton.Render(noLabel))
	} else {
		buttons = append(buttons, unselectedButton.Render(noLabel))
	}

	// Never button
	neverLabel := "Don't ask again"
	if m.selection == 2 {
		buttons = append(buttons, selectedButton.Render(neverLabel))
	} else {
		buttons = append(buttons, muteButton.Render(neverLabel))
	}

	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, buttons...))

	// Footer hint
	hintStyle := r.NewStyle().
		Foreground(m.theme.Subtext).
		Italic(true).
		MarginTop(1)
	b.WriteString("\n")
	b.WriteString(hintStyle.Render("← → to select • Enter to confirm • Esc to cancel"))

	return modalStyle.Render(b.String())
}

// Result returns the user's choice, or AgentPromptPending if still deciding.
func (m AgentPromptModal) Result() AgentPromptResult {
	return m.result
}

// FilePath returns the path of the file to modify.
func (m AgentPromptModal) FilePath() string {
	return m.filePath
}

// IsUpgrade returns true when the modal offers to update an outdated blurb.
func (m AgentPromptModal) IsUpgrade() bool {
	return m.isUpgrade
}

// ExistingVersion returns the detected outdated blurb version (0 for legacy).
func (m AgentPromptModal) ExistingVersion() int {
	return m.existingVersion
}

// SetSize sets the modal dimensions.
func (m *AgentPromptModal) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// getBlurbPreview returns a truncated preview of the blurb content.
func getBlurbPreview() string {
	// Get first few lines of the blurb content (skip marker)
	lines := strings.Split(agents.AgentBlurb, "\n")

	var preview []string
	lineCount := 0
	for _, line := range lines {
		// Skip marker lines
		if strings.HasPrefix(line, "<!--") {
			continue
		}
		// Skip empty lines at start
		if lineCount == 0 && strings.TrimSpace(line) == "" {
			continue
		}
		// Skip horizontal rules
		if strings.TrimSpace(line) == "---" {
			continue
		}
		preview = append(preview, line)
		lineCount++
		if lineCount >= 6 {
			break
		}
	}

	return strings.Join(preview, "\n") + "\n..."
}

// CenterModal returns the modal view centered in the given dimensions.
func (m AgentPromptModal) CenterModal(termWidth, termHeight int) string {
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
