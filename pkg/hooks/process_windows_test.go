//go:build windows

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsHookHelperTest = "^TestWindowsHookProcessHelper$"

// TestWindowsHookProcessHelper is re-entered by this test executable. Helpers
// use only Go and local files, so these native regressions need neither network
// access nor locale-dependent Windows commands.
func TestWindowsHookProcessHelper(t *testing.T) {
	mode := os.Getenv("BV_HOOK_HELPER_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("BV_HOOK_HELPER_DIR")
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(21)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			fail(err)
		}
	}
	waitFor := func(name string) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		fail(fmt.Errorf("helper timed out waiting for %s", name))
	}

	switch mode {
	case "mark":
		write("executed", "unexpected")
	case "args":
		for i, arg := range os.Args {
			if arg == "--" {
				data, err := json.Marshal(os.Args[i+1:])
				if err != nil {
					fail(err)
				}
				write("args.json", string(data))
				fmt.Fprintln(os.Stdout, "helper-output")
				os.Exit(0)
			}
		}
		fail(errors.New("helper received no argument separator"))
	case "child":
		// Job termination must also stop children that ignore console signals.
		signal.Ignore(os.Interrupt)
		fmt.Fprintln(os.Stdout, "child-running")
		fmt.Fprintln(os.Stderr, "child-running")
		write("child.pid", strconv.Itoa(os.Getpid()))
		waitFor("child-release")
		write("child-side-effect", "child survived hook completion")
	case "parent-success", "parent-error", "parent-cancel", "parent-deadline":
		child := exec.Command(os.Args[0], "-test.run="+windowsHookHelperTest)
		child.Env = append(os.Environ(), "BV_HOOK_HELPER_MODE=child")
		if os.Getenv("BV_HOOK_CHILD_REDIRECT") == "" {
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
		}
		if err := child.Start(); err != nil {
			fail(err)
		}
		waitFor("child.pid")
		waitFor("parent-release")
		if mode == "parent-error" {
			os.Exit(7)
		}
	default:
		fail(fmt.Errorf("unknown helper mode %q", mode))
	}
	os.Exit(0)
}

func windowsHookHelperCommand(t *testing.T, ctx context.Context, mode, dir string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run="+windowsHookHelperTest)
	cmd.Env = append(os.Environ(), "BV_HOOK_HELPER_MODE="+mode, "BV_HOOK_HELPER_DIR="+dir, "BV_HOOK_CHILD_REDIRECT=")
	cmd.WaitDelay = hookWaitDelay
	return cmd
}

func waitForWindowsHookChild(t *testing.T, dir string, done <-chan error) windows.Handle {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("hook returned before its child became ready: %v", err)
		case <-deadline:
			t.Fatal("hook child did not become ready")
		case <-tick.C:
			data, err := os.ReadFile(filepath.Join(dir, "child.pid"))
			if err != nil {
				continue
			}
			pid, err := strconv.ParseUint(string(data), 10, 32)
			if err != nil || pid == 0 {
				continue // The small ready file may still be in the middle of a write.
			}
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
			if err != nil {
				t.Fatalf("open ready child: %v", err)
			}
			// Retain this exact process handle before releasing the parent; no
			// later PID lookup can accidentally inspect or kill a reused PID.
			t.Cleanup(func() {
				state, _ := windows.WaitForSingleObject(handle, 0)
				if state != windows.WAIT_OBJECT_0 {
					_ = windows.TerminateProcess(handle, 1)
					_, _ = windows.WaitForSingleObject(handle, 1000)
				}
				_ = windows.CloseHandle(handle)
			})
			return handle
		}
	}
}

func TestRunHookCommandWindowsCleansDescendants(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		redirect bool
	}{
		{mode: "parent-success"},
		{mode: "parent-error"},
		{mode: "parent-cancel"},
		{mode: "parent-deadline"},
		{mode: "parent-success", redirect: true},
		{mode: "parent-cancel", redirect: true},
	} {
		name := tc.mode
		if tc.redirect {
			name += "-redirected-child"
		}
		t.Run(name, func(t *testing.T) {
			mode := tc.mode
			dir := t.TempDir()
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "parent-deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			cmd := windowsHookHelperCommand(t, ctx, mode, dir)
			if tc.redirect {
				cmd.Env = append(cmd.Env, "BV_HOOK_CHILD_REDIRECT=1")
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			done := make(chan error, 1)
			go func() { done <- runHookCommand(ctx, cmd) }()
			child := waitForWindowsHookChild(t, dir, done)

			start := time.Now()
			var runErr error
			returned := false
			if mode == "parent-cancel" {
				cancel()
			} else if mode == "parent-deadline" {
				select {
				case <-ctx.Done():
				case runErr = <-done:
					returned = true
				}
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatalf("hook returned before its deadline: %v", runErr)
				}
				start = time.Now()
			} else if err := os.WriteFile(filepath.Join(dir, "parent-release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if !returned {
				select {
				case runErr = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("hook did not finish after parent exit/cancellation; descendant retained output pipes")
				}
			}
			if mode == "parent-success" && runErr != nil {
				t.Fatalf("successful parent failed while draining child pipes: %v", runErr)
			}
			if mode != "parent-success" && runErr == nil {
				t.Fatal("expected parent failure or cancellation")
			}
			if mode == "parent-error" {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 7 {
					t.Fatalf("parent exit status was not preserved: %v", runErr)
				}
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("hook cleanup exceeded bounded grace: %v", elapsed)
			}
			if !tc.redirect && (!strings.Contains(stdout.String(), "child-running") || !strings.Contains(stderr.String(), "child-running")) {
				t.Fatalf("fixture did not exercise both inherited output pipes: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if err := os.WriteFile(filepath.Join(dir, "child-release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := windows.WaitForSingleObject(child, 1500)
			if err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("descendant survived hook completion: state=%d err=%v", state, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "child-side-effect")); !os.IsNotExist(err) {
				t.Fatalf("descendant performed a side effect after hook completion: %v", err)
			}
		})
	}
}

func TestRunHookCommandWindowsCanceledContextDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := windowsHookHelperCommand(t, ctx, "mark", dir)
	if err := runHookCommand(ctx, cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before execution, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "executed")); !os.IsNotExist(err) {
		t.Fatalf("canceled command executed: %v", err)
	}
}

func TestExecutorWindowsPreservesQuotedCommandArguments(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	spacedExecutable := filepath.Join(dir, "hook helper with spaces.exe")
	if err := os.WriteFile(spacedExecutable, data, 0o700); err != nil {
		t.Fatal(err)
	}
	want := []string{"two words", "", "literal&value", "literal|value", "(parentheses)", "caret^value"}
	outputPath := filepath.Join(dir, "hook output with spaces.txt")
	completionPath := filepath.Join(dir, "hook completion with spaces.txt")
	command := `"` + spacedExecutable + `" -test.run=` + windowsHookHelperTest + ` -- "two words" "" "literal&value" "literal|value" "(parentheses)" "caret^value" > "` + outputPath + `" && echo completed > "` + completionPath + `"`
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{{
		Name: "quoted arguments", Command: command, Timeout: 5 * time.Second, OnError: "fail",
		Env: map[string]string{"BV_HOOK_HELPER_MODE": "args", "BV_HOOK_HELPER_DIR": dir},
	}}}}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err != nil {
		t.Fatalf("quoted hook command failed: %v; results=%+v", err, executor.Results())
	}
	data, err = os.ReadFile(filepath.Join(dir, "args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native cmd argument roundtrip: got %#v, want %#v", got, want)
	}
	for path, want := range map[string]string{outputPath: "helper-output", completionPath: "completed"} {
		data, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(data)) != want {
			t.Fatalf("shell redirection/&& result %s: got %q err=%v, want %q", path, data, err, want)
		}
	}
}
