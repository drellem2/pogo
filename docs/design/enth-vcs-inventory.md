# pogo's git assumptions: an inventory for an enth VCS option

Work item: mg-49f3b. Parent: mg-2151d. Date: 2026-11-02. Status: research only. The design comes after this.

Daniel asked on 2026-11-02 for pogo to support [enth](https://github.com/drellem2/enth) as a VCS option out of the box. This document lists every place pogo assumes git (or GitHub). For each one it gives the operation, what pogo needs from it, and the closest enth equivalent in enth's `docs/epics/9-write-path.md` (the write path: `checkout → status → change → land`), read at enth `9cd5d12` (2026-11-02). It also uses enth's `docs/architecture.md` §3–§5 and `docs/milestones.md` where the epic does not cover something.

pogo was read at `858ae31e`. Line numbers are against that revision.

## Classes

- **(a) maps 1:1.** One enth verb does the same job.
- **(b) needs an adapter.** enth can answer the question, but in a different shape: different ids, a different verb, or a different number of steps. This includes git steps that have no enth equivalent **because enth does not need them**. For example, `git rebase --abort` has nothing to clean up, because `enth land` is all-or-nothing (epic #9 §3, I5). The adapter for those is a no-op, and the entry says so.
- **(c) no enth analogue: a design decision is needed.** Entries in this class marked **[keep git]** are GitHub, not git. They should stay as they are; see "Answers", question 2.

## The enth model in one paragraph (for reading the table)

- `enth checkout REV DIR PATHS…` writes a sparse working copy. It records the base Change, the selection and an index in `DIR/.enth/checkout`.
- `enth status DIR` prints the edits as ops.
- `enth change DIR -m …` stores a Change (local `ENTH_STORE`, or enthd with `--remote`) and moves DIR's base to it. Repeating it builds a stack.
- `enth land C1 … Cn [--dry-run] [--expect-tip T] [--request-id R]` rebases the Changes onto the ref tip as a pure function. It checks conflicts path by path and then hunk by hunk, then writes one `L` land record and one ref CAS atomically. It prints `{"type":"landed","new":…,"changes":[{"submitted","landed"}]}`.
- Several git concepts do not exist in enth: a branch name for work in progress, `origin` as a cached remote view, a worktree registry, an ignore file and a tree id (epic §8 Q1).
- Release tags are "refs created by CAS" (architecture.md §4; milestones.md l.179), and they are exported to the GitHub mirror as tags. Epic #9 defines no CLI verb that creates such a ref.
- On a repo GitHub still writes, every ref move is refused (`source_authoritative`, epic §4). Today that is every hosted repo.

## 1. Polecat worktree creation and branch naming

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| W1 | `internal/agent/api.go:2306`; `internal/gitgc/gitgc.go:40` (`BranchPrefix`); duplicate at `internal/strandedwork/strandedwork.go:1129`; used by `cmd/pogod/refineryfailroute.go:84-90` | (naming) branch `polecat-<name>` | A durable name that ties a worker's commits to the worker. Stranded-work, gitgc and failure routing all find work by this name. | none. A checkout has no name, and a Change carries no branch. Candidates: a trailer (`Work-Item: mg-…`; trailers are free-form, epic §2), or a non-trunk ref, which is refused on followed repos (epic §4). | c |
| W2 | `internal/agent/api.go:2402-2406` | `git -C <src> worktree add <dir> -b polecat-<name> [<base>]` | An isolated tree on a fresh branch from the chosen base. Objects are shared with the source repo. | `enth [--remote] checkout <rev> DIR <paths…>`. The base is recorded in `DIR/.enth/checkout`. A path selection has to be chosen: the whole repo, or something narrower. | b |
| W3 | `internal/agent/api.go:1812-1822`, `internal/agent/spawntarget.go:~105-163`, `cmd/pogo/main.go:3001` | (none directly) `--branch` / work item `branch:` picks the base ref and the submit target | Work that targets a non-default branch, such as a release branch. | The ref to check out and to `land` onto. Non-trunk refs cannot move on a followed repo (§4); see L4. | b |
| W4 | `internal/agent/api.go:2993` (`remote get-url origin`), `:3066-3068` (narrow `fetch +refs/heads/T:refs/remotes/origin/T`), `:3078` (wide fetch), `:3005`/`:3116` (`symbolic-ref refs/remotes/origin/HEAD`), `:3001`/`:3011` (`rev-parse --verify origin/<T>`, `origin/main`) | fetch, then resolve the freshest base through a fallback chain | Base the worktree on the current remote tip of the target, not a stale local copy. | `enth --remote checkout <ref>` resolves the ref on enthd at call time. There is no cached remote view to refresh, so the fetch and the fallback chain become one call. | b |
| W5 | `internal/agent/api.go:2802` (`rev-parse refs/heads/polecat-x`), `:2857` (`worktree list --porcelain`), `:2883` (`rev-list --count base..polecat-x`), `:2964` (`branch -D`) | find a leftover branch, check it is spent, delete it | A respawn must not collide with a previous worker's branch or lose its work. | No branch can collide. The equivalent check is "does the old DIR hold Changes that have not landed": read `.enth/checkout` for the base, then query the land records. | b |
| W6 | `internal/agent/api.go:2782` (`worktree remove --force`), `:2791` (`branch -D`) | roll back a failed spawn | Leave no half-made worktree or branch. | `rm -rf DIR`. There is no registry to update. | b |
| W7 | `internal/agent/strandedadopt.go:157` (via `api.go:2442`) | `merge-base --is-ancestor <adopted> polecat-x` | `--stranded-adopt`: prove the new tree really starts from the stranded work it adopts. | `enth checkout <unlanded Change id> DIR`, then read the base back from `.enth/checkout`. | b |
| W8 | `internal/agent/command.go:210` (`rev-parse --show-toplevel --git-path info/exclude`, then writes exclude patterns); `internal/gitgc/gitgc.go:880-935` (the `.claude/` carve-out) | ignore pogod's own files | pogod writes `.claude/` hook settings and persona files into every worktree. git must neither commit them nor count them as dirt. | **none.** Epic #9 §2 says "New files are `put`" and names no ignore mechanism. So pogod's own files would be built into the polecat's Change and landed. | c |
| W9 | `internal/gitgc/preserved.go:732-750`, `internal/gitgc/treerepos.go:61`, `cmd/pogo/checkstranded.go:523,540`, `internal/config/pogohomerepo.go:48`, `internal/gitgc/worktreecommits.go:109-115` | read the `.git` pointer / `rev-parse --git-common-dir` | Find the source repo from a worktree. Worktrees share the source repo's `.git` (objects, refs, config). | `DIR/.enth/checkout` records the base and the selection. Epic #9 does not say whether it records the store or remote, so that field may have to be added. | b |

## 2. The polecat's own protocol and the pre-submit dirty check

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| P1 | `cmd/pogo/worktreedirty.go:54,77`; `internal/gitgc/gitgc.go:840,880-882` (`WorktreeDirty`, `porcelainStatus`) | `rev-parse --show-toplevel`, `status --porcelain [--untracked-files=all]` | `pogo worktree-dirty`: is there uncommitted work (excluding pogod's `.claude/`)? Also gitgc's preserve guard. | `enth status DIR` gives the edits as ops; empty means clean. The `.claude/` exclusion depends on W8. | a |
| P2 | `internal/agent/prompts/templates/polecat.md:136-153` (`presubmit_check`), same block in `polecat-architect.md:193-209` | `git fetch origin`, `rev-list --count origin/$T..HEAD`, `rev-parse HEAD` == `origin/$BRANCH` | Refuse to submit a branch that would deliver nothing, or that is not pushed. | "`.enth/checkout`'s base is a Change I made, and it is stored on enthd". Built from `.enth/checkout` and an enthd query. | b |
| P3 | `polecat.md:122-123` and every template | `git add`, `git commit -m "… (mg-x)"` | Record the work with an author and a message. | `enth change DIR -m … [--trailer …]`. The identity chain reads `GIT_AUTHOR_*`, so agents' git setup carries over (epic §2). | a |
| P4 | `polecat.md:124`; `scripts/lib/fake-agent.sh:76` | `git push origin "$BRANCH"` | Make the work durable off the host and visible to the refinery. | No separate step: `enth --remote change` stores the Change on enthd. A local-store change would need copying to enthd, and the epic has no verb for that. | b |
| P5 | `polecat.md:97` (`BRANCH=$(git rev-parse --abbrev-ref HEAD)`), and the submit call's positional `<branch>` | name the work | `pogo refinery submit <branch>` identifies the work by branch name. | none. The work is identified by a list of Change ids. The submit API has to change shape; this follows from W1. | c |

## 3. The refinery

Context: the refinery has its own clone per repo (`git clone --no-local`, `internal/refinery/merge.go:1025`, under `$POGO_HOME/refinery/worktrees/<base>`). Every git call goes through `gitCmdOutput` (`merge.go:1507`) or a direct `exec.Command`. The pipeline is `attemptMerge`, `merge.go:648`.

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| R1 | `internal/refinery/submitbranch.go:66` (`ls-remote --heads origin B`), `:76,:98` (`rev-parse refs/heads/B`), `:123` (`for-each-ref --contains <sha> refs/remotes/origin/`) | submit-time validation | Does the submitted work exist where the refinery can reach it? | A query on enthd for the submitted Change ids (`not_found` otherwise, epic §3.3). | b |
| R2 | `internal/refinery/refinery.go:848,862` | `ls-remote --heads origin T`, `rev-parse refs/heads/T` | Does the target exist? | A query for the ref's tip. | a |
| R3 | `internal/refinery/refinery.go:887,897,912`; `internal/strandedwork/strandedwork.go:1279`; `internal/agent/api.go:3005,3116` | `symbolic-ref refs/remotes/origin/HEAD`, `ls-remote --symref origin HEAD`, `symbolic-ref HEAD` | Find the repo's default branch. | "Trunk. The one ref that matters" (architecture.md §4). The name has to come from enthd or config. | b |
| R4 | `internal/refinery/refinery.go:939-964` | `update-ref`, `fetch`, `push origin refs/remotes/origin/S:refs/heads/T` | `--auto-create-target`: create a target branch from the default branch. | Creating a ref by CAS (architecture.md §4) has no verb in epic #9, and is refused on followed repos (§4). | c |
| R5 | `internal/refinery/merge.go:992-1067` (`clone --no-local`, `remote get-url`, `remote set-url`, `rev-parse --is-bare-repository`), `gatelock.go:322` | a private clone per repo | A tree the refinery owns, to rebase and gate in, isolated from the user's checkout. | No clone is needed for the land itself. A checkout is still needed for the gate (see R13). | b |
| R6 | `internal/refinery/recovery.go:182,195` (`rebase --abort`, `reset --hard`), `merge.go:689` (`rebase --abort`), `gatedirt.go:322` (`rev-parse --git-path rebase-merge|MERGE_HEAD|…`) | crash and failure cleanup | Leave no half-done rebase or merge behind. | none needed: `land` is all-or-nothing and leaves no in-progress state (I5). No-op adapter. | b |
| R7 | `internal/refinery/recovery.go:197`, `merge.go:651,789` | `git fetch origin [T]` | Update the local view of the branch and the target. | none needed: enthd reads are live. No-op adapter. | b |
| R8 | `internal/refinery/recovery.go:201,207` | `rev-parse refs/remotes/origin/{B,T}` | The branch head, and the target tip at start (`TargetAtStart`, `emptybranch.go:121`). | The submitted Change ids, and a query for the ref's tip. | a |
| R9 | `internal/refinery/recovery.go:213,225`, `emptybranch.go:82,111` | `merge-base --is-ancestor` | Is this head already landed, or does an earlier MR's merged SHA contain it? Used for the already-merged path and crash recovery. | The land record pairs `{submitted, landed}` (epic §3), and `--request-id` replays the answer (I12). That is stronger than ancestry, because it survives a rebase. | b |
| R10 | `internal/refinery/merge.go:668` | `checkout -B B origin/B` | A fresh local copy of the submitted branch. | `enth checkout <top Change id> DIR`. | b |
| R11 | `internal/refinery/merge.go:680` | `git rebase origin/T` | Put the work on top of the current target. | `enth land … --dry-run` runs the same pure rebase. It returns the landed ids and conflicts as structured JSON, and writes nothing. | b |
| R12 | `internal/refinery/closingref_gate.go:86-87` (via `merge.go:709`) | `git log --format=%H%x00%B origin/T..B` | The commit messages, checked for GitHub closing keywords. | Query the submitted Changes' message fields. | a |
| R13 | `internal/refinery/merge.go:1095` (`runQualityGates` in `wtDir`, after the rebase) | (the gate runs in the rebased worktree) | **Gate the exact tree that will land.** | **none.** A dry run writes nothing, so its landed Changes cannot be checked out. A tree id is still open (epic §8 Q1), and pre-land preconditions are M3. Options: gate the submitted Changes on their own base and trust the hunk merge; gate a local reconstruction (checkout the tip, then apply the member ops); or ask enth for a materialised dry run. | c |
| R14 | `internal/refinery/merge.go:610,716` | `rev-parse HEAD^{tree}` | Reuse a passed gate run when a retry produces the same tree. | none: no tree id (epic §8 Q1). The landed id identifies the result only from the same tip. | c |
| R15 | `internal/refinery/gatedirt.go:76` (`status --porcelain --untracked-files=no`), `:121` (`reset --hard HEAD`), `:358-359` (`diff --name-only origin/T...origin/B`) | detect and discard gate-written files, and name the branch's paths | Separate "the gate dirtied the tree" from "the branch did". | `enth status DIR` for dirt, a fresh checkout to reset, and the submitted Changes' ops for the paths the branch touches. | b |
| R16 | `internal/refinery/merge.go:795,806,817` | `checkout -B T origin/T`, `merge --ff-only B`, `push origin T` | Advance the target to the gated result, and lose a race safely. | `enth land C1…Cn --expect-tip T`: one atomic CAS (`stale_tip` on a race). Three steps become one. | a |
| R17 | `internal/refinery/merge.go:832` (into `lanes.go:266`) | `rev-parse HEAD` → `merged_sha` | Record what landed. Deploy hooks, `--post-merge-tag` and revision probes use it. | The `new` id in the `landed` line. Consumers that expect a **git** sha need the sha↔change map from `enth-git-export` (deterministic, architecture.md §5). | a |
| R18 | `internal/refinery/postmerge.go:102-155` (`ls-remote origin refs/tags/X`, `tag -d`, `tag -a X <sha>`, `push origin refs/tags/X`) | `--post-merge-tag` | Put an annotated release tag on the merged commit, idempotently. | A release is "a ref created by CAS" that is exported as a tag (architecture.md §4, milestones.md l.179). Epic #9 has no verb for it, and it is refused on followed repos. | c |
| R19 | `internal/refinery/fastforward.go:23-60` | `rev-parse --is-inside-work-tree`, `symbolic-ref`, `status`, `fetch origin T`, `merge --ff-only FETCH_HEAD` | Fast-forward the user's own checkout after a merge. | Check out again. `enth checkout --sync` does not exist yet (epic §8 Q3). | b |
| R20 | `internal/refinery/merge.go:928` | `push origin --delete B` | Delete the merged branch. | none needed: there is no branch. No-op adapter. | b |
| R21 | `internal/refinery/prune.go:55-95` | `fetch --prune`, `checkout main`, `reset --hard origin/main`, `branch --merged main`, `branch -d` | `pogo refinery prune`: tidy the clones. | none needed: no clones and no branches. No-op adapter. | b |
| R22 | `internal/refinery/merge.go:381` | `config --get remote.origin.url` | Name the remote and its transport in failure records. | The enthd endpoint from `enth --remote` config. | b |
| R23 | `internal/refinery/merge.go:1475-1486,1507` (`gitIdentityEnv`) | committer "pogo refinery" set on the rebase | The rebase needs a committer identity. | `land` keeps the author and committer, and records the lander in the `L` record (epic §3). | a |
| R24 | `internal/ghtoken/childenv.go:156` (`GitNeedsCredential`), used by `gitCmdOutput` and the fetch calls in `agent/api.go` and `strandedwork.go` | credential for `fetch/push/pull/ls-remote/clone` | Authenticate git over https to github.com through `gh auth git-credential`. | An enthd token (`Access::Write`, epic §5). | b |

GitHub operations in the refinery are listed under §9, H1.

## 4. Stranded work (`check-stranded`, `strandedgate`, `strandwatch`, the drain)

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| S1 | `internal/strandedwork/strandedwork.go:973` (callers `agent/strandedgate.go:83,368`, `strandedsweep.go:215`, `strandwatch.go:1536`) | `git fetch --quiet origin` | Fresh remote refs before judging. | none needed: reads are live. No-op adapter. | b |
| S2 | `internal/strandedwork/strandedwork.go:1149` (`for-each-ref refs/heads/polecat-* refs/remotes/origin/polecat-*`), `:1107`, `:824` (`for-each-ref --contains … polecat-*`) | list every worker's work, and which branches carry an item's commits | The candidate set: all unfinished worker output. | **none.** Listing proposed (unlanded) Changes is enth's open question 4 (epic §8), and nobody owns it. Depends on W1. | c |
| S3 | `internal/strandedwork/strandedwork.go:1083` | `git log --format='%H %s' --stdin` (`^T` plus the polecat refs) | Match commits to a work item by the `(mg-xxxx)` in the subject. | `enth query` can filter by trailer (architecture.md §3), over the S2 candidate set. | b |
| S4 | `internal/strandedwork/strandedwork.go:1329` (`cherry -v T B`), `internal/gitgc/durability.go:288` (`cherry`), `internal/gitgc/worktreecommits.go:163` (`cherry -v`), `scripts/pogo-self-deploy:2493` | `git cherry` (patch-id) | Did this work land, even after a rebase? | Land records map submitted id to landed id. That is exact, where patch-id is a heuristic, so it is a better answer than git's. | b |
| S5 | `internal/strandedwork/strandedwork.go:1238-1285,1431` (`show-ref origin/B` vs `refs/heads/B`), `:1492` (`rev-list … --not --remotes`), `:867,872` (`for-each-ref --contains <sha> refs/remotes/origin/`); `internal/gitgc/durability.go:221,265`; `scripts/pogo-self-deploy:2373-2473,2717-2722` | "is it pushed / where is it durable" | Will removing this tree lose committed work? | Is the Change stored on enthd, or only in a local `ENTH_STORE`? Note that `enth change` already stores into the store, so deleting DIR never loses a committed Change. | b |
| S6 | `internal/strandedwork/strandedwork.go:1306` | `log -1 --format=%cI <ref>` | Age of the work. | The Change's committer time field. | a |
| S7 | `internal/strandedwork/strandedwork.go:1391` | `log --no-walk=unsorted --format=%H%n%b%x00 <sha>…` | Commit bodies, scanned for pre-registration, rescue and declaration markers. | The Change message and trailers, via `enth query`. | a |
| S8 | `internal/strandedwork/content.go:337,357` | `show --name-only`, `show --unified=0` | The paths and added lines of a stranded commit. | The Change's ops give the paths. Added lines need a diff of `query cat` at the parent and at the Change. | b |
| S9 | `internal/strandedwork/content.go:312` | `git show T:path` | Target file content, to check whether the lines already landed. | `enth query cat <ref> <path>`. | a |
| S10 | `cmd/pogo/checkstranded.go:523,551` | `rev-parse --git-common-dir`, `symbolic-ref --short HEAD` | Source repo and branch of a polecat tree. | `DIR/.enth/checkout` (see W9). The branch half follows W1. | b |

The open-PR probe (`ghpr.Lookup` via `strandedwork.GitHubOpenPR`, `strandedwork.go:906`) is §9, H2.

## 5. gitgc

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| C1 | `internal/gitgc/gitgc.go:148` (callers `sweep.go:196`, `agent/api.go:2857`) | `worktree list --porcelain` | Enumerate worktrees and their branches. | enth keeps no registry of checkouts. pogo already knows its trees (`$POGO_HOME/polecats/*`), and each has `.enth/checkout`. | b |
| C2 | `internal/gitgc/gitgc.go:69,225` | `branch --list polecat-*`, `branch -D` | Collect spent branches. | none needed: there are no branches. No-op adapter. | b |
| C3 | `internal/gitgc/gitgc.go:233-237` | `worktree prune` | Drop registrations whose dirs are gone. | none needed: there is no registry. No-op adapter. | b |
| C4 | `internal/gitgc/gitgc.go:210` (`sweep.go:396`) | `merge-base --is-ancestor B main` | A done ticket's branch is in main. | Land record or ancestry query (see S4). | b |
| C5 | `internal/gitgc/gitgc.go:964`, `detached.go:40` | `rev-parse --abbrev-ref HEAD`, `symbolic-ref -q HEAD` | Name a preserved tree's branch; a detached HEAD means commits would be orphaned. | A checkout cannot be detached. Changes are stored at `enth change` time, so they cannot be orphaned. No-op adapter. | b |
| C6 | `internal/gitgc/gitgc.go:1142` (`removeWorktreeFn`, `sweep.go:94,273`); `cmd/pogod/worktreecleanup.go:146`; `scripts/pogo-self-deploy:3297-3303` | `worktree remove --force` (+ `worktree prune`) | Remove the tree once the guard passes. | `rm -rf DIR`. | b |

## 6. Deploy and self-deploy

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| D1 | `scripts/launchd/pogo-deploy.sh:2291-2341` | `[ -d $SRC/.git ]`, `remote get-url`, `clone`, `fetch`, `status --porcelain`, `checkout main`, `merge --ff-only origin/main`, `rev-parse --short HEAD` | Keep a clean deploy checkout at the tip of main, and refuse if it is dirty. | `enth --remote checkout trunk ~/.pogo/deploy-src <all>`, re-run each night, plus `enth status` (empty). | b |
| D2 | `scripts/launchd/pogo-deploy.sh:4767-4903` | macguffin: `clone`, `fetch --tags --force`, `checkout --detach origin/main` | macguffin's `build.sh` versions itself from the nearest tag. | Tags are refs with no enth verb (R18), and there is no `describe`. Only relevant if macguffin moves too. | c |
| D3 | `scripts/launchd/pogo-deploy.sh:5079-5285` | `hash-object`, `show <sha>:path`, `log --find-object`, `cat-file blob`, `rev-parse -q --verify` | Runner self-refresh: compare the installed runner to source. | `enth query cat` at a revision. "Which commit introduced this content" becomes "who touched P since N" (architecture.md §3). | b |
| D4 | `build.sh:87-97,149,175-181`; `scripts/pogo-self-deploy:3344-3348`; consumers `internal/version/resolve.go:116-127`, `internal/driftwatch/revision.go:173-193`, `internal/revcheck/revcheck.go:224` | `rev-parse HEAD`, `--abbrev-ref HEAD`, `status --porcelain` into `-X version.Commit/Branch/Dirty`; Go's automatic `vcs.revision`/`vcs.modified` | Every binary says which revision it is. Drift-watch, revcheck and `/version` all depend on this. | **none for Go's stamp.** The Go toolchain records `vcs.*` only for the VCSs it knows. An enth checkout has no `.git`, so the stamp is absent and the revision-staleness detector stays unarmed (`driftwatch/revision.go:382-389`). This is inferred from the toolchain's behaviour, not measured on an enth checkout. The ldflags stamp could carry the Change id instead. | c |
| D5 | `scripts/pogo-self-deploy:756,769,779,784`; `internal/selfdrift/hostdeps.go:213,239,252`; `scripts/revision-probe.sh:593,632,999,1009,1036,1047`; `internal/driftwatch/revision.go:211-217` | `cat-file -e <rev>^{commit}`, `rev-parse main`, `rev-list --count rev..origin/main`, `log --since`, `ls-remote` | Is the running revision in this repo, and how far behind main is it? | Ancestry and log queries. Ids are Change ids, so a git-sha stamp needs the import/export sha↔change map. | b |
| D6 | `scripts/pogo-self-deploy:3422-3427` | `rev-parse HEAD` == `$DEPLOY_REF`, `status --porcelain` empty | Build only a clean checkout of exactly main. | `.enth/checkout` base == trunk tip, and `enth status` empty. | b |
| D7 | `scripts/launchd/pogo-deploy.sh:1220-1243,2209-2286` | resolve a working `git`; `GIT_SSH_COMMAND`, `GIT_HTTP_LOW_SPEED_*` | Bounded, non-interactive git transport. | Resolve the `enth` binary; enthd HTTP timeouts. | b |
| D8 | `internal/service/runnerlineage.go:94,136,148` | `remote get-url origin` (matched to drellem2/pogo), `cat-file blob <ref>:<path>` | Is the runner from pogo's own lineage? | The enthd repo name, and `query cat`. | b |
| D9 | `internal/ineffect/hostdeps.go:107-215` | `rev-parse`, `log -1`, `show --name-only`, `merge-base --is-ancestor`, `show rev:path`, `log -N -- path`, `rev-parse HEAD` | Is a fix commit in effect on this host? | `enth query` (log, ancestry, cat, "who touched P since N"). | b |

## 7. The release flow

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| L1 | `scripts/bump-version.sh:177,206,282,303-314` | `rev-parse --abbrev-ref HEAD` (on main), `diff-index`, `diff --stat`, `add`, `commit` | Make the version-bump commit. | `enth status`, then `enth change -m "chore: Bump version to X"`. | a |
| L2 | `scripts/bump-version.sh:327-342` | `tag -a vX`, `push origin main`, `push origin vX`, `ls-remote --tags origin` | Create and publish the release tag. | A release ref by CAS, exported as a tag. No verb in epic #9 (same decision as R18). | c |
| L3 | `scripts/check-version.sh:62,85` | `git tag -l 'v*'`, `git tag -l vX` | The newest release, and whether vX is already taken. | List the release refs. No verb. | c |
| L4 | `docs/release-process.md` steps 0–5 (`git push origin CAND:refs/heads/release/vX.Y.Z`, `submit --target=release/vX.Y.Z --post-merge-tag`, verify with `rev-parse vX^{commit}`) | a release branch at a soaked SHA, landed onto, tagged, then back-ported | Cut a release from a known-good commit, not from the moving main. | Needs a non-trunk ref created at a chosen Change and landed onto. Refused on followed repos (epic §4), and there is no verb to create one. | c |

## 8. The projects index and repo recognition

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| X1 | `internal/project/indexer.go:76`; `internal/project/project.go:389,430-437` (`hasGit`); `cmd/pogo/main.go:4429-4443` | a `.git` entry marks a repo | Discover and register repos (`projects.json`, `lsp`). | `.enth/` is reserved and written by every checkout (epic §2), so add it as a second marker. | b |
| X2 | `internal/staleness/reference.go:449`; `scripts/revision-probe.sh:588,922`; `cmd/pogod/main.go:2566-2585` | `.git` exists means "is a checkout" | Arm detectors only on a real checkout. | `.enth/checkout` exists. | b |
| X3 | `internal/gitceiling/gitceiling.go`, `resolve.go:206`; `Ensure()` at `cmd/pogo/main.go:159`, `cmd/pogod/main.go:1378` | `GIT_CEILING_DIRECTORIES=$POGO_HOME`, `rev-parse --show-toplevel` | Stop git from treating `$POGO_HOME` as an enclosing repo. | none needed: enth verbs take DIR explicitly. No-op adapter. | b |
| X4 | `internal/config/pogohomerepo.go:48` | `rev-parse --git-common-dir` | Is this repo `$POGO_HOME`? | A path comparison on `.enth/checkout`. | b |
| X5 | `internal/homevcs/audit.go:165-245`, `publication.go:219` | `rev-parse --show-toplevel --show-prefix`, `remote get-url`, `ls-files`, `status --porcelain -z` | Is `~/.pogo` versioned by git (the `pogo-config` repo), and is it dirty or public? | none. This audits a host-local git repo. **[keep git]** | c |

## 9. GitHub (gh and the GitHub API): git-only by design

| # | Where | Operation | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| H1 | `internal/refinery/closingref_gate.go:169-172` (`gh pr view --json number,body`); `merge.go:868` (`push --force-with-lease` PR push-back), `:877,:885` → `internal/ghpr/ghpr.go:36-75` (`gh pr view --json state,number`), `:949-952` (`gh pr close`); `internal/prtracking/gh.go:22` (`gh pr list`) | PR flow: `pr_mode`, closing keywords in PR bodies, closing PRs GitHub cannot detect as merged | Keep GitHub PRs coherent with what the refinery merged. | In Phase B a GitHub PR becomes a submission into enth (architecture.md §5). The PR concept itself stays on GitHub. **[keep git]** for repos still on GitHub. | c |
| H2 | `internal/strandedwork/strandedwork.go:902-907` (`PRProbe`, `GitHubOpenPR`); `agent/strandedgate.go:411,472`; `strandedsweep.go:240` | `gh pr view <branch>` | "Is this work awaiting review, rather than stranded?" | Nearest equivalent: list proposed Changes (epic §8 Q4, unowned). **[keep git]** while review lives on GitHub. | c |
| H3 | `internal/ghintake/source.go:503` (`gh issue list`), `internal/ghteardown/issue.go:57` (`gh issue view`), `internal/carrierdrift/source.go:490`, `internal/ghwatch/production.go:22` | gh issue triage and carriers | Track GitHub issues as work. | Not VCS. **[keep git]** | c |
| H4 | `internal/homevcs/visibility.go:62,69` | `gh repo view --json visibility` | Is the pogo-config remote public? | Not VCS. **[keep git]** | c |
| H5 | `internal/ghtoken/ghtoken.go:422,433` (`gh auth token`), `verify.go:139` (`GET api.github.com/user`) | harvest and verify the GitHub credential | A working `GH_TOKEN` for every gh call. | Not VCS. An enthd token would sit beside it (R24). **[keep git]** | c |
| H6 | `.github/workflows/release.yml` (goreleaser on `v*` tag push); `install.sh:95,118` (GitHub Releases API and downloads); changelog compare links (`scripts/roll-changelog.sh:89-99`, `changelog-links.sh:98,122,163`) | GitHub Releases and Actions | Ship binaries. | The tag reaches the mirror through export, so tag-triggered workflows keep working (milestones.md l.179). **[keep git]** | c |

## 10. Other Go code

| # | Where | git op | What pogo needs | enth equivalent | Class |
|---|---|---|---|---|---|
| O1 | `internal/freshen/freshen.go:191-362,440` (called from `internal/agent/workspace.go:56`) | `rev-parse`, `config branch.X.*`, `remote`, `ls-remote --exit-code`, `fetch`, `status`, `merge --ff-only FETCH_HEAD`, `rev-list --count` | Fast-forward crew workspaces at crew start, and refuse if dirty or diverged. | `enth status`, then check out again. `checkout --sync` does not exist yet (epic §8 Q3). | b |
| O2 | `internal/staleness/prompts.go:140-235,595`, `reference.go:95-402` | `rev-parse ref^{commit}`, `log -1 %cI`, `ls-tree -r`, `show c:name`, `rev-parse --git-path FETCH_HEAD`, `config branch.*`, `ls-remote`, `fetch`, `log from..to -- subtree`, `rev-list --count` | Is the installed prompt corpus behind source? | `enth query ls`, `cat` and `log` at a ref. Remote reads are live, so FETCH_HEAD age has no meaning. | b |
| O3 | `internal/midsessionwedge/source.go:205` | parse the `.git` `gitdir:` file, then stat the git metadata | Use git activity in a tree as a liveness signal. | The mtime of `.enth/checkout` (it is rewritten by `enth change`). | b |
| O4 | `cmd/pogo/main.go:3919` (`pogo doctor`) | `git` on PATH | Prerequisite check. | `enth` on PATH. | a |
| O5 | `hooks/commit-msg:20`, `git config core.hooksPath hooks` (CLAUDE.md) | commit-msg hook | Early warning on closing keywords. | enth has no hooks. The refinery already runs the same check (R12), so the hook simply has no counterpart. | b |
| P6 | `internal/agent/prompts/**` (counted below) | git and gh commands in agent instructions | Agents follow a git protocol. | Per-VCS template variants, or a `{{.VCS}}` block in each template. | b |

## Prompts and templates

**Measured** with `grep -cE` over subcommand-anchored patterns: `\bgit (add|commit|push|fetch|log|diff|show|rev-parse|rev-list|checkout|merge-base|status|ls-remote|tag|cherry|branch|worktree|stash|pull|reset|clone|merge|config|remote|for-each-ref|cat-file|-C)` and `\bgh (pr|issue|run|repo|api|release|auth)`. A search agent counted independently with a slightly different pattern and got the same git totals and 44 gh lines (it found one fewer in `polecat.md`).

| File | lines with a git command | lines with a gh command |
|---|---|---|
| `internal/agent/prompts/templates/polecat.md` | 18 | 5 |
| `internal/agent/prompts/templates/polecat-build-pr.md` | 14 | 5 |
| `internal/agent/prompts/templates/polecat-review.md` | 15 | 5 |
| `internal/agent/prompts/templates/polecat-qa.md` | 13 | 0 |
| `internal/agent/prompts/templates/polecat-architect.md` | 14 | 5 |
| `internal/agent/prompts/templates/polecat-triage.md` | 8 | 4 |
| `internal/agent/prompts/mayor.md` | 12 | 7 |
| `internal/agent/prompts/pm/pm-template.md` | 19 | 14 |
| `internal/agent/prompts/crew/doctor.md` | 3 | 0 |
| **Total (9 embedded files)** | **116** | **45** |

Outside the embedded set, `docs/examples/research-triage/prompts/mayor.md` has 1 git line. The installed copies under `~/.pogo/agents/` are rendered from these files, so they carry the same counts plus any local edits, which were not counted.

The load-bearing instructions:
- **The polecat protocol**: `polecat.md` steps 3–5 (read the branch with `rev-parse --abbrev-ref`, `add`, `commit`, `push`, and `presubmit_check`), duplicated in `polecat-architect.md`. These are entries P2–P5.
- **The review and QA checkout**: `polecat-review.md:106-117` and `polecat-qa.md:100-101` (`git fetch`, `git checkout -B … --no-track origin/<src>`). pogo-self-deploy's drain durability test is coupled to this.
- **PR flow**: `gh pr create/comment` in `polecat-build-pr.md`, `polecat.md` and `polecat-architect.md`.
- **pm and mayor**: GitHub issues, CI and release checks (`gh issue/pr/run/release`, `gh api`). These are GitHub, not git.

A few Go string literals also tell humans to run git. They are remedy text, not executed: `freshen.go`, `cmd/pogod/worktreecleanup.go`, `cmd/pogod/main.go`, `cmd/pogo/gclive.go`, `cmd/pogo/checkorphans.go`, the `strandwatch.go` remedies, and the alert text in `pogo-deploy.sh` (2598–2703, 5825–5836).

## Counts

84 entries: W1–W9, P1–P6, R1–R24, S1–S10, C1–C6, D1–D9, L1–L4, X1–X5, H1–H6 and O1–O5. P6 sits in §10 for layout but belongs to the P series.

Tallied from the class column of the tables above, not by hand: `grep -E '^\| [A-Z][0-9]+ \|'` over this file, grouped by the last column.

| Class | Count | Entries |
|---|---|---|
| (a) maps 1:1 | **13** | P1, P3, R2, R8, R12, R16, R17, R23, S6, S7, S9, L1, O4 |
| (b) needs an adapter | **51** | 9 of these are no-op adapters (a git step enth makes unnecessary): R6, R7, R20, R21, S1, C2, C3, C5, X3. The other 42 need real translation. |
| (c) no analogue | **20** | 7 are **[keep git]** (X5, H1–H6). 13 need a design decision: W1, W8, P5, R4, R13, R14, R18, S2, D2, D4, L2, L3, L4. |
| **Total** | **84** | |

The 13 decisions fall into five clusters, and solving each cluster solves its entries:
1. **What names a worker's work** (W1, P5, S2): a trailer, a non-trunk ref, or a listing of proposed Changes (enth §8 Q4).
2. **Gating the exact landed tree** (R13, R14): needs a materialised dry run or a tree id (enth §8 Q1). This is the largest and touches enth's own roadmap.
3. **Tags, release refs and release branches** (R4, R18, L2, L3, L4, D2): need a ref-creation verb, and need the repo to be enth-written (the handover, enth #11).
4. **Ignoring pogod's own files** (W8): enth needs an ignore rule, or pogod must stop writing into worker trees.
5. **The revision stamp** (D4): Go's `vcs.revision` is absent in an enth checkout, so pogo has to stamp the Change id itself.

## Answers

### (1) Is there a seam where a VCS backend could be selected per repo?

**No VCS seam exists.** The method was a search for `interface` types and injected runners in every package that calls git, plus three independent sweeps. These are confirmed per area: refinery (`internal/refinery` has no `Git` interface; tests run real git against a bare origin), worktree/stranded/gitgc, and the rest. git is called through `os/exec` in about ten per-package helpers:
- `internal/refinery/merge.go:1507` `gitCmdOutput`
- `internal/gitgc/exec.go:62` `runSplit` and `gitgc.go:57` `git`
- `internal/strandedwork/strandedwork.go:1443` `git`
- `internal/staleness/prompts.go:140,175`
- `internal/freshen/freshen.go:440`
- `internal/ineffect/hostdeps.go:333-360`
- `internal/service/runnerlineage.go:94`
- `internal/homevcs/audit.go:280`
- `internal/gitceiling/resolve.go:205`

`internal/agent/api.go` calls git inline.

**There are partial seams at the right altitude**, meaning interfaces over the *question* pogo asks, not over git commands:
- `agent.StrandedWorkGate` (`internal/agent/strandedgate.go:38`, production `GitStrandedWorkGate` at `:47`)
- `agent.PreservedWorktreeGate` (`internal/agent/preservedgate.go:51`)
- `strandedwork.PRProbe` (`strandedwork.go:902`)
- `stallwatch.Stranded` / `Preserved` (`internal/stallwatch/stranded.go:214`, `preserved.go:168`)
- `gitgc.removeWorktreeFn` (`sweep.go:94`)
- `strandwatch.Options` func fields (`strandwatch.go:1322-1351`)

The `Git…` names show these were written with one backend in mind, but they are already shaped like backend interfaces.

**Where a per-repo selection could live:**
- `.pogo/refinery.toml` is already read per repo: `Refinery.loadConfig`, `internal/refinery/merge.go:1291`, struct `refineryConfig` at `:1316`.
- The project record in `projects.json`.

Both are candidates for a `vcs = "enth"` key. The catch with `refinery.toml` is that it is read *from a checkout*, so the choice of VCS would have to be known before the first checkout exists. The project record does not have that problem.

The table suggests the backend boundary should sit at the semantic level rather than per git command: Workspace (create, remove, dirty, source repo), Submission (ids, durable?, landed?), Land (gate tree, land, merged id), Release (tag). Many entries in class (b) collapse to no-ops on the enth side, so a command-level shim would carry a lot of dead surface. That is for the design ticket to decide.

### (2) What should stay git-only by design?

- **GitHub issue triage and carriers** (H3): `ghintake`, `ghteardown`, `carrierdrift`, `ghwatch`. These track issues, which have nothing to do with the VCS.
- **GitHub credentials** (H5): `ghtoken`. An enthd token sits beside it, not in its place.
- **GitHub Releases, Actions and `install.sh`** (H6). enth's export mirror carries tags to GitHub, so tag-triggered workflows keep working (milestones.md l.179).
- **PR flow and the open-PR probe** (H1, H2): for any repo GitHub still writes, which today is all of them (epic §4). After a handover, PRs on the mirror become enth submissions (architecture.md §5). Until enth has a proposed-Changes listing, "awaiting review" can only be asked of GitHub.
- **`homevcs`** (X5, H4): it audits whether `~/.pogo` is a git repo (`drellem2/pogo-config`). That is a fact about this host, not about any project's VCS.
- **`gitceiling`** (X3): stays as it is. It is harmless with enth and still needed for any git repo.

Also worth stating: pogo's own repo cannot leave git while it is followed from GitHub. Epic #9 §4 refuses every ref move until the handover, so an enth option in pogo first serves enth-native repos (epic item F, mg-54f05) and repos after their handover.
