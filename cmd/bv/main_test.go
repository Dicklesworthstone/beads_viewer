package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
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
				"Error: --history-since requires one of --robot-history or --bead-history",
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
	now := time.Now()
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
