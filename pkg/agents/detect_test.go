package agents

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestDetectAgentFileDoesNotFollowSymlink(t *testing.T) {
	workDir := t.TempDir()
	externalDir := t.TempDir()
	targetPath := filepath.Join(externalDir, "external-instructions.md")
	targetContent := []byte("private external bytes\n" + AgentBlurb)
	if err := os.WriteFile(targetPath, targetContent, 0o600); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workDir, "AGENTS.md")
	if err := os.Symlink(targetPath, agentPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	detection := DetectAgentFile(workDir)
	if !detection.Found() || detection.FilePath != agentPath || detection.FileType != "AGENTS.md" {
		t.Fatalf("DetectAgentFile()=%+v, want found symlink metadata", detection)
	}
	if detection.Content != "" || detection.HasBlurb || detection.HasLegacyBlurb || detection.BlurbVersion != 0 || detection.BlurbCount != 0 || detection.BlurbStructureError != "" {
		t.Fatalf("DetectAgentFile() exposed or analyzed symlink target: %+v", detection)
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, targetContent) {
		t.Fatalf("symlink target changed: got %q, want %q", after, targetContent)
	}
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
			{"found with malformed markers", AgentFileDetection{FilePath: "/path", BlurbStructureError: "bad markers"}, false},
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
			{"current version", AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion, BlurbCount: 1}, false},
			{"old version", AgentFileDetection{HasBlurb: true, BlurbVersion: 0}, true},
			{"future version", AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion + 1, BlurbCount: 1}, true},
			{"malformed current version", AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion, BlurbStructureError: "bad markers"}, true},
			{"duplicate current version", AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion, BlurbCount: 2}, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.det.NeedsUpgrade() != tt.expected {
					t.Errorf("NeedsUpgrade() = %v, want %v", tt.det.NeedsUpgrade(), tt.expected)
				}
			})
		}
	})

	t.Run("HasFutureBlurb", func(t *testing.T) {
		if (AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion}).HasFutureBlurb() {
			t.Fatal("current blurb reported as future")
		}
		if !(AgentFileDetection{HasBlurb: true, BlurbVersion: BlurbVersion + 1}).HasFutureBlurb() {
			t.Fatal("newer blurb was not reported as future")
		}
	})
}

func TestDetectAgentFileReportsMalformedAndDuplicateBlurbs(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantHasBlurb  bool
		wantCount     int
		wantMalformed bool
		wantDuplicate bool
	}{
		{
			name:          "unterminated current blurb",
			content:       "# Header\n\n<!-- bv-agent-instructions-v4 -->\ncontent",
			wantHasBlurb:  true,
			wantMalformed: true,
		},
		{
			name:          "stray end marker",
			content:       "# Header\n\n<!-- end-bv-agent-instructions -->",
			wantMalformed: true,
		},
		{
			name: "duplicate current blurbs",
			content: "<!-- bv-agent-instructions-v4 -->\none\n<!-- end-bv-agent-instructions -->\n" +
				"<!-- bv-agent-instructions-v4 -->\ntwo\n<!-- end-bv-agent-instructions -->",
			wantHasBlurb:  true,
			wantCount:     2,
			wantDuplicate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "AGENTS.md")
			if err := os.WriteFile(filePath, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			detection := DetectAgentFile(tmpDir)
			if detection.HasBlurb != tt.wantHasBlurb {
				t.Fatalf("HasBlurb=%v, want %v", detection.HasBlurb, tt.wantHasBlurb)
			}
			if detection.BlurbCount != tt.wantCount {
				t.Fatalf("BlurbCount=%d, want %d", detection.BlurbCount, tt.wantCount)
			}
			if detection.HasMalformedBlurb() != tt.wantMalformed {
				t.Fatalf("HasMalformedBlurb()=%v, want %v (error=%q)", detection.HasMalformedBlurb(), tt.wantMalformed, detection.BlurbStructureError)
			}
			if detection.HasDuplicateBlurbs() != tt.wantDuplicate {
				t.Fatalf("HasDuplicateBlurbs()=%v, want %v", detection.HasDuplicateBlurbs(), tt.wantDuplicate)
			}
			if !detection.NeedsUpgrade() {
				t.Fatal("malformed or duplicate blurb should need update/normalization")
			}
			if detection.NeedsBlurb() {
				t.Fatal("malformed marker structure must not be treated as a safe append target")
			}
		})
	}
}

func TestDetectAgentFileReportsMarkdownAnalysisLimit(t *testing.T) {
	tmpDir := t.TempDir()
	content := "[reference]: " + strings.Repeat("(", maxCommonMarkDestinationParenDepth+1) + "\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.Found() || !detection.HasBlurb || !detection.HasMalformedBlurb() {
		t.Fatalf("DetectAgentFile()=%+v, want a found, fail-closed malformed detection", detection)
	}
	if detection.HasLegacyBlurb || detection.BlurbCount != 0 || detection.BlurbVersion != 0 {
		t.Fatalf("DetectAgentFile() asserted unsupported blurb details under analysis uncertainty: %+v", detection)
	}
	if !strings.Contains(detection.BlurbStructureError, "markdown analysis limit exceeded") {
		t.Fatalf("BlurbStructureError=%q, want explicit analysis-limit diagnostic", detection.BlurbStructureError)
	}
}

func TestDetectAgentFileReportsHighestAndFutureBlurbVersion(t *testing.T) {
	tmpDir := t.TempDir()
	content := "<!-- bv-agent-instructions-v4 -->\ncurrent\n<!-- end-bv-agent-instructions -->\n" +
		"<!-- bv-agent-instructions-v7 -->\nfuture\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if detection.BlurbVersion != 7 {
		t.Fatalf("BlurbVersion=%d, want highest version 7", detection.BlurbVersion)
	}
	if !detection.HasFutureBlurb() || !detection.NeedsUpgrade() {
		t.Fatalf("future detection=%+v, want future and needs-attention state", detection)
	}
}

func TestDetectAgentFileReportsFutureBlurbRevealedByLegacyRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	content := LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v11 -->\n" +
		"```bash\nfuture command\n```\n" +
		"future\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if detection.BlurbVersion != 11 || !detection.HasFutureBlurb() {
		t.Fatalf("future detection behind legacy fence=%+v, want version 11 future state", detection)
	}
	if detection.HasMalformedBlurb() || detection.BlurbCount != 1 {
		t.Fatalf("revealed future structure=%+v, want one complete block", detection)
	}
}

func TestDetectAgentFileCompleteFutureBlockPrecedesLaterMalformedMarker(t *testing.T) {
	futureVersion := BlurbVersion + 8
	futureBlockWithStrayEnd := fmt.Sprintf("<!-- bv-agent-instructions-v%d -->\nfuture instructions\n%s\n%s\n", futureVersion, BlurbEndMarker, BlurbEndMarker)
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "direct view",
			content: futureBlockWithStrayEnd,
		},
		{
			name: "legacy-revealed conservative view",
			content: LegacyBlurbContent + "\n" +
				BlurbStartMarker + "\ncurrent physical block\n" + BlurbEndMarker + "\n" +
				"```\n" +
				futureBlockWithStrayEnd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			detection := DetectAgentFile(tmpDir)
			if !detection.HasFutureBlurb() {
				t.Fatalf("detection=%+v, want complete future block classification", detection)
			}
			if detection.HasMalformedBlurb() {
				t.Fatalf("lower-precedence marker error survived complete future block: %+v", detection)
			}
			if detection.BlurbVersion != futureVersion || detection.BlurbCount != 1 {
				t.Fatalf("future detection version/count=%d/%d, want %d/1", detection.BlurbVersion, detection.BlurbCount, futureVersion)
			}
		})
	}
}

func TestDetectAgentFileReportsAmbiguousCurrentBlurbAsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	content := LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v4 -->\n" +
		"```bash\ncurrent command\n```\n" +
		"current\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.HasMalformedBlurb() || !detection.NeedsUpgrade() {
		t.Fatalf("ambiguous legacy/current detection=%+v, want fail-closed malformed state", detection)
	}
	if detection.HasFutureBlurb() {
		t.Fatalf("current-version fenced content was misclassified as future: %+v", detection)
	}
}

func TestDetectAgentFileAnalyzesEveryPreservedAmbiguousLegacyFence(t *testing.T) {
	tests := []struct {
		name          string
		version       int
		body          string
		wantFuture    bool
		wantMalformed bool
	}{
		{name: "current simple body", version: BlurbVersion, body: "current instructions\n", wantMalformed: true},
		{name: "current bare fenced body", version: BlurbVersion, body: "```\ncurrent command\n```\ncurrent instructions\n", wantMalformed: true},
		{name: "future simple body", version: BlurbVersion + 7, body: "future instructions\n", wantFuture: true},
		{name: "future bare fenced body", version: BlurbVersion + 7, body: "```\nfuture command\n```\nfuture instructions\n", wantFuture: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			content := LegacyBlurbContent + "\n" +
				fmt.Sprintf("<!-- bv-agent-instructions-v%d -->\n", tt.version) +
				tt.body + BlurbEndMarker + "\n"
			if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			detection := DetectAgentFile(tmpDir)
			if detection.HasFutureBlurb() != tt.wantFuture {
				t.Fatalf("HasFutureBlurb()=%v, want %v: %+v", detection.HasFutureBlurb(), tt.wantFuture, detection)
			}
			if detection.HasMalformedBlurb() != tt.wantMalformed {
				t.Fatalf("HasMalformedBlurb()=%v, want %v: %+v", detection.HasMalformedBlurb(), tt.wantMalformed, detection)
			}
			if tt.wantFuture && (detection.BlurbVersion != tt.version || detection.BlurbCount != 1) {
				t.Fatalf("future detection version/count=%d/%d, want %d/1", detection.BlurbVersion, detection.BlurbCount, tt.version)
			}
		})
	}
}

func TestDetectAgentFileRejectsAmbiguousLegacyInterpretations(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantError string
	}{
		{
			name:      "same-version physical blocks swap visibility",
			content:   ambiguousSameVersionBlockSwapContent(),
			wantError: "ambiguous marker material",
		},
		{
			name:      "legacy removal count diverges",
			content:   ambiguousTwoLegacyBlocksContent(),
			wantError: "removal count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			detection := DetectAgentFile(tmpDir)
			if !detection.HasMalformedBlurb() || !strings.Contains(detection.BlurbStructureError, tt.wantError) {
				t.Fatalf("detection=%+v, want malformed error containing %q", detection, tt.wantError)
			}
			if detection.HasFutureBlurb() {
				t.Fatalf("ambiguous current/legacy content was misclassified as future: %+v", detection)
			}
		})
	}
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
