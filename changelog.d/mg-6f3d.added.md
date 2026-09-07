- **An alarm on N consecutive failing assistant turns, delivered out of band and
  verified with the fleet DOWN (mg-6f3d).** Successor to mg-6616, which
  established six failure modes across 2026-08-04..09-03 and one actionable
  output. This is that output — plus the correction the same day's incident
  forced on its shape.

  **The gap.** An agent that cannot complete a turn is indistinguishable from an
  idle one by every instrument on the box except the transcript. `pogo agent
  list` reports `running` with correct uptime; the process does not die, so
  `restart_on_crash` never fires; schedules keep firing and
  `scheduler_fire_delivered` keeps logging success — 647 of them on 2026-07-22
  while every consuming turn died instantly on an expired credential. What is
  not absent is the evidence: the failure text sits in the transcript, once per
  failed turn, for the whole duration. Nobody was reading it.

  **Measured, on this fleet's crew transcripts, 2026-09-07 — 114 files, 76,541
  assistant turns:**

  ```
  12,230  failing turns          longest single run: 661 turns
  64,290  established work       mayor, 2026-08-14T08:25:23Z .. 08-19T06:48:35Z
      21  ambiguous                    = 4 days 22 hours of answering nothing
       0  failing turns matching no known string
  ```

  **Consecutive, not a rate — and the difference is not stylistic.** `synthwatch`
  counts failing turns in a trailing 30-minute window, which is bounded by how
  busy the agent is. That 661-turn run reads out of a 30m window as `2 errors in
  30m`, which is also what one bad afternoon looks like. A run is a position in
  the transcript and has no ceiling. The two detectors read the same files, ask
  different questions, and do opposite jobs — synthwatch's output is a restart
  SUPPRESSION, this one's is a delivery to a person.

  **N=3 is read off the distribution, not chosen.** Longest run per file, over
  the 92 files holding any failing turn: `1 turn: 24 files | 2: 8 | 3-9: 6 |
  10-99: 18 | 100+: 36`. Three is where it separates.

  ### The string is the NAME. The predicate is structural.

  The ticket proposed alarming on turns "matching a known refusal/failure
  string". Measured against the corpus that proposal came from, the string is
  the wrong predicate on its own **in both directions**: 21 turns match a
  failure string and are real, token-spending model turns — agents writing
  ABOUT the outage — and a string-only detector calls those failures. So a turn
  is a failure when the harness attributed it to a synthetic model, spent
  nothing either way, and flagged it an API error; the string then says which
  mode. A structurally-synthetic turn matching nothing in the table is still a
  failure, named `unrecognised`, and its own text travels in the alarm.

  ### There is no healthy default bucket

  mg-6616's own classifier bucketed the failures it knew and defaulted the rest
  to `work`. It scored **386 failures as healthy turns** and turned one entirely
  dead day (2026-08-21) into "41 work". The error points toward health, which
  makes the surrounding days look like a solid baseline and pushes the apparent
  onset later. The fix was not a better string list; it was removing the default
  when the default is the healthy state. So every turn lands in one of four
  verdicts and none is reached by falling through, and the report has four
  states of which exactly one — a most-recent turn that is ESTABLISHED work — is
  a claim of health. An unreadable transcript, a transcript with no assistant
  turns, and a sub-threshold tail are each their own answer.

  ### The delivery is the deliverable

  mg-3222 measured this on the outage that is this item's fourth exhibit. The
  wedge detector fired **correctly, in 14m30s**, and named all six agents and
  the cause. It then emitted **sixteen further findings over 3h55m, every one
  carrying `"routed_to": "nobody"`.** The outage ran 5h30m and ended when a
  human noticed a dead fleet. Detection was never the missing piece.

  - **No sink requires an agent turn.** Every path is pogod writing to disk:
    `human` mail, which the out-of-process `com.pogo.deadman` launchd job polls,
    then a plain append to `~/.pogo/alarms/refusal-streak.log`. ALL sinks are
    tried, never just until one works — two channels that fail independently is
    the only reason to have two, and a chain that short-circuits has one channel
    plus a spare that is never exercised.
  - **An alarm is delivered only when the artefact is observed on disk.** `mg
    mail send` exiting 0 says the command ran; the file under
    `<root>/mail/human/new/<msg-id>` is what the notifier polls, and that is
    what gets stat'd. "Did it alarm?" and "did anyone learn?" are separate
    questions and only the second matters.
  - **An undelivered alarm has its own event type and no floor.**
    `refusal_streak_undelivered` fires on every scan until something takes it,
    carrying `attempts` and `silent_seconds`. The 60-minute floor between
    repeats applies only after a delivery has been CONFIRMED. A field inside a
    normal-looking event gets read past; a type gets grepped for.

  ### Verified with the fleet down, not up

  `pogo check-refusals --probe` builds a throwaway macguffin store with **no
  agents in it at all**, delivers the alarm through the same code path pogod
  uses driving the same real `mg` binary, and confirms the bytes are in the
  maildir. Matched controls, because an arm that only ever passes proves nothing
  about whether it can fail: an unregistered recipient must be REFUSED, a ledger
  path that cannot be created must not confirm, and `Deliver` with every sink
  failing must return `ErrNoRecipient` rather than `nil`. A probe that could not
  be BUILT reports INSTRUMENT FAILURE and exits 3, never a pass. The same probe
  runs in `go test ./...`, so the refinery exercises it on every merge.

  ### The fourth outage, replayed through the shipped binary

  mayor's real 2026-09-07 session, truncated at the last failing turn so the tail
  is the outage as it stood, read by `pogo check-refusals`:

  ```
  [ STOP ] fleetdown-control  streaking
           16 consecutive failing turns (login), 2026-09-07T10:57:41Z–16:02:45Z
           Login expired · Please run /login
  exit=1
  ```

  Its control — the same command against the live, recovered fleet — reports nine
  agents `working` and exits 0. **This is not faster detection and is not claimed
  as such:** mg-3222's wedge detector named the same outage at 11:06:10Z, ahead
  of this detector's fleet-first N=3 at 11:30:10Z. What is new is that the alarm
  has somewhere to go.

  `internal/refusalstreak` (the detector), `internal/refusalwatch` (the alarm and
  the fleet-down probe), `cmd/pogod/refusalwatch.go`, `cmd/pogo/checkrefusals.go`.
  New events: `refusal_streak_alarm`, `refusal_streak_undelivered`,
  `refusal_streak_cleared`.
