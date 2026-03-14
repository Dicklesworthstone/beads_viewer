package beadscli

import (
	"fmt"
	"os"
	"strings"
)

const backendEnvVar = "BV_BEADS_CLI"

// Current returns the active command backend for user-facing helper commands.
// Defaults to br for legacy stacks unless main configures a bd workspace.
func Current() string {
	backend := strings.TrimSpace(strings.ToLower(os.Getenv(backendEnvVar)))
	if backend == "bd" {
		return "bd"
	}
	return "br"
}

// Tool returns the active command binary name.
func Tool() string {
	return Current()
}

// Shell formats a plain shell command for the active backend.
func Shell(format string, args ...any) string {
	return fmt.Sprintf(strings.ReplaceAll(format, "{tool}", Tool()), args...)
}

// CI formats a CI-prefixed shell command for the active backend.
func CI(format string, args ...any) string {
	return "CI=1 " + Shell(format, args...)
}
