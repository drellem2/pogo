- **The mg-61a0 hangup guard decided a product guarantee from `kill(pid, 0)`,
  which answers `true` for a running process, for a corpse awaiting reap, and
  for an unrelated process that was handed the same number — and reported
  `REGRESSION` for all three (mg-bec4).** Only the first of those is "the
  polecat outlived pogod". The other two are "the polecat died", wearing the
  same reading.

  **This is mg-1a23's defect one step further in**, and that item's fix lands
  here with it: `requirePolecatCanReceiveSIGHUP` refuses to judge when SIGHUP is
  ignored in the process that ran `go test`, because SIG_IGN survives fork AND
  execve and reaches `sleep 600` itself. mg-1a23 also made the other two cases
  *legible* by printing `ps` on the failure path. Legible is not ruled out: it
  still takes a human re-reading output whose headline says `REGRESSION`.

  **What the wait now does.** The polling loop is unchanged and costs what it
  always did — `kill(pid, 0)` every 20ms. Only when the bounded wait expires
  with the pid still answering, the one moment a caller is about to announce a
  regression, does `waitPolecatGone` pay for two `ps` reads: the pid's start
  time (a value that is not the one captured at spawn means the number was
  recycled and ours is gone — the discrimination `internal/agent/witness.go`
  already makes by name for mg-13a3) and its process state (`Z` is a corpse).
  Cheap poll, expensive adjudication, once. Polling the adjudication instead
  would spend ~1,000 `ps` forks per call, in a test whose reported failure mode
  is a whole-tree run.

  **The zombie case is measured, not argued** (macOS 15.6, 2026-08-20): for a
  child killed and not reaped, `kill(pid,0)` succeeds, `ps -o stat=` prints `Z`,
  `command=` prints `<defunct>` — and `ps -o lstart=` still prints the ORIGINAL
  start time, so the start-time comparison matches and cannot catch this case.
  That is why the check reads both and not either. It matters because the
  polecat is orphaned by construction: pogod is SIGKILLed, the polecat
  reparents, and whether launchd reaps the corpse inside the guard's 10s bound
  is not something pogo controls or promises.

  **The negative control is the load-bearing test.** Every adjudication added
  here buys a new way to report GONE, and a guard that reports GONE too eagerly
  is worse than the one it replaced — mg-61a0 exists because a live,
  unregistered polecat gets swept. `TestPolecatWaitDoesNotCallALivePolecatGone`
  pins that a running, unrecycled, non-zombie process comes back not-gone
  through both entry points, including the degraded `startKnown=false` path,
  which must lose precision rather than invent a verdict.

  **Both new adjudications were shown RED before they were believed**, and the
  first attempt at the zombie one failed that check in the most instructive way:
  with `procIsZombie` stubbed to `false` the test PASSED, in 10.4s, as a `t.Skip`
  — because it had staged the corpse using the same call it was testing. The
  oracle now reads `command=` (`<defunct>`) and the instrument reads `stat=`, so
  the two can disagree, and the stub is a failure instead of a skip.

  **NOT CLAIMED: that either of these is what failed on main.** The ticket's
  hypothesis — that the full `go test ./...` run leaves SIGHUP ignored for the
  test binary — was tested here and **did not reproduce**. SIGHUP reads as
  default in every launch context reachable from a polecat on this box: a plain
  tool-call shell, a backgrounded one, `scripts/tmpdir-leak-guard.sh`, and a
  `&`-backgrounded job. It reproduces perfectly under `nohup` — 1,934 of 1,934
  consecutive runs refused with mg-1a23's diagnosis, which is the guard working
  and is how this harness caught its own contamination — but nothing in the
  gate's chain supplies it. Separately, 5,625 trials of
  `TestPolecatDoesNotOutlivePogod` run concurrently with a whole-tree
  `go test ./...` at 1-minute load averages from 4.8 to 35.9 produced **zero**
  failures. So what landed is not a root cause: it is the removal of two
  enumerated ways this guard can name a product regression it never measured.
  The next firing says which it is, or says it is neither.
