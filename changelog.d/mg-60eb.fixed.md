- **The $TMPDIR leak guard now measures the WHOLE TREE, and two test setup
  helpers stopped leaking a directory per run (mg-60eb).** On 2026-08-13 this
  host's data volume reached 100% — 204 MiB free of 460 GiB — and `./build.sh`
  died mid-suite with `OSError: [Errno 28] No space left on device` on a temp
  fixture. That fails **every merge gate on the box**, and it fails them for a
  non-reason: the suite never reached a decision, so nothing about the branch
  was established. It is also not attributable from the failing MR — the gate
  that dies is whichever one happens to be running when the disk crosses, so it
  presents as a random branch defect. ~5,000 fixture directories in the per-user
  temp dir carried our own test-harness prefixes; 3,160 were older than the
  running pogod, so no live process could have owned one.

  **The leak was ours and the guard for it already existed.** mg-de3c landed
  `scripts/tmpdir-leak_test.sh` after the same failure in July, and it works —
  on the five packages it names. Its slice was chosen as "every
  `internal/testtmp` caller", which is the set of packages mg-de3c's own fix had
  just touched, so its **coverage was a property of the fix rather than of the
  tree**. `cmd/pogo` and `internal/refinery` were outside it, and both went on
  leaking one directory per run.

  Both of those are `TestMain`s, and both already called `os.RemoveAll` — after
  `m.Run()` returns. That is the path where the process reaches the bottom of
  `TestMain`. A panicking test does not, a `-timeout` expiry does not (Go
  implements the timeout **by panicking**), and a test binary killed by the gate
  or by `pogo agent stop` does not. **The failure path is the point**: a helper
  that cleans up only on success leaks exactly when tests are failing, which is
  when they are run most. Both now take their directory from
  `internal/testtmp.Dir`, which nests it under one swept root and reaps it by
  pid ownership — so removal no longer depends on any code being reached.

  **And the nest itself was not being reclaimed — 148 roots, 120 MB, selected
  for removal on every sweep since mg-de3c and removed by none of them.** A
  sandbox root under `internal/testtmp` is a fake `$HOME`, and any test in it
  that shells out to `go build` populates `$HOME/go/pkg/mod`. Go writes its
  module cache **read-only** — 0444 files inside 0555 directories — so
  `os.RemoveAll` returns EACCES at the first one and stops. Both the sweep and
  `testsandbox.Main`'s teardown ignored that error, correctly for a sweep that
  must never fail a test and disastrously as a reclaim: an entry that cannot be
  removed was indistinguishable from one that had been. mg-de3c's guard reported
  the root steady the whole time, because none of the five packages in its slice
  builds anything and so none of them ever writes a module cache — the same
  coverage gap, in its second form. `testtmp.RemoveAll` now retries once, after
  restoring owner write permission inside the condemned tree. Running one
  package with the fix took `$TMPDIR/pogo-test-tmp` from 148 entries and 120 MB
  to 1 entry and 4 KB.

  **The guard now rides on the run the gate already pays for.** The whole-tree
  `go test ./...` in `test.sh` is wrapped by `scripts/tmpdir-leak-guard.sh`,
  which pins `$TMPDIR` to a private directory, reports by name anything the run
  abandoned there, and removes it. Coverage is every package in the tree,
  including packages that do not exist yet, at **no additional test time** — a
  leak in a package nobody thought to list now fails on the day it is written.
  The report names the remedy too, because `defer`/`t.Cleanup` is the fix that
  produced this ticket.

  The rule is an allowlist of **names**, not a before/after count. A count needs
  two runs over one `$TMPDIR`, and a `$TMPDIR` reused across gate runs cannot
  work on this box — several gates and polecats run at once and would count each
  other's fixtures. So the run is cold and the four permitted entries
  (`pogo-test-tmp`, `pogo-prompts`, `pogo-agents`, `go-build*`) are each a fixed
  name that cannot grow with the number of runs. Anything carrying an
  `os.MkdirTemp` random suffix is per-run growth, and fails.

  **A signalled gate still reports its own failure, not the leak.** This
  incident was an environmental failure read as a verdict about a branch; a
  wrapper that reported "leak" in place of the wrapped suite's own red would be
  the same substitution with the arrow reversed. The suite's exit status wins,
  and the leak is listed as a note underneath it.

  The wrapper is subject to the defect it remedies, so it was checked against
  it: its own removal rides on a trap, and nothing rides through SIGKILL — which
  the refinery does use on a gate it has given up on. Its private `$TMPDIR`
  therefore carries the owning pid, and each run sweeps siblings whose owner is
  gone while leaving live owners' alone, both directions asserted (a sweep that
  removed everything would delete a concurrent gate's fixtures). Thirteen cases in
  `scripts/tmpdir-leak_test.sh`, of which the load-bearing one is the positive
  control: an allowlist only ever seen letting things through is not known to
  stop anything.

  Not addressed here, and named rather than left quiet: the other 34 GB in that
  temp dir (`move-*`, `tmp.*`, ~3,000 directories) is of unknown provenance, is
  not ours, and was not deleted; and the same leak shape is live in two sibling
  repos — `bridget-*` (627 directories in four minutes) and `mg*-` (~1,000 in
  two hours) — which this branch cannot reach.
