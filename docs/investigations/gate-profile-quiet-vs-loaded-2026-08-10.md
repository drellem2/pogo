# The merge gate, quiet and under load — the pair mg-eed9 asked for

**Work item:** mg-db58 (successor to mg-eed9) · **Measured:** 2026-08-10 00:11–00:45Z,
host `darwin 24.6.0`, 10 cores · **Branch:** `polecat-qdb58`

mg-eed9 built the instrument (`scripts/lib/gate-profile.sh`) and took the first
profile. It then declared its own remainder in one sentence: *"time each step,
run it on a quiet host and again under fleet load."* mg-db58 is that pair, and
the ticket is explicit that **the second run is the point** — a profile taken
only on a quiet box measures the case nobody is complaining about.

**This document measures. It does not fix, and it does not rule between
mg-eed9's strategies A–E.** One finding below is an unambiguous defect and it is
recorded here as a finding, deliberately unrepaired, for the same reason.

## The runs

Every run is `./build.sh` in this polecat worktree with
`POGO_GATE_PROFILE_JSON` set. A load sampler ran alongside at 10s intervals, so
each run's conditions are recorded rather than reconstructed.

| run | cache | host | load (min/mean/max, sampled) | test.sh wall | test.sh cpu | steps |
|---|---|---|---|---|---|---|
| **r1** cold | 0/62 cached | 5 other polecats, ordinary fleet | 2.78 / **5.17** / 8.75 | 514.32s | 357.14s | 20, exit 0 |
| **r2** quiet | 56/62 cached | 5 polecats idle, **0 compile procs** | 1.68 / **2.51** / 3.28 | 252.11s | 99.13s | 20, exit 0 |
| **r3** loaded | 58/62 cached | r2 + synthetic contention | 78.38 / **137.82** / 205.18 | 468.87s | 66.46s | **15, FAILED** |

**On the load in r3, and why it is synthetic.** Natural fleet load tonight never
exceeded 8.75 — five polecats were resident but mostly *thinking*, not building,
so there was no natural contention to measure against. The contention in r3 is
therefore generated: 10 CPU busy-loops (core contention, what the Go step feels)
plus 14 fork/exec churn loops (runnable-queue pressure, which is how this host
reaches load 40+ while its CPUs idle). It overshot: I calibrated for ~30 and got
a mean of 138. That band is **not** invented — mg-6c90 measured its CPU-floor
test failing 13/13 at load 52–106, and this host has recorded 174 in a night —
but it is heavier than an ordinary busy evening, and every ratio below should be
read as *the high end*, not the typical case. A repeat at load ~40 was started
and **truncated by the scheduled predeploy stop before it produced a table**; it
is not in this document.

## The ranked list — quiet, loaded, ratio

`test.sh` totals: **252.11s quiet → 468.87s loaded = 1.86×** — and that
understates it, because the loaded run **aborted at step 15 of 20**. The five
missing steps cost 6.33s quiet; the completed loaded run would have been higher.

```
rank      quiet     loaded   ratio  q-cores  l-cores  q-load  l-load  step
   1     33.89s    172.33s   5.08x     0.12     0.04    1.83  200.63  Testing pogo-deploy nightly trigger
   2     73.18s     64.94s   0.89x     0.12     0.13    2.89  133.79  Testing pogo-self-deploy live mail-check control
   3     51.11s     56.27s   1.10x     0.05     0.04    2.36  140.38  Testing the pogod condition annunciator (live controls: NEG + A2)
   4     25.51s     32.64s   1.28x     1.53     0.35    3.11   81.48  Testing Go packages
   5     25.88s     30.21s   1.17x     0.10     0.08    2.02  126.47  Testing the live control's sandbox isolation
   6     10.07s     29.79s   2.96x     1.54     0.49    2.65  119.25  Testing the Go per-package test budget and overrun report
   7      4.24s     22.67s   5.35x     1.42     0.23    2.89  108.18  Testing the packaged test isolation (scripts/pogo-sandbox)
   8      4.17s     12.17s   2.92x     0.26     0.11    2.81  129.21  Testing the gate's per-step profile        [FAILED]
   9      4.48s     11.61s   2.59x     0.74     0.40    2.88  100.55  Testing pogo-self-deploy driver
  10      1.60s      9.73s   6.08x     3.62     0.34    2.45  116.36  Testing the from-source staleness runner
  11      1.80s      8.89s   4.94x     1.34     0.23    1.90  199.55  Testing pogo-self-deploy SIGINT interrupt-safety control
  12      5.96s      8.47s   1.42x     0.16     0.15    2.96  131.50  Testing the revision probe's launchd arming
  13      3.41s      8.09s   2.37x     0.72     0.42    2.70  131.54  Testing the external redeploy revision probe
  14      0.21s      0.40s   1.90x     0.33     0.27    2.88  100.55  Testing bash shell integration
  15      0.01s      0.01s   1.00x     1.00     1.00    2.88  100.55  Testing neovim plugin
  --  the loaded run never reached these five; quiet times shown for completeness  --
  16      2.30s          -      -       0.70        -    2.74       -  Testing changelog coverage check
  17      1.60s          -      -       0.86        -    2.74       -  Testing build.sh
  18      1.23s          -      -       0.52        -    2.74       -  Testing changelog release-roll and link references
  19      0.66s          -      -       0.88        -    2.74       -  Testing changelog fragment assembler
  20      0.54s          -      -       0.78        -    3.00       -  Testing work-item scope guard
```

`build.sh`'s three phases, quiet: `test.sh` 252.4s (99.6%), `go build ./cmd/...`
0.77s, `fmt.sh` 0.30s. mg-eed9's finding that fmt and the compile are ~0.4% of
the gate reproduces exactly.

## Finding 1 — the ratio inverts the ranking. The top of the quiet list is the *least* load-sensitive part.

Sort the quiet column and you get mg-eed9's answer: the live-daemon suites
dominate. Sort the **ratio** column and you get a different list entirely.

| | quiet share | ratio under load |
|---|---|---|
| The three live-daemon suites (ranks 2, 3, 5) | **59.6%** | **0.89× / 1.10× / 1.17×** |
| Everything else | 40.4% | 1.90× – 6.08× |

**The live suites are the most expensive steps and the most load-*stable* ones.**
The live mail-check control was *faster* under load 134 than under load 2.9
(0.89×) — it is dominated by a fixed 30s wait for pogod's mail-check reap, and a
fixed wait does not care what else the box is doing. The condition annunciator
went 1.10×. These are wall-clock floors in the strict sense: they are already
almost entirely *not on the host*, so contention has almost nothing left to take.

The steps that blow up are the small, CPU-doing ones: the staleness runner
6.08×, the sandbox isolation suite 5.35×, SIGINT 4.94×, the test-budget suite
2.96×. Every one of them has `q-cores` above 1.0 — they were genuinely computing
on a quiet box, and under contention they are the ones queued behind everyone
else. Look at their `l-cores`: 3.62 → 0.34, 1.42 → 0.23, 1.34 → 0.23. **They
were not given the cores.**

This has a direct consequence for the ruling, and it is the one thing this
profile adds that mg-eed9's could not see: **strategy B (move the live suites
off the merge gate) removes ~60% of quiet-host gate time and almost none of the
load sensitivity.** The two problems are in different steps. A ruling that
picks B on mg-eed9's evidence should know it is buying wall-clock, not
stability, and that the flakiness tickets (mg-5551 / mg-6092 / mg-6c90) live in
the rows B does not touch.

## Finding 2 — one step is genuinely pathological: 33.89s → 172.33s, 5.08×, at 0.04 cores

`Testing pogo-deploy nightly trigger` (`scripts/pogo-deploy_test.sh`) is rank 3
quiet and **rank 1 loaded by a factor of 2.7 over the next row**. It took
172.33s while using 6.30 CPU-seconds — **0.04 cores**. It is not computing and
it is not being starved of CPU; it is *waiting longer*, and 5× longer.

It is also the least stable step across every run I have: **11.45s** (mg-eed9's
run 2), **32.28s** (r1), **33.89s** (r2, quiet), **172.33s** (r3). A 15× spread
on an unchanged tree.

That combination — near-zero CPU, enormous variance, superlinear in load — is
the signature of a timeout or poll loop that is being hit rather than a
computation that is being slowed. **This is the single highest-value target in
the whole profile and it is not one mg-eed9's strategies name.** It is not in
the live-daemon trio (B does not move it), it is not a Go package (A does not
select it away), and it is one file (D's parallelism is irrelevant). Finding out
*what* it waits on is a small, bounded next measurement, and it should be made
before A–E are ranked. **Explicitly not diagnosed here** — this ticket profiles.

## Finding 3 — the gate has a load-sensitive assertion in the instrument mg-eed9 just landed

The r3 run **failed**, at step 8, and the failing assertion is
`scripts/gate-profile_test.sh` Test 3:

```
Test 3: Rows are ranked slowest-first
  FAIL: rank 1 is not the slowest step; row was:
        1      2.43s    68.3%      0.27s     0.11  129.21  BURNING STEP
```

The fixture runs a 1s `SLEEPING STEP` and a fixed-work `BURNING STEP`, and
asserts the sleep ranks first. On a quiet host the burner finishes in well under
a second, so it does. At load 129 the burner took **2.43s** and overtook the
sleep. Nothing is wrong with the branch; the assertion compares a *fixed amount
of CPU work* against a *fixed amount of wall-clock* over a **shared** resource,
and load moves one of them and not the other.

This is the fourth known member of the family in mg-eed9's own root-cause list
(mg-5551 live state, mg-6092 fails on operations, mg-6c90 an absolute CPU floor
— that one merged as b9e1d1b hours before this measurement). It is worth
recording precisely because of where it is: **the instrument built to measure a
problem whose stated cause is load-sensitive tests is itself load-sensitive.**
mg-eed9's run 1 caught its own suite reading live `~/.pogo` and converted it;
this is the same shape one layer down.

And it is the retry mechanism mg-eed9 costs the fleet on, caught in the act: at
load 129 this gate went red on a green tree, which under the refinery buys a
full re-run. **Not fixed here** — this ticket measures, and the fix belongs with
whoever rules on C. Filed for a successor.

## Finding 4 — cold cache costs more than heavy load does

The cache effect and the load effect are separable in these runs and they are
not the same size:

| | test.sh wall | Go step wall | Go packages cached |
|---|---|---|---|
| r2 quiet, warm | 252.11s | 25.51s | 56 / 62 |
| r1 **cold**, load 5.17 | 514.32s | **320.38s** | **0 / 62** |
| r3 warm, load 137.82 | 468.87s (15 steps) | 32.64s | 58 / 62 |

A cold Go cache is worth **+294.9s on one step**; load 138 is worth **+7.1s** on
that same step (1.28×). mg-eed9's Finding 4 said caching is worth 65% of the
largest step *when it hits*, and that the fleet is never in the cached case
because the gate runs twice per merge in two different trees. This run pair adds
the comparison: **the cache miss the fleet takes on every gate is roughly forty
times more expensive than heavy contention is, for the Go step.** If C
(hermeticity) does buy the cache back per-tree — mg-eed9 flags the *mechanism*
as unverified and it remains unverified here — that is the largest single number
on the table.

Note also the direction of `q-cores` vs `l-cores` on the Go step: 1.53 → 0.35.
Under load it is not doing less work, it is doing the same work with fewer
cores.

## What this does not decide

Nothing here ranks A–E, and nothing here was repaired. Findings 2 and 3 are both
things a reader will want to fix on sight; they are written down instead. Two
concrete next measurements fall out, both small:

1. **What does `scripts/pogo-deploy_test.sh` wait on?** 0.04 cores, 5.08× under
   load, 15× spread across four runs. Bounded, and it is the largest loaded row.
2. **Repeat the pair at load ~40.** r3 landed at a mean of 138 — real for this
   host, but the high end. The ~40 run was truncated by the predeploy stop.

## Reproducing

```bash
POGO_GATE_PROFILE_JSON=/tmp/gate.json ./build.sh    # any run, any host
```
The table prints on EXIT, including on a failed run, and carries the host load
average sampled at each step's start. That is mg-eed9's instrument; this
document is two readings from it and one comparison.
