# Nudge → Claude Code Workarounds — Investigation & Test Protocol

**Status:** **settled for Claude Code 2.1.220** — §3 protocol executed 2026-07-29 (mg-68c8).
The bug is still present; the Claude profile keeps its 50ms `SubmitDelay`. No code changed.
Results: `docs/investigations/mg-09b6-test-results.yaml`. Re-test rig:
`internal/claude/submitdelay_e2e_test.go`. See §0 before re-running.
**Origin:** mg-09b6 (Daniel directive 2026-05-18 19:20 BST — *"may be the wrong workaround now"*).
**Author:** architect. **§0 and the §3 amendments:** mg-68c8.
**Sibling docs:** mg-ef6b's `docs/design/rating-dialog-watcher-design.md` (same Claude Code PTY-interaction territory).

## 0 · Result — Claude Code 2.1.220, measured 2026-07-29 (mg-68c8)

The §3 protocol ran, unmodified in substance, against a short-lived `claude`
under a polecat-owned PTY at pogo's 200×50 winsize. Ten interleaved probe pairs:

| Variant | What it writes | Submitted |
|---|---|---|
| **A** — no delay | `body + "\r"` in **one** write | **0/10** |
| **B** — shipped config | `body`, sleep 50ms, `"\r"` | **10/10** |

**Conclusion: `bug-still-present` → `keep`.** Claude Code's Ink composer still
swallows the submit when the body and the terminator land in one `read()`. Per
§5 this is a legitimate outcome and changes no code: the Claude profile's
`SubmitDelay` stays at 50ms, and it is now a measured, version-pinned fact
rather than an inherited belief.

Two things the 2026-05 protocol could not have known, both worth carrying
forward:

### 0.1 The heuristic keys on chunk SIZE, and the boundary is exactly 64 bytes

A pilot probe submitted when the protocol's own probe did not, and the only
difference was body length. A controlled sweep (uniform padding, no
punctuation, byte count the sole variable) found a sharp boundary: **every
single write of ≤63 bytes submits; every write of ≥64 bytes does not.** No grey
zone, no flaky middle. So this is a size test on the read chunk, not a
keystroke-timing test.

That makes probe length load-bearing for the *test*, not just the product. A
re-test that probes with a short token — the natural thing to write — measures
63-byte behaviour, sees 10/10 on both variants, and concludes the bug is fixed.
It would be wrong about every real nudge: production nudge bodies are a message
plus the scheduler's `[scheduler id=… due=… fired=…]` metadata line, far past 64
bytes. The committed rig pins an 82-byte probe and says so at the constant.

This also explains the §1 note that polecats "accidentally escaped the bug" —
but only partly. Startup timing is one escape; short bodies are another, and
the fleet's are not short.

### 0.2 What "submitted" means, and why it is no longer read off the screen

§3 proposed distinguishing "echoed into the conversation" from "sitting in the
input box" by scanning the PTY tee. Both regions render the same characters
into the same redraw stream, so that is a screen-region heuristic — the family
of matching that has already broken twice here (gh#76 / mg-d06a's
space-collapse, and the ready sentinel that stopped matching in v2.1.x).

The rig uses a file fact instead: Claude Code appends every **submitted** user
message to its session JSONL under `~/.claude/projects/<slug>/`, which this
package already locates via `SessionTranscriptGlob` and `internal/synthfail`
already reads. A probe retained in the composer never appears there. PTY byte
volume after each probe is recorded as an independent cross-check and separates
cleanly — ~950 bytes for a retained probe (composer repaint) versus ~2,900 for a
submitted one (a whole response render).

### 0.3 Rig gotchas, recorded so the next run does not rediscover them

- **Clear the composer with Ctrl+U (`\x15`), not Escape.** Escape looks right
  and is wrong: sent to an idle composer holding a retained probe, it left the
  TUI in a state where the *next* probe's keystrokes produced no render at all
  (4 PTY bytes over 20s). Every probe after the first then measured nothing.
- **Strip `CLAUDECODE` and `CLAUDE_*` from the child environment.** The runner
  is itself a Claude Code agent — §3 assigns this to a polecat — so the
  inherited env marks the child as a nested session, which is not the harness
  pogod spawns.

### 0.4 Re-running

```
POGO_CLAUDE_SUBMITDELAY_E2E=1 go test ./internal/claude/ \
    -run TestClaudeSubmitDelayStillRequired -v -timeout 30m
```

Opt-in, real binary, real credentials, ~4 minutes, one model turn per submitted
probe — the same shape as `internal/codex/e2e_test.go` and
`internal/cursor/e2e_test.go`, and for the same reason: there is no offline mock
of the behaviour under test. No CI job; CI has no Claude credentials. The rig
fails loudly if variant B — the shipped configuration — submits 0/N, because
that means the run measured the rig rather than the delay.

The protocol below is preserved as written in 2026-05, with the amendments
above folded in where they contradict it.

## TL;DR — two candidates, two different dispositions

The ticket identified two pieces of pogo behaviour that look like Claude
Code workarounds. After inspection:

1. **`SubmitDelay` (50ms gap before `\r`)** — *load-bearing*, and as of
   2026-07-29 **measured, not assumed**: §0 records 0/10 vs 10/10 on Claude
   Code 2.1.220. Keep the 50ms. (Since 2026-05 the value moved out of a
   Claude-specific `NudgeSubmitDelay` constant into the per-provider
   `agent.NudgeProfile`; the Claude profile is
   `agent.DefaultNudgeProfile`, adopted verbatim by `claude.Provider`.)
2. **`\n\n` between nudge body and `[scheduler id=...]` metadata line** —
   **not a workaround.** Pure formatting choice. Leave alone (§4 rationale).

The "weird line break" Daniel recalled is almost certainly Candidate 1's
mechanism (the bug *manifests* as a literal newline being typed into the
input box where a submit was expected). Candidate 2 is just visual
separation in the rendered nudge body and has no upstream-bug connection.

## 1 · What each candidate actually does

### Candidate 1 — `Agent.Nudge` in `internal/agent/agent.go`

As written in 2026-05 the delay was a package constant, `NudgeSubmitDelay`. It
now lives in the per-provider `agent.NudgeProfile` (`SubmitDelay`,
`SubmitTerminator`), so the shape is:

```go
// agent.DefaultNudgeProfile — adopted verbatim by claude.Provider
SubmitDelay:      50 * time.Millisecond,
SubmitTerminator: "\r",

func (a *Agent) Nudge(message string) error {
    // ... lock + nil checks ...
    if message != "" {
        a.master.WriteString(message)
        time.Sleep(a.nudge.SubmitDelay)
    }
    a.master.WriteString(a.nudge.SubmitTerminator)
    return nil
}
```

The generalisation narrowed the question rather than changing it: codex, cursor
and pi each calibrated their own value against their own TUI, so `claude` was
the one provider whose number was inherited rather than measured. §0 measures it.

The comment (lines 600-611) describes the failure mode precisely: Claude
Code's React/Ink input box has paste-detection. When the body bytes and
the trailing `\r` arrive in a *single* `read()` syscall on the receiving
end, paste-detection treats the whole chunk as paste, and the `\r` becomes
a literal newline embedded in the input field rather than a submit
keystroke. Result: the nudge text sits in the input box unsubmitted, and
the agent eventually wedges (nothing fires the response cycle).

The 50ms gap ensures the body and `\r` arrive in *separate* `read()` calls
on the receiving side, defeating paste-detection. Polecats accidentally
escaped the bug because their first nudges fire before Ink's
paste-detection arms; long-running crew agents are exposed.

### Candidate 2 — `internal/scheduler/deliverer.go:81`

```go
return fmt.Sprintf("%s\n\n[scheduler id=%s due=%s fired=%s]",
    entry.Message, entry.ID, original, now)
```

Format: nudge body + `\n\n` + bracketed metadata line. The `\n\n` is
purely a visual separator between the agent-meaningful content and the
debug/traceability metadata. It is **not** a workaround for any Claude
Code behaviour; nothing in the codebase, comments, or git history ties
this `\n\n` to a paste-detection or submit bug.

## 2 · Inspection findings: §1 confirms

- Candidate 1's comment explicitly cites the bug, names the mechanism
  (paste detection in React/Ink input), and explains the precise reason
  the delay matters (50ms covers Node's read-loop tick + Ink's
  re-render). The comment is self-documenting and load-bearing iff the
  underlying paste-detection still does what the comment describes.
- Candidate 2 has no such bug-attached comment. The format string is in
  `buildBody`, a helper that constructs the message string passed to
  `Mail()` (not the same path as `Nudge()`). The `\n\n` is what a reader
  sees in their maildir, not what gets PTY-pasted.

Conclusion: **Candidate 1 is the only true workaround.** Candidate 2 is a
formatting artifact the user might *recall* as related but isn't.

## 3 · Candidate 1 — empirical test protocol (polecat-executed)

> **Executed 2026-07-29 against Claude Code 2.1.220 — see §0 for results and for
> the three places reality amended this design (probe length, submission
> detection, composer clearing). The text below is the 2026-05 original.**

The test asks: does Claude Code's input-box still drop the `\r` when it
arrives in the same `read()` chunk as the body? Or has upstream fixed it
so we can drop the sleep?

### Test environment

**Critical constraint:** the test must not disrupt production crew agents.
Long-running real crew agents (architect, mayor, pm-pogo, pm-onethird) are
the *most relevant* targets (paste-detection is fully armed) but injecting
test probes would corrupt their conversation state. Solution: spawn a
**dedicated short-lived test Claude Code instance** under the polecat's
own pty pair, drive it through the same paths.

```go
// Inside the polecat:
import "github.com/creack/pty"

ptmx, err := pty.Start(exec.Command("claude"))
defer ptmx.Close()
```

This gives a Claude Code TUI under polecat-owned PTY. No interaction with
the agent registry, no production-state pollution.

### "Settled" — how the polecat knows TUI is initialized

Paste-detection arms when the React/Ink input box is fully rendered
post-init. Heuristic for detecting "settled":

1. Read PTY output until a quiet period of **≥2 seconds** with no new
   bytes. This means initial TUI render is done and Claude is awaiting
   input.
2. As a fallback / verification, look for the input-box prompt marker in
   the output buffer (the ` > ` prompt character that Ink renders at the
   bottom of the input box).

The polecat picks whichever signal is more reliable in practice; both
agree at the same moment.

### Test variants

Two probe modes, each tested **N=10 times** per side to absorb timing
variance.

**Variant A — without `NudgeSubmitDelay`** (the "is the bug still there?"
case):

```go
ptmx.Write([]byte("echo PROBE-A-<iter>\r"))
```

Body + `\r` in one syscall → one `read()` chunk on the receiver side.

**Variant B — with `NudgeSubmitDelay`** (the "known-good" baseline):

```go
ptmx.Write([]byte("echo PROBE-B-<iter>"))
time.Sleep(50 * time.Millisecond)
ptmx.Write([]byte("\r"))
```

Body and `\r` arrive in separate `read()` chunks.

### Detection: did the probe submit?

After each probe + 3-second wait, the polecat scans the PTY tee buffer
for one of:

1. **Echo back** — Claude's response will contain `PROBE-A-<iter>` (or
   `-B-<iter>`) somewhere in its output once it has processed the input.
   Specifically: the response will include the probe string inside a
   tool-use / message block. If found within 3s, **submitted**.
2. **Input-box retention** — if the probe string is visible *only* in the
   input-box render area (the bottom couple of lines of the TUI buffer,
   recognizable by being adjacent to the ` > ` prompt) and never appears
   as a user message in the conversation flow, **not submitted**.

The polecat reports per-iteration: `submitted` / `not-submitted` / `timeout-3s`.

### Randomization + replication

- Iterate Variant A and Variant B in **interleaved order**
  (A,B,A,B,A,B,...,A,B — not all-A-then-all-B) so any drift over the
  20-probe test window affects both sides equally.
- 10 probes per variant gives the polecat a clean ratio: e.g. "A submitted
  3/10, B submitted 10/10" → bug still present.
- If A and B both submit 10/10, the bug is **fixed upstream**; the sleep
  is removable.
- If A submits some but not all (e.g. 7/10), the bug is **partially
  triggered** — paste-detection may now have looser timing. Report the
  rate so architect can decide whether to shrink the delay vs keep at
  50ms.

### Recording Claude Code version

The polecat must capture `claude --version` and the OS / terminal env
into the result file. Bug status is version-pinned; future re-tests need
this baseline.

### Result file

The polecat writes:

```yaml
# polecat-output: mg-09b6-test-results.yaml
claude_version: "..."
test_date: "2026-05-19T..."
host_os: "darwin"
test_runs:
  - variant: A
    iter: 1
    result: submitted | not-submitted | timeout-3s
    notes: "..."
  # ... 20 entries ...
summary:
  variant_a_submit_rate: "X/10"
  variant_b_submit_rate: "Y/10"
  conclusion: "bug-still-present | bug-fixed | partial-trigger"
  recommended_action: "keep | remove | shrink-to-Nms"
```

Polecat mails the result file path to architect; architect reviews and
either:

- Bug fixed → ships a small commit removing `NudgeSubmitDelay` (or
  setting it to 0 with a comment), updates this doc with version-pinned
  conclusion.
- Bug still present → leaves code alone, updates this doc with the
  version-pinned re-test result so the next investigator has data.
- Partial / weird → architect inspects, may propose shrinking the delay
  (e.g. 16ms = one Ink frame instead of 50ms) and re-running.

## 4 · Candidate 2 — disposition: leave alone

Daniel asked whether the `\n\n` separator is "the wrong workaround now."
After inspection: **it isn't a workaround at all** — it's a visual format
choice in `buildBody`. No paste-detection link, no Claude Code-specific
behaviour, no comment tying it to any bug.

### Why keep it

- Agents parse the metadata line by looking for `[scheduler id=`. A
  preceding `\n\n` makes that a clean line-anchor; with `\n` it's harder
  to grep visually.
- The metadata is debug-traceability for humans (mayor's stall-watch
  logs, sweep.log appendings). Two blank-separated blocks read better
  than one mushed block.
- Touching it for aesthetics-only invites churn in downstream consumers
  that might depend on the layout (mayor's log parsers, refinery's
  message-extraction heuristics).

**Recommendation: leave Candidate 2 alone.** Mention in the design doc
that it was inspected and ruled out as a workaround.

## 5 · Routing & roles

- **Architect (this doc):** test protocol picked; Candidate 2 ruled out;
  inspection complete. ✅ 2026-05.
- **Polecat-executed next:** run the §3 protocol with a fresh
  short-lived Claude Code instance under polecat-owned PTY; report
  `mg-09b6-test-results.yaml` to architect. ✅ 2026-07-29 (mg-68c8) —
  `docs/investigations/mg-09b6-test-results.yaml`.
- **Architect (post-results):** either ship the small remove-the-sleep
  commit or update this doc with the version-pinned "still needed"
  conclusion. Tiny architect work; no separate ticket. ✅ the
  "still needed" branch was taken; §0 is that conclusion, and no code changed.
- **No Daniel review gate** — this is investigative; architect can act
  on the result without a §8 picks round.

Nothing is outstanding. The next event is a Claude Code upgrade: re-run §0.4 and
append a second version-pinned block rather than overwriting the first, so the
series shows when upstream behaviour moved.

## 6 · References

- mg-09b6 (this directive); mg-68c8 (ran §3, wrote §0).
- `internal/agent/provider.go` — `NudgeProfile.SubmitDelay` and
  `DefaultNudgeProfile` (the Claude values, adopted verbatim by
  `claude.Provider`).
- `internal/agent/agent.go` — `Agent.Nudge` plus the comment block explaining
  the paste-detection bug.
- `internal/claude/submitdelay_e2e_test.go` — the §3 protocol as an opt-in
  live test; `docs/investigations/mg-09b6-test-results.yaml` — its output.
- `internal/scheduler/deliverer.go:81` — `buildBody` (Candidate 2, ruled
  out).
- mg-ef6b (rating-dialog watcher) — same Claude Code PTY-interaction
  territory; reuses the `internal/claude/` adapter pattern. The §3 test
  protocol's `pty.Start(exec.Command("claude"))` approach is also useful
  for mg-ef6b's §4 upstream-opt-out verification.
- Daniel directive 2026-05-18 19:20 BST.
