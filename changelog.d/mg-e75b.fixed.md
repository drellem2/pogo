- **`check-progress --json` emitted `stalled:false` for a clean reading and for
  a BLIND one alike, so a consumer checking the obvious field got a green it
  could not tell from "nothing was measured" (mg-e75b).** This is mg-516e's
  defect one layer down, and it was left in place on purpose by that polecat,
  which named it in its own unverified list as the same defect class.

  mg-516e fixed the *render*: the blind paragraph opened with the clean
  paragraph's own headline — "No finding is possible from this run" against "No
  finding. What rules it out:" — so `grep "No finding"` matched both. The JSON
  had the identical collision by a different route. `stalled` is the conjunction,
  and a run that could not measure a member of the conjunction has no
  conjunction to report, so it is `false` there too. What separated the two cases
  was the *presence* of the `blind` array — which is `omitempty`, so **the
  distinguishing evidence was absent in precisely the case that looked
  healthy.** The exit codes (3 vs 0) were correct throughout and the struct
  comment said so; a careful consumer could always tell. The one who checks
  `.stalled` and stops could not, and this detector exists because of an outage
  where every signal read green while the fleet did nothing.

  `Reading` now carries **`verdict`**, emitted on every reading with no
  `omitempty`, one of `clean` / `stalled` / `blind` / `unknown`, mapping
  one-to-one onto exit 0 / 1 / 3 / 3. The healthy case is now *asserted* rather
  than inferred from an absence.

  Three things about the field are load-bearing, because a remedy is an artifact
  of the same kind as the defect it remedies:

  - **It is derived, not stored.** A second copy of a state already encoded in
    `Stalled` and `Blind` is a second source of truth that can drift, and a
    `verdict` disagreeing with the booleans would be a worse false green than
    the ambiguity it replaced. `Reading.Verdict()` computes it; `MarshalJSON`
    injects the computed value. It is therefore also correct for a reading
    decoded from a pogod that **predates the field** — the value is recomputed
    from what that daemon did send, which is asserted against a captured
    pre-mg-e75b wire body.
  - **The Go zero value does not answer `clean`.** A `Reading{}` — what a caller
    holds alongside an error — has `Stalled` false and `Blind` empty, so a naive
    derivation would call it healthy. That is the same false green one level
    further down, so an unevaluated reading answers `unknown`.
  - **None of the four tokens is a substring of another**, asserted, because
    mg-516e's failure was a `grep` matching where equality would not have, and a
    consumer piping `--json` through `grep` is the likeliest reader of this
    field.

  The human render is **unchanged** and still prints no verdict token (mg-c058:
  a state word invites a present-tense over-reading the measurement cannot
  support). Both output modes and the exit code now switch on the same derived
  value, so they cannot disagree about which state a run was in — pinned by a
  test that checks each state's headline against the verdict its JSON carries.
  Nothing in this repo consumed the field, checked before changing the schema.

  The four new package tests were run against the pre-fix code: without
  `MarshalJSON` they report `verdict=<nil>` for both the clean and the blind
  reading, and without the zero-value guard the empty `Reading` reports `clean`.
