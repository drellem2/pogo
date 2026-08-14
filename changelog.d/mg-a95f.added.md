- **`agent_stopped` now names the stop path that produced it, so a fleet-wide
  drain is no longer indistinguishable from five separate `pogo agent stop`s
  (mg-a95f).** `reason` has two values, and `"requested"` is every deliberate
  stop in the tree collapsed into one word: six call sites reach
  `Registry.Stop`, of which exactly one is a person. The new
  `details.stop_cause` separates them — `request`, `stop_all`, `park`,
  `merge_reap`, `merge_backstop`, `done_reap` — and it is written only on the
  `requested` branch, because an agent that finished its own work was not
  stopped by anybody and an empty cause there would read as an *unattributed*
  stop rather than as no stop at all.

  **The case it is drawn from.** On 2026-08-08T00:44:20Z five crew agents
  stopped 0.42s apart, all `exit_code=0 reason=requested`, and the fleet stayed
  dark for 33 hours. From the event log alone, "was that one command or five?"
  was unanswerable — the answer lived in a single `~/Library/Logs/pogo/pogod.log`
  line, and `internal/server/modeaudit.go` documents that file as exactly what
  cannot be relied on (pogod logs to inherited stderr; four months of it once
  held zero copies of the very line in question). mg-293c closed the half above
  the registry — a run-mode change now names its HTTP caller. This closes the
  half below it, so the five records themselves say `stop_all` and the two
  events join up without leaving `events.log`.

  **What the investigation established, recorded in `orchestrationresume.go`
  rather than left in a ticket.** The mechanism is now measured, not inferred:
  pogod.log carries `server: transitioning to index-only mode` at 01:44:19
  local, three seconds before all five `restart failed: registry shut down`
  lines — one `transitionToIndexOnly`, and its shutdown latch is why
  crash-respawn was refused. **The caller remains unidentified**, and the reason
  is the part worth keeping: the obvious query, `pogo events list
  --type=server_mode_changed` around that minute, returns nothing, and that
  nothing is a **null instrument, not a negative result**. The emitter merged at
  2026-08-07T18:49:27Z; the daemon that logged the line had been running since
  17:37:28Z at the latest, and the first `server_mode_boot` in `events.log` is
  2026-08-09T09:41:19Z — more than 33 hours after the stop it was being asked
  about. Merged is not running.

  **The remedy inherits that same defect and says so.** `stop_cause` is absent
  from every record written before it, and from every record a daemon writes
  until it restarts onto a build carrying it — so its absence means the field
  did not exist, never that the stop was unattributed. Both `docs/event-log.md`
  and `modeaudit.go` now state the dates a reader has to check before treating
  a silence as evidence, which is the mistake this ticket was filed to prevent.

  A second residue, stated rather than left to be found: a seventh stop path
  added later inherits `request` by calling the bare `Registry.Stop`, and would
  read as a person having asked. That is the least-bad default — every existing
  caller of `Stop` *is* the explicit path — but the doc-drift test catches an
  undocumented new *constant*, not an unthinking reuse of this one.

  **Not fixed, deliberately.** Nothing here stops the fleet from being stopped,
  and nothing guesses at a mechanism. This has happened once in the observable
  log; the response is identification. The restart-obligation half is mg-5af1
  and is independent.
