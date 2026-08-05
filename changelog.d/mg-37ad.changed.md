- **The refinery merges in per-repo lanes instead of one global slot, so a gate
  in one repo no longer sets merge latency for every repo (mg-37ad).** New knob
  `[refinery] max_concurrent_merges` (default 2; `1` is the old behaviour).

  **The measurement.** On 2026-08-05 one quality gate held the single slot for
  1h17m with twelve merge requests behind it and zero merges since 17:45Z. A
  second gate held it ~30 minutes the same evening. Nothing was malfunctioning:
  the head gate was alive and compute-bound (child process at 366% CPU) and
  `pogo refinery show` correctly said "Slow, not hung — waiting is correct". The
  serialisation was a deliberate property, which is why this is a design change
  and not a bug fix.

  The load-bearing detail is the composition, not the depth. **Seven of the
  twelve were for a different repo than the one holding the slot** — `pogo` work
  queued behind a `one_third_width_three` gate while the pogo repo was, from the
  refinery's point of view, idle. Among the seven were that day's own incident
  fixes: the wedge detector written in response to the outage, and the
  mailbox-addressing fix. The fleet's ability to repair itself was rate-limited
  by the slowest gate in any repo it happened to share a refinery with.

  **The rule.** Merges are partitioned into lanes keyed on the repo. Within a
  lane they stay strictly serial and in submit order; across lanes they run
  concurrently up to the cap. The lane is per-repo because the refinery keeps
  exactly one private clone per repo and each merge rebases onto a target ref the
  next one is about to move — there is nothing to parallelise there. Across
  repos neither dependency exists.

  The lane key is the repo **basename**, matching how `ensureWorktree` names the
  clone. Two checkouts of different repos that share a basename share a clone, so
  they must share a lane; keying on the full path would have put two merges into
  a directory only one of them can own. It errs in the safe direction — it can
  serialise unnecessarily, never overlap two merges that must not.

  **Why the cap is 2, and why there is a cap at all.** The lane rule bounds
  correctness but not cost. A gate is the most expensive thing pogod runs
  (`build.sh` compiles and runs a full test suite) on a host shared with the
  polecat fleet, and gates running against each other inflate one
  another's wall time until a gate timeout starts failing branches that were
  fine. That is not hypothetical: it is why the contention record exists, and why
  a timed-out gate already reports what the host was doing so a contended timeout
  does not read as a verdict on the change. Two is what the measured incident
  needs — the stalled queue spanned exactly two repos. Load-aware admission was
  considered and deliberately not built: one number an operator can turn down
  beats two mechanisms governing the same quantity.

  **The default is binding, and that is recorded rather than smoothed over.**
  While this change sat in the merge queue it was itself queued behind a
  `one_third_width_three` gate burning 464% CPU, in a queue spanning THREE repos
  — `one_third_width_three`, `onethird_program` and `pogo`. A cap of 2 would have
  freed one of the two idle repos and left the other waiting. That does not make
  the number wrong on a host already at load average 25–120, but "two is enough"
  is not the claim; "two is the most this host should spend on gates by default"
  is. A site with cheaper gates or a bigger host should raise it.

  **What happens to a queue that exists when this lands** — stated because this
  redesigns the thing that merges its own change.

  *Upgrade:* the old state file's single `processing` slot is still read, so an
  in-flight merge lands in the recovery set and is resolved by the same ancestor
  probe as always. Nothing is dropped.

  *Rollback:* this is the direction that can lose work, and it shaped the on-disk
  format. The schema **version is not bumped** — a bump makes an older pogod
  refuse the file outright and take the merge queue down. Since an older pogod
  reads only `processing` (one slot) and would drop everything past the first
  in-flight merge, every in-flight merge is **also mirrored into `queue`**, at
  the head, marked queued. An older pogod re-queues them all instead; the
  already-merged probe (gh #34) makes re-running one that had landed a no-op.
  This binary strips the mirror on load. The test asserts against a decode into
  the *old* wire shape, because the question is what their loader does with our
  file, not what ours does.

  *Shutdown:* `Stop` waits for in-flight lanes rather than cancelling them —
  exactly what it did when the loop was serial and *inside* the merge. It matters
  more now, because pogod builds a replacement Refinery from the state file the
  outgoing one flushes, and returning early would put two refineries on one
  clone. A long gate therefore still makes `Stop` slow; that is preserved
  deliberately.

  **The cancel handle moved to the lane, and that was not optional.** One shared
  `context.CancelFunc` was correct while a single merge could run and would have
  become a *broadcast* the moment two could — cancelling one repo's merge would
  have killed every other repo's gate, silently, since each victim reports itself
  as cancelled by an operator.

  **Reporting.** The other half of the incident was that no view named the repo
  holding things up; five polecats independently read `pogo refinery queue` as
  "refinery stalled" that day (mg-48d8). `pogo refinery queue` now leads with
  every running merge, longest-running first; `pogo refinery status` prints one
  `Active:` line per lane naming its repo and branch, plus `Lanes: N of M busy`.
  `Status.Processing` is kept meaning "one of them" so a client older than this
  change still reports a busy refinery as busy.

  **The concurrency test is a two-arm control.** Both arms use the same gate
  construction — a rendezvous that can only complete if another named gate is
  running at that moment. Across two repos both merge; across one repo the first
  gate times out, proving it ran alone. A passing arm on its own would be
  consistent with a gate that always succeeds.

  **Not fixed here:** seven merge requests for one repo still merge one at a
  time — per-repo throughput is bounded by gate cost, which mg-da30 has since cut
  by dropping the redundant second gate from the refinery's default list. The
  liveness verdict that reports the runner's heartbeat as the gate's progress is
  still mg-48d8.

  **Composes with mg-3977**, which landed alongside this and was written
  independently: that cap withholds worker slots from a repo's dispatch budget
  while the refinery holds a merge request for *that repo*, deciding it by
  scanning `QueueWithProcessing()` for a matching repo path. Both halves are
  already per-repo, so a second lane reserves in a second repo's budget. The
  answer gets more accurate, not less — before lanes, only one merge could be in
  flight, so a second repo's running merge was visible to that scan only while it
  was still queued.
