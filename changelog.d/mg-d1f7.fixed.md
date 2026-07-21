- **The `commit-msg` hook now probes for the subcommand, not the binary — and passes loudly when
  it cannot find it at all (mg-d1f7).** mg-2627 shipped a tracked `hooks/commit-msg`, and
  `core.hooksPath = hooks` is relative, so it went live in every linked worktree the moment the
  merge landed. The CLI it calls goes live only on deploy. In the window between the two, the hook
  rejected **every commit in the repo** — benign and hazardous bodies alike, with the identical
  message `unknown command "check-commit-body"`. Identical failure on both arms is the signature
  of a gate broken by its own dependency, not one catching bad input.

  The cause was one line. The hook resolved its checker by asking `command -v pogo` — **presence,
  not capability**. A `pogo` built at 249f349 (2026-07-17) was installed, so that arm won, and the
  `go run ./cmd/pogo` arm below it — which would have worked, from source, that whole time — was
  never reached. Each candidate is now asked whether it actually *has* the subcommand
  (`check-commit-body --help`, which exits 0 when the command exists), so a stale binary falls
  through instead of winning. The gate self-activates from source; no redeploy needed, nothing to
  remember and nothing to undo.

  Two exit codes made this sharper than it looks: an unknown command and a real finding both exit
  1, so the probe has to be a separate side-effect-free invocation — you cannot tell them apart
  after the fact. Reading the exit code through a pipe hides it further; `./hooks/commit-msg f |
  head` reports `head`'s status, and an absence reads as a pass.

  When **no** route exists, the hook now prints a warning naming the deploy state and **exits 0**.
  A gate whose dependency is missing must not block work; it must say so. The old failure text
  sent the reader to re-read their commit message — the one place the problem was not. The warning
  also states plainly that a hazardous body would pass right now too, because a check that is not
  running should not be mistaken for a check that found nothing. The refinery still gates the merge
  either way.

  Tested by executing the hook, which is the coverage mg-2627 lacked: a stale-`pogo` shim on PATH
  must fall through and still *reject* the real e83f394 wrapped-ref specimen while *accepting* an
  ordinary `Refs drellem2/pogo#89` body, and a PATH with no route at all must pass both with the
  warning. All four cases are asserted. Only checking that bad input is refused is precisely how
  a gate that refused everything shipped.

  Cost: when the source arm is taken, the probe and the check are two `go run` invocations, about
  a second once the build cache is warm. `./build.sh` populates `bin/pogo` and takes the first
  arm, which is free.
