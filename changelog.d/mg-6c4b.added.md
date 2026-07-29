- **The refinery refuses protected paths, unconditionally (mg-6c4b).** A branch
  whose diff touches a path listed in `.protected-paths` is now refused
  after the rebase and before the quality gates, with stage
  `protected-path-check`. Initial list: `internal/agent/prompts/**` and
  `internal/agent/templates/**`.

  **The boundary moved, and that is the whole point.** The obvious placement is
  a GitHub Actions job on `pull_request`, and it is not merely leaky — it is
  inapplicable. The refinery does not merge through pull requests: `attemptMerge`
  runs `git merge --ff-only` and `git push origin main` directly, and `gh pr
  close` runs only afterwards to tidy a PR that already exists. The merge that
  motivated this ticket is on record:

  ```
  03:35:19  step=quality-gates    attempt=1
  03:38:58  step=push target=main attempt=1
  03:39:02  step=pr-close skipped: no PR for branch polecat-1935
  ```

  MR `mr-d9kmdoqtjv1m5em5h9og` landed a +38-line edit to
  `internal/agent/prompts/mayor.md` (a3f0efa, mg-1935) on main with **no pull
  request in existence** — so `on: pull_request` would never have fired,
  `on: push` would have fired after the write, and branch protection would have
  rejected the refinery's own push, i.e. stopped merging outright. GitHub is
  downstream of a push that already happened. The refinery is the process that
  performs the write, so the check belongs inside it. The gate phase running
  3m39s before the push was verified against that live merge in pogod's log, not
  inferred from reading the code.

  **The protected list is data, not prose.** One file, one pattern per line,
  read by the gate. "Red line" was previously a social fact distributed across
  the bodies of whichever tickets happened to be about it, which is how a fourth
  ticket walked through the door while three others sat correctly frozen. Adding
  a red line is now a data edit.

  The list is read from the **target ref**, never from the branch under merge.
  A list read from the branch would let a branch delete the list in one commit
  and edit a protected path in the next; read from `origin/main` it is state the
  branch cannot reach, and the list file protects its own path implicitly — so
  removing its self-entry does not unlock it. `--no-renames` is likewise
  deliberate: with rename detection on, moving a protected file out of its
  directory reports only the unprotected destination.

  **There is no bypass, and that is not an oversight.** No flag, no label, no
  CODEOWNERS rule, no marker file. An authorisation mechanism would have to
  distinguish "Daniel approved this" from "an agent claims Daniel approved
  this", and on this machine that distinction does not exist: every agent holds
  a `GH_TOKEN` authenticating as `drellem2`, so an agent can label, approve and
  satisfy CODEOWNERS *as Daniel*, and `--dangerously-skip-permissions` makes any
  local marker equally producible. Anything an agent can read the rules for, it
  can produce — so a bypass would convert the gate into an opt-out, and an
  opt-out routes back through somebody remembering, which is the control class
  that already failed. Daniel's route is not a bypass but a different mechanism:
  he pushes by hand, which never enters the refinery. Nothing is presented, so
  nothing can be forged. **Stated cost:** prompt changes now always require a
  manual push from him.

  `[gates] skip_on_retry` does **not** bypass it — the check sits beside the
  closing-ref check, outside the gate phase, for the same reason: the diff under
  inspection is exactly what the retry is about to push, and a check the retry
  path can bypass is a check the retry path will eventually bypass on the commit
  that matters. Two failure modes fail **closed**: a list that exists but does
  not parse, and a diff git cannot enumerate. An unrecognised pattern is a hard
  error rather than a skipped line, because a red line that silently matches
  nothing reads as protection while providing none.

  A `PreToolUse` hook cannot carry this and was not re-proposed. The existing
  scope guard's `case` covers `Edit|Write|MultiEdit|NotebookEdit`; `Bash` falls
  to the default branch and is allowed under a debug line asserting it "does not
  write", so a `cat >` heredoc walks past it regardless of polarity or wiring —
  and the tool set is open, so enumerating more tools does not close it. A gate
  reads the diff and does not care which tool produced it.

  The positive control drives the full `processNext()` pipeline rather than the
  gate function, replays the breach branch, and asserts both the refusal **and**
  that `origin/main` did not move — a gate that never fires and a gate that
  correctly found nothing are the same observation. Each test was confirmed to
  fail with the check disabled.
