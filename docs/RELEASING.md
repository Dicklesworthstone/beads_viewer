# Releasing bv

Release packaging requires a complete clean gate receipt for the exact source
commit. Publication requires verification of the sealed archive hashes and
separate authorization. The receipt does not replace the native-platform,
browser, or WASM proofs listed below.

## The gate

```bash
scripts/release_gate.sh                     # everything, ~12 minutes on the reference machine (8 of them stage 8)
RELEASE_GATE_SKIP="8" scripts/release_gate.sh   # ~4 minutes: skip the benchmark comparison for quick loops (see below)
```

Stages, in order: `gofmt`, `go build` + `go vet`, unit tests with `-race`,
e2e tests with `-race`, docs parity (`go generate` must not change the
tree), GitHub Actions pin check (`scripts/check_action_pins.sh`), vendored
asset hashes (`scripts/verify_vendor.sh` against `MANIFEST.json`), benchmark
comparison (`scripts/benchmark.sh compare` against `benchmarks/baseline.txt`,
fails above 20% best-of-N sec/op regression on any tracked benchmark), and the
robot smoke (`scripts/robot_smoke.sh`: every robot command on this repository
and on a synthetic fixture), and the gate's own script self-tests
(`tests/scripts/benchmark_compare_test.sh`, `release_gate_test.sh`, and
`install_ps1_test.sh`). PowerShell must be on PATH or named by `PWSH` for a
complete gate. A missing PowerShell runner makes stage 10 skipped and the
receipt ineligible. The gate also requires Python 3 for receipt and archive
verification and GoReleaser for the packaging regression tests. Race tests
enable CGO; packaged binaries use `CGO_ENABLED=0`.

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

- **Locally, before every release.** This is mandatory and is the step that
  actually protects releases today, because the GitHub Actions workflows are
  disabled (`gh workflow list --all` shows `disabled_manually` for CI,
  Release, Fuzz, and Flake Update since 2026-08-16).
- **In CI, once re-enabled.** `.github/workflows/ci.yml` already runs the
  same script with `RELEASE_GATE_SKIP="8" RELEASE_GATE_ALLOW_MISSING=1`.
  That configuration is diagnostic and cannot authorize release. Its legacy
  artifact upload path must be changed to the external output directory if
  the workflow is re-enabled, and the job must explicitly handle diagnostic
  exit 2 or run the complete gate. Whether to re-enable is the
  maintainer's call: it costs Actions minutes and Codecov uploads, and it
  is the only way the README badge becomes live again.

## Release steps

1. Update the version, `CHANGELOG.md`, and release documentation; commit
   those changes on `main` **before** the final gate. Confirm the tree is clean.
2. Run `scripts/release_gate.sh`; retain the printed `receipt.json` and log.
   Set `RELEASE_GATE_RECEIPT` to that absolute receipt path. Only a summary
   saying `RELEASE GATE PASSED` permits the next step.
3. Tag that same commit `vX.Y.Z`, subject to the repository's authorization
   rules. Any source edit or new commit requires a fresh gate.
4. Set `RELEASE_GATE_DIST` to a fresh, nonexistent directory outside the
   checkout, then run `scripts/release_gate.sh package`. This runs GoReleaser
   with publication skipped, verifies every embedded binary's revision,
   clean-source flag and Go version, checks `checksums.txt`, and seals hashes
   for all five supported target archives into the receipt. Existing output
   directories are refused; the command does not delete them.
   GoReleaser does not template its dist field, so the wrapper derives a
   config beside the receipt that changes only the declared dist path. Its
   hash is recorded and checked; the tracked config stays untouched.
5. Run `scripts/release_gate.sh verify` immediately before an authorized
   publication. Changed source, toolchain, receipt stages, checksums, or
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
sealed receipt; no SBOM or additional release manifest is currently emitted.
The receipt is an audit record, not a cryptographic attestation against a
malicious host. Retire it when its revision, toolchain, or artifacts change,
or when that release is superseded; retention/deletion follows maintainer policy.

## Native installation and package stores

Run `tests/scripts/install_native_test.ps1` on native Windows x64 before a
release. It installs two real published releases into fresh temporary
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

Set `CURRENT_TAG` and `PREVIOUS_TAG` when testing versions other than v0.23.0
and v0.22.0. Obtain the archive hashes independently from the release's verified
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
