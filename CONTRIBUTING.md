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

## Releases

Releases are cut by tag-trigger: pushing a `vX.Y.Z` tag to `origin` triggers
`.github/workflows/release.yml`, which runs `goreleaser` and publishes the
GitHub release with all four binaries (`pogo`, `pogod`, `lsp`, `pose` for
linux/darwin × amd64/arm64). `install.sh`'s `releases/latest` resolver picks
up the new release within minutes.

**Tag-creation policy.** Only the release-cut path pushes `v*` tags — either
`scripts/bump-version.sh` (which validates strict semver and tags
annotated/signed) or a maintainer directly. No other automation creates tags.
Versioning is semver: **patch** for CI / docs / chore-only changes, **minor**
otherwise; reserve major for breaking CLI changes. Prereleases use a
`vX.Y.Z-<suffix>` form and surface as GitHub prereleases automatically.

**Cutting a release.** From a clean main with the change you want to ship:

```bash
./scripts/bump-version.sh X.Y.Z --commit --tag --push
```

This bumps `internal/version/version.go`, rolls `CHANGELOG.md`, commits, tags,
and pushes. The release workflow does the rest.

Two things the script does **not** do, which the releaser must:

- **Run the upgrade smoke first if the release changes a role-name default.**
  `./scripts/upgrade-smoke.sh` seeds a config from the previous release, upgrades
  to the working tree, and asserts that an existing install keeps its role names
  across both pin sites (`pogo install` and pogod boot) while a fresh install
  adopts the new ones. It is a **hard publish gate**: a red run means do not tag.
  The guard it protects is unrecoverable after the fact — an install whose role
  names were never pinned cannot have them recovered.
- **Maintain the link-reference block at the bottom of `CHANGELOG.md`.**
  `update_changelog()` only inserts the version heading; the `[X.Y.Z]:` compare
  links are hand-maintained. Each cut adds one line for the new version and
  repoints `[Unreleased]` at it:

  ```
  [Unreleased]: https://github.com/drellem2/pogo/compare/vX.Y.Z...HEAD
  [X.Y.Z]: https://github.com/drellem2/pogo/compare/vW.V.U...vX.Y.Z
  ```

  Miss it and the new heading renders as literal `[X.Y.Z]` text on GitHub, and
  `[Unreleased]` keeps claiming the commits you just released.

**Recovery from a failed publish.** If GitHub Actions is wedged or the
goreleaser step fails, the tag stays in place — re-trigger via
`workflow_dispatch` on the tagged ref once Actions recovers; goreleaser
handles idempotent re-uploads.

**Cadence.** `pm-pogo` files a `release-cut` `mg` ticket automatically once
origin/main drifts past either threshold (>= 50 commits ahead of the latest
release tag, OR >= 30 days since the latest published release). Thresholds
live in `internal/agent/prompts/pm/pm-template.md`.

## Reporting Issues

Use the GitHub issue templates for [bug reports](.github/ISSUE_TEMPLATE/bug_report.md) and [feature requests](.github/ISSUE_TEMPLATE/feature_request.md).

## License

By contributing, you agree that your contributions will be licensed under the [GPL-3.0 License](LICENSE).
