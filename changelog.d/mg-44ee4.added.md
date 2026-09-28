- **The nightly deploy now installs the fleet's `mg` from macguffin
  `origin/main` (mg-44ee4, pm-pogo decision (a)).** Once mg-e42de removed the
  side-effect installs, nothing updated `~/go/bin/mg`, so merged macguffin fixes
  were not live and nothing reported it. `pogo-deploy.sh` now builds from its
  own clean checkout (`~/.pogo/deploy-src-macguffin`) using
  `./build.sh --install` only, never tests. It installs to a staging dir,
  keeps `mg.prev`, and swaps by rename. It verifies `mg version` and a
  read-only `mg list --json` at the installed path, and restores `mg.prev` if
  either fails. The step runs after pogod's outcome is recorded and never
  changes the run's exit status. Every attempting fire logs
  `mg: installed <sha> (origin/main <sha>) prev <sha> result=...`.
  `--mg-only` runs just this step, which is useful for a rehearsal against a
  scratch `POGO_DEPLOY_MG_GOBIN`.
