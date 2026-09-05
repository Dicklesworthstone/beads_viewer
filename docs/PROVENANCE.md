# Vendored source and asset provenance

## Go source patches

The vendored `github.com/charmbracelet/glamour` v1.0.0 contains a local
patch in `ansi/elements.go`: fenced and indented code blocks append their
logical line segments to a `strings.Builder`. The upstream growing-string
loop copied every preceding line again, making long dependency trees expensive
to render. Segment padding, synthetic final newlines, language selection and
rendering remain unchanged. The upstream version and license are unchanged.

The vendored `github.com/alecthomas/chroma/v2` v2.24.1 also uses a
`strings.Builder` in `coalesce.go` when merging adjacent tokens of the same
type. It preserves the existing 8192-byte pre-append boundary, empty-token
handling, token types and error propagation. Resetting the builder when a
group is emitted keeps previously returned strings immutable.

The vendored `github.com/muesli/reflow` v0.3.0 measures each already decoded
rune directly in `padding/padding.go`, avoiding a temporary single-rune string.
All valid Unicode scalar values were checked against the previous operation
under all four East Asian/emoji width settings. Existing writer output, chunk
handling and downstream errors were compared exactly, including malformed UTF-8
and terminal escape sequences.

`pkg/ui/markdown_test.go` checks actual parsed blocks, padded segments, complete
content, token boundaries, scalar widths and allocation bounds. The original
operations fail their respective bounds for both block types, a real plaintext
lexer and a Unicode padding writer. The two builder patches were each compared
against their originals on 256 common-input rendering records. The padding
change was checked on 248 common-input records and four actual detail frames,
including dense trees, with both builder patches enabled on both sides. These
comparisons retain complete raw ANSI output.

These patches apply to builds using this checkout's vendor directory,
including the Nix build. `go mod vendor` replaces them, so review and reapply
them until upstream versions include the fixes. Version-suffixed
[`go install`](https://go.dev/ref/mod#go-install) ignores vendored dependencies;
those installations do not include these optimizations. No timing claim for a
vendored build applies automatically to them.

## Dashboard assets

The static dashboard that `bv --export-pages` produces ships a set of
third-party JavaScript libraries, two WebAssembly modules, and two fonts from
`pkg/export/viewer_assets/vendor/`. They are embedded into the `bv` binary
(`pkg/export/viewer_embed.go`, `//go:embed viewer_assets`) and copied into
every exported bundle, so a tampered or silently replaced file would reach
every dashboard viewer. This document states how those files are tracked.

## The manifest

`pkg/export/viewer_assets/vendor/MANIFEST.json` lists every file with:

| Field | Meaning |
|-------|---------|
| `name` | File name inside the vendor directory |
| `upstream` | The project the file comes from |
| `version` | Version read from the artifact's own header or embedded strings; `unknown` when the artifact carries no marker (the hash still pins the exact bytes) |
| `license` | SPDX-style license of the upstream project |
| `sha256` | SHA-256 of the file as shipped |
| `source_url` | Where the file can be fetched or rebuilt from |
| `build_command` | `none (published build)` for upstream releases, or the command that produces the file from source |
| `reviewed_by` / `date` (top level) | Who last verified the entries and when |

The manifest is itself embedded, so an exported bundle carries its own
provenance record.

## Verification

Two checks read the manifest and recompute every hash:

- `scripts/verify_vendor.sh [dir]` fails on a hash mismatch, a listed file
  that is missing, or a file on disk that the manifest does not name.
- `scripts/verify_vendor.sh --source`, stage 7 of `scripts/release_gate.sh`,
  also rebuilds graph WASM and its JavaScript glue with the pinned pipeline.
  Both must match the shipped bytes and the manifest's source fingerprint.
- `pkg/export/vendor_manifest_test.go` performs the same check under
  `go test`, and additionally proves the check is not vacuous by verifying a
  copy with one flipped byte.

Replacing an asset therefore requires updating its manifest entry (hash,
version, source, date, reviewer) in the same change; the gate blocks
anything else.

## Rebuilding the in-repo WebAssembly

`scripts/build_graph_wasm.sh` (also `cd bv-graph-wasm && make build-release`)
rebuilds `bv_graph.js` and `bv_graph_bg.wasm` together. The reviewed pipeline is:

| Component | Pin |
|-----------|-----|
| Compiler | `nightly-2026-08-31`, rustc commit `90850177249efe0321573c569aec5d12b257f8d6`, Linux x86-64 |
| Target | `wasm32-unknown-unknown`, required in `rust-toolchain.toml` |
| Dependencies/features | `Cargo.lock`, default features, `--locked --offline` |
| Cargo profile | release, size optimization, LTO, one codegen unit, abort on panic |
| Bindgen | 0.2.121, Linux musl executable; `--target web --out-name bv_graph` |
| Binaryen | 132, Linux x86-64 executable; exactly `wasm-opt -Os` |

The script checks the compiler's full version/commit and the bindgen and
optimizer executable hashes. It refuses missing tools/target with
`INCOMPLETE` and exit 2, and rejects mismatched tools, source or output with
exit 1. Optimization cannot silently disappear. A different host/compiler or
unoptimized build requires its own reviewed pipeline; it is not this artifact.

Install the dated compiler and target, cache the locked dependencies, and
provide the two release binaries using `WASM_BINDGEN` and `WASM_OPT`. The
build itself makes no network requests. Downloads must use the User-Agent
`OpenAI File Downloader, XaiImageApiFetch/1.0`. The reviewed upstream archives
were fetched from immutable release URLs and checked against GitHub's release
digests:

- [wasm-bindgen 0.2.121 Linux musl](https://github.com/wasm-bindgen/wasm-bindgen/releases/download/0.2.121/wasm-bindgen-0.2.121-x86_64-unknown-linux-musl.tar.gz):
  `3039f38f65fe237b640cf06a140c919ca8d717ec5012146d145d3f27bb4d6b28`.
- [Binaryen 132 Linux x86-64](https://github.com/WebAssembly/binaryen/releases/download/version_132/binaryen-version_132-x86_64-linux.tar.gz):
  `195ddc94f9bc89f45abdabb0b9eea86023d727ba90eac8b35b80f2544fc30572`.

The installed Rust compiler was checked by full version and commit; its
installation archive digest was not independently verified. These checks are
reproducibility controls, not attestations against a malicious build host.

Each run copies source into a fresh external directory, uses an empty compiler
output cache and isolated Cargo configuration, remaps filesystem paths, and
retains its full Cargo log and `receipt.json`. The receipt binds every source
and pipeline input hash, tool versions/options, and both output hashes. The
manifest's `graph_wasm` entry binds that source fingerprint and pipeline to
the two shipped hashes. Unrelated working-tree edits do not change this
fingerprint; the release gate separately requires the entire checkout clean.

For a source update, first run `scripts/build_graph_wasm.sh --build-only`.
This produces a review candidate and a `built` receipt, without claiming that
the shipped artifact matches. Review the API and graph results, then update
both vendor files and the manifest together. The default verification command
must then succeed. `tests/scripts/graph_wasm_test.sh` runs two isolated builds,
executes the actual modules against all five committed graph fixtures, tests
the viewer's HITS field adapter, and rejects tool, source, glue and byte drift.
Existing Go goldens constrain shared graph semantics; eigenvector and some
HITS convergence behavior have documented Go/Rust differences, so their
mathematical invariants are checked separately.

Receipts exist because the source-correspondence gate and its regression
harness consume them: the previous script printed `DIFFERENT` yet exited
success. Retire a receipt when its source/pipeline or shipped pair is
superseded. No automatic cleanup is performed. These module tests establish
neither rendered-browser behavior nor a complete native release gate.

The optional hybrid scorer WASM is not shipped. The real JavaScript scorer
and loader fallback remain tested with the optional module absent; no hybrid
WASM provenance is claimed.

## Adding or upgrading an asset

1. Fetch the release artifact from the upstream release page (never a
   mutable branch URL), or rebuild from source with the recorded command.
2. Compute `sha256sum` and update the manifest entry: version, license,
   source URL, hash, date, reviewer.
3. Run `scripts/verify_vendor.sh` and `go test ./pkg/export -run VendorManifest`.
4. Mention the upgrade in the change description with the upstream release
   notes link.
