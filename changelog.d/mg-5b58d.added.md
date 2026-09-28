- **Crew agents are asked to reset their context every 4 hours (`[crew_reset]`,
  mg-5b58d). This changes crew behaviour for every operator, and it is on by
  default.** When a crew agent's session passes `after` (4h), pogod mails it
  once from `pogod`: at its next safe point (not mid-task, and not while it
  holds unread or unhandled mail), write a handoff note in its own `sweep.log`
  and memory directory, then run `pogo agent stop <itself>`. restart_on_crash
  respawns it about 2s later into a fresh session that runs "On Startup".
  mg-c2d5's audit estimated this saves about 22% of crew tokens.
  - **It asks; it stops nothing.** A session gets at most two notices: the first
    at 4h and one reminder an hour later. After that pogod is quiet for the rest
    of the session. A respawned session is a new session with a fresh count.
  - **Only agents whose stop is a reset are asked:** running crew with
    restart_on_crash and auto_start, and no park flag. Parked and on-demand
    agents are never asked, and polecats are out of scope.
  - **A self-issued stop is safe.** The stop runs in pogod's handler, which does
    not need the requesting `pogo` process to survive. Crew hold no claim to
    release, and schedules survive a supervised respawn. A new test has the
    agent stop itself from inside its own process tree and checks that it
    comes back with a new pid and a new start time.
  - **Default on:** it only asks, at most twice a session, and only agents
    whose respawn the supervisor already guarantees. Turn it off with
    `[crew_reset] enabled = false`, or leave out single agents with
    `exclude = ["name"]`. `after`, `renotice_after` and `interval` are tunable.
    With `[wake_watch]` on, wake-watch's pointer announces the mail. With it
    off, crew-reset types its own pointer of at most 100 bytes. Each notice
    emits `crew_reset_notice`.
