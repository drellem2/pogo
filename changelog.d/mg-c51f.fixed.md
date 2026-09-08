- **A pogo test was asserting that a defect in `pogo-reminders` was still
  present, so fixing that defect turned pogo's main deterministically red and
  blocked the merge queue for the whole repo (mg-c51f).**

  `internal/refusalwatch`'s last-hop probe drives the **deployed**
  `poll-mail.sh` — the notifier `com.pogo.deadman` executes, which lives in
  another repo with no compile-time link to this one. Its arm E constructed a
  **four-digit fractional second** because Python 3.9's `fromisoformat` rejected
  that width, and asserted the dropped grouping that followed. On **2026-09-08 at
  06:08Z** that reader defect was correctly repaired and deployed (**mg-3ba8**,
  commissioned by mg-d788's own successor), and:

  ```
  01:45Z  cold ./build.sh on origin/main 499eb8a — 85 packages, EXIT=0, GREEN
  06:08Z  the deployed poll-mail.sh gains the parse fix
  06:28Z  polecat-t3ba8 FAILED    06:35Z  polecat-tbaf3 FAILED
  06:38Z  main alone, 5 runs, 5 FAILED — deterministic, not a flake
  ```

  Same commit, green before 06:08Z and red after. **The code did not change; a
  file it depends on did.** Two branches carrying unrelated work were stranded,
  and every subsequent branch would have failed the same way.

  **What changed.** Arm E now constructs its own unreadable condition — a
  boundary of `not-a-timestamp`, which no parser turns into an instant — and
  gates on the notifier's degrade *direction*: the grouping is dropped, every
  message pages on its own, and the alarm is among them. A new **arm F** drives
  the fractional width pogod really emits (`.1234`, one of the 9.91% the old
  reader rejected) and gates on the invariant that holds under **both** readers —
  the width decides how many banners the burst becomes, never whether the alarm
  is raised — while **reporting** which behaviour it saw, so the probe's output
  still answers "which reader is deployed" without the gate depending on it.
  Measured: `NORMALISED the width and coalesced the burst` against the deployed
  copy, `REJECTED the width and dropped the episode` against the pre-mg-3ba8
  `~/dev` checkout, **both green**.

  **The rule, written down where the next arm will be added:** an arm may depend
  on what the notifier DOES, never on what it gets WRONG — a bug is the one
  property of a dependency that somebody is actively commissioned to remove.

  **And an instrument for it.** `TestTheLastHopProbeDoesNotDependOnWHICHNotifierIsDeployed`
  re-runs the whole probe against every *other* `poll-mail.sh` on the box. The
  deployed `~/.pogo` copy and the `~/dev/pogo-reminders` checkout are routinely
  different versions, and that difference is the instrument: a probe green
  against both is measuring the notifier's invariants, one green against only the
  deployed copy is measuring a release. It is the only arrangement that would
  have caught this before the deploy — pogo's gate drove exactly one copy, and
  whichever copy that was defined what "passing" meant. With fewer than two
  copies present it skips and says why.

  Not done, deliberately: the deployed notifier was **not** reverted. It carries
  a correct fix for a real defect, and reverting it to make a test green is the
  same mistake in the other direction.
