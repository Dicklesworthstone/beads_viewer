#!/usr/bin/env bash
# release_gate.sh: the single pre-release check. Every stage must pass; the
# gate writes its log and receipt outside the source checkout. Only a clean,
# complete receipt permits packaging; diagnostic skips never authorize release.
# Usage: release_gate.sh [run|verify-source|verify-binary PATH|package|seal DIR|verify]
# Set RELEASE_GATE_RECEIPT to the receipt printed by `run` for later commands.
#
# Stages:
#   1 gofmt            gofmt -l over repo Go files (vendor excluded) is empty
#   2 build+vet        go build ./... && go vet ./...
#   3 unit tests       go test ./... -race -count=1 (pkg/, cmd/, internal/)
#   4 e2e tests        go test ./tests/e2e -race -count=1
#   5 docs parity      go generate ./... leaves the tree unchanged
#   6 action pins      scripts/check_action_pins.sh (40-hex SHAs only)
#   7 vendor hashes    scripts/verify_vendor.sh --source (hashes and graph rebuild)
#   8 benchmarks       scripts/benchmark.sh compare, > 20% regression fails
#   9 robot smoke      scripts/robot_smoke.sh on this repo and a fixture
#  10 script tests     tests/scripts/*_test.sh for the gate's own helpers:
#                      the benchmark comparator always; the installer harness
#                      when pwsh is available (PWSH=/path or on PATH). The
#                      isomorphic-verify self-test clones and builds twice and
#                      stays a manual run (tests/scripts/verify_isomorphic_test.sh).
#
# Environment:
#   RELEASE_GATE_SKIP="7 8"        skip listed stages (each skip is logged as
#                                  SKIPPED; incomplete diagnostic run exits 2)
#   RELEASE_GATE_ALLOW_MISSING=1   a stage whose script does not exist yet is
#                                  logged as SKIPPED instead of failing
#   RELEASE_GATE_BENCH_PCT=20      regression threshold for stage 8
#   RELEASE_GATE_OUTPUT_DIR       parent for a fresh log/receipt directory;
#                                  defaults to /tmp, must be outside the checkout
#   RELEASE_GATE_RECEIPT           existing receipt consumed by packaging/verify
#   RELEASE_GATE_DIST             packaging directory, defaults to /tmp/bv-dist
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root" || exit 1
export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1 BV_NO_UPDATE_CHECK=1
export GOFLAGS="${GOFLAGS:-}"
export GOWORK=off CGO_ENABLED=0

# JSON, content hashing, and archive inspection live here rather than in a
# second gate implementation. This code only reads source and writes evidence
# outside it. It never executes a packaged binary or extracts archive paths.
receipt() {
  python3 - "$root" "${RELEASE_GATE_RECEIPT:-}" "$@" <<'PY'
import hashlib, json, os, pathlib, stat, subprocess, sys, tarfile, tempfile, zipfile
from datetime import datetime, timezone

root = pathlib.Path(sys.argv[1]).resolve()
receipt_path = pathlib.Path(sys.argv[2]).resolve() if sys.argv[2] else None
mode, *args = sys.argv[3:]
stages = ["gofmt", "build+vet", "unit-tests", "e2e-tests", "docs-parity",
          "action-pins", "vendor-hashes", "benchmarks", "robot-smoke", "script-tests"]

def fail(message):
    raise ValueError(message)

def run(*argv):
    return subprocess.check_output(argv, cwd=root).decode().strip()

def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()

def outside(path):
    path = pathlib.Path(path).resolve()
    if path == root or root in path.parents:
        fail(f"evidence/build output must be outside source: {path}")
    return path

def source():
    paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=root).split(b"\0")
    h = hashlib.sha256()
    for raw in sorted(p for p in paths if p):
        path = root / os.fsdecode(raw)
        h.update(raw + b"\0")
        if path.is_symlink():
            h.update(b"symlink\0" + os.fsencode(os.readlink(path)))
            if not path.is_file() or root not in path.resolve().parents:
                fail(f"undeclared symlink build input: {path}")
            h.update(bytes.fromhex(digest(path)))
        elif path.is_file():
            h.update(str(stat.S_IMODE(path.stat().st_mode)).encode() + b"\0")
            h.update(bytes.fromhex(digest(path)))
        else:
            h.update(b"missing-or-non-file")
    dirty = run("git", "status", "--porcelain=v1", "--untracked-files=all")
    # Ignored Go/embed inputs still affect builds. Build caches outside these
    # declared source directories are not inputs to the release binary.
    ignored = run("git", "ls-files", "--others", "--ignored", "--exclude-standard",
                  "--", "cmd", "pkg", "internal", "vendor", "go.work", "go.work.sum")
    return {"revision": run("git", "rev-parse", "HEAD"),
            "tree": run("git", "rev-parse", "HEAD^{tree}"),
            "inputs_sha256": h.hexdigest(), "dirty": dirty.splitlines(),
            "ignored_build_inputs": ignored.splitlines()}

def toolchain():
    return json.loads(run("go", "env", "-json", "GOVERSION", "GOHOSTOS", "GOHOSTARCH",
                          "GOEXPERIMENT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "CGO_ENABLED"))

def dataset():
    path = root / "tests/testdata/benchmark/medium.jsonl"
    return {"path": str(path.relative_to(root)), "sha256": digest(path) if path.is_file() else None}

def load():
    if receipt_path is None or not outside(receipt_path).is_file():
        fail("missing RELEASE_GATE_RECEIPT; run the complete gate first")
    with open(receipt_path) as f:
        return json.load(f)

def save(value):
    outside(receipt_path)
    # A run updates one ineligible receipt as it progresses; only finish can
    # mark it eligible, and each run receives a fresh external directory.
    with open(receipt_path, "w") as f:
        json.dump(value, f, indent=2, sort_keys=True)
        f.write("\n")

def verify_source(value):
    if value.get("schema") != 1 or value.get("eligible") is not True:
        fail("receipt is diagnostic/incomplete, not eligible for release")
    rows = value.get("stages", [])
    if len(rows) != len(stages) or any(row.get("number") != i + 1 or
            row.get("name") != name or row.get("status") != "passed" or
            not row.get("command") or row.get("exit_code") != 0
            for i, (name, row) in enumerate(zip(stages, rows))):
        fail("receipt has missing, skipped, or failed required stages")
    current = source()
    if current["dirty"] or current["ignored_build_inputs"]:
        fail("source is dirty or contains undeclared build inputs: " + json.dumps(current))
    if value["source"] != current:
        fail(f"source changed after gate: receipt {value['source']['revision']} / current {current['revision']}")
    if value["toolchain"] != toolchain():
        fail("Go toolchain/build environment differs from gate receipt")
    if value["dataset"] != dataset():
        fail("benchmark dataset differs from gate receipt")
    config = value.get("packaging_config")
    if config and digest(outside(config["path"])) != config["sha256"]:
        fail("derived packaging config changed after receipt binding")

def binary_info(path, value):
    output = run("go", "version", "-m", str(path))
    settings = {}
    version = ""
    for line in output.splitlines():
        if line.startswith(str(path) + ": "):
            version = line[len(str(path)) + 2:]
        words = line.strip().split(None, 1)
        if len(words) == 2 and words[0] == "build" and "=" in words[1]:
            key, val = words[1].split("=", 1)
            settings[key] = val
    if settings.get("vcs.revision") != value["source"]["revision"]:
        fail(f"binary revision mismatch or missing build info: {path}")
    if settings.get("vcs.modified") != "false":
        fail(f"binary was built from dirty/unknown source: {path}")
    if version != value["toolchain"]["GOVERSION"]:
        fail(f"binary toolchain mismatch: {path}: {version}")
    if settings.get("CGO_ENABLED") != value["toolchain"]["CGO_ENABLED"]:
        fail(f"binary CGO setting differs from release build environment: {path}")
    return {"sha256": digest(path), "go_version": version, "settings": settings}

try:
    if mode == "check-output":
        outside(args[0])
    elif mode == "init":
        outside(receipt_path)
        if receipt_path.exists(): fail(f"refusing to reuse receipt: {receipt_path}")
        save({"schema": 1, "eligible": False, "created_at": datetime.now(timezone.utc).isoformat(),
              "source": source(), "toolchain": toolchain(), "dataset": dataset(),
              "stages": [], "diagnostic_environment": args})
    elif mode == "stage":
        value = load()
        num, name, outcome, code, seconds, command = args
        value["stages"].append({"number": int(num), "name": name, "status": outcome,
                               "exit_code": int(code), "seconds": int(seconds), "command": command})
        save(value)
    elif mode == "finish":
        value = load()
        current = source()
        value["source_after"] = current
        value["eligible"] = (value["source"] == current and not current["dirty"] and
                not current["ignored_build_inputs"] and not value["diagnostic_environment"] and
                value["toolchain"] == toolchain() and
                value["dataset"]["sha256"] is not None and len(value["stages"]) == len(stages) and
                all(row["status"] == "passed" for row in value["stages"]))
        save(value)
        print("eligible" if value["eligible"] else "diagnostic")
    elif mode == "verify-source":
        value = load(); verify_source(value)
        print(f"source verified: {value['source']['revision']} (receipt: {receipt_path})")
    elif mode == "verify-binary":
        value = load(); verify_source(value)
        path = outside(args[0]); info = binary_info(path, value)
        print(f"binary verified: {path} sha256={info['sha256']}")
    elif mode == "package-config":
        value = load(); verify_source(value)
        dist = outside(args[0])
        original = (root / ".goreleaser.yaml").read_text()
        lines = original.splitlines(keepends=True)
        if sum(line == "dist: /tmp/bv-dist\n" for line in lines) != 1:
            fail("expected one declared dist setting in .goreleaser.yaml")
        # GoReleaser's dist field does not expand templates. Derive an external
        # config changing this one field; never modify the tracked config.
        rendered = "".join("dist: " + json.dumps(str(dist)) + "\n" if line == "dist: /tmp/bv-dist\n" else line for line in lines)
        config = receipt_path.parent / ("goreleaser-" + hashlib.sha256(str(dist).encode()).hexdigest()[:16] + ".yaml")
        with open(config, "x") as f: f.write(rendered)
        value["packaging_config"] = {"path": str(config), "sha256": digest(config), "dist": str(dist)}
        save(value)
        print(config)
    elif mode == "seal":
        value = load(); verify_source(value)
        dist = outside(args[0])
        archives = sorted([*dist.glob("*.tar.gz"), *dist.glob("*.zip")])
        if not archives: fail(f"no release archives in {dist}")
        checksum_file = dist / "checksums.txt"
        expected = {}
        for line in checksum_file.read_text().splitlines():
            sha, name = line.split(None, 1)
            expected[name.lstrip(" *")] = sha
        records = []
        targets = set()
        inspect_dir = pathlib.Path(tempfile.mkdtemp(prefix="bv-release-inspect-", dir=receipt_path.parent))
        for i, archive in enumerate(archives):
            sha = digest(archive)
            if expected.get(archive.name) != sha:
                fail(f"archive checksum mismatch: {archive.name} sha256={sha}")
            if archive.name.endswith(".zip"):
                with zipfile.ZipFile(archive) as z:
                    names = [n for n in z.namelist() if pathlib.PurePosixPath(n).name in ("bv", "bv.exe")]
                    if len(names) != 1: fail(f"expected one bv binary in {archive.name}")
                    data = z.read(names[0])
            else:
                with tarfile.open(archive, "r:gz") as t:
                    members = [m for m in t.getmembers() if m.isfile() and pathlib.PurePosixPath(m.name).name in ("bv", "bv.exe")]
                    if len(members) != 1: fail(f"expected one bv binary in {archive.name}")
                    data = t.extractfile(members[0]).read()
            binary = inspect_dir / f"binary-{i}"
            with open(binary, "xb") as f: f.write(data)
            info = binary_info(binary, value)
            target = (info["settings"].get("GOOS"), info["settings"].get("GOARCH"))
            if target in targets: fail(f"duplicate release target: {target}")
            targets.add(target)
            records.append({"path": str(archive), "sha256": sha, "binary": info})
        required = {("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"), ("windows", "amd64")}
        if targets != required: fail(f"release target set mismatch: got {sorted(targets)}, want {sorted(required)}")
        verify_source(value)
        value["artifacts"] = records
        value["checksums"] = {"path": str(checksum_file), "sha256": digest(checksum_file)}
        save(value)
        print(f"sealed {len(records)} archives: {receipt_path}")
    elif mode == "verify":
        value = load(); verify_source(value)
        artifacts = value.get("artifacts", [])
        if len(artifacts) != 5 or "checksums" not in value:
            fail("receipt has no complete packaged archive evidence; run package/seal first")
        for artifact in [*artifacts, value["checksums"], *([value["packaging_config"]] if "packaging_config" in value else [])]:
            path = outside(artifact["path"])
            actual = digest(path)
            if actual != artifact["sha256"]:
                fail(f"artifact changed: {path}; expected {artifact['sha256']}, got {actual}")
        print(f"RELEASE ARTIFACTS VERIFIED: {value['source']['revision']} ({receipt_path})")
    else:
        fail(f"unknown receipt operation: {mode}")
except (ValueError, KeyError, IndexError, OSError, subprocess.CalledProcessError, tarfile.TarError, zipfile.BadZipFile) as error:
    print(f"release gate: {error}", file=sys.stderr)
    sys.exit(1)
PY
}

case "${1:-run}" in
  verify-source|verify-binary|seal|verify) receipt "$@"; exit $? ;;
  package)
    receipt verify-source || exit 1
    # GoReleaser creates its dist directory; never clean/delete an existing one.
    dist="${RELEASE_GATE_DIST:-/tmp/bv-dist}"
    receipt check-output "$dist" || exit 1
    [ ! -e "$dist" ] || { echo "release gate: dist exists; choose a fresh RELEASE_GATE_DIST" >&2; exit 1; }
    export RELEASE_GATE_DIST="$dist"
    config="$(receipt package-config "$dist")" || exit 1
    # Tap/bucket generators require a published release URL; their updates are
    # separate authorized operations after the archives have been verified.
    goreleaser release --config "$config" --skip=publish,announce,homebrew,scoop || exit 1
    receipt seal "$dist" && receipt verify
    exit $?
    ;;
  run) ;;
  *) echo "usage: $0 [run|verify-source|verify-binary PATH|package|seal DIR|verify]" >&2; exit 2 ;;
esac

ts="$(date -u +%Y%m%dT%H%M%SZ)"
python3 - "$root" "${RELEASE_GATE_OUTPUT_DIR:-/tmp}" <<'PY' || exit 1
import pathlib, sys
root, output = (pathlib.Path(p).resolve() for p in sys.argv[1:])
if root == output or root in output.parents:
    sys.exit("release gate: output must be outside source")
PY
output="$(mktemp -d "${RELEASE_GATE_OUTPUT_DIR:-/tmp}/bv-release-gate-${ts}.XXXXXX")" || exit 1
export RELEASE_GATE_RECEIPT="$output/receipt.json"
diagnostic_env=()
for name in BV_SKIP_ENV_TESTS BENCH_REFERENCE BENCH_DATASET BENCH_COUNT RELEASE_GATE_BENCH_PCT ROBOT_SMOKE_BV ROBOT_SMOKE_SYNTHETIC; do
  if [ -n "${!name:-}" ]; then diagnostic_env+=("$name=${!name}"); fi
done
receipt init "${diagnostic_env[@]}" || exit 1
log="$output/gate.log"
: >"$log"
echo "release gate $ts on $(git rev-parse --short HEAD) ($(go version)); receipt: $RELEASE_GATE_RECEIPT" | tee -a "$log"

skip_list=" ${RELEASE_GATE_SKIP:-} "
failed=()
skipped=()
gate_start=$(date +%s)

stage() {
  # stage <number> <name> <command...>
  local num="$1" name="$2"; shift 2
  local start end secs status command
  command="$*"
  if declare -F "$1" >/dev/null; then command+=$'\n'"$(declare -f "$1")"; fi
  if [[ "$skip_list" == *" $num "* ]]; then
    printf '%-2s %-14s SKIPPED (RELEASE_GATE_SKIP)\n' "$num" "$name" | tee -a "$log"
    skipped+=("$num $name")
    receipt stage "$num" "$name" skipped 0 0 "$command (RELEASE_GATE_SKIP)" || exit 1
    return 0
  fi
  start=$(date +%s)
  {
    echo "----- stage $num $name: $*"
    "$@"
  } >>"$log" 2>&1
  status=$?
  end=$(date +%s); secs=$((end - start))
  if [ "$status" -eq 0 ]; then
    printf '%-2s %-14s ok      %4ds\n' "$num" "$name" "$secs" | tee -a "$log"
    receipt stage "$num" "$name" passed 0 "$secs" "$command" || exit 1
  elif [ "$status" -eq 200 ]; then
    printf '%-2s %-14s SKIPPED %4ds (required helper unavailable)\n' "$num" "$name" "$secs" | tee -a "$log"
    skipped+=("$num $name")
    receipt stage "$num" "$name" skipped "$status" "$secs" "$command" || exit 1
  else
    printf '%-2s %-14s FAILED  %4ds  (exit %d, see %s)\n' "$num" "$name" "$secs" "$status" "$log" | tee -a "$log"
    failed+=("$num $name")
    receipt stage "$num" "$name" failed "$status" "$secs" "$command" || exit 1
    tail -n 25 "$log" | sed 's/^/    /'
  fi
}

# Stage helpers that need more than one command.
check_gofmt() {
  local out
  out=$(gofmt -l cmd pkg internal tests) || return 1
  if [ -n "$out" ]; then
    echo "files need gofmt:"; echo "$out"; return 1
  fi
  echo "gofmt clean"
}

build_and_vet() { go build ./... && go vet ./...; }

unit_tests() {
  # tests/e2e builds the binary and takes its own stage.
  local packages package
  local -a package_args=()
  packages="$(go list ./... | grep -v '/tests/e2e$')" || return 1
  [ -n "$packages" ] || { echo "no unit-test packages discovered"; return 1; }
  # Race instrumentation requires CGO; shipped binaries remain pure Go.
  while IFS= read -r package; do package_args+=("$package"); done <<<"$packages"
  CGO_ENABLED=1 go test -race -count=1 "${package_args[@]}"
}

e2e_tests() { CGO_ENABLED=1 go test ./tests/e2e -race -count=1; }

docs_parity() {
  # Compare the working tree with itself before and after go generate, so
  # uncommitted work in progress does not count as drift; only what the
  # generators change does.
  local before after
  before=$( { git status --porcelain=v1 --untracked-files=all; git --no-pager diff; } | sha256sum)
  go generate ./... || return 1
  after=$( { git status --porcelain=v1 --untracked-files=all; git --no-pager diff; } | sha256sum)
  if [ "$before" != "$after" ]; then
    echo "go generate changed files (docs or tables out of date):"
    git status --porcelain=v1 --untracked-files=all
    return 1
  fi
  echo "generated files are up to date"
}

script_stage() {
  # script_stage <path> <args...>: run a helper script, honouring RELEASE_GATE_ALLOW_MISSING.
  local script="$1"; shift
  if [ ! -x "$script" ]; then
    if [ "${RELEASE_GATE_ALLOW_MISSING:-0}" = "1" ]; then
      echo "MISSING_OK $script"
      return 200
    fi
    echo "missing helper script $script (set RELEASE_GATE_ALLOW_MISSING=1 to skip while it is pending)"
    return 1
  fi
  "$script" "$@"
}

benchmarks() {
  # scripts/benchmark.sh runs the tracked set against the frozen dataset and
  # compares median sec/op per benchmark with benchmarks/baseline.txt; it
  # exits non-zero above the threshold or when a tracked benchmark is missing.
  [ -f benchmarks/baseline.txt ] || { echo "no benchmarks/baseline.txt (run scripts/benchmark.sh baseline)"; return 1; }
  BENCH_PCT="${RELEASE_GATE_BENCH_PCT:-20}" scripts/benchmark.sh compare
}

script_self_tests() {
  # The gate's own helpers have tests; a comparator that mis-parses a column
  # or an installer that stops failing closed must not pass silently.
  tests/scripts/benchmark_compare_test.sh || return 1
  # This harness executes synthetic Git repositories, never a release publish.
  tests/scripts/release_gate_test.sh || return 1
  local pwsh="${PWSH:-pwsh}"
  if command -v "$pwsh" >/dev/null 2>&1; then
    PWSH="$pwsh" tests/scripts/install_ps1_test.sh || return 1
  else
    echo "install_ps1_test skipped: pwsh not found (set PWSH=/path/to/pwsh to include it)"
    return 200
  fi
}

# Stages that wrap script_stage translate its MISSING_OK sentinel (200) into a skip.
run_script_stage() {
  local num="$1" name="$2" script="$3"; shift 3
  if [[ "$skip_list" == *" $num "* ]]; then
    printf '%-2s %-14s SKIPPED (RELEASE_GATE_SKIP)\n' "$num" "$name" | tee -a "$log"
    skipped+=("$num $name")
    receipt stage "$num" "$name" skipped 0 0 "$script $* (RELEASE_GATE_SKIP)" || exit 1
    return 0
  fi
  if [ ! -x "$script" ] && [ "${RELEASE_GATE_ALLOW_MISSING:-0}" = "1" ]; then
    printf '%-2s %-14s SKIPPED (missing %s; RELEASE_GATE_ALLOW_MISSING=1)\n' "$num" "$name" "$script" | tee -a "$log"
    skipped+=("$num $name")
    receipt stage "$num" "$name" skipped 200 0 "$script $* (missing helper)" || exit 1
    return 0
  fi
  stage "$num" "$name" script_stage "$script" "$@"
}

stage 1 gofmt check_gofmt
stage 2 build+vet build_and_vet
stage 3 unit-tests unit_tests
stage 4 e2e-tests e2e_tests
stage 5 docs-parity docs_parity
run_script_stage 6 action-pins scripts/check_action_pins.sh
run_script_stage 7 vendor-hashes scripts/verify_vendor.sh --source
stage 8 benchmarks benchmarks
run_script_stage 9 robot-smoke scripts/robot_smoke.sh
stage 10 script-tests script_self_tests

total=$(( $(date +%s) - gate_start ))
echo "----- release gate finished in ${total}s: ${#failed[@]} failed, ${#skipped[@]} skipped (log: $log)" | tee -a "$log"
if [ ${#skipped[@]} -gt 0 ]; then
  printf '  skipped: %s\n' "${skipped[@]}" | tee -a "$log"
fi
if [ ${#failed[@]} -gt 0 ]; then
  printf '  FAILED: %s\n' "${failed[@]}" | tee -a "$log"
  receipt finish >>"$log" || exit 1
  exit 1
fi
eligibility="$(receipt finish)" || exit 1
if [ "$eligibility" = eligible ]; then
  echo "RELEASE GATE PASSED (clean complete source; package and verify artifacts next)" | tee -a "$log"
else
  echo "INCOMPLETE; NOT ELIGIBLE FOR RELEASE (receipt: $RELEASE_GATE_RECEIPT)" | tee -a "$log"
  exit 2
fi
