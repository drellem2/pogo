- **The stranded-work detector gets an ADDRESSEE, and a second one covers the
  polecats it can never reach (mg-be37).** `work_item_stranded_push` now mails the
  coordinator; a startup sweep reports polecats that outlived a pogod restart; and
  pogod closes a merged item whatever submitted the branch.

  **THE DEFECT WAS DELIVERY, NOT DETECTION, and this ticket was filed on the
  opposite premise.** `reportStrandedWorkOnRelease` shipped 2026-08-05 in e354eba
  and works. On the night of 2026-08-09 five polecats were released leaving pushed,
  unmerged work behind and it fired on **all five** — five events in the entire
  log, five stranded branches, 1:1, no false positives — each payload already
  naming the branch, the commit, the unmerged count, the exact `pogo refinery
  submit` line and an explicit "do NOT dispatch a worker at this item". Nobody saw
  any of them. Its only two outputs were pogod's log and `~/.pogo/events.log`, it
  runs inside pogod after the agent process is gone, and
  `work_item_stranded_push` appeared in exactly one non-test place in the tree:
  the emit site. **The measured gap between detection and a human noticing was ~1h,
  2.5h and ~3h.** In each of those windows the board read `available`,
  priority-wake advertised the item as unclaimed, and the action it recommended —
  dispatch — is the one that re-derives work that is already pushed. mg-9a19 lost
  1026 lines exactly that way. So the fix is an addressee, not a second detector:
  the emit site already knows the work item, the repo, the branch and the remedy.

  **THE SUBJECT LINE CARRIES THE PROHIBITION**, because that is the part that gets
  skimmed. Recipients are the coordinator, unconditionally and first, plus the
  repo's PM when a mailbox of that name **actually exists** — `pm-<repo>` is a
  guess, and mg-f04b removed a literal `pm-pogo` from this tree because such names
  belong to one machine's fleet, so the guess is probed rather than assumed. No
  probe can gate the coordinator: an alert whose addressee list can resolve to
  empty is this ticket's own defect rebuilt one layer down. For the same reason a
  **failed send emits `work_item_stranded_push_undelivered`** — without it, "the
  alert was never generated" and "the alert bounced" are the same silence. That
  half is copied from the worktree-preservation notifier, the one path in this
  daemon validated end to end (22 delivered notices over three days).

  **THE ONE POPULATION THE GATE CAN NEVER REACH is a polecat that outlives a pogod
  restart**, and it is permanent for that polecat rather than one missed report.
  Hard exits are covered — `kill -9`, OOM and crash all route through the reaper
  door, and the `restart_on_crash` no-release exclusion does not apply to polecats
  — so the ticket's original "test kill -9 first" instruction was discharged
  without finding a hole. The hole is structural: `reportStrandedWorkOnRelease` has
  one caller, `releasePolecatClaim`, which needs an `*Agent` **out of the
  registry**; the registry is in-memory with no adopt path, so a successor pogod
  has no entry for a surviving polecat and never will. Neither door is reachable
  for it again — a later graceful stop included. It is un-instrumented for the rest
  of its life, **and this fleet restarts pogod nightly.**

  So `ReportStrandedWorkAcrossRestart` runs **once per boot, on every startup
  path**, and the trigger is the restart because the restart is what *creates* the
  uncovered set. A clock would only be guessing at when that happened. The
  population is the **witness store minus the registry**, not "branches minus the
  registry": at boot the registry is empty by construction, so the literal reading
  selects all 634 polecat branches in this repo — the forbidden all-branch sweep
  wearing a different trigger. `noteWitnessExit` deletes a record when the daemon
  sees a polecat exit, so a record that survives into a new boot **is** a polecat
  whose exit was never witnessed. Report-only, and a live survivor's mail says so:
  its work is real but its branch may still grow.

  **AN ITEM THAT MERGES IS NOW CLOSED WHATEVER SUBMITTED IT.** pogod closed an
  author's work item at merge only when a polecat had CLAIMED it, so a coordinator
  submitting a stranded branch by hand left the item in `available/` with its work
  on main — four times that night (mg-51f4, mg-00b3, mg-6c90, mg-56ac).
  priority-wake then told the mayor to "claim or dispatch now: mg-6c90" **four
  minutes after that branch merged as b9e1d1b with 1116 insertions already on
  main**. While a branch is unmerged the spawn-time guard refuses the dispatch; the
  moment it merges the guard correctly stops refusing and the item is still open,
  so that window opens at merge and never closes. `reapMergedPolecat` now completes
  the item whenever the MR's author is SHAPED like a work-item id, with no registry
  lookup — a hand-submitted branch has no polecat by construction, so requiring one
  was requiring the condition the case cannot satisfy. Crew authors (`mayor`,
  `pm-pogo`) are excluded by that same shape test, and the post-merge-work probe now
  runs on authorless merges too, or the new close would have bypassed mg-d86e's
  declaration check.

  **A FAILED READ IS ITS OWN OUTCOME, in the sweep and in the report.** The natural
  predicate — `git cherry <target> <branch> | grep -q '^+'` — answers CLEAN
  whenever git FAILS, because a failed git prints nothing and "no output" is how
  that predicate spells clean (measured against an unresolvable ref on mg-b6d1).
  Clean is the *permissive* answer here, so that silently converts a stranded
  branch into an all-clear: this ticket's own defect rebuilt inside its own remedy,
  and the fleet has measured ~40-minute connectivity waves. Anything unreadable is
  counted `unjudged`, never folded into either verdict — one direction hides
  strandings, the other cries wolf on every blip. The same applies one level up: an
  unreadable witness store yields an error, never a report of zero. **Both
  polarities are proven able to fail**, not merely to pass: breaking UNJUDGED into
  CLEAN fails the sweep test, and disabling the mail call fails the addressee test.

  **THE PATCH-ID BLIND SPOT IS REAL BUT NOT WHERE IT WAS PREDICTED.**
  `git rev-list --count main..<branch>` reports every healthy merged branch as
  stranded forever; `git cherry` compares patch ids and gets that right. The ticket
  predicted a residual blind spot for "a branch the refinery rebased through a
  CONFLICT" and flagged it unvalidated. **Both halves of that turned out wrong, and
  the truth is worse:** the refinery ABORTS on a rebase conflict (mg-eac0) and
  never merges through one — but it rebases into its own copy without force-pushing
  the branch, so origin keeps the commit as written and the target gets it as
  replayed. **A patch id covers the diff's context lines, so no conflict is
  required — only a neighbouring change.** `polecat-79dc` is the exact control:

  ```
  77e012c (origin/polecat-79dc)   patch-id 959d2fa2…
  1e1292f (main)                  patch-id 5a479b4d…
  identical --stat; every added and removed line byte-identical
  ```

  So `internal/strandedwork/content.go` offers a **content-level second opinion**:
  what fraction of the substantive lines the unmerged commits ADD does the target
  already hold? At ≥95% (and ≥20 countable lines) a row becomes
  `conflict_suspect`, which recommends **neither** remedy — the two instruments
  disagree, and closing an unmerged branch throws the work away. The threshold is
  deliberately conservative: branches measured at 0.88, 0.91 and 0.94 are also on
  main and are also not demoted. Under-demoting costs a line of report;
  over-demoting costs a branch. The conflict case is a constructed fixture in the
  gate, asserting the blind spot in one test and the fallback's catch of it in
  another.

  **`pogo check-stranded` is the operator-invoked reader for the residue**, and it
  is **not on a clock** — nothing in this change schedules it. It walks the ~115
  OPEN work items and asks each whether it has a branch, which is what makes it
  readable: a branch-first sweep of this repo's origin finds **57 of 634** with
  unmerged patches, 48 on archived items and 2 on no item at all, while the
  item-driven walk produced **three rows**, one a live instance nothing else had
  found (`mg-65d2`, merged as 0640bc7, item still `available`). Rank is on item
  status, `available` first, because that is the status priority-wake advertises.
  Two exclusions, both counted and nameable with `--all`: a running polecat's
  branch (unmerged commits on a claimed item is what work in progress *is*) and a
  branch already in the refinery queue. It REPORTS; it never submits and never
  closes. Documented in [docs/operations.md](../docs/operations.md).

  **WHAT IS STILL UNCOVERED, stated rather than dropped:** every guard here —
  the spawn refusal, `git cherry`, the release gate, this sweep — is defined over
  **pushed** commits. Uncommitted work in a preserved worktree is invisible to all
  of them by construction, and the only artifact that knows is the
  preserved-worktree notice, which is filed under hygiene and says nothing about
  work-item safety. That gap ate this ticket's own predecessor.
