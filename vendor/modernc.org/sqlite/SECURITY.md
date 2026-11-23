# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.** Any of the three
private channels below works; use whichever suits you.

1. **GitHub** -- [Report a vulnerability](https://github.com/modernc-org/sqlite/security/advisories/new).
   Private vulnerability reporting is enabled on the mirror, so this opens a
   thread visible only to you and the maintainers, and it gives us a draft
   advisory to work from. This is the quickest route.

2. **GitLab**, the canonical repository -- open a
   [new issue](https://gitlab.com/cznic/sqlite/-/issues/new) and tick
   **"This issue is confidential"** before submitting. Confidential issues are
   visible only to project members.

3. **Email**, if you would rather not use either platform -- write to
   `contact-project+cznic-sqlite-9241019-issue-@incoming.gitlab.com`. That is
   the project's GitLab Service Desk address. On a public project every ticket
   it creates is **confidential by default**, so the report is private from the
   moment it arrives, and no account is needed to send it.

The GitHub repository is a mirror and development happens on GitLab, but a
report filed there notifies the maintainers directly. Do not worry about
picking the right one.

### What helps in a report

- The module version, and the `modernc.org/libc` version your `go.mod` pins.
- `GOOS`/`GOARCH`, since the C is transpiled per target and a fault may exist
  on only some of them.
- The DSN, including any `_pragma` or other query parameters.
- A reproducer: a Go test, a database file, or a sequence of statements.
- Whether triggering it needs a hostile *database file*, hostile *SQL text*,
  or only hostile *data* in a table.
- What you believe the impact is. A guess is fine; we would rather discuss it
  than not hear about it.

## What to expect

- We aim to **acknowledge a report within 7 days**.
- We do **not** promise a fix deadline. The project has two active
  maintainers, and what a fix costs depends on where the flaw is: driver code
  is ours to change today, while a flaw in the transpiled C may mean
  re-vendoring and a coordinated `modernc.org/libc` release.
- What we can promise: a remotely exploitable flaw takes priority over
  everything else in the project, and you will be told what is happening
  either way rather than left waiting.
- We will credit you in the advisory and in the release notes unless you would
  rather not be named.

## What happens on our side

[IRP.md](IRP.md) is the incident response plan: how a confirmed report is triaged,
scoped, fixed across the layers this module is built from, disclosed, and -- if a
released version turns out to be the problem -- retracted. It is published so that
the process is inspectable rather than asserted.

## Supported versions

**The latest release only.** Fixes land on `master` and ship in a new tag;
there are no maintenance branches and never have been. The `retract`
directives in `go.mod` mark versions that should not be used.

Each release after v1.60.1 has a signed provenance document, linked from its
[GitLab release](https://gitlab.com/cznic/sqlite/-/releases); [VERIFYING.md](VERIFYING.md)
says what it proves and how to check it. v1.60.1 and earlier releases have none.

## Scope

In scope, with a security consequence:

- Memory unsafety, panics, or silently wrong results reachable from SQL text
  or from table data that the application does not control.
- A database file that causes out-of-bounds access, a crash, or undetected
  corruption. Reports of memory unsafety from a hostile or corrupt file are
  wanted; a corrupt file that is cleanly *rejected with an error* is working
  as intended.
- **Transpilation faults**: a place where the generated Go in `lib/`, `vec/`
  or `vfs/` does not faithfully implement the C it was produced from. These
  are ours, not upstream's, and they are the class of bug unique to this
  project.
- Flaws in the driver layer: DSN parsing, connection hooks, user-defined
  functions, virtual tables, the VFS bridge, and the file-locking integration.
- The pluggable page cache, when an application registers one with
  `RegisterPageCache`: the binding in `pagecache.go` and
  `pagecache_trampolines.go`, which SQLite then calls for every page it
  reads, and the reference implementation in `pcache/`. A fault there -- a
  page returned for the wrong key, or freed while SQLite still holds it -- is
  silent corruption, not a crash. Without `RegisterPageCache` SQLite uses its
  own page cache and none of this code runs.

Out of scope:

- SQL injection in an application that concatenates untrusted input into
  statements. Use placeholders.
- What an attacker can do by writing the DSN. A DSN names the file to open
  or create, selects the VFS, and can run SQL through `_pragma`: it carries
  the authority of the application that opens it, and one taken from an
  untrusted source is equivalent to untrusted SQL plus file system access.
  [`StrictPragmas`](https://pkg.go.dev/modernc.org/sqlite#StrictPragmas)
  narrows it, and a way around `StrictPragmas`, or around the rule that a
  rejected DSN applies nothing, is in scope.
- Resource exhaustion from queries that are simply expensive. SQLite's own
  limits are the tool for that.
- Anything reproducible only after modifying the generated sources by hand.
- Vulnerabilities in dependencies as such -- report those to their
  maintainers. Do tell us if a version we ship is affected;
  [SBOM.md](SBOM.md) and [LICENSE-3RD-PARTY.md](LICENSE-3RD-PARTY.md) list
  exactly what this module carries and what is merely in the module graph.

[THREAT-MODEL.md](THREAT-MODEL.md) goes through the driver layer, the page cache
binding and the VFS bridge row by row, with the status of each.

### Vulnerabilities in SQLite itself

This module does not link SQLite; it ships SQLite's C amalgamation transpiled
to Go, so a flaw in SQLite's own code is a flaw here too, and the fix reaches
you through a new release of this module rather than through your system
packages. Report it here so that we re-vendor, and consider reporting it
upstream at [sqlite.org](https://www.sqlite.org/support.html) as well -- they
own the fix, we own shipping it.

## Disclosure

Once a report is confirmed:

1. The fix lands on `master` and goes out in a new release.
2. A [GitHub Security Advisory](https://github.com/modernc-org/sqlite/security/advisories)
   is published on the mirror.
3. The advisory is filed with the
   [Go vulnerability database](https://go.dev/s/vulndb-report-new), so that
   `govulncheck` reports it to every user of the module automatically. This is
   the step that actually reaches Go developers, and we treat it as part of
   shipping the fix rather than as an optional extra.
4. The release entry in [CHANGELOG.md](CHANGELOG.md) says plainly what was
   wrong and who found it.

We will not publish before a fix is available unless the flaw is already
public, and we will coordinate timing with you.

## Hardening you can use today

Independent of any report, three opt-in measures exist:

- `_defensive=1` in the DSN turns on SQLite's defensive mode for the
  connection, making `PRAGMA writable_schema=ON`, `PRAGMA journal_mode=OFF`
  and `PRAGMA schema_version=N` no-ops and refusing writes to shadow tables
  and `sqlite_dbpage`. It is a hardening measure, not a sandbox for hostile
  database files; see
  [SQLite's own recommendations](https://www.sqlite.org/security.html).
- `MODERNC_SQLITE_OFD_LOCK=1`, or `OFDLocking(true)` before the first
  connection, switches Linux file locking to Open File Description locks,
  which survive a `Close` of any descriptor of the database file elsewhere in
  the process. Off by default.
- `StrictPragmas(true)`, called once from Go, makes every connection reject
  a `_pragma` DSN value holding more than one SQL statement, before anything
  is applied. Recommended for any application whose DSN is not a
  compile-time constant. Off by default.

All three are documented on `Driver.Open` and in the package documentation.
