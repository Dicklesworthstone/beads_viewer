# Changelog

All notable changes to **Beads Viewer (`bv`)** are documented here. Versions are listed newest-first. Each entry links to the tagged commit on GitHub. Where a version was published as a GitHub Release (with binaries), it is marked accordingly; tag-only versions are noted as such.

Scope window: this update reconstructs `v0.21.0` through `v0.21.2`; the earlier version history is
retained below.

## Release Timeline

| Version | Date | Publication | Orientation |
|---|---|---|---|
| [`v0.23.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.23.0) | 2026-09-04 | GitHub Release | Reality Check hardening sweep, 10-stage release gate, proactive drift alerts, typed env registry, docgen, and full tracker completion. |
| [`v0.22.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.22.0) | 2026-08-25 | GitHub Release | Makes snapshot delivery pointer-based and incrementally rebuilds safe list changes, with measured UI latency and allocation reductions. |
| [`v0.21.2`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.21.2) | 2026-08-24 | GitHub Release | Publishes the 50-pass performance campaign, verified binaries, checksums, SBOM, and corrected Nix guidance. |
| [`v0.21.1`](https://github.com/Dicklesworthstone/beads_viewer/tree/v0.21.1) | 2026-08-24 | Tag only | Staged the performance and license work; superseded before binary publication. |
| [`v0.21.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.21.0) | 2026-08-23 | GitHub Release | Strict robot-count semantics, bounded history liveness, theme selection, and cache-path repairs. |

---

## [Unreleased]

---

## [v0.23.0] -- 2026-09-04 (Release)

### Reality check 2026-09 (bridge plan `docs/planning/REALITY_CHECK_BRIDGE_PLAN_2026-09-01.md`)

- **Data sources:** discovery only reads issue-file names from the loader allowlist (no more `sync_base.jsonl` shadowing), probe warnings are buffered and only surface for the source actually used, and every robot payload names its `source_path` / `source_kind` plus `as_of` / `scope` in one shared envelope.
- **Robot registry:** five handlers that ignored `--label` / `--recipe` / `--repo` / `--as-of` now honour them; `--robot-file-hotspots` moved into the registry and roughly 1,400 lines of unreachable inline handler copies were deleted from `cmd/bv/main.go`, along with the never-imported `pkg/beadscli` package; `--robot-help` is generated from the registries.
- **Feedback loops:** `--feedback-*` weights change `--robot-triage` scoring (after three samples), and correlation confirm/reject changes `--robot-history`, the commit index, `--robot-explain-correlation`, and the History view.
- **Correlation:** explicit-ID and temporal strategies run alongside co-commit; the artifact cache is format-versioned; `--robot-orphans` reports the scanned window and beads-only commit count.
- **Sprints and alerts:** four-signal at-risk detection shared by the dashboard and `--robot-burndown` (`at_risk`), a scope-aware ideal line, `P` opens the dashboard; every declared alert type has an emitter (`velocity_drop`, `high_impact_unblock`, `abandoned_claim`, `potential_duplicate`) plus new `priority_mismatch` and `scope_creep`, each with a `suggested_action`, labels for `--alert-label`, a `proactive_max_issues` cap with `skipped_checks`, and every threshold documented from `.bv/drift.yaml`.
- **TUI:** attention view with cursor and drilldown, tutorial progress persisted, `Shift+Tab` / `n` `N` / `t` bindings, startup update check opt-out (`BV_NO_UPDATE_CHECK`).
- **Workspaces and recipes:** `.bv/workspace.yaml` is auto-discovered when no `.beads` is reachable; recipes load from `.beads/recipes/*.yaml` and `--recipe` accepts a file path.
- **Release gate:** `scripts/release_gate.sh` (gofmt, build+vet, `-race` unit and e2e, docs parity, action pins, vendor hashes, benchmark compare, robot smoke, and the gate's own script self-tests) with `scripts/check_action_pins.sh`, `scripts/robot_smoke.sh`, `scripts/verify_vendor.sh`, a vendored-asset `MANIFEST.json` and `docs/PROVENANCE.md`; `ci.yml` runs the gate; `scripts/verify_isomorphic.sh` builds the baseline in a detached worktree instead of stashing the caller's tree.
- **Release archives (#195):** `.goreleaser.yaml` now names archives `bv_<version>_<os>_<arch>.<ext>`; `bv --update` prefers the versioned name and still accepts the unversioned form older releases used; `install.sh` selects by platform so it handles both; README's direct-download section points at the release page and `checksums.txt` instead of moving `latest` links.
- **Dashboard CSP (#197 residue):** the exported dashboard's `script-src` no longer allows `'unsafe-inline'`: the four inline bootstrap scripts moved into `head_init.js` and the top of `viewer.js`, `'wasm-unsafe-eval'` is declared for sql.js and the graph WASM, and `bv --preview-pages` serves its live-reload script as `/__preview__/livereload.js` instead of injecting an inline block. `'unsafe-eval'` remains because the vendored Alpine build evaluates `x-*` expressions with `Function()`; switching to Alpine's CSP build is the remaining step. Guards: `TestEmbeddedIndex_CSPHasNoInlineScripts` (also checks every referenced asset is embedded) and the e2e export check. Verified in a headless Chromium with `scripts/dashboard_browser_smoke.sh`: no refusals, database, WASM graph engine, charts, and triage all boot, and a planted inline script is blocked while the app still runs.
- **Hardening sweep, `pkg/analysis` (from `wip/fresh-eyes-20260826`):** 37 files landed after a per-file rebase behind the gate: exact issue-ID matching in dependency suggestions (`bv-42` no longer matches inside `bv-420`), shell-quoted bead IDs in suggested `br` commands, cycle detection that reports truncation instead of silently capping, readiness-after-completions helpers shared by plan and priority, config caps normalised to defaults, and the cache refusing to serve incomplete Phase 2 results. Two tests on that branch were broken on the branch itself (a `DeferUntil` pointer aliased into the expected value; a cache-version literal not bumped) and are fixed here; the branch's asynchronous cache publish raced a synchronous second `Analyze`, which now stores before returning. The remaining packages of the branch are triaged in tracker item H4.
- **Benchmark gate (stage 8):** `scripts/benchmark.sh` now runs ten tracked benchmarks against the frozen `tests/testdata/benchmark/medium.jsonl` (never the live tracker), writes `benchmarks/baseline.txt` with a provenance header (date, Go, CPU, OS, commit, dataset hash), and compares the best observed `ns/op` per benchmark with a built-in comparator (benchstat optional); `tests/scripts/benchmark_compare_test.sh` proves it turns red on a doubled median and a missing benchmark. `compare` judges HEAD against a fresh run of the baseline commit built in a detached worktree minutes earlier on the same machine, so host drift on a shared VM no longer reads as a regression; the stored baseline is the fallback.
- **Key registry decision (tracker item B9):** the TUI `KeyRegistry` is the help index only; its never-called dispatch surface (`Dispatch`, `RegisterView`, `Handler`, `BindingsCount`, `Clear`) is gone. Keys that worked but were undocumented (`E`, `f`, `!`, `w`, `s`, `S` in the list; `H`, `L`, `s` on the board; `E` in the tree) are now in `GetKeyBindingDocs`, the sidebar, and the README, with tests driving each through `Update`.
- **Windows installer (#197 finding 3):** `install.ps1` now downloads the release zip and `checksums.txt`, verifies SHA-256 with `Get-FileHash`, and refuses a missing or mismatching checksum before anything reaches the install directory; Go is no longer required (`-FromSource` keeps a build pinned to the resolved tag). `tests/scripts/install_ps1_test.sh` runs it under pwsh against a local fake release: verified install, tampered checksum refused, missing checksums refused, `-Version` pin. README pins the piped form to the reviewed commit.
- **Hardening sweep, `pkg/loader` and `pkg/workspace` (from `wip/fresh-eyes-20260826`, pass 3):** `.beads/redirect` following exposed as `ResolveBeadsDir`/`ResolveBeadsDirWithTrace`, the issues file opened only after a same-file check, `bd export` refreshes run with an absolute `BEADS_DIR` and without an ambient `BEADS_DB`, and the workspace aggregate loader reports dropped records per repository, routes parse warnings safely in robot mode, and rejects cross-repository ID collisions; over-limit lines are counted before their warning fires so handlers see consistent stats.
- **Hardening sweep, `pkg/search` (from `wip/fresh-eyes-20260826`, pass 4):** a stored vector index whose dimension does not match the embedder is backed up and rebuilt instead of being served as a hit; `NewHybridScorerAt` pins the recency clock; normalizer and query-adjustment fixes land with their tests. Main's stricter vector-index validation is kept. The branch's `internal/datasource` slice is retired: the allowlist and silent-probing design already on main replaced it.
- **Graph WASM rebuild (#197 finding 8):** `scripts/build_graph_wasm.sh` pins the rebuild without `wasm-pack` (cargo for `wasm32-unknown-unknown`, a `wasm-bindgen` CLI that must match the crate version in `Cargo.lock`, `wasm-opt -Os`) and prints built and vendored hashes with tool versions; `docs/PROVENANCE.md` records that the comparison is still owed and why.
- **Agent blurb v5:** the ready-made AGENTS.md block now says that `--graph-format=dot|mermaid` returns the diagram text in the `graph` field of the JSON envelope. The version marker moved from v4 to v5 so `bv --agents-update` refreshes installed blocks (`--agents-add` compares versions, not content); this repository's AGENTS.md and the README copy were regenerated with the tool.
- **Decisions recorded:** no path-matching correlation strategy (README diagram and prose agree); downgrade priority recommendations are not alerts; `cycle_introduced` is documented as `new_cycle`.
- **Environment registry (`internal/env`):** Centralizes all 41 `BV_*` and `BEADS_*` environment variables in a single package with typed accessors (`BV_NO_COLOR`, `BV_TEST_MODE`, `BV_LOG_FORMAT`, `BV_SEARCH_MODE`, etc.) and an AST-walking vet test guaranteeing zero raw `os.Getenv` / `os.LookupEnv` calls in production code.
- **Documentation generator (`internal/docgen`):** Emits living reference documentation (`docs/generated/{flags,env,alerts,recipes,presets,keys,sort_modes}.md` and `constants.json`) and synchronizes tables into `README.md` via `go generate` / `bv --generate-docs`.
- **Milestone completion:** All 615 tracking beads and epics closed (100% completion across graph analysis, drift detection, TUI, search, correlation, and the 10-stage release gate).
- **Tracker recovery (2026-09-02):** `.beads/beads.db` was at schema 0 and rejected by br 0.5.7 (`SCHEMA_MISMATCH expected 17, found 0`); the JSONL was harmonized (empty-string fields dropped, dependency `metadata` / `thread_id` added), a fresh DB was rebuilt from it and promoted, and the old DB was renamed aside (`beads.db.bad_20260902T030027Z`) rather than deleted. With the maintainer's written approval later that day the renamed DB/WAL/SHM and the two rebuild `*.fsqlite-migration-state` markers were removed; the `recovery_20260902T023914Z/` snapshot (git-ignored) is the one leftover, kept for a recursive removal from the maintainer's own shell.

### Fixed

- SQLite-backed reloads (Ctrl-R / F5 and file-watch refreshes) failed on Windows with
  `cannot connect to database: SQL logic error: invalid uri authority: E:%5C...`. The read-only
  DSN was built with `net/url`, which turns a drive-letter path (or any relative path) into
  `file://E:%5C...`, putting the first path segment in the URI authority slot. The DSN path is
  now absolute and slash-normalized (`file:///E:/...`) on every platform (#198).

---

## [v0.22.0] -- 2026-08-25 (Release)

This release finishes the responsive-snapshot workstream: Bubble Tea now keeps the large UI model
behind a pointer, snapshot installation reuses model-owned buffers, and small content-only changes
rebuild only the affected list items. The fast path is guarded by deterministic fingerprints and
falls back to a full rebuild whenever graph topology, recipe membership, sort order, or more than
20% of issues changes.

### Delivered capability: Faster UI Updates

- **Stop copying the entire model on every message.** The TUI model and its hot navigation helpers
  now use pointer receivers, eliminating the roughly 198 KB interface-boxing copy that previously
  accompanied each `Update`. On the 1,000-issue snapshot-swap benchmark, the measured handler moved
  from **105-225 us** before this work to **4.076-4.284 us**, a **24.5x-55.2x latency reduction**;
  bytes per operation fell by about **99.8%**. See
  [`96029793`](https://github.com/Dicklesworthstone/beads_viewer/commit/96029793) and
  [`1a90b016`](https://github.com/Dicklesworthstone/beads_viewer/commit/1a90b016).
- **Keep interactive latency inside the frame budget.** Five update-only keypress runs measured
  p99 at **148-160 us**. Three isolated update-plus-render runs measured p99 at
  **33.05-35.54 ms**, below the 50 ms interaction target. Isolated GC validation measured maximum
  pauses of **0.879-1.374 ms**. The benchmarks live with the code in
  [`5aac8532`](https://github.com/Dicklesworthstone/beads_viewer/commit/5aac8532) and the broader
  regression coverage in [`d5a1be08`](https://github.com/Dicklesworthstone/beads_viewer/commit/d5a1be08).
- **Precompute immutable view inputs off the UI loop.** Snapshots now carry list-model items,
  semantic-search documents, alert summaries, graph data, and stable ID/order indexes so delivery
  does not reconstruct these structures during rendering. See
  [`b24297fc`](https://github.com/Dicklesworthstone/beads_viewer/commit/b24297fc).

### Delivered capability: Correct Incremental List Rebuilds

- **Detect changes deterministically.** Issue fingerprints distinguish nil from present zero-value
  fields, ignore nil dependency entries, canonically order tied comments, and classify simultaneous
  content and dependency changes exactly once. See
  [`16ed4342`](https://github.com/Dicklesworthstone/beads_viewer/commit/16ed4342).
- **Reuse unchanged work only when it is safe.** A change at or below the 20% threshold uses the
  incremental list path only when recipe identity, membership, order, and dependency topology are
  unchanged. Additions, removals, dependency edits, recipe changes, reordered results, and larger
  diffs automatically take the full path. Incremental and full results are compared directly in
  tests, including recipe and topology fallbacks.
- **Measured result:** on an AMD EPYC-Milan worker, five paired 1,000-item runs reduced one-change
  list construction from **374.9-412.4 us** to **189.1-212.6 us** (**1.76x-2.18x faster**).
  Allocations fell from **1,001 to 2 per operation** (**99.8% fewer**). The remaining approximately
  516 KB/op is the immutable output slice itself and is not claimed as eliminated.

### Reliability, Integration, and Operator Improvements

- Make background-worker cancellation and recovery ownership thread-safe, with lifecycle and idle-GC
  coverage in [`2a2ec14e`](https://github.com/Dicklesworthstone/beads_viewer/commit/2a2ec14e) and
  [`3e6473bb`](https://github.com/Dicklesworthstone/beads_viewer/commit/3e6473bb).
- Copy interactive graph node descriptions without mutating Beads, including clipboard fallbacks
  for offline dashboards ([`f2e6c9da`](https://github.com/Dicklesworthstone/beads_viewer/commit/f2e6c9da)).
- Generate tracker-neutral v4 agent guidance with current `bd` and `br` command families, backed by
  integration coverage ([`b9ae1472`](https://github.com/Dicklesworthstone/beads_viewer/commit/b9ae1472)).
- Keep fallback and Nix version sources aligned through a release invariant test
  ([`70adf7f8`](https://github.com/Dicklesworthstone/beads_viewer/commit/70adf7f8)).

### Verification Boundary

- Passed `go build ./...`, `go vet ./...`, the complete analysis package, focused incremental race
  tests (three repetitions), and all non-environment-sensitive packages through RCH.
- The RCH environment runs as root, rewrites the working directory, and lacks usable VCS metadata;
  seven permission/cwd/timing tests were therefore excluded from the aggregate remote run after
  their failure modes were reproduced and classified. The E2E package passed separately with
  `GOFLAGS=-buildvcs=false` propagated into its nested build.

### Added

- **Copy graph node descriptions without mutating Beads.** The exported interactive graph's
  right-click menu can now copy either a node ID or its raw description. Both actions share a
  clipboard fallback for local/offline dashboards and report empty descriptions or copy failures
  instead of silently doing nothing.
- **Generate correct agent guidance for both Beads trackers.** `bv --agents-add` now installs a
  tracker-neutral v4 blurb with separate, current `bd` and `br` command families, rather than
  telling Go Beads workspaces to mutate their tracker with `br`.

---

## [v0.21.2] -- 2026-08-24 (Release)

Published release: [GitHub Release v0.21.2](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.21.2).
This release publishes the profile-driven work staged under the tag-only `v0.21.1`, plus the
final Nix packaging correction. The campaign completed all 50 requested iterations, but credits
only improvements that survived command-level measurement, output-equivalence checks, focused
causal tests, and the full Go verification suite.

### Delivered capability: Faster Cold Robot Triage

- **Profile the real bottleneck first.** On the pinned 540-issue / 19-open Git fixture, history
  correlation accounted for 62.26% of command CPU and snapshot extraction for 58.49%. Within that
  path, profiles attributed roughly 100 ms to large allocations, 70 ms to clearing memory, and
  70 ms to background garbage collection. This ruled out speculative graph and JSON tuning as the
  first lever.
- **Recycle a blob only after its last reader is gone.** Snapshot record sets contain slices that
  point directly into their Git blob buffer, so reusing a live buffer would silently corrupt
  history. The extractor already knew each blob's last use. Commit
  [`22305d12`](https://github.com/Dicklesworthstone/beads_viewer/commit/22305d1208d2d17671f34beb480b68489fcb4a1c)
  made that lifetime explicit: evict the record set first, then place its backing buffer in a
  one-slot, largest-capacity spare owned by the streaming `git cat-file --batch` reader. The next
  blob re-slices that storage when it fits instead of allocating and zeroing another large byte
  array. Only one spare is retained, so reuse stays bounded rather than becoming a memory cache.
- **Preserve the algorithm, remove allocation churn.** Git object order, the one-response-at-a-time
  protocol, line identity, event ordering, and the existing two-to-three-snapshot live window from
  [#182](https://github.com/Dicklesworthstone/beads_viewer/issues/182) did not change. Four-snapshot
  lifetime tests prove that two live blobs never alias and that only an
  explicitly recycled buffer is reused. A real-history differential reproduced all 1,776 legacy
  events, and 20 independently normalized robot outputs were identical at SHA-256
  `5992ff99901b1c5abf8ccf3b1a9e3d2490a6f0eda1742cad938e7b3ff9809918`.
- **Measured result:** ten independent low-load, interleaved cold-cache pairs reduced mean user CPU
  from **0.647 s to 0.573 s (11.44%)**, with 10/10 wins and exact sign `p=0.00098`. Five traced
  pairs reduced mean GC cycles from **25.8 to 13.0 (49.61%)**. Wall time improved 2.26% but missed
  significance (`p=0.0547`), while peak RSS increased 0.177%; therefore this release claims lower
  CPU and GC work, **not** lower wall latency or memory usage.

### Delivered capability: Reused Graph Analysis

- **What-if batches:** [`4b685960`](https://github.com/Dicklesworthstone/beads_viewer/commit/4b685960)
  added a stats-consuming path so `--robot-insights` can pass the completed `GraphStats` it already
  owns. Previously a planted 540-issue batch performed 540 redundant analysis-cache decodes through
  `TopWhatIfDeltas -> computeWhatIfDelta -> Analyze`. The focused same-worker benchmark moved from
  **4.4108 s / 1.648 GB / 8,530,586 allocations** to **3.203–16.564 ms / 1.444–4.931 MB /
  8,504–26,341 allocations**. This is a scoped batch benchmark, not a whole-command latency claim.
- **Cycle-break insights:** [`5b73a6b6`](https://github.com/Dicklesworthstone/beads_viewer/commit/5b73a6b6)
  routes the handler's completed statistics into advanced-insight generation instead of launching
  another analysis merely to recover the same cycle list. Sentinel-cycle tests prove the supplied
  statistics control the result, while nil callers retain the ordinary analyze-on-demand fallback.
- **Direct dependency decoding:** [`a83bf01c`](https://github.com/Dicklesworthstone/beads_viewer/commit/a83bf01c)
  reduced a direct-loader benchmark by 13.00% and allocations by 33.63% while replaying malformed
  and invalid-UTF-8 inputs through the standard library for exact behavior. It did not improve the
  dominant cold-correlation command path, so it is not included in the 11.44% release claim.

### Closed workstreams: Measurement and Negative Evidence

- Fixed registry-backed CPU-profile shutdown in
  [`854a8070`](https://github.com/Dicklesworthstone/beads_viewer/commit/854a8070) and
  [`0a0838ec`](https://github.com/Dicklesworthstone/beads_viewer/commit/0a0838ec), ensuring profiling
  completes before robot dispatch exits instead of leaving zero-byte or truncated profiles.
- Ran 50 bounded optimization passes. PGO, `GOAMD64=v3`, fixed GC pacing, smaller transport
  buffers, direct event fusion, prefetch windows, alternate record tables, and other promising
  candidates were rejected when end-to-end confidence intervals crossed zero, system CPU or tails
  regressed, memory worsened, or the active profile placed too little cost in the proposed seam.
  These results and retry conditions remain in the pinned
  [hotspot table](https://github.com/Dicklesworthstone/beads_viewer/blob/v0.21.2/tests/artifacts/perf/HOTSPOT_TABLE.md)
  and [negative-evidence ledger](https://github.com/Dicklesworthstone/beads_viewer/blob/v0.21.2/tests/artifacts/perf/HYPOTHESIS_LEDGER.md).
- Retained snapshot frontier/allocation refinements have differential coverage, but no additional
  release-wide percentage is assigned to them. The only accepted cold-triage result from this
  campaign is buffer reuse's 11.44% user-CPU and 49.61% GC-cycle reduction.

### Representative commits

- [`22305d12`](https://github.com/Dicklesworthstone/beads_viewer/commit/22305d1208d2d17671f34beb480b68489fcb4a1c)
  — recycle evicted Git blob buffers on the measured cold-correlation path.
- [`4b685960`](https://github.com/Dicklesworthstone/beads_viewer/commit/4b685960) — consume
  completed graph statistics across what-if batches.
- [`5b73a6b6`](https://github.com/Dicklesworthstone/beads_viewer/commit/5b73a6b6) — reuse
  completed cycle data in advanced insights.
- [`854a8070`](https://github.com/Dicklesworthstone/beads_viewer/commit/854a8070) and
  [`0a0838ec`](https://github.com/Dicklesworthstone/beads_viewer/commit/0a0838ec) — make robot
  CPU profiles complete and trustworthy.

### Fixed and Packaged

- Document the explicit Nix unfree-package opt-in required by the project's OpenAI/Anthropic
  license rider, keeping the published flake instructions consistent with its corrected nonfree
  metadata.
- Publish five platform archives with individual SHA-256 sidecars, aggregate checksums, an SPDX
  SBOM, and the DSR build manifest. Publicly downloaded macOS and Windows binaries reported
  `bv v0.21.2`; the Go module proxy resolved `v0.21.2` to the tagged source commit.

---

## [v0.21.1] -- 2026-08-24 (Tag only)

Staging tag only; no GitHub Release or binary assets were published for `v0.21.1`. Its performance,
profiling, differential-test, and license-metadata changes were published immediately afterward as
the `v0.21.2` release and are documented above. The version bump was necessary because the
`v0.21.1` tag was already immutable when the final Nix usage correction landed.

---

## [v0.21.0] -- 2026-08-23 (Release)

### Changed — **BREAKING (robot JSON semantics)**

- **Strict count semantics in triage output (#165).** `quick_ref.open_count` /
  `project_health.counts.open` now count ONLY issues whose status is exactly `open` (previously:
  every non-closed issue, i.e. `open`+`in_progress`+`blocked`+`deferred`), and
  `quick_ref.blocked_count` / `counts.blocked` now count ONLY issues whose status is exactly
  `blocked` (previously: every non-closed, non-actionable issue). Both now always equal the
  corresponding `counts.by_status` entries. The legacy aggregates are preserved under
  semantically accurate names: `not_closed_count` / `counts.not_closed` (old `open_count`) and
  `not_actionable_count` / `counts.dependency_blocked` (old `blocked_count`), with the partition
  invariant `not_closed == actionable + not_actionable`. Consumers that relied on
  `counts.open` meaning "non-closed" must switch to `not_closed`; consumers that relied on
  `counts.blocked` meaning "dependency-blocked" must switch to `dependency_blocked` /
  `not_actionable_count`. This also aligns the triage counts with the bundled viewer's strict
  status tiles.

### Changed

- **Better `.gitignore` handling (#179, revisiting #34/#151).** `bv` no longer unconditionally
  appends `.bv/` to the repo's committed `.gitignore`. It now prefers **`.git/info/exclude`**
  (no repo litter, invisible to collaborators, shared across linked worktrees — worktree
  `.git` pointer files and their `commondir` indirection are resolved), skips writing entirely
  when the project is not a git repository or when `.bv` is already covered by the repo
  `.gitignore`, `.git/info/exclude`, or the user's global gitignore (`core.excludesFile` from
  the global git config, or the `$XDG_CONFIG_HOME/git/ignore` default). A new
  `BV_NO_GITIGNORE` environment variable (any non-empty value) disables all ignore-file
  management. Everything remains pure file I/O — no `git` subprocess, no git-binary
  dependency — and `bv` still never deletes or rewrites existing ignore entries; appending to
  `.gitignore` survives only as a last-resort fallback when `.git` exists but the exclude file
  is unusable.

### Added

- **Reliable light/dark theme selection (bv-128, idea from PR #178).** New `--theme` flag
  (`light` | `dark` | `auto`) plus a top-level `theme:` key in `~/.config/bv/config.yaml`,
  resolved once at startup with the precedence `--theme` → `BV_THEME` → config → auto-detect.
  The resolved preference is now applied to the **global** lipgloss renderer
  (`lipgloss.SetHasDarkBackground`), fixing the pre-existing `BV_THEME` override, which only
  touched per-model renderers while the bulk of the UI (package-global styles, badges, glamour
  markdown) kept auto-detecting — and auto-detection falls back to *dark* whenever the terminal
  doesn't answer the background query (common over SSH and in `tmux`/`screen`), leaving light
  terminals with near-white, unreadable text and no working escape hatch. Invalid `--theme`
  values warn on stderr and resolve to auto-detect.

- **Bounded robot liveness for the triage history prologue (#166).** The git-history correlation
  step of `--robot-triage` / `--robot-next` now runs under a hard budget (default 10 s),
  overridable via `--robot-history-timeout-ms` or `BV_ROBOT_HISTORY_TIMEOUT_MS` (`0` =
  unbounded). On timeout the in-flight git subprocess is killed (the correlation package now
  threads a `context.Context` into every `git` invocation via `exec.CommandContext`) and triage
  proceeds without history. The outcome is surfaced as `meta.history_status`
  (`ok` | `error` | `timeout`; omitted when history generation was not attempted).

---

## [v0.17.0] -- 2026-06-08 (Release)

Performance release: a profile-driven overhaul of the agent-facing robot path makes repeat
`--robot-*` calls roughly **25× faster** (warm triage ~2.3 s → ~0.09 s) and cuts git
subprocesses from ~340 to 3 — all while keeping output byte-identical (verified by golden and
differential tests). Also bundles the correctness fixes and a fresh-eyes bug-review pass found
during the optimization work, plus a routine dependency refresh.

### Added

- **TUI:** left-click now focuses a panel and selects the row under the cursor, with precise
  geometry inversion for split and single-column list views (drags and out-of-range clicks are
  ignored) (bv-162).

### Performance — Robot Path (`--robot-triage` / `--robot-next` / `--robot-plan` / `--robot-insights`)

- **Correlation (the dominant former hotspot):** replaced ~2 `git show` per commit with 2 batched
  `git log` calls; extract bead lifecycle via deduped `--raw` blob snapshots instead of
  `git log -p`; added a persistent correlation result cache keyed on HEAD + beads-hash + opts so
  repeat calls skip git extraction entirely. Cold-path: split the cache so working-tree bead edits
  no longer bust the HEAD-only extraction, and added content-addressed per-commit **event** and
  **co-commit** caches keyed by immutable commit SHA, so advancing HEAD only processes the new
  commits (#160, #161).
- **Analysis disk cache:** no longer rewrites the whole cache file on a read-hit; switched to
  goccy/go-json streaming codec; adopted a columnar struct-of-arrays on-disk shape (v2, ~44 %
  smaller).
- **Loader / datasource:** parse `issues.jsonl` once on the robot path (fused load + validation
  via a typed probe), memoize the data hash per invocation, and added a size-gated
  (≥ 4 MiB) parallel JSONL parse for large stores.

### Fixed

- **triage:** exclude unblocked parent-child rollup edges from `blocked_by` (#158).
- **watch:** coalesce `--watch-export` with adaptive backoff and skip exports via the canonical
  content + dependency hash (#159).
- **Fresh-eyes review (output-preserving):** restored commit `BeadID`s dropped from the HEAD
  artifact cache; rejected a fresher empty/non-issue JSONL from shadowing the real `issues.jsonl`;
  fixed parallel-parser `ParseStats` merge and an off-by-one max-capacity line boundary; stopped a
  transient git failure from poisoning the co-commit cache; fixed a possible `readBlobs` deadlock
  on a `cat-file` parse error; fixed a negative-index panic and a per-commit cache self-wipe.

### Dependencies

- Updated 7 direct dependencies (`git.sr.ht/~sbinet/gg`, `mattn/go-runewidth`,
  `golang.org/x/{image,sync,sys,term}`, `modernc.org/sqlite`) and re-vendored canonically. See
  `UPGRADE_LOG.md`.

---

## [v0.16.2] -- 2026-05-16 (Release)

Patch release focused on updater correctness, release artifact reliability, and hardening fixes found during fresh-eyes review passes.

### Updater & Versioning
- Ignore update notices whose release tag is equal to or older than the running version, including tags with stray whitespace.
- Clear stale in-session update banners when a later update check resolves to the current version.
- Label the footer badge as `Update vX.Y.Z` so it is not confused with the running `bv --version` value.
- Avoid redundant update checks after a recent successful check.
- Accept stable release archive names in the updater.

### Build & Release
- Align GoReleaser archive names with README `latest/download` aliases, publish Windows as a zip archive, and update the GoReleaser config to v2 syntax.
- Keep direct download aliases reproducible and version-aliased.
- Keep GoReleaser snapshot builds from double-prefixing versions as `vvX.Y.Z-next`.

### Tests
- Harden background worker tests by waiting on worker state instead of fixed sleeps.

### Robustness
- Preserve completed history searches and coalesce queued background refreshes in the TUI.
- Resolve historical `--as-of` dates from commit history and filter stale data sources by true newest time.
- Harden correlation batch-stat locking, git stream progress callbacks, EOF handling, temporal author filters, and tombstone lifecycle classification.
- Safely pair agent blurb removal markers.
- Recover stale unreadable instance locks.

### Search, Hooks, Analysis & Export
- Score all bead statuses in hybrid search mode.
- Reject malformed hook timeout values.
- Normalize loaded feedback data for analysis.
- Escape interactive graph HTML, validate Cloudflare project names, keep Cloudflare suggestions nonempty, and fail stale or zero-issue deployment verification.
- Avoid shell browser launchers on Windows and strengthen GitHub deployment helper behavior before force-with-lease pushes.

---

## [v0.16.1] -- 2026-05-14 (Release)

Patch release focused on dependency freshness, vendored reproducibility, and release metadata cleanup.

### Dependencies
- Update Go module and vendored dependencies, including `modernc.org/sqlite` 1.50.1, `fsnotify` 1.10.1, `pgregory.net/rapid` 1.3.0, `golang.org/x/*` packages, `goldmark` 1.8.2, `chroma` 2.24.1, and terminal-width/runtime helpers.
- Verify the local `/dp/toon-go` dependency is already at the latest local commit used by `go.mod`.
- Update Rust/WASM lockfiles and move `bv-graph-wasm` to `getrandom` 0.4 with the `wasm_js` feature.

### Build & Release
- Refresh fallback, Nix, README latest-download aliases, and changelog release metadata for `v0.16.1`.
- Restrict ACFS notification workflow triggers to `main`.
- Keep transient `.beads/.write.lock` files ignored and out of release commits.

---

## [v0.16.0] -- 2026-04-24 (Release)

Release focused on robot registry hardening, scoped graph/search workflows, static export improvements, and broad robustness fixes.

### Features
- Phase-three robot registry with immutable snapshots and cache hardening.
- `--robot-triage` scoping via `--graph-root` subgraphs.
- Hybrid graph-aware ranking for static export search.
- Heatmap controls, dynamic force layout, improved graph navigation, and mobile help.
- JSONL reader and `IssueReader` interface for more flexible data loading.
- Smart terminal editor dispatch via `O` key for opening beads in `$EDITOR` ([550f3bd](https://github.com/Dicklesworthstone/beads_viewer/commit/550f3bd))

### Robustness
- Suppress spurious robot-mode success banners that polluted JSON consumers.
- Harden metrics cache singleflight behavior, worker snapshot publication, and concurrent status reads.
- Guard `truncate()` against negative/small max values and nil process handles ([816f9c3](https://github.com/Dicklesworthstone/beads_viewer/commit/816f9c3))
- Guard against negative `strings.Repeat` and normalize whitespace status ([a0a35ee](https://github.com/Dicklesworthstone/beads_viewer/commit/a0a35ee))
- Preserve issue deep-links during cold load filter sync ([81a1983](https://github.com/Dicklesworthstone/beads_viewer/commit/81a1983))

### Export & Graph
- Include downstream dependents in root-focused graph BFS.
- Pin graph node side panels reliably on click and add explicit open/close controls.
- Preserve explicit pages source files and safely confine preview file serving.
- Keep preview paths and status validation safe.

### Robot Mode
- Add agent capability manifest and executable/canonical command forms.
- Align robot schema/envelope contracts across search, history, alerts, label, graph, burndown, forecast, diff, and recipe outputs.
- Respect claimability in triage top picks and publish safer command examples.

### Data Source & Loader
- Load minimal SQLite schemas, honor explicit beads database files, and safely escape SQLite DSN paths.
- Accept legacy dependency field names and UUIDv7 comment IDs.
- Recognize SQLite database path extensions and handle canonical beads history paths.

### Correlation & Search
- Preserve explicit history paths in caches and aggregate directory file references.
- Stop streaming git output after parse errors.
- Harden hybrid weights, short-query boosts, and vector index access.

### UI & Workflow
- YAML frontmatter: single-pass unescape, escape all fields, fix body whitespace and labels handling ([7a481b0](https://github.com/Dicklesworthstone/beads_viewer/commit/7a481b0), [90aa46e](https://github.com/Dicklesworthstone/beads_viewer/commit/90aa46e), [c0f670b](https://github.com/Dicklesworthstone/beads_viewer/commit/c0f670b))

---

## [v0.15.2] -- 2026-03-09 (Release)

Patch release fixing Cloudflare Pages deployment on headless servers.

### Deployment Fixes
- Check all wrangler config paths and handle refresh tokens ([cf001ba](https://github.com/Dicklesworthstone/beads_viewer/commit/cf001ba))

---

## [v0.15.1] -- 2026-03-09 (Release)

Patch release for a wrangler auth hang discovered immediately after v0.15.0.

### Deployment Fixes
- Fix wrangler auth check hanging on headless servers ([4cc8635](https://github.com/Dicklesworthstone/beads_viewer/commit/4cc8635))

---

## [v0.15.0] -- 2026-03-08 (Release)

Major release focused on `br`/`beads-rs` compatibility, expanded status types, security hardening, and POSIX-compliant CLI flags.

### Compatibility & Data Source
- Read labels from separate `labels` table for `br`/`beads-rs` SQLite compatibility ([19437c4](https://github.com/Dicklesworthstone/beads_viewer/commit/19437c4))
- Add `--db` flag and `BEADS_DB` env var for configuring database path (#125) ([b56ddae](https://github.com/Dicklesworthstone/beads_viewer/commit/b56ddae))
- Migrate from Go `flag` to `pflag` for POSIX double-dash options ([064b3d0](https://github.com/Dicklesworthstone/beads_viewer/commit/064b3d0))
- Complete `bd`-to-`br` command migration across all source and tests ([f9ba482](https://github.com/Dicklesworthstone/beads_viewer/commit/f9ba482), [6bce598](https://github.com/Dicklesworthstone/beads_viewer/commit/6bce598))

### Status & Display
- Add color mappings for deferred, draft, pinned, hooked, review, and tombstone statuses ([42d69f7](https://github.com/Dicklesworthstone/beads_viewer/commit/42d69f7), [ce542b3](https://github.com/Dicklesworthstone/beads_viewer/commit/ce542b3))
- Improve footer text contrast across terminal themes (#128) ([271cb10](https://github.com/Dicklesworthstone/beads_viewer/commit/271cb10))
- Color-profile-aware styling for Solarized and 16-color terminals ([cbbcb1f](https://github.com/Dicklesworthstone/beads_viewer/commit/cbbcb1f))
- Use terminal default background to prevent ANSI color mismap (#101) ([2599cce](https://github.com/Dicklesworthstone/beads_viewer/commit/2599cce))

### Security
- Scope GitHub token to `github.com` domains to prevent credential leaking on redirects ([ccd23d0](https://github.com/Dicklesworthstone/beads_viewer/commit/ccd23d0))
- Trim whitespace from GitHub token env vars to prevent 401 errors ([a148823](https://github.com/Dicklesworthstone/beads_viewer/commit/a148823))
- Add `GITHUB_TOKEN` support for self-update (#116, #117) ([2ff6cab](https://github.com/Dicklesworthstone/beads_viewer/commit/2ff6cab))

### Board View
- Correct column width calculation to prevent line rendering glitch (#114) ([08eb523](https://github.com/Dicklesworthstone/beads_viewer/commit/08eb523))
- Allow board columns to shrink below 12 chars on very narrow terminals ([e50be8a](https://github.com/Dicklesworthstone/beads_viewer/commit/e50be8a))
- Correct detail panel box drawing ([2ff6cab](https://github.com/Dicklesworthstone/beads_viewer/commit/2ff6cab))

### Robot Mode & Agent Support
- Add `--agents-*` CLI flags for AGENTS.md blurb management ([8e9c656](https://github.com/Dicklesworthstone/beads_viewer/commit/8e9c656))
- Normalize robot output envelope across all commands ([23172a1](https://github.com/Dicklesworthstone/beads_viewer/commit/23172a1))
- Apply recipe filtering before robot modes ([dc6bfab](https://github.com/Dicklesworthstone/beads_viewer/commit/dc6bfab))
- Upgrade agent blurb to v2 ([ce542b3](https://github.com/Dicklesworthstone/beads_viewer/commit/ce542b3))

### Version Detection
- Multi-source version detection with graceful fallback; filter pseudo-versions and dirty builds ([ede65f2](https://github.com/Dicklesworthstone/beads_viewer/commit/ede65f2), [1bb3c27](https://github.com/Dicklesworthstone/beads_viewer/commit/1bb3c27))
- Validate ldflags injection to prevent empty version output (#126) ([087af33](https://github.com/Dicklesworthstone/beads_viewer/commit/087af33))

### Triage & Analysis
- Transitive parent-blocked check in `GetActionableIssues` ([b14e9c4](https://github.com/Dicklesworthstone/beads_viewer/commit/b14e9c4))
- Invalidate robot triage disk cache when `.beads/` directory changes (#127) ([9464db4](https://github.com/Dicklesworthstone/beads_viewer/commit/9464db4))
- Deep mtime scan via `WalkDir` for cache invalidation ([d1e8233](https://github.com/Dicklesworthstone/beads_viewer/commit/d1e8233))

### Deployment
- Add GitHub Pages and Cloudflare deployment support for static export ([e60384b](https://github.com/Dicklesworthstone/beads_viewer/commit/e60384b))
- Auto release notes CI workflow (#99) ([2599cce](https://github.com/Dicklesworthstone/beads_viewer/commit/2599cce))

### License
- Update license to MIT with OpenAI/Anthropic Rider ([81c2b94](https://github.com/Dicklesworthstone/beads_viewer/commit/81c2b94))

---

## [v0.14.4] -- 2026-02-03 (Release)

Expanded robot-mode CLI with additional machine-readable outputs.

### Robot Mode
- Expand robot-mode commands with enhanced outputs and wizard support ([ae2e2e7](https://github.com/Dicklesworthstone/beads_viewer/commit/ae2e2e7), [15e72df](https://github.com/Dicklesworthstone/beads_viewer/commit/15e72df), [65f2708](https://github.com/Dicklesworthstone/beads_viewer/commit/65f2708))

---

## [v0.14.3] -- 2026-02-03 (Release)

Security and reliability fixes for the static HTML export.

### Security & Export
- XSS-escape title in HTML export, wrap errors, improve OPFS cache cleanup ([005a220](https://github.com/Dicklesworthstone/beads_viewer/commit/005a220))
- Ensure SHA-256 hash is always computed for OPFS cache invalidation ([d263a78](https://github.com/Dicklesworthstone/beads_viewer/commit/d263a78))

---

## [v0.14.2] -- 2026-02-03 (Release)

Cache-busting fix for GitHub Pages deployments.

### Export
- Add cache-busting to HTML `<script>` tags for GitHub Pages ([fcf1b7f](https://github.com/Dicklesworthstone/beads_viewer/commit/fcf1b7f))

---

## [v0.14.1] -- 2026-02-03 (Release)

Prevent stale data on GitHub Pages updates.

### Export
- Add cache-busting query strings to prevent stale data after updates ([5cfd94a](https://github.com/Dicklesworthstone/beads_viewer/commit/5cfd94a))

---

## [v0.14.0] -- 2026-02-02 (Release)

Major release introducing smart multi-source data detection, TOON format, live-reload preview, resizable split panes, and the `--robot-docs` / `--robot-schema` commands.

### Data Sources
- Smart multi-source data detection: auto-detect JSONL, SQLite, and `.beads/` directories (#88) ([2016b25](https://github.com/Dicklesworthstone/beads_viewer/commit/2016b25), [af5499b](https://github.com/Dicklesworthstone/beads_viewer/commit/af5499b))
- Prefer `beads.jsonl` over `issues.jsonl` for backward compatibility ([87f36d7](https://github.com/Dicklesworthstone/beads_viewer/commit/87f36d7))
- Watch all repos in workspace mode (closes #79) ([ac11e35](https://github.com/Dicklesworthstone/beads_viewer/commit/ac11e35))

### TOON Output Format
- Add `--format json|toon` for token-optimized output in robot mode ([4f5f032](https://github.com/Dicklesworthstone/beads_viewer/commit/4f5f032))
- Add `--stats` for bv robot output ([ebfeb6f](https://github.com/Dicklesworthstone/beads_viewer/commit/ebfeb6f))

### Robot Mode
- Add `--robot-docs` for machine-readable documentation ([0c9f6a3](https://github.com/Dicklesworthstone/beads_viewer/commit/0c9f6a3))
- Add `--robot-schema` for JSON Schema output ([9213bc5](https://github.com/Dicklesworthstone/beads_viewer/commit/9213bc5))
- Consistent `RobotEnvelope` wrapper across all robot commands ([e6609dd](https://github.com/Dicklesworthstone/beads_viewer/commit/e6609dd))

### Live Preview
- Live-reload via SSE for instant browser refresh during `--preview` ([4b6a095](https://github.com/Dicklesworthstone/beads_viewer/commit/4b6a095))
- Fix Content-Length handling for SSE script injection ([9a72ba7](https://github.com/Dicklesworthstone/beads_viewer/commit/9a72ba7))
- Fix HTTP 408 timeout on large GitHub Pages pushes ([a9594bf](https://github.com/Dicklesworthstone/beads_viewer/commit/a9594bf))

### TUI
- Resizable split view panes ([acf7568](https://github.com/Dicklesworthstone/beads_viewer/commit/acf7568))
- Add `y` shortcut to copy bead ID in list view ([a9ff252](https://github.com/Dicklesworthstone/beads_viewer/commit/a9ff252))
- Reset list cursor before filter refresh to prevent panic ([3a1cde5](https://github.com/Dicklesworthstone/beads_viewer/commit/3a1cde5))
- Preserve triage data across reloads ([6aa9fbf](https://github.com/Dicklesworthstone/beads_viewer/commit/6aa9fbf))

### Analysis & Triage
- Priority scoring for bead analysis and task triage ([da11317](https://github.com/Dicklesworthstone/beads_viewer/commit/da11317))
- Compact adjacency graph for analysis performance ([e16ee9b](https://github.com/Dicklesworthstone/beads_viewer/commit/e16ee9b))
- Staleness analysis in `robot-triage` using git history ([157c3c7](https://github.com/Dicklesworthstone/beads_viewer/commit/157c3c7))
- Support for `review` status ([5fc1a70](https://github.com/Dicklesworthstone/beads_viewer/commit/5fc1a70))
- Group tracks by topological depth instead of connected components ([319b45e](https://github.com/Dicklesworthstone/beads_viewer/commit/319b45e))

### Installation
- Default install to `~/.local/bin` to avoid requiring root ([32785d4](https://github.com/Dicklesworthstone/beads_viewer/commit/32785d4))

---

## [v0.13.0] -- 2026-01-14 (Release)

Major release adding Homebrew/Scoop package distribution, SQLite comment export, watch mode, buffer pooling performance optimizations, and comprehensive agent-friendliness improvements.

### Distribution
- GoReleaser auto-publishing to Homebrew and Scoop ([09db3df](https://github.com/Dicklesworthstone/beads_viewer/commit/09db3df))
- Claude Code `SKILL.md` for automatic capability discovery ([3d393e0](https://github.com/Dicklesworthstone/beads_viewer/commit/3d393e0))

### Export & Data
- Add comments to SQLite export (#52) and watch mode for continuous export (#55) ([2324e61](https://github.com/Dicklesworthstone/beads_viewer/commit/2324e61))
- Support all 8 beads status types; filter blocked items from `robot-next` ([c1c5c40](https://github.com/Dicklesworthstone/beads_viewer/commit/c1c5c40))
- XSS prevention, JSON encoding, and preview validation ([a1e4ca3](https://github.com/Dicklesworthstone/beads_viewer/commit/a1e4ca3))
- Orphan detection, O(1) cycle lookup, and case-insensitive labels ([cba2f3f](https://github.com/Dicklesworthstone/beads_viewer/commit/cba2f3f))
- `IssueFilter` callback and status normalization in loader ([c0e3582](https://github.com/Dicklesworthstone/beads_viewer/commit/c0e3582))
- Unified tombstone/closed status handling ([a80a75c](https://github.com/Dicklesworthstone/beads_viewer/commit/a80a75c))
- Deterministic data hash computation for cache invalidation ([ea25629](https://github.com/Dicklesworthstone/beads_viewer/commit/ea25629))

### Performance
- Comprehensive Round 2 optimizations for `robot-triage` latency ([47adaeb](https://github.com/Dicklesworthstone/beads_viewer/commit/47adaeb))
- Buffer pooling for Brandes' algorithm ([44c10c2](https://github.com/Dicklesworthstone/beads_viewer/commit/44c10c2))
- Memoize `GetActionableIssues` via `TriageContext` ([127176b](https://github.com/Dicklesworthstone/beads_viewer/commit/127176b))
- `topk` utility package for performance-critical sorting ([1cbdb9c](https://github.com/Dicklesworthstone/beads_viewer/commit/1cbdb9c))
- Integrate `topk` into vector search ([8ec02df](https://github.com/Dicklesworthstone/beads_viewer/commit/8ec02df))

### Robot Mode
- Performance metrics package and `--robot-metrics` command ([af868af](https://github.com/Dicklesworthstone/beads_viewer/commit/af868af))
- TTY guard to prevent control sequence leakage in robot mode ([c7e2cfe](https://github.com/Dicklesworthstone/beads_viewer/commit/c7e2cfe))

### Platform Support
- Windows compatibility for atomic file operations ([fe79efa](https://github.com/Dicklesworthstone/beads_viewer/commit/fe79efa))

### Build
- Vendor all Go dependencies for reproducible builds ([fdd2f75](https://github.com/Dicklesworthstone/beads_viewer/commit/fdd2f75))

---

## [v0.12.1] -- 2026-01-07 (Release)

Reliability and concurrency improvements, introducing multi-instance coordination and background workers.

### Concurrency
- Multi-instance awareness and coordination to prevent conflicts ([8bf8118](https://github.com/Dicklesworthstone/beads_viewer/commit/8bf8118))
- Phase 1 background worker infrastructure with snapshot dedup ([cd31623](https://github.com/Dicklesworthstone/beads_viewer/commit/cd31623), [0b67acd](https://github.com/Dicklesworthstone/beads_viewer/commit/0b67acd))
- Async Phase 2 analysis notification ([bb5c4bc](https://github.com/Dicklesworthstone/beads_viewer/commit/bb5c4bc))
- Prevent race conditions and panics in `BackgroundWorker` lifecycle ([160ff93](https://github.com/Dicklesworthstone/beads_viewer/commit/160ff93), [2e47a0a](https://github.com/Dicklesworthstone/beads_viewer/commit/2e47a0a))
- Fix TOCTOU race condition in stale lock takeover ([b544a31](https://github.com/Dicklesworthstone/beads_viewer/commit/b544a31))

### Security
- Prevent shell injection in editor file path ([9cd383b](https://github.com/Dicklesworthstone/beads_viewer/commit/9cd383b))

### Compatibility
- AdaptiveColor for light terminal mode support ([761f4fb](https://github.com/Dicklesworthstone/beads_viewer/commit/761f4fb))
- Accept any non-empty `IssueType` for Gastown compatibility ([f3cba6e](https://github.com/Dicklesworthstone/beads_viewer/commit/f3cba6e))
- Windows installation support via PowerShell script ([3d48d7a](https://github.com/Dicklesworthstone/beads_viewer/commit/3d48d7a))

### Testing
- Comprehensive fuzz testing for JSONL parser ([3b0ed00](https://github.com/Dicklesworthstone/beads_viewer/commit/3b0ed00))

---

## [v0.12.0] -- 2026-01-06 (Release)

Major release introducing the Tree View, tutorial system, `cass` (coding agent session search) integration, three-pane history layout, and `AGENTS.md` auto-injection.

### Tree View
- Full file tree view with cursor-follows-viewport scrolling ([7cae563](https://github.com/Dicklesworthstone/beads_viewer/commit/7cae563), [f514f95](https://github.com/Dicklesworthstone/beads_viewer/commit/f514f95), [e3ad293](https://github.com/Dicklesworthstone/beads_viewer/commit/e3ad293))
- Tree state persistence on expand/collapse ([f54da8b](https://github.com/Dicklesworthstone/beads_viewer/commit/f54da8b), [8355e48](https://github.com/Dicklesworthstone/beads_viewer/commit/8355e48))
- Scroll position indicator and windowed viewport rendering ([a1b5e7c](https://github.com/Dicklesworthstone/beads_viewer/commit/a1b5e7c))
- Integrated with main app model ([33a8618](https://github.com/Dicklesworthstone/beads_viewer/commit/33a8618))

### Tutorial System
- Multi-page tutorial with Glamour markdown rendering ([187c022](https://github.com/Dicklesworthstone/beads_viewer/commit/187c022), [091a597](https://github.com/Dicklesworthstone/beads_viewer/commit/091a597))
- Beautiful UI layout and chrome ([e218e97](https://github.com/Dicklesworthstone/beads_viewer/commit/e218e97))
- Content: Introduction, Core Concepts, Views & Navigation, Advanced Features, Real-World Workflows ([d8a055d](https://github.com/Dicklesworthstone/beads_viewer/commit/d8a055d), [e3297fc](https://github.com/Dicklesworthstone/beads_viewer/commit/e3297fc), [3df90f1](https://github.com/Dicklesworthstone/beads_viewer/commit/3df90f1), [0a4d0de](https://github.com/Dicklesworthstone/beads_viewer/commit/0a4d0de), [9573362](https://github.com/Dicklesworthstone/beads_viewer/commit/9573362))
- Tutorial progress persistence ([9f5e51b](https://github.com/Dicklesworthstone/beads_viewer/commit/9f5e51b))
- Space key entry point and CapsLock trigger detection ([49fc7a9](https://github.com/Dicklesworthstone/beads_viewer/commit/49fc7a9), [689455b](https://github.com/Dicklesworthstone/beads_viewer/commit/689455b))

### Cass (Coding Agent Session Search) Integration
- Detection and health checking for local `cass` instances ([beca14b](https://github.com/Dicklesworthstone/beads_viewer/commit/beca14b))
- Search interface with safety wrappers and LRU result caching ([24b4594](https://github.com/Dicklesworthstone/beads_viewer/commit/24b4594), [5fddade](https://github.com/Dicklesworthstone/beads_viewer/commit/5fddade))
- Session Preview Modal via `V` key ([04c640a](https://github.com/Dicklesworthstone/beads_viewer/commit/04c640a))
- Status bar session indicator ([8c18b66](https://github.com/Dicklesworthstone/beads_viewer/commit/8c18b66))

### History View Enhancements
- Adaptive three-pane layout ([f374d48](https://github.com/Dicklesworthstone/beads_viewer/commit/f374d48))
- Git-centric view mode ([04dde17](https://github.com/Dicklesworthstone/beads_viewer/commit/04dde17))
- Timeline visualization panel ([b51608c](https://github.com/Dicklesworthstone/beads_viewer/commit/b51608c))
- File-centric drill-down ([f975c29](https://github.com/Dicklesworthstone/beads_viewer/commit/f975c29))
- Lifecycle events display in detail pane ([9ba1348](https://github.com/Dicklesworthstone/beads_viewer/commit/9ba1348))
- Search and filter infrastructure ([6d2f89d](https://github.com/Dicklesworthstone/beads_viewer/commit/6d2f89d))
- Statistics header bar with badges ([7c56a35](https://github.com/Dicklesworthstone/beads_viewer/commit/7c56a35))
- View mode toggle animation ([db435cf](https://github.com/Dicklesworthstone/beads_viewer/commit/db435cf))
- Keyboard navigation improvements ([37bf83e](https://github.com/Dicklesworthstone/beads_viewer/commit/37bf83e))
- Enhanced commit detail pane with rich display ([358280e](https://github.com/Dicklesworthstone/beads_viewer/commit/358280e))

### AGENTS.md Management
- Auto-detect and inject bv blurbs into project `AGENTS.md` files ([35e5298](https://github.com/Dicklesworthstone/beads_viewer/commit/35e5298), [4de28ca](https://github.com/Dicklesworthstone/beads_viewer/commit/4de28ca))
- Legacy blurb detection and migration support ([f68d307](https://github.com/Dicklesworthstone/beads_viewer/commit/f68d307))
- Prompt modal component and preference storage ([61e1068](https://github.com/Dicklesworthstone/beads_viewer/commit/61e1068), [e123584](https://github.com/Dicklesworthstone/beads_viewer/commit/e123584))
- Atomic file operations for safe updates ([738eebf](https://github.com/Dicklesworthstone/beads_viewer/commit/738eebf))

### Board View
- Detail panel with Tab toggle ([22e55ae](https://github.com/Dicklesworthstone/beads_viewer/commit/22e55ae))
- Rich card content with improved info density ([6753569](https://github.com/Dicklesworthstone/beads_viewer/commit/6753569))
- Visual dependency indicators with color-coded borders ([f9c754f](https://github.com/Dicklesworthstone/beads_viewer/commit/f9c754f))
- Swimlane grouping modes ([0c23321](https://github.com/Dicklesworthstone/beads_viewer/commit/0c23321))
- Filter keys (`o`/`c`/`r`) in board view ([60dabe2](https://github.com/Dicklesworthstone/beads_viewer/commit/60dabe2))
- Inline card expansion ([966f513](https://github.com/Dicklesworthstone/beads_viewer/commit/966f513))
- Column statistics in headers ([053999d](https://github.com/Dicklesworthstone/beads_viewer/commit/053999d))
- Smart empty column handling ([b827f64](https://github.com/Dicklesworthstone/beads_viewer/commit/b827f64))

### Correlation & Impact Analysis
- Impact network graph for bead correlation ([7729029](https://github.com/Dicklesworthstone/beads_viewer/commit/7729029))
- Related work discovery ([1c92d8f](https://github.com/Dicklesworthstone/beads_viewer/commit/1c92d8f))
- Temporal causality analysis ([a941fbd](https://github.com/Dicklesworthstone/beads_viewer/commit/a941fbd))
- File co-change pattern detection ([9b58387](https://github.com/Dicklesworthstone/beads_viewer/commit/9b58387))
- Orphan commit detection with smart heuristics ([8a01220](https://github.com/Dicklesworthstone/beads_viewer/commit/8a01220))
- Change impact analysis for agents ([25fe4e5](https://github.com/Dicklesworthstone/beads_viewer/commit/25fe4e5))
- Correlation confidence audit ([f279882](https://github.com/Dicklesworthstone/beads_viewer/commit/f279882))
- Blocker chain visualization and `--robot-blocker-chain` command ([b18a1cf](https://github.com/Dicklesworthstone/beads_viewer/commit/b18a1cf))
- File-bead reverse index for history view ([382a90f](https://github.com/Dicklesworthstone/beads_viewer/commit/382a90f))

### Context Help
- Context-specific help system ([c0e83e1](https://github.com/Dicklesworthstone/beads_viewer/commit/c0e83e1))
- Context detection system ([021e044](https://github.com/Dicklesworthstone/beads_viewer/commit/021e044))

### Export & Viewer
- Pure-Go SQLite with built-in FTS5 support (replaces CGO dependency) ([eda80f0](https://github.com/Dicklesworthstone/beads_viewer/commit/eda80f0))
- Vendor all CDN dependencies for offline use ([0fe11cd](https://github.com/Dicklesworthstone/beads_viewer/commit/0fe11cd))
- Auto-add `.bv` to `.gitignore` ([6921e11](https://github.com/Dicklesworthstone/beads_viewer/commit/6921e11))

### UI Polish
- Redesign flow matrix as interactive dashboard ([3381324](https://github.com/Dicklesworthstone/beads_viewer/commit/3381324))
- Scrollable detail panel with viewport in Insights ([b9bdc71](https://github.com/Dicklesworthstone/beads_viewer/commit/b9bdc71))
- Enhanced label picker with count-based sorting ([9983300](https://github.com/Dicklesworthstone/beads_viewer/commit/9983300))

---

## [v0.11.3] -- 2026-01-03 (Release)

Documentation and TUI corrections, plus Nix flake for reproducible builds.

### TUI
- Correct Board view context help -- `L` jumps to last column, not label picker ([122a08a](https://github.com/Dicklesworthstone/beads_viewer/commit/122a08a))
- Fix keyboard shortcut inconsistencies in help/tutorial ([c125906](https://github.com/Dicklesworthstone/beads_viewer/commit/c125906))
- Restore focus to label picker and time travel input after help ([1769970](https://github.com/Dicklesworthstone/beads_viewer/commit/1769970))
- Multiple TUI fixes for focus, detail view, and design field ([686fad4](https://github.com/Dicklesworthstone/beads_viewer/commit/686fad4))

### Self-Update
- TUI update modal with false-positive prevention for dev builds ([8b8bc01](https://github.com/Dicklesworthstone/beads_viewer/commit/8b8bc01), [da0abad](https://github.com/Dicklesworthstone/beads_viewer/commit/da0abad))
- Handle restore-from-backup error in update flow ([8f64e9c](https://github.com/Dicklesworthstone/beads_viewer/commit/8f64e9c))

### Build
- Add Nix flake for reproducible builds and development ([ceb993a](https://github.com/Dicklesworthstone/beads_viewer/commit/ceb993a))
- Change `Version` from `const` to `var` for ldflags support ([8b08c46](https://github.com/Dicklesworthstone/beads_viewer/commit/8b08c46))

---

## [v0.11.2] -- 2025-12-21 (Release)

Embed viewer assets in binary for self-contained `--pages` export.

### Export
- Embed viewer assets in binary so `--pages` export works without external files ([d537ba5](https://github.com/Dicklesworthstone/beads_viewer/commit/d537ba5))

### Analysis
- Use `normalizedFiles` length in correlation `ImpactAnalysis` ([1c4c0fb](https://github.com/Dicklesworthstone/beads_viewer/commit/1c4c0fb))

---

## [v0.11.1] -- 2025-12-19 (Release)

Enhanced static HTML viewer with mobile support.

### Static HTML Viewer
- Arrow key navigation, enhanced heatmap, and mobile help modal ([5a8c94c](https://github.com/Dicklesworthstone/beads_viewer/commit/5a8c94c))

---

## [v0.11.0] -- 2025-12-19 (Release)

Major release introducing hybrid search with graph-aware ranking across both the TUI and static export.

### Hybrid Search
- Core types with weighted scoring model ([7956d47](https://github.com/Dicklesworthstone/beads_viewer/commit/7956d47))
- Metrics cache and query-adaptive weight adjustment ([4cbd453](https://github.com/Dicklesworthstone/beads_viewer/commit/4cbd453))
- CLI hybrid search integration and search configuration ([87981a0](https://github.com/Dicklesworthstone/beads_viewer/commit/87981a0))
- Graph-aware ranking in static export with optional WASM scorer ([8ba75d8](https://github.com/Dicklesworthstone/beads_viewer/commit/8ba75d8), [61a93f7](https://github.com/Dicklesworthstone/beads_viewer/commit/61a93f7))

### TUI Fixes
- Escape key properly closes label picker before quit confirm ([bff5876](https://github.com/Dicklesworthstone/beads_viewer/commit/bff5876))
- Clear `isHistoryView` when switching to other views ([04299d6](https://github.com/Dicklesworthstone/beads_viewer/commit/04299d6))

---

## [v0.10.6] -- 2025-12-18 (Release)

Rapid patch release fixing E2E test compatibility on Linux.

### Testing
- Support Linux `script` command syntax for TUI E2E tests ([ebfdcf0](https://github.com/Dicklesworthstone/beads_viewer/commit/ebfdcf0))

---

## [v0.10.5] -- 2025-12-18 (Release)

Escape key fix in label picker.

### TUI
- Escape key now properly closes label picker before triggering quit confirm ([bff5876](https://github.com/Dicklesworthstone/beads_viewer/commit/bff5876))

---

## [v0.10.4] -- 2025-12-18 (Release)

Mobile-responsive static export and native lipgloss tutorial components.

### Static HTML Export
- Mobile-responsive UI and advanced graph metrics ([53da0ca](https://github.com/Dicklesworthstone/beads_viewer/commit/53da0ca))

### TUI
- Replace ASCII art tutorial with native lipgloss component system ([6fe88ed](https://github.com/Dicklesworthstone/beads_viewer/commit/6fe88ed))

---

## [v0.10.3] -- 2025-12-18 (Release)

Heatmap mode, dynamic force graph layout, and overhauled detail pane typography in the static export.

### Static HTML Export
- Heatmap mode with gold glow hover highlighting ([63d43dd](https://github.com/Dicklesworthstone/beads_viewer/commit/63d43dd))
- Integrate heatmap controls and switch to dynamic force layout ([5ea0df6](https://github.com/Dicklesworthstone/beads_viewer/commit/5ea0df6))
- Overhaul detail pane UI and add prose typography system ([d570f02](https://github.com/Dicklesworthstone/beads_viewer/commit/d570f02))
- Pre-computed graph layout and detail pane for pages export ([a0d7169](https://github.com/Dicklesworthstone/beads_viewer/commit/a0d7169))

### Robustness
- Add nil checks in `sqlite_export.go` to prevent panics ([bdc7527](https://github.com/Dicklesworthstone/beads_viewer/commit/bdc7527))
- Fix deadlock in `FeedbackData` weight retrieval methods ([13c3c6a](https://github.com/Dicklesworthstone/beads_viewer/commit/13c3c6a))
- Add 100ms ready timeout to prevent startup hang ([2b72b34](https://github.com/Dicklesworthstone/beads_viewer/commit/2b72b34))

---

## [v0.10.2] -- 2025-11-30 (Release)

Test hardening and coverage improvements; no user-facing feature changes.

### Testing & CI
- Coverage gates and Codecov wiring ([e9f795e](https://github.com/Dicklesworthstone/beads_viewer/commit/e9f795e))
- Broaden UI edge coverage, harden robot CLI flags, expand recipe filter/sort tests ([8e5efd4](https://github.com/Dicklesworthstone/beads_viewer/commit/8e5efd4), [70bd854](https://github.com/Dicklesworthstone/beads_viewer/commit/70bd854), [477825d](https://github.com/Dicklesworthstone/beads_viewer/commit/477825d))
- Extend analysis cache/graph and hooks loader coverage ([5783560](https://github.com/Dicklesworthstone/beads_viewer/commit/5783560))
- Add hook loader/executor edge cases (missing file, empty cmd, timeout) ([60b33bc](https://github.com/Dicklesworthstone/beads_viewer/commit/60b33bc))
- Deflake git loader cache expiry timing ([9fc5f9c](https://github.com/Dicklesworthstone/beads_viewer/commit/9fc5f9c))

### Analysis
- Support unbounded track IDs in execution plan ([443162e](https://github.com/Dicklesworthstone/beads_viewer/commit/443162e))
- Refresh insights panel when toggled ([f531f4e](https://github.com/Dicklesworthstone/beads_viewer/commit/f531f4e))

---

## [v0.10.1-build.2] -- 2025-11-30 (Release)

Build fix release; no functional changes beyond test coverage.

---

## [v0.10.1] -- 2025-11-30 (Tag only)

Module path correction after the v0.10.0 rename.

### Build
- Update module path and bump version to v0.10.1 ([ad84b1b](https://github.com/Dicklesworthstone/beads_viewer/commit/ad84b1b))
- Resolve syntax error in `diff.go` and correct README install URL ([eca1623](https://github.com/Dicklesworthstone/beads_viewer/commit/eca1623))

---

## [v0.10.0] -- 2025-11-30 (Release)

Major release adding drift detection, workspace mode, async graph engine, and a revamped install script.

### Drift Detection
- Full drift detection package with summary output, custom thresholds, and E2E tests ([b275251](https://github.com/Dicklesworthstone/beads_viewer/commit/b275251))

### Workspace & File Watching
- Workspace mode: view and watch multiple repos simultaneously ([6f6c069](https://github.com/Dicklesworthstone/beads_viewer/commit/6f6c069))

### Analysis Engine
- Async phase 2 graph engine with caching and configuration ([4757dde](https://github.com/Dicklesworthstone/beads_viewer/commit/4757dde))
- Handle UTF-8 BOM in JSONL files ([e985dff](https://github.com/Dicklesworthstone/beads_viewer/commit/e985dff))
- Robust error handling and validation in loader and updater packages ([9191c59](https://github.com/Dicklesworthstone/beads_viewer/commit/9191c59))

### Export
- Improved markdown reporting and expanded integration tests ([fd86957](https://github.com/Dicklesworthstone/beads_viewer/commit/fd86957))

### Installation
- Prefer release binaries and add mac-friendly fallbacks ([5426d73](https://github.com/Dicklesworthstone/beads_viewer/commit/5426d73))
- Guard `BASH_SOURCE` for piped bash installs ([c31f605](https://github.com/Dicklesworthstone/beads_viewer/commit/c31f605), [12a8c97](https://github.com/Dicklesworthstone/beads_viewer/commit/12a8c97), [62b971f](https://github.com/Dicklesworthstone/beads_viewer/commit/62b971f))

---

## [v0.9.3] -- 2025-11-30 (Release)

Checkpoint release preserving in-progress work; no major user-facing changes.

---

## [v0.9.2] -- 2025-11-27 (Release)

TUI interactivity additions and comprehensive unit tests.

### TUI Interactivity
- Time-travel input: jump to any date to see historical state ([c28d145](https://github.com/Dicklesworthstone/beads_viewer/commit/c28d145))
- Clipboard copy shortcut ([c28d145](https://github.com/Dicklesworthstone/beads_viewer/commit/c28d145))
- Launch external editor from TUI ([c28d145](https://github.com/Dicklesworthstone/beads_viewer/commit/c28d145))
- `E` keybinding to export Markdown report from TUI ([51f1b72](https://github.com/Dicklesworthstone/beads_viewer/commit/51f1b72))
- Allow Ctrl+C to quit during time-travel input ([21f4f38](https://github.com/Dicklesworthstone/beads_viewer/commit/21f4f38))

### Testing
- Comprehensive unit tests and markdown export bug fixes ([737632e](https://github.com/Dicklesworthstone/beads_viewer/commit/737632e))

### Stability
- Prevent graph analysis hang with HITS/cycle timeouts ([1e83209](https://github.com/Dicklesworthstone/beads_viewer/commit/1e83209))

---

## [v0.9.1] -- 2025-11-27 (Release)

Documentation fixes only; no code changes.

### Documentation
- Fix Mermaid diagram rendering in README ([3ac9d43](https://github.com/Dicklesworthstone/beads_viewer/commit/3ac9d43), [dfca05b](https://github.com/Dicklesworthstone/beads_viewer/commit/dfca05b))
- Updated screenshots with latest UI improvements ([1f575e3](https://github.com/Dicklesworthstone/beads_viewer/commit/1f575e3))
- Redesign TUI architecture diagram ([eddabb6](https://github.com/Dicklesworthstone/beads_viewer/commit/eddabb6))

---

## [v0.9.0] -- 2025-11-27 (Release)

Major UI overhaul with Stripe-level visual polish, redesigned footer, badge components, and a smart install script.

### UI Design System
- Comprehensive UI design system with badge components ([f821019](https://github.com/Dicklesworthstone/beads_viewer/commit/f821019))
- Stripe-level visual polish for all UI components ([4ebb573](https://github.com/Dicklesworthstone/beads_viewer/commit/4ebb573))
- Redesign footer with keyboard hints and status indicators ([aec03fd](https://github.com/Dicklesworthstone/beads_viewer/commit/aec03fd))

### Analysis
- Optimize graph analysis and fix edge direction semantics ([04e96e7](https://github.com/Dicklesworthstone/beads_viewer/commit/04e96e7))
- Improve analysis package with better typing and tests ([dc26170](https://github.com/Dicklesworthstone/beads_viewer/commit/dc26170))

### Data Loading
- Enhance git loader with date parsing and scanner error handling ([3037beb](https://github.com/Dicklesworthstone/beads_viewer/commit/3037beb))

### Installation
- Smart install script with binary-first strategy ([8c3ad56](https://github.com/Dicklesworthstone/beads_viewer/commit/8c3ad56))

### License
- Add MIT license ([1b0bc8d](https://github.com/Dicklesworthstone/beads_viewer/commit/1b0bc8d))

---

## [v0.8.2] -- 2025-11-27 (Release)

Critical bug fixes in graph visualization and ego-centric graph redesign.

### Graph View
- Redesign graph view with ego-centric neighborhood display ([100638c](https://github.com/Dicklesworthstone/beads_viewer/commit/100638c))
- Implement visual ASCII graph with comprehensive metrics ([0dc4c5d](https://github.com/Dicklesworthstone/beads_viewer/commit/0dc4c5d))
- Fix critical bugs in graph visualization ([ce1cc83](https://github.com/Dicklesworthstone/beads_viewer/commit/ce1cc83))

### TUI
- Resolve header cutoff bug and add mouse wheel scrolling support ([6f32017](https://github.com/Dicklesworthstone/beads_viewer/commit/6f32017))

### Build
- Use GoReleaser v1 compatible config format ([00fca20](https://github.com/Dicklesworthstone/beads_viewer/commit/00fca20), [18216b5](https://github.com/Dicklesworthstone/beads_viewer/commit/18216b5))

---

## v0.8.1 / v0.8.0 -- 2025-11-27 (Draft releases, never published)

These tags exist but were superseded by v0.8.2 before publication. No distinct user-facing content beyond what shipped in v0.8.2.

---

## [v0.7.0] -- 2025-11-27 (Tag only)

Major feature release adding time-travel, planning, recipes, the AI agent CLI interface, interactive insights with calculation proofs, and dependency graph visualization.

### Time-Travel & Planning
- Time-travel diff view: see how issues changed between any two dates ([9ee882e](https://github.com/Dicklesworthstone/beads_viewer/commit/9ee882e))
- Correctly handle time-travel diff status in filters and recipes ([c5fd196](https://github.com/Dicklesworthstone/beads_viewer/commit/c5fd196))

### Recipes
- Analysis, loader, and UI components for planning and recipe-based views ([9ee882e](https://github.com/Dicklesworthstone/beads_viewer/commit/9ee882e))

### AI Agent Interface
- CLI interface for programmatic graph analysis (`--robot-*` flags) ([fe6646a](https://github.com/Dicklesworthstone/beads_viewer/commit/fe6646a))

### Interactive Insights
- Insights dashboard with calculation proofs ([b8106a5](https://github.com/Dicklesworthstone/beads_viewer/commit/b8106a5))
- `InsightItem` struct with metric values for transparency ([d5eeb7f](https://github.com/Dicklesworthstone/beads_viewer/commit/d5eeb7f))

### Dependency Graph
- Interactive dependency graph visualization ([c6ea4f7](https://github.com/Dicklesworthstone/beads_viewer/commit/c6ea4f7))

### TUI Polish
- Paging, fixed header row, F1 help, quit confirmation ([8910c0c](https://github.com/Dicklesworthstone/beads_viewer/commit/8910c0c))
- Refactor board view with adaptive column navigation ([562beb7](https://github.com/Dicklesworthstone/beads_viewer/commit/562beb7))
- Simplify list delegate with cleaner row rendering ([249441b](https://github.com/Dicklesworthstone/beads_viewer/commit/249441b))
- UTF-8 safe string truncation and improved time formatting ([845731e](https://github.com/Dicklesworthstone/beads_viewer/commit/845731e))

### Export
- Improved Mermaid diagram generation and markdown output ([4f0ceef](https://github.com/Dicklesworthstone/beads_viewer/commit/4f0ceef))

### Data Loading
- Smart JSONL file discovery with fallback support ([aee943b](https://github.com/Dicklesworthstone/beads_viewer/commit/aee943b))

---

## [v0.6.2] -- 2025-11-26 (Tag only)

CI cleanup: remove legacy branch trigger after branch consolidation.

---

## [v0.6.1] -- 2025-11-26 (Tag only)

Finalize sync from the legacy branch to `main`.

---

## [v0.6.0] -- 2025-11-26 (Tag only)

Documentation update for main branch migration.

### Documentation
- Update README with main branch install URL and feature overview ([e67f99f](https://github.com/Dicklesworthstone/beads_viewer/commit/e67f99f))

### Analysis
- Optimize graph analysis and fix UBS findings ([55152e5](https://github.com/Dicklesworthstone/beads_viewer/commit/55152e5))

---

## [v0.5.3] -- 2025-11-26 (Tag only)

Analysis optimizations discovered by UBS (Ultimate Bug Scanner).

### Analysis
- Optimize graph analysis and fix findings from UBS scan ([55152e5](https://github.com/Dicklesworthstone/beads_viewer/commit/55152e5))

---

## [v0.5.2] -- 2025-11-26 (Tag only)

Install URL fix and code formatting.

---

## [v0.5.1] -- 2025-11-26 (Tag only)

README documentation update; no code changes.

---

## [v0.5.0] -- 2025-11-26 (Tag only)

Insights dashboard with sparklines and advanced analytics.

### Insights Dashboard
- Sparkline visualizations for trend data ([294dd8e](https://github.com/Dicklesworthstone/beads_viewer/commit/294dd8e))
- Advanced analytics computations surfaced in UI ([294dd8e](https://github.com/Dicklesworthstone/beads_viewer/commit/294dd8e))

### Bug Fix
- Correct impact score logic and UI integration ([5b8a8fc](https://github.com/Dicklesworthstone/beads_viewer/commit/5b8a8fc))

---

## [v0.4.1] -- 2025-11-26 (Tag only)

Fix impact score calculation and its UI wiring.

### Bug Fix
- Correct impact score logic and UI integration ([5b8a8fc](https://github.com/Dicklesworthstone/beads_viewer/commit/5b8a8fc))

---

## [v0.4.0] -- 2025-11-26 (Tag only)

Graph theory analytics engine and impact scoring.

### Graph Analytics
- PageRank, betweenness centrality, and impact scoring for beads ([a0de64b](https://github.com/Dicklesworthstone/beads_viewer/commit/a0de64b))

---

## [v0.3.0] -- 2025-11-26 (Tag only)

Critical board view bug fix and layout enhancements.

### Bug Fix
- Fix critical board filtering bug that dropped items ([5294241](https://github.com/Dicklesworthstone/beads_viewer/commit/5294241))

### TUI
- Enhanced layouts and additional test coverage ([5294241](https://github.com/Dicklesworthstone/beads_viewer/commit/5294241))

---

## [v0.2.0] -- 2025-11-26 (Tag only)

Kanban board view, Mermaid export, and visual polish.

### Kanban Board
- Full Kanban board view with status columns (`b` to toggle) ([d18b489](https://github.com/Dicklesworthstone/beads_viewer/commit/d18b489))

### Export
- Mermaid dependency diagram export ([d18b489](https://github.com/Dicklesworthstone/beads_viewer/commit/d18b489))

### Visual Polish
- Improved styling and color scheme ([d18b489](https://github.com/Dicklesworthstone/beads_viewer/commit/d18b489))

---

## [v0.1.1] -- 2025-11-26 (Tag only)

Markdown export and ultra-wide terminal support.

### Export
- Markdown report export ([bed7a9b](https://github.com/Dicklesworthstone/beads_viewer/commit/bed7a9b))

### TUI
- Ultra-wide terminal layout support ([bed7a9b](https://github.com/Dicklesworthstone/beads_viewer/commit/bed7a9b))
- Real-data test fixtures ([bed7a9b](https://github.com/Dicklesworthstone/beads_viewer/commit/bed7a9b))

---

## [v0.1.0] -- 2025-11-26 (Tag only)

Initial release of Beads Viewer -- a keyboard-driven terminal interface for the Beads issue tracker.

### Core Features
- Split view TUI with fast list and rich detail pane ([61ff39d](https://github.com/Dicklesworthstone/beads_viewer/commit/61ff39d))
- Statistics and summary panels ([61ff39d](https://github.com/Dicklesworthstone/beads_viewer/commit/61ff39d))
- Self-updater for in-place binary upgrades ([61ff39d](https://github.com/Dicklesworthstone/beads_viewer/commit/61ff39d))
- CI/CD pipeline with GoReleaser ([61ff39d](https://github.com/Dicklesworthstone/beads_viewer/commit/61ff39d))

---

[Unreleased]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.22.0...HEAD
[v0.22.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.21.2...v0.22.0
[v0.21.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.21.1...v0.21.2
[v0.21.1]: https://github.com/Dicklesworthstone/beads_viewer/tree/v0.21.1
[v0.21.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.20.0...v0.21.0
[v0.17.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.16.4...v0.17.0
[v0.16.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.16.1...v0.16.2
[v0.16.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.16.0...v0.16.1
[v0.16.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.15.2...v0.16.0
[v0.15.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.15.1...v0.15.2
[v0.15.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.15.0...v0.15.1
[v0.15.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.14.4...v0.15.0
[v0.14.4]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.14.3...v0.14.4
[v0.14.3]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.14.2...v0.14.3
[v0.14.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.14.1...v0.14.2
[v0.14.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.14.0...v0.14.1
[v0.14.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.13.0...v0.14.0
[v0.13.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.12.1...v0.13.0
[v0.12.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.12.0...v0.12.1
[v0.12.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.11.3...v0.12.0
[v0.11.3]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.11.2...v0.11.3
[v0.11.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.11.1...v0.11.2
[v0.11.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.11.0...v0.11.1
[v0.11.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.6...v0.11.0
[v0.10.6]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.5...v0.10.6
[v0.10.5]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.4...v0.10.5
[v0.10.4]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.3...v0.10.4
[v0.10.3]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.2...v0.10.3
[v0.10.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.1-build.2...v0.10.2
[v0.10.1-build.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.1...v0.10.1-build.2
[v0.10.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.10.0...v0.10.1
[v0.10.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.9.3...v0.10.0
[v0.9.3]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.9.2...v0.9.3
[v0.9.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.9.1...v0.9.2
[v0.9.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.9.0...v0.9.1
[v0.9.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.8.2...v0.9.0
[v0.8.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.7.0...v0.8.2
[v0.7.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.6.2...v0.7.0
[v0.6.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.6.1...v0.6.2
[v0.6.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.6.0...v0.6.1
[v0.6.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.5.3...v0.6.0
[v0.5.3]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.5.2...v0.5.3
[v0.5.2]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.5.1...v0.5.2
[v0.5.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.5.0...v0.5.1
[v0.5.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.4.1...v0.5.0
[v0.4.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.4.0...v0.4.1
[v0.4.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.1.1...v0.2.0
[v0.1.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/Dicklesworthstone/beads_viewer/commit/61ff39dd7ee57d0de4f3f8d56b728378e9ae6730
