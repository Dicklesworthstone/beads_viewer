package hooks

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Test fixtures are authored by the test, so explicitly approve their snapshot.
// Production callers must obtain this value through a separate user review.
func approvedOptions(t *testing.T, config *Config, grants ...string) ExecutionOptions {
	t.Helper()
	digest, err := config.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return ExecutionOptions{TrustDigest: digest, AllowEnv: grants}
}

func newApprovedExecutor(t *testing.T, config *Config, ctx ExportContext, grants ...string) *Executor {
	t.Helper()
	return NewExecutor(config, ctx, approvedOptions(t, config, grants...))
}

func TestExecutorDeniesUnapprovedHooksInEitherPhase(t *testing.T) {
	for _, phase := range []HookPhase{PreExport, PostExport} {
		for _, supplied := range []string{"", "true", "sha256:" + strings.Repeat("0", 64)} {
			t.Run(string(phase)+"/"+supplied, func(t *testing.T) {
				dir := t.TempDir()
				marker := filepath.Join(dir, "unapproved.txt")
				hook := Hook{Name: "unapproved", Command: `echo unexpected > "` + marker + `"`, OnError: "continue"}
				cfg := &Config{}
				if phase == PreExport {
					cfg.Hooks.PreExport = []Hook{hook}
				} else {
					cfg.Hooks.PostExport = []Hook{hook}
				}
				executor := NewExecutor(cfg, ExportContext{}, ExecutionOptions{TrustDigest: supplied})
				// Even preflight for a post-only config must reject untrusted code.
				if err := executor.RunPreExport(); !errors.Is(err, ErrUntrustedHooks) {
					t.Fatalf("pre-export authorization = %v", err)
				}
				if err := executor.RunPostExport(); !errors.Is(err, ErrUntrustedHooks) {
					t.Fatalf("post-export authorization = %v", err)
				}
				if len(executor.Results()) != 0 {
					t.Fatal("authorization denial must precede command execution")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("unapproved hook created a marker: %v", err)
				}
			})
		}
	}
}

func TestDigestCoversEveryExecutionField(t *testing.T) {
	base := &Config{Hooks: HooksByPhase{
		PreExport: []Hook{
			{Name: "first", Command: "echo first", Timeout: time.Second, OnError: "fail", Env: map[string]string{"COLOR": "blue"}},
			{Name: "second", Command: "echo second", Timeout: time.Second, OnError: "fail"},
		},
		PostExport: []Hook{{Name: "last", Command: "echo last", OnError: "continue"}},
	}}
	approved := approvedOptions(t, base)
	for name, mutate := range map[string]func(*Config){
		"command":      func(c *Config) { c.Hooks.PreExport[0].Command = "echo changed" },
		"name":         func(c *Config) { c.Hooks.PreExport[0].Name = "changed" },
		"timeout":      func(c *Config) { c.Hooks.PreExport[0].Timeout++ },
		"error policy": func(c *Config) { c.Hooks.PreExport[0].OnError = "continue" },
		"environment":  func(c *Config) { c.Hooks.PreExport[0].Env["COLOR"] = "red" },
		"order": func(c *Config) {
			c.Hooks.PreExport[0], c.Hooks.PreExport[1] = c.Hooks.PreExport[1], c.Hooks.PreExport[0]
		},
		"phase": func(c *Config) {
			c.Hooks.PostExport = append(c.Hooks.PostExport, c.Hooks.PreExport[0])
			c.Hooks.PreExport = c.Hooks.PreExport[1:]
		},
		"post command": func(c *Config) { c.Hooks.PostExport[0].Command = "echo changed" },
		"removed hook": func(c *Config) { c.Hooks.PreExport = c.Hooks.PreExport[1:] },
		"source bytes": func(c *Config) { c.sourceHash = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneConfig(base)
			mutate(changed)
			executor := NewExecutor(changed, ExportContext{}, approved)
			if err := executor.RunPreExport(); !errors.Is(err, ErrUntrustedHooks) {
				t.Fatalf("old approval accepted changed configuration: %v", err)
			}
		})
	}
}

func TestExecutorFreezesApprovedSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeHooksFile(t, dir, "hooks:\n  pre-export:\n    - command: echo original\n      env: {COLOR: blue}\n  post-export:\n    - command: echo original-post\n")
	loader := NewLoader(WithProjectDir(dir))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	config := loader.Config()
	options := approvedOptions(t, config)
	executor := NewExecutor(config, ExportContext{}, options)
	// Changes through public config pointers and a replacement on disk cannot
	// substitute new commands into an already authorized executor.
	config.Hooks.PreExport[0].Command = "echo changed"
	config.Hooks.PreExport[0].Env["COLOR"] = "red"
	config.Hooks.PostExport[0].Command = "echo changed-post"
	writeHooksFile(t, dir, "hooks:\n  pre-export:\n    - command: echo replaced\n")
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	if err := executor.RunPostExport(); err != nil {
		t.Fatal(err)
	}
	results := executor.Results()
	if len(results) != 2 || results[0].Stdout != "original" || results[1].Stdout != "original-post" || results[0].Hook.Env["COLOR"] != "blue" {
		t.Fatalf("approved snapshot was mutated: %+v", results)
	}
	results[0].Hook.Env["COLOR"] = "result mutation"
	if executor.Results()[0].Hook.Env["COLOR"] != "blue" || executor.config.Hooks.PreExport[0].Env["COLOR"] != "blue" {
		t.Fatal("results expose mutable execution state")
	}
	if next, err := RunHooks(dir, ExportContext{}, false, options); next != nil || !errors.Is(err, ErrUntrustedHooks) {
		t.Fatalf("reload accepted old approval: executor=%v err=%v", next, err)
	}
}

func TestRunHooksOptOutPrecedesLoadingAndAuthorization(t *testing.T) {
	dir := t.TempDir()
	writeHooksFile(t, dir, "invalid: [yaml")
	for _, project := range []string{dir, filepath.Join(dir, "does-not-exist")} {
		executor, err := RunHooks(project, ExportContext{}, true, ExecutionOptions{TrustDigest: "wrong", AllowEnv: []string{"TOKEN=value"}})
		if executor != nil || err != nil {
			t.Fatalf("--no-hooks must be unconditional: executor=%v err=%v", executor, err)
		}
	}
	if _, err := RunHooks(dir, ExportContext{}, false); err == nil {
		t.Fatal("invalid YAML must fail closed when hooks are enabled")
	}
}

func TestLoaderFailedReloadDiscardsPreviousConfig(t *testing.T) {
	dir := t.TempDir()
	writeHooksFile(t, dir, "hooks:\n  pre-export:\n    - command: echo approved\n")
	loader := NewLoader(WithProjectDir(dir))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	writeHooksFile(t, dir, "hooks: [")
	if err := loader.Load(); err == nil {
		t.Fatal("expected invalid YAML error")
	}
	if loader.HasHooks() || loader.Config().hasHooks() {
		t.Fatal("failed reload retained previously loaded commands")
	}
	if _, err := loader.Inspect(); err == nil {
		t.Fatal("failed reload retained inspectable approval")
	}
}

func TestDigestUsesRawYAMLAndStableMapOrder(t *testing.T) {
	dir := t.TempDir()
	data := "hooks:\n  pre-export:\n    - command: echo safe\n      env: {B: second, A: first}\n"
	writeHooksFile(t, dir, data)
	loader := NewLoader(WithProjectDir(dir))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	before := approvedOptions(t, loader.Config())
	for i := 0; i < 20; i++ {
		if got := approvedOptions(t, loader.Config()); got.TrustDigest != before.TrustDigest {
			t.Fatal("map iteration changed approval digest")
		}
	}
	writeHooksFile(t, dir, data+"# a changed file requires a new review\n")
	if _, err := RunHooks(dir, ExportContext{}, false, before); !errors.Is(err, ErrUntrustedHooks) {
		t.Fatalf("raw YAML edit kept previous approval: %v", err)
	}
}

func TestExecutorValidatesAllEnvironmentDeclarationsBeforeRunning(t *testing.T) {
	config := &Config{Hooks: HooksByPhase{
		PreExport:  []Hook{{Command: "echo must-not-run", OnError: "continue"}},
		PostExport: []Hook{{Command: "echo post", OnError: "continue", Env: map[string]string{"INVALID=KEY": "value"}}},
	}}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err == nil {
		t.Fatal("invalid post-export environment should fail preflight")
	}
	if len(executor.Results()) != 0 {
		t.Fatal("a hook ran before all declarations were validated")
	}
}

func TestShellInvocationPreservesCommandAndDisablesWindowsAutoRun(t *testing.T) {
	const command = `echo "spaces and quotes" && echo second`
	for _, tc := range []struct {
		platform string
		shell    string
		args     []string
	}{
		{"linux", "sh", []string{"-c", command}},
		{"darwin", "sh", []string{"-c", command}},
		{"windows", "cmd", []string{"/D", "/S", "/C", command}},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			shell, args := shellInvocation(tc.platform, command)
			if shell != tc.shell || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("shell invocation = %q %q, want %q %q", shell, args, tc.shell, tc.args)
			}
		})
	}
}

func TestExecutorRejectsPreviousLifetimePolicyApproval(t *testing.T) {
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{{Name: "reviewed", Command: "echo reviewed"}}}}
	// Frozen earlier approval encodings for this declaration. New execution
	// modes and declaration validation require review of the current policy.
	for _, policy := range []string{"bv-project-hooks-v1", "bv-project-hooks-v2"} {
		t.Run(policy, func(t *testing.T) {
			legacy := fmt.Sprintf(`{"policy":%q,"platform":%q,"source_hash":"","hooks":{"pre-export":[{"name":"reviewed","command":"echo reviewed"}]}}`, policy, runtime.GOOS)
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(legacy)))
			executor := NewExecutor(config, ExportContext{}, ExecutionOptions{TrustDigest: digest})
			if err := executor.RunPreExport(); !errors.Is(err, ErrUntrustedHooks) {
				t.Fatalf("previous execution policy still authorized hook execution: %v", err)
			}
			if len(executor.Results()) != 0 {
				t.Fatal("hook ran with an outdated policy approval")
			}
		})
	}
}
