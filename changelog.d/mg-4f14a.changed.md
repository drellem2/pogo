- **`[lineage]` covers the deploy runner: `runner_repo`, `runner_ref`,
  `runner_path`.** A host whose `~/.pogo/bin/pogo-deploy.sh` comes from an org
  template can now say so. The payload audit then compares the installed runner
  against `<runner_repo>@<runner_ref>:<runner_path>` instead of drellem2/pogo's
  copy. When the declared repo's origin is not drellem2/pogo, `pogo service
  install-deploy` refuses to replace a differing installed runner unless given
  `--force` (the old copy is still kept as `.prev`), and the nightly's runner
  self-refresh logs `runner: not-checked reason=lineage` and leaves it alone.
  `pogo config get lineage.runner_foreign` reports the answer the nightly reads.
  Without a declaration nothing is refused. The audits no longer tell you to run
  an installer over a difference they cannot attribute: the payload audit's
  stale row and the plist audit's changed-`ProgramArguments` row now state what
  differs and what `pogo service install-*` would replace, with no "run"
  instruction. The plist audit also names the keys that differ
  (drellem2/pogo#126, mg-4f14a).
