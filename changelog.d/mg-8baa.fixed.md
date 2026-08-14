- **`pogo check-stranded` states how many items it CHECKED, and every item it
  could not check is a row (mg-8baa).** The sweep's population is work items,
  but a repository whose branch listing failed was recorded per-*repository*
  and nowhere else. The items behind it left the join without appearing in any
  column — not as a finding, not as an exclusion, not in the exit code — while
  the header went on counting them. A mayor found the gap only by having swept
  item statuses by hand and holding a fourth item the tool had not named.

  The run that produced this ticket, verbatim:

      112 open work item(s) scanned across 9 repo(s)
        ...
        onethird_program — COULD NOT LIST BRANCHES: ... No such file or directory
        riemann — COULD NOT LIST BRANCHES: ... No such file or directory
      ...
      No open work item has work already sitting on a branch.

  exit 0. Three of those 112 were never looked at. Note that the two failure
  lines *did* print — this was never total silence, and that is what made it
  worse: they name no item id, so a reader who saw them could not go and check
  anything, and they sit above a flat all-clear and a clean exit status.

  Now:

      112 of 113 open work item(s) CHECKED across 8 repo(s) — 1 NOT CHECKED
      ...
      1 FINDING(S) — 0 stranded, 0 landed-but-not-closed, 0 conflict suspect,
                     0 UNJUDGED, 1 REPO UNREADABLE:

        repo_unreadable   available  mg-d318
          repo "/Users/daniel/.claude" COULD NOT BE LISTED: ... not a git repository
          NO BRANCH WAS LOOKED FOR on this item. It is not stranded, not landed
          and not clean — it is unchecked.

  exit 1.

  **The predicate is the failure, not the shape of the string.** The ticket's
  hypothesis was the bare relative name — `repo: onethird_program` rather than
  `/Users/daniel/research/onethird_program` — and flagged itself as a
  correlation rather than a proven cause. It is a real instance and it is a
  subset. The third missed item on that run was `/Users/daniel/.claude`:
  absolute, present on disk, and not a git repository. Keying the row on
  `filepath.IsAbs` would have left that one silent — and by the time this
  landed the two relative-path items had been normalised by hand, so the
  absolute one was the *only* live instance remaining. A test pins the
  distinction.

  **An item naming no repo at all is the same row.** That case was already
  counted and printed in prose. What it was not was a row, so `Actionable()`
  stayed false and the command exited 0: a report that states a gap and then
  exits clean is read by every schedule as clean.

  **The header change is the half the mayor asked for by name** — "the skip is
  the defect; the header claiming full coverage is what made it undetectable".
  `items_scanned` is the population enumerated, not the coverage, so it is no
  longer rendered alone; the header prints checked-of-population with any
  shortfall on the same line. This is the rule its sibling `pogo check-intake`
  already shipped ("a failed issue list and a repo with no open issues are
  indistinguishable to a careless check, so an unreadable repo is reported
  rather than counted as covered"), and the two commands disagreeing about it
  was itself worth removing.

  Also stated rather than silently narrowed: items dropped by a `--repo`
  restriction. Not a failure — the caller asked for it — but it is still the
  difference between the population and what was looked at, and the header
  overstated its reach the same way.

  **Ways this fix could re-create the defect, checked rather than assumed.** The
  leak was never "one branch forgot a row"; it was that no test asserted no such
  branch existed. Adding rows on the two leaking paths repairs those paths and
  does not establish the property, so a test now walks a board carrying every
  shape at once and requires the population to partition into checked +
  out-of-scope + reported-as-unchecked with no remainder. A future path that
  drops an item fails there whether or not its author reads this file. The four
  new guards were also each run against the pre-fix code and confirmed to fail.

  Two adjacent defects found while confirming this one and deliberately left
  out, filed separately rather than folded in: the `stranded` remedy prints
  `pogo refinery submit` without consulting refinery history, so for a branch
  the refinery already classed DEFECT at the rebase stage it confidently
  recommends the one command that provably cannot work; and the spawn-time
  stranded guard has no cell for a *rescue* dispatch, treating every dispatch at
  a stranded item as re-derivation.
