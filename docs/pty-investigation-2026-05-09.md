# PTY rendering funkiness on `pogo agent attach` — investigation 2026-05-09

**Origin:** Daniel observation 2026-05-09 ~17:38 UTC (mg-098c).

> "there's some funkiness going on with the pty… the input is higher on the
> screen and the old input is lower and the new input slowly clears the old."

This is a read-only investigation. No code is changed by this ticket; a
follow-up impl ticket carries the fix.

## TL;DR

The bug is on the **pogo side**. pogod spawns each agent's PTY without
ever setting a window size, and `pogo agent attach` has no protocol for
the client to communicate its terminal size to the agent. Claude Code
inside the agent therefore renders against the kernel default winsize
(0×0, which Ink falls back to as 80×24) regardless of how big Daniel's
actual Ghostty window is. Cursor-positioning escapes emitted by the TUI
land at the wrong absolute rows in the larger viewport, exactly
producing the symptom Daniel described.

There is a related upstream Claude Code TUI bug
([anthropics/claude-code#52945](https://github.com/anthropics/claude-code/issues/52945)),
but it is triggered by **mid-session resize** on top of an already-correct
size; in pogo's case the size is wrong from spawn, so we'd hit the
analogous mis-render even without any resize.

Direction: **pogo-bridge-bug** (primary), with a known upstream Claude
Code issue contributing in resize scenarios.

## Symptom

Inside an interactive Claude Code session attached via `pogo agent
attach <name>`:

- New input renders **higher** on the screen than expected.
- Old input lingers **lower**.
- New input slowly clears the old as the TUI does partial redraws.
- Cosmetic; doesn't block usage.

## Root cause

### 1. PTY size is never set on agent spawn

`internal/agent/agent.go:288` calls `pty.Start(cmd)`, which delegates to
`creack/pty`'s `StartWithSize(cmd, nil)`. Inspecting the dependency at
`~/go/pkg/mod/github.com/creack/pty@v1.1.24/run.go`:

```go
func Start(cmd *exec.Cmd) (*os.File, error) {
    return StartWithSize(cmd, nil)
}

// In StartWithAttrs:
if sz != nil {
    if err := Setsize(pty, sz); err != nil { ... }
}
```

When `sz == nil`, `Setsize` is **never called**. The kernel allocates a
PTY pair via `openpty(3)` with the default winsize (zeros on Linux and
Darwin). Node/Ink read `process.stdout.columns/rows`, get `undefined`,
and fall back to its hard-coded 80×24.

Result: every pogo-spawned agent runs its TUI as if the screen is 80×24,
forever — even after the user's terminal has been resized.

### 2. `pogo agent attach` has no size negotiation

`internal/client/attach.go:17-53`: the unix-socket attach client opens a
raw byte stream and does `io.Copy` in both directions. There is no
control-message format, no header, no sideband. The client knows its own
terminal size (`golang.org/x/term`) but never tells the agent.

Compare with `internal/agent/terminal.go:122-134`, which is the WebSocket
endpoint used by the React dashboard. **That** path supports a JSON
control frame `{"type":"resize","cols":N,"rows":M}` and dutifully calls
`pty.Setsize`. The Unix socket / CLI attach path is missing the
equivalent.

### 3. SIGWINCH set up but never consumed during attach

`internal/client/attach.go:32-34`:

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGWINCH)
defer signal.Stop(sigCh)
```

`sigCh` is registered, then nothing reads from it. So even if mechanism
(2) existed, terminal resizes during an active attach would be dropped
on the floor.

## Why this produces Daniel's exact symptom

Trace, assuming Daniel's Ghostty is e.g. 200 cols × 60 rows:

1. pogod spawns the agent's PTY; size is 0×0 → Ink renders at 80×24.
2. Claude Code's input box lives "at the bottom of the screen" — it
   targets row 24 (the last row of the 24-row world Claude believes
   it lives in).
3. The TUI emits absolute cursor-positioning escape codes (CSI `H`/
   `f`, CSI `J`, etc.) for row 24. These pass straight through pogod's
   master fd, through the unix socket, through the attach client, and
   land in Daniel's actual terminal.
4. Daniel's terminal places "row 24" wherever row 24 actually is in a
   60-row viewport — the **middle** of the screen. New input appears
   there.
5. Older output that had previously scrolled into rows 25–60 of the
   actual terminal (because cursor advancement is real even when
   absolute positioning is wrong) sits below the input box, **stale**.
6. As Ink does partial redraws, it overwrites rows 1–24 progressively.
   Rows below 24 stay until natural scroll evicts them — i.e. "the new
   input slowly clears the old."

This matches Daniel's description verbatim.

## Survey of relevant Claude Code upstream issues

Searched anthropic/claude-code for: pty, terminal, render, input
position, screen, scroll, ghostty.

| # | State | Title | Relevance |
|---|-------|-------|-----------|
| [#52945](https://github.com/anthropics/claude-code/issues/52945) | OPEN | Conversation content duplicates in scrollback after terminal resize (font-size / window-size) | **High** — exact "duplicate stacked frames" symptom on resize. Workaround: `CLAUDE_CODE_NO_FLICKER=1`. Independent of pogo, but composes with pogo's missing winsize. |
| [#43273](https://github.com/anthropics/claude-code/issues/43273) | OPEN | TUI doesn't redraw after terminal resize from KVM switch | Related — TUI doesn't recover from a size change without user keystroke. |
| [#48308](https://github.com/anthropics/claude-code/issues/48308) | OPEN | Double ESC rollback: history not visible in Ghostty until resize/fullscreened | Ghostty-specific; size-recovery bug. |
| [#52660](https://github.com/anthropics/claude-code/issues/52660) | CLOSED | TUI input box leaves ghost renders in scrollback on tmux pane vertical resize | Closed; matches "ghost renders" wording. |
| [#41965](https://github.com/anthropics/claude-code/issues/41965) | CLOSED | v2.1.89 regression: flicker-free rendering destroys terminal scrollback by default | Closed; relevant historical context for Ink's render strategy. |
| [#36582](https://github.com/anthropics/claude-code/issues/36582), [#826](https://github.com/anthropics/claude-code/issues/826) | OPEN | Terminal scrolls to top during long sessions | Different symptom (vertical jumps), keeping for context. |
| [#55089](https://github.com/anthropics/claude-code/issues/55089) | CLOSED | Streamed-output rendering corruption on Ghostty + SSH | Different mechanism (terminfo mismatch over SSH); not our case but worth flagging if Daniel ever uses Claude Code over SSH from Ghostty. |

**Headline:** #52945 is the closest direct match for Daniel's wording,
and its workaround (`CLAUDE_CODE_NO_FLICKER=1`) would mask the symptom
even on pogo. But the *root* problem in pogo's case is the missing
winsize at the bridge layer; #52945 is a TUI-side bug that compounds it.

## Reproduction steps

Without changing any code, this is reproducible:

1. Spawn an agent: `pogo agent spawn-polecat …` or `pogo agent spawn …`.
2. From a terminal noticeably taller than 24 rows (say a maximised
   Ghostty window), `pogo agent attach <name>`.
3. Interact for a few turns. The Claude Code input box should appear
   roughly a third to halfway down the screen instead of at the bottom,
   with prior conversation lingering below it. Typing slowly clears
   the lingering content.

Confirmation that the size is the cause (read-only):

- After spawning an agent and before attaching, you can verify the
  agent's PTY size by inspecting the slave fd from the agent process's
  perspective. From inside an attached session, `stty size` will print
  the size the agent's PTY is configured to. Expect `24 80` (or
  similar) regardless of your Ghostty size.
- Resize Ghostty mid-attach. Symptom does **not** correct itself,
  because SIGWINCH is dropped by the attach client (bug #3 above).

## Recommended fix (carried by follow-up ticket)

Three-part change in pogo:

1. **Set initial winsize on spawn.** In
   `internal/agent/agent.go:Spawn` and `Respawn`, switch from
   `pty.Start(cmd)` to `pty.StartWithSize(cmd, &pty.Winsize{...})`
   with a sensible default (e.g. 200×50). The exact default is less
   important than "not 0×0" — a real attach will overwrite it.
2. **Add size negotiation to the unix-socket attach protocol.** Two
   workable approaches:
   - **(a) Sideband control byte in the stream.** Reserve a leading
     framed message: a magic byte + 4 bytes (`cols`,`rows` little-endian
     u16). Client sends it once on connect; server applies via
     `pty.Setsize`; rest of the stream is raw bytes as today. Backward
     compatibility: bump pogod and the CLI together; legacy clients
     fall through to the existing default-size behavior.
   - **(b) Use the existing WebSocket endpoint.** The dashboard's
     `HandleTerminal` already implements size negotiation cleanly. The
     CLI could speak ws:// to localhost instead of dialing the unix
     socket. Heavier change but eliminates the dual code path.
   Recommend **(a)**: small, localised, and keeps the CLI dependency
   surface minimal.
3. **Wire SIGWINCH through during attach.** In
   `internal/client/attach.go`, add a goroutine that drains `sigCh` and
   sends an updated size frame using mechanism (2). This makes
   mid-session resize work.

The follow-up ticket should also add a small integration test: spawn an
agent, dial its socket, send the size frame with a known
non-default size, and verify (via `stty size` injected as a nudge) that
the agent's PTY took the size.

## What this is *not*

- **Not a Claude Code bug** in the primary sense. Claude Code is doing
  what every TUI does when given a wrong winsize. Setting the size
  correctly at the bridge layer is pogo's job.
- **Not a Ghostty bug.** Same outcome would occur in iTerm2, Terminal.app,
  Alacritty, or any emulator when attached at a non-80×24 size.
- **Not specific to polecats vs crew.** Affects every agent spawned
  through the registry.

## Workarounds available to Daniel today (until the fix lands)

- Run the user terminal at exactly 80 columns × 24 rows during attach.
  Ugly but eliminates the misalignment.
- Use the React dashboard's WebSocket terminal instead of `pogo agent
  attach` — it already negotiates size correctly.
- Resize Ghostty after attach to roughly 80×24 of the agent's PTY by
  trial and error. Not recommended.
- Set `CLAUDE_CODE_NO_FLICKER=1` in the agent's environment via
  `--env`. This may **mask** the symptom by switching Claude Code to
  alternate-screen mode, but the underlying winsize is still wrong, so
  expect lingering edge cases.

## Audit trail / suspect code paths (for the follow-up)

- `internal/agent/agent.go:288` — `pty.Start(cmd)` (Spawn), no winsize.
- `internal/agent/agent.go:522` — `pty.Start(cmd)` (Respawn), same.
- `internal/agent/terminal.go:124-134` — correct size handling, but
  WebSocket-only.
- `internal/client/attach.go:17-53` — unix-socket attach: raw `io.Copy`,
  no size frame, SIGWINCH registered but never read.
- `internal/agent/agent.go:801-833` — `handleAttach`: also raw
  `io.Copy`, server-side counterpart.
