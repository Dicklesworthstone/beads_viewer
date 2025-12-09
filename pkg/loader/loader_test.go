package loader_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

// =============================================================================
// FindJSONLPath Tests
// =============================================================================

func TestFindJSONLPath_NonExistentDirectory(t *testing.T) {
	_, err := loader.FindJSONLPath("/nonexistent/path/to/beads")
	if err == nil {
		t.Fatal("Expected error for non-existent directory")
	}
	if !strings.Contains(err.Error(), "failed to read beads directory") {
		t.Errorf("Expected 'failed to read beads directory' error, got: %v", err)
	}
}

func TestFindJSONLPath_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := loader.FindJSONLPath(dir)
	if err == nil {
		t.Fatal("Expected error for empty directory")
	}
	if !strings.Contains(err.Error(), "no beads JSONL file found") {
		t.Errorf("Expected 'no beads JSONL file found' error, got: %v", err)
	}
}

func TestFindJSONLPath_NoJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	// Create non-JSONL files
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0644)

	_, err := loader.FindJSONLPath(dir)
	if err == nil {
		t.Fatal("Expected error when no .jsonl files exist")
	}
}

func TestFindJSONLPath_PrefersBeadsJSONL(t *testing.T) {
	dir := t.TempDir()
	// Create multiple JSONL files
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "beads.jsonl"), []byte(`{"id":"2"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"3"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "beads.jsonl" {
		t.Errorf("Expected beads.jsonl to be preferred, got: %s", path)
	}
}

func TestFindJSONLPath_FallsBackToBeadsBase(t *testing.T) {
	dir := t.TempDir()
	// Create beads.base.jsonl and issues.jsonl (no beads.jsonl)
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "beads.base.jsonl"), []byte(`{"id":"2"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "beads.base.jsonl" {
		t.Errorf("Expected beads.base.jsonl, got: %s", path)
	}
}

func TestFindJSONLPath_FallsBackToIssues(t *testing.T) {
	dir := t.TempDir()
	// Create only issues.jsonl
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"1"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Expected issues.jsonl, got: %s", path)
	}
}

func TestFindJSONLPath_SkipsBackupFiles(t *testing.T) {
	dir := t.TempDir()
	// Create backup and regular files
	os.WriteFile(filepath.Join(dir, "beads.jsonl.backup"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "beads.backup.jsonl"), []byte(`{"id":"2"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"3"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if strings.Contains(filepath.Base(path), "backup") {
		t.Errorf("Should not select backup file, got: %s", path)
	}
}

func TestFindJSONLPath_SkipsMergeArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Create merge artifacts and regular files
	os.WriteFile(filepath.Join(dir, "beads.orig.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "beads.merge.jsonl"), []byte(`{"id":"2"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"3"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if strings.Contains(filepath.Base(path), "orig") || strings.Contains(filepath.Base(path), "merge") {
		t.Errorf("Should not select merge artifacts, got: %s", path)
	}
}

func TestFindJSONLPath_SkipsDeletionsJSONL(t *testing.T) {
	dir := t.TempDir()
	// Create deletions.jsonl and another file
	os.WriteFile(filepath.Join(dir, "deletions.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"2"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) == "deletions.jsonl" {
		t.Error("Should not select deletions.jsonl")
	}
}

func TestFindJSONLPath_SkipsEmptyPreferredFiles(t *testing.T) {
	dir := t.TempDir()
	// Create empty beads.jsonl and non-empty other.jsonl
	os.WriteFile(filepath.Join(dir, "beads.jsonl"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"1"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) == "beads.jsonl" {
		t.Error("Should skip empty beads.jsonl and use non-empty file")
	}
}

func TestFindJSONLPath_ReturnsEmptyFileAsLastResort(t *testing.T) {
	dir := t.TempDir()
	// Create only empty files
	os.WriteFile(filepath.Join(dir, "empty.jsonl"), []byte{}, 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if path == "" {
		t.Error("Should return empty file as last resort")
	}
}

func TestFindJSONLPath_IgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	// Create a directory with .jsonl name and a regular file
	os.MkdirAll(filepath.Join(dir, "fake.jsonl"), 0755)
	os.WriteFile(filepath.Join(dir, "real.jsonl"), []byte(`{"id":"1"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "real.jsonl" {
		t.Errorf("Expected real.jsonl, got: %s", path)
	}
}

func TestFindJSONLPath_FollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "beads.jsonl")
	if err := os.WriteFile(target, []byte(`{"id":"link-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "beads.link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported on this filesystem: %v", err)
	}

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if path != target {
		t.Errorf("Expected to resolve symlink to %s, got %s", target, path)
	}
}

// =============================================================================
// GetBeadsDir Tests
// =============================================================================

func TestGetBeadsDir_UsesEnvVar(t *testing.T) {
	// Set BEADS_DIR environment variable
	customDir := "/custom/beads/path"
	os.Setenv("BEADS_DIR", customDir)
	defer os.Unsetenv("BEADS_DIR")

	dir, err := loader.GetBeadsDir("")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if dir != customDir {
		t.Errorf("Expected %s, got %s", customDir, dir)
	}
}

func TestGetBeadsDir_EnvVarOverridesRepoPath(t *testing.T) {
	// Set BEADS_DIR environment variable
	customDir := "/custom/beads/path"
	os.Setenv("BEADS_DIR", customDir)
	defer os.Unsetenv("BEADS_DIR")

	// Even with a repo path, BEADS_DIR should take precedence
	dir, err := loader.GetBeadsDir("/some/repo/path")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if dir != customDir {
		t.Errorf("Expected BEADS_DIR (%s) to override repo path, got %s", customDir, dir)
	}
}

func TestGetBeadsDir_FallsBackToRepoPath(t *testing.T) {
	// Ensure BEADS_DIR is not set
	os.Unsetenv("BEADS_DIR")

	repoPath := "/my/repo"
	dir, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected := filepath.Join(repoPath, ".beads")
	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
	}
}

func TestGetBeadsDir_FallsBackToCwd(t *testing.T) {
	// Ensure BEADS_DIR is not set
	os.Unsetenv("BEADS_DIR")

	dir, err := loader.GetBeadsDir("")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	cwd, _ := os.Getwd()
	expected := filepath.Join(cwd, ".beads")
	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
	}
}

// =============================================================================
// LoadIssues Tests
// =============================================================================

func TestLoadIssues_NonExistentBeadsDir(t *testing.T) {
	dir := t.TempDir()
	// Don't create .beads directory
	_, err := loader.LoadIssues(dir)
	if err == nil {
		t.Fatal("Expected error for non-existent .beads directory")
	}
}

func TestLoadIssues_RespectsBeadsDirEnvVar(t *testing.T) {
	// Create a custom beads directory (not inside .beads)
	customDir := t.TempDir()
	os.WriteFile(filepath.Join(customDir, "beads.jsonl"), []byte(`{"id":"env-1","title":"From Env","status":"open","issue_type":"task"}`+"\n"), 0644)

	// Set BEADS_DIR to point to custom directory
	os.Setenv("BEADS_DIR", customDir)
	defer os.Unsetenv("BEADS_DIR")

	// Call LoadIssues with empty path - it should use BEADS_DIR
	issues, err := loader.LoadIssues("")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "env-1" {
		t.Errorf("Expected ID 'env-1', got '%s'", issues[0].ID)
	}
}

func TestLoadIssues_BeadsPathIsFile(t *testing.T) {
	dir := t.TempDir()
	beadsFile := filepath.Join(dir, ".beads")
	if err := os.WriteFile(beadsFile, []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := loader.LoadIssues(dir)
	if err == nil {
		t.Fatal("Expected error when .beads is a file, not a directory")
	}
	if !strings.Contains(err.Error(), "failed to read beads directory") {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestLoadIssues_EmptyPath(t *testing.T) {
	// This test verifies that empty path uses current directory
	// We just verify it doesn't panic - actual behavior depends on cwd
	_, err := loader.LoadIssues("")
	// Error is expected since cwd likely doesn't have .beads
	if err == nil {
		t.Log("Empty path used current directory successfully")
	}
}

func TestLoadIssues_PathWithSpaces(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "dir with spaces")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"space-1","title":"Space Path","status":"open","issue_type":"task"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	issues, err := loader.LoadIssues(dir)
	if err != nil {
		t.Fatalf("Unexpected error loading issues from path with spaces: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "space-1" {
		t.Fatalf("Expected single issue space-1, got %v", issues)
	}
}

func TestLoadIssues_ValidRepository(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"test-1","title":"Test Issue","status":"open","issue_type":"task"}`+"\n"), 0644)

	issues, err := loader.LoadIssues(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "test-1" {
		t.Errorf("Expected ID 'test-1', got '%s'", issues[0].ID)
	}
}

// =============================================================================
// LoadIssuesFromFile Tests
// =============================================================================

func TestLoadIssuesFromFile_NonExistentFile(t *testing.T) {
	_, err := loader.LoadIssuesFromFile("/nonexistent/path/to/file.jsonl")
	if err == nil {
		t.Fatal("Expected error for non-existent file")
	}
	if !strings.Contains(err.Error(), "no beads issues found") {
		t.Errorf("Expected 'no beads issues found' error, got: %v", err)
	}
}

func TestLoadIssuesFromFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(path, []byte{}, 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Empty file should not error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues from empty file, got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_WhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitespace.jsonl")
	os.WriteFile(path, []byte("\n\n\n   \n\t\n"), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Whitespace-only file should not error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues from whitespace-only file, got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_ValidSingleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.jsonl")
	os.WriteFile(path, []byte(`{"id":"issue-1","title":"Single Issue","status":"open","issue_type":"task"}`+"\n"), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "issue-1" {
		t.Errorf("Expected ID 'issue-1', got '%s'", issues[0].ID)
	}
	if issues[0].Title != "Single Issue" {
		t.Errorf("Expected Title 'Single Issue', got '%s'", issues[0].Title)
	}
}

func TestLoadIssuesFromFile_ValidMultiLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.jsonl")
	content := `{"id":"issue-1","title":"First","status":"open","issue_type":"task"}
{"id":"issue-2","title":"Second","status":"open","issue_type":"task"}
{"id":"issue-3","title":"Third","status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("Expected 3 issues, got %d", len(issues))
	}
	for i, expected := range []string{"issue-1", "issue-2", "issue-3"} {
		if issues[i].ID != expected {
			t.Errorf("Issue %d: expected ID '%s', got '%s'", i, expected, issues[i].ID)
		}
	}
}

func TestLoadIssuesFromFile_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.jsonl")
	content := `{"id":"good-1","title":"Valid","status":"open","issue_type":"task"}
{not valid json}
{"id":"good-2","title":"Also Valid","status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Should skip malformed lines, got error: %v", err)
	}
	// Should load the 2 valid lines
	if len(issues) != 2 {
		t.Errorf("Expected 2 valid issues (skipping malformed), got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_PartiallyMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.jsonl")
	content := `{"id":"1","title":"A","status":"open","issue_type":"task"}
{"id":"2"
{"id":"3","title":"C","status":"open","issue_type":"task"}
invalid
{"id":"4","title":"D","status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Should continue loading after malformed lines: %v", err)
	}
	if len(issues) != 3 {
		t.Errorf("Expected 3 valid issues, got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_ValidJSONInvalidSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.jsonl")
	// Valid JSON but not matching Issue schema exactly - should still parse
	content := `{"id":"1","title":"Normal","extraField":"ignored","status":"open","issue_type":"task"}
{"id":"2","title":"Also Normal","nested":{"deep":true},"status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues (extra fields ignored), got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 permission test not reliable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "denied.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"1"}`+"\n"), 0000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}

	_, err := loader.LoadIssuesFromFile(path)
	if err == nil {
		t.Fatal("Expected permission error when reading file")
	}
	if !strings.Contains(err.Error(), "failed to open issues file") {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestLoadIssuesFromFile_VeryLargeLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")

	// Create a ~2MB description to exercise scanner buffer (default 64K would fail)
	largeDesc := strings.Repeat("A", 2*1024*1024)
	line := fmt.Sprintf(`{"id":"big-1","title":"Big","description":"%s","status":"open","issue_type":"task"}`, largeDesc)
	if err := os.WriteFile(path, []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error reading large line: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "big-1" {
		t.Errorf("Expected ID big-1, got %s", issues[0].ID)
	}
}

func TestLoadIssuesFromFile_Unicode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unicode.jsonl")
	content := `{"id":"emoji-1","title":"Fix bug 🐛 in code 💻","status":"open","issue_type":"task"}
{"id":"cjk-1","title":"中文标题测试","status":"open","issue_type":"task"}
{"id":"rtl-1","title":"عنوان عربي","status":"open","issue_type":"task"}
{"id":"special-1","title":"Line\nwith\ttabs and \"quotes\"","status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error loading unicode: %v", err)
	}
	if len(issues) != 4 {
		t.Fatalf("Expected 4 issues, got %d", len(issues))
	}

	// Verify emoji preserved
	if !strings.Contains(issues[0].Title, "🐛") {
		t.Errorf("Emoji not preserved: %s", issues[0].Title)
	}
	// Verify CJK preserved
	if !strings.Contains(issues[1].Title, "中文") {
		t.Errorf("CJK not preserved: %s", issues[1].Title)
	}
	// Verify RTL preserved
	if !strings.Contains(issues[2].Title, "عربي") {
		t.Errorf("RTL not preserved: %s", issues[2].Title)
	}
}

func TestLoadIssuesFromFile_LargeLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")

	// Create an issue with a very large description (1MB)
	largeDesc := strings.Repeat("x", 1024*1024)
	content := `{"id":"large-1","title":"Large Issue","description":"` + largeDesc + `","status":"open","issue_type":"task"}`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Should handle large lines: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}
	if len(issues[0].Description) != 1024*1024 {
		t.Errorf("Description truncated: expected %d bytes, got %d", 1024*1024, len(issues[0].Description))
	}
}

func TestLoadIssuesFromFile_MixedEmptyLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	content := `
{"id":"1","title":"First","status":"open","issue_type":"task"}

{"id":"2","title":"Second","status":"open","issue_type":"task"}


{"id":"3","title":"Third","status":"open","issue_type":"task"}
`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 3 {
		t.Errorf("Expected 3 issues (ignoring empty lines), got %d", len(issues))
	}
}

func TestLoadIssuesFromFile_AllFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allfields.jsonl")
	content := `{"id":"full-1","title":"Complete Issue","description":"A full issue","status":"open","priority":1,"issue_type":"bug","dependencies":[{"depends_on":"other-1","type":"blocks"}]}`
	os.WriteFile(path, []byte(content+"\n"), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}

	issue := issues[0]
	if issue.ID != "full-1" {
		t.Errorf("ID mismatch: %s", issue.ID)
	}
	if issue.Title != "Complete Issue" {
		t.Errorf("Title mismatch: %s", issue.Title)
	}
	if issue.Description != "A full issue" {
		t.Errorf("Description mismatch: %s", issue.Description)
	}
	if issue.Priority != 1 {
		t.Errorf("Priority mismatch: %d", issue.Priority)
	}
}

// =============================================================================
// Original Test (kept for compatibility)
// =============================================================================

func TestLoadRealIssues(t *testing.T) {
	files := []string{
		"../../tests/testdata/srps_issues.jsonl",
		"../../tests/testdata/cass_issues.jsonl",
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				t.Skipf("Test file %s not found, skipping", f)
			}

			issues, err := loader.LoadIssuesFromFile(f)
			if err != nil {
				t.Fatalf("Failed to load %s: %v", f, err)
			}
			if len(issues) == 0 {
				t.Fatalf("Expected issues in %s, got 0", f)
			}
			t.Logf("Loaded %d issues from %s", len(issues), f)

			// Basic validation of fields
			for _, issue := range issues {
				if issue.ID == "" {
					t.Errorf("Issue missing ID")
				}
				if issue.Title == "" {
					t.Errorf("Issue %s missing Title", issue.ID)
				}
			}
		})
	}
}

func TestLoadIssuesFromFile_MissingID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing_id.jsonl")
	content := `{"title":"No ID Issue"}`
	os.WriteFile(path, []byte(content), 0644)

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues (skipping empty ID), got %d", len(issues))
	}
}
