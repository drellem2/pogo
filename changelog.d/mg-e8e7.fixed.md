- **The rest of `internal/agent`'s respawn tests no longer ask the developer's machine whether an
  agent is parked (mg-e8e7).** mg-6092 fixed the four `ShouldRespawnAgent` tests that a real
  `pm-dealdesk` spin-down had turned red. It did not fix the class. Park state lives on disk and is
  addressed by **agent name** — `ParkFilePath(name)` is `PromptDir()/<name>/.parked`, `PromptDir()`
  is `config.PogoHome()/agents`, and `PogoHome()` is `$POGO_HOME` falling back to `$HOME/.pogo`.
  The package's `TestMain` cleared `POGO_HOME` but left `HOME` alone, so any test that named an
  agent and did not re-point `HOME` itself was reading live host state.

  **The direction that matters is the quiet one.** A stray park flag turning a restart test red is
  loud and self-correcting — that is what happened on 2026-07-24, and it announced itself within
  the hour. The hazard is the reverse: `Registry.Stop` branches on `RestartOnCrash && !IsParked`,
  and a park flag makes an agent stay down for a reason that has nothing to do with the flag under
  test. `TestRestartOnCrashFlagDrivesBranching` is exactly that shape — its two *"stays down"* rows
  assert an agent is **not** respawned, and a park flag on `roc-crew-off` satisfies that assertion
  whether or not the `RestartOnCrash` branch still works. A regression in the restart gate could
  have shipped green.

  **Swept empirically, not by grep.** The whole suite was run twice under synthetic `HOME` trees
  identical but for planted `.parked` flags — one flag for every agent-name literal in every
  `_test.go` in the repo (1,502 names; the tests build no agent names dynamically, so that set is
  complete) — and the per-test outcomes diffed. Seven tests in `internal/agent` changed answer:
  `TestRespawn`, `TestWorkItemIDPreservedAcrossRespawn`, `TestEmitsAgentRestartedOnRespawn`,
  `TestRespawnKeepsResolvedProvider`, `TestCrewRestartOnCrash`,
  `TestRestartOnCrashFlagDrivesBranching` (2 of 4 rows) and `TestStopRespawnsRestartOnCrashAgent`.
  Every other package was already clean — `cmd/pogod`'s `sandboxPogoHome` and
  `internal/agent`'s `TestIsConfiguredAgent` already do exactly this.

  **Fixed in two independent layers, test-only.** No production code changed and no assertion
  changed. Each of the seven now calls `isolateParkState` — promoted out of
  `synthfail_diagnose_test.go` into `testmain_test.go`, since it now serves the package rather than
  one file — and `TestMain` additionally re-points `HOME` at a throwaway tree for the whole package.
  The per-test call keeps the intent visible where the assertion is; the `TestMain` backstop is what
  actually closes the class, because it covers the next respawn test somebody writes without
  remembering any of this.

  **Proved host-independent, not merely green.** The two-tree diff is now byte-identical: no test in
  the repo changes outcome between "every agent parked" and "none parked". Both layers were then
  disabled in turn and re-run under the all-parked tree — per-test isolation alone passes, and the
  `TestMain` backstop alone passes — so neither is carrying the other. And the package is green
  against the **real** `~/.pogo` with `pm-dealdesk/.parked` and `pm-lineara/.parked` still present
  (they were not removed), writing nothing into it: `~/.pogo/agents/` and its park flags are
  byte-identical before and after a full package run.
