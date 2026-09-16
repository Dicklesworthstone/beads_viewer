//go:build !windows

package ui

// platformHasDarkBackground has no override on non-Windows platforms:
// termenv's own OSC 11 query (used via lipgloss) already handles background
// detection there.
func platformHasDarkBackground() (isDark bool, ok bool) {
	return false, false
}
