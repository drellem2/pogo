# Rate-Limit Modal Watcher — Design & Recommendation (mg-ef6b extension)

**Status:** design / recommendation. Not implemented.
**Origin:** mg-5a3d (Daniel directive 2026-05-19 — live incident on origin/main: mayor 10h27m + architect 11h32m wedged at the Claude API rate-limit-options modal; drellem2/pogo#17 filed by mayor before going down). Combined impact ~10-12h downtime + downstream blockage on mg-ea01 + mg-d8c7 dispatches.
**Author:** architect.
**Sibling doc:** [`rating-dialog-watcher-design.md`](rating-dialog-watcher-design.md) (mg-ef6b). This design **extends that watcher** to a second modal; it does not propose a separate watcher.
**Cross-product reference:** drellem2/pogo#17 — close on merge of the impl PR with comment linking mg-5a3d + impl mg ID.

## TL;DR

Extend the mg-ef6b SessionHook watcher with a **second modal matcher** for the Claude API rate-limit-options modal. One watcher, one tee-stream scan, one goroutine; matchers are entries in a per-watcher table. Pattern: marker → idle-gate → dismissal write.

The rate-limit-options matcher cannot reuse mg-ef6b's `≥500ms scanner-idle` gate because the modal **actively repaints** (auto-update animation + modal redraw is the very thing that masked `pogo agent diagnose` last-activity in GH#17). Introduce a second idle-gate mode — **events-stale** — that gates on staleness of the agent's `events.jsonl` / `claude.jsonl` writes rather than on tee-stream quiescence.

**Decisions (the three options in mg-5a3d §Why-this-needs-architect-design):**

1. **Option 1 — single watcher, multi-modal. ✓ Recommended.** Reasons in §3.
2. **Option 2 — separate watchers per modal type.** Rejected. The mg-ef6b watcher is already event-driven on a shared tee stream; spawning a second goroutine on the same stream is waste + coordination cost.
3. **Option 3 — generic "numbered list + idle" heuristic.** Rejected. Numbered lists appear legitimately in agent output (todo lists in prompts, polecat narration, this very design doc). False-positive surface is too large for an auto-keystroke action.

**Decision on critical false-positive avoidance (mg-5a3d §Concrete-requirements §4):**

Per-matcher idle-gate policy. Two modes:
- `scanner-idle` (existing, used by rating-dialog): trigger if marker is the most recent line + no further tee-stream output for `IdleAfterMarker` (500ms).
- `events-stale` (NEW, used by rate-limit-options): trigger if marker has been seen recently (last 60s) AND the agent's `events.jsonl` has not been written-to for `EventStaleness` (20 minutes = 2× stall threshold per mg-5a3d §4).

The events-stale gate disambiguates `agent wedged at modal` from `user typed /rate-limit-options to check status`:
- A user inspecting the modal will either pick a choice or hit Escape within seconds-to-minutes; the agent's reasoning loop is active throughout, emitting events. Timer never reaches 20m.
- A truly-wedged agent emits no events (Claude is blocked on stdin). After 20m of no event-bus activity, the watcher fires `1\n`.

**Cost estimate (incremental over mg-ef6b's ~200-300 LOC):** ~80 LOC for the second matcher table entry, the events-stale idle-gate mode, and an event-bus mtime-poller; ~30 LOC tests. **Total combined impl** (rating-dialog + rate-limit-options) ~280-380 LOC in one polecat session.

---

## 1 · Why this is a one-watcher problem, not two

mg-ef6b's watcher reads the agent's tee'd PTY stdout once and scans for a marker. The cost of adding a second marker is a `range matchers` loop inside the scanner. The cost of adding a second *watcher* (Option 2) is:

- A second goroutine reading the same stream (or a second tee, expanding the pogod tee fanout).
- Coordination: if both matchers fire on overlapping output, two dismissals get written. Single-watcher serializes naturally.
- More surface to reason about for future Claude Code modals: each new one becomes "did I add it to watcher A or B or do I need watcher C?"

Single watcher + matcher table is the only shape that scales.

**Architectural property:** the SessionHook is the lifecycle; the matcher table is the policy. Future modals (memory-pressure warning, network-error retry prompt, anything Claude Code adds) get one new table entry. Polecat-friendly.

---

## 2 · The matcher table — shape and semantics

```go
// internal/claude/modal_hook.go (post-impl)

type ModalMatcher struct {
    Name        string         // for logs/events: "rating-dialog", "rate-limit-options"
    LineMarker  string         // exact substring to match per scanned line
    Dismissal   []byte         // bytes to write to PTY stdin to dismiss
    IdleGate    IdleGatePolicy
}

type IdleGatePolicy struct {
    Mode             IdleMode
    IdleAfterMarker  time.Duration  // for ModeScannerIdle
    EventStaleness   time.Duration  // for ModeEventsStale
    EventLogPath     string         // for ModeEventsStale: path to events.jsonl
}

type IdleMode int
const (
    ModeScannerIdle IdleMode = iota  // mg-ef6b semantic
    ModeEventsStale                  // mg-5a3d new semantic
)

var defaultModalMatchers = []ModalMatcher{
    {
        Name:       "rating-dialog",
        LineMarker: "1:Bad 2:Fine 3:Good 0:Dismiss",
        Dismissal:  []byte("0\n"),
        IdleGate: IdleGatePolicy{
            Mode:            ModeScannerIdle,
            IdleAfterMarker: 500 * time.Millisecond,
        },
    },
    {
        Name:       "rate-limit-options",
        LineMarker: "Stop and wait for limit to reset",
        Dismissal:  []byte("1\n"),
        IdleGate: IdleGatePolicy{
            Mode:           ModeEventsStale,
            EventStaleness: 20 * time.Minute,
        },
    },
}
```

### Marker choice — why `"Stop and wait for limit to reset"` and not `"What do you want to do?"`

The ticket and GH#17 both name two candidate markers. Pick the more specific one:

- `"What do you want to do?"` — generic English, appears in plenty of legitimate output (polecat narration, agent prompts, this paragraph). High false-positive surface.
- `"Stop and wait for limit to reset"` — verbatim phrase from the Claude rate-limit modal, vanishingly unlikely to appear elsewhere. Specific enough that the idle-gate is mostly belt-and-suspenders against the *user-invoked* `/rate-limit-options` case.

If Claude Code rewords the line in a future version, expose `POGO_MODAL_RATE_LIMIT_MARKER` env-var override (parallel to the override already discussed in mg-ef6b §3 for the rating-dialog marker). Hardcoded default; env-var fallback.

### Dismissal `1\n` — confirm via mg-09b6 PTY rig before merge

mg-09b6's PTY rig (the polecat-only nudge test rig — *not* the production crew) is the right place to verify that `1\n` lands as a menu selection rather than as input-box text. The rating-dialog matcher uses `0\n` per mg-ef6b §3; same code path expected to work for `1\n` against the rate-limit modal.

If the polecat rig shows the dismissal lands as input-box text rather than a menu selection (the `NudgeSubmitDelay` bug class flagged in mg-09b6 Candidate 1), fall back to the two-write pattern (`1`, sleep 50ms, `\n`). Decide during impl; not blocking on architect.

---

## 3 · The new idle-gate mode — `ModeEventsStale`

### Why `ModeScannerIdle` does not work for rate-limit-options

mg-ef6b §3's idle gate is: "after marker is seen, require no further tee-stream output for ≥500ms before firing dismissal." For the rating dialog this is robust because the dialog appears once and Claude's reasoning loop fully halts.

The rate-limit modal does **not** halt the tee stream. From GH#17 (Clover Ross's repro):

> Claude Code's auto-update animation and modal repaint cause periodic terminal writes, so `last-activity` can look "fresh" (under 60s) even though no real LLM work is happening.

So the tee stream is *not* idle while the modal is showing — animation frames and modal redraws keep firing. A 500ms scanner-idle window will never trigger; a 60s scanner-idle window might catch some cases but is brittle to future animation cadence changes.

### The cleaner signal — agent-side event staleness

The truth we want to detect is **the agent's reasoning loop is blocked**. Pogod already observes that directly: every Claude tool call, every transcript-message, every state transition produces a write to `events.jsonl` (and/or `claude.jsonl`).

When Claude is wedged at a stdin-blocking modal, those files stop growing. They keep growing for a healthy agent (even a quiet one — sweep heartbeats, mail checks, scheduler ticks all emit events).

The `events-stale` mode is:

1. Watcher sees the rate-limit marker in tee stream → record `markerLastSeen = now`.
2. Marker may re-fire (modal repaint repeats the line) — re-record `markerLastSeen`.
3. Every 30s while `markerLastSeen` is recent (< 60s):
   - Stat the agent's `events.jsonl`.
   - If `now - eventsMtime > EventStaleness` (20m) → fire dismissal.
4. After dismissal, set a 5-minute cooldown so the same modal doesn't trigger re-dismissal in a loop if it somehow reappears.

### Why 20 minutes specifically

mg-5a3d §4 specifies "2× stall threshold (20m)." Stall threshold is 10m. Pogod's existing stall-watch fires restart at the same horizon. The 20m gate gives:

- A clear factor of safety against the user-invokes-`/rate-limit-options` case (no human stays at a menu for 20m).
- Worst-case wedge duration of 20m instead of 10+ hours — orders of magnitude better than the GH#17 incident.
- Headroom for transient event-emission gaps in healthy agents (e.g. long tool calls, waiting on external APIs). Healthy agents emit *something* — at minimum heartbeat events — well inside 20m.

### Edge: what if `events.jsonl` is itself wedged for non-modal reasons?

Possible but acceptable. If `events.jsonl` is stale for 20m AND the rate-limit marker is currently the most-recent matchable text in the tee stream, the agent is wedged regardless of the proximate cause; firing `1\n` is at worst a no-op (the modal isn't actually up so the keystroke either goes to the input box harmlessly or is ignored). The dismissal action is idempotent enough.

If pogod is the thing wedged (not just the agent), the watcher itself is dead and won't fire. That's a different failure class addressed by mg-83ef / mg-32a9.

---

## 4 · Failure-stacking constraint (inherited from mg-ef6b §7)

mg-ef6b §7 documented: any mitigation depending on cron-fired heartbeats fails when the schedule registry purges. This watcher inherits that immunity:

- Runs inside pogod's PTY-managing goroutine for the agent.
- Reads tee stream (pogod-internal).
- Reads `events.jsonl` mtime (filesystem; no schedule dependency).
- Writes to PTY stdin (pogod-internal).

No `pogo schedule` cron involvement at any step. Schedule purges (mg-8e5d class) have zero effect on this watcher's correctness.

---

## 5 · Test fixtures the polecat must include

Per mg-09b6's PTY-rig protocol — **run on a polecat, never the production crew.** Six test cases for the combined watcher:

### Rating-dialog cases (inherited from mg-ef6b):

1. Marker present in stream → dismissal fires within ~500ms.
2. Marker mentioned-in-transcript (not an active dialog) → no false-positive dismissal.

### Rate-limit-options cases (new):

3. Marker present + `events.jsonl` stale for 20m → dismissal fires.
4. Marker present + `events.jsonl` actively being written (simulates healthy agent or active user) → **no** dismissal within first 20m.
5. User-invoked `/rate-limit-options` (modal appears, user picks option 1 manually within 30s) → dismissal must **not** fire (events.jsonl had recent writes throughout; marker may have re-appeared during user inspection but never combined with 20m of event silence).
6. Marker text mentioned in agent's own transcript output (e.g. polecat reading this design doc aloud, or agent narrating a plan that includes the phrase) — no dismissal because events.jsonl is actively being written.

The PTY-rig protocol from mg-09b6 §3 covers the harness; this design adds the six cases.

---

## 6 · Out-of-scope (deliberately)

### GH#17 fix #2 — "inbox-progress watchdog"

Clover's GH#17 issue suggests a separate signal: agent mg inbox grows past N unread for >M minutes despite the process being "active." This is a **generic stall detector**, not modal-specific, and it's a stronger signal than this watcher for *any* modal-class wedge (current or future).

Out of scope for mg-5a3d (which is specifically the modal extension) but worth a follow-on ticket. The combined PTY watcher + an inbox-progress watchdog would give two independent failure-detection signals, which is the right architectural posture for a class of bugs that has now produced GH#17 + the live incident.

Recommend pm-pogo file a follow-on ticket `inbox-progress watchdog (GH#17 fix #2)` referencing this design. Not blocking mg-5a3d impl.

### Auto-notification on dismissal

mg-ef6b §3's sketch emits a `rating_dialog_dismissed` event. The rate-limit equivalent should emit `rate_limit_modal_dismissed` AND mail the responsible PM (pm-pogo for pogo-crew agents, pm-onethird for onethird agents) so they know a 20m wedge just resolved. This is a small extension to the dismissal-write code path; polecat can include without architect input. Documented here for completeness.

---

## 7 · Open choices — minimized for fast Daniel sign-off

Most decisions are locked by mg-ef6b's prior approval. The only mg-5a3d-specific choices remaining:

1. **Confirm `ModeEventsStale` over a simpler "long-fixed-window scanner-idle" (e.g. 30 min idle).** Recommended `ModeEventsStale` per §3 (more robust to animation cadence drift; uses the truth signal). Daniel may prefer the simpler scanner-idle approach accepting brittleness to future Claude Code animation changes.
2. **Confirm 20-min `EventStaleness` over a longer window (30m / 1h).** Recommended 20m per mg-5a3d §4. Daniel may prefer a longer window to be extra-conservative on false-positives (cost: longer wedge before recovery).
3. **Marker text — `"Stop and wait for limit to reset"` vs `"What do you want to do?"`.** Recommended the former per §2. Daniel may prefer matching both as an OR (mild false-positive risk increase, somewhat more robust to Claude Code line rewording).

Defaults stand absent Daniel pushback.

---

## 8 · Routing & rollout

Per `feedback_design_vs_exec_routing`:

- **Architect (this doc):** design complete; pending Daniel's §7 picks (defaults stand).
- **Polecat-executed impl, one ticket** — supersedes mg-ef6b's §9 routing in favor of a *combined* impl that handles both modals in one watcher:
  1. **First 30 min:** verify upstream opt-out per mg-ef6b §4 (separate verifications for rating-dialog and rate-limit-options — different Claude Code config surfaces). If a flag/env exists for either, prefer that over watcher coverage of that modal.
  2. `internal/agent/registry.go` — add `SessionHook` type, setter, invocation in spawn path post-PostSpawnHook (per mg-ef6b §2).
  3. `internal/claude/modal_hook.go` — new file. Tee-stream scanner; matcher table per §2 of THIS doc; per-matcher idle-gate dispatch (`ModeScannerIdle` from mg-ef6b §3, `ModeEventsStale` from §3 of this doc); dismissal writes; event emission per §6 above.
  4. `internal/claude/init.go` (or wherever hooks register) — wire the single combined modal watcher onto the new SessionHook lifecycle.
  5. Tests: six fixture cases per §5.
  6. Prompt-template edits per mg-ef6b §5 — extend the directive to cover both modals:
     > If at any point you see a Claude Code rating dialog (`1:Bad 2:Fine 3:Good 0:Dismiss`) **or rate-limit-options modal** (`Stop and wait for limit to reset`), immediately respond with `0` or `1` respectively and continue your work.
  7. PR closes drellem2/pogo#17 with comment linking mg-5a3d + the impl mg ID.

**Sizing:** one polecat session, ~280-380 LOC combined, ~30 LOC tests. The combined ticket is strictly cheaper than two sequential tickets (one watcher file, one wiring step, one test fixture set).

---

## 9 · References

- [`rating-dialog-watcher-design.md`](rating-dialog-watcher-design.md) — mg-ef6b, the parent design this extends.
- mg-ef6b — rating-dialog watcher design directive. Approved 2026-05-17.
- mg-5a3d — this directive. Filed by pm-pogo 2026-05-19 after the live incident.
- mg-09b6 — nudge-claude-code-workaround design with the PTY-rig test protocol (polecat-only, no production-crew exposure). Approved 2026-05-19 (per pm-pogo ack).
- mg-7327 — `internal/claude/trust_hook.go` (commit 782080b). The adapter-boundary precedent; rating + rate-limit watchers share that pattern.
- mg-2ba0 — agent state-machine design. This auto-restart-via-keystroke becomes a state transition under that framework; doc this design's interface contract there once mg-2ba0 lands.
- mg-83ef / mg-32a9 / mg-8e5d — sibling session-stability cluster; substrate-immunity property in §4 inherited from mg-ef6b §7.
- drellem2/pogo#17 — origin issue (Clover Ross, 2026-05-19). Close on merge.
- Live incident 2026-05-19: mayor 10h27m + architect 11h32m wedged at the rate-limit-options modal. Doctor diagnosed; pm-onethird flagged cross-product; pm-pogo restarted architect.
