package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type inspectedHook struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
	Timeout time.Duration     `json:"timeout"`
	OnError string            `json:"on_error"`
}

type hookInspection struct {
	Digest     string `json:"digest"`
	ProjectDir string `json:"project_dir"`
	ConfigPath string `json:"config_path"`
	Platform   string `json:"platform"`
	Hooks      struct {
		PreExport  []inspectedHook `json:"pre-export"`
		PostExport []inspectedHook `json:"post-export"`
	} `json:"hooks"`
}

func writeHookFixture(t *testing.T, dir, config string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bv", "hooks.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hookProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, `{"id":"hook-1","title":"Hook security fixture","status":"open","priority":1,"issue_type":"task"}`+"\n")
	return dir
}

// runHookBV closes stdin and bounds runtime so an accidental interactive trust
// prompt fails the test instead of hanging an unattended export indefinitely.
func runHookBV(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildBvBinary(t), args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("noninteractive hook command did not finish: %v\nstdout=%s\nstderr=%s", ctx.Err(), &stdout, &stderr)
	}
	return stdout.String(), stderr.String(), err
}

// Fixture approval is deliberately computed only for test-authored commands.
// Production automation must supply a digest reviewed outside the untrusted job.
func reviewedHookDigest(t *testing.T, dir string) string {
	t.Helper()
	stdout, stderr, err := runHookBV(t, dir, "--hooks-info")
	if err != nil {
		t.Fatalf("inspect test-authored hooks: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var info hookInspection
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("decode hook inspection: %v\n%s", err, stdout)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(info.Digest) {
		t.Fatalf("invalid review digest %q", info.Digest)
	}
	return info.Digest
}

func requireHookFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no hook/export side effect at %s, stat=%v", path, err)
	}
}

func hookExportArgs(kind, path string) []string {
	switch kind {
	case "markdown":
		return []string{"--export-md", path}
	case "json":
		return []string{"--export", path, "--export-format", "json"}
	case "pages":
		return []string{"--export-pages", path, "--pages-include-history=false"}
	default:
		panic("unexpected hook test export kind")
	}
}

func TestHookTrustInspectionIsReadOnlyAndDoesNotExpandSecrets(t *testing.T) {
	dir := t.TempDir() // Inspection must work before any issue data is loaded.
	const secret = "fake-inspection-secret-sentinel"
	t.Setenv("BV_HOOK_TEST_SECRET", secret)
	writeHookFixture(t, dir, `hooks:
  pre-export:
    - name: "inspect\u001b[2J"
      command: 'echo inspected > inspection-marker'
      timeout: 12s
      on_error: continue
      env:
        COPY: "${BV_HOOK_TEST_SECRET}"
`)
	stdout, stderr, err := runHookBV(t, dir, "--hooks-info")
	if err != nil {
		t.Fatalf("read-only inspection failed: %v\n%s\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, secret) || strings.ContainsRune(stdout, '\x1b') {
		t.Fatalf("inspection expanded a secret or emitted a terminal escape: %q", stdout+stderr)
	}
	var info hookInspection
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ProjectDir != canonical || info.ConfigPath != filepath.Join(canonical, ".bv", "hooks.yaml") || info.Platform != runtime.GOOS {
		t.Fatalf("inspection lost project/platform identity: %+v", info)
	}
	if len(info.Hooks.PreExport) != 1 || info.Hooks.PreExport[0].Command != "echo inspected > inspection-marker" || info.Hooks.PreExport[0].Env["COPY"] != "${BV_HOOK_TEST_SECRET}" || info.Hooks.PreExport[0].Timeout != 12*time.Second || info.Hooks.PreExport[0].OnError != "continue" {
		t.Fatalf("inspection lost exact hook declarations: %+v", info)
	}
	requireHookFileAbsent(t, filepath.Join(dir, "inspection-marker"))
	requireHookFileAbsent(t, filepath.Join(dir, ".beads"))
}

func TestHookTrustUnapprovedExportsFailBeforeExecutingEitherPhase(t *testing.T) {
	for _, kind := range []string{"markdown", "json", "pages"} {
		for _, phase := range []string{"pre-export", "post-export"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				dir := hookProject(t)
				writeHookFixture(t, dir, fmt.Sprintf(`trusted: true
hooks:
  %s:
    - name: unreviewed
      command: 'echo unexpected > unapproved-marker'
      on_error: continue
`, phase))
				output := filepath.Join(dir, "unapproved-export")
				args := append(hookExportArgs(kind, output), "--yes")
				stdout, stderr, err := runHookBV(t, dir, args...)
				if err == nil {
					t.Fatalf("unapproved export unexpectedly succeeded\n%s\n%s", stdout, stderr)
				}
				for _, want := range []string{"not trusted", "--hooks-info", "--trust-hooks", "--no-hooks"} {
					if !strings.Contains(stdout+stderr, want) {
						t.Errorf("trust denial lacks %q guidance\n%s\n%s", want, stdout, stderr)
					}
				}
				requireHookFileAbsent(t, filepath.Join(dir, "unapproved-marker"))
				requireHookFileAbsent(t, output)
			})
		}
	}
}

func TestHookTrustApprovedExportsRetainBothPhasesWithoutPersistingApproval(t *testing.T) {
	stageViewerAssets(t, buildBvBinary(t))
	for _, kind := range []string{"markdown", "json", "pages"} {
		t.Run(kind, func(t *testing.T) {
			dir := hookProject(t)
			writeHookFixture(t, dir, `hooks:
  pre-export:
    - name: before
      command: 'echo pre > approved-pre-marker'
  post-export:
    - name: after
      command: 'echo post > approved-post-marker'
`)
			digest := reviewedHookDigest(t, dir)
			output := filepath.Join(dir, "approved-export")
			args := append(hookExportArgs(kind, output), "--trust-hooks", digest)
			stdout, stderr, err := runHookBV(t, dir, args...)
			if err != nil {
				t.Fatalf("reviewed export failed: %v\n%s\n%s", err, stdout, stderr)
			}
			for _, path := range []string{output, filepath.Join(dir, "approved-pre-marker"), filepath.Join(dir, "approved-post-marker")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("approved hook/export missing %s: %v", path, err)
				}
			}
			unapprovedOutput := filepath.Join(dir, "later-export")
			stdout, stderr, err = runHookBV(t, dir, hookExportArgs(kind, unapprovedOutput)...)
			if err == nil || !strings.Contains(stdout+stderr, "not trusted") {
				t.Fatalf("invocation approval leaked into a later export: %v\n%s\n%s", err, stdout, stderr)
			}
			requireHookFileAbsent(t, unapprovedOutput)
		})
	}
}

func TestHookTrustChangedDeclarationsInvalidateReview(t *testing.T) {
	const initial = `hooks:
  pre-export:
    - name: reviewed
      command: 'echo reviewed > changed-marker'
      timeout: 5s
      on_error: fail
      env:
        MODE: reviewed
`
	for _, tc := range []struct{ name, old, replacement string }{
		{"command", "echo reviewed >", "echo unreviewed >"},
		{"environment", "MODE: reviewed", "MODE: unreviewed"},
		{"timeout", "timeout: 5s", "timeout: 10s"},
		{"error policy", "on_error: fail", "on_error: continue"},
		{"phase", "pre-export:", "post-export:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := hookProject(t)
			writeHookFixture(t, dir, initial)
			digest := reviewedHookDigest(t, dir)
			writeHookFixture(t, dir, strings.Replace(initial, tc.old, tc.replacement, 1))
			output := filepath.Join(dir, "changed-export.md")
			stdout, stderr, err := runHookBV(t, dir, "--export-md", output, "--trust-hooks", digest)
			if err == nil || !strings.Contains(stdout+stderr, "not trusted") {
				t.Fatalf("changed declaration retained old authority: %v\n%s\n%s", err, stdout, stderr)
			}
			requireHookFileAbsent(t, output)
			requireHookFileAbsent(t, filepath.Join(dir, "changed-marker"))
		})
	}
}

func TestHookTrustDigestSupportsExplicitApprovalInEphemeralCheckout(t *testing.T) {
	const config = "hooks:\n  pre-export:\n    - command: 'echo approved > copied-marker'\n"
	first, second := hookProject(t), hookProject(t)
	writeHookFixture(t, first, config)
	writeHookFixture(t, second, config)
	digest := reviewedHookDigest(t, first)
	if got := reviewedHookDigest(t, second); got != digest {
		t.Fatalf("identical reviewed declarations changed digest across checkout paths: %s != %s", got, digest)
	}
	output := filepath.Join(second, "report.md")
	stdout, stderr, err := runHookBV(t, second, "--export-md", output, "--trust-hooks", digest)
	if err != nil {
		t.Fatalf("explicit approval in ephemeral checkout failed: %v\n%s\n%s", err, stdout, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(second, "copied-marker")); err != nil || strings.TrimSpace(string(data)) != "approved" {
		t.Fatalf("approved hook did not run: %q, %v", data, err)
	}
}

func TestHookTrustCredentialGrantsAreSeparateFromYAML(t *testing.T) {
	const secret = "fake-hook-primary-secret-sentinel"
	const otherSecret = "fake-hook-secondary-secret-sentinel"
	t.Setenv("BV_HOOK_TEST_SECRET", secret)
	t.Setenv("BV_HOOK_OTHER_TOKEN", otherSecret)
	t.Setenv("BV_HOOK_ORDINARY_VALUE", "ordinary-hook-value")
	command := `echo [$BV_HOOK_TEST_SECRET][$A_ALIAS][$B_CHAIN][$GITHUB_TOKEN][$C_OTHER][$BV_HOOK_ORDINARY_VALUE] > credential-marker`
	if runtime.GOOS == "windows" {
		command = `echo [%BV_HOOK_TEST_SECRET%][%A_ALIAS%][%B_CHAIN%][%GITHUB_TOKEN%][%C_OTHER%][%BV_HOOK_ORDINARY_VALUE%] > credential-marker`
	}
	for _, tc := range []struct {
		name       string
		grants     []string
		wantSecret bool
		wantOther  bool
	}{
		{"no user grants", nil, false, false},
		{"one user grant", []string{"--hook-env", "BV_HOOK_TEST_SECRET"}, true, false},
		{"two explicit grants", []string{"--hook-env", "BV_HOOK_TEST_SECRET", "--hook-env", "BV_HOOK_OTHER_TOKEN"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := hookProject(t)
			writeHookFixture(t, dir, fmt.Sprintf(`hooks:
  pre-export:
    - name: aliases
      command: '%s'
      env:
        A_ALIAS: "${BV_HOOK_TEST_SECRET}"
        B_CHAIN: "${A_ALIAS}"
        BV_HOOK_TEST_SECRET: "${BV_HOOK_TEST_SECRET}"
        C_OTHER: "${BV_HOOK_OTHER_TOKEN}"
        GITHUB_TOKEN: "${BV_HOOK_TEST_SECRET}"
`, command))
			digest := reviewedHookDigest(t, dir)
			args := []string{"--export-md", filepath.Join(dir, "report.md"), "--trust-hooks", digest}
			args = append(args, tc.grants...)
			stdout, stderr, err := runHookBV(t, dir, args...)
			if err != nil {
				t.Fatalf("credential fixture export failed: %v\n%s\n%s", err, stdout, stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, "credential-marker"))
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tc.wantSecret {
				wantCount = 4 // direct name, renamed alias, chained alias, protected destination
			}
			if strings.Count(string(data), secret) != wantCount || strings.Contains(string(data), otherSecret) != tc.wantOther || !strings.Contains(string(data), "ordinary-hook-value") {
				t.Fatalf("wrong granted child environment: %q", data)
			}
			if strings.Contains(stdout+stderr, secret) || strings.Contains(stdout+stderr, otherSecret) {
				t.Fatalf("credential value leaked into export diagnostics: %s\n%s", stdout, stderr)
			}
		})
	}
}

func TestHookTrustNoHooksShortCircuitsYAMLApprovalAndGrants(t *testing.T) {
	stageViewerAssets(t, buildBvBinary(t))
	for _, kind := range []string{"markdown", "json", "pages"} {
		for _, tc := range []struct{ name, config string }{
			{"malformed", "hooks: [invalid: yaml\n"},
			{"valid", "hooks:\n  pre-export:\n    - command: 'echo unexpected > disabled-hook-marker'\n"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				dir := hookProject(t)
				writeHookFixture(t, dir, tc.config)
				output := filepath.Join(dir, "without-hooks")
				args := append(hookExportArgs(kind, output), "--no-hooks", "--trust-hooks", "invalid", "--hook-env", "*=invalid")
				stdout, stderr, err := runHookBV(t, dir, args...)
				if err != nil {
					t.Fatalf("--no-hooks did not short-circuit: %v\n%s\n%s", err, stdout, stderr)
				}
				if _, err := os.Stat(output); err != nil {
					t.Fatalf("hook-free export missing: %v", err)
				}
				requireHookFileAbsent(t, filepath.Join(dir, "disabled-hook-marker"))
			})
		}
	}
}

func TestHookTrustInvalidGrantCannotExecuteReviewedHook(t *testing.T) {
	for _, grant := range []string{"BV_HOOK_TEST_SECRET=value", "*", ""} {
		t.Run(grant, func(t *testing.T) {
			dir := hookProject(t)
			writeHookFixture(t, dir, "hooks:\n  pre-export:\n    - command: 'echo unexpected > invalid-grant-marker'\n")
			digest := reviewedHookDigest(t, dir)
			output := filepath.Join(dir, "report.md")
			stdout, stderr, err := runHookBV(t, dir, "--export-md", output, "--trust-hooks", digest, "--hook-env="+grant)
			if err == nil {
				t.Fatalf("invalid grant %q accepted\n%s\n%s", grant, stdout, stderr)
			}
			requireHookFileAbsent(t, output)
			requireHookFileAbsent(t, filepath.Join(dir, "invalid-grant-marker"))
		})
	}
}

func TestHookTrustModifierFlagsRequireHookCapableExport(t *testing.T) {
	dir := hookProject(t)
	for _, args := range [][]string{
		{"--robot-triage", "--trust-hooks", "sha256:" + strings.Repeat("a", 64)},
		{"--robot-triage", "--hook-env", "BV_HOOK_TEST_SECRET"},
	} {
		stdout, stderr, err := runHookBV(t, dir, args...)
		if err == nil {
			t.Errorf("hook authority flag accepted without an export: %v\n%s\n%s", args, stdout, stderr)
		}
	}
}

func TestHookTrustWatchRejectsChangedCommands(t *testing.T) {
	stageViewerAssets(t, buildBvBinary(t))
	dir := hookProject(t)
	writeHookFixture(t, dir, "hooks:\n  pre-export:\n    - command: 'echo initial > watch-approved-marker'\n")
	digest := reviewedHookDigest(t, dir)
	output := filepath.Join(dir, "watch-export")
	logPath := filepath.Join(dir, "watch.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildBvBinary(t), "--export-pages", output, "--watch-export", "--pages-include-history=false", "--trust-hooks", digest)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitForLog := func(needle string, retry func()) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		nextRetry := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(logPath)
			if strings.Contains(string(data), needle) {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("watch exited before %q: %v\n%s", needle, err, data)
			default:
			}
			if retry != nil && time.Now().After(nextRetry) {
				retry()
				nextRetry = time.Now().Add(time.Second)
			}
			time.Sleep(25 * time.Millisecond)
		}
		data, _ := os.ReadFile(logPath)
		t.Fatalf("watch never reported %q\n%s", needle, data)
	}
	waitForLog("To preview with auto-refresh", nil)
	if _, err := os.Stat(filepath.Join(dir, "watch-approved-marker")); err != nil {
		t.Fatalf("initial approved hook did not run: %v", err)
	}
	writeHookFixture(t, dir, "hooks:\n  pre-export:\n    - command: 'echo unexpected > watch-unapproved-marker'\n      on_error: continue\n")
	update := 0
	changeIssues := func() {
		update++
		writeIssuesJSONL(t, dir, fmt.Sprintf("{\"id\":\"hook-1\",\"title\":\"Watch update %d\",\"status\":\"open\",\"priority\":1,\"issue_type\":\"task\"}\n", update))
	}
	changeIssues()
	// A second mutation also covers the narrow interval between the ready
	// message and the file watcher actually being attached.
	waitForLog("not trusted", changeIssues)
	requireHookFileAbsent(t, filepath.Join(dir, "watch-unapproved-marker"))
}
