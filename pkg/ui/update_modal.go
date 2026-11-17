package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// UpdateState represents the current state of the update process
type UpdateState int

const (
	UpdateStateIdle UpdateState = iota
	UpdateStateConfirm
	UpdateStateDownloading
	UpdateStateVerifying
	UpdateStateInstalling
	UpdateStateSuccess
	UpdateStateError
	UpdateStateRollingBack
)

var (
	canRollbackCheck = updater.CanRollback
	rollbackRunner   = func() error {
		return updater.PerformRollback(io.Discard)
	}
)

// RollbackCompleteMsg is sent when a rollback finishes.
type RollbackCompleteMsg struct {
	Success bool
	Message string
}

type updatePhaseTracker struct {
	mu         sync.Mutex
	stage      string
	detail     string
	downloaded int64
	total      int64
}

func (t *updatePhaseTracker) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line == "" {
		return len(p), nil
	}
	// Take the last non-empty line if multiple lines arrived together
	if idx := strings.LastIndex(line, "\n"); idx >= 0 {
		line = strings.TrimSpace(line[idx+1:])
	}
	lower := strings.ToLower(line)
	stage := ""
	switch {
	case strings.Contains(lower, "download"):
		stage = "downloading"
	case strings.Contains(lower, "verif") || strings.Contains(lower, "checksum"):
		stage = "verifying"
	case strings.Contains(lower, "extract") || strings.Contains(lower, "backing up") || strings.Contains(lower, "install"):
		stage = "installing"
	}

	t.mu.Lock()
	if stage != "" {
		t.stage = stage
	}
	t.detail = line
	t.mu.Unlock()
	return len(p), nil
}

func (t *updatePhaseTracker) ReportBytes(downloaded, total int64) {
	t.mu.Lock()
	t.downloaded = downloaded
	t.total = total
	if t.stage == "" {
		t.stage = "downloading"
	}
	t.mu.Unlock()
}

func (t *updatePhaseTracker) snapshot() (stage, detail string, downloaded, total int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stage, t.detail, t.downloaded, t.total
}

// UpdateProgress represents progress during download
type UpdateProgress struct {
	BytesDownloaded int64
	TotalBytes      int64
	Stage           string
}

// UpdateProgressMsg is sent during the update process
type UpdateProgressMsg struct {
	Progress UpdateProgress
}

// UpdateCompleteMsg is sent when the update completes
type UpdateCompleteMsg struct {
	Success     bool
	Message     string
	NewVersion  string
	BackupPath  string
	RequireRoot bool
}

type updateTickMsg struct {
	startedAt time.Time
}

func updateTickCmd(startedAt time.Time) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return updateTickMsg{startedAt: startedAt}
	})
}

// UpdateModal displays the update confirmation and progress.
type UpdateModal struct {
	currentVersion    string
	newVersion        string
	releaseURL        string
	state             UpdateState
	progress          UpdateProgress
	phaseDetail       string
	phaseTracker      *updatePhaseTracker
	errorMessage      string
	successMessage    string
	backupPath        string
	canRollback       bool
	lastOpWasRollback bool
	theme             Theme
	width             int
	termWidth         int
	height            int
	startTime         time.Time
	confirmFocus      int // 0 = Update, 1 = Cancel, 2 = Rollback (when available)
}

// NewUpdateModal creates a new update modal.
func NewUpdateModal(newVersion, releaseURL string, theme Theme) UpdateModal {
	canRollback := canRollbackCheck()
	initialFocus := 0 // Default to "Update" button
	if strings.TrimSpace(newVersion) == "" && canRollback {
		initialFocus = 2 // Focus "Rollback" when opened without a pending update
	}
	return UpdateModal{
		currentVersion: version.Version,
		newVersion:     newVersion,
		releaseURL:     releaseURL,
		state:          UpdateStateConfirm,
		canRollback:    canRollback,
		theme:          theme,
		width:          60,
		height:         20,
		confirmFocus:   initialFocus,
	}
}

// SetCanRollback sets whether a backup binary is available for rollback.
func (m *UpdateModal) SetCanRollback(can bool) {
	m.canRollback = can
	if !can && m.confirmFocus > 1 {
		m.confirmFocus = 0
	} else if can && strings.TrimSpace(m.newVersion) == "" && m.confirmFocus == 0 {
		m.confirmFocus = 2
	}
}

// CanRollback reports whether rollback is currently available in the modal.
func (m UpdateModal) CanRollback() bool {
	return m.canRollback || m.backupPath != ""
}

func validateConfirmedRelease(release *updater.Release, expectedVersion string) error {
	if release == nil {
		return fmt.Errorf("release metadata is nil")
	}
	if strings.TrimSpace(release.TagName) != strings.TrimSpace(expectedVersion) {
		return fmt.Errorf("latest release changed from %s to %s; reopen the update dialog to review it", expectedVersion, release.TagName)
	}
	return nil
}

// PerformUpdateCmd returns a command that performs the update in the background.
func PerformUpdateCmd(expectedVersion string) tea.Cmd {
	return performUpdateWithProgressCmd(expectedVersion, io.Discard)
}

func performUpdateWithProgressCmd(expectedVersion string, progress io.Writer) tea.Cmd {
	if progress == nil {
		progress = io.Discard
	}
	return func() tea.Msg {
		release, err := updater.GetLatestRelease()
		if err != nil {
			return UpdateCompleteMsg{
				Success: false,
				Message: fmt.Sprintf("Failed to fetch release info: %v", err),
			}
		}
		if err := validateConfirmedRelease(release, expectedVersion); err != nil {
			return UpdateCompleteMsg{
				Success: false,
				Message: err.Error(),
			}
		}

		result, err := updater.PerformUpdate(release, progress)
		if err != nil {
			msg := fmt.Sprintf("Update failed: %v", err)
			requireRoot := false
			if result != nil {
				if result.RequireRoot {
					requireRoot = true
					msg = "Update requires elevated permissions. Run: sudo bv --update"
				}
			}
			return UpdateCompleteMsg{
				Success:     false,
				Message:     msg,
				RequireRoot: requireRoot,
			}
		}

		return UpdateCompleteMsg{
			Success:    result.Success,
			Message:    result.Message,
			NewVersion: result.NewVersion,
			BackupPath: result.BackupPath,
		}
	}
}

// PerformRollbackCmd returns a command that restores the backup binary in the background.
func PerformRollbackCmd() tea.Cmd {
	return func() tea.Msg {
		if err := rollbackRunner(); err != nil {
			return RollbackCompleteMsg{
				Success: false,
				Message: fmt.Sprintf("Rollback failed: %v", err),
			}
		}
		return RollbackCompleteMsg{
			Success: true,
			Message: "Rolled back to previous binary backup successfully",
		}
	}
}

func (m UpdateModal) startUpdate() (UpdateModal, tea.Cmd) {
	tracker := &updatePhaseTracker{stage: "downloading"}
	m.phaseTracker = tracker
	m.phaseDetail = ""
	m.state = UpdateStateDownloading
	m.startTime = time.Now()
	return m, tea.Batch(performUpdateWithProgressCmd(m.newVersion, tracker), updateTickCmd(m.startTime))
}

func (m UpdateModal) startRollback() (UpdateModal, tea.Cmd) {
	m.phaseTracker = nil
	m.phaseDetail = "Validating & restoring backup..."
	m.state = UpdateStateRollingBack
	m.startTime = time.Now()
	return m, tea.Batch(PerformRollbackCmd(), updateTickCmd(m.startTime))
}

// Update handles input for the modal.
func (m UpdateModal) Update(msg tea.Msg) (UpdateModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.state {
		case UpdateStateConfirm:
			hasUpdate := strings.TrimSpace(m.newVersion) != ""
			minFocus := 0
			if !hasUpdate {
				minFocus = 1
			}
			buttonCount := 2
			if m.CanRollback() {
				buttonCount = 3
			}
			switch msg.String() {
			case "left", "h":
				if m.confirmFocus > minFocus {
					m.confirmFocus--
				}
			case "right", "l":
				if m.confirmFocus < buttonCount-1 {
					m.confirmFocus++
				}
			case "tab":
				if !hasUpdate {
					if m.CanRollback() {
						if m.confirmFocus == 1 {
							m.confirmFocus = 2
						} else {
							m.confirmFocus = 1
						}
					} else {
						m.confirmFocus = 1
					}
				} else {
					m.confirmFocus = (m.confirmFocus + 1) % buttonCount
				}
			case "shift+tab":
				if !hasUpdate {
					if m.CanRollback() {
						if m.confirmFocus == 1 {
							m.confirmFocus = 2
						} else {
							m.confirmFocus = 1
						}
					} else {
						m.confirmFocus = 1
					}
				} else {
					m.confirmFocus = (m.confirmFocus + buttonCount - 1) % buttonCount
				}
			case "enter":
				if m.confirmFocus == 0 && hasUpdate {
					// User confirmed update
					return m.startUpdate()
				}
				if m.confirmFocus == 2 && m.CanRollback() {
					return m.startRollback()
				}
				// Cancel - will be handled by parent
				return m, nil
			case "y", "Y":
				if hasUpdate {
					// Quick confirm
					return m.startUpdate()
				}
			case "r", "R":
				if m.CanRollback() {
					return m.startRollback()
				}
			case "n", "N":
				// Quick cancel - will be handled by parent
				return m, nil
			}

		case UpdateStateSuccess, UpdateStateError:
			switch msg.String() {
			case "r", "R":
				if m.CanRollback() {
					return m.startRollback()
				}
			case "enter", "esc", "q":
				// Will be handled by parent to close modal
				return m, nil
			}
		}

	case updateTickMsg:
		if m.IsInProgress() && msg.startedAt == m.startTime {
			if m.phaseTracker != nil {
				stage, detail, downloaded, total := m.phaseTracker.snapshot()
				m.phaseDetail = detail
				m.progress.BytesDownloaded = downloaded
				m.progress.TotalBytes = total
				if stage != "" {
					m.progress.Stage = stage
				}
				switch stage {
				case "downloading":
					m.state = UpdateStateDownloading
				case "verifying":
					m.state = UpdateStateVerifying
				case "installing":
					m.state = UpdateStateInstalling
				}
			}
			return m, updateTickCmd(m.startTime)
		}

	case UpdateProgressMsg:
		m.progress = msg.Progress
		switch m.progress.Stage {
		case "downloading":
			m.state = UpdateStateDownloading
		case "verifying":
			m.state = UpdateStateVerifying
		case "installing":
			m.state = UpdateStateInstalling
		}

	case UpdateCompleteMsg:
		m.phaseTracker = nil
		m.lastOpWasRollback = false
		if msg.Success {
			m.state = UpdateStateSuccess
			m.successMessage = msg.Message
			m.backupPath = msg.BackupPath
			if msg.BackupPath != "" {
				m.canRollback = true
			}
		} else {
			m.state = UpdateStateError
			m.errorMessage = msg.Message
		}

	case RollbackCompleteMsg:
		m.phaseTracker = nil
		m.lastOpWasRollback = true
		if msg.Success {
			m.state = UpdateStateSuccess
			m.successMessage = msg.Message
			m.backupPath = ""
			m.canRollback = false
		} else {
			m.state = UpdateStateError
			m.errorMessage = msg.Message
		}
	}

	return m, nil
}

func (m UpdateModal) effectiveWidth() int {
	modalWidth := m.width
	if modalWidth <= 0 {
		modalWidth = 60
	}
	if m.termWidth > 0 {
		modalWidth = min(modalWidth, max(24, m.termWidth-4))
	}
	return modalWidth
}

// View renders the modal.
func (m UpdateModal) View() string {
	r := m.theme.Renderer
	modalWidth := m.effectiveWidth()

	// Modal container style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(1, 2).
		Width(modalWidth)

	// Header style
	headerStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	// Version styles
	currentVersionStyle := r.NewStyle().
		Foreground(m.theme.Subtext)

	newVersionStyle := r.NewStyle().
		Bold(true).
		Foreground(ColorStatusOpen)

	// Button styles
	buttonStyle := r.NewStyle().
		Padding(0, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Border)

	selectedButtonStyle := r.NewStyle().
		Padding(0, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Background(m.theme.Primary).
		Foreground(ColorBg).
		Bold(true)

	successStyle := r.NewStyle().
		Foreground(ColorStatusOpen).
		Bold(true)

	errorStyle := r.NewStyle().
		Foreground(ColorStatusBlocked).
		Bold(true)

	subtextStyle := r.NewStyle().
		Foreground(m.theme.Subtext).
		Italic(true)

	var b strings.Builder

	switch m.state {
	case UpdateStateConfirm:
		hasUpdate := strings.TrimSpace(m.newVersion) != ""
		if hasUpdate {
			b.WriteString(headerStyle.Render("Update Available"))
		} else {
			b.WriteString(headerStyle.Render("Rollback Binary"))
		}
		b.WriteString("\n\n")

		b.WriteString("Current version: ")
		b.WriteString(currentVersionStyle.Render(m.currentVersion))
		b.WriteString("\n")

		if hasUpdate {
			b.WriteString("New version:     ")
			b.WriteString(newVersionStyle.Render(m.newVersion))
			b.WriteString("\n\n")
			b.WriteString("Would you like to update now?\n\n")
		} else {
			b.WriteString("\nA previous binary backup is available to restore.\n\n")
		}

		// Buttons
		var cancelBtn string
		if m.confirmFocus == 1 {
			cancelBtn = selectedButtonStyle.Render(" Cancel ")
		} else {
			cancelBtn = buttonStyle.Render(" Cancel ")
		}
		if hasUpdate {
			var updateBtn string
			if m.confirmFocus == 0 {
				updateBtn = selectedButtonStyle.Render(" Update ")
			} else {
				updateBtn = buttonStyle.Render(" Update ")
			}
			b.WriteString("  ")
			b.WriteString(updateBtn)
		}
		b.WriteString("  ")
		b.WriteString(cancelBtn)
		if m.CanRollback() {
			var rollbackBtn string
			if m.confirmFocus == 2 {
				rollbackBtn = selectedButtonStyle.Render(" Rollback ")
			} else {
				rollbackBtn = buttonStyle.Render(" Rollback ")
			}
			b.WriteString("  ")
			b.WriteString(rollbackBtn)
		}
		b.WriteString("\n\n")

		if hasUpdate && m.CanRollback() {
			b.WriteString(subtextStyle.Render("[Y] Update   [R] Rollback   [N] Cancel   [Enter] Select"))
		} else if hasUpdate {
			b.WriteString(subtextStyle.Render("[Y] Update   [N] Cancel   [Enter] Select"))
		} else if m.CanRollback() {
			b.WriteString(subtextStyle.Render("[R] Rollback   [N] Cancel   [Enter] Select"))
		} else {
			b.WriteString(subtextStyle.Render("[N] Cancel   [Enter] Select"))
		}

	case UpdateStateDownloading:
		b.WriteString(headerStyle.Render("Updating..."))
		b.WriteString("\n\n")
		b.WriteString(m.renderSpinner())
		b.WriteString(" Applying ")
		b.WriteString(newVersionStyle.Render(m.newVersion))
		b.WriteString("...\n\n")
		b.WriteString(m.renderPhaseSteps())
		b.WriteString("\n")
		if m.progress.TotalBytes > 0 {
			b.WriteString(m.renderProgressBar())
			b.WriteString("\n\n")
		}
		elapsed := time.Since(m.startTime).Round(time.Second)
		b.WriteString(subtextStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed)))

	case UpdateStateVerifying:
		b.WriteString(headerStyle.Render("Updating..."))
		b.WriteString("\n\n")
		b.WriteString(m.renderSpinner())
		b.WriteString(" Verifying checksum...\n\n")
		b.WriteString(m.renderPhaseSteps())
		b.WriteString("\n")
		elapsed := time.Since(m.startTime).Round(time.Second)
		b.WriteString(subtextStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed)))

	case UpdateStateInstalling:
		b.WriteString(headerStyle.Render("Updating..."))
		b.WriteString("\n\n")
		b.WriteString(m.renderSpinner())
		b.WriteString(" Installing new version...\n\n")
		b.WriteString(m.renderPhaseSteps())
		b.WriteString("\n")
		elapsed := time.Since(m.startTime).Round(time.Second)
		b.WriteString(subtextStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed)))

	case UpdateStateRollingBack:
		b.WriteString(headerStyle.Render("Rolling Back..."))
		b.WriteString("\n\n")
		b.WriteString(m.renderSpinner())
		b.WriteString(" Restoring previous version from backup...\n\n")
		b.WriteString(subtextStyle.Render("Phase: Validating & restoring backup"))
		b.WriteString("\n\n")
		elapsed := time.Since(m.startTime).Round(time.Second)
		b.WriteString(subtextStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed)))

	case UpdateStateSuccess:
		isRollback := m.lastOpWasRollback || strings.HasPrefix(m.successMessage, "Rolled back")
		if isRollback {
			b.WriteString(successStyle.Render("Rollback Complete!"))
		} else {
			b.WriteString(successStyle.Render("Update Complete!"))
		}
		b.WriteString("\n\n")
		b.WriteString(m.successMessage)
		b.WriteString("\n\n")
		if m.backupPath != "" {
			b.WriteString(subtextStyle.Render(fmt.Sprintf("Backup: %s", m.backupPath)))
			b.WriteString("\n")
			b.WriteString(subtextStyle.Render("Press 'r' or run 'bv --rollback' to restore if needed"))
			b.WriteString("\n\n")
		}
		if isRollback {
			b.WriteString(successStyle.Render("Restart bv to use the rolled-back binary."))
		} else {
			b.WriteString(successStyle.Render("Restart bv to use the updated binary."))
		}
		b.WriteString("\n\n")
		if m.CanRollback() {
			b.WriteString(subtextStyle.Render("[R] Rollback   [Enter] Close"))
		} else {
			b.WriteString(subtextStyle.Render("[Enter] Close"))
		}

	case UpdateStateError:
		if m.lastOpWasRollback || strings.HasPrefix(m.errorMessage, "Rollback failed") {
			b.WriteString(errorStyle.Render("Rollback Failed"))
		} else {
			b.WriteString(errorStyle.Render("Update Failed"))
		}
		b.WriteString("\n\n")
		b.WriteString(m.errorMessage)
		b.WriteString("\n\n")
		if m.CanRollback() {
			b.WriteString(subtextStyle.Render("[R] Rollback   [Enter] Close"))
		} else {
			b.WriteString(subtextStyle.Render("[Enter] Close"))
		}
	}

	return modalStyle.Render(b.String())
}

// renderPhaseSteps renders the 3-phase update stepper (Download -> Verify -> Install).
func (m UpdateModal) renderPhaseSteps() string {
	r := m.theme.Renderer
	doneStyle := r.NewStyle().Foreground(ColorStatusOpen)
	activeStyle := r.NewStyle().Foreground(m.theme.Primary).Bold(true)
	pendingStyle := r.NewStyle().Foreground(m.theme.Subtext)

	activeStep := 0
	switch m.state {
	case UpdateStateDownloading:
		activeStep = 0
	case UpdateStateVerifying:
		activeStep = 1
	case UpdateStateInstalling:
		activeStep = 2
	}

	labels := []string{"1/3 Download", "2/3 Verify", "3/3 Install"}
	parts := make([]string, len(labels))
	for i, label := range labels {
		switch {
		case i < activeStep:
			parts[i] = doneStyle.Render("✓ " + label)
		case i == activeStep:
			parts[i] = activeStyle.Render("● " + label)
		default:
			parts[i] = pendingStyle.Render("○ " + label)
		}
	}
	line := "Phase: " + strings.Join(parts, "  →  ")
	if m.phaseDetail != "" {
		line += "\n" + pendingStyle.Italic(true).Render(m.phaseDetail)
	}
	return line + "\n"
}

// renderSpinner returns an animated spinner character
func (m UpdateModal) renderSpinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	idx := int(time.Since(m.startTime).Milliseconds()/100) % len(frames)
	return frames[idx]
}

// renderProgressBar renders a progress bar for downloads
func (m UpdateModal) renderProgressBar() string {
	if m.progress.TotalBytes <= 0 {
		// Indeterminate progress
		return "[                    ]"
	}

	percent := float64(m.progress.BytesDownloaded) / float64(m.progress.TotalBytes)
	if percent < 0 {
		percent = 0
	} else if percent > 1 {
		percent = 1
	}
	width := 20
	filled := int(percent * float64(width))

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %.0f%%", bar, percent*100)
}

// SetSize sets the modal dimensions based on terminal size.
func (m *UpdateModal) SetSize(width, height int) {
	maxWidth := width - 10
	if maxWidth < 50 {
		maxWidth = 50
	}
	if maxWidth > 70 {
		maxWidth = 70
	}
	m.width = maxWidth
	m.termWidth = width
	m.height = height
}

// IsConfirming returns true if the modal is in confirm state
func (m UpdateModal) IsConfirming() bool {
	return m.state == UpdateStateConfirm
}

// IsCancelled returns true if user selected Cancel
func (m UpdateModal) IsCancelled() bool {
	return m.state == UpdateStateConfirm && m.confirmFocus == 1
}

// IsComplete returns true if the update is done (success or error)
func (m UpdateModal) IsComplete() bool {
	return m.state == UpdateStateSuccess || m.state == UpdateStateError
}

// IsInProgress returns true if the update is in progress
func (m UpdateModal) IsInProgress() bool {
	return m.state == UpdateStateDownloading ||
		m.state == UpdateStateVerifying ||
		m.state == UpdateStateInstalling ||
		m.state == UpdateStateRollingBack
}

// CenterModal returns the modal view centered in the given dimensions.
func (m UpdateModal) CenterModal(termWidth, termHeight int) string {
	if termWidth > 0 {
		m.termWidth = termWidth
	}
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
