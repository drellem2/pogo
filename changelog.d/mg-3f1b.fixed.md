- **`internal/events` is now test-safe by DEFAULT, not by remembering (mg-3f1b).**
  `resolvePath()`'s empty override fell through to `DefaultLogPath()`, so the
  ZERO VALUE resolved to the operator's live `~/.pogo/events.log`: any test that
  did not explicitly call `SetLogPathForTesting` wrote to the real audit log.
  Under a test binary an empty override now resolves to a per-process sandbox
  and the live log is **not reachable from `resolvePath` at all** — the default
  ratified at `ARCHITECTURE.md:433-447` (mg-da48), implemented at
  `internal/agent/witness.go:196` and already copied to
  `internal/ghteardown/source.go:156`. `internal/events` was the one store that
  never adopted it, which is pointed given that witness.go's own comment cites
  "the same pollutant that hit events.log".

  This is the third instance of the class and events.log's second pollution:
  mg-e06d left a permanent aggregate-contamination cutoff in
  `docs/event-log.md`, and six phantom `auto_renudge` rows reached the live log
  on 2026-07-20. Both prior fixes (including mg-c33e's helper-cleanup repair,
  which this complements rather than duplicates) were point fixes that left the
  package default untouched.

  The explicit override is unchanged and still worth setting — one test picking
  its own path is isolation from *other tests*, a different question the default
  does not answer. `""` was made SAFE, not unsayable. The regression guard in
  `internal/events/default_sandbox_test.go` carries a positive control: it runs
  the same check against a verbatim replica of the pre-fix resolver and requires
  it to FAIL, so the default can be observed to fail rather than merely observed
  to pass.
