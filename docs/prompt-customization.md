# Customizing pogo prompts

Pogo ships a default set of prompts at `~/.pogo/agents/` (mayor, crew, polecat
templates, PM template + configs). You'll want to customize them. This guide
covers the two safe ways to do that without losing your edits to the next
`pogo install` run.

> **Status (2026-05-09):** Drop-ins (the primary path) are shipped — `pogo agent
> prompt show` synthesizes them, and the spawn-time loaders pick them up. The
> canonical-edit safety net (`.dist` files, `--force` backups, `--no-backup`)
> is **specified but not yet shipped**: see follow-up tickets `mg-06cb`,
> `mg-7c35`, `mg-6f9f` and the design at
> [`prompt-customization-design.md`](design/prompt-customization-design.md). Until
> those land, treat editing the canonical file as "back it up first" — see
> [Backup hygiene](#backup-hygiene).

## Two paths

| Path | Use it when | What it gives you |
|---|---|---|
| **Drop-ins** (recommended) | You want to *add* rules, sections, or notes to a shipped prompt | Customizations live in a separate, install-untouched directory. `pogo install --force` never touches them. |
| **Editing the canonical file** (safety net) | You want to *delete* or *rewrite* a shipped section that drop-ins can't express by appending | Edits land in the canonical file. Conflict detection (forthcoming) writes a `.dist` sidecar when the embed advances under your edits. |

Reach for drop-ins first — they're additive, ordered, and immune to
`pogo install`. Only edit the canonical file when you genuinely need
replacement, not addition.

## Drop-ins

### Directory layout

For every shipped prompt, you can drop overlay files into a sibling
`dropins/<basename>/` directory. The basename is the filename stem, no
extension, no parent directory:

```
~/.pogo/agents/
├── mayor.md                          # shipped, hash-stamped
├── crew/
│   ├── doctor.md                     # shipped
│   └── pm-pogo.md                    # shipped (extends pm-template + pm/pogo.toml)
├── pm/
│   ├── pm-template.md                # shipped
│   └── pogo.toml                     # shipped per-instance config
├── templates/
│   ├── polecat.md                    # shipped
│   └── polecat-qa.md                 # shipped
└── dropins/                          # ← user-owned, install never touches
    ├── mayor/
    │   ├── 00-house-style.md
    │   └── 90-late-rules.md
    ├── polecat/
    │   └── 50-extra-claim-rules.md
    ├── pm-template/
    │   └── 20-mailroom-policy.md
    └── doctor/
        └── 10-extra-checks.md
```

A drop-in directory `dropins/<basename>/` can contain any number of `.md`
files. Subdirectories are ignored. Non-`.md` files are ignored.

### Lexical order

Files inside a drop-in directory are concatenated in **lexical order** (the
same convention as `systemd`, `cron.d`, and `sudoers.d`). Use numeric prefixes
to control ordering:

```
dropins/mayor/
├── 00-house-style.md     # appended first
├── 50-overrides.md       # appended second
└── 90-late-rules.md      # appended last
```

There's no config knob for ordering — the filename is the knob. Pick a number
spaced enough that you can wedge new files in later (`10`, `20`, `30` rather
than `1`, `2`, `3`).

Each fragment is appended to the base prompt verbatim, with a separating
newline if the base doesn't already end with one. Fragments are *not* parsed
as Markdown — they're concatenated. Headings and blank lines are your
responsibility.

### When to use a drop-in vs. editing the canonical

Drop-ins are **additive only by design**. Choose them when:

- You want to add a rule, section, or workflow step.
- You're amplifying or contradicting an existing rule by appending a
  stronger statement (the last word in a prompt usually wins).
- You want your customizations to survive `pogo install --force` no matter
  what the install logic does.

Edit the canonical file when:

- You need to *delete* a shipped rule outright (drop-ins can't remove text
  from the base body).
- You need to rewrite a paragraph in place rather than at the end.
- You're authoring a non-coding profile from scratch and most of the shipped
  prompt doesn't apply (consider `pogo init --minimal` instead — see
  [`customizing.md`](customizing.md)).

### Verifying a drop-in composed correctly

Use `pogo agent prompt show <name>` to print exactly what an agent will
receive, drop-ins included:

```bash
pogo agent prompt show mayor
pogo agent prompt show polecat
pogo agent prompt show pm-pogo
```

The output is the synthesized prompt (extends-directive expanded, drop-ins
appended, polecat templates rendered with stub preview values for `{{.Var}}`).
Run it before and after adding a drop-in to see the diff.

To inspect the source file alone without synthesis:

```bash
pogo agent prompt show mayor --raw
```

Resolution order is `mayor` → `crew/<name>.md` → `templates/<name>.md`.
Unknown names exit non-zero with a "not found" message.

### Drop-ins and live agents

Drop-ins are read at **spawn time**, not on every prompt eval. A *running*
crew agent or polecat won't pick up a new drop-in until it's restarted.

| Agent | How to apply a drop-in change |
|---|---|
| Mayor / crew | `pogo agent stop <name> && pogo agent start <name>`, or restart pogod |
| Polecat | The next polecat spawned from that template picks it up. A running polecat keeps its old prompt — that's intentional, mid-task prompt swaps would be confusing |

### Drop-ins and `extends`

A crew prompt that uses the `extends <template> with config <toml>` directive
(e.g., `crew/pm-pogo.md` extending `pm/pm-template.md` with `pm/pogo.toml`)
keys drop-ins on the **crew agent's name**, not the underlying template.
For `crew/pm-pogo.md`, drop in fragments at `dropins/pm-pogo/`. They're
appended after the template + config are merged, so the fragment is the
last word.

If you want a customization to apply to *every* PM instance (pm-pogo,
pm-onethird, pm-lineara, …), edit `~/.pogo/agents/pm/pm-template.md`
directly — there's no template-level drop-in slot today. That's a
canonical edit; back it up first ([Backup hygiene](#backup-hygiene)).

## Examples

### Add a house rule to the mayor

`~/.pogo/agents/dropins/mayor/00-house-style.md`:

```markdown
## House style

Spawn polecats with `priority=high` only for items tagged `urgent` or
`bug`. Everything else stays at `medium` until pm-pogo bumps it.

When mailing humans, prefer one paragraph over bullet lists — Daniel reads
mail on his phone.
```

Then verify:

```bash
pogo agent prompt show mayor | tail -15
pogo agent stop mayor && pogo agent start mayor
```

### Add an extra protocol step to the polecat template

`~/.pogo/agents/dropins/polecat/50-changelog-stamp.md`:

```markdown
## Extra: changelog stamp

After step 4 (`git push`), append a one-line summary of your change to
`CHANGELOG.md` under "Unreleased" before submitting to the refinery:

```bash
echo "- {{.Task}} (mg-{{.Id}})" >> CHANGELOG.md
git add CHANGELOG.md && git commit --amend --no-edit
```
```

The fragment can use the same `{{.Var}}` placeholders as the base template —
they're parsed together. Verify with:

```bash
pogo agent prompt show polecat | grep -A5 "changelog"
```

The next polecat spawn picks it up.

### Override a single PM's behavior

Drop-ins for a PM are keyed on the crew agent's name (the file under
`crew/`), not the underlying template. To customize just `pm-pogo`:

`~/.pogo/agents/dropins/pm-pogo/30-pogo-specifics.md`:

```markdown
## pm-pogo specifics

Treat any item touching `internal/agent/prompts/` as high-impact: open
the design doc at `docs/design/prompt-customization-design.md` before triaging.
```

This appends only to the `pm-pogo` synthesized prompt. To apply a
customization to *every* PM at once (pm-pogo, pm-onethird, pm-lineara, …),
edit `~/.pogo/agents/pm/pm-template.md` directly — there's no template-level
drop-in slot today. Back it up first.

### Customize a `pm/<instance>.toml` config

> **Planned (mg-6f9f), not yet shipped.** TOML drop-ins are designed to merge
> later-wins on top of the shipped base. The intended layout:

```
~/.pogo/agents/dropins/pm/pogo/
└── 50-extra-tags.toml
```

with a fragment like:

```toml
# 50-extra-tags.toml — appended onto pm/pogo.toml
tags_any = ["pogo", "macguffin", "pogo-darwin", "rent-a-programmer", "tutorial"]
```

Until `mg-6f9f` lands, edit `~/.pogo/agents/pm/pogo.toml` directly and back it
up first (see [Backup hygiene](#backup-hygiene)).

## Editing the canonical file

When a drop-in can't express what you want — most often, removing a shipped
rule or rewriting a paragraph in place — you edit the file under
`~/.pogo/agents/` directly. This is the safety net, not the primary path.

### What `pogo install` does today

```bash
pogo install                # default
pogo install --force        # always overwrite
```

For each shipped file, `pogo install` reads the `<!-- pogo-prompt: embed=…
body=… -->` stamp on the first line and decides:

- Stamp matches the embedded version → **skip** (no work to do).
- Stamp doesn't match (a newer pogo binary changed the embed) → **update**
  by overwriting.
- `--force` → **install** (overwrite unconditionally).

Today the gating compares the *embed* hash, not your edits. A `--force` run,
or any embed advance under an edited canonical file, **silently overwrites
your edits**. That's the failure mode the conflict-detection design (below)
fixes.

### Reconciling on update — `.dist` files (Planned: mg-06cb)

> **Not yet shipped.** This describes the designed behavior; the install code
> currently overwrites. Track via `mg show mg-06cb`.

When the conflict-detection ticket lands, an embed advance under a
user-edited canonical file will **preserve your edit** and write the new
embed alongside as a sidecar:

```
~/.pogo/agents/
├── mayor.md           # your edited version, untouched
└── mayor.md.dist      # the new shipped embed
```

`pogo install` will print a clear "conflict — wrote `<name>.dist`, please
reconcile" notice. Reconcile it manually:

```bash
cd ~/.pogo/agents
git diff mayor.md mayor.md.dist     # see what shipped changed
$EDITOR mayor.md                    # merge in the bits you want
rm mayor.md.dist                    # clear the sidecar when satisfied
```

There's no automatic three-way merge — the design treats `.dist` as a
"here's what shipped, now you decide" prompt, not a `git rebase --continue`
flow.

### `--force` semantics (Planned: mg-7c35)

> **Not yet shipped.** This describes the designed behavior; today
> `--force` overwrites silently with no backup.

When `mg-7c35` lands, `pogo install --force` will:

1. **Back up** any user-edited canonical file to `<name>.bak.<ISO-8601>`
   alongside the file (e.g., `mayor.md.bak.2026-05-09T14:30:00Z`).
2. **Print** the backup path to stdout so you know where it went.
3. **Then** overwrite with the embed.

To skip the backup (loud opt-out), pass `--no-backup`:

```bash
pogo install --force --no-backup    # opt-in to silent stomp
```

`pogo install` (without `--force`) under the new model will never overwrite
edited files at all — it writes `.dist` instead and tells you to reconcile.

Until those changes land, treat `--force` as destructive. Run a backup
yourself before invoking it on a directory you've customized.

## Backup hygiene

If you customize prompts heavily — drop-ins, canonical edits, or both —
**make `~/.pogo/agents/` a git repo**:

```bash
cd ~/.pogo/agents
git init
git add .
git commit -m "snapshot before pogo install"
```

This is the cheapest safety net pogo can recommend. After it:

- Run `git status` after every `pogo install` to see what changed.
- `git diff` makes `.dist` reconciliation trivial.
- `git checkout <file>` recovers a stomped customization.
- Commit your customizations as you make them — `~/.pogo/agents/` is local,
  the repo never has to leave the machine.

Pogo doesn't ship a `pogo agent backup` command yet (mentioned as a possible
follow-up in the design); `git init` is the right substitute and gives you
strictly more than a one-shot tarball would.

If you've already lost work to a `--force` stomp, the design's conflict
detection won't recover it retroactively — but going forward, drop-ins +
`.dist` (once shipped) make the loss case rare and the surviving cases
recoverable from `<name>.bak.*`.

## Troubleshooting

### My drop-in doesn't appear in the prompt

```bash
pogo agent prompt show <name> | grep -A2 "<text from your drop-in>"
```

If nothing matches:

- Confirm the file is at `~/.pogo/agents/dropins/<basename>/<your-file>.md`
  (basename matches the shipped file's stem — `mayor` for `mayor.md`,
  `polecat` for `templates/polecat.md`, `doctor` for `crew/doctor.md`).
- Confirm the file ends in `.md`. Other extensions are skipped.
- Confirm you're not nesting it in a subdirectory — subdirs are ignored.
- For a *running* mayor or crew agent, restart it
  (`pogo agent stop <name> && pogo agent start <name>`). Drop-ins are read
  at spawn, not on every prompt eval.

### `pogo install` overwrote my edits

Today (pre-mg-06cb): the embed advanced under your edited file and the
install code overwrote it without warning. If you have
`~/.pogo/agents/` under git, `git diff HEAD~1` shows what was lost. If you
don't — this is exactly the case the [Backup hygiene](#backup-hygiene)
section recommends preventing.

Once `mg-06cb` and `mg-7c35` land, you'll get either a `.dist` sidecar
(no `--force`) or a `.bak.<timestamp>` (with `--force`, unless `--no-backup`).

### `pogo doctor` says my prompts have drifted

That's working as designed — `CheckPromptDrift` compares the installed
embed-stamp against the binary's current embed. A canonical-file edit
*will* show as drift today because the embed hash gating treats "user edit"
and "stale" as the same thing. Once `mg-06cb` lands, drift detection will
distinguish the two cleanly.

For now, if you've intentionally edited the canonical file, you can ignore
the drift warning — but understand that `pogo install` will overwrite on its
next run.

## See also

- [`customizing.md`](customizing.md) — agent roster, polecat templates,
  refinery toggle. The broader "how to bend pogo to your workflow" guide.
- [`prompt-customization-design.md`](design/prompt-customization-design.md) —
  the design doc that this guide implements. Read it for the rationale,
  the failure-mode analysis, and the file-class boundary.
- `pogo agent prompt --help` — `list`, `show`, `init`, `install`, `create`.
