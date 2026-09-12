# Dependency Upgrade Log

**Date:** 2026-09-12 UTC | **Project:** beads_viewer | **Status:** in progress

## September release update

The user requested `library-updater` followed by a new release. Each selected
dependency is researched and tested separately. Local replacements and
pseudo-version pins are preserved; modules present only in dependency tooling
graphs are distinguished from this project's declared requirements. The raw
registry inventory is `/data/tmp/bv-release-module-inventory-20260911.json`.
Release preparation uses DSR/RCH and the complete repository release gate;
GitHub Actions must not run. Existing incomplete P1/native evidence remains open.

- [x] Inventory Go requirements and upstream stable versions.
- [ ] Research and update each eligible declared requirement, testing each.
- [ ] Inspect the two Rust/WASM manifests and embedded asset requirements.
- [ ] Run final full tests and vulnerability audit.
- [ ] Update changelog/version and pass the complete release gate.
- [ ] Build and verify all five configured release targets.
- [ ] Publish GitHub release, Homebrew tap and Scoop bucket; verify installers.

### Current candidate: github.com/charmbracelet/x/ansi v0.11.7 → v0.11.8

Research and unchanged-source baseline testing are in progress. Upstream source
is tag `ansi/v0.11.8`, commit `00c6608f106b9c6cd8a1a77156f7901f41265e64`.
[Upstream comparison](https://github.com/charmbracelet/x/compare/ansi/v0.11.7...ansi/v0.11.8).

---

**Date:** 2026-06-08 | **Project:** beads_viewer | **Language:** Go | **Release:** v0.17.0

## Summary

- **Updated:** 7 direct Go dependencies (all minor/patch bumps of mature libraries) + transitive `golang.org/x/text` and re-vendored.
- **Method:** Staged by risk — `golang.org/x/*` + `go-runewidth` together (official, near-zero risk), then `modernc.org/sqlite` alone (FTS5 search engine), then `git.sr.ht/~sbinet/gg` alone (graphics/export). Full `go test ./...` gate after the batch; focused `pkg/search` + `pkg/export` (FTS5) gate after the sqlite bump.
- **Failed:** None. **Needs attention:** None.
- **Validation:** `go vet ./...` clean, `gofmt -l` clean, `go test ./...` 27 packages OK / 0 FAIL (with `BV_NO_BROWSER=1 BV_TEST_MODE=1 CGO_CFLAGS=-DSQLITE_ENABLE_FTS5`).

### Direct dependencies

- `git.sr.ht/~sbinet/gg`: `v0.7.0` -> `v0.8.0`
- `github.com/mattn/go-runewidth`: `v0.0.23` -> `v0.0.24`
- `golang.org/x/image`: `v0.40.0` -> `v0.42.0`
- `golang.org/x/sync`: `v0.20.0` -> `v0.21.0`
- `golang.org/x/sys`: `v0.44.0` -> `v0.46.0`
- `golang.org/x/term`: `v0.43.0` -> `v0.44.0`
- `modernc.org/sqlite`: `v1.50.1` -> `v1.52.0`

### Notable indirect dependency updates

- `golang.org/x/text`: `v0.37.0` -> `v0.38.0` (pulled by the x/* bumps)

Remaining outdated entries reported by `go list -m -u` are all transitive-only deps not required by `go mod why` (e.g. `gonum.org/v1/plot`, `honnef.co/go/tools`, `codeberg.org/go-latex/*`); left untouched to keep the release surface minimal.

---

**Date:** 2026-05-14 | **Project:** beads_viewer | **Languages:** Go, Rust/WASM

## Summary

- **Updated:** Go module/vendor dependencies and Rust/WASM lockfiles.
- **Skipped:** Unneeded modules that only appear in the broader module graph and are not required by `go mod why -m`.
- **Failed:** None.
- **Needs attention:** None.

## Local `/dp` Dependency Check

### github.com/Dicklesworthstone/toon-go

- **Current:** `v0.0.0-20260322013033-4564467a45fb`
- **Local `/dp` state:** `/dp/toon-go` is on commit `4564467a45fbb70f24b08cf500d7637b24e65e56`.
- **Action:** Preserved. The project is already using the latest local `/dp/toon-go` version.

## Go Updates

### Direct dependencies

- `github.com/fsnotify/fsnotify`: `v1.9.0` -> `v1.10.1`
- `github.com/mattn/go-runewidth`: `v0.0.21` -> `v0.0.23`
- `golang.org/x/image`: `v0.37.0` -> `v0.40.0`
- `golang.org/x/sys`: `v0.42.0` -> `v0.44.0`
- `golang.org/x/term`: `v0.41.0` -> `v0.43.0`
- `modernc.org/sqlite`: `v1.47.0` -> `v1.50.1`
- `pgregory.net/rapid`: `v1.2.0` -> `v1.3.0`

### Notable indirect dependency updates

- `github.com/alecthomas/chroma/v2`: `v2.23.1` -> `v2.24.1`
- `github.com/charmbracelet/x/ansi`: `v0.11.6` -> `v0.11.7`
- `github.com/dlclark/regexp2`: `v1.11.5` -> `v1.12.0`
- `github.com/lucasb-eyer/go-colorful`: `v1.3.0` -> `v1.4.0`
- `github.com/mattn/go-isatty`: `v0.0.20` -> `v0.0.22`
- `github.com/sahilm/fuzzy`: `v0.1.1` -> `v0.1.2`
- `github.com/yuin/goldmark`: `v1.7.17` -> `v1.8.2`
- `golang.org/x/net`: `v0.52.0` -> `v0.54.0`
- `golang.org/x/text`: `v0.35.0` -> `v0.37.0`
- `golang.org/x/tools`: `v0.43.0` -> `v0.45.0`
- `modernc.org/libc`: `v1.70.0` -> `v1.72.3`

## Rust/WASM Updates

### bv-graph-wasm

- `getrandom`: `0.2` with `js` feature -> `0.4` with `wasm_js` feature.
- Updated call site from `getrandom::getrandom` to `getrandom::fill`.
- Refreshed `Cargo.lock` to latest compatible stable versions.

### pkg/export/wasm_scorer

- Refreshed `Cargo.lock` to latest compatible stable versions.
- Added `rlib` crate type alongside `cdylib` so integration tests can import the crate while preserving WASM output.

## Validation

Final validation was run after updates:

- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go vet ./...`
- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go build ./...`
- `BV_NO_BROWSER=1 BV_TEST_MODE=1 go test ./...`
- `cargo fmt --check`, `cargo clippy -- -D warnings`, and `cargo test` in both Rust/WASM crates.

Additional release-gate commands are tracked in the release session notes and final response.
