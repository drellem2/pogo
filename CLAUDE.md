# Pogo

Pogo is a daemon for multi-repo discovery and agent orchestration. It keeps an
index of the local git repositories you work in (`projects.json`). It does
**not** search file contents: the zoekt indexer, `pose`, `pogo refs` and
`pogo deps` were removed (mg-14647). For code search use `rg` or `git grep`
over the paths `lsp` prints.

## CLI Tools

Pogo provides two CLI tools. Both support `--json` for machine-readable output.

### `lsp` - List known projects

Lists all repositories pogo has discovered on the machine.

```bash
lsp              # One path per line
lsp --json       # JSON array of project objects
```

Use this to find local repos, discover what's available, or locate related projects.

### `pogo` - Server and project management

```bash
pogo visit <path>        # Register a file/directory's repo with pogo
pogo project list        # List registered projects (also: add, remove)
pogo server start        # Start the pogo daemon
pogo server stop         # Stop the pogo daemon
```

## When to Use Pogo

- **Discovering local repos**: `lsp` shows everything pogo knows about, useful when you're unsure what's cloned locally.
- **Cross-repo questions**: use `lsp --json` to find the local repos, then search them with `rg` / `git grep`, e.g. `lsp | xargs rg -l PATTERN`.

## Integration Status

When working on docs or integration code, verify statuses match reality before writing:

| Integration | Status | Code location |
|-------------|--------|---------------|
| Emacs | Supported | `emacs/pogo.el` |
| Zsh | Supported | `shell/.zshrc` |
| Bash | Supported | `shell/.bashrc` |
| Fish | Supported | `shell/pogo.fish` |
| tmux | Supported | `tmux/pogo.tmux` |
| VS Code | Supported | `vscode/` |
| Neovim | Supported | `nvim/` |

If you add or advance an integration, update both `README.md` and this table.

## Development

Go project. Build and test:

```bash
./build.sh       # Build all binaries into ./bin (runs fmt + test + build)
./test.sh        # Run tests
./fmt.sh         # Format code (go fmt)
```

`./build.sh` compiles into `./bin` and does **not** `go install` into GOBIN —
that would overwrite the machine's live `pogod` with a branch build. Pass
`--install` if you want the binaries on `PATH`.

Binaries are in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pogod` (daemon).

### Before committing

Always run `./build.sh` before committing. If it fails, fix the issue before pushing.

Set up the pre-commit hook to enforce this automatically:

```bash
git config core.hooksPath hooks
```

The hook runs `gofmt -l` and `go build ./...` on every commit.

The `commit-msg` hook additionally rejects commit messages whose closing
keywords would shut a GitHub issue — including across a line wrap, which is how
a narrative body once shut an external contributor's issue by accident. Cite
issues as `Refs owner/repo#N`, or add `Closing-ref-ack: <ref> — <why>` when the
closure is deliberate. The refinery runs the same check on every merge, so this
hook is an early warning rather than the guarantee.

The refinery also reads the **pull request body**, not just the commits: a
`Resolves #N` there closes the whole issue when the PR merges, and no hook can
see it because it is in no commit. Cite the issue as `Refs owner/repo#N` in PR
bodies too, and fix a flagged one with `gh pr edit <number> --body-file -` —
amending and re-pushing does nothing to a PR body.
