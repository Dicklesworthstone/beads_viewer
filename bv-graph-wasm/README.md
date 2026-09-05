# bv-graph-wasm

High-performance graph algorithms for the bv static viewer, compiled to WebAssembly.

## Prerequisites

Release verification requires the pinned Rust toolchain and WASM target,
wasm-bindgen 0.2.121, Binaryen 132, Python 3 and cached locked crates. See
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

## Size

### Current Measurements

| Component | Raw | Gzipped | Budget |
|-----------|-----|---------|--------|
| WASM binary | 221 KiB | 95 KiB | <80 KiB |
| JS glue | 33 KiB | 7 KiB | <25 KiB |
| **Total** | **254 KiB** | **102 KiB** | **<120 KiB** |

Measured with `gzip -n -9` on the reviewed pair. The total compressed size is
within budget; the separate WASM size target remains unmet.

### Size Optimization

The build pipeline applies multiple optimizations:

1. **Cargo.toml profile settings**:
   - `opt-level = "s"` - Optimize for size
   - `lto = true` - Link-time optimization
   - `codegen-units = 1` - Better optimization
   - `panic = "abort"` - Remove panic unwinding

2. **Pinned release tools**:
   - `nightly-2026-08-31` Rust with `wasm32-unknown-unknown`
   - wasm-bindgen 0.2.121 and Binaryen 132, with a required `wasm-opt -Os` pass
   - Locked, offline compilation in a fresh external directory

`make build-release` rebuilds and checks both shipped files against the
manifest. To produce a candidate for review, run
`../scripts/build_graph_wasm.sh --build-only`. Neither command writes the
vendor directory. Tool installation, exact hashes and the review workflow are
documented in [PROVENANCE.md](../docs/PROVENANCE.md).

### Checking Size

```bash
make size  # Shows raw and gzipped sizes
```

### Feature Flags

The crate declares the following feature names. Algorithm modules are currently
exported unconditionally, so selecting these flags does not trim the exported
API or establish a smaller release pipeline. The reviewed build uses defaults.

| Feature | Description | Default |
|---------|-------------|---------|
| `core` | Core algorithms (pagerank, betweenness, cycles, critical path) | Yes |
| `eigenvector` | Eigenvector centrality | No |
| `kcore` | K-core decomposition | No |
| `slack` | Slack/float calculations | No |
| `hits` | HITS algorithm | No |
| `reachability` | Reachability queries | No |
| `full` | All algorithms | No |

## License

MIT
