#!/usr/bin/env bash
# Exercise the real shell installer's archive path with native released binaries.
# Downloads remain the caller's responsibility; require their independently
# verified SHA256 values. Local file transport supplies controlled failure cases.
# This harness does not replace the live release installation check.
# Usage: bash tests/scripts/install_sh_test.sh CURRENT_ARCHIVE CURRENT_SHA256 PREVIOUS_ARCHIVE PREVIOUS_SHA256
# INSTALLER_PATH can select a saved old installer for the negative control.
set -euo pipefail
if [ "$#" -ne 4 ]; then
    echo "usage: $0 CURRENT_ARCHIVE CURRENT_SHA256 PREVIOUS_ARCHIVE PREVIOUS_SHA256" >&2
    exit 2
fi
root="$(cd "$(dirname "$0")/../.." && pwd)"
installer="${INSTALLER_PATH:-$root/install.sh}"
current_archive="$1"
current_sha="$2"
previous_archive="$3"
previous_sha="$4"
current_tag="${CURRENT_TAG:-v0.23.0}"
previous_tag="${PREVIOUS_TAG:-v0.22.0}"
work="$(mktemp -d "${TMPDIR:-/tmp}/bv native shell tests.XXXXXX")"
echo "Evidence retained at $work"

hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    else shasum -a 256 "$1" | awk '{print $1}'; fi
}
for prerequisite in bash tar python3; do
    command -v "$prerequisite" >/dev/null || { echo "missing prerequisite: $prerequisite" >&2; exit 2; }
done
[ "$(hash_file "$current_archive")" = "$current_sha" ] || { echo 'current archive SHA256 mismatch' >&2; exit 1; }
[ "$(hash_file "$previous_archive")" = "$previous_sha" ] || { echo 'previous archive SHA256 mismatch' >&2; exit 1; }
mkdir -p "$work/current" "$work/previous"
tar -xzf "$current_archive" -C "$work/current"
tar -xzf "$previous_archive" -C "$work/previous"
current_binary="$(find "$work/current" -type f -name bv | head -1)"
previous_binary="$(find "$work/previous" -type f -name bv | head -1)"
if [ -z "$current_binary" ] || [ -z "$previous_binary" ]; then echo 'release binary missing' >&2; exit 1; fi
[ "$("$current_binary" --version)" = "bv $current_tag" ] || { echo 'current binary does not run natively with the requested version' >&2; exit 1; }
[ "$("$previous_binary" --version)" = "bv $previous_tag" ] || { echo 'previous binary does not run natively with the requested version' >&2; exit 1; }
good_hash="$(hash_file "$current_binary")"
echo "Native host: $(uname -s) $(uname -m); installer SHA256=$(hash_file "$installer"); current archive=$current_sha; previous archive=$previous_sha"

failures=0
for scenario in matching-release wrong-version corrupt-checksum corrupt-archive missing-checksum missing-binary; do
    dir="$work/$scenario"
    mkdir -p "$dir/source" "$dir/existing bin"
    cp "$current_binary" "$dir/existing bin/bv"
    archive="$dir/source/bv_test.tar.gz"
    if [ "$scenario" = wrong-version ]; then cp "$previous_archive" "$archive"
    elif [ "$scenario" = corrupt-archive ]; then printf 'not a tar archive\n' > "$archive"
    elif [ "$scenario" = missing-binary ]; then
        printf 'archive intentionally contains no executable\n' > "$dir/source/README.txt"
        tar -czf "$archive" -C "$dir/source" README.txt
    else cp "$current_archive" "$archive"; fi
    sha="$(hash_file "$archive")"
    if [ "$scenario" = corrupt-checksum ]; then sha="$(printf '%064d' 0)"; fi
    if [ "$scenario" = missing-checksum ]; then
        printf '%s  unrelated.tar.gz\n' "$sha" > "$dir/source/checksums.txt"
    else
        printf '%s  bv_test.tar.gz\n' "$sha" > "$dir/source/checksums.txt"
    fi
    # The selected asset's name, used for the checksum lookup, must match the
    # actual file name while retaining this host's platform suffix.
    platform="$(INSTALL_DIR="$dir/existing bin" bash -c 'source "$1"; detect_platform' _ "$installer")"
    asset="bv_test_${platform}.tar.gz"
    cp "$archive" "$dir/source/$asset"
    if [ "$scenario" != missing-checksum ]; then printf '%s  %s\n' "$sha" "$asset" > "$dir/source/checksums.txt"; fi
    python3 - "$dir/source/$asset" "$current_tag" > "$dir/release.json" <<'PY'
import json, pathlib, sys
archive = pathlib.Path(sys.argv[1])
print(json.dumps({"tag_name": sys.argv[2], "assets": [{"name": archive.name, "browser_download_url": archive.as_uri()}]}))
PY
    rc=0
    INSTALL_DIR="$dir/existing bin" BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_UPDATE_CHECK=1 \
        bash -c '
            source "$1"
            metadata="$2"
            fallback="$3"
            get_latest_release() { cat "$metadata"; }
            download_file() {
                local path
                path=$(python3 -c "import sys,urllib.parse; print(urllib.parse.unquote(urllib.parse.urlparse(sys.argv[1]).path))" "$1")
                cp "$path" "$2"
            }
            try_go_install() {
                printf "Unexpected source fallback after a release was selected\n" > "$fallback"
                cat "$fallback" >&2
                return 97
            }
            main
        ' _ "$installer" "$dir/release.json" "$dir/source-fallback" > "$dir/install.log" 2>&1 || rc=$?
    preserved=false
    if [ "$(hash_file "$dir/existing bin/bv")" = "$good_hash" ]; then preserved=true; fi
    printf '%s exit=%s preserved=%s log=%s\n' "$scenario" "$rc" "$preserved" "$dir/install.log"
    if { [ "$scenario" = matching-release ] && [ "$rc" -ne 0 ]; } ||
       { [ "$scenario" != matching-release ] && [ "$rc" -eq 0 ]; } || [ "$preserved" != true ]; then
        cat "$dir/install.log"
        failures=$((failures + 1))
    fi
    if [ -e "$dir/source-fallback" ]; then
        cat "$dir/source-fallback" >&2
        failures=$((failures + 1))
    fi
done
if [ "$failures" -ne 0 ]; then echo "FAIL: $failures archive replacement controls failed" >&2; exit 1; fi
echo 'PASS native archive version, checksum, extraction, and failed-install preservation controls'
