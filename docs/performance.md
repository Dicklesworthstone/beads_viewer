# Performance Tuning Guide

This guide explains `bv`'s performance characteristics, how to diagnose slow startup, and available tuning options.

## Responsiveness acceptance

A 60fps frame target allows 16.67 ms per frame. The reference-host interaction SLO is **p99 ≤50 ms** for ordinary list navigation and navigation while snapshots are built in the background. Every delivered snapshot must also take ≤50 ms on the event loop: sparse slow deliveries cannot hide below the p99 rank. These are acceptance targets, not universal guarantees. The harness times the production `Update` and `View` methods at 140 columns ×45 rows; it does not observe terminal paint or input-device latency.

`scripts/benchmark.sh latency` exercises six frozen workload families—realistic sparse graphs, deep chains, wide DAGs, clustered dense cycles, 95% closed issues, and long Unicode/Markdown text—at **1,000, 5,000, and 10,000 issues**. Seed `20260904`, fixed issue timestamps, serialized fixture hashes, binary hashes, source patch identity, Go version, and host metadata identify a run. Snapshot refresh uses real concurrent construction with the worker's size-tier configuration and previous-snapshot diff reuse. Both `SnapshotReadyMsg` and the subsequent `Phase2ReadyMsg` enter the production `Update` handler, and both handler durations count toward the measured interaction. The harness executes the actual returned command batch, including history completion, while navigation continues. Background build and command durations are recorded separately. Filesystem watcher latency is outside this measurement.

Build baseline binaries from the source state you want to compare, with this same harness present, then run on the same host:

```bash
go build -o /absolute/retained/baseline-bv ./cmd/bv
go test -c -o /absolute/retained/baseline-ui.test ./pkg/ui
# After the proposed change, choose a NEW output directory:
bash scripts/benchmark.sh latency \
  /absolute/retained/baseline-bv /absolute/retained/baseline-ui.test \
  /absolute/retained/latency-run
```

The runner retains temporary datasets, binaries, raw stdout/stderr, and samples. It runs four alternating baseline/current UI pairs, with 1,000 samples per workload and mode, and 200 alternating baseline/current CLI pairs per workload and cache mode. `BV_PERF_UI_SAMPLES` and `BV_PERF_CLI_SAMPLES` override these counts; both reject values below 200. Empirical nearest-rank p50/p95/p99 describe the observed cohorts. At 200 samples only two observations occupy the top 1%; repeated runs help expose noise but do not establish a population p99 confidence bound. Shared-host contention remains a limitation. Baseline SLO misses remain visible; the acceptance gate enforces the SLO on the current binary and requires valid samples and result parity from both.

The verifier binds every record to its expected workload, size, cache or navigation mode, seed, and actual loaded issue count. Executable hashes must match the baseline/current roles in `source.sha256`. Host, Go version, OS, architecture, and `GOMAXPROCS` must agree, as must the UI's effective analysis configuration and terminal size. Fixture identities must agree across repetitions and modes; reusing one workload's results for a different slot fails verification. Baseline and current CLI sample counts must match.

Both CLI cache modes start a fresh process. Cold runs use a previously nonexistent application cache; warm runs first populate a separate cache and verify an actual analysis entry exists. The OS page cache is uncontrolled. `SOURCE_DATE_EPOCH` is deliberately removed because its robot reproducibility mode runs metrics to completion and would change the timeout behavior under test. Fixed fixture timestamps contain no active deferrals; the production wall clock remains active.

The timed CLI comparison checks ordered decision IDs, claim/readiness fields, and metric states/reasons/approximation samples. Per-metric elapsed times are excluded; skipped or timed-out metrics remain visible and a change makes the speed comparison inconclusive. This projection does not assert exact score equality.

A separate `cli-exact` cohort compares the complete CLI JSON at `SOURCE_DATE_EPOCH=1788220800` (`2026-09-01T00:00:00Z`). Across the same 18 workloads and two cache modes, two alternating repeats yield 72 baseline/current comparisons: 144 compared invocations plus 36 warmups. Only `triage.meta.compute_time_ms` and the nine named `triage.status.<metric>.ms` fields are removed. Scores, component values, array order, source authority, timestamps, statuses, and other fields remain compared without floating-point rounding. These runs must pass alongside the timed cohorts, but receive no latency credit because their metrics run to completion. Their equivalence claim applies to the two identified executables.

UI list order and priority recommendations are compared in full, using the analyzer's existing clock seam fixed at `2026-09-01T00:00:00Z` for both preparation phases; metric timeouts remain active. Each accepted Phase2 completion records final list order, metric status, and an exact fingerprint of priorities, triage scores, and unblocks. UI records include settled setup time, allocation bytes/counts, heap before/after, GC cycles and pauses, including the background builder and measurement instrumentation. These are process statistics, not peak RSS or a memory limit.

Each refresh generation changes the final issue's title. Exact decision comparisons therefore match the same generation across runs. A faster run may complete additional generations; the verifier reports any generation observed on only one side as unpaired, with no baseline/current score-parity claim for that completion. Every delivered generation must still install its actual Phase2 result and record valid order, metric state, and handler timing.

Normal `go test` runs exercise navigation beyond the issue count, a deliberate 60 ms slow-handler rejection, missing/degraded metric controls, and real 1k/5k/10k CLI smoke cases. Complete distributions require the explicit latency runner; a skipped opt-in cohort is not performance acceptance. Search relevance is evaluated separately against judged queries; a timing pass says nothing about semantic quality.

### September 5, 2026 source verification

The complete default matrix passed on Linux amd64 host `hz3`, Go 1.25.5,
`GOMAXPROCS=64`, at 140×45 terminal cells. Four alternating UI pairs retained
288,000 observations; every current-code cohort passed the original gates.
The measured working tree is based on `7393a06b` with source-manifest SHA-256
`0446e4b826c5f0266e46db3a41a2f18dca97123e94d2346fe3b5804f6139971e`.
All 4,193 measured files stayed unchanged during the 2h41m25s run. These results
describe that source build, not a newly published release.

| Current-code observation | Worst observed value | Acceptance |
|---|---:|---|
| Per-cohort interaction p99 | 30.862 ms | All 144 cohorts ≤50 ms |
| Snapshot delivery handler | 12.255 ms | Every delivered handler ≤50 ms |
| Phase 2 completion handler | 19.050 ms | Every delivered handler ≤50 ms |
| Individual interaction | 57.616 ms | No every-interaction ≤50 ms claim |

The baseline already contained compact dependency trees and the rendering fixes;
the current side added prepared-row reuse and detached history input. The baseline
retains a 54.978 ms dense10k snapshot-handler failure. Earlier rendering/tree
comparisons have separate source identities in the
[reality-check record](planning/REALITY_CHECK_BRIDGE_PLAN_2026-09-01.md).

All 14,400 timed `--robot-triage` calls preserved ordered decisions, readiness and
metric states. The separate 144 fixed-clock calls preserved complete JSON except
the named elapsed fields. Independent review also checked all 72 warmups, raw
quantiles, executable hashes and fixture identities. This is not a CLI speedup:
22 of 36 current CLI cohorts had slower p99s, including dense10k warm at
1,478.982 ms versus 908.890 ms for its baseline.

Aggregate measured UI allocations increased from 813.09 to 817.51 GB, with 547
baseline and 555 current refresh completions. Extra generations remain unpaired;
the isolated allocation fixes do not establish lower total allocation. The whole
run's maximum resident set was 1,963,168 KiB. Configured skipped and sampled metrics
remain visible. These observations establish neither a memory cap nor physical
terminal paint, universal 60fps, population tail bounds or search relevance.

## Graph Analysis Performance

`bv` computes graph metrics in two phases, with the enabled metrics selected by configuration. The timing values below are historical rough examples without a frozen host/fixture identity; use the retained harness output for acceptance. Their computational complexity varies significantly:

| Metric | Complexity | 100 nodes | 500 nodes | 1000 nodes | 2000 nodes |
|--------|-----------|-----------|-----------|------------|------------|
| Degree | O(V) | <1ms | <1ms | <5ms | <10ms |
| TopologicalSort | O(V+E) | <1ms | <5ms | <5ms | <10ms |
| Critical Path | O(V+E) | <1ms | <5ms | <5ms | <10ms |
| PageRank | O(iter×E) | <5ms | ~20ms | ~40ms | ~100ms |
| Eigenvector | O(iter×E) | <5ms | ~15ms | ~30ms | ~70ms |
| HITS | O(iter×E) | <5ms | ~5ms | ~10ms | ~30ms |
| **Betweenness** | **O(V×E)** | ~10ms | ~300ms | **~1.3s** | **~4.6s** |
| **Cycle representatives** | SCC traversal, DFS, and sorting | varies | varies | varies | varies |

Profile the actual slow path. Betweenness can dominate graph analysis, while index I/O, Markdown rendering, and concurrent snapshot allocation can dominate other interactions.

## Two-Phase Startup Architecture

`bv` uses a two-phase startup to ensure responsive UI:

### Phase 1: Blocking (target <50ms)
Computes metrics needed for initial render:
- Degree centrality (blocking indicators)
- Topological sort (execution order)
- Basic stats (counts, density)

**Result:** The issue list can render after Phase 1, while expensive metrics continue in the background.

### Phase 2: Background (async)
Computes expensive metrics in a background goroutine:
- PageRank
- Betweenness (with timeout)
- Eigenvector
- HITS
- Cycle detection
- Critical path scoring
- k-core, articulation points, and slack

**Result:** Insights dashboard shows "Computing..." until Phase 2 completes.

## Factors Affecting Performance

### 1. Graph Size (Node Count)
- Linear algorithms (degree, topo sort) scale with V+E
- Exact betweenness cost grows with V×E
- For 2000+ nodes, betweenness can take 5+ seconds

### 2. Graph Density
```
density = edges / (nodes × (nodes - 1))
```

| Density | Classification | Impact |
|---------|---------------|--------|
| <0.01 | Sparse | Fast - most real projects |
| 0.01-0.05 | Normal | Standard performance |
| 0.05-0.15 | Dense | Betweenness may timeout |
| >0.15 | Very Dense | Consider simplifying deps |

### 3. Cycle Structure

Cycle detection uses Tarjan's strongly connected components and extracts one representative cycle per cyclic component, including self-loops. It operates on the blocking graph of open-like issues. Adjacency and representatives are sorted for deterministic output; this does not enumerate every elementary cycle.

`MaxCyclesToStore` limits the returned representatives. A truncation reason reports the stored and detected counts. A dense component can contain many more cycles than the single representative shown. Check the metric status before interpreting an empty cycle list, since detection can also be disabled or time out.

## Size-Based Algorithm Selection

`bv` automatically adjusts algorithm selection based on graph size:

### Small Graphs (<100 nodes)
- Exact betweenness; other enabled metrics retain their own convergence and availability rules
- Generous timeouts (2 seconds)
- Up to 1,000 cycle representatives

### Medium Graphs (100–499 nodes)
- Exact betweenness
- Standard timeouts (500ms)
- Cycle representative limit: 100

### Large Graphs (500–1,999 nodes)
- **Approximate betweenness** for sparse graphs (density < 0.01)
- Betweenness skipped when density ≥ 0.01
- Betweenness timeout: 500 ms; PageRank, HITS, and cycles: 300 ms
- Cycle representative limit: 50

### XL Graphs (≥2,000 nodes)
- **Approximate betweenness** (sampling-based)
- Cycle detection skipped
- HITS skipped when density ≥ 0.001
- Betweenness timeout: 500 ms; PageRank and enabled HITS: 200 ms

These are `ConfigForSize` defaults. The separate triage configuration uses 50 betweenness pivots and disables metrics it does not need. `--force-full-analysis` selects exact betweenness and enables the metrics with 30-second timeouts; it does not change representative cycle detection into full enumeration.

## Sampling-Based Betweenness Approximation

For large graphs (500+ nodes), `bv` uses a sampling-based approximation of betweenness centrality instead of the exact O(V×E) algorithm:

### How It Works
Instead of computing shortest paths from ALL nodes, we sample k pivot nodes and extrapolate:
1. Randomly select k pivot nodes
2. Compute betweenness contribution from each pivot
3. Scale up by (n/k) to estimate full betweenness

### Accuracy

Sample size alone does not establish a relative error bound or guarantee unchanged rankings. Accuracy depends on graph structure and the nodes being compared. Inspect the reported approximation status and sample count, and compare with exact analysis on suitable graphs when the decision requires it.

### Default Sample Sizes
| Graph Size | Sample Size |
|------------|-------------|
| <500 nodes | Exact under the size-based configuration |
| 500–1,999 nodes | 100 when density < 0.01; otherwise skipped |
| ≥2,000 nodes | 200 |

The sampler traverses the graph from the selected pivots instead of every node. Its speedup depends on graph shape, sample count, worker count, and allocation cost. Compare measured cohorts and preserve approximation status when evaluating a change.

## CLI Flags for Performance

### Diagnostic Flags

```bash
# Show detailed startup timing breakdown
bv --profile-startup

# Machine-readable timing (JSON)
bv --profile-startup --profile-json
```

**Historical illustrative profile (not a current output fixture or measured guarantee):**
```
Startup Profile for /path/to/.beads/beads.jsonl
================================================
Data: 847 issues, 2341 dependencies, density=0.003

Phase 1 (blocking):
  Build graph:     12ms
  Degree:           3ms
  TopoSort:         5ms
  Total Phase 1:   20ms

Phase 2 (async):
  PageRank:        45ms
  Betweenness:    312ms (timeout: NO)
  Eigenvector:     28ms
  HITS:            19ms
  Cycles:          67ms (found: 3)
  Critical Path:   11ms
  Total Phase 2:  482ms

Total startup:    502ms

Recommendations:
  ✓ Startup within acceptable range (<1s)
  ⚠ Betweenness taking 60% of Phase 2 time
    Consider: --force-full-analysis only when needed
```

### Performance Control Flags

```bash
# Force compute ALL metrics regardless of graph size
# (May be slow for large graphs - use sparingly)
bv --force-full-analysis
```

## Troubleshooting Slow Startup

### Step 1: Profile Startup
```bash
bv --profile-startup
```

Identify which phase/metric is slow.

### Step 2: Check Graph Size
```bash
bv --robot-insights | jq '.Stats | {NodeCount, EdgeCount, Density}'
```

At 500 nodes the default configuration starts sampling or skipping betweenness according to density. Inspect `.analysis_config` and `.status` for the actual choices.

### Step 3: Check for Cycles
```bash
bv --robot-insights | jq '{Cycles, status: .status.Cycles}'
```

This returns representative cycles and their computation status, not a count of every simple cycle in the graph.

### Step 4: Try Without Problem Metrics

If betweenness is the bottleneck, check whether exact, sampled, or skipped computation was selected. Size alone does not imply it will be skipped.

If cycles are the bottleneck:
- Review your dependencies for circular patterns
- Inspect the source issues and correct the unintended blocking dependency using that repository's tracker.

### Step 5: Report Issues

If startup is slow and profiling shows unexpected behavior:
```bash
bv --profile-startup --profile-json > profile.json
```

Include `profile.json` in your bug report.

## Historical Startup Targets

| Graph Size | Target Startup |
|------------|----------------|
| <100 nodes | <100ms |
| 100-500 nodes | <300ms |
| 500-1000 nodes | <500ms |
| 1000-2000 nodes | <1s |
| >2000 nodes | <2s |

These historical targets refer to Phase 1 startup, not a measured guarantee or a bound on full robot-command execution. Phase 2 completes asynchronously in the TUI. The current navigation acceptance and required distributions are defined at the top of this guide.

## Best Practices

### For Project Maintainers

1. **Keep dependency graphs sparse**
   - Only create blocking dependencies where truly needed
   - Use `related` type for informational links

2. **Avoid circular dependencies**
   - Cycles indicate design issues
   - Break cycles before they accumulate

3. **Monitor graph density**
   - Healthy: <0.05
   - Warning: >0.15

### For AI Agents

1. **Use robot flags for programmatic access**
   - `--robot-insights` for metrics
   - `--robot-plan` for actionable items

2. **Check for timeouts in robot output**
   - Timeout flags indicate metrics may be incomplete
   - Design agents to handle partial data gracefully

3. **For large repositories**
   - Use `--robot-plan` for immediate actionable items
   - Avoid forcing full analysis unless needed

## Timeout Configuration

The following metrics have per-metric timeouts chosen by graph size (`ConfigForSize` in `pkg/analysis/config.go`); `BV_PHASE2_TIMEOUT_S` overrides their enabled timeouts:

| Algorithm | < 100 nodes | < 500 nodes | < 2,000 nodes | ≥ 2,000 nodes | Rationale |
|-----------|-------------|-------------|---------------|---------------|-----------|
| Betweenness | 2s (exact) | 500ms (exact) | 500ms (sampled; skipped when density ≥ 0.01) | 500ms (sampled) | O(V×E) can be seconds |
| PageRank | 2s | 500ms | 300ms | 200ms | Usually fast, defensive |
| HITS | 2s | 500ms | 300ms | 200ms (only when density < 0.001, else skipped) | Usually fast, defensive |
| Cycle representatives | 2s | 500ms | 300ms | skipped | Bounds waiting for SCC/DFS results |

When a timeout triggers, the metric is marked unavailable; inspect its status and reason instead of treating an empty result as successful computation. A timeout bounds waiting for the result and does not guarantee the worker stops immediately. In robot mode, a valid `SOURCE_DATE_EPOCH` requests reproducible output by running enabled metrics to completion; that mode is excluded from the timeout-sensitive CLI latency cohorts.

## Advanced: Memory Considerations

For very large graphs:

1. **Cycle detection retains representatives**
   - Each representative stores its path
   - `MaxCyclesToStore` limits returned paths; it is not a bound on all traversal allocations

2. **Graph structure** uses a compact adjacency representation
   - Sparse graphs generally require less storage than dense graphs
   - Issue text, indexes, derived metrics, and concurrent snapshots also consume memory

3. **Measure the whole workload**
   - The latency harness reports allocated bytes, heap snapshots, and GC activity
   - Use a host profiler for peak RSS; these counters are not interchangeable
   - Issue count alone cannot establish a memory bound, especially with long descriptions or overlapping snapshots

## Benchmarking

Run the benchmark suite to measure performance on your hardware:

```bash
# Run all benchmarks
./scripts/benchmark.sh

# Save baseline
./scripts/benchmark.sh baseline

# Compare after changes
./scripts/benchmark.sh compare

# Quick benchmarks (CI mode)
./scripts/benchmark.sh quick
```

See `benchmarks/` directory for detailed results.
