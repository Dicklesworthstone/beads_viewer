//go:build linux || darwin

package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Unix shells receive the approved command as an argument, without an additional
// platform command-line encoding layer.
func configureHookShell(_ *exec.Cmd, _ string) {}

// runHookCommand owns a new process group for the complete hook lifetime. The
// caller must supply a fresh CommandContext and a positive WaitDelay, and must
// not wait for its process independently.
func runHookCommand(ctx context.Context, cmd *exec.Cmd) error {
	return runHookCommandObserved(ctx, cmd, waitHookCommandExit)
}

func runHookCommandObserved(ctx context.Context, cmd *exec.Cmd, observeExit func(int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.WaitDelay <= 0 {
		return errors.New("hook process requires a bounded pipe wait")
	}
	if cmd.Process != nil {
		return errors.New("hook process has already been started")
	}
	var attributes syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.Setpgid = true
	attributes.Pgid = 0 // The child becomes the leader of its own new group.
	cmd.SysProcAttr = &attributes

	// Start can launch CommandContext's cancellation goroutine before returning.
	// Serialize cancellation with ownership publication and final cleanup, and
	// disable it before Wait releases the leader's PID back to the operating
	// system. No negative-PID signal is ever sent after the leader is reaped.
	var groupMu sync.Mutex
	owned := false
	killGroup := func() error {
		if !owned || cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.Cancel = func() error {
		groupMu.Lock()
		defer groupMu.Unlock()
		return killGroup()
	}

	groupMu.Lock()
	if err := cmd.Start(); err != nil {
		groupMu.Unlock()
		return err
	}
	owned = true
	pid := cmd.Process.Pid
	groupMu.Unlock()

	// Observe exit without reaping. The live or zombie leader keeps its PID and
	// group identity reserved while we stop remaining children, including ones
	// whose output was redirected. Killing a group after Cmd.Wait would risk
	// signaling an unrelated process group if that numeric PID were reused.
	observeErr := observeExit(pid)
	groupMu.Lock()
	cleanupErr := killGroup()
	owned = false
	groupMu.Unlock()
	if errors.Is(cleanupErr, os.ErrProcessDone) {
		cleanupErr = nil
	}
	// Wait reaps the leader and drains captured output. WaitDelay also bounds
	// pipes retained by descendants that deliberately leave the process group;
	// group ownership is lifecycle management, not a process sandbox.
	waitErr := cmd.Wait()
	if observeErr != nil {
		observeErr = fmt.Errorf("observing hook process exit: %w", observeErr)
	}
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("terminating hook process group: %w", cleanupErr)
	}
	return errors.Join(ctx.Err(), observeErr, cleanupErr, waitErr)
}
