# CLI Flags

| Flag | Type | Default | Description | Group |
| :--- | :--- | :--- | :--- | :--- |
| `--agents-add` | bool | `false` | Add beads workflow instructions to AGENTS.md (creates file if needed) | Agent File Management |
| `--agents-check` | bool | `false` | Check AGENTS.md blurb status (default if no --agents-* action) | Agent File Management |
| `--agents-dry-run` | bool | `false` | Show what would happen without executing (use with --agents-*) | Agent File Management |
| `--agents-force` | bool | `false` | Skip confirmation prompts (use with --agents-*) | Agent File Management |
| `--agents-remove` | bool | `false` | Remove beads workflow instructions from AGENTS.md | Agent File Management |
| `--agents-update` | bool | `false` | Update beads workflow instructions to latest version | Agent File Management |
| `--agent-brief` | string | (empty) | Export agent brief bundle to directory (includes triage.json, insights.json, brief.md, helpers.md) | Export & Reporting |
| `--debug-height` | int | `50` | Height for debug render | Export & Reporting |
| `--debug-render` | string | (empty) | Render a view and output to file (views: insights, board) | Export & Reporting |
| `--debug-width` | int | `180` | Width for debug render | Export & Reporting |
| `--emit-script` | bool | `false` | Emit shell script for top-N recommendations (agent workflows) | Export & Reporting |
| `--export-graph` | string | (empty) | Export graph: .html for interactive, .png/.svg for static (auto-names if empty) | Export & Reporting |
| `--export-md` | string | (empty) | Export issues to a Markdown file (e.g., report.md) | Export & Reporting |
| `--export-pages` | string | (empty) | Export static site to directory (e.g., ./bv-pages) | Export & Reporting |
| `--graph-preset` | string | `compact` | Graph layout preset: compact (default) or roomy | Export & Reporting |
| `--graph-title` | string | (empty) | Title for graph export (default: project name) | Export & Reporting |
| `--no-hooks` | bool | `false` | Skip running hooks during export | Export & Reporting |
| `--no-live-reload` | bool | `false` | Disable live-reload in preview mode | Export & Reporting |
| `--pages` | bool | `false` | Launch interactive Pages deployment wizard | Export & Reporting |
| `--pages-include-closed` | bool | `true` | Include closed issues in export (default: true) | Export & Reporting |
| `--pages-include-history` | bool | `true` | Include git history for time-travel (default: true) | Export & Reporting |
| `--pages-title` | string | (empty) | Custom title for static site | Export & Reporting |
| `--preview-pages` | string | (empty) | Preview existing static site bundle | Export & Reporting |
| `--priority-brief` | string | (empty) | Export priority brief to Markdown file (e.g., brief.md) | Export & Reporting |
| `--script-format` | string | `bash` | Script format: bash, fish, or zsh (use with --emit-script) | Export & Reporting |
| `--script-limit` | int | `5` | Limit number of items in emitted script (use with --emit-script) | Export & Reporting |
| `--watch-export` | bool | `false` | Watch for beads changes and auto-regenerate export (use with --export-pages) | Export & Reporting |
| `--background-mode` | bool | `false` | Enable experimental background snapshot loading (TUI only) | General Flags |
| `--check-update` | bool | `false` | Check if a new version is available | General Flags |
| `--cpu-profile` | string | (empty) | Write CPU profile to file | General Flags |
| `--db` | string | (empty) | Path to beads database file or .beads directory (overrides BEADS_DB and BEADS_DIR env vars) | General Flags |
| `--force-full-analysis` | bool | `false` | Compute all metrics regardless of graph size (may be slow for large graphs) | General Flags |
| `--format` | string | (empty) | Structured output format for --robot-* commands: json or toon (env: BV_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT) | General Flags |
| `--no-background-mode` | bool | `false` | Disable experimental background snapshot loading (TUI only) | General Flags |
| `--no-cache` | bool | `false` | Bypass disk cache for robot triage (also: BV_NO_CACHE=1) | General Flags |
| `--profile-json` | bool | `false` | Output profile in JSON format (use with --profile-startup) | General Flags |
| `--profile-startup` | bool | `false` | Output detailed startup timing profile for diagnostics | General Flags |
| `--rollback` | bool | `false` | Rollback to the previous version (from backup) | General Flags |
| `--stats` | bool | `false` | Show JSON vs TOON token estimates on stderr (env: TOON_STATS=1) | General Flags |
| `--theme` | string | (empty) | Color theme: light, dark, or auto (default: detect terminal background) | General Flags |
| `--update` | bool | `false` | Update bv to the latest version | General Flags |
| `--update-dry-run` | bool | `false` | Show what an update would do without installing (use via 'bv upgrade --dry-run') | General Flags |
| `--version` | bool | `false` | Show version | General Flags |
| `--yes` | bool | `false` | Skip confirmation prompts (use with --update) | General Flags |
| `--as-of` | string | (empty) | View state at point in time (commit SHA, branch, tag, or date) | History & Drift |
| `--baseline-info` | bool | `false` | Show information about the current baseline | History & Drift |
| `--bead-history` | string | (empty) | Show history for specific bead ID | History & Drift |
| `--check-drift` | bool | `false` | Check for drift from baseline (exit codes: 0=OK, 1=critical, 2=warning) | History & Drift |
| `--diff-since` | string | (empty) | Show changes since historical point (commit SHA, branch, tag, or date) | History & Drift |
| `--history-limit` | int | `500` | Max commits to analyze (0 = unlimited) | History & Drift |
| `--history-since` | string | (empty) | Limit history to commits after this date/ref (e.g., '30 days ago', '2024-01-01') | History & Drift |
| `--min-confidence` | float64 | `0` | Filter correlations by minimum confidence (0.0-1.0) | History & Drift |
| `--save-baseline` | string | (empty) | Save current metrics as baseline with optional description | History & Drift |
| `--feedback-accept` | string | (empty) | Record accept feedback for issue ID (tunes recommendation weights) | Other |
| `--feedback-ignore` | string | (empty) | Record ignore feedback for issue ID (tunes recommendation weights) | Other |
| `--feedback-reset` | bool | `false` | Reset all feedback data to defaults | Other |
| `--feedback-show` | bool | `false` | Show current feedback status and weight adjustments | Other |
| `--generate-docs` | bool | `false` | Generate documentation markdown and JSON artifacts | Other |
| `--id-pattern` | stringArray | `[]` | Custom bead ID regex for commit-message matching, e.g. 'bh-[a-z0-9]{5}' (repeatable; capture group 1 is the ID, else the whole match) (#188) | Other |
| `--network-depth` | int | `2` | Depth of subnetwork when querying specific bead (1-3) | Other |
| `--agents` | int | `1` | Number of parallel agents for capacity simulation | Robot & Planning Flags |
| `--attention-limit` | int | `5` | Limit number of labels in --robot-label-attention output | Robot & Planning Flags |
| `--brief` | bool | `false` | Compact --robot-triage output: only decision-relevant fields (id, title, status, assignee, blockers, unblocks) (#183) | Robot & Planning Flags |
| `--capacity-label` | string | (empty) | Filter capacity simulation by label | Robot & Planning Flags |
| `--correlation-by` | string | (empty) | Agent/user identifier for correlation feedback | Robot & Planning Flags |
| `--correlation-reason` | string | (empty) | Reason for correlation feedback | Robot & Planning Flags |
| `--file-beads-limit` | int | `20` | Max closed beads to show (use with --robot-file-beads) | Robot & Planning Flags |
| `--forecast-agents` | int | `1` | Number of parallel agents for capacity calculation | Robot & Planning Flags |
| `--forecast-label` | string | (empty) | Filter forecast by label | Robot & Planning Flags |
| `--forecast-sprint` | string | (empty) | Filter forecast by sprint ID | Robot & Planning Flags |
| `--graph-depth` | int | `0` | Max depth for subgraph (0 = unlimited) | Robot & Planning Flags |
| `--graph-format` | string | `json` | Graph output format: json, dot, mermaid | Robot & Planning Flags |
| `--graph-root` | string | (empty) | Subgraph from specific root issue ID | Robot & Planning Flags |
| `--hotspots-limit` | int | `10` | Max hotspots to show (use with --robot-file-hotspots) | Robot & Planning Flags |
| `--orphans-min-score` | int | `30` | Minimum suspicion score for orphan candidates (0-100) | Robot & Planning Flags |
| `--related-include-closed` | bool | `false` | Include closed beads in related work results | Robot & Planning Flags |
| `--related-max-results` | int | `10` | Max results per category for related work | Robot & Planning Flags |
| `--related-min-relevance` | percent_or_fraction | `20` | Minimum relevance score for related work (int 0-100 percent OR float 0.0-1.0 fraction) | Robot & Planning Flags |
| `--relations-limit` | int | `10` | Max related files to show | Robot & Planning Flags |
| `--relations-threshold` | float64 | `0.5` | Minimum correlation threshold (0.0-1.0) for related files | Robot & Planning Flags |
| `--robot-alerts` | bool | `false` | Output alerts (drift + proactive) as JSON for AI agents | Robot & Planning Flags |
| `--robot-blocker-chain` | string | (empty) | Output full blocker chain analysis for issue ID as JSON | Robot & Planning Flags |
| `--robot-burndown` | string | (empty) | Output burndown data for sprint ID, or 'current' for active sprint | Robot & Planning Flags |
| `--robot-capabilities` | bool | `false` | Output machine-readable command capabilities for AI agents | Robot & Planning Flags |
| `--robot-capacity` | bool | `false` | Output capacity simulation and completion projection as JSON | Robot & Planning Flags |
| `--robot-causality` | string | (empty) | Output causal chain analysis for bead ID as JSON | Robot & Planning Flags |
| `--robot-confirm-correlation` | string | (empty) | Confirm a correlation is correct (format: SHA:beadID) | Robot & Planning Flags |
| `--robot-correlation-stats` | bool | `false` | Output correlation feedback statistics as JSON | Robot & Planning Flags |
| `--robot-diff` | bool | `false` | Output diff as JSON (use with --diff-since) | Robot & Planning Flags |
| `--robot-docs` | string | (empty) | Machine-readable JSON docs for AI agents. Topics: guide, commands, examples, env, exit-codes, all | Robot & Planning Flags |
| `--robot-drift` | bool | `false` | Output drift check as JSON (use with --check-drift) | Robot & Planning Flags |
| `--robot-explain-correlation` | string | (empty) | Explain why a commit is linked to a bead (format: SHA:beadID) | Robot & Planning Flags |
| `--robot-file-beads` | string | (empty) | Output beads that touched a file path as JSON | Robot & Planning Flags |
| `--robot-file-hotspots` | bool | `false` | Output files touched by most beads as JSON | Robot & Planning Flags |
| `--robot-file-relations` | string | (empty) | Output files that frequently co-change with the given file path | Robot & Planning Flags |
| `--robot-forecast` | string | (empty) | Output ETA forecast for bead ID, or 'all' for all open issues | Robot & Planning Flags |
| `--robot-graph` | bool | `false` | Output dependency graph as JSON/DOT/Mermaid for AI agents | Robot & Planning Flags |
| `--robot-help` | bool | `false` | Show AI agent help | Robot & Planning Flags |
| `--robot-history` | bool | `false` | Output bead-to-commit correlations as JSON | Robot & Planning Flags |
| `--robot-history-timeout-ms` | int | `-1` | Budget in ms for the git-history prologue of robot triage (0 = unbounded; default 10000, env BV_ROBOT_HISTORY_TIMEOUT_MS) | Robot & Planning Flags |
| `--robot-impact` | string | (empty) | Analyze impact of modifying files (comma-separated paths) | Robot & Planning Flags |
| `--robot-impact-network` | string | (empty) | Output bead impact network as JSON (empty for full, or bead ID for subnetwork) | Robot & Planning Flags |
| `--robot-insights` | bool | `false` | Output graph analysis and insights as JSON for AI agents | Robot & Planning Flags |
| `--robot-label-attention` | bool | `false` | Output attention-ranked labels as JSON for AI agents | Robot & Planning Flags |
| `--robot-label-flow` | bool | `false` | Output cross-label dependency flow as JSON for AI agents | Robot & Planning Flags |
| `--robot-label-health` | bool | `false` | Output label health metrics as JSON for AI agents | Robot & Planning Flags |
| `--robot-metrics` | bool | `false` | Output performance metrics (timing, cache, memory) as JSON | Robot & Planning Flags |
| `--robot-next` | bool | `false` | Output only the top pick recommendation as JSON (minimal triage) | Robot & Planning Flags |
| `--robot-not-ready-labels` | string | (empty) | Comma-separated labels marking a bead not-ready: excluded from claimable --robot-next/--robot-triage top picks (env: BV_ROBOT_NOT_READY_LABELS; #173) | Robot & Planning Flags |
| `--robot-orphans` | bool | `false` | Output orphan commit candidates (commits that should be linked but aren't) as JSON | Robot & Planning Flags |
| `--robot-plan` | bool | `false` | Output dependency-respecting execution plan as JSON for AI agents | Robot & Planning Flags |
| `--robot-priority` | bool | `false` | Output priority recommendations as JSON for AI agents | Robot & Planning Flags |
| `--robot-recipes` | bool | `false` | Output available recipes as JSON for AI agents | Robot & Planning Flags |
| `--robot-reject-correlation` | string | (empty) | Reject an incorrect correlation (format: SHA:beadID) | Robot & Planning Flags |
| `--robot-related` | string | (empty) | Output beads related to a specific bead ID as JSON | Robot & Planning Flags |
| `--robot-schema` | bool | `false` | Output JSON Schema definitions for all robot commands | Robot & Planning Flags |
| `--robot-search` | bool | `false` | Output semantic search results as JSON for AI agents (use with --search) | Robot & Planning Flags |
| `--robot-sprint-list` | bool | `false` | Output sprints as JSON | Robot & Planning Flags |
| `--robot-sprint-show` | string | (empty) | Output specific sprint details as JSON | Robot & Planning Flags |
| `--robot-suggest` | bool | `false` | Output smart suggestions (duplicates, dependencies, labels, cycles) as JSON | Robot & Planning Flags |
| `--robot-triage` | bool | `false` | Output unified triage as JSON (the mega-command for AI agents) | Robot & Planning Flags |
| `--robot-triage-by-label` | bool | `false` | Group triage recommendations by label (bv-87) | Robot & Planning Flags |
| `--robot-triage-by-track` | bool | `false` | Group triage recommendations by execution track (bv-87) | Robot & Planning Flags |
| `--schema-command` | string | (empty) | Output schema for specific command only (e.g., robot-triage) | Robot & Planning Flags |
| `--suggest-bead` | string | (empty) | Filter suggestions for specific bead ID | Robot & Planning Flags |
| `--suggest-confidence` | float64 | `0` | Minimum confidence for suggestions (0.0-1.0) | Robot & Planning Flags |
| `--suggest-type` | string | (empty) | Filter suggestions by type: duplicate, dependency, label, cycle | Robot & Planning Flags |
| `--alert-label` | string | (empty) | Filter robot alerts by label match | Search & Filters |
| `--alert-type` | string | (empty) | Filter robot alerts by alert type (e.g., stale_issue) | Search & Filters |
| `--label` | string | (empty) | Scope analysis to label's subgraph (affects --robot-insights, --robot-plan, --robot-priority) | Search & Filters |
| `--recipe` | string | (empty) | Apply a recipe by name (e.g., triage, actionable, high-impact) or by .yaml/.yml file path (e.g., .beads/recipes/sprint.yaml) | Search & Filters |
| `--repo` | string | (empty) | Filter issues by repository prefix (e.g., 'api-' or 'api') | Search & Filters |
| `--robot-by-assignee` | string | (empty) | Filter robot outputs by assignee (exact match) | Search & Filters |
| `--robot-by-label` | string | (empty) | Filter robot outputs by label (exact match) | Search & Filters |
| `--robot-max-results` | int | `0` | Limit robot output count (0 = use defaults) | Search & Filters |
| `--robot-min-confidence` | float64 | `0` | Filter robot outputs by minimum confidence (0.0-1.0) | Search & Filters |
| `--search` | string | (empty) | Semantic search query (vector-based; builds/updates index on first run) | Search & Filters |
| `--search-limit` | int | `10` | Max results for --search/--robot-search | Search & Filters |
| `--search-mode` | string | (empty) | Search ranking mode: text or hybrid (default: BV_SEARCH_MODE or text) | Search & Filters |
| `--search-preset` | string | (empty) | Hybrid preset name (default: BV_SEARCH_PRESET or default) | Search & Filters |
| `--search-weights` | string | (empty) | Hybrid weights JSON (overrides preset; keys: text,pagerank,status,impact,priority,recency) | Search & Filters |
| `--severity` | string | (empty) | Filter robot alerts by severity (info|warning|critical) | Search & Filters |
| `--workspace` | string | (empty) | Load issues from workspace config file (.bv/workspace.yaml) | Search & Filters |
