- **A work item whose branch has already merged stops being offered for
  dispatch — and stops merging into silence (mg-9d4e).** pogod closes a work
  item the instant its branch lands, but that close can be **refused, and the
  refusal is correct**: an item tagged `declares-remainder` names work that must
  outlive it, and `mg done` turns such an item away until a successor is named.
  So the merge lands, the close is turned away, the polecat is stopped ~0.5s
  later, its claim is released, and the item returns to `available/` — genuinely
  unclaimed, genuinely open, and completely done. It happened to `mg-0e8c` at
  23:42Z and `mg-ac0c` at 23:51Z on 2026-08-12, and priority-wake advertised each
  as "high priority, ready and unclaimed" within minutes of its branch merging.
  A worker dispatched at either would have re-derived a finding already on
  `main`. Both were caught only because a coordinator happened to remember
  watching each branch go through the refinery, which is not a control.

  **The guard that produces the state is not the bug and is not weakened here.**
  Its alternative failure is strictly worse and happened the same night: `mg-69f1`
  was untagged, closed cleanly, and silently dropped its remainder, leaving a
  compression drop with no open representation until it was noticed by hand. A
  completed item re-offered to the queue is loud and recoverable; a lost
  remainder is neither. The ordering stays; the side effect is what this closes.

  **A fifth dispatch refusal.** `pogo agent spawn-polecat` now refuses an item
  whose work has already landed on the target (409, above every side effect,
  like the four beside it). It is the complement of the stranded-work gate
  rather than a variant of it: that one covers a branch that is pushed and
  **unmerged**, and this covers the case that opens the moment that branch lands.
  The answer comes from the **refinery's own record** of the merge, keyed on the
  `--author` the branch was submitted under, not from git — a heuristic refusal
  gets overridden on reflex, and the two git-side attribution routes are ones
  `internal/strandedwork` already documents as incomplete. **PR-flow** merges are
  excluded: they land on an integration branch whose deliverable is a pull
  request that does not exist yet, so the item is legitimately unfinished. The
  refusal names the branch, target, sha and merge request so a reader can check
  it, and offers the item's own remedy (`mg done <id> --successor=<new id>`)
  before the escape hatch.

  It **fails open** — no id, no refinery, or a merge the bounded in-memory
  history has already pruned all dispatch — so a quiet answer from this gate is
  never proof that nothing merged. `--merged-override="<why>"` dispatches anyway,
  recorded as `dispatch_merged_work_overridden`; unlike `--stranded-override` or
  `--preserved-override` this has a use that is not a false positive, since an
  item can genuinely owe work after its merge.

  **And the state stops arising in silence.** Two different things fail the
  post-merge `mg done` — the worker won the race (benign; its verdict stands) and
  the item declined to close (the state above) — and they were one ambiguous log
  line, indistinguishable by exit code. pogod now asks the **store** which one it
  is, and for the second emits `work_item_merged_not_closed` and mails the
  coordinator with the branch, the sha, what `mg` actually said, and the
  two-command remedy. A **gated** item is excluded: pogod declined to close that
  one on purpose (`parked`/`human`/`blocked:`), and being gated is what already
  stops it being dispatched, so the hazard this alert warns about cannot arise.
  A status probe that FAILS still alerts: an unreadable store is not evidence
  the item closed, and it is the same `mg` that just refused the close.
  The event is on the spine before the mail is attempted, so a machine with no
  `mg` on `PATH` loses the improvement and not the record.

  **The detection has to live in the daemon.** The obvious fix — have the polecat
  file its successor, or mail the coordinator, before exiting — cannot work:
  pogod stops the polecat about half a second after the merge whether or not the
  close applied. The polecat is not slow to notice, it is gone.
