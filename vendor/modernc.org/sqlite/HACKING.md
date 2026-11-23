## Tagging and releases

Please do not tag commits unless all builders at
https://modern-c.appspot.com/-/builder/?importpath=modernc.org%2fsqlite are
happy.

Unlike most modernc.org repositories, this one is deliberately NOT
auto-tagged: builder.json sets "autotag": "<none>". Many projects depend on
modernc.org/sqlite, so letting the build bots tag it automatically is too
risky. Releases here are tagged manually by the maintainer, once all the
builders above are green.

## CHANGELOG

Because releases are tagged manually (see above), the top of CHANGELOG.md can
run ahead of the actual git tags: a version section may be written and dated
when the work lands on master while the tag itself is pushed later. Before
assuming a version is released, check the real latest tag with

    git ls-remote --tags origin

Keep all not-yet-tagged work in a SINGLE pending section at the top of
CHANGELOG.md. Do not open a new "vX.Y.Z" header per merge request - that splits
one pending release across several version numbers. Add each merge request's
entry to the existing pending section, grouped with related work, and let the
maintainer set the final version number and date when the release is actually
tagged. The next version is the one after the latest tag reported by the
command above, not after whatever header currently sits at the top of the file.

### Entry style

CHANGELOG.md is read by people deciding whether they need to upgrade, not by
people auditing the implementation. Keep it that way.

  - One bullet per user-visible change. One to three sentences for an ordinary
    change, five at the very outside for a new public API or for something
    people must act on; roughly sixty words is the right size to aim at. A
    release is a list someone can scan, not prose.

  - A bullet answers three questions and stops: what changed, does it affect
    me, where do I read more. Mechanism, benchmark figures, review history and
    internal details belong in the linked issue or merge request, or in
    doc.go - not inlined here. Detail worth keeping is worth keeping where it
    can be maintained.

  - Never restate what a linked document already says. An entry announcing a
    new doc.go section must not also paraphrase that section.

  - Say plainly when a change is opt-in, off by default, documentation-only or
    breaking. That is the sentence people are scanning for. Put it in bold
    when it changes the behavior of an existing DSN or API call.

  - Keep "See [GitLab merge request #N](...), thanks X!" attached to the change
    it credits. Stranded on a bullet of its own it carries no context.

Length is not fidelity. Between v1.51.0 and v1.59.0 entries grew from about
twenty words per bullet to over a hundred and seventy, and nearly all of the
detail that accumulated was already in the issues, the merge requests and the
doc.go sections those same entries linked to. Those six releases were rewritten
against the rules above; see GitLab issue #258.

## Integrating merge requests

The canonical repository is GitLab cznic/sqlite; the GitHub mirror does not
auto-sync merge requests, and the "merge request !NNN" references in
CHANGELOG.md are GitLab MR numbers.

Land a contributor MR by fetching its head ref and merging it with a merge
commit:

    git fetch origin refs/merge-requests/<iid>/head
    git merge --no-ff FETCH_HEAD

GitLab publishes every MR's commits at refs/merge-requests/<iid>/head
regardless of which fork the source branch lives on. Merging --no-ff keeps the
contributor's original commit SHAs and author metadata, and because the MR's
head commit becomes reachable from master, GitLab automatically flips the MR to
"Merged" once master is pushed.

Do NOT land an MR by downloading its .patch and running git am: that rewrites
the committer and the commit date, producing a new SHA that GitLab cannot match
against the MR's source branch. The MR then has to be closed by hand and shows
as "Closed" rather than "Merged", which reads as "rejected" to anyone scanning
the list.

Match the merge-commit message GitLab itself uses:

    Merge branch '<source-branch>' into 'master'

    <substantive subject> (#<issue>)

    [Closes #<issue>]
    See merge request cznic/sqlite!<NNN>

Include the "Closes #<issue>" line only when the MR actually closes an issue;
omit it for follow-ups. <source-branch> is the contributor's branch name on
their fork.
