//go:build !windows

package ui

// platformHasDarkBackground has nothing to add away from Windows: termenv
// performs its own OSC 11 query there, so lipgloss.HasDarkBackground() is
// already answering from the real terminal background.
func platformHasDarkBackground() (isDark bool, ok bool) {
	return false, false
}
