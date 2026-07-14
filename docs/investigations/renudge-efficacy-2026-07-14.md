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
pileup, not burst size.) Output was ANSI-stripped for inspection; note the TUI
footer's per-word column moves collapse spaces under `StripANSI`, so ready/submit
markers were matched space-insensitively (see the sentinel space-collapse note,
mg-d06a). The harness is not committed — it drives a real, billable Claude Code
session; the method above is sufficient to reproduce.

## Result (3/3 runs)

| Step | Observation |
|------|-------------|
| Pre-arm burst written at t=0 | On arm, the **entire kickoff sits unsent in the composer** (`❯ VERIFICATION PING for mg-feb3 …`), no processing indicator → `auto-submitted? false`. **Wedge reproduced.** |
| Bare `\r` delivered (the fix's payload) | Composer submits: `esc to interrupt` / `thinking` / token counter appear and Claude replies ("Acknowledged — no action taken … automated verification ping (mg-feb3)"). → `recovered? true`. |

```
==================  VERDICT  ==================
wedge reproduced (burst not auto-submitted): true
bare CR recovered (submitted the buffer):    true
==============================================
```

Three consecutive runs produced the identical verdict.

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
