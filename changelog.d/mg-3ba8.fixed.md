- **The notifier could not read one episode record in ten, and said nothing
  about it (mg-3ba8).** Fixed in `pogo-reminders`, not here — this repo's entry
  is the correction to the claim `docs/operations.md` was left carrying.

  pogod stamps an episode boundary's `opened_at`/`closed_at` with Go's
  `time.RFC3339Nano`, which **trims trailing zeros** and therefore emits every
  fractional-second width from 0 to 6 digits. `pogo-reminders`' `parse_ts` hands
  those to Python 3.9's `datetime.fromisoformat`, which accepts **exactly 3 or 6
  digits and nothing else**, and clamped only the *long* side — so widths 1, 2, 4
  and 5 were rejected, `load_episodes` dropped the record, and that episode's
  coalescing was silently dead for its whole burst. Re-derived here rather than
  repeated: over the 1,000,000 microsecond values a `time.Time` can carry,
  RFC3339Nano emits 6 digits 900,000 times, 5 digits 90,000, 4 digits 9,000, 3
  digits 900, 2 digits 90, 1 digit 9, none once — **99,099 of 1,000,000, 9.91%,
  rejected**.

  **The live log agrees with the model.** `~/.pogo/events.log` holds 11 real
  `incident_episode_cleared` records — 22 boundary fields — and exactly one is
  unparseable by the old clamp: episode `ep-1787367213823303000-architect`,
  `closed_at = 2026-08-24T02:06:31.45799Z`, five digits, a roster of six. One
  record in 11 against a predicted 9.91%. That six-agent episode's mail paged
  one-by-one.

  `parse_ts` now normalises the fraction to 6 digits in **both** directions
  (right-pad short with zeros, clamp long exactly as before), and
  `tests/test-poll-mail-episode-coalesce.sh` replays every width 0–6 plus the 7-
  and 9-digit long side, showing 1, 2, 4 and 5 fail against a copy of the same
  script with the fix reverted — a control that asserts it found the code to
  revert, so a future rewrite fails loudly instead of comparing the fixed script
  against itself.

  **The silence was the wrong answer, not the drop.** A dropped boundary looked
  exactly like an episode that never happened: coalescing simply stopped, once in
  ten bursts, with nothing in the log to read. `load_episodes` now names it on
  stderr — `plan: episode <id> DROPPED - unparseable boundary (…)`. The degrade
  is unchanged and still safe (every message pages, one notification each); only
  the silence is gone.

  **Nothing changes in this repo's emitter, deliberately.** `RFC3339Nano` is
  correct, every Go-side reader parses it, and `internal/synthwatch`'s tests
  assert it. `ProbeLastHop`'s arm E — the control that constructs the failing
  width on purpose — stays: it is the standing check that this reader degrades
  toward noise rather than dropping the alarm, and it is what the next cross-repo
  format mismatch will trip.
