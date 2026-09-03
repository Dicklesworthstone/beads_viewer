# Alert Checks

**Proactive checks** (run on the current graph, no baseline needed):

| Type | Trigger | Severity | `.bv/drift.yaml` keys (default) |
|------|---------|----------|----------------------------------|
| `stale_issue` | No activity for `stale_warning_days` (warning) or `stale_critical_days` (critical); thresholds are multiplied by `in_progress_stale_multiplier` for `in_progress` issues; `label_overrides` can tighten or loosen per label | Warning / Critical | `stale_warning_days` (14), `stale_critical_days` (30), `in_progress_stale_multiplier` (0.5) |
| `blocking_cascade` | Actionable issue unblocks N+ others | Info / Warning | `blocking_cascade_info_threshold` (3), `blocking_cascade_warning_threshold` (5) |
| `high_impact_unblock` | Actionable issue unblocks N+ others of which at least one is P0/P1 (two or more urgent items escalate to warning) | Info / Warning | `high_impact_unblock_min` (3), `high_impact_priority_max` (1) |
| `abandoned_claim` | An `in_progress` issue with an assignee idle longer than `stale_warning_days` x `in_progress_stale_multiplier` x `abandoned_claim_multiplier` (14 days by default) | Warning | `abandoned_claim_multiplier` (2) |
| `potential_duplicate` | Two open issues whose title/description keyword Jaccard similarity reaches the threshold (same detector as `--robot-suggest`); closed issues are never paired | Info | `duplicate_jaccard_threshold` (0.7), `duplicate_max_alerts` (10) |
| `priority_mismatch` | `--robot-priority` recommends a *higher* priority with confidence at or above the floor (downgrade suggestions stay in `--robot-priority`) | Warning | `priority_mismatch_min_confidence` (0.6) |
| `velocity_drop` | Closes in the last window fell by the percentage or more versus the previous window, which must contain at least the baseline count of closes | Warning | `velocity_drop_pct` (50), `velocity_window_days` (7), `velocity_min_baseline` (5) |

**Drift checks** (compare the current graph with the baseline saved by `bv --save-baseline`):

| Type | Trigger | Severity | `.bv/drift.yaml` keys (default) |
|------|---------|----------|----------------------------------|
| `new_cycle` | A cycle exists that the baseline did not have | Critical | (always on unless disabled) |
| `density_growth` | Graph density up by the info or warning percentage | Info / Warning | `density_info_pct` (20), `density_warning_pct` (50) |
| `node_count_change` | Node count changed by the percentage or more | Info | `node_growth_info_pct` (25) |
| `edge_count_change` | Edge count changed by the percentage or more | Info | `edge_growth_info_pct` (25) |
| `scope_creep` | Open-issue count grew by the percentage or more since the baseline | Info | `scope_creep_pct` (20) |
| `blocked_increase` | N or more additional blocked issues | Warning | `blocked_increase_threshold` (5) |
| `actionable_change` | Actionable count down by the warning percentage, or changed by the info percentage | Info / Warning | `actionable_decrease_warning_pct` (30), `actionable_increase_info_pct` (20) |
| `pagerank_change` | A top-metric issue's PageRank moved by the percentage or more | Warning | `pagerank_change_warning_pct` (50) |
