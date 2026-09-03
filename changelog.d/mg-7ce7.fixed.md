- **`scripts/revision-probe.sh --mail` could not deliver: `pipefail` + `| grep -q`
  at all THREE tool resolvers, one failing 10/10 and two passing on timing luck.
  55 correctly-computed staleness alerts reached nobody (mg-7ce7).**

  `grep -q` exits on the first match, the producer takes SIGPIPE and exits 141,
  `set -uo pipefail` makes 141 the pipeline's status, `|| continue` fires, and a
  capability probe reports a WORKING TOOL AS ABSENT. `mg --help` writes 2404
  bytes out of a 7.7MB Go binary and lost 10/10; the probe then printed "no
  macguffin 'mg' was found — refusing bare 'mg'" and exited on an alert nobody
  received. The suppressed finding was the one that mattered: *pogod has not been
  redeployed for 5d2h*.

  **The pattern is not refuted; the idiom is.** The probe delivered three real
  alerts on 08-16 and 08-17 and then went silent mid-incident **with no code
  change** — last delivery 2026-08-17T23:20Z. A component that never worked gets
  caught the first time anyone looks; one that worked, was cited as precedent for
  having worked, and then stopped is caught by nobody.

  **All three call sites are fixed, not the one that was red.** `git --version`
  (~25 bytes) and `curl --version` (~25 bytes) measured 0/10 — the identical
  defect, winning the race, one grown binary or one loaded box from this probe
  announcing "no working git found" about the git in its hand, which is a worse
  failure than the mail one because git and curl are how it does its job. Each
  resolver now captures output and matches with `case`: no pipeline, no second
  binary, and the anchoring made explicit (`^curl ` became a prefix pattern).

  **Section 13 of `scripts/revision-probe_test.sh` guards all three the way a race
  has to be guarded.** Chatty stub `git`, `curl` and `mg` that print the identity
  line and then far more than a pipe buffer holds; the FIRST assertion is that the
  old idiom really does return 141 against each fixture, so the section cannot
  pass by certifying timing luck. That is exactly how this suite stayed green over
  the defect: its stub `mg` echoed one short line and never lost the race.

  **The git and curl cases assert SELECTION, not survival — the first draft of
  them passed against the defect.** `resolve_git` walks a candidate list, so a
  wrongly-rejected `$GIT` does not fail the run; it falls through to
  `/usr/bin/git` and the probe reaches its verdict anyway. The stubs now record
  every delegated call and the assertion is that the handed candidate did the
  work. Verified red against the pre-fix resolvers: 4 failing assertions, 42
  passing; green after: 46 passing, 0 failing.

  **The audit found no other live site — but this ticket's own triage rule was
  wrong, and it is corrected in CONTRIBUTING.md.** The rule said a shell BUILTIN
  producer is safe at any size, so `printf | grep -q` sites need not be examined.
  Measured as a two-stage pipeline that isolates the builtin (match at byte 0, 20
  runs each, bash 3.2 and zsh alike): 0/20 at 8KB, **20/20 at exit 141 at both
  64KB and 256KB**. The predicate is the PIPE BUFFER, not the producer's class —
  whoever is writing dies if bytes remain when the consumer exits.
  `changelog.d/mg-712e.fixed.md` had repeated the false generalisation and is
  corrected in place; its local reading (0 of 1,200) was a true measurement of a
  three-stage pipeline in which the middle `grep` is external and is the process
  that dies. The live `printf | grep -q` sites in this tree stay safe under the
  corrected rule, by payload size rather than by producer class.

  `scripts/launchd/pogo-deploy.sh` has four sites of the same shape and is NOT
  affected: it never sets `pipefail`, so only `grep`'s status is read.
