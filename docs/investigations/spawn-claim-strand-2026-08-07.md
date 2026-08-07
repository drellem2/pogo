# A failed spawn, a stranded claim, and two commands that disagreed

**Date:** 2026-08-07 · **Work item:** mg-790f · **Status:** three questions
answered, one fix landed

Mayor lost roughly half an hour on the night of 2026-08-06 to a work item that
`mg show` called `available` and that `spawn-polecat` refused with `already
claimed (by PID 4368)` — pogod's own pid. This is what was actually happening,
what was fixed, and which of the night's theories did not survive.

## The two observations, side by side

Two `spawn-polecat` failures, indistinguishable from outside — no agent
registered, no output — and **opposite inside the store**:

| | `mg-6f5e` | `mg-325c` |
|---|---|---|
| worktree | created (removed by hand later) | created |
| agent registered | no | no |
| claim | **claimed/, under pogod's pid 4368** | **not claimed at all** |
| `mg unclaim` | "was claimed by PID 4368" | "not claimed, so there is nothing to release" |

Two failures, one symptom, opposite claim states. That pair is what the
investigation had to explain, and it turns out not to need two failure modes.

## 1. Where the claim is taken

`handleSpawnPolecat` (`internal/agent/api.go`) runs, in order:

```
dispatch gates → template → prompt file → git worktree add →
command expansion → claimForSpawn → r.Spawn → register mail-check
```

The claim is the **last fallible step before the spawn** and the worktree is
created **well before it**. So the worktree discriminates nothing — which is
exactly what the two data points show:

- a dispatch that dies between `worktree add` and `claimForSpawn` leaves a
  worktree and no claim → **mg-325c**;
- a dispatch that dies at or after `r.Spawn` leaves a worktree **and** a claim
  under pogod's pid → **mg-6f5e**.

One order of operations, two death points, both observations accounted for. The
ticket's instruction to "say which step each observed failure reached" is
answered: `325c` did not reach the claim, `6f5e` did.

## 2. `mg show` and the claim check read the same record — the two-stores theory is refuted

They are not two stores, and this is worth stating flatly because the ticket
listed it as the possibility that would make the stale claim a mere symptom.

`mg show` is `workitem.ReadWithStatus`; `mg claim` is `workitem.Claim`. Both
resolve through the **single** `workitem.ResolveUnique` walk over
`<root>/work/*`, and an item's status **is** the directory its file sits in
(`resolve.go`, `activeStates`). There is no cache, no index, and no second
record. `mg show` therefore cannot report `available` for a file sitting in
`claimed/`.

**What actually happened** is two readings separated in time against a dispatch
that was still running. The `spawn-polecat` CLI had been killed by a wrapper
`timeout`, and killing the client does not stop the server-side handler: pogod
went on to take the claim *after* the `mg show` that reported available. The
store never lied.

The defect is real, but it is one layer over: **a dispatch that holds a claim
with no agent to show for it is invisible to every command an operator has**. A
claim taken at 23:41 and a status read at 23:38 then look like a contradiction
instead of a sequence, and no command on the machine could tell the difference.

## 3. Release-on-failure is necessary and not sufficient — so the check changed

`releaseSpawnClaim` already hands the claim back when `r.Spawn` returns an
error, and it is kept: it is the cheap path and it fires for the common failure.
But it only runs if the failing dispatch lives long enough to run it, and
`mg-6f5e` produced no output at all. The ticket said not to stop there, and it
was right.

The fix is in the **claim check**, where it survives a spawn that executes no
cleanup whatsoever (`internal/agent/spawnclaimadopt.go`):

> A claim held under pogod's **own pid**, with **no dispatch in flight** and
> **no live agent** on the item, is owned by nothing. The dispatch adopts it.

Adoption is a **no-op on disk** — the claim file already names pogod's pid,
which is exactly what a fresh claim-at-spawn writes — so the item is in
`claimed/` before and after with no state between, and mg-7254's
duplicate-dispatch guarantee is kept by construction. `mg unclaim` + `mg claim`
would not do that; it is the same reason macguffin grew `mg reclaim`.

All three conditions are load-bearing:

| Condition | What it rules out |
|---|---|
| the pid is **ours** | stealing a human's `mg claim`, or a live worker's re-stamp |
| **no dispatch in flight** | a second dispatch adopting the claim of a first that is merely slow inside `Spawn` — the double dispatch, rebuilt on top of its own fix |
| **no live agent** | a worker that started but has not re-stamped yet, or runs against an `mg` with no `reclaim` at all and so works under pogod's pid for its whole life |

Note what is **not** tested: whether the holder pid is *alive*. A healthy
worker's re-stamped claim names the pid of the `mg reclaim` subprocess, which
exits immediately — "the claim pid is dead" is the ordinary state of a perfectly
owned item, and a liveness test would condemn the fleet.

### The in-flight ledger also answers the question that cost the time

A second dispatch onto an item held by a wedged first one is now refused with
that dispatch's **name and age** — "a dispatch for polecat 6f5e, started 31m
ago, still holds it and has not returned" — instead of a bare pid. That sentence
is the diagnosis mayor spent half an hour reconstructing, delivered in the
refusal.

### The acceptance criterion, and where it is pinned

> "`mg show` must not report `available` for an item that a dispatch cannot
> claim."

`TestMgShowNeverReportsAvailableForAnItemDispatchRefuses` asserts it over every
shape of claim refusal, not just the observed one. One case needed a code change
rather than an assertion: macguffin's `claim_race` (the rename out of
`available/` failed while the item is still available) is flagged retryable by
`mgerr` and would otherwise have produced a 409 on an item `mg show` calls
available. `MGWorkItemClaimer` now retries once when a conflict names no claim
file, which resolves it to either a claim or an honest already-claimed answer.

### What is deliberately NOT adopted

A claim stranded by a pogod that has since **restarted** carries the old
daemon's pid, so it is refused rather than adopted. A claim under some other pid
is indistinguishable from a human's, and silently stealing a human's claim is
worse than making an operator run `mg unclaim`. The refusal names the pid and
`pogo agent list` shows no agent — the diagnosis is in the refusal, but the
recovery is still manual.

## What this does NOT explain

**Why the spawn wedged in the first place.** That is `mg-6ea3` (the refinery
endpoint not returning, with two spawns hanging at the same moment while `pogo
agent list` answered instantly). This work makes the residue recoverable and the
state legible; it does not identify what blocks `r.Spawn`. If mg-6ea3 finds a
shared lock, the two hangs and these two strands have one cause.

**The load story is struck.** Mayor proposed a load threshold, revised it twice,
and then withdrew the causal claim: `mg-325c` failed at load 62 where three
spawns had succeeded at 44–57, and every one of those spawns also happened while
the refinery was degrading, which nobody controlled for. It is recorded here as
withdrawn rather than omitted, because a plausible story with numbers attached
outlives its author's belief in it unless someone says so.

## The standing caution, which survives all of the above

Repeated here from mg-0d70 because this is where a reader chasing a spawn hang
will actually look. It is a statement about the **instrument**, not about spawn,
so it holds whatever mg-6ea3 concludes:

> **A slow instrument and a stalled subject are the same token to a timeout.**
> Before asserting anything is wedged, run a **control** — a cheap call to the
> *same* daemon on a *different* path — and report the control alongside the
> finding.

On the night, `pogo agent list` returning in 0s while `pogo refinery queue`
timed out at 45s was a diagnosis; either reading alone was not. Four agents in
one evening would have filed a false wedge without checking load first. That is
a rate, not an anecdote.
