package datasource

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func captureDatasourceStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() {
		os.Stderr = original
		_ = writer.Close()
		_ = reader.Close()
	}()

	fn()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		_ = reader.Close()
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return string(output)
}

// makeBDWorkspace creates a temp bd/Dolt workspace layout (#189):
// .beads/metadata.json with backend=dolt plus an embeddeddolt/ data dir.
func makeBDWorkspace(t *testing.T) (dir, beadsDir string) {
	t.Helper()
	dir = t.TempDir()
	beadsDir = filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir embeddeddolt: %v", err)
	}
	meta := `{"database":"dolt","backend":"dolt","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	return dir, beadsDir
}

// installFakeBD puts a stub bd binary on PATH whose `bd export -o <path>`
// writes the given JSONL content.
func installFakeBD(t *testing.T, dir, jsonl string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub uses POSIX sh")
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	payload := filepath.Join(binDir, "payload.jsonl")
	if err := os.WriteFile(payload, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	script := "#!/bin/sh\nif [ \"$1\" != \"export\" ] || [ \"$2\" != \"-o\" ]; then exit 2; fi\ncat '" + payload + "' > \"$3\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// hideBD ensures no bd binary is reachable on PATH.
func hideBD(t *testing.T, dir string) {
	t.Helper()
	emptyDir := filepath.Join(dir, "emptybin")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir emptybin: %v", err)
	}
	t.Setenv("PATH", emptyDir)
}

// TestLoadIssuesFromDir_BDWorkspaceRefreshesViaExport verifies the #141 bridge
// is actually wired into the datasource load path (#189): a Dolt workspace
// with no issues.jsonl gets a fresh compatibility export via `bd export`.
func TestLoadIssuesFromDir_BDWorkspaceRefreshesViaExport(t *testing.T) {
	dir, beadsDir := makeBDWorkspace(t)
	installFakeBD(t, dir, `{"id":"BD-1","title":"From export","status":"open","priority":1,"issue_type":"task"}`+"\n")

	issues, err := LoadIssuesFromDir(beadsDir)
	if err != nil {
		t.Fatalf("LoadIssuesFromDir() error = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "BD-1" {
		t.Fatalf("expected the exported issue, got %#v", issues)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "issues.jsonl")); err != nil {
		t.Fatalf("expected issues.jsonl to be materialized: %v", err)
	}
}

// TestLoadIssuesFromDir_BDWorkspaceNoExportErrorsLoudly pins the #189
// regression: with no bd binary and no compatibility JSONL, the load must
// fail with actionable guidance — never succeed with an empty issue set.
func TestLoadIssuesFromDir_BDWorkspaceNoExportErrorsLoudly(t *testing.T) {
	dir, beadsDir := makeBDWorkspace(t)
	hideBD(t, dir)

	issues, err := LoadIssuesFromDir(beadsDir)
	if err == nil {
		t.Fatalf("expected loud error for Dolt workspace without export, got %d issues", len(issues))
	}
	if !strings.Contains(err.Error(), "bd export") {
		t.Errorf("error should tell the user to run bd export, got: %v", err)
	}
}

// TestLoadIssuesFromDir_BDWorkspaceIgnoresStrayNonIssueJSONL pins the exact
// silent-empty failure from #189: a stray non-issue JSONL (e.g. memories)
// must not be silently loaded as an empty project.
func TestLoadIssuesFromDir_BDWorkspaceIgnoresStrayNonIssueJSONL(t *testing.T) {
	dir, beadsDir := makeBDWorkspace(t)
	hideBD(t, dir)
	stray := `{"_type":"memory","id":"m1","content":"hello"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "memories.jsonl"), []byte(stray), 0o644); err != nil {
		t.Fatalf("write memories.jsonl: %v", err)
	}

	issues, err := LoadIssuesFromDir(beadsDir)
	if err == nil {
		t.Fatalf("expected error, got %d issues (silent-empty regression)", len(issues))
	}
}

// TestLoadIssuesFromDir_BDWorkspaceFallsBackToExistingExport verifies graceful
// degradation: when `bd export` fails but a previous compatibility export
// exists, the existing file is used.
func TestLoadIssuesFromDir_BDWorkspaceFallsBackToExistingExport(t *testing.T) {
	dir, beadsDir := makeBDWorkspace(t)
	hideBD(t, dir)
	existing := `{"id":"BD-2","title":"Existing export","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(existing), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	issues, err := LoadIssuesFromDir(beadsDir)
	if err != nil {
		t.Fatalf("LoadIssuesFromDir() error = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "BD-2" {
		t.Fatalf("expected the existing export issue, got %#v", issues)
	}
}

func TestLoadIssuesFromDir_BDStaleExportRobotModeIsSilentAndReportsAuthorityWarning(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	dir, beadsDir := makeBDWorkspace(t)
	hideBD(t, dir)
	path := filepath.Join(beadsDir, "issues.jsonl")
	existing := `{"id":"BD-STALE","title":"Existing stale export","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write stale issues.jsonl: %v", err)
	}

	var issues []model.Issue
	var loadErr error
	stderr := captureDatasourceStderr(t, func() {
		issues, loadErr = LoadIssuesFromDir(beadsDir)
	})
	if loadErr != nil {
		t.Fatalf("LoadIssuesFromDir() error = %v", loadErr)
	}
	if stderr != "" {
		t.Fatalf("robot-mode stale-export fallback wrote stderr: %q", stderr)
	}
	if len(issues) != 1 || issues[0].ID != "BD-STALE" {
		t.Fatalf("expected existing stale export issue, got %#v", issues)
	}
	report := LastLoadReport()
	if report == nil || report.Path != path {
		t.Fatalf("stale-export load report = %+v, want path %q", report, path)
	}
	if len(report.AuthorityWarnings) != 1 {
		t.Fatalf("authority warnings = %v, want exactly one stale-export warning", report.AuthorityWarnings)
	}
	for _, want := range []string{"bd export failed", "using existing issues.jsonl", "bd binary not found"} {
		if !strings.Contains(report.AuthorityWarnings[0], want) {
			t.Fatalf("authority warning %q missing %q", report.AuthorityWarnings[0], want)
		}
	}
}

// TestLoadIssues_BDWorkspaceViaRepoPath exercises the repo-path entry point
// used by the CLI (datasource.LoadIssues).
func TestLoadIssues_BDWorkspaceViaRepoPath(t *testing.T) {
	dir, _ := makeBDWorkspace(t)
	installFakeBD(t, dir, `{"id":"BD-3","title":"Via repo path","status":"open","priority":1,"issue_type":"task"}`+"\n")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")

	issues, err := LoadIssues(dir)
	if err != nil {
		t.Fatalf("LoadIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "BD-3" {
		t.Fatalf("expected the exported issue, got %#v", issues)
	}
}
