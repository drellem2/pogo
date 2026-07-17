# The redeploy drain: what it actually does

**Date:** 2026-07-17 · **Work item:** mg-46a4 · **Xref:** mg-f206 (nightly
unattended redeploy — arming decision), mg-61a0
([registry-absent-while-alive](registry-absent-while-alive-2026-07-17.md)),
mg-ea3e (`3407cc7`, name-based mail-check slack), mg-cae1 (`1b1f12d`, drain
mode), mg-6afa, mg-065e (`d02e087`)

Point-in-time record. Read against `05eef74`. Fact-finding only — **no fix
shipped, nothing changed**. Defects noted here are for the mayor to file.

> **AMENDMENT 2026-07-17 (mg-65b2) — finding #1 is TRUE ONLY WHEN THE READOUT IS
> READABLE, and that qualifier is load-bearing.** This document's central claim
> — *"it WAITS ... on timeout it fails closed"* — was measured on the path where
> `GET /agents/drain` answers. It did **not** hold when the readout could not be
> read. `drain_wait` ended with `count="${count:-0}"`, and `curl -sf` yields an
> empty body on **any** failure, so an unreadable count rendered as a confident
> **zero** and the drain reported *quiesced on the first poll, having waited for
> nothing*. The gate **failed OPEN**, and a redeploy could `kickstart -k` a live
> fleet and mint survivors with **no `--force`** — the very compounding this
> document's §5(a) describes, reachable on the **default** path.
>
> This matters most for the reader who came here for **mg-f206's arming
> decision**, because f206's non-gating of mg-0b77 and mg-13a3 rested on this
> document's finding #1: *"the drain waits, so f206 mints no survivors."* That
> reasoning was sound only while the drain actually waited. **mg-65b2 fixed the
> gate** — a missing sample is re-polled; sustained silence is classified by
> `classify_drain_precondition`; a `down` verdict consults the mg-13a3 witness
> (idle → proceed, live → refuse, double absence → fail closed); `--force`
> overrides. Finding #1 now holds unconditionally, but it did not when this was
> written, and nothing else here should be read as though it did.
>
> The rest of this record stands as written, including §5(b), which mg-8b48 has
> since fixed. It is a point-in-time record, not a live spec — **verify before
> relying on any claim here**.

## The question

> Does the drain **WAIT** for in-flight polecats, **KILL** them, or **neither**
> (i.e. lean on the PTY hangup accident mg-61a0 found)?

## Answer in one paragraph

**It WAITS — a real, blocking, bounded wait — and it never kills.** On the
default path pm-pogo's third answer is **wrong**: the drain is a genuine
mechanism, it polls a live readout, and on timeout it **fails closed** (exit 7,
dispatch restored, no bounce). But on the one path that claims to kill —
`--force` — pm-pogo's third answer is **exactly right**: nothing in the script,
and nothing in pogod, kills a polecat. `--force`'s entire documented contract
("kills them", `pogo-self-deploy:26-27`) is delegated to `launchctl kickstart
-k`, and **I measured that `kickstart -k` cannot reach a polecat**. The kill is
the PTY accident wearing a procedural name.

## 1. The drain WAITS — default path, with evidence

| Step | Where | What it does |
|------|-------|--------------|
| Enable drain | `pogo-self-deploy:533-534` → `drain_post true` (`:399`) → `POST /agents/drain` | `api.go:1249` → `SetDraining(true)` (`agent.go:427`) |
| What drain *means* | `agent.go:386-393`, `api.go:924-929` | `handleSpawnPolecat` **503s new polecat dispatch**. That is all it means. |
| Wait | `drain_wait` (`:424-435`) | Polls `GET /agents/drain` `count` every **15s** until 0 or deadline. |
| Timeout | `DRAIN_TIMEOUT=1800` (`:50`), `--drain-timeout` (`:634`) | Default **30 minutes**. |
| On timeout, no `--force` | `:542-547` | `err`, **`drain_post false` (restores dispatch)**, **`exit 7`**. No bounce. |
| On timeout, with `--force` | `:548` | Logs and proceeds to the hard bounce. |

**Drain touches no running polecat.** `SetDraining` flips one bool. Nothing in
the drain path calls `Stop`, `StopAll`, or signals any pid. It is a dispatch
gate, not a reaper — correctly so, and the header says as much.

So the wait is real and the timeout is real: **30 minutes, then fail closed.**

## 2. `--force` does NOT kill — measured, not inferred

The only "kill" in the script is `do_restart` (`:484-490`):
`launchctl kickstart -k gui/$(id -u)/com.pogo.daemon`. The header (`:16`) asserts
this "kills pogod's entire process tree". **That assertion is false for
polecats**, and the topology says why:

- `pty.StartWithSize` forces `Setsid`+`Setctty` (`agent.go:817-822`, gh #22) —
  **every polecat is its own session leader in its own process group**, not in
  pogod's.
- pogod has **no signal handler**; `defer agentRegistry.StopAll(...)`
  (`cmd/pogod/main.go:915`) is skipped outright by SIGTERM (mg-61a0 §2).
- The plist sets no `AbandonProcessGroup`, so launchd's default process-group
  cleanup applies — **to the job's process group**, which polecats setsid out of.

### The measurement

A throwaway launchd label (`com.pogo.drainprobe.46a4`; the live `com.pogo.daemon`
was never touched, verified `running` after) ran a job root with two children:

- **TREATMENT** — `POSIX::setsid()` + `$SIG{HUP}='IGNORE'`. Models a polecat's
  topology and mg-61a0's "SIGHUP-ignoring harness". Observed `pgid=33558` — its
  own group, escaped.
- **POSITIVE CONTROL** — stays in the job's process group (`pgid=33527`),
  default dispositions. **If `kickstart -k` does anything at all, this must die.**

Then the exact command `do_restart` runs at `:489`:

| Child | After `launchctl kickstart -k` |
|-------|-------------------------------|
| POSITIVE CONTROL (in job pgroup) | **DEAD** |
| TREATMENT (setsid, ignores HUP) | **ALIVE**, reparented `ppid=1` |

**Why this measurement is valid:** it can come out the other way, and one half of
it did. The control's death proves the signal was delivered and the probe is
wired up; the treatment's survival is therefore a fact about the *topology*, not
a broken probe. The probe deliberately has **no PTY**, which is the point: it
isolates launchd's contribution and finds it **zero**. It also honours mg-61a0's
two burned measurements — the probe is a single process that is itself the
session leader (`perl` + `POSIX::setsid`), not `sh -c 'trap "" HUP; sleep'`
(which measures a child's disposition), and it kills a **real parent** rather
than closing a master fd in-process.

### What follows

`launchctl kickstart -k` kills only the job's process group. Polecats are not in
it. **Nothing in the `--force` path kills a polecat.** They die exactly and only
by mg-61a0's accident: pogod's real death force-closes the PTY master → terminal
revoked → SIGHUP → death at the harness's default disposition. A harness that
traps SIGHUP — pogo is multi-provider; disposition is a per-binary property —
survives the "hard bounce" entirely.

**`--force` is not a fleet-kill. It is a fleet-kill *hope*.** And that is worse
than either branch of the trade-off anyone thought they were choosing.

## 3. What `--force` then does to the polecats it assumed dead

`cleanup_orphans` (`:440-462`) runs **after** `verify_running` (`:596-599`) and,
for every polecat in the pre-kickstart snapshot, runs `mg unclaim` and
`git worktree remove --force`. It **cannot** re-check liveness first — the new
pogod's registry is empty by construction (mg-61a0 §2).

So against a SIGHUP-ignoring harness, `--force` does not kill the polecat: it
leaves it **running** while yanking its worktree out from under it and releasing
its claim for someone else to pick up. That is a strictly worse outcome than the
kill nobody chose.

## 4. mg-ea3e's snapshot premise — sound, but narrowly

The slack set is computed from the **pre-kickstart drain snapshot** (`:551`) via
`expected_lost_mail_checks` (`:612`, `:188-192`). Its premise: *these polecats
died in the bounce, so their mail-checks legitimately vanished.*

**The premise is only ever consulted on the `--force` path.** Slack is computed
only when `leftover != 0` and `!= "?"` (`:611`); a clean drain snapshots zero
polecats and the slack set is empty. So:

- **Clean drain → mg-ea3e is sound.** The empty set forgives nothing. mg-ea3e's
  name-based fix is *not* wrong, and this does not re-open it.
- **`--force` → the premise is the accident.** pm-pogo's phrasing is precise:
  the check "grants slack for polecats it merely EXPECTS the accident to have
  killed." If the harness ignores SIGHUP, the polecat is alive, its mail-check is
  reaped anyway (mg-61a0 §3 — reproduced), and the slack set tells the detector
  **in advance** to say nothing about it. That is mg-61a0's dark polecat with the
  one detector that would have caught it pre-silenced by name.

mg-ea3e made the slack name-based rather than count-based, which is right and
strictly narrows the hole. But **names inherit the accident's soundness**: the
set is correct about *which* schedules to forgive and unfounded about *whether*
they should be forgiven at all.

## 5. Two further defects found while reading (report only — not filed, not fixed)

**(a) The wait's count is a fact about the registry, not the machine.**
`drain_wait` polls `PolecatCount()` (`agent.go:475-485`), which iterates the
**in-memory** `r.agents`. Per mg-61a0 §2, a polecat that outlived an *earlier*
pogod restart is **permanently absent** from the registry while alive. Such a
survivor reads as 0, so `"drain complete — 0 polecats active"` (`:538`) is a
claim about the registry. It gets bounced silently: no snapshot, no cleanup, no
slack, no mention. A *nightly* redeploy makes each night's survivors invisible to
the next night's drain — the failure compounds on exactly the cadence f206
proposes.

**(b) A failed build leaves drain stuck ON — no trap, no restore.**
There is **no `trap`** in the script. The only `drain_post false` is the
non-force timeout path (`:545`). `do_build`'s four `exit 4`s (`:469`, `:473`,
`:475`, `:479`) all fire **after** drain was enabled and **before** the
kickstart — so the *old* pogod stays up with `draining=true` and **dispatches no
polecats until someone restarts it**. (`exit 5`/`exit 8` are self-healing only
because a landed kickstart resets `draining` to its zero value, `agent.go:392`.)
Unattended at 03:00, a dirty tree or a broken build is a **silent fleet-wide
dispatch outage until a human notices.**

## 6. For f206's arming decision (pm-pogo's call — this is a recommendation)

The premise the retitle rests on deserves a correction: **"idle fleet" is not a
safety precondition on the default path — it is a liveness one.** Without
`--force`, a busy fleet cannot cause the nightly to destroy in-flight work. The
worst it does is wait 30 minutes and exit 7 without deploying. The human's
deliberate idle window bought *deploy success*, not *fleet safety*.

Recommendation:

1. **Arm without `--force`, never with it.** `--force` is unchosen *and*
   unimplemented; it does not do the thing the trade-off was about.
2. **The real arming blocker is not the drain — it is (b).** A build failure
   after a successful drain silently kills dispatch fleet-wide until a human
   intervenes. That is a worse unattended outcome than a missed deploy, and it
   fires on a dirty tree. A `trap` restoring dispatch on any exit looks like the
   cheap precondition.
3. **exit 7 must be loud.** Fail-closed at 03:00 with nobody watching is a deploy
   that silently never happens. Whatever arms f206 needs to surface exit 7, or
   the nightly's success is unobserved either way.
4. **(a) argues against the nightly cadence specifically**, not against
   redeploying. Each unnoticed survivor is invisible to every subsequent drain.

None of this is a recommendation to arm. The drain is sound on the path a
nightly would take; the reasons f206 is NOT READY survive this reading, and (b)
adds one nobody had counted.

## 7. Findings

1. The drain **WAITS**: real poll, 15s cadence, 1800s default, `--drain-timeout`
   override, fails closed on timeout (exit 7, dispatch restored).
2. The drain **never kills**. It is a dispatch gate; it touches no live polecat.
3. **`--force` neither waits nor reliably kills.** Measured: `launchctl kickstart
   -k` kills the job's process group; polecats setsid out of it and survive. The
   kill is mg-61a0's PTY accident. `pogo-self-deploy:16` and `:26-27` are both
   **wrong** about this.
4. Against a SIGHUP-ignoring harness, `--force` unclaims and deletes the worktree
   of a **live** polecat.
5. mg-ea3e's slack is **sound on a clean drain** (empty set) and **rests on the
   accident under `--force`**. Not re-opened; narrowed.
6. `drain_wait`'s zero is a claim about the **registry**, not the machine
   (mg-61a0 §2).
7. **No `trap`**: a build failure after drain leaves the live pogod
   undispatchable indefinitely.
