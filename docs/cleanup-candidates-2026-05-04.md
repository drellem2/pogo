# docs/ cleanup pass — ambiguous bucket (mg-a374)

Filed by polecat cat-mg-a374 on 2026-05-04. The mg-a374 cleanup pass
triaged every file under `docs/` into Keep / Move-to-`~/docs/` /
Delete / Ambiguous. Keep, Move, and Delete went out in their own
commits. The four files below are the *Ambiguous* bucket — load-bearing
enough that I didn't want to delete or move them without Daniel's call.

For each: path, type (one-off report vs. design vs. options), and the
reason it's ambiguous. The whole list is small enough that Daniel can
walk it in one sitting; pick a disposition for each and either:

- file a follow-up cleanup ticket that names the survivors, or
- mail `human` with a one-line ruling per file and let the next polecat
  apply it, or
- edit this file in place to record the call (and let the next pass
  treat it as the source of truth).

## 1. `docs/claude-explore-integration.md`

- **Type:** scoping/research doc filed against mg-39b6 (commit 8b4877e,
  2026-04-29).
- **Why ambiguous:** explicitly recommends "option 3 — out of scope
  today, backlog" and contains backlog notes for a possible future MCP
  wrapper. So it reads like a one-off scoping report (delete bucket)
  *and* a backlog memo (keep bucket). Recently committed, hasn't had
  time to age.
- **Suggested call:** delete if the architect's stance ("don't pursue
  this now") is durable; keep if the backlog notes are likely to be
  consulted before mg-39b6's question gets re-asked.

## 2. `docs/declarative-orchestration.md`

- **Type:** options/comparison doc that recommends "Option 5: TOML
  Frontmatter in Prompt Files" with Phase 1/2/3 implementation sketch.
- **Why ambiguous:** Phase 1 (frontmatter parsing in
  `internal/agent/prompt.go`) and Phase 2 (auto-start roster) both
  shipped, judging by the live `auto_start` / `restart_on_crash`
  / `nudge_on_start` fields. So the doc is partially superseded.
  But the *rationale* (why TOML frontmatter and not HCL/YAML/Starlark/
  custom DSL) isn't reproduced anywhere else, and "we considered X and
  rejected it because Y" is exactly the kind of context that's
  expensive to re-derive.
- **Suggested call:** keep as architecture archeology (no churn cost,
  small file), or delete if Daniel feels the rationale is already
  captured by the shipped code's shape.

## 3. `docs/mg-domain-audit.md`

- **Type:** read-only audit filed against mg-854d (E4) — answers "is
  mg coding-specific?"
- **Why ambiguous:** the audit's main answer ("mg is mostly
  domain-neutral") is durable orientation for anyone re-asking the
  same question. But it also names three concrete follow-ups
  (rename `Branch`, add `--no-repo` flag, add non-coding examples to
  README) that may or may not have been filed/closed by now. If those
  follow-ups are filed as their own mg tickets, this doc is just a
  pointer; if they aren't, this is the only place those suggestions
  live.
- **Suggested call:** delete if the named follow-ups are tracked
  elsewhere (Daniel can confirm with `mg list --tag=...`); keep if the
  audit is still the durable record of the recommendations.

## 4. `docs/spend-tracking-design.md`

- **Type:** substantive design / recommendation. **Status: not
  implemented.** Filed against mg-d66b.
- **Why ambiguous:** unlike `product-manager-design.md` (which we
  deleted because pm-template + tests already encode the design),
  this design hasn't shipped — there's no `mg spend` subcommand in
  the tree, no `~/.macguffin/spend/` store, no `WorkItemID` field
  on the Agent struct. So either (a) the design is still the
  forward plan and should stay, or (b) the design has been shelved
  and the doc is cruft. The polecat can't tell which from the
  repo alone.
- **Suggested call:** keep if it's still the plan; delete (and
  re-derive at implementation time) if direction has changed.
- **Note:** I trimmed this doc's "Sibling docs" line to drop two
  references that pointed at files this cleanup pass deleted
  (`product-manager-design.md`, `mail-to-daniel-design.md`). If you
  decide to delete this file too, the trim is harmless; if you keep
  it, the trim was load-bearing.
