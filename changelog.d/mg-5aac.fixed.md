- **Two tests that failed branches which had never touched them are now
  independent of what else is running on the box (mg-5aac).**
  `TestAttachRebindsAfterSocketFileReplaced` and
  `TestProbeGoesRedAgainstAConstructedOrphan` failed a merge gate on a 12-file
  branch containing zero lines in either package, and the author spent a full
  analysis proving that negative. Both are fixed, both were reproduced first, and
  both were re-run under the condition that produced them.

  **The variable is contention in the measurement window, not the host's load
  average**, and the distinction decided the remedy. The probe passes 6 of 6
  isolated at load 47 and 0 of 2 inside `go test ./...` on the same box, so a fix
  keyed on load — skip above N, scale a deadline by uptime — would have skipped a
  quiet-but-contended gate and fired on a busy idle one.

  **orphanwatch was asserting an absolute CPU floor over a shared resource**,
  which is unmeetable by construction under contention (the shape of mg-6c90).
  The probe ran its RED arm at `DefaultFloor`, 0.20 cores. What a shell burner
  earns is the host's to decide — N runnable processes on C cores get about C/N
  each — so on this 10-core box one burner cleared 0.20 by only 1.2-1.45x even
  isolated, and by nothing at all under a gate. The failure was not a skip: the orphan was
  attributed *correctly* — right cwd, right owner, right liveness answer — and
  only the magnitude comparison fell short, landing it in `below_owner_floor`,
  which is a **verdict** bucket. The test read `Reported == false` and reported
  the detector broken.

      isolated, load 53          0.24 - 0.29 cores    6 of 6 passed
      + 30 competing processes   under the floor      4 of 4 FAILED
      inside go test ./...       0.105 - 0.160 cores  every sample under 0.20

  Every in-gate sample lands in `[0.105, 0.160)`. That interval is entirely below
  0.20, so in-gate this was **constant, not flaky**. The probe now runs the scan
  at its own `probeFloor` of 0.05 — 2.5x the candidate floor, so a process
  reaching it is measured rather than rounded, and low enough that one burner
  clears it to a load of about 200 here. What that gives up is stated in the
  code: this probe no longer witnesses the *value* 0.20, which it was never the
  instrument for, and the unit tests pin that arithmetic against a stubbed table.
  What it uniquely witnesses — real process table, real cwd reader, real
  attribution, real liveness answer, RED at the end — is unchanged. And
  `below_owner_floor` is now a **blind** bucket beside `cwd_unreadable`, so a host
  that starves the burner past even 0.05 makes the probe skip rather than accuse.

  The remedy considered and **rejected**: construct more burners under the dead
  owner until their sum cleared 0.20. It works arithmetically — two clear it at
  load 84 — but it answers host contention by adding host contention, inside a
  merge gate. mg-c675 is a branch failed by exactly that.

  **A process that cleared no floor had no observable magnitude anywhere in the
  report**, which is why the failing run described a provably-spinning process as
  `0.00 cores`: `Orphan.Cores` rides only on findings. `Report.Rates` now carries
  the measured rate of every busy pid, keyed the same as `Dispositions`, so "under
  the floor" and "not running" stop being the same reading — only one of them is
  a fact about the host, and telling them apart is the constructive probe's whole
  job. Its unit test asserts the arithmetic (50ms of CPU over a 1s window is 0.05
  cores) rather than the map's presence, because a map full of zeroes would
  satisfy a presence check while reinstating the reading it replaced.

  **The attach test's load sensitivity was in its fault INJECTION, not in a
  deadline** — worth knowing, because it presented as the intermittent one. It
  replaced the socket with `os.Remove(path)` followed by `net.Listen(path)`.
  Between those two statements the path names *nothing*, and the supervisor polls
  that same path every 10ms in this test: landing in the gap it reads
  `socket_file_missing`, rebinds, recreates the file — and the test's own foreign
  bind then loses with `EADDRINUSE`, having injected no fault at all. The window
  is microseconds on a quiet box and widens with contention. Measured: widening
  it to 30ms fails **10 runs out of 10**.

  It is now a bind at a sibling path plus an `os.Rename` over the target, which
  closes the window rather than narrowing it — the path names the agent's own
  socket right up to the instant it names the foreign one, so
  `socket_file_missing` is unreachable and the only reason the supervisor can see
  is the one under test. The same 30ms gap now passes 10 out of 10.

  The wall-clock bounds in that file (2s and 3s against a supervisor ticking every
  10ms) were a real load sensitivity even though they were not this failure, and
  are now a single 30s backstop, shortened when the test binary's own deadline
  would arrive first so the assertion rather than `go test -timeout` reports the
  failure. On a quiet host these waits return in single-digit milliseconds, so the
  bound costs nothing there and costs only patience on a host that is merely slow.

  **Acceptance was run, not asserted.** Both suites green under `go test ./...`
  contention plus 40 additional competing processes — 20 iterations of all six
  attach-listener tests, and 3 of 3 probe runs granted 0.120-0.140 cores. The load
  generators were cleaned up from a `trap` armed before they started, naming
  `EXIT INT TERM HUP` and recording their own pids, because `jobs -p` is empty in
  a non-interactive shell and a bare `EXIT` does not fire on SIGTERM (mg-c675).
  Nothing outlived its run.
