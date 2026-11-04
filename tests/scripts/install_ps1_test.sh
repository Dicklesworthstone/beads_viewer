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
# Usage: tests/scripts/install_ps1_test.sh   (needs pwsh on PATH or PWSH=/path/to/pwsh, and python3)
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
pwsh="${PWSH:-pwsh}"
command -v "$pwsh" >/dev/null || { echo "install_ps1_test: pwsh not found (set PWSH=/path/to/pwsh)"; exit 2; }
command -v python3 >/dev/null || { echo "install_ps1_test: python3 is required"; exit 2; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-install-ps1-test.XXXXXX")"
server_pid=""
cleanup() {
  [ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null
  printf 'install_ps1_test: retained artifacts at %s\n' "$tmp"
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

# Serve the fake release on a free loopback port.
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
( cd "$site" && python3 -m http.server "$port" --bind 127.0.0.1 >"$tmp/server.log" 2>&1 ) &
server_pid=$!
for _ in $(seq 1 50); do
  if curl -s -o /dev/null "http://127.0.0.1:$port/api/releases/latest"; then break; fi
  sleep 0.1
done

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
param([string]$Installer, [string]$Binary)
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
Assert-BinaryVersion -Binary $Binary -Tag 'v0.0.1'
Write-Output 'version accepted'
''')

cases = [
    ("version-success", "print('bv v0.0.1', flush=True)", 0, ["version accepted"], False),
    ("version-mismatch", "print('bv v0.0.2', flush=True)", 1, ["expected v0.0.1"], False),
    ("version-nonzero", "print('nonzero diagnostic', file=sys.stderr, flush=True)\nsys.exit(7)", 1,
     ["exited with code 7", "nonzero diagnostic"], False),
    ("version-oversized", "print('bv v0.0.1' + ' ' * 65536, flush=True)", 1,
     ["Unexpected --version output"], False),
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
]
failed = False
for name, body, expected, markers, timed_out in cases:
    candidate = scratch / name
    candidate.write_text('#!/usr/bin/env python3\nimport os, subprocess, sys, time\nfrom pathlib import Path\n'
                         "Path(__file__ + '.pid').write_text(str(os.getpid()))\n"
                         "if sys.argv[1:] != ['--version']: sys.exit(91)\n" + body + '\n')
    candidate.chmod(0o700)
    started = time.monotonic()
    process = subprocess.Popen([pwsh, '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(probe),
                                '-Installer', installer, '-Binary', str(candidate)],
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
    if timed_out and elapsed < 10:
        errors.append(f'returned before original 10-second deadline: {elapsed:.3f}s')
    if not timed_out and elapsed >= 5:
        errors.append(f'exited child blocked for {elapsed:.3f}s')
    if errors:
        failed = True
        print(f'FAIL: {name}: {"; ".join(errors)}', flush=True)
        print(output, flush=True)
    else:
        print(f'ok: {name} (exit {process.returncode}, {elapsed:.3f}s, '
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
