package agents

import (
	"os"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// canonicalRobotFlags is the authoritative sorted list of primary robot command
// flags. TestRobotRegistry_TotalContract in cmd/bv asserts 1:1 parity against
// RobotRegistry.PrimaryRobotFlagNames() so non-"--robot-*" commands (such as
// --bead-history) can never drift out of sync with early TTY suppression.
var canonicalRobotFlags = []string{
	"bead-history",
	"robot-alerts",
	"robot-blocker-chain",
	"robot-burndown",
	"robot-capabilities",
	"robot-capacity",
	"robot-causality",
	"robot-confirm-correlation",
	"robot-correlation-stats",
	"robot-diff",
	"robot-docs",
	"robot-drift",
	"robot-explain-correlation",
	"robot-file-beads",
	"robot-file-hotspots",
	"robot-file-relations",
	"robot-forecast",
	"robot-graph",
	"robot-help",
	"robot-history",
	"robot-impact",
	"robot-impact-network",
	"robot-insights",
	"robot-label-attention",
	"robot-label-flow",
	"robot-label-health",
	"robot-metrics",
	"robot-next",
	"robot-orphans",
	"robot-plan",
	"robot-priority",
	"robot-recipes",
	"robot-reject-correlation",
	"robot-related",
	"robot-schema",
	"robot-search",
	"robot-sprint-list",
	"robot-sprint-show",
	"robot-suggest",
	"robot-triage",
	"robot-triage-by-label",
	"robot-triage-by-track",
	"robot-whatif",
}

// canonicalHeadlessFlags are non-robot CLI flags that execute headlessly and
// exit before launching the interactive Bubble Tea TUI.
var canonicalHeadlessFlags = []string{
	"agent-brief",
	"agents-add",
	"agents-check",
	"agents-remove",
	"agents-update",
	"baseline-info",
	"check-drift",
	"check-update",
	"diff-since",
	"emit-script",
	"export-graph",
	"export-md",
	"export-pages",
	"feedback-accept",
	"feedback-ignore",
	"feedback-reset",
	"feedback-show",
	"generate-docs",
	"help",
	"hooks-info",
	"priority-brief",
	"rollback",
	"save-baseline",
	"search",
	"update",
	"version",
}

var nonInteractiveFlagSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(canonicalRobotFlags)+len(canonicalHeadlessFlags))
	for _, name := range canonicalRobotFlags {
		m[name] = struct{}{}
	}
	for _, name := range canonicalHeadlessFlags {
		m[name] = struct{}{}
	}
	return m
}()

// CanonicalRobotFlagNames returns a sorted copy of the primary robot command
// flag names suppressed by the early TTY guard.
func CanonicalRobotFlagNames() []string {
	out := append([]string(nil), canonicalRobotFlags...)
	sort.Strings(out)
	return out
}

// CanonicalHeadlessFlagNames returns a sorted copy of additional headless CLI
// flag names suppressed by the early TTY guard.
func CanonicalHeadlessFlagNames() []string {
	out := append([]string(nil), canonicalHeadlessFlags...)
	sort.Strings(out)
	return out
}

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
	if env.Get("CI") != "" {
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
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		if strings.HasPrefix(arg, "--robot-") {
			return true
		}
		flagName := strings.TrimPrefix(arg, "--")
		if eq := strings.IndexByte(flagName, '='); eq >= 0 {
			flagName = flagName[:eq]
		}
		if _, ok := nonInteractiveFlagSet[flagName]; ok {
			return true
		}
	}

	return false
}
