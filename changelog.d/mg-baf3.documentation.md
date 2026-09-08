- **The gate's SIGTERM report now BOUNDS the stale-pid class instead of leaving
  it in the catch-all — the floor is the killed process's own age, and it
  narrows the class from "every watchdog in the tree" to exactly one
  construction, which is inside the gate (mg-baf3).**

  The sender is **still not identified**, and this does not identify it. What it
  does is turn mg-cbc3's standing account of the kill from a class that admits
  everything into a filter, and then apply the filter.

  **Site A is gone from this question.** mg-f387 filed it as one fault at two
  sites. Site A — the nightly deploy's `Terminated: 15` on `launchctl kickstart
  -k` — was answered by **mg-bead**: pogod was started from an Emacs shell
  buffer, so launchd owns nothing, `kickstart -k` kills nothing and then blocks,
  and the SIGTERM is a later kill landing on an already-hung command. The
  two-site lead is refuted rather than unsupported, and site B stands alone.

  **The stale-pid class has a floor, and stating it needs no measurement.**
  mg-cbc3 measured this box recycling its whole pid space in minutes and drew
  the consequence that "any construction that holds a pid and signals it later
  reproduces the recorded shape". True, and it eliminates nothing — every
  watchdog in the tree captures a pid and fires later. But a pid has ONE owner
  at a time, so a killer holding a STALE pid captured that number while a
  DIFFERENT process owned it, necessarily **before the current owner was born**:
  its hold, capture to signal, is **at least the victim's age at the kill**. No
  allocation policy is assumed, so it holds off darwin too. On darwin it is
  strictly stronger — pids are issued sequentially and reused only after a full
  wrap (measured: 40 consecutive spawns, 13487..13526, `+1` each; 1,390 pids in
  10s = 139/s, a ~719s wrap at that load) — so the hold must also cover a wrap.

  **Applied to site B, one candidate survives and it is inside the gate.** The
  victim is the tmpdir guard, started by the FIRST gate step, so ≈80s/173s/259s
  old at the three kills. Against that floor: `kill_tree`'s own per-node gap is
  one `ps -ax` + `awk`, **measured 0.01s** at 862 live processes; `StopServer`
  and `reapOrphanedWatchers` signal within the same call; `netc_probe` and
  `probe_tcp` hold 5s; `run_bounded`'s normal-path race is a scheduling gap; the
  literal bounds passed to `run_bounded` in test bodies are 30, 2 and 0; and
  `on_deadline`'s backstop holds 120s, which reaches the 85s occurrence and
  neither of the other two. **The one row that clears the floor is
  `run_bounded "$GIT_TIMEOUT"` reached from inside a gate step**:
  `scripts/pogo-deploy_test.sh` is itself a gate step and executes the deploy
  runner seven times, **five of them without overriding
  `POGO_DEPLOY_GIT_TIMEOUT`**, so they run at its **300s** default and arm a
  watchdog whose payload is `kill_tree "$p" TERM` — the recorded leaves-first
  SIGTERM subtree walk, with a hold that clears the floor for all three.

  **Carried as a candidate, not a finding.** That 300s hold is only realised if
  the watchdog is ORPHANED; on the normal path the parent kills it within
  milliseconds of `wait` returning, and the floor excludes that. Not established
  here: whether those five invocations reach a git step at all, and whether a
  `run_bounded` watchdog is ever left at `ppid 1` during a gate run. The second
  is directly samplable and the sampler was confirmed against a deliberately
  orphaned control before this was written.

  `gatesignal.go` reports this class as `BOUNDED`, states why the floor holds so
  a reader can apply it to a candidate this repo has never heard of, and gives
  the run's elapsed as a **ceiling** on the victim's age rather than as the
  floor — a process spawned late in a run is younger than the run, so quoting
  the elapsed as the floor overstates the bound and discards a real candidate.

- **`grep -r` under `~/.pogo` returns a clean, exit-1 EMPTY answer — the sweep
  that would find an off-repo sender could not have found one (mg-baf3).**

  Measured, same pattern, same root: `grep -rlI kill_tree ~/.pogo/` returns **0
  files at exit 1** where `/usr/bin/grep` returns **80**. Positive control —
  pointed at `~/.pogo/bin/` instead of the root, the two agree at 2 files each,
  so the pattern is right and the tree is readable. The cause is that `grep` in
  an agent's shell is a function from the harness's shell snapshot that execs
  `ugrep --ignore-files`, and `~/.pogo/.gitignore` is an allowlist that opens
  with `/*`; `git check-ignore -v` names the rule (`.gitignore:19:/bin/*`) that
  hides the deployed `pogo-deploy.sh` launchd actually runs.

  This matters here because `~/.pogo` is where the fleet's live non-repo code
  lives — the deployed runner, the reminder pollers, `bridget-supervise`, every
  agent prompt — and it is exactly the space you sweep once the repo has no
  sender in it. Every negative taken there with the default `grep` is void. The
  sweep is redone with `/usr/bin/grep` in the investigation: the repo still
  ships exactly one leaves-first SIGTERM subtree walk, no userspace OOM daemon
  runs on this box, and `com.pogo.reclaim` — mg-19e4's unverified `pgrep -x go`
  — **is not installed**, so it cannot be the sender. The in-repo enumerations
  behind the table above were re-taken with `/usr/bin/grep` and agree with the
  wrapper's counts, so the remedy does not exhibit the defect it documents.

  Full record: `docs/investigations/gate-sigterm-site-b-2026-09-08.md`.
