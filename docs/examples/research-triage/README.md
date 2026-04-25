# Research-triage example

This is a worked example of using pogo for a workflow that has nothing to do
with code. Pogo's defaults — git worktrees, branch-per-polecat, a refinery
merge queue — are aimed at coding work, but the pieces underneath (mayor,
polecats, work items, mail) are general. This example walks through bending
them into a "research triage" workspace where:

- The user files investigation/decision items via `mg new`.
- A custom **mayor** dispatches polecats to research them.
- A custom **polecat template** writes a markdown note per item, mails the
  mayor with the path, and waits.
- The **refinery is disabled** in `config.toml` — there are no branches, no
  merges, nothing to gate.
- Polecats run **without git worktrees**, because the output is a markdown
  file, not a code change.

The goal is to show that the orchestration shell ("watch the queue, dispatch
agents, route mail, archive when done") is the load-bearing thing, and the
default coding glue is just one set of choices on top of it.

## Files in this example

```
docs/examples/research-triage/
├── README.md                       (this file)
├── config.toml                     ([refinery] enabled = false)
├── prompts/
│   ├── mayor.md                    (custom triage mayor)
│   └── polecat-template.md         (file-based polecat — no worktree)
└── work-items/
    └── sample-research.md          (a sample input)
```

## Setup

### 1. Lay down a minimal agent profile

`pogo init --minimal` scaffolds `~/.pogo/agents/` with empty mayor and
polecat skeletons rather than the shipped coding-profile prompts:

```bash
pogo init --minimal
```

You should see `~/.pogo/agents/mayor.md` and
`~/.pogo/agents/templates/polecat.md` created.

> Skipping `--minimal` would lay down the full coding profile (mayor with
> refinery instructions, crew/doctor, polecat templates that push branches).
> Those files would actively contradict everything below — `--minimal`
> exists exactly so you don't have to delete that boilerplate.

### 2. Drop the example prompts in place

Copy this example's prompts over the skeletons:

```bash
cp docs/examples/research-triage/prompts/mayor.md \
   ~/.pogo/agents/mayor.md

cp docs/examples/research-triage/prompts/polecat-template.md \
   ~/.pogo/agents/templates/polecat.md
```

### 3. Disable the refinery

Drop the example `config.toml` at `~/.config/pogo/config.toml` (or
`$XDG_CONFIG_HOME/pogo/config.toml`):

```bash
mkdir -p ~/.config/pogo
cp docs/examples/research-triage/config.toml ~/.config/pogo/config.toml
```

The key line is in the `[refinery]` section:

```toml
[refinery]
enabled = false
```

With this set, pogod skips refinery startup entirely. `pogo refinery
status` will report `Status: disabled`, and any accidental `pogo refinery
submit` returns an immediate 503 with a clear message. There is nothing to
keep alive in the background.

### 4. Make the notes directory

The polecat template writes to `$NOTES_DIR`, which the mayor sets to
`$HOME/research-notes/` when spawning. Create it once:

```bash
mkdir -p ~/research-notes
```

### 5. Start the daemon and macguffin

`pogo install` starts pogod, runs `mg init`, and copies prompts. You've
already placed the prompts you want, so the prompt-copy step will mostly
no-op (the timestamps line up because you ran `pogo init --minimal` first).

```bash
pogo install
pogo agent start mayor
```

`pogo agent list` should now show the mayor running. `pogo refinery
status` should show `Status: disabled` — sanity check that your config
took effect.

## Try it

### File a research item

```bash
mg new --type=task --tag=research \
  --title="Investigate streaming-JSON parsers for the ingest pipeline" \
  --body="$(cat docs/examples/research-triage/work-items/sample-research.md)"
```

`mg list --status=available` will now show the new item.

### Watch the loop

Within a coordination cycle (30–60s), the mayor:

1. Sees the new item, reads it with `mg show`, decides it's tagged
   `research` and unblocked.
2. Spawns a polecat with `pogo agent spawn-polecat <short-id> ... --env
   NOTES_DIR=$HOME/research-notes` — note the absence of `--repo`, so no
   git worktree is created.
3. The polecat claims the item, does the research (web + `pose` + `lsp`
   for local code, if relevant), writes
   `~/research-notes/research-notes-<id>.md`, mails the mayor with subject
   `note-filed: <id>`, and calls `mg done`.
4. The mayor reads the mail, opens the note, and either:
   - Archives the item with `mg archive <id>`, then stops the polecat —
     because the refinery is off, **archival is the mayor's job, not the
     refinery's**; or
   - Mails the polecat back with `follow-up: <id>` if the note needs
     revision. The polecat updates the file and re-files.

You can follow the mayor's reasoning by tailing its output:

```bash
pogo agent output mayor
```

### Read the result

The polecat's research lives at `~/research-notes/research-notes-<id>.md`.
You can open it directly, or browse the whole folder over time as your
research backlog accumulates.

## What this example demonstrates

- **`pogo init --minimal` decouples profile setup from daemon setup.** You
  can scaffold `~/.pogo/agents/` for any workflow without committing to
  the coding-mayor's view of the world.

- **The refinery is opt-out.** `[refinery] enabled = false` is a
  first-class supported state. The agent and workitem packages don't
  import the refinery package, so disabled refinery is structurally
  supported, not just feature-flagged off.

- **`spawn-polecat` works without `--repo`.** When the mayor omits
  `--repo`, pogod skips worktree creation entirely. The polecat runs in
  pogod's CWD and works on whatever paths the prompt and `--env` tell it
  about. This is what makes file-based, non-git workflows tractable.

- **Mail is the post-completion channel, not the refinery.** In the
  coding workflow, "merged" mail from the refinery is what tells the
  mayor a polecat is done. Here, the polecat itself mails the mayor
  (`note-filed:`) — mail is general-purpose, not refinery-specific.

## Adapting this to your own workflow

The shape — mayor watches queue, dispatches polecat per item, polecat
files output via mail, mayor archives — generalizes well. Some examples:

- **Reading list triage.** Items are papers/links; polecats summarize and
  classify; output is a one-paragraph annotation in a shared bibtex or
  markdown file.
- **Inbox-zero assistant.** Items are forwarded emails; polecats draft
  replies; mayor reviews drafts.
- **Bug-triage prep (still no code).** Items are bug reports; polecats
  cross-reference logs and prior issues, write a triage note, recommend
  severity. The actual fix is a separate human-written work item later.

Things to change per workflow:
- **`NOTES_DIR`** — wherever your output should land.
- **Tags the mayor watches** — switch `research` for whatever you tag your
  inputs with, so the mayor doesn't grab unrelated items.
- **Note structure** — the polecat template's "Suggested structure" is
  a starting point; tighten it once you know what you want to skim.

The orchestration loop stays the same — only the prompts and the output
medium change.
