- **`internal/scheduler`'s `TestAckHTTP` no longer rots against the wall-clock date — the ack
  handler's notion of "now" is injectable (mg-a35b).** The test hardcoded a fire time of
  `2026-07-22` and acked it through the HTTP handler, which stamped the ack with `time.Now()` and
  compared its age against a real 24h freshness window (`AckStaleWindow`). It passed the day it was
  written (mg-a754) and returned **409, want 200** every day after, because the fixture aged past
  24h against a clock the test did not control. `build.sh` gates on `go build ./cmd/...`, not
  `go test`, so this never froze merges — but main was red to anyone running the suite, and a latent
  landmine if the gate ever adds `go test`.

  This is the controls-must-not-hardcode-a-fact class, same family as mg-2894/mg-4e12 (commit SHAs)
  and mg-4e12 (clone depth): the fixture carried a fact about *when the test runs* rather than about
  the code under test, and local verification the day it was written could not detect the rot. The
  tempting wrong fix — bump `2026, 7, 22` to today — reproduces the bug tomorrow with a fresh
  hardcoded date.

  Fixed by making the seam, not the date, control time. `Scheduler` grows an injectable clock
  (`now func() time.Time`, defaulting to `time.Now` in `New`, overridable via `SetClock`); the two
  HTTP handlers that stamped requests with `time.Now()` — `handleAck` and the `POST` add path — now
  read `s.clock()` (locked, race-safe against `SetClock`). Production is unchanged. `TestAckHTTP`
  pins the clock to its fixture's `base`, so `base` is now an arbitrary label, not a live calendar
  assumption, and the 200 assertion holds on any date. A new `TestAckHTTPFreshnessWindow` proves the
  window is still enforced in both directions — 200 inside it, 409 past it — with the out-of-bounds
  age constructed by *advancing the injected clock*, never by the machine's date drifting.

  The class, not just the instance, was removed: `completion_test.go` is the only test that reaches
  the ack path through the real-clock handler, and the two `time.Now()` call sites in `api.go` were
  the only handler wall-clock reads that a fixed-date fixture could rot against. Every other
  `time.Date(...)` in the package's tests feeds a `now` parameter into a pure function (`Ack`,
  `Tick`, `buildBody`) and cannot rot. No absolute date, SHA, or wall-clock assumption remains in
  either ack-handler test. Verified: `go test ./internal/scheduler/` was RED today (2026-07-23,
  already >24h past the fixture) and is GREEN after; both ack tests pass under `-race`.
