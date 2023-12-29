# pogo
code intelligence daemon

*Like a language server for project navigation.*

Open a git repository in your terminal, then navigate the project in emacs - no extra effort required.

Currently supports zshell and emacs. 

## Installation 
*(instructions for Mac)*

1. Build the daemon
`brew install go && chmod +x build.sh.sh && ./build.sh`
2. Add the generated `bin` file to your path.
`export PATH=$PATH:/path-to-pogo/bin`
3. Add the zshell tool to your `.zshrc` file.
`cat shell/.zshrc >> ~/.zshrc`
4. Install the emacs client (instructions below).

## Shell Usage

1. Pogo will autodiscover projects as you visit directories in the shell.
2. Use `lsp` to list projects.
3. Use `sp` to switch projects.
4. Use `pose` to search with zoekt.

Zoekt query examples can be found [here](https://github.com/sourcegraph/zoekt/blob/main/web/templates.go#L158).
e.g. `pose banana` or `pose banana .` will seach the current  directory for `banana`. Currently only files are returned, sorted by relevance.

## Environment Variables

- `POGO_HOME`: Folder for pogo to store indexes.
- `POGO_PLUGIN_PATH`: Folder to discover plugins.

## Plugins
You can write/install plugins to provide IDE-like features for all editors. See the included `search` plugin for an example.

## Emacs Client

Provides a project navigation interface matching `projectile.el`, but running in a separate process. Indexing is done in the background, and can be shared with other tools.

Goal: Open a file in emacs, then see its repository automatically imported into your favorite IDE.

### Installation
- Build the server using the instructions above.
- Open `pogo-mode.el` in emacs and run `M-x package-install-from-buffer`. 
- Add to your `init.el`:
```emacs-lisp
(defvar pogo-exec-path "[YOUR_INSTALL_DIR]/pogo/bin")
(progn
        (add-to-list 'exec-path pogo-exec-path)
        (pogo-mode +1)
        (define-key pogo-mode-map (kbd "C-c p") 'pogo-command-map))
```

If you use `projectile`, it will work the same but doesn't have all of the features (yet).
