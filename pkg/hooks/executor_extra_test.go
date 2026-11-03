package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunPreExportTimeout ensures hook timeouts fail pre-export with on_error=fail.
func TestRunPreExportTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timeout fixture uses the Unix sleep command")
	}
	cfg := &Config{
		Hooks: HooksByPhase{
			PreExport: []Hook{
				{
					Name:    "slow",
					Command: "sleep 1",
					Timeout: 10 * time.Millisecond,
					OnError: "fail",
				},
			},
		},
	}
	ex := newApprovedExecutor(t, cfg, ExportContext{})
	err := ex.RunPreExport()
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	results := ex.Results()
	if len(results) != 1 || results[0].Error == nil {
		t.Fatalf("expected result with error")
	}
	if !strings.Contains(results[0].Error.Error(), "timeout") {
		t.Fatalf("expected a timeout rather than an unrelated failure, got %v", results[0].Error)
	}
}

// TestRunPostExportContinue verifies post-export honors on_error=continue (default).
func TestRunPostExportContinue(t *testing.T) {
	cfg := &Config{
		Hooks: HooksByPhase{
			PostExport: []Hook{
				{
					Name:    "fail-ok",
					Command: "exit 1",
					Timeout: 500 * time.Millisecond,
					// OnError empty => default continue
				},
			},
		},
	}
	ex := newApprovedExecutor(t, cfg, ExportContext{})
	if err := ex.RunPostExport(); err != nil {
		t.Fatalf("post-export should not surface error, got %v", err)
	}
	res := ex.Results()
	if len(res) != 1 || res[0].Success {
		t.Fatalf("expected failed hook recorded, got %+v", res)
	}
}

// TestSummaryEmpty ensures summary handles no hooks run.
func TestSummaryEmpty(t *testing.T) {
	ex := NewExecutor(nil, ExportContext{})
	if s := ex.Summary(); s != "No hooks executed" {
		t.Fatalf("unexpected summary: %s", s)
	}
}

// TestLoadDefaultMissingFile loads from temp dir with no hooks file.
func TestLoadDefaultMissingFile(t *testing.T) {
	tmp := t.TempDir()
	// No .bv/hooks.yaml
	loader := NewLoader(WithProjectDir(tmp))
	if err := loader.Load(); err != nil {
		t.Fatalf("expected nil error when hooks.yaml missing: %v", err)
	}
	if loader.HasHooks() {
		t.Fatalf("expected no hooks when file missing")
	}
	if len(loader.Warnings()) != 0 {
		t.Fatalf("expected no warnings for missing file")
	}
}

// TestNormalizeDropsEmptyCommand ensures empty commands are skipped with warning.
func TestNormalizeDropsEmptyCommand(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".bv"), 0755); err != nil {
		t.Fatalf("mkdir .bv: %v", err)
	}
	configPath := filepath.Join(tmp, ".bv", "hooks.yaml")
	data := []byte(`
hooks:
  pre-export:
    - { command: "" }
  post-export:
    - { command: "echo ok" }
`)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write hooks.yaml: %v", err)
	}

	loader := NewLoader(WithProjectDir(tmp))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loader.Config().Hooks.PreExport) != 0 {
		t.Fatalf("empty pre-export hook should be removed")
	}
	if len(loader.Config().Hooks.PostExport) != 1 {
		t.Fatalf("expected one post-export hook")
	}
	if len(loader.Warnings()) == 0 {
		t.Fatalf("expected warning about empty command")
	}
}
