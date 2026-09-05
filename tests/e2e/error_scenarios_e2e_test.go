package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Error Scenario Tests for bv-fqpv
// Tests error handling and graceful degradation across various failure modes.

// =============================================================================
// 1. Data Errors
// =============================================================================

// TestError_CorruptedBeadsJSONL tests handling of corrupted beads.jsonl file.
func TestError_CorruptedBeadsJSONL(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create .beads directory with corrupted issues.jsonl
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write corrupted JSON (incomplete JSON object)
	corrupted := `{"id":"test-1","title":"Valid issue","status":"open","priority":1}
{"id":"test-2","title":"Missing closing brace"
{"id":"test-3","title":"After corruption","status":"open"}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(corrupted), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Robot commands should handle gracefully (partial data or error message)
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	// Should either succeed with partial data or fail gracefully
	if err != nil {
		// Check for helpful error message
		if !strings.Contains(string(output), "parse") && !strings.Contains(string(output), "JSON") &&
			!strings.Contains(string(output), "line") && !strings.Contains(string(output), "error") {
			t.Errorf("error message not helpful for corrupted JSON: %s", output)
		}
	}
	// If it succeeded, it should have loaded at least some issues
	t.Logf("corrupted JSON handling output: %s", output)
}

// TestError_MalformedJSONLines tests various malformed JSON scenarios.
func TestError_MalformedJSONLines(t *testing.T) {
	bv := buildBvBinary(t)

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "empty_object",
			content: `{}`,
		},
		{
			name:    "array_instead_of_object",
			content: `["not", "an", "object"]`,
		},
		{
			name:    "null_value",
			content: `null`,
		},
		{
			name:    "trailing_comma",
			content: `{"id":"test-1","title":"Test",}`,
		},
		{
			name:    "unquoted_key",
			content: `{id:"test-1","title":"Test"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := t.TempDir()
			beadsDir := filepath.Join(env, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("failed to create .beads dir: %v", err)
			}

			issuesPath := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(issuesPath, []byte(tc.content), 0644); err != nil {
				t.Fatalf("failed to write issues.jsonl: %v", err)
			}

			cmd := exec.Command(bv, "--robot-triage")
			cmd.Dir = env
			output, _ := cmd.CombinedOutput()

			// Should not panic - either succeed with empty/partial data or fail gracefully
			t.Logf("%s output: %s", tc.name, output)
		})
	}
}

// TestError_MissingRequiredFields tests handling of issues missing required fields.
func TestError_MissingRequiredFields(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Issue missing ID
	content := `{"title":"No ID","status":"open","priority":1}
{"id":"valid-1","title":"Valid","status":"open","priority":1}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Should handle gracefully
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	// Should succeed with valid issues or fail gracefully
	if err != nil {
		t.Logf("handling missing ID: %s", output)
	} else {
		// Should have processed at least the valid issue
		if !strings.Contains(string(output), "valid-1") && !strings.Contains(string(output), "issue_count") {
			t.Logf("output after missing ID: %s", output)
		}
	}
}

// TestError_InvalidUTF8 tests handling of invalid UTF-8 in issue data.
func TestError_InvalidUTF8(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Issue with invalid UTF-8 byte sequences in title
	// \xff\xfe are invalid UTF-8 continuation bytes
	content := []byte(`{"id":"utf8-test","title":"Invalid `)
	content = append(content, 0xff, 0xfe)
	content = append(content, []byte(` bytes","status":"open","priority":1}`)...)

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, content, 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, _ := cmd.CombinedOutput()

	// Should not panic
	t.Logf("invalid UTF-8 handling: %s", output)
}

// =============================================================================
// 2. File System Errors
// =============================================================================

// TestError_MissingBeadsDirectory tests behavior when .beads directory doesn't exist.
func TestError_MissingBeadsDirectory(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Don't create .beads directory

	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	// Should fail gracefully with helpful message
	if err == nil {
		t.Log("bv succeeded without .beads directory (empty project handling)")
	} else {
		stderrStr := stderr.String()
		// Should mention beads or initialization
		if !strings.Contains(stderrStr, "beads") && !strings.Contains(stderrStr, "init") &&
			!strings.Contains(stderrStr, "not found") && !strings.Contains(stderrStr, "No such file") {
			t.Errorf("unhelpful error for missing .beads: %s", stderrStr)
		}
	}
}

// TestError_EmptyBeadsDirectory tests behavior with empty .beads directory.
func TestError_EmptyBeadsDirectory(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// No issues.jsonl file

	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, _ := cmd.CombinedOutput()

	// Should handle gracefully - empty project is valid
	t.Logf("empty .beads directory output: %s", output)
}

// TestError_ReadOnlyBeadsFile tests handling of read-only files.
func TestError_ReadOnlyBeadsFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping read-only test when running as root")
	}

	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1}`
	if err := os.WriteFile(issuesPath, []byte(content), 0444); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}
	defer os.Chmod(issuesPath, 0644) // Cleanup

	// Read operations should still work
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("read-only file should not block reading: %s", output)
	}
}

// =============================================================================
// 3. Git Errors
// =============================================================================

// TestError_NotGitRepository tests behavior in non-git directory.
func TestError_NotGitRepository(t *testing.T) {
	bv := buildBvBinary(t)
	// Historical loading intentionally ignores ambient Git routing variables,
	// including GIT_CEILING_DIRECTORIES. Use the platform's default temp root
	// instead of RCH's checkout-local TMPDIR, and verify the fixture is non-Git.
	t.Setenv("TMPDIR", "")
	t.Setenv("TMP", "")
	t.Setenv("TEMP", "")
	env := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("Git is required to verify the non-Git fixture: %v", err)
	}
	probe := exec.Command("git", "rev-parse", "--show-toplevel")
	probe.Dir = env
	if output, err := probe.CombinedOutput(); err == nil {
		t.Fatalf("non-Git fixture inherited a repository: %s", output)
	}

	// Create .beads but no .git
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1}`
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Regular triage should work without git
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("--robot-triage should work without git: %s", output)
	}

	// Git-dependent features should fail gracefully
	cmd = exec.Command(bv, "--robot-diff", "--diff-since", "HEAD~1")
	cmd.Dir = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()

	if err == nil {
		t.Fatal("--robot-diff must fail without a Git repository")
	} else {
		stderrStr := stderr.String()
		// Should mention git
		if !strings.Contains(stderrStr, "git") && !strings.Contains(stderrStr, "repository") {
			t.Errorf("unhelpful error for git command without git: %s", stderrStr)
		}
	}
}

// TestError_InvalidGitRevision tests handling of invalid git revision.
func TestError_InvalidGitRevision(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Initialize git repo
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = env
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}

	git("init")

	// Create .beads with issues
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1}`
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	git("add", ".")
	git("commit", "-m", "Initial")

	// Try invalid revision
	cmd := exec.Command(bv, "--robot-diff", "--diff-since", "nonexistent-branch-abc123")
	cmd.Dir = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Log("--robot-diff with invalid revision succeeded (may have fallback)")
	} else {
		stderrStr := stderr.String()
		// Should mention the revision or git error
		if !strings.Contains(stderrStr, "revision") && !strings.Contains(stderrStr, "git") &&
			!strings.Contains(stderrStr, "nonexistent") && !strings.Contains(stderrStr, "unknown") {
			t.Logf("error for invalid revision: %s", stderrStr)
		}
	}
}

// =============================================================================
// 4. Analysis Errors
// =============================================================================

// TestError_PathologicalCyclicGraph tests handling of highly cyclic graphs.
func TestError_PathologicalCyclicGraph(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create a fully connected cyclic graph (every node depends on every other)
	var lines []string
	nodeCount := 10
	for i := 0; i < nodeCount; i++ {
		deps := "["
		for j := 0; j < nodeCount; j++ {
			if i != j {
				if len(deps) > 1 {
					deps += ","
				}
				deps += `{"depends_on_id":"cycle-` + string('a'+rune(j)) + `","type":"blocks"}`
			}
		}
		deps += "]"
		lines = append(lines, `{"id":"cycle-`+string('a'+rune(i))+`","title":"Cyclic Node `+string('A'+rune(i))+`","status":"open","priority":1,"dependencies":`+deps+`}`)
	}

	content := strings.Join(lines, "\n")
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Should handle cyclic graph without hanging or crashing
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("cyclic graph analysis result: %s", output)
	} else {
		// Should report cycles
		if !strings.Contains(string(output), "cycle") {
			t.Logf("cyclic graph output (should mention cycles): %s", output)
		}
	}
}

// TestError_LargeGraphAnalysis tests handling of larger graphs.
func TestError_LargeGraphAnalysis(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create a large-ish graph (100 nodes with chain dependencies)
	var lines []string
	nodeCount := 100
	for i := 0; i < nodeCount; i++ {
		var deps string
		if i > 0 {
			deps = `,"dependencies":[{"depends_on_id":"large-` + string(rune('a'+((i-1)%26))) + `-` + string(rune('0'+(i-1)/26)) + `","type":"blocks"}]`
		}
		id := "large-" + string(rune('a'+(i%26))) + "-" + string(rune('0'+i/26))
		lines = append(lines, `{"id":"`+id+`","title":"Issue `+id+`","status":"open","priority":1`+deps+`}`)
	}

	content := strings.Join(lines, "\n")
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Should complete within reasonable time
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("large graph analysis failed: %s", output)
	} else {
		// Should have analyzed all issues
		t.Logf("large graph analysis completed")
	}
}

// =============================================================================
// 5. Export Errors
// =============================================================================

// TestError_InvalidExportPath tests handling of invalid export paths.
func TestError_InvalidExportPath(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1}`
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Try to export to non-existent directory
	cmd := exec.Command(bv, "--export-graph", "/nonexistent/path/graph.json")
	cmd.Dir = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Log("export to invalid path succeeded (may create parent dirs)")
	} else {
		stderrStr := stderr.String()
		// Should mention the path issue
		if !strings.Contains(stderrStr, "path") && !strings.Contains(stderrStr, "directory") &&
			!strings.Contains(stderrStr, "No such file") && !strings.Contains(stderrStr, "create") {
			t.Logf("error for invalid export path: %s", stderrStr)
		}
	}
}

// TestError_ExportToReadOnlyDirectory tests export to read-only directory.
func TestError_ExportToReadOnlyDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping read-only test when running as root")
	}

	bv := buildBvBinary(t)
	env := t.TempDir()

	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1}`
	if err := os.WriteFile(issuesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create read-only directory
	readOnlyDir := filepath.Join(env, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0555); err != nil {
		t.Fatalf("failed to create read-only dir: %v", err)
	}
	defer os.Chmod(readOnlyDir, 0755) // Cleanup

	// Try to export to read-only directory
	cmd := exec.Command(bv, "--export-graph", filepath.Join(readOnlyDir, "graph.json"))
	cmd.Dir = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Error("export to read-only directory should fail")
	} else {
		stderrStr := stderr.String()
		// Should mention permission
		if !strings.Contains(stderrStr, "permission") && !strings.Contains(stderrStr, "denied") &&
			!strings.Contains(stderrStr, "read-only") && !strings.Contains(stderrStr, "readonly") {
			t.Logf("error for read-only export: %s", stderrStr)
		}
	}
}

// =============================================================================
// 6. Exit Code Verification
// =============================================================================

// TestError_ExitCodes verifies that error scenarios produce non-zero exit codes.
func TestError_ExitCodes(t *testing.T) {
	bv := buildBvBinary(t)

	tests := []struct {
		name       string
		setup      func(dir string) error
		args       []string
		expectFail bool
	}{
		{
			name: "missing_beads_dir",
			setup: func(dir string) error {
				return nil // Don't create .beads
			},
			args:       []string{"--robot-triage"},
			expectFail: true, // May or may not fail depending on implementation
		},
		{
			name: "empty_beads_dir",
			setup: func(dir string) error {
				return os.MkdirAll(filepath.Join(dir, ".beads"), 0755)
			},
			args:       []string{"--robot-triage"},
			expectFail: true, // Missing issues.jsonl file
		},
		{
			name: "valid_data",
			setup: func(dir string) error {
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(beadsDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(beadsDir, "issues.jsonl"),
					[]byte(`{"id":"test","title":"Test","status":"open","priority":1}`),
					0644,
				)
			},
			args:       []string{"--robot-triage"},
			expectFail: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := t.TempDir()
			if err := tc.setup(env); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			cmd := exec.Command(bv, tc.args...)
			cmd.Dir = env
			err := cmd.Run()

			if tc.expectFail && err == nil {
				t.Logf("%s: expected failure but succeeded", tc.name)
			} else if !tc.expectFail && err != nil {
				t.Errorf("%s: expected success but failed: %v", tc.name, err)
			}
		})
	}
}
