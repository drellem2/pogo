# enth as a per-repo VCS backend for pogo

Work item: mg-f09d3. Parent: mg-2151d. Date: 2026-11-02. Status: design. No code.

Inputs:
- The inventory, [enth-vcs-inventory.md](enth-vcs-inventory.md) (mg-49f3b). Entry ids (W1, R13, …) and the five decision clusters are that document's.
- enth's write-path epic, `docs/epics/9-write-path.md` in drellem2/enth, read at `origin/main` `515724f` (2026-11-02).
- pm-enth's additions to the #9 items, recorded in the item bodies on 2026-11-02 19:20Z: `land --expect-new` (mg-b1b9e), `enth changes --unlanded --trailer` (mg-10079), `enth tag` / `enth ref create` (mg-40d5e), ignore files (mg-2ef01).
- pm-pogo's eleven product decisions (the mg-f09d3 body). This document does not reopen them. Where a decision has a consequence it did not state, the consequence is written down here and marked.

Labels, as in enth's docs: **measured** (with the command), **inferred**, **proposed**. Anything not labelled is proposed.

**Gate.** Every build slice at the end waits for enth #9 to exit (mg-927f8, the #9 exit report). This document does not wait. The enth API it designs against is #9 as designed plus the four additions, and none of them is built yet: the installed `enth` here is `0.0.0` (M1), with no `change`, `land`, `status` or `.enth/` state (measured: `enth --help`; `enth checkout` writes no `.enth/` directory).

## 1. Scope

**In scope:** everything pogo does to a **managed repo**, meaning a repo whose work pogo dispatches, gates and lands. That is spawn and workspaces (§1 of the inventory: W1–W9), the polecat protocol (P1–P6), the refinery (R1–R24), stranded work (S1–S10), gitgc (C1–C6), repo recognition (X1, X2, X4), crew workspace freshening (O1), the wedge signal (O3), `pogo doctor` (O4), the commit-msg hook (O5), the release-tag hook (R18) and its release-flow neighbours (R4, L4), the revision stamp (D4), and the prompts (P6).

**Out of scope, and why:**
- **pogo's own source and deploy** (D1–D3, D5–D9, L1–L3, O2): these read *pogo's* repo, which stays git while GitHub writes it. enth #9 §4 refuses every ref move on a followed repo, so pogo's repo cannot take an enth landing before its handover (#11). They move when pogo moves, which is the open question in §14.2. D4 is the exception and is in scope (§10), because a read-only `enth checkout` of pogo is possible today.
- **macguffin's tag-versioned build** (D2): relevant only if macguffin moves.
- **`homevcs`** (X5, H4) and **`gitceiling`** (X3): facts about this host's `~/.pogo` git repo, not about any managed repo. They stay git and stay outside the seam.
- **GitHub** (H1–H6): unchanged by decision 10. §11 says what an enth repo does instead of a PR.
- **Configuring this box for drellem2/enth**: a separate box-op after the build (mg-2151d step 4).

## 2. Selection: a `vcs` key on the project record (decision 1)

**The key lives on the project record in `projects.json`**, not in `.pogo/refinery.toml`.

```json
{"id": 7, "path": "/Users/daniel/dev/enth", "vcs": "enth",
 "enth": {"remote": "https://enth.app", "repo": "drellem2/enth", "trunk": "refs/heads/main"}}
```

Why the project record:
1. **It is known before any checkout exists.** `refinery.toml` is read from a checkout (`Refinery.loadConfig`, `internal/refinery/merge.go:1291`, which reads the worktree's copy and the source repo's copy). To spawn the first polecat pogo must already know which VCS makes the workspace (W2). The inventory flagged this (Answers §1).
2. **A branch cannot change it.** `loadConfig` lets the worktree's values win per field. A key there would let a polecat's own edit to `refinery.toml` switch the VCS its own work is landed with. The project record is written only by pogod and by `pogo project`, never by a worker.
3. **An enth repo needs more than one word.** It has no local clone to point `path` at in the git sense: pogo needs the enthd endpoint, the repo name and the trunk ref (R3, R22). `refinery.toml` has nowhere natural for an endpoint, and the endpoint is per-host configuration, not something to commit into the repo.

`path` keeps its meaning: a local directory registered with pogo. For an enth repo it is a read checkout that pogo keeps (R19, O1). It is the user's, as a git repo's path is today.

**Byte-identical git.** Both fields are `omitempty` and absent means `git`. A `projects.json` with no `vcs` key is read and re-written byte for byte as today. The test is a golden round trip of the current file. `refinery.toml` does not read a `vcs` key. A `vcs` key found there is reported as a config warning (once per load), so a user who puts it in the obvious place learns where it goes rather than having it silently ignored.

**Setting it:** `pogo project add <path> --vcs=enth --enth-remote=URL --enth-repo=NAME`, and `pogo project set-vcs` for an existing record. Changing the VCS of a repo with open MRs or live polecats is refused. Moving a repo is a handover, not a toggle (§14.2).

## 3. The seam: `internal/vcs` (decision 2)

The inventory found no seam. git runs through `os/exec` in about ten helpers, and `internal/agent/api.go` calls it inline (Answers §1). It also found that the useful boundary is the **question** pogo asks, not the git command: many class-(b) entries are no-ops on the enth side, so a command-level shim would carry dead surface.

```go
package vcs

type Kind string // "git" | "enth"

// Backend is selected per repo from the project record. git is the zero value.
type Backend interface {
    Kind() Kind
    Workspaces
    Work
    Lander
    Refs
}

// Workspaces: W2–W9, P1, C1, C3, C5, C6, S10, X2, X4, O1, O3.
type Workspaces interface {
    Create(ctx context.Context, s WorkspaceSpec) (Workspace, error) // W2, W3, W4, W7
    Remove(ctx context.Context, dir string) error                   // W6, C6 (+C3)
    Dirty(ctx context.Context, dir string) ([]string, error)        // P1, R15 (pogod's own files excluded)
    Describe(ctx context.Context, dir string) (Workspace, error)    // W9, S10, X4, C5: source repo, base, owner
    Refresh(ctx context.Context, dir string) error                  // R19, O1
}

// Work finds and judges a worker's output: W1, W5, P2, P5, R1, R9, R12, S2–S9, C4.
type Work interface {
    Find(ctx context.Context, repo Repo, q WorkQuery) ([]Submission, error) // by item and/or worker
    State(ctx context.Context, repo Repo, s Submission, target string) (WorkState, error) // landed? durable? age
    Messages(ctx context.Context, repo Repo, s Submission) ([]Message, error)             // R12, S7
    Diff(ctx context.Context, repo Repo, s Submission) (Diff, error)                      // S8, R15 paths
    Cat(ctx context.Context, repo Repo, rev, path string) ([]byte, error)                 // S9
}

// Lander is the refinery pipeline: R2, R5–R11, R13, R14, R16, R17, R20–R23.
type Lander interface {
    Prepare(ctx context.Context, mr MR) (Prepared, error) // tip at start, the result to gate, a gate dir
    Land(ctx context.Context, p Prepared) (Landed, error) // the one ref move
    Cleanup(ctx context.Context, mr MR) error             // R6, R20, R21: git work, enth no-op
}

// Refs: R3, R4, R18, L4.
type Refs interface {
    Default(ctx context.Context, repo Repo) (string, error)
    Create(ctx context.Context, repo Repo, ref, at string) error // --auto-create-target, release branches
    Tag(ctx context.Context, repo Repo, name, at, msg string) error
}
```

Errors are typed so that callers branch on meaning, not on text: `ErrStale` (re-run from `Prepare`), `*ConflictError` (paths, kinds, the Changes against), `*RefusedError{Code}` (enth's stable codes, or the git backend's equivalents), and `ErrUnsupported` (for example `Refs.Tag` on a followed enth repo).

**Signatures are sketches.** The build slices fix them. What is fixed here: the four groups, the error kinds, and that the git backend is written first, against today's behaviour.

**What the seam absorbs:** the per-package helpers in `internal/refinery` (`gitCmdOutput`, `merge.go:1507`), `internal/gitgc` (`exec.go:62`, `gitgc.go:57`), `internal/strandedwork` (`strandedwork.go:1443`), `internal/freshen` (`freshen.go:440`), `internal/midsessionwedge` (O3), and the inline git in `internal/agent/api.go`. The existing question-shaped interfaces (`agent.StrandedWorkGate`, `agent.PreservedWorktreeGate`, `strandedwork.PRProbe`, `stallwatch.Stranded`/`Preserved`, `gitgc.removeWorktreeFn`, the `strandwatch.Options` func fields) stay. Their production implementations call the backend instead of git.

**What stays outside the seam:** `internal/homevcs` (X5), `internal/gitceiling` (X3), `internal/staleness` (O2), `internal/ineffect` (D9), `internal/service/runnerlineage.go` (D8), `internal/ghtoken` (R24 stays for git, H5), and the deploy scripts. Section 1 says why. Each of them acts on pogo's own repo or on this host.

**No-op adapters.** The inventory lists nine (R6, R7, R20, R21, S1, C2, C3, C5, X3). On the enth backend R6 and R20/R21 are `Cleanup` returning nil, R7 and S1 do not exist because reads are live, C2/C3/C5 have nothing to collect, and X3 is outside the seam. None of them is a method of its own.

## 4. Workspaces: one `enth checkout` per polecat (decision 3)

**Create (W2, W3, W4).** `enth --remote URL --repo NAME checkout <target ref> DIR` with **no path arguments**, so the selection is the whole repo. An `outside_selection` refusal (epic §2) arrives at `enth change` time, after the work is done, and a polecat cannot know in advance which paths it will touch. A whole-repo checkout costs megabytes. pm-enth gives ~1 MB for pogo (mg-2151d, 18:56Z section; their figure, not re-measured here). The ref is resolved on enthd at call time, so W4's fetch and its four-step fallback chain become one call.

**The escape hatch** for a repo too large to check out whole is `enth checkout --add PATH DIR` (epic §2), which widens the selection later. pogo exposes it as an optional `workspace_paths` list on the project record's `enth` block. When set, the checkout is narrowed to those paths, and the polecat prompt names `enth checkout --add` with the `outside_selection` remedy. It is unset by default and no repo needs it today.

**What the checkout must record (W9).** `DIR/.enth/checkout` holds the base, the selection and an index (epic §2). It does not say whether it records the store or the remote. pogo needs that to find the repo from a workspace (W9, S10, X4). **Requirement E6 (§13):** the state file records the remote URL and repo name. Until it does, pogod keeps the same facts in its own spawn record, keyed by DIR, as it already keeps the worker's name.

**Environment.** pogod gives the worker `ENTH_REMOTE`, the repo name and a write token (R24: an enthd token with `Access::Write`, epic §5). That way every `enth` verb the worker runs goes to enthd and every Change is durable on the server at creation (P4). The token is the user's, as `GH_TOKEN` is today. Per-worker tokens come with enth's folder-scoped tokens (#34) and are not designed here.

**Leftovers, rollback, removal (W5, W6, C1, C3, C6).** No branch can collide, so W5's check becomes: "does the old DIR hold edits that `enth status` reports (uncommitted), or Changes of this item that have not landed?" (S4, via the land records). Rollback (W6) and removal (C6) are `rm -rf DIR`. There is no registry to prune (C3). pogo already knows its trees (`$POGO_HOME/polecats/*`), so C1 enumerates those directories and reads `.enth/checkout` in each.

**Durability (S5, C5).** `enth change` stores the Change on enthd before it returns, so deleting DIR can never lose a *committed* Change. Only uncommitted edits are at risk. gitgc's preserve guard for an enth workspace reduces to "`Dirty(DIR)` is empty". A checkout cannot be detached, so C5's orphan check has nothing to guard.

**Adopting stranded work (W7).** `--stranded-adopt` checks out the stranded worker's top unlanded Change (`enth checkout <id> DIR`) and reads the base back from `.enth/checkout` to prove it. That is the analogue of `merge-base --is-ancestor`.

**Freshening (R19, O1).** `enth checkout --sync` does not exist (epic §8 Q3). `Refresh` checks out again into a temporary sibling and swaps it in, after refusing if `Dirty` is non-empty, as `freshen` refuses today.

**Repo recognition (X1, X2).** `.enth/` becomes a second repo marker beside `.git` in the indexer (`internal/project/indexer.go:76`, `project.go:430-437`) and in the "is a checkout" tests (X2). A directory with both is refused as ambiguous rather than guessed at.

**Liveness (O3).** The wedge detector's "git activity" signal reads the mtime of `.enth/checkout`, which `enth change` rewrites.

**As built (slice 5, mg-cc5cd, 2026-11-03).** Against enth `origin/main` `6488096` (#9 B–G), measured by `scripts/enth_workspaces_test.sh` (a sandboxed pogod and enthd: spawn, edit, dirty, stop, with a git control on the same daemon) and `internal/vcs/enthvcs`'s enthd tests. What differs from the text above:
- **E6 had already shipped.** #9's `.enth/checkout` header records `"origin":{"remote":URL,"repo":NAME}`. It does not record the pogo project or the ref, so pogod still writes a record, `DIR/.enth/pogo-workspace.json` (project path, remote, repo, ref, base at checkout, selection). It lives inside `.enth/` because enth skips `.enth` at every depth, and so it dies with `rm -rf DIR`. `Describe` reads both files. A checkout with no record is an `UnnamedRepoError` that names its origin.
- **Selection by tree, not only by repo.** The spawn picks the backend from the project record (`agent.SetRepoVCS(project.VCSOf)`), because the checkout does not exist yet. Everything after that picks it from the tree's nearest marker (`enthvcs.Detect`): the persona exclude, the failed-spawn rollback, the crew refresh, `pogo worktree-dirty`, and gitgc's dirty check and removal. The nearest marker wins, so a checkout nested in `~/.pogo` (a git repo) is enth. A level that holds both markers is refused. A tree with no marker at all is git and is handled exactly as before.
- **`Dirty` answers in git porcelain shape.** A put of an index path is ` M`, a put of any other path is `??`, a delete is ` D`, and `outside_selection` paths are `??` (uncommitted work, not an error). That way gitgc's counts and `pogo worktree-dirty`'s output read it unchanged. The daemon-file excludes are `/.claude/settings.local.json` and `**/.claude/pogo-mailhook.stamp`, the same two files `gitvcs.IsPogodArtefact` carves out.
- **Removal goes through gitgc's guard.** `pogo agent stop` keeps a dirty enth tree, refuses one it cannot read, and removes a clean one. The git durability arm (`checkOrphanCommits`) is skipped for enth (§4, durability). Phase 1b of the sweep used to delete a `.git`-less polecat directory with no dirty check. It now gives an enth checkout the same guard. Without that, a dirty enth tree whose item was done would have been deleted.
- **`Refresh` does not swap the directory.** It checks out again into a temporary sibling and renames the new base's files into place, so files enth does not track (ignored files, pogod's own) survive. It declines when the tree is dirty, when there is no record, and when the base has moved off the recorded one (a Change was made there). `Behind` stays -1, because enth does not count entries between bases.
- **`workspace_paths`** is `EnthRepo.WorkspacePaths` (`pogo project set-vcs --enth-workspace-path`). When set, the polecat's prompt gets a prelude that names the selection and `enth checkout --add`.
- **The token.** pogod passes `ENTH_TOKEN` through only when its own environment holds one. Otherwise enth uses the token `enth login` saved, which the worker reads from the same HOME. The worker also gets `ENTH_REMOTE`, `ENTH_REPO` and `ENTH_TRUNK`.
- **Not in this slice:** `--stranded-adopt` on enth is refused at spawn (W7, slice 6). gitgc's preserved-tree and stranded scans still count an enth checkout as "not a worktree" (reporting only, nothing is removed). A worker's `enth change` needs an identity (`ENTH_AUTHOR_*`/`GIT_AUTHOR_*` or `user.*` in enth's config), and pogod does not supply one. The prompts are still git's (slice 11).

## 5. Identity: trailers name a worker's work (decision 4)

Every Change a worker produces carries two trailers:

```
Pogo-Item: mg-X
Pogo-Worker: <agent name>
```

They are written with `enth change DIR -m "…" --trailer Pogo-Item=mg-X --trailer Pogo-Worker=<name>` (epic §2). This replaces the `polecat-<name>` branch (W1) as the durable tie between a worker and its output.

**Finding the work (S2, S3, P5).** `enth changes --unlanded --trailer Pogo-Item=mg-X [--trailer Pogo-Worker=<name>]` (mg-10079, pogo's ask #2) lists the item's unlanded Changes as JSON lines: id, author, trailers, base and created time. That one query answers stranded-work's candidate set (S2), its match of work to item (S3, which today parses `(mg-xxxx)` out of commit subjects), review's "which Change do I check out", and submit's "what does this MR carry".

**What a submission is.** A `Submission` is an ordered stack: the listed Changes sorted so that each one's base is the previous one, ending at a top Change. The listing is refused as ambiguous when the Changes form more than one stack. That happens when two workers on the same item both produced work, for example a respawn that did not adopt. The refusal names each stack's worker, so the mayor chooses rather than pogo guessing. Filtering by `Pogo-Worker` too is the normal case and makes the ambiguity rare.

**Submit (P5, R1).** *(Superseded, mg-5b904: an enth repo has no merge queue, and the refinery refuses an enth submit. The worker lands its own stack and records it with `pogo land --item`, §6. The listing and the stack rules below are unchanged: `pogo presubmit --item --print-stack` applies them to what the worker lands.)* `pogo refinery submit` takes the work item as the identity of the work on an enth repo:

```bash
pogo refinery submit --item=mg-X --repo=… --author=… --target=main --verdict-file=…
```

The positional `<branch>` stays required for git and is refused for enth. At submit the refinery lists the item's Changes, refuses an empty list with the same `class=defect` "carries nothing ahead of the target" failure as mg-c184d, refuses an ambiguous one, and records the submitted ids on the MR. R1's "does the refinery reach it" is the listing itself: it runs against enthd, so a listed Change is reachable. On git `--item` is accepted as well, and resolves to the worker's branch through the existing spawn record. That way the polecat template's submit line can be the same for both VCSs.

**Trailers the worker forgot.** A Change with no `Pogo-Item` trailer is invisible to every query above. That is the failure this identity scheme has to guard against, because a branch name could not be forgotten. Two guards:
1. **A second, trailer-independent signal.** pogod knows each worker's DIR, and `.enth/checkout` names its base. Stranded-work also walks the live and preserved workspaces and reads the base Change of each. A base that is an unlanded Change without the trailers is reported as stranded work with the remedy "this Change has no Pogo-Item trailer". So it is reported as a defect, not missed.
2. **The prompt renders the full command.** The template writes the `--trailer` flags out with the values filled in (§12), so a worker copies a complete command rather than composing one.

Trailers are free-form text (epic §2), so a worker can write another worker's item. That is no weaker than today: any worker can push any branch name.

**Stranded work (S4–S9).** Has it landed (S4)? The land record pairs each submitted id with its landed id, which is exact where `git cherry`'s patch-id is a heuristic. Is it durable (S5)? Always, on enthd (§4). Age (S6) is the Change's committer time. Bodies and markers (S7) are the message and trailers from the listing. Paths and added lines (S8) come from the Change's ops plus a diff of `query cat` at the parent and at the Change. Target content (S9) is `enth query cat <ref> <path>`.

**gitgc (C2, C4).** There are no branches to collect (C2). "The item's work is in main" (C4) becomes "every Change listed for the item by `Pogo-Item` has a land record". That needs the listing's complement: the item's *landed* Changes. **Requirement E3 (§13):** `enth changes --landed --trailer …`, or a `landed` field on each listed Change, so that a done item's work can be confirmed as landed and not just as absent from the unlanded list. "Absent from the unlanded list" also matches a Change that was never made, so it must not be read as "landed".

**As built (slice 6, mg-30da0, 2026-11-03).** Against enth `origin/main` `34ae8fb` (#9 B–G), measured by `internal/vcs/enthvcs`'s enthd tests and `scripts/enth_workspaces_test.sh` (step 6b: 50 of 50 observations, with the git control). What differs from the text above, or was settled by building it:
- **E3 shipped as the `landed` field.** `enth changes` without `--unlanded` lists landed Changes too, each with the id a land record says it landed as, else null. `State` reads that field. A Change the listing does not know at all is an error, never "landed".
- **The listing is `enthvcs.Backend.ListChanges`**, and `vcs.Stacks` orders it. A Change whose base is not listed starts a stack. A fork (two listed children of one Change) is two stacks sharing a prefix. A gap (a middle Change missing from the listing) is also two stacks. `FindStack` refuses none (`vcs.ErrNoWork`), several (`*vcs.AmbiguousWorkError`, naming each stack's worker, size and top), and an empty item (an empty filter would list every worker's work).
- **The trailer-less signal is `UntrailedBase`**: the checkout's base, looked up in the unlanded listing, has no `Pogo-Item` trailer. It is a refusal in `pogo presubmit`. In `pogo check-stranded` it is reported as `STRANDED WORK:` with the remedy, through the enumerator's unreadable-worktree list, because an enth checkout has no branch to measure. The sweep reads each enth checkout through enth, where git would have walked up to `~/.pogo` and described that.
- **S2's candidate set on enth (slice 6b, mg-28cbc)** is `enth changes --unlanded` with no trailer filter, one listing per enth-backed project record. Each Change's `Pogo-Item` trailer is joined to the item's status. An open item with no live worker (matched by `Pogo-Worker` or by the item) is `stranded`, and so is a **done** item, because on enth no branch outlives the item. A queued enth MR is matched by its `item`. Archived and shelved items are excluded and counted. An item `mg show` cannot read is `unjudged`. A failed listing is a `repo_unreadable` row per open item in that repo. The remedy is `pogo refinery submit --item`, or the listing itself when the Changes form more than one stack. A landed Change is absent from `--unlanded` by its land record, so it is not reported. Not built: an open item whose Changes have *all* landed is not yet reported as git's `landed_not_closed`, and enth rows are not checked against the refinery's history of refusals.
- **S7–S9:** `Messages` is the message without its subject line (git's `%b`), trailers included. `Diff` compares `enth query ls -r` at the first parent and at the Change. `Cat` is `enth query cat`, and a missing path is the refusal `not_found`. `Fetch` is a no-op. S6 is the listing's `created`.
- **`pogo presubmit`** runs the template's `presubmit_check` line for line on git. `cmd/pogo/presubmit_test.go` runs the template's own shell function beside the verb on nine trees and requires the same stdout and the same pass or fail; a deliberate one-word change to a refusal fails four of those cases. On enth it requires `--item`, a clean checkout, a base that is not trailer-less, one stack, and that stack's top equal to the base. The templates kept their shell until mg-71014, when both arms became the verb; the test now runs the shell from `cmd/pogo/testdata/presubmit_check.sh`, where it is kept verbatim.
- **`pogo refinery submit --item`:** on enth the branch is refused, the target is checked on enthd (`RefTip`), and the stack is filtered by the item's live polecat as `Pogo-Worker` when there is exactly one. The ids are recorded as `enth_submitted`, kept apart from §7's `enth` land record, which is slice 8's. An empty listing fails as `class=defect … carries nothing ahead of <target>`. On git `--item` resolves an omitted branch to the branch checked out in the item's live polecat's worktree, and a given branch must agree with it. A git request without `--item` is byte-identical. Until slice 7 the merge worker failed an enth MR before any git step; slice 7 (§6.5.7) lands it. **Superseded by mg-5b904 (§6.4):** the refinery now refuses every enth submit, and the worker lands its own work.
- **The worker's identity.** enth reads no git config, and this host has no enth config and no `ENTH_`/`GIT_` identity, so every polecat's `enth change` would have been refused `no_identity`. pogod now fills only the fields enth's own chain leaves empty (`enthvcs.IdentityEnv`). It uses git's `user.name`/`user.email` for the source repo, which is the identity a git polecat's commits carry, and falls back to `<worker>@pogo.invalid`.
- **`--stranded-adopt` (W7)** resolves the item's one unlanded stack and checks out its top. It verifies that `.enth/checkout`'s base reads back as that Change, and rolls the checkout back if it does not. It refuses no stack, an ambiguous one, and a missing `--id`. The git stranded gate is not consulted for an enth repo.
- **gitgc's preserved scan** lists an enth checkout (repo from pogod's record, no branch, no commits at risk) instead of counting it "not a worktree". It skips the git-only probes, which in an enth tree would read `~/.pogo`.

## 6. Landing: the worker lands its own Change (decision 5, revised)

### 6.1 The decision

**DECISION (Daniel, 2026-11-03, mg-5b904).** 09:30Z: "pogo in enth mode won't need the refinery ... each polecat can just submit its own change". 09:37Z: skip refinery changes; CI on the GitHub mirror after the land is acceptable in the interim. 09:39Z: "I'd rather remove the enth ~~recovery~~ refinery path to have more confidence the de facto workflow is what we expect" (corrected at 09:39:31Z to "Refinery\*", so no reading of "recovery path" remains open).

So an enth repo has **no merge queue**. The worker lands its own work, and the refinery's enth land path (slice 7, §6.5.7) is **deleted rather than left dormant**, so the workflow in the templates is the only one. Git repos are unchanged: they still merge through the refinery, and its git pipeline is byte-for-byte what it was.

**ACCEPTED TRADE-OFF (Daniel's call; recorded so nobody "fixes" it).** The merged result `N` is **not tested before it lands**. A worker tests its own base (`./build.sh` before `enth change`), and `enth land` hunk-merges onto whatever the tip is by then. Decision 5 as first written, "gate exactly what lands" (§6.7), existed to rule exactly that out; this revision accepts it. Post-land CI on the GitHub mirror (mg-dd053) is the safety net. An enth pre-land gate (mg-b729a) may close the gap later, under a new GO.

### 6.2 The worker's land

Step 5 of the build protocol (`polecat.md`) and shape D of the architect protocol (`polecat-architect.md`) render one shell function, `self_land`. Their enth blocks carry the same text (`TestSelfLandIsTheSameInBothTemplates`). It is a bounded loop:

1. `pogo presubmit --item=<item> --print-stack` runs the pre-land check and prints the stack to land, bottom first. The check is §12's, plus the closing-ref check (R12) on every Change's whole message. The refinery ran that check on enth MRs, and now nothing else would.
2. `enth land [--ref refs/heads/<branch>] <stack> --request-id <item>/<attempt>`, with stdout kept.
3. On success, `pogo land --item=<item> --repo=… --landed-file=<stdout> --request-id=… --verdict-file=…` (§6.3).
4. `stale_tip`: nothing landed. It lands again under the **next** attempt number (at most six tries).
5. **No answer** (`unreachable`, `interrupted`, `bad_response`, or exit 4 with no error line): the land may have happened, so the **same** request id is re-issued. enthd answers what the first land answered and lands nothing twice. A new id would land the same Changes a second time (measured in slice 7; enth bug mg-19949). The attempt number and a `pending` marker live in `/tmp/<item>-land.*`, so a later `self_land` re-issues a pending request before it plans a new one.
6. `conflict` stops with exit 3, the conflict lines printed and nothing landed. The prompt's re-make (step 6) checks the new trunk out into a fresh directory, because an enth checkout cannot be moved. The worker re-applies the work, runs `./build.sh` (the only test the merged work gets), makes the Change with a `Pogo-Supersedes: <old ids>` trailer and runs `self_land` again. At most three re-makes; a fourth conflict goes to the coordinator.
7. Any other refusal stops with exit 1. A land that happened but could not be recorded stops with exit 2, `LANDED BUT NOT RECORDED`, and the instruction to re-run only `pogo land`.

**Why `Pogo-Supersedes`.** enth cannot withdraw a Change. Without the trailer, the Change that conflicted stays unlanded beside its re-make. `pogo presubmit` would then refuse the item as two stacks, and once the item closed, check-stranded would report the dead Change as stranded work forever. `vcs.DropSuperseded` drops every Change that a Change *of the same item* names in the trailer. A trailer naming another item's Change retires nothing. `enthvcs.Backend.ListChanges(…, unlanded=true)` now reads the whole listing (landed Changes included, because the superseding Change has usually landed by the time anyone asks), drops the superseded Changes, and keeps those whose `landed` is null. FindStack, presubmit, the trailer-less base check and check-stranded all read through it.

### 6.3 Completion: `pogo land --item`

One verb, in one place, testable: `pogo land` → `POST /refinery/land` → `Refinery.RecordSelfLand` (`internal/refinery/selfland.go`). It refuses, and records nothing, in these cases:
- a repo whose project record is not enth;
- output that is not exactly one `landed` line (a dry run, a conflict, nothing, two);
- a land that moved a ref other than the target (default: the project record's trunk);
- a record whose `new` is not its top Change's landed id;
- a Change that enthd does not list under `Pogo-Item=<item>`;
- a Change that enthd does not show as landed, or shows as landed under a different id.

pogo closes an item on what enthd says landed, never on the worker's word alone.

It then records the land as a merged MR in the refinery's history, with slice 8's `enth` land record (the request id included), `"self_landed": true`, `enth_submitted`, `enth_worker`, the verdict and `merged_sha` absent. It emits `refinery_merged` with `landed: {enth, N}` and fires the **same OnMerged callback** a refinery merge fires. So pogod closes the item exactly as it closes one today: the item is marked done; the sidecar names `landed {vcs: enth, id}` and carries the verdict; the filer is notified, the coordinator gets the `MERGED:` mail, and the polecat is stopped.

**The worker is not reaped before the land is recorded (mg-cef7).** The MR is written through to the state file *before* OnMerged runs (tested: the callback reads the file and finds the MR). The reap is therefore downstream of the record. A worker stopped between `enth land` and `pogo land` leaves landed work on an open item. `pogo land` can be re-run by anyone with the same output, and enthd replays the same request id to produce that output again. Recording the same land twice answers the first MR (`already_recorded`) and fires nothing.

**Not done by a self-land:** the per-repo deploy hook. The refinery did not land the work and holds no checkout of `N`. A repo that needs a deploy on land has none on enth until something runs it.

### 6.4 What was removed

The refinery no longer lands enth work:
- `pogo refinery submit` on an enth repo is **refused**, naming `pogo land` (`resolveEnthSubmission`). The listing it used to do at submit is gone with it.
- An enth MR that reaches the queue anyway (one queued before this change, or one restored by crash recovery) is failed on its first attempt, `class=defect`, before any git step, with the same pointer (`refuseEnthMR`, `requeueRecoveredEnth`).
- Deleted: the slice-7 pipeline (`enthland.go`: dry run, the gate checkout of `N`, `--expect-tip/--expect-new`, the `enth_pending` replay, enth crash recovery, the enth deploy, the `.enth` gate lock); the enth `Lander` (`Prepare` and `Land` now return `enthvcs.ErrSelfLanded`); `DryRun`, `LandExpect`, `CheckoutWhole` and `RemoveGateDir`; `scripts/enth_land_test.sh`.
- Kept: slice 8's merge-record shape (`enth`, `mr.Landed()`, `landed {vcs, id}`, every consumer), which a self-land writes, and slice 6b's check-stranded, which is how an unlanded Change is noticed. Its remedy is now the hand land plus `pogo land`, marked untested. The closing-ref judgement (`refinery.EnthClosingRefs`) moved to `pogo presubmit`.

**Proved by** `internal/refinery/selfland_test.go`, `internal/vcs/enthvcs/land_test.go` (both against a sandboxed enthd), and end to end by `scripts/enth_selfland_test.sh`. That script runs a real pogod and a real enthd, takes the `self_land` block from the prompt pogod rendered for a real (sleeping) polecat, and runs it in the polecat's checkout with the polecat's environment: 33 of 33 observations. It covers these cases:
- a clean self-land: the item is done, and the sidecar has `landed` and the verdict;
- `stale_tip`, retried and landed;
- a lost answer, re-issued under the same id, with one trunk entry;
- a conflict, re-made with `Pogo-Supersedes` and landed. check-stranded does not report the retired Change; as a positive control, it does report an unretired one;
- the refinery refusing an enth submit;
- the git control: a git branch through the same daemon gets a `merged_sha` and no `enth` record.

The `stale_tip` there is made by a wrapper `enth`, because a plain land without `--expect-tip` meets a real one only in a CAS race.

### 6.5 The superseded design (slice 7), for the record

What follows is decision 5 as first designed and built in slice 7 (mg-1fe2b): the refinery gated exactly what landed. It is **no longer in the code** (§6.5.4). It is kept here as the rationale an enth pre-land gate (mg-b729a) would start from.

#### 6.5.1 The pipeline for one MR

| Step | git today | enth backend | Entries |
|---|---|---|---|
| target exists | `ls-remote`, `rev-parse` | query the ref's tip | R2 |
| tip at start | `fetch`, `rev-parse origin/T` | `T` = the ref's tip on enthd | R7, R8 |
| rebase | `checkout -B`, `rebase origin/T` | `enth land C1…Cn --dry-run` against `T` → `N` (the `new` id) and the per-Change `landed` ids. A conflict is exit 3 with one stdout line per path | R10, R11 |
| closing-ref gate | `git log origin/T..B` | the submitted Changes' messages | R12 |
| gate | run in the rebased clone | **check out `N`** into the gate dir, run the gates there | R5, R13, R15 |
| land | `merge --ff-only`, `push` | `enth land C1…Cn --expect-tip T --expect-new N --request-id <MR>/<attempt>` | R16, R23 |
| record | `rev-parse HEAD` → `merged_sha` | the `landed` line (§7) | R17 |
| cleanup | `rebase --abort`, `push --delete B` | nothing | R6, R20, R21 |

`--expect-new N` is mg-b1b9e's addition: the land refuses with `result_mismatch` (exit 3) if its rebased result is not `N`. Together with `--expect-tip T` it binds the gated result to the landed one. **The tree that was gated is the tree that lands, or nothing lands.** `stale_tip` and `result_mismatch` are both "re-run from the dry run". They count against the existing `max_attempts` budget (default 7), as a lost ff-only race does today. `skip_on_retry` keeps its meaning (no gates on attempts after the first), and it keeps today's cost too: a retry it skipped landed a tree nobody gated. That is a property of the knob, not of enth, and this design does not change it.

#### 6.5.2 The prerequisite: a dry run's result must be checkable-out

**This is the one place the decisions rest on an enth behaviour that the enth items do not yet promise.** mg-b1b9e says `--dry-run` "returns the landed ids, writes nothing". If nothing is written, `enth checkout N` has no `N` to read whenever `N ≠ submitted`, which is every land whose base is not the tip. That is the common case at concurrency: epic §3.2 measured 53% of pogo's lands sharing a path with one of the previous three. The gate step needs the rebased Change records and any merged blobs to be **stored**, though not landed: no `L` record and no CAS. Content-addressed records that no ref reaches are harmless, and they are what a later real land would write anyway.

**Requirement E1 (§13):** `land --dry-run` stores the result Changes and merged blobs (or a `--store-result` flag does), so that `enth checkout N` works. The ask goes to pm-enth through pm-pogo, against mg-b1b9e.

**If enth declines E1,** the only exact fallback is to gate only lands where `N = submitted`. When the submitted base is not `T`, the refinery fails the MR as `stale_base` with the remedy "check out the target again and re-make the Change". That gives up the hunk merge (epic §3.2) for pogo's lands and pushes the rebase back onto the worker. It is named here so it can be rejected on purpose, not reached by default. Gating the submitted tree on its own base and trusting the merge is **not** a fallback: it is what decision 5 exists to rule out.

When `N = submitted` (the base is the tip), the submitted Change is already stored and the gate checks it out directly. This works with no enth change at all, and slice 6 can be tested on that path first.

#### 6.5.3 Crash recovery and already-merged (R9)

The request id `<MR id>/<attempt>` makes a land idempotent across a pogod restart (I12). `recovery.go`'s in-flight path re-issues the same land with the same request id. If it had landed, enthd replays the recorded answer and pogo records it. If it had not, it lands now or is refused `stale_tip`. A new attempt after `stale_tip` takes a new request id, because its `T` and `N` differ.

An already-merged resubmit (gh #34, `emptybranch.go`) keeps mg-c184d's rule: it resolves as `already_merged` only when the refinery's own history holds a merged MR for the same item whose `enth` record (§7) lists every submitted id. An item with no unlanded Changes and no such record fails as a defect: it delivers nothing.

#### 6.5.4 Gate reuse (R14)

git reuses a passed gate when a retry produces the same tree id (`merge.go:610,716`). enth has no tree id (epic §8 Q1). The reuse key is `N`, which identifies the result only from the same tip. So after a `stale_tip` the gate re-runs even when the tree is the same. This is a throughput cost, inferred and not measured. It is accepted for v1 and goes away if enth adds a tree id. mg-927f8's replay is the place to measure how often it fires.

#### 6.5.5 One Change stack per MR in v1; batch land later

v1 lands **one MR's stack per `enth land` call**: the Changes of one item, in order. That keeps parity with git, where one MR is one branch. It also means the refinery's lanes, attempt budget, failure routing and verdicts all keep their one-MR shape.

**Batch land is a later slice (slice 13).** It would land several MRs' stacks as one trunk entry. What it would buy, from epic §6:
- **Merge-queue throughput.** One dry-run conflict pass, **one gate run**, one store write, one log commit and one CAS cover n MRs. The gate is the expensive part: pogo's gate is `./build.sh`, measured in minutes, against enth's ~2 durable steps per land.
- **Fewer gate re-runs.** `stale_tip` races are between lands. Landing n at once means one race window instead of n.
- **Atomicity across MRs** when two items must land together, which git cannot offer.
- One mirror push and one CI trigger per batch (#11, M3).

What it costs is the reason it is not v1: a red gate on a batch has to be bisected to find the failing MR, and the refinery's per-MR states become per-batch states. That is a design of its own.

#### 6.5.6 What the refinery no longer needs on enth

No private clone for landing (R5: a gate dir is still needed and is a fresh checkout per attempt), no fetch (R7), no credential helper (R24, replaced by the enthd token), no remote-URL lookup for failure records beyond the enthd endpoint (R22), no committer identity (R23: `land` keeps author and committer and records the lander in the `L` record), and no `refinery prune` (R21). `refinery.toml` keeps every key and its meaning, except `pr_mode`, which is refused on an enth repo (§11).

#### 6.5.7 As built (slice 7, mg-1fe2b, 2026-11-03)

Against enth `0.2.0-dev` (#9 B, D, E, F, G), measured by `internal/refinery/enthland_test.go` and `internal/vcs/enthvcs/land_test.go` against a sandboxed enthd, and end to end by `scripts/enth_land_test.sh` (a real pogod and a real enthd: 26 of 26 observations, with the git control). What differs from the text above, or was settled by building it:
- **E1 is used, not the `N = submitted` fallback.** `vcs.Lander.Prepare` on enth is `enth land --dry-run --ref <target> C1…Cn` and then a whole-repo `enth checkout N` into the refinery's gate dir, `<worktrees>/<repo>.enth-gate`, re-made from scratch on every attempt. `Prepare` checks that the checkout's base reads back as `N`. The tests gate a land whose base is behind the tip, so `N` exists only because the dry run stored it, and they check that the gate saw `N` and the trunk then holds `N`.
- **`vcs.MR` carries `Changes` and `RequestID`; `vcs.Prepared` carries `Tip`, `New` and the dry run's per-Change plan; `vcs.Landed` carries the `landed` line as `Record`.** git leaves all of them empty, and the git Lander does not read them.
- **The request id is `<MR id>/<attempt>`, and the land is persisted before it is sent** (`enth_pending` on the MR, written through). Measured: a land of already-landed Changes under a **new** request id is not refused; enth lands them a second time as a new trunk entry (pm-enth: a bug, `already_landed` checks only the Change's own id on the ref; filed as mg-19949. pogo's guard stays regardless). So a land whose answer was lost (a dropped connection, a killed pogod) is re-issued with its **original** request id before anything is re-planned. enthd replays the recorded answer if it had landed, even after the tip has moved on to `N`. A refusal that establishes nothing landed (enth's exit-3 codes, `unauthorized`, `forbidden`: `enthvcs.LandedNothing`) clears it. `unreachable`, an unknown code or no answer at all leaves the outcome unknown, and the attempt fails without re-planning. A land that never reached enthd with the tip unmoved simply lands on replay, because it is persisted only after its gates passed on that `N`.
- **Crash recovery (R9)** has no git probe for an enth MR. With a pending land it replays it: landed means merged (OnMerged fires, the gate is not re-run), refused means re-queued at head. An enthd that does not answer re-queues it with the pending land kept, and the re-run replays it first. An enth MR is never marked lost by recovery, because everything needed to finish it is on the record. `scripts/enth_land_test.sh` step 8 SIGKILLs pogod from inside the land (after enth landed, before pogod read the answer) and restarts it.
- **Retries** go through `retryPolicy`, which is processMerge's budget switch moved out unchanged so both pipelines share it. `stale_tip` and `result_mismatch` are `contention` and count against `max_attempts`. enthd's `unreachable` is `infrastructure` and retried on the network budget. `unauthorized`/`forbidden` are `infrastructure`, not retried. `conflict` is a `defect` at stage `rebase` naming each path. Every other enth refusal code is a `defect`, and `request_id_reused` (a refinery bug) is `infrastructure`, not retried. Stage names stay git's (`rebase`, `closing-ref-check`, the gate stages, `push`), so readers of `AttemptFailure.Stage` need no enth table.
- **Gate reuse (§6.5.4)** holds a passed gate on `N`. A `result_mismatch` retry from the same tip re-plans to the same `N` and replays the verdict. A `stale_tip` retry re-plans to a new `N` and re-gates.
- **The already-merged rule (§6.5.3)** reads the submitted Changes' land records (E3) on every attempt. If all have landed, the MR resolves `already_merged` only when history holds a merged MR for the same item and repo, not itself a no-op, whose land record lists every submitted id. Otherwise it fails `class=defect` at stage `empty-branch` ("NOTHING WAS DELIVERED"). Since a submit lists only unlanded Changes, this is reached by a resubmit queued before the first one landed, or by a land made out of band. Some-but-not-all landed is a defect too.
- **The closing-ref gate (R12)** reads every submitted Change's whole message (subject included, as git's `%B`) through `enth query show`. There is no PR body on enth.
- **`pr_mode` is refused** at submit (the repo's `.pogo/refinery.toml`) and at land time (either copy, including the submission's own), as a `defect` naming the key.
- **The `enth` land record is written now (§7's shape, plus the request id), not in slice 8.** The already-merged rule and recovery both read it. `merged_sha` stays empty and the `refinery_merged` event carries no merge commit. Slice 8 still owns `mr.Landed()`, the event's `landed` field, the deploy-hook environment, and the pogod consumers that print `merged_sha` (which print nothing for an enth MR until then).
- **Deploy** runs in a checkout of the landed `N` (the gate dir, re-checked-out after a replay). The source read checkout is not refreshed after a land: it usually has no pogo workspace record, so `Refresh` would decline. Crew freshening covers it.
- **The gate lock** of an enth gate dir lives in `.enth/pogo-gate.lock`. enth skips `.enth/`, so the lock is never part of a Change. A git clone's lock path is unchanged.

## 7. The merge record (decision 6)

An enth MR records its land under a field named for the VCS, and leaves `merged_sha` empty:

```json
"merged_sha": "",
"enth": {
  "ref": "refs/heads/main",
  "entry": 1042,
  "old": "<T>",
  "new": "<N>",
  "changes": [{"submitted": "<id>", "landed": "<id>"}]
}
```

These are the fields of enth's `landed` line (epic §3.3), stored as given. A reader that wants a git sha finds `merged_sha` empty and an `enth` object it does not know, so it cannot take a 64-hex Change id for a 40-hex git sha by accident.

The consumers of `merged_sha` become VCS-aware through one accessor, `mr.Landed() (vcs.Kind, id string)`, rather than each reading the field:
- `internal/refinery/emptybranch.go:65-82` (the prior-merge check, §6.5.3),
- `internal/refinery/events.go:186,205` (the event carries `landed: {vcs, id}` alongside the existing `merged_sha`, which stays as it is for git),
- `internal/refinery/postmerge.go` (§8),
- `cmd/pogod/refinerymerged.go:92`, `mergedopen.go`, `reopenguard.go:169-231` (the merged-but-open and reopen guards, which print the id with its kind).

The deploy hook gets `POGO_LANDED_VCS` and `POGO_LANDED_ID` in its environment. Any variable that carries a git sha today stays unset for an enth MR rather than holding a Change id.

### 7.1 As built (slice 8, mg-01821, 2026-11-03)

- **`mr.Landed() (vcs.Kind, string)`** is `(git, merged_sha)` for a git MR and `(enth, enth.new)` for an enth one. An MR is enth when it has an `enth` record or `enth_submitted` Changes. Both are empty on every git MR, so a git MR answers exactly what `merged_sha` says. `mr.LandedLabel(n)` renders it for people: a git sha unchanged, and `enth <id>` for enth. `mr.EnthLandedDetail()` is the `{vcs, id}` object, and it is present for an enth landing only.
- **Git output is byte-unchanged.** `TestGitMergeRequestJSONUnchanged` pins a git MR's JSON. The same golden passes on the pre-slice `main` and fails when perturbed. Every pogod consumer keeps its git rendering and its `merged_sha` key. For an enth MR, each one adds the landing beside the key instead of putting a Change id in it.
- **Consumers.** `emptybranch.go`'s ancestry check considers only git landings. The enth already-merged rule stays in `enthland.go`. `refinery_merged` carries `landed` for **both** kinds, as this section says (`merge_commit` is `""` for enth). The other consumers add `landed` only for enth: the merged-work dispatch gate (`refinerymerged.go`, `agent.MergedWork`), the merged-but-open alert and its gated event (`mergedopen.go`), the reopen guard's mail line and event (`reopenguard.go`), the reap sidecar and its log lines (`reap.go`), `work_item_closed_without_verdict` (`reapverdictabsent.go`), the filer completion notice and its event (`filernotify`, `filernotifywire.go`), the pogod `MERGED:` mail (`Landed: enth <id> (entry N on <ref>)`), and `pogo refinery show` (`Landed as: …`).
- **`merged_sha` stays absent on an enth MR**, in the record (`omitempty`), the sidecar and every event. `pogo refinery show <mr> --json | jq .merged_sha` is therefore `null`, and the release-cut recipe refuses to tag. That is the safe direction until slice 12 maps tags.
- **The deploy hook** gets `POGO_LANDED_VCS` and `POGO_LANDED_ID` on both paths (git: the merge commit; enth: `new`). Both are always set, and both are empty when the landing is unknown, so a copy inherited from pogod's own environment is overridden rather than stripped. The environment still roots in `os.Environ()`, as gitceiling's inherit check requires. No variable carries a git sha for an enth MR. This was measured end to end against a sandboxed enthd, with the git control, by `TestEnthMergeRecordAndGitControl`.
- **Not done here:** `postmerge.go` (§8) is slice 12. An enth submit with `--post-merge-tag` is still refused at submit. Messages that named `branch %s` printed an empty branch for an enth MR. Slice 8b fixed them (§7.2).

### 7.2 As built (slice 8b, mg-4377f, 2026-11-03): messages name the item, not an empty branch

An enth MR has `branch: ""`. Its work is named by its item (`Item`, the Pogo-Item trailer, §5). `vcs.WorkRef(branch, item)` (and the `mr.WorkRef()` wrapper) fills the slot that `"branch %s"` filled. It gives `branch <name>` for git, byte-identical, and `enth item <id>` when there is no branch but there is an item. `WorkField` is for a slot already labelled as the branch (`Branch:`, `(%s -> main)`) and gives `(none: enth item <id>)`. `WorkKV` is for `key=value` log lines and gives `item=<id>`, the key the enth queued line already used. A git MR always carries its branch, so every git rendering is unchanged. Each fixed message has a test with an enth case and a git control asserted against the old format string (`cmd/pogod/workref_test.go`, `internal/agent/mergedgate_test.go`, `internal/vcs/workref_test.go`).

The call sites, judged. This is `grep -rn --include='*.go' -iE 'branch %s|branch %q'` over non-test code, plus the other places that format an MR's `Branch` (`Branch: %s`, `branch=%s`, `(%s)`):

| Call site | Judgment |
|---|---|
| `internal/agent/mergedgate.go` merged-work refusal | **fixed**: `vcs.WorkRef`. `MergedWork` gained `Item` (`refinerymerged.go`) |
| `cmd/pogod/reopenguard.go` declined-reopen line | **fixed**: `mr.WorkRef()` |
| `cmd/pogod/mergedopen.go` alert log line, `Branch:` body line | **fixed**: `WorkRef` / `WorkField`. The alert gained `Item`. The event `branch` key is unchanged (machine data; the record has `item`) |
| `cmd/pogod/reap.go` MERGED BUT NOT CLOSED log | **fixed**: `mr.WorkRef()` |
| `cmd/pogod/progresswatch.go` submitted/landed notes | **fixed**: `submittedNote` / `mergeLandedNote` |
| `cmd/pogod/projectvcs.go` busy-repo reason | **fixed**: `mr.WorkField()` |
| `cmd/pogod/refineryfailroute.go` unrouted reason | **fixed**: the enth form names the item and says it has no branch to carry the prefix |
| `cmd/pogod/main.go` `MERGED:` mail subject, body, and log | **fixed**: `mergedNoticeHead` (`WorkKV` / `WorkField`) |
| `cmd/pogo/main.go` `refinery show` `Branch:` | **fixed**: `mr.WorkField()`. The test is the `WorkField` unit test only, because the line is inside a cobra `Run` |
| `internal/refinery/lanes.go` merged/no-op/cancelled/post-merge-failed logs; `refinery.go` cancel log | **fixed**: `mr.WorkKV()`. The test is the `WorkKV` unit test only, because these log inside the lane runner |
| `internal/refinery/refinery.go` queued log, PR-flow log | git-only: the enth path logs its own `item=` line and returns before both |
| `internal/refinery/merge.go`, `land.go`, `closingref_gate.go`, `recovery.go` (probe and resolve logs), `postmerge.go` tag message, `prune.go`, `submitbranch.go` | git-only: `processMerge` and `resolveRecoveredOne` send enth MRs to `processEnthMerge` / `resolveRecoveredEnth` first, and an enth post-merge tag is refused at submit (§8) |
| `internal/refinery/submititem.go` | already enth-aware: these are the item-vs-branch refusals themselves |
| `internal/refinery/gatelock.go` `(branch %s)` | guarded by `Branch != ""`, so it never prints empty |
| `internal/agent/*` (`api.go`, `stranded*.go`, `liveownergate.go`, `spawntarget.go`, `enthgithub.go`), `internal/gitgc`, `internal/strandwatch`, `internal/strandedwork`, `internal/freshen`, `internal/staleness`, `internal/mgcontract` | not an MR's branch: these name a polecat worktree's git branch, a remote ref, or a `--branch` flag, which exist only on git |

## 8. Tags and release refs (decision 7)

- **`--post-merge-tag X` (R18)** maps to `enth tag X <new>` (mg-40d5e). That is a CAS create of `refs/tags/X`, and a tag is immutable. Today's git path deletes and re-creates a local tag to stay idempotent (`postmerge.go:102-155`). The enth path cannot do that and should not: a retry finds `tag_exists`, reads the tag, and succeeds if it already names `new`. It fails as a post-merge error, as today, if the tag names anything else.
- **`--auto-create-target` (R4)** and **release branches (L4)** map to `enth ref create refs/heads/<T> <id>` with CAS (mg-40d5e). `docs/release-process.md`'s "push CAND to `release/vX.Y.Z`" step becomes `pogo refinery ref-create release/vX.Y.Z <id>` so that it goes through the seam. Landing onto it is the ordinary land path with `--target`.
- **On a followed repo** every one of these is refused by enth (`source_authoritative`, epic §4). That is the same refusal that keeps pogo's own repo on git (§1).
- **L2 and L3** (pogo's own `bump-version.sh` and `check-version.sh`) are out of scope (§1). A managed enth repo's release tags arrive on GitHub through the export (§11), so tag-triggered workflows keep working (H6).

## 9. Ignoring pogod's files (decision 8)

pogod writes `.claude/` hook settings and persona files into every workspace (W8). On an enth workspace it writes their patterns into `DIR/.enth/info/exclude` right after the checkout, from the same list it puts into `.git/info/exclude` today (`internal/agent/command.go:210`). `.enth/info/exclude` and `.gitignore` handling are mg-2ef01's (pogo's ask #4): the repo's `.gitignore` files with git's semantics, plus the local exclude list.

Two consequences:
- **`Dirty` (P1) uses `enth status` alone.** The `.claude/` carve-out that `gitgc` applies after `git status` (`gitgc.go:880-935`) is not needed on enth, because the exclude list keeps those paths out of `status` in the first place. The carve-out stays on git, where repos without `.claude/` in their `.gitignore` still need it (gh #240).
- **A tracked file is never ignored** (mg-2ef01, git's rule). If a repo ever tracks a path under `.claude/`, pogod's write to it shows as an edit and would be landed. That is the same as git today, and the same guard applies: `presubmit` refuses a dirty workspace and names the path.

## 10. The revision stamp (decision 9)

**Measured, not inferred.** On 2026-11-02 with `go1.24.0 darwin/arm64` and `enth 0.0.0`, a one-file module was imported into a scratch store (`enth --store S import SRC`) and checked out (`enth --store S checkout main DIR`). Then each binary below was built with `go build` and read with `go version -m BIN | grep vcs`:

| build directory | `vcs.revision` |
|---|---|
| the git source repo (**positive control**) | `863f940…`, the commit (also `vcs=git`, `vcs.modified=false`) |
| the enth checkout, not inside any git repo | **absent** (no `vcs*` line at all) |
| the same, with a stub `.enth/checkout` file added (the M1 binary writes no `.enth/`; #9 adds it) | **absent** |
| an enth checkout made **inside** another git repo | **`0fa7d93…`, the OUTER repo's HEAD**, with `vcs.modified=true` |
| the same, with `GIT_CEILING_DIRECTORIES` set to the outer repo | **the outer repo's HEAD** again |
| the same, with `-buildvcs=false` | absent |

So the inventory's inference (D4: "absent") holds only for a checkout outside any git repo. **Inside one it is worse than absent: it is foreign.** That is exactly where pogo puts workspaces. `~/.pogo` is a git repo (measured: `git -C ~/.pogo rev-parse --show-toplevel` prints `/Users/daniel/.pogo`), and polecats live at `~/.pogo/polecats/<x>`. Go walks up from the module to the first `.git` it finds, and pogo's `gitceiling` does not stop it. `build.sh --install` already reports this failure for nested git worktrees ("Go stamped the OUTER repo's HEAD"), and an enth workspace always triggers it.

**Design.** `build.sh`'s `version_ldflags` gets an enth branch, taken when `git rev-parse HEAD` fails in the build's own directory and `.enth/checkout` exists there:
- `-X version.Commit=<the checkout's base Change id>`, `-X version.Build=<its first 12 hex>`, `-X version.Branch=<the checked-out ref>`, `-X version.Dirty=<enth status non-empty>`, and a new `-X version.VCS=enth`. `VCS` is empty for git, so git binaries are byte-identical.
- **`-buildvcs=false`** on every `go build` and `go install` in that branch, so Go cannot stamp the outer repo. `verify_installed_stamps` then expects `vcs.revision` to be absent and the ldflags `Commit` to equal the base Change id.
- The git branch is untouched, and the check for it is the existing build tests.

`internal/version` already prefers ldflags over build info (`resolve.go`, `SourceLdflags`). It gains `Info.VCS`. `driftwatch` and `revcheck` compare a revision with git ancestry, and an `enth` stamp makes them report "not comparable: enth revision" instead of arming against a git main they cannot place. Teaching them enth ancestry is part of moving pogo's own repo (§14.2).

The measurement was made on the M1 binary with a stub `.enth/checkout`. Slice 10 re-runs the same table on a #9 checkout before it merges.

**Re-run on #9, and what slice 10 changed (mg-c8568, 2026-11-03).** `scripts/enth_stamp_test.sh` is the table as a script. On enth `origin/main` `6b6fbd4` (built from source: the installed `enth` was still `0.0.0` and wrote no `.enth/`) with `go1.25.0 darwin/arm64`, every row reproduced: the git control stamps its commit; a real #9 checkout (`.enth/checkout` format `enth-checkout` version 1) outside git carries no `vcs.revision`; nested in a git repo it carries the outer HEAD, `GIT_CEILING_DIRECTORIES` does not help, and `-buildvcs=false` removes it. Two departures from the design above, both forced by the measurement:
- **The branch is not "`git rev-parse HEAD` fails".** Inside an outer repo, the case that matters, `git rev-parse HEAD` *succeeds* with the outer repo's HEAD, so that test would have stamped the foreign sha as `version.Commit`. The test is `stamp_vcs` (`scripts/lib/enth-stamp.sh`): `.enth/checkout` exists in the build directory **and** git's top level for it is not the directory itself. A git repo rooted there still wins.
- **No `version.Branch`.** #9's `.enth/checkout` records the base, the origin and the selection but not the ref that was checked out, so there is nothing to read. An enth binary reports `branch=unknown`. When pogo's enth workspaces (slice 5) record the ref, the stamp can carry it.

`Dirty` comes from the `ops` count on `enth status`'s first line. When that cannot be read (no `enth` on PATH, an enth older than #9, a refusal) the build is stamped `Dirty=true` and the log says why: a tree nobody checked is not called clean. `selfdrift` and `revcheck` see an enth binary as `<enth>` (from the recorded `-X ...version.VCS=enth`, which wins over any `vcs.revision`) or an enth daemon (from `build.vcs` in `GET /version`). Both report UNKNOWN with "not comparable: enth revision". `driftwatch` disarms its staleness check with the same reason. `scripts/pogo-self-deploy` is untouched: it builds pogo's own repo, which stays git (§1).

## 11. GitHub stays GitHub (decision 10)

Unchanged for every repo: issue intake and carriers (H3), credentials (H5), `homevcs` (H4, X5), Releases, Actions and `install.sh` (H6).

**An enth repo has no PR flow (H1, H2).**
- `pr_mode = true` in an enth repo's `refinery.toml` is refused at config load, with the reason. That beats a push-back to a branch that does not exist.
- Spawning a `--branch` PR-flow polecat (`polecat-build-pr.md`; MR `PRFlow`, `DeferDone`) on an enth repo is refused at spawn.
- The closing-ref gate (R12) still checks Change messages for GitHub closing keywords. A message lands on the mirror verbatim, so `Fixes #N` still closes the issue there. There is no PR body to read, so `closingref_gate.go:169-172`'s `gh pr view` step is skipped.
- The open-PR probe (`strandedwork.PRProbe`, `GitHubOpenPR`, H2) is skipped for an enth repo. Under self-landing (§6, mg-5b904) there is no refinery MR to stand in for it either. The stranded check on enth is the unlanded listing, not a review probe (slice 9b, below).

**As built (slice 9, mg-2e9fc, 2026-11-03).** What building it settled:
- **PR-flow spawn refused** (`internal/agent/enthgithub.go`, `enthPRFlowRefusal`). Two shapes are PR flow, and both are a 409 before any side effect: the `polecat-build-pr` template, and a `--branch` (or the item's `branch:`) naming a ref other than the repo's trunk. On git the second is an integration branch whose completion is a PR (MR `PRFlow`), and an enth repo has none to open. A `--branch` naming the trunk itself is allowed. The same requests on a git repo are dispatched as before (the test's git control). Slice 12 may relax the second shape for release branches.
- **Open-PR probe skipped.** Slice 9 routed both callers, the release gate and the startup sweep, through `Registry.checkOpenReview`. It skipped GitHub on enth and asked the refinery queue for an open MR (`Finding.CheckOpenMR`, `open_mr`). **Slice 9b (mg-35ae4) removed that wiring:** the refinery refuses enth MRs, so the answer was always "no". It also could not be reached, because both callers ran `strandedwork.Inspect` first, and that fails on an enth checkout. See the slice 9b note below.

**As built (slice 9b, mg-35ae4, 2026-11-03): pogod's stranded detectors on enth.**
- **Measured first, before any change.** The setup was a sandboxed enthd, an item with one unlanded Change, and the project path an enth read checkout. The release reporter's `Inspect` failed (`show-ref … exit status 128`, no `.git`). It logged "could NOT check" and sent **no event and no mail**. The restart sweep counted the item `Unjudged`, log only. The dispatch gate (`GitStrandedWorkGate`) failed open ("dispatching WITHOUT the stranded-work check"), and **slice 9b leaves it that way**: it is outside this slice. (mg-e2ab1 closed it; see the next note.)
- **The branch** (`internal/agent/enthstranded.go`). It sits in front of `Inspect` in `reportStrandedWorkOnRelease` and `ReportStrandedWorkAcrossRestart`, reached only when the project record says enth. It lists the item's unlanded Changes with `enthvcs.Backend.ListChanges(item, unlanded)`. That is the listing `pogo check-stranded` joins on Pogo-Item (slice 6b), and it already drops landed and superseded Changes. The row is described by `strandwatch.NewEnthWork`/`EnthLabel` and remedied by strandwatch's enth remedy (`enth land … && pogo land --item`). So the mail, the event and `check-stranded` print the same land line.
- **Stranded** means unlanded, non-superseded Changes on an item with no live polecat on it, where a live polecat is one other than the one being released, found in the registry or the witness. The item's status is not consulted, so a done item is reported, as in 6b. A sweep candidate that is still running counts as in flight, because under self-landing it can still land its own work. It goes in the sweep's new `in_flight` count, which `work_item_stranded_sweep` carries only when it is non-zero.
- **An unreadable listing alerts.** It sends the mail and the event with `disposition: unjudged`, and the sweep also counts it `Unjudged`. "Could not ask enthd" is never rendered as "no work".
- **CheckOpenMR removed**, along with `OpenMRProbe`, `Finding.OpenMR`, `Registry.checkOpenReview`/`refineryOpenMRProbe`, and the mail's "MR check" line. `strandedgate.go`, `strandedsweep.go` and `strandedmail.go` reverse-apply slice 9's hunks, so the git path is byte-identical to before slice 9 (`git diff 4b43b694^` on those files shows only the enth branch).
- **Closing-ref gate without the PR body.** Slice 7's land pipeline already read each Change's whole message, subject included (`enthvcs.Backend.FullMessages`), and never ran `gh`. Slice 9 moves that judgement into `enthClosingRefs` (`internal/refinery/closingref_gate.go`), which `Refinery.checkEnthClosingRefs` calls. It adds two things. The report now carries a `closingref.ChangeMessage` remedy, because the commit remedy tells an enth author to git-amend. And an MR with no Changes, or a read that returns fewer messages than Changes, is now refused instead of judged clean. The test's git control checks that `checkClosingRefs` still runs `gh pr view` with the same stub `gh` on PATH.
- **O4:** `pogo doctor --check` prints an `enth in PATH` row only when some project is recorded as enth. It fails when there is no `enth` on PATH, and warns when the `enth` found predates #9 (no `status DIR` in its help) or when its help cannot be run. It asks the CLI's PATH, which can differ from pogod's, and the row says so. A host with only git repos prints no new row.
- **`pr_mode` refused** shipped in slice 7 (§16).

**As built (mg-e2ab1, 2026-11-03): the dispatch gate on enth.**
- **The branch** (`internal/agent/enthstrandedgate.go`, `Registry.enthStrandedCheck`). When the project record says enth, the spawn's stranded-work gate judges from slice 9b's listing (`judgeEnthItem`) and does not consult `GitStrandedWorkGate` at all. An item with unlanded, non-superseded Changes is refused with a 409. The refusal names the Changes, the self-land remedy (`enth land … && pogo land --item`), and both exits. Landed and superseded Changes do not refuse. A live polecat on the item is not an exemption, as on git; the refusal names it.
- **The exits.** `--stranded-override` starts the worker from the trunk and records `dispatch_stranded_work_overridden`, as on git. `--stranded-adopt` is not resolved by `adoptableFinding`, whose findings name branches. It falls through to slice 6's `resolveEnthAdoption`, which makes the checkout at the stack's top Change. The `dispatch_stranded_work_adopted` event now carries the gate's refusal. An enth adopt on a spawn that makes no checkout is refused, as git's is.
- **An unreadable listing fails open, and alerts.** It fails open in the git gate's direction, but not silently. Once the spawn has gone out, the response's `base.warning` says it was dispatched UNCHECKED. A `work_item_stranded_push` event goes out (`route: dispatch`, `disposition: unjudged`, `polecat`), and so does a `[stranded-push] … dispatched WITHOUT the stranded-work check` mail naming the polecat and the listing to reproduce. A spawn refused later by another gate dispatched nothing and sends no alert.
- **Git byte-unchanged.** A git repo takes the old `strandedWorkCheck` call and switch unchanged. `TestEnthStrandedGateGitControl` and the existing stranded suites cover it.

**The GitHub mirror.** An enth-written repo is mirrored to GitHub by `enth-git-export --follow` (enth #10/#11, architecture.md §5, milestones.md §5). It subscribes to `ref.moved` and pushes on each land, a reconcile pass runs every 5 minutes and alarms if a GitHub ref is not at the expected sha, and branch protection lets only the enth bot push. **pogo does not run the export and never pushes to the mirror.** One writer per ref is enth's rule, and pogo pushing would make a second writer. The enth backend therefore has no `git push` in it at all, which keeps that rule structural.

For CI this means:
- **Actions run on the mirror**, triggered by the bot's push, after the export lag. Tags exported the same way trigger the release workflow (H6).
- **Mapping a land to its mirror commit.** Export is deterministic and writes an `Enth-Change: <id>` trailer (architecture.md §5), so a landed id has exactly one mirror sha. pogo needs that sha to ask GitHub about CI for a land (the pm prompts' `gh run list`). **Requirement E5 (§13):** an enth verb that returns the exported git sha for a Change id. Without it, pogo would have to search the mirror's log for the trailer, which is slow and races the export. The sha goes into the `enth` record as `mirror_sha` once the export reports it. It is never written into `merged_sha` (§7).
- `enth-git-export` is not built yet (measured: not among the installed `enth-*` tools). The mirror is enth's M2 work and is out of pogo's scope. pogo has nothing to configure for it.

## 12. Prompts: one template, VCS-specific blocks (decision 11)

`TemplateVars` (`internal/agent/prompt.go:196`) gains `VCS` (`"git"` or `"enth"`) and, for enth, `EnthRemote` and `EnthRepo`. The templates branch inside themselves:

```
{{if eq .VCS "enth"}}
   enth change {{.WorktreeDir}} -m "<type>: <description> ({{.Id}})" \
       --trailer Pogo-Item={{.Id}} --trailer Pogo-Worker=$POGO_AGENT_NAME
{{else}}
   git add <files>
   git commit -m "<type>: <description> ({{.Id}})"
   git push origin "$BRANCH"
{{end}}
```

There is one template per role and no `polecat-enth.md`. The git output is byte-identical when `VCS` is `git` or empty. The test renders every embedded template with `VCS=""` and compares it with the output from before the change.

The blocks are the load-bearing instructions the inventory names: the polecat protocol in `polecat.md` and `polecat-architect.md` (P2–P5), the review and QA checkout (`polecat-review.md:106-117`, `polecat-qa.md:100-101`), and the PR-flow steps, which are omitted for enth (§11). pm and mayor prompts name GitHub, not git, and need no blocks. The measured counts are 116 git lines and 45 gh lines across 9 files (inventory, "Prompts and templates").

**Where a block is a predicate, it becomes a pogo verb.** `presubmit_check` (P2) is 15 lines of shell that a worker runs. Duplicating it per VCS would put two copies of it in each of two templates. Instead `pogo presubmit --item=mg-X` runs the check through the seam. On git it does exactly today's checks, in today's order: fetch, `worktree-dirty`, ahead count, HEAD equal to `origin/$BRANCH`. On enth it checks that `Dirty(DIR)` is empty and that `Work.Find(item, worker)` returns one non-empty stack whose top is the workspace's base. The template's block shrinks to one line, the same for both VCSs. `pogo worktree-dirty` (P1) already works this way.

**The installed copies.** `~/.pogo/agents/templates` and the prompt stubs are rendered from these files and may carry local edits (inventory). A deployed template without the new blocks renders the git text for an enth repo. Slice 11 makes pogod refuse to spawn onto an enth repo when the resolved template has no `.VCS` reference, rather than hand a worker a git protocol it cannot follow.

**As built (slice 11, mg-7c936, 2026-11-03).** What differs from the text above, or was settled by building it:
- **`presubmit_check` stays in the git arm.** §16 requires the git render to be byte-identical, and the shell is part of today's git render, so only the enth arm runs `pogo presubmit --item=<item>`. `pogo presubmit` already equals the shell on git (slice 6, `TestPresubmitGitEqualsTheTemplateShell`, which still lifts the shell from the templates). Swapping the git arm to the verb is a separate, deliberate change of the git text. **Done (mg-71014):** the git arm of `polecat.md` and `polecat-architect.md` now runs `pogo presubmit --item=<item> --target=<target> &&` before `pogo refinery submit` (the other four templates never submitted). Against `2f237ed9` the render diff is that line plus the prose naming it, in those two templates' git renders only (128 of 384 differ; enth renders and the other four templates identical). `TestGitSubmitRunsPogoPresubmit` pins the line, and the shell is kept in `cmd/pogo/testdata/presubmit_check.sh` as the verb's reference.
- **The proof of byte-identity is a test against a revision.** `TestGitRenderUnchangedSinceBase` renders every embedded template with `VCS=""` across a 64-shape matrix (branch, no-worktree, SME, cores, claim restamp, provider) and compares it with the same template at `$POGO_TEMPLATE_BASE`. It is opt-in, because any later edit to a template's git text is a change it would report. Against `a085a530` (the parent of this slice): 384 of 384 renders identical. The control: against the revision before an earlier `polecat.md` edit, 128 renders differ. Always on: `VCS=""` and `VCS="git"` render the same, no enth render carries a git step (`git push`, `git commit`, `presubmit_check`, `BRANCH=$(git rev-parse`, `git checkout -B`, `gh pr comment`, …; each forbidden string is checked to occur in some git render first), and every shipped template references `.VCS`.
- **`enth change` takes its options before DIR**, as enth-checkout's usage gives it: `enth change -m "…" --trailer Pogo-Item=<item> --trailer Pogo-Worker="$POGO_AGENT_NAME" DIR`. The worker name is the shell variable, so the command is complete as rendered.
- **No PR flow on enth.** The `{{if .Branch}}` PR steps in `polecat.md` and `polecat-architect.md` are `{{if and .Branch (ne .VCS "enth")}}`: slice 9 refuses a non-trunk `--branch` on enth at spawn, so a `--branch` that reaches an enth worker is the trunk, and it submits there like any other. Slice 9 also refuses the `polecat-build-pr.md` spawn on enth (§11); the template still renders, on enth, only a notice telling the worker to stop and mail the coordinator, for a dispatch that reaches it some other way (an installed copy under another name).
- **Review and QA** list the build item's unlanded Changes (`enth changes --unlanded --trailer Pogo-Item=<item>`), take as top the one no other listed Change is based on (a jq line, tested on one stack, two stacks and none), stop on anything but exactly one, and `enth checkout` it into a `mktemp -d` scratch directory outside `~/.pogo`. A checkout cannot be re-pointed, so a later round checks out again. Findings that went to `gh pr comment` go in the round mail.
- **The spawn refusal** reads the template's parse tree, not its text, so `.VCS` in prose does not count and `$.VCS` does. It checks the body the worker would be rendered from: the resolved template plus its drop-ins. A template that does not branch is refused 409 before any side effect, with the remedy (`pogo agent prompt install`).
- **Each arm starts on its own line.** A block that replaces whole lines is written `P{{if eq .VCS "enth"}}\nENTH{{else}}\nGIT{{end}}`, with the newline moved inside both arms, and an arm that ends in a closing fence gets its own newline. The prompt-corpus checks (the `--body=` ratchet, the CLI-surface check) read the raw source with both arms present, so a list marker or a fence that followed `{{else}}` on one line was invisible to them and flipped their fence parity for the rest of the file.
- **Not done here:** the "running daemon carries the control" bullet still shows `git merge-base`, because it asks about pogo's own repo, which stays git (§1).
- **Since mg-5b904** the build and architect enth arms end in `self_land` (§6.2) instead of `pogo refinery submit --item`, and the steps that polled the refinery are an enth arm of their own (what to do when `self_land` did not land, and what happens when it did). The git arms are still byte-identical to 26ae00d9 (`TestGitRenderUnchangedSinceBase`, 384 of 384).

## 13. What enth must provide

| # | What | enth item | State on 2026-11-02 |
|---|---|---|---|
| E1 | `land --dry-run` stores its result so `enth checkout N` works (§6.5.2) | ask against mg-b1b9e | **not promised** at writing; **shipped in #9 item D** ("a dry run stores what it predicts", epic §3.4) |
| E2 | `land --expect-new N` → `result_mismatch` | mg-b1b9e | placed (19:20Z section); **shipped in #9 item D**, used by slice 7 (§6.5.7) |
| E3 | the landed complement of the listing: `--landed`, or a `landed` field (§5) | ask against mg-10079 | **not promised** at writing; **shipped in #9** as the `landed` field of `enth changes` (slice 6) |
| E4 | `enth changes --unlanded --trailer K=V` through enthd | mg-10079 | placed |
| E5 | exported git sha for a Change id (§11) | ask against #10 | **not promised** |
| E6 | `.enth/checkout` records the remote and repo (§4) | ask against mg-2ef01 | **not promised** at writing; **shipped in #9** (measured 2026-11-03, slice 5: `origin` carries both). pogod's record still carries the project and the ref |
| E7 | `enth tag`, `enth ref create` with CAS | mg-40d5e | placed |
| E8 | `.gitignore` semantics plus `.enth/info/exclude` | mg-2ef01 | placed |
| E9 | `enth status`, `change --trailer`, `--remote` write routes, `--request-id` replay | mg-2ef01, mg-6ac27, mg-10079 | in #9 as designed |

E1, E3, E5 and E6 are new asks from this design. pm-pogo carries them to pm-enth. E1 is the one the gate design depends on (§6.5.2), so it is first.

## 14. Open (stated, not resolved)

1. **Merge drivers for hot files** (epic §8 Q2, §3.2). `Cargo.lock` and append-only files such as enth's `tests/shell/PENDING` conflict under a line merge whenever two lands touch them. 28 of enth's 74 consecutive-land overlaps include `PENDING` (epic §3.2's run, enth's figure). enth may add named built-in strategies chosen by path, or nothing. pogo's side is the same either way: a `conflict` result is a merge failure routed to the worker, as a rebase conflict is today. How often that fires for pogo's own hot files (`cmd/pogo/main.go`, `cmd/pogod/main.go`, `docs/CONFIGURATION.md`, epic §3.2) is what mg-927f8's replay should report.
2. **How a followed repo moves to enth at its handover** (#11). This includes pogo's own repo, which is what would bring D1–D3, D5–D9, L1–L3 and O2 into scope. The questions are: is the handover a `pogo project set-vcs` that drains open MRs and live polecats first? What happens to unmerged `polecat-*` branches (land them through git before the handover, or import them as unlanded Changes with trailers)? Who runs the mirror's branch protection change? None of it is designed here.
3. **Per-worker enthd tokens** (#34). v1 uses the user's token (§4).

## 15. Coverage of the inventory

Every inventory entry, and where it is answered:

| Entries | Where |
|---|---|
| W1, P5, S2, S3 | §5 (cluster 1) |
| W2, W3, W4, W5, W6, W7, W9, C1, C3, C5, C6, S5, S10, X1, X2, X4, O1, O3, R19 | §4 |
| W8, P1 | §9 (cluster 4) |
| P2, P3, P4, P6, O5 | §12, §5, §4. O5: enth has no hooks, and the refinery's R12 check is the guarantee |
| R1, R2, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15, R16, R17, R20, R21, R22, R23, R24 | §6 (cluster 2), §7 |
| R3, R4, R18, L4 | §8 (cluster 3), §3 `Refs` |
| S1, S4, S6, S7, S8, S9, C2, C4 | §5 |
| D4 | §10 (cluster 5) |
| O4 | slice 9: `pogo doctor` checks `enth` on PATH when any project has `vcs = "enth"` |
| H1, H2, H3, H4, H5, H6 | §11 |
| X3, X5 | §3, outside the seam |
| D1, D2, D3, D5, D6, D7, D8, D9, L1, L2, L3, O2 | §1, out of scope until pogo's own repo moves (§14.2) |

## 16. Build plan

All slices wait for enth #9 to exit (mg-927f8). Slices 1–3 are pure refactors that do not need enth at all. They could start earlier if pm-pogo lifts the gate for them, which is pm-pogo's call and not assumed here. Each slice is one polecat, one MR, and it ships with tests.

| # | Slice | Proves it with | Depends |
|---|---|---|---|
| 1 | **`internal/vcs` + git backend: the refinery.** The interface of §3. A git `Lander`/`Refs` built from `internal/refinery`'s existing calls (`gitCmdOutput`, the pipeline in `attemptMerge`, `postmerge.go`). The refinery calls the backend. **No behaviour change.** | The existing refinery tests, unmodified and green, including the ones that run real git against a bare origin. The diff touches no `_test.go` assertions | — |
| 2 | **Git backend: workspaces.** `Workspaces` from `internal/agent/api.go`'s inline git (spawn, rollback, leftover, base resolution), `freshen`, the `.git/info/exclude` write. No behaviour change | the existing agent and freshen tests, unmodified | 1 |
| 3 | **Git backend: work.** `Work` from `strandedwork`, `gitgc`, `checkstranded`, `worktree-dirty`. The existing gate interfaces' production implementations call it. No behaviour change | the existing strandedwork and gitgc tests, unmodified | 1 |
| 4 | **Selection.** `vcs` and `enth` on the project record, `pogo project add --vcs`, `set-vcs` with its refusal, the `refinery.toml` warning, backend lookup by repo. enth selects a backend that answers `ErrUnsupported` everywhere | a golden round trip of today's `projects.json` (byte-identical); tests for the refusal and the warning | 1 |
| 5 | **enth workspaces.** `enth --remote checkout` whole-repo, the `workspace_paths` narrowing, `.enth/info/exclude`, `Dirty` via `enth status`, `Remove`, `Describe` (spawn record until E6), X1/X2 markers, O3, `Refresh`. pogod passes `ENTH_REMOTE` and the token | shell-level tests against a sandboxed enthd on an enth-native repo (epic item F): spawn, edit, dirty, remove | 2, 4; enth B, F, G |
| 6 | **enth work.** Trailer listing, stacks and the ambiguity refusal, the trailer-less base signal, `State` via land records (E3), S6–S9. `pogo refinery submit --item`, git's `--item` resolution, `pogo presubmit` | tests for the listing → stack cases (one, none, ambiguous, trailer-less), and git `presubmit` equal to today's shell | 3, 5; enth G + E3 |
| 7 | **enth land.** §6.5.1's pipeline: dry run, gate on `N` (E1, or on the `N = submitted` path first), `--expect-tip/--expect-new`, retries counted against `max_attempts`, request ids, crash recovery, the already-merged rule. `pr_mode` refused | sandboxed enthd: clean land, conflict, `stale_tip` re-run, `result_mismatch`, kill pogod mid-land then replay, a gated tree equal to the landed tree | 6; enth D, E, G + E1 |
| 8 | **Merge record.** The `enth` field, `mr.Landed()`, every consumer listed in §7, the deploy-hook env | tests that a git MR's JSON is unchanged, and that an enth MR has an empty `merged_sha` | 7 |
| 9 | **GitHub boundary and doctor.** PR-flow spawn refused, open-PR probe skipped, closing-ref gate without the PR body, O4 | unit tests per refusal and skip, plus a control showing the git path unchanged | 6 |
| 10 | **Revision stamp.** `build.sh`'s enth branch, `-buildvcs=false`, `version.VCS`, `verify_installed_stamps`, driftwatch/revcheck "not comparable". Re-run §10's table on a #9 checkout | §10's table as a script, with the git control. git build stamps unchanged | — (needs only enth B) |
| 11 | **Prompts.** `TemplateVars.VCS`, the blocks of §12 in the six polecat templates, `pogo presubmit` in place of `presubmit_check`, the spawn refusal for a template that does not branch | render every embedded template with `VCS=""` and diff it against today's output (byte-identical), and render with `VCS="enth"` | 6, 7 |
| 12 | **Tags and release refs.** `Refs.Tag` → `enth tag` with the idempotent retry, `Refs.Create` → `enth ref create`, `pogo refinery ref-create`, `--auto-create-target` | sandboxed enthd: a tag, a retry, a conflicting tag, a release branch landed onto | 7, 8; enth K (mg-40d5e) |
| 13 | **Batch land** (later; §6.5.5). Several MRs' stacks per `enth land`, the bisect on a red gate, per-batch states | its own design first | 7, and mg-927f8's numbers |

**Slice 7 was removed by mg-5b904** (§6): workers land their own enth work, and the refinery refuses it. Slice 13 (batch land) went with it: there is no queue left to batch.

The order follows the dependencies. Slices 1–3 de-risk the seam with no enth in the loop. Slice 4 makes selection real while the enth backend is still inert. Slices 5–7 are the enth path end to end. After 7, an enth-native repo can be worked by pogo, and the box-op for drellem2/enth (mg-2151d step 4) can be filed once that repo has been handed over (#11).
