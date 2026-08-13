# Host contention inflates a gate's wall-clock 1.8x–6.8x, measured

**Date:** 2026-07-30
**Work item:** mg-1b8c
**Host:** Mac14,12, 10 logical cores, Darwin 24.6.0

## What was asked

mg-1b8c reports that two compute-heavy polecats on one host inflate each
other's refinery gate runtimes by roughly **10x**, and that a fixed gate
timeout converts that into a merge failure attributed to the branch. The
acceptance is deliberately a **test** obligation, not a proof obligation: run a
known gate uncontended, run the same gate with a second heavy workload on the
host, and **state both numbers**. If the ratio is not materially greater than
1, the premise is wrong and that is the finding.

## Method

The measured unit is a fixed CPU-and-allocation sweep in Python — identical
work on every run, so a wall-clock difference is contention and not input. It
stands in for the numpy-heavy gate cases the original report measured; numpy is
not installed on this host.

Contention is applied as *N* CPU-bound "burner" processes, standing in for the
compute a heavy polecat runs. The live instance in the ticket measured **one**
polecat self-parallelised into three processes at ~5.7 cores, so a burner count
is a closer analogue of the real condition than a polecat count would be.

Harness, workloads, and raw output are reproduced at the bottom of this file.

**The host was not quiet.** Other fleet agents were live throughout — this is
the fleet's working machine and could not be quiesced. Every ratio below is
therefore a **lower bound**: the true uncontended cost is faster than the
0-burner arm, so the true inflation is larger than the number stated.

## Result 1 — the first, single-level run

| arm | wall-clock |
| --- | --- |
| uncontended (best of 2) | **31.3 s** |
| contended, 6 burners (best of 2) | **48.7 s** |
| ratio | **1.56x** |

Materially greater than 1, and nowhere near 10x. **6 burners plus the gate is
about 7 of 10 cores — the host was not oversubscribed**, so a small ratio is
the expected result at that level and says nothing about the reported instance,
which ran at load averages of 35–75. Measuring one contention level and
reporting a single ratio would have been the wrong shape of answer.

## Result 2 — the dose–response sweep

Same work, same tree, contention swept. Baseline repeated last to expose drift.

| burners | wall-clock | 1-min load average after | vs first 0-burner |
| --- | --- | --- | --- |
| 0 | 11.5 s | 13.89 | 1.00x |
| 6 | 20.2 s | 32.15 | **1.76x** |
| 14 | 47.1 s | 72.19 | **4.10x** |
| 24 | 56.5 s | 81.63 | **4.91x** |
| 40 | 78.5 s | 101.05 | **6.83x** |
| 0 (repeat) | 17.5 s | 80.88 | 1.52x |

**Finding: the premise holds, and it is a dose–response, not a constant.**
Wall-clock rises monotonically with oversubscription and reaches **6.83x** at
40 burners on a 10-core box, in the load-average range (35–101) where the
original ~10x was reported. The reported figure is not reproduced exactly and
is not contradicted: it is at the top of a curve this measurement climbs.

**Second finding, from the repeated baseline: the same work on the same tree
took 11.5 s and then 17.5 s with no burners at all — 1.52x of drift from
ambient fleet activity alone.** That is the ticket's condition occurring
incidentally, inside the experiment meant to measure it, and it is why the
numbers above are lower bounds.

## Result 3 — the load average is not the instrument to gate on

The ticket's follow-on note records a load average of 214 against roughly 7.5
of 10 cores in use, and asks that the fix not gate on `uptime`'s first column.
The sweep is consistent with that: load average moved from 13.9 to 101.1 across
the arms while total CPU consumed could never exceed 10 cores. On Darwin the
figure counts uninterruptible-sleep tasks as well as runnable ones, so it
tracks queueing — including I/O queueing — rather than CPU.

Two consequences, both now enforced in code:

1. **A guard keyed on load average refuses to dispatch while cores sit idle.**
2. **A share of the load is never the fleet's.** During these runs, non-fleet
   processes (a VPN extension, Spotlight indexing, `mediaanalysisd`) held on
   the order of 1–2.5 cores. A guard on total host load hands an unrelated
   process a veto over the fleet's own dispatch, and pausing fleet work does
   not give those cores back.

`uptime` was nonetheless a *sufficient* instrument for a coordinator to notice
something was wrong. Sufficient to look, insufficient to decide on. Both are
true and the design keeps the difference: `pogo host load` prints the load
average, last and labelled `CONTEXT ONLY`.

## Result 4 — the guard, run live, and the threshold it caught out

The shipped guard was exercised against the real host with a subtree the script
controlled standing in for the fleet, so both arms are live rather than
simulated.

**First attempt, with `FleetHeavyAt = 0.60`** — calibrated directly to the
ticket's live instance of one worker at ~5.7 of 10 cores:

```
ARM B: 7 compute processes under one root
fleet 5.8 of 10 cores across 7 procs, external 1.9, free 2.3 (loadavg 23.90)
PROCEED: the fleet is holding 5.8 of 10 cores (58%), below the 60% mark.
```

**It did not fire on the condition it was built for**, by two percentage
points. Calibrating a threshold to sit just under a single measurement leaves
it one point of jitter from missing that measurement. The number was changed to
**half the host**, which follows from an argument instead: below half, the
host's spare capacity and non-fleet work dominate and holding a dispatch would
be gating on something pausing our own work cannot fix; above half, the
marginal worker competes chiefly with fleet work, which is the only thing
dispatch controls.

**Re-run at 0.50, both arms:**

```
ARM A  ordinary, nothing heavy under the root
       fleet 0.0 of 10 cores across 1 procs, external 3.3, free 6.7 (loadavg 13.36)
       PROCEED: the fleet is holding 0.0 of 10 cores (0%), below the 50% mark.

ARM B  ONE notional worker, SEVEN compute processes
       fleet 7.0 of 10 cores across 7 procs, external 1.9, free 1.1 (loadavg 12.61)
       HOLD: the fleet is already holding 7.0 of 10 cores (70%) across 7 processes...

ARM A' after the heavy work finishes
       fleet 0.0 of 10 cores across 0 procs, external 2.4, free 7.6 (loadavg 11.76)
       PROCEED: the fleet is holding 0.0 of 10 cores (0%), below the 50% mark.
```

Three things this establishes that the synthetic tests cannot:

1. **Both arms, live.** The guard holds on a full host and does not on a free
   one, and it clears by itself when the work in flight finishes — which is
   what makes the 503 a *later* rather than something needing intervention.
2. **One worker, seven processes, counted as seven.** A count of agents sees
   one agent here. The guard sees 7.0 cores.
3. **The load average would have got both arms wrong.** It read **12.61 while
   the host was full** and **13.36 while it was free** — *higher in the arm
   with 6.7 cores idle than in the arm with 1.1*. On this host, over these two
   samples, the load average was not merely a poor instrument; it pointed the
   wrong way. Nothing decides on it, and this is why.

## What shipped as a result

Measured as `internal/hostload`: per-process CPU time differenced over a
window, attributed to the fleet by **process subtree** from pogod. Subtree, not
agent count — the live instance was one agent and three compute processes, and
any count of agents reads that as an idle host.

- **`pogo host load`** and `GET /agents/hostload` report fleet cores, non-fleet
  cores, free cores, and whether a spawn would currently be refused. (The
  endpoint shipped at `/hostload`, which pogod mounted nowhere and therefore
  404'd for the two weeks until mg-c26d moved it under `/agents`. The
  enforcement was never affected — the gate is an in-process call on the spawn
  path — but the preview this bullet describes was unreadable.)
- **`pogo agent spawn-polecat` answers 503** when the fleet already holds
  `FleetHeavyAt` — half the host. Retryable — the refusal says so, because
  "hold and re-check" and "abandon this item" are opposite actions and the
  reader is usually an agent with no human in the loop. It fails **open** on an
  unreadable or unattributable sample: refusing work on missing information
  stalls the queue for a reason nobody downstream can check.
- **A running gate samples the host** and carries the summary on its progress
  record, so `pogo refinery show` prints a `Host:` line.
- **A gate timeout reached on a saturated host says so**, in the error text,
  with the numbers.

## What did NOT ship, and why

**No cost prediction, and no scheduler.** Knowing a work item is compute-heavy
*before* dispatch is not implementable here:

- Nothing on a work item declares expected cost.
- A filer-set marker requires whoever files to set one reliably, and mg-ddf4
  established the store cannot say who filed anything — `creator` is the unix
  user and reads `daniel` for all 14 items checked. mg-0e24's `PREMISE
  COLLAPSED` section refines this: agents *can* be instructed to set a marker,
  so the objection is not that markers never work; it is that a marker depends
  on somebody remembering, and an observation of the host does not.
- Per-repo history is thin, and wrong in exactly the case that matters — the
  expensive runs are the unusual ones.

So the gate does not predict. It observes what the fleet holds **right now**,
which is knowable exactly. That is a strictly weaker claim than a scheduler
makes and it is the one the evidence supports.

**The timeout still kills, and the merge still fails.** A bound that could be
silenced by loading the host would not be a bound — that is the mg-2789 defect,
a control that cannot fail. What changed is the *reading*: the failure no
longer implies a verdict on the branch when the evidence does not support one.

**Gates were not narrowed to make them cheap.** Speed is not the goal; honest
signal under load is.

## The half this does NOT close

A load-aware dispatcher would not have helped a polecat that was **already
running** when the host filled up. Interpretation is a separate exposure: a
contention-induced timeout is indistinguishable from a red gate to the actor
with the least context, and that actor has to decide. The mayor prompt now says
a timeout on a saturated host is UNKNOWN and tells the coordinator to pass that
on, but the general problem — what a worker should do with an inconclusive gate
— is filed separately per the ticket's instruction.

## Limits of the measurement

- **Surrogate workload.** A Python CPU sweep, not the fleet's real gates. It
  isolates the CPU-contention mechanism and does not model a gate whose cost is
  I/O or process spawning.
- **Saturation cannot see past full.** The shipped instrument computes CPU time
  consumed, which is bounded by the core count: a host with twice as many
  runnable tasks as cores reads the same 1.0 as a host with one per core. It
  detects "full" and cannot say how far past full. The sweep above shows
  wall-clock keeps climbing well past that point.
- **Blind to I/O.** Neither the measurement nor the guard sees a step slowed by
  disk or network. The ticket's own note that a load average of 214 was
  dominated by waiters suggests I/O pressure on this host that nothing here
  explains. That remains unmeasured, and is a discrepancy rather than a
  mechanism — anyone acting on it should profile rather than inherit a guess.
- **`FleetHeavyAt = 0.50` is a judgement**, and Result 4 below is the record of
  getting it wrong once. It is a threshold on a marginal decision made without
  knowing the new work's cost, so it cannot be exactly right; the refusal
  prints the measurement it acted on so a wrong value is diagnosable rather
  than mysterious.

## Reproduction

`gatework.py` — the measured unit (identical work every run):

```python
N, INNER = int(sys.argv[1]), int(sys.argv[2])
for case in range(N):
    a = [float(i % 97) for i in range(200000)]
    b = [float(i % 89) for i in range(200000)]
    s = 0.0
    for _ in range(INNER):
        for i in range(200000):
            s += a[i] * b[i]
```

`burn.py` — one contending process (`while True: x = (x + i*1.0000001) % 1000003.0`).

Sweep: for each level in `0 6 14 24 40 0`, start that many `burn.py`, sleep 6,
time `gatework.py 3 200`, sample `uptime`, kill the burners, sleep 5.
