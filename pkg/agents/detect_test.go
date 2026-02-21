package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectAgentFile(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	t.Run("no agent file", func(t *testing.T) {
		detection := DetectAgentFile(tmpDir)
		if detection.Found() {
			t.Error("Expected no detection in empty directory")
		}
	})

	t.Run("AGENTS.md without blurb", func(t *testing.T) {
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		err := os.WriteFile(agentsPath, []byte("# My Agent Instructions\n\nSome content."), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		detection := DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Expected to find AGENTS.md")
		}
		if detection.FileType != "AGENTS.md" {
			t.Errorf("Expected FileType 'AGENTS.md', got %q", detection.FileType)
		}
		if detection.HasBlurb {
			t.Error("Expected HasBlurb to be false")
		}
		if !detection.NeedsBlurb() {
			t.Error("Expected NeedsBlurb() to return true")
		}
	})

	t.Run("AGENTS.md with blurb", func(t *testing.T) {
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		content := "# My Agent Instructions\n\n" + AgentBlurb
		err := os.WriteFile(agentsPath, []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		detection := DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Expected to find AGENTS.md")
		}
		if !detection.HasBlurb {
			t.Error("Expected HasBlurb to be true")
		}
		if detection.BlurbVersion != BlurbVersion {
			t.Errorf("Expected BlurbVersion %d, got %d", BlurbVersion, detection.BlurbVersion)
		}
		if detection.NeedsBlurb() {
			t.Error("Expected NeedsBlurb() to return false")
		}
	})

	t.Run("CLAUDE.md fallback", func(t *testing.T) {
		claudePath := filepath.Join(tmpDir, "CLAUDE.md")
		err := os.WriteFile(claudePath, []byte("# Claude Instructions"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(claudePath)

		detection := DetectAgentFile(tmpDir)
		if !detection.Found() {
			t.Error("Expected to find CLAUDE.md")
		}
		if detection.FileType != "CLAUDE.md" {
			t.Errorf("Expected FileType 'CLAUDE.md', got %q", detection.FileType)
		}
	})

	t.Run("AGENTS.md preferred over CLAUDE.md", func(t *testing.T) {
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		claudePath := filepath.Join(tmpDir, "CLAUDE.md")
		err := os.WriteFile(agentsPath, []byte("# AGENTS"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		err = os.WriteFile(claudePath, []byte("# CLAUDE"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(claudePath)

		detection := DetectAgentFile(tmpDir)
		if detection.FileType != "AGENTS.md" {
			t.Errorf("Expected AGENTS.md to be preferred, got %q", detection.FileType)
		}
	})
}

func TestAgentFileDetectionMethods(t *testing.T) {
	t.Run("Found", func(t *testing.T) {
		empty := AgentFileDetection{}
		if empty.Found() {
			t.Error("Empty detection should not be found")
		}

		withPath := AgentFileDetection{FilePath: "/some/path"}
		if !withPath.Found() {
			t.Error("Detection with path should be found")
		}
	})

	t.Run("NeedsBlurb", func(t *testing.T) {
		tests := []struct {
			name     string
			det      AgentFileDetection
			expected bool
		}{
			{"empty", AgentFileDetection{}, false},
			{"found without blurb", AgentFileDetection{FilePath: "/path", HasBlurb: false}, true},
			{"found with blurb", AgentFileDetection{FilePath: "/path", HasBlurb: true}, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.det.NeedsBlurb() != tt.expected {
					t.Errorf("NeedsBlurb() = %v, want %v", tt.det.NeedsBlurb(), tt.expected)
				}
			})
		}
	})

	t.Run("NeedsUpgrade", func(t *testing.T) {
		tests := []struct {
			name     string
			det      AgentFileDetection
			expected bool
		}{
			{"no blurb", AgentFileDetection{HasBlurb: false}, false},
			{"current version", AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion}, false},
			{"old version", AgentFileDetection{HasBlurb: true, BlurbVersion: 0}, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.det.NeedsUpgrade() != tt.expected {
					t.Errorf("NeedsUpgrade() = %v, want %v", tt.det.NeedsUpgrade(), tt.expected)
				}
			})
		}
	})
}

func TestDetectAgentFileInParents(t *testing.T) {
	// Create nested temporary directories
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	subSubDir := filepath.Join(subDir, "subsub")
	err := os.MkdirAll(subSubDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("find in parent", func(t *testing.T) {
		// Create AGENTS.md in root
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		err := os.WriteFile(agentsPath, []byte("# Root AGENTS"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		// Search from subsubdir
		detection := DetectAgentFileInParents(subSubDir, 3)
		if !detection.Found() {
			t.Error("Expected to find AGENTS.md in parent")
		}
		if detection.FilePath != agentsPath {
			t.Errorf("Expected FilePath %q, got %q", agentsPath, detection.FilePath)
		}
	})

	t.Run("prefer closer parent", func(t *testing.T) {
		// Create AGENTS.md in both root and sub
		rootAgents := filepath.Join(tmpDir, "AGENTS.md")
		subAgents := filepath.Join(subDir, "AGENTS.md")

		err := os.WriteFile(rootAgents, []byte("# Root"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(rootAgents)

		err = os.WriteFile(subAgents, []byte("# Sub"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(subAgents)

		// Search from subsubdir - should find sub's file first
		detection := DetectAgentFileInParents(subSubDir, 3)
		if detection.FilePath != subAgents {
			t.Errorf("Expected to find closer AGENTS.md at %q, got %q", subAgents, detection.FilePath)
		}
	})

	t.Run("respect maxLevels", func(t *testing.T) {
		// Create AGENTS.md only in root
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		err := os.WriteFile(agentsPath, []byte("# Root"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		// Search with maxLevels=1 should not find root from subsubdir
		detection := DetectAgentFileInParents(subSubDir, 1)
		if detection.Found() {
			t.Error("Expected not to find file with limited maxLevels")
		}
	})
}

func TestAgentFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("no file", func(t *testing.T) {
		if AgentFileExists(tmpDir) {
			t.Error("Expected false for empty directory")
		}
	})

	t.Run("with AGENTS.md", func(t *testing.T) {
		agentsPath := filepath.Join(tmpDir, "AGENTS.md")
		err := os.WriteFile(agentsPath, []byte("# AGENTS"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(agentsPath)

		if !AgentFileExists(tmpDir) {
			t.Error("Expected true when AGENTS.md exists")
		}
	})
}

func TestGetPreferredAgentFilePath(t *testing.T) {
	path := GetPreferredAgentFilePath("/my/project")
	expected := filepath.Join("/my/project", "AGENTS.md")
	if path != expected {
		t.Errorf("GetPreferredAgentFilePath() = %q, want %q", path, expected)
	}
}

func TestDetectAgentFileWithLegacyBlurb(t *testing.T) {
	tmpDir := t.TempDir()

	// Legacy blurb content (pre-v1 format without HTML markers)
	legacyContent := `# Project AGENTS.md

### Using bv as an AI sidecar

bv can help AI agents with task management.

**Available robot flags**:
- ` + "`--robot-insights`" + ` - Deep analysis
- ` + "`--robot-plan`" + ` - Generate plans

bv already computes the hard parts for you.
`

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	err := os.WriteFile(agentsPath, []byte(legacyContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Expected to find AGENTS.md")
	}
	if !detection.HasBlurb {
		t.Error("Expected HasBlurb to be true for legacy content")
	}
	if !detection.HasLegacyBlurb {
		t.Error("Expected HasLegacyBlurb to be true")
	}
	if !detection.NeedsUpgrade() {
		t.Error("Expected NeedsUpgrade() to return true for legacy blurb")
	}
}

func TestDetectAgentFileNotFalsePositive(t *testing.T) {
	// Regression test: content that references robot flags but is NOT the legacy blurb
	// should NOT be detected as legacy (missing "bv already computes the hard parts")
	tmpDir := t.TempDir()

	// This mimics current AGENTS.md which has header + robot flags but NOT the legacy end phrase
	notLegacyContent := `### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects.

**Commands:**
- ` + "`--robot-insights`" + ` - Full metrics
- ` + "`--robot-plan`" + ` - Parallel execution tracks

Use bv instead of parsing beads.jsonl.
`

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	err := os.WriteFile(agentsPath, []byte(notLegacyContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Error("Expected to find AGENTS.md")
	}
	if detection.HasLegacyBlurb {
		t.Error("Expected HasLegacyBlurb to be false - this is NOT legacy content")
	}
	if detection.HasBlurb {
		t.Error("Expected HasBlurb to be false - no current or legacy blurb markers")
	}
}
