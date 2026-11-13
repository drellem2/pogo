# VCS backends as plugins

**Status:** design / recommendation. Not implemented.
**Origin:** mg-69742 (Daniel, 2026-11-09: "investigate removing all git and
other tech specific coding from pogo and moving it to something like the
hashicorp plugin system").
**Author:** polecat-architect.
**Builds on:** the `internal/vcs` seam (`internal/vcs/vcs.go`, slices 1–3b:
mg-a18a3, mg-cc7ae, mg-d72cb, mg-afad2), and the plugin system that was deleted
in 858ae31e (mg-14647).

## TL;DR

**GO, conditionally, and not in the shape the question assumes.**

- The seam already exists, and its method set is enough. `internal/vcs`
  declares four question-shaped groups: Workspaces, Work, Lander and Refs.
  A downstream fork carries an alternate VCS backend that implements **the
  same method set, unchanged**.
- The fork is still a fork because of the code **above** the seam. In the
  fork, 19 non-test files outside `internal/vcs` import the alternate
  backend's package directly. Code outside the seam compares against the
  backend's `Kind` 32 times, execs the backend's CLI 25 times, and the
  polecat templates branch on the backend name 35 times. Those sites carry
  three things core has no generic name for yet: a different **landing
  workflow** (the worker lands its own change, with no merge queue), a
  different **worker protocol** in the prompts, and **per-backend worker
  environment and credentials**.
- So the transport is the small part of this design. The large part is
  giving those three things generic names in core. Until that happens, a
  plugin cannot carry the alternate backend, whatever the transport.
- **Transport:** an exec-a-binary protocol (`pogo-vcs-<kind> <verb>`, JSON
  in and out), discovered on `PATH` the way git finds `git-<cmd>`. Not
  HashiCorp go-plugin. Git stays a **built-in, in-process** backend; it is
  not moved out of the binary.
- **Phasing:** A, selection and registry, generic and git-only. B, generalise
  the above-seam capabilities, with the fork's 32/19/35 counts as the exit
  measure. C, the exec adapter plus a conformance suite that runs git through
  it. D, rebuild the alternate backend as a plugin binary and retire the
  fork. Estimate: **10–15 polecat slices.** This is a prior, not a
  measurement (§7).

---

## 1 · What is different from the plugin system we deleted

pogo had a HashiCorp go-plugin system (`pkg/plugin`, `internal/driver`),
deleted in 858ae31e. The commit message gives the reasons. `/plugin` was the
only way to reach any plugin. The one non-search plugin ran `go vet` on every
repo and nothing could read its output. Search itself had 0 requests in 3
weeks of logs (mg-14647). The interface was
`Info() / Execute(req string) string / ProcessProject(path)` over `net/rpc`:
stringly typed, and shaped around no particular question.

The deletion was a verdict on **a plugin system with no consumer**. It was not
a verdict on plugins. Three things are different now:

| Then | Now |
|---|---|
| No consumer beyond built-ins nobody used | A consumer in production: a fork that pogod is deployed from today |
| `Execute(string) string`: no contract | A typed, question-shaped interface with typed errors (`ErrStale`, `*ConflictError`, `*RefusedError`, `ErrUnsupported`) |
| Designed before any second implementation | Two implementations of the same method set already exist (git on main, the alternate backend on the fork) |

The risk that remains is the same one in a new form. The protocol would have
**one** out-of-tree consumer, owned by the same person, so it can overfit that
consumer. §5 answers this by making git the protocol's second consumer: the
conformance suite runs git through the exec adapter.

Housekeeping: the installed `com.pogo.daemon` plist still sets
`POGO_PLUGIN_PATH` (mg-e403c, blocked on a human). This design does not reuse
that variable. Backends are found on `PATH` (§4.2), so mg-e403c remains a pure
cleanup.

## 2 · Inventory: where core is VCS-specific

An inventory already exists: mg-49f3b, 84 entries in 10 areas. It lives on the
fork, not on main. This section does not redo it. It **sizes** what is left
on main after slices 1–3b, and it measures the fork's diff, which is the
quantity the migration has to drive to zero.

### 2.1 Main: git calls outside the seam

Population: non-test `.go` files under `internal/` and `cmd/` on main at
7b9e56dd, outside `internal/vcs/`. The counting instrument was
`grep -rnE '"git"'` plus calls to the package-local wrappers (`gitCmdOutput`,
`gitCmd`). The same grep finds 32 sites inside `internal/vcs`, which is the
positive control.

| Area | Literal `git` execs | Wrapper calls | In plugin scope? |
|---|---|---|---|
| `internal/refinery`: private clone, submit-branch resolution, recovery ancestry, gate-dirt classification, target existence | 13 | 23 (`gitCmdOutput`) | **Yes, but only for a queue-landing backend.** A self-landing backend bypasses the refinery (§3.2) |
| `internal/gitgc`: worktree sweep, durability, detached-HEAD check | 6 (+ an `exec.go` wrapper) | — | Yes; mostly through `Workspaces`/`Work` already |
| `internal/freshen` | 1 | — | Yes (`Workspaces.Refresh` has a git impl; one residual call) |
| `cmd/pogo` (`doctor` tool list) | 1 | — | Yes: a backend health check (§3.5) |
| `internal/ghpr` (origin URL) | 1 | — | No: GitHub is a forge, not a VCS (§2.3) |
| `internal/config` (`pogohomerepo.go`) | 1 | — | No: pogo's own home repo |
| `internal/selfdrift`, `driftwatch`, `staleness`, `homevcs`, `hermeticgit`, `service`, `ineffect`, `gitceiling` | 15 | 2 (`ineffect`) | **No.** These act on pogo's own repo or on this host, not on a managed repo |

Main also constructs the git backend by name (`gitvcs.*` / `vcs.Git`) at 39
non-test sites outside `internal/vcs`. Nothing selects a backend per repo yet;
the package comment marks that as "slice 4". That is the first missing piece.

The git **Lander** is the leakiest group in the seam. `vcs.Step` is git's
merge pipeline as an enum (`fetch`, `checkout-branch`, `rebase`,
`merge-ff-only`, `push`, …). `LandHooks.StepFailed` lets the refinery
reclassify a git step's failure in-process (`internal/refinery/land.go:52`).
That callback cannot cross a process boundary without bidirectional RPC. It is
one reason git stays in-process (§4).

### 2.2 The fork: what sits above the seam

Population: a tree diff between the fork's deployed HEAD and main's tree at
7b9e56dd (`diff -ruN`, excluding `.git`, `bin`, `.claude`). 274 files differ
or exist only on the fork: about 15.1k added non-test lines and 12.0k added
test lines.

**Dominance first, because it needs no denominator.** The fork does not change
the seam's interfaces at all. Every method in Workspaces, Work, Lander and
Refs has the same signature on both sides. All of the fork's reasons to exist
are above the seam:

| Measure (non-test, outside `internal/vcs`) | Count | Where |
|---|---|---|
| Files importing the alternate backend's package directly | **19** | `cmd/pogo` (5), `internal/agent` (5), `internal/gitgc` (4), `internal/refinery` (3), `internal/project` (2) |
| Comparisons against the alternate `Kind` | **32** across 21 files | refinery (9), `cmd/pogo` (8), `cmd/pogod` (3), agent (3), others |
| Direct execs of the alternate backend's CLI | **25** across 15 files | agent spawn/stranded/prompt, strandwatch, version, selfdrift, refinery |
| Template branch points on the backend name | **35** across 6 templates | `polecat-review` (14), `polecat` (10), `polecat-architect` (6), `polecat-qa` (3), `build-pr` (1), `triage` (1) |

By area, in fork-added lines including tests: `scripts/` 5.3k (deploy, sync,
runner ops), `internal/agent` 4.5k, `internal/vcs` 4.4k (the backend itself
plus selection types), `cmd/pogo` 2.1k, `internal/refinery` 1.9k,
`internal/gitgc` 1.3k, `internal/strandwatch` 1.1k, a gate binary 1.0k.
About 2.0k lines (`internal/vis`) are a fork-first feature unrelated to VCS.
They upstream normally and are not a plugin concern.

**Stationarity: no.** The fork grows with its own dispatch. Commits touching
the alternate backend, by day: 3 (11-02), 36 (11-03), 8, 12, 2, 5, 2 (11-08).
A plan scoped to a snapshot of the 32 sites would chase a moving target. So
Phase B's exit criterion is **the counts reaching zero, re-measured at the
time**, not "fix these 32 lines".

### 2.3 What stays outside any VCS plugin, by design

- **Forge (GitHub):** `gh`, PR flow, issue intake, closing-ref checks on PR
  bodies, `ghtoken`. These are forge operations. A forge seam is a separate
  question; this design does not open it.
- **pogo's own repo and this host:** self-deploy, selfdrift, driftwatch,
  revision stamps, homevcs, gitceiling, hermeticgit. These ask about the repo
  pogo was built from. When pogo's own source lives in another VCS, that is
  deployment configuration (§6.3), not a backend for managed repos.

## 3 · What core must be able to say generically

These are the capabilities the fork currently expresses by naming its backend.
Each needs a generic name in core before any transport can carry it.

### 3.1 Selection and per-backend configuration

The project record gains `vcs` (kind; empty means git) and an **opaque**
`vcs_config` object that core stores and passes back without interpreting it.
Endpoints, repo names and the *path* of a token file all go there. Today the
fork adds a typed struct for its backend to `internal/vcs` and to the project
record. That is the selection diff that has to disappear.

Lookup: `vcs.For(project) (Backend, error)`. An unknown kind is refused with
a named error. It never falls back to git.

### 3.2 Landing mode

```go
type LandMode int
const (
    LandByQueue LandMode = iota // the refinery lands (git today)
    LandBySelf                  // the worker lands; core verifies and records
)
```

A `LandBySelf` backend's `Lander.Prepare/Land` return `ErrUnsupported`. The
refinery refuses a submit and points at `pogo land`. `pogo land` calls a new
`Work.VerifyLanded(repo, item, evidence) (Landed, error)` and records the MR
**only on what the backend says landed**, never on the worker's word. Then it
fires the same OnMerged path a queue merge fires. The fork already has this
logic, under its backend's name. It becomes generic, and git answers
`LandByQueue`.

`Landed` gains `Kind` and `ID` beside `SHA`, so the merge record can name a
landed change that is not a git sha.

### 3.3 Worker environment

`Workspaces.WorkerEnv(repo, worker) ([]string, error)` returns the variables a
worker in this repo needs: an endpoint, a repo name, a credential read from
the token file **at use**. Core appends them to the spawn environment and
never logs their values. An unreadable credential refuses the spawn, naming
the path. On git this returns nil.

### 3.4 The worker protocol in prompts

This is the hardest of the four, and it carries a naming requirement of its
own. The polecat templates teach the worker how to save, check and hand off
work: commit, push, presubmit, submit or land, review checkout. Today the fork
writes the alternate backend's arm **inline in core's templates**.

Generic form: core's templates reference **named protocol blocks**, such as
`{{vcsblock "save"}}`, `{{vcsblock "handoff"}}`, `{{vcsblock "review_checkout"}}`
and `{{vcsblock "presubmit"}}`. Git's text is the built-in default, and a
render test keeps it byte-identical to today. A plugin backend supplies its own
blocks (`pogo-vcs-<kind> prompt-blocks --json`). Spawn is refused when the
backend lacks a block the resolved template references. This uses the same
refuse-before-side-effects rule the fork already applies to templates that do
not branch. The alternate backend's protocol text then lives in the plugin,
not in pogo's public templates.

Where a block is really a predicate (presubmit), it is a `pogo` verb that runs
through the seam (`pogo presubmit` already is). Block text should shrink
toward verbs over time.

### 3.5 Health check and cleanup

- `Backend.Check(ctx) []Problem`, for `pogo doctor`: the backend's tool is on
  PATH, it is reachable, and it is logged in.
- A backend's own build-output and cache layout, for gitgc's pruning phase.
  It reaches gitgc through `Workspaces.Describe`, which gains
  `PrunablePaths`. gitgc must not learn the backend's directory names.

## 4 · Transport

### 4.1 The options

| | In-process interface (built-in) | HashiCorp go-plugin (gRPC subprocess) | Exec a binary per call |
|---|---|---|---|
| Unix-utility fit | n/a (it's a library) | Poor: a long-lived RPC server pogod must supervise | **Good: one verb, JSON out, exit code, the way `git-<cmd>` works** |
| Callbacks (`LandHooks`) | Native | Possible through the broker; complex | Not possible as written; replaced by events and typed errors |
| Typed errors | Native | gRPC status details; per-type mapping | JSON `{"error":{"code":…,"paths":[…]}}` mapped back to the `vcs` error types |
| Process lifecycle | None | pogod supervises a child per backend, with restart, handshake and orphan risk. pogod already runs dozens of watchers that exist because supervision is hard | None: the process exits when the call ends |
| Per-call cost | Nil | One RPC | One fork/exec. pogo already has 70 `git` exec sites, and the seam's calls are coarse (spawn, land, sweep) |
| Versioning | Compile time | Handshake plus protobuf evolution | `pogo-vcs-<kind> capabilities --json` → `{protocol: 1, land_mode, blocks, …}` |
| Language | Go only | Any, with gRPC | Any |
| Genericity | The backend's code is inside pogo's binary | Separate binary | Separate binary |
| Track record **in pogo** | The seam (works) | Deleted for disuse (858ae31e) | `mg` is integrated this way already, under a named-clause contract (`docs/design/mg-contract.md`) |

### 4.2 Recommendation

- **Git: built-in, in-process.** It is the default and the reference
  implementation, and its Lander is coupled to the refinery through in-process
  hooks (§2.1) that are worth more than the purity of moving it out. Rewriting
  the most battle-tested path in pogo to prove a point is pure risk.
- **Other backends: exec protocol.** `pogo-vcs-<kind>` on PATH. One verb per
  seam method (`workspace-create`, `work-find`, `work-verify-landed`,
  `refs-tag`, …). Request JSON on stdin, response JSON on stdout,
  stable error codes, exit status 0, 1 (refused, typed) or 2+ (failed).
  An `internal/vcs/execvcs` adapter implements `vcs.Backend` on top of the
  protocol, so the rest of core cannot tell the difference.
- **Not go-plugin.** It would bring back the supervision burden pogod is worst
  at, and the one call that wants an RPC callback (`StepFailed`) belongs to
  git, which stays in-process anyway. The alternate backend already works by
  exec'ing its own CLI, so a subprocess-per-call protocol matches how it
  already runs.

**Fallback if exec proves too thin:** a *distribution build*. Public pogo
exposes `pkg/vcs` plus `pogod.Main(backends ...vcs.Factory)`, and a private
repo builds its own `pogod` with one blank import. That replaces the fork with
about 20 lines of `main.go`, but it means two binaries. Choose it only if
Phase C shows a seam method that cannot be expressed as one request and one
response.

## 5 · Proving the protocol is not shaped around one consumer

A conformance suite (`internal/vcs/vcstest`) runs the same scenarios against
any `vcs.Backend`: create a workspace, write, dirty, find, state, verify the
land, remove, tag, and the refusals. It runs:

1. against `gitvcs` directly, as the reference;
2. against **`execvcs` driving a `pogo-vcs-git` shim** that wraps `gitvcs`
   (the protocol's second consumer, and the control that the adapter loses
   nothing);
3. against any installed plugin, opt-in.

If (1) passes and (2) fails, the protocol is lossy, and that is the finding.
If a scenario cannot be written for (2), that method belongs to the
fallback (§4.2).

## 6 · Migration path

### 6.1 Today's git path

It is unchanged at every phase. Each slice keeps the existing "no behaviour
change" proof style: the existing tests stay unmodified, and template renders
stay byte-identical against a base revision.

### 6.2 The fork

| Phase | Fork diff afterwards |
|---|---|
| A: selection and registry upstream (generic `vcs` and `vcs_config`) | Loses its selection types and project-record changes |
| B: §3 capabilities upstream, with git implementations | Its 32 kind comparisons, 19 direct imports and 35 template branches go to 0. What remains is a backend package plus its CLI calls behind the seam |
| C: exec adapter and conformance suite | Unchanged; this phase prepares the landing spot |
| D: the backend becomes `pogo-vcs-<kind>` in its own repo | **Reduces to: nothing in pogo.** A plugin binary, its prompt blocks and its ops scripts live in the backend's repo. pogod is built from public main |

### 6.3 What is not part of the fork's code diff, but still has to move

- **Ops scripts (≈5.3k fork lines):** the deploy runner, the mirror sync and
  the CI-runner upgrade. They are about hosting pogo's *own* source somewhere
  else, not about managed repos. They move to the backend's or the operator's
  repo. Core needs at most a generic "deploy source is <command>" hook in
  `pogo-self-deploy`. Design that hook when Phase D reaches it; it is not
  assumed here.
- **Revision stamping when pogo's own source is not git:** `build.sh` and
  `internal/version` handle this on the fork today. The generic form is
  "`-buildvcs=false` plus an injected revision string". That is a small,
  backend-neutral upstream.

## 7 · Risks and effort

**Risks**

1. **Moving target (high).** The fork adds branch sites as it is dispatched
   (§2.2). Mitigation: from Phase A on, new alternate-backend work lands on
   main **as generic capabilities**, or not at all. A fork-only branch site
   added after Phase A counts as a regression against the exit measure.
2. **Genericity (high, procedural).** Every upstreamed name must be generic, and
   so must every test fixture and every changelog entry. Prompt-block text for
   the alternate backend must never be committed to this repo. Mitigation: the
   alternate backend's arms move into the plugin (§3.4), and names are checked
   before merge (with a positive control).
3. **Git regression (medium).** It is contained by keeping git in-process and
   by the byte-identity tests. The largest exposure is §3.4's template
   rewrite, which already has a proven test pattern on the fork.
4. **A lossy protocol (medium).** Contained by §5's git-through-exec control
   and the distribution-build fallback.
5. **Credential handling across a process boundary (medium).** Core passes
   the token *file path* in `vcs_config` and never the value. The plugin reads
   it. pogod never logs plugin stderr verbatim at info level.

**Effort (a prior, not a measurement).** The fork needed about 68 commits over
a week to build the backend and its wiring. Generalising is smaller work per
site but touches the same sites:

| Phase | Slices |
|---|---|
| A: selection and registry | 1–2 |
| B: land mode and VerifyLanded, WorkerEnv, prompt blocks (likely 2), doctor check, gitgc prunable paths, merge-record `Landed{Kind,ID}` | 5–7 |
| C: `execvcs`, the `pogo-vcs-git` shim, the conformance suite | 2–3 |
| D: port the backend to a plugin binary and retire the fork | 2–3 |
| **Total** | **10–15** |

## 8 · Recommendation

**GO on Phases A and B now.** They are worth doing even if no plugin ever
ships: they turn backend-named special cases into generic capabilities, and
they shrink the fork every day the fork exists. **GO on C and D, gated on B's
exit measure** (the fork's above-seam counts at 0, re-measured). **NO-GO on
HashiCorp go-plugin**, and no-go on moving git out of the binary.

## 9 · Open questions (not decided here)

1. Is a **runtime plugin** actually required, or is the real goal "no fork"?
   The distribution build (§4.2) also removes the fork, and is cheaper. Only
   exec gives "the same pogo binary for everyone". This is Daniel's call.
2. Does the alternate backend's CLI implement the `pogo-vcs-<kind>` protocol
   itself, or does a thin adapter binary wrap it? Fewer moving parts, against
   keeping pogo's vocabulary out of the backend.
3. A forge seam (GitHub vs. other forges) is the obvious next question. It is
   deliberately not opened here.
4. The Lander's git step enum (`vcs.Step`) is git vocabulary in the public
   seam. Leave it, since git is in-process, or move it into `gitvcs`? This is
   cosmetic until a second queue-landing backend exists.
