# Polecat validation: `zsh -ilc` directive for env-dependent commands

**Work item:** mg-de7c
**Validated:** 2026-05-04
**Validator:** polecat cat-mg-de7c
**Subject of validation:** the directive in `~/.claude/CLAUDE.md` (Daniel-machine local, not in any repo) instructing every Claude session to wrap `~/.zshrc`-dependent commands with `zsh -ilc 'cmd'`.

This file records *that* validation passed and the method used. It does **not** restate the directive — that lives in `~/.claude/CLAUDE.md` on Daniel's machine and is intentionally out of repo.

## Why this validation matters

Polecats are spawned by `pogod` via `launchd`, which gives them an environment that does not source `~/.zshrc`. Without the wrapper, tools that read auth tokens from `~/.zshrc` exports (`gh`, `aws`, etc.) silently fail or prompt for login in a non-TTY. The directive tells agents to wrap *only* env-dependent commands, not every shell call — wrapping reflexively adds latency and trains agents to ignore the rule.

## Acceptance criteria — results

| # | Criterion | Result |
|---|-----------|--------|
| 1 | `zsh -ilc 'gh auth status'` from a polecat sees Daniel's auth, no `gh auth login` prompt. | Pass — authenticated as `drellem2` via `GH_TOKEN`. |
| 2 | A non-env-dependent command (e.g. `git status`) runs directly without the wrapper. | Pass — this polecat ran `git status` directly throughout the task. |
| 3 | `find` inside the pogo repo returns no `CLAUDE.md` attributable to this directive. | Pass — repo `CLAUDE.md` is the unrelated project doc; no leakage. |

## Empirical confirmation of the `-l` vs `-ilc` distinction

The ticket's key claim is that bare `zsh -lc` is **not** sufficient — `.zshrc` is sourced only for interactive shells. Verified existence-only (no secret values logged):

| Invocation | `[[ -n $GH_TOKEN ]]` |
|------------|----------------------|
| polecat default env (no wrapper) | unset → `gh` reports "not logged in" |
| `zsh -lc '…'` (login, non-interactive) | unset |
| `zsh -ilc '…'` (interactive + login) | set → `gh` works |

The ticket-listed canary `zsh -ilc 'gh repo view drellem2/pogo --json visibility'` returned `{"visibility":"PUBLIC"}`.

## Observations (not blockers)

- Every `zsh -ilc` invocation prints `Warning: PATH set to RVM ruby but GEM_HOME and/or GEM_PATH not set …` to stderr. Cosmetic; stdout JSON parsing is unaffected. Daniel-side `~/.zshrc` rvm-init issue, out of scope here.
- No env vars listed in CLAUDE.md were observed missing under `zsh -ilc`. If a future polecat finds one, the ticket asks it to be documented for iteration.

## Status

All acceptance criteria met. Directive is functioning end-to-end on this machine.
