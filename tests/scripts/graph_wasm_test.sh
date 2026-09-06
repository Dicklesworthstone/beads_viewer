#!/usr/bin/env bash
# Real isolated builds and Node WebAssembly execution; no browser proof claimed.
# Fault-injection tools below are negative controls only. Never used by positives.
# Retain external outputs/receipts so a verifier can inspect or re-execute them.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d /tmp/bv-graph-wasm-test.XXXXXX)"
echo "graph-WASM test artifacts: $tmp"
fail() { echo "FAIL: $*" >&2; exit 1; }
for tool in node python3 rustup; do
  command -v "$tool" >/dev/null || { echo "INCOMPLETE: missing $tool" >&2; exit 2; }
done
run_build() {
  local name="$1" source="$2"; shift 2
  local status=0
  bash "$source/scripts/build_graph_wasm.sh" --out-dir "$tmp/$name" "$@" >"$tmp/$name.log" 2>&1 || status=$?
  if [ "$status" -ne 0 ]; then cat "$tmp/$name.log"; fail "$name exited $status"; fi
  echo "ok: $name ($tmp/$name/receipt.json)"
}
reject() {
  local name="$1" expected_exit="$2" expected="$3"; shift 3
  local status=0
  "$@" >"$tmp/$name.log" 2>&1 || status=$?
  if [ "$status" -ne "$expected_exit" ] || ! grep -q "$expected" "$tmp/$name.log"; then
    cat "$tmp/$name.log"
    fail "$name exit=$status; expected $expected_exit and '$expected'"
  fi
  if grep -q '^VERIFIED:\|^PASS' "$tmp/$name.log"; then fail "$name claimed verification"; fi
  echo "ok: $name rejected (exit $status; $tmp/$name.log)"
}
fixture() {
  local path="$tmp/$1"
  mkdir -p "$path/scripts" "$path/bv-graph-wasm" "$path/pkg/export/viewer_assets/vendor"
  cp "$root/scripts/build_graph_wasm.sh" "$root/scripts/verify_vendor.sh" "$path/scripts/"
  cp "$root/bv-graph-wasm/Cargo.toml" "$root/bv-graph-wasm/Cargo.lock" \
    "$root/bv-graph-wasm/rust-toolchain.toml" "$root/bv-graph-wasm/Makefile" "$path/bv-graph-wasm/"
  cp -R "$root/bv-graph-wasm/src" "$path/bv-graph-wasm/src"
  cp "$root/pkg/export/viewer_assets/vendor/"* "$path/pkg/export/viewer_assets/vendor/"
  printf '%s\n' "$path"
}

# Separate copies, output roots and empty compiler target caches. The second
# compiler has identical component bytes at a different physical sysroot. A
# shared Rust home missed embedded rust-src panic paths in the original proof.
first="$(fixture 'first source')"
second="$(fixture 'second source')"
run_build build-a "$first"
toolchain=nightly-2026-08-31
host=x86_64-unknown-linux-gnu
pinned_sysroot="$(rustup run "$toolchain" rustc --print sysroot)"
relocated_home="$tmp/relocated rust home"
relocated_sysroot="$relocated_home/toolchains/$toolchain-$host"
mkdir -p "$relocated_sysroot/bin" "$relocated_sysroot/lib/rustlib"
# Copy only compiler/Cargo, their libraries, the host and WASM target, and
# optional installed Rust sources. No tool installation, user HOME change,
# docs, other compilation targets, downloads or shared toolchain edits.
for name in rustc cargo cargo-rch-real; do
  if [ -e "$pinned_sysroot/bin/$name" ]; then
    cp -a --reflink=auto "$pinned_sysroot/bin/$name" "$relocated_sysroot/bin/"
  fi
done
for path in "$pinned_sysroot/lib/"* "$pinned_sysroot/lib/rustlib/"*; do
  if [ -f "$path" ]; then
    relative="${path#"$pinned_sysroot/"}"
    cp -a --reflink=auto "$path" "$relocated_sysroot/$relative"
  fi
done
for name in "$host" wasm32-unknown-unknown src; do
  if [ -d "$pinned_sysroot/lib/rustlib/$name" ]; then
    cp -a --reflink=auto "$pinned_sysroot/lib/rustlib/$name" "$relocated_sysroot/lib/rustlib/"
  fi
done
python3 - "$pinned_sysroot" "$relocated_sysroot" <<'PY'
import pathlib, sys
original, relocated = [pathlib.Path(p).resolve() for p in sys.argv[1:]]
assert original != relocated, 'compiler was not physically relocated'
files = [p for p in relocated.rglob('*') if p.is_file()]
assert files, 'relocated compiler is empty'
for p in files:
    relative = p.relative_to(relocated)
    assert p.read_bytes() == (original/relative).read_bytes(), relative
print(f'ok: {len(files)} relocated compiler component files exactly match {original}')
PY
RUSTUP_HOME="$relocated_home" run_build build-b "$second"
python3 - "$tmp/build-a/receipt.json" "$tmp/build-b/receipt.json" <<'PY'
import json, sys
a, b = [json.load(open(p)) for p in sys.argv[1:]]
assert a['status'] == b['status'] == 'verified'
assert a['source_sha256'] == b['source_sha256']
assert a['pipeline'] == b['pipeline']
assert a['artifacts'] == b['artifacts']
assert a['rust_sysroot']['resolved'] != b['rust_sysroot']['resolved']
assert a['pipeline']['sysroot_remapping'] == '/rust-toolchain'
print('ok: two physical Rust homes produce identical glue/WASM and verified receipts')
PY

# All five committed graph fixtures execute in the actual shipped and rebuilt
# modules. Go goldens independently constrain the algorithms that share semantics.
node --experimental-vm-modules --input-type=module - "$root" "$tmp/build-a/pkg" "$tmp/build-b/pkg" <<'JS'
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const [root, a, b] = process.argv.slice(2);
const viewer = path.join(root, 'pkg/export/viewer_assets');
const moduleFrom = async (file, tag = '') => import('data:text/javascript;base64,' +
  Buffer.from(fs.readFileSync(file, 'utf8') + '\n//' + tag).toString('base64'));
const consumer = await moduleFrom(path.join(viewer, 'graph.js'));
const dirs = [path.join(viewer, 'vendor'), a, b];
const methods = ['nodeCount', 'edgeCount', 'pagerankDefault', 'betweenness',
  'criticalPathHeights', 'kcore', 'hasCycles', 'hitsDefault', 'eigenvectorDefault',
  'slack', 'outDegrees', 'inDegrees'];
const modules = [];
for (const dir of dirs) {
  const mod = await moduleFrom(path.join(dir, 'bv_graph.js'), dir);
  mod.initSync({ module: fs.readFileSync(path.join(dir, 'bv_graph_bg.wasm')) });
  modules.push(mod);
}
for (const mod of modules.slice(1)) assert.deepEqual(
  Object.getOwnPropertyNames(mod.DiGraph.prototype).sort(),
  Object.getOwnPropertyNames(modules[0].DiGraph.prototype).sort());
const near = (got, expected, tolerance, label) => {
  assert.ok(Number.isFinite(got) && Math.abs(got - expected) <= tolerance,
    `${label}: got ${got}, expected ${expected} ± ${tolerance}`);
};
for (const name of ['chain_10', 'diamond_5', 'star_10', 'cycle_5', 'complex_20']) {
  const fixture = JSON.parse(fs.readFileSync(path.join(root, `testdata/graphs/${name}.json`)));
  const expected = JSON.parse(fs.readFileSync(path.join(root, `testdata/expected/${name}_metrics.json`)));
  let reference;
  for (const [index, mod] of modules.entries()) {
    const graph = new mod.DiGraph();
    fixture.nodes.forEach(id => graph.addNode(id));
    fixture.edges.forEach(([from, to]) => graph.addEdge(from, to));
    const metrics = Object.fromEntries(methods.map(method => [method, graph[method]() ]));
    if (index === 0) reference = metrics; else assert.deepEqual(metrics, reference, `${name} module ${index}`);
    assert.equal(metrics.nodeCount, fixture.nodes.length);
    assert.equal(metrics.edgeCount, fixture.edges.length);
    assert.equal(metrics.hasCycles, expected.has_cycles);
    for (const [method, field, tolerance] of [
      ['pagerankDefault', 'pagerank', 1e-5], ['betweenness', 'betweenness', 1e-6],
      ['kcore', 'core_number', 0], ['outDegrees', 'out_degree', 0], ['inDegrees', 'in_degree', 0]
    ]) fixture.nodes.forEach((id, i) => near(metrics[method][i], expected[field][id] ?? 0,
      tolerance, `${name}.${field}.${id}`));
    if (!expected.has_cycles) fixture.nodes.forEach((id, i) =>
      near(metrics.criticalPathHeights[i], expected.critical_path_score[id] ?? 0, 1e-6, `${name}.path.${id}`));
    const hits = consumer.computeHITSMetrics(graph);
    assert.equal(hits.hub.length, fixture.nodes.length, `${name} viewer hub scores`);
    assert.equal(hits.authority.length, fixture.nodes.length, `${name} viewer authority scores`);
    if (name === 'chain_10' || name === 'star_10') fixture.nodes.forEach((id, i) => {
      near(hits.hub[i], expected.hubs[id] ?? 0, 1e-5, `${name}.viewer.hub.${id}`);
      near(hits.authority[i], expected.authorities[id] ?? 0, 1e-5, `${name}.viewer.authority.${id}`);
    });
    near(Math.hypot(...hits.hub), 1, 1e-6, `${name}.hub norm`);
    near(Math.hypot(...hits.authority), 1, 1e-6, `${name}.authority norm`);
    near(Math.hypot(...metrics.eigenvectorDefault), 1, 0.01, `${name}.eigenvector norm`);
    const topo = graph.topologicalSort();
    if (expected.has_cycles) assert.equal(topo, null);
    else {
      const order = Array.from(topo);
      assert.equal(new Set(order).size, fixture.nodes.length);
      fixture.edges.forEach(([from, to]) => assert.ok(order.indexOf(from) < order.indexOf(to)));
    }
    graph.free();
  }
  console.log(`ok: real WASM ${name}: three modules, 12 metrics, independent goldens and viewer HITS`);
}

// Execute the real optional-loader failure path and real JS scorer, with no
// substitute WASM module. This is a Node module test, not a rendered browser.
const context = vm.createContext({ console, WebAssembly, Date });
context.window = context;
for (const file of ['hybrid_scorer.js', 'wasm_loader.js']) {
  vm.runInContext(fs.readFileSync(path.join(viewer, file), 'utf8'), context, {
    filename: path.join(viewer, file),
    importModuleDynamically: spec => import(pathToFileURL(path.resolve(viewer, spec)).href)
  });
}
assert.equal(await context.initHybridWasmScorer(5000), false);
assert.equal(context.getHybridWasmStatus().ready, false);
assert.equal(context.getHybridWasmStatus().attempted, true);
assert.match(context.getHybridWasmStatus().reason, /Cannot find module/);
const ranked = context.scoreBatchHybrid([{ id: 'low', textScore: 0.1 }, { id: 'high', textScore: 0.9 }],
  { text: 1, pagerank: 0, status: 0, impact: 0, priority: 0, recency: 0 });
assert.deepEqual(Array.from(ranked, r => [r.id, r.hybrid_score]), [['high', 0.9], ['low', 0.1]]);
console.log('ok: missing optional hybrid WASM preserves actual JS ranking');
JS

# Fault-injected tools exercise rejection, never the successful builds above.
mkdir -p "$tmp/fault-tools"
cat >"$tmp/fault-tools/version" <<'SH'
#!/usr/bin/env bash
echo 'wrong-tool 0.0.0'
SH
chmod +x "$tmp/fault-tools/version"
reject wrong-bindgen 1 'wasm-bindgen version differs' env WASM_BINDGEN="$tmp/fault-tools/version" bash "$first/scripts/build_graph_wasm.sh"
reject wrong-optimizer 1 'optimizer version differs' env WASM_OPT="$tmp/fault-tools/version" bash "$first/scripts/build_graph_wasm.sh"
reject missing-bindgen 2 'required tool unavailable' env WASM_BINDGEN="$tmp/absent-bindgen" bash "$first/scripts/build_graph_wasm.sh"
reject missing-optimizer 2 'required tool unavailable' env WASM_OPT="$tmp/absent-optimizer" bash "$first/scripts/build_graph_wasm.sh"
reject disabled-optimizer 2 'optimization is required' bash "$first/scripts/build_graph_wasm.sh" --no-opt
reject changed-flags 2 'unsupported build override' env RUSTFLAGS=-Copt-level=0 bash "$first/scripts/build_graph_wasm.sh"
GRAPH_WASM_TEST_REAL_RUSTUP="$(command -v rustup)"
export GRAPH_WASM_TEST_REAL_RUSTUP
cat >"$tmp/fault-tools/rustup" <<'SH'
#!/usr/bin/env bash
case "$GRAPH_WASM_TEST_FAULT:$*" in
  'compiler:run nightly-2026-08-31 rustc -vV') echo 'rustc 0.0.0'; exit 0 ;;
  'target:target list --installed --toolchain nightly-2026-08-31') echo x86_64-unknown-linux-gnu; exit 0 ;;
  'missing:toolchain list') echo stable-x86_64-unknown-linux-gnu; exit 0 ;;
esac
exec "$GRAPH_WASM_TEST_REAL_RUSTUP" "$@"
SH
chmod +x "$tmp/fault-tools/rustup"
reject wrong-compiler 1 'compiler version differs' env PATH="$tmp/fault-tools:$PATH" GRAPH_WASM_TEST_FAULT=compiler bash "$first/scripts/build_graph_wasm.sh"
reject missing-compiler 2 'missing compiler' env PATH="$tmp/fault-tools:$PATH" GRAPH_WASM_TEST_FAULT=missing bash "$first/scripts/build_graph_wasm.sh"
reject missing-target 2 'missing target' env PATH="$tmp/fault-tools:$PATH" GRAPH_WASM_TEST_FAULT=target bash "$first/scripts/build_graph_wasm.sh"

drift="$(fixture lock-drift)"
printf '\n# changed after provenance review\n' >>"$drift/bv-graph-wasm/Cargo.lock"
reject lock-drift 1 'source/pipeline fingerprint differs' bash "$drift/scripts/build_graph_wasm.sh"
drift="$(fixture source-drift)"
printf '\n// changed after provenance review\n' >>"$drift/bv-graph-wasm/src/lib.rs"
reject source-drift 1 'source/pipeline fingerprint differs' bash "$drift/scripts/build_graph_wasm.sh"
drift="$(fixture source-symlink)"
ln -s "$root/bv-graph-wasm/src/algorithms" "$drift/bv-graph-wasm/src/linked-algorithms"
reject source-symlink 1 'source symlinks are not accepted' bash "$drift/scripts/build_graph_wasm.sh"
drift="$(fixture pipeline-drift)"
printf '\n# changed pipeline\n' >>"$drift/bv-graph-wasm/Makefile"
reject pipeline-drift 1 'source/pipeline fingerprint differs' bash "$drift/scripts/build_graph_wasm.sh"
drift="$(fixture manifest-lock-drift)"
python3 - "$drift/pkg/export/viewer_assets/vendor/MANIFEST.json" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
manifest = json.loads(p.read_text())
manifest['graph_wasm']['cargo_lock_sha256'] = '0' * 64
p.write_text(json.dumps(manifest))
PY
reject manifest-lock-drift 1 'recorded Cargo.lock hash differs' bash "$drift/scripts/build_graph_wasm.sh"

for name in bv_graph.js bv_graph_bg.wasm; do
  drift="$(fixture "tampered-$name")"
  # One changed byte in a retained isolated fixture; never edit the real vendor.
  printf X | dd of="$drift/pkg/export/viewer_assets/vendor/$name" bs=1 seek=17 count=1 conv=notrunc
  reject "hash-$name" 1 MISMATCH bash "$drift/scripts/verify_vendor.sh"
  reject "rebuild-$name" 1 'generated and shipped bytes differ' bash "$drift/scripts/build_graph_wasm.sh"
done
echo "PASS: graph-WASM reproducibility, negative controls, algorithm parity and JS fallback ($tmp)"
