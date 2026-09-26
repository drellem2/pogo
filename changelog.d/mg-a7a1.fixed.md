- **pogod no longer dies ~30 s after a non-launchd start because its output was on a pipe nobody read any more (mg-a7a1).**

  The second pogod spawner in the 2026-09 outage was `pogo`'s own auto-start,
  `client.StartServer`. Every `lsp`, `pose` and `pogo visit` reaches it when
  pogod is down, and the shell integration runs `pogo visit` on every `cd`.
  It captured pogod's stdout and stderr on a pipe that the CLI read, and the
  CLI exits as soon as pogod answers. pogod's next log line then killed it
  with SIGPIPE, along with every agent it had started. Measured against the
  deployed binaries: an old `pogo visit` with an old pogod left fd 2 on a
  PIPE, and the daemon died on its next write. Emacs's `pogo-start` also used
  a pipe, which it read only until Emacs exited.

  - `StartServer` now spawns pogod onto `~/Library/Logs/pogo/pogod.log`, with
    stdin on `/dev/null`. Early-exit errors are still reported, read back from
    what this spawn appended to the log.
  - `pogo-start` in Emacs spawns onto `pogo-server-log-file`, whose default is
    the same file.
  - pogod itself checks, before its first write, whether its stdout or stderr
    is a pipe. If so it re-points both at its log and logs the parent that
    started it (`grep 'was a PIPE' pogod.log`). A spawner nobody has found yet
    can no longer take the daemon down this way.

  `docs/operations.md` ("Who can start pogod") lists every spawner and where
  its output goes.
