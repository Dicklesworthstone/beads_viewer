#!/usr/bin/env bash
# verify_vendor_test.sh: verifies vendor manifest checking and TMPDIR hygiene
# across scripts/verify_vendor.sh and scripts/build_graph_wasm.sh (bv-jgz0).
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
test_ws="$(mktemp -d "${TMPDIR:-/tmp}/bv-verify-vendor-test.XXXXXX")"
cleanup_ws() {
  local rc=$?
  if [ -n "${test_ws:-}" ] && [ -d "$test_ws" ]; then
    rm -rf "$test_ws"
  fi
  exit "$rc"
}
trap cleanup_ws EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

snapshot_tmp_entries() {
  local dir="$1"
  python3 - "$dir" <<'PY'
import pathlib, sys
base = pathlib.Path(sys.argv[1])
if base.exists():
    for entry in sorted(base.iterdir()):
        if entry.name.startswith(("bv-graph-wasm.", "bv-verify-vendor.")):
            print(entry.name)
PY
}

# 1. Real repository manifest check passes with custom TMPDIR and zero temp leaks.
custom_tmp_1="$test_ws/tmp-basic"
mkdir -p "$custom_tmp_1"
before_sys_tmp="$(snapshot_tmp_entries /tmp)"
out_basic="$(TMPDIR="$custom_tmp_1" bash "$root/scripts/verify_vendor.sh" 2>&1)" || fail "verify_vendor.sh failed: $out_basic"
[[ "$out_basic" == *"0 problem(s)"* ]] || fail "unexpected verify_vendor.sh output: $out_basic"
[ -z "$(snapshot_tmp_entries "$custom_tmp_1")" ] || fail "verify_vendor.sh leaked temp dir in custom TMPDIR"
after_sys_tmp="$(snapshot_tmp_entries /tmp)"
[ "$before_sys_tmp" = "$after_sys_tmp" ] || fail "verify_vendor.sh created entries in /tmp"
echo "ok: verify_vendor.sh passes and leaves zero temp dirs"

# 2. build_graph_wasm.sh uses ${TMPDIR:-/tmp}/bv-graph-wasm.XXXXXX rather than hardcoded /tmp.
if grep -Fq '/tmp/bv-graph-wasm.' "$root/scripts/build_graph_wasm.sh"; then
  fail "scripts/build_graph_wasm.sh still contains hardcoded /tmp/bv-graph-wasm.*"
fi
# shellcheck disable=SC2016
grep -Fq '"${TMPDIR:-/tmp}/bv-graph-wasm.XXXXXX"' "$root/scripts/build_graph_wasm.sh" || \
  fail "scripts/build_graph_wasm.sh missing TMPDIR-aware mktemp template"
echo "ok: build_graph_wasm.sh honors TMPDIR template"

# 3. Real verify_vendor.sh --source honors custom TMPDIR and cleans up on exit
#    regardless of whether optional WASM build prerequisites are installed.
custom_tmp_2="$test_ws/tmp-real-source"
mkdir -p "$custom_tmp_2"
before_sys_tmp="$(snapshot_tmp_entries /tmp)"
rc_real=0
TMPDIR="$custom_tmp_2" bash "$root/scripts/verify_vendor.sh" --source >"$test_ws/real-source.log" 2>&1 || rc_real=$?
case "$rc_real" in
  0|1|2) ;;
  *) fail "unexpected verify_vendor.sh --source exit code: $rc_real" ;;
esac
[ -z "$(snapshot_tmp_entries "$custom_tmp_2")" ] || fail "verify_vendor.sh --source leaked temp dir in custom TMPDIR"
after_sys_tmp="$(snapshot_tmp_entries /tmp)"
[ "$before_sys_tmp" = "$after_sys_tmp" ] || fail "verify_vendor.sh --source created entries in /tmp"
echo "ok: real verify_vendor.sh --source (exit $rc_real) left zero temp dirs in TMPDIR or /tmp"

# 4. Isolated fixture proves verify_vendor.sh --source passes explicit --out-dir
#    under TMPDIR/bv-verify-vendor.XXXXXX and removes it on exit 0, 1, and 2.
#    (Synthetic build observer tests TMPDIR/trap mechanics only, not WASM provenance.)
fixture="$test_ws/fixture"
mkdir -p "$fixture/scripts" "$fixture/pkg/export/viewer_assets/vendor"
cp "$root/scripts/verify_vendor.sh" "$fixture/scripts/verify_vendor.sh"
cp "$root/pkg/export/viewer_assets/vendor/"* "$fixture/pkg/export/viewer_assets/vendor/"
cat >"$fixture/scripts/build_graph_wasm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = "--out-dir" ] || { echo "expected --out-dir, got $*" >&2; exit 91; }
[ -n "${2:-}" ] || { echo "missing --out-dir value" >&2; exit 92; }
out_dir="$2"
[ ! -e "$out_dir" ] || { echo "out_dir already existed: $out_dir" >&2; exit 93; }
mkdir -p "$out_dir"
printf 'synthetic-observer\n' >"$out_dir/marker.txt"
printf '%s\n' "$out_dir" >"$OBSERVER_RECORD_FILE"
exit "${OBSERVER_EXIT_CODE:-0}"
SH
chmod +x "$fixture/scripts/build_graph_wasm.sh"

for want_rc in 0 1 2; do
  iter_tmp="$test_ws/tmp-fixture-$want_rc"
  record_file="$test_ws/observed-outdir-$want_rc.txt"
  mkdir -p "$iter_tmp"
  before_sys_tmp="$(snapshot_tmp_entries /tmp)"
  got_rc=0
  (
    cd "$fixture" || exit 1
    TMPDIR="$iter_tmp" OBSERVER_EXIT_CODE="$want_rc" OBSERVER_RECORD_FILE="$record_file" \
      bash scripts/verify_vendor.sh --source >"$test_ws/fixture-$want_rc.log" 2>&1
  ) || got_rc=$?
  [ "$got_rc" -eq "$want_rc" ] || fail "fixture --source exit $got_rc != expected $want_rc"
  observed_out="$(cat "$record_file")"
  case "$observed_out" in
    "$iter_tmp"/bv-verify-vendor.*/graph-wasm) ;;
    *) fail "unexpected --out-dir passed to build_graph_wasm.sh: $observed_out" ;;
  esac
  [ ! -e "$observed_out" ] || fail "--out-dir still exists after exit $want_rc: $observed_out"
  [ -z "$(snapshot_tmp_entries "$iter_tmp")" ] || fail "bv-verify-vendor.* leaked in $iter_tmp on exit $want_rc"
  after_sys_tmp="$(snapshot_tmp_entries /tmp)"
  [ "$before_sys_tmp" = "$after_sys_tmp" ] || fail "fixture --source touched /tmp on exit $want_rc"
done
echo "ok: verify_vendor.sh --source passes TMPDIR-scoped --out-dir and cleans up on exit 0, 1, and 2"

# 5. Negative control: tampered vendor asset fails verify_vendor.sh with exit 1.
tampered="$test_ws/tampered-vendor"
mkdir -p "$tampered"
cp "$root/pkg/export/viewer_assets/vendor/"* "$tampered/"
printf 'X' | dd of="$tampered/bv_graph.js" bs=1 seek=0 count=1 conv=notrunc status=none
tamper_rc=0
tamper_out="$(TMPDIR="$custom_tmp_1" bash "$root/scripts/verify_vendor.sh" "$tampered" 2>&1)" || tamper_rc=$?
[ "$tamper_rc" -eq 1 ] || fail "tampered vendor expected exit 1, got $tamper_rc"
[[ "$tamper_out" == *"MISMATCH:"* ]] || fail "tampered vendor missing MISMATCH diagnostic: $tamper_out"
echo "ok: tampered vendor copy rejected with MISMATCH (exit 1)"

echo "verify_vendor_test: PASS"
