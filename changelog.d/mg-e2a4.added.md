- **ack-watch grew a second, ABSOLUTE arm — FLEET BLACKOUT — and it escalates
  outside the fleet on its first sample rather than after 24 hours (mg-e2a4).**
  On 2026-08-09 every crew agent and polecat stopped completing turns at ~12:50Z
  and resumed at ~17:20Z, an `ENOTFOUND` on the model API that pogod's own
  wedge-watch classified correctly as `cause=network_down`. The diagnosis was
  never the problem. Over 13:20–17:20 the events log holds 274 `nudge_sent`, 251
  `scheduler_fire_delivered` and **3** `scheduler_fire_completed`; zero merges,
  zero spawns, zero work-item transitions. Every agent read `status=running`,
  `last-activity=just now`, because PTY animation is output. The human found out
  because it was his own wifi.

  **Two things were wrong, and neither was the detector failing to notice.**

  **1. The per-schedule test cannot fire on a uniform failure.** It is
  peer-relative: `gap = peer_median - rate`. In a total outage every schedule
  degrades in lockstep, the median falls with the members, and the gap stays near
  zero. Every `ack_watch_fired` that day reported `deficit_count: 0` — including
  the one at 16:12:59, mid-outage. That is not an arithmetic bug, it is the
  defining blind spot of an outlier detector: the worse the outage, the more
  uniform it is, and the more uniform it is the less it looks like a deficit. The
  detector was weakest exactly where the failure was worst.

  **2. The branch that DID see it could only tell someone inside the outage.**
  The cohort-level branch worked — `fleet_count: 1` in every firing that day. It
  mailed `mayor` and nobody else, with `escalated: false`, because escalation to
  a person required 24h of persistence. A 4.5-hour total fleet death produced one
  mail, addressed to an agent whose own mail-check showed the same 27 unacked
  fires. *The only recipient of a fleet-outage alarm was a member of the fleet.*

  **What was added.** `Report.Blackout`: one fleet-wide finding computed from the
  ABSOLUTE completion rate over a trailing three hours — fires delivered against
  fires completed, no median, no peer set, nothing compared to anything. It gets
  *more* confident as the failure becomes more uniform, which is the exact inverse
  of the peer arm's failure mode, and that is the point of having both.

  **Both of its false-positive paths were found by measurement, not reasoning,
  and both are gated.** "Fires delivered and nothing completing them" describes
  three situations and only one is a fault. (1) *The fleet is not there*: between
  00:00 and 09:30Z that same day the scheduler delivered ~30 fires an hour and
  completed ZERO, every hour, all night, because no crew agent was running.
  Ungated, this arm mails a person at 4am nightly — strictly worse than the
  silence it replaced. So it judges RUNNING agents only, and only those up for the
  WHOLE window: a spawn 40 minutes ago cannot speak for the last three hours, and
  that produced a measured false positive at 10:00Z right after the crew came up.
  (2) *The fleet is working in long turns*: a fire is only ackable at the end of an
  agent turn, so completions come in bursts, and the same day had a TWO-HOUR
  stretch with all six agents up and working, 84 fires delivered, none completed.

  The window was therefore swept against the real log rather than chosen — 1h gave
  9 false positives, 2h gave 4, 2h30m gave 1, **3h gave 0**. The cost is stated
  rather than hidden: detection lands about three hours into an outage, and an
  outage shorter than that is not caught by this arm at all, because no
  completion-based measurement can separate it from healthy bursty acking sooner.
  Three hours is still the difference between a notice and none. With both gates
  the arm fired on 4 samples across the whole of 2026-08-09, all inside the
  outage, and on no others. The peer arm is
  untouched: it catches the single deaf agent (mg-1935, the reason the package
  exists), and an absolute rule alone would be noisy across schedules with
  legitimately different turn lengths.

  It reads **events**, not the counters already in hand, for two measured
  reasons. The counters are lifetime totals, so a cohort median below the floor
  cannot distinguish "dead right now" from "carried a bad ratio for days" — which
  is why the branch that fired was useless. And they are zeroed by
  re-registration, which the nightly redeploy guarantees, after which `min_fires`
  blinds every counter-based arm for over three hours; an outage starting at 03:30
  was invisible to anything reading the table. `Detect` stays pure: the caller
  supplies the window (`ackwatch.RecentFires`).

  **Routing is structural, not a timeout.** A blackout copies the escalation box
  on its first sample and ignores `escalate_after` entirely, including the
  negative value that disables the age-based escalation — the same rule, for the
  same reason, as `deafwatch.escalateNow`. A 24-hour persistence bar is sensible
  for "one agent is lagging its peers" and wrong for "nothing has completed a turn
  in hours"; those do not share a clock. The asymmetry that makes an out-of-band
  path exist at all is that **pogod survived**: it kept sampling and writing while
  every agent it hosts was inert, and the escalation box is polled out of process,
  so no fleet agent, no agent turn and no schedule anybody acks sits anywhere on
  that path.

  **And the recipient is checked against the population it is reporting on.**
  `ack_watch_fired` now carries `notify_to_stalled` and `escalate_to_stalled`; a
  bare `notified: "mayor"` is what let 16:12:59 read as a delivered alarm. If the
  escalation box is itself one of the stalled agents, the mail says so in as many
  words — that state is this defect one level in, not a weaker version of it. A
  blackout notice that every recipient refused emits its own
  `ack_watch_blackout_unreported`, because a `mail_error_*` key buried in the
  details of an event named "fired" is not greppable as "the fleet was dead and
  the alarm never left the machine".

  **New `[ack_watch] blackout_renotify`, default 30m** — the sampling interval —
  replacing `renotify_after` (6h) while a blackout stands. The incident began and
  ended inside a single renotify shadow, so an arbitrarily severe event produced
  one notice, and a second identical outage 30 minutes later would have been
  suppressed entirely. Repetition is the signal here: the notice leaves for a
  reader with no acknowledgement path back, so "was the last one seen?" is
  unanswerable from this side, and the all-clear is the notice stopping. The
  ordinary window is unchanged — a standing per-schedule deficit still mails once
  per 6 hours, asserted.

  **Blind is not calm.** A window that was not supplied, could not be read, or was
  too small is reported as `blackout_blind` with a reason — on the fired path, on
  `ack_watch_clear` (`blackout_judged`), and in `pogo check-acks` output. Zero
  completions is precisely what a blackout looks like, so a failed read arriving
  as a measurement of zero would mail a person about a healthy fleet, and one
  arriving as a clean scan would be the silence this package exists to end.

  **The positive control is the point.** The fleet branch fired during this
  incident and was still useless, so a test asserting "we detected it" would have
  passed throughout. The tests therefore assert the two things that were actually
  wrong. `TestBlackout_TheUniformOutageThePeerRelativeArmCannotSee` builds the
  measured 17:21Z table and reproduces `deficit_count: 0` and `fleet_count: 1`
  exactly, then requires the new arm to fire.
  `TestBlackout_WritesToARecipientOutsideTheStalledFleetOnTheFirstSample` asserts
  the *property* — that at least one recipient is not a member of the population
  the finding is about — rather than a mailbox name, because "also mail human" is
  not the fix. Both were confirmed by mutation: reverting the escalation makes
  four tests fail, and reverting the renotify window makes the escalation box hear
  about a 4.5-hour outage exactly once.

  Documented in [CONFIGURATION.md](../docs/CONFIGURATION.md) §"The second arm:
  FLEET BLACKOUT". Not merged with mg-20eb (wedgewatch), which is a different
  detector and did its job here — it fired twice with `cause=network_down`.
