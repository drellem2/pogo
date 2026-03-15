# Emacs Integration

Pogo provides `pogo-mode`, an Emacs minor mode for project navigation and code intelligence. It communicates with the pogo daemon to provide project switching, file finding, code search, and buffer management.

## Installation

### straight.el (recommended)

```emacs-lisp
(straight-use-package
 '(pogo :type git
        :host github
        :repo "drellem2/pogo"
        :files ("emacs/*.el")))

(require 'pogo)
(pogo-mode +1)
(define-key pogo-mode-map (kbd "C-c p") 'pogo-command-map)
```

Or with `use-package` and straight:

```emacs-lisp
(use-package pogo
  :straight (:type git :host github :repo "drellem2/pogo"
             :files ("emacs/*.el"))
  :bind-keymap ("C-c p" . pogo-command-map)
  :config
  (pogo-mode +1))
```

### Manual

1. Ensure the pogo server is built and accessible (see main [README](../README.md)).
2. Open `emacs/pogo.el` in Emacs.
3. Run `M-x package-install-from-buffer`.
4. Add to your `init.el`:

```emacs-lisp
(require 'pogo)
(pogo-mode +1)
(define-key pogo-mode-map (kbd "C-c p") 'pogo-command-map)
```

If you use a custom install directory, also set `exec-path`:

```emacs-lisp
(defvar pogo-exec-path "/path/to/pogo/bin")
(add-to-list 'exec-path pogo-exec-path)
```

## Requirements

- Emacs 29.1+
- Packages: `request` (0.3.2+), `cl-lib`, `pcache` (0.5.1+)
- pogo server running on `localhost:10000` (start with `pogo server start`)

## Key Bindings

All commands are under the configurable prefix (default: `C-c p`):

| Key | Command | Description |
|-----|---------|-------------|
| `C-c p f` | `pogo-find-file` | Find file in current project |
| `C-c p g` | `pogo-search` | Search project code via zoekt |
| `C-c p p` | `pogo-switch-project` | Switch between known projects |
| `C-c p b` | `pogo-switch-to-buffer` | Switch to a project buffer |
| `C-c p D` | `pogo-dired` | Open project root in dired |
| `C-c p k` | `pogo-kill-buffers` | Kill all project buffers |
| `C-c p e` | `pogo-recentf` | Recently visited project files |
| `C-c p <left>` | `pogo-previous-project-buffer` | Previous project buffer |
| `C-c p <right>` | `pogo-next-project-buffer` | Next project buffer |

## Features

- **Project navigation**: Switch between projects, find files, and manage buffers within project scope.
- **Code search**: Search across the current project using zoekt (via the pogo search plugin).
- **Auto-discovery**: Projects in `pogo-project-search-path` are discovered automatically when the mode activates.
- **Mode line**: Displays the current project name in the mode line.
- **Completion integration**: Works with ido, helm, ivy, or the default Emacs completion system (auto-detected).
- **Buffer tracking**: Tracks which buffers belong to which project for scoped buffer operations.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `pogo-server-url` | URL of the pogo server | `http://localhost:10000` |
| `pogo-keymap-prefix` | Prefix key for pogo commands | `nil` (set via `define-key`) |
| `pogo-auto-discover` | Auto-discover projects on mode activation | `t` |
| `pogo-project-search-path` | Directories to scan for projects | `nil` |
| `pogo-completion-system` | Completion backend: `auto`, `ido`, `helm`, `ivy`, `default` | `auto` |
| `pogo-switch-project-action` | Function to call after switching projects | `pogo-find-file` |
| `pogo-kill-buffers-filter` | Filter for which buffers to kill | `nil` |
| `pogo-mode-line-function` | Custom mode-line format function | `pogo-default-mode-line` |
| `pogo-debug-log` | Enable debug logging to `*pogo-mode-log*` | `t` |

## Projectile Compatibility

If you use [projectile](https://github.com/bbatsov/projectile), pogo-mode works alongside it using the same keybinding conventions. Not all projectile features are replicated yet.

## Troubleshooting

- **"Connection refused" errors**: Ensure the pogo server is running: `pogo server start`.
- **No projects listed**: Projects must be registered first. Visit a project directory in the shell (with the shell integration active) or use `pogo visit /path/to/project`.
- **Completion not working**: Set `pogo-completion-system` explicitly if auto-detection picks the wrong backend.
- **Debug output**: Enable `pogo-debug-log` and check the `*pogo-mode-log*` buffer for diagnostic messages.
