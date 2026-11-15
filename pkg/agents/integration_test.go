package agents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFullFlow_Accept tests the complete flow when user accepts.
func TestFullFlow_Accept(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Create AGENTS.md without blurb
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# My AGENTS.md\n\nExisting content."), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Should prompt for new project
	if !ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt for new project without preference")
	}

	// 2. Detect file
	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Fatal("Should detect AGENTS.md")
	}
	if !detection.NeedsBlurb() {
		t.Error("Should need blurb")
	}

	// 3. User accepts - append blurb
	if err := AppendBlurbToFile(detection.FilePath); err != nil {
		t.Fatalf("AppendBlurbToFile failed: %v", err)
	}

	// 4. Record acceptance
	if err := RecordAccept(tmpDir); err != nil {
		t.Fatalf("RecordAccept failed: %v", err)
	}

	// 5. Verify blurb was added
	present, err := VerifyBlurbPresent(detection.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Error("Blurb should be present after append")
	}

	// 6. Should not prompt again
	if ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should not prompt after acceptance")
	}

	// 7. Verify original content preserved
	content, _ := os.ReadFile(agentsPath)
	if !strContains(string(content), "Existing content.") {
		t.Error("Original content should be preserved")
	}
}

// TestFullFlow_Decline tests the flow when user declines without "don't ask again".
func TestFullFlow_Decline(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Create AGENTS.md without blurb
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# My AGENTS.md"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Should prompt initially
	if !ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt for new project")
	}

	// 2. User declines (but allows future prompts)
	if err := RecordDecline(tmpDir, false); err != nil {
		t.Fatalf("RecordDecline failed: %v", err)
	}

	// 3. Blurb should not be added
	present, _ := VerifyBlurbPresent(agentsPath)
	if present {
		t.Error("Blurb should not be added on decline")
	}

	// 4. Should not prompt again (we respect decline)
	if ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should not prompt after decline (respects user choice)")
	}
}

// TestFullFlow_NeverAsk tests the flow when user chooses "don't ask again".
func TestFullFlow_NeverAsk(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Create AGENTS.md without blurb
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# My AGENTS.md"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Should prompt initially
	if !ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt for new project")
	}

	// 2. User says "don't ask again"
	if err := RecordDecline(tmpDir, true); err != nil {
		t.Fatalf("RecordDecline failed: %v", err)
	}

	// 3. Verify preference stored
	pref, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref == nil || !pref.DontAskAgain {
		t.Error("DontAskAgain preference should be stored")
	}

	// 4. Should never prompt again
	if ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should never prompt after 'don't ask again'")
	}
}

// TestFullFlow_AlreadyHasBlurb tests that we don't prompt when blurb exists.
func TestFullFlow_AlreadyHasBlurb(t *testing.T) {
	tmpDir := t.TempDir()

	// Create AGENTS.md with blurb
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# My AGENTS.md\n\n" + AgentBlurb
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should find file with blurb
	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect AGENTS.md")
	}
	if !detection.HasBlurb {
		t.Error("Should detect existing blurb")
	}
	if detection.NeedsBlurb() {
		t.Error("Should not need blurb")
	}
}

// TestFullFlow_NoAgentFile tests behavior with no agent file.
func TestFullFlow_NoAgentFile(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Empty directory - should prompt but detection returns empty
	detection := DetectAgentFile(tmpDir)
	if detection.Found() {
		t.Error("Should not find agent file in empty directory")
	}

	// Should still prompt for new project (might create file)
	if !ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt for new project (no preference stored)")
	}
}

// TestFullFlow_ClaudeMDFallback tests fallback to CLAUDE.md.
func TestFullFlow_ClaudeMDFallback(t *testing.T) {
	tmpDir := t.TempDir()

	// Create only CLAUDE.md
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Claude Instructions"), 0644); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect CLAUDE.md")
	}
	if detection.FileType != "CLAUDE.md" {
		t.Errorf("FileType should be 'CLAUDE.md', got %q", detection.FileType)
	}
	if detection.FilePath != claudePath {
		t.Errorf("FilePath should be %q, got %q", claudePath, detection.FilePath)
	}
}

// strContains is a simple helper for string containment.
func strContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
