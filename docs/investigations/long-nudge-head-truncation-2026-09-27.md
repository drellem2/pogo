# Long PTY nudges lose their head on Claude Code 2.1.283 (mg-8a70)

## Symptom

On 2026-09-26/27, 128 of 257 polecat mail-check fires on Claude Code 2.1.283
reached the transcript as only their last ~120–140 characters. The measurement is
p02fc's (mg-02fc). On 2.1.263 it was 3 of 66. pogod logged every one of them as
`nudge_sent mode=confirm`. The mayor saw the same thing on crew notices
("randed` before dispatching.").

## Re-measured here

The measurement joined `nudge_sent.fire_token` in `~/.pogo/events.log{,.1}` to
the token in each typed transcript prompt under
`~/.claude/projects/-Users-daniel--pogo-polecats-*/*.jsonl`. 368 fires joined.

| harness | sent ≤ 649 chars | sent 1137–1161 chars |
|---|---|---|
| 2.1.263 | 0 of 101 damaged | 3 of 3 damaged |
| 2.1.283 | 0 of 114 damaged | 139 of 145 damaged |

In 128 of the damaged 2.1.283 fires the loss was **exactly 1016 characters**.
The first 1016 characters of the message contain three em-dashes, so they are
**1022 bytes**. The remnants are 121 characters from the 1143-byte fire and 137
from the 1159-byte fire. The other damaged records either kept a literal
`[Pasted text #N +3 lines]` placeholder or were delivered whole but wrapped in
`<pasted_content>`, which Claude Code tells the model to treat as possibly not
the user's words.

## Cause: two halves

**Kernel.** On darwin the tty input queue holds 1022 bytes (TTYHOG−2). A larger
write to the PTY master blocks until the reader drains it, so the reader sees
separate reads. Measured with a raw-mode reader that starts late:

| write | reads |
|---|---|
| 1022 | [1022] |
| 1023 | [1022, 1] |
| 1143 | [1022, 121] |
| 3000 | [1022, 1022, 956] |

**Harness.** Claude Code 2.1.283 classifies input per read:

- A single read of 793 characters was typed.
- A read of 900 bytes was a paste, wrapped in `<pasted_content>`.
- A paste followed at once by a second read is dropped at submit. Only the second
  read is submitted.

This was reproduced against the real binary with a `UserPromptSubmit` hook that
records the prompt and exits 2, which blocks the prompt so no model call is made.
pogo's exact write pattern (one write, 50 ms, `\r`) gave 121 characters, 3 of 3.

Spacing the pieces by time is not a fix. 256-byte pieces written back to back
(which is what a stalled reader sees) coalesced in the queue. Once they came back
truncated to 121, and once they came back wrapped as a paste.

## Fix

- `Agent.Nudge` writes a body longer than `NudgeProfile.InputChunkBytes` (512
  for Claude) in pieces. Before each next piece it waits until `FIONREAD` on the
  slave fd reads 0, meaning the harness has read everything written so far.
  pogod already holds that fd (mg-9aa1). This held 8 of 8 in the repro,
  including runs where claude was SIGSTOPped for 1 s mid-message, and 3 of 3
  with a 3.2 KB body.
- The receipt hook records an excerpt (length, first and last 100 runes) of
  the submitted prompt. `deliverConfirmed` checks both ends of the sent message
  against it. A message with one end missing is `ErrNudgeMangled` /
  `nudge_unconfirmed outcome=mangled`. The scheduler then mails the whole fire.

## Before and after, through pogo's own code, against the real binary

`TestLiveClaudeLongNudge` is skipped unless `POGO_LIVE_CLAUDE_DIR` and
`POGO_LIVE_POGO` are set. It uses the 1137-character t036c fire:

| InputChunkBytes | confirmed intact | mangled (detected) |
|---|---|---|
| 0 (the write before this fix) | 0 of 3 | 3 of 3: 121 of 1137 characters arrived |
| 512 | 3 of 3 | 0 of 3 |

## Limits

- A prompt taken **mid-turn** fires no `UserPromptSubmit`, so there is no receipt
  and no content check (`ErrNudgeQueued`). The chunked write applies there too,
  but nothing verifies it on that path.
- If the harness stops reading for longer than `inputDrainBudget` (10 s), the
  rest of the body is written without waiting, as before. The content check is
  what catches that case.
- A deployed `pogo` binary older than this change writes count-only receipts.
  Those are recorded as `content_check: "unknown"` and never judged.
- The drain-gated write was verified on darwin only. Linux uses TIOCINQ (the
  same ioctl as FIONREAD) and its tty buffer is larger, so it was not measured
  there.
