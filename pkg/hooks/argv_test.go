package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const argvProbeTest = "^TestHookArgvSubprocessProbe$"

type argvProbeReceipt struct {
	Args       []string          `json:"args"`
	Directory  string            `json:"directory"`
	Executable string            `json:"executable"`
	Env        map[string]string `json:"env"`
}

// Re-execute the test binary directly so the argument oracle does not itself
// depend on shell quoting, an installed interpreter, or platform echo rules.
func TestHookArgvSubprocessProbe(t *testing.T) {
	if os.Getenv("BV_HOOK_ARGV_PROBE") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(41)
	}
	dir, err := os.Getwd()
	if err != nil {
		os.Exit(42)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(43)
	}
	receipt := argvProbeReceipt{
		Args:       append([]string{}, os.Args[separator+1:]...),
		Directory:  dir,
		Executable: executable,
		Env:        make(map[string]string),
	}
	for _, name := range []string{
		"BV_EXPORT_PATH", "BV_EXPORT_FORMAT", "BV_ISSUE_COUNT", "BV_TIMESTAMP",
		"BV_ARGV_FAKE_SECRET", "BV_ARGV_ALIAS", "BV_ARGV_CHAIN", "BV_ARGV_ORDINARY", "PATH",
	} {
		receipt.Env[name] = os.Getenv(name)
	}
	if marker := os.Getenv("BV_HOOK_ARGV_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("executed"), 0o600); err != nil {
			os.Exit(44)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(receipt); err != nil {
		os.Exit(45)
	}
	os.Exit(0)
}

func argvProbeHook(t *testing.T, arguments ...string) Hook {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Hook{
		Name:    "literal-arguments",
		Argv:    append([]string{executable, "-test.run=" + argvProbeTest, "--"}, arguments...),
		Timeout: 10 * time.Second,
		OnError: "fail",
		Env:     map[string]string{"BV_HOOK_ARGV_PROBE": "1"},
	}
}

func decodeArgvReceipt(t *testing.T, result HookResult) argvProbeReceipt {
	t.Helper()
	if !result.Success {
		t.Fatalf("argv probe failed: %v; stderr=%q", result.Error, result.Stderr)
	}
	var receipt argvProbeReceipt
	if err := json.Unmarshal([]byte(result.Stdout), &receipt); err != nil {
		t.Fatalf("invalid argv receipt: %v; stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	return receipt
}

func copyArgvProbeExecutable(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
}

func argvProbeName() string {
	if runtime.GOOS == "windows" {
		return "bv-argv-probe.exe"
	}
	return "bv-argv-probe"
}

func TestHookArgvYAMLRejectsMalformedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty array", "argv: []"},
		{"null array", "argv: null"},
		{"implicit null array", "argv:"},
		{"scalar", "argv: echo hello"},
		{"mapping", "argv: {program: echo}"},
		{"numeric argument", "argv: [echo, 7]"},
		{"boolean argument", "argv: [echo, true]"},
		{"null argument", "argv: [echo, null]"},
		{"nested sequence", "argv: [echo, [nested]]"},
		{"nested mapping", "argv: [echo, {key: value}]"},
		{"empty program", "argv: ['']"},
		{"NUL program", "argv: [\"bad\\0program\"]"},
		{"NUL argument", "argv: [echo, \"bad\\0argument\"]"},
		{"both forms", "command: echo shell\nargv: [echo, direct]"},
		{"whitespace command with argv", "command: ' '\nargv: [echo, direct]"},
		{"NUL command", "command: \"echo bad\\0value\""},
		{"duplicate argv", "argv: [echo, first]\nargv: [echo, second]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			declaration := strings.ReplaceAll(tc.data, "\n", "\n      ")
			writeHooksFile(t, dir, "hooks:\n  pre-export:\n    - "+declaration+"\n")
			loader := NewLoader(WithProjectDir(dir))
			if err := loader.Load(); err == nil {
				t.Fatal("malformed execution declaration was accepted")
			}
			if loader.HasHooks() {
				t.Fatal("failed load retained executable hooks")
			}
		})
	}
}

func TestHookArgvYAMLRetainsLiteralStringsAndLegacyEmptyCommands(t *testing.T) {
	dir := t.TempDir()
	writeHooksFile(t, dir, `hooks:
  pre-export:
    - command: ""
    - name: literal
      argv: [program, '', 'two words', '$VALUE', '${VALUE}', '%VALUE%', '*.json', 'a&b', 'a|b', 'a;b', 'a>b', '"quoted"', 'trailing\']
  post-export:
    - argv: [program, after]
`)
	loader := NewLoader(WithProjectDir(dir))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	want := []string{"program", "", "two words", "$VALUE", "${VALUE}", "%VALUE%", "*.json", "a&b", "a|b", "a;b", "a>b", `"quoted"`, `trailing\`}
	config := loader.Config()
	if len(config.Hooks.PreExport) != 1 || !reflect.DeepEqual(config.Hooks.PreExport[0].Argv, want) {
		t.Fatalf("literal argv changed during loading: %+v", config.Hooks.PreExport)
	}
	if config.Hooks.PreExport[0].Command != "" || config.Hooks.PreExport[0].Timeout != DefaultTimeout || config.Hooks.PreExport[0].OnError != "fail" {
		t.Fatalf("argv hook defaults were not applied: %+v", config.Hooks.PreExport[0])
	}
	if len(config.Hooks.PostExport) != 1 || config.Hooks.PostExport[0].OnError != "continue" {
		t.Fatalf("post-export default was lost: %+v", config.Hooks.PostExport)
	}
	if len(loader.Warnings()) != 1 || !strings.Contains(loader.Warnings()[0], "empty command") {
		t.Fatalf("legacy empty command was not omitted with its warning: %v", loader.Warnings())
	}
	inspection, err := loader.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Inspection
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip.Hooks.PreExport[0].Argv, want) {
		t.Fatalf("inspection lost argument boundaries: %s", data)
	}
	inspection.Hooks.PreExport[0].Argv[0] = "inspection-mutation"
	if config.Hooks.PreExport[0].Argv[0] != "program" {
		t.Fatal("inspection exposed mutable configuration argv")
	}
	if next, err := loader.Inspect(); err != nil || next.Digest != roundTrip.Digest {
		t.Fatalf("inspection mutation changed approval: digest=%q err=%v", next.Digest, err)
	}
}

func TestHookArgvYAMLAliasesPreserveLiteralArguments(t *testing.T) {
	var config Config
	if err := yaml.Unmarshal([]byte(`hooks:
  pre-export:
    - name: &program print-helper
      argv: &arguments [*program, 'two words', '$LITERAL']
  post-export:
    - argv: *arguments
`), &config); err != nil {
		t.Fatal(err)
	}
	want := []string{"print-helper", "two words", "$LITERAL"}
	if !reflect.DeepEqual(config.Hooks.PreExport[0].Argv, want) || !reflect.DeepEqual(config.Hooks.PostExport[0].Argv, want) {
		t.Fatalf("YAML aliases changed argument boundaries: %+v", config.Hooks)
	}
}

func TestHookArgvExecutionKeepsArgumentBoundariesAndEnvironment(t *testing.T) {
	t.Setenv("BV_ARGV_FAKE_SECRET", "fake-argv-sentinel")
	t.Setenv("BV_ARGV_ORDINARY", "ordinary-parent-value")
	for _, grant := range []bool{false, true} {
		t.Run(map[bool]string{false: "withheld", true: "granted"}[grant], func(t *testing.T) {
			dir := t.TempDir()
			arguments := []string{
				"", "two words", `"quoted"`, `a\b\\`, "line one\nline two", "λ Unicode",
				"$BV_EXPORT_PATH", "${BV_ARGV_FAKE_SECRET}", "%BV_EXPORT_PATH%", "*.json",
				"; echo wrong > shell-marker", "& echo wrong > shell-marker", "| echo wrong > shell-marker",
				"$(echo wrong > shell-marker)", "`echo wrong > shell-marker`", "> shell-marker", "a(b)c^d",
			}
			hook := argvProbeHook(t, arguments...)
			hook.Env["BV_ARGV_ALIAS"] = "${BV_ARGV_FAKE_SECRET}"
			hook.Env["BV_ARGV_CHAIN"] = "${BV_ARGV_ALIAS}"
			config := &Config{Hooks: HooksByPhase{PreExport: []Hook{hook}}, projectDir: dir}
			ctx := ExportContext{ExportPath: "report with spaces;and&syntax.md", ExportFormat: "markdown", IssueCount: 7, Timestamp: time.Unix(1700000000, 0).UTC()}
			grants := []string{"BV_ARGV_ORDINARY"}
			if grant {
				grants = append(grants, "BV_ARGV_FAKE_SECRET")
			}
			executor := newApprovedExecutor(t, config, ctx, grants...)
			if err := executor.RunPreExport(); err != nil {
				t.Fatal(err)
			}
			results := executor.Results()
			if len(results) != 1 {
				t.Fatalf("expected one execution, got %d", len(results))
			}
			receipt := decodeArgvReceipt(t, results[0])
			if !reflect.DeepEqual(receipt.Args, arguments) {
				t.Fatalf("arguments were interpreted or split:\nreceived %#v\nexpected %#v", receipt.Args, arguments)
			}
			for name, want := range map[string]string{
				"BV_EXPORT_PATH": ctx.ExportPath, "BV_EXPORT_FORMAT": "markdown", "BV_ISSUE_COUNT": "7",
				"BV_TIMESTAMP": ctx.Timestamp.Format(time.RFC3339), "BV_ARGV_ORDINARY": "ordinary-parent-value",
			} {
				if receipt.Env[name] != want {
					t.Errorf("child environment %s=%q, want %q", name, receipt.Env[name], want)
				}
			}
			wantSecret := ""
			if grant {
				wantSecret = "fake-argv-sentinel"
			}
			for _, name := range []string{"BV_ARGV_FAKE_SECRET", "BV_ARGV_ALIAS", "BV_ARGV_CHAIN"} {
				if receipt.Env[name] != wantSecret {
					t.Errorf("child credential %s=%q, want %q", name, receipt.Env[name], wantSecret)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "shell-marker")); !os.IsNotExist(err) {
				t.Fatalf("argument text executed shell syntax: %v", err)
			}
		})
	}
}

func TestHookArgvUnapprovedAndChangedDeclarationsCannotRun(t *testing.T) {
	dir := t.TempDir()
	hook := argvProbeHook(t, "reviewed", "")
	marker := filepath.Join(dir, "must-not-execute")
	hook.Env["BV_HOOK_ARGV_MARKER"] = marker
	base := &Config{Hooks: HooksByPhase{PreExport: []Hook{hook}}, projectDir: dir}
	options := approvedOptions(t, base)
	for name, mutate := range map[string]func(*Config){
		"program":                func(c *Config) { c.Hooks.PreExport[0].Argv[0] = "different-program" },
		"argument":               func(c *Config) { c.Hooks.PreExport[0].Argv[len(hook.Argv)-2] = "unreviewed" },
		"empty argument removed": func(c *Config) { c.Hooks.PreExport[0].Argv = c.Hooks.PreExport[0].Argv[:len(hook.Argv)-1] },
		"shell mode":             func(c *Config) { c.Hooks.PreExport[0].Argv = nil; c.Hooks.PreExport[0].Command = "echo unreviewed" },
	} {
		t.Run(name, func(t *testing.T) {
			config := cloneConfig(base)
			mutate(config)
			executor := NewExecutor(config, ExportContext{}, options)
			if err := executor.RunPreExport(); !errors.Is(err, ErrUntrustedHooks) {
				t.Fatalf("old approval authorized changed argv: %v", err)
			}
			if len(executor.Results()) != 0 {
				t.Fatal("changed argv produced an execution result")
			}
		})
	}
	for _, phase := range []HookPhase{PreExport, PostExport} {
		t.Run("unapproved "+string(phase), func(t *testing.T) {
			config := &Config{projectDir: dir}
			if phase == PreExport {
				config.Hooks.PreExport = []Hook{hook}
			} else {
				config.Hooks.PostExport = []Hook{hook}
			}
			executor := NewExecutor(config, ExportContext{})
			if err := executor.RunPreExport(); !errors.Is(err, ErrUntrustedHooks) {
				t.Fatalf("unapproved argv passed preflight: %v", err)
			}
			if err := executor.RunPostExport(); !errors.Is(err, ErrUntrustedHooks) {
				t.Fatalf("unapproved argv passed post-export: %v", err)
			}
		})
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied argv created a marker: %v", err)
	}
}

func TestHookArgvExecutorFreezesArgumentsAndResultSnapshots(t *testing.T) {
	pre := argvProbeHook(t, "original-pre")
	post := argvProbeHook(t, "original-post")
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{pre}, PostExport: []Hook{post}}}
	executor := newApprovedExecutor(t, config, ExportContext{})
	config.Hooks.PreExport[0].Argv[len(pre.Argv)-1] = "mutated-pre"
	config.Hooks.PostExport[0].Argv[len(post.Argv)-1] = "mutated-post"
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	results := executor.Results()
	if got := decodeArgvReceipt(t, results[0]).Args; !reflect.DeepEqual(got, []string{"original-pre"}) {
		t.Fatalf("approved argv changed through caller configuration: %q", got)
	}
	results[0].Hook.Argv[len(pre.Argv)-1] = "mutated-result"
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	if err := executor.RunPostExport(); err != nil {
		t.Fatal(err)
	}
	results = executor.Results()
	for i, want := range []string{"original-pre", "original-pre", "original-post"} {
		if got := decodeArgvReceipt(t, results[i]).Args; !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("execution %d changed through a mutable slice: %q, want %q", i, got, want)
		}
		if got := results[i].Hook.Argv[len(results[i].Hook.Argv)-1]; got != want {
			t.Errorf("result %d did not retain its approved argv: %q", i, got)
		}
	}
}

func TestHookArgvInvalidPostHookPreventsAllExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook Hook
	}{
		{"empty argv", Hook{Argv: []string{}}},
		{"empty program", Hook{Argv: []string{""}}},
		{"NUL program", Hook{Argv: []string{"program\x00suffix"}}},
		{"NUL argument", Hook{Argv: []string{"program", "argument\x00suffix"}}},
		{"invalid UTF-8 argument", Hook{Argv: []string{"program", "invalid-utf8-\xff"}}},
		{"both execution forms", Hook{Command: "echo shell", Argv: []string{"program"}}},
		{"whitespace command with argv", Hook{Command: " ", Argv: []string{"program"}}},
		{"NUL command", Hook{Command: "echo invalid\x00suffix"}},
		{"invalid UTF-8 command", Hook{Command: "echo invalid-utf8-\xff"}},
		{"invalid UTF-8 environment name", Hook{Argv: []string{"program"}, Env: map[string]string{"BAD\xff": "value"}}},
		{"invalid UTF-8 environment value", Hook{Argv: []string{"program"}, Env: map[string]string{"BAD": "value\xff"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "pre-hook-ran")
			pre := argvProbeHook(t, "should-not-run")
			pre.Env["BV_HOOK_ARGV_MARKER"] = marker
			invalid := tc.hook
			invalid.OnError = "continue"
			config := &Config{Hooks: HooksByPhase{PreExport: []Hook{pre}, PostExport: []Hook{invalid}}, projectDir: dir}
			executor := newApprovedExecutor(t, config, ExportContext{})
			if err := executor.RunPreExport(); err == nil {
				t.Fatal("invalid post-export execution declaration passed preflight")
			}
			if err := executor.RunPostExport(); err == nil {
				t.Fatal("on_error: continue bypassed execution validation")
			}
			if len(executor.Results()) != 0 {
				t.Fatal("a hook ran before every execution declaration was validated")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("pre-hook ran before an invalid post-hook was rejected: %v", err)
			}
		})
	}
}

func TestHookArgvRelativeExecutableUsesProjectDirectory(t *testing.T) {
	dir := t.TempDir()
	relative := filepath.Join("tools with spaces", argvProbeName())
	executable := filepath.Join(dir, relative)
	copyArgvProbeExecutable(t, executable)
	hook := argvProbeHook(t, "relative-executable")
	hook.Argv[0] = "." + string(filepath.Separator) + relative
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{hook}}, projectDir: dir}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	receipt := decodeArgvReceipt(t, executor.Results()[0])
	actual, err := os.Stat(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	project, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(actual, project) || !reflect.DeepEqual(receipt.Args, []string{"relative-executable"}) {
		t.Fatalf("relative executable used the caller directory: %+v", receipt)
	}
}

func TestHookArgvBareExecutableUsesParentPath(t *testing.T) {
	parentDir, overrideDir := t.TempDir(), t.TempDir()
	name := argvProbeName()
	parentExecutable := filepath.Join(parentDir, name)
	copyArgvProbeExecutable(t, parentExecutable)
	copyArgvProbeExecutable(t, filepath.Join(overrideDir, name))
	t.Setenv("PATH", parentDir)
	hook := argvProbeHook(t, "parent-path")
	hook.Argv[0] = name
	hook.Env["PATH"] = overrideDir
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{hook}}, projectDir: t.TempDir()}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	receipt := decodeArgvReceipt(t, executor.Results()[0])
	actual, err := os.Stat(receipt.Executable)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(parentExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(actual, expected) {
		t.Fatalf("project PATH replaced parent executable selection: %q", receipt.Executable)
	}
	if receipt.Env["PATH"] != overrideDir {
		t.Fatalf("ordinary child PATH override was lost: %q", receipt.Env["PATH"])
	}
}

func TestHookArgvImplicitCurrentDirectoryIsRejected(t *testing.T) {
	dir := t.TempDir()
	name := argvProbeName()
	copyArgvProbeExecutable(t, filepath.Join(dir, name))
	t.Chdir(dir)
	t.Setenv("PATH", ".")
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",execerrdot=1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd, err := newHookCommand(ctx, Hook{Argv: []string{name}}, dir)
	if err == nil && cmd != nil {
		err = cmd.Err
	}
	if !errors.Is(err, exec.ErrDot) {
		t.Fatalf("implicit current-directory executable was accepted: cmd=%v error=%v", cmd, err)
	}
}

func TestHookArgvDoesNotFallBackToShellForExecutableText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows batch resolution has dedicated coverage")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "text-without-shebang")
	if err := os.WriteFile(script, []byte("printf unexpected > shell-fallback-marker\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{{Name: "no-fallback", Argv: []string{script}, Timeout: time.Second, OnError: "fail"}}}, projectDir: dir}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err == nil {
		t.Fatal("executable text was silently interpreted by a shell")
	}
	if _, err := os.Stat(filepath.Join(dir, "shell-fallback-marker")); !os.IsNotExist(err) {
		t.Fatalf("failed direct execution ran a shell fallback: %v", err)
	}
}

func TestHookArgvWindowsDeclarationValidation(t *testing.T) {
	for _, tc := range []struct {
		program string
		valid   bool
	}{
		{"tool.exe", true},
		{`tools\tool.exe`, true},
		{`.\tools\tool.exe`, true},
		{`C:\tools\tool.exe`, true},
		{`\\server\share\tool.exe`, true},
		{"tool.cmd.exe", true},
		{"tool.cmd", false},
		{"tool.BaT", false},
		{`C:\tools\tool.CMD`, false},
		{`C:tool.exe`, false},
		{`C:`, false},
		{`\tools\tool.exe`, false},
		{`/tools/tool.exe`, false},
	} {
		t.Run(tc.program, func(t *testing.T) {
			err := validateHookExecution(Hook{Argv: []string{tc.program}}, "windows")
			if (err == nil) != tc.valid {
				t.Fatalf("Windows program %q validation=%v, want valid=%v", tc.program, err, tc.valid)
			}
		})
	}
}
