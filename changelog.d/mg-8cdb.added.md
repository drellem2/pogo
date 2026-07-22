- **pogo now detects the agent failure that made every health check read green for a day —
  and refuses to restart it (mg-8cdb).** mg-18d0 established that the 23h30m fleet outage of
  2026-07-22 was credential expiry, not wedging, and that the decisive evidence was written to
  local disk within milliseconds of every failure. Nothing read it. This is the reader.

  **The class, not the incident.** When a harness cannot reach the model it does not block and
  does not crash: it answers the turn locally, in ~10ms, with a zero-token error. Auth expiry is
  the most spectacular member of that family and not the common one — across fleet history
  mg-18d0 counted ~5500 such turns, of which rate-limit was **2818** against login-expired's
  **914**. So `internal/synthfail` detects **structurally** (synthetic model attribution + zero
  tokens in and out + API-error flag) and never keys on a message string. The strings are used
  afterwards, to name a reason for a human; a harness that invents a new error code is still
  detected, and merely falls back to an unnamed reason. There is a test for exactly that.

  **One reader distinguishes two opposite failure modes.** A genuinely wedged agent writes
  **nothing** to its transcript; an agent in this class writes a **new turn on every nudge**.
  That asymmetry is the whole design, and it is why this needed no new instrumentation.

  **DETECT AND PAGE — restart is suppressed, not merely discouraged.** No member of this class
  is fixable by restarting: the replacement session inherits the same dead credential, limit or
  cap, while the restart discards the live session's accumulated context (pm-pogo held **2339
  messages** at failure) and overwrites the transcript the diagnosis rests on. mg-18d0 costed the
  ungated path at ~11 rounds x 6 agents = **~66 restarts recovering nothing**. Suppression is
  enforced in three places, not asserted in one: `Registry.ShouldRespawnAgent` gates pogod's
  respawn hook, `pogo agent diagnose` reports `health: failing_turns` with `restart_suppressed`,
  and the mayor's stall-watch prompt gains a **mandatory pre-restart check** before its
  120-minute rule — which is where the only restart path that exists today actually lives.
  Every withheld restart emits `synthetic_failure_restart_suppressed`, because a suppression
  that only ever happens silently is indistinguishable from one that never shipped.

  **Paging is coalesced into episodes.** This class is characteristically fleet-wide — one
  credential is shared — so per-agent mail would turn one fact into an N-agent storm at the
  moment a human most needs one clear thing. One page to `human` on episode open, one on close,
  following the usage-limit coordinator's precedent (gh #45). The record is not coalesced: every
  agent still gets its own `synthetic_failure_detected` event.

  **The path is provider-declared, exactly as mg-5a06 treated the memory root.**
  `~/.claude/projects/**` is harness-internal and not a contract pogo owns. `agent.Provider`
  gains `SessionTranscriptGlob`; Claude declares its slug encoding inside `internal/claude`;
  codex, pi and cursor decline explicitly with the measurement behind each (2026-07-23), and a
  test fails when `All()` grows so a new harness must decide rather than default. **A missing
  transcript is `StateUnavailable`, never `StateQuiet`** — the tri-state's zero value is the
  no-claim answer, `diagnose` prints "this is not evidence of health", and every behaviour falls
  back byte-for-byte to what it was before the detector existed. Reading absence as an all-clear
  is the error the incident was made of, and it is asserted against in both packages.

  **Verified by discrimination, not by firing.** A detector proven only against the failing case
  would pass by saying yes unconditionally, and distinguishing the modes is its entire job. It
  is checked against the untouched 2026-07-22 transcripts still on the incident machine:
  `pm-pogo` FIRES with **143 failing turns — mg-18d0's measured count to the turn** — while
  `doctor`, which received no nudges that day and emitted nothing, STAYS SILENT over the
  identical window with restart left available. Both halves also run in CI against checked-in
  fixtures: real verbatim failure turns for each member of the class, plus a wedged-silent
  negative control. Writing the window's upper bound test found a real bug — the window had no
  horizon, so any historical scan swept in everything after it.

  **The controls were run RED, and one of them came back green.** Six deliberate breaks were
  applied and reverted: a reader that counts every record, one that reads absence as health, a
  respawn gate that ignores the verdict, `failing_turns` demoted below `stalled`, and a pager
  that suppresses on any state. Five failed the suite as intended. The sixth — the live
  `doctor` control — **passed against a reader that could not discriminate at all**, because
  doctor's window is genuinely empty, so counting everything still counts nothing. That control
  proved less than it appeared to. A third live control was added to close the gap: pm-pogo's
  own RECOVERY window, the same file an hour later, 63 real model turns, which the broken
  reader then correctly failed. A verification that had not been attacked would have shipped
  the weaker one.

  **Known gap, deliberately left to its own ticket (mg-a754).** This detector reads transcripts;
  the events log still counts *delivery* rather than *completion*. During the outage
  `scheduler_fire_delivered` logged **647 successful deliveries** and `nudge_sent` **771** — all
  true, all useless, and the reason a 100%-dead fleet was indistinguishable from a healthy one.
  A completion signal belongs in the scheduler and the nudge path, not here; filed rather than
  folded in, because it changes the shape of two subsystems this ticket does not otherwise touch.
