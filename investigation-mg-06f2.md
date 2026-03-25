# Investigation: Tickets Archived Without Merging (mg-06f2)

## Root Cause

Polecats mark work items "done" **before the refinery confirms the merge**. When the refinery later rejects the branch (quality gate failure, rebase conflict, etc.), the work item is stuck in `done/` with no path back to `available/`.

## Detailed Trace

### The failure sequence

1. Polecat pushes branch and runs `pogo refinery submit` (async — just queues)
2. Polecat immediately runs `mg done` — moves item from `claimed/` to `done/`
3. Polecat exits
4. Refinery picks up the MR, processes it → **fails** (build/test, rebase conflict, etc.)
5. `onFailed` callback sends mail to mayor + author polecat (but polecat already exited)
6. `onMerged` callback (which auto-archives via `ArchiveMGDoneItems()`) **never fires**
7. Work item sits in `done/` forever — not available, not archived, not reclaimable

### Why `mg reap` doesn't help

`mg reap` only reclaims items from `claimed/` whose owning process (PID) is dead. Items in `done/` are invisible to it.

### Why the mayor can't recover

The mayor prompt says to check refinery history before stopping polecats — but the polecat already called `mg done` and exited on its own. The mayor has no tool (`mg undone`, `mg reopen`) to move items back from `done/` to `available/`.

## Contributing Factors

### 1. Polecat template (`internal/agent/prompts/templates/polecat.md`)

Steps 4-6 are fire-and-forget:
```
4. pogo refinery submit ...   ← async, just queues
5. mg done ...                ← immediate, no verification
6. Exit.
```

**No verification that the merge succeeded.** The template doesn't even mention that the refinery is asynchronous or that submission ≠ merge.

### 2. `mg done` command (`macguffin/internal/workitem/done.go`)

Pure state transition with zero validation:
- Does NOT check if a branch exists
- Does NOT check refinery submission status
- Does NOT check if code was pushed
- Just atomically renames `claimed/<id>.md.<pid>` → `done/<id>.md`

### 3. No recovery mechanism in `mg` or refinery

- No `mg undone` or `mg reopen` command exists
- The refinery `onFailed` callback sends mail but doesn't touch work item state
- `ArchiveMGDoneItems()` only runs in the `onMerged` callback (success path)

### 4. Mayor prompt contradiction

Mayor prompt says:
> "Polecats do NOT exit on their own after finishing work. They remain running until you stop them."

But polecat template says:
> "6. **Exit.** The refinery handles testing and merging."

This contradiction means the mayor's safeguard (check refinery before stopping) is bypassed — the polecat already left.

## Proposed Fixes

### Fix 1: Refinery `onFailed` should reopen work items (code — pogo)

In `cmd/pogod/main.go`, the `onFailed` callback should move the work item back from `done/` to `available/` so it can be re-dispatched. This requires a new `mg reopen` command (or equivalent library call).

**Impact**: Automatic recovery. No human intervention needed.

### Fix 2: Add `mg reopen` command (code — macguffin)

New command: `mg reopen <id>` — moves item from `done/` back to `available/`. The refinery's `onFailed` callback and the mayor can both use this.

**Impact**: Enables both automated and manual recovery.

### Fix 3: Polecat template — don't `mg done` until merge confirmed (prompting — pogo)

Change the polecat protocol so step 5 waits for refinery confirmation:
```
5. Wait for merge confirmation:
   Watch refinery status until merged or failed:
   curl http://localhost:10000/refinery/history | grep <branch>
6. If merged: mg done <id>
   If failed: mail the mayor with the failure details. Do NOT run mg done.
7. Exit.
```

**Impact**: Prevents the problem entirely at the source. Polecats only mark done on confirmed merge.

### Fix 4: Mayor prompt — handle refinery failures on done items (prompting — pogo)

Add explicit instructions for the mayor to:
- On refinery failure mail: check if work item is in `done/`, reopen it with `mg reopen`
- Never assume `done/` means code landed

### Fix 5: `mg done --require-refinery` flag (code — macguffin, optional)

Add an optional flag that makes `mg done` query the refinery API to confirm the branch merged before allowing the state transition. This is defense-in-depth — Fix 3 is the primary fix.

## Recommended Implementation Order

1. **Fix 2** (`mg reopen`) — prerequisite for Fixes 1 and 4
2. **Fix 1** (refinery `onFailed` auto-reopens) — automated recovery
3. **Fix 3** (polecat template) — prevents the problem at source
4. **Fix 4** (mayor prompt) — belt-and-suspenders
5. **Fix 5** (optional, defense-in-depth)
