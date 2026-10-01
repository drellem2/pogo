- **The merge gate no longer leaves a `pogo-gate-profile.*` file in `$TMPDIR`
  when it ends inside its first step.** `scripts/lib/gate-profile.sh` created
  its `times` scratch file in `gate_profile_begin` and removed it only at the
  bottom of `gate_profile_report`, after that function's "no steps recorded"
  early return. So a gate killed (SIGTERM/SIGINT/SIGHUP) or exiting before its
  first step was recorded left one file per run. 410 of them had built up by
  2026-10-01, each holding the near-zero CPU reading taken before step one.
  Callers with no EXIT trap, or that called `gate_profile_begin` twice, leaked
  the same way. Now each CPU reading creates, reads and removes its own file,
  so no exit path depends on the caller's trap. `scripts/gate-profile_test.sh`
  Test 10 checks that the file count in a scratch `TMPDIR` is unchanged after
  each exit path (mg-c1a5e).
