#!/usr/bin/env bash
# Verify retained output from scripts/benchmark.sh latency.
# Usage: bash tests/artifacts/perf/verify.sh OUTPUT_DIR
# No live tracker, stored timing baseline, or blanket JSON normalization is used.
set -euo pipefail
python3 - "${1:?latency output directory}" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
families = ("realistic", "deep-chain", "wide-dag", "cyclic-dense", "mostly-closed", "unicode")
sizes = (1000, 5000, 10000)
errors = []
metrics = {"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"}

def read(path):
    try:
        record = json.loads(path.read_text())
        values = record.get("sample_ns", [])
        if len(values) < 200 or any(not isinstance(n, int) or n < 0 for n in values):
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
            expected_refresh_decisions = None
            for round_no in range(4):
                for side in (0, 1):
                    path = root / f"ui-round-{round_no}-side-{side}" / f"ui-{kind}-{size}-{mode}.json"
                    record = read(path)
                    if record is None:
                        continue
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
                        elif expected_refresh_decisions is None:
                            expected_refresh_decisions = set(decisions)
                        elif set(decisions) != expected_refresh_decisions:
                            errors.append(f"{path}: completed Phase2 changed exact priority/score/unblocks results")
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
        for mode in ("cold-application-cache", "warm-application-cache"):
            pair = []
            for side in (0, 1):
                path = root / "cli" / f"cli-{kind}-{size}-{mode}-{side}" / "result.json"
                record = read(path)
                if record is None:
                    continue
                states(record.get("decision_behavior", {}).get("status"), path)
                if record.get("parity_mismatches"):
                    errors.append(f"{path}: decision/metric-state parity failed")
                pair.append(record)
            if len(pair) == 2:
                for key in ("fixture_sha256", "decision_behavior"):
                    if pair[0].get(key) != pair[1].get(key):
                        errors.append(f"{kind}/{size}/{mode}: baseline/current {key} mismatch")
if errors:
    for error in errors:
        print(f"FAIL {error}", file=sys.stderr)
    raise SystemExit(1)
print("PASS: complete cohorts, ordered decision IDs and metric states agree; current UI empirical p99 <=50ms.")
print("These descriptive cohorts do not establish terminal paint, population tail bounds, or search relevance.")
PY
