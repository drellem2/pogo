# Bare-CR auto-renudge efficacy vs. the paste-buffered kickoff wedge (mg-feb3)

_Empirical verification, 2026-07-14. Anchors: `internal/agent/startverify.go`,
`internal/agent/agent.go` (Spawn initial-nudge goroutine), issue
drellem2/macguffin#24, triage mg-5ece, review ticket mg-fc92._

## Why this exists

The mg-feb3 fix re-delivers a **bare CR** (`a.Nudge("")` — submit terminator
only) to a polecat whose mg work item is still unclaimed after spawn, to recover
the concurrent-spawn init-stall. Unit tests + `TestSpawnDrivesStartVerify` prove
the bare CR is *delivered*; they do not prove it *recovers* a wedged agent (they
run against a stubbed verifier and a `cat` PTY, not a real Ink input box). The
ticket flagged this explicitly — "CR reliability at flushing a real
paste-buffered Ink kickoff has not been established" — and the field-proven
recovery used `nudge "1"` (a char + submit), not a bare CR. This report closes
that gap with a real Claude Code drive.

## The wedge mechanism (recap)

Under a concurrent spawn wave a CPU-starved node/Ink process has not yet armed
its interactive input reader when pogod delivers the kickoff nudge. The bytes
pile in the **kernel PTY buffer** and are read in one burst when the reader
finally arms; Ink's paste heuristic absorbs the whole burst as one paste block
whose trailing `\r` is inserted as literal text rather than re-tokenized as a
submit (mg-ce61). The agent is alive but the kickoff sits **unsent** in the
composer, so the work item is never claimed. The `SubmitDelay` lore in
`internal/agent/provider.go` is the same phenomenon seen from the other side: a
50 ms gap between body and terminator exists precisely so they land in *separate*
`read()`s and the `\r` submits.

## Harness

A throwaway PTY driver (`creack/pty`, 200×50) spawned the real `claude` binary
(v2.1.207, Opus 4.8) at a temp CWD and reproduced the wedge **deterministically**
by inducing the same kernel-buffer pileup the CPU-starvation path produces: it
wrote a 3088-byte kickoff blob ending in `\r` **at t=0, before Ink armed its
input reader**, then waited for the composer to render and settle. (Writing the
identical burst *after* the composer is ready does **not** reproduce the wedge —
the `\r` submits normally — confirming the bug is specifically the pre-arm
pileup, not burst size.) A `PAYLOAD` switch selected the
recovery keystroke — `cr` for the fix's bare `\r`, `one` for the field-confirmed
`"1"`+`\r` (typed through the same body→50ms→terminator path as `pogo nudge`) —
for the head-to-head below. Output was ANSI-stripped for inspection; note the TUI
footer's per-word column moves collapse spaces under `StripANSI`, so ready/submit
markers were matched space-insensitively (see the sentinel space-collapse note,
mg-d06a). The harness is not committed — it drives a real, billable Claude Code
session; the method above is sufficient to reproduce.

## Result

Every run reproduced the wedge: on arm, the **entire kickoff sat unsent in the
composer** (`❯ VERIFICATION PING for mg-feb3 …`) with no processing indicator.
The recovery payload was then delivered and the composer's submit state observed.

**Head-to-head — bare CR (the fix) vs. `"1"`+CR (the field-confirmed mayor
workaround):**

| Recovery payload | Runs | Wedge reproduced | Recovered (buffer submitted) |
|------------------|------|------------------|------------------------------|
| bare `\r` (fix) | 8 | 8/8 | **8/8** |
| `"1"` then `\r` (field) | 5 | 5/5 | **5/5** (see note) |

_Note:_ one early `"1"`+CR run first read as non-recovered; that was a **detector
false-negative** (Claude Code's spinner rotates whimsical gerunds — "Smooshing",
"Seasoning", "Churned", "Brewed" — the first matcher didn't cover). With the
matcher hardened, `"1"`+CR recovered 3/3 on re-run and the earlier runs were
confirmed real recoveries. On recovery, processing indicators appear
(`esc to interrupt`, spinner `(Ns · thinking)`, token counter) and Claude begins
the turn (e.g. "Acknowledged — no action taken … automated verification ping
(mg-feb3)").

## Conclusion

Against a **real** Claude Code v2.1.207 Ink input box holding a genuine
paste-buffered kickoff:

1. **A bare CR alone reliably flushes (submits) the buffer — 8/8.** This settles
   the open question (triage + the mayor's field note): bare CR *recovers*, not
   merely *delivers*. The `"1"` fallback the ticket sanctioned ("only if CR proves
   unreliable") is **not needed**.
2. **The field-confirmed `"1"`+CR also recovers (5/5),** which cross-validates the
   harness: it reproduces the same recoverable state the mayor observed live (a
   3-concurrent-spawn wave where fc92 stalled ~2m44s and `pogo nudge fc92 "1"`
   recovered it).
3. **Bare CR is preferable to `"1"`.** The `"1"` is appended to the buffered
   kickoff text and submitted *with* it, corrupting the prompt with a stray
   character; the bare CR submits the kickoff clean. Same recovery, no
   contamination.

## Conclusion

Against a **real** Claude Code v2.1.207 Ink input box holding a genuine
paste-buffered kickoff, a **bare CR reliably flushes (submits) the buffer** —
recovery, not merely delivery. The core assumption of the mg-feb3 fix holds; the
`"1"` fallback the ticket sanctioned ("only if CR proves unreliable") is **not
needed**, and the bare CR is preferable because it injects no stray character
into the submitted prompt.

## Scope / caveats

- This reproduces the **downstream** wedge state (pre-arm kernel-buffer pileup →
  paste absorption → non-submit) and the recovery, which is failure-mode-agnostic
  and identical regardless of what caused the pileup (CPU starvation, a stale
  ready-sentinel, or an early write). It does **not** reproduce the *upstream*
  CPU-contention timing of a live multi-polecat spawn wave — still a follow-up
  gate, and the reason the mayor's MAX-2 cap + unstarted-check is intentionally
  retained by this ticket until a live wave confirms recovery end-to-end.
- Claude Code's paste heuristic is version-specific (v2.1.207 here); the
  `auto_renudge` event + the fleet-wide sentinel-drift detector (mg-ce4c) are the
  standing signals if a future harness version changes submit tokenization.
