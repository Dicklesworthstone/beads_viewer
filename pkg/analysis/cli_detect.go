package analysis

import (
	"fmt"
	"os/exec"
	"sync"
)

// Detect whether `br` (beads_rust) is installed and prefer it over `bd`.

var (
	detectOnce sync.Once
	hasBR      bool
)

func detectBR() {
	detectOnce.Do(func() {
		_, err := exec.LookPath("br")
		hasBR = err == nil
	})
}

// BeadsCLI returns "br" if installed, "bd" otherwise.
func BeadsCLI() string {
	detectBR()
	if hasBR {
		return "br"
	}
	return "bd"
}

// HasBR reports whether `br` is available on PATH.
func HasBR() bool {
	detectBR()
	return hasBR
}

// ClaimCommand returns the command to claim an issue.
func ClaimCommand(id string) string {
	if HasBR() {
		return fmt.Sprintf("br update %s --claim", id)
	}
	return fmt.Sprintf("bd update %s --status=in_progress", id)
}

// ClaimCommandJSON returns the claim command with JSON output.
func ClaimCommandJSON(id string) string {
	if HasBR() {
		return fmt.Sprintf("br update %s --claim --format json", id)
	}
	return fmt.Sprintf("CI=1 bd update %s --status in_progress --json", id)
}

// ShowCommand returns the command to show issue details.
func ShowCommand(id string) string {
	return fmt.Sprintf("%s show %s", BeadsCLI(), id)
}

// ShowCommandJSON returns the show command with JSON output.
func ShowCommandJSON(id string) string {
	if HasBR() {
		return fmt.Sprintf("br show %s --format json", id)
	}
	return fmt.Sprintf("CI=1 bd show %s --json", id)
}

// ReadyCommandJSON returns the ready command with JSON output.
func ReadyCommandJSON() string {
	if HasBR() {
		return "br ready --format json"
	}
	return "CI=1 bd ready --json"
}

// BlockedCommandJSON returns the blocked command with JSON output.
func BlockedCommandJSON() string {
	if HasBR() {
		return "br blocked --format json"
	}
	return "CI=1 bd blocked --json"
}
