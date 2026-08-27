package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"
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

func writeCurrentBRMetadata(t *testing.T, beadsDir string) {
	t.Helper()
	metadata := []byte(`{"database":"beads.db","jsonl_export":"issues.jsonl"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatalf("write current-br metadata: %v", err)
	}
}

func TestExplicitSingleRepoRepositoryRouteUnavailableReasons(t *testing.T) {
	tests := []struct {
		name     string
		dbFlag   string
		beadsDB  string
		beadsDir string
		wantText string
		wantGap  bool
	}{
		{name: "automatic discovery is routed", wantGap: false},
		{name: "db flag", dbFlag: "/other/.beads/issues.jsonl", wantText: "--db", wantGap: true},
		{name: "BEADS_DB", beadsDB: "/other/.beads/issues.jsonl", wantText: "BEADS_DB", wantGap: true},
		{name: "BEADS_DIR", beadsDir: "/other/.beads", wantText: "BEADS_DIR", wantGap: true},
		{name: "flag wins over inherited env", dbFlag: "/flag/.beads/issues.jsonl", beadsDB: "/env/.beads/issues.jsonl", beadsDir: "/env/.beads", wantText: "--db", wantGap: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(loader.BeadsDBEnvVar, tt.beadsDB)
			t.Setenv(loader.BeadsDirEnvVar, tt.beadsDir)
			reasons := explicitSingleRepoRepositoryRouteUnavailableReasons(tt.dbFlag)
			if (len(reasons) > 0) != tt.wantGap {
				t.Fatalf("reasons = %v, want gap %v", reasons, tt.wantGap)
			}
			if tt.wantText != "" && !strings.Contains(strings.Join(reasons, "; "), tt.wantText) {
				t.Fatalf("reasons = %v, want selector %q", reasons, tt.wantText)
			}
		})
	}
}

func TestSingleRepoWatchSiblingFilesTracksCanonicalAuthorityChanges(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	path := filepath.Join(beadsDir, "issues.jsonl")
	got := make(map[string]bool)
	for _, name := range singleRepoWatchSiblingFiles(path) {
		got[name] = true
	}
	for _, want := range []string{"beads.db", "beads.db-wal", "issues.jsonl", "beads.jsonl", "beads.base.jsonl"} {
		if !got[want] {
			t.Fatalf("watch siblings = %v, missing %q", got, want)
		}
	}

	t.Setenv(loader.BeadsDBEnvVar, path)
	if explicit := singleRepoWatchSiblingFiles(path); len(explicit) != 0 {
		t.Fatalf("explicit source broadened into sibling watch: %v", explicit)
	}
}

func requireReadableCPUProfile(t *testing.T, exe, profilePath string) {
	t.Helper()

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

func TestWorkspaceParseAuthorityGapReasonsAreDeterministicAndSuccessfulRepoOnly(t *testing.T) {
	results := []workspace.LoadResult{
		{RepoName: "zeta", ParseStats: loader.ParseStats{Valid: 2, Errors: 3}},
		{RepoName: "failed", ParseStats: loader.ParseStats{Errors: 9}, Error: errors.New("missing repository")},
		{RepoName: "clean", ParseStats: loader.ParseStats{Valid: 1}},
		{RepoName: "alpha", ParseStats: loader.ParseStats{Valid: 4, Errors: 1}},
	}

	got := workspaceParseAuthorityGapReasons(results)
	want := []string{`workspace JSONL parsing dropped records (repository=count): "alpha"=1, "zeta"=3`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace authority reasons = %v, want %v", got, want)
	}
	if got := workspaceParseAuthorityGapReasons([]workspace.LoadResult{{RepoName: "clean"}}); got != nil {
		t.Fatalf("clean workspace authority reasons = %v, want nil", got)
	}

	ambiguous := []workspace.LoadResult{
		{RepoName: "api=old, web", RepoPath: "repos/one", ParseStats: loader.ParseStats{Errors: 2}},
		{RepoName: "api=old, web", RepoPath: "repos/two", Error: errors.New("unavailable")},
	}
	if got := workspaceParseAuthorityGapReasons(ambiguous); !reflect.DeepEqual(got,
		[]string{`workspace JSONL parsing dropped records (repository=count): "api=old, web" (path "repos/one")=2`}) {
		t.Fatalf("delimited repository parse diagnostic is ambiguous: %v", got)
	}
	if got := workspaceFailedAuthorityGapReasons(ambiguous); !reflect.DeepEqual(got,
		[]string{`workspace failed to load 1 configured repository: "api=old, web" (path "repos/two")`}) {
		t.Fatalf("delimited repository failure diagnostic is ambiguous: %v", got)
	}
}

func TestWorkspaceWatchReloadAuthorityErrorRejectsIncompleteSnapshots(t *testing.T) {
	clean := []workspace.LoadResult{{RepoName: "api", ParseStats: loader.ParseStats{Valid: 2}}}
	if err := workspaceWatchReloadAuthorityError(clean); err != nil {
		t.Fatalf("clean workspace reload rejected: %v", err)
	}

	tests := []struct {
		name     string
		results  []workspace.LoadResult
		wantText string
	}{
		{
			name:     "repository failed",
			results:  []workspace.LoadResult{{RepoName: "api", Error: errors.New("unavailable")}},
			wantText: "failed to load 1 configured repository",
		},
		{
			name:     "records dropped",
			results:  []workspace.LoadResult{{RepoName: "api", ParseStats: loader.ParseStats{Valid: 2, Errors: 1}}},
			wantText: "JSONL parsing dropped records",
		},
		{
			name: "source fallback",
			results: []workspace.LoadResult{{
				RepoName:          "api",
				ParseStats:        loader.ParseStats{Valid: 2},
				AuthorityWarnings: []string{"using stale compatibility export"},
			}},
			wantText: "source authority is incomplete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := workspaceWatchReloadAuthorityError(tt.results)
			if err == nil {
				t.Fatal("incomplete workspace reload accepted")
			}
			if got := err.Error(); !strings.Contains(got, "retaining the previous export") || !strings.Contains(got, tt.wantText) {
				t.Fatalf("reload error = %q, want retention message containing %q", got, tt.wantText)
			}
		})
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

func TestCPUProfileFinalizedBeforeCommandExit(t *testing.T) {
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

		requireReadableCPUProfile(t, exe, profilePath)
	})

	t.Run("preview error unwinds profile defer", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "preview-cpu.pprof")
		missingBundle := filepath.Join(t.TempDir(), "missing-pages-bundle")
		stdout, stderr, err := runCommandWithTimeout(
			t,
			tmpDir,
			exe,
			"--cpu-profile", profilePath,
			"--preview-pages", missingBundle,
		)
		if err == nil {
			t.Fatalf("missing preview bundle unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "Error starting preview server") {
			t.Fatalf("missing preview error diagnostic\nstderr:\n%s", stderr)
		}
		requireReadableCPUProfile(t, exe, profilePath)
	})

	t.Run("direct exit command finalizes profile", func(t *testing.T) {
		profilePath := filepath.Join(t.TempDir(), "agents-check-cpu.pprof")
		stdout, stderr, err := runCommandWithTimeout(
			t,
			tmpDir,
			exe,
			"--cpu-profile", profilePath,
			"--agents-check",
		)
		if err != nil {
			t.Fatalf("profiled direct-exit command failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		requireReadableCPUProfile(t, exe, profilePath)
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

func TestResolveSingleRepoWatchFile_RespectsExplicitBeadsDBFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(dbPath, []byte("placeholder"), 0644); err != nil {
		t.Fatalf("write selected db: %v", err)
	}
	t.Setenv(loader.BeadsDBEnvVar, dbPath)

	got, err := resolveSingleRepoWatchFile(t.TempDir())
	if err != nil {
		t.Fatalf("resolveSingleRepoWatchFile: %v", err)
	}
	requireString(t, got, dbPath)
}

func TestResolveSingleRepoWatchFileReusesLastSuccessfulSource(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("BV_ROBOT", "1")

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	selectedPath := filepath.Join(beadsDir, "issues.jsonl")
	writeIssueJSONL(t, selectedPath, "SELECTED-1")
	invalidPath := filepath.Join(beadsDir, "fresher-invalid.jsonl")
	if err := os.WriteFile(invalidPath, []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatalf("write invalid JSONL candidate: %v", err)
	}
	older := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if err := os.Chtimes(selectedPath, older, older); err != nil {
		t.Fatalf("age selected source: %v", err)
	}
	if err := os.Chtimes(invalidPath, newer, newer); err != nil {
		t.Fatalf("freshen invalid source: %v", err)
	}

	sources, err := datasource.DiscoverSources(datasource.DiscoveryOptions{
		BeadsDir:               beadsDir,
		RepoPath:               repoDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("discover watcher fixture: %v", err)
	}
	if len(sources) < 2 || sources[0].Path != invalidPath {
		t.Fatalf("fixture did not make naive rediscovery choose invalid source: %+v", sources)
	}
	issues, err := datasource.LoadIssues(repoDir)
	if err != nil {
		t.Fatalf("load watcher fixture: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "SELECTED-1" {
		t.Fatalf("loaded issues = %+v, want SELECTED-1", issues)
	}

	watchFile, err := resolveSingleRepoWatchFile(repoDir)
	if err != nil {
		t.Fatalf("resolveSingleRepoWatchFile: %v", err)
	}
	requireString(t, watchFile, selectedPath)
}

func TestSingleRepoTrackerAuthorityRequiresAffirmativeCurrentBRMetadata(t *testing.T) {
	for _, test := range []struct {
		name         string
		selectedBase string
		metadata     string
		markBD       bool
		wantReason   string
	}{
		{name: "canonical SQLite declared by real metadata", selectedBase: "beads.db", metadata: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`},
		{name: "canonical issues JSONL declared by real metadata", selectedBase: "issues.jsonl", metadata: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`},
		{name: "missing selected source", metadata: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`, wantReason: "could not be resolved"},
		{name: "missing metadata", selectedBase: "issues.jsonl", wantReason: "metadata"},
		{name: "malformed metadata", selectedBase: "issues.jsonl", metadata: `{not-json}`, wantReason: "metadata"},
		{name: "metadata without source declarations", selectedBase: "issues.jsonl", metadata: `{}`, wantReason: "metadata"},
		{name: "mismatched declaration", selectedBase: "issues.jsonl", metadata: `{"jsonl_export":"other.jsonl"}`, wantReason: "metadata"},
		{name: "ambiguous database declaration for JSONL", selectedBase: "issues.jsonl", metadata: `{"database":"issues.jsonl"}`, wantReason: "metadata"},
		{name: "ambiguous JSONL declaration for SQLite", selectedBase: "beads.db", metadata: `{"jsonl_export":"beads.db"}`, wantReason: "metadata"},
		{name: "legacy JSONL remains diagnostic even when declared", selectedBase: "beads.jsonl", metadata: `{"jsonl_export":"beads.jsonl"}`, wantReason: "canonical"},
		{name: "arbitrary JSONL remains diagnostic even when declared", selectedBase: "custom.jsonl", metadata: `{"jsonl_export":"custom.jsonl"}`, wantReason: "canonical"},
		{name: "Dolt backend is not current br authority", selectedBase: "issues.jsonl", metadata: `{"backend":"dolt","jsonl_export":"issues.jsonl"}`, wantReason: "Dolt"},
		{name: "bd Dolt marker overrides current-looking metadata", selectedBase: "issues.jsonl", metadata: `{"jsonl_export":"issues.jsonl"}`, markBD: true, wantReason: "bd/Dolt workspace detected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if test.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(test.metadata), 0o644); err != nil {
					t.Fatalf("write metadata fixture: %v", err)
				}
			}
			if test.markBD {
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
					t.Fatalf("mark bd/Dolt workspace: %v", err)
				}
			}
			selectedPath := ""
			if test.selectedBase != "" {
				selectedPath = filepath.Join(beadsDir, test.selectedBase)
				if err := os.WriteFile(selectedPath, nil, 0o644); err != nil {
					t.Fatalf("write selected-source fixture: %v", err)
				}
			}
			reasons := singleRepoTrackerAuthorityUnavailableReasons(beadsDir, selectedPath)
			if test.wantReason == "" {
				if len(reasons) != 0 {
					t.Fatalf("affirmatively declared current-br source rejected: %v", reasons)
				}
				return
			}
			if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), test.wantReason) {
				t.Fatalf("tracker authority reasons = %v, want %q", reasons, test.wantReason)
			}
		})
	}
}

func TestSingleRepoTrackerAuthorityRejectsSymlinkedAuthorityFiles(t *testing.T) {
	t.Run("selected source", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeCurrentBRMetadata(t, beadsDir)
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, nil, 0o644); err != nil {
			t.Fatalf("write outside source: %v", err)
		}
		selected := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.Symlink(outside, selected); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		reasons := singleRepoTrackerAuthorityUnavailableReasons(beadsDir, selected)
		if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), "non-symlink") {
			t.Fatalf("symlinked selected source was accepted: %v", reasons)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		beadsDir := t.TempDir()
		selected := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(selected, nil, 0o644); err != nil {
			t.Fatalf("write selected source: %v", err)
		}
		outsideMetadata := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(outsideMetadata, []byte(`{"jsonl_export":"issues.jsonl"}`), 0o644); err != nil {
			t.Fatalf("write outside metadata: %v", err)
		}
		if err := os.Symlink(outsideMetadata, filepath.Join(beadsDir, "metadata.json")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		reasons := singleRepoTrackerAuthorityUnavailableReasons(beadsDir, selected)
		if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), "non-symlink") {
			t.Fatalf("symlinked metadata was accepted: %v", reasons)
		}
	})
}

func TestEmitScriptIsInspectOnly(t *testing.T) {
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	t.Setenv("BV_NO_CACHE", "1")

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"READY-1","title":"Ready work","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write issues: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

	exe := buildTestBinary(t)
	stdout, stderr, err := runCommandWithTimeout(t, dir, exe, "--emit-script", "--script-limit=1")
	if err != nil {
		t.Fatalf("emit inspect-only script: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "inspect-only") {
		t.Fatalf("script does not identify its non-mutating behavior:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# To claim: br update READY-1 --status=in_progress") {
		t.Fatalf("script omitted the commented opt-in claim command:\n%s", stdout)
	}

	var executable []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		executable = append(executable, line)
	}
	wantExecutable := []string{"set -euo pipefail", "br show READY-1"}
	if !reflect.DeepEqual(executable, wantExecutable) {
		t.Fatalf("executable script lines = %q, want inspect-only %q\n%s", executable, wantExecutable, stdout)
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

func TestRobotNowRejectsUnencodableSourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "9223372036854775807")

	if sourceDateEpochActive() {
		t.Fatal("unencodable SOURCE_DATE_EPOCH should not activate the pinned robot clock")
	}
	got := robotNow()
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("robotNow() fallback %v is not JSON encodable: %v", got, err)
	}
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

func TestShellScriptFieldsRejectCommandInjection(t *testing.T) {
	comment := shellCommentText("title\nprintf ignored\x00\tend")
	if strings.ContainsAny(comment, "\r\n\x00\t") {
		t.Fatalf("shell comment retained a control character: %q", comment)
	}
	if comment != "title printf ignored  end" {
		t.Fatalf("shell comment = %q, want single inert line", comment)
	}
	if got := shellCommentText("reason\\"); got != "reason\\ " {
		t.Fatalf("trailing-backslash comment = %q, want newline-stable comment text", got)
	}

	if got, ok := shellCommandBeadID("team's item"); !ok || got != "'team'\\''s item'" {
		t.Fatalf("quoted bead ID = (%q, %v), want safe single-quoted word", got, ok)
	}
	for _, id := range []string{"--help", "READY-1\nprintf ignored", "READY-1\x00ignored"} {
		if got, ok := shellCommandBeadID(id); ok || got != "" {
			t.Fatalf("unsafe bead ID %q encoded as (%q, %v)", id, got, ok)
		}
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

func TestResolveSingleRepoWatchFileUsesDiscoveredBeadsJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"legacy"}`+"\n"), 0644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"canonical"}`+"\n"), 0644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}

	watchFile, err := resolveSingleRepoWatchFile(tmpDir)
	if err != nil {
		t.Fatalf("resolveSingleRepoWatchFile: %v", err)
	}
	requireString(t, filepath.Base(watchFile), "beads.jsonl")
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

func TestIssueBackedRobotSchemasExposeSourceEvidence(t *testing.T) {
	schemas := generateRobotSchemas()
	evidenceFields := []string{
		"load_stats",
		"authority_incomplete_reasons",
		"repository_route_unavailable_reasons",
		"as_of",
		"as_of_commit",
		"analysis_time",
	}
	envelopeProperties := requireNestedSchemaProperties(t, schemas.Envelope, "robot envelope")
	for _, field := range evidenceFields {
		if envelopeProperties[field] == nil {
			t.Fatalf("common robot envelope schema missing source evidence property %q", field)
		}
	}
	for command, doc := range robotCommandDocs() {
		if !doc.NeedsIssues {
			continue
		}
		properties := requireRobotSchemaProperties(t, schemas, command)
		for _, field := range evidenceFields {
			if properties[field] == nil {
				t.Fatalf("%s schema missing source evidence property %q", command, field)
			}
		}
	}
}

func TestRobotPlanAndInsightsSchemasMatchRuntimeShapes(t *testing.T) {
	schemas := generateRobotSchemas()
	planProperties := requireRobotSchemaProperties(t, schemas, "robot-plan")
	plan := requireNestedSchemaProperties(t, planProperties["plan"], "robot-plan plan")
	for _, field := range []string{"tracks", "total_actionable", "total_blocked", "summary"} {
		if plan[field] == nil {
			t.Fatalf("robot-plan plan schema missing %q", field)
		}
	}
	if plan["phases"] != nil {
		t.Fatalf("robot-plan schema still documents nonexistent phases")
	}

	insights := requireRobotSchemaProperties(t, schemas, "robot-insights")
	for _, field := range []string{"analysis_config", "full_stats", "top_what_ifs", "ClusterDensity", "Cores", "Slack", "status"} {
		if insights[field] == nil {
			t.Fatalf("robot-insights schema missing %q", field)
		}
	}
	for _, field := range []string{"Cores", "Slack"} {
		property, ok := insights[field].(map[string]interface{})
		if !ok || property["type"] != "array" {
			t.Fatalf("robot-insights %s schema type = %v, want array", field, property["type"])
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

func TestSearchExactIDPromotionTreatsIssueIDsAsOpaque(t *testing.T) {
	query := "bv-9gf.3"
	textResults := []search.SearchResult{
		{IssueID: "bv-other", Score: 0.9},
		{IssueID: query, Score: 0.1},
	}
	promotedText := promoteExactSearchResult(query, textResults)
	if promotedText[0].IssueID != query || promotedText[1].IssueID != "bv-other" {
		t.Fatalf("text exact-ID promotion = %+v", promotedText)
	}

	hybridResults := []search.HybridScore{
		{IssueID: "bv-other", FinalScore: 0.9},
		{IssueID: query, FinalScore: 0.1},
	}
	promotedHybrid := promoteExactHybridResult(query, hybridResults)
	if promotedHybrid[0].IssueID != query || promotedHybrid[1].IssueID != "bv-other" {
		t.Fatalf("hybrid exact-ID promotion = %+v", promotedHybrid)
	}
}

func TestSearchExactIDPromotionRejectsAmbiguousCaseFold(t *testing.T) {
	textResults := []search.SearchResult{
		{IssueID: "other", Score: 0.9},
		{IssueID: "ABC", Score: 0.2},
		{IssueID: "abc", Score: 0.1},
	}
	promotedText := promoteExactSearchResult("AbC", textResults)
	if promotedText[0].IssueID != "other" {
		t.Fatalf("ambiguous folded text match was promoted: %+v", promotedText)
	}
	promotedText = promoteExactSearchResult("abc", textResults)
	if promotedText[0].IssueID != "abc" {
		t.Fatalf("exact-case text match was not promoted: %+v", promotedText)
	}

	hybridResults := []search.HybridScore{
		{IssueID: "other", FinalScore: 0.9},
		{IssueID: "ABC", FinalScore: 0.2},
		{IssueID: "abc", FinalScore: 0.1},
	}
	promotedHybrid := promoteExactHybridResult("AbC", hybridResults)
	if promotedHybrid[0].IssueID != "other" {
		t.Fatalf("ambiguous folded hybrid match was promoted: %+v", promotedHybrid)
	}
	promotedHybrid = promoteExactHybridResult("abc", hybridResults)
	if promotedHybrid[0].IssueID != "abc" {
		t.Fatalf("exact-case hybrid match was not promoted: %+v", promotedHybrid)
	}
}

func TestLoadIssuesFromBeadsDirMetadataJSONLExcludesTombstones(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := strings.Join([]string{
		`{"id":"LIVE-1","title":"Live","status":"open","issue_type":"task"}`,
		`{"id":"OLD-1","title":"Deleted","status":"tombstone","issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(issuesPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write issues: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"jsonl_export":"issues.jsonl"}`),
		0o644,
	); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	issues, err := loadIssuesFromBeadsDir(beadsDir)
	if err != nil {
		t.Fatalf("loadIssuesFromBeadsDir: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "LIVE-1" {
		t.Fatalf("metadata-selected issues = %#v, want only LIVE-1", issues)
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

func TestRobotCorrelationSchemasMatchHandlerOutputs(t *testing.T) {
	schemas := generateRobotSchemas()

	explanation := requireRobotSchemaProperties(t, schemas, "robot-explain-correlation")
	explanationFields := []string{
		"generated_at", "data_hash", "output_format", "version",
		"commit_sha", "bead_id", "confidence", "confidence_pct", "level", "method",
		"signals", "total_weight", "summary", "recommendation",
	}
	for _, name := range explanationFields {
		if explanation[name] == nil {
			t.Fatalf("robot-explain-correlation schema missing top-level property %q", name)
		}
	}
	explanationRequired, ok := schemas.Commands["robot-explain-correlation"]["required"].([]string)
	if !ok {
		t.Fatalf("robot-explain-correlation required has unexpected type %T", schemas.Commands["robot-explain-correlation"]["required"])
	}
	for _, name := range explanationFields {
		requireContainsString(t, explanationRequired, name)
	}
	signals, ok := explanation["signals"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-explain-correlation signals has unexpected type %T", explanation["signals"])
	}
	signal := requireNestedSchemaProperties(t, signals["items"], "robot-explain-correlation signal")
	for _, name := range []string{"type", "weight", "detail"} {
		if signal[name] == nil {
			t.Fatalf("robot-explain-correlation signal schema missing %q", name)
		}
	}

	for _, command := range []string{"robot-confirm-correlation", "robot-reject-correlation"} {
		properties := requireRobotSchemaProperties(t, schemas, command)
		feedbackFields := []string{
			"generated_at", "data_hash", "output_format", "version",
			"status", "commit", "bead", "by", "reason", "orig_conf",
		}
		for _, name := range feedbackFields {
			if properties[name] == nil {
				t.Fatalf("%s schema missing top-level property %q", command, name)
			}
		}
		required, ok := schemas.Commands[command]["required"].([]string)
		if !ok {
			t.Fatalf("%s required has unexpected type %T", command, schemas.Commands[command]["required"])
		}
		for _, name := range feedbackFields {
			requireContainsString(t, required, name)
		}
		status, ok := properties["status"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s status has unexpected type %T", command, properties["status"])
		}
		statuses, ok := status["enum"].([]string)
		if !ok {
			t.Fatalf("%s status enum has unexpected type %T", command, status["enum"])
		}
		wantStatus := "confirmed"
		if command == "robot-reject-correlation" {
			wantStatus = "rejected"
		}
		requireContainsString(t, statuses, wantStatus)
	}
}

func TestRobotCorrelationStatsOutputIncludesEnvelopeWithoutIssueSource(t *testing.T) {
	exe := buildTestBinary(t)
	tmpDir := t.TempDir()

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
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "result", "degraded"} {
		if blockerProps[name] == nil {
			t.Fatalf("robot-blocker-chain schema missing top-level property %q", name)
		}
	}
	degraded, ok := blockerProps["degraded"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-blocker-chain degraded has unexpected type %T", blockerProps["degraded"])
	}
	degradation := requireNestedSchemaProperties(t, degraded["items"], "robot-blocker-chain degradation")
	for _, name := range []string{"code", "severity", "message", "repair"} {
		if degradation[name] == nil {
			t.Fatalf("robot-blocker-chain degradation schema missing %q", name)
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
	for _, name := range []string{"bead_id", "title", "status", "events", "edge_count", "start_time", "end_time", "total_time", "is_complete"} {
		if causalChainProps[name] == nil {
			t.Fatalf("robot-causality chain schema missing %q", name)
		}
	}
	causalInsightsProps := requireNestedSchemaProperties(t, causalityProps["insights"], "robot-causality insights")
	for _, name := range []string{"total_duration", "blocked_duration", "active_duration", "blocked_percentage", "blocked_periods", "critical_path", "summary", "recommendations"} {
		if causalInsightsProps[name] == nil {
			t.Fatalf("robot-causality insights schema missing %q", name)
		}
	}
}

func TestRobotRelationshipWorkflowDocsExposeLiveJSONPaths(t *testing.T) {
	docs := robotCommandDocs()
	expectations := map[string][]string{
		"robot-related":        {"target_bead_id", "total_related", "file_overlap"},
		"robot-blocker-chain":  {"result.target_id", "result.root_blockers", "result.chain"},
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

func TestRobotTriageSchemaSeparatesRecommendationShapes(t *testing.T) {
	schemas := generateRobotSchemas()
	schema := schemas.Commands["robot-triage"]
	properties := requireRobotSchemaProperties(t, schemas, "robot-triage")

	triage := requireNestedSchemaProperties(t, properties["triage"], "robot-triage triage")
	quickRef := requireNestedSchemaProperties(t, triage["quick_ref"], "robot-triage quick_ref")
	topPicks, ok := quickRef["top_picks"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-triage top_picks has unexpected type %T", quickRef["top_picks"])
	}
	topPickItems, ok := topPicks["items"].(map[string]interface{})
	if !ok || topPickItems["$ref"] != "#/$defs/top_pick" {
		t.Fatalf("robot-triage top_picks items = %#v, want top_pick ref", topPicks["items"])
	}

	recommendations, ok := triage["recommendations"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-triage recommendations has unexpected type %T", triage["recommendations"])
	}
	recommendationItems, ok := recommendations["items"].(map[string]interface{})
	if !ok || recommendationItems["$ref"] != "#/$defs/recommendation" {
		t.Fatalf("robot-triage recommendations items = %#v, want recommendation ref", recommendations["items"])
	}

	briefRecommendations, ok := properties["recommendations"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-triage brief recommendations has unexpected type %T", properties["recommendations"])
	}
	briefItems, ok := briefRecommendations["items"].(map[string]interface{})
	if !ok || briefItems["$ref"] != "#/$defs/brief_recommendation" {
		t.Fatalf("robot-triage brief recommendations items = %#v, want brief_recommendation ref", briefRecommendations["items"])
	}

	definitions, ok := schema["$defs"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-triage $defs has unexpected type %T", schema["$defs"])
	}
	topPick := requireNestedSchemaProperties(t, definitions["top_pick"], "robot-triage top_pick definition")
	for _, name := range []string{"id", "title", "score", "reasons", "unblocks"} {
		if topPick[name] == nil {
			t.Fatalf("robot-triage top_pick definition missing %q", name)
		}
	}
	for _, name := range []string{"action", "breakdown", "unblocks_ids"} {
		if topPick[name] != nil {
			t.Fatalf("robot-triage top_pick definition incorrectly exposes full recommendation field %q", name)
		}
	}

	full := requireNestedSchemaProperties(t, definitions["recommendation"], "robot-triage recommendation definition")
	for _, name := range []string{
		"id", "title", "type", "status", "priority", "score", "breakdown", "action",
		"reasons", "unblocks_ids", "blocked_by",
	} {
		if full[name] == nil {
			t.Fatalf("robot-triage recommendation definition missing %q", name)
		}
	}
	if full["unblocks"] != nil {
		t.Fatalf("robot-triage full recommendation incorrectly exposes integer top-pick field unblocks")
	}

	brief := requireNestedSchemaProperties(t, definitions["brief_recommendation"], "robot-triage brief recommendation definition")
	for _, name := range []string{"id", "title", "status", "assignee", "score", "unblocks", "blocked_by"} {
		if brief[name] == nil {
			t.Fatalf("robot-triage brief recommendation definition missing %q", name)
		}
	}
	for _, name := range []string{"action", "breakdown", "unblocks_ids"} {
		if brief[name] != nil {
			t.Fatalf("robot-triage brief recommendation definition incorrectly exposes full field %q", name)
		}
	}
}

func TestRobotNextSchemaMatchesActionableAndDegradedOutputs(t *testing.T) {
	schemas := generateRobotSchemas()
	properties := requireRobotSchemaProperties(t, schemas, "robot-next")
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version", "load_stats",
		"authority_incomplete_reasons", "repository_route_unavailable_reasons",
		"as_of", "as_of_commit", "analysis_time", "actionable", "phase2_ready", "status", "message",
		"id", "title", "score", "reasons", "unblocks", "diagnostic_top_pick",
		"claim_command", "show_command", "degraded", "usage_hints",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-next schema missing top-level property %q", name)
		}
	}

	schema := schemas.Commands["robot-next"]
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("robot-next required has unexpected type %T", schema["required"])
	}
	for _, name := range []string{"generated_at", "data_hash", "output_format", "version", "actionable", "phase2_ready", "status"} {
		requireContainsString(t, required, name)
	}
	for _, conditional := range []string{"id", "title", "score", "claim_command", "show_command"} {
		for _, name := range required {
			if name == conditional {
				t.Fatalf("robot-next schema requires conditional field %q", conditional)
			}
		}
	}

	diagnostic := requireNestedSchemaProperties(t, properties["diagnostic_top_pick"], "robot-next diagnostic_top_pick")
	for _, name := range []string{"id", "title", "score", "reasons", "unblocks"} {
		if diagnostic[name] == nil {
			t.Fatalf("robot-next diagnostic schema missing %q", name)
		}
	}

	degraded, ok := properties["degraded"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-next degraded has unexpected type %T", properties["degraded"])
	}
	degradation := requireNestedSchemaProperties(t, degraded["items"], "robot-next degradation item")
	for _, name := range []string{"code", "severity", "message", "repair"} {
		if degradation[name] == nil {
			t.Fatalf("robot-next degradation schema missing %q", name)
		}
	}

	doc := robotCommandDocs()["robot-next"]
	for _, name := range []string{"actionable", "phase2_ready", "status", "claim_command", "degraded"} {
		requireContainsString(t, doc.KeyFields, name)
	}
	if !strings.Contains(doc.Description, "only when actionable") {
		t.Fatalf("robot-next description does not explain conditional claim commands: %q", doc.Description)
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
	for _, name := range []string{"resolved_revision", "from_data_hash", "to_data_hash", "from_load_stats", "to_load_stats", "degraded", "diff"} {
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
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"load_stats", "authority_incomplete_reasons", "repository_route_unavailable_reasons", "as_of", "as_of_commit", "analysis_time",
		"format", "graph", "nodes", "edges", "filters_applied", "explanation",
		"adjacency", "analysis_config", "status",
	} {
		if properties[name] == nil {
			t.Fatalf("robot-graph schema missing top-level property %q", name)
		}
	}
	for _, stale := range []string{"stats"} {
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
	for _, name := range []string{
		"generated_at", "data_hash", "output_format", "version",
		"load_stats", "authority_incomplete_reasons", "repository_route_unavailable_reasons", "as_of", "as_of_commit", "analysis_time",
		"filters", "suggestions", "degraded", "usage_hints",
	} {
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
	for _, name := range []string{"flow", "analysis_config", "usage_hints"} {
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
		"critical_path", "actionable_count", "actionable", "bottlenecks", "degraded",
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

	degradedProp, ok := properties["degraded"].(map[string]interface{})
	if !ok {
		t.Fatalf("robot-capacity degraded has unexpected type %T", properties["degraded"])
	}
	degradationProps := requireNestedSchemaProperties(t, degradedProp["items"], "robot-capacity degradation item")
	for _, name := range []string{"code", "severity", "message", "repair"} {
		if degradationProps[name] == nil {
			t.Fatalf("robot-capacity degradation schema missing %q", name)
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
				"Error: --history-since requires one of --robot-history or --bead-history",
				"Try: `bv robot-history --history-since \"30 days ago\" --json`.",
			},
		},
		{
			name: "history timeout is rejected by robot next",
			args: []string{"--robot-next", "--robot-history-timeout-ms", "1"},
			wantMessages: []string{
				"Error: --robot-history-timeout-ms requires one of --robot-triage, --robot-triage-by-track or --robot-triage-by-label",
				"Try: `bv robot-triage --robot-history-timeout-ms 10000 --json`.",
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

func TestApplyRecipeFilters_ActionableAndHasBlockers(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	now := time.Now()
	a := model.Issue{ID: "A", Title: "Root", Status: model.StatusOpen, Priority: 2, CreatedAt: now}
	b := model.Issue{
		ID:     "B",
		Title:  "Blocked by A",
		Status: model.StatusOpen,
		Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		},
		CreatedAt: now.Add(-time.Hour),
	}
	issues := []model.Issue{a, b}

	r := &recipe.Recipe{
		Filters: recipe.FilterConfig{
			Actionable: ptrBool(true),
		},
	}
	actionable := applyRecipeFilters(issues, r)
	requireIssueIDs(t, actionable, "A")

	r.Filters.Actionable = nil
	r.Filters.HasBlockers = ptrBool(true)
	blocked := applyRecipeFilters(issues, r)
	requireIssueIDs(t, blocked, "B")
}

func TestApplyRecipeFilters_UsesCanonicalParentBlocking(t *testing.T) {
	issues := []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen},
		{ID: "PARENT", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "ROOT", Type: model.DepBlocks},
		}},
		{ID: "CHILD", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "PARENT", Type: model.DepParentChild},
		}},
		{ID: "CLOSED", Status: model.StatusClosed},
	}

	r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}}
	requireIssueIDs(t, applyRecipeFilters(issues, r), "ROOT")

	r.Filters.Actionable = nil
	r.Filters.HasBlockers = ptrBool(true)
	requireIssueIDs(t, applyRecipeFilters(issues, r), "PARENT", "CHILD")
}

func TestApplyRecipeFilters_TombstoneDependencyIsNotAnOpenBlocker(t *testing.T) {
	issues := []model.Issue{
		{ID: "DONE", Title: "Removed blocker", Status: model.StatusTombstone},
		{
			ID:     "READY",
			Title:  "Depends on removed blocker",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "DONE", Type: model.DepBlocks},
			},
		},
	}
	r := &recipe.Recipe{Filters: recipe.FilterConfig{HasBlockers: ptrBool(true)}}
	if got := applyRecipeFilters(issues, r); len(got) != 0 {
		t.Fatalf("tombstone dependency was treated as open: %+v", got)
	}
}

func TestApplyRecipeFilters_TitleAndPrefix(t *testing.T) {
	issues := []model.Issue{
		{ID: "UI-1", Title: "Add login button"},
		{ID: "API-2", Title: "Login endpoint"},
		{ID: "API-3", Title: "Health check"},
	}
	r := &recipe.Recipe{
		Filters: recipe.FilterConfig{
			TitleContains: "login",
			IDPrefix:      "API",
		},
	}
	got := applyRecipeFilters(issues, r)
	requireIssueIDs(t, got, "API-2")
}

func TestApplyRecipeFilters_TagsAndDates(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	issues := []model.Issue{
		{ID: "T1", Title: "Tagged", Labels: []string{"backend", "p0"}, CreatedAt: now, UpdatedAt: now},
		{ID: "T2", Title: "Old", Labels: []string{"backend"}, CreatedAt: old, UpdatedAt: old},
	}
	r := &recipe.Recipe{
		Filters: recipe.FilterConfig{
			Tags:         []string{"backend"},
			ExcludeTags:  []string{"p0"},
			CreatedAfter: "1d",
			UpdatedAfter: "1d",
		},
	}
	got := applyRecipeFilters(issues, r)
	if len(got) != 0 {
		t.Fatalf("expected all filtered out (exclude p0 and date), got %#v", got)
	}
}

func TestApplyRecipeFilters_DatesBlockersAndPrefix(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	now := time.Now()
	early := now.Add(-72 * time.Hour)
	issues := []model.Issue{
		{ID: "API-1", Title: "Fresh", CreatedAt: now, UpdatedAt: now},
		{ID: "API-2", Title: "Stale", CreatedAt: early, UpdatedAt: early,
			Dependencies: []*model.Dependency{{DependsOnID: "API-1", Type: model.DepBlocks}}},
	}
	r := &recipe.Recipe{Filters: recipe.FilterConfig{
		CreatedBefore: "1h",
		UpdatedBefore: "1h",
		HasBlockers:   ptrBool(true),
		IDPrefix:      "API-2",
	}}
	got := applyRecipeFilters(issues, r)
	requireIssueIDs(t, got, "API-2")

	r.Filters.HasBlockers = ptrBool(false)
	got = applyRecipeFilters(issues, r)
	if len(got) != 0 {
		t.Fatalf("expected blockers=false to exclude API-2, got %#v", got)
	}
}

func TestInteractiveRecipeFiltersIgnoreSourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "0")

	elapsed := time.Now().UTC().Add(-time.Minute)
	issue := model.Issue{
		ID:         "ELAPSED-1",
		Title:      "Deferral elapsed on the wall clock",
		Status:     model.StatusOpen,
		IssueType:  model.TypeTask,
		DeferUntil: &elapsed,
	}
	r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}}

	requireIssueIDs(t, applyRecipeFilters([]model.Issue{issue}, r), issue.ID)
	if got := applyRobotRecipeFilters([]model.Issue{issue}, r); len(got) != 0 {
		t.Fatalf("pinned robot filter should still treat %s as deferred at epoch zero: %#v", issue.ID, got)
	}
}

func TestApplyRecipeSort_DefaultsAndFields(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "A", Title: "zzz", Priority: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-30 * time.Minute)},
		{ID: "B", Title: "aaa", Priority: 0, CreatedAt: now, UpdatedAt: now},
	}

	// Priority default ascending
	r := &recipe.Recipe{Sort: recipe.SortConfig{Field: "priority"}}
	sorted := applyRecipeSort(append([]model.Issue{}, issues...), r)
	requireIssueIDs(t, sorted[:1], "B")

	// Created default descending (newest first)
	r.Sort = recipe.SortConfig{Field: "created"}
	sorted = applyRecipeSort(append([]model.Issue{}, issues...), r)
	requireIssueIDs(t, sorted[:1], "B")

	// Title ascending explicit desc
	r.Sort = recipe.SortConfig{Field: "title", Direction: "desc"}
	sorted = applyRecipeSort(append([]model.Issue{}, issues...), r)
	requireIssueIDs(t, sorted[:1], "A")

	// Status ascending (string compare)
	r.Sort = recipe.SortConfig{Field: "status"}
	sorted = applyRecipeSort(append([]model.Issue{}, issues...), r)
	requireIssueIDs(t, sorted[:1], "A")

	// ID natural sort
	idIssues := []model.Issue{
		{ID: "bv-10"},
		{ID: "bv-2"},
		{ID: "bv-1"},
	}
	r.Sort = recipe.SortConfig{Field: "id"}
	sortedIDs := applyRecipeSort(append([]model.Issue{}, idIssues...), r)
	requireIssueIDs(t, sortedIDs, "bv-1", "bv-2", "bv-10")

	// Unknown field should preserve order
	r.Sort = recipe.SortConfig{Field: "unknown"}
	sorted = applyRecipeSort(append([]model.Issue{}, issues...), r)
	requireIssueIDs(t, sorted, "A", "B")
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

func TestPathWithinDirectoryResolvesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.jsonl")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatalf("write inside file: %v", err)
	}
	if !pathWithinDirectory(inside, root) {
		t.Fatal("ordinary child path should be within root")
	}

	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "outside.jsonl")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escape := filepath.Join(root, "escape.jsonl")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if pathWithinDirectory(escape, root) {
		t.Fatal("symlink to an outside source must not pass repository containment")
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
}
