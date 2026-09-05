#!/usr/bin/env bash
# benchmark_compare_test.sh: proves scripts/benchmark.sh compare-files (the
# comparison behind release-gate stage 8) turns red on a real regression and
# stays green on noise inside the threshold. Uses synthetic `go test -bench`
# output and synthetic cohort records so it does not depend on machine speed.
# Usage: tests/scripts/benchmark_compare_test.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-bench-test.XXXXXX")"
trap 'echo "Retained benchmark comparison fixtures: $tmp"' EXIT

# Three runs per benchmark, like a real -count=3 file, with a provenance header
# and the goos/pkg lines go test prints; the comparison must ignore all of them.
cat > "$tmp/base.txt" <<'EOF'
# bv benchmark baseline
# generated_at: 2026-09-02T00:00:00Z
goos: linux
goarch: amd64
pkg: github.com/Dicklesworthstone/beads_viewer/pkg/analysis
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1020000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100    990000 ns/op   500 B/op   10 allocs/op
BenchmarkSnapshotSwap-64            1000     50000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     52000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     49000 ns/op   100 B/op    2 allocs/op
PASS
EOF

# Within threshold: +10% on one benchmark, a little faster on the other.
cat > "$tmp/noise.txt" <<'EOF'
BenchmarkRealData_FullTriage-64      100   1100000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1090000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1110000 ns/op   500 B/op   10 allocs/op
BenchmarkSnapshotSwap-64            1000     48000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     47000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     49000 ns/op   100 B/op    2 allocs/op
EOF

# Regression: SnapshotSwap doubled in every run. The comparison takes the best
# observed time per side (contention only inflates samples), so a regression
# has to show in all samples, and a slow outlier on the current side must NOT
# by itself turn the comparison red (that is the noise the minimum absorbs).
cat > "$tmp/slow.txt" <<'EOF'
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
BenchmarkSnapshotSwap-64            1000    100000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     98000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000    105000 ns/op   100 B/op    2 allocs/op
EOF

# Contention: one inflated sample on the current side, the others at par.
cat > "$tmp/contended.txt" <<'EOF'
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   2600000 ns/op   500 B/op   10 allocs/op
BenchmarkRealData_FullTriage-64      100   1010000 ns/op   500 B/op   10 allocs/op
BenchmarkSnapshotSwap-64            1000     50000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000    140000 ns/op   100 B/op    2 allocs/op
BenchmarkSnapshotSwap-64            1000     49500 ns/op   100 B/op    2 allocs/op
EOF

# Missing: a tracked benchmark disappeared from the current run.
cat > "$tmp/missing.txt" <<'EOF'
BenchmarkRealData_FullTriage-64      100   1000000 ns/op   500 B/op   10 allocs/op
EOF

fail=0
check() {
  # check <name> <expected exit> <current file>
  local name="$1" want="$2" cur="$3" rc=0
  set +e
  out="$(BENCH_PCT=20 "$root/scripts/benchmark.sh" compare-files "$tmp/base.txt" "$cur" 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL: $name exited $rc, want $want"; echo "$out" | sed 's/^/    /'; fail=1
  else
    echo "ok: $name (exit $rc)"
  fi
}

check "identical files pass" 0 "$tmp/base.txt"
check "noise inside 20% passes" 0 "$tmp/noise.txt"
check "doubled in every run fails" 1 "$tmp/slow.txt"
check "one contended sample passes" 0 "$tmp/contended.txt"
check "missing benchmark fails" 1 "$tmp/missing.txt"

# The reported worst regression must come from the best observed samples
# (98000 vs 49000 = +100.0%), not from the slowest ones.
worst="$(BENCH_PCT=20 "$root/scripts/benchmark.sh" compare-files "$tmp/base.txt" "$tmp/slow.txt" 2>&1 | awk '/worst sec\/op regression/{print $4}' || true)"
case "$worst" in
  100.0%) echo "ok: worst regression reported as $worst" ;;
  *) echo "FAIL: worst regression reported as '$worst', want 100.0%"; fail=1 ;;
esac

# A lower threshold turns the noise file red, proving BENCH_PCT is honoured.
set +e
BENCH_PCT=5 "$root/scripts/benchmark.sh" compare-files "$tmp/base.txt" "$tmp/noise.txt" >"$tmp/low-threshold.log" 2>&1
rc=$?
set -e
if [ "$rc" -eq 1 ]; then echo "ok: BENCH_PCT=5 rejects +10%"; else echo "FAIL: BENCH_PCT=5 exited $rc, want 1"; fail=1; fi

# These are synthetic verifier fixtures, not measured performance cohorts.
# Exercise the actual comparator, including rejected metadata and denominators.
python3 - "$root" "$tmp" <<'PY' || fail=1
import copy
import hashlib
import json
import math
import pathlib
import subprocess
import sys

repo, temp = map(pathlib.Path, sys.argv[1:])
cohorts = temp / "latency-verifier"
cohorts.mkdir()
roles = {role: hashlib.sha256(role.encode()).hexdigest()
         for role in ("baseline_bv", "baseline_ui", "current_bv", "current_ui")}
manifest = "".join(f"# {role} {digest}\n{digest}  fixture-{role}\n"
                   for role, digest in roles.items())
(cohorts / "source.sha256").write_text(manifest)
metrics = {name: {"state": "computed"} for name in
           ("PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack")}
families = ("realistic", "deep-chain", "wide-dag", "cyclic-dense", "mostly-closed", "unicode")
for family in families:
    for size in (1000, 5000, 10000):
        common = {"workload": family, "issues": size, "loaded_issues": size,
                  "seed": 20260904, "host": "verifier-fixture-host",
                  "go_version": "go1.25.5", "goos": "linux", "goarch": "amd64", "gomaxprocs": 8,
                  "sample_ns": [1000000] * 200,
                  "distribution": {"samples": 200, "p50_ms": 1, "p95_ms": 1, "p99_ms": 1, "max_ms": 1}}
        for mode in ("navigation", "refresh"):
            for round_no in range(4):
                for side in (0, 1):
                    record = copy.deepcopy(common)
                    record.update({"mode": mode,
                        "binary_sha256": roles["baseline_ui" if side == 0 else "current_ui"],
                        "analysis_config_hash": hashlib.sha256(f"config:{family}:{size}".encode()).hexdigest(),
                        "fixture_sha256": hashlib.sha256(f"ui:{family}:{size}".encode()).hexdigest(),
                        "metric_status": metrics, "priority_recommendations": [],
                        "priority_reference_time": "2026-09-01T00:00:00Z",
                        "terminal_columns": 140, "terminal_rows": 45,
                        "selected_ids": [f"issue-{i}" for i in range(200)],
                        "list_ids": [f"issue-{i}" for i in range(size)],
                        "snapshot_swap_ns": [], "refresh_metric_status": []})
                    for key in ("settled_setup_ns", "allocated_bytes", "allocations", "heap_before_bytes", "heap_after_bytes", "gc_cycles", "gc_pause_ns"):
                        record[key] = 1
                    if mode == "refresh":
                        record.update({"snapshot_swap_ns": [1000000], "snapshot_build_ns": [1000000],
                            "phase2_command_ns": [1000000], "phase2_handler_ns": [1000000],
                            "refresh_order_sha256": ["a" * 64], "refresh_decisions_sha256": ["b" * 64],
                            "refresh_generations": [1], "refresh_metric_status": [metrics]})
                    path = cohorts / f"ui-round-{round_no}-side-{side}" / f"ui-{family}-{size}-{mode}.json"
                    path.parent.mkdir(exist_ok=True)
                    path.write_text(json.dumps(record))
        for mode in ("cold-application-cache", "warm-application-cache"):
            for side in (0, 1):
                record = copy.deepcopy(common)
                record.update({"mode": mode, "side": side,
                    "binary_sha256": roles["baseline_bv" if side == 0 else "current_bv"],
                    "fixture_sha256": hashlib.sha256(f"cli:{family}:{size}".encode()).hexdigest(),
                    "decision_behavior": {"status": metrics}, "parity_mismatches": []})
                path = cohorts / "cli" / f"cli-{family}-{size}-{mode}-{side}" / "result.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps(record))
            full = {"generated_at": "2026-09-01T00:00:00Z",
                    "triage": {"meta": {"generated_at": "2026-09-01T00:00:00Z", "issue_count": size},
                               "status": metrics, "recommendations": [{"id": "issue-1", "score": 0.125}]}}
            exact = {key: value for key, value in common.items()
                     if key not in ("sample_ns", "distribution", "go_version", "goos", "goarch")}
            exact.update({"mode": mode, "reference_epoch": "1788220800",
                "fixture_sha256": hashlib.sha256(f"cli:{family}:{size}".encode()).hexdigest(),
                "binaries": [{"binary_sha256": roles[role], "go_version": "go1.25.5", "goos": "linux", "goarch": "amd64"}
                             for role in ("baseline_bv", "current_bv")],
                "outputs": [[full, full], [full, full]], "parity_mismatches": []})
            path = cohorts / "cli-exact" / f"exact-{family}-{size}-{mode}" / "result.json"
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps(exact))

failures = []
def check(name, expected, diagnostic=""):
    result = subprocess.run(["bash", str(repo / "tests/artifacts/perf/verify.sh"), str(cohorts)],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    (temp / f"latency-{name}.stdout.log").write_bytes(result.stdout)
    (temp / f"latency-{name}.stderr.log").write_bytes(result.stderr)
    if result.returncode != expected or diagnostic.encode() not in result.stderr:
        failures.append(name)
        print(f"FAIL {name}: exit {result.returncode}, expected {expected}; stderr={result.stderr.decode()}")
    else:
        print(f"ok: latency {name} (exit {result.returncode})")

check("complete-synthetic-contract", 0)
ui = cohorts / "ui-round-0-side-1/ui-realistic-1000-navigation.json"
cli = cohorts / "cli/cli-realistic-1000-cold-application-cache-1/result.json"
for field, value in (("workload", "unicode"), ("issues", 10000), ("mode", "refresh"), ("seed", 9),
                     ("loaded_issues", 2), ("fixture_sha256", "bad-hash"), ("host", "another-host"),
                     ("go_version", "go1.26.0"), ("goos", "darwin"), ("goarch", "arm64"),
                     ("gomaxprocs", 2), ("binary_sha256", "c" * 64), ("analysis_config_hash", "d" * 64),
                     ("terminal_columns", 80), ("terminal_rows", 24)):
    original = ui.read_bytes()
    changed = json.loads(original)
    changed[field] = value
    ui.write_text(json.dumps(changed))
    check(f"wrong-{field}", 1, field)
    ui.write_bytes(original)

original = cli.read_bytes()
changed = json.loads(original)
changed["sample_ns"].append(1000000)
changed["distribution"]["samples"] = 201
cli.write_text(json.dumps(changed))
check("unequal-cli-samples", 1, "sample count")
cli.write_bytes(original)
changed = json.loads(original)
changed["binary_sha256"] = roles["baseline_bv"]
cli.write_text(json.dumps(changed))
check("wrong-cli-binary", 1, "binary_sha256")
cli.write_bytes(original)
for field, value in (("workload", "unicode"), ("loaded_issues", 999), ("side", True),
                     ("gomaxprocs", True)):
    changed = json.loads(original)
    changed[field] = value
    cli.write_text(json.dumps(changed))
    check(f"wrong-cli-{field}", 1, field)
    cli.write_bytes(original)

# Change an entire otherwise-consistent pair, proving consistency is checked
# across cold/warm modes and repetitions as well as inside each pair.
paths = [cohorts / "cli" / f"cli-realistic-1000-warm-application-cache-{side}/result.json"
         for side in (0, 1)]
originals = [path.read_bytes() for path in paths]
for path, content in zip(paths, originals):
    changed = json.loads(content)
    changed["fixture_sha256"] = "f" * 64
    path.write_text(json.dumps(changed))
check("cross-mode-fixture", 1, "fixture_sha256")
for path, content in zip(paths, originals):
    path.write_bytes(content)

paths = [cohorts / "cli" / f"cli-unicode-1000-{mode}-{side}/result.json"
         for mode in ("cold-application-cache", "warm-application-cache") for side in (0, 1)]
originals = [path.read_bytes() for path in paths]
for path, content in zip(paths, originals):
    changed = json.loads(content)
    changed["fixture_sha256"] = hashlib.sha256(b"cli:realistic:1000").hexdigest()
    path.write_text(json.dumps(changed))
check("reused-workload-fixture", 1, "fixture_sha256 reused")
for path, content in zip(paths, originals):
    path.write_bytes(content)

exact = cohorts / "cli-exact/exact-realistic-1000-cold-application-cache/result.json"
original = exact.read_bytes()
for name in ("score-ulp", "full-metadata", "new-ms-field", "exact-clock", "exact-repeat-count", "exact-binary", "exact-loaded-count"):
    changed = json.loads(original)
    output = changed["outputs"][1][1]
    if name == "score-ulp":
        output["triage"]["recommendations"][0]["score"] = math.nextafter(0.125, math.inf)
    elif name == "full-metadata":
        output["triage"]["meta"]["new_contract_field"] = "must remain compared"
    elif name == "new-ms-field":
        output["future_result_ms"] = 17
    elif name == "exact-clock":
        for side in changed["outputs"]:
            for result in side:
                result["generated_at"] = "2001-01-01T00:00:00Z"
    elif name == "exact-repeat-count":
        changed["outputs"][1].pop()
    elif name == "exact-binary":
        changed["binaries"][1]["binary_sha256"] = roles["baseline_bv"]
    elif name == "exact-loaded-count":
        for side in changed["outputs"]:
            for result in side:
                result["triage"]["meta"]["issue_count"] = 999
    exact.write_text(json.dumps(changed))
    check(name, 1)
    exact.write_bytes(original)
exact.rename(exact.with_suffix(".retained"))
check("missing-exact-proof", 1, "cli-exact")
exact.with_suffix(".retained").rename(exact)

original = ui.read_bytes()
for name in ("slow", "skipped-status", "reordered-selection"):
    changed = json.loads(original)
    if name == "slow":
        changed["sample_ns"] = [60000000] * 200
        changed["distribution"] = {"samples": 200, "p50_ms": 60, "p95_ms": 60, "p99_ms": 60, "max_ms": 60}
    elif name == "skipped-status":
        changed["metric_status"]["PageRank"] = {"state": "skipped", "reason": "planted negative"}
    else:
        changed["selected_ids"][0], changed["selected_ids"][1] = changed["selected_ids"][1], changed["selected_ids"][0]
    ui.write_text(json.dumps(changed))
    check(name, 1)
    ui.write_bytes(original)

(cohorts / "source.sha256").rename(cohorts / "source.sha256.retained")
check("missing-source-manifest", 1, "source.sha256")
(cohorts / "source.sha256.retained").rename(cohorts / "source.sha256")
for name, text in (("duplicate-role", manifest + f"# baseline_ui {roles['baseline_ui']}\n"),
                   ("unbound-role", manifest.replace(f"{roles['baseline_ui']}  fixture-baseline_ui\n", ""))):
    (cohorts / "source.sha256").write_text(text)
    check(name, 1, "source.sha256")
    (cohorts / "source.sha256").write_text(manifest)
print("Synthetic comparator controls only; no latency or full-matrix capability is claimed.")
raise SystemExit(1 if failures else 0)
PY

if [ "$fail" -eq 0 ]; then
  echo "benchmark_compare_test: PASS"
fi
exit "$fail"
