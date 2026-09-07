- **Three frozen 2026-03-20 binaries in `/usr/local/bin` — `pogod`, `lsp`,
  `pose` — replaced with symlinks, and `pogo service status` now reports the
  copies that do NOT win `$PATH` (mg-dabf).**

  An `install.sh` run on 2026-03-20 22:50 left copies of `pogod`, `lsp` and
  `pose` in `/usr/local/bin` (its default `INSTALL_DIR`). Five and a half
  months later they were still there, unchanged — `/usr/local/bin/pogod` was
  11.7 MB smaller than the live one and old enough that `--version` did not
  exist on it: it answered `flag provided but not defined: -version`.

  **Nothing was running them, and the reason was `$PATH` order alone.**
  `~/.zprofile` prepends `~/go/bin` ahead of `/usr/local/bin`, so every shell
  that sources it resolved correctly, and every instrument on the box —
  including `pogo service status`, which reads `exec.LookPath` — looked past
  three stale binaries and reported the install clean.

  **That protection was never a property of the machine.** It is a property of
  how a shell happens to be invoked, and the ticket's own hedge ("I have not
  tested a non-login invocation") resolved the wrong way when tested. Measured
  on the affected host, before the fix:

      zsh -c -l   'command -v pogod'  ->  /Users/daniel/go/bin/pogod
      bash -lc    'command -v pogod'  ->  /usr/local/bin/pogod      <- the March build
      bash -lc    'command -v lsp'    ->  /usr/local/bin/lsp
      bash -lc    'command -v pose'   ->  /usr/local/bin/pose

  A `bash` login shell gets `/etc/profile`'s `path_helper` ordering, which
  leads with `/usr/local/bin` straight out of `/etc/paths`, and the pogo
  prepend lives only in `~/.zprofile`. So this was not latent on that box: a
  shell available on it today resolved all three names to five-month-old
  builds. A missing binary errors loudly; a wrong binary of the right name
  starts, serves, and is simply wrong — which is how `mg-ce2c` happened, when a
  `pkill` anchored at `/usr/local/bin/pogod` matched nothing while `ls`
  confirmed the path existed.

  **`rm` would have been a chore, not a fix**, because `install.sh` re-places
  them; and it converts a silent wrong-version bug into a loud missing-file one
  for anything holding the absolute path. The remedy is the one already applied
  to `mg` after mg-015f — `/usr/local/bin/mg` has been a symlink to
  `~/go/bin/mg` since 2026-08-05. A symlink cannot go stale, resolves the same
  under any `$PATH` order, and an `install.sh` re-run overwrites it with a
  *current* binary rather than leaving a frozen one.

  Before removing anything, every absolute reference was checked and none was
  found: no `~/Library/LaunchAgents` plist, no `~/.pogo` file, no crontab (there
  is none), and in-repo only the `Dockerfile`'s container path, `CHANGELOG.md`,
  a `systemd` unit fixture, and `internal/agent/prompt_test.go`'s mg-ce2c guard
  — which forbids the string rather than depending on it.

  **The durable half is in `internal/selfdrift`.** `pogo service status` now
  walks `$PATH` for every binary `install.sh` installs and lists each copy that
  loses resolution, benign ones included — "we looked and it is fine" and "we
  never looked" must not render identically. A copy is benign when it resolves
  to the same file as the winner (a symlink) or carries the same revision; it is
  a hazard when it is a different build, or when it has no vcs stamp at all,
  since unknown provenance is not a reason to stop looking at it. Run on the
  repaired box, the check reports the three repairs as benign and independently
  found a fourth frozen copy nobody had named: `~/.pogo/bin/pogo`, a 2026-03-28
  build shadowed by `~/go/bin/pogo`. That one is left in place and reported.

  The scan covers `install.sh`'s whole `BINARIES` list (`pogo pogod lsp pose`),
  which is deliberately wider than the two binaries the drift axes compare —
  `lsp` and `pose` are exactly the names the two answers differ on, and nothing
  was looking at them at all. `TestInstallSetMatchesInstallScript` holds the two
  lists together so a name added to the installer cannot go unwatched again.

  It does **not** move `status`. `.drift.status` is a documented gate value
  meaning "are the three axes in agreement", and widening it would silently
  change what every existing caller of that field is asking. The finding is its
  own `shadow_hazard` field, and it is appended to `action` — the line a human
  actually reads — so a clean verdict can no longer print beside a frozen binary
  with nothing connecting them.

  **Not done, deliberately:** whether `install.sh` should itself place symlinks
  when the source is a live `go/bin` build. That is a change to shipped tooling
  rather than to one box and wants its own decision.
