- **The wedged-agent detector was blind on 100% of agents from its first pass,
  and the fallback that was supposed to survive exactly that had never executed
  outside the test suite. Wired the fallback, then fixed the counter stems — in
  that order, because the reverse destroys the evidence (mg-20eb).** 40
  `wedge_watch_error` events, 0 verdicts, over the detector's entire 25-minute
  production lifetime.

  **The fallback had no production writer.** `internal/wedgewatch`'s `stallOf`
  documents an event-log-silence fallback for when the declared-work counter
  cannot be parsed, "so that a harness that renames its status line degrades
  this detector to a coarser one rather than to a silent one". It keys on
  `Observation.EventsLastSeen`. `grep -rn EventsLastSeen internal/ cmd/` returned
  three hits: the branch that reads it, and one unit test that writes it.
  `observe()` — the only production constructor of an `Observation` — set Name,
  Identity, Type, Alive, Uptime, Output and LastOutputAt, and never that. So
  `o.EventsLastSeen.IsZero()` was **true by construction for every agent that has
  ever run on this box**, and an unparseable counter did not coarsen the
  detector, it disabled it.

  `wedgewatch.SystemEvents` now builds the index and pogod binds it. The read is
  **lazy** — at most once per sample, and only when some agent's counter failed
  to parse — because the scan is ~720ms against this box's 76MB live log
  (measured) and a fleet whose counters all read needs it for nothing. Only the
  live log is scanned, not the five rotated files: an identity whose last line
  has rotated out reports as *unjudgeable* rather than as stale, which
  understates a very stale agent and is the safe direction. An unreadable log is
  never an empty one.

  **Scheduler traffic deliberately does not count as recency.** The index keys on
  the event's own `agent` field. pogod's 64,194 `scheduler_fire_delivered` lines
  are logged against `pogod`, not against the polecat each was delivered to — so
  they cannot keep a wedged agent's clock warm. Crediting them would repeat
  mg-fc8d's own fault one level down: a delivery record proves the sender ran,
  never the receiver, exactly as PTY animation proved the spinner was repainting
  and not that any work was happening.

  **The error text was misleading, which is a third defect.** The single blind
  message said the event log "has no entry for this identity". Nothing had opened
  the log, and `crew-mayor`, `cat-e6cc` and most of the others plainly had
  entries — so anyone diagnosing this would check the claim, find it false, and
  have to work out that the clause was a constant rather than an observation.
  There are now two messages, and the never-looked one says only that no fallback
  was available.

  **Then the stems.** All four missed on all agents simultaneously, which is not
  the drift the package doc predicted. Live PTY tails off five running agents
  (doctor, mayor, architect, pm-pogo, and the polecat that did the work) found
  three changes: the completed-turn line now reads `✻ worked for 55s` rather than
  `Baked for`; the live counter moved into a spinner parenthetical whose verb is
  randomized per render (`cerebrating…`, `crystallizing…`, `slithering…`), so
  only its shape `(11m53s · ↓ 29.6k tokens)` can anchor it; and `esc to
  interrupt` left that parenthetical to become part of a permanent hint bar.

  **A stem on a permanently-rendered string is a false anchor**, and that is the
  transferable lesson. `esctointerrupt` did not stop matching — it matches on
  every agent on every pass and carries no number, and only `onlySeparators`
  stopped it reading the spinner's repaint digits as a counter. It is kept for
  older harnesses and demoted to last. The new stems go first, ahead of the
  legacy ones, because `lastDurationNear`'s last-occurrence rule protects against
  a quoted counter *within* a stem but not *across* stems: a higher-priority stem
  quoted once anywhere beats a live one at the buffer tail, and an agent editing
  `counter.go` has `Baked for 3m 2s` in its own PTY. The live parenthetical also
  outranks `worked for`, because the latter is the previous turn's total and is
  frozen for the whole of the current one — reading it would report a long honest
  turn as a wedge.

  **Measured, not asserted:** parsing the live 4KB PTY tails of all six running
  agents goes from 0/6 to 6/6. Reverting the wiring in place fails the three new
  fallback tests with the production error text, so the reproduction is real
  rather than a test of itself.

  **Not fixed here:** the 03:00 redeploy that missed five nights and left the
  daemon ten days behind main is tracked under mg-01f7 / mg-0ffc. Separately
  noted: `internal/agent/api.go` documents `?lines=N` / `?bytes=N` on
  `GET /agents/{name}/output` and the handler ignores both, capping at 4096
  bytes — which is why the 16KB window `OutputScanBytes` actually scans could not
  be dumped for verification.
