- **`auto_start = false` MEANS deliberately absent, and absent-watch had no way
  to say so — it escalated two on-demand agents to the mayor for 132 unbroken
  hours over a state both were configured into on purpose (mg-c86d).**

  The finding was **correct** and the alarm was still wrong. `doctor` and
  `representative` were absent 156h; both are `auto_start = false` by design
  (doctor is additionally reaped nightly on purpose), and the detector's own
  escalation preamble read *"a fleet that has not started it in 132h is not going
  to on its own"* — which is the **definition** of an on-demand agent, not a
  fault in it. There was no way for the finding to clear except by starting an
  agent that was not supposed to be running, so it would have repeated every 12
  hours forever.

  **An alarm that cannot clear trains its reader to ignore it** (mg-c232, where
  the same mechanism held a different detector escalated for 61 hours). That
  matters more here than anywhere else: absent-watch is the **only** instrument
  on this box that can see an agent which is *not there*. `pogo agent list`,
  stall-watch, ackwatch and deaf-watch all iterate pogod's registry, and an
  absent member cannot appear in a set it has left. A reader who learns to skip
  this detector leaves the fleet with no sight of absence at all.

  **`parked` was the shape of the answer, one layer over.** Park is an
  intentional absence that stays visible without drawing dispatch nudges; there
  was no equivalent vocabulary for an intentionally-absent *agent*. It did not
  need one — `auto_start = false` is already the declaration, already in the
  config, already what `pogo agent roster` reads. So the fix is not a suppression
  flag a coordinator adds each time; it is the detector reading the declaration
  that was there all along. The confirmed set is now partitioned:

  - **fault** (`auto_start = true`, or a prompt that could not be read) — an
    episode, exactly as before: renotified, escalated to `human` on age, cleared
    when the agent returns.
  - **declared** (`auto_start = false`) — reported **once** per unbroken absence
    and then carried as roster *context* in fault mail. It opens no episode, is
    never renotified, and **never ages into an escalation at any
    `escalate_after` setting**. It emits `absent_watch_declared`, not
    `absent_watch_fired`, so a reader counting alarms does not count it.

  The mail says which it is. A declared notice shares none of the fault subject's
  vocabulary — no *"NOT RUNNING"*, no *"nothing else reports it"* — because the
  subject line is the part that gets skimmed, filtered and forwarded. The
  on-demand class line changed from *"on-demand; nothing will bring it back"* to
  *"on-demand: DELIBERATELY ABSENT by declaration, and only an explicit start
  brings it back"* across all four surfaces that print it (`absent-watch`, `pogo agent roster`,
  `pogo agent list`'s footer via `internal/mailwarn`, and `pogo agent roster
  --help`), so a reader who meets the sentence in mail and then runs the CLI does
  not have two phrasings to reconcile.

  **Quieted, never hidden — and that constraint is one line away from being
  broken.** mg-f341 is the opposite failure: an `auto_start = false` agent
  *invisible* while absent, which is the hole absent-watch was built to close. So
  a declared absence is still announced once at `dormant_after`, still named in
  the `ALSO ABSENT, BY DECLARATION` section of every fault mail and every
  all-clear, still counted in the denominator those mails print, and still shown
  by `pogo agent roster`. The one-time ledger clears the moment the agent comes
  back, in the same loop and on the same condition as the hold-down clock — if
  the two could drift, a declared absence could end up muted across a return,
  which is mg-f341 wearing this fix's clothes.

  **The remedy is an artifact of the same kind as the defect, so three of its own
  edges are pinned as tests.** A fault mail's fingerprint ignores declared churn,
  or the un-clearable set would be back on the mailing clock by another route. An
  all-clear over remaining declared absences no longer prints *"0 absent"* — the
  same wrong sentence as the alarm it closes, reassuring instead of alarming.
  And editing `auto_start = false` onto an agent that is *already* absent — a
  plausible response to this very alarm — closes its episode without claiming it
  came back: the close reads *"episode closed by DECLARATION"* and lists the
  agent under `STILL ABSENT`. That last check reads the live snapshot rather than
  the confirmed set, because an agent reclassified twenty minutes into its
  absence is confirmed in neither and would otherwise be mailed about as
  *Restored* while sitting in the snapshot's own `Absent` slice.
