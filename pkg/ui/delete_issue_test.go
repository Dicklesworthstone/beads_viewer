package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
)

func TestDeleteConfirmationDefaultsToCancel(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads directory: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, nil, 0o600); err != nil {
		t.Fatalf("create issues file: %v", err)
	}

	m := NewModel([]model.Issue{{
		ID:     "bv-delete-me",
		Title:  "Delete only after confirmation",
		Status: model.StatusOpen,
	}}, nil, beadsPath)
	m.ready = true
	m.showDetails = true
	m.focused = focusDetail

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("opening delete confirmation must not execute a command")
	}
	if !m.showDeleteConfirm {
		t.Fatal("expected delete confirmation to be visible")
	}
	if m.deleteTargetID != "bv-delete-me" {
		t.Fatalf("delete target = %q, want bv-delete-me", m.deleteTargetID)
	}
	if m.deleteConfirmFocus != 0 {
		t.Fatalf("default button = %d, want Cancel (0)", m.deleteConfirmFocus)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("confirming the default Cancel button must not execute deletion")
	}
	if m.showDeleteConfirm {
		t.Fatal("expected confirmation to close after Cancel")
	}
	if m.focused != focusDetail {
		t.Fatalf("focus = %v, want detail after Cancel", m.focused)
	}
}

func TestDeleteConfirmationRequiresExplicitDeleteSelection(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads directory: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, nil, 0o600); err != nil {
		t.Fatalf("create issues file: %v", err)
	}

	m := NewModel([]model.Issue{{
		ID:     "bv-delete-me",
		Title:  "Delete only after confirmation",
		Status: model.StatusOpen,
	}}, nil, beadsPath)
	m.ready = true
	m.showDetails = true
	m.focused = focusDetail

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if m.deleteConfirmFocus != 1 {
		t.Fatalf("button = %d, want Delete (1)", m.deleteConfirmFocus)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("explicitly confirming Delete should return a deletion command")
	}
	if !m.deleteInProgress {
		t.Fatal("expected deletion-in-progress state")
	}
}

func TestDeleteConfirmationRendersExactTargetAndButtons(t *testing.T) {
	m := newTestModel()
	m.ready = true
	m.width = 100
	m.height = 30
	m.showDeleteConfirm = true
	m.deleteTargetID = "bv-exact-id"
	m.deleteTargetTitle = "Exact target title"

	view := m.View()
	for _, want := range []string{"Delete this bead?", "bv-exact-id", "Exact target title", "Cancel", "Delete"} {
		if !strings.Contains(view, want) {
			t.Fatalf("delete confirmation missing %q", want)
		}
	}
	if got := m.CurrentContext(); got != ContextDeleteConfirm {
		t.Fatalf("context = %q, want %q", got, ContextDeleteConfirm)
	}
	if !ContextDeleteConfirm.IsOverlay() {
		t.Fatal("delete confirmation must be classified as an overlay")
	}
}

func TestBuildDeleteIssueCommandSelectsRepositoryBackend(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, beadsDir string)
		wantBinary string
	}{
		{
			name: "br SQLite workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
					t.Fatalf("create beads.db: %v", err)
				}
			},
			wantBinary: "br",
		},
		{
			name: "bd Dolt workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
					t.Fatalf("create embeddeddolt: %v", err)
				}
			},
			wantBinary: "bd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			beadsDir := filepath.Join(repoDir, ".beads")
			if err := os.Mkdir(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads directory: %v", err)
			}
			tt.setup(t, beadsDir)
			beadsPath := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(beadsPath, nil, 0o600); err != nil {
				t.Fatalf("create issues file: %v", err)
			}

			cmd, err := buildDeleteIssueCommand("bv-target", beadsPath)
			if err != nil {
				t.Fatalf("buildDeleteIssueCommand failed: %v", err)
			}
			if filepath.Base(cmd.Path) != tt.wantBinary {
				t.Fatalf("binary = %q, want %q", filepath.Base(cmd.Path), tt.wantBinary)
			}
			if cmd.Dir != repoDir {
				t.Fatalf("working directory = %q, want %q", cmd.Dir, repoDir)
			}
			joinedArgs := strings.Join(cmd.Args, " ")
			for _, want := range []string{"delete", "--reason", "--json", "-- bv-target"} {
				if !strings.Contains(joinedArgs, want) {
					t.Fatalf("delete command %q missing %q", joinedArgs, want)
				}
			}
			for _, forbidden := range []string{"--force", "--cascade", "--hard"} {
				if strings.Contains(joinedArgs, forbidden) {
					t.Fatalf("delete command must not contain %q: %s", forbidden, joinedArgs)
				}
			}
		})
	}
}

func TestDeleteUnavailableInReadOnlyContexts(t *testing.T) {
	m := newTestModel()
	m.timeTravelMode = true
	m.promptDeleteSelectedIssue()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "historical") {
		t.Fatalf("expected historical-state error, got %q", m.statusMsg)
	}

	m = newTestModel()
	m.workspaceMode = true
	m.promptDeleteSelectedIssue()
	if !m.statusIsError || !strings.Contains(m.statusMsg, "single writable") {
		t.Fatalf("expected workspace-mode error, got %q", m.statusMsg)
	}
}

func TestDeleteResultRefreshesAfterSuccessAndPreservesFailure(t *testing.T) {
	m := newTestModel()
	m.showDeleteConfirm = true
	m.deleteInProgress = true
	m.deleteTargetID = "bv-success"
	m.deleteTargetTitle = "Successful deletion"
	m.showDetails = true
	m.focused = focusDeleteConfirm
	m.beadsPath = filepath.Join(t.TempDir(), ".beads", "issues.jsonl")

	updated, cmd := m.Update(deleteIssueResultMsg{issueID: "bv-success"})
	success := updated.(Model)
	if success.showDeleteConfirm || success.deleteInProgress {
		t.Fatal("successful deletion should close the confirmation")
	}
	if success.showDetails || success.focused != focusList {
		t.Fatalf("successful deletion should return to list, details=%v focus=%v", success.showDetails, success.focused)
	}
	if success.statusIsError || !strings.Contains(success.statusMsg, "Deleted bv-success") {
		t.Fatalf("unexpected success status: %q", success.statusMsg)
	}
	if cmd == nil {
		t.Fatal("successful deletion should trigger an immediate refresh")
	}
	refreshMsg := cmd()
	if _, ok := refreshMsg.(FileChangedMsg); !ok {
		t.Fatalf("refresh command returned %T, want FileChangedMsg", refreshMsg)
	}

	m = newTestModel()
	m.showDeleteConfirm = true
	m.deleteInProgress = true
	m.deleteTargetID = "bv-blocked"
	m.showDetails = true
	m.focused = focusDeleteConfirm

	updated, cmd = m.Update(deleteIssueResultMsg{
		issueID: "bv-blocked",
		output:  "cannot delete: dependent issue exists",
		err:     os.ErrPermission,
	})
	failed := updated.(Model)
	if cmd != nil {
		t.Fatal("failed deletion must not trigger a refresh")
	}
	if failed.showDeleteConfirm || failed.deleteInProgress {
		t.Fatal("failed deletion should close the progress dialog")
	}
	if !failed.showDetails || failed.focused != focusDetail {
		t.Fatalf("failed deletion should preserve detail view, details=%v focus=%v", failed.showDetails, failed.focused)
	}
	if !failed.statusIsError || !strings.Contains(failed.statusMsg, "dependent issue exists") {
		t.Fatalf("unexpected failure status: %q", failed.statusMsg)
	}
}
