#!/usr/bin/env bash
# Exercise the real gate against committed toy repositories and real Go builds.
# Fixture helper stages test marker files; they are not evidence that bv's
# actual benchmark, browser, installer, or native-platform suites have passed.
# Artifacts are retained for inspection; this harness never publishes/deletes.
# `audit-environment` runs a focused, offline subprocess-boundary control with
# an explicitly synthetic scanner observer; `gate-mechanics` uses the observer
# for all receipt/package controls. The default mode uses real govulncheck.
set -euo pipefail
export GOTOOLCHAIN=local
scope="${1:-all}"
case "$scope" in
  all|audit-environment|gate-mechanics) ;;
  *) echo "usage: $0 [all|audit-environment|gate-mechanics]" >&2; exit 2 ;;
esac

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
fixture_go="$(command -v go)"
toolchain="$(go env GOVERSION)"
net_version="$(awk '$1 == "golang.org/x/net" { print $2 }' "$root/go.mod")"
printf 'module example.invalid/gatefixture\n\ngo %s\n\nrequire golang.org/x/net %s\n' \
  "${toolchain#go}" "$net_version" >"$fixture/go.mod"
# The fixture links a real vendored security dependency so the binary metadata
# controls cannot accidentally pass while inspecting only a dependency-free Go
# program. These copies are synthetic fixture inputs, not release artifacts.
mkdir -p "$fixture/vendor/golang.org/x/net"
cp -R "$root/vendor/golang.org/x/net/html" "$fixture/vendor/golang.org/x/net/"
printf '# golang.org/x/net %s\n## explicit; go 1.26.0\ngolang.org/x/net/html\ngolang.org/x/net/html/atom\n' \
  "$net_version" >"$fixture/vendor/modules.txt"
printf 'package fixture\n' >"$fixture/pkg/doc.go"
printf 'package internal\n' >"$fixture/internal/doc.go"
cat >"$fixture/cmd/bv/main.go" <<'GO'
package main

import (
    "strings"

    "golang.org/x/net/html"
)

func main() {
    node, err := html.Parse(strings.NewReader("<p>bv release-gate fixture</p>"))
    if err != nil || node == nil {
        panic("fixture HTML parser failed")
    }
    println("bv release-gate fixture")
}
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
gofmt -w "$fixture/cmd/bv/main.go"
git -C "$fixture" init -q -b main
commit_fixture "$fixture" 'release gate fixture'
revision="$(git -C "$fixture" rev-parse HEAD)"
git -C "$fixture" remote add origin https://github.com/release-gate-fixture/bv
git -C "$fixture" tag v0.0.1
echo "fixture revision: $revision"

if [ "$scope" != all ]; then
  # Replace only the scanner installation/identity boundary. All Go compiler,
  # source/receipt checks and the gate's actual scanner subprocess invocation
  # still execute. The observer reports the environment it really received;
  # this is not evidence of a vulnerability scan or release eligibility for bv.
  mkdir -p "$tmp/audit-observer-tools"
  export REAL_GO_FOR_FIXTURE="$fixture_go"
  export FIXTURE_SCANNER_OBSERVER="$tmp/scanner-observer.py"
  cat >"$FIXTURE_SCANNER_OBSERVER" <<'PY'
#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
print("Go: " + os.environ["GOVERSION"])
print("fixture-only scanner environment: " + json.dumps({
    key: os.environ[key] for key in ("GOVERSION", "CGO_ENABLED", "GOOS", "GOARCH", "GOFLAGS")
}, sort_keys=True))
print("fixture scanner observer completed; no vulnerability analysis performed")
binary_mode = "-mode=binary" in sys.argv
if binary_mode:
    binary = pathlib.Path(sys.argv[-1])
    print("fixture binary scan sha256: " + hashlib.sha256(binary.read_bytes()).hexdigest())
    if os.environ.get("FIXTURE_BINARY_MUTATE") == "1":
        with binary.open("ab") as f:
            f.write(b"fixture changed binary during scan")
if os.environ.get("FIXTURE_SCANNER_MUTATE") == ("binary" if binary_mode else "source"):
    with open(__file__, "a") as f:
        f.write("\n# fixture changed scanner during scan\n")
code = int(os.environ.get("FIXTURE_BINARY_SCANNER_EXIT" if binary_mode else "FIXTURE_SCANNER_EXIT", "0"))
if code:
    print("fixture planned scanner exit: " + str(code))
sys.exit(code)
PY
  cat >"$tmp/audit-observer-tools/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -eq 2 ] && [ "$1" = install ] && [ "$2" = golang.org/x/vuln/cmd/govulncheck@v1.9.0 ]; then
  printf 'fixture-only scanner install GOFLAGS: %s\n' "$GOFLAGS"
  scanner="$GOBIN/govulncheck"
  if [ "$("$REAL_GO_FOR_FIXTURE" env GOHOSTOS)" = windows ]; then scanner="$scanner.exe"; fi
  cp "$FIXTURE_SCANNER_OBSERVER" "$scanner"
  chmod +x "$scanner"
  exit
fi
if [ "$#" -eq 3 ] && [ "$1" = version ] && [ "$2" = -m ]; then
  case "$3" in
    */govulncheck|*/govulncheck.exe)
      compiler="$("$REAL_GO_FOR_FIXTURE" env GOVERSION)"
      version=v1.9.0
      command=golang.org/x/vuln/cmd/govulncheck
      case "${FIXTURE_SCANNER_METADATA:-}" in
        old-compiler) compiler=go1.26.8 ;;
        old-version) version=v1.8.0 ;;
        wrong-command) command=example.invalid/pretend-scanner ;;
      esac
      printf '%s: %s\n\tpath\t%s\n\tmod\tgolang.org/x/vuln\t%s\n' "$3" "$compiler" "$command" "$version"
      if [ "${FIXTURE_SCANNER_METADATA:-}" = replacement ]; then
        printf '\t=>\t./fixture-scanner-replacement\t(devel)\n'
      fi
      exit ;;
  esac
fi
exec "$REAL_GO_FOR_FIXTURE" "$@"
SH
  chmod +x "$tmp/audit-observer-tools/go"
  export PATH="$tmp/audit-observer-tools:$PATH"
  echo 'offline controls: synthetic scanner observer; no vulnerability database/network access'
fi

# A fresh receipt must never bless an unsafe compiler just because the same
# unsafe compiler is then used for packaging. Preflight has its own policy.
bash "$fixture/scripts/release_gate.sh" verify-toolchain
env GOFLAGS= GOENV=off bash "$fixture/scripts/release_gate.sh" verify-toolchain
env GOFLAGS=-p=2 bash "$fixture/scripts/release_gate.sh" verify-toolchain
echo 'ok: empty GOFLAGS and resource-only -p=2 accepted'

# External files can change while their GOFLAGS path stays identical. Neither
# source overlays/module substitutions nor tool/profile inputs may escape the
# tracked source and release configuration through ambient Go flags.
external="$tmp/external-go-inputs"
mkdir -p "$external"
cp "$fixture/go.mod" "$external/external.mod"
cp "$fixture/cmd/bv/main.go" "$external/overlay-main.go"
printf '\n// external overlay fixture\n' >>"$external/overlay-main.go"
python3 - "$fixture/cmd/bv/main.go" "$external/overlay-main.go" "$external/overlay.json" <<'PY'
import json, sys
json.dump({'Replace': {sys.argv[1]: sys.argv[2]}}, open(sys.argv[3], 'w'))
PY
printf '#!/usr/bin/env bash\nexit 73\n' >"$external/toolexec"
chmod +x "$external/toolexec"
printf 'external fixture profile\n' >"$external/profile.pgo"
reject overlay-flags 'unsupported release GOFLAGS' env GOFLAGS="-overlay=$external/overlay.json" bash "$fixture/scripts/release_gate.sh" verify-toolchain
reject modfile-flags 'unsupported release GOFLAGS' env GOFLAGS="-modfile=$external/external.mod" bash "$fixture/scripts/release_gate.sh" verify-toolchain
reject non-vendor-flags 'unsupported release GOFLAGS' env GOFLAGS=-mod=mod bash "$fixture/scripts/release_gate.sh" verify-toolchain
reject toolexec-flags 'unsupported release GOFLAGS' env GOFLAGS="-toolexec=$external/toolexec" bash "$fixture/scripts/release_gate.sh" verify-toolchain
reject profile-flags 'unsupported release GOFLAGS' env GOFLAGS="-pgo=$external/profile.pgo" bash "$fixture/scripts/release_gate.sh" verify-toolchain
printf 'GOFLAGS=-overlay=%s\n' "$external/overlay.json" >"$external/goenv"
reject persistent-flags 'unsupported release GOFLAGS' env GOFLAGS= GOENV="$external/goenv" bash "$fixture/scripts/release_gate.sh" verify-toolchain

old_toolchain="$(clone_fixture old-toolchain)"
(cd "$old_toolchain" && go mod edit -toolchain=go1.26.8)
reject old-toolchain 'below the security floor' bash "$old_toolchain/scripts/release_gate.sh" verify-toolchain
vulnerable_next_minor="$(clone_fixture vulnerable-next-minor)"
(cd "$vulnerable_next_minor" && go mod edit -toolchain=go1.27.2)
reject vulnerable-next-minor 'below the security floor' bash "$vulnerable_next_minor/scripts/release_gate.sh" verify-toolchain
wrong_toolchain="$(clone_fixture wrong-toolchain)"
(cd "$wrong_toolchain" && go mod edit -toolchain=go1.26.99)
reject compiler-selection 'release requires go1.26.99' bash "$wrong_toolchain/scripts/release_gate.sh" verify-toolchain
fake_modcache="$tmp/fake-gomodcache"
fake_tc_bin="$fake_modcache/golang.org/toolchain@v0.0.1-go1.26.99.$(go env GOHOSTOS)-$(go env GOHOSTARCH)/bin"
mkdir -p "$fake_tc_bin"
printf '#!/usr/bin/env bash\nexit 0\n' >"$fake_tc_bin/go"
chmod +x "$fake_tc_bin/go"
reject preflight-compiler-hint 'hint: prepend PATH=' env GOMODCACHE="$fake_modcache" bash "$wrong_toolchain/scripts/release_gate.sh" preflight
if grep -q 'Traceback' "$tmp/preflight-compiler-hint.log"; then
  cat "$tmp/preflight-compiler-hint.log"
  fail 'preflight compiler mismatch leaked Python Traceback'
fi
old_net="$(clone_fixture old-net)"
(cd "$old_net" && go mod edit -require=golang.org/x/net@v0.59.0)
reject old-net 'below the security floor' bash "$old_net/scripts/release_gate.sh" verify-toolchain
stale_vendor="$(clone_fixture stale-vendor)"
printf '# golang.org/x/net v0.59.0\n## explicit; go 1.26.0\ngolang.org/x/net/html\ngolang.org/x/net/html/atom\n' \
  >"$stale_vendor/vendor/modules.txt"
reject stale-vendor 'inconsistent vendoring' bash "$stale_vendor/scripts/release_gate.sh" verify-toolchain
replaced_net="$(clone_fixture replaced-net)"
(cd "$replaced_net" && go mod edit -replace=golang.org/x/net=./vendor/golang.org/x/net)
reject replaced-net 'must use its verified upstream module' bash "$replaced_net/scripts/release_gate.sh" verify-toolchain

# This first control fails against the old gate: it printed PASSED after skips
# and did not emit an eligibility receipt.
run_gate skipped "$fixture" 2 RELEASE_GATE_SKIP='1 2 3 4 5 6 7 8 9 10 11'
if grep -q 'RELEASE GATE PASSED' "$tmp/gate-skipped.log"; then
  fail 'skipped checks were advertised as a passing release gate'
fi
reject skipped diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source

run_gate complete "$fixture" 0 GOVERSION=go1.99.0
receipt="$gate_receipt"
export RELEASE_GATE_RECEIPT="$receipt"
bash "$fixture/scripts/release_gate.sh" verify-source
python3 - "$receipt" "$revision" "$scope" <<'PY'
import json, pathlib, sys
r = json.load(open(sys.argv[1]))
assert r['eligible'] is True
assert r['source']['revision'] == sys.argv[2]
assert len(r['stages']) == 11 and all(s['status'] == 'passed' for s in r['stages'])
assert 'go test -race -count=1' in r['stages'][2]['command']
assert r['dataset']['sha256'] and r['toolchain']['GOVERSION']
assert r['toolchain']['GOTOOLCHAIN'] == 'local'
assert r['module_policy']['dependencies']['golang.org/x/net']
assert len(r['source_audit']['scans']) == 5
assert all(row['exit_code'] == 0 for row in r['source_audit']['scans'])
for row in r['source_audit']['scans']:
    assert row['go_version'] == r['toolchain']['GOVERSION'] != 'go1.99.0'
    assert row['cgo_enabled'] == r['toolchain']['CGO_ENABLED'] == '0'
    assert row['go_flags'] == r['toolchain']['GOFLAGS']
    lines = pathlib.Path(row['path']).read_text().splitlines()
    assert 'Go: ' + row['go_version'] in lines
    if sys.argv[3] != 'all':
        prefix = 'fixture-only scanner environment: '
        observed = [json.loads(line[len(prefix):]) for line in lines if line.startswith(prefix)]
        assert observed == [{'GOVERSION': row['go_version'], 'CGO_ENABLED': '0',
                             'GOFLAGS': r['toolchain']['GOFLAGS'],
                             'GOOS': row['goos'], 'GOARCH': row['goarch']}]
if sys.argv[3] != 'all':
    install_log = pathlib.Path(r['source_audit']['tool']['path']).parent.parent / 'install.log'
    assert 'fixture-only scanner install GOFLAGS: ' + r['toolchain']['GOFLAGS'] in install_log.read_text().splitlines()
PY
echo 'ok: caller GOVERSION=go1.99.0 cannot change the scanner standard-library version'
python3 - "$receipt" "$tmp/wrong-audit-go.json" "$tmp/wrong-audit-cgo.json" "$tmp/wrong-audit-flags.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
r['source_audit']['scans'][0]['go_version'] = 'go1.99.0'
json.dump(r, open(sys.argv[2], 'w'))
r['source_audit']['scans'][0]['go_version'] = r['toolchain']['GOVERSION']
r['source_audit']['scans'][0]['cgo_enabled'] = '1'
json.dump(r, open(sys.argv[3], 'w'))
r['source_audit']['scans'][0]['cgo_enabled'] = r['toolchain']['CGO_ENABLED']
r['source_audit']['scans'][0]['go_flags'] = '-mod=mod'
json.dump(r, open(sys.argv[4], 'w'))
PY
reject wrong-audit-go 'analysis environment differs' env RELEASE_GATE_RECEIPT="$tmp/wrong-audit-go.json" bash "$fixture/scripts/release_gate.sh" verify-source
reject wrong-audit-cgo 'analysis environment differs' env RELEASE_GATE_RECEIPT="$tmp/wrong-audit-cgo.json" bash "$fixture/scripts/release_gate.sh" verify-source
reject wrong-audit-flags 'analysis environment differs' env RELEASE_GATE_RECEIPT="$tmp/wrong-audit-flags.json" bash "$fixture/scripts/release_gate.sh" verify-source
python3 - "$receipt" "$tmp/wrong-audit-header.json" "$tmp/wrong-audit-scanner-version.log" <<'PY'
import hashlib, json, pathlib, sys
r = json.load(open(sys.argv[1]))
row = r['source_audit']['scans'][0]
original = pathlib.Path(row['path']).read_text()
header = 'Go: ' + row['go_version']
assert header in original.splitlines()
log = pathlib.Path(sys.argv[3])
log.write_text(original.replace(header, 'Go: go1.99.0', 1))
row.update(path=str(log), sha256=hashlib.sha256(log.read_bytes()).hexdigest())
json.dump(r, open(sys.argv[2], 'w'))
PY
reject wrong-audit-header 'scanner Go version differs' env RELEASE_GATE_RECEIPT="$tmp/wrong-audit-header.json" bash "$fixture/scripts/release_gate.sh" verify-source
if [ "$scope" != all ]; then
  # Only the build-info observation is synthetic; the gate must inspect the
  # actual executable metadata again instead of trusting a stored version.
  for metadata in old-version wrong-command replacement; do
    reject "scanner-$metadata" 'must identify upstream x/vuln v1.9.0 without replacement' \
      env FIXTURE_SCANNER_METADATA="$metadata" bash "$fixture/scripts/release_gate.sh" verify-source
  done
  reject scanner-old-compiler 'govulncheck compiler is below the security floor' \
    env FIXTURE_SCANNER_METADATA=old-compiler bash "$fixture/scripts/release_gate.sh" verify-source
fi
if [ "$scope" = audit-environment ]; then
  echo 'release_gate_test: PASS (focused scanner environment control; synthetic observer, no vulnerability scan/publication)'
  exit 0
fi
if [ "$scope" = gate-mechanics ]; then
  # govulncheck uses exit 3 for findings and nonzero for scanner failures.
  # Exercise both subprocess outcomes through the actual stage/receipt path.
  for code in 3 1; do
    run_gate "scanner-exit-$code" "$fixture" 1 FIXTURE_SCANNER_EXIT="$code"
    python3 - "$gate_receipt" "$code" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
assert r['eligible'] is False
assert r['stages'][10]['name'] == 'source-vulnerabilities'
assert r['stages'][10]['status'] == 'failed'
assert all(row['exit_code'] == int(sys.argv[2]) for row in r['source_audit']['scans'])
PY
    reject "scanner-exit-$code" diagnostic env RELEASE_GATE_RECEIPT="$gate_receipt" bash "$fixture/scripts/release_gate.sh" verify-source
  done
fi
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
python3 - "$receipt" "$tmp/no-audit.json" "$tmp/failed-audit.json" "$tmp/changed-audit.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
missing = dict(r)
missing.pop('source_audit')
json.dump(missing, open(sys.argv[2], 'w'))
r['source_audit']['scans'][0]['exit_code'] = 3
json.dump(r, open(sys.argv[3], 'w'))
r['source_audit']['scans'][0]['exit_code'] = 0
r['source_audit']['scans'][0]['sha256'] = '0' * 64
json.dump(r, open(sys.argv[4], 'w'))
PY
reject no-audit 'source vulnerability evidence' env RELEASE_GATE_RECEIPT="$tmp/no-audit.json" bash "$fixture/scripts/release_gate.sh" verify-source
reject failed-audit 'source vulnerability evidence' env RELEASE_GATE_RECEIPT="$tmp/failed-audit.json" bash "$fixture/scripts/release_gate.sh" verify-source
reject changed-audit 'source vulnerability evidence changed' env RELEASE_GATE_RECEIPT="$tmp/changed-audit.json" bash "$fixture/scripts/release_gate.sh" verify-source

# The actual checked-in GoReleaser config must consume the receipt and finish
# packaging without network publication. Its post hooks inspect all binaries.
# When goreleaser is absent on a diagnostic host, verify that `package` still
# fails closed (and still binds packaging_config), continue testing all 5
# pure-Go cross-builds and tamper controls, and return sentinel 200 at the end
# so Stage 10 records SKIPPED (ineligible receipt) instead of FAILED.
goreleaser_dist="$tmp/goreleaser-packages"
goreleaser_skipped=0
if command -v goreleaser >/dev/null 2>&1; then
  if ! RELEASE_GATE_DIST="$goreleaser_dist" bash "$fixture/scripts/release_gate.sh" package >"$tmp/goreleaser-package.log" 2>&1; then
    cat "$tmp/goreleaser-package.log"
    fail "GoReleaser package path failed"
  fi
  echo "ok: GoReleaser packaged and verified real binaries ($tmp/goreleaser-package.log)"
  cp "$receipt" "$tmp/goreleaser-sealed-receipt.json"
else
  if RELEASE_GATE_DIST="$goreleaser_dist" bash "$fixture/scripts/release_gate.sh" package >"$tmp/goreleaser-package.log" 2>&1; then
    fail "package unexpectedly succeeded without goreleaser installed"
  fi
  mkdir -p "$goreleaser_dist"
  goreleaser_skipped=1
  echo "SKIPPED: goreleaser not installed (verified package fails closed; running pure-Go cross-build, seal, verify, and tamper controls)"
fi
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
python3 - "$receipt" "$net_version" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
assert len(r['artifacts']) == 5
for artifact in r['artifacts']:
    assert artifact['binary']['dependencies']['golang.org/x/net']['version'] == sys.argv[2]
    assert artifact['binary']['go_version'] == r['toolchain']['GOVERSION']
    audit = artifact['binary_audit']
    assert audit['mode'] == 'binary' and audit['format'] == 'text' and audit['exit_code'] == 0
    assert audit['binary_sha256'] == artifact['binary']['sha256']
    assert audit['tool'] == r['source_audit']['tool']
    assert audit['go_version'] == artifact['binary']['go_version']
    assert audit['started_at'] and audit['finished_at']
PY

# Archive evidence stays independently reviewable after later negative controls.
cp "$receipt" "$tmp/receipt-before-tampering.json"

# An archive hash without successful scans of its exact binary is insufficient.
python3 - "$receipt" "$tmp/no-binary-audit.json" "$tmp/wrong-binary-audit.json" "$tmp/changed-binary-audit.json" <<'PY'
import copy, json, sys
original = json.load(open(sys.argv[1]))
missing = copy.deepcopy(original)
missing['artifacts'][0].pop('binary_audit')
json.dump(missing, open(sys.argv[2], 'w'))
wrong = copy.deepcopy(original)
wrong['artifacts'][0]['binary_audit']['binary_sha256'] = '0' * 64
json.dump(wrong, open(sys.argv[3], 'w'))
changed = copy.deepcopy(original)
changed['artifacts'][0]['binary_audit']['sha256'] = '0' * 64
json.dump(changed, open(sys.argv[4], 'w'))
PY
reject no-binary-audit 'unsuccessful binary vulnerability evidence' env RELEASE_GATE_RECEIPT="$tmp/no-binary-audit.json" bash "$fixture/scripts/release_gate.sh" verify
reject wrong-binary-audit 'differs from archived binary' env RELEASE_GATE_RECEIPT="$tmp/wrong-binary-audit.json" bash "$fixture/scripts/release_gate.sh" verify
reject changed-binary-audit 'binary vulnerability evidence changed' env RELEASE_GATE_RECEIPT="$tmp/changed-binary-audit.json" bash "$fixture/scripts/release_gate.sh" verify
# A forged success flag cannot override a scanner transcript that records a
# nonzero result, even if the transcript hash is recomputed in the receipt.
python3 - "$receipt" "$tmp/forged-binary-success.json" "$tmp/forged-binary-scan.log" <<'PY'
import hashlib, json, pathlib, sys
r = json.load(open(sys.argv[1]))
audit = r['artifacts'][0]['binary_audit']
original = pathlib.Path(audit['path']).read_bytes()
assert original.endswith(b'\ngovulncheck exit status: 0\n')
log = pathlib.Path(sys.argv[3])
log.write_bytes(original[:-len(b'govulncheck exit status: 0\n')] + b'govulncheck exit status: 3\n')
audit['path'] = str(log)
audit['sha256'] = hashlib.sha256(log.read_bytes()).hexdigest()
json.dump(r, open(sys.argv[2], 'w'))
PY
reject forged-binary-success 'transcript does not match successful scan metadata' env RELEASE_GATE_RECEIPT="$tmp/forged-binary-success.json" bash "$fixture/scripts/release_gate.sh" verify

# Keep five rows in these controls: completeness requires the exact target set,
# and each scan's target must agree with the binary metadata it accompanies.
python3 - "$receipt" "$tmp/duplicate-artifact-target.json" \
  "$tmp/unsupported-artifact-target.json" "$tmp/mismatched-audit-target.json" \
  "$tmp/unsupported-target-scan.log" <<'PY'
import copy, hashlib, json, pathlib, sys
original = json.load(open(sys.argv[1]))
duplicate = copy.deepcopy(original)
duplicate['artifacts'][1] = copy.deepcopy(duplicate['artifacts'][0])
json.dump(duplicate, open(sys.argv[2], 'w'))
unsupported = copy.deepcopy(original)
unsupported['artifacts'][0]['binary']['settings']['GOOS'] = 'freebsd'
audit = unsupported['artifacts'][0]['binary_audit']
audit['goos'] = 'freebsd'
# Keep this copied fixture transcript consistent with its edited metadata so
# the control reaches the distinct target-inventory rejection it exercises.
header, body = pathlib.Path(audit['path']).read_bytes().split(b'\n', 1)
metadata = json.loads(header)
metadata['goos'] = 'freebsd'
log = pathlib.Path(sys.argv[5])
log.write_bytes((json.dumps(metadata, sort_keys=True) + '\n').encode() + body)
audit['path'] = str(log)
audit['sha256'] = hashlib.sha256(log.read_bytes()).hexdigest()
json.dump(unsupported, open(sys.argv[3], 'w'))
mismatched = copy.deepcopy(original)
mismatched['artifacts'][0]['binary_audit']['goos'] = 'freebsd'
json.dump(mismatched, open(sys.argv[4], 'w'))
PY
reject duplicate-artifact-target 'release target set mismatch' env RELEASE_GATE_RECEIPT="$tmp/duplicate-artifact-target.json" bash "$fixture/scripts/release_gate.sh" verify
reject unsupported-artifact-target 'release target set mismatch' env RELEASE_GATE_RECEIPT="$tmp/unsupported-artifact-target.json" bash "$fixture/scripts/release_gate.sh" verify
reject mismatched-audit-target 'differs from archived binary' env RELEASE_GATE_RECEIPT="$tmp/mismatched-audit-target.json" bash "$fixture/scripts/release_gate.sh" verify

if [ "$scope" = gate-mechanics ]; then
  python3 - "$receipt" <<'PY'
import json, pathlib, sys
r = json.load(open(sys.argv[1]))
for artifact in r['artifacts']:
    audit = artifact['binary_audit']
    lines = pathlib.Path(audit['path']).read_text().splitlines()
    assert 'fixture binary scan sha256: ' + artifact['binary']['sha256'] in lines
    prefix = 'fixture-only scanner environment: '
    observed = [json.loads(line[len(prefix):]) for line in lines if line.startswith(prefix)]
    assert observed == [{'GOVERSION': artifact['binary']['go_version'], 'CGO_ENABLED': '0',
                         'GOFLAGS': r['toolchain']['GOFLAGS'],
                         'GOOS': audit['goos'], 'GOARCH': audit['goarch']}]
PY
  # GoReleaser's post-build hook must fail on findings and scanner errors,
  # before archive sealing can provide a second independent scan.
  for code in 3 1; do
    reject "binary-posthook-exit-$code" 'unsuccessful binary vulnerability evidence' \
      env GOVERSION=go1.99.0 FIXTURE_BINARY_SCANNER_EXIT="$code" \
      bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/binaries/bv-linux-amd64"
  done
  cp "$receipt" "$tmp/failed-package.json"
  if command -v goreleaser >/dev/null 2>&1; then
    reject failed-package-posthook 'unsuccessful binary vulnerability evidence' \
      env RELEASE_GATE_RECEIPT="$tmp/failed-package.json" FIXTURE_BINARY_SCANNER_EXIT=3 \
      RELEASE_GATE_DIST="$tmp/failed-package-dist" bash "$fixture/scripts/release_gate.sh" package
  else
    if RELEASE_GATE_RECEIPT="$tmp/failed-package.json" FIXTURE_BINARY_SCANNER_EXIT=3 \
      RELEASE_GATE_DIST="$tmp/failed-package-dist" bash "$fixture/scripts/release_gate.sh" package >"$tmp/failed-package-posthook.log" 2>&1; then
      fail "failed-package-posthook unexpectedly succeeded without goreleaser"
    fi
  fi
  python3 - "$tmp/failed-package.json" "$tmp/failed-package-dist" <<'PY'
import json, pathlib, sys
r = json.load(open(sys.argv[1]))
assert 'artifacts' not in r and 'checksums' not in r
assert not (pathlib.Path(sys.argv[2]) / 'checksums.txt').exists()
PY
  reject failed-package-evidence 'no complete packaged archive evidence' \
    env RELEASE_GATE_RECEIPT="$tmp/failed-package.json" bash "$fixture/scripts/release_gate.sh" verify
  for code in 3 1; do
    cp "$tmp/receipt-before-tampering.json" "$receipt"
    reject "binary-scan-exit-$code" 'unsuccessful binary vulnerability evidence' \
      env GOVERSION=go1.99.0 FIXTURE_BINARY_SCANNER_EXIT="$code" bash "$fixture/scripts/release_gate.sh" seal "$dist"
    reject "failed-reseal-$code" 'no complete packaged archive evidence' bash "$fixture/scripts/release_gate.sh" verify
    cp "$receipt" "$tmp/failed-reseal-$code.json"
  done
  cp "$tmp/receipt-before-tampering.json" "$receipt"
  reject changed-during-binary-scan 'archived binary changed during vulnerability analysis' \
    env FIXTURE_BINARY_MUTATE=1 bash "$fixture/scripts/release_gate.sh" seal "$dist"
  reject changed-binary-reseal 'no complete packaged archive evidence' bash "$fixture/scripts/release_gate.sh" verify
  cp "$receipt" "$tmp/changed-binary-reseal.json"
  scanner="$(python3 - "$receipt" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))['source_audit']['tool']['path'])
PY
)"
  cp "$scanner" "$tmp/scanner-before-mutation"
  cp "$tmp/receipt-before-tampering.json" "$receipt"
  reject changed-during-scanner-run 'govulncheck executable changed after audit binding' \
    env FIXTURE_SCANNER_MUTATE=binary bash "$fixture/scripts/release_gate.sh" seal "$dist"
  mv "$scanner" "$tmp/scanner-after-mutation"
  cp "$tmp/scanner-before-mutation" "$scanner"
  chmod +x "$scanner"
  reject changed-scanner-reseal 'no complete packaged archive evidence' bash "$fixture/scripts/release_gate.sh" verify
  cp "$receipt" "$tmp/changed-scanner-reseal.json"
  # Retain the failed-reseal receipts above and finish with a new
  # real seal operation, never by restoring stale successful archive evidence.
  GOVERSION=go1.99.0 bash "$fixture/scripts/release_gate.sh" seal "$dist"
  bash "$fixture/scripts/release_gate.sh" verify
fi

(cd "$dirty" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=true -o "$tmp/dirty-bv" ./cmd/bv)
reject dirty-binary 'dirty/unknown source' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/dirty-bv"
(cd "$changed" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=true -o "$tmp/wrong-revision-bv" ./cmd/bv)
reject wrong-binary 'binary revision mismatch' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/wrong-revision-bv"
(cd "$fixture" && CGO_ENABLED=0 GOWORK=off go build -buildvcs=false -o "$tmp/no-build-info-bv" ./cmd/bv)
reject no-build-info 'binary revision mismatch' bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/no-build-info-bv"

# Feed altered build-info records into the real verification path. These are
# parser/validation controls; the positive archive controls above use actual
# unmodified Go metadata from all five cross-built executables.
mkdir -p "$tmp/metadata-tools"
export REAL_GO_FOR_FIXTURE="$fixture_go"
BUILDINFO_GO_FOR_FIXTURE="$(command -v go)"
export BUILDINFO_GO_FOR_FIXTURE
cat >"$tmp/metadata-tools/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ] && [ "${2:-}" = -m ]; then
  case "${3:-}" in
    */govulncheck|*/govulncheck.exe) exec "$BUILDINFO_GO_FOR_FIXTURE" "$@" ;;
  esac
  "$BUILDINFO_GO_FOR_FIXTURE" "$@" | python3 -c '
import os, re, sys
mode = os.environ["FIXTURE_BINARY_METADATA"]
for line in sys.stdin:
    fields = line.split()
    if mode == "old-compiler" and not line.startswith("\t"):
        line = re.sub(r"go[0-9]+\.[0-9]+\.[0-9]+", "go1.26.8", line)
    if fields[:2] == ["dep", "golang.org/x/net"]:
        if mode == "missing-net":
            continue
        if mode == "old-net":
            line = "\tdep\tgolang.org/x/net\tv0.59.0\n"
        elif mode == "replaced-net":
            line += "\t=>\t./unverified-net\t(devel)\n"
    sys.stdout.write(line)
'
  exit
fi
exec "$BUILDINFO_GO_FOR_FIXTURE" "$@"
SH
chmod +x "$tmp/metadata-tools/go"
reject old-binary-compiler 'binary toolchain mismatch' \
  env PATH="$tmp/metadata-tools:$PATH" FIXTURE_BINARY_METADATA=old-compiler \
  bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/binaries/bv-linux-amd64"
reject old-binary-net 'binary dependency mismatch' \
  env PATH="$tmp/metadata-tools:$PATH" FIXTURE_BINARY_METADATA=old-net \
  bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/binaries/bv-linux-amd64"
reject missing-binary-net 'missing golang.org/x/net build information' \
  env PATH="$tmp/metadata-tools:$PATH" FIXTURE_BINARY_METADATA=missing-net \
  bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/binaries/bv-linux-amd64"
reject replaced-binary-net 'undeclared dependency replacement' \
  env PATH="$tmp/metadata-tools:$PATH" FIXTURE_BINARY_METADATA=replaced-net \
  bash "$fixture/scripts/release_gate.sh" verify-binary "$tmp/binaries/bv-linux-amd64"

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
if [ "$goreleaser_skipped" -eq 1 ]; then
  echo "release_gate_test: SKIPPED live GoReleaser packaging (goreleaser unavailable; package fail-closed, five pure-Go cross-builds, seal/verify, and all tamper controls PASSED)"
  exit 200
fi
if [ "$scope" = gate-mechanics ]; then
  echo "release_gate_test: PASS (synthetic stages/scanner; real Git, GoReleaser and five Go cross-builds; no vulnerability scan/publication)"
else
  echo "release_gate_test: PASS (synthetic helper stages; real govulncheck, Git, GoReleaser and five Go cross-builds; no publication)"
fi
