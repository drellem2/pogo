# mg Work Item Store: Domain-Neutrality Audit

**Work item:** mg-854d (E4)
**Scope:** mg's data model + commands. Read-only audit; no code changes here.
**Question:** Are work items domain-neutral (research, triage, writing) or do
they bake in coding-specific assumptions?

## TL;DR

mg is **mostly domain-neutral**. The lifecycle, IDs, dependencies, mail, events,
priorities, tags, and assignees are all generic. Two fields lean coding-specific
(`branch`, and the auto-detected `repo`), but they are **optional** and degrade
gracefully — a research/writing/triage item created outside a git repo never
populates them and never needs them.

The biggest non-data assumptions are **conventions and defaults** in the README,
shell tests, and `mg new` auto-detection. None block non-coding workflows; they
just make the tool *feel* coding-shaped.

## Findings

### Item data model (`internal/workitem/workitem.go`)

The `Item` struct fields:

| Field      | Required | Coding-specific? | Notes |
|------------|----------|------------------|-------|
| `ID`       | yes      | no               | hash with configurable prefix |
| `Type`     | no       | no               | free-form string, default `"task"`; not constrained |
| `Created`  | yes      | no               | timestamp |
| `Creator`  | yes      | no               | OS username |
| `Depends`  | no       | no               | list of work item IDs (generic DAG) |
| `Tags`     | no       | no               | free-form labels |
| `Repo`     | no       | **leans yes**    | filesystem path; auto-set from `git rev-parse --show-toplevel` in `mg new` (workitem.go:22, new.go:58–60) |
| `Assignee` | no       | no               | free-form name |
| `Priority` | no       | no               | constrained to `low`/`medium`/`high` (new.go:73, edit.go:92) — generic |
| `Branch`   | no       | **yes**          | git-flavored by name; pure breadcrumb (workitem.go:25) |
| `Title`    | yes      | no               | free-form |
| `Body`     | no       | no               | markdown |

**No required field is coding-specific.** Only `ID`, `Created`, `Creator`, and
`Title` are required to create or read an item. `Repo` and `Branch` are pure
optional breadcrumbs — they are written only if set, parsed only if present, and
shown by `mg show` only when non-empty (show.go:43–54).

### Type field

- Default value `"task"` is generic (new.go:107).
- No enum, no validation — anything goes.
- `mg list` prints the type as a column but doesn't interpret it (list.go:77).
- No code path branches on type. A research, triage, or writing item can use
  `--type=research`, `--type=triage`, `--type=draft`, etc. with no friction.

### Tag conventions

- Free-form `[]string`. No reserved tags. No code path branches on tags.
- Filtering by tag is exact-match (list.go:208–222).
- Only convention surface: `mg claim` emits a `work_item_claimed` event with
  the tag list as event details (claim.go:44) — this is observability, not
  enforcement.

### Lifecycle and commands

- States: `available` / `pending` / `claimed` / `done` / `archived` / `shelved`.
  None are coding-specific.
- `mg new` — works for any title/type. Coupling is `detectRepo()` calling
  `git rev-parse --show-toplevel` (new.go:117–125). If invoked outside a git
  repo, returns `""` and `Repo` is left blank — no error. **Side effect:** for
  agents that *do* run inside a git repo (e.g., Claude in a polecat worktree),
  every new item silently inherits a `repo:` line, even research/triage items
  that aren't about that repo. Cosmetic, but worth flagging.
- `mg edit` — every field is independently editable; no constraints beyond
  priority validation. Domain-neutral (edit.go).
- `mg claim` — atomic rename, no field inspection. Domain-neutral (claim.go).
- `mg done` — atomic rename plus optional opaque `--result` JSON sidecar
  (done.go:23–29). Schema-free; the polecat protocol passes
  `{"branch": "..."}` only by convention. Domain-neutral.
- `mg show`, `mg list`, `mg assign`, `mg shelve`, `mg unshelve`, `mg reopen`,
  `mg archive`, `mg reap`, `mg schedule`, `mg snapshot`, `mg log`, `mg event`,
  `mg mail` — all domain-neutral.

### Filtering

`mg list` supports `--repo`, `--tag`, `--assignee`, `--status` filters
(list.go:14–19). The `--repo` filter is substring match against the optional
`Repo` field; for items without a repo, the filter simply excludes them. No
filter is mandatory.

### Workspace-level git (`mg snapshot` / `mg log`)

- These commit the macguffin store itself for cold-path durability — not user
  content. Generic for any item type.
- Requires git only if `mg init --git` was used; off by default.

## Recommendations

### Leave as-is (cost-of-change > benefit)

1. **Type field free-form, default `"task"`.** Already domain-neutral. Don't
   constrain it.
2. **Tag conventions.** Already free-form. Don't reserve any tags.
3. **`Repo` field.** Cheap optional breadcrumb. Useful for many non-coding
   workflows too (e.g., a writing task in a docs repo). Keep.
4. **`Priority` enum (`low`/`medium`/`high`).** Generic across domains.
5. **Workspace git snapshots.** Off by default; doesn't constrain user items.

### Consider tweaking (low-effort wins)

1. **`Branch` field is the most coding-specific field by name.** It is purely a
   breadcrumb — nothing in mg reads or branches on it. Two paths:
   - **Keep but rename** to something domain-neutral (`ref`, `context`,
     `pointer`) — minor migration cost, breaks shell scripts grepping
     `^branch:`.
   - **Keep as-is and document** that it is just a free-form string slot. Lower
     cost; matches the "convention over machinery" design philosophy in the
     README.

   Recommendation: keep as-is, document it as "git branch by convention; any
   short pointer string works." File a follow-up only if friction is reported.

2. **`mg new` auto-`detectRepo()` is silent.** For non-coding items created
   inside a git repo, `Repo` gets populated whether the user wanted it or not.
   Two options:
   - Add a `--no-repo` / `--repo=""` opt-out flag.
   - Skip auto-detection when `--type` matches a non-coding heuristic (fragile;
     don't do this).

   Recommendation: add a `--no-repo` opt-out. Cheap, opt-in, doesn't break
   existing flows. File as follow-up.

3. **README and shell tests use coding examples** (`--type=bug "Auth tokens not
   refreshing"`). Doesn't constrain the tool, but shapes user expectations.

   Recommendation: add one or two non-coding examples to the README quick start
   (e.g., `mg new --type=research "Audit Q3 incidents"`,
   `mg new --type=draft "Outline weekly digest"`). Documentation-only. File as
   follow-up.

### Do not change

- The lifecycle directory layout. It is the load-bearing primitive and works
  for any item type.
- Dependencies as work item IDs. Already a generic DAG.
- The `--result` sidecar on `mg done`. Schema-free is the right call.
- Mail, events, claim/reap/schedule. All domain-neutral.

## Follow-ups to file (if desired)

These are small, independent items the architect could schedule:

- `mg new`: add `--no-repo` flag to skip git auto-detection.
- README: add 1–2 non-coding usage examples.
- Optional: rename `Branch` field to a domain-neutral name + migration note.
  Lower priority; field is harmless as-is.

## Conclusion

A research task, a triage workflow, and a writing task all work in mg today
without modification. The data model is generic; the only coding-flavored
fields are optional breadcrumbs. Conventions in docs and `mg new`'s
auto-detection make the tool *feel* coding-shaped, but nothing prevents
non-coding use.
