#!/usr/bin/env bash
# release_gate.sh: the single pre-release check. Every stage must pass; the
# gate writes its log and receipt outside the source checkout. Only a clean,
# complete receipt permits packaging; diagnostic skips never authorize release.
# Usage: release_gate.sh [run|verify-toolchain|audit|verify-source|verify-binary PATH|package|seal DIR|verify]
# Set RELEASE_GATE_RECEIPT to the receipt printed by `run` for later commands.
#
# Stages:
#   1 gofmt            gofmt -l over repo Go files (vendor excluded) is empty
#   2 build+vet        go build ./... && go vet ./...
#   3 unit tests       go test ./... -race -count=1 (pkg/, cmd/, internal/)
#   4 e2e tests        go test ./tests/e2e -race -count=1 -timeout=30m
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
#  11 vulnerabilities  pinned source-mode govulncheck for every release target
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
export GOWORK=off CGO_ENABLED=0 GOTOOLCHAIN=local

# JSON, content hashing, and archive inspection live here rather than in a
# second gate implementation. This code only reads source and writes evidence
# outside it. It never executes a packaged binary or extracts archive paths.
receipt() {
  python3 - "$root" "${RELEASE_GATE_RECEIPT:-}" "$@" <<'PY'
import hashlib, json, os, pathlib, re, stat, subprocess, sys, tarfile, tempfile, zipfile
from datetime import datetime, timezone

root = pathlib.Path(sys.argv[1]).resolve()
receipt_path = pathlib.Path(sys.argv[2]).resolve() if sys.argv[2] else None
mode, *args = sys.argv[3:]
stages = ["gofmt", "build+vet", "unit-tests", "e2e-tests", "docs-parity",
          "action-pins", "vendor-hashes", "benchmarks", "robot-smoke", "script-tests",
          "source-vulnerabilities"]
release_targets = {("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"),
                   ("darwin", "arm64"), ("windows", "amd64")}

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

def stable_version(version, prefix):
    match = re.fullmatch(re.escape(prefix) + r"(\d+)\.(\d+)\.(\d+)", version)
    if not match:
        fail(f"expected a stable {prefix} version, got {version!r}")
    return tuple(map(int, match.groups()))

def validate_go_flags(flags):
    # Overlays, alternate module files, tool wrappers and profiles can supply
    # inputs outside the source digest. Keep build settings in tracked config;
    # only compilation concurrency may be adjusted through GOFLAGS.
    if not re.fullmatch(r"(?:-p=[1-9][0-9]*)?", flags.strip()):
        fail("unsupported release GOFLAGS: only empty flags or a single -p=N are allowed")

def module_policy():
    validate_go_flags(os.environ.get("GOFLAGS", ""))
    # Empty caller GOFLAGS can fall back to settings persisted in GOENV.
    # Validate those before any module command can consume external inputs.
    validate_go_flags(run("go", "env", "GOFLAGS"))
    module = json.loads(run("go", "mod", "edit", "-json", str(root / "go.mod")))
    # `go mod tidy` removes a toolchain directive equal to the go directive.
    # Both canonical forms select the exact compiler; neither is a floating
    # major/minor version or permission to auto-upgrade the release toolchain.
    intended = module.get("Toolchain") or "go" + module.get("Go", "")
    version = stable_version(intended, "go")
    # GO-2026-6809 was also present in 1.27.0--1.27.2: an unconditional
    # >=1.26.10 comparison would accidentally permit those vulnerable releases.
    if version < (1, 26, 10) or (version[:2] == (1, 27) and version < (1, 27, 3)):
        fail(f"go.mod toolchain is below the security floor: {intended}")
    dependencies = {row["Path"]: row["Version"] for row in module.get("Require") or []}
    net_version = dependencies.get("golang.org/x/net", "")
    if stable_version(net_version, "v") < (0, 62, 0):
        fail(f"golang.org/x/net is below the security floor: {net_version}")
    replacements = {row["Old"]["Path"]: row["New"] for row in module.get("Replace") or []}
    if "golang.org/x/net" in replacements:
        fail("golang.org/x/net must use its verified upstream module, not a replacement")
    # Go validates the complete vendor/modules.txt declaration set before
    # listing a module, so a stale vendored version cannot pass this preflight.
    selected = json.loads(run("go", "list", "-mod=vendor", "-m", "-json", "golang.org/x/net"))
    if selected.get("Version") != net_version or selected.get("Replace"):
        fail("selected golang.org/x/net does not match go.mod")
    return {"toolchain": intended, "dependencies": dependencies, "replacements": replacements}

def toolchain():
    validate_go_flags(os.environ.get("GOFLAGS", ""))
    actual = json.loads(run("go", "env", "-json", "GOVERSION", "GOHOSTOS", "GOHOSTARCH",
                            "GOEXPERIMENT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "CGO_ENABLED"))
    validate_go_flags(actual["GOFLAGS"])
    intended = module_policy()["toolchain"]
    if actual["GOVERSION"] != intended or actual["GOTOOLCHAIN"] != "local":
        fail(f"release requires {intended} with GOTOOLCHAIN=local; selected {actual['GOVERSION']}")
    return actual

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

def scanner_info(path):
    path = outside(path)
    sha = digest(path)
    output = run("go", "version", "-m", str(path))
    lines = [line.strip().split() for line in output.splitlines()]
    command = [row[1] for row in lines if len(row) == 2 and row[0] == "path"]
    modules = [row for row in lines if row and row[0] == "mod"]
    if (command != ["golang.org/x/vuln/cmd/govulncheck"] or len(modules) != 1 or
            modules[0][1:3] != ["golang.org/x/vuln", "v1.9.0"] or
            any(row and row[0] == "=>" for row in lines)):
        fail(f"govulncheck executable must identify upstream x/vuln v1.9.0 without replacement: {path}")
    first = output.splitlines()[0] if output else ""
    prefix = str(path) + ": "
    if not first.startswith(prefix):
        fail(f"govulncheck executable has no Go compiler metadata: {path}")
    compiler = first[len(prefix):]
    version = stable_version(compiler, "go")
    if version < (1, 26, 10) or (version[:2] == (1, 27) and version < (1, 27, 3)):
        fail(f"govulncheck compiler is below the security floor: {compiler}")
    if digest(path) != sha:
        fail("govulncheck executable changed during metadata inspection")
    return {"path": str(path), "sha256": sha, "build_info": output,
            "module_version": "v1.9.0", "go_version": compiler}

def verify_scanner(record):
    if scanner_info(record["path"]) != record:
        fail("govulncheck executable changed after audit binding")

def audit_source():
    actual = toolchain()
    parent = outside(os.environ.get("RELEASE_GATE_OUTPUT_DIR", "/tmp"))
    output = pathlib.Path(tempfile.mkdtemp(prefix="bv-source-audit-", dir=parent))
    tool_dir = output / "tools"
    tool_dir.mkdir()
    env = dict(os.environ, GOOS=actual["GOHOSTOS"], GOARCH=actual["GOHOSTARCH"],
               GOFLAGS=actual["GOFLAGS"], GOBIN=str(tool_dir), GOTOOLCHAIN="local", GOWORK="off")
    install_log = output / "install.log"
    install_command = ["go", "install", "golang.org/x/vuln/cmd/govulncheck@v1.9.0"]
    with open(install_log, "wb") as log:
        installed = subprocess.run(install_command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
    if installed.returncode:
        fail(f"could not install pinned govulncheck (exit {installed.returncode}); see {install_log}")
    scanner = tool_dir / ("govulncheck.exe" if actual["GOHOSTOS"] == "windows" else "govulncheck")
    result = {"started_at": datetime.now(timezone.utc).isoformat(),
              "tool": scanner_info(scanner),
              "database": "https://vuln.go.dev", "mode": "source", "format": "text", "scans": []}
    for goos, goarch in sorted(release_targets):
        target_log = output / f"govulncheck-{goos}-{goarch}.log"
        command = [str(scanner), "-db=https://vuln.go.dev", "-mode=source", "-show=version", "./..."]
        # govulncheck accepts GOVERSION as an override for standard-library
        # analysis. Bind it to the selected compiler, never the caller's env.
        scan_env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0",
                        GOTOOLCHAIN="local", GOWORK="off", GOVERSION=actual["GOVERSION"],
                        GOFLAGS=actual["GOFLAGS"])
        with open(target_log, "wb") as log:
            scanned = subprocess.run(command, cwd=root, env=scan_env, stdout=log, stderr=subprocess.STDOUT)
        verify_scanner(result["tool"])
        result["scans"].append({"goos": goos, "goarch": goarch, "exit_code": scanned.returncode,
                                "go_version": scan_env["GOVERSION"], "cgo_enabled": scan_env["CGO_ENABLED"],
                                "go_flags": scan_env["GOFLAGS"],
                                "command": command, "path": str(target_log), "sha256": digest(target_log)})
        print(f"source vulnerability scan {goos}/{goarch}: exit={scanned.returncode}; {target_log}", flush=True)
    # Text output preserves govulncheck's nonzero exit status for reachable
    # findings. JSON/SARIF/OpenVEX output alone is not a blocking check.
    result["finished_at"] = datetime.now(timezone.utc).isoformat()
    report = output / "audit.json"
    with open(report, "w") as f:
        json.dump(result, f, indent=2, sort_keys=True)
        f.write("\n")
    if receipt_path is not None:
        value = load()
        value["source_audit"] = result
        save(value)
    if any(row["exit_code"] != 0 for row in result["scans"]):
        fail(f"source vulnerability analysis failed; see {report}")
    print(f"source vulnerability analysis passed for all release targets: {report}")

def verify_audit(value):
    audit = value.get("source_audit", {})
    rows = audit.get("scans", [])
    if (audit.get("mode") != "source" or audit.get("format") != "text" or
            not audit.get("started_at") or not audit.get("finished_at") or
            audit.get("database") != "https://vuln.go.dev" or len(rows) != len(release_targets) or
            {(row.get("goos"), row.get("goarch")) for row in rows} != release_targets or
            any(row.get("exit_code") != 0 for row in rows)):
        fail("missing or unsuccessful source vulnerability evidence for release targets")
    if any(row.get("go_version") != value["toolchain"]["GOVERSION"] or
            row.get("cgo_enabled") != value["toolchain"]["CGO_ENABLED"] or
            row.get("go_flags") != value["toolchain"]["GOFLAGS"] for row in rows):
        fail("source vulnerability analysis environment differs from gate receipt")
    verify_scanner(audit["tool"])
    for record in [audit["tool"], *rows]:
        if digest(outside(record["path"])) != record["sha256"]:
            fail(f"source vulnerability evidence changed: {record['path']}")
    for row in rows:
        # -show=version retains text-mode failure semantics and reports the
        # version the scanner actually used, unlike the early-exit -version.
        if f"Go: {row['go_version']}" not in pathlib.Path(row["path"]).read_text().splitlines():
            fail(f"source vulnerability scanner Go version differs from gate receipt: {row['path']}")

def audit_binary(path, info, value):
    # Reuse the pinned, independently inspected scanner installed by the source
    # audit. Scan the bytes taken from each final archive, including cross-built
    # executables; none of those executables is run on the host.
    scanner = value["source_audit"]["tool"]
    verify_scanner(scanner)
    command = [scanner["path"], "-db=https://vuln.go.dev", "-mode=binary", "-show=version", str(path)]
    actual = value["toolchain"]
    env = dict(os.environ, GOOS=info["settings"]["GOOS"], GOARCH=info["settings"]["GOARCH"],
               CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off",
               GOVERSION=info["go_version"], GOFLAGS=actual["GOFLAGS"])
    record = {"mode": "binary", "format": "text", "database": "https://vuln.go.dev",
              "started_at": datetime.now(timezone.utc).isoformat(), "tool": scanner,
              "command": command, "binary_sha256": info["sha256"],
              "goos": env["GOOS"], "goarch": env["GOARCH"], "go_version": env["GOVERSION"],
              "cgo_enabled": env["CGO_ENABLED"], "go_flags": env["GOFLAGS"]}
    descriptor, log_path = tempfile.mkstemp(prefix=f"bv-binary-audit-{env['GOOS']}-{env['GOARCH']}-",
                                          suffix=".log", dir=outside(receipt_path.parent))
    with os.fdopen(descriptor, "wb") as log:
        log.write((json.dumps(record, sort_keys=True) + "\n").encode())
        log.flush()
        scanned = subprocess.run(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
        log.write(f"\ngovulncheck exit status: {scanned.returncode}\n".encode())
    record.update(exit_code=scanned.returncode, path=log_path, sha256=digest(log_path),
                  finished_at=datetime.now(timezone.utc).isoformat())
    print(f"binary vulnerability scan {env['GOOS']}/{env['GOARCH']}: exit={scanned.returncode}; {log_path}", flush=True)
    verify_scanner(scanner)
    if digest(path) != info["sha256"]:
        fail("archived binary changed during vulnerability analysis")
    # Linked vulnerable symbols conservatively block a release. Binary mode
    # cannot establish attacker reachability; its findings get no exemption.
    verify_binary_audit(record, info, value)
    return record

def verify_binary_audit(record, info, value):
    if (not record or record.get("mode") != "binary" or record.get("format") != "text" or
            record.get("database") != "https://vuln.go.dev" or record.get("exit_code") != 0 or
            not record.get("started_at") or not record.get("finished_at")):
        fail("missing or unsuccessful binary vulnerability evidence")
    if (record.get("binary_sha256") != info["sha256"] or
            record.get("go_version") != info["go_version"] or
            record.get("goos") != info["settings"]["GOOS"] or
            record.get("goarch") != info["settings"]["GOARCH"] or
            record.get("cgo_enabled") != value["toolchain"]["CGO_ENABLED"] or
            record.get("go_flags") != value["toolchain"]["GOFLAGS"] or
            record.get("tool") != value["source_audit"]["tool"]):
        fail("binary vulnerability evidence differs from archived binary or gate environment")
    if digest(outside(record["path"])) != record["sha256"]:
        fail(f"binary vulnerability evidence changed: {record['path']}")

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
    if value.get("module_policy") != module_policy():
        fail("Go dependency policy differs from gate receipt")
    verify_audit(value)
    if value["dataset"] != dataset():
        fail("benchmark dataset differs from gate receipt")
    config = value.get("packaging_config")
    if config and digest(outside(config["path"])) != config["sha256"]:
        fail("derived packaging config changed after receipt binding")

def binary_info(path, value):
    output = run("go", "version", "-m", str(path))
    settings = {}
    dependencies = {}
    dependency = None
    version = ""
    for line in output.splitlines():
        if line.startswith(str(path) + ": "):
            version = line[len(str(path)) + 2:]
        words = line.strip().split(None, 1)
        fields = line.strip().split()
        if len(fields) >= 3 and fields[0] == "dep":
            dependency = fields[1]
            dependencies[dependency] = {"version": fields[2]}
            if len(fields) > 3:
                dependencies[dependency]["sum"] = fields[3]
        elif len(fields) >= 2 and fields[0] == "=>" and dependency:
            dependencies[dependency]["replace"] = fields[1:]
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
    policy = value["module_policy"]
    if "golang.org/x/net" not in dependencies:
        fail(f"binary is missing golang.org/x/net build information: {path}")
    for name, metadata in dependencies.items():
        expected = policy["dependencies"].get(name)
        if expected is None or metadata["version"] != expected:
            fail(f"binary dependency mismatch: {path}: {name} {metadata['version']} != {expected}")
        replacement = policy["replacements"].get(name)
        if replacement:
            wanted = [replacement["Path"], replacement.get("Version") or "(devel)"]
            if metadata.get("replace", [])[:2] != wanted:
                fail(f"binary replacement mismatch: {path}: {name}")
        elif "replace" in metadata:
            fail(f"binary has undeclared dependency replacement: {path}: {name}")
    return {"sha256": digest(path), "go_version": version, "settings": settings,
            "dependencies": dependencies}

try:
    if mode == "check-output":
        outside(args[0])
    elif mode == "verify-toolchain":
        actual = toolchain()
        policy = module_policy()
        print(f"release toolchain verified: {actual['GOVERSION']}; golang.org/x/net {policy['dependencies']['golang.org/x/net']}")
    elif mode == "audit":
        audit_source()
    elif mode == "init":
        outside(receipt_path)
        if receipt_path.exists(): fail(f"refusing to reuse receipt: {receipt_path}")
        save({"schema": 1, "eligible": False, "created_at": datetime.now(timezone.utc).isoformat(),
              "source": source(), "toolchain": toolchain(), "module_policy": module_policy(), "dataset": dataset(),
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
        if value["eligible"]:
            verify_audit(value)
        save(value)
        print("eligible" if value["eligible"] else "diagnostic")
    elif mode == "verify-source":
        value = load(); verify_source(value)
        print(f"source verified: {value['source']['revision']} (receipt: {receipt_path})")
    elif mode == "verify-binary":
        value = load(); verify_source(value)
        path = outside(args[0]); info = binary_info(path, value)
        audit_binary(path, info, value)
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
        # A fresh packaging attempt supersedes earlier artifacts before any
        # post-build scan runs. A failed hook must not leave an old seal usable.
        value.pop("artifacts", None)
        value.pop("checksums", None)
        save(value)
        print(config)
    elif mode == "seal":
        value = load()
        # A failed reseal must not leave an earlier successful artifact seal
        # reusable. Keep the source-stage evidence, but consume the old seal.
        value["artifacts"] = []
        value.pop("checksums", None)
        save(value)
        verify_source(value)
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
            audit = audit_binary(binary, info, value)
            target = (info["settings"].get("GOOS"), info["settings"].get("GOARCH"))
            if target in targets: fail(f"duplicate release target: {target}")
            targets.add(target)
            records.append({"path": str(archive), "sha256": sha, "binary": info, "binary_audit": audit})
        required = release_targets
        if targets != required: fail(f"release target set mismatch: got {sorted(targets)}, want {sorted(required)}")
        verify_source(value)
        value["artifacts"] = records
        value["checksums"] = {"path": str(checksum_file), "sha256": digest(checksum_file)}
        save(value)
        print(f"sealed {len(records)} archives: {receipt_path}")
    elif mode == "verify":
        value = load(); verify_source(value)
        artifacts = value.get("artifacts", [])
        if len(artifacts) != len(release_targets) or "checksums" not in value:
            fail("receipt has no complete packaged archive evidence; run package/seal first")
        targets = set()
        for artifact in artifacts:
            info = artifact["binary"]
            verify_binary_audit(artifact.get("binary_audit"), info, value)
            targets.add((info["settings"]["GOOS"], info["settings"]["GOARCH"]))
        # Five rows can still omit a platform if a saved artifact is duplicated.
        if targets != release_targets:
            fail(f"release target set mismatch: got {sorted(targets)}, want {sorted(release_targets)}")
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
  verify-toolchain|verify-source|verify-binary|seal|verify) receipt "$@"; exit $? ;;
  audit) RELEASE_GATE_RECEIPT= receipt audit; exit $? ;;
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
  *) echo "usage: $0 [run|verify-toolchain|audit|verify-source|verify-binary PATH|package|seal DIR|verify]" >&2; exit 2 ;;
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

# The full suite includes 600 CLI search judgments across three dataset sizes.
# Budget the aggregate suite separately from per-test assertions and benchmarks.
e2e_tests() { CGO_ENABLED=1 go test ./tests/e2e -race -count=1 -timeout=30m; }

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
  # compares best-of-N sec/op with a fresh build of the baseline revision,
  # falling back to stored timings. Missing benchmarks or excess regression fail.
  [ -f benchmarks/baseline.txt ] || { echo "no benchmarks/baseline.txt (run scripts/benchmark.sh baseline)"; return 1; }
  BENCH_PCT="${RELEASE_GATE_BENCH_PCT:-20}" scripts/benchmark.sh compare
}

script_self_tests() {
  # The gate's own helpers have tests; a comparator that mis-parses a column
  # or an installer that stops failing closed must not pass silently.
  tests/scripts/benchmark_compare_test.sh || return 1
  # Fixture scanner responses are synthetic to test orchestration and failure
  # rejection; Go/GoReleaser packaging is real. Stage 11 runs the required real
  # project source vulnerability scans, including access to the real database.
  bash tests/scripts/release_gate_test.sh gate-mechanics || return 1
  local pwsh="${PWSH:-pwsh}"
  if command -v "$pwsh" >/dev/null 2>&1; then
    PWSH="$pwsh" tests/scripts/install_ps1_test.sh || return 1
  else
    echo "install_ps1_test skipped: pwsh not found (set PWSH=/path/to/pwsh to include it)"
    return 200
  fi
}

source_vulnerabilities() { receipt audit; }

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
stage 11 source-vulnerabilities source_vulnerabilities

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
