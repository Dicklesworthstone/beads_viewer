//go:build !linux && !darwin && !windows

package hooks

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func configureHookShell(_ *exec.Cmd, _ string) {}

// Unsupported platforms can still export with --no-hooks. Do not silently run
// approved commands without the process-lifetime guarantees of release targets.
func runHookCommand(_ context.Context, _ *exec.Cmd) error {
	return fmt.Errorf("hook process lifecycle is unsupported on %s; use --no-hooks", runtime.GOOS)
}
