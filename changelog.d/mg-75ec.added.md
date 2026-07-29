- **`pogo service status` now tells you whether the daemon is running the code
  you think it is (mg-75ec).** It prints the three-way *running / installed /
  main* revision comparison — the report `scripts/pogo-self-deploy check` has
  printed for the fleet since mg-6afa — from a shipped binary, with no repo
  script required. Report-only: it never builds, installs, restarts, or
  reconciles anything, and exits 0 either way so existing callers are
  unaffected. Gate on `pogo --json service status | jq -r .drift.status`
  (`clean` / `drift` / `unknown`), or skip it with `--no-drift`.

  **The gap was consumer-shaped.** `mg` self-installs on merge; **`pogod` does
  not.** Nothing rebuilds the binary when a change lands and nothing restarts
  the daemon when the binary is replaced, so every installation drifts from its
  own source silently. The fleet solved this for *itself* — a detector
  (mg-5701), a runner (mg-345b), and finally the nightly `com.pogo.deploy`
  redeploy (mg-42ac) — and all of it is Daniel-local. A consumer got none of it,
  and could not even *look*: the three-way lived in a repo script, so anyone who
  installed pogo with `go install` had no copy of the one thing that could have
  told them. The recommendation to ship it closed the 2026-07-23 investigation
  doc and was never filed as a work item for six days, which is its own lesson
  about where recommendations go to die.

  **No Go toolchain required.** The script reads installed revisions with `go
  version -m`, which is fine on the developer box it was written for and useless
  to someone running a release binary — the exact population this is for. The Go
  implementation parses the same build metadata straight out of the executable
  with `debug/buildinfo`, from the standard library.

  **You get an answer without a checkout, and that is the point.** `main HEAD`
  needs a source repo (`--repo`, `$POGO_REPO`, or the checkout you are standing
  in). Without one the third axis is reported unavailable *with the reason*, and
  the other two are still compared — a daemon running code that `go install`
  already replaced on disk is real drift, decidable with no repo, no git, and no
  network. Refusing to answer because one axis of three cannot be observed would
  throw away the finding a consumer is most likely to have.

  **A revision is evidence, not truth** (the lesson mg-8f09 taught the shell
  version, inherited rather than relearned). A binary with no vcs stamp, or one
  stamped with a commit the checkout has never heard of, reports `unknown` — not
  clean, and not behind; both would be claims about ancestry nothing measured.
  The unstamped case specifically does *not* get a rebuild verdict: the rebuild
  is unstamped too, so the drift would never clear, and "never equals main"
  stops being a safe default the moment it becomes permanent. `pogod` being down
  is likewise its own finding, not a stale-revision one — they send the reader to
  different places.

  **Tested against drift, not just against green.** A status command only ever
  observed clean is indistinguishable from one that prints "clean"
  unconditionally. The end-to-end tests build real vcs-stamped binaries in a
  real git repo, advance `main` past them, and drive the shipped CLI, asserting
  it reports `BUILD + RESTART owed` — with a clean case alongside so the drift
  assertion proves something. On a live box on 2026-07-29 the new command and
  `pogo-self-deploy check` independently reported the same genuine drift
  (`023fab5` running and installed, `e4a406c` on main).

  Not to be confused with `pogo service check-drift`, which compares
  `[reconcile]` *host artifacts* (plists, scripts) against their repo sources.
  Same word, different axis: that one is about files pogo generates, this one is
  about the binaries pogo is.
