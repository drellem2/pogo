# Confirmed nudge delivery: what was measured, against a real Claude

**Date:** 2026-07-29. **Work item:** mg-ebee. **Machine:** Daniel's (darwin 24.6.0).
**Harness:** `claude` at `/Users/daniel/.local/bin/claude`.
**Provenance:** candidate (a) of `docs/orc-comparative-study-2026-07-28.md` §5.2,
ranked first of eight by pm-pogo.

Everything below was run, not reasoned. The reproduction is
`internal/providers/nudgedelivery_live_test.go` (`POGO_LIVE_CLAUDE=1`), which
skips wherever the preconditions are absent — hence this file, so a skip in CI
does not quietly become "never observed". The fakes in
`internal/agent/nudgedelivery_test.go` carry the same assertions into CI, but a
fake cannot falsify a claim about a binary it is standing in for, which is the
whole reason this page exists.

Every agent used here was spawned by the test, in a temp directory, and stopped
at the end. No crew agent's pty was touched.

---

## 1. The defect, on this codebase, before the fix

`internal/agent/nudgedelivery_test.go` at commit `931d569` — **before** any fix
— demonstrates both halves red:

```
--- PASS: TestWaitIdleNudgeReportsSuccessForUndeliveredMessage
    DEFECT CONFIRMED: NudgeWithMode(wait-idle) returned nil for a message
    the agent never received (witness file empty)

--- PASS: TestWaitIdleNudgeRefusesToReachABusyAgent
    DEFECT CONFIRMED: a busy agent whose input loop is provably listening is
    unreachable under wait-idle; nothing was written to its PTY.
    err = wait for idle: agent "busy-waitidle" still producing output after 2s
          (last PTY write 43ms ago) — agent is busy or stuck redrawing:
          context deadline exceeded
```

The second error is the same sentence, modulo the name, that killed pm-pogo's
09:00 sweep that morning. The busy control ends by delivering the identical
message with `NudgeImmediate` and watching it arrive, so the empty witness
cannot be explained by a deaf fake.

## 2. The defect, live, against a real Claude mid-output

`TestLive_ConfirmedDeliveryReachesABusyClaude`, 2026-07-29 11:25–11:27:

```
receipt file: .../agents/receipts/live-ebee.submits
baseline: a nudge to an IDLE Claude was confirmed (receipts=1)
LIVE DEFECT: wait-idle could not reach a working Claude — nothing was even
  written: wait for idle: agent "live-ebee" still producing output after 20s
  (last PTY write 60ms ago) — agent is busy or stuck redrawing
```

Two things are established here and both are load-bearing:

- **The receipt chain works end to end against the real binary.** pogod
  installed the `UserPromptSubmit` hook into the agent's own
  `.claude/settings.local.json`, Claude ran it, and an idle nudge was confirmed
  by a count that moved. Without this baseline the busy result below would be
  unreadable — a receipt that never moves for *any* nudge looks identical to one
  dropped by a busy harness.
- **wait-idle cannot reach a working Claude.** Claude Code redraws continuously
  while a tool runs, so the quiet window the precondition demands never opens.
  This is the general form of the defect: the precondition is the negation of
  the state a working agent is in.

## 3. The thing we did not expect, and it changed the design

The obvious next step — type into the middle of a turn and wait for the receipt
when the turn ends — does not work, and the reason is not the one Orc's writeup
would predict. `TestDiag_QueuedMidTurn` (a throwaway probe, not committed) ran
three times with the same result:

```
turn started, receipts=1 midTurn=true
typed mid-turn at t=0                      # "QUEUEDMESSAGE please reply with
                                           #  the single word PONG"
t=32s: turn ended. receipts=1
STRANDED: receipt still 1
A) bare return        -> submitted=false  receipts=1
B) second bare return -> submitted=false  receipts=1
C) full resend        -> submitted=true   receipts=2
```

and the screen at `t=32s`, ANSI-stripped, held:

```
PONG  The command completed — it printed 1 through 30.
```

**Claude answered the mid-turn message. The receipt never moved.** One run
watched it for four minutes past the end of the turn; it never moved, while the
answer had been on screen the whole time.

So, measured:

| | mid-turn prompt | idle prompt |
|---|---|---|
| Delivered and acted on | **yes** | yes |
| `UserPromptSubmit` fires | **no** | yes |

**The receipt is blind to mid-turn delivery. It is not evidence against it.**
That is a fact about Claude Code, not about pogo, and it is the single most
important thing on this page — a version of this feature that read the missing
receipt as "dropped" would escalate and resend, and step (C) above proves a
resend is delivered a second time. The message would arrive twice.

It also falsifies the reasoning Orc's §5.2 gives for the same rule. Orc says a
mid-turn session is never retried because "Claude queues the prompt
legitimately", i.e. absence means *not yet*. On this build absence means *not
observable* — the prompt was neither queued nor lost, it was taken immediately.
Same rule, sounder reason, and the rule is now load-bearing rather than
precautionary.

A bare return does not rescue anything here (A and B), which is consistent: with
nothing loaded in the composer there is nothing for it to submit. It remains the
correct FIRST escalation step for the idle case it was designed for, where text
really is sitting unsent — and it still cannot duplicate, which is why it goes
ahead of the resend.

*(Incidental: the composer was observed holding ghost suggestion text — "now run
it again with sleep 0.2" — that neither pogod nor the test typed. Neither bare
return submitted it. Noted so a future reader does not mistake it for an
injected nudge.)*

## 4. What shipped, and what each outcome now means

| Agent state | Behaviour | Result |
|---|---|---|
| Idle / starting | type → bare return → resend → refuse | confirmed, or `ErrNudgeUnconfirmed` naming what was tried |
| Mid-turn | type once, wait one step, stop | `ErrNudgeQueued` — pogod claims neither delivery nor loss |
| No receipt signal | unchanged wait-idle path | exactly the pre-existing behaviour |

The busy case is therefore **improved but not closed**. Before: nothing was
written at all and the caller got an error. Now: the message is written, Claude
takes it, and pogod says plainly that it cannot verify that. What is gone is the
third possibility — reporting success for a message nobody can check.

The scheduler withholds its mail fallback for `ErrNudgeQueued` alone. Section 3
is why that is safe: the message almost certainly landed, so a mail copy would
be a duplicate. Every other failure still falls back to mail, which is what
saved the 09:00 sweep.

## 5. Known limits

- **Mid-turn delivery is unverifiable through this signal.** The remaining
  route is Claude Code's session transcript (`Provider.SessionTranscriptGlob`
  already exists, and `internal/synthfail` already reads it), where a submitted
  user message should appear regardless of turn state. Not attempted here: it
  is a second mechanism, and this ticket's shape was the hook. Worth a follow-up
  item.
- **Transcript saving was off in the sandbox.** The probe ran Claude as a child
  of a Claude session, so `CLAUDE_CODE_CHILD_SESSION` was inherited and the
  harness printed *"Transcript saving is off"*. It does not affect the hook — the
  receipts above are real — but it does mean the transcript route could not have
  been measured in this sandbox even if it had been tried. A follow-up must set
  `CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1`.
- **One harness.** Only Claude Code declares a `SubmitReceiptHook`. Codex, pi
  and cursor agents have no receipt signal and keep the wait-idle behaviour
  unchanged, which is a supported answer rather than a gap — but it does mean
  the busy-agent problem is untouched for them.
- **The live control is opt-in and skips by default.** It spends tokens and
  needs the machine's Claude credentials. This page is the record that it was
  run and what it said.
