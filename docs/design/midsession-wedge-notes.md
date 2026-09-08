# WIP notes: mid-session polecat wedge detector (mg-daf4)

**STATUS: SUPERSEDED BY A SHIPPED DETECTOR (mg-5246, 2026-09-08).** The
detector is `internal/midsessionwedge`, wired in `cmd/pogod/main.go` and
configured by `[midsession_wedge]`. What it does, what it measured, and where it
DEPARTS from the shape sketched below are in "What shipped" at the end of this
file. Read that section before this one: the survey here is still accurate and
still worth having, but the design sketch in "The shape I was going to build"
was changed on contact with the measurement and is kept only as history.

Original header, 2026-09-03: *INVESTIGATION ONLY. No detector code written.*
Work stopped at 2026-09-03 ~21:47Z on a fleet-wide pause (GitHub token
rotation). This file is committed so the reading below is not lost with the
worktree; it is notes, not a design ruling, and the numbers in it are the only
ones I measured myself.

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

---

# What shipped (mg-5246, 2026-09-08)

`internal/midsessionwedge`, wired in `cmd/pogod/main.go` on the heartbeat tick
and configured by `[midsession_wedge]` (on by default). The seam it reads is
`internal/agent/queuednudge.go`.

## The one change of shape, and why the sketch above could not stand

The sketch's conjunction is *ring static for the threshold* AND *a durable probe
of work-item / worktree / branch state*. Measuring the first clause is what
broke it.

**A polecat that has finished its work and is holding for the coordinator is
parked at an empty composer with a byte-identical ring, indefinitely, by
design.** That is every polecat's normal end state. It was measured doing exactly
that for 19.0 minutes in a 20-minute window, and there is no duration a healthy
hold does not reach, so no threshold excludes it. Its work item is claimed, its
worktree is still, its branch is pushed — the durable probe agrees with the
quiescence timer, and the detector fires on every finished polecat on the box.

What actually separates the wedge from the hold is that in the wedge **a submit
is owed**, and pogod already knew and had thrown the knowledge away.
`deliverConfirmed` (`internal/agent/nudge.go`) writes a nudge to a mid-turn
agent, gets no submission receipt — Claude Code emits no `UserPromptSubmit` for a
prompt typed into the middle of a turn — and returns `ErrNudgeQueued`, correctly
declining to resend something that probably landed. Right at delivery time; and
it leaves a prompt in a working agent's composer with nothing watching whether
the end of the turn ever drains it. `queuednudge.go` makes that moment durable:
the timestamp, and the receipt count the obligation is measured against. 3611
such deliveries are in this box's event log.

So the shipped conjunction is:

    a submit is owed
    AND the receipt count has not moved since
    AND the PTY ring has been byte-identical for Quiescence
    AND the worktree has not moved either

Four independent witnesses, checked cheapest-first, and **every one of them can
only clear the alarm, never raise it alone.**

## The measurement

Live fleet, 2026-09-08. Eight agents' FULL 64KB rings every 2.19s for 20.4
minutes (561 readings each); plus one continuously-working agent's ring and
receipt count sampled *together* every 0.44s for 10.3 minutes (1390 readings);
joined against `~/.pogo/agents/receipts/<agent>.submits`, which the harness
timestamps per submit and so needed no sampling of its own.

The two readings the ticket demands be taken separately:

| agent | ring changed on | longest byte-identical run |
|---|---|---|
| t5246 (working continuously) | **100.0%** of 2.19s samples, **99.9%** of 0.44s samples | **0.42s** — one sampling period |
| mayor | 21.4% | 10.6 min |
| architect | 5.4% | 5.5 min |
| pa / pm-onethird / pm-pogo / pm-riemann | 2.1–2.7% | 9.7–9.8 min |
| tc86d (polecat holding after `mg done`) | 0.4% | **19.0 min** |

- **A working agent's ring never holds still.** Longest completed run across 571
  of them: 2.35s at the coarse cadence, 0.42s at the fine one — the resolution
  of the instrument, not a property of the agent.
- **A parked agent's ring is byte-identical for tens of minutes.** Fleet-wide, 11
  of 788 completed quiet runs (1.40%) reached five minutes, and every one
  belonged to an idle agent with **nothing owed**.
- **The composer drains at the instant the turn ends.** At every submit observed
  from a working agent the ring had been quiet 0.00s.

`Quiescence = 5 minutes` follows: ~700x the measured working-agent ceiling of
0.42s, and strictly inside the fleet's `*/10` cron cadence so a wedged agent
reaches it within one gap instead of having its quiet run reset by every fire.
That second argument is the sketch's, kept — but it is now the *second* reason
rather than the only one, which is the difference between 5 and the sketch's
unverified 8.

### A claim above that this work did NOT reproduce

The section "Measurement I did take" argues the ring hash beats
`RingBuffer.LastWriteTime` because an identical repaint frame leaves the 64KB
window unchanged while `lastWrite` advances — so repaint-without-progress is
visible to the hash and invisible to the timer. **That case did not appear.**
Every parked agent measured was *silent*: `last-activity` aged monotonically
across the whole quiet run (`pa` 7m3s → 9m22s at a fixed hash, sampled every
15s), so the hash and the timer agreed on all of them. The hash is kept because
it **dominates** — never worse, and it covers the reported-but-unreproduced case
mg-daf4 saw in pae5a — not because a measurement showed it winning.

### The worktree probe, measured against a live working agent

Two readings from this box on 2026-09-08, taken against the polecat that wrote
this — working continuously the whole time:

- **Its worktree metadata (`HEAD`, `index`, `logs/HEAD`, `ORIG_HEAD`) had not
  moved in 50 minutes.** That is the mayor's positive-only limit as a direct
  observation rather than an argument: a live, busy, provably-healthy agent is
  indistinguishable from a wedged one by this probe alone. It can only clear.
- **Repeated `git status`, `git diff` and `git log` over that window did not
  advance the index mtime.** So the obvious accidental-exoneration path — a
  read-only command refreshing the index and silently clearing the alarm for
  every polecat — did not occur. One box, one day; the exoneration event names
  the path regardless, so if it ever does occur it reads as the same filename
  every time instead of as silence.

A third, smaller thing fell out of it: this worktree's linked git directory is
`.../.git/worktrees/t5246`, not `.../polecat-t5246`. The probe takes the path
from `Agent.WorktreeDir` — an observation — rather than reconstructing it from
the agent's name, which would have been a claim, and a wrong one.

### What is still not measured

The **joint** distribution — quiet run *and* submit owed — over the queued
population. The fleet produced zero queued deliveries during the observation
window; the current rate is 0.2–0.8/hour. Historically 1558 queued deliveries
join to a later receipt with p50 19.3 min and 16.6% still unsubmitted an hour
later, but that latency spans the whole spinning period and cannot be split into
"still working" and "parked with the prompt loaded" without ring history nobody
kept. The working-agent ceiling stands in for that split; it is an inference,
not a direct observation of the rare case.

## The action, and why it is this one

A **bare return** (`Agent.Nudge("")`) and nothing else. It carries no content, so
it submits whatever is loaded and cannot duplicate anything — which is exactly
why `deliverConfirmed` already puts a bare return *first* in its own escalation.
Attempts are bounded per owed submit, each emits an event, and recovery is
confirmed by the receipt count moving, never by the nudge returning nil (writing
to a PTY master succeeds whether or not anything is listening). `report_only =
true` keeps the detection and withholds the keystroke.

## This detector's own blind spot, stated because it is the same shape

It fires only on an agent that owes a submit, and a submit becomes owed only via
the `ErrNudgeQueued` branch. On a box where that branch never runs, this watcher
samples forever, judges nobody, emits nothing, and is **indistinguishable from a
box with no wedges** — the instrument-that-cannot-fail one level out. So
`Watcher.Snapshot()` reports `Armed`, `LastSample`, `Judged`, `Skipped` and
`Owed`: a long run of `Judged > 0, Owed == 0` is a detector with nothing to do,
and `Judged == 0` is a detector covering nobody. **Nothing consumes it yet** —
that is a seam for a `blindwatch`-shaped reader, not a claim that one exists.

The worktree probe carries the same hazard in miniature: it can only exonerate,
so a path that moves for reasons unrelated to progress (a background `git status`
refreshing `index` is the candidate) would suppress every finding, silently. The
exoneration event therefore names **which** file carried the newest mtime, so
that failure reads as the same filename every time instead of as silence.

## Residue, unchanged from mg-daf4 and still first in line

It does not catch a wedge whose PTY keeps receiving bytes from something
unrelated to the agent's own progress. It also sees only deliveries pogod made
itself: a prompt a human typed into an attached terminal and never submitted owes
nothing pogod recorded, and is invisible here.
