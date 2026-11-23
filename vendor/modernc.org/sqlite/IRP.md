# Incident Response Plan

How the maintainers of `modernc.org/sqlite` respond when something goes wrong: a
confirmed vulnerability, a bad release, or a compromised account.

[SECURITY.md](SECURITY.md) is the public-facing document -- how to report, what to
expect, what is in scope. This one is the procedure we follow afterwards. It is
published for the same reason Bootstrap publishes theirs: a plan nobody can read is
hard to trust. Operational detail that would help an attacker -- the credential
inventory, where each token lives, how to revoke it -- is kept in the maintainers'
private notes, not here.

## What counts as an incident

- A confirmed vulnerability in code we ship: the hand-written driver, the transpiled
  SQLite in `lib/`, `sqlite-vec` in `vec/`, or the VFS bridge in `vfs/`.
- A vulnerability in SQLite itself, which reaches users through this module rather
  than through their system packages.
- A released version that is broken, wrong, or malicious.
- Compromise of a maintainer account, an access token, or the push mirror.
- A secret committed to the repository or exposed in logs.
- A builder result that cannot be trusted: a target reported green that did not
  build or pass. The builder dashboard is what a release is tagged on, so a false
  green is a release decision made on bad data. Every tag made on it is suspect
  until the affected targets are re-run; if one then fails, Phase 5 applies.

An ordinary bug, however severe, is not an incident. It goes through the normal
issue and release process.

## Who runs it

There are two maintainers, roughly six hours apart (Central European and UTC+8).
**Whoever picks the report up first says so explicitly in the thread and runs it**
until they hand over. That one sentence prevents the common failure of a two-person
team: both people investigating, or each assuming the other has.

## Phase 1 -- Triage

1. Acknowledge the reporter. The commitment in `SECURITY.md` is seven days; usually
   it is much sooner.
2. Reproduce it. If there is no proof of concept, write one. If it cannot be
   reproduced, say so to the reporter and ask for specifics rather than closing.
3. Decide whether it is a vulnerability at all, and whether it is ours. See the scope
   section of `SECURITY.md`.
4. Assess severity: what an attacker gets, and what they need to already control --
   hostile SQL, a hostile database file, or only hostile data in a table.
5. Open a **draft** GitHub Security Advisory to hold the work. Private vulnerability
   reporting is enabled, so a report that arrives that way already has one.

## Phase 2 -- Scope it

Answer these before writing any fix; they determine who fixes what, and where.

- **Which layer?** Hand-written Go, generated Go, or SQLite's own C. This decides
  the entire remediation path, and the layers are not equally ours to fix.
- **Which targets?** The module supports 20 `GOOS`/`GOARCH` combinations and the
  generated code differs per target. A fault may exist on some and not others.
- **Which versions?** When was it introduced, and is the current release affected.
  For generated code, `git show vX.Y.Z:vendor.json` names the `libsqlite3` and
  `libsqlite_vec` commits a release was vendored from.
- **Does the fix move `modernc.org/libc`?** If so it cannot ship from this
  repository alone; see [issue #177](https://gitlab.com/cznic/sqlite/-/issues/177).

## Phase 3 -- Fix it

The path depends on the layer, and only the first is quick:

| Layer | Path |
| --- | --- |
| Hand-written Go (driver, `vtab/`, `pcache/`, `vfs/` Go side) | Fix on GitLab, release. |
| Generated Go in `lib/`, `vec/` | The fix belongs in `modernc.org/libsqlite3` (and `modernc.org/libsqlite_vec` for `vec/`), which own the transpilation and the patch set. Re-transpile there, `make vendor` here, `make build_all_targets`, release. |
| Generated Go in `vfs/` | The C is `vfs/c/vfs.c`, in this repository. Fix the C and re-transpile it here; each generated file's first line records the `ccgo` command that produced it. |
| SQLite's own C | Report upstream at [sqlite.org](https://www.sqlite.org/support.html). Upstream owns the fix; we own shipping it. If the wait is unacceptable, a local patch can go into the `libsqlite3` patch set and be dropped when upstream lands theirs -- there is precedent in the v1.56.0 journal-rollback fix. |

**When the fix moves `modernc.org/libc`**, it is four releases in one incident, in
this order, and none can be skipped or reordered, because each pins the one before:

1. `modernc.org/libc`, released with the fix.
2. `modernc.org/libsqlite3`, its `go.mod` bumped to that libc, re-transpiled, tagged.
3. `modernc.org/libsqlite_vec`, bumped to both, re-transpiled, tagged.
4. Here: both sibling checkouts at those tags, `go.mod` on the new libc, `make vendor`,
   the builders green on all 21 targets, then the tag. `make vendor` refuses a dirty
   checkout, two checkouts on different libc versions, and a `libsqlite_vec` that
   requires another `libsqlite3` commit than `../libsqlite3` is at, and records what it
   used in `vendor.json`. The test suite fails when the libc in `go.mod` differs from
   the one in `vendor.json`, so the `go.mod` bump goes in the same push as
   `make vendor`, or every builder is red until it does.

`lib/` and `vec/` must come from the same libc. Do not re-vendor one while the other
stays on the old version.

Four rules that do not bend under time pressure:

- **Never merge a pull request on the GitHub mirror.** GitHub `master` would gain a
  commit GitLab does not have, and because the mirror keeps divergent refs and
  force-push is disabled, every later mirror push fails. Cross-merge into GitLab by
  hand, as `HACKING.md` describes.
- **Do not bump `modernc.org/libc` on its own.** The generated code is pinned to the
  exact version `go.mod` names.
- **Tag manually, and only when the builders are green.** `builder.json` sets
  `"autotag": "<none>"` deliberately. A fix that breaks three platforms is a second
  incident.
- **Run `make sbom` after any dependency change.** CI fails on stale documents, and
  an SBOM that misstates what shipped is worse than none during an incident.

## Phase 4 -- Disclose

As `SECURITY.md` states: a GitHub Security Advisory, then an entry in the
[Go vulnerability database](https://go.dev/s/vulndb-report-new) so `govulncheck`
reports it, then the release note. The middle step is the one that actually reaches
Go developers and is part of shipping the fix, not an afterthought.

Credit the reporter by name unless they prefer otherwise.

## Phase 5 -- When a released version is the problem

**A published Go module version cannot be recalled.** This is the fact to have
internalised before the day it matters. From the module mirror's own FAQ:

> Whenever possible, the mirror aims to cache content in order to avoid breaking
> builds for people that depend on your package, so this bad release may still be
> available in the mirror even if it is not available at the origin. The same
> situation applies if you delete your entire repository.

Deleting the tag does nothing. Deleting the repository does nothing. The checksum
database has it permanently.

What actually works:

1. Release a fixed version.
2. Add a `retract` directive to `go.mod` naming the bad version or range, with a
   comment saying why, and release that. Retraction is advisory -- the version stays
   downloadable -- but `go get` stops selecting it and `go list -m -versions` stops
   offering it.
3. Say so in `CHANGELOG.md`.

The `retract` block in `go.mod` already carries seven entries. It is load-bearing
history, not clutter: never remove entries while editing it.

## Phase 6 -- Account or token compromise

Assume the worst ordering: revoke first, investigate second.

1. **Revoke before diagnosing.** Sessions, then tokens, then keys. A token that
   might be compromised is compromised.
   **The mirror push credential is a case of its own.** It can rewrite what GitHub
   users see while GitLab looks fine, so nothing on the canonical side reveals its
   misuse. Revoke it with the rest, and compare the mirror's branches and tags
   against GitLab's rather than assuming they match.
2. **Check what was done with it**, not only what it could do: recent commits on
   both hosts, tags created, releases published, mirror settings, workflow changes,
   and whether any release artifact changed after it was tagged.
3. **Re-establish the account**: authentication factors, recovery codes, and confirm
   no additional factor or key was added by someone else.
4. **Assume any release made during the window is suspect** and treat it under
   Phase 5, which is the only reason Phase 5 exists in this document.

The specific credential inventory and revocation URLs live in the maintainers'
private notes, deliberately not here.

## Phase 7 -- Afterwards

Write down what happened and what was slow, while it is still annoying. If the
response revealed a gap, fix the document that should have prevented it --
`SECURITY.md`, `CONTRIBUTING.md`, `HACKING.md`, or this plan. A post-incident note
that changes no document has not finished.
