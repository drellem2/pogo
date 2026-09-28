- **`scripts/check-version.sh` ran nowhere, and both it and
  `bump-version.sh` read the version with a text grep that a comment could
  match (mg-cb8dc).** At the v0.11.0 cut a comment quoting `Version = ` made the
  grep return three lines, and the bump aborted with "sed: unterminated
  substitute pattern" (mg-3225). check-version.sh had the same defect, but its
  CI job was `if: false`, so nothing ran it. Both scripts now go through
  `scripts/lib/version.sh`, which matches only the `var Version = "..."`
  declaration and refuses unless exactly one line matches. The bump rewrites
  only that line. The CI job is back on. Its old rule ("no tag exists for this
  version") failed on main between every two cuts, which is why it had been
  switched off. The rule now checks that version.go is not behind the newest
  release tag, which catches a release bump that never reached main. The old
  rule is still available as `--require-untagged`.
  `scripts/check-version_test.sh` runs in the gate and in CI. Its fixtures
  quote the pattern in comments, and against the old scripts it reproduces the
  v0.11.0 abort.
