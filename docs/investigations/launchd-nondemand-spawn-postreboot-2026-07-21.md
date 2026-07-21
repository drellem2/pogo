# launchd nondemand spawn works in a post-reboot, never-slept session (2026-07-21)

**Measurement only.** No code changed, nothing fixed, no tier-2 designed. This
document exists because the evidence was **perishable** — it could only be
collected in the window between an unplanned reboot and the machine's next
sleep, and that window is now closed behind us.

Work item: `mg-c09f`. Filed by mayor at pm-pogo's surfacing.
Gating context: `mg-0ffc` (tier-2 / delivery-independent drain) has been blocked
since 2026-07-10 waiting for exactly this reboot.

## Result in one line

**Both** `StartInterval` **and** `StartCalendarInterval` fired normally, mid-session,
~2h22m after boot, in `gui/501` — the session that the standing note describes as wedged.

## Anchoring facts

| Fact | Value |
|---|---|
| Boot (`kern.boottime`) | `Tue Jul 21 00:13:26 2026` BST (unplanned reboot) |
| Probes registered | 2026-07-21 **01:35:29Z** (02:35:29 BST), 2h22m post-boot |
| Probes completed | 2026-07-21 **01:38:30Z** |
| System sleeps since boot | **zero** — `pmset -g log` shows only display/idle assertions, no `Sleep`/`Wake` transitions |
| Window still open at measurement? | **Yes.** Never-slept session throughout. |
| Load average during probes | ~58–76 (heavily loaded box; probes fired on time anyway) |

## Method

Two throwaway LaunchAgents in `gui/501`, each appending a UTC timestamp to a file
under the session scratch dir. Nothing else — no network, no repo writes, no
`~/.pogo/` state.

Critically, **both plists set `RunAtLoad` to `<false/>`**. This is what makes the
measurement mean anything: `RunAtLoad` firing at bootstrap is a *different code
path* from a nondemand spawn, so with it disabled, any output at all is
unambiguously a nondemand spawn in an established session.

- `com.mayor.probe.startinterval.1784597714` — `StartInterval` = 30
- `com.mayor.probe.startcalendar.1784597714` — `StartCalendarInterval` = 02:38 local

Both `launchctl bootstrap` calls returned rc=0. The measurement is the
**presence of the artifact file**, not the job being listed; `launchctl print`
`runs`/`last exit code` were read as corroboration, not as the primary signal.

## Result — `StartInterval`: FIRED

Registered 01:35:29Z. Six fires on an exact 30s cadence:

```
2026-07-21T01:36:00Z startinterval fired
2026-07-21T01:36:30Z startinterval fired
2026-07-21T01:37:00Z startinterval fired
2026-07-21T01:37:30Z startinterval fired
2026-07-21T01:38:00Z startinterval fired
2026-07-21T01:38:30Z startinterval fired
```

`launchctl print` at 01:36:04Z: `state = not running`, `runs = 1`,
`last exit code = 0`, `run interval = 30 seconds` — consistent with a job that
fires, succeeds, and exits.

This is the class the standing note calls dead. Here it is not merely alive but
metronomic.

## Result — `StartCalendarInterval`: FIRED

Target 02:38:00 local. Fired at **01:38:05Z** — 5s past target, ordinary launchd
calendar jitter. `runs = 1`, `last exit code = 0`.

```
2026-07-21T01:38:05Z startcalendar fired
```

Reported separately on purpose: the wedge has previously been observed to be
**class-specific**, with `StartCalendarInterval` working while `StartInterval`
and cron were dead. In this window that asymmetry **did not appear** — both
classes worked. That is a distinct observation from either class alone.

## What this does and does not license

**It does license:** the claim that nondemand spawn in `gui/501` is *not
intrinsically broken on this host*. There is no permanent, install-level,
plist-level, or machine-level defect. A `gui/501` session **can** run
`StartInterval` and `StartCalendarInterval` jobs correctly.

**It does not license:** "the wedge is fixed" or "nondemand spawn works." It was
measured in a **never-slept** session. Re-read `mg-50e0`: the four dead jobs there
all took **SIGTERM (143) at system sleep**. The wedge as originally observed is a
**post-sleep** condition. This measurement is from before this boot's first sleep,
so it is fully consistent with the wedge existing and being sleep-induced.

The honest synthesis: **the evidence now points at sleep, not uptime, as the
trigger — and at reboot as something that clears it.** That was the hypothesis
`mg-0ffc` was blocked on, and it now has direct support rather than inference
from a `RunAtLoad`-at-boot baseline.

## Not tested — do not extrapolate

- **`KeepAlive` respawn-after-death.** Not probed. `mg-50e0`'s asymmetry (two
  `KeepAlive=true` jobs recovered, two did not) is untouched by this.
- **Behaviour after a sleep/wake cycle.** The decisive question. Not measurable
  in this window by construction.
- **`WatchPaths`.** Separate class; `mg-4938` measured it at ~50%. Unchanged here.
- **Whether the wedge re-establishes on the next sleep**, and if so whether it
  hits both classes or reproduces the earlier `StartInterval`-only asymmetry.

## The cheap next measurement

The follow-up is now unblocked and costs minutes: **re-run these same two probes
after the host's next sleep/wake cycle.** Same labels, same method, same
artifact-presence criterion. That single comparison — never-slept vs.
post-wake, same box, same session-class — converts "the wedge is probably
sleep-induced" into a measured fact, and it is the input `mg-0ffc`'s tier-2
question actually needs.

Per the architect's standing `mg-d18b` ruling, **tier-2 remains un-built**; this
document does not change that and does not propose a design.

## Safety

The fleet was live throughout (8 `com.pogo.*` jobs with PIDs, plus on-demand
`com.pogo.recovery`). No `com.pogo.*` job was booted out, kickstarted, stopped,
or otherwise touched; no `pkill` was used. Both probes were booted out (rc=0) and
their plists deleted. `launchctl list | grep com.pogo` before and after the probe
is **byte-identical**, same PIDs (3316, 3317, 3324, 3325, 3330, 3335, 3341, 3347):

```
-     0  com.pogo.recovery
3316  0  com.pogo.pa-heyfeed
3317  0  com.pogo.notify
3324  0  com.pogo.watchdog
3325  0  com.pogo.gh-issues
3330  0  com.pogo.pa-calendar
3335  0  com.pogo.daemon
3341  0  com.pogo.sleepwake
3347  0  com.pogo.bridget
```
