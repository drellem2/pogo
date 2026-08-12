- **A full boot volume no longer surfaces as "main is broken": `com.pogo.reclaim`
  reclaims the Go module cache on a SIZE trigger, and states what it does not
  fix (mg-b7c3).** On 2026-08-12 this host measured `460Gi / 422Gi used /
  571Mi free / 100%` with a 7.3G module cache, and `./build.sh` failed at the
  link step with `no space left on device` across ~40 packages. `./build.sh` is
  the refinery's merge gate, so at that fill every merge was one build from
  failing. The cost was not the outage — it was that a full volume presents as a
  compile error naming specific packages, which reads like a broken branch.

  **Expect this job to do nothing on this host, and expect that to be correct.**
  The volume sits at ~99% capacity while the module cache sits at ~680M, so
  every sample exits **4 (CANNOT HELP)** — the free-space arm is satisfied
  continuously and the cache arm never is. That is the true answer, not a
  misconfiguration: deleting 680M off a 415G fill returns 680M and costs a full
  re-download. It starts acting when either input changes — the cache
  accumulates past 5G, or someone reclaims part of the ~414G it does not own.
  That is measured rather than hypothetical: the volume was watched dropping
  1.3 GiB in 28 minutes on 2026-08-12 with the module cache **unchanged at
  680M** throughout, while `~/.pogo/polecats` (4.3G, 2.6G of it one stale
  worktree), `~/.pogo/refinery` (2.1G) and `/var/folders/.../go-build*` held the
  space.

  **There are two Go caches on this box and they differ by a factor of fifty.**
  `~/go/pkg/mod` is 680M and is what this job reclaims; `~/Library/Caches/go-build`
  is **34G** and is not touched by it — `go clean -modcache` does not reach the
  build cache. "The Go cache is large" is ambiguous and the larger reading is the
  one nothing here addresses, so it is named in the script header, in the README,
  and in every scope note the job prints. Leaving it alone is deliberate:
  `go clean -cache` would make the next `./build.sh` — the refinery's merge gate —
  recompile the world, trading a disk problem for a gate-latency problem on every
  merge. Reclaiming polecat worktrees is likewise out of scope; that is `gitgc`'s
  job and it has a live-agent witness, and a cron deleting a worktree under a
  running polecat is a worse failure than a full disk.
  The reasoning is repeated at the top of `pogo-reclaim.sh` and of the README
  section, because an exit code whose normal state looks like a failure needs
  its explanation upstream of the log line: the person diagnosing it is reading
  the code, not the output.

  `pogo service install-reclaim` installs a fourth LaunchAgent that samples every
  30 minutes and runs `go clean -modcache` when **both** floors are crossed:
  free space below 20G **and** cache above 5G. The two arms are not redundant.
  Free space is what maps to the observed damage; cache size is what maps to what
  the reclaim can actually return. **Free-space alone would reproduce this
  ticket's own defect inside its fix** — it fires on a full disk whose cache is
  small, deletes almost nothing, exits 0, and writes a line that reads like the
  disk was handled. Cache-size alone throws away a cache that costs a network
  round to rebuild on a box with 300G free.

  The cache floor is set against a measurement, not a hunch: after a manual
  clean the cache came back to **680M after one build and was flat across three
  readings spanning a full gate run** — so the plateau holds under load, not
  only at rest. That is enough to design against and not enough to call a
  measured steady state, and it says the pre-clean 7.3G was mostly stale
  accumulation rather than live working set — so 5G (~7.5× that reading) is
  reachable only by accumulation, and the 7.3G that produced the ticket would
  have fired.

  **launchd has no size trigger**, so the schedule is a *sampler* and the size is
  the trigger. That is affordable only because the measurements are ordered
  cheap-first: one `df` per fire, and the `du` of the cache only once `df` has
  established the disk is low. A nightly `StartCalendarInterval` was the
  alternative and is worse here — the volume went from healthy to 571 MiB free
  inside a working day.

  **It defers while something is building.** `go clean -modcache` deletes module
  trees a running build is *reading*, so a racing build does not get slower, it
  fails with a missing-file error — this ticket's complaint, caused by its fix.
  Two limits are stated in the log rather than assumed away: the check cannot see
  a build starting one second later, and when the check *cannot be made* (daemon
  down, no `pogo` on PATH) the job proceeds and logs `in-flight check PARTIAL`,
  because deferring forever on an unanswerable question is how the disk fills.
  Below 2G free the deferral is skipped outright — the in-flight build fails
  either way.

  **What it does not fix is written into every path that could be mistaken for
  "handled",** computed from the run's own numbers so it cannot go stale. A
  successful reclaim prints a `WHAT THIS DOES NOT FIX` block with the post-run
  fill and the measured consumer list (Library 73G, tools 15G, chrome 12G,
  research 9.8G, go 8.0G, .pogo 6.4G, Virtual Machines 5.0G, dev 4.6G), and mails
  `human` if the volume is still under the floor. A low disk with a small cache
  does not fire at all: it exits **4**, logs `the Go module cache is NOT what is
  filling this volume`, and mails `human` (rate-limited to once per 24h) — which
  is the state this host is in today, for the reason given at the top.

  Three notes on the fix being an artifact of the kind it remedies:

  - The percentage it prints is `df`'s Capacity — `used/(used+available)`,
    rounded up — not `used/total`. Running the real script against the real box
    caught it reporting **90%** where `df -h` says **99%**: on APFS the volume
    reserves space, so the two denominators differ by nine points, and 90% reads
    as comfortable. Pinned by a test that reproduces the APFS shape.
  - `~/.pogo/bin/pogo-reclaim.sh` is a **static copy** — a merge does not refresh
    it. Every fire logs `runner: <path> (mtime …)` so a log can answer which copy
    ran, and `com.pogo.reclaim` is registered in `managedLaunchAgents()` so
    `pogo doctor` audits the installed plist (including `StartInterval` drift)
    against what the build renders. The audit covers the plist, not the script;
    the script's answer is the `runner:` line.
  - Nothing that fails to measure renders as a pass: no `go`, an unreadable `df`
    or an unreadable `du` all exit **3 UNKNOWN**, and an unmeasured cache is
    distinguished in words from a cache found innocent.

  59 shell assertions (`scripts/pogo-reclaim_test.sh`, wired into `test.sh`) drive
  the real script against recording stubs on PATH, plus 10 Go tests over the
  installer and the audit registration.

  **Merging this does not install it.** Three things have to happen and only the
  first is a merge: the branch lands; the `pogo` binary is rebuilt onto a
  revision carrying `install-reclaim` (the nightly deploy); and somebody runs
  `pogo service install-reclaim`. Until then no plist exists and no sample has
  ever run — which `pogo doctor` reports as **absent** with the remedy attached,
  because "shipped" and "armed" are different states and the box should be able
  to say which one it is in.
