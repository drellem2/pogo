- **`log stream` wake watchers no longer accumulate one per pogod death
  (mg-55de).** A machine carrying **243** of them — 242 orphaned at PPID 1 —
  held one core at ~89% in `diagnosticd` and drove load averages to 90–120.
  Each orphan is a live subscriber to the unified logging system, so the cost
  is not an idle process: it is a subscription the OS services forever.

  **The reported cause was not the cause.** The ticket named
  `sleep_darwin_test.go`'s `Watch(context.Background(), nil)` as the emitter.
  `Watch` rejects a nil hook *before* it spawns anything, so that call cannot
  leak, and a before/after count across the sleep package's tests confirmed it:
  the count did not move.

  The actual mechanism is in the exit paths, not the spawn path. **pogod
  installs no SIGTERM handler**, so every way it dies — the routine
  `pogo server stop`, launchd's restart, `log.Fatal` on a bind failure,
  SIGKILL, panic, host crash — skips deferred functions (inventoried in
  `cmd/pogod/main.go`). The `defer hbCancel()` that `exec.CommandContext`
  relies on therefore *never runs on any path*, and the watcher reparents to
  launchd. pogod's other children do not survive this: agents die of the PTY
  hangup, and an ordinary child dies of SIGPIPE on its next write to the
  closed stdout pipe. This one writes only when the wake predicate matches,
  which is almost never — so it never writes, never takes SIGPIPE, and streams
  forever.

  That also explains the burst the ticket read as a steady drip: tests which
  boot a real pogod and SIGTERM it (`upgrade_boot_test.go`,
  `stallwatch_gate_test.go`) strand one apiece, so the leak tracked test
  activity rather than uptime. The test correlation was real; the proposed
  mechanism for it was not.

  darwin has no PDEATHSIG, so a child cannot ask the kernel to kill it when
  its parent dies. `Watch` now **reaps on start**: before spawning, it
  terminates watchers left at PPID 1 by an earlier pogod. Each boot clears the
  previous corpse, bounding the leak at one instead of unbounded accumulation,
  and it covers the SIGKILL/panic/crash paths no shutdown handler could. The
  production spawn path is unchanged — it was always correct.

  **Matching is exact, and the narrowness is the safety property.** A
  candidate must have PPID 1 (a running pogod's watcher is parented to that
  pogod, so it is never a candidate) *and* an argv equal to our own
  `log stream --predicate <wakePredicate>`. Kills are delivered one PID at a
  time. `pkill -f` is never used and must not be introduced here: every pogo
  poller idles in `sleep N` under `set -euo pipefail`, and a broad pattern has
  taken this fleet's mail pollers and watchdog down before.

  Verified by reproduction rather than inspection, since the code under
  suspicion read as correct: booting a real pogod and SIGTERMing it strands a
  watcher at PPID 1 every time, and
  `TestReapOrphanedWatchers_KillsOrphanSparesLiveWatcher` reconstructs exactly
  that — a genuinely orphaned watcher alongside a live parented one — then
  asserts the orphan dies and the live one survives. Both halves are
  load-bearing: without the orphan the test would police a leak it never
  observed, and without the live watcher the leak could be "fixed" by killing
  wake detection outright. `TestIsOrphanedWatcher_RejectsFleetProcesses` pins
  the matcher against `sleep 600`, pogod, claude, polecats, and
  substring-shaped near misses.
