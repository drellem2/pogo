- **`docs/pogo-home-version-control.md` called `drellem2/pogo-config` a *public*
  repo; it is private — corrected the one property that governs what may be
  committed there (mg-ee70).** The doc is **status: decided** (mg-3610,
  2026-08-06) and is the artifact agents are pointed at to reason about what
  belongs in `~/.pogo`, so line 12 was the load-bearing sentence in it.

  Measured 2026-08-13: `gh repo view drellem2/pogo-config --json
  isPrivate,visibility` → `{"isPrivate":true,"visibility":"PRIVATE"}`, twice and
  independently (pa, then doctor). The same run confirms the two facts sitting
  in the corrected sentence: `git ls-files` in `~/.pogo` returns **16** paths,
  and `agents/crew/pa.md` is one of them.

  **It was wrong in the direction that costs more.** The tracked set includes
  personal data — `pa.md` carries Daniel's email address and thirteen references
  to his calendar setup — and an agent trusting line 12 had been told those
  details were *already public*, which is exactly the belief that makes someone
  relax about what else goes in. It errs the other way too: an agent could
  refuse a legitimate commit on a privacy ground the repo does not impose.
  Nothing was published as a result; the repo really is private, so this was a
  live wrong belief and not a live leak.

  **The reasoning built on the public reading is repaired, not just the word.**
  The prohibition bullet said anything added to `~/.pogo` "may be published to a
  public repo"; it now says such an addition may be committed and pushed to
  GitHub — off-host rather than world-readable, still off-host, and one `gh repo
  edit --visibility public` away from becoming the thing the old sentence
  described. The mechanism that bullet names is unchanged and was never
  visibility-dependent: the allowlist `.gitignore` stops an addition by default,
  new top-level paths need an explicit un-ignore, and `git ls-files` before any
  commit is the check. The rest of the doc's argument is about *which* files
  belong there rather than about visibility, and stands unchanged. The 0.9.0
  changelog entry for mg-3610 carried the same wrong word and is corrected in
  place.

  **The claim is now dated and re-measurable**, because that is the failure being
  fixed: the doc states the command and its output, and says outright that
  `isPrivate` is a present-tense reading. It does **not** claim the repo was
  always private — GitHub stops surfacing a repo's public events once it goes
  private, so the absence of such events is not evidence about the window
  between creation (2026-07-07) and now, and asserting otherwise would repeat
  the original defect in a new place.

  **Provenance worth keeping.** The finding came out of an audit of stranded
  memory notes (mg-b765, mg-a9b3), where the stranded note was right and the
  decided doc was wrong. The rule everyone had been applying — the shipped
  artifact beats the stale note — is a good rule, and this is its counterexample.
