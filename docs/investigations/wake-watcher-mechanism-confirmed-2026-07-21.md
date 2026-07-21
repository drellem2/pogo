# The wake-watcher leak: parent death CONFIRMED, and the histogram was never evidence against it

**Date:** 2026-07-21
**Work item:** mg-c3a6 (confirmation pass over mg-55de)
**Verdict:** mg-55de's mechanism is **correct**. The reaper is the right fix and a
complete one for the observed mechanism. **Close the question.**
**Xref:** mg-55de (`b928637`, the reaper), `pogod-shutdown-stops-nothing-2026-07-17.md`
(the same exit-path inventory, one consumer over)

## Why this was re-opened

mg-55de landed a reaper for `log stream` watchers "stranded by pogod deaths."
Two observations appeared to contradict that:

1. **pogod pid 3335 has not died once since boot** (started 00:15:27, one
   continuous process) — yet the 242 orphans cleared by hand spanned ~8 hours.
2. **The age histogram showed repeated cohorts**, not one: `01h:24 02h:14
   03h:15 05h:16 06h:28 07h:114 08h:32`. A single stranding event produces one
   cohort. Several cohorts read as several *spawns*.

If the source were repeated spawns rather than stranding, the reaper would be a
guard placed downstream of where the damage is generated — the box refills after
every sweep, and the fix reads as working because someone keeps sweeping.

## The dichotomy is false

"Stranded by parent death" and "repeated spawns" are **the same event observed at
its two ends**, not competing mechanisms.

The production spawn path (`cmd/pogod/main.go:1532` — the **only** call site of
`sleep.Watch` in non-test code) spawns **exactly one** watcher per pogod boot.
That same watcher is stranded on that same pogod's death. So one boot/death cycle
contributes exactly one orphan, and *N* cycles contribute *N* orphans spread
across whenever those cycles happened.

**A multi-cohort histogram is therefore the signature of parent-death, not
evidence against it** — provided the deaths are numerous. They are.

## Resolving the "pogod never died" objection

The objection is sound about the process it names and irrelevant to the leak.
"pogod death" here means **any pogod process exiting**, not specifically the
long-lived daemon. The deaths that produced the 242 were **short-lived pogod
instances**, each booted and killed by a test or a deploy:

- `scripts/pogo-self-deploy_live_test.sh` — boots a real sandbox pogod (2 boot
  sites, ~7 references), SIGTERMs it
- `scripts/pogo-self-deploy_sigint_test.sh`, `scripts/test-e2e.sh`,
  `scripts/upgrade-smoke.sh` — each boots and kills its own
- every `pogo server stop` / launchd restart / redeploy cycle

Each such instance reaches line 1532, spawns its watcher, and strands it on exit.
A busy night of refinery merges — every merge runs the gate — plausibly turns over
~30 pogods/hour, and a heavy merge hour explains the 114-in-one-hour burst.

Meanwhile the live daemon's own watcher behaved exactly as a non-leaking path
should: **pid 3396, PPID 3335, one of them, alive since boot.** It was still the
sole survivor at every observation in this investigation. The production path is
not leaking; the *population of dead pogods* is.

## Confirmed by reproduction, not inspection

Run against a sandboxed pogod built from `b928637` (`HOME`/`XDG_CONFIG_HOME`/
`POGO_HOME` redirected, `POGO_AGENT_AUTOSTART=false`, spare port). Every kill below
was by explicit verified PID; the machine was returned to its baseline.

```
T0 baseline        3396  3335   log stream --predicate ... "Wake reason:"   <- live pogod's, spared throughout

T1 pogod #1 up (13897)
                   3396  3335
                  13904 13897   <- its watcher, correctly parented

T2 SIGTERM 13897 (pogod dead)
                   3396  3335
                  13904     1   <- STRANDED. reparented to launchd, still streaming
```

**That is the mechanism, observed directly.** The watcher outlives its parent and
reparents to PID 1 — because `exec.CommandContext`'s kill depends on
`defer hbCancel()` (`main.go:1511-1512`), and pogod installs no SIGTERM handler,
so that defer runs on **no exit path whatsoever** — SIGTERM, `log.Fatal`, SIGKILL,
panic, host crash. The exit-path inventory at `main.go:1091-1096` already says so
in prose; this is the same fact reaching a second victim.

Then, the fix under test:

```
T3 pogod #2 up (14507)
                   3396  3335   <- live pogod's watcher SPARED
                  14512 14507   <- new watcher
   pogod2.log: "reaped 1 orphaned `log stream` watcher(s) stranded by an earlier pogod"
```

The reaper killed the orphan (13904), spared the live daemon's watcher (3396),
and the population converged to one. Cleanup: 14512 killed by PID; final state
back to `3396 3335` alone.

## An unprovoked confirmation, from an ordinary gate run

The reproduction above provokes an orphan deliberately, which is a fair objection
to it: a contrived kill proves the kill strands, not that anything on this machine
does the killing. So this is the stronger observation, and it was not sought.

Running `./build.sh` on this branch — the ordinary pre-commit gate, no experiment
attached — left behind:

```
39605     1  Tue Jul 21 10:04:06 2026  log stream --predicate ... "Wake reason:"
```

**One orphan, at PPID 1, produced by the test suite doing nothing but its job.**
That is the leak generating itself, live, from the very activity the histogram
attributed it to — and it is the observation the cleared population could no
longer supply. It was exactly one, not several, which is also the bounded steady
state the reaper is supposed to hold. Killed by verified PID; baseline restored.

## Why the reaper is not a downstream guard

The concern that motivated this ticket was placement — a sweeper downstream of a
source that keeps producing. **It is not downstream.** `reapOrphanedWatchers()` is
called *inside* `Watch()`, before the spawn (`sleep_darwin.go:57-63`). Every pogod
that boots converges the population to one on its way in, and the boot rate is the
*same rate* as the strand rate, since they are the same event. The source cannot
outrun the guard, because the source **is** the guard's trigger.

Convergence holds under concurrency too: a wave of *N* parallel pogods dying
together leaves *N* orphans, and the next single boot clears all *N* in one pass
(the reaper loops over every matching `ps` row, not just the first).

The 242 accumulated because **no version of pogod had ever reaped** — not because
a reaper was failing to keep up.

## Was a better upstream fix available?

No, and the alternatives are worse:

- **A SIGTERM handler** would cover the routine stop, but not SIGKILL, panic, or
  host crash — the paths that actually strand. It is also a behaviour change with
  a deadline problem already documented at `main.go:1110-1117`.
- **PDEATHSIG** — darwin does not have it. A child cannot ask the kernel to kill
  it with its parent.
- **SIGPIPE self-termination** is what saves pogod's *other* children, and it
  fails here for a specific reason: `wakePredicate` matches almost nothing, so
  this child never writes, never takes SIGPIPE, and streams forever.

Reaping at startup is the remedy darwin actually offers.

## The retracted attribution, stated plainly

The hypothesis that `sleep_darwin_test.go`'s `Watch(context.Background(), nil)`
was the emitter is **wrong**, and it is worth being precise about *why*, because
the obvious reason is not the interesting one.

- **The shallow reason:** `Watch` rejects a nil hook at `sleep_darwin.go:50-52`,
  before `LookPath` and before any spawn. That call site cannot leak. (Confirmed
  independently by the architect, and by a before/after count in mg-55de.)
- **The reason that matters:** `context.Background()` was never the
  differentiator. **The production path is equally uncancellable in practice** —
  `hbCtx` has a `cancel` that no exit path ever calls. A test passing
  `context.Background()` and production passing `hbCtx` produce the *same*
  behaviour on pogod death. The defect was never "a test used a bad context";
  it was that pogod's exit paths cancel nothing at all.

This attribution was offered as a hypothesis, written before the implementation
was read, and explicitly flagged as such by its author on both occasions it
appeared. It is recorded here as refuted so it does not harden by repetition a
third time.

## Two non-findings, recorded so they are not cited

- **The 09:40 clean reading has no discriminating power.** It was taken after
  every orphan had been killed by hand, and if the trigger is test activity in a
  window where no tests ran, absence of the effect says nothing. Its author
  flagged it as powerless; that flag stands.
- **The current count of exactly one is likewise not evidence.** That population
  was destroyed by hand. Absence now is a human's doing, not the system's. This
  is why the confirmation above provokes a *fresh* orphan in a sandbox rather
  than reasoning from the cleared population.

## Status of the fix on this machine

`b928637` is a **pogod-side** change; the installed pogod is `249f349`
(2026-07-18). **The reaper is inert until a redeploy** — a Daniel decision, not
taken here. Every result above is from **source**, built and run in a sandbox.
The only claims from the **running system** are the observations of the live
daemon's own watcher (pid 3396, stable, singular).

Until the bounce, the standing check remains:

```
ps -Ao pid,ppid,command | grep -F 'log stream' | grep -F 'Wake reason' | grep -v grep
# expect exactly one, at pogod's PID. Any PPID=1 entry means the leak is live again.
```

## Conclusion

**Parent death is the sole mechanism.** No second source exists: there is one
production call site, it spawns once per boot, and the multi-cohort age
distribution is explained entirely by repeated boot/death cycles of short-lived
test and deploy pogods. A reaper is the correct and complete fix, it is placed at
the spawn point rather than downstream of it, and it converges to one.

The question is closed. Re-opening it needs a *new* observation — an orphan whose
existence cannot be accounted for by a pogod that died.
