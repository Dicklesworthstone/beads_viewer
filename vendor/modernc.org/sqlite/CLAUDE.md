# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this package is

`modernc.org/sqlite` is a pure-Go `database/sql/driver` for SQLite — **CGo-free**. The SQLite C amalgamation is transpiled to Go via `modernc.org/ccgo`; the generated code lives in this repo as per-`GOOS`/`GOARCH` files under `lib/` (SQLite itself), `vec/` (the `sqlite-vec` extension), and `vfs/` (the C side of the Go-fs VFS bridge). Runtime support — `malloc`, `pthread`, syscalls, etc. — is provided by `modernc.org/libc`.

The hand-written Go on top of that transpiled core implements the `database/sql/driver` shim and additional Go-facing APIs (virtual tables, VFS, hooks, UDFs).

## Repository layout (the parts that aren't self-evident)

- `sqlite.go`, `conn.go`, `driver.go`, `stmt.go`, `rows.go`, `tx.go`, `backup.go`, `error.go`, `result.go`, `convert.go` — hand-written `database/sql/driver` implementation calling into `lib/`.
- `vtab.go`, `pre_update_hook.go`, `fcntl.go`, `mutex.go`, `ofd.go` — Go-facing extensions wired to SQLite hooks/trampolines (`ofd.go`: the process-wide opt-in switch to Linux OFD locks, backed by `modernc_ofd_locking()` in the transpiled library; the C side lives in `../libsqlite3/internal/sqlite_issue255.patch{,2}`).
- `lib/` — transpiled SQLite 3.54.1. One `sqlite_<goos>_<goarch>.go` per supported triple plus build-tagged `sqlite_g_*.go` files holding declarations `modernc.org/undup` deduplicated across triples (so to check what code a target compiles, resolve its full GoFiles via `go list`, not by filename); `defs.go`, `hooks.go`, `hooks_linux_arm64.go`, `mutex.go`, plus `libsqlite3_freebsd.go`/`libsqlite3_windows.go` hold hand-written patches that augment the generated code. Import as `sqlite3 "modernc.org/sqlite/lib"`.
- `vec/` — transpiled `sqlite-vec` v0.1.9, auto-registers via `sqlite3_auto_extension` in `patches.go` on package init. Activate by blank-importing: `_ "modernc.org/sqlite/vec"`. Covers the same 21 targets `lib/` does (21 is the count in `builder.json` and `make build_all_targets`; the 20 per-target files are fewer because `windows/amd64` and `windows/arm64` share `sqlite_windows.go`); `vec_test.go`'s `//go:build` constrains by GOOS only.
- `vfs/` — exposes a Go `fs.FS` as a read-only SQLite VFS. `vfs.New(fsys)` returns a registered VFS name; open with `?vfs=<name>`. C side is transpiled per platform from `vfs/c/vfs.c` via the `vfs/Makefile`.
- `vtab/` — Go-facing virtual-table API (no dependency on the transpiled C). `vtab.RegisterModule(db, name, module)` registers modules on **new connections only**; a nil `db` targets the driver registered as `sqlite`, a non-nil `db` the driver backing it (via `vtab.ModuleRegisterer`). The bridge to C lives in the top-level `vtab.go`. See `vtab/doc.go` for the contract (Updater/Renamer/Transactional optional interfaces, re-entrancy rules, ArgIndex/Omit semantics).
- `licensegen/` (own module, build tag `none`, built with `go build -tags none .`) — generates `LICENSE-3RD-PARTY.md`, `SBOM.md`, `sbom.cdx.json` (CycloneDX 1.6) and `sbom.spdx.json` (SPDX 2.3) from `go list -m all`, the license files in the module cache, the notice files dependencies carry (`modernc.org/libc`'s `LICENSE-3RD-PARTY.md`, which is how musl reaches us), and the vendored C. Classifies every component as linked / test-only / module-graph-only. Deterministic: no timestamps anywhere, SPDX's required `created` pinned to the epoch and its `documentNamespace` derived from the document's own content, so regenerating an unchanged tree is byte-identical and `./licgen -check` is meaningful. Both JSON documents validate against the published CycloneDX 1.6 and SPDX 2.3 schemas. `sbom.go` holds the SBOM writers, `main.go` the inventory and the Markdown. Invoked by `make licenses`, and wired into one of the repository's CI jobs: `.gitlab-ci.yml` runs `licgen -check` when `go.mod`, `go.sum`, `licensegen/`, `lib/sqlite.go`, `vec/vec.go` or any of the four documents change; `vendor-stamp` runs `go test ./internal/vendorstamp/`; `attestgen` runs that tool's tests; `attest` runs only on the push of a `vX.Y.Z` tag. Nothing else runs in GitLab CI -- tests and cross-builds live on the builder farm. The `LICENSE` name prefix is load-bearing: `go mod vendor` matches metadata files by case-sensitive prefix, so `3RD_PARTY_LICENSES.md` would never reach downstream `vendor/` trees -- the same trap as the v1.57.0 `SQLITE-LICENSE` rename. Downloads into a scratch module so the repo's `go.sum` is never touched, and forces `GOWORK=off` so a `make work` workspace cannot leak into the document.
- `attestgen/` (own module, build tag `none`, stdlib only) — the release tag's provenance tool, run by the `attest` job. `-siblings` prints the libsqlite3/libsqlite_vec repositories and commits vendor.json names, which the job clones next to the checkout before `make vendor-check` (deletes every file vendor.json covers, vendors again without the cross-builds, stamps again, and fails unless those files and vendor.json, apart from its `go` field, come out as committed). `-observe` checks the job's ID token claims (`-check-token`: push trigger, GitLab-hosted runner, project, tag, commit) so nothing is signed that `-verify` would reject, checks that HEAD and the tag are `CI_COMMIT_SHA`, downloads the module from gitlab.com and from proxy.golang.org with sum.golang.org, each into its own empty scratch module and cache with none of the caller's Go or git settings, and writes `provenance.json` only if both agree and the direct download came from that commit. `-verify` runs `cosign verify-blob` for the tag pipeline's identity, then checks the Fulcio certificate claims cosign has no GitLab flags for (commit, ref, project ID 9241019, push trigger, GitLab-hosted runner) and compares the document with the module the proxy serves. `-publish` stores the document and bundle in the generic package registry and links them from the GitLab release, never overwriting; it reads every page of the package listings, hidden packages included, and keeps the job token off redirects to object storage. See `VERIFYING.md`.
- `vendor_libs/main.go` (build tag `none`) — regeneration tool. Reads transpiled `ccgo_<goos>_<goarch>.go` from sibling repos `../libsqlite3` and `../libsqlite_vec`, rewrites package names and imports, and writes `lib/sqlite_*.go` / `vec/vec_*.go`. Invoked by `make vendor`. `vendor_libs/stamp.go` is its first and last step: `-preflight` refuses dirty or mismatched sibling checkouts before anything is touched, `-stamp` writes `vendor.json` (sibling commits, Go toolchain, undup pin, digest of the output) once the cross-builds have passed. `internal/vendorstamp` checks that file; `vendorstamp_test.go` runs the check in the suite, so the builders catch a stale or dirty stamp.
- `examples/` — runnable samples: `example1`, `connector`, `vtab_basic`, `vtab_csv`, `vtab_match`, `vtab_regexp`.
- `addport.go`, `issue198/`, `issue120.diff` — porting/regression scaffolding kept around for reference; not built.

## Commands

```bash
make editor              # quick local check: go test -c + go build ./... + vendor_libs build
make test                # go test -v -timeout 24h (the full suite is long)
make build_all_targets   # cross-build every supported GOOS/GOARCH
make vendor              # regenerate lib/ and vec/ from sibling ../libsqlite3 + ../libsqlite_vec
make sbom                # regenerate LICENSE-3RD-PARTY.md, SBOM.md, sbom.cdx.json, sbom.spdx.json
make licenses            # alias for make sbom
make all                 # editor + golint + staticcheck
make work                # set up go.work pointing at sibling cc/ccgo/libc/libtcl8.6/libsqlite3/libz repos
make clean               # removes log-*, *.test, *.out, go.work*
```

Single test: `go test -v -run TestScalar` (pattern is a regexp; tests live in `all_test.go`, `module_test.go`, `func_test.go`, `pre_update_hook_test.go`, `vec_test.go`, `leak_test.go`, `fcntl_test.go`, `backup_test.go`, `null_test.go`). VFS tests: `go test ./vfs/...`.

Build/debug tags:
- `-tags=sqlite.dmesg` — enables this package's `dmesg(...)` (writes to `/tmp/libc.log`); see `dmesg.go` / `nodmesg.go`.
- `-tags=libc.dmesg` — enables debug logs from `modernc.org/libc` (must be combined with patching `libc` itself — see the worked example in `doc.go`).

There is no `go generate` in this repo — `generator.go` lives in `../libsqlite3`, which owns the transpilation and its SQLite compile-time options. To produce a debug-instrumented transpilation, change the options there, `make generate` in that repo, then `make vendor` here.

## Fragile `modernc.org/libc` coupling

Downstream `go.mod` files **must pin the exact `modernc.org/libc` version that this repo's `go.mod` pins** — the transpiled code in `lib/` is closely tied to that specific `libc`. This is documented in `doc.go` and tracked in [issue #177](https://gitlab.com/cznic/sqlite/-/issues/177). Bumping `libc` here without re-transpiling (or vice-versa) breaks consumers; that's why `v1.33.0`, `v1.34.3`, and `v1.42.0` are retracted in `go.mod`.

When debugging into `libc`, use `make work` (or a manual `go work init && go work use . <path-to-libc>`) — `doc.go` has a worked example showing how to enable `Xwrite` dmesg logging in a local `libc` checkout.

## Security policy

`SECURITY.md` is the published policy; keep answers consistent with it. In short: reports go
through GitHub private vulnerability reporting (enabled on the mirror), a **confidential**
GitLab issue, or the project's GitLab Service Desk address (tickets on a public project are
always confidential, verified in the UI 2026-09-20), never a public issue; acknowledgement is aimed at 7 days with **no fix deadline
promised**; **only the latest release is supported** -- no maintenance branches; transpilation
faults in `lib/`, `vec/` and `vfs/` are explicitly in scope and are this project's own bug
class, so is the pluggable page cache when registered -- the binding in `pagecache.go`/`pagecache_trampolines.go` plus `pcache/` (a fault there is silent corruption; unregistered, none of it runs), an attacker-controlled DSN is explicitly **out** of scope (it is untrusted SQL plus file system access) while a bypass of `StrictPragmas` or of the rejected-DSN-applies-nothing rule is in, while flaws in SQLite's C are reported here *and* upstream. On a confirmed report the
fix ships in a new release, a GitHub advisory is published, **the advisory is filed with the
Go vulnerability database** (`https://go.dev/s/vulndb-report-new`) so `govulncheck` sees it,
and the CHANGELOG says what was wrong and who found it.

`IRP.md` is the incident response plan and the procedural companion to `SECURITY.md`: triage,
scoping by layer (hand-written Go / generated Go / SQLite's own C), the fix paths -- including
the fixed four-release order when a fix moves `libc` (libc -> libsqlite3 -> libsqlite_vec -> here)
-- and the four rules that do not bend (never merge on the mirror, never bump `libc` alone, tag only on green
builders, `make sbom` after dependency changes), disclosure, and Phase 5 -- **a published Go
module version cannot be recalled**, so `retract` plus a new release is the only remedy.

`THREAT-MODEL.md` is the working sheet behind `SECURITY.md`'s scope: four surfaces (the DSN, the
connection lifecycle, the page cache binding, the VFS bridge), five positions (A1 writes the DSN
... A5 is another user of the same process), one row per concern with its status -- Fixed or
Documented with the commit, Holds with where the code keeps it, Struck with the reason, Out of
scope, or Open with what is missing. When a change touches one of those surfaces, update its row.

## Repository / release workflow

- The canonical repo is GitLab `cznic/sqlite`. The GitHub `modernc-org/sqlite` mirror **does accept** issues and PRs, but PRs land via a manual cross-merge into GitLab — there can be a delay. The PRs listed in `CHANGELOG.md` (e.g. "merge request #113") are GitLab MR numbers, not GitHub PRs.
- Per `HACKING.md`: this repo is **not** auto-tagged — `builder.json` has `"autotag": "<none>"` because too many projects depend on `modernc.org/sqlite` to risk bot tagging. Releases are tagged **manually by the maintainer**; don't tag unless asked, and only once [the builder dashboard](https://modern-c.appspot.com/-/builder/?importpath=modernc.org%2fsqlite) is green for all platforms listed in `builder.json`.
- `go.mod` `retract` directives encode known-broken versions; treat them as load-bearing — don't remove entries when bumping the module.

## Driver registration model

`init()` in `sqlite.go` calls `sql.Register("sqlite", defaultDriver())` with a single package-level `*Driver` (`var d` in `driver.go`). The package-level registration functions (`RegisterFunction`, `RegisterScalarFunction`, `RegisterDeterministicScalarFunction`, `RegisterCollationUtf8`, `RegisterConnectionHook`) attach to that driver alone; its vtab modules — `vtab.RegisterModule` with a nil `db` — are the one category held process-globally and reach every driver's connections. A caller-constructed `Driver` carries its own registrations through the mirroring `*Driver` methods (`RegisterFunction`, `RegisterScalarFunction`, `RegisterDeterministicScalarFunction`, `RegisterCollationUtf8`, `RegisterConnectionHook`, `RegisterModule`, plus `Must*` variants), and `vtab.RegisterModule` with a non-nil `db` lands on the driver backing that `db`. Same-name module on both: the package-level implementation wins on that driver's connections. All registration applies to connections opened **afterwards**; registrations made after a connection is open do not affect it — open a new one. See the `Driver` type doc in `driver.go` and `vtab/doc.go`.

DSN query params are parsed in `conn.go`/`driver.go`: `_pragma`, `_time_format`, `_time_integer_format`, `_inttotime`, `_texttotime`, `_timezone`, `_txlock`, plus `vfs=<name>` to select a VFS registered via `vfs.New`. A `_pragma` value runs as SQL text, not as one PRAGMA -- anything after a `;` runs too; `StrictPragmas(true)` (`strictpragma.go`, process-wide, off by default, deliberately not a DSN key) rejects multi-statement values in the validation phase with a lexical check that compiles nothing, held to SQLite's parser by `FuzzSingleStatement`.

The pluggable page cache (`RegisterPageCache`, `pagecache*.go`, reference impl `pcache/`) is opt-in and cannot be combined with `modernc.org/sqlite/vec` (vec's init initializes SQLite first -> SQLITE_MISUSE). The binding panics if a Cache drops or replaces a pinned page. End-to-end coverage: `make test_pcache` (`-tags pcachepool`: drops `vec_test.go`, registers `pcache.New()` in `pcachepool_test.go`).
