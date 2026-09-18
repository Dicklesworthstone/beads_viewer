#!/usr/bin/env bash
# bv-apal.1: summarise a completed scripts/benchmark.sh latency output dir into
# the counts the bead's acceptance line names (288 UI records, 72 timed CLI
# records / 14400 samples, 36 exact records / 144 outputs) plus SLO and parity
# outcomes. Read-only. Literal paths.
#
# UI records carry no "side" field; the side is in the parent directory name
# ui-round-<r>-side-<s>. run_latency enforces the SLO only on side 1.
set -uo pipefail
OUT=/data/projects/beads_viewer/benchmarks/bb-p1-matrix-20260918
LOG=/data/projects/beads_viewer/benchmarks/bb-p1-matrix-20260918.log

python3 - "$OUT" <<'PY'
import json, pathlib, re, sys
root = pathlib.Path(sys.argv[1])

def load(p):
    try:
        return json.loads(p.read_text())
    except Exception as exc:
        print(f"UNREADABLE {p}: {exc}")
        return None

ui = sorted(root.glob("ui-round-*/*.json"))
cli = sorted(root.glob("cli/cli-*/result.json"))
exact = sorted(root.glob("cli-exact/exact-*/result.json"))

print(f"UI records:    {len(ui)}   (acceptance: 288)")
print(f"CLI records:   {len(cli)}  (acceptance: 72)")
print(f"EXACT records: {len(exact)} (acceptance: 36)")

side_re = re.compile(r"ui-round-(\d+)-side-(\d+)")
worst = {0: 0.0, 1: 0.0}
counts = {0: 0, 1: 0}
breaches = []
ui_samples = 0
binaries = {0: set(), 1: set()}
for p in ui:
    m = side_re.search(p.parent.name)
    r = load(p)
    if not (m and r):
        continue
    side = int(m.group(2))
    counts[side] = counts.get(side, 0) + 1
    if r.get("binary_sha256"):
        binaries[side].add(r["binary_sha256"])
    dist = r.get("distribution") or {}
    ui_samples += int(dist.get("samples") or 0)
    p99 = dist.get("p99_ms")
    if isinstance(p99, (int, float)):
        worst[side] = max(worst[side], p99)
        if side == 1 and p99 > 50:
            breaches.append((p.parent.name + "/" + p.name, p99))

print(f"UI per side:   baseline(side0)={counts[0]}  current(side1)={counts[1]}   total samples={ui_samples}")
print(f"UI max p99:    baseline={worst[0]:.3f} ms   current={worst[1]:.3f} ms   (50 ms SLO, enforced on current only)")
for side in (0, 1):
    if len(binaries[side]) > 1:
        print(f"  WARNING side {side} mixes {len(binaries[side])} distinct binaries")
if breaches:
    print(f"UI CURRENT-SIDE SLO BREACHES: {len(breaches)}")
    for name, v in breaches[:8]:
        print(f"   {name} p99={v:.3f}")
else:
    print("UI current-side SLO breaches: 0")

samples = 0
cli_mismatch = 0
cli_max_p99 = 0.0
for p in cli:
    r = load(p)
    if not r:
        continue
    samples += len(r.get("sample_ns") or [])
    if r.get("parity_mismatches"):
        cli_mismatch += 1
    dist = r.get("distribution") or {}
    v = dist.get("p99_ms")
    if isinstance(v, (int, float)):
        cli_max_p99 = max(cli_max_p99, v)
print(f"CLI samples:   {samples} (acceptance: 14400)   parity-mismatched records: {cli_mismatch}   max p99={cli_max_p99:.3f} ms")

outputs = 0
exact_mismatch = 0
for p in exact:
    r = load(p)
    if not r:
        continue
    for side in (r.get("outputs") or []):
        outputs += len(side)
    if r.get("parity_mismatches"):
        exact_mismatch += 1
print(f"EXACT outputs: {outputs} (acceptance: 144)   parity-mismatched records: {exact_mismatch}")
PY

echo "--- verifier verdict ---"
grep -aE '^(PASS: complete timed cohorts|FAIL |BASELINE MISS|UNPAIRED)' "$LOG" | tail -20 || echo "(verifier has not run yet)"
echo "--- harness exit ---"
grep -aE '^HARNESS_RC=' "$LOG" || echo "(run still in progress)"
