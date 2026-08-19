- **The `MERGE FAILED` notice is addressed to the AGENT that owns the branch, not
  only to the work-item id (mg-1fcc).** The refinery mailed `mr.Author` — for a
  polecat, its work-item id `mg-32e3`. The running agent's mailbox is `c32e3`.
  Those are different Maildirs, so on 2026-08-10 two notices sat unread in
  `mg-32e3` and `mg-db58` while `c32e3` and `cdb58` — the only actors that can
  resubmit — had empty inboxes. The recipient list is now resolved from the
  BRANCH (`polecat-c32e3` names `c32e3`), with the author lookup as the fallback
  that keeps crew- and human-authored merge requests working, and the work-item
  mailbox is kept on the list as the audit trail it may be.

  **THIS IS A REDUNDANCY REPAIR, NOT A WORK-STRANDING BUG, and the evidence says
  so.** The primary channel is the polecat's own polling loop, and it works: four
  of four polecats that hit this recovered without ever reading the mail — c6d7b
  unprompted in 12m43s, c3a96 unprompted in 3m (uncontaminated), c32e3 and cdb58
  in the same ~18-minute window, with c32e3 self-reporting that it learned from
  its `pogo refinery show` loop rather than from mail. The 12-versus-18-minute
  spread is the network outage those two failures landed in, not the channel. No
  work was at risk, and nothing here rescues the common case.

  **WHAT IT CLOSES IS THE CASE POLLING CANNOT COVER**, in c32e3's words: *an
  author who polls finds out at failure time; an author who has finished polling
  (or was stopped) finds out never.* That is the mg-be37 population — branch
  pushed, unmerged, nobody watching — and it had no channel at all.

  **THE NOTICE ALSO STOPS BEING REACHABLE ONLY BY CONVENTION.** The polecat
  template already tells every polecat to check both its own box and its
  work-item box, so the notice was reachable rather than unreachable — the
  original filing overstated this and the correction is carried here. But that
  made a convention load-bearing for correctness: every polecat has to remember a
  second mailbox named after its ticket, and a rescuer reading a stopped
  polecat's inbox has no such instruction. One registry lookup removes the
  dependency.

  **A NOTICE THAT REACHES NO READER IS NO LONGER A SUCCESSFUL-LOOKING DELIVERY.**
  When no live agent resolves for a branch, pogod emits
  `refinery_failure_notice_unaddressed` (`pogo events list --type
  refinery_failure_notice_unaddressed`) carrying the branch, its owner, why
  nothing resolved, and the per-recipient delivery outcome — so a refused send
  (`no_such_mailbox`, exit 3 since mg-d639) is queryable instead of being a line
  in `pogod.log`. It is an event and not a mail on purpose: the thing that just
  failed is addressing, and answering that with another address is a retry
  dressed as an alarm. The coordinator still receives its own copy of every
  failure notice regardless.

  **THE REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so the ways it
  could repeat it are checked.** (1) *Mailing a box with no reader* is the defect
  itself, and the fix still does it — a stopped polecat's Maildir outlives its
  process and is where a rescuer or successor looks — but that case is exactly
  what now emits an event, so it is no longer silent. (2) *Deriving the wrong
  name*: the branch is asked first because it is the fact about whose work this
  is, and a foreign branch checked out by another agent (the hazard gitgc's
  `PolecatNameForWorktree` documents) does not misroute here, because both
  candidates are addressed rather than one being chosen. (3) *An event nobody
  reads* is the same failure one layer down; it is documented in
  [docs/event-log.md](../docs/event-log.md) with the query that answers it, which
  is the same standing `worktree_notice_undelivered` has — no watcher alerts on
  either, and that is stated rather than implied.
