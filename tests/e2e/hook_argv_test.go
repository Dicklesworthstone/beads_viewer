package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type hookArgvProbeRecord struct {
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	WorkingDir   string            `json:"working_dir"`
	ExportExists bool              `json:"export_exists"`
}

// Build one native, dependency-free executable for the entire matrix. Inspecting
// its actual argv avoids relying on either shell's quoting or expansion rules.
func buildHookArgvProbe(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "helper with spaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "argv probe")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	source := filepath.Join(dir, "main.go")
	const code = `package main
import (
	"encoding/json"
	"fmt"
	"os"
)
func main() {
	values := make(map[string]string)
	for _, name := range []string{
		"BV_EXPORT_PATH", "BV_EXPORT_FORMAT", "BV_ISSUE_COUNT", "BV_TIMESTAMP",
		"BV_HOOK_ARGV_VISIBLE", "BV_HOOK_ARGV_TEST_SECRET", "BV_HOOK_ARGV_OTHER_TOKEN",
		"A_ALIAS", "B_CHAIN", "C_OTHER", "D_CONTEXT", "E_ORDINARY", "F_ORDINARY_CHAIN", "GITHUB_TOKEN",
	} {
		values[name] = os.Getenv(name)
	}
	cwd, err := os.Getwd()
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	_, exportErr := os.Stat(values["BV_EXPORT_PATH"])
	output, err := os.Create(os.Getenv("BV_ARGV_PROBE_OUTPUT"))
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	record := map[string]any{
		"args": os.Args[1:], "env": values, "working_dir": cwd,
		"export_exists": exportErr == nil,
	}
	if err := json.NewEncoder(output).Encode(record); err != nil {
		fmt.Fprintln(os.Stderr, err); os.Exit(1)
	}
	if err := output.Close(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", program, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build literal argv probe: %v\n%s", err, output)
	}
	return program
}

func argvProbeHook(program, output string, args ...string) map[string]any {
	return map[string]any{
		"name": "literal argv probe",
		"argv": append([]string{program}, args...),
		"env":  map[string]string{"BV_ARGV_PROBE_OUTPUT": output},
	}
}

func writeArgvHookFixture(t *testing.T, dir string, pre, post []map[string]any) {
	t.Helper()
	// JSON flow values are also YAML, including correctly escaped Windows paths,
	// empty arguments, embedded newlines, and shell metacharacters.
	data, err := json.MarshalIndent(map[string]any{
		"hooks": map[string]any{"pre-export": pre, "post-export": post},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeHookFixture(t, dir, string(data))
}

func readHookArgvProbe(t *testing.T, path string) hookArgvProbeRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv probe: %v", err)
	}
	var record hookArgvProbeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode argv probe: %v\n%s", err, data)
	}
	return record
}

func TestHookArgvExports(t *testing.T) {
	program := buildHookArgvProbe(t)
	stageViewerAssets(t, buildBvBinary(t))
	const secret = "fake-argv-primary-secret-sentinel"
	const otherSecret = "fake-argv-secondary-secret-sentinel"
	t.Setenv("BV_HOOK_ARGV_TEST_SECRET", secret)
	t.Setenv("BV_HOOK_ARGV_OTHER_TOKEN", otherSecret)
	t.Setenv("BV_HOOK_ARGV_VISIBLE", "ordinary-argv-value")
	literalArgs := []string{
		"", "two words", "$BV_HOOK_ARGV_VISIBLE", "${BV_HOOK_ARGV_VISIBLE}",
		"%BV_HOOK_ARGV_VISIBLE%", "argv-glob-*", "semi;colon", "and&&or||pipe|",
		"$(printf expanded)", "`printf expanded`", "> redirection-marker",
		`quote"inside`, "apostrophe'inside", `trailing\`, "line\nbreak", "雪",
	}
	for _, kind := range []string{"markdown", "pages"} {
		t.Run(kind, func(t *testing.T) {
			for _, phase := range []string{"pre-export", "post-export"} {
				t.Run("unapproved/"+phase, func(t *testing.T) {
					dir := hookProject(t)
					marker := filepath.Join(dir, "unapproved.json")
					hook := argvProbeHook(program, marker)
					hook["on_error"] = "continue"
					var pre, post []map[string]any
					if phase == "pre-export" {
						pre = []map[string]any{hook}
					} else {
						post = []map[string]any{hook}
					}
					writeArgvHookFixture(t, dir, pre, post)
					output := filepath.Join(dir, "unapproved-export")
					args := append(hookExportArgs(kind, output), "--yes", "--hook-env", "BV_HOOK_ARGV_TEST_SECRET")
					stdout, stderr, err := runHookBV(t, dir, args...)
					if err == nil || !strings.Contains(stdout+stderr, "not trusted") {
						t.Fatalf("argv hooks escaped noninteractive approval: %v\n%s\n%s", err, stdout, stderr)
					}
					requireHookFileAbsent(t, marker)
					requireHookFileAbsent(t, output)
				})
			}

			t.Run("approved_literal_arguments_and_context", func(t *testing.T) {
				dir := hookProject(t)
				if err := os.WriteFile(filepath.Join(dir, "argv-glob-match"), []byte("bounded fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				preOutput := filepath.Join(dir, "pre.json")
				postOutput := filepath.Join(dir, "post.json")
				writeArgvHookFixture(t, dir,
					[]map[string]any{argvProbeHook(program, preOutput, literalArgs...)},
					[]map[string]any{argvProbeHook(program, postOutput, literalArgs...)})
				stdout, stderr, err := runHookBV(t, dir, "--hooks-info")
				if err != nil {
					t.Fatalf("inspect literal argv: %v\n%s\n%s", err, stdout, stderr)
				}
				var info struct {
					Digest string `json:"digest"`
					Hooks  map[string][]struct {
						Argv []string `json:"argv"`
					} `json:"hooks"`
				}
				if err := json.Unmarshal([]byte(stdout), &info); err != nil {
					t.Fatalf("decode literal argv inspection: %v", err)
				}
				for _, phase := range []string{"pre-export", "post-export"} {
					inspected := info.Hooks[phase]
					if len(inspected) != 1 || !reflect.DeepEqual(inspected[0].Argv, append([]string{program}, literalArgs...)) {
						t.Fatalf("review omitted or expanded %s arguments: %+v", phase, inspected)
					}
				}
				requireHookFileAbsent(t, preOutput)
				requireHookFileAbsent(t, postOutput)
				output := filepath.Join(dir, "approved-export")
				args := append(hookExportArgs(kind, output), "--trust-hooks", info.Digest)
				stdout, stderr, err = runHookBV(t, dir, args...)
				if err != nil {
					t.Fatalf("approved literal argv export failed: %v\n%s\n%s", err, stdout, stderr)
				}
				format := kind
				if kind == "pages" {
					format = "html"
				}
				for _, phase := range []struct {
					path         string
					exportExists bool
				}{{preOutput, false}, {postOutput, true}} {
					record := readHookArgvProbe(t, phase.path)
					if !reflect.DeepEqual(record.Args, literalArgs) {
						t.Errorf("argv was expanded, split, or requoted:\ngot  %#v\nwant %#v", record.Args, literalArgs)
					}
					if record.ExportExists != phase.exportExists {
						t.Errorf("hook ran at wrong export phase: %+v", record)
					}
					for key, want := range map[string]string{
						"BV_EXPORT_PATH": output, "BV_EXPORT_FORMAT": format,
						"BV_ISSUE_COUNT": "1", "BV_HOOK_ARGV_VISIBLE": "",
					} {
						if record.Env[key] != want {
							t.Errorf("context %s = %q, want %q", key, record.Env[key], want)
						}
					}
					if _, err := time.Parse(time.RFC3339, record.Env["BV_TIMESTAMP"]); err != nil {
						t.Errorf("argv hook lost timestamp context: %v", err)
					}
					gotDir, err := os.Stat(record.WorkingDir)
					wantDir, wantErr := os.Stat(dir)
					if err != nil || wantErr != nil || !os.SameFile(gotDir, wantDir) {
						t.Errorf("argv hook working directory = %q, want project %q", record.WorkingDir, dir)
					}
				}
				requireHookFileAbsent(t, filepath.Join(dir, "redirection-marker"))
			})

			for _, change := range []string{"argument", "order", "program", "shell mode"} {
				t.Run("changed_approval/"+change, func(t *testing.T) {
					dir := hookProject(t)
					marker := filepath.Join(dir, "changed.json")
					hook := argvProbeHook(program, marker, "first", "second")
					hook["on_error"] = "continue"
					writeArgvHookFixture(t, dir, []map[string]any{hook}, nil)
					digest := reviewedHookDigest(t, dir)
					switch change {
					case "argument":
						hook["argv"] = []string{program, "changed", "second"}
					case "order":
						hook["argv"] = []string{program, "second", "first"}
					case "program":
						hook["argv"] = []string{program + "-changed", "first", "second"}
					case "shell mode":
						delete(hook, "argv")
						hook["command"] = "echo unexpected > changed-shell-marker"
					}
					writeArgvHookFixture(t, dir, []map[string]any{hook}, nil)
					output := filepath.Join(dir, "changed-export")
					args := append(hookExportArgs(kind, output), "--trust-hooks", digest)
					stdout, stderr, err := runHookBV(t, dir, args...)
					if err == nil || !strings.Contains(stdout+stderr, "not trusted") {
						t.Fatalf("changed argv reused stale approval: %v\n%s\n%s", err, stdout, stderr)
					}
					requireHookFileAbsent(t, marker)
					requireHookFileAbsent(t, filepath.Join(dir, "changed-shell-marker"))
					requireHookFileAbsent(t, output)
				})
			}

			for _, invalid := range []struct {
				name string
				hook map[string]any
			}{
				{"empty program", map[string]any{"argv": []string{""}}},
				{"non-string argument", map[string]any{"argv": []any{program, 42}}},
				{"both forms", map[string]any{"argv": []string{program}, "command": "echo unexpected"}},
			} {
				t.Run("invalid_post_hook_blocks_entire_pipeline/"+invalid.name, func(t *testing.T) {
					dir := hookProject(t)
					marker := filepath.Join(dir, "earlier-pre-hook.json")
					invalid.hook["on_error"] = "continue"
					writeArgvHookFixture(t, dir,
						[]map[string]any{argvProbeHook(program, marker)}, []map[string]any{invalid.hook})
					stdout, stderr, err := runHookBV(t, dir, "--hooks-info")
					if err == nil || !strings.Contains(stdout+stderr, "argv") {
						t.Fatalf("invalid argv was reviewable: %v\n%s\n%s", err, stdout, stderr)
					}
					output := filepath.Join(dir, "invalid-export")
					args := append(hookExportArgs(kind, output), "--trust-hooks", "sha256:"+strings.Repeat("a", 64))
					stdout, stderr, err = runHookBV(t, dir, args...)
					if err == nil || !strings.Contains(stdout+stderr, "argv") {
						t.Fatalf("invalid argv pipeline was accepted: %v\n%s\n%s", err, stdout, stderr)
					}
					requireHookFileAbsent(t, marker)
					requireHookFileAbsent(t, output)
				})
			}

			for _, invalid := range []bool{false, true} {
				name := "valid"
				if invalid {
					name = "invalid"
				}
				t.Run("no_hooks/"+name, func(t *testing.T) {
					dir := hookProject(t)
					marker := filepath.Join(dir, "disabled.json")
					hook := argvProbeHook(program, marker)
					if invalid {
						hook["command"] = "echo unexpected > disabled-shell-marker"
					}
					writeArgvHookFixture(t, dir, []map[string]any{hook}, nil)
					output := filepath.Join(dir, "without-hooks")
					args := append(hookExportArgs(kind, output), "--no-hooks", "--trust-hooks", "invalid", "--hook-env", "*=invalid")
					stdout, stderr, err := runHookBV(t, dir, args...)
					if err != nil {
						t.Fatalf("--no-hooks did not bypass argv configuration: %v\n%s\n%s", err, stdout, stderr)
					}
					if _, err := os.Stat(output); err != nil {
						t.Fatalf("hook-free export missing: %v", err)
					}
					requireHookFileAbsent(t, marker)
					requireHookFileAbsent(t, filepath.Join(dir, "disabled-shell-marker"))
				})
			}

			for _, grant := range []struct {
				name         string
				keys         []string
				wantMain     string
				wantOther    string
				wantOrdinary string
			}{
				{"none", nil, "", "", ""},
				{"alias is not source consent", []string{"A_ALIAS", "E_ORDINARY"}, "", "", ""},
				{"primary source only", []string{"BV_HOOK_ARGV_TEST_SECRET"}, secret, "", ""},
				{"both sources", []string{"BV_HOOK_ARGV_TEST_SECRET", "BV_HOOK_ARGV_OTHER_TOKEN"}, secret, otherSecret, ""},
				{"ordinary-looking source requires grant", []string{"BV_HOOK_ARGV_VISIBLE"}, "", "", "ordinary-argv-value"},
			} {
				t.Run("credential_grants/"+grant.name, func(t *testing.T) {
					dir := hookProject(t)
					marker := filepath.Join(dir, "environment.json")
					hook := argvProbeHook(program, marker, "${BV_HOOK_ARGV_TEST_SECRET}")
					env := hook["env"].(map[string]string)
					env["A_ALIAS"] = "${BV_HOOK_ARGV_TEST_SECRET}"
					env["B_CHAIN"] = "${A_ALIAS}"
					env["C_OTHER"] = "${BV_HOOK_ARGV_OTHER_TOKEN}"
					env["D_CONTEXT"] = "${BV_EXPORT_PATH}"
					env["E_ORDINARY"] = "${BV_HOOK_ARGV_VISIBLE}"
					env["F_ORDINARY_CHAIN"] = "${E_ORDINARY}"
					env["GITHUB_TOKEN"] = "${BV_HOOK_ARGV_TEST_SECRET}"
					writeArgvHookFixture(t, dir, []map[string]any{hook}, nil)
					digest := reviewedHookDigest(t, dir)
					output := filepath.Join(dir, "credential-export")
					args := append(hookExportArgs(kind, output), "--trust-hooks", digest)
					for _, key := range grant.keys {
						args = append(args, "--hook-env", key)
					}
					stdout, stderr, err := runHookBV(t, dir, args...)
					if err != nil {
						t.Fatalf("credential grant export failed: %v\n%s\n%s", err, stdout, stderr)
					}
					record := readHookArgvProbe(t, marker)
					if !reflect.DeepEqual(record.Args, []string{"${BV_HOOK_ARGV_TEST_SECRET}"}) {
						t.Errorf("credential source expanded inside literal argv: %#v", record.Args)
					}
					for key, want := range map[string]string{
						"BV_HOOK_ARGV_TEST_SECRET": grant.wantMain, "A_ALIAS": grant.wantMain,
						"B_CHAIN": grant.wantMain, "GITHUB_TOKEN": grant.wantMain,
						"BV_HOOK_ARGV_OTHER_TOKEN": grant.wantOther, "C_OTHER": grant.wantOther,
						"D_CONTEXT": output, "BV_HOOK_ARGV_VISIBLE": grant.wantOrdinary,
						"E_ORDINARY": grant.wantOrdinary, "F_ORDINARY_CHAIN": grant.wantOrdinary,
					} {
						if record.Env[key] != want {
							t.Errorf("argv environment %s = %q, want %q", key, record.Env[key], want)
						}
					}
					if strings.Contains(stdout+stderr, secret) || strings.Contains(stdout+stderr, otherSecret) {
						t.Errorf("argv environment leaked credential values into diagnostics")
					}
				})
			}

			t.Run("no_implicit_shell_fallback", func(t *testing.T) {
				dir := hookProject(t)
				file := filepath.Join(dir, "not-a-native-executable")
				contents := "printf unexpected > fallback-marker\n"
				if runtime.GOOS == "windows" {
					file += ".cmd"
					contents = "@echo unexpected > fallback-marker\r\n"
				}
				if err := os.WriteFile(file, []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
				writeArgvHookFixture(t, dir, []map[string]any{{"argv": []string{file}}}, nil)
				digest := "sha256:" + strings.Repeat("a", 64)
				if runtime.GOOS == "windows" {
					stdout, stderr, err := runHookBV(t, dir, "--hooks-info")
					if err == nil || !strings.Contains(stdout+stderr, "batch files") {
						t.Fatalf("implicit Windows batch execution was reviewable: %v\n%s\n%s", err, stdout, stderr)
					}
				} else {
					digest = reviewedHookDigest(t, dir)
				}
				output := filepath.Join(dir, "fallback-export")
				args := append(hookExportArgs(kind, output), "--trust-hooks", digest)
				stdout, stderr, err := runHookBV(t, dir, args...)
				if err == nil {
					t.Fatalf("argv silently used a shell fallback\n%s\n%s", stdout, stderr)
				}
				requireHookFileAbsent(t, filepath.Join(dir, "fallback-marker"))
				requireHookFileAbsent(t, output)
			})
		})
	}
}
