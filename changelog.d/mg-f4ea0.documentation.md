- **A release cut's polecat stops after submitting the back-port; the back-port merge closes the item (mg-f4ea0).**
  `docs/release-process.md` step 5 and the step-2 dispatch body now say the cutting polecat submits the back-port to
  main with `--author=<release-cut-item>` and stops without waiting and without `mg done`. When the back-port merges,
  the refinery closes the item itself (`completed_by: refinery`), and the coordinator only archives it. In the v0.11.0
  cut the polecat was reaped while waiting on the back-port queue, which has no bound. This supersedes the 2026-09-26
  note on mg-8382 ("the cutting polecat runs `mg done` itself").
