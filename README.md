# pogo
UNIX-native agent orchestrator for autonomous development

Pogo coordinates multiple AI agents working on a codebase together. Agents are UNIX processes — you can find them with `ps`, signal them with `kill`, and attach to their terminals with `pogo agent attach`. All coordination happens through the filesystem via [macguffin](https://github.com/drellem2/macguffin).

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full system design, [MVP.md](MVP.md) for implementation roadmap, and [VISION.md](VISION.md) for principles.

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

File work, walk away, come back to merged code on `main`.

## Getting started

### Prerequisites

- [Go](https://go.dev/dl/) 1.21+ (to build from source)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI installed
- [macguffin](https://github.com/drellem2/macguffin) (`mg`) CLI installed

### Install

```sh
# Quick install (prebuilt binaries)
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh

# Or build from source
git clone https://github.com/drellem2/pogo.git && cd pogo
./build.sh
```

Then run one-step setup:

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

### Start the mayor and file work

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

## Project discovery and code search

Pogo includes a background daemon (`pogod`) that automatically discovers and indexes git repositories as you work. This infrastructure supports both human navigation and agent awareness of the codebase.

### CLI tools

**`lsp`** — List known projects:

```sh
lsp              # One path per line
lsp --json       # JSON array of project objects
```

**`pose`** — Search across projects (powered by [zoekt](https://github.com/sourcegraph/zoekt)):

```sh
pose QUERY           # Search all indexed repos
pose QUERY .         # Search current repo only
pose -l QUERY        # List only file paths
pose --json QUERY    # JSON output with full match details
```

**`pogo`** — Server and project management:

```sh
pogo visit <path>        # Register a repo with pogo
pogo server start        # Start the daemon
pogo server stop         # Stop the daemon
```

Projects are discovered automatically as you `cd` into directories — no manual registration required. Code search uses a pre-built trigram index, so results return instantly even in large repos.

### Shell integration

Use `sp` (fuzzy project switcher via fzf) and automatic project registration in any supported shell:

| Shell | Setup |
|-------|-------|
| [Bash](docs/bash.md) | Append snippet to `~/.bashrc` |
| [Zsh](docs/zsh.md) | Append snippet to `~/.zshrc` |
| [Fish](docs/fish.md) | Copy `pogo.fish` to `conf.d/` |

### Editor integration

| Editor | Status | Docs |
|--------|--------|------|
| [Emacs](docs/emacs.md) | Supported | Full minor mode with project navigation, code search, and buffer management |
| [Neovim](docs/neovim.md) | Supported | Lua plugin with Telescope/fzf-lua integration |
| [VS Code](docs/vscode.md) | In development | Extension with command palette and search panel |

### tmux

| Tool | Status | Docs |
|------|--------|------|
| [tmux](docs/tmux.md) | Supported | Plugin with project switcher popup, code search popup, and status bar segment |

Run the interactive installer to set up shell, editor, and tmux integrations automatically:

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

## How it works

Pogo has three layers:

1. **Discovery** — background daemon scans and indexes git repositories as you visit them
2. **Agent supervision** — pogod spawns agents with PTY allocation, manages lifecycle, provides interactive access
3. **Refinery** — deterministic merge queue loop runs quality gates and merges polecat branches to main

The refinery is code, not an agent — it never burns tokens on merge decisions. It maintains its own git worktrees and never touches agent or user working directories.

All coordination state lives in macguffin (`~/.macguffin/`): work items are markdown files, mail is Maildir, claims are atomic renames. No database, no server.

## Development

```sh
./build.sh       # Format, test, build, install
./test.sh        # Run tests only
./fmt.sh         # Format code only
```

Binaries are in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pose`, `cmd/pogod`.

Set up the pre-commit hook:

```sh
git config core.hooksPath hooks
```

## Environment variables

- `POGO_HOME`: Folder for pogo to store indexes
- `POGO_PLUGIN_PATH`: Folder to discover plugins
