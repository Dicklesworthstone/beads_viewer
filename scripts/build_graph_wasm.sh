#!/usr/bin/env bash
# Build the reviewed Linux graph-WASM pipeline in a fresh external directory.
# Default: rebuild and verify BOTH shipped files and their source fingerprint.
# --build-only produces a review candidate, never a source-correspondence claim.
# All dependencies must already be cached; this script never downloads tools.
# Usage: scripts/build_graph_wasm.sh [--build-only] [--out-dir NEW_EXTERNAL_DIR]
# Env: WASM_BINDGEN, WASM_OPT select binaries with the pinned versions/hashes.
# Retained receipt.json is consumed by this check and its reproducibility tests.
set -euo pipefail
export LC_ALL=C

root="$(cd "$(dirname "$0")/.." && pwd)"
crate="$root/bv-graph-wasm"
vendor="$root/pkg/export/viewer_assets/vendor"
mode=verify
out=""
incomplete() { echo "INCOMPLETE: $*" >&2; exit 2; }
fail() { echo "FAIL: $*" >&2; exit 1; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --build-only) mode=build ;;
    --offline) ;; # All builds are offline and locked.
    --out-dir)
      [ "$#" -ge 2 ] || incomplete '--out-dir needs a path'
      out="$2"; shift ;;
    *) incomplete "unsupported argument $1; optimization is required by this pipeline" ;;
  esac
  shift
done

toolchain=nightly-2026-08-31
target=wasm32-unknown-unknown
want_rustc='rustc 1.100.0-nightly (908501772 2026-08-30)'
want_commit=90850177249efe0321573c569aec5d12b257f8d6
want_cargo='cargo 1.100.0-nightly (e8cb624d5 2026-08-22)'
bindgen="${WASM_BINDGEN:-wasm-bindgen}"
wasmopt="${WASM_OPT:-wasm-opt}"
for tool in rustup python3 sha256sum "$bindgen" "$wasmopt"; do
  command -v "$tool" >/dev/null || incomplete "required tool unavailable: $tool"
done
bindgen="$(command -v "$bindgen")"
wasmopt="$(command -v "$wasmopt")"
rustup="$(command -v rustup)"
for override in RUSTFLAGS CARGO_ENCODED_RUSTFLAGS RUSTC RUSTC_WRAPPER RUSTC_WORKSPACE_WRAPPER \
                CARGO_BUILD_RUSTFLAGS CARGO_BUILD_TARGET RUSTUP_TOOLCHAIN; do
  [ -z "${!override:-}" ] || incomplete "unsupported build override: $override"
done
# Check installation before invoking rustup run: missing prerequisites must not
# trigger implicit downloads with a different network policy/toolchain.
installed="$($rustup toolchain list)"
case "$installed" in *"$toolchain-x86_64-unknown-linux-gnu"*) ;; *) incomplete "missing compiler $toolchain" ;; esac
rust_version="$($rustup run "$toolchain" rustc -vV)"
[[ "$rust_version" == "$want_rustc"$'\n'* ]] || fail "compiler version differs from pinned $want_rustc"
[[ "$rust_version" == *"commit-hash: $want_commit"* ]] || fail 'compiler commit differs'
[[ "$rust_version" == *'host: x86_64-unknown-linux-gnu'* ]] || fail 'compiler host differs'
[ "$($rustup run "$toolchain" cargo --version)" = "$want_cargo" ] || fail 'cargo version differs'
installed_targets="$($rustup target list --installed --toolchain "$toolchain")"
[[ "$installed_targets" == *"$target"* ]] || incomplete "missing target $target for $toolchain"
[ "$("$bindgen" --version)" = 'wasm-bindgen 0.2.121' ] || fail 'wasm-bindgen version differs: expected 0.2.121'
[ "$("$wasmopt" --version)" = 'wasm-opt version 132 (version_132)' ] || fail 'optimizer version differs: expected Binaryen 132'
[ "$(sha256sum "$bindgen" | cut -d' ' -f1)" = 778ec413ee7c3ea501d49b376fef3c390bf1f6e64ece888ed30472f09c3a1923 ] || fail 'wasm-bindgen executable hash differs'
[ "$(sha256sum "$wasmopt" | cut -d' ' -f1)" = 1014958e6f20d412f1542320b43970214b0fb1ed780595e8f7c0d8761ed53725 ] || fail 'optimizer executable hash differs'

want_bindgen="$(awk '/^name = "wasm-bindgen"$/{getline; sub(/version = "/, "", $0); sub(/"$/, "", $0); print; exit}' "$crate/Cargo.lock")"
[ "$want_bindgen" = 0.2.121 ] || fail 'Cargo.lock wasm-bindgen differs from pinned CLI'
if [ -z "$out" ]; then
  out="$(mktemp -d /tmp/bv-graph-wasm.XXXXXX)"
else
  out="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$out")"
  case "$out/" in "$root/"*) incomplete 'build output must be outside the source tree' ;; esac
  [ ! -e "$out" ] || incomplete 'output directory already exists; use a fresh directory'
  mkdir -p "$out"
fi
echo "graph-WASM build artifacts: $out"
printf '%s\n' "$rust_version" >"$out/rustc.txt"

# Fingerprint all compilation inputs and the pipeline, independently of checkout
# path or unrelated project edits. The manifest cannot be its own hash input.
fingerprint() {
  python3 - "$root" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
names = ['bv-graph-wasm/Cargo.toml', 'bv-graph-wasm/Cargo.lock',
         'bv-graph-wasm/rust-toolchain.toml', 'bv-graph-wasm/Makefile',
         'scripts/build_graph_wasm.sh']
for p in (root/'bv-graph-wasm/src').rglob('*'):
    if p.is_symlink():
        raise SystemExit('FAIL: source symlinks are not accepted: ' + str(p.relative_to(root)))
    if p.is_file():
        names.append(str(p.relative_to(root)))
inputs = {}
for name in sorted(names):
    p = root/name
    if p.is_symlink():
        raise SystemExit('source symlinks are not accepted: ' + name)
    inputs[name] = hashlib.sha256(p.read_bytes()).hexdigest()
digest = hashlib.sha256(json.dumps(inputs, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
print(json.dumps({'source_sha256': digest, 'inputs': inputs}, sort_keys=True))
PY
}
fingerprint >"$out/source.json"
if [ "$mode" = verify ]; then
  python3 - "$out/source.json" "$vendor/MANIFEST.json" <<'PY'
import json, sys
source, manifest = [json.load(open(p)) for p in sys.argv[1:]]
if source['source_sha256'] != manifest.get('graph_wasm', {}).get('source_sha256'):
    raise SystemExit('FAIL: graph-WASM source/pipeline fingerprint differs from manifest')
if source['inputs']['bv-graph-wasm/Cargo.lock'] != manifest['graph_wasm'].get('cargo_lock_sha256'):
    raise SystemExit('FAIL: recorded Cargo.lock hash differs from source')
PY
fi

mkdir -p "$out/source/bv-graph-wasm" "$out/cargo" "$out/pkg" "$out/bindgen"
cp "$crate/Cargo.toml" "$crate/Cargo.lock" "$crate/rust-toolchain.toml" "$out/source/bv-graph-wasm/"
cp -R "$crate/src" "$out/source/bv-graph-wasm/src"
cargo_cache="${CARGO_HOME:-$HOME/.cargo}"
[ -d "$cargo_cache/registry" ] || incomplete 'Cargo registry cache unavailable; fetch locked dependencies first'
ln -s "$cargo_cache/registry" "$out/cargo/registry"
# A fresh Cargo home prevents user config/patch overrides. Reject ancestor
# configs too; Cargo would otherwise discover them outside the copied crate.
ancestor="$out/source/bv-graph-wasm"
while :; do
  if [ -e "$ancestor/.cargo/config" ] || [ -e "$ancestor/.cargo/config.toml" ]; then
    incomplete "ambient Cargo config: $ancestor/.cargo"
  fi
  [ "$ancestor" != / ] || break
  ancestor="$(dirname "$ancestor")"
done
echo '== cargo build (locked, offline, default features, isolated local compiler)'
(
  cd "$out/source/bv-graph-wasm"
  env -i HOME="$HOME" PATH="$PATH" LC_ALL=C \
    RUSTUP_HOME="${RUSTUP_HOME:-$HOME/.rustup}" CARGO_HOME="$out/cargo" \
    CARGO_TARGET_DIR="$out/target" CARGO_HTTP_USER_AGENT='OpenAI File Downloader, XaiImageApiFetch/1.0' \
    RCH_CARGO_WRAPPER_BYPASS=1 \
    RUSTFLAGS="--remap-path-prefix=$out=/bv-build --remap-path-prefix=$cargo_cache=/cargo" \
    "$rustup" run "$toolchain" cargo build --locked --offline --release --target "$target" -j 2
) 2>&1 | tee "$out/cargo.log"
raw="$out/target/$target/release/bv_graph_wasm.wasm"
[ -f "$raw" ] || fail "expected $raw after cargo build"

echo "== wasm-bindgen"
"$bindgen" --target web --out-dir "$out/bindgen" --out-name bv_graph "$raw"
cp "$out/bindgen/bv_graph.js" "$out/pkg/bv_graph.js"
echo '== wasm-opt -Os (Binaryen 132 required)'
"$wasmopt" -Os "$out/bindgen/bv_graph_bg.wasm" -o "$out/pkg/bv_graph_bg.wasm"
fingerprint >"$out/source-after.json"
cmp -s "$out/source.json" "$out/source-after.json" || fail 'source changed during build'

python3 - "$out" "$vendor" "$mode" "$want_cargo" <<'PY'
import hashlib, json, pathlib, sys
out, vendor = map(pathlib.Path, sys.argv[1:3])
mode, cargo = sys.argv[3:]
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
receipt = json.loads((out/'source.json').read_text())
receipt.update(schema=1, status='built', pipeline={
    'rustc': (out/'rustc.txt').read_text().strip(), 'cargo': cargo,
    'target': 'wasm32-unknown-unknown', 'features': 'default',
    'cargo_flags': '--locked --offline --release --target wasm32-unknown-unknown -j 2',
    'wasm_bindgen': '0.2.121', 'bindgen_flags': '--target web --out-name bv_graph',
    'wasm_opt': '132', 'optimizer_flags': '-Os', 'path_remapping': True,
    'bindgen_executable_sha256': '778ec413ee7c3ea501d49b376fef3c390bf1f6e64ece888ed30472f09c3a1923',
    'optimizer_executable_sha256': '1014958e6f20d412f1542320b43970214b0fb1ed780595e8f7c0d8761ed53725'},
    artifacts={name: {'sha256': sha(out/'pkg'/name), 'bytes': (out/'pkg'/name).stat().st_size}
               for name in ('bv_graph.js', 'bv_graph_bg.wasm')})
problems = []
if mode == 'verify':
    manifest = json.loads((vendor/'MANIFEST.json').read_text())
    if receipt['source_sha256'] != manifest.get('graph_wasm', {}).get('source_sha256'):
        problems.append('recorded source fingerprint changed during build')
    if receipt['inputs']['bv-graph-wasm/Cargo.lock'] != manifest.get('graph_wasm', {}).get('cargo_lock_sha256'):
        problems.append('recorded Cargo.lock hash differs from source')
    if receipt['pipeline'] != manifest.get('graph_wasm', {}).get('pipeline'):
        problems.append('recorded pipeline differs')
    entries = {e['name']: e['sha256'] for e in manifest['files']}
    for name, info in receipt['artifacts'].items():
        if not (vendor/name).is_file() or (out/'pkg'/name).read_bytes() != (vendor/name).read_bytes():
            problems.append(name + ': generated and shipped bytes differ')
        if info['sha256'] != entries.get(name):
            problems.append(name + ': generated hash differs from manifest')
    receipt['status'] = 'failed' if problems else 'verified'
receipt['problems'] = problems
(out/'receipt.json').write_text(json.dumps(receipt, indent=2, sort_keys=True) + '\n')
print(json.dumps({'status': receipt['status'], 'source_sha256': receipt['source_sha256'],
                  'artifacts': receipt['artifacts'], 'receipt': str(out/'receipt.json')}))
if problems:
    raise SystemExit('FAIL: ' + '; '.join(problems))
print('VERIFIED: source rebuild matches glue, WASM and manifest' if mode == 'verify' else
      'BUILT: review candidate; shipped source correspondence has not been verified')
PY
