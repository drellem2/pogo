- **The nightly lost-schedule alert now checks whether the agent still exists
  before telling anyone to nudge it, and prescribes the remedy that fits what it
  found (mg-6d7b).** Observed live 2026-08-10 02:03Z, on the bounce that
  redeployed pogod to b802170. `pogo-deploy` mailed mayor and human that
  `mail-check-doctor` had not come back, correctly warned that the fleet's mail
  loop "WILL LOOK HEALTHY", and then closed with: *restore by nudging the
  affected agents to re-register.* That remedy could not work. There was no
  doctor process to nudge — doctor was absent from `pogo agent list` entirely.
  The agent was lost and the schedule went with it, which is the reap working.

  **Why one right finding produced an impossible instruction.** The check derives
  its verdict from a single observation — a mail-check that existed before the
  bounce and does not exist after — and that observation has causes with opposite
  remedies:

  - the agent is alive and lost its schedule → nudge it to re-register;
  - the agent is GONE and its schedule was reaped with it → START the agent.

  It printed the first, unconditionally, for eight months. A nudge into the void
  returns no error worth noticing, so an agent following the printed remedy
  literally would have reported the fleet restored with the mail loop still dead.
  Mayor recovered doctor with `pogo agent start doctor` only because it happened
  to run `pogo agent list` first.

  **What it does now.** Before composing the mail it reads the registry — the
  same one `pogo agent list` reads — and writes one paragraph per lost schedule,
  chosen by what that registry says about the owning agent: `pogo nudge` when the
  agent is running, `pogo agent start` when it is absent, `pogo agent wake` when
  it is parked, and for any other status (`restarting`, …) the status itself and
  no remedy at all. When the registry cannot be read the mail says so and gates
  both commands on a check the reader must run — an alert that does not know must
  not print a confident remedy, which is the whole defect restated.

  **Three ways the repair could have re-committed the defect, and what stops
  each.** *Presence is not liveness* — `pogo agent list` says so in its own help,
  and a parked agent is listed with pid 0 and status `parked`; reading presence
  alone would have called it alive and prescribed a nudge for a process that is
  not there, so the status travels with the name and parked is its own class.
  *The owner is not the id* — a polecat's schedule is keyed on its work item
  (`mail-check-mg-6d7b`) while its agent is named something else (`c6d7b`), so
  the id→agent map is read from the schedule list, pre-bounce, while the agents
  that own the schedules still exist; stripping the prefix would address an agent
  that never existed. *`pogo agent start` is crew-only* — it reads
  `~/.pogo/agents/crew/<name>.md`, so a gone polecat gets `mg show <work-item>`
  and an explicit note that a finished polecat taking its mail-check with it is
  the reap working, not a fault.

  **The recurring case is named, and named as a condition rather than a defect.**
  An agent that pogod cannot auto-start regenerates this alert on every nightly
  bounce, forever. The absent-agent paragraph says that in as many words and
  points at `pogo agent prompt list` and the `auto_start` declaration it is
  looking for, so the twelfth identical night is legible as one cause rather than
  twelve incidents. It then tells the reader **not** to switch the flag on to
  silence the mail, and cites why: doctor's `auto_start = false` is a deliberate
  mitigation for mg-8677, where the reap lets auto_start override a corpse
  (mg-d9d1, mg-d6ac). Naming a missing flag as the cause is one quick read away
  from being taken as a request to add it, and that trade buys a quieter mail
  with a live reap bug. The mail closes that reading itself, because it is read
  at 02:03 by someone who does not have those ticket numbers to hand.

  **The same wrong sentence is gone from `pogo-self-deploy`.** Its post-kickstart
  check reports the same finding ~30s earlier and closed with the same
  unconditional nudge. It reads schedules and never the registry, so it genuinely
  cannot make the call — it now prints both remedies as conditional on
  `pogo agent list`, and says which answer selects which, rather than guessing.
  Leaving it would have put contradictory instructions in the same night's log.

  Proven in both polarities by mutation: forcing the classifier to "alive" (the
  pre-fix behaviour) fails 13 assertions and forcing it to "gone" — the swap that
  would pass a one-sided test suite — fails 12.
