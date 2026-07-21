- **Confirmed mg-55de's mechanism by reproduction; the reaper is the right fix
  (mg-c3a6).** mg-55de's attribution — watchers stranded by pogod *deaths* — was
  re-examined against two observations that appeared to contradict it: the live
  pogod had not died once since boot, and the orphan age histogram showed
  repeated cohorts (`07h:114`) rather than the single cohort one stranding event
  would produce.

  **The dichotomy was false.** "Stranded by parent death" and "repeated spawns"
  are the same event at its two ends: the sole production call site
  (`cmd/pogod/main.go:1532`) spawns exactly one watcher per pogod boot, and
  strands that same watcher on that same pogod's death. *N* boot/death cycles
  give *N* orphans spread across whenever they happened — so a multi-cohort
  histogram is the **signature** of parent death, not evidence against it.

  The deaths were never the long-lived daemon's. They were the many short-lived
  pogods that tests and deploys boot and kill (`pogo-self-deploy_live_test.sh`,
  `_sigint_test.sh`, `test-e2e.sh`, `upgrade-smoke.sh`, every `pogo server stop`
  and redeploy). The live daemon's own watcher stayed **singular and stable since
  boot** throughout — the production path is not leaking.

  Confirmed against a sandboxed pogod rather than by inspection: booting one and
  SIGTERMing it strands its watcher at PPID 1, observed directly; booting a
  second reaps that orphan while **sparing** the live daemon's watcher, and the
  population converges to one. Every kill by explicit verified PID; the machine
  was returned to baseline.

  Then the stronger, **unprovoked** confirmation: an ordinary `./build.sh` gate
  run — no experiment attached — left exactly one orphan at PPID 1. That is the
  leak generating itself from the very activity the histogram attributed it to,
  and it is the observation the hand-cleared population could no longer supply.

  **The reaper is not a downstream guard.** `reapOrphanedWatchers()` runs inside
  `Watch()` *before* the spawn, so the source cannot outrun it — the source is
  its trigger. The 242 accumulated because no pogod had ever reaped, not because
  a reaper was falling behind. Alternatives are worse: a SIGTERM handler misses
  SIGKILL/panic/crash, darwin has no PDEATHSIG, and SIGPIPE self-termination
  fails precisely because this child almost never writes.

  Also refuted plainly, and for a sharper reason than the obvious one: the
  `Watch(context.Background(), nil)` hypothesis is wrong not merely because
  `Watch` rejects a nil hook before spawning, but because `context.Background()`
  was never the differentiator — **production's `hbCtx` is equally uncancellable
  in practice**, since no pogod exit path ever calls its cancel. The defect was
  never a test's bad context.

  Record: `docs/investigations/wake-watcher-mechanism-confirmed-2026-07-21.md`.
  Fact-finding only — no behaviour change. The reaper remains **inert until a
  redeploy** (installed pogod `249f349`), so every result is from source.
