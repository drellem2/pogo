- **pogo can redeploy itself: `scripts/pogo-self-deploy`.** A merged change to the
  daemon used to be stale the moment it landed — the supervisor restarts the same
  binary from the same path and never rebuilds from source, so shipped work sat
  inert until somebody rebuilt by hand. The new driver closes that loop.
  `pogo-self-deploy check` reports three-way drift between the running daemon, the
  installed binary and the tip of the main branch, together with the action each
  gap implies, and never acts. `pogo-self-deploy redeploy` drains the fleet to
  zero, rebuilds if a build is owed, restarts, and verifies that the revision now
  running is the one it meant to ship; it refuses a fleet-killing bounce while
  polecats are working unless you pass `--force`, and a forced bounce unclaims the
  work items and collects the worktrees it orphaned.

  The daemon side is deliberately small: a drain flag that makes pogod refuse new
  dispatch with 503 while the fleet quiesces, `GET`/`POST /agents/drain` to read
  and set it, and `GET /version`, which self-reports the revision of the *running*
  process. Reading the binary on disk reports the installed revision, which stops
  being the running one the instant a rebuild rewrites the file, so the running
  side of the comparison has to come from the process itself. The driver is a
  standalone script calling only stable tools rather than a subcommand of the
  daemon it replaces, and it never triggers itself — it reports, and a person
  invokes it.
