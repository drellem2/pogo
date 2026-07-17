# The polecat witness — making absence stop being the input

**Date:** 2026-07-17 · **Work item:** mg-13a3 · **Xref:** mg-61a0
([the repro + the accident](registry-absent-while-alive-2026-07-17.md), `05eef74`),
mg-8677 (`d90676c`, the fall-through), mg-de08 (`7a2dc7f`, the principle),
mg-76e5 (absent-vs-zero, done right), mg-f206 (the nightly restart),
drellem2/pogo#89 (the external report that started it)

Point-in-time record of the fix mg-61a0's repro called for. Built against `fbdcae1`.

## The defect, re-derived from main at claim time

`registryLiveness.AgentState()` (`cmd/pogod/main.go`, `d90676c`) resolved an
agent from the registry, then — when the registry held no entry — from the
desired state, whose `default` arm returned `AgentGone`. **That default arm is
the polecat path, and both of its inputs are absences:**

1. not in the registry — absence
2. not in the desired state — absence

Two absences; the code concluded death. That is *absence of evidence of life*,
which mg-de08 forbids. The comment shipped with mg-8677 defined the problem
away rather than fixing it:

> "GONE means positive evidence of death — a corpse in the registry, **or an
> agent that is neither registered nor in the desired state** (polecats...)"

First disjunct: positive evidence. Second disjunct: a double absence *called*
positive evidence by definition. mg-de08 did not fix the principle — it fixed
the population that happened to have a second witness. Crew survive the
registry's amnesia only because `auto_start` in their prompt is an independent
source of truth. **Polecats have no prompt at all.**

The honest statement of what had shipped, before this fix:

> A reap requires positive evidence of death **for agents that have a second
> source of truth**. For everyone else it still reaps on absence, and that has
> been safe only because nothing has yet made absence lie.

Not theoretical: mg-61a0 reproduced it end-to-end. A live polecat (pid 32471),
unregistered after a pogod restart, was classified GONE and had its
`mail-check-*` deleted from **both memory and disk** — permanently dark. The
registry is in-memory with **no adopt/reattach path**, so a restarted pogod's
registry is empty *permanently, for any survivor*. Absence never heals.

## The fix

Persist each polecat's `(pid, start_time)` at spawn
(`~/.pogo/polecat-witness.json`), drop it at exit, and consult it **between**
the registry and the desired state:

> **Registry-absent + OUR process alive = UNKNOWN, never GONE.**

Order is registry → witness → desired state: evidence, then evidence, then
expectation. mg-8677's precedence rule is untouched — a corpse the registry
holds still beats both later sources, and that is asserted, not assumed
(`TestRegistryLiveness_RegistryStillBeatsWitness`).

**`(pid, start_time)`, never pid alone.** Pids are reused. A bare
`kill(pid, 0)` answers "is SOME process alive", never "is OUR process alive" —
an instrument that cannot tell our process from some process is tonight's
disease wearing a lanyard. A recycled pid reading alive would keep a dead
polecat's schedule firing at a corpse forever: **mg-8677, re-entered through the
fix for mg-61a0.** A pid whose start time disagrees is not our polecat → GONE.

The start time is **the kernel's**, read via `ps -o lstart=`, not `time.Now()`
at spawn: it must be re-derivable by a pogod that never spawned the process. A
value we invented could not be matched against. Because the same instrument
produces both sides of the comparison, no skew tolerance is needed — unlike
`internal/reconcile`'s twin (`hostdeps.go`), which compares an lstart reading to
a file mtime and needs `procStartSkew` to absorb lstart's whole-second
truncation.

**If the start time cannot be read at spawn, nothing is written.** No witness
leaves the classifier exactly as it was before this store existed — no better,
no worse. A pid-only record would be strictly worse: a false witness that
answers UNKNOWN at a corpse forever.

## Proving the controls can go RED

The bar for this ticket was mg-61a0's own: a control that cannot fail is the
defect, not the test. Each guarantee was broken on purpose, the specific test
watched to fail, and the source restored. All four confirmed on `90a3eb1`.

| # | Break | Test that went RED | Observed |
|---|-------|--------------------|----------|
| 1 | Delete the witness consultation from `AgentState` (revert to the shipped double-absence logic) | `TestRegistryLiveness_WitnessedPolecatIsNotReaped` | `AgentState(cat-13a3) = 3` (`AgentGone`) for a **live** polecat — mg-61a0's bug, reproduced |
| 2 | Make the witness naive: `return WitnessAlive` on a bare live pid, ignoring start_time | `TestWitnessDeadWhenPidRecycled` (store) **and** `TestRegistryLiveness_RecycledPidIsGone` (classifier) | recycled pid read `alive` / `AgentUnknown(0)`, want `dead` / `AgentGone(3)` — mg-8677 re-entered |
| 3 | Comment out `noteWitnessStart(a)` in `Spawn` | `TestRegistryLiveness_SpawnRecordsWitness` | `PolecatWitness(cat-spawned) = no-record`, want `alive` |
| 4 | Comment out `noteWitnessExit(a)` in `waitAndHandle` | `TestRegistryLiveness_SpawnRecordsWitness` | witness `dead` after exit, want `no-record` |

**Break 4 is worth recording precisely, because one test did *not* fail.**
`TestWitnessDropRemovesRecord` stayed green with the exit wiring commented out —
it calls `noteWitnessExit` directly, so it pins the *function*, not the
*wiring*. That is legitimate as a unit test and the wiring is covered by
`TestRegistryLiveness_SpawnRecordsWitness`, which did go red. But the split is
exactly the class of thing that hides: know which test guards which claim.

### Two instrument problems caught in the building

Both were caught by running the tests, not by reasoning about them — which is
the point.

- **The recycled-pid test SKIPPED on first run.** `ps lstart` resolves to whole
  seconds, so two `sleep`s started in the same tick are indistinguishable to the
  probe and the test skipped itself. **The single required control in this
  ticket was silently not running.** Fixed by crossing a second boundary before
  starting the stand-in process (`waitForNextSecond`), making the distinction
  deterministic, and by turning the leftover same-second case from a `t.Skip`
  into a `t.Fatal` — a skip is a control that cannot fail.
- **An exported test hook was nearly added to production.** The first draft of
  the classifier test called an `agent.CorruptWitnessStartTimeForTest(...)`
  helper exported from the agent package. It was replaced with a test-local
  function that edits the witness **file** — less invasive *and* more faithful,
  since the classifier's whole premise is reading a file some other process
  wrote.

### What the tests deliberately do NOT fake

`procStartFn` is indirected, but it is overridden in exactly two tests — the two
whose subject *is* an unreadable probe. Everything about "can we tell our
process from some process" runs against **real processes and the real `ps`**. An
instrument that cannot make that distinction is the defect this store exists to
prevent, so it is the one thing that must not be mocked.

The recycled-pid case cannot be produced by forcing the kernel to hand back a
specific pid. It is modelled the only way the probe can observe: a witnessed pid
that answers signals while holding a process whose start time is not the one we
recorded. The process's *history* is not something the probe can see, so
crossing two real identities is not an approximation of the recycle — it is
exactly what the recycle looks like from here.

## Design questions the ticket asked, answered

**Where pids persist / what invalidates them.**
`~/.pogo/polecat-witness.json`, versioned JSON with atomic temp+rename writes,
mirroring `internal/scheduler/store.go`. A future-version file is refused rather
than overwritten, and — because a refusal is an inability to read, not evidence
of death — it yields UNKNOWN, never a reap. Records are invalidated by: process
exit (dropped by `waitAndHandle`, which is *this pogod watching the process
die*, so the record is known false rather than merely stale); a failed identity
match; a re-spawn under the same name (replaced, not stacked). The atomic write
is load-bearing rather than hygiene: a torn file reads as "no record" and would
reap live polecats.

**PID reuse.** Handled by the `(pid, start_time)` match — see above. Stated
limit of the instrument, rather than glossed: `ps lstart` is whole-second, so
this cannot distinguish a recycled pid whose new process started within the
*same second*. That is not a real exposure (pids are allocated sequentially; a
reuse requires churning the whole pid space first), but it is where the
instrument ends. If pids ever became reusable that fast, the answer is a finer
identity source, **not** a wider tolerance.

**Whether an unprobeable pid is UNKNOWN.** Yes — `WitnessUnreadable` → UNKNOWN,
never reaped. A live pid whose identity we cannot read means we know *something*
is alive and do not know that it is *ours*; that difference is the entire
subject. The cost is bounded noise; the alternative is reaping on an inability
to measure, which is mg-de08's defect. This matches the existing
`DesiredStateFor` error arm, which already declines to call an unreadable prompt
death.

**Interaction with gitgc's `livePolecatSet`.** Verified against
`internal/gitgc/sweep.go` rather than assumed — and the obvious worry is
**wrong**. `livePolecatSet` is registry-built and so is empty after a restart,
but an empty live set is **not** sufficient to delete a running polecat's work:
every deletion path is independently gated on `state.Concluded()`
(`sweep.go:127`, `:197`, `:275`), and a `claimed` ticket is `TicketInFlight`
(`classify.go:52-63`), never concluded. Branch deletion additionally requires
the branch to be merged (`sweep.go:204-216`).

The real exposure is narrower and is **a different reap from this ticket's**:
the window where a ticket is already `done`/`archived` while its polecat is
**still running**. Under the polecat protocol that is every polecat's normal end
state — `mg done`, then stay alive awaiting the mayor — and in that window
`LivePolecats` is the *sole* guard for the worktree (worktree removal has no
merge gate, `sweep.go:124-142`). A restart empties it. Same root cause as this
ticket (reasoning about live polecats off an empty registry), reaching a
different victim (a worktree instead of a schedule). The witness now on disk is
the natural fix, but it is a distinct reap with its own risk profile and
controls: **left to a follow-up ticket, not widened into this one.** One
mitigation narrows it further — after a restart the registry is empty, so
`gitGCRepos` yields only `cfg.Repos`, and with none configured the sweep returns
before doing anything (`cmd/pogod/gitgc.go:53-55`).

## What this does and does not close

mg-61a0 shipped option (a) (`05eef74`): `TestPolecatDoesNotOutlivePogod` pins the
accident that has been holding the line — pogod installs **no** signal handler
(`StopAll` is a bare `defer` at `main.go:915`, skipped entirely on SIGTERM), so
its death closes the PTY master and the setsid'd polecat takes SIGHUP and dies
at its default disposition. That accident does work nothing else can: on
SIGKILL, panic, or host crash no handler runs, and the hangup is the only thing
that kills polecats. **(a) is permanent, not a stopgap.**

But (a) detects; it cannot prevent. Its contract — "a polecat MUST NOT outlive
pogod" — must be honoured by a **third-party binary we do not control** (claude,
codex, cursor, pi). We can test their SIGHUP disposition; we cannot fix it. And
(a) pins only *pogo's* half: `TestPolecatDoesNotOutlivePogod` spawns `sleep`, so
a provider that began trapping SIGHUP would leave the test green while real
polecats went dark. **That gap is what this ticket closes.** (a) makes the luck
visible; the witness makes it unnecessary.

Not closed, and deliberately so:

- **The registry still has no adopt/reattach path.** A survivor is still
  *absent* from `pogo agent list` after a restart; it is merely no longer
  *reaped*. The witness is evidence for the classifier, not a registry.
- **The gitgc done/archived-while-running window** — see above; follow-up.
- **A polecat spawned by a pogod that could not read its start time** gets no
  witness and is no better off than before. That is the honest boundary of the
  fix, and it is loud in the log rather than silent.
