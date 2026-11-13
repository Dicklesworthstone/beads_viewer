package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	flag "github.com/spf13/pflag"
)

func runCommandWithTimeout(t *testing.T, dir, exe string, args ...string) (string, string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("command %v timed out\nstdout:\n%s\nstderr:\n%s", args, stdout.String(), stderr.String())
	}

	return stdout.String(), stderr.String(), err
}

// A merged side branch that touched the tracker makes the first N commits in
// topological order a different set from the first N in date order, which is
// the window the extractor reads. Every retained commit must still be ranked;
// before the fix the export failed with "timeline commit ... missing from
// source history" and shipped no history.json.
func TestHistoryExportRanksRetainedCommitsAfterMergedSideBranch(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	previousLimit := timeTravelCommitLimit
	timeTravelCommitLimit = 3
	t.Cleanup(func() { timeTravelCommitLimit = previousLimit })

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	git := func(hour int, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		at := start.Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	line := func(id string) string {
		return fmt.Sprintf("{\"id\":%q,\"title\":\"Issue %s\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", id, id, start.Format(time.RFC3339), start.Format(time.RFC3339))
	}
	record := func(hour int, message string, ids ...string) string {
		t.Helper()
		var data strings.Builder
		for _, id := range ids {
			data.WriteString(line(id))
		}
		if err := os.WriteFile(".beads/issues.jsonl", []byte(data.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		git(hour, "add", ".beads/issues.jsonl")
		git(hour, "commit", "-m", message)
		return git(hour, "rev-parse", "HEAD")
	}

	git(0, "init", "-b", "main")
	if err := os.Mkdir(".beads", 0o755); err != nil {
		t.Fatal(err)
	}
	record(0, "base", "bv-base")
	// The side branch is older than the main-line work, so date order puts it
	// after main, while topological order lists it first.
	git(0, "checkout", "-b", "side")
	record(1, "side 1", "bv-base", "bv-s1")
	record(2, "side 2", "bv-base", "bv-s1", "bv-s2")
	record(3, "side 3", "bv-base", "bv-s1", "bv-s2", "bv-s3")
	git(3, "checkout", "main")
	m1 := record(10, "main 1", "bv-base", "bv-m1")
	m2 := record(11, "main 2", "bv-base", "bv-m1", "bv-m2")
	git(20, "merge", "--no-ff", "--no-commit", "-X", "ours", "side")
	record(20, "merge side", "bv-base", "bv-m1", "bv-m2", "bv-s1", "bv-s2", "bv-s3")

	// Precondition: a bounded topological walk really does omit main-line
	// commits that the date-ordered window keeps.
	bounded := git(20, "log", "--format=%H", "--topo-order", "--follow", "-n", strconv.Itoa(timeTravelCommitLimit), "HEAD", "--", ".beads/issues.jsonl")
	if strings.Contains(bounded, m1) || strings.Contains(bounded, m2) {
		t.Fatalf("fixture no longer separates the two walks; bounded topological walk:\n%s", bounded)
	}

	issues := []model.Issue{{ID: "bv-base"}, {ID: "bv-m1"}, {ID: "bv-m2"}, {ID: "bv-s1"}, {ID: "bv-s2"}, {ID: "bv-s3"}}
	history, err := generateHistoryForExport(issues)
	if err != nil {
		t.Fatalf("history export failed after merging a side branch: %v", err)
	}
	position := map[string]int{}
	for i, commit := range history.Commits {
		position[commit.SHA] = i
	}
	for sha, id := range map[string]string{m1: "bv-m1", m2: "bv-m2"} {
		i, ok := position[sha]
		if !ok {
			t.Fatalf("retained commit %s (%s) missing from timeline: %#v", sha, id, history.Commits)
		}
		if got := history.Commits[i].BeadsAdded; !reflect.DeepEqual(got, []string{id}) {
			t.Fatalf("commit %s added %v, want [%s]", sha, got, id)
		}
	}
	if position[m1] >= position[m2] {
		t.Fatalf("timeline replays main 2 before its parent main 1: %#v", history.Commits)
	}
}

// The tracker is renamed on main while an older side branch still edits the
// old name, and that branch is merged afterwards. The extractor's date-ordered
// --follow walk reaches the rename first and keeps the side commit; a
// --follow walk in topological order lists the side branch while still on the
// new name and drops it, so ranking by a path walk failed with "timeline
// commit ... missing from source history".
func TestHistoryExportRanksRenamedTrackerSideBranch(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	git := func(hour int, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		at := start.Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name string, ids ...string) {
		t.Helper()
		var data strings.Builder
		for _, id := range ids {
			fmt.Fprintf(&data, "{\"id\":%q,\"title\":\"Issue %s\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", id, id, start.Format(time.RFC3339), start.Format(time.RFC3339))
		}
		if err := os.WriteFile(name, []byte(data.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(0, "init", "-b", "main")
	if err := os.Mkdir(".beads", 0o755); err != nil {
		t.Fatal(err)
	}
	write(".beads/beads.jsonl", "bv-a")
	git(0, "add", "-A")
	git(0, "commit", "-m", "base")
	write(".beads/beads.jsonl", "bv-a", "bv-b")
	git(1, "commit", "-am", "main old name")
	mainOld := git(1, "rev-parse", "HEAD")
	git(1, "checkout", "-b", "side")
	write(".beads/beads.jsonl", "bv-a", "bv-b", "bv-s")
	git(2, "commit", "-am", "side old name")
	side := git(2, "rev-parse", "HEAD")
	git(2, "checkout", "main")
	git(3, "mv", ".beads/beads.jsonl", ".beads/issues.jsonl")
	git(3, "commit", "-m", "rename tracker")
	write(".beads/issues.jsonl", "bv-a", "bv-b", "bv-c")
	git(4, "commit", "-am", "main new name")
	mainNew := git(4, "rev-parse", "HEAD")
	git(7, "merge", "--no-commit", "-X", "ours", "side")
	write(".beads/issues.jsonl", "bv-a", "bv-b", "bv-s", "bv-c")
	git(7, "add", "-A")
	git(7, "commit", "-m", "merge side")

	issues := []model.Issue{{ID: "bv-a"}, {ID: "bv-b"}, {ID: "bv-c"}, {ID: "bv-s"}}
	history, err := generateHistoryForExport(issues)
	if err != nil {
		t.Fatalf("history export failed after merging a side branch that edited the old tracker name: %v", err)
	}
	position := map[string]int{}
	for i, commit := range history.Commits {
		position[commit.SHA] = i
	}
	for sha, id := range map[string]string{mainOld: "bv-b", side: "bv-s", mainNew: "bv-c"} {
		i, ok := position[sha]
		if !ok {
			t.Fatalf("commit %s (adds %s) missing from timeline: %#v", sha, id, history.Commits)
		}
		if got := history.Commits[i].BeadsAdded; !reflect.DeepEqual(got, []string{id}) {
			t.Fatalf("commit %s added %v, want [%s]", sha, got, id)
		}
	}
	if position[mainOld] >= position[side] || position[mainOld] >= position[mainNew] {
		t.Fatalf("timeline replays a commit before its parent: %#v", history.Commits)
	}
}

func TestHistoryExportUsesRecordedLifecycle(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	git := func(hour int, args ...string) string {
		t.Helper()
		// The retained_window subtest makes 500 commits. With automatic
		// maintenance on, git 2.54 runs it mid-sequence and a later commit
		// fails with "fatal: unable to read <object>"; the fixture never needs it.
		cmd := exec.Command("git", append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
		cmd.Dir = repo
		// Distinct commits can share Git's one-second timestamp resolution.
		at := start.Format(time.RFC3339)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(0, "init", "-b", "main")
	if err := os.Mkdir(".beads", 0o755); err != nil {
		t.Fatal(err)
	}
	var want []TimeTravelCommit
	for hour, status := range []string{"open", "closed", "closed", "open", "absent", "open"} {
		at := start.Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
		data := fmt.Sprintf("{\"id\":\"bv-a\",\"title\":\"Target revision %d\",\"status\":%q,\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", hour, status, start.Format(time.RFC3339), at)
		if status == "absent" {
			data = ""
		}
		data += fmt.Sprintf("{\"id\":\"bv-z\",\"title\":\"Already closed\",\"status\":\"closed\",\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", start.Format(time.RFC3339), start.Format(time.RFC3339))
		if err := os.WriteFile(".beads/beads.jsonl", []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		git(hour, "add", ".beads/beads.jsonl")
		message := fmt.Sprintf("record lifecycle %d", hour)
		git(hour, "commit", "-m", message)
		commit := TimeTravelCommit{SHA: git(hour, "rev-parse", "HEAD"), Date: start.Format(time.RFC3339), Message: message}
		switch hour {
		case 0:
			commit.BeadsAdded = []string{"bv-a", "bv-z"}
			commit.BeadsClosed = []string{"bv-z"}
		case 1:
			commit.BeadsClosed = []string{"bv-a"}
		case 3, 5:
			commit.BeadsAdded = []string{"bv-a"}
		case 4:
			commit.BeadsRemoved = []string{"bv-a"}
		}
		want = append(want, commit)
	}
	if err := os.Rename(".beads/beads.jsonl", ".beads/issues.jsonl"); err != nil {
		t.Fatal(err)
	}
	git(4, "add", ".beads")
	git(4, "commit", "-m", "rename issue source")
	// A correlated code-only commit is not an observed lifecycle transition.
	if err := os.WriteFile("feature.go", []byte("package fixture\nconst Ready = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(4, "add", "feature.go")
	git(4, "commit", "-m", "bv-a: follow-up code work")
	issues := []model.Issue{{ID: "bv-a", Title: "Target revision 3", Status: model.StatusOpen}, {ID: "bv-z", Title: "Already closed", Status: model.StatusClosed}}
	if err := os.WriteFile(".beads/sprints.jsonl", []byte("{\"id\":\"sprint-1\",\"name\":\"Recorded sprint\",\"start_date\":\"2026-01-01T00:00:00Z\",\"end_date\":\"2026-01-08T00:00:00Z\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		history, err := generateHistoryForExport(issues)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(history.Commits, want) {
			t.Fatalf("run %d: lifecycle timeline mismatch\n got: %#v\nwant: %#v", run, history.Commits, want)
		}
		if len(history.InitialBeads) != 0 {
			t.Fatalf("creation inside the retained window must not seed earlier visibility: %v", history.InitialBeads)
		}
		if len(history.Sprints) != 1 || history.Sprints[0].ID != "sprint-1" || !history.Sprints[0].StartDate.Equal(start) {
			t.Fatalf("timeline sprint definitions: %#v", history.Sprints)
		}
	}
	t.Run("retained_window", func(t *testing.T) {
		// Push creation outside the real 500-source-commit window. The target
		// is open at its boundary but closed now; current status is no baseline.
		for i := 0; i <= 500; i++ {
			status := "open"
			if i == 500 {
				status = "closed"
			}
			data := fmt.Sprintf("{\"id\":\"bv-a\",\"title\":\"Target %d\",\"status\":%q}\n{\"id\":\"bv-z\",\"title\":\"Closed %d\",\"status\":\"closed\"}\n", i, status, i)
			if i >= 499 {
				data += "{\"id\":\"bv-new\",\"title\":\"Created inside window\",\"status\":\"open\"}\n"
			}
			if err := os.WriteFile(".beads/issues.jsonl", []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			git(i, "add", ".beads/issues.jsonl")
			git(i, "commit", "-m", fmt.Sprintf("retained record %d", i))
		}
		history, err := generateHistoryForExport([]model.Issue{
			{ID: "bv-a", Status: model.StatusClosed}, {ID: "bv-z", Status: model.StatusClosed},
			{ID: "bv-new", Status: model.StatusOpen}, {ID: "bv-unknown", Status: model.StatusOpen},
		})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		var exported struct {
			InitialBeads []string `json:"initial_beads"`
		}
		if err := json.Unmarshal(data, &exported); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(exported.InitialBeads, []string{"bv-a"}) {
			t.Fatalf("observed unresolved baseline: got %v, want only bv-a (not closed, newly created, or unknown records)", exported.InitialBeads)
		}
		if len(history.Commits) != 500 || history.Commits[0].Message != "retained record 1" || !reflect.DeepEqual(history.Commits[499].BeadsClosed, []string{"bv-a"}) {
			t.Fatalf("retained window or final closure incorrect: %#v", history)
		}
	})
}

func TestFilterByRepo_CaseInsensitiveAndFlexibleSeparators(t *testing.T) {
	issues := []model.Issue{
		{ID: "api-AUTH-1", SourceRepo: "services/api"},
		{ID: "web:UI-2", SourceRepo: "apps/web"},
		{ID: "lib_UTIL_3", SourceRepo: "libs/util"},
		{ID: "misc-4", SourceRepo: "misc"},
	}

	tests := []struct {
		filter   string
		expected int
	}{
		{"API", 1},      // case-insensitive, matches api-
		{"web", 1},      // flexible with ':' separator
		{"lib", 1},      // flexible with '_' separator
		{"missing", 0},  // no match
		{"misc-", 1},    // exact prefix
		{"services", 1}, // matches SourceRepo when ID lacks prefix
	}

	for _, tt := range tests {
		got := filterByRepo(issues, tt.filter)
		if len(got) != tt.expected {
			t.Errorf("filterByRepo(%q) = %d issues, want %d", tt.filter, len(got), tt.expected)
		}
	}
}

func TestRobotFlagsOutputJSON(t *testing.T) {
	tmpDir := t.TempDir()
	beads := `{"id":"A","title":"Root","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Blocked","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"depends_on_id":"A","type":"blocks"}]}`

	if err := os.WriteFile(filepath.Join(tmpDir, ".beads.jsonl"), []byte(beads), 0644); err != nil {
		t.Fatalf("write beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "beads.jsonl"), []byte(beads), 0644); err != nil {
		t.Fatalf("write beads dir: %v", err)
	}

	// Build a temporary bv binary using the repo module
	bin := filepath.Join(tmpDir, "bv")
	build := exec.Command("go", "build", "-C", repoRoot(t), "-o", bin, "./cmd/bv")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build bv: %v\n%s", err, out)
	}

	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = tmpDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
		return out
	}

	for _, flag := range [][]string{
		{"--robot-plan"},
		{"--robot-insights"},
		{"--robot-priority"},
		{"--robot-recipes"},
		{"--robot-capabilities"},
		{"--robot-docs", "commands"},
		{"--robot-next"},
		{"--robot-triage"},
		{"--robot-label-health"},
		{"--robot-label-flow"},
		{"--robot-label-attention"},
		{"--robot-capacity"},
	} {
		out := run(flag...)
		if !json.Valid(out) {
			t.Fatalf("%v did not return valid JSON: %s", flag, string(out))
		}
	}
}

func TestRobotCPUProfileFinalizedBeforeExit(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)
	exe := buildTestBinary(t)

	t.Run("successful registry command", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "cpu.pprof")
		stdout, stderr, err := runCommandWithTimeout(
			t,
			tmpDir,
			exe,
			"--cpu-profile", profilePath,
			"--robot-insights",
			"--format=json",
		)
		if err != nil {
			t.Fatalf("profiled robot command failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !json.Valid([]byte(stdout)) {
			t.Fatalf("profiled robot command returned invalid JSON: %s", stdout)
		}

		profileInfo, err := os.Stat(profilePath)
		if err != nil {
			t.Fatalf("stat CPU profile: %v", err)
		}
		if profileInfo.Size() == 0 {
			t.Fatal("CPU profile is empty; profiling was not finalized before process exit")
		}

		pprofCmd := exec.Command("go", "tool", "pprof", "-top", exe, profilePath)
		pprofOutput, err := pprofCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("read CPU profile with go tool pprof: %v\n%s", err, pprofOutput)
		}
		if !bytes.Contains(pprofOutput, []byte("Type: cpu")) {
			t.Fatalf("go tool pprof did not identify a CPU profile:\n%s", pprofOutput)
		}
	})

	t.Run("invalid destination fails before success output", func(t *testing.T) {
		profileDir := t.TempDir()
		stdout, stderr, err := runCommandWithTimeout(
			t,
			tmpDir,
			exe,
			"--cpu-profile", profileDir,
			"--robot-insights",
			"--format=json",
		)
		if err == nil {
			t.Fatalf("invalid CPU profile destination unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if stdout != "" {
			t.Fatalf("invalid CPU profile destination emitted success output:\n%s", stdout)
		}
		if !strings.Contains(stderr, "Could not create CPU profile") {
			t.Fatalf("missing CPU profile creation error\nstderr:\n%s", stderr)
		}
	})
}

func TestCLIFlagCompatibility(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)

	exe := buildTestBinary(t)

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(exe, args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}

	t.Run("double-dash robot flag", func(t *testing.T) {
		out := run("--robot-next", "--format", "json")
		if !json.Valid([]byte(out)) {
			t.Fatalf("expected JSON output for long flags, got %q", out)
		}
	})

	t.Run("single-dash compatibility", func(t *testing.T) {
		out := run("-robot-next", "-format", "json")
		if !json.Valid([]byte(out)) {
			t.Fatalf("expected JSON output for single-dash long flags, got %q", out)
		}
	})

	t.Run("short aliases", func(t *testing.T) {
		out := run("--robot-insights", "-l", "backend", "-f", "json")
		if !json.Valid([]byte(out)) {
			t.Fatalf("expected JSON output for short aliases, got %q", out)
		}
	})

	t.Run("grouped help output", func(t *testing.T) {
		out := run("--help")
		for _, snippet := range []string{
			"General Flags:",
			"Search & Filters:",
			"Robot & Planning Flags:",
			"Export & Reporting:",
			"Agent File Management:",
			"--robot-capabilities",
			"-f, --format",
			"-l, --label",
			"-r, --recipe",
		} {
			if !strings.Contains(out, snippet) {
				t.Fatalf("help output missing %q:\n%s", snippet, out)
			}
		}
	})

	t.Run("version flag", func(t *testing.T) {
		out := strings.TrimSpace(run("--version"))
		if !strings.HasPrefix(out, "bv ") {
			t.Fatalf("expected version output, got %q", out)
		}
	})
}

func TestUnknownFlagErrorSuggestsNearestFlag(t *testing.T) {
	exe := buildTestBinary(t)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "robot flag typo",
			args: []string{"--robot-triag", "--json"},
			want: []string{
				"unknown flag: --robot-triag",
				"Did you mean `bv --robot-triage --json`?",
				"bv --robot-help",
			},
		},
		{
			name: "value flag typo preserves and quotes value",
			args: []string{"--robot-graph", "--graph-rooot=A>B"},
			want: []string{
				"unknown flag: --graph-rooot",
				"Did you mean `bv --robot-graph '--graph-root=A>B'`?",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runCommandWithTimeout(t, t.TempDir(), exe, tt.args...)
			if err == nil {
				t.Fatalf("expected unknown flag to fail\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("expected empty stdout for unknown flag, got:\n%s", stdout)
			}
			for _, want := range tt.want {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr missing %q\nstderr:\n%s", want, stderr)
				}
			}
			if strings.Count(stderr, "unknown flag: --") != 1 {
				t.Fatalf("expected unknown flag error once, got:\n%s", stderr)
			}
		})
	}
}

func TestResolvePagesSource_RespectsExplicitBeadsDBFile(t *testing.T) {
	beadsDir := t.TempDir()
	selectedPath := filepath.Join(beadsDir, "selected.jsonl")
	defaultPath := filepath.Join(beadsDir, "beads.jsonl")

	writeIssueJSONL(t, selectedPath, "SELECTED-1")
	writeIssueJSONL(t, defaultPath, "DEFAULT-1")
	t.Setenv(loader.BeadsDBEnvVar, selectedPath)

	source, err := resolvePagesSource(&export.WizardConfig{}, "")
	if err != nil {
		t.Fatalf("resolvePagesSource: %v", err)
	}
	if len(source.Issues) != 1 {
		t.Fatalf("issue count = %d, want 1", len(source.Issues))
	}
	requireString(t, source.Issues[0].ID, "SELECTED-1")
	requireString(t, source.SourcePath, selectedPath)
}

func TestResolvePagesSource_ReadinessRetainsTombstonesAndParentGates(t *testing.T) {
	t.Setenv("BEADS_DB", "")
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	data := `{"id":"ready","title":"Resolved tombstone","status":"open","issue_type":"task","dependencies":[{"issue_id":"ready","depends_on_id":"deleted","type":"blocks"}]}
{"id":"deleted","title":"Deleted","status":"tombstone","issue_type":"task"}
{"id":"child","title":"Inherited gate","status":"open","issue_type":"task","dependencies":[{"issue_id":"child","depends_on_id":"parent","type":"parent-child"}]}
{"id":"parent","title":"Parent","status":"open","issue_type":"epic","dependencies":[{"issue_id":"parent","depends_on_id":"missing","type":"blocks"}]}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := resolvePagesSource(&export.WizardConfig{SourcePath: path}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Issues) != 3 || source.Readiness == nil {
		t.Fatalf("wizard source must retain three visible issues and full readiness: %+v", source)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if !source.Readiness.Ready("ready", now) || source.Readiness.Ready("child", now) || source.Readiness.DependencyState("child") != model.DependenciesUnknown {
		t.Fatal("wizard source lost resolved or inherited prerequisite state")
	}
}

func TestResolvePagesSource_RespectsSavedSourcePath(t *testing.T) {
	beadsDir := t.TempDir()
	selectedPath := filepath.Join(beadsDir, "selected.jsonl")
	defaultPath := filepath.Join(beadsDir, "beads.jsonl")

	writeIssueJSONL(t, selectedPath, "SAVED-1")
	writeIssueJSONL(t, defaultPath, "DEFAULT-1")

	source, err := resolvePagesSource(&export.WizardConfig{SourcePath: selectedPath}, "")
	if err != nil {
		t.Fatalf("resolvePagesSource: %v", err)
	}
	if len(source.Issues) != 1 {
		t.Fatalf("issue count = %d, want 1", len(source.Issues))
	}
	requireString(t, source.Issues[0].ID, "SAVED-1")
	requireString(t, source.SourcePath, selectedPath)
}

func TestResolvePagesSource_ExplicitBeadsDBOverridesSavedSourcePath(t *testing.T) {
	beadsDir := t.TempDir()
	savedPath := filepath.Join(beadsDir, "saved.jsonl")
	explicitPath := filepath.Join(beadsDir, "explicit.jsonl")

	writeIssueJSONL(t, savedPath, "SAVED-1")
	writeIssueJSONL(t, explicitPath, "EXPLICIT-1")
	t.Setenv(loader.BeadsDBEnvVar, explicitPath)

	source, err := resolvePagesSource(&export.WizardConfig{SourcePath: savedPath}, "")
	if err != nil {
		t.Fatalf("resolvePagesSource: %v", err)
	}
	if len(source.Issues) != 1 {
		t.Fatalf("issue count = %d, want 1", len(source.Issues))
	}
	requireString(t, source.Issues[0].ID, "EXPLICIT-1")
	requireString(t, source.SourcePath, explicitPath)
}

func TestUnknownCommandErrorSuggestsNearestCommand(t *testing.T) {
	exe := buildTestBinary(t)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "canonical robot command typo",
			args: []string{"robot-triag", "--json"},
			want: []string{
				`unknown command "robot-triag" for "bv"`,
				"Did you mean `bv robot-triage --json`?",
				"Canonical flag form: `bv --robot-triage --format json`.",
				"bv robot-capabilities --json",
			},
		},
		{
			name: "canonical value command typo preserves args",
			args: []string{"robot-relatd", "A", "--json"},
			want: []string{
				`unknown command "robot-relatd" for "bv"`,
				"Did you mean `bv robot-related A --json`?",
				"Canonical flag form: `bv --robot-related A --format json`.",
			},
		},
		{
			name: "canonical value command typo quotes shell metacharacters",
			args: []string{"robot-relatd", "A>B", "--json"},
			want: []string{
				`unknown command "robot-relatd" for "bv"`,
				"Did you mean `bv robot-related 'A>B' --json`?",
				"Canonical flag form: `bv --robot-related 'A>B' --format json`.",
			},
		},
		{
			name: "agent alias typo preserves args",
			args: []string{"schem", "triage", "--json"},
			want: []string{
				`unknown command "schem" for "bv"`,
				"Did you mean `bv schema triage --json`?",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runCommandWithTimeout(t, t.TempDir(), exe, tt.args...)
			if err == nil {
				t.Fatalf("expected unknown command to fail\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("expected empty stdout for unknown command, got:\n%s", stdout)
			}
			for _, want := range tt.want {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr missing %q\nstderr:\n%s", want, stderr)
				}
			}
			if strings.Count(stderr, `unknown command "`) != 1 {
				t.Fatalf("expected unknown command error once, got:\n%s", stderr)
			}
		})
	}
}

func TestMissingFlagArgumentErrorSuggestsValueShape(t *testing.T) {
	exe := buildTestBinary(t)

	stdout, stderr, err := runCommandWithTimeout(t, t.TempDir(), exe, "--name")
	if err == nil {
		t.Fatalf("expected missing flag argument to fail\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout for missing flag argument, got:\n%s", stdout)
	}
	for _, want := range []string{"flag needs an argument: --label", "Use --label VALUE."} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q\nstderr:\n%s", want, stderr)
		}
	}
}

func TestRobotNowHonorsSourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1234567890")
	requireString(t, robotNow().Format(time.RFC3339), "2009-02-13T23:31:30Z")
	requireString(t, NewRobotEnvelope("hash").GeneratedAt, "2009-02-13T23:31:30Z")
}

func TestAgentIntentArgRewrite(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "json defaults to triage",
			args: []string{"--json"},
			want: []string{"--robot-triage", "--format", "json"},
		},
		{
			name: "json false still avoids tui",
			args: []string{"--json=false"},
			want: []string{"--robot-triage", "--format=json"},
		},
		{
			name: "toon defaults to triage",
			args: []string{"--toon"},
			want: []string{"--robot-triage", "--format", "toon"},
		},
		{
			name: "toon false keeps structured output without forcing toon",
			args: []string{"--toon=false"},
			want: []string{"--robot-triage", "--format=json"},
		},
		{
			name: "output toon defaults to triage",
			args: []string{"--output=toon"},
			want: []string{"--robot-triage", "--format=toon"},
		},
		{
			name: "triage subcommand",
			args: []string{"triage", "--json", "--name", "backend", "--limit", "3"},
			want: []string{"--robot-triage", "--format", "json", "--label", "backend", "--robot-max-results", "3"},
		},
		{
			name: "canonical robot command name",
			args: []string{"robot-triage", "--json"},
			want: []string{"--robot-triage", "--format", "json"},
		},
		{
			name: "canonical robot help with json becomes docs",
			args: []string{"robot-help", "--json"},
			want: []string{"--robot-docs", "guide", "--format", "json"},
		},
		{
			name: "canonical grouped triage command name",
			args: []string{"robot-triage-by-track", "--json", "--limit=2"},
			want: []string{"--robot-triage-by-track", "--format", "json", "--robot-max-results=2"},
		},
		{
			name: "schema subcommand",
			args: []string{"schema", "triage", "--json"},
			want: []string{"--robot-schema", "--schema-command", "robot-triage", "--format", "json"},
		},
		{
			name: "canonical schema command name",
			args: []string{"robot-schema", "triage", "--json"},
			want: []string{"--robot-schema", "--schema-command", "robot-triage", "--format", "json"},
		},
		{
			name: "schema accepts output alias before command",
			args: []string{"schema", "--json", "triage"},
			want: []string{"--robot-schema", "--schema-command", "robot-triage", "--format", "json"},
		},
		{
			name: "schema normalizes mixed case command name",
			args: []string{"schema", "Robot-Triage", "--json"},
			want: []string{"--robot-schema", "--schema-command", "robot-triage", "--format", "json"},
		},
		{
			name: "search subcommand",
			args: []string{"search", "login", "oauth", "--json", "--limit=5"},
			want: []string{"--search", "login oauth", "--robot-search", "--format", "json", "--search-limit=5"},
		},
		{
			name: "search accepts limit before query",
			args: []string{"search", "--limit", "5", "login", "oauth", "--json"},
			want: []string{"--search", "login oauth", "--robot-search", "--search-limit", "5", "--format", "json"},
		},
		{
			name: "search accepts output alias between query terms",
			args: []string{"search", "login", "--json", "oauth"},
			want: []string{"--search", "login oauth", "--robot-search", "--format", "json"},
		},
		{
			name: "canonical search command name",
			args: []string{"robot-search", "login", "oauth", "--json", "--limit", "5"},
			want: []string{"--search", "login oauth", "--robot-search", "--format", "json", "--search-limit", "5"},
		},
		{
			name: "graph format positional",
			args: []string{"graph", "mermaid", "--output", "json"},
			want: []string{"--robot-graph", "--graph-format", "mermaid", "--format", "json"},
		},
		{
			name: "canonical graph command name",
			args: []string{"robot-graph", "mermaid", "--json"},
			want: []string{"--robot-graph", "--graph-format", "mermaid", "--format", "json"},
		},
		{
			name: "graph accepts output alias before format",
			args: []string{"graph", "--json", "mermaid"},
			want: []string{"--robot-graph", "--graph-format", "mermaid", "--format", "json"},
		},
		{
			name: "related accepts output alias before target",
			args: []string{"related", "--json", "bv-123"},
			want: []string{"--robot-related", "bv-123", "--format", "json"},
		},
		{
			name: "canonical value command name",
			args: []string{"robot-related", "bv-123", "--json", "--limit=2"},
			want: []string{"--robot-related", "bv-123", "--format", "json", "--related-max-results=2"},
		},
		{
			name: "missing value command keeps required flag after output alias",
			args: []string{"robot-related", "--json"},
			want: []string{"--format", "json", "--robot-related"},
		},
		{
			name: "missing value command keeps required flag after native options",
			args: []string{"robot-confirm-correlation", "--correlation-by", "agent", "--json"},
			want: []string{"--correlation-by", "agent", "--format", "json", "--robot-confirm-correlation"},
		},
		{
			name: "canonical diff command name",
			args: []string{"robot-diff", "HEAD~1", "--json"},
			want: []string{"--robot-diff", "--diff-since", "HEAD~1", "--format", "json"},
		},
		{
			name: "canonical drift command name includes required check",
			args: []string{"robot-drift", "--json"},
			want: []string{"--check-drift", "--robot-drift", "--format", "json"},
		},
		{
			name: "docs accepts output alias before topic",
			args: []string{"docs", "--json", "guide"},
			want: []string{"--robot-docs", "guide", "--format", "json"},
		},
		{
			name: "canonical docs command name",
			args: []string{"robot-docs", "guide", "--json"},
			want: []string{"--robot-docs", "guide", "--format", "json"},
		},
		{
			name: "upgrade maps to --update",
			args: []string{"upgrade"},
			want: []string{"--update"},
		},
		{
			name: "upgrade --yes skips confirmation",
			args: []string{"upgrade", "--yes"},
			want: []string{"--update", "--yes"},
		},
		{
			name: "upgrade -y short flag skips confirmation",
			args: []string{"upgrade", "-y"},
			want: []string{"--update", "--yes"},
		},
		{
			name: "upgrade --check maps to --check-update",
			args: []string{"upgrade", "--check"},
			want: []string{"--check-update"},
		},
		{
			name: "upgrade check bare word maps to --check-update",
			args: []string{"upgrade", "check"},
			want: []string{"--check-update"},
		},
		{
			name: "upgrade --dry-run maps to --update-dry-run",
			args: []string{"upgrade", "--dry-run"},
			want: []string{"--update-dry-run"},
		},
		{
			name: "update dry-run stays non-robot with structured-output alias",
			args: []string{"--update-dry-run", "--json"},
			want: []string{"--update-dry-run", "--format", "json"},
		},
		{
			name: "upgrade --rollback maps to --rollback",
			args: []string{"upgrade", "--rollback"},
			want: []string{"--rollback"},
		},
		{
			name: "self-update alias maps to --update",
			args: []string{"self-update"},
			want: []string{"--update"},
		},
		{
			name: "upgrade passes through unknown flags for cobra to report",
			args: []string{"upgrade", "--bogus"},
			want: []string{"--update", "--bogus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireArgs(t, rewriteAgentIntentArgs(tt.args), tt.want)
		})
	}
}

func TestReadUpdateConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      bool
		wantError bool
	}{
		{name: "blank line accepts default", input: "\n", want: true},
		{name: "yes accepts", input: " YES \n", want: true},
		{name: "single y at EOF accepts", input: "y", want: true},
		{name: "no cancels", input: "n\n", want: false},
		{name: "arbitrary response cancels", input: "later\n", want: false},
		{name: "empty EOF fails closed", input: "", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readUpdateConfirmation(strings.NewReader(tt.input))
			if (err != nil) != tt.wantError {
				t.Fatalf("readUpdateConfirmation error = %v, wantError %v", err, tt.wantError)
			}
			if got != tt.want {
				t.Fatalf("readUpdateConfirmation = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentIntentValueCommandMissingTargetFailsBeforeTUI(t *testing.T) {
	exe := buildTestBinary(t)

	stdout, stderr, err := runCommandWithTimeout(t, t.TempDir(), exe, "robot-related", "--json")
	if err == nil {
		t.Fatalf("expected missing value command to fail\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout for missing value command, got:\n%s", stdout)
	}
	for _, want := range []string{
		"flag needs an argument: --robot-related",
		"Use --robot-related VALUE.",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q\nstderr:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "could not open a new TTY") {
		t.Fatalf("missing value command fell through to the TUI:\n%s", stderr)
	}
}

func TestAgentIntentAliasesOutputJSON(t *testing.T) {
	tmpDir := t.TempDir()
	beads := `{"id":"A","title":"Root","status":"open","priority":1,"issue_type":"task","labels":["backend"]}
{"id":"B","title":"Blocked","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"depends_on_id":"A","type":"blocks"}]}`
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "beads.jsonl"), []byte(beads), 0644); err != nil {
		t.Fatalf("write beads dir: %v", err)
	}

	exe := buildTestBinary(t)
	for _, args := range [][]string{
		{"--json"},
		{"--robot-help", "--json"},
		{"--robot-help", "--format", "json"},
		{"robot-help", "--json"},
		{"robot-triage", "--json"},
		{"triage", "--json"},
		{"robot-capabilities", "--json"},
		{"capabilities", "--json"},
		{"robot-docs", "guide", "--json"},
		{"docs", "guide", "--json"},
		{"docs", "--json", "guide"},
		{"robot-schema", "triage", "--json"},
		{"schema", "triage", "--json"},
		{"schema", "--json", "triage"},
		{"schema", "Robot-Triage", "--json"},
		{"robot-graph", "mermaid", "--json"},
		{"graph", "--json", "mermaid"},
		{"--name", "backend", "--json"},
		{"--json=false"},
		{"--toon=false"},
	} {
		stdout, stderr, err := runCommandWithTimeout(t, tmpDir, exe, args...)
		if err != nil {
			t.Fatalf("%v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
		}
		if !json.Valid([]byte(stdout)) {
			t.Fatalf("%v did not return valid JSON\nstdout:\n%s\nstderr:\n%s", args, stdout, stderr)
		}
	}
}

func TestEnumFlagErrorSuggestsNearestValue(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("graph-format", "json", "")
	if err := fs.Set("graph-format", "jsno"); err != nil {
		t.Fatalf("set graph-format: %v", err)
	}

	err := validateEnumFlags(fs, []enumFlagRule{{name: "graph-format", allowed: []string{"json", "dot", "mermaid"}}})
	if err == nil {
		t.Fatal("expected invalid enum error")
	}
	if !strings.Contains(err.Error(), `did you mean "json"?`) {
		t.Fatalf("missing did-you-mean hint: %v", err)
	}
}

func TestRobotCapabilitiesManifest(t *testing.T) {
	capabilities := generateRobotCapabilities()
	if capabilities["tool"] != "bv" {
		t.Fatalf("tool = %v, want bv", capabilities["tool"])
	}
	if capabilities["contract_version"] != robotContractVersion {
		t.Fatalf("contract_version = %v, want %s", capabilities["contract_version"], robotContractVersion)
	}
	commands, ok := capabilities["commands"].([]map[string]interface{})
	if !ok {
		t.Fatalf("commands has unexpected type %T", capabilities["commands"])
	}
	seen := map[string]map[string]interface{}{}
	for _, command := range commands {
		name, _ := command["name"].(string)
		seen[name] = command
	}
	for name := range primaryRobotFlagNames() {
		if seen[name] == nil {
			t.Fatalf("capabilities missing command %q", name)
		}
	}
	requireString(t, seen["robot-help"]["preferred_invocation"].(string), "bv robot-help --json")
	requireString(t, seen["robot-triage"]["preferred_invocation"].(string), "bv robot-triage --json")
	requireContainsString(t, seen["robot-triage"]["accepted_invocations"].([]string), "bv --robot-triage --format json")
	requireContainsString(t, seen["robot-related"]["accepted_invocations"].([]string), "bv robot-related ISSUE_ID --json")
	if seen["robot-related"]["needs_git"] != true {
		t.Fatalf("robot-related needs_git = %v, want true", seen["robot-related"]["needs_git"])
	}
	if seen["robot-correlation-stats"]["needs_git"] != false {
		t.Fatalf("robot-correlation-stats needs_git = %v, want false", seen["robot-correlation-stats"]["needs_git"])
	}
	if seen["robot-sprint-show"]["preferred_invocation"] != "bv robot-sprint-show SPRINT_ID --json" {
		t.Fatalf("robot-sprint-show preferred_invocation = %v, want SPRINT_ID example", seen["robot-sprint-show"]["preferred_invocation"])
	}
	if seen["robot-sprint-show"]["needs_sprint"] != true {
		t.Fatalf("robot-sprint-show needs_sprint = %v, want true", seen["robot-sprint-show"]["needs_sprint"])
	}
	if seen["robot-drift"]["needs_baseline"] != true {
		t.Fatalf("robot-drift needs_baseline = %v, want true", seen["robot-drift"]["needs_baseline"])
	}
	if seen["robot-confirm-correlation"]["mutates_state"] != true {
		t.Fatalf("robot-confirm-correlation mutates_state = %v, want true", seen["robot-confirm-correlation"]["mutates_state"])
	}
	if seen["robot-reject-correlation"]["mutates_state"] != true {
		t.Fatalf("robot-reject-correlation mutates_state = %v, want true", seen["robot-reject-correlation"]["mutates_state"])
	}
	if seen["robot-explain-correlation"]["mutates_state"] != false {
		t.Fatalf("robot-explain-correlation mutates_state = %v, want false", seen["robot-explain-correlation"]["mutates_state"])
	}
	requireContainsString(t, seen["robot-forecast"]["params"].([]string), "--forecast-sprint SPRINT_ID")
	requireString(t, seen["robot-confirm-correlation"]["preferred_invocation"].(string), "bv robot-confirm-correlation deadbeef:ISSUE_ID --correlation-by agent --json")
	requireString(t, seen["robot-search"]["preferred_invocation"].(string), `bv robot-search "login oauth" --json`)
	requireContainsString(t, seen["robot-search"]["accepted_invocations"].([]string), `bv --search "login oauth" --robot-search --format json`)
	requireString(t, seen["robot-diff"]["preferred_invocation"].(string), "bv robot-diff HEAD~1 --json")
	requireContainsString(t, seen["robot-diff"]["accepted_invocations"].([]string), "bv --robot-diff --diff-since HEAD~1 --format json")
	for _, command := range commands {
		for _, key := range []string{"flag", "preferred_invocation"} {
			value, _ := command[key].(string)
			if strings.ContainsAny(value, "<>") {
				t.Fatalf("%s for %s contains shell redirection placeholder: %q", key, command["name"], value)
			}
		}
		for _, value := range command["accepted_invocations"].([]string) {
			if strings.ContainsAny(value, "<>") {
				t.Fatalf("accepted invocation for %s contains shell redirection placeholder: %q", command["name"], value)
			}
		}
		if params, ok := command["params"].([]string); ok {
			for _, value := range params {
				if strings.ContainsAny(value, "<>") {
					t.Fatalf("param for %s contains shell redirection placeholder: %q", command["name"], value)
				}
			}
		}
	}
	if _, ok := capabilities["environment_variables"].(map[string]string); !ok {
		t.Fatalf("environment_variables has unexpected type %T", capabilities["environment_variables"])
	}
	if _, ok := capabilities["exit_codes"].(map[string]string); !ok {
		t.Fatalf("exit_codes has unexpected type %T", capabilities["exit_codes"])
	}
}

func TestRobotDocsUnknownTopicSuggestsNearestTopic(t *testing.T) {
	docs := generateRobotDocs("guied")
	if docs["did_you_mean"] != "guide" {
		t.Fatalf("did_you_mean = %v, want guide; docs=%v", docs["did_you_mean"], docs)
	}
	if action, _ := docs["suggested_action"].(string); !strings.Contains(action, "bv --robot-docs guide") {
		t.Fatalf("suggested_action missing exact command: %v", docs["suggested_action"])
	}
}

func TestRobotDocsPreferSafeAgentCommandExamples(t *testing.T) {
	guideDocs := generateRobotDocs("guide")
	guide, ok := guideDocs["guide"].(map[string]interface{})
	if !ok {
		t.Fatalf("guide has unexpected type %T", guideDocs["guide"])
	}
	quickstart, ok := guide["quickstart"].([]string)
	if !ok {
		t.Fatalf("quickstart has unexpected type %T", guide["quickstart"])
	}
	requireContainsString(t, quickstart, "bv robot-triage --json           # Full triage with recommendations")
	requireContainsString(t, quickstart, "bv robot-capabilities --json     # Machine-readable command manifest")
	dataSource, ok := guide["data_source"].(string)
	if !ok {
		t.Fatalf("data_source has unexpected type %T", guide["data_source"])
	}
	if !strings.Contains(dataSource, ".beads/beads.jsonl") || !strings.Contains(dataSource, ".beads/issues.jsonl") {
		t.Fatalf("data_source should mention both canonical and compatibility JSONL paths, got %q", dataSource)
	}

	exampleDocs := generateRobotDocs("examples")
	examples, ok := exampleDocs["examples"].([]map[string]string)
	if !ok {
		t.Fatalf("examples has unexpected type %T", exampleDocs["examples"])
	}
	commands := make([]string, 0, len(examples))
	for _, example := range examples {
		command := example["command"]
		commands = append(commands, command)
		if strings.Contains(command, "| sh") {
			t.Fatalf("robot docs example auto-executes shell output: %s", command)
		}
	}
	requireContainsString(t, commands, "bv robot-next --json | jq -r '.claim_command'")
	requireContainsString(t, commands, `bv robot-search "authentication" --json`)
	requireContainsString(t, commands, "BV_OUTPUT_FORMAT=toon bv robot-triage")
	for _, command := range commands {
		if strings.Contains(command, "BV_OUTPUT_FORMAT=toon") && strings.Contains(command, "--json") {
			t.Fatalf("env default example is overridden by --json: %s", command)
		}
	}
}

func TestRobotSchemaCoversDocumentedRobotCommands(t *testing.T) {
	schemas := generateRobotSchemas()
	for name := range robotCommandDocs() {
		if _, ok := schemas.Commands[name]; !ok {
			t.Fatalf("schema missing documented command %q", name)
		}
	}
	for _, name := range []string{"robot-capabilities", "robot-related", "robot-file-hotspots", "robot-impact"} {
		if _, ok := schemas.Commands[name]; !ok {
			t.Fatalf("schema missing %q", name)
		}
	}
}

func TestRobotCapabilitiesSchemaDocumentsCommandMetadata(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-capabilities"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-capabilities properties has unexpected type %T", schema["properties"])
	}
	if properties["commands"] == nil {
		t.Fatalf("robot-capabilities schema missing commands property")
	}

	commandsProp, ok := properties["commands"].(map[string]interface{})
	if !ok {
		t.Fatalf("commands property has unexpected type %T", properties["commands"])
	}
	items, ok := commandsProp["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("commands items has unexpected type %T", commandsProp["items"])
	}
	commandProperties, ok := items["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("command properties has unexpected type %T", items["properties"])
	}
	for _, name := range []string{"preferred_invocation", "accepted_invocations", "needs_git", "needs_sprint", "needs_baseline", "mutates_state"} {
		if commandProperties[name] == nil {
			t.Fatalf("robot-capabilities command schema missing %q", name)
		}
	}
}

func TestRobotSchemaCommandSchemaMatchesHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-schema")
	for _, name := range []string{"schema_version", "generated_at", "envelope", "commands", "command", "schema"} {
		if properties[name] == nil {
			t.Fatalf("robot-schema schema missing property %q", name)
		}
	}
	for _, stale := range []string{"output_format", "version"} {
		if properties[stale] != nil {
			t.Fatalf("robot-schema schema still exposes stale generic property %q", stale)
		}
	}
	if schemas.Commands["robot-schema"]["oneOf"] == nil {
		t.Fatalf("robot-schema schema should distinguish full and single-command outputs")
	}
}

func TestRobotDocsSchemaMatchesTopicOutputs(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-docs")
	for _, name := range []string{
		"generated_at",
		"output_format",
		"version",
		"topic",
		"guide",
		"commands",
		"examples",
		"environment_variables",
		"exit_codes",
		"error",
		"available_topics",
		"did_you_mean",
		"suggested_action",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-docs schema missing property %q", name)
		}
	}
	if properties["data_hash"] != nil {
		t.Fatalf("robot-docs schema still exposes stale generic data_hash property")
	}
}

func TestRobotHelpSchemaMatchesStructuredHelpAlias(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-help")
	for _, name := range []string{"generated_at", "output_format", "version", "topic", "guide"} {
		if properties[name] == nil {
			t.Fatalf("robot-help schema missing property %q", name)
		}
	}
	for _, stale := range []string{"data_hash", "commands", "examples", "environment_variables", "exit_codes"} {
		if properties[stale] != nil {
			t.Fatalf("robot-help schema still exposes stale or unrelated property %q", stale)
		}
	}
}

func TestRobotSearchSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-search")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"query", "provider", "model", "dim", "index_path", "index",
		"loaded", "limit", "mode", "preset", "weights", "results", "usage_hints",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-search schema missing top-level property %q", name)
		}
	}

	resultsProp, ok := properties["results"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-search results has unexpected type %T", properties["results"])
	}
	resultProps := requireNestedSchemaProperties(t, resultsProp["items"], "robot-search result item")
	for _, name := range []string{"issue_id", "score", "text_score", "title", "component_scores"} {
		if resultProps[name] == nil {
			t.Fatalf("robot-search result schema missing %q", name)
		}
	}
}

func TestRobotHistorySchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-history")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"git_range", "latest_commit_sha", "stats", "histories", "commit_index",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-history schema missing top-level property %q", name)
		}
	}
}

func TestRobotCorrelationStatsSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-correlation-stats")
	for _, name := range []string{
		"generated_at", "output_format", "version",
		"total_feedback", "confirmed", "rejected", "ignored",
		"accuracy_rate", "avg_confirm_conf", "avg_reject_conf",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-correlation-stats schema missing top-level property %q", name)
		}
	}
	if properties["data_hash"] != nil {
		t.Fatalf("robot-correlation-stats schema should not expose issue data_hash")
	}

	docs := robotCommandDocs()
	requireContainsString(t, docs["robot-correlation-stats"].KeyFields, "total_feedback")
	requireContainsString(t, docs["robot-correlation-stats"].KeyFields, "accuracy_rate")
	staleFields := map[string]struct{}{"total": {}, "by_user": {}}
	for _, field := range docs["robot-correlation-stats"].KeyFields {
		if _, ok := staleFields[field]; ok {
			t.Fatalf("robot-correlation-stats key fields still contain stale field %q", field)
		}
	}
}

func TestRobotCorrelationStatsOutputIncludesEnvelope(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)

	out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-correlation-stats")
	if err != nil {
		t.Fatalf("robot-correlation-stats failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("robot-correlation-stats JSON: %v\n%s", err, out)
	}
	for _, name := range []string{"generated_at", "output_format", "version", "total_feedback", "confirmed", "rejected", "ignored"} {
		if payload[name] == nil {
			t.Fatalf("robot-correlation-stats output missing %q: %#v", name, payload)
		}
	}
	if payload["data_hash"] != nil {
		t.Fatalf("robot-correlation-stats output should not include data_hash: %#v", payload)
	}
}

func TestRobotOrphansSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-orphans")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"git_range", "stats", "candidates", "by_bead",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-orphans schema missing top-level property %q", name)
		}
	}

	statsProps := requireNestedSchemaProperties(t, properties["stats"], "robot-orphans stats")
	for _, name := range []string{
		"total_commits", "correlated_count", "orphan_count",
		"candidate_count", "orphan_ratio", "avg_suspicion_score",
	} {
		if statsProps[name] == nil {
			t.Fatalf("robot-orphans stats schema missing %q", name)
		}
	}

	candidatesProp, ok := properties["candidates"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-orphans candidates has unexpected type %T", properties["candidates"])
	}
	candidateTypes, ok := candidatesProp["type"].([]string)
	if ok {
		t.Fatalf("robot-orphans candidates should not be nullable: %#v", candidateTypes)
	}
	candidateType, ok := candidatesProp["type"].(string)
	if !ok {
		t.Fatalf("robot-orphans candidates type has unexpected type %T", candidatesProp["type"])
	}
	requireString(t, candidateType, "array")

	candidateProps := requireNestedSchemaProperties(t, candidatesProp["items"], "robot-orphans candidate")
	for _, name := range []string{
		"sha", "short_sha", "message", "author", "author_email", "timestamp",
		"files", "suspicion_score", "probable_beads", "signals",
	} {
		if candidateProps[name] == nil {
			t.Fatalf("robot-orphans candidate schema missing %q", name)
		}
	}

	probableBeadsProp, ok := candidateProps["probable_beads"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-orphans probable_beads has unexpected type %T", candidateProps["probable_beads"])
	}
	probableBeadProps := requireNestedSchemaProperties(t, probableBeadsProp["items"], "robot-orphans probable bead")
	for _, name := range []string{"bead_id", "bead_title", "bead_status", "confidence", "reasons"} {
		if probableBeadProps[name] == nil {
			t.Fatalf("robot-orphans probable bead schema missing %q", name)
		}
	}

	signalsProp, ok := candidateProps["signals"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-orphans signals has unexpected type %T", candidateProps["signals"])
	}
	signalProps := requireNestedSchemaProperties(t, signalsProp["items"], "robot-orphans signal")
	for _, name := range []string{"signal", "details", "weight"} {
		if signalProps[name] == nil {
			t.Fatalf("robot-orphans signal schema missing %q", name)
		}
	}

	byBeadProp, ok := properties["by_bead"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-orphans by_bead has unexpected type %T", properties["by_bead"])
	}
	byBeadValues, ok := byBeadProp["additionalProperties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-orphans by_bead values have unexpected type %T", byBeadProp["additionalProperties"])
	}
	byBeadTypes, ok := byBeadValues["type"].([]string)
	if ok {
		t.Fatalf("robot-orphans by_bead values should not be nullable: %#v", byBeadTypes)
	}
	byBeadType, ok := byBeadValues["type"].(string)
	if !ok {
		t.Fatalf("robot-orphans by_bead value type has unexpected type %T", byBeadValues["type"])
	}
	requireString(t, byBeadType, "array")

	docs := robotCommandDocs()
	requireContainsString(t, docs["robot-orphans"].KeyFields, "stats.candidate_count")
	requireContainsString(t, docs["robot-orphans"].KeyFields, "candidates[].probable_beads")
	requireContainsString(t, docs["robot-orphans"].KeyFields, "by_bead")
}

func TestRobotFileWorkflowSchemasMatchHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()
	tests := []struct {
		command          string
		topLevelFields   []string
		arrayField       string
		arrayItemFields  []string
		nestedObjectName string
		nestedFields     []string
	}{
		{
			command:         "robot-file-beads",
			topLevelFields:  []string{"file_path", "total_beads", "open_beads", "closed_beads"},
			arrayField:      "open_beads",
			arrayItemFields: []string{"bead_id", "title", "status", "commit_shas", "last_touch", "total_changes"},
		},
		{
			command:          "robot-file-hotspots",
			topLevelFields:   []string{"hotspots", "stats"},
			arrayField:       "hotspots",
			arrayItemFields:  []string{"file_path", "total_beads", "open_beads", "closed_beads"},
			nestedObjectName: "stats",
			nestedFields:     []string{"total_files", "total_bead_links", "files_with_multiple_beads"},
		},
		{
			command:         "robot-file-relations",
			topLevelFields:  []string{"file_path", "total_commits", "threshold", "related_files"},
			arrayField:      "related_files",
			arrayItemFields: []string{"file_path", "co_change_count", "total_commits", "correlation", "sample_commits"},
		},
		{
			command:         "robot-impact",
			topLevelFields:  []string{"files", "risk_level", "risk_score", "summary", "warnings", "affected_beads"},
			arrayField:      "affected_beads",
			arrayItemFields: []string{"bead_id", "title", "status", "overlap_files", "overlap_count", "last_activity", "relevance", "total_changes"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			properties := requireRobotSchemaProperties(t, schemas, tc.command)
			for _, name := range []string{"generated_at", "data_hash", "output_format", "version"} {
				if properties[name] == nil {
					t.Fatalf("%s schema missing envelope property %q", tc.command, name)
				}
			}
			for _, name := range tc.topLevelFields {
				if properties[name] == nil {
					t.Fatalf("%s schema missing top-level property %q", tc.command, name)
				}
			}
			arrayProp, ok := properties[tc.arrayField].(map[string]interface{})
			if !ok {
				t.Fatalf("%s %s has unexpected type %T", tc.command, tc.arrayField, properties[tc.arrayField])
			}
			arrayTypes, ok := arrayProp["type"].([]string)
			if !ok {
				t.Fatalf("%s %s type has unexpected type %T", tc.command, tc.arrayField, arrayProp["type"])
			}
			requireContainsString(t, arrayTypes, "array")
			requireContainsString(t, arrayTypes, "null")
			itemProps := requireNestedSchemaProperties(t, arrayProp["items"], tc.command+" "+tc.arrayField+" item")
			for _, name := range tc.arrayItemFields {
				if itemProps[name] == nil {
					t.Fatalf("%s %s item schema missing %q", tc.command, tc.arrayField, name)
				}
			}
			if tc.nestedObjectName != "" {
				nestedProps := requireNestedSchemaProperties(t, properties[tc.nestedObjectName], tc.command+" "+tc.nestedObjectName)
				for _, name := range tc.nestedFields {
					if nestedProps[name] == nil {
						t.Fatalf("%s nested %s schema missing %q", tc.command, tc.nestedObjectName, name)
					}
				}
			}
		})
	}
}

func TestRobotFileWorkflowDocsExposeLiveJSONPaths(t *testing.T) {
	docs := robotCommandDocs()
	expectations := map[string][]string{
		"robot-file-beads":     {"file_path", "total_beads", "open_beads", "closed_beads"},
		"robot-file-hotspots":  {"hotspots", "stats.total_files"},
		"robot-file-relations": {"file_path", "total_commits", "threshold", "related_files"},
		"robot-impact":         {"files", "risk_level", "risk_score", "affected_beads"},
	}
	for command, fields := range expectations {
		for _, field := range fields {
			requireContainsString(t, docs[command].KeyFields, field)
		}
	}
}

func TestRobotRelationshipWorkflowSchemasMatchHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()

	relatedProps := requireRobotSchemaProperties(t, schemas, "robot-related")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"target_bead_id", "target_title", "file_overlap", "commit_overlap",
		"dependency_cluster", "concurrent", "total_related",
	} {
		if relatedProps[name] == nil {
			t.Fatalf("robot-related schema missing top-level property %q", name)
		}
	}
	fileOverlapProp, ok := relatedProps["file_overlap"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-related file_overlap has unexpected type %T", relatedProps["file_overlap"])
	}
	relatedItemProps := requireNestedSchemaProperties(t, fileOverlapProp["items"], "robot-related item")
	for _, name := range []string{"bead_id", "title", "status", "relation_type", "relevance", "reason", "shared_files", "shared_commits"} {
		if relatedItemProps[name] == nil {
			t.Fatalf("robot-related item schema missing %q", name)
		}
	}

	blockerProps := requireRobotSchemaProperties(t, schemas, "robot-blocker-chain")
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "result"} {
		if blockerProps[name] == nil {
			t.Fatalf("robot-blocker-chain schema missing top-level property %q", name)
		}
	}
	blockerResultProps := requireNestedSchemaProperties(t, blockerProps["result"], "robot-blocker-chain result")
	for _, name := range []string{"target_id", "target_title", "is_blocked", "chain_length", "root_blockers", "chain", "has_cycle", "cycle_ids"} {
		if blockerResultProps[name] == nil {
			t.Fatalf("robot-blocker-chain result schema missing %q", name)
		}
	}
	chainProp, ok := blockerResultProps["chain"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-blocker-chain chain has unexpected type %T", blockerResultProps["chain"])
	}
	blockerEntryProps := requireNestedSchemaProperties(t, chainProp["items"], "robot-blocker-chain entry")
	for _, name := range []string{"id", "title", "status", "priority", "depth", "is_root", "actionable", "blocks_count"} {
		if blockerEntryProps[name] == nil {
			t.Fatalf("robot-blocker-chain entry schema missing %q", name)
		}
	}

	whatIfProps := requireRobotSchemaProperties(t, schemas, "robot-whatif")
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "result"} {
		if whatIfProps[name] == nil {
			t.Fatalf("robot-whatif schema missing top-level property %q", name)
		}
	}
	whatIfResultProps := requireNestedSchemaProperties(t, whatIfProps["result"], "robot-whatif result")
	for _, name := range []string{"issue_id", "title", "status", "priority", "delta"} {
		if whatIfResultProps[name] == nil {
			t.Fatalf("robot-whatif result schema missing %q", name)
		}
	}
	whatIfDeltaProps := requireNestedSchemaProperties(t, whatIfResultProps["delta"], "robot-whatif delta")
	for _, name := range []string{"direct_unblocks", "transitive_unblocks", "blocked_reduction", "depth_reduction", "unblocked_issue_ids", "parallelization_gain", "explanation"} {
		if whatIfDeltaProps[name] == nil {
			t.Fatalf("robot-whatif delta schema missing %q", name)
		}
	}

	networkProps := requireRobotSchemaProperties(t, schemas, "robot-impact-network")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"bead_id", "depth", "network", "stats", "top_clusters", "top_connected",
	} {
		if networkProps[name] == nil {
			t.Fatalf("robot-impact-network schema missing top-level property %q", name)
		}
	}
	networkStatsProps := requireNestedSchemaProperties(t, networkProps["stats"], "robot-impact-network stats")
	for _, name := range []string{"total_nodes", "total_edges", "cluster_count", "avg_degree", "max_degree", "density", "isolated_nodes", "largest_cluster"} {
		if networkStatsProps[name] == nil {
			t.Fatalf("robot-impact-network stats schema missing %q", name)
		}
	}
	topConnectedProp, ok := networkProps["top_connected"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-impact-network top_connected has unexpected type %T", networkProps["top_connected"])
	}
	nodeProps := requireNestedSchemaProperties(t, topConnectedProp["items"], "robot-impact-network node")
	for _, name := range []string{"bead_id", "title", "status", "priority", "last_activity", "degree", "cluster_id", "commit_count", "file_count", "connectivity"} {
		if nodeProps[name] == nil {
			t.Fatalf("robot-impact-network node schema missing %q", name)
		}
	}

	causalityProps := requireRobotSchemaProperties(t, schemas, "robot-causality")
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "chain", "insights"} {
		if causalityProps[name] == nil {
			t.Fatalf("robot-causality schema missing top-level property %q", name)
		}
	}
	causalChainProps := requireNestedSchemaProperties(t, causalityProps["chain"], "robot-causality chain")
	for _, name := range []string{"bead_id", "title", "status", "events", "edge_count", "start_time", "end_time", "total_time", "is_complete", "links", "duration_known", "time_basis"} {
		if causalChainProps[name] == nil {
			t.Fatalf("robot-causality chain schema missing %q", name)
		}
	}
	causalInsightsProps := requireNestedSchemaProperties(t, causalityProps["insights"], "robot-causality insights")
	for _, name := range []string{"total_duration", "blocked_duration", "active_duration", "blocked_percentage", "blocked_periods", "critical_path", "summary", "recommendations", "coverage", "limitations", "blocked_duration_known", "explicit_blocked_duration", "dependency_wait_duration", "critical_path_duration"} {
		if causalInsightsProps[name] == nil {
			t.Fatalf("robot-causality insights schema missing %q", name)
		}
	}
}

func TestRobotCausalityCommittedWaitAndUnknownSchema(t *testing.T) {
	exe := buildTestBinary(t)
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(start.Add(12*time.Hour).Unix(), 10))
	for _, tc := range []struct {
		name       string
		hours      []int
		statuses   []string
		dependency bool
		missing    bool
		want       any
	}{
		{"known six hours scoped blocker", []int{0, 2, 8, 10}, []string{"open", "blocked", "open", "closed"}, true, false, float64(6 * time.Hour)},
		{"known zero", []int{0, 10}, []string{"open", "closed"}, false, false, float64(0)},
		{"unknown missing blocker", []int{0, 2}, []string{"open", "open"}, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git := func(at time.Time, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at.Format(time.RFC3339), "GIT_COMMITTER_DATE="+at.Format(time.RFC3339))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git(start, "init", "-b", "main")
			if err := os.Mkdir(filepath.Join(repo, ".beads"), 0o755); err != nil {
				t.Fatal(err)
			}
			var blockedSHA, anchorSHA string
			for i, hour := range tc.hours {
				at := start.Add(time.Duration(hour) * time.Hour)
				deps := "[]"
				if tc.dependency && hour >= 2 {
					deps = `[{"depends_on_id":"B","type":"blocks"}]`
				}
				data := fmt.Sprintf("{\"id\":\"A\",\"title\":\"Scoped target\",\"status\":%q,\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q,\"labels\":[\"backend\"],\"dependencies\":%s}\n", tc.statuses[i], start.Format(time.RFC3339), at.Format(time.RFC3339), deps)
				if !tc.missing {
					status := "open"
					if hour >= 8 {
						status = "closed"
					}
					data += fmt.Sprintf("{\"id\":\"B\",\"title\":\"Outside selected label\",\"status\":%q,\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q,\"labels\":[\"external\"]}\n", status, start.Format(time.RFC3339), at.Format(time.RFC3339))
				}
				if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
				git(at, "add", ".beads/issues.jsonl")
				git(at, "commit", "-m", fmt.Sprintf("snapshot %d", i))
				if tc.want == float64(6*time.Hour) && hour == 2 {
					blockedSHA = git(at, "rev-parse", "HEAD")
					git(start.Add(3*time.Hour), "commit", "--allow-empty", "-m", "A: code-only anchor")
					anchorSHA = git(at, "rev-parse", "HEAD")
				}
			}
			out, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-causality", "A", "--label", "backend")
			if err != nil {
				t.Fatalf("actual causal CLI: %v\nstdout:%s\nstderr:%s", err, out, stderr)
			}
			var payload struct {
				Chain    map[string]any `json:"chain"`
				Insights map[string]any `json:"insights"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatalf("invalid output: %v\n%s\n%s", err, out, stderr)
			}
			if got := payload.Insights["blocked_duration"]; got != tc.want {
				t.Fatalf("blocked_duration=%v expected%v\n%s\n%s", got, tc.want, out, stderr)
			}
			if payload.Insights["blocked_duration_known"] != (tc.want != nil) {
				t.Fatalf("measurement availability disagrees: %s", out)
			}
			if payload.Chain["bead_id"] != "A" {
				t.Fatalf("target scope changed: %s", out)
			}
			props := requireNestedSchemaProperties(t, robotCausalInsightsSchema(), "causal insights")
			for _, field := range []string{"blocked_duration", "explicit_blocked_duration", "dependency_wait_duration"} {
				definition := props[field].(map[string]interface{})
				if !reflect.DeepEqual(definition["type"], []string{"integer", "null"}) {
					t.Fatalf("schema rejects actual null/known duration for%s: %v", field, definition)
				}
			}
			if blockedSHA != "" {
				for _, window := range [][]string{{"--history-limit", "2"}, {"--history-since", "2025-01-15T03:30:00Z"}} {
					t.Run(window[0], func(t *testing.T) {
						args := append([]string{"--robot-causality", "A", "--label", "backend"}, window...)
						out, stderr, err := runCommandWithTimeout(t, repo, exe, args...)
						if err != nil {
							t.Fatalf("bounded causal CLI %v: %v\n%s\n%s", args, err, out, stderr)
						}
						var got correlation.CausalityResult
						if err := json.Unmarshal([]byte(out), &got); err != nil {
							t.Fatal(err)
						}
						if got.Insights.BlockedDurationKnown || got.Insights.Coverage != "partial" || !strings.Contains(out, `"blocked_duration":null`) || strings.Contains(out, blockedSHA) {
							t.Fatalf("truncated history claimed complete measurement or included excluded commit:\n%s", out)
						}
					})
				}
				t.Run("invalid since rejected", func(t *testing.T) {
					out, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-causality", "A", "--history-since", "invalid-date")
					if err == nil || !strings.Contains(stderr, "parsing --history-since") || strings.Contains(out, `"chain"`) {
						t.Fatalf("invalid history bound accepted or misdiagnosed: %v\n%s\n%s", err, out, stderr)
					}
				})
				checkRevision := func(t *testing.T, sha string, hours int, wantBlocked time.Duration) string {
					t.Helper()
					out, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-causality", "A", "--label", "backend", "--as-of", sha)
					if err != nil {
						t.Fatalf("revision-bound causal CLI: %v\n%s\n%s", err, out, stderr)
					}
					var got correlation.CausalityResult
					if err := json.Unmarshal([]byte(out), &got); err != nil {
						t.Fatal(err)
					}
					end := start.Add(time.Duration(hours) * time.Hour)
					if !got.Chain.EndTime.Equal(end) || !got.Insights.BlockedDurationKnown || got.Insights.BlockedDuration != wantBlocked || got.Insights.Coverage != "complete" {
						t.Fatalf("wrong revision measurement, want end%s wait%s:\n%s", end, wantBlocked, out)
					}
					for _, event := range got.Chain.Events {
						if event.Timestamp.After(end) || event.Type == correlation.CausalClosed {
							t.Fatalf("future lifecycle leaked into %s: %+v\n%s", sha, event, out)
						}
					}
					if strings.Contains(out, `"unsupported":["as_of"]`) {
						t.Fatalf("implemented as-of still declared unsupported:\n%s", out)
					}
					return out
				}
				t.Run("revision cold and warm", func(t *testing.T) {
					cold := checkRevision(t, blockedSHA, 2, 0)
					warm := checkRevision(t, blockedSHA, 2, 0)
					if cold != warm {
						t.Fatalf("revision cache changed exact output:\ncold%s\nwarm%s", cold, warm)
					}
				})
				t.Run("code-only revision extends observed ongoing wait", func(t *testing.T) {
					checkRevision(t, anchorSHA, 3, time.Hour)
				})
				t.Run("current cache remains distinct", func(t *testing.T) {
					again, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-causality", "A", "--label", "backend")
					if err != nil || again != out {
						t.Fatalf("historical query contaminated current result: %v\n%s\n%s", err, again, stderr)
					}
				})
				t.Run("backdated descendant excluded by ancestry", func(t *testing.T) {
					path := filepath.Join(repo, ".beads", "issues.jsonl")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					data = bytes.ReplaceAll(data, []byte("Scoped target"), []byte("Future target edit"))
					if err := os.WriteFile(path, data, 0o644); err != nil {
						t.Fatal(err)
					}
					git(start.Add(time.Hour), "add", ".beads/issues.jsonl")
					git(start.Add(time.Hour), "commit", "-m", "A: backdated future descendant")
					future := git(start, "rev-parse", "HEAD")
					bounded := checkRevision(t, blockedSHA, 2, 0)
					if strings.Contains(bounded, future) || strings.Contains(bounded, "Future target edit") {
						t.Fatalf("backdated descendant leaked:\n%s", bounded)
					}
				})
			}
		})
	}
}

func TestRobotCausalityUsesHistoricalSourcePath(t *testing.T) {
	exe := buildTestBinary(t)
	repo := t.TempDir()
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(start.Add(12*time.Hour).Unix(), 10))
	git := func(hour int, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		at := start.Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(0, "init", "-b", "main")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var historicalSHA string
	for _, step := range []struct {
		hour         int
		file, status string
	}{{0, "beads.jsonl", "open"}, {2, "beads.jsonl", "blocked"}, {8, "issues.jsonl", "closed"}} {
		data := fmt.Sprintf("{\"id\":\"A\",\"title\":\"Historical target\",\"status\":%q,\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", step.status, start.Format(time.RFC3339), start.Add(time.Duration(step.hour)*time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(beadsDir, step.file), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		git(step.hour, "add", ".beads/"+step.file)
		git(step.hour, "commit", "-m", step.status)
		if step.hour == 2 {
			historicalSHA = git(2, "rev-parse", "HEAD")
		}
	}
	var firstOutput string
	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprintf("live_directory_%v", live), func(t *testing.T) {
			if !live {
				// Preserve the fixture bytes while proving the query requires no live source.
				if err := os.Rename(beadsDir, filepath.Join(repo, "preserved-live-beads")); err != nil {
					t.Fatal(err)
				}
			}
			out, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-causality", "A", "--as-of", historicalSHA)
			if err != nil {
				t.Fatalf("historical path CLI: %v\n%s\n%s", err, out, stderr)
			}
			var got struct {
				correlation.CausalityResult
				SourceAuthority RobotSourceAuthority `json:"source_authority"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.SourceAuthority.Sources) != 1 || got.SourceAuthority.Sources[0].SourcePath != ".beads/beads.jsonl@"+historicalSHA || got.Chain.Status != "blocked" || got.Insights.Coverage != "complete" || !got.Insights.BlockedDurationKnown || got.Insights.BlockedDuration != 0 || !got.Chain.EndTime.Equal(start.Add(2*time.Hour)) || len(got.Chain.Events) < 2 {
				t.Fatalf("historical authority and causal consumer disagree:\n%s\n%s", out, stderr)
			}
			if live {
				firstOutput = out
			} else if firstOutput != out {
				t.Fatalf("live file presence changed historical result:\n%s\n%s", firstOutput, out)
			}
		})
	}
}

func TestRobotCorrelationHandlersRespectAsOfAndSQLiteWorkspace(t *testing.T) {
	exe := buildTestBinary(t)
	repo := t.TempDir()
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(start.Add(12*time.Hour).Unix(), 10))
	git := func(hour int, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		at := start.Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(0, "init", "-b", "main")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeStep := func(hour int, beadsFile, status, codeLine, commitMsg string) {
		data := fmt.Sprintf("{\"id\":\"A\",\"title\":\"Historical target\",\"status\":%q,\"priority\":2,\"issue_type\":\"task\",\"created_at\":%q,\"updated_at\":%q}\n", status, start.Format(time.RFC3339), start.Add(time.Duration(hour)*time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(beadsDir, beadsFile), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "svc.go"), []byte("package main\n"+codeLine+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(hour, "add", ".beads/"+beadsFile, "svc.go")
		git(hour, "commit", "-m", commitMsg)
	}

	writeStep(0, "beads.jsonl", "open", "// A: initial", "A: initial open")
	writeStep(2, "beads.jsonl", "in_progress", "// A: historical fix", "A: historical blocked fix")
	historicalSHA := git(2, "rev-parse", "HEAD")

	// Hour 8 adds a future commit touching svc.go and an unrelated orphan file.
	if err := os.WriteFile(filepath.Join(repo, "orphan.go"), []byte("package main\n// future orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(8, "add", "orphan.go")
	writeStep(8, "issues.jsonl", "closed", "// A: future close", "unrelated future change and close")
	futureSHA := git(8, "rev-parse", "HEAD")

	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprintf("live_directory_%v", live), func(t *testing.T) {
			preservedDir := filepath.Join(repo, "preserved-live-beads")
			if !live {
				if err := os.Rename(beadsDir, preservedDir); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Rename(preservedDir, beadsDir)
				})
			}

			histOut, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-history", "--as-of", historicalSHA)
			if err != nil {
				t.Fatalf("robot-history --as-of failed: %v\n%s\n%s", err, histOut, stderr)
			}
			if !strings.Contains(histOut, "historical blocked fix") {
				t.Fatalf("robot-history --as-of missing historical commit:\n%s", histOut)
			}
			if strings.Contains(histOut, "unrelated future change") || strings.Contains(histOut, futureSHA) {
				t.Fatalf("robot-history --as-of leaked future commit:\n%s", histOut)
			}

			orphansOut, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-orphans", "--as-of", historicalSHA)
			if err != nil {
				t.Fatalf("robot-orphans --as-of failed: %v\n%s\n%s", err, orphansOut, stderr)
			}
			if strings.Contains(orphansOut, "orphan.go") || strings.Contains(orphansOut, futureSHA) {
				t.Fatalf("robot-orphans --as-of leaked future orphan commit:\n%s", orphansOut)
			}

			fileBeadsOut, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-file-beads", "svc.go", "--as-of", historicalSHA)
			if err != nil {
				t.Fatalf("robot-file-beads --as-of failed: %v\n%s\n%s", err, fileBeadsOut, stderr)
			}
			if strings.Contains(fileBeadsOut, "unrelated future change") || strings.Contains(fileBeadsOut, futureSHA) {
				t.Fatalf("robot-file-beads --as-of leaked future commit:\n%s", fileBeadsOut)
			}

			if live {
				explainOut, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-explain-correlation", historicalSHA+":A", "--as-of", historicalSHA)
				if err != nil {
					t.Fatalf("robot-explain-correlation --as-of failed: %v\n%s\n%s", err, explainOut, stderr)
				}
				if !strings.Contains(explainOut, historicalSHA) || !strings.Contains(explainOut, `"bead_id":"A"`) {
					t.Fatalf("robot-explain-correlation --as-of missing expected explanation:\n%s", explainOut)
				}
			}
		})
	}

	t.Run("sqlite_only_working_tree", func(t *testing.T) {
		if err := os.Remove(filepath.Join(beadsDir, "beads.jsonl")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(beadsDir, "issues.jsonl")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads.db"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := correlation.ValidateRepository(repo); err != nil {
			t.Fatalf("ValidateRepository rejected SQLite-backed workspace: %v", err)
		}
		_, resolved, err := resolveCorrelationBeadsPath(repo)
		if err != nil {
			t.Fatalf("resolveCorrelationBeadsPath failed in SQLite-backed workspace: %v", err)
		}
		if filepath.Base(resolved) != "issues.jsonl" {
			t.Fatalf("expected issues.jsonl from git HEAD fallback, got %q", resolved)
		}
		explainOut, stderr, err := runCommandWithTimeout(t, repo, exe, "--robot-explain-correlation", historicalSHA+":A", "--as-of", historicalSHA)
		if err != nil {
			t.Fatalf("robot-explain-correlation --as-of in SQLite-only workspace failed: %v\n%s\n%s", err, explainOut, stderr)
		}
		if !strings.Contains(explainOut, historicalSHA) {
			t.Fatalf("robot-explain-correlation --as-of in SQLite-only workspace missing commit SHA:\n%s", explainOut)
		}
	})
}

func TestRobotRelationshipWorkflowDocsExposeLiveJSONPaths(t *testing.T) {
	docs := robotCommandDocs()
	expectations := map[string][]string{
		"robot-related":        {"target_bead_id", "total_related", "file_overlap"},
		"robot-blocker-chain":  {"result.target_id", "result.root_blockers", "result.chain"},
		"robot-whatif":         {"result.issue_id", "result.delta.direct_unblocks", "result.delta.transitive_unblocks", "result.delta.unblocked_issue_ids"},
		"robot-impact-network": {"network.nodes", "stats.total_nodes", "top_connected"},
		"robot-causality":      {"chain.events", "insights.summary", "insights.recommendations"},
	}
	for command, fields := range expectations {
		for _, field := range fields {
			requireContainsString(t, docs[command].KeyFields, field)
		}
	}
}

func TestRobotGroupedTriageSchemasMatchHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	for _, tc := range []struct {
		command       string
		groupProperty string
		groupFields   []string
	}{
		{
			command:       "robot-triage-by-track",
			groupProperty: "recommendations_by_track",
			groupFields:   []string{"track_id", "reason", "recommendations", "top_pick", "claim_command", "total_unblocks"},
		},
		{
			command:       "robot-triage-by-label",
			groupProperty: "recommendations_by_label",
			groupFields:   []string{"label", "recommendations", "top_pick", "claim_command", "total_unblocks"},
		},
	} {
		t.Run(tc.command, func(t *testing.T) {
			properties := requireRobotSchemaProperties(t, schemas, tc.command)
			for _, name := range []string{"generated_at", "data_hash", "triage", "usage_hints"} {
				if properties[name] == nil {
					t.Fatalf("%s schema missing top-level property %q", tc.command, name)
				}
			}
			for _, stale := range []string{"output_format", "version"} {
				if properties[stale] != nil {
					t.Fatalf("%s schema still exposes stale generic property %q", tc.command, stale)
				}
			}

			triageSchema, ok := properties["triage"].(map[string]interface{})
			if !ok {
				t.Fatalf("%s triage has unexpected type %T", tc.command, properties["triage"])
			}
			if required, ok := triageSchema["required"].([]string); ok {
				for _, name := range required {
					if strings.Compare(name, tc.groupProperty) == 0 {
						t.Fatalf("%s should document optional grouped property %q without requiring it", tc.command, tc.groupProperty)
					}
				}
			}

			triageProps := requireNestedSchemaProperties(t, triageSchema, tc.command+" triage")
			groupProp, ok := triageProps[tc.groupProperty].(map[string]interface{})
			if !ok {
				t.Fatalf("%s triage missing grouped property %q", tc.command, tc.groupProperty)
			}
			groupProps := requireNestedSchemaProperties(t, groupProp["items"], tc.command+" group item")
			for _, name := range tc.groupFields {
				if groupProps[name] == nil {
					t.Fatalf("%s group schema missing %q", tc.command, name)
				}
			}
		})
	}
}

func TestRobotGroupedTriageDocsUseLiveJSONPaths(t *testing.T) {
	docs := robotCommandDocs()
	requireContainsString(t, docs["robot-triage-by-track"].KeyFields, "triage.recommendations_by_track[].top_pick")
	requireContainsString(t, docs["robot-triage-by-label"].KeyFields, "triage.recommendations_by_label[].claim_command")
	for _, stale := range []string{"tracks[].", "labels[]."} {
		for name, doc := range docs {
			for _, field := range doc.KeyFields {
				if strings.Contains(field, stale) {
					t.Fatalf("%s key field still uses stale grouped path %q", name, field)
				}
			}
		}
	}
}

func TestRobotDiffSchemaMatchesHandlerEnvelope(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-diff"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-diff properties has unexpected type %T", schema["properties"])
	}
	for _, name := range []string{"resolved_revision", "from_data_hash", "to_data_hash", "diff"} {
		if properties[name] == nil {
			t.Fatalf("robot-diff schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"since", "since_commit", "new", "closed", "modified", "cycles"} {
		if properties[stale] != nil {
			t.Fatalf("robot-diff schema still exposes stale top-level property %q", stale)
		}
	}

	diffProp, ok := properties["diff"].(map[string]interface{})
	if !ok {
		t.Fatalf("diff property has unexpected type %T", properties["diff"])
	}
	diffProperties, ok := diffProp["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("diff nested properties has unexpected type %T", diffProp["properties"])
	}
	for _, name := range []string{"new_issues", "closed_issues", "removed_issues", "modified_issues", "metric_deltas", "summary"} {
		if diffProperties[name] == nil {
			t.Fatalf("robot-diff nested schema missing %q", name)
		}
	}
}

func TestRobotForecastSchemaMatchesHandlerEnvelope(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-forecast"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-forecast properties has unexpected type %T", schema["properties"])
	}
	for _, name := range []string{"agents", "filters", "forecast_count", "forecasts", "summary", "output_format", "version"} {
		if properties[name] == nil {
			t.Fatalf("robot-forecast schema missing top-level property %q", name)
		}
	}
	if properties["methodology"] != nil {
		t.Fatalf("robot-forecast schema still exposes stale methodology property")
	}

	summaryProp, ok := properties["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary property has unexpected type %T", properties["summary"])
	}
	summaryProperties, ok := summaryProp["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary nested properties has unexpected type %T", summaryProp["properties"])
	}
	for _, name := range []string{"total_minutes", "total_days", "avg_confidence", "earliest_eta", "latest_eta"} {
		if summaryProperties[name] == nil {
			t.Fatalf("robot-forecast summary schema missing %q", name)
		}
	}
}

func TestRobotBurndownSchemaMatchesHandlerEnvelope(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-burndown"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-burndown properties has unexpected type %T", schema["properties"])
	}
	for _, name := range []string{
		"output_format", "version", "sprint_name", "start_date", "end_date",
		"total_days", "elapsed_days", "remaining_days",
		"total_issues", "completed_issues", "remaining_issues",
		"ideal_burn_rate", "actual_burn_rate", "projected_complete",
		"on_track", "daily_points", "ideal_line",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-burndown schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"burndown", "at_risk"} {
		if properties[stale] != nil {
			t.Fatalf("robot-burndown schema still exposes stale top-level property %q", stale)
		}
	}

	dailyPoints, ok := properties["daily_points"].(map[string]interface{})
	if !ok {
		t.Fatalf("daily_points property has unexpected type %T", properties["daily_points"])
	}
	items, ok := dailyPoints["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("daily_points items has unexpected type %T", dailyPoints["items"])
	}
	pointProperties, ok := items["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("daily_points item properties has unexpected type %T", items["properties"])
	}
	for _, name := range []string{"date", "remaining", "completed"} {
		if pointProperties[name] == nil {
			t.Fatalf("robot-burndown point schema missing %q", name)
		}
	}
}

func TestRobotGraphSchemaMatchesExportResult(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-graph"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-graph properties has unexpected type %T", schema["properties"])
	}
	for _, name := range []string{"format", "graph", "nodes", "edges", "filters_applied", "explanation", "data_hash", "adjacency"} {
		if properties[name] == nil {
			t.Fatalf("robot-graph schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"generated_at", "stats"} {
		if properties[stale] != nil {
			t.Fatalf("robot-graph schema still exposes stale top-level property %q", stale)
		}
	}

	explanation, ok := properties["explanation"].(map[string]interface{})
	if !ok {
		t.Fatalf("explanation property has unexpected type %T", properties["explanation"])
	}
	explanationProperties, ok := explanation["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("explanation nested properties has unexpected type %T", explanation["properties"])
	}
	for _, name := range []string{"what", "how_to_render", "when_to_use"} {
		if explanationProperties[name] == nil {
			t.Fatalf("robot-graph explanation schema missing %q", name)
		}
	}
}

func TestRobotSuggestSchemaMatchesOutputShape(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-suggest"]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-suggest properties has unexpected type %T", schema["properties"])
	}
	for _, name := range []string{"filters", "suggestions", "usage_hints"} {
		if properties[name] == nil {
			t.Fatalf("robot-suggest schema missing top-level property %q", name)
		}
	}
	if properties["counts"] != nil {
		t.Fatalf("robot-suggest schema still exposes stale counts property")
	}

	suggestionsProp, ok := properties["suggestions"].(map[string]interface{})
	if !ok {
		t.Fatalf("suggestions property has unexpected type %T", properties["suggestions"])
	}
	if typeName, ok := suggestionsProp["type"].(string); !ok || strings.Compare(typeName, "object") != 0 {
		t.Fatalf("suggestions property type = %v; want object", suggestionsProp["type"])
	}
	suggestionSetProperties, ok := suggestionsProp["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("suggestion set properties has unexpected type %T", suggestionsProp["properties"])
	}
	for _, name := range []string{"suggestions", "generated_at", "data_hash", "stats"} {
		if suggestionSetProperties[name] == nil {
			t.Fatalf("robot-suggest nested suggestion set missing %q", name)
		}
	}

	stats, ok := suggestionSetProperties["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats property has unexpected type %T", suggestionSetProperties["stats"])
	}
	statsProperties, ok := stats["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats nested properties has unexpected type %T", stats["properties"])
	}
	for _, name := range []string{"total", "by_type", "by_confidence", "high_confidence_count", "actionable_count"} {
		if statsProperties[name] == nil {
			t.Fatalf("robot-suggest stats schema missing %q", name)
		}
	}
}

func TestRobotLabelSchemasMatchHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()

	healthProps := requireRobotSchemaProperties(t, schemas, "robot-label-health")
	for _, name := range []string{"analysis_config", "results", "usage_hints"} {
		if healthProps[name] == nil {
			t.Fatalf("robot-label-health schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"output_format", "version"} {
		if healthProps[stale] != nil {
			t.Fatalf("robot-label-health schema still exposes stale top-level property %q", stale)
		}
	}
	healthResultProps := requireNestedSchemaProperties(t, healthProps["results"], "robot-label-health results")
	for _, name := range []string{"total_labels", "healthy_count", "warning_count", "critical_count", "labels", "summaries", "attention_needed"} {
		if healthResultProps[name] == nil {
			t.Fatalf("robot-label-health results schema missing %q", name)
		}
	}

	flowProps := requireRobotSchemaProperties(t, schemas, "robot-label-flow")
	for _, name := range []string{"flow", "blockage_cascade", "analysis_config", "usage_hints"} {
		if flowProps[name] == nil {
			t.Fatalf("robot-label-flow schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"output_format", "version"} {
		if flowProps[stale] != nil {
			t.Fatalf("robot-label-flow schema still exposes stale top-level property %q", stale)
		}
	}
	flowNestedProps := requireNestedSchemaProperties(t, flowProps["flow"], "robot-label-flow flow")
	for _, name := range []string{"labels", "flow_matrix", "dependencies", "critical_paths", "bottleneck_labels", "total_cross_label_deps"} {
		if flowNestedProps[name] == nil {
			t.Fatalf("robot-label-flow nested schema missing %q", name)
		}
	}

	attentionProps := requireRobotSchemaProperties(t, schemas, "robot-label-attention")
	for _, name := range []string{"limit", "total_labels", "labels", "usage_hints"} {
		if attentionProps[name] == nil {
			t.Fatalf("robot-label-attention schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"output_format", "version"} {
		if attentionProps[stale] != nil {
			t.Fatalf("robot-label-attention schema still exposes stale top-level property %q", stale)
		}
	}
	labelsProp, ok := attentionProps["labels"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-label-attention labels has unexpected type %T", attentionProps["labels"])
	}
	labelItemProps := requireNestedSchemaProperties(t, labelsProp["items"], "robot-label-attention label item")
	for _, name := range []string{"rank", "label", "attention_score", "normalized_score", "reason", "open_count", "blocked_count", "stale_count", "pagerank_sum", "velocity_factor"} {
		if labelItemProps[name] == nil {
			t.Fatalf("robot-label-attention label schema missing %q", name)
		}
	}
}

func TestRobotPrioritySchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-priority")
	for _, name := range []string{
		"analysis_config", "status", "recommendations", "field_descriptions",
		"filters", "summary", "usage_hints",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-priority schema missing top-level property %q", name)
		}
	}

	filters := requireNestedSchemaProperties(t, properties["filters"], "robot-priority filters")
	for _, name := range []string{"min_confidence", "max_results", "by_label", "by_assignee"} {
		if filters[name] == nil {
			t.Fatalf("robot-priority filters schema missing %q", name)
		}
	}

	summary := requireNestedSchemaProperties(t, properties["summary"], "robot-priority summary")
	for _, name := range []string{"total_issues", "recommendations", "high_confidence"} {
		if summary[name] == nil {
			t.Fatalf("robot-priority summary schema missing %q", name)
		}
	}
}

func TestRobotAlertsSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-alerts")
	for _, name := range []string{"output_format", "version", "alerts", "summary", "usage_hints"} {
		if properties[name] == nil {
			t.Fatalf("robot-alerts schema missing top-level property %q", name)
		}
	}

	alertsProp, ok := properties["alerts"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-alerts alerts has unexpected type %T", properties["alerts"])
	}
	alertItemProps := requireNestedSchemaProperties(t, alertsProp["items"], "robot-alerts alert item")
	for _, name := range []string{"type", "severity", "message", "baseline_value", "current_value", "delta", "detected_at"} {
		if alertItemProps[name] == nil {
			t.Fatalf("robot-alerts alert schema missing %q", name)
		}
	}

	summary := requireNestedSchemaProperties(t, properties["summary"], "robot-alerts summary")
	for _, name := range []string{"total", "critical", "warning", "info"} {
		if summary[name] == nil {
			t.Fatalf("robot-alerts summary schema missing %q", name)
		}
	}
}

func TestRobotRecipesSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-recipes")
	for _, name := range []string{"generated_at", "output_format", "version", "recipes"} {
		if properties[name] == nil {
			t.Fatalf("robot-recipes schema missing top-level property %q", name)
		}
	}
	if properties["data_hash"] != nil {
		t.Fatalf("robot-recipes schema should not require issue data_hash")
	}

	recipesProp, ok := properties["recipes"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-recipes recipes has unexpected type %T", properties["recipes"])
	}
	recipeProps := requireNestedSchemaProperties(t, recipesProp["items"], "robot-recipes recipe item")
	for _, name := range []string{"name", "description", "source"} {
		if recipeProps[name] == nil {
			t.Fatalf("robot-recipes recipe schema missing %q", name)
		}
	}
}

func TestRobotRecipesOutputIncludesEnvelope(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()

	out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-recipes")
	if err != nil {
		t.Fatalf("robot-recipes failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("robot-recipes JSON: %v\n%s", err, out)
	}
	for _, name := range []string{"generated_at", "output_format", "version", "recipes"} {
		if payload[name] == nil {
			t.Fatalf("robot-recipes output missing %q: %#v", name, payload)
		}
	}
	if _, ok := payload["recipes"].([]any); !ok {
		t.Fatalf("robot-recipes recipes has unexpected type %T", payload["recipes"])
	}
}

func TestRobotMetricsSchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-metrics")
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "timing", "cache", "memory"} {
		if properties[name] == nil {
			t.Fatalf("robot-metrics schema missing top-level property %q", name)
		}
	}

	memoryProps := requireNestedSchemaProperties(t, properties["memory"], "robot-metrics memory")
	for _, name := range []string{"heap_alloc_mb", "heap_sys_mb", "heap_objects_k", "gc_cycles", "gc_pause_ms", "goroutine_count"} {
		if memoryProps[name] == nil {
			t.Fatalf("robot-metrics memory schema missing %q", name)
		}
	}

	timingProp, ok := properties["timing"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-metrics timing has unexpected type %T", properties["timing"])
	}
	timingProps := requireNestedSchemaProperties(t, timingProp["items"], "robot-metrics timing item")
	for _, name := range []string{"name", "count", "total_ms", "avg_ms", "max_ms", "min_ms"} {
		if timingProps[name] == nil {
			t.Fatalf("robot-metrics timing schema missing %q", name)
		}
	}

	cacheProp, ok := properties["cache"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-metrics cache has unexpected type %T", properties["cache"])
	}
	cacheProps := requireNestedSchemaProperties(t, cacheProp["items"], "robot-metrics cache item")
	for _, name := range []string{"name", "hits", "misses", "total", "hit_rate"} {
		if cacheProps[name] == nil {
			t.Fatalf("robot-metrics cache schema missing %q", name)
		}
	}
}

func TestRobotMetricsOutputIncludesEnvelope(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)

	out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-metrics")
	if err != nil {
		t.Fatalf("robot-metrics failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("robot-metrics JSON: %v\n%s", err, out)
	}
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "memory"} {
		if payload[name] == nil {
			t.Fatalf("robot-metrics output missing %q: %#v", name, payload)
		}
	}
	if _, ok := payload["memory"].(map[string]any); !ok {
		t.Fatalf("robot-metrics memory has unexpected type %T", payload["memory"])
	}
}

func TestRobotSprintSchemasMatchHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()

	listProps := requireRobotSchemaProperties(t, schemas, "robot-sprint-list")
	for _, name := range []string{"output_format", "version", "sprint_count", "sprints"} {
		if listProps[name] == nil {
			t.Fatalf("robot-sprint-list schema missing top-level property %q", name)
		}
	}
	sprintsProp, ok := listProps["sprints"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-sprint-list sprints has unexpected type %T", listProps["sprints"])
	}
	sprintItemProps := requireNestedSchemaProperties(t, sprintsProp["items"], "robot-sprint-list sprint item")
	for _, name := range []string{"id", "name", "start_date", "end_date", "bead_ids", "velocity_target"} {
		if sprintItemProps[name] == nil {
			t.Fatalf("robot-sprint-list sprint schema missing %q", name)
		}
	}

	showProps := requireRobotSchemaProperties(t, schemas, "robot-sprint-show")
	for _, name := range []string{"output_format", "version", "sprint"} {
		if showProps[name] == nil {
			t.Fatalf("robot-sprint-show schema missing top-level property %q", name)
		}
	}
	showSprintProps := requireNestedSchemaProperties(t, showProps["sprint"], "robot-sprint-show sprint")
	for _, name := range []string{"id", "name", "start_date", "end_date", "bead_ids", "velocity_target"} {
		if showSprintProps[name] == nil {
			t.Fatalf("robot-sprint-show sprint schema missing %q", name)
		}
	}
}

func TestRobotCapacitySchemaMatchesHandlerOutput(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-capacity")
	for _, name := range []string{
		"output_format", "version", "agents", "label", "open_issue_count",
		"total_minutes", "total_days", "serial_minutes", "parallel_minutes",
		"parallelizable_pct", "estimated_days", "critical_path_length",
		"critical_path", "actionable_count", "actionable", "bottlenecks",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-capacity schema missing top-level property %q", name)
		}
	}

	bottlenecksProp, ok := properties["bottlenecks"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-capacity bottlenecks has unexpected type %T", properties["bottlenecks"])
	}
	bottleneckProps := requireNestedSchemaProperties(t, bottlenecksProp["items"], "robot-capacity bottleneck item")
	for _, name := range []string{"id", "title", "blocks_count", "blocks"} {
		if bottleneckProps[name] == nil {
			t.Fatalf("robot-capacity bottleneck schema missing %q", name)
		}
	}
}

func requireRobotSchemaProperties(t *testing.T, schemas RobotSchemas, command string) map[string]interface{} {
	t.Helper()
	schema := schemas.Commands[command]
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s properties has unexpected type %T", command, schema["properties"])
	}
	return properties
}

func requireNestedSchemaProperties(t *testing.T, schema interface{}, name string) map[string]interface{} {
	t.Helper()
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		t.Fatalf("%s schema has unexpected type %T", name, schema)
	}
	properties, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s properties has unexpected type %T", name, schemaMap["properties"])
	}
	return properties
}

func TestModifierFlagValidation(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()

	tests := []struct {
		name         string
		args         []string
		wantMessages []string
	}{
		{
			name: "robot diff requires diff since",
			args: []string{"--robot-diff"},
			wantMessages: []string{
				"Error: --robot-diff requires --diff-since",
				"Try one of:",
				"`bv robot-diff HEAD~1 --json`",
				"`bv --robot-diff --diff-since HEAD~1 --format json`",
			},
		},
		{
			name: "robot search requires search query",
			args: []string{"robot-search", "--json"},
			wantMessages: []string{
				"Error: --robot-search requires --search",
				"Try one of:",
				"`bv robot-search \"login oauth\" --json`",
				"`bv --search \"login oauth\" --robot-search --format json`",
			},
		},
		{
			name: "graph format requires graph command",
			args: []string{"--graph-format", "mermaid"},
			wantMessages: []string{
				"Error: --graph-format requires --robot-graph",
				"Try: `bv robot-graph mermaid --json`.",
			},
		},
		{
			name: "robot drift requires check drift",
			args: []string{"--robot-drift"},
			wantMessages: []string{
				"Error: --robot-drift requires --check-drift",
				"Try: `bv --check-drift --robot-drift --format json`.",
			},
		},
		{
			name: "schema command requires robot schema",
			args: []string{"--schema-command", "robot-triage"},
			wantMessages: []string{
				"Error: --schema-command requires --robot-schema",
				"Try: `bv robot-schema triage --json`.",
			},
		},
		{
			name: "watch export requires export pages",
			args: []string{"--watch-export"},
			wantMessages: []string{
				"Error: --watch-export requires --export-pages",
				"Try: `bv --export-pages ./bv-pages --watch-export`.",
			},
		},
		{
			name: "history since requires history mode",
			args: []string{"--history-since", "30 days ago"},
			wantMessages: []string{
				"Error: --history-since requires one of --robot-history, --bead-history or --robot-causality",
				"Try: `bv robot-history --history-since \"30 days ago\" --json`.",
			},
		},
		{
			name: "capacity agents requires robot capacity",
			args: []string{"--agents", "3"},
			wantMessages: []string{
				"Error: --agents requires --robot-capacity",
				"Try: `bv robot-capacity --agents 3 --json`.",
			},
		},
		{
			// #211: the label filter points at --label for other commands.
			name: "robot by label requires robot priority",
			args: []string{"--robot-by-label", "backend"},
			wantMessages: []string{
				"Error: --robot-by-label requires --robot-priority",
				"Try one of:",
				"`bv robot-priority --robot-by-label backend --json`",
				"`bv robot-triage --label backend --json`",
			},
		},
		{
			// #211: the assignee hint must show the assignee flag, not the label flag.
			name: "robot by assignee requires robot priority",
			args: []string{"--robot-by-assignee", "alice"},
			wantMessages: []string{
				"Error: --robot-by-assignee requires --robot-priority",
				"Try: `bv robot-priority --robot-by-assignee alice --json`.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runCommandWithTimeout(t, tmpDir, exe, tt.args...)
			if err == nil {
				t.Fatalf("expected %v to fail, got success\nstdout:\n%s\nstderr:\n%s", tt.args, stdout, stderr)
			}

			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected ExitError for %v, got %T", tt.args, err)
			}
			if exitErr.ExitCode() != 1 {
				t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", exitErr.ExitCode(), stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("expected empty stdout for %v, got:\n%s", tt.args, stdout)
			}
			for _, want := range tt.wantMessages {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr missing %q\nfull stderr:\n%s", want, stderr)
				}
			}
		})
	}
}

// mustApplyRecipe runs the CLI's single recipe entry point (applyRecipe, which
// wires analyzer/triage metrics into recipe.Apply) and fails the test on error.
func mustApplyRecipe(t *testing.T, issues []model.Issue, r *recipe.Recipe) []model.Issue {
	t.Helper()
	got, err := applyRecipe(issues, r)
	if err != nil {
		t.Fatalf("applyRecipe: %v", err)
	}
	return got
}

// The per-filter and per-sort-field semantics are covered in pkg/recipe
// (apply_test.go); this proves the CLI path routes every FilterConfig field,
// the sort chain and view.max_items through that one engine.
func TestApplyRecipe_FiltersSortAndLimitThroughSharedEngine(t *testing.T) {
	// Own the relative-date clock instead of inheriting a packaging epoch.
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(now.Unix(), 10))
	issues := []model.Issue{
		{ID: "A", Title: "Root", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "B", Title: "Blocked by A", Status: model.StatusOpen, Priority: 0, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
			Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Title: "Login endpoint", Status: model.StatusClosed, Priority: 1, Labels: []string{"backend", "p0"},
			CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-72 * time.Hour)},
	}

	r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}}
	// Closed records are not ready work, even without an explicit status filter.
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "A")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Status: []string{"open"}, HasBlockers: ptrBool(true)}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "B")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Priority: []int{1, 2}, TitleContains: "login", Tags: []string{"BACKEND"}, IDPrefix: "C"}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "C")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{CreatedBefore: "1d", UpdatedBefore: "1d"}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "C")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{CreatedAfter: "1d", UpdatedAfter: "1d", ExcludeTags: []string{"P0"}}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "A", "B")

	// Sort chain and max_items go through the same call.
	r = &recipe.Recipe{Sort: recipe.SortConfig{Field: "priority", Secondary: &recipe.SortConfig{Field: "id"}}, View: recipe.ViewConfig{MaxItems: 2}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "B", "C")
	r = &recipe.Recipe{Sort: recipe.SortConfig{Field: "created"}} // dates default newest-first
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "A", "B", "C")

	// The caller's slice is never reordered or truncated.
	requireIssueIDs(t, issues, "A", "B", "C")

	// A nil recipe passes issues through untouched.
	if got, err := applyRecipe(issues, nil); err != nil || len(got) != len(issues) {
		t.Fatalf("applyRecipe(nil) = %d issues, %v", len(got), err)
	}

	// A malformed time filter is an error naming the field, never a silently skipped filter.
	r = &recipe.Recipe{Name: "bad", Filters: recipe.FilterConfig{UpdatedAfter: "whenever"}}
	if _, err := applyRecipe(issues, r); err == nil || !strings.Contains(err.Error(), "filters.updated_after") {
		t.Fatalf("applyRecipe(bad) error = %v, want filters.updated_after named", err)
	}
}

// Metric sorts need real scores: applyRecipe must run the analyzer for
// pagerank/betweenness and compute triage scores for triage, on the same
// issue set the robot handlers see.
func TestApplyRecipe_MetricSortsUseAnalyzerAndTriage(t *testing.T) {
	blocks := func(on string) []*model.Dependency {
		return []*model.Dependency{{DependsOnID: on, Type: model.DepBlocks}}
	}
	// root blocks mid blocks leaf; solo is independent; done is closed.
	issues := []model.Issue{
		{ID: "leaf", Title: "Leaf", Status: model.StatusOpen, Priority: 1, Dependencies: blocks("mid")},
		{ID: "solo", Title: "Solo", Status: model.StatusOpen, Priority: 0},
		{ID: "root", Title: "Root", Status: model.StatusOpen, Priority: 3},
		{ID: "mid", Title: "Mid", Status: model.StatusOpen, Priority: 2, Dependencies: blocks("root")},
		{ID: "done", Title: "Done", Status: model.StatusClosed, Priority: 0},
	}

	stats := analysis.NewAnalyzer(issues).AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	if stats.GetPageRankScore("root") <= stats.GetPageRankScore("leaf") {
		t.Fatalf("fixture has no PageRank gradient: root=%v leaf=%v", stats.GetPageRankScore("root"), stats.GetPageRankScore("leaf"))
	}

	r := &recipe.Recipe{
		Name:    "pagerank-desc",
		Filters: recipe.FilterConfig{Status: []string{"open"}},
		Sort:    recipe.SortConfig{Field: "pagerank", Direction: "desc", Secondary: &recipe.SortConfig{Field: "priority"}},
	}
	got := mustApplyRecipe(t, issues, r)
	if len(got) != 4 || got[0].ID != "root" {
		t.Fatalf("pagerank order = %v, want root first and the closed issue gone", issueIDs(got))
	}
	for i := 1; i < len(got); i++ {
		prev, cur := stats.GetPageRankScore(got[i-1].ID), stats.GetPageRankScore(got[i].ID)
		if prev < cur {
			t.Fatalf("not in descending PageRank order at %d: %v", i, issueIDs(got))
		}
		if prev == cur && got[i-1].Priority > got[i].Priority {
			t.Fatalf("secondary priority sort not applied on PageRank tie: %v", issueIDs(got))
		}
	}

	// mid is the only node on the root->leaf path, so it has the top betweenness;
	// the rest tie at 0 and fall through to natural ID order.
	r = &recipe.Recipe{Name: "bottleneck", Filters: recipe.FilterConfig{Status: []string{"open"}}, Sort: recipe.SortConfig{Field: "betweenness"}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "mid", "leaf", "root", "solo")

	// Triage order equals the analysis package's own triage ranking.
	open := issues[:4]
	var want []string
	for _, ts := range analysis.ComputeTriageScores(open) {
		want = append(want, ts.IssueID)
	}
	if len(want) != 4 {
		t.Fatalf("ComputeTriageScores returned %d scores for 4 open issues", len(want))
	}
	r = &recipe.Recipe{Name: "triage-desc", Sort: recipe.SortConfig{Field: "triage"}}
	requireIssueIDs(t, mustApplyRecipe(t, open, r), want...)
}

func issueIDs(issues []model.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

func TestScopeLoadedIssuesKeepsFullSourceMetricContext(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Status: model.StatusOpen, SourceRepo: "selected", Labels: []string{"focus"}},
		{ID: "z", Status: model.StatusOpen, SourceRepo: "selected", Labels: []string{"focus"}},
		{ID: "outside", Status: model.StatusOpen, SourceRepo: "other", Dependencies: []*model.Dependency{
			{IssueID: "outside", DependsOnID: "z", Type: model.DepBlocks},
		}},
	}
	r := &recipe.Recipe{Name: "impact", Sort: recipe.SortConfig{Field: "pagerank"}, View: recipe.ViewConfig{MaxItems: 1}}
	ctx := RobotContext{Issues: issues, Repo: "selected", LabelScope: "focus", Readiness: model.NewReadinessIndex(issues)}
	got, err := scopeLoadedIssues(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// With the hidden dependent dropped before ranking, a and z tie and the
	// ID tie-break incorrectly keeps a. Real full-source PageRank keeps z.
	requireIssueIDs(t, got.Issues, "z")
	if got.DataHash != analysis.ComputeDataHash(issues[:2]) || got.DataHashMatchesIssues {
		t.Fatalf("scope lost its pre-recipe hash: %+v", got)
	}
	if got.LabelContext == nil || got.LabelContext.Label != "focus" {
		t.Fatal("missing label context")
	}
	requireIssueIDs(t, ctx.Issues, "a", "z", "outside")
	// Reuse the prior dispatch context with a newly loaded source. Old
	// candidate membership and label health must not survive an empty scope.
	got.Issues = issues[2:]
	got.Readiness = model.NewReadinessIndex(got.Issues)
	got.DataHash = ""
	empty, err := scopeLoadedIssues(got, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Issues) != 0 || len(empty.CandidateIDs) != 0 || empty.LabelContext != nil {
		t.Fatalf("empty reload retained old scope: %+v", empty)
	}
}

func TestWorkspaceUIReloadPreservesScopeAndRefreshesAuthority(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, ".bv", "workspace.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(webPath string) {
		t.Helper()
		config := fmt.Sprintf("repos:\n  - path: api\n    prefix: api-\n  - path: %s\n    prefix: web-\n", webPath)
		if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeIssues := func(repo string, issues []model.Issue, corrupt bool) {
		t.Helper()
		path := filepath.Join(root, repo, ".beads", "issues.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		for _, issue := range issues {
			if issue.IssueType == "" {
				issue.IssueType = model.TypeTask
			}
			if err := json.NewEncoder(&data).Encode(issue); err != nil {
				t.Fatal(err)
			}
		}
		if corrupt {
			data.WriteString("{invalid\n")
		}
		if err := os.WriteFile(path, data.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	api := []model.Issue{
		{ID: "blocked", Title: "External prerequisite", Status: model.StatusOpen, Labels: []string{"focus"},
			Dependencies: []*model.Dependency{{IssueID: "blocked", DependsOnID: "web-gate", Type: model.DepBlocks}}},
		{ID: "ready", Title: "Resolved prerequisites", Status: model.StatusOpen, Labels: []string{"focus"},
			Dependencies: []*model.Dependency{
				{IssueID: "ready", DependsOnID: "gone", Type: model.DepBlocks},
				{IssueID: "ready", DependsOnID: "context", Type: model.DepBlocks},
			}},
		{ID: "context", Title: "Label neighbor", Status: model.StatusClosed},
		{ID: "outside", Title: "Outside label scope", Status: model.StatusOpen},
		{ID: "gone", Title: "Deleted prerequisite", Status: model.StatusTombstone},
	}
	web := []model.Issue{{ID: "gate", Title: "Hidden repository prerequisite", Status: model.StatusOpen}}
	writeConfig("web")
	writeIssues("api", api, false)
	writeIssues("web", web, false)
	// Process-wide selectors must not override an explicitly configured source.
	t.Setenv("BEADS_DIR", filepath.Join(root, "unrelated", ".beads"))
	t.Setenv("BEADS_DB", filepath.Join(root, "unrelated", "beads.db"))
	reload := func() ui.SourceReloadResult {
		t.Helper()
		got, err := reloadWorkspaceForUI(context.Background(), configPath, "focus", "api")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := reload()
	if len(got.Issues) != 3 || !reflect.DeepEqual(got.CandidateIDs, map[string]bool{"api-blocked": true, "api-ready": true}) {
		t.Fatalf("CLI repo/label scope or label context changed on reload: %+v", got)
	}
	for _, issue := range got.Issues {
		if issue.SourceRepo != "api" || issue.Origin == nil || issue.Origin.LocalID != strings.TrimPrefix(issue.ID, "api-") {
			t.Fatalf("reload lost source-local identity: %+v", issue)
		}
	}
	if got.Authority.Ready("api-blocked", time.Now()) || !got.Authority.Ready("api-ready", time.Now()) {
		t.Fatal("filtered reload lost hidden cross-repository blockers or resolved tombstones")
	}
	if got.Workspace == nil || !got.Workspace.Enabled || got.Workspace.RepoCount != 2 || got.Workspace.FailedCount != 0 || got.Workspace.TotalIssues != 5 {
		t.Fatalf("workspace summary differs from full source: %+v", got.Workspace)
	}
	web[0].Status = model.StatusClosed
	writeIssues("web", web, false)
	got = reload()
	if !got.Authority.Ready("api-blocked", time.Now()) {
		t.Fatal("refresh retained the old state of a hidden repository prerequisite")
	}
	api[1].Labels = nil
	api = append(api, model.Issue{ID: "new", Title: "New selected issue", Status: model.StatusOpen, Labels: []string{"focus"}})
	writeIssues("api", api, false)
	got = reload()
	if !reflect.DeepEqual(got.CandidateIDs, map[string]bool{"api-blocked": true, "api-new": true}) {
		t.Fatalf("reload retained stale label membership: %+v", got.CandidateIDs)
	}
	writeConfig("missing")
	got = reload()
	if got.Workspace.FailedCount != 1 || len(got.Warnings) == 0 || got.Authority.DependencyState("api-blocked") != model.DependenciesUnknown {
		t.Fatalf("partial reload concealed missing authority: %+v", got)
	}
	for _, issue := range got.Issues {
		if issue.Origin == nil || issue.Origin.ReadOnlyReason != "source authority is incomplete or stale" {
			t.Fatalf("partial workspace retained writable source authority: %+v", issue.Origin)
		}
	}
	writeConfig("web")
	writeIssues("api", api, true)
	got = reload()
	if len(got.Warnings) == 0 || got.Workspace.FailedCount != 0 || len(got.Issues) == 0 {
		t.Fatalf("parse loss did not preserve useful data and diagnostics: %+v", got)
	}
	for _, issue := range got.Issues {
		if issue.Origin.ReadOnlyReason != "source authority is incomplete or stale" {
			t.Fatal("parse loss retained writable source authority")
		}
	}
	writeIssues("api", api, false)
	empty, err := reloadWorkspaceForUI(context.Background(), configPath, "no-such-label", "api")
	if err != nil || len(empty.Issues) != 0 || empty.CandidateIDs == nil || len(empty.CandidateIDs) != 0 || empty.Authority == nil {
		t.Fatalf("successful empty selection was confused with a failed source: %+v, %v", empty, err)
	}
	if err := os.WriteFile(configPath, []byte("repos:\n  - path: nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed, err := reloadWorkspaceForUI(context.Background(), configPath, "focus", "api")
	if err == nil || failed.Issues != nil || failed.Authority != nil || failed.Workspace != nil {
		t.Fatalf("failed workspace reload claimed to be a fresh empty source: %+v, %v", failed, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	failed, err = reloadWorkspaceForUI(cancelled, configPath, "focus", "api")
	if err != context.Canceled || failed.Authority != nil || failed.Issues != nil {
		t.Fatalf("cancelled reload returned source authority: %+v, %v", failed, err)
	}
}

func TestFormatCycle(t *testing.T) {
	requireString(t, formatCycle(nil), "(empty)")
	c := []string{"X", "Y", "Z"}
	want := "X → Y → Z → X"
	requireString(t, formatCycle(c), want)
}

func ptrBool(b bool) *bool { return &b }

func requireIssueIDs(t *testing.T, issues []model.Issue, want ...string) {
	t.Helper()
	if len(issues) != len(want) {
		t.Fatalf("issue count = %d, want %d; issues=%#v", len(issues), len(want), issues)
	}
	for i := range want {
		if strings.Compare(issues[i].ID, want[i]) != 0 {
			t.Fatalf("issue[%d].ID = %q, want %q; issues=%#v", i, issues[i].ID, want[i], issues)
		}
	}
}

func requireString(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func requireContainsString(t *testing.T, got []string, want string) {
	t.Helper()
	for _, value := range got {
		if strings.Compare(value, want) == 0 {
			return
		}
	}
	t.Fatalf("%#v does not contain %q", got, want)
}

func requireArgs(t *testing.T, got, want []string) {
	t.Helper()
	gotJoined := strings.Join(got, "\x00")
	wantJoined := strings.Join(want, "\x00")
	if strings.Compare(gotJoined, wantJoined) != 0 {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func writeTestBeadsFixture(t *testing.T, dir string) {
	t.Helper()

	beads := `{"id":"A","title":"Root","status":"open","priority":1,"issue_type":"task","labels":["backend"]}
{"id":"B","title":"Blocked","status":"blocked","priority":2,"issue_type":"task","labels":["backend"],"dependencies":[{"depends_on_id":"A","type":"blocks"}]}
{"id":"C","title":"UI","status":"open","priority":2,"issue_type":"task","labels":["frontend"]}`

	if err := os.WriteFile(filepath.Join(dir, ".beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads dir: %v", err)
	}
}

func writeIssueJSONL(t *testing.T, path, id string) {
	t.Helper()
	content := `{"id":"` + id + `","title":"` + id + `","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write issue JSONL: %v", err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}

func TestIssuesFingerprintDetectsContentChangesOrderIndependently(t *testing.T) {
	t1 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	base := []model.Issue{
		{ID: "A", Status: model.StatusOpen, UpdatedAt: t1},
		{ID: "B", Status: model.StatusInProgress, UpdatedAt: t1},
	}
	// Reordering the same content must not change the fingerprint (#159).
	reordered := []model.Issue{base[1], base[0]}
	if issuesFingerprint(base) != issuesFingerprint(reordered) {
		t.Fatalf("fingerprint must be order-independent")
	}
	// A status change must change the fingerprint.
	statusChanged := []model.Issue{
		{ID: "A", Status: model.StatusClosed, UpdatedAt: t1},
		{ID: "B", Status: model.StatusInProgress, UpdatedAt: t1},
	}
	if issuesFingerprint(base) == issuesFingerprint(statusChanged) {
		t.Fatalf("fingerprint must change when an issue's status changes")
	}
	// An updated_at change must change the fingerprint.
	timeChanged := []model.Issue{
		{ID: "A", Status: model.StatusOpen, UpdatedAt: t2},
		{ID: "B", Status: model.StatusInProgress, UpdatedAt: t1},
	}
	if issuesFingerprint(base) == issuesFingerprint(timeChanged) {
		t.Fatalf("fingerprint must change when an issue's updated_at changes")
	}
	// A title change with NO updated_at bump must still change the fingerprint —
	// the previous id/status/updated_at-only fingerprint missed this (#159).
	titleChanged := []model.Issue{
		{ID: "A", Title: "renamed", Status: model.StatusOpen, UpdatedAt: t1},
		{ID: "B", Status: model.StatusInProgress, UpdatedAt: t1},
	}
	if issuesFingerprint(base) == issuesFingerprint(titleChanged) {
		t.Fatalf("fingerprint must change when a title changes without an updated_at bump")
	}
	// A dependency change with no updated_at bump must also be detected.
	depChanged := []model.Issue{
		{ID: "A", Status: model.StatusOpen, UpdatedAt: t1,
			Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusInProgress, UpdatedAt: t1},
	}
	if issuesFingerprint(base) == issuesFingerprint(depChanged) {
		t.Fatalf("fingerprint must change when a dependency changes without an updated_at bump")
	}
	// Metadata can move an identical issue set to another live tracker, or
	// revoke writable authority, without changing serialized issue content.
	withOrigin := []model.Issue{{ID: "A", Status: model.StatusOpen, Origin: &model.IssueOrigin{
		LocalID: "local-A", Database: "/tracker/first.db", Tracker: "br", SupportsClaim: true,
	}}}
	copyOrigin := withOrigin[0].Clone()
	if issuesFingerprint(withOrigin) != issuesFingerprint([]model.Issue{copyOrigin}) {
		t.Fatal("equivalent origin values must not depend on pointer identity")
	}
	copyOrigin.Origin.Database = "/tracker/second.db"
	if issuesFingerprint(withOrigin) == issuesFingerprint([]model.Issue{copyOrigin}) {
		t.Fatal("fingerprint must change when identical issues acquire a different tracker route")
	}
	copyOrigin = withOrigin[0].Clone()
	copyOrigin.Origin.ReadOnlyReason = "source authority is incomplete or stale"
	if issuesFingerprint(withOrigin) == issuesFingerprint([]model.Issue{copyOrigin}) {
		t.Fatal("fingerprint must change when a source becomes read-only")
	}
	copyOrigin = withOrigin[0].Clone()
	copyOrigin.Origin.SupportsClaim = false
	if issuesFingerprint(withOrigin) == issuesFingerprint([]model.Issue{copyOrigin}) {
		t.Fatal("fingerprint must change when installed tracker claim support changes")
	}
	copyOrigin.Origin = nil
	if issuesFingerprint(withOrigin) == issuesFingerprint([]model.Issue{copyOrigin}) {
		t.Fatal("fingerprint must change when a live tracker route disappears")
	}
}

func TestRobotRegistry_TotalContract(t *testing.T) {
	configured := configuredPrimaryRobotFlagNames()
	primary := primaryRobotFlagNames()
	docs := robotCommandDocs()
	schemas := generateRobotSchemas()
	ttyCanonical := agents.CanonicalRobotFlagNames()

	if !reflect.DeepEqual(configured, ttyCanonical) {
		t.Fatalf("agents.CanonicalRobotFlagNames() drifted from RobotRegistry.PrimaryRobotFlagNames():\ngot  %v\nwant %v", ttyCanonical, configured)
	}

	for name := range primary {
		if _, ok := docs[name]; !ok {
			t.Errorf("primary robot command %q missing from robotCommandDocs()", name)
		}
		if schemas.Commands[name] == nil {
			t.Errorf("primary robot command %q missing from RobotSchemas.Commands", name)
		}
	}
	for name := range docs {
		if !primary[name] {
			t.Errorf("documented robot command %q missing from primaryRobotFlagNames()", name)
		}
		if schemas.Commands[name] == nil {
			t.Errorf("documented robot command %q missing from RobotSchemas.Commands", name)
		}
	}
	for _, mustHave := range []string{
		"robot-triage",
		"robot-plan",
		"robot-priority",
		"robot-insights",
		"robot-alerts",
		"robot-drift",
		"robot-capacity",
		"bead-history",
		"robot-explain-correlation",
		"robot-confirm-correlation",
		"robot-reject-correlation",
		"robot-correlation-stats",
	} {
		if !primary[mustHave] {
			t.Errorf("must-have command %q missing from primaryRobotFlagNames()", mustHave)
		}
		if _, ok := docs[mustHave]; !ok {
			t.Errorf("must-have command %q missing from robotCommandDocs()", mustHave)
		}
		if schemas.Commands[mustHave] == nil {
			t.Errorf("must-have command %q missing from RobotSchemas.Commands", mustHave)
		}
	}

	// Live round-trip CLI schema conformance across all 42 registered primary
	// robot commands against a seeded git + beads + sprint + baseline workspace.
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester",
			"GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester",
			"GIT_COMMITTER_EMAIL=tester@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	issuesJSONL := strings.Join([]string{
		`{"id":"bv-1","title":"Auth login pipeline","description":"Implement OAuth2 login flow","status":"open","priority":1,"issue_type":"task","labels":["auth"],"estimated_minutes":60,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"bv-2","title":"Session refresh handler","description":"Depends on bv-1","status":"open","priority":2,"issue_type":"task","labels":["auth"],"estimated_minutes":45,"dependencies":[{"issue_id":"bv-2","depends_on_id":"bv-1","type":"blocks"}],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"bv-3","title":"Initial bootstrap","status":"closed","priority":2,"issue_type":"task","labels":["infra"],"estimated_minutes":30,"created_at":"2025-12-28T00:00:00Z","updated_at":"2025-12-29T00:00:00Z","closed_at":"2025-12-29T00:00:00Z"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(issuesJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	sprintJSONL := `{"id":"sprint-1","name":"Sprint 1","start_date":"2026-01-01T00:00:00Z","end_date":"2026-01-08T00:00:00Z","bead_ids":["bv-1","bv-2"]}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "sprints.jsonl"), []byte(sprintJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "app.go"), []byte("package src\nfunc App() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "chore: initial repo seed")

	if err := os.WriteFile(filepath.Join(srcDir, "app.go"), []byte("package src\nfunc App() { Auth() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "lib.go"), []byte("package src\nfunc Auth() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "feat(auth): implement bv-1 OAuth login")
	headSHA := runGit("rev-parse", "HEAD")

	t.Setenv("SOURCE_DATE_EPOCH", "1767312000") // 2026-01-02T00:00:00Z

	if out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--save-baseline", "contract-baseline"); err != nil {
		t.Fatalf("--save-baseline failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	schemaStdout, schemaStderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-schema")
	if err != nil {
		t.Fatalf("--robot-schema failed: %v\nstdout:\n%s\nstderr:\n%s", err, schemaStdout, schemaStderr)
	}
	var liveSchemas struct {
		Envelope map[string]any            `json:"envelope"`
		Commands map[string]map[string]any `json:"commands"`
	}
	if err := json.Unmarshal([]byte(schemaStdout), &liveSchemas); err != nil {
		t.Fatalf("unmarshal live --robot-schema failed: %v", err)
	}

	invocations := map[string][]string{
		"robot-help":                {"--robot-help", "--format", "json"},
		"robot-capabilities":        {"--robot-capabilities"},
		"robot-docs":                {"--robot-docs", "guide"},
		"robot-schema":              {"--robot-schema"},
		"robot-recipes":             {"--robot-recipes"},
		"robot-metrics":             {"--robot-metrics"},
		"robot-plan":                {"--robot-plan"},
		"robot-priority":            {"--robot-priority"},
		"robot-alerts":              {"--robot-alerts"},
		"robot-drift":               {"--robot-drift", "--check-drift"},
		"robot-suggest":             {"--robot-suggest"},
		"robot-burndown":            {"--robot-burndown", "sprint-1"},
		"robot-forecast":            {"--robot-forecast", "all"},
		"robot-sprint-list":         {"--robot-sprint-list"},
		"robot-graph":               {"--robot-graph"},
		"robot-search":              {"--robot-search", "--search", "auth"},
		"robot-diff":                {"--robot-diff", "--diff-since", "HEAD~1"},
		"robot-insights":            {"--robot-insights"},
		"robot-next":                {"--robot-next"},
		"robot-triage":              {"--robot-triage"},
		"robot-triage-by-track":     {"--robot-triage-by-track"},
		"robot-triage-by-label":     {"--robot-triage-by-label"},
		"robot-history":             {"--robot-history"},
		"bead-history":              {"--bead-history", "bv-1"},
		"robot-correlation-stats":   {"--robot-correlation-stats"},
		"robot-explain-correlation": {"--robot-explain-correlation", headSHA + ":bv-1"},
		"robot-confirm-correlation": {"--robot-confirm-correlation", headSHA + ":bv-1"},
		"robot-reject-correlation":  {"--robot-reject-correlation", headSHA + ":bv-1"},
		"robot-label-health":        {"--robot-label-health"},
		"robot-label-flow":          {"--robot-label-flow"},
		"robot-label-attention":     {"--robot-label-attention"},
		"robot-orphans":             {"--robot-orphans"},
		"robot-file-beads":          {"--robot-file-beads", "src/app.go"},
		"robot-file-hotspots":       {"--robot-file-hotspots"},
		"robot-impact":              {"--robot-impact", "src/app.go"},
		"robot-file-relations":      {"--robot-file-relations", "src/app.go"},
		"robot-related":             {"--robot-related", "bv-1"},
		"robot-blocker-chain":       {"--robot-blocker-chain", "bv-2"},
		"robot-whatif":              {"--robot-whatif", "bv-1"},
		"robot-impact-network":      {"--robot-impact-network", "bv-1"},
		"robot-causality":           {"--robot-causality", "bv-1"},
		"robot-sprint-show":         {"--robot-sprint-show", "sprint-1"},
		"robot-capacity":            {"--robot-capacity"},
	}

	sortedNames := append([]string(nil), configured...)
	sort.Strings(sortedNames)
	for _, cmdName := range sortedNames {
		args, ok := invocations[cmdName]
		if !ok {
			t.Fatalf("TestRobotRegistry_TotalContract missing live CLI invocation for registered command %q", cmdName)
		}
		cmdSchema := liveSchemas.Commands[cmdName]
		if cmdSchema == nil {
			t.Fatalf("live --robot-schema missing command %q", cmdName)
		}
		out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, args...)
		if err != nil {
			t.Fatalf("live invocation %v (%s) failed: %v\nstdout:\n%s\nstderr:\n%s", args, cmdName, err, out, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("live invocation %v (%s) did not emit valid JSON object: %v\nstdout:\n%s", args, cmdName, err, out)
		}
		validateJSONObjectAgainstSchema(t, cmdName, payload, cmdSchema, cmdSchema)
	}
}

func validateJSONObjectAgainstSchema(t *testing.T, path string, obj map[string]any, schema map[string]any, rootSchema map[string]any) {
	t.Helper()
	if reqSlice, ok := schema["required"].([]any); ok {
		for _, r := range reqSlice {
			reqKey, _ := r.(string)
			if _, exists := obj[reqKey]; !exists {
				t.Errorf("%s: missing required property %q in live output (keys=%v)", path, reqKey, sortedMapKeys(obj))
			}
		}
	}
	if oneOfSlice, ok := schema["oneOf"].([]any); ok && len(oneOfSlice) > 0 {
		matchedBranch := false
		for _, rawBranch := range oneOfSlice {
			branch, _ := rawBranch.(map[string]any)
			reqSlice, _ := branch["required"].([]any)
			allPresent := true
			for _, r := range reqSlice {
				reqKey, _ := r.(string)
				if _, exists := obj[reqKey]; !exists {
					allPresent = false
					break
				}
			}
			if allPresent {
				matchedBranch = true
				break
			}
		}
		if !matchedBranch {
			t.Errorf("%s: live output satisfied none of oneOf branches (keys=%v)", path, sortedMapKeys(obj))
		}
	}
	props, _ := schema["properties"].(map[string]any)
	if addl, ok := schema["additionalProperties"].(bool); ok && !addl && props != nil {
		for k := range obj {
			if _, declared := props[k]; !declared {
				t.Errorf("%s: undeclared property %q when additionalProperties=false", path, k)
			}
		}
	}
	for k, val := range obj {
		propSchema, ok := props[k].(map[string]any)
		if !ok {
			continue
		}
		validateJSONValueAgainstSchema(t, path+"."+k, val, propSchema, rootSchema)
	}
}

func validateJSONValueAgainstSchema(t *testing.T, path string, val any, schema map[string]any, rootSchema map[string]any) {
	t.Helper()
	if ref, ok := schema["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
		defName := strings.TrimPrefix(ref, "#/$defs/")
		if defs, ok := rootSchema["$defs"].(map[string]any); ok {
			if target, ok := defs[defName].(map[string]any); ok {
				schema = target
			}
		}
	}
	if constVal, hasConst := schema["const"]; hasConst {
		if !reflect.DeepEqual(val, constVal) {
			t.Errorf("%s: expected const %v, got %v", path, constVal, val)
		}
	}
	if enumSlice, ok := schema["enum"].([]any); ok && val != nil {
		matched := false
		for _, item := range enumSlice {
			if reflect.DeepEqual(val, item) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s: value %v not in enum %v", path, val, enumSlice)
		}
	}

	var allowedTypes []string
	switch rawType := schema["type"].(type) {
	case string:
		allowedTypes = []string{rawType}
	case []any:
		for _, item := range rawType {
			if s, ok := item.(string); ok {
				allowedTypes = append(allowedTypes, s)
			}
		}
	}
	if len(allowedTypes) > 0 {
		actualType := jsonSchemaValueType(val)
		typeOK := false
		for _, want := range allowedTypes {
			if want == actualType || (want == "number" && actualType == "integer") {
				typeOK = true
				break
			}
		}
		if !typeOK {
			t.Errorf("%s: expected JSON type %v, got %s (%#v)", path, allowedTypes, actualType, val)
			return
		}
	}

	switch typed := val.(type) {
	case map[string]any:
		validateJSONObjectAgainstSchema(t, path, typed, schema, rootSchema)
	case []any:
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for idx, elem := range typed {
				validateJSONValueAgainstSchema(t, fmt.Sprintf("%s[%d]", path, idx), elem, itemSchema, rootSchema)
			}
		}
	}
}

func jsonSchemaValueType(v any) string {
	if v == nil {
		return "null"
	}
	switch n := v.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if float64(int64(n)) == n {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestRobotDriftRevivesIssueLevelAlertsAndTriageMaxResults(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	initialJSONL := `{"id":"bv-1","title":"Root authentication refactor","description":"Refactor OAuth2 token validation across handlers","status":"open","priority":3,"issue_type":"task","created_at":"2025-11-01T00:00:00Z","updated_at":"2025-11-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(initialJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--save-baseline", "initial"); err != nil {
		t.Fatalf("--save-baseline failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	evolvedJSONL := strings.Join([]string{
		`{"id":"bv-1","title":"Root authentication refactor","description":"Refactor OAuth2 token validation across handlers","status":"open","priority":4,"issue_type":"task","created_at":"2025-10-01T00:00:00Z","updated_at":"2025-10-01T00:00:00Z"}`,
		`{"id":"bv-2","title":"Root authentication refactor","description":"Refactor OAuth2 token validation across handlers","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
		`{"id":"bv-3","title":"Dependent A","status":"open","priority":0,"issue_type":"task","dependencies":[{"issue_id":"bv-3","depends_on_id":"bv-1","type":"blocks"}],"created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
		`{"id":"bv-4","title":"Dependent B","status":"open","priority":0,"issue_type":"task","dependencies":[{"issue_id":"bv-4","depends_on_id":"bv-1","type":"blocks"}],"created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
		`{"id":"bv-5","title":"Dependent C","status":"open","priority":0,"issue_type":"task","dependencies":[{"issue_id":"bv-5","depends_on_id":"bv-1","type":"blocks"}],"created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
		`{"id":"bv-6","title":"Independent Ready 1","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
		`{"id":"bv-7","title":"Independent Ready 2","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-10T00:00:00Z","updated_at":"2026-01-10T00:00:00Z"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(evolvedJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	driftOut, driftStderr, driftErr := runCommandWithTimeout(t, tmpDir, exe, "--robot-drift", "--check-drift")
	if driftErr == nil {
		t.Fatalf("expected non-zero drift exit code when warnings/criticals fire; stdout:\n%s", driftOut)
	}
	var driftPayload map[string]any
	if err := json.Unmarshal([]byte(driftOut), &driftPayload); err != nil {
		t.Fatalf("--robot-drift JSON decode failed: %v\nstdout:\n%s\nstderr:\n%s", err, driftOut, driftStderr)
	}
	for _, field := range []string{"generated_at", "data_hash", "has_drift", "exit_code", "summary", "alerts"} {
		if driftPayload[field] == nil {
			t.Fatalf("--robot-drift missing field %q: %#v", field, driftPayload)
		}
	}
	alertsSlice, _ := driftPayload["alerts"].([]any)
	foundIssueLevelAlert := false
	for _, raw := range alertsSlice {
		m, _ := raw.(map[string]any)
		typ, _ := m["type"].(string)
		if typ == "potential_duplicate" || typ == "high_impact_unblock" || typ == "stale_issue" || typ == "priority_mismatch" {
			foundIssueLevelAlert = true
			break
		}
	}
	if !foundIssueLevelAlert {
		t.Fatalf("expected revived issue-level alert in --robot-drift output, got alerts=%#v", alertsSlice)
	}

	triageOut, triageStderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-triage", "--robot-max-results", "2")
	if err != nil {
		t.Fatalf("--robot-triage --robot-max-results 2 failed: %v\nstdout:\n%s\nstderr:\n%s", err, triageOut, triageStderr)
	}
	var triagePayload struct {
		Triage struct {
			QuickRef struct {
				TopPicks []any `json:"top_picks"`
			} `json:"quick_ref"`
			Recommendations []any `json:"recommendations"`
		} `json:"triage"`
	}
	if err := json.Unmarshal([]byte(triageOut), &triagePayload); err != nil {
		t.Fatalf("unmarshaling triage output: %v", err)
	}
	if len(triagePayload.Triage.Recommendations) > 2 || len(triagePayload.Triage.QuickRef.TopPicks) > 2 {
		t.Fatalf("--robot-max-results 2 not honored: recs=%d top_picks=%d",
			len(triagePayload.Triage.Recommendations), len(triagePayload.Triage.QuickRef.TopPicks))
	}
}

func TestAgentsCLIMutatesUnderBVRobotAndSupportsDryRun(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)

	runRobotEnv := func(args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(exe, args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BV_ROBOT=1", "BV_NO_BROWSER=1", "BV_TEST_MODE=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	dryOut, dryErrStr, err := runRobotEnv("--agents-add", "--agents-dry-run")
	if err != nil {
		t.Fatalf("BV_ROBOT=1 --agents-add --agents-dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, dryOut, dryErrStr)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("--agents-dry-run unexpectedly created AGENTS.md: %v", statErr)
	}

	addOut, addErrStr, err := runRobotEnv("--agents-add")
	if err != nil {
		t.Fatalf("BV_ROBOT=1 --agents-add failed: %v\nstdout:\n%s\nstderr:\n%s", err, addOut, addErrStr)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md was not written under BV_ROBOT=1 --agents-add: %v", err)
	}
	if !agents.ContainsBlurb(string(content)) {
		t.Fatalf("AGENTS.md missing agent blurb after --agents-add:\n%s", string(content))
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(addOut), &receipt); err != nil {
		t.Fatalf("expected JSON receipt from BV_ROBOT=1 --agents-add, got: %v\n%s", err, addOut)
	}
	if receipt["action"] != "add" || receipt["modified"] != true {
		t.Fatalf("unexpected --agents-add receipt: %#v", receipt)
	}

	remOut, remErrStr, err := runRobotEnv("--agents-remove")
	if err != nil {
		t.Fatalf("BV_ROBOT=1 --agents-remove failed: %v\nstdout:\n%s\nstderr:\n%s", err, remOut, remErrStr)
	}
	afterRemove, err := os.ReadFile(filepath.Join(tmpDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if agents.ContainsBlurb(string(afterRemove)) {
		t.Fatalf("AGENTS.md still contains blurb after BV_ROBOT=1 --agents-remove")
	}
}

func TestBeadHistoryAndCorrelationActionEnvelopes(t *testing.T) {
	exe := buildTestBinary(t)
	repo := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester",
			"GIT_AUTHOR_EMAIL=tester@example.invalid",
			"GIT_COMMITTER_NAME=Tester",
			"GIT_COMMITTER_EMAIL=tester@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	gitRun("init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	issueLine := `{"id":"bv-h1","title":"History envelope test","status":"in_progress","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(issueLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "handler.go"), []byte("package sample\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".beads/issues.jsonl", "handler.go")
	gitRun("commit", "-m", "Fixes bv-h1: wire envelope")
	sha := gitRun("rev-parse", "HEAD")

	// 1. --bead-history emits RobotEnvelope + history
	bhOut, bhErr, err := runCommandWithTimeout(t, repo, exe, "--bead-history", "bv-h1")
	if err != nil {
		t.Fatalf("--bead-history failed: %v\nstdout:\n%s\nstderr:\n%s", err, bhOut, bhErr)
	}
	var bhPayload map[string]any
	if err := json.Unmarshal([]byte(bhOut), &bhPayload); err != nil {
		t.Fatalf("--bead-history JSON decode failed: %v\n%s", err, bhOut)
	}
	for _, f := range []string{"generated_at", "data_hash", "histories", "stats", "commit_index"} {
		if bhPayload[f] == nil {
			t.Fatalf("--bead-history missing %q: %#v", f, bhPayload)
		}
	}

	// 2. --robot-explain-correlation, --robot-confirm-correlation, --robot-reject-correlation emit RobotEnvelope
	pairArg := sha[:8] + ":bv-h1"
	for _, flagName := range []string{"--robot-explain-correlation", "--robot-confirm-correlation", "--robot-reject-correlation"} {
		out, stderr, err := runCommandWithTimeout(t, repo, exe, flagName, pairArg)
		if err != nil {
			t.Fatalf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", flagName, err, out, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("%s JSON decode failed: %v\n%s", flagName, err, out)
		}
		for _, envKey := range []string{"generated_at", "data_hash", "version"} {
			if payload[envKey] == nil {
				t.Fatalf("%s output missing RobotEnvelope key %q: %#v", flagName, envKey, payload)
			}
		}
	}
}

func TestSubdirectoryProjectContextParity(t *testing.T) {
	exe := buildTestBinary(t)
	repo := t.TempDir()
	nestedDir := filepath.Join(repo, "cmd", "subpkg")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}

	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=SubdirTest",
			"GIT_AUTHOR_EMAIL=subdir@example.invalid",
			"GIT_COMMITTER_NAME=SubdirTest",
			"GIT_COMMITTER_EMAIL=subdir@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	gitRun("init", "-b", "main")
	nowStr := time.Now().UTC().Format(time.RFC3339)
	commit1JSONL := fmt.Sprintf(`{"id":"bv-sub-1","title":"Initial issue","status":"open","priority":2,"issue_type":"task","created_at":%q,"updated_at":%q}`+"\n", nowStr, nowStr)
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(commit1JSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".beads/issues.jsonl")
	gitRun("commit", "-m", "initial tracker snapshot")

	commit2JSONL := commit1JSONL + fmt.Sprintf(`{"id":"bv-sub-2","title":"Second issue","status":"open","priority":1,"issue_type":"task","created_at":%q,"updated_at":%q}`+"\n", nowStr, nowStr)
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(commit2JSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".beads/issues.jsonl")
	gitRun("commit", "-m", "second tracker snapshot")

	// Configure .bv/hooks.yaml and save baseline at repo root.
	hooksYAML := "hooks:\n  post-export:\n    - command: \"echo ok\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".bv", "hooks.yaml"), []byte(hooksYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, stderr, err := runCommandWithTimeout(t, repo, exe, "--save-baseline", "root baseline"); err != nil {
		t.Fatalf("--save-baseline failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
	}

	// 1. --as-of HEAD~1 from nestedDir must match repo root (returning 1 issue, not failing git show).
	rootAsOf, _, err := runCommandWithTimeout(t, repo, exe, "--as-of", "HEAD~1", "--robot-triage")
	if err != nil {
		t.Fatalf("root --as-of HEAD~1 failed: %v", err)
	}
	subAsOf, subStderr, err := runCommandWithTimeout(t, nestedDir, exe, "--as-of", "HEAD~1", "--robot-triage")
	if err != nil {
		t.Fatalf("nested --as-of HEAD~1 failed: %v\nstdout:\n%s\nstderr:\n%s", err, subAsOf, subStderr)
	}
	var rootTriage, subTriage map[string]any
	if err := json.Unmarshal([]byte(rootAsOf), &rootTriage); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(subAsOf), &subTriage); err != nil {
		t.Fatal(err)
	}
	if rootTriage["data_hash"] != subTriage["data_hash"] {
		t.Fatalf("subdirectory --as-of data_hash %v != root data_hash %v", subTriage["data_hash"], rootTriage["data_hash"])
	}

	// 2. --robot-drift --check-drift from nestedDir must locate <repo>/.bv/baseline.json cleanly.
	driftOut, driftErrStr, err := runCommandWithTimeout(t, nestedDir, exe, "--robot-drift", "--check-drift")
	if err != nil {
		t.Fatalf("nested --robot-drift --check-drift failed: %v\nstdout:\n%s\nstderr:\n%s", err, driftOut, driftErrStr)
	}

	// 3. --hooks-info from nestedDir must locate <repo>/.bv/hooks.yaml.
	hooksOut, hooksErrStr, err := runCommandWithTimeout(t, nestedDir, exe, "--hooks-info")
	if err != nil {
		t.Fatalf("nested --hooks-info failed: %v\nstdout:\n%s\nstderr:\n%s", err, hooksOut, hooksErrStr)
	}
	if !strings.Contains(hooksOut, "echo ok") {
		t.Fatalf("nested --hooks-info did not inspect root .bv/hooks.yaml:\n%s", hooksOut)
	}

	// 4. Standalone --workspace config outside .bv/ must resolve relative repo paths against config's parent dir.
	wsHolder := t.TempDir()
	wsConfigPath := filepath.Join(wsHolder, "standalone-ws.yaml")
	relRepo, err := filepath.Rel(wsHolder, repo)
	if err != nil {
		t.Fatal(err)
	}
	wsYAML := fmt.Sprintf("repos:\n  - name: mainrepo\n    path: %s\n    prefix: mr-\n", relRepo)
	if err := os.WriteFile(wsConfigPath, []byte(wsYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	wsOut, wsErrStr, err := runCommandWithTimeout(t, nestedDir, exe, "--workspace", wsConfigPath, "--robot-plan")
	if err != nil {
		t.Fatalf("standalone --workspace failed from nestedDir: %v\nstdout:\n%s\nstderr:\n%s", err, wsOut, wsErrStr)
	}
	if !strings.Contains(wsOut, "mr-bv-sub-1") {
		t.Fatalf("expected prefixed workspace issue mr-bv-sub-1 in output:\n%s", wsOut)
	}
}

func TestBriefOutputsRefuseSymlinkTargets(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()
	writeTestBeadsFixture(t, tmpDir)

	victim := filepath.Join(tmpDir, "victim.txt")
	if err := os.WriteFile(victim, []byte("DO NOT OVERWRITE"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkBrief := filepath.Join(tmpDir, "brief-link.md")
	if err := os.Symlink(victim, symlinkBrief); err != nil {
		t.Fatal(err)
	}

	if out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--priority-brief", symlinkBrief); err == nil {
		t.Fatalf("expected --priority-brief to refuse symlink target; stdout:\n%s\nstderr:\n%s", out, stderr)
	}
	victimBytes, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(victimBytes) != "DO NOT OVERWRITE" {
		t.Fatalf("symlink victim was modified: %q", string(victimBytes))
	}

	victimDir := filepath.Join(tmpDir, "victim-dir")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(tmpDir, "bundle-link")
	if err := os.Symlink(victimDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if out, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--agent-brief", symlinkDir); err == nil {
		t.Fatalf("expected --agent-brief to refuse symlink directory; stdout:\n%s\nstderr:\n%s", out, stderr)
	}
}

func TestRobotWhatIfAndWave6CLIWiring(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()

	// Initialize a tiny git repo so baseline/pages git_hash stamping is exercised against real git.
	for _, gitArgs := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", gitArgs, err, out)
		}
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	issuesContent := strings.Join([]string{
		`{"id":"root-1","title":"Unblock backend","status":"open","priority":1,"issue_type":"task","labels":["core"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`,
		`{"id":"back-1","title":"Backend API","status":"open","priority":1,"issue_type":"task","labels":["backend"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z","dependencies":[{"issue_id":"back-1","depends_on_id":"root-1","type":"blocks"}]}`,
		`{"id":"track-2","title":"Independent frontend","status":"open","priority":2,"issue_type":"task","labels":["frontend"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`,
		`{"id":"done-1","title":"Closed task","status":"closed","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","closed_at":"2026-10-02T00:00:00Z"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(issuesContent), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, gitArgs := range [][]string{
		{"add", ".beads/issues.jsonl"},
		{"commit", "-m", "seed issues"},
	} {
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", gitArgs, err, out)
		}
	}
	revOut, err := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	headSHA := strings.TrimSpace(string(revOut))

	// 1. --robot-whatif and `bv whatif <id> --json` intent alias
	stdout, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "whatif", "root-1", "--json")
	if err != nil {
		t.Fatalf("bv whatif root-1 --json failed: %v\nstderr: %s", err, stderr)
	}
	var whatIfResp struct {
		Result struct {
			IssueID string `json:"issue_id"`
			Delta   struct {
				DirectUnblocks    int      `json:"direct_unblocks"`
				UnblockedIssueIDs []string `json:"unblocked_issue_ids"`
			} `json:"delta"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &whatIfResp); err != nil {
		t.Fatalf("unmarshal robot-whatif: %v\nstdout: %s", err, stdout)
	}
	if whatIfResp.Result.IssueID != "root-1" || whatIfResp.Result.Delta.DirectUnblocks != 1 ||
		len(whatIfResp.Result.Delta.UnblockedIssueIDs) != 1 || whatIfResp.Result.Delta.UnblockedIssueIDs[0] != "back-1" {
		t.Fatalf("unexpected robot-whatif output: %s", stdout)
	}

	// Closed issue -> 0 unblocks; missing issue -> exit 1.
	closedOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-whatif", "done-1")
	if err != nil {
		t.Fatalf("--robot-whatif done-1 failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(closedOut, `"direct_unblocks": 0`) && !strings.Contains(closedOut, `"direct_unblocks":0`) {
		t.Fatalf("expected 0 direct_unblocks for closed issue, got: %s", closedOut)
	}
	if _, missingStderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-whatif", "missing-999"); err == nil || !strings.Contains(missingStderr, "Issue not found: missing-999") {
		t.Fatalf("expected exit 1 + Issue not found for missing issue; err=%v stderr=%s", err, missingStderr)
	}

	// 2. --robot-triage-by-track / --robot-triage-by-label --robot-max-results 1 --brief preserves full group recommendations across all actionable items
	briefOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-triage-by-track", "--robot-max-results", "1", "--brief")
	if err != nil {
		t.Fatalf("--robot-triage-by-track --brief failed: %v\nstderr: %s", err, stderr)
	}
	var briefPayload struct {
		Recommendations        []json.RawMessage `json:"recommendations"`
		RecommendationsByTrack []struct {
			TrackID         string            `json:"track_id"`
			Recommendations []json.RawMessage `json:"recommendations"`
		} `json:"recommendations_by_track"`
	}
	if err := json.Unmarshal([]byte(briefOut), &briefPayload); err != nil {
		t.Fatalf("unmarshal brief triage: %v\n%s", err, briefOut)
	}
	if len(briefPayload.Recommendations) != 1 || len(briefPayload.RecommendationsByTrack) != 1 || len(briefPayload.RecommendationsByTrack[0].Recommendations) != 2 {
		t.Fatalf("expected 1 top recommendation and 1 track containing both actionable items in brief output, got recs=%d tracks=%d\n%s",
			len(briefPayload.Recommendations), len(briefPayload.RecommendationsByTrack), briefOut)
	}

	briefLabelOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-triage-by-label", "--robot-max-results", "1", "--brief")
	if err != nil {
		t.Fatalf("--robot-triage-by-label --brief failed: %v\nstderr: %s", err, stderr)
	}
	var briefLabelPayload struct {
		Recommendations        []json.RawMessage `json:"recommendations"`
		RecommendationsByLabel []json.RawMessage `json:"recommendations_by_label"`
	}
	if err := json.Unmarshal([]byte(briefLabelOut), &briefLabelPayload); err != nil {
		t.Fatalf("unmarshal brief label triage: %v\n%s", err, briefLabelOut)
	}
	if len(briefLabelPayload.Recommendations) != 1 || len(briefLabelPayload.RecommendationsByLabel) != 2 {
		t.Fatalf("expected 1 top recommendation and 2 label groups in brief output, got recs=%d labels=%d\n%s",
			len(briefLabelPayload.Recommendations), len(briefLabelPayload.RecommendationsByLabel), briefLabelOut)
	}

	// 3. Feedback mutual exclusion + subdirectory invocation + structured format output
	subDir := filepath.Join(tmpDir, "nested", "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, fbStderr, err := runCommandWithTimeout(t, subDir, exe, "--feedback-accept", "root-1", "--feedback-ignore", "track-2"); err == nil || !strings.Contains(fbStderr, "cannot be used together") {
		t.Fatalf("expected mutual exclusion error for simultaneous --feedback-accept and --feedback-ignore; err=%v stderr=%s", err, fbStderr)
	}
	fbOut, stderr, err := runCommandWithTimeout(t, subDir, exe, "--feedback-accept", "root-1", "--format", "json")
	if err != nil {
		t.Fatalf("--feedback-accept from subdir failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(fbOut, `"action"`) || !strings.Contains(fbOut, `"accept"`) || !strings.Contains(fbOut, `"generated_at"`) {
		t.Fatalf("expected structured RobotEnvelope JSON from --feedback-accept --format json, got:\n%s", fbOut)
	}

	// 4. --save-baseline from subdir captures git commit SHA from projectDir
	if _, stderr, err := runCommandWithTimeout(t, subDir, exe, "--save-baseline", "from-subdir"); err != nil {
		t.Fatalf("--save-baseline from subdir failed: %v\nstderr: %s", err, stderr)
	}
	baselineBytes, err := os.ReadFile(filepath.Join(tmpDir, ".bv", "baseline.json"))
	if err != nil {
		t.Fatalf("read baseline.json: %v", err)
	}
	if !strings.Contains(string(baselineBytes), headSHA) {
		t.Fatalf("baseline.json missing git commit %s when saved from subdir:\n%s", headSHA, baselineBytes)
	}

	// 5. --export-pages --pages-title --pages-subtitle stamps git_hash, renders subtitle, and reports readiness-blocked count in README.md
	pagesOut := filepath.Join(tmpDir, "pages-out")
	if _, stderr, err := runCommandWithTimeout(t, subDir, exe, "--export-pages", pagesOut, "--pages-title", "Wave 6 Title", "--pages-subtitle", "Sprint <Alpha> & Co"); err != nil {
		t.Fatalf("--export-pages failed: %v\nstderr: %s", err, stderr)
	}
	indexHTML, err := os.ReadFile(filepath.Join(pagesOut, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexHTML), "Wave 6 Title") || !strings.Contains(string(indexHTML), "Sprint &lt;Alpha&gt; &amp; Co") {
		t.Fatalf("exported index.html missing title or escaped subtitle:\n%s", indexHTML[:min(2000, len(indexHTML))])
	}
	metaBytes, err := os.ReadFile(filepath.Join(pagesOut, "data", "meta.json"))
	if err != nil {
		t.Fatalf("read data/meta.json: %v", err)
	}
	if !strings.Contains(string(metaBytes), headSHA) {
		t.Fatalf("exported data/meta.json missing git_hash %s:\n%s", headSHA, metaBytes)
	}
	readmeBytes, err := os.ReadFile(filepath.Join(pagesOut, "README.md"))
	if err != nil {
		t.Fatalf("read exported README.md: %v", err)
	}
	if !strings.Contains(string(readmeBytes), "**1** blocked") {
		t.Fatalf("exported README.md should report 1 readiness-blocked issue (back-1), got:\n%s", readmeBytes)
	}

	// 6. --export-graph --label backend preserves 1-hop cross-label blocker edge (root-1 -> back-1)
	graphHTMLPath := filepath.Join(tmpDir, "backend-graph.html")
	graphStdout, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--export-graph", graphHTMLPath, "--label", "backend")
	if err != nil {
		t.Fatalf("--export-graph --label backend failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(graphStdout, "1 edges") {
		t.Fatalf("expected --export-graph --label backend to preserve 1-hop blocker edge, got stdout: %s", graphStdout)
	}

	// 7. Live --robot-schema & --robot-docs parity for --brief grouped triage and triage.alerts
	schemaOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-schema")
	if err != nil {
		t.Fatalf("--robot-schema failed: %v\nstderr: %s", err, stderr)
	}
	var liveSchemas struct {
		Envelope struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"envelope"`
		Commands map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
			OneOf      []struct {
				Required []string `json:"required"`
			} `json:"oneOf"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(schemaOut), &liveSchemas); err != nil {
		t.Fatalf("unmarshal --robot-schema: %v", err)
	}
	var triageProp struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(liveSchemas.Commands["robot-triage"].Properties["triage"], &triageProp); err != nil {
		t.Fatalf("unmarshal robot-triage.properties.triage: %v", err)
	}
	if _, ok := triageProp.Properties["alerts"]; !ok {
		t.Fatalf("robot-triage schema missing triage.properties.alerts")
	}

	briefPlainOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-triage", "--brief")
	if err != nil {
		t.Fatalf("--robot-triage --brief failed: %v\nstderr: %s", err, stderr)
	}
	for cmdName, rawPayload := range map[string]string{
		"robot-triage":          briefPlainOut,
		"robot-triage-by-track": briefOut,
		"robot-triage-by-label": briefLabelOut,
	} {
		cmdSchema, ok := liveSchemas.Commands[cmdName]
		if !ok {
			t.Fatalf("missing schema for %s", cmdName)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rawPayload), &obj); err != nil {
			t.Fatalf("unmarshal %s payload: %v", cmdName, err)
		}
		for k := range obj {
			_, inCmd := cmdSchema.Properties[k]
			_, inEnv := liveSchemas.Envelope.Properties[k]
			if !inCmd && !inEnv {
				t.Fatalf("%s --brief emitted undeclared top-level key %q (absent from both command properties and envelope.properties)", cmdName, k)
			}
		}
		if len(cmdSchema.OneOf) == 0 {
			t.Fatalf("%s schema missing oneOf branch for --brief", cmdName)
		}
		matchedBranch := false
		for _, branch := range cmdSchema.OneOf {
			allPresent := true
			for _, req := range branch.Required {
				if _, present := obj[req]; !present {
					allPresent = false
					break
				}
			}
			if allPresent {
				matchedBranch = true
				break
			}
		}
		if !matchedBranch {
			t.Fatalf("%s --brief payload failed all oneOf required branches: %+v", cmdName, cmdSchema.OneOf)
		}
	}

	docsOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-docs", "commands")
	if err != nil {
		t.Fatalf("--robot-docs commands failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{"--brief", "--robot-max-results"} {
		if strings.Count(docsOut, want) < 3 {
			t.Fatalf("expected --robot-docs commands to document %s on robot-triage and both grouped variants:\n%s", want, docsOut)
		}
	}

	// 8. Git-commit History staleness parity across --robot-triage, --priority-brief, --agent-brief, --export-pages, and --emit-script:
	// All open issues have JSONL updated_at 2026-10-01 (>14d old), so a fresh git commit referencing them must suppress false stale alerts everywhere.
	if err := os.WriteFile(filepath.Join(tmpDir, "work.txt"), []byte("active work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, gitArgs := range [][]string{
		{"add", "work.txt"},
		{"commit", "-m", "feat: fresh progress on root-1, back-1, and track-2"},
	} {
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", gitArgs, err, out)
		}
	}

	triageWithCommitOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--robot-triage")
	if err != nil {
		t.Fatalf("--robot-triage after commit failed: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(triageWithCommitOut, `"type": "stale"`) || strings.Contains(triageWithCommitOut, `"type":"stale"`) {
		t.Fatalf("expected git commit to suppress stale alert in --robot-triage:\n%s", triageWithCommitOut)
	}

	prioBriefFile := filepath.Join(tmpDir, "prio-brief.md")
	if _, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--priority-brief", prioBriefFile); err != nil {
		t.Fatalf("--priority-brief failed: %v\nstderr: %s", err, stderr)
	}
	prioBriefBytes, err := os.ReadFile(prioBriefFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prioBriefBytes), "**stale**") {
		t.Fatalf("--priority-brief emitted false stale alert despite fresh git commit:\n%s", prioBriefBytes)
	}

	agentBriefDir := filepath.Join(tmpDir, "agent-brief-out")
	if _, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--agent-brief", agentBriefDir); err != nil {
		t.Fatalf("--agent-brief failed: %v\nstderr: %s", err, stderr)
	}
	agentBriefMD, err := os.ReadFile(filepath.Join(agentBriefDir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agentBriefMD), "**stale**") {
		t.Fatalf("--agent-brief brief.md emitted false stale alert despite fresh git commit:\n%s", agentBriefMD)
	}
	agentTriageJSON, err := os.ReadFile(filepath.Join(agentBriefDir, "triage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agentTriageJSON), `"type": "stale"`) || !strings.Contains(string(agentTriageJSON), `"history_status": "ok"`) {
		t.Fatalf("--agent-brief triage.json missing git history staleness parity:\n%s", agentTriageJSON)
	}

	pagesOut2 := filepath.Join(tmpDir, "pages-out-no-tt")
	if _, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--export-pages", pagesOut2, "--pages-include-history=false"); err != nil {
		t.Fatalf("--export-pages (no history bundle) failed: %v\nstderr: %s", err, stderr)
	}
	pagesTriageJSON, err := os.ReadFile(filepath.Join(pagesOut2, "data", "triage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pagesTriageJSON), `"type": "stale"`) || !strings.Contains(string(pagesTriageJSON), `"history_status": "ok"`) {
		t.Fatalf("--export-pages data/triage.json emitted false stale alert despite fresh git commit:\n%s", pagesTriageJSON)
	}

	if scriptOut, stderr, err := runCommandWithTimeout(t, tmpDir, exe, "--emit-script"); err != nil || !strings.Contains(scriptOut, "root-1") {
		t.Fatalf("--emit-script after commit failed: err=%v stderr=%s out=%s", err, stderr, scriptOut)
	}
}

// TestMain isolates HOME and XDG_CONFIG_HOME for every cmd/bv test (and the
// bv binaries they exec, which inherit the environment) so nothing can write
// into the real ~/.config/bv (H3). The teardown fails the package if the real
// directory changed during the run.
func TestMain(m *testing.M) {
	realConfig, _ := os.UserConfigDir()
	before := fingerprintConfigDir(realConfig)

	tmp, err := os.MkdirTemp("", "bv-cmd-test-home-")
	if err != nil {
		panic("creating isolated HOME: " + err.Error())
	}
	os.Setenv("HOME", tmp)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	os.Setenv("BV_NO_BROWSER", "1")

	// RCH can execute as a different uid from the copied checkout's owner.
	// Trust only this checkout in the disposable HOME so nested builds retain
	// VCS stamping without depending on the worker's real Git configuration.
	repoDir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic("resolving test checkout: " + err.Error())
	}
	repoDir, err = filepath.EvalSymlinks(repoDir)
	if err != nil {
		panic("resolving test checkout symlinks: " + err.Error())
	}
	gitConfig := exec.Command("git", "config", "--file", filepath.Join(tmp, ".gitconfig"), "--add", "safe.directory", repoDir)
	if out, err := gitConfig.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("configuring isolated checkout trust: %v\n%s", err, out))
	}

	code := m.Run()

	if after := fingerprintConfigDir(realConfig); before != after {
		fmt.Fprintf(os.Stderr, "cmd/bv tests modified the real config dir %s:\nbefore: %s\nafter:  %s\n", realConfig, before, after)
		if code == 0 {
			code = 1
		}
	}
	os.RemoveAll(tmp)
	os.Exit(code)
}

func fingerprintConfigDir(configDir string) string {
	if configDir == "" {
		return ""
	}
	var out string
	_ = filepath.Walk(filepath.Join(configDir, "bv"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		out += fmt.Sprintf("%s:%d;", path, info.Size())
		return nil
	})
	return out
}
