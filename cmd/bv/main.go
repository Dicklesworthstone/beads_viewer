package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	json "github.com/goccy/go-json"

	toon "github.com/Dicklesworthstone/toon-go"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/beadscli"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/hooks"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
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

func configureCommandBackend(repoPath string) {
	backend := loader.BackendBR
	if beadsDir, err := loader.GetBeadsDir(repoPath); err == nil {
		backend = loader.DetectWorkspaceBackend(beadsDir)
	}
	_ = os.Setenv("BV_CMD_BACKEND", string(backend))
}

func resolveWorkspaceForCLI(repoPath string, refreshBDExport bool) (*loader.WorkspaceResolution, error) {
	return loader.PrepareWorkspaceForRead(repoPath, refreshBDExport, func(msg string) {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	})
}

func loadIssuesForCLI(repoPath string, refreshBDExport bool) ([]model.Issue, string, error) {
	resolution, err := resolveWorkspaceForCLI(repoPath, refreshBDExport)
	if err != nil {
		return nil, "", err
	}

	issues, err := datasource.LoadIssuesFromDir(resolution.BeadsDir)
	if err != nil {
		return nil, "", err
	}

	return issues, resolution.JSONL, nil
}

func main() {
	cpuProfile := flag.String("cpu-profile", "", "Write CPU profile to file")
	dbPath := flag.String("db", "", "Path to beads database file or .beads directory (overrides BEADS_DB and BEADS_DIR env vars)")
	help := flag.Bool("help", false, "Show help")
	versionFlag := flag.Bool("version", false, "Show version")
	// Update flags (bv-182)
	updateFlag := flag.Bool("update", false, "Update bv to the latest version")
	checkUpdateFlag := flag.Bool("check-update", false, "Check if a new version is available")
	rollbackFlag := flag.Bool("rollback", false, "Rollback to the previous version (from backup)")
	yesFlag := flag.Bool("yes", false, "Skip confirmation prompts (use with --update)")
	exportFile := flag.String("export-md", "", "Export issues to a Markdown file (e.g., report.md)")
	robotHelp := flag.Bool("robot-help", false, "Show AI agent help")
	robotDocs := flag.String("robot-docs", "", "Machine-readable JSON docs for AI agents. Topics: guide, commands, examples, env, exit-codes, all")
	outputFormat := flag.String("format", "", "Structured output format for --robot-* commands: json or toon (env: BV_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT)")
	toonStats := flag.Bool("stats", false, "Show JSON vs TOON token estimates on stderr (env: TOON_STATS=1)")
	robotInsights := flag.Bool("robot-insights", false, "Output graph analysis and insights as JSON for AI agents")
	robotPlan := flag.Bool("robot-plan", false, "Output dependency-respecting execution plan as JSON for AI agents")
	robotPriority := flag.Bool("robot-priority", false, "Output priority recommendations as JSON for AI agents")
	robotTriage := flag.Bool("robot-triage", false, "Output unified triage as JSON (the mega-command for AI agents)")
	robotTriageByTrack := flag.Bool("robot-triage-by-track", false, "Group triage recommendations by execution track (bv-87)")
	robotTriageByLabel := flag.Bool("robot-triage-by-label", false, "Group triage recommendations by label (bv-87)")
	robotNext := flag.Bool("robot-next", false, "Output only the top pick recommendation as JSON (minimal triage)")
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
	labelScope := flag.String("label", "", "Scope analysis to label's subgraph (affects --robot-insights, --robot-plan, --robot-priority)")
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
	relatedMinRelevance := flag.Int("related-min-relevance", 20, "Minimum relevance score (0-100) for related work")
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
	// Override pflag's default usage so -h/--help prints our custom header.
	flag.Usage = func() {
		fmt.Println("Usage: bv [options]")
		fmt.Println("\nA TUI viewer for beads issue tracker.")
		flag.PrintDefaults()
	}
	flag.Parse()

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

	// Detect the active workspace stack once so helper commands can emit the
	// right CLI (`bd` for modern beads, `br` for legacy/file-first stacks).
	configureCommandBackend("")

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
		*robotSprintList ||
		*robotSprintShow != "" ||
		*robotForecast != "" ||
		*robotBurndown != "" ||
		*robotByLabel != "" ||
		*robotByAssignee != "" ||
		*robotCapacity ||
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

	if *help {
		fmt.Println("Usage: bv [options]")
		fmt.Println("\nA TUI viewer for beads issue tracker.")
		flag.PrintDefaults()
		os.Exit(0)
	}

	if *robotHelp {
		fmt.Println("bv (Beads Viewer) AI Agent Interface")
		fmt.Println("====================================")
		fmt.Println("This tool provides structural analysis of the issue tracker graph (DAG).")
		fmt.Println("Use these commands to understand project state without parsing raw JSONL.")
		fmt.Println("")
		fmt.Println("Output format:")
		fmt.Println("  --format json|toon")
		fmt.Println("      Structured output encoding for --robot-* commands (default: json).")
		fmt.Println("      Env: BV_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT.")
		fmt.Println("  --stats")
		fmt.Println("      Print JSON vs TOON token estimates to stderr (or set TOON_STATS=1).")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  --robot-plan")
		fmt.Println("      Outputs a dependency-respecting execution plan as JSON.")
		fmt.Println("      Shows what can be worked on now and what it unblocks.")
		fmt.Println("      Key fields:")
		fmt.Println("      - tracks: Independent work streams that can be parallelized")
		fmt.Println("      - items: Actionable issues sorted by priority within each track")
		fmt.Println("      - unblocks: Issues that become actionable when this item is done")
		fmt.Println("      - summary: Highlights highest-impact item to work on first")
		fmt.Println("")
		fmt.Println("  --robot-insights")
		fmt.Println("      Outputs a JSON object containing deep graph analysis.")
		fmt.Println("      Key metrics explained:")
		fmt.Println("      - PageRank: Measures 'blocking power'. High score = Fundamental dependency.")
		fmt.Println("      - Betweenness: Measures 'bottleneck status'. High score = Connects disparate clusters.")
		fmt.Println("      - CriticalPathScore: Heuristic for depth. High score = Blocking a long chain of work.")
		fmt.Println("      - Hubs/Authorities: HITS algorithm scores for dependency relationships.")
		fmt.Println("      - Cycles: Lists of circular dependencies (unhealthy state).")
		fmt.Println("")
		fmt.Println("  --robot-priority")
		fmt.Println("      Outputs priority recommendations as JSON.")
		fmt.Println("      Compares impact scores to current priorities and suggests adjustments.")
		fmt.Println("      Key fields:")
		fmt.Println("      - recommendations: Sorted by confidence, then impact score")
		fmt.Println("      - confidence: 0-1 score indicating strength of recommendation")
		fmt.Println("      - reasoning: Human-readable explanations for the suggestion")
		fmt.Println("      - direction: 'increase' or 'decrease' priority")
		fmt.Println("")
		fmt.Println("  --robot-triage")
		fmt.Println("      THE MEGA-COMMAND: Unified triage output combining all analysis.")
		fmt.Println("      Single entry point for AI agents - one call gets everything needed.")
		fmt.Println("      Key sections:")
		fmt.Println("      - meta: Generation timestamp, data stats")
		fmt.Println("      - quick_ref: At-a-glance summary (open/actionable/blocked counts, top 3 picks)")
		fmt.Println("      - recommendations: Ranked actionable items with scores and reasoning")
		fmt.Println("      - quick_wins: Low-complexity, high-impact items")
		fmt.Println("      - blockers_to_clear: Items that unblock the most downstream work")
		fmt.Println("      - project_health: Counts, graph metrics, overall status")
		fmt.Println("      - commands: Copy-paste commands for common next steps")
		fmt.Println("")
		fmt.Println("  --robot-next")
		fmt.Println("      Minimal triage: returns only the single top recommendation.")
		fmt.Println("      Output includes: id, title, score, reasons, claim_command, show_command")
		fmt.Println("      Use when you just need to know \"what should I work on next?\"")
		fmt.Println("")
		fmt.Println("  --search \"query\" [--robot-search]")
		fmt.Println("      Semantic vector search over issue titles/descriptions.")
		fmt.Println("      Builds/updates a local on-disk vector index on first run.")
		fmt.Println("      Use --robot-search to emit JSON for automation.")
		fmt.Println("      Optional hybrid re-ranking:")
		fmt.Println("      - --search-mode=text|hybrid (default: BV_SEARCH_MODE or text)")
		fmt.Println("      - --search-preset=default|bug-hunting|sprint-planning|impact-first|text-only")
		fmt.Println("      - --search-weights='{\"text\":0.4,\"pagerank\":0.2,\"status\":0.15,\"impact\":0.1,\"priority\":0.1,\"recency\":0.05}'")
		fmt.Println("")
		fmt.Println("  --emit-script [--script-limit=N]")
		fmt.Println("      Emits a shell script for top-N recommendations (default: 5).")
		fmt.Println("      Includes hash/config header for deterministic ordering.")
		fmt.Printf("      Output: %s show commands for each item, commented claim commands\n", beadscli.Tool())
		fmt.Println("      Options: --script-format=bash|fish|zsh, --script-limit=N")
		fmt.Println("      Example: bv --emit-script > work.sh && bash work.sh")
		fmt.Println("      Example: bv --emit-script --script-limit=3")
		fmt.Println("")
		fmt.Println("  --robot-history")
		fmt.Println("      Outputs bead-to-commit correlations as JSON.")
		fmt.Println("      Tracks which code changes relate to which beads via git history analysis.")
		fmt.Println("      Key sections:")
		fmt.Println("      - stats: Summary (total beads, beads with commits, avg cycle time)")
		fmt.Println("      - histories: Per-bead data (events, commits, milestones, cycle_time)")
		fmt.Println("      - commit_index: Reverse lookup from commit SHA to bead IDs")
		fmt.Println("      Flags:")
		fmt.Println("      - --bead-history <id>: Filter to single bead")
		fmt.Println("      - --history-since <ref>: Limit to recent commits")
		fmt.Println("      - --history-limit <n>: Max commits to analyze (default: 500)")
		fmt.Println("      - --min-confidence <0.0-1.0>: Filter by minimum confidence score")
		fmt.Println("      Example: bv --robot-history --history-since '30 days ago'")
		fmt.Println("      Example: bv --robot-history --min-confidence 0.7")
		fmt.Println("")
		fmt.Println("  --robot-file-beads <path>")
		fmt.Println("      Outputs beads that have touched a file path as JSON.")
		fmt.Println("      Answers: 'What beads have touched this file, and why?'")
		fmt.Println("      Key sections:")
		fmt.Println("      - file_path: The queried file path")
		fmt.Println("      - open_beads: Currently open beads that touched this file")
		fmt.Println("      - closed_beads: Recently closed beads (limited by --file-beads-limit)")
		fmt.Println("      - total_beads: Total count of beads")
		fmt.Println("      Each bead includes: bead_id, title, status, commit_shas, last_touch, total_changes")
		fmt.Println("      Flags:")
		fmt.Println("      - --file-beads-limit <n>: Max closed beads to show (default: 20)")
		fmt.Println("      Example: bv --robot-file-beads pkg/auth/token.go")
		fmt.Println("      Example: bv --robot-file-beads pkg/auth")
		fmt.Println("")
		fmt.Println("  --robot-file-hotspots")
		fmt.Println("      Outputs files touched by the most beads as JSON.")
		fmt.Println("      Identifies potential conflict zones or high-churn areas.")
		fmt.Println("      Key sections:")
		fmt.Println("      - hotspots: Array of {file_path, total_beads, open_beads, closed_beads}")
		fmt.Println("      - stats: Index statistics (total_files, total_bead_links)")
		fmt.Println("      Flags:")
		fmt.Println("      - --hotspots-limit <n>: Max hotspots to show (default: 10)")
		fmt.Println("      Example: bv --robot-file-hotspots")
		fmt.Println("")
		fmt.Println("  --robot-impact <files>")
		fmt.Println("      Analyzes impact of modifying files - what beads might be affected?")
		fmt.Println("      Critical for agents: check before making changes to avoid conflicts.")
		fmt.Println("      Key sections:")
		fmt.Println("      - risk_level: low/medium/high/critical based on open beads")
		fmt.Println("      - affected_beads: Beads touching these files with relevance scores")
		fmt.Println("      - warnings: Actionable warnings about potential conflicts")
		fmt.Println("      - summary: Human-readable impact summary")
		fmt.Println("      Input: Comma-separated file paths")
		fmt.Println("      Example: bv --robot-impact pkg/auth/token.go")
		fmt.Println("      Example: bv --robot-impact pkg/auth/token.go,pkg/auth/session.go")
		fmt.Println("")
		fmt.Println("  --robot-file-relations <path>")
		fmt.Println("      Outputs files that frequently co-change with the given file.")
		fmt.Println("      Reveals hidden coupling: what other files typically change together?")
		fmt.Println("      Key fields:")
		fmt.Println("      - total_commits: How many commits touched this file")
		fmt.Println("      - related_files: Files that co-change, with correlation scores")
		fmt.Println("        - correlation: 0.0-1.0 (e.g., 0.8 = changed together in 80% of commits)")
		fmt.Println("        - sample_commits: Example commit SHAs showing co-change")
		fmt.Println("      Options:")
		fmt.Println("      - --relations-threshold <0.0-1.0>: Min correlation (default 0.5)")
		fmt.Println("      - --relations-limit <n>: Max related files to return (default 10)")
		fmt.Println("      Example: bv --robot-file-relations pkg/auth/token.go")
		fmt.Println("      Example: bv --robot-file-relations pkg/auth/token.go --relations-threshold 0.3")
		fmt.Println("")
		fmt.Println("  --robot-related <bead-id>")
		fmt.Println("      Outputs beads related to a specific bead as JSON.")
		fmt.Println("      Discovers related work across multiple dimensions:")
		fmt.Println("      - file_overlap: Beads touching the same files")
		fmt.Println("      - commit_overlap: Beads sharing commits")
		fmt.Println("      - dependency_cluster: Beads in the same dependency graph area")
		fmt.Println("      - concurrent: Beads active in overlapping time windows")
		fmt.Println("      Key fields per related bead:")
		fmt.Println("      - relevance: 0-100 score indicating strength of relationship")
		fmt.Println("      - reason: Human-readable explanation of the connection")
		fmt.Println("      - shared_files/shared_commits: Evidence of overlap")
		fmt.Println("      Options:")
		fmt.Println("      - --related-min-relevance <0-100>: Min relevance score (default 20)")
		fmt.Println("      - --related-max-results <n>: Max results per category (default 10)")
		fmt.Println("      - --related-include-closed: Include closed beads")
		fmt.Println("      Example: bv --robot-related bv-abc1")
		fmt.Println("      Example: bv --robot-related bv-abc1 --related-include-closed")
		fmt.Println("")
		fmt.Println("  --robot-sprint-list")
		fmt.Println("      Outputs all sprints as JSON for planning and forecasting.")
		fmt.Println("      Key fields:")
		fmt.Println("      - generated_at: Timestamp of the output")
		fmt.Println("      - sprint_count: Number of sprints")
		fmt.Println("      - sprints: Array of sprint objects (id, name, start_date, end_date, bead_ids)")
		fmt.Println("      Example: bv --robot-sprint-list")
		fmt.Println("")
		fmt.Println("  --robot-sprint-show <id>")
		fmt.Println("      Outputs details for a specific sprint as JSON.")
		fmt.Println("      Returns the full sprint object with all fields.")
		fmt.Println("      Example: bv --robot-sprint-show sprint-1")
		fmt.Println("")
		fmt.Println("  --robot-burndown <id|current>")
		fmt.Println("      Outputs burndown data for a sprint as JSON.")
		fmt.Println("      Use 'current' to get the active sprint, or specify sprint ID.")
		fmt.Println("      Key fields:")
		fmt.Println("      - total_days, elapsed_days, remaining_days")
		fmt.Println("      - total_issues, completed_issues, remaining_issues")
		fmt.Println("      - ideal_burn_rate, actual_burn_rate")
		fmt.Println("      - projected_complete: Estimated completion date")
		fmt.Println("      - on_track: Whether sprint will complete on time")
		fmt.Println("      - daily_points: Actual burndown data points")
		fmt.Println("      - ideal_line: Expected burndown line")
		fmt.Println("      Example: bv --robot-burndown current")
		fmt.Println("      Example: bv --robot-burndown sprint-1")
		fmt.Println("")
		fmt.Println("  --robot-forecast <id|all>")
		fmt.Println("      Outputs ETA forecast for a specific bead or all open issues.")
		fmt.Println("      Returns estimated completion date, confidence, and factors.")
		fmt.Println("      Options:")
		fmt.Println("        --forecast-label=X    Filter by label")
		fmt.Println("        --forecast-sprint=Y   Filter by sprint")
		fmt.Println("        --forecast-agents=N   Parallel agents (default: 1)")
		fmt.Println("      Example: bv --robot-forecast bv-123")
		fmt.Println("      Example: bv --robot-forecast all --forecast-label=backend")
		fmt.Println("      Example: bv --robot-forecast all --forecast-agents=2")
		fmt.Println("")
		fmt.Println("  --robot-capacity [--agents=N] [--capacity-label=X]")
		fmt.Println("      Outputs capacity simulation and completion projection as JSON.")
		fmt.Println("      Analyzes work remaining, parallelizability, and bottlenecks.")
		fmt.Println("      Key fields:")
		fmt.Println("        - total_minutes: Sum of estimated work across open issues")
		fmt.Println("        - parallelizable_pct: Percentage that can be parallelized")
		fmt.Println("        - serial_minutes: Work that must be done sequentially")
		fmt.Println("        - estimated_days: Projected completion time with N agents")
		fmt.Println("        - bottlenecks: Issues limiting parallelization")
		fmt.Println("      Options:")
		fmt.Println("        --agents=N           Number of parallel agents (default: 1)")
		fmt.Println("        --capacity-label=X   Filter analysis to label's subgraph")
		fmt.Println("      Example: bv --robot-capacity --agents=3")
		fmt.Println("      Example: bv --robot-capacity --capacity-label=backend")
		fmt.Println("")
		fmt.Println("  --emit-script [--script-limit=N] [--script-format=bash|fish|zsh]")
		fmt.Println("      Emits a shell script for top-N priority recommendations.")
		fmt.Println("      Useful for agent workflows and automation.")
		fmt.Println("      Output includes:")
		fmt.Println("        - Header comment with data hash and generation time")
		fmt.Printf("        - %s show commands for each recommended item\n", beadscli.Tool())
		fmt.Printf("        - Commented %s update commands to claim items\n", beadscli.Tool())
		fmt.Println("      Options:")
		fmt.Println("        --script-limit=N      Number of items (default: 5)")
		fmt.Println("        --script-format=X     Script format: bash, fish, zsh")
		fmt.Println("      Example: bv --emit-script")
		fmt.Println("      Example: bv --emit-script --script-limit=3")
		fmt.Println("      Example: bv --emit-script --script-format=fish > work.fish")
		fmt.Println("      Example: bv --emit-script | bash  # Show top 5 items")
		fmt.Println("")
		fmt.Println("  --export-md <file>")
		fmt.Println("      Generates a readable status report with Mermaid.js visualizations.")
		fmt.Println("      Runs pre-export and post-export hooks if configured in .bv/hooks.yaml")
		fmt.Println("")
		fmt.Println("  --no-hooks")
		fmt.Println("      Skip running hooks during export. Useful for CI or quick exports.")
		fmt.Println("")
		fmt.Println("  Hook Configuration (.bv/hooks.yaml)")
		fmt.Println("      Configure hooks to automate export workflows:")
		fmt.Println("      - pre-export: Validation, notifications (failure cancels export)")
		fmt.Println("      - post-export: Notifications, uploads (failure logged only)")
		fmt.Println("      Environment variables: BV_EXPORT_PATH, BV_EXPORT_FORMAT,")
		fmt.Println("        BV_ISSUE_COUNT, BV_TIMESTAMP")
		fmt.Println("")
		fmt.Println("  --diff-since <commit|date>")
		fmt.Println("      Shows changes since a historical point.")
		fmt.Println("      Accepts: SHA, branch name, tag, HEAD~N, or date (YYYY-MM-DD)")
		fmt.Println("      Key output:")
		fmt.Println("      - new_issues: Issues added since then")
		fmt.Println("      - closed_issues: Issues that were closed")
		fmt.Println("      - removed_issues: Issues deleted from tracker")
		fmt.Println("      - modified_issues: Issues with field changes")
		fmt.Println("      - new_cycles: Circular dependencies introduced")
		fmt.Println("      - resolved_cycles: Circular dependencies fixed")
		fmt.Println("      - summary.health_trend: 'improving', 'degrading', or 'stable'")
		fmt.Println("")
		fmt.Println("  --as-of <commit|date>")
		fmt.Println("      View issue state at a point in time (works with all robot commands).")
		fmt.Println("      Useful for historical analysis without modifying the working tree.")
		fmt.Println("      Robot outputs include 'as_of' and 'as_of_commit' metadata fields.")
		fmt.Println("      Examples: --as-of HEAD~30, --as-of v1.0.0, --as-of '2024-01-01'")
		fmt.Println("")
		fmt.Println("  --robot-diff")
		fmt.Println("      Output diff as JSON (use with --diff-since).")
		fmt.Println("      Fields: generated_at, resolved_revision, from_data_hash, to_data_hash, diff{...}")
		fmt.Println("      Diff payload includes metric deltas, cycles introduced/resolved, and modified issues.")
		fmt.Println("")
		fmt.Println("  --robot-recipes")
		fmt.Println("      Lists all available recipes as JSON.")
		fmt.Println("      Output: {recipes: [{name, description, source}]}")
		fmt.Println("      Sources: 'builtin', 'user' (~/.config/bv/recipes.yaml), 'project' (.bv/recipes.yaml)")
		fmt.Println("")
		fmt.Println("  --robot-schema [--schema-command=NAME]")
		fmt.Println("      Outputs JSON Schema definitions for all robot command outputs.")
		fmt.Println("      Useful for validation, tool integration, and understanding output structure.")
		fmt.Println("      Key fields:")
		fmt.Println("        - schema_version: Version of the schema format")
		fmt.Println("        - envelope: Common fields present in all robot outputs")
		fmt.Println("        - commands: Map of command name -> JSON Schema definition")
		fmt.Println("      Options:")
		fmt.Println("        --schema-command=NAME: Output schema for specific command only")
		fmt.Println("      Example: bv --robot-schema")
		fmt.Println("      Example: bv --robot-schema --schema-command=robot-triage")
		fmt.Println("")
		fmt.Println("  --robot-label-health")
		fmt.Println("      Outputs label health metrics as JSON (velocity, freshness, flow, criticality).")
		fmt.Println("      Includes label summaries, detailed metrics, and cross-label dependencies.")
		fmt.Println("      Key fields: health_level (healthy|warning|critical), velocity_score, flow_score.")
		fmt.Println("")
		fmt.Println("  --robot-label-flow")
		fmt.Println("      Outputs cross-label dependency flow as JSON (label->label edges).")
		fmt.Println("      Key fields: labels[], flow_matrix[from][to], dependencies[{from,to,count,issue_ids}],")
		fmt.Println("                  bottleneck_labels (highest outgoing), total_cross_label_deps.")
		fmt.Println("      Use when you need to see which labels are blocking others at a glance.")
		fmt.Println("")
		fmt.Println("  --robot-label-attention [--attention-limit=N]")
		fmt.Println("      Outputs attention-ranked labels as JSON (default limit: 5).")
		fmt.Println("      Labels ranked by attention score = (pagerank * staleness * block_impact) / velocity.")
		fmt.Println("      Key fields: rank, label, attention_score, normalized_score, reason, blocked_count, stale_count.")
		fmt.Println("      Use to identify which labels need the most focus based on centrality and health factors.")
		fmt.Println("")
		fmt.Println("  --robot-alerts")
		fmt.Println("      Outputs drift + proactive alerts as JSON (staleness, cascades, density, cycles).")
		fmt.Println("      Filters: --severity=<info|warning|critical>, --alert-type=<type>, --alert-label=<label>")
		fmt.Println("      Fields: type, severity, message, issue_id, label, detected_at, details[].")
		fmt.Println("")
		fmt.Println("  --robot-graph [--graph-format=json|dot|mermaid] [--graph-root=ID] [--graph-depth=N]")
		fmt.Println("      Outputs dependency graph in specified format (default: JSON adjacency).")
		fmt.Println("      Formats:")
		fmt.Println("        - json: Adjacency list with nodes[], edges[], metadata")
		fmt.Println("        - dot: Graphviz DOT format (render with: dot -Tpng file.dot -o graph.png)")
		fmt.Println("        - mermaid: Mermaid diagram format (paste into GitHub/markdown)")
		fmt.Println("      Options:")
		fmt.Println("        --label LABEL: Filter to issues with specific label")
		fmt.Println("        --graph-root ID: Extract subgraph starting from root issue")
		fmt.Println("        --graph-depth N: Limit subgraph depth (0 = unlimited)")
		fmt.Println("      Fields: format, graph (string for dot/mermaid), nodes, edges, filters_applied, explanation")
		fmt.Println("      Example: bv --robot-graph --graph-format=dot --label=api > api-deps.dot")
		fmt.Println("")
		fmt.Println("  --export-graph <path.png|path.svg> [--graph-style=force|grid] [--graph-preset=compact|roomy]")
		fmt.Println("      Export dependency graph as PNG or SVG image (pure Go, no external dependencies).")
		fmt.Println("      Format is inferred from file extension (.png or .svg).")
		fmt.Println("")
		fmt.Println("      Styles:")
		fmt.Println("        --graph-style=force (default): Beautiful force-directed layout")
		fmt.Println("          - Physics-based positioning (Fruchterman-Reingold algorithm)")
		fmt.Println("          - Dark Dracula-inspired theme with vibrant status colors")
		fmt.Println("          - Curved bezier edges with glow effects")
		fmt.Println("          - Node size reflects importance (PageRank + betweenness)")
		fmt.Println("          - Priority badges, drop shadows, gradients")
		fmt.Println("")
		fmt.Println("        --graph-style=grid: Simple hierarchical grid layout")
		fmt.Println("          - Nodes arranged by critical path depth")
		fmt.Println("          - Light theme with pastel colors")
		fmt.Println("")
		fmt.Println("      Options:")
		fmt.Println("        --label LABEL: Filter to issues with specific label")
		fmt.Println("        --graph-preset: Layout spacing - 'compact' (default) or 'roomy'")
		fmt.Println("        --graph-title: Custom title for the graph header")
		fmt.Println("")
		fmt.Println("      Example: bv --export-graph deps.svg --label=api --graph-title='API Dependencies'")
		fmt.Println("      Example: bv --export-graph full.png --graph-style=force --graph-preset=roomy")
		fmt.Println("")
		fmt.Println("  --robot-insights")
		fmt.Println("      Graph metrics JSON for agents.")
		fmt.Println("      Top lists: Bottlenecks (betweenness), Keystones (critical path), Influencers (eigenvector),")
		fmt.Println("                 Cores (k-core), Articulation points (cut vertices), Slack (parallelism headroom).")
		fmt.Println("      Full maps (capped by BV_INSIGHTS_MAP_LIMIT): pagerank, betweenness, eigenvector, hubs/authorities, core_number, slack.")
		fmt.Println("      status captures per-metric state: computed|approx|timeout|skipped with elapsed_ms and reasons.")
		fmt.Println("      Shared fields: data_hash, analysis_config.")
		fmt.Println("      Quick jq: jq '.full_stats.core_number | to_entries | sort_by(-.value)[:5]'   # top k-core nodes")
		fmt.Println("                 jq '.Articulation'                                                  # structural cut points")
		fmt.Println("                 jq '.Slack[:5]'                                                     # highest slack (parallel-friendly)")
		fmt.Println("      advanced_insights: Canonical structure for advanced graph features:")
		fmt.Println("        - topk_set: Best k issues for maximum downstream unlock (status: pending)")
		fmt.Println("        - coverage_set: Minimal set covering all critical paths (status: pending)")
		fmt.Println("        - k_paths: K-shortest critical paths through the graph (status: pending)")
		fmt.Println("        - parallel_cut: Suggestions for maximizing parallel work (status: pending)")
		fmt.Println("        - parallel_gain: Parallelization metrics for recommendations (status: pending)")
		fmt.Println("        - cycle_break: Suggestions for breaking cycles with minimal impact (status: available)")
		fmt.Println("        Per-feature: status (available|pending|skipped|error), items, usage hints")
		fmt.Println("        Config: caps for deterministic output (topk<=5, paths<=5, path_len<=50, etc.)")
		fmt.Println("        Quick jq: jq '.advanced_insights.cycle_break'   # cycle break suggestions")
		fmt.Println("")
		fmt.Println("  --robot-plan")
		fmt.Println("      Execution tracks grouped for parallel work. Includes data_hash, analysis_config, status.")
		fmt.Println("      plan.tracks[].items[].unblocks shows what completes next; summary.highest_impact surfaces best unblocker.")
		fmt.Println("")
		fmt.Println("  --robot-priority")
		fmt.Println("      Priority recommendations with explanations. Includes data_hash, analysis_config, status.")
		fmt.Println("      recommendation fields: id, current_priority, suggested_priority, impact_score, confidence, reasoning[].")
		fmt.Println("      explanation.what_if: impact of completing (direct_unblocks, transitive_unblocks, estimated_days_saved).")
		fmt.Println("      explanation.top_reasons: top 3 factors (pagerank, betweenness, blockers, staleness, etc.).")
		fmt.Println("")
		fmt.Println("  Robot Output Filters (bv-84):")
		fmt.Println("      --robot-min-confidence 0.6    Filter by minimum confidence (0.0-1.0)")
		fmt.Println("      --robot-max-results 5         Limit to top N results")
		fmt.Println("      --robot-by-label bug          Filter by label (exact match)")
		fmt.Println("      --robot-by-assignee alice     Filter by assignee (exact match)")
		fmt.Println("")
		fmt.Println("  Label Subgraph Scoping (bv-122):")
		fmt.Println("      --label LABEL                 Scope analysis to label's subgraph")
		fmt.Println("      Affects: --robot-insights, --robot-plan, --robot-priority")
		fmt.Println("      Filters issues to those with the label, then runs analysis on subgraph.")
		fmt.Println("      Includes label_scope and label_context in output with health metrics.")
		fmt.Println("      Example: bv --robot-insights --label api")
		fmt.Println("")
		fmt.Println("  --robot-triage / --robot-next")
		fmt.Println("      Unified triage (mega command) or single top pick. QuickRef includes top picks, quick_wins, blockers_to_clear.")
		fmt.Println("")
		fmt.Println("  --recipe NAME, -r NAME")
		fmt.Println("      Apply a named recipe to filter and sort issues.")
		fmt.Println("      Example: bv --recipe actionable")
		fmt.Println("      Built-in recipes: default, actionable, recent, blocked, high-impact, stale,")
		fmt.Println("                        triage, closed, release-cut, quick-wins, bottlenecks")
		fmt.Println("")
		fmt.Println("  --profile-startup")
		fmt.Println("      Outputs detailed startup timing profile for diagnostics.")
		fmt.Println("      Shows Phase 1 (blocking) and Phase 2 (async) breakdown.")
		fmt.Println("      Provides recommendations based on timing analysis.")
		fmt.Println("      Use with --profile-json for machine-readable output.")
		fmt.Println("")
		fmt.Println("  --workspace CONFIG")
		fmt.Println("      Load issues from workspace configuration file.")
		fmt.Println("      Path: typically .bv/workspace.yaml")
		fmt.Println("      Aggregates issues from multiple repositories with namespaced IDs.")
		fmt.Println("      Example: bv --workspace .bv/workspace.yaml")
		fmt.Println("")
		fmt.Println("  --repo PREFIX")
		fmt.Println("      Filter issues by repository prefix.")
		fmt.Println("      Use with --workspace to focus on one repo in a multi-repo view.")
		fmt.Println("      Matches ID prefixes like 'api-', 'web-', or partial 'api'.")
		fmt.Println("      Example: bv --workspace .bv/workspace.yaml --repo api")
		fmt.Println("")
		fmt.Println("  --save-baseline \"description\"")
		fmt.Println("      Save current metrics as a baseline snapshot.")
		fmt.Println("      Stores graph stats, top metrics, and cycle info in .bv/baseline.json.")
		fmt.Println("      Use for drift detection: compare current state to saved baseline.")
		fmt.Println("      Example: bv --save-baseline \"Before major refactor\"")
		fmt.Println("")
		fmt.Println("  --baseline-info")
		fmt.Println("      Show information about the saved baseline.")
		fmt.Println("      Displays: creation date, git commit, graph stats, top metrics.")
		fmt.Println("")
		fmt.Println("  --check-drift")
		fmt.Println("      Check current metrics against saved baseline for drift.")
		fmt.Println("      Exit codes for CI integration:")
		fmt.Println("        0 = No critical or warning alerts (info-only OK)")
		fmt.Println("        1 = Critical alerts (new cycles detected)")
		fmt.Println("        2 = Warning alerts (blocked increase, density growth)")
		fmt.Println("      Human-readable output by default, use --robot-drift for JSON.")
		fmt.Println("")
		fmt.Println("  --robot-drift")
		fmt.Println("      Output drift check as JSON (use with --check-drift).")
		fmt.Println("      Output: {has_drift, exit_code, summary, alerts, baseline}")
		fmt.Println("")
		fmt.Println("  Static Site Export & GitHub Pages (bv-7pu):")
		fmt.Println("      --pages")
		fmt.Println("          Launch interactive Pages deployment wizard.")
		fmt.Println("          Guides you through export -> preview -> deploy to GitHub Pages.")
		fmt.Println("          Handles gh CLI authentication and repository creation.")
		fmt.Println("")
		fmt.Println("      --export-pages <dir>")
		fmt.Println("          Export static HTML site to directory.")
		fmt.Println("          Creates self-contained bundle viewable in any browser.")
		fmt.Println("          Output: index.html, beads.sqlite3, data/*.json, viewer assets")
		fmt.Println("          Example: bv --export-pages ./bv-pages")
		fmt.Println("")
		fmt.Println("      --preview-pages <dir>")
		fmt.Println("          Start local server to preview existing export.")
		fmt.Println("          Opens http://localhost:9000 (or next available port) in your browser.")
		fmt.Println("          Live-reload is enabled by default: browser auto-refreshes on file changes.")
		fmt.Println("          Example: bv --preview-pages ./bv-pages")
		fmt.Println("")
		fmt.Println("      --no-live-reload")
		fmt.Println("          Disable live-reload for --preview-pages (default: live-reload is enabled).")
		fmt.Println("")
		fmt.Println("      --pages-title <title>")
		fmt.Println("          Custom title for the static site (default: 'Project Issues')")
		fmt.Println("")
		fmt.Println("      --pages-include-closed=false")
		fmt.Println("          Exclude closed issues from export (default: include all)")
		fmt.Println("")
		fmt.Println("  Database Path Configuration:")
		fmt.Println("      --db <path>")
		fmt.Println("          Specify path to beads database file or .beads directory.")
		fmt.Println("          Accepts a .beads/ directory, a beads.jsonl file, or a beads.db file.")
		fmt.Println("          Overrides all environment variables.")
		fmt.Println("          Example: bv --db /path/to/repo/.beads --robot-triage")
		fmt.Println("          Example: bv --db /path/to/.beads/beads.jsonl --robot-next")
		fmt.Println("")
		fmt.Println("      Environment variables (in priority order):")
		fmt.Println("        BEADS_DB   - Path to beads database file or .beads directory")
		fmt.Println("        BEADS_DIR  - Path to .beads directory (existing, lower priority)")
		fmt.Println("")
		fmt.Println("      Resolution priority: --db flag > BEADS_DB env > BEADS_DIR env > auto-discovery")
		fmt.Println("")
		fmt.Println("  Drift Detection Configuration (.bv/drift.yaml)")
		fmt.Println("      Customize drift detection thresholds:")
		fmt.Println("      - density_warning_pct: 50    # Warn if density +50%")
		fmt.Println("      - blocked_increase_threshold: 5   # Warn if 5+ more blocked")
		fmt.Println("      Run 'bv --baseline-info' to see current baseline state.")
		os.Exit(0)
	}

	if *versionFlag {
		fmt.Printf("bv %s\n", version.Version)
		os.Exit(0)
	}

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

	// Handle --update (bv-182)
	if *updateFlag {
		release, err := updater.GetLatestRelease()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching release info: %v\n", err)
			os.Exit(1)
		}

		// Check if update is needed
		available, newVersion, _, _ := updater.CheckUpdateAvailable()
		if !available {
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
			issues, _, err := loadIssuesForCLI("", true)
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
	recipeLoader, err := recipe.LoadDefault()
	if err != nil {
		if !envRobot {
			fmt.Fprintf(os.Stderr, "Warning: Error loading recipes: %v\n", err)
		}
		// Create empty loader to continue
		recipeLoader = recipe.NewLoader()
	}

	// Handle --robot-recipes (before loading issues)
	if *robotRecipes {
		summaries := recipeLoader.ListSummaries()
		// Sort by name for consistent output
		sort.Slice(summaries, func(i, j int) bool {
			return summaries[i].Name < summaries[j].Name
		})

		output := struct {
			Recipes []recipe.RecipeSummary `json:"recipes"`
		}{
			Recipes: summaries,
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding recipes: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Handle --robot-schema (bd-2kxo)
	if *robotSchema {
		schemas := generateRobotSchemas()

		// Filter to specific command if requested
		if *schemaCommand != "" {
			if schema, ok := schemas.Commands[*schemaCommand]; ok {
				singleOutput := map[string]interface{}{
					"schema_version": schemas.SchemaVersion,
					"generated_at":   schemas.GeneratedAt,
					"command":        *schemaCommand,
					"schema":         schema,
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(singleOutput); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding schema: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n", *schemaCommand)
			fmt.Fprintln(os.Stderr, "Available commands:")
			for cmd := range schemas.Commands {
				fmt.Fprintf(os.Stderr, "  %s\n", cmd)
			}
			os.Exit(1)
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(schemas); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding schemas: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Machine-readable robot docs (bd-2v50)
	if *robotDocs != "" {
		docs := generateRobotDocs(*robotDocs)
		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(docs); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding robot-docs: %v\n", err)
			os.Exit(1)
		}
		if _, hasErr := docs["error"]; hasErr {
			os.Exit(2) // Invalid arguments per documented exit codes
		}
		os.Exit(0)
	}

	// Get project directory for baseline operations (moved up to allow info check without loading issues)
	projectDir, _ := os.Getwd()
	baselinePath := baseline.DefaultPath(projectDir)

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
		issues, beadsPath, err = loadIssuesForCLI("", true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
			fmt.Fprintln(os.Stderr, "Make sure you are in a project initialized with 'bd init' or 'br init'.")
			os.Exit(1)
		}
		// Get beads file path for live reload (respects BEADS_DIR env var)
		resolution, err := resolveWorkspaceForCLI("", false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving beads workspace: %v\n", err)
			os.Exit(1)
		}
		beadsDir := resolution.BeadsDir

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
	}

	// Handle semantic search CLI (bv-9gf.3)
	if *robotSearch && *semanticQuery == "" {
		fmt.Fprintln(os.Stderr, "Error: --robot-search requires --search \"query\"")
		os.Exit(1)
	}
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
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DataHash:    dataHash,
				Query:       *semanticQuery,
				Provider:    embedCfg.Provider,
				Model:       embedCfg.Model,
				Dim:         embedder.Dim(),
				IndexPath:   indexPath,
				Index:       syncStats,
				Loaded:      loaded,
				Limit:       limit,
				Mode:        searchCfg.Mode,
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

			if err := writeRobotSearchOutput(os.Stdout, out); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding robot-search: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
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
			cwd, _ := os.Getwd()
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

			// Collect all issues.jsonl files to watch
			var watchFiles []string
			var watchers []*watcher.Watcher

			if *workspaceConfig != "" {
				// Workspace mode: watch all repos' issues.jsonl files (bv-79)
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
					issuesFile, err := loader.FindJSONLPath(beadsDir)
					if err != nil {
						fmt.Printf("  → Warning: could not find issues.jsonl for repo %s: %v\n", repo.GetName(), err)
						continue
					}
					watchFiles = append(watchFiles, issuesFile)
				}

				if len(watchFiles) == 0 {
					fmt.Fprintf(os.Stderr, "Error: no valid issues.jsonl files found in workspace\n")
					os.Exit(1)
				}
			} else {
				// Single-repo mode: watch current directory's issues.jsonl
				cwd, _ := os.Getwd()
				issuesFile := filepath.Join(cwd, ".beads", "issues.jsonl")
				watchFiles = append(watchFiles, issuesFile)
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

			// Watch loop
			for {
				select {
				case <-mergedChangeCh:
					// Reload issues from disk using appropriate method
					var freshIssues []model.Issue
					var err error
					if *workspaceConfig != "" {
						freshIssues, _, err = workspace.LoadAllFromConfig(context.Background(), *workspaceConfig)
					} else {
						freshIssues, _, err = loadIssuesForCLI("", false)
					}
					if err != nil {
						fmt.Printf("  → Error reloading issues: %v\n", err)
						continue
					}
					if err := doExport(freshIssues); err != nil {
						fmt.Printf("  → Export error: %v\n", err)
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
	if *robotGraph {
		analyzer := analysis.NewAnalyzer(issues)
		stats := analyzer.Analyze()

		// Determine format
		var format export.GraphExportFormat
		switch strings.ToLower(*graphFormat) {
		case "dot":
			format = export.GraphFormatDOT
		case "mermaid":
			format = export.GraphFormatMermaid
		default:
			format = export.GraphFormatJSON
		}

		config := export.GraphExportConfig{
			Format:   format,
			Label:    *labelScope,
			Root:     *graphRoot,
			Depth:    *graphDepth,
			DataHash: dataHash,
		}

		result, err := export.ExportGraph(issues, &stats, config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error exporting graph: %v\n", err)
			os.Exit(1)
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding graph: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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
		cwd, _ := os.Getwd()
		projectName := filepath.Base(cwd)

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
	if *robotAlerts {
		driftConfig, err := drift.LoadConfig(projectDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading drift config: %v\n", err)
			os.Exit(1)
		}

		analyzer := analysis.NewAnalyzer(issues)
		stats := analyzer.Analyze()

		openCount, closedCount, blockedCount := 0, 0, 0
		for _, issue := range issues {
			switch issue.Status {
			case model.StatusClosed:
				closedCount++
			case model.StatusBlocked:
				blockedCount++
			case model.StatusOpen, model.StatusInProgress:
				openCount++
			default:
				// Ignore tombstones and any unknown statuses for summary counts.
			}
		}
		actionableCount := len(analyzer.GetActionableIssues())
		cycles := stats.Cycles()
		curStats := baseline.GraphStats{
			NodeCount:       stats.NodeCount,
			EdgeCount:       stats.EdgeCount,
			Density:         stats.Density,
			OpenCount:       openCount,
			ClosedCount:     closedCount,
			BlockedCount:    blockedCount,
			CycleCount:      len(cycles),
			ActionableCount: actionableCount,
		}

		// Default behavior (no baseline): drift comparisons are suppressed by using
		// baseline=current for stats, while still allowing cycle/staleness/cascade alerts.
		bl := &baseline.Baseline{Stats: curStats}
		cur := &baseline.Baseline{Stats: curStats, Cycles: cycles}

		// If a baseline exists, compare against it for real drift deltas.
		if baseline.Exists(baselinePath) {
			loaded, err := baseline.Load(baselinePath)
			if err != nil {
				if !envRobot {
					fmt.Fprintf(os.Stderr, "Warning: Error loading baseline: %v\n", err)
				}
			} else {
				bl = loaded
				topMetrics := baseline.TopMetrics{
					PageRank:     buildMetricItems(stats.PageRank(), 10),
					Betweenness:  buildMetricItems(stats.Betweenness(), 10),
					CriticalPath: buildMetricItems(stats.CriticalPathScore(), 10),
					Hubs:         buildMetricItems(stats.Hubs(), 10),
					Authorities:  buildMetricItems(stats.Authorities(), 10),
				}
				cur = &baseline.Baseline{Stats: curStats, TopMetrics: topMetrics, Cycles: cycles}
			}
		}

		calc := drift.NewCalculator(bl, cur, driftConfig)
		calc.SetIssues(issues)
		driftResult := calc.Calculate()

		// Apply optional filters
		filtered := driftResult.Alerts[:0]
		for _, a := range driftResult.Alerts {
			if *alertSeverity != "" && string(a.Severity) != *alertSeverity {
				continue
			}
			if *alertType != "" && string(a.Type) != *alertType {
				continue
			}
			if *alertLabel != "" {
				found := false
				for _, d := range a.Details {
					if strings.Contains(strings.ToLower(d), strings.ToLower(*alertLabel)) {
						found = true
						break
					}
				}
				if !found && a.Label != "" && !strings.Contains(strings.ToLower(a.Label), strings.ToLower(*alertLabel)) {
					continue
				}
			}
			filtered = append(filtered, a)
		}
		driftResult.Alerts = filtered

		output := struct {
			RobotEnvelope
			Alerts  []drift.Alert `json:"alerts"`
			Summary struct {
				Total    int `json:"total"`
				Critical int `json:"critical"`
				Warning  int `json:"warning"`
				Info     int `json:"info"`
			} `json:"summary"`
			UsageHints []string `json:"usage_hints"`
		}{
			RobotEnvelope: NewRobotEnvelope(dataHash),
			Alerts:        driftResult.Alerts,
			UsageHints: []string{
				"--severity=warning --alert-type=stale_issue   # stale warnings only",
				"--alert-type=blocking_cascade                 # high-unblock opportunities",
				"jq '.alerts | map(.issue_id)'                # list impacted issues",
			},
		}
		for _, a := range driftResult.Alerts {
			switch a.Severity {
			case drift.SeverityCritical:
				output.Summary.Critical++
			case drift.SeverityWarning:
				output.Summary.Warning++
			case drift.SeverityInfo:
				output.Summary.Info++
			}
			output.Summary.Total++
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding alerts: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Handle --robot-suggest (bv-180)
	if *robotSuggest {
		config := analysis.DefaultSuggestAllConfig()
		config.MinConfidence = *suggestConfidence
		config.FilterBead = *suggestBead

		// Parse filter type
		switch *suggestType {
		case "duplicate", "duplicates":
			config.FilterType = analysis.SuggestionPotentialDuplicate
		case "dependency", "dependencies":
			config.FilterType = analysis.SuggestionMissingDependency
		case "label", "labels":
			config.FilterType = analysis.SuggestionLabelSuggestion
		case "cycle", "cycles":
			config.FilterType = analysis.SuggestionCycleWarning
		case "":
			// All types
		default:
			fmt.Fprintf(os.Stderr, "Invalid suggest-type: %s (use: duplicate, dependency, label, cycle)\n", *suggestType)
			os.Exit(1)
		}

		output := analysis.GenerateRobotSuggestOutput(issues, config, dataHash)

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding suggestions: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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
			trim := make(map[string]int, limit)
			count := 0
			for k, v := range m {
				trim[k] = v
				count++
				if count >= limit {
					break
				}
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

	if *robotPlan {
		analyzer := analysis.NewAnalyzer(issues)
		// For --robot-plan we primarily need Phase 1 metrics (degree/topo/density).
		// However, we still emit a stable status contract for agents. If the user
		// explicitly asks for full analysis, honor it; otherwise, skip expensive
		// centrality metrics and record the skip reasons deterministically.
		cfg := analysis.ConfigForSize(len(issues), countEdges(issues))
		if *forceFullAnalysis {
			cfg = analysis.FullAnalysisConfig()
		} else {
			const skipReason = "not computed for --robot-plan"
			cfg.ComputePageRank = false
			cfg.PageRankSkipReason = skipReason
			cfg.ComputeBetweenness = false
			cfg.BetweennessMode = analysis.BetweennessSkip
			cfg.BetweennessSkipReason = skipReason
			cfg.ComputeHITS = false
			cfg.HITSSkipReason = skipReason
			cfg.ComputeEigenvector = false
			cfg.ComputeCriticalPath = false
			cfg.ComputeCycles = false
			cfg.CyclesSkipReason = skipReason
		}

		plan := analyzer.GetExecutionPlan()

		stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), cfg)
		stats.WaitForPhase2()
		status := stats.Status()

		// Wrap with metadata
		output := struct {
			GeneratedAt    string                  `json:"generated_at"`
			DataHash       string                  `json:"data_hash"`
			AsOf           string                  `json:"as_of,omitempty"`        // Historical snapshot ref
			AsOfCommit     string                  `json:"as_of_commit,omitempty"` // Resolved commit SHA
			AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
			Status         analysis.MetricStatus   `json:"status"`
			LabelScope     string                  `json:"label_scope,omitempty"`   // bv-122: Label filter applied
			LabelContext   *analysis.LabelHealth   `json:"label_context,omitempty"` // bv-122: Health context for scoped label
			Plan           analysis.ExecutionPlan  `json:"plan"`
			UsageHints     []string                `json:"usage_hints"` // bv-84: Agent-friendly hints
		}{
			GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
			DataHash:       dataHash,
			AsOf:           *asOf,
			AsOfCommit:     asOfResolved,
			AnalysisConfig: cfg,
			Status:         status,
			LabelScope:     *labelScope,
			LabelContext:   labelScopeContext,
			Plan:           plan,
			UsageHints: []string{
				"jq '.plan.tracks | length' - Number of parallel execution tracks",
				"jq '.plan.tracks[0].items | map(.id)' - First track item IDs",
				"jq '.plan.tracks[].items[] | select(.unblocks | length > 0)' - Items that unblock others",
				"jq '.plan.summary' - High-level execution summary",
				"jq '[.plan.tracks[].items[]] | length' - Total items across all tracks",
			},
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding execution plan: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *robotPriority {
		analyzer := analysis.NewAnalyzer(issues)
		cfg := analysis.ConfigForSize(len(issues), countEdges(issues))
		if *forceFullAnalysis {
			cfg = analysis.FullAnalysisConfig()
		}
		analyzer.SetConfig(&cfg)
		stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), cfg)
		stats.WaitForPhase2()
		status := stats.Status()

		// Use enhanced recommendations with what-if deltas and top reasons (bv-83)
		recommendations := analyzer.GenerateEnhancedRecommendations()

		// Apply robot filters (bv-84)
		filtered := make([]analysis.EnhancedPriorityRecommendation, 0, len(recommendations))
		issueMap := make(map[string]model.Issue, len(issues))
		for _, iss := range issues {
			issueMap[iss.ID] = iss
		}
		for _, rec := range recommendations {
			// Filter by minimum confidence
			if *robotMinConf > 0 && rec.Confidence < *robotMinConf {
				continue
			}
			// Filter by label
			if *robotByLabel != "" {
				if iss, ok := issueMap[rec.IssueID]; ok {
					hasLabel := false
					for _, lbl := range iss.Labels {
						if lbl == *robotByLabel {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						continue
					}
				} else {
					continue
				}
			}
			// Filter by assignee
			if *robotByAssignee != "" {
				if iss, ok := issueMap[rec.IssueID]; ok {
					if iss.Assignee != *robotByAssignee {
						continue
					}
				} else {
					continue
				}
			}
			filtered = append(filtered, rec)
		}
		recommendations = filtered

		// Apply max results limit
		maxResults := 10 // Default cap
		if *robotMaxResults > 0 {
			maxResults = *robotMaxResults
		}
		if len(recommendations) > maxResults {
			recommendations = recommendations[:maxResults]
		}

		// Count high confidence recommendations
		highConfidence := 0
		for _, rec := range recommendations {
			if rec.Confidence >= 0.7 {
				highConfidence++
			}
		}

		// Build output with summary
		output := struct {
			GeneratedAt       string                                    `json:"generated_at"`
			DataHash          string                                    `json:"data_hash"`
			AsOf              string                                    `json:"as_of,omitempty"`        // Historical snapshot ref
			AsOfCommit        string                                    `json:"as_of_commit,omitempty"` // Resolved commit SHA
			AnalysisConfig    analysis.AnalysisConfig                   `json:"analysis_config"`
			Status            analysis.MetricStatus                     `json:"status"`
			LabelScope        string                                    `json:"label_scope,omitempty"`   // bv-122: Label filter applied
			LabelContext      *analysis.LabelHealth                     `json:"label_context,omitempty"` // bv-122: Health context for scoped label
			Recommendations   []analysis.EnhancedPriorityRecommendation `json:"recommendations"`
			FieldDescriptions map[string]string                         `json:"field_descriptions"`
			Filters           struct {
				MinConfidence float64 `json:"min_confidence,omitempty"`
				MaxResults    int     `json:"max_results"`
				ByLabel       string  `json:"by_label,omitempty"`
				ByAssignee    string  `json:"by_assignee,omitempty"`
			} `json:"filters"`
			Summary struct {
				TotalIssues     int `json:"total_issues"`
				Recommendations int `json:"recommendations"`
				HighConfidence  int `json:"high_confidence"`
			} `json:"summary"`
			Usage []string `json:"usage_hints"` // bv-84: Agent-friendly hints
		}{
			GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
			DataHash:          dataHash,
			AsOf:              *asOf,
			AsOfCommit:        asOfResolved,
			AnalysisConfig:    cfg,
			Status:            status,
			LabelScope:        *labelScope,
			LabelContext:      labelScopeContext,
			Recommendations:   recommendations,
			FieldDescriptions: analysis.DefaultFieldDescriptions(),
			Usage: []string{
				"jq '.recommendations[] | select(.confidence > 0.7)' - Filter high confidence",
				"jq '.recommendations[0].explanation.what_if' - Get top item's impact",
				"jq '.recommendations | map({id: .issue_id, score: .impact_score})' - Extract IDs and scores",
				"jq '.recommendations[] | select(.explanation.what_if.parallelization_gain > 0)' - Find items that increase parallel work capacity",
				"--robot-min-confidence 0.6 - Pre-filter by confidence",
				"--robot-max-results 5 - Limit to top N results",
				"--robot-by-label bug - Filter by specific label",
			},
		}
		output.Filters.MinConfidence = *robotMinConf
		output.Filters.MaxResults = maxResults
		output.Filters.ByLabel = *robotByLabel
		output.Filters.ByAssignee = *robotByAssignee
		output.Summary.TotalIssues = len(issues)
		output.Summary.Recommendations = len(recommendations)
		output.Summary.HighConfidence = highConfidence

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding priority recommendations: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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
			// Minimal output: just the top pick
			envelope := NewRobotEnvelope(dataHash)
			if len(triage.QuickRef.TopPicks) == 0 {
				output := struct {
					RobotEnvelope
					AsOf       string `json:"as_of,omitempty"`
					AsOfCommit string `json:"as_of_commit,omitempty"`
					Message    string `json:"message"`
				}{
					RobotEnvelope: envelope,
					AsOf:          *asOf,
					AsOfCommit:    asOfResolved,
					Message:       "No actionable items available",
				}
				encoder := newRobotEncoder(os.Stdout)
				if err := encoder.Encode(output); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding robot-next: %v\n", err)
					os.Exit(1)
				}
				os.Exit(0)
			}

			top := triage.QuickRef.TopPicks[0]
			output := struct {
				RobotEnvelope
				AsOf       string   `json:"as_of,omitempty"`
				AsOfCommit string   `json:"as_of_commit,omitempty"`
				ID         string   `json:"id"`
				Title      string   `json:"title"`
				Score      float64  `json:"score"`
				Reasons    []string `json:"reasons"`
				Unblocks   int      `json:"unblocks"`
				ClaimCmd   string   `json:"claim_command"`
				ShowCmd    string   `json:"show_command"`
			}{
				RobotEnvelope: envelope,
				AsOf:          *asOf,
				AsOfCommit:    asOfResolved,
				ID:            top.ID,
				Title:         top.Title,
				Score:         top.Score,
				Reasons:       top.Reasons,
				Unblocks:      top.Unblocks,
				ClaimCmd:      beadscli.Shell("{tool} update %s --status=in_progress", top.ID),
				ShowCmd:       beadscli.Shell("{tool} show %s", top.ID),
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
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
				sb.WriteString(beadscli.Shell("# To claim: {tool} update %s --status=in_progress\n", rec.ID))
				// Show command
				sb.WriteString(beadscli.Shell("{tool} show %s\n", rec.ID))
				sb.WriteString("\n")
			}

			// Add summary section
			sb.WriteString("# === Quick Actions ===\n")
			sb.WriteString("# To claim the top pick:\n")
			if len(recs) > 0 {
				sb.WriteString(beadscli.Shell("# {tool} update %s --status=in_progress\n", recs[0].ID))
			}
			sb.WriteString("#\n")
			sb.WriteString("# To claim all listed items (uncomment to enable):\n")
			for _, rec := range recs {
				sb.WriteString(beadscli.Shell("# {tool} update %s --status=in_progress\n", rec.ID))
			}
		}

		fmt.Print(sb.String())
		os.Exit(0)
	}

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

		// Parse SHA:beadID format
		parseCorrelationArg := func(arg string) (string, string, error) {
			parts := strings.SplitN(arg, ":", 2)
			if len(parts) != 2 {
				return "", "", fmt.Errorf("expected format: SHA:beadID, got: %s", arg)
			}
			return parts[0], parts[1], nil
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

			var targetCommit *correlation.CorrelatedCommit
			for i := range history.Commits {
				if strings.HasPrefix(history.Commits[i].SHA, commitSHA) || history.Commits[i].ShortSHA == commitSHA {
					targetCommit = &history.Commits[i]
					break
				}
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

			var originalConf float64
			if history, ok := report.Histories[beadID]; ok {
				for _, c := range history.Commits {
					if strings.HasPrefix(c.SHA, commitSHA) || c.ShortSHA == commitSHA {
						originalConf = c.Confidence
						commitSHA = c.SHA // Use full SHA
						break
					}
				}
			}

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

			var originalConf float64
			if history, ok := report.Histories[beadID]; ok {
				for _, c := range history.Commits {
					if strings.HasPrefix(c.SHA, commitSHA) || c.ShortSHA == commitSHA {
						originalConf = c.Confidence
						commitSHA = c.SHA // Use full SHA
						break
					}
				}
			}

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

		// Filter by minimum score
		var filteredCandidates []correlation.OrphanCandidate
		for _, candidate := range orphanReport.Candidates {
			if candidate.SuspicionScore >= *orphansMinScore {
				filteredCandidates = append(filteredCandidates, candidate)
			}
		}
		orphanReport.Candidates = filteredCandidates

		// Update stats for filtered results
		orphanReport.Stats.CandidateCount = len(filteredCandidates)
		if len(filteredCandidates) > 0 {
			totalSuspicion := 0
			for _, c := range filteredCandidates {
				totalSuspicion += c.SuspicionScore
			}
			orphanReport.Stats.AvgSuspicion = float64(totalSuspicion) / float64(len(filteredCandidates))
		}

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
				depGraph[issue.ID] = append(depGraph[issue.ID], dep.DependsOnID)
			}
		}

		// Configure options
		opts := correlation.RelatedWorkOptions{
			MinRelevance:      *relatedMinRelevance,
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

	// Handle --robot-impact-network flag (bv-48kr)
	// Use "all" for full network or a bead ID for subnetwork
	if *robotImpactNetwork != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
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

	// Handle --robot-sprint-list and --robot-sprint-show flags (bv-156)
	if *robotSprintList || *robotSprintShow != "" {
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
	if *robotBurndown != "" {
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

		// Find the target sprint
		var targetSprint *model.Sprint
		if *robotBurndown == "current" {
			// Find active sprint
			for i := range sprints {
				if sprints[i].IsActive() {
					targetSprint = &sprints[i]
					break
				}
			}
			if targetSprint == nil {
				fmt.Fprintf(os.Stderr, "No active sprint found\n")
				os.Exit(1)
			}
		} else {
			// Find sprint by ID
			for i := range sprints {
				if sprints[i].ID == *robotBurndown {
					targetSprint = &sprints[i]
					break
				}
			}
			if targetSprint == nil {
				fmt.Fprintf(os.Stderr, "Sprint not found: %s\n", *robotBurndown)
				os.Exit(1)
			}
		}

		// Build burndown data
		now := time.Now()
		burndown := calculateBurndownAt(targetSprint, issues, now)
		burndown.RobotEnvelope = NewRobotEnvelope(analysis.ComputeDataHash(issues))
		issueMap := make(map[string]model.Issue, len(issues))
		for _, iss := range issues {
			issueMap[iss.ID] = iss
		}
		if scopeChanges, err := computeSprintScopeChanges(cwd, targetSprint, issueMap, now); err == nil && len(scopeChanges) > 0 {
			burndown.ScopeChanges = scopeChanges
		}

		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(burndown); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding burndown: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Handle --robot-forecast flag (bv-158)
	if *robotForecast != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
			os.Exit(1)
		}

		// Build graph stats for depth calculation
		analyzer := analysis.NewAnalyzer(issues)
		graphStats := analyzer.Analyze()

		// Filter issues by label and sprint if specified
		targetIssues := make([]model.Issue, 0, len(issues))
		var sprintBeadIDs map[string]bool
		if *forecastSprint != "" {
			sprints, err := loader.LoadSprints(cwd)
			if err == nil {
				for _, s := range sprints {
					if s.ID == *forecastSprint {
						sprintBeadIDs = make(map[string]bool)
						for _, bid := range s.BeadIDs {
							sprintBeadIDs[bid] = true
						}
						break
					}
				}
			}
			if sprintBeadIDs == nil {
				fmt.Fprintf(os.Stderr, "Sprint not found: %s\n", *forecastSprint)
				os.Exit(1)
			}
		}

		for _, iss := range issues {
			// Filter by label
			if *forecastLabel != "" {
				hasLabel := false
				for _, l := range iss.Labels {
					if l == *forecastLabel {
						hasLabel = true
						break
					}
				}
				if !hasLabel {
					continue
				}
			}
			// Filter by sprint
			if sprintBeadIDs != nil && !sprintBeadIDs[iss.ID] {
				continue
			}
			targetIssues = append(targetIssues, iss)
		}

		now := time.Now()
		agents := *forecastAgents
		if agents <= 0 {
			agents = 1
		}

		type ForecastSummary struct {
			TotalMinutes  int       `json:"total_minutes"`
			TotalDays     float64   `json:"total_days"`
			AvgConfidence float64   `json:"avg_confidence"`
			EarliestETA   time.Time `json:"earliest_eta"`
			LatestETA     time.Time `json:"latest_eta"`
		}
		type ForecastOutput struct {
			RobotEnvelope
			Agents        int                    `json:"agents"`
			Filters       map[string]string      `json:"filters,omitempty"`
			ForecastCount int                    `json:"forecast_count"`
			Forecasts     []analysis.ETAEstimate `json:"forecasts"`
			Summary       *ForecastSummary       `json:"summary,omitempty"`
		}

		var forecasts []analysis.ETAEstimate
		var outputErr error

		if *robotForecast == "all" {
			// Forecast all open issues
			for _, iss := range targetIssues {
				if iss.Status == model.StatusClosed {
					continue
				}
				eta, err := analysis.EstimateETAForIssue(issues, &graphStats, iss.ID, agents, now)
				if err != nil {
					continue
				}
				forecasts = append(forecasts, eta)
			}
		} else {
			// Single issue forecast
			eta, err := analysis.EstimateETAForIssue(issues, &graphStats, *robotForecast, agents, now)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			forecasts = append(forecasts, eta)
		}

		// Build summary if multiple forecasts
		var summary *ForecastSummary
		if len(forecasts) > 1 {
			totalMin := 0
			totalConf := 0.0
			earliest := forecasts[0].ETADate
			latest := forecasts[0].ETADate
			for _, f := range forecasts {
				totalMin += f.EstimatedMinutes
				totalConf += f.Confidence
				if f.ETADate.Before(earliest) {
					earliest = f.ETADate
				}
				if f.ETADate.After(latest) {
					latest = f.ETADate
				}
			}
			summary = &ForecastSummary{
				TotalMinutes:  totalMin,
				TotalDays:     float64(totalMin) / (60.0 * 8.0), // 8hr workday
				AvgConfidence: totalConf / float64(len(forecasts)),
				EarliestETA:   earliest,
				LatestETA:     latest,
			}
		}

		// Build output
		filters := make(map[string]string)
		if *forecastLabel != "" {
			filters["label"] = *forecastLabel
		}
		if *forecastSprint != "" {
			filters["sprint"] = *forecastSprint
		}

		output := ForecastOutput{
			RobotEnvelope: NewRobotEnvelope(analysis.ComputeDataHash(issues)),
			Agents:        agents,
			ForecastCount: len(forecasts),
			Forecasts:     forecasts,
			Summary:       summary,
		}
		if len(filters) > 0 {
			output.Filters = filters
		}

		encoder := newRobotEncoder(os.Stdout)
		if outputErr = encoder.Encode(output); outputErr != nil {
			fmt.Fprintf(os.Stderr, "Error encoding forecast: %v\n", outputErr)
			os.Exit(1)
		}
		os.Exit(0)
	}

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
	if *robotMetrics {
		output := metrics.GetAllMetrics()
		encoder := newRobotEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding metrics: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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
			// JSON output
			output := struct {
				GeneratedAt      string                 `json:"generated_at"`
				ResolvedRevision string                 `json:"resolved_revision"`
				AsOf             string                 `json:"as_of,omitempty"`        // "to" snapshot ref (if --as-of used)
				AsOfCommit       string                 `json:"as_of_commit,omitempty"` // Resolved commit SHA for "to"
				FromDataHash     string                 `json:"from_data_hash"`
				ToDataHash       string                 `json:"to_data_hash"`
				Diff             *analysis.SnapshotDiff `json:"diff"`
			}{
				GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
				ResolvedRevision: revision,
				AsOf:             *asOf,
				AsOfCommit:       asOfResolved,
				FromDataHash:     analysis.ComputeDataHash(historicalIssues),
				ToDataHash:       dataHash,
				Diff:             diff,
			}

			encoder := newRobotEncoder(os.Stdout)
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding diff: %v\n", err)
				os.Exit(1)
			}
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
			return
		}

		// Launch TUI with historical issues (already loaded, no live reload)
		m := ui.NewModel(issues, activeRecipe, "")
		defer m.Stop()
		if err := runTUIProgram(m); err != nil {
			fmt.Printf("Error running beads viewer: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *exportFile != "" {
		fmt.Printf("Exporting to %s...\n", *exportFile)

		// Load and run pre-export hooks
		cwd, _ := os.Getwd()
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
		fmt.Printf("No issues found. Create some with '%s create'!\n", beadscli.Tool())
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
	if err != nil && errors.Is(err, tea.ErrProgramKilled) {
		if err == tea.ErrProgramKilled || errors.Is(err, tea.ErrInterrupted) {
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
			if dep != nil && dep.Type == model.DepBlocks {
				count++
			}
		}
	}
	return count
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
				if dep.Type == model.DepBlocks && openBlockers[dep.DependsOnID] {
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
				if dep.Type == model.DepBlocks && openBlockers[dep.DependsOnID] {
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
		var less bool

		switch s.Field {
		case "priority":
			less = issues[i].Priority < issues[j].Priority
		case "created":
			less = issues[i].CreatedAt.Before(issues[j].CreatedAt)
		case "updated":
			less = issues[i].UpdatedAt.Before(issues[j].UpdatedAt)
		case "title":
			less = strings.ToLower(issues[i].Title) < strings.ToLower(issues[j].Title)
		case "id":
			less = naturalLess(issues[i].ID, issues[j].ID)
		case "status":
			less = issues[i].Status < issues[j].Status
		default:
			// Unknown sort field, maintain order
			return false
		}

		if ascending {
			return less
		}
		return !less
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
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
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
			wizard.PrintSuccess(result)
		} else {
			// Perform deployment with issue count for verification
			result, err := wizard.PerformDeployWithIssueCount(len(exportIssues))
			if err != nil {
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
		wizard.PrintSuccess(result)
	}

	// Persist source metadata and last-export info for reliable updates.
	if source.BeadsDir != "" {
		config.SourceBeadsDir = source.BeadsDir
	}
	if source.RepoRoot != "" {
		config.SourceRepoRoot = source.RepoRoot
	}
	config.LastIssueCount = len(exportIssues)
	config.LastDataHash = analysis.ComputeDataHash(exportIssues)

	// Save config for next run
	export.SaveWizardConfig(config)

	return nil
}

type pagesSource struct {
	Issues   []model.Issue
	BeadsDir string
	RepoRoot string
	Reason   string
}

type pagesSourceCandidate struct {
	BeadsDir string
	Reason   string
}

func resolvePagesSource(config *export.WizardConfig, beadsPath string) (pagesSource, error) {
	var candidates []pagesSourceCandidate
	seen := map[string]bool{}

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

	var lastErr error
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
		if err != nil {
			return nil
		}
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

	// Check for closed beads (status = closed)
	issueStatusMap := make(map[string]bool)
	for _, issue := range issues {
		issueStatusMap[issue.ID] = issue.Status == model.StatusClosed
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
			"bv --robot-triage               # Full triage with recommendations",
			"bv --robot-next                  # Single top pick for immediate work",
			"bv --robot-plan                  # Dependency-respecting execution plan",
			"bv --robot-insights              # Deep graph analysis (PageRank, betweenness, etc.)",
			"bv --robot-triage-by-track       # Parallel work streams for multi-agent coordination",
			"bv --robot-schema                # JSON Schema definitions for all commands",
		},
		"data_source": ".beads/issues.jsonl and git history (correlations)",
		"output_modes": map[string]string{
			"json": "Default structured output",
			"toon": "Token-optimized notation (saves ~30-50% tokens)",
		},
	}

	type cmdDoc struct {
		Flag        string   `json:"flag"`
		Description string   `json:"description"`
		KeyFields   []string `json:"key_fields,omitempty"`
		Params      []string `json:"params,omitempty"`
		NeedsIssues bool     `json:"needs_issues"`
	}

	commands := map[string]cmdDoc{
		"robot-triage": {
			Flag: "--robot-triage", Description: "Unified triage: top picks, recommendations, quick wins, blockers, project health, velocity.",
			KeyFields:   []string{"triage.quick_ref.top_picks", "triage.recommendations", "triage.quick_wins", "triage.blockers_to_clear", "triage.project_health"},
			NeedsIssues: true,
		},
		"robot-next": {
			Flag: "--robot-next", Description: "Single top recommendation with claim/show commands.",
			KeyFields:   []string{"id", "title", "score", "reasons", "unblocks", "claim_command", "show_command"},
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
			KeyFields:   []string{"tracks[].track_id", "tracks[].top_pick", "tracks[].items"},
			NeedsIssues: true,
		},
		"robot-triage-by-label": {
			Flag: "--robot-triage-by-label", Description: "Triage grouped by label for area-focused agents.",
			KeyFields:   []string{"labels[].label", "labels[].top_pick", "labels[].items"},
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
		},
		"robot-diff": {
			Flag: "--robot-diff", Description: "Changes since a historical point (commit, branch, tag, or date).",
			Params:      []string{"--diff-since <ref>"},
			NeedsIssues: true,
		},
		"robot-search": {
			Flag: "--robot-search", Description: "Semantic vector search over issue titles and descriptions.",
			Params:      []string{"--search <query>", "--search-limit <n>", "--search-mode text|hybrid"},
			NeedsIssues: true,
		},
		"robot-label-health": {
			Flag: "--robot-label-health", Description: "Per-label health metrics: open/closed counts, velocity, staleness.",
			NeedsIssues: true,
		},
		"robot-label-flow": {
			Flag: "--robot-label-flow", Description: "Cross-label dependency flow analysis.",
			NeedsIssues: true,
		},
		"robot-label-attention": {
			Flag: "--robot-label-attention", Description: "Attention-ranked labels requiring focus.",
			Params:      []string{"--attention-limit <n>"},
			NeedsIssues: true,
		},
		"robot-graph": {
			Flag: "--robot-graph", Description: "Dependency graph export in JSON, DOT, or Mermaid format.",
			Params:      []string{"--graph-format json|dot|mermaid", "--graph-root <id>", "--graph-depth <n>"},
			NeedsIssues: true,
		},
		"robot-metrics": {
			Flag: "--robot-metrics", Description: "Performance metrics: timing, cache hit rates, memory usage.",
			NeedsIssues: true,
		},
		"robot-orphans": {
			Flag: "--robot-orphans", Description: "Orphan commit candidates that should be linked to beads.",
			Params:      []string{"--orphans-min-score 0-100"},
			NeedsIssues: true,
		},
		"robot-file-beads": {
			Flag: "--robot-file-beads <path>", Description: "Beads that touched a specific file path.",
			Params:      []string{"--file-beads-limit <n>"},
			NeedsIssues: true,
		},
		"robot-file-hotspots": {
			Flag: "--robot-file-hotspots", Description: "Files touched by the most beads.",
			Params:      []string{"--hotspots-limit <n>"},
			NeedsIssues: true,
		},
		"robot-file-relations": {
			Flag: "--robot-file-relations <path>", Description: "Files that frequently co-change with a given file.",
			Params:      []string{"--relations-threshold 0.0-1.0", "--relations-limit <n>"},
			NeedsIssues: true,
		},
		"robot-related": {
			Flag: "--robot-related <id>", Description: "Beads related to a specific bead ID.",
			Params:      []string{"--related-min-relevance 0-100", "--related-max-results <n>", "--related-include-closed"},
			NeedsIssues: true,
		},
		"robot-blocker-chain": {
			Flag: "--robot-blocker-chain <id>", Description: "Full blocker chain analysis for an issue.",
			NeedsIssues: true,
		},
		"robot-impact-network": {
			Flag: "--robot-impact-network [<id>|all]", Description: "Impact network graph (full or subnetwork for a bead).",
			Params:      []string{"--network-depth 1-3"},
			NeedsIssues: true,
		},
		"robot-causality": {
			Flag: "--robot-causality <id>", Description: "Causal chain analysis for a bead.",
			NeedsIssues: true,
		},
		"robot-sprint-list": {
			Flag: "--robot-sprint-list", Description: "List all sprints as JSON.",
			NeedsIssues: true,
		},
		"robot-sprint-show": {
			Flag: "--robot-sprint-show <id>", Description: "Show details for a specific sprint.",
			NeedsIssues: true,
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
			Flag: "--robot-burndown <sprint|current>", Description: "Sprint burndown data.",
			NeedsIssues: true,
		},
		"robot-drift": {
			Flag: "--robot-drift", Description: "Drift detection from saved baseline.",
			NeedsIssues: true,
		},
	}

	examples := []map[string]string{
		{"description": "Get top 3 picks for immediate work", "command": "bv --robot-triage | jq '.triage.quick_ref.top_picks[:3]'"},
		{"description": "Claim the top recommendation", "command": "bv --robot-next | jq -r '.claim_command' | sh"},
		{"description": "Find high-impact blockers to clear", "command": "bv --robot-triage | jq '.triage.blockers_to_clear | map(.id)'"},
		{"description": "Get bug-only recommendations", "command": "bv --robot-triage | jq '.triage.recommendations[] | select(.type == \"bug\")'"},
		{"description": "Multi-agent: top pick per parallel track", "command": "bv --robot-triage-by-track | jq '.triage.recommendations_by_track[].top_pick'"},
		{"description": "Find beads related to a specific file", "command": "bv --robot-file-beads src/main.rs"},
		{"description": "Search for issues by keyword", "command": "bv --search 'authentication' --robot-search"},
		{"description": "Get TOON output (saves tokens)", "command": "bv --robot-triage --format toon"},
		{"description": "Use env for default format", "command": "BV_OUTPUT_FORMAT=toon bv --robot-triage"},
		{"description": "Show token savings estimate", "command": "bv --robot-triage --format toon --stats"},
	}

	envVars := map[string]string{
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

	exitCodes := map[string]string{
		"0": "Success",
		"1": "Error (general failure, drift critical)",
		"2": "Invalid arguments or drift warning",
	}

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
		result["available_topics"] = []string{"guide", "commands", "examples", "env", "exit-codes", "all"}
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
							},
						},
						"quick_ref": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"open_count":        map[string]interface{}{"type": "integer"},
								"actionable_count":  map[string]interface{}{"type": "integer"},
								"blocked_count":     map[string]interface{}{"type": "integer"},
								"in_progress_count": map[string]interface{}{"type": "integer"},
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
				"recommendations": map[string]interface{}{"type": "array"},
				"status":          map[string]interface{}{"type": "object"},
				"usage_hints":     map[string]interface{}{"type": "array"},
			},
		},
		"robot-graph": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Graph Output",
			"description": "Dependency graph in JSON/DOT/Mermaid format",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"format":       map[string]interface{}{"type": "string", "enum": []string{"json", "dot", "mermaid"}},
				"nodes":        map[string]interface{}{"type": "array"},
				"edges":        map[string]interface{}{"type": "array"},
				"stats":        map[string]interface{}{"type": "object"},
			},
		},
		"robot-diff": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Diff Output",
			"description": "Changes since a historical point (commit, branch, date)",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"since":        map[string]interface{}{"type": "string"},
				"since_commit": map[string]interface{}{"type": "string"},
				"new":          map[string]interface{}{"type": "array"},
				"closed":       map[string]interface{}{"type": "array"},
				"modified":     map[string]interface{}{"type": "array"},
				"cycles":       map[string]interface{}{"type": "object"},
			},
		},
		"robot-alerts": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Alerts Output",
			"description": "Stale issues, blocking cascades, priority mismatches",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"alerts":       map[string]interface{}{"type": "array"},
				"summary":      map[string]interface{}{"type": "object"},
			},
		},
		"robot-suggest": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Suggest Output",
			"description": "Smart suggestions for duplicates, dependencies, labels, cycle breaks",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"suggestions":  map[string]interface{}{"type": "array"},
				"counts":       map[string]interface{}{"type": "object"},
			},
		},
		"robot-burndown": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Burndown Output",
			"description": "Sprint burndown data with scope changes and at-risk items",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at":  map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":     map[string]interface{}{"type": "string"},
				"sprint_id":     map[string]interface{}{"type": "string"},
				"burndown":      map[string]interface{}{"type": "array"},
				"scope_changes": map[string]interface{}{"type": "array"},
				"at_risk":       map[string]interface{}{"type": "array"},
			},
		},
		"robot-forecast": {
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"title":       "Robot Forecast Output",
			"description": "ETA predictions with dependency-aware scheduling",
			"type":        "object",
			"properties": map[string]interface{}{
				"generated_at": map[string]interface{}{"type": "string", "format": "date-time"},
				"data_hash":    map[string]interface{}{"type": "string"},
				"forecasts":    map[string]interface{}{"type": "array"},
				"methodology":  map[string]interface{}{"type": "object"},
			},
		},
	}

	return RobotSchemas{
		SchemaVersion: "1.0.0",
		GeneratedAt:   now,
		Envelope:      envelope,
		Commands:      commands,
	}
}
