- **A declined prompt sync now reaches the agent whose prompt was declined, instead
  of only a log no agent reads (mg-c3f0).**
  `pogod` has always reported this condition correctly — it names the file, names
  the `.dist` sidecar, states the remedy, and links the doc (mg-f86c). It reported
  it at **every boot for seven days**, and nobody acted, because `pogod.log` is on
  no agent's schedule. The mayor ran 13-day-stale guidance the whole time and it was
  found by accident, by the architect reading a log while writing an unrelated design
  (mg-3ebe item 5). **The message was never the defect; the channel was.** An alarm
  with no reader is worse than silence, because the system genuinely reports the
  problem and therefore *audits as instrumented*.

  Full enumeration of every pogod log condition with an actor who never hears —
  15 of ~90, ranked, plus the ones that already have a channel and the ones that
  correctly have none — in
  [`docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md`](docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md).

  **Addressed to the agent that can act, resolved the way `ListPrompts` resolves it.**
  `mayor.md` → the **configured coordinator name**, not the literal `"mayor"`: the file
  is always `mayor.md` (mechanism) but the agent it starts as follows
  `[agents] coordinator` (policy), so a hardcoded name would misroute on any consumer
  who renamed it — and mail to a name no agent reads is silently accepted into a
  phantom mailbox and lost, which would recreate this defect with extra steps.
  `crew/<n>.md` → `<n>`. Everything else — `templates/polecat*.md`,
  `pm/pm-template.md`, nested or empty stems — falls back to the coordinator and is
  marked unowned. No branch synthesizes a name from a path it did not recognize.

  **Not `human`, and the reason is measured rather than asserted.** The `human`
  maildir holds **988 unread** — 23% of the fleet's entire 4279 — against **0** for
  `mayor`, **0** for `doctor`, and **10** for the worst of 269 crew-shaped mailboxes.
  Routing a fleet condition to `human` looks instrumented and is not. Surfaced en
  route and not previously noted: **839 polecat mailboxes hold 3016 unread**, so
  mailing an *affected agent* is only a read channel when that agent is standing, not
  ephemeral.

  **Rate-limited by condition, not by message, and it has to be on disk.** A declined
  sync is a steady state, not an event: `InstallPrompts` leaves the user-edited
  canonical untouched, so it keeps its **old** embed stamp, so the next boot compares
  the same stale stamp against the same shipped embed and declines again. The conflict
  set is re-derived identically at every boot — which is why the log line fired seven
  times — and in-process memory cannot suppress it, because **the process restart is
  the tick.** `~/.pogo/prompt-sync-notices.json` therefore records, per path, the
  **content hash of the `.dist`** plus the last delivery time. It mails on first sight,
  on a *changed* declined update (the divergence grew, so the recipient's merge job is
  bigger than the one they were told about), and then at most once per **72h** while
  unreconciled. A reconciled conflict is **forgotten**, so a recurrence is a fresh
  transition rather than inheriting a stale quiet window. 72h and not daily because
  reconciling a prompt is a judgement about which local edits are load-bearing, not a
  command to run — a nag arriving faster than the work can be scheduled trains the
  recipient to filter it, which is the original failure mode one level up.

  **No log tailer.** pogod knows it declined the sync at the moment it declined it, so
  this runs at the decision point from the `InstallResult` already in hand. It runs
  **before** the crew auto-start sweep, which is load-bearing rather than incidental:
  the notice is in the affected agent's maildir *before that agent starts*, so it hears
  on its very first mail-check of the boot that declined its prompt.

  **It can fail loudly — three ways.** A `prompt_sync_declined` event goes on the spine
  for **every** conflict on **every** boot, suppressed ones included, so a notifier that
  has quietly stopped reads as a run of `notified:false, reason:new` rather than looking
  identical to a fleet with no conflicts. A **failed send is never recorded as
  delivered**, so the next boot retries — there is no path where a clean-looking state
  file claims an announcement that never happened. And every state-store failure biases
  toward **noise, never silence**: a corrupt or unreadable file makes the notifier forget
  and re-announce, costing at worst a duplicate mail, because failing toward silence is
  the defect being fixed.

  The mail states the remedy as a **decision, not a command** — it shows `diff -u` and
  deliberately does not hand out `cp <f>.dist <f>`. The only reason the canonical file
  was preserved is that its local edits might be load-bearing; a paste-ready copy-over
  would hand out the single destructive action this mechanism exists to prevent, with
  the daemon's authority behind it. Pinned by test, as is the renamed-coordinator
  misroute this could have shipped with.

  **The logging is untouched.** mg-f86c already made the line correct and the fix is
  live; architect's first draft recommended improving it and withdrew that after reading
  the code. Reconciling the one currently-stale file is mg-4999 and stays separate —
  it clears today's instance and leaves the class defect alone.
