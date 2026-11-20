//go:build windows

package instance

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isProcessAlive checks if a process with the given PID is still running.
// On Windows, this uses OpenProcess plus GetExitCodeProcess(STILL_ACTIVE),
// treating ERROR_ACCESS_DENIED as alive (process exists under another user or
// elevated integrity level).
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	// PROCESS_QUERY_LIMITED_INFORMATION is sufficient to check if process exists
	// and works even for processes running as a different user.
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	const stillActive uint32 = 259 // Windows STILL_ACTIVE exit code

	handle, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true
		}
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == stillActive
}
