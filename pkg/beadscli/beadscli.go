package beadscli

import (
	"fmt"
	"strings"
)

// current tracks the active Beads CLI for this process.
// It is internal runtime state, not a user-facing environment/config contract.
var current = "br"

// SetTool sets the active Beads CLI for generated helper commands.
func SetTool(tool string) {
	switch strings.TrimSpace(strings.ToLower(tool)) {
	case "bd":
		current = "bd"
	default:
		current = "br"
	}
}

// Tool returns the active command binary name.
func Tool() string {
	return current
}

// Shell formats a plain shell command for the active backend.
func Shell(format string, args ...any) string {
	return fmt.Sprintf(strings.ReplaceAll(format, "{tool}", Tool()), args...)
}

// CI formats a CI-prefixed shell command for the active backend.
func CI(format string, args ...any) string {
	return "CI=1 " + Shell(format, args...)
}
