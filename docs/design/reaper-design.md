# Tier-1 heartbeat reaper (mg-d18b)

Status: implemented. Package `internal/reaper`, wired in `cmd/pogod`.
Architect ruling: 2026-07-10 (build tier 1; tier 2 stays unbuilt).

## What it is

A goroutine inside pogod that, for a declared list of launchd jobs, reads each
job's **heartbeat** and `launchctl kickstart`s anything stale. It is not a
LaunchAgent — anything that relies on being spawned by launchd never starts on
this host (the nondemand-spawn wedge, mg-50e0). It lives inside the
already-running pogod and only *kickstarts*, which is a demand spawn and works.

## Why it exists — frame this correctly

The reaper is **not a workaround for the launchd wedge.** It is the mechanism
that implements a specific, correct definition of liveness.

`KeepAlive` — even on a perfectly healthy launchd — restarts a job only when it
**EXITS**. It can never detect a job that is *alive and not working*: a wedged
run loop, a closed socket, a timer that never rearmed. The process persists, so
launchd sees a healthy job forever. That is not a property of this broken host;
it is a property of process-existence supervision on **every** host.

So a heartbeat-driven reaper is warranted *regardless of* the wedge. If a reboot
ever unwedges launchd, the reaper simply stops firing for exits (harmless, zero
maintenance) and goes on catching the failure class launchd structurally cannot
see. The wedge does not make it redundant; the wedge only makes it *urgent*.

This closes the class that actually bit us: watchdog dead 4.8h, gh-issues dead
4.8h, pa's mail feed dark 3.1h, com.pogo.recovery inert for six weeks — every
one a process that existed while doing no work.

## The liveness definition

**Liveness is heartbeat freshness. Never process existence. Never PID liveness.**

Each watched job touches a state file at the end of every *successful* loop
iteration (`seen.json`, `bridget.seen`, or a dedicated
`~/.pogo/health/<job>.heartbeat`). The reaper keys on that file's **mtime**
against the job's known **period**. A job at `state = running` whose heartbeat
is older than its period is **dead**, and the reaper acts.

It must **not** infer liveness from a log line. A poller logs only when it
delivers, so a quiet mailbox is indistinguishable from a dead poller — that
mistake cost a 5.4h estimate for a 3.1h outage. The state-file mtime ticks every
cycle regardless of whether the cycle had anything to deliver, which is exactly
what makes it a liveness signal and a log line not.

## Boundary: the reaper detects a stopped heartbeat, not stale code

Heartbeat freshness proves a process is **doing work** — it does **not** prove
the process is running the **current** code. A job whose file was patched but
whose process was never restarted keeps ticking its heartbeat perfectly: a bash
poller parses its top-level `while` loop once at start and never re-reads the
file, so the *old* loop keeps running and freshening the state file. The reaper
sees a fresh heartbeat and — correctly — leaves it alone. The process is alive;
it is simply executing outdated code.

So **the reaper and mg-be0c's reconcile step are complementary, not
overlapping** — neither covers the other's failure:

| Mechanism | Trigger | Catches |
|-----------|---------|---------|
| **d18b (this reaper)** | heartbeat gone **stale** | dead / wedged processes — a job that stopped doing work |
| **mg-be0c (reconcile)** | job's **file changed** | alive-but-running-old-code — a job still executing a pre-patch loop |

The reaper **cannot** detect stale code, and must not be designed to try — a
fresh heartbeat carries no information about which revision produced it. Do not
read this reaper as making be0c redundant: a running process executing outdated
code is invisible to heartbeat supervision by construction, and closing that gap
is be0c's job, not this one's.

## Hard conditions (all from the architect ruling)

1. **Heartbeat freshness only** — implemented in `Reaper.checkJob`: `fresh :=
   err == nil && now.Sub(mtime) <= j.Period`. A missing file counts as stale
   (job likely never started) and is reported distinctly ("heartbeat missing").

2. **Loud.** Every kickstart logs job, staleness, attempt N/max, and resulting
   pid. Every recovery logs (`RECOVERED — heartbeat fresh … after N
   kickstart(s)`). Every give-up logs. A silent supervisor eventually becomes
   the thing that conceals the failure (com.pogo.recovery's six-week inertness;
   pogod's mail-check GC).

3. **Bounded, backed off, gives up loudly.** `max_kickstarts` (default 3) caps
   consecutive kickstarts. On exhaustion the reaper STOPS and mails both `mayor`
   and `human` — **once** — then stays quiet. This is the **mg-1679 defense**: a
   job that FATALs on every start never freshens its heartbeat, so launchctl
   reports a fresh pid each time while the job does no work; without the cap the
   reaper would kickstart it forever, a *new* self-concealing failure.
   `"GIVING UP on <label> — kickstarted 3 times, heartbeat still stale"` is the
   single most important line the reaper emits. Backoff: the job's `period`
   doubles as a settle window — a just-kickstarted job is not re-judged until it
   has had `period` to write a fresh beat, so the reaper never tight-loops.

4. **Positive control, both directions.** See Acceptance below.

5. **Verify the RUNNING pogod contains the reaper.** See Acceptance below — a
   patched file/branch is not a patched process (mg-be0c, mg-0a89).

6. **Never `pkill -f`.** The reaper never kills by pattern. It issues only
   `launchctl kickstart -k gui/$UID/<label>` (`service.KickstartJob`), which
   targets exactly one label and can never take out a bystander (mg-8c9c).

7. **No self-dispatch assumption.** The reaper never writes a plist expecting
   the platform to pick it up (mg-be0c). Kickstart is the only mechanism.

## The obligation tier 1 carries: DETECTION of pogod's own death

Tier 2 — *who reaps pogod* — is deliberately **unbuilt**. A child agent cannot
reap its parent, launchd will not, and whether a reboot unwedges `gui/501` is an
open experiment. Building tier 2 now means building it blind (and, per mg-be0c,
with no reconcile step to deploy it and no guarantee it starts) — a false green
about false greens.

But the reaper can restart every `com.pogo.*` job **except pogod itself**, and
an unnoticed pogod death is indistinguishable from a quiet afternoon — the same
shape that left the mail feed dark for 3.1h. So the one obligation tier 1 does
carry is **detection, not recovery**: pogod publishes its **own** heartbeat to
`~/.pogo/health/pogod.heartbeat` on every heartbeat tick (in `hb.OnTick`,
independent of `[reaper]` enablement, via `reaper.WriteHeartbeat`). An external,
human-held check — the digest, or bridget once threading is on — reads that
file's freshness to detect a dead pogod. **That one check is a known, named,
accepted single point of failure until the reboot settles tier 2.**

This makes the machine *self-restarting for everything except the restarter*,
and *self-reporting* about that one exception. It is not self-healing, and must
not be described that way.

## Configuration

`[reaper]` in `config.toml` — see [docs/CONFIGURATION.md](../CONFIGURATION.md#heartbeat-reaper-tier-1).
Jobs are a flat single-line encoding (`label|path|period`) because pogo's config
is hand-parsed flat TOML with no table-array support; malformed entries are
dropped with a log line rather than failing the whole load.

## Code map

- `internal/reaper/reaper.go` — the launchd-free, unit-tested core. `Reaper.Check`
  is the deterministic pass (injected clock/stat/kickstart/mail); `Reaper.Run`
  drives it on a ticker. `WriteHeartbeat` publishes pogod's own heartbeat.
- `internal/service/service.go` — `KickstartJob(label)` is the single launchctl
  call (`kickstart -k gui/$UID/<label>`, then read pid from `launchctl list`).
- `internal/config/config.go` — `[reaper]` section, `ReaperConfig`/`ReaperJob`.
- `cmd/pogod/reaper_start.go` — wiring: supplies `service.KickstartJob` and
  `client.SendMGMail` as the two real-world seams.
- `cmd/pogod/main.go` — `startReaper` after `startGitGC`; pogod's own heartbeat
  write in `hb.OnTick`.

## Acceptance — behavioral, not artifactual

Do **not** close this on "the reaper merged." Per mg-0a89 / mg-be0c that proves
nothing. The unit tests in `internal/reaper/reaper_test.go` already exercise the
liveness logic deterministically (fires-then-recovers, never-fires-on-healthy,
mg-1679 give-up, settle-window backoff, missing heartbeat, kickstart-error
escalation). The remaining acceptance is a **live** run against the RUNNING
pogod, performed by whoever deploys (a polecat must not disrupt the live fleet):

1. **Prove it FIRES.** With a job configured under `[reaper]`, kill that job's
   process **BY PID** (never `pkill -f` — mg-8c9c: `kill <pid>`). Watch
   `pogod.log`: within one sweep the reaper logs `STALE … kickstarted … new pid
   N`, `launchctl list <label>` shows the new pid, and the job's heartbeat file
   mtime resumes ticking.
2. **Prove it does NOT fire against a healthy job.** Confirm no kickstart line
   for a job whose heartbeat stays fresh across the same window.
3. **Verify the RUNNING pogod contains the reaper.** Compare pogod's process
   start time (`ps -o lstart= -p <pogod-pid>`) against the built binary's mtime.
   If the process predates the binary, `launchctl kickstart -k
   gui/$UID/com.pogo.daemon`, then re-verify: the restarted pogod's start time
   must now postdate the binary, and `pogod.log` must show the reaper's startup
   line (`reaper: watching N job(s) …`). Merged is not running; a patched file
   is not a patched process (pa: two pollers ran pre-edit code for 41 min after
   the fix landed).

## Cross-references

mg-50e0 (the wedge), mg-6e82 (recovery net cannot be restored under it), mg-8c9c
(never pkill -f), mg-be0c (no reconcile step; running≠merged), mg-0a89
(acceptance must be behavioral), mg-1679 (bridget-supervise FATAL respawn — the
shape this reaper is designed against).
