- **A nudge is now CONFIRMED by the agent, not assumed by pogod — and a busy
  agent is finally reachable (mg-ebee).** `pogo nudge` wrote to the PTY master
  and returned success. A write to a pty master succeeds whether or not anything
  is listening, so "success" meant "the bytes left pogod" and nothing more.

  Two failures, one cause:

  ```
  # an idle agent that never reads its stdin
  NudgeWithMode("do the thing", NudgeWaitIdle, 5s)  ->  nil        # nothing received it

  # pm-pogo's 09:00 sweep, 2026-07-29
  wait for idle: pm-pogo still producing output after 30s          # nothing was even written
  ```

  The second is the sharper one, and it generalises past the startup race Orc
  measured: **the nudge precondition is `idle`, and a working agent is by
  definition producing output — so the precondition is the negation of the state
  you need to reach.** The busier and more useful the agent, the less reachable
  it is. The 09:00 sweep only ran because the mail fallback caught it.

  **The signal.** Claude Code fires a `UserPromptSubmit` hook once per prompt
  the harness actually *submits*. pogod registers `pogo hook prompt-submit` as
  that hook at spawn, in the agent's own `.claude/settings.local.json`
  (**merged**, never clobbered — an agent's working directory can be a real
  repository holding a human's permissions and hooks), and passes the receipt
  file's location as `POGO_SUBMIT_RECEIPT`. Each firing appends one line. pogod
  now reads a number instead of inferring from quiescence, and nothing but the
  harness can make that number move.

  **The escalation**, and the order is the design:

  1. **The message.** A receipt arrives, done — the common case, costing nothing.
  2. **A bare return.** The measured failure is usually not a lost message but a
     lost *submit*, leaving the text sitting unsent in the composer. A return
     carries no content, so it submits whatever is loaded and **cannot
     duplicate** anything. That is why it goes first: the other order would
     double-deliver every merely-unsent message, and an agent acting twice on
     one instruction is a worse outcome than one that missed it.
  3. **The message again** — only now, when a bare return has proved there was
     nothing loaded to submit.
  4. **Refuse.** `ErrNudgeUnconfirmed`, naming what was tried, instead of a
     success nobody can check.

  A **mid-turn** agent stops after step 1 with `ErrNudgeQueued`, and the live
  measurement changed why. Against a real Claude mid-output, the message **is**
  taken and acted on — the probe asked for "PONG" and got it — but **no
  `UserPromptSubmit` fires for a prompt taken while a turn is in flight**. One
  run watched the receipt for four minutes past the end of the turn; it never
  moved, while the answer had been on screen throughout. So the receipt is
  *blind* to mid-turn delivery, not evidence against it, and a resend would
  deliver the instruction twice — measured, in the same session, by resending
  and watching it arrive a second time. Orc's §5.2 gives the same rule a
  different reason ("Claude queues the prompt legitimately"); on this build the
  prompt is neither queued nor lost but taken immediately, which makes the rule
  load-bearing rather than precautionary.

  `MidTurn` is deliberately not `!IsIdle()` — `IsIdle` answers false for an
  agent that has never written anything, which is a just-spawned harness, and
  reading that as "mid-turn" would switch the escalation off in exactly the case
  the startup drop lives in. Silence is not a turn.

  **What replaces wait-idle's guarantee.** The wait-idle path is unchanged and
  still reachable (`pogo nudge --wait-idle`, API `mode: "wait-idle"`). What it
  was standing in for — *don't type into a harness that will not process it* —
  is now carried by the receipt, which is a direct observation of processing
  rather than a proxy for it. Confirmed delivery therefore does not wait for
  idle at all, which is what makes a working agent reachable.

  **Nothing is escalated against an agent that cannot report.** A provider with
  no receipt hook, an unresolvable `pogo` binary, a `settings.local.json` that
  will not parse (pogo declines rather than overwriting a human's file), or an
  agent spawned before any of this existed — each leaves the agent with no
  receipt signal, and confirmed delivery degrades to precisely the pre-existing
  wait-idle behaviour. A hook that never fires and a message that was dropped
  look identical on disk, and escalating against the first would resend a
  message the agent received perfectly well.

  **Where it is used.** Confirmed delivery is the new default for `pogo nudge`,
  for the nudge API when no mode is given, for the scheduler's fires (the path
  that failed pm-pogo), and for the post-spawn initial nudge — a polecat's
  entire existence hangs on that one landing, and a dropped one is a polecat
  that claims nothing and sits until somebody notices. `stallwatch`'s nudger and
  the mail-check reachability escalator stay on wait-idle on purpose: both are
  documented as relying on "the message was NOT written, so mail is the only
  delivery, not a second one" (mg-79dc), and that reasoning is unaffected.

  The scheduler withholds its mail fallback for `ErrNudgeQueued` alone — the one
  outcome where something *did* receive the message — and keeps it for every
  other failure. That is not a silent success: the nudge path emits a new
  `nudge_unconfirmed` event carrying the outcome (`queued` or `refused`) and the
  fire's correlation token, so an ambiguous delivery sits in the event log next
  to the confirmed ones rather than being indistinguishable from them. The
  2026-07-22 log recording 647 successful deliveries into a fleet where nothing
  consumed them is the shape of failure this is meant to close.

  **The busy case is improved, not closed, and the difference is written down.**
  Before: nothing was written at all and the caller got an error. Now: the
  message is written, Claude takes it, and pogod says plainly that it cannot
  verify that. What is gone is the third possibility — reporting success for a
  message nobody can check. Verifying mid-turn delivery needs a second signal
  (the session transcript, which `Provider.SessionTranscriptGlob` already
  locates); that is a follow-up, and it is named as one rather than implied.

  **Demonstrated red first.** `internal/agent/nudgedelivery_test.go` proves both
  failures on the unfixed tree before proving the fix — including a control that
  rules out a deaf fake by landing the same message with `--immediate` and
  watching it arrive, so the empty witness cannot be explained away. Then
  verified live against a real disposable Claude mid-output, not only against
  fakes: `internal/providers/nudgedelivery_live_test.go` (opt-in,
  `POGO_LIVE_CLAUDE=1`), with every number and the mid-turn finding above
  recorded in `docs/investigations/confirmed-nudge-delivery-2026-07-29.md` so a
  skip in CI does not quietly become "never observed".

  New: `pogo hook prompt-submit` (hidden; harness-invoked), `pogo nudge
  --wait-idle`, `Provider.SubmitReceiptHook`, `agent.NudgeConfirm`,
  `agent.ErrNudgeQueued`, `agent.ErrNudgeUnconfirmed`, event
  `nudge_unconfirmed`, receipts under `$POGO_HOME/agents/receipts/`. Provenance:
  candidate (a) of the Orc comparative study (`docs/orc-comparative-study-2026-07-28.md`
  §5.2), ranked first of eight by pm-pogo.
