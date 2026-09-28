- **The coordinator, not the cutting polecat, closes a release cut (mg-f4ea0).** `docs/release-process.md` step 5 and
  the step-2 dispatch body now say the cutting polecat submits the back-port to main and stops without waiting, and the
  coordinator runs `mg done` on the release-cut item once the back-port MR merges. In the v0.11.0 cut the polecat was
  reaped by the defer-done backstop while waiting on an unbounded back-port queue. This supersedes the 2026-09-26 note
  on mg-8382 ("the cutting polecat runs `mg done` itself").
