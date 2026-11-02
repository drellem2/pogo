# VS Code Integration

A VS Code extension that integrates with the pogo daemon for project discovery.

## Features

- **Command palette** project switcher (`Pogo: Switch Project`)
- **Tree view** listing all known projects
- **Status bar** indicator showing the current project
- **Auto-register** workspace folders with pogo on open

## Requirements

- VS Code 1.75+
- pogo daemon running (`pogo server start`)

## Installation

From the `vscode/` directory:

```bash
npm install
npm run compile
```

To install as a local extension for development:

```bash
cd vscode
code --install-extension .
```

Or use VS Code's "Developer: Install Extension from Location" command.

## Configuration

| Setting | Default | Description |
|---------|---------|-------------|
| `pogo.serverUrl` | `http://localhost:10000` | URL of the pogo daemon |
| `pogo.autoRegister` | `true` | Auto-register projects when opening folders |

## Commands

| Command | Description |
|---------|-------------|
| `Pogo: Switch Project` | Pick a project from pogo's index and open it |
| `Pogo: Show Registered Projects` | Refresh the tree view and list registered project paths |

## Status Bar

The status bar shows:
- `$(repo) pogo[name]` — current project
- `$(repo) pogo` — daemon running, no current project
- `$(circle-slash) pogo` — daemon not running
