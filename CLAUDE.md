# Pogo

Pogo is a code intelligence daemon for multi-repo discovery. It automatically discovers, indexes, and searches local git repositories.

## CLI Tools

Pogo provides three CLI tools. All support `--json` for machine-readable output.

### `lsp` - List known projects

Lists all repositories pogo has discovered on the machine.

```bash
lsp              # One path per line
lsp --json       # JSON array of project objects
```

Use this to find local repos, discover what's available, or locate related projects.

### `pose` - Search across projects

Searches code across all indexed repositories using zoekt.

```bash
pose QUERY           # Search all indexed repos (results sorted by match count)
pose QUERY .         # Search current directory's repo
pose QUERY /path     # Search a specific repo
pose -l QUERY        # List only file paths (no match details)
pose --json QUERY    # JSON output with full match details
```

Query syntax follows [zoekt conventions](https://github.com/sourcegraph/zoekt/blob/main/web/templates.go#L158). Use this when looking for code patterns, function definitions, or usages across multiple repos.

### `pogo` - Server and project management

```bash
pogo visit <path>        # Register a file/directory's repo with pogo
pogo server start        # Start the pogo daemon
pogo server stop         # Stop the pogo daemon
```

## When to Use Pogo

- **Cross-repo dependency questions**: Use `lsp --json` to find local repos, then `pose --json` to search across them.
- **Finding related code**: `pose` searches all indexed projects at once - faster than grepping repos one by one.
- **Discovering local repos**: `lsp` shows everything pogo knows about, useful when you're unsure what's cloned locally.

## Integration Status

When working on docs or integration code, verify statuses match reality before writing:

| Integration | Status | Code location |
|-------------|--------|---------------|
| Emacs | Supported | `emacs/pogo.el` |
| Zsh | Supported | `shell/.zshrc` |
| Bash | Supported | `shell/.bashrc` |
| Fish | Supported | `shell/pogo.fish` |
| tmux | Supported | `tmux/pogo.tmux` |
| VS Code | In development | `vscode/` |
| Neovim | Supported | `nvim/` |

If you add or advance an integration, update both `README.md` and this table.

## Development

Go project. Build and test:

```bash
./build.sh       # Build all binaries (runs fmt + test + install)
./test.sh        # Run tests
./fmt.sh         # Format code (go fmt)
```

Binaries are in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pose`, `cmd/pogod` (daemon).

### Before committing

Always run `./build.sh` before committing. If it fails, fix the issue before pushing.

Set up the pre-commit hook to enforce this automatically:

```bash
git config core.hooksPath hooks
```

The hook runs `gofmt -l` and `go build ./...` on every commit.
