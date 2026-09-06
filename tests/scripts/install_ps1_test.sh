#!/usr/bin/env bash
# install_ps1_test.sh: proves install.ps1 fails closed. It builds a fake
# release (a zip carrying bv.exe, checksums.txt, and the two GitHub API
# responses the script reads), serves it from a local HTTP server, and runs
# the installer under pwsh with BV_INSTALL_API_URL / BV_INSTALL_DOWNLOAD_URL
# pointed at that server:
#   1. happy path installs bv.exe into -InstallDir and exits 0;
#   2. a tampered checksums.txt aborts with exit 1 and installs nothing;
#   3. a release without checksums.txt aborts with exit 1.
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

if [ "$fail" -eq 0 ]; then
  echo "install_ps1_test: PASS (verified install, tampered and unverified releases refused)"
fi
exit "$fail"
