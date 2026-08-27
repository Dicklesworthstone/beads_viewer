package datasource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression for a loadSmart bug: a fresher non-issue JSONL (e.g. a stray
// sprints.jsonl, all `_type` records) must NOT shadow the real issues.jsonl.
// The fused load+validate gate counts non-issue records as Skipped, and a source
// that yields zero valid issues from non-empty content is rejected so loadSmart
// falls through to the next candidate — while a legitimately empty project still
// loads as 0 issues without error.
func TestLoadSmartNonIssueShadow(t *testing.T) {
	const issue1 = `{"id":"bv-1","title":"real","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
	const issue2 = `{"id":"bv-2","title":"two","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`

	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beads, "issues.jsonl"), []byte(issue1+"\n"+issue2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadIssuesFromDir(beads)
	if err != nil || len(got) != 2 {
		t.Fatalf("clean: got %d issues err=%v, want 2", len(got), err)
	}

	// Add a FRESHER sprints-only file. It must not shadow issues.jsonl.
	time.Sleep(20 * time.Millisecond)
	sprints := filepath.Join(beads, "sprints.jsonl")
	if err := os.WriteFile(sprints, []byte(`{"_type":"sprint","name":"S1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = os.Chtimes(sprints, now, now)

	got2, err := LoadIssuesFromDir(beads)
	if err != nil {
		t.Fatalf("with sprints: err=%v", err)
	}
	if len(got2) != 2 {
		t.Errorf("fresher sprints.jsonl shadowed issues.jsonl: got %d issues, want 2", len(got2))
	}
}

func TestLoadSmartEqualTimePrefersCanonicalJSONLName(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	beadsDir := t.TempDir()
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	legacyPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(issuesPath, []byte(`{"id":"CURRENT-1","title":"current","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write canonical source: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"id":"LEGACY-1","title":"legacy","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy source: %v", err)
	}
	equalTime := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, path := range []string{issuesPath, legacyPath} {
		if err := os.Chtimes(path, equalTime, equalTime); err != nil {
			t.Fatalf("set equal timestamp for %s: %v", path, err)
		}
	}

	issues, err := LoadIssuesFromDir(beadsDir)
	if err != nil {
		t.Fatalf("LoadIssuesFromDir: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "CURRENT-1" {
		t.Fatalf("loaded issues = %+v, want canonical issues.jsonl contents", issues)
	}
	selected, ok := LastSelectedSource()
	if !ok || selected.Path != issuesPath {
		t.Fatalf("selected source = %+v, present %v; want %s", selected, ok, issuesPath)
	}
}

// A legitimately empty issues.jsonl (no records at all) is a valid empty project:
// it must load as 0 issues with no error, not be rejected.
func TestLoadSmartEmptyProjectValid(t *testing.T) {
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beads, "issues.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadIssuesFromDir(beads)
	if err != nil || len(got) != 0 {
		t.Errorf("empty project: got %d issues err=%v, want 0 / nil", len(got), err)
	}
}

func TestLoadIssuesFromDirRejectsAllSkippedOnlySource(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	beadsDir := t.TempDir()
	path := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(path, []byte(`{"_type":"sprint","id":"sprint-only"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write all-skipped source: %v", err)
	}

	issues, err := LoadIssuesFromDir(beadsDir)
	if err == nil {
		t.Fatalf("all-skipped-only source was accepted as empty authority: %+v", issues)
	}
	if issues != nil {
		t.Fatalf("all-skipped-only source returned %d issues with an error", len(issues))
	}
	for _, want := range []string{path, "no issue records", "1 non-issue"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("all-skipped rejection %q missing %q", err, want)
		}
	}
}
