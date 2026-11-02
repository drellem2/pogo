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

## 5. Identity: trailers name a worker's work (decision 4)

Every Change a worker produces carries two trailers:

```
Pogo-Item: mg-X
Pogo-Worker: <agent name>
```

They are written with `enth change DIR -m "…" --trailer Pogo-Item=mg-X --trailer Pogo-Worker=<name>` (epic §2). This replaces the `polecat-<name>` branch (W1) as the durable tie between a worker and its output.

**Finding the work (S2, S3, P5).** `enth changes --unlanded --trailer Pogo-Item=mg-X [--trailer Pogo-Worker=<name>]` (mg-10079, pogo's ask #2) lists the item's unlanded Changes as JSON lines: id, author, trailers, base and created time. That one query answers stranded-work's candidate set (S2), its match of work to item (S3, which today parses `(mg-xxxx)` out of commit subjects), review's "which Change do I check out", and submit's "what does this MR carry".

**What a submission is.** A `Submission` is an ordered stack: the listed Changes sorted so that each one's base is the previous one, ending at a top Change. The listing is refused as ambiguous when the Changes form more than one stack. That happens when two workers on the same item both produced work, for example a respawn that did not adopt. The refusal names each stack's worker, so the mayor chooses rather than pogo guessing. Filtering by `Pogo-Worker` too is the normal case and makes the ambiguity rare.

**Submit (P5, R1).** `pogo refinery submit` takes the work item as the identity of the work on an enth repo:

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

## 6. The refinery: gate exactly what lands (decision 5)

### 6.1 The pipeline for one MR

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

### 6.2 The prerequisite: a dry run's result must be checkable-out

**This is the one place the decisions rest on an enth behaviour that the enth items do not yet promise.** mg-b1b9e says `--dry-run` "returns the landed ids, writes nothing". If nothing is written, `enth checkout N` has no `N` to read whenever `N ≠ submitted`, which is every land whose base is not the tip. That is the common case at concurrency: epic §3.2 measured 53% of pogo's lands sharing a path with one of the previous three. The gate step needs the rebased Change records and any merged blobs to be **stored**, though not landed: no `L` record and no CAS. Content-addressed records that no ref reaches are harmless, and they are what a later real land would write anyway.

**Requirement E1 (§13):** `land --dry-run` stores the result Changes and merged blobs (or a `--store-result` flag does), so that `enth checkout N` works. The ask goes to pm-enth through pm-pogo, against mg-b1b9e.

**If enth declines E1,** the only exact fallback is to gate only lands where `N = submitted`. When the submitted base is not `T`, the refinery fails the MR as `stale_base` with the remedy "check out the target again and re-make the Change". That gives up the hunk merge (epic §3.2) for pogo's lands and pushes the rebase back onto the worker. It is named here so it can be rejected on purpose, not reached by default. Gating the submitted tree on its own base and trusting the merge is **not** a fallback: it is what decision 5 exists to rule out.

When `N = submitted` (the base is the tip), the submitted Change is already stored and the gate checks it out directly. This works with no enth change at all, and slice 6 can be tested on that path first.

### 6.3 Crash recovery and already-merged (R9)

The request id `<MR id>/<attempt>` makes a land idempotent across a pogod restart (I12). `recovery.go`'s in-flight path re-issues the same land with the same request id. If it had landed, enthd replays the recorded answer and pogo records it. If it had not, it lands now or is refused `stale_tip`. A new attempt after `stale_tip` takes a new request id, because its `T` and `N` differ.

An already-merged resubmit (gh #34, `emptybranch.go`) keeps mg-c184d's rule: it resolves as `already_merged` only when the refinery's own history holds a merged MR for the same item whose `enth` record (§7) lists every submitted id. An item with no unlanded Changes and no such record fails as a defect: it delivers nothing.

### 6.4 Gate reuse (R14)

git reuses a passed gate when a retry produces the same tree id (`merge.go:610,716`). enth has no tree id (epic §8 Q1). The reuse key is `N`, which identifies the result only from the same tip. So after a `stale_tip` the gate re-runs even when the tree is the same. This is a throughput cost, inferred and not measured. It is accepted for v1 and goes away if enth adds a tree id. mg-927f8's replay is the place to measure how often it fires.

### 6.5 One Change stack per MR in v1; batch land later

v1 lands **one MR's stack per `enth land` call**: the Changes of one item, in order. That keeps parity with git, where one MR is one branch. It also means the refinery's lanes, attempt budget, failure routing and verdicts all keep their one-MR shape.

**Batch land is a later slice (slice 13).** It would land several MRs' stacks as one trunk entry. What it would buy, from epic §6:
- **Merge-queue throughput.** One dry-run conflict pass, **one gate run**, one store write, one log commit and one CAS cover n MRs. The gate is the expensive part: pogo's gate is `./build.sh`, measured in minutes, against enth's ~2 durable steps per land.
- **Fewer gate re-runs.** `stale_tip` races are between lands. Landing n at once means one race window instead of n.
- **Atomicity across MRs** when two items must land together, which git cannot offer.
- One mirror push and one CI trigger per batch (#11, M3).

What it costs is the reason it is not v1: a red gate on a batch has to be bisected to find the failing MR, and the refinery's per-MR states become per-batch states. That is a design of its own.

### 6.6 What the refinery no longer needs on enth

No private clone for landing (R5: a gate dir is still needed and is a fresh checkout per attempt), no fetch (R7), no credential helper (R24, replaced by the enthd token), no remote-URL lookup for failure records beyond the enthd endpoint (R22), no committer identity (R23: `land` keeps author and committer and records the lander in the `L` record), and no `refinery prune` (R21). `refinery.toml` keeps every key and its meaning, except `pr_mode`, which is refused on an enth repo (§11).

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
- `internal/refinery/emptybranch.go:65-82` (the prior-merge check, §6.3),
- `internal/refinery/events.go:186,205` (the event carries `landed: {vcs, id}` alongside the existing `merged_sha`, which stays as it is for git),
- `internal/refinery/postmerge.go` (§8),
- `cmd/pogod/refinerymerged.go:92`, `mergedopen.go`, `reopenguard.go:169-231` (the merged-but-open and reopen guards, which print the id with its kind).

The deploy hook gets `POGO_LANDED_VCS` and `POGO_LANDED_ID` in its environment. Any variable that carries a git sha today stays unset for an enth MR rather than holding a Change id.

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

## 11. GitHub stays GitHub (decision 10)

Unchanged for every repo: issue intake and carriers (H3), credentials (H5), `homevcs` (H4, X5), Releases, Actions and `install.sh` (H6).

**An enth repo has no PR flow (H1, H2).**
- `pr_mode = true` in an enth repo's `refinery.toml` is refused at config load, with the reason. That beats a push-back to a branch that does not exist.
- Spawning a `--branch` PR-flow polecat (`polecat-build-pr.md`; MR `PRFlow`, `DeferDone`) on an enth repo is refused at spawn.
- The closing-ref gate (R12) still checks Change messages for GitHub closing keywords. A message lands on the mirror verbatim, so `Fixes #N` still closes the issue there. There is no PR body to read, so `closingref_gate.go:169-172`'s `gh pr view` step is skipped.
- The open-PR probe (`strandedwork.PRProbe`, `GitHubOpenPR`, H2) is skipped for an enth repo. Its answer is replaced by "the item has an open MR in the refinery". There is no other place review can be waiting.

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

## 13. What enth must provide

| # | What | enth item | State on 2026-11-02 |
|---|---|---|---|
| E1 | `land --dry-run` stores its result so `enth checkout N` works (§6.2) | ask against mg-b1b9e | **not promised**: mg-b1b9e says the dry run "writes nothing" |
| E2 | `land --expect-new N` → `result_mismatch` | mg-b1b9e | placed (19:20Z section) |
| E3 | the landed complement of the listing: `--landed`, or a `landed` field (§5) | ask against mg-10079 | **not promised** |
| E4 | `enth changes --unlanded --trailer K=V` through enthd | mg-10079 | placed |
| E5 | exported git sha for a Change id (§11) | ask against #10 | **not promised** |
| E6 | `.enth/checkout` records the remote and repo (§4) | ask against mg-2ef01 | **not promised**; pogod's spawn record covers it meanwhile |
| E7 | `enth tag`, `enth ref create` with CAS | mg-40d5e | placed |
| E8 | `.gitignore` semantics plus `.enth/info/exclude` | mg-2ef01 | placed |
| E9 | `enth status`, `change --trailer`, `--remote` write routes, `--request-id` replay | mg-2ef01, mg-6ac27, mg-10079 | in #9 as designed |

E1, E3, E5 and E6 are new asks from this design. pm-pogo carries them to pm-enth. E1 is the one the gate design depends on (§6.2), so it is first.

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
| 7 | **enth land.** §6.1's pipeline: dry run, gate on `N` (E1, or on the `N = submitted` path first), `--expect-tip/--expect-new`, retries counted against `max_attempts`, request ids, crash recovery, the already-merged rule. `pr_mode` refused | sandboxed enthd: clean land, conflict, `stale_tip` re-run, `result_mismatch`, kill pogod mid-land then replay, a gated tree equal to the landed tree | 6; enth D, E, G + E1 |
| 8 | **Merge record.** The `enth` field, `mr.Landed()`, every consumer listed in §7, the deploy-hook env | tests that a git MR's JSON is unchanged, and that an enth MR has an empty `merged_sha` | 7 |
| 9 | **GitHub boundary and doctor.** PR-flow spawn refused, open-PR probe skipped, closing-ref gate without the PR body, O4 | unit tests per refusal and skip, plus a control showing the git path unchanged | 6 |
| 10 | **Revision stamp.** `build.sh`'s enth branch, `-buildvcs=false`, `version.VCS`, `verify_installed_stamps`, driftwatch/revcheck "not comparable". Re-run §10's table on a #9 checkout | §10's table as a script, with the git control. git build stamps unchanged | — (needs only enth B) |
| 11 | **Prompts.** `TemplateVars.VCS`, the blocks of §12 in the six polecat templates, `pogo presubmit` in place of `presubmit_check`, the spawn refusal for a template that does not branch | render every embedded template with `VCS=""` and diff it against today's output (byte-identical), and render with `VCS="enth"` | 6, 7 |
| 12 | **Tags and release refs.** `Refs.Tag` → `enth tag` with the idempotent retry, `Refs.Create` → `enth ref create`, `pogo refinery ref-create`, `--auto-create-target` | sandboxed enthd: a tag, a retry, a conflicting tag, a release branch landed onto | 7, 8; enth K (mg-40d5e) |
| 13 | **Batch land** (later; §6.5). Several MRs' stacks per `enth land`, the bisect on a red gate, per-batch states | its own design first | 7, and mg-927f8's numbers |

The order follows the dependencies. Slices 1–3 de-risk the seam with no enth in the loop. Slice 4 makes selection real while the enth backend is still inert. Slices 5–7 are the enth path end to end. After 7, an enth-native repo can be worked by pogo, and the box-op for drellem2/enth (mg-2151d step 4) can be filed once that repo has been handed over (#11).
