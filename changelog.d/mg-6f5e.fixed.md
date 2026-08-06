- **`pogo refinery` timestamps render as labelled UTC across `history`, `show`
  and `queue`, and an unresolved entry says so instead of printing a year-one
  date (mg-6f5e).** Reported as drellem2/pogo#109: on a Europe/London host,
  `refinery history` printed `done=04:06` for a merge that really happened at
  `02:06:27Z`. A reader who assumed the bare digits matched the shell's `+0100`
  decoded them as an hour **in the future** relative to a `02:11Z` "now" — which
  reads as an impossible clock or a bogus merge record, not as a missing label.
  That is a bad failure mode for the one surface a coordinator uses to confirm
  that work actually shipped.

  **It is broader than reported, and does not need the `+0200` the reporter
  suspected.** A Go `time.Time` renders in whatever `Location` it was
  deserialized into, and the two `refinery history` paths deserialize
  differently: the retained window is unmarshalled from `refinery-state.json`,
  whose RFC3339 carries the offset it was *stored* with, while `--since` is
  reconstructed from `events.log`, which is written `.UTC()`. Formatted with a
  layout carrying no zone designator and no normalisation, the command therefore
  disagreed with **itself** by an hour, through a documented flag, on any
  non-UTC host:

      pogo refinery history            -> done=2026-08-04 21:17
      pogo refinery history --since=2d -> done=2026-08-04 20:17

  Same merge request, same host, one hour apart.

  **Z-suffixed UTC rather than the explicit local offset the report also
  offered.** A `Z` timestamp cannot be misread by a reader in any zone; a local
  one is unambiguous only to someone who already knows the host's offset, which
  an agent, a log reader, or a future reader at a different offset does not. UTC
  is also what the artifacts a reader correlates these against are already in —
  `events.log`, the refinery mail-item epoch ids, `auditsuccessors.go` — so it
  removes the arithmetic instead of relabelling it. `.Local().Format(RFC3339)`,
  matching `schedule list`, was considered and rejected: it widens a fixed-width
  table column by ~11 characters and leaves the reader converting.

  **The whole `refinery *` family, and no further.** `history`, `show` and
  `queue` all move; a history-only fix would leave the family half-labelled, and
  a reader who learns "refinery prints UTC now" will confidently misread
  whichever surface was left bare — worse than today's uniform distrust.
  `--json` is deliberately untouched and was never defective: it emits RFC3339,
  which carries the offset.

  **A zero time renders `-`, not `0001-01-01`.** The `--since` path is the only
  one that emits non-terminal rows, and a `StatusProcessing` row has no done
  time at all. It was printing a year-one date, which reads as a corrupt record
  rather than as an absent one — the same class of wrong conclusion the missing
  zone label produced.

  **The tests assert the disagreement, not the suffix.** A test that only
  checked for a trailing `Z` would pass on output that still disagreed with
  itself, so the fixtures build the *same instant* in the two `Location`s the
  two history paths actually produce and require the rendered rows to be
  byte-identical. They also assert the digits are not merely relabelled: if the
  `.UTC()` conversion were dropped the layout would still print a `Z` and the
  digits would still read `21:17`, which is worse than printing them bare
  because the label would assert something false. The history row was extracted
  into `formatHistoryRow` to make the line a reader actually sees reachable from
  a test.

  **The mg-0235 waiver is deleted, which is the event it was written for.** That
  recurrence check held these five calls in a `gh109Waiver` map while this fix
  sat at a human go/no-go gate, and it was built to be *exhausted* rather than
  respected — an entry matching nothing fails the test. With the fix landed the
  map is removed rather than emptied: a waiver map with no entries is an
  invitation to add one. Every rendered time layout in `cmd/pogo` now carries a
  zone designator with no waived lines.
