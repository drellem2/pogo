Every agent pogod starts is now witnessed, crew included, so an
`auto_start = false` crew agent that is alive but registry-absent stops being
classified `AgentGone` and losing its mail-check.

`DesiredStateFor`'s fall-through had two defusing witnesses and this population
was in neither, by construction. `noteWitnessStart`
(`internal/agent/witness.go`) returned early unless `Type == TypePolecat`, and
is the only production caller of `RecordPolecatWitness`, so `PolecatWitness`
answered `WitnessNoRecord` for every crew agent. The rationale above it said why
— crew "have a second witness in their prompt", and giving them a redundant one
would put two sources in a position to disagree. That reasoning is sound and it
silently assumed `auto_start = true`. **The prompt-side witness IS `auto_start`.**
Turn it off and the agent has no process witness (not a polecat) and no
desired-state witness (not expected), and the classifier concluded death from
the pair — the exact prohibition mg-de08 exists to enforce, applied to the one
population that ticket's fix excluded. `crew/doctor.md` and `representative.md`
are in that class today.

The claim set, each item checkable at the source named, with no severity
attached to it:

- **Permanent.** No exit occurs, so neither an `auto_start` respawn nor the
  suppression page fires. Recovery needs someone to re-register the schedule.
- **Unannounced.** `deafwatch` iterates the REGISTRY —
  `Registry.MailLoopReport` ranges over `r.List()`
  (`internal/agent/mailloop_report.go:173`), reached through
  `deafwatch.RegistrySource` — and this population is registry-absent by
  construction, that absence being the first of the two the classifier reasoned
  from. The detector is armed on this box and has produced real alerts; it does
  not scan this set. *A detector's existence is not its coverage: the question
  is which set it iterates, and that is one `for` loop away from any reader.*
- **Detectable on demand.** `mailLoopExclusionFor` returns `""` for exactly this
  shape (not expected, not a polecat, alive, `ConfiguredStateFor` says ours), so
  `pogo agent diagnose <name>` calls it a DEAF SURVIVOR
  (`internal/agent/api.go:807`) — if you already know which name to type.
  mg-738f's own closing section calls that "detectable, not announced".
- **The mechanism is reproduced, not merely read.**
  `docs/investigations/registry-absent-while-alive-2026-07-17.md` (mg-61a0) ran
  it end-to-end on Daniel's host against `d90676c`: a SIGHUP-ignoring agent
  survives pogod's SIGTERM reparented to init, the restarted pogod's
  `GET /agents` returns `[]` while its pid is demonstrably alive, and the sweep
  then deletes the mail-check from memory AND disk with no error logged
  anywhere. It carries the control too — the same agent *registered*, gate open,
  sweeps ran, schedule untouched.
- **Reachable, conditional on the harness binary.** pogod runs no cleanup on any
  exit path (no signal handler; mg-6b66 deleted its `defer StopAll` as
  unreachable), so it never stops its agents. What kills them is the PTY hangup:
  pogod owns the master, its death force-closes it, and the agent takes SIGHUP.
  That coupling exists *because* of `Setsid+Setctty` (`agent.go:1022`), which
  makes the agent a session leader with that PTY as its controlling terminal —
  the isolation guarantee is what DELIVERS the hangup, not what prevents it, and
  `TestPolecatDoesNotOutlivePogod` pins the death rather than the survival. So
  "pogod never stops its agents" does not make every restart a survivor path:
  the investigation measured a live polecat dying within 5s of pogod's SIGTERM
  under the real claude harness. The margin is the harness's SIGHUP disposition
  — a per-binary property of a third-party program, across four providers, that
  nothing in pogo enforces or checks (its finding #2).
  `TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP` is the negative control.
- **Zero observed instances *of the crew case*,** which is narrower than the
  blanket version. The mechanism has a reproduction; what has none is this
  population's instance of it. Three agents filed the crew case within 93
  seconds (mg-f9e8, mg-d67b, mg-4215, consolidated) and none reproduced it —
  one reading replicated, not three confirmations.

**This is the unfinished half of that investigation, not a new direction.** Its
§6 "not shipped, pending a call" names the candidate: *"give the fall-through
POSITIVE LIVENESS EVIDENCE for unregistered polecats (persist polecat pids so a
restarted pogod can probe one), making absence trustworthy rather than
assumed"*. That shipped as mg-13a3 — for polecats. Crew were left out of the
store built to survive the exposure, and that is mg-f9e8. Its §5.4 also binds
this fix's shape: *"this does not re-open mg-de08 or mg-8677 ... the fix is NOT
to loosen the reap"* — nothing here widens what counts as expected.

One staleness note on that document, since it is three weeks old: §2 cites
`defer agentRegistry.StopAll(cmd/pogod/main.go:915)`, which mg-6b66 has since
deleted as unreachable. The citation is stale; the conclusion is not, and is now
stated more strongly in-source at the site where the defer used to be.

**Widening the writer is not widening the readers, and that distinction is the
shape of the fix.** The suggested direction was to widen `noteWitnessStart`
alone as the minimal change. It is not minimal: this store is read by things
that mean "the polecats" literally. `WitnessedAlivePolecats` feeds the redeploy
drain, which waits for the count to reach ZERO — crew never exit, so an
unfiltered widening wedges every redeploy — and the orphan alert, which mails
the coordinator an authoritative `kill <pid>` per row, which would have become a
standing kill order for the fleet including its reader. `WitnessedPolecatRepos`
feeds the per-repo dispatch cap (it would refuse correct dispatches, more of
them the healthier the fleet was), `WitnessedPolecatVerdicts` feeds gitgc's live
set, `WitnessedPolecatWorkItems` feeds stall-watch, and `UnadoptablePolecats`
looks for a `polecat-<name>` branch that no crew agent has. So `witnessRecord`
carries a `Type`, those six readers filter on it, and only `AgentWitness` — the
classifier's probe, which asks about ONE named agent — answers across the whole
store. An empty `Type` reads as polecat: every record a pre-mg-f9e8 pogod left
on disk has no such key, and reading those as "not a polecat" would drop a
redeploy's survivors out of the live set, which is a worktree removed from under
a running polecat.

**What did not change, and both were checked rather than assumed.** An agent
pogod never starts is still unwitnessed, still not expected, and still reaped:
`mailcheck_gc_restart_test.go:152`'s `lurker` assertion passes UNCHANGED, which
was a hard condition on this work because that reap is deliberate and prevents
orphan nudges. And the negative arm binds — a crew agent an earlier pogod
recorded and that has genuinely exited is still reaped once its witness reads
`WitnessDead`, because a guard observed only keeping things alive is not known
to work.

Three hazards the remedy created in its own image, each found by asking how the
fix could exhibit the defect it repairs:

1. **A dead witness no longer answers GONE by itself; it falls through to the
   desired state.** For a polecat nothing changes — no prompt, so the answer is
   still GONE and mg-8677's recycled pid is still reaped — but crew can now hold
   a dead witness, and the state that produces one is ordinary: pogod restarts
   nightly, its death takes the crew with it, and every crew witness is a corpse
   from the successor's boot until `AutoStartAgents` respawns it. Returning GONE
   there would reap the whole fleet's mail loop whenever that sweep is late or
   fails for one agent — mg-de08 re-entered through mg-f9e8's fix. The startup
   GC gate usually covers the window; mg-de08 was "usually covered" too.
2. **The respawn path now re-witnesses.** `noteWitnessExit` clears the record on
   exit, and respawn is the COMMON path for crew (`RestartOnCrash` defaults true
   for them), so without a write at `Registry.respawn` the fix would have
   covered only agents that had never crashed.
3. **`AgentWitness` resolves `crew-<name>` as well as `cat-<name>`.** Crew
   mail-checks are registered under both spellings on this fleet —
   `mailcheck_gc_restart_test.go` carries one under `crew-pm-pogo` — so matching
   only `cat-` would have fixed an agent whose schedule used one spelling and
   left the identical agent broken under the other. `DesiredStateFor` strips the
   same prefix, so the classifier's two steps now agree about which strings name
   the same agent.

`PolecatWitness` is renamed `AgentWitness`, since it is the one function whose
subject actually changed. The polecat-named readers keep their names because
they keep their meaning — enforced by a filter now rather than by the writer.
