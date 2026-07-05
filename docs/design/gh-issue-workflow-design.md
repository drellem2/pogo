# GH-Issue Response Workflow — Mechanism Design

Status: DRAFT for review (pm-pogo, mayor, Daniel)
Author: architect
Date: 2026-07-05
Context: Daniel directive 2026-07-05 (company-adoption pivot). Product spec and
triage-quality bar are owned by pm-pogo; this doc covers mechanism only.

## 1. The workflow being built

1. GH issue arrives → triage polecat investigates → recommendation formed in
   concert with pm-pogo → mayor sends Daniel a triage + recommendation summary.
2. Daniel replies go / no-go / other.
3. On go: polecat builds on a branch and opens a **PR** (branch protection to
   be enabled on main).
4. A **separate review polecat** reviews the PR — QA, architecture,
   design-faithfulness — commenting on the PR.
5. Modify ↔ review loop until satisfactory; then merge.

## 2. Current-state facts that constrain the design

- **Refinery** (`internal/refinery/`): daemon merge queue. Per branch: fetch →
  rebase onto target → quality gates (`.pogo/refinery.toml` or
  `build.sh`/`test.sh`) → `merge --ff-only` → `git push origin main` directly.
  No PR support anywhere; retries on races; mails author + coordinator on
  failure; escalates after 3 consecutive failures per author.
- **Single GitHub identity.** Every actor on this machine — polecats, crew,
  the refinery — authenticates as the same GitHub user (ambient credentials of
  `pogod`; the refinery's `refinery@pogo.local` identity is committer metadata,
  not a credential). Consequences:
  - GitHub rejects reviews (approve *or* request-changes) on your own PR, so a
    review polecat **cannot file a counting PR review** on a PR the build
    polecat opened. Plain PR comments are allowed.
  - "Require approving reviews" in branch protection is unsatisfiable today.
- **Issue poller already exists and is running**: `poll-gh-issues.sh` in the
  standalone `drellem2/pogo-reminders` repo (unix-utility principle already
  satisfied). launchd (`com.pogo.gh-issues`), 60s interval, polls
  drellem2/{pogo,macguffin,pogo-reminders} via `gh issue list`, dedups on
  `updatedAt` per issue in `~/.pogo/gh-issues/seen-<owner>-<repo>.json`, and
  mails **mayor** with pull-verify-consume delivery. It does not create
  tickets; the recipient does.
- **No workflow primitive exists, by design.** Multi-step processes are prose
  playbooks in prompt markdown (mayor coordination loop, PM sweeps). The
  existing declarative hooks are: prompt frontmatter (`auto_start`, …),
  work-item frontmatter (the `qa: required|auto|manual` field that drives
  mayor's QA dispatch), `extends <template> with config <toml>` (PM tier), and
  drop-in overlays (`dropins/<prompt>/*.md`, append-composed).
- **Polecat templates** are Go `text/template` files selected at spawn
  (`spawn-polecat --template=X`); `polecat-qa.md` already exists as a
  verification-only variant: own worktree, checks out the source branch,
  verdict via `mg done --result='{"verdict":...}'`, coordinator-mediated
  follow-up loop.

## 3. Q1 — Branch protection × refinery

**Recommendation: keep the refinery as the single merge executor for all
work. The PR is a review/visibility wrapper on the issue track, not a second
merge path.** Two-track in *ceremony*, one-track in *integration*.

Rejected alternatives:
- *Refinery converts wholesale to PR-merge executor*: forces PR ceremony onto
  high-volume internal mg work for no reviewer benefit (nobody reviews those
  PRs), and adds a `gh`/network dependency to every merge.
- *Two independent merge paths* (refinery for internal, `gh pr merge` by some
  agent for issue work): two integration codepaths drift; the second path
  would lack the refinery's rebase/gate/retry/failure-escalation machinery
  exactly on the work Daniel cares most about.

### Mechanism

- **Protection**: a GitHub **ruleset** on `main` in each participating repo:
  require a pull request before merging; block force pushes; **bypass actor:
  repository admin** (today that is the one identity everything uses; in a
  company/org deployment the bypass actor becomes a dedicated refinery GitHub
  App — the design is identical, only the actor changes). Do **not** enable
  "required approving reviews" yet — unsatisfiable with one identity (§2).
- **Internal track (mg tickets)**: unchanged. Polecat pushes `polecat-<id>`,
  `pogo refinery submit`, refinery rebases/gates/ff-pushes main under bypass.
- **Issue track**: build polecat pushes its branch, then `gh pr create` with
  the triage recommendation linked, review loop runs on the PR (§6), and when
  the loop terminates the coordinator submits to the refinery exactly as
  today. Gates still run; the refinery still does the merge.

### Phasing

- **Phase 1 (no refinery code change)**: everything above works today. Known
  cosmetic wart: the refinery rebases before merging, so the PR's head SHAs
  never land verbatim on main and GitHub shows the PR "closed" (when gitgc
  deletes the branch) rather than "merged". Review history is preserved either
  way.
- **Phase 2 (small refinery change, makes PRs read "merged")**: teach
  `attemptMerge` a PR-aware mode — when an open PR exists for the branch
  (`gh pr view --json`), after rebase + gates, `git push --force-with-lease`
  the rebased branch back to origin *before* the ff-merge push. GitHub then
  marks the PR merged when the tip lands on main. This is ~30 lines in
  `internal/refinery/merge.go` plus a `gh` lookup; keep it optional per-repo
  config (`[gates]`-style key in `.pogo/refinery.toml`, e.g. `pr_mode = true`).

## 4. Q2 — Issue poller

**Recommendation: adopt the existing `poll-gh-issues.sh` as-is in shape;
change only routing content and repo list. Do not rebuild it, do not move it
into pogod.** It already embodies the skeleton pm-pogo described (poll →
seen.json → mail), lives in its own repo per the unix-utility principle, has
verified delivery, and is running at a 60s interval — which already meets
"respond quickly".

Changes:
1. **Mail body, not mechanism**: the poller keeps mailing mayor; the *mayor
   prompt* gains the playbook "on `[gh]` mail: file the triage ticket and
   request triage-polecat dispatch" (§5). Keep the poller a dumb transport —
   orchestration belongs in prompts, and every alternative (poller runs
   `mg new` itself, poller talks to pogod) couples the standalone utility to
   pogo internals.
2. **Repos**: stays env-driven (`REPOS`); extend when company repos arrive.
3. **Interval**: keep 60s. No change needed.
4. **Coverage note**: `updatedAt` bumps on issue comments too, so Daniel
   replying *on the issue* re-alerts mayor — the Daniel-gate (step 2) can run
   over GitHub issue comments with no new code. **Gap**: PR review comments
   are *not* polled. The modify↔review loop doesn't need them (it's local
   mail, §6), but if Daniel comments directly on PRs we will want a sibling
   `poll-gh-prs.sh` (same skeleton, `gh pr list` + `updatedAt` dedup). Defer
   until Daniel says he'll work that way.

## 5. Q3 — Workflow encoding in prompts/config

**Recommendation: no new primitive. Encode the workflow with the three
mechanisms that already exist** — this matches "pogo deliberately avoids
framework abstractions" (docs/design/declarative-orchestration.md) and the
`qa:` precedent:

1. **Work-item frontmatter as the state carrier**: issue-track tickets carry
   `workflow: gh-issue` plus a `stage:` field
   (`triage → gated → build → review → merge`), and the GH issue ref
   (`gh: <owner>/<repo>#<n>`). `depends=` chains triage → build → review
   tickets, mirroring how `qa: required` pairs items today.
2. **Polecat templates as the step encoding**: `polecat-triage.md` and
   `polecat-review.md` (§6) join `polecat.md`/`polecat-qa.md`. The template
   *is* the machine-readable definition of a workflow step; `spawn-polecat
   --template` is the dispatch primitive. No new spawn machinery.
3. **Prose playbooks in the coordinator prompts**: mayor gets the 5-step
   state machine as a numbered playbook (keyed off the frontmatter above);
   pm-pogo's template gets the triage-consult section (pm-pogo owns that
   content).

**Growth path, when a second workflow shows up**: package a workflow as a
**drop-in pack** — a directory of `dropins/mayor/gh-issue.md`,
`dropins/polecat-triage/…`, etc. plus templates — materialized by `pogo
install`. Drop-ins already append-compose, so this needs zero new code to
prototype by hand. A `[workflows]` TOML section that templates render is a
real new primitive; don't build it for a sample size of one.

## 6. Q4 — Reviewer polecat

**Recommendation: new template `polecat-review.md`, sibling of
`polecat-qa.md` (keep qa as-is for internal work).** Shape:

- **Inputs** (via existing `TemplateVars`, no new vars needed): `.Id` = review
  ticket; `.Body` carries the PR number, source ticket id, and pointers to the
  approved triage recommendation / spec (pm-pogo's artifacts). `.Repo` /
  worktree as today.
- **Behavior**: own worktree (qa pattern — never shares the builder's branch
  checkout); fetch and check out the PR branch; three review lenses, in order:
  1. **QA** — build + tests actually run (reuse polecat-qa's protocol);
  2. **Architecture** — consistency with docs/design/* and codebase
     conventions;
  3. **Design-faithfulness** — diff against the *approved* triage
     recommendation; flag scope creep and silent omissions.
- **Output, dual-channel**: findings posted to the PR as a structured
  **comment** (`gh pr comment`: verdict header + findings list + file:line
  refs) for human visibility — *not* `gh pr review`, which GitHub rejects
  same-identity (§2). Verdict of record goes to the control plane as today:
  `mg done --result='{"verdict":"pass|fail", ...}'` + mail. GitHub is the
  window; mg is the state.

### Modify ↔ review loop protocol and termination

- Builder and reviewer polecats both **stay alive** across rounds (polecats
  already never self-exit); reuse beats respawn — both keep context.
- **Direct mail for findings, coordinator for verdicts**: reviewer mails the
  builder its findings directly (cuts a relay hop) and mails mayor a one-line
  round status. Builder fixes, pushes, comments on the PR, mails the reviewer
  back. All *verdict transitions* (pass, fail-final, escalation) go to mayor,
  who owns the Daniel touchpoint. Mayor stays the hub for decisions, not for
  every packet.
- **Termination, exactly three exits**:
  1. **Pass** (including pass-with-nits: advisory findings the reviewer
     explicitly marks non-blocking) → mayor submits the branch to the
     refinery; normal reap follows.
  2. **Round cap: 3 modify↔review rounds** without pass → reviewer stops
     re-reviewing and mails mayor the open findings; mayor escalates to
     Daniel with the summary. (Matches the refinery's FailureThreshold=3
     precedent; prevents unbounded burn.)
  3. **Coordinator abort** (Daniel no-go mid-flight, superseded issue) →
     mayor stops both polecats; gitgc reaps branch/worktree as usual.

## 7. Proposed build tickets (for mayor to file)

1. **Ruleset setup** — enable the §3 ruleset on drellem2/{pogo,macguffin,
   pogo-reminders}. Gated on Daniel's scope answer; trivial to execute
   (admin `gh api` call or UI).
2. **`polecat-triage.md` template** — investigate + recommend, no code;
   pm-pogo supplies the quality bar / recommendation format.
3. **`polecat-review.md` template** — §6. pm-pogo reviews prompt content.
4. **Mayor playbook section** — §5 state machine + Daniel gate summary
   format (pm-pogo owns summary content standards).
5. **Poller routing tweak** — `[gh]` mail body text pointing mayor at the
   playbook; repo list env when company repos land.
6. **Phase 2 refinery PR-mode** — §3 phasing; do after the flow is proven
   end-to-end on a real issue.

Tickets 2–5 are polecat-executable and independent; 1 is Daniel-gated; 6 is
deliberately last.

## 8. Open questions (Daniel)

- Scope of branch protection: all merges wrapped in PRs eventually, or
  issue-track only? (pm-pogo already asked; §3 works for either answer —
  it only changes whether internal work *also* opens PRs, not who merges.)
- Will Daniel comment on PRs directly (→ build `poll-gh-prs.sh`) or gate via
  issue comments / existing channels only?
- Appetite for a second GitHub identity (bot account or GitHub App) to make
  approvals count and unlock "required approving reviews" — needed for the
  company-org story eventually; not needed to ship this workflow.
