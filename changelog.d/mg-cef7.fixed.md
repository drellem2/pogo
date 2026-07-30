- **A release cut now emits the changelog compare links it was silently omitting,
  and refuses to tag a commit the refinery will re-commit (mg-cef7).** Two
  release-path defects that failed silently and recurred every cut.

  First, `bump-version.sh`'s `update_changelog()` was a single unanchored `sed`.
  It inserted the new version heading and **no `[X.Y.Z]:` compare link**, and left
  `[Unreleased]` pointing at the previous tag — still claiming the commits the
  release had just shipped. A missing link reference does not error: Markdown
  renders the version as **literal text** in the published changelog, so it
  degraded a user-facing artifact and read as a typo rather than a tooling fault.
  It was repaired by hand at v0.7.0 and again at v0.8.0; each repair was correct
  and left the next cut to rediscover it. Worse, `s///` without `g` replaces the
  first match on *every* matching line, so every cut also injected a **spurious
  version heading** into the body of any entry whose prose mentions the
  `[Unreleased]` heading — measured at **two headings added per cut** (9 → 11 →
  13 → 15 across v0.6.0/v0.7.0/v0.8.0). The injection split the entry's
  inline-code span across a blank line, which terminates the span, so the
  renderer promoted the injected lines to real sections and the published
  changelog showed 0.8.0/0.7.0/0.6.0 twice. That corruption is now repaired, and
  the logic moved to `scripts/roll-changelog.sh`, which matches exactly and
  first-occurrence-only, is fence-aware, emits both link references, and
  **refuses** rather than producing an entry it cannot link.

  Second, `bump-version.sh --tag` tagged the local pre-merge commit. Off `main`
  that commit does not survive — the refinery re-commits what it merges (v0.7.0's
  merged commit `4112875` carries committer *"pogo refinery"*) — so the tag
  dangled off a commit no branch contained. `--tag` is now **refused** off `main`
  rather than warned about, since a pushed release tag cannot be unpublished, and
  the refusal prints the correct sequence. `--push` additionally confirms the tag
  reached `origin`, because the release workflow triggers on the *pushed* tag and
  a local tag proves nothing about what was published. `CONTRIBUTING.md` and the
  PM sweep hook in `pm-template.md` both prescribed the broken one-liner and now
  prescribe tagging the merged sha instead — as a **separately-owned step**,
  because pogod stops a polecat within ~3s of merge success, so the worker whose
  merge closes the ticket is reaped before it can tag. Both v0.8.0 cut attempts
  hit exactly that: correctly instructed, merged, reaped, work item `done`, no
  tag.

- **`scripts/changelog-links.sh` checks version sections against link references
  by set, not by count (mg-cef7).** mg-cef7 originally proposed comparing the
  heading count to the link-reference count. Measured on live `main` that check
  fired — and was wrong about why: it reported `14 headings / 11 link references`
  as *three missing link references*, when in fact every version had a
  well-formed link reference and the difference was the three spurious headings
  above. The obvious remedy for its report — add the missing link references —
  would have **entrenched the corruption**, giving the injected headings link
  targets and making them look legitimate. A control that fires correctly and
  diagnoses wrongly is worse than one that stays silent, because it directs the
  repair at the wrong object. The shipped check therefore compares the sets and
  names the unmatched version and direction, never a count difference; treats a
  section with no link reference and a link reference with no section as distinct
  findings with distinct remedies; reports a version whose section appears more
  than once as a **duplicate**, explicitly telling the reader *not* to add link
  references for the extra copies; ignores occurrences inside fenced code blocks
  and flags indented ones as not-a-section; and catches `[Unreleased]` still
  comparing against a superseded tag. `bump-version.sh` runs it after rolling, so
  a cut that produces an unlinked heading now aborts.
