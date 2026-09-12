# Changelog

All notable changes to **Beads Viewer (`bv`)** are documented here. Versions are listed newest-first, with GitHub Releases distinguished from tag-only versions.

Scope window: this update verifies `v0.24.0..v0.24.1` and the post-release
commits through [`80450e34`](https://github.com/Dicklesworthstone/beads_viewer/commit/80450e345e6b2061fd1e17c6eee007bcb49d56c6).
The September 10 canonical-source and Cass repairs, September 11 performance
measurement, and subsequent enhanced-priority optimization are covered separately below.
Earlier entries are retained without a fresh historical audit. The recent entries
are checked against Git diffs, tags, live GitHub Release metadata, Beads records,
and release receipts; [research notes](CHANGELOG_RESEARCH.md) record coverage.
Release dates use UTC publication dates. `Unreleased` describes changes after
the latest tag, including installer changes usable with already released binaries.

## Release Timeline

| Version | Date | Publication | Orientation |
|---|---|---|---|
| [Unreleased] | — | Commits after v0.24.1 | Workflow dependency support, dashboard readiness/rankings/simulations, directed Flow inspection, and installer/documentation repairs. |
| [`v0.24.1`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.24.1) | 2026-09-08 | GitHub Release | Reuse loaded source hashes and avoid waiting for a busy analysis-cache writer. |
| [`v0.24.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.24.0) | 2026-09-07 | GitHub Release | Latency campaign across analysis, loader and TUI, graph-navigation and causality repairs, release-gate isolation, and the x/text GO-2026-5970 dependency fix. |
| [`v0.23.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.23.0) | 2026-09-04 | GitHub Release | Reality Check hardening sweep, 10-stage release gate, proactive drift alerts, typed env registry, docgen, and full tracker completion. |
| [`v0.22.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.22.0) | 2026-08-25 | GitHub Release | Makes snapshot delivery pointer-based and incrementally rebuilds safe list changes, with measured UI latency and allocation reductions. |
| [`v0.21.2`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.21.2) | 2026-08-24 | GitHub Release | Publishes the 50-pass performance campaign, verified binaries, checksums, SBOM, and corrected Nix guidance. |
| [`v0.21.1`](https://github.com/Dicklesworthstone/beads_viewer/tree/v0.21.1) | 2026-08-24 | Tag only | Staged the performance and license work; superseded before binary publication. |
| [`v0.21.0`](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.21.0) | 2026-08-23 | GitHub Release | Strict robot-count semantics, bounded history liveness, theme selection, and cache-path repairs. |

---

## [Unreleased]

### Priority recommendation performance

- Cascade simulations now reuse sorted dependency and hierarchy adjacency
  instead of rebuilding it at every step. Completion state and readiness checks
  remain specific to each query ([frontier reuse](https://github.com/Dicklesworthstone/beads_viewer/commit/01383eb6a3ab2f5bac5a95f7596aa3f89ecb510f)).
- Enhanced priority recommendations, used by `--robot-priority`, now reuse one
  completed analysis snapshot across the batch instead of rereading the analysis
  cache for every issue. Scoring, explanation fields, ordering, and output caps
  are preserved ([snapshot reuse](https://github.com/Dicklesworthstone/beads_viewer/commit/c3091424a5895e62e1aa8c59ca8ca41fa7f24ee0);
  [full-output regression](https://github.com/Dicklesworthstone/beads_viewer/commit/10666a6a74980ba4c39fa0f67081cab510b0e0cf)).

### Performance verification and documentation

- Failed CLI measurements now report sample identity, elapsed command time,
  and cancellation state captured before diagnostic files are written. Slow
  artifact writes cannot retroactively make a command failure look like a
  timeout ([diagnostics](https://github.com/Dicklesworthstone/beads_viewer/commit/3bc5c15c481b299a4fbec20ca9f1ab513dd1f096)).
  The original incomplete P1 matrix remains open.
- The September 11 source measurement passed all 144 current UI cohorts at
  the original 50 ms p99 and delivered-handler limits; the worst cohort p99
  was 46.472 ms. The overall matrix failed when a baseline process was killed
  in the last timed CLI cohort, leaving 70 of 72 records complete. The README
  and [performance guide](docs/performance.md#september-11-2026-measurement-attempt)
  distinguish those outcomes and keep the P1 work open. This adds measurement
  evidence, not a runtime optimization or release qualification.
- Documentation now distinguishes settled navigation from startup and
  background preparation, and makes dependent readiness conditional on
  remaining prerequisites and eligibility. The v0.22.0 entry names the
  50 ms interaction target while preserving its original measurements.

### Workflow data and static dashboards

- SQLite dashboard rebuilds construct a private database before publishing it.
  A failed construction preserves the last good database, and readers no
  longer lock a partially populated export. On Windows, an open reader may
  still prevent replacement; that failure preserves the prior database.
- Source discovery now honors `issues.jsonl`, then `beads.jsonl`, then
  `beads.base.jsonl` before comparing the selected export with SQLite and
  worktree sources. A newer sync snapshot cannot replace current issue state,
  and an empty export stays empty. Sidecar-only directories no longer bypass
  the filename allowlist through the fallback loader; explicit file overrides
  remain available. Human commands now report ignored left/right merge
  artifacts while robot stderr stays clean (`bv-mvvu`, `bv-uoyj.1`).
- SQLite live refresh now detects committed WAL updates in event and polling
  modes. TUI and watched Pages exports update while the writer remains open;
  checkpoint removal of the WAL is handled without reporting the database
  itself as removed (`bv-oonu.21`;
  [WAL refresh](https://github.com/Dicklesworthstone/beads_viewer/commit/cd100c661a5bb7ce751b6e10a45c5af16af34ce5)).
- Live TUI refreshes and single-repository watched exports now use the source
  that successfully loaded at startup. A corrupt newer file can no longer
  redirect the watcher away from its valid fallback. Explicit JSONL and SQLite
  selection remains supported. Historical `--as-of` exports reject
  `--watch-export` before writing files (`bv-oonu.20`;
  [source repair](https://github.com/Dicklesworthstone/beads_viewer/commit/ccc166e9c43f200e3c8d5b26136a4c25a2c0ebe8)).
- SQLite and JSONL loading now retain nonblank custom workflow statuses and
  relationship types. `conditional-blocks` and `waits-for` affect blocking
  analysis; custom statuses do not automatically become claimable
  ([loader change](https://github.com/Dicklesworthstone/beads_viewer/commit/55ec82b8ceb099222de99e2fdbe23329c91e9bd1)).
- Recipe validation now rejects a blank `filters.status` entry with a message
  that says so, instead of calling it an unknown status. Since the loader change
  above the check has accepted custom workflow states such as `done`; only the
  error text and the `Validate` documentation still described a closed
  vocabulary.
- Static dashboards now include both blocking variants and legacy untyped
  dependencies in their relationship lists and browser graphs. Active ID lists
  agree with blocker counts, including after either endpoint closes, so blocked
  work no longer appears ready because its relationship type was omitted.
- In issue details, `h` now navigates to a prerequisite and `l` to a dependent,
  with stable ordering. The export and real Chromium regressions are tracked by
  `bv-oonu.12`
  ([dashboard repair](https://github.com/Dicklesworthstone/beads_viewer/commit/1c768eacdfcec4937c018b3c1e5febb7091cfc7c)).
- “Simulate Close” now follows dependencies in the correct direction and shows
  the engine's actual direct and downstream counts. Priority picks and cascade
  cards show their calculated gains. Deleted prerequisites and closed rows
  excluded from the dashboard remain resolved in both browser graph engines.
- Graph simulation and reset use the bundled renderer's redraw API; cancelling,
  reloading or cleaning up a simulation cancels its pending animation callbacks.
  The rebuilt graph WASM preserves unrelated graph metrics. Real Chromium
  regressions and source-rebuild evidence are tracked in `bv-oonu.13`
  ([simulation repair](https://github.com/Dicklesworthstone/beads_viewer/commit/40a7cd07d0dab2d8a2a7a8c0ee1874b120fe3d9a)).
- HITS hub and authority panels now display the bundled engine's scores and
  open their ranked issues. Their JavaScript consumer previously read field
  names that the engine does not return (`bv-oonu.14`).
- Dashboard ready counts, quick wins and Ready/Blocked filters now use the
  full-source readiness snapshot. Missing or filtered prerequisites, inherited
  parent gates and deferral no longer disappear from eligibility checks;
  resolved prerequisites still satisfy them when omitted from the display.
  Ready cards include eligible in-progress work. Active-node counts include
  each unresolved issue once and remain numeric when a status group is absent
  (`bv-oonu.15`;
  [rankings and readiness repair](https://github.com/Dicklesworthstone/beads_viewer/commit/51d25a80991e2488f85eb030ceb7d8d126d8f9ba)).
- Direct dashboard exports now apply recipes, including sorted `max_items`
  selection. Watched exports reapply repository, label and recipe filters to
  each reload instead of expanding to the full dataset. Hidden prerequisites
  still govern readiness, newly matching issues enter the selection, and an
  empty selection clears old rows (`bv-oonu.16`;
  [export scope repair](https://github.com/Dicklesworthstone/beads_viewer/commit/d10d341dfb46412cffdcee7641b3f67a3b53b015)).
- Missing or filtered dependency endpoints no longer crowd real issues out of
  WASM-backed ranking panels. HITS, k-core, slack and metric fallback lists
  apply their limits to exported issue rows while preserving full-graph scores
  (`bv-oonu.17`;
  [ranking limit repair](https://github.com/Dicklesworthstone/beads_viewer/commit/3a56b9224ab621f2b178caf2453c4c1685efa62c)).
- Interactive HTML and static SVG/PNG graph exports now honor recipes,
  including custom files, sorted limits and label/repository intersections.
  Actionable selection retains full-source prerequisite checks; an empty
  recipe selection reports an error without creating a graph (`bv-oonu.18`;
  [graph recipe repair](https://github.com/Dicklesworthstone/beads_viewer/commit/75be8362f67709e74266f709644b7b4465b706c7)).
- Dashboard Priority Picks now restrict candidates before optimizing gains.
  Missing issue IDs cannot be selected or implicitly completed to inflate
  another issue's gain. Cascade suggestions and actionable lookup use the
  exported full-source readiness snapshot. The rebuilt WASM preserves results
  for callers without candidate restrictions (`bv-oonu.19`;
  [candidate selection repair](https://github.com/Dicklesworthstone/beads_viewer/commit/5efb1daf3f3f61fe2122ba4c5033deb438b4db2f)).
- Label-scoped insights rank hypothetical completions only for selected issues;
  neighboring context can no longer take a result slot. Top-k candidate counts
  use the same selection, while graph metrics and unresolved prerequisites
  retain their context (`bv-xbvo.11`;
  [scoped ranking repair](https://github.com/Dicklesworthstone/beads_viewer/commit/f24e2df76c0af757f0a0322bccd78f1d62ed63c5)).
- Capacity reports now use full-source readiness and intersect global selection
  with `--capacity-label`. Missing prerequisites, inherited parent gates and
  parked/deferred work no longer produce false actionable counts. Direct
  bottlenecks exclude non-blocking relationships, resolved endpoints and
  duplicate pairs; result ordering is stable. The duration calculation remains
  a heuristic (`bv-xbvo.12`;
  [capacity repair](https://github.com/Dicklesworthstone/beads_viewer/commit/ab144521b77114c45341733d5179009962cfb193)).
- Capacity path calculation now shares suffix results on acyclic graphs,
  avoiding exponential enumeration of overlapping paths. It preserves the
  chosen path, ties and estimates; reachable cycles retain the original
  exhaustive search. A dense 26-issue fixture fell from a 3.55-second median
  to 64 ms across ten measured runs per binary (`bv-xbvo.14`;
  [capacity performance repair](https://github.com/Dicklesworthstone/beads_viewer/commit/06cc108f3dbb6df725e1616bfba1c079ad7b84b2)).
- Forecasts now honor global selection intersected with the forecast label
  and sprint. A single requested issue must also pass these filters; an
  excluded ID returns an error. Selected estimates retain their loaded
  dependency and closure context (`bv-xbvo.13`;
  [forecast scope repair](https://github.com/Dicklesworthstone/beads_viewer/commit/9de473f47d66ec36c2103af928915f9fef5204b2)).

### Build requirements

- Source builds now require Go 1.26 or newer, with Go 1.26.8 selected by
  `go.mod`. Both source installers enforce the same minimum. The Nix flake
  moves to the 26.05 package set, which supplies Go 1.26.7.
- Terminal rendering, Unicode normalization, HTML parsing and graph-image
  dependencies are updated. The four local dependency patches remain in place;
  [upgrade notes](UPGRADE_LOG.md) record the individual transitions and tests.

### Vendored dependency patches

- The four locally patched dependencies (`chroma`, `glamour`, `reflow`,
  `go-json`; see `docs/PROVENANCE.md`) now live under `third_party/` as
  complete Go modules reached through `replace` directives in `go.mod`, so
  `go mod vendor` copies the patched sources instead of silently reverting
  them. Previously the patches existed only as hand edits inside `vendor/`,
  and a routine `go mod vendor` reverted all four, including the go-json
  decoder-cache race repair that no `-race` test exercises. A new e2e
  check fails when `vendor/` and `third_party/` disagree, and
  `docs/RELEASING.md` documents editing, upgrading and retiring a patch.
  The vendored package sources remain byte-identical to the previously
  committed patched files.

### Dependency inspection and documentation

- Cass session lookups now run in the background, keeping navigation and
  resizing available while Cass responds. `V` or Esc cancels a pending lookup;
  results from an older selection or dataset cannot open a stale modal.
  Refreshed data receives a fresh correlation cache. `V` also works from the
  detail pane, and closing the modal restores its originating view (`bv-xiyd`;
  [responsiveness repair](https://github.com/Dicklesworthstone/beads_viewer/commit/4ffc37c6f162d3e5b9de6f35511744ca12a0a2eb)).
- Cass session lookup now reads the actual `hits` response and requests the
  title, preview, workspace and millisecond timestamp fields used by the UI.
  Pressing `V` can show sessions from a searchable stale or rebuilding index
  while retaining its health warning. Cached sessions keep their computed
  scores and match reasons; failed or partial lookups remain retryable instead
  of becoming cached empty results (`bv-8phk`;
  [search adapter](https://github.com/Dicklesworthstone/beads_viewer/commit/fe88cfb7),
  [cache repair and live verification](https://github.com/Dicklesworthstone/beads_viewer/commit/c008a9b7f955680e17baebcc0e35de83efe3804d)).
- README examples now place impact-network fields under `network`, show
  priority reasoning as an array and use the actual alert fields. Flow Matrix
  rows are documented as blockers and columns as dependents; saved-baseline
  drift checks are distinguished from Git-revision comparisons. Cass scoring
  and modal controls now describe the implemented behavior
  ([documentation corrections](https://github.com/Dicklesworthstone/beads_viewer/commit/b543d376)).
- Generated agent instructions now use `br update <id> --claim --json`,
  matching the README and assigning the current actor as work starts.
  Instruction version 6 makes existing version 5 blocks eligible for refresh
  through `--agents-add`; surrounding user instructions remain intact.
- Flow Matrix drilldowns now show the actual blocker and dependent for each
  relationship, exclude unrelated issues sharing a label, and deduplicate pairs
  spanning multiple labels. Enter inspects either endpoint without changing the
  active recipe or selected work; Escape returns to the relationship.
- Open relationships and endpoint details update after snapshots and file
  reloads, retain surviving selection by ID, and remove obsolete relationships.
  Unicode and long IDs fit narrow drilldown rows. The implementation and Linux
  terminal journey are tracked by `bv-apal.12`/`bv-apal.13`
  ([implementation and regression tests](https://github.com/Dicklesworthstone/beads_viewer/commit/b6e21d22f6098a78553aed2a365526bf98730fd6)).
- README now matches the recipe-picker key, cass health/count indicators and
  separate history modal, plan/history JSON fields and duration units, dependency
  direction, and binary versus source requirements. `BV_INSIGHTS_MAP_LIMIT`
  documentation now gives its existing default of 200; zero and invalid values
  use that default. A real CLI regression checks all five documented limit cases.
  These corrections are part of the still-open `bv-apal.3`/`bv-apal.4` workstream;
  they do not establish the remaining performance or native-platform claims.

### Windows installation

- Version-check failures now retain stdout and stderr, capped at 4,096
  characters per stream. The original ten-second execution deadline remains;
  pipe draining is bounded to one additional second, including when a child
  inherits the pipes. Truncated version output is rejected before installation
  (`bv-oonu.9`;
  [diagnostic repair](https://github.com/Dicklesworthstone/beads_viewer/commit/80450e345e6b2061fd1e17c6eee007bcb49d56c6)).
  Both README install commands now pin this reviewed script. Portable process
  regressions and native PowerShell 5.1 version/mismatch checks pass; the original
  native source first-start failure remains open.
- Suppress download progress locally inside `Install-FromRelease`, avoiding
  the redirected-download stalls observed with Windows PowerShell 5.1 without
  changing the caller's preference. The native harness now checks the existing
  `diagnostic_top_pick` and metadata-free claim refusal instead of an obsolete
  top-level ID ([`3ca2176f`](https://github.com/Dicklesworthstone/beads_viewer/commit/3ca2176f11cc6106be452815e03fc4164b581761)).
  This fixes installer behavior and its test; it does not change robot output.
- README's Windows commands initially pinned that installer revision
  ([`87756815`](https://github.com/Dicklesworthstone/beads_viewer/commit/87756815cb55e8450cc559a74b29677327f830b6)).
  The pinned source option uses a verified tagged checkout and vendored
  dependencies; the stale reference to its older `go install` path is corrected.
  The installer fix follows v0.24.1's immutable tag and is already usable with
  its published binaries.

### Verification and remaining limits

- With the repaired installer, the complete default native Windows suite
  passes against public release archives: installation, readiness,
  update/no-update, and preservation of the installed executable and user PATH
  on failure. Readback covers 28 command logs, eight capability results, and
  five specific rejection cases; it does not count a transport timeout as a
  successful rejection.
- An initial native Windows source installation passed, but a later optional
  source run exceeded the unchanged 10-second first-start guard. Its retained
  executable succeeded on a second diagnostic invocation; the original failure
  remains unresolved. Native macOS amd64/arm64 and Linux arm64 execution,
  native Homebrew installation, and a supported Nix build remain unverified.
  The broader installation workstream
  [bv-oonu.10](https://github.com/Dicklesworthstone/beads_viewer/blob/7983ee3f9a4d5d2cf615dbb9a919256a3e74c2fc/.beads/issues.jsonl#L454)
  stays open. See the
  [retained release findings](https://github.com/Dicklesworthstone/beads_viewer/commit/7983ee3f9a4d5d2cf615dbb9a919256a3e74c2fc).

---

## [v0.24.1] -- 2026-09-08 (Release)

This patch removes avoidable hashing and cache-lock waiting from robot commands
while preserving their data-hash scope and computed analysis results.

### Fixed

- Optional analysis-cache publication no longer waits for another process's
  writer lock. A contended writer leaves the existing cache entry intact and
  returns the computed result; a later request can publish successfully
  ([`f719c41a`](https://github.com/Dicklesworthstone/beads_viewer/commit/f719c41a95d3565c736e4c20267d7568990b09ba)).
  Linux and native Windows regression tests exercise the real lock, preservation
  of the existing entry, and a successful later publication.

### Performance

- Robot commands reuse the hash computed while loading an unchanged single
  source, including historical snapshots, avoiding a second fingerprint pass.
  Workspace aggregates, tombstone-bearing sources, and `--repo` filtering still
  trigger recomputation. The envelope hash remains scoped after `--repo` and
  before `--label` or `--recipe`; 15 scenarios exercise both robot plan and
  triage ([`6c8a4474`](https://github.com/Dicklesworthstone/beads_viewer/commit/6c8a4474)).

### Distribution

- Published through DSR using locally packaged GoReleaser artifacts, without
  GitHub Actions or repository dispatch. Linux amd64/arm64, macOS amd64/arm64,
  and Windows amd64 archives identify the clean tagged revision
  [`3e4e61c9`](https://github.com/Dicklesworthstone/beads_viewer/commit/3e4e61c91a74dafe211d3f6a62f3c2919969657c),
  Go 1.25.5, and disabled CGO. The 14 assets include checksums, a
  [sealed gate receipt](https://github.com/Dicklesworthstone/beads_viewer/releases/download/v0.24.1/release-gate-receipt.json),
  and an SPDX SBOM for the Linux amd64 binary. No minisign signature was produced.
- [Homebrew](https://github.com/Dicklesworthstone/homebrew-tap/commit/cae0685b4c703d5e4ba22e7c093511ffdf72d9d6)
  and [Scoop](https://github.com/Dicklesworthstone/scoop-bucket/commit/4fddb86e07486cd1bf1d2ad9e76c7a120ac14779)
  were advanced from v0.22.0 to v0.24.1 using the verified archive hashes.
  Go module publication and the tagged Nix flake were checked against the same
  source revision. These publication checks do not establish native Homebrew
  installation or a supported Nix build.

### Verification

- All ten release-gate stages passed on the clean tagged commit; no gate stage
  was skipped. All 14 uploaded assets were downloaded and matched by size and
  SHA-256 before publication. Native Linux installation, readiness,
  update/no-update, and failed-install preservation checks passed. Windows
  installer results and remaining native-platform limits are recorded under
  Unreleased because the installer repair followed the tag.
- The separate pre-release performance campaign retained 288 UI records, 72 timed CLI
  records and 36 fixed-clock comparisons. Its original exact-output check
  failed because the baseline reports v0.23.0 and the candidate v0.24.0;
  readback of all 144 outputs found only those 72 version-field differences.
  This accounts for the failure without changing the original result or
  claiming the broader performance campaign complete. The latency workstream
  [bv-apal.1](https://github.com/Dicklesworthstone/beads_viewer/blob/7983ee3f9a4d5d2cf615dbb9a919256a3e74c2fc/.beads/issues.jsonl#L283)
  remains in progress; the distribution workstream
  [bv-l76l](https://github.com/Dicklesworthstone/beads_viewer/blob/7983ee3f9a4d5d2cf615dbb9a919256a3e74c2fc/.beads/issues.jsonl#L422)
  is complete. [Release verification details](docs/RELEASING.md#native-installation-and-package-stores)
  preserve the original failures and execution limits.

---

## [v0.24.0] -- 2026-09-07 (Release)

### Performance (latency campaign `bv-apal.1`)

- **Analysis:** canonical graph hashing streams through successors instead of boxing and sorting every edge, so allocations scale with nodes (`fbc41526`); `ComputeDataHash` / `ComputeIssueDiff` reuse one fingerprint writer per call, 1,289 -> 777 allocations per 256-issue aggregate (`c48bd53c`); the ready set is reused across parallel-gain candidates and marginal unlock IDs are selected without copying issues (`9a72543d`, `187f7525`); blocker IDs sort without reflection (`347134f1`); the regenerable analysis cache no longer forces durable flushes (`4b018e1b`); the drift `Calculator` gains `ReuseAnalyzer` to share precomputed readiness and topology across runs (`1713989c`); Phase 2 analysis is size-tiered with a deterministic betweenness approximation that reduces in sample order (`341a0005`).
- **TUI:** the visible critical chain is computed once, at snapshot construction, instead of on the event loop (`735f246a`, `30417526`); full list rows are initialised in place and incremental row copies are avoided (`934da756`, `e3dcc7ce`); owned readiness data is compacted (`84cf799b`); background Phase 2 preparation no longer mutates the active model (`1c5d55f9`); snapshot diffing and markdown element layout allocate less (`f2bad78d`).
- **Loader / search:** one default 10 MiB JSONL reader is retained between parses while caller-owned buffers keep their semantics (`7f708334`); streaming readers and concurrency pooling in `internal/datasource` and `pkg/loader` (`3b4df94d`); metrics vector-search caching layers (`19323918`).
- **Vendored renderers:** `strings.Builder` replaces quadratic concatenation in chroma `coalesce.go` and glamour `ansi/elements.go`, and reflow `padding.go` uses `runewidth.RuneWidth` (`5e16bff3`). These are hand patches under `vendor/`; a bare `go mod vendor` reverts them.

### Fixed

- Graph navigation and historical causality restored: pan, scroll and expansion follow bounded visible dependency paths with a deterministic highlighted chain; historical transition and dependency authority survive extraction and caching, observed waits carry explicit uncertainty, and causal history is bound to the selected revision and source path (`93b90959`).
- Decoder cache synchronised and pseudo-versions filtered (`40644bd9`); XFetch cache refresh keyed on the actual expiry (`8285b6f6`).
- SQLite export creates its schema atomically (`a4f8245b`).
- Robot parallel-gain output omits elapsed timing when the clock is pinned, keeping envelopes reproducible (`5814bed8`).
- Graph WASM build remaps compiler source paths (`8cd63299`); the frozen 1,000-issue benchmark input is tracked so a clean checkout can run the release gate (`09fed4e9`).

### Changed

- Robot registry outputs, `defer_until` flags and CLI fixtures harmonised (`54db481b`); the interactive HTML graph export embeds the robot envelope (`19323918`); readiness scopes modelled with projection and authority boundaries, richer triage recommendations, cycle detection and ETA prediction (`341a0005`); graph-analysis invariants, vector indexing and SQLite export schema validation hardened (`3db9dd04`); ingestion pipelines, `defer_until` parsing and workspace path resolution hardened (`3b4df94d`).
- Release engineering: goreleaser dist output isolated to `/tmp/bv-dist` with `CGO_ENABLED=0` and `GOWORK=off` (`a90029b8`, `0aca294f`); `install.sh` / `install.ps1` handle native packaging with checksum verification and isolated release verification; `scripts/release_gate.sh` writes logs and receipts outside the checkout; the `bv-graph-wasm` Rust toolchain is pinned; installer, gate, WASM and dashboard smoke tests live under `tests/scripts` (`0aca294f`); smoke and installer artifacts are retained as evidence (`8a6386e9`).
- Tests: end-to-end coverage for robot scoping, search relevance, recipe execution, export flows and board/swimlane interaction, plus benchmark workloads isolated from tree state and a frozen real correlation history (`087a847f`, `1a400830`, `257427df`, `f9d950a0`).

### Dependencies

- `golang.org/x/text` v0.38.0 -> v0.41.0 (fixes GO-2026-5970, reachable through `norm.Form.Properties`), `golang.org/x/image` v0.42.0 -> v0.45.0, `golang.org/x/net` -> v0.58.0; `vendor/` regenerated with the three hand-patched files above preserved (`1e8acace`, #200).

### Housekeeping

- Version metadata bumped to v0.24.0 (`flake.nix`, `pkg/version` fallback, README install examples, installer test defaults).

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
- **Measure interactive latency against the 50 ms target.** Five update-only keypress runs measured
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

[Unreleased]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.24.1...HEAD
[v0.24.1]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.24.0...v0.24.1
[v0.24.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.23.0...v0.24.0
[v0.23.0]: https://github.com/Dicklesworthstone/beads_viewer/compare/v0.22.0...v0.23.0
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
