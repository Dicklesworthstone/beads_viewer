package agents

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestAppendBlurbToFileRejectsMalformedMarkersWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n\n<!-- end-bv-agent-instructions -->\nUser instructions"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := AppendBlurbToFile(filePath); err == nil {
		t.Fatal("expected malformed marker error")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("malformed append changed file:\n got: %q\nwant: %q", content, original)
	}
}

func TestAppendBlurbToFileRejectsEOFOpenFenceWithoutWriting(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{name: "tilde fence", original: "# Header\n\n~~~~markdown\nexample continues to EOF\n"},
		{name: "backtick fence", original: "# Header\n\n```markdown\nexample continues to EOF\n"},
		{name: "long backtick fence", original: "# Header\n\n````markdown\nexample continues to EOF\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "AGENTS.md")
			if err := os.WriteFile(filePath, []byte(tt.original), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := AppendBlurbToFile(filePath); err == nil {
				t.Fatal("expected EOF-open fence validation error")
			}
			got, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.original {
				t.Fatalf("append changed EOF-fenced content:\n got: %q\nwant: %q", got, tt.original)
			}
		})
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
	if !strings.Contains(contentStr, "br ready") {
		t.Error("Updated blurb should contain current content")
	}
}

func TestUpdateBlurbInFileRejectsMalformedMarkersWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n\n<!-- bv-agent-instructions-v1 -->\nUser instructions without an end marker"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateBlurbInFile(filePath); err == nil {
		t.Fatal("expected malformed marker error")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("malformed update changed file:\n got: %q\nwant: %q", content, original)
	}

	if err := UpdateBlurbInFile(filePath); err == nil {
		t.Fatal("expected repeated malformed marker error")
	}
	content, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("repeated malformed update changed file:\n got: %q\nwant: %q", content, original)
	}
}

func TestUpdateBlurbInFileRejectsFutureVersionWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n\n<!-- bv-agent-instructions-v9 -->\nnewer\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := UpdateBlurbInFile(filePath)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("UpdateBlurbInFile() error=%v, want future-version refusal", err)
	}
	got, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("future-version update changed file:\n got: %q\nwant: %q", got, original)
	}
}

func TestUpdateBlurbInFileRejectsFutureVersionRevealedByLegacyRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n\n" + LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v9 -->\n" +
		"```bash\nfuture command\n```\n" +
		"newer\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := UpdateBlurbInFile(filePath)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("UpdateBlurbInFile() error=%v, want revealed future-version refusal", err)
	}
	got, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("revealed future-version update changed file:\n got: %q\nwant: %q", got, original)
	}
}

func TestUpdateBlurbInFileRejectsEOFOpenFenceWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n\n" +
		"<!-- bv-agent-instructions-v3 -->\nold instructions\n<!-- end-bv-agent-instructions -->\n\n" +
		"```markdown\nuser example continues to EOF\n"
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UpdateBlurbInFile(filePath); err == nil || !strings.Contains(err.Error(), "validate updated") {
		t.Fatalf("UpdateBlurbInFile() error=%v, want EOF-open fence validation failure", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("EOF-open fence update changed file:\n got: %q\nwant: %q", got, original)
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

func TestRemoveBlurbFromFileRemovesLegacyAndDuplicateVersionedBlocks(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := `# Header

### Using bv as an AI sidecar

--robot-insights
--robot-plan
bv already computes the hard parts for you.

## Preserve Me
`
		if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveBlurbFromFile(filePath); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "bv already computes the hard parts") {
			t.Fatalf("legacy blurb was not removed:\n%s", content)
		}
		if !strings.Contains(string(content), "# Header") || !strings.Contains(string(content), "## Preserve Me") {
			t.Fatalf("legacy removal lost surrounding content:\n%s", content)
		}
	})

	t.Run("multiple versioned", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# Header\n\n" +
			"<!-- bv-agent-instructions-v4 -->\none\n<!-- end-bv-agent-instructions -->\n\n" +
			"Preserve between.\n\n" +
			"<!-- bv-agent-instructions-v4 -->\ntwo\n<!-- end-bv-agent-instructions -->\n\n# Footer\n"
		if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveBlurbFromFile(filePath); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), blurbStartPrefix) || strings.Contains(string(content), BlurbEndMarker) {
			t.Fatalf("not all versioned blurbs were removed:\n%s", content)
		}
		for _, preserved := range []string{"# Header", "Preserve between.", "# Footer"} {
			if !strings.Contains(string(content), preserved) {
				t.Fatalf("versioned removal lost %q:\n%s", preserved, content)
			}
		}
	})
}

func TestRemoveBlurbFromFileRejectsMalformedMarkersWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := "# Header\n<!-- bv-agent-instructions-v1 -->\nUser instructions\n" +
		"<!-- bv-agent-instructions-v4 -->\nMore user instructions\n<!-- end-bv-agent-instructions -->\n# Footer"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveBlurbFromFile(filePath); err == nil {
		t.Fatal("expected malformed marker error")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("malformed removal changed file:\n got: %q\nwant: %q", content, original)
	}
}

func TestAgentFileOperationsRejectMarkdownAnalysisLimitWithoutWriting(t *testing.T) {
	original := "[reference]: " + strings.Repeat("(", maxCommonMarkDestinationParenDepth+1) + "\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	operations := []struct {
		name string
		run  func(string) error
	}{
		{name: "append", run: AppendBlurbToFile},
		{name: "update", run: UpdateBlurbInFile},
		{name: "remove", run: RemoveBlurbFromFile},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := operation.run(filePath); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
				t.Fatalf("%s error=%v, want explicit analysis-limit refusal", operation.name, err)
			}
			got, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Fatalf("%s changed indeterminate content:\n got: %q\nwant: %q", operation.name, got, original)
			}
		})
	}

	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if present, err := VerifyBlurbPresent(filePath); err == nil || present || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("VerifyBlurbPresent() present=%v error=%v, want explicit analysis-limit refusal", present, err)
	}
}

func TestAppendBlurbToFileRejectsAnalysisDenseMaximumSizeWithoutWriting(t *testing.T) {
	tests := []struct {
		name     string
		original []byte
	}{
		{name: "physical lines", original: bytes.Repeat([]byte{'\n'}, maxAgentFileBytes)},
		{name: "nested containers", original: bytes.Repeat([]byte{'>', ' '}, maxAgentFileBytes/2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "AGENTS.md")
			if len(tt.original) != maxAgentFileBytes {
				t.Fatalf("fixture size=%d, want exact accepted file limit %d", len(tt.original), maxAgentFileBytes)
			}
			if err := os.WriteFile(filePath, tt.original, 0o600); err != nil {
				t.Fatal(err)
			}

			err := AppendBlurbToFile(filePath)
			if err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
				t.Fatalf("AppendBlurbToFile() error=%v, want explicit Markdown-budget refusal", err)
			}
			got, readErr := os.ReadFile(filePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(got, tt.original) {
				t.Fatalf("analysis-budget refusal changed exact-limit file: got %d bytes, want original %d bytes", len(got), len(tt.original))
			}
		})
	}
}

func TestRemoveBlurbFromFileRejectsFutureVersionWithoutWriting(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{
			name:     "direct",
			original: "# Header\n<!-- bv-agent-instructions-v12 -->\nnewer\n<!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "revealed after legacy removal with fenced example",
			original: "# Header\n\n" + LegacyBlurbContent + "\n" +
				"<!-- bv-agent-instructions-v12 -->\n" +
				"```bash\nfuture command\n```\n" +
				"newer\n<!-- end-bv-agent-instructions -->\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "AGENTS.md")
			if err := os.WriteFile(filePath, []byte(tt.original), 0o600); err != nil {
				t.Fatal(err)
			}

			err := RemoveBlurbFromFile(filePath)
			if err == nil || !strings.Contains(err.Error(), "refusing to remove") {
				t.Fatalf("RemoveBlurbFromFile() error=%v, want future-version refusal", err)
			}
			got, readErr := os.ReadFile(filePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != tt.original {
				t.Fatalf("future-version removal changed file:\n got: %q\nwant: %q", got, tt.original)
			}
		})
	}
}

func TestRemoveBlurbFromFilePreservesImmediatelyAdjacentUserFence(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	userCode := "```\nuser code must retain both fences\n```\n"
	original := "# Header\n\n" + legacy + userCode
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveBlurbFromFile(filePath); err != nil {
		t.Fatalf("RemoveBlurbFromFile failed: %v", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), userCode) {
		t.Fatalf("file removal changed adjacent user fence:\n got: %q\nwant suffix: %q", got, userCode)
	}
}

func TestRemoveBlurbFromFilePreservesUnclosedFenceWhoseBodyStartsWithHeading(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	userCode := "```\n## literal heading inside unfinished fence\n"
	original := "# Header\n\n" + legacy + userCode
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveBlurbFromFile(filePath); err != nil {
		t.Fatalf("RemoveBlurbFromFile failed: %v", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), userCode) {
		t.Fatalf("file removal changed unclosed fenced heading:\n got: %q\nwant suffix: %q", got, userCode)
	}
}

func TestEnsureBlurbRejectsAmbiguousLegacyFenceWithoutWriting(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v4 -->\n" +
		"```bash\ncurrent command\n```\n" +
		"current\n<!-- end-bv-agent-instructions -->\n"
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBlurb(tmpDir); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("EnsureBlurb error=%v, want fail-closed malformed result", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("EnsureBlurb changed ambiguous fenced content:\n got: %q\nwant: %q", got, original)
	}
}

func TestFileMutationsRejectAmbiguousLegacyInterpretationsWithoutWriting(t *testing.T) {
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
	mutations := []struct {
		name string
		run  func(string) error
	}{
		{name: "update", run: UpdateBlurbInFile},
		{name: "remove", run: RemoveBlurbFromFile},
	}

	for _, tt := range tests {
		for _, mutation := range mutations {
			t.Run(tt.name+"/"+mutation.name, func(t *testing.T) {
				tmpDir := t.TempDir()
				filePath := filepath.Join(tmpDir, "AGENTS.md")
				if err := os.WriteFile(filePath, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}

				if err := mutation.run(filePath); err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("%s error=%v, want %q", mutation.name, err, tt.wantError)
				}
				got, err := os.ReadFile(filePath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, []byte(tt.content)) {
					t.Fatalf("%s changed ambiguous file bytes:\n got: %q\nwant: %q", mutation.name, got, tt.content)
				}
			})
		}
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

func TestCreateAgentFileDoesNotReplaceExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	original := []byte("# Existing instructions\n\nDo not overwrite me.\n")
	if err := os.WriteFile(filePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := CreateAgentFile(filePath)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("CreateAgentFile() error=%v, want os.ErrExist", err)
	}
	got, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("CreateAgentFile() replaced existing content:\n got: %q\nwant: %q", got, original)
	}
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("existing mode=%o after failed create, want 600", info.Mode().Perm())
	}
	matches, globErr := filepath.Glob(filepath.Join(tmpDir, ".bv-create-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("unpublished files=%v, want none when destination existed before create", matches)
	}
}

func TestWriteNewFileExclusiveUsesNoReplacementCreate(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	want := []byte("# Complete instructions\n")

	if err := writeNewFileExclusive(filePath, want); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("created content=%q, want %q", got, want)
	}

	err = writeNewFileExclusive(filePath, []byte("replacement"))
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing-path error=%v, want os.ErrExist", err)
	}
	got, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("exclusive create replaced existing content: got %q, want %q", got, want)
	}
}

func TestWriteNewFileExclusiveReportsPublishedStateWhenPrivateLinkCleanupFails(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	want := []byte("# Complete instructions\n")

	originalPublish := publishAgentFileExclusiveForCreate
	publishAgentFileExclusiveForCreate = func(sourcePath, destinationPath string) (bool, error) {
		if err := os.Link(sourcePath, destinationPath); err != nil {
			t.Skipf("filesystem cannot emulate link-published cleanup failure: %v", err)
			return false, err
		}
		return true, errors.New("injected private-link cleanup failure")
	}
	defer func() { publishAgentFileExclusiveForCreate = originalPublish }()

	err := writeNewFileExclusive(filePath, want)
	if err == nil || !strings.Contains(err.Error(), "destination was published") || strings.Contains(err.Error(), "unpublished file retained") {
		t.Fatalf("writeNewFileExclusive() error=%v, want honest partial-publication diagnostic", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("published content=%q, want %q", got, want)
	}
	matches, err := filepath.Glob(filepath.Join(tmpDir, ".bv-create-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained private links=%v, want exactly one recovery name", matches)
	}
	destinationInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(destinationInfo, privateInfo) {
		t.Fatal("retained private name is not the published destination inode")
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

	t.Run("malformed current blurb", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "malformed-blurb.md")
		content := "<!-- bv-agent-instructions-v4 -->\nmissing end marker"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err == nil {
			t.Fatal("expected malformed marker error")
		}
		if present {
			t.Fatal("malformed marker must not verify as a present blurb")
		}
	})

	t.Run("duplicate current blurbs", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "duplicate-blurb.md")
		content := AgentBlurb + "\n\n" + AgentBlurb
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err == nil {
			t.Fatal("expected duplicate marker error")
		}
		if present {
			t.Fatal("duplicate blocks must not verify as one healthy blurb")
		}
	})

	t.Run("older blurb does not verify as current", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "old-blurb.md")
		content := "<!-- bv-agent-instructions-v3 -->\nold\n<!-- end-bv-agent-instructions -->"
		if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err == nil || present {
			t.Fatalf("VerifyBlurbPresent() present=%v err=%v, want false and version error", present, err)
		}
	})

	t.Run("fenced marker example does not verify", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "fenced-example.md")
		content := "```markdown\n<!-- bv-agent-instructions-v4 -->\nexample\n<!-- end-bv-agent-instructions -->\n```"
		if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err != nil || present {
			t.Fatalf("VerifyBlurbPresent() present=%v err=%v, want false, nil", present, err)
		}
	})

	t.Run("current and legacy blurbs", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "current-and-legacy.md")
		content := AgentBlurb + "\n\n" + LegacyBlurbContent
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		present, err := VerifyBlurbPresent(filePath)
		if err == nil {
			t.Fatal("expected remaining legacy blurb error")
		}
		if present {
			t.Fatal("versioned and legacy blocks must not verify as one healthy blurb")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := VerifyBlurbPresent(filepath.Join(tmpDir, "nonexistent.md"))
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})
}

func TestWriteReplacementPreservesPermissions(t *testing.T) {
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("Initial permissions wrong: %o", info.Mode().Perm())
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	// Same-directory replacement of the locked, still-current source.
	if err := locked.replace([]byte("new content")); err != nil {
		t.Fatal(err)
	}

	// Verify permissions preserved
	info, err = os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
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

func TestAppendBlurbRejectsSymlinkWithoutUpdatingTarget(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "instructions.md")
	linkPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(targetPath, []byte("# Original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("instructions.md", linkPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	if err := AppendBlurbToFile(linkPath); err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("AppendBlurbToFile error=%v, want symbolic-link refusal", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("AppendBlurbToFile replaced the symlink instead of its target")
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# Original\n" {
		t.Fatalf("symlink target changed despite refusal:\n%s", content)
	}
}

func TestWriteReplacementRejectsConcurrentSymlinkRetarget(t *testing.T) {
	tmpDir := t.TempDir()
	secondTarget := filepath.Join(tmpDir, "second.md")
	linkPath := filepath.Join(tmpDir, "AGENTS.md")
	newLinkPath := filepath.Join(tmpDir, "new-link")
	if err := os.WriteFile(secondTarget, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := lockAgentFileForMutation(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := os.Symlink(secondTarget, newLinkPath); err != nil {
		t.Skipf("cannot create replacement symlink: %v", err)
	}
	if err := os.Rename(newLinkPath, linkPath); err != nil {
		t.Skipf("platform cannot atomically retarget a symlink: %v", err)
	}

	if err := locked.replace([]byte("bv replacement")); !errors.Is(err, errAgentFileChanged) {
		t.Fatalf("replace error=%v, want symlink-change refusal", err)
	}
	got, err := os.ReadFile(secondTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("retarget race changed %s: got %q, want %q", secondTarget, got, "second")
	}
}

func TestVerifyRequestedPathRejectsRegularFileChangedToSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	requestedPath := filepath.Join(tmpDir, "AGENTS.md")
	displacedPath := filepath.Join(tmpDir, "AGENTS.displaced.md")
	victimPath := filepath.Join(tmpDir, "victim.md")
	if err := os.WriteFile(requestedPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victimPath, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	initialInfo, err := os.Lstat(requestedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(requestedPath, displacedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimPath, requestedPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	victimInfo, err := os.Stat(victimPath)
	if err != nil {
		t.Fatal(err)
	}

	locked := &lockedAgentFile{
		requestedPath: requestedPath,
		requestedInfo: initialInfo,
	}
	if err := locked.verifyRequestedPath(victimInfo); !errors.Is(err, errAgentFileChanged) {
		t.Fatalf("verifyRequestedPath error=%v, want regular-to-symlink refusal", err)
	}
	content, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "victim" {
		t.Fatalf("victim content changed: %q", content)
	}
}

func TestWriteReplacementRejectsConcurrentByteChange(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	// A lock-ignoring writer changes a byte while bv holds its cooperative
	// process lock. Byte verification must still refuse to overwrite the edit.
	editor, err := os.OpenFile(filePath, os.O_WRONLY, 0)
	if err != nil {
		if runtime.GOOS == "windows" {
			// The Windows lock intentionally denies new write-sharing handles, which
			// is a stronger form of the same no-overwrite guarantee.
			content, readErr := os.ReadFile(filePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(content) != "original" {
				t.Fatalf("source changed after denied concurrent writer: %q", content)
			}
			return
		}
		t.Fatal(err)
	}
	if _, err := editor.WriteAt([]byte("X"), 1); err != nil {
		_ = editor.Close()
		t.Fatal(err)
	}
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := locked.replace([]byte("bv replacement")); !errors.Is(err, errAgentFileChanged) {
		t.Fatalf("replace error=%v, want concurrent-change refusal", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "oXiginal" {
		t.Fatalf("concurrent bytes were overwritten: %q", content)
	}
}

func TestWriteReplacementRejectsConcurrentIdentityChange(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	editorPath := filepath.Join(tmpDir, "editor-save")
	if err := os.WriteFile(editorPath, []byte("concurrent atomic save"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(editorPath, filePath); err != nil {
		t.Skipf("platform does not permit replacing a locked destination: %v", err)
	}
	if err := locked.replace([]byte("bv replacement")); !errors.Is(err, errAgentFileChanged) {
		t.Fatalf("replace error=%v, want destination-identity refusal", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "concurrent atomic save" {
		t.Fatalf("concurrent replacement was overwritten: %q", content)
	}
}

func TestWriteReplacementRejectsHardLinkedFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	aliasPath := filepath.Join(tmpDir, "AGENTS.alias.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filePath, aliasPath); err != nil {
		t.Skipf("filesystem does not support hard links: %v", err)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("bv replacement")); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("replace error=%v, want hard-link refusal", err)
	}

	for _, path := range []string{filePath, aliasPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "original" {
			t.Fatalf("hard-linked file %s changed: %q", path, content)
		}
	}
}

func TestAppendBlurbRejectsOversizedAgentFileWithoutReadingOrWritingIt(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSize := int64(maxAgentFileBytes + 1)
	if err := os.Truncate(filePath, wantSize); err != nil {
		t.Fatal(err)
	}

	err := AppendBlurbToFile(filePath)
	if !errors.Is(err, errAgentFileTooLarge) {
		t.Fatalf("AppendBlurbToFile() error=%v, want safe-size refusal", err)
	}
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Size() != wantSize {
		t.Fatalf("oversized source size=%d after refusal, want %d", info.Size(), wantSize)
	}
}

func TestEnsureBlurbDetectsOversizedAgentFileWithoutReadingOrWritingIt(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSize := int64(maxAgentFileBytes + 1)
	if err := os.Truncate(filePath, wantSize); err != nil {
		t.Fatal(err)
	}

	detection := DetectAgentFile(tmpDir)
	if !detection.Found() {
		t.Fatal("DetectAgentFile() did not report the oversized AGENTS.md")
	}
	if detection.Content != "" {
		t.Fatalf("DetectAgentFile() retained %d oversized bytes, want no content", len(detection.Content))
	}

	err := EnsureBlurb(tmpDir)
	if !errors.Is(err, errAgentFileTooLarge) {
		t.Fatalf("EnsureBlurb() error=%v, want safe-size refusal", err)
	}
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Size() != wantSize {
		t.Fatalf("oversized source size=%d after refusal, want %d", info.Size(), wantSize)
	}
}

func TestRemoveAgentReplacementIfSameRetainsCandidateAndPeerPath(t *testing.T) {
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, ".bv-replace-candidate")
	if err := os.WriteFile(candidatePath, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidateInfo, err := os.Lstat(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	removeAgentReplacementIfSame(candidatePath, candidateInfo)
	if content, err := os.ReadFile(candidatePath); err != nil || string(content) != "candidate" {
		t.Fatalf("matching recovery candidate was removed or changed: content=%q err=%v", content, err)
	}

	preservedPath := filepath.Join(dir, "preserved-candidate")
	if err := os.Rename(candidatePath, preservedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, []byte("peer"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeAgentReplacementIfSame(candidatePath, candidateInfo)
	if content, err := os.ReadFile(candidatePath); err != nil || string(content) != "peer" {
		t.Fatalf("peer path was removed or changed: content=%q err=%v", content, err)
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

	t.Run("malformed current blurb - errors without writing", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n<!-- bv-agent-instructions-v4 -->\nunterminated user content"
		if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("EnsureBlurb error=%v, want explicit malformed-blurb refusal", err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != original {
			t.Fatalf("EnsureBlurb changed malformed content:\n got: %q\nwant: %q", content, original)
		}
	})

	t.Run("future blurb - refuses downgrade without writing", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n<!-- bv-agent-instructions-v999 -->\nfuture instructions\n<!-- end-bv-agent-instructions -->\n\nPreserve exactly.\n"
		if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err == nil || !strings.Contains(err.Error(), "refusing to downgrade") {
			t.Fatalf("EnsureBlurb error=%v, want explicit future-blurb refusal", err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != original {
			t.Fatalf("EnsureBlurb changed future-version content:\n got: %q\nwant: %q", content, original)
		}
	})

	t.Run("EOF-open fence - errors without writing", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n~~~~markdown\nexample continues to EOF\n"
		if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err == nil {
			t.Fatal("expected EOF-open fence validation error")
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != original {
			t.Fatalf("EnsureBlurb changed EOF-fenced content:\n got: %q\nwant: %q", content, original)
		}
	})

	t.Run("old blurb followed by EOF-open fence - errors without writing", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n" +
			"<!-- bv-agent-instructions-v3 -->\nold instructions\n<!-- end-bv-agent-instructions -->\n\n" +
			"```markdown\nuser example continues to EOF\n"
		if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := EnsureBlurb(tmpDir); err == nil {
			t.Fatal("expected EOF-open fence validation error")
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != original {
			t.Fatalf("EnsureBlurb changed old blurb plus EOF-fenced content:\n got: %q\nwant: %q", content, original)
		}
	})

	t.Run("duplicate current blurbs - consolidates", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "AGENTS.md")
		original := "# My Instructions\n\n" + AgentBlurb + "\n\nPreserve me.\n\n" + AgentBlurb
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
		if got := strings.Count(string(content), BlurbStartMarker); got != 1 {
			t.Fatalf("current blurb count=%d, want 1", got)
		}
		if !strings.Contains(string(content), "Preserve me.") {
			t.Fatalf("duplicate consolidation lost user content:\n%s", content)
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

func TestWriteReplacementNoPermission(t *testing.T) {
	// Skip on platforms where we can't test permissions properly
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs, not Unix mode bits, govern directory writes")
	}
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test as root")
	}

	tmpDir := t.TempDir()

	// Create a read-only directory
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(readOnlyDir, "test.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readOnlyDir, 0755) // Cleanup

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	// This should fail because we can't create temp file in read-only dir
	err = locked.replace([]byte("test"))
	if err == nil {
		t.Error("Expected error writing to read-only directory")
	}
}
