#!/usr/bin/env bash
# install_ps1_test.sh: proves install.ps1 fails closed. It builds a fake
# release (a zip carrying bv.exe, checksums.txt, and the two GitHub API
# responses the script reads), serves it from a local HTTP server, and runs
# the installer under pwsh with BV_INSTALL_API_URL / BV_INSTALL_DOWNLOAD_URL
# pointed at that server:
#   1. happy path installs bv.exe into -InstallDir and exits 0;
#   2. a tampered checksums.txt aborts with exit 1 and installs nothing;
#   3. a release without checksums.txt aborts with exit 1.
# It also extracts the production version-check function and runs real local
# processes to check its deadline and diagnostics. This is portable function
# coverage, not native Windows executable or source-install first-start proof.
# Usage:
#   tests/scripts/install_ps1_test.sh                         (needs pwsh on PATH or PWSH=/path/to/pwsh, and python3)
#   tests/scripts/install_ps1_test.sh --server-lifecycle-only (verifies HTTP server PID tracking + cleanup teardown without pwsh)
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
mode="full"
lifecycle_record_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --server-lifecycle-only)
      mode="lifecycle-parent"
      ;;
    --_server-lifecycle-child)
      [ "$#" -ge 2 ] || { echo "install_ps1_test: missing record file argument" >&2; exit 2; }
      mode="lifecycle-child"
      lifecycle_record_file="$2"
      shift
      ;;
    *)
      echo "install_ps1_test: unsupported argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

command -v python3 >/dev/null || { echo "install_ps1_test: python3 is required"; exit 2; }

is_loopback_port_closed() {
  local check_port="$1"
  python3 - "$check_port" <<'PY'
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(0.1)
try:
    rc = s.connect_ex(("127.0.0.1", int(sys.argv[1])))
finally:
    s.close()
sys.exit(0 if rc != 0 else 1)
PY
}

if [ "$mode" = "lifecycle-parent" ]; then
  parent_tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-install-ps1-lifecycle.XXXXXX")"
  trap 'rm -rf "$parent_tmp"' EXIT
  record_file="$parent_tmp/server-record.txt"
  bash "$0" --_server-lifecycle-child "$record_file"
  [ -f "$record_file" ] || { echo "FAIL: install_ps1_test: child did not write server record" >&2; exit 1; }
  read -r recorded_pid recorded_port recorded_comm < "$record_file"
  [ -n "$recorded_pid" ] && [ -n "$recorded_port" ] || { echo "FAIL: install_ps1_test: incomplete server record" >&2; exit 1; }
  case "$recorded_comm" in
    python*) ;;
    *) echo "FAIL: install_ps1_test: server_pid $recorded_pid command was '$recorded_comm', expected python3" >&2; exit 1 ;;
  esac
  if kill -0 "$recorded_pid" 2>/dev/null; then
    echo "FAIL: install_ps1_test: server_pid $recorded_pid survived child exit" >&2
    exit 1
  fi
  if ! is_loopback_port_closed "$recorded_port"; then
    echo "FAIL: install_ps1_test: port 127.0.0.1:$recorded_port remained open after child exit" >&2
    exit 1
  fi
  echo "install_ps1_test: PASS (--server-lifecycle-only: pid=$recorded_pid comm=$recorded_comm port=$recorded_port torn down cleanly)"
  exit 0
fi

pwsh="${PWSH:-pwsh}"
if [ "$mode" = "full" ]; then
  command -v "$pwsh" >/dev/null || { echo "install_ps1_test: pwsh not found (set PWSH=/path/to/pwsh)"; exit 2; }
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-install-ps1-test.XXXXXX")"
port=""
server_pid=""
# shellcheck disable=SC2329 # Invoked via trap cleanup EXIT below.
cleanup() {
  local exit_rc=$?
  local teardown_failed=0
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      if ! kill -0 "$server_pid" 2>/dev/null; then
        break
      fi
      sleep 0.05
    done
    if kill -0 "$server_pid" 2>/dev/null; then
      kill -9 "$server_pid" 2>/dev/null || true
      wait "$server_pid" 2>/dev/null || true
    fi
    if kill -0 "$server_pid" 2>/dev/null; then
      echo "FAIL: install_ps1_test: server process $server_pid still running after cleanup" >&2
      teardown_failed=1
    fi
  fi
  if [ -n "$port" ]; then
    local closed=0
    for _ in $(seq 1 50); do
      if is_loopback_port_closed "$port"; then
        closed=1
        break
      fi
      sleep 0.05
    done
    if [ "$closed" -ne 1 ]; then
      echo "FAIL: install_ps1_test: loopback port 127.0.0.1:$port still open after cleanup" >&2
      teardown_failed=1
    fi
  fi
  if [ "$mode" = "lifecycle-child" ]; then
    rm -rf "$tmp"
  else
    printf 'install_ps1_test: retained artifacts at %s\n' "$tmp"
  fi
  if [ "$teardown_failed" -ne 0 ] && [ "$exit_rc" -eq 0 ]; then
    exit 1
  fi
  exit "$exit_rc"
}
trap cleanup EXIT

tag="v0.0.1"
asset="bv_0.0.1_windows_amd64.zip"
site="$tmp/site"
mkdir -p "$site/api/releases/tags" "$site/dl/$tag" "$site/dl-nochecksums/$tag"

# The "binary": a marker file named bv.exe (never executed off Windows).
python3 - "$site/dl/$tag/$asset" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("bv.exe", "fake bv binary for install.ps1 test\n")
PY
sha="$(sha256sum "$site/dl/$tag/$asset" | cut -d' ' -f1)"
printf '%s  %s\n' "$sha" "$asset" > "$site/dl/$tag/checksums.txt"
cp "$site/dl/$tag/$asset" "$site/dl-nochecksums/$tag/$asset"

cat > "$site/api/releases/latest" <<EOF
{"tag_name": "$tag", "assets": [{"name": "$asset"}, {"name": "checksums.txt"}]}
EOF
cp "$site/api/releases/latest" "$site/api/releases/tags/$tag"
mkdir -p "$site/api-nochecksums/releases/tags"
cat > "$site/api-nochecksums/releases/tags/$tag" <<EOF
{"tag_name": "$tag", "assets": [{"name": "$asset"}]}
EOF

# Serve the fake release on a free loopback port directly (no subshell so $! is python3).
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$site" >"$tmp/server.log" 2>&1 &
server_pid=$!
server_ready=0
for _ in $(seq 1 50); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$port/api/releases/latest" 2>/dev/null; then
    server_ready=1
    break
  fi
  sleep 0.1
done
[ "$server_ready" -eq 1 ] || { echo "FAIL: install_ps1_test: HTTP server failed to bind 127.0.0.1:$port" >&2; exit 1; }

if [ "$mode" = "lifecycle-child" ]; then
  kill -0 "$server_pid" 2>/dev/null || { echo "FAIL: install_ps1_test: server_pid $server_pid not alive" >&2; exit 1; }
  server_comm="$(ps -p "$server_pid" -o comm= | tr -d '[:space:]')"
  payload="$(curl -fsS "http://127.0.0.1:$port/api/releases/latest")"
  [[ "$payload" == *"\"tag_name\": \"$tag\""* ]] || { echo "FAIL: install_ps1_test: unexpected payload: $payload" >&2; exit 1; }
  printf '%s %s %s\n' "$server_pid" "$port" "$server_comm" > "$lifecycle_record_file"
  exit 0
fi

fail=0
run_installer() {
  # run_installer <name> <expected-exit> <api-path> <download-path> <install-dir>
  local name="$1" want="$2" api="$3" dl="$4" dir="$5" rc=0
  set +e
  out="$(BV_INSTALL_API_URL="http://127.0.0.1:$port/$api" BV_INSTALL_DOWNLOAD_URL="http://127.0.0.1:$port/$dl" \
    "$pwsh" -NoLogo -NonInteractive -File "$root/install.ps1" -InstallDir "$dir" -NoPathUpdate 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL: $name exited $rc, want $want"; echo "$out" | sed 's/^/    /' | tail -8; fail=1
  else
    echo "ok: $name (exit $rc)"
  fi
}

# 1. Happy path.
run_installer "happy path installs" 0 api dl "$tmp/install-ok"
if [ ! -f "$tmp/install-ok/bv.exe" ]; then echo "FAIL: bv.exe missing after happy path"; fail=1; fi

# 2. Tampered checksum: flip the recorded hash.
printf '%s  %s\n' "$(echo "$sha" | tr '0-9a-f' '1-9a-f0')" "$asset" > "$site/dl/$tag/checksums.txt"
run_installer "tampered checksum aborts" 1 api dl "$tmp/install-tampered"
if [ -e "$tmp/install-tampered/bv.exe" ]; then echo "FAIL: tampered archive was installed"; fail=1; fi
printf '%s  %s\n' "$sha" "$asset" > "$site/dl/$tag/checksums.txt"

# 3. Release without checksums.txt.
run_installer "missing checksums.txt aborts" 1 api-nochecksums dl-nochecksums "$tmp/install-nochecksums"
if [ -e "$tmp/install-nochecksums/bv.exe" ]; then echo "FAIL: unverified archive was installed"; fail=1; fi

# 4. Explicit -Version pins the tag (no latest lookup needed).
set +e
out="$(BV_INSTALL_API_URL="http://127.0.0.1:$port/api" BV_INSTALL_DOWNLOAD_URL="http://127.0.0.1:$port/dl" \
  "$pwsh" -NoLogo -NonInteractive -File "$root/install.ps1" -Version 0.0.1 -InstallDir "$tmp/install-pinned" -NoPathUpdate 2>&1)"
rc=$?
set -e
if [ "$rc" -eq 0 ] && [ -f "$tmp/install-pinned/bv.exe" ]; then echo "ok: -Version 0.0.1 installs (exit 0)"; else echo "FAIL: -Version path exited $rc"; echo "$out" | tail -5; fail=1; fi

# 5. Exercise the actual function without running the rest of the installer.
# Only the platform guard is overridden: Fail and Assert-BinaryVersion are
# extracted unchanged, and every candidate is a real process with real pipes.
if ! python3 - "$pwsh" "$root/install.ps1" "$tmp" <<'PY'
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

pwsh, installer, scratch = sys.argv[1:]
scratch = Path(scratch)
probe = scratch / "version-probe.ps1"
probe.write_text(r'''
param([string]$Installer, [string]$Binary, [string]$TimingFile)
$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($Installer, [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw "Installer parse errors: $errors" }
foreach ($name in @('Fail', 'Assert-BinaryVersion')) {
    $definition = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }, $true)
    if ($null -eq $definition) { throw "Missing production function: $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}
function Test-IsWindowsHost { return $true }
$verification = [System.Diagnostics.Stopwatch]::StartNew()
try {
    Assert-BinaryVersion -Binary $Binary -Tag 'v0.0.1'
} finally {
    $verification.Stop()
    [IO.File]::WriteAllText($TimingFile, $verification.Elapsed.TotalSeconds.ToString('R', [Globalization.CultureInfo]::InvariantCulture))
}
Write-Output 'version accepted'
''')

def operation_timing_errors(elapsed, timed_out):
    if timed_out and elapsed < 10:
        return [f'returned before original 10-second deadline: {elapsed:.3f}s']
    if not timed_out and elapsed >= 5:
        return [f'exited child blocked for {elapsed:.3f}s']
    return []

cases = [
    ("version-success", "print('bv v0.0.1', flush=True)", 0, ["version accepted"], False),
    ("version-mismatch", "print('bv v0.0.2', flush=True)", 1, ["expected v0.0.1"], False),
    ("version-nonzero", "print('nonzero diagnostic', file=sys.stderr, flush=True)\nsys.exit(7)", 1,
     ["exited with code 7", "nonzero diagnostic"], False),
    ("version-oversized", "print('bv v0.0.1' + ' ' * 65536, flush=True)", 1,
     ["Unexpected --version output"], False),
    # Sleeping between 1024-character chunks cannot drain this finite burst
    # within the execution/drain deadlines. Ready reads must make progress.
    ("version-stderr-burst", "print('x' * (1024 * 1024), file=sys.stderr, flush=True)\n"
     "print('bv v0.0.1', flush=True)", 0, ["version accepted"], False),
    ("timeout-diagnostics", "print('timeout stdout marker', flush=True)\n"
     "print('timeout stderr marker', file=sys.stderr, flush=True)\ntime.sleep(30)", 1,
     ["timed out", "timeout stdout marker", "timeout stderr marker"], True),
    ("timeout-capped", "print('capped stdout marker ' + 'x' * 65536, flush=True)\n"
     "print('capped stderr marker ' + 'y' * 65536, file=sys.stderr, flush=True)\ntime.sleep(30)", 1,
     ["timed out", "capped stdout marker", "capped stderr marker", "truncated"], True),
    ("timeout-inherited-pipes", "subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])\n"
     "print('inherited stdout marker', flush=True)\n"
     "print('inherited stderr marker', file=sys.stderr, flush=True)\ntime.sleep(30)", 1,
     ["timed out", "inherited stdout marker", "inherited stderr marker", "incomplete"], True),
    ("exited-inherited-pipes", "subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])\n"
     "print('bv v0.0.1', flush=True)\nprint('exited stderr marker', file=sys.stderr, flush=True)", 1,
     ["incomplete", "bv v0.0.1", "exited stderr marker"], False),
    # A real slow operation must still fail the unchanged five-second check,
    # even though PowerShell startup and AST extraction are measured separately.
    ("slow-version-timing-control", "time.sleep(6)\nprint('bv v0.0.1', flush=True)", 0,
     ["version accepted"], False),
]
failed = False
for name, body, expected, markers, timed_out in cases:
    candidate = scratch / name
    candidate.write_text('#!/usr/bin/env python3\nimport os, subprocess, sys, time\nfrom pathlib import Path\n'
                         "Path(__file__ + '.pid').write_text(str(os.getpid()))\n"
                         "if sys.argv[1:] != ['--version']: sys.exit(91)\n" + body + '\n')
    candidate.chmod(0o700)
    timing_file = scratch / (name + '.seconds')
    started = time.monotonic()
    process = subprocess.Popen([pwsh, '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(probe),
                                '-Installer', installer, '-Binary', str(candidate), '-TimingFile', str(timing_file)],
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    outer_timeout = False
    child_still_running = False
    try:
        stdout, stderr = process.communicate(timeout=15)
    except subprocess.TimeoutExpired:
        outer_timeout = True
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
    finally:
        # Check before cleanup so the group kill cannot hide a failed installer
        # kill. The direct child must already be gone when verification returns.
        pid_file = scratch / (name + '.pid')
        if pid_file.exists():
            try:
                child_still_running = os.getpgid(int(pid_file.read_text())) == process.pid
            except ProcessLookupError:
                pass
        # Only this case's process group; descendants intentionally hold pipes.
        # Artifacts are retained, and no unrelated processes are signalled.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    elapsed = time.monotonic() - started
    (scratch / (name + '.stdout')).write_bytes(stdout)
    (scratch / (name + '.stderr')).write_bytes(stderr)
    output = (stdout + stderr).decode(errors='replace')
    errors = []
    verification_elapsed = None
    try:
        verification_elapsed = float(timing_file.read_text())
        if not math.isfinite(verification_elapsed) or not 0 <= verification_elapsed <= elapsed:
            raise ValueError('verification interval is outside the process lifetime')
    except (OSError, ValueError) as error:
        errors.append(f'missing or invalid function timing: {error}')
    if outer_timeout:
        errors.append('exceeded 15-second outer guard')
    if child_still_running:
        errors.append('version-check child still exists before test cleanup')
    if process.returncode != expected:
        errors.append(f'exit {process.returncode}, expected {expected}')
    for marker in markers:
        if marker not in output:
            errors.append(f'missing {marker!r}')
    if len(stdout) + len(stderr) > 10000:
        errors.append('diagnostic output exceeds 10000 bytes')
    # Include child launch, execution, pipe draining and disposal in the same
    # original limits, excluding only PowerShell startup and AST extraction.
    # The fifteen-second outer guard above still bounds the complete process.
    timing_errors = [] if verification_elapsed is None else operation_timing_errors(verification_elapsed, timed_out)
    if name == 'slow-version-timing-control':
        if not timing_errors:
            errors.append('five-second check accepted the deliberately slow operation')
    else:
        errors.extend(timing_errors)
    if errors:
        failed = True
        print(f'FAIL: {name}: {"; ".join(errors)}', flush=True)
        print(output, flush=True)
    elif name == 'slow-version-timing-control':
        print(f'ok: {name} (five-second check rejects {verification_elapsed:.3f}s operation, '
              f'{elapsed:.3f}s overall)', flush=True)
    else:
        print(f'ok: {name} (exit {process.returncode}, {verification_elapsed:.3f}s function, {elapsed:.3f}s overall, '
              f'{len(stdout) + len(stderr)} diagnostic bytes)', flush=True)

# Exercise the same toolchain policy and metadata check used before the source
# installer promotes its executable. This does not claim a native Windows build.
toolchain_probe = scratch / "toolchain-probe.ps1"
toolchain_probe.write_text(r'''
param([string]$Installer, [string]$ModuleFile, [string]$BuildVersion)
$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($Installer, [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw "Installer parse errors: $errors" }
$minimumAssignments = @($ast.FindAll({ param($node)
    $node -is [System.Management.Automation.Language.AssignmentStatementAst] -and
    $node.Left -is [System.Management.Automation.Language.VariableExpressionAst] -and
    $node.Left.VariablePath.UserPath -ceq 'MIN_GO_VERSION'
}, $false))
if ($minimumAssignments.Count -ne 1) { throw 'Installer must declare one MIN_GO_VERSION assignment' }
. ([scriptblock]::Create($minimumAssignments[0].Extent.Text))
foreach ($name in @('Fail', 'Write-Info', 'Test-GoVersion', 'Get-SourceToolchain', 'Assert-SourceToolchain')) {
    $definition = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }, $true)
    if ($null -eq $definition) { throw "Missing production function: $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}
$expected = Get-SourceToolchain $ModuleFile
Write-Output "selected=$expected"
Assert-SourceToolchain @("C:\path with spaces\bv.exe: $BuildVersion") $expected
''')
toolchain_cases = [
    ('canonical-go', 'go 1.26.10\n', 'go1.26.10', 0, 'selected=go1.26.10'),
    ('explicit-pin', 'go 1.26.0\ntoolchain go1.26.10\n', 'go1.26.10', 0, 'selected=go1.26.10'),
    ('older-tag', 'go 1.26.0\ntoolchain go1.26.8\n', 'go1.26.10', 0, 'selected=go1.26.10'),
    ('older-language', 'go 1.25\n', 'go1.26.10', 0, 'selected=go1.26.10'),
    ('newer-vulnerable-pin', 'go 1.27.0\ntoolchain go1.27.2\n', 'go1.27.3', 0, 'selected=go1.27.3'),
    ('old-built-compiler', 'go 1.26.10\n', 'go1.26.8', 1, 'expected go1.26.10'),
    ('newer-vulnerable-compiler', 'go 1.26.10\n', 'go1.27.2', 1, 'expected go1.26.10'),
    ('unknown-built-compiler', 'go 1.26.10\n', 'unknown', 1, 'expected go1.26.10'),
    ('no-declaration', 'module example.test/missing\n', 'go1.26.10', 1, 'stable Go version'),
    ('development-compiler', 'go 1.26.0\ntoolchain go1.27rc1\n', 'go1.27rc1', 1, 'stable Go version'),
]
for name, module, built_version, expected, marker in toolchain_cases:
    module_path = scratch / f'{name}.mod'
    module_path.write_text(module)
    result = subprocess.run([pwsh, '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(toolchain_probe),
                             '-Installer', installer, '-ModuleFile', str(module_path),
                             '-BuildVersion', built_version], capture_output=True, text=True, timeout=15)
    output = result.stdout + result.stderr
    if result.returncode != expected or marker not in output:
        failed = True
        print(f'FAIL: toolchain-{name}: exit={result.returncode}, expected={expected}\n{output}', flush=True)
    else:
        print(f'ok: toolchain-{name} (exit {result.returncode})', flush=True)

# Deliberately lower the production constant in an external fixture. The probe
# must observe that value instead of substituting its own safe constant. This
# copy is parsed only; it is never executed as an installer.
installer_text = Path(installer).read_text()
floor_assignment = '$MIN_GO_VERSION = "1.26.10"'
if installer_text.count(floor_assignment) != 1:
    raise SystemExit('Cannot construct the single-assignment source-floor control')
old_floor_installer = scratch / 'installer-old-floor.ps1'
old_floor_installer.write_text(installer_text.replace(floor_assignment, '$MIN_GO_VERSION = "1.26.8"', 1))
old_tag_module = scratch / 'source-floor-control.mod'
old_tag_module.write_text('go 1.26.0\ntoolchain go1.26.8\n')
result = subprocess.run([pwsh, '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(toolchain_probe),
                         '-Installer', str(old_floor_installer), '-ModuleFile', str(old_tag_module),
                         '-BuildVersion', 'go1.26.10'], capture_output=True, text=True, timeout=15)
output = result.stdout + result.stderr
if result.returncode != 1 or 'selected=go1.26.8' not in output or 'expected go1.26.8' not in output:
    failed = True
    print(f'FAIL: source-floor probe ignored the production assignment: exit={result.returncode}\n{output}', flush=True)
else:
    print('ok: source-floor probe detects a controlled production downgrade', flush=True)
sys.exit(1 if failed else 0)
PY
then
  fail=1
fi

if [ "$fail" -eq 0 ]; then
  echo "install_ps1_test: PASS (archive fixtures and portable version/toolchain checks; no native Windows proof)"
fi
exit "$fail"
