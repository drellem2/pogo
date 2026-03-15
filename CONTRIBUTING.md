# Contributing to Pogo

Thanks for your interest in contributing to pogo! This guide covers the basics of building, testing, and submitting changes.

## Getting Started

1. Fork and clone the repository
2. Install Go (1.24+)
3. Run `./build.sh` to build, test, and install

## Development Workflow

### Building

```bash
./build.sh    # Format, test, and install all binaries
./test.sh     # Run tests only
./fmt.sh      # Format code only
```

`./build.sh` runs all three steps (format, test, install) and is the recommended way to verify your changes before committing.

### Code Style

- All Go code must be formatted with `gofmt`. The CI pipeline checks this.
- Run `./fmt.sh` or `gofmt -w .` to format your code.
- Follow standard Go conventions and idioms.

### Pre-commit Hook

Set up the pre-commit hook to catch formatting and build issues early:

```bash
git config core.hooksPath hooks
```

This runs `gofmt -l` and `go build ./...` on every commit.

## Submitting Changes

1. Create a feature branch from `main`
2. Make your changes in focused, atomic commits
3. Run `./build.sh` and ensure it passes
4. Open a pull request against `main`

### Pull Request Guidelines

- Keep PRs focused on a single change
- Include a clear description of what the PR does and why
- Ensure CI passes (formatting, build, tests)
- Commit messages should follow the format: `type: description`
  - Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`

## Project Structure

- `cmd/` - CLI entry points (`pogo`, `lsp`, `pose`, `pogod`)
- `internal/` - Internal packages
- `pkg/` - Public packages
- `emacs/`, `nvim/`, `vscode/` - Editor integrations
- `shell/` - Shell integrations (zsh, bash, fish)
- `tmux/` - tmux integration

## Reporting Issues

Use the GitHub issue templates for [bug reports](.github/ISSUE_TEMPLATE/bug_report.md) and [feature requests](.github/ISSUE_TEMPLATE/feature_request.md).

## License

By contributing, you agree that your contributions will be licensed under the [GPL-3.0 License](LICENSE).
