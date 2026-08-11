- **The build-PR template stopped telling the builder both things about `mg done`,
  and the reviewer stopped waiting on a counterparty it never checked was there
  (mg-e7cb, drellem2/pogo#131).** The closing `FAILURE MODE` paragraph of
  `polecat-build-pr.md` contradicted **itself**: "if you ... skip `mg done`, the
  work is lost ... only you can close the item", and three sentences later, in the
  same paragraph, "Calling `mg done` before the coordinator confirms the merge is
  also a failure." A builder could cite it whichever way it went, and the half
  that is false on this track was the last thing it read.

  It is rewritten as a whole rather than deleted — the correct prohibition lived
  in that same paragraph, and so did the coordinator-confirmed exception; a line
  excision would have taken both with it. The invariant is now also stated at the
  **PR-open step**, which is where the builder is standing when it makes the call.

  Why this is not bookkeeping: pogod's done-reaper stops any polecat whose work
  item reads terminal after two minutes of PTY quiet, so a self-closed builder is
  gone before the reviewer's findings land. Measured over 17 real between-round
  waits in this fleet's mail: median 8.3m, longest 20.0m, and **15 of 17 above the
  two-minute grace** — the reap is not an edge case in this loop, it is the
  expected outcome.

  `polecat-review.md` now checks the counterparty is alive before entering the
  untimed between-rounds wait (`mg show <build-ticket> --json` for the item state,
  `pogo agent list` for a running process), and routes a failed check to the
  coordinator instead of polling. The reporting reviewer did exactly this on its
  own initiative, which is the only reason its round did not stall silently.

  One trap the new text names explicitly, because it was measured rather than
  assumed: on a missing item `mg show --json` writes its error to **stderr** and
  exits 3, so `mg show ... | jq -r .status` yields an empty string and the
  pipeline still exits **0** — that is `jq`'s status, not `mg`'s. A check that
  branches on the pipeline's exit code cannot see this failure at all.
