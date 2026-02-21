package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendBlurbToFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with initial content
	initial := "# My AGENTS.md\n\nSome existing content."
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	// Append blurb
	if err := AppendBlurbToFile(filePath); err != nil {
		t.Fatal(err)
	}

	// Verify
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "Some existing content.") {
		t.Error("Original content was not preserved")
	}
	if !strings.Contains(contentStr, BlurbStartMarker) {
		t.Error("Blurb start marker not found")
	}
	if !strings.Contains(contentStr, BlurbEndMarker) {
		t.Error("Blurb end marker not found")
	}
}

func TestAppendBlurbToEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")

	// Create empty file
	if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Append blurb
	if err := AppendBlurbToFile(filePath); err != nil {
		t.Fatal(err)
	}

	// Verify
	present, err := VerifyBlurbPresent(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Error("Blurb should be present")
	}
}

func TestUpdateBlurbInFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with old blurb (simulated)
	oldContent := "# My AGENTS.md\n\n<!-- bv-agent-instructions-v1 -->\nOld content\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filePath, []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Update blurb
	if err := UpdateBlurbInFile(filePath); err != nil {
		t.Fatal(err)
	}

	// Verify - should have new blurb, only one copy
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	count := strings.Count(contentStr, BlurbStartMarker)
	if count != 1 {
		t.Errorf("Expected exactly 1 blurb marker, got %d", count)
	}
	if !strings.Contains(contentStr, "bd ready") {
		t.Error("Updated blurb should contain current content")
	}
}

func TestRemoveBlurbFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")

	// Create file with blurb
	content := "# My AGENTS.md\n\n" + AgentBlurb + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Remove blurb
	if err := RemoveBlurbFromFile(filePath); err != nil {
		t.Fatal(err)
	}

	// Verify
	newContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(newContent), BlurbStartMarker) {
		t.Error("Blurb should have been removed")
	}
	if !strings.Contains(string(newContent), "# My AGENTS.md") {
		t.Error("Header should still be present")
	}
}

func TestCreateAgentFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")

	// Create new file
	if err := CreateAgentFile(filePath); err != nil {
		t.Fatal(err)
	}

	// Verify file exists with blurb
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "# AI Agent Instructions") {
		t.Error("Expected header")
	}
	if !strings.Contains(contentStr, BlurbStartMarker) {
		t.Error("Expected blurb marker")
	}
}

func TestVerifyBlurbPresent(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("file with blurb", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "with-blurb.md")
		content := "# Test\n\n" + AgentBlurb
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			t.Error("Expected blurb to be present")
		}
	})

	t.Run("file without blurb", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "without-blurb.md")
		if err := os.WriteFile(filePath, []byte("# Test\n\nNo blurb here"), 0644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if present {
			t.Error("Expected blurb to NOT be present")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := VerifyBlurbPresent(filepath.Join(tmpDir, "nonexistent.md"))
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})
}

func TestAtomicWritePreservesPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.md")

	// Create file with specific permissions
	if err := os.WriteFile(filePath, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}

	// Verify initial permissions
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("Initial permissions wrong: %o", info.Mode().Perm())
	}

	// Atomic write
	if err := atomicWrite(filePath, []byte("new content")); err != nil {
		t.Fatal(err)
	}

	// Verify permissions preserved
	info, err = os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Permissions not preserved: expected 0600, got %o", info.Mode().Perm())
	}

	// Verify content changed
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new content" {
		t.Errorf("Content not updated: %s", content)
	}
}

func TestAtomicWriteNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "new-file.md")

	// Write to non-existent file
	if err := atomicWrite(filePath, []byte("brand new")); err != nil {
		t.Fatal(err)
	}

	// Verify file created
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "brand new" {
		t.Errorf("Unexpected content: %s", content)
	}
}

func TestEnsureBlurb(t *testing.T) {
	t.Run("no agent file - creates one", func(t *testing.T) {
		tmpDir := t.TempDir()

		if err := EnsureBlurb(tmpDir); err != nil {
			t.Fatal(err)
		}

		// Should have created AGENTS.md
		detection := DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Expected AGENTS.md to be created")
		}
		if !detection.HasBlurb {
			t.Error("Expected blurb to be present")
		}
	})

	t.Run("agent file exists without blurb - appends", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		if err := os.WriteFile(filePath, []byte("# My Instructions\n\nExisting."), 0644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err != nil {
			t.Fatal(err)
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "Existing.") {
			t.Error("Original content should be preserved")
		}
		if !strings.Contains(string(content), BlurbStartMarker) {
			t.Error("Blurb should be appended")
		}
	})

	t.Run("agent file with current blurb - no change", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n" + AgentBlurb
		if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err != nil {
			t.Fatal(err)
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		// Should not add duplicate
		count := strings.Count(string(content), BlurbStartMarker)
		if count != 1 {
			t.Errorf("Expected exactly 1 blurb, got %d", count)
		}
	})
}

func TestAppendBlurbNonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "nonexistent.md")

	err := AppendBlurbToFile(filePath)
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

func TestAtomicWriteNoPermission(t *testing.T) {
	// Skip on platforms where we can't test permissions properly
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test as root")
	}

	tmpDir := t.TempDir()

	// Create a read-only directory
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readOnlyDir, 0755) // Cleanup

	filePath := filepath.Join(readOnlyDir, "test.md")

	// This should fail because we can't create temp file in read-only dir
	err := atomicWrite(filePath, []byte("test"))
	if err == nil {
		t.Error("Expected error writing to read-only directory")
	}
}
