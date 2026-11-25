# Dependency Upgrade Log

**Date:** 2026-01-18  |  **Project:** beads_viewer  |  **Language:** Go

## Summary
- **Updated:** 27 modules
- **Skipped:** 0
- **Failed:** 0
- **Needs attention:** 0

## Major Updates

| Module | Old | New |
|--------|-----|-----|
| git.sr.ht/~sbinet/gg | v0.6.0 | v0.7.0 |
| github.com/alecthomas/chroma/v2 | v2.14.0 | v2.23.0 |
| github.com/charmbracelet/colorprofile | v0.2.3 | v0.4.1 |
| github.com/charmbracelet/x/ansi | v0.10.1 | v0.11.4 |
| github.com/lucasb-eyer/go-colorful | v1.2.0 | v1.3.0 |
| github.com/mattn/go-runewidth | v0.0.16 | v0.0.19 |
| github.com/ncruces/go-strftime | v0.1.9 | v1.0.0 |
| golang.org/x/image | v0.25.0 | v0.35.0 |
| golang.org/x/sync | v0.16.0 | v0.19.0 |
| golang.org/x/term | v0.31.0 | v0.39.0 |
| gonum.org/v1/gonum | v0.16.0 | v0.17.0 |
| modernc.org/sqlite | v1.40.1 | v1.44.2 |

## Verification

- `go build ./...` - Build successful
- `go test ./...` - All tests pass (25 packages)
- `go mod vendor` - Vendor directory synced

---

**Date:** 2026-11-25  |  **Project:** beads_viewer  |  **Language:** Go 1.26.11

`go get -u ./...` offered six updates. One was taken; five were held.

| Module | Old | New | Decision |
|--------|-----|-----|----------|
| github.com/charmbracelet/x/exp/slice (indirect, via glamour) | v0.0.0-20260511125431-fe5d686e0c99 | v0.1.0 | updated: the tag is byte-identical to the pseudo-version already in use |
| github.com/alecthomas/chroma/v2 | v2.24.1 | v2.27.0 | held: patched copy in `third_party/chroma`; bumping `go.mod` alone would not change the code that builds. Upgrade by re-porting the patch (docs/RELEASING.md, "Vendored dependency patches") |
| github.com/goccy/go-json | v0.10.6 | v0.12.0 | held: patched copy in `third_party/go-json` (decoder-cache fix, docs/PROVENANCE.md), and on the JSONL loader hot path, so an upgrade needs the patch re-ported plus a loader benchmark A/B |
| modernc.org/libc, modernc.org/memory (indirect) | v1.80.1, v1.12.1 | v1.82.13, v1.13.0 | held: `modernc.org/sqlite` v1.63.0 (the latest) requires exactly libc v1.80.1; modernc.org/sqlite must run with the libc version it was generated against |
| github.com/xo/terminfo (indirect, via colorprofile) | v0.0.0-20220910002029-abceb7e1c41e | v1.3.1 | held: v1 changes color-level detection (a terminal whose terminfo reports 8-16 colors gets basic color instead of none), which could change TUI rendering; wait for colorprofile to adopt it |

Verification: `go mod tidy`, `go mod vendor` (only `vendor/modules.txt` changes), `go vet ./...` and `go test -p 4 ./...`.
