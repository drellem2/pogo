# `spawn-polecat`: the rc=0 that isn't, and the poisoned branch that is (mg-d22a, 2026-07-17)

Three defects were reported against `pogo agent spawn-polecat`. **One does not
exist. Two do, and are fixed here.** A fourth item — a same-repo concurrency
race — remains unproven and is not depended on by either fix.

The ticket asked for the reproduction to **capture stderr**, because the
original wave's evidence had been destroyed by `>/dev/null 2>&1`. It was
captured. That is what settled the first item.

---

## 1. `rc=0` on a failed spawn: **REFUTED. It is the harness, not pogo.**

**Claim:** `pogo agent spawn-polecat` prints `spawn-polecat failed: ...` and
exits `0`, so no caller can detect a failed spawn by exit code.

**Measured, with stderr captured, against the live installed binary:**

```
$ pogo agent spawn-polecat 0b77 ... --repo=/Users/daniel/dev/pogo ; echo rc=$?
spawn-polecat failed: worktree creation failed: exit status 255
Preparing worktree (new branch 'polecat-0b77')
fatal: a branch named 'polecat-0b77' already exists
rc=1          <-- NOT 0
```

`--json` mode: also `rc=1`. **The CLI is correct and always has been.**

### Where rc=0 actually comes from

The ticket records that the wave issued its spawns with `&` + `wait`. That is
the whole mechanism — reproduced directly:

| harness shape | rc |
|---|---|
| `pogo agent spawn-polecat ...` then `$?` | **1** ✅ |
| `pogo agent spawn-polecat ... &` then bare `wait` | **0** ❌ |
| `pogo agent spawn-polecat ... \| tee log` | **0** ❌ |

**Bare `wait` returns 0 regardless of the job's exit status**, and a pipeline's
status is its *last* stage. The rc=0 was manufactured at the point of
measurement. The same harness that discarded the stderr discarded the exit code.

This is the ticket's own hazard, turned on the ticket: it argues *"the failure
and the success are the same token"* — and it was the **harness** that collapsed
them. The wave measured itself.

### This is the SECOND refutation of the same claim

**gh #28 reported this already, and mg-a1e4 refuted it on 2026-07-03** — two
weeks before the re-report. From that commit message:

> gh #28 reported spawn-polecat exiting 0 on a failed spawn. **The reported
> behavior does not reproduce**: the failure path has routed through
> `cli.ExitWithError(..., cli.ExitError)` **since the command was introduced**
> (055623d) […] all exit 1

Corroborated independently here: the box carries **two** `pogo` binaries, built
**four months apart** — `~/go/bin/pogo` (2026-07-17) and a shadowed
`~/.pogo/bin/pogo` (2026-03-28, predating mg-a1e4 by months). **Both exit 1.**
There is no build on this machine that exhibits rc=0.

A "stale binary" cannot explain the report either: `~/go/bin/pogo` was installed
at **12:31:35Z** and has not been touched since — it **predates** the 13:21Z
observation. The binary that allegedly returned 0 is byte-for-byte the one
measured at 1 above.

### Why it recurred, and what was done about it

mg-a1e4's refutation lived **only in a commit message**. A commit message is not
a place anyone looks before filing, so the claim was re-reported at high
priority, wired under two other tickets as a gate, and re-investigated from
scratch. This document exists so the third report doesn't have to repeat the
work: it is indexed, and it names the mechanism — which is the part mg-a1e4
lacked, and the reason "does not reproduce" wasn't enough to make it stay dead.

**Consequence for the gate:** mg-d22a was filed as gating mg-eb54 → mg-3c32
(the MAX-2 retirement) via a "two-blindness stack" whose first blindness is
rc=0. **That half is void.** A dispatcher that checks the exit code — rather
than a bare `wait` — can detect a failed spawn today, and could throughout. The
stall-watch half is out of scope here and untouched by this finding.

**No code changed for this item.** `cmd/pogo/main_test.go` already locks the
contract end-to-end (mg-a1e4); nothing needed adding.

---

## 2. A failed spawn left a branch with no worktree: **REAL — and the mechanism is now measured, deterministically.**

`git worktree add -b <branch>` creates the branch and **then** checks it out, so
a failed checkout leaves the branch behind. Verified directly, no concurrency
involved — block the target directory and the add fails:

```
$ git worktree add /path/blocked -b polecat-probe
Preparing worktree (new branch 'polecat-probe')
fatal: '/path/blocked' already exists
$ git branch --list polecat-probe
  polecat-probe          <-- the branch survived the failed add
```

That leftover — **not the original fault** — is what broke the retry:
permanently, with a *different* and misleading error (`a branch named X already
exists`) naming nothing about the original cause. Every other failure path in
`handleSpawnPolecat` already called `cleanupFailedPolecatSpawn`; the
`worktree add` path did not.

**This needed no race to happen.** It is deterministic and reproducible on
demand, which is why the fix does not wait on the race question.

## 3. The orphan branches are the repo's normal condition: **REAL — 55 confirmed**

Cross-referencing `git branch --list 'polecat-*'` against `git worktree list` in
`/Users/daniel/dev/pogo`: **55 branches with no worktree** — accumulated from
ordinary **successful, merged** work, not only from failed spawns. Each is a
permanent landmine for the next dispatch reusing that id.

It also broke a **documented recovery procedure**: `prompts/mayor.md` instructs
stop → `mg unclaim` → re-dispatch for a dead polecat. The re-dispatch fails on
the surviving branch until a human runs `git branch -D`.

### The fix, and the limit on it

A spawn now **reclaims** a leftover `polecat-<name>` branch when it is *provably
spent* — no worktree, and nothing that isn't already in the base ref.

Reclamation deletes branches, so it is deliberately narrow. It **refuses**, with
an error naming the cause and the recovery, in the two cases where deleting
would destroy something:

- **checked out in a worktree** — a live polecat owns it;
- **carries unmerged commits** — real work nobody has merged.

On a failed `worktree add`, the handler rolls back **only the branch it
created**, never one that appeared underneath it. That guard costs one
`rev-parse` and makes the fix safe *whether or not* the race in §5 is real.

## 4. pogod emitted no spawn-failure event: **REAL — zero, out of 34,090 spawns**

```
$ grep -oE '"event_type":"[^"]*"' ~/.pogo/events.log | sort | uniq -c
  34090 "event_type":"agent_spawned"
      0  <- no agent_spawn_failed. None. Ever.
```

So a work item with no spawn record was **ambiguous**: a throttled dispatch, a
failed dispatch, and a dispatch never attempted all emitted the *identical
nothing*. A reader reconstructing the gap had to supply a mechanism from
imagination — and one did, inferring "the MAX-2 cap throttled it", writing it
into mg-eb54 as a structural finding and mailing a stop order built on it. The
spawn had failed. **No amount of care distinguishes two causes that emit the
same absence.**

`agent_spawn_failed` now fires on every failure path — including the drain-gate
refusal, so a **throttle** is distinguishable from a **failure** — carrying the
intended agent, work item, repo, status code, and the underlying error verbatim.
The drain gate moved to just after the request decode so its refusal can name
what it refused; the decode has no side effects, so the gate loses nothing.

**This is independent of the exit code, and would be even if §1 had been real:**
the status tells the *live caller*, the event tells *every later reader*. Fixing
one leaves the other blind. (Both halves of the ticket's acceptance are
demonstrated separately, in separate tests, for this reason.)

## 5. The same-repo concurrency race: **STILL UNPROVEN — and now unnecessary**

The ticket's 6-for-6 separation of contended vs. uncontended spawns is
suggestive, and wave 2's *double success* already limits it to something
intermittent. Nothing here confirms or refutes it: **the original error was
destroyed** by the wave's own `>/dev/null 2>&1`, and it is not recoverable.

Two things changed around it, though:

- **It is no longer needed to explain the observations.** The poisoning is
  deterministic (§2) and orphan branches are the repo's steady state (§3), so
  "the retry failed with *branch already exists*" needs no race at all.
- **Its cost is now bounded.** Previously any spawn failure — race-induced or
  not — left a permanent blocker. With the rollback, a failed spawn leaves clean
  state, so a race would degrade to a **transient, retryable** failure that now
  also **announces itself** in the event log (§4). If the race is real, the next
  occurrence will be on the record with its actual error, which is precisely
  what this investigation could not recover.

**Do not treat "index.lock contention" as measured.** It remains a guess fitted
to a pattern.

---

## Verification

Every test added was checked to **fail against the pre-fix behavior** before
being trusted — a passing test proves nothing until it has been seen to fail:

| test | pre-fix result |
|---|---|
| `TestSpawnPolecat_FailureEmitsSpawnFailedEvent` | FAIL (no event log written at all) |
| `TestSpawnPolecat_DrainRefusalIsNamedOnTheRecord` | FAIL (throttle emitted nothing) |
| `TestSpawnPolecat_FailedWorktreeAddRollsBackItsBranch` | FAIL (`branch left behind … permanently blocked`) |
| `TestReclaimStalePolecatBranch_ClearsSpentOrphan` | FAIL (`orphan branch survived reclamation`) |
| `TestSpawnPolecat_StaleOrphanBranchNoLongerBlocksDispatch` | FAIL (`worktree creation failed: exit status 255`) |

The last one **initially passed against the mutant** — it was dying at template
resolution, long before the code under test, and proving nothing. It now asserts
it reached the worktree stage before asserting anything about the branch. A test
that cannot fail is the same defect as an exit code that cannot fail.

Full gate: `./build.sh` — exit 0.
