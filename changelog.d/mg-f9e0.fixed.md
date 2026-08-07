- **The shipped build-PR template told every builder to write `Resolves #N` in
  the one artifact the closing-ref guard could not see. The default is now
  `Refs`, and the guard reads pull request bodies (mg-f9e0).** Two separable
  defects, fixed together because either one alone leaves the fleet closing
  strangers' issues by default.

  **The shape.** `internal/agent/prompts/templates/polecat-build-pr.md`
  prescribed `Resolves <owner>/<repo>#<n>` in the `gh pr create` body.
  `closingref.Check` — the control that exists to stop a merge from shutting an
  issue — was called on **commit messages only**. A PR body carrying a closing
  keyword auto-closes the issue when the PR merges, and the gate never looked.
  The default path and the guard were pointed at different artifacts, so the
  guard could not fail on the path everybody takes. A check that cannot fail on
  the default path is not a guard; it is a check that passes.

  **Why `Refs` is the default now, and not a style preference.** GitHub closes
  the WHOLE issue on a closing keyword — there is no way to close part of one.
  So `Resolves` is correct only when a PR discharges an issue entirely, and on
  the gh-issue track, splitting an issue into a landed part and a deliberately
  deferred part is routine: `drellem2/pogo#111` was split exactly that way, with
  the config key carved out and the reporter told in the thread that it is
  tracked separately. That one was safe only because a human reviewed the
  auto-close and released it. The fleet had already been bitten by this shape
  once: a body reading `Resolves #N (item 1)` closed the whole issue, because
  the parenthetical scopes nothing. The recorded lesson was "multi-item = Refs
  #N, close by hand" and the template still said `Resolves`. `Refs` costs one
  manual close on the days the PR really did discharge the issue; `Resolves`
  costs a reopen and an explanation to a stranger on the days it did not. The
  asymmetry is not close.

  Deliberate closure is still available — as a choice that gets recorded rather
  than a default that gets inherited. Write the keyword *and* acknowledge it per
  reference in the same body:

      Resolves drellem2/pogo#111

      Closing-ref-ack: drellem2/pogo#111 — intentional; this PR discharges the issue in full

  **The guard now reads both artifacts.** `checkClosingRefs` splits into
  `commitClosingRefs` (unchanged behaviour, over the branch's own commits) and
  `prBodyClosingRefs`, which asks `gh pr view <branch> --json number,body` and
  runs the same predicate over the description. `closingref.Report` takes an
  `Artifact` and the remedy follows it: a commit says amend and re-push, a PR
  body says `gh pr edit <number> --body-file -` and states plainly that the
  string is in no commit, so amending changes nothing. Telling someone holding a
  PR body to amend a commit sends them looking for text that isn't there, and a
  check that reads as broken gets routed around.

  **The PR half fails SOFT, deliberately, and that residual is stated rather
  than hidden.** Unreadable commit history is a hard failure — it is a property
  of the branch under judgement. An unreadable PR body is not: `gh` missing,
  unauthenticated, offline, or aimed at a non-GitHub remote says nothing about
  the branch, and hard-failing there would stop **every** merge on the machine,
  including the entire internal track where no branch has a PR at all. That
  trades a rare wrong auto-close for a total halt. So the check logs
  `closing-ref PR-body check INDETERMINATE … a closing keyword in the PR body is
  NOT guarded on this merge` and lets the merge proceed on the commit half. The
  log line is the only place that says the body went unguarded, which is why it
  says it in those words. A branch with no PR is genuinely clean and is not
  logged.

  **What else the guard believed it covered and did not.** Asked directly, since
  the last omission stayed invisible for as long as nobody wrote it down. One
  more was real and is fixed here: GitHub links **three** reference forms —
  `#123`, `owner/repo#123` and `GH-123` — and the pattern carried two. Nobody on
  this fleet writes `GH-123`, which is exactly why one would have sailed through
  a check everyone believed mirrored GitHub's rule; an ack naming either
  spelling now silences the other, because they are the same issue in the same
  repo. The rest are recorded in the package doc as known limits: coverage is a
  property of callers, not of the predicate; commits already on the target are
  excluded on purpose; **a PR body edited after the gate reads it and before the
  merge lands is unobserved** — the gate is a snapshot, not a lock; PR titles
  are not read because GitHub does not link from a title (a GitHub-UI squash
  merge would promote one into a commit subject, a path the refinery's rebase
  and fast-forward never takes); issue and PR comments are not read and do not
  need to be, since GitHub acts only on the description and commit messages; and
  a closing keyword is flagged on any target ref although GitHub only acts when
  the merge reaches the default branch — over-strict with a per-reference escape
  is the safe direction.

  **Provenance.** Raised by a reviewer during the `drellem2/pogo#111` review and
  routed to the coordinator as a gate decision. Not a finding against the
  builder, who followed the shipped template exactly as written — which is what
  makes it a platform defect rather than a worker one.

  **Not done here.** There is no pre-flight for a PR body: a builder learns of
  an unacknowledged keyword when the refinery bounces the merge at the
  coordinator's submit, after the review loop has closed. The template change
  means the default path never trips it, so `pogo check-commit-body` was left
  alone rather than grown a second artifact mode. The on-disk copy under
  `~/.pogo/agents/templates/` is written by the installer from the binary's
  embedded corpus; an install that predates this change keeps prescribing
  `Resolves` until it is refreshed.
