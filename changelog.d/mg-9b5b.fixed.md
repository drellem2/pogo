Detaching from `pogo agent attach` now hands the terminal back clean, and an
agent that goes away under a live attach detaches you instead of freezing.

Two independent defects produced one report — "attached a long time, came back
detached with control characters in my prompt". Neither is time-based.

The corruption was not an escaping bug but a **restore** bug. `term.Restore`
puts the tty driver's termios back; it cannot touch state that lives in the
terminal *emulator*. A full-screen TUI on the far end arms the alternate screen,
mouse reporting, focus reporting, synchronized output and a hidden cursor by
writing DEC private-mode sequences that attach forwards verbatim — and nothing
ever wrote the matching resets, so they stayed latched on the shell prompt you
came back to. With focus reporting (`\x1b[?1004h`) still armed, every window
focus change types a literal `\x1b[I` at that prompt. Attach now tracks the
modes the agent's output turns on — with a *streaming* parser, since a PTY read
boundary can split a sequence — and resets exactly those on detach. Only those:
blanket-resetting a fixed list would send `\x1b[?2004l` and disable the
bracketed paste the user's own shell owns.

The detach half was the agent dying underneath the attach. `Cleanup` closed the
PTY master and retired the listener but left established attach connections
open, so the client sat on a dead socket showing a frozen screen and was dropped
by its next outgoing byte — which, once focus reporting is armed, is merely
refocusing the window. Attach connections now close when the agent process
exits, so you are detached (and your terminal restored) at the moment it
happens. "Long attach" was never a trigger, only a probability: crew agents
respawn and polecats are stopped when their merge lands.

`pogo agent attach` also prints `Detached from agent <name>.` on return —
silence made a detach nobody asked for read as a broken terminal.

Positive controls run RED against the old code first: the real attach path
driven with the exact byte sequence Claude Code 2.1.220 emits at startup (read
as literals out of the shipped binary), for both a Ctrl-\ detach and a
server-side close, plus a real spawned agent whose attach connection must close
on exit with no client input. Full trace in
`docs/investigations/attach-terminal-corruption-2026-07-28.md`.
