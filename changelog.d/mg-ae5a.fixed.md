- **`TestScan_LiveIncidentTranscripts` read the developer's live session
  transcripts and its skip guard could not see that the incident fixture had
  rotated away — `main` went red, and with it EVERY merge request in the fleet
  (mg-ae5a).**

  The test asserts the synthetic-failure detector against the untouched
  transcripts of the 2026-07-22 outage, at
  `~/.claude/projects/-Users-daniel--pogo-agents-pm-pogo/*.jsonl`. It skipped
  only when `Locate` found **zero** files for that agent. On 2026-09-03 it found
  **22** — every one of them newer than the incident, the oldest record
  `2026-08-04T19:49:29Z` against a window ending `2026-07-22T22:30:30Z`. So the
  guard answered "the fixture is present" for a directory that no longer
  contained it, and the test flipped from skipped to FAILING on the day the
  transcripts rotated out. It is a detector whose own presence check cannot see
  the thing it is checking for (p7ce7's phrasing, and it is exact). Reproduced 3
  of 3 runs, including from a pristine `git archive main` export.

  Each sub-test now guards on **its own asserted window**: `pm-pogo` requires a
  record inside the window it asserts about, and skips naming that window and
  the span actually on disk. `doctor` is the negative control for an agent that
  wrote NOTHING in the window, so "a record inside the window" is unsatisfiable
  by construction there; it requires instead that some single doctor transcript's
  own record span straddles the window — i.e. that a file from that era is still
  here. The window was NOT widened to match today's transcripts, and no test was
  deleted or unconditionally skipped: both would change what is asserted, and an
  unconditional skip is how the next decay goes unnoticed.

  Two of the three sub-tests had been **passing vacuously** the whole time —
  `StateQuiet` over an empty window is trivially true — including the one whose
  window is supposed to hold 63 real model turns. They now skip honestly rather
  than reporting green about a window with nothing in it.

- **Gave the new guard a positive control, because its job is to say NO
  (mg-ae5a).**

  A NO from a broken instrument is indistinguishable from a true NO, and its
  consequence here is a permanent silent skip — the exact outcome the fix exists
  to prevent. `TestMeasureTranscripts_SeesAWindowItIsGiven` runs the guard
  against the checked-in `auth-expired-2026-07-22.jsonl`, whose six records and
  their timestamps are known on every machine, and then against a window ten
  days away to prove it can also say NO. A harness that renames its `timestamp`
  field now fails loudly in CI instead of quietly disarming the live check.

  The guard reads record timestamps only — never `Scan`, never the
  synthetic-turn signature. A guard that asked the detector whether it had found
  anything would skip whenever the detector broke, and a broken detector is the
  one answer that must stay a failure. It scans raw bytes rather than reusing
  `readLine`, whose `maxLineBytes` cap drops the megabyte-scale assistant turns a
  real transcript is full of; inheriting that cap would report a window packed
  with large records as empty, which is the same false absence one layer down
  (`TestMeasureTranscripts_ReadsRecordsLongerThanTheScannerCap`).

  Scope note: mg-5551 records **48** test suites still reading this developer's
  live state. This is one instance of that class, and the class already has a
  ticket. A second instance surfaced the same evening — `cmd/pogo`'s
  `TestShippedPromptsMatchTheCLISurface` execs `/Users/daniel/go/bin/mg` at a
  hardcoded absolute path and failed with `bad file descriptor` under host load
  16.6 while passing at load 6.59 — and is deliberately NOT fixed here: it failed
  1 of 3 runs where this failed 3 of 3, and an intermittent failure cannot be
  verified by the same gate run that verifies a deterministic one. (Load
  correlation measured by p7ce7; not re-derived here.)
