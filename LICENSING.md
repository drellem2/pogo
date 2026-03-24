# Licensing

Pogo uses a split licensing model to keep the CLI tools and editor integrations
fully open while protecting the server-side components from being offered as a
competing hosted service.

## Apache 2.0 — CLI and editor integrations

The following components are licensed under the [Apache License 2.0](LICENSE-APACHE):

- `cmd/pogo/` — CLI tool
- `cmd/lsp/` — project listing tool
- `cmd/pose/` — code search tool
- `emacs/` — Emacs integration
- `nvim/` — Neovim integration
- `vscode/` — VS Code extension
- `tmux/` — tmux integration
- `shell/` — shell integrations (bash, zsh, fish)

**What this means:** You can use, modify, and redistribute these components
freely for any purpose, including commercial use. The Apache 2.0 license is
a permissive open-source license with no copyleft requirements.

## BSL 1.1 — Daemon and internal packages

The following components are licensed under the
[Business Source License 1.1](LICENSE-BSL):

- `cmd/pogod/` — pogo daemon
- `internal/` — internal packages
- `pkg/` — shared library packages

**What this means:**

- **Local development use is fully permitted.** You can run pogod on your own
  machines, modify it, build on top of it, and use it within your organization.
- **The one restriction:** You may not offer the Licensed Work as a commercial
  hosted service to third parties — i.e., you cannot operate it as a managed
  service, platform, or SaaS product that provides pogo's daemon functionality
  to external users.
- **BSL converts to Apache 2.0 after 4 years.** Each release of the BSL-licensed
  components automatically becomes Apache 2.0 licensed four years after its
  release date. This is a one-way conversion — once a version becomes Apache 2.0,
  it stays Apache 2.0 forever.

## Contributing

Future contributors will be asked to sign a Contributor License Agreement (CLA)
to ensure we can maintain this licensing structure. Details will be provided
when the CLA is in place.

## Questions

If you have questions about licensing or need an alternative arrangement,
please open an issue or contact the maintainer.
