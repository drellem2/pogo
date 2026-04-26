# Token Spend Tracking — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-d66b. **Author:** architect.
**Sibling docs:** `product-manager-design.md` (PM tier — primary consumer), `macguffin/docs/mg-flow-redesign.md` (flow consumes spend later), `mail-to-daniel-design.md` (PM digests reference roadmap).

## TL;DR

Spend is already there; we just need to harvest it. Every Claude Code session writes its token usage to `~/.claude/projects/<encoded-cwd>/<session>.jsonl` — each assistant message has a `usage` block (input/output/cache-read/cache-write tokens, model name). Combine that with the existing `events.jsonl` (which records `actor` + `pid` per work-item state transition) and the agent registry (which knows polecat name ↔ work item ID), and we can attribute tokens to mg items.

**Recommendation:**

1. **Persist `WorkItemID` on the Agent struct in pogod** — small, makes the polecat → mg-id link first-class instead of inferred.
2. **New aggregator: `mg spend`** — scans Claude transcripts + events.jsonl + agent registry, writes a per-item store at `~/.macguffin/spend/by-item/<mg-id>.json`. Append-only. No mg core schema change.
3. **Add an optional `budget` frontmatter field** on mg items (tokens). The one mg core change. Tiny — it's just another scalar field next to `priority`.
4. **PM roadmaps live in-product**, at `<repo>/docs/roadmap.md`, regenerated each PM sweep. Roadmaps are product artifacts, not agent state — they belong with the code, not under `~/.pogo/`.
5. **ROI proxy:** tokens-per-closed-mg, aggregated per tag/repo/PM. Plus override-rate as a separate PM-quality signal. Don't try to be precise; this is directional.
6. **Surface in `mg flow`** post-mg-69ba via `--show=cost` (`actual / budget`, ROI per group).

**Cost:** ~30 LOC in pogod (`WorkItemID` field + load/save), one new aggregator subcommand (~250 LOC including transcript parser + storage), one new mg frontmatter field (~10 LOC), one new section in pm-template ("regenerate roadmap.md"). Total ~300–400 LOC across pogo + macguffin.

**Not blocked on PM tier landing in production**: the data layer is independent. PMs become the prettiest consumer, but humans get value from `mg spend` immediately.

---

## 1 · Per-mg token expenditure tracking

### Where the data already lives

For every Claude Code session, transcripts at:

```
~/.claude/projects/-Users-daniel--pogo-polecats-<polecat-name>/<session-uuid>.jsonl
~/.claude/projects/-Users-daniel--pogo-agents-<agent-name>/<session-uuid>.jsonl
```

Each assistant message in the JSONL has a `usage` object:

```json
{
  "input_tokens": 6,
  "cache_creation_input_tokens": 9379,
  "cache_read_input_tokens": 16446,
  "output_tokens": 134,
  "service_tier": "standard"
}
```

Plus the model name (`claude-opus-4-7` etc.) on the same message. So **token counts per session are free**; the question is attribution to mg items.

### Attribution: polecat → mg-id

Three ways to derive the link, in increasing reliability:

1. **`events.jsonl`** (lowest cost, already exists). `work.claim` records `actor` (agent name) and `pid` plus `item_id`. We can build "this agent claimed mg-X at time T1, was claimed until T2" intervals; assistant messages in T1..T2 belong to mg-X. Works for both crew (which switch tasks) and polecats (one-task lifetime).
2. **Polecat name convention.** Polecats spawned with `--id=<mg-id>` produce `pc-<id>` names that map predictably. Less reliable: polecats are also spawned without an mg id, polecat names get reused, and crew agents don't follow this convention at all.
3. **Persist `WorkItemID` on the Agent struct in pogod.** Authoritative. Set at spawn, kept across restarts, queryable via the existing agent registry.

### Recommendation: 1 + 3

- **Use `events.jsonl` for the time-interval join** — it's the truth source for "what was this agent working on at time T." Already complete; no new instrumentation.
- **Add `WorkItemID` to the Agent struct** for the live-attribution case (a running polecat's spend should attribute even before its `work.done` event lands). ~30 LOC: field on the struct, plumbed through `SpawnPolecatAPIRequest` (already has `Id`), persisted in the runtime state file, exposed in agent registry JSON.
- **Crew agents** (architect, mayor, pm-*, doctor) often work across many items in a session. Use the events-based interval join for them too. Periods with no claimed item attribute to a per-agent "general overhead" bucket.

### Storage

```
~/.macguffin/spend/
├── by-item/
│   ├── mg-69cb.json       # all spend events for mg-69cb
│   ├── mg-151b.json
│   └── ...
└── by-agent/
    ├── architect.json     # general-overhead bucket per agent
    ├── mayor.json
    └── ...
```

Each file is a small newline-delimited JSON log:

```jsonl
{"ts":"2026-04-26T09:30:00Z","agent":"pc-69cb","model":"claude-opus-4-7","input":6,"cache_read":16446,"cache_create":9379,"output":134,"session":"d816f9b0-..."}
{"ts":"2026-04-26T09:30:01Z",...}
```

Append-only, one line per assistant message. Trivial to grep, trivial to aggregate. The aggregator is idempotent: it knows the latest `(session, message-uuid)` it has processed and skips re-processed entries.

### Why not store in mg item frontmatter

- Frontmatter is meant to be small, stable, hand-edited. Spend grows monotonically and is machine-only.
- Concurrent writes — multiple agents working on a multi-stage mg would race on the YAML.
- Bloats `git diff` if mg items are committed.

Separate dir keeps mg core clean and avoids `mg edit` collisions.

### Why not events.jsonl entries

- Mixing observation (work.claim) with measurement (token usage per message) bloats the event log.
- events.jsonl is meant to be human-readable for `mg flow --live`; thousands of token entries would drown out state transitions.
- Different cardinality: state transitions are O(items); token entries are O(messages) — orders of magnitude more.

Keep events.jsonl as the work-state log. Use a separate spend dir for the firehose.

---

## 2 · Per-tag aggregation

Once per-mg spend exists, per-tag is a `mg list --json --tag=<x>` ⨯ `~/.macguffin/spend/by-item/*.json` join. Same for per-repo, per-assignee, per-priority.

Surface options:

```bash
mg spend --by tag                          # all tags, sorted by total
mg spend --by tag:ux                       # one tag, with item breakdown
mg spend --by repo                         # cross-product overview
mg spend --by item --tag=ux                # individual items, filtered
mg spend --by agent                        # who's spending the most
mg spend --since 7d                        # time window
```

Output format: table with columns `group | items | input | cache_read | cache_create | output | total_in | total_out`. Optional `--json` for machine consumption (PMs, dashboards).

This composes cleanly with mg-69ba's `mg flow --group-by`. Eventually `mg flow --group-by tag --show=cost` shows flow + spend in one view.

---

## 3 · Estimate vs actual

### Add one frontmatter field: `budget` (tokens, optional)

```yaml
---
id: mg-69cb
type: task
budget: 200000     # tokens (input + output combined; left undefined = no budget)
priority: medium
tags: [pogo, design]
---
```

- **Tokens, not dollars.** Pricing varies per model and changes; tokens are stable.
- **Combined input + output** for simplicity. Cache reads count toward input. (If needed later, split via `budget_input` / `budget_output` — defer.)
- **Optional.** Most existing items won't have one; that's fine. PMs set budgets on tickets they file; humans can `mg new --budget=N` or `mg edit --budget=N` if they care.
- **No defaults.** A missing `budget` means "no estimate"; not "0".

### Display

```
mg show mg-69cb
…
Budget:  200,000 tokens
Spent:   142,308 tokens (71%)  ← warning if >100%
```

In `mg flow`, when `--show=budget` is on:

```
GROUP   ITEMS  SPENT     BUDGET    USED
ux      8      4.2M      6.0M      70%
flow    3      2.1M      2.0M      105% ⚠
…
```

### Estimate provenance

Two ways `budget` gets onto a ticket:

1. **PM filings** — pm-template includes a step: "estimate the token budget when filing a ticket." Errs are fine; PMs learn over time via the actual-vs-budget feedback loop.
2. **Human filings** — `mg new --budget=N` flag. Lazy by default; we don't force humans to estimate.

Daniel can leave most tickets unbudgeted. The signal works wherever budgets exist.

---

## 4 · PM roadmaps as primary artifact

### Where roadmaps live

**In the product repo, as committed markdown** — not under `~/.pogo/`.

```
<product-repo>/docs/roadmap.md
```

For pm-pogo: `pogo/docs/roadmap.md`. For pm-onethird: `onethird-twothirds/docs/roadmap.md` (or wherever its repo is).

### Why in repo

- Roadmaps are **product artifacts**, like CHANGELOG.md or ARCHITECTURE.md. They belong with the code.
- `git log -p docs/roadmap.md` gives Daniel the history of how product direction evolved — high-leverage view.
- Survives PM lifecycle (PM crashes, PM is killed, PM is replaced). The roadmap doesn't die with the agent.
- Reviewable in PRs if Daniel wants to comment on direction.

### Format

Recommended structure (PM template ships with this skeleton):

```markdown
# <Product> Roadmap

*Generated by pm-<product> on YYYY-MM-DD HH:MM. Manual edits will be overwritten on next sweep — push back via OVERRIDE mail or by editing the PM config.*

## Now (in flight)
- mg-aaaa — title (claimed by pc-xxx) — budget 200k / spent 142k — ETA: …
- mg-bbbb — …

## Next (queued, available)
- mg-cccc — title — budget 300k — depends-on: mg-aaaa

## Later (proposed)
- *Idea: thing X. Budget guess: 500k. Filing as mg if approved.*

## Backlog (open but no near-term plan)
- mg-dddd — …

## Recently shipped (last 7d)
- mg-eeee — closed 2026-04-25 — actual 187k vs budget 200k (94%)

## Trajectory
- 7d throughput: 12 items closed, 2.4M tokens spent
- Bottleneck: tag:ux median age 4d (vs 1d a week ago) — investigating
```

PM regenerates this file each sweep. Daniel reads it as a static markdown file (locally, or via gh on the web). The mail digest links to it (`pogo/docs/roadmap.md@HEAD`).

### Why not a generated `mg roadmap` command

A live-rendered `mg roadmap --product=<x>` is tempting, but:

- Daniel doesn't get the git history view of "how direction evolved."
- It can't be reviewed in PRs.
- It doesn't survive PM downtime.

Defer the live-render command unless one is missing. The committed file is the primary artifact.

### Roadmaps are an OUTPUT, not a state

Important: PM uses its TOML config + mg state + spend data + feedback memory as **inputs**, regenerates roadmap.md as **output**. The roadmap is a derived artifact. If it gets stale, the PM is sick — restart it.

---

## 5 · ROI computation

### Numerator: closed mg items

- Not "merged commits" — an mg can produce multiple commits (revisions, fixes, follow-ups). The unit of value is the closed work item.
- Not "PM self-assessed value" — too gameable, too fluffy.
- **Closed mg's** is the cleanest practical proxy. Augmentations (priority-weighted closures, exclude-trivial) are easy to add later if needed.

### Denominator: tokens spent on the item

Sum of input + output + cache (with cache_read counted at the discounted rate, if we ever care about dollar-cost; tokens-only is fine to start).

### Metric: tokens-per-closed-mg

Per tag/repo/PM/assignee, surfaced via `mg spend --by <axis> --metric=roi` or as a column in the roadmap "Trajectory" section.

```
TAG     CLOSED  TOTAL_TOKENS  PER_CLOSED
ux      18      4.2M          233k
flow    3       6.5M          2.2M     ← outlier, investigate
infra   12      1.8M          150k
```

**Don't over-engineer.** This is a directional signal. PMs see "this product line spends 2M tokens per closed mg vs another's 200k" and ask "why?" — not "exactly how much?" Precision is wasted here; orders-of-magnitude differences are what matter.

### Override-rate as a complementary PM-quality metric

PMs can produce closed mg's that Daniel later overrides (revert UX change, deprecate the wrong feature). Track override-count per PM as a separate signal:

```
PM            CLOSED  OVERRIDES  OVERRIDE_RATE
pm-pogo       12      1          8.3%
pm-onethird   3       0          0%
```

This is a **PM-quality** signal, not a product-ROI signal. Surface it in the PM tier (digest section, `pogo agent status pm-<x>`), not in `mg spend`. Out-of-scope for the spend-tracking core but worth implementing as part of pm-template.

---

## Source data inventory

| Source | What | Where | Notes |
|---|---|---|---|
| Claude transcripts | per-message token usage, model | `~/.claude/projects/-Users-daniel--<encoded>/<session>.jsonl` | Already produced for free by Claude Code. |
| events.jsonl | work-item claim/done/etc with actor + pid | `~/.macguffin/log/events.jsonl` (mg-4258) | Time-interval join source. |
| Agent registry | running agents + their state | pogod in-process + runtime state file | Add `WorkItemID` field. |
| mg metadata | tags, repo, priority, depends, assignee | `~/.macguffin/work/<status>/<id>.md` frontmatter | Add optional `budget` field. |
| Polecat dirs | exit code, worktree info | `~/.pogo/polecats/<id>/` | Useful for "did this polecat actually finish?" — orthogonal to spend. |

The aggregator reads all five, joins, writes to `~/.macguffin/spend/by-item/`.

---

## 6 · Coordination questions answered

### Does this depend on the PM tier landing first?

**No.** Spend tracking is independent infra. PMs are the prettiest consumer; if absent, humans use `mg spend` directly and roadmaps don't get auto-generated. Ship spend independently; roadmap-regen is a pm-template addition that lands when PMs land (mg-69cb tickets are in flight).

### Does this affect mg core?

**Minimally, yes** — one new optional frontmatter field (`budget`). No change to mg's data model otherwise. Storage is in a separate `~/.macguffin/spend/` dir; access is via `mg spend` subcommand. mg's existing commands are unaffected.

### Cross-references with mg-69ba (flow redesign)

Spend integrates cleanly:

- `mg flow --show=cost` (added post-spend-landing) shows actual + budget per group.
- `mg flow --group-by tag --show=cost` is the PM's primary product-flow view.
- `mg flow --group-by tag --metric=roi` — directional ROI per theme.

Not blocking either way. Implement in this order: spend-tracking → flow grouping (mg-69ba) → flow cost integration.

---

## 7 · v0 scope (explicit)

### In

- `WorkItemID` field on Agent struct (~30 LOC, pogod).
- `~/.macguffin/spend/` storage layout.
- `mg spend` subcommand with `--by {item, tag, tag:<v>, repo, agent, priority, assignee}` and `--since <duration>` flags.
- Append-only NDJSON per item.
- Aggregator: scan Claude transcripts + events.jsonl + agent registry; idempotent.
- Optional `budget` frontmatter field on mg items.
- pm-template addition: "regenerate `<repo>/docs/roadmap.md` each sweep."

### Out (deferred)

- **Dollar-cost conversion.** Tokens only for now. Add a model-pricing config later if Daniel wants $-denominated views.
- **Live spend dashboard.** `mg spend` is on-demand. Tail mode (`--live`) deferred.
- **Per-message granularity beyond input/output/cache.** Don't track tool-use timing, retries, etc. yet.
- **Scraping non-Claude-Code agents.** This design assumes all agents are Claude Code sessions. If we ever run a different runtime (per ARCHITECTURE.md "agent contract is runtime-agnostic"), add a per-runtime parser.
- **Cross-machine spend aggregation.** Single-machine for v0; multi-machine sync is a later concern.
- **`mg flow --show=cost` integration.** Lands after mg-69ba's flow grouping. Separate ticket.
- **PM-quality dashboards (override-rate trends, PM-vs-PM comparisons).** Surfaceable from spend data + events.jsonl, but not in this ticket.

---

## 8 · Failure modes & mitigations

| Mode | Mitigation |
|---|---|
| **Transcript file format changes** (Claude Code version bump) | Aggregator parser is small; pin to known fields (`message.usage.{input,output,cache_*}_tokens`); skip-and-log unknown shapes. |
| **Session bleeds across multiple mg items** (crew agents) | Time-interval join via events.jsonl: an assistant message attributes to whichever item the actor had `claimed` at that timestamp. If none, attributes to per-agent overhead bucket. |
| **Polecat ID reuse** | Spawning logic should make IDs unique (already does). Aggregator keys on `(session-uuid, message-uuid)` to dedupe; even if names collide, sessions don't. |
| **Aggregator double-counts on re-run** | Idempotent: key on `(session, message-uuid)`, skip already-stored entries. |
| **Budget set unrealistically low** | The signal is "actual/budget"; >100% just shows red. PM learns over time via feedback loop; humans tune by hand. No enforcement. |
| **Roadmap regen overwrites manual edits** | The header explicitly says "manual edits will be overwritten." Daniel pushes back via OVERRIDE mail (PM updates feedback memory, regenerates differently next sweep) or by editing PM config. |
| **WorkItemID stale on a crew agent** | Crew agents work across many items; the field reflects "current" item. For attribution, prefer events.jsonl interval join over WorkItemID. WorkItemID is a hint for live views (`pogo agent list` shows what each agent is on right now). |
| **Aggregator falls behind** | Make it fast: it processes only new entries since last run. For >1M-message backlogs, a one-time bulk pass; steady-state is incremental. Run on-demand or via a cron during PM sweeps. |
| **Spend dir grows unbounded** | NDJSON per item; sizes are tiny (a few hundred messages = tens of KB). At pathological scale, rotate to `spend/archive/<year-month>/`. Defer. |

---

## 9 · Implementation sketch (small)

```
pogo/
  internal/agent/
    agent.go            # +1 field (WorkItemID), +1 line each in marshal/unmarshal
    api.go              # plumb spawnReq.Id → agent.WorkItemID

macguffin/
  cmd/mg/
    spend.go            # NEW — `mg spend` subcommand
    new.go              # +1 flag (--budget)
    edit.go             # +1 flag (--budget)
  internal/spend/       # NEW
    aggregator.go       # scan transcripts, join with events, write store
    transcript.go       # parse claude .jsonl files
    store.go            # read/write ~/.macguffin/spend/ NDJSON
    spend.go            # query API for `mg spend` and PM consumers
  internal/workitem/
    workitem.go         # +1 field (Budget *int) in Item, +parsing

pogo/agents/pm/
  pm-template.md        # +1 section: "regenerate <repo>/docs/roadmap.md"
                        # +1 input: read mg spend by tag for product
                        # +1 output: roadmap.md committed via PM (or via a tool the PM invokes)
```

That's the whole footprint.

### A note on PMs committing files

PMs need write access to a product repo to commit roadmap.md. Currently PMs are crew agents that don't push to main (per mg-69cb §4). Two options:

1. **Relax the constraint for `docs/roadmap.md` only** — PM may commit and push that one file; it's the PM's primary artifact and reverting a roadmap edit is trivial.
2. **PM stages the file locally; refinery handles the merge.** Cleaner architecturally but requires more plumbing.

**Recommendation:** option 1 for v0 — narrowly scoped, reversible, matches the PM's mini-CEO authority for its product. Document it in the PM template as "the only file you may push directly."

---

## 10 · Recommendation summary

1. **Harvest existing transcript data** — token usage is already there, no new instrumentation.
2. **Persist `WorkItemID`** on Agent struct in pogod.
3. **New `~/.macguffin/spend/` store** — append-only NDJSON, separate from mg core.
4. **New `mg spend` subcommand** — query + aggregate by item/tag/repo/agent.
5. **One mg core change**: optional `budget` frontmatter field (tokens).
6. **PM roadmaps live in-product** at `<repo>/docs/roadmap.md`, regenerated each sweep, committed by PM (narrow exception to PM's "no main push" rule).
7. **ROI = tokens-per-closed-mg**, directional. Override-rate as a complementary PM-quality signal.
8. **Compose with `mg flow`** post-mg-69ba via `--show=cost`.

Total surface area: ~300–400 LOC across pogo + macguffin. No data already missing — the design is mostly plumbing.
