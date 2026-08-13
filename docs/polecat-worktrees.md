# Polecat Worktree Isolation

## Problem

Polecats (disposable worker agents) currently work directly in whatever repo path is passed as `--repo`. If that's the same checkout a crew member or user is using, `git checkout -b polecat-<id>` switches the branch in their working directory. This causes branch conflicts, dirty tree errors, and confusion.

## Solution

pogod creates a git worktree per polecat before launching it. The polecat works in its own isolated worktree. pogod cleans up the worktree after the polecat exits.

## Why worktrees, not clones

- `git worktree add` takes ~1 second vs cloning which can take minutes for large repos
- Worktrees share the object store with the parent repo — no disk duplication
- `git worktree remove` is clean and atomic
- This is the same approach Gas Town uses successfully

## Layout

```
~/.pogo/polecats/
└── <name>/           # git worktree, one per polecat
    ├── .git          # worktree link file (not a full .git dir)
    └── ...           # repo contents on a fresh branch
```

## Lifecycle

### Spawn

In `handleSpawnPolecat` (internal/agent/api.go), before calling `r.Spawn()`:

1. Resolve the repo path to the actual git root
2. Create the worktree:
   ```
   git -C <repo> worktree add ~/.pogo/polecats/<name> -b polecat-<name>
   ```
3. Set the polecat's working directory to the worktree path
4. Store the worktree path and source repo on the Agent struct for cleanup

### Work

The polecat's cwd IS the worktree. It's already on branch `polecat-<name>`. The polecat:
- Runs `mg claim <id>`
- Makes changes, commits
- Runs `git push origin polecat-<name>`
- Runs `pogo refinery submit polecat-<name> --repo=<worktree-path>`
- Runs `mg done <id>`
- Exits

### Cleanup

In the `onExit` callback (cmd/pogod/main.go):

1. Remove the worktree:
   ```
   git -C <source-repo> worktree remove ~/.pogo/polecats/<name> --force
   ```
2. Optionally delete the local branch if the refinery (the merge queue) already merged:
   ```
   git -C <source-repo> branch -d polecat-<name>
   ```
3. Log cleanup event

### Stale worktree recovery

On pogod startup, run `git worktree prune` on repos with known worktrees. This handles the case where pogod crashed and left stale worktrees behind.

## Ownership: the path names the polecat, the branch does not

The garbage collector (`internal/gitgc`, run by pogod on a ticker and by
`pogo gc`) reclaims concluded polecats' worktrees. Deciding *whether a worktree
is still in use* is the one question it must never get wrong, and there are two
strings that look like they answer it:

| String | Answers | Sound key for |
|---|---|---|
| the worktree's **path basename** (`~/.pogo/polecats/<name>`) | whose tree this is | removing a **directory** |
| the checked-out **branch's** `polecat-` suffix | whose work this is | deleting a **ref** |

They agree only while a polecat stays on the branch it was spawned on. A
polecat that checks out a *foreign* branch — which the review and QA roles are
instructed to do, and which anyone fixing conflicts on an existing PR will do —
makes them disagree.

The GC used to read liveness off the **branch** (gh #94). A live polecat on a
foreign branch was therefore invisible to the liveness gate: it inherited the
foreign, concluded ticket's state, its worktree was removed **mid-task**, and
freeing its branch waived the branch-deletion guard so the ref went too. The
agent kept running, `pogo agent list` kept reporting it healthy, and the work
survived only because the commit was still loose in the shared object store — a
`git gc --prune=now` would have finished the job.

**The rule now:** worktree liveness is keyed on the path
(`gitgc.PolecatNameForWorktree`), branch deletion on the branch suffix. Neither
substitutes for the other. This makes the spawn-time path layout load-bearing:
`~/.pogo/polecats/<name>` is not a convention, it is the record of who owns the
tree, and a spawn that named the directory anything else would make every live
polecat invisible to the gate again.

The inverse failure is not traded away. A polecat that has exited is in no live
set under any key, so a normal exit still reaps its tree — the GC's ability to
collect is asserted by a control in the same test as the fix
(`TestSweepKeepsLivePolecatOnForeignBranch`).

### Ticket state follows the same rule (mg-bdda)

The rule above was applied to *liveness* first and left ticket-state
classification split. Phase 1 (registered worktrees) classified by the
**checked-out branch**; phase 1b (orphan dirs — no `.git`, no registration) has
no branch to read and classified by the **owner** from the directory name. For
one dead owner and one directory name they reached opposite conclusions, and
the only thing deciding which was whether git still held the registration:

```
owner 0047 (mg-0047 archived, dead), tree parked on foreign in-flight polecat-a773

  registered worktree -> KEPT     "ticket in-flight"            <- inherited from a773
  orphan dir          -> REMOVED  "orphan dir, ticket archived"
```

The two sets are disjoint within a sweep, so nothing contradicted itself, but a
dead polecat's tree could be pinned forever by a foreign ticket that never
concludes — and a `git worktree prune` between two sweeps flipped the verdict
on the same files.

**Both phases now classify by the owner** (`TicketIndex.OwnerState`,
`gitgc.classifyTree`), because the owner is what the *directory* is a fact
about: the tree was made for that polecat's work and nothing else will come
back to it. Reaping it loses nothing. The ref keeps its own gate — phase 2
still classifies branches by `BranchState` — so removing a tree parked on an
unconcluded foreign branch merely un-checks-it-out, leaving every commit
reachable, while uncommitted files are held back by the dirty guard (mg-ee02).

**The branch is now a fallback, not a second gate.** An owner-only rule would
strand every worktree whose basename resolves to no work item — legacy layouts,
hand-made review trees — which is the symmetric defect gh #94 warned against:
never reaping a dead tree. When the owner resolves to nothing the branch
decides instead, and the log line says so:

```
… (owner workshop, branch polecat-aaaa): branch's ticket archived (owner "workshop" resolves to no work item)
```

It is deliberately *not* an additional must-also-be-concluded condition; that
direction would preserve the indefinite pin. The cost is one direction becoming
more conservative: a tree whose owner is still in flight is now kept even if the
branch inside it has concluded, which is right — a respawn lands on that same
path.

#### Why phase 1b has no dirty guard

mg-ee02 added a `WorktreeDirty` check to phase 1 only, and phase 1b still
`os.RemoveAll`s. That is the answer, not an oversight: `WorktreeDirty` shells
out to `git -C <path> status`, and an orphan dir has no `.git` by construction —
that is what makes it an orphan and what gets it past the still-linked check.
There is no index and no HEAD to compare its files against, so "uncommitted" is
not a property the directory has. The owner's concluded ticket is the only
signal available about the leftovers.

## Who counts as live: two sources, one answer

The ownership rule above says which *string* names a worktree's owner. A
separate question is where the set of live owners comes from, and it has bitten
twice.

There are two sources and **neither is complete alone**:

| Source | Complete when | Blind when |
|---|---|---|
| pogod's in-memory **registry** | pogod has run continuously since the polecat spawned | after a pogod restart — permanently, for every survivor, because the registry has no adopt/reattach path (mg-13a3) |
| the persisted **polecat witness** (`~/.pogo/polecat-witness.json`) | the polecat was spawned by any pogod on this box | the witness was never written or was dropped at exit |

A restart is not exotic and the survivors are not exotic either: every polecat's
normal end state is `mg done` followed by *staying alive* until the mayor stops
it. In that window its ticket reads concluded while its process and tree are
still in use — and worktree removal, unlike branch deletion, has **no merge
gate**, so the live set is the tree's only guard. A registry-only live set after
a restart is therefore an empty guard on exactly the population that needs it
(mg-0130).

**The rule:** the live set is the registry **unioned with** the witness, and a
witnessed polecat counts as live on `Alive` *or* `Unreadable` — never proving it
is ours is a reason to keep the tree, not to reclaim it. A witness that is on
disk but unreadable is **not** an empty fleet: both callers decline to sweep
(pogod skips the pass, `pogo gc` exits nonzero) rather than sweep against a set
they know is missing survivors.

That union lives in **`agent.LivePolecatSet`**, called by both `cmd/pogod`'s
sweep and `pogo gc`. It was originally written in `cmd/pogod` only, and `pogo
gc` — the manual entry point to the same `gitgc.Sweep` — kept its own
registry-only copy, so the restart hole survived one caller over and `pogo gc
--apply` would take a running polecat's tree (mg-1403). Two independent defects
in one gate is an argument about the gate's shape rather than its key: there is
now one definition, and a third caller gets the same answer by construction.

## Reading the GC log

The sweep logs one line per action. Before gh #94 it logged counts only, so a
removal in a multi-megabyte pogod log was a bare number and "did the GC take my
worktree, and why" could not be answered after the fact.

```
pogod: git GC /Users/x/dev/pogo — removed worktree /Users/x/.pogo/polecats/caa65 (owner caa65, branch polecat-dccb): ticket archived
pogod: git GC /Users/x/dev/pogo — kept worktree /Users/x/.pogo/polecats/beef (owner beef, branch polecat-beef): 3 uncommitted change(s) — rerun with --force to discard
```

Each line names the repo swept, what happened, which tree, **whose** tree
(`owner`), what was checked out in it (`branch`), and the reason. `owner` and
`branch` are printed separately on purpose: when they differ you are looking at
exactly the situation gh #94 was about.

Only *actions* log — removals, and trees deliberately preserved. A
kept-because-live line per polecat per tick would be noise.

### When `git status` could not be read (gh #97)

The reason field above is the whole forensic value of the line, so it must never
name an innocent cause for a destructive act. It used to: a worktree whose `git
status` **errored** was force-removed and the line said `ticket archived`, with
the status error appearing nowhere. A missing line prompts investigation; a
plausible one ends it.

Two shapes now appear, and the reason tells you which you are holding:

```
… kept worktree …/polecats/beef (owner beef, branch polecat-beef): git status could not be read (status …: fatal: not a git repository: /nonexistent/garbage) (untouched 30 days) — refusing to act on a tree we could not read; rerun with --force to discard
… removed worktree …/polecats/beef (owner beef, branch polecat-beef): owner's ticket archived; git status could not be read (…) — removed anyway because --force was given
```

The first is the outcome for **every** unreadable tree: if we could not read it,
we do not act on it — under any ownership and at any age. It is a **permanent**
keep, pinned until a human clears it or `--force` takes it. `untouched 30 days`
is there to make that decision cheap; the age is reported and never acted on,
and no amount of it will turn this keep into a deletion.

The second is the only removal on this path, and it exists because a human asked
for it. Note that it names the status failure too: a removal line you can
reconstruct the whole decision from is the point — the ticket state, the
instrument that failed, and the fact that an operator overrode a refusal.

Where the worktree's contents cannot be listed at all, the same keep says
`age unknown — the tree could not be listed` rather than omitting the age, which
would otherwise read as though the tree were fresh.

## The retained population: `pogo gc --list-preserved` (mg-f4c0)

Everything above describes how a tree comes to be **retained**. Nothing above
describes how one stops being retained, and for a long time nothing did.

pogod preserves a polecat's worktree when it exits holding uncommitted work, and
every half of that mechanism works: the guard refuses, the coordinator is mailed
naming the tree and the reclaim command, a `worktree_preserved` event lands on
the spine. What did not exist was anything on the **read** side. The mail fires
once into the middle of other traffic and is never repeated; the event is a
stream, not a population. So the trees accumulated — **six** when this was
filed, **twenty-three** by the time it was fixed, across four repositories —
each pinning a branch that cannot be deleted, and each posing a question ("is
this uncommitted work worth rescuing?") that nobody was assigned to ask. The
only instrument that could see the population was `ls ~/.pogo/polecats`.

```bash
pogo gc --list-preserved                 # every repo — this is the point
pogo gc --list-preserved --repo /path    # narrowed, when you already know
pogo gc --list-preserved --json          # the full record, nothing capped
```

It changes nothing, reclaims nothing, and refuses nothing.

**It is rooted at the polecats directory, not at a repo.** `pogo gc` takes one
`--repo` and reports what it would do there; retained trees accumulate wherever
the fleet works, so a repo-scoped listing reports a fraction of the population
while looking complete.

**It reports facts and no verdict, and that restraint is the design.** Per tree:
the owner, the branch it pins, its work item and that item's state, how long it
has gone untouched, and every uncommitted path split into modified and
**untracked**. It does not say whether a tree may be reclaimed.

The reason is a measured one. `~/.pogo/polecats/p687f` held seven modified
files, all of them `code/**/out_*.txt` — regenerated suite output, a pure
function of repo state, reproducible in seconds. A reader sampled two of the
seven, saw only timing churn, and concluded "residue, safe to reclaim"; the
third file held three new registry entries and a count going 20 → 23. Any
classifier cheap enough to run over the whole population would have made that
mistake **systematically** rather than once. Reclaiming is one already-existing
command and was never the hard part; knowing which of twenty-three trees can
safely take it is, and that is a question about the files.

**Untracked paths are never truncated in the listing; modified entries are
capped and the cap announces itself.** A modified tracked file has a committed
version in the object store. An untracked path is on no branch, in no stash and
on no remote — the tree is its only copy anywhere on the machine, which is how
`~/.pogo/polecats/qbe37` came to hold an entire 1450-line package.

**It states the blast radius of the reclaim command, per repository.** `pogo gc
--repo=<repo> --apply --force` is repo-scoped *and* forced: it acts on every
eligible retained tree in that repository, not on the one you inspected. That is
the reason a careful reader does nothing, so the listing groups by repo and
names which trees a single run would take:

```
/Users/x/dev/pogo
  reclaiming ANY of these is `pogo gc --repo=/Users/x/dev/pogo --apply --force`,
  which is repo-scoped and forced — it takes ALL 8 eligible tree(s) here at once,
  not the one you inspected, and it DISCARDS whatever is still uncommitted.
    it would reclaim: 622f, p49b1, p6c90, qbe37, qdb58, t83c5, wda30, z48d8
    it would NOT touch (work item not concluded): b4b01, c1fcc, c8074, p9d4e
```

That second line is not decoration. **`--force` is not the whole gate**: the
sweep checks the owner's ticket state *before* it consults the dirty guard, so
`--force` overrides the guard and never the state check, and a retained tree
whose work item is still in flight survives the flag entirely. The column is
pinned against what `Sweep` actually does with the same inputs, not against a
restatement of the rule.

A dirty tree whose owner is a **running** polecat is not retained — it is in
use, and is reported separately so the headline count is the population that
needs an owner rather than the population that has one. Clean trees and
non-worktree orphan dirs are counted too, so the listing is a partition of the
directory rather than a selection out of it.

The preservation notice now names this list, says it will not itself be
repeated, and states that the reclaim command it recommends is repo-scoped and
forced.

## What changes

### internal/agent/api.go — handleSpawnPolecat

Before spawning:
```go
// Create worktree for polecat isolation
worktreeDir := filepath.Join(home, ".pogo", "polecats", spawnReq.Name)
branchName := "polecat-" + spawnReq.Name
cmd := exec.Command("git", "-C", spawnReq.Repo, "worktree", "add", worktreeDir, "-b", branchName)
if err := cmd.Run(); err != nil {
    http.Error(w, fmt.Sprintf("worktree creation failed: %v", err), http.StatusInternalServerError)
    return
}
```

Set `cmd.Dir` on the Claude process to the worktree path.

### internal/agent/agent.go — Agent struct

Add fields:
```go
WorktreeDir string // path to polecat's worktree (for cleanup)
SourceRepo  string // path to the source repo (for worktree removal)
```

### cmd/pogod/main.go — onExit callback

After existing cleanup:
```go
if a.WorktreeDir != "" {
    exec.Command("git", "-C", a.SourceRepo, "worktree", "remove", a.WorktreeDir, "--force").Run()
}
```

### internal/agent/prompts/templates/polecat.md

Remove `git checkout -b` and `cd {{.Repo}}` from the template — the worktree handles both. Update step 3:
```
3. **Commit and push your branch:**
   ```bash
   git add <files>
   git commit -m "<type>: <description> ({{.Id}})"
   git push origin polecat-{{.Id}}
   ```
```

### buildPolecatPrompt in api.go

Same update — remove the `cd` and `git checkout -b` from the generated prompt.

## Edge cases

- **Repo has uncommitted changes**: `git worktree add` is unaffected by the parent's working tree state.
- **Two polecats on same repo**: Each gets its own worktree with a unique branch name. No conflict.
- **Polecat crashes**: `onExit` still fires via `cmd.Wait()`. Worktree gets cleaned up.
- **pogod crashes**: Stale worktrees accumulate. Startup prune handles this.
- **Worktree creation fails**: Fail the spawn, return error to caller. Don't fall back to cloning.

## What doesn't change

- Crew agents work in their own long-lived clones
- The refinery uses its own worktrees under `~/.pogo/refinery/worktrees/`
- The mayor doesn't need a worktree (it coordinates, doesn't write code)
- No new config knobs — `~/.pogo/polecats/` is the fixed location
