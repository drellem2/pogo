# Pogo sleep-resilience design

**Status:** **shipped / historical — rationale, not a plan** · **Owner:** architect · **Tracks:** mg-c4a3
**Date:** 2026-05-02 (as filed; header rewritten 2026-07-30, mg-9557)

> **Read the body below as archeology.** Every section except §3 has shipped. The
> proposal text is preserved exactly as architect filed it on 2026-05-02 — future
> tense, "tickets to file", and all. Do not read that tense as a description of
> today's code, and do not implement any of it again. The table immediately below
> names what implements each section; when the two disagree, the code wins.
>
> **On mg-a374's deletion — settled, do not re-open.** mg-a374 (the 2026-05-04
> `docs/` cleanup pass) deleted this file, and **that was the correct call about the
> proposal.** A proposal for a mechanism that has since shipped is cruft: the next
> reader either builds it a second time or concludes the rules are unbuilt. What is
> being kept here is a *different artifact* that happened to be stapled to the same
> file — the **rationale**: why the core is a clock-jump comparison rather than an
> OS API, why replay policy is per-cadence, why `pogo schedule` is canonical over
> an in-harness cron, and (§3) why a plausible cause was declined for want of
> evidence. The proposal was rightly deleted; the rationale is why this file exists.

## What implements each section

| Section | Status | What implements it |
|---|---|---|
| §1 — monotonic-vs-wall heartbeat, `system_wake` | Shipped | `internal/heartbeat/` (mg-283e), constructed and ticked from `cmd/pogod/main.go` (`heartbeat.New()`, ~line 1514); emits `system_wake` to `~/.pogo/events.log`. Interval and jump threshold are configurable (`cfg.Heartbeat`). |
| §2 — per-cron replay policy, at-most-once default | Shipped | `ReplayPolicy` (`once` \| `count` \| `skip`) in `internal/scheduler/scheduler.go`, defaulting to `ReplayOnce`. The per-cadence reaction table lives in `internal/agent/prompts/templates/polecat.md` ("Reacting to scheduler fires"); the user-facing summary is [../CONFIGURATION.md](../CONFIGURATION.md) § Scheduler. |
| §3 — the `auto_start` / pogod-boot question | **No code, by design** — see below | Filed as mg-60ca, which concluded **(b): pm-pogo was running but its Claude session was wedged.** `auto_start` was never redesigned. |
| §4 — pogo-native scheduling primitive | Shipped, **renamed** | `internal/scheduler/` plus the CLI (mg-bcfa). It is spelled **`pogo schedule`**, not the `pogod schedule` written below. Crew and polecat templates were migrated off the harness's in-process `CronCreate` in mg-2f79; `CronCreate` remains valid only for ephemeral in-session reminders, as §4 recommended. |
| §5 — platform sleep/wake shims | Shipped | `internal/platform/sleep/`: `sleep_darwin.go` (mg-baf6), `sleep_linux.go` (mg-ef30), `sleep_other.go` (no-op). Still a latency optimization only — the heartbeat is correct without them. |

## Why this file is kept, section by section

§3 is the part worth the filing. **It is not design rationale, it is a calibration
record.** It listed three candidate causes for the 55-minute May 2 gap, named
**(b) a wedged Claude session** explicitly, and then *refused to redesign
`auto_start`* on evidence that did not support it — deferring to an investigation
ticket instead. mg-60ca later concluded (b). We ask other agents to decline to act
on unproven causes routinely and hold almost no written evidence that the
discipline actually pays; this is that evidence.

Of the rest, **§2 and §4 are the genuinely undocumented arguments.** `docs/`
records the `--replay` *behaviour* but not the per-cadence *policy* and why it
differs by cadence; `CronCreate` appears in two files incidentally, with neither
arguing why a daemon-side scheduler is canonical. **§1 is the weakest case** — the
`system_wake` / monotonic-clock reasoning is partly covered elsewhere, including
[stall-watch-design.md](stall-watch-design.md). Harvesting §1/§2/§4 rationale
*into* the mechanism docs is welcome; this file remains §3's home either way.

## Context

`pm-pogo` went silent for ~58h (2026-04-29 23:54Z → 2026-05-02 09:48Z). Three crons (`0 9 * * *`, `0 17 * * *`, `*/10 * * * *`) all stopped firing through the gap; on wake, no missed-fire replay. The same shape applies to any pogo agent during host sleep — this is not pm-pogo-specific.

Daniel's constraint: the fix must be **OS-agnostic at the core**, with platform glue (IOKit / systemd-logind / etc.) as adapter shims.

## Diagnosis (in code terms)

What we have today (per the survey):

- **Pogod itself has no scheduler.** The daemon main loop (`cmd/pogod/main.go:350-624`) is purely reactive HTTP. The only periodic work inside the daemon is the refinery's `time.NewTicker(PollInterval)` (`internal/refinery/refinery.go:278-293`) on the **wall clock** — no clock-jump guard.
- **Agent scheduling lives inside Claude.** Crew prompts (`internal/agent/prompts/pm/pm-template.md`, `prompts/templates/polecat.md`) tell agents to use Claude's in-process `CronCreate`. That runtime stops firing while the agent process is paused (host sleep) and recomputes the next fire from "now" on wake — missed firings are silently dropped.
- **`auto_start` is wired.** `internal/agent/autostart.go:46-133` scans prompt frontmatter at pogod boot and spawns crew agents with `auto_start = true`. Crash-respawn lives at `cmd/pogod/main.go:401-436`.
- **Launchd/systemd restart pogod, not the system clock.** Plist uses `KeepAlive=true` + `ProcessType=Interactive` (`internal/service/service.go:21-99`); systemd uses `Restart=on-failure`. These restart pogod on crash, not on host wake. On macOS, host sleep typically does *not* exit pogod, so no restart happens.
- **No sleep/wake detection or clock-jump heartbeat anywhere.** Searches for IOKit, dbus, monotonic clocks, "wake/suspend" come back empty.
- **Platform abstraction is install-only.** A `runtime.GOOS` switch in `internal/service/service.go:163` picks launchd vs systemd. There is no `internal/platform/` layer for runtime concerns.

So the gap is exactly what the symptoms describe: the agent's Claude process was paused during host sleep, its in-process crons did not fire, and nothing replayed them on wake.

## Proposal

### 1. Detection: monotonic-vs-wall heartbeat in pogod (portable baseline)

Add a small heartbeat goroutine to pogod that ticks every `T_hb = 30s` (configurable). On each tick, compare elapsed monotonic time vs elapsed wall time since the last tick. If `wall - mono > T_jump` (default `2 * T_hb` = 60s), declare a `system_wake` event with `gap_duration = wall - mono` and emit it to the existing event log (`~/.pogo/events.log`, `internal/events/`).

This catches host sleep, container pause, VM migration, NTP step, and laptop-lid-close uniformly — all surface as a wall-clock jump while monotonic time keeps quasi-paused. Implementation is ~30 lines of Go using `time.Now()` and `runtime.nanotime()` (or a `monotonic` library); zero cgo, zero platform code.

**This is the OS-agnostic core.** Platform-specific listeners (§5) are an *optimization* that can fire the wake event slightly earlier and with a known cause; they are not load-bearing.

### 2. Replay policy: per-cron, default at-most-once catch-up

When pogod emits `system_wake`, it nudges affected agents with a structured wake message: `"system_wake gap=58h12m"`. The agent's prompt instructs it on what to do — this keeps policy in the agent layer, where it belongs.

Default policy (codified in crew prompt templates):

| Cron type | Policy | Rationale |
|---|---|---|
| PM sweep / mail-check | at-most-once catch-up | one sweep covers any gap |
| Refinery poll | skip / resume | next tick is fine; no per-firing semantics |
| Long-cadence reports | at-most-once | dedup beats double-post |
| Counted batch jobs | count / catch-up | each firing has a distinct unit of work |

We do **not** need a generic replay engine. Each agent reads `system_wake` and decides; the prompt describes the right call. This is consistent with "scheduling is agent-owned."

### 3. auto_start / pogod-boot question

`auto_start` *is* already wired (`autostart.go:46`, scanned at boot). The May 2 morning anomaly — pogod up 08:53Z, pm-pogo up 09:48Z (55-min gap) — is **not** an auto_start design issue; it's either (a) pogod did not actually restart at 08:53 (just unblocked), (b) pm-pogo was already running but its Claude session was wedged, or (c) AutoStartAgents has a bug not yet observed.

**Action:** file a separate investigation ticket (suggested below) — out of scope for this design. Do not redesign auto_start.

### 4. Pogo-native scheduling primitive (becomes canonical)

The deeper fix: agents that need sleep-resilient scheduling should not put their crons inside Claude. They should ask **pogod** to fire them.

Add a small daemon-side scheduler:

- `pogod schedule <agent> --cron <expr> [--id <slug>] [--replay <once|count|skip>]` — register a recurring fire
- `pogod schedule <agent> --once --in <duration> [--id <slug>]` — one-shot wakeup
- `pogod schedule list <agent>` / `pogod schedule rm <id>` — manage

State lives in `~/.pogo/schedules.json` (filesystem-as-coordination, per `ARCHITECTURE.md`). Pogod's heartbeat goroutine ticks the scheduler: on each tick, fire any cron whose next-fire is now in the past, then reschedule. Because the tick uses monotonic time and the schedule stores absolute wall-clock fire times, `system_wake` and the scheduler are the same loop — clock jumps are handled for free.

A fired schedule sends a `nudge` (or `mail`, agent-configurable) with the schedule id and the original fire time. The agent receives it whether or not its Claude session was alive at the original time.

**Recommendation:** make `pogod schedule` the canonical mechanism for crew agents. Update `pm-template.md` and `polecat.md` to use it instead of `CronCreate` for the mail/sweep loops. Claude `CronCreate` remains available for ephemeral, in-session reminders ("nudge me in 5 min while I'm working on this").

This is where Daniel's framing of "ScheduleWakeup as canonical" lands in pogo: not Claude's `ScheduleWakeup` (which has the same in-process limitation as `CronCreate`), but a pogo-native equivalent.

### 5. Platform shims (optional optimization)

Add `internal/platform/sleep/`:

- `sleep_darwin.go` — IOKit `IOPMSleepWakeMessageType` registration (cgo) or `pmset -g log` poll fallback.
- `sleep_linux.go` — dbus `org.freedesktop.login1.PrepareForSleep` signal.
- `sleep_other.go` — no-op.

Each shim, on a wake event, calls into the heartbeat goroutine to *short-circuit* the next tick (so we react in <1s instead of waiting up to `T_hb`). The shim is a strict performance optimization; the system is correct without it.

Containers / EC2 / fly.io / generic Linux servers without logind: clock-jump heartbeat alone suffices.

## Out of scope

- pogo-darwin Mac-app concerns (separate phase).
- Crash-recovery / restart-loop (mg-f5fc / mg-6749 family).
- Refinery's per-poll catch-up logic (its own ticket once §4 lands).

## Implementation tickets to file

1. **mg: pogod heartbeat goroutine + monotonic-vs-wall clock-jump detection** — emits `system_wake` event. ~1 day. (No platform code; just `internal/heartbeat/` + event-log wiring.)
2. **mg: pogod-native scheduler (`pogod schedule`)** — `~/.pogo/schedules.json`, register/list/remove, tick-and-fire from the heartbeat loop, nudge/mail delivery. ~3-5 days.
3. **mg: migrate pm-template + polecat templates from `CronCreate` to `pogod schedule`** — prompt edits + a `system_wake` reaction stanza. ~0.5 day.
4. **mg: investigate May 2 morning auto_start gap (08:53Z → 09:48Z)** — read events.log around that window; determine if AutoStartAgents fired and pm-pogo simply took 55min to come up, or if it was re-spawned later via different path. Likely small fix or no-op. ~0.5 day.
5. **mg (optional, per-platform): macOS IOKit sleep/wake shim under `internal/platform/sleep/`.** ~1 day.
6. **mg (optional, per-platform): Linux logind sleep/wake shim.** ~1 day.

Tickets 1-4 give us full sleep-resilience on every platform pogo runs on. 5 and 6 are latency optimizations.

## Why this shape

- **Core stays portable.** Clock-jump detection is two `time.Now()` reads and an arithmetic. Every OS produces clock jumps on sleep; we lean on that universal signal.
- **Policy stays in prompts.** Replay logic lives where the rest of agent behavior lives, not in a generic "replay engine" inside pogod.
- **Filesystem stays the coordination layer.** `schedules.json` matches the existing `~/.pogo/events.log`, prompt-files-as-roster, mail-as-mailbox style. No new RPC, no new daemon state machine.
- **No mandatory cgo, no mandatory dbus.** Platform glue is opt-in; pogo-on-fly.io still works.
