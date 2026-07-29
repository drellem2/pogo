- **The coordinator stops being told that `--status=available` means unassigned
  (mg-df42).** `mg list --status=available     # Unassigned work ready to claim`
  described a filter the command does not apply. Status and assignee are
  orthogonal by construction, so that listing returns items assigned to `human`
  and `parked` too — measured, not inferred: the live queue answers it with
  `assignee: human` and `assignee: parked` rows. Nor is there a flag that
  narrows it, because `--assignee=<name>` selects *one* assignee and cannot
  select "none". The comment now says `Unclaimed work — NOT "unassigned"`, and
  step 1 — where the coordinator actually reads the list and decides what to
  dispatch — says what to do instead: read the assignee column, which `mg list`
  always prints, and skip `human`/`parked` before spawning anything.

  Fixed in the prose, deliberately, and **not** in `mg list`. Filtering the CLI
  was option (a) of the mg-4798 ruling and was rejected on five independent
  grounds — ARCHITECTURE.md M2 ("the CLI is convenience, not gatekeeper"), the
  orthogonality itself, a shipped tested contract at `main_test.go:873`, and
  that it fixes nothing for pogo's real consumers, which read `available/`
  directly. The comment was the thing that was wrong.

- **The dispatch guarantee names what enforces it, and admits where it does not
  reach (mg-df42).** The `qa: manual` step asserted that a `--assignee=human` QA
  item "won't be dispatched to a {{.Worker}}" — an automatic outcome stated
  bare, with nothing named that produces it. That is now true rather than
  hopeful, because mg-ebb0 landed the gate in Go at the dispatch point
  (`e4a406c`): an assignee in `non_dispatchable_assignees` makes `spawn-polecat`
  fail with **409 Conflict**, naming the assignee, before any worktree or agent
  dir is created. The line now says so, and points at step 2 where the gate is
  described once.

  It also states the gate's two limits, because a sentence that is true only
  because of a guard elsewhere decays the moment the guard moves. The gate is
  keyed on `--id`, so a spawn with no `--id` or a wrong one is never checked;
  and it lives in the daemon, not in the template, so a pogod predating that
  build does not refuse at all. That is not hypothetical at the time of writing:
  the running pogod is `023fab5`, and `git merge-base --is-ancestor e4a406c
  023fab5` exits non-zero — the gate is merged but not yet live. So the template
  puts the control in step 1's assignee check and calls the daemon gate a
  backstop the coordinator has not confirmed is behind it, which reads correctly
  both before the next deploy and after it.
