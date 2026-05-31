# Configuring pogo

A map of pogo's customization points — what you can tune, where each setting
lives, and which doc to read for depth. This is a survey, not a reference. For
the guided walkthrough of reshaping pogo for a non-coding workflow, start with
[docs/customizing.md](customizing.md).

## PM TOMLs

Per-product-manager config lives in `~/.pogo/agents/pm/<name>.toml` —
`repos`, `tags_any`, and `sources` define what a PM owns and scans during a
sweep. A PM crew prompt (`crew/pm-<name>.md`) composes by extending the shared
`pm-template` *with* its TOML (see the synthesis pattern below). To add one,
drop a new `<name>.toml` and a matching `crew/pm-<name>.md` stub.
See [docs/prompt-customization.md](prompt-customization.md).

## Prompt templates

Agent behavior is defined by prompt files under `internal/agent/prompts/` —
`mayor.md`, `crew/doctor.md`, `pm/pm-template.md`, and the
`templates/polecat.md` / `templates/polecat-qa.md` worker templates (installed
copies live in `~/.pogo/agents/`). The `extends <template> with config <toml>`
directive synthesizes a crew prompt from a base plus a TOML. See
[docs/prompt-customization.md](prompt-customization.md) and [PROMPT_GUIDELINES.md](PROMPT_GUIDELINES.md).

## Scheduler

`pogo schedule` registers recurring (`--cron`) or one-shot (`--once --in N`)
wakeups that fire from pogod's heartbeat and survive host sleep and restarts.
`--id` makes a schedule idempotent (re-running replaces, not stacks); the
default `--replay once` is at-most-once, firing once after a long sleep then
rescheduling forward. Source of truth: `internal/scheduler/`; run
`pogo schedule --help` for the full flag set.

## Agent registry

Each agent has a directory under `~/.pogo/agents/<name>/` holding its prompt,
PID, and last-activity state; `pogo agent start`/`stop` manage the lifecycle and
`pogo agent diagnose <name>` reports health. A dead-process entry is now
cleared on the next start so a stale record can't block a respawn (mg-427f /
78b69d7). See [docs/agent-state-machine-design.md](agent-state-machine-design.md)
and [docs/operations.md](operations.md).

## Refinery / build.sh gates

The refinery is a deterministic merge loop inside pogod (not an agent): it
checks out each merge-ready polecat branch in its own worktree, runs the repo's
quality gate, and fast-forward-merges to `main` only on success. The gate is
your repo's `build.sh` / `test.sh` (or a `.pogo/refinery.toml`). Worktrees and
logs live under `~/.pogo/refinery/`; disable with `[refinery] enabled = false`.
See [ARCHITECTURE.md](../ARCHITECTURE.md) §"The Refinery".

## `pogo install`

`pogo install` is one-step setup: start pogod, run `mg init`, and install the
default agent prompts to `~/.pogo/agents/`. It is idempotent — stale canonical
prompts are auto-updated, user edits preserved (`--force` overwrites, backing up
to `<name>.bak.<timestamp>`). The bundled `install.sh` runs it as its final
step; opt out with `--no-pogo-install` or `POGO_NO_POGO_INSTALL=1` (mg-6bfd).
See [docs/customizing.md](customizing.md).

## Mail

Inter-agent coordination flows through Maildir mailboxes under
`~/.macguffin/mail/`, one per agent plus a `human` mailbox the notifier watches.
Each uses the standard `cur/new/tmp` convention, so delivery is an atomic
rename — no locks, no server. Send with `mg mail send <to> --from=<id> ...` and
read with `mg mail list <id>`. See [ARCHITECTURE.md](../ARCHITECTURE.md) for the
filesystem-coordination model.
