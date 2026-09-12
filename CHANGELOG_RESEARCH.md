# Changelog research: v0.24.1 and its follow-ups

Scope: the complete `v0.24.0..v0.24.1` and `v0.24.1..80450e34` commit
windows, extended by the dated follow-up sections through `b0ce5669` and
the current dependency preparation. Earlier changelog entries are preserved,
not re-audited by this update.
This is a bounded application of `changelog-md-workmanship`, requested after
publication of v0.24.1. Dates use UTC publication dates for GitHub Releases.

Evidence order: Git diffs and tag identities, live GitHub Release metadata,
checked-in Beads history, retained release receipts, then existing release
documentation. Public source links belong in the changelog; local execution
evidence supplements them here.

## September 12 release preparation

Reviewed the follow-up history through `b0ce5669` against the v0.25.0 candidate entry.
This includes the final Rust lock transitions, rebuilt embedded graph pair,
and the two test-only timing repairs. The candidate has no publication date
until the final gate and release upload succeed.
The subsequent gate found a scheduler-dependent Cass timeout test. Both timeout
unit tests now retain their durations and assertions under a deterministic test
clock, with deadline checks and a default-timeout success control. Repeated race
tests and the full Cass package pass; this changes test reliability, not runtime
timeout behavior. RCH release instructions also require an external temporary
directory to keep generated test artifacts out of source provenance.
The Windows installer paragraph describes its first pin historically;
the current README uses `a43b8e85`, which contains the Go 1.26 minimum.
Commit `3bc5c15c` captures command cancellation immediately after the child
returns, before writing diagnostic artifacts. It also reports sample identity
and elapsed time. This improves failed-matrix evidence; it neither attributes
the original stall nor completes P1 acceptance.

The Go and Rust dependency transitions passed their individually recorded
affected-package tests. Vendor regeneration preserves all four local patches;
all 546 replacement files match their `third_party` sources. Final release
qualification remains pending. A newly added SQLite preservation regression
fails against the old exporter and passes three times with private construction
and rename publication. The selected existing watch and claim E2E cases also
pass three times through strict RCH; the final SQLite 1.58.0 transition also
passes export/loader/UI race tests and the watched-export E2E cases. These are
source-check results, not a
published release or proof of native Windows rename behavior. Detailed command
logs and outstanding release tasks are in `UPGRADE_LOG.md`.

The later `a35fc2a3` full gate passed documentation parity and the actual
offline WASM rebuild but hit the unchanged 15-second watched-export deadline.
The log proves the second notification entered database export; it does not
identify the stall's cause. A retained earlier executable with identical
SQLite export source made 80 filesystem sync calls for one issue. The candidate
batches FTS, materialized-view and metadata writes into transactions without
changing durability pragmas or watch deadlines. All three new rollback
regressions fail on the earlier implementation and pass ten times each with
the race detector after batching. The full export race suite, all watched-source
cases repeated three times, build and vet also pass. The same one-issue trace
on the remote-built candidate records 40 sync calls, half the original count;
the single samples are not a latency distribution or an attribution of the
earlier worker stall. The complete release gate still remains required.

## September 11 cascade frontier reuse

Follow-up `01383eb6` caches the sorted, deduplicated union of blocking dependents
and hierarchy children once per Analyzer. It changes only adjacency lookup:
each simulation still owns its completed set, uses the same FIFO order and
rechecks readiness with the current clock and candidate scope. Floating-point
scoring, tie-breaking and random seeds are unchanged. Independent source review
confirmed graph ownership and `sync.Once` publication; it did not run tests.

The preceding 10k profile attributed 100.57 seconds of cumulative sampled CPU
(53.54%) to `countTransitiveUnblocks`, including repeated frontier construction.
The selected opportunity scored 3 × 5 / 2 = 7.5 for impact, confidence and effort.
The cache is lazy, but its first non-leaf query scans the full graph and sorts
each frontier, retaining O(V+E) storage for the Analyzer lifetime. Missing, closed and leaf roots still
return before that initialization. Warm batch measurements exclude this cold cost.

The first baseline launch was refused before remote execution because the test
overlay changed during admission; its exit 103 log is retained. A frozen retry
against `957c9abe` with only the new tests overlaid passed. Three 540-node chain
batch samples took 535.5–990.5 ms, allocated 24.63 MB and 296,836–296,842 objects.
Every starting node's cascade count was checked against the chain length.
The optimized samples on the same worker with Go 1.25.5 took 124.7–290.1 ms,
allocated 15.32 MB and 151,305–151,306 objects. The unchanged complete enhanced
output SHA regression passed three executions. These are three one-operation
samples of warmed synthetic batches, not latency quantiles or a 10k acceptance.
Successful receipts bind old base `957c9abe` plus test-only fingerprint
`870791ecae9a535b75739f307b40440feaad71885f2c114003e7073d0a3daa8c`
and optimized fingerprint
`a490965134089d0dc04569d3393e81ec6fc41db85fe0dec255279f1e8d0959e6`.
Logs for this follow-up are retained under `/data/tmp/bv-cascade-frontier-*20260911.log`.

Full analysis/model tests passed. The final asymmetric-diamond regression forces
a failed readiness check followed by a successful revisit, while also checking
duplicate blocking/parent edges, hierarchy propagation, deferred/parked/missing
exclusions and concurrent first use. Targeted race tests, including the frozen
output regression, passed with Go 1.25.5. Full build and vet also passed. Final-test
receipts bind base `49b97739` and overlay fingerprint
`c2ef1b4b4f22e59795639da5af299c7350a3f8eb2938d9e444638a1adfeb52fc`.
RCH reported downloaded-toolchain cleanup residue after successful package and
build commands; local launchers exited zero, and no manual cleanup was attempted.

UBS returned exit 1: its 21 critical reports concern existing graph-ID, index
and score comparisons misclassified as secret comparisons. Reviewed warnings
include existing deferred recover/cancel patterns and a new goroutine-loop
warning whose loop variable is not captured. No findings were suppressed.
Formatting reports zero non-vendor entries and 49 pre-existing vendor entries.
This checkpoint does not complete the original P1 matrix or native qualification.

## September 11 enhanced-priority snapshot reuse

Narrow follow-up: `c3091424` wires the existing statistics-aware scoring,
recommendation, and what-if methods into the enhanced-priority batch;
`10666a6a` pins the complete serialized output. The reviewed runtime diff is
three substitutions plus one completed analysis snapshot. It retains all scored
candidates, both what-if representations, ordering, and the final ten-item cap.

The 540-issue wide-graph benchmark ran through strict RCH on Linux amd64 `hz3`:
three one-operation samples before were 1.213–1.314 seconds and 648–651 MB
allocated; afterward they were 8.679–10.316 milliseconds and 3.581–3.601 MB.
These are focused synthetic-batch observations under shared-worker I/O pressure.
The before CPU profile attributes 60.60% of sampled CPU to repeated analysis.
The after profile has only 430 ms of samples and also includes regression tests;
it is unsuitable for a precise whole-profile percentage comparison.

The explicit all-metrics, run-to-completion regression fixes scoring time and
checks output SHA-256 `0910d7fe9597707b58c50d594a33bdca75698ca5da0b29ce59a92243c0bcab46`.
The observed old-base invocation failed at 3,379,150 allocations against the
unchanged 250,000 ceiling; the optimized regression passed three executions.
The failed old-base log lacks a successful clean-overlay receipt; its source
binding is the recorded launcher (`52853c6a` plus the test-only overlay), not
an independent worker-source attestation. The successful optimized receipt
binds base `c3091424` and overlay fingerprint
`cca270357f7823fe8b4062396ec939798b51fa34130ce0fd5b39c9a0b87aff9d`.
Logs and profiles are retained under `/data/tmp/bv-enhanced-priority-*20260911*`.

The focused measurements and initial package/CLI checks used unpinned worker
defaults; their compiler versions were not captured directly. A later binary
readback identified Go 1.26.0, prompting explicit Go 1.25.5 qualification with
`GOTOOLCHAIN` forwarded through RCH's per-run environment allowlist. The pinned
full build, full vet, and analysis/model package tests passed with the same
source receipt above, including the frozen-output regression. The pinned CLI
binary is Linux amd64 with CGO enabled and SHA-256
`3c4655ae262794bb64c786cb961ee68472dd0bc50c5e3e74b55814cc68ded161`.
Initial priority CLI contract, schema, metadata and CPU-profile tests also
passed. RCH reported read-only downloaded-toolchain residue during vet cleanup;
the command and local launcher both exited zero. No manual cleanup was attempted.

An earlier 10,000-issue priority diagnostic hit its separate 300-second RCH
limit and returned 137 with empty profile/output artifacts, retained under
`.rch-go/priority-profile-20260911-2003`. It earns no completed-run evidence.
The optimized Go 1.25.5 binary subsequently completed one cold-cache run on
the same 10,000-issue deep-chain fixture in 102.32 seconds, within the unchanged
300-second bound (remote and local exit zero). Its retained JSON contains ten
recommendations, sampled betweenness and explicitly skipped cycles; its CPU
profile parses successfully. Artifacts are in
`.rch-go/priority-smoke-go1255-20260911`. This is a single completion check,
not a paired benchmark or a responsiveness pass.
The original P1 matrix below remains incomplete; this later runtime change
requires its own full qualification. No release or native-platform claim is added.

## September 11 incomplete performance matrix

This bounded follow-up records measurements of current source `4ffc37c6`,
with no runtime changes during the run. The September 10–11 UI matrix passed
its original current-code gates. The overall matrix failed in the last timed
CLI cohort; `bv-apal.1` and its proof task `bv-apal.2` remain open. Earlier
release failures and September 5 measurements retain their original scope.

The baseline is reconstructed from `7393a06b` plus the original working-tree
changes, rather than substituted with a newer convenient commit. Recovered
patch SHA-256 is
`b24b00b39e170e575b4b8d77b2326bd6370b1d088fb3581a6d01e18c71b3f18f`;
the untracked readiness source is
`8c4761428888b247c11dcfa041ea607c6fbbc956ae3847a6f894982fdf275905`.
The reconstructed input manifest is
`a3f432bf218d46c74d35112f60a02cce05e53750ef10f08645a55d641bc8b7ff`.
Independent review checked all 3,212 Go/module/vendor inputs and six embedded
asset roots against recovered evidence. Four nonbuild files are exceptions:
the historical README and performance guide use available later bytes, and
the original tracker and bridge-plan hashes could not be recovered. The
rebuilt baseline is not claimed byte-identical to the missing old executables.

Strict RCH builds ran on Linux amd64 `hz3`, with Go 1.25.5, `GOMAXPROCS=64`,
CGO enabled and `GOAMD64=v1`. Current build receipts agree on all 4,800
transmitted source-file entries; the frozen checkout has 4,810 tracked files,
including ten tracker files excluded by RCH. Both CLI builds use the explicit
benchmark label `v0.0.0-p1.20260910`; version fields remain in exact comparison.
This controls benchmark metadata without claiming released-binary equivalence.

| Measured executable | SHA-256 |
|---|---|
| Rebuilt baseline CLI | `620ef252d7c5b47b7b62afe72d9d0bb6746283c16ab25b318677f4216199d00f` |
| Rebuilt baseline UI test | `c5a3b7e7864269e7df6bb871c9ba924af4cf2f5c196cc029c3acda80a45c5e54` |
| Current CLI | `69dbf11f4335f7bb23ecf1d24e773346fde58828bbb69fa107e1e9d00e285167` |
| Current UI test | `4d6b478afa6928519b95a8c9d98f89009a27e6c4800a02fbd896f5b4d5b7f605` |

The steps of `scripts/benchmark.sh latency` were dispatched separately through
strict RCH to keep compilation remote. No GitHub Actions ran. Its original
workloads, order, sample counts, timeout limits, parity projections and SLOs
were preserved. Runner SHA-256 is
`896d7e2518217e484b41d53176e6fb4d354a0317840d812f26c0a9061c168f7b`;
unchanged verifier SHA-256 is
`939b66ccb210c92927044c05760fc2d206c4eeb170ce48debb6de17bda5b2199`.
Original positive and deliberate-failure controls remain bound to the qualified
sources and binaries; a skipped opt-in test receives no matrix credit.

Independent UI readback checked 304 original files totaling 682,433,712 bytes,
with no missing or changed files. Their canonical path/size/hash map is
`b19be8dcf361f41a6c5ec61c712224cc3bdd645a406901f50ef7bad7b8f5c3cc`.
All eight processes exited zero: 288 records and 288,000 samples. The worst
current p99 is 46.472034 ms; every delivered snapshot and Phase 2 handler
passes 50 ms. The slowest individual interaction is 94.027194 ms. Baseline
misses, all raw outliers, sampled/skipped metrics and unpaired generations
are retained; [the guide](docs/performance.md#september-11-2026-measurement-attempt)
reports their counts and interpretation.

Measured UI allocation totals are 1,561,563,406,448 baseline bytes and
1,541,086,120,632 current bytes. GC counts are 8,974 and 8,893; cumulative GC
pause times are 2,441,823,234 and 2,532,533,278 ns. Those include background
work and measurement overhead, with different completion counts, and do not
establish a causal memory reduction. No new peak-RSS observation was collected.

Timed CLI RCH job `30015430739361967` recorded remote exit 1 at
`2026-09-11T05:22:05.951649Z`, after 7,096,881 ms including Go setup. Its test
ran for 6,864.73 seconds. The first 70 records contain 14,000 complete measured
calls. Unicode 10k warm-cache outputs 0000–0135 exist for both binaries;
baseline `sample-0136.stdout.json` and stderr are empty. No final result record
exists for either side of that pair. The previous baseline output timestamp
is `05:20:00.188265Z`, and the failure file is `05:22:01.851341Z`.
The 121.663-second interval is consistent with the unchanged two-minute
`CommandContext` limit, but the harness did not retain `ctx.Err()` or the
failed call's elapsed duration. `signal: killed` alone cannot establish OOM,
an external kill, or context expiry. No selective retry or timeout increase
was used, and the partial pair receives no completed-cohort credit.

After the remote test exited, RCH's checksum comparison of the source tree
timed out at its 303,000 ms limit. The local wrapper therefore exited 103,
separately from the test's exit 1, without emitting a final source-content
receipt. Both failures are retained; earlier build receipts are not described
as a successful post-test source check. Strict RCH did not fall back locally.
Independent manual readback later verified all 4,800 timed-job source files,
285,732,185 bytes and executable modes against the qualified input manifest,
with no extra files outside the excluded Git, tracker and Go-cache paths.
That is separate post-run evidence, not a replacement RCH receipt.

Independent readback covers all 70 terminal CLI records: source and fixture
identities, every raw decision/status projection, all 14,000 durations and
their quantiles. All 35 complete current p99 values are lower than baseline;
there are no equal or higher pairs in that completed subset. Baseline's
worst p99 is 26,885.120095 ms (Unicode 10k cold), and its maximum sample is
45,643.976476 ms (Unicode 5k cold, sample 147). Current's worst p99 is
743.587494 ms, with a maximum sample of 11,189.315648 ms (Unicode 10k cold,
sample 154). The missing pair is explicitly incomplete, not an excluded
outlier. Its 272 successful outputs establish no duration distribution.

The separately prescribed exact-output test ran through strict RCH job
`30015430739361983` and recorded remote exit 0 at
`2026-09-11T05:33:40.512614Z`; the Go package reports 96.489 seconds.
All 36 records completed, with 144 compared outputs and 36 warmups at
`SOURCE_DATE_EPOCH=1788220800`. Only the original named elapsed fields are
removed; complete scores, statuses, source authority, timestamps, version and
array order remain compared. This result receives no latency credit and
does not replace either missing timed record.

The exact test's final source receipt is
`a87ba22320bad6ca20137539cea843ae7e15e8b121e478e9724c80a9681f37f2`.
All 4,800 file entries match the qualified current binary-build inputs, and
local frozen files were rehashed against the receipt. Both remote test and
local RCH wrapper exited zero. The retained exact log is 848,952 bytes,
SHA-256 `ed9ae54df27a10518325b270d04672d48f09942ac1589501fd2c8c1790020546`.
Independent readback verified all 144 full raw results and 36 warmups, with
180 empty stderr files. Only `triage.meta.compute_time_ms` was present among
the allowed elapsed fields; metric `ms` fields were absent. Number lexemes,
array order, version and actual source paths remain compared. All exact
outputs retain sampled betweenness with 50 pivots and seven skipped metrics.
The complete exact subtree has 650 files and 374,347,740 bytes; its canonical
path/size/hash map is
`65202561bd1160c33ab9dffd6a3719a7795e8509ce55d85d32473f2e120be8da`.

The original whole-matrix verifier then ran without modified inputs or filters
through strict RCH and exited 1. Its only two `FAIL` reports are the missing
baseline and current Unicode 10k warm-cache `result.json` files. Configured
metric degradation and unpaired refresh generations remain visible in its
complete output; no missing record was replaced or synthesized.

Shared-host build activity, verification reads and evidence transfers remain
part of the run. The observed I/O pressure is recorded without assigning
causation to individual tails. Raw measurements are retained under
`/data/tmp/bv-p1-final-20260910.Gtw1Rb5t/matrix.CRrcJzPH` on `hz3`; source
recovery and orchestration evidence is in
`/data/tmp/bv-cass-async-20260910-ZTUc4a`. This performance record includes
only the named P1 provenance, not the unrelated live Cass archive material.

The complete inventory retains 44,198 files totaling 4,069,001,097 bytes,
including actual baseline/current binaries, unchanged harness files, source
recovery/build/control evidence, all measurements and the failed empty output.
The sibling manifest `matrix.CRrcJzPH.raw-files.jsonl` is 10,455,180 bytes,
SHA-256 `4e8cdac2ca7abf995119707a1be4adbbbc31b4bbda6cd47ada1b120004202b54`.
The lossless sibling archive `matrix.CRrcJzPH.tar.zst` is 169,092,402 bytes,
SHA-256 `4cc4e8d088816214ebf2b18a9b90e5d977ba39cb34915deac1fd569713b678c6`.
Streaming decompression verified every file's hash, size and mode, plus 14,932
directories, against the inventory without extraction. Both files are retained
locally in `/data/tmp/bv-p1-final-20260910.Gtw1Rb5t` with identical worker
copies; the complete worker raw tree also remains. No failed sample or earlier
artifact was deleted, and the archive is outside the tree it contains.
Independent review reconciled the archive with every previously verified
measurement map and the retained harness, binary and execution provenance,
with no missing, extra or duplicate members. Agent Mail 708 records that
retention verdict separately from the failed matrix verdict.

The v0.22.0 wording correction changes “inside the frame budget” to the
50 ms interaction target. Its historical 33.05–35.54 ms values are unchanged;
this is not a new audit or rerun of that release.

Documentation whitespace checks and the skill's changelog structural validator
pass. The validator retains its warning about bare commit hashes in earlier
entries. UBS classifies the five changed Markdown/JSONL files as Bash, then
exits 2 with `MODULE_EXIT_2` and zero files scanned. No partial-scan override
was enabled, and that tooling failure is not reported as a passing scan.

## September 10 Cass responsiveness follow-up

The `bv-xiyd` follow-up starts from `ff451598`. The previous repair restored
real session results, but all four `V` dispatch paths still called health
detection and correlation before `Model.Update` returned. A strict-RCH run
of the new regression against that old source fails with `command=false`,
`modal=true`: the subprocess work completed inside the input handler.

The repair moves health and search work into a Bubble Tea command that owns
a copied issue and cancellable request. Completion is bound to the request,
selected issue, view, data generation and workspace. Pending lookups cancel
on `V`/Esc, selection/view changes, refresh, quit and shutdown. The original
bounded health check remains separately bounded; cancellation suppresses a
subsequent query rather than interrupting an already-running health probe.
Completed modals restore their originating view when dismissed.

Independent review also found the detail pane swallowed `V`, a stale command
could populate a cache subsequently reused after refresh, and unchanged focus
could let a reply interrupt alerts or embedded search. Those paths now dispatch
the lookup, isolate caches by dataset/workspace, and cancel when another input
or overlay opens. A cancelled early lookup still accepts startup health without
letting a late startup probe replace the newer lookup's health result.

Controlled subprocess tests block health or search while resizing, rendering
and navigating the real model. They are distinct from the installed-Cass
archive replay. Exact run output is retained in
`/data/tmp/bv-cass-async-20260910-ZTUc4a`; final verification and closure are
recorded on `bv-xiyd`. This change adds no release or native/installed-tracker
qualification and does not complete the original P1 performance matrix.

The final strict-RCH UI race run records 2,048 passing test events, nine
explicit skips and no failures; full-package build and vet also pass. All three
receipts bind base `ff451598` and overlay fingerprint
`c6cc180567d5e891c9a23384ee3dfd1690eb7cd789c06f4892ed294893f2f7d5`.
The worker's two changed source hashes match the reviewed files. The resulting
UI test binary is SHA256
`aedf4b43dd612453da05a087fed18a3e36ebddd8744c356b52580102e0957875`.
Independent execution passes 64 focused cases with one live-test opt-in skip,
then passes the opt-in installed-Cass test: three direct archive hits become
three modal sessions with the original preview and timestamp assertions.

Initial failures remain visible in the same evidence directory: the detail
dispatch defect, incomplete tree/alert test setup, and RCH receipt/admission
and transfer refusals. Test setup was corrected without weakening assertions.
Changed-file formatting and whitespace checks pass; whole-tree formatting
still lists 49 unchanged vendor files. UBS exits 1 on the final files, reporting
158 critical findings, four warnings and 138 informational findings. Manual
and independent review traced these to ordinary comparisons, a fixed executable
invoked with separate arguments, and explicitly owned asynchronous cancellation
lifetimes; this is not reported as a passing scanner run.

## September 10 Cass lookup and generated claim guidance

This bounded follow-up covers the changes after `65cfc346`, beginning with
`fe88cfb7` and `b543d376`. It does not add a release or repeat the earlier
historical audit. Direct installed Cass searches returned nonempty `hits`,
but the viewer decoded an invented `results` envelope and silently discarded
them. Minimal producer fields also omitted the preview and timestamp needed
by the modal. A stale or rebuilding archive could remain searchable while
the viewer refused all searches based on its advisory health result.

The repaired adapter requests bounded preview fields, maps millisecond
timestamps, keeps workspace separate from the archive file path and preserves
the health warning. Correlation caches now retain computed scores, strategy,
keywords and producer totals. Failed or partial searches keep diagnostics and
remain retryable; a successful empty search still caches normally. Controlled
subprocess fixtures cover these branches separately from an opt-in test that
uses the installed Cass archive through the actual `V` update and modal render.

The old runtime fails the wire and advisory-health regressions. The retained
old executable also fails with three direct live hits: root reproduced the
NeedsIndex refusal, and independent replay reproduced silent empty results
with healthy Cass. The first root attempt instead hit the health timeout;
that distinct failure remains recorded. Old cache controls reproduce changed
scores, suppressed retries and changed empty-result metadata. Initial test
compilation errors and RCH admission refusals are retained as failed attempts,
not counted as product negatives. Evidence lives in
`/data/tmp/bv-cass-20260910-9vMyP3`; final results and independent closure belong
to the original `bv-8phk` record.

The independent documentation suite also caught a preexisting mismatch:
`2c9493d2` changed README claim commands without updating the generated blurb.
The installed tracker's help confirms that `--claim` sets both assignee and
in-progress status. The generator now matches that command, and instruction
version 6 allows existing version 5 blocks to refresh. The existing parity
assertion stays unchanged; a real temporary-file upgrade checks new guidance,
preserved user text, one instruction block and repeat-call byte equality.
This is a bounded `bv-apal.3` repair, not closure of its remaining release gates.

Final Cass/UI race verification passes 167 Cass and 2,023 UI test entries,
with nine explicit UI skips. The opt-in installed-archive check skips in that
ordinary suite, then passes when run locally against the real Cass executable:
three direct hits become three rendered modal sessions with matching fields.
Independent replay of the same test executable also passes. Its SHA-256 is
`ce7a4abd787a707bc6999df7928f88d554532ca3219df2eb6ed692f130b29d19`;
it was built remotely with Go 1.26.0 for Linux amd64. The archive stayed local.
At that revision the UI lookup remained synchronous, with a separate initial
health-probe timeout; the later responsiveness follow-up is described above.
These checks do not establish an end-to-end latency guarantee.

Independent agents-package verification passes 326 test entries with three
Darwin-only skips. All 24 documentation-parity test entries pass. The old v5
file-upgrade control fails before the version repair and passes afterward.
UBS exits 0 on the three changed agents files. Its scan of the eleven Cass/UI
files exits 1 with 61 critical and six warning findings: reviewed findings
misclassify session/UI comparisons as secret checks, a fixed executable's
query argument as shell injection, stored asynchronous cancellation as missing
cleanup, and test goroutines as loop-variable captures. No suppression was
added and the raw scanner result is not described as green. First-party
formatting passes; the same 49 vendor files remain unformatted.
Full `go build ./...` and `go vet ./...` complete remotely with exit 0 against
the final Cass and version 6 instruction sources. Their frozen overlay and
the live test executable's compile receipt share fingerprint
`be10a60ec5f51ec87d7708294b96ca354b8ec2c512b1bbb1b91ffdf8e5d190ec`.
No shared executable was replaced, and no release or native/performance gate
is closed by these checks.

## September 10 canonical-source repair

This is an additional bounded implementation review against `d70ebf45`, not a
new release or a repeat of the historical audit. Ordinary `bv --robot-next`
selected `beads.base.jsonl` when a normal tracker flush wrote that snapshot
177 ms after `issues.jsonl`. The live copies happened to be identical, but
the selected snapshot lacked the metadata-declared live action route.

The retained old-source control uses different issue contents: eight of nine
regression cases fail, including an empty canonical export resurrecting an
issue and a sidecar-only directory being accepted by the legacy fallback.
Base-only loading is the one passing control. The repair resolves local JSONL
filename authority before freshness comparison with SQLite/worktree sources;
explicit file overrides remain available. It also connects the previously
test-only merge-warning callback to human loading, keeping robot stderr quiet.
Normal tracker base snapshots are not classified as left/right merge artifacts.

The existing test that expected an empty preferred export to lose to an
arbitrary JSONL file was corrected to require the empty authoritative export.
Backup/deletion/merge tests retain those exclusions with a canonical positive
fixture; directory and symlink controls now use actual canonical filenames.
The discarded-candidate warning test still executes a real rejected JSONL
probe. The initial watch-export run exposed an obsolete expectation that an
ignored legacy file degraded authority: only that initial expected Boolean was
corrected. The corrupt-SQLite fallback, exact source/readback assertions and
15-second publication bounds remain unchanged. Raw controls and subsequent
checks are retained in `/data/tmp/bv-canonical-20260910-UPzL40`; final verification
and independent review belong to the original `bv-mvvu` and `bv-uoyj.1` records.

Final independent RCH verification on `hz4` passes 289 top-level tests and
268 subtests across the datasource, loader and workspace race suites, with no
race warnings. Two existing missing-fixture cases skip. Four real CLI tests
and four explicit-override subtests pass without skips, including the human
merge warning and silent robot control. The independent evidence is retained
in `/data/tmp/bv-canonical-independent-20260910-Sucibl`; all seven Go overlay
hashes match the final build and vet inputs. Root's extended CLI cohort passes
all 46 source, authority and watch test entries with unchanged deadlines.

A combined run also retains the installed tracker's four deferral failures:
the prior temporary candidate path had disappeared, and captured argv proves
the installed `br` ran. After copying and hash-checking the qualified candidate
into a fresh isolated worker directory, all 18 tracker-route leaves pass with
the final viewer source. This does not establish the shared installed-tracker
gate. Neither shared executable was replaced by this repair.

Full `go build ./...` and `go vet ./...` both complete remotely with exit 0.
First-party formatting and diff checks pass; the same 49 vendor files remain
unformatted. UBS exits 0 for the seven changed Go files, with no critical
findings and two reviewed false positives: a loop capture under per-iteration
Go semantics and a SQL result explicitly closed in the existing test. No
finding is suppressed. Independent review reports no remaining substantive
finding; native-platform and original performance gates remain separate.

## September 10 installer diagnostics and tracker recheck

`80450e34` preserves both version-check streams in `install.ps1`, retaining
at most 4,096 characters each while draining excess output. The execution
deadline stays at 10,000 ms; post-exit draining stops after 1,000 ms even if a
descendant inherits a pipe. Output exceeding the cap is rejected before version
validation, including whitespace that previously disappeared during trimming.
The two README Windows install commands pin this source revision.

The unchanged function loses both stream markers in three timeout controls
and hangs beyond the 15-second outer guard after a parent exits with inherited
pipes. The repaired function passes all eight real-process controls plus the
four existing archive-fixture checks. An independent replay of the final bytes
passes all twelve checks with no skips, including direct-child termination
before fixture cleanup. Its `independent.stdout` SHA-256 is
`56df062af03c112d2eac4d228ec2fe28e1180d05de31a8a1246276409ee3c355`.
Raw output is retained in `/data/tmp/bv-ps1-timeout-20260910-cQm5qS`.
These portable checks use PowerShell 7.5.4 on Linux; they are not native Windows
source first-start evidence. A separate OldSurface check runs the exact function
under Windows PowerShell 5.1.26100.9444 against the retained v0.23.0 executable.
Both the implementing agent and root run the successful-version and wrong-tag
cases: root observes exit 0 in 1,145 ms and the intended exit 1 in 561 ms, with
the executable's hash unchanged. The native driver checks source and executable
hashes and uses the real platform guard. Raw output and the driver are retained
in `/data/tmp/bv-ps51-compat-20260910-xi8wYK`; SSH warnings are preserved. This
establishes those two PS5.1 paths, leaving native timeout handling and the current
source-built executable's first invocation unproven.

RCH build and vet pass for the unchanged Go tree
at `f0133a4a`. First-party formatting and Bash syntax pass. ShellCheck reports
the same three false unreachable-code notices for the existing EXIT trap in
both old and new harnesses. UBS has no PowerShell or shell scanner and exits 3;
this is explicitly not a scanner pass. No assertion or scanner suppression was
added. Live GitHub metadata still identifies v0.24.1, published September 8 at
00:28:07 UTC; no release was created.

Installed `br 0.5.12` now passes the unchanged fourteen live-route cases at
`f0133a4a` through RCH with zero skips. Three competing-claimant pairs each
produce one winner and one assignment-validation error. S5 remains open:
after a captured action's issue is deferred to 2099, fresh `bv` correctly
withholds a claim but the captured action still succeeds in `br`. Both a
direct tracker control and the installed v0.24.1 typed-action journey reproduce
this. Beads comments 469–471 retain source/tool identities and raw paths;
Agent Mail message 215 hands the defect to the tracker agent, who acknowledged
it in message 218. This is an observed deferral failure, not proof of a
closed/claim transaction race. No tracker source or installation was changed
by this session. The intervening `f0133a4a` recipe wording/test correction is
also included in Unreleased; the intervening WAL proof commits add verification
without changing the already-described watcher behavior.

## September 9 SQLite WAL follow-up

`bv-oonu.21` extends the shared watcher to the selected SQLite database's exact
`-wal` companion, covering event delivery and polling. Source matching uses the
same three extensions as the loader. WAL removal after checkpointing signals a
change rather than removal of the main database; `-shm` and unrelated siblings
are ignored. JSONL polling does not gain another filesystem lookup.

The retained `06cc108f` binary, whose watcher matches the pre-repair source,
reads the committed `after` row in a fresh triage process but leaves its running
Pages export at `before` for the full 15-second observation. The writer stays
open, main-file metadata stays unchanged and the WAL changes. Before the fix,
both real SQLite watcher regressions fail at their three-second deadline;
the JSONL sidecar control passes. All raw evidence is retained in
`/data/tmp/bv-wal-refresh-20260909-wwqn2zz5`.

The affected RCH race suite passes 1,143 tests: 37 watcher, 70 datasource and
1,036 UI tests. Eight existing UI tests skip: two Phase 2 timing transitions,
one permission test under the worker's root account, and five opt-in performance
or stress tests. Independent review found no runtime defect and requested
stronger checks for the actual watcher backend and WAL creation; those
assertions are now present. The real-close test verifies notification without
a database-removal error, but does not isolate sidecar removal from the
checkpoint's main-file write. The final focused suite passes five top-level
tests and twelve subcases with no skips, including all six Pages source cases
and four TUI refresh cases. The stronger backend and WAL-creation assertions
pass as well. The independent verifier repeats all five tests and twelve
subcases at `2cd958d6630170976fedd54415e2dda360645d25`, using RCH's clean
committed tree with no overlay on vmi1153651 and Go 1.25.5. The run exits 0
with no failures or skips; existing deadlines stay unchanged. The retained
`independent.stderr` has SHA-256
`5922514ecbbf8c556ff3557b91899833396ea61b7f35d27a5d5e1f50688c6bac`.
This establishes the selected-source Linux fixture behavior, not automatic
source switching, native-platform acceptance or the original performance and
tracker gates.

Build and vet pass through RCH; first-party formatting is clean, with 49
unchanged vendor files listed. UBS 7.1.2 exits 0 with no critical findings,
18 warnings and 178 informational findings, using regex fallback because
ast-grep is unavailable. Its lock heuristic flags all 17 lock calls: seven
production calls have immediate deferred unlocks, and ten existing test calls
use short explicit pairs. The remaining warning is a missing module file in
the scanner's temporary shadow. No missing unlock was found and no finding
was suppressed. An initially overbroad description of those 17 sites as
existing was corrected in the Beads record; one belongs to the new WAL poller.

During verification, another process committed and pushed the partial runtime
and unit-test patch as `cd100c66`, followed by native-prerequisite notes at
`9bb51cc0`. This session did not create those commits. Their history is retained;
closure waited for verification of the complete source and test tree.
The same concurrent activity later committed the remaining tests at `2cd958d6`
and documentation/tracker changes through `b3cdaa62`. The resulting source and
test tree is the one independently verified above; no runtime changes were
made after the successful race suite. No Actions run was listed for the first
push or the later push through `b3cdaa62`.

## September 9 live-source follow-up

`ccc166e9` (`bv-oonu.20`) binds TUI and single-repository Pages watchers to the
successful startup `LoadResult.Source.Path`. It removes a second, unvalidated discovery
pass that could choose a corrupt newer source instead of the loaded fallback.
Historical `--as-of` exports reject live watch mode before loading or writing.
The version stays unreleased; live GitHub metadata still identifies v0.24.1
as published on September 8 at 00:28:07 UTC.

Evidence is retained in `/data/tmp/bv-watch-source-20260909-n5wgm1md`.
The old clean binary renders initial fallback data but misses its TUI update.
The old-source RCH test identifies the wrong SQLite watch path; its JSONL case
separately times out during export. One old-binary Pages mutation is consumed
by the startup settle recheck, so it does not prove notification delivery.
The new test requires two successive edits, including a real explicit SQLite
source, and verifies source authority as well as the published issue IDs.

The complete export cohort passes 40 tests and nine subcases. Package checks
pass 225 tests; four existing TOON tests skip because this worker lacks `tru`.
Build and vet pass through RCH. The initial hz3 run was cancelled during severe
disk I/O pressure; verification moved to vmi1153651 with unchanged deadlines.
The new TUI test initially lacked real terminal sizing; explicit 110-by-35
PTY sizing makes both foreground and background refresh cases pass. Independent
review also caught an ambiguous title match in the existing Flow journey;
its waits now require the detail heading before sending Escape. The original
failures remain recorded. The final five-journey cohort passes all three
repetitions: 15 top-level runs, 18 subcases, no failures or skips. UBS remains
nonzero: its worker-side regex scan classifies console/help output as XSS,
reports a missing module in its temporary shadow, and misses the existing
timer Stop/Reset. The source review records these limitations without
suppressing findings.

Independent replay at committed `ccc166e9c43f200e3c8d5b26136a4c25a2c0ebe8`
passes all five focused tests and six subcases, with no failures or skips.
RCH confirms remote execution on vmi1153651 with `--clean-overlay --no-overlay`,
Go 1.25.5 and exit 0. This verifies the selected Linux CLI/PTY fixtures, not
SQLite WAL behavior, workspace source switching or other native platforms.
The raw `independent.stderr` log has SHA-256
`803be9b1554cb39c876a99ff68b4077b220a400c3099975fea4c65809a3b8191`.
The original performance, tracker, native and final proof gates remain open.

## September 9 dashboard follow-up

`06cc108f` repairs exponential capacity path enumeration on acyclic graphs
(`bv-xbvo.14`). A reachable-subgraph postorder computes each longest suffix
once, preserving the first longest path in seed/neighbor order. Only the path
search becomes O(V+E); other capacity calculations retain their existing costs.
Reachable cycles keep the original exhaustive simple-path fallback.

The original 26-node complete DAG profile attributes 89.21% cumulative CPU to
the repeated path walk. On the same host and fixture, with three warmups and
ten measured runs per binary, clean baseline `4abf4bec` has a 3.5547-second
median and 47,024 KiB maximum RSS; clean `06cc108f` has a 64.25-ms median and
38,972 KiB maximum RSS. Nearest-rank p95/p99 equal each ten-sample maximum:
4.0516 seconds before and 65.48 ms after. These are descriptive fixture results,
not the original P1 performance proof or a tail-latency guarantee. The earlier
working-tree run's 58.14-ms median is retained separately. Its after-profile
contains only 20 ms of CPU samples, too few for a reliable new hotspot ranking.

All 126 command-package tests and twelve capacity/forecast/scoping CLI tests
pass, with 152 and 80 subcases respectively, zero skips. The new path test also
compares 200 seeded small graphs with the original traversal. Build/vet and
first-party formatting pass; 49 existing vendor files remain unformatted.
UBS exits 1 with the same reviewed cancellation/invariant-panic findings and
scanner-workspace warning recorded below. An initial local build selected
Go 1.24.13 and failed the version requirement; the verified absolute Go 1.25.5
compiler built successfully. Both logs remain; the selection's cause is unproved.

Fresh solo replay at clean `06cc108f` passes all three focused handler tests,
17 subcases and all twelve CLI tests. The new 64-node dense DAG completes in
62.36 ms; the retained original binary still exceeds the unchanged five-second
deadline. All 102 complete JSON responses across 51 fixtures and two scopes
match the original immutable goldens, including version/envelope fields. This
is author verification, not independent review or completion of the broader
tracker, native, performance or final gates. Evidence and the clean binary are
in `/data/tmp/bv-capacity-dag-20260909-2redfrz4`; binary `bv-06cc108f` has
`vcs.modified=false` and SHA-256
`3afae80f5b79efb65cac6f130ca7b5a2627dfea99593e8955cf5a784dba064f5`.

`9de473f4` applies candidate selection to forecast output and requires a single
target to pass the same label/sprint filters (`bv-xbvo.13`). Estimate calculation
still uses the loaded graph, median and closure context. Blocked work remains
forecastable. Seven of eleven new handler cases and five of twelve new CLI cases
fail before the repair; after it, all 125 command-package tests and eleven
forecast/capacity/scoping CLI tests pass, with 142 and 80 subcases respectively
and zero skips. Build/vet and first-party formatting pass. UBS exits 1 with the
same existing cancellation/invariant-panic findings and scanner-workspace
warning described below; no suppression was added.

A full disk interrupted the Beads JSONL export after its database mutations
succeeded. `4abf4bec` records the successful export after relocating this session's
reproducible capacity binary to runtime storage, preserving its SHA-256 and an
original-path symlink. No source, logs or original f24e2df7 evidence were removed.
The relocated capacity binary and new forecast binary are on volatile runtime
storage; their exact source revisions and durable logs remain available for
rebuilding after reboot. Disk pressure is not fully resolved.

Fresh solo replay at clean `4abf4bec` passes all eleven handler cases and all
eleven CLI tests; the original binary still fails five of twelve CLI cases.
Four complete JSON responses (all, single, agent-scaled and forecast-label)
remain identical. The global-label reproduction removes only the excluded
forecast and its aggregate summary, preserving the selected issue's estimate.
The Go 1.25.5 binary has SHA-256
`efdb1d0cbd5d6bccc9ce3b0dcfeb82b29d4f3dde93ce0d2d127addf610f49742`
and `vcs.modified=false`. Durable evidence is in
`/data/tmp/bv-forecast-scope-20260909-ps070jwk`; the binary is
`/run/user/1000/bv-preserved-builds-20260909/bv-forecast`.
This is bounded fixture verification by the author, not independent review
or completion of the original performance, tracker, native or final gates.

Publication encountered a concurrent upstream commit, `d76172b6`, which preserves
patched modules under `third_party/` and updates two tests. Merge `c06fbfee`
retains both workstreams. Its 125 command tests and fourteen of fifteen selected
CLI/integration tests pass, including the new replacement/vendor comparison;
all 94 subcases pass. The repository-history test skips remotely because the
worker lacks `.beads/issues.jsonl`. Build/vet and first-party formatting pass.
The incoming stale-claim test conditionally skips old tracker versions; that
condition did not trigger on this worker, but does not replace the still-open
transactional-claim proof. UBS on the three incoming test files exits 1 with
two existing intentional shell-execution matches and the missing-module warning.
The unchanged repository-history test passes locally against the merged tree:
all three correlation strategies contribute, the frozen 500-commit window and
boundary pair are preserved, and the existing 15-second deadline passes. The
remote skip remains a separate result. The integration review also narrows the
incoming changelog's binary-equivalence wording to the verified unchanged
vendored package sources; build metadata can change executable bytes.

`ab144521` repairs the capacity handler's separate readiness calculation
(`bv-xbvo.12`). Global candidate scope now intersects the capacity label, while
the shared readiness index retains full-source prerequisite and lifecycle gates.
Only distinct blocking edges between selected unresolved issues contribute to
direct bottlenecks and the existing longest-chain duration heuristic. Sorted
seeds, neighbors and bottleneck ties give stable output.

Both new handler tests fail before the repair, including all seven readiness
subcases; the real CLI fails five of seven new scope cases. Afterward, all 124
command-package tests and eight capacity/scoping CLI tests pass, with 131 and
68 subcases respectively and zero skips. Build/vet and first-party formatting
pass; 49 existing vendor files remain listed by gofmt. UBS exits 1 with one
critical and ten warnings: its cancellation checks miss the existing deferred
cancel outside the conditional assignment; eight registration-invariant panics
are intentional, and the three-file scanner workspace lacks `go.mod`.
These findings remain recorded without suppressions or a clean-scanner claim.

Fresh solo replay at the committed revision passes both handler tests and all
eight CLI tests. The same CLI regression still fails five of seven cases against
the retained original binary. In the original seven-row reproduction, both label
forms now select six backlog items and exactly one actionable item. Three simple
capacity outputs (one agent, three agents, and a capacity label) retain identical
complete JSON against the original binary. This is bounded fixture evidence,
not an independent review, a scheduler, or the missing performance/native/tracker
proof. The clean Go 1.25.5 binary has SHA-256
`ae2a73f69677ef8c05f6579abb0811f42636c7d117c17f49c0630bddb6d400ee`
and `vcs.modified=false`. Logs and binaries are retained in
`/data/tmp/bv-capacity-readiness-20260909-9ydryzod`.

`f24e2df7` fixes a related CLI scope leak (`bv-xbvo.11`): label-scoped
`top_what_ifs` ranked an outside prerequisite ahead of selected issues, and
top-k metadata counted that neighbor as a candidate. Two predicates now apply
the existing candidate scope. Hypothetical impact still includes blocked
selected issues; the feasible top-k sequence retains full-source readiness.
The README corrects its assertion that label-scoped metrics contain only
matching issues: metrics and structural paths intentionally retain neighbors.

Before the repair, seven of nine new analyzer cases and one of five new CLI
cases fail on candidate IDs/counts. Afterward, the complete analysis and CLI
packages pass 924 top-level tests with twelve existing skips: ten opt-in
performance tests, the golden generator, and an explicitly skipped
self-reference test. Six CLI scoping tests with 61 subcases pass without skips.
Build/vet and first-party formatting pass. UBS exits 1 with two critical
matches for unchanged intentional shell-execution tests and three warnings;
the warnings concern existing loop/cancellation heuristics and the scanner's
four-file workspace lacking `go.mod`. Findings remain unsuppressed.

Fresh solo replay at the committed revision passes all nine analyzer cases and
all six scoping tests. A clean Go 1.25.5 binary has SHA-256
`a86aa57a36e86bbcb4587aca749b6588919cf74bef786117d2bbf942405c00aa`.
The same committed CLI regression still fails its label case against the
retained original binary, with the other four cases passing.
Comparing actual original/fixed CLI output on the retained reproduction gives
identical complete unscoped JSON; scoped JSON differs only in the outside
ranking entry and candidate count. This is bounded fixture evidence, not an
independent review or the missing full performance/native/tracker proof.
Logs and binaries are retained in
`/data/tmp/bv-insights-scope-20260909-gfypkwh8`. `75f70440` recorded the preceding
dashboard verification and tracker closure; neither commit creates a release.

`5efb1daf` fixes dashboard candidate selection before graph optimization
(`bv-oonu.19`). The original six-issue browser fixture chooses a missing
prerequisite, then inflates the real root's gain from one to two. Optional
candidate masks in the Rust algorithms preserve unresolved dependency context;
the dashboard supplies visible issues for greedy picks and full-source ready
issues for cascade suggestions. Actionable lookup now reads the same exported
readiness predicate as the Ready filter. The README distinguishes potential
graph unblocks from live tracker claims.

Fresh solo replay of the clean Go 1.25.5 binary at this revision passes eleven
real browser journeys and still rejects both original suggestion/readiness
exports. All 231 Rust and 452 command/export Go tests pass; the two established
live-Pages/Windows-only Go skips remain. Two isolated WASM builds agree, and a
third build at the committed revision verifies both shipped assets against the
manifest. The installed Rust compiler is shared by these builds; this is not
a fresh run of the two-physical-Rust-home shell suite. The unchanged Node block
from that suite verifies five graph fixtures and twelve metrics across all
three modules. Another 240 exact comparisons with the original engine preserve
unrestricted and all-candidate top-k/what-if behavior. No golden was regenerated.

Initial concurrent browser runs had cache, navigation and CDP failures under
disk pressure. A subsequent scratch directory on `/dev/shm` hit a user quota;
global free space did not imply writable quota. Sequential runs in the user's
runtime directory pass with unchanged assertions and deadlines. Navigation/CDP
root causes are not established by those passes. The first readiness-negative
attempt also used a nonexistent bundle path; the corrected run demonstrates
the semantic failure. All these failures remain recorded. UBS remains nonzero
with 63 critical heuristic findings and 336 warnings, including generated
null-prototype literals, browser comparisons and declaration parsing. No
suppression or clean-scanner claim is made.

Persistent evidence: `/data/tmp/bv-pick-scope-20260909-q49x1l5x`, including six
replayed bundles, browser captures and `replay-wasm-proof/receipt.json`.
The clean binary's SHA-256 is `4e791f0285aa98ed63dbdb7567aaaea3291cc53cbb88c78df3c8e95931e005a4`.
Build/vet, syntax, first-party formatting and vendor checks pass. `b0d5f189`
and `80a2d47c` recorded the preceding graph-export evidence and corrected its
tracker checklist formatting. Latest release metadata remains v0.24.1; no new
release, complete remote-suite, original-baseline, transactional-claim or native
platform proof is implied.

`75be8362` connects HTML/SVG/PNG graph exports to the existing recipe scope
pipeline (`bv-oonu.18`). The original actionable export included blocked and
closed rows; 18 of 24 new format cases fail before the repair. The fixed
artifacts honor custom recipes, sort limits, label/repository intersections,
full-source readiness and empty-selection refusal. PNG checks decode the image
and compare its dimensions with a separately checked SVG; they are not an OCR
or pixel-perfect rendering claim. Both retained PNGs were also visually read.

Fresh solo replay uses unchanged runtime/test source at `75be8362`; the clean
Go 1.25.5 binary has SHA-256 `e23585425a9920279411f7b103f85bfd3bdda384c5b4069860ff6aacce605333`
and `vcs.modified=false`. All 24 graph recipe cases pass remotely. That remote
replay also exposed a robot-alias timeout, a watched-export deadline failure
and a TUI environment skip. Those tests passed three unchanged local runs;
one full local replay passes 490 command/export/recipe tests and 34 selected
CLI tests, with the two existing live-Pages/Windows-only unit skips. The remote
failures remain recorded, not replaced by a claim that both environments pass.
Post-failure worker telemetry does not establish the timeouts' root cause.

Required remote build/vet pass; first-party formatting is clean. UBS exits 0
with two inspected warnings: selected-file staging lacks go.mod, and an
existing timer's Stop/Reset calls are missed by its heuristic. Evidence is
retained at `/data/tmp/bv-graph-recipe-20260909-1wkluyri`. `0d6b24ca` recorded
the preceding ranking proof. No runtime change followed the verification;
only changelog, bridge-plan and Beads notes changed. Latest release metadata
still reports v0.24.1; Actions remain disabled. Broader acceptance stays open.

`3a56b922` repairs WASM metric consumers that limited graph indices before
resolving exported issue rows (`bv-oonu.17`). Six real issues with eleven
missing prerequisites produced only one authority card. The browser now selects
actual issue IDs before limiting results, preserving raw graph scores and
zero-slack filtering. Rust and shipped WASM/glue bytes are unchanged.
Fresh solo replay at this revision passes desktop/mobile ranking boundaries,
navigation, an empty export and existing HITS/readiness/what-if variants: nine
browser journeys. All 452 command/export Go tests pass with two existing skips
(live Pages deployment and Windows-only path handling). Build/vet, JS syntax
and first-party formatting pass. UBS remains nonzero: 56 critical heuristic
findings (secret-comparison and global-assignment rules), 162 warnings, no
suppression or clean-scanner claim. Original and final browser outputs are
retained at `/data/tmp/bv-ranking-scope-20260909-PAYK1U`; the original still
fails the unchanged expected-six assertion. Clean Go 1.25.5 binary SHA-256:
`36b8c865…`, `vcs.modified=false`. `9fa49931` recorded the preceding export-scope
repair. Latest live release metadata remains v0.24.1, published September 8 at
00:28:07 UTC; Actions are disabled. No new release or broader proof is claimed.

`d10d341d` connects direct Pages exports and watched reloads to the existing
recipe, repository and label selection pipeline (`bv-oonu.16`). The original
binary ignores `actionable` during export and expands a watched repository
selection after its initial attachment recheck. The same retained CLI/SQLite
replay rejects both original results and accepts the clean Go 1.25.5 binary
at that revision (`vcs.modified=false`, SHA-256 `11af8f5e…`). Fresh solo
verification passes all 160 command/recipe unit tests and all 64 selected
Pages/recipe/scoped-robot CLI tests, with zero skips in that final run. The
earlier remote CLI run skipped one hybrid-WASM test, subsequently run locally.
Build/vet and first-party formatting pass. UBS exits 0 with seven inspected
warnings and zero critical findings; this is not a warning-free scan.
The first checks caught a leftover local variable and an obsolete recipe test
rejecting custom statuses allowed since `55ec82b8`. Its replacement verifies
blank-status rejection, custom-status selection and their non-actionability.
No acceptance assertion, timeout or snapshot was relaxed to accommodate the
export repair. Evidence: `/data/tmp/bv-export-scope-20260909-9q38zb`.
The final notes also correct a test comment; its assertions remain unchanged.
Live release metadata still reports v0.24.1 published September 8 at
00:28:07 UTC. Actions remain disabled. No release or P1/S5/native proof is
claimed. `cf2b6782` recorded the preceding dashboard verification.

The next bounded repair covers `bv-oonu.14` and `bv-oonu.15`. HITS data already
exists in the shipped WASM; the dashboard read singular names instead of its
`hubs` and `authorities` fields. The original real-browser bundle fails with an
empty hub list. The readiness consumer separately counted six ready tasks in
a nine-row fixture where only three qualify. Export now carries the full-source
readiness index and clock into SQLite; browser counts, quick wins and filters
read those predicates. Active-node totals no longer double-count blocked work
or depend on every status being present. The graph engine bytes are unchanged.

Evidence is retained in `/data/tmp/bv-hits-20260909-btyveb`: desktop/mobile HITS
and readiness journeys, existing what-if journey, 451 Go unit tests (two existing
export skips), and all 37 selected CLI export tests. RCH's first CLI run exposed
a fixture mistake: label scope includes neighboring context. Using the documented
repository filter preserves the original six expected IDs and every readiness
assertion. Local CLI execution also runs the two tests skipped by that worker's
environment. Browser setup corrections wait for visible cards and revisit
Insights after issue navigation; no scores, thresholds or timeouts were relaxed.
Fresh solo replay at `51d25a80991e2488f85eb030ceb7d8d126d8f9ba` passes the same
451 unit and 37 CLI tests, both desktop/mobile HITS and readiness journeys,
and both included/excluded closed-row simulation journeys. The clean Go 1.25.5
binary records that revision with `vcs.modified=false`, SHA-256 `54050d86…`.
The final harness still rejects both original bundles. Build/vet and first-party
formatting pass; 49 unchanged vendor files have formatting drift. UBS exits 1
on inspected heuristics (50 critical, 164 warnings across nine files), with no
suppression or clean-scanner claim. The six Go warnings include four existing
panic sites, a missing-go.mod result from selected-file staging, and a timer
whose stop/reset is present. `6ede497c` records the previous repair's evidence;
`51d25a80` supplies these runtime changes. Fresh release metadata still reports
v0.24.1 published September 8 at 00:28:07 UTC; Actions remain disabled. No
release or original P1/S5/native completion follows from these fixes.
The final evidence-only commit changes no runtime or test files. Its Markdown
UBS attempt exits 3 (unsupported input), not a scan pass; changelog structural
validation passes with the existing older-history bare-hash warning.

The additional `bv-oonu.13` repair follows the actual dependent-to-prerequisite
edges through Rust what-if/actionability/parallel-cut queries, SQLite export
metadata, both browser graph engines and the detail/priority templates. The
original Chromium bundle returns zero direct unblocks for a root that releases
one child; the repaired root reports one direct and two transitive unblocks.
Closed and tombstone prerequisite identities survive omitted issue rows. A
second real browser failure exposed calls to an unsupported renderer refresh
method; those now use the existing redraw helper. Reset cancels pending timers.
The cleanup regression also caught the renderer ticking after its graph was
released. Cleanup now stops rendering using the documented
[`pauseAnimation()` API](https://github.com/vasturiano/force-graph#render-control),
confirmed in the bundled source, and delayed zoom callbacks check graph identity.

The unchanged pinned rebuild harness produces identical glue and WASM from
two physical Rust homes and passes all five graph fixtures and negative
controls. Root re-execution passes 202 unit and 25 existing Rust golden tests;
the affected Go run passes 515 top-level tests with two existing export skips.
Both real Chromium simulation variants pass, including actual edge pixels,
displayed gains and reset. Browser setup failures (visible row count, CDP proxy
serialization and the default link-color accessor) are retained separately
from product counterexamples. Evidence: `/data/tmp/bv-whatif-20260909-5k4UlI`.
No version bump or publication accompanies this follow-up.
Final source commit: `40a7cd07d0dab2d8a2a7a8c0ee1874b120fe3d9a`; preceding
`aa5efbeb` only records the previous repair's evidence. A clean binary from
the final commit, both Chromium variants, the existing blocking-types journey,
515 Go tests and 227 Rust tests pass on fresh solo re-execution. The original
bundle still fails the exact direct-count assertion. `bv-oonu.13` closes after
that replay. UBS remains nonzero on inspected heuristics; no suppression or
golden regeneration was added. Fresh release metadata still identifies
v0.24.1 published September 8 at 00:28:07 UTC, neither draft nor prerelease;
repository Actions remain disabled.

The nine-commit window `b6e21d22..1c768eac` contains two runtime changes:
`55ec82b8` preserves producer workflow vocabulary and classifies blocking types;
`1c768eac` connects those types to the static dashboard's SQL and JavaScript
consumers and fixes reversed dependency keyboard navigation. `8ab18310` corrects
the test setup for the documented unset insight limit. The other six commits
(`953c831d`, `8477a01f`, `5003be9d`, `768a1db9`, `cf6fe649`, `3dc66c65`)
record earlier delivery, research and remaining proof. They add no runtime
capability. Source diffs and the existing Beads evidence establish these scopes.

The dashboard counterexample exports seven real JSONL issues into SQLite:
four active blocking dependents, one closed dependent and one informational
reference to a custom-status prerequisite. Original `55ec82b8` reports four
dependents but lists only two; Chromium fails the exact expected-ID assertion.
The repaired bundle includes all four, keeps only the informational reference
ready, preserves historical graph edges, and executes h/l navigation correctly.
The SQL regression covers eight relationship types and five endpoint lifecycle
pairs. Its first setup omitted analyzer metrics; that setup error and the first
failed full suite are retained, not counted as product counterexamples.

The final full export package has 328 top-level passes and two existing skips
(live GitHub Pages deployment and a Windows-specific path test). All 35 selected
page-export CLI tests pass. Build/vet and first-party formatting pass; UBS exits
1 on reviewed heuristics, so this is not a clean scanner claim. Evidence is in
`/data/tmp/bv-continuation-20260908-o5jLRf`; `bv-oonu.12` records the fresh solo
acceptance replay and its limits. No release, native-platform or complete
performance-matrix result follows from this repair. Fresh GitHub metadata still
reports published v0.24.1 at `2026-09-08T00:28:07Z`, neither draft nor prerelease;
Actions remain disabled. This extends the existing research memo only.
The changelog validator passes structural checks with the existing older-history
bare-hash warning. The documentation-only UBS attempt exits 3 because Markdown
is unsupported; it checks nothing and is not counted as a pass.

## September 8 Flow follow-up

Small-update window `7983ee3f..b6e21d22`, researched from all four commits:
`84c3774e` updates the previous changelog; `eb2a7c86` records the assessment and
Flow counterexamples; `ce982942` records tracker comments only; `b6e21d22`
implements directed relationship drilldowns, live endpoint inspection and the
identified documentation repairs. The complete runtime/test diff was reviewed,
then original acceptance replayed on exact `b6e21d22f6098a78553aed2a365526bf98730fd6`.
Beads `bv-apal.12` and `.13` are closed after solo verification; `.3` and `.4`
remain unfinished. No new release or tag was created. Fresh GitHub metadata
still identifies v0.24.1 as published at `2026-09-08T00:28:07Z`, neither draft
nor prerelease. Repository Actions are disabled.

The original rendered-model counterexample now passes unchanged. Complete
UI/analysis race and focused internal/docs/PTY execution produced 1,862
top-level passes and 19 explicitly retained pre-existing skips. A separate
post-claim P6 replay passes 27 top-level tests with no skips. These are solo
source and Linux terminal fixture checks, not independent-agent, full performance
matrix or native-target proof. Raw streams and failed setup attempts remain in
`/data/tmp/bv-reality-20260908-1umcHa`; the detailed honesty inventory is in `.12`.
The selected-file UBS scan exits 1 on reviewed heuristic findings; no clean
scanner result or suppressed rule is claimed. This existing memo records the
requested changelog provenance and retires as an active checklist with the update.

## Coverage and completion

- [x] Read the skill and its research, linking, and quality guidance; review
  project instructions, installation guidance, and existing changelog.
- [x] Verify the v0.24.0 and v0.24.1 tags and live publication dates.
- [x] Research every commit in the v0.24.1 release window, inspect runtime
  diffs and regressions, and connect the changes to Beads workstreams.
- [x] Distill that window into the live changelog with representative links.
- [x] Research all five post-tag commits through `7983ee3f`; distinguish
  installer delivery from the immutable tagged binaries and test evidence.
- [x] Distill the follow-up under Unreleased, including material limits.
- [x] Correct the README sentence that still described the newly pinned
  Windows installer's source option as the older `go install` implementation.
- [x] Check scope, dates, links, tracker references, and unchanged older history.
- [x] Run the skill validator, whitespace validation, and the required UBS
  attempt; record results. Delivery is tracked in documentation bead `bv-ie9m`.

## Findings

### Release window: 11 commits, distilled

Live GitHub metadata identifies v0.24.0 as published at
`2026-09-07T06:29:55Z` and v0.24.1 at `2026-09-08T00:28:07Z`; neither is a
draft or prerelease. Annotated tags resolve respectively to
`8c1f0011a78b4f291ff175af7276ae450f772cd2` and
`3e4e61c91a74dafe211d3f6a62f3c2919969657c`. GitHub's v0.24.1
`target_commitish` says `main`; the tag and binary receipts establish the
immutable revision, not that mutable field.

All commits in `v0.24.0..v0.24.1` are accounted for:

- `6c8a4474`: loaded source-hash reuse; inspected the full runtime diff and
  all 15 source/scope cases exercised by two robot commands.
- `f719c41a`: optional cache writes use the existing nonblocking lock;
  inspected real contention, entry preservation, later-publication tests,
  and the Windows test-fixture correction to capture open-handle identity.
- `3e4e61c9`: version, Nix, installation examples, release documentation,
  changelog, and release-tracking metadata.
- `59d98598`, `43c6cc62`, `cf7850d5`, `4a5a564f`, `f8fd09c0`, `578e34bf`,
  `51a15788`, `ce0d64ee`: Beads and bridge-plan evidence updates for the
  latency campaign. These do not add runtime behavior or close that campaign.

The resulting themes are preserved hash semantics, bounded optional-cache
publication, and verified release distribution. Homebrew commit `cae0685b`
and Scoop commit `4fddb86e` publish the five matching archive hashes.
The release bead was still in progress at the tag; its later closure is
linked to the checked-in record at `7983ee3f` (line 422). The latency bead
remains in progress at line 283.

Evidence retained in `/data/tmp/bv-release-v0.24.1-20260908`:

- `gate-result.json`: exit 0 on the clean tagged revision, 835.838 seconds.
- `publish/release-gate-receipt.json`: all five archives identify that revision,
  Go 1.25.5, CGO disabled, and unmodified source.
- Live release API: 14 published assets, including the receipt and Linux amd64
  SBOM, with no minisign asset.
- `performance-readback.json`: original campaign exit 1 retained; 144 outputs
  have only 72 `/version` differences (v0.23.0 baseline, v0.24.0 candidate).
  No new performance measurement or full acceptance is implied.

The changelog now corrects the stale scope and links the runtime commits,
published receipt, package-store commits, and precise tracker records.

### Post-tag window: five commits, distilled

- `3ca2176f`: function-local PowerShell progress suppression and correction of
  the native harness's obsolete top-level ID assertion. The robot binary's
  output schema did not change in this commit.
- `8bdfc079`: distribution and native-test evidence in Beads/release docs.
- `99d2066d`: despite its subject naming graph WASM, the actual diff appends
  comment 418 to **bv-oonu.10**, recording release distribution and remaining
  Windows/native-platform limits. No WASM implementation changed.
- `87756815`: four README installer URLs pin `3ca2176f`; native follow-up
  findings added to release documentation.
- `7983ee3f`: release closure and the complete default Windows suite's evidence;
  the optional source first-start failure and broader acceptance stay open.

The full installer and harness diffs establish that progress suppression is
post-tag. No Go runtime file differs between v0.24.1 and the research endpoint.
`windows-download-readback.json` records the default suite pass (28 logs,
eight capability results, five rejection reasons). `retained-source-startup.json`
explicitly labels the successful 1,742 ms invocation as a second diagnostic
attempt, not a replacement for the failed first-start result.

README still claimed the newly pinned installer used `go install`. Inspection
of `Install-FromSource` confirms a tagged checkout, `go build -mod=vendor`,
version checks, and revision checks; the installer is unchanged since
`3ca2176f`. Correcting that one sentence accompanies this changelog update.
The older XFetch expiry repair (`8285b6f6`) is an ancestor of v0.24.0 and is
not credited to v0.24.1.

All runtime and publication findings from both windows are distilled in
CHANGELOG.md. Older entries remain outside this audit. No additional runtime
change, new benchmark measurement, or new release publication is part of it.

## Validation

- The skill's `validate-changelog-md.py CHANGELOG.md` passes structural checks.
  Its sole warning concerns bare hashes in retained v0.24.0/older entries;
  those entries are outside this bounded historical audit.
- All 19 inline HTTP links in the updated introduction, timeline, Unreleased,
  and v0.24.1 sections returned HTTP 200. Checked with the repository-required
  User-Agent rather than the validator's hard-coded network User-Agent.
  Results: `/data/tmp/bv-release-v0.24.1-20260908/changelog-link-check.json`.
- All three pinned Beads line references resolve to the named records and
  stated statuses. Local research/release-documentation links resolve.
- A byte comparison confirms the v0.24.0 heading's following content through
  the reference footer is unchanged. `git diff --check` passes.
- UBS does not support Markdown/JSONL and exits 3 without scanning; this is
  not reported as a passing code scan. No Go code changed, so no Go test rerun
  was needed for this documentation update.
