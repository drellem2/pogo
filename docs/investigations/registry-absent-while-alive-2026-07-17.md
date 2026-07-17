# Registry ABSENT while the process is ALIVE — and the dark polecat

**Date:** 2026-07-17 · **Work item:** mg-61a0 · **Xref:** drellem2/pogo#89 (third
item, reported by CloverRoss 06:24Z), mg-8677 (`d90676c`), mg-de08 (`7a2dc7f`),
mg-76e5 (the absent-vs-zero distinction), mg-07ba (triage packet, predates this)

Point-in-time record. Reproduced on Daniel's host against `d90676c`.

## The report

> "a *registry-absent-while-process-ALIVE* flap … Our daily-cycle supervisor read
> an agent absent from `pogo agent list` while its tracked pid was still running
> … likely a read during a registry write/lock window."

## Outcome in one paragraph

The symptom is **real and reproduced here** — but the reporter's mechanism is
wrong, and the honest headline is worse than a flap. The registry reports a live
agent absent whenever pogod **restarts** and the agent outlived it; there is no
lock window involved and the state never heals. From there, a live polecat's
`mail-check-*` **is** reaped end-to-end, exactly as mg-61a0 predicted, taking it
dark mid-task. What stops that today is **not** anything in `AgentState`: it is an
**accidental PTY side effect** that kills polecats when pogod dies. It holds for
the current harness, it is undocumented, untested, and it is a property of the
harness binary rather than of pogo.

## 1. The reporter's mechanism does not exist in our code

No torn read, and no remove-then-readd window with a live process:

| Path | Why it cannot produce absent-while-alive |
|------|------------------------------------------|
| `Registry.List` (`internal/agent/agent.go:961`) | Copies the map under `r.mu.RLock()`. |
| `Registry.Spawn` (`:721`) | Holds `r.mu.Lock()` across **both** the process start (`:822`) and the registry insert (`:878`); a concurrent `List()` blocks. |
| `Registry.Respawn` (`:1093`) | Holds the write lock and **never deletes** — it overwrites `r.agents[name]` in place. |
| `Registry.Remove` callers | `Stop` (dead pid / no-respawn) and the `onExit` hook — the process is already gone. |

Their host runs `/opt/pogo` ~170 commits behind (#88 item 3), so their pogod may
not be our pogod. The mechanism below explains their observation without a lock.

## 2. The real mechanism: pogod restart + an in-memory registry

- The registry is **in-memory with no adopt/reattach path**. A restarted pogod's
  registry is empty, **permanently**, for anything that outlived it.
- pogod has **no signal handler at all** (no `signal.Notify` anywhere), and
  `defer agentRegistry.StopAll(...)` (`cmd/pogod/main.go:915`) is skipped
  outright by SIGTERM. Verified: zero `stopped` log lines after SIGTERM. **pogod
  never stops its agents on the way out.**
- `mail-check-*` schedules persist to `~/.pogo/schedules.json` and are reloaded.

So after a restart: schedule present, agent absent, process alive. That is the
reported symptom, and for a survivor it is permanent, not transient.

## 3. The dark polecat — reproduced end-to-end

Sandboxed pogod (isolated `HOME`/`XDG_CONFIG_HOME`/`POGO_HOME`, `[agents]
autostart = false`, `[heartbeat] interval = "2s"`):

1. Spawn a polecat that ignores SIGHUP (`sh -c 'trap "" HUP; sleep 600'`);
   register its `mail-check-*`.
2. SIGTERM pogod. The polecat **survives**, reparented to init (`ppid=1`).
3. Restart pogod on the same `POGO_HOME`. `GET /agents` → `[]` while the pid is
   demonstrably alive. **This is the reported symptom.**
4. Wait for the `startupGCSettle` gate (30s) and a sweep.

`AgentState` → not in registry → `DesiredStateFor` → polecat is not `auto_start`
→ **`AgentGone`** → the mail-check is deleted from **memory and disk**. The
polecat is alive and permanently dark. No error is logged anywhere.

**Control (the repro can distinguish):** with the same agent *registered*, the
gate opened and sweeps ran with the schedule untouched.

## 4. Why it does not bite us today — an accident, not a design

With the **real claude harness**, a live polecat died within 5s of pogod's
SIGTERM. Mechanism: pogod owns the PTY master; its death force-closes it; the
terminal is revoked; the polecat — a session leader with that PTY as its
controlling terminal, via `pty.StartWithSize`'s `Setsid` (gh #22) — gets SIGHUP
and dies at the default disposition.

So the sweep's ABSENT→GONE inference is *usually* right, for a reason
`AgentState` knows nothing about.

Two measurements worth keeping, because both mislead:

- **Closing the master in-process does NOT hang up the terminal.** While
  `readOutput` is blocked in `read(2)`, the kernel still holds a reference to the
  file description, so no SIGHUP is sent and a `sleep` polecat survives
  indefinitely. Only the parent's real death force-closes the fd. A test built on
  `master.Close()` passes for the wrong reason.
- **`sh -c 'trap "" HUP; sleep 600'` does not model a SIGHUP-ignoring harness.**
  The hangup kills its unprotected `sleep` child and the shell exits when that
  child reaps — measuring the child's disposition, not the harness's. A single
  process that is itself the session leader (verified with `perl
  $SIG{HUP}="IGNORE"`, pid survived, `ppid=1`) is the valid probe.

## 5. Findings

1. The reap of a **live** polecat's mail-check is real, reachable, and silent.
2. The **only** thing preventing it is the harness leaving SIGHUP at its default
   disposition. Nothing in pogo enforces or checks this. pogo is multi-provider
   (claude, codex, cursor, pi); SIGHUP disposition is a per-binary property. A
   harness that traps SIGHUP to shut down gracefully — an entirely reasonable
   thing for a TUI to do — re-opens this instantly.
3. The `AgentState` fall-through **does** conflate *absent* with *unknown*, the
   same distinction mg-76e5 enforced for `mail_check_count` ("unreachable" and
   "zero" are different facts). Absence is currently trusted as a fact about the
   world because an accident happens to make it true.
4. This does **not** re-open mg-de08 or mg-8677. Their precedence rule (*consult
   desired state ONLY when the registry yields no evidence*) is correct. The fix
   is **not** to loosen the reap.

## 6. What shipped, and what did not

Shipped: `internal/agent/polecat_pty_hangup_test.go` — converts the accident into
a checked invariant. `TestPolecatDoesNotOutlivePogod` kills a **real** parent
process (the helper-process pattern; see the fidelity note above) and requires the
polecat to die with it. `TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP` is the
negative control: it asserts survival on purpose, documenting that the harness's
signal disposition is the whole margin. Both were demonstrated RED against the
real hazard before landing.

Not shipped, pending a call from mayor/pm-pogo — a harness-independent guarantee.
The candidate: give the fall-through **positive liveness evidence** for
unregistered polecats (persist polecat pids so a restarted pogod can probe one),
making absence *trustworthy* rather than *assumed*, or distinguishing ABSENT from
UNKNOWN. This adds evidence at the fall-through and does not loosen the reap.

Deliberately not attempted: adding a SIGTERM handler to pogod so `StopAll` runs.
It narrows nothing — it only makes the common path orderly, while the crash and
SIGKILL paths (the ones that produce survivors) are exactly the paths a handler
cannot cover.
