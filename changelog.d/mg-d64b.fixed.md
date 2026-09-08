- **CI had been red on every commit for five days because one test named a
  directory that exists on exactly one machine — and the merge gate runs on that
  machine, so it could not see it (mg-d64b).**

  `cmd/pogod`'s `TestNewStallCapacityReadsTheLiveRegistry` passed the literal
  `/Users/daniel/dev/pogo` to `Registry.RepoOccupancyFor`, which **stats** the
  path and correctly reports an absolute path that is not a directory as
  unresolvable — so `known` came back false and the test failed instantly:

  ```
  --- FAIL: TestNewStallCapacityReadsTheLiveRegistry (0.00s)
      Unresolved:"/Users/daniel/dev/pogo is not a directory on this host"
  ```

  Twenty consecutive GitHub Actions runs back to 2026-09-03, one package of 85,
  `(0.00s)` on every one. The same commit built green: a cold `./build.sh` on
  `origin/main` 499eb8a exited 0 over the same 85 packages. Not a flake and not a
  red main — a **constant**, which is the one reading re-running cannot correct.
  Twenty commits merged through a CI that was already red before any of them, and
  the pre-deploy quiesce's step (0) ("confirm CI is green on the commit being
  deployed") was unsatisfiable by anybody for five days.

  **What changed.** The test now creates the repository it queries
  (`t.TempDir()`), so it resolves on any host. Its companion
  `TestNewStallCapacityReportsAnAbsentRepoAsUNKNOWN` pins the other direction —
  a path that is not a directory answers `known = false` and names itself in
  `Unresolved` — which is the assertion the old spelling was making by accident
  on every machine but one. With only the resolvable case under test, a real
  UNKNOWN and a test written against somebody's home directory produce the same
  red.

  **The hypothesis this rules out.** Two `cmd/pogod` tests failed
  environment-dependently on 2026-09-08, and the narrower reading offered was
  that the package depends on host state that varies — a socket path, a
  registry, a running daemon. It does not, here: the dependence is on a
  **directory existing**, and `TestFallbackSocketDirIsNestedAndClaimed` passed in
  all twenty of those Actions runs, on runners with no `~/.pogo` and no pogod
  running at all.

  **`CONTRIBUTING.md` gains the third case** alongside "CI green, gate red" and
  "gate green, CI red": a test naming a dev-host path is green on the gate and
  red on CI *forever*, and neither instrument can see the other's answer. It also
  answers what step (0) should do with a red CI, which the procedure did not say:
  read `--log-failed`, name the failing test, and stop only if the failure is
  about the commit being deployed — step (1)'s cold `./build.sh` is the
  authoritative check on this box. A hard gate nobody can pass is a gate
  everybody learns to step over.
