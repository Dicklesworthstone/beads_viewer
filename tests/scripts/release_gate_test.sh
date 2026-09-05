#!/usr/bin/env bash
# Exercise the real gate against committed toy repositories and real Go builds.
# Fixture helper stages test marker files; they are not evidence that bv's
# actual benchmark, browser, installer, or native-platform suites have passed.
# Artifacts are retained for inspection; this harness never publishes/deletes.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d /tmp/bv-release-gate-test.XXXXXX)"
fixture="$tmp/source with spaces"
mkdir -p "$fixture" "$tmp/tools"
echo "release gate test artifacts: $tmp"

fail() { echo "FAIL: $*" >&2; exit 1; }
commit_fixture() {
  git -C "$1" add -A
  git -C "$1" -c user.name=gate-test -c user.email=gate-test@example.invalid commit -qm "$2"
}
clone_fixture() {
  local path="$tmp/$1"
  git clone --quiet --local --no-hardlinks "$fixture" "$path"
  printf '%s\n' "$path"
}
reject() {
  local name="$1" expected="$2"; shift 2
  local status=0
  "$@" >"$tmp/$name.log" 2>&1 || status=$?
  if [ "$status" -eq 0 ] || ! grep -q "$expected" "$tmp/$name.log"; then
    cat "$tmp/$name.log"
    fail "$name: exit=$status; expected nonzero and '$expected'"
  fi
  echo "ok: $name rejected (exit $status; $tmp/$name.log)"
}
run_gate() {
  local name="$1" tree="$2" expected="$3"; shift 3
  local status=0 output="$tmp/output-$name"
  mkdir -p "$output"
  env RELEASE_GATE_OUTPUT_DIR="$output" PWSH="$tmp/tools/pwsh-fixture" "$@" \
    bash "$tree/scripts/release_gate.sh" >"$tmp/gate-$name.log" 2>&1 || status=$?
  if [ "$status" -ne "$expected" ]; then
    cat "$tmp/gate-$name.log"
    fail "gate $name exited $status, expected $expected"
  fi
  if [ "$name" = skipped ] && grep -q 'RELEASE GATE PASSED' "$tmp/gate-$name.log"; then
    cat "$tmp/gate-$name.log"
    fail 'skipped checks were advertised as a passing release gate'
  fi
  gate_receipt="$(python3 - "$output" <<'PY'
import pathlib, sys
paths = list(pathlib.Path(sys.argv[1]).glob('*/receipt.json'))
assert len(paths) == 1, paths
print(paths[0])
PY
)"
  echo "ok: gate $name exit=$status receipt=$gate_receipt"
}

mkdir -p "$fixture/scripts" "$fixture/cmd/bv" "$fixture/pkg" "$fixture/internal" \
  "$fixture/tests/e2e" "$fixture/tests/scripts" "$fixture/tests/testdata/benchmark" "$fixture/benchmarks"
cp "${RELEASE_GATE_TEST_SCRIPT:-$root/scripts/release_gate.sh}" "$fixture/scripts/release_gate.sh"
cp "$root/.goreleaser.yaml" "$fixture/.goreleaser.yaml"
printf 'module example.invalid/gatefixture\n\ngo 1.25.5\n' >"$fixture/go.mod"
printf 'package fixture\n' >"$fixture/pkg/doc.go"
printf 'package internal\n' >"$fixture/internal/doc.go"
cat >"$fixture/cmd/bv/main.go" <<'GO'
package main

func main() { println("bv release-gate fixture") }
GO
cat >"$fixture/cmd/bv/main_test.go" <<'GO'
package main

import "testing"

func TestFixture(t *testing.T) {
	if got := 2 + 2; got != 4 {
		t.Fatalf("fixture arithmetic = %d", got)
	}
}
GO
cat >"$fixture/tests/e2e/gate_test.go" <<'GO'
package e2e

import "testing"

func TestFixtureE2E(t *testing.T) { t.Log("synthetic gate fixture, not bv E2E") }
GO
printf '{}\n' >"$fixture/tests/testdata/benchmark/medium.jsonl"
printf '# synthetic fixture baseline\n' >"$fixture/benchmarks/baseline.txt"
printf 'fixture\n' >"$fixture/fixture.marker"
printf 'Fixture release source.\n' >"$fixture/README.md"
printf 'cmd/bv/ignored.go\n' >"$fixture/.gitignore"
for script in scripts/check_action_pins.sh scripts/verify_vendor.sh scripts/benchmark.sh \
    scripts/robot_smoke.sh tests/scripts/benchmark_compare_test.sh \
    tests/scripts/release_gate_test.sh tests/scripts/install_ps1_test.sh; do
  cat >"$fixture/$script" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
test "$(cat fixture.marker)" = fixture
if [[ "$0" = *check_action_pins.sh ]] && [ "${FIXTURE_STAGE_FAIL:-0}" = 1 ]; then
  echo 'planted fixture stage failure' >&2
  exit 23
fi
echo "fixture helper: $0"
SH
  chmod +x "$fixture/$script"
done
printf '#!/usr/bin/env bash\nprintf "fixture pwsh runner\\n"\n' >"$tmp/tools/pwsh-fixture"
chmod +x "$tmp/tools/pwsh-fixture"
git -C "$fixture" init -q -b main
commit_fixture "$fixture" 'release gate fixture'
revision="$(git -C "$fixture" rev-parse HEAD)"
git -C "$fixture" remote add origin https://github.com/release-gate-fixture/bv
git -C "$fixture" tag v0.0.1
echo "fixture revision: $revision"

# This first control fails against the old gate: it printed PASSED after skips
# and did not emit an eligibility receipt.
run_gate skipped "$fixture" 2 RELEASE_GATE_SKIP='1 2 3 4 5 6 7 8 9 10'
if grep -q 'RELEASE GATE PASSED' "$tmp/gate-skipped.log"; then
  fail 'skipped checks were advertised as a passing release gate'
fi
reject skipped diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source

run_gate complete "$fixture" 0
receipt="$gate_receipt"
export RELEASE_GATE_RECEIPT="$receipt"
bash "$fixture/scripts/release_gate.sh" verify-source
python3 - "$receipt" "$revision" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
assert r['eligible'] is True
assert r['source']['revision'] == sys.argv[2]
assert len(r['stages']) == 10 and all(s['status'] == 'passed' for s in r['stages'])
assert 'go test -race -count=1' in r['stages'][2]['command']
assert r['dataset']['sha256'] and r['toolchain']['GOVERSION']
PY
reject unsealed 'no complete packaged archive evidence' bash "$fixture/scripts/release_gate.sh" verify
reject missing-receipt 'missing RELEASE_GATE_RECEIPT' env RELEASE_GATE_RECEIPT= bash "$fixture/scripts/release_gate.sh" verify-source

run_gate failed "$fixture" 1 FIXTURE_STAGE_FAIL=1
reject failed-stage diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source
run_gate no-pwsh "$fixture" 2 PWSH=/no/such/pwsh
reject missing-pwsh diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source

missing="$(clone_fixture missing-helper)"
chmod -x "$missing/scripts/verify_vendor.sh"
commit_fixture "$missing" 'fixture with unavailable vendor helper'
run_gate missing "$missing" 2 RELEASE_GATE_ALLOW_MISSING=1
reject missing-stage diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$missing/scripts/release_gate.sh" verify-source

dirty="$(clone_fixture dirty)"
printf 'post-gate edit\n' >>"$dirty/README.md"
reject dirty-source 'source is dirty' bash "$dirty/scripts/release_gate.sh" verify-source
run_gate dirty "$dirty" 2
reject dirty-run diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$dirty/scripts/release_gate.sh" verify-source
run_gate override "$fixture" 2 BENCH_COUNT=1
reject override-run diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source
untracked="$(clone_fixture untracked)"
printf 'package main\n' >"$untracked/cmd/bv/extra.go"
reject untracked-input 'source is dirty' bash "$untracked/scripts/release_gate.sh" verify-source
ignored="$(clone_fixture ignored)"
printf 'package main\n' >"$ignored/cmd/bv/ignored.go"
reject ignored-input 'undeclared build inputs' bash "$ignored/scripts/release_gate.sh" verify-source
changed="$(clone_fixture changed-revision)"
printf 'new committed source\n' >>"$changed/README.md"
commit_fixture "$changed" 'later source revision'
reject changed-source 'source changed after gate' bash "$changed/scripts/release_gate.sh" verify-source

# Tampered/incomplete receipts must fail even if eligible was left true.
python3 - "$receipt" "$tmp/missing-row.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1])); r['stages'].pop()
json.dump(r, open(sys.argv[2], 'w'))
PY
reject missing-row 'missing, skipped, or failed required stages' env RELEASE_GATE_RECEIPT="$tmp/missing-row.json" bash "$fixture/scripts/release_gate.sh" verify-source
python3 - "$receipt" "$tmp/wrong-toolchain.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1])); r['toolchain']['GOVERSION'] = 'go0.0.0'
json.dump(r, open(sys.argv[2], 'w'))
PY
reject changed-toolchain 'toolchain/build environment differs' env RELEASE_GATE_RECEIPT="$tmp/wrong-toolchain.json" bash "$fixture/scripts/release_gate.sh" verify-source

# The actual checked-in GoReleaser config must consume the receipt and finish
# packaging without network publication. Its post hooks inspect all binaries.
goreleaser_dist="$tmp/goreleaser-packages"
if ! RELEASE_GATE_DIST="$goreleaser_dist" bash "$fixture/scripts/release_gate.sh" package >"$tmp/goreleaser-package.log" 2>&1; then
  cat "$tmp/goreleaser-package.log"
  fail "GoReleaser package path failed"
fi
echo "ok: GoReleaser packaged and verified real binaries ($tmp/goreleaser-package.log)"
cp "$receipt" "$tmp/goreleaser-sealed-receipt.json"
reject reused-dist 'dist exists' env RELEASE_GATE_DIST="$goreleaser_dist" bash "$fixture/scripts/release_gate.sh" package
reject source-dist 'must be outside source' env RELEASE_GATE_DIST="$fixture/dist" bash "$fixture/scripts/release_gate.sh" package

dist="$tmp/packages"
mkdir -p "$dist" "$tmp/binaries"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os="${target%/*}" arch="${target#*/}"
  binary="$tmp/binaries/bv-$os-$arch"
  (cd "$fixture" && CGO_ENABLED=0 GOWORK=off GOOS="$os" GOARCH="$arch" go build -buildvcs=true -o "$binary" ./cmd/bv)
  bash "$fixture/scripts/release_gate.sh" verify-binary "$binary"
  python3 - "$binary" "$dist" "$os" "$arch" <<'PY'
import hashlib, pathlib, sys, tarfile, zipfile
binary, dist = map(pathlib.Path, sys.argv[1:3]); goos, arch = sys.argv[3:]
name = f'bv_fixture_{goos}_{arch}'
if goos == 'windows':
    archive = dist / (name + '.zip')
    with zipfile.ZipFile(archive, 'w') as z: z.write(binary, 'bv.exe')
else:
    archive = dist / (name + '.tar.gz')
    with tarfile.open(archive, 'w:gz') as t: t.add(binary, arcname='bv')
with open(dist / 'checksums.txt', 'a') as f:
    f.write(hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')
PY
done
bash "$fixture/scripts/release_gate.sh" seal "$dist"
bash "$fixture/scripts/release_gate.sh" verify

# Archive evidence stays independently reviewable after later negative controls.
cp "$receipt" "$tmp/receipt-before-tampering.json"

(cd "$dirty" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=true -o "$tmp/dirty-bv" ./cmd/bv)
reject dirty-binary 'dirty/unknown source' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/dirty-bv"
(cd "$changed" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=true -o "$tmp/wrong-revision-bv" ./cmd/bv)
reject wrong-binary 'binary revision mismatch' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/wrong-revision-bv"
(cd "$fixture" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=false -o "$tmp/no-build-info-bv" ./cmd/bv)
reject no-build-info 'binary revision mismatch' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/no-build-info-bv"

config_path="$(python3 - "$receipt" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))['packaging_config']['path'])
PY
)"
printf '# post-gate config edit\n' >>"$config_path"
reject changed-config 'packaging config changed' bash "$fixture/scripts/release_gate.sh" verify
# Preserve the changed file and restore a byte-identical copy of its original
# external config, so the independent archive-tamper control remains isolated.
mv "$config_path" "$tmp/changed-packaging-config.yaml"
python3 - "$tmp/changed-packaging-config.yaml" "$config_path" <<'PY'
import pathlib, sys
data = pathlib.Path(sys.argv[1]).read_bytes()
suffix = b'# post-gate config edit\n'
assert data.endswith(suffix)
pathlib.Path(sys.argv[2]).write_bytes(data[:-len(suffix)])
PY

printf 'tamper\n' >>"$dist/bv_fixture_linux_amd64.tar.gz"
reject changed-archive 'artifact changed' bash "$fixture/scripts/release_gate.sh" verify
reject changed-checksum 'archive checksum mismatch' bash "$fixture/scripts/release_gate.sh" seal "$dist"
echo "release_gate_test: PASS (synthetic stages; real Git trees and five Go cross-builds; no publication)"
