# The drain budget outlived its calibration

**Date:** 2026-07-31 · **Work item:** mg-8f7e · **Xref:** mg-46a4
([what the drain actually does](redeploy-drain-2026-07-17.md)), mg-cae1
(`1b1f12d`, drain mode), mg-65b2 (the fail-closed gate), mg-42ac (the nightly
trigger)

Read against `b3efaa2`. The two open questions in mg-8f7e's *Bound* are answered
here by measurement; the rest records why the fix took the shape it did.

## What happened

The 2026-07-31 nightly redeploy exited **7 — drain stalled**.

    02:00:04Z  enabling drain mode
    02:00:04Z  draining: 5 polecat(s) still active — waiting...
    02:10:14Z  draining: 4 polecat(s) still active — waiting...
    02:20:08Z  draining: 3 polecat(s) still active — waiting...
    02:30:17Z  ERROR: 3 polecat(s) still active after 1800s drain timeout
    02:30:19Z  restoring dispatch to its pre-deploy state (draining=false)

The three that blocked it had uptimes of **1h33m, 1h19m and 38m**. Two of them
had individually been running longer than the whole 1800s budget before the
drain started. The box was healthy, dispatch was correctly restored, and about
24 hours of merges stayed inert with the next attempt 24 hours away.

## The two questions the ticket left open

The ticket was filed without reading the drain implementation and asked that
both be confirmed before anything was designed. Both are now measured.

### 1. Is the 1800s configurable? — **Yes, and it was never being configured**

`scripts/pogo-self-deploy` sets `DRAIN_TIMEOUT=1800` as a plain default and
accepts `--drain-timeout SECS` to override it (arg parsing at the foot of the
file). The nightly wrapper `scripts/launchd/pogo-deploy.sh` did **not** pass it,
so the unattended run — the only run that has all night available — was taking
the default meant for a human waiting at a terminal.

### 2. Can new spawns begin after `draining=true`? — **No. The 5→4→3 was pure drain**

This refutes the ticket's leading hypothesis (its option 2, "stop dispatching
before the drain starts"), which is already implemented:

- `internal/agent/api.go`, `handleSpawnPolecat`: `if r.Draining()` → **503**,
  before any spawn work, with the refusal naming the agent and work item.
- It is the only path that creates a polecat — `pogo agent spawn-polecat` is a
  client of that endpoint, and nothing in `pogod` spawns one behind it.
- It is **live in the running daemon**, not merely merged:

      $ curl -s http://127.0.0.1:10000/version | jq -r .revision
      d31297f493cdd757fc46654351e0a2c93e66f49b
      $ git merge-base --is-ancestor 1b1f12d d31297f && echo live
      live

So the fleet was not racing the drain. The count fell monotonically because
polecats finished, and the three that remained were work that had been in flight
before the drain began. **The failure needs no explanation beyond the one the
numbers give: the budget was smaller than the work.**

## Why a bigger constant was the wrong fix, and what replaced it

The ticket's objection to option 1 is correct as far as it goes: 30 minutes was
not a calibration that expired, it was a guess that had never been tested. The
2026-07-30 deploy drained **0 polecats in 3m50s**; the budget's first real
exercise was the night it failed. A 60-minute guess would fail the same way
later.

The fix is to stop having a constant. The quantity that actually constrains the
drain is **when the deploy window closes** — the redeploy must not bounce the
fleet into the working day — so the budget is derived from it:

    drain budget = (seconds until WINDOW_END) - RESERVE,  capped at MAX_DRAIN
                   skipped entirely if below MIN_DRAIN

`RESERVE` (1200s) is what the run still owes after the drain returns: `go
install`, the post-install revision check, `do_prove`, the kickstart,
`verify_running`, the mail-check post-check and the wrapper's own grace sleep.
`MAX_DRAIN` (7200s) bounds patience, because nothing dispatches while
`draining=true` and unbounded patience trades a missed deploy for a night with
no work in it. `MIN_DRAIN` (600s) is the floor: a drain that cannot finish has
still stopped dispatch for its whole length and delivered nothing.

The deploy window widened from `2-5` to `2-6` at the same time, and that is not
slack — the window's width is now the deploy's patience. A 03:00 fire gets the
full 2h cap where `2-5` would have capped it at 100 minutes.

Nothing here needs recalibrating when polecats get longer. The window is a thing
an operator already reasons about; 1800 was not.

## The cost of waiting longer, and why it is smaller than it looks

Because `draining=true` refuses **all** new polecat dispatch, a drain is not
merely a late bounce — it is an interval in which no new work starts for anyone.
The architect raised this against raising the budget at all: a budget sized to
cover current polecat lifetimes is 2–3 hours, and that reads as a 2–3 hour
dispatch freeze on every deploy night.

**The freeze is bounded by fleet quiescence, not by the budget.** The drain ends
the moment the count reaches zero; the budget only decides when to give up
waiting. The 2026-07-30 deploy drained in **3m50s** under the same 1800s budget,
and it would have drained in 3m50s under a 2-hour one. So a larger budget costs
nothing on a night that would have succeeded anyway.

What it does cost is confined to the nights that **stall**: those pay the full
budget and get no activation for it, where before they paid 30 minutes and got
no activation for it. That is the real trade, and it is why `MAX_DRAIN` exists
at 7200s rather than letting the budget run to the window's end.

Rather than argue the number, the stall now leaves a measurement behind:
`dispatch_freeze_note` reports the frozen interval in the log, in the RED alert,
and as `dispatch_frozen_s` on the `deploy_nightly_retry_pending` event — for
exit 7 only, because on every other outcome the elapsed time includes the build
and the bounce and quoting it as a freeze would overstate it. After a week of
deploy nights the cap is a decision with data behind it, adjustable via
`POGO_DEPLOY_MAX_DRAIN` without a code change.

## Why the retry is the *second* half, not the first

Option 4 (cheap retries) is implemented — the plist now fires at 03:00, 04:00
and 05:00 — but it must not be mistaken for the fix, because **three short
attempts are strictly worse than one long one**.

The reason is finding 2 above. A drain is monotone only while it is running:
`draining=true` refuses dispatch, so the count only falls. The moment an attempt
gives up, `pogo-self-deploy`'s exit trap restores dispatch and the fleet
refills. A retry therefore starts against a partly-fresh fleet, and the long
blockers it was waiting out have been joined by new arrivals. Repeated short
budgets do not converge on a busy fleet; one long budget does.

So the budget comes first, and the retry is the backstop for an attempt that
ended **early** — one that stalled with window to spare, or a night where the
03:00 fire never happened because the mac was asleep. Under the production
numbers a 03:00 attempt that uses its full 2h is still draining when the 04:00
and 05:00 fires land, and they exit 0 on the lock. That is the intended ordering,
not an accident.

The retry is scoped to **exit 7** for the same reason: it is the only exit whose
cause is "the fleet was busy" — transient by construction — and the only one
that built nothing and bounced nothing. A build failure or a `do_prove` RED
fails identically an hour later and mails a duplicate alert, so the night is
settled on the first attempt. The outcome is recorded in
`~/.pogo/deploy-attempt.stamp`; an absent or unreadable record reads as "first
attempt", so a corrupt stamp costs one extra attempt rather than silently
disabling the nightly.

## The secondary defect: the alert explained a different failure

The RED alert carried one paragraph, about exit 9, under every exit code. Under
the 07-31 exit 7 it told its reader:

> *"On exit 9 the control suite went RED before the kickstart, which means the
> running pogod was never replaced — the box is in a known-good state and the
> artifact is the problem."*

For exit 7 the artifact is not the problem; **the artifact was never built**.
The advice that followed ("read the log, fix the cause, let the next nightly
carry it") was right by accident, which is worse than being wrong: a reader who
trusts the stated reasoning goes and reads a build log that does not exist.

`remedy_for_exit` now returns the paragraph that is true of the code it got, and
`scripts/pogo-deploy_test.sh` pins the specific confusion — remedy(7) must name
the drain, must not blame the control suite, and must not send anyone to the
build.

The alert also claimed "did not retry" unconditionally. With three fires that
claim has to be computed, and it is only true when the exit is retryable, a fire
remains tonight, and that fire would get a usable budget. A stall with a real
retry behind it now emits a `deploy_nightly_retry_pending` event and stays quiet;
the mail waits for the night's last chance.

## What was deliberately not done

**Option 3 — deploy without a full drain, letting in-flight polecats survive the
restart.** Untouched, but priced, because it was pressed as the direction that
removes the trade-off rather than relocating it. It is — and its usual premise
is wrong in a way that changes where the cost sits.

The premise is that the drain exists because `launchctl kickstart -k` kills
pogod's whole process tree and takes polecats with it. **Polecats are not in
pogod's process tree.** `pty.StartWithSize` forces `Setsid`+`Setctty`
(`internal/agent/agent.go:937-942`, gh #22, guarded by
`TestSpawnProcessGroupIsolation`), so every polecat is already its own session
leader, and `pogo-self-deploy`'s own refusal text says a survivor *"outlives
kickstart -k (they setsid out of the process group) and goes dark forever."*

What actually kills them is an accident. From `internal/agent/witness.go:44-52`:

> *"pogod installs no signal handler, so its death closes the PTY master and the
> setsid'd polecat takes SIGHUP and dies at its default disposition ... That
> accident is load-bearing on the SIGHUP disposition of a third-party harness
> binary we do not control."*

So polecats **already survive structurally**. The blocker is that the registry is
in-memory with **no adopt/reattach path** and *absence never heals* — stated in
`witness.go:26`, `orphan.go:25,310`, `livepolecats.go:14` and
`cmd/pogod/main.go:146,218`, and carried by mg-13a3, mg-61a0 and mg-0b77.

In shape rather than hours:

- **Already built** — the persisted witness. `RecordPolecatWitness`'s own doc
  says it is exported because *"a future adopt/reattach path would use exactly
  this."* Liveness across a restart is solved.
- **Cheap** — rebuilding registry entries from the witness store at startup.
- **The unknown, and where the cost lives** — the PTY master fd dies with the old
  pogod, so an adopted polecat has no channel back: no attach, no nudge, no
  mail-check delivery. Re-parenting means either giving polecats a channel that
  outlives their parent, or accepting live-but-mute workers. That is a design
  question, not an implementation, and it belongs to whoever owns mg-b7d0/mg-42ac.

This is the shape priced, not the effort; the reattach was not prototyped.

**The `1800` default in `pogo-self-deploy` itself.** Left alone. Thirty minutes
is about as long as anybody waits at a terminal, and it is right for the
hand-run case; only the unattended run has the whole night, and only it now asks
for it.

## Bound

Measured on this host, against the running `d31297f` and the checked-out
`b3efaa2`. The budget arithmetic, the retry gate and the per-code remedies are
covered by `scripts/pogo-deploy_test.sh` and each assertion was confirmed to
fail against the pre-fix behaviour. What is **not** exercised end to end here is
a real nightly under the new numbers — the first one is 2026-08-01 03:00, and
the thing to check in the log is the `budget:` line and, if it stalls, whether
the 04:00 fire records `attempt: RETRY`.
