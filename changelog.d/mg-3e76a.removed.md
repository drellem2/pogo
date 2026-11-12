- **Removed the Emacs and Neovim editor integrations (out of scope)
  (mg-3e76a).** The last commit that contains them is
  dc14d86ddca5b0ec27f1cc3b826ea89db217b178; restore them with
  `git checkout dc14d86ddca5b0ec27f1cc3b826ea89db217b178 -- emacs nvim docs/emacs.md docs/neovim.md`.
  The `emacs-test` and `nvim-test` CI jobs, the Neovim step in `test.sh`, and
  the Emacs/Neovim steps in `install.sh` (including the `--with-editors` flag,
  which installed only those two and is now ignored like any unknown flag) are
  gone with them. VS Code, tmux and the shell integrations are unaffected.
