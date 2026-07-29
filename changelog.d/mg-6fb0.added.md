- **The dispatch gate learns one SHAPE — `assignee: blocked:<agent>` — so an item
  can say *who* it is waiting on without giving up the gate (mg-6fb0).**
  `blocked:mayor`, `blocked:daniel`, `blocked:pm-anyone` gate dispatch exactly as
  `human` and `parked` do, and additionally record who the item waits on, so
  `mg list --assignee=blocked:daniel` is an answerable question in the same way
  `--assignee=parked` became one under mg-a3a2. Both enforcement points read it
  through the one predicate they already share (`config.IsDispatchGated`):
  stall-watch stops nudging about it, and `pogo agent spawn-polecat` refuses with
  409 — naming the agent to chase, because "blocked on mayor" is not resolved by
  the generic "done by hand or unparked".

  **The gap was narrower than the ticket's diagnosis, and the ruling reversed
  half of it.** The filing argued `assignee` should stop gating and the signal
  move elsewhere. It doesn't, and it didn't: mg-a3a2 was already the
  make-it-declared move (`human` and `parked` are two readable sentinels, each
  separately queryable), and the denylist is deliberate — an allowlist of
  dispatchable agents would have to enumerate the roster and would stop watching
  work the day a new agent is hired, which is mg-4bd4's defect exactly. What was
  actually missing is that **blocked-on-a-NAMED-agent could not be said at all**.
  All three recurrences (mg-bb43, mg-779b, mg-bf5e) set an agent name and got no
  gate — which is *correct*: an item merely owned by mayor is dispatchable. The
  filer's only options were `assignee=<agent>` (keeps who, loses the gate) or
  `assignee=parked` (keeps the gate, loses who).

  **A shape, not a longer list, and that distinction is the whole fix.**
  `blocked:` matches structurally, so every agent name that will ever exist gates
  with no config line and no code change — mg-4bd4 cannot recur through this
  door. It is also independent of `non_dispatchable_assignees`: a deployment that
  replaces the vocabulary still gates `blocked:mayor`, because the rule is about
  how the field is written rather than which values are listed. A bare `blocked:`
  still gates (the author wrote "blocked"; declining on a truncated name would
  fail unsafe) and the refusal says it names nobody rather than inventing an
  agent.

  **Additive, and the sequencing hazard was measured before the change, not
  after.** Zero of the eight then-`human` items carried a `blocked-on-*` tag, so
  anything that *stopped* reading `human` would have stranded all eight as
  dispatchable. `human` and `parked` read exactly as before, nothing was
  backfilled, and there is no window in which the queue is unguarded.

- **Both paths now say when an item declares a block the gate cannot hear
  (mg-6fb0).**
  Agents had already invented a channel for "blocked on X" before the field could
  express it — `blocked-on-daniel`, `blocked-on-daniel-confirm`,
  `blocked-on-redeploy` exist in the store (mg-cf48, mg-e925, mg-a96c) and
  `mg archive` was taught to respect them (mg-3c53). Those tags stay
  human-facing markers and are **never** consulted for gating: moving the gate
  onto tags would split it across two channels and forfeit `mg list --assignee=…`
  as the single answerable question. Instead, when an item declares a block in
  its tags *while its assignee leaves it dispatchable*, stall-watch appends
  `[block-intent] mg-xxxx is tagged blocked-on-daniel but its assignee does not
  gate dispatch — if it is genuinely waiting, set --assignee=blocked:daniel` to
  the nudge it was already sending (plus `block_intent_mismatch_ids` in the
  event), and the dispatch gate logs the same contradiction and **dispatches
  anyway**. Advice, not a gate.

  **It fires on the contradiction, never on ownership.** The ticket asked for a
  warning on any named assignee on an `available` item; that would fire on nearly
  the whole queue, because `pm-template` files every ticket with
  `--assignee=pm-<name>` and an owned item is legitimately dispatchable. What
  separates "owner" from "intended block" is that the author said so — and today
  they say it in a tag. mg-a96c carries `assignee: pm-pogo` with
  `blocked-on-daniel-confirm` and is the live positive control. A
  `blocked-on-mg-1234` tag names another work item, so the advice there points at
  `mg new --depends`, not at the assignee field.

  **Positive control both ways, in one test each.** The shape is demonstrated
  gating at both enforcement points and the advisory demonstrated firing, beside
  the cases that must stay quiet — ordinary ownership, `human`, `parked`, and
  `blocked:<agent>` itself, since an interim check that flagged the repair it
  recommends would be worse than no check.

  **Population report, named.** At the time of the change **0 of the 15
  `available` items carried a non-gating assignee** — the population is 8 `human`
  and 7 `parked`, and no other value appears. The three cited instances had all
  since left `available` (mg-bb43 done, mg-779b re-set to `parked`, mg-bf5e
  archived still holding `assignee: mayor`). So the silent backlog is empty
  today, which is what makes "additive, backfill nothing" free rather than a
  deferred migration — and is why the interim check earns its place: the
  measurement says the next instance has not been filed yet, not that the shape
  stopped recurring.
