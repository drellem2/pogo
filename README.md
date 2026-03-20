# pogo
code intelligence daemon

*Like a language server for project navigation.*

Open a git repository in your terminal, then navigate the project in your editor — no extra effort required.

See [VISION.md](VISION.md) for the design principles and long-term direction.

## Installation

### Quick Install

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

This detects your OS and architecture, downloads the latest release binaries, and installs them to `/usr/local/bin`. Override the install directory with `POGO_INSTALL_DIR` or pin a version with `POGO_VERSION`:

```sh
POGO_INSTALL_DIR=~/.local/bin curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

### Build from Source

Requires [Go](https://go.dev/dl/) 1.21+.

```sh
git clone https://github.com/drellem2/pogo.git && cd pogo
./build.sh                    # fmt, test, build
export PATH="$PWD/bin:$PATH"  # or copy bin/* to ~/.local/bin
```

Then run the interactive installer to set up shell/editor integrations:

```sh
./install.sh --interactive
```

## Integrations

Pogo integrates with shells, terminal multiplexers, and editors. Run the interactive installer to set up integrations automatically:

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

### Shells

Auto-discover projects as you `cd` into directories. All shell integrations provide `sp` (fuzzy project switcher via fzf) and automatic project registration.

| Shell | Status | Docs |
|-------|--------|------|
| [Bash](docs/bash.md) | Supported | Append snippet to `~/.bashrc` |
| [Zsh](docs/zsh.md) | Supported | Append snippet to `~/.zshrc` |
| [Fish](docs/fish.md) | Supported | Copy `pogo.fish` to `conf.d/` |

### Terminal Multiplexers

| Tool | Status | Docs |
|------|--------|------|
| [tmux](docs/tmux.md) | Supported | Plugin with project switcher popup, code search popup, and status bar segment |

### Editors

| Editor | Status | Docs |
|--------|--------|------|
| [Emacs](docs/emacs.md) | Supported | Full minor mode with project navigation, code search, and buffer management |
| [Neovim](docs/neovim.md) | Supported | Lua plugin with Telescope/fzf-lua integration |
| [VS Code](docs/vscode.md) | In development | Extension with command palette and search panel |

> **Emacs**: Install with [straight.el](https://github.com/radian-software/straight.el) or manually from the `.el` file. See [docs/emacs.md](docs/emacs.md) for details.

> **Neovim plugin manager configuration required**: Once available, the Neovim plugin will need to be added to your plugin manager (lazy.nvim, packer, etc.) and configured in your `init.lua`. See [docs/neovim.md](docs/neovim.md) for details.

## Shell Usage

1. Pogo will autodiscover projects as you visit directories in the shell.
2. Use `lsp` to list projects.
3. Use `sp` to switch projects.
4. Use `pose` to search with zoekt.

Zoekt query examples can be found [here](https://github.com/sourcegraph/zoekt/blob/main/web/templates.go#L158).
e.g. `pose banana` or `pose banana .` will search the current directory for `banana`.

## Getting Started with Agent Orchestration

Pogo doubles as a minimal agent orchestrator. If you have [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed, you can use pogo to coordinate multiple agents working on a codebase together.

### Prerequisites

- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI installed
- [macguffin](https://github.com/drellem2/macguffin) (`mg`) CLI installed

### 1. Install

```sh
pogo install
```

This single command:
- Starts the pogo daemon (if not already running)
- Initializes macguffin (`mg init`) for work item coordination
- Installs default agent prompts to `~/.pogo/agents/`

After install, your agent configuration lives in plain markdown files:

```
~/.pogo/agents/
├── mayor.md           # Coordinator prompt — edit to change dispatch strategy
├── crew/              # Long-running agent prompts — add your own here
└── templates/
    └── polecat.md     # Ephemeral worker template
```

Run `pogo install` again at any time — it's idempotent. Existing prompt files are preserved unless you pass `--force`.

### 2. Start the mayor

The mayor is the coordinator agent. It watches for available work and spawns polecats to handle it.

```sh
pogo agent start mayor
```

### 3. File work

```sh
mg new "fix the auth token refresh bug"
```

The mayor picks it up, spawns a polecat, and the polecat claims the work, makes changes on a feature branch, and submits it to the refinery merge queue. The refinery runs your quality gates and merges to main.

### Working with agents

```sh
# See what's running
pogo agent list              # Running agents (mayor + active polecats)
pogo agent status mayor      # Mayor's current state
mg list                      # All work items and their status

# Talk to agents
pogo agent attach mayor              # Live terminal session (detach: ~.)
pogo nudge mayor "check for work"    # Inject text without attaching
mg mail send mayor --subject="priority change" --body="pause feature work"
```

`pogo agent attach` connects your terminal to the agent's PTY — you see exactly what the agent sees. `pogo nudge` writes text to the agent's input without attaching, waiting for idle by default.

### How the pieces fit together

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

Agents are UNIX processes. You can find them with `ps`, send signals with `kill`, and monitor output with `pogo agent output <name>`. All coordination happens through the filesystem via `mg` — no database, no server, no custom protocol.

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

## How pogo compares to alternatives

Most project tools are scoped to a single editor or shell. Pogo takes a different approach: a background daemon that discovers and indexes repositories automatically, then exposes them to any integration.

| | pogo | [Projectile](https://github.com/bbatsov/projectile) | [project.el](https://www.gnu.org/software/emacs/manual/html_node/emacs/Projects.html) | [telescope.nvim](https://github.com/nvim-telescope/telescope.nvim) |
|---|---|---|---|---|
| **Scope** | All local repos, all editors and shells | Emacs only | Emacs only | Neovim only |
| **Discovery** | Automatic — daemon watches as you `cd` | Manual (`projectile-add-known-project`) or dir-local | Automatic within VC dirs | Manual (`:Telescope find_files`) |
| **Code search** | [zoekt](https://github.com/sourcegraph/zoekt) trigram index, searches all repos | `grep`/`rg` per project | `grep`/`xref` per project | `rg`/`fd` per project |
| **Indexing** | Background, incremental, always ready | On-demand | On-demand | On-demand |
| **Cross-repo** | Built-in — `pose QUERY` searches everything | No (single project) | No (single project) | No (single project) |
| **Shell integration** | Bash, Zsh, Fish, tmux | N/A | N/A | N/A |

**What pogo does differently:**

- **Daemon-based auto-discovery.** You never register projects. Open a terminal in a git repo and pogo learns about it. Switch editors and the same project list is there.
- **Background zoekt indexing.** Code search uses a pre-built trigram index, so results return instantly even in large repos. The index updates in the background as files change.
- **One tool, many surfaces.** Instead of configuring project navigation separately in each editor and shell, pogo provides a single daemon that all integrations talk to.

**Where alternatives do better:**

- **Projectile** has deep Emacs integration — project-scoped compilation, test runners, and buffer management that pogo's Emacs mode doesn't replicate.
- **project.el** ships with Emacs and requires zero setup. If you only use Emacs and don't need cross-repo search, it's simpler.
- **telescope.nvim** has a rich extension ecosystem and tight Neovim integration (live grep, LSP pickers, git status) that goes well beyond project switching.

If you live in one editor and don't need cross-repo search, these tools may be all you need. Pogo is useful when you work across multiple editors, terminals, or repositories and want a single source of truth for project discovery and code search.

## Environment Variables

- `POGO_HOME`: Folder for pogo to store indexes.
- `POGO_PLUGIN_PATH`: Folder to discover plugins.

## Plugins

You can write/install plugins to provide IDE-like features for all editors. See the included `search` plugin for an example.
