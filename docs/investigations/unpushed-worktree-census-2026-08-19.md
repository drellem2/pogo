# Census: commits in polecat worktrees reachable from no remote ref (2026-08-19)

**Work item:** mg-f4f7 (split from mg-5b2b).
**Question:** `mg-11fa` rescued the population *"worktrees holding uncommitted work"* — its
predicate was `git status` dirtiness. A worktree that has **committed** its work and pushed
nothing is clean, so it was outside that population by construction. `bf3ae` was one such tree
(five commits, found by architect looking, not by any sweep; now pushed as `c304c5e`). **If the
predicate missed it once it could have missed it up to 43 times. This is the check nobody had run.**

**Answer: zero worktrees are holding work that exists nowhere on a remote.** One tree
(`pc-rev-c5d5a10`) holds a commit reachable from no remote ref, and that commit is patch-identical
to `2cd6bd5`, already on `origin/main` via a second attempt on a differently-named branch. Nothing
was pushed as a result of this sweep, because nothing needed to be.

`bf3ae` was the only instance, and it was already rescued before this ticket was filed.

---

## What the population actually is

`~/.pogo/polecats/` held **67 entries** at the start of the sweep. They are not 67 worktrees:

| Kind | Count | What it is |
|---|---|---|
| Live git worktrees | 48 | A checked-out branch with a real `.git` link |
| Reaped shells | 19 | A directory containing **only** `.pogo/` — gitgc removed the repo content |

The 19 shells (`27bc`, `gt-0373`, `gt-0f18`, `gt-1471`, `gt-1bba`, `gt-54e2`, `gt-6d1b`, `gt-b657`,
`gt-b74d`, `gt-bbaf`, `gt-c1b4`, `gt-e286`, `gt-e584`, `mg-0fa6`, `mg-5b9c`, `mg-843f`, `pc-5f0c`,
`pc-a5g2`, `pc-b329`) hold no commits and cannot be swept — whatever they held is either on a
remote or already gone. `pc-5f0c` additionally has a `lean/.lake` build-residue directory, which is
not a repo either. **The "43 unchecked trees" in the ticket body is an overestimate**: after
subtracting the shells and the 24 dirty trees mg-11fa already covered, the genuinely-unexamined
population was smaller. That does not change the finding, only its denominator.

## Result: 48 live worktrees, 5 distinct repos

`origin` resolves differently per tree, and this is the trap that nearly produced a false report
once already (running `ls-remote` from `~/dev/pogo` against a worktree of another repo returns
empty, which reads as "not pushed"). The five remotes, each queried **from a worktree of that
repo**:

| Repo | `git-common-dir` | Remote heads |
|---|---|---|
| `pogo` | `/Users/daniel/dev/pogo/.git` | 811 → 812 (one appeared mid-sweep) |
| `onethird_program` | `/Users/daniel/research/onethird_program/.git` | 364 |
| `one_third_width_three` | `/Users/daniel/research/one_third_width_three/.git` | 480 |
| `pogo-reminders` | `/Users/daniel/dev/pogo-reminders/.git` | 38 |
| `macguffin` | `/Users/daniel/dev/macguffin/.git` | 154 |

**47 of 48 have their `HEAD` contained in a branch that exists on their own origin right now.**
One does not.

## The one tree with commits on no remote ref

```
tree      pc-rev-c5d5a10
repo      one_third_width_three
branch    polecat-pc-rev-c5d5a10   -- ABSENT from origin
HEAD      4419261  revert: drop hStep variant from width3_one_third_two_thirds
                   (revert c5d5a10, mg-b329 rejection) (mg-05d3)
ahead of origin/main    1
reachable from no remote ref    1
worktree status                 clean
```

**It is not lost work.** `git cherry origin/main HEAD` prints `-` for it: the patch is already
upstream. The revert landed on `origin/main` as `2cd6bd5` — same subject line, same work item — via
the branch `polecat-pc-rev2-c5d5a10`, and `mg-05d3`'s own result sidecar records exactly that:

```json
{"branch": "polecat-pc-rev2-c5d5a10", "merge_id": "mr-d7n7a3qtjv1m5kqar4d0"}
```

`mg-05d3` is `archived`. This worktree is the abandoned **first** attempt; the second attempt
shipped. Pushing `polecat-pc-rev-c5d5a10` would add a duplicate branch and rescue nothing.
Recommendation: nothing to do; the tree is reapable.

## Two trees whose own branch name is not where their work lives

These pass the containment check but would **fail** a naive "is `polecat-<name>` on origin at
HEAD?" test, and are worth recording because that naive test is the obvious one to write:

| Tree | Its own branch on origin | Where HEAD actually is on origin |
|---|---|---|
| `622f` | `polecat-622f` @ `ff4d1a0` — **different sha** | `rescue-622f-mg11fa` @ `f1a6847` |
| `p5058` | `polecat-p5058` @ `6b69746` — **different sha** | `rescue-p5058-mg11fa` @ `5f07158` |

These are mg-11fa's own rescue branches doing their job. A sweep that only compared
`polecat-<name>` to `HEAD` would have reported both as unpushed work.

## Trees at or behind main (nothing to rescue, branch never pushed)

`ab17b`, `install`, `pc456`, `pf4f7`, `t24d2`, `t3bb3`, `t49b5`, `t6fec`, `t771b` are 0 commits
ahead of `origin/main` and their branch is absent from origin. That combination is not a gap —
there is nothing on the branch to push. `p6b2d` is on a **detached HEAD** at `11b8803`, also
contained in `origin/main`.

## Full census

Snapshot `2026-08-19T13:03:45Z`. `AHEAD` = commits over `origin/main`; `NOREM` = commits reachable
from no origin ref; `ON_ORIGIN` = sha of `refs/heads/<branch>` on that tree's own origin; `LIVE REF`
= a branch that exists on origin **right now** and contains HEAD.

| Tree | Branch | Repo | AHEAD | NOREM | ON_ORIGIN | LIVE REF | Work item(s) | Item status |
|---|---|---|---|---|---|---|---|---|
| 75f0 | polecat-75f0 | one_third_width_three | 1 | 0 | a2c4578 | polecat-75f0 | mg-11fa | archived |
| a41b7 | polecat-a41b7 | onethird_program | 5 | 0 | bb1ad06 | polecat-a41b7 | mg-11fa, mg-200d, mg-41b7, mg-6bc2, mg-ba78 | archived |
| ab17b | polecat-ab17b | pogo | 0 | 0 | ABSENT | main | mg-b17b (by name) | archived |
| aeaa1 | polecat-aeaa1 | onethird_program | 4 | 0 | 7e5d9c6 | polecat-aeaa1 | mg-11fa, mg-131e, mg-200d, mg-eaa1 | archived |
| b4b01 | polecat-b4b01 | pogo | 3 | 0 | 579b084 | polecat-b4b01 | mg-11fa, mg-4b01 | archived |
| bf3ae | polecat-bf3ae | pogo | 5 | 0 | c304c5e | polecat-bf3ae | mg-f3ae | archived |
| c1fcc | polecat-c1fcc | pogo | 1 | 0 | b65b7fe | polecat-c1fcc | mg-11fa | archived |
| c479c | polecat-c479c | onethird_program | 2 | 0 | 7ac39bb | polecat-c479c | mg-0d1b, mg-11fa, mg-479c | archived |
| c8074 | polecat-c8074 | pogo | 1 | 0 | 9e60524 | polecat-c8074 | mg-11fa | archived |
| ca397 | polecat-ca397 | onethird_program | 1 | 0 | 294d714 | polecat-ca397 | mg-11fa | archived |
| fix-rem | polecat-fix-rem | pogo-reminders | 0 | 0 | 371aee8 | main | mg-8419 (from HEAD subject) | shelved |
| gt-4811 | polecat-gt-4811 | pogo | 1 | 0 | be27aa7 | polecat-gt-4811 | gt-4811 | — |
| gt-6621 | polecat-gt-6621 | pogo | 1 | 0 | ce58cfd | polecat-gt-6621 | gt-6621 | — |
| gt-834a | polecat-gt-834a | pogo | 0 | 0 | ff9023f | main | gt-834a | — |
| gt-b1ce | polecat-gt-b1ce | pogo | 1 | 0 | 77b6f1c | polecat-gt-b1ce | gt-b1ce | — |
| gt-ffbd | polecat-gt-ffbd | macguffin | 0 | 0 | 2e333d2 | main | gt-ffbd | — |
| install | polecat-install | pogo | 0 | 0 | ABSENT | main | mg-6421 (from HEAD subject) | archived |
| p0fc6 | polecat-p0fc6 | onethird_program | 3 | 0 | 6951d77 | polecat-p0fc6 | mg-0fc6, mg-11fa, mg-8d66 | archived |
| p1d05 | polecat-p1d05 | pogo | 1 | 0 | e6e3afc | polecat-p1d05 | mg-1d05, mg-51bf | available |
| p5058 | polecat-p5058 | onethird_program | 1 | 0 | 6b69746 | **rescue-p5058-mg11fa** | mg-11fa | archived |
| p516e | polecat-p516e | pogo | 1 | 0 | d84e002 | polecat-p516e | mg-516e, mg-51bf | available |
| p6476 | polecat-p6476 | one_third_width_three | 1 | 0 | 2ea8d66 | polecat-p6476 | mg-11fa | archived |
| p6b2d | (detached) | onethird_program | 0 | 0 | — | main | mg-6b2d (by name) | available |
| p872c | polecat-p872c | onethird_program | 2 | 0 | 0657c25 | polecat-p872c | mg-11fa, mg-6ff4, mg-872c, mg-9d9e | archived |
| p9d4e | polecat-p9d4e | pogo | 1 | 0 | c74d2f0 | polecat-p9d4e | mg-51bf, mg-9d4e | archived |
| pbdc0 | polecat-pbdc0 | onethird_program | 1 | 0 | 1c5f2ea | polecat-pbdc0 | mg-11fa | archived |
| pc-8419 | polecat-pc-8419 | pogo-reminders | 0 | 0 | d3a5fde | main | mg-8419 (by name) | shelved |
| **pc-rev-c5d5a10** | **polecat-pc-rev-c5d5a10** | **one_third_width_three** | **1** | **1** | **ABSENT** | **NONE** | **mg-05d3, mg-b329** | **archived** |
| pc2e1 | polecat-pc2e1 | pogo | 1 | 0 | 59a1394 | polecat-pc2e1 | mg-c2e1 | claimed (live) |
| pc456 | polecat-pc456 | pogo | 0 | 0 | ABSENT | main | mg-c456 (by name) | claimed (live) |
| pe7ff | polecat-pe7ff | pogo | 1 | 0 | 653feef | polecat-pe7ff | mg-51bf, mg-e7ff | archived |
| pf4f7 | polecat-pf4f7 | pogo | 0 | 0 | ABSENT | main | mg-f4f7 (this sweep) | claimed |
| pfbaf | polecat-pfbaf | pogo | 1 | 0 | ef98694 | polecat-pfbaf | mg-51bf, mg-fbaf | archived |
| r8419 | polecat-r8419 | pogo-reminders | 0 | 0 | 371aee8 | main | mg-8419 (by name) | shelved |
| t24d2 | polecat-t24d2 | pogo | 0 | 0 | ABSENT | main | mg-24d2 (by name) | available |
| t3bb3 | polecat-t3bb3 | pogo | 0 | 0 | ABSENT | main | mg-3bb3 (by name) | available |
| t49b5 | polecat-t49b5 | pogo | 0 | 0 | ABSENT | main | mg-49b5 (by name) | available |
| t6fec | polecat-t6fec | pogo | 0 | 0 | ABSENT | main | mg-6fec (by name) | available |
| t771b | polecat-t771b | pogo | 0 | 0 | ABSENT | main | mg-771b (by name) | available |
| w1d03 | polecat-w1d03 | one_third_width_three | 2 | 0 | a262c7f | polecat-w1d03 | mg-0242, mg-11fa, mg-1d03 | archived |

Work-item ids marked **(by name)** are a name transform (`t24d2` → `mg-24d2`) confirmed to resolve
via `mg show`, not read off a commit — those trees carry no commits of their own, so there is no
commit subject to read them from. They could in principle collide with an unrelated item; nothing
in this census depends on them.

## Eight worktrees were reaped *during* the sweep

`622f`, `p49b1`, `p6c90`, `qbe37`, `qdb58`, `t83c5`, `wda30`, `z48d8` existed when the census
started and were gone ~20 minutes later (68 directory entries → 60). All eight had been verified
`NOREM=0` with their branch on origin at exactly `HEAD` before they disappeared, so nothing was
lost — but **this census is perishable, and gitgc runs concurrently with anyone reading it.** A
sweep like this is only valid at its timestamp. The `pogo` remote also gained a branch mid-sweep
(811 → 812 heads).

## Method, and the controls that make the empty answer readable

An empty result from a sweep is worthless unless the query is shown capable of returning a
non-empty one. Four controls, all run:

1. **`ls-remote` returns a known-pushed branch.** `polecat-bf3ae` → `c304c5e8f3a97ae…`, the exact
   sha the ticket records for the branch architect pushed. The control is a branch that was
   *personally pushed and confirmed*, not a merged-and-deleted one — a deleted branch returns
   empty for the same reason a broken query does, which is how a previous attempt at this control
   nearly certified a broken query.
2. **`ls-remote` returns empty for a branch that does not exist.** `polecat-zzzz-nonexistent` →
   absent.
3. **The "no remote ref" query returns non-empty.** `pc-rev-c5d5a10` → 1 commit. The query is not
   uniformly silent.
4. **End-to-end on this sweep's own branch**, the control this sweep personally pushed and
   confirmed. Commit `d8bcaac` (the commit that added this file), measured on `polecat-pf4f7`:

   | | `rev-list --count HEAD --not --remotes=origin` | `ls-remote --heads origin polecat-pf4f7` |
   |---|---|---|
   | before `git push` | **1** | *(empty — 0 refs)* |
   | after `git push` | **0** | `d8bcaac41ede19d8061b81726a92d233c662a6ec` |

   The pre-push row is the exact signature the sweep hunts for — commits ahead, branch absent from
   origin — produced deliberately and then cleared. Both halves of the query therefore move, on a
   branch whose push this sweep performed and observed, not on one it was told about.
   `d8bcaac` is not this branch's tip any more: the commit was later amended to add this table and
   rebased onto `main`. The two readings above were taken on `d8bcaac` while it was the tip, and are
   left at that sha rather than restated at the current one, because a control re-run after the
   branch already exists on origin can no longer produce the `ABSENT` row that makes it a control.

Per tree, in **its own** repo:

```bash
git -C "$tree" rev-list --count origin/main..HEAD          # AHEAD
git -C "$tree" rev-list --count HEAD --not --remotes=origin # NOREM
git -C "$tree" ls-remote --heads origin                     # authoritative branch list
```

`NOREM` is computed from **local remote-tracking refs**, which are a cache: they can hold a stale
`refs/remotes/origin/<b>` for a branch that has since been deleted upstream, and a `NOREM=0` that
rests on such a ref is a false negative. `pogo` had 821 local tracking refs against 812 real remote
heads, so the discrepancy is real. Every `NOREM=0` was therefore re-checked against `ls-remote`:
list the tracking refs containing `HEAD`, keep only those still present upstream, and require
`git merge-base --is-ancestor HEAD <upstream-sha>`. 47 of 48 pass on a **live** ref; that is the
claim, not the cache's.

`origin/main` was current on all five repos at sweep time (local tracking sha == `ls-remote` sha for
`refs/heads/main`), so no `fetch` was issued and no shared object store was mutated.

## What this does not settle

Several branches in the table are abandoned work from finished or superseded items — `gt-4811`,
`gt-6621`, `gt-b1ce` are unmerged March feature branches, and the `mg-51bf` cluster
(`p1d05`, `p516e`, `p9d4e`, `pe7ff`, `pfbaf`) is five trees against one item. Whether any of that
should be merged, dropped, or re-dispatched is per-item and is not this ticket's call. The point of
the table is that someone else can now make those calls from a list instead of from a guess.
