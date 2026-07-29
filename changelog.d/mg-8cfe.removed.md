- **The refinery's protected-path gate is removed, premise and all (mg-8cfe,
  reverting mg-6c4b).** `internal/refinery/protectedpath_gate.go`, its call site
  in `attemptMerge`, the `.protected-paths` list, the `protected-path-check`
  stage and the gate's tests are gone. A branch touching
  `internal/agent/prompts/**` or `internal/agent/templates/**` merges like any
  other branch.

  **The premise was never checked with the repo owner.** The gate was built to
  enforce a "red line" on shipped prompts and templates that existed only as
  prose in the bodies of successive work items, reasoned from by three agents
  over a day and never confirmed. Asked directly, Daniel's answer was that there
  is no such red line — *"Remove the gate this is an overcorrection"*, and
  *"There is absolutely no red line for prompt file edits"* (2026-07-29). The
  mechanism was sound; the fact it enforced was not one.

  **Removed rather than softened, deliberately.** No warning mode, no allowlist,
  no label-based bypass. What was called an overcorrection is the mechanism, not
  its strictness setting, and a weakened gate is still the thing that was asked
  to go.

  **What replaces it is ownership, not enforcement.** Prompt and template edits
  are owned by the coordinator (`mayor`) in conjunction with `pm-pogo` as pogo
  SME. That is a routing rule about who decides, held by the people who decide,
  not a check at the write boundary — so the answer to "what stops the next
  unreviewed prompt edit" is a named owner rather than a refusal, and the
  question was answered rather than abandoned.

  Timing, because it decides whether the gate ever refused anything: it merged
  as `47b5d48` while the running pogod was still on `023fab5`, so it was code on
  `main` that no live process had loaded, and the removal was cut to land before
  the redeploy that would have loaded it. `v0.7.0`'s changelog entry for the gate
  is left standing as released history rather than rewritten.

  `scripts/mg-scope-guard.sh` is untouched — it is a separate, unwired, opt-in
  per-task mechanism.
