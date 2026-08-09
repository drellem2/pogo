- **The scheduler's mail fallback is coalesced: one mailbox copy per unbroken run
  of undelivered fires, not one per fire (mg-af83).**

  **First, the premise this ticket was filed on is false, and it is worth
  recording because three agents reasoned from it for a day.** The ticket — and
  its title — assert a double-write: a fire delivered as a nudge that *also*
  writes a mailbox copy. `internal/scheduler/deliverer.go` has returned before
  `sendMail` on the nudge-success path since mg-bcfa, the original scheduler
  commit. `mailAfterNudge(nil)` is false and the success path returns `nil`.
  Implementing the dispatched scope verbatim is a zero-line change.

  Measured rather than read: architect's PTY carried every fire from ~09:40Z on
  2026-08-09 onward, and received **zero** scheduler mails in that window — ~48
  consecutive nudge-delivered fires, no mailbox copies. Every one of its 265
  scheduler messages predates that timestamp.

  **What the mailboxes actually contain.** Classifying all 12,295 `From:
  scheduler` messages under `~/.macguffin/mail` by which fallback branch wrote
  them:

  | branch | messages |
  |---|---|
  | agent not running | 10,521 (86%) |
  | nudge failed (busy PTY, wait-for-idle) | 1,425 |
  | terminal wake suppressed | 349 |

  For architect, 265 of 295 are the not-running branch — and **264 of those are a
  single unbroken run** of `*/10` fires spanning 2026-08-07 13:20Z to 2026-08-09
  09:40Z. The defect is not a double-write on the healthy path. It is **one
  undeliverable schedule repeating for 44 hours**, which is the 33-hour fleet
  outage rendered as mail. On a healthy fleet the fallback's steady-state
  production is zero.

  **What changed.** While a schedule's fires cannot be delivered as a nudge, at
  most one mailbox copy is written per unbroken run of undelivered fires. A fire
  that reaches the agent's PTY closes the run; the next one that cannot opens a
  new one. Every copy that is not written emits
  `scheduler_fallback_coalesced` with the run length and the age of the copy it
  rode on, and the copy that *is* written says in its body that it stands for the
  ones behind it — a suppression whose reason is not observable is
  indistinguishable from the delivery bug it prevents.

  Simulated against the real corpus (runs reconstructed from message timestamps,
  a new run at a gap greater than 1.5× the median for that schedule):

      TOTAL      12,295 -> 1,363   (88.9% fewer)
      architect     295 ->    24
      mayor         403 ->   167
      pm-pogo       426 ->   138

  **Two details that look incidental and are not:**

  - **The run opens only after the send SUCCEEDS.** A failed send left nothing in
    the mailbox, so treating it as an open run would suppress every later copy
    against a message that does not exist — trading a noisy mailbox for a
    silently undeliverable schedule, which is the strictly worse fault. This is
    also the answer to the test architect derived today (*what would this
    instrument report if the thing it names stopped entirely?*): the predicate
    names "a copy is already unread in this box", and if mail stopped entirely
    the run never opens and every fire retries. It fails loud, not green.
  - **`fallbackRefreshInterval` (24h) does two jobs.** It bounds how stale the
    single copy may get — the 44-hour outage yields two copies, the newest never
    more than a day old — *and* it bounds the run map, whose keys would otherwise
    accumulate one entry per polecat schedule for the life of the process.
    Removing either half loses the other.

  **What this deliberately does NOT do.** It does not gate the write on
  `unacked_streak`. That counter cannot presently separate an agent that did not
  do the work from one that did it and did not ack (measured fleet ack rates
  18–22%), nor a dead agent from a live one batching its fires — mg-af83 records
  both confounds. The predicate here is run length, a property of the deliverer's
  own delivery attempts: it needs no threshold, no work-coupled ack, and no
  liveness term, so it is orthogonal to that work rather than blocked on it.
  Delivery accounting is untouched — a coalesced fire still returns `nil`, so
  `fires_delivered` and `unacked_streak` read exactly as before.

  **Scope preserved.** `PogodDeliverer` only ever sends the scheduler's own fire
  copies, keyed per `(agent, schedule id)`. Agent-to-agent and human mail take a
  different path and are untouched, so a polecat's `{{.Id}}` work-item box keeps
  its cross-instance handover function. Tests pin that two schedules to one agent
  each keep their own copy, and that an explicit `delivery=mail` schedule is
  never coalesced — mail is the requested channel there, not a fallback.

  **Still open, and orthogonal to this change:** coupling the ack to the work,
  measuring the streak distribution afterwards, and the two separate alarms
  (`streak + process absent` = dead; `streak + process present` = alive but not
  doing scheduled work). None of it is built here and none of it is obsoleted by
  this. mg-5168, the read-side sender predicate on `mg mail list`, merged
  separately at 17:46Z.
