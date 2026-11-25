# Releasing bv

Release packaging requires a complete clean gate receipt for the exact source
commit. Publication requires verification of the sealed archive hashes and
separate authorization. The receipt does not replace the native-platform,
browser, or WASM proofs listed below.

## Security toolchain and dependencies

The release compiler is selected from `go.mod`: use its `toolchain` directive
when present, otherwise its exact `go` version. The current pin and source-build
minimum are **Go 1.26.11**. There is no redundant `toolchain go1.26.11` line:
`go mod tidy` removes a toolchain directive equal to the `go` directive. The
retained DSR/`act` workflow inputs use `actions/setup-go`'s `go-version-file`
support to select the same version explicitly.

Install that compiler on PATH before running the gate. Automatic toolchain
switching is disabled with `GOTOOLCHAIN=local`; the gate rejects an active
compiler that differs from the pin. It also rejects an affected compiler
version or `golang.org/x/net` below v0.62.0, and checks the vendored module
declarations. Update dependencies with Go's module commands, regenerate the
affected vendored sources, and retain the verified `go.sum` entries. Both the
Nix package and its development shell use the fixed Go 1.26.11 compiler.

The gate accepts only empty `GOFLAGS` or one positive parallelism setting
such as `-p=2`. This applies to both the caller environment and Go's persistent
environment file. Keep build settings in the tracked release configuration;
external overlays, alternate module files, tool wrappers and profiles are not
covered by the source receipt and are rejected.

```bash
scripts/release_gate.sh verify-toolchain
make release-audit
```

The audit installs pinned `golang.org/x/vuln/cmd/govulncheck@v1.9.0` outside
the checkout and runs source-mode analysis with CGO disabled for all five
release targets. It queries the official vulnerability database and retains
the scanner identity and each target's command, output, hash and exit code.
The scanner's `GOVERSION` is set to the verified compiler version, and its
reported Go version is checked in the retained output. A caller-provided
version override cannot change which standard library is analyzed.
The blocking invocation uses text output: govulncheck's JSON, SARIF and
OpenVEX modes return zero even when they report vulnerabilities.

Source-mode findings identify vulnerable calls in the selected build; binary
mode reports linked vulnerable symbols and cannot establish an exploitable
execution path. Packaging extracts the actual executable from each final
archive, inspects it with `go version -m`, and requires a successful
`govulncheck -mode=binary` scan before sealing the archive. The gate binds the
scanner identity and binary scan logs to the receipt, and checks each binary's
compiler and linked dependency versions against the checked source, in
addition to its revision, clean-source flag and target. A failed reseal clears
prior artifact approval. A manifest change alone is not evidence that a
packaged executable was rebuilt.

## The gate

```bash
scripts/release_gate.sh preflight           # zero-mutation toolchain, clean-tree & helper check
scripts/release_gate.sh                     # everything, ~12 minutes on the reference machine (8 of them stage 8)
RELEASE_GATE_SKIP="8" scripts/release_gate.sh   # ~4 minutes: skip the benchmark comparison for quick loops (see below)
```

Stages, in order: `gofmt`, `go build` + `go vet`, unit tests with `-race`,
e2e tests with `-race`, docs parity (`go generate` must not change the
tree), GitHub Actions pin check (`scripts/check_action_pins.sh`), vendored
asset hashes and pinned graph-WASM source rebuild (`scripts/verify_vendor.sh --source` against `MANIFEST.json`), benchmark
comparison (`scripts/benchmark.sh compare` against `benchmarks/baseline.txt`,
fails above 20% best-of-N sec/op regression on any tracked benchmark), and the
robot smoke (`scripts/robot_smoke.sh`: all 47 registered robot commands on this repository
and on a synthetic fixture), and the gate's own script self-tests
(`tests/scripts/benchmark_compare_test.sh`, `release_gate_test.sh`,
`install_ps1_test.sh`, and `tests/scripts/graph_wasm_test.sh`), followed by required source-mode vulnerability analysis
for every release target (stage 11). The `release_gate_test.sh gate-mechanics`
invocation tests receipt, compiler, dependency and archive controls
with real Git/Go/GoReleaser operations and a synthetic scanner in isolated
fixtures. It is not vulnerability-analysis evidence for bv. Stage 11 always
runs the real scanner against the project; network or scanner failures block
release eligibility. PowerShell must be on PATH or named by `PWSH` for a
complete gate. A missing PowerShell runner makes stage 10 skipped and the
receipt ineligible. The gate also requires Python 3 for receipt and archive
verification and GoReleaser for the packaging regression tests. Race tests
enable CGO; packaged binaries use `CGO_ENABLED=0`.

Put `br` 0.6.0 or newer on the gate's PATH for live tracker verification.
Those tests execute saved claims after an issue becomes deferred; older
trackers can accept those stale claims. The guard shipped in
[br v0.6.0](https://github.com/Dicklesworthstone/beads_rust/releases/tag/v0.6.0).
A per-run tools directory is sufficient; the tests create fresh tracker fixtures.

### Required tooling

The repository does not vendor or install these. A complete gate needs all of
them on PATH. Because the pinned Rust compiler for the graph-WASM rebuild is
`x86_64-unknown-linux-gnu`, a complete gate runs only on an x86_64 Linux host;
other hosts can run every other stage for diagnostics.

| Tool | Version | Used by | If absent |
|------|---------|---------|-----------|
| Go | exactly the `go.mod` version (Go 1.26.11), `GOTOOLCHAIN=local` | every stage | the gate refuses to start |
| Python 3, Git | any current release | receipts, source digest, archive checks | the gate refuses to start |
| `jq` | any | stages 7 and 9 | stage SKIPPED |
| `sha256sum` | GNU coreutils or compatible | stages 7 and 8 | stage 7 SKIPPED, stage 8 FAILED |
| `rustup` with `nightly-2026-08-31-x86_64-unknown-linux-gnu`, its `wasm32-unknown-unknown` target, and a populated Cargo registry cache | pinned in `scripts/build_graph_wasm.sh` | stage 7 (graph-WASM source rebuild), stage 10 (`graph_wasm_test.sh`) | stage SKIPPED |
| `wasm-bindgen` | 0.2.128, the exact executable whose SHA-256 is pinned in `scripts/build_graph_wasm.sh` (`WASM_BINDGEN` selects another path) | stages 7 and 10 | missing: SKIPPED; another version or build: FAILED |
| `wasm-opt` (Binaryen) | version 132, the pinned executable (`WASM_OPT` selects another path) | stages 7 and 10 | missing: SKIPPED; another version or build: FAILED |
| Node.js | with WebAssembly support | stage 10 (`graph_wasm_test.sh`) | stage SKIPPED |
| GoReleaser | v2.18.1, the version the retained release workflow pins | stage 10 packaging test, `release_gate.sh package` | stage SKIPPED; `package` fails closed |
| PowerShell (`pwsh`, or `PWSH=/path`) | 7.x | stage 10 (`install_ps1_test.sh`) | stage SKIPPED |
| `govulncheck` | v1.9.0 | stage 11 | the gate installs the pinned version itself (needs network) |
| `br` | 0.6.0 or newer | live-tracker e2e tests | those tests skip; the stage still runs |

`scripts/release_gate.sh preflight` checks all of these without building
anything. It reads the graph-WASM pins from `scripts/build_graph_wasm.sh`,
compares each tool's reported version and executable hash, and lists every
missing or mismatched prerequisite in `missing_prerequisites`, on stderr, and
in `release_eligible_if_run_now`. `scripts/release_gate.sh run` prints the same
list before stage 1 and continues for diagnostics. A helper that reports
`INCOMPLETE:` and exits 2 because a tool is absent is recorded as SKIPPED, so
the summary separates "this host lacks the tooling" (`INCOMPLETE`, exit 2) from
a failed check (`FAILED`, exit 1). Either way the receipt is ineligible and
packaging refuses it.

For RCH, use a complete sanitized clone and transfer settings that preserve
Git history and tracked Beads data. Set `TMPDIR` in the remote command to an
existing directory outside that clone, for example
`RCH_REQUIRE_REMOTE=1 rch exec -- env TMPDIR=/data/tmp make release-gate`.
RCH's default `.rch-tmp` directory puts test and retained helper artifacts
inside the checkout, which marks binaries dirty and prevents a clean receipt.
Keep the same external temporary directory and toolchain settings for packaging
and verification.

The complete E2E stage has a 30-minute aggregate timeout, including the full
600-query search evaluation. This replaces Go's default ten-minute suite limit;
per-test assertions, evaluation cases and benchmark thresholds remain unchanged.

Each run prints a fresh directory under `/tmp` containing `gate.log` and
`receipt.json`. Set `RELEASE_GATE_OUTPUT_DIR` to another existing directory
outside the checkout. Outputs never enter the source hash. A failed stage
prints its last 25 log lines. `RELEASE_GATE_SKIP="n m"` and
`RELEASE_GATE_ALLOW_MISSING=1` support diagnostic runs: skips are recorded,
the summary says `NOT ELIGIBLE FOR RELEASE`, and packaging rejects the receipt.
An incomplete diagnostic run prints `INCOMPLETE` and exits 2; a failed check
exits 1. Only a complete clean gate exits zero. Dirty trees and altered
benchmark/smoke settings also produce incomplete diagnostic receipts.

The receipt records the Git revision and tree, hashes of all tracked files
and the frozen dataset, dirty/untracked paths, Go toolchain/build environment,
and each stage's command, duration, exit code, and outcome. Ignored files under
`cmd`, `pkg`, `internal`, or `vendor`, and ignored `go.work` inputs, are rejected
as undeclared build inputs. Commit intended inputs before running the final
gate. Symlink inputs must resolve to files inside the checkout.
The receipt also binds the pinned vulnerability scanner and successful scan
logs. Missing, failed or altered security evidence prevents packaging.

Stage 8 requires Go, Git and standard shell tools: `scripts/benchmark.sh`
runs the ten tracked benchmarks (`BenchmarkRealData_*`, `BenchmarkFullAnalysis_*`,
`BenchmarkSnapshotSwap`, `BenchmarkKeyPressLatency`, `BenchmarkListItemBuild`,
`BenchmarkParseIssuesPoolComparison`) against the frozen dataset
`tests/testdata/benchmark/medium.jsonl`, four rounds per package alternating
between the baseline commit's tree and HEAD with the pair order swapped each
round (always running one tree first biased identical code by 10-20%), and
compares the best observed `ns/op` of each side. Taking the minimum reduces
the effect of slow outliers; shared-host scheduling, cache state and CPU
frequency can still affect either side. The stored
`benchmarks/baseline.txt` carries a
provenance header (date, Go version, CPU, OS, commit, dataset hash) and is
regenerated only on the reference machine with `scripts/benchmark.sh baseline`.
The frozen `medium.jsonl` input is tracked with the source and matches that
header's SHA256. Keep those exact bytes in clean checkouts; regenerating the
fixture changes the workload and invalidates the recorded baseline.
`BENCH_PCT` (gate: `RELEASE_GATE_BENCH_PCT`) sets the threshold;
`tests/scripts/benchmark_compare_test.sh` proves the comparison turns red on a
benchmark doubled in every sample, stays green on one contended sample, and
turns red on a missing benchmark. Because a stored baseline cannot
tell host drift from a code regression on a shared machine (on 2026-09-03 the
same code read +38% against the stored file and 0% against a fresh build of
the baseline commit), `compare` first builds and runs the tracked set for the
commit named in the baseline header, in a detached worktree, and judges HEAD
against that contemporaneous run; the stored file is the fallback when that
commit is not in the clone (`BENCH_REFERENCE=stored` forces it). A reported
threshold breach does not by itself establish the cause. Missing benchmark
results and build errors can also fail the stage. Inspect the retained samples and
reference-build identity before attributing a regression to code or host
conditions. A failed comparison is never a licence to raise the threshold.

## Where the gate runs

Run the gate through **DSR**, locally or on the maintainer's configured build
hosts, before every release. GitHub Actions must remain disabled under the
repository's permanent automation policy. Existing workflow YAML is retained
as input for local DSR/`act` execution; it does not authorize Actions execution.
DSR's `quality` command can execute the gate locally through a tool registration
whose `required_checks` contains `bash scripts/release_gate.sh run`.

The retained `.github/workflows/ci.yml` invokes the gate with diagnostic skips.
Those settings cannot authorize release. Final packaging always requires the
complete gate for the exact clean source commit, with no skipped stages.

The retained `release.yml` verifies the selected compiler immediately after
setup, requires the complete receipt, installs GoReleaser **v2.18.1** without
running it, and calls `scripts/release_gate.sh package` to scan and seal the
archives. It has read-only permissions and no publication-token bindings.
For local DSR/`act` execution, supply `RELEASE_GATE_RECEIPT` and
`RELEASE_GATE_DIST`, with the clean checkout and retained evidence available
at their recorded paths. The output path must be fresh and outside the
checkout. Keep the tracked GoReleaser flags, including `-mod=vendor` and
`-trimpath`; do not add them through `GOFLAGS`.

## Release steps

The current release route uses the local/SSH gate and GoReleaser packaging,
then DSR publication. Do not enable or dispatch GitHub Actions. For this route,
register `bv` in DSR with all five targets and use the sealed archives as the
artifact inputs. DSR's external-artifact mode does not create a DSR build
manifest; the original gate receipt and downloaded-asset hash checks establish
source identity and completeness for this route.
Publish with `dsr release bv X.Y.Z --artifacts <verified-directory> --draft
--verify-tag --no-dispatch --no-manifest`. The external-artifact path needs
`--no-manifest`; bv's receipt, seal and checksum verification remain mandatory.
This mode uploads the directory's top-level files, so prepare a directory
containing only the approved upload inventory rather than incidental
GoReleaser metadata. Download and compare every draft asset against its
verified local bytes. Preserve DSR state and publish that same verified draft
with `--resume --publish-draft --verify-tag --no-dispatch --no-manifest`.
The existing 14-asset contract comprises five platform archives, their five
individual `.sha256` files, `checksums.txt`, `SHA256SUMS`,
`release-gate-receipt.json`, and an SPDX SBOM of the packaged Linux amd64 binary.
Update Homebrew and Scoop directly from the same checksum manifest when
separately authorized.
The release request can authorize this entire sequence, including both stores;
do not ask again when it already does.
Approval to commit directly to `main` does not itself authorize a tag, draft
upload, or publication. Obtain the separately requested release approval
before those actions.

1. Update the version, `CHANGELOG.md`, and release documentation; commit
   those changes on `main` with explicit approval **before** the final gate.
   Confirm the tree is clean.
2. Run `scripts/release_gate.sh`; retain the printed `receipt.json` and log.
   Set `RELEASE_GATE_RECEIPT` to that absolute receipt path. Only a summary
   saying `RELEASE GATE PASSED` permits the next step.
3. Tag that same commit `vX.Y.Z`, subject to the repository's authorization
   rules. Any source edit or new commit requires a fresh gate.
4. Set `RELEASE_GATE_DIST` to a fresh, nonexistent directory outside the
   checkout, then run `scripts/release_gate.sh package`. This runs GoReleaser
   with publication skipped, verifies every embedded binary's revision,
   clean-source flag, Go version and dependency versions, scans the extracted
   executable from each archive, checks `checksums.txt`, and seals hashes for
   all five supported target archives into the receipt. Existing output
   directories are refused; the command does not delete them.
   GoReleaser does not template its dist field, so the wrapper derives a
   config beside the receipt that changes only the declared dist path. Its
   hash is recorded and checked; the tracked config stays untouched.
5. Run `scripts/release_gate.sh verify` immediately before an authorized
   publication. Changed source, toolchain, vulnerability evidence, receipt stages, checksums, or
   archives fail verification. After the remaining platform/browser/WASM
   checks pass, publish the verified archives, `checksums.txt`, and sealed
   receipt through the maintainer's explicitly authorized GitHub Release
   operation. GoReleaser's release publication is disabled in the checked-in
   config so it cannot upload archives before this verification. The
   Homebrew/Scoop upload settings remain disabled; updating those repositories
   is a separately authorized operation. Follow AGENTS.md for branch pushes.
6. Verify the published artifacts: `install.sh` on a clean machine
   (it verifies `checksums.txt` and fails closed), `bv --version`, and
   `bv --robot-capabilities | jq .version`.

`.goreleaser.yaml` emits five archives and `checksums.txt`. The gate adds the
sealed receipt. Generate any additional SBOM from the actual packaged binary
before uploading it; a scan of the working directory is not an archive SBOM.
The receipt is an audit record, not a cryptographic attestation against a
malicious host. Retire it when its revision, toolchain, or artifacts change,
or when that release is superseded; retention/deletion follows maintainer policy.

## Native installation and package stores

### v0.28.0 verification (2026-11-23)

Published at 17:10 UTC from clean revision `88664e48` after all 11 release-gate
stages (including Stage 11 `govulncheck` and Stage 7 pinned WASM source rebuild)
passed with `eligible: true` on Go 1.26.11 (`CGO_ENABLED=0`). All 14 release
assets (five platform archives, five per-archive `.sha256` files,
`checksums.txt`, `SHA256SUMS`, `release-gate-receipt.json`, and the Linux amd64
SPDX SBOM) were published. Homebrew (`Formula/bv.rb` at `4b2be87`, four
macOS/Linux targets) and Scoop (`bv.json` at `63fc917`, Windows amd64) match
all five archive SHA-256 digests in `checksums.txt`.

Post-publication Linux amd64 verification checked:
- `bv_0.28.0_linux_amd64.tar.gz` SHA-256 (`6b11b1c652e35b900465e30c59fa5dcd08d289e1bc0e5fee370660c893c34b4b`), extracted binary SHA-256 (`097808f3aa785e7da58c134e70e815de9d6a8947759c6bbe75f753e39d210212`), `go version -m` (`go1.26.11`, `CGO_ENABLED=0`, `vcs.revision=88664e48863ab1959463caadfc67b39689083485`, `vcs.modified=false`), `--version` (`bv v0.28.0`), and `--robot-capabilities` (46 registered commands at `v0.28.0`; `--robot-check-dep` was added post-`v0.28.0` on `main`).
- `tests/scripts/install_sh_test.sh` against real `v0.28.0` and `v0.27.0` archives passed all six positive and failed-install preservation controls (`matching-release`, `wrong-version`, `corrupt-checksum`, `corrupt-archive`, `missing-checksum`, `missing-binary`).
- Live `install.sh --version v0.27.0` into an isolated directory containing spaces, followed by `--check-update`, `--update --yes` (`v0.27.0` to `v0.28.0`), idempotent `--update --yes`, `--rollback` (`v0.28.0` to `v0.27.0`), and re-update to `v0.28.0`, produced the exact packaged binary SHA-256.
- Dual-binary vision checks (six-issue readiness/label/search counterexample, partial and colliding-ID workspaces, `--as-of HEAD~1` action withholding, and copied README `sprint-review` recipe exports in Markdown/JSON/CSV/Mermaid) passed on both packaged `v0.28.0` and compiled `main`.

Physical `darwin/amd64` and `linux/arm64` hardware execution (as well as native
`v0.28.0` execution on Windows, macOS arm64, and Nix) remain outside this Linux
amd64 verification and stay explicitly tracked under `bv-oonu.9` / `bv-oonu.10`.

### v0.27.0 verification (2026-11-21)

Published at 12:34 UTC from clean revision `2ccf44e0` after all 11 release-gate
stages passed with `eligible: true` on Go 1.26.11 (`CGO_ENABLED=0`), shipping
the 14-asset contract with Homebrew (`9db4921`) and Scoop (`6a63772`) manifest
updates.

### v0.25.2 verification (2026-10-01)

Published at 04:07 UTC from `721353e7` after all ten gate stages passed
without skips on vmi1149989 (Go 1.26.8, GoReleaser 2.18.1, PowerShell 7.6.5,
br 0.6.0, wasm-bindgen 0.2.128, Binaryen 132). Packaging sealed and verified
all five archives; DSR uploaded them with `checksums.txt`, the receipt and an
SPDX SBOM of the packaged Linux amd64 binary as a draft without dispatch, and
`SHA256SUMS` plus the per-archive `.sha256` files completed the same 14-asset
set as v0.25.1. Every draft asset was byte-compared before publication.
Homebrew `16accca` and Scoop `3a34742` publish the same archive hashes.

Checked after publication in Debian containers (Linux amd64 and arm64):
`install.sh` installs v0.25.2 with `python3` and, after removing it, with only
`jq` and no Go; a tiny project's `--robot-next` reports `t-1` as the
diagnostic pick; the v0.25.1 binary's `--update --yes` moves to v0.25.2 and a
second run reports it is up to date. The macOS arm64 archive runs and reports
v0.25.2. Native Windows, native macOS amd64 and Nix were not run.

Gate-host notes: on hosts with git 2.54.0, `TestHistoryExportUsesRecordedLifecycle`
failed in most runs (also at v0.25.1) because automatic maintenance ran in
the middle of its 500-commit fixture; `721353e7` turns it off for that fixture.
Hosts under I/O pressure (hz4, ~50% `some`) failed the timing-bound watch and
correlation e2e tests, and shared VPS hosts can fail stage 8 on benchmarks of
unchanged code (one run reported +34% on `BenchmarkRealData_FullAnalysis`, the
previous run on the same host -23%); a rerun on the same commit passed.

### v0.25.1 verification (2026-09-29)

Published at 17:22 UTC from `4db2f8dc` after all ten gate stages passed
without skips on vmi1152480 (Go 1.26.8, GoReleaser 2.18.1, PowerShell 7.6.5,
br 0.6.0, the pinned wasm-bindgen 0.2.128 and Binaryen 132). Packaging sealed
and verified all five archives. DSR uploaded the archives, `checksums.txt`,
the sealed receipt and an SPDX SBOM of the packaged Linux amd64 binary as a
draft without dispatch; `SHA256SUMS` and the per-archive `.sha256` files were
added from the sealed `checksums.txt`, giving the same 14-asset set as
v0.25.0. Every draft asset was downloaded and byte-compared before publication.
Homebrew `cbca153` and Scoop `c96fc70` publish the same archive hashes.

Checked after publication: `install.sh` into a fresh directory on Linux amd64
and arm64 (Debian containers) installs v0.25.1 and a tiny project gets the
expected pick; the v0.25.0 binary's `--update --yes` moves to v0.25.1 and a
second run reports it is up to date; the macOS arm64 archive runs and reports
v0.25.1. Native Windows execution, native macOS amd64 and Linux arm64 hardware,
and Nix were not run for this release.

Gate-host notes from this release: the default `br` 0.7.1 fails
`TestRobotActionRoutesLiveTrackers/db` (a read-only `br --db … show` on a fresh
tracker exits 2 with "database is busy (recovery in progress)"), so the gate
used br 0.6.0 from a per-run tools directory. On trj the graph-WASM rebuild
produced a `bv_graph_bg.wasm` 8 bytes shorter than the manifest with the same
pinned tools; vmi1152480 and hz4 reproduce it exactly. On heavily loaded hosts
the timing-bound watch/correlation e2e tests and the benchmark stage can fail
from contention alone. `install.sh` needs `python3` for binary installs; with
only `jq` it falls back to a source build. (Fixed after v0.25.1: the installer
now parses release metadata with `jq` when `python3` is missing.)

### v0.25.0 verification (2026-09-12)

Published at 19:39:38 UTC from `87cee258` after all ten clean-source gate
stages passed through RCH. Packaging sealed and verified all five archives.
DSR uploaded 14 draft assets without dispatch; every asset was downloaded and
compared before publication, and public asset digests matched afterward.
Homebrew `4b2e5aa` and Scoop `c06a555` publish the verified archive hashes.

Native Linux amd64, macOS arm64 and Windows amd64 passed binary installation,
capabilities, tiny-project/readiness output, update from 0.24.1, no-update and
failed-install preservation checks. The actual packaged Linux binary passed
the desktop/mobile/offline/update browser harness. Go proxy metadata identifies
the tagged revision; Nix evaluation reports 0.25.0 on all four platforms.
These checks do not establish a native source build, Homebrew/Scoop installation,
Nix build, native macOS amd64/Linux arm64 execution, or P1 performance completion.
`UPGRADE_LOG.md` records the commands, retained artifacts and earlier failures.

On 2026-09-14, the same release source also built through strict RCH with
Nix 2.35.2 and its pinned Go 1.26.7 on native Linux amd64. The active builder's
namespaces confirmed sandbox isolation. The derivation's `cmd/bv` tests passed, and independent
native checks confirmed version `v0.25.0`, capabilities JSON and the expected
recommendation from a tiny real issue file. The executable's SHA-256 is
`c1d288d4f1d19d6a1a05993f7b5952e714af0f48211afc1760f8d240780e2036`.
RCH's post-build source verification then exceeded its 180-second SSH timeout,
so this run has no completed RCH source receipt. A separate independent readback
matched all 5,084 tracked source files to `87cee258`, including executable modes.
This proves the observed Linux amd64 Nix build and execution, not Nix profile installation or another
platform. Bead `bv-oonu.9` retains the build log, smoke evidence and receipt
failure separately.

On 2026-09-15, that existing store package was installed offline into a fresh,
isolated Nix profile on Linux amd64. The active profile entry and executable
resolved to the same store path and SHA-256 above. Invoking the profile's `bv`
returned v0.25.0, 41 robot capabilities, and the expected diagnostic pick from
a tiny JSONL project. That project intentionally had no tracker metadata, so
no live claim command was expected or emitted. The check changed neither the
user's default profile nor PATH and performed no compilation. It establishes
existing-store profile installation, not the public GitHub-flake download path,
profile upgrades, another native platform, or a completed RCH source receipt.
Bead `bv-oonu.9` retains the profile manifest and native command logs.

Run `tests/scripts/install_native_test.ps1` on native Windows x64 as part of
release verification. It installs two real published releases into fresh temporary
directories containing spaces, checks version/capabilities and a tiny Beads
project, exercises the released self-updater and no-update path, then serves
deliberately broken archives over loopback. A wrong-version executable, corrupt
checksum/archive, or missing checksum manifest must fail without replacing
the working installation. The harness uses `-NoPathUpdate`, refuses an existing
test directory, and retains all evidence. It requires Windows PowerShell 5.1+
and network access; `-Version`, `-PreviousVersion`, and `-InstallerPath` select
the releases and installer under review. Linux PowerShell cannot substitute
for this native execution. `-IncludeSource` additionally runs the pinned source
installation with the host's real Go toolchain. The always-run controlled
compiler fixture checks that wrong-version source output cannot replace a
working installation; it is not proof of a real source build.

On Linux or macOS, run the real `install.sh` with `INSTALL_DIR` set to a fresh
temporary destination, then check `--version`, `--robot-capabilities`, a tiny
project's `--robot-next`, and an isolated older binary's `--update --yes` twice.
The second update must leave the binary unchanged. For controlled archive
failures, run:

```bash
bash tests/scripts/install_sh_test.sh \
  /path/to/current-platform.tar.gz CURRENT_ARCHIVE_SHA256 \
  /path/to/previous-platform.tar.gz PREVIOUS_ARCHIVE_SHA256
```

Set `CURRENT_TAG` and `PREVIOUS_TAG` when testing versions other than v0.24.0
and v0.23.0. Obtain the archive hashes independently from the release's verified
checksum manifest. This harness executes the native binaries and the shell
installer's entry point; local file transport controls its negative fixtures.
It rejects wrong-version, corrupt, and missing-binary archives, and verifies
that verification failures cannot fall through to a source build. It retains
its fixture tree and logs. A fixture pass supplements the live installation
check above.

On 2026-09-04, live v0.22.0 to v0.23.0 installation/update and these failure
controls passed on Linux amd64 and Windows x64 (PowerShell 5.1). The v0.23.0
binaries report the tagged revision `0b770db4741f7993b16a6531f87183a9f392d6c4`
and `vcs.modified=true`; successful native execution does not establish a
clean or reproducible release build. Native macOS amd64/arm64, native Linux
arm64, and a supported Nix build remain unverified by that run.

Check the public [Homebrew formula](https://github.com/Dicklesworthstone/homebrew-tap/blob/main/Formula/bv.rb)
and [Scoop manifest](https://github.com/Dicklesworthstone/scoop-bucket/blob/main/bv.json)
separately. On 2026-09-05 both still selected v0.22.0, while the latest GitHub
release was v0.23.0; all five store archive hashes matched the v0.22.0 checksum
manifest. `skip_upload: true` means publishing a GitHub Release does not update
either store. After explicit authorization, update the formula's four platform
URLs/hashes and Scoop's Windows URL/hash and version from the same verified
release. Use its actual asset names: v0.23.0 adds `bv_0.23.0_` to the archive
names, so replacing only the tag in a v0.22.0 URL is insufficient.
Publish those changes to the respective repositories' `main` branches,
then fetch the public manifests again and verify their version and hashes.
Until that separate step completes, do not describe the package-store install
as installing the latest GitHub release.

On 2026-09-08, [v0.24.1](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.24.1)
was published through DSR after all ten original gate stages passed without
skips on commit `3e4e61c91a74dafe211d3f6a62f3c2919969657c`. All five archives
identify Go 1.25.5, that exact revision, and `vcs.modified=false`. The 14
uploaded assets were downloaded and matched by SHA-256 and size before the
draft was published. They include the sealed receipt and an SPDX SBOM of the
actual Linux amd64 binary; no minisign signature was produced because the
signing key was unavailable. GitHub Actions and repository dispatch were not
used.

Homebrew commit `cae0685` and Scoop commit `4fddb86` publish v0.24.1 with all
five archive hashes verified against the sealed manifest. Public readback
matches both commits; the existing legacy branch mirrors are synchronized.
The Go proxy identifies the same tagged revision, and the public Nix flake
matches the tagged source. Ruby syntax and the existing formula validator
pass (the validator retains one warning about the existing platform DSL).
These checks do not establish native Homebrew or Nix installation. Native
macOS amd64/arm64, Linux arm64, and a supported Nix build remain unverified.

The Windows installer follow-up in `3ca2176f` suppresses download progress
inside `Install-FromRelease` and repairs the native harness's obsolete
top-level `--robot-next` ID assertion. It follows the immutable release tag;
the archives still identify `3e4e61c9`. The original native test failure and
the later incomplete loopback transfer are retained. An initial native source
installation using Go 1.26.5 passed. A subsequent optional source run compiled
successfully but exceeded the existing 10-second first-start guard; a second
diagnostic invocation of that retained executable returned the correct version
with empty stderr in 1,742 ms. The original failure remains unresolved, and
the timeout is unchanged. Broader native-platform acceptance stays open.

The complete default native Windows suite passes with installer `3ca2176f`
against the public v0.24.0/v0.24.1 archives. Readback verifies all 28 command
logs, eight capability results, the tagged binary identity and each of the
five intended rejection reasons. Both the working executable and user PATH
are preserved. Linux live installation, readiness, update/no-update and all
six archive/preservation scenarios also pass on a separate native host.
The Windows optional-source rerun above is a separate failed result, not part
of that default-suite pass. Raw release and verification evidence is retained
under `/data/tmp/bv-release-v0.24.1-20260908`.

### v0.26.0 verification and receipt qualification (2026-11-02)

On 2026-11-02, [v0.26.0](https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.26.0)
was published with all five platform archives (`bv_0.26.0_<os>_<arch>.tar.gz`
and `.zip`) and `checksums.txt`. As disclosed in `CHANGELOG.md`, the v0.26.0
release notes record one failed E2E stage and an ineligible diagnostic receipt;
that historical exception does not authorize any future release (including
`v0.26.1`) to bypass a complete 11-stage clean-source gate. On hosts without
`goreleaser` or `pwsh`, `scripts/release_gate.sh preflight` reports the missing
helpers up front, `tests/scripts/release_gate_test.sh gate-mechanics` still
executes all five pure-Go cross-builds and tamper negative controls before
returning skip sentinel `200` (marking stage 10 `SKIPPED` and the receipt
ineligible), and `scripts/release_gate.sh package` fails closed. Native macOS
(`darwin/amd64`, `darwin/arm64`) and native `linux/arm64` smoke verification
remain tracked separately under `bv-oonu.9` and `bv-oonu.10`.

## Vendored dependency patches

Four dependencies carry local patches (listed in `docs/PROVENANCE.md`):
`github.com/alecthomas/chroma/v2`, `github.com/charmbracelet/glamour`,
`github.com/muesli/reflow` and `github.com/goccy/go-json`. Each lives under
`third_party/<name>/` as a complete Go module (the upstream `go.mod` plus the
imported package set, patches applied), and `go.mod` replaces the upstream
module path with that directory. `go mod vendor` copies from `third_party/`,
so running it is safe and never reverts a patch; `go build`, `go test` and
the Nix build keep reading `vendor/` as before.

- Change a patch: edit the file under `third_party/`, run `go mod vendor`,
  and commit both. `tests/e2e/third_party_vendor_test.go` fails when
  `vendor/` and `third_party/` disagree, so editing `vendor/` directly is
  caught by the gate.
- Upgrade a patched module: bump the version in `go.mod`, run
  `go mod download -json <module>@<version>` to locate the pristine source in
  the module cache, copy its `go.mod` and the packages listed for that module
  in `vendor/modules.txt` into `third_party/<name>/` (no `_test.go` files or
  `testdata/`), reapply the patch, then `go mod tidy && go mod vendor`.
  `go.sum` carries no entry for a replaced module.
- Drop a patch once upstream ships it: remove the `replace` line and the
  `third_party/<name>/` directory, then `go mod tidy && go mod vendor`.

## What is not covered

- The gate does not run the native installation checks above. Its
  `tests/scripts/install_ps1_test.sh` fixture uses a marker executable under
  Linux PowerShell and establishes archive/checksum handling only; it cannot
  establish native executable identity, Windows paths, or update behavior.
- The gate does not run the opt-in browser journey. Run
  `BV_HEADLESS_BROWSER=/path/to/chrome scripts/dashboard_browser_smoke.sh`
  with Node (native WebSocket support) and Python available. It exports real
  issue data and checks desktop/360-pixel search, filters, keyboard detail,
  comments, copy-link, Mermaid, charts, graph selection, offline reload and
  service-worker update. Offline phases stop the actual loopback server and
  block worker network requests; they cannot repair missing cache entries
  from a still-running server. Missing browser exits INCOMPLETE (2). Console,
  network, screenshot and DOM evidence stays in the printed temporary path.
  Missing/changed required assets, unprimed offline access, unsafe display
  content and optional hybrid-WASM fallback have distinct negative controls.
  Repeat after changing viewer assets or CSP. Alpine still requires
  `unsafe-eval`; passing these journeys does not remove that residual policy.
- Stage 7 rebuilds the graph WASM and compares both module and glue with the
  vendored bytes using the pinned source toolchain in `docs/PROVENANCE.md`.
  That module proof does not substitute for the browser journey above.
