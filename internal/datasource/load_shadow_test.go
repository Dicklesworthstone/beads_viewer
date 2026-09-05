package datasource

import (
	"os"
	"path/filepath"
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

	loaded, err := LoadIssuesFromDir(beads)
	got := loaded.Issues
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

	loaded2, err := LoadIssuesFromDir(beads)
	got2 := loaded2.Issues
	if err != nil {
		t.Fatalf("with sprints: err=%v", err)
	}
	if len(got2) != 2 {
		t.Errorf("fresher sprints.jsonl shadowed issues.jsonl: got %d issues, want 2", len(got2))
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
	loaded, err := LoadIssuesFromDir(beads)
	got := loaded.Issues
	if err != nil || len(got) != 0 {
		t.Errorf("empty project: got %d issues err=%v, want 0 / nil", len(got), err)
	}
}
