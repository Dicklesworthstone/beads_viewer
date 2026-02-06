package main_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
)

// TestAgentsE2E_DetectionFlow tests the complete detection flow across different scenarios.
func TestAgentsE2E_DetectionFlow(t *testing.T) {
	t.Run("no_agent_file_prompts", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Should prompt for new project (no agent file, no preference)
		if !agents.ShouldPromptForAgentFile(tmpDir) {
			t.Error("Should prompt for new project without agent file")
		}

		// Detection should not find any file
		detection := agents.DetectAgentFile(tmpDir)
		if detection.Found() {
			t.Error("Should not find agent file in empty directory")
		}
	})

	t.Run("agents_md_without_blurb_prompts", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		if err := os.WriteFile(agentsPath, []byte("# Project Instructions\n\nSome content."), 0644); err != nil {
			t.Fatal(err)
		}

		if !agents.ShouldPromptForAgentFile(tmpDir) {
			t.Error("Should prompt when AGENTS.md exists without blurb")
		}

		detection := agents.DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Should detect AGENTS.md")
		}
		if detection.HasBlurb {
			t.Error("Should detect that blurb is missing")
		}
		if detection.FileType != "AGENTS.md" {
			t.Errorf("FileType should be 'AGENTS.md', got %q", detection.FileType)
		}
	})

	t.Run("agents_md_with_blurb_no_prompt", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		content := "# Project Instructions\n\n" + agents.AgentBlurb
		if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		detection := agents.DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Should detect AGENTS.md")
		}
		if !detection.HasBlurb {
			t.Error("Should detect existing blurb")
		}
		if detection.NeedsBlurb() {
			t.Error("Should not need blurb when already present")
		}
	})

	t.Run("claude_md_fallback", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudePath := filepath.Join(tmpDir, "CLAUDE.md")
		if err := os.WriteFile(claudePath, []byte("# Claude Instructions"), 0644); err != nil {
			t.Fatal(err)
		}

		detection := agents.DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Should detect CLAUDE.md as fallback")
		}
		if detection.FileType != "CLAUDE.md" {
			t.Errorf("FileType should be 'CLAUDE.md', got %q", detection.FileType)
		}
		if detection.FilePath != claudePath {
			t.Errorf("FilePath should be %q, got %q", claudePath, detection.FilePath)
		}
	})

	t.Run("agents_md_takes_precedence_over_claude_md", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		claudePath := filepath.Join(tmpDir, "CLAUDE.md")
		if err := os.WriteFile(agentsPath, []byte("# Agents Instructions"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(claudePath, []byte("# Claude Instructions"), 0644); err != nil {
			t.Fatal(err)
		}

		detection := agents.DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Should detect agent file")
		}
		if detection.FileType != "AGENTS.md" {
			t.Errorf("Should prefer AGENTS.md, got %q", detection.FileType)
		}
	})

	t.Run("lowercase_agents_md_detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsPath := filepath.Join(tmpDir, "agents.md")
		if err := os.WriteFile(agentsPath, []byte("# Agents Instructions"), 0644); err != nil {
			t.Fatal(err)
		}

		detection := agents.DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Should detect lowercase agents.md")
		}
	})
}

// TestAgentsE2E_AcceptFlow tests the complete accept flow end-to-end.
func TestAgentsE2E_AcceptFlow(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create AGENTS.md without blurb
	original := "# My Project\n\nExisting instructions here."
	if err := os.WriteFile(agentsPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Should prompt
	if !agents.ShouldPromptForAgentFile(tmpDir) {
		t.Fatal("Should prompt for new project")
	}

	// Step 2: Detect and verify needs blurb
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() || !detection.NeedsBlurb() {
		t.Fatal("Should find file and need blurb")
	}

	// Step 3: User accepts - append blurb
	if err := agents.AppendBlurbToFile(detection.FilePath); err != nil {
		t.Fatalf("AppendBlurbToFile failed: %v", err)
	}

	// Step 4: Record acceptance
	if err := agents.RecordAccept(tmpDir); err != nil {
		t.Fatalf("RecordAccept failed: %v", err)
	}

	// Step 5: Verify blurb was added
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(content)

	// Original content preserved
	if !strings.Contains(contentStr, "Existing instructions here.") {
		t.Error("Original content should be preserved")
	}

	// Blurb markers present
	if !strings.Contains(contentStr, agents.BlurbStartMarker) {
		t.Error("Blurb start marker missing")
	}
	if !strings.Contains(contentStr, agents.BlurbEndMarker) {
		t.Error("Blurb end marker missing")
	}

	// Verify blurb content
	present, err := agents.VerifyBlurbPresent(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Error("Blurb verification failed")
	}

	// Step 6: Should not prompt again
	if agents.ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should not prompt after acceptance")
	}
}

// TestAgentsE2E_DeclineFlow tests the decline flow.
func TestAgentsE2E_DeclineFlow(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	if err := os.WriteFile(agentsPath, []byte("# My Project"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should prompt initially
	if !agents.ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt initially")
	}

	// User declines (but allows future prompts)
	if err := agents.RecordDecline(tmpDir, false); err != nil {
		t.Fatalf("RecordDecline failed: %v", err)
	}

	// Blurb should not be added
	present, _ := agents.VerifyBlurbPresent(agentsPath)
	if present {
		t.Error("Blurb should not be added on decline")
	}

	// Should not prompt again (decline is remembered)
	if agents.ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should not prompt after decline")
	}
}

// TestAgentsE2E_NeverAskFlow tests the "don't ask again" flow.
func TestAgentsE2E_NeverAskFlow(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	if err := os.WriteFile(agentsPath, []byte("# My Project"), 0644); err != nil {
		t.Fatal(err)
	}

	// User says "don't ask again"
	if err := agents.RecordDecline(tmpDir, true); err != nil {
		t.Fatalf("RecordDecline failed: %v", err)
	}

	// Verify preference stored
	pref, err := agents.LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref == nil || !pref.DontAskAgain {
		t.Error("DontAskAgain preference should be stored")
	}

	// Should never prompt again
	if agents.ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should never prompt after 'don't ask again'")
	}
}

// TestAgentsE2E_PreferencePersistence tests that preferences persist correctly.
func TestAgentsE2E_PreferencePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	if err := os.WriteFile(agentsPath, []byte("# Test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Record acceptance
	if err := agents.RecordAccept(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Load preference to verify persistence
	pref, err := agents.LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref == nil {
		t.Fatal("Preference should exist")
	}
	if pref.BlurbVersionAdded == 0 {
		t.Error("BlurbVersionAdded should be set")
	}

	// Clear preference
	if err := agents.ClearPreference(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Should prompt again after clearing
	if !agents.ShouldPromptForAgentFile(tmpDir) {
		t.Error("Should prompt after clearing preference")
	}
}

// TestAgentsE2E_LegacyBlurbMigration tests detection and update of legacy blurbs.
func TestAgentsE2E_LegacyBlurbMigration(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with old-format blurb (simulated legacy content)
	oldContent := "# My Project\n\n" + agents.BlurbStartMarker + "\nOld blurb content\n" + agents.BlurbEndMarker + "\n"
	if err := os.WriteFile(agentsPath, []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should find blurb
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect file")
	}
	if !detection.HasBlurb {
		t.Error("Should detect blurb presence")
	}

	// Update blurb (migration)
	if err := agents.UpdateBlurbInFile(agentsPath); err != nil {
		t.Fatal(err)
	}

	// Verify only one blurb exists
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(content), agents.BlurbStartMarker)
	if count != 1 {
		t.Errorf("Expected exactly 1 blurb marker, got %d", count)
	}

	// Verify new content
	if !strings.Contains(string(content), "br ready") {
		t.Error("Should have current blurb content")
	}
}

// TestAgentsE2E_BlurbRemoval tests complete removal of blurb.
func TestAgentsE2E_BlurbRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with blurb
	content := "# My Project\n\nBefore blurb.\n\n" + agents.AgentBlurb + "\n\nAfter blurb."
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify blurb present
	present, _ := agents.VerifyBlurbPresent(agentsPath)
	if !present {
		t.Fatal("Blurb should be present initially")
	}

	// Remove blurb
	if err := agents.RemoveBlurbFromFile(agentsPath); err != nil {
		t.Fatal(err)
	}

	// Verify blurb removed
	present, _ = agents.VerifyBlurbPresent(agentsPath)
	if present {
		t.Error("Blurb should be removed")
	}

	// Verify surrounding content preserved
	newContent, _ := os.ReadFile(agentsPath)
	contentStr := string(newContent)
	if !strings.Contains(contentStr, "Before blurb.") {
		t.Error("Content before blurb should be preserved")
	}
	if !strings.Contains(contentStr, "After blurb.") {
		t.Error("Content after blurb should be preserved")
	}
}

// TestAgentsE2E_EnsureBlurbIdempotent tests that EnsureBlurb is idempotent.
func TestAgentsE2E_EnsureBlurbIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	if err := os.WriteFile(agentsPath, []byte("# Initial Content"), 0644); err != nil {
		t.Fatal(err)
	}

	// First call - adds blurb
	if err := agents.EnsureBlurb(tmpDir); err != nil {
		t.Fatal(err)
	}
	content1, _ := os.ReadFile(agentsPath)

	// Second call - should not duplicate
	if err := agents.EnsureBlurb(tmpDir); err != nil {
		t.Fatal(err)
	}
	content2, _ := os.ReadFile(agentsPath)

	// Third call - still no duplication
	if err := agents.EnsureBlurb(tmpDir); err != nil {
		t.Fatal(err)
	}
	content3, _ := os.ReadFile(agentsPath)

	// All should have exactly one blurb
	for i, content := range [][]byte{content1, content2, content3} {
		count := strings.Count(string(content), agents.BlurbStartMarker)
		if count != 1 {
			t.Errorf("Call %d: expected 1 blurb marker, got %d", i+1, count)
		}
	}
}

// TestAgentsE2E_EdgeCase_LargeFile tests handling of large AGENTS.md files.
func TestAgentsE2E_EdgeCase_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create a large file (100KB)
	var builder strings.Builder
	builder.WriteString("# Large Project Instructions\n\n")
	for i := 0; i < 2000; i++ {
		builder.WriteString("## Section ")
		builder.WriteString(string(rune('0' + i%10)))
		builder.WriteString("\n\nThis is a large section with lots of content to test handling of big files.\n")
		builder.WriteString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.\n\n")
	}
	largeContent := builder.String()

	if err := os.WriteFile(agentsPath, []byte(largeContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should work
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect large file")
	}

	// Append blurb should work
	if err := agents.AppendBlurbToFile(agentsPath); err != nil {
		t.Fatalf("AppendBlurbToFile failed on large file: %v", err)
	}

	// Verify blurb added
	present, err := agents.VerifyBlurbPresent(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Error("Blurb should be present in large file")
	}

	// Verify original content preserved
	content, _ := os.ReadFile(agentsPath)
	if !strings.Contains(string(content), "Lorem ipsum") {
		t.Error("Original content should be preserved")
	}
}

// TestAgentsE2E_EdgeCase_UnicodeContent tests handling of unicode content.
func TestAgentsE2E_EdgeCase_UnicodeContent(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with various unicode characters
	unicodeContent := "# 项目说明 (Project Instructions)\n\n" +
		"## 日本語セクション\n\nコンテンツはここに。\n\n" +
		"## Émojis Section 🚀\n\nWith emojis: 🎉 🔥 💯 🎯\n\n" +
		"## Ελληνικά\n\nΚείμενο στα ελληνικά.\n"

	if err := os.WriteFile(agentsPath, []byte(unicodeContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should work
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect file with unicode content")
	}

	// Append blurb
	if err := agents.AppendBlurbToFile(agentsPath); err != nil {
		t.Fatal(err)
	}

	// Verify unicode content preserved
	content, _ := os.ReadFile(agentsPath)
	contentStr := string(content)
	if !strings.Contains(contentStr, "项目说明") {
		t.Error("Chinese content should be preserved")
	}
	if !strings.Contains(contentStr, "日本語") {
		t.Error("Japanese content should be preserved")
	}
	if !strings.Contains(contentStr, "🚀") {
		t.Error("Emoji should be preserved")
	}
	if !strings.Contains(contentStr, "Ελληνικά") {
		t.Error("Greek content should be preserved")
	}
}

// TestAgentsE2E_EdgeCase_ReadOnlyDirectory tests error handling for read-only scenarios.
func TestAgentsE2E_EdgeCase_ReadOnlyDirectory(t *testing.T) {
	// Skip on Windows where chmod permissions don't work the same way
	if runtime.GOOS == "windows" {
		t.Skip("Skipping permission test on Windows - chmod behavior differs")
	}
	// Skip when running as root (permissions are bypassed)
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test as root")
	}

	tmpDir := t.TempDir()
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readOnlyDir, 0755)

	// EnsureBlurb should fail gracefully on read-only directory
	err := agents.EnsureBlurb(readOnlyDir)
	if err == nil {
		t.Error("EnsureBlurb should fail on read-only directory")
	}
}

// TestAgentsE2E_EdgeCase_SymlinkHandling tests that symlinked files are detected.
// Note: Due to atomic write semantics, the symlink is replaced with a regular file
// after modification. This is expected behavior for data safety - atomic writes
// cannot preserve symlinks.
func TestAgentsE2E_EdgeCase_SymlinkHandling(t *testing.T) {
	tmpDir := t.TempDir()

	// Create real file in subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(subDir, "AGENTS.md")
	if err := os.WriteFile(realPath, []byte("# Real File"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink in main directory
	linkPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skip("Symlinks not supported on this platform")
	}

	// Detection should find the symlinked file
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect symlinked AGENTS.md")
	}

	// Verify content is readable through symlink before modification
	contentBefore, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("Failed to read through symlink: %v", err)
	}
	if !strings.Contains(string(contentBefore), "Real File") {
		t.Error("Should read original content through symlink")
	}

	// Append should succeed (atomic write replaces symlink with regular file)
	if err := agents.AppendBlurbToFile(detection.FilePath); err != nil {
		t.Fatalf("AppendBlurbToFile failed on symlink: %v", err)
	}

	// Verify blurb is in the file at linkPath (now a regular file)
	content, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("Failed to read file after append: %v", err)
	}
	if !strings.Contains(string(content), agents.BlurbStartMarker) {
		t.Error("Blurb should be present in file after append")
	}
	if !strings.Contains(string(content), "Real File") {
		t.Error("Original content should be preserved")
	}

	// Verify linkPath is now a regular file (not a symlink)
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Failed to stat link path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Log("Note: Symlink was preserved (implementation may have changed)")
	}
}

// TestAgentsE2E_EdgeCase_EmptyFile tests handling of empty AGENTS.md.
func TestAgentsE2E_EdgeCase_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create empty file
	if err := os.WriteFile(agentsPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should find file
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect empty AGENTS.md")
	}

	// Append should work
	if err := agents.AppendBlurbToFile(agentsPath); err != nil {
		t.Fatal(err)
	}

	// Verify blurb added
	present, _ := agents.VerifyBlurbPresent(agentsPath)
	if !present {
		t.Error("Blurb should be added to empty file")
	}
}

// TestAgentsE2E_EdgeCase_OnlyWhitespace tests file with only whitespace.
func TestAgentsE2E_EdgeCase_OnlyWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with only whitespace
	if err := os.WriteFile(agentsPath, []byte("   \n\n\t\t\n   "), 0644); err != nil {
		t.Fatal(err)
	}

	// Detection should find file
	detection := agents.DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Should detect whitespace-only AGENTS.md")
	}

	// Append should work
	if err := agents.AppendBlurbToFile(agentsPath); err != nil {
		t.Fatal(err)
	}

	// Verify blurb added
	present, _ := agents.VerifyBlurbPresent(agentsPath)
	if !present {
		t.Error("Blurb should be added")
	}
}
