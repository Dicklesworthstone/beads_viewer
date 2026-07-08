package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	json "github.com/goccy/go-json"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"

	toon "github.com/Dicklesworthstone/toon-go"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/hooks"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"
	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"

	tea "github.com/charmbracelet/bubbletea"
)

type flagHelpSection struct {
	title string
	match func(string) bool
}

var rootHelpSections = []flagHelpSection{
	{
		title: "General Flags",
		match: func(name string) bool {
			return isOneOf(name,
				"help",
				"version",
				"cpu-profile",
				"db",
				"update",
				"check-update",
				"update-dry-run",
				"rollback",
				"yes",
				"format",
				"stats",
				"profile-startup",
				"profile-json",
				"no-cache",
				"force-full-analysis",
				"theme",
				"background-mode",
				"no-background-mode",
			)
		},
	},
	{
		title: "Search & Filters",
		match: func(name string) bool {
			return isOneOf(name,
				"recipe",
				"search",
				"label",
				"severity",
				"alert-type",
				"alert-label",
				"workspace",
				"repo",
				"robot-min-confidence",
				"robot-max-results",
				"robot-by-label",
				"robot-by-assignee",
			) || hasAnyPrefix(name, "search-")
		},
	},
	{
		title: "Robot & Planning Flags",
		match: func(name string) bool {
			return strings.HasPrefix(name, "robot-") ||
				isOneOf(name,
					"attention-limit",
					"schema-command",
					"suggest-type",
					"suggest-confidence",
					"suggest-bead",
					"graph-format",
					"graph-root",
					"graph-depth",
					"orphans-min-score",
					"file-beads-limit",
					"hotspots-limit",
					"relations-threshold",
					"relations-limit",
					"related-min-relevance",
					"related-max-results",
					"related-include-closed",
					"forecast-label",
					"forecast-sprint",
					"forecast-agents",
					"agents",
					"capacity-label",
				) ||
				hasAnyPrefix(name, "correlation-")
		},
	},
	{
		title: "History & Drift",
		match: func(name string) bool {
			return isOneOf(name,
				"diff-since",
				"as-of",
				"save-baseline",
				"baseline-info",
				"check-drift",
				"bead-history",
				"history-since",
				"history-limit",
				"min-confidence",
			)
		},
	},
	{
		title: "Export & Reporting",
		match: func(name string) bool {
			return isOneOf(name,
				"export-md",
				"no-hooks",
				"export-graph",
				"graph-preset",
				"graph-title",
				"emit-script",
				"script-limit",
				"script-format",
				"priority-brief",
				"agent-brief",
				"export-pages",
				"pages-title",
				"pages-include-closed",
				"pages-include-history",
				"preview-pages",
				"no-live-reload",
				"watch-export",
				"pages",
				"debug-render",
				"debug-width",
				"debug-height",
			)
		},
	},
	{
		title: "Agent File Management",
		match: func(name string) bool {
			return strings.HasPrefix(name, "agents-")
		},
	},
}

func isOneOf(name string, values ...string) bool {
	return slices.Contains(values, name)
}

func hasAnyPrefix(name string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type modifierFlagRule struct {
	modifier string
	requires []string
}

type enumFlagRule struct {
	name    string
	allowed []string
}

type primaryCommandGroup struct {
	flags []string
}

func validateModifierFlags(flags *flag.FlagSet, rules []modifierFlagRule) error {
	for _, rule := range rules {
		if !flags.Changed(rule.modifier) {
			continue
		}
		if hasActiveRequiredFlag(flags, rule.requires...) {
			continue
		}

		return fmt.Errorf("--%s requires %s%s", rule.modifier, formatRequiredFlags(rule.requires), formatModifierRecoveryExamples(rule.modifier))
	}

	return nil
}

func formatModifierRecoveryExamples(modifier string) string {
	examples := modifierRecoveryExamples(modifier)
	if len(examples) == 0 {
		return ""
	}
	if len(examples) == 1 {
		return "\nTry: `" + examples[0] + "`."
	}

	var b strings.Builder
	b.WriteString("\nTry one of:")
	for _, example := range examples {
		b.WriteString("\n  `")
		b.WriteString(example)
		b.WriteString("`")
	}
	return b.String()
}

func modifierRecoveryExamples(modifier string) []string {
	switch modifier {
	case "robot-search", "search-limit", "search-mode", "search-preset", "search-weights":
		return []string{
			`bv robot-search "login oauth" --json`,
			`bv --search "login oauth" --robot-search --format json`,
		}
	case "robot-diff":
		return []string{
			"bv robot-diff HEAD~1 --json",
			"bv --robot-diff --diff-since HEAD~1 --format json",
		}
	case "schema-command":
		return []string{"bv robot-schema triage --json"}
	case "graph-format", "graph-depth":
		return []string{"bv robot-graph mermaid --json"}
	case "graph-root":
		return []string{"bv robot-graph json --graph-root A --json"}
	case "severity", "alert-type", "alert-label":
		return []string{"bv robot-alerts --severity critical --json"}
	case "robot-drift":
		return []string{"bv --check-drift --robot-drift --format json"}
	case "history-since", "history-limit", "min-confidence":
		return []string{"bv robot-history --history-since \"30 days ago\" --json"}
	case "robot-history-timeout-ms":
		return []string{"bv robot-triage --robot-history-timeout-ms 10000 --json"}
	case "correlation-by", "correlation-reason":
		return []string{"bv robot-confirm-correlation deadbeef:A --correlation-by agent --json"}
	case "orphans-min-score":
		return []string{"bv robot-orphans --orphans-min-score 30 --json"}
	case "file-beads-limit":
		return []string{"bv robot-file-beads README.md --file-beads-limit 10 --json"}
	case "hotspots-limit":
		return []string{"bv robot-file-hotspots --hotspots-limit 10 --json"}
	case "relations-threshold", "relations-limit":
		return []string{"bv robot-file-relations README.md --relations-limit 10 --json"}
	case "related-min-relevance", "related-max-results", "related-include-closed":
		return []string{"bv robot-related A --related-max-results 5 --json"}
	case "network-depth":
		return []string{"bv robot-impact-network A --network-depth 2 --json"}
	case "forecast-label", "forecast-sprint", "forecast-agents":
		return []string{"bv robot-forecast all --forecast-agents 3 --json"}
	case "agents", "capacity-label":
		return []string{"bv robot-capacity --agents 3 --json"}
	case "robot-by-label", "robot-by-assignee":
		return []string{"bv robot-priority --robot-by-label backend --json"}
	case "script-limit", "script-format":
		return []string{"bv --emit-script --script-limit 5"}
	case "pages-title", "pages-include-closed", "pages-include-history":
		return []string{`bv --export-pages ./bv-pages --pages-title "Nightly Build"`}
	case "no-live-reload":
		return []string{"bv --preview-pages ./bv-pages --no-live-reload"}
	case "watch-export":
		return []string{"bv --export-pages ./bv-pages --watch-export"}
	case "debug-width", "debug-height":
		return []string{"bv --debug-render triage --debug-width 120 --debug-height 40"}
	default:
		return nil
	}
}

func validateEnumFlags(flags *flag.FlagSet, rules []enumFlagRule) error {
	for _, rule := range rules {
		if !flags.Changed(rule.name) {
			continue
		}

		value, err := flags.GetString(rule.name)
		if err != nil {
			return fmt.Errorf("reading --%s: %w", rule.name, err)
		}

		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			return fmt.Errorf("invalid --%s %q (expected one of %s)", rule.name, value, joinAllowedValues(rule.allowed))
		}

		valid := false
		for _, allowed := range rule.allowed {
			if normalized == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid --%s %q (expected one of %s)%s", rule.name, value, joinAllowedValues(rule.allowed), formatDidYouMean(normalized, rule.allowed))
		}
	}

	return nil
}

func validateExclusivePrimaryCommands(flags *flag.FlagSet, groups []primaryCommandGroup) error {
	activeGroups := make([]string, 0, len(groups))
	for _, group := range groups {
		active := activePrimaryFlags(flags, group.flags)
		if len(active) == 0 {
			continue
		}
		activeGroups = append(activeGroups, strings.Join(active, ", "))
	}

	if len(activeGroups) <= 1 {
		return nil
	}

	return fmt.Errorf("multiple primary robot commands specified: %s", strings.Join(activeGroups, " | "))
}

func activePrimaryFlags(flags *flag.FlagSet, names []string) []string {
	active := make([]string, 0, len(names))
	for _, name := range names {
		if isFlagActive(flags, name) {
			active = append(active, "--"+name)
		}
	}
	return active
}

func joinAllowedValues(values []string) string {
	return strings.Join(values, ", ")
}

func formatDidYouMean(value string, allowed []string) string {
	if suggestion := suggestClosest(value, allowed); suggestion != "" {
		return fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return ""
}

func suggestClosest(value string, allowed []string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(allowed) == 0 {
		return ""
	}

	best := ""
	bestDist := maxSuggestionDistance(value)
	for _, candidate := range allowed {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if normalized == "" {
			continue
		}
		dist := levenshteinDistance(value, normalized)
		if dist <= bestDist && (best == "" || dist < bestDist || normalized < strings.ToLower(best)) {
			best = candidate
			bestDist = dist
		}
	}
	return best
}

func maxSuggestionDistance(value string) int {
	switch n := len(value); {
	case n <= 4:
		return 2
	case n <= 10:
		return 3
	default:
		return 4
	}
}

func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = minInt(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func minInt(first int, rest ...int) int {
	min := first
	for _, v := range rest {
		if v < min {
			min = v
		}
	}
	return min
}

func hasActiveRequiredFlag(flags *flag.FlagSet, names ...string) bool {
	for _, name := range names {
		if isFlagActive(flags, name) {
			return true
		}
	}
	return false
}

func isFlagActive(flags *flag.FlagSet, name string) bool {
	f := flags.Lookup(name)
	if f == nil {
		return false
	}

	switch f.Value.Type() {
	case "bool":
		v, err := flags.GetBool(name)
		return err == nil && v
	case "string":
		v, err := flags.GetString(name)
		return err == nil && strings.TrimSpace(v) != ""
	default:
		return flags.Changed(name)
	}
}

func formatRequiredFlags(names []string) string {
	if len(names) == 0 {
		return "another flag"
	}
	if len(names) == 1 {
		return "--" + names[0]
	}

	var parts []string
	for _, name := range names {
		parts = append(parts, "--"+name)
	}
	return "one of " + strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

func newRootCommand(run func() error) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "bv",
		Short:                 "A TUI viewer for beads issue tracker.",
		Args:                  cobra.NoArgs,
		SilenceErrors:         true,
		SilenceUsage:          true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().AddFlagSet(flag.CommandLine)
	cmd.SetUsageFunc(func(cmd *cobra.Command) error {
		printRootHelp(cmd)
		return nil
	})
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		printRootHelp(cmd)
	})
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return enrichFlagParseError(err, cmd.Flags(), nil)
	})

	return cmd
}

func rewriteSingleDashLongFlags(args []string, flags *flag.FlagSet) []string {
	rewritten := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) <= 2 {
			rewritten = append(rewritten, arg)
			continue
		}

		name := arg[1:]
		if eq := strings.IndexRune(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if len(name) <= 1 || flags.Lookup(name) == nil {
			rewritten = append(rewritten, arg)
			continue
		}

		rewritten = append(rewritten, "-"+arg)
	}
	return rewritten
}

func rewriteAgentIntentArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	if rewritten, ok := rewriteAgentIntentCommand(args); ok {
		return rewritten
	}

	rewritten := rewriteAgentIntentFlagAliases(args, "")
	if containsAgentStructuredOutputAlias(args) && !hasPrimaryRobotArg(rewritten) && !hasNonRobotPrimaryArg(rewritten) {
		return append([]string{"--robot-triage"}, rewritten...)
	}
	return rewritten
}

func rewriteAgentIntentCommand(args []string) ([]string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return nil, false
	}

	command := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	if rewritten, ok := rewriteCanonicalRobotCommandIntent(command, rest); ok {
		return rewritten, true
	}
	switch command {
	case "triage", "recommend", "recommendations":
		return append([]string{"--robot-triage"}, rewriteAgentIntentFlagAliases(rest, "triage")...), true
	case "next", "pick":
		return append([]string{"--robot-next"}, rewriteAgentIntentFlagAliases(rest, "next")...), true
	case "plan":
		return append([]string{"--robot-plan"}, rewriteAgentIntentFlagAliases(rest, "plan")...), true
	case "insights", "insight", "analysis", "analyze":
		return append([]string{"--robot-insights"}, rewriteAgentIntentFlagAliases(rest, "insights")...), true
	case "priority", "priorities":
		return append([]string{"--robot-priority"}, rewriteAgentIntentFlagAliases(rest, "priority")...), true
	case "alerts":
		return append([]string{"--robot-alerts"}, rewriteAgentIntentFlagAliases(rest, "alerts")...), true
	case "suggest", "suggestions":
		return append([]string{"--robot-suggest"}, rewriteAgentIntentFlagAliases(rest, "suggest")...), true
	case "recipes":
		return append([]string{"--robot-recipes"}, rewriteAgentIntentFlagAliases(rest, "recipes")...), true
	case "metrics":
		return append([]string{"--robot-metrics"}, rewriteAgentIntentFlagAliases(rest, "metrics")...), true
	case "capabilities", "capability", "manifest":
		return append([]string{"--robot-capabilities"}, rewriteAgentIntentFlagAliases(rest, "capabilities")...), true
	case "docs", "doc":
		return rewriteRobotDocsIntent(rest), true
	case "schema", "schemas":
		return rewriteRobotSchemaIntent(rest), true
	case "search", "find":
		return rewriteRobotSearchIntent(rest), true
	case "graph":
		return rewriteRobotGraphIntent(rest), true
	case "diff", "changes":
		return rewriteRobotValueIntent(rest, "diff", "--robot-diff", "--diff-since", ""), true
	case "history":
		return rewriteRobotValueIntent(rest, "history", "--robot-history", "--bead-history", ""), true
	case "labels", "label-health":
		return append([]string{"--robot-label-health"}, rewriteAgentIntentFlagAliases(rest, "label-health")...), true
	case "label-flow":
		return append([]string{"--robot-label-flow"}, rewriteAgentIntentFlagAliases(rest, "label-flow")...), true
	case "label-attention":
		return append([]string{"--robot-label-attention"}, rewriteAgentIntentFlagAliases(rest, "label-attention")...), true
	case "hotspots", "file-hotspots":
		return append([]string{"--robot-file-hotspots"}, rewriteAgentIntentFlagAliases(rest, "file-hotspots")...), true
	case "file-beads":
		return rewriteRobotValueIntent(rest, "file-beads", "", "--robot-file-beads", ""), true
	case "file-relations":
		return rewriteRobotValueIntent(rest, "file-relations", "", "--robot-file-relations", ""), true
	case "impact":
		return rewriteRobotValueIntent(rest, "impact", "", "--robot-impact", ""), true
	case "related":
		return rewriteRobotValueIntent(rest, "related", "", "--robot-related", ""), true
	case "blockers", "blocker-chain":
		return rewriteRobotValueIntent(rest, "blocker-chain", "", "--robot-blocker-chain", ""), true
	case "impact-network":
		return rewriteRobotValueIntent(rest, "impact-network", "", "--robot-impact-network", "all"), true
	case "causality":
		return rewriteRobotValueIntent(rest, "causality", "", "--robot-causality", ""), true
	case "sprints", "sprint-list":
		return append([]string{"--robot-sprint-list"}, rewriteAgentIntentFlagAliases(rest, "sprint-list")...), true
	case "sprint", "sprint-show":
		return rewriteRobotValueIntent(rest, "sprint-show", "", "--robot-sprint-show", ""), true
	case "forecast":
		return rewriteRobotValueIntent(rest, "forecast", "", "--robot-forecast", "all"), true
	case "capacity":
		return append([]string{"--robot-capacity"}, rewriteAgentIntentFlagAliases(rest, "capacity")...), true
	case "burndown":
		return rewriteRobotValueIntent(rest, "burndown", "", "--robot-burndown", "current"), true
	case "upgrade", "self-update", "selfupdate":
		return rewriteUpgradeIntent(rest), true
	default:
		return nil, false
	}
}

// rewriteUpgradeIntent maps the ergonomic `bv upgrade` subcommand (mirroring
// `br upgrade` and `cass upgrade`) onto bv's existing self-update flags. The
// pure translation keeps the network-facing update machinery in pkg/updater
// while giving agents and humans a discoverable, sibling-consistent verb.
//
//	bv upgrade                -> --update
//	bv upgrade --yes/-y       -> --update --yes
//	bv upgrade --check        -> --check-update   (is a newer version available?)
//	bv upgrade --dry-run      -> --update-dry-run (what would be downloaded/verified)
//	bv upgrade --rollback     -> --rollback       (restore the previous binary)
//
// Bare-word aliases (check/dry-run/rollback/yes/force) are accepted too so the
// command reads naturally either way. Unrecognized tokens are passed through so
// cobra reports genuine typos instead of silently ignoring them.
func rewriteUpgradeIntent(rest []string) []string {
	mode := "update"
	yes := false
	passthrough := make([]string, 0, len(rest))
	for _, arg := range rest {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "check", "--check", "check-update", "--check-update":
			if mode == "update" {
				mode = "check"
			}
		case "dry-run", "--dry-run", "dryrun", "--dryrun":
			if mode == "update" {
				mode = "dry-run"
			}
		case "rollback", "--rollback":
			mode = "rollback"
		case "yes", "--yes", "-y", "force", "--force":
			yes = true
		default:
			passthrough = append(passthrough, arg)
		}
	}

	var out []string
	switch mode {
	case "check":
		out = []string{"--check-update"}
	case "dry-run":
		out = []string{"--update-dry-run"}
	case "rollback":
		out = []string{"--rollback"}
	default:
		out = []string{"--update"}
		if yes {
			out = append(out, "--yes")
		}
	}
	return append(out, passthrough...)
}

func rewriteCanonicalRobotCommandIntent(command string, rest []string) ([]string, bool) {
	switch command {
	case "robot-help":
		if containsAgentStructuredOutputAlias(rest) {
			return rewriteRobotDocsIntent(append([]string{"guide"}, rest...)), true
		}
		return append([]string{"--robot-help"}, rewriteAgentIntentFlagAliases(rest, "help")...), true
	case "robot-triage", "robot-triage-by-track", "robot-triage-by-label", "robot-next", "robot-plan", "robot-insights", "robot-priority", "robot-alerts", "robot-suggest", "robot-recipes", "robot-metrics", "robot-label-health", "robot-label-flow", "robot-label-attention", "robot-file-hotspots", "robot-sprint-list", "robot-capacity", "robot-capabilities", "robot-orphans", "robot-correlation-stats":
		context := strings.TrimPrefix(command, "robot-")
		return append([]string{"--" + command}, rewriteAgentIntentFlagAliases(rest, context)...), true
	case "robot-docs":
		return rewriteRobotDocsIntent(rest), true
	case "robot-schema":
		return rewriteRobotSchemaIntent(rest), true
	case "robot-search":
		return rewriteRobotSearchIntent(rest), true
	case "robot-graph":
		return rewriteRobotGraphIntent(rest), true
	case "robot-diff":
		return rewriteRobotValueIntent(rest, "diff", "--robot-diff", "--diff-since", ""), true
	case "robot-history":
		return rewriteRobotValueIntent(rest, "history", "--robot-history", "--bead-history", ""), true
	case "robot-explain-correlation":
		return rewriteRobotValueIntent(rest, "correlation", "", "--robot-explain-correlation", ""), true
	case "robot-confirm-correlation":
		return rewriteRobotValueIntent(rest, "correlation", "", "--robot-confirm-correlation", ""), true
	case "robot-reject-correlation":
		return rewriteRobotValueIntent(rest, "correlation", "", "--robot-reject-correlation", ""), true
	case "robot-file-beads":
		return rewriteRobotValueIntent(rest, "file-beads", "", "--robot-file-beads", ""), true
	case "robot-file-relations":
		return rewriteRobotValueIntent(rest, "file-relations", "", "--robot-file-relations", ""), true
	case "robot-impact":
		return rewriteRobotValueIntent(rest, "impact", "", "--robot-impact", ""), true
	case "robot-related":
		return rewriteRobotValueIntent(rest, "related", "", "--robot-related", ""), true
	case "robot-blocker-chain":
		return rewriteRobotValueIntent(rest, "blocker-chain", "", "--robot-blocker-chain", ""), true
	case "robot-impact-network":
		return rewriteRobotValueIntent(rest, "impact-network", "", "--robot-impact-network", "all"), true
	case "robot-causality":
		return rewriteRobotValueIntent(rest, "causality", "", "--robot-causality", ""), true
	case "robot-sprint-show":
		return rewriteRobotValueIntent(rest, "sprint-show", "", "--robot-sprint-show", ""), true
	case "robot-forecast":
		return rewriteRobotValueIntent(rest, "forecast", "", "--robot-forecast", "all"), true
	case "robot-burndown":
		return rewriteRobotValueIntent(rest, "burndown", "", "--robot-burndown", "current"), true
	case "robot-drift":
		return append([]string{"--check-drift", "--robot-drift"}, rewriteAgentIntentFlagAliases(rest, "drift")...), true
	default:
		return nil, false
	}
}

func rewriteRobotDocsIntent(rest []string) []string {
	prefix, rest := consumeLeadingAgentIntentFlagAliases(rest, "docs")
	out := []string{"--robot-docs"}
	topic := "guide"
	if len(rest) > 0 && isPositionalValue(rest[0]) {
		topic = rest[0]
		rest = rest[1:]
	}
	out = append(out, topic)
	out = append(out, prefix...)
	return append(out, rewriteAgentIntentFlagAliases(rest, "docs")...)
}

func rewriteRobotSchemaIntent(rest []string) []string {
	prefix, rest := consumeLeadingAgentIntentFlagAliases(rest, "schema")
	out := []string{"--robot-schema"}
	if len(rest) > 0 && isPositionalValue(rest[0]) {
		out = append(out, "--schema-command", normalizeRobotCommandName(rest[0]))
		rest = rest[1:]
	}
	out = append(out, prefix...)
	return append(out, rewriteAgentIntentFlagAliases(rest, "schema")...)
}

func rewriteRobotSearchIntent(rest []string) []string {
	prefix := make([]string, 0, len(rest))
	queryParts := make([]string, 0, len(rest))
	for len(rest) > 0 {
		if isPositionalValue(rest[0]) {
			queryParts = append(queryParts, rest[0])
			rest = rest[1:]
			continue
		}
		aliases, remaining := consumeLeadingAgentIntentFlagAliases(rest, "search")
		if len(aliases) == 0 {
			break
		}
		prefix = append(prefix, aliases...)
		rest = remaining
	}

	out := []string{"--robot-search"}
	if len(queryParts) > 0 {
		out = append([]string{"--search", strings.Join(queryParts, " ")}, out...)
	}
	out = append(out, prefix...)
	return append(out, rewriteAgentIntentFlagAliases(rest, "search")...)
}

func rewriteRobotGraphIntent(rest []string) []string {
	prefix, rest := consumeLeadingAgentIntentFlagAliases(rest, "graph")
	out := []string{"--robot-graph"}
	if len(rest) > 0 && isGraphFormat(rest[0]) {
		out = append(out, "--graph-format", strings.ToLower(rest[0]))
		rest = rest[1:]
	}
	out = append(out, prefix...)
	return append(out, rewriteAgentIntentFlagAliases(rest, "graph")...)
}

func rewriteRobotValueIntent(rest []string, context, boolFlag, valueFlag, defaultValue string) []string {
	prefix, rest := consumeLeadingAgentIntentFlagAliases(rest, context)
	out := make([]string, 0, len(rest)+3)
	if boolFlag != "" {
		out = append(out, boolFlag)
	}
	if len(rest) > 0 && isPositionalValue(rest[0]) {
		out = append(out, valueFlag, rest[0])
		rest = rest[1:]
	} else if defaultValue != "" {
		out = append(out, valueFlag, defaultValue)
	} else if valueFlag != "" && boolFlag == "" {
		out = append(out, prefix...)
		out = append(out, rewriteAgentIntentFlagAliases(rest, context)...)
		return append(out, valueFlag)
	}
	out = append(out, prefix...)
	return append(out, rewriteAgentIntentFlagAliases(rest, context)...)
}

func consumeLeadingAgentIntentFlagAliases(args []string, context string) ([]string, []string) {
	rewritten := make([]string, 0, len(args))
	rest := args
	for len(rest) > 0 {
		arg := rest[0]
		switch arg {
		case "--json":
			rewritten = append(rewritten, "--format", "json")
			rest = rest[1:]
		case "--toon":
			rewritten = append(rewritten, "--format", "toon")
			rest = rest[1:]
		case "--output", "-o":
			if len(rest) >= 2 && isRobotOutputFormat(rest[1]) {
				rewritten = append(rewritten, "--format", strings.ToLower(rest[1]))
				rest = rest[2:]
				continue
			}
			return rewritten, rest
		case "--limit":
			if len(rest) >= 2 {
				rewritten = append(rewritten, limitFlagForAgentContext(context), rest[1])
				rest = rest[2:]
				continue
			}
			return rewritten, rest
		default:
			if alias, ok := rewriteLeadingAgentIntentEqualsAlias(arg, context); ok {
				rewritten = append(rewritten, alias)
				rest = rest[1:]
				continue
			}
			return rewritten, rest
		}
	}
	return rewritten, rest
}

func rewriteAgentIntentFlagAliases(args []string, context string) []string {
	rewritten := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			rewritten = append(rewritten, "--format", "json")
		case "--toon":
			rewritten = append(rewritten, "--format", "toon")
		case "--output", "-o":
			if i+1 < len(args) && isRobotOutputFormat(args[i+1]) {
				rewritten = append(rewritten, "--format", strings.ToLower(args[i+1]))
				i++
			} else {
				rewritten = append(rewritten, arg)
			}
		case "--name":
			rewritten = append(rewritten, "--label")
		case "--limit":
			rewritten = append(rewritten, limitFlagForAgentContext(context))
		default:
			rewritten = append(rewritten, rewriteAgentIntentEqualsArg(arg, context))
		}
	}
	return rewritten
}

func rewriteAgentIntentEqualsArg(arg, context string) string {
	if alias, ok := rewriteAgentIntentEqualsAlias(arg, context); ok {
		return alias
	}
	return arg
}

func rewriteAgentIntentEqualsAlias(arg, context string) (string, bool) {
	if alias, ok := rewriteLeadingAgentIntentEqualsAlias(arg, context); ok {
		return alias, true
	}
	switch {
	case strings.HasPrefix(arg, "--name="):
		return "--label=" + strings.TrimPrefix(arg, "--name="), true
	}
	return "", false
}

func rewriteLeadingAgentIntentEqualsAlias(arg, context string) (string, bool) {
	switch {
	case arg == "--json=true":
		return "--format=json", true
	case arg == "--json=false":
		return "--format=json", true
	case arg == "--toon=true":
		return "--format=toon", true
	case arg == "--toon=false":
		return "--format=json", true
	case strings.HasPrefix(arg, "--output="):
		value := strings.TrimPrefix(arg, "--output=")
		if isRobotOutputFormat(value) {
			return "--format=" + strings.ToLower(value), true
		}
	case strings.HasPrefix(arg, "-o="):
		value := strings.TrimPrefix(arg, "-o=")
		if isRobotOutputFormat(value) {
			return "--format=" + strings.ToLower(value), true
		}
	case strings.HasPrefix(arg, "--limit="):
		return limitFlagForAgentContext(context) + "=" + strings.TrimPrefix(arg, "--limit="), true
	}
	return "", false
}

func limitFlagForAgentContext(context string) string {
	switch context {
	case "search":
		return "--search-limit"
	case "label-attention":
		return "--attention-limit"
	case "file-beads":
		return "--file-beads-limit"
	case "file-hotspots":
		return "--hotspots-limit"
	case "history":
		return "--history-limit"
	case "related":
		return "--related-max-results"
	case "file-relations":
		return "--relations-limit"
	default:
		return "--robot-max-results"
	}
}

func normalizeRobotCommandName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "robot-") {
		return value
	}
	return "robot-" + value
}

func containsAgentStructuredOutputAlias(args []string) bool {
	for i, arg := range args {
		if arg == "--json" || arg == "--json=true" || arg == "--json=false" || arg == "--toon" || arg == "--toon=true" || arg == "--toon=false" {
			return true
		}
		if strings.EqualFold(arg, "--output=json") || strings.EqualFold(arg, "-o=json") || strings.EqualFold(arg, "--output=toon") || strings.EqualFold(arg, "-o=toon") {
			return true
		}
		if (arg == "--output" || arg == "-o") && i+1 < len(args) && isRobotOutputFormat(args[i+1]) {
			return true
		}
	}
	return false
}

func hasPrimaryRobotArg(args []string) bool {
	for _, arg := range args {
		name := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
		if _, ok := primaryRobotFlagNames()[name]; ok {
			return true
		}
	}
	return false
}

func hasNonRobotPrimaryArg(args []string) bool {
	for _, arg := range args {
		name := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
		switch name {
		case "version", "help", "check-update", "update", "rollback", "pages", "export-pages", "preview-pages", "export-md", "export-graph":
			return true
		}
	}
	return false
}

func agentIntentCommandNames() []string {
	names := []string{
		"triage",
		"recommend",
		"recommendations",
		"next",
		"pick",
		"plan",
		"insights",
		"insight",
		"analysis",
		"analyze",
		"priority",
		"priorities",
		"alerts",
		"suggest",
		"suggestions",
		"recipes",
		"metrics",
		"capabilities",
		"capability",
		"manifest",
		"docs",
		"doc",
		"schema",
		"schemas",
		"search",
		"find",
		"graph",
		"diff",
		"changes",
		"history",
		"labels",
		"label-health",
		"label-flow",
		"label-attention",
		"hotspots",
		"file-hotspots",
		"file-beads",
		"file-relations",
		"impact",
		"related",
		"blockers",
		"blocker-chain",
		"impact-network",
		"causality",
		"sprints",
		"sprint-list",
		"sprint",
		"sprint-show",
		"forecast",
		"capacity",
		"burndown",
		"upgrade",
		"self-update",
		"selfupdate",
	}
	for name := range primaryRobotFlagNames() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func primaryRobotFlagNames() map[string]bool {
	return map[string]bool{
		"robot-help":                true,
		"robot-capabilities":        true,
		"robot-docs":                true,
		"robot-insights":            true,
		"robot-plan":                true,
		"robot-priority":            true,
		"robot-triage":              true,
		"robot-next":                true,
		"robot-triage-by-track":     true,
		"robot-triage-by-label":     true,
		"robot-diff":                true,
		"robot-recipes":             true,
		"robot-metrics":             true,
		"robot-schema":              true,
		"robot-suggest":             true,
		"robot-graph":               true,
		"robot-search":              true,
		"robot-drift":               true,
		"robot-history":             true,
		"robot-explain-correlation": true,
		"robot-confirm-correlation": true,
		"robot-reject-correlation":  true,
		"robot-correlation-stats":   true,
		"robot-orphans":             true,
		"robot-file-beads":          true,
		"robot-file-hotspots":       true,
		"robot-impact":              true,
		"robot-file-relations":      true,
		"robot-related":             true,
		"robot-blocker-chain":       true,
		"robot-impact-network":      true,
		"robot-causality":           true,
		"robot-sprint-list":         true,
		"robot-sprint-show":         true,
		"robot-forecast":            true,
		"robot-burndown":            true,
		"robot-capacity":            true,
		"robot-label-health":        true,
		"robot-label-flow":          true,
		"robot-label-attention":     true,
	}
}

func isPositionalValue(arg string) bool {
	return arg != "" && !strings.HasPrefix(arg, "-")
}

func isRobotOutputFormat(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "json", "toon":
		return true
	default:
		return false
	}
}

func isGraphFormat(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "json", "dot", "mermaid":
		return true
	default:
		return false
	}
}

func robotNow() time.Time {
	if value := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func sourceDateEpochActive() bool {
	value := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	if value == "" {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func stabilizeRobotTriageForPinnedClock(triage *analysis.TriageResult) {
	if sourceDateEpochActive() {
		triage.Meta.ComputeTimeMs = 0
	}
}

func enrichCommandParseError(err error, args []string) error {
	if err == nil {
		return nil
	}

	name, ok := unknownCommandName(err.Error())
	if !ok {
		return err
	}

	lookupName, tail := splitUnknownCommandIntent(name, args)
	suggestion := suggestClosest(lookupName, agentIntentCommandNames())
	if suggestion == "" {
		return err
	}

	suggestedCommand := joinCommandWords(append([]string{"bv", suggestion}, tail...))
	if strings.HasPrefix(suggestion, "robot-") {
		canonicalTail := rewriteAgentIntentFlagAliases(tail, strings.TrimPrefix(suggestion, "robot-"))
		canonicalCommand := joinCommandWords(append([]string{"bv", "--" + suggestion}, canonicalTail...))
		return fmt.Errorf("%w\nDid you mean `%s`?\nCanonical flag form: `%s`.\nRun `bv robot-capabilities --json` for the machine-readable command inventory.", err, suggestedCommand, canonicalCommand)
	}
	return fmt.Errorf("%w\nDid you mean `%s`?\nRun `bv robot-capabilities --json` for the machine-readable command inventory.", err, suggestedCommand)
}

func splitUnknownCommandIntent(name string, args []string) (string, []string) {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return name, nil
	}

	lookupName := fields[0]
	tail := append([]string{}, fields[1:]...)
	if len(args) > 0 {
		if args[0] == name || args[0] == lookupName {
			tail = append(tail, args[1:]...)
		}
	}
	return lookupName, tail
}

func joinCommandWords(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, shellQuoteWord(word))
	}
	return strings.Join(quoted, " ")
}

func shellQuoteWord(word string) string {
	if word == "" {
		return "''"
	}
	if strings.IndexFunc(word, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '=' || r == '/' || r == '.' || r == ':' || r == ',' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
	}) < 0 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
}

func enrichFlagParseError(err error, flags *flag.FlagSet, args []string) error {
	if err != nil {
		if name, ok := missingFlagArgumentName(err.Error()); ok {
			return fmt.Errorf("%w\nUse --%s VALUE. Run `bv --help` for all flags or `bv --robot-help` for agent-focused docs.", err, name)
		}

		name, ok := unknownLongFlagName(err.Error())
		if !ok {
			return err
		}

		var candidates []string
		flags.VisitAll(func(f *flag.Flag) {
			if !f.Hidden {
				candidates = append(candidates, f.Name)
			}
		})
		if suggestion := suggestClosest(name, candidates); suggestion != "" {
			correctedCommand := correctedUnknownFlagCommand(args, name, suggestion)
			if correctedCommand == "" {
				return fmt.Errorf("%w\nDid you mean --%s?\nRun `bv --help` for all flags or `bv --robot-help` for agent-focused docs.", err, suggestion)
			}
			return fmt.Errorf("%w\nDid you mean `%s`?\nRun `bv --help` for all flags or `bv --robot-help` for agent-focused docs.", err, correctedCommand)
		}

		return err
	}

	return nil
}

func correctedUnknownFlagCommand(args []string, unknown, suggestion string) string {
	if suggestion == "" {
		return ""
	}

	target := "--" + unknown
	replacement := "--" + suggestion
	correctedArgs := make([]string, 0, len(args)+1)
	replaced := false
	for _, arg := range args {
		switch {
		case arg == target:
			correctedArgs = append(correctedArgs, replacement)
			replaced = true
		case strings.HasPrefix(arg, target+"="):
			correctedArgs = append(correctedArgs, replacement+strings.TrimPrefix(arg, target))
			replaced = true
		default:
			correctedArgs = append(correctedArgs, arg)
		}
	}
	if !replaced {
		correctedArgs = append(correctedArgs, replacement)
	}
	return joinCommandWords(append([]string{"bv"}, correctedArgs...))
}

func unknownCommandName(message string) (string, bool) {
	const prefix = "unknown command \""
	idx := strings.Index(message, prefix)
	if idx < 0 {
		return "", false
	}

	rest := message[idx+len(prefix):]
	end := strings.IndexRune(rest, '"')
	if end <= 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

func missingFlagArgumentName(message string) (string, bool) {
	const prefix = "flag needs an argument: --"
	idx := strings.Index(message, prefix)
	if idx < 0 {
		return "", false
	}

	name := strings.TrimSpace(message[idx+len(prefix):])
	if name == "" {
		return "", false
	}
	return name, true
}

func unknownLongFlagName(message string) (string, bool) {
	const prefix = "unknown flag: --"
	idx := strings.Index(message, prefix)
	if idx < 0 {
		return "", false
	}

	rest := message[idx+len(prefix):]
	end := len(rest)
	for i, r := range rest {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			end = i
			break
		}
	}
	name := strings.TrimSpace(rest[:end])
	if name == "" {
		return "", false
	}
	return name, true
}

func printRootHelp(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	allFlags := cmd.Flags()
	seen := make(map[string]bool)

	fmt.Fprintln(out, "Usage: bv [flags]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "A TUI viewer for beads issue tracker.")
	fmt.Fprintln(out)

	for _, section := range rootHelpSections {
		names := collectFlagNames(allFlags, seen, section.match)
		printFlagSection(out, allFlags, section.title, names)
		markFlagNamesSeen(seen, names)
	}

	otherNames := collectFlagNames(allFlags, seen, func(string) bool { return true })
	printFlagSection(out, allFlags, "Other Flags", otherNames)

	fmt.Fprintln(out, "Run `bv --robot-help` for detailed AI/robot command documentation.")
}

func collectFlagNames(flags *flag.FlagSet, seen map[string]bool, match func(string) bool) []string {
	var names []string
	flags.VisitAll(func(f *flag.Flag) {
		if f.Hidden || seen[f.Name] || !match(f.Name) {
			return
		}
		names = append(names, f.Name)
	})
	return names
}

func markFlagNamesSeen(seen map[string]bool, names []string) {
	for _, name := range names {
		seen[name] = true
	}
}

func printFlagSection(out io.Writer, allFlags *flag.FlagSet, title string, names []string) {
	if len(names) == 0 {
		return
	}

	sectionFlags := flag.NewFlagSet(title, flag.ContinueOnError)
	sectionFlags.SortFlags = false
	sectionFlags.SetOutput(io.Discard)

	for _, name := range names {
		if f := allFlags.Lookup(name); f != nil {
			sectionFlags.AddFlag(f)
		}
	}

	fmt.Fprintf(out, "%s:\n", title)
	fmt.Fprint(out, sectionFlags.FlagUsagesWrapped(100))
	fmt.Fprintln(out)
}

// issuesFingerprint returns an order-independent fingerprint of the issue set
// that changes whenever anything the exported site renders changes. It folds in
// the canonical per-issue content + dependency hashes — the same signal the
// analysis cache uses for change detection — so it catches title/body/label/
// priority/dependency edits, not just an updated_at bump. The --watch-export
// loop uses it to skip a full re-export when a file change didn't actually
// change any issue content (#159).
func issuesFingerprint(issues []model.Issue) string {
	keys := make([]string, len(issues))
	for i, iss := range issues {
		fp := analysis.ComputeIssueFingerprint(iss)
		keys[i] = fp.ID + "\x1f" + fp.ContentHash + "\x1f" + fp.DependencyHash
	}
	sort.Strings(keys)
	h := fnv.New64a()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func main() {
	flag.CommandLine.SortFlags = false

	cpuProfile := flag.String("cpu-profile", "", "Write CPU profile to file")
	dbPath := flag.String("db", "", "Path to beads database file or .beads directory (overrides BEADS_DB and BEADS_DIR env vars)")
	versionFlag := flag.Bool("version", false, "Show version")
	// Update flags (bv-182)
	updateFlag := flag.Bool("update", false, "Update bv to the latest version")
	checkUpdateFlag := flag.Bool("check-update", false, "Check if a new version is available")
	rollbackFlag := flag.Bool("rollback", false, "Rollback to the previous version (from backup)")
	updateDryRunFlag := flag.Bool("update-dry-run", false, "Show what an update would do without installing (use via 'bv upgrade --dry-run')")
	yesFlag := flag.Bool("yes", false, "Skip confirmation prompts (use with --update)")
	exportFile := flag.String("export-md", "", "Export issues to a Markdown file (e.g., report.md)")
	robotHelp := flag.Bool("robot-help", false, "Show AI agent help")
	robotCapabilities := flag.Bool("robot-capabilities", false, "Output machine-readable command capabilities for AI agents")
	robotDocs := flag.String("robot-docs", "", "Machine-readable JSON docs for AI agents. Topics: guide, commands, examples, env, exit-codes, all")
	outputFormat := flag.StringP("format", "f", "", "Structured output format for --robot-* commands: json or toon (env: BV_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT)")
	toonStats := flag.Bool("stats", false, "Show JSON vs TOON token estimates on stderr (env: TOON_STATS=1)")
	robotInsights := flag.Bool("robot-insights", false, "Output graph analysis and insights as JSON for AI agents")
	robotPlan := flag.Bool("robot-plan", false, "Output dependency-respecting execution plan as JSON for AI agents")
	robotPriority := flag.Bool("robot-priority", false, "Output priority recommendations as JSON for AI agents")
	robotTriage := flag.Bool("robot-triage", false, "Output unified triage as JSON (the mega-command for AI agents)")
	robotTriageByTrack := flag.Bool("robot-triage-by-track", false, "Group triage recommendations by execution track (bv-87)")
	robotTriageByLabel := flag.Bool("robot-triage-by-label", false, "Group triage recommendations by label (bv-87)")
	robotNext := flag.Bool("robot-next", false, "Output only the top pick recommendation as JSON (minimal triage)")
	robotNotReadyLabels := flag.String("robot-not-ready-labels", "", "Comma-separated labels marking a bead not-ready: excluded from claimable --robot-next/--robot-triage top picks (env: BV_ROBOT_NOT_READY_LABELS; #173)")
	robotDiff := flag.Bool("robot-diff", false, "Output diff as JSON (use with --diff-since)")
	robotRecipes := flag.Bool("robot-recipes", false, "Output available recipes as JSON for AI agents")
	robotLabelHealth := flag.Bool("robot-label-health", false, "Output label health metrics as JSON for AI agents")
	robotLabelFlow := flag.Bool("robot-label-flow", false, "Output cross-label dependency flow as JSON for AI agents")
	robotLabelAttention := flag.Bool("robot-label-attention", false, "Output attention-ranked labels as JSON for AI agents")
	attentionLimit := flag.Int("attention-limit", 5, "Limit number of labels in --robot-label-attention output")
	robotAlerts := flag.Bool("robot-alerts", false, "Output alerts (drift + proactive) as JSON for AI agents")
	robotMetrics := flag.Bool("robot-metrics", false, "Output performance metrics (timing, cache, memory) as JSON")
	// JSON Schema for robot outputs (bd-2kxo)
	robotSchema := flag.Bool("robot-schema", false, "Output JSON Schema definitions for all robot commands")
	schemaCommand := flag.String("schema-command", "", "Output schema for specific command only (e.g., robot-triage)")
	// Smart suggestions (bv-180)
	robotSuggest := flag.Bool("robot-suggest", false, "Output smart suggestions (duplicates, dependencies, labels, cycles) as JSON")
	suggestType := flag.String("suggest-type", "", "Filter suggestions by type: duplicate, dependency, label, cycle")
	suggestConfidence := flag.Float64("suggest-confidence", 0.0, "Minimum confidence for suggestions (0.0-1.0)")
	suggestBead := flag.String("suggest-bead", "", "Filter suggestions for specific bead ID")
	// Graph export (bv-136)
	robotGraph := flag.Bool("robot-graph", false, "Output dependency graph as JSON/DOT/Mermaid for AI agents")
	graphFormat := flag.String("graph-format", "json", "Graph output format: json, dot, mermaid")
	graphRoot := flag.String("graph-root", "", "Subgraph from specific root issue ID")
	graphDepth := flag.Int("graph-depth", 0, "Max depth for subgraph (0 = unlimited)")
	// Graph snapshot export (bv-94)
	exportGraph := flag.String("export-graph", "", "Export graph: .html for interactive, .png/.svg for static (auto-names if empty)")
	graphPreset := flag.String("graph-preset", "compact", "Graph layout preset: compact (default) or roomy")
	graphTitle := flag.String("graph-title", "", "Title for graph export (default: project name)")
	// Robot output filters (bv-84)
	robotMinConf := flag.Float64("robot-min-confidence", 0.0, "Filter robot outputs by minimum confidence (0.0-1.0)")
	robotMaxResults := flag.Int("robot-max-results", 0, "Limit robot output count (0 = use defaults)")
	robotByLabel := flag.String("robot-by-label", "", "Filter robot outputs by label (exact match)")
	robotByAssignee := flag.String("robot-by-assignee", "", "Filter robot outputs by assignee (exact match)")
	// Label subgraph scoping (bv-122)
	labelScope := flag.StringP("label", "l", "", "Scope analysis to label's subgraph (affects --robot-insights, --robot-plan, --robot-priority)")
	alertSeverity := flag.String("severity", "", "Filter robot alerts by severity (info|warning|critical)")
	alertType := flag.String("alert-type", "", "Filter robot alerts by alert type (e.g., stale_issue)")
	alertLabel := flag.String("alert-label", "", "Filter robot alerts by label match")
	recipeName := flag.StringP("recipe", "r", "", "Apply named recipe (e.g., triage, actionable, high-impact)")
	semanticQuery := flag.String("search", "", "Semantic search query (vector-based; builds/updates index on first run)")
	robotSearch := flag.Bool("robot-search", false, "Output semantic search results as JSON for AI agents (use with --search)")
	searchLimit := flag.Int("search-limit", 10, "Max results for --search/--robot-search")
	searchMode := flag.String("search-mode", "", "Search ranking mode: text or hybrid (default: BV_SEARCH_MODE or text)")
	searchPreset := flag.String("search-preset", "", "Hybrid preset name (default: BV_SEARCH_PRESET or default)")
	searchWeights := flag.String("search-weights", "", "Hybrid weights JSON (overrides preset; keys: text,pagerank,status,impact,priority,recency)")
	diffSince := flag.String("diff-since", "", "Show changes since historical point (commit SHA, branch, tag, or date)")
	asOf := flag.String("as-of", "", "View state at point in time (commit SHA, branch, tag, or date)")
	noCache := flag.Bool("no-cache", false, "Bypass disk cache for robot triage (also: BV_NO_CACHE=1)")
	forceFullAnalysis := flag.Bool("force-full-analysis", false, "Compute all metrics regardless of graph size (may be slow for large graphs)")
	profileStartup := flag.Bool("profile-startup", false, "Output detailed startup timing profile for diagnostics")
	profileJSON := flag.Bool("profile-json", false, "Output profile in JSON format (use with --profile-startup)")
	noHooks := flag.Bool("no-hooks", false, "Skip running hooks during export")
	workspaceConfig := flag.String("workspace", "", "Load issues from workspace config file (.bv/workspace.yaml)")
	repoFilter := flag.String("repo", "", "Filter issues by repository prefix (e.g., 'api-' or 'api')")
	saveBaseline := flag.String("save-baseline", "", "Save current metrics as baseline with optional description")
	baselineInfo := flag.Bool("baseline-info", false, "Show information about the current baseline")
	checkDrift := flag.Bool("check-drift", false, "Check for drift from baseline (exit codes: 0=OK, 1=critical, 2=warning)")
	robotDriftCheck := flag.Bool("robot-drift", false, "Output drift check as JSON (use with --check-drift)")
	robotHistory := flag.Bool("robot-history", false, "Output bead-to-commit correlations as JSON")
	beadHistory := flag.String("bead-history", "", "Show history for specific bead ID")
	historySince := flag.String("history-since", "", "Limit history to commits after this date/ref (e.g., '30 days ago', '2024-01-01')")
	historyLimit := flag.Int("history-limit", 500, "Max commits to analyze (0 = unlimited)")
	robotHistoryTimeoutMs := flag.Int("robot-history-timeout-ms", -1, "Budget in ms for the git-history prologue of robot triage (0 = unbounded; default 10000, env BV_ROBOT_HISTORY_TIMEOUT_MS)")
	minConfidence := flag.Float64("min-confidence", 0.0, "Filter correlations by minimum confidence (0.0-1.0)")
	// Correlation audit flags (bv-e1u6)
	robotExplainCorrelation := flag.String("robot-explain-correlation", "", "Explain why a commit is linked to a bead (format: SHA:beadID)")
	robotConfirmCorrelation := flag.String("robot-confirm-correlation", "", "Confirm a correlation is correct (format: SHA:beadID)")
	robotRejectCorrelation := flag.String("robot-reject-correlation", "", "Reject an incorrect correlation (format: SHA:beadID)")
	correlationFeedbackBy := flag.String("correlation-by", "", "Agent/user identifier for correlation feedback")
	correlationFeedbackReason := flag.String("correlation-reason", "", "Reason for correlation feedback")
	robotCorrelationStats := flag.Bool("robot-correlation-stats", false, "Output correlation feedback statistics as JSON")
	// Orphan commit detection flags (bv-jdop)
	robotOrphans := flag.Bool("robot-orphans", false, "Output orphan commit candidates (commits that should be linked but aren't) as JSON")
	orphansMinScore := flag.Int("orphans-min-score", 30, "Minimum suspicion score for orphan candidates (0-100)")
	// File-bead index flags (bv-hmib)
	robotFileBeads := flag.String("robot-file-beads", "", "Output beads that touched a file path as JSON")
	fileBeadsLimit := flag.Int("file-beads-limit", 20, "Max closed beads to show (use with --robot-file-beads)")
	fileHotspots := flag.Bool("robot-file-hotspots", false, "Output files touched by most beads as JSON")
	hotspotsLimit := flag.Int("hotspots-limit", 10, "Max hotspots to show (use with --robot-file-hotspots)")
	// Impact analysis flag (bv-19pq)
	robotImpact := flag.String("robot-impact", "", "Analyze impact of modifying files (comma-separated paths)")
	// Co-change detection flag (bv-7a2f)
	robotFileRelations := flag.String("robot-file-relations", "", "Output files that frequently co-change with the given file path")
	relationsThreshold := flag.Float64("relations-threshold", 0.5, "Minimum correlation threshold (0.0-1.0) for related files")
	relationsLimit := flag.Int("relations-limit", 10, "Max related files to show")
	// Related work discovery flag (bv-jtdl)
	robotRelatedWork := flag.String("robot-related", "", "Output beads related to a specific bead ID as JSON")
	// Accepts EITHER int 0-100 (percent) OR float 0.0-1.0 (fraction). The
	// sibling --relations-threshold uses fraction; this dual form removes the
	// silent-failure trap when an agent or user reaches for the wrong unit.
	relatedMinRelevanceFlag, flagErr := newPercentOrFraction("related-min-relevance", 20)
	if flagErr != nil {
		fmt.Fprintf(os.Stderr, "internal flag setup error: %v\n", flagErr)
		os.Exit(1)
	}
	flag.Var(relatedMinRelevanceFlag, "related-min-relevance", "Minimum relevance score for related work (int 0-100 percent OR float 0.0-1.0 fraction)")
	relatedMaxResults := flag.Int("related-max-results", 10, "Max results per category for related work")
	relatedIncludeClosed := flag.Bool("related-include-closed", false, "Include closed beads in related work results")
	// Blocker chain analysis flag (bv-nlo0)
	robotBlockerChain := flag.String("robot-blocker-chain", "", "Output full blocker chain analysis for issue ID as JSON")
	// Impact network graph flag (bv-48kr)
	robotImpactNetwork := flag.String("robot-impact-network", "", "Output bead impact network as JSON (empty for full, or bead ID for subnetwork)")
	networkDepth := flag.Int("network-depth", 2, "Depth of subnetwork when querying specific bead (1-3)")
	// Temporal causality analysis flag (bv-j74w)
	robotCausality := flag.String("robot-causality", "", "Output causal chain analysis for bead ID as JSON")
	// Sprint flags (bv-156)
	robotSprintList := flag.Bool("robot-sprint-list", false, "Output sprints as JSON")
	robotSprintShow := flag.String("robot-sprint-show", "", "Output specific sprint details as JSON")
	// Forecast flags (bv-158)
	robotForecast := flag.String("robot-forecast", "", "Output ETA forecast for bead ID, or 'all' for all open issues")
	forecastLabel := flag.String("forecast-label", "", "Filter forecast by label")
	forecastSprint := flag.String("forecast-sprint", "", "Filter forecast by sprint ID")
	forecastAgents := flag.Int("forecast-agents", 1, "Number of parallel agents for capacity calculation")
	// Capacity simulation flags (bv-160)
	robotCapacity := flag.Bool("robot-capacity", false, "Output capacity simulation and completion projection as JSON")
	capacityAgents := flag.Int("agents", 1, "Number of parallel agents for capacity simulation")
	capacityLabel := flag.String("capacity-label", "", "Filter capacity simulation by label")
	// Burndown flags (bv-159)
	robotBurndown := flag.String("robot-burndown", "", "Output burndown data for sprint ID, or 'current' for active sprint")
	// Action script emission flags (bv-89)
	emitScript := flag.Bool("emit-script", false, "Emit shell script for top-N recommendations (agent workflows)")
	scriptLimit := flag.Int("script-limit", 5, "Limit number of items in emitted script (use with --emit-script)")
	scriptFormat := flag.String("script-format", "bash", "Script format: bash, fish, or zsh (use with --emit-script)")
	// Feedback loop flags (bv-90)
	feedbackAccept := flag.String("feedback-accept", "", "Record accept feedback for issue ID (tunes recommendation weights)")
	feedbackIgnore := flag.String("feedback-ignore", "", "Record ignore feedback for issue ID (tunes recommendation weights)")
	feedbackReset := flag.Bool("feedback-reset", false, "Reset all feedback data to defaults")
	feedbackShow := flag.Bool("feedback-show", false, "Show current feedback status and weight adjustments")
	// Priority brief export (bv-96)
	priorityBrief := flag.String("priority-brief", "", "Export priority brief to Markdown file (e.g., brief.md)")
	// Agent brief bundle (bv-131)
	agentBrief := flag.String("agent-brief", "", "Export agent brief bundle to directory (includes triage.json, insights.json, brief.md, helpers.md)")
	// Static pages export flags (bv-73f)
	exportPages := flag.String("export-pages", "", "Export static site to directory (e.g., ./bv-pages)")
	pagesTitle := flag.String("pages-title", "", "Custom title for static site")
	pagesIncludeClosed := flag.Bool("pages-include-closed", true, "Include closed issues in export (default: true)")
	pagesIncludeHistory := flag.Bool("pages-include-history", true, "Include git history for time-travel (default: true)")
	previewPages := flag.String("preview-pages", "", "Preview existing static site bundle")
	previewNoLiveReload := flag.Bool("no-live-reload", false, "Disable live-reload in preview mode")
	watchExport := flag.Bool("watch-export", false, "Watch for beads changes and auto-regenerate export (use with --export-pages)")
	pagesWizard := flag.Bool("pages", false, "Launch interactive Pages deployment wizard")
	// Debug rendering flag (for diagnosing TUI issues)
	debugRender := flag.String("debug-render", "", "Render a view and output to file (views: insights, board)")
	debugWidth := flag.Int("debug-width", 180, "Width for debug render")
	debugHeight := flag.Int("debug-height", 50, "Height for debug render")
	// Color theme selection (bv-128): light, dark, or auto (default).
	themeFlag := flag.String("theme", "", "Color theme for the TUI: light, dark, or auto (default: auto-detect terminal background)")
	// Experimental background snapshot worker (bv-o11l)
	backgroundMode := flag.Bool("background-mode", false, "Enable experimental background snapshot loading (TUI only)")
	noBackgroundMode := flag.Bool("no-background-mode", false, "Disable experimental background snapshot loading (TUI only)")
	// Agent blurb management (bv-105)
	agentsAdd := flag.Bool("agents-add", false, "Add beads workflow instructions to AGENTS.md (creates file if needed)")
	agentsRemove := flag.Bool("agents-remove", false, "Remove beads workflow instructions from AGENTS.md")
	agentsUpdate := flag.Bool("agents-update", false, "Update beads workflow instructions to latest version")
	agentsCheck := flag.Bool("agents-check", false, "Check AGENTS.md blurb status (default if no --agents-* action)")
	agentsDryRun := flag.Bool("agents-dry-run", false, "Show what would happen without executing (use with --agents-*)")
	agentsForce := flag.Bool("agents-force", false, "Skip confirmation prompts (use with --agents-*)")
	var recipeLoader *recipe.Loader
	phaseOneRobotRegistry := newRobotRegistry()
	registerPhaseOneRobotHandlers(&phaseOneRobotRegistry, phaseOneRobotHandlerConfig{
		RobotHelpFlag:         robotHelp,
		RobotCapabilitiesFlag: robotCapabilities,
		RobotSchemaFlag:       robotSchema,
		RobotRecipesFlag:      robotRecipes,
		RobotMetricsFlag:      robotMetrics,
		RobotDocsFlag:         robotDocs,
		VersionFlag:           versionFlag,
		SchemaCommand:         schemaCommand,
		RecipeLoader: func() *recipe.Loader {
			return recipeLoader
		},
		StructuredOutput: func() bool {
			return flag.CommandLine.Changed("format")
		},
	})
	phaseTwoRobotRegistry := newRobotRegistry()
	registerPhaseTwoRobotHandlers(&phaseTwoRobotRegistry, phaseTwoRobotHandlerConfig{
		RobotPlanFlag:       robotPlan,
		RobotPriorityFlag:   robotPriority,
		RobotAlertsFlag:     robotAlerts,
		RobotSuggestFlag:    robotSuggest,
		RobotBurndownFlag:   robotBurndown,
		RobotForecastFlag:   robotForecast,
		RobotSprintListFlag: robotSprintList,
		RobotGraphFlag:      robotGraph,
		RobotSearchFlag:     robotSearch,
		RobotDiffFlag:       robotDiff,
		ForceFullAnalysis:   forceFullAnalysis,
		GraphFormat:         graphFormat,
		GraphRoot:           graphRoot,
		GraphDepth:          graphDepth,
		AlertSeverity:       alertSeverity,
		AlertType:           alertType,
		AlertLabel:          alertLabel,
		SuggestType:         suggestType,
		SuggestConfidence:   suggestConfidence,
		SuggestBead:         suggestBead,
		RobotMinConf:        robotMinConf,
		RobotMaxResults:     robotMaxResults,
		RobotByLabel:        robotByLabel,
		RobotByAssignee:     robotByAssignee,
		ForecastLabel:       forecastLabel,
		ForecastSprint:      forecastSprint,
		ForecastAgents:      forecastAgents,
	})
	phaseThreeRobotRegistry := newRobotRegistry()
	registerPhaseThreeRobotHandlers(&phaseThreeRobotRegistry, phaseThreeRobotHandlerConfig{
		RobotInsightsFlag:       robotInsights,
		RobotTriageFlag:         robotTriage,
		RobotTriageByTrackFlag:  robotTriageByTrack,
		RobotTriageByLabelFlag:  robotTriageByLabel,
		RobotNextFlag:           robotNext,
		RobotHistoryFlag:        robotHistory,
		GraphRoot:               graphRoot,
		BeadHistoryFlag:         beadHistory,
		RobotExplainCorrFlag:    robotExplainCorrelation,
		RobotConfirmCorrFlag:    robotConfirmCorrelation,
		RobotRejectCorrFlag:     robotRejectCorrelation,
		RobotCorrStatsFlag:      robotCorrelationStats,
		CorrelationFeedbackBy:   correlationFeedbackBy,
		CorrelationReason:       correlationFeedbackReason,
		RobotLabelHealthFlag:    robotLabelHealth,
		RobotLabelFlowFlag:      robotLabelFlow,
		RobotLabelAttentionFlag: robotLabelAttention,
		RobotOrphansFlag:        robotOrphans,
		OrphansMinScore:         orphansMinScore,
		RobotFileBeadsFlag:      robotFileBeads,
		FileBeadsLimit:          fileBeadsLimit,
		RobotImpactFlag:         robotImpact,
		RobotFileRelationsFlag:  robotFileRelations,
		RobotRelatedFlag:        robotRelatedWork,
		RobotBlockerChainFlag:   robotBlockerChain,
		RobotImpactNetworkFlag:  robotImpactNetwork,
		RobotCausalityFlag:      robotCausality,
		RobotSprintShowFlag:     robotSprintShow,
		RobotCapacityFlag:       robotCapacity,
		ForceFullAnalysis:       forceFullAnalysis,
		HistoryLimit:            historyLimit,
		HistoryTimeoutMs:        robotHistoryTimeoutMs,
		HistorySince:            historySince,
		MinConfidence:           minConfidence,
		AttentionLimit:          attentionLimit,
		RelationsThreshold:      relationsThreshold,
		RelationsLimit:          relationsLimit,
		RelatedMinRelevance:     &relatedMinRelevanceFlag.val,
		RelatedMaxResults:       relatedMaxResults,
		RelatedIncludeClosed:    relatedIncludeClosed,
		NetworkDepth:            networkDepth,
		CapacityAgents:          capacityAgents,
		CapacityLabel:           capacityLabel,
		NotReadyLabels:          robotNotReadyLabels,
	})
	rootCmd := newRootCommand(func() error {
		// Resolve and apply the color theme before any styling is computed, so
		// every adaptive color (global styles, badges, markdown) agrees on the
		// terminal background. Precedence: --theme > BV_THEME > config > auto.
		// This is what makes light terminals readable when auto-detection fails
		// (e.g. over SSH/tmux, where lipgloss falls back to a dark background). (bv-128)
		if flag.CommandLine.Changed("theme") && normalizeThemePreference(*themeFlag) == "" &&
			strings.TrimSpace(*themeFlag) != "" {
			fmt.Fprintf(os.Stderr, "Warning: invalid --theme value %q (expected light, dark, or auto); using auto-detect\n", *themeFlag)
		}
		ui.SetThemeOverride(resolveThemePreference(*themeFlag, flag.CommandLine.Changed("theme")))

		modifierRules := []modifierFlagRule{
			{modifier: "robot-diff", requires: []string{"diff-since"}},
			{modifier: "robot-search", requires: []string{"search"}},
			{modifier: "search-limit", requires: []string{"search"}},
			{modifier: "search-mode", requires: []string{"search"}},
			{modifier: "search-preset", requires: []string{"search"}},
			{modifier: "search-weights", requires: []string{"search"}},
			{modifier: "attention-limit", requires: []string{"robot-label-attention"}},
			{modifier: "schema-command", requires: []string{"robot-schema"}},
			{modifier: "suggest-type", requires: []string{"robot-suggest"}},
			{modifier: "suggest-confidence", requires: []string{"robot-suggest"}},
			{modifier: "suggest-bead", requires: []string{"robot-suggest"}},
			{modifier: "graph-format", requires: []string{"robot-graph"}},
			{modifier: "graph-root", requires: []string{"robot-graph", "robot-triage", "robot-triage-by-track", "robot-triage-by-label", "robot-next"}},
			{modifier: "graph-depth", requires: []string{"robot-graph"}},
			{modifier: "graph-preset", requires: []string{"export-graph"}},
			{modifier: "graph-title", requires: []string{"export-graph"}},
			{modifier: "severity", requires: []string{"robot-alerts"}},
			{modifier: "alert-type", requires: []string{"robot-alerts"}},
			{modifier: "alert-label", requires: []string{"robot-alerts"}},
			{modifier: "profile-json", requires: []string{"profile-startup"}},
			{modifier: "robot-drift", requires: []string{"check-drift"}},
			{modifier: "history-since", requires: []string{"robot-history", "bead-history"}},
			{modifier: "history-limit", requires: []string{"robot-history", "bead-history"}},
			{modifier: "robot-history-timeout-ms", requires: []string{"robot-triage", "robot-triage-by-track", "robot-triage-by-label", "robot-next"}},
			{modifier: "robot-not-ready-labels", requires: []string{"robot-triage", "robot-triage-by-track", "robot-triage-by-label", "robot-next"}},
			{modifier: "min-confidence", requires: []string{"robot-history", "bead-history"}},
			{modifier: "correlation-by", requires: []string{"robot-confirm-correlation", "robot-reject-correlation"}},
			{modifier: "correlation-reason", requires: []string{"robot-confirm-correlation", "robot-reject-correlation"}},
			{modifier: "orphans-min-score", requires: []string{"robot-orphans"}},
			{modifier: "file-beads-limit", requires: []string{"robot-file-beads"}},
			{modifier: "hotspots-limit", requires: []string{"robot-file-hotspots"}},
			{modifier: "relations-threshold", requires: []string{"robot-file-relations"}},
			{modifier: "relations-limit", requires: []string{"robot-file-relations"}},
			{modifier: "related-min-relevance", requires: []string{"robot-related"}},
			{modifier: "related-max-results", requires: []string{"robot-related"}},
			{modifier: "related-include-closed", requires: []string{"robot-related"}},
			{modifier: "network-depth", requires: []string{"robot-impact-network"}},
			{modifier: "forecast-label", requires: []string{"robot-forecast"}},
			{modifier: "forecast-sprint", requires: []string{"robot-forecast"}},
			{modifier: "forecast-agents", requires: []string{"robot-forecast"}},
			{modifier: "agents", requires: []string{"robot-capacity"}},
			{modifier: "capacity-label", requires: []string{"robot-capacity"}},
			{modifier: "robot-by-label", requires: []string{"robot-priority"}},
			{modifier: "robot-by-assignee", requires: []string{"robot-priority"}},
			{modifier: "script-limit", requires: []string{"emit-script"}},
			{modifier: "script-format", requires: []string{"emit-script"}},
			{modifier: "pages-title", requires: []string{"export-pages"}},
			{modifier: "pages-include-closed", requires: []string{"export-pages"}},
			{modifier: "pages-include-history", requires: []string{"export-pages"}},
			{modifier: "no-live-reload", requires: []string{"preview-pages"}},
			{modifier: "watch-export", requires: []string{"export-pages"}},
			{modifier: "debug-width", requires: []string{"debug-render"}},
			{modifier: "debug-height", requires: []string{"debug-render"}},
		}
		enumRules := []enumFlagRule{
			{name: "graph-format", allowed: []string{"json", "dot", "mermaid"}},
			{name: "script-format", allowed: []string{"bash", "fish", "zsh"}},
		}
		primaryRobotCommandGroups := []primaryCommandGroup{
			{flags: []string{"robot-help"}},
			{flags: []string{"robot-capabilities"}},
			{flags: []string{"robot-docs"}},
			{flags: []string{"robot-insights"}},
			{flags: []string{"robot-plan"}},
			{flags: []string{"robot-priority"}},
			{flags: []string{"robot-triage", "robot-next", "robot-triage-by-track", "robot-triage-by-label"}},
			{flags: []string{"robot-diff"}},
			{flags: []string{"robot-recipes"}},
			{flags: []string{"robot-label-health"}},
			{flags: []string{"robot-label-flow"}},
			{flags: []string{"robot-label-attention"}},
			{flags: []string{"robot-alerts"}},
			{flags: []string{"robot-metrics"}},
			{flags: []string{"robot-schema"}},
			{flags: []string{"robot-suggest"}},
			{flags: []string{"robot-graph"}},
			{flags: []string{"robot-search"}},
			{flags: []string{"robot-drift"}},
			{flags: []string{"robot-history", "bead-history"}},
			{flags: []string{"robot-explain-correlation"}},
			{flags: []string{"robot-confirm-correlation"}},
			{flags: []string{"robot-reject-correlation"}},
			{flags: []string{"robot-correlation-stats"}},
			{flags: []string{"robot-orphans"}},
			{flags: []string{"robot-file-beads"}},
			{flags: []string{"robot-file-hotspots"}},
			{flags: []string{"robot-impact"}},
			{flags: []string{"robot-file-relations"}},
			{flags: []string{"robot-related"}},
			{flags: []string{"robot-blocker-chain"}},
			{flags: []string{"robot-impact-network"}},
			{flags: []string{"robot-causality"}},
			{flags: []string{"robot-sprint-list"}},
			{flags: []string{"robot-sprint-show"}},
			{flags: []string{"robot-forecast"}},
			{flags: []string{"robot-burndown"}},
			{flags: []string{"robot-capacity"}},
		}
		if err := validateModifierFlags(flag.CommandLine, modifierRules); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := validateEnumFlags(flag.CommandLine, enumRules); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := validateExclusivePrimaryCommands(flag.CommandLine, primaryRobotCommandGroups); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// CPU profiling support
		if *cpuProfile != "" {
			f, err := os.Create(*cpuProfile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Could not create CPU profile: %v\n", err)
				os.Exit(1)
			}
			defer f.Close()
			if err := pprof.StartCPUProfile(f); err != nil {
				fmt.Fprintf(os.Stderr, "Could not start CPU profile: %v\n", err)
				os.Exit(1)
			}
			defer pprof.StopCPUProfile()
		}

		// Apply --db flag: set BEADS_DB env var so all downstream code respects it.
		// Priority: --db flag > BEADS_DB env > BEADS_DIR env > auto-discovery.
		if *dbPath != "" {
			absDB, err := filepath.Abs(*dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving --db path: %v\n", err)
				os.Exit(1)
			}
			os.Setenv(loader.BeadsDBEnvVar, absDB)
		}

		// Apply --no-cache flag: set BV_NO_CACHE=1 so disk cache is bypassed.
		if *noCache {
			os.Setenv("BV_NO_CACHE", "1")
		}

		// Ensure static export flags are retained even when build tags strip features in some environments.
		_ = exportPages
		_ = pagesTitle
		_ = pagesIncludeClosed
		_ = pagesIncludeHistory
		_ = previewPages
		_ = previewNoLiveReload
		_ = pagesWizard
		_ = watchExport
		_ = debugRender
		_ = debugWidth
		_ = debugHeight
		_ = robotForecast
		_ = forecastLabel
		_ = forecastSprint
		_ = forecastAgents
		_ = robotCapacity
		_ = capacityAgents
		_ = capacityLabel
		_ = labelScope
		_ = agentBrief

		envRobot := os.Getenv("BV_ROBOT") == "1"
		stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd()))

		robotMode := envRobot ||
			*robotHelp ||
			*robotInsights ||
			*robotPlan ||
			*robotPriority ||
			*robotTriage ||
			*robotTriageByTrack ||
			*robotTriageByLabel ||
			*robotNext ||
			*robotDiff ||
			*robotRecipes ||
			*robotLabelHealth ||
			*robotLabelFlow ||
			*robotLabelAttention ||
			*robotAlerts ||
			*robotMetrics ||
			*robotSchema ||
			*robotSuggest ||
			*robotGraph ||
			*robotSearch ||
			*robotDriftCheck ||
			*robotHistory ||
			*robotFileBeads != "" ||
			*fileHotspots ||
			*robotImpact != "" ||
			*robotFileRelations != "" ||
			*robotRelatedWork != "" ||
			*robotBlockerChain != "" ||
			*robotImpactNetwork != "" ||
			*robotCausality != "" ||
			*robotOrphans ||
			*robotExplainCorrelation != "" ||
			*robotConfirmCorrelation != "" ||
			*robotRejectCorrelation != "" ||
			*robotCorrelationStats ||
			*robotSprintList ||
			*robotSprintShow != "" ||
			*robotForecast != "" ||
			*robotBurndown != "" ||
			*robotCapacity ||
			*robotCapabilities ||
			*robotDocs != "" ||
			// When stdout is non-TTY, --diff-since auto-enables JSON output. Mark this
			// as robot mode early so parsers keep stdout JSON clean.
			(*diffSince != "" && !stdoutIsTTY)

		// Mark robot mode for downstream packages (e.g., parsers) to keep stdout JSON clean.
		if robotMode && !envRobot {
			_ = os.Setenv("BV_ROBOT", "1")
			envRobot = true
		}

		// Structured output format for --robot-* commands.
		robotOutputFormat = resolveRobotOutputFormat(*outputFormat)
		robotToonEncodeOptions = resolveToonEncodeOptionsFromEnv()
		robotShowToonStats = *toonStats || strings.TrimSpace(os.Getenv("TOON_STATS")) == "1"
		if robotOutputFormat != "json" && robotOutputFormat != "toon" {
			fmt.Fprintf(os.Stderr, "Invalid --format %q (expected json|toon)\n", robotOutputFormat)
			os.Exit(2)
		}

		robotDispatchContext := RobotContext{
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
			Encoder: newRobotEncoder(os.Stdout),
		}
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-help", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "version", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-capabilities", robotDispatchContext)

		// Handle --check-update (bv-182)
		if *checkUpdateFlag {
			available, newVersion, releaseURL, err := updater.CheckUpdateAvailable()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error checking for updates: %v\n", err)
				os.Exit(1)
			}
			if available {
				fmt.Printf("New version available: %s (current: %s)\n", newVersion, version.Version)
				fmt.Printf("Download: %s\n", releaseURL)
				fmt.Println("\nRun 'bv --update' to update automatically")
			} else {
				fmt.Printf("bv is up to date (version %s)\n", version.Version)
			}
			os.Exit(0)
		}

		// Handle --update-dry-run (bv upgrade --dry-run): report exactly what an
		// update would fetch/verify/install without touching the running binary.
		if *updateDryRunFlag {
			release, err := updater.GetLatestRelease()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching release info: %v\n", err)
				os.Exit(1)
			}

			newVersion := release.TagName
			if !updater.IsNewerThanCurrent(newVersion) {
				fmt.Printf("bv is already up to date (version %s)\n", version.Version)
				os.Exit(0)
			}

			fmt.Printf("[dry-run] Would update bv from %s to %s\n", version.Version, newVersion)
			if asset := release.FindPlatformAsset(); asset != nil {
				fmt.Printf("[dry-run] Would download %s (%d bytes) for %s/%s\n",
					asset.Name, asset.Size, runtime.GOOS, runtime.GOARCH)
				fmt.Printf("[dry-run] From: %s\n", asset.BrowserDownloadURL)
			} else {
				fmt.Fprintf(os.Stderr, "[dry-run] No matching release asset found for %s/%s\n",
					runtime.GOOS, runtime.GOARCH)
			}
			if checksum := release.FindChecksumAsset(); checksum != nil {
				fmt.Printf("[dry-run] Would verify SHA-256 checksum via %s\n", checksum.Name)
			} else {
				fmt.Println("[dry-run] Warning: no checksum file found; download integrity could not be verified")
			}
			fmt.Println("[dry-run] No changes made. Run 'bv upgrade' to apply.")
			os.Exit(0)
		}

		// Handle --update (bv-182)
		if *updateFlag {
			release, err := updater.GetLatestRelease()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching release info: %v\n", err)
				os.Exit(1)
			}

			newVersion := release.TagName
			if !updater.IsNewerThanCurrent(newVersion) {
				fmt.Printf("bv is already up to date (version %s)\n", version.Version)
				os.Exit(0)
			}

			// Confirm unless --yes is provided
			if !*yesFlag {
				fmt.Printf("Update bv from %s to %s? [Y/n]: ", version.Version, newVersion)
				var response string
				fmt.Scanln(&response)
				response = strings.ToLower(strings.TrimSpace(response))
				if response != "" && response != "y" && response != "yes" {
					fmt.Println("Update cancelled")
					os.Exit(0)
				}
			}

			result, err := updater.PerformUpdate(release, *yesFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
				if result != nil && result.BackupPath != "" {
					fmt.Fprintf(os.Stderr, "Backup preserved at: %s\n", result.BackupPath)
				}
				os.Exit(1)
			}

			fmt.Println(result.Message)
			if result.BackupPath != "" {
				fmt.Printf("Backup saved to: %s\n", result.BackupPath)
				fmt.Println("Run 'bv --rollback' to restore if needed")
			}
			os.Exit(0)
		}

		// Handle --rollback (bv-182)
		if *rollbackFlag {
			if err := updater.Rollback(); err != nil {
				fmt.Fprintf(os.Stderr, "Rollback failed: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --agents-* commands (bv-105)
		agentsAnyAction := *agentsAdd || *agentsRemove || *agentsUpdate || *agentsCheck
		agentsAnyFlag := agentsAnyAction || *agentsDryRun || *agentsForce
		if agentsAnyFlag {
			workDir, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
				os.Exit(1)
			}

			// Default to check mode when no explicit action
			isCheck := !*agentsAdd && !*agentsRemove && !*agentsUpdate

			detection := agents.DetectAgentFileInParents(workDir, 3)

			if robotMode {
				// JSON output for AI agents
				result := map[string]interface{}{
					"found":            detection.Found(),
					"file_path":        detection.FilePath,
					"file_type":        detection.FileType,
					"has_blurb":        detection.HasBlurb,
					"has_legacy_blurb": detection.HasLegacyBlurb,
					"blurb_version":    detection.BlurbVersion,
					"current_version":  agents.BlurbVersion,
					"needs_blurb":      detection.Found() && detection.NeedsBlurb(),
					"needs_upgrade":    detection.NeedsUpgrade(),
				}
				data, _ := json.MarshalIndent(result, "", "  ")
				fmt.Println(string(data))
				os.Exit(0)
			}

			if isCheck || *agentsCheck {
				// Check mode: report status
				if !detection.Found() {
					fmt.Printf("No agent file found (searched up to 3 parent directories from %s)\n", workDir)
					fmt.Println("Run 'bv --agents-add' to create AGENTS.md with beads workflow instructions.")
					os.Exit(0)
				}
				if detection.HasLegacyBlurb {
					fmt.Printf("Found %s at %s (legacy blurb — needs upgrade)\n", detection.FileType, detection.FilePath)
					fmt.Println("Run 'bv --agents-update' to upgrade to the current format.")
					os.Exit(0)
				}
				if detection.HasBlurb && detection.BlurbVersion < agents.BlurbVersion {
					fmt.Printf("Found %s at %s (blurb v%d, current v%d — needs update)\n",
						detection.FileType, detection.FilePath, detection.BlurbVersion, agents.BlurbVersion)
					fmt.Println("Run 'bv --agents-update' to update to the latest version.")
					os.Exit(0)
				}
				if detection.HasBlurb {
					fmt.Printf("Found %s at %s (blurb v%d — up to date)\n",
						detection.FileType, detection.FilePath, detection.BlurbVersion)
					os.Exit(0)
				}
				// File exists but no blurb
				fmt.Printf("Found %s at %s (no beads workflow instructions)\n", detection.FileType, detection.FilePath)
				fmt.Println("Run 'bv --agents-add' to add beads workflow instructions.")
				os.Exit(0)
			}

			if *agentsAdd {
				if detection.Found() && detection.HasBlurb && detection.BlurbVersion >= agents.BlurbVersion {
					fmt.Printf("%s already has current blurb (v%d) — no action needed.\n", detection.FilePath, detection.BlurbVersion)
					os.Exit(0)
				}
				if detection.Found() && (detection.HasLegacyBlurb || (detection.HasBlurb && detection.BlurbVersion < agents.BlurbVersion)) {
					fmt.Println("Existing blurb found but outdated. Use --agents-update instead.")
					os.Exit(1)
				}

				targetPath := detection.FilePath
				creating := false
				if !detection.Found() {
					targetPath = agents.GetPreferredAgentFilePath(workDir)
					creating = true
				}

				if *agentsDryRun {
					if creating {
						fmt.Printf("[dry-run] Would create %s with beads workflow instructions.\n", targetPath)
					} else {
						fmt.Printf("[dry-run] Would append beads workflow instructions to %s.\n", targetPath)
					}
					os.Exit(0)
				}

				if !*agentsForce {
					action := "Append blurb to"
					if creating {
						action = "Create"
					}
					fmt.Printf("%s %s? [Y/n]: ", action, targetPath)
					reader := bufio.NewReader(os.Stdin)
					response, _ := reader.ReadString('\n')
					response = strings.ToLower(strings.TrimSpace(response))
					if response != "" && response != "y" && response != "yes" {
						fmt.Println("Cancelled.")
						os.Exit(0)
					}
				}

				if creating {
					if err := agents.CreateAgentFile(targetPath); err != nil {
						fmt.Fprintf(os.Stderr, "Error creating agent file: %v\n", err)
						os.Exit(1)
					}
					fmt.Printf("Created %s with beads workflow instructions.\n", targetPath)
				} else {
					if err := agents.AppendBlurbToFile(targetPath); err != nil {
						fmt.Fprintf(os.Stderr, "Error appending blurb: %v\n", err)
						os.Exit(1)
					}
					fmt.Printf("Appended beads workflow instructions to %s.\n", targetPath)
				}

				ok, _ := agents.VerifyBlurbPresent(targetPath)
				if !ok {
					fmt.Fprintf(os.Stderr, "Warning: verification failed — blurb may not have been written correctly.\n")
					os.Exit(1)
				}
				os.Exit(0)
			}

			if *agentsUpdate {
				if !detection.Found() {
					fmt.Println("No agent file found. Use --agents-add to create one.")
					os.Exit(1)
				}
				if !detection.HasBlurb && !detection.HasLegacyBlurb {
					fmt.Printf("%s has no blurb to update. Use --agents-add to add one.\n", detection.FilePath)
					os.Exit(1)
				}
				if detection.HasBlurb && detection.BlurbVersion >= agents.BlurbVersion {
					fmt.Printf("%s already has current blurb (v%d) — no update needed.\n", detection.FilePath, detection.BlurbVersion)
					os.Exit(0)
				}

				if *agentsDryRun {
					if detection.HasLegacyBlurb {
						fmt.Printf("[dry-run] Would upgrade legacy blurb to v%d in %s.\n", agents.BlurbVersion, detection.FilePath)
					} else {
						fmt.Printf("[dry-run] Would update blurb from v%d to v%d in %s.\n",
							detection.BlurbVersion, agents.BlurbVersion, detection.FilePath)
					}
					os.Exit(0)
				}

				if !*agentsForce {
					fmt.Printf("Update blurb in %s? [Y/n]: ", detection.FilePath)
					reader := bufio.NewReader(os.Stdin)
					response, _ := reader.ReadString('\n')
					response = strings.ToLower(strings.TrimSpace(response))
					if response != "" && response != "y" && response != "yes" {
						fmt.Println("Cancelled.")
						os.Exit(0)
					}
				}

				if err := agents.UpdateBlurbInFile(detection.FilePath); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating blurb: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Updated blurb to v%d in %s.\n", agents.BlurbVersion, detection.FilePath)

				ok, _ := agents.VerifyBlurbPresent(detection.FilePath)
				if !ok {
					fmt.Fprintf(os.Stderr, "Warning: verification failed — blurb may not have been written correctly.\n")
					os.Exit(1)
				}
				os.Exit(0)
			}

			if *agentsRemove {
				if !detection.Found() {
					fmt.Println("No agent file found — nothing to remove.")
					os.Exit(0)
				}
				if !detection.HasBlurb && !detection.HasLegacyBlurb {
					fmt.Printf("%s has no blurb — nothing to remove.\n", detection.FilePath)
					os.Exit(0)
				}

				if *agentsDryRun {
					fmt.Printf("[dry-run] Would remove blurb from %s.\n", detection.FilePath)
					os.Exit(0)
				}

				if !*agentsForce {
					fmt.Printf("Remove blurb from %s? [Y/n]: ", detection.FilePath)
					reader := bufio.NewReader(os.Stdin)
					response, _ := reader.ReadString('\n')
					response = strings.ToLower(strings.TrimSpace(response))
					if response != "" && response != "y" && response != "yes" {
						fmt.Println("Cancelled.")
						os.Exit(0)
					}
				}

				if err := agents.RemoveBlurbFromFile(detection.FilePath); err != nil {
					fmt.Fprintf(os.Stderr, "Error removing blurb: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Removed blurb from %s.\n", detection.FilePath)
				os.Exit(0)
			}
		}

		// Handle feedback commands (bv-90)
		if *feedbackAccept != "" || *feedbackIgnore != "" || *feedbackReset || *feedbackShow {
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}

			feedback, err := analysis.LoadFeedback(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading feedback: %v\n", err)
				os.Exit(1)
			}

			if *feedbackReset {
				feedback.Reset()
				if err := feedback.Save(beadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Error saving feedback: %v\n", err)
					os.Exit(1)
				}
				fmt.Println("Feedback data reset to defaults.")
				os.Exit(0)
			}

			if *feedbackShow {
				feedbackJSON := feedback.ToJSON()
				data, _ := json.MarshalIndent(feedbackJSON, "", "  ")
				fmt.Println(string(data))
				os.Exit(0)
			}

			// For accept/ignore, we need to get the issue's score breakdown
			if *feedbackAccept != "" || *feedbackIgnore != "" {
				issueID := *feedbackAccept
				action := "accept"
				if *feedbackIgnore != "" {
					issueID = *feedbackIgnore
					action = "ignore"
				}

				// Load issues to get score breakdown
				issues, err := datasource.LoadIssues("")
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error loading issues: %v\n", err)
					os.Exit(1)
				}

				// Find the issue
				var foundIssue *model.Issue
				for i := range issues {
					if issues[i].ID == issueID {
						foundIssue = &issues[i]
						break
					}
				}

				if foundIssue == nil {
					fmt.Fprintf(os.Stderr, "Issue not found: %s\n", issueID)
					os.Exit(1)
				}

				// Compute impact score for the issue to get breakdown
				an := analysis.NewAnalyzer(issues)
				scores := an.ComputeImpactScores()

				var score float64
				var breakdown analysis.ScoreBreakdown
				for _, s := range scores {
					if s.IssueID == issueID {
						score = s.Score
						breakdown = s.Breakdown
						break
					}
				}

				if err := feedback.RecordFeedback(issueID, action, score, breakdown); err != nil {
					fmt.Fprintf(os.Stderr, "Error recording feedback: %v\n", err)
					os.Exit(1)
				}

				if err := feedback.Save(beadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Error saving feedback: %v\n", err)
					os.Exit(1)
				}

				fmt.Printf("Recorded %s feedback for %s (score: %.3f)\n", action, issueID, score)
				fmt.Println(feedback.Summary())
				os.Exit(0)
			}
		}

		// Load recipes (needed for both --robot-recipes and --recipe)
		var loadErr error
		recipeLoader, loadErr = recipe.LoadDefault()
		if loadErr != nil {
			if !envRobot {
				fmt.Fprintf(os.Stderr, "Warning: Error loading recipes: %v\n", loadErr)
			}
			// Create empty loader to continue
			recipeLoader = recipe.NewLoader()
		}

		// Handle --robot-recipes (before loading issues)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-recipes", robotDispatchContext)

		// Handle --robot-schema (bd-2kxo)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-schema", robotDispatchContext)

		// Machine-readable robot docs (bd-2v50)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-docs", robotDispatchContext)

		// Get project directory for baseline operations (moved up to allow info check without loading issues)
		projectDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
			os.Exit(1)
		}
		baselinePath := baseline.DefaultPath(projectDir)
		robotDispatchContext.WorkDir = projectDir
		robotDispatchContext.ProjectDir = projectDir
		robotDispatchContext.BaselinePath = baselinePath
		robotDispatchContext.EnvRobot = envRobot

		// Handle --baseline-info
		if *baselineInfo {
			if !baseline.Exists(baselinePath) {
				fmt.Println("No baseline found.")
				fmt.Println("Create one with: bv --save-baseline \"description\"")
				os.Exit(0)
			}
			bl, err := baseline.Load(baselinePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading baseline: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(bl.Summary())
			os.Exit(0)
		}

		// Validate recipe name if provided (before loading issues)
		var activeRecipe *recipe.Recipe
		if *recipeName != "" {
			activeRecipe = recipeLoader.Get(*recipeName)
			if activeRecipe == nil {
				fmt.Fprintf(os.Stderr, "Error: Unknown recipe '%s'\n\n", *recipeName)
				fmt.Fprintln(os.Stderr, "Available recipes:")
				for _, name := range recipeLoader.Names() {
					r := recipeLoader.Get(name)
					fmt.Fprintf(os.Stderr, "  %-15s %s\n", name, r.Description)
				}
				os.Exit(1)
			}
		}

		// Load issues from current directory or workspace (with timing for profile)
		loadStart := time.Now()
		var issues []model.Issue
		var beadsPath string
		var workspaceInfo *workspace.LoadSummary
		var asOfResolved string // Resolved commit SHA when using --as-of (for robot output metadata)

		if *asOf != "" {
			// Time-travel mode: load historical issues from git
			// Note: --as-of takes precedence over --workspace (can't combine historical + multi-repo)
			if *workspaceConfig != "" {
				fmt.Fprintf(os.Stderr, "Warning: --workspace is ignored when --as-of is specified\n")
			}
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}
			gitLoader := loader.NewGitLoader(cwd)
			issues, err = gitLoader.LoadAt(*asOf)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading issues at %s: %v\n", *asOf, err)
				os.Exit(1)
			}
			// Resolve to commit SHA for metadata
			asOfResolved, _ = gitLoader.ResolveRevision(*asOf)
			// No live reload for historical view
			beadsPath = ""
			if !envRobot {
				if asOfResolved != "" {
					fmt.Fprintf(os.Stderr, "Loaded %d issues from %s (%s)\n", len(issues), *asOf, asOfResolved[:min(7, len(asOfResolved))])
				} else {
					fmt.Fprintf(os.Stderr, "Loaded %d issues from %s\n", len(issues), *asOf)
				}
			}
		} else if *workspaceConfig != "" {
			// Load from workspace configuration
			loadedIssues, results, err := workspace.LoadAllFromConfig(context.Background(), *workspaceConfig)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
				os.Exit(1)
			}
			issues = loadedIssues
			summary := workspace.Summarize(results)
			workspaceInfo = &summary

			// Print workspace loading summary
			if summary.FailedRepos > 0 {
				if !envRobot {
					fmt.Fprintf(os.Stderr, "Warning: %d repos failed to load\n", summary.FailedRepos)
					for _, name := range summary.FailedRepoNames {
						fmt.Fprintf(os.Stderr, "  - %s\n", name)
					}
				}
			}
			// No live reload for workspace mode (multiple files)
			beadsPath = ""

			// Automatically ensure .bv/ is in .gitignore at workspace root
			// Workspace config is typically at .bv/workspace.yaml, so project root is two levels up
			workspaceRoot := filepath.Dir(filepath.Dir(*workspaceConfig))
			_ = loader.EnsureBVInGitignore(workspaceRoot)
		} else {
			// Load from single repo (original behavior)
			var err error
			issues, err = datasource.LoadIssues("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				fmt.Fprintln(os.Stderr, "Make sure you are in a project initialized with 'br init'.")
				os.Exit(1)
			}
			// Get the selected source file for live reload.
			beadsDir, _ := loader.GetBeadsDir("")
			beadsPath, _ = resolveSingleRepoWatchFile("")

			// Automatically ensure .bv/ is in .gitignore to prevent polluting git
			// with search indexes, baselines, and other bv-specific files.
			// This is done silently and only in single-repo mode.
			projectDir := filepath.Dir(beadsDir)
			_ = loader.EnsureBVInGitignore(projectDir)
		}
		loadDuration := time.Since(loadStart)

		// Apply --repo filter if specified
		if *repoFilter != "" {
			issues = filterByRepo(issues, *repoFilter)
		}

		issuesForSearch := issues

		// Stable data hash for robot outputs (after repo filter but before recipes/TUI)
		dataHash := analysis.ComputeDataHash(issues)
		// dataHash corresponds to the current `issues` slice. Track whether later
		// reassignments (label-scope subgraph, recipe filtering) change `issues`
		// out from under it; when unchanged we can seed analyzers with dataHash to
		// avoid recomputing the identical SHA256 for their disk-cache key.
		dataHashMatchesIssues := true

		// Label subgraph scoping (bv-122)
		// When --label is specified, extract the label's subgraph and use it for all robot analysis.
		// This includes label health context in the output.
		var labelScopeContext *analysis.LabelHealth
		if *labelScope != "" {
			sg := analysis.ComputeLabelSubgraph(issues, *labelScope)
			if sg.IssueCount == 0 {
				if !envRobot {
					fmt.Fprintf(os.Stderr, "Warning: No issues found with label %q\n", *labelScope)
				}
			} else {
				// Replace issues with the subgraph issues
				subgraphIssues := make([]model.Issue, 0, len(sg.AllIssues))
				for _, id := range sg.AllIssues {
					if iss, ok := sg.IssueMap[id]; ok {
						subgraphIssues = append(subgraphIssues, iss)
					}
				}
				issues = subgraphIssues
				dataHashMatchesIssues = false
				// Compute label health for context
				cfg := analysis.DefaultLabelHealthConfig()
				allHealth := analysis.ComputeAllLabelHealth(issues, cfg, time.Now().UTC(), nil)
				for i := range allHealth.Labels {
					if allHealth.Labels[i].Label == *labelScope {
						labelScopeContext = &allHealth.Labels[i]
						break
					}
				}
			}
		}

		// Apply recipe filtering early for robot modes (bv-93)
		// This ensures --recipe filters are applied before robot modes exit.
		// dataHash uses pre-filtered issues for stability.
		if activeRecipe != nil && (*robotTriage || *robotNext || *robotTriageByTrack || *robotTriageByLabel || *robotPriority || *robotInsights || *robotPlan) {
			issues = applyRecipeFilters(issues, activeRecipe)
			issues = applyRecipeSort(issues, activeRecipe)
			dataHashMatchesIssues = false
		}
		robotDispatchContext.Issues = issues
		robotDispatchContext.DataHash = dataHash
		robotDispatchContext.DataHashMatchesIssues = dataHashMatchesIssues
		robotDispatchContext.AsOf = *asOf
		robotDispatchContext.AsOfCommit = asOfResolved
		robotDispatchContext.LabelScope = *labelScope
		robotDispatchContext.LabelContext = labelScopeContext

		// Handle semantic search CLI (bv-9gf.3)
		if *semanticQuery != "" {
			embedCfg := search.EmbeddingConfigFromEnv()
			searchCfg, err := search.SearchConfigFromEnv()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			searchCfg, err = applySearchConfigOverrides(searchCfg, *searchMode, *searchPreset, *searchWeights)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			embedder, err := search.NewEmbedderFromConfig(embedCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			projectDir, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			indexPath := search.DefaultIndexPath(projectDir, embedCfg)
			idx, loaded, err := search.LoadOrNewVectorIndex(indexPath, embedder.Dim())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			docs := search.DocumentsFromIssues(issuesForSearch)
			if !*robotSearch && !loaded {
				fmt.Fprintf(os.Stderr, "Building semantic index (%d issues)...\n", len(docs))
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			syncStats, err := search.SyncVectorIndex(ctx, idx, embedder, docs, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error building semantic index: %v\n", err)
				os.Exit(1)
			}
			if !loaded || syncStats.Changed() {
				if err := idx.Save(indexPath); err != nil {
					fmt.Fprintf(os.Stderr, "Error saving semantic index: %v\n", err)
					os.Exit(1)
				}
			}

			qvecs, err := embedder.Embed(ctx, []string{*semanticQuery})
			if err != nil || len(qvecs) != 1 {
				if err == nil {
					err = fmt.Errorf("embedder returned %d vectors for query", len(qvecs))
				}
				fmt.Fprintf(os.Stderr, "Error embedding query: %v\n", err)
				os.Exit(1)
			}

			limit := *searchLimit
			if limit <= 0 {
				limit = 10
			}
			fetchLimit := limit
			if searchCfg.Mode == search.SearchModeHybrid {
				fetchLimit = search.HybridCandidateLimit(limit, len(issuesForSearch), *semanticQuery)
			}
			results, err := idx.SearchTopK(qvecs[0], fetchLimit)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error searching index: %v\n", err)
				os.Exit(1)
			}
			results = search.ApplyShortQueryLexicalBoost(results, *semanticQuery, docs)
			if isLikelyIssueID(*semanticQuery) {
				results = promoteExactSearchResult(*semanticQuery, results)
			}

			titleByID := make(map[string]string, len(issuesForSearch))
			for _, iss := range issuesForSearch {
				titleByID[iss.ID] = iss.Title
			}

			var hybridResults []search.HybridScore
			var resolvedPreset search.PresetName
			var resolvedWeights *search.Weights
			if searchCfg.Mode == search.SearchModeHybrid {
				weights, presetName, err := resolveSearchWeights(searchCfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				weights = weights.Normalize()
				weights = search.AdjustWeightsForQuery(weights, *semanticQuery)
				resolvedPreset = presetName
				resolvedWeights = &weights

				cache := search.NewMetricsCache(search.NewAnalyzerMetricsLoader(issuesForSearch))
				if err := cache.Refresh(); err != nil {
					fmt.Fprintf(os.Stderr, "Error computing hybrid metrics: %v\n", err)
					os.Exit(1)
				}

				scorer := search.NewHybridScorer(weights, cache)
				hybridResults, err = buildHybridScores(results, scorer)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error scoring hybrid results: %v\n", err)
					os.Exit(1)
				}
				if isLikelyIssueID(*semanticQuery) {
					hybridResults = promoteExactHybridResult(*semanticQuery, hybridResults)
				}
				if len(hybridResults) > limit {
					hybridResults = hybridResults[:limit]
				}
			}

			if *robotSearch {
				out := robotSearchOutput{
					GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
					DataHash:     dataHash,
					OutputFormat: robotOutputFormat,
					Version:      version.Version,
					Query:        *semanticQuery,
					Provider:     embedCfg.Provider,
					Model:        embedCfg.Model,
					Dim:          embedder.Dim(),
					IndexPath:    indexPath,
					Index:        syncStats,
					Loaded:       loaded,
					Limit:        limit,
					Mode:         searchCfg.Mode,
				}
				if searchCfg.Mode == search.SearchModeHybrid {
					out.Preset = resolvedPreset
					out.Weights = resolvedWeights
				}
				out.Results = make([]robotSearchResult, 0, max(len(results), len(hybridResults)))
				if searchCfg.Mode == search.SearchModeHybrid {
					for _, r := range hybridResults {
						out.Results = append(out.Results, robotSearchResult{
							IssueID:         r.IssueID,
							Score:           r.FinalScore,
							TextScore:       r.TextScore,
							Title:           titleByID[r.IssueID],
							ComponentScores: r.ComponentScores,
						})
					}
					out.UsageHints = []string{
						"jq '.results[] | {id: .issue_id, score: .score, text: .text_score}' - Extract scores",
						"jq '.results[] | {id: .issue_id, components: .component_scores}' - Hybrid breakdown",
						"jq '.index' - Index update stats (added/updated/removed/embedded)",
					}
				} else {
					for _, r := range results {
						out.Results = append(out.Results, robotSearchResult{
							IssueID: r.IssueID,
							Score:   r.Score,
							Title:   titleByID[r.IssueID],
						})
					}
					out.UsageHints = []string{
						"jq '.results[] | {id: .issue_id, score: .score, title: .title}' - Extract results",
						"jq '.index' - Index update stats (added/updated/removed/embedded)",
					}
				}

				searchDispatchContext := robotDispatchContext
				searchDispatchContext.SearchOutput = &out
				dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-search", searchDispatchContext)
			}

			// Human-readable output
			if !loaded || syncStats.Changed() {
				fmt.Fprintf(os.Stderr, "Index: +%d ~%d -%d (%d total) → %s\n", syncStats.Added, syncStats.Updated, syncStats.Removed, idx.Size(), indexPath)
			}
			if searchCfg.Mode == search.SearchModeHybrid {
				for _, r := range hybridResults {
					fmt.Printf("%.4f\t%s\t%s\n", r.FinalScore, r.IssueID, titleByID[r.IssueID])
				}
			} else {
				for _, r := range results {
					fmt.Printf("%.4f\t%s\t%s\n", r.Score, r.IssueID, titleByID[r.IssueID])
				}
			}
			os.Exit(0)
		}

		// Handle --pages wizard (bv-10g)
		if *pagesWizard {
			if err := runPagesWizard(beadsPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --preview-pages (before export since it doesn't need analysis)
		if *previewPages != "" {
			if err := runPreviewServer(*previewPages, !*previewNoLiveReload); err != nil {
				fmt.Fprintf(os.Stderr, "Error starting preview server: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --export-pages (bv-73f) with optional --watch-export (bv-55)
		if *exportPages != "" {
			// Define export function for reuse in watch mode
			exportCount := 0
			doExport := func(allIssues []model.Issue) error {
				exportCount++
				if exportCount > 1 {
					fmt.Printf("\n[%s] Re-exporting (change #%d)...\n", time.Now().Format("15:04:05"), exportCount-1)
				} else {
					fmt.Println("Exporting static site...")
				}
				fmt.Printf("  → Loading %d issues\n", len(allIssues))

				// Filter closed issues if not requested
				exportIssues := allIssues
				if !*pagesIncludeClosed {
					var openIssues []model.Issue
					for _, issue := range allIssues {
						if issue.Status != model.StatusClosed {
							openIssues = append(openIssues, issue)
						}
					}
					exportIssues = openIssues
					fmt.Printf("  → Filtering to %d open issues\n", len(exportIssues))
				}

				// Load and run pre-export hooks (bv-qjc.3)
				cwd, cwdErr := os.Getwd()
				if cwdErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not get working directory for hooks: %v\n", cwdErr)
				}
				var pagesExecutor *hooks.Executor
				if !*noHooks {
					hookLoader := hooks.NewLoader(hooks.WithProjectDir(cwd))
					if err := hookLoader.Load(); err != nil {
						fmt.Printf("  → Warning: failed to load hooks: %v\n", err)
					} else if hookLoader.HasHooks() {
						fmt.Println("  → Running pre-export hooks...")
						ctx := hooks.ExportContext{
							ExportPath:   *exportPages,
							ExportFormat: "html",
							IssueCount:   len(exportIssues),
							Timestamp:    time.Now(),
						}
						pagesExecutor = hooks.NewExecutor(hookLoader.Config(), ctx)
						pagesExecutor.SetLogger(func(msg string) {
							fmt.Printf("  → %s\n", msg)
						})

						if err := pagesExecutor.RunPreExport(); err != nil {
							return fmt.Errorf("pre-export hook failed: %w", err)
						}
					}
				}

				// Build graph and compute stats
				fmt.Println("  → Running graph analysis...")
				analyzer := analysis.NewAnalyzer(exportIssues)
				stats := analyzer.AnalyzeAsync(context.Background())
				stats.WaitForPhase2()

				// Compute triage
				fmt.Println("  → Generating triage data...")
				triage := analysis.ComputeTriage(exportIssues)

				// Extract dependencies
				var deps []*model.Dependency
				for i := range exportIssues {
					issue := &exportIssues[i]
					for _, dep := range issue.Dependencies {
						if dep == nil || !dep.Type.IsBlocking() {
							continue
						}
						deps = append(deps, &model.Dependency{
							IssueID:     issue.ID,
							DependsOnID: dep.DependsOnID,
							Type:        dep.Type,
						})
					}
				}

				// Create exporter
				issuePointers := make([]*model.Issue, len(exportIssues))
				for i := range exportIssues {
					issuePointers[i] = &exportIssues[i]
				}
				exporter := export.NewSQLiteExporter(issuePointers, deps, stats, &triage)
				if *pagesTitle != "" {
					exporter.Config.Title = *pagesTitle
				}

				// Export SQLite database
				fmt.Println("  → Writing database and JSON files...")
				if err := exporter.Export(*exportPages); err != nil {
					return fmt.Errorf("exporting: %w", err)
				}

				// Copy viewer assets
				fmt.Println("  → Copying viewer assets...")
				if err := copyViewerAssets(*exportPages, *pagesTitle); err != nil {
					return fmt.Errorf("copying assets: %w", err)
				}

				// Generate README.md with project stats (useful for GitHub Pages deployment)
				fmt.Println("  → Generating README.md...")
				if err := generateREADME(*exportPages, *pagesTitle, "", exportIssues, &triage, stats); err != nil {
					fmt.Printf("  → Warning: failed to generate README: %v\n", err)
				}

				// Export history data for time-travel feature (bv-z38b)
				if *pagesIncludeHistory {
					fmt.Println("  → Generating time-travel history data...")
					if historyReport, err := generateHistoryForExport(allIssues); err == nil && historyReport != nil {
						historyPath := filepath.Join(*exportPages, "data", "history.json")
						if historyJSON, err := json.MarshalIndent(historyReport, "", "  "); err == nil {
							if err := os.WriteFile(historyPath, historyJSON, 0644); err != nil {
								fmt.Printf("  → Warning: failed to write history.json: %v\n", err)
							} else {
								fmt.Printf("  → history.json (%d commits)\n", len(historyReport.Commits))
							}
						}
					} else if err != nil {
						fmt.Printf("  → Warning: failed to generate history: %v\n", err)
					}
				}

				// Run post-export hooks (bv-qjc.3)
				if pagesExecutor != nil {
					fmt.Println("  → Running post-export hooks...")
					if err := pagesExecutor.RunPostExport(); err != nil {
						fmt.Printf("  → Warning: post-export hook failed: %v\n", err)
					}

					if len(pagesExecutor.Results()) > 0 {
						fmt.Println("")
						fmt.Println(pagesExecutor.Summary())
					}
				}

				fmt.Printf("✓ Export complete [%s]\n", time.Now().Format("15:04:05"))
				return nil
			}

			// Initial export
			if err := doExport(issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Watch mode (bv-55): monitor .beads/ for changes and auto-regenerate
			if *watchExport {
				fmt.Println("")
				fmt.Println("Watch mode enabled. Monitoring for changes...")

				// Collect all Beads JSONL files to watch
				var watchFiles []string
				var watchers []*watcher.Watcher

				if *workspaceConfig != "" {
					// Workspace mode: watch all repos' discovered Beads JSONL files (bv-79)
					wsConfig, err := workspace.LoadConfig(*workspaceConfig)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error loading workspace config: %v\n", err)
						os.Exit(1)
					}
					workspaceRoot := filepath.Dir(filepath.Dir(*workspaceConfig))

					for _, repo := range wsConfig.Repos {
						if !repo.IsEnabled() {
							continue
						}
						repoPath := repo.Path
						if !filepath.IsAbs(repoPath) {
							repoPath = filepath.Join(workspaceRoot, repoPath)
						}
						beadsDir := filepath.Join(repoPath, repo.GetBeadsPath())
						beadsFile, err := loader.FindJSONLPath(beadsDir)
						if err != nil {
							fmt.Printf("  → Warning: could not find Beads JSONL for repo %s: %v\n", repo.GetName(), err)
							continue
						}
						watchFiles = append(watchFiles, beadsFile)
					}

					if len(watchFiles) == 0 {
						fmt.Fprintf(os.Stderr, "Error: no valid Beads JSONL files found in workspace\n")
						os.Exit(1)
					}
				} else {
					// Single-repo mode: watch the same JSONL file that was selected for loading.
					watchFile, err := resolveSingleRepoWatchFile(projectDir)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error: %v\n", err)
						os.Exit(1)
					}
					watchFiles = append(watchFiles, watchFile)
				}

				// Print watched files
				for _, f := range watchFiles {
					fmt.Printf("  → Watching: %s\n", f)
				}
				fmt.Println("  → Press Ctrl+C to stop")
				fmt.Println("")
				fmt.Println("To preview with auto-refresh, run in another terminal:")
				fmt.Printf("  bv --preview-pages %s\n", *exportPages)

				// Create a merged change channel for all watchers
				mergedChangeCh := make(chan struct{}, 1)

				// Create file watchers with 500ms debounce for each file
				for _, watchFile := range watchFiles {
					w, err := watcher.NewWatcher(watchFile,
						watcher.WithDebounceDuration(500*time.Millisecond),
						watcher.WithOnError(func(err error) {
							fmt.Printf("  → Watch error: %v\n", err)
						}),
					)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error creating watcher for %s: %v\n", watchFile, err)
						os.Exit(1)
					}

					if err := w.Start(); err != nil {
						fmt.Fprintf(os.Stderr, "Error starting watcher for %s: %v\n", watchFile, err)
						os.Exit(1)
					}
					watchers = append(watchers, w)

					// Forward changes to merged channel
					go func(ch <-chan struct{}) {
						for range ch {
							select {
							case mergedChangeCh <- struct{}{}:
							default:
								// Already a change pending, skip
							}
						}
					}(w.Changed())
				}

				// Cleanup all watchers on exit
				defer func() {
					for _, w := range watchers {
						w.Stop()
					}
				}()

				// Set up signal handling for graceful shutdown
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
				defer signal.Stop(sigCh)

				// Coalescing watch loop (#159): collapse bursts of file changes
				// into a single export, rate-limit with adaptive backoff, and skip
				// re-exporting when issue content is unchanged. Previously every
				// file write triggered a full site + git-history regeneration,
				// pinning a CPU under an active author.
				reload := func() ([]model.Issue, error) {
					if *workspaceConfig != "" {
						iss, _, err := workspace.LoadAllFromConfig(context.Background(), *workspaceConfig)
						return iss, err
					}
					return datasource.LoadIssues("")
				}

				const (
					watchSettleMin = 500 * time.Millisecond
					watchSettleMax = 30 * time.Second
				)
				// Seed the fingerprint from the initial export above so the first
				// file change doesn't redundantly re-export identical content.
				settle := watchSettleMin
				lastHash := issuesFingerprint(issues)
				var settleTimer *time.Timer
				var settleC <-chan time.Time
				armSettle := func() {
					if settleTimer == nil {
						settleTimer = time.NewTimer(settle)
						settleC = settleTimer.C
						return
					}
					if !settleTimer.Stop() {
						select {
						case <-settleTimer.C:
						default:
						}
					}
					settleTimer.Reset(settle)
					settleC = settleTimer.C
				}

				for {
					select {
					case <-mergedChangeCh:
						// (Re)arm the settle timer; further changes within the
						// quiet window coalesce into the same export.
						armSettle()
					case <-settleC:
						settleC = nil
						freshIssues, err := reload()
						if err != nil {
							fmt.Printf("  → Error reloading issues: %v\n", err)
							continue
						}
						// Skip the (expensive) export when nothing meaningful
						// changed — a file can be rewritten with identical content.
						h := issuesFingerprint(freshIssues)
						if h == lastHash {
							settle = watchSettleMin
							continue
						}
						start := time.Now()
						if err := doExport(freshIssues); err != nil {
							fmt.Printf("  → Export error: %v\n", err)
							continue
						}
						lastHash = h
						// Adaptive backoff: widen the coalescing window to ~2× the
						// export cost (capped) so sustained churn can't thrash the
						// CPU; cheap exports stay near the floor for responsiveness.
						settle = 2 * time.Since(start)
						if settle < watchSettleMin {
							settle = watchSettleMin
						}
						if settle > watchSettleMax {
							settle = watchSettleMax
						}
					case <-sigCh:
						fmt.Println("\nStopping watch mode...")
						os.Exit(0)
					}
				}
			}

			fmt.Println("")
			fmt.Printf("✓ Static site exported to: %s\n", *exportPages)
			fmt.Println("")
			fmt.Println("To preview locally:")
			fmt.Printf("  bv --preview-pages %s\n", *exportPages)
			fmt.Println("")
			fmt.Println("Or open in browser:")
			fmt.Printf("  open %s/index.html\n", *exportPages)
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-label-health", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-label-flow", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-label-attention", robotDispatchContext)

		// Handle --robot-label-health
		if *robotLabelHealth {
			cfg := analysis.DefaultLabelHealthConfig()
			results := analysis.ComputeAllLabelHealth(issues, cfg, time.Now().UTC(), nil)

			output := struct {
				GeneratedAt    string                       `json:"generated_at"`
				DataHash       string                       `json:"data_hash"`
				AnalysisConfig analysis.LabelHealthConfig   `json:"analysis_config"`
				Results        analysis.LabelAnalysisResult `json:"results"`
				UsageHints     []string                     `json:"usage_hints"`
			}{
				GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
				DataHash:       dataHash,
				AnalysisConfig: cfg,
				Results:        results,
				UsageHints: []string{
					"jq '.results.summaries | sort_by(.health) | .[:3]' - Critical labels",
					"jq '.results.labels[] | select(.health_level == \"critical\")' - Critical details",
					"jq '.results.cross_label_flow.bottleneck_labels' - Bottleneck labels",
					"jq '.results.attention_needed' - Labels needing attention",
				},
			}
			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding label health: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --robot-label-flow (can be used stand-alone to avoid full health computation)
		if *robotLabelFlow {
			cfg := analysis.DefaultLabelHealthConfig()
			flow := analysis.ComputeCrossLabelFlow(issues, cfg)
			output := struct {
				GeneratedAt string                     `json:"generated_at"`
				DataHash    string                     `json:"data_hash"`
				Flow        analysis.CrossLabelFlow    `json:"flow"`
				Config      analysis.LabelHealthConfig `json:"analysis_config"`
				UsageHints  []string                   `json:"usage_hints"`
			}{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DataHash:    dataHash,
				Flow:        flow,
				Config:      cfg,
				UsageHints: []string{
					"jq '.flow.bottleneck_labels' - labels blocking the most others",
					"jq '.flow.dependencies[] | select(.issue_count > 0) | {from:.from_label,to:.to_label,count:.issue_count}'",
					"jq '.flow.flow_matrix' - raw matrix (row=from, col=to, align with .flow.labels)",
				},
			}
			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding label flow: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --robot-label-attention (bv-121)
		if *robotLabelAttention {
			cfg := analysis.DefaultLabelHealthConfig()
			result := analysis.ComputeLabelAttentionScores(issues, cfg, time.Now().UTC())

			// Apply limit
			limit := *attentionLimit
			if limit <= 0 {
				limit = 5
			}
			if limit > len(result.Labels) {
				limit = len(result.Labels)
			}

			// Build limited output
			type AttentionOutput struct {
				GeneratedAt string `json:"generated_at"`
				DataHash    string `json:"data_hash"`
				Limit       int    `json:"limit"`
				TotalLabels int    `json:"total_labels"`
				Labels      []struct {
					Rank            int     `json:"rank"`
					Label           string  `json:"label"`
					AttentionScore  float64 `json:"attention_score"`
					NormalizedScore float64 `json:"normalized_score"`
					Reason          string  `json:"reason"`
					OpenCount       int     `json:"open_count"`
					BlockedCount    int     `json:"blocked_count"`
					StaleCount      int     `json:"stale_count"`
					PageRankSum     float64 `json:"pagerank_sum"`
					VelocityFactor  float64 `json:"velocity_factor"`
				} `json:"labels"`
				UsageHints []string `json:"usage_hints"`
			}

			output := AttentionOutput{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DataHash:    dataHash,
				Limit:       limit,
				TotalLabels: result.TotalLabels,
				UsageHints: []string{
					"jq '.labels[0]' - top attention label details",
					"jq '.labels[] | select(.blocked_count > 0)' - labels with blocked issues",
					"jq '.labels[] | {label:.label,score:.attention_score,reason:.reason}'",
				},
			}

			for i := 0; i < limit; i++ {
				score := result.Labels[i]
				// Build human-readable reason
				reason := buildAttentionReason(score)
				output.Labels = append(output.Labels, struct {
					Rank            int     `json:"rank"`
					Label           string  `json:"label"`
					AttentionScore  float64 `json:"attention_score"`
					NormalizedScore float64 `json:"normalized_score"`
					Reason          string  `json:"reason"`
					OpenCount       int     `json:"open_count"`
					BlockedCount    int     `json:"blocked_count"`
					StaleCount      int     `json:"stale_count"`
					PageRankSum     float64 `json:"pagerank_sum"`
					VelocityFactor  float64 `json:"velocity_factor"`
				}{
					Rank:            score.Rank,
					Label:           score.Label,
					AttentionScore:  score.AttentionScore,
					NormalizedScore: score.NormalizedScore,
					Reason:          reason,
					OpenCount:       score.OpenCount,
					BlockedCount:    score.BlockedCount,
					StaleCount:      score.StaleCount,
					PageRankSum:     score.PageRankSum,
					VelocityFactor:  score.VelocityFactor,
				})
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding label attention: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --robot-graph (bv-136)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-graph", robotDispatchContext)

		// Handle --export-graph (bv-94) - PNG/SVG/HTML export
		if *exportGraph != "" {
			analyzer := analysis.NewAnalyzer(issues)
			stats := analyzer.Analyze()

			// Apply label filter if specified
			exportIssues := issues
			if *labelScope != "" {
				var filtered []model.Issue
				for _, iss := range issues {
					for _, lbl := range iss.Labels {
						if strings.EqualFold(lbl, *labelScope) {
							filtered = append(filtered, iss)
							break
						}
					}
				}
				exportIssues = filtered
			}

			if len(exportIssues) == 0 {
				fmt.Fprintf(os.Stderr, "No issues to export (check filters)\n")
				os.Exit(1)
			}

			// Get project name from current directory
			cwd, cwdErr := os.Getwd()
			projectName := "project"
			if cwdErr == nil {
				projectName = filepath.Base(cwd)
			}

			// Check if HTML export requested (interactive graph)
			if strings.HasSuffix(strings.ToLower(*exportGraph), ".html") || *exportGraph == "html" || *exportGraph == "interactive" {
				title := *graphTitle
				if title == "" {
					title = projectName
				}

				// Compute triage for the graph export
				triageOpts := analysis.TriageOptions{WaitForPhase2: true}
				triage := analysis.ComputeTriageWithOptions(exportIssues, triageOpts)

				opts := export.InteractiveGraphOptions{
					Issues:      exportIssues,
					Stats:       &stats,
					Triage:      &triage,
					Title:       title,
					DataHash:    dataHash,
					Path:        *exportGraph,
					ProjectName: projectName,
				}
				// Auto-generate filename if just "html" or "interactive"
				if *exportGraph == "html" || *exportGraph == "interactive" {
					opts.Path = ""
				}
				outputPath, err := export.GenerateInteractiveGraphHTML(opts)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error exporting interactive graph: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("✓ Interactive graph exported to %s (%d nodes, %d edges)\n", outputPath, len(exportIssues), stats.EdgeCount)
				os.Exit(0)
			}

			// Static PNG/SVG export (use .html for better interactive graphs)
			opts := export.GraphSnapshotOptions{
				Path:     *exportGraph,
				Title:    *graphTitle,
				Preset:   *graphPreset,
				Issues:   exportIssues,
				Stats:    &stats,
				DataHash: dataHash,
			}

			err := export.SaveGraphSnapshot(opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error exporting graph snapshot: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("✓ Graph exported to %s (%d nodes) - tip: use .html for interactive graphs\n", *exportGraph, len(exportIssues))
			os.Exit(0)
		}

		// Handle --robot-alerts (drift + proactive)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-alerts", robotDispatchContext)

		// Handle --robot-suggest (bv-180)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-suggest", robotDispatchContext)

		// Handle --profile-startup
		if *profileStartup {
			runProfileStartup(issues, loadDuration, *profileJSON, *forceFullAnalysis)
			os.Exit(0)
		}

		// Handle --save-baseline
		if *saveBaseline != "" {
			analyzer := analysis.NewAnalyzer(issues)
			if *forceFullAnalysis {
				cfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&cfg)
			}
			stats := analyzer.Analyze()

			// Compute status counts from issues
			openCount, closedCount, blockedCount := 0, 0, 0
			for _, issue := range issues {
				switch issue.Status {
				case model.StatusOpen, model.StatusInProgress:
					openCount++
				case model.StatusClosed:
					closedCount++
				case model.StatusBlocked:
					blockedCount++
				}
			}

			// Get actionable count from analyzer
			actionableCount := len(analyzer.GetActionableIssues())

			// Get cycles (method returns a copy)
			cycles := stats.Cycles()

			// Build GraphStats from analysis
			graphStats := baseline.GraphStats{
				NodeCount:       stats.NodeCount,
				EdgeCount:       stats.EdgeCount,
				Density:         stats.Density,
				OpenCount:       openCount,
				ClosedCount:     closedCount,
				BlockedCount:    blockedCount,
				CycleCount:      len(cycles),
				ActionableCount: actionableCount,
			}

			// Build TopMetrics from analysis (top 10 for each)
			// Methods return copies of the maps
			topMetrics := baseline.TopMetrics{
				PageRank:     buildMetricItems(stats.PageRank(), 10),
				Betweenness:  buildMetricItems(stats.Betweenness(), 10),
				CriticalPath: buildMetricItems(stats.CriticalPathScore(), 10),
				Hubs:         buildMetricItems(stats.Hubs(), 10),
				Authorities:  buildMetricItems(stats.Authorities(), 10),
			}

			bl := baseline.New(graphStats, topMetrics, cycles, *saveBaseline)

			if err := bl.Save(baselinePath); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving baseline: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Baseline saved to %s\n", baselinePath)
			fmt.Print(bl.Summary())
			os.Exit(0)
		}

		// Handle --check-drift
		if *checkDrift {
			if !baseline.Exists(baselinePath) {
				fmt.Fprintln(os.Stderr, "Error: No baseline found.")
				fmt.Fprintln(os.Stderr, "Create one with: bv --save-baseline \"description\"")
				os.Exit(1)
			}

			bl, err := baseline.Load(baselinePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading baseline: %v\n", err)
				os.Exit(1)
			}

			// Run analysis on current issues
			analyzer := analysis.NewAnalyzer(issues)
			if *forceFullAnalysis {
				cfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&cfg)
			}
			stats := analyzer.Analyze()

			// Compute status counts from issues
			openCount, closedCount, blockedCount := 0, 0, 0
			for _, issue := range issues {
				switch issue.Status {
				case model.StatusOpen, model.StatusInProgress:
					openCount++
				case model.StatusClosed:
					closedCount++
				case model.StatusBlocked:
					blockedCount++
				}
			}
			actionableCount := len(analyzer.GetActionableIssues())
			cycles := stats.Cycles()

			// Build current snapshot as baseline for comparison
			currentStats := baseline.GraphStats{
				NodeCount:       stats.NodeCount,
				EdgeCount:       stats.EdgeCount,
				Density:         stats.Density,
				OpenCount:       openCount,
				ClosedCount:     closedCount,
				BlockedCount:    blockedCount,
				CycleCount:      len(cycles),
				ActionableCount: actionableCount,
			}
			currentMetrics := baseline.TopMetrics{
				PageRank:     buildMetricItems(stats.PageRank(), 10),
				Betweenness:  buildMetricItems(stats.Betweenness(), 10),
				CriticalPath: buildMetricItems(stats.CriticalPathScore(), 10),
				Hubs:         buildMetricItems(stats.Hubs(), 10),
				Authorities:  buildMetricItems(stats.Authorities(), 10),
			}
			current := baseline.New(currentStats, currentMetrics, cycles, "current")

			// Load drift config and run calculator
			driftConfig, err := drift.LoadConfig(projectDir)
			if err != nil {
				if !envRobot {
					fmt.Fprintf(os.Stderr, "Warning: Error loading drift config: %v\n", err)
				}
				driftConfig = drift.DefaultConfig()
			}

			calc := drift.NewCalculator(bl, current, driftConfig)
			result := calc.Calculate()

			if *robotDriftCheck {
				// JSON output
				output := struct {
					GeneratedAt string `json:"generated_at"`
					HasDrift    bool   `json:"has_drift"`
					ExitCode    int    `json:"exit_code"`
					Summary     struct {
						Critical int `json:"critical"`
						Warning  int `json:"warning"`
						Info     int `json:"info"`
					} `json:"summary"`
					Alerts   []drift.Alert `json:"alerts"`
					Baseline struct {
						CreatedAt string `json:"created_at"`
						CommitSHA string `json:"commit_sha,omitempty"`
					} `json:"baseline"`
				}{
					GeneratedAt: time.Now().UTC().Format(time.RFC3339),
					HasDrift:    result.HasDrift,
					ExitCode:    result.ExitCode(),
					Alerts:      result.Alerts,
				}
				output.Summary.Critical = result.CriticalCount
				output.Summary.Warning = result.WarningCount
				output.Summary.Info = result.InfoCount
				output.Baseline.CreatedAt = bl.CreatedAt.Format(time.RFC3339)
				output.Baseline.CommitSHA = bl.CommitSHA

				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding drift result: %v\n", err)
					os.Exit(1)
				}
			} else {
				// Human-readable output
				fmt.Print(result.Summary())
			}

			os.Exit(result.ExitCode())
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-insights", robotDispatchContext)

		if *robotInsights {
			analyzer := analysis.NewAnalyzer(issues)
			if *forceFullAnalysis {
				cfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&cfg)
			}
			stats := analyzer.Analyze()
			// Generate top 50 lists for summary, but full stats are included in the struct
			insights := stats.GenerateInsights(50)

			// Add project-level velocity snapshot (using dedicated helper for efficiency)
			if v := analysis.ComputeProjectVelocity(issues, time.Now(), 8); v != nil {
				snap := &analysis.VelocitySnapshot{
					Closed7:   v.ClosedLast7Days,
					Closed30:  v.ClosedLast30Days,
					AvgDays:   v.AvgDaysToClose,
					Estimated: v.Estimated,
				}
				if len(v.Weekly) > 0 {
					snap.Weekly = make([]int, len(v.Weekly))
					for i := range v.Weekly {
						snap.Weekly[i] = v.Weekly[i].Closed
					}
				}
				insights.Velocity = snap
			}

			// Optional cap for metric maps to avoid overload
			limitMaps := func(m map[string]float64, limit int) map[string]float64 {
				if limit <= 0 || limit >= len(m) {
					return m
				}
				type kv struct {
					k string
					v float64
				}
				var items []kv
				for k, v := range m {
					items = append(items, kv{k, v})
				}
				sort.Slice(items, func(i, j int) bool {
					if items[i].v == items[j].v {
						return items[i].k < items[j].k
					}
					return items[i].v > items[j].v
				})
				trim := make(map[string]float64, limit)
				for i := 0; i < limit; i++ {
					trim[items[i].k] = items[i].v
				}
				return trim
			}

			limitMapInt := func(m map[string]int, limit int) map[string]int {
				if limit <= 0 || len(m) <= limit {
					return m
				}
				type kv struct {
					k string
					v int
				}
				var items []kv
				for k, v := range m {
					items = append(items, kv{k, v})
				}
				sort.Slice(items, func(i, j int) bool {
					if items[i].v == items[j].v {
						return items[i].k < items[j].k
					}
					return items[i].v > items[j].v
				})
				trim := make(map[string]int, limit)
				for i := 0; i < limit; i++ {
					trim[items[i].k] = items[i].v
				}
				return trim
			}

			limitSlice := func(s []string, limit int) []string {
				if limit <= 0 || len(s) <= limit {
					return s
				}
				return s[:limit]
			}

			// Default cap to keep payload small; allow override via env
			mapLimit := 200
			if v := os.Getenv("BV_INSIGHTS_MAP_LIMIT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					mapLimit = n
				}
			}

			fullStats := struct {
				PageRank          map[string]float64 `json:"pagerank"`
				Betweenness       map[string]float64 `json:"betweenness"`
				Eigenvector       map[string]float64 `json:"eigenvector"`
				Hubs              map[string]float64 `json:"hubs"`
				Authorities       map[string]float64 `json:"authorities"`
				CriticalPathScore map[string]float64 `json:"critical_path_score"`
				CoreNumber        map[string]int     `json:"core_number"`
				Slack             map[string]float64 `json:"slack"`
				Articulation      []string           `json:"articulation_points"`
			}{
				PageRank:          limitMaps(stats.PageRank(), mapLimit),
				Betweenness:       limitMaps(stats.Betweenness(), mapLimit),
				Eigenvector:       limitMaps(stats.Eigenvector(), mapLimit),
				Hubs:              limitMaps(stats.Hubs(), mapLimit),
				Authorities:       limitMaps(stats.Authorities(), mapLimit),
				CriticalPathScore: limitMaps(stats.CriticalPathScore(), mapLimit),
				CoreNumber:        limitMapInt(stats.CoreNumber(), mapLimit),
				Slack:             limitMaps(stats.Slack(), mapLimit),
				Articulation:      limitSlice(stats.ArticulationPoints(), mapLimit),
			}

			// Get top what-if deltas for issues with highest downstream impact (bv-83)
			topWhatIfs := analyzer.TopWhatIfDeltas(10)

			// Generate advanced insights with canonical structure (bv-181)
			advancedInsights := analyzer.GenerateAdvancedInsights(analysis.DefaultAdvancedInsightsConfig())

			output := struct {
				GeneratedAt    string                  `json:"generated_at"`
				DataHash       string                  `json:"data_hash"`
				AsOf           string                  `json:"as_of,omitempty"`        // Historical snapshot ref
				AsOfCommit     string                  `json:"as_of_commit,omitempty"` // Resolved commit SHA
				AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
				Status         analysis.MetricStatus   `json:"status"`
				LabelScope     string                  `json:"label_scope,omitempty"`   // bv-122: Label filter applied
				LabelContext   *analysis.LabelHealth   `json:"label_context,omitempty"` // bv-122: Health context for scoped label
				analysis.Insights
				FullStats        interface{}                `json:"full_stats"`
				TopWhatIfs       []analysis.WhatIfEntry     `json:"top_what_ifs,omitempty"`      // Issues with highest downstream impact (bv-83)
				AdvancedInsights *analysis.AdvancedInsights `json:"advanced_insights,omitempty"` // bv-181: Canonical advanced features
				UsageHints       []string                   `json:"usage_hints"`                 // bv-84: Agent-friendly hints
			}{
				GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
				DataHash:         dataHash,
				AsOf:             *asOf,
				AsOfCommit:       asOfResolved,
				AnalysisConfig:   stats.Config,
				Status:           stats.Status(),
				LabelScope:       *labelScope,
				LabelContext:     labelScopeContext,
				Insights:         insights,
				FullStats:        fullStats,
				TopWhatIfs:       topWhatIfs,
				AdvancedInsights: advancedInsights,
				UsageHints: []string{
					"jq '.Bottlenecks[:5] | map(.ID)' - Top 5 bottleneck IDs",
					"jq '.CriticalPath[:3]' - Top 3 critical path items",
					"jq '.top_what_ifs[] | select(.delta.direct_unblocks > 2)' - High-impact items",
					"jq '.full_stats.pagerank | to_entries | sort_by(-.value)[:5]' - Top PageRank",
					"jq '.full_stats.core_number | to_entries | sort_by(-.value)[:5]' - Strongly embedded nodes (k-core)",
					"jq '.full_stats.articulation_points' - Structural cut points",
					"jq '.Slack[:5]' - Nodes with slack (good parallel work candidates)",
					"jq '.Cycles | length' - Count of detected cycles",
					"jq '.advanced_insights.cycle_break' - Cycle break suggestions (bv-181)",
					"BV_INSIGHTS_MAP_LIMIT=50 bv --robot-insights - Reduce map sizes",
				},
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding insights: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-plan", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-priority", robotDispatchContext)

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-next", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-triage", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-triage-by-track", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-triage-by-label", robotDispatchContext)

		if *robotTriage || *robotNext || *robotTriageByTrack || *robotTriageByLabel {
			// Attempt to load history for staleness analysis
			// We use a best-effort approach here - if history isn't available or fails,
			// we just proceed without staleness data.
			var historyReport *correlation.HistoryReport

			// bv-perf: Skip history loading if no open issues exist
			// ComputeStaleness only processes open issues, so loading git history
			// is wasted work when all issues are closed.
			hasOpenIssues := false
			for _, issue := range issues {
				if issue.Status != model.StatusClosed && issue.Status != model.StatusTombstone {
					hasOpenIssues = true
					break
				}
			}

			if hasOpenIssues {
				if cwd, err := os.Getwd(); err == nil {
					if beadsDir, err := loader.GetBeadsDir(""); err == nil {
						if beadsPath, err := loader.FindJSONLPath(beadsDir); err == nil {
							// Use a smaller limit for triage to keep it fast, unless overridden
							limit := *historyLimit
							if limit == 500 { // If default
								limit = 200 // Use smaller default for triage
							}

							// Validate repo first
							if correlation.ValidateRepository(cwd) == nil {
								beadInfos := make([]correlation.BeadInfo, len(issues))
								for i, issue := range issues {
									beadInfos[i] = correlation.BeadInfo{
										ID:     issue.ID,
										Title:  issue.Title,
										Status: string(issue.Status),
									}
								}

								correlator := correlation.NewCorrelator(cwd, beadsPath)
								opts := correlation.CorrelatorOptions{Limit: limit}

								// Swallow errors for triage flow - staleness is optional
								if report, err := correlator.GenerateReport(beadInfos, opts); err == nil {
									historyReport = report
								}
							}
						}
					}
				}
			}

			// bv-87: Support track/label-aware grouping for multi-agent coordination
			opts := analysis.TriageOptions{
				GroupByTrack:  *robotTriageByTrack,
				GroupByLabel:  *robotTriageByLabel,
				WaitForPhase2: true, // Triage needs full graph metrics
				UseFastConfig: true, // Use minimal Phase 2 config for robot mode (bv-t1js)
				History:       historyReport,
			}
			triage := analysis.ComputeTriageWithOptions(issues, opts)

			// bv-90: Load feedback data for output
			var feedbackInfo *analysis.FeedbackJSON
			if robotTriageBeadsDir, err := loader.GetBeadsDir(""); err == nil {
				if feedbackData, err := analysis.LoadFeedback(robotTriageBeadsDir); err == nil && len(feedbackData.Events) > 0 {
					info := feedbackData.ToJSON()
					feedbackInfo = &info
				}
			}

			if *robotNext {
				if err := handleRobotNext(robotDispatchContext, phaseThreeRobotHandlerConfig{
					RobotNextFlag:  robotNext,
					GraphRoot:      graphRoot,
					NotReadyLabels: robotNotReadyLabels,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding robot-next: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}

			// Full triage output with usage hints
			output := struct {
				GeneratedAt string                 `json:"generated_at"`
				DataHash    string                 `json:"data_hash"`
				AsOf        string                 `json:"as_of,omitempty"`        // Historical snapshot ref (e.g., HEAD~30)
				AsOfCommit  string                 `json:"as_of_commit,omitempty"` // Resolved commit SHA
				Triage      analysis.TriageResult  `json:"triage"`
				Feedback    *analysis.FeedbackJSON `json:"feedback,omitempty"` // bv-90: Feedback loop state
				UsageHints  []string               `json:"usage_hints"`        // bv-84: Agent-friendly hints
			}{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DataHash:    dataHash,
				AsOf:        *asOf,
				AsOfCommit:  asOfResolved,
				Triage:      triage,
				Feedback:    feedbackInfo,
				UsageHints: []string{
					"jq '.triage.quick_ref.top_picks[:3]' - Top 3 picks for immediate work",
					"jq '.triage.recommendations[3:10] | map({id,title,score})' - Next candidates after top picks",
					"jq '.triage.blockers_to_clear | map(.id)' - High-impact blockers to clear",
					"jq '.triage.recommendations[] | select(.type == \"bug\")' - Bug-focused recommendations",
					"jq '.triage.quick_ref.top_picks[] | select(.unblocks > 2)' - High-impact picks",
					"jq '.triage.quick_wins' - Low-effort, high-impact items",
					"--robot-next - Get only the single top recommendation",
					"--robot-triage-by-track - Group by execution track for multi-agent coordination",
					"--robot-triage-by-label - Group by label for area-focused agents",
					"jq '.triage.recommendations_by_track[].top_pick' - Top pick per track",
					"jq '.triage.recommendations_by_label[].claim_command' - Claim commands per label",
					"jq '.feedback.weight_adjustments' - View feedback-adjusted weights (bv-90)",
				},
			}
			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding robot-triage: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --priority-brief flag (bv-96)
		if *priorityBrief != "" {
			fmt.Printf("Generating priority brief to %s...\n", *priorityBrief)
			triage := analysis.ComputeTriage(issues)

			// Marshal triage to JSON for the export function
			triageJSON, err := json.Marshal(triage)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshaling triage data: %v\n", err)
				os.Exit(1)
			}

			// Generate the brief
			config := export.DefaultPriorityBriefConfig()
			config.DataHash = dataHash
			brief, err := export.GeneratePriorityBriefFromTriageJSON(triageJSON, config)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating priority brief: %v\n", err)
				os.Exit(1)
			}

			// Write to file
			if err := os.WriteFile(*priorityBrief, []byte(brief), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing priority brief: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Done! Priority brief saved to %s\n", *priorityBrief)
			os.Exit(0)
		}

		// Handle --agent-brief flag (bv-131)
		if *agentBrief != "" {
			fmt.Printf("Generating agent brief bundle to %s/...\n", *agentBrief)

			// Create output directory
			if err := os.MkdirAll(*agentBrief, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
				os.Exit(1)
			}

			// Generate triage data
			triage := analysis.ComputeTriage(issues)
			triageJSON, err := json.MarshalIndent(triage, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshaling triage: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(filepath.Join(*agentBrief, "triage.json"), triageJSON, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing triage.json: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  → triage.json")

			// Generate insights
			analyzer := analysis.NewAnalyzer(issues)
			stats := analyzer.Analyze()
			insights := stats.GenerateInsights(50)
			insightsJSON, err := json.MarshalIndent(insights, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshaling insights: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(filepath.Join(*agentBrief, "insights.json"), insightsJSON, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing insights.json: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  → insights.json")

			// Generate priority brief
			config := export.DefaultPriorityBriefConfig()
			config.DataHash = dataHash
			brief, err := export.GeneratePriorityBriefFromTriageJSON(triageJSON, config)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating brief: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(filepath.Join(*agentBrief, "brief.md"), []byte(brief), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing brief.md: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  → brief.md")

			// Generate jq helpers
			helpers := generateJQHelpers()
			if err := os.WriteFile(filepath.Join(*agentBrief, "helpers.md"), []byte(helpers), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing helpers.md: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  → helpers.md")

			// Generate meta.json with hash and config
			meta := struct {
				GeneratedAt string   `json:"generated_at"`
				DataHash    string   `json:"data_hash"`
				IssueCount  int      `json:"issue_count"`
				Version     string   `json:"version"`
				Files       []string `json:"files"`
			}{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DataHash:    dataHash,
				IssueCount:  len(issues),
				Version:     version.Version,
				Files:       []string{"triage.json", "insights.json", "brief.md", "helpers.md", "meta.json"},
			}
			metaJSON, _ := json.MarshalIndent(meta, "", "  ")
			if err := os.WriteFile(filepath.Join(*agentBrief, "meta.json"), metaJSON, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing meta.json: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  → meta.json")

			fmt.Printf("\nDone! Agent brief bundle saved to %s/\n", *agentBrief)
			os.Exit(0)
		}

		// Handle --emit-script flag (bv-89)
		if *emitScript {
			triage := analysis.ComputeTriage(issues)

			// Determine script limit
			limit := *scriptLimit
			if limit <= 0 {
				limit = 5
			}

			// Collect top recommendations
			recs := triage.Recommendations
			if len(recs) > limit {
				recs = recs[:limit]
			}

			// Build script header with hash/config
			var sb strings.Builder
			switch *scriptFormat {
			case "fish":
				sb.WriteString("#!/usr/bin/env fish\n")
			case "zsh":
				sb.WriteString("#!/usr/bin/env zsh\n")
			default:
				sb.WriteString("#!/usr/bin/env bash\n")
				sb.WriteString("set -euo pipefail\n")
			}

			sb.WriteString(fmt.Sprintf("# Generated by bv --emit-script at %s\n", time.Now().UTC().Format(time.RFC3339)))
			sb.WriteString(fmt.Sprintf("# Data hash: %s\n", dataHash))
			sb.WriteString(fmt.Sprintf("# Top %d recommendations from %d actionable items\n", len(recs), len(triage.Recommendations)))
			sb.WriteString("#\n")
			sb.WriteString("# Usage: source this script or run it directly\n")
			sb.WriteString("# Each command will claim and show the recommended issue\n")
			sb.WriteString("#\n\n")

			if len(recs) == 0 {
				sb.WriteString("echo 'No actionable recommendations available'\n")
				sb.WriteString("exit 0\n")
			} else {
				// Generate commands for each recommendation
				for i, rec := range recs {
					sb.WriteString(fmt.Sprintf("# %d. %s (score: %.3f)\n", i+1, rec.Title, rec.Score))
					if len(rec.Reasons) > 0 {
						sb.WriteString(fmt.Sprintf("#    Reason: %s\n", rec.Reasons[0]))
					}
					if len(rec.UnblocksIDs) > 0 {
						sb.WriteString(fmt.Sprintf("#    Unblocks: %d downstream items\n", len(rec.UnblocksIDs)))
					}

					// Claim command
					sb.WriteString(fmt.Sprintf("# To claim: br update %s --status=in_progress\n", rec.ID))
					// Show command
					sb.WriteString(fmt.Sprintf("br show %s\n", rec.ID))
					sb.WriteString("\n")
				}

				// Add summary section
				sb.WriteString("# === Quick Actions ===\n")
				sb.WriteString("# To claim the top pick:\n")
				if len(recs) > 0 {
					sb.WriteString(fmt.Sprintf("# br update %s --status=in_progress\n", recs[0].ID))
				}
				sb.WriteString("#\n")
				sb.WriteString("# To claim all listed items (uncomment to enable):\n")
				for _, rec := range recs {
					sb.WriteString(fmt.Sprintf("# br update %s --status=in_progress\n", rec.ID))
				}
			}

			fmt.Print(sb.String())
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-history", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "bead-history", robotDispatchContext)

		// Handle --robot-history flag
		if *robotHistory || *beadHistory != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			// Validate repository
			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Resolve beads file path (bv-history fix, respects BEADS_DIR)
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			// Build correlator options
			opts := correlation.CorrelatorOptions{
				BeadID: *beadHistory,
				Limit:  *historyLimit,
			}

			// Parse --history-since if provided
			if *historySince != "" {
				since, err := recipe.ParseRelativeTime(*historySince, time.Now())
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing --history-since: %v\n", err)
					os.Exit(1)
				}
				if !since.IsZero() {
					opts.Since = &since
				}
			}

			// Convert issues to BeadInfo for correlator
			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			// Generate report with explicit beads path
			correlator := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlator.GenerateReport(beadInfos, opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Apply confidence filter if specified
			if *minConfidence > 0 {
				scorer := correlation.NewScorer()
				report.Histories = scorer.FilterHistoriesByConfidence(report.Histories, *minConfidence)

				// Rebuild commit index after filtering
				report.CommitIndex = make(correlation.CommitIndex)
				for beadID, history := range report.Histories {
					for _, commit := range history.Commits {
						report.CommitIndex[commit.SHA] = append(report.CommitIndex[commit.SHA], beadID)
					}
				}

				// Update stats
				report.Stats.BeadsWithCommits = 0
				for _, history := range report.Histories {
					if len(history.Commits) > 0 {
						report.Stats.BeadsWithCommits++
					}
				}
			}

			// Output JSON
			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(report); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding history report: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-correlation-stats", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-explain-correlation", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-confirm-correlation", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-reject-correlation", robotDispatchContext)

		// Handle correlation audit commands (bv-e1u6)
		if *robotExplainCorrelation != "" || *robotConfirmCorrelation != "" || *robotRejectCorrelation != "" || *robotCorrelationStats {
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}

			feedbackStore := correlation.NewFeedbackStore(beadsDir)
			if err := feedbackStore.Load(); err != nil {
				fmt.Fprintf(os.Stderr, "Error loading feedback: %v\n", err)
				os.Exit(1)
			}

			// Handle --robot-correlation-stats
			if *robotCorrelationStats {
				stats := feedbackStore.GetStats()
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(stats); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding stats: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}

			// Handle --robot-explain-correlation
			if *robotExplainCorrelation != "" {
				commitSHA, beadID, err := parseCorrelationArg(*robotExplainCorrelation)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}

				// Generate history report to find the correlation
				cwd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
					os.Exit(1)
				}
				beadsPath, err := loader.FindJSONLPath(beadsDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
					os.Exit(1)
				}
				correlator := correlation.NewCorrelator(cwd, beadsPath)

				beadInfos := make([]correlation.BeadInfo, len(issues))
				for i, issue := range issues {
					beadInfos[i] = correlation.BeadInfo{
						ID:     issue.ID,
						Title:  issue.Title,
						Status: string(issue.Status),
					}
				}

				opts := correlation.CorrelatorOptions{BeadID: beadID}
				report, err := correlator.GenerateReport(beadInfos, opts)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error generating report: %v\n", err)
					os.Exit(1)
				}

				// Find the specific commit
				history, ok := report.Histories[beadID]
				if !ok {
					fmt.Fprintf(os.Stderr, "Bead not found: %s\n", beadID)
					os.Exit(1)
				}

				targetCommit, err := resolveCorrelatedCommit(history.Commits, commitSHA)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if targetCommit == nil {
					fmt.Fprintf(os.Stderr, "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
					os.Exit(1)
				}

				// Generate explanation
				scorer := correlation.NewScorer()
				explanation := scorer.BuildExplanation(*targetCommit, beadID)

				// Check for existing feedback
				if fb, ok := feedbackStore.Get(targetCommit.SHA, beadID); ok {
					explanation.Recommendation = fmt.Sprintf("Already has feedback: %s", fb.Type)
				}

				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(explanation); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding explanation: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}

			// Handle --robot-confirm-correlation
			if *robotConfirmCorrelation != "" {
				commitSHA, beadID, err := parseCorrelationArg(*robotConfirmCorrelation)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}

				feedbackBy := *correlationFeedbackBy
				if feedbackBy == "" {
					feedbackBy = "cli"
				}

				// Get original confidence from history
				cwd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
					os.Exit(1)
				}
				beadsPath, err := loader.FindJSONLPath(beadsDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
					os.Exit(1)
				}
				correlator := correlation.NewCorrelator(cwd, beadsPath)

				beadInfos := make([]correlation.BeadInfo, len(issues))
				for i, issue := range issues {
					beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
				}

				opts := correlation.CorrelatorOptions{BeadID: beadID}
				report, err := correlator.GenerateReport(beadInfos, opts)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error generating report: %v\n", err)
					os.Exit(1)
				}

				history, ok := report.Histories[beadID]
				if !ok {
					fmt.Fprintf(os.Stderr, "Bead not found: %s\n", beadID)
					os.Exit(1)
				}
				targetCommit, err := resolveCorrelatedCommit(history.Commits, commitSHA)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if targetCommit == nil {
					fmt.Fprintf(os.Stderr, "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
					os.Exit(1)
				}
				originalConf := targetCommit.Confidence
				commitSHA = targetCommit.SHA // Use full SHA

				if err := feedbackStore.Confirm(commitSHA, beadID, feedbackBy, originalConf, *correlationFeedbackReason); err != nil {
					fmt.Fprintf(os.Stderr, "Error saving feedback: %v\n", err)
					os.Exit(1)
				}

				result := map[string]interface{}{
					"status":    "confirmed",
					"commit":    commitSHA,
					"bead":      beadID,
					"by":        feedbackBy,
					"reason":    *correlationFeedbackReason,
					"orig_conf": originalConf,
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(result); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding result: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}

			// Handle --robot-reject-correlation
			if *robotRejectCorrelation != "" {
				commitSHA, beadID, err := parseCorrelationArg(*robotRejectCorrelation)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}

				feedbackBy := *correlationFeedbackBy
				if feedbackBy == "" {
					feedbackBy = "cli"
				}

				// Get original confidence from history
				cwd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
					os.Exit(1)
				}
				beadsPath, err := loader.FindJSONLPath(beadsDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
					os.Exit(1)
				}
				correlator := correlation.NewCorrelator(cwd, beadsPath)

				beadInfos := make([]correlation.BeadInfo, len(issues))
				for i, issue := range issues {
					beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
				}

				opts := correlation.CorrelatorOptions{BeadID: beadID}
				report, err := correlator.GenerateReport(beadInfos, opts)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error generating report: %v\n", err)
					os.Exit(1)
				}

				history, ok := report.Histories[beadID]
				if !ok {
					fmt.Fprintf(os.Stderr, "Bead not found: %s\n", beadID)
					os.Exit(1)
				}
				targetCommit, err := resolveCorrelatedCommit(history.Commits, commitSHA)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if targetCommit == nil {
					fmt.Fprintf(os.Stderr, "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
					os.Exit(1)
				}
				originalConf := targetCommit.Confidence
				commitSHA = targetCommit.SHA // Use full SHA

				if err := feedbackStore.Reject(commitSHA, beadID, feedbackBy, originalConf, *correlationFeedbackReason); err != nil {
					fmt.Fprintf(os.Stderr, "Error saving feedback: %v\n", err)
					os.Exit(1)
				}

				result := map[string]interface{}{
					"status":    "rejected",
					"commit":    commitSHA,
					"bead":      beadID,
					"by":        feedbackBy,
					"reason":    *correlationFeedbackReason,
					"orig_conf": originalConf,
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(result); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding result: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-orphans", robotDispatchContext)

		// Handle --robot-orphans flag (bv-jdop)
		if *robotOrphans {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			// Validate repository
			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Get beads path
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			// Convert issues to BeadInfo
			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			// Generate history report first (to get existing correlations)
			correlator := correlation.NewCorrelator(cwd, beadsPath)
			correlatorOpts := correlation.CorrelatorOptions{
				Limit: *historyLimit,
			}

			report, err := correlator.GenerateReport(beadInfos, correlatorOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Detect orphans using OrphanDetector
			detector := correlation.NewOrphanDetector(report, cwd)
			extractOpts := correlation.ExtractOptions{
				Limit: *historyLimit,
			}
			orphanReport, err := detector.DetectOrphans(extractOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error detecting orphans: %v\n", err)
				os.Exit(1)
			}

			filterOrphanReportByMinScore(orphanReport, *orphansMinScore)

			// Wrap orphan report with standard envelope fields
			type OrphanOutputEnvelope struct {
				*correlation.OrphanReport
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
			}
			output := OrphanOutputEnvelope{
				OrphanReport: orphanReport,
				OutputFormat: robotOutputFormat,
				Version:      version.Version,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding orphan report: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		if !*fileHotspots {
			dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-file-beads", robotDispatchContext)
		}

		// Handle --robot-file-beads and --robot-file-hotspots flags (bv-hmib)
		if *robotFileBeads != "" || *fileHotspots {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			// Validate repository
			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Resolve beads file path
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			// Convert issues to BeadInfo for correlator
			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			// Generate history report first
			correlator := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlator.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Create file lookup
			fileLookup := correlation.NewFileLookup(report)

			encoder := newRobotEncoder(os.Stdout)

			if *fileHotspots {
				// Output hotspots
				type HotspotsOutput struct {
					RobotEnvelope
					Hotspots []correlation.FileHotspot  `json:"hotspots"`
					Stats    correlation.FileIndexStats `json:"stats"`
				}

				hotspots := fileLookup.GetHotspots(*hotspotsLimit)
				output := HotspotsOutput{
					RobotEnvelope: NewRobotEnvelope(report.DataHash),
					Hotspots:      hotspots,
					Stats:         fileLookup.GetStats(),
				}

				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding hotspots: %v\n", err)
					os.Exit(1)
				}
			} else {
				// Output file-beads lookup
				result := fileLookup.LookupByFile(*robotFileBeads)

				// Limit closed beads if specified
				if len(result.ClosedBeads) > *fileBeadsLimit {
					result.ClosedBeads = result.ClosedBeads[:*fileBeadsLimit]
				}

				type FileBeadsOutput struct {
					RobotEnvelope
					FilePath    string                      `json:"file_path"`
					TotalBeads  int                         `json:"total_beads"`
					OpenBeads   []correlation.BeadReference `json:"open_beads"`
					ClosedBeads []correlation.BeadReference `json:"closed_beads"`
				}

				output := FileBeadsOutput{
					RobotEnvelope: NewRobotEnvelope(report.DataHash),
					FilePath:      *robotFileBeads,
					TotalBeads:    result.TotalBeads,
					OpenBeads:     result.OpenBeads,
					ClosedBeads:   result.ClosedBeads,
				}

				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding file beads: %v\n", err)
					os.Exit(1)
				}
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-impact", robotDispatchContext)

		// Handle --robot-impact flag (bv-19pq)
		if *robotImpact != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			correlator := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlator.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			fileLookup := correlation.NewFileLookup(report)
			files := strings.Split(*robotImpact, ",")
			for i := range files {
				files[i] = strings.TrimSpace(files[i])
			}

			impactResult := fileLookup.ImpactAnalysis(files)

			type ImpactOutput struct {
				RobotEnvelope
				Files         []string                   `json:"files"`
				RiskLevel     string                     `json:"risk_level"`
				RiskScore     float64                    `json:"risk_score"`
				Summary       string                     `json:"summary"`
				Warnings      []string                   `json:"warnings"`
				AffectedBeads []correlation.AffectedBead `json:"affected_beads"`
			}

			output := ImpactOutput{
				RobotEnvelope: NewRobotEnvelope(report.DataHash),
				Files:         impactResult.Files,
				RiskLevel:     impactResult.RiskLevel,
				RiskScore:     impactResult.RiskScore,
				Summary:       impactResult.Summary,
				Warnings:      impactResult.Warnings,
				AffectedBeads: impactResult.AffectedBeads,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding impact analysis: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-file-relations", robotDispatchContext)

		// Handle --robot-file-relations flag (bv-7a2f)
		if *robotFileRelations != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			issues, err := datasource.LoadIssues(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				os.Exit(1)
			}

			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			correlator := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlator.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			fileLookup := correlation.NewFileLookup(report)
			result := fileLookup.GetRelatedFiles(*robotFileRelations, *relationsThreshold, *relationsLimit)

			type RelationsOutput struct {
				RobotEnvelope
				FilePath     string                      `json:"file_path"`
				TotalCommits int                         `json:"total_commits"`
				Threshold    float64                     `json:"threshold"`
				RelatedFiles []correlation.CoChangeEntry `json:"related_files"`
			}

			output := RelationsOutput{
				RobotEnvelope: NewRobotEnvelope(report.DataHash),
				FilePath:      result.FilePath,
				TotalCommits:  result.TotalCommits,
				Threshold:     result.Threshold,
				RelatedFiles:  result.RelatedFiles,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding file relations: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-related", robotDispatchContext)

		// Handle --robot-related flag (bv-jtdl)
		if *robotRelatedWork != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			issues, err := datasource.LoadIssues(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				os.Exit(1)
			}

			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			correlatorObj := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlatorObj.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Build dependency graph from issues
			depGraph := make(map[string][]string)
			for _, issue := range issues {
				for _, dep := range issue.Dependencies {
					if dep == nil {
						continue
					}
					depGraph[issue.ID] = append(depGraph[issue.ID], dep.DependsOnID)
				}
			}

			// Configure options
			opts := correlation.RelatedWorkOptions{
				MinRelevance:      relatedMinRelevanceFlag.Value(),
				MaxResults:        *relatedMaxResults,
				ConcurrencyWindow: 7 * 24 * time.Hour,
				IncludeClosed:     *relatedIncludeClosed,
				DependencyGraph:   depGraph,
			}

			result := report.FindRelatedWork(*robotRelatedWork, opts)
			if result == nil {
				fmt.Fprintf(os.Stderr, "Bead not found in history: %s\n", *robotRelatedWork)
				os.Exit(1)
			}

			// Add envelope fields to output
			type RelatedWorkOutput struct {
				*correlation.RelatedWorkResult
				DataHash     string `json:"data_hash"`
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
			}

			output := RelatedWorkOutput{
				RelatedWorkResult: result,
				DataHash:          report.DataHash,
				OutputFormat:      robotOutputFormat,
				Version:           version.Version,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding related work: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-blocker-chain", robotDispatchContext)

		// Handle --robot-blocker-chain flag (bv-nlo0)
		if *robotBlockerChain != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			issues, err := datasource.LoadIssues(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				os.Exit(1)
			}

			an := analysis.NewAnalyzer(issues)
			result := an.GetBlockerChain(*robotBlockerChain)

			if result == nil {
				fmt.Fprintf(os.Stderr, "Issue not found: %s\n", *robotBlockerChain)
				os.Exit(1)
			}

			type BlockerChainOutput struct {
				RobotEnvelope
				Result *analysis.BlockerChainResult `json:"result"`
			}

			// Compute data hash for consistency
			dataHash := analysis.ComputeDataHash(issues)

			output := BlockerChainOutput{
				RobotEnvelope: NewRobotEnvelope(dataHash),
				Result:        result,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding blocker chain: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-impact-network", robotDispatchContext)

		// Handle --robot-impact-network flag (bv-48kr)
		// Use "all" for full network or a bead ID for subnetwork
		if *robotImpactNetwork != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Find beads path
			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			// Load issues
			issues, err := datasource.LoadIssues(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				os.Exit(1)
			}

			// Convert to BeadInfo slice
			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			// Generate history report
			correlator := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlator.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Build impact network
			builder := correlation.NewNetworkBuilderWithIssues(report, issues)
			network := builder.Build()

			// Determine if specific bead or full network
			beadID := ""
			if *robotImpactNetwork != "all" {
				beadID = *robotImpactNetwork
			}
			if beadID != "" {
				if _, ok := network.Nodes[beadID]; !ok {
					fmt.Fprintf(os.Stderr, "Bead not found in network: %s\n", beadID)
					os.Exit(1)
				}
			}

			// Cap depth to reasonable range
			depth := *networkDepth
			if depth < 1 {
				depth = 1
			}
			if depth > 3 {
				depth = 3
			}

			// Generate result and wrap with envelope fields
			result := network.ToResult(beadID, depth)

			type ImpactNetworkEnvelope struct {
				*correlation.ImpactNetworkResult
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
			}
			output := ImpactNetworkEnvelope{
				ImpactNetworkResult: result,
				OutputFormat:        robotOutputFormat,
				Version:             version.Version,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding impact network: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-causality", robotDispatchContext)

		// Handle --robot-causality flag (bv-j74w)
		if *robotCausality != "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			if err := correlation.ValidateRepository(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			issues, err := datasource.LoadIssues(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
				os.Exit(1)
			}

			beadsDir, err := loader.GetBeadsDir("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting beads directory: %v\n", err)
				os.Exit(1)
			}
			beadsPath, err := loader.FindJSONLPath(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding beads file: %v\n", err)
				os.Exit(1)
			}

			beadInfos := make([]correlation.BeadInfo, len(issues))
			for i, issue := range issues {
				beadInfos[i] = correlation.BeadInfo{
					ID:     issue.ID,
					Title:  issue.Title,
					Status: string(issue.Status),
				}
			}

			correlatorObj := correlation.NewCorrelator(cwd, beadsPath)
			report, err := correlatorObj.GenerateReport(beadInfos, correlation.CorrelatorOptions{
				Limit: *historyLimit,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating history report: %v\n", err)
				os.Exit(1)
			}

			// Build blocker titles map for better descriptions
			blockerTitles := make(map[string]string)
			for _, issue := range issues {
				blockerTitles[issue.ID] = issue.Title
			}

			opts := correlation.CausalityOptions{
				IncludeCommits: true,
				BlockerTitles:  blockerTitles,
			}

			result := report.BuildCausalityChain(*robotCausality, opts)
			if result == nil {
				fmt.Fprintf(os.Stderr, "Bead not found: %s\n", *robotCausality)
				os.Exit(1)
			}

			// Wrap with envelope fields
			type CausalityEnvelope struct {
				*correlation.CausalityResult
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
			}
			output := CausalityEnvelope{
				CausalityResult: result,
				OutputFormat:    robotOutputFormat,
				Version:         version.Version,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding causality result: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-sprint-list", robotDispatchContext)
		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-sprint-show", robotDispatchContext)

		// Handle --robot-sprint-list and --robot-sprint-show flags (bv-156)
		if *robotSprintList || *robotSprintShow != "" {
			if *robotSprintShow == "" {
				dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-sprint-list", robotDispatchContext)
			}

			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			sprints, err := loader.LoadSprints(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading sprints: %v\n", err)
				os.Exit(1)
			}

			dataHash := analysis.ComputeDataHash(issues)

			if *robotSprintShow != "" {
				// Find specific sprint
				var found *model.Sprint
				for i := range sprints {
					if sprints[i].ID == *robotSprintShow {
						found = &sprints[i]
						break
					}
				}
				if found == nil {
					fmt.Fprintf(os.Stderr, "Sprint not found: %s\n", *robotSprintShow)
					os.Exit(1)
				}
				// Wrap sprint with standard envelope
				type SprintShowOutput struct {
					RobotEnvelope
					Sprint *model.Sprint `json:"sprint"`
				}
				output := SprintShowOutput{
					RobotEnvelope: NewRobotEnvelope(dataHash),
					Sprint:        found,
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding sprint: %v\n", err)
					os.Exit(1)
				}
			} else {
				// Output all sprints as JSON
				output := struct {
					RobotEnvelope
					SprintCount int            `json:"sprint_count"`
					Sprints     []model.Sprint `json:"sprints"`
				}{
					RobotEnvelope: NewRobotEnvelope(dataHash),
					SprintCount:   len(sprints),
					Sprints:       sprints,
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding sprints: %v\n", err)
					os.Exit(1)
				}
			}
			os.Exit(0)
		}

		// Handle --robot-burndown flag (bv-159)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-burndown", robotDispatchContext)

		// Handle --robot-forecast flag (bv-158)
		dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-forecast", robotDispatchContext)

		dispatchRobotFlagOrExit(&phaseThreeRobotRegistry, "robot-capacity", robotDispatchContext)

		// Handle --robot-capacity flag (bv-160)
		if *robotCapacity {
			// Build graph stats for analysis
			analyzer := analysis.NewAnalyzer(issues)
			graphStats := analyzer.Analyze()

			// Filter issues by label if specified
			targetIssues := issues
			if *capacityLabel != "" {
				filtered := make([]model.Issue, 0)
				for _, iss := range issues {
					for _, l := range iss.Labels {
						if l == *capacityLabel {
							filtered = append(filtered, iss)
							break
						}
					}
				}
				targetIssues = filtered
			}

			// Calculate open issues only
			openIssues := make([]model.Issue, 0)
			issueMap := make(map[string]model.Issue)
			for _, iss := range targetIssues {
				issueMap[iss.ID] = iss
				if iss.Status != model.StatusClosed {
					openIssues = append(openIssues, iss)
				}
			}

			now := time.Now()
			agents := *capacityAgents
			if agents <= 0 {
				agents = 1
			}

			// Calculate total work remaining
			medianMinutes := 60 // default
			totalMinutes := 0
			for _, iss := range openIssues {
				eta, err := analysis.EstimateETAForIssue(targetIssues, &graphStats, iss.ID, 1, now)
				if err == nil {
					totalMinutes += eta.EstimatedMinutes
				}
			}

			// Analyze parallelizability by finding dependency chains
			// Serial work = longest chain (critical path)
			// Parallelizable = work that can run concurrently

			// Build dependency adjacency for open issues
			blockedBy := make(map[string][]string) // issue -> its blockers
			blocks := make(map[string][]string)    // issue -> issues it blocks
			for _, iss := range openIssues {
				for _, dep := range iss.Dependencies {
					if dep == nil {
						continue
					}
					depID := dep.DependsOnID
					if _, exists := issueMap[depID]; exists {
						blockedBy[iss.ID] = append(blockedBy[iss.ID], depID)
						blocks[depID] = append(blocks[depID], iss.ID)
					}
				}
			}

			// Find issues with no blockers (can start immediately)
			actionable := make([]string, 0)
			for _, iss := range openIssues {
				hasOpenBlocker := false
				for _, depID := range blockedBy[iss.ID] {
					if dep, ok := issueMap[depID]; ok && dep.Status != model.StatusClosed {
						hasOpenBlocker = true
						break
					}
				}
				if !hasOpenBlocker {
					actionable = append(actionable, iss.ID)
				}
			}

			// Calculate critical path (longest chain)
			var longestChain []string
			var dfs func(id string, path []string)
			visited := make(map[string]bool)
			dfs = func(id string, path []string) {
				if visited[id] {
					return
				}
				visited[id] = true
				path = append(path, id)
				if len(path) > len(longestChain) {
					longestChain = make([]string, len(path))
					copy(longestChain, path)
				}
				for _, nextID := range blocks[id] {
					if dep, ok := issueMap[nextID]; ok && dep.Status != model.StatusClosed {
						dfs(nextID, path)
					}
				}
				visited[id] = false
			}
			for _, startID := range actionable {
				dfs(startID, nil)
			}

			// Calculate serial minutes (work on critical path)
			serialMinutes := 0
			for _, id := range longestChain {
				eta, err := analysis.EstimateETAForIssue(targetIssues, &graphStats, id, 1, now)
				if err == nil {
					serialMinutes += eta.EstimatedMinutes
				}
			}

			// Parallelizable percentage
			parallelizablePct := 0.0
			if totalMinutes > 0 {
				parallelizablePct = float64(totalMinutes-serialMinutes) / float64(totalMinutes) * 100
			}

			// Calculate estimated completion with N agents
			// Serial work must be done sequentially, parallel work can be divided
			parallelMinutes := totalMinutes - serialMinutes
			effectiveMinutes := serialMinutes + parallelMinutes/agents
			estimatedDays := float64(effectiveMinutes) / (60.0 * 8.0) // 8hr workday

			// Find bottlenecks (issues blocking the most other issues)
			type Bottleneck struct {
				ID          string   `json:"id"`
				Title       string   `json:"title"`
				BlocksCount int      `json:"blocks_count"`
				Blocks      []string `json:"blocks,omitempty"`
			}
			bottlenecks := make([]Bottleneck, 0)
			for _, iss := range openIssues {
				if len(blocks[iss.ID]) > 1 {
					blockedIssues := blocks[iss.ID]
					bottlenecks = append(bottlenecks, Bottleneck{
						ID:          iss.ID,
						Title:       iss.Title,
						BlocksCount: len(blockedIssues),
						Blocks:      blockedIssues,
					})
				}
			}
			// Sort by blocks count descending
			sort.Slice(bottlenecks, func(i, j int) bool {
				return bottlenecks[i].BlocksCount > bottlenecks[j].BlocksCount
			})
			if len(bottlenecks) > 5 {
				bottlenecks = bottlenecks[:5]
			}

			// Build output
			type CapacityOutput struct {
				RobotEnvelope
				Agents            int          `json:"agents"`
				Label             string       `json:"label,omitempty"`
				OpenIssueCount    int          `json:"open_issue_count"`
				TotalMinutes      int          `json:"total_minutes"`
				TotalDays         float64      `json:"total_days"`
				SerialMinutes     int          `json:"serial_minutes"`
				ParallelMinutes   int          `json:"parallel_minutes"`
				ParallelizablePct float64      `json:"parallelizable_pct"`
				EstimatedDays     float64      `json:"estimated_days"`
				CriticalPathLen   int          `json:"critical_path_length"`
				CriticalPath      []string     `json:"critical_path,omitempty"`
				ActionableCount   int          `json:"actionable_count"`
				Actionable        []string     `json:"actionable,omitempty"`
				Bottlenecks       []Bottleneck `json:"bottlenecks,omitempty"`
			}

			output := CapacityOutput{
				RobotEnvelope:     NewRobotEnvelope(analysis.ComputeDataHash(issues)),
				Agents:            agents,
				OpenIssueCount:    len(openIssues),
				TotalMinutes:      totalMinutes,
				TotalDays:         float64(totalMinutes) / (60.0 * 8.0),
				SerialMinutes:     serialMinutes,
				ParallelMinutes:   parallelMinutes,
				ParallelizablePct: parallelizablePct,
				EstimatedDays:     estimatedDays,
				CriticalPathLen:   len(longestChain),
				CriticalPath:      longestChain,
				ActionableCount:   len(actionable),
				Actionable:        actionable,
				Bottlenecks:       bottlenecks,
			}
			if *capacityLabel != "" {
				output.Label = *capacityLabel
			}

			// Suppress unused variable warning
			_ = medianMinutes

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding capacity: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}

		// Handle --robot-metrics flag (bv-84tp)
		dispatchRobotFlagOrExit(&phaseOneRobotRegistry, "robot-metrics", robotDispatchContext)

		// Handle --diff-since flag
		if *diffSince != "" {
			// Auto-enable robot diff for non-interactive/agent contexts
			if !*robotDiff && (envRobot || !stdoutIsTTY) {
				*robotDiff = true
			}

			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
				os.Exit(1)
			}

			gitLoader := loader.NewGitLoader(cwd)

			// Load historical issues
			historicalIssues, err := gitLoader.LoadAt(*diffSince)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading issues at %s: %v\n", *diffSince, err)
				os.Exit(1)
			}

			// Get revision info for timestamp
			revision, err := gitLoader.ResolveRevision(*diffSince)
			if err != nil {
				revision = *diffSince
			}

			// Create snapshots
			fromSnapshot := analysis.NewSnapshotAt(historicalIssues, time.Time{}, revision)
			toSnapshot := analysis.NewSnapshot(issues)

			// Compute diff
			diff := analysis.CompareSnapshots(fromSnapshot, toSnapshot)

			if *robotDiff {
				diffDispatchContext := robotDispatchContext
				diffDispatchContext.Diff = diff
				diffDispatchContext.DiffHistoricalIssues = historicalIssues
				diffDispatchContext.DiffResolvedRevision = revision
				dispatchRobotFlagOrExit(&phaseTwoRobotRegistry, "robot-diff", diffDispatchContext)
			} else {
				// Human-readable output
				printDiffSummary(diff, *diffSince)
			}
			os.Exit(0)
		}

		// Handle --as-of flag for TUI mode (robot commands already handled above with historical data)
		if *asOf != "" {
			if len(issues) == 0 {
				fmt.Printf("No issues found at %s.\n", *asOf)
				return nil
			}

			// Launch TUI with historical issues (already loaded, no live reload)
			m := ui.NewModel(issues, activeRecipe, "")
			defer m.Stop()
			if err := runTUIProgram(m); err != nil {
				fmt.Printf("Error running beads viewer: %v\n", err)
				os.Exit(1)
			}
			return nil
		}

		if *exportFile != "" {
			fmt.Printf("Exporting to %s...\n", *exportFile)

			// Load and run pre-export hooks
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not get working directory for hooks: %v\n", cwdErr)
			}
			var executor *hooks.Executor
			if !*noHooks {
				hookLoader := hooks.NewLoader(hooks.WithProjectDir(cwd))
				if err := hookLoader.Load(); err != nil {
					fmt.Printf("Warning: failed to load hooks: %v\n", err)
				} else if hookLoader.HasHooks() {
					ctx := hooks.ExportContext{
						ExportPath:   *exportFile,
						ExportFormat: "markdown",
						IssueCount:   len(issues),
						Timestamp:    time.Now(),
					}
					executor = hooks.NewExecutor(hookLoader.Config(), ctx)

					// Run pre-export hooks
					if err := executor.RunPreExport(); err != nil {
						fmt.Printf("Error: pre-export hook failed: %v\n", err)
						os.Exit(1)
					}
				}
			}

			// Perform the export
			if err := export.SaveMarkdownToFile(issues, *exportFile); err != nil {
				fmt.Printf("Error exporting: %v\n", err)
				os.Exit(1)
			}

			// Run post-export hooks
			if executor != nil {
				if err := executor.RunPostExport(); err != nil {
					fmt.Printf("Warning: post-export hook failed: %v\n", err)
					// Don't exit, just warn
				}

				// Print hook summary if any hooks ran
				if len(executor.Results()) > 0 {
					fmt.Println(executor.Summary())
				}
			}

			fmt.Println("Done!")
			os.Exit(0)
		}

		if len(issues) == 0 {
			fmt.Println("No issues found. Create some with 'br create'!")
			os.Exit(0)
		}

		// Apply recipe filters and sorting if specified
		if activeRecipe != nil {
			issues = applyRecipeFilters(issues, activeRecipe)
			issues = applyRecipeSort(issues, activeRecipe)
		}

		// Background mode rollout (bv-o11l):
		// - CLI flags override env var
		// - env var overrides user config file
		if *backgroundMode && *noBackgroundMode {
			fmt.Fprintln(os.Stderr, "Error: --background-mode and --no-background-mode are mutually exclusive")
			os.Exit(2)
		}
		if *backgroundMode {
			_ = os.Setenv("BV_BACKGROUND_MODE", "1")
		} else if *noBackgroundMode {
			_ = os.Setenv("BV_BACKGROUND_MODE", "0")
		} else if v, ok := os.LookupEnv("BV_BACKGROUND_MODE"); ok && strings.TrimSpace(v) != "" {
			// Respect explicit user env var.
		} else if enabled, ok := loadBackgroundModeFromUserConfig(); ok {
			if enabled {
				_ = os.Setenv("BV_BACKGROUND_MODE", "1")
			} else {
				_ = os.Setenv("BV_BACKGROUND_MODE", "0")
			}
		}

		// Initial Model with live reload support
		m := ui.NewModel(issues, activeRecipe, beadsPath)
		defer m.Stop() // Clean up file watcher

		// Enable workspace mode if loading from workspace config
		if workspaceInfo != nil {
			m.EnableWorkspaceMode(ui.WorkspaceInfo{
				Enabled:      true,
				RepoCount:    workspaceInfo.TotalRepos,
				FailedCount:  workspaceInfo.FailedRepos,
				TotalIssues:  workspaceInfo.TotalIssues,
				RepoPrefixes: workspaceInfo.RepoPrefixes,
			})
		}

		// Debug render mode - output a view to file and exit
		if *debugRender != "" {
			output := m.RenderDebugView(*debugRender, *debugWidth, *debugHeight)
			fmt.Println(output)
			os.Exit(0)
		}

		// Run Program
		if err := runTUIProgram(m); err != nil {
			fmt.Printf("Error running beads viewer: %v\n", err)
			os.Exit(1)
		}

		return nil
	})
	originalArgs := append([]string{}, os.Args[1:]...)
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return enrichFlagParseError(err, cmd.Flags(), originalArgs)
	})
	normalizedArgs := rewriteAgentIntentArgs(originalArgs)
	rootCmd.SetArgs(rewriteSingleDashLongFlags(normalizedArgs, rootCmd.Flags()))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, enrichCommandParseError(err, originalArgs))
		os.Exit(1)
	}
}

func runTUIProgram(m ui.Model) error {
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithoutSignalHandler(),
	)

	runDone := make(chan struct{})
	defer close(runDone)

	// Graceful shutdown on SIGINT/SIGTERM (bv-bzt8).
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-runDone:
			return
		case <-sigCh:
		}

		p.Quit()

		select {
		case <-runDone:
			return
		case <-sigCh:
		case <-time.After(5 * time.Second):
		}

		p.Kill()
	}()

	// Optional auto-quit for automated tests: set BV_TUI_AUTOCLOSE_MS.
	if v := os.Getenv("BV_TUI_AUTOCLOSE_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			go func() {
				timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
				defer timer.Stop()

				select {
				case <-runDone:
					return
				case <-timer.C:
				}

				p.Quit()

				select {
				case <-runDone:
					return
				case <-time.After(2 * time.Second):
				}

				p.Kill()
			}()
		}
	}

	_, err := p.Run()
	if err != nil {
		if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted) {
			return nil
		}
	}
	return err
}

// countEdges counts blocking dependencies for config sizing
func countEdges(issues []model.Issue) int {
	count := 0
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type.IsBlocking() {
				count++
			}
		}
	}
	return count
}

// normalizeThemePreference canonicalizes a theme string to "light", "dark",
// "auto", or "" (unrecognized/empty). Matching is case-insensitive and
// whitespace-tolerant.
func normalizeThemePreference(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "light":
		return "light"
	case "dark":
		return "dark"
	case "auto":
		return "auto"
	default:
		return ""
	}
}

// resolveThemePreference determines the effective color-theme preference using
// the precedence: --theme flag > BV_THEME env var > ~/.config/bv/config.yaml >
// auto-detect. It returns "light", "dark", "auto", or "" when nothing is
// specified (in which case auto-detection is used). An invalid --theme value is
// treated as "auto" so the flag never silently falls through to a lower-priority
// source the user did not intend. (bv-128)
func resolveThemePreference(flagVal string, flagChanged bool) string {
	if flagChanged {
		if v := normalizeThemePreference(flagVal); v != "" {
			return v
		}
		return "auto" // explicit but invalid → honor the flag level as auto
	}
	if v := normalizeThemePreference(os.Getenv("BV_THEME")); v != "" {
		return v
	}
	if v, ok := loadThemeFromUserConfig(); ok {
		if nv := normalizeThemePreference(v); nv != "" {
			return nv
		}
	}
	return ""
}

// loadThemeFromUserConfig reads the top-level `theme:` key from
// ~/.config/bv/config.yaml. It returns (value, true) when the key is present and
// non-empty, else ("", false). Validation of the value happens in
// normalizeThemePreference. (bv-128)
func loadThemeFromUserConfig() (string, bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return "", false
	}
	configPath := filepath.Join(homeDir, ".config", "bv", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", false
	}

	var cfg struct {
		Theme string `yaml:"theme"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	if strings.TrimSpace(cfg.Theme) == "" {
		return "", false
	}
	return cfg.Theme, true
}

func loadBackgroundModeFromUserConfig() (bool, bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return false, false
	}
	configPath := filepath.Join(homeDir, ".config", "bv", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, false
	}

	var cfg struct {
		Experimental struct {
			BackgroundMode *bool `yaml:"background_mode"`
		} `yaml:"experimental"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false, false
	}
	if cfg.Experimental.BackgroundMode == nil {
		return false, false
	}
	return *cfg.Experimental.BackgroundMode, true
}

// printDiffSummary prints a human-readable diff summary
func printDiffSummary(diff *analysis.SnapshotDiff, since string) {
	fmt.Printf("Changes since %s\n", since)
	fmt.Println("=" + repeatChar('=', len("Changes since "+since)))
	fmt.Println()

	// Health trend
	trendEmoji := "→"
	switch diff.Summary.HealthTrend {
	case "improving":
		trendEmoji = "↑"
	case "degrading":
		trendEmoji = "↓"
	}
	fmt.Printf("Health Trend: %s %s\n\n", trendEmoji, diff.Summary.HealthTrend)

	// Summary counts
	fmt.Println("Summary:")
	if diff.Summary.IssuesAdded > 0 {
		fmt.Printf("  + %d new issues\n", diff.Summary.IssuesAdded)
	}
	if diff.Summary.IssuesClosed > 0 {
		fmt.Printf("  ✓ %d issues closed\n", diff.Summary.IssuesClosed)
	}
	if diff.Summary.IssuesRemoved > 0 {
		fmt.Printf("  - %d issues removed\n", diff.Summary.IssuesRemoved)
	}
	if diff.Summary.IssuesReopened > 0 {
		fmt.Printf("  ↺ %d issues reopened\n", diff.Summary.IssuesReopened)
	}
	if diff.Summary.IssuesModified > 0 {
		fmt.Printf("  ~ %d issues modified\n", diff.Summary.IssuesModified)
	}
	if diff.Summary.CyclesIntroduced > 0 {
		fmt.Printf("  ⚠ %d new cycles introduced\n", diff.Summary.CyclesIntroduced)
	}
	if diff.Summary.CyclesResolved > 0 {
		fmt.Printf("  ✓ %d cycles resolved\n", diff.Summary.CyclesResolved)
	}
	fmt.Println()

	// New issues
	if len(diff.NewIssues) > 0 {
		fmt.Println("New Issues:")
		for _, issue := range diff.NewIssues {
			fmt.Printf("  + [%s] %s (P%d)\n", issue.ID, issue.Title, issue.Priority)
		}
		fmt.Println()
	}

	// Closed issues
	if len(diff.ClosedIssues) > 0 {
		fmt.Println("Closed Issues:")
		for _, issue := range diff.ClosedIssues {
			fmt.Printf("  ✓ [%s] %s\n", issue.ID, issue.Title)
		}
		fmt.Println()
	}

	// Reopened issues
	if len(diff.ReopenedIssues) > 0 {
		fmt.Println("Reopened Issues:")
		for _, issue := range diff.ReopenedIssues {
			fmt.Printf("  ↺ [%s] %s\n", issue.ID, issue.Title)
		}
		fmt.Println()
	}

	// Modified issues (show first 10)
	if len(diff.ModifiedIssues) > 0 {
		fmt.Println("Modified Issues:")
		shown := 0
		for _, mod := range diff.ModifiedIssues {
			if shown >= 10 {
				fmt.Printf("  ... and %d more\n", len(diff.ModifiedIssues)-10)
				break
			}
			fmt.Printf("  ~ [%s] %s\n", mod.IssueID, mod.Title)
			for _, change := range mod.Changes {
				fmt.Printf("      %s: %s → %s\n", change.Field, change.OldValue, change.NewValue)
			}
			shown++
		}
		fmt.Println()
	}

	// New cycles
	if len(diff.NewCycles) > 0 {
		fmt.Println("⚠ New Circular Dependencies:")
		for _, cycle := range diff.NewCycles {
			fmt.Printf("  %s\n", formatCycle(cycle))
		}
		fmt.Println()
	}

	// Metric deltas
	fmt.Println("Metric Changes:")
	if diff.MetricDeltas.TotalIssues != 0 {
		fmt.Printf("  Total issues: %+d\n", diff.MetricDeltas.TotalIssues)
	}
	if diff.MetricDeltas.OpenIssues != 0 {
		fmt.Printf("  Open issues: %+d\n", diff.MetricDeltas.OpenIssues)
	}
	if diff.MetricDeltas.BlockedIssues != 0 {
		fmt.Printf("  Blocked issues: %+d\n", diff.MetricDeltas.BlockedIssues)
	}
	if diff.MetricDeltas.CycleCount != 0 {
		fmt.Printf("  Cycles: %+d\n", diff.MetricDeltas.CycleCount)
	}
}

// repeatChar creates a string of n repeated characters
func repeatChar(c rune, n int) string {
	result := make([]rune, n)
	for i := range result {
		result[i] = c
	}
	return string(result)
}

// formatCycle formats a cycle for display
func formatCycle(cycle []string) string {
	if len(cycle) == 0 {
		return "(empty)"
	}
	result := cycle[0]
	for i := 1; i < len(cycle); i++ {
		result += " → " + cycle[i]
	}
	result += " → " + cycle[0]
	return result
}

// naturalLess compares two strings using natural sort order (numeric parts sorted numerically)
func naturalLess(s1, s2 string) bool {
	// Simple heuristic: if both strings end with numbers, compare the prefix then the number
	// e.g. "bv-2" vs "bv-10" -> "bv-" == "bv-", 2 < 10

	// Helper to split into prefix and numeric suffix
	split := func(s string) (string, int, bool) {
		lastDigit := -1
		for i := len(s) - 1; i >= 0; i-- {
			if s[i] >= '0' && s[i] <= '9' {
				lastDigit = i
			} else {
				break
			}
		}
		if lastDigit == -1 {
			return s, 0, false
		}
		// If the whole string is number, prefix is empty
		prefix := s[:lastDigit]
		numStr := s[lastDigit:]
		num, err := strconv.Atoi(numStr)
		if err != nil {
			return s, 0, false
		}
		return prefix, num, true
	}

	p1, n1, ok1 := split(s1)
	p2, n2, ok2 := split(s2)

	if ok1 && ok2 && p1 == p2 {
		return n1 < n2
	}

	return s1 < s2
}

// applyRecipeFilters filters issues based on recipe configuration
func applyRecipeFilters(issues []model.Issue, r *recipe.Recipe) []model.Issue {
	if r == nil {
		return issues
	}

	f := r.Filters
	now := time.Now()

	// Build a set of open blocker IDs for actionable filtering
	openBlockers := make(map[string]bool)
	for _, issue := range issues {
		if issue.Status != model.StatusClosed {
			openBlockers[issue.ID] = true
		}
	}

	var result []model.Issue
	for _, issue := range issues {
		// Status filter
		if len(f.Status) > 0 {
			match := false
			for _, s := range f.Status {
				if strings.EqualFold(string(issue.Status), s) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		// Priority filter
		if len(f.Priority) > 0 {
			match := false
			for _, p := range f.Priority {
				if issue.Priority == p {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		// Tags filter (must have all)
		if len(f.Tags) > 0 {
			match := true
			for _, tag := range f.Tags {
				found := false
				for _, label := range issue.Labels {
					if strings.EqualFold(label, tag) {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}

		// ExcludeTags filter
		if len(f.ExcludeTags) > 0 {
			excluded := false
			for _, excludeTag := range f.ExcludeTags {
				for _, label := range issue.Labels {
					if strings.EqualFold(label, excludeTag) {
						excluded = true
						break
					}
				}
				if excluded {
					break
				}
			}
			if excluded {
				continue
			}
		}

		// CreatedAfter filter
		if f.CreatedAfter != "" {
			threshold, err := recipe.ParseRelativeTime(f.CreatedAfter, now)
			if err == nil && !issue.CreatedAt.IsZero() && issue.CreatedAt.Before(threshold) {
				continue
			}
		}

		// CreatedBefore filter
		if f.CreatedBefore != "" {
			threshold, err := recipe.ParseRelativeTime(f.CreatedBefore, now)
			if err == nil && !issue.CreatedAt.IsZero() && issue.CreatedAt.After(threshold) {
				continue
			}
		}

		// UpdatedAfter filter
		if f.UpdatedAfter != "" {
			threshold, err := recipe.ParseRelativeTime(f.UpdatedAfter, now)
			if err == nil && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.Before(threshold) {
				continue
			}
		}

		// UpdatedBefore filter
		if f.UpdatedBefore != "" {
			threshold, err := recipe.ParseRelativeTime(f.UpdatedBefore, now)
			if err == nil && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.After(threshold) {
				continue
			}
		}

		// HasBlockers filter
		if f.HasBlockers != nil {
			hasOpenBlockers := false
			for _, dep := range issue.Dependencies {
				if dep != nil && dep.Type.IsBlocking() && openBlockers[dep.DependsOnID] {
					hasOpenBlockers = true
					break
				}
			}
			if *f.HasBlockers != hasOpenBlockers {
				continue
			}
		}

		// Actionable filter (no open blockers)
		if f.Actionable != nil && *f.Actionable {
			hasOpenBlockers := false
			for _, dep := range issue.Dependencies {
				if dep != nil && dep.Type.IsBlocking() && openBlockers[dep.DependsOnID] {
					hasOpenBlockers = true
					break
				}
			}
			if hasOpenBlockers {
				continue
			}
		}

		// TitleContains filter
		if f.TitleContains != "" {
			if !strings.Contains(strings.ToLower(issue.Title), strings.ToLower(f.TitleContains)) {
				continue
			}
		}

		// IDPrefix filter
		if f.IDPrefix != "" {
			if !strings.HasPrefix(issue.ID, f.IDPrefix) {
				continue
			}
		}

		result = append(result, issue)
	}

	return result
}

// applyRecipeSort sorts issues based on recipe configuration
func applyRecipeSort(issues []model.Issue, r *recipe.Recipe) []model.Issue {
	if r == nil || r.Sort.Field == "" {
		return issues
	}

	s := r.Sort
	ascending := s.Direction != "desc"

	// For priority, default to ascending (P0 first)
	if s.Field == "priority" && s.Direction == "" {
		ascending = true
	}
	// For dates, default to descending (newest first)
	if (s.Field == "created" || s.Field == "updated") && s.Direction == "" {
		ascending = false
	}

	sort.SliceStable(issues, func(i, j int) bool {
		// For descending, swap comparison operands
		a, b := i, j
		if !ascending {
			a, b = j, i
		}

		switch s.Field {
		case "priority":
			return issues[a].Priority < issues[b].Priority
		case "created":
			return issues[a].CreatedAt.Before(issues[b].CreatedAt)
		case "updated":
			return issues[a].UpdatedAt.Before(issues[b].UpdatedAt)
		case "title":
			return strings.ToLower(issues[a].Title) < strings.ToLower(issues[b].Title)
		case "id":
			return naturalLess(issues[a].ID, issues[b].ID)
		case "status":
			return issues[a].Status < issues[b].Status
		default:
			// Unknown sort field, maintain order
			return false
		}
	})

	return issues
}

// runProfileStartup runs profiled startup analysis and outputs results
func runProfileStartup(issues []model.Issue, loadDuration time.Duration, jsonOutput bool, forceFullAnalysis bool) {
	// Get actual beads path (respects BEADS_DIR)
	beadsDir, _ := loader.GetBeadsDir("")
	dataPath, _ := loader.FindJSONLPath(beadsDir)
	if dataPath == "" {
		dataPath = beadsDir // fallback
	}

	// Time analyzer construction
	buildStart := time.Now()
	analyzer := analysis.NewAnalyzer(issues)
	buildDuration := time.Since(buildStart)

	// Select config
	var config analysis.AnalysisConfig
	if forceFullAnalysis {
		config = analysis.FullAnalysisConfig()
	} else {
		nodeCount := len(issues)
		// Estimate edge count from issues
		edgeCount := 0
		for _, issue := range issues {
			edgeCount += len(issue.Dependencies)
		}
		config = analysis.ConfigForSize(nodeCount, edgeCount)
	}

	// Run profiled analysis
	_, profile := analyzer.AnalyzeWithProfile(config)

	// Add load and build durations to profile
	profile.BuildGraph = buildDuration

	// Calculate total including load
	totalWithLoad := loadDuration + profile.Total

	if jsonOutput {
		// JSON output
		output := struct {
			GeneratedAt     string                   `json:"generated_at"`
			DataPath        string                   `json:"data_path"`
			LoadJSONL       string                   `json:"load_jsonl"`
			Profile         *analysis.StartupProfile `json:"profile"`
			TotalWithLoad   string                   `json:"total_with_load"`
			Recommendations []string                 `json:"recommendations"`
		}{
			GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
			DataPath:        dataPath,
			LoadJSONL:       loadDuration.String(),
			Profile:         profile,
			TotalWithLoad:   totalWithLoad.String(),
			Recommendations: generateProfileRecommendations(profile, loadDuration, totalWithLoad),
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding profile: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Human-readable output
		printProfileReport(profile, loadDuration, totalWithLoad)
	}
}

// printProfileReport outputs a human-readable startup profile
func printProfileReport(profile *analysis.StartupProfile, loadDuration, totalWithLoad time.Duration) {
	fmt.Println("Startup Profile")
	fmt.Println("===============")
	fmt.Printf("Data: %d issues, %d dependencies, density=%.4f\n\n",
		profile.NodeCount, profile.EdgeCount, profile.Density)

	// Phase 1
	fmt.Println("Phase 1 (blocking):")
	fmt.Printf("  Load JSONL:      %v\n", formatDuration(loadDuration))
	fmt.Printf("  Build graph:     %v\n", formatDuration(profile.BuildGraph))
	fmt.Printf("  Degree:          %v\n", formatDuration(profile.Degree))
	fmt.Printf("  TopoSort:        %v\n", formatDuration(profile.TopoSort))
	fmt.Printf("  Total Phase 1:   %v\n\n", formatDuration(loadDuration+profile.BuildGraph+profile.Phase1))

	// Phase 2
	fmt.Println("Phase 2 (async in normal mode, sync for profiling):")
	printMetricLine("PageRank", profile.PageRank, profile.PageRankTO, profile.Config.ComputePageRank)
	printMetricLine("Betweenness", profile.Betweenness, profile.BetweennessTO, profile.Config.ComputeBetweenness)
	printMetricLine("Eigenvector", profile.Eigenvector, false, profile.Config.ComputeEigenvector)
	printMetricLine("HITS", profile.HITS, profile.HITSTO, profile.Config.ComputeHITS)
	printMetricLine("Critical Path", profile.CriticalPath, false, profile.Config.ComputeCriticalPath)
	printCyclesLine(profile)
	fmt.Printf("  Total Phase 2:   %v\n\n", formatDuration(profile.Phase2))

	// Total
	fmt.Printf("Total startup:     %v\n\n", formatDuration(totalWithLoad))

	// Configuration used
	fmt.Println("Configuration:")
	fmt.Printf("  Size tier: %s\n", getSizeTier(profile.NodeCount))
	skipped := profile.Config.SkippedMetrics()
	if len(skipped) > 0 {
		var names []string
		for _, s := range skipped {
			names = append(names, s.Name)
		}
		fmt.Printf("  Skipped metrics: %s\n", strings.Join(names, ", "))
	} else {
		fmt.Println("  All metrics computed")
	}
	fmt.Println()

	// Recommendations
	recommendations := generateProfileRecommendations(profile, loadDuration, totalWithLoad)
	if len(recommendations) > 0 {
		fmt.Println("Recommendations:")
		for _, rec := range recommendations {
			fmt.Printf("  %s\n", rec)
		}
	}
}

// printMetricLine prints a single metric timing line
func printMetricLine(name string, duration time.Duration, timedOut, computed bool) {
	if !computed {
		fmt.Printf("  %-14s [Skipped]\n", name+":")
		return
	}
	suffix := ""
	if timedOut {
		suffix = " (TIMEOUT)"
	}
	fmt.Printf("  %-14s %v%s\n", name+":", formatDuration(duration), suffix)
}

// printCyclesLine prints the cycles metric line with count
func printCyclesLine(profile *analysis.StartupProfile) {
	if !profile.Config.ComputeCycles {
		fmt.Printf("  %-14s [Skipped]\n", "Cycles:")
		return
	}
	suffix := ""
	if profile.CyclesTO {
		suffix = " (TIMEOUT)"
	} else if profile.CycleCount > 0 {
		suffix = fmt.Sprintf(" (found: %d)", profile.CycleCount)
	} else {
		suffix = " (none)"
	}
	fmt.Printf("  %-14s %v%s\n", "Cycles:", formatDuration(profile.Cycles), suffix)
}

// formatDuration formats a duration for display, right-aligned
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%6.2fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%6dms", d.Milliseconds())
}

// getSizeTier returns the size tier name based on node count
func getSizeTier(nodeCount int) string {
	switch {
	case nodeCount < 100:
		return "Small (<100 issues)"
	case nodeCount < 500:
		return "Medium (100-500 issues)"
	case nodeCount < 2000:
		return "Large (500-2000 issues)"
	default:
		return "XL (>2000 issues)"
	}
}

// generateProfileRecommendations generates actionable recommendations based on profile
func generateProfileRecommendations(profile *analysis.StartupProfile, loadDuration, totalWithLoad time.Duration) []string {
	var recs []string

	// Check overall startup time
	if totalWithLoad < 500*time.Millisecond {
		recs = append(recs, "✓ Startup within acceptable range (<500ms)")
	} else if totalWithLoad < 1*time.Second {
		recs = append(recs, "✓ Startup acceptable (<1s)")
	} else if totalWithLoad < 2*time.Second {
		// Check if full analysis is being used (no skipped metrics on a large graph)
		if len(profile.Config.SkippedMetrics()) == 0 && profile.NodeCount >= 500 {
			recs = append(recs, "⚠ Startup is slow (1-2s) - if using --force-full-analysis, consider removing it")
		} else {
			recs = append(recs, "⚠ Startup is slow (1-2s)")
		}
	} else {
		recs = append(recs, "⚠ Startup is very slow (>2s) - optimization recommended")
	}

	// Check for timeouts
	if profile.PageRankTO {
		recs = append(recs, "⚠ PageRank timed out - graph may be too large or dense")
	}
	if profile.BetweennessTO {
		recs = append(recs, "⚠ Betweenness timed out - this is expected for large graphs (>500 nodes)")
	}
	if profile.HITSTO {
		recs = append(recs, "⚠ HITS timed out - graph may have convergence issues")
	}
	if profile.CyclesTO {
		recs = append(recs, "⚠ Cycle detection timed out - graph may have many overlapping cycles")
	}

	// Check which metric is taking longest
	if profile.Config.ComputeBetweenness && profile.Betweenness > 0 {
		phase2NoZero := profile.Phase2
		if phase2NoZero > 0 {
			betweennessPercent := float64(profile.Betweenness) / float64(phase2NoZero) * 100
			if betweennessPercent > 50 {
				recs = append(recs, fmt.Sprintf("⚠ Betweenness taking %.0f%% of Phase 2 time - consider skipping for large graphs", betweennessPercent))
			}
		}
	}

	// Check for cycles
	if profile.CycleCount > 0 {
		recs = append(recs, fmt.Sprintf("⚠ Found %d circular dependencies - resolve to improve graph health", profile.CycleCount))
	}

	return recs
}

// filterByRepo filters issues to only include those from a specific repository.
// The filter matches issue IDs that start with the given prefix.
// If the prefix doesn't end with a separator character, it normalizes by checking
// common patterns (prefix-, prefix:, etc.).
func filterByRepo(issues []model.Issue, repoFilter string) []model.Issue {
	if repoFilter == "" {
		return issues
	}

	// Normalize the filter - ensure it's a proper prefix
	filter := repoFilter
	filterLower := strings.ToLower(filter)
	// If filter doesn't end with common separators, try matching as-is or with separators
	needsFlexibleMatch := !strings.HasSuffix(filter, "-") &&
		!strings.HasSuffix(filter, ":") &&
		!strings.HasSuffix(filter, "_")

	var result []model.Issue
	for _, issue := range issues {
		idLower := strings.ToLower(issue.ID)

		// Check if issue ID starts with the filter (case-insensitive)
		if strings.HasPrefix(idLower, filterLower) {
			result = append(result, issue)
			continue
		}

		// If flexible matching is needed, try with common separators
		if needsFlexibleMatch {
			if strings.HasPrefix(idLower, filterLower+"-") ||
				strings.HasPrefix(idLower, filterLower+":") ||
				strings.HasPrefix(idLower, filterLower+"_") {
				result = append(result, issue)
				continue
			}
		}

		// Also check SourceRepo field if set (case-insensitive)
		if issue.SourceRepo != "" && issue.SourceRepo != "." {
			sourceRepoLower := strings.ToLower(issue.SourceRepo)
			if strings.HasPrefix(sourceRepoLower, filterLower) {
				result = append(result, issue)
			}
		}
	}

	return result
}

// buildMetricItems converts a metrics map to a sorted slice of MetricItems
func buildMetricItems(metrics map[string]float64, limit int) []baseline.MetricItem {
	if len(metrics) == 0 {
		return nil
	}

	// Convert to slice for sorting
	items := make([]baseline.MetricItem, 0, len(metrics))
	for id, value := range metrics {
		items = append(items, baseline.MetricItem{ID: id, Value: value})
	}

	// Sort by value descending
	sort.Slice(items, func(i, j int) bool {
		return items[i].Value > items[j].Value
	})

	// Limit to top N
	if len(items) > limit {
		items = items[:limit]
	}

	return items
}

// buildAttentionReason creates a human-readable reason for attention score
func buildAttentionReason(score analysis.LabelAttentionScore) string {
	var parts []string

	// High PageRank
	if score.PageRankSum > 0.5 {
		parts = append(parts, "High PageRank")
	}

	// Blocked issues
	if score.BlockedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", score.BlockedCount))
	}

	// Stale issues
	if score.StaleCount > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", score.StaleCount))
	}

	// Low velocity (VelocityFactor = ClosedLast30Days + 1, so 1.0 means zero closures)
	if score.VelocityFactor <= 1.0 {
		parts = append(parts, "low velocity")
	}

	// If no specific reasons, note the open count
	if len(parts) == 0 {
		return fmt.Sprintf("%d open issues", score.OpenCount)
	}

	return strings.Join(parts, ", ")
}

// ============================================================================
// Static Pages Export Helpers (bv-73f)
// ============================================================================

// copyViewerAssets copies the viewer HTML/JS/CSS assets to the output directory.
// If title is provided, it replaces the default title in index.html.
func copyViewerAssets(outputDir, title string) error {
	// First try to use embedded assets (production builds)
	if export.HasEmbeddedAssets() {
		return export.CopyEmbeddedAssets(outputDir, title)
	}

	// Fall back to filesystem-based approach (development mode)
	assetsDir := findViewerAssetsDir()
	if assetsDir == "" {
		return fmt.Errorf("viewer assets not found")
	}

	if err := maybeBuildHybridWasmAssets(assetsDir); err != nil {
		return err
	}

	// Files to copy
	files := []string{
		"index.html",
		"viewer.js",
		"styles.css",
		"graph.js",
		"charts.js",
		"hybrid_scorer.js",
		"wasm_loader.js",
		"coi-serviceworker.js",
	}

	for _, file := range files {
		src := filepath.Join(assetsDir, file)
		dst := filepath.Join(outputDir, file)

		// Special handling for index.html to replace title and add cache-busting
		if file == "index.html" {
			if err := copyFileWithTitleAndCacheBusting(src, dst, title); err != nil {
				return fmt.Errorf("copy %s: %w", file, err)
			}
			continue
		}

		if err := copyFile(src, dst); err != nil {
			// Skip missing optional files
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("copy %s: %w", file, err)
		}
	}

	// Copy vendor directory
	vendorSrc := filepath.Join(assetsDir, "vendor")
	vendorDst := filepath.Join(outputDir, "vendor")
	if err := copyDir(vendorSrc, vendorDst); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("copy vendor: %w", err)
		}
	}

	// Copy optional WASM directory
	wasmSrc := filepath.Join(assetsDir, "wasm")
	wasmDst := filepath.Join(outputDir, "wasm")
	if err := copyDir(wasmSrc, wasmDst); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("copy wasm: %w", err)
		}
	}

	// Always add GitHub Actions workflow for reliable Pages deployment
	// This ensures the workflow is in the bundle regardless of deployment target
	if err := export.WriteGitHubActionsWorkflow(outputDir); err != nil {
		// Non-fatal - just log a warning
		fmt.Printf("  Warning: Could not add GitHub Actions workflow: %v\n", err)
	}

	return nil
}

func maybeBuildHybridWasmAssets(assetsDir string) error {
	if os.Getenv("BV_BUILD_HYBRID_WASM") == "" {
		return nil
	}

	wasmPackPath, err := exec.LookPath("wasm-pack")
	if err != nil {
		return fmt.Errorf("BV_BUILD_HYBRID_WASM is set but wasm-pack was not found in PATH")
	}

	wasmSrc := filepath.Join(assetsDir, "..", "wasm_scorer")
	info, err := os.Stat(wasmSrc)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("hybrid wasm source directory not found at %s", wasmSrc)
	}

	outDir := filepath.Join(assetsDir, "wasm")
	cmd := exec.Command(wasmPackPath, "build", "--release", "--target", "web", "--out-dir", outDir)
	cmd.Dir = wasmSrc
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build hybrid wasm: %w", err)
	}
	return nil
}

// findViewerAssetsDir locates the viewer assets directory.
func findViewerAssetsDir() string {
	// Try relative to current working directory (development)
	candidates := []string{
		"pkg/export/viewer_assets",
		"../pkg/export/viewer_assets",
		"../../pkg/export/viewer_assets",
	}

	// Try relative to executable
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "pkg/export/viewer_assets"),
			filepath.Join(exeDir, "../pkg/export/viewer_assets"),
			filepath.Join(exeDir, "../../pkg/export/viewer_assets"),
		)
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}

	return ""
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	_, err = io.Copy(dstFile, srcFile)
	if closeErr := dstFile.Close(); err == nil {
		err = closeErr
	}
	return err
}

// copyFileWithTitleAndCacheBusting copies a file while replacing the default title
// and adding cache-busting query parameters to script tags.
func copyFileWithTitleAndCacheBusting(src, dst, title string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	result := string(content)

	// Replace title in <title> tag and in the h1 header (if title provided)
	if title != "" {
		safeTitle := html.EscapeString(title)
		result = strings.Replace(result, "<title>Beads Viewer</title>", "<title>"+safeTitle+"</title>", 1)
		result = strings.Replace(result, `<h1 class="text-xl font-semibold">Beads Viewer</h1>`, `<h1 class="text-xl font-semibold">`+safeTitle+`</h1>`, 1)
	}

	// Always add cache-busting to script tags to prevent CDN from serving stale JS files
	result = export.AddScriptCacheBusting(result)

	return os.WriteFile(dst, []byte(result), 0644)
}

// copyDir recursively copies a directory.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// generateREADME creates a README.md file for the GitHub Pages repository.
// It includes actionable insights, graph analysis, and a direct link to the live site.
func generateREADME(bundlePath, title, pagesURL string, issues []model.Issue, triage *analysis.TriageResult, stats *analysis.GraphStats) error {
	var b strings.Builder

	// Title
	if title == "" {
		title = "Project Dashboard"
	}
	b.WriteString(fmt.Sprintf("# %s\n\n", title))

	// Prominent live link - THE MOST IMPORTANT THING
	if pagesURL != "" {
		b.WriteString(fmt.Sprintf("## 🔗 [View Live Dashboard](%s)\n\n", pagesURL))
	}

	// Executive summary - not boring counts, but actionable intelligence
	if triage != nil {
		health := triage.ProjectHealth.Counts

		// Quick status line
		completionPct := float64(0)
		if health.Total > 0 {
			completionPct = float64(health.Closed) / float64(health.Total) * 100
		}

		b.WriteString("## 📊 Executive Summary\n\n")
		b.WriteString(fmt.Sprintf("**%d** total issues | **%.0f%%** complete | **%d** ready to work | **%d** blocked\n\n",
			health.Total, completionPct, health.Actionable, health.Blocked))

		// Health assessment
		if health.Blocked > 0 && health.Actionable > 0 {
			blockRatio := float64(health.Blocked) / float64(health.Actionable)
			if blockRatio > 1.0 {
				b.WriteString("⚠️ **Health Warning:** More issues are blocked than actionable. Focus on clearing blockers.\n\n")
			}
		}
	}

	// TOP RECOMMENDATIONS - the actual useful content
	if triage != nil && len(triage.QuickRef.TopPicks) > 0 {
		b.WriteString("## 🎯 Top Priorities\n\n")
		b.WriteString("The graph analysis identified these as the highest-impact items to work on:\n\n")

		for i, pick := range triage.QuickRef.TopPicks {
			b.WriteString(fmt.Sprintf("### %d. %s\n", i+1, pick.Title))
			b.WriteString(fmt.Sprintf("**ID:** `%s` | **Impact Score:** %.2f", pick.ID, pick.Score))
			if pick.Unblocks > 0 {
				b.WriteString(fmt.Sprintf(" | **Unblocks:** %d issues", pick.Unblocks))
			}
			b.WriteString("\n\n")

			if len(pick.Reasons) > 0 {
				b.WriteString("**Why this matters:**\n")
				for _, reason := range pick.Reasons {
					b.WriteString(fmt.Sprintf("- %s\n", reason))
				}
				b.WriteString("\n")
			}
		}
	}

	// CRITICAL BLOCKERS - what's holding everything up
	if triage != nil && len(triage.BlockersToClear) > 0 {
		b.WriteString("## 🚧 Critical Bottlenecks\n\n")
		b.WriteString("These issues are blocking the most downstream work. Clearing them has outsized impact:\n\n")

		maxBlockers := 5
		if len(triage.BlockersToClear) < maxBlockers {
			maxBlockers = len(triage.BlockersToClear)
		}

		b.WriteString("| Issue | Title | Unblocks | Status |\n")
		b.WriteString("|-------|-------|----------|--------|\n")
		for i := 0; i < maxBlockers; i++ {
			blocker := triage.BlockersToClear[i]
			status := "Ready"
			if !blocker.Actionable {
				status = fmt.Sprintf("Blocked by %d", len(blocker.BlockedBy))
			}
			// Escape title to prevent markdown table breakage
			safeTitle := escapeMarkdownTableCell(truncateTitle(blocker.Title, 40))
			b.WriteString(fmt.Sprintf("| `%s` | %s | **%d** issues | %s |\n",
				blocker.ID, safeTitle, blocker.UnblocksCount, status))
		}
		if len(triage.BlockersToClear) > 5 {
			b.WriteString(fmt.Sprintf("\n*+%d more bottlenecks in the dashboard*\n", len(triage.BlockersToClear)-5))
		}
		b.WriteString("\n")
	}

	// CYCLES - these are BUGS in the project structure!
	// Cache cycles since Cycles() does a deep copy each call
	var cycles [][]string
	if stats != nil {
		cycles = stats.Cycles()
	}
	if len(cycles) > 0 {
		b.WriteString("## 🔴 Dependency Cycles Detected!\n\n")
		b.WriteString("**These are structural bugs** that make completion impossible. Fix immediately:\n\n")

		maxCycles := 3
		if len(cycles) < maxCycles {
			maxCycles = len(cycles)
		}
		for i := 0; i < maxCycles; i++ {
			cycle := cycles[i]
			b.WriteString(fmt.Sprintf("- `%s`\n", strings.Join(cycle, "` → `")))
		}
		if len(cycles) > 3 {
			b.WriteString(fmt.Sprintf("\n*+%d more cycles - see dashboard for details*\n", len(cycles)-3))
		}
		b.WriteString("\n")
	}

	// ALERTS - important warnings
	if triage != nil && len(triage.Alerts) > 0 {
		hasCritical := false
		hasWarning := false
		for _, alert := range triage.Alerts {
			if alert.Severity == "critical" {
				hasCritical = true
			} else if alert.Severity == "warning" {
				hasWarning = true
			}
		}

		if hasCritical || hasWarning {
			b.WriteString("## ⚠️ Alerts\n\n")
			for _, alert := range triage.Alerts {
				if alert.Severity == "critical" || alert.Severity == "warning" {
					icon := "🟡"
					if alert.Severity == "critical" {
						icon = "🔴"
					}
					b.WriteString(fmt.Sprintf("- %s **%s**: %s\n", icon, alert.Type, alert.Message))
				}
			}
			b.WriteString("\n")
		}
	}

	// GRAPH ANALYSIS INSIGHTS - what the analysis tells us
	if stats != nil && stats.NodeCount > 0 {
		b.WriteString("## 📈 Graph Analysis\n\n")

		// Density interpretation
		densityHealth := "🟢 Healthy"
		densityDesc := "Issues are well-isolated and can be parallelized"
		if stats.Density > 0.15 {
			densityHealth = "🔴 High Coupling"
			densityDesc = "Many inter-dependencies; changes cascade widely"
		} else if stats.Density > 0.05 {
			densityHealth = "🟡 Moderate"
			densityDesc = "Normal coupling for a complex project"
		}

		b.WriteString(fmt.Sprintf("- **Dependency Density:** %.3f (%s) — %s\n", stats.Density, densityHealth, densityDesc))
		b.WriteString(fmt.Sprintf("- **Graph Size:** %d issues with %d dependencies\n", stats.NodeCount, stats.EdgeCount))

		// Use cached cycles variable (already fetched above)
		if len(cycles) > 0 {
			b.WriteString(fmt.Sprintf("- **Cycles:** %d circular dependencies detected (must fix!)\n", len(cycles)))
		} else if stats.EdgeCount > 0 {
			b.WriteString("- **Cycles:** None detected ✓\n")
		}
		b.WriteString("\n")
	}

	// QUICK WINS - low effort, high impact
	if triage != nil && len(triage.QuickWins) > 0 {
		b.WriteString("## 🏃 Quick Wins\n\n")
		b.WriteString("Low-effort items that clear the path forward:\n\n")

		maxWins := 5
		if len(triage.QuickWins) < maxWins {
			maxWins = len(triage.QuickWins)
		}
		for i := 0; i < maxWins; i++ {
			qw := triage.QuickWins[i]
			unblockText := ""
			if len(qw.UnblocksIDs) > 0 {
				unblockText = fmt.Sprintf(" (unblocks %d)", len(qw.UnblocksIDs))
			}
			b.WriteString(fmt.Sprintf("- **%s**: %s%s\n", qw.ID, qw.Title, unblockText))
			if qw.Reason != "" {
				b.WriteString(fmt.Sprintf("  - *%s*\n", qw.Reason))
			}
		}
		if len(triage.QuickWins) > 5 {
			b.WriteString(fmt.Sprintf("\n*+%d more quick wins in the dashboard*\n", len(triage.QuickWins)-5))
		}
		b.WriteString("\n")
	}

	// SUMMARY STATS - compact reference at the end
	if triage != nil {
		health := triage.ProjectHealth.Counts
		b.WriteString("## 📋 Status Summary\n\n")

		// Priority breakdown inline
		if len(health.ByPriority) > 0 {
			var prioItems []string
			priorities := []int{0, 1, 2, 3, 4}
			for _, p := range priorities {
				if count, ok := health.ByPriority[p]; ok && count > 0 {
					prioItems = append(prioItems, fmt.Sprintf("P%d: %d", p, count))
				}
			}
			if len(prioItems) > 0 {
				b.WriteString(fmt.Sprintf("**By Priority:** %s\n\n", strings.Join(prioItems, " | ")))
			}
		}

		// Type breakdown inline
		if len(health.ByType) > 0 {
			var typeItems []string
			for t, count := range health.ByType {
				if count > 0 {
					typeItems = append(typeItems, fmt.Sprintf("%s: %d", t, count))
				}
			}
			if len(typeItems) > 0 {
				sort.Strings(typeItems)
				b.WriteString(fmt.Sprintf("**By Type:** %s\n\n", strings.Join(typeItems, " | ")))
			}
		}
	}

	// Footer with timestamp and links
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("*Generated %s by [bv](https://github.com/Dicklesworthstone/beads_viewer)*\n\n", time.Now().Format("Jan 2, 2006 at 3:04 PM MST")))

	if pagesURL != "" {
		b.WriteString(fmt.Sprintf("**[Open Interactive Dashboard](%s)** for full details, dependency graph, search, and time-travel.\n", pagesURL))
	}

	// Write to file
	readmePath := filepath.Join(bundlePath, "README.md")
	return os.WriteFile(readmePath, []byte(b.String()), 0644)
}

// truncateTitle truncates a title to maxLen runes, adding ellipsis if needed.
// It safely handles UTF-8 and ensures maxLen is reasonable.
func truncateTitle(title string, maxLen int) string {
	if maxLen < 4 {
		maxLen = 4 // Minimum sensible length: "X..."
	}
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}
	return string(runes[:maxLen-3]) + "..."
}

// escapeMarkdownTableCell escapes characters that would break markdown table formatting
func escapeMarkdownTableCell(s string) string {
	// Replace pipe characters and newlines that break tables
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// runPreviewServer starts a local HTTP server to preview the static site.
func runPreviewServer(dir string, liveReload bool) error {
	cfg := export.DefaultPreviewConfig()
	cfg.BundlePath = dir
	cfg.LiveReload = liveReload
	return export.StartPreviewWithConfig(cfg)
}

// runPagesWizard runs the interactive deployment wizard (bv-10g).
func runPagesWizard(beadsPath string) error {
	wizard := export.NewWizard(beadsPath)

	// Run interactive wizard to collect configuration
	_, err := wizard.Run()
	if err != nil {
		return err
	}

	config := wizard.GetConfig()

	// Resolve the actual source of issues for this deployment.
	// This ensures updates always use the originally-deployed dataset,
	// even if the user runs bv from a different directory.
	source, err := resolvePagesSource(config, beadsPath)
	if err != nil {
		return err
	}
	issues := source.Issues

	// Filter issues based on config
	exportIssues := issues
	if !config.IncludeClosed {
		var openIssues []model.Issue
		for _, issue := range issues {
			if issue.Status != model.StatusClosed {
				openIssues = append(openIssues, issue)
			}
		}
		exportIssues = openIssues
	}

	// Create temp directory for bundle
	bundlePath := config.OutputPath
	if bundlePath == "" {
		tmpDir, err := os.MkdirTemp("", "bv-pages-*")
		if err != nil {
			return fmt.Errorf("failed to create temp directory: %w", err)
		}
		bundlePath = tmpDir
	}

	// Ensure output directory exists
	if err := os.MkdirAll(bundlePath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Perform export
	wizard.PerformExport(bundlePath)

	if source.BeadsDir != "" {
		fmt.Printf("  -> Using beads source: %s (%s)\n", source.BeadsDir, source.Reason)
	}

	fmt.Println("Exporting static site...")
	fmt.Printf("  -> Loading %d issues\n", len(exportIssues))

	// Build graph and compute stats
	fmt.Println("  -> Running graph analysis...")
	analyzer := analysis.NewAnalyzer(exportIssues)
	stats := analyzer.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	// Compute triage
	fmt.Println("  -> Generating triage data...")
	triage := analysis.ComputeTriage(exportIssues)

	// Extract dependencies
	var deps []*model.Dependency
	for i := range exportIssues {
		issue := &exportIssues[i]
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			deps = append(deps, &model.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dep.DependsOnID,
				Type:        dep.Type,
			})
		}
	}

	// Create exporter
	issuePointers := make([]*model.Issue, len(exportIssues))
	for i := range exportIssues {
		issuePointers[i] = &exportIssues[i]
	}
	exporter := export.NewSQLiteExporter(issuePointers, deps, stats, &triage)
	if config.Title != "" {
		exporter.Config.Title = config.Title
	}

	// Export SQLite database
	fmt.Println("  -> Writing database and JSON files...")
	if err := exporter.Export(bundlePath); err != nil {
		return fmt.Errorf("export failed: %w", err)
	}

	// Copy viewer assets
	fmt.Println("  -> Copying viewer assets...")
	if err := copyViewerAssets(bundlePath, config.Title); err != nil {
		return fmt.Errorf("failed to copy assets: %w", err)
	}

	// Generate README.md with project stats (for GitHub Pages)
	if config.DeployTarget == "github" {
		fmt.Println("  -> Generating README.md...")
		// Compute the GitHub Pages URL from username and repo name
		pagesURL := ""
		if ghStatus, err := export.CheckGHStatus(); err == nil && ghStatus.Authenticated && ghStatus.Username != "" {
			repoName := config.RepoName
			// Handle repo names that already include owner (e.g., "owner/repo")
			if strings.Contains(repoName, "/") {
				parts := strings.Split(repoName, "/")
				// Validate we have both owner and repo parts
				if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
					pagesURL = fmt.Sprintf("https://%s.github.io/%s/", parts[0], parts[1])
				}
			}
			// Fallback to username + repo name if no valid owner/repo format
			if pagesURL == "" && repoName != "" {
				// Strip any leading/trailing slashes from repo name
				cleanRepo := strings.Trim(repoName, "/")
				if cleanRepo != "" {
					pagesURL = fmt.Sprintf("https://%s.github.io/%s/", ghStatus.Username, cleanRepo)
				}
			}
		}
		if err := generateREADME(bundlePath, config.Title, pagesURL, exportIssues, &triage, stats); err != nil {
			fmt.Printf("  -> Warning: failed to generate README: %v\n", err)
		}
	}

	// Export history data for time-travel feature if requested
	if config.IncludeHistory {
		fmt.Println("  -> Generating time-travel history data...")
		if historyReport, err := generateHistoryForExport(exportIssues); err == nil && historyReport != nil {
			historyPath := filepath.Join(bundlePath, "data", "history.json")
			if historyJSON, err := json.MarshalIndent(historyReport, "", "  "); err == nil {
				if err := os.WriteFile(historyPath, historyJSON, 0644); err != nil {
					fmt.Printf("  -> Warning: failed to write history.json: %v\n", err)
				} else {
					fmt.Printf("  -> history.json (%d commits)\n", len(historyReport.Commits))
				}
			}
		} else if err != nil {
			fmt.Printf("  -> Warning: failed to generate history: %v\n", err)
		}
	}

	fmt.Printf("  -> Bundle created: %s\n", bundlePath)
	fmt.Println("")

	// Persist source metadata and last-export info for reliable updates.
	if source.BeadsDir != "" {
		config.SourceBeadsDir = source.BeadsDir
	}
	if source.RepoRoot != "" {
		config.SourceRepoRoot = source.RepoRoot
	}
	config.SourcePath = source.SourcePath
	config.LastIssueCount = len(exportIssues)
	config.LastDataHash = analysis.ComputeDataHash(exportIssues)
	saveWizardConfig := func() error {
		if err := export.SaveWizardConfig(config); err != nil {
			return fmt.Errorf("save pages wizard configuration: %w", err)
		}
		return nil
	}

	// Offer preview and deploy (for GitHub and Cloudflare)
	if config.DeployTarget == "github" || config.DeployTarget == "cloudflare" {
		action, err := wizard.OfferPreview()
		if err != nil {
			return err
		}

		if action == "cancel" {
			// User cancelled after preview - show local result instead
			fmt.Println("Deployment cancelled. Bundle available at:", bundlePath)
			result := &export.WizardResult{
				BundlePath:   bundlePath,
				DeployTarget: "local",
			}
			if err := saveWizardConfig(); err != nil {
				return err
			}
			wizard.PrintSuccess(result)
		} else {
			// Perform deployment with issue count for verification
			result, err := wizard.PerformDeployWithIssueCount(len(exportIssues))
			if err != nil {
				return err
			}

			if err := saveWizardConfig(); err != nil {
				return err
			}
			wizard.PrintSuccess(result)
		}
	} else {
		// Local export - just show success
		result := &export.WizardResult{
			BundlePath:   bundlePath,
			DeployTarget: "local",
		}
		if err := saveWizardConfig(); err != nil {
			return err
		}
		wizard.PrintSuccess(result)
	}

	return nil
}

type pagesSource struct {
	Issues     []model.Issue
	BeadsDir   string
	RepoRoot   string
	SourcePath string
	Reason     string
}

type pagesSourceCandidate struct {
	BeadsDir string
	Reason   string
}

func resolvePagesSource(config *export.WizardConfig, beadsPath string) (pagesSource, error) {
	var candidates []pagesSourceCandidate
	seen := map[string]bool{}
	var lastErr error

	if source, ok, err := datasource.ExplicitBeadsDBSource(); err != nil {
		return pagesSource{}, err
	} else if ok {
		src, err := loadPagesSourceFromDataSource(source, "explicit BEADS_DB file")
		if err != nil {
			return pagesSource{}, err
		}
		return src, nil
	}
	if config.SourcePath != "" {
		src, ok, err := loadPagesSourceFromFile(config.SourcePath, "saved source file")
		if err != nil {
			lastErr = err
		} else if ok {
			return src, nil
		}
	}

	addCandidate := func(dir, reason string) {
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if seen[dir] {
			return
		}
		seen[dir] = true
		candidates = append(candidates, pagesSourceCandidate{BeadsDir: dir, Reason: reason})
	}

	if config.SourceBeadsDir != "" {
		addCandidate(config.SourceBeadsDir, "saved source")
	}
	if config.SourceRepoRoot != "" {
		addCandidate(filepath.Join(config.SourceRepoRoot, ".beads"), "saved repo root")
	}
	if beadsPath != "" {
		addCandidate(filepath.Dir(beadsPath), "current beads path")
	}
	if dir, err := loader.GetBeadsDir(""); err == nil {
		addCandidate(dir, "current repo")
	}

	for _, cand := range candidates {
		if info, err := os.Stat(cand.BeadsDir); err != nil || !info.IsDir() {
			continue
		}
		issues, err := loadIssuesFromBeadsDir(cand.BeadsDir)
		if err != nil {
			lastErr = err
			continue
		}
		src := pagesSource{
			Issues:   issues,
			BeadsDir: cand.BeadsDir,
			RepoRoot: filepath.Dir(cand.BeadsDir),
			Reason:   cand.Reason,
		}

		// If the issue count looks wildly off, try to auto-detect a better source.
		if isSuspiciousIssueCount(len(issues), config.LastIssueCount) {
			if improved, ok := findBetterPagesSource(config, src, beadsPath); ok {
				return improved, nil
			}
		}
		return src, nil
	}

	if lastErr != nil {
		return pagesSource{}, lastErr
	}
	return pagesSource{}, fmt.Errorf("no valid beads source found for pages export")
}

func loadPagesSourceFromFile(path, reason string) (pagesSource, bool, error) {
	source, ok, err := datasource.SourceFromFile(path)
	if err != nil || !ok {
		return pagesSource{}, ok, err
	}
	src, err := loadPagesSourceFromDataSource(source, reason)
	return src, true, err
}

func loadPagesSourceFromDataSource(source datasource.DataSource, reason string) (pagesSource, error) {
	issues, err := datasource.LoadFromSource(source)
	if err != nil {
		return pagesSource{}, err
	}
	sourcePath := source.Path
	if abs, err := filepath.Abs(sourcePath); err == nil {
		sourcePath = abs
	}
	beadsDir := filepath.Dir(sourcePath)
	return pagesSource{
		Issues:     issues,
		BeadsDir:   beadsDir,
		RepoRoot:   filepath.Dir(beadsDir),
		SourcePath: sourcePath,
		Reason:     reason,
	}, nil
}

func isSuspiciousIssueCount(current, expected int) bool {
	if current == 0 {
		return true
	}
	if expected <= 0 {
		return false
	}
	threshold := expected / 5
	if threshold < 5 {
		threshold = 5
	}
	return current < threshold
}

func findBetterPagesSource(config *export.WizardConfig, current pagesSource, beadsPath string) (pagesSource, bool) {
	expected := config.LastIssueCount
	currentCount := len(current.Issues)
	currentDiff := absInt(currentCount - expected)

	repoHint := strings.ToLower(strings.TrimSpace(config.RepoName))
	altHint := repoHint
	if strings.HasPrefix(altHint, "beads-for-") {
		altHint = strings.TrimPrefix(altHint, "beads-for-")
	}

	roots := []string{}
	seenRoots := map[string]bool{}
	addRoot := func(root string) {
		if root == "" {
			return
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		if seenRoots[root] {
			return
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return
		}
		seenRoots[root] = true
		roots = append(roots, root)
	}

	if config.SourceRepoRoot != "" {
		addRoot(config.SourceRepoRoot)
	}
	if beadsPath != "" {
		addRoot(filepath.Dir(filepath.Dir(beadsPath)))
	}
	if cwd, err := os.Getwd(); err == nil {
		addRoot(cwd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		addRoot(home)
	}
	if info, err := os.Stat("/dp"); err == nil && info.IsDir() {
		addRoot("/dp")
	}

	bestDir := ""
	bestCount := 0
	bestDiff := 0
	bestHintDir := ""
	bestHintCount := 0
	bestHintDiff := 0

	for _, root := range roots {
		for _, beadsDir := range discoverBeadsDirs(root, 4) {
			if beadsDir == current.BeadsDir {
				continue
			}
			count, err := countIssuesInBeadsDir(beadsDir)
			if err != nil || count == 0 {
				continue
			}

			pathLower := strings.ToLower(beadsDir)
			hintMatch := repoHint != "" && (strings.Contains(pathLower, repoHint) || strings.Contains(pathLower, altHint))

			if expected > 0 {
				diff := absInt(count - expected)
				if bestDir == "" || diff < bestDiff || (diff == bestDiff && count > bestCount) {
					bestDir = beadsDir
					bestCount = count
					bestDiff = diff
				}
				if hintMatch && (bestHintDir == "" || diff < bestHintDiff || (diff == bestHintDiff && count > bestHintCount)) {
					bestHintDir = beadsDir
					bestHintCount = count
					bestHintDiff = diff
				}
			} else if count > bestCount {
				if hintMatch && count > bestHintCount {
					bestHintDir = beadsDir
					bestHintCount = count
				} else if bestHintDir == "" {
					bestDir = beadsDir
					bestCount = count
				}
			}
		}
	}

	if bestHintDir != "" && (bestDir == "" || bestHintDiff <= bestDiff) {
		bestDir = bestHintDir
		bestCount = bestHintCount
		bestDiff = bestHintDiff
	}

	if bestDir == "" {
		return pagesSource{}, false
	}

	if expected > 0 && bestDiff >= currentDiff {
		return pagesSource{}, false
	}
	if expected == 0 && bestCount <= currentCount {
		return pagesSource{}, false
	}

	issues, err := loadIssuesFromBeadsDir(bestDir)
	if err != nil {
		return pagesSource{}, false
	}
	return pagesSource{
		Issues:   issues,
		BeadsDir: bestDir,
		RepoRoot: filepath.Dir(bestDir),
		Reason:   "auto-detected better source",
	}, true
}

func discoverBeadsDirs(root string, maxDepth int) []string {
	var dirs []string
	root = filepath.Clean(root)
	sep := string(os.PathSeparator)

	skip := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		"target":       true,
		".cache":       true,
		".bv":          true,
		".idea":        true,
		".vscode":      true,
	}

	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			if !d.IsDir() {
				return nil
			}

			name := d.Name()
			if skip[name] && path != root {
				return fs.SkipDir
			}

			rel := strings.TrimPrefix(strings.TrimPrefix(path, root), sep)
			if rel != "" {
				if depth := len(strings.Split(rel, sep)); depth > maxDepth {
					return fs.SkipDir
				}
			}

			if name == ".beads" {
				dirs = append(dirs, path)
				return fs.SkipDir
			}
		}
		return nil
	})
	return dirs
}

func countIssuesInBeadsDir(beadsDir string) (int, error) {
	if path, typ := metadataPreferredSource(beadsDir); path != "" {
		info, err := os.Stat(path)
		if err == nil {
			priority := datasource.PriorityJSONLLocal
			if typ == datasource.SourceTypeSQLite {
				priority = datasource.PrioritySQLite
			}
			source := datasource.DataSource{
				Type:     typ,
				Path:     path,
				Priority: priority,
				ModTime:  info.ModTime(),
				Size:     info.Size(),
			}
			if err := datasource.ValidateSource(&source); err == nil {
				return source.IssueCount, nil
			}
		}
	}

	sources, err := datasource.DiscoverSources(datasource.DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: true,
		IncludeInvalid:         false,
	})
	if err != nil {
		return 0, err
	}
	if len(sources) == 0 {
		return 0, fmt.Errorf("no sources in %s", beadsDir)
	}
	result, err := datasource.SelectBestSourceDetailed(sources, datasource.DefaultSelectionOptions())
	if err != nil {
		return 0, err
	}
	return result.Selected.IssueCount, nil
}

type beadsMetadata struct {
	Database    string `json:"database"`
	JSONLExport string `json:"jsonl_export"`
}

func metadataPreferredSource(beadsDir string) (string, datasource.SourceType) {
	metaPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", ""
	}
	var meta beadsMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", ""
	}
	if meta.Database != "" {
		path := meta.Database
		if !filepath.IsAbs(path) {
			path = filepath.Join(beadsDir, path)
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, datasource.SourceTypeSQLite
		}
	}
	if meta.JSONLExport != "" {
		path := meta.JSONLExport
		if !filepath.IsAbs(path) {
			path = filepath.Join(beadsDir, path)
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, datasource.SourceTypeJSONLLocal
		}
	}
	return "", ""
}

func loadIssuesFromBeadsDir(beadsDir string) ([]model.Issue, error) {
	if path, typ := metadataPreferredSource(beadsDir); path != "" {
		switch typ {
		case datasource.SourceTypeSQLite:
			reader, err := datasource.NewSQLiteReader(datasource.DataSource{
				Type: datasource.SourceTypeSQLite,
				Path: path,
			})
			if err != nil {
				break
			}
			defer reader.Close()
			if issues, err := reader.LoadIssues(); err == nil {
				return issues, nil
			}
		case datasource.SourceTypeJSONLLocal:
			if issues, err := loader.LoadIssuesFromFile(path); err == nil {
				return issues, nil
			}
		}
	}
	return datasource.LoadIssuesFromDir(beadsDir)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// BurndownOutput represents the JSON output for --robot-burndown (bv-159)
type BurndownOutput struct {
	RobotEnvelope
	SprintID          string                `json:"sprint_id"`
	SprintName        string                `json:"sprint_name"`
	StartDate         time.Time             `json:"start_date"`
	EndDate           time.Time             `json:"end_date"`
	TotalDays         int                   `json:"total_days"`
	ElapsedDays       int                   `json:"elapsed_days"`
	RemainingDays     int                   `json:"remaining_days"`
	TotalIssues       int                   `json:"total_issues"`
	CompletedIssues   int                   `json:"completed_issues"`
	RemainingIssues   int                   `json:"remaining_issues"`
	IdealBurnRate     float64               `json:"ideal_burn_rate"`
	ActualBurnRate    float64               `json:"actual_burn_rate"`
	ProjectedComplete *time.Time            `json:"projected_complete,omitempty"`
	OnTrack           bool                  `json:"on_track"`
	DailyPoints       []model.BurndownPoint `json:"daily_points"`
	IdealLine         []model.BurndownPoint `json:"ideal_line"`
	ScopeChanges      []ScopeChangeEvent    `json:"scope_changes,omitempty"`
}

// ScopeChangeEvent represents when issues were added/removed from sprint
type ScopeChangeEvent struct {
	Date       time.Time `json:"date"`
	IssueID    string    `json:"issue_id"`
	IssueTitle string    `json:"issue_title"`
	Action     string    `json:"action"` // "added" or "removed"
}

type sprintSnapshot struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BeadIDs []string `json:"bead_ids,omitempty"`
}

type scopeCommit struct {
	sha       string
	timestamp time.Time
	order     int // stable ordering when timestamps are tied (git dates are second-granularity)
	events    []ScopeChangeEvent
}

func computeSprintScopeChanges(repoPath string, sprint *model.Sprint, issueMap map[string]model.Issue, now time.Time) ([]ScopeChangeEvent, error) {
	if sprint == nil || sprint.ID == "" {
		return nil, nil
	}
	if sprint.StartDate.IsZero() || sprint.EndDate.IsZero() {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		// Not a git repository (common for ad-hoc exports/tests); scope changes are optional.
		return nil, nil
	}

	// Bound the history window to the sprint to keep this fast.
	since := sprint.StartDate.AddDate(0, 0, -1)
	until := sprint.EndDate
	if until.After(now) {
		until = now
	}

	args := []string{
		"-c", "color.ui=false",
		"log",
		"-p",
		"-U0",
		"--format=%H%x00%cI",
		fmt.Sprintf("--since=%s", since.Format(time.RFC3339)),
		fmt.Sprintf("--until=%s", until.Format(time.RFC3339)),
		"--",
		filepath.ToSlash(filepath.Join(".beads", loader.SprintsFileName)),
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w: %s", filepath.ToSlash(filepath.Join(".beads", loader.SprintsFileName)), err, bytes.TrimSpace(out))
	}

	var commits []scopeCommit
	var currentTS time.Time
	var currentSHA string
	var haveCommit bool
	var oldSnap, newSnap sprintSnapshot
	var haveOld, haveNew bool

	processCommit := func() {
		if !haveCommit {
			return
		}

		if haveOld && haveNew && oldSnap.ID == sprint.ID && newSnap.ID == sprint.ID {
			added := setDifference(newSnap.BeadIDs, oldSnap.BeadIDs)
			removed := setDifference(oldSnap.BeadIDs, newSnap.BeadIDs)
			if len(added) == 0 && len(removed) == 0 {
				return
			}

			sort.Strings(added)
			sort.Strings(removed)

			events := make([]ScopeChangeEvent, 0, len(added)+len(removed))
			for _, id := range removed {
				title := ""
				if iss, ok := issueMap[id]; ok {
					title = iss.Title
				}
				events = append(events, ScopeChangeEvent{
					Date:       currentTS.UTC(),
					IssueID:    id,
					IssueTitle: title,
					Action:     "removed",
				})
			}
			for _, id := range added {
				title := ""
				if iss, ok := issueMap[id]; ok {
					title = iss.Title
				}
				events = append(events, ScopeChangeEvent{
					Date:       currentTS.UTC(),
					IssueID:    id,
					IssueTitle: title,
					Action:     "added",
				})
			}

			commits = append(commits, scopeCommit{
				sha:       currentSHA,
				timestamp: currentTS.UTC(),
				order:     len(commits),
				events:    events,
			})
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	// Sprints JSONL lines can contain large bead ID lists; allow a generous buffer.
	const maxCapacity = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(make([]byte, 64*1024), maxCapacity)
	for scanner.Scan() {
		line := scanner.Text()

		sha, ts, ok := parseGitHeaderLine(line)
		if ok {
			processCommit()

			currentTS = ts
			currentSHA = sha
			haveCommit = true
			oldSnap, newSnap = sprintSnapshot{}, sprintSnapshot{}
			haveOld, haveNew = false, false
			continue
		}

		if !haveCommit {
			continue
		}

		if strings.HasPrefix(line, "-{") {
			if snap, ok := parseSprintJSONLine(strings.TrimPrefix(line, "-")); ok && snap.ID == sprint.ID {
				oldSnap = snap
				haveOld = true
			}
			continue
		}
		if strings.HasPrefix(line, "+{") {
			if snap, ok := parseSprintJSONLine(strings.TrimPrefix(line, "+")); ok && snap.ID == sprint.ID {
				newSnap = snap
				haveNew = true
			}
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	processCommit()

	if len(commits) == 0 {
		return nil, nil
	}

	// Ensure stable chronological output regardless of git log ordering nuances.
	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].timestamp.Equal(commits[j].timestamp) {
			return commits[i].timestamp.Before(commits[j].timestamp)
		}
		// When commit timestamps are identical (common in tests), preserve the
		// original git log order reversed into chronological order.
		return commits[i].order > commits[j].order
	})

	var scopeChanges []ScopeChangeEvent
	for _, c := range commits {
		scopeChanges = append(scopeChanges, c.events...)
	}

	return scopeChanges, nil
}

func parseGitHeaderLine(line string) (sha string, ts time.Time, ok bool) {
	parts := strings.SplitN(line, "\x00", 2)
	if len(parts) != 2 {
		return "", time.Time{}, false
	}
	if len(parts[0]) != 40 {
		return "", time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
	if err != nil {
		return "", time.Time{}, false
	}
	return parts[0], parsed, true
}

func parseSprintJSONLine(line string) (sprintSnapshot, bool) {
	var snap sprintSnapshot
	if err := json.Unmarshal([]byte(line), &snap); err != nil {
		return sprintSnapshot{}, false
	}
	if snap.ID == "" {
		return sprintSnapshot{}, false
	}
	return snap, true
}

func setDifference(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	mb := make(map[string]bool, len(b))
	for _, v := range b {
		mb[v] = true
	}
	var out []string
	for _, v := range a {
		if v == "" {
			continue
		}
		if !mb[v] {
			out = append(out, v)
		}
	}
	return out
}

// calculateBurndownAt is a deterministic variant of calculateBurndown for testing.
func calculateBurndownAt(sprint *model.Sprint, issues []model.Issue, now time.Time) BurndownOutput {

	// Build issue map for sprint beads
	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	// Count total and completed issues in sprint
	var sprintIssues []model.Issue
	for _, beadID := range sprint.BeadIDs {
		if iss, ok := issueMap[beadID]; ok {
			sprintIssues = append(sprintIssues, iss)
		}
	}

	totalIssues := len(sprintIssues)
	completedIssues := 0
	for _, iss := range sprintIssues {
		if iss.Status == model.StatusClosed {
			completedIssues++
		}
	}
	remainingIssues := totalIssues - completedIssues

	// Calculate days
	totalDays := 0
	elapsedDays := 0
	remainingDays := 0

	if !sprint.StartDate.IsZero() && !sprint.EndDate.IsZero() {
		totalDays = int(sprint.EndDate.Sub(sprint.StartDate).Hours()/24) + 1
		if now.Before(sprint.StartDate) {
			elapsedDays = 0
			remainingDays = totalDays
		} else if now.After(sprint.EndDate) {
			elapsedDays = totalDays
			remainingDays = 0
		} else {
			elapsedDays = int(now.Sub(sprint.StartDate).Hours()/24) + 1
			remainingDays = totalDays - elapsedDays
		}
	}

	// Calculate burn rates
	idealBurnRate := 0.0
	if totalDays > 0 {
		idealBurnRate = float64(totalIssues) / float64(totalDays)
	}

	actualBurnRate := 0.0
	if elapsedDays > 0 {
		actualBurnRate = float64(completedIssues) / float64(elapsedDays)
	}

	// Calculate projected completion
	var projectedComplete *time.Time
	onTrack := true
	if actualBurnRate > 0 && remainingIssues > 0 {
		daysToComplete := float64(remainingIssues) / actualBurnRate
		projected := now.AddDate(0, 0, int(daysToComplete)+1)
		projectedComplete = &projected
		onTrack = !projected.After(sprint.EndDate)
	} else if remainingIssues == 0 {
		// Already complete
		onTrack = true
	} else if elapsedDays > 0 && completedIssues == 0 {
		// No progress made
		onTrack = false
	}

	// Generate daily burndown points
	dailyPoints := generateDailyBurndown(sprint, sprintIssues, now)

	// Generate ideal line
	idealLine := generateIdealLine(sprint, totalIssues)

	return BurndownOutput{
		SprintID:          sprint.ID,
		SprintName:        sprint.Name,
		StartDate:         sprint.StartDate,
		EndDate:           sprint.EndDate,
		TotalDays:         totalDays,
		ElapsedDays:       elapsedDays,
		RemainingDays:     remainingDays,
		TotalIssues:       totalIssues,
		CompletedIssues:   completedIssues,
		RemainingIssues:   remainingIssues,
		IdealBurnRate:     idealBurnRate,
		ActualBurnRate:    actualBurnRate,
		ProjectedComplete: projectedComplete,
		OnTrack:           onTrack,
		DailyPoints:       dailyPoints,
		IdealLine:         idealLine,
		ScopeChanges:      nil,
	}
}

// generateDailyBurndown creates actual burndown points based on issue closure dates
func generateDailyBurndown(sprint *model.Sprint, issues []model.Issue, now time.Time) []model.BurndownPoint {
	if sprint.StartDate.IsZero() || sprint.EndDate.IsZero() {
		return nil
	}

	var points []model.BurndownPoint
	totalIssues := len(issues)

	// Iterate through each day of the sprint
	for d := sprint.StartDate; !d.After(sprint.EndDate) && !d.After(now); d = d.AddDate(0, 0, 1) {
		dayEnd := d.Add(24*time.Hour - time.Second)
		completed := 0

		for _, iss := range issues {
			if iss.Status == model.StatusClosed && iss.ClosedAt != nil && !iss.ClosedAt.After(dayEnd) {
				completed++
			}
		}

		points = append(points, model.BurndownPoint{
			Date:      d,
			Remaining: totalIssues - completed,
			Completed: completed,
		})
	}

	return points
}

// generateIdealLine creates the ideal burndown line
func generateIdealLine(sprint *model.Sprint, totalIssues int) []model.BurndownPoint {
	if sprint.StartDate.IsZero() || sprint.EndDate.IsZero() || totalIssues == 0 {
		return nil
	}

	var points []model.BurndownPoint
	totalDays := int(sprint.EndDate.Sub(sprint.StartDate).Hours()/24) + 1
	burnPerDay := float64(totalIssues) / float64(totalDays)

	for i := 0; i <= totalDays; i++ {
		d := sprint.StartDate.AddDate(0, 0, i)
		remaining := totalIssues - int(float64(i)*burnPerDay)
		if remaining < 0 {
			remaining = 0
		}
		points = append(points, model.BurndownPoint{
			Date:      d,
			Remaining: remaining,
			Completed: totalIssues - remaining,
		})
	}

	return points
}

// generateJQHelpers creates a markdown document with jq snippets for agent brief
func generateJQHelpers() string {
	return `# jq Helper Snippets

Quick reference for extracting data from the agent brief JSON files.

## triage.json

### Top Picks
` + "```bash" + `
# Get top 3 recommendations
jq '.quick_ref.top_picks[:3]' triage.json

# Get IDs of top picks
jq '.quick_ref.top_picks[].id' triage.json

# Get top pick with highest unblocks
jq '.quick_ref.top_picks | max_by(.unblocks)' triage.json
` + "```" + `

### Recommendations
` + "```bash" + `
# List all recommendations with scores
jq '.recommendations[] | {id, score, action}' triage.json

# Filter high-score items (score > 0.15)
jq '.recommendations[] | select(.score > 0.15)' triage.json

# Get breakdown metrics
jq '.recommendations[] | {id, pr: .breakdown.pagerank_norm, bw: .breakdown.betweenness_norm}' triage.json
` + "```" + `

### Quick Wins
` + "```bash" + `
# List quick wins
jq '.quick_wins[] | {id, title, reason}' triage.json

# Count quick wins
jq '.quick_wins | length' triage.json
` + "```" + `

### Blockers
` + "```bash" + `
# Get actionable blockers
jq '.blockers_to_clear[] | select(.actionable)' triage.json

# Sort by unblocks count
jq '.blockers_to_clear | sort_by(-.unblocks_count)' triage.json
` + "```" + `

## insights.json

### Graph Metrics
` + "```bash" + `
# Top PageRank issues
jq '.top_pagerank | to_entries | sort_by(-.value)[:5]' insights.json

# Top betweenness centrality
jq '.top_betweenness | to_entries | sort_by(-.value)[:5]' insights.json

# Find hub issues (high in-degree)
jq '.top_in_degree | to_entries | sort_by(-.value)[:3]' insights.json
` + "```" + `

### Project Health
` + "```bash" + `
# Get velocity metrics
jq '.velocity' insights.json

# List critical issues
jq '.critical_issues' insights.json
` + "```" + `

## Combining Files
` + "```bash" + `
# Cross-reference top picks with insights
jq -s '.[0].quick_ref.top_picks[0].id as $id | .[1].top_pagerank[$id] // 0' triage.json insights.json

# Export summary to CSV
jq -r '.recommendations[] | [.id, .score, .action] | @csv' triage.json
` + "```" + `
`
}

// TimeTravelHistory represents the history data format for time-travel animation (bv-z38b)
type TimeTravelHistory struct {
	GeneratedAt string             `json:"generated_at"`
	Commits     []TimeTravelCommit `json:"commits"`
}

// TimeTravelCommit represents a single commit in the time-travel history
type TimeTravelCommit struct {
	SHA         string   `json:"sha"`
	Date        string   `json:"date"`
	Message     string   `json:"message,omitempty"`
	BeadsAdded  []string `json:"beads_added,omitempty"`
	BeadsClosed []string `json:"beads_closed,omitempty"`
}

// generateHistoryForExport creates time-travel history data from git history
func generateHistoryForExport(issues []model.Issue) (*TimeTravelHistory, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// Check if we're in a git repository
	if err := correlation.ValidateRepository(cwd); err != nil {
		return nil, err
	}

	// Get beads path
	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return nil, err
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return nil, err
	}

	// Build bead info from issues
	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{
			ID:     issue.ID,
			Title:  issue.Title,
			Status: string(issue.Status),
		}
	}

	// Generate correlation report
	correlator := correlation.NewCorrelator(cwd, beadsPath)
	report, err := correlator.GenerateReport(beadInfos, correlation.CorrelatorOptions{
		Limit: 500, // Reasonable limit for time-travel
	})
	if err != nil {
		return nil, err
	}

	// Convert to time-travel format
	// Group by commit date and track bead changes
	commitMap := make(map[string]*TimeTravelCommit)

	for beadID, history := range report.Histories {
		for _, commit := range history.Commits {
			ttCommit, exists := commitMap[commit.SHA]
			if !exists {
				ttCommit = &TimeTravelCommit{
					SHA:     commit.SHA,
					Date:    commit.Timestamp.Format(time.RFC3339),
					Message: commit.Message,
				}
				commitMap[commit.SHA] = ttCommit
			}

			// Determine if this bead was added or modified in this commit
			// For simplicity, we consider any commit touching a bead as "adding" it
			// (the first time it appears in history)
			ttCommit.BeadsAdded = append(ttCommit.BeadsAdded, beadID)
		}
	}

	// Build map of bead ID -> latest commit SHA that touched it before/at ClosedAt.
	// This attributes closure only to the most relevant commit, not every commit.
	closedBeadCommit := make(map[string]string) // beadID -> commitSHA
	for _, issue := range issues {
		if issue.Status != model.StatusClosed || issue.ClosedAt == nil {
			continue
		}
		// Find the commit closest to (but not after) the closure time
		var bestSHA string
		var bestDist time.Duration = -1
		for sha, commit := range commitMap {
			for _, id := range commit.BeadsAdded {
				if id != issue.ID {
					continue
				}
				commitDate, _ := time.Parse(time.RFC3339, commit.Date)
				if commitDate.IsZero() {
					continue
				}
				dist := issue.ClosedAt.Sub(commitDate)
				if dist >= 0 && (bestDist < 0 || dist < bestDist) {
					bestSHA = sha
					bestDist = dist
				}
			}
		}
		if bestSHA != "" {
			closedBeadCommit[issue.ID] = bestSHA
		}
	}

	// Convert map to sorted slice
	var commits []TimeTravelCommit
	for _, commit := range commitMap {
		// Deduplicate beads_added
		seen := make(map[string]bool)
		var dedupedAdded []string
		for _, id := range commit.BeadsAdded {
			if !seen[id] {
				seen[id] = true
				dedupedAdded = append(dedupedAdded, id)
				if closedBeadCommit[id] == commit.SHA {
					commit.BeadsClosed = append(commit.BeadsClosed, id)
				}
			}
		}
		commit.BeadsAdded = dedupedAdded
		commits = append(commits, *commit)
	}

	// Sort commits by date
	sort.Slice(commits, func(i, j int) bool {
		return commits[i].Date < commits[j].Date
	})

	return &TimeTravelHistory{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Commits:     commits,
	}, nil
}

var robotOutputFormat = "json"
var robotToonEncodeOptions = toon.DefaultEncodeOptions()
var robotShowToonStats bool

const robotContractVersion = "1.0.0"

// RobotEnvelope is the standard envelope for all robot command outputs.
// All robot outputs MUST include these fields for consistency.
type RobotEnvelope struct {
	GeneratedAt  string `json:"generated_at"`            // RFC3339 timestamp
	DataHash     string `json:"data_hash"`               // Fingerprint of source data
	OutputFormat string `json:"output_format,omitempty"` // "json" or "toon"
	Version      string `json:"version,omitempty"`       // bv version (e.g., "1.0.0")
}

// RobotMeta contains optional timing and computation metadata.
// Commands that perform async/phased analysis should include this.
type RobotMeta struct {
	Phase2Ready bool              `json:"phase2_ready,omitempty"` // True if all async metrics computed
	Timings     map[string]string `json:"timings,omitempty"`      // Per-metric timing info
	CacheHit    bool              `json:"cache_hit,omitempty"`    // True if results from cache
	IssueCount  int               `json:"issue_count,omitempty"`  // Number of issues analyzed
}

// NewRobotEnvelope creates a standard envelope for robot output.
func NewRobotEnvelope(dataHash string) RobotEnvelope {
	return RobotEnvelope{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		DataHash:     dataHash,
		OutputFormat: robotOutputFormat,
		Version:      version.Version,
	}
}

type robotEncoder interface {
	Encode(v any) error
}

type toonRobotEncoder struct {
	w io.Writer
}

func (e *toonRobotEncoder) Encode(v any) error {
	if !toon.Available() {
		fmt.Fprintln(os.Stderr, "warning: tru not available; falling back to JSON")
		return newJSONRobotEncoder(e.w).Encode(v)
	}

	out, err := toon.EncodeWithOptions(v, robotToonEncodeOptions)
	if err != nil {
		return err
	}

	// json.Encoder.Encode always terminates with a newline; match that behavior for TOON.
	out = strings.TrimRight(out, "\n")

	if robotShowToonStats {
		if jsonBytes, jerr := json.Marshal(v); jerr == nil {
			jsonTokens := estimateTokens(string(jsonBytes))
			toonTokens := estimateTokens(out)
			savings := 0
			if jsonTokens > 0 && toonTokens <= jsonTokens {
				savings = int((1.0 - (float64(toonTokens) / float64(jsonTokens))) * 100.0)
			}
			fmt.Fprintf(os.Stderr, "[stats] JSON≈%d tok, TOON≈%d tok (%d%% savings)\n", jsonTokens, toonTokens, savings)
		}
	}

	_, err = io.WriteString(e.w, out+"\n")
	return err
}

// newJSONRobotEncoder creates a JSON encoder for robot mode output.
// By default, output is compact (no indentation) for performance.
// Set BV_PRETTY_JSON=1 to enable pretty-printing for human readability.
func newJSONRobotEncoder(w io.Writer) *json.Encoder {
	encoder := json.NewEncoder(w)
	if os.Getenv("BV_PRETTY_JSON") == "1" {
		encoder.SetIndent("", "  ")
	}
	return encoder
}

// newRobotEncoder creates an encoder for robot mode output.
//
// Default output is JSON. Use `--format toon` (or BV_OUTPUT_FORMAT/TOON_DEFAULT_FORMAT)
// to emit TOON for agent-friendly token savings.
func newRobotEncoder(w io.Writer) robotEncoder {
	if robotOutputFormat == "toon" {
		return &toonRobotEncoder{w: w}
	}
	return newJSONRobotEncoder(w)
}

func resolveRobotOutputFormat(cli string) string {
	format := strings.TrimSpace(cli)
	if format == "" {
		format = strings.TrimSpace(os.Getenv("BV_OUTPUT_FORMAT"))
	}
	if format == "" {
		format = strings.TrimSpace(os.Getenv("TOON_DEFAULT_FORMAT"))
	}
	if format == "" {
		format = "json"
	}
	return strings.ToLower(format)
}

func resolveToonEncodeOptionsFromEnv() toon.EncodeOptions {
	opts := toon.DefaultEncodeOptions()

	if v := strings.TrimSpace(os.Getenv("TOON_KEY_FOLDING")); v != "" {
		opts.KeyFolding = v
	}
	if v := strings.TrimSpace(os.Getenv("TOON_INDENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			// Be conservative; tru supports 0..=16 but clamp to avoid surprising output.
			if n < 0 {
				n = 0
			}
			if n > 16 {
				n = 16
			}
			opts.Indent = n
		}
	}

	return opts
}

func estimateTokens(s string) int {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	// Coarse heuristic; good enough for comparing JSON vs TOON output size.
	return (len(trimmed) + 3) / 4
}

type robotCommandDoc struct {
	Flag          string   `json:"flag"`
	Description   string   `json:"description"`
	KeyFields     []string `json:"key_fields,omitempty"`
	Params        []string `json:"params,omitempty"`
	NeedsIssues   bool     `json:"needs_issues"`
	NeedsGit      bool     `json:"needs_git"`
	NeedsSprint   bool     `json:"needs_sprint"`
	NeedsBaseline bool     `json:"needs_baseline"`
	MutatesState  bool     `json:"mutates_state"`
}

func robotDocsTopics() []string {
	return []string{"guide", "commands", "examples", "env", "exit-codes", "all"}
}

func robotEnvVars() map[string]string {
	return map[string]string{
		"BEADS_DB":            "Path to beads database file or .beads directory (overrides BEADS_DIR; overridden by --db flag)",
		"BEADS_DIR":           "Path to .beads directory (fallback when BEADS_DB and --db are not set)",
		"BV_OUTPUT_FORMAT":    "Default output format: json or toon (overridden by --format)",
		"TOON_DEFAULT_FORMAT": "Fallback format if BV_OUTPUT_FORMAT not set",
		"TOON_STATS":          "Set to 1 to show JSON vs TOON token estimates on stderr",
		"TOON_KEY_FOLDING":    "TOON key folding mode",
		"TOON_INDENT":         "TOON indentation level (0-16)",
		"BV_PRETTY_JSON":      "Set to 1 for indented JSON output",
		"BV_ROBOT":            "Set to 1 to force robot mode (clean stdout)",
		"BV_SEARCH_MODE":      "Search mode: text or hybrid",
		"BV_SEARCH_PRESET":    "Hybrid search preset name",
	}
}

func robotExitCodes() map[string]string {
	return map[string]string{
		"0": "Success",
		"1": "Error (general failure, drift critical)",
		"2": "Invalid arguments or drift warning",
	}
}

func robotCommandDocs() map[string]robotCommandDoc {
	return map[string]robotCommandDoc{
		"robot-help": {
			Flag:        "--robot-help",
			Description: "Agent-focused command help. Use robot-docs guide for structured JSON documentation.",
			NeedsIssues: false,
		},
		"robot-triage": {
			Flag: "--robot-triage", Description: "Unified triage: top picks, recommendations, quick wins, blockers, project health, velocity.",
			KeyFields:   []string{"triage.quick_ref.top_picks", "triage.recommendations", "triage.quick_wins", "triage.blockers_to_clear", "triage.project_health"},
			Params:      []string{"--graph-root <id>"},
			NeedsIssues: true,
		},
		"robot-next": {
			Flag: "--robot-next", Description: "Single top recommendation with claim/show commands.",
			KeyFields:   []string{"id", "title", "score", "reasons", "unblocks", "claim_command", "show_command"},
			Params:      []string{"--graph-root <id>"},
			NeedsIssues: true,
		},
		"robot-plan": {
			Flag: "--robot-plan", Description: "Dependency-respecting execution plan with parallel tracks.",
			KeyFields:   []string{"tracks", "items", "unblocks", "summary"},
			NeedsIssues: true,
		},
		"robot-insights": {
			Flag: "--robot-insights", Description: "Deep graph analysis: PageRank, betweenness, HITS, eigenvector, k-core, cycle detection.",
			KeyFields:   []string{"pagerank", "betweenness", "hits", "eigenvector", "k_core", "cycles"},
			NeedsIssues: true,
		},
		"robot-priority": {
			Flag: "--robot-priority", Description: "Priority misalignment detection: items whose graph importance differs from assigned priority.",
			KeyFields:   []string{"misalignments", "suggestions"},
			NeedsIssues: true,
		},
		"robot-triage-by-track": {
			Flag: "--robot-triage-by-track", Description: "Triage grouped by independent parallel execution tracks.",
			KeyFields:   []string{"triage.recommendations_by_track[].track_id", "triage.recommendations_by_track[].top_pick", "triage.recommendations_by_track[].claim_command"},
			Params:      []string{"--graph-root <id>"},
			NeedsIssues: true,
		},
		"robot-triage-by-label": {
			Flag: "--robot-triage-by-label", Description: "Triage grouped by label for area-focused agents.",
			KeyFields:   []string{"triage.recommendations_by_label[].label", "triage.recommendations_by_label[].top_pick", "triage.recommendations_by_label[].claim_command"},
			Params:      []string{"--graph-root <id>"},
			NeedsIssues: true,
		},
		"robot-alerts": {
			Flag: "--robot-alerts", Description: "Stale issues, blocking cascades, priority mismatches.",
			KeyFields:   []string{"alerts", "severity", "affected_issues"},
			Params:      []string{"--severity info|warning|critical", "--alert-type <type>", "--alert-label <label>"},
			NeedsIssues: true,
		},
		"robot-suggest": {
			Flag: "--robot-suggest", Description: "Smart suggestions: potential duplicates, missing dependencies, label assignments, cycle warnings.",
			KeyFields:   []string{"suggestions", "type", "confidence"},
			Params:      []string{"--suggest-type duplicate|dependency|label|cycle", "--suggest-confidence 0.0-1.0", "--suggest-bead <id>"},
			NeedsIssues: true,
		},
		"robot-capabilities": {
			Flag:        "--robot-capabilities",
			Description: "Machine-readable capability manifest: version, contract, commands, env vars, exit codes, and output formats.",
			KeyFields:   []string{"tool", "version", "contract_version", "commands", "environment_variables", "exit_codes"},
			NeedsIssues: false,
		},
		"robot-recipes": {
			Flag:        "--robot-recipes",
			Description: "Recipe names, descriptions, and usage hints for pre-filtering work.",
			KeyFields:   []string{"recipes"},
			NeedsIssues: false,
		},
		"robot-schema": {
			Flag: "--robot-schema", Description: "JSON Schema definitions for all robot command outputs.",
			KeyFields:   []string{"schema_version", "envelope", "commands"},
			Params:      []string{"--schema-command <cmd>"},
			NeedsIssues: false,
		},
		"robot-docs": {
			Flag: "--robot-docs <topic>", Description: "Machine-readable JSON documentation. Topics: guide, commands, examples, env, exit-codes, all.",
			NeedsIssues: false,
		},
		"robot-history": {
			Flag: "--robot-history", Description: "Bead-to-commit correlations from git history.",
			KeyFields:   []string{"correlations", "confidence", "commit_sha", "bead_id"},
			Params:      []string{"--bead-history <id>", "--history-since <date>", "--history-limit <n>", "--min-confidence 0.0-1.0"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-diff": {
			Flag: "--robot-diff", Description: "Changes since a historical point (commit, branch, tag, or date).",
			Params:      []string{"--diff-since <ref>"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-correlation-stats": {
			Flag:        "--robot-correlation-stats",
			Description: "Summary counts for saved correlation feedback.",
			KeyFields:   []string{"total_feedback", "confirmed", "rejected", "ignored", "accuracy_rate"},
			NeedsIssues: false,
		},
		"robot-explain-correlation": {
			Flag:        "--robot-explain-correlation <sha:bead>",
			Description: "Explain why a commit is linked to a bead.",
			KeyFields:   []string{"commit", "bead", "score", "reasons"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-confirm-correlation": {
			Flag:         "--robot-confirm-correlation <sha:bead>",
			Description:  "Record positive feedback for a commit-to-bead correlation.",
			Params:       []string{"--correlation-by agent", "--correlation-reason verified"},
			NeedsIssues:  true,
			NeedsGit:     true,
			MutatesState: true,
		},
		"robot-reject-correlation": {
			Flag:         "--robot-reject-correlation <sha:bead>",
			Description:  "Record negative feedback for a commit-to-bead correlation.",
			Params:       []string{"--correlation-by agent", "--correlation-reason unrelated"},
			NeedsIssues:  true,
			NeedsGit:     true,
			MutatesState: true,
		},
		"robot-search": {
			Flag: "--robot-search", Description: "Semantic vector search over issue titles and descriptions.",
			Params:      []string{"--search <query>", "--search-limit <n>", "--search-mode text|hybrid"},
			NeedsIssues: true,
		},
		"robot-label-health": {
			Flag:        "--robot-label-health",
			Description: "Per-label health metrics: open/closed counts, velocity, staleness.",
			NeedsIssues: true,
		},
		"robot-label-flow": {
			Flag:        "--robot-label-flow",
			Description: "Cross-label dependency flow analysis.",
			NeedsIssues: true,
		},
		"robot-label-attention": {
			Flag:        "--robot-label-attention",
			Description: "Attention-ranked labels requiring focus.",
			Params:      []string{"--attention-limit <n>"},
			NeedsIssues: true,
		},
		"robot-graph": {
			Flag: "--robot-graph", Description: "Dependency graph export in JSON, DOT, or Mermaid format.",
			Params:      []string{"--graph-format json|dot|mermaid", "--graph-root <id>", "--graph-depth <n>"},
			NeedsIssues: true,
		},
		"robot-metrics": {
			Flag:        "--robot-metrics",
			Description: "Performance metrics: timing, cache hit rates, memory usage.",
			NeedsIssues: true,
		},
		"robot-orphans": {
			Flag: "--robot-orphans", Description: "Orphan commit candidates that should be linked to beads.",
			KeyFields:   []string{"git_range", "stats.candidate_count", "candidates", "candidates[].probable_beads", "by_bead"},
			Params:      []string{"--orphans-min-score 0-100"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-file-beads": {
			Flag: "--robot-file-beads <path>", Description: "Beads that touched a specific file path.",
			KeyFields:   []string{"file_path", "total_beads", "open_beads", "closed_beads"},
			Params:      []string{"--file-beads-limit <n>"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-file-hotspots": {
			Flag: "--robot-file-hotspots", Description: "Files touched by the most beads.",
			KeyFields:   []string{"hotspots", "stats.total_files", "stats.total_bead_links", "stats.files_with_multiple_beads"},
			Params:      []string{"--hotspots-limit <n>"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-file-relations": {
			Flag: "--robot-file-relations <path>", Description: "Files that frequently co-change with a given file.",
			KeyFields:   []string{"file_path", "total_commits", "threshold", "related_files"},
			Params:      []string{"--relations-threshold 0.0-1.0", "--relations-limit <n>"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-related": {
			Flag: "--robot-related <id>", Description: "Beads related to a specific bead ID.",
			KeyFields:   []string{"target_bead_id", "total_related", "file_overlap", "commit_overlap", "dependency_cluster", "concurrent"},
			Params:      []string{"--related-min-relevance 0-100 or 0.0-1.0", "--related-max-results <n>", "--related-include-closed"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-blocker-chain": {
			Flag:        "--robot-blocker-chain <id>",
			Description: "Full blocker chain analysis for an issue.",
			KeyFields:   []string{"result.target_id", "result.is_blocked", "result.root_blockers", "result.chain", "result.has_cycle"},
			NeedsIssues: true,
		},
		"robot-impact-network": {
			Flag: "--robot-impact-network [<id>|all]", Description: "Impact network graph (full or subnetwork for a bead).",
			KeyFields:   []string{"network.nodes", "network.edges", "stats.total_nodes", "top_clusters", "top_connected"},
			Params:      []string{"--network-depth 1-3"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-causality": {
			Flag:        "--robot-causality <id>",
			Description: "Causal chain analysis for a bead.",
			KeyFields:   []string{"chain.bead_id", "chain.events", "insights.summary", "insights.critical_path", "insights.recommendations"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
		"robot-sprint-list": {
			Flag:        "--robot-sprint-list",
			Description: "List all sprints as JSON.",
			NeedsIssues: true,
		},
		"robot-sprint-show": {
			Flag:        "--robot-sprint-show <id>",
			Description: "Show details for a specific sprint.",
			NeedsIssues: true,
			NeedsSprint: true,
		},
		"robot-forecast": {
			Flag: "--robot-forecast <id|all>", Description: "ETA predictions for bead completion.",
			Params:      []string{"--forecast-label <label>", "--forecast-sprint <id>", "--forecast-agents <n>"},
			NeedsIssues: true,
		},
		"robot-capacity": {
			Flag: "--robot-capacity", Description: "Capacity simulation and completion projections.",
			Params:      []string{"--agents <n>", "--capacity-label <label>"},
			NeedsIssues: true,
		},
		"robot-burndown": {
			Flag:        "--robot-burndown <sprint|current>",
			Description: "Sprint burndown data.",
			NeedsIssues: true,
			NeedsSprint: true,
		},
		"robot-drift": {
			Flag:          "--robot-drift",
			Description:   "Drift detection from saved baseline.",
			NeedsIssues:   true,
			NeedsBaseline: true,
		},
		"robot-impact": {
			Flag:        "--robot-impact <path[,path...]>",
			Description: "Analyze bead impact for files that may be modified.",
			KeyFields:   []string{"files", "risk_level", "risk_score", "warnings", "affected_beads"},
			NeedsIssues: true,
			NeedsGit:    true,
		},
	}
}

func generateRobotCapabilities() map[string]interface{} {
	docs := robotCommandDocs()
	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}
	sort.Strings(names)

	commands := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		doc := docs[name]
		entry := map[string]interface{}{
			"name":                 name,
			"flag":                 robotFlagExampleFormForCommand(name, doc.Flag),
			"description":          doc.Description,
			"preferred_invocation": preferredRobotInvocation(name, doc),
			"accepted_invocations": acceptedRobotInvocations(name, doc),
			"needs_issues":         doc.NeedsIssues,
			"needs_git":            doc.NeedsGit,
			"needs_sprint":         doc.NeedsSprint,
			"needs_baseline":       doc.NeedsBaseline,
			"mutates_state":        doc.MutatesState,
		}
		if len(doc.KeyFields) > 0 {
			entry["key_fields"] = doc.KeyFields
		}
		if len(doc.Params) > 0 {
			entry["params"] = robotExampleFormsForCommand(name, doc.Params)
		}
		commands = append(commands, entry)
	}

	return map[string]interface{}{
		"generated_at":          time.Now().UTC().Format(time.RFC3339),
		"tool":                  "bv",
		"version":               version.Version,
		"contract_version":      robotContractVersion,
		"default_robot_command": "bv --robot-triage",
		"output_formats":        []string{"json", "toon"},
		"commands":              commands,
		"docs_topics":           robotDocsTopics(),
		"schema_command":        "bv --robot-schema",
		"agent_intent_aliases":  agentIntentAliasDocs(),
		"environment_variables": robotEnvVars(),
		"exit_codes":            robotExitCodes(),
		"stream_contract": map[string]string{
			"stdout": "Structured robot data only for robot commands.",
			"stderr": "Diagnostics, warnings, and actionable errors.",
		},
	}
}

func preferredRobotInvocation(commandName string, doc robotCommandDoc) string {
	if invocation := preferredRobotInvocationOverride(commandName); invocation != "" {
		return invocation
	}
	return "bv " + commandName + robotCommandArgumentSuffix(commandName, doc) + " --json"
}

func acceptedRobotInvocations(commandName string, doc robotCommandDoc) []string {
	if invocations := acceptedRobotInvocationOverrides(commandName); len(invocations) > 0 {
		return invocations
	}
	return []string{
		"bv " + robotFlagExampleFormForCommand(commandName, doc.Flag) + " --format json",
		preferredRobotInvocation(commandName, doc),
	}
}

func preferredRobotInvocationOverride(commandName string) string {
	switch commandName {
	case "robot-help":
		return "bv robot-help --json"
	case "robot-search":
		return `bv robot-search "login oauth" --json`
	case "robot-diff":
		return "bv robot-diff HEAD~1 --json"
	case "robot-history":
		return "bv robot-history --history-limit 20 --json"
	case "robot-alerts":
		return "bv robot-alerts --severity critical --json"
	case "robot-suggest":
		return "bv robot-suggest --suggest-type duplicate --json"
	case "robot-label-attention":
		return "bv robot-label-attention --attention-limit 5 --json"
	case "robot-graph":
		return "bv robot-graph mermaid --json"
	case "robot-orphans":
		return "bv robot-orphans --orphans-min-score 30 --json"
	case "robot-explain-correlation":
		return "bv robot-explain-correlation deadbeef:ISSUE_ID --json"
	case "robot-confirm-correlation":
		return "bv robot-confirm-correlation deadbeef:ISSUE_ID --correlation-by agent --json"
	case "robot-reject-correlation":
		return "bv robot-reject-correlation deadbeef:ISSUE_ID --correlation-by agent --json"
	case "robot-file-beads":
		return "bv robot-file-beads README.md --json"
	case "robot-file-relations":
		return "bv robot-file-relations README.md --json"
	case "robot-related":
		return "bv robot-related ISSUE_ID --json"
	case "robot-blocker-chain":
		return "bv robot-blocker-chain ISSUE_ID --json"
	case "robot-causality":
		return "bv robot-causality ISSUE_ID --json"
	case "robot-forecast":
		return "bv robot-forecast all --json"
	case "robot-burndown":
		return "bv robot-burndown current --json"
	case "robot-drift":
		return "bv --check-drift --robot-drift --format json"
	case "robot-impact":
		return "bv robot-impact README.md --json"
	default:
		return ""
	}
}

func acceptedRobotInvocationOverrides(commandName string) []string {
	switch commandName {
	case "robot-help":
		return []string{
			"bv robot-help --json",
			"bv robot-docs guide --json",
		}
	case "robot-search":
		return []string{
			`bv --search "login oauth" --robot-search --format json`,
			`bv robot-search "login oauth" --json`,
			`bv search "login oauth" --json`,
		}
	case "robot-diff":
		return []string{
			"bv --robot-diff --diff-since HEAD~1 --format json",
			"bv robot-diff HEAD~1 --json",
		}
	case "robot-drift":
		return []string{
			"bv --check-drift --robot-drift --format json",
			"bv robot-drift --json",
		}
	default:
		return nil
	}
}

func robotCommandArgumentSuffix(commandName string, doc robotCommandDoc) string {
	parts := strings.Fields(robotFlagExampleFormForCommand(commandName, doc.Flag))
	if len(parts) <= 1 {
		return ""
	}
	return " " + strings.Join(parts[1:], " ")
}

func robotExampleFormsForCommand(commandName string, values []string) []string {
	examples := make([]string, 0, len(values))
	for _, value := range values {
		examples = append(examples, robotFlagExampleFormForCommand(commandName, value))
	}
	return examples
}

func robotCommandDocsForAgentOutput() map[string]robotCommandDoc {
	raw := robotCommandDocs()
	docs := make(map[string]robotCommandDoc, len(raw))
	for name, doc := range raw {
		doc.Flag = robotFlagExampleFormForCommand(name, doc.Flag)
		if len(doc.Params) > 0 {
			doc.Params = robotExampleFormsForCommand(name, doc.Params)
		}
		docs[name] = doc
	}
	return docs
}

func robotFlagExampleFormForCommand(commandName, flag string) string {
	switch commandName {
	case "robot-sprint-show":
		flag = strings.ReplaceAll(flag, "<id>", "SPRINT_ID")
	case "robot-burndown":
		flag = strings.ReplaceAll(flag, "<sprint|current>", "current")
	case "robot-forecast":
		flag = strings.ReplaceAll(flag, "--forecast-sprint <id>", "--forecast-sprint SPRINT_ID")
	}
	return robotFlagExampleForm(flag)
}

func robotFlagExampleForm(flag string) string {
	replacements := []struct {
		old string
		new string
	}{
		{"[<id>|all]", "all"},
		{"<path[,path...]>", "README.md"},
		{"<sprint|current>", "current"},
		{"<sha:bead>", "deadbeef:ISSUE_ID"},
		{"<id|all>", "all"},
		{"<query>", `"login oauth"`},
		{"<topic>", "guide"},
		{"<cmd>", "robot-triage"},
		{"<date>", `"30 days ago"`},
		{"<label>", "backend"},
		{"<path>", "README.md"},
		{"<type>", "critical"},
		{"<ref>", "HEAD~1"},
		{"<id>", "ISSUE_ID"},
		{"<n>", "10"},
	}
	for _, replacement := range replacements {
		flag = strings.ReplaceAll(flag, replacement.old, replacement.new)
	}
	return flag
}

func resolveSingleRepoWatchFile(projectDir string) (string, error) {
	if source, ok, err := datasource.ExplicitBeadsDBSource(); err != nil {
		return "", err
	} else if ok {
		return source.Path, nil
	}

	beadsDir, err := loader.GetBeadsDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("getting beads directory: %w", err)
	}

	// Watch whatever source the smart loader actually selected so file events
	// match the source bv reads from. For br repos this is typically the
	// SQLite beads.db; without this, the watcher fires only on JSONL writes
	// even though br updates land in SQLite first.
	//
	// We only need the selected source's PATH here, not a content validation:
	// DiscoverSources already returns sources sorted freshest-first (ties broken
	// by priority), which is exactly what SelectBestSource picks among valid
	// candidates. Skipping ValidateAfterDiscovery avoids a redundant full parse
	// of the 1.9MB issues.jsonl on the robot path (it is parsed once by the
	// loader for the actual data load).
	sources, discoverErr := datasource.DiscoverSources(datasource.DiscoveryOptions{
		BeadsDir:               beadsDir,
		RepoPath:               projectDir,
		ValidateAfterDiscovery: false,
	})
	if discoverErr == nil && len(sources) > 0 && sources[0].Path != "" {
		return sources[0].Path, nil
	}

	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return "", fmt.Errorf("finding Beads JSONL file: %w", err)
	}
	return beadsPath, nil
}

func agentIntentAliasDocs() []map[string]string {
	return []map[string]string{
		{"agent_instinct": "bv --json", "canonical": "bv --robot-triage --format json"},
		{"agent_instinct": "bv robot-triage --json", "canonical": "bv --robot-triage --format json"},
		{"agent_instinct": "bv triage --json", "canonical": "bv --robot-triage --format json"},
		{"agent_instinct": "bv next --json", "canonical": "bv --robot-next --format json"},
		{"agent_instinct": "bv plan --json", "canonical": "bv --robot-plan --format json"},
		{"agent_instinct": "bv insights --json", "canonical": "bv --robot-insights --format json"},
		{"agent_instinct": "bv robot-capabilities --json", "canonical": "bv --robot-capabilities --format json"},
		{"agent_instinct": "bv capabilities --json", "canonical": "bv --robot-capabilities --format json"},
		{"agent_instinct": "bv robot-docs guide --json", "canonical": "bv --robot-docs guide --format json"},
		{"agent_instinct": "bv docs guide --json", "canonical": "bv --robot-docs guide --format json"},
		{"agent_instinct": "bv robot-schema triage --json", "canonical": "bv --robot-schema --schema-command robot-triage --format json"},
		{"agent_instinct": "bv schema triage --json", "canonical": "bv --robot-schema --schema-command robot-triage --format json"},
		{"agent_instinct": "bv robot-search login oauth --json --limit 5", "canonical": "bv --search 'login oauth' --robot-search --format json --search-limit 5"},
		{"agent_instinct": "bv search login oauth --json --limit 5", "canonical": "bv --search 'login oauth' --robot-search --format json --search-limit 5"},
		{"agent_instinct": "bv robot-graph mermaid --json", "canonical": "bv --robot-graph --graph-format mermaid --format json"},
		{"agent_instinct": "bv graph mermaid --json", "canonical": "bv --robot-graph --graph-format mermaid --format json"},
		{"agent_instinct": "bv robot-related bv-123 --json", "canonical": "bv --robot-related bv-123 --format json"},
		{"agent_instinct": "bv --name backend --json", "canonical": "bv --label backend --robot-triage --format json"},
	}
}

// generateRobotDocs returns machine-readable documentation for AI agents (bd-2v50).
// Topics: guide, commands, examples, env, exit-codes, all.
func generateRobotDocs(topic string) map[string]interface{} {
	now := time.Now().UTC().Format(time.RFC3339)
	result := map[string]interface{}{
		"generated_at":  now,
		"output_format": robotOutputFormat,
		"version":       version.Version,
		"topic":         topic,
	}

	guide := map[string]interface{}{
		"description": "bv (Beads Viewer) provides structural analysis of the beads issue tracker DAG. It is the primary interface for AI agents to understand project state, plan work, and discover high-impact tasks.",
		"quickstart": []string{
			"bv robot-triage --json           # Full triage with recommendations",
			"bv robot-next --json             # Single top pick plus claim/show commands",
			"bv robot-plan --json             # Dependency-respecting execution plan",
			"bv robot-insights --json         # Deep graph analysis (PageRank, betweenness, etc.)",
			"bv robot-triage-by-track --json  # Parallel work streams for multi-agent coordination",
			"bv robot-capabilities --json     # Machine-readable command manifest",
			"bv robot-schema --json           # JSON Schema definitions for all commands",
			"bv triage --json                 # Short alias for robot-triage",
			"bv capabilities --json           # Short alias for robot-capabilities",
		},
		"data_source": ".beads/beads.jsonl, .beads/issues.jsonl, or BEADS_DB plus git history (correlations)",
		"output_modes": map[string]string{
			"json": "Default structured output",
			"toon": "Token-optimized notation (saves ~30-50% tokens)",
		},
		"agent_intent_aliases": agentIntentAliasDocs(),
	}

	commands := robotCommandDocsForAgentOutput()

	examples := []map[string]string{
		{"description": "Get top 3 picks for immediate work", "command": "bv robot-triage --json | jq '.triage.quick_ref.top_picks[:3]'"},
		{"description": "Inspect the claim command for the top recommendation", "command": "bv robot-next --json | jq -r '.claim_command'"},
		{"description": "Find high-impact blockers to clear", "command": "bv robot-triage --json | jq '.triage.blockers_to_clear | map(.id)'"},
		{"description": "Get bug-only recommendations", "command": "bv robot-triage --json | jq '.triage.recommendations[] | select(.type == \"bug\")'"},
		{"description": "Multi-agent: top pick per parallel track", "command": "bv robot-triage-by-track --json | jq '.triage.recommendations_by_track[].top_pick'"},
		{"description": "Find beads related to a specific file", "command": "bv robot-file-beads README.md --json"},
		{"description": "Search for issues by keyword", "command": `bv robot-search "authentication" --json`},
		{"description": "Get TOON output (saves tokens)", "command": "bv robot-triage --toon"},
		{"description": "Use env for default format", "command": "BV_OUTPUT_FORMAT=toon bv robot-triage"},
		{"description": "Show token savings estimate", "command": "TOON_STATS=1 bv robot-triage --toon"},
	}

	envVars := robotEnvVars()
	exitCodes := robotExitCodes()

	switch topic {
	case "guide":
		result["guide"] = guide
	case "commands":
		result["commands"] = commands
	case "examples":
		result["examples"] = examples
	case "env":
		result["environment_variables"] = envVars
	case "exit-codes":
		result["exit_codes"] = exitCodes
	case "all":
		result["guide"] = guide
		result["commands"] = commands
		result["examples"] = examples
		result["environment_variables"] = envVars
		result["exit_codes"] = exitCodes
	default:
		result["error"] = "Unknown topic: " + topic
		topics := robotDocsTopics()
		result["available_topics"] = topics
		if suggestion := suggestClosest(topic, topics); suggestion != "" {
			result["did_you_mean"] = suggestion
			result["suggested_action"] = "Run `bv --robot-docs " + suggestion + "`"
		} else {
			result["suggested_action"] = "Run `bv --robot-docs guide` or `bv --robot-docs all`"
		}
	}

	return result
}

// RobotSchemas holds JSON Schema definitions for all robot commands
type RobotSchemas struct {
	SchemaVersion string                            `json:"schema_version"`
	GeneratedAt   string                            `json:"generated_at"`
	Envelope      map[string]interface{}            `json:"envelope"`
	Commands      map[string]map[string]interface{} `json:"commands"`
}

// generateRobotSchemas creates JSON Schema definitions for robot command outputs
func generateRobotSchemas() RobotSchemas {
	now := time.Now().UTC().Format(time.RFC3339)

	// Common envelope schema (present in all robot outputs)
	envelope := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"generated_at": map[string]interface{}{
				"type":        "string",
				"format":      "date-time",
				"description": "ISO 8601 timestamp when output was generated",
			},
			"data_hash": map[string]interface{}{
				"type":        "string",
				"description": "Fingerprint of source beads.jsonl for cache validation",
			},
			"output_format": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"json", "toon"},
				"description": "Output format used (json or toon)",
			},
			"version": map[string]interface{}{
				"type":        "string",
				"description": "bv version that generated this output",
			},
		},
		"required": []string{"generated_at", "data_hash"},
	}

	commands := map[string]map[string]interface{}{
		"robot-capabilities": robotCapabilitiesSchema(),
		"robot-docs":         robotDocsOutputSchema(),
		"robot-help":         robotHelpOutputSchema(),
		"robot-history":      robotHistoryOutputSchema(),
		"robot-schema":       robotSchemaOutputSchema(),
		"robot-search":       robotSearchOutputSchema(),
		"robot-triage": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Triage Output",
			"description": "Unified triage recommendations with quick picks, blockers, and project health",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"triage": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"meta": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"version":      map[string]interface{}{"type": "string"},
								"generated_at": map[string]interface{}{"type": "string"},
								"phase2_ready": map[string]interface{}{"type": "boolean"},
								"issue_count":  map[string]interface{}{"type": "integer"},
								"history_status": map[string]interface{}{
									"type":        "string",
									"enum":        []string{"ok", "error", "timeout"},
									"description": "Outcome of the git-history correlation prologue; omitted when history was not attempted (#166)",
								},
							},
						},
						"quick_ref": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"open_count":           map[string]interface{}{"type": "integer", "description": "Strict count of issues with status == open (equals project_health.counts.by_status.open)"},
								"actionable_count":     map[string]interface{}{"type": "integer", "description": "Non-closed issues ready to work on (no open blocking dependencies)"},
								"blocked_count":        map[string]interface{}{"type": "integer", "description": "Strict count of issues with status == blocked (equals project_health.counts.by_status.blocked)"},
								"in_progress_count":    map[string]interface{}{"type": "integer", "description": "Strict count of issues with status == in_progress"},
								"not_closed_count":     map[string]interface{}{"type": "integer", "description": "All non-closed issues (open+in_progress+blocked+deferred); equals actionable_count + not_actionable_count"},
								"not_actionable_count": map[string]interface{}{"type": "integer", "description": "Non-closed issues blocked by open dependencies, regardless of status"},
								"top_picks": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"$ref": "#/$defs/recommendation"},
								},
							},
						},
						"recommendations": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"$ref": "#/$defs/recommendation"},
						},
						"quick_wins":        map[string]interface{}{"type": "array"},
						"blockers_to_clear": map[string]interface{}{"type": "array"},
						"project_health":    map[string]interface{}{"type": "object"},
						"commands":          map[string]interface{}{"type": "object"},
					},
				},
				"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"$defs": map[string]interface{}{
				"recommendation": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":       map[string]interface{}{"type": "string"},
						"title":    map[string]interface{}{"type": "string"},
						"type":     map[string]interface{}{"type": "string"},
						"status":   map[string]interface{}{"type": "string"},
						"priority": map[string]interface{}{"type": "integer"},
						"labels":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
						"score":    map[string]interface{}{"type": "number"},
						"reasons":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
						"unblocks": map[string]interface{}{"type": "integer"},
					},
					"required": []string{"id", "title", "score"},
				},
			},
		},
		"robot-next": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Next Output",
			"description": "Single top pick recommendation with claim command",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"id":            map[string]interface{}{"type": "string"},
				"title":         map[string]interface{}{"type": "string"},
				"score":         map[string]interface{}{"type": "number"},
				"reasons":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"unblocks":      map[string]interface{}{"type": "integer"},
				"claim_command": map[string]interface{}{"type": "string"},
				"show_command":  map[string]interface{}{"type": "string"},
			},
			"required": []string{"generated_at", "data_hash", "id", "title", "score"},
		},
		"robot-triage-by-track": robotGroupedTriageOutputSchema(
			"Robot Triage By Track Output",
			"Triage recommendations grouped by independent parallel execution tracks",
			"recommendations_by_track",
			robotTrackRecommendationGroupSchema(),
		),
		"robot-triage-by-label": robotGroupedTriageOutputSchema(
			"Robot Triage By Label Output",
			"Triage recommendations grouped by label for area-focused agents",
			"recommendations_by_label",
			robotLabelRecommendationGroupSchema(),
		),
		"robot-plan": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Plan Output",
			"description": "Dependency-respecting execution plan with parallel tracks",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"plan": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"phases": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"phase":  map[string]interface{}{"type": "integer"},
									"issues": map[string]interface{}{"type": "array"},
								},
							},
						},
						"summary": map[string]interface{}{"type": "object"},
					},
				},
				"status":      map[string]interface{}{"type": "object"},
				"usage_hints": map[string]interface{}{"type": "array"},
			},
		},
		"robot-insights": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Insights Output",
			"description": "Full graph analysis metrics including PageRank, betweenness, HITS, cycles",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":      map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":         map[string]interface{}{"type": "string"},
				"Stats":             map[string]interface{}{"type": "object"},
				"Cycles":            map[string]interface{}{"type": "array"},
				"Keystones":         map[string]interface{}{"type": "array"},
				"Bottlenecks":       map[string]interface{}{"type": "array"},
				"Influencers":       map[string]interface{}{"type": "array"},
				"Hubs":              map[string]interface{}{"type": "array"},
				"Authorities":       map[string]interface{}{"type": "array"},
				"Orphans":           map[string]interface{}{"type": "array"},
				"Cores":             map[string]interface{}{"type": "object"},
				"Articulation":      map[string]interface{}{"type": "array"},
				"Slack":             map[string]interface{}{"type": "object"},
				"Velocity":          map[string]interface{}{"type": "object"},
				"status":            map[string]interface{}{"type": "object"},
				"advanced_insights": map[string]interface{}{"type": "object"},
				"usage_hints":       map[string]interface{}{"type": "array"},
			},
		},
		"robot-priority": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Priority Output",
			"description": "Priority misalignment detection with recommendations",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":    map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":       map[string]interface{}{"type": "string"},
				"as_of":           map[string]interface{}{"type": "string"},
				"as_of_commit":    map[string]interface{}{"type": "string"},
				"analysis_config": map[string]interface{}{"type": "object"},
				"status":          map[string]interface{}{"type": "object"},
				"label_scope":     map[string]interface{}{"type": "string"},
				"label_context":   map[string]interface{}{"type": "object"},
				"recommendations": map[string]interface{}{"type": "array"},
				"field_descriptions": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": map[string]interface{}{"type": "string"},
				},
				"filters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"min_confidence": map[string]interface{}{"type": "number"},
						"max_results":    map[string]interface{}{"type": "integer"},
						"by_label":       map[string]interface{}{"type": "string"},
						"by_assignee":    map[string]interface{}{"type": "string"},
					},
				},
				"summary": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"total_issues":    map[string]interface{}{"type": "integer"},
						"recommendations": map[string]interface{}{"type": "integer"},
						"high_confidence": map[string]interface{}{"type": "integer"},
					},
				},
				"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "analysis_config", "status", "recommendations", "field_descriptions", "filters", "summary", "usage_hints"},
		},
		"robot-graph": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Graph Output",
			"description": "Dependency graph in JSON/DOT/Mermaid format",
			"type":        "object",
			"properties": map[string]interface{}{
				"format": map[string]interface{}{"type": "string", "enum": []string{"json", "dot", "mermaid"}},
				"graph":  map[string]interface{}{"type": "string"},
				"nodes":  map[string]interface{}{"type": "integer"},
				"edges":  map[string]interface{}{"type": "integer"},
				"filters_applied": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": map[string]interface{}{"type": "string"},
				},
				"explanation": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"what":          map[string]interface{}{"type": "string"},
						"how_to_render": map[string]interface{}{"type": "string"},
						"when_to_use":   map[string]interface{}{"type": "string"},
					},
					"required": []string{"what", "when_to_use"},
				},
				"data_hash": map[string]interface{}{"type": "string"},
				"adjacency": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"nodes": map[string]interface{}{"type": "array"},
						"edges": map[string]interface{}{"type": "array"},
					},
				},
			},
			"required": []string{"format", "nodes", "edges", "explanation"},
		},
		"robot-diff": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Diff Output",
			"description": "Changes since a historical point (commit, branch, date)",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":      map[string]interface{}{"type": "string", "format": "date-time"},
				"resolved_revision": map[string]interface{}{"type": "string"},
				"as_of":             map[string]interface{}{"type": "string"},
				"as_of_commit":      map[string]interface{}{"type": "string"},
				"from_data_hash":    map[string]interface{}{"type": "string"},
				"to_data_hash":      map[string]interface{}{"type": "string"},
				"diff": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"from_timestamp":  map[string]interface{}{"type": "string", "format": "date-time"},
						"to_timestamp":    map[string]interface{}{"type": "string", "format": "date-time"},
						"from_revision":   map[string]interface{}{"type": "string"},
						"to_revision":     map[string]interface{}{"type": "string"},
						"new_issues":      map[string]interface{}{"type": "array"},
						"closed_issues":   map[string]interface{}{"type": "array"},
						"removed_issues":  map[string]interface{}{"type": "array"},
						"reopened_issues": map[string]interface{}{"type": "array"},
						"modified_issues": map[string]interface{}{"type": "array"},
						"new_cycles":      map[string]interface{}{"type": "array"},
						"resolved_cycles": map[string]interface{}{"type": "array"},
						"metric_deltas":   map[string]interface{}{"type": "object"},
						"summary":         map[string]interface{}{"type": "object"},
					},
				},
			},
			"required": []string{"generated_at", "resolved_revision", "from_data_hash", "to_data_hash", "diff"},
		},
		"robot-alerts": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Alerts Output",
			"description": "Stale issues, blocking cascades, priority mismatches",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"alerts": map[string]interface{}{
					"type":  "array",
					"items": alertSchema(),
				},
				"summary":     alertSummarySchema(),
				"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "output_format", "version", "alerts", "summary", "usage_hints"},
		},
		"robot-recipes": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Recipes Output",
			"description": "Recipe names, descriptions, and sources for pre-filtering work",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"recipes": map[string]interface{}{
					"type":  "array",
					"items": recipeSummarySchema(),
				},
			},
			"required": []string{"generated_at", "output_format", "version", "recipes"},
		},
		"robot-correlation-stats": robotCorrelationStatsOutputSchema(),
		"robot-file-beads":        robotFileBeadsOutputSchema(),
		"robot-file-hotspots":     robotFileHotspotsOutputSchema(),
		"robot-file-relations":    robotFileRelationsOutputSchema(),
		"robot-impact":            robotImpactOutputSchema(),
		"robot-related":           robotRelatedOutputSchema(),
		"robot-blocker-chain":     robotBlockerChainOutputSchema(),
		"robot-impact-network":    robotImpactNetworkOutputSchema(),
		"robot-causality":         robotCausalityOutputSchema(),
		"robot-orphans":           robotOrphansOutputSchema(),
		"robot-metrics": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Metrics Output",
			"description": "Performance metrics: timing, cache hit rates, and memory usage",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"timing": map[string]interface{}{
					"type":  "array",
					"items": timingMetricSchema(),
				},
				"cache": map[string]interface{}{
					"type":  "array",
					"items": cacheMetricSchema(),
				},
				"memory": memoryMetricSchema(),
			},
			"required": []string{"generated_at", "data_hash", "output_format", "version", "memory"},
		},
		"robot-suggest": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Suggest Output",
			"description": "Smart suggestions for duplicates, dependencies, labels, cycle breaks",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"filters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"type":           map[string]interface{}{"type": "string"},
						"min_confidence": map[string]interface{}{"type": "number"},
						"bead_id":        map[string]interface{}{"type": "string"},
					},
				},
				"suggestions": suggestionSetSchema(),
				"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "filters", "suggestions", "usage_hints"},
		},
		"robot-label-health": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Label Health Output",
			"description": "Per-label health metrics with analysis config and usage hints",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":    map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":       map[string]interface{}{"type": "string"},
				"analysis_config": labelHealthConfigSchema(),
				"results":         labelAnalysisResultSchema(),
				"usage_hints":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "analysis_config", "results", "usage_hints"},
		},
		"robot-label-flow": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Label Flow Output",
			"description": "Cross-label dependency flow analysis with analysis config and usage hints",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":    map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":       map[string]interface{}{"type": "string"},
				"flow":            crossLabelFlowSchema(),
				"analysis_config": labelHealthConfigSchema(),
				"usage_hints":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "flow", "analysis_config", "usage_hints"},
		},
		"robot-label-attention": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Label Attention Output",
			"description": "Attention-ranked labels requiring focus",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"limit":        map[string]interface{}{"type": "integer"},
				"total_labels": map[string]interface{}{"type": "integer"},
				"labels": map[string]interface{}{
					"type":  "array",
					"items": labelAttentionItemSchema(),
				},
				"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"generated_at", "data_hash", "limit", "total_labels", "labels", "usage_hints"},
		},
		"robot-sprint-list": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Sprint List Output",
			"description": "All configured sprints with the standard robot envelope",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"sprint_count":  map[string]interface{}{"type": "integer"},
				"sprints": map[string]interface{}{
					"type":  "array",
					"items": sprintSchema(),
				},
			},
			"required": []string{"generated_at", "data_hash", "output_format", "version", "sprint_count", "sprints"},
		},
		"robot-sprint-show": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Sprint Show Output",
			"description": "A single configured sprint with the standard robot envelope",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"sprint":        sprintSchema(),
			},
			"required": []string{"generated_at", "data_hash", "output_format", "version", "sprint"},
		},
		"robot-capacity": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Capacity Output",
			"description": "Capacity simulation and dependency-aware completion projection",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":         map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":            map[string]interface{}{"type": "string"},
				"output_format":        map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":              map[string]interface{}{"type": "string"},
				"agents":               map[string]interface{}{"type": "integer"},
				"label":                map[string]interface{}{"type": "string"},
				"open_issue_count":     map[string]interface{}{"type": "integer"},
				"total_minutes":        map[string]interface{}{"type": "integer"},
				"total_days":           map[string]interface{}{"type": "number"},
				"serial_minutes":       map[string]interface{}{"type": "integer"},
				"parallel_minutes":     map[string]interface{}{"type": "integer"},
				"parallelizable_pct":   map[string]interface{}{"type": "number"},
				"estimated_days":       map[string]interface{}{"type": "number"},
				"critical_path_length": map[string]interface{}{"type": "integer"},
				"critical_path":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"actionable_count":     map[string]interface{}{"type": "integer"},
				"actionable":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"bottlenecks":          map[string]interface{}{"type": "array", "items": capacityBottleneckSchema()},
			},
			"required": []string{
				"generated_at", "data_hash", "output_format", "version", "agents",
				"open_issue_count", "total_minutes", "total_days", "serial_minutes",
				"parallel_minutes", "parallelizable_pct", "estimated_days",
				"critical_path_length", "critical_path", "actionable_count", "actionable",
			},
		},
		"robot-burndown": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Burndown Output",
			"description": "Sprint burndown data with issue counts, burn rates, daily points, and optional scope changes",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":       map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":          map[string]interface{}{"type": "string"},
				"output_format":      map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":            map[string]interface{}{"type": "string"},
				"sprint_id":          map[string]interface{}{"type": "string"},
				"sprint_name":        map[string]interface{}{"type": "string"},
				"start_date":         map[string]interface{}{"type": "string", "format": "date-time"},
				"end_date":           map[string]interface{}{"type": "string", "format": "date-time"},
				"total_days":         map[string]interface{}{"type": "integer"},
				"elapsed_days":       map[string]interface{}{"type": "integer"},
				"remaining_days":     map[string]interface{}{"type": "integer"},
				"total_issues":       map[string]interface{}{"type": "integer"},
				"completed_issues":   map[string]interface{}{"type": "integer"},
				"remaining_issues":   map[string]interface{}{"type": "integer"},
				"ideal_burn_rate":    map[string]interface{}{"type": "number"},
				"actual_burn_rate":   map[string]interface{}{"type": "number"},
				"projected_complete": map[string]interface{}{"type": "string", "format": "date-time"},
				"on_track":           map[string]interface{}{"type": "boolean"},
				"daily_points": map[string]interface{}{
					"type":  []string{"array", "null"},
					"items": burndownPointSchema(),
				},
				"ideal_line": map[string]interface{}{
					"type":  []string{"array", "null"},
					"items": burndownPointSchema(),
				},
				"scope_changes": map[string]interface{}{
					"type": []string{"array", "null"},
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"date":        map[string]interface{}{"type": "string", "format": "date-time"},
							"issue_id":    map[string]interface{}{"type": "string"},
							"issue_title": map[string]interface{}{"type": "string"},
							"action":      map[string]interface{}{"type": "string", "enum": []string{"added", "removed"}},
						},
					},
				},
			},
			"required": []string{
				"generated_at", "data_hash", "output_format", "version",
				"sprint_id", "sprint_name", "start_date", "end_date",
				"total_days", "elapsed_days", "remaining_days",
				"total_issues", "completed_issues", "remaining_issues",
				"ideal_burn_rate", "actual_burn_rate", "on_track",
				"daily_points", "ideal_line",
			},
		},
		"robot-forecast": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Forecast Output",
			"description": "ETA predictions with dependency-aware scheduling",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
				"version":       map[string]interface{}{"type": "string"},
				"agents":        map[string]interface{}{"type": "integer"},
				"filters": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": map[string]interface{}{"type": "string"},
				},
				"forecast_count": map[string]interface{}{"type": "integer"},
				"forecasts":      map[string]interface{}{"type": "array"},
				"summary": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"total_minutes":  map[string]interface{}{"type": "integer"},
						"total_days":     map[string]interface{}{"type": "number"},
						"avg_confidence": map[string]interface{}{"type": "number"},
						"earliest_eta":   map[string]interface{}{"type": "string", "format": "date-time"},
						"latest_eta":     map[string]interface{}{"type": "string", "format": "date-time"},
					},
				},
			},
			"required": []string{"generated_at", "data_hash", "output_format", "version", "agents", "forecast_count", "forecasts"},
		},
	}
	for name, doc := range robotCommandDocs() {
		if _, ok := commands[name]; !ok {
			commands[name] = genericRobotCommandSchema(name, doc)
		}
	}

	return RobotSchemas{
		SchemaVersion: robotContractVersion,
		GeneratedAt:   now,
		Envelope:      envelope,
		Commands:      commands,
	}
}

func burndownPointSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"date":      map[string]interface{}{"type": "string", "format": "date-time"},
			"remaining": map[string]interface{}{"type": "integer"},
			"completed": map[string]interface{}{"type": "integer"},
		},
		"required": []string{"date", "remaining", "completed"},
	}
}

func suggestionSetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"suggestions": map[string]interface{}{
				"type":  "array",
				"items": suggestionSchema(),
			},
			"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":    map[string]interface{}{"type": "string"},
			"stats":        suggestionStatsSchema(),
		},
		"required": []string{"suggestions", "generated_at", "stats"},
	}
}

func suggestionSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":           map[string]interface{}{"type": "string"},
			"target_bead":    map[string]interface{}{"type": "string"},
			"related_bead":   map[string]interface{}{"type": "string"},
			"summary":        map[string]interface{}{"type": "string"},
			"reason":         map[string]interface{}{"type": "string"},
			"confidence":     map[string]interface{}{"type": "number"},
			"action_command": map[string]interface{}{"type": "string"},
			"generated_at":   map[string]interface{}{"type": "string", "format": "date-time"},
			"metadata":       map[string]interface{}{"type": "object", "additionalProperties": true},
		},
		"required": []string{"type", "target_bead", "summary", "reason", "confidence", "generated_at"},
	}
}

func suggestionStatsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total":                 map[string]interface{}{"type": "integer"},
			"by_type":               map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "integer"}},
			"by_confidence":         map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "integer"}},
			"high_confidence_count": map[string]interface{}{"type": "integer"},
			"actionable_count":      map[string]interface{}{"type": "integer"},
		},
		"required": []string{"total", "by_type", "by_confidence", "high_confidence_count", "actionable_count"},
	}
}

func labelHealthConfigSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"stale_threshold_days":  map[string]interface{}{"type": "integer"},
			"velocity_weight":       map[string]interface{}{"type": "number"},
			"freshness_weight":      map[string]interface{}{"type": "number"},
			"flow_weight":           map[string]interface{}{"type": "number"},
			"criticality_weight":    map[string]interface{}{"type": "number"},
			"min_issues_for_health": map[string]interface{}{"type": "integer"},
			"include_closed_in_flow": map[string]interface{}{
				"type": "boolean",
			},
		},
	}
}

func labelAnalysisResultSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"generated_at":     map[string]interface{}{"type": "string", "format": "date-time"},
			"total_labels":     map[string]interface{}{"type": "integer"},
			"healthy_count":    map[string]interface{}{"type": "integer"},
			"warning_count":    map[string]interface{}{"type": "integer"},
			"critical_count":   map[string]interface{}{"type": "integer"},
			"labels":           map[string]interface{}{"type": "array"},
			"summaries":        map[string]interface{}{"type": "array"},
			"cross_label_flow": crossLabelFlowSchema(),
			"attention_needed": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"generated_at", "total_labels", "healthy_count", "warning_count", "critical_count", "labels", "summaries", "attention_needed"},
	}
}

func crossLabelFlowSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"labels":                 map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"flow_matrix":            map[string]interface{}{"type": "array"},
			"dependencies":           map[string]interface{}{"type": "array"},
			"critical_paths":         map[string]interface{}{"type": "array"},
			"bottleneck_labels":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"total_cross_label_deps": map[string]interface{}{"type": "integer"},
		},
		"required": []string{"labels", "flow_matrix", "dependencies", "critical_paths", "bottleneck_labels", "total_cross_label_deps"},
	}
}

func labelAttentionItemSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"rank":             map[string]interface{}{"type": "integer"},
			"label":            map[string]interface{}{"type": "string"},
			"attention_score":  map[string]interface{}{"type": "number"},
			"normalized_score": map[string]interface{}{"type": "number"},
			"reason":           map[string]interface{}{"type": "string"},
			"open_count":       map[string]interface{}{"type": "integer"},
			"blocked_count":    map[string]interface{}{"type": "integer"},
			"stale_count":      map[string]interface{}{"type": "integer"},
			"pagerank_sum":     map[string]interface{}{"type": "number"},
			"velocity_factor":  map[string]interface{}{"type": "number"},
		},
		"required": []string{"rank", "label", "attention_score", "normalized_score", "reason", "open_count", "blocked_count", "stale_count", "pagerank_sum", "velocity_factor"},
	}
}

func alertSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":                    map[string]interface{}{"type": "string"},
			"severity":                map[string]interface{}{"type": "string", "enum": []string{"critical", "warning", "info"}},
			"message":                 map[string]interface{}{"type": "string"},
			"baseline_value":          map[string]interface{}{"type": "number"},
			"current_value":           map[string]interface{}{"type": "number"},
			"delta":                   map[string]interface{}{"type": "number"},
			"details":                 map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"issue_id":                map[string]interface{}{"type": "string"},
			"label":                   map[string]interface{}{"type": "string"},
			"detected_at":             map[string]interface{}{"type": "string", "format": "date-time"},
			"unblocks_count":          map[string]interface{}{"type": "integer"},
			"downstream_priority_sum": map[string]interface{}{"type": "integer"},
		},
		"required": []string{"type", "severity", "message"},
	}
}

func alertSummarySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total":    map[string]interface{}{"type": "integer"},
			"critical": map[string]interface{}{"type": "integer"},
			"warning":  map[string]interface{}{"type": "integer"},
			"info":     map[string]interface{}{"type": "integer"},
		},
		"required": []string{"total", "critical", "warning", "info"},
	}
}

func recipeSummarySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":        map[string]interface{}{"type": "string"},
			"description": map[string]interface{}{"type": "string"},
			"source":      map[string]interface{}{"type": "string", "enum": []string{"builtin", "user", "project"}},
		},
		"required": []string{"name", "description", "source"},
	}
}

func timingMetricSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":     map[string]interface{}{"type": "string"},
			"count":    map[string]interface{}{"type": "integer"},
			"total_ms": map[string]interface{}{"type": "number"},
			"avg_ms":   map[string]interface{}{"type": "number"},
			"max_ms":   map[string]interface{}{"type": "number"},
			"min_ms":   map[string]interface{}{"type": "number"},
		},
		"required": []string{"name", "count", "total_ms", "avg_ms", "max_ms"},
	}
}

func cacheMetricSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":     map[string]interface{}{"type": "string"},
			"hits":     map[string]interface{}{"type": "integer"},
			"misses":   map[string]interface{}{"type": "integer"},
			"total":    map[string]interface{}{"type": "integer"},
			"hit_rate": map[string]interface{}{"type": "number"},
		},
		"required": []string{"name", "hits", "misses", "total", "hit_rate"},
	}
}

func memoryMetricSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"heap_alloc_mb":   map[string]interface{}{"type": "number"},
			"heap_sys_mb":     map[string]interface{}{"type": "number"},
			"heap_objects_k":  map[string]interface{}{"type": "number"},
			"gc_cycles":       map[string]interface{}{"type": "integer"},
			"gc_pause_ms":     map[string]interface{}{"type": "number"},
			"goroutine_count": map[string]interface{}{"type": "integer"},
		},
		"required": []string{"heap_alloc_mb", "heap_sys_mb", "heap_objects_k", "gc_cycles", "gc_pause_ms", "goroutine_count"},
	}
}

func sprintSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":              map[string]interface{}{"type": "string"},
			"name":            map[string]interface{}{"type": "string"},
			"start_date":      map[string]interface{}{"type": "string", "format": "date-time"},
			"end_date":        map[string]interface{}{"type": "string", "format": "date-time"},
			"bead_ids":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"velocity_target": map[string]interface{}{"type": "number"},
			"created_at":      map[string]interface{}{"type": "string", "format": "date-time"},
			"updated_at":      map[string]interface{}{"type": "string", "format": "date-time"},
		},
		"required": []string{"id", "name"},
	}
}

func capacityBottleneckSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":           map[string]interface{}{"type": "string"},
			"title":        map[string]interface{}{"type": "string"},
			"blocks_count": map[string]interface{}{"type": "integer"},
			"blocks":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"id", "title", "blocks_count"},
	}
}

func robotCapabilitiesSchema() map[string]interface{} {
	commandProperties := map[string]interface{}{
		"name":                 map[string]interface{}{"type": "string"},
		"flag":                 map[string]interface{}{"type": "string"},
		"description":          map[string]interface{}{"type": "string"},
		"preferred_invocation": map[string]interface{}{"type": "string"},
		"accepted_invocations": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"needs_issues":         map[string]interface{}{"type": "boolean"},
		"needs_git":            map[string]interface{}{"type": "boolean"},
		"needs_sprint":         map[string]interface{}{"type": "boolean"},
		"needs_baseline":       map[string]interface{}{"type": "boolean"},
		"mutates_state":        map[string]interface{}{"type": "boolean"},
		"key_fields":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"params":               map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
	}

	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Capabilities Output",
		"description": "Machine-readable command manifest for agent command discovery.",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":          map[string]interface{}{"type": "string", "format": "date-time"},
			"tool":                  map[string]interface{}{"type": "string"},
			"version":               map[string]interface{}{"type": "string"},
			"contract_version":      map[string]interface{}{"type": "string"},
			"default_robot_command": map[string]interface{}{"type": "string"},
			"output_formats":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"commands": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type":                 "object",
					"properties":           commandProperties,
					"required":             []string{"name", "flag", "description", "preferred_invocation", "accepted_invocations", "needs_issues", "needs_git", "needs_sprint", "needs_baseline", "mutates_state"},
					"additionalProperties": true,
				},
			},
			"docs_topics":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"schema_command":        map[string]interface{}{"type": "string"},
			"agent_intent_aliases":  map[string]interface{}{"type": "array"},
			"environment_variables": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
			"exit_codes":            map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
			"stream_contract":       map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"generated_at", "tool", "version", "contract_version", "commands", "environment_variables", "exit_codes"},
	}
}

func robotSchemaOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Schema Output",
		"description": "JSON Schema definitions for all robot commands, or one command when --schema-command is set",
		"type":        "object",
		"properties": map[string]interface{}{
			"schema_version": map[string]interface{}{"type": "string"},
			"generated_at":   map[string]interface{}{"type": "string", "format": "date-time"},
			"envelope":       map[string]interface{}{"type": "object"},
			"commands": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": map[string]interface{}{"type": "object"},
			},
			"command": map[string]interface{}{"type": "string"},
			"schema":  map[string]interface{}{"type": "object"},
		},
		"required": []string{"schema_version", "generated_at"},
		"oneOf": []map[string]interface{}{
			{"required": []string{"schema_version", "generated_at", "envelope", "commands"}},
			{"required": []string{"schema_version", "generated_at", "command", "schema"}},
		},
		"additionalProperties": false,
	}
}

func robotDocsOutputSchema() map[string]interface{} {
	stringMapSchema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": map[string]interface{}{"type": "string"},
	}

	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Docs Output",
		"description": "Machine-readable documentation for one robot docs topic, or an unknown-topic diagnostic",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
			"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":       map[string]interface{}{"type": "string"},
			"topic":         map[string]interface{}{"type": "string"},
			"guide":         map[string]interface{}{"type": "object"},
			"commands": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": map[string]interface{}{"type": "object"},
			},
			"examples": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "object"},
			},
			"environment_variables": stringMapSchema,
			"exit_codes":            stringMapSchema,
			"error":                 map[string]interface{}{"type": "string"},
			"available_topics": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
			"did_you_mean":     map[string]interface{}{"type": "string"},
			"suggested_action": map[string]interface{}{"type": "string"},
		},
		"required":             []string{"generated_at", "output_format", "version", "topic"},
		"additionalProperties": false,
	}
}

func robotHelpOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Help Output",
		"description": "Machine-readable guide output emitted by the agent-friendly `bv robot-help --json` invocation",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
			"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":       map[string]interface{}{"type": "string"},
			"topic":         map[string]interface{}{"type": "string", "const": "guide"},
			"guide":         map[string]interface{}{"type": "object"},
		},
		"required":             []string{"generated_at", "output_format", "version", "topic", "guide"},
		"additionalProperties": false,
	}
}

func robotSearchOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Search Output",
		"description": "Semantic or hybrid issue search results with index metadata and usage hints",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":     map[string]interface{}{"type": "string"},
			"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":       map[string]interface{}{"type": "string"},
			"query":         map[string]interface{}{"type": "string"},
			"provider":      map[string]interface{}{"type": "string"},
			"model":         map[string]interface{}{"type": "string"},
			"dim":           map[string]interface{}{"type": "integer"},
			"index_path":    map[string]interface{}{"type": "string"},
			"index":         map[string]interface{}{"type": "object"},
			"loaded":        map[string]interface{}{"type": "boolean"},
			"limit":         map[string]interface{}{"type": "integer"},
			"mode":          map[string]interface{}{"type": "string", "enum": []string{"text", "hybrid"}},
			"preset":        map[string]interface{}{"type": "string"},
			"weights":       map[string]interface{}{"type": "object"},
			"results": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"issue_id":         map[string]interface{}{"type": "string"},
						"score":            map[string]interface{}{"type": "number"},
						"text_score":       map[string]interface{}{"type": "number"},
						"title":            map[string]interface{}{"type": "string"},
						"component_scores": map[string]interface{}{"type": "object"},
					},
				},
			},
			"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"generated_at", "data_hash", "output_format", "version", "query", "provider", "dim", "index_path", "index", "loaded", "limit", "mode", "results"},
	}
}

func robotCorrelationStatsOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Correlation Stats Output",
		"description": "Summary counts and confidence aggregates for saved correlation feedback",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":     map[string]interface{}{"type": "string", "format": "date-time"},
			"output_format":    map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":          map[string]interface{}{"type": "string"},
			"total_feedback":   map[string]interface{}{"type": "integer"},
			"confirmed":        map[string]interface{}{"type": "integer"},
			"rejected":         map[string]interface{}{"type": "integer"},
			"ignored":          map[string]interface{}{"type": "integer"},
			"accuracy_rate":    map[string]interface{}{"type": "number"},
			"avg_confirm_conf": map[string]interface{}{"type": "number"},
			"avg_reject_conf":  map[string]interface{}{"type": "number"},
		},
		"required": []string{
			"generated_at", "output_format", "version", "total_feedback", "confirmed",
			"rejected", "ignored", "accuracy_rate", "avg_confirm_conf", "avg_reject_conf",
		},
	}
}

func robotOrphansOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot Orphans Output",
		"description": "Orphan commit candidates that should be linked to beads",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":     map[string]interface{}{"type": "string"},
			"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":       map[string]interface{}{"type": "string"},
			"git_range":     map[string]interface{}{"type": "string"},
			"stats":         robotOrphanStatsSchema(),
			"candidates":    map[string]interface{}{"type": "array", "items": robotOrphanCandidateSchema()},
			"by_bead": map[string]interface{}{
				"type": "object",
				"additionalProperties": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
			},
		},
		"required": []string{"generated_at", "data_hash", "output_format", "version", "git_range", "stats", "candidates"},
	}
}

func robotOrphanStatsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total_commits":       map[string]interface{}{"type": "integer"},
			"correlated_count":    map[string]interface{}{"type": "integer"},
			"orphan_count":        map[string]interface{}{"type": "integer"},
			"candidate_count":     map[string]interface{}{"type": "integer"},
			"orphan_ratio":        map[string]interface{}{"type": "number"},
			"avg_suspicion_score": map[string]interface{}{"type": "number"},
		},
	}
}

func robotOrphanCandidateSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sha":             map[string]interface{}{"type": "string"},
			"short_sha":       map[string]interface{}{"type": "string"},
			"message":         map[string]interface{}{"type": "string"},
			"author":          map[string]interface{}{"type": "string"},
			"author_email":    map[string]interface{}{"type": "string"},
			"timestamp":       map[string]interface{}{"type": "string", "format": "date-time"},
			"files":           robotNullableArraySchema(map[string]interface{}{"type": "string"}),
			"suspicion_score": map[string]interface{}{"type": "integer"},
			"probable_beads":  map[string]interface{}{"type": "array", "items": robotProbableBeadSchema()},
			"signals":         map[string]interface{}{"type": "array", "items": robotOrphanSignalSchema()},
		},
	}
}

func robotProbableBeadSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":     map[string]interface{}{"type": "string"},
			"bead_title":  map[string]interface{}{"type": "string"},
			"bead_status": map[string]interface{}{"type": "string"},
			"confidence":  map[string]interface{}{"type": "integer"},
			"reasons":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
	}
}

func robotOrphanSignalSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"signal":  map[string]interface{}{"type": "string"},
			"details": map[string]interface{}{"type": "string"},
			"weight":  map[string]interface{}{"type": "integer"},
		},
	}
}

func robotFileBeadsOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot File Beads Output", "Beads that touched a specific file path", map[string]interface{}{
		"file_path":    map[string]interface{}{"type": "string"},
		"total_beads":  map[string]interface{}{"type": "integer"},
		"open_beads":   robotNullableArraySchema(robotBeadReferenceSchema()),
		"closed_beads": robotNullableArraySchema(robotBeadReferenceSchema()),
	}, []string{"generated_at", "data_hash", "output_format", "version", "file_path", "total_beads", "open_beads", "closed_beads"})
}

func robotFileHotspotsOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot File Hotspots Output", "Files touched by the most beads", map[string]interface{}{
		"hotspots": robotNullableArraySchema(robotFileHotspotSchema()),
		"stats":    robotFileIndexStatsSchema(),
	}, []string{"generated_at", "data_hash", "output_format", "version", "hotspots", "stats"})
}

func robotFileRelationsOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot File Relations Output", "Files that frequently co-change with a given file", map[string]interface{}{
		"file_path":     map[string]interface{}{"type": "string"},
		"total_commits": map[string]interface{}{"type": "integer"},
		"threshold":     map[string]interface{}{"type": "number"},
		"related_files": robotNullableArraySchema(robotCoChangeEntrySchema()),
	}, []string{"generated_at", "data_hash", "output_format", "version", "file_path", "total_commits", "threshold", "related_files"})
}

func robotImpactOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot Impact Output", "Bead impact analysis for files that may be modified", map[string]interface{}{
		"files":          robotNullableArraySchema(map[string]interface{}{"type": "string"}),
		"risk_level":     map[string]interface{}{"type": "string"},
		"risk_score":     map[string]interface{}{"type": "number"},
		"summary":        map[string]interface{}{"type": "string"},
		"warnings":       robotNullableArraySchema(map[string]interface{}{"type": "string"}),
		"affected_beads": robotNullableArraySchema(robotAffectedBeadSchema()),
	}, []string{"generated_at", "data_hash", "output_format", "version", "files", "risk_level", "risk_score", "summary", "warnings", "affected_beads"})
}

func robotRelatedOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot Related Output", "Beads related to a specific bead ID", map[string]interface{}{
		"target_bead_id":     map[string]interface{}{"type": "string"},
		"target_title":       map[string]interface{}{"type": "string"},
		"file_overlap":       map[string]interface{}{"type": "array", "items": robotRelatedWorkBeadSchema()},
		"commit_overlap":     map[string]interface{}{"type": "array", "items": robotRelatedWorkBeadSchema()},
		"dependency_cluster": map[string]interface{}{"type": "array", "items": robotRelatedWorkBeadSchema()},
		"concurrent":         map[string]interface{}{"type": "array", "items": robotRelatedWorkBeadSchema()},
		"total_related":      map[string]interface{}{"type": "integer"},
	}, []string{"generated_at", "data_hash", "output_format", "version", "target_bead_id", "target_title", "file_overlap", "commit_overlap", "dependency_cluster", "concurrent", "total_related"})
}

func robotRelatedWorkBeadSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":        map[string]interface{}{"type": "string"},
			"title":          map[string]interface{}{"type": "string"},
			"status":         map[string]interface{}{"type": "string"},
			"relation_type":  map[string]interface{}{"type": "string"},
			"relevance":      map[string]interface{}{"type": "integer"},
			"reason":         map[string]interface{}{"type": "string"},
			"shared_files":   robotNullableArraySchema(map[string]interface{}{"type": "string"}),
			"shared_commits": robotNullableArraySchema(map[string]interface{}{"type": "string"}),
		},
	}
}

func robotBlockerChainOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot Blocker Chain Output", "Full blocker chain analysis for an issue", map[string]interface{}{
		"result": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_id":     map[string]interface{}{"type": "string"},
				"target_title":  map[string]interface{}{"type": "string"},
				"is_blocked":    map[string]interface{}{"type": "boolean"},
				"chain_length":  map[string]interface{}{"type": "integer"},
				"root_blockers": map[string]interface{}{"type": "array", "items": robotBlockerChainEntrySchema()},
				"chain":         map[string]interface{}{"type": "array", "items": robotBlockerChainEntrySchema()},
				"has_cycle":     map[string]interface{}{"type": "boolean"},
				"cycle_ids":     robotNullableArraySchema(map[string]interface{}{"type": "string"}),
			},
		},
	}, []string{"generated_at", "data_hash", "output_format", "version", "result"})
}

func robotBlockerChainEntrySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":           map[string]interface{}{"type": "string"},
			"title":        map[string]interface{}{"type": "string"},
			"status":       map[string]interface{}{"type": "string"},
			"priority":     map[string]interface{}{"type": "integer"},
			"depth":        map[string]interface{}{"type": "integer"},
			"is_root":      map[string]interface{}{"type": "boolean"},
			"actionable":   map[string]interface{}{"type": "boolean"},
			"blocks_count": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotImpactNetworkOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot Impact Network Output", "Impact network graph for all beads or one bead subnetwork", map[string]interface{}{
		"bead_id":       map[string]interface{}{"type": "string"},
		"depth":         map[string]interface{}{"type": "integer"},
		"network":       robotImpactNetworkSchema(),
		"stats":         robotNetworkStatsSchema(),
		"top_clusters":  robotNullableArraySchema(robotBeadClusterSchema()),
		"top_connected": robotNullableArraySchema(robotNetworkNodeSchema()),
	}, []string{"generated_at", "data_hash", "output_format", "version", "depth", "network", "stats"})
}

func robotImpactNetworkSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":    map[string]interface{}{"type": "string"},
			"nodes": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": robotNetworkNodeSchema(),
			},
			"edges":    robotNullableArraySchema(robotNetworkEdgeSchema()),
			"clusters": robotNullableArraySchema(robotBeadClusterSchema()),
			"stats":    robotNetworkStatsSchema(),
		},
	}
}

func robotNetworkNodeSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":       map[string]interface{}{"type": "string"},
			"title":         map[string]interface{}{"type": "string"},
			"status":        map[string]interface{}{"type": "string"},
			"priority":      map[string]interface{}{"type": "integer"},
			"last_activity": map[string]interface{}{"type": "string", "format": "date-time"},
			"degree":        map[string]interface{}{"type": "integer"},
			"cluster_id":    map[string]interface{}{"type": "integer"},
			"commit_count":  map[string]interface{}{"type": "integer"},
			"file_count":    map[string]interface{}{"type": "integer"},
			"connectivity":  map[string]interface{}{"type": "number"},
		},
	}
}

func robotNetworkEdgeSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": true,
	}
}

func robotBeadClusterSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"cluster_id":            map[string]interface{}{"type": "integer"},
			"bead_ids":              map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"label":                 map[string]interface{}{"type": "string"},
			"internal_edges":        map[string]interface{}{"type": "integer"},
			"external_edges":        map[string]interface{}{"type": "integer"},
			"internal_connectivity": map[string]interface{}{"type": "number"},
			"central_bead":          map[string]interface{}{"type": "string"},
			"shared_files":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"total_commits":         map[string]interface{}{"type": "integer"},
		},
	}
}

func robotNetworkStatsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total_nodes":     map[string]interface{}{"type": "integer"},
			"total_edges":     map[string]interface{}{"type": "integer"},
			"cluster_count":   map[string]interface{}{"type": "integer"},
			"avg_degree":      map[string]interface{}{"type": "number"},
			"max_degree":      map[string]interface{}{"type": "integer"},
			"density":         map[string]interface{}{"type": "number"},
			"isolated_nodes":  map[string]interface{}{"type": "integer"},
			"largest_cluster": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotCausalityOutputSchema() map[string]interface{} {
	return robotFileCommandOutputSchema("Robot Causality Output", "Causal chain analysis for a bead", map[string]interface{}{
		"chain":    robotCausalChainSchema(),
		"insights": robotCausalInsightsSchema(),
	}, []string{"generated_at", "data_hash", "output_format", "version", "chain", "insights"})
}

func robotCausalChainSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":     map[string]interface{}{"type": "string"},
			"title":       map[string]interface{}{"type": "string"},
			"status":      map[string]interface{}{"type": "string"},
			"events":      map[string]interface{}{"type": "array", "items": robotCausalEventSchema()},
			"edge_count":  map[string]interface{}{"type": "integer"},
			"start_time":  map[string]interface{}{"type": "string", "format": "date-time"},
			"end_time":    map[string]interface{}{"type": "string", "format": "date-time"},
			"total_time":  map[string]interface{}{"type": "integer"},
			"is_complete": map[string]interface{}{"type": "boolean"},
		},
	}
}

func robotCausalEventSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":            map[string]interface{}{"type": "integer"},
			"type":          map[string]interface{}{"type": "string"},
			"timestamp":     map[string]interface{}{"type": "string", "format": "date-time"},
			"description":   map[string]interface{}{"type": "string"},
			"commit_sha":    map[string]interface{}{"type": "string"},
			"blocker_id":    map[string]interface{}{"type": "string"},
			"caused_by_id":  map[string]interface{}{"type": "integer"},
			"enables_ids":   robotNullableArraySchema(map[string]interface{}{"type": "integer"}),
			"duration_next": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotCausalInsightsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total_duration":     map[string]interface{}{"type": "integer"},
			"blocked_duration":   map[string]interface{}{"type": "integer"},
			"active_duration":    map[string]interface{}{"type": "integer"},
			"blocked_percentage": map[string]interface{}{"type": "number"},
			"blocked_periods":    robotNullableArraySchema(robotBlockedPeriodSchema()),
			"critical_path":      robotNullableArraySchema(map[string]interface{}{"type": "integer"}),
			"critical_path_desc": map[string]interface{}{"type": "string"},
			"commit_count":       map[string]interface{}{"type": "integer"},
			"avg_time_between":   map[string]interface{}{"type": "integer"},
			"longest_gap":        map[string]interface{}{"type": "integer"},
			"longest_gap_desc":   map[string]interface{}{"type": "string"},
			"estimated_without":  map[string]interface{}{"type": "integer"},
			"summary":            map[string]interface{}{"type": "string"},
			"recommendations":    robotNullableArraySchema(map[string]interface{}{"type": "string"}),
		},
	}
}

func robotBlockedPeriodSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"start_time": map[string]interface{}{"type": "string", "format": "date-time"},
			"end_time":   map[string]interface{}{"type": "string", "format": "date-time"},
			"duration":   map[string]interface{}{"type": "integer"},
			"blocker_id": map[string]interface{}{"type": "string"},
		},
	}
}

func robotNullableArraySchema(items map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":  []string{"array", "null"},
		"items": items,
	}
}

func robotFileCommandOutputSchema(title, description string, commandProperties map[string]interface{}, required []string) map[string]interface{} {
	properties := map[string]interface{}{
		"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
		"data_hash":     map[string]interface{}{"type": "string"},
		"output_format": map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
		"version":       map[string]interface{}{"type": "string"},
	}
	for name, schema := range commandProperties {
		properties[name] = schema
	}
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       title,
		"description": description,
		"type":        "object",
		"properties":  properties,
		"required":    required,
	}
}

func robotBeadReferenceSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":       map[string]interface{}{"type": "string"},
			"title":         map[string]interface{}{"type": "string"},
			"status":        map[string]interface{}{"type": "string"},
			"commit_shas":   robotNullableArraySchema(map[string]interface{}{"type": "string"}),
			"last_touch":    map[string]interface{}{"type": "string", "format": "date-time"},
			"total_changes": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotFileHotspotSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":    map[string]interface{}{"type": "string"},
			"total_beads":  map[string]interface{}{"type": "integer"},
			"open_beads":   map[string]interface{}{"type": "integer"},
			"closed_beads": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotFileIndexStatsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"total_files":               map[string]interface{}{"type": "integer"},
			"total_bead_links":          map[string]interface{}{"type": "integer"},
			"files_with_multiple_beads": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotCoChangeEntrySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":       map[string]interface{}{"type": "string"},
			"co_change_count": map[string]interface{}{"type": "integer"},
			"total_commits":   map[string]interface{}{"type": "integer"},
			"correlation":     map[string]interface{}{"type": "number"},
			"sample_commits":  robotNullableArraySchema(map[string]interface{}{"type": "string"}),
		},
	}
}

func robotAffectedBeadSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bead_id":       map[string]interface{}{"type": "string"},
			"title":         map[string]interface{}{"type": "string"},
			"status":        map[string]interface{}{"type": "string"},
			"overlap_files": robotNullableArraySchema(map[string]interface{}{"type": "string"}),
			"overlap_count": map[string]interface{}{"type": "integer"},
			"last_activity": map[string]interface{}{"type": "string", "format": "date-time"},
			"relevance":     map[string]interface{}{"type": "number"},
			"total_changes": map[string]interface{}{"type": "integer"},
		},
	}
}

func robotGroupedTriageOutputSchema(title, description, groupProperty string, groupItemSchema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       title,
		"description": description,
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":    map[string]interface{}{"type": "string"},
			"as_of":        map[string]interface{}{"type": "string"},
			"as_of_commit": map[string]interface{}{"type": "string"},
			"triage": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"meta":              map[string]interface{}{"type": "object"},
					"quick_ref":         map[string]interface{}{"type": "object"},
					"recommendations":   map[string]interface{}{"type": "array"},
					"quick_wins":        map[string]interface{}{"type": "array"},
					"blockers_to_clear": map[string]interface{}{"type": "array"},
					"project_health":    map[string]interface{}{"type": "object"},
					"commands":          map[string]interface{}{"type": "object"},
					groupProperty: map[string]interface{}{
						"type":  "array",
						"items": groupItemSchema,
					},
				},
				"required": []string{"meta", "quick_ref", "recommendations"},
			},
			"feedback":    map[string]interface{}{"type": "object"},
			"usage_hints": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"generated_at", "data_hash", "triage", "usage_hints"},
	}
}

func robotTrackRecommendationGroupSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"track_id":        map[string]interface{}{"type": "string"},
			"reason":          map[string]interface{}{"type": "string"},
			"recommendations": map[string]interface{}{"type": "array"},
			"top_pick":        map[string]interface{}{"type": "object"},
			"claim_command":   map[string]interface{}{"type": "string"},
			"total_unblocks":  map[string]interface{}{"type": "integer"},
		},
		"required": []string{"track_id", "reason", "recommendations", "total_unblocks"},
	}
}

func robotLabelRecommendationGroupSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"label":           map[string]interface{}{"type": "string"},
			"recommendations": map[string]interface{}{"type": "array"},
			"top_pick":        map[string]interface{}{"type": "object"},
			"claim_command":   map[string]interface{}{"type": "string"},
			"total_unblocks":  map[string]interface{}{"type": "integer"},
		},
		"required": []string{"label", "recommendations", "total_unblocks"},
	}
}

func robotHistoryOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Robot History Output",
		"description": "Bead-to-commit correlation history report with aggregate stats and reverse commit index",
		"type":        "object",
		"properties": map[string]interface{}{
			"generated_at":      map[string]interface{}{"type": "string", "format": "date-time"},
			"data_hash":         map[string]interface{}{"type": "string"},
			"output_format":     map[string]interface{}{"type": "string", "enum": []string{"json", "toon"}},
			"version":           map[string]interface{}{"type": "string"},
			"git_range":         map[string]interface{}{"type": "string"},
			"latest_commit_sha": map[string]interface{}{"type": "string"},
			"stats":             map[string]interface{}{"type": "object"},
			"histories":         map[string]interface{}{"type": "object"},
			"commit_index":      map[string]interface{}{"type": "object"},
		},
		"required": []string{"generated_at", "data_hash", "output_format", "version", "git_range", "stats", "histories", "commit_index"},
	}
}

func genericRobotCommandSchema(name string, doc robotCommandDoc) map[string]interface{} {
	properties := map[string]interface{}{
		"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
		"data_hash":    map[string]interface{}{"type": "string"},
		"output_format": map[string]interface{}{
			"type": "string",
			"enum": []string{"json", "toon"},
		},
		"version": map[string]interface{}{"type": "string"},
	}
	if !doc.NeedsIssues {
		delete(properties, "data_hash")
	}

	return map[string]interface{}{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                titleCaseRobotCommand(name) + " Output",
		"description":          doc.Description,
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	}
}

func titleCaseRobotCommand(name string) string {
	parts := strings.Split(strings.ReplaceAll(name, "-", " "), " ")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
