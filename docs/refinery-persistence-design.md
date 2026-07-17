# Refinery state persistence across pogod restart (mg-abfd)

Design note. Sub-ticket #1 of mg-e574 (gh drellem2/macguffin #15). Facts
verified against origin/main `a8f3665` on 2026-07-02.

## Problem

All refinery state lives in one mutex-guarded in-memory struct
(`internal/refinery/refinery.go:121-140`). A pogod restart empties queue,
history, and the `byID` index; `refinery show <id>` then returns the same
404 as "never existed", and the polecat poll loop
(`templates/polecat.md:100-110`) reads a null STATUS and improvises —
observed exit-and-abandon in the 2026-06-18 incident (mg-c7e8), requiring
mayor re-submit.

State is also lost **without a daemon crash**: the orchestration-restart
path (`SetRefineryStarter`, `cmd/pogod/main.go:813-828`) builds a fresh
empty `Refinery` on every index-only → full transition.

## Decision 1 — storage backend: versioned JSON snapshot (not jsonl, not a DB)

**`~/.pogo/refinery-state.json`, written whole on every state mutation via
the existing atomic pattern: CreateTemp-in-same-dir → Write → fsync →
Rename** — exactly `internal/scheduler/store.go:96-119`, including the
versioned envelope with refuse-newer-version load semantics
(store.go:24-27, 72-74).

Why snapshot over the ticket's jsonl lean: refinery state is *not*
append-only — records mutate status in place, `failureCounts` increments
and clears, and history prunes at 100 entries / 7 days
(refinery.go:573-595). A jsonl log therefore needs replay + compaction
logic; a snapshot needs neither. At the measured volume (~25 MRs/day
average, 55/day peak; state ≤ ~tens of KB since history is capped at 100
entries) a full rewrite per mutation is a handful of milliseconds a few
dozen times a day. BoltDB/sqlite would be the first binary-DB dependency
in the repo (go.mod has none) for a job a 100KB JSON file does.

`MergeRequest` is already fully JSON-tagged (refinery.go:83-107) — the
struct serializes as-is. Persist: `queue`, `history`, `failureCounts`,
plus the new `lost` list (Decision 2). Do not persist: `byID` (rebuilt on
load), callbacks, cfg, worktree clones (recreated on demand,
merge.go:221-266).

Flush points (all already under `mu`): Submit append, dequeue/status
transition, done/history append, cancel, prune, failureCounts change.
Write-through while holding the lock is acceptable at this volume.

## Decision 2 — distinguish STATE_LOST from NOT_FOUND

Add a `lost` list to the state file: MR IDs (with branch + author +
timestamp) that recovery could not carry forward, capped/TTL'd to the last
N=3 restarts. `refinery show` on a lost ID returns HTTP **410 Gone** with
`{"status": "lost", "id": ...}` (vs today's 404 at
`internal/refinery/api.go:159`), and the client surfaces
`status=lost` so callers can auto-resubmit.

With write-per-mutation persistence the loss window is ~zero in normal
operation, so `lost` mainly covers: state file unreadable/corrupt, version
skew, or a processing-item recovery that could not be resolved (below).

Two adjacent gaps to fix in the same pass because they produce the same
ambiguity:

- **Prune-induced 404.** History pruning deletes from `byID` too
  (refinery.go:573-595), so an MR older than 7 days already returns
  "not found" by design. Return a distinct message ("pruned from
  history") — cheap: keep pruned IDs in a small ring in the state file.
- **Polecat poll loop null-STATUS.** `polecat.md:100-110` breaks only on
  `merged|failed`; on 404/410 jq yields null and the loop spins or the
  polecat improvises. Template must branch explicitly: `lost` →
  resubmit once and continue polling; `not found` → escalate to mayor by
  mail and hold (per template step 8, stay alive).

## Decision 3 — recovery semantics: replay queued, resolve in-flight (option c)

On load (both instantiation paths — see Hook points):

1. **`queued` and `held` items → replay.** Safe: no side effects have
   happened yet. (This originally read "beyond the OnSubmit worktree-unlink,
   which already occurred pre-crash" — that hook was deleted in gh #88, so
   submit now has no side effects at all and replay is strictly safer.)
   Preserve FIFO order; `held` re-enters via the QA gate as today.
2. **The `processing` item (at most one — the queue loop is
   single-threaded, refinery.go:366-388) → resolve, don't blindly re-run.**
   The dangerous window is **after `git push`, before the history
   append** (refinery.go:513-536): the commit is live on origin but the
   MR record died. Recovery probes
   `git merge-base --is-ancestor <branch-sha> origin/<target>` in the
   private clone:
   - ancestor ⇒ the merge landed: append to history as `merged`, fire
     OnMerged (mail + work-item reopen) so the post-merge notifications
     that died with the daemon still happen;
   - not ancestor ⇒ clean the private clone (a crash mid-rebase leaves
     an in-progress rebase; today only the *failure* path aborts it,
     merge.go:123, and `ensureWorktree` only checks `.git` existence,
     merge.go:226-243 — recovery must `rebase --abort` + reset) and
     re-queue at head for a fresh attempt.
   - probe itself fails (branch deleted, remote unreachable) ⇒ move the
     ID to `lost` and emit an event.
3. **Residual accepted gap:** a crash between push and the deploy hook
   (deploy.go) can skip a deploy. Record it as a known limitation; if it
   bites, add a `deployed` checkpoint field to `MergeRequest` later.

## Hook points

- Primary init: load between `refinery.New` and `Start`
  (`cmd/pogod/main.go:713-806` — New at :720, Start at :803). Cleanest:
  do load/recovery inside `New` given a store path, mirroring
  `scheduler.New` (scheduler.go:246-266).
- **The `SetRefineryStarter` closure (main.go:813-828) must use the same
  store** — this path fires on every orchestration restart and is a
  state-loss source in its own right.
- Adjacent bug found during investigation, fixed in the same impl ticket:
  that closure re-wired OnMerged/OnFailed but **not OnSubmit**, so after an
  orchestration restart submits stopped unlinking polecat worktrees.
  Superseded — gh #88 deleted the unlink hook itself, so there is no longer
  an OnSubmit callback to carry over, and the re-wire went with it.
- Graceful shutdown: final flush in `Stop()` (refinery.go:391-396) /
  server.go:89-92 — belt-and-braces only, since writes are already
  per-mutation.

## What this deliberately does not do

- No write-ahead log, no DB, no schema migration machinery — the
  versioned envelope's refuse-newer + rewrite-on-save covers evolution.
- No multi-MR concurrency changes; single-threaded loop is untouched.
- No persistence of the agent registry (also lost on restart —
  `agentRegistry.Get(mr.Author)` returns nil post-restart, degrading
  OnSubmit/notify paths). Out of scope here; worth its own ticket if it
  bites.

## Implementation

Filed as a polecat-executable sub-ticket off this design (see mg-abfd
result note for the ID). Rough shape: `internal/refinery/store.go`
(~150 LOC, clone of scheduler/store.go pattern) + load/recovery in
`New` (~120 LOC) + 410-Gone path in api.go/client (~40 LOC) + polecat.md
poll-loop branch + tests (crash-window table tests using the existing
`nowFunc` seam).
