- **The dispatch gate is enforced in Go at the dispatch point (mg-ebb0, building
  the mg-4798 ruling).** `pogo agent spawn-polecat --id <item>` now refuses to
  put a worker on a work item whose assignee gates it away from automatic
  execution — by default `human` (a person must do this by hand) or `parked`
  (deliberately set aside) — with HTTP `409` and a message naming the item and
  the assignee that gated it.

  **The rule existed and was already enforced in Go; it was enforced on the wrong
  path.** `internal/stallwatch` has tested it since mg-a3a2 to decide what to
  **watch**. The path that actually **dispatches** carried it as prose instead:
  `internal/agent/prompts/mayor.md:423` told the coordinator that a
  `--assignee=human` item "won't be dispatched — it stays assigned to the human",
  170 lines from the listing advice it had to be read with. So the guarantee was
  that an agent had read and retained a paragraph, and it was not kept —
  `spawn-polecat` would spawn on a human-assigned item without complaint. The
  measured state at build time: `grep -rn 'isDispatchGated|NonDispatchableAssignees'`
  returned no hit anywhere in `cmd/pogo/`, and the live store held real items
  assigned to `human` and `parked` sitting in `available/`.

  **One predicate, two enforcement points, no second copy.** The rule now lives in
  `config.IsDispatchGated(assignee, gates)` beside the vocabulary it tests;
  `stallwatch.isDispatchGated` became a one-line delegation to it and its existing
  tests were left to prove the behaviour did not move. A second implementation of
  the rule *is* the defect mg-4798 named — "one enforcing the rule in Go, one
  describing it in prose, and the prose one dispatches" — so the reuse is the
  deliverable, not a tidiness pass. mg-a3a2's config-driven vocabulary is
  inherited rather than re-fixed: an operator's `non_dispatchable_assignees`
  replaces the default for both paths at once.

  **The gate sits in `handleSpawnPolecat`, beside the redeploy drain, not in the
  CLI.** `cmd/pogo` is one caller of `POST /agents/spawn-polecat`; a check there
  is bypassed by anything that posts to the endpoint, and the coordinator is a
  program. This is the same boundary argument as mg-6c4b: the check belongs in the
  process that performs the act. `409` rather than the drain's retryable `503` —
  the request conflicts with the item's own state, so retrying it unchanged is
  refused identically forever. It is placed above `ValidateAgentName` and every
  side effect, so a refused dispatch leaves no worktree, agent dir, or prompt file
  behind (mg-ef80), and it routes through `failPolecatSpawn`, so the refusal is on
  the record as an `agent_spawn_failed` event carrying its reason (mg-d22a).

  **The default enforces.** A `Registry` that never gets `SetDispatchGate` still
  gates `human` and `parked` — the same reasoning as `SetClaimReleaser`'s default:
  a guard that engages only once somebody remembers to wire it is absent in every
  deployment where they didn't. It is also deliberately not armed behind
  `stallWatchArmed`: the gate is not a detector, and tying it to whether a
  coordinator is being watched would silently disable it on every sandbox and
  unconfigured daemon.

  **It fails OPEN, which is stated rather than discovered.** No `--id`, an id
  absent from the store, or an unreadable store all dispatch normally — the last
  with a log line saying the gate could not answer. `--id` is optional by design
  (a spawn without one merely forfeits start-verification, mg-2437), so failing
  closed would refuse every id-less spawn, and one bad path in macguffin would
  halt the fleet. This is the opposite of stall-watch's asymmetry and for the
  opposite reason: there, guessing wrong costs a spurious nudge, which is
  self-correcting; here it costs a refused dispatch. What the gate stops is a
  coordinator dispatching a gated item it read out of `available/` — the actual
  mg-4798 failure, where the assignee is right there in the frontmatter. It is not
  proof that no worker can reach gated work, and `docs/CONFIGURATION.md` says so.

  **Positive control, per the ticket and the house rule.** A guard only ever
  observed on unassigned items has not been observed at all — that is how the
  sibling-file gap in mg-da48 survived. So the tests assert the guard *can* fail:
  dispatch is refused for each default gate value, refused for a
  configured-vocabulary value, and **not** refused for unassigned / `pm-pogo` /
  `mayor` (without which an unconditionally-refusing gate would pass). Each
  refusal test was confirmed to fail with the guard disabled — a `human`-assigned
  item then reaches template resolution, which is the pre-fix behaviour
  reproduced. Verified end-to-end against a real `pogo agent spawn-polecat`
  invocation in a sandboxed store, not only at the handler.

  Added `workitem.FindFrom(root, id)` for the by-id lookup. `ListFrom` was the
  wrong primitive twice over: it parses every item to answer about one, and its
  `.md` suffix filter cannot see `claimed/` at all, because a claim renames the
  file to `<id>.md.<pid>` — so a by-id lookup built on it reports a claimed item
  as *absent*, and for a gate "absent" means "not gated". The test pins both the
  fallback and the premise. Ids containing a path separator or `..` resolve to
  not-found rather than a read, since the id arrives in an HTTP request body.
  Store-root resolution is shared with `MGClaimReleaser` rather than copied, so
  the gate inherits its test-safe default: under a test binary it reads a
  throwaway temp store, never the live `~/.macguffin` — which currently holds
  `human`- and `parked`-assigned items that would otherwise make existing spawn
  tests depend on Daniel's queue.

  Scope note: `mayor.md`'s two false statements are **not** touched here. That is
  mg-df42, sequenced after this deliberately — landing this is what makes
  `:423`'s sentence true, at which point it should name what enforces it rather
  than assert the outcome bare. `internal/agent/prompts/**` is a protected path
  (mg-6c4b) and Daniel's alone.
