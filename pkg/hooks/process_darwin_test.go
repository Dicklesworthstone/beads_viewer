package hooks

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestHookGroupExitedMembersOnlyAcceptsFinishedGroups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live := exec.CommandContext(ctx, "sleep", "30")
	live.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-live.Process.Pid, syscall.SIGKILL)
		_ = live.Wait()
	}()
	if hookGroupHasOnlyExitedMembers(live.Process.Pid) {
		t.Fatal("a group with a live member was reported as finished")
	}

	exited := exec.CommandContext(ctx, "true")
	exited.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := exited.Start(); err != nil {
		t.Fatal(err)
	}
	pid := exited.Process.Pid
	if err := waitHookCommandExit(pid); err != nil {
		_ = exited.Wait()
		t.Fatalf("observing exit: %v", err)
	}
	// The unreaped leader is the group's only member. Record the platform
	// behavior this helper exists for, then check the helper agrees.
	signalErr := syscall.Kill(-pid, syscall.SIGKILL)
	finished := hookGroupHasOnlyExitedMembers(pid)
	if err := exited.Wait(); err != nil {
		t.Fatalf("leader exit status: %v", err)
	}
	if signalErr != nil && !errors.Is(signalErr, syscall.EPERM) {
		t.Fatalf("unexpected group signal result for an exited leader: %v", signalErr)
	}
	if !finished {
		t.Fatal("a group holding only its exited leader was not reported as finished")
	}
}
