- **`events.DefaultLogPath()` is gone; the live event log is no longer nameable
  from outside `internal/events` (mg-abbf).** mg-3f1b made `resolvePath()`
  test-safe, but the mechanism underneath was untouched: `DefaultLogPath()`
  stayed **exported** and stayed **memoised behind a `sync.Once`**. Three
  non-test callers reached it directly (`pogo events list`, `pogo events tail`,
  and the modal hook's activity tracker), so the mg-3f1b fix held only for
  callers that happened to route through `resolvePath`.

  Two exposures closed:

  - **Read-side non-hermeticity.** A test reaching any of those callers opened
    the operator's real `~/.pogo/events.log` — flaky assertions, and operator
    data surfacing in test output.
  - **The frozen-path lottery.** The memo pinned the destination at whatever
    `config.PogoHome()` was on the *first* call, for the process lifetime, so
    under a test binary that re-points `HOME`/`POGO_HOME` the destination was
    decided by **call order**. b399 moved 131 log lines between two destinations
    by skipping a single test in a single file, changing nothing else.

  `DefaultLogPath()` is now the unexported, non-memoising `livePath()`, and the
  only exported accessor is **`events.LogPath()`**, which is `resolvePath()` —
  override, then sandbox-under-test, then live. Production behaviour of all
  three callers is byte-for-byte unchanged; under a test binary they get this
  binary's sandbox. This finishes b399's recommendation #1 ("no way to express
  'the live path' from test code at all") at the source rather than at
  `resolvePath`. Dropping the memo is what turns a wrong path back into a
  *predictable* one; `filepath.Join` over an env lookup bought nothing worth a
  process-lifetime frozen global.

  **External callers:** replace `events.DefaultLogPath()` with
  `events.LogPath()`. Same signature, same production answer.

  Guarded by `internal/events/livepath_test.go`, in which every arm was
  mutation-tested to prove it can fail: re-memoising `livePath` trips the
  freeze check; disabling `resolvePath`'s `testing.Testing()` branch trips the
  path check; pointing the sandbox at `livePath()` trips the *not-opened* arm at
  the read-back (the stand-in operator log is `chmod 0000`, so any open fails
  EACCES); and making `Emit` also append to `livePath()` trips the *not-grown*
  census. The two arms are separate subtests because a `0000` file cannot grow,
  which would have made a combined growth assertion vacuous — the setup that
  makes one arm firable makes the other one dead.
