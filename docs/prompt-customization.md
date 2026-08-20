# Customizing pogo prompts

Pogo ships a default set of prompts at `~/.pogo/agents/`: the mayor (the
coordinator), crew agents, templates for polecats (disposable worker agents),
and the PM template + configs. You'll want to customize them. This guide
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
│   └── pm-yourproject.md             # LOCAL — you write it (extends pm-template)
├── pm/
│   ├── pm-template.md                # shipped
│   └── yourproject.toml              # LOCAL — per-PM-instance config
├── templates/
│   ├── polecat.md                    # shipped
│   ├── polecat-qa.md                 # shipped
│   ├── polecat-build-pr.md           # shipped
│   ├── polecat-triage.md             # shipped
│   ├── polecat-review.md             # shipped
│   └── polecat-architect.md          # shipped
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
pogo agent prompt show pm-yourproject
```

The output is the synthesized prompt (extends-directive expanded, drop-ins
appended, polecat templates rendered with stub preview values for `{{.Var}}`,
and the `{{.Coordinator}}` / `{{.CoordinatorTitle}}` and `{{.Worker}}` /
`{{.WorkerTitle}}` placeholders resolved to the configured coordinator and
worker names — see `[agents] coordinator` and `[agents] worker` in
[CONFIGURATION.md](CONFIGURATION.md)). Run it before and after adding a
drop-in to see the diff.

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
(e.g., `crew/pm-yourproject.md` extending `pm/pm-template.md` with `pm/yourproject.toml`)
keys drop-ins on the **crew agent's name**, not the underlying template.
For `crew/pm-yourproject.md`, drop in fragments at `dropins/pm-yourproject/`. They're
appended after the template + config are merged, so the fragment is the
last word.

If you want a customization to apply to *every* PM instance, edit
`~/.pogo/agents/pm/pm-template.md`
directly — there's no template-level drop-in slot today. That's a
canonical edit; back it up first ([Backup hygiene](#backup-hygiene)).

## Examples

### Add a house rule to the mayor

`~/.pogo/agents/dropins/mayor/00-house-style.md`:

```markdown
## House style

Spawn polecats with `priority=high` only for items tagged `urgent` or
`bug`. Everything else stays at `medium` until that PM bumps it.

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

After step 4 (`git push`), write a changelog **fragment** — a NEW file named by
your work-item id — before submitting to the refinery (the merge queue). Do
**not** append to `CHANGELOG.md`: every polecat appending to the same
`## [Unreleased]` tail collided there under concurrency, and that shared tail was
the dominant recorded merge-conflict cause (mg-d917). One file per change means
two polecats never touch the same path, so the conflict is structurally
impossible. See `changelog.d/README.md` for the format.

```bash
cat > changelog.d/mg-{{.Id}}.changed.md <<'EOF'
- {{.Task}} (mg-{{.Id}})
EOF
git add changelog.d/mg-{{.Id}}.changed.md && git commit --amend --no-edit
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
`crew/`), not the underlying template. To customize just one PM:

`~/.pogo/agents/dropins/pm-yourproject/30-project-specifics.md`:

```markdown
## pm-yourproject specifics

Treat any item touching `internal/agent/prompts/` as high-impact: open
the design doc at `docs/design/prompt-customization-design.md` before triaging.
```

This appends only to that one PM's synthesized prompt. To apply a
customization to *every* PM at once,
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
# 50-extra-tags.toml — appended onto pm/yourproject.toml
tags_any = ["pogo", "macguffin", "pogo-darwin", "rent-a-programmer", "tutorial"]
```

Until `mg-6f9f` lands, edit `~/.pogo/agents/pm/<project>.toml` directly and back it
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

The stamp records **two** hashes: `embed` (the payload the file was written
from) and `body` (the file body as written). The embed hash answers "has the
shipped version moved"; the body hash answers "has this file been changed since
we wrote it", and the two together are what let `pogo install` decline rather
than overwrite. See the conflict handling below, and
[the hand-edit detector](#detecting-a-hand-edited-prompt) for the reader that
uses the body half on a cadence.

### Reconciling on update — `.dist` files

> **Shipped** (mg-06cb). `InstallPrompts` returns the conflict set and pogod
> mails the affected agent about it at boot (mg-c3f0). This section described
> it as planned long after it landed; corrected 2026-08-20.

An embed advance under a user-edited canonical file **preserves your edit** and
writes the new embed alongside as a sidecar:

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

### `--force` semantics

> **Shipped** (mg-7c35). `InstallOpts.NoBackup` and `InstallResult.Backups`
> are live. Corrected 2026-08-20.

`pogo install --force`:

1. **Back up** any user-edited canonical file to `<name>.bak.<ISO-8601>`
   alongside the file (e.g., `mayor.md.bak.2026-05-09T14:30:00Z`).
2. **Print** the backup path to stdout so you know where it went.
3. **Then** overwrite with the embed.

To skip the backup (loud opt-out), pass `--no-backup`:

```bash
pogo install --force --no-backup    # opt-in to silent stomp
```

`pogo install` (without `--force`) never overwrites edited files at all — it
writes `.dist` instead and tells you to reconcile.

Treat `--force` as destructive anyway. The backup is a recovery path, not a
merge.

## Detecting a hand-edited prompt

The stamp on a prompt's first line records the hash of the body the installer
wrote. If the body no longer hashes to that value, the file has been edited in
place since it was installed — and you can check any file yourself with two
commands, no `.dist` sidecar and no reference checkout involved:

```bash
head -1 ~/.pogo/agents/mayor.md
tail -n +2 ~/.pogo/agents/mayor.md | shasum -a 256
```

pogod sweeps for this on a coarse interval (6h by default) and mails the agent
that can act on each edited file. The on-demand half is:

```bash
pogo check-prompt-edits          # human report, exit 1 on a finding
pogo check-prompt-edits --json   # findings plus the full classification
```

### What it reports, and what it deliberately does not

An **unstamped** file is ambiguous: it may have no upstream at all (`crew/pa.md`,
`crew/pm-*.md`, `pm/anti-drift-protocol.md` — the deployed file *is* the source),
or it may have lost a stamp it should have. Nothing in the file tells the two
apart, so the sweep never pools them into one "unknown" count. Every enumerated
file lands in exactly one bucket:

| bucket | meaning |
|---|---|
| **judged** | the shipped corpus has this path and the file is stamped. A mismatch is a finding; a match is clean. |
| `stamp-missing` | the corpus ships it and the stamp is gone. Unjudgeable — and the one unstamped case worth attention. |
| `upstream-withdrawn` | stamped by an older install, and the corpus no longer ships the path. What the stamp says is printed, but there is nothing to reconcile against. |
| `no-upstream` | not shipped and not stamped. Expected, and normally the largest bucket. |

The census prints on every run, clean or not.

### It reports; it never repairs

There is no `--fix`, and that is deliberate. Carrying a local line forward
changes the body, which stales the stamp — and the stamp cannot be recomputed
without the installer's exact canonicalisation. A tool that recomputed it anyway
would certify a body it never validated, turning an honest "unknown" into a
false "verified". You have two safe options:

- **Keep the edit.** Expect the notice again on the renotify interval (72h).
  The file reads as edited until an install rewrites it.
- **Drop the edit.** `pogo install` restores the shipped text; `--force` writes
  a `.bak` sidecar first.

### This is a different notice from a declined sync

Mail from `pogod-promptsync` is about a **shipped update that could not be
applied** because of your edits — there is a `.dist` sidecar waiting.
Reconciling that resolves both. Mail from `pogod-promptedit` on its own means
the edits exist and no shipped update has collided with them yet, which is the
case nothing used to report.


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
- `pogo check-prompt-edits --help` — the hand-edit detector's on-demand half,
  and the fullest written statement of the domain constraint it applies.
