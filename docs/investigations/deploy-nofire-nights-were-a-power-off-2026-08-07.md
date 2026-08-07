# The four silent deploy nights were a POWER-OFF, not the launchd wedge (2026-08-07)

**Measurement only.** No code changed here. This document exists because the
evidence is **perishable**: `pmset -g log` reads an ASL store that already only
reaches back to 2026-07-31, and `wtmp` rotates. Both windows close on their own.

Work item: `mg-2416`, whose body carried the wedge as an explicit HYPOTHESIS and
asked for it to be verified. It is not supported. The competing explanation
already recorded in `internal/staleness/deploy.go` (mg-dd49) — that the box was
powered off — is the one the evidence backs.

## Result in one line

The machine was **off from 2026-07-31 21:18 BST to 2026-08-04 11:40 BST**, which
contains every one of the four missed 03:00–05:00 deploy windows.

## The claim being tested

`mg-2416` proposed:

> The likely mechanism (HYPOTHESIS, not verified) is the nondemand-spawn wedge
> already documented in `docs/investigations/launchd-nondemand-spawn-postreboot-
> 2026-07-21.md`: a `StartCalendarInterval` fire IS a nondemand spawn and gets
> pended after sleep. `com.pogo.deploy` is exactly such a job.

That mechanism requires the machine to have been **running and asleep**. It was
neither.

## Anchoring facts

| Fact | Value | Source |
|---|---|---|
| Last power-management event before the gap | `2026-07-31 21:18:35 +0100` | `pmset -g log` |
| First event after the gap | `2026-08-04 11:40:36 +0100 Start — powerd process is started` | `pmset -g log` |
| Dates present in the pmset store | 07-31, 08-04, 08-05, 08-06, 08-07 | `pmset -g log \| awk '{print $1}' \| sort -u` |
| Dates absent | **08-01, 08-02, 08-03** — and 08-04 before 11:40 | same |
| Boots on record | `Aug 4 11:57`, `Aug 4 11:40`, `Jul 21 00:13`, `Jan 4 08:29` | `last reboot` |
| Boots between Jul 21 and Aug 4 | **none** | same |

## Why this is a power-off and not a sleep

`powerd process is started` is a **boot** line, not a wake line. A sleep/wake
cycle keeps `powerd` running and logs `Wake`/`DarkWake` transitions; it does not
restart the process. So the 08-04 11:40 entry is the machine coming up, and the
absence of any entry at all for 08-01..08-03 is the machine not existing to log
one — a sleeping Mac still logs assertion churn and wake transitions, as the
07-31 and 08-05..08-07 days in the same store show.

It also reconciles with `last reboot` rather than contradicting it. There is no
boot recorded between Jul 21 00:13 and Aug 4 11:40 because a machine that is off
records nothing; the Aug 4 11:40 boot **is** the return from the off period.

## It matches the deploy log exactly

Every `pogo-deploy: start` line, against the power state:

```
2026-07-29T02:00:04Z  fired     machine up (since Jul 21 00:13)
2026-07-30T02:00:02Z  fired     machine up
2026-07-31T02:00:00Z  fired     machine up
                                --- powered off 2026-07-31 21:18 ---
2026-08-01            NO FIRE   machine off
2026-08-02            NO FIRE   machine off
2026-08-03            NO FIRE   machine off
2026-08-04            NO FIRE   machine off (booted 11:40, after the window)
                                --- booted 2026-08-04 11:40 ---
2026-08-05T02:00:03Z  fired     machine up
2026-08-06T02:00:04Z  fired     machine up
2026-08-07T02:00:05Z  fired     machine up
```

There is no residue for the wedge to explain. Every silent night is off, every
firing night is up, with no exceptions in either direction.

## What this does and does not license

**Licensed.** For THIS incident, the cause of the four silent nights is that the
host was powered off, and `launchd` replays a missed `StartCalendarInterval` on
wake but not across a power cycle. Nothing needs to be un-wedged, and a
`launchctl kickstart` would have fixed nothing because there was nothing running
to kickstart.

**Not licensed.** This says nothing about whether the nondemand-spawn wedge is
real — it is separately documented (mg-50e0, and the 2026-07-21 post-reboot
measurement) and is untouched by this. It says only that it was not the
mechanism here.

**Why the detector does not depend on which one it was.** The did-not-run
detector added under mg-2416 reports that the job DID NOT START and names the
nights. It deliberately does not diagnose why, and its notice names both causes
without asserting either. That is not a hedge: powered off, wedged, LaunchAgent
unloaded and plist removed are indistinguishable from the log, they all produce
the identical observable, and they all have the same consequence — the fleet runs
yesterday's code. A detector that had been built around the wedge hypothesis
would have been built around the wrong cause and would still have needed to fire
on this one.

**A note on `runs`.** Read at 2026-08-07T18:30Z, after seven fires had
demonstrably run and written to the log:

```
gui/501/com.pogo.deploy = { runs = 0, last exit code = (never exited) }
```

`runs` counts spawns on the CURRENT bootstrap and re-installing the plist resets
it — mg-b201 re-installed it that morning. It reads 0 for a job that just ran and
0 for a job that never has, so it cannot corroborate or refute anything here.
That is why the detector reads the log.

## Correction this makes to the record

`mg-2416`'s body asserts that the four nights are "the nondemand-spawn wedge".
They are not. `internal/staleness/deploy.go`'s package comment already said
"the box was powered off through each 03:00 window" — written for mg-dd49 —
and that statement is now measured rather than assumed.
