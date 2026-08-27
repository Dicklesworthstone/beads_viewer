package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a git repo with an initial beads commit and a follow-up change.
// It returns the repository directory and the hash of the first commit (HEAD~1).
func initGitRepo(t *testing.T) (string, string) {
	t.Helper()
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Commit 1: single issue
	first := `{"id":"A","title":"Alpha","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(first), 0o644); err != nil {
		t.Fatalf("write beads v1: %v", err)
	}

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")
	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "initial")

	// Commit 2: add new issue B
	second := first + "\n" + `{"id":"B","title":"Beta","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(second), 0o644); err != nil {
		t.Fatalf("write beads v2: %v", err)
	}
	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "add B")

	revCmd := exec.Command("git", "rev-parse", "HEAD~1")
	revCmd.Dir = repoDir
	out, err := revCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	return repoDir, strings.TrimSpace(string(out))
}

// initGitRepoWithMalformedIssues creates a git repo that uses issues.jsonl and includes
// a malformed JSON line to verify robot-mode stderr cleanliness.
func initGitRepoWithMalformedIssues(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	first := `{"id":"A","title":"Alpha","status":"open","priority":1,"issue_type":"task"}` + "\n" +
		`{this is not json}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(first), 0o644); err != nil {
		t.Fatalf("write issues v1: %v", err)
	}

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")
	git("add", ".beads/issues.jsonl")
	git("commit", "-m", "initial")

	second := `{"id":"A","title":"Alpha","status":"open","priority":1,"issue_type":"task"}` + "\n" +
		`{"id":"B","title":"Beta","status":"open","priority":2,"issue_type":"task"}` + "\n" +
		`{this is not json}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(second), 0o644); err != nil {
		t.Fatalf("write issues v2: %v", err)
	}
	git("add", ".beads/issues.jsonl")
	git("commit", "-m", "add B")

	return repoDir
}

func TestRobotDiffIncludesHashesAndNewIssues(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir, priorRev := initGitRepo(t)

	cmd := exec.Command(bv, "--robot-diff", "--diff-since", "HEAD~1")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-diff failed: %v\n%s", err, out)
	}

	var payload struct {
		GeneratedAt      string `json:"generated_at"`
		ResolvedRevision string `json:"resolved_revision"`
		FromDataHash     string `json:"from_data_hash"`
		ToDataHash       string `json:"to_data_hash"`
		Diff             struct {
			NewIssues []struct {
				ID string `json:"id"`
			} `json:"new_issues"`
			Summary struct {
				IssuesAdded int `json:"issues_added"`
			} `json:"summary"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	if payload.GeneratedAt == "" {
		t.Fatal("generated_at missing")
	}
	if payload.FromDataHash == "" || payload.ToDataHash == "" {
		t.Fatalf("expected both data hashes, got from=%q to=%q", payload.FromDataHash, payload.ToDataHash)
	}
	if payload.FromDataHash == payload.ToDataHash {
		t.Fatalf("data hashes should differ when issues change")
	}
	if payload.ResolvedRevision != priorRev {
		t.Fatalf("resolved_revision mismatch: want %s got %s", priorRev, payload.ResolvedRevision)
	}
	if len(payload.Diff.NewIssues) != 1 || payload.Diff.NewIssues[0].ID != "B" {
		t.Fatalf("expected new issue B, got %+v", payload.Diff.NewIssues)
	}
	if payload.Diff.Summary.IssuesAdded != 1 {
		t.Fatalf("expected issues_added=1, got %d", payload.Diff.Summary.IssuesAdded)
	}
}

func TestDiffSinceAutoJSON_MalformedIssues_NoStderr(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := initGitRepoWithMalformedIssues(t)

	cmd := exec.Command(bv, "--diff-since", "HEAD~1")
	cmd.Dir = repoDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("--diff-since failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("expected empty stderr; got:\n%s", got)
	}

	var payload struct {
		FromDataHash  string `json:"from_data_hash"`
		ToDataHash    string `json:"to_data_hash"`
		FromLoadStats *struct {
			Valid   int `json:"valid"`
			Errors  int `json:"errors"`
			Skipped int `json:"skipped"`
		} `json:"from_load_stats"`
		ToLoadStats *struct {
			Valid   int `json:"valid"`
			Errors  int `json:"errors"`
			Skipped int `json:"skipped"`
		} `json:"to_load_stats"`
		Degraded []struct {
			Code string `json:"code"`
		} `json:"degraded"`
		Diff struct {
			NewIssues []struct {
				ID string `json:"id"`
			} `json:"new_issues"`
			Summary struct {
				IssuesAdded int `json:"issues_added"`
			} `json:"summary"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, stdout.String())
	}
	if payload.FromDataHash == "" || payload.ToDataHash == "" {
		t.Fatalf("expected both data hashes, got from=%q to=%q", payload.FromDataHash, payload.ToDataHash)
	}
	if payload.FromLoadStats == nil || payload.FromLoadStats.Errors != 1 || payload.FromLoadStats.Valid != 1 {
		t.Fatalf("from_load_stats = %+v, want one valid and one malformed historical record", payload.FromLoadStats)
	}
	if payload.ToLoadStats == nil || payload.ToLoadStats.Errors != 1 || payload.ToLoadStats.Valid != 2 {
		t.Fatalf("to_load_stats = %+v, want two valid and one malformed current record", payload.ToLoadStats)
	}
	degradationCodes := make(map[string]bool, len(payload.Degraded))
	for _, degradation := range payload.Degraded {
		degradationCodes[degradation.Code] = true
	}
	if !degradationCodes["robot_diff_from_load_incomplete"] || !degradationCodes["robot_diff_to_load_incomplete"] {
		t.Fatalf("degraded = %+v, want distinct malformed from/to load warnings", payload.Degraded)
	}
	if payload.Diff.Summary.IssuesAdded != 1 {
		t.Fatalf("expected issues_added=1, got %d", payload.Diff.Summary.IssuesAdded)
	}
	if len(payload.Diff.NewIssues) != 1 || payload.Diff.NewIssues[0].ID != "B" {
		t.Fatalf("expected new issue B, got %+v", payload.Diff.NewIssues)
	}
}

func TestRobotDiffAppliesLabelAndRepoScopeToBothSnapshots(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	historical := `{"id":"app-A","title":"Scoped","status":"open","priority":1,"issue_type":"task","labels":["scope"]}
{"id":"other-B","title":"Outside scope","status":"open","priority":1,"issue_type":"task","labels":["other"]}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(historical), 0o644); err != nil {
		t.Fatalf("write historical issues: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("add", ".beads/issues.jsonl")
	git("commit", "-m", "historical scoped fixture")

	current := `{"id":"app-A","title":"Scoped","status":"closed","priority":1,"issue_type":"task","labels":["scope"]}
{"id":"other-B","title":"Outside scope","status":"open","priority":1,"issue_type":"task","labels":["other"]}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(current), 0o644); err != nil {
		t.Fatalf("write current issues: %v", err)
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "label", args: []string{"--label", "scope"}},
		{name: "repo", args: []string{"--repo", "app"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--robot-diff", "--diff-since", "HEAD"}, test.args...)
			cmd := exec.Command(bv, args...)
			cmd.Dir = repoDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("scoped robot diff failed: %v\n%s", err, out)
			}
			var payload struct {
				Diff struct {
					ClosedIssues []struct {
						ID string `json:"id"`
					} `json:"closed_issues"`
				} `json:"diff"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("decode scoped diff: %v\n%s", err, out)
			}
			if len(payload.Diff.ClosedIssues) != 1 || payload.Diff.ClosedIssues[0].ID != "app-A" {
				t.Fatalf("scoped closed issues = %+v, want only app-A", payload.Diff.ClosedIssues)
			}
		})
	}
}

func TestRobotOutputsShareDataHashAndStatus(t *testing.T) {
	bv := buildBvBinary(t)

	envDir := t.TempDir()
	beadsDir := filepath.Join(envDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	beads := `{"id":"X","title":"Node X","status":"open","priority":1,"issue_type":"task"}
{"id":"Y","title":"Node Y","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"Y","depends_on_id":"X","type":"blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	flags := []string{"--robot-insights", "--robot-plan", "--robot-priority"}
	hashes := make([]string, 0, len(flags))
	for _, flag := range flags {
		cmd := exec.Command(bv, flag)
		cmd.Dir = envDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", flag, err, out)
		}
		var payload struct {
			DataHash string                 `json:"data_hash"`
			Status   map[string]interface{} `json:"status"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("%s json decode: %v\nout=%s", flag, err, out)
		}
		if payload.DataHash == "" {
			t.Fatalf("%s missing data_hash", flag)
		}
		if len(payload.Status) == 0 {
			t.Fatalf("%s missing status map", flag)
		}
		hashes = append(hashes, payload.DataHash)
	}

	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Fatalf("data_hash mismatch across robot outputs: %v", hashes)
		}
	}
}
