//go:build linux || darwin

package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHookProcessGroupStopsDescendants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  bool
		redirect bool
		exitCode int
	}{
		{name: "success with inherited pipes"},
		{name: "success with redirected pipes", redirect: true},
		{name: "failed shell with descendants", exitCode: 7},
		{name: "timeout with inherited pipes", timeout: true},
		{name: "timeout with redirected pipes", timeout: true, redirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			command := "(printf started > child-ready; sleep 1; printf escaped > late-marker)"
			if tc.redirect {
				command += " >/dev/null 2>&1"
			}
			command += " & while [ ! -f child-ready ]; do sleep 0.01; done; printf parent-output; "
			timeout := 3 * time.Second
			if tc.timeout {
				command += "wait"
				timeout = 150 * time.Millisecond
			} else {
				command += fmt.Sprintf("exit %d", tc.exitCode)
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", command)
			cmd.Dir = dir
			cmd.WaitDelay = 250 * time.Millisecond
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			started := time.Now()
			err := runHookCommand(ctx, cmd)
			elapsed := time.Since(started)
			if tc.timeout {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected deadline failure, got %v", err)
				}
			} else if tc.exitCode != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exitCode {
					t.Fatalf("shell exit status was lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("successful parent failed: %v", err)
			}
			if elapsed >= 750*time.Millisecond {
				t.Errorf("hook waited for the one-second descendant: %v", elapsed)
			}
			if _, err := os.Stat(filepath.Join(dir, "child-ready")); err != nil {
				t.Fatalf("fixture child never started: %v", err)
			}
			if output.String() != "parent-output" {
				t.Errorf("parent output was lost: %q", output.String())
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && !tc.timeout {
				t.Fatalf("leader was not reaped: %v", cmd.ProcessState)
			}
			// Cancellation after reap must never signal a potentially reused PGID.
			if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("completed process group still accepts cancellation: %v", err)
			}
			remaining := time.Until(started.Add(1200 * time.Millisecond))
			if remaining > 0 {
				time.Sleep(remaining)
			}
			if _, err := os.Stat(filepath.Join(dir, "late-marker")); !os.IsNotExist(err) {
				t.Fatalf("descendant survived cleanup and wrote a delayed marker: %v", err)
			}
		})
	}
}

func TestHookExitObservationRetainsWaitStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 7")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Observe twice: the second registration targets a definitely exited,
	// unreaped leader (Darwin may report ESRCH rather than NOTE_EXIT).
	for i := 0; i < 2; i++ {
		if err := waitHookCommandExit(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("exit observation %d: %v", i+1, err)
		}
	}
	var exitErr *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("observation consumed or altered child wait status: %v", err)
	}
}

func TestHookProcessObserverFailureCleansUpBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "(printf ready > child-ready; sleep 1; printf escaped > late-marker) >/dev/null 2>&1 & wait")
	cmd.Dir = dir
	cmd.WaitDelay = 250 * time.Millisecond
	witness := errors.New("injected exit observer failure")
	started := time.Now()
	err := runHookCommandObserved(ctx, cmd, func(int) error {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "child-ready")); err == nil {
				return witness
			}
			time.Sleep(time.Millisecond)
		}
		return errors.New("fixture child did not start")
	})
	if !errors.Is(err, witness) {
		t.Fatalf("exit-observer failure was not reported: %v", err)
	}
	if time.Since(started) >= 750*time.Millisecond {
		t.Error("observer failure did not promptly terminate the owned group")
	}
	if cmd.ProcessState == nil {
		t.Fatal("failed observer left the leader unreaped")
	}
	time.Sleep(time.Until(started.Add(1200 * time.Millisecond)))
	if _, err := os.Stat(filepath.Join(dir, "late-marker")); !os.IsNotExist(err) {
		t.Fatalf("failed observer left a surviving descendant: %v", err)
	}
}

func TestHookProcessCancellationPreservesObserverFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 1")
	cmd.WaitDelay = 250 * time.Millisecond
	witness := errors.New("injected observer failure during cancellation")
	started := time.Now()
	err := runHookCommandObserved(ctx, cmd, func(int) error {
		cancel()
		return witness
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, witness) {
		t.Fatalf("cancellation hid the exit-observer failure: %v", err)
	}
	if time.Since(started) >= 750*time.Millisecond || cmd.ProcessState == nil {
		t.Fatalf("cancellation did not promptly reap the leader: %v", cmd.ProcessState)
	}
}

func TestHookProcessCancelledBeforeStartAndStartFailure(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "echo unexpected > cancelled-marker")
	cmd.Dir = dir
	cmd.WaitDelay = 250 * time.Millisecond
	if err := runHookCommand(ctx, cmd); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("cancelled command was started: process=%v error=%v", cmd.Process, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cancelled-marker")); !os.IsNotExist(err) {
		t.Fatalf("cancelled command created a marker: %v", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, filepath.Join(dir, "missing-executable"))
	cmd.WaitDelay = 250 * time.Millisecond
	if err := runHookCommand(ctx, cmd); err == nil || !strings.Contains(err.Error(), "missing-executable") {
		t.Fatalf("missing executable did not fail startup: %v", err)
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("failed startup published process-group ownership: %v", err)
	}
}
