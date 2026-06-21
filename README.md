# pogo

A daemon for agent-shaped work. Agents are UNIX processes — `ps` finds them, `kill` signals them, `pogo agent attach` drops you into their terminal.

## The newest process type

A daemon runs in the background. A shell runs interactively. An **agent** is the newest process type: a long-lived process you prompt, supervise, and pipe like any other.

Spawn them, name them, list them, signal them, attach to them. Script a fleet or drive one by hand. Coordination happens through the filesystem — no database, no agent framework, no SDK. The process *is* the agent.

Pogo is the UNIX-native successor to Gas Town: the same autonomous-agent experience, none of the conceptual weight. Build whatever workflow you want (coding, research, content, triage) out of the same few primitives, or work hands-on.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

This installs the binaries and runs `pogo install`: the daemon starts under launchd (macOS) or systemd (Linux), [macguffin](https://github.com/drellem2/macguffin) initializes, and default prompts land in `~/.pogo/agents/`. Re-run `pogo install` any time — it's idempotent and preserves your prompts (`--force` to overwrite).

To inspect `~/.pogo/` before anything is written, pass `--no-pogo-install` and run `pogo install` when ready. To scaffold prompts only, use `pogo init` (`--minimal` for a bare mayor/polecat skeleton).

**Prerequisites:** an agent harness on PATH — [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by default (select another with the `provider` config key). The installer handles macguffin; pass `--interactive` to wire up shell and editor integrations.

Verify:

```sh
pogo server status      # daemon, agents, refinery — all reachable?
pogo agent list         # mayor running?
mg list                 # work items — empty on a fresh install
```

If anything errors, rerun `pogo install` (it repairs a half-install) or run `pogo doctor --check`.

## Working with agents

Agents are processes. `pgrep pogo-crew` lists long-running crew; `pgrep pogo-cat` lists ephemeral polecats. `pogo agent list` wraps this with formatted output.

```sh
pogo agent list                       # what's running
pogo agent status mayor               # one agent's state
pogo agent attach mayor               # live PTY session (detach: ~.)
pogo nudge mayor "check for work"     # inject text without attaching
pogo agent spawn "add retry logic"    # one-off polecat
mg mail send mayor --subject="priority change" --body="pause feature work"
```

`attach` connects your terminal to the agent's PTY. `nudge` writes to its input and waits for idle.

| | Crew | Polecat |
|---|------|---------|
| Process name | `pogo-crew-<name>` | `pogo-cat-<id>` |
| Lifetime | Persistent — respawned on crash | Ephemeral — exits after task |
| Prompt | `~/.pogo/agents/crew/<name>.md` | Template + work item |
| Merge path | Push to main | Refinery merge queue |

Behavior is entirely prompt-defined. Edit `~/.pogo/agents/mayor.md` to change dispatch. Drop a `~/.pogo/agents/crew/<name>.md` and `pogo agent start <name>` to add a crew agent. Polecats re-read `~/.pogo/agents/templates/polecat.md` on every spawn, so template edits take effect immediately. Crew start at boot when their frontmatter declares `auto_start = true` (default mayor); start the rest on demand.

## Coordination: macguffin

Pogo coordinates through [macguffin](https://github.com/drellem2/macguffin), not a database. Work items are markdown files. Mail is Maildir. Claims are atomic renames. State lives in `~/.macguffin/` — no server, no schema, no port.

```sh
mg new "fix the auth token refresh bug"   # file work
mg list                                    # watch it move: available → claimed → done
```

## Default workflow: coding

Out of the box pogo is wired for coding. The **mayor** (an auto-started crew agent) watches for work, spawns a **polecat** per item, and the polecat implements a fix on a feature branch and submits it to the **refinery**, which runs your gates and merges to `main`.

```sh
mg new "fix the auth token refresh bug"
```

That's the whole loop — the mayor is already running. Swap the prompts and set `[refinery] enabled = false` and the same machinery drives research notes, content, or any queue-shaped work. See [docs/customizing.md](docs/customizing.md) and the worked [research-triage example](docs/examples/research-triage/README.md).

## Batteries included

The core is small; the defaults are useful.

- **Refinery** — runs quality gates and merges polecat branches to `main`. Deterministic code, not an agent: it never burns tokens on merge decisions and works even when every agent is down. Disable with `[refinery] enabled = false`.
- **Discovery + search** — `lsp` lists known repos, `pose` searches across them (zoekt-backed). pogod indexes repos in the background as you visit them.
- **Integrations** — shell, editor (Emacs, Neovim, VS Code), and tmux. Run the installer with `--interactive`, or see [docs/](docs/).

## Configuration

Behavior is prompts plus a few TOML knobs. [docs/CONFIGURATION.md](docs/CONFIGURATION.md) surveys every customization point — PM TOMLs, prompt templates, the scheduler, agent registry, refinery gates, `pogo install`, and mail — with the file path and design doc for each.

## Learn more

- [ARCHITECTURE.md](ARCHITECTURE.md) — full system design
- [VISION.md](VISION.md) — principles
- [MVP.md](MVP.md) — roadmap
- [docs/development.md](docs/development.md) — build from source, tests, pre-commit hook
- [docs/examples/research-triage/](docs/examples/research-triage/README.md) — non-coding example, end to end

A star on [drellem2/pogo](https://github.com/drellem2/pogo) helps others find it — optional, never required (`gh repo star drellem2/pogo`).

## Environment variables

- `POGO_HOME` — state, recovery queue, and projects.json. Defaults to `~/.pogo`.
- `POGO_PLUGIN_PATH` — plugin discovery. Defaults to `$POGO_HOME/plugin`.

## License

Split model:

- **Apache 2.0** — CLI tools (`pogo`, `lsp`, `pose`), editor plugins, shell and tmux integrations. See [LICENSE-APACHE](LICENSE-APACHE).
- **BSL 1.1** — daemon (`pogod`), `internal/`, and `pkg/`. Local use is fully permitted; the only restriction is offering it as a commercial hosted service. Converts to Apache 2.0 after 4 years. See [LICENSE-BSL](LICENSE-BSL).

Full details in [LICENSING.md](LICENSING.md).
