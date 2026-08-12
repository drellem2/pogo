- **The test suites stop leaking a temp directory per package per run (mg-de3c).**
  Several packages resolve a test-mode default lazily and process-wide — the
  witness store, the event log, the claim-release store, the ghintake and
  ghteardown mg stores, `testsandbox.Main`'s sandbox root. Each was a
  `sync.Once` around `os.MkdirTemp`, each correct about never handing a test
  binary the LIVE store, and each silent about cleanup: `os.MkdirTemp` registers
  none, there is no `*testing.T` in scope to hang one on, and a test binary's
  `main()` ends in `os.Exit`, which runs no deferred function anywhere. So every
  `go test ./...` abandoned 33 directories in `$TMPDIR`, forever. Measured on
  2026-08-12: **37,083 entries and 61 GB in one `$TMPDIR`**, and it was not a
  curiosity — at 255 MiB free, `./build.sh` failed in the refinery gate with
  *no space left on device* and a healthy branch was rejected as defective
  (mg-b41f).

  Those call sites now go through `internal/testtmp`, which puts every test-mode
  directory inside **one** entry in `$TMPDIR` and sweeps it by OWNERSHIP: an
  entry whose name encodes a live pid is kept at any age, one whose owner is
  gone is removed. Ownership rather than age deliberately — this box runs
  several polecats and a refinery gate concurrently, so a sweep that guessed
  wrong the other way would delete a running suite's fixtures and surface as a
  branch defect, which is the failure being fixed arriving by a new route.

  `scripts/tmpdir-leak_test.sh` runs the ticket's acceptance criterion in the
  merge gate: count `$TMPDIR`, run a suite that creates fixtures, count again,
  unchanged. Its first assertion is the positive control — the same counting
  code shown reporting growth against a planted leak — because until that runs,
  "the count did not grow" is a claim about nothing.

  Two known leaks are left alone and named rather than quietly folded in.
  `config.AgentSocketDir`'s `$TMPDIR/pogo-agents-<hash>` fallback (3 per run)
  cannot carry a pid or a shared parent: that leaf must be derived identically
  by a test binary and by a non-test `pogod` sharing one `POGO_HOME`, and at 69
  of the 73 bytes `sun_path` allows it there is no room to nest it, and making it
  differ between test and non-test binaries is mg-8532 verbatim (filed as
  mg-a997).
  `agent.ExpandTemplateToFile` accumulates one prompt file per polecat spawn in
  `$TMPDIR/pogo-prompts` — 15,819 files and 74 MB measured — which is a
  production leak rather than a test one (filed as mg-5197).
  Reclaiming the 61 GB already on disk is likewise a separate operation from
  stopping the leak; this is the second.
