// Package docgen generates documentation artifacts and Markdown tables from code
// for beads_viewer (bv).
//
// It generates:
//   - docs/generated/flags.md
//   - docs/generated/env.md
//   - docs/generated/alerts.md
//   - docs/generated/recipes.md
//   - docs/generated/presets.md
//   - docs/generated/keys.md
//   - docs/generated/sort_modes.md
//   - docs/generated/constants.json
//
// And updates marker pairs in README.md and AGENTS.md between:
//
//	<!-- bv:generated:<name> -->
//	...
//	<!-- /bv:generated -->
package docgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

// FlagSection maps flags to a category/group.
type FlagSection struct {
	Title string
	Match func(string) bool
}

// DefaultFlagSections mirrors rootHelpSections from cmd/bv/main.go.
func DefaultFlagSections() []FlagSection {
	return []FlagSection{
		{
			Title: "General Flags",
			Match: func(name string) bool {
				switch name {
				case "help", "version", "db", "as-of", "recipe", "label",
					"filter", "theme", "no-browser", "no-hooks", "save-baseline",
					"background-mode", "no-background-mode", "check-update", "update":
					return true
				default:
					return false
				}
			},
		},
		{
			Title: "Search & Filters",
			Match: func(name string) bool {
				switch name {
				case "search", "search-mode", "search-preset", "search-weights",
					"search-limit", "search-score-threshold", "search-rebuild",
					"priority-intent", "priority-history-window", "priority-sample-size",
					"priority-explain", "priority-include-closed":
					return true
				default:
					return false
				}
			},
		},
		{
			Title: "Robot & Planning Flags",
			Match: func(name string) bool {
				return strings.HasPrefix(name, "robot-") || name == "format" || name == "toon-stats" || name == "no-cache" || name == "force-full-analysis"
			},
		},
		{
			Title: "History & Drift",
			Match: func(name string) bool {
				switch name {
				case "diff-since", "drift", "drift-since", "drift-critical-only", "history", "recent":
					return true
				default:
					return false
				}
			},
		},
		{
			Title: "Export & Reporting",
			Match: func(name string) bool {
				switch name {
				case "export-pages", "watch-export", "export-graph", "pages", "preview-pages",
					"pages-title", "pages-base-url", "graph-format", "burndown", "forecast":
					return true
				default:
					return false
				}
			},
		},
		{
			Title: "Agent File Management",
			Match: func(name string) bool {
				switch name {
				case "install-agent-blurb", "check-agent-blurb", "blurb-format", "workspace":
					return true
				default:
					return false
				}
			},
		},
		{
			Title: "Debug Flags",
			Match: func(name string) bool {
				switch name {
				case "cpu-profile", "profile-startup", "profile-json", "debug-render",
					"debug-width", "debug-height", "generate-docs":
					return true
				default:
					return false
				}
			},
		},
	}
}

// RenderFlagsTable generates a Markdown table of CLI flags from the given pflag.FlagSet.
func RenderFlagsTable(fs *pflag.FlagSet, sections []FlagSection) string {
	if sections == nil {
		sections = DefaultFlagSections()
	}

	type flagInfo struct {
		name        string
		typeStr     string
		defValue    string
		description string
		group       string
		isDebug     bool
	}

	var flags []flagInfo

	fs.VisitAll(func(f *pflag.Flag) {
		// Identify group
		group := "Other"
		isDebug := false
		for _, s := range sections {
			if s.Match(f.Name) {
				group = s.Title
				if s.Title == "Debug Flags" {
					isDebug = true
				}
				break
			}
		}

		typeStr := f.Value.Type()
		defVal := f.DefValue
		if defVal == "" {
			defVal = `(empty)`
		} else if defVal == "false" || defVal == "true" {
			defVal = "`" + defVal + "`"
		} else {
			defVal = "`" + defVal + "`"
		}

		desc := strings.ReplaceAll(f.Usage, "\n", " ")

		flags = append(flags, flagInfo{
			name:        f.Name,
			typeStr:     typeStr,
			defValue:    defVal,
			description: desc,
			group:       group,
			isDebug:     isDebug,
		})
	})

	sort.Slice(flags, func(i, j int) bool {
		// Debug flags at the end
		if flags[i].isDebug != flags[j].isDebug {
			return !flags[i].isDebug
		}
		if flags[i].group != flags[j].group {
			return flags[i].group < flags[j].group
		}
		return flags[i].name < flags[j].name
	})

	var sb strings.Builder
	sb.WriteString("| Flag | Type | Default | Description | Group |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
	for _, f := range flags {
		flagName := "`--" + f.name + "`"
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			flagName, f.typeStr, f.defValue, f.description, f.group))
	}

	return strings.TrimSpace(sb.String())
}

// RenderEnvTable generates a Markdown table of environment variables from internal/env.
func RenderEnvTable(vars []env.Var) string {
	if vars == nil {
		vars = env.All()
	}

	var sb strings.Builder
	sb.WriteString("| Variable | Description | Default |\n")
	sb.WriteString("|:---|:---|:---|\n")

	for _, v := range vars {
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", v.Name, v.Description, v.Default))
	}

	return strings.TrimSpace(sb.String())
}

// RenderAlertsTables generates the proactive and drift check Markdown tables.
func RenderAlertsTables(cfg *drift.Config) string {
	if cfg == nil {
		cfg = drift.DefaultConfig()
	}

	var sb strings.Builder
	sb.WriteString("**Proactive checks** (run on the current graph, no baseline needed):\n\n")
	sb.WriteString("| Type | Trigger | Severity | `.bv/drift.yaml` keys (default) |\n")
	sb.WriteString("|------|---------|----------|----------------------------------|\n")

	proactive := []struct {
		typ      drift.AlertType
		trigger  string
		severity string
		keys     string
	}{
		{
			typ:      drift.AlertStaleIssue,
			trigger:  "No activity for `stale_warning_days` (warning) or `stale_critical_days` (critical); thresholds are multiplied by `in_progress_stale_multiplier` for `in_progress` issues; `label_overrides` can tighten or loosen per label",
			severity: "Warning / Critical",
			keys:     fmt.Sprintf("`stale_warning_days` (%d), `stale_critical_days` (%d), `in_progress_stale_multiplier` (%.1f)", cfg.StaleWarningDays, cfg.StaleCriticalDays, cfg.InProgressStaleMultiplier),
		},
		{
			typ:      drift.AlertBlockingCascade,
			trigger:  "Actionable issue unblocks N+ others",
			severity: "Info / Warning",
			keys:     fmt.Sprintf("`blocking_cascade_info_threshold` (%d), `blocking_cascade_warning_threshold` (%d)", cfg.BlockingCascadeInfo, cfg.BlockingCascadeWarning),
		},
		{
			typ:      drift.AlertHighImpactUnblock,
			trigger:  "Actionable issue unblocks N+ others of which at least one is P0/P1 (two or more urgent items escalate to warning)",
			severity: "Info / Warning",
			keys:     fmt.Sprintf("`high_impact_unblock_min` (%d), `high_impact_priority_max` (%d)", cfg.HighImpactUnblockMin, cfg.HighImpactPriorityMax),
		},
		{
			typ:      drift.AlertAbandonedClaim,
			trigger:  "An `in_progress` issue with an assignee idle longer than `stale_warning_days` x `in_progress_stale_multiplier` x `abandoned_claim_multiplier` (14 days by default)",
			severity: "Warning",
			keys:     fmt.Sprintf("`abandoned_claim_multiplier` (%g)", cfg.AbandonedClaimMultiplier),
		},
		{
			typ:      drift.AlertPotentialDuplicate,
			trigger:  "Two open issues whose title/description keyword Jaccard similarity reaches the threshold (same detector as `--robot-suggest`); closed issues are never paired",
			severity: "Info",
			keys:     fmt.Sprintf("`duplicate_jaccard_threshold` (%.1f), `duplicate_max_alerts` (%d)", cfg.DuplicateJaccardThreshold, cfg.DuplicateMaxAlerts),
		},
		{
			typ:      drift.AlertPriorityMismatch,
			trigger:  "`--robot-priority` recommends a *higher* priority with confidence at or above the floor (downgrade suggestions stay in `--robot-priority`)",
			severity: "Warning",
			keys:     fmt.Sprintf("`priority_mismatch_min_confidence` (%.1f)", cfg.PriorityMismatchMinConfidence),
		},
		{
			typ:      drift.AlertVelocityDrop,
			trigger:  "Closes in the last window fell by the percentage or more versus the previous window, which must contain at least the baseline count of closes",
			severity: "Warning",
			keys:     fmt.Sprintf("`velocity_drop_pct` (%g), `velocity_window_days` (%d), `velocity_min_baseline` (%d)", cfg.VelocityDropPct, cfg.VelocityWindowDays, cfg.VelocityMinBaseline),
		},
	}

	for _, p := range proactive {
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", p.typ, p.trigger, p.severity, p.keys))
	}

	sb.WriteString("\n**Drift checks** (compare the current graph with the baseline saved by `bv --save-baseline`):\n\n")
	sb.WriteString("| Type | Trigger | Severity | `.bv/drift.yaml` keys (default) |\n")
	sb.WriteString("|------|---------|----------|----------------------------------|\n")

	driftChecks := []struct {
		typ      drift.AlertType
		trigger  string
		severity string
		keys     string
	}{
		{
			typ:      drift.AlertNewCycle,
			trigger:  "A cycle exists that the baseline did not have",
			severity: "Critical",
			keys:     "(always on unless disabled)",
		},
		{
			typ:      drift.AlertDensityGrowth,
			trigger:  "Graph density up by the info or warning percentage",
			severity: "Info / Warning",
			keys:     fmt.Sprintf("`density_info_pct` (%g), `density_warning_pct` (%g)", cfg.DensityInfoPct, cfg.DensityWarningPct),
		},
		{
			typ:      drift.AlertNodeCountChange,
			trigger:  "Node count changed by the percentage or more",
			severity: "Info",
			keys:     fmt.Sprintf("`node_growth_info_pct` (%g)", cfg.NodeGrowthInfoPct),
		},
		{
			typ:      drift.AlertEdgeCountChange,
			trigger:  "Edge count changed by the percentage or more",
			severity: "Info",
			keys:     fmt.Sprintf("`edge_growth_info_pct` (%g)", cfg.EdgeGrowthInfoPct),
		},
		{
			typ:      drift.AlertScopeCreep,
			trigger:  "Open-issue count grew by the percentage or more since the baseline",
			severity: "Info",
			keys:     fmt.Sprintf("`scope_creep_pct` (%g)", cfg.ScopeCreepPct),
		},
		{
			typ:      drift.AlertBlockedIncrease,
			trigger:  "N or more additional blocked issues",
			severity: "Warning",
			keys:     fmt.Sprintf("`blocked_increase_threshold` (%d)", cfg.BlockedIncreaseThreshold),
		},
		{
			typ:      drift.AlertActionableChange,
			trigger:  "Actionable count down by the warning percentage, or changed by the info percentage",
			severity: "Info / Warning",
			keys:     fmt.Sprintf("`actionable_decrease_warning_pct` (%g), `actionable_increase_info_pct` (%g)", cfg.ActionableDecreaseWarningPct, cfg.ActionableIncreaseInfoPct),
		},
		{
			typ:      drift.AlertPageRankChange,
			trigger:  "A top-metric issue's PageRank moved by the percentage or more",
			severity: "Warning",
			keys:     fmt.Sprintf("`pagerank_change_warning_pct` (%g)", cfg.PageRankChangeWarningPct),
		},
	}

	for _, d := range driftChecks {
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", d.typ, d.trigger, d.severity, d.keys))
	}

	return strings.TrimSpace(sb.String())
}

// RenderRecipesTable generates the Markdown table of built-in recipes.
func RenderRecipesTable() string {
	l := recipe.NewLoader()
	_ = l.Load()
	recipes := l.List()

	// Keep clean standard ordering
	order := []string{
		"default", "actionable", "recent", "blocked", "high-impact",
		"stale", "triage", "closed", "release-cut", "quick-wins", "bottlenecks",
	}

	recipeMap := make(map[string]recipe.Recipe)
	for _, r := range recipes {
		recipeMap[r.Name] = r
	}

	var sb strings.Builder
	sb.WriteString("| Recipe | Purpose |\n")
	sb.WriteString("|:---|:---|\n")

	seen := make(map[string]bool)
	for _, name := range order {
		if r, ok := recipeMap[name]; ok {
			sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", r.Name, r.Description))
			seen[name] = true
		}
	}

	// Any others
	for _, r := range recipes {
		if !seen[r.Name] {
			sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", r.Name, r.Description))
		}
	}

	return strings.TrimSpace(sb.String())
}

// RenderPresetsTable generates the Markdown table of hybrid search presets.
func RenderPresetsTable() string {
	presets := search.ListPresets()
	var sb strings.Builder
	sb.WriteString("| Preset | Text | PageRank | Status | Impact | Priority | Recency | Description |\n")
	sb.WriteString("|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---|\n")

	descriptions := map[string]string{
		"default":         "Balanced general-purpose search (text-led with graph context)",
		"bug-hunting":     "Prioritizes open issues with high impact and recency",
		"sprint-planning": "Heavily weights PageRank and blocker impact for sprint grooming",
		"impact-first":    "Centrality-first: PageRank and graph impact dominate text matches",
		"text-only":       "Pure keyword/semantic similarity with zero graph metric weighting",
	}

	for _, name := range presets {
		p, err := search.GetPreset(name)
		if err != nil {
			continue
		}
		desc := descriptions[string(name)]
		sb.WriteString(fmt.Sprintf("| `%s` | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %s |\n",
			name, p.TextRelevance, p.PageRank, p.Status, p.Impact, p.Priority, p.Recency, desc))
	}

	return strings.TrimSpace(sb.String())
}

// RenderKeysTable generates the Markdown table of key bindings.
func RenderKeysTable() string {
	bindings := ui.GetKeyBindingDocs()

	var sb strings.Builder
	sb.WriteString("| Context | Key | Action |\n")
	sb.WriteString("| :--- | :---: | :--- |\n")

	lastContext := ""
	for _, b := range bindings {
		contextDisplay := ""
		if b.Context != lastContext {
			contextDisplay = "**" + b.Context + "**"
			lastContext = b.Context
		}

		keyDisplay := b.Key
		if !strings.HasPrefix(keyDisplay, "`") {
			parts := strings.Split(keyDisplay, ", ")
			var formattedParts []string
			for _, p := range parts {
				formattedParts = append(formattedParts, "`"+p+"`")
			}
			keyDisplay = strings.Join(formattedParts, ", ")
		}

		sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", contextDisplay, keyDisplay, b.Desc))
	}

	return strings.TrimSpace(sb.String())
}

// RenderSortModesTable generates the Markdown table of sort modes.
func RenderSortModesTable() string {
	var sb strings.Builder
	sb.WriteString("| Mode | Key Display | Ordering Logic | Use Case |\n")
	sb.WriteString("|:---|:---:|:---|:---|\n")
	sb.WriteString("| **Default** | `Default` | Priority (asc) → Created (desc) | Standard priority-driven workflow |\n")
	sb.WriteString("| **Created ↑** | `Created ↑` | Creation date ascending (oldest first) | Audit: find long-standing issues |\n")
	sb.WriteString("| **Created ↓** | `Created ↓` | Creation date descending (newest first) | Review: see recently created work |\n")
	sb.WriteString("| **Priority** | `Priority` | Priority only (P0 → P4) | Pure priority triage |\n")
	sb.WriteString("| **Updated** | `Updated` | Last update descending (newest first) | Activity tracking: see active issues |\n")
	return strings.TrimSpace(sb.String())
}

// GenerateConstantsJSON builds the constants data structure and encodes it to JSON.
func GenerateConstantsJSON() ([]byte, error) {
	driftDef := drift.DefaultConfig()

	data := map[string]any{
		"impact_weights": map[string]float64{
			"pagerank":       analysis.WeightPageRank,
			"betweenness":    analysis.WeightBetweenness,
			"blocker_ratio":  analysis.WeightBlockerRatio,
			"staleness":      analysis.WeightStaleness,
			"priority_boost": analysis.WeightPriorityBoost,
			"time_to_impact": analysis.WeightTimeToImpact,
			"urgency":        analysis.WeightUrgency,
			"risk":           analysis.WeightRisk,
		},
		"label_health": map[string]any{
			"weights": map[string]float64{
				"velocity":    analysis.VelocityWeight,
				"freshness":   analysis.FreshnessWeight,
				"flow":        analysis.FlowWeight,
				"criticality": analysis.CriticalityWeight,
			},
			"thresholds": map[string]any{
				"healthy":    analysis.HealthyThreshold,
				"warning":    analysis.WarningThreshold,
				"stale_days": analysis.DefaultStaleThresholdDays,
			},
		},
		"timeout_tiers": map[string]any{
			"small": map[string]any{
				"max_nodes":  99,
				"timeout_ms": 2000,
				"mode":       "exact",
			},
			"medium": map[string]any{
				"min_nodes":  100,
				"max_nodes":  499,
				"timeout_ms": 500,
				"mode":       "exact",
			},
			"large": map[string]any{
				"min_nodes":                     500,
				"max_nodes":                     1999,
				"timeout_ms":                    300,
				"approx_betweenness_timeout_ms": 500,
				"mode":                          "approximate",
			},
			"xl": map[string]any{
				"min_nodes":                     2000,
				"timeout_ms":                    200,
				"approx_betweenness_timeout_ms": 500,
				"mode":                          "approximate",
			},
		},
		"staleness_thresholds": map[string]any{
			"priority_staleness_days":            30,
			"label_health_stale_days":            analysis.DefaultStaleThresholdDays,
			"drift_stale_warning_days":           driftDef.StaleWarningDays,
			"drift_stale_critical_days":          driftDef.StaleCriticalDays,
			"drift_in_progress_stale_multiplier": driftDef.InProgressStaleMultiplier,
		},
		"correlation_ranges": map[string]any{
			"co_committed": map[string]any{
				"min":         correlation.MethodRanges[correlation.MethodCoCommitted].Min,
				"max":         correlation.MethodRanges[correlation.MethodCoCommitted].Max,
				"description": correlation.MethodRanges[correlation.MethodCoCommitted].Desc,
			},
			"explicit_id": map[string]any{
				"min":         correlation.MethodRanges[correlation.MethodExplicitID].Min,
				"max":         correlation.MethodRanges[correlation.MethodExplicitID].Max,
				"description": correlation.MethodRanges[correlation.MethodExplicitID].Desc,
			},
			"temporal_author": map[string]any{
				"min":         correlation.MethodRanges[correlation.MethodTemporalAuthor].Min,
				"max":         correlation.MethodRanges[correlation.MethodTemporalAuthor].Max,
				"description": correlation.MethodRanges[correlation.MethodTemporalAuthor].Desc,
			},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return nil, fmt.Errorf("encoding constants JSON: %w", err)
	}
	return buf.Bytes(), nil
}

// ReplaceBetweenMarkers finds <!-- bv:generated:<name> --> and <!-- /bv:generated -->
// and replaces everything in between. If end marker is <!-- /bv:generated:<name> -->, it handles that too.
func ReplaceBetweenMarkers(content, markerName, newContent string) (string, bool) {
	pattern := fmt.Sprintf(`(?s)(<!--\s*bv:generated:%s\s*-->)(.*?)(<!--\s*/bv:generated(?::%s)?\s*-->)`,
		regexp.QuoteMeta(markerName), regexp.QuoteMeta(markerName))
	re := regexp.MustCompile(pattern)

	if !re.MatchString(content) {
		return content, false
	}

	replacement := fmt.Sprintf("${1}\n%s\n${3}", strings.TrimSpace(newContent))
	return re.ReplaceAllString(content, replacement), true
}

// GenerateOptions configures documentation generation.
type GenerateOptions struct {
	RepoRoot    string
	FlagSet     *pflag.FlagSet
	Sections    []FlagSection
	DriftConfig *drift.Config
}

// Generate executes generation for docs/generated and updates markers in README.md and AGENTS.md.
func Generate(opts GenerateOptions) error {
	repoRoot := opts.RepoRoot
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting cwd: %w", err)
		}
		repoRoot = findRepoRoot(cwd)
	}

	generatedDir := filepath.Join(repoRoot, "docs", "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", generatedDir, err)
	}

	// 1. Generate tables
	flagsTable := RenderFlagsTable(opts.FlagSet, opts.Sections)
	envTable := RenderEnvTable(nil)
	alertsTable := RenderAlertsTables(opts.DriftConfig)
	recipesTable := RenderRecipesTable()
	presetsTable := RenderPresetsTable()
	keysTable := RenderKeysTable()
	sortModesTable := RenderSortModesTable()
	constantsJSON, err := GenerateConstantsJSON()
	if err != nil {
		return fmt.Errorf("generating constants.json: %w", err)
	}

	// 2. Write standalone generated files
	files := map[string][]byte{
		"flags.md":       []byte("# CLI Flags\n\n" + flagsTable + "\n"),
		"env.md":         []byte("# Environment Variables\n\n" + envTable + "\n"),
		"alerts.md":      []byte("# Alert Checks\n\n" + alertsTable + "\n"),
		"recipes.md":     []byte("# Built-in Recipes\n\n" + recipesTable + "\n"),
		"presets.md":     []byte("# Hybrid Search Presets\n\n" + presetsTable + "\n"),
		"keys.md":        []byte("# Keyboard Shortcuts\n\n" + keysTable + "\n"),
		"sort_modes.md":  []byte("# Sort Modes\n\n" + sortModesTable + "\n"),
		"constants.json": constantsJSON,
	}

	for name, data := range files {
		target := filepath.Join(generatedDir, name)
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", target, err)
		}
	}

	// 3. Update README.md markers
	readmePath := filepath.Join(repoRoot, "README.md")
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("reading README.md: %w", err)
	}
	readmeContent := string(readmeBytes)

	replacements := map[string]string{
		"flags":      flagsTable,
		"env":        envTable,
		"alerts":     alertsTable,
		"recipes":    recipesTable,
		"presets":    presetsTable,
		"keys":       keysTable,
		"sort-modes": sortModesTable,
	}

	updatedReadme := readmeContent
	for marker, table := range replacements {
		if updated, ok := ReplaceBetweenMarkers(updatedReadme, marker, table); ok {
			updatedReadme = updated
		}
	}

	if updatedReadme != readmeContent {
		if err := os.WriteFile(readmePath, []byte(updatedReadme), 0o644); err != nil {
			return fmt.Errorf("updating README.md: %w", err)
		}
	}

	// 4. Update AGENTS.md markers (if any)
	agentsPath := filepath.Join(repoRoot, "AGENTS.md")
	if agentsBytes, err := os.ReadFile(agentsPath); err == nil {
		agentsContent := string(agentsBytes)
		updatedAgents := agentsContent
		for marker, table := range replacements {
			if updated, ok := ReplaceBetweenMarkers(updatedAgents, marker, table); ok {
				updatedAgents = updated
			}
		}
		if updatedAgents != agentsContent {
			if err := os.WriteFile(agentsPath, []byte(updatedAgents), 0o644); err != nil {
				return fmt.Errorf("updating AGENTS.md: %w", err)
			}
		}
	}

	return nil
}

func findRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}
