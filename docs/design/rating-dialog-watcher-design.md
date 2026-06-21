# Rating Dialog Watcher — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-ef6b (Daniel directive 2026-05-16 — 3 confirmed wedges in 8h on the Claude Code mid-session rating dialog). Filed by pm-pogo per mayor's `class evidence` invitation.
**Author:** architect.
**Sibling docs:** none for the watcher; mg-7327 (`internal/claude/trust_hook.go`, commit 782080b) is the adapter-boundary precedent. mg-83ef / mg-8e5d / mg-2ba0 are siblings in the same session-stability cluster but address different failure modes.

## TL;DR

A new long-running PTY watcher lives in `internal/claude/` alongside the
existing trust-dialog hook, registered via a new agent-Registry lifecycle
hook called **`SessionHook`** (sibling to today's spawn-only `PostSpawnHook`).
It runs for the agent's lifetime, scanning the tee'd PTY output stream for
the rating dialog marker `1:Bad 2:Fine 3:Good 0:Dismiss`, and on detection
writes `0\n` to stdin. The watcher is *event-driven on the output stream*,
not poll-based — no continuous I/O cost on quiet agents.

The watcher runs **inside the pogod agent process**, not driven by
`pogo schedule`. Important per the UPDATE-2026-05-16 note: rating-dialog
wedges co-occurred with mg-8e5d schedule purges; any mitigation that depends
on cron-fired heartbeats fails when the schedule registry purges. The
SessionHook lives in pogod's PTY-managing goroutine and is unaffected by
schedule-substrate failures.

**Recommendations:**

1. **New `SessionHook` lifecycle on `Registry`** (Option A from the ticket's
   §1) — not a hidden goroutine inside `PostSpawnHook` (Option B). Discoverability
   wins; the spawn-only vs lifetime distinction is load-bearing and should be
   named in the API.
2. **Tee-stream byte-scanning** for the dialog marker, not PTY polling.
   Zero baseline I/O on quiet agents; activates only when the marker is seen.
3. **Upstream-opt-out check is a prerequisite** to impl: spend the first
   30 minutes of the impl ticket verifying whether Claude Code already
   exposes a flag/env var to suppress the rating prompt. If yes, prefer
   that over a watcher.
4. **Add the prompt directive** (`If a rating dialog appears, dismiss
   immediately`) to the crew/PM/polecat prompt templates — harmless,
   defensive, free.
5. **No max-uptime cap.** Session memory loss is a worse failure than the
   wedge; the watcher addresses the root cause.

**Cost estimate (if upstream opt-out doesn't exist):** ~150 LOC in
`internal/claude/rating_hook.go` (the watcher + matcher + dismissal write) +
~30 LOC in `internal/agent/registry.go` (the SessionHook lifecycle) + tests
with fixture PTY streams. Total ~200-300 LOC. If a Claude Code flag exists,
the fix collapses to setting that flag and the watcher is unnecessary.

---

## 1 · The lifecycle gap

mg-7327 shipped `internal/claude/trust_hook.go` (commit 782080b). It registers
a **PostSpawnHook** that runs once per agent for an 8-second window after
spawn, polling the PTY at 500ms intervals for the workspace-trust dialog
and dismissing it.

The rating dialog cannot be caught by that mechanism for three reasons:

1. **Timing.** The rating dialog appears at multi-hundred-hour session uptime
   (pm-pogo wedge: 293h uptime). The 8-second post-spawn window is irrelevant.
2. **Lifecycle.** `PostSpawnHook` is a goroutine that runs and exits. There
   is no hook today that runs for the agent's lifetime.
3. **Marker.** The trust dialog and rating dialog have different stdout
   patterns. Even if the post-spawn watcher ran indefinitely, its matcher
   would miss.

So the fix is **structurally distinct** — a new hook lifecycle, a new
adapter file, a new matcher. The mg-7327 architectural lesson holds:
Claude-specific PTY handling stays in `internal/claude/`, not `internal/agent/`.

---

## 2 · `SessionHook` — the new lifecycle

```go
// internal/agent/registry.go (additions)

type SessionHookFunc func(ctx context.Context, agent *Agent)

func (r *Registry) SetSessionHook(fn SessionHookFunc) { r.sessionHook = fn }

// Called from the spawn path AFTER the PostSpawnHook returns. The fn
// receives a context that is cancelled when the agent's pty exits.
// fn is expected to block (run goroutine-style) for the agent's lifetime.
func (r *Registry) invokeSessionHook(agent *Agent) {
    if r.sessionHook == nil { return }
    ctx, cancel := context.WithCancel(context.Background())
    agent.OnExit(cancel)              // cancel hook ctx when pty exits
    go r.sessionHook(ctx, agent)
}
```

**Why a new hook type, not a goroutine inside `PostSpawnHook`:**

- `PostSpawnHook` is named for its trigger (post-spawn) and has the documented
  semantic of "runs for a bounded window." Spawning a never-returning
  goroutine inside it violates that semantic and buries the actual lifecycle
  somewhere a reader has to chase.
- A `SessionHook` first-class in the Registry API is greppable: a developer
  reads `registry.go`, sees two hooks, learns the difference (spawn-only vs
  lifetime). The cost of adding a lifecycle name is a few lines.
- Future watchers (e.g. a "memory-pressure" watcher, a "stdout-flood"
  watcher) all need the same lifetime lifecycle. Adding it now gets paid back
  the next time.

**Why hooks at all, not just hardcode the rating watcher into `internal/claude/`:**

The Registry hooks are the boundary between agent-lifecycle vocabulary
(pogod-side) and Claude-specific behaviour (adapter-side). Hardcoding the
watcher into pogod would couple agent.Registry to the rating dialog. With
the hook, `internal/claude/` registers both hooks on init; the rating
watcher is a Claude-adapter concern from start to finish.

---

## 3 · Detection mechanism — tee-stream byte-scanning, not PTY polling

mg-7327's trust-dialog watcher polls the PTY at 500ms for 8 seconds — bounded
and cheap. A lifetime watcher cannot afford that polling cost (288k extra
reads per 24h × N agents). Two cheaper approaches:

### Option A — Idle-trigger polling

Watch the PTY only when stdout has been quiet for N seconds (e.g. 30s),
on the theory that a quiet PTY + a few seconds of waiting often means
a prompt is showing. **Rejected:** still bursty I/O, false positives on
agents legitimately idle in API-call blocks, complex to tune the idle
threshold.

### Option B — Tee-stream byte-scanning (recommended)

pogod already tees the agent's PTY stdout to:

- The agent's transcript (claude.jsonl)
- A live-view buffer (for `pogo agent attach`)
- The events.jsonl bus (for state-change observations)

Add a fourth tee: a `rating_dialog.Scanner` that runs alongside, byte-scanning
the stream for the marker. When matched, the scanner writes `0\n` (Dismiss)
to the PTY's stdin.

```go
// internal/claude/rating_hook.go (sketch)

const RatingDialogMarker = "1:Bad 2:Fine 3:Good 0:Dismiss"

func ratingDialogSessionHook(ctx context.Context, agent *Agent) {
    scanner := bufio.NewScanner(agent.PTYOutputTee())
    for scanner.Scan() {
        select { case <-ctx.Done(): return; default: }
        if strings.Contains(scanner.Text(), RatingDialogMarker) {
            agent.WriteStdin([]byte("0\n"))
            log.Info("rating dialog dismissed", "agent", agent.Name)
            emitEvent("rating_dialog_dismissed", agent)
        }
    }
}
```

**Cost:** roughly zero baseline. Scanner blocks on output; runs only when
output happens (which is also when interesting things happen). No baseline
polling; no cost on quiet agents.

**Marker stability across Claude Code versions:** the dialog text
`1:Bad 2:Fine 3:Good 0:Dismiss` is stable in current versions. The watcher
should make the marker configurable via env var (`POGO_RATING_DIALOG_MARKER`)
so future Claude Code text drift is fixable without redeploying pogod.

**False-positive risk:** if any other process writes the marker text to the
agent's transcript (e.g. a polecat writing about *this very design*), the
scanner triggers spuriously. Mitigation: require the marker to appear at
the *start of a line* (PTY clears) AND require it to be the most recent
output for ≥500ms before dismissing (genuine dialogs hang waiting for
input; non-dialog mentions are quickly followed by more output). This adds
a small inline buffer but eliminates the false-positive class.

---

## 4 · Upstream opt-out is the *first* impl step

mg-7327's "Why no CLI flag exists" section confirmed Claude Code keeps the
trust dialog separate from `--dangerously-skip-permissions` on purpose
(CVE-2026-33068). The rating dialog's situation is unknown to me at the
time of writing this design — there *may* be an env var or settings.json
key that disables it entirely.

**The first 30 minutes of the impl ticket are dedicated to verification:**

1. Run `claude --help` and grep output for `rating`, `prompt`, `survey`, etc.
2. Check `~/.claude/settings.json` schema for a rating/feedback toggle.
3. Search the public Claude Code documentation and recent release notes.
4. Search the Claude Code source / GitHub issues for prior mentions of
   suppressing the dialog.

If a flag exists: **prefer that.** Set it in the pogod-spawned agent's
env/settings and ship. The watcher work is then unnecessary.

If no flag exists: proceed with the watcher per §2-3.

The 30-minute verification is small enough that the impl ticket includes
it inline; it is not a separate ticket. The verification's *outcome* is
what determines the rest of the impl scope.

---

## 5 · Belt-and-suspenders: prompt directive

Add to crew/PM/polecat prompt templates:

> If at any point you see a Claude Code session-rating dialog
> (`1:Bad 2:Fine 3:Good 0:Dismiss`) appear in your output, immediately
> respond with `0` (Dismiss) and continue your work.

**Acknowledged limitation:** the dialog captures stdin *before* Claude
reads its next prompt. Once captured, Claude cannot reply because its
own input is gone. The directive is therefore mostly useless in the
acute case — the wedge has already happened by the time Claude could
act.

**Why include it anyway:** harmless, trivial (~3 lines per template),
and catches the rare case where the dialog appears at a moment Claude is
already mid-response and can dispatch a `0` write through some other
channel (mailbox response, tool call output that gets interpreted as
stdin, etc.). Free safety margin.

---

## 6 · No max-uptime cap

Question §5 of the ticket: pre-emptively restart agents after N hours to
avoid the dialog-trigger threshold entirely.

**Rejected.** Three reasons:

1. **Session memory loss** is a worse failure than the wedge. The wedge is
   recoverable via mayor's stall-watch + restart; pre-emptive restart loses
   the in-conversation state every cycle. Crew agents in particular need
   long-running state (mayor's ongoing coordination, pm-pogo's sweep
   context, architect's accumulated memory references).
2. **The dialog trigger is non-deterministic.** Claude Code may fire the
   dialog at 293h, 50h, or 500h. A uniform N-hour cap either over-restarts
   (small N) or doesn't reliably catch the wedge (large N).
3. **The watcher addresses the root cause directly.** If the watcher works,
   there's no need for the cap.

Worth naming in the design only to record that we considered it.

---

## 7 · Failure-stacking observation (UPDATE 2026-05-16)

The ticket UPDATE flagged: pm-onethird's 03:34Z rating-dialog wedge
co-occurred with the mg-8e5d schedule-purge. Any mitigation that depends on
cron-fired heartbeats fails when the schedule registry has purged the
agent's heartbeats.

This design satisfies that constraint by construction: the SessionHook
runs **inside pogod's PTY-managing goroutine for the agent**, not driven by
`pogo schedule`. Pogod is the agent process from the harness side, so the
hook exists as long as the agent's PTY exists. mg-8e5d's schedule purges
have zero effect on this watcher's lifecycle.

This is a useful architectural property: PTY-side watchers are immune to
substrate failures that affect agent-side schedules. Document it as a
general principle once both fixes land.

---

## 8 · Open choices for Daniel

These are the only unresolved points:

1. **Confirm Option A (new `SessionHook`) over Option B (goroutine inside
   `PostSpawnHook`).** Recommended A per §2 (discoverability). Daniel may
   prefer B for minimal surface area, accepting the hidden-lifecycle cost.
2. **False-positive mitigation strictness — require ≥500ms idle after
   marker before dismissing?** Recommended yes per §3. Daniel may want
   immediate dismissal (simpler; tolerates the rare false-positive).
3. **Include the prompt-template directive (§5)?** Recommended yes
   (harmless, ~3 lines per template). Daniel may want to keep prompts
   uncluttered.

## 9 · Routing & rollout

Per `feedback_design_vs_exec_routing`:

- **Architect (this doc):** design complete; pending Daniel's §8 picks.
- **Polecat-executed impl, once §8 lands** — mayor dispatches:
  1. **First 30 min: verify upstream opt-out per §4.** If found, ship the
     opt-out and close the impl ticket; the rest of this rollout is moot.
  2. `internal/agent/registry.go` — add `SessionHook` type, setter, and
     invocation in the spawn path post-PostSpawnHook return.
  3. `internal/claude/rating_hook.go` — new file. Scanner over PTY tee
     stream; marker match per §3; ≥500ms idle gate per §8.2 if confirmed;
     `0\n` write on detection; event emission.
  4. `internal/claude/init.go` (or wherever hooks register) — wire the
     rating watcher onto the new SessionHook lifecycle.
  5. Tests: fixture PTY streams with marker present + absent +
     marker-mentioned-in-transcript-without-dialog (false-positive case);
     verify dismissal write + event emission.
  6. Prompt-template edit per §5 (if §8.3 picks yes) — `pm-template.md`,
     polecat default, crew template defaults.

Rough sizing: items 2-5 are one polecat run, ~200-300 LOC total. Item 6 is
a tiny prompt-doc edit. Total impl ~1 polecat session.

**References:** mg-ef6b (this directive). mg-7327 (commit 782080b —
trust-dialog hook precedent; adapter-boundary pattern). mg-6ac4 (review
decision underpinning the `internal/claude/` adapter location). mg-8e5d
(schedule-purge sibling — drove the §7 substrate-immunity observation).
mg-83ef (mayor-loop sibling). `feedback_design_vs_exec_routing`,
`feedback_dismiss_rating_dialogs` (pm-pogo memory capturing the failure
pattern).
