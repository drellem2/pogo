# Customizing pogo

Pogo's defaults — a coding mayor, a polecat that opens a feature branch, and a
refinery that merges to `main` — are one specific shape. The pieces underneath
(prompt files, frontmatter, `config.toml`) are general. This guide walks through
the three knobs you'll reach for first:

1. **Define your own agent roster** — pick which crew agents auto-start, whether
   pogod restarts them on crash, and what they hear on boot.
2. **Author a custom polecat template** — change what one-shot workers do, and
   whether they get a git worktree at all.
3. **Toggle the refinery off** — for non-coding workflows where there's nothing
   to merge.

If you want to read the same ideas applied end-to-end to a non-coding workflow
first, jump to [`docs/examples/research-triage/`](examples/research-triage/README.md)
and come back here when you want the reference.

> **Before you start:** `pogo init` scaffolds `~/.pogo/agents/` for you. Pass
> `--minimal` if you're building a non-coding workflow and don't want the
> shipped coding-profile prompts in your way. Without `--minimal`, you get the
> full mayor + `crew/doctor` + `polecat` + `polecat-qa` set, which is the right
> starting point for code-shaped work.

## 1. The agent roster

In pogo, **the prompt files in `~/.pogo/agents/` *are* the agent roster.** You
don't declare agents in a separate config — adding a file under `crew/` makes a
new crew agent exist; deleting one removes it. Per-agent runtime properties
live in TOML frontmatter at the top of each prompt file.

### Frontmatter fields

A crew prompt looks like this:

```markdown
+++
auto_start = true
restart_on_crash = true
nudge_on_start = "You are now running. Begin your coordination loop."
+++

# Mayor

You are the mayor — the coordinator for this pogo workspace...
```

Three keys worth knowing:

| Key | Type | What it does | Default |
|---|---|---|---|
| `auto_start` | bool | pogod starts this agent on daemon boot | `false` |
| `restart_on_crash` | bool | pogod restarts the agent if it exits unexpectedly | `true` for crew, `false` for polecats |
| `nudge_on_start` | string | Message sent to the agent's PTY immediately after spawn | empty |

There are two more (`command` for per-agent command overrides, `worktree` for
polecat templates — see below) but those three carry the day.

Frontmatter is parsed at spawn time. Edit a file, restart the agent, the new
settings apply. There's no live reload — pogod won't notice that you flipped
`auto_start` on a running agent until you restart it.

### Add a custom crew agent

Say you want a `triager` crew agent that wakes up on boot, watches a research
queue, and gets restarted if it crashes. Drop a file at
`~/.pogo/agents/crew/triager.md`:

```markdown
+++
auto_start = true
restart_on_crash = true
nudge_on_start = "Check mail and the research queue, then settle into your loop."
+++

# Triager

You are the triager. You watch `mg list --tag=research --status=available`,
spawn polecats for unblocked items, and review their notes when they file...
```

Then either restart pogod (`pogo server stop && pogo server start`) or start
the agent manually right now without rebooting (`pogo agent start triager`).
The next pogod boot will pick it up automatically because of `auto_start = true`.

`pogo agent list` should now show `triager` running.

### Opt out of auto-start

Some crew agents shouldn't run all the time — the shipped `doctor` is a good
example. Its prompt has no `auto_start` line at all, which means it defaults
to `false`. You start it on demand:

```bash
pogo agent start doctor
```

If you want to *temporarily* keep an existing crew agent from booting (without
deleting its file), open the prompt and set `auto_start = false` at the top.

### Stop pogod from restarting a flaky agent

If a crew agent is in a crash loop and you'd rather not have pogod respawn it
while you debug, set `restart_on_crash = false` in its frontmatter and restart
the daemon. The agent will run once on boot (if `auto_start = true`); if it
exits, it stays down.

This is the same knob that makes polecats ephemeral by default — they're set
to `restart_on_crash = false` because they're supposed to exit (well, get
stopped by the mayor) once their work is done.

### Shape the boot nudge

`nudge_on_start` is the first thing the agent reads after spawn. The default
mayor uses it to kick off its coordination loop:

```toml
nudge_on_start = "You are now running. Begin your coordination loop."
```

You can put anything here — a status check, a list of what to look at first, a
reminder of which queue to watch. For polecat templates, the nudge can use the
same `{{.Id}}`, `{{.Repo}}`, `{{.WorktreeDir}}` template variables that the
prompt body uses:

```toml
nudge_on_start = "Look at the system prompt and complete the steps for this work item: {{.Id}}"
```

The shipped polecat template uses exactly that — the polecat boots, sees the
nudge naming its work item, and immediately knows what to do.

## 2. Custom polecat templates

Polecat templates live under `~/.pogo/agents/templates/`. The mayor decides
which template to use when it spawns a polecat (`spawn-polecat --template=<name>`,
defaulting to `polecat`). Each template is a markdown file with the same
frontmatter convention as crew prompts, plus access to a few template variables
that get expanded per-spawn:

| Variable | Filled with |
|---|---|
| `{{.Id}}` | Work item ID (e.g., `mg-a3f1`) |
| `{{.Task}}` | Work item title |
| `{{.Body}}` | Work item body (markdown) |
| `{{.Repo}}` | Target repository path (empty if no `--repo`) |
| `{{.Branch}}` | Target branch for refinery submit |
| `{{.WorktreeDir}}` | Path to the polecat's isolated worktree |

### The `worktree` knob

Polecat templates support one frontmatter key crew prompts don't:

```toml
worktree = true   # default — create an isolated git worktree at spawn
worktree = false  # do not create a worktree; polecat runs in pogod's CWD
```

The default coding workflow needs a worktree per polecat: the polecat builds a
feature branch in `~/.pogo/polecats/<name>/`, the refinery rebases that branch
onto `main` in its own worktree, and they don't step on each other. Set
`worktree = false` for any polecat that doesn't produce a git change — the
[research-triage example](examples/research-triage/README.md) uses this for
polecats that write markdown files outside any repo.

Note: even with `worktree = true` in the template, pogod still skips worktree
creation if the mayor spawns the polecat without `--repo`. The frontmatter is
the *upper bound*; the spawn call decides whether to actually create one.

### Author a second polecat template

You're not limited to one template. The shipped profile already has two:
`polecat.md` (writes code) and `polecat-qa.md` (verifies code). Add a third by
dropping a file under `~/.pogo/agents/templates/`:

```bash
$EDITOR ~/.pogo/agents/templates/polecat-research.md
```

Minimum viable template:

```markdown
+++
worktree = false
nudge_on_start = "Read the task and produce a research note for {{.Id}}."
+++

# Research polecat

**Task:** {{.Task}}
**Work Item ID:** {{.Id}}

### Background
{{.Body}}

## Protocol

1. `mg claim {{.Id}}` — fail loudly if someone else owns it.
2. Do the research. Write the note to `$NOTES_DIR/research-notes-{{.Id}}.md`.
3. Mail the mayor: `mg mail send mayor --from={{.Id}} --subject="note-filed: {{.Id}}" --body="..."`
4. `mg done {{.Id}} --result='{"note":"research-notes-{{.Id}}.md"}'`
5. Wait for the mayor to stop you. **Do not exit on your own.**
```

Then teach the mayor's prompt to spawn it for the right kind of item:

```bash
pogo agent spawn-polecat <short-id> \
  --template=polecat-research \
  --task="..." --body="..." --id="..." \
  --env NOTES_DIR=$HOME/research-notes
```

A few rules of thumb:

- **Always include `mg claim {{.Id}}` and `mg done {{.Id}}`.** Skipping `claim`
  means another polecat can grab the same item; skipping `done` means
  `mg list --status=available` will keep showing it.
- **Tell the polecat not to exit on its own.** The mayor stops polecats once
  their output is accepted — premature exit means the mayor can't send
  follow-up nudges (e.g., "this note is too vague, revise it"). The shipped
  templates all carry an explicit "**Never exit on your own**" line; keep that
  pattern.
- **Match the spawner's expectations.** If the mayor's prompt expects the
  polecat to mail back with subject `note-filed:`, the template's protocol
  must produce exactly that subject. The two prompts together are the
  contract.

Polecat templates are read fresh on every spawn — no daemon restart required.
Edit the template, file a new work item, the next polecat picks up the change.

## 3. Toggling the refinery off

The refinery is the merge-queue loop pogod runs by default. It only does
useful work if your polecats produce git branches that need to land on `main`.
For workflows that produce files, decisions, or notes — anything that isn't a
git change — turn it off.

Drop a `[refinery]` section in `~/.config/pogo/config.toml` (or
`$XDG_CONFIG_HOME/pogo/config.toml`):

```toml
[refinery]
enabled = false
```

Then restart pogod:

```bash
pogo server stop && pogo server start
```

What changes:

- **pogod skips refinery startup entirely.** No background loop, no fetcher,
  no merge worker.
- **`pogo refinery status` reports `Status: disabled`.** That's your sanity
  check — if it still says `running` after a restart, the config didn't load.
- **Accidental `pogo refinery submit` calls return a 503** with a clear
  "refinery is disabled" message instead of silently queuing nothing. This
  matters when you're porting code from a coding workflow and forget to strip
  the submit call.
- **Archival becomes the mayor's job.** With the refinery on, a successful
  merge archives the work item; with it off, your mayor prompt has to call
  `mg archive <id>` explicitly after reviewing the polecat's output.

There's no half-on state. The refinery is either running (default) or absent.
That's deliberate — gate-then-merge plumbing is opt-out, not partially
configurable.

### Quick sanity-check the toggle

```bash
pogo refinery status
# Status: disabled

curl -s http://localhost:10000/refinery/status | jq
# {"enabled": false, ...}
```

If either of those still reports the refinery as enabled, pogod hasn't reread
the config. Make sure the file is at `~/.config/pogo/config.toml` (or
`$XDG_CONFIG_HOME/pogo/config.toml`) and you restarted the daemon, not just
the agents.

## Putting it all together

The three knobs above compose. A non-coding workflow looks like:

1. `pogo init --minimal` — scaffold the agent profile without the coding
   prompts in the way.
2. Edit `~/.pogo/agents/mayor.md` — set `auto_start = true`, write your
   dispatch logic, mention the tags and templates you actually use.
3. Edit `~/.pogo/agents/templates/polecat.md` (or add a new template) — set
   `worktree = false` if there's no git change, and write the protocol the
   mayor expects.
4. Drop `[refinery] enabled = false` in `~/.config/pogo/config.toml`.
5. `pogo install` (or `pogo server start` if pogod is already up) and `mg new`
   your first item.

For a fully worked example with all four steps wired up — research items in,
markdown notes out, no git, no merges — see
[`docs/examples/research-triage/`](examples/research-triage/README.md). It's
the reference you can copy from when adapting pogo to your own non-coding
workflow.

## Where to look when something doesn't take

- **Frontmatter changes don't apply:** the agent is still running with the old
  metadata. Restart it (`pogo agent stop <name> && pogo agent start <name>`)
  or restart pogod.
- **`auto_start = true` agent didn't boot:** check `pogo agent list` for an
  error state, then `pogo agent output <name>` for the spawn-time stderr.
  Common cause: a typo in the frontmatter (e.g., a missing closing `+++`
  fence) — pogod logs the parse error and falls back to defaults.
- **Polecat template changes ignored:** templates are read at spawn time, so a
  *running* polecat won't see edits — but the *next* spawn will. If even new
  spawns ignore the change, double-check you edited
  `~/.pogo/agents/templates/polecat.md` and not one of pogo's embedded
  defaults under `internal/agent/prompts/`.
- **`config.toml` changes ignored:** make sure `pogo server stop && pogo
  server start` ran cleanly. The hand-rolled TOML parser is intentionally
  small — unknown sections and keys are silently skipped, so a typo in
  `[refinery]` (e.g., `[Refinery]` or `enable = false`) gets dropped without
  warning.
