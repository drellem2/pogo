- **The crew's restart obligation now has an owner that survives the deploy
  dying: pogod arms a resume deadline at the moment of the stop and restores the
  fleet itself if nothing else has (mg-5af1).** Stopping orchestration is step 1
  of a two-step sequence and, until now, the only thing that could perform step 2
  was the process that performed step 1. On 2026-08-08 that contingency was
  collected: the crew was stopped at 00:44:20Z, the run that stopped it hung at
  02:00:05Z for 31h39m, and the fleet stayed dark for 33 hours with every
  supervisor behaving correctly — a requested stop is not a crash, so nothing was
  entitled to undo it.

  `internal/server` now records a `ResumeObligation` inside
  `transitionToIndexOnly` — before the stop work, under the transition lock, at
  the same site and for the same reason as the mode-change audit record.
  `cmd/pogod/orchresume.go` fires it from the heartbeat tick: past the deadline
  it calls `StartOrchestration` and mails the coordinator naming who stopped the
  fleet, when, and what came back. Returning to full mode by ANY route discharges
  the obligation, so the resumer cannot issue a duplicate restart and cannot
  latch.

  **This is the OWNERSHIP half of mg-56ac's repair #3; the alerting half already
  shipped as 974edc1 (mg-6d2f) and is untouched.** A deploy that leaves the fleet
  stopped still exits 11/12 saying so. The gap this closes is that the 08-08 run
  never reached an exit at all, so a loud exit would not have fired — and exiting
  loudly is not the same as somebody else holding the obligation.

  **Why pogod and not a `trap`, a background child, or a crew watcher.** The
  holder has to outlive the stopper, which rules out everything the stopper owns;
  and it cannot be the crew, because the crew is what is down (the same constraint
  mg-a14c carries). pogod is a separate process from every stopper, it stays up
  for the whole index-only window by definition, and its heartbeat is not gated on
  the run mode.

  **Declaring a long stop.** `pogo server stop --hold 4h` (or
  `POST /server/stop-orchestration?hold=4h`) declares a longer window so a
  deliberate maintenance stop is not fought. There is no indefinite hold: an
  undeclared indefinite dark fleet is indistinguishable from an outage, and it was
  one. `pogo server stop` now prints when the fleet comes back, and says so
  loudly when NO deadline is armed. `/server/mode` gains `stopped_since` and
  `resume_due`; the stop endpoint returns a `StopReport`.

  Configurable via `[orchestration_resume] enabled` / `grace` / `retry`
  (defaults: on, 15m, 1m). `enabled = false` restores the pre-fix behaviour and
  pogod says so at boot in those words.

  **What it does not cover, stated.** The obligation is in memory and dies with
  the process. That is correct for a pogod that is killed and restarted — the
  successor boots into full mode by construction, which is the state the
  obligation would have restored — and it is NOT coverage for a pogod that is
  killed and never restarted. That is a dead-daemon problem belonging to the
  tier-1 heartbeat reaper. And when the restore itself fails, the notice is
  written into a fleet that is still down; it says so rather than implying it was
  read.
