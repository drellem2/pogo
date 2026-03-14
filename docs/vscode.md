# VS Code Integration

A VS Code extension that integrates with the pogo daemon for project discovery and code search.

## Features

- **Command palette** project switcher (`Pogo: Switch Project`)
- **File finder** across pogo-indexed projects (`Pogo: Find File in Project`)
- **Code search** using zoekt syntax (`Pogo: Search Project`)
- **Tree view** listing all known projects with indexing status
- **Status bar** indicator showing current project and indexing state
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
| `Pogo: Find File in Project` | Find and open a file in the current project |
| `Pogo: Search Project` | Search code with zoekt syntax, jump to results |
| `Pogo: Show Project Status` | Refresh tree view and show indexing status |

## Status Bar

The status bar shows:
- `$(check) pogo[name]` — project indexed and ready
- `$(sync~spin) pogo[name]` — currently indexing
- `$(warning) pogo[name]` — index is stale
- `$(circle-slash) pogo` — daemon not running
