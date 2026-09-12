# Dependency Upgrade Log

**Date:** 2026-09-12 UTC | **Project:** beads_viewer | **Status:** in progress

## September release update

The user requested `library-updater` followed by a new release. Each selected
dependency is researched and tested separately. Local replacements and
pseudo-version pins are preserved except parent-required transitions recorded
below; modules present only in dependency tooling
graphs are distinguished from this project's declared requirements. The raw
registry inventory is `/data/tmp/bv-release-module-inventory-20260911.json`.
Release preparation uses DSR/RCH and the complete repository release gate;
GitHub Actions must not run. Existing incomplete P1/native evidence remains open.

- [x] Inventory Go requirements and upstream stable versions.
- [ ] Research and update each eligible declared requirement, testing each.
- [x] Inspect the two Rust/WASM manifests and embedded asset requirements.
- [ ] Run final full tests and vulnerability audit.
- [ ] Update changelog/version and pass the complete release gate.
- [ ] Build and verify all five configured release targets.
- [ ] Publish GitHub release, Homebrew tap and Scoop bucket; verify installers.

### github.com/charmbracelet/x/ansi v0.11.7 → v0.11.8

Affected-package and focused integration testing passed; final full-suite
qualification remains pending. Upstream source
is tag `ansi/v0.11.8`, commit `00c6608f106b9c6cd8a1a77156f7901f41265e64`.
[Upstream comparison](https://github.com/charmbracelet/x/compare/ansi/v0.11.7...ansi/v0.11.8).

- The upstream patch changes `WcWidth` codepoint summation and corrects Kitty
  `Quite` to `Quiet` while retaining the deprecated writing field. No direct
  BV callers of those APIs were found; the active wrapping paths use grapheme
  widths. No migration was needed for the candidate.
- `go get` changed only the ANSI requirement and its two checksum lines.
  Vendor is deliberately not refreshed yet; candidate tests use `-mod=mod`.
- Initial RCH attempts failed downloading Go 1.25.5, before compilation. A
  fresh worker SDK copy was verified against the existing SDK's Go executable
  SHA256, `d29b19f04e57fa2f35d4725a8743b663289ac29832128a235c4a3f76f885b150`.
- Baseline revision `4a68abd8` reported failures in
  `TestAgentsE2E_AcceptFlow` and `TestExportPagesWatchUsesLoadedSource`.
  The former still expects `br --status=in_progress`, although the production
  blurb and its unit tests use `br --claim`. The E2E assertion is corrected in
  the canonical checkout; focused verification subsequently passed below.
- The full local disk interrupted baseline result capture. All candidate
  non-E2E packages passed; the completed E2E run failed its stale assertion,
  hit SQLITE_BUSY during watched export, then exhausted the aggregate ten-minute
  timeout while running the 5k/10k search evaluation. Remote exit was 1 after
  822.967 seconds (including compilation). Neither source has a passing suite.
- A full clone was staged at `/tmp/bv-release-20260912` during disk exhaustion.
  Work subsequently returned to the canonical checkout; that retained clone
  is now stale. No source or cache files were deleted.
- RCH status at 00:55 UTC reported zero healthy workers and zero available
  slots. A focused retry was refused with RCH-I001; local fallback is disabled.
  Agent Mail also refuses writes with DISK_FULL. Logs are under
  `/tmp/bv-release-baseline-fixes*-20260912.log` and
  `/tmp/bv-rch-status-20260912.json`.
- About 1 GB became available externally; no files were deleted by this agent.
  A successful independent probe with `XDG_STATE_HOME` on `/tmp` isolated the
  SSH-state failure to local storage. Scheduler admission still failed at
  01:04 UTC despite recovering circuits; no local build was substituted.
- Prepared a SQLite publication fix in the existing exporter: construct a
  private database, close it and rename it onto the published path. Added a
  duplicate-primary-key failure regression proving preservation of the previous
  database, followed by successful replacement. Remote verification is pending.
  This does not make the whole JSON/SQLite/chunk bundle atomic. On Windows an
  open SQLite reader can still prevent rename; the old database is preserved
  and the exporter returns an error. Native Windows watch success is unverified.
- Latest `x/image` 0.46.0, `x/net` 0.59.0 and `x/sync` 0.23.0 require Go 1.26.0
  according to their official proxy manifests. That toolchain change must be
  tested separately before advancing these requirements.

### golang.org/x/sys v0.47.0 → v0.48.0

After Go 1.26.8 passed all `pkg`, `cmd`, and `internal` tests, the isolated
`x/sys` update passed the UI, loader, watcher, analysis, instance, and export
suites remotely. No API migration was required. RCH exit 0 in 118.773 seconds;
log `/tmp/bv-upgrade-sys-tests-20260912.log`, overlay fingerprint
`16ceba78f516c780682e0c3b54e4bc26e5ecd909bf884a4cdd4e86b4decfda88`.

### golang.org/x/sync v0.22.0 → v0.23.0

Upstream adds rejection of negative semaphore capacity; BV uses errgroup and
singleflight and needs no migration. The workspace, correlation, loader, and
search suites passed with `-race -count=1`, remote exit 0 in 108.587 seconds.
Log: `/tmp/bv-upgrade-sync-tests-20260912.log`.

### golang.org/x/term v0.45.0 → v0.46.0

The upstream change preserves partial ReadLine bytes on error; BV's terminal
detection API is unchanged. Full cmd/bv, UI and export suites passed remotely
in 205.834 seconds. Log `/tmp/bv-upgrade-term-tests-20260912.log`, overlay
`5a17db0541e6c250eea08b7f714f7997da67a674d5d4de27fac80aa5aa922a08`.

### golang.org/x/text v0.41.0 → v0.42.0

Upstream normalization corrections cover non-BMP/Hangul, combining marks and
short buffers. No BV API migration was needed. Full UI/export suites and the
upstream normalization/cases suites passed remotely in 111.392 seconds. Log
`/tmp/bv-upgrade-text-tests-20260912.log`, overlay
`56796295a5b0ab4b4f3d50ff047688712190bf18c4fc87e53501078922c2c950`.
The solver downloaded tools 0.49/mod 0.41 graph requirements; neither became
a newly declared BV requirement.

### golang.org/x/image v0.45.0 → v0.46.0

The upstream diff only changes module metadata. BV's full export suite passed
remotely in 70.145 seconds, with no code or expectation changes. Log
`/tmp/bv-upgrade-image-tests-20260912.log`, overlay
`6ce128ec57bb2163dfa12400f09181a8f2e3f7e843accb38c94374b87cfa84ca`.

### golang.org/x/net v0.58.0 → v0.59.0

BV uses the HTML packages; the selected parser changes preserve semantics.
The full UI/export and upstream html/atom/charset suites passed remotely in
140.561 seconds. Log `/tmp/bv-upgrade-net-tests-20260912.log`, overlay
`5df7a2057d29baaac2baabd37aa43e50b517f4fc49ea53f9a62256d4fef88753`.
Upstream HTTP/QUIC changes are not BV runtime improvements.

### github.com/charmbracelet/x/xpty v0.1.3 → v0.1.4

The updated helper requires Go 1.25 and fixes Windows process exit/cancellation
reporting. Upstream Linux process tests and BV's full UI suite passed remotely
in 98.697 seconds. No native Windows behavior is established by this result.
Log `/tmp/bv-upgrade-xpty-tests-20260912.log`, overlay
`a17d5547a1533210797a9ea99202416f5a859920cda94da39a9d67b1ade1c28b`.

### github.com/rogpeppe/go-internal v1.14.1 → v1.16.0

The module raises its Go floor to 1.25. The reachable fmtsort change uses the
equivalent reflect.Pointer name; no BV migration was needed. Upstream fmtsort
and kr/pretty plus BV recipe/workspace tests passed remotely in 40.153 seconds.
Log `/tmp/bv-upgrade-internal-tests-20260912.log`, overlay
`9175ad69db194a0d60438db9aaec14d4d9181b331d5ed3952a4f70c640f9e251`.

### modernc.org/gc/v3 v3.1.3 → v3.1.5

The generated-source parser budget increases with input size; no BV runtime
API changes. Upstream scanner tests passed 11,079 files. Parser tests passed
8,479 of 8,562 files, with 83 upstream-defined skips and no failures. The
match-all `-re=.` flag preserves corpus selection while disabling upstream
report-file rewriting. Remote exit 0 in 95.636 seconds; log
`/tmp/bv-upgrade-gc-tests-20260912.log`, overlay
`863a62d84cd470da6ef6fcfcb0f6daf7fc5c9dbd03a7b7b5b61de9ac903b3338`.

### SQLite v1.52.0 → v1.58.0 with its required support libraries

SQLite's own go.mod selects libc 1.75.6 and memory 1.12.1; that exact libc pair
is retained instead of unrelated latest 1.75.7. The parent also requires pprof
`v0.0.0-20260802141513-ef3492d7dac3`, the only intentional pseudo-pin change.
Full export/loader/UI race suites passed remotely in 229.297 seconds, followed
by the four watched-export/historical-rejection E2E cases in 138.439 seconds.
Logs `/tmp/bv-upgrade-sqlite-tests-20260912.log` and
`/tmp/bv-upgrade-sqlite-e2e-20260912.log` share overlay
`05775f20140181783ed26304db81ffda0c50d362692e079ee4be63d2e2244835`.
No new SQLite options or DSN behavior were introduced. Native Windows watch
replacement remains unqualified; final vendored/full release gates remain open.

### Dependency research (qualification tracked above)

- Go `x/sys`, `x/term`, `x/sync`, `x/text`, `x/image`, and `x/net` targets
  require Go 1.26.0. The intended toolchain is the maintained 1.26.8 patch.
  Update order is sys, sync, term, text, image, net after a separate toolchain
  qualification. Net selects crypto 0.57 in the module graph; BV vendors no
  crypto packages, so upstream SSH fixes are not a demonstrated BV security fix.
  Source comparisons: [sys](https://github.com/golang/sys/compare/v0.47.0...v0.48.0),
  [sync](https://github.com/golang/sync/compare/v0.22.0...v0.23.0),
  [term](https://github.com/golang/term/compare/v0.45.0...v0.46.0),
  [text](https://github.com/golang/text/compare/v0.41.0...v0.42.0),
  [image](https://github.com/golang/image/compare/v0.45.0...v0.46.0),
  [net](https://github.com/golang/net/compare/v0.58.0...v0.59.0).
  Text normalization changes merit Unicode rendering checks; BV's used term,
  sync, HTML and image APIs have no required migration.
- go-internal 1.16 raises its floor to Go 1.25. The reachable fmtsort change
  uses the equivalent reflect.Pointer spelling. xpty 0.1.4 also requires
  Go 1.25 and corrects Windows child-exit/cancellation reporting; Linux tests
  cannot qualify that Windows path. gc/v3 3.1.5 keeps Go 1.23 and increases
  its generated-source parser budget, without changing SQLite query behavior.
- `go mod why -m` confirms xpty is used by huh's tests, go-internal by the
  yaml/check test chain, and gc/v3 by libc/ccgo tests. They are requirements
  reachable through upstream tests, not imported BV runtime packages. Evidence:
  `/tmp/bv-module-why-20260912.log`.
- Release prerequisites staged separately on vmi1152480: Go 1.26.8 under
  `/data/tmp/bv-release-go1.26.8-20260912`, GoReleaser 2.18.1 and PowerShell
  7.6.6 under `/data/tmp/bv-release-tools-20260912`. Archive SHA256s matched
  official Go release metadata and GitHub release asset digests before unpacking.
  Both release tools executed their version commands successfully. No project
  build or gate pass is implied by these prerequisite checks.

- `go-colorful` 1.4.1 corrects the D50-to-D65 matrix. Its complete upstream
  comparison is `/tmp/bv-colorful-compare-20260912.json`.
- `go-isatty` 0.0.24 switches affected Unix terminal detection to TIOCGWINSZ,
  adds a Haiku stub and lowers its Go floor; comparison retained in
  `/tmp/bv-isatty-compare-20260912.json`.
- Rust graph updates should start with getrandom 0.4.3, which removes the
  WASI dependency chains, followed by a fresh lockfile inventory. Serde
  1.0.229 and serde_json 1.0.151 are candidates. Updating wasm-bindgen
  0.2.121 to 0.2.128 requires matching CLI, generated JS/WASM and provenance
  changes; the current source-verification script explicitly pins 0.2.121.
- Verified WASM tools are now staged on vmi1152480 beneath
  `/data/tmp/bv-release-tools-20260912`: bindgen executable
  `wasm-bindgen-0.2.128/wasm-bindgen` SHA256
  `dc9e4f1e03996c26fb8bfedfded73d81120a37251c3f19eb87bb460f1f89a5be`,
  and `binaryen-132/binaryen-version_132/bin/wasm-opt` SHA256
  `1014958e6f20d412f1542320b43970214b0fb1ed780595e8f7c0d8761ed53725`.
  Their version commands match the intended pins; the Rust nightly and wasm32
  target are already installed. No WASM source qualification is implied yet.

### Final Go module cleanup

Native `go mod tidy` completed after the SQLite tests and removed requirements
no longer needed by BV: gc/v3 and pprof, plus temporary transitive requirements
from explicitly testing upstream helpers. The selected SQLite module graph
still carries its own pprof revision; it is no longer a direct BV pin. Final
vendor and full-suite checks must qualify this tidied tree.

### Rust getrandom 0.4.2 → 0.4.3

Native Cargo updated only getrandom and pruned 26 obsolete package identities
after removing its WASI 2/3 dependency edges. Existing nightly pin and features
are preserved. Both Rust components' release-profile tests passed via RCH:
206 graph unit tests, 25 graph integration tests, and 3 scorer tests. Remote
exit 0 in 97.153 seconds; `/tmp/bv-upgrade-getrandom-tests-20260912.log`,
overlay `8f149245ff00d93db3fe5abbaa91a6a83c35ada1b73b569834e9f7cd98370545`.
The new Make target uses the graph's dated compiler pin selected by cwd.

### Rust serde_json 1.0.149 → 1.0.151

Updated in both locks. Upstream tightens enum object-key parsing; no BV API
migration was required. All 234 existing graph/scorer release-profile tests
passed remotely in 70.336 seconds; `/tmp/bv-upgrade-serdejson-tests-20260912.log`,
overlay `9f133a83df51f0e02a20e35c83b8980db1b97228ea8e41c96f6cb5082623753e`.

### Rust serde/core/derive 1.0.228 → 1.0.229

Both locks selected the matching serde trio and its required Syn 3.0.5.
All 234 existing graph/scorer release-profile tests passed remotely in
71.776 seconds; `/tmp/bv-upgrade-serde-tests-20260912.log`, overlay
`3a8fbf498d891738e48480770cbc8767cb5547b8c15114f5a649f34685ccb3c8`.
No application code or test expectations changed for the macro transition.

### Rust wasm-bindgen 0.2.121 → 0.2.128 cohort

Cargo selected matching macro/shared crates, js-sys 0.3.105, futures 0.4.78,
and test 0.3.78. Both Rust components passed all 234 existing release-profile
tests; `/tmp/bv-upgrade-bindgen-tests-20260912.log`, overlay
`420289a10e3135704b1c8e6b16ff4fa13a20de0f748619a94dbcd9b8d6de677f`.
The build script and prerequisite docs now pin the verified 0.2.128 CLI.
Embedded JS/WASM and manifest are intentionally still pending regeneration
after the remaining lock changes; native Rust tests do not qualify those assets.

### Rust async-trait 0.1.89 → 0.1.92

The helper adopts Syn 3 and corrects reference-receiver mutability handling;
Cargo removed the graph lock's final Syn 2 entry. All 234 release-profile tests
passed remotely in 84.976 seconds. Log
`/tmp/bv-upgrade-async-trait-tests-20260912.log`, overlay
`d9e019adb68460b85f49ab280e41ef7640a60870fff95819492662b852a20a23`.

### Rust proc-macro2 1.0.106 → 1.0.107

The compiler-floor adjustment requires no BV source migration. Both locks
updated, and all 234 release-profile tests passed remotely in 67.397 seconds.
Log `/tmp/bv-upgrade-procmacro-tests-20260912.log`, overlay
`ab33614e75c8a6c88cf34d6c024e45929944c4dfd3650ba8badb24ec6d2e9634`.

### Rust quote 1.0.45 → 1.0.47

Generated-token handling changed upstream; existing macro consumers needed
no application migration. All 234 release-profile tests passed remotely in
66.793 seconds. Log `/tmp/bv-upgrade-quote-tests-20260912.log`, overlay
`a456bce770a4041e25e35e8d7225163a1108cef3b81bf42b7ee929f210e069d4`.

### Rust memchr 2.8.0 → 2.8.3

Upstream fixes big-endian AArch64 and a lower-level unsafe API; no evidence
establishes that BV reaches the latter defect. Both locks updated and all 234
release-profile tests passed remotely in 84.714 seconds. Log
`/tmp/bv-upgrade-memchr-tests-20260912.log`, overlay
`e2393a27e573d75c4be52790ca49d3547bc47eeb1f28c745e6057604bfb01ac2`.

The concurrent commit sweep landed work through `2acf8f04` during the updates.
Those commits are retained, but are not release qualification. Subsequent RCH
tests use that base plus explicit overlays, including the now-smaller vendor
tree. The sweep was asked to hold further release commits/publication.

### Rust zmij 1.0.21 → 1.0.23

The float formatter now selects compressed tables at optimization levels s/z.
Both actual release profiles (graph s, scorer 3) passed all 234 existing tests
through RCH in 82.813 seconds. Log `/tmp/bv-upgrade-zmij-tests-20260912.log`,
base `2acf8f04`, overlay
`448d7a786dc5d0a6cdecd4516a0e6e6b780ae71bd8cfce88020653dbe92a2505`.

### Release execution remaining

- [ ] Rust: update getrandom 0.4.3 first and verify removal of obsolete WASI
  chains; then serde_json 1.0.151, serde 1.0.229 and bindgen 0.2.128 cohorts.
- [ ] Rust: reinspect the solver result, then update remaining eligible
  proc-macro2 1.0.107, quote 1.0.47, async-trait 0.1.92, memchr 2.8.3,
  zmij 1.0.23, bumpalo 3.20.3, rustversion 1.0.23, futures 0.3.34,
  libc 0.2.189, autocfg 1.5.1 and cc 1.4.5, testing each transition.
  Serde/bindgen require Syn 3.0.5; cc requires shlex 2.0.1 and
  find-msvc-tools 0.1.12. Preserve exact minicov 0.3.8 and parent-constrained
  r-efi 6.0.0/windows-link 0.2.1. These are researched targets, not updates.
- [ ] Generate and review both graph assets, copy actual receipt values into
  the manifest, then run fresh source verification and the existing two-home
  reproducibility/graph fixture harness. New Make targets dispatch the existing
  scripts through RCH; they are not yet execution evidence.
- [x] Verify the stale assertion correction and diagnose watched-export failure.
- [x] Complete affected-package verification for ANSI and the exporter fix.
- [ ] Run the final complete suite with the documented aggregate E2E budget.
- [x] Refresh vendor through a fresh staging directory, preserving old files.
  Old tree retained at `/data/tmp/bv-vendor-before-20260912`; native candidate
  at `/data/tmp/bv-vendor-candidate-20260912` copied into vendor. The result is
  159 MB versus 257 MB previously. Final patch-parity/full tests are pending.
- [ ] Validate new Make release targets through strict RCH using a complete,
  sanitized Git clone; transfer Git history and tracked Beads data.
- [x] Align source installers, README, Go tooling and Nix lock with the final
  Go minimum; retain historical benchmark toolchain identities unchanged.
- [ ] Resolve required UBS execution: installed runner and Go module have no
  supported retention option and remove temporary files. Its Go helper also
  compiles, so a complete scan needs RCH. Do not substitute a partial scanner.
- [ ] Preserve the complete original release gate, benchmark thresholds and
  provenance checks. Retrieve receipts and archives outside the source tree.
- [x] Resolve local disk exhaustion and fleet admission; continue monitoring space.

No version/tag bump or new release publication has occurred.

### Recovery and verification follow-up

- The old exporter fails the new preservation regression as intended:
  `failed rebuild changed published database`, in
  `/tmp/bv-release-sqlite-regression-old-20260912.log`. This establishes a real
  construction-failure bug independent of timing-sensitive watched-export reads.
- RCH recovered automatically after local disk space returned. Its transfer
  setup for the temporary clone changed vmi1152480's `/dp` alias to `/tmp`.
  At 01:18 UTC the incorrect link was preserved at
  `/data/tmp/bv-release-displaced-dp-20260912` and `/dp` was restored to
  `/data/projects`; source data was not changed by this repair. Capabilities
  were refreshed. Build commands now run from the canonical project root;
  `/tmp` is used for staging/cache/state, not as RCH's canonical project root.
- The corrected exporter, preservation regression, claim assertion and unchanged
  watch cases passed three times via strict RCH. Evidence:
  `/tmp/bv-release-fixes-canonical-restored-20260912.log`, remote exit 0.
  Base `42a208ab` and overlay fingerprint
  `caf3c490de1d94cf6c523f6cf48201a8eb2133ec2721576648fd814f49dcc373`
  identify the tested source. Full build and vet passed. Full UI/export packages
  passed with `-count=1` in 24.010/33.545 seconds, respectively;
  `/tmp/bv-upgrade-ansi-affected-tests-20260912.log` records remote exit 0.
- The final E2E gate now grants the aggregate suite 30 minutes. All 600 search
  observations, individual assertions and performance thresholds are preserved.
  Remaining dependency transitions use their affected package/integration suites;
  the complete suite and complete release gate remain mandatory on the final tree.
- Upstream SQLite 1.58.0 specifies libc 1.75.6 exactly. Its generated-code ABI
  compatibility warning makes independent libc 1.75.7 selection inappropriate.
  The paired transition also selects memory 1.12.1 and a newer pprof pseudo-version.
  Those solver-required changes must be recorded together when SQLite is updated.

### go-colorful v1.4.0 → v1.4.1

- [Upstream comparison](https://github.com/lucasb-eyer/go-colorful/compare/v1.4.0...v1.4.1)
  corrects the D50-to-D65 matrix. No API removal or BV migration is needed.
  BV and its local forks have no direct D50/ProPhoto callers.
- Complete UI/export suites passed with `-count=1`, 20.006/23.321 seconds;
  strict RCH exit 0 in `/tmp/bv-upgrade-colorful-tests-20260912.log`.
- GitHub repository Actions permissions were read back as `enabled: false`;
  release publication will use DSR without dispatch.

### go-isatty v0.0.22 → v0.0.24

- [Official comparison](https://github.com/mattn/go-isatty/compare/v0.0.22...v0.0.24):
  Unix detection uses TIOCGWINSZ to avoid an OSS ioctl collision; adds a Haiku
  stub and lowers its Go floor to 1.20. No BV API migration required.
- `go get` changes its requirement/checksums only. Upstream terminal tests and
  the full BV UI suite passed via RCH, 0.033/24.999 seconds, remote exit 0 in
  `/tmp/bv-upgrade-isatty-tests-20260912.log`; native Windows is not implied.

### go-runewidth v0.0.24 → v0.0.30

- [Official comparison](https://github.com/mattn/go-runewidth/compare/v0.0.24...v0.0.30):
  fixes spacing marks and grapheme wrapping, sums cluster widths capped at two
  cells, avoids zero-width Wrap division, and lazily initializes width tables.
  Adds TruncatePrefix; existing BV calls require no API migration.
- Complete UI suite and upstream width tests passed without changing rendering
  expectations, 24.431/1.965 seconds; strict RCH exit 0 in
  `/tmp/bv-upgrade-runewidth-tests-20260912.log`.

### fuzzy v0.1.2 → v0.1.3

- [Official comparison](https://github.com/sahilm/fuzzy/compare/v0.1.2...v0.1.3):
  adds iterator APIs and delegates FindFromNoSort through the iterator adapter.
  Existing ordering and match indices are preserved; Go floor remains 1.24.5.
  No source migration required. Full UI/search and upstream matching tests pass,
  20.731/0.129/0.046 seconds; remote exit 0 in
  `/tmp/bv-upgrade-fuzzy-tests-20260912.log`.

### goldmark v1.8.2 → v1.8.6

- [Official comparison](https://github.com/yuin/goldmark/compare/v1.8.2...v1.8.6):
  fixes fenced indentation, link punctuation, percent-escape decoding,
  truncated UTF-8 escaping and segment padding; SVG data URLs are now treated
  as dangerous. No BV API migration identified. Complete UI/export suites pass,
  23.807/44.215 seconds; remote exit 0 in
  `/tmp/bv-upgrade-goldmark-tests-20260912.log`.

### Export permissions review

Independent review found that the pending private-file exporter used Chmod(0644),
overriding a restrictive umask. The subprocess negative control failed as
expected: `database mode under umask 077 = 0644, want 0600`, remote exit 1 in
`/tmp/bv-release-sqlite-umask-negative-20260912.log`. The correction now builds
inside a private temporary directory, letting SQLite apply its original
creation-mode/umask semantics, then renames the closed database. The extended
regression checks both first creation and replacement under masks 077 and 022.
Three executions of SQLite export and existing watch tests passed via RCH,
43.367/86.161 seconds, remote exit 0; evidence is
`/tmp/bv-release-sqlite-umask-fixed-20260912.log`, overlay fingerprint
`545179ca30155477b866892c8d35988eaa5e735ae1a3b5e21344cb938a305588`.
This fixes the unpublished candidate; neither negative control counts as a
dependency compatibility failure.

### Go 1.26 language floor and Go 1.26.8 release toolchain

- [Go 1.26 release notes](https://go.dev/doc/go1.26): default Green Tea GC,
  stack-allocation improvements, TLS defaults, URL validation and HTTP redirect
  changes need actual integration tests, especially the local go-json fork's
  private runtime interfaces. No required production API migration found.
- [1.26.7-to-1.26.8 comparison](https://github.com/golang/go/compare/go1.26.7...go1.26.8)
  contains no newly listed security fix. The Go floor is a language minimum,
  not a recommendation to use an unpatched compiler.
- Source installer checks, README and AGENTS now require Go 1.26. The controlled
  Windows compiler fixture reports 1.26.8 so its wrong-binary test still reaches
  the intended promotion boundary. Historical performance identities are intact.
- Nix moves to the supported 26.05 branch, revision
  `21a67dc470149f337cecafbe965d8d252a390518`, with Go 1.26.7. Official Nix metadata
  established its NAR hash; upstream attributes evaluate for all four existing
  systems. Current unstable drops Intel macOS and was rejected. The actual
  updated BV flake subsequently evaluated for all four systems, with
  GOTOOLCHAIN=local, vendored modules and checks enabled. Exact flake/lock/go.mod
  file hashes matched worker staging and evaluated store source at
  `/data/tmp/bv-flake-evaluation-20260912.YytCLjfd` on hz3. No native Nix build
  has passed; missing nixbld and Intel macOS deprecation warnings are retained.
- Full pkg/cmd/internal suites passed through RCH with SDK 1.26.8, remote exit
  0 after 321.689 seconds; `/tmp/bv-upgrade-go126-tests-20260912.log` binds
  overlay `16731c4b40823d1be30a3a4afc297a4e9dcd8e68ea76daba2329d652419bdca2`.
  The manifest's Go x/* versions were unchanged during this toolchain test.

### Current candidate: x/sys v0.47.0 → v0.48.0

- Official comparison linked above fixes Linux Ifreq alignment and NetBSD AVX
  detection. BV's locking/filesystem interfaces have no required migration.
- Test UI, loader, watcher, analysis, instance and export paths after the update.

---

**Date:** 2026-06-08 | **Project:** beads_viewer | **Language:** Go | **Release:** v0.17.0

## Summary

- **Updated:** 7 direct Go dependencies (all minor/patch bumps of mature libraries) + transitive `golang.org/x/text` and re-vendored.
- **Method:** Staged by risk — `golang.org/x/*` + `go-runewidth` together (official, near-zero risk), then `modernc.org/sqlite` alone (FTS5 search engine), then `git.sr.ht/~sbinet/gg` alone (graphics/export). Full `go test ./...` gate after the batch; focused `pkg/search` + `pkg/export` (FTS5) gate after the sqlite bump.
- **Failed:** None. **Needs attention:** None.
- **Validation:** `go vet ./...` clean, `gofmt -l` clean, `go test ./...` 27 packages OK / 0 FAIL (with `BV_NO_BROWSER=1 BV_TEST_MODE=1 CGO_CFLAGS=-DSQLITE_ENABLE_FTS5`).

### Direct dependencies

- `git.sr.ht/~sbinet/gg`: `v0.7.0` -> `v0.8.0`
- `github.com/mattn/go-runewidth`: `v0.0.23` -> `v0.0.24`
- `golang.org/x/image`: `v0.40.0` -> `v0.42.0`
- `golang.org/x/sync`: `v0.20.0` -> `v0.21.0`
- `golang.org/x/sys`: `v0.44.0` -> `v0.46.0`
- `golang.org/x/term`: `v0.43.0` -> `v0.44.0`
- `modernc.org/sqlite`: `v1.50.1` -> `v1.52.0`

### Notable indirect dependency updates

- `golang.org/x/text`: `v0.37.0` -> `v0.38.0` (pulled by the x/* bumps)

Remaining outdated entries reported by `go list -m -u` are all transitive-only deps not required by `go mod why` (e.g. `gonum.org/v1/plot`, `honnef.co/go/tools`, `codeberg.org/go-latex/*`); left untouched to keep the release surface minimal.

---

**Date:** 2026-05-14 | **Project:** beads_viewer | **Languages:** Go, Rust/WASM

## Summary

- **Updated:** Go module/vendor dependencies and Rust/WASM lockfiles.
- **Skipped:** Unneeded modules that only appear in the broader module graph and are not required by `go mod why -m`.
- **Failed:** None.
- **Needs attention:** None.

## Local `/dp` Dependency Check

### github.com/Dicklesworthstone/toon-go

- **Current:** `v0.0.0-20260322013033-4564467a45fb`
- **Local `/dp` state:** `/dp/toon-go` is on commit `4564467a45fbb70f24b08cf500d7637b24e65e56`.
- **Action:** Preserved. The project is already using the latest local `/dp/toon-go` version.

## Go Updates

### Direct dependencies

- `github.com/fsnotify/fsnotify`: `v1.9.0` -> `v1.10.1`
- `github.com/mattn/go-runewidth`: `v0.0.21` -> `v0.0.23`
- `golang.org/x/image`: `v0.37.0` -> `v0.40.0`
- `golang.org/x/sys`: `v0.42.0` -> `v0.44.0`
- `golang.org/x/term`: `v0.41.0` -> `v0.43.0`
- `modernc.org/sqlite`: `v1.47.0` -> `v1.50.1`
- `pgregory.net/rapid`: `v1.2.0` -> `v1.3.0`

### Notable indirect dependency updates

- `github.com/alecthomas/chroma/v2`: `v2.23.1` -> `v2.24.1`
- `github.com/charmbracelet/x/ansi`: `v0.11.6` -> `v0.11.7`
- `github.com/dlclark/regexp2`: `v1.11.5` -> `v1.12.0`
- `github.com/lucasb-eyer/go-colorful`: `v1.3.0` -> `v1.4.0`
- `github.com/mattn/go-isatty`: `v0.0.20` -> `v0.0.22`
- `github.com/sahilm/fuzzy`: `v0.1.1` -> `v0.1.2`
- `github.com/yuin/goldmark`: `v1.7.17` -> `v1.8.2`
- `golang.org/x/net`: `v0.52.0` -> `v0.54.0`
- `golang.org/x/text`: `v0.35.0` -> `v0.37.0`
- `golang.org/x/tools`: `v0.43.0` -> `v0.45.0`
- `modernc.org/libc`: `v1.70.0` -> `v1.72.3`

## Rust/WASM Updates

### bv-graph-wasm

- `getrandom`: `0.2` with `js` feature -> `0.4` with `wasm_js` feature.
- Updated call site from `getrandom::getrandom` to `getrandom::fill`.
- Refreshed `Cargo.lock` to latest compatible stable versions.

### pkg/export/wasm_scorer

- Refreshed `Cargo.lock` to latest compatible stable versions.
- Added `rlib` crate type alongside `cdylib` so integration tests can import the crate while preserving WASM output.

## Validation

Final validation was run after updates:

- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go vet ./...`
- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go build ./...`
- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go test ./...`
- `cargo fmt --check`, `cargo clippy -- -D warnings`, and `cargo test` in both Rust/WASM crates.

Additional release-gate commands are tracked in the release session notes and final response.
