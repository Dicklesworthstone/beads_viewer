# Software Bill of Materials

This repository ships a machine-readable SBOM in both common formats, generated from the
same inventory as [LICENSE-3RD-PARTY.md](LICENSE-3RD-PARTY.md):

| File | Format | For |
| --- | --- | --- |
| [`sbom.cdx.json`](sbom.cdx.json) | CycloneDX 1.6 | Scanners and policy engines. Carries `pedigree` notes and per-component `scope`. |
| [`sbom.spdx.json`](sbom.spdx.json) | SPDX 2.3 | License and provenance review. Carries precise relationships and the full text of every non-standard license. |
| [`LICENSE-3RD-PARTY.md`](LICENSE-3RD-PARTY.md) | Markdown | People. Full license texts, transitively flattened. |

This page is generated too. Regenerate all four documents with `make sbom`; `licgen -check`
writes nothing and fails if any of them is stale, which is the form to run in CI.

## Why this is not what a stock Go SBOM tool produces

Run `cyclonedx-gomod` or `syft` against this module and the result will be a clean,
complete-looking list of Go modules that is missing the single largest thing the module
ships. `lib/` is SQLite itself: the C amalgamation, transpiled to Go by
`modernc.org/ccgo` and committed here. It is not a dependency, it has no `go.mod` entry,
and nothing in the module graph names it. The same is true of `sqlite-vec` in `vec/` and
of the VFS bridge in `vfs/`.

A consumer asking "does this project ship SQLite 3.53.4, and am I exposed to a CVE
against it" gets the wrong answer from a module-graph SBOM: not a cautious answer, a
wrong one. These documents name it, version it, and mark it with a pedigree note saying
where it came from.

The other direction matters too. `modernc.org/libc` carries notices for upstreams
vendored into it -- musl libc among them -- which no module graph reports either. Those
are listed as components in their own right, related to `modernc.org/libc` by
containment.

## What the scopes mean

Every component is classified by what it does to a program that imports this driver.
This is the field worth reading first, and the one a module list cannot fill in:

| Components | CycloneDX `scope` | SPDX relationship | Meaning |
| --- | --- | --- | --- |
| 14 | `required` | `DEPENDS_ON` / `CONTAINS` | Linked into your binary. Their licenses are the ones you carry when you redistribute. |
| 2 | `optional` | `TEST_DEPENDENCY_OF` | Compiled into this repository's own tests only. Never reaches your program. |
| 17 | `excluded` | `OPTIONAL_DEPENDENCY_OF` | Present in `go list -m all`, compiled into nothing here. Reported because `go.sum` and module-graph scanners will surface them. |
| 1 | `excluded` | `CONTAINS` | Third-party material committed to this repository and shipped in the module zip, compiled into nothing. |

The distinction is not academic. The only copyleft-adjacent license anywhere in this
project, MPL-2.0, sits in the third group: `github.com/hashicorp/golang-lru/v2`, reached
through `modernc.org/libc`'s `go.mod` and in no import closure. A scanner that reads the
module graph will flag it; these documents say plainly that nothing of it is compiled
in.

## Inventory

Full license texts are in [LICENSE-3RD-PARTY.md](LICENSE-3RD-PARTY.md); a `LicenseRef-`
identifier below names the appendix entry there that holds the text, and the text is
embedded in both JSON documents as well.

### 1. Components linked into your program

| Component | Version | License | Package URL |
| --- | --- | --- | --- |
| github.com/dustin/go-humanize | v1.1.0 | `MIT` | `pkg:golang/github.com/dustin/go-humanize@v1.1.0` |
| github.com/google/uuid | v1.6.0 | `BSD-3-Clause` | `pkg:golang/github.com/google/uuid@v1.6.0` |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | `BSD-3-Clause` | `pkg:golang/github.com/remyoudompheng/bigfft@v0.0.0-20230129092748-24d4a6f8daec` |
| Go | -- | `BSD-3-Clause` | -- |
| go-netdb | -- | `MIT` | -- |
| golang.org/x/sys | v0.49.0 | `BSD-3-Clause` | `pkg:golang/golang.org/x/sys@v0.49.0` |
| modernc.org/libc | v1.80.1 | `BSD-3-Clause` | `pkg:golang/modernc.org/libc@v1.80.1` |
| modernc.org/mathutil | v1.7.1 | `BSD-3-Clause` | `pkg:golang/modernc.org/mathutil@v1.7.1` |
| modernc.org/memory | v1.12.1 | `BSD-3-Clause AND LicenseRef-L5` | `pkg:golang/modernc.org/memory@v1.12.1` |
| musl libc | -- | `MIT` | -- |
| NixOS/nixpkgs | -- | `MIT` | -- |
| SQLite | 3.54.1 | `LicenseRef-SQLite-PublicDomain` | `pkg:generic/sqlite@3.54.1` |
| SQLite `test_demovfs.c` | -- | `LicenseRef-SQLite-PublicDomain` | -- |
| sqlite-vec | v0.1.9 | `MIT` | `pkg:github/asg017/sqlite-vec@v0.1.9` |

### 2. Components used only by the test suite

| Component | Version | License | Package URL |
| --- | --- | --- | --- |
| github.com/google/pprof | v0.0.0-20261016150608-1facf8f91b50 | `Apache-2.0 AND BSD-3-Clause` | `pkg:golang/github.com/google/pprof@v0.0.0-20261016150608-1facf8f91b50` |
| modernc.org/fileutil | v1.4.0 | `BSD-3-Clause` | `pkg:golang/modernc.org/fileutil@v1.4.0` |

### 3. Components in the module graph, not compiled into anything

| Component | Version | License | Package URL |
| --- | --- | --- | --- |
| github.com/chzyer/readline | v1.5.1 | `MIT` | `pkg:golang/github.com/chzyer/readline@v1.5.1` |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | `MPL-2.0 AND BSD-3-Clause` | `pkg:golang/github.com/hashicorp/golang-lru/v2@v2.0.7` |
| github.com/ianlancetaylor/demangle | v0.0.0-20250417193237-f615e6bd150b | `BSD-3-Clause` | `pkg:golang/github.com/ianlancetaylor/demangle@v0.0.0-20250417193237-f615e6bd150b` |
| github.com/mattn/go-isatty | v0.0.24 | `MIT` | `pkg:golang/github.com/mattn/go-isatty@v0.0.24` |
| github.com/ncruces/go-strftime | v1.1.0 | `MIT` | `pkg:golang/github.com/ncruces/go-strftime@v1.1.0` |
| golang.org/x/mod | v0.42.0 | `BSD-3-Clause` | `pkg:golang/golang.org/x/mod@v0.42.0` |
| golang.org/x/sync | v0.24.0 | `BSD-3-Clause` | `pkg:golang/golang.org/x/sync@v0.24.0` |
| golang.org/x/tools | v0.52.0 | `BSD-3-Clause` | `pkg:golang/golang.org/x/tools@v0.52.0` |
| modernc.org/cc/v4 | v4.30.6-0.20261101114202-1b9d1a076f6b | `BSD-3-Clause` | `pkg:golang/modernc.org/cc/v4@v4.30.6-0.20261101114202-1b9d1a076f6b` |
| modernc.org/ccgo/v4 | v4.38.1 | `BSD-3-Clause` | `pkg:golang/modernc.org/ccgo/v4@v4.38.1` |
| modernc.org/gc/v2 | v2.6.5 | `BSD-3-Clause` | `pkg:golang/modernc.org/gc/v2@v2.6.5` |
| modernc.org/gc/v3 | v3.2.0 | `BSD-3-Clause` | `pkg:golang/modernc.org/gc/v3@v3.2.0` |
| modernc.org/goabi0 | v0.2.0 | `BSD-3-Clause` | `pkg:golang/modernc.org/goabi0@v0.2.0` |
| modernc.org/opt | v0.2.0 | `BSD-3-Clause` | `pkg:golang/modernc.org/opt@v0.2.0` |
| modernc.org/sortutil | v1.2.1 | `BSD-3-Clause` | `pkg:golang/modernc.org/sortutil@v1.2.1` |
| modernc.org/strutil | v1.2.1 | `BSD-3-Clause` | `pkg:golang/modernc.org/strutil@v1.2.1` |
| modernc.org/token | v1.1.0 | `BSD-3-Clause` | `pkg:golang/modernc.org/token@v1.1.0` |

### 4. Third-party material in the repository, outside any build

| Component | Version | License | Package URL |
| --- | --- | --- | --- |
| SQLite TCL test suite | -- | `LicenseRef-SQLite-PublicDomain` | -- |

## What these documents do not claim

- No file-level hashes or a package verification code: `filesAnalyzed` is `false`
  throughout. The documents describe components, not a file manifest.
- No build attestation or signature. These say what is in the tree, not who built a given
  binary from it.
- The upstreams reached through a dependency's notices carry no version. The notice names
  the project, not the revision that was vendored, and guessing one would be worse than
  saying so.
- The subject's own version is `NOASSERTION`. The document describes a source tree; the
  release tag is applied later, by hand, once the builders are green.
- Timestamps are fixed at the Unix epoch and the SPDX document namespace is derived from
  the document's content. Both are deliberate: a regenerated document for an unchanged
  tree is byte-identical, which is what makes `licgen -check` meaningful. The real date of
  the document is the date of the commit that last changed it.

<!--
Generated by licensegen. Do not edit by hand: run `make sbom`.
-->
