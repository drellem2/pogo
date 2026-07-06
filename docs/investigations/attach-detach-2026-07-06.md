# `pogo attach` detach never worked — Ctrl-\ was a no-op — 2026-07-06

**Origin:** Daniel reminder (Apple Reminders / Pogo list, 2026-07-06T12:52Z),
mg-5be3:

> "issue with detaching from pogo shell. Noticed in Claude Code, maybe a pty
> bug."

No repro steps were given. This is the root-cause trace for that report; the
fix ships in the same PR (unlike the read-only
[2026-05-09 PTY investigation](pty-investigation-2026-05-09.md)).

## TL;DR

The bug is on the **pogo side**, and it is not a race or a teardown glitch:
**the detach path was never implemented.** `pogo attach <name>` has advertised
"Detach with Ctrl-\ (SIGQUIT)" in its help text since Phase 0 (44e7d19), but no
code ever honored it. Pressing Ctrl-\ while attached did nothing useful — the
byte was forwarded into the agent's PTY as a keystroke — so the user was stuck
attached with no clean way out. When the agent is itself a Claude Code TUI
(the "surfaced in Claude Code" part), the stray 0x1c also lands inside that
inner session.

## Root cause

Two independent facts combine so that Ctrl-\ can never detach:

1. **Raw mode disables the signal.** `AttachAgent`
   (`internal/client/attach.go`) calls `term.MakeRaw(stdinFd)`. In
   `golang.org/x/term@v0.41.0/term_unix.go:34`, `MakeRaw` clears `ISIG`:

   ```go
   termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
   ```

   With `ISIG` off, the kernel no longer translates Ctrl-\ (0x1c) into
   `SIGQUIT`; the byte is delivered to `os.Stdin.Read` like any other
   keystroke.

2. **Nothing watches for it.** The client only `signal.Notify`s `SIGWINCH`
   (for resize) — never `SIGQUIT` — and the stdin→conn goroutine wrapped every
   byte it read, including 0x1c, in a `sendDataFrame`. There was no byte-level
   scan for the escape and no other detach mechanism.

Net effect: once attached, the only ways out were the agent process exiting
(which closes the socket) or killing `pogo attach` from another terminal /
closing the window. Because `term.Restore` runs in a `defer` that was only
reachable on connection close, a user who force-killed the terminal could also
be left with a raw-mode shell.

This was true from the very first commit — `git show 44e7d19:internal/client/
attach.go` already notifies only `SIGWINCH` while the doc comment claims
Ctrl-\. The promise was documentation that never matched code.

## Fix

Implement the standard raw-mode escape-byte detach (the approach `docker
attach`, `ssh ~.`, and `tmux` all use, precisely because raw mode means you
cannot rely on a signal):

- Scan each stdin chunk for the detach byte `0x1c` (Ctrl-\) via `splitDetach`.
- Forward the bytes that precede it, then return from the stdin goroutine so
  `AttachAgent` unwinds and the deferred `term.Restore` leaves the terminal
  sane.
- Consume the escape byte itself — it is never forwarded to the agent, so no
  stray Ctrl-\ reaches the inner TUI.

The help text is corrected to drop the inaccurate "(SIGQUIT)" — detach is now
an escape keystroke intercepted by the client, not a signal.

Regression guard: `TestSplitDetach` and `TestDetachByteIsCtrlBackslash` in
`internal/client/attach_test.go`.

## Not the cause

- **Not Claude Code's tty handling.** The 0x1c never left the pogo client as a
  detach request; it was pogo that swallowed it into a data frame. Claude Code
  only ever saw it because pogo forwarded it.
- **Not PTY allocation/teardown.** Spawn-side winsize and framed-mode handshake
  (mg-5564, mg-8772) are unrelated; the socket and PTY were healthy. The gap
  was purely the missing client-side detach.
