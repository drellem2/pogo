- **The nightly deploy now refreshes its own runner, for the next night
  (mg-cba69).** `~/.pogo/bin/pogo-deploy.sh` was a static copy that only
  `pogo service install-deploy` rewrote, so merged runner fixes did not run and
  nothing reported it. At the end of every attempting fire, after pogod and mg
  are handled, the runner compares the installed copy with
  `scripts/launchd/pogo-deploy.sh` at the commit it just synced to. If they
  differ, it installs the committed blob: it checks the hash and `bash -n`,
  keeps `pogo-deploy.sh.prev`, swaps by rename, verifies the hash at the path,
  and puts `.prev` back on a mismatch. It logs `runner: current <sha>` or
  `runner: refreshed <old> -> <new> (effective next run)`. It never touches the
  plist (a changed plist source gets its own `runner: plist ...` line) and never
  changes the run's exit status. `--runner-only` runs just this step for a
  rehearsal against a scratch `POGO_DEPLOY_RUNNER`. `install-deploy` now keeps a
  changed runner as `.prev` and swaps by rename too (Refs drellem2/pogo#123).
  The runner that is installed now predates this step, so it must be installed
  by hand once.
