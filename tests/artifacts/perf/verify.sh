#!/usr/bin/env bash
# Verify retained output from scripts/benchmark.sh latency.
# Usage: bash tests/artifacts/perf/verify.sh OUTPUT_DIR
# No live tracker, stored timing baseline, or blanket JSON normalization is used.
set -euo pipefail
python3 - "${1:?latency output directory}" <<'PY'
import decimal
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
families = ("realistic", "deep-chain", "wide-dag", "cyclic-dense", "mostly-closed", "unicode")
sizes = (1000, 5000, 10000)
errors = []
metrics = {"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"}
digest_pattern = re.compile(r"[0-9a-f]{64}")
binary_roles = {}
manifest_hashes = set()
try:
    for line in (root / "source.sha256").read_text().splitlines():
        fields = line.split()
        if len(fields) == 3 and fields[0] == "#" and fields[1] in ("baseline_bv", "baseline_ui", "current_bv", "current_ui"):
            if fields[1] in binary_roles or not digest_pattern.fullmatch(fields[2]):
                errors.append(f"source.sha256: invalid or duplicate binary role {fields[1]}")
            binary_roles[fields[1]] = fields[2]
        elif len(fields) >= 2 and digest_pattern.fullmatch(fields[0]):
            manifest_hashes.add(fields[0])
    for role in ("baseline_bv", "baseline_ui", "current_bv", "current_ui"):
        if binary_roles.get(role) not in manifest_hashes:
            errors.append(f"source.sha256: missing bound executable hash for {role}")
except OSError as exc:
    errors.append(f"source.sha256: {exc}")

expected_runtime = None
fixture_hashes = {}
fixture_owners = {}
config_hashes = {}

def identity(record, path, kind, size, mode, side, surface):
    global expected_runtime
    for field, expected in (("workload", kind), ("issues", size), ("loaded_issues", size), ("mode", mode), ("seed", 20260904)):
        value = record.get(field)
        if type(value) is not type(expected) or value != expected:
            errors.append(f"{path}: {field}={value!r}, expected {expected!r}")
    if surface == "cli" and (type(record.get("side")) is not int or record["side"] != side):
        errors.append(f"{path}: wrong CLI side")
    fixture = record.get("fixture_sha256")
    if not isinstance(fixture, str) or not digest_pattern.fullmatch(fixture):
        errors.append(f"{path}: invalid fixture_sha256")
    else:
        key = (surface, kind, size)
        if fixture_hashes.setdefault(key, fixture) != fixture:
            errors.append(f"{path}: fixture_sha256 differs across modes or repetitions")
        if fixture_owners.setdefault((surface, fixture), key) != key:
            errors.append(f"{path}: fixture_sha256 reused for another workload or size")
    role = ("baseline_" if side == 0 else "current_") + ("bv" if surface == "cli" else "ui")
    binary = record.get("binary_sha256")
    if not isinstance(binary, str) or not digest_pattern.fullmatch(binary) or binary != binary_roles.get(role):
        errors.append(f"{path}: binary_sha256 does not match source.sha256 role {role}")
    if surface == "ui":
        for field, expected in (("terminal_columns", 140), ("terminal_rows", 45)):
            if type(record.get(field)) is not int or record[field] != expected:
                errors.append(f"{path}: {field} differs from the measured terminal size {expected}")
        config = record.get("analysis_config_hash")
        if not isinstance(config, str) or not digest_pattern.fullmatch(config):
            errors.append(f"{path}: invalid analysis_config_hash")
        elif config_hashes.setdefault((kind, size), config) != config:
            errors.append(f"{path}: analysis_config_hash differs across modes or repetitions")
    runtime = {key: record.get(key) for key in ("host", "go_version", "goos", "goarch", "gomaxprocs")}
    for field, value in runtime.items():
        if field == "gomaxprocs":
            valid = type(value) is int and value > 0
        else:
            valid = isinstance(value, str) and bool(value.strip())
        if not valid:
            errors.append(f"{path}: missing or invalid {field}")
    if expected_runtime is None:
        expected_runtime = runtime
    else:
        for field, value in runtime.items():
            if value != expected_runtime[field]:
                errors.append(f"{path}: {field} differs from the measurement host/toolchain configuration")

def read(path):
    try:
        record = json.loads(path.read_text())
        values = record.get("sample_ns", [])
        if len(values) < 200 or any(type(n) is not int or n < 0 for n in values):
            errors.append(f"{path}: invalid cohort (minimum 200 samples)")
            return None
        ordered = sorted(values)
        report = record.get("distribution", {})
        if report.get("samples") != len(values):
            errors.append(f"{path}: sample count mismatch")
        for p in (50, 95, 99):
            observed = ordered[(p * len(ordered) + 99) // 100 - 1] / 1e6
            if abs(report.get(f"p{p}_ms", -1) - observed) > 1e-9:
                errors.append(f"{path}: incorrect p{p}")
        return record
    except (OSError, ValueError) as exc:
        errors.append(f"{path}: {exc}")
        return None

def states(status, path):
    if not isinstance(status, dict) or not metrics.issubset(status):
        errors.append(f"{path}: missing metric states")
        return
    for name in metrics:
        if status[name].get("state") not in ("computed", "approx", "timeout", "skipped"):
            errors.append(f"{path}: invalid {name} state")
    degraded = [f"{n}={status[n].get('state')}" for n in sorted(metrics) if status[n].get("state") != "computed"]
    if degraded:
        print(f"DEGRADED {path.name}: {', '.join(degraded)}")

for kind in families:
    for size in sizes:
        for mode in ("navigation", "refresh"):
            expected = None
            expected_refresh_orders = None
            expected_refresh_decisions = {}
            refresh_generation_sides = {}
            for round_no in range(4):
                for side in (0, 1):
                    path = root / f"ui-round-{round_no}-side-{side}" / f"ui-{kind}-{size}-{mode}.json"
                    record = read(path)
                    if record is None:
                        continue
                    identity(record, path, kind, size, mode, side, "ui")
                    states(record.get("metric_status"), path)
                    selected = record.get("selected_ids", [])
                    if len(record.get("list_ids", [])) < 2:
                        errors.append(f"{path}: missing full list order")
                    if not isinstance(record.get("priority_recommendations"), list) or record.get("priority_reference_time") != "2026-09-01T00:00:00Z":
                        errors.append(f"{path}: missing priority recommendations or fixed scoring clock")
                    for key in ("settled_setup_ns", "allocated_bytes", "allocations", "heap_before_bytes", "heap_after_bytes", "gc_cycles", "gc_pause_ns"):
                        if not isinstance(record.get(key), int) or record[key] < 0:
                            errors.append(f"{path}: missing or invalid {key}")
                    if len(selected) != len(record["sample_ns"]) or any(a == b for a, b in zip(selected, selected[1:])):
                        errors.append(f"{path}: selection did not change every sample")
                    parity = {key: record.get(key) for key in ("fixture_sha256", "selected_ids", "metric_status", "list_ids", "priority_recommendations")}
                    if expected is None:
                        expected = parity
                    elif expected != parity:
                        errors.append(f"{path}: baseline/current ID/order/metric-state mismatch")
                    if mode == "refresh" and not record.get("snapshot_swap_ns"):
                        errors.append(f"{path}: no real snapshot delivery")
                    if mode == "refresh":
                        delivery_count = len(record.get("snapshot_swap_ns", []))
                        for key in ("snapshot_build_ns", "phase2_command_ns"):
                            values = record.get(key)
                            if not isinstance(values, list) or len(values) != delivery_count or any(not isinstance(ns, int) or ns < 0 for ns in values):
                                errors.append(f"{path}: missing or invalid actual background {key}")
                        phase2_handlers = record.get("phase2_handler_ns")
                        if not isinstance(phase2_handlers, list) or len(phase2_handlers) != len(record.get("snapshot_swap_ns", [])):
                            errors.append(f"{path}: a delivered snapshot is missing its real Phase2Ready handler")
                        elif any(not isinstance(ns, int) or ns < 0 for ns in phase2_handlers):
                            errors.append(f"{path}: invalid Phase2Ready timing")
                        elif side == 1 and any(ns > 50e6 for ns in phase2_handlers):
                            errors.append(f"{path}: Phase2Ready handler exceeded 50ms event-loop bound")
                        orders = set(record.get("refresh_order_sha256", []))
                        if not orders or len(record.get("refresh_order_sha256", [])) != len(record.get("snapshot_swap_ns", [])):
                            errors.append(f"{path}: missing order for a delivered snapshot")
                        elif expected_refresh_orders is None:
                            expected_refresh_orders = orders
                        elif orders != expected_refresh_orders:
                            errors.append(f"{path}: refresh changed baseline/current list order")
                        decisions = record.get("refresh_decisions_sha256", [])
                        if len(decisions) != delivery_count or any(not isinstance(digest, str) or len(digest) != 64 for digest in decisions):
                            errors.append(f"{path}: missing completed-Phase2 priority/score/unblocks identity")
                        generations = record.get("refresh_generations", [])
                        if generations != list(range(1, delivery_count + 1)):
                            errors.append(f"{path}: delivered generations are missing, duplicated, or out of order")
                        else:
                            for generation, digest in zip(generations, decisions):
                                expected_digest = expected_refresh_decisions.setdefault(generation, digest)
                                if digest != expected_digest:
                                    errors.append(f"{path}: generation{generation} changed exact priority/score/unblocks results")
                                refresh_generation_sides.setdefault(generation, set()).add(side)
                        if len(record.get("refresh_metric_status", [])) != delivery_count:
                            errors.append(f"{path}: missing metric status after a completed Phase2 install")
                        if side == 1 and any(ns > 50e6 for ns in record.get("snapshot_swap_ns", [])):
                            errors.append(f"{path}: snapshot delivery exceeded 50ms event-loop bound")
                    for status in record.get("refresh_metric_status") or []:
                        states(status, path)
                        if status != record.get("metric_status"):
                            errors.append(f"{path}: refresh changed metric availability; speed comparison is inconclusive")
                    if record["distribution"].get("p99_ms", float("inf")) > 50:
                        if side == 1:
                            errors.append(f"{path}: p99 exceeds 50ms reference-host interaction SLO")
                        else:
                            print(f"BASELINE MISS {path}: p99={record['distribution']['p99_ms']:.3f}ms")
            for generation, sides in refresh_generation_sides.items():
                if sides != {0, 1}:
                    print(f"UNPAIRED {kind}/{size}/{mode} generation{generation}: no baseline/current score comparison for this additional completion")
        for mode in ("cold-application-cache", "warm-application-cache"):
            pair = []
            for side in (0, 1):
                path = root / "cli" / f"cli-{kind}-{size}-{mode}-{side}" / "result.json"
                record = read(path)
                if record is None:
                    continue
                identity(record, path, kind, size, mode, side, "cli")
                states(record.get("decision_behavior", {}).get("status"), path)
                if record.get("parity_mismatches"):
                    errors.append(f"{path}: decision/metric-state parity failed")
                pair.append(record)
            if len(pair) == 2:
                if len(pair[0]["sample_ns"]) != len(pair[1]["sample_ns"]):
                    errors.append(f"{kind}/{size}/{mode}: baseline/current sample count mismatch")
                for key in ("fixture_sha256", "decision_behavior"):
                    if pair[0].get(key) != pair[1].get(key):
                        errors.append(f"{kind}/{size}/{mode}: baseline/current {key} mismatch")
            path = root / "cli-exact" / f"exact-{kind}-{size}-{mode}" / "result.json"
            try:
                exact = json.loads(path.read_text(), parse_float=decimal.Decimal)
            except (OSError, ValueError) as exc:
                errors.append(f"{path}: {exc}")
                continue
            if exact.get("reference_epoch") != "1788220800":
                errors.append(f"{path}: wrong exact-result reference_epoch")
            if exact.get("parity_mismatches"):
                errors.append(f"{path}: complete fixed-clock result mismatch")
            binaries = exact.get("binaries", [])
            outputs = exact.get("outputs", [])
            if len(binaries) != 2 or len(outputs) != 2 or any(len(side) != 2 for side in outputs):
                errors.append(f"{path}: exact result requires two repeats from each of two binaries")
                continue
            expected_exact = None
            for side in (0, 1):
                record = dict(exact, **binaries[side], side=side)
                identity(record, path, kind, size, mode, side, "cli")
                for output in outputs[side]:
                    triage = output.get("triage", {})
                    meta = triage.get("meta", {})
                    status = triage.get("status", {})
                    states(status, path)
                    if output.get("generated_at") != "2026-09-01T00:00:00Z" or meta.get("generated_at") != "2026-09-01T00:00:00Z":
                        errors.append(f"{path}: exact result did not preserve generated_at")
                    if type(meta.get("issue_count")) is not int or meta["issue_count"] != size:
                        errors.append(f"{path}: exact result has wrong loaded issue_count")
                    if "compute_time_ms" in meta or any("ms" in status.get(name, {}) for name in metrics):
                        errors.append(f"{path}: exact result retains excluded duration fields")
                    if expected_exact is None:
                        expected_exact = output
                    elif output != expected_exact:
                        errors.append(f"{path}: complete fixed-clock result mismatch")
if errors:
    for error in errors:
        print(f"FAIL {error}", file=sys.stderr)
    raise SystemExit(1)
print("PASS: complete timed cohorts and separate fixed-clock full CLI results agree; current UI empirical p99 <=50ms.")
print("These descriptive cohorts do not establish terminal paint, population tail bounds, or search relevance.")
PY
