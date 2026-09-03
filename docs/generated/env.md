# Environment Variables

| Variable | Description | Default |
|:---|:---|:---|
| `BEADS_DB` | Path to a beads database file or `.beads` directory. Overrides `BEADS_DIR`; overridden by `--db`. | (unset) |
| `BEADS_DIR` | Custom beads directory path. When set, overrides the default `.beads` directory lookup. | `.beads` in cwd |
| `BV_BACKGROUND_MODE` | Startup default for the background snapshot worker (`1` on, `0` off). At runtime the TUI promotes itself to the worker after any synchronous reload that takes 1 s or longer; `0` pins synchronous reload and disables that promotion. | sync at startup, auto-promote after a slow reload |
| `BV_BUILD_HYBRID_WASM` | Set to `1` to build the hybrid search WASM scorer during `--export-pages` (requires wasm-pack). | (skip) |
| `BV_CACHE_DIR` | Base directory for the disk caches (`analysis_cache/` and correlation caches live under it). | `<user cache dir>/bv` |
| `BV_DEBOUNCE_MS` | Debounce window (milliseconds) for live reload events in background mode. | `200` |
| `BV_DEBUG` | Any value: write `[BV_DEBUG]` diagnostics to stderr. | (off) |
| `BV_FORCE_POLL` | Alias for `BV_FORCE_POLLING`. | (auto) |
| `BV_FORCE_POLLING` | Force polling-based live reload (useful on NFS/SMB/SSHFS/FUSE or any setup where filesystem events are unreliable) (`1`/`0`). | (auto) |
| `BV_FRESHNESS_STALE_S` | Snapshot staleness critical threshold (seconds). | `120` |
| `BV_FRESHNESS_WARN_S` | Snapshot staleness warning threshold (seconds). | `30` |
| `BV_HEARTBEAT_INTERVAL_S` | Background worker heartbeat interval (seconds). | `5` |
| `BV_INSIGHTS_MAP_LIMIT` | Cap on the number of entries in each `--robot-insights` metric map. | (all) |
| `BV_MAX_LINE_SIZE_MB` | Max JSONL line size in MB (lines larger than this are skipped with a warning). Applies to the TUI, the background worker, and robot loads. | `10` |
| `BV_METRICS` | Set to `0` to disable internal timing metrics collection (`--robot-metrics`). | (enabled) |
| `BV_NO_BROWSER` | Any value: never open a browser after exports or deployments. | (unset) |
| `BV_NO_CACHE` | Set to `1` to bypass the robot analysis and correlation disk caches (`--no-cache` sets it). | (cache on) |
| `BV_NO_GITIGNORE` | Disable automatic ignore-file management for `.bv/` entirely (any non-empty value). See [Automatic `.bv/` ignore handling](#automatic-bv-ignore-handling). | (enabled) |
| `BV_NO_SAVED_CONFIG` | Any value: the `--pages` wizard ignores the saved deployment configuration. | (unset) |
| `BV_NO_UPDATE_CHECK` | Set to `1` to skip the TUI's startup release check (`updates: {check: false}` in `~/.config/bv/config.yaml` does the same); explicit `--check-update` / `--update` still work. | (check on) |
| `BV_OUTPUT_FORMAT` | Default robot output format: `json` or `toon` (overridden by `--format`). | `json` |
| `BV_PHASE2_TIMEOUT_S` | Override per-metric Phase 2 timeouts (seconds). | (size-based) |
| `BV_PRETTY_JSON` | Set to `1` for indented JSON output. | (compact) |
| `BV_ROBOT` | Set to `1` to force robot mode (clean stdout, JSON logs, disk cache on). Every `--robot-*` flag sets it. | (unset) |
| `BV_ROBOT_HISTORY_TIMEOUT_MS` | Bound on the git-history prologue of `--robot-triage` in milliseconds; `0` = unbounded. | `10000` |
| `BV_ROBOT_NOT_READY_LABELS` | Comma-separated labels marking a bead not-ready; excluded from claimable `--robot-next`/`--robot-triage` top picks (`--robot-not-ready-labels` overrides). | (none) |
| `BV_SEARCH_MODE` | Default search mode: `text` or `hybrid` (`--search-mode` overrides). | `text` |
| `BV_SEARCH_PRESET` | Default hybrid preset: `default`, `bug-hunting`, `sprint-planning`, `impact-first`, `text-only`; setting one implies hybrid mode. | `default` |
| `BV_SEARCH_WEIGHTS` | JSON weight map for hybrid search; overrides the preset. | (preset) |
| `BV_SEMANTIC_DIM` | Embedding dimension for the hashed search index. | `384` |
| `BV_SEMANTIC_EMBEDDER` | Embedding provider for `bv --search` and TUI search. Only `hash` (FNV-1a keyword feature hashing) is implemented; `python-sentence-transformers` and `openai` are reserved names that fail with "not implemented". | `hash` |
| `BV_SEMANTIC_MODEL` | Model name for a future non-hash provider; ignored by `hash`. | (empty) |
| `BV_SKIP_PHASE2` | Skip Phase 2 graph metrics (centrality, cycles, critical path) (`1`/`0`). | (disabled) |
| `BV_TEST_MODE` | Any value: test harness mode; suppresses browser opening, terminal capability queries, and the background worker's idle GC tuning. | (unset) |
| `BV_THEME` | Pin the TUI palette: `light` or `dark` (overridden by `--theme`). | (auto-detect) |
| `BV_TUI_AUTOCLOSE_MS` | Quit the TUI automatically after this many milliseconds (for automated tests). | (unset) |
| `BV_UPDATE_USE_TOKEN` | Set to `1` to let the update check and `--update` send the ambient `GITHUB_TOKEN` / `GH_TOKEN` to api.github.com (`updates: {use_token: true}` in config.yaml does the same). | (never sent) |
| `BV_WATCHDOG_INTERVAL_S` | Background worker watchdog interval (seconds). | `10` |
| `BV_WORKER_LOG_LEVEL` | Log level for the background snapshot worker. | (default) |
| `BV_WORKER_METRICS` | Truthy value: the background worker records its own metrics. | (off) |
| `BV_WORKER_TRACE` | Path to a trace file the background worker appends to. | (off) |
