# WIP notes: mid-session polecat wedge detector (mg-daf4)

**STATUS: INVESTIGATION ONLY. No detector code written.** Work stopped at
2026-09-03 ~21:47Z on a fleet-wide pause (GitHub token rotation). This file is
committed so the reading below is not lost with the worktree; it is notes, not a
design ruling, and the numbers in it are the only ones I measured myself.

## What the tree already has, and why none of it covers this

- `internal/agent/startverify.go` — the auto-renudge net. Per-spawn, one-shot,
  25s x 3 attempts, gated on a started-signal (`work_item_unclaimed`,
  `claim_pid_not_restamped`, `no_ready_composer`). Every one of those signals is
  satisfied long before a mid-session wedge, exactly as mg-daf4 says.
- `internal/wedgewatch` — report-only, credential/dead-end focused, STRING
  matching (`markers.go`) plus the harness's own elapsed counter
  (`counter.go`). String matching is what mg-daf4's 21:18Z and 21:22Z entries
  retract; not the instrument to build on.
- `internal/progresswatch` — FLEET-level (`DefaultMinWorkers = 3`), report-only.
  A single wedged polecat is below its population floor by construction.
- `internal/turnwatch` — 3h staleness, 30m hold-down, 45m grace. A 17-minute
  wedge sits entirely inside its blind spot, and it says so itself.
- `diagnoseAgentAt` (`internal/agent/api.go:423`) — `StallThresholdPolecat` is
  5 minutes, measured from `RingBuffer.LastWriteTime()`. pae5a read `idle`, not
  `stalled`, at 17 minutes, so SOMETHING was writing to that PTY inside 5
  minutes. I could not establish what; the ticket's own caveat about the
  "1 shell still running" note is unresolved and stays unresolved.

## Measurement I did take (2026-09-03 21:44Z, this box)

Sampled `pogo agent output <name> | md5` every 15s alongside `last-activity`:

    21:44:27 / 21:44:42
    architect     83f97bfe...  ->  83f97bfe...   STATIC    la 2m46s -> 3m2s
    pa            57e52b6d...  ->  57e52b6d...   STATIC    la 2m3s  -> 2m18s
    pm-onethird   50be0d61...  ->  50be0d61...   STATIC    la 2m1s  -> 2m16s
    mayor         e663ef66...  ->  e9b2654a...   CHANGED   la just now
    pa854         cbd21bfc...  ->  fbe036c5...   CHANGED   la just now

So the content hash separates working from parked agents on this box, and it is
NOT the same reading as `last-activity`: an idle-at-composer crew agent's
`last-activity` ages monotonically (2m46s -> 3m2s), i.e. it is not being
repainted at all here. That contradicts mg-daf4's "`last-activity=just now`
whenever the TUI repaints" for THESE agents; I did not reproduce whatever kept
pae5a's timer fresh. Two readings 15s apart is a positive control for the
instrument, nothing more — it is explicitly NOT a wedge threshold (mg-daf4's own
21:32Z retraction).

Why a hash of the ring buffer is stronger than `LastWriteTime` and not just a
restatement of it: `RingBuffer` (`internal/agent/ringbuf.go`) holds the last
64KB of the byte STREAM. Appending an IDENTICAL repaint frame drops one frame's
worth off the front and adds it at the back, so the window content is unchanged
and the hash holds while `lastWrite` advances. Repaint-without-progress is
therefore invisible to `LastWriteTime` and visible to the hash.

## The shape I was going to build (not built, not reviewed)

In `internal/agent`, beside `startverify.go`, riding `hb.OnTick` in
`cmd/pogod/main.go`:

1. Every ~1 minute (must be much finer than the threshold), hash each live
   polecat's ring. Cheap, in-process, no syscalls, no string matching. Any
   change clears that agent's static timer immediately — mg-daf4's asymmetry:
   CHANGED across any interval is conclusive liveness; STATIC only means
   something across a long one.
2. Only for an agent whose hash has held for the threshold, run the EXPENSIVE
   durable probe (work-item status/claim pid, worktree HEAD sha, branch tip on
   origin). This ordering is the ticket's "cheapest first", and it makes the
   expensive case rare by construction.
3. On the conjunction, deliver a bare submit terminator (`a.Nudge("")`) —
   the same payload the start-verifier sends and the same thing that released
   pae5a — bounded attempts, event-emitting, declining LOUDLY when the probe is
   unavailable rather than firing blind.

Threshold: NOT measured. The ticket bounds it from both ends and nothing sits
between (one wedge at 17m, one false positive at 7s). My intended pick was 8
minutes, on the argument that it is above `StallThresholdPolecat` (5m) and
strictly inside the fleet's `*/10` mail-check cadence, so a wedged agent can
reach the threshold within one cron gap instead of having its timer reset by
every fire. That argument is unverified and the number is a pick, not a
measurement.

## Residue I had already identified and would have had to declare

The conjunction does NOT catch a wedge whose PTY keeps receiving bytes from
something unrelated to the agent's own progress — a background shell, or a cron
nudge landing in the composer. That is mg-daf4's own "an instrument that cannot
fail because something unrelated keeps satisfying it", one level down, and it is
the first thing to attack when this resumes.
