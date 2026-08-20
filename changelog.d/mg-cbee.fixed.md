- **`pgrep` cannot see pogod from any agent, and `ps eww $(pgrep -x pogod)`
  therefore prints the CALLER's own environment at exit 0 — pogod now reports its
  own pid, and both halves of the trap are in the prompts a fresh worker reads
  (mg-cbee).**

  The ticket filed this as an observation and explicitly declined to claim a
  cause. The cause is `man pgrep`: `pgrep`/`pkill` exclude the calling process
  **and every one of its ancestors** unless passed `-a`, and pogod spawns every
  agent, so pogod is the ancestor of every shell an agent runs. It is not a `-x`
  versus `-f` matter — the process is filtered out **before** the pattern is
  applied. Measured 2026-08-20: `pgrep -x pogod`, `pgrep -f pogod` and bare
  `pgrep pogod` all empty at exit 1 while `lsof -iTCP:10000 -sTCP:LISTEN` showed
  pogod serving; `pgrep -ax pogod` returned the pid. Enumerated rather than
  argued — `pgrep -a .` 717 rows against `pgrep .` 712, and the resolvable
  difference is exactly the ancestor chain (`launchd` 1, `pogod` 11579, `zsh`
  22135, `claude` 69347). Nothing about it is specific to pogod: `pgrep -x
  claude` omits this shell's own parent while listing the other seven, and
  `pgrep -x launchd` returns empty from anywhere on the machine.

  **The empty result is not the harm.** An empty command substitution takes the
  argument off the command wrapped around it: `ps eww $(pgrep -x pogod)` becomes
  bare `ps eww`, which describes the caller's own processes with their
  environments attached and exits 0 — a well-formed answer to a question that was
  never asked. An architect polecat read its own harness's
  `POGO_HOME=/var/folders/.../tmp.*/home/.pogo` back out of exactly that during a
  deploy verification; it caught itself, but the artifact would otherwise have
  been a confident, well-evidenced, entirely false finding that the live daemon
  was misconfigured into a temp dir — filed the same night pogod's `POGO_HOME`
  was independently confirmed correct on both the plist and the running pid.
  Adding `| head -1` makes it worse rather than better: it discards pgrep's exit
  status too.

  **`-P` does not even fail loudly.** `pgrep -P 11579` returned **9 of pogod's 10
  children**, the single omission being this shell's own `claude` ancestor. A
  caller asking "what are pogod's children" is answered with every child except
  the branch it is standing on, at exit 0. That is the shape mg-48d8 recorded
  once — "`pgrep -P <pogod-pid>` returned nothing while pogod had 24 direct
  children" — as one of three reasons a proposed gate-liveness discriminator was
  rejected, without naming the cause.

  **An instrument, so the question has a first-class answer.** `pogo server
  status` now prints pogod's pid, and `GET /health/full` (`pogod.pid`) and `GET
  /version` (`pid`) carry it for programmatic callers:

  ```
  pogod:    ok  (mode=full, uptime=57m49s, pid=11579)
  ```

  It is served *by* pogod, which is the property the pattern match lacked: it
  cannot report a pid for a daemon that is not answering, because the request
  fails first (`pogo server is not reachable`, non-zero). A remedy is an artifact
  of the same kind as the defect, so the obvious version was checked for the same
  shape and changed: a daemon predating the field decodes to `0`, and `pid=0` is a
  plausible-looking token a reader carries into `kill` or `ps`. An unreported pid
  renders as a **named** absence with a working alternative attached, pinned by a
  test that fails on the naive implementation. The daemon-side test asserts
  `== os.Getpid()`, not `> 0`, because a non-zero pid is satisfied by any
  hardcoded number and the entire value of the field is that it names the process
  that answered.

  **Why the prompts are where this belongs.** mg-ce2c had already measured the
  ancestor exclusion and written it into `prompts/mayor.md` and
  `prompts/crew/doctor.md` — inside the *unanchored-`pkill`* bullet, framed as a
  reason a KILL will not land. The agent that hit this was a polecat, and it was
  **reading, not killing**; neither prompt that knew was in front of it. The
  `$(...)`-degrades-to-no-argument half was written down nowhere in the repo, and
  the trap was recorded in at least two agents' private memory, where a fresh
  agent cannot reach it. All six polecat templates plus mayor and doctor now
  carry both halves and the replacement, pinned by
  `TestShippedPromptsWarnPgrepIsNotALivenessInstrument` in the manner of the
  existing pkill and trapped-cleanup corpus assertions. The pin includes a sweep
  over every shipped prompt: any prompt showing a `$(pgrep ...)` capture must ship
  the `[ -n "$PID" ]` guard beside it, the same pairing the `pkill -f "^$BIN"`
  assertion already enforces — because the next prompt to grow one of these will
  not be one of the eight.

  Both new corpus arms were run against the pre-fix text: with the bullet removed
  from one template the assertion reports seven missing tokens by name. One token
  was demoted during that check — `"exits 0"` passed against a template that had
  lost the paragraph entirely (the phrase already occurs elsewhere in these
  prompts), and is replaced by `"loses its only argument"`.

  Measurements, controls and the two call sites that were audited but **not**
  cleared — `kill_tree()` in `scripts/launchd/pogo-deploy.sh` and
  `builds_in_flight()` in `scripts/launchd/pogo-reclaim.sh`, filed as
  **mg-19e4** — are in
  `docs/investigations/pgrep-cannot-see-pogod-2026-08-20.md`. Everything here is
  macOS; `procps-ng`'s `pgrep` is a different implementation and nothing was run
  against it.
