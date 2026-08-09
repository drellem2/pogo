# Where the merge gate's time actually goes — a per-step profile

**Work item:** mg-eed9 · **Measured:** 2026-08-09/10, host `darwin 24.6.0`, 10
cores (`hw.ncpu` = `hw.physicalcpu` = 10) · **Branch:** `polecat-qeed9`

Daniel, 2026-08-09: *"i think we need to prioritize build times. i'm open to
different strategies."* The ticket enumerated five candidate strategies (A
select-by-what-changed, B move-live-suites-to-a-schedule, C hermeticity-first,
D parallelise-the-shell-suites, E raise-the-cap) and then explicitly declined to
rule between them, because **nobody had a per-step breakdown**. It asked for the
profile first and forbade starting with a fix.

This is the profile. It changes the ranking of those strategies, and it refutes
one premise that all five were resting on.

## What was measured, and how

`scripts/lib/gate-profile.sh` wraps every step of `test.sh` and `build.sh` in a
`gate_step` that records **wall-clock**, **CPU seconds of the reaped process
subtree** (bash's `times`, read through a file — a `$(times)` capture forks and
silently reads zero), and the **host 1-minute load average at step start**.
`scripts/go-test-budget.sh` additionally reports, for the Go suite, how many
packages ran, how many Go served from cache, and the ten slowest.

Two consecutive full `./build.sh` runs on the same tree, no Go source change
between them:

* **Run 1** — the first gate run in this polecat worktree. Aborted in the Go
  step (see [What run 1 caught](#what-run-1-caught)), so only that step and
  `fmt.sh` have timings.
* **Run 2** — immediately after, complete, exit 0. This is the 20-step table.
* **Run 3** — a third run on the finished tree, fully warm. Reported in
  [Run 3](#run-3-the-warmest-case-makes-finding-2-larger-not-smaller); it is the
  same shape as run 2 with the Go step warmer still, and it moves the live trio
  to ranks 1-2-3.

The host was otherwise busy with the ordinary fleet: six crew agents, and at
least two *other* concurrent `go test` runs were observed during these windows
(distinct `go-build*` temp dirs in `ps`). Load averages in the tables are what
they are; this was not a quiet box.

## The 20-step table (run 2)

```
GATE STEP PROFILE — test.sh: 20 steps, 286.89s wall, 180.62s cpu

   rank       wall    share        cpu    cores    load  step
      1    113.84s    39.7%    121.69s     1.07    4.56  Testing Go packages
      2     51.12s    17.8%      2.59s     0.05    2.74  Testing the pogod condition annunciator (live controls: negative + A2)
      3     42.25s    14.7%      8.28s     0.20    3.30  Testing pogo-self-deploy live mail-check control
      4     25.90s     9.0%      2.59s     0.10    2.76  Testing the live control's sandbox isolation and setup-failure reporting
      5     11.45s     4.0%      2.27s     0.20    3.27  Testing pogo-deploy nightly trigger
      6      9.76s     3.4%     15.34s     1.57    3.16  Testing the Go per-package test budget and overrun report
      7      6.06s     2.1%      0.91s     0.15    4.61  Testing the revision probe's launchd arming
      8      4.38s     1.5%      3.31s     0.76    3.16  Testing pogo-self-deploy driver
      9      4.21s     1.5%      5.96s     1.42    3.30  Testing the packaged test isolation (scripts/pogo-sandbox)
     10      4.15s     1.4%      1.09s     0.26    4.24  Testing the gate's per-step profile
     11      3.47s     1.2%      2.41s     0.69    4.75  Testing the external redeploy revision probe
     12      2.29s     0.8%      1.56s     0.68    3.90  Testing changelog coverage check
     13      1.78s     0.6%      2.42s     1.36    3.30  Testing pogo-self-deploy SIGINT interrupt-safety control
     14      1.72s     0.6%      7.16s     4.16    3.16  Testing the from-source staleness runner
     15      1.66s     0.6%      1.36s     0.82    3.90  Testing build.sh
     16      1.21s     0.4%      0.61s     0.50    3.66  Testing changelog release-roll and link references
     17      0.64s     0.2%      0.56s     0.88    3.90  Testing changelog fragment assembler
     18      0.54s     0.2%      0.43s     0.80    3.66  Testing work-item scope guard
     19      0.21s     0.1%      0.07s     0.33    3.16  Testing bash shell integration
     20      0.01s     0.0%      0.01s     1.00    3.16  Testing neovim plugin
```

And the three phases `build.sh` owns:

```
GATE STEP PROFILE — build.sh: 3 steps, 288.07s wall, 186.07s cpu
      1    287.00s    99.6%    180.89s     0.63    4.56  test.sh
      2      0.76s     0.3%      4.10s     5.39    3.66  go build ./cmd/...
      3      0.27s     0.1%      1.08s     4.00    4.56  fmt.sh (go fmt ./...)
```

**`fmt` and the compile are not the problem and never were: 1.03s of 288.07s,
0.4%.** They are also the only two phases that use the machine — 5.39 and 4.00
cores. Everything below is about `test.sh`.

## Finding 1 — the gate is ~94% idle. It is not a compute problem.

`test.sh` spent **180.62 CPU-seconds over 286.89 wall-seconds** = **0.63 cores
busy, on a 10-core box — 6.3% of the machine.** Run 1's Go step, the heavier of
the two, managed 0.93 cores (9.3%).

This is the ticket's own opening observation (`cpu: 0.0 cores busy` on a 7m19s
gate) generalised: **it is not an artifact of one bad night, it is the steady
state.** The gate is a wall-clock artifact, not a compute artifact.

That single number reorders the strategies. Every remedy shaped like *do less
work* — selection, caching — is attacking the 6%. Every remedy shaped like
*stop waiting* is attacking the 94%.

## Finding 2 — three live-daemon suites cost more than the entire Go suite, at 1/9th the CPU

| | wall | cpu | cores | share of `test.sh` |
|---|---|---|---|---|
| Ranks 2 + 3 + 4 (the live-daemon trio) | **119.27s** | 13.46s | **0.11** | **41.6%** |
| Rank 1 (all ~70 Go packages) | 113.84s | 121.69s | 1.07 | 39.7% |
| The other 16 steps combined | 53.78s | 45.47s | 0.85 | 18.7% |

Three suites — `pogo-condition-controls.sh NEG A2`, `pogo-self-deploy_live_test.sh`,
`pogo-self-deploy_live_setup_test.sh` — **outweigh the whole Go test suite**, and
they do it at **0.11 cores**: 13.46 CPU-seconds spread over 119.27 seconds of
wall-clock. They are daemon boots and their waits. `test.sh`'s own comments
already price two of them ("costs ~40s — pogod holds its mail-check reap for 30s
after boot"; "the full file is 15 daemon boots") — what was not known is that
together they are the **largest single line item in the gate**.

This is the ticket's strategy **B**, and the profile promotes it from "cheapest
to try" to **the highest-value change available**, for a reason the ticket did
not have: their cost is almost entirely *not on the host*. Moving them off the
merge path returns ~42% of gate wall-clock while freeing ~0.11 cores — i.e. the
gate gets much faster and the fleet's compute contention barely changes. The
precedent already exists in this same file (the remaining condition-control rows
are excluded on exactly this reasoning), and the decision it needs is not
technical: it is an explicit statement of **what we accept catching at the
nightly instead of at the merge**.

### Run 3 — the warmest case makes Finding 2 larger, not smaller

```
GATE STEP PROFILE — test.sh: 20 steps, 230.15s wall, 101.12s cpu
      1     72.41s    31.5%      9.10s     0.13    5.11  Testing pogo-self-deploy live mail-check control
      2     51.05s    22.2%      2.76s     0.05    3.74  Testing the pogod condition annunciator (live controls: negative + A2)
      3     25.61s    11.1%      2.36s     0.09    4.37  Testing the live control's sandbox isolation and setup-failure reporting
      4     25.56s    11.1%     39.70s     1.55    5.65  Testing Go packages
      5     11.57s     5.0%      2.35s     0.20    4.35  Testing pogo-deploy nightly trigger
      6     10.44s     4.5%     16.38s     1.57    4.50  Testing the Go per-package test budget and overrun report
```

Go packages: 6 ran, **56 cached**, 37.8s of package time.

With the cache fully warm the three live-daemon suites are **ranks 1, 2 and 3**:
**149.07s of 230.15s — 64.8% of `test.sh` — at 14.22 CPU-seconds, 0.095 cores.**
The entire Go suite is 11.1%. The whole file runs at **0.44 cores of 10, 4.4% of
the machine**.

The direction matters: every improvement to the Go step (caching, selection)
makes the live suites a *larger* share, because they are the part that does not
respond to any of it. They are wall-clock floors.

One caution the three runs also supply: the live mail-check control measured
**42.25s** in run 2 and **72.41s** in run 3, a 1.7× spread on an unchanged tree.
These suites wait on daemon boots, so their cost is variable as well as large —
which is another reason the `load` column is now recorded on every row rather
than reconstructed afterwards.

## Finding 3 — the Go suite's wall-clock is set by ONE package, and selection only pays if it excludes that one

| | run 1 (cold) | run 2 (warm) |
|---|---|---|
| Go step wall | 322.71s | 113.84s |
| packages ran / cached | 61 / **1** | 7 / **55** |
| sum of per-package time | 640.6s | 148.0s |
| slowest package | `internal/agent` **319.19s** | `internal/refinery` **109.34s** |
| slowest as share of the step's wall | **98.9%** | **96.0%** |

In both runs the slowest single package accounts for **essentially the whole
step**. Everything else fits inside its shadow — `go test` already parallelises
by package, so the other 60 packages are free.

`internal/agent` was sampled repeatedly by `ps` while it held run 1: **0.0–1.0%
CPU** across five samples over four minutes. The package that defines the Go
suite's duration is *waiting*, not computing.

The consequence for strategy **A** (select by what changed) is sharp and was not
visible before: **selection buys nothing unless it excludes the slowest package.**
Skipping 60 fast packages saves seconds. The dependency map — which the ticket
correctly names as "the whole difficulty", and where a wrong edge ships a
defect — has to be right about *one* edge to pay, and that edge is the one into
the most connected package in the tree. That is a poor risk/reward, and the
profile says so with numbers rather than with an opinion.

## Finding 4 — caching works. The fleet is just never in the cached case.

Two identical consecutive runs, no Go source change: **1/61 cached → 55/62
cached**, Go step **322.71s → 113.84s**. Caching is worth **209s, a 65%
reduction of the largest step** — when it hits.

The gate does not get that. It runs **at least twice per merge in two different
trees** (the polecat's worktree, then the refinery's gate checkout), plus once
more on every retry, and each of those is a *first* run in its tree. Run 1 — the
1/61 run — is the fleet's normal case; run 2 is the case nobody is ever in.

> **Unverified: the mechanism.** This measures *that* a first run in a fresh
> tree misses and a second hits, not *why*. The obvious candidate is that these
> tests open files by absolute path and read ambient state, which Go folds into
> the test-cache key and which differs per worktree — that would also be the
> ticket's own hermeticity thesis, and it would mean strategy **C** buys the
> cache back for every gate rather than only for reruns. It is a cheap next
> measurement (`go test -x`, or diff the cache keys between two worktrees) and
> it should be made before C is costed. Do not quote the mechanism as
> established; the 1/61 → 55/62 numbers are what was measured.

## Finding 5 — the concurrency cap may be guarding the wrong signal

During run 1's Go step, the host's 1-minute load average was observed at
**40.60** while that same step averaged **0.93 cores busy of 10**. Load average
on darwin counts runnable *and* uninterruptible processes, so a suite that
spawns hundreds of short-lived processes and blocks on I/O and daemon boots
drives load hard while leaving the CPUs idle.

mg-3977 set the per-repo cap at 3 because "what saturates is one repo's test
suite run concurrently". **What saturates is measured here as not being CPU.**

This is stated as an observation, not a recommendation. Strategy **E** (raise
the cap) is still not something to do on this evidence alone — the load-337
incident is real, and what a repo's suite contends *for* (file descriptors,
ports, the live daemon, mailboxes, worktrees) is exactly the non-hermetic
surface mg-5551 is about. But the cap's stated justification and the measurement
disagree, and that gap should be closed deliberately rather than inherited.

## The corrections to the ticket's own description

The ticket describes an 8-step `test.sh`. It has **20 steps** — 2.5× — and had
19 before this work item added its own. Two specific corrections:

* **`scripts/pogo-condition-controls.sh` is NOT excluded from every merge.** The
  ticket says it is ("~3min is ALREADY excluded"). The `NEG A2` subset runs on
  every merge, and it is **rank 2 in the profile at 51.12s** — the single most
  expensive suite after Go. Only the remaining rows (A4/A7/A11, A5, A9, A10,
  A14) are on demand.
* **One gate step is a no-op on this host.** `Testing neovim plugin` takes
  0.01s and prints `SKIP: nvim not found in PATH`. It is not costing anything;
  it is also not testing anything here, which is worth knowing before anyone
  counts it as coverage.

## What run 1 caught

Run 1 failed, and the failure is worth recording because it is the profile's
first real output and it landed on the profiler itself.

`internal/testsandbox`'s `TestEveryTestSuiteRoutesThroughTheIsolation` refused
the new `scripts/gate-profile_test.sh`: it did not route through
`scripts/pogo-sandbox`, so it would have read and written the developer's live
`~/.pogo`. The instrument built to measure a problem whose stated root cause is
*48 suites reading the developer's live state* was, on its first run, a 49th.
It was converted rather than ledgered (the ledger is a ratchet and may only
shrink); the adoption comment in that file says so out loud.

The failing run also exercised the part of the instrument that is hardest to get
right: `set -e` aborted at step 1 of 20, and the profile still printed, with the
row marked `[FAILED]`. That is the case the report is armed on `EXIT` for.

## Ranked conclusion

1. **B — move the live-daemon suites off the merge path.** **41.6% of gate
   wall-clock warm, 64.8% fully warm, at ~0.1 cores.** Largest win, lowest
   compute cost, precedent exists in the same file — and the share *grows* as
   every other lever succeeds, because these suites respond to none of them.
   Needs a ruling on what we accept catching at the nightly.
2. **C — hermeticity.** Now has a measured prize attached (Finding 4: 65% of the
   largest step, currently forfeited on every gate run) rather than only a
   quality argument — *pending* the mechanism check flagged as unverified above.
3. **A — selection.** Demoted. Pays only if the map is right about the single
   slowest package; skipping the other 60 is worth seconds (Finding 3).
4. **D — parallelise the shell suites.** The 16 non-Go, non-live steps total
   53.78s (18.7%); the live trio is already the B item. Small, and blocked by C
   wherever suites share live state.
5. **E — raise the cap.** Not on this evidence, but its stated premise is now
   known to be inconsistent with the measurement (Finding 5) and should be
   revisited rather than inherited.

Nothing in this work item makes the gate faster. It was not supposed to: the
ticket asked for the profile first, and the profile is now permanent — every
gate run prints this table, with a `load` column, so the distribution under
fleet load accrues from ordinary runs instead of having to be requested in
advance.
