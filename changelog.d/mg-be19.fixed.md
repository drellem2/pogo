- **The completion notice no longer asserts that a worker recorded no verdict on
  the strength of one read taken at the close (mg-be19).**
  `internal/filernotify` emitted, to the agent that commissioned the work:

      Verdict: NONE RECORDED — the item closed with no readable result sidecar

  It is a real read — the package genuinely inspects the store, which is what
  separated it from the three other false-absence instruments measured the same
  day — but it reported the *reading* as a property of the *work*. The failure
  direction is false-absence: a filer concludes its worker produced nothing,
  when the verdict exists and is on disk.

  **What the two reported instances actually were, measured.** pm-onethird found
  `mg-30bd` (notice 16:54:11Z, sidecar on disk 16:55Z) and `mg-6e4f` (11:41:19Z,
  sidecar 11:44Z) and read them as a race between the notice and the sidecar
  write. The event log says otherwise, and the distinction changes the fix:

      mg-30bd  16:54:11.339  work_item_completion_notice   route=merge, sent
               16:54:12.963  work_item_claim_released      reason=agent_stopped
               16:54:30.044  stall_watch_fired             mg-30bd unclaimed, high

  The close **failed**; the item went back to `available/` and stall-watch
  advertised it 19 seconds later. There was no sidecar because there had been no
  close — which is mg-2b71's defect, already fixed on `main` and already worded
  for (`Verdict: NONE — mg writes the result sidecar at the close, and this item
  did not close`), and **not yet deployed**: the running pogod is 082ec38, which
  carries mg-f120 and neither mg-2b71 nor mg-da12. Both reported instances are
  therefore fixed already, by a commit that is merged and not running.

  **The race the ticket names is nevertheless real, and now measured.**
  `mg done --result` makes the item visible in `done/` *before* its sidecar lands
  beside it. Over 15 sandboxed closes with a 400 KB result: observed in **14 of
  15**, median window **0.14 ms**, max 0.42 ms. That is the whole of it — 0.14
  milliseconds, not 87 seconds. An observer that reads "this item is done" from
  the store and then reads the sidecar can land inside it; pogod's merge route
  cannot, because it calls `mg done` synchronously and holds the sidecar it
  wrote, but the done-reaper's poll can.

  **So the change is to the claim, not to the ordering.** The notice now reports
  what it observed, says what that does and does not establish, and hands the
  reader the one command that settles it:

      Verdict: NOT READ — this item closed, and no result sidecar was readable for it at the
               moment this notice was composed (see the Date: header). ...
               The likeliest cause is still a worker that skipped `--verdict-file` at
               submit (mg-dfea) — a real absence — but this notice cannot tell the two
               apart. One command settles it, and it looks in the archive too:

                   mg sidecar mg-XXXX

  The true case stays readable: a worker that skips `--verdict-file` leaves
  exactly this shape and is still by far the commonest cause, so mg-dfea is named
  rather than buried. `mg sidecar` is the right referral because it resolves the
  item and reads the sidecar beside it wherever the item now lives — verified
  against both a `done/` item and an `archive/2026-08/` one — which is precisely
  the lookup the day's other three false absences got wrong.

  **A second false-absence source in the same function, fixed with it.**
  `resolveResult` returned `""` when the store read *failed*, so a store pogod
  could not read reached the filer through the same wording as a worker that
  recorded nothing — a failure of the instrument rendered as a fact about the
  work, which is this ticket's defect one layer in. Its own doc comment claimed
  the error was "folded into the returned string as a legible statement"; it was
  not. A failed read is now reported as `Verdict: UNREADABLE`, carrying the error
  itself, and it no longer discards the caller's copy of the sidecar — the merge
  route hands in the one it just wrote, and throwing that away because a separate
  read failed loses the only verdict in hand.

  The read error is deliberately **not** part of the dedup key: a read that
  failed established nothing, so the next observation of the same completion is
  free to try again and say something better.
