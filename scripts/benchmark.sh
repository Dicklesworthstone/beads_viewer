#!/usr/bin/env bash
# benchmark.sh: the tracked bv benchmarks, their baseline, and the regression
# comparison used by scripts/release_gate.sh stage 8.
#
# Usage:
#   scripts/benchmark.sh baseline                 # regenerate benchmarks/baseline.txt (with provenance header)
#   scripts/benchmark.sh compare                  # run the tracked set, compare against the baseline, exit 1 above BENCH_PCT
#   scripts/benchmark.sh compare-files BASE CUR   # compare two existing `go test -bench` outputs
#   scripts/benchmark.sh run                      # run the tracked set into benchmarks/current.txt
#   scripts/benchmark.sh quick                    # one-shot subset for a fast local sanity check
#   scripts/benchmark.sh latency BASE_BV BASE_UI_TEST OUTPUT_DIR
#                        same-host cold/warm CLI and Update+View cohorts;
#                        BASE_UI_TEST is built with go test -c ./pkg/ui
#
# Environment:
#   BENCH_COUNT=5        -count per benchmark (the best observed time per side is compared)
#   BENCH_PCT=20         regression threshold in percent on best-of-N sec/op
#   BENCH_DATASET=tests/testdata/benchmark/medium.jsonl
#                        frozen issue file the RealData_* benchmarks read (copied to a temp
#                        BEADS_DIR as issues.jsonl); the live tracker is never used, so the
#                        baseline does not drift as beads close
#   BENCH_USE_BENCHSTAT=1 prefer benchstat when it is installed (the built-in comparison is
#                        the default so the gate has no dependency outside the Go toolchain)
#   BENCH_REFERENCE=worktree|stored
#                        worktree (default): `compare` also builds and runs the tracked set for
#                        the commit recorded in the baseline header, in a detached worktree,
#                        right before the HEAD run, and judges the regression against that
#                        contemporaneous run. A stored baseline alone cannot tell host drift
#                        from a code regression on a shared machine (2026-09-03: +38% against
#                        the stored file, 0% against the reference build). stored: compare
#                        against benchmarks/baseline.txt only (also the automatic fallback
#                        when the recorded commit is not available in this clone).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

BENCHMARK_DIR="benchmarks"
BASELINE_FILE="$BENCHMARK_DIR/baseline.txt"
CURRENT_FILE="$BENCHMARK_DIR/current.txt"
# The baseline is taken once with more repetitions; compare runs (gate stage 8)
# use fewer so the gate stays under ten minutes on the reference machine.
# BENCH_COUNT overrides both.
COUNT="${BENCH_COUNT:-5}"
COMPARE_COUNT="${BENCH_COUNT:-3}"
PCT="${BENCH_PCT:-20}"
DATASET="${BENCH_DATASET:-tests/testdata/benchmark/medium.jsonl}"

# The tracked set. Names are anchored so a new BenchmarkFoo_Sparse100Extra does not
# silently join the comparison; add it here deliberately.
TRACKED='^Benchmark(RealData_(FullTriage|FullAnalysis|GraphBuild)|FullAnalysis_(Sparse100|Dense100|ManyCycles20)|SnapshotSwap|KeyPressLatency|ListItemBuild|ParseIssuesPoolComparison)$'
PACKAGES=(./pkg/analysis ./pkg/ui ./pkg/loader)

mkdir -p "$BENCHMARK_DIR"

dataset_dir=""
ref_dir=""
# Plain ifs (not `[ ] &&`) so the trap's status never overrides the script's exit code.
cleanup() {
  if [ -n "$ref_dir" ]; then
    echo "Retained reference worktree and evidence: $ref_dir" >&2
  fi
  if [ -n "$dataset_dir" ]; then echo "Retained benchmark dataset: $dataset_dir" >&2; fi
}
trap cleanup EXIT

prepare_dataset() {
  [ -n "$dataset_dir" ] && return 0
  [ -f "$DATASET" ] || { echo "benchmark: dataset $DATASET not found" >&2; exit 2; }
  dataset_dir="$(mktemp -d "${TMPDIR:-/tmp}/bv-bench-dataset.XXXXXX")"
  mkdir -p "$dataset_dir/.beads"
  cp "$DATASET" "$dataset_dir/.beads/issues.jsonl"
  export BEADS_DIR="$dataset_dir/.beads"
  export BV_NO_UPDATE_CHECK=1 BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1
}

provenance_header() {
  # Lines starting with '#' are ignored by benchstat and by the built-in comparison.
  local cpu os
  cpu="$(lscpu 2>/dev/null | awk -F: '/Model name/{gsub(/^[ \t]+/, "", $2); print $2; exit}')"
  [ -n "$cpu" ] || cpu="$(uname -p 2>/dev/null || echo unknown)"
  os="$(uname -sr 2>/dev/null || echo unknown)"
  cat <<EOF
# bv benchmark baseline
# generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
# go: $(go version | cut -d' ' -f3)
# cpu: $cpu
# os: $os
# commit: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)
# dataset: $DATASET
# dataset_sha256: $(sha256sum "$DATASET" | cut -d' ' -f1)
# dataset_issues: $(wc -l < "$DATASET" | tr -d ' ')
# count: $COUNT
# tracked: $TRACKED
# command: go test -run '^\$' -bench '$TRACKED' -benchmem -count=$COUNT ${PACKAGES[*]}
# compare: scripts/benchmark.sh compare (best-of-N sec/op per benchmark against an interleaved reference build of this commit, fails above BENCH_PCT=$PCT%)
EOF
}

run_tracked() {
  # run_tracked <output-file> <count> [tree-dir]   (tree-dir defaults to this checkout)
  prepare_dataset
  (cd "${3:-$root}" && go test -run '^$' -bench "$TRACKED" -benchmem -count="$2" "${PACKAGES[@]}" 2>&1) | tee -a "$1"
}

save_baseline() {
  echo "Regenerating $BASELINE_FILE (count=$COUNT, dataset=$DATASET)"
  provenance_header > "$BASELINE_FILE"
  run_tracked "$BASELINE_FILE" "$COUNT"
  echo "Baseline saved to $BASELINE_FILE"
}

run_benchmarks() {
  : > "$CURRENT_FILE"
  run_tracked "$CURRENT_FILE" "$COMPARE_COUNT"
  echo "Results saved to $CURRENT_FILE"
}

# compare_files BASE CUR: best observed (minimum) ns/op per benchmark (the -N
# cpu suffix is stripped), delta in percent, worst regression versus BENCH_PCT.
# The minimum is the right statistic on a shared host: contention only ever
# inflates a run, so the smallest sample is the closest to the uncontended
# time, while a real regression raises every sample and therefore the minimum.
compare_files() {
  local base="$1" cur="$2"
  [ -f "$base" ] || { echo "no baseline at $base (run scripts/benchmark.sh baseline)"; return 2; }
  [ -f "$cur" ] || { echo "no current results at $cur"; return 2; }
  awk -v pct="$PCT" '
    function best(arr, n,   i, m) {
      m = arr[1]
      for (i = 2; i <= n; i++) if (arr[i] < m) m = arr[i]
      return m
    }
    function record(file, name, ns,   key) {
      sub(/-[0-9]+$/, "", name)
      key = file SUBSEP name
      count[key]++
      values[key, count[key]] = ns
      if (!(name in seen)) { seen[name] = 1; order[++n] = name }
    }
    FNR == 1 { file++ }
    # go test -bench lines: <name-cpus> <iterations> <value> ns/op [<B/op> <allocs/op>]
    /^Benchmark/ && $4 == "ns/op" { record(file, $1, $3 + 0) }
    END {
      worst = 0; missing = 0
      printf "%-42s %14s %14s %9s\n", "benchmark (best of N)", "base ns/op", "current ns/op", "delta"
      for (i = 1; i <= n; i++) {
        name = order[i]
        kb = 1 SUBSEP name; kc = 2 SUBSEP name
        if (!(kb in count) || !(kc in count)) {
          printf "%-42s %14s %14s %9s\n", name, (kb in count) ? "present" : "MISSING", (kc in count) ? "present" : "MISSING", "n/a"
          missing++
          continue
        }
        for (j = 1; j <= count[kb]; j++) a[j] = values[kb, j]
        for (j = 1; j <= count[kc]; j++) b[j] = values[kc, j]
        mb = best(a, count[kb]); mc = best(b, count[kc])
        delta = (mb > 0) ? (mc - mb) / mb * 100 : 0
        if (delta > worst) worst = delta
        printf "%-42s %14.0f %14.0f %+8.1f%%\n", name, mb, mc, delta
      }
      printf "worst sec/op regression: %.1f%% (threshold %s%%)\n", worst, pct
      if (missing > 0) { printf "%d benchmark(s) missing on one side\n", missing; exit 1 }
      if (n == 0) { print "no benchmark lines found"; exit 1 }
      exit !(worst <= pct + 0)
    }
  ' "$base" "$cur"
}

# reference_run: build and run the tracked set for the commit recorded in the
# baseline header, in a detached worktree, so HEAD is judged against a run
# taken on the same machine minutes earlier. Prints the reference file path on
# success; prints nothing (and says why on stderr) when it cannot.
reference_run() {
  local ref_commit
  ref_commit="$(awk '/^# commit: /{print $3; exit}' "$BASELINE_FILE")"
  if [ -z "$ref_commit" ]; then
    echo "baseline header has no '# commit:' line; falling back to the stored baseline" >&2
    return 0
  fi
  if ! git rev-parse --verify --quiet --end-of-options "${ref_commit}^{commit}" >/dev/null; then
    echo "baseline commit $ref_commit is not in this clone; falling back to the stored baseline" >&2
    return 0
  fi
  # ref_dir is created by the caller (this function runs in a command
  # substitution, so a variable set here would never reach the cleanup trap).
  [ -n "$ref_dir" ] || { echo "internal: ref_dir not set by caller" >&2; return 0; }
  if ! git worktree add -q --detach "$ref_dir/tree" "$ref_commit" 2>/dev/null; then
    echo "could not create a worktree for $ref_commit; falling back to the stored baseline" >&2
    return 0
  fi
  if [ -d "$root/vendor" ] && [ ! -d "$ref_dir/tree/vendor" ]; then
    cp -r "$root/vendor" "$ref_dir/tree/vendor"
  fi
  local ref_file="$BENCHMARK_DIR/reference.txt"
  {
    echo "# reference run of baseline commit $ref_commit on $(date -u +%Y-%m-%dT%H:%M:%SZ), interleaved per package with the HEAD run"
  } > "$ref_file"
  : > "$CURRENT_FILE"
  prepare_dataset
  # Interleave the two trees per package per round: a run five minutes apart
  # on a shared host still drifted by 30% either way, while adjacent runs of
  # the same package stayed within a few percent. The pair order alternates
  # each round (an even number of rounds), because always running the
  # reference first left a consistent +10-20% on packages that were
  # byte-identical between the two trees.
  local rounds=$(( (COMPARE_COUNT + 1) / 2 * 2 ))
  echo "Running the tracked benchmarks for baseline commit $ref_commit and HEAD, interleaved ($rounds rounds, alternating order)..." >&2
  local round pkg
  for round in $(seq 1 "$rounds"); do
    for pkg in "${PACKAGES[@]}"; do
      if [ $((round % 2)) -eq 1 ]; then
        (cd "$ref_dir/tree" && go test -run '^$' -bench "$TRACKED" -benchmem -count=1 "$pkg" 2>&1) >> "$ref_file"
        (cd "$root" && go test -run '^$' -bench "$TRACKED" -benchmem -count=1 "$pkg" 2>&1) >> "$CURRENT_FILE"
      else
        (cd "$root" && go test -run '^$' -bench "$TRACKED" -benchmem -count=1 "$pkg" 2>&1) >> "$CURRENT_FILE"
        (cd "$ref_dir/tree" && go test -run '^$' -bench "$TRACKED" -benchmem -count=1 "$pkg" 2>&1) >> "$ref_file"
      fi
    done
  done
  echo "$ref_file"
}

compare_benchmarks() {
  [ -f "$BASELINE_FILE" ] || { echo "No baseline found at $BASELINE_FILE; run 'scripts/benchmark.sh baseline' first"; return 2; }
  local base_file="$BASELINE_FILE" base_label="stored baseline $BASELINE_FILE"
  local have_current=0
  if [ "${BENCH_REFERENCE:-worktree}" = "worktree" ]; then
    local ref_file
    ref_dir="$(mktemp -d "${TMPDIR:-/tmp}/bv-bench-ref.XXXXXX")"
    ref_file="$(reference_run)"
    if [ -n "$ref_file" ]; then
      base_file="$ref_file"
      base_label="contemporaneous reference build ($(head -1 "$ref_file" | cut -c3-))"
      have_current=1 # the interleaved reference run also produced the HEAD results
    fi
  fi
  [ "$have_current" -eq 1 ] || run_benchmarks
  echo ""
  echo "=== Comparing HEAD against: $base_label ==="
  if [ "${BENCH_USE_BENCHSTAT:-0}" = "1" ] && command -v benchstat >/dev/null 2>&1; then
    benchstat "$base_file" "$CURRENT_FILE" | tee "$BENCHMARK_DIR/compare.txt"
  fi
  compare_files "$base_file" "$CURRENT_FILE" | tee "$BENCHMARK_DIR/compare.txt"
  # tee masks the awk exit code; re-run silently for the verdict.
  compare_files "$base_file" "$CURRENT_FILE" >/dev/null
}

run_quick() {
  echo "Running quick benchmarks (one shot, tracked analysis subset)..."
  prepare_dataset
  go test -run '^$' -bench '^BenchmarkFullAnalysis_(Sparse100|Dense100|ManyCycles20)$' -benchmem -count=1 ./pkg/analysis 2>&1 | tee "$CURRENT_FILE"
}

run_latency() {
  local baseline_bv="${1:?baseline bv binary}" baseline_ui="${2:?baseline UI test binary}" out="${3:?new output directory}"
  if [ ! -x "$baseline_bv" ] || [ ! -x "$baseline_ui" ]; then
    echo "latency: both baseline binaries must be executable" >&2
    return 2
  fi
  case "$baseline_bv:$baseline_ui:$out" in
    /*:/*:/*) ;;
    *) echo "latency: use absolute paths for both binaries and the new output directory" >&2; return 2 ;;
  esac
  [ ! -e "$out" ] || { echo "latency: output directory already exists; choose a new retained destination" >&2; return 2; }
  mkdir -p "$out"
  {
    date -u '+utc=%Y-%m-%dT%H:%M:%SZ'
    uname -a
    go version
    git rev-parse HEAD
    git diff --stat -- '*.go' go.mod go.sum
    if command -v lscpu >/dev/null 2>&1; then lscpu; fi
  } > "$out/source-host.txt"
  git diff --binary -- '*.go' go.mod go.sum > "$out/source.patch"
  {
    sha256sum "$out/source.patch" "$baseline_bv" "$baseline_ui"
    while IFS= read -r file; do sha256sum "$file"; done < <(git ls-files --others --exclude-standard -- '*.go')
  } > "$out/source.sha256"
  go build -o "$out/current-bv" ./cmd/bv
  go test -c -o "$out/current-ui.test" ./pkg/ui
  go test -c -o "$out/current-e2e.test" ./tests/e2e
  sha256sum "$out/current-bv" "$out/current-ui.test" "$out/current-e2e.test" >> "$out/source.sha256"
  local result=0 round position side binary dir enforce
  # Four pairs give repeated, alternating order. Each UI cohort independently
  # settles the production model; all observed samples are retained.
  for round in 0 1 2 3; do
    for position in 0 1; do
      side=$(( (round + position) % 2 ))
      binary="$baseline_ui"
      if [ "$side" -eq 1 ]; then binary="$out/current-ui.test"; fi
      dir="$out/ui-round-$round-side-$side"
      mkdir "$dir"
      enforce=0
      if [ "$side" -eq 1 ]; then enforce="${BV_PERF_ENFORCE_SLO:-1}"; fi
      BV_PERF_DIR="$dir" BV_PERF_ENFORCE_SLO="$enforce" \
        "$binary" -test.run '^TestPerformanceNavigationCohorts$' -test.timeout 2h -test.v \
        > "$dir/run.log" 2>&1 || result=1
      cat "$dir/run.log"
    done
  done
  mkdir "$out/cli"
  # E2E TestMain resolves the source checkout relative to tests/e2e.
  (cd "$root/tests/e2e" && BV_PERF_DIR="$out/cli" BV_PERF_BASELINE_BINARY="$baseline_bv" \
    BV_PERF_CURRENT_BINARY="$out/current-bv" "$out/current-e2e.test" \
    -test.run '^TestPerformanceCLICohorts$' -test.timeout 4h -test.v) \
    > "$out/cli/run.log" 2>&1 || result=1
  cat "$out/cli/run.log"
  bash tests/artifacts/perf/verify.sh "$out" || result=1
  echo "Retained all latency evidence: $out"
  return "$result"
}

case "${1:-run}" in
  baseline) save_baseline ;;
  compare) compare_benchmarks ;;
  compare-files) compare_files "${2:?base file}" "${3:?current file}" ;;
  quick) run_quick ;;
  latency) run_latency "${2:?baseline bv}" "${3:?baseline UI test}" "${4:?output directory}" ;;
  run) run_benchmarks ;;
  *) echo "usage: $0 {baseline|compare|compare-files BASE CUR|run|quick|latency BASE_BV BASE_UI_TEST OUTPUT_DIR}" >&2; exit 2 ;;
esac
