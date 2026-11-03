package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeHooksFile(t *testing.T, dir, content string) {
	bv := filepath.Join(dir, ".bv")
	if err := os.MkdirAll(bv, 0o755); err != nil {
		t.Fatalf("mkdir .bv: %v", err)
	}
	path := filepath.Join(bv, "hooks.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write hooks.yaml: %v", err)
	}
}

func TestRunHooksNoHooksFlagAndMissingConfig(t *testing.T) {
	tmp := t.TempDir()
	exec, err := RunHooks(tmp, ExportContext{}, true)
	if err != nil || exec != nil {
		t.Fatalf("noHooks should short-circuit, got exec=%v err=%v", exec, err)
	}

	exec, err = RunHooks(tmp, ExportContext{}, false)
	if err != nil || exec != nil {
		t.Fatalf("missing config should return nil executor without error, got exec=%v err=%v", exec, err)
	}
}

func TestRunHooksLoadsExecutor(t *testing.T) {
	tmp := t.TempDir()
	writeHooksFile(t, tmp, `
hooks:
  pre-export:
    - name: hello
      command: echo hi
`)

	ctx := ExportContext{ExportPath: "out.md", ExportFormat: "markdown", IssueCount: 1, Timestamp: time.Now()}
	loader := NewLoader(WithProjectDir(tmp))
	if err := loader.Load(); err != nil {
		t.Fatalf("load hooks for approval: %v", err)
	}
	exec, err := RunHooks(tmp, ctx, false, approvedOptions(t, loader.Config()))
	if err != nil {
		t.Fatalf("RunHooks returned error: %v", err)
	}
	if exec == nil {
		t.Fatalf("expected executor when hooks present")
	}

	// Execution retains a private copy of the approved configuration.
	if exec.config == nil || len(exec.config.Hooks.PreExport) != 1 {
		t.Fatalf("executor config not initialized correctly")
	}

	if res := exec.Results(); len(res) != 0 {
		t.Fatalf("results should be empty before runs: %v", res)
	}
}

func TestLoadDefaultUsesCWD(t *testing.T) {
	tmp := t.TempDir()
	writeHooksFile(t, tmp, "hooks:\n  post-export:\n    - command: echo ok\n")
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	loader, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault error: %v", err)
	}
	if !loader.HasHooks() {
		t.Fatalf("expected hooks loaded via cwd")
	}
}

func TestRunPostExportFailStopsOnFail(t *testing.T) {
	config := &Config{Hooks: HooksByPhase{PostExport: []Hook{
		{Name: "fail", Command: "exit 1", Timeout: 200 * time.Millisecond, OnError: "fail"},
		{Name: "skip", Command: "echo ok", Timeout: 200 * time.Millisecond, OnError: "continue"},
	}}}

	exec := newApprovedExecutor(t, config, ExportContext{})
	err := exec.RunPostExport()
	if err == nil {
		t.Fatalf("expected error when post-export hook fails with on_error=fail")
	}
	if len(exec.Results()) != 2 {
		t.Fatalf("expected both hooks recorded, got %d", len(exec.Results()))
	}
}

func TestTruncateBehaviour(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate should return original when shorter, got %q", got)
	}
	if got := truncate("abcdefghijklmnopqrstuvwxyz", 8); got != "abcde..." {
		t.Fatalf("unexpected truncation output: %q", got)
	}
}
