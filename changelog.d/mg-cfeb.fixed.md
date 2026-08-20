- **A nightly fire that FAILED at the transport no longer logs "0 consecutive
  night(s) lost" (mg-cfeb).**

  `transport_streak_next` is idempotent per date, and that rule is right: this
  box fires the deploy three times a night (03:00, 04:00, 05:00), and a streak
  that counted *fires* would cross the threshold of two before sunrise. The
  consequence nobody wrote down is that if an earlier fire tonight already
  zeroed the stamp to `<today> 0` — it reached the tree, or it failed with a
  tree-fault class, or the fallback already bounced — then a **later** fire that
  fails at the transport computes 0, and the bump arm printed

      transport streak: 0 consecutive night(s) lost at the transport step

  on a run that had just failed at the transport. That is the one line a reader
  checks to find out whether the transport is in trouble, and on that run it
  read as reassurance. A wrong-looking line gets investigated; a reassuring one
  does not.

  **The count is not what was wrong and it is unchanged.** By the documented
  rule the unit is nights, and if the 03:00 fire synced then the night genuinely
  was not lost. What changed is the sentence. At a count of 0 the run now leads
  with the failure, reports the streak as UNCHANGED rather than as no-night-
  lost, quotes the record it read so the reader can go and look at the stamp,
  and names **all three** ways the stamp could already read 0 rather than
  asserting the flattering one — the record cannot distinguish them, and picking
  one would be this ticket's own defect a level down. Non-zero counts are
  reported exactly as before.

  **The same zero was in the mail, one field over.** The bump arm hands
  `fallback_bounce` the count it just computed; below the threshold that comes
  back as `FALLBACK_DETAIL`, which is logged **and** copied into the abort
  mail's `fallback:` field, where it read `0 consecutive transport-lost
  night(s)`. Both sites now go through pure helpers (`transport_streak_report`,
  `transport_streak_detail`).

  Seventeen assertions added to `scripts/pogo-deploy_test.sh`: a unit block on the
  two helpers, and a fifth end-to-end arm that seeds `<today> 0` and drives the
  real `main()` into a transport failure — the join the unit arms have to
  assume. **Polarity proved rather than asserted:** against a scratch copy of
  the runner with the old lines restored, that arm reports the defect verbatim,
  `transport streak: 0 consecutive night(s) lost at the transport step (class
  timeout)`, from a run whose own abort mail says `ABORTED: could not sync`. The
  arm also asserts the count STAYS 0, so a future "fix" that quiets the line by
  counting fires fails here.

  **Not claimed:** that this state has been observed in production. The three
  nights of 08-17..19 predate the mechanism entirely (mg-62eb), and the stamp
  currently on this box reads a clean success. What is measured is that the
  runner reaches the state from a stamp on disk and its own failed sync, three
  times a night, every night.

  Filed out of mg-62eb's completion record, where it had been noted as a narrow
  un-counted case and would otherwise have been orphaned when that item closed.
