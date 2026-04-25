# pogo

File a task, walk away, come back to merged code on `main`.

Pogo is a UNIX-native agent orchestrator. It coordinates multiple AI agents working on your codebase — spawning them, supervising them, and merging their work. Agents are plain UNIX processes: find them with `ps`, signal them with `kill`, attach to their terminals with `pogo agent attach`. Coordination happens through the filesystem via [macguffin](https://github.com/drellem2/macguffin).

## The loop

```
You                    pogod                     macguffin
 │                       │                          │
 │  mg new "task"        │                          │
 ├──────────────────────►├─────────────────────────►│ work/available/
 │                       │                          │
 │                       │  mayor notices            │
 │                       │◄─────────────────────────┤ mg list --status=available
 │                       │                          │
 │                       │  spawn polecat            │
 │                       ├─────────┐                │
 │                       │ polecat │  mg claim       │
 │                       │         ├───────────────►│ work/claimed/
 │                       │         │  (does work)   │
 │                       │         │  mg done        │
 │                       │         ├───────────────►│ work/done/
 │                       │         │                │
 │                       │  refinery runs gates      │
 │                       │  merges to main           │
 │                       │                          │
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

Then run setup:

```sh
pogo install
```

This starts the daemon, initializes macguffin, and installs default agent prompts to `~/.pogo/agents/`:

```
~/.pogo/agents/
├── mayor.md           # Coordinator prompt — edit to change dispatch strategy
├── crew/              # Long-running agent prompts — add your own here
└── templates/
    └── polecat.md     # Ephemeral worker template
```

Run `pogo install` again any time — it's idempotent. Existing prompt files are preserved unless you pass `--force`.

**Prerequisites:** [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI must be installed. The install script handles [macguffin](https://github.com/drellem2/macguffin) automatically; pass `--interactive` to configure shell and editor integrations.

## Getting started

```sh
pogo agent start mayor              # Start the coordinator
mg new "fix the auth token refresh bug"   # File work
```

The mayor picks it up, spawns a polecat, and the polecat claims the work, implements a fix on a feature branch, and submits it to the refinery merge queue. The refinery runs your quality gates and merges to `main`.

## Working with agents

```sh
# See what's running
pogo agent list              # Running agents (mayor + active polecats)
pogo agent status mayor      # Mayor's current state
mg list                      # All work items and their status

# Interact with agents
pogo agent attach mayor              # Live terminal session (detach: ~.)
pogo nudge mayor "check for work"    # Inject text without attaching
mg mail send mayor --subject="priority change" --body="pause feature work"

# Spawn a one-off polecat directly
pogo agent spawn "add retry logic to the API client"
```

`pogo agent attach` connects your terminal to the agent's PTY — you see exactly what the agent sees. `pogo nudge` writes text to the agent's input without attaching, waiting for idle by default.

### Agent types

| | Crew | Polecat |
|---|------|---------|
| Process name | `pogo-crew-<name>` | `pogo-cat-<id>` |
| Lifetime | Persistent — daemon restarts on crash | Ephemeral — exits after task |
| Prompt | `~/.pogo/agents/crew/<name>.md` | Generated from template + work item |
| Merge path | Push to main | Submit to refinery merge queue |

Agents are UNIX processes. No agent framework, no agent SDK. The process IS the agent. `pgrep pogo-crew` lists all crew. `pgrep pogo-cat` lists all polecats. `pogo agent list` wraps this with formatted output.

### Customization

Agent behavior is defined entirely by prompt files. To change how the mayor dispatches work, edit `~/.pogo/agents/mayor.md`. To add a persistent crew agent:

```sh
# Create a prompt file
cat > ~/.pogo/agents/crew/reviewer.md << 'EOF'
# Reviewer
You review pull requests for code quality...
EOF

# Start it
pogo agent start reviewer
```

Polecats read `~/.pogo/agents/templates/polecat.md` fresh on each spawn, so template changes take effect immediately.

## How it works

Pogo has three layers: a **discovery daemon** that indexes git repositories as you visit them, **agent supervision** that spawns agents with PTY allocation and manages their lifecycle, and a **refinery** that runs quality gates and merges polecat branches to main. The refinery is code, not an agent — it never burns tokens on merge decisions.

All coordination state lives in macguffin (`~/.macguffin/`): work items are markdown files, mail is Maildir, claims are atomic renames. No database, no server.

## Also included

Pogo bundles project discovery and cross-repo code search via `lsp` (list projects) and `pose` (search code). Shell, editor, and tmux integrations are available — see [docs/](docs/) for setup details or run the installer with `--interactive`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full system design, [MVP.md](MVP.md) for implementation roadmap, and [VISION.md](VISION.md) for principles.

## Development

```sh
./build.sh       # Format, test, build, install
./test.sh        # Run tests only
./fmt.sh         # Format code only
```

Binaries are in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pose`, `cmd/pogod`.

To build from source: `git clone https://github.com/drellem2/pogo.git && cd pogo && ./build.sh` (requires [Go](https://go.dev/dl/) 1.21+).

Set up the pre-commit hook:

```sh
git config core.hooksPath hooks
```

### End-to-end smoke test

`scripts/test-e2e.sh` exercises the full orchestration loop — `pogo init`,
`pogod`, mayor auto-start, polecat spawn, refinery merge, gate-failure
rejection, and crew crash → respawn — against a sandboxed `$HOME`, a
non-default port, and a fake-agent stand-in for `claude`. No API keys
required.

```sh
scripts/test-e2e.sh                  # ~30s; prints a per-step PASS/FAIL summary
POGO_E2E_KEEP=1 scripts/test-e2e.sh  # leave the sandbox dir on disk to inspect
POGO_E2E_PORT=20000 scripts/test-e2e.sh
```

The test is also wrapped as a Go test that's skipped by default (so it
doesn't slow `go test ./...`). To run it through the Go toolchain:

```sh
POGO_RUN_E2E=1 go test ./internal/agent -run TestE2ESmoke -v -timeout 5m
```

Requires `mg` (macguffin) on `$PATH`.

## Environment variables

- `POGO_HOME`: Folder for pogo to store indexes
- `POGO_PLUGIN_PATH`: Folder to discover plugins

## License

Pogo uses a split license model:

- **Apache 2.0** — CLI tools (`pogo`, `lsp`, `pose`), editor plugins, shell and tmux integrations. See [LICENSE-APACHE](LICENSE-APACHE).
- **BSL 1.1** — Daemon (`pogod`), `internal/`, and `pkg/`. Local use is fully permitted; the only restriction is offering it as a commercial hosted service. Converts to Apache 2.0 after 4 years. See [LICENSE-BSL](LICENSE-BSL).

See [LICENSING.md](LICENSING.md) for the full details.
