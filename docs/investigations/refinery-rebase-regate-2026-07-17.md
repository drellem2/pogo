# Does a rebased 2nd+ PR in a batch get re-tested post-rebase?

**Work item:** mg-a9bb — fact-finding only, no fix.
**Date:** 2026-07-17.
**Asked by:** architect, via pm-pogo → mayor. Framing: *"I'm not asserting that; I'm
saying don't rest the nightly on it without checking."*

## Answer

**YES — it re-runs, and it re-runs against the exact tree that lands.**

The gate runs **after** the rebase, on every MR, including the 2nd+ item of a batch.
Observed, not inferred: for a batch's second PR the gate was invoked against
`11ab2e8`, a tree containing **both** PRs' files, and `11ab2e8` is byte-for-byte the
SHA that ended up as main's HEAD. The gate's HEAD **was** the landed commit.

This **softens one of mg-bfe5's arguments** — merge-time proof here is a measurement
of the tree that lands, not a claim about a different commit. bfe5 stands on its
other, undisturbed leg (see "What this does not touch").

## What was observed

A real `Refinery` (not a mock) processed a two-branch batch. Both branches forked from
the **same** main tip `922d8e1`, so the second could only see the first's file if a
rebase had happened before the gate. The gate script recorded its own `git rev-parse
HEAD` and tree contents on each invocation.

```
shared fork point for both branches:      922d8e1
beta tip as submitted (pre-rebase):       42fccfe

--- GATE INVOCATION ---            <- alpha (1st in batch)
head_sha=008b97e
alpha.txt=PRESENT  beta.txt=ABSENT
log=add alpha|initial|

--- GATE INVOCATION ---            <- beta (2nd in batch, post-rebase)
head_sha=11ab2e8
alpha.txt=PRESENT  beta.txt=PRESENT
log=add beta|add alpha|initial|

===== FINAL main HEAD = 11ab2e8 =====
11ab2e8 add beta
008b97e add alpha
922d8e1 initial
```

Three things this pins, each separately:

1. **The 2nd PR re-enters the gate at all.** Two gate invocations for two MRs.
2. **The gate ran post-rebase.** beta was submitted at `42fccfe`; the gate saw
   `11ab2e8`. The rebase preceded the gate.
3. **The gate ran against the *combination*.** `alpha.txt=PRESENT` in beta's gate
   invocation — the tree under test contained the other PR's change. And beta's gate
   SHA `11ab2e8` **equals** the final main HEAD.

Point 3 is the one that matters, and it is deliberately the *whole* claim rather than
the subset "the gate function is called in the merge path" (the instance-13 shape).

## The mechanism, by symbol

Read at `internal/refinery/`, verified at read time (symbols, not line numbers):

- **`Refinery.processNext`** (`refinery.go`) — dequeues **one** MR and calls
  `processMerge`. `dequeue` sets `r.processing`, the single in-flight slot. **There is
  no batch merge primitive.** A "batch" is just N sequential MRs; the 2nd is a
  full independent cycle, not a rider on the 1st.
- **`Refinery.processMerge`** (`merge.go`) — loops attempts, calling `attemptMerge`.
- **`Refinery.attemptMerge`** (`merge.go`) — the order, as it executes:

  ```
  fetch origin
  checkout -B <branch> origin/<branch>
  rebase origin/<target>            <- rebase
  runQualityGates(wtDir, repoPath)  <- gate, AFTER the rebase, on the rebased worktree
  fetch origin <target>
  checkout -B <target> origin/<target>
  merge --ff-only <branch>          <- structural guard, see below
  push origin <target>
  ```

  Confirmed live in the daemon's own step logging: `step=rebase` → `step=quality-gates`
  → `step=merge` → `step=push`, in that order, for both MRs.

- **`runQualityGates` / `loadGateConfig`** (`merge.go`) — pogo has **no**
  `.pogo/refinery.toml` (verified absent in the worktree, in `/Users/daniel/dev/pogo`,
  and on `main`), so gates fall back to the defaults that exist in the worktree:
  **`./build.sh` and `./test.sh`**. Both run, against the rebased tree.

### The ff-only merge is what makes the answer hold under a race

The gate runs on the rebased branch, but the target is re-fetched *after* the gate.
If `origin/<target>` advanced **during** the gate run, `merge --ff-only` fails (the
branch is no longer a descendant), returns `retryableError`, and the whole cycle —
**including the gate** — runs again on a freshly rebased tree. So the system cannot
silently push a combination the gate never saw. Already pinned by the existing
**`TestProcessMergeFFRetryOnRace`**, which injects exactly that race and asserts the
gate ran ≥2 times. Re-run today: **PASS**.

## The one hole: `[gates] skip_on_retry` — latent, and OFF for pogo

`processMerge` computes `skipGates := skipGatesOnRetry && attempt > 1`. When a repo
sets `[gates] skip_on_retry = true`, attempt ≥2 **rebases onto the advanced target and
skips the gate**. The tree that lands on that path was gated zero times; the gate's
one run was against attempt 1's different tree. That is precisely the
"proof about a commit that is not the one that landed" shape — it exists, in code,
today.

It is **not a live defect for pogo**, on two independent counts:

- `SkipGatesOnRetry` is only ever set from `[gates] skip_on_retry` in
  `.pogo/refinery.toml` (`parseRefineryConfig`). **pogo has no such file**, in either
  location `loadConfig` reads. The field defaults to `false`.
- The default `maxAttempts` comment recommends the knob be *paired* with it, but
  nothing enables it implicitly.

**No ticket filed.** This is a config-gated risk on a config pogo does not use, not a
live defect — reporting it rather than building, per scope. Its existing test
(`TestProcessMergeSkipGatesOnRetry`, PASS today) documents the trade-off as
intentional: "gates already passed on near-identical code." That reasoning is sound
for the CI-version-bump case it was written for and unsound in general; worth knowing
before any repo turns it on.

## What this does NOT touch

**mg-bfe5 (deploy-time control) is unaffected in its main argument.** This finding
removes only the "merge-time proof is about the wrong commit" leg. The other leg is
untouched and independently verified by architect: `do_build` runs `go install` and
**no tests at all**, so the 24-assertion live control
(`scripts/pogo-self-deploy_live_test.sh`, wired into `test.sh`) runs **at merge,
never at deploy**. Merge-time proof measures the merged tree correctly — and then the
deploy re-builds an artifact that nothing re-measures. bfe5 loses one argument, keeps
its case.

**No defect ticket comes out of this**, contra the "if it does not re-run" branch of
the assignment. There is no mg-8b48-shaped "live today, every batch merge" bug here.

## What I could NOT determine

- **Concurrent refineries.** Everything above holds for the single in-flight slot
  (`r.processing`). Two refinery *processes* against one origin were not tested; the
  ff-only guard should still refuse, but I did not observe it.
- **Gate flakiness.** "The gate ran against the landed tree" is not "the gate is
  correct." A gate that passes a broken tree is out of scope here.
- **The `probeAlreadyMerged` path.** An already-landed branch resolves merged
  **without** running gates (`processMerge`, gh #34). That is a no-op by construction —
  the tree is already on main — but I did not exercise it.
- **PR-mode push-back.** `pushBackForPR` runs *after* gates and force-pushes the
  rebased branch; it does not alter the gated tree. Not separately observed.

## Reproducing

Fact-finding only — the harness below was run and then removed rather than committed
(scope: read/measure/report). Drop it in `internal/refinery/` as
`zz_observe_a9bb_test.go` and run:

```
go test ./internal/refinery/ -run TestObserveBatchSecondPRGateTree -v
```

It reuses the package's existing `run` / `runOut` helpers, builds a bare origin, forks
two branches from one tip, submits both, calls `processNext()` twice, and prints what
the gate saw. The gate script is the whole instrument:

```sh
#!/bin/sh
set -e
{
  echo "--- GATE INVOCATION ---"
  echo "head_sha=$(git rev-parse --short HEAD)"
  if [ -f alpha.txt ]; then echo "alpha.txt=PRESENT"; else echo "alpha.txt=ABSENT"; fi
  if [ -f beta.txt ]; then echo "beta.txt=PRESENT"; else echo "beta.txt=ABSENT"; fi
  echo "log=$(git log --oneline --format='%s' | tr '\n' '|')"
} >> "$GATE_LOG"
exit 0
```

Compare the second invocation's `head_sha` against the final `main` HEAD. They match.

## Xref

- **mg-bfe5** — deploy-time control. Softened by this (loses the wrong-commit
  argument), not refuted.
- **mg-8b48 / 4b41bb2** — the "live today, not nightly-only" precedent. **Does not
  apply**; no live defect found.
- **mg-c02d** — the decaying-claim class. "main is green" is, on this evidence, **not**
  decaying at the merge boundary: the claim is about the tree that landed.
- **gh-issue-rebased-pr-dangles / mg-f18c** — the established rebase behaviour this
  question built on; the rebase is real, and the gate follows it.
