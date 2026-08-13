- **`pogo schedule list`'s `COMPLETED` column is now `ACKED`, and it states the
  ceiling it is read against.** A tracked row reads `103/302  1 ack per 2.9
  fires`, followed by the unchanged `⚠ N unacked` marker, over a legend saying
  the column counts *token redemptions, not work done*, that the ceiling is
  below 100% for any schedule whose turns outlast its cadence, and that
  `⚠ N unacked` is the number to read because it is the one that does not
  saturate. `pogo schedule completion` prints the same quantity both ways and
  reports the outstanding-token count separately, since that part of the deficit
  is a property of when you looked.

- **The counter cannot distinguish a dropped fire from an unacked-but-executed
  one, and it never could: acking the newest token does NOT clear the earlier
  ones.** `issueFireTokenLocked` abandons the previous token deliberately, so a
  run of N fires landing inside one agent turn yields at most one ack however
  completely the work was done. That was the question this item was filed to
  settle, and it settles it in the direction that makes 42% an artefact of
  delivery interleaving rather than a fault of anyone's.

- **The obvious repair — "one ack retires the ENTIRE outstanding set" — is
  rejected on a measurement, and the rejection is recorded where a future
  proposer will hit it.** It assumes the run was superseded by an agent BUSY
  working, so one catch-up discharged all N. mg-772f measured that assumption:
  **51.5% of this fleet's superseded fires landed while synthwatch had already
  detected the agent's turns were dying**, and in the worked 27-fire episode of
  2026-08-09, 26 of the 27 turns died on `ENOTFOUND` and never ran. Retiring the
  set would have booked 27 completions for one surviving turn, converting a
  4.5-hour fleet outage into a clean reading in the one instrument built to see
  it. A repair that scores a dead fleet at 100% is worse than the deficit it
  tidies.

- **So the name changed instead, and no threshold did.** The item's constraint
  was that if the number is an artefact the artefact is the bug, and widening a
  floor until the alarm stops is worse than the deficit. Numerator, denominator,
  every gate in `internal/ackwatch` and the `DefaultStallThreshold` are all
  untouched; what is added is `Entry.AttentionGap`, the unit that says what the
  ratio measures, and the ceiling printed beside it. `TestAckColumnLegend_
  DoesNotTellTheReaderToRaiseTheFloor` pins that the legend redirects the alarm
  rather than suggesting it be silenced.

- **The identity is now pinned against the COUNTERS, not just the events.**
  `FiresCompleted/FiresDelivered == 1/AttentionGap - outstanding/FiresDelivered`
  to zero residual — the same equality `populations.go` asserts over events,
  asserted over the persisted surface that `pogo schedule list` actually renders.
  It is algebra, not a fit, and a future edit that gives the ratio some other
  meaning fails a test instead of mailing an alert nobody trusts.

- **Answering the fourth question — who receives this and what they DO.** The
  ratio's recipient is nobody: it is not an alarm input. `mayor.md` §3b now says
  so explicitly, in the same block that lists the findings that *do* name an
  agent or cohort and a remedy, because the reason a `FLEET DEFICIT: median 42%`
  sat escalated for 46 hours is that it named no action a coordinator could
  take. The playbook's old pointer — *"the raw table: completed/delivered per
  schedule"* — was the sentence that reading came through, and a guard now
  rejects it by name.

- **A retracted cause was still shipping in `pogo check-acks --populations`
  help.** mg-772f corrected the `batched` legend in `populations.go` because
  *"several fires delivered inside one agent turn"* names a cause nothing in
  that report measures and is wrong for 51.5% of the fleet's superseded fires.
  The identical wording survived one file over, in the CLI help, unpinned. It
  now states the measured fact and points at the report's own `WHICH SIDE` block
  for cause.

- **Noted, not fixed, and filed separately: an ack that beats its own delivery
  record produces `completed > delivered`.** Observed live in this item's own
  worktree — `Acked mail-check-mg-a14c … 1/0 fires completed`, then `1/1` with
  `unacked_streak: 1` a moment later. `recordDeliveryLocked` runs *after*
  `Deliver` returns, and the nudge deliverer blocks on the harness's PTY idle
  gate, so an ack landing inside that window is counted before its delivery is.
  Distinct mechanism from supersession and out of this item's scope.
