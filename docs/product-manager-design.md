# Product Manager Crew-Agent — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-69cb. **Author:** architect.
**Sibling docs:** `ARCHITECTURE.md` (system model), `mail-to-daniel-design.md` (notification path PMs depend on).

## TL;DR

Add a `pm` agent **tier** between the user and the mayor. One PM per product line. PMs are long-running crew agents (no new code in pogod), driven by a `pm-template.md` prompt parameterized per product. They don't dispatch work or merge branches — they file `mg` tickets and mail summaries. Identity is config-file driven (`~/.pogo/agents/pm/<name>.toml`) listing repos, tags, and gap-detection sources. Cadence is cron (twice-daily digest) + opportunistic event-driven wake (new merge, new ticket assigned to product). **Authority is broad — PMs are mini-CEOs of their product.** They drive features, UX, deprecation, redesigns, priorities. The user is **informed** (not asked) via digest mail; they may override anytime. Structural constraints only: PMs don't spawn polecats (mayor's job), push to main (refinery's job), or edit prompts; cross-product or platform-schema changes go to the user. Pogo ships only the *generic* PM tier: the `pm-template.md` prompt and the `extends ... with config ...` crew-loader directive. Per-product configs (`pm/<name>.toml`) and crew shims (`crew/pm-<name>.md`) are user-installed under `~/.pogo/agents/`; nothing product-specific lives in the repo.

---

## 1 · PM identity & lifecycle

### Recommendation

**Daniel-invoked, config-driven, long-running crew agent.** Same lifecycle as mayor and architect.

```
pogo agent start pm-pogo            # bring up
pogo agent stop  pm-pogo            # take down
pogo agent list                     # see all PMs alongside mayor/architect
```

A PM is just a crew agent whose prompt file is the PM template, parameterized by a config file:

```
~/.pogo/agents/pm/
├── pm-template.md              # the shared PM prompt (shipped by pogo)
└── <product>.toml              # per-product config (user-installed)
```

The crew loader reads `~/.pogo/agents/crew/pm-<product>.md` and finds a one-line directive: `extends pm-template with config pm/<product>.toml`. The agent process starts with the merged prompt. (Implementation note: this is the simplest extension to the existing crew loader; alternative is to have `pogo agent start pm-<product>` synthesize the prompt at start time and write it to `crew/pm-<product>.md`.)

Pogo ships the template and the loader directive only — the per-product config and crew shim are user-installed under `~/.pogo/agents/`.

### Why not auto-spawned per product

Auto-spawn per repo is tempting but wrong. (1) Most repos don't need a PM — only the ones with active development and a stakeholder. (2) Auto-spawn couples PM existence to repo discovery, which is too eager. (3) Daniel's portfolio is small enough that explicit `pogo agent start` is the right granularity.

### Restart-on-crash, handoff-on-context-full

Same as other crew agents. PMs are long-lived but stateless across handoffs — their state is the config file plus the on-disk artifacts they read (mg, gh, transcripts).

---

## 2 · What a "product line" is

### Recommendation

**Product = (repo set, tag set, optional path filter).** Defined in the PM's config file. Don't add a `product` field to mg items.

Example user-installed config (lives at `~/.pogo/agents/pm/<product>.toml` — not shipped in the pogo repo):

```toml
# ~/.pogo/agents/pm/pogo.toml
name        = "pm-pogo"
display     = "Pogo"
repos       = ["pogo"]
tags_any    = ["pogo"]                 # mg items with any of these tags
extra_paths = []                       # untracked dirs the PM still cares about
```

### Why not a `product` field on mg items

(1) It's redundant — `repo` + `tags` already give us 95% of the signal, and the few edge cases (cross-repo work) are cleaner as multi-repo configs than as another schema field. (2) Adds churn: every existing item would need backfilling. (3) The PM is the right place for the "what counts as my product" rule, not mg. mg stays content-agnostic.

### How a PM finds its work

```bash
mg list --repo=pogo --status=open
mg list --tag=pogo
gh issue list --repo drellem2/pogo
```

PM dedupes the union and filters by config. No new mg flag needed.

---

## 3 · Cadence

### Recommendation

**Cron-driven, twice daily, plus opportunistic wake.**

- **09:00 and 17:00 local** — PM runs a "status sweep": read mg activity, gh issues, recent merges, agent transcripts since last sweep. Produce a digest (kept in a per-PM scratchpad) and either:
  - File 0..N routine tickets directly, AND/OR
  - Mail Daniel a high-level summary IF the digest has anything novel (otherwise: silent).

- **Event-driven wake (best-effort, not v0):** later, hook into `events.jsonl` so a new merge or a high-priority ticket on the PM's product nudges the PM. v0 ships cron-only; the event hook is a v1 add when `mg flow` lands.

Cadence is set via the standard pogo cron mechanism (same one mayor uses for routines). Each PM has its own line. Twice daily is the upper bound on Daniel's information rate — anything more frequent is noise.

### Why twice daily, not hourly

Daniel said "twice a day" explicitly. Hourly cadence would defeat the purpose: PMs are macro-view, not reactive. Twice daily means each digest covers ~12h of activity, which is enough for trends to emerge.

---

## 4 · Authority boundary — mini-CEO model

### Recommendation

**PMs are mini-CEOs of their product.** They have decision-making authority across product scope: features, UX, deprecation, prioritization, redesigns, new directions. Daniel is **informed** via the sweep digest, not asked for approval. He may override at any time, and PMs accept overrides gracefully — same dynamic as a real-life CEO override of a product manager.

### What PMs may do without asking Daniel

1. **File mg tickets at any priority** — including high — for any change they think the product needs: bug fixes, UX changes, redesigns, new features, deprecations, refactors.
2. **Drive product direction:** scope features, decide trade-offs, sequence work, propose roadmaps.
3. **Change the user interface or any user-visible behavior** of their product — file the ticket, log the decision in the next digest. Daniel sees it; he overrides if he disagrees.
4. **Comment, tag, relabel, close** their own product's tickets (closing your own product's ticket is a normal product call).
5. **Mail mayor** for coordination and dispatch questions.
6. **Mail Daniel** with FYI summaries — these are **informational, not asking permission**.
7. **Propose new product lines** (mail Daniel as FYI; he overrides if he wants the proposal shelved).
8. **Reverse, redo, or escalate** their own prior decisions if a sweep surfaces new info.

### What PMs may NOT do (these are structural separations, not authority limits)

1. **Spawn polecats.** Architecturally the mayor's job — splitting dispatch across N agents creates contention on the refinery and shared state. PMs file tickets; mayor dispatches. Same rule whatever the PM's seniority.
2. **Push to main, modify branches, run the refinery.** Mechanical separation; the refinery owns merges.
3. **Edit prompt files** (no self-modification, no editing other agents' prompts). Daniel does that.
4. **Make changes outside their product.** A PM's authority is scoped to its configured repos/tags. Cross-product proposals → mail Daniel; do not act unilaterally.
5. **Change mg, pogod, or other core platform schemas / CLIs.** Those are platform decisions; mail Daniel for any proposal. (A PM may *request* a platform change via a ticket assigned to architect, but does not unilaterally drive it.)

These constraints are about clean architectural lines, not trust. They don't change as a PM's track record grows.

### "Inform, not ask" — operational pattern

Every sweep digest includes a `## Decisions I made this sweep` section listing what the PM acted on (priorities raised, features scoped, deprecations flagged, redesigns proposed). Daniel reads it. If he disagrees with a decision, he mails the PM with `OVERRIDE: <thing>` and the PM:

1. **Reverses the decision** (closes the ticket, reverts the tag, retracts the proposal, etc.).
2. **Saves the override to its feedback memory** so it doesn't re-make the same call.
3. **Acks the override** in the next digest's `## Overrides applied` section.

Override is fast, cheap, and lossless — same dynamic as a CEO walking into a PM's office and saying "no, that's not the direction." The PM does not litigate it; if Daniel changes his mind later, he can say so.

### Vision red lines (the small charter)

One narrow class still gets pre-approval: **decisions Daniel has explicitly named as his to make.** These are encoded in each PM's feedback memory as red lines:

- Cross-product / platform scope (e.g., "pogo stays portable to Linux," "macguffin stays content-agnostic").
- Commercial / licensing direction.
- Anything Daniel has previously said "let me decide that."

If a PM thinks a red line needs revisiting, it **proposes** in a digest — does not act. Red lines start near-empty; Daniel adds to them via feedback when something matters.

### Why mini-CEO over bounded-routine

Daniel said "informed > approving" and "may get overridden if I disagree." That's the mini-CEO contract — broad authority bounded by an explicit charter and a fast, cheap override path. Bounded-routine is the wrong model: it makes the PM defer on exactly the calls (UX changes, redesigns, scope decisions) that are highest-leverage and where Daniel does NOT want to be in the approval loop. Override-driven autonomy converts Daniel's review from "approve every move" to "spot-check the digest and veto rarely." That's what the PM tier is for.

### Reversibility, not authority, is the safety mechanism

The override loop only works because PM actions are cheap to reverse: tickets can be closed, tags can flip, proposals can be retracted, polecat work hasn't started yet (PMs don't spawn). The structural constraints in the "may NOT" list are exactly the irreversible / contentious actions — pushing to main, dispatching polecats, editing prompts. Keep those off the PM's plate; everything else is reversible enough for override-driven autonomy to be safe.

---

## 5 · PM ↔ mayor protocol

### Recommendation

**PMs are mg-ticket producers. Mayor is the sole dispatcher.** No direct PM→polecat link.

Cross-product coordination: **via Daniel, not peer-to-peer.** Until there's a concrete cross-product workflow that needs it, PMs do not mail each other.

Concretely:

- `pm-pogo` files mg ticket with `tags=[pogo]` → mayor sees it in `mg list --status=available` → mayor dispatches polecat as usual.
- `pm-pogo` notices something needs `pm-other`'s attention → mails Daniel ("FYI, pm-other should look at X"), Daniel decides whether to act.

### Why no PM-peer channel yet

(1) YAGNI. There are 2 PMs in the initial roster; cross-product needs are vanishingly rare. (2) Peer-to-peer agent comms is a multiplier for pathological loops (two PMs ping-ponging proposals). Routing through Daniel is a cheap, observable backstop. (3) If the need emerges, adding peer mail is a one-line config change later.

### Why no PM→polecat path

PMs are macro-view. Spawning polecats is a tactical decision (which task, in which repo, with which permissions, in which order to avoid conflicts) — that's the mayor's job. Splitting dispatch between mayor and N PMs creates contention on the refinery and on shared state.

---

## 6 · PM ↔ Daniel protocol

### Recommendation

**Mail-only, with notification via the apple-side service** (see `mail-to-daniel-design.md`). **One digest per sweep, not per event.**

- Each PM sends **at most one mail per sweep** (≤ 2/day per PM). If nothing's new and no decisions were made: silent. Combine into the digest body — don't fragment.
- Subject: `[pm-<product>] <one-line summary>`.
- Body, structured sections (in this order so Daniel can spot-check fast):
  1. `## Decisions I made this sweep` — what the PM acted on (priorities changed, UX changes filed, deprecations flagged, redesigns proposed). This is the section Daniel scans for `OVERRIDE` candidates.
  2. `## Tickets I filed` — links + one-liners.
  3. `## Trajectory vs goals` — short macro read.
  4. `## Gaps I'm watching` — things I haven't acted on yet.
  5. `## Proposals` — direction-level FYI (new product lines, scope shifts, etc.).
  6. `## Overrides applied` — acks for any `OVERRIDE:` mail received since last sweep.
- Daniel reads via the apple-side notification path. He can ignore for days; nothing breaks. He overrides anything he disagrees with by mailing the PM with `OVERRIDE: <thing>`.

### Why digest, not per-event

Per-event mail = noise. Daniel's stated preference is "informed, not approving" — he wants the macro signal, which only emerges when you batch. A 3-line digest at 5pm beats six separate one-liners between 1pm and 5pm.

### Special channel: urgent

If a PM detects something genuinely urgent (e.g., main is broken on a core repo, a security issue, a user-visible regression), it sends an out-of-band mail with subject `[pm-<product>] URGENT: ...` instead of waiting for the next digest. Urgent is rare and the PM prompt should set a high bar for invoking it.

---

## 7 · Sources for gap detection

### Recommendation

Every PM has a baseline source set; product-specific PMs add to it.

**Baseline (every PM):**

1. `mg list --repo=<repo>` and `--tag=<tag>` for open / recently-closed items.
2. `mg show <id>` for items with new comments since last sweep.
3. `gh issue list --repo <owner>/<repo>` and `gh pr list`.
4. `git log --since=<last_sweep>` per repo.
5. `~/.macguffin/log/events.jsonl` filtered by repo (when available — depends on mg-4258).
6. Refinery history (`mg show mr-*` or wherever the refinery surfaces failures) — recent merge failures are a strong signal.

**pm-pogo extras:**

7. **Polecat transcripts** under `~/.pogo/polecats/<id>/` — scan for "this UX is annoying", "had to do X manually", "couldn't figure out", "wish there was". This is the highest-signal source for UX gaps and Daniel called it out specifically.
8. Crew agent transcripts (architect, mayor, doctor) — scan for the same patterns.

**Extras for formalization-style products:**

9. Axiom-dependence output for key theorems — track regressions in what each headline result depends on.
10. Audit reports in the project repo (path TBD per repo conventions).
11. Open-goal indicators (`sorry` / `admit` counts in proof assistants, equivalent placeholders elsewhere) — track over time.

### What "scan transcripts" means concretely

A transcript scan is a grep + summarize pass, not a vector index:

```bash
grep -i -E "(annoying|frustrat|wish|couldn't|had to|why doesn't|missing)" \
  ~/.pogo/polecats/*/transcript.* | tail -200
```

The PM agent then reads the matches and decides whether they cohere into a real gap or are noise. v0 is grep-based; if it works, fine; if it doesn't, we'll consider richer techniques.

### Out-of-scope for v0

Slack history, IDE telemetry, screen recordings, anything cloud. The signal-to-cost ratio for those is bad and they're privacy-sensitive.

---

## 8 · Default-in-pogo, or just Daniel's workflow?

### Recommendation

**Optional in pogo, off by default.** Ship the PM template and config schema as part of pogo; do not auto-create any PM on `pogo init`.

### Rationale

The PM concept generalizes IF a user is running multiple product lines through pogo. Most pogo users will be single-product (or single-developer) and a PM tier is overhead. But the concept is **general enough** that it shouldn't be Daniel-only fork code:

- The idea of "long-running agent that mines transcripts and files routine tickets" is reusable for any sufficiently active codebase.
- The config schema + prompt template is small and self-contained.
- Costs zero if you don't enable it.

So: PM is a first-class **opt-in feature** in pogo. `pogo agent start pm-<x>` works on any install, given a config file. Daniel is the proof-of-concept user but the feature is general.

This also gives a clean upgrade path: if the PM tier turns out to be load-bearing (Daniel can't go back), it stays opt-in but well-supported. If it turns out to be a Daniel-specific quirk, it stays opt-in and only Daniel uses it. Either way, sunk cost on integration is zero.

---

## 9 · Choosing your initial roster

Pogo does **not** ship a default PM roster — neither the per-product TOML configs nor the `crew/pm-<name>.md` shims live in the repo. Each user installs the PMs they want under `~/.pogo/agents/`, pointing at `pm-template.md` via the `extends ... with config ...` directive.

### How to pick the first PMs

A PM only earns its keep when there is a substantive, ongoing backlog and a stakeholder who wants twice-daily macro signal. For each candidate product line, ask:

- Is there enough activity (open tickets, recent merges, transcripts) for a 12-hour digest to have something to say?
- Is the work cohesive enough to define cleanly via `repos` + `tags_any`?
- Does the user actually want to be informed at the macro level, or do they prefer to read tickets directly?

If all three are "yes," install a PM. If not, defer — a silent PM is pure overhead.

### Example (Daniel's personal install — not shipped)

Daniel runs `pm-pogo` over the pogo / macguffin repos as the proof-of-concept user. The crew shim and TOML config live under his `~/.pogo/agents/`, not in the pogo repo. Anyone else who installs pogo starts with zero PMs and adds whichever ones their workflow justifies.

Re-evaluate after a couple of weeks of any newly-installed PM running. If it's useful, keep it; if it's noisy, fix the prompt or config before adding more.

---

## 10 · Failure modes & corrections

### Failure modes to design for

| Mode | Symptom | Mitigation |
|---|---|---|
| **Too noisy** | Daniel inboxes a digest he didn't want, multiple times/day | Hard cap: max 1 mail per sweep. Feedback-memory loop: Daniel says "this section is noise" → PM raises the bar next sweep. |
| **Too quiet** | Real gaps go unflagged for days | Sweep-completion log line on every sweep so Daniel can spot long silence. If a PM stops sweeping entirely, mayor's crash-restart catches it. |
| **Redundant tickets** | Files duplicate of an existing ticket | Pre-file dedup: `mg list --tag=<product>` and substring-match titles before filing. If match score > threshold, comment on existing instead of filing new. |
| **Misses obvious gap** | Daniel asks "why didn't you flag X" | Feedback memory pattern: Daniel's correction → PM saves to `feedback_*.md` → next sweep applies the rule. Same pattern polecats use. |
| **Wrong-direction call** | PM ships a UX change Daniel disagrees with | Override loop (§4): Daniel mails `OVERRIDE: <thing>`, PM reverses + saves to feedback memory + acks in next digest. The first override on a topic is free; pattern of overrides on similar topics → re-evaluate the PM's prompt. |
| **Override storm** | Daniel finds himself overriding the same PM repeatedly within a single sweep window | Signal that the PM's charter or feedback memory is wrong. Daniel edits the PM config / prompt; or mails the PM with a broader red-line update. If chronic, `pogo agent stop pm-<x>` until corrected. |
| **Loops with mayor** | PM re-files a ticket mayor closed as out-of-scope | Check ticket history before re-filing; if mayor closed it once, mail Daniel with the disagreement rather than re-file. (PM may file high-priority normally, so this only matters when mayor explicitly rejects scope.) |
| **Wrong product scope** | PM acts on something outside its product | Boundary check before any action: ticket's `repo` / `tags` must intersect PM's config. If not, mail Daniel rather than act. |
| **Vision red-line violation** | PM acts on a Daniel-only call (e.g., licensing) | Red-line check from feedback memory before acting; if matched, switch from "act" to "propose in digest." |

### Correction protocol

Three-tier:

1. **Per-decision override** — `OVERRIDE: <thing>` mail from Daniel. PM reverses, saves to feedback memory, acks. Cheap; expected to be the most common signal.
2. **Behavioral feedback** — non-override mail with guidance ("stop filing X", "watch for Y"). Same auto-memory pattern polecats use; PM saves to `~/.pogo/agents/pm/<name>/memory/feedback_*.md` and applies next sweep.
3. **Structural change** — Daniel edits the PM prompt template or per-PM TOML config. Picked up at next handoff.

Each tier is heavier than the last; use the lightest one that fits.

### Kill switch

`pogo agent stop pm-<name>` halts a PM cleanly. The PM produces nothing; mayor and other agents continue unaffected. This is the "if it's broken, just turn it off" backstop. Tickets the PM filed stay open and reversible (mayor or Daniel can close them); no cleanup needed.

---

## Implementation sketch (small)

If greenlit, the implementation is roughly:

**Shipped by pogo:**

1. **`pm-template.md`** — shared prompt. Sections: identity (parameterized), cadence rules, authority boundary, sources to scan, output protocol, failure-mode reminders. ~200 lines.
2. **Crew loader directive in pogod** — recognize `extends pm-template with config <file>` directive in `crew/pm-*.md` and synthesize the merged prompt at agent start. ~30 LOC.

**User-installed under `~/.pogo/agents/` per PM:**

3. **`pm/<product>.toml`** — per-PM config. Schema in §2. Tiny.
4. **`crew/pm-<product>.md`** — one-line `extends pm-template with config pm/<product>.toml`.
5. **Two sweep crons** registered by the PM itself on startup (`0 9 * * *` and `0 17 * * *` with prompt `sweep`) — see `pm-template.md` §"On Startup".
6. **One launch per PM:** `pogo agent start pm-<product>`.

No new mg fields, no refinery changes, no new daemons. Total in the repo: one prompt template plus one crew-loader directive. Everything per-product is opt-in user state.

The PM tier is a new role layered on top of the existing crew-agent infrastructure. That's the point: prove the value before building anything heavy.

---

## What's NOT in scope (v0)

- Per-PM dashboards / `pogo pm status` views (use `pogo agent status pm-<name>`).
- PM-to-PM peer mail.
- PMs spawning polecats.
- Auto-creation of PMs from repo discovery.
- Slack / external comms.
- Vector indexes over transcripts (grep is fine).
- Hard SLAs ("PM must respond within X hours").

These are reasonable v1+ extensions; none are blockers for v0.
