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
	"robot-cass",
	"robot-causality",
	"robot-check-dep",
	"robot-commit",
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
	"robot-ignore-correlation",
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
	"agents-dry-run",
	"agents-force",
	"agents-remove",
	"agents-reset",
	"agents-update",
	"baseline-info",
	"check-drift",
	"check-update",
	"debug-render",
	"diff-since",
	"emit-script",
	"export",
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
	"init-drift",
	"preview-pages",
	"priority-brief",
	"profile-startup",
	"rollback",
	"save-baseline",
	"search",
	"update",
	"update-dry-run",
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

var nonInteractiveSubcommandSet = map[string]struct{}{
	"alerts": {}, "analysis": {}, "analyze": {}, "bead-history": {},
	"blocker-chain": {}, "blockers": {}, "burndown": {}, "capabilities": {},
	"capability": {}, "capacity": {}, "cass": {}, "causality": {}, "changes": {}, "check-dep": {},
	"commit": {}, "diff": {}, "doc": {}, "docs": {}, "drift": {},
	"file-beads": {}, "file-hotspots": {}, "file-relations": {}, "find": {},
	"forecast": {}, "graph": {}, "help": {}, "history": {},
	"hotspots": {}, "impact": {}, "impact-network": {}, "insight": {},
	"insights": {}, "label-attention": {}, "label-flow": {}, "label-health": {},
	"labels": {}, "manifest": {}, "metrics": {}, "next": {},
	"orphans": {}, "pick": {}, "plan": {}, "priorities": {},
	"priority": {}, "recipes": {}, "recommend": {}, "recommendations": {},
	"related": {}, "schema": {}, "schemas": {}, "search": {},
	"self-update": {}, "selfupdate": {}, "sessions": {}, "sprint": {}, "sprint-list": {},
	"sprint-show": {}, "sprints": {}, "suggest": {}, "suggestions": {},
	"triage": {}, "upgrade": {}, "version": {}, "what-if": {}, "whatif": {},
}

func isStructuredFormatValue(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "json", "toon":
		return true
	default:
		return false
	}
}

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
	if !shouldSuppressTTYQueries(os.Args, env.Robot.Bool(), env.TestMode.Get() != "") {
		return
	}

	if env.Get("CI") == "" {
		_ = os.Setenv("CI", "1")
	}
	if env.NoBackgroundQuery.Get() == "" {
		_ = os.Setenv(env.NoBackgroundQuery.Name, "1")
	}
}

func isPositiveBoolFlagValue(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func isNegatedBoolFlagValue(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "false", "0", "no", "off":
		return true
	default:
		return false
	}
}

var valueTakingTUIFlagSet = map[string]struct{}{
	"as-of": {}, "cpu-profile": {}, "db": {}, "debug-height": {},
	"debug-width": {}, "history-limit": {}, "history-since": {},
	"id-pattern": {}, "label": {}, "l": {}, "recipe": {}, "r": {},
	"repo": {}, "search-mode": {}, "search-preset": {},
	"search-weights": {}, "theme": {}, "workspace": {},
}

func shouldSuppressTTYQueries(args []string, envRobot, envTest bool) bool {
	if envRobot || envTest {
		return true
	}

	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		sub := strings.ToLower(strings.TrimSpace(args[1]))
		if strings.HasPrefix(sub, "robot-") {
			return true
		}
		if _, ok := nonInteractiveSubcommandSet[sub]; ok {
			return true
		}
	}

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		if arg == "-h" || arg == "-v" {
			return true
		}
		if (arg == "--output" || arg == "-o" || arg == "--format" || arg == "-f") && i+1 < len(args) {
			if isStructuredFormatValue(args[i+1]) {
				return true
			}
			i++
			continue
		}

		flagToken := strings.TrimLeft(arg, "-")
		flagName := flagToken
		flagVal := ""
		hasEq := false
		if eq := strings.IndexByte(flagToken, '='); eq >= 0 {
			flagName = flagToken[:eq]
			flagVal = flagToken[eq+1:]
			hasEq = true
		}
		lowerName := strings.ToLower(flagName)
		if !hasEq {
			if _, takesVal := valueTakingTUIFlagSet[lowerName]; takesVal && i+1 < len(args) {
				i++
				continue
			}
		}

		if strings.HasPrefix(lowerName, "robot-") {
			if hasEq && isNegatedBoolFlagValue(flagVal) {
				continue
			}
			return true
		}
		if _, ok := nonInteractiveFlagSet[lowerName]; ok {
			if hasEq && isNegatedBoolFlagValue(flagVal) {
				continue
			}
			return true
		}
		switch lowerName {
		case "json", "toon":
			// Both positive and negated boolean forms (--json=false, --toon=false,
			// --json=0, --toon=0) rewrite to structured JSON robot output rather
			// than launching the TUI, so TTY queries must be suppressed for all
			// valid boolean alias forms.
			if !hasEq || isPositiveBoolFlagValue(flagVal) || isNegatedBoolFlagValue(flagVal) {
				return true
			}
		case "format", "f", "output", "o":
			if hasEq && isStructuredFormatValue(flagVal) {
				return true
			}
		}
	}

	return false
}
