package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/hooks"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	help := flag.Bool("help", false, "Show help")
	versionFlag := flag.Bool("version", false, "Show version")
	exportFile := flag.String("export-md", "", "Export issues to a Markdown file (e.g., report.md)")
	robotHelp := flag.Bool("robot-help", false, "Show AI agent help")
	robotInsights := flag.Bool("robot-insights", false, "Output graph analysis and insights as JSON for AI agents")
	robotPlan := flag.Bool("robot-plan", false, "Output dependency-respecting execution plan as JSON for AI agents")
	robotPriority := flag.Bool("robot-priority", false, "Output priority recommendations as JSON for AI agents")
	robotDiff := flag.Bool("robot-diff", false, "Output diff as JSON (use with --diff-since)")
	robotRecipes := flag.Bool("robot-recipes", false, "Output available recipes as JSON for AI agents")
	recipeName := flag.String("recipe", "", "Apply named recipe (e.g., triage, actionable, high-impact)")
	recipeShort := flag.String("r", "", "Shorthand for --recipe")
	diffSince := flag.String("diff-since", "", "Show changes since historical point (commit SHA, branch, tag, or date)")
	asOf := flag.String("as-of", "", "View state at point in time (commit SHA, branch, tag, or date)")
	forceFullAnalysis := flag.Bool("force-full-analysis", false, "Compute all metrics regardless of graph size (may be slow for large graphs)")
	profileStartup := flag.Bool("profile-startup", false, "Output detailed startup timing profile for diagnostics")
	profileTUIStartup := flag.Bool("profile-tui-startup", false, "Output comprehensive TUI startup profile (includes analysis + UI components)")
	profileJSON := flag.Bool("profile-json", false, "Output profile in JSON format (use with --profile-startup or --profile-tui-startup)")
	noHooks := flag.Bool("no-hooks", false, "Skip running hooks during export")
	workspaceConfig := flag.String("workspace", "", "Load issues from workspace config file (.bv/workspace.yaml)")
	repoFilter := flag.String("repo", "", "Filter issues by repository prefix (e.g., 'api-' or 'api')")
	saveBaseline := flag.String("save-baseline", "", "Save current metrics as baseline with optional description")
	baselineInfo := flag.Bool("baseline-info", false, "Show information about the current baseline")
	checkDrift := flag.Bool("check-drift", false, "Check for drift from baseline (exit codes: 0=OK, 1=critical, 2=warning)")
	robotDriftCheck := flag.Bool("robot-drift", false, "Output drift check as JSON (use with --check-drift)")
	flag.Parse()

	// Handle -r shorthand
	if *recipeShort != "" && *recipeName == "" {
		*recipeName = *recipeShort
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
		fmt.Println("      View issue state at a point in time.")
		fmt.Println("      Useful for reviewing historical project state.")
		fmt.Println("")
		fmt.Println("  --robot-diff")
		fmt.Println("      Output diff as JSON (use with --diff-since).")
		fmt.Println("")
		fmt.Println("  --robot-recipes")
		fmt.Println("      Lists all available recipes as JSON.")
		fmt.Println("      Output: {recipes: [{name, description, source}]}")
		fmt.Println("      Sources: 'builtin', 'user' (~/.config/bv/recipes.yaml), 'project' (.bv/recipes.yaml)")
		fmt.Println("")
		fmt.Println("  --recipe NAME, -r NAME")
		fmt.Println("      Apply a named recipe to filter and sort issues.")
		fmt.Println("      Example: bv --recipe actionable")
		fmt.Println("      Built-in recipes: default, actionable, recent, blocked, high-impact, stale")
		fmt.Println("")
		fmt.Println("  --profile-startup")
		fmt.Println("      Outputs detailed startup timing profile for diagnostics.")
		fmt.Println("      Shows Phase 1 (blocking) and Phase 2 (async) breakdown.")
		fmt.Println("      Provides recommendations based on timing analysis.")
		fmt.Println("      Use with --profile-json for machine-readable output.")
		fmt.Println("")
		fmt.Println("  --profile-tui-startup")
		fmt.Println("      Comprehensive TUI startup profile including UI component initialization.")
		fmt.Println("      Shows all phases: data loading, graph analysis, and TUI components.")
		fmt.Println("      Use this to diagnose slow startup when --profile-startup shows fast times.")
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

	// Load recipes (needed for both --robot-recipes and --recipe)
	recipeLoader, err := recipe.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Error loading recipes: %v\n", err)
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

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding recipes: %v\n", err)
			os.Exit(1)
		}
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

	if *workspaceConfig != "" {
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
			fmt.Fprintf(os.Stderr, "Warning: %d repos failed to load\n", summary.FailedRepos)
			for _, name := range summary.FailedRepoNames {
				fmt.Fprintf(os.Stderr, "  - %s\n", name)
			}
		}
		// No live reload for workspace mode (multiple files)
		beadsPath = ""
	} else {
		// Load from single repo (original behavior)
		var err error
		issues, err = loader.LoadIssues("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
			fmt.Fprintln(os.Stderr, "Make sure you are in a project initialized with 'bd init'.")
			os.Exit(1)
		}
		// Get beads file path for live reload
		cwd, _ := os.Getwd()
		beadsPath, _ = loader.FindJSONLPath(filepath.Join(cwd, ".beads"))
	}
	loadDuration := time.Since(loadStart)

	// Apply --repo filter if specified
	if *repoFilter != "" {
		issues = filterByRepo(issues, *repoFilter)
	}

	// Handle --profile-startup
	if *profileStartup {
		runProfileStartup(issues, loadDuration, *profileJSON, *forceFullAnalysis)
		os.Exit(0)
	}

	// Handle --profile-tui-startup
	if *profileTUIStartup {
		runProfileTUIStartup(issues, loadDuration, activeRecipe, beadsPath, *profileJSON)
		os.Exit(0)
	}

	// Get project directory for baseline operations
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
			fmt.Fprintf(os.Stderr, "Warning: Error loading drift config: %v\n", err)
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

			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
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

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(insights); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding insights: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *robotPlan {
		analyzer := analysis.NewAnalyzer(issues)
		if *forceFullAnalysis {
			cfg := analysis.FullAnalysisConfig()
			analyzer.SetConfig(&cfg)
		}
		plan := analyzer.GetExecutionPlan()

		// Wrap with metadata
		output := struct {
			GeneratedAt string                 `json:"generated_at"`
			Plan        analysis.ExecutionPlan `json:"plan"`
		}{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Plan:        plan,
		}

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding execution plan: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *robotPriority {
		analyzer := analysis.NewAnalyzer(issues)
		if *forceFullAnalysis {
			cfg := analysis.FullAnalysisConfig()
			analyzer.SetConfig(&cfg)
		}
		recommendations := analyzer.GenerateRecommendations()

		// Count high confidence recommendations
		highConfidence := 0
		for _, rec := range recommendations {
			if rec.Confidence >= 0.7 {
				highConfidence++
			}
		}

		// Build output with summary
		output := struct {
			GeneratedAt     string                            `json:"generated_at"`
			Recommendations []analysis.PriorityRecommendation `json:"recommendations"`
			Summary         struct {
				TotalIssues     int `json:"total_issues"`
				Recommendations int `json:"recommendations"`
				HighConfidence  int `json:"high_confidence"`
			} `json:"summary"`
		}{
			GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
			Recommendations: recommendations,
		}
		output.Summary.TotalIssues = len(issues)
		output.Summary.Recommendations = len(recommendations)
		output.Summary.HighConfidence = highConfidence

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding priority recommendations: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Handle --diff-since flag
	if *diffSince != "" {
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
				GeneratedAt string                 `json:"generated_at"`
				Diff        *analysis.SnapshotDiff `json:"diff"`
			}{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				Diff:        diff,
			}

			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
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

	// Handle --as-of flag
	if *asOf != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
			os.Exit(1)
		}

		gitLoader := loader.NewGitLoader(cwd)

		// Load historical issues
		historicalIssues, err := gitLoader.LoadAt(*asOf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading issues at %s: %v\n", *asOf, err)
			os.Exit(1)
		}

		if len(historicalIssues) == 0 {
			fmt.Printf("No issues found at %s.\n", *asOf)
			os.Exit(0)
		}

		// Launch TUI with historical issues (no live reload for historical view)
		m := ui.NewModel(historicalIssues, activeRecipe, "")
		p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Printf("Error running beads viewer: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
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
		fmt.Println("No issues found. Create some with 'bd create'!")
		os.Exit(0)
	}

	// Apply recipe filters and sorting if specified
	if activeRecipe != nil {
		issues = applyRecipeFilters(issues, activeRecipe)
		issues = applyRecipeSort(issues, activeRecipe)
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

	// Run Program
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running beads viewer: %v\n", err)
		os.Exit(1)
	}
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
			less = issues[i].ID < issues[j].ID
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
			DataPath:        ".beads/beads.jsonl",
			LoadJSONL:       loadDuration.String(),
			Profile:         profile,
			TotalWithLoad:   totalWithLoad.String(),
			Recommendations: generateProfileRecommendations(profile, loadDuration, totalWithLoad),
		}

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding profile: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Human-readable output
		printProfileReport(profile, loadDuration, totalWithLoad)
	}
}

// runProfileTUIStartup runs profiled TUI startup and outputs comprehensive results
func runProfileTUIStartup(issues []model.Issue, loadDuration time.Duration, activeRecipe *recipe.Recipe, beadsPath string, jsonOutput bool) {
	// Create model with profiling
	_, tuiProfile := ui.NewModelWithProfile(issues, activeRecipe, beadsPath)

	// Set the load duration
	tuiProfile.SetLoadDuration(loadDuration)
	tuiProfile.ComputeTotals()

	if jsonOutput {
		// JSON output
		output := struct {
			GeneratedAt string             `json:"generated_at"`
			DataPath    string             `json:"data_path"`
			Profile     *ui.TUIStartupProfile `json:"profile"`
		}{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			DataPath:    ".beads/beads.jsonl",
			Profile:     tuiProfile,
		}

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding profile: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Human-readable output
		printTUIProfileReport(tuiProfile)
	}
}

// printTUIProfileReport outputs a human-readable TUI startup profile
func printTUIProfileReport(profile *ui.TUIStartupProfile) {
	fmt.Println("TUI Startup Profile")
	fmt.Println("===================")
	fmt.Printf("Data: %d issues, %d dependencies\n\n", profile.IssueCount, profile.DependencyCount)

	// Phase 1 - Data Loading
	fmt.Println("Phase 1 - Data Loading:")
	fmt.Printf("  Load JSONL:        %v\n\n", formatDuration(profile.LoadJSONL))

	// Phase 2 - Analysis
	if profile.Analysis != nil {
		fmt.Println("Phase 2 - Graph Analysis:")
		fmt.Printf("  Build graph:       %v\n", formatDuration(profile.Analysis.BuildGraph))
		fmt.Printf("  Degree:            %v\n", formatDuration(profile.Analysis.Degree))
		fmt.Printf("  TopoSort:          %v\n", formatDuration(profile.Analysis.TopoSort))
		printMetricLine("PageRank", profile.Analysis.PageRank, profile.Analysis.PageRankTO, profile.Analysis.Config.ComputePageRank)
		printMetricLine("Betweenness", profile.Analysis.Betweenness, profile.Analysis.BetweennessTO, profile.Analysis.Config.ComputeBetweenness)
		printMetricLine("Eigenvector", profile.Analysis.Eigenvector, false, profile.Analysis.Config.ComputeEigenvector)
		printMetricLine("HITS", profile.Analysis.HITS, profile.Analysis.HITSTO, profile.Analysis.Config.ComputeHITS)
		printMetricLine("Critical Path", profile.Analysis.CriticalPath, false, profile.Analysis.Config.ComputeCriticalPath)
		printCyclesLine(profile.Analysis)
		fmt.Printf("  Total Analysis:    %v\n\n", formatDuration(profile.Analysis.Total))
	}

	// Phase 3 - TUI Components
	fmt.Println("Phase 3 - TUI Components:")
	fmt.Printf("  Sort issues:       %v\n", formatDuration(profile.SortIssues))
	fmt.Printf("  Build lookup:      %v\n", formatDuration(profile.BuildLookup))
	fmt.Printf("  Compute stats:     %v\n", formatDuration(profile.ComputeStats))
	fmt.Printf("  Theme init:        %v\n", formatDuration(profile.ThemeInit))
	fmt.Printf("  List setup:        %v\n", formatDuration(profile.ListSetup))
	fmt.Printf("  Glamour renderer:  %v\n", formatDuration(profile.GlamourInit))
	fmt.Printf("  Board model:       %v\n", formatDuration(profile.BoardInit))
	fmt.Printf("  Insights panel:    %v\n", formatDuration(profile.InsightsInit))
	fmt.Printf("  Graph view:        %v\n", formatDuration(profile.GraphViewInit))
	fmt.Printf("  Recipe loader:     %v\n", formatDuration(profile.RecipeLoader))
	fmt.Printf("  Recipe picker:     %v\n", formatDuration(profile.RecipePicker))
	fmt.Printf("  Text input:        %v\n", formatDuration(profile.TextInputInit))
	fmt.Printf("  File watcher:      %v\n", formatDuration(profile.FileWatcherInit))
	fmt.Printf("  Total TUI:         %v\n\n", formatDuration(profile.TUIComponentsTotal))

	// Total
	fmt.Printf("NewModel total:      %v\n", formatDuration(profile.NewModelTotal))
	fmt.Printf("Total startup:       %v\n\n", formatDuration(profile.TotalStartup))

	// Recommendations
	recommendations := generateTUIProfileRecommendations(profile)
	if len(recommendations) > 0 {
		fmt.Println("Recommendations:")
		for _, rec := range recommendations {
			fmt.Printf("  %s\n", rec)
		}
	}
}

// generateTUIProfileRecommendations generates actionable recommendations based on TUI profile
func generateTUIProfileRecommendations(profile *ui.TUIStartupProfile) []string {
	var recs []string

	// Check overall startup time
	if profile.TotalStartup < 500*time.Millisecond {
		recs = append(recs, "✓ Startup within acceptable range (<500ms)")
	} else if profile.TotalStartup < 1*time.Second {
		recs = append(recs, "✓ Startup acceptable (<1s)")
	} else if profile.TotalStartup < 2*time.Second {
		recs = append(recs, "⚠ Startup is slow (1-2s)")
	} else {
		recs = append(recs, "⚠ Startup is very slow (>2s) - see breakdown above")
	}

	// Find the slowest TUI component
	type component struct {
		name     string
		duration time.Duration
	}
	components := []component{
		{"Glamour renderer", profile.GlamourInit},
		{"File watcher", profile.FileWatcherInit},
		{"Recipe loader", profile.RecipeLoader},
		{"Board model", profile.BoardInit},
		{"Insights panel", profile.InsightsInit},
		{"Graph view", profile.GraphViewInit},
		{"List setup", profile.ListSetup},
		{"Theme init", profile.ThemeInit},
	}

	// Find slowest
	var slowest component
	for _, c := range components {
		if c.duration > slowest.duration {
			slowest = c
		}
	}

	if slowest.duration > 100*time.Millisecond {
		recs = append(recs, fmt.Sprintf("⚠ Slowest component: %s (%v)", slowest.name, formatDuration(slowest.duration)))
	}

	// Check if analysis is dominating
	if profile.Analysis != nil && profile.Analysis.Total > profile.TUIComponentsTotal {
		recs = append(recs, "ℹ Analysis phase is taking longer than TUI initialization - use --profile-startup for details")
	}

	return recs
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
