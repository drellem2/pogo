# zsh PATH audit: stop /usr/local/bin shadowing go/bin in shell subprocesses

**Work item:** mg-bc8b
**Date:** 2026-05-04
**Auditor:** polecat cat-mg-bc8b
**Parent:** mg-4275 (closed via `rm /usr/local/bin/pogo`; this ticket addresses the underlying shell-init root cause)

## Symptom

`/bin/zsh -c -l 'echo $PATH'` (the invocation Claude Code's Bash tool uses) produced PATH with `/usr/local/bin` ahead of `/Users/daniel/go/bin`. Any binary present in both directories resolved to the stale `/usr/local/bin` copy in shell subprocesses, even though pogod's plist and the polecat agent process itself had the correct order.

`/bin/zsh -ilc 'echo $PATH'` (interactive login) and `/bin/zsh -c 'echo $PATH'` (non-login non-interactive) were already correct — only login non-interactive was broken.

## Files audited

- `/etc/zshenv` — does not exist
- `~/.zshenv` — did not exist before this fix
- `/etc/zprofile` — runs `eval $(/usr/libexec/path_helper -s)`
- `~/.zprofile` — prepends `$HOME/.elan/bin`
- `/etc/zshrc` — macOS default (key bindings, history; no PATH edits)
- `~/.zshrc` — interactive only; multiple PATH prepends (lines 51, 65, 66 prepend `go/bin` and `.local/bin`, which is why interactive shells were already correct). Line 5 contains a stale hardcoded `PATH=...` snapshot with `/usr/local/bin` early; later prepends override it for interactive shells.
- `~/.zlogin` — sources RVM, which prepends RVM ruby paths
- `/etc/paths` — leads with `/usr/local/bin`
- `/etc/paths.d/*` — cryptex, rvictl, XQuartz, TeX, VMware Fusion, Mono, postgresapp; none touch `/usr/local/bin`

## Root cause

`/etc/zprofile` runs `path_helper -s`, which rebuilds `PATH` from `/etc/paths` (leading with `/usr/local/bin`) and appends pre-existing entries at the end. For login shells, this reorders `/Users/daniel/go/bin` and `/opt/homebrew/bin` behind `/usr/local/bin`. Interactive login shells then go on to source `~/.zshrc`, which re-prepends `go/bin` and `.local/bin` (lines 51, 65, 66) — masking the bug for interactive sessions. Login *non-interactive* shells skip `~/.zshrc` entirely, so the path_helper-imposed order survives intact.

## Fix

**`~/.zshenv` (created)** — defines `pogo_prepend_user_paths()` (a function that prepends `$HOME/go/bin`, `$HOME/.local/bin`, `$HOME/.pogo/bin`, `/opt/homebrew/bin`, `/opt/homebrew/sbin` to `path` using `typeset -gxU` for de-duplication). Defining it in `~/.zshenv` makes it available to every zsh invocation regardless of mode.

**`~/.zprofile` (edited)** — calls `pogo_prepend_user_paths` after the existing `$HOME/.elan/bin` prepend. Because `~/.zprofile` is sourced after `/etc/zprofile`, this re-asserts the correct order *after* path_helper has run.

`~/.zshrc` was deliberately left untouched. The pre-existing prepends on lines 51/65/66 already keep interactive login PATH correct, and the stale hardcoded `PATH=...` on line 5 is a long-standing quirk out of scope for this ticket. Editing line 5 risks dropping paths Daniel relies on (`perl5/perlbrew/bin`, `documents/apps/apache-maven-3.6.3/bin`, `usr/local/opt`).

## Verification

```
$ /bin/zsh -c -l 'echo $PATH' | tr ':' '\n' | grep -nE '/usr/local/bin|go/bin'
4:/Users/daniel/go/bin
6:/Users/daniel/.pogo/bin
10:/usr/local/bin
```

`/Users/daniel/go/bin` now precedes `/usr/local/bin` in the login non-interactive PATH. `which pogo` from the same shell resolves to `/Users/daniel/go/bin/pogo`, not the (already-removed in mg-4275, but for any future shadowed binary) `/usr/local/bin` copy. Interactive login (`zsh -ilc`) and non-login (`zsh -c`) cases are unchanged — they were already correct and remain so.

`~/.claude/shell-snapshots/` was inspected; existing snapshots were captured under interactive login and already had the correct PATH order. Stale snapshots older than one hour were deleted; Claude Code regenerates per session, so future snapshots will reflect the fixed environment.

## Follow-up

Per ticket scope (e): the `export PATH=/Users/daniel/go/bin:$PATH` workaround in mayor's session memory can be retired now that login subshells return the correct order without it. Removal is intentionally out of scope here — file a separate ticket so we have a clean revert path if any tooling regression surfaces.

Backups of pre-fix files were saved as `~/.zprofile.bak-mg-bc8b` (no `~/.zshenv` existed pre-fix).
