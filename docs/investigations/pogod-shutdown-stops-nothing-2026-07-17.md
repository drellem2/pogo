# pogod's shutdown stops nothing: the `defer StopAll` was unreachable on every path

**Date:** 2026-07-17
**Work item:** mg-6b66 (carved out of mg-61a0)
**Verdict:** the `defer` is deleted. `StopAll` stays — it has a live caller.
**Xref:** mg-61a0 (`05eef74`, the invariant test), mg-13a3 (the actual repair),
`registry-absent-while-alive-2026-07-17.md`, gh #22

## The claim that was in the code

`cmd/pogod/main.go` carried, right after the registry was built:

```go
defer agentRegistry.StopAll(5 * time.Second)
```

Anyone reading that concludes pogod shuts its agents down deliberately on the
way out. It does not, and never has on any path anyone can trigger.

## Why it could never run — and it is not only SIGTERM

The original finding said the defer was "skipped by SIGTERM." True, but it
undersold it. There is **no execution path that reaches this defer at all**:

| Exit path | Reaches the defer? | Why |
|---|---|---|
| SIGTERM — `pogo server stop`, launchd, the nightly restart (mg-f206) | No | No `signal.Notify` anywhere in `cmd/pogod`. Default disposition; defers skipped. |
| `Serve()` returns an error | No | `main` ends in `log.Fatal(httpServer.Serve(...))`. `log.Fatal` is `os.Exit(1)`, which skips defers. |
| SIGKILL, panic, host crash | No | Skipped by definition. |

SIGTERM is not an edge case here: `internal/client.stopDaemon` signals it
directly, so it is *the* routine stop.

Every other `defer` in `main()` is dead for the same reason — including
`lock.Unlock()`. Out of scope for this ticket, but it is the same fact.

## The proof (real pogod, real agent, real SIGTERM)

Sandboxed pogod (`--port 10877`, isolated `HOME`/`XDG_CONFIG_HOME`/`POGO_HOME`,
`[agents] command = "sleep 600"`), one real PTY-backed agent.

**Positive control first — an instrument that cannot go RED measures nothing.**
Stopped the agent via `DELETE /agents/probe`, which runs the real `Stop` path:

```
2026/07/17 09:08:07 agent probe: stopped (restart_on_crash=true; supervisor will respawn)
```

The instrument sees `stopped` when `Stop` really runs. So a zero count means
something. Then SIGTERM to pogod, with a live agent registered:

```
pogod=89379 agent=91354   'stopped' lines before: 1
=== SIGTERM pogod ===
pogod alive?          NO (dead)
agent 91354 alive?    NO (dead - PTY hangup)
>>> 'stopped' lines after SIGTERM: 1  (was 1)
--- tail of pogod.log ---
2026/07/17 09:08:09 agent probe: respawned pid=91354 restart=1
```

**Zero new `stopped` lines.** The count never moved off the control's. pogod
died silently, emitting no shutdown logging whatsoever — the last line is the
respawn that preceded the signal. The agent died anyway, by PTY hangup.

## What actually kills the agents

pogod owns each agent's PTY master. Its death force-closes that fd; the terminal
is revoked; the agent — a session leader with that PTY as its controlling
terminal, via `pty.StartWithSize`'s `Setsid` (gh #22) — takes SIGHUP and dies at
the default disposition.

This accident is **load-bearing**. The mail-check GC reaps any polecat absent
from the in-memory registry, which is sound only because a polecat cannot
outlive pogod. It holds only while the harness binary leaves SIGHUP at its
default disposition; a provider that traps it re-opens the dark-polecat path
silently. Pinned by `TestPolecatDoesNotOutlivePogod` (mg-61a0); the durable
repair is mg-13a3's pid+start_time witness. **Deleting the defer does not fix
that hazard and must not be cited as doing so** — nothing about the runtime
changed here.

## Why delete the defer instead of adding the handler

Both were on the table. Deleting won on three counts:

1. **A handler fixes the path that isn't the problem.** It covers graceful
   SIGTERM only. SIGKILL, panic and host crash — the paths that actually strand
   agents — are exactly the ones no handler can cover.
2. **A naive handler is a regression, not a cleanup.** `StopAll` stops agents
   *serially*, each with the full timeout (`timeout × len(agents)` worst case),
   while `stopDaemon` gives pogod **5 seconds total** before reporting failure.
   A handler running `StopAll(5s)` makes `pogo server stop` miss its own
   deadline on any real fleet. Doing it right means budgeting to that deadline —
   a behaviour change, not the thin revertible diff this ticket asked for.
3. **The defer is not what makes shutdown work**, so removing it changes no
   behaviour. Verified above: the agent dies identically either way.

A handler remains a legitimate future choice. If you add one, size the stop
budget to `stopDaemon`'s deadline, prove `stopped` lines appear where this
document proves there are none, and keep the hangup documented — it stays
load-bearing regardless, because SIGKILL still rides it.

## What was NOT deleted, and why it matters

**`StopAll` itself stays.** The ticket originally called it dead code with "one
call site, a bare `defer`." That was false: `Server.transitionToIndexOnly`
(`internal/server/server.go`) calls it directly, on the live full → index-only
transition, and it really runs. Deleting it would have broken that path.

The mayor caught this only by re-deriving the call sites at dispatch time rather
than trusting the ticket body it had itself written. Worth recording: the fix
for a false-claim-about-the-system was itself dispatched carrying a false claim
about the system, and would have introduced the very class of bug it exists to
remove.

## The class

A **cleanup that cannot fire, reading as the mechanism** — the same
false-fact-about-the-world found eight-plus times on 2026-07-17 across five
altitudes. The others were checks and instruments that couldn't fail; this one
was a shutdown path that couldn't run, wearing the appearance of the thing doing
the work.

It had spread beyond the `defer`. The same false claim was asserted in five
places, including `StopAll`'s own doc comment ("use this on registry teardown
(pogod shutdown…)") and `ARCHITECTURE.md`. All five now describe the PTY hangup.
A lie in a doc comment propagates: it is what the next reader greps for.

> architect: *"but not silence, because right now the code lies to the next
> reader about how agents die."*
