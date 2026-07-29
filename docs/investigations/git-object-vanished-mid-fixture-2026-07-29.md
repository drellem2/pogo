# A reachable git object vanished mid-fixture: `TestFreshenWorkspaceAlertsOnDirtyStaleCheckout` (mg-ea0c)

**Date:** 2026-07-29 (failure), 2026-07-30 (investigation)
**Ticket:** mg-ea0c · **Surfaced by:** the gh#91 build (mg-8792), relayed by reviewer d631
**Verdict:** environmental. The test is a victim, not the defect — but it was the
package's largest git-subprocess population and held the exposure window open for
~2 seconds, and that part is now fixed.

## The failure

One CI `test` job, `internal/agent`:

```
--- FAIL: TestFreshenWorkspaceAlertsOnDirtyStaleCheckout (2.24s)
    workspace_test.go:139: git push origin main in /tmp/TestFreshen...56991347/002/publisher: exit status 1
        fatal: bad tree object 018d7985efea179740aaf648e3795be6e9b7fa90
        error: remote unpack failed: eof before pack header was fully read
        To /tmp/TestFreshen...56991347/002/origin.git
         ! [remote rejected] main -> main (unpacker error)
```

The sending side's `pack-objects` died walking its own object store; the remote's
unpacker then saw EOF because nothing arrived. The remote error is downstream
noise. The local `fatal:` is the event.

## Reproduction rate, measured before any change

| Population | Runs | Failures |
|---|---|---|
| CI `test` jobs since the test landed (2026-07-21) | **87** | **1** (1.1%) |
| `-run TestFreshenWorkspaceAlertsOnDirtyStaleCheckout -count=30`, in-suite | 30 | 0 |
| Fixture mirror, 8 concurrent workers × 12 iterations | 96 | 0 |
| `go test ./...` (the CI shape, packages in parallel) | 8 | 0 |

The 87 CI executions enumerate every attempt of every `CI` run since
`d73dba0` introduced the test. Three other `test` jobs failed in that window;
all three were `TestAckHTTP`, unrelated. So the rate is **1 in 87**, and it has
never been reproduced locally in 134 fixture executions.

**This is emphatically not mg-d578's shape.** That one was 10-of-15 under
`go test -race` in `internal/claude` and reproduced on demand. This is 1-in-87,
CI-only, not race-flagged, and in a different package. They are not the same
finding and this one is not fixed by that one's fix.

## What the error text actually means (positive control)

`fatal: bad tree object <oid>` comes from `list-objects.c: process_tree()`, which
calls `parse_tree_gently(tree, 1)` — `quiet_on_missing = 1`. So the message alone
does not say *why* the object could not be read. Four mutations were applied to a
byte-identical rebuild of the fixture, each to the same object the CI log named,
and pushed:

| Mutation | git's output |
|---|---|
| **object deleted** | `fatal: bad tree object <oid>` — **and nothing else** |
| object truncated to 0 bytes | adds `error: object file ... is empty` (×2) |
| 8 bytes zeroed mid-object | `error: inflate: data stream error` + `fatal: loose object ... is corrupt` |
| object chmod 000 | adds `error: unable to open loose object ...: Permission denied` (×2) |
| clean push under `ulimit -n 12` | **succeeds** |

Only ENOENT is silent — `open_loose_object()` prints `unable to open loose object`
for every errno *except* ENOENT. The CI log carries no such line, and the test
captures git's stderr in full via `CombinedOutput`, so nothing was dropped.

**Therefore: at push time that object was absent from the sender's store.** Not
truncated, not corrupt, not unopenable, and not an fd problem.

## Which object, and what that rules out

Tree OIDs in this fixture are content-addressed over deterministic content, so
they are reproducible even though commit OIDs are not.
`018d7985efea179740aaf648e3795be6e9b7fa90` is the tree of **commit 75 of 129** —
mid-loop. No first/last-iteration boundary artifact.

Each elimination was established, not assumed:

- **git did not prune it.** `git gc --auto` estimates loose-object count by
  sampling `objects/17` and needs ≥28 entries there; this fixture puts **0** there
  (measured), so `gc --auto` declines *even with `gc.auto=1`* (measured). The
  publisher also had 0 packs, so `too_many_packs` cannot fire either.
- **Even if gc had run, it could not have taken this object.** By push time
  commit 75 is an ancestor of `main`, so its tree is *reachable*, and git never
  prunes reachable objects. This is the decisive one.
- **Not ENOSPC.** All 129 commits succeeded. A loose-object write that hits
  ENOSPC fails the commit loudly; the fixture `t.Fatalf`s on any git error.
- **Not fd exhaustion in git.** EMFILE/ENFILE at object-open would print
  `unable to open loose object ...: Too many open files`. Absent. And a starved
  fd limit does not break the push at all (control above). Note the same test
  binary *did* log `accept: too many open files` 84 s earlier — real evidence the
  runner was in resource distress, but not the mechanism for this failure.
- **Not the fixture's own concurrency.** The commits are sequential.

What remains: a reachable loose object was removed from
`publisher/.git/objects/01/` between commit 75 and the push, by something outside
the fixture's git. **The deleter was not identified**, and one CI log does not
contain enough to name it. That is stated as a limit, not papered over.

## What was changed, and what it does and does not claim

Not a retry. A retry would convert this into a slow pass and destroy the evidence
that something is killing git subprocesses on the runner — which is worth knowing
regardless of this test.

1. **`advance()` builds its commits with one `git fast-import`** instead of an
   `add`+`commit` pair per commit. Measured for `advance(129)`: **394 git
   processes → 9**. The window between writing object 75 and reading it drops
   from ~2 s to one process's lifetime, and the test went from ~5.3 s to ~0.4 s.
2. **Fixture git no longer forks detached background daemons.** `git commit`,
   `fetch` and `push` each end by forking `git maintenance run --auto --detach`,
   which daemonizes and outlives its parent. `advance(129)` left **130** of them
   running against a repo the test was still driving. `maintenance.auto=false`
   stops the fork; `gc.auto=0` does **not** — each daemon still forks and only
   then declines (both measured). The push's *remote* half runs under the bare
   origin's own config, so that one needs the setting written into origin.
   Measured detached-maintenance trace records for `advance(129)`: **260 → 0**.
3. **A fixture git failure now prints its own diagnosis.** `gitFailureForensics`
   probes any OID named in git's output and reports PRESENT-with-size versus
   ABSENT (distinguishing a missing object from a missing fanout directory), plus
   loose/pack counts and `fsck`. The work above had to be done from outside; a
   recurrence should not need it again.

**Honest scope:** (1) and (2) narrow exposure — fewer git processes, a shorter
window, and this test stops being a load *source* for its neighbours. They do not
prove a cure for an unidentified external deleter, and the 1-in-87 base rate is
far too low for any local run to demonstrate one. (3) is what makes the next
occurrence cheap to diagnose.

## Shared cause with mg-d578?

**No.** mg-d578 turned out to be fixture state — a mutable test-only package var
plus watchers leaked into the following test — in `internal/claude`, reproducible
on demand under `-race`. This is a 1-in-87 CI-only object disappearance in
`internal/agent` with no race involvement. Nothing links them beyond both being
flakes reported the same evening.

What *is* worth recording as a shared environmental finding: in that same CI run
the `internal/agent` binary logged `accept: too many open files` and churned
~14 000 pids in 84 seconds spawning real PTY agents. Whatever removed the object
happened on a runner under genuine resource distress. If a second unexplained
object disappearance shows up, that is the direction — and the forensics block
above is what will say so.
