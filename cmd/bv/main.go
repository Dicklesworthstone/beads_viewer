package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"

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

	// Load issues from current directory
	issues, err := loader.LoadIssues("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading beads: %v\n", err)
		fmt.Fprintln(os.Stderr, "Make sure you are in a project initialized with 'bd init'.")
		os.Exit(1)
	}

	if *robotInsights {
		analyzer := analysis.NewAnalyzer(issues)
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
			GeneratedAt     string                           `json:"generated_at"`
			Recommendations []analysis.PriorityRecommendation `json:"recommendations"`
			Summary         struct {
				TotalIssues    int `json:"total_issues"`
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
				GeneratedAt string                  `json:"generated_at"`
				Diff        *analysis.SnapshotDiff  `json:"diff"`
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

		// Launch TUI with historical issues
		m := ui.NewModel(historicalIssues, activeRecipe)
		p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Printf("Error running beads viewer: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *exportFile != "" {
		fmt.Printf("Exporting to %s...\n", *exportFile)
		if err := export.SaveMarkdownToFile(issues, *exportFile); err != nil {
			fmt.Printf("Error exporting: %v\n", err)
			os.Exit(1)
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

	// Initial Model
	m := ui.NewModel(issues, activeRecipe)

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
