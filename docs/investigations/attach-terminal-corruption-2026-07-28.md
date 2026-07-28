# `pogo agent attach`: a long attach ends detached, with control characters in the shell prompt — 2026-07-28

**Origin:** Daniel, 2026-07-28, mg-9b5b:

> "whenever I'm attached to an agent for a long time, when I come back I'm
> detached with some control characters in my terminal prompt. PTY is probably
> mishandling / not escaping something."

Two symptoms, two independent defects, both in the attach path, and they compose
into exactly the reported experience. Both are fixed here.

## TL;DR

1. **The corruption half is not an escaping bug — it is a *restore* bug.** Attach
   never restored terminal-emulator state on detach. `term.MakeRaw` /
   `term.Restore` save and restore the **tty driver's termios** (ECHO, ICANON,
   ISIG). They cannot touch state that lives in the terminal *emulator*: the
   alternate screen, mouse reporting, focus reporting, cursor visibility, the
   scroll region, SGR attributes. The agent's TUI turns those on by writing DEC
   private-mode sequences, which attach forwards verbatim (correctly — it must).
   On detach they stayed latched on Daniel's terminal. With **focus reporting**
   (`\x1b[?1004h`) still armed, every subsequent focus change types a literal
   `\x1b[I` / `\x1b[O` at the shell prompt. That is the control characters.

2. **The detach half is not a timeout.** It is the agent going away underneath a
   live attach. `Cleanup` closes the PTY master and retires the listener but left
   **established attach connections open**, so the client sat on a dead socket
   showing a frozen screen — and was dropped by its *next* outgoing byte, which
   fails `master.Write` on the closed fd. Come back, touch the terminal, get
   detached. And with focus reporting armed by defect 1, merely refocusing the
   window *is* an outgoing byte.

Nothing here is time-based. "Long attach" is a **probability**, not a trigger:
crew agents respawn and polecats are stopped the moment their merge lands, so the
longer an attach runs the likelier the agent under it has already been replaced.

## What was OBSERVED

### The modes a real agent leaves behind

Extracted as literals from the Claude Code binary pogo agents run
(`~/.local/share/claude/versions/2.1.220`, 2026-07-28):

```
\x1b[?1049h / \x1b[?1049l   alternate screen buffer
\x1b[?1000h / \x1b[?1000l   mouse tracking
\x1b[?1006h / \x1b[?1006l   SGR mouse encoding
\x1b[?1004h / \x1b[?1004l   focus reporting          <-- the \x1b[I source
\x1b[?2026h / \x1b[?2026l   synchronized output
\x1b[?25l   / \x1b[?25h     cursor visibility
\x1b[?1l, \x1b[?12l, \x1b[?1007h, \x1b[?9001l
```

`?25l`/`?25h` are also observable live in any running agent's ring buffer
(`pogo agent output <name> | grep -oa $'\x1b\[?[0-9;]*[hl]'` — five live agents
checked, all show the hide/show pair). The mode-*enabling* burst happens at TUI
startup and has long scrolled out of the 64 KiB buffer, which is why only `?25`
shows up there.

Grep of the whole repo for any of `1049`, `2004`, `1004`, `1000`, `1006` outside
of this change: **no hits**. pogo has never emitted a mode reset anywhere.

### The connection outliving the agent

A probe against pre-fix code (`internal/agent`, `cat` agent, framed attach,
`kill` the agent process):

```
OBSERVED: conn STILL OPEN 1.5s after agent exit (read timeout)
          -- client stays 'attached' to a dead agent
OBSERVED (post-keystroke): conn CLOSED
          -- the keystroke is what detaches the user
```

That is the reported sequence, reproduced: silence while you are away, then
detach at the first thing you do when you come back.

### No session evidence exists for real attaches

`~/.pogo/events.log` (177 788 events) contains **no attach or detach event** —
every `agent_attach_*` line in it is `agent_attach_rebound` from test runs.
`~/Library/Logs/pogo/pogod.log` has no attach lines either. pogo does not
instrument attach at all, so Daniel's specific sessions left no trace. The
evidence above is from the binary, the live buffers, and reproduction — not from
a recording of his session.

## Root cause, in code

**Corruption.** `internal/client/attach.go`. `AttachAgent` did:

```go
oldState, err := term.MakeRaw(stdinFd)
defer term.Restore(stdinFd, oldState)
...
go io.Copy(os.Stdout, conn)   // agent's mode sequences → user's terminal
<-done
return nil                    // termios restored; emulator state left as-is
```

Every `\x1b[?…h` the agent wrote reached the user's terminal and nothing ever
wrote the matching `l`. The alternate screen made it worse in a second way: the
attach's 64 KiB scrollback replay can itself contain a `\x1b[?1049h` whose
matching exit scrolled out, so a *fresh* attach can arm modes on the user's
terminal before a single new byte arrives.

**Detach.** `internal/agent/agent.go`. `Cleanup` closes `a.master` and calls
`retireListenerLocked` (close listener, unlink socket). Neither touches
`attachConns`. `handleAttach`'s `readAttachInput` is parked in a socket read that
nothing wakes. The captured `master` is a closed `*os.File`, so the first data
frame that arrives hits `master.Write` → `file already closed` → return →
`defer conn.Close()` → client's `io.Copy` returns → detach.

## Fix

**Corruption — `internal/client/termstate.go` (new).** A streaming VT parser
sits in the conn→stdout copy (`io.MultiWriter(stdout, tstate)`) and records which
DEC private modes the agent's output turned on and left on, plus SGR, scroll
region and keypad mode. On unwind, attach stops the output pump (closing the conn
first, so no late PTY bytes race the restore and re-dirty the terminal), then
writes the matching resets, then `term.Restore` runs.

Two properties the parser has to have, and does:

- **Streaming.** A 4 KiB PTY read boundary lands wherever the kernel puts it,
  including between the `?` and the `1049h`. A per-chunk `bytes.Contains` scan
  would miss precisely the mode it most needs to reset. Tested down to
  one byte per `Write`.
- **Reset only what the agent set.** The tempting fix — blanket-reset a fixed
  list on detach — would send `\x1b[?2004l` and turn off the bracketed paste the
  user's *shell* owns, breaking pasting in the terminal the detach just handed
  back. Pinned by test.

**Detach — `internal/agent/agent.go`.** `handleAttach` now closes the attach
connection when `a.done` closes, so the user is detached at the moment the agent
dies (and their terminal restored, via the above) rather than being left staring
at a frozen screen until they touch a key.

**Telling the user.** `pogo agent attach` now prints `Detached from agent <name>.`
on return. Silence was half the report: a detach the user did not ask for read as
"my terminal broke".

## Positive controls

All four were run RED against pre-fix code before being made to pass.

| Test | Proves |
|---|---|
| `TestAttachRestoresTerminalModesOnDetach` (`internal/client`) | Drives the real `attachAgent` over a unix socket with the exact Claude Code startup burst; Ctrl-\ detach; asserts `?1049l ?1000l ?1006l ?1004l ?2026l ?25h` in the returned stream. RED: `after-detach output: "agent TUI output\r\n"` — nothing at all. |
| `TestAttachRestoresTerminalModesWhenAgentGoesAway` | Same, but the *server* closes the conn — the path the user is not watching. RED: output ends `…\x1b[?25lagent TUI output\r\n`, modes still armed. |
| `TestAttachConnClosesWhenAgentExits` (`internal/agent`) | Real spawned agent, real framed attach, agent killed; conn must close with no client input. RED: `attach connection still open 3s after the agent exited`. |
| `TestAttachDoesNotResetModesTheAgentNeverSet` | The detach must not clobber the shell's bracketed paste. |

Plus `TestAttachConnStaysOpenWhileAgentLives` as the counterweight (the close is
tied to agent exit, not eager), and unit coverage of the parser: split writes,
multi-parameter sequences (`\x1b[?1000;1002;1006;1004h`), OSC/DCS strings
containing mode-shaped bytes, RIS, default-on modes (`?7`, `?25`) restored with
`h` rather than `l`, and ordering (alt-screen exit last).

## Not the cause

- **Not an idle or keepalive timeout.** There is none on either side of the
  attach path — no `SetDeadline`, no ping. Grepped.
- **Not a PTY read error.** `readOutput` is the sole master reader and logs any
  non-EOF error; the pogod log has no such lines.
- **Not resize/SIGWINCH.** `applyResize` has been idempotent and 0×0-guarded
  since mg-5564/mg-8772, and the resize path was re-verified by the existing
  regression suite.
- **Not the detach byte firing spuriously.** `splitDetach` scans for `0x1c`,
  which cannot appear in UTF-8 continuation bytes and is below the `32+coord`
  floor of X10 mouse reports.

## Left alone, deliberately

The scrollback replay (`a.outputBuf.Last(a.outputBuf.Len())`) is the tail of a
ring buffer and can begin mid-escape-sequence, so an attach can open with a few
junk characters. That is cosmetic — the terminal is in ground state, so the
fragment prints as text rather than changing any mode — and no observation ties
it to this report. Noted, not fixed. (Its *other* effect, replaying a
mode-enabling sequence whose matching reset has scrolled out, is covered: the
tracker sees the replay bytes too.)

Attach still emits no events, so a future attach report will again have no
session evidence. Instrumenting it is worth its own item.
