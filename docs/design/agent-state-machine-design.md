# pogo Agent State Machine — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-2ba0 (Daniel reminder 2026-05-10 11:40Z — *"agents status show as stalled before initial output, maybe there should be an initial status like Starting"*). Composes with **gh issue drellem2/pogo#16** (CloverRoss — bridget needs an `idle` state between healthy and stalled).
**Author:** architect.
**Sibling docs:** none directly; `pogo agent diagnose` is the affected surface. `mg-783f` mayor stall-watch consumes the `health` enum; gh#16's bridget consumer drives the renderer side.

## TL;DR

Five states, not four — Daniel's directive plus gh#16 plus an obviously-needed
**Stopped** state for process-not-alive. The full enum:

| State    | Meaning                                                  | Bridget glyph |
|----------|----------------------------------------------------------|---------------|
| Starting | Process spawned + pid registered; no output yet          | 🟡 (busy)     |
| Healthy  | Activity within T_idle_threshold seconds                 | 🟢 (busy/up)  |
| Idle     | Alive; activity between T_idle and T_stalled             | ⚪ (idle)     |
| Stalled  | Alive but no activity for ≥ T_stalled seconds            | 🔴 (stalled)  |
| Stopped  | Process not alive (pid gone)                             | ⚫ (down)     |

Activity is *any* observable signal — transcript line, event-log entry, mail
send/receive, schedule fire, sweep.log write. Defined precisely in §3.

**Transitions:**

- spawn → **Starting**
- Starting → **Healthy** on first observable output, OR → **Stalled** after
  T_starting_max if still silent
- Healthy ↔ **Idle** as activity ages past / refreshes within T_idle_threshold
- Idle → **Stalled** after T_stalled_threshold
- Stalled → **Healthy** on any new activity
- Any → **Stopped** when pid no longer alive

**Thresholds:** per-agent-type defaults in pogod config, per-agent overrides
in the agent's `.toml` / `.md` frontmatter. Defaults:

| Agent type | T_starting_max | T_idle_threshold | T_stalled_threshold |
|------------|----------------|------------------|---------------------|
| polecat    | 60s            | 60s              | 600s (10m)          |
| crew       | 60s            | 300s (5m)        | 1800s (30m)         |
| pm         | 60s            | 600s (10m)       | 5400s (90m)         |

The PM thresholds are deliberately longer than the longest scheduled cron
gap (sweep-evening fires once daily; the *inter-fire* gap that matters is
mail-check at 10min; 90m T_stalled gives ~9 missed fires before red).

**JSON shape:** `health` field extended with the two new values; new
`state` object alongside carries the richer info (`since_ts`, `since_s`,
`reason`). Old consumers keep working; new consumers use `state`. Bridget's
gh#16 tiebreaker workaround can be retired once `idle` ships.

**Cost estimate:** ~250 LOC in pogod (state-resolution helper, threshold
config loader, diagnose extension) + ~50 LOC test fixtures. No breaking
change to the `health` enum; the `state` object is purely additive. Bridget
gets to drop its tiebreaker workaround — a small win on the consumer side.

---

## 1 · Background

`pogo agent diagnose` today produces a binary verdict — `healthy` or
`stalled` — based on `last_activity` mtime vs a single threshold. Two
edge cases break it:

1. **Pre-first-output.** Process spawned, pid registered, but the agent
   hasn't produced output yet (Claude warming up, prompt loading, first
   API call in flight). `last_activity` is zero (or equal to spawn time)
   so the threshold check immediately fires → reports `stalled`. Visible
   in every crew/polecat startup window for the first 5–30s. Daniel
   noticed and asked for a `Starting` state.

2. **Mid-life quiet.** An agent that's alive and producing output every
   few minutes — exactly the PM agents' steady-state — flips between
   `healthy` and `stalled` based on whether the last sweep happened
   within the threshold window. There's no representation of "alive and
   waiting" vs. "alive and stuck." gh#16 (CloverRoss, bridget) asks
   for an `idle` state to occupy this middle ground.

Both gaps are in the same state-machine; this design unifies them.

---

## 2 · The five states

```
                  spawn
                    │
                    ▼
              ┌──────────┐  first output     ┌─────────┐
              │ Starting │ ───────────────▶  │ Healthy │ ◀──┐
              └──────────┘                   └─────────┘    │
                    │                              │        │
                    │ T_starting_max elapsed       │ T_idle │ new output
                    │ still silent                 │ elapsed│
                    │                              ▼        │
                    │                          ┌──────┐     │
                    │                          │ Idle │ ───┘
                    │                          └──────┘
                    │                              │
                    │                              │ T_stalled elapsed
                    ▼                              ▼
              ┌─────────────────────────────────────────┐
              │                Stalled                  │ ◀── new output
              └─────────────────────────────────────────┘     bumps to Healthy
                                                              (direct, skips Idle)
                    
              any state ─── pid not alive ───▶ Stopped
```

### State definitions

- **Starting** — entered at process spawn. Exit conditions:
  - First observable output → Healthy
  - T_starting_max elapsed with no output → Stalled (degenerate case;
    rare; means the agent crashed at startup or hung in init)
- **Healthy** — `last_activity` within T_idle_threshold seconds of now.
- **Idle** — `last_activity` between T_idle_threshold and T_stalled_threshold.
- **Stalled** — `last_activity` ≥ T_stalled_threshold seconds ago, process
  still alive.
- **Stopped** — process not alive (pid lookup fails). Distinguishes from
  Stalled: Stalled needs a nudge; Stopped needs a restart.

### Why Stopped is included (not in Daniel's 4 or gh#16's 3)

Today's `pogo agent diagnose` may already handle process-gone via
disappearing from the agent list, but the design should be explicit:
process-gone is a *state*, not the absence of one. Two reasons:

1. Mayor's auto-restart-on-crash logic needs a clear signal — Stopped
   is the trigger. Today this is implicit (pid lookup failure).
2. Bridget's renderer needs ⚫ to distinguish "this agent crashed and
   should be restarted" from "this agent is wedged and needs a nudge"
   from "this agent has been removed entirely."

If Daniel disagrees, fold Stopped back into "absent from output"
behaviour — small change, §8 open.

---

## 3 · What counts as activity

`last_activity` must be defined precisely or every consumer interprets
differently. Sources, in order of authority:

1. **Transcript writes** — any line appended to the agent's Claude transcript
   (`~/.claude/projects/.../session.jsonl`). Authoritative for "the agent
   thought something."
2. **Event-log writes** — any entry in `~/.pogo/events.log` where this agent
   is `actor`. Authoritative for "the agent did something observable."
3. **Mail send/receive** — `mg mail send` (this agent as sender) or any
   delivery to this agent's maildir. Counts as activity for both parties.
4. **Schedule fires** — when a `pogo schedule` entry fires *for this agent*
   (the receipt of the nudge prompt). Counts because it tells us the agent
   is responsive to the substrate.
5. **Sweep.log writes** — any append to `~/.pogo/agents/<type>/<name>/sweep.log`.
   Authoritative for "the agent finished a sweep cycle."

`last_activity` = MAX(mtime of each of the above). Pogod computes this
once per `diagnose` invocation, no caching (these are cheap stat calls).

Things that **do not** count as activity:
- Cron *registrations* (just config changes — no actual agent action).
- Other agents' events that mention this agent as `to` (only the *send*
  side counts — receipt requires the receiver to do something, which
  produces its own activity signal).
- Process CPU/memory deltas — too noisy, and Claude can sit in API-call
  block legitimately for minutes.

If no signal exists (very fresh agent, no transcript yet, no events),
`last_activity` is the process spawn time. This is what gates the
Starting → Stalled fallback path.

---

## 4 · Per-agent-type thresholds + per-agent overrides

### Defaults (in `~/.pogo/config.toml` or hardcoded)

```toml
[agent_state.defaults]
starting_max_s      = 60       # how long Starting can last before degenerating to Stalled

[agent_state.types.polecat]
idle_threshold_s    = 60       # active task; quick to bucket as idle if silent
stalled_threshold_s = 600      # 10m without output = something wrong

[agent_state.types.crew]
idle_threshold_s    = 300      # 5m — crew agents are slower-tempo
stalled_threshold_s = 1800     # 30m

[agent_state.types.pm]
idle_threshold_s    = 600      # 10m — matches mail-check cadence
stalled_threshold_s = 5400     # 90m — accommodates sweep gaps with 9 mail-check fires of headroom
```

### Per-agent overrides (in the agent's own config)

For agents whose work pattern doesn't match the type default — e.g. a PM
with an unusual schedule — add a `[state_thresholds]` block:

```toml
# ~/.pogo/agents/pm/onethird.toml
[state_thresholds]
idle_s    = 1800       # 30m — Lean math sessions can be long-quiet
stalled_s = 7200       # 2h
```

```markdown
<!-- ~/.pogo/agents/crew/architect.md -->
+++
state_thresholds = { idle_s = 600, stalled_s = 3600 }
+++
```

The resolution order: per-agent override → type default → hardcoded
fallback. Pogod resolves at agent registration and caches.

### Polecats: defaults only, no override

Polecat threshold tuning would require per-spawn config that nobody is
going to maintain. Polecats use the polecat type defaults always.

---

## 5 · JSON output shape (`pogo agent diagnose --json`)

### Existing shape (assumed — verify against current code before impl)

```json
{
  "agent": "pm-pogo",
  "pid": 15027,
  "last_activity": "2026-05-17T17:30:00Z",
  "health": "healthy"        // enum: "healthy" | "stalled"
}
```

### Proposed extended shape

```json
{
  "agent": "pm-pogo",
  "pid": 15027,
  "last_activity": "2026-05-17T17:30:00Z",
  "health": "idle",          // enum extended: "starting" | "healthy" | "idle" | "stalled" | "stopped"
  "state": {                 // NEW — richer object
    "name": "idle",
    "since_ts": "2026-05-17T17:30:00Z",
    "since_s": 348,
    "reason": "last_activity 348s ago; idle_threshold=600s, stalled_threshold=5400s"
  }
}
```

### Backwards-compat contract

- The `health` field stays. Existing consumers (mayor stall-watch,
  mg-783f, anything else reading the field) continue to work.
- Two new enum values, `starting` and `idle`, get added to `health`.
  Old consumers that don't recognize them should treat them as `healthy`
  (closest semantic). New consumers use the `state` object for fidelity.
- The `state` object is purely additive. Old consumers ignore it.
- Bridget's gh#16 tiebreaker workaround (currently inferring `idle` from
  `healthy`-with-stale-activity) becomes obsolete when `idle` ships;
  bridget can drop it cleanly.

### `pogo agent status` (human-readable)

```
pm-pogo        🟢 healthy    pid=15027   uptime=199h54m
pm-onethird    ⚪ idle       pid=15026   uptime=199h44m   (12m since last activity)
ped38          🟡 starting   pid=89365   uptime=4s        (no output yet)
pm-lineara     🔴 stalled    pid=67922   uptime=179h54m   (2h12m since last activity)
crashed-cat    ⚫ stopped    pid=-        last seen 2h ago
```

Glyphs match bridget's renderer (CloverRoss spec'd 🟡/🟢/🔴 in gh#16;
adding ⚪ for idle and ⚫ for stopped here to complete the set).

---

## 6 · Migration path for consumers

| Consumer | Today | After landing |
|----------|-------|---------------|
| Mayor stall-watch | reads `health == "stalled"` | unchanged; still triggers on `stalled` |
| mg-783f (stall-watch contract) | reads `health` | unchanged; `starting`/`idle` mapped to `healthy` by default unless mg-783f opts into the new states |
| Bridget (gh#16) | reads `health` + heuristic tiebreaker for idle | reads `state.name`; tiebreaker code removed |
| `pogo agent status` CLI | renders `healthy`/`stalled` | renders all 5 states with glyphs |
| Future Discord/web dashboards | n/a | new — read `state` object directly |

**gh#16 closure path:** once impl lands, the architect (or whoever ships
the impl PR) comments on gh#16 with the merged PR link + a note that
bridget's tiebreaker workaround is now unnecessary. Issue closes.

---

## 7 · Generalizability check

Per `feedback_generalizability_filter`: would another downstream consumer
hit this shape?

- **Bridget** (CloverRoss) — yes, the immediate driver via gh#16.
- **Discord bot** (mentioned in pogo roadmap) — yes, same enum.
- **Web dashboard** (mg-roadmap §9-style cross-PM view, or a future bridget
  web mode) — yes, will want `since_s` for "this agent has been idle for
  N minutes" displays.
- **Refinery** — already consumes events, not diagnose; doesn't care.
- **`mg flow --group-by agent`** — might benefit from the enum to bucket
  active vs idle agents in throughput math. Forward dep, not blocking.

The design is intentionally generic: `state.name` + `state.since_s` +
`state.reason` is enough for any renderer or alerter to make decisions.
No bridget-specific fields.

---

## 8 · Open choices for Daniel

These are the only unresolved points:

1. **Include `Stopped` as a fifth state?** Recommend yes (§2 rationale).
   Daniel may prefer to keep diagnose's output to "states for live processes"
   and let "absent from list" mean process-gone, in which case drop to 4.
2. **Threshold defaults — accept §4 as-is, or tune?** The PM 90m
   T_stalled is the most opinionated number (chosen to accommodate sweep
   gaps with headroom). Polecat 10m T_stalled may be too tight for
   long-running impl polecats. Daniel may want to set these higher or
   lower; values are starting points.
3. **`starting`/`idle` mapped to `healthy` for old consumers, or to
   their own value?** Recommend "treat as `healthy`" — least disruption.
   Alternative: ship a `--legacy-health` flag that collapses the enum to
   the old binary for one release before forcing consumers to update.

## 9 · Routing & rollout

Per `feedback_design_vs_exec_routing`:

- **Architect (this doc):** design complete; pending Daniel's §8 picks.
- **Polecat-executed impl, once §8 lands** — mayor dispatches:
  1. `internal/diagnose/state.go` — state-resolution logic, threshold
     resolver, `last_activity` aggregator per §3.
  2. `internal/diagnose/config.go` — threshold config loader (`agent_state.*`
     blocks + per-agent overrides).
  3. `cmd/pogo/diagnose.go` — extend JSON shape per §5; preserve
     backwards compat.
  4. `cmd/pogo/status.go` (or wherever the human-readable renderer lives)
     — render all 5 states with glyphs per §5.
  5. Test fixtures: each state, each transition, threshold-boundary cases,
     `last_activity` source aggregation.
  6. Bridget-side coordination: after impl lands, the impl PR includes a
     note for CloverRoss to drop the gh#16 tiebreaker; closes gh#16 with
     the merged PR link.

Rough sizing: items 1-4 are one polecat run (~600 LOC including tests).
Bridget update is a separate PR on the bridget repo, drellem2-internal
coordination. Estimated ~1 day end-to-end including PR review.

**References:** mg-2ba0 (this directive). gh issue drellem2/pogo#16
(CloverRoss). mg-783f (mayor stall-watch contract — read-only consumer).
`feedback_generalizability_filter` (drove the §7 consumer survey).
`feedback_design_vs_exec_routing` (split design from execution wording).
