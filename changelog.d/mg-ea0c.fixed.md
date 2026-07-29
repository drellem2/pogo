- **The `internal/agent` workspace fixture stops manufacturing 394 git processes
  and 130 detached background git daemons to establish a commit count — and a
  fixture git failure now prints its own diagnosis instead of costing a day
  (mg-ea0c).**
  `TestFreshenWorkspaceAlertsOnDirtyStaleCheckout` failed one CI job with
  `fatal: bad tree object 018d7985…` / `error: remote unpack failed: eof before
  pack header was fully read`, and was green on rerun of the identical commit.

  **Rate, measured before any change.** **1 in 87** CI `test`-job executions since
  the test landed on 2026-07-21 — every attempt of every `CI` run enumerated; the
  window's three other `test` failures are all `TestAckHTTP`, unrelated. Locally
  **0 in 134**: 0/30 with `-count=30` in-suite, 0/96 across 8 concurrent workers
  driving a fixture mirror, 0/8 full `go test ./...` in the CI shape. A single
  green run is not evidence about a 1-in-87 flake, and neither is 134 of them —
  which is why the verdict below rests on the error text rather than on runs.

  **Not mg-d578, and not fixed by it.** That one was 10-of-15 under `-race` in
  `internal/claude` and turned out to be fixture state. This is CI-only,
  not race-flagged, in a different package, and is an object disappearing.

  **What the error text means — established by mutation, not by reading.** Four
  mutations were applied to a byte-identical rebuild of the fixture, each to the
  same object the CI log named, and pushed. A **deleted** object reproduces the CI
  pair *and prints nothing else*. Truncated to 0 bytes adds `object file … is
  empty`; 8 bytes zeroed says `loose object … is corrupt`; `chmod 000` says
  `unable to open loose object …: Permission denied`; and a clean push under
  `ulimit -n 12` **succeeds**. `open_loose_object` prints for every errno except
  ENOENT, so the silence was the whole diagnosis: **that object was absent from
  the sender's store at push time** — not truncated, not corrupt, not unopenable,
  not an fd problem.

  **Which object, and what that eliminates.** Tree OIDs here are content-addressed
  over deterministic content, so the OID is reproducible: it is the tree of
  **commit 75 of 129**, mid-loop — no boundary artifact. git is exonerated twice
  over. `gc --auto` estimates loose objects by sampling `objects/17`, which this
  fixture leaves **empty**, so it declines *even at `gc.auto=1`* (measured), and
  the publisher had 0 packs so `too_many_packs` cannot fire either. More
  decisively, by push time that tree is **reachable from `main`**, and git never
  prunes reachable objects. ENOSPC is out because all 129 commits succeeded;
  git-side fd exhaustion is out because it would have printed a line that is
  absent. **Verdict: environmental — and the deleter is NOT identified.** One CI
  log does not contain enough to name it, and that is stated as a limit rather
  than filled in with a plausible story.

  **Fixed without a retry.** A retry would convert this into a slow pass and
  destroy the evidence that something is killing git subprocesses on that runner,
  which is worth knowing regardless of this test. Instead:

  - `advance()` builds its commits with **one `git fast-import`** rather than an
    `add`+`commit` pair each. Measured for `advance(129)`: **394 git processes →
    9**, and the test drops from ~5.3s to ~0.4s. The window between writing that
    object and reading it shrinks from ~2 seconds to one process's lifetime.
  - **Fixture git no longer forks detached background daemons.** `git commit`,
    `fetch` and `push` each end by forking `git maintenance run --auto --detach`,
    which daemonizes and outlives its parent; `advance(129)` left **130** of them
    running against a repo the test was still driving. `maintenance.auto=false`
    stops the fork — `gc.auto=0` does **not**, each daemon still forks and only
    then declines (both measured). A push's *remote* half runs under the bare
    origin's own config, so that one needed the setting written into origin.
    Measured detached-maintenance trace records for `advance(129)`: **260 → 0**.
  - **A fixture git failure now names its own mechanism.** `gitFailureForensics`
    probes every OID in git's output and reports PRESENT-with-size versus ABSENT,
    distinguishing a missing object from a missing fanout directory, plus
    loose/pack counts and `fsck`. It never retries and never softens the failure.

  **After.** 30 runs of the formerly-flaky test in **13.5s** (was **159.0s** for
  the same 30), 3 clean full-`internal/agent` runs, and `./build.sh` exit 0
  including all 43 shell assertions. None of that is evidence of a cure and is not
  offered as any — the flake never reproduced locally to begin with.

  **Verified by mutation, and honest about what it claims.** Emptying
  `fixtureGitArgs` reddens the trace test (`2 records, want 0`); dropping origin's
  own setting reddens it too (`4 records`); dropping `reset --hard` reddens the
  fixture-coherence test; and making the forensics blind to absence reddens the
  discriminator test — each mutation hitting only its own test. The trace test
  carries a **positive control** that runs the same commands *without* the
  suppression and requires the daemon to appear, so it fails for "the suppression
  broke" rather than passing vacuously if git's behaviour or trace format changes.
  The load reduction **narrows exposure** — fewer processes, a shorter window, and
  this test stops being a load source for its neighbours — but it does not prove a
  cure for an unidentified external deleter, and no local run can at a 1-in-87
  base rate. Full trail in
  `docs/investigations/git-object-vanished-mid-fixture-2026-07-29.md`.

  No shipped behaviour changes: the edits are confined to
  `internal/agent/workspace_test.go`.
