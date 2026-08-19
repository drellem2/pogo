- **The deploy suite isolated the nightly runner with `HOME`, and the runner
  reads `POGO_HOME` — so the sandbox silently did nothing, and every suite run
  wrote the live fleet-bounce counter (mg-0af3).** Two lines carry the whole
  defect:

      scripts/pogo-deploy_test.sh:68     HOME="$WORK" bash "$RUNNER"
      scripts/launchd/pogo-deploy.sh:613   STAMP="${POGO_DEPLOY_STAMP:-${POGO_HOME:-$HOME/.pogo}/deploy-attempt.stamp}"
      scripts/launchd/pogo-deploy.sh:2244  TRANSPORT_STREAK="${POGO_DEPLOY_TRANSPORT_STREAK:-${POGO_HOME:-$HOME/.pogo}/deploy-transport-streak.stamp}"

  `$HOME` is consulted ONLY when `POGO_HOME` is unset, and every pogod child has
  it set. Overriding `HOME` to isolate a script that prefers `POGO_HOME` is
  isolation that does nothing at all.

  **MEASURED, pre-fix.** Wrapping `transport_streak_save` in a scratch copy of the
  runner to record every target path and caller line across a full 450-assertion
  run: TWO writes escape per run, both to
  `$POGO_HOME/deploy-transport-streak.stamp` —
  `pogo-deploy.sh:3759` (the bump, from `run_e2e step`) and `:3811` (the post-sync
  CLEAR, from `run_e2e ok`). The CLEAR is the one worth naming: it writes without
  logging — `log` fires only when the previous count was above zero — so reading
  the e2e logs undercounts the leak and finds only the bump.

  **The ticket framed this as an ORDERING hazard, gated behind mg-e121. It is not
  gated; it is live.** The premise was that `POGO_HOME` is drifted to
  `/Users/daniel`, diverting the writes to a harmless decoy, and that fixing the
  drift would redirect them onto real state. On this box `POGO_HOME` is already
  `/Users/daniel/.pogo` — the real one — for at least some pogod children,
  including the agent that made this change. The redirection has already happened.

  **Observed twice on live state, not inferred.**
  `~/.pogo/deploy-transport-streak.stamp` was ABSENT at 2026-08-19T16:47Z and
  PRESENT at 16:52:31Z holding `2026-08-19 0 -` — byte-identical to what a pre-fix
  run produces in a canary. Its mtime then moved AGAIN, to 18:14:50Z. Neither write
  was this author's: every streak write made by these runs was instrumented at
  `transport_streak_save` and recorded its target, and all of them landed in canary
  directories; the second transition happened while this branch's own `./build.sh`
  was running, and the in-suite acceptance check — which brackets the deploy
  suite's own 38.8s window — passed, so it did not fall inside it. Other pogo
  `./build.sh` gate runs were active on the host across both windows, and
  `test.sh:171` runs this suite. The writing pid was NOT captured, so attributing
  the writes to a refinery gate is strong but unproven. What IS measured is the
  absent→present transition, the byte-identical content, the second mtime move, and
  that this author's runs account for none of it.

  That file is the counter that fires a FLEET-WIDE BOUNCE at threshold 2. A gate
  run that leaves it at 1 arms a real bounce on the next lost night; one that
  resets it to 0 suppresses a bounce the fleet is owed. Both directions are a test
  run reaching into production state.

  **The fix scopes by the variable the runner actually reads.** `POGO_HOME` is
  exported to `$WORK` once, covering every child — the `source`, `--help`, and both
  `bash -c 'source "$RUNNER"'` harnesses — and each invocation site also sets it
  inline beside its `HOME` override, so the scoping is readable where the run is
  written rather than only 3000 lines above it. Two assertions fire immediately
  after the `source` to prove `STAMP` and `TRANSPORT_STREAK` landed inside `$WORK`,
  at the moment it becomes true rather than by inferring it from a later absence.

  **The acceptance leads with a POSITIVE CONTROL, because "no files appeared" is
  exactly what a broken check reports.** A real runner process is driven at a
  canary `POGO_HOME` with no `POGO_DEPLOY_TRANSPORT_STREAK` override — the shape of
  the `run_e2e ok` arm that was measured to leak — and the stamp is REQUIRED to
  appear there, and required to be SEEN by the same fingerprint function the leak
  check uses. A third assertion then requires the write NOT to land under
  `$HOME/.pogo`, which is the structural defect asserted rather than described: it
  goes red if anyone reverts the isolation to `HOME` alone. Only then does the leak
  check compare the real `$POGO_HOME` stamps, captured BEFORE the export — reading
  them afterwards would resolve to `$WORK` and assert nothing, which is this
  ticket's own defect wearing the remedy's clothes.
