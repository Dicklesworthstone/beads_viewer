# Contributing

Contributions are welcome. This page covers where to send them and the few
things about this repository that are not obvious from looking at it.

For who maintains what, see [GOVERNANCE.md](GOVERNANCE.md). For the
maintainer-side mechanics of landing a merge request and cutting a release,
see [HACKING.md](HACKING.md).

## Found a security problem?

**Do not open a public issue.** [SECURITY.md](SECURITY.md) has three private
channels and what to expect from each.

## Where to send things

The canonical repository is GitLab
[cznic/sqlite](https://gitlab.com/cznic/sqlite); GitHub
[modernc-org/sqlite](https://github.com/modernc-org/sqlite) is a mirror.

- **A merge request on GitLab** is the shortest path, and it is where the
  issue numbers in `CHANGELOG.md` point.
- **A pull request on GitHub is accepted too.** It is cross-merged into GitLab
  by hand, so allow extra time. Nothing is lost, it just does not land the
  same day.
- For anything larger than a small fix, an issue first is welcome -- it can
  save you writing code that turns out to belong upstream in SQLite rather
  than here. A merge request that speaks for itself is fine as well.

## The one thing to know before editing

**Most of the Go in this repository is generated, and edits to it are silently
lost at the next re-vendoring.** The SQLite C amalgamation, the `sqlite-vec`
extension and the C side of the VFS bridge are transpiled to Go by
`modernc.org/ccgo`. A generated file carries Go's standard marker, a comment line
of the form

```
// Code generated ... DO NOT EDIT.
```

near the top. What comes between those words varies -- the transpiler's command
line, or `modernc.org/undup` for the per-target files it deduplicated -- so match
the convention, not any one wording. `grep -L 'DO NOT EDIT\.' lib/*.go` lists the
hand-written files.

| Path | Status |
| --- | --- |
| `lib/sqlite_*.go`, `lib/sqlite_g_*.go` | Generated from SQLite's C. Do not edit. |
| `vec/vec*.go` | Generated from `sqlite-vec`. Do not edit. |
| `vfs/vfs_*.go` | Generated from `vfs/c/vfs.c`. Edit the C, not the Go. |
| `vendor.json` | Written by `make vendor`: the sibling commits and Go toolchain `lib/` and `vec/` were vendored with, and a digest of them. Do not edit; the test suite fails when it does not match. |
| `lib/defs.go`, `lib/hooks*.go`, `lib/mutex.go`, `lib/libsqlite3_*.go` | Hand-written, no marker. Edit freely. |
| Everything at the top level, `vtab/`, `pcache/`, `examples/` | Hand-written. Edit freely. |

If a fix belongs in the C, it belongs in `modernc.org/libsqlite3` (which owns
the transpilation and the patch set) or upstream in SQLite itself -- say so in
the issue and it will be routed there.

The generated licence and SBOM documents -- `LICENSE-3RD-PARTY.md`,
`SBOM.md`, `sbom.cdx.json`, `sbom.spdx.json` -- are produced by `make sbom`.
Do not hand-edit those either; regenerate them if a dependency changes.

## Building and testing

```sh
make editor              # quick check: compiles tests, builds everything
go test -v -run TestFoo  # a single test; the suite is long
make test                # the whole suite; it is long
make test_pcache         # the suite again, through the pluggable page cache
make all                 # editor plus golint and staticcheck
make build_all_targets   # cross-build every supported GOOS/GOARCH
```

`gofmt -s` is expected. `make all` should be clean before you send anything.

CI here is deliberately small, in `.gitlab-ci.yml`. Three jobs check a merge
request, each only when something that feeds it changes: the generated licence
and SBOM documents are rebuilt and compared with what is committed,
`vendor.json` is checked against `go.mod` and the committed `lib/` and `vec/`,
and `attestgen/` runs its own tests. A fourth job runs only on the push of a
release tag, vendors `lib/` and `vec/` again from the commits `vendor.json`
names, and signs the release's provenance document; see
[VERIFYING.md](VERIFYING.md). **Nothing else runs in CI** -- no tests of the
driver, no cross-builds, so run those yourself. Platform coverage is checked by the
[modernc.org builder](https://modern-c.appspot.com/-/builder/?importpath=modernc.org%2fsqlite)
farm across the targets declared in `builder.json`, and the maintainer runs it
before tagging a release. If your change touches anything platform-specific,
run `make build_all_targets` yourself -- this module supports 20
`GOOS`/`GOARCH` combinations and a build break on one of them is easy to miss.
Do not count the per-target files to work out which: they are deduplicated, and
`lib/sqlite_windows.go` serves both `windows/amd64` and `windows/arm64`. Use
`go list` for a given target, or read `builder.json`.

Do not bump `modernc.org/libc` on its own. The generated code is tied to the
exact version `go.mod` pins; the two move together or downstream builds break.
See [GitLab issue #177](https://gitlab.com/cznic/sqlite/-/issues/177).

Do not tag releases. Tagging here is manual and deliberate; see
[HACKING.md](HACKING.md).

## Commits and credit

- Commit subjects in this repository name the files they touch:
  `driver.go, doc.go: document SQLite's own URI query parameters`. The body
  explains why, not what.
- Reference GitLab issues as `#NNN`.
- **Add yourself to [AUTHORS](AUTHORS) and [CONTRIBUTORS](CONTRIBUTORS)** in
  the same change, keeping both sorted. They are different lists: `AUTHORS` is
  copyright holders, so if you are contributing work owned by your employer,
  the employer belongs there; `CONTRIBUTORS` is people.
- You do not need to write a `CHANGELOG.md` entry. The maintainer writes one
  when the change lands and credits you by name in it.

## Licensing

Contributions are accepted under this project's licence, BSD-3-Clause; see
[LICENSE](LICENSE). There is no CLA to sign.

If your change includes code from somewhere else, even a few lines, say so in
the merge request and name the source and its licence. This module ships a
full third-party inventory in [LICENSE-3RD-PARTY.md](LICENSE-3RD-PARTY.md) and
an SBOM in [SBOM.md](SBOM.md); both have to keep telling the truth.
