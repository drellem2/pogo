- **`parked` joins the default `non_dispatchable_assignees` (mg-a3a2).** A
  deliberately-parked work item can now say so — `mg edit <id> --assignee=parked`
  silences stall-watch's dispatch nudges without asserting that a human owns it.
  The default gate list is `["human", "parked"]`; `human` recovers its single
  meaning, *a person must act*.

  Until now `human` was the **only** value that silenced the detectors, so it
  accumulated three incompatible senses in one queue: gated-on-a-person,
  parked-do-not-chase, and filed-here-for-lack-of-an-alternative. Worth being
  precise about why a convention would not have fixed this: **the overload was
  not a discipline failure, it was the only expressible option.** Two agents who
  each understood the problem misfiled items into `human` within a single
  session — including the agent filing this very ticket, which went to `human`
  reflexively for ordinary pogo work that never needed a person at all.

  The cost was not confined to the misfiled rows. Everything that reads
  `assignee` to decide what to escalate — stall-watch, PM digests, mayor,
  architect — re-derived the conflation independently and could not see the error
  from the field, because the data never recorded which sense was meant.
  Architect summarized the queue to Daniel as "entirely gated on you" while most
  of it was parked fleet-internal work. A convention about how to use `human`
  cannot be read back out of the data; a distinct sentinel can.

  Two things this deliberately does **not** do. Parking buys silence from the
  alert channel, not disappearance from listings (the `gh-open:` precedent,
  mg-6e57) — a parked item still appears in `mg list` with its assignee and age.
  And every gate here remains unconditional and permanent: a gated item never
  ages back into the alert channel whatever sentinel it carries. Aging the gated
  queue belongs to the PM sweep, which reads it anyway and can flag "gated N
  days" with no code change; `mg-0ffc` sat `available` and gated for eleven days
  with stall-watch structurally unable to notice.

  Both directions are pinned by test, since a gate only ever observed
  suppressing has not been shown to pass anything through: `parked` goes quiet,
  and unassigned and agent-owned items still alarm. The point was to stop
  overloading `human`, not to make it easier to mute real work.
