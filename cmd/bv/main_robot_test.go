package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TestRobotPlanAndPriorityIncludeMetadata runs the built binary against a tiny fixture project
// to assert that robot-plan and robot-priority include data_hash, analysis_config, and status.
func TestRobotPlanAndPriorityIncludeMetadata(t *testing.T) {
	dir := t.TempDir()
	// create minimal .beads directory with beads.jsonl
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	beads := `{"id":"TEST-1","title":"A","status":"open","priority":1,"issue_type":"task"}
{"id":"TEST-2","title":"B","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"TEST-2","depends_on_id":"TEST-1","type":"blocks"}]}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	runAndCheck := func(flag string) {
		cmd := exec.Command(exe, flag)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s failed: %v, out=%s", flag, err, string(out))
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("%s json: %v", flag, err)
		}
		if _, ok := payload["data_hash"]; !ok {
			t.Fatalf("%s missing data_hash", flag)
		}
		if _, ok := payload["analysis_config"]; !ok {
			t.Fatalf("%s missing analysis_config", flag)
		}
		statusAny, ok := payload["status"]
		if !ok {
			t.Fatalf("%s missing status", flag)
		}

		status, ok := statusAny.(map[string]any)
		if !ok {
			t.Fatalf("%s status not an object", flag)
		}

		// Ensure the status contract is usable at process exit (no pending/empty states).
		expected := []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"}
		for _, metric := range expected {
			entryAny, ok := status[metric]
			if !ok {
				t.Fatalf("%s status missing %s", flag, metric)
			}
			entry, ok := entryAny.(map[string]any)
			if !ok {
				t.Fatalf("%s status.%s not an object", flag, metric)
			}
			stateAny, ok := entry["state"]
			if !ok {
				t.Fatalf("%s status.%s missing state", flag, metric)
			}
			state, _ := stateAny.(string)
			if state == "" {
				t.Fatalf("%s status.%s state empty", flag, metric)
			}
			if state == "pending" {
				t.Fatalf("%s status.%s still pending at exit", flag, metric)
			}
		}
	}

	runAndCheck("--robot-plan")
	runAndCheck("--robot-priority")
}

// buildTestBinary builds the current module's bv binary for testing.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "bv-testbin")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = "." // build current package
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bv: %v, out=%s", err, string(out))
	}
	return exe
}

// TestTOONOutputFormat verifies that --format=toon produces valid TOON output (bd-2lmf)
func TestTOONOutputFormat(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON tests")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Test Issue","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	// Test TOON output for robot-next
	cmd := exec.Command(exe, "--robot-next", "--format=toon")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with toon failed: %v", err)
	}

	// TOON output should not start with { (that's JSON)
	toonOut := string(out)
	if len(toonOut) > 0 && toonOut[0] == '{' {
		t.Fatalf("TOON output looks like JSON, expected TOON format: %s", toonOut[:min(100, len(toonOut))])
	}

	// Should contain key: value pattern typical of TOON
	if !containsKeyValuePattern(toonOut) {
		t.Fatalf("TOON output doesn't look like TOON: %s", toonOut[:min(200, len(toonOut))])
	}
}

func TestRobotNextFailClosedWhenNoClaimableItem(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"BLOCKED-1","title":"Blocked high impact","status":"blocked","priority":0,"issue_type":"bug"}
{"id":"OWNED-1","title":"Already owned","status":"open","assignee":"OtherAgent","priority":1,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)
	cmd := exec.Command(exe, "--robot-next", "--format=json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, out)
	}
	if got := payload["actionable"]; got != false {
		t.Fatalf("actionable = %v, want false; payload=%v", got, payload)
	}
	if _, ok := payload["claim_command"]; ok {
		t.Fatalf("fail-closed robot-next must not emit claim_command: %s", out)
	}
	if _, ok := payload["status"].(map[string]any); !ok {
		t.Fatalf("robot-next missing metric status: %s", out)
	}
	degraded, ok := payload["degraded"].([]any)
	if !ok || len(degraded) == 0 {
		t.Fatalf("robot-next fail-closed response missing degraded[]: %s", out)
	}
	first, ok := degraded[0].(map[string]any)
	if !ok || first["code"] != "no_actionable_recommendation" {
		t.Fatalf("unexpected degraded payload: %v", degraded)
	}
}

func TestRobotNextEmitsClaimOnlyForSafeTopPick(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"READY-1","title":"Ready work","status":"open","priority":1,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

	exe := buildTestBinary(t)
	cmd := exec.Command(exe, "--robot-next", "--format=json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, out)
	}
	if got := payload["actionable"]; got != true {
		t.Fatalf("actionable = %v, want true; payload=%v", got, payload)
	}
	if got := payload["id"]; got != "READY-1" {
		t.Fatalf("id = %v, want READY-1; payload=%v", got, payload)
	}
	claim, ok := payload["claim_command"].(string)
	if !ok || !strings.Contains(claim, "READY-1") {
		t.Fatalf("safe robot-next missing claim command for READY-1: %s", out)
	}
	if _, ok := payload["status"].(map[string]any); !ok {
		t.Fatalf("robot-next missing metric status: %s", out)
	}
}

func TestRobotNextEmitsClaimForCanonicalLiveSQLiteSource(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_SKIP_PHASE2", "")
	t.Setenv("BV_PHASE2_TIMEOUT_S", "")
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite fixture: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER DEFAULT 3,
			issue_type TEXT DEFAULT 'task'
		);
		INSERT INTO issues (id, title, status, priority, issue_type)
		VALUES ('SQLITE-READY-1', 'Ready SQLite work', 'open', 1, 'task');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("initialize SQLite fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close SQLite fixture: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, dir, exe, "--robot-next", "--format=json")
	if err != nil {
		t.Fatalf("SQLite robot-next failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode SQLite robot-next: %v\n%s", err, stdout)
	}
	if !output.Actionable || output.ID != "SQLITE-READY-1" {
		t.Fatalf("SQLite robot-next = %+v, want actionable SQLITE-READY-1", output)
	}
	if !strings.Contains(output.ClaimCmd, "SQLITE-READY-1") || !strings.Contains(output.ShowCmd, "SQLITE-READY-1") {
		t.Fatalf("SQLite robot-next omitted live br commands: %+v", output)
	}
	if len(output.Degraded) != 0 || len(output.RepositoryRouteUnavailableReasons) != 0 {
		t.Fatalf("canonical SQLite source was degraded: %+v", output)
	}
}

func TestRejectedCanonicalSourceFallbackDisablesClaimsAndPersistentSearch(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_SKIP_PHASE2", "1")
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(`{"id":"READY-1","title":"Fallback work","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write JSONL fallback: %v", err)
	}
	corruptDBPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(corruptDBPath, []byte("not a SQLite database"), 0o644); err != nil {
		t.Fatalf("write corrupt SQLite: %v", err)
	}
	older := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if err := os.Chtimes(issuesPath, older, older); err != nil {
		t.Fatalf("age JSONL fallback: %v", err)
	}
	if err := os.Chtimes(corruptDBPath, newer, newer); err != nil {
		t.Fatalf("freshen corrupt SQLite: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, dir, exe, "--robot-next", "--format=json")
	if err != nil {
		t.Fatalf("fallback robot-next failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var next robotNextOutput
	if err := json.Unmarshal([]byte(stdout), &next); err != nil {
		t.Fatalf("decode fallback robot-next: %v\n%s", err, stdout)
	}
	if next.Actionable || next.ClaimCmd != "" || next.ShowCmd != "" {
		t.Fatalf("fallback robot-next retained mutation signals: %+v", next)
	}
	if len(next.Degraded) != 1 || next.Degraded[0].Code != "robot_next_authority_incomplete" {
		t.Fatalf("fallback robot-next degradation = %+v, want authority incomplete", next.Degraded)
	}
	joinedReasons := strings.Join(next.AuthorityIncompleteReasons, "\n")
	for _, want := range []string{corruptDBPath, issuesPath, "using fallback"} {
		if !strings.Contains(joinedReasons, want) {
			t.Fatalf("fallback authority evidence %q does not contain %q", joinedReasons, want)
		}
	}

	stdout, stderr, err = runCommandWithTimeout(t, dir, exe, "--search", "fallback work", "--robot-search", "--format=json")
	if err != nil {
		t.Fatalf("fallback robot-search failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var searchOutput robotSearchOutput
	if err := json.Unmarshal([]byte(stdout), &searchOutput); err != nil {
		t.Fatalf("decode fallback robot-search: %v\n%s", err, stdout)
	}
	if searchOutput.IndexPath != "ephemeral" || searchOutput.Loaded {
		t.Fatalf("fallback robot-search used persistent state: %+v", searchOutput)
	}
	if !strings.Contains(strings.Join(searchOutput.AuthorityIncompleteReasons, "\n"), corruptDBPath) {
		t.Fatalf("fallback robot-search omitted rejected-source evidence: %+v", searchOutput.AuthorityIncompleteReasons)
	}
	if _, err := os.Stat(filepath.Join(dir, ".bv", "semantic")); !os.IsNotExist(err) {
		t.Fatalf("fallback robot-search created persistent semantic index state: %v", err)
	}
}

func TestRobotSearchHonorsLabelScopeAndUsesEphemeralIndex(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_SEMANTIC_EMBEDDER", "hash")
	t.Setenv("BV_SEMANTIC_DIM", "16")
	t.Setenv("BV_NO_CACHE", "1")
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	fixture := `{"id":"BACKEND-1","title":"Backend work","description":"server token","status":"open","issue_type":"task","labels":["backend"]}
{"id":"FRONTEND-1","title":"Frontend outside","description":"outsideunique token","status":"open","issue_type":"task","labels":["frontend"]}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write issues: %v", err)
	}

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(
		t, dir, exe,
		"--search", "outsideunique",
		"--robot-search",
		"--label", "backend",
		"--format=json",
	)
	if err != nil {
		t.Fatalf("label-scoped robot-search failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotSearchOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode label-scoped search: %v\n%s", err, stdout)
	}
	if output.IndexPath != "ephemeral" || output.Loaded {
		t.Fatalf("label-scoped search used persistent index: %+v", output)
	}
	for _, result := range output.Results {
		if result.IssueID != "BACKEND-1" {
			t.Fatalf("label-scoped search leaked outside issue: %+v", output.Results)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".bv", "semantic")); !os.IsNotExist(err) {
		t.Fatalf("label-scoped search created persistent semantic state: %v", err)
	}
}

func TestRobotNextExplicitDBFailsClosedInsteadOfClaimingInCWD(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_NO_CACHE", "1")

	cwd := t.TempDir()
	external := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatalf("mkdir external beads: %v", err)
	}
	externalIssues := filepath.Join(external, "issues.jsonl")
	if err := os.WriteFile(externalIssues, []byte(`{"id":"FOREIGN-1","title":"Foreign work","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write external issues: %v", err)
	}

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, cwd, exe, "--db", externalIssues, "--robot-next", "--format=json")
	if err != nil {
		t.Fatalf("robot-next with explicit source failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode explicit-source robot-next: %v\n%s", err, stdout)
	}
	if output.Actionable || output.ClaimCmd != "" || output.ShowCmd != "" {
		t.Fatalf("explicit-source robot-next emitted cwd mutation commands: %+v", output)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_next_claim_routing_unavailable" {
		t.Fatalf("explicit-source degradation = %+v", output.Degraded)
	}
	if len(output.RepositoryRouteUnavailableReasons) != 1 || !strings.Contains(output.RepositoryRouteUnavailableReasons[0], "--db") {
		t.Fatalf("explicit-source route evidence = %v", output.RepositoryRouteUnavailableReasons)
	}
}

func TestRobotNextNonexistentLabelDoesNotFallBackToWholeProject(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_NO_CACHE", "1")
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"READY-1","title":"Ready","status":"open","priority":1,"issue_type":"task","labels":["backend"]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write issues: %v", err)
	}

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, dir, exe, "--robot-next", "--label", "does-not-exist", "--format=json")
	if err != nil {
		t.Fatalf("robot-next empty label failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode empty-label robot-next: %v\n%s", err, stdout)
	}
	if output.Actionable || output.ID != "" || output.ClaimCmd != "" {
		t.Fatalf("nonexistent label leaked whole-project work: %+v", output)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "no_actionable_recommendation" {
		t.Fatalf("empty-label degradation = %+v", output.Degraded)
	}
}

func TestRobotNextHistoricalDeferralUsesResolvedCommitTime(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_NO_CACHE", "1")
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2024-02-03T04:05:06Z",
			"GIT_COMMITTER_DATE=2024-02-03T04:05:06Z",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	fixture := `{"id":"FUTURE-1","title":"Deferred after snapshot","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","defer_until":"2025-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write historical issues: %v", err)
	}
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "historical deferred issue")

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, repo, exe, "--as-of", "HEAD", "--robot-next", "--format=json")
	if err != nil {
		t.Fatalf("historical robot-next failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode historical robot-next: %v\n%s", err, stdout)
	}
	if output.AnalysisTime != "2024-02-03T04:05:06Z" {
		t.Fatalf("historical analysis_time = %q", output.AnalysisTime)
	}
	if output.Actionable || output.DiagnosticTopPick != nil || output.ClaimCmd != "" {
		t.Fatalf("historically deferred issue became actionable at invocation time: %+v", output)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "no_actionable_recommendation" {
		t.Fatalf("historical deferral degradation = %+v", output.Degraded)
	}
}

func TestHistoricalRobotSearchUsesEphemeralIndexWithoutMutatingLiveIndex(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_SEMANTIC_EMBEDDER", "hash")
	t.Setenv("BV_SEMANTIC_DIM", "16")
	t.Setenv("BV_NO_CACHE", "1")

	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2024-02-03T04:05:06Z",
			"GIT_COMMITTER_DATE=2024-02-03T04:05:06Z",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	historicalFixture := `{"id":"HIST-1","title":"Historical snapshot token","status":"open","priority":1,"issue_type":"task","updated_at":"2024-01-04T04:05:06Z"}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(historicalFixture), 0o644); err != nil {
		t.Fatalf("write historical issues: %v", err)
	}
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "historical search fixture")

	liveFixture := `{"id":"LIVE-1","title":"Live workspace token","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(liveFixture), 0o644); err != nil {
		t.Fatalf("write live issues: %v", err)
	}

	exe := buildTestBinary(t)
	liveStdout, liveStderr, err := runCommandWithTimeout(t, repo, exe, "--search", "live workspace", "--robot-search", "--format=json")
	if err != nil {
		t.Fatalf("live robot-search failed: %v\nstdout:\n%s\nstderr:\n%s", err, liveStdout, liveStderr)
	}
	var liveOutput robotSearchOutput
	if err := json.Unmarshal([]byte(liveStdout), &liveOutput); err != nil {
		t.Fatalf("decode live robot-search: %v\n%s", err, liveStdout)
	}
	if liveOutput.IndexPath == "" || liveOutput.IndexPath == "ephemeral" {
		t.Fatalf("live robot-search index_path = %q, want persistent index", liveOutput.IndexPath)
	}
	liveIndexBefore, err := os.ReadFile(liveOutput.IndexPath)
	if err != nil {
		t.Fatalf("read live search index before historical query: %v", err)
	}

	historicalStdout, historicalStderr, err := runCommandWithTimeout(
		t,
		repo,
		exe,
		"--as-of", "HEAD",
		"--search", "historical snapshot",
		"--search-mode", "hybrid",
		"--search-weights", `{"text":0,"pagerank":0,"status":0,"impact":0,"priority":0,"recency":1}`,
		"--robot-search",
		"--format=json",
	)
	if err != nil {
		t.Fatalf("historical robot-search failed: %v\nstdout:\n%s\nstderr:\n%s", err, historicalStdout, historicalStderr)
	}
	var historicalOutput robotSearchOutput
	if err := json.Unmarshal([]byte(historicalStdout), &historicalOutput); err != nil {
		t.Fatalf("decode historical robot-search: %v\n%s", err, historicalStdout)
	}
	if historicalOutput.IndexPath != "ephemeral" {
		t.Fatalf("historical robot-search index_path = %q, want ephemeral", historicalOutput.IndexPath)
	}
	if historicalOutput.Loaded {
		t.Fatal("historical robot-search reported loading the live persistent index")
	}
	if historicalOutput.AnalysisTime != "2024-02-03T04:05:06Z" {
		t.Fatalf("historical robot-search analysis_time = %q", historicalOutput.AnalysisTime)
	}
	if len(historicalOutput.Results) == 0 || historicalOutput.Results[0].IssueID != "HIST-1" {
		t.Fatalf("historical robot-search results = %+v, want HIST-1", historicalOutput.Results)
	}
	if got, want := historicalOutput.Results[0].ComponentScores["recency"], math.Exp(-1); math.Abs(got-want) > 1e-12 {
		t.Fatalf("historical robot-search recency = %.15f, want pinned-clock %.15f", got, want)
	}
	liveIndexAfter, err := os.ReadFile(liveOutput.IndexPath)
	if err != nil {
		t.Fatalf("read live search index after historical query: %v", err)
	}
	if !bytes.Equal(liveIndexBefore, liveIndexAfter) {
		t.Fatal("historical robot-search mutated the persistent live search index")
	}
}

func TestRobotSearchArtifactsStayAtInvokingRepoRootAcrossRedirect(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_SEMANTIC_EMBEDDER", "hash")
	t.Setenv("BV_SEMANTIC_DIM", "16")
	t.Setenv("BV_NO_CACHE", "1")

	repo := t.TempDir()
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = repo
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	nested := filepath.Join(repo, "nested", "work")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested cwd: %v", err)
	}

	localBeads := filepath.Join(repo, ".beads")
	if err := os.Mkdir(localBeads, 0o755); err != nil {
		t.Fatalf("mkdir local beads: %v", err)
	}
	trackerRoot := t.TempDir()
	trackerBeads := filepath.Join(trackerRoot, ".beads")
	if err := os.Mkdir(trackerBeads, 0o755); err != nil {
		t.Fatalf("mkdir redirected tracker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localBeads, "redirect"), []byte(trackerBeads+"\n"), 0o644); err != nil {
		t.Fatalf("write tracker redirect: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(trackerBeads, "issues.jsonl"),
		[]byte(`{"id":"REDIRECT-1","title":"Redirected search token","status":"open","issue_type":"task"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write redirected issues: %v", err)
	}
	writeCurrentBRMetadata(t, trackerBeads)

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, nested, exe, "--search", "redirected token", "--robot-search", "--format=json")
	if err != nil {
		t.Fatalf("nested redirected robot-search failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var output robotSearchOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode robot-search: %v\n%s", err, stdout)
	}
	wantPrefix := filepath.Join(repo, ".bv", "semantic") + string(os.PathSeparator)
	if !strings.HasPrefix(output.IndexPath, wantPrefix) {
		t.Fatalf("index path = %q, want invoking repo prefix %q", output.IndexPath, wantPrefix)
	}
	for _, wrongRoot := range []string{nested, trackerRoot} {
		if _, err := os.Stat(filepath.Join(wrongRoot, ".bv")); !os.IsNotExist(err) {
			t.Fatalf("robot-search wrote artifacts under %s: %v", wrongRoot, err)
		}
	}
}

func TestAsOfRejectsExplicitIssueSourceSelector(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	external := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(external, []byte(`{"id":"FOREIGN-1","title":"Foreign","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write external source: %v", err)
	}
	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, t.TempDir(), exe, "--db", external, "--as-of", "HEAD", "--robot-insights")
	if err == nil {
		t.Fatalf("--as-of with --db unexpectedly succeeded: %s", stdout)
	}
	if !strings.Contains(stderr, "cannot safely combine") || !strings.Contains(stderr, "--db") {
		t.Fatalf("explicit historical selector error = %q", stderr)
	}
}

func TestRobotNextClaimablePickRejectsAssignedTopPick(t *testing.T) {
	picks := []analysis.TopPick{{
		ID:    "ASSIGNED-1",
		Title: "Already owned",
		Score: 100,
	}}
	issues := []model.Issue{{
		ID:        "ASSIGNED-1",
		Title:     "Already owned",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Assignee:  " cc11 ",
	}}

	_, diagnostic, reasons, ok := robotNextClaimablePick(picks, issues, time.Time{})
	if ok {
		t.Fatalf("assigned top pick must not be claimable")
	}
	if diagnostic == nil || diagnostic.ID != "ASSIGNED-1" {
		t.Fatalf("diagnostic = %+v, want ASSIGNED-1", diagnostic)
	}
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), "assigned") {
		t.Fatalf("reasons = %v, want assigned reason", reasons)
	}
}

func TestRobotNextUsesUnfilteredAuthorityForOpenChildGate(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_SKIP_PHASE2", "")
	t.Setenv("BV_PHASE2_TIMEOUT_S", "")
	t.Setenv("BV_NO_CACHE", "1")

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	parent := model.Issue{
		ID:        "PARENT-1",
		Title:     "Planning container",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Priority:  0,
	}
	child := model.Issue{
		ID:        "CHILD-1",
		Title:     "Recipe-hidden child",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Priority:  1,
		Dependencies: []*model.Dependency{{
			DependsOnID: parent.ID,
			Type:        model.DepParentChild,
		}},
	}
	picks := []analysis.TopPick{{ID: parent.ID, Title: parent.Title, Score: 100}}
	if _, _, _, ok := robotNextClaimablePick(picks, []model.Issue{parent}, now); !ok {
		t.Fatal("fixture precondition failed: filtered parent alone should look claimable")
	}
	if _, _, reasons, ok := robotNextClaimablePick(picks, []model.Issue{parent, child}, now); ok {
		t.Fatal("parent with an open child in the authoritative graph must not be claimable")
	} else if !strings.Contains(strings.Join(reasons, "; "), "open child work") {
		t.Fatalf("authoritative rejection reasons = %v, want open-child explanation", reasons)
	}

	var encoded strings.Builder
	if err := handleRobotNextAt(RobotContext{
		Issues:              []model.Issue{parent},
		AuthoritativeIssues: []model.Issue{parent, child},
		DataHash:            "filtered-fixture",
		Encoder:             json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{}, now); err != nil {
		t.Fatalf("handle robot-next with unfiltered authority: %v", err)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode robot-next output: %v\n%s", err, encoded.String())
	}
	if output.Actionable || output.ClaimCmd != "" {
		t.Fatalf("filtered parent received an unsafe claim: %+v", output)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_next_claim_unsafe" {
		t.Fatalf("degradation = %+v, want robot_next_claim_unsafe", output.Degraded)
	}
}

func TestRobotNextFailsClosedOnIncompleteAuthoritativeLoad(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_SKIP_PHASE2", "")
	t.Setenv("BV_PHASE2_TIMEOUT_S", "")
	t.Setenv("BV_NO_CACHE", "1")

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	ready := model.Issue{
		ID:        "READY-1",
		Title:     "Apparently ready",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
	}
	var encoded strings.Builder
	if err := handleRobotNextAt(RobotContext{
		Issues:              []model.Issue{ready},
		AuthoritativeIssues: []model.Issue{ready},
		LoadStats: &RobotLoadStats{
			SourcePath: ".beads/issues.jsonl",
			Valid:      1,
			Errors:     1,
		},
		DataHash: "partial-fixture",
		Encoder:  json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{}, now); err != nil {
		t.Fatalf("handle robot-next with incomplete load: %v", err)
	}
	var output robotNextOutput
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode robot-next output: %v\n%s", err, encoded.String())
	}
	if output.Actionable || output.ClaimCmd != "" {
		t.Fatalf("partial issue load received an unsafe claim: %+v", output)
	}
	if output.LoadStats == nil || output.LoadStats.Errors != 1 {
		t.Fatalf("load stats = %+v, want one surfaced error", output.LoadStats)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_next_load_incomplete" {
		t.Fatalf("degradation = %+v, want robot_next_load_incomplete", output.Degraded)
	}

	// Exercise the real loader-to-dispatch seam as well: keep the malformed
	// rate below the loader's rejection threshold so issues are returned, but
	// require robot-next to treat the surfaced dropped record as no-claim.
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	var records strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&records, "{\"id\":\"READY-%d\",\"title\":\"Ready %d\",\"status\":\"open\",\"priority\":%d,\"issue_type\":\"task\"}\n", i, i, i)
	}
	records.WriteString("{\"id\":\"BROKEN\"\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(records.String()), 0o644); err != nil {
		t.Fatalf("write partial fixture: %v", err)
	}

	exe := buildTestBinary(t)
	cmd := exec.Command(exe, "--robot-next", "--format=json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on partial load: %v", err)
	}
	var endToEnd robotNextOutput
	if err := json.Unmarshal(encodedOutput, &endToEnd); err != nil {
		t.Fatalf("decode partial-load robot-next: %v\n%s", err, encodedOutput)
	}
	if endToEnd.Actionable || endToEnd.ClaimCmd != "" {
		t.Fatalf("partial loader seam emitted a claim: %+v", endToEnd)
	}
	if endToEnd.LoadStats == nil || endToEnd.LoadStats.Errors != 1 {
		t.Fatalf("loader seam load stats = %+v, want one error", endToEnd.LoadStats)
	}
	if len(endToEnd.Degraded) != 1 || endToEnd.Degraded[0].Code != "robot_next_load_incomplete" {
		t.Fatalf("loader seam degradation = %+v", endToEnd.Degraded)
	}

	// The historical --as-of path must preserve the same authority evidence,
	// including through GitLoader's revision cache. Otherwise a dropped blocker
	// or child could make an apparently ready historical issue claimable.
	runGit := func(args ...string) {
		t.Helper()
		gitCmd := exec.Command("git", args...)
		gitCmd.Dir = dir
		if out, runErr := gitCmd.CombinedOutput(); runErr != nil {
			t.Fatalf("git %v: %v\n%s", args, runErr, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "record partial historical authority")
	cmd = exec.Command(exe, "--as-of", "HEAD", "--robot-next", "--format=json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err = cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on partial historical load: %v", err)
	}
	var historicalOutput robotNextOutput
	if err := json.Unmarshal(encodedOutput, &historicalOutput); err != nil {
		t.Fatalf("decode partial historical robot-next: %v\n%s", err, encodedOutput)
	}
	if historicalOutput.Actionable || historicalOutput.ClaimCmd != "" {
		t.Fatalf("partial historical authority emitted a claim: %+v", historicalOutput)
	}
	if historicalOutput.LoadStats == nil || historicalOutput.LoadStats.Errors != 1 || !strings.Contains(historicalOutput.LoadStats.SourcePath, ":.beads/issues.jsonl") {
		t.Fatalf("historical loader seam lost authority evidence: %+v", historicalOutput.LoadStats)
	}
	if len(historicalOutput.Degraded) != 1 || historicalOutput.Degraded[0].Code != "robot_next_load_incomplete" {
		t.Fatalf("historical loader degradation = %+v, want robot_next_load_incomplete", historicalOutput.Degraded)
	}

	cleanHistorical := `{"id":"HISTORICAL-READY","title":"Historically ready","status":"open","priority":0,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(cleanHistorical), 0o644); err != nil {
		t.Fatalf("write clean historical fixture: %v", err)
	}
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "record clean historical authority")
	cmd = exec.Command(exe, "--as-of", "HEAD", "--robot-next", "--format=json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err = cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on clean historical load: %v", err)
	}
	historicalOutput = robotNextOutput{}
	if err := json.Unmarshal(encodedOutput, &historicalOutput); err != nil {
		t.Fatalf("decode clean historical robot-next: %v\n%s", err, encodedOutput)
	}
	if historicalOutput.Actionable || historicalOutput.ClaimCmd != "" || historicalOutput.ShowCmd != "" {
		t.Fatalf("historical snapshot emitted a live mutation command: %+v", historicalOutput)
	}
	if historicalOutput.LoadStats != nil {
		t.Fatalf("clean historical snapshot reported parse gaps: %+v", historicalOutput.LoadStats)
	}
	if len(historicalOutput.Degraded) != 1 || historicalOutput.Degraded[0].Code != "robot_next_claim_routing_unavailable" {
		t.Fatalf("clean historical routing degradation = %+v", historicalOutput.Degraded)
	}

	// Workspace aggregation deliberately tolerates an individual repository
	// failure when at least one other repository loads. That is useful for
	// inspection, but it is not enough authority to emit a claim command: the
	// absent repository may contain a blocker, parent, assignment, or duplicate
	// identifier relevant to the apparent top pick.
	workspaceDir := t.TempDir()
	workspaceBVDir := filepath.Join(workspaceDir, ".bv")
	if err := os.MkdirAll(workspaceBVDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace metadata: %v", err)
	}
	goodBeadsDir := filepath.Join(workspaceDir, "good", ".beads")
	if err := os.MkdirAll(goodBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir good workspace repo: %v", err)
	}
	goodRecord := `{"id":"READY-1","title":"Apparently ready","status":"open","priority":0,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(goodBeadsDir, "issues.jsonl"), []byte(goodRecord), 0o644); err != nil {
		t.Fatalf("write good workspace repo: %v", err)
	}
	workspaceConfig := `repos:
  - name: good
    path: good
    prefix: good-
  - name: missing
    path: missing
    prefix: missing-
`
	workspaceConfigPath := filepath.Join(workspaceBVDir, "workspace.yaml")
	if err := os.WriteFile(workspaceConfigPath, []byte(workspaceConfig), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	cmd = exec.Command(exe, "--workspace", workspaceConfigPath, "--robot-next", "--format=json")
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err = cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on partial workspace: %v", err)
	}
	var workspaceOutput robotNextOutput
	if err := json.Unmarshal(encodedOutput, &workspaceOutput); err != nil {
		t.Fatalf("decode partial-workspace robot-next: %v\n%s", err, encodedOutput)
	}
	if workspaceOutput.Actionable || workspaceOutput.ClaimCmd != "" {
		t.Fatalf("partial workspace emitted a claim: %+v", workspaceOutput)
	}
	if workspaceOutput.LoadStats != nil {
		t.Fatalf("partial workspace inherited unrelated single-source load stats: %+v", workspaceOutput.LoadStats)
	}
	if workspaceOutput.DiagnosticTopPick == nil || workspaceOutput.DiagnosticTopPick.ID != "good-READY-1" {
		t.Fatalf("partial workspace diagnostic = %+v, want apparent top pick good-READY-1", workspaceOutput.DiagnosticTopPick)
	}
	if len(workspaceOutput.Degraded) != 1 || workspaceOutput.Degraded[0].Code != "robot_next_authority_incomplete" {
		t.Fatalf("partial workspace degradation = %+v", workspaceOutput.Degraded)
	}
	if !strings.Contains(workspaceOutput.Degraded[0].Message, "missing") {
		t.Fatalf("partial workspace degradation omitted failed repo: %+v", workspaceOutput.Degraded)
	}

	completeWorkspaceConfigPath := filepath.Join(workspaceBVDir, "workspace-complete.yaml")
	completeWorkspaceConfig := `repos:
  - name: good
    path: good
    prefix: good-
`
	if err := os.WriteFile(completeWorkspaceConfigPath, []byte(completeWorkspaceConfig), 0o644); err != nil {
		t.Fatalf("write complete workspace config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodBeadsDir, "issues.jsonl"), []byte(goodRecord+"{\"id\":\"BROKEN\"\n"), 0o644); err != nil {
		t.Fatalf("write partial successful workspace repository: %v", err)
	}
	cmd = exec.Command(exe, "--workspace", completeWorkspaceConfigPath, "--robot-next", "--format=json")
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err = cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on workspace parse gap: %v", err)
	}
	if err := json.Unmarshal(encodedOutput, &workspaceOutput); err != nil {
		t.Fatalf("decode workspace parse-gap robot-next: %v\n%s", err, encodedOutput)
	}
	if workspaceOutput.Actionable || workspaceOutput.ClaimCmd != "" {
		t.Fatalf("workspace parse gap emitted a claim: %+v", workspaceOutput)
	}
	if len(workspaceOutput.Degraded) != 1 || workspaceOutput.Degraded[0].Code != "robot_next_authority_incomplete" || !strings.Contains(workspaceOutput.Degraded[0].Message, "parsing dropped records") {
		t.Fatalf("workspace parse-gap degradation = %+v", workspaceOutput.Degraded)
	}

	if err := os.WriteFile(filepath.Join(goodBeadsDir, "issues.jsonl"), []byte(goodRecord), 0o644); err != nil {
		t.Fatalf("restore clean workspace repository: %v", err)
	}
	cmd = exec.Command(exe, "--workspace", completeWorkspaceConfigPath, "--robot-next", "--format=json")
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_CACHE=1")
	encodedOutput, err = cmd.Output()
	if err != nil {
		t.Fatalf("robot-next on complete workspace: %v", err)
	}
	if err := json.Unmarshal(encodedOutput, &workspaceOutput); err != nil {
		t.Fatalf("decode complete-workspace robot-next: %v\n%s", err, encodedOutput)
	}
	if workspaceOutput.Actionable || workspaceOutput.ClaimCmd != "" || workspaceOutput.ShowCmd != "" {
		t.Fatalf("viewer-namespaced workspace emitted an unroutable command: %+v", workspaceOutput)
	}
	if workspaceOutput.DiagnosticTopPick == nil || workspaceOutput.DiagnosticTopPick.ID != "good-READY-1" {
		t.Fatalf("complete workspace diagnostic = %+v, want good-READY-1", workspaceOutput.DiagnosticTopPick)
	}
	if len(workspaceOutput.Degraded) != 1 || workspaceOutput.Degraded[0].Code != "robot_next_claim_routing_unavailable" {
		t.Fatalf("complete workspace routing degradation = %+v", workspaceOutput.Degraded)
	}
}

func TestRobotTriageScrubsClaimSignalsWithoutCompleteRoutingAuthority(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_SKIP_PHASE2", "1")
	t.Setenv("BV_NO_CACHE", "1")

	ready := model.Issue{
		ID:        "READY-1",
		Title:     "Apparently ready",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Labels:    []string{"backend"},
	}
	blocked := model.Issue{
		ID:        "BLOCKED-1",
		Title:     "Blocked downstream work",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Dependencies: []*model.Dependency{{
			DependsOnID: ready.ID,
			Type:        model.DepBlocks,
		}},
	}
	byTrack, byLabel := true, true
	ctx := RobotContext{
		Issues:              []model.Issue{ready, blocked},
		AuthoritativeIssues: []model.Issue{ready, blocked},
		LoadStats: &RobotLoadStats{
			SourcePath: ".beads/issues.jsonl",
			Valid:      2,
			Errors:     1,
		},
		AuthorityIncompleteReasons: []string{"configured repository missing failed to load"},
		ClaimCommandUnavailableReasons: []string{
			"viewer ID has no verified live mutation route",
		},
		DataHash: "partial-workspace-fixture",
	}

	var encoded strings.Builder
	ctx.Encoder = json.NewEncoder(&encoded)
	if err := handleRobotTriage(ctx, phaseThreeRobotHandlerConfig{
		RobotTriageByTrackFlag: &byTrack,
		RobotTriageByLabelFlag: &byLabel,
	}); err != nil {
		t.Fatalf("handle authority-limited robot-triage: %v", err)
	}
	fullJSON := encoded.String()
	if !strings.Contains(fullJSON, `"top_picks":[]`) {
		t.Fatalf("authority-limited full triage serialized top_picks as non-array: %s", fullJSON)
	}
	var fullOutput struct {
		LoadStats *RobotLoadStats        `json:"load_stats"`
		Degraded  []robotNextDegradation `json:"degraded"`
		Triage    analysis.TriageResult  `json:"triage"`
	}
	if err := json.Unmarshal([]byte(fullJSON), &fullOutput); err != nil {
		t.Fatalf("decode authority-limited robot-triage: %v\n%s", err, fullJSON)
	}
	if fullOutput.LoadStats == nil || fullOutput.LoadStats.Errors != 1 {
		t.Fatalf("load stats = %+v, want the authoritative dropped-record evidence", fullOutput.LoadStats)
	}
	if len(fullOutput.Triage.Recommendations) == 0 {
		t.Fatal("diagnostic recommendations were removed along with claim signals")
	}
	for _, recommendation := range fullOutput.Triage.Recommendations {
		if strings.Contains(strings.ToLower(recommendation.Action), "start work") {
			t.Fatalf("authority-limited recommendation retained a live action: %+v", recommendation)
		}
		joinedReasons := strings.ToLower(strings.Join(recommendation.Reasons, "; "))
		if strings.Contains(joinedReasons, "currently unclaimed") || strings.Contains(joinedReasons, "available for work") {
			t.Fatalf("authority-limited recommendation retained live claim language: %+v", recommendation)
		}
		if !strings.Contains(joinedReasons, "diagnostic only") {
			t.Fatalf("authority-limited recommendation lacks diagnostic boundary: %+v", recommendation)
		}
	}
	if len(fullOutput.Triage.QuickRef.TopPicks) != 0 {
		t.Fatalf("authority-limited quick-ref still advertises claimable work: %+v", fullOutput.Triage.QuickRef.TopPicks)
	}
	if fullOutput.Triage.Commands != (analysis.CommandHelpers{}) {
		t.Fatalf("authority-limited triage emitted live commands: %+v", fullOutput.Triage.Commands)
	}
	if len(fullOutput.Triage.BlockersToClear) == 0 {
		t.Fatal("fixture did not produce blockers_to_clear in full triage")
	}
	for _, blocker := range fullOutput.Triage.BlockersToClear {
		if blocker.Actionable {
			t.Fatalf("authority-limited full blocker retained actionable=true: %+v", blocker)
		}
	}
	for _, group := range fullOutput.Triage.RecommendationsByTrack {
		if group.TopPick != nil || group.ClaimCommand != "" {
			t.Fatalf("track group retained a claim signal: %+v", group)
		}
	}
	for _, group := range fullOutput.Triage.RecommendationsByLabel {
		if group.TopPick != nil || group.ClaimCommand != "" {
			t.Fatalf("label group retained a claim signal: %+v", group)
		}
	}
	wantCodes := map[string]bool{
		"robot_triage_authority_incomplete":      false,
		"robot_triage_claim_routing_unavailable": false,
		"robot_triage_metric_incomplete":         false,
	}
	for _, degradation := range fullOutput.Degraded {
		if _, ok := wantCodes[degradation.Code]; ok {
			wantCodes[degradation.Code] = true
		}
	}
	for code, found := range wantCodes {
		if !found {
			t.Fatalf("degradations = %+v, missing %s", fullOutput.Degraded, code)
		}
	}

	brief := true
	ctx.LoadStats = nil
	ctx.AuthorityIncompleteReasons = nil
	ctx.Encoder = json.NewEncoder(&encoded)
	encoded.Reset()
	if err := handleRobotTriage(ctx, phaseThreeRobotHandlerConfig{RobotTriageBriefFlag: &brief}); err != nil {
		t.Fatalf("handle routing-limited brief triage: %v", err)
	}
	briefJSON := encoded.String()
	if !strings.Contains(briefJSON, `"top_picks":[]`) {
		t.Fatalf("routing-limited brief triage serialized top_picks as non-array: %s", briefJSON)
	}
	var briefOutput briefTriageOutput
	if err := json.Unmarshal([]byte(briefJSON), &briefOutput); err != nil {
		t.Fatalf("decode routing-limited brief triage: %v\n%s", err, briefJSON)
	}
	if len(briefOutput.Recommendations) == 0 {
		t.Fatal("brief diagnostic recommendations were removed along with claim signals")
	}
	if len(briefOutput.QuickRef.TopPicks) != 0 {
		t.Fatalf("routing-limited brief triage advertises claimable work: %+v", briefOutput.QuickRef.TopPicks)
	}
	if len(briefOutput.BlockersToClear) == 0 {
		t.Fatal("fixture did not produce blockers_to_clear in brief triage")
	}
	for _, blocker := range briefOutput.BlockersToClear {
		if blocker.Actionable {
			t.Fatalf("routing-limited brief blocker retained actionable=true: %+v", blocker)
		}
	}
	briefCodes := make(map[string]bool, len(briefOutput.Degraded))
	for _, degradation := range briefOutput.Degraded {
		briefCodes[degradation.Code] = true
	}
	if !briefCodes["robot_triage_claim_routing_unavailable"] || !briefCodes["robot_triage_metric_incomplete"] {
		t.Fatalf("brief degradation = %+v, want routing and metric evidence", briefOutput.Degraded)
	}
}

func TestRobotTriageMetricGateScrubsClaimSignals(t *testing.T) {
	triage := analysis.TriageResult{
		Meta: analysis.TriageMeta{GeneratedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)},
		QuickRef: analysis.QuickRef{TopPicks: []analysis.TopPick{{
			ID: "READY-1", Title: "Apparently ready", Score: 100,
		}}},
		Commands: analysis.CommandHelpers{ClaimTop: "CI=1 br update READY-1 --status in_progress --json"},
	}
	degraded := applyRobotTriageAuthorityPolicy(RobotContext{Issues: []model.Issue{{
		ID: "READY-1", Status: model.StatusOpen, IssueType: model.TypeTask,
	}}}, &triage)
	if len(triage.QuickRef.TopPicks) != 0 || triage.Commands != (analysis.CommandHelpers{}) {
		t.Fatalf("metric-incomplete triage retained claim signals: %+v %+v", triage.QuickRef.TopPicks, triage.Commands)
	}
	if len(degraded) != 1 || degraded[0].Code != "robot_triage_metric_incomplete" {
		t.Fatalf("metric degradation = %+v, want robot_triage_metric_incomplete", degraded)
	}
}

func TestRobotBlockerChainAuthorityGapScrubsActionability(t *testing.T) {
	targetID := "TARGET-1"
	issues := []model.Issue{
		{
			ID:        targetID,
			Title:     "Blocked target",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{
				DependsOnID: "BLOCKER-1",
				Type:        model.DepBlocks,
			}},
		},
		{
			ID:        "BLOCKER-1",
			Title:     "Normally actionable root blocker",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
		},
	}
	var encoded strings.Builder
	if err := handleRobotBlockerChain(RobotContext{
		Issues:                         issues,
		DataHash:                       analysis.ComputeDataHash(issues),
		ClaimCommandUnavailableReasons: []string{"the issue source cannot be routed to a live repository"},
		Encoder:                        json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{RobotBlockerChainFlag: &targetID}); err != nil {
		t.Fatalf("handle authority-limited robot-blocker-chain: %v", err)
	}
	var output struct {
		Result   *analysis.BlockerChainResult `json:"result"`
		Degraded []robotNextDegradation       `json:"degraded"`
	}
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode authority-limited robot-blocker-chain: %v\n%s", err, encoded.String())
	}
	if output.Result == nil || len(output.Result.RootBlockers) != 1 {
		t.Fatalf("blocker-chain result = %+v, want one root blocker", output.Result)
	}
	for _, entry := range output.Result.RootBlockers {
		if entry.Actionable {
			t.Fatalf("authority-limited root blocker retained actionable=true: %+v", entry)
		}
	}
	for _, entry := range output.Result.Chain {
		if entry.Actionable {
			t.Fatalf("authority-limited chain entry retained actionable=true: %+v", entry)
		}
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_blocker_chain_authority_incomplete" {
		t.Fatalf("blocker-chain degradation = %+v", output.Degraded)
	}
}

func TestRobotTriageUsesUnfilteredAuthorityForOpenChildGate(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	parent := model.Issue{
		ID:        "PARENT-1",
		Title:     "Planning container",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
	}
	child := model.Issue{
		ID:        "CHILD-1",
		Title:     "Filtered child",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Dependencies: []*model.Dependency{{
			DependsOnID: parent.ID,
			Type:        model.DepParentChild,
		}},
	}
	safe := model.Issue{
		ID:        "SAFE-1",
		Title:     "Safe leaf",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
	}
	parentPick := analysis.TopPick{ID: parent.ID, Title: parent.Title, Score: 100}
	safePick := analysis.TopPick{ID: safe.ID, Title: safe.Title, Score: 90}
	triage := analysis.TriageResult{
		QuickRef: analysis.QuickRef{TopPicks: []analysis.TopPick{parentPick, safePick}},
		Commands: analysis.CommandHelpers{
			ClaimTop:      "CI=1 br update PARENT-1 --status in_progress --json",
			ShowTop:       "CI=1 br show PARENT-1 --json",
			ListReady:     "CI=1 br ready --json",
			RefreshTriage: "bv --robot-triage",
		},
		RecommendationsByTrack: []analysis.TrackRecommendationGroup{{
			TrackID:      "track-A",
			TopPick:      &parentPick,
			ClaimCommand: "CI=1 br update PARENT-1 --status in_progress --json",
		}},
		RecommendationsByLabel: []analysis.LabelRecommendationGroup{{
			Label:        "safe",
			TopPick:      &safePick,
			ClaimCommand: "CI=1 br update SAFE-1 --status in_progress --json",
		}},
	}
	reasons := filterRobotTriageClaimsByAuthority(&triage, []model.Issue{parent, child, safe}, []model.Issue{parent, safe}, now)
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), "open child work") {
		t.Fatalf("authority filter reasons = %v, want open-child evidence", reasons)
	}
	if len(triage.QuickRef.TopPicks) != 1 || triage.QuickRef.TopPicks[0].ID != safe.ID {
		t.Fatalf("authority-filtered picks = %+v, want only SAFE-1", triage.QuickRef.TopPicks)
	}
	if !strings.Contains(triage.Commands.ClaimTop, safe.ID) || strings.Contains(triage.Commands.ClaimTop, parent.ID) {
		t.Fatalf("authority-filtered commands = %+v, want SAFE-1", triage.Commands)
	}
	if triage.Commands.ListReady == "" || triage.Commands.RefreshTriage == "" {
		t.Fatalf("safe live helper commands were unnecessarily removed: %+v", triage.Commands)
	}
	track := triage.RecommendationsByTrack[0]
	if track.TopPick != nil || track.ClaimCommand != "" {
		t.Fatalf("unsafe track claim survived authority filter: %+v", track)
	}
	label := triage.RecommendationsByLabel[0]
	if label.TopPick == nil || label.TopPick.ID != safe.ID || !strings.Contains(label.ClaimCommand, safe.ID) {
		t.Fatalf("safe label claim was lost: %+v", label)
	}
}

func TestRobotTriagePolicyWiresUnfilteredAuthorityAfterCompleteMetrics(t *testing.T) {
	t.Setenv("BV_SKIP_PHASE2", "")
	t.Setenv("BV_PHASE2_TIMEOUT_S", "")
	t.Setenv("BV_NO_CACHE", "1")
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", fmt.Sprintf("%d", now.Unix()))

	safeA := model.Issue{ID: "SAFE-A", Title: "Safe leaf A", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 1}
	safeB := model.Issue{ID: "SAFE-B", Title: "Safe leaf B", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 1}
	safeC := model.Issue{ID: "SAFE-C", Title: "Safe leaf C", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 1}
	parent := model.Issue{ID: "ZZZ-PARENT", Title: "Planning container", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 1}
	child := model.Issue{
		ID: "CHILD-1", Title: "Filtered child", Status: model.StatusOpen, IssueType: model.TypeTask,
		Dependencies: []*model.Dependency{{DependsOnID: parent.ID, Type: model.DepParentChild}},
	}
	scoped := []model.Issue{safeA, safeB, safeC, parent}
	var encoded strings.Builder
	ctx := RobotContext{
		Issues:                scoped,
		AuthoritativeIssues:   []model.Issue{safeA, safeB, safeC, parent, child},
		DataHash:              analysis.ComputeDataHash(scoped),
		DataHashMatchesIssues: true,
		Encoder:               json.NewEncoder(&encoded),
	}
	if err := handleRobotTriage(ctx, phaseThreeRobotHandlerConfig{}); err != nil {
		t.Fatalf("handle robot-triage: %v", err)
	}
	var output struct {
		Degraded []robotNextDegradation `json:"degraded"`
		Triage   analysis.TriageResult  `json:"triage"`
	}
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode robot-triage: %v\n%s", err, encoded.String())
	}
	if reasons := robotNextMetricUnsafeReasons(output.Triage.Meta.Phase2Ready, output.Triage.Status); len(reasons) != 0 {
		t.Fatalf("handler metrics incomplete: %v", reasons)
	}
	if len(output.Triage.QuickRef.TopPicks) != 3 {
		t.Fatalf("handler top picks = %+v, want three safe leaves", output.Triage.QuickRef.TopPicks)
	}
	for _, pick := range output.Triage.QuickRef.TopPicks {
		if pick.ID == parent.ID {
			t.Fatalf("lower-ranked unsafe parent survived as a top pick: %+v", output.Triage.QuickRef.TopPicks)
		}
	}

	var parentRecommendation, safeRecommendation *analysis.Recommendation
	for i := range output.Triage.Recommendations {
		switch output.Triage.Recommendations[i].ID {
		case parent.ID:
			parentRecommendation = &output.Triage.Recommendations[i]
		case safeA.ID:
			safeRecommendation = &output.Triage.Recommendations[i]
		}
	}
	if parentRecommendation == nil {
		t.Fatalf("unsafe lower-ranked parent missing from diagnostic recommendations: %+v", output.Triage.Recommendations)
	}
	if !strings.Contains(parentRecommendation.Action, "Inspect only") {
		t.Fatalf("unsafe lower-ranked recommendation retained live action: %+v", *parentRecommendation)
	}
	parentReasons := strings.ToLower(strings.Join(parentRecommendation.Reasons, "; "))
	if strings.Contains(parentReasons, "available for work") || !strings.Contains(parentReasons, "authoritative issue graph") {
		t.Fatalf("unsafe lower-ranked recommendation reasons were not sanitized: %+v", *parentRecommendation)
	}
	if safeRecommendation == nil || strings.Contains(safeRecommendation.Action, "Inspect only") {
		t.Fatalf("safe recommendation was unnecessarily sanitized: %+v", safeRecommendation)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_triage_claim_unsafe" {
		t.Fatalf("handler degradation = %+v, want robot_triage_claim_unsafe", output.Degraded)
	}
}

func TestRobotNextClaimablePickRejectsCommandUnsafeID(t *testing.T) {
	for _, id := range []string{"--help", "READY-1\nprintf ignored", "READY-1\x00suffix"} {
		id := id
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			picks := []analysis.TopPick{{ID: id, Title: "Unsafe command argument", Score: 100}}
			issues := []model.Issue{{
				ID:        id,
				Title:     "Unsafe command argument",
				Status:    model.StatusOpen,
				IssueType: model.TypeTask,
			}}

			_, _, reasons, ok := robotNextClaimablePick(picks, issues, time.Time{})
			if ok {
				t.Fatalf("command-unsafe ID %q must not be claimable", id)
			}
			if !strings.Contains(strings.Join(reasons, "; "), "br command argument") {
				t.Fatalf("reasons = %v, want command argument safety reason", reasons)
			}
		})
	}
}

func TestRobotNextMetricUnsafeReasonsRequiresPhase2Readiness(t *testing.T) {
	reasons := robotNextMetricUnsafeReasons(false, analysis.MetricStatus{})
	if len(reasons) == 0 || reasons[0] != "phase 2 analysis is not ready" {
		t.Fatalf("phase2-not-ready reasons = %v, want explicit readiness failure", reasons)
	}
}

func TestRobotCapacityUsesAnalyzerActionabilityAndBlockingEdgesOnly(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_SKIP_PHASE2", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	issues := []model.Issue{
		{ID: "A-ROOT", Title: "Open blocker", Status: model.StatusOpen, IssueType: model.TypeTask},
		{
			ID: "B-RELATED", Title: "Related work", Status: model.StatusOpen, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "A-ROOT", Type: model.DepRelated}},
		},
		{ID: "C-PARENT-FREE", Title: "Unblocked parent", Status: model.StatusOpen, IssueType: model.TypeTask},
		{
			ID: "D-CHILD-FREE", Title: "Child of unblocked parent", Status: model.StatusOpen, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "C-PARENT-FREE", Type: model.DepParentChild}},
		},
		{
			ID: "E-PARENT-BLOCKED", Title: "Blocked parent", Status: model.StatusOpen, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "A-ROOT", Type: model.DepBlocks}},
		},
		{
			ID: "F-CHILD-PROPAGATED", Title: "Child inherits parent block", Status: model.StatusOpen, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "E-PARENT-BLOCKED", Type: model.DepParentChild}},
		},
		{ID: "G-FUTURE", Title: "Deferred work", Status: model.StatusOpen, IssueType: model.TypeTask, DeferUntil: &future},
	}

	var encoded strings.Builder
	if err := handleRobotCapacity(RobotContext{
		Issues:       issues,
		DataHash:     "caller-owned-capacity-hash",
		AnalysisTime: now,
		Encoder:      json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{}); err != nil {
		t.Fatalf("handle robot-capacity: %v", err)
	}
	var output struct {
		DataHash        string                 `json:"data_hash"`
		CriticalPath    []string               `json:"critical_path"`
		ActionableCount int                    `json:"actionable_count"`
		Actionable      []string               `json:"actionable"`
		Degraded        []robotNextDegradation `json:"degraded"`
	}
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode robot-capacity: %v\n%s", err, encoded.String())
	}
	if output.DataHash != "caller-owned-capacity-hash" {
		t.Fatalf("data_hash = %q, want caller-owned hash", output.DataHash)
	}
	if got, want := strings.Join(output.Actionable, ","), "A-ROOT,B-RELATED,C-PARENT-FREE,D-CHILD-FREE"; got != want {
		t.Fatalf("actionable = %v, want %s", output.Actionable, want)
	}
	if output.ActionableCount != len(output.Actionable) {
		t.Fatalf("actionable_count = %d, actionable = %v", output.ActionableCount, output.Actionable)
	}
	if got, want := strings.Join(output.CriticalPath, ","), "A-ROOT,E-PARENT-BLOCKED"; got != want {
		t.Fatalf("critical_path = %v, want %s; related and parent-child edges must not extend it", output.CriticalPath, want)
	}
	if len(output.Degraded) != 0 {
		t.Fatalf("safe capacity context unexpectedly degraded: %+v", output.Degraded)
	}
}

func TestRobotCapacityScrubsClaimUnsafeActionability(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_SKIP_PHASE2", "1")

	issue := model.Issue{ID: "READY-1", Title: "Ready work", Status: model.StatusOpen, IssueType: model.TypeTask}
	var encoded strings.Builder
	if err := handleRobotCapacity(RobotContext{
		Issues:                         []model.Issue{issue},
		DataHash:                       "unsafe-capacity-hash",
		ClaimCommandUnavailableReasons: []string{"historical snapshot has no live claim route"},
		Encoder:                        json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{}); err != nil {
		t.Fatalf("handle authority-limited robot-capacity: %v", err)
	}
	var output struct {
		DataHash        string                 `json:"data_hash"`
		ActionableCount int                    `json:"actionable_count"`
		Actionable      []string               `json:"actionable"`
		Degraded        []robotNextDegradation `json:"degraded"`
	}
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode authority-limited robot-capacity: %v\n%s", err, encoded.String())
	}
	if output.DataHash != "unsafe-capacity-hash" {
		t.Fatalf("data_hash = %q, want caller-owned hash", output.DataHash)
	}
	if output.Actionable == nil || len(output.Actionable) != 0 || output.ActionableCount != 0 {
		t.Fatalf("claim-unsafe capacity retained actionability: count=%d actionable=%#v", output.ActionableCount, output.Actionable)
	}
	if len(output.Degraded) != 1 || output.Degraded[0].Code != "robot_capacity_actionability_unavailable" {
		t.Fatalf("degraded = %+v, want robot_capacity_actionability_unavailable", output.Degraded)
	}
	if !strings.Contains(output.Degraded[0].Message, "historical snapshot") || output.Degraded[0].Repair == "" {
		t.Fatalf("degradation lacks cause or repair guidance: %+v", output.Degraded[0])
	}
}

func TestRobotCapacitySerializesEmptyCriticalPathAndActionableArrays(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_SKIP_PHASE2", "1")

	var encoded strings.Builder
	if err := handleRobotCapacity(RobotContext{
		DataHash: "empty-capacity-hash",
		Encoder:  json.NewEncoder(&encoded),
	}, phaseThreeRobotHandlerConfig{}); err != nil {
		t.Fatalf("handle empty robot-capacity: %v", err)
	}
	var output struct {
		CriticalPath    []string `json:"critical_path"`
		CriticalPathLen int      `json:"critical_path_length"`
		Actionable      []string `json:"actionable"`
		ActionableCount int      `json:"actionable_count"`
	}
	if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
		t.Fatalf("decode empty robot-capacity: %v\n%s", err, encoded.String())
	}
	if output.CriticalPath == nil || len(output.CriticalPath) != 0 || output.CriticalPathLen != 0 {
		t.Fatalf("critical_path must serialize as [] with zero length, got len=%d value=%#v", output.CriticalPathLen, output.CriticalPath)
	}
	if output.Actionable == nil || len(output.Actionable) != 0 || output.ActionableCount != 0 {
		t.Fatalf("actionable must serialize as [] with zero count, got count=%d value=%#v", output.ActionableCount, output.Actionable)
	}
}

func TestScopedRobotHandlersPreserveContextDataHash(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_SKIP_PHASE2", "1")

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", fmt.Sprintf("%d", now.Unix()))
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir sprint fixture: %v", err)
	}
	sprintJSON := fmt.Sprintf(
		`{"id":"sprint-1","name":"Sprint 1","start_date":%q,"end_date":%q,"bead_ids":["READY-1"]}`+"\n",
		now.Add(-24*time.Hour).Format(time.RFC3339),
		now.Add(24*time.Hour).Format(time.RFC3339),
	)
	if err := os.WriteFile(filepath.Join(beadsDir, loader.SprintsFileName), []byte(sprintJSON), 0o644); err != nil {
		t.Fatalf("write sprint fixture: %v", err)
	}

	estimate := 60
	issues := []model.Issue{{
		ID: "READY-1", Title: "Ready work", Status: model.StatusOpen, IssueType: model.TypeTask,
		EstimatedMinutes: &estimate,
	}}
	const callerHash = "caller-owned-scoped-data-hash"
	if computed := analysis.ComputeDataHash(issues); computed == callerHash {
		t.Fatal("fixture caller hash unexpectedly equals the issue-derived hash")
	}
	baseCtx := RobotContext{
		Issues:       issues,
		DataHash:     callerHash,
		AnalysisTime: now,
		WorkDir:      workDir,
	}

	tests := []struct {
		name string
		run  func(RobotContext) error
	}{
		{
			name: "sprint-list",
			run: func(ctx RobotContext) error {
				active := true
				registry := newRobotRegistry()
				registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotSprintListFlag: &active})
				handled, err := registry.DispatchFlag("robot-sprint-list", ctx)
				if !handled {
					return fmt.Errorf("robot-sprint-list was not dispatched")
				}
				return err
			},
		},
		{
			name: "burndown",
			run: func(ctx RobotContext) error {
				target := "sprint-1"
				registry := newRobotRegistry()
				registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotBurndownFlag: &target})
				handled, err := registry.DispatchFlag("robot-burndown", ctx)
				if !handled {
					return fmt.Errorf("robot-burndown was not dispatched")
				}
				return err
			},
		},
		{
			name: "forecast",
			run: func(ctx RobotContext) error {
				target := "all"
				registry := newRobotRegistry()
				registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotForecastFlag: &target})
				handled, err := registry.DispatchFlag("robot-forecast", ctx)
				if !handled {
					return fmt.Errorf("robot-forecast was not dispatched")
				}
				return err
			},
		},
		{
			name: "sprint-show",
			run: func(ctx RobotContext) error {
				target := "sprint-1"
				return handleRobotSprintShow(ctx, phaseThreeRobotHandlerConfig{RobotSprintShowFlag: &target})
			},
		},
		{
			name: "capacity",
			run: func(ctx RobotContext) error {
				agents := 2
				return handleRobotCapacity(ctx, phaseThreeRobotHandlerConfig{CapacityAgents: &agents})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoded strings.Builder
			ctx := baseCtx
			ctx.Encoder = json.NewEncoder(&encoded)
			if err := test.run(ctx); err != nil {
				t.Fatalf("run scoped handler: %v", err)
			}
			var output struct {
				DataHash string `json:"data_hash"`
			}
			if err := json.Unmarshal([]byte(encoded.String()), &output); err != nil {
				t.Fatalf("decode handler output: %v\n%s", err, encoded.String())
			}
			if output.DataHash != callerHash {
				t.Fatalf("data_hash = %q, want caller-owned %q", output.DataHash, callerHash)
			}
		})
	}
}

// TestTOONRoundTrip verifies that TOON output can be decoded back to JSON (bd-2lmf)
func TestTOONRoundTrip(t *testing.T) {
	// Check if tru binary is available
	truPath, err := exec.LookPath("tru")
	if err != nil {
		t.Skip("tru binary not available, skipping TOON round-trip test")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Round Trip Test","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

	exe := buildTestBinary(t)

	// Get TOON output
	cmd := exec.Command(exe, "--robot-next", "--format=toon")
	cmd.Dir = dir
	toonOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with toon failed: %v", err)
	}

	// Decode TOON back to JSON using tru --decode
	decodeCmd := exec.Command(truPath, "--decode")
	decodeCmd.Stdin = strings.NewReader(string(toonOut))
	jsonOut, err := decodeCmd.Output()
	if err != nil {
		t.Fatalf("tru --decode failed: %v", err)
	}

	// Verify the decoded JSON is valid and contains expected fields
	var payload map[string]interface{}
	if err := json.Unmarshal(jsonOut, &payload); err != nil {
		t.Fatalf("decoded JSON is invalid: %v, content: %s", err, string(jsonOut))
	}

	// Check required fields are present
	if _, ok := payload["id"]; !ok {
		t.Error("decoded payload missing 'id' field")
	}
	if _, ok := payload["title"]; !ok {
		t.Error("decoded payload missing 'title' field")
	}
	if _, ok := payload["generated_at"]; !ok {
		t.Error("decoded payload missing 'generated_at' field")
	}
}

// TestTOONTokenStats verifies that --stats produces token statistics on stderr (bd-2lmf)
func TestTOONTokenStats(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON stats test")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Stats Test Issue","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	// Test --stats flag with TOON output
	cmd := exec.Command(exe, "--robot-next", "--format=toon", "--stats")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with stats failed: %v", err)
	}

	stderrStr := stderr.String()
	// Should contain token statistics
	if !strings.Contains(stderrStr, "tok") || !strings.Contains(stderrStr, "savings") {
		t.Errorf("--stats should produce token statistics on stderr, got: %s", stderrStr)
	}
}

// TestTOONSchemaOutput verifies that --robot-schema works with TOON format (bd-2lmf)
func TestTOONSchemaOutput(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON schema test")
	}

	exe := buildTestBinary(t)

	// Test --robot-schema with TOON format
	cmd := exec.Command(exe, "--robot-schema", "--format=toon")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-schema with toon failed: %v", err)
	}

	toonOut := string(out)
	// Should produce valid TOON output
	if len(toonOut) > 0 && toonOut[0] == '{' {
		t.Fatalf("TOON output looks like JSON, expected TOON format")
	}

	// Should contain schema_version key
	if !strings.Contains(toonOut, "schema_version") {
		t.Error("TOON schema output missing schema_version")
	}
}

// containsKeyValuePattern checks if the string looks like TOON format
func containsKeyValuePattern(s string) bool {
	// TOON format typically has lines like "key: value" without the JSON braces/quotes
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Look for key: value pattern (not JSON's "key": value)
		if strings.Contains(trimmed, ": ") && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "\"") {
			return true
		}
	}
	return false
}
