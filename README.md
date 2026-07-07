# pogo

A daemon for agent-shaped work. Agents are UNIX processes — `ps` finds them, `kill` signals them, `pogo agent attach` drops you into their terminal.

## The newest process type

A daemon runs in the background, a shell interactively. An **agent** is the newest: a long-lived process you prompt, supervise, and pipe. Coordination is through the filesystem — no database, no framework, no SDK. The process *is* the agent. Build any workflow — coding, research, content, triage — from the same few primitives.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

Runs `pogo install`: the daemon starts under launchd (macOS) or systemd (Linux), [macguffin](https://github.com/drellem2/macguffin) (the task-store CLI, `mg`) initializes, default prompts land in `~/.pogo/agents/`. Idempotent (`--force` overwrites prompts).

`pogo init` scaffolds prompts only (`--minimal` for a bare skeleton); `--no-pogo-install` inspects `~/.pogo/` before anything is written.

**Prerequisites:** an agent harness on PATH — [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by default (`provider` selects another) — and [macguffin](https://github.com/drellem2/macguffin) >= v0.1.3 (default prompts use `mg unclaim`, added in v0.1.3). Pass `--interactive` to wire up shell and editor integrations.

Verify:

```sh
pogo server status      # daemon, agents, refinery (the merge queue) — all reachable?
pogo agent list         # coordinator running?
mg list                 # work items — empty on a fresh install
```

On error, rerun `pogo install` or `pogo doctor --check`.

## Working with agents

`pgrep pogo-crew` lists crew; `pgrep pogo-cat` lists polecats (disposable worker agents). `pogo agent list` formats this. The coordinator's agent name is `ringmaster` by default (workers default to `pogocat`); both are configurable via `[agents] coordinator` / `[agents] worker`.

```sh
pogo agent list                         # what's running
pogo agent status ringmaster            # one agent's state
pogo agent attach ringmaster            # live PTY session (detach: ~.)
pogo nudge ringmaster "check for work"  # inject text without attaching
pogo agent spawn "add retry logic"      # one-off polecat
mg mail send ringmaster --subject="priority change" --body="pause feature work"
```

| | Crew | Polecat |
|---|------|---------|
| Process name | `pogo-crew-<name>` | `pogo-cat-<id>` |
| Lifetime | Persistent — respawned on crash | Ephemeral — exits after task |
| Prompt | `~/.pogo/agents/crew/<name>.md` | Template + work item |
| Merge path | Push to main | Refinery merge queue |

Behavior is prompt-defined. Edit `~/.pogo/agents/mayor.md` to change dispatch. Add a crew agent with `~/.pogo/agents/crew/<name>.md` + `pogo agent start <name>`. Polecats re-read `~/.pogo/agents/templates/polecat.md` each spawn. Crew with `auto_start = true` start at boot (default ringmaster).

## Coordination: macguffin

Pogo coordinates through [macguffin](https://github.com/drellem2/macguffin). Work items are markdown files. Mail is Maildir. Claims are atomic renames. State lives in `~/.macguffin/` — no server, no schema, no port.

```sh
mg new "fix the auth token refresh bug"   # file work
mg list                                    # available → claimed → done
```

## Default workflow: coding

The **coordinator** (auto-started crew, already running) watches for work and spawns a **polecat** per item; the polecat fixes it on a branch and submits to the **refinery**, which runs your gates and merges to `main`.

Swap the prompts and set `[refinery] enabled = false` to drive research, content, or any queue-shaped work. See [docs/customizing.md](docs/customizing.md) and the [research-triage example](docs/examples/research-triage/README.md).

## Batteries included

- **Refinery** — runs quality gates and merges polecat branches to `main`. Deterministic, not an agent. Disable with `[refinery] enabled = false`.
- **Discovery + search** — `lsp` lists known repos, `pose` searches across them (zoekt-backed); pogod indexes as you visit.
- **Integrations** — shell, editor (Emacs, Neovim, VS Code), and tmux. Installer `--interactive`, or see [docs/](docs/).

## Configuration

[docs/CONFIGURATION.md](docs/CONFIGURATION.md) surveys every customization point — PM TOMLs, prompt templates, the scheduler, agent registry, refinery gates, and mail.

## Learn more

- [ARCHITECTURE.md](ARCHITECTURE.md) — full system design
- [VISION.md](VISION.md) — principles
- [MVP.md](MVP.md) — roadmap
- [docs/development.md](docs/development.md) — build from source, tests, pre-commit hook
- [docs/examples/research-triage/](docs/examples/research-triage/README.md) — non-coding example, end to end

## Environment variables

- `POGO_HOME` — the pogo state directory, default `~/.pogo`. All daemon state
  derives from it: agent prompts (`agents/`), polecat worktrees (`polecats/`),
  `refinery-state.json`, `schedules.json`, `events.log`, `projects.json`, the
  recovery queue, and the singleton lockfile. Overriding it (together with
  `HOME`) fully isolates a test/CI daemon from the real one; a `config.toml`
  placed directly in `$POGO_HOME` overrides `~/.config/pogo/config.toml`.
  (Legacy: `POGO_HOME=$HOME`, exported by an old shell integration, is
  normalized to `$HOME/.pogo`.)
- `POGO_PLUGIN_PATH` — plugin discovery. Defaults to `$POGO_HOME/plugin`.

pogod only installs default prompts and auto-starts crew agents when a
`config.toml` exists — a daemon with no config file never spawns agents. A
configured daemon can also opt out with `[agents] autostart = false` (or
`POGO_AGENT_AUTOSTART=false`).

## License

- **Apache 2.0** — CLI tools (`pogo`, `lsp`, `pose`), editor plugins, shell and tmux integrations. See [LICENSE-APACHE](LICENSE-APACHE).
- **BSL 1.1** — daemon (`pogod`), `internal/`, and `pkg/`. Local use is permitted; the only restriction is commercial hosted service. Converts to Apache 2.0 after 4 years. See [LICENSE-BSL](LICENSE-BSL).

Full details in [LICENSING.md](LICENSING.md).
