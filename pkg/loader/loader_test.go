package loader_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
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

func TestFindJSONLPath_PrefersIssuesJSONL(t *testing.T) {
	dir := t.TempDir()
	// Create multiple JSONL files
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "beads.jsonl"), []byte(`{"id":"2"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"3"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	// Current br workspaces export issues.jsonl; beads.jsonl remains a legacy fallback.
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Expected issues.jsonl to be preferred, got: %s", path)
	}
}

func TestFindJSONLPath_FallsBackToBeadsJSONL(t *testing.T) {
	dir := t.TempDir()
	// Create beads.jsonl only (no issues.jsonl)
	os.WriteFile(filepath.Join(dir, "beads.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"2"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "beads.jsonl" {
		t.Errorf("Expected beads.jsonl as fallback, got: %s", path)
	}
}

func TestFindJSONLPath_FallsBackToBeadsBase(t *testing.T) {
	dir := t.TempDir()
	// Create only beads.base.jsonl (no issues.jsonl or beads.jsonl)
	os.WriteFile(filepath.Join(dir, "beads.base.jsonl"), []byte(`{"id":"1"}`), 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(`{"id":"2"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	// beads.base.jsonl is last priority fallback
	if filepath.Base(path) != "beads.base.jsonl" {
		t.Errorf("Expected beads.base.jsonl as last resort fallback, got: %s", path)
	}
}

func TestFindJSONLPath_OnlyIssuesJSONL(t *testing.T) {
	dir := t.TempDir()
	// Create only issues.jsonl (beads.jsonl not present)
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

func TestFindJSONLPath_SkipsBeadsLeftArtifact(t *testing.T) {
	dir := t.TempDir()
	// Create beads.left.jsonl (git merge OURS artifact) and canonical file
	os.WriteFile(filepath.Join(dir, "beads.left.jsonl"), []byte(`{"id":"stale"}`), 0644)
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"current"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if strings.Contains(filepath.Base(path), "left") {
		t.Errorf("Should not select beads.left.jsonl merge artifact, got: %s", path)
	}
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Expected issues.jsonl, got: %s", path)
	}
}

func TestFindJSONLPath_SkipsBeadsRightArtifact(t *testing.T) {
	dir := t.TempDir()
	// Create beads.right.jsonl (git merge THEIRS artifact) and canonical file
	os.WriteFile(filepath.Join(dir, "beads.right.jsonl"), []byte(`{"id":"theirs"}`), 0644)
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"current"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if strings.Contains(filepath.Base(path), "right") {
		t.Errorf("Should not select beads.right.jsonl merge artifact, got: %s", path)
	}
}

func TestFindJSONLPathWithWarnings_ReportsMergeArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Create merge artifacts and canonical file
	os.WriteFile(filepath.Join(dir, "beads.left.jsonl"), []byte(`{"id":"stale"}`), 0644)
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"current"}`), 0644)

	var warnings []string
	warnFunc := func(msg string) {
		warnings = append(warnings, msg)
	}

	path, err := loader.FindJSONLPathWithWarnings(dir, warnFunc)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Expected issues.jsonl, got: %s", path)
	}
	if len(warnings) != 1 {
		t.Fatalf("Expected 1 warning about merge artifacts, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "beads.left.jsonl") {
		t.Errorf("Warning should mention beads.left.jsonl: %s", warnings[0])
	}
	if !strings.Contains(warnings[0], "Clean them up") {
		t.Errorf("Warning should suggest cleanup: %s", warnings[0])
	}
}

func TestIsBDWorkspace_DoltDir(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir dolt: %v", err)
	}

	if !loader.IsBDWorkspace(beadsDir) {
		t.Fatal("IsBDWorkspace() = false, want true")
	}
}

func TestIsBDWorkspace_MetadataJSON(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !loader.IsBDWorkspace(beadsDir) {
		t.Fatal("IsBDWorkspace() = false for metadata.json backend=dolt, want true")
	}
}

func TestIsBDWorkspace_RegularBR(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a regular JSONL file, no dolt/ dir or metadata.json
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"T-1","title":"Test","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if loader.IsBDWorkspace(beadsDir) {
		t.Fatal("IsBDWorkspace() = true for regular br workspace, want false")
	}
}

func TestPrepareWorkspaceForRead_BDWithFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub uses POSIX sh")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir dolt: %v", err)
	}

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	bdScript := filepath.Join(binDir, "bd")
	script := "#!/bin/sh\nif [ \"$1\" != \"export\" ] || [ \"$2\" != \"-o\" ]; then exit 2; fi\nprintf '{\"id\":\"BD-1\",\"title\":\"Test\",\"status\":\"open\",\"priority\":1,\"issue_type\":\"task\"}\\n' > \"$3\"\n"
	if err := os.WriteFile(bdScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	beadsDirResolved, jsonlPath, err := loader.PrepareWorkspaceForRead(dir, true, nil)
	if err != nil {
		t.Fatalf("PrepareWorkspaceForRead() error = %v", err)
	}
	if beadsDirResolved != beadsDir {
		t.Fatalf("BeadsDir = %q, want %q", beadsDirResolved, beadsDir)
	}
	if filepath.Base(jsonlPath) != "issues.jsonl" {
		t.Fatalf("JSONL = %q, want issues.jsonl", jsonlPath)
	}
	if _, err := os.Stat(jsonlPath); err != nil {
		t.Fatalf("expected issues.jsonl to exist: %v", err)
	}
}

func TestPrepareWorkspaceForRead_FallsBackToExistingIssuesJSONL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub uses POSIX sh")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir dolt: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(`{"id":"BD-1","title":"Stale","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	bdScript := filepath.Join(binDir, "bd")
	script := "#!/bin/sh\necho export failed >&2\nexit 1\n"
	if err := os.WriteFile(bdScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var warnings []string
	_, jsonlPath, err := loader.PrepareWorkspaceForRead(dir, true, func(msg string) {
		warnings = append(warnings, msg)
	})
	if err != nil {
		t.Fatalf("PrepareWorkspaceForRead() error = %v", err)
	}
	if jsonlPath != issuesPath {
		t.Fatalf("JSONL = %q, want %q", jsonlPath, issuesPath)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bd export failed") {
		t.Fatalf("expected export failure warning, got %#v", warnings)
	}
}

func TestPrepareWorkspaceForRead_RegularBRFallsThrough(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsFile := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(beadsFile, []byte(`{"id":"BR-1","title":"Test","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DIR", beadsDir)

	_, jsonlPath, err := loader.PrepareWorkspaceForRead("", false, nil)
	if err != nil {
		t.Fatalf("PrepareWorkspaceForRead() error = %v", err)
	}
	if filepath.Base(jsonlPath) != "beads.jsonl" {
		t.Fatalf("expected beads.jsonl for regular br workspace, got %s", filepath.Base(jsonlPath))
	}
}

func TestIssuePoolResetsFields(t *testing.T) {
	issue := loader.GetIssue()
	if issue == nil {
		t.Fatal("GetIssue returned nil")
	}

	now := time.Now()
	ext := "ref"
	issue.ID = "id-1"
	issue.Title = "title"
	issue.Description = "desc"
	issue.Assignee = "owner"
	issue.DueDate = &now
	issue.DeferUntil = &now
	issue.ClosedAt = &now
	issue.EstimatedMinutes = new(int)
	*issue.EstimatedMinutes = 42
	issue.ExternalRef = &ext
	issue.Dependencies = append(issue.Dependencies, &model.Dependency{IssueID: "id-1"})
	issue.Comments = append(issue.Comments, &model.Comment{ID: "1", Text: "note"})
	issue.Labels = append(issue.Labels, "label-a")

	loader.PutIssue(issue)

	reset := loader.GetIssue()
	defer loader.PutIssue(reset)

	if reset.ID != "" || reset.Title != "" || reset.Description != "" || reset.Assignee != "" {
		t.Fatalf("expected scalar fields to be cleared, got ID=%q title=%q desc=%q assignee=%q", reset.ID, reset.Title, reset.Description, reset.Assignee)
	}
	if reset.DueDate != nil || reset.DeferUntil != nil || reset.ClosedAt != nil || reset.EstimatedMinutes != nil || reset.ExternalRef != nil {
		t.Fatalf("expected pointer fields to be nil: due=%v defer=%v closed=%v est=%v ext=%v", reset.DueDate, reset.DeferUntil, reset.ClosedAt, reset.EstimatedMinutes, reset.ExternalRef)
	}
	if len(reset.Dependencies) != 0 {
		t.Fatalf("expected dependencies to be reset, got %d", len(reset.Dependencies))
	}
	if len(reset.Comments) != 0 {
		t.Fatalf("expected comments to be reset, got %d", len(reset.Comments))
	}
	if len(reset.Labels) != 0 {
		t.Fatalf("expected labels to be reset, got %d", len(reset.Labels))
	}
	if cap(reset.Dependencies) == 0 || cap(reset.Comments) == 0 || cap(reset.Labels) == 0 {
		t.Fatalf("expected pooled slices to retain capacity, got deps=%d comments=%d labels=%d", cap(reset.Dependencies), cap(reset.Comments), cap(reset.Labels))
	}
}

func TestParseIssuesWithOptionsPooled_SkipsInvalidLines(t *testing.T) {
	input := strings.Join([]string{
		`{"id":"a","title":"A","status":"open","priority":1,"issue_type":"task"}`,
		`{bad json`,
		`{"id":"b","title":"B","status":"blocked","priority":2,"issue_type":"bug"}`,
	}, "\n") + "\n"

	result, err := loader.ParseIssuesWithOptionsPooled(strings.NewReader(input), loader.ParseOptions{})
	if err != nil {
		t.Fatalf("ParseIssuesWithOptionsPooled failed: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(result.Issues))
	}
	if len(result.PoolRefs) != 2 {
		t.Fatalf("expected 2 pool refs, got %d", len(result.PoolRefs))
	}
	if result.Issues[0].ID != "a" || result.Issues[1].ID != "b" {
		t.Fatalf("unexpected issue IDs: %q %q", result.Issues[0].ID, result.Issues[1].ID)
	}
	if result.PoolRefs[0] == nil || result.PoolRefs[1] == nil {
		t.Fatalf("expected non-nil pool refs")
	}

	loader.ReturnIssuePtrsToPool(result.PoolRefs)
	for i, ref := range result.PoolRefs {
		if ref == nil {
			continue
		}
		if ref.ID != "" || ref.Title != "" || ref.Description != "" {
			t.Fatalf("expected pooled issue %d to be reset, got ID=%q title=%q desc=%q", i, ref.ID, ref.Title, ref.Description)
		}
		if len(ref.Dependencies) != 0 || len(ref.Comments) != 0 || len(ref.Labels) != 0 {
			t.Fatalf("expected pooled issue %d slices to be reset", i)
		}
	}
}

func TestParseIssues_NormalizesStatus(t *testing.T) {
	input := `{"id":"a","title":"A","status":" TombStone ","priority":1,"issue_type":"task"}`

	issues, err := loader.ParseIssues(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Status != model.StatusTombstone {
		t.Fatalf("expected normalized status %q, got %q", model.StatusTombstone, issues[0].Status)
	}
}

func TestParseIssuesWithOptionsPooled_IssueFilter_SkipsClosed(t *testing.T) {
	input := strings.Join([]string{
		`{"id":"a","title":"A","status":"open","priority":1,"issue_type":"task"}`,
		`{"id":"b","title":"B","status":"closed","priority":2,"issue_type":"task"}`,
		`{"id":"c","title":"C","status":"blocked","priority":3,"issue_type":"task"}`,
	}, "\n") + "\n"

	result, err := loader.ParseIssuesWithOptionsPooled(strings.NewReader(input), loader.ParseOptions{
		IssueFilter: func(i *model.Issue) bool {
			return i.Status != model.StatusClosed
		},
	})
	if err != nil {
		t.Fatalf("ParseIssuesWithOptionsPooled failed: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(result.Issues))
	}
	if len(result.PoolRefs) != 2 {
		t.Fatalf("expected 2 pool refs, got %d", len(result.PoolRefs))
	}
	if got := []string{result.Issues[0].ID, result.Issues[1].ID}; got[0] != "a" || got[1] != "c" {
		t.Fatalf("unexpected issue IDs: %#v", got)
	}

	loader.ReturnIssuePtrsToPool(result.PoolRefs)
}

// Regression test for issue #145: bd export emits memories, sprints,
// and other non-issue records into the same JSONL stream, tagged with
// `_type`. The loader must skip them silently rather than try to parse
// every line as an Issue and warn-spam at TUI exit with "issue ID
// cannot be empty".
func TestLoadIssuesFromFile_SkipsNonIssueRecordsByType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := strings.Join([]string{
		`{"id":"REAL-1","title":"A","status":"open","priority":1,"issue_type":"task"}`,
		`{"_type":"memory","key":"some.key","value":"some value"}`,
		`{"_type":"issue","id":"REAL-2","title":"B","status":"open","priority":2,"issue_type":"task"}`,
		`{"_type":"sprint","id":"SP-1","name":"Sprint 1"}`,
		`{"_type":"forecast","id":"FC-1"}`,
		`{"_type":"burndown","sprint_id":"SP-1"}`,
		`{"_type":"future_record_kind","whatever":true}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	var warnings []string
	opts := loader.ParseOptions{
		WarningHandler: func(msg string) { warnings = append(warnings, msg) },
	}
	issues, err := loader.LoadIssuesFromFileWithOptions(path, opts)
	if err != nil {
		t.Fatalf("LoadIssuesFromFileWithOptions failed: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issue rows (REAL-1 + REAL-2), got %d", len(issues))
	}
	if issues[0].ID != "REAL-1" || issues[1].ID != "REAL-2" {
		t.Fatalf("unexpected issue IDs: %q, %q", issues[0].ID, issues[1].ID)
	}
	if len(warnings) > 0 {
		t.Fatalf("non-issue records should skip silently, got warnings: %v", warnings)
	}
}

// Regression test for issue #145: comments with UUIDv7 string IDs
// (beads v1.0+) must round-trip through the loader without dropping
// the parent issue. Pre-fix the loader emitted "skipping malformed
// JSON ... cannot unmarshal number into Comment.ID of type int64"
// and silently lost every issue that had at least one comment.
func TestLoadIssuesFromFile_AcceptsUUIDCommentIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := `{"id":"WITH-COMMENT","title":"X","status":"open","priority":1,"issue_type":"task","comments":[{"id":"019d9b8d-e35f-7ce4-9714-d304b1eb90b0","issue_id":"WITH-COMMENT","author":"u","text":"hi","created_at":"2026-04-17T13:07:41Z"}]}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	var warnings []string
	opts := loader.ParseOptions{
		WarningHandler: func(msg string) { warnings = append(warnings, msg) },
	}
	issues, err := loader.LoadIssuesFromFileWithOptions(path, opts)
	if err != nil {
		t.Fatalf("LoadIssuesFromFileWithOptions: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d (warnings: %v)", len(issues), warnings)
	}
	if len(issues[0].Comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(issues[0].Comments))
	}
	if issues[0].Comments[0].ID != "019d9b8d-e35f-7ce4-9714-d304b1eb90b0" {
		t.Fatalf("comment ID round-trip: got %q", issues[0].Comments[0].ID)
	}
	if len(warnings) > 0 {
		t.Fatalf("expected zero warnings, got: %v", warnings)
	}
}

func TestLoadIssuesFromFileWithOptionsPooled_ReturnsPoolRefs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := strings.Join([]string{
		`{"id":"a","title":"A","status":"open","priority":1,"issue_type":"task"}`,
		`{bad json`,
		`{"id":"b","title":"B","status":"open","priority":2,"issue_type":"feature"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := loader.LoadIssuesFromFileWithOptionsPooled(path, loader.ParseOptions{})
	if err != nil {
		t.Fatalf("LoadIssuesFromFileWithOptionsPooled failed: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(result.Issues))
	}
	if len(result.PoolRefs) != 2 {
		t.Fatalf("expected 2 pool refs, got %d", len(result.PoolRefs))
	}

	loader.ReturnIssuePtrsToPool(result.PoolRefs)
	for i, ref := range result.PoolRefs {
		if ref == nil {
			continue
		}
		if ref.ID != "" || ref.Title != "" {
			t.Fatalf("expected pooled issue %d to be reset, got ID=%q title=%q", i, ref.ID, ref.Title)
		}
		if len(ref.Dependencies) != 0 || len(ref.Comments) != 0 || len(ref.Labels) != 0 {
			t.Fatalf("expected pooled issue %d slices to be reset", i)
		}
	}
}

type errAfterRead struct {
	data []byte
	read bool
}

func (r *errAfterRead) Read(p []byte) (int, error) {
	if r.read {
		return 0, fmt.Errorf("boom")
	}
	r.read = true
	n := copy(p, r.data)
	return n, nil
}

func TestParseIssuesWithOptionsPooled_ErrorReturnsNoIssues(t *testing.T) {
	reader := &errAfterRead{
		data: []byte(`{"id":"a","title":"A","status":"open","priority":1,"issue_type":"task"}` + "\n"),
	}

	result, err := loader.ParseIssuesWithOptionsPooled(reader, loader.ParseOptions{})
	if err == nil {
		t.Fatal("expected error from ParseIssuesWithOptionsPooled")
	}
	if len(result.Issues) != 0 {
		t.Fatalf("expected no issues on error, got %d", len(result.Issues))
	}
	if len(result.PoolRefs) != 0 {
		t.Fatalf("expected no pool refs on error, got %d", len(result.PoolRefs))
	}
}

func TestFindJSONLPath_IssuesPreferredOverBeadsBase(t *testing.T) {
	dir := t.TempDir()
	// Create both issues.jsonl and beads.base.jsonl
	// issues.jsonl should be preferred per beads upstream
	os.WriteFile(filepath.Join(dir, "beads.base.jsonl"), []byte(`{"id":"base"}`), 0644)
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"id":"canonical"}`), 0644)

	path, err := loader.FindJSONLPath(dir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Expected issues.jsonl to be preferred over beads.base.jsonl, got: %s", path)
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
	if len(issue.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(issue.Dependencies))
	}
	if issue.Dependencies[0].IssueID != "full-1" {
		t.Errorf("dependency IssueID = %q, want full-1", issue.Dependencies[0].IssueID)
	}
	if issue.Dependencies[0].DependsOnID != "other-1" {
		t.Errorf("dependency DependsOnID = %q, want other-1", issue.Dependencies[0].DependsOnID)
	}
}

func TestLoadIssuesFromFile_DependencyTargetAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deps.jsonl")
	content := `{"id":"target-alias","title":"Target alias","status":"open","issue_type":"task","dependencies":[{"target_id":"root","type":"blocks"}]}
{"id":"depends-alias","title":"Depends alias","status":"open","issue_type":"task","dependencies":[{"depends_on":"root","type":"blocks"}]}
{"id":"canonical","title":"Canonical","status":"open","issue_type":"task","dependencies":[{"depends_on_id":"root","type":"blocks"}]}`
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	issues, err := loader.LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("LoadIssuesFromFile: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(issues))
	}
	for _, issue := range issues {
		if len(issue.Dependencies) != 1 {
			t.Fatalf("%s expected 1 dependency, got %d", issue.ID, len(issue.Dependencies))
		}
		dep := issue.Dependencies[0]
		if dep.IssueID != issue.ID {
			t.Fatalf("%s dependency IssueID = %q, want %q", issue.ID, dep.IssueID, issue.ID)
		}
		if dep.DependsOnID != "root" {
			t.Fatalf("%s dependency DependsOnID = %q, want root", issue.ID, dep.DependsOnID)
		}
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

// =============================================================================
// GetBeadsDir Tests (bv-zaxb)
// =============================================================================

func TestGetBeadsDir_RespectsEnvVar(t *testing.T) {
	// Set up custom directory
	customDir := t.TempDir()

	// Set environment variable
	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Setenv(loader.BeadsDirEnvVar, customDir)
	defer os.Setenv(loader.BeadsDirEnvVar, oldVal)

	result, err := loader.GetBeadsDir("/some/random/path")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != customDir {
		t.Errorf("Expected BEADS_DIR to be used: got %s, want %s", result, customDir)
	}
}

func TestGetBeadsDir_EnvVarOverridesRepoPath(t *testing.T) {
	customDir := t.TempDir()
	repoPath := t.TempDir()

	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Setenv(loader.BeadsDirEnvVar, customDir)
	defer os.Setenv(loader.BeadsDirEnvVar, oldVal)

	result, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// BEADS_DIR should win over repoPath
	if result != customDir {
		t.Errorf("BEADS_DIR should override repoPath: got %s, want %s", result, customDir)
	}
}

func TestGetBeadsDir_FallsBackToBeadsDir(t *testing.T) {
	// Unset environment variable
	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Unsetenv(loader.BeadsDirEnvVar)
	defer func() {
		if oldVal != "" {
			os.Setenv(loader.BeadsDirEnvVar, oldVal)
		}
	}()

	repoPath := "/some/repo/path"
	expected := filepath.Join(repoPath, ".beads")

	result, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != expected {
		t.Errorf("Without env var, should fallback to .beads: got %s, want %s", result, expected)
	}
}

func TestGetBeadsDir_EmptyRepoPath_UsesCwd(t *testing.T) {
	// Unset environment variable
	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Unsetenv(loader.BeadsDirEnvVar)
	defer func() {
		if oldVal != "" {
			os.Setenv(loader.BeadsDirEnvVar, oldVal)
		}
	}()

	// Use a temp directory outside git to test pure cwd fallback behavior
	// (within a git repo, GetBeadsDir now intelligently finds .beads in the repo root)
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir to temp: %v", err)
	}
	defer os.Chdir(oldCwd)

	expected := filepath.Join(tmpDir, ".beads")

	result, err := loader.GetBeadsDir("")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != expected {
		t.Errorf("Empty repoPath should use cwd: got %s, want %s", result, expected)
	}
}

func TestGetBeadsDir_EnvVarEmpty_FallsBack(t *testing.T) {
	// Set to empty string (should be treated as unset)
	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Setenv(loader.BeadsDirEnvVar, "")
	defer func() {
		if oldVal != "" {
			os.Setenv(loader.BeadsDirEnvVar, oldVal)
		} else {
			os.Unsetenv(loader.BeadsDirEnvVar)
		}
	}()

	repoPath := "/some/repo"
	expected := filepath.Join(repoPath, ".beads")

	result, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != expected {
		t.Errorf("Empty BEADS_DIR should fallback: got %s, want %s", result, expected)
	}
}

func TestGetBeadsDir_BeadsDBMissingSQLiteFileUsesParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.sqlite3")
	t.Setenv(loader.BeadsDBEnvVar, dbPath)
	t.Setenv(loader.BeadsDirEnvVar, filepath.Join(t.TempDir(), ".beads"))

	result, err := loader.GetBeadsDir("/some/random/path")
	if err != nil {
		t.Fatalf("GetBeadsDir: %v", err)
	}
	if result != filepath.Dir(dbPath) {
		t.Fatalf("BEADS_DB sqlite file should resolve to parent dir: got %s, want %s", result, filepath.Dir(dbPath))
	}
}

func TestGetBeadsDir_FindsBeadsInGitRepo(t *testing.T) {
	// Unset environment variable
	oldVal := os.Getenv(loader.BeadsDirEnvVar)
	os.Unsetenv(loader.BeadsDirEnvVar)
	defer func() {
		if oldVal != "" {
			os.Setenv(loader.BeadsDirEnvVar, oldVal)
		}
	}()

	// When running from a subdirectory within a git repo that has .beads,
	// GetBeadsDir should find .beads in the repo root (even via symlinks/worktrees)

	result, err := loader.GetBeadsDir("")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify the returned path exists and is a directory
	info, err := os.Stat(result)
	if err != nil {
		t.Fatalf("Returned beads dir does not exist: %s, error: %v", result, err)
	}
	if !info.IsDir() {
		t.Fatalf("Returned beads dir is not a directory: %s", result)
	}

	// Verify the path ends with .beads
	if filepath.Base(result) != ".beads" {
		t.Errorf("Returned path should end with .beads: got %s", result)
	}
}

// clearBeadsEnv unsets BEADS_DB and BEADS_DIR for the duration of a test so the
// directory-discovery path (which is what follows redirects) is exercised.
func clearBeadsEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{loader.BeadsDBEnvVar, loader.BeadsDirEnvVar} {
		old := os.Getenv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if old != "" {
				os.Setenv(key, old)
			}
		})
	}
}

func TestGetBeadsDir_FollowsRedirect(t *testing.T) {
	clearBeadsEnv(t)

	root := t.TempDir()
	source := filepath.Join(root, "workspace", ".beads")
	target := filepath.Join(root, "tracker", ".beads")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	// Relative redirect from the source .beads to the tracker .beads.
	if err := os.WriteFile(filepath.Join(source, "redirect"), []byte("../../tracker/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	result, err := loader.GetBeadsDir(filepath.Join(root, "workspace"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantAbs, _ := filepath.Abs(target)
	if result != wantAbs {
		t.Errorf("redirect not followed: got %s, want %s", result, wantAbs)
	}
}

func TestGetBeadsDir_NoRedirectReturnsLocal(t *testing.T) {
	clearBeadsEnv(t)

	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	result, err := loader.GetBeadsDir(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != beadsDir {
		t.Errorf("without redirect should return local .beads: got %s, want %s", result, beadsDir)
	}
}

func TestGetBeadsDir_RedirectLoopErrors(t *testing.T) {
	clearBeadsEnv(t)

	root := t.TempDir()
	first := filepath.Join(root, "first", ".beads")
	second := filepath.Join(root, "second", ".beads")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatalf("mkdir first: %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("mkdir second: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first, "redirect"), []byte("../../second/.beads"), 0o644); err != nil {
		t.Fatalf("write first redirect: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "redirect"), []byte("../../first/.beads"), 0o644); err != nil {
		t.Fatalf("write second redirect: %v", err)
	}

	if _, err := loader.GetBeadsDir(filepath.Join(root, "first")); err == nil {
		t.Fatal("expected loop error, got nil")
	}
}

func TestGetBeadsDir_RedirectMissingTargetErrors(t *testing.T) {
	clearBeadsEnv(t)

	root := t.TempDir()
	source := filepath.Join(root, ".beads")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "redirect"), []byte("../does-not-exist/.beads"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	if _, err := loader.GetBeadsDir(root); err == nil {
		t.Fatal("expected missing-target error, got nil")
	}
}

func TestIsBDWorkspace_EmbeddedDoltDir(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir embeddeddolt: %v", err)
	}

	if !loader.IsBDWorkspace(beadsDir) {
		t.Fatal("IsBDWorkspace() = false for .beads/embeddeddolt, want true (#189)")
	}
}

func TestFindJSONLPath_BDWorkspaceRejectsStrayJSONL(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir embeddeddolt: %v", err)
	}
	// A stray non-issue JSONL must not be silently selected in a bd
	// workspace whose compatibility export is missing (#189).
	if err := os.WriteFile(filepath.Join(beadsDir, "memories.jsonl"), []byte(`{"_type":"memory","id":"m1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loader.FindJSONLPath(beadsDir)
	if err == nil {
		t.Fatal("FindJSONLPath() = nil error for bd workspace without issues.jsonl, want loud error")
	}
	if !strings.Contains(err.Error(), "bd export") {
		t.Errorf("error should suggest bd export, got: %v", err)
	}
}

func TestFindJSONLPath_BDWorkspaceAcceptsEmptyIssuesJSONL(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir embeddeddolt: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		t.Fatalf("FindJSONLPath() error = %v", err)
	}
	if got != issuesPath {
		t.Fatalf("FindJSONLPath() = %q, want %q (empty export = legitimately empty project)", got, issuesPath)
	}
}
