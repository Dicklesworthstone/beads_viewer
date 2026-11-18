package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ============================================================================
// NewUpdateModal tests
// ============================================================================

func TestNewUpdateModal_InitialState(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "https://github.com/example/release", theme)

	if m.state != UpdateStateConfirm {
		t.Errorf("expected initial state UpdateStateConfirm, got %v", m.state)
	}
	if m.newVersion != "v1.0.0" {
		t.Errorf("expected newVersion v1.0.0, got %s", m.newVersion)
	}
	if m.confirmFocus != 0 {
		t.Errorf("expected confirmFocus 0 (Update button), got %d", m.confirmFocus)
	}
	if !m.IsConfirming() {
		t.Error("expected IsConfirming() to return true")
	}
	if m.IsComplete() {
		t.Error("expected IsComplete() to return false")
	}
	if m.IsInProgress() {
		t.Error("expected IsInProgress() to return false")
	}
}

func TestValidateConfirmedRelease(t *testing.T) {
	if err := validateConfirmedRelease(&updater.Release{TagName: "v1.2.3"}, "v1.2.3"); err != nil {
		t.Fatalf("matching release rejected: %v", err)
	}
	if err := validateConfirmedRelease(&updater.Release{TagName: "v1.2.4"}, "v1.2.3"); err == nil {
		t.Fatal("changed release accepted without renewed confirmation")
	}
	if err := validateConfirmedRelease(nil, "v1.2.3"); err == nil {
		t.Fatal("nil release accepted")
	}
}

// ============================================================================
// State helper tests
// ============================================================================

func TestUpdateModal_IsConfirming(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	if !m.IsConfirming() {
		t.Error("expected IsConfirming() true in confirm state")
	}

	m.state = UpdateStateDownloading
	if m.IsConfirming() {
		t.Error("expected IsConfirming() false in downloading state")
	}
}

func TestUpdateModal_IsCancelled(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	// Initially focused on Update button
	if m.IsCancelled() {
		t.Error("expected IsCancelled() false when focused on Update")
	}

	// Move focus to Cancel
	m.confirmFocus = 1
	if !m.IsCancelled() {
		t.Error("expected IsCancelled() true when focused on Cancel")
	}

	// Not cancelled if not in confirm state
	m.state = UpdateStateDownloading
	if m.IsCancelled() {
		t.Error("expected IsCancelled() false when not in confirm state")
	}
}

func TestUpdateModal_IsComplete(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	if m.IsComplete() {
		t.Error("expected IsComplete() false in confirm state")
	}

	m.state = UpdateStateSuccess
	if !m.IsComplete() {
		t.Error("expected IsComplete() true in success state")
	}

	m.state = UpdateStateError
	if !m.IsComplete() {
		t.Error("expected IsComplete() true in error state")
	}
}

func TestUpdateModal_IsInProgress(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	if m.IsInProgress() {
		t.Error("expected IsInProgress() false in confirm state")
	}

	for _, state := range []UpdateState{UpdateStateDownloading, UpdateStateVerifying, UpdateStateInstalling} {
		m.state = state
		if !m.IsInProgress() {
			t.Errorf("expected IsInProgress() true for state %v", state)
		}
	}
}

// ============================================================================
// Update (key handling) tests
// ============================================================================

func TestUpdateModal_Update_NavigationKeys(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	tests := []struct {
		name          string
		key           string
		expectedFocus int
	}{
		{"left moves to Update", "left", 0},
		{"h moves to Update", "h", 0},
		{"right moves to Cancel", "right", 1},
		{"l moves to Cancel", "l", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewUpdateModal("v1.0.0", "", theme)
			// Start with focus in middle state
			m.confirmFocus = 0
			if tt.expectedFocus == 0 {
				m.confirmFocus = 1 // Start on Cancel to test moving to Update
			}

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			if tt.key == "left" {
				msg = tea.KeyMsg{Type: tea.KeyLeft}
			} else if tt.key == "right" {
				msg = tea.KeyMsg{Type: tea.KeyRight}
			}

			updated, _ := m.Update(msg)
			if updated.confirmFocus != tt.expectedFocus {
				t.Errorf("expected confirmFocus %d, got %d", tt.expectedFocus, updated.confirmFocus)
			}
		})
	}
}

func TestUpdateModal_Update_TabCyclesFocus(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	// Start on Update (0)
	if m.confirmFocus != 0 {
		t.Fatal("expected initial focus on Update")
	}

	// Tab to Cancel (1)
	msg := tea.KeyMsg{Type: tea.KeyTab}
	m, _ = m.Update(msg)
	if m.confirmFocus != 1 {
		t.Errorf("expected focus 1 after first tab, got %d", m.confirmFocus)
	}

	// Tab back to Update (0)
	m, _ = m.Update(msg)
	if m.confirmFocus != 0 {
		t.Errorf("expected focus 0 after second tab, got %d", m.confirmFocus)
	}
}

func TestUpdateModal_Update_QuickConfirmY(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}
	updated, cmd := m.Update(msg)

	if updated.state != UpdateStateDownloading {
		t.Errorf("expected state Downloading after Y, got %v", updated.state)
	}
	if cmd == nil {
		t.Error("expected command to be returned for update")
	} else {
		requireUpdateCommandBatch(t, cmd, updated.startTime)
	}
	if updated.startTime.IsZero() {
		t.Error("expected startTime to be set")
	}
}

func TestUpdateModal_Update_QuickConfirmUpperY(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Y")}
	updated, cmd := m.Update(msg)

	if updated.state != UpdateStateDownloading {
		t.Errorf("expected state Downloading after Y, got %v", updated.state)
	}
	if cmd == nil {
		t.Error("expected command to be returned")
	} else {
		requireUpdateCommandBatch(t, cmd, updated.startTime)
	}
}

func requireUpdateCommandBatch(t *testing.T, cmd tea.Cmd, wantStart time.Time) {
	t.Helper()

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("update confirmation returned %T, want tea.BatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("update confirmation scheduled %d commands, want update plus repaint tick", len(batch))
	}
	tickResult := batch[1]()
	tick, ok := tickResult.(updateTickMsg)
	if !ok {
		t.Fatalf("second update command returned %T, want updateTickMsg", tickResult)
	}
	if tick.startedAt != wantStart {
		t.Fatal("initial repaint tick was not bound to the current update run")
	}
}

func TestUpdateModal_Update_QuickCancelN(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	updated, cmd := m.Update(msg)

	// State should remain confirm, cmd should be nil (parent handles close)
	if updated.state != UpdateStateConfirm {
		t.Errorf("expected state to remain Confirm, got %v", updated.state)
	}
	if cmd != nil {
		t.Error("expected nil command for cancel")
	}
}

func TestUpdateModal_Update_EnterConfirmsUpdate(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.confirmFocus = 0 // Focus on Update

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	updated, cmd := m.Update(msg)

	if updated.state != UpdateStateDownloading {
		t.Errorf("expected state Downloading, got %v", updated.state)
	}
	if cmd == nil {
		t.Error("expected command to be returned")
	} else {
		requireUpdateCommandBatch(t, cmd, updated.startTime)
	}
}

func TestUpdateModal_Update_EnterCancels(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.confirmFocus = 1 // Focus on Cancel

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	updated, cmd := m.Update(msg)

	// Parent handles the close, state remains confirm
	if updated.state != UpdateStateConfirm {
		t.Errorf("expected state Confirm, got %v", updated.state)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateModal_Update_IgnoresKeysWhenInProgress(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading

	// Try to cancel - should be ignored
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	updated, _ := m.Update(msg)

	if updated.state != UpdateStateDownloading {
		t.Errorf("expected state to remain Downloading, got %v", updated.state)
	}
}

func TestUpdateModalTickContinuesOnlyWhileUpdateIsInProgress(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading
	m.startTime = time.Now()

	updated, cmd := m.Update(updateTickMsg{startedAt: m.startTime})
	if cmd == nil {
		t.Fatal("in-progress update tick did not schedule the next repaint")
	}

	updated.state = UpdateStateSuccess
	_, cmd = updated.Update(updateTickMsg{startedAt: updated.startTime})
	if cmd != nil {
		t.Fatal("completed update continued scheduling repaint ticks")
	}
}

func TestUpdateModalIgnoresTickFromPreviousRun(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading
	m.startTime = time.Now()

	staleStart := m.startTime.Add(-time.Second)
	_, cmd := m.Update(updateTickMsg{startedAt: staleStart})
	if cmd != nil {
		t.Fatal("stale tick joined the current update's repaint loop")
	}
}

func TestUpdateModal_Update_DismissOnComplete(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	for _, state := range []UpdateState{UpdateStateSuccess, UpdateStateError} {
		t.Run(state.String(), func(t *testing.T) {
			m := NewUpdateModal("v1.0.0", "", theme)
			m.state = state

			msg := tea.KeyMsg{Type: tea.KeyEnter}
			updated, cmd := m.Update(msg)

			// State remains, parent handles dismiss
			if updated.state != state {
				t.Errorf("expected state %v, got %v", state, updated.state)
			}
			if cmd != nil {
				t.Error("expected nil command")
			}
		})
	}
}

// ============================================================================
// Message handling tests
// ============================================================================

func TestUpdateModal_Update_ProgressMessage(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading

	msg := UpdateProgressMsg{
		Progress: UpdateProgress{
			BytesDownloaded: 500,
			TotalBytes:      1000,
			Stage:           "downloading",
		},
	}

	updated, _ := m.Update(msg)

	if updated.progress.BytesDownloaded != 500 {
		t.Errorf("expected BytesDownloaded 500, got %d", updated.progress.BytesDownloaded)
	}
	if updated.progress.TotalBytes != 1000 {
		t.Errorf("expected TotalBytes 1000, got %d", updated.progress.TotalBytes)
	}
}

func TestUpdateModal_Update_ProgressStageTransitions(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	tests := []struct {
		stage    string
		expected UpdateState
	}{
		{"downloading", UpdateStateDownloading},
		{"verifying", UpdateStateVerifying},
		{"installing", UpdateStateInstalling},
	}

	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			m := NewUpdateModal("v1.0.0", "", theme)
			m.state = UpdateStateDownloading

			msg := UpdateProgressMsg{
				Progress: UpdateProgress{Stage: tt.stage},
			}

			updated, _ := m.Update(msg)
			if updated.state != tt.expected {
				t.Errorf("expected state %v, got %v", tt.expected, updated.state)
			}
		})
	}
}

func TestUpdateModal_Update_CompleteMessageSuccess(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading

	msg := UpdateCompleteMsg{
		Success:    true,
		Message:    "Updated successfully",
		NewVersion: "v1.0.0",
		BackupPath: "/tmp/bv.backup",
	}

	updated, _ := m.Update(msg)

	if updated.state != UpdateStateSuccess {
		t.Errorf("expected state Success, got %v", updated.state)
	}
	if updated.successMessage != "Updated successfully" {
		t.Errorf("expected successMessage, got %s", updated.successMessage)
	}
	if updated.backupPath != "/tmp/bv.backup" {
		t.Errorf("expected backupPath, got %s", updated.backupPath)
	}
}

func TestUpdateModal_Update_CompleteMessageError(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.state = UpdateStateDownloading

	msg := UpdateCompleteMsg{
		Success: false,
		Message: "Download failed",
	}

	updated, _ := m.Update(msg)

	if updated.state != UpdateStateError {
		t.Errorf("expected state Error, got %v", updated.state)
	}
	if updated.errorMessage != "Download failed" {
		t.Errorf("expected errorMessage, got %s", updated.errorMessage)
	}
}

// ============================================================================
// View rendering tests
// ============================================================================

func TestUpdateModal_View_ConfirmState(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v2.0.0", "", theme)
	m.SetSize(80, 24)

	view := m.View()

	if !strings.Contains(view, "Update Available") {
		t.Error("expected 'Update Available' header in view")
	}
	if !strings.Contains(view, "v2.0.0") {
		t.Error("expected new version in view")
	}
	if !strings.Contains(view, "Update") {
		t.Error("expected Update button in view")
	}
	if !strings.Contains(view, "Cancel") {
		t.Error("expected Cancel button in view")
	}
}

func TestUpdateModal_View_DownloadingState(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v2.0.0", "", theme)
	m.state = UpdateStateDownloading
	m.startTime = time.Now()
	m.SetSize(80, 24)

	view := m.View()

	if !strings.Contains(view, "Updating") {
		t.Error("expected 'Updating' in view")
	}
	if !strings.Contains(view, "Applying") {
		t.Error("expected 'Applying' in view")
	}
	if strings.Contains(view, "[                    ]") {
		t.Error("view rendered a permanently empty progress bar without progress data")
	}
}

func TestUpdateModal_View_SuccessState(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v2.0.0", "", theme)
	m.state = UpdateStateSuccess
	m.successMessage = "Update complete"
	m.backupPath = "/tmp/backup"
	m.SetSize(80, 24)

	view := m.View()

	if !strings.Contains(view, "Update Complete") {
		t.Error("expected 'Update Complete' in view")
	}
	if !strings.Contains(view, "Update complete") {
		t.Error("expected success message in view")
	}
	if !strings.Contains(view, "/tmp/backup") {
		t.Error("expected backup path in view")
	}
	if !strings.Contains(view, "rollback") {
		t.Error("expected rollback hint in view")
	}
}

func TestUpdateModal_View_ErrorState(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v2.0.0", "", theme)
	m.state = UpdateStateError
	m.errorMessage = "Network error"
	m.SetSize(80, 24)

	view := m.View()

	if !strings.Contains(view, "Update Failed") {
		t.Error("expected 'Update Failed' in view")
	}
	if !strings.Contains(view, "Network error") {
		t.Error("expected error message in view")
	}
}

// ============================================================================
// SetSize tests
// ============================================================================

func TestUpdateModal_SetSize(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)

	m.SetSize(100, 40)
	if m.width != 70 { // Capped at 70
		t.Errorf("expected width 70 (max), got %d", m.width)
	}

	m.SetSize(40, 20)
	if m.width != 50 { // Min 50
		t.Errorf("expected width 50 (min), got %d", m.width)
	}

	m.SetSize(65, 30)
	if m.width != 55 { // 65-10 = 55
		t.Errorf("expected width 55, got %d", m.width)
	}
}

// ============================================================================
// CenterModal tests
// ============================================================================

func TestUpdateModal_CenterModal(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.SetSize(80, 24)

	centered := m.CenterModal(100, 50)

	// Just verify it doesn't panic and returns non-empty content
	if centered == "" {
		t.Error("expected non-empty centered modal")
	}
}

// ============================================================================
// Spinner and progress bar tests
// ============================================================================

func TestUpdateModal_RenderSpinner(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.startTime = time.Now()

	spinner := m.renderSpinner()

	// Should return a non-empty spinner character
	if spinner == "" {
		t.Error("expected non-empty spinner")
	}
}

func TestUpdateModal_RenderProgressBar_Indeterminate(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.progress.TotalBytes = 0

	bar := m.renderProgressBar()

	if !strings.Contains(bar, "[") || !strings.Contains(bar, "]") {
		t.Errorf("expected brackets in progress bar, got %s", bar)
	}
}

func TestUpdateModal_RenderProgressBar_WithProgress(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.progress.BytesDownloaded = 500
	m.progress.TotalBytes = 1000

	bar := m.renderProgressBar()

	if !strings.Contains(bar, "50%") {
		t.Errorf("expected 50%% in progress bar, got %s", bar)
	}
	if !strings.Contains(bar, "█") {
		t.Errorf("expected filled blocks in progress bar, got %s", bar)
	}
}

func TestUpdateModal_RenderProgressBar_Complete(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v1.0.0", "", theme)
	m.progress.BytesDownloaded = 1000
	m.progress.TotalBytes = 1000

	bar := m.renderProgressBar()

	if !strings.Contains(bar, "100%") {
		t.Errorf("expected 100%% in progress bar, got %s", bar)
	}
}

func TestUpdateModal_RenderProgressBar_ClampsOutOfRangeProgress(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	tests := []struct {
		name       string
		downloaded int64
		total      int64
		want       string
	}{
		{name: "negative downloaded bytes", downloaded: -1, total: 100, want: "0%"},
		{name: "download exceeds total", downloaded: 101, total: 100, want: "100%"},
		{name: "negative total is indeterminate", downloaded: 1, total: -1, want: "[                    ]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewUpdateModal("v1.0.0", "", theme)
			m.progress.BytesDownloaded = tt.downloaded
			m.progress.TotalBytes = tt.total

			bar := m.renderProgressBar()
			if !strings.Contains(bar, tt.want) {
				t.Fatalf("progress bar %q does not contain %q", bar, tt.want)
			}
		})
	}
}

// ============================================================================
// UpdateState String helper (for test output)
// ============================================================================

func (s UpdateState) String() string {
	switch s {
	case UpdateStateIdle:
		return "Idle"
	case UpdateStateConfirm:
		return "Confirm"
	case UpdateStateDownloading:
		return "Downloading"
	case UpdateStateVerifying:
		return "Verifying"
	case UpdateStateInstalling:
		return "Installing"
	case UpdateStateRollingBack:
		return "RollingBack"
	case UpdateStateSuccess:
		return "Success"
	case UpdateStateError:
		return "Error"
	default:
		return "Unknown"
	}
}

func TestUpdateModal_RollbackFlowAndPhaseSteps(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	origCanRollback := canRollbackCheck
	origRunner := rollbackRunner
	canRollbackCheck = func() bool { return false }
	rollbackCalled := 0
	rollbackRunner = func() error {
		rollbackCalled++
		return nil
	}
	t.Cleanup(func() {
		canRollbackCheck = origCanRollback
		rollbackRunner = origRunner
	})

	// 1. Update modal with backup available: 3-button Tab cycle + 'r' shortcut + RollbackCompleteMsg
	m := NewUpdateModal("v1.2.0", "https://github.com/example/release", theme)
	m.SetCanRollback(true)
	if !m.CanRollback() {
		t.Fatal("expected CanRollback() == true")
	}
	view := m.View()
	if !strings.Contains(view, "Rollback") || !strings.Contains(view, "[R] Rollback") {
		t.Fatalf("expected Rollback button & hint in confirm view:\n%s", view)
	}

	// Tab cycles 0 (Update) -> 1 (Cancel) -> 2 (Rollback) -> 0 (Update)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.confirmFocus != 1 {
		t.Fatalf("after 1st Tab: confirmFocus=%d, want 1", m.confirmFocus)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.confirmFocus != 2 {
		t.Fatalf("after 2nd Tab: confirmFocus=%d, want 2", m.confirmFocus)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.confirmFocus != 0 {
		t.Fatalf("after 3rd Tab: confirmFocus=%d, want 0", m.confirmFocus)
	}

	// Press 'r' to start rollback
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.state != UpdateStateRollingBack || !m.IsInProgress() || cmd == nil {
		t.Fatalf("expected UpdateStateRollingBack in-progress with cmd, got state=%s inProgress=%v cmd=%v", m.state, m.IsInProgress(), cmd != nil)
	}
	rollingView := m.View()
	if !strings.Contains(rollingView, "Rolling Back...") || !strings.Contains(rollingView, "Validating & restoring backup") {
		t.Fatalf("expected rollback progress view:\n%s", rollingView)
	}

	// Execute PerformRollbackCmd directly to verify runner invocation
	rbMsg := PerformRollbackCmd()()
	rbDone, ok := rbMsg.(RollbackCompleteMsg)
	if !ok || !rbDone.Success || rollbackCalled != 1 {
		t.Fatalf("unexpected PerformRollbackCmd result: msg=%#v calls=%d", rbMsg, rollbackCalled)
	}
	m, _ = m.Update(rbDone)
	if m.state != UpdateStateSuccess || !m.IsComplete() || m.CanRollback() {
		t.Fatalf("after RollbackCompleteMsg: state=%s complete=%v canRollback=%v", m.state, m.IsComplete(), m.CanRollback())
	}
	if !strings.Contains(m.View(), "Rollback Complete!") {
		t.Fatalf("expected Rollback Complete! header:\n%s", m.View())
	}

	// 2. Rollback-only modal (no pending update)
	rbOnly := NewUpdateModal("", "", theme)
	rbOnly.SetCanRollback(true)
	if rbOnly.confirmFocus != 2 {
		t.Fatalf("rollback-only modal initial confirmFocus=%d, want 2 (Rollback)", rbOnly.confirmFocus)
	}
	rbOnlyView := rbOnly.View()
	if !strings.Contains(rbOnlyView, "Rollback Binary") || strings.Contains(rbOnlyView, "[Y] Update") {
		t.Fatalf("unexpected rollback-only view:\n%s", rbOnlyView)
	}
	// 'y' must NOT start an update when newVersion is empty
	rbOnly, yCmd := rbOnly.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if rbOnly.state != UpdateStateConfirm || yCmd != nil {
		t.Fatalf("rollback-only modal must ignore 'y', got state=%s cmd=%v", rbOnly.state, yCmd != nil)
	}
	// Enter on focused button 2 starts rollback
	rbOnly, enterCmd := rbOnly.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if rbOnly.state != UpdateStateRollingBack || enterCmd == nil {
		t.Fatalf("Enter on button 2 should start rollback, got state=%s", rbOnly.state)
	}

	// 3. 3-phase stepper rendering & phase tracker progression
	pm := NewUpdateModal("v1.3.0", "", theme)
	tracker := &updatePhaseTracker{stage: "downloading"}
	pm.phaseTracker = tracker
	pm.state = UpdateStateDownloading
	pm.startTime = time.Now()
	dlView := pm.View()
	if !strings.Contains(dlView, "1/3 Download") || !strings.Contains(dlView, "2/3 Verify") || !strings.Contains(dlView, "3/3 Install") {
		t.Fatalf("expected 3-phase stepper in Downloading view:\n%s", dlView)
	}
	_, _ = tracker.Write([]byte("Verifying SHA256 checksum...\n"))
	pm, _ = pm.Update(updateTickMsg{startedAt: pm.startTime})
	if pm.state != UpdateStateVerifying || !strings.Contains(pm.View(), "✓ 1/3 Download") {
		t.Fatalf("expected transition to Verifying with completed step 1, state=%s view:\n%s", pm.state, pm.View())
	}
	_, _ = tracker.Write([]byte("Installing binary...\n"))
	pm, _ = pm.Update(updateTickMsg{startedAt: pm.startTime})
	if pm.state != UpdateStateInstalling || !strings.Contains(pm.View(), "✓ 2/3 Verify") {
		t.Fatalf("expected transition to Installing with completed step 2, state=%s view:\n%s", pm.state, pm.View())
	}
}

func TestUpdateModalCtrlCContextHelpAndRollbackState(t *testing.T) {
	issues := []model.Issue{
		{ID: "UM-1", Title: "Update modal regression test", Status: model.StatusOpen, Priority: 1},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	m.updateAvailable = true
	m.updateTag = "v9.9.9"
	m.updateURL = "https://example.com/releases/v9.9.9"
	m.showSelfUpdateModal()

	if !m.showUpdateModal || m.focused != focusUpdateModal {
		t.Fatalf("expected UpdateModal open and focused, got showUpdateModal=%v focused=%v", m.showUpdateModal, m.focused)
	}

	// 1. Pressing '~' while UpdateModal is in Confirm state opens contextual tutorial ON TOP of the modal.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("~")})
	if !m.showTutorial || m.focused != focusTutorial || !m.IsTutorialContextMode() {
		t.Fatalf("expected '~' in UpdateModal confirm state to open context tutorial, got showTutorial=%v focused=%v ctxMode=%v",
			m.showTutorial, m.focused, m.IsTutorialContextMode())
	}
	tutPlain := ansi.Strip(m.View())
	if !strings.Contains(tutPlain, "Tutorial") && !strings.Contains(tutPlain, "Self-Update") && !strings.Contains(tutPlain, "Update") {
		t.Fatalf("expected rendered View() to display tutorial overlay above UpdateModal:\n%s", tutPlain)
	}
	// Closing tutorial via Esc restores focusUpdateModal and keeps showUpdateModal open.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTutorial {
		t.Fatal("expected Esc to close tutorial overlay")
	}
	if !m.showUpdateModal || m.focused != focusUpdateModal {
		t.Fatalf("expected UpdateModal to remain open and focused after closing tutorial, got show=%v focused=%v",
			m.showUpdateModal, m.focused)
	}

	// While an update is actively downloading/installing, '~' must NOT interrupt or open tutorial.
	m.updateModal.state = UpdateStateDownloading
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("~")})
	if m.showTutorial {
		t.Fatal("expected '~' to be ignored while UpdateModal IsInProgress() is true")
	}
	m.updateModal.state = UpdateStateConfirm

	// 2. Successful UpdateCompleteMsg clears updateAvailable and shows "updated binary" message.
	m.Update(UpdateCompleteMsg{
		Success:    true,
		NewVersion: "v9.9.9",
		Message:    "Updated successfully to v9.9.9",
		BackupPath: "/tmp/bv.bak",
	})
	if m.updateAvailable {
		t.Fatal("expected updateAvailable=false immediately after successful update")
	}
	afterUpdateView := ansi.Strip(m.updateModal.View())
	if !strings.Contains(afterUpdateView, "Restart bv to use the updated binary.") {
		t.Fatalf("expected 'updated binary' restart prompt after update:\n%s", afterUpdateView)
	}

	// 3. Subsequent RollbackCompleteMsg renders "rolled-back binary" (never "updated binary")
	// and restores m.updateAvailable + tag/URL so the user can re-apply the update if desired.
	m.Update(RollbackCompleteMsg{
		Success: true,
		Message: "Restored backup binary",
	})
	afterRollbackView := ansi.Strip(m.updateModal.View())
	if !strings.Contains(afterRollbackView, "Restart bv to use the rolled-back binary.") {
		t.Fatalf("expected 'rolled-back binary' restart prompt after rollback:\n%s", afterRollbackView)
	}
	if strings.Contains(afterRollbackView, "Restart bv to use the updated binary.") {
		t.Fatalf("rollback success view must not say 'updated binary':\n%s", afterRollbackView)
	}
	if !m.updateAvailable || m.updateTag != "v9.9.9" || m.updateURL != "https://example.com/releases/v9.9.9" {
		t.Fatalf("expected RollbackCompleteMsg to restore updateAvailable=true tag=v9.9.9, got avail=%v tag=%q url=%q",
			m.updateAvailable, m.updateTag, m.updateURL)
	}

	// 4. Ctrl+C while UpdateModal is open must trigger quitCmd instead of being swallowed.
	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quitCmd == nil {
		t.Fatal("expected non-nil quitCmd on Ctrl+C while UpdateModal is open")
	}
}

func TestUpdateModal_ByteProgressTickRenderAndRetryReset(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	m := NewUpdateModal("v2.1.0", "", theme)
	m.SetSize(80, 24)

	// Start update via 'y': initializes fresh tracker and resets progress.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.state != UpdateStateDownloading || m.phaseTracker == nil {
		t.Fatalf("expected UpdateStateDownloading with phaseTracker, got state=%s tracker=%v", m.state, m.phaseTracker)
	}

	// Report chunked byte progress on tracker (e.g. 256 / 1024 -> 25%).
	m.phaseTracker.ReportBytes(256, 1024)

	// Planted negative control: a stale updateTickMsg from a prior run is ignored and does NOT copy tracker bytes or schedule a tick.
	staleStart := m.startTime.Add(-2 * time.Second)
	mStale, staleCmd := m.Update(updateTickMsg{startedAt: staleStart})
	if staleCmd != nil || mStale.progress.BytesDownloaded != 0 || mStale.progress.TotalBytes != 0 {
		t.Fatalf("stale updateTickMsg mutated progress or scheduled tick: cmd=%v progress=%+v", staleCmd != nil, mStale.progress)
	}

	// Positive observable: matching updateTickMsg snapshots (downloaded, total) and renders percentage in View().
	m, tickCmd := m.Update(updateTickMsg{startedAt: m.startTime})
	if tickCmd == nil {
		t.Fatal("expected matching updateTickMsg to schedule next tick while downloading")
	}
	if m.progress.BytesDownloaded != 256 || m.progress.TotalBytes != 1024 {
		t.Fatalf("progress = %+v, want 256/1024", m.progress)
	}
	view25 := ansi.Strip(m.View())
	if !strings.Contains(view25, "25%") {
		t.Fatalf("expected 25%% progress bar in Downloading view:\n%s", view25)
	}

	// Advance to 768 / 1024 (75%) and verify monotonic update.
	m.phaseTracker.ReportBytes(768, 1024)
	m, _ = m.Update(updateTickMsg{startedAt: m.startTime})
	if view75 := ansi.Strip(m.View()); !strings.Contains(view75, "75%") {
		t.Fatalf("expected 75%% progress bar after chunk advance:\n%s", view75)
	}

	// Verify startUpdate() resets stale byte counts if restarted after an error.
	m, _ = m.Update(UpdateCompleteMsg{Success: false, Message: "network glitch"})
	m.state = UpdateStateConfirm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.progress.BytesDownloaded != 0 || m.progress.TotalBytes != 0 || m.progress.Stage != "downloading" {
		t.Fatalf("startUpdate did not clear stale byte counts on retry: %+v", m.progress)
	}
	if retryView := ansi.Strip(m.View()); strings.Contains(retryView, "75%") {
		t.Fatalf("retry view flashed stale 75%% byte count before first tick:\n%s", retryView)
	}
}
