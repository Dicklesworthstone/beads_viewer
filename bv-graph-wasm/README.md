# bv-graph-wasm

High-performance graph algorithms for the bv static viewer, compiled to WebAssembly.

## Prerequisites

Release verification requires the pinned Rust toolchain and WASM target,
wasm-bindgen 0.2.128, Binaryen 132, Python 3 and cached locked crates. See
[PROVENANCE.md](../docs/PROVENANCE.md) for exact tool hashes and installation
requirements. `wasm-pack` is used only by the separate development workflow.

## Building

```bash
# Development build (faster, larger)
make build

# Release build (optimized, smaller)
make build-release

# Run tests
make test
```

## Output

The pinned release command reports a fresh external build directory containing
`pkg/bv_graph.js`, `pkg/bv_graph_bg.wasm`, full logs and `receipt.json`.

After a development build, the local `pkg/` directory contains:
- `bv_graph_wasm.js` - JavaScript bindings
- `bv_graph_wasm_bg.wasm` - WebAssembly binary
- `bv_graph_wasm.d.ts` - TypeScript definitions

## Usage

```javascript
import init, { DiGraph, version } from './pkg/bv_graph_wasm.js';

async function main() {
    await init();

    console.log('Version:', version());

    const graph = new DiGraph();
    const a = graph.addNode('bv-1');
    const b = graph.addNode('bv-2');
    graph.addEdge(a, b);

    console.log('Nodes:', graph.nodeCount());
    console.log('Edges:', graph.edgeCount());
    console.log('Density:', graph.density());

    // Export/import
    const json = graph.toJson();
    const graph2 = DiGraph.fromJson(json);

    // Don't forget to free when done
    graph.free();
    graph2.free();
}

main();
```

## API

### DiGraph

| Method | Description |
|--------|-------------|
| `new()` | Create empty graph |
| `withCapacity(n, e)` | Create with pre-allocated capacity |
| `addNode(id)` | Add node, returns index (idempotent) |
| `addEdge(from, to)` | Add directed edge (idempotent) |
| `nodeCount()` | Number of nodes |
| `edgeCount()` | Number of edges |
| `density()` | Graph density |
| `nodeId(idx)` | Get node ID by index |
| `nodeIdx(id)` | Get node index by ID |
| `nodeIds()` | All node IDs as array |
| `outDegree(node)` | Out-degree of node |
| `inDegree(node)` | In-degree of node |
| `successors(node)` | Get successor indices |
| `predecessors(node)` | Get predecessor indices |
| `toJson()` | Export as JSON |
| `fromJson(json)` | Import from JSON |
| `free()` | Release memory |

`topkSet(closedSet, k, candidateSet?)` selects a greedy set of issues;
`topWhatIf(closedSet, limit, candidateSet?)` ranks currently actionable nodes
by cascade impact. The optional `Uint8Array` mask restricts direct choices:
nonzero entries are eligible, missing entries are ineligible, and omitting
the mask considers all nodes. Excluded nodes remain in dependency checks and
are not marked closed. The mask does not truncate downstream cascades.

## Size

### Current Measurements

| Component | Raw | Gzipped (`gzip -n -9`) | Budget |
|-----------|-----|------------------------|--------|
| WASM binary (`default` / `full`) | 204 KiB (208,792 B) | 86 KiB (88,054 B) | <80 KiB |
| WASM binary (`--no-default-features --features core`) | 192 KiB (196,762 B) | 82 KiB (84,162 B) | <80 KiB |
| JS glue (`bv_graph.js`) | 35 KiB (35,456 B) | 7 KiB (7,100 B) | <25 KiB |
| **Shipped Total (`full` + glue)** | **239 KiB (244,248 B)** | **93 KiB (95,154 B)** | **<120 KiB** |

Measured with `gzip -n -9` on the reviewed pair (`source_sha256`
`500c61457a45b58a6ca95c2114bf1f6ec95d6ec6659d41c39ddf79c235faaf9e`). The total
compressed size (`93 KiB`) is well within the `<120 KiB` budget; the separate
`<80 KiB` WASM-only target remains unmet (`86 KiB` shipped `full`, `82 KiB`
`core`-only) due to `serde`/`serde_json`/`serde-wasm-bindgen` runtime floor.

### Size Optimization

The build pipeline applies multiple optimizations:

1. **Cargo.toml profile settings**:
   - `opt-level = "z"` - Optimize aggressively for size
   - `lto = true` - Link-time optimization
   - `codegen-units = 1` - Better optimization
   - `panic = "abort"` - Remove panic unwinding

2. **Pinned release tools**:
   - `nightly-2026-08-31` Rust with `wasm32-unknown-unknown`
   - wasm-bindgen 0.2.128 and Binaryen 132, with a required `wasm-opt -Os` pass
   - Locked, offline compilation in a fresh external directory

`make build-release` rebuilds and checks both shipped files against the
manifest. To produce a candidate for review, run
`../scripts/build_graph_wasm.sh --build-only`. Neither command writes the
vendor directory. Tool installation, exact hashes and the review workflow are
documented in [PROVENANCE.md](../docs/PROVENANCE.md).

### Checking Size

```bash
make size  # Shows raw and gzipped (-n -9) sizes
```

### Feature Flags

Optional algorithm groups are gated behind real `#[cfg(feature = "...")]`
blocks on `DiGraph`. The reviewed viewer bundle enables `full` by default so
`viewer.js` and `graph.js` have complete parity, while custom builds can trim
16 JS/WASM exports (`~12 KiB` raw / `~4 KiB` gzipped) with
`cargo build --release --target wasm32-unknown-unknown --no-default-features --features core`.

| Feature | Description | Default |
|---------|-------------|---------|
| `core` | Core algorithms (pagerank, betweenness, cycles, critical path, topo, articulation, subgraph, what-if, topk_set, parallel_cut, k_paths, coverage) | Yes (via `full`) |
| `eigenvector` | Eigenvector centrality | Yes (via `full`) |
| `kcore` | K-core decomposition | Yes (via `full`) |
| `slack` | Slack/float calculations | Yes (via `full`) |
| `hits` | HITS algorithm | Yes (via `full`) |
| `reachability` | Reachability & blocker queries | Yes (via `full`) |
| `full` | All algorithms (`core` + all 5 optional groups) | Yes |

## License

MIT
