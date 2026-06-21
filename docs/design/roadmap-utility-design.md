# Roadmap Utility — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-3069 (Daniel directive 2026-05-13). Open design questions answered by Daniel 2026-05-14 (Apple Reminders → architect).
**Author:** architect.
**Sibling docs:** `docs/design/spend-tracking-design.md` (the `mg spend` aggregator this consumes), `docs/product-manager-design.md` (PM toml schema, for contrast — see §3 on why we do *not* read it).

## TL;DR

A standalone, mg-only command-line utility that aggregates `mg` work items into a
product-line → initiative → item roadmap, and surfaces per-product-line token
budget vs. actual spend for cross-PM rationing.

Daniel's directive resolved six open questions. The load-bearing answers:

1. **No pogo coupling.** The utility depends on **`mg` alone** — no pogo concepts,
   no reading of `~/.pogo/` config. It lives in its **own repo**, not a `pogo`
   subcommand. Recommended repo name: **`mg-roadmap`**; binary name: **`roadmap`**.
2. **Config is a git-tracked dotfile directory: `~/.roadmap/`.** It holds
   `roadmap.toml` (product-line definitions, tag mappings, budget allocations)
   and, optionally, rendered output artifacts. It is itself a git repo; pogo (or
   the user) may `git push` it, but that is the *caller's* concern, not the
   utility's.
3. **Hierarchy: product line → initiative → item.** A *product line* is a named
   bucket in `roadmap.toml` (often but not necessarily a PM). An *initiative* is
   just an **mg tag**, by convention one tag per initiative. Items whose tags
   match no product line land in an auto-generated **Unassigned** bucket — the
   triage surface.
4. **Two output modes, one binary.** `roadmap render` = cross-PM snapshot to
   stdout. `roadmap render --live` (aliased `roadmap monitor`) = live-updating
   feed. `--json` for structured output. `--line=<name>` for a single
   product-line view (the artifact that replaces hand-rolled PM roadmap markdown).
5. **Budgets stay soft/epic-level — plus a new product-line layer.** The utility
   adds *per-product-line* advisory token allocations so Daniel can keep a
   low-priority product line from consuming the whole token budget. It surfaces
   allocation vs. actual spend; it never gates.

**Cost estimate:** new repo, ~600–900 LOC Go (CLI scaffold, `~/.roadmap` loader,
`mg list --json` adapter, tree renderer, `mg spend` adapter, `--live` mode).
No changes to `mg` core. No changes to `pogo`. One follow-up ticket on pm-pogo
to roll `roadmap render --line=…` into the PM sweep procedure.

**Deviations from the mg-3069 ticket text** (the ticket predates Daniel's
answers): the ticket proposed a `pogo roadmap` subcommand in the pogo repo and a
`pogo/docs/pogo-roadmap-design.md` deliverable. Daniel explicitly rejected the
pogo coupling and the `pogo-roadmap` name. This doc is therefore named
`roadmap-utility-design.md` and specifies a standalone `mg-roadmap` repo. It
remains in `pogo/docs/` only as a transitional review location — it should move
into the `mg-roadmap` repo once that repo exists.

---

## 1 · Architectural shape

```
        ┌──────────────┐         ┌─────────────────┐
        │  mg list     │ ──────▶ │                 │
        │  --json      │         │                 │      stdout (human / --json)
        ├──────────────┤         │   roadmap       │ ───▶ ~/.roadmap/<artifact>.md
        │  mg spend    │ ──────▶ │   (binary)      │      (optional, caller pushes)
        │  --by tag    │         │                 │
        │  --json      │         │                 │
        └──────────────┘         └────────▲────────┘
                                          │
                                 ┌────────┴────────┐
                                 │  ~/.roadmap/    │  git-tracked
                                 │   roadmap.toml  │
                                 └─────────────────┘
```

The utility is a **pure function of two inputs**: the mg work-item set (via
`mg list --json`) and `~/.roadmap/roadmap.toml`. Spend data is a third, optional
input via `mg spend --by tag --json`. It writes nothing except stdout and,
optionally, a rendered artifact into `~/.roadmap/`.

**Why mg-only, no pogo:** Daniel's framing — "we are building the
next-generation operating system for devs … broad, flexible utilities." The
utility is useful to anyone using `mg`, whether or not they run pogo. Coupling it
to pogo would be the same mistake mg-71da warned against (baking product-specific
"roadmap" shape into a composable core). The dependency arrow points
`roadmap → mg` and stops there.

**Why a separate repo, not `cmd/` in pogo:** the mg-3069 ticket recommended a
`pogo` subcommand for discoverability. Daniel overruled this — a subcommand makes
the utility *look* like a pogo feature and invites pogo-specific creep. A sibling
repo (the macguffin ↔ pogo relationship is the precedent) keeps the boundary
honest. Recommended: **`mg-roadmap`** — the `mg-` prefix advertises the sole
dependency and avoids `roadmap` colliding as an over-generic name. The compiled
binary is still just `roadmap` for CLI ergonomics. *Open for Daniel: `mg-roadmap`
vs. plain `roadmap` as the repo name — §8.*

---

## 2 · Configuration: `~/.roadmap/`

`~/.roadmap/` is a **git-tracked directory** (Daniel: "a ~/.roadmap dotfile which
is git-tracked … pogo can push that repo if the user desires"). The utility
treats it as plain config + an output sink; it never runs git itself. Layout:

```
~/.roadmap/
  roadmap.toml          # product-line definitions, tag maps, budgets
  render/               # optional: rendered artifacts land here
    pogo.md
    onethird.md
    cross-pm.md
  .git/                 # the user (or pogo) manages this
```

### `roadmap.toml` schema

```toml
# ~/.roadmap/roadmap.toml
# Product-line definitions for the `roadmap` utility.
# This file is intentionally independent of pogo's ~/.pogo/agents/pm/*.toml —
# see roadmap-utility-design.md §3 for the decoupling rationale.

# Optional: total advisory token budget across all product lines.
# If set, the utility shows each line's share of this ceiling.
total_budget = 10_000_000

[[product_line]]
name        = "pogo"
display     = "Pogo / Macguffin"
# mg tags that map an item into this product line (any-of match).
tags        = ["pogo", "macguffin", "reminders", "notifier", "pogo-darwin", "rent-a-programmer"]
# Advisory token allocation for this line. Soft — surfaced, never enforced.
budget      = 4_000_000
# Optional: curated initiative tags. If omitted, the utility groups by every
# tag the line's items carry (the "raw tags" default — lightest path).
initiatives = ["roadmap", "sandbox", "spend-tracking"]

[[product_line]]
name        = "onethird"
display     = "1/3-2/3 Conjecture"
tags        = ["onethird", "one-third", "lean", "audit"]
budget      = 5_000_000

[[product_line]]
name        = "lineara"
display     = "Lineara"
tags        = ["lineara"]
budget      = 1_000_000
```

**Field notes:**

- `tags` is how ownership is decided (see §3) — an item belongs to a product line
  if any of its mg tags appears in that line's `tags`.
- `budget` is per-line, advisory. New layer per Daniel: lets a low-priority line
  be capped *in attention* without hard enforcement.
- `initiatives` is **optional**. Present → the line's items are grouped under
  those designated tags (Daniel's "PM chooses a single tag per initiative"
  convention), with a per-line "Uncategorized" sub-bucket for items carrying the
  line's tags but no initiative tag. Absent → group by all tags (raw-tags
  fallback). Start with the fallback; let PMs curate `initiatives` retroactively
  "if the roadmap demands" (Daniel).

---

## 3 · Ownership model — and why we duplicate the tag list

Daniel: "utility doesn't use any pogo info or depend on pogo concepts … it allows
pm's to add `| tag, product-line |` and it will aggregate mg's into a table."

**Ownership rule:** an mg item belongs to product line *L* iff
`item.tags ∩ L.tags ≠ ∅`. No new field on mg items. No reading of pogo's
`pm/*.toml`. The tag→line mapping lives **only** in `~/.roadmap/roadmap.toml`.

**Conflict rule (two lines claim the same tag):** the item appears under **every**
matching line. This is intentional — a shared initiative (e.g. an `infra` tag
both lines touch) genuinely belongs to both roadmaps. The cross-PM view
deduplicates for budget math (an item's spend is attributed to its
*primary* line: the first `product_line` in file order that matches — documented,
deterministic, overridable later if it bites).

**Unassigned bucket:** any item matching zero product lines is collected under a
top-level **Unassigned** heading. This is the directive's "automatically shows
mgs without a product line so they can be assigned the right spot" — the triage
surface. `roadmap render` always shows it (possibly empty); `--json` includes it
as a line with `name: "unassigned"`.

**The duplication tradeoff — explicit architectural call-out:** pogo's
`pm/pogo.toml` already lists `tags_any = ["pogo", "macguffin", …]`. `roadmap.toml`
re-lists nearly the same tags. This redundancy is *deliberate* — it is the price
of the unix-utility independence Daniel explicitly asked for. The alternative
(have `roadmap` read `~/.pogo/agents/pm/*.toml`) would reintroduce exactly the
pogo coupling Daniel rejected. The two files will drift; that is acceptable
because they serve different masters (pogo's PM sweep scope vs. the roadmap
view). If drift becomes painful, the *right* fix is a one-shot
`roadmap init --from-pogo` importer that reads pm tomls *once* to seed
`roadmap.toml` — a convenience, not a runtime dependency. Not in v1.

---

## 4 · CLI surface

```
roadmap render                 # cross-PM roadmap, human-readable, to stdout
roadmap render --json          # same, NDJSON/JSON for scripting
roadmap render --line=pogo     # single product-line view (replaces hand-rolled PM md)
roadmap render --live          # live-updating cross-PM feed (re-reads mg on interval)
roadmap monitor                # alias for `render --live`
roadmap render --out=render/cross-pm.md   # also write artifact under ~/.roadmap/
roadmap --config <dir>         # override ~/.roadmap location (testing, multi-config)
roadmap render --since=7d      # time-window filter (uses mg item timestamps)
```

**Naming note:** the mg-3069 ticket used `--pm=pm-pogo`. Renamed to `--line=` —
the utility has no notion of "PM"; it has product lines. A product line *named*
`pogo` need not correspond to an agent. (`--pm` could be kept as a hidden alias
if PM ergonomics demand, but the canonical flag is `--line`.)

### `render` (default mode)

Reads `mg list --json` once, buckets items per §3, prints a tree:

```
ROADMAP  ·  2026-05-14  ·  3 product lines, 47 items

▸ Pogo / Macguffin                          spend 1.2M / 4.0M  (30%)
    ▸ roadmap            3 items   · mg-3069 mg-… 
    ▸ sandbox            5 items   · …
    ▸ Uncategorized      2 items   · …
▸ 1/3-2/3 Conjecture                         spend 6.9M / 5.0M  (138% ⚠)
    ▸ ex-7               8 items   · …
▸ Lineara                                    spend 0.1M / 1.0M  (10%)
    ▸ …
▸ Unassigned (3)                             ← triage: assign a product-line tag
    · mg-…  "…"
```

The `spend X / Y` line is the budget layer (§5). The `⚠` is advisory only.

### `render --live` / `monitor`

Same render, redrawn on an interval (default 30 s; `--interval=`). Re-runs
`mg list --json` + `mg spend`. Intended as the cross-PM rationing dashboard
Daniel described — "I or another agent should be able to use it to monitor and
ration budgets between pms." Plain full-screen redraw; no fancy TUI in v1.

### `--json` output

One object per product line (plus `unassigned`), each with nested initiatives and
item IDs, plus `budget`, `spend`, `spend_ratio`. This is the contract for any
downstream consumer (a PM sweep script, a dashboard, etc.).

---

## 5 · Budget layer

Per Daniel: budgets "will still continue to be soft/epic-level, but this will add
another layer … of also giving different product lines budgets, so I can ensure
that … a low priority product line doesn't spend my whole token budget."

**Mechanics:**

- Each `[[product_line]]` carries an advisory `budget` (tokens). Optional
  `total_budget` at the top level.
- The utility computes **actual spend per line** by calling
  `mg spend --by tag --json` and summing the spend of every tag in the line's
  `tags` list (deduplicating items via the primary-line rule, §3).
- It displays `spend / budget` and the ratio per line, and — if `total_budget` is
  set — each line's share of the global ceiling.
- **It never enforces.** No exit codes on overage, no gating. Over-budget lines
  get a `⚠` glyph and nothing more. This preserves `feedback_budget_semantics`
  (budgets are soft/epic-level) while adding the product-line *visibility* layer
  Daniel asked for. Rationing is a human/agent decision the tool *informs*.

**Dependency:** this mode needs `mg spend` (see `docs/design/spend-tracking-design.md`).
If `mg spend` is unavailable or returns nothing, `render` degrades gracefully —
it prints the tree with budget columns blank rather than failing. The roadmap
tree itself only needs `mg list`.

---

## 6 · How pogo interacts with it

Daniel's Q5 split into two: (1) how the binary expects input, (2) how pogo
interacts with it. Answered:

1. **Input:** `~/.roadmap/roadmap.toml` + `mg` on `$PATH`. Nothing else. A pure
   unix utility — config file in, mg data in, text out.
2. **pogo's interaction is *invocation only*, no code coupling:**
   - During a PM sweep, the PM agent runs `roadmap render --line=<name>` and
     writes the result into the product repo (or into `~/.roadmap/render/`),
     replacing today's hand-generated `roadmap.md`.
   - pogo may `git -C ~/.roadmap push` to back up config + artifacts, *if the
     user wants that* — but that is a pogo-side cron/hook, not anything the
     `roadmap` binary knows about.
   - The pm-template / per-PM prompt change to call the binary is a **follow-up
     ticket on pm-pogo**, explicitly *not* part of this design or the
     `mg-roadmap` impl tickets.

This keeps the utility a leaf: pogo depends on `roadmap` (by calling it); `roadmap`
depends on nothing pogo.

---

## 7 · Timestamps & staleness

Daniel: "if we have timestamps in mg we can use them as well." `mg list --json`
exposes each item's `created` timestamp. Uses:

- `--since=<dur>` filters the roadmap to recently-created items.
- The tree can show a staleness marker on items untouched for a long window
  (v1.1 — needs an `updated` timestamp from mg; `created` alone only gives age).
- `--json` always emits `created` so downstream consumers can sort/window.

v1 ships `created`-based `--since` and age display. An `updated`/last-transition
timestamp would need an mg core addition (out of scope here — note it as a
possible mg follow-up if staleness tracking proves valuable).

---

## 8 · Open choices for Daniel

These are the only unresolved points; everything else above follows from the
2026-05-14 directive:

1. **Repo name:** `mg-roadmap` (recommended — advertises the sole dependency) vs.
   plain `roadmap`. Binary stays `roadmap` either way.
2. **`~/.roadmap/` as a directory** (recommended — config + `render/` artifacts +
   `.git`) vs. a single flat `~/.roadmap` TOML file. The directory form is the
   only one consistent with "track roadmap output" + "push that repo."
3. **Budget config co-located** in `roadmap.toml` (recommended — it is all
   cross-line roadmap config) vs. a separate `budgets.toml`.

## 9 · Routing & rollout

Per the mg-3069 routing section and `feedback_design_vs_exec_routing`:

- **Architect (this doc):** design complete, pending Daniel review of §8.
- **Polecat-executed, once design is picked** — mayor dispatches:
  1. Create `mg-roadmap` repo + Go CLI scaffold (`render` / `monitor` commands,
     `--json`, `--config`).
  2. `~/.roadmap/roadmap.toml` loader + schema (§2).
  3. `mg list --json` adapter + ownership bucketing + tree renderer (§3–4).
  4. `mg spend --by tag --json` adapter + budget layer (§5).
  5. `--live` / `monitor` redraw loop (§4).
  6. Seed `~/.roadmap/roadmap.toml` with the three current lines
     (pogo, onethird, lineara) from the §2 example.
- **PM-side roll-in:** follow-up ticket on **pm-pogo** — update the PM sweep
  procedure to call `roadmap render --line=…` instead of hand-generating
  markdown. Not an `mg-roadmap` impl task.
- **Design questions** go to architect; **impl questions** go to the polecat.

**References:** mg-3069 (this directive). mg-71da (archived 2026-05-02 — prior
"don't bake roadmap into mg" investigation + reopen triggers; trigger #3 fired).
`docs/design/spend-tracking-design.md` (`mg spend`, the budget-layer dependency).
`docs/product-manager-design.md` §2 (pogo PM toml schema — the thing we
deliberately do *not* read).
