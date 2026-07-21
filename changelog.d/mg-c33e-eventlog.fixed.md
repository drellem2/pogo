- **`go test ./internal/agent/` no longer writes phantom events into the live
  event log (mg-c33e).** `useTempEventLog`'s cleanup called
  `events.SetLogPathForTesting("")`, which restores the *default* path — the
  developer's real `~/.pogo/events.log` — rather than the package-wide sandbox
  `TestMain` installs. Every test that ran after one using the helper therefore
  emitted live, which is how `auto_renudge` records for a non-existent
  `cat-renudge-test` / `mg-test` ended up interleaved with production events.
  Cleanup now restores `TestMain`'s sandbox path. Measured on the pre-fix tree,
  `go test ./internal/agent/ -run TestVerifyStartAndRenudge` grew the live log by
  6 lines; after the fix it grows by 0. This is the same non-transferring-guard
  shape tracked as mg-da48 for the witness store: the guard existed, and did not
  transfer to a sibling file.
