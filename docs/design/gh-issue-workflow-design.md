# GH-Issue Response Workflow — Mechanism Design

Status: APPROVED (pm-pogo review 2026-07-05) with Daniel corrections folded in
Author: architect
Date: 2026-07-05 (rev 2: pm-pogo additions + Daniel branch-field/scope corrections)
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
  `build.sh`/`test.sh`) → `merge --ff-only` → direct `git push origin
  <target>`. The target is the work item's `branch` field when set, default
  branch otherwise (Daniel correction 2026-07-05 — the refinery is *not*
  main-only). No PR support anywhere; retries on races; mails author +
  coordinator on failure; escalates after 3 consecutive failures per author.
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

- **Protection**: a GitHub **ruleset** on `main` in **drellem2/pogo and
  drellem2/macguffin** (Daniel scope decision 2026-07-05: the intensive
  workflow targets those two repos; the poller's repo list stays broader):
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
- **External-fork PRs** (first hit: pogo#43 / PR #44, 2026-07-05): the
  refinery-path default above assumes *our* PRs, where closed-not-merged is
  cosmetic. For an external contributor, "closed" reads as rejected and costs
  them the merged-PR credit — the exact optics this workflow exists to fix.
  Until pr_mode (phase 2) lands, apply this decision rule: if the PR is
  current with main (`gh pr view --json mergeStateStatus` not BEHIND), use
  the refinery path — the ff-merge lands the exact head SHAs and GitHub marks
  the PR merged on its own. If it is behind, merge with `gh pr merge --rebase
  --admin` (the documented fallback): the review polecat's QA lens already
  built and tested the head, which acceptably approximates the gates for the
  one-off. Rebase preserves author fields either way, so git-log attribution
  survives the refinery path too; only the PR badge is at stake. Once pr_mode
  lands this rule retires.

### Phasing

- **Phase 1 (no refinery code change)**: everything above works today. Known
  cosmetic wart: the refinery rebases before merging, so the PR's head SHAs
  never land verbatim on main and GitHub shows the PR "closed" (when gitgc
  deletes the branch) rather than "merged". Review history is preserved either
  way.
- **Phase 2 (small refinery change, makes PRs read "merged")** — **SHIPPED
  (mg-b828, 2026-07-05)**: `attemptMerge` has a PR-aware mode — when an open
  PR exists for the branch (`gh pr view --json state,number`), after rebase +
  gates, it `git push --force-with-lease`es the rebased branch back to origin
  *before* the ff-merge push. GitHub then marks the PR merged when the tip
  lands on main. Optional per-repo config: `pr_mode = true` in
  `.pogo/refinery.toml` (top-level or under `[gates]`). Fail-soft throughout:
  a failed `gh` lookup or push-back logs and falls through to the normal
  path (PR reads closed — the phase-1 status quo). See
  `internal/refinery/merge.go` (`pushBackForPR`) and ARCHITECTURE.md
  "PR mode". The §3 external-fork decision rule above retires once the
  target repos set `pr_mode = true`.

  Phase 2 is scheduled **immediately after the first end-to-end proof**, not
  loosely "later": pm-pogo's review is right that for the company-adoption
  audience a wall of "closed" PRs is storefront optics, not cosmetics.

  Alternatives weighed for phase 2 (Daniel prompt, 2026-07-05):
  - *Refinery-merge to a staging branch the PR targets* (using the existing
    work-item `branch` field): **doesn't avoid the push-back.** GitHub marks
    a PR merged only when the head branch's *tip* becomes reachable from the
    base, and the refinery rebases before merging, so the SHAs change
    regardless of base branch. Same dance, plus a staging→main promotion
    step. Declined.
  - *`gh pr merge --rebase --admin` after gates pass*: genuinely simpler (one
    call, real merged event, admin bypasses protection), but GitHub re-does
    the rebase server-side, so what merges is not byte-for-byte what the
    gates tested when the base has moved. Kept as fallback; force-with-lease
    remains the recommendation because it merges exactly the tested SHAs.

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

Two product requirements folded in from pm-pogo's review (2026-07-05):

- **Reporter-visible response is the product goal.** The triage polecat posts
  a brief professional ack comment on the GH issue when triage starts, and a
  substantive public reply lands after the Daniel gate — the plan on go, an
  honest reasoned close on no-go. Wording/tone standards are pm-pogo's
  (UNIX-voice, no AI slop) and ship in the template ticket bodies.
- **Daniel-gate semantics: silence = HOLD.** No timeout-defaults-to-go, ever,
  on external-facing work. One re-ping at 48h is acceptable; beyond that the
  ticket stays gated.

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

## 7. Build tickets (filed under design ticket mg-01e9)

1. **mg-f7a3 — Ruleset setup** — enable the §3 ruleset on drellem2/pogo and
   drellem2/macguffin (scope per Daniel 2026-07-05; pogo-reminders dropped).
   **Done 2026-07-05**: ruleset `main-require-pr` active on both repos (pogo
   id 18534732, macguffin id 18534735); admin direct-push bypass and
   `gh pr create` verified. Ops notes: `docs/operations.md`.
2. **mg-be91 — `polecat-triage.md` template** — investigate + recommend, no
   code; pm-pogo supplies the quality bar / recommendation format.
3. **mg-546c — `polecat-review.md` template** — §6. pm-pogo reviews prompt
   content.
4. **mg-9675 — `polecat-build-pr.md` template** — the issue-track build
   variant of `polecat.md`: after branch push, `gh pr create` instead of
   `pogo refinery submit`; stays alive for the §6 loop; never self-submits —
   the coordinator submits on pass. Separate template rather than a
   conditional in `polecat.md`: two audiences, two protocols.
5. **mg-841a — Mayor playbook section** — §5 state machine + Daniel gate
   summary format (pm-pogo owns summary content standards).
6. **mg-0606 — Poller routing tweak** — `[gh]` mail body text pointing mayor
   at the playbook; repo list env when company repos land.
7. **mg-b828 — Phase 2 refinery PR-mode** — §3 phasing; lands immediately
   after the flow is proven end-to-end on one real issue.

Tickets 2–6 are polecat-executable and independent; 1 is unblocked (scope
answered); 7 waits only for the first end-to-end proof.

## 8. Open questions (Daniel)

- ~~Scope of branch protection~~ **Answered 2026-07-05**: the intensive
  workflow + rulesets target **pogo and macguffin** only; poller repo list
  stays broader. (Whether internal work also opens PRs remains open but
  blocks nothing — §3 works either way.)
- Will Daniel comment on PRs directly (→ build `poll-gh-prs.sh`) or gate via
  issue comments / existing channels only?
- Appetite for a second GitHub identity (bot account or GitHub App) to make
  approvals count and unlock "required approving reviews". Architect + pm-pogo
  joint recommendation: **yes eventually** — required for the company-org
  story — but not needed to ship this workflow.
