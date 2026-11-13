package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestNewAgentPromptModal(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	if modal.filePath != "/test/AGENTS.md" {
		t.Errorf("Expected filePath '/test/AGENTS.md', got %q", modal.filePath)
	}
	if modal.fileType != "AGENTS.md" {
		t.Errorf("Expected fileType 'AGENTS.md', got %q", modal.fileType)
	}
	if modal.selection != 0 {
		t.Errorf("Expected initial selection 0, got %d", modal.selection)
	}
	if modal.result != AgentPromptPending {
		t.Errorf("Expected initial result Pending, got %d", modal.result)
	}
}

func TestAgentPromptModalKeyNavigation(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	// Initial selection is 0 (Yes)
	if modal.selection != 0 {
		t.Errorf("Expected initial selection 0, got %d", modal.selection)
	}

	// Press right
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if modal.selection != 1 {
		t.Errorf("Expected selection 1 after right, got %d", modal.selection)
	}

	// Press right again
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if modal.selection != 2 {
		t.Errorf("Expected selection 2 after right, got %d", modal.selection)
	}

	// Press right - should wrap to 0
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if modal.selection != 0 {
		t.Errorf("Expected selection to wrap to 0, got %d", modal.selection)
	}

	// Press left - should wrap to 2
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if modal.selection != 2 {
		t.Errorf("Expected selection to wrap to 2, got %d", modal.selection)
	}
}

func TestAgentPromptModalEnterConfirms(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}

	tests := []struct {
		name           string
		selection      int
		expectedResult AgentPromptResult
	}{
		{"yes", 0, AgentPromptAccept},
		{"no", 1, AgentPromptDecline},
		{"never", 2, AgentPromptNeverAsk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)
			modal.selection = tt.selection

			modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if modal.result != tt.expectedResult {
				t.Errorf("Expected result %d, got %d", tt.expectedResult, modal.result)
			}
		})
	}
}

func TestAgentPromptModalShortcuts(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}

	tests := []struct {
		key            string
		expectedResult AgentPromptResult
	}{
		{"y", AgentPromptAccept},
		{"Y", AgentPromptAccept},
		{"n", AgentPromptDecline},
		{"N", AgentPromptDecline},
		{"d", AgentPromptNeverAsk},
		{"D", AgentPromptNeverAsk},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)
			modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)})
			if modal.result != tt.expectedResult {
				t.Errorf("Key %q: expected result %d, got %d", tt.key, tt.expectedResult, modal.result)
			}
		})
	}
}

func TestAgentPromptModalEscDismisses(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if modal.result != AgentPromptDecline {
		t.Errorf("Escape should decline, got %d", modal.result)
	}
}

func TestAgentPromptModalView(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Subtext:   lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Border:    lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
	}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	view := modal.View()

	// Should contain title
	if !strings.Contains(view, "Enhance AI Agent Integration") {
		t.Error("View should contain title")
	}

	// Should contain buttons
	if !strings.Contains(view, "Yes, add it") {
		t.Error("View should contain Yes button")
	}
	if !strings.Contains(view, "No thanks") {
		t.Error("View should contain No button")
	}
	if !strings.Contains(view, "Don't ask again") {
		t.Error("View should contain Never button")
	}

	// Should contain preview
	if !strings.Contains(view, "Preview") {
		t.Error("View should contain preview section")
	}

	// Should contain file type
	if !strings.Contains(view, "AGENTS.md") {
		t.Error("View should mention file type")
	}
}

func TestAgentPromptModalResult(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	if modal.Result() != AgentPromptPending {
		t.Error("Initial result should be Pending")
	}

	modal.result = AgentPromptAccept
	if modal.Result() != AgentPromptAccept {
		t.Error("Result should return set value")
	}
}

func TestAgentPromptModalFilePath(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/my/project/AGENTS.md", "AGENTS.md", theme)

	if modal.FilePath() != "/my/project/AGENTS.md" {
		t.Errorf("FilePath() = %q, want '/my/project/AGENTS.md'", modal.FilePath())
	}
}

func TestAgentPromptModalSetSize(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	modal.SetSize(80, 30)
	if modal.width != 80 {
		t.Errorf("Expected width 80, got %d", modal.width)
	}
	if modal.height != 30 {
		t.Errorf("Expected height 30, got %d", modal.height)
	}
}

func TestGetBlurbPreview(t *testing.T) {
	preview := getBlurbPreview()

	// Should not be empty
	if preview == "" {
		t.Error("Preview should not be empty")
	}

	// Should end with ellipsis
	if !strings.HasSuffix(preview, "...") {
		t.Error("Preview should end with ellipsis")
	}

	// Should contain some key content
	if !strings.Contains(preview, "Beads") {
		t.Error("Preview should contain 'Beads'")
	}

	// Should not contain HTML comments (markers)
	if strings.Contains(preview, "<!--") {
		t.Error("Preview should not contain HTML comment markers")
	}
}

func TestCenterModal(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Subtext:   lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Border:    lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
	}
	modal := NewAgentPromptModal("/test/AGENTS.md", "AGENTS.md", theme)

	centered := modal.CenterModal(100, 40)

	// Should not be empty
	if centered == "" {
		t.Error("Centered modal should not be empty")
	}

	// Should still contain the content
	if !strings.Contains(centered, "Enhance AI Agent") {
		t.Error("Centered modal should contain title")
	}
}

func TestCheckAgentFileCmd_UpgradeAndNegativeControls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	runCheck := func(t *testing.T, dir string) AgentFileCheckMsg {
		t.Helper()
		msg := CheckAgentFileCmd(dir)()
		check, ok := msg.(AgentFileCheckMsg)
		if !ok {
			t.Fatalf("CheckAgentFileCmd returned %T, want AgentFileCheckMsg", msg)
		}
		return check
	}

	t.Run("missing blurb prompts fresh add", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project Rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := runCheck(t, dir)
		if !got.ShouldPrompt || got.IsUpgrade {
			t.Fatalf("got %+v, want ShouldPrompt=true IsUpgrade=false", got)
		}
	})

	t.Run("outdated v3 blurb prompts upgrade even after prior v3 acceptance", func(t *testing.T) {
		dir := t.TempDir()
		oldDoc := "# Project Rules\n\n<!-- bv-agent-instructions-v3 -->\nOld v3 instructions\n<!-- end-bv-agent-instructions -->\n"
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(oldDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := agents.SaveAgentPromptPreference(dir, agents.AgentPromptPreference{
			BlurbVersionAdded: 3,
			AddedAt:           time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		got := runCheck(t, dir)
		if !got.ShouldPrompt || !got.IsUpgrade || got.ExistingVersion != 3 {
			t.Fatalf("got %+v, want ShouldPrompt=true IsUpgrade=true ExistingVersion=3", got)
		}
	})

	t.Run("current version blurb does not prompt", func(t *testing.T) {
		dir := t.TempDir()
		curDoc := "# Project Rules\n\n" + agents.AgentBlurb + "\n"
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(curDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runCheck(t, dir); got.ShouldPrompt {
			t.Fatalf("current v%d blurb must not prompt, got %+v", agents.BlurbVersion, got)
		}
	})

	t.Run("future blurb v99 negative control does not prompt", func(t *testing.T) {
		dir := t.TempDir()
		futureDoc := "# Project Rules\n\n<!-- bv-agent-instructions-v99 -->\nFuture instructions\n<!-- end-bv-agent-instructions -->\n"
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(futureDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runCheck(t, dir); got.ShouldPrompt {
			t.Fatalf("future v99 blurb must never prompt for downgrade, got %+v", got)
		}
	})

	t.Run("malformed blurb negative control does not prompt", func(t *testing.T) {
		dir := t.TempDir()
		malformedDoc := "# Project Rules\n\n<!-- bv-agent-instructions-v3 -->\nMissing closing marker\n"
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(malformedDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runCheck(t, dir); got.ShouldPrompt {
			t.Fatalf("malformed blurb must not auto-prompt, got %+v", got)
		}
	})
}

func TestModel_AgentUpgradePromptAcceptAndAsOfGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// 1. Verify upgrade modal rendering and in-place UpdateBlurbInFile on accept.
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(dir, "AGENTS.md")
	initialContent := "# Custom Header\n\n<!-- bv-agent-instructions-v3 -->\nOutdated v3 blurb\n<!-- end-bv-agent-instructions -->\n\n# Custom Footer\n"
	if err := os.WriteFile(agentsPath, []byte(initialContent), 0o644); err != nil {
		t.Fatal(err)
	}

	issues := []model.Issue{{ID: "A-1", Title: "One", Status: model.StatusOpen, Priority: 1}}
	mp := NewModel(issues, nil, beadsPath)
	t.Cleanup(mp.Stop)
	mp.Update(tea.WindowSizeMsg{Width: 120, Height: 36})

	checkMsg := CheckAgentFileCmd(dir)()
	updated, _ := mp.Update(checkMsg)
	mp = updated.(*Model)
	if !mp.showAgentPrompt || !mp.agentPromptModal.IsUpgrade() || mp.agentPromptModal.ExistingVersion() != 3 {
		t.Fatalf("expected upgrade modal for v3, show=%v isUpgrade=%v ver=%d",
			mp.showAgentPrompt, mp.agentPromptModal.IsUpgrade(), mp.agentPromptModal.ExistingVersion())
	}

	modalView := mp.agentPromptModal.View()
	wantUpgradeText := fmt.Sprintf("v%d", agents.BlurbVersion)
	if !strings.Contains(modalView, "Update AI Agent Instructions") ||
		!strings.Contains(modalView, "v3") ||
		!strings.Contains(modalView, wantUpgradeText) ||
		!strings.Contains(modalView, "Yes, update it") {
		t.Fatalf("upgrade modal view missing expected upgrade copy:\n%s", modalView)
	}

	// Accept via 'y' shortcut and verify in-place replacement to current BlurbVersion.
	updated, _ = mp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	mp = updated.(*Model)
	if mp.showAgentPrompt {
		t.Fatal("expected agent prompt modal to close after accepting")
	}
	afterBytes, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	afterContent := string(afterBytes)
	if !strings.Contains(afterContent, agents.BlurbStartMarker) ||
		strings.Contains(afterContent, "bv-agent-instructions-v3") ||
		!strings.Contains(afterContent, "# Custom Header") ||
		!strings.Contains(afterContent, "# Custom Footer") {
		t.Fatalf("expected in-place blurb upgrade preserving custom sections, got:\n%s", afterContent)
	}

	// 2. Negative control: historical --as-of mode (beadsPath == "") must NOT dispatch CheckAgentFileCmd from Init().
	asOfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(asOfDir, "AGENTS.md"), []byte("# Unmodified AGENTS.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asOfModel := NewModel(issues, nil, "")
	t.Cleanup(asOfModel.Stop)
	asOfModel.SetWorkDir(asOfDir)

	var collectMsgs func(tea.Cmd) []tea.Msg
	collectMsgs = func(cmd tea.Cmd) []tea.Msg {
		if cmd == nil {
			return nil
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, sub := range batch {
				out = append(out, collectMsgs(sub)...)
			}
			return out
		}
		return []tea.Msg{msg}
	}

	for _, msg := range collectMsgs(asOfModel.Init()) {
		if _, isAgentCheck := msg.(AgentFileCheckMsg); isAgentCheck {
			t.Fatalf("read-only historical model (beadsPath == \"\") must not dispatch CheckAgentFileCmd, got %+v", msg)
		}
	}
}
