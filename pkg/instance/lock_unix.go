//go:build !windows

package instance

import (
	"os"
	"syscall"
)

func defaultSignalProcessZero(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.Signal(0))
}

// isProcessAlive checks if a process with the given PID is still running.
// On Unix systems, this uses signal 0 which checks process existence
// without actually sending a signal; EPERM indicates the process exists
// under a different UID.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	lockHooksMu.RLock()
	override := signalZeroOverride
	lockHooksMu.RUnlock()

	if override != nil {
		return classifySignalZeroErr(override(pid))
	}
	return classifySignalZeroErr(defaultSignalProcessZero(pid))
}
