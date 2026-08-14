- **The ownership/wedge positive control is recorded as SPENT, and the launchd
  residue is stopped from becoming its replacement — in the detector, not only
  in prose (mg-bb68).** pm-pogo ruled on 2026-08-07 17:52Z that the arc's
  positive control was SPLIT and that one half was spent. The half that needed a
  live Emacs-child pogod holding `:10000` against a spawn-scheduled launchd job
  **self-resolved at 2026-08-07 17:37:28Z**, before any demonstration was
  dispatched at it: Daniel's Emacs exited, launchd bound the port. Not a window
  missed through inaction — it closed by accident while the ruling sat unread
  during a fleet restart. It was the **third** control spent unused on this arc
  (mg-fc99, recorded by mg-8dcb, was an earlier one), and a spent control that is
  not written down is one nobody can count.

  `docs/investigations/ownership-wedge-control-spent-2026-08-07.md` is the
  record, re-measured from the primary source rather than inherited: **19,274**
  `Cannot acquire pogod lock … held by pid 4368` lines in `pogod.log.1` and
  **zero** in the current log, the last preceded by `18:37:18` local and the win
  at `18:37:28` — so the ticket's `17:37:28Z` and mg-fa79's `18:37:28` are one
  instant, not a discrepancy.

  **The residue is evidence the wedge existed, not that a detector fires on it.**
  `runs` read 24976 at filing and **24991** a week later on a box that was
  supervised throughout, and `last exit reason = OS_REASON_CODESIGNING` still
  sits on a healthy daemon. Both are lifetime fields; one sample of one cannot
  name a night.

  **The detector shipped anyway and honoured an instruction it never saw.**
  `internal/supervision` (mg-fa79) was merged six days later by a polecat who had
  not read the ruling, and proves itself on **constructed `Observation` literals**
  rather than on the host. Checked rather than assumed, per the merged-is-not-
  running rule: `git merge-base --is-ancestor 107f6b2a 082ec38b` exits non-zero
  and the installed CLI has no `service supervision` subcommand — the detector is
  on `main` and not in the running daemon. Built from the branch it reads
  `SUPERVISED … (pid 77880)`, which is both the point and the loss: the only
  state it can now be pointed at is the healthy one.

  **What the spent control actually cost, named precisely.** `Check` across every
  distinguishable state, the two residue-field guards, `ParseLaunchctlPID`'s
  no-live-pid case and `Observe` on an absent job are all already proven. The
  single irreducible gap is the **join** — nothing shows `Observe` reading a
  loaded job with no live pid *beside* a live rival holding the lockfile. One
  seam, not a hole. Naming it is worth more than mourning the control.

  **And one place the residue could still have been written up as the
  demonstration was the detector itself.** `Check` calls the post-wedge shape —
  loaded job, no live process, no live holder, exit reason still set — `UNKNOWN`
  only because `JobLoaded && !LockPIDOK` is evaluated *before*
  `JobLoaded && !JobPIDOK`. **Nothing pinned that ordering.** Measured, both
  arms: swapping the two cases leaves the package's entire pre-existing suite
  **green** and makes the residue shape report `UNSUPERVISED: … pid 0 owns this
  POGO_HOME and is serving … a wedged pid 0 would never be restarted` — a
  confident verdict naming a process that does not exist, produced from a job
  that is merely idle. Nothing outside the package would have caught it either:
  `Check`'s only two call sites are in `cmd/pogo/main.go` and have no Go test,
  and `scripts/pogo-self-deploy_test.sh` asserts an `UNSUPERVISED` verdict
  against a **stub CLI that echoes a canned line**.
  `internal/supervision/residue_test.go` closes it with two guards that pass on
  the code as it stands and **fail on the swap**, so the guard has been shown able
  to fail.

  Not spent, and not to be conflated: the staleness half (**mg-6da3**) was live
  and was used properly, as a controlled pair whose two arms are required to
  disagree (`internal/driftwatch/revision_test.go:710`).

  Reproducing the condition on purpose remains possible — `pogo.el` spawns pogod
  with no port check (mg-2def), so a fresh Emacs session recreates it exactly —
  but it is **fleet-destructive** and is Daniel's call. A cheaper non-destructive
  synthesis is recorded in the investigation and deliberately not taken.
