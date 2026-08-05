# Refinery concurrency: per-repo gate lanes (mg-37ad)

Status: implemented. Package `internal/refinery` (`lanes.go`), knob
`[refinery] max_concurrent_merges`.
Ruling: pm-pogo, 2026-08-05 — YES to per-repo gate concurrency.
Lineage: gh drellem2/pogo#36 (the submit-wakeup half shipped as mg-b7b9; this is
the deferred remainder). The ticket was parked "until throughput demands"; this
document records that the condition fired and what was built in response.

## The measurement that unparked it

On 2026-08-05 the refinery had one global merge slot. A single quality gate held
it for 1h17m:

```
queue depth   5 (18:21Z) -> 11 (18:40Z) -> 12 (18:50Z)
merges        0 since 17:45:32Z
head MR       polecat-o9d7b, one_third_width_three, gate running 1h0m
```

Nothing was malfunctioning. The head gate was alive and computing (parent
sleeping at 0%, child at 366%), and `pogo refinery show` correctly reported
"Slow, not hung — waiting is correct." A second gate held the slot for ~30
minutes the same evening. (Two network outages the same day flushed 19 merge
requests at the fetch stage — a separate fault, noted only so it is not read as
part of this one.)

The load-bearing detail is not the depth, it is the **composition**: twelve
merge requests behind one gate, **seven of them for a different repo**. `pogo`
work sat behind a `one_third_width_three` gate while the pogo repo was, from
the refinery's point of view, idle. Among the seven were that day's own
incident fixes — the wedge detector written in response to the outage, and the
mailbox-addressing fix.

doctor's framing, kept verbatim because it is the whole argument:

> gate cost in the slowest repo sets merge latency for every repo, and nobody
> reading `pogo refinery queue` would see why.

Two repos' gates share no working tree and no test suite. Serialising them
bought nothing and cost the fleet its ability to repair itself at the rate the
slowest unrelated gate allowed.

## The rule

Merges are partitioned into **lanes, keyed on the repo**. Within a lane merges
are strictly serial and in submit order; across lanes they run concurrently, up
to a configured cap.

**Why a lane is per-repo and not per-merge.** The refinery keeps exactly one
private clone per repo (`~/.pogo/refinery/worktrees/<repo>`), and `attemptMerge`
does `fetch` → `checkout -B` → `rebase` → `merge --ff-only` → `push` in it. Two
merges for one repo in one clone would clobber each other's checkout, and even
with separate clones each rebases onto a target ref the other is about to move —
the second one's answer depends on the first one's outcome. There is nothing to
parallelise there. Across repos, neither dependency exists.

**Why the key is the repo BASENAME, not the path.** `ensureWorktree` names the
clone `WorktreeDir/filepath.Base(repoPath)`. Two checkouts of different repos
that happen to share a basename therefore share one clone. Keying lanes on the
full path would give them separate lanes and put two merges into a directory
only one of them can own. Keying on the basename derives the lane rule from the
shared resource instead of hoping the two agree. It is conservative in the safe
direction: it can serialise two merges unnecessarily, never overlap two that
must not be.

**Scheduling.** The queue is scanned front to back; the first item whose lane is
free starts. An item whose lane is busy is passed over. That overtaking IS the
change, and it is bounded — an item can only ever be passed by a merge that
could not have contended with it. Within any one repo the order is exactly the
submit order it has always been.

## The cap, and why it is 2

`[refinery] max_concurrent_merges` (default 2) bounds how many **different**
repos merge at once. It cannot allow two merges for one repo to overlap; that is
the lane rule's job and no setting changes it.

The cap exists because the lane rule bounds correctness but not cost. A quality
gate is the most expensive thing pogod runs — `build.sh` compiles and runs a
full test suite — on a host shared with the polecat fleet.

(An earlier draft of this document said `build.sh` runs that suite *twice*. It
does not, and the claim is withdrawn: `build.sh` runs it once, and the doubling
was the refinery gate's own default list — `./build.sh` then `./test.sh` — which
mg-da30 removed. The observed cost was real; that explanation of it was not.)

Gates running against each other inflate one another's
wall time until a gate timeout starts failing branches that were fine. That
failure mode is not hypothetical: it is why `gatecontention.go` exists, and why
a timed-out gate already reports what the host was doing while it ran, so a
contended timeout does not read as a verdict on the change.

Two is what the measured incident needs — the queue that stalled spanned exactly
two repos, and one lane each drains it. Raising it buys parallelism across more
repos at the cost of contention. Setting it to **1 restores the historic
single-slot refinery exactly**, which is the intended rollback and is covered by
a test.

**A live observation that qualifies this, recorded because it cuts against the
number.** While this change sat in the merge queue (2026-08-06 00:50), the queue
it was waiting in spanned THREE repos, not two:

```
in flight  one_third_width_three   (a Python gate at 464% CPU, ~1h45m)
queued     onethird_program        x2
queued     one_third_width_three   x2   <- correctly serial behind the holder
queued     pogo                    x2   <- including this change
```

So a cap of 2 is BINDING in ordinary operation: it would have started one of the
two idle repos and left the other still waiting. The number is not thereby
wrong — one gate alone was holding 4.6 cores on a host already at load average
25–120, and opening a third would have been the contention this cap exists to
prevent — but "two is enough" is not the claim. The claim is that two is the
most this host should spend on gates by default, and that a site whose gates are
cheaper, or whose host is bigger, should raise it. Recorded here rather than
smoothed over, because the next person to tune this deserves the case where the
default did not fully drain the queue.

Deliberately NOT built: load-aware admission (refusing to open a second lane
when the host is already saturated). The refinery already samples host load, so
it is available, but two mechanisms governing the same quantity is more to
explain and more to get wrong than one number an operator can turn down. If the
cap proves to be the wrong control, that is the next thing to try.

## What happens to the queue that exists when this lands

This redesigns the thing that merges its own change, so the transition is stated
rather than assumed. The failure to avoid is a merge request that exists in the
old world and exists nowhere in the new one.

Note the order of events: the refinery that merges **this** change is the old,
single-slot one. Nothing here takes effect at merge time. The switch happens at
the next pogod restart onto a binary containing it, which is the "upgrade" case
below — and at that moment the queue is whatever the old refinery left in the
state file.

**pogod restarts onto this build with merges in flight.** The old state file has
one in-flight merge in `processing` and the rest in `queue`. `loadState` reads
`processing` as well as the new `processing_lanes`, so the in-flight merge lands
in the recovery set and is resolved by the same ancestor probe as always —
merged if it landed, re-queued at head if it did not, lost only if the probe
cannot answer. The queue loads unchanged. Nothing is dropped. Covered by
`TestUpgradeCarriesTheSingleProcessingSlot`.

**pogod is rolled back to a build predating this change.** This is the direction
that can lose work, and the one that shaped the on-disk format:

- The schema **version is not bumped**. A bump makes an older pogod refuse the
  file outright ("state version N newer than this binary supports — refusing to
  overwrite"), which takes the merge queue down on any rollback. Additive and
  readable both ways beats detectable.
- An older pogod reads only `processing` (one slot) and `queue`. Handed a file
  with three merges in flight it would keep at most one and silently drop two.
  So every in-flight merge is **also written into `queue`**, at the head, marked
  `queued`. An older pogod re-queues all three instead of dropping them, and the
  already-merged probe at the top of `processMerge` (gh #34) makes re-running one
  that had landed a no-op rather than a double merge.
- This binary strips that mirror on load, keying on the IDs in
  `processing_lanes`, so it never queues a duplicate of a merge it is about to
  recover.

Covered by `TestOlderPogodStillFindsEveryInFlightMerge`, which asserts against a
decode into the *old* wire shape — the question is not what our loader does with
our file, it is what theirs does.

**Shutdown.** `Stop` waits for in-flight lanes rather than cancelling them. That
is exactly what it did when the loop was serial (the loop was *inside* the
merge, so stopping always meant "finish the merge you are on"). It matters more
now: pogod builds a **replacement** Refinery from the state file the outgoing one
flushes, so returning while lanes were still pushing would put two refineries on
one clone. A long gate therefore still makes `Stop` slow. That is the
pre-existing cost of never abandoning a merge halfway, and it is preserved
deliberately. Covered by `TestStopWaitsForInFlightLanes`.

**In-flight merges are unaffected by the cap changing.** The cap gates the start
of a merge, never a running one.

## Consequences elsewhere in the refinery

- **Cancel is per-lane.** The cancellation handle moved from the Refinery to the
  lane. A single shared `context.CancelFunc` was correct while one merge could
  run and would have become a *broadcast* the moment two could — cancelling one
  repo's merge would have killed every other repo's gate. That failure would
  have been silent, because each victim reports itself as cancelled by an
  operator. `TestCancelReachesOnlyItsOwnLane` is the guard.
- **A QA hold releases its lane.** Otherwise a branch held for QA — which is not
  even running — would block every other merge for its repo: the serialisation
  this removes, reintroduced one repo at a time.
- **The loop must not re-arm on undispatchable work.** Dispatch no longer
  blocks, so treating a queue full of busy-lane items as actionable would spin
  the loop: dispatch starts nothing, re-arms the wake, runs again, forever.
  `wakeIfActionable` therefore only fires for an item that could actually start.
- **Recovery resolves several in-flight merges,** newest-first, one at a time,
  before any lane starts. Each probe force-removes rebase debris from its repo's
  clone and must not do that under another merge's feet.

## Reporting

The other half of the incident was that no view named the repo holding things
up. Five polecats independently concluded "0 processing / refinery stalled" from
`pogo refinery queue` that day (mg-48d8). A view that showed only one running
merge would have left that exactly as it was, with the extra rows hidden instead
of the one.

- `pogo refinery queue` leads with **every** running merge, longest-running
  first, then the pending ones in order.
- `pogo refinery status` prints one `Active:` line per lane, each naming its
  repo and branch, plus `Lanes: N of M busy`.
- `Status.InFlight` / `ProcessingCount` / `MaxConcurrentMerges` carry this over
  the wire. `Status.Processing` is kept, meaning "one of them" (the
  longest-running), so a client older than this change still reports a busy
  refinery as busy instead of as idle.

`QueueLen` still counts pending requests only, so it can read 0 while merges
run — unchanged, and still the reason `Active:` is printed separately (mg-0c51).

## What this does not fix

- Seven merge requests for one repo still merge one at a time. That is correct
  and unavoidable; per-repo throughput is bounded by gate cost, which is
  gate cost's subject, not this one. mg-da30 has since landed and cut that cost
  by dropping the redundant second gate from the default list.
- Nothing here caps polecats per repo — that is mg-3977, which has landed.

  It **composes** with lanes rather than competing with them, and the two were
  written independently, so the interaction is worth stating. mg-3977 withholds
  `refinery_reserve` worker slots from a repo's dispatch budget while the
  refinery holds a merge request for **that repo**, in flight or queued, and it
  answers that question by scanning `QueueWithProcessing()` for a matching repo
  path. Both halves are already per-repo, so a second lane simply reserves in a
  second repo's budget. If anything the answer gets *more* accurate: before
  lanes only one merge could be in flight, so a second repo's running merge was
  visible to that scan only while it was still queued.
- The liveness verdict reporting the runner's heartbeat as the gate's progress
  is mg-48d8, filed from the same incident and not addressed here.
- `PruneWorktrees` still operates on a repo's clone without asking whether a
  merge is running in it — it can `checkout main` and delete branches under a
  live gate. That hazard is **pre-existing** (it was already reachable from an
  HTTP handler while the serial loop was mid-merge) and is not created here,
  though lanes make it marginally more likely by allowing a second merge to be
  in flight. Lanes now make the fix trivial — skip a clone whose lane is
  occupied — but it is a different defect and was left alone rather than
  smuggled in. Not yet filed.

## Ticket metadata corrections carried forward

Two corrections were applied to mg-37ad itself and are recorded so they are not
re-derived:

1. The `gh-issue` tag was removed. It carried the workflow tag without a body
   carrier block, which the dispatch guard correctly refuses.
2. "close #36 when landed" is **stale** — gh drellem2/pogo#36 is already closed,
   and was authored by Daniel's own account, not an external reporter. Closing
   it is not an acceptance criterion for this work.
