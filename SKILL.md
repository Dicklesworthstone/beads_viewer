---
name: bv
description: "Beads Viewer - Graph-aware triage engine for Beads projects. Computes PageRank, betweenness, critical path, and cycles. Use --robot-* flags for AI agents."
---

# BV - Beads Viewer

A graph-aware triage engine for Beads projects (`.beads/issues.jsonl` in current `br` and Dolt-backed `bd` workspaces, with `.beads/beads.jsonl` supported for legacy workspaces). Computes graph metrics, generates execution plans, and explains recommendations. Human TUI for browsing; robot flags for AI agents. Compare source, scope, configuration, reference time and metric status when checking repeatability.

## Why BV vs Raw Beads

| Capability | Raw Beads JSONL | BV Robot Mode |
|------------|-----------------|---------------|
| Query | "List all issues" | "List the top 5 bottlenecks blocking the release" |
| Context Cost | Full issue records | Compact summaries available; full graph output still grows with the project |
| Graph Logic | Agent must compute | Pre-computed (PageRank, betweenness, cycles) |
| Safety | Agent might miss cycles | Cycles explicitly flagged |

Use BV instead of parsing Beads JSONL directly. It computes graph metrics deterministically.

## CRITICAL: Robot Mode for Agents

**Never run bare `bv`**. It launches an interactive TUI that blocks your session.

Always use `--robot-*` flags:

```bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick
bv --robot-plan          # Parallel execution tracks
bv --robot-insights      # Full graph metrics
```

## The 9 Graph Metrics

BV computes these metrics to surface hidden project dynamics:

| Metric | What It Measures | Key Insight |
|--------|------------------|-------------|
| **PageRank** | Recursive dependency importance | Foundational blockers |
| **Betweenness** | Shortest-path traffic | Bottlenecks and bridges |
| **HITS** | Hub/Authority duality | Epics vs utilities |
| **Critical Path** | Longest dependent chain in task counts | Prerequisites supporting long chains; not delivery-time estimates |
| **Eigenvector** | Influence via neighbors | Strategic dependencies |
| **Degree** | Direct connection counts | Immediate blockers/blocked |
| **Density** | Directed edges / possible edges: `E / (N × (N−1))` for `N > 1` | Project coupling health |
| **Cycles** | Circular dependencies | Structural errors (must fix!) |
| **Topo Sort** | Prerequisites-first order for acyclic graphs | Structural order; readiness still requires lifecycle and dependency checks |

## Two-Phase Analysis

BV uses async computation with timeouts:

- **Phase 1 (instant):** degree, topo sort, density
- **Phase 2:** PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core, articulation points and slack. `ConfigForSize` selects per-metric budgets and size/density skips; this is not one 500 ms end-to-end deadline.

Check the capitalized metric keys in `.status`, such as `.status.Betweenness`. Normal states are `pending`, `computed`, `timeout` or `skipped`; sampled betweenness reports `computed` with `reason: "approximate"`. Treat an error or unknown state as unavailable. Cycle detection stores one representative per cyclic component, subject to limits, rather than every simple cycle.

## Robot Commands Reference

### Triage & Planning

```bash
bv --robot-triage              # Full triage: recommendations, quick_wins, blockers_to_clear
bv --robot-next                # Claimable pick with route, or an explicit no-action response
bv --robot-plan                # Parallel execution tracks with unblocks lists
bv --robot-priority            # Priority misalignment detection
```

### Graph Analysis

```bash
bv --robot-insights            # Full metrics: PageRank, betweenness, HITS, cycles, etc.
bv --robot-label-health        # Per-label health: healthy|warning|critical
bv --robot-label-flow          # Cross-label dependency flow matrix
bv --robot-label-attention     # Attention-ranked labels
```

### History & Changes

```bash
bv --robot-history             # Bead-to-commit correlations
bv --robot-diff --diff-since HEAD~1 # Changes since an existing Git ref
```

### Other Commands

```bash
bv --robot-burndown sprint-1  # Use an existing sprint ID for burndown/scope changes
bv --robot-forecast all       # Duration/velocity heuristic, not a scheduler
bv --robot-alerts              # Stale issues, blocking cascades
bv --robot-suggest             # Hygiene: duplicates, missing deps, cycle breaks
bv --robot-graph               # Dependency graph export (JSON, DOT, Mermaid)
bv --export-graph graph.html   # Self-contained interactive HTML visualization
```

## Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

## Built-in Recipes

| Recipe | Purpose |
|--------|---------|
| `default` | All open issues sorted by priority |
| `actionable` | Ready to work (no blockers) |
| `high-impact` | Top PageRank scores |
| `blocked` | Waiting on dependencies |
| `stale` | Open but untouched for 30+ days |
| `triage` | Sorted by computed triage score |
| `quick-wins` | Easy P2/P3 items with no blockers |
| `bottlenecks` | High betweenness nodes |

## Robot Output Structure

Issue-backed responses include `data_hash`, `source_authority`, `authority_hash` and `scope_hash`. Use these alongside the reference clock and effective configuration. Metric-bearing commands include `.status`; graph export and metadata commands such as capabilities have their own schemas. Use `bv --robot-schema` for command-specific contracts. Historical issue analysis includes `as_of` / `as_of_commit`.

Partial or unknown authority permits exploratory results but withholds proven picks and claim commands. A computationally ready issue must be open or in progress, have no future deferral, and have satisfied direct and inherited parent dependency gates. Closed/tombstoned predecessors satisfy gates; missing records do not. Candidate filters stay separate from this full-source dependency context.

A new claim additionally requires an open, unassigned, non-epic issue with no open children or configured not-ready labels, plus a usable live tracker route. Plans can include ongoing work; the first recommendation is not necessarily claimable.

### --robot-triage Output

Selected fields, with illustrative values; the full response also includes source diagnostics and metric status:

```json
{
  "triage": {
    "quick_ref": { "open_count": 3, "actionable_count": 1, "blocked_count": 0 },
    "recommendations": [
      { "id": "bd-123", "score": 0.85, "reasons": ["Unblocks 5 tasks"] }
    ]
  }
}
```

Other triage fields include `.triage.quick_wins`, `.triage.blockers_to_clear`, `.triage.project_health` and `.triage.commands`. Inspect `.actions` in `--robot-next` for a selected issue's typed live route.

### --robot-insights Output

Selected fields; names and `ID`/`Value` casing are significant:

```json
{
  "Bottlenecks": [{ "ID": "bd-123", "Value": 0.45 }],
  "Keystones": [{ "ID": "bd-456", "Value": 12.0 }],
  "Cycles": [["bd-A", "bd-B", "bd-A"]],
  "ClusterDensity": 0.045,
  "full_stats": { "pagerank": { "bd-123": 0.15 } },
  "status": { "PageRank": { "state": "computed" }, "Cycles": { "state": "computed" } }
}
```

## jq Quick Reference

```bash
bv --robot-triage | jq '.triage.quick_ref'                 # At-a-glance summary
bv --robot-triage | jq '.triage.recommendations[0]'        # Top recommendation, not necessarily claimable
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '{status: .status.Cycles, cycles: .Cycles}' # Stored cycle representatives
bv --robot-label-health | jq '.results.labels[] | select(.health_level == "critical")'
```

## Agent Workflow Pattern

```bash
# 1. Inspect priorities and cycle computation status
bv --robot-triage | jq '.triage.quick_ref'
bv --robot-insights | jq '{status: .status.Cycles, cycles: .Cycles}'

# 2. Ask for a live next action; metadata-free/historical/partial sources may refuse
NEXT=$(bv --robot-next)
printf '%s\n' "$NEXT" | jq '{actionable, id, diagnostic_top_pick, actions}'

# 3. Require the actual claim route before considering a mutation
printf '%s\n' "$NEXT" | jq -e '.actionable == true and .source_authority.claim_safe == true and (.actions.claim.argv | type == "array")'
```

The last check intentionally fails for a no-action response. Inspect `.actions.show` against the current tracker state, then use the returned `.actions.claim.argv` in its `.working_directory` only when a claim is intended. Execute the argument array directly (for example, Python `subprocess.run(action["argv"], cwd=action["working_directory"], check=True)`); do not split a shell string or substitute a namespaced display ID. Analysis does not reserve work or guarantee a later claim succeeds. Close work through the same tracker and original local ID after completion.

## TUI Views (for Humans)

When running `bv` interactively (not for agents):

| Key | View |
|-----|------|
| `l` | Label picker (quick filter by label) |
| `b` | Kanban board |
| `g` | Graph view (directed dependencies, including cycles) |
| `E` | Tree view (parent-child hierarchy) |
| `i` | Insights dashboard (10 panels, including priority recommendations) |
| `h` | History view (bead-to-commit correlation) |
| `a` | Actionable plan (parallel tracks) |
| `f` | Flow matrix (cross-label dependencies) |
| `[` | Label dashboard (per-label health) |
| `]` | Attention view (label priority ranking) |

## Integration with br/bd CLIs

BV reads `.beads/issues.jsonl` in current `br` and Dolt-backed `bd` workspaces and `.beads/beads.jsonl` in legacy workspaces:

```bash
br ready --json             # Show actionable beads
br sync --flush-only        # Export current br state to JSONL
```

Legacy `bd` workflow:

```bash
bd init                    # Initialize beads in project
bd create "Task title"     # Create a bead
bd list                    # List beads
bd ready                   # Show actionable beads
bd update bd-123 --claim --json # Claim through the selected bd tracker
bd close bd-123            # Close a bead
```

## Integration with Agent Mail

Use bead IDs as thread IDs for coordination:

```
file_reservation_paths(..., reason="bd-123")
send_message(..., thread_id="bd-123", subject="[bd-123] Starting...")
```

## Graph Export Formats

```bash
bv --robot-graph                              # JSON (default)
bv --robot-graph --graph-format=dot | jq -r .graph      # Extract Graphviz DOT from JSON
bv --robot-graph --graph-format=mermaid | jq -r .graph  # Extract Mermaid from JSON
bv --robot-graph --graph-root=bd-123 --graph-depth=3  # Subgraph
bv --export-graph report.html                 # Interactive HTML
```

For JSON graphs, `.nodes` and `.edges` are counts. Records are in `.adjacency.nodes` and `.adjacency.edges`; an empty result may omit `.adjacency`.

## Time Travel

Compare against historical states:

```bash
bv --robot-insights --as-of HEAD~10        # 10 commits ago
bv --robot-insights --as-of v1.0.0         # At an existing tag
bv --robot-insights --as-of "2024-01-15"   # At a date
bv --robot-diff --diff-since HEAD~30  # Changes in last 30 commits
```

## Common Pitfalls

| Issue | Fix |
|-------|-----|
| TUI blocks agent | Use `--robot-*` flags only |
| Stale metrics | Compare data/configuration hashes, reference clock and `.status` |
| Missing cycles | Check `.status.Cycles` and `.Cycles`; skips/timeouts do not prove acyclicity |
| Need a claimable pick | Use `--robot-next`; inspect authority and typed action availability |

## Performance Notes

- Phase 1 metrics (degree, topo, density): instant
- Phase 2 uses per-metric size/density budgets; see `.analysis_config` and `.status`
- Graph-stat caches use both data and analysis-configuration hashes; readiness and rankings also depend on scope and the reference clock
- Prefer `--robot-plan` over `--robot-insights` when speed matters
