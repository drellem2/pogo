- **`pogo doctor --check` hung forever and printed zero bytes, because the
  sibling-family rule opened every subdirectory of a user's home — including the
  TCC-gated `~/Desktop` (mg-aab5).** Rule 2 of source discovery asks whether a
  directory has like-shaped neighbours: `<grandparent>/*/<basename>`. It asked
  by way of `filepath.Glob`, and Go's glob does not shortcut a meta-free final
  component — for every entry of the grandparent it does `os.Open` +
  `Readdirnames(-1)` and only then matches names. So testing for the presence of
  one name in each of ~200 directories was implemented as reading all 200 of
  them in full.

  On macOS `~/Desktop`, `~/Documents` and `~/Downloads` are gated by
  Transparency, Consent and Control: `stat` succeeds, `open(2)` **blocks** on a
  consent prompt. `STATE_DIR=/Users/daniel/.pogo/reminders-deadman` (from
  `com.pogo.deadman.plist`) has grandparent `~`, so the audit opened all three
  and stopped. In a headless agent nobody answers the prompt, and the command
  never returned. Six reproductions across three callers, one left for 16
  minutes: **0.06s of CPU consumed in that window** — a hard block, not slow
  progress. `tccd` logged `AUTHREQ_PROMPTING` for
  `kTCCServiceSystemPolicyDownloadsFolder` at the moment of each probe.

  The failure mode is what made it expensive. This is the command the repo
  documents as the first-line health check, and the first item the `doctor` crew
  prompt runs on every sweep — so a long-running crew agent drove itself into an
  unbounded block on a routine sweep. Output is printed after all checks
  complete, so a wedged check produced **no result at all** rather than a red
  one: the operator saw a blank terminal, and naming the frame took a goroutine
  dump (`[syscall, 16 minutes]` at `discover.go:236`).

  `siblingDirs` now does what its comment always claimed: one `os.ReadDir` of
  the grandparent, then `os.Stat` on each joined `<sibling>/<basename>`. Stat
  resolves a name *through* a directory without opening it, so a gated sibling
  returns an error instead of never returning. It is also strictly less work.

  **Measured on the affected host, both polarities, same command and same
  minute.** The installed binary at `b802170`: `timeout 12 pogo doctor --check`
  → exit 124, **0 bytes**. This branch: exit 1, **16,186 bytes, 0.75s wall** —
  all 21 checks reported, including the consumer-source-liveness check itself,
  which examined 2 consumers against 1,451 comparable sources and returned a
  real finding. The exit 1 is pre-existing host warnings that were previously
  unreachable, not a new failure.

  **The regression test does not depend on TCC being present**, because a test
  that only runs on a Mac with a real gated directory cannot protect this — it
  would be permanently skipped on CI, which is where this would otherwise have
  been caught. A directory at mode `0111` is the portable stand-in for the same
  structural demand: traversable but not enumerable, so `os.Stat` through it
  succeeds while `os.Open` + `Readdirnames` fails. An implementation that
  enumerates the sibling loses the peer there exactly as the real one blocked on
  `~/Desktop`. The test asserts its own premise before relying on it, skips
  under `root` (permission bits do not gate the superuser), and was confirmed to
  **fail against the old glob** before the fix was applied.

  **Residual, deliberately not folded in.** `os.ReadDir` still opens the
  grandparent itself, so a binding whose grandparent *is* a gated directory
  would still block — one open rather than ~200, and no worse than before. More
  generally, `Audit`/`Discover` walk user-controlled paths with no deadline, and
  `--check` prints nothing until every check has finished; either alone would
  have degraded this to "could not determine" instead of hanging the command.
  Both are filed separately rather than expanded into this fix.
