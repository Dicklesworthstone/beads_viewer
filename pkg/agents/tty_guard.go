package agents

import (
	"os"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// init runs before Bubble Tea acquires the terminal (and before any TUI starts).
//
// In some PTY/TTY capture environments (notably agent runners), Bubble Tea's init
// triggers Lipgloss/Termenv background detection, which can emit OSC/DSR control
// sequences to stdout. Those sequences are harmless in a real terminal but can
// break JSON parsers consuming robot-mode output.
//
// We treat robot-mode invocations as non-interactive and set CI=1 early. Termenv
// uses CI to disable TTY probing, preventing those sequences from being written.
func init() {
	if os.Getenv("CI") != "" {
		return
	}

	if !shouldSuppressTTYQueries(os.Args, env.Robot.Bool(), env.TestMode.Get() != "") {
		return
	}

	_ = os.Setenv("CI", "1")
}

func shouldSuppressTTYQueries(args []string, envRobot, envTest bool) bool {
	if envRobot || envTest {
		return true
	}

	for _, arg := range args {
		if strings.HasPrefix(arg, "--robot-") {
			return true
		}
		switch arg {
		case "--version", "--help":
			return true
		}
	}

	return false
}
