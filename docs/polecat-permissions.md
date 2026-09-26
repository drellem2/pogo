# Polecat Startup: Permissions and Claim Behavior

## How permissions work for polecats

Polecats (disposable worker agents) run in freshly-created git worktrees at `~/.pogo/polecats/<name>`. These directories don't exist until spawn time. Claude Code normally prompts for "directory trust" when started in a directory it hasn't seen before, which would block autonomous execution.

Three mechanisms handle this:

1. The `--dangerously-skip-permissions` flag bypasses **tool execution** permission prompts (shell execution, file access, etc.). It is part of the Claude provider's default command template, together with the `--settings` argument described in 3:

```go
// internal/claude/provider.go — expands to:
claude --dangerously-skip-permissions --settings {"skipDangerousModePermissionPrompt":true} --append-system-prompt-file {{.PromptFile}}
```

The agent harness is selected by a first-class `agent.Provider` value (see `docs/design/multi-provider-architecture-survey.md`). Four providers are registered — `claude` (the default), `codex`, `pi`, and `cursor` — each owning its command template, required non-interactive flags, nudge dialect, and lifecycle hooks (`internal/claude`, `internal/codex`, `internal/pi`, `internal/cursor`).

2. The **trust dialog auto-accept** hook (`claude.TrustDialogHook`) monitors the agent's PTY output during startup for the workspace trust dialog and answers it **by label**: it moves the highlight to "Yes, I trust this folder", re-reads the screen, and only then presses Enter; a dialog it does not recognise gets no keystroke (drellem2/pogo#177, mg-f394). This is registered as a `PostSpawnHook` on the agent registry and lives in `internal/claude/`, keeping Claude-specific behavior out of the generic agent package.

3. The **bypass-permissions warning** is pre-accepted per session by `--settings {"skipDangerousModePermissionPrompt":true}` (`claude.BypassWarningSettings`, drellem2/pogo#173). A profile that has never accepted `--dangerously-skip-permissions` shows "WARNING: Claude Code running in Bypass Permissions mode" with **"No, exit" highlighted**, so an unattended first-run spawn would exit. The flag layers the key onto that one session only: nothing is written to `~/.claude/settings.json`, so your own interactive sessions are unchanged. It is announced rather than silent — `pogo install` prints one line saying pogo's agents run Claude Code in bypass-permissions mode, and `pogo doctor --check` shows the same fact as the `claude permission mode` row. There is no interactive consent step, because install also runs non-interactively. Verified against Claude Code 2.1.283 in a sandbox HOME: without the argument the warning renders; with it the session goes straight to the composer, and the trust dialog is still shown and answerable.

### Why two mechanisms?

The `--dangerously-skip-permissions` flag does **not** suppress the workspace trust dialog ("Quick safety check: Is this a project you created or one you trust?"). This dialog is a separate security boundary in Claude Code that runs before the permission mode takes effect.

Since Claude Code 2.1.270 the dialog's highlighted default is "No, exit", so the hook must select "Yes" by label rather than press a bare Enter — see mg-f394 in the changelog for the details.

### First-run login (drellem2/pogo#173)

A Claude Code profile that has never logged in shows a theme picker and then a browser OAuth flow before any composer. Only a human can complete that, so pogo does not try. Instead it checks `claude auth status --json`:

- `pogo install` prints a loud first step — run `claude` once in a terminal and log in — when the reading is not-logged-in.
- `pogo doctor --check` has a `claude login` row: pass when logged in, **fail** on a positive not-logged-in, warn when the reading is inconclusive.
- **Crew auto-start** (pogod's boot sweep and every `pogo server start`) refuses to spawn crew agents **only on a positive `loggedIn: false`**. Every other outcome fails open and spawns with a logged warning: a non-zero exit for another reason, a CLI without the `auth` subcommand, a timeout, unparseable output, or `loggedIn: false` while `ANTHROPIC_API_KEY` (or another API-credential variable) is set. A false refusal stops the whole crew on a machine that works; a false spawn costs one stalled agent. The refusal is re-checked on every sweep, nothing is latched, and it is annunciated once per episode as the `harness_not_logged_in` condition, copied out of band. A hand-run `pogo agent start` is never gated, and a custom `[agents] command` whose binary is not `claude` is never probed.

Not covered: with `ANTHROPIC_API_KEY` set but never approved, Claude Code shows "Detected a custom API key … ❯ No (recommended)" while `auth status` reports `loggedIn: true`. That gate is tracked separately.

### Why not `--add-dir`?

The `--add-dir` flag was considered and rejected. Adding the worktree directory to Claude Code's trusted directories triggers an interactive trust confirmation prompt — the opposite of what we want. Since `--dangerously-skip-permissions` already handles permissions globally, `--add-dir` is unnecessary.

### The custom-API-key prompt is detected, never answered

When `ANTHROPIC_API_KEY` is set in the environment pogod gives its agents, and the Claude Code profile has never answered for that key, Claude Code stops before its composer:

```
Detected a custom API key in your environment
ANTHROPIC_API_KEY: sk-ant-...<last 20 characters>
Do you want to use this API key?
  Yes
❯ No (recommended)
```

`claude auth status` reports `loggedIn: true` for an API-key user whether or not the key was ever approved, so an auth preflight cannot see this gate (mg-2037). pogo does **not** answer it, unlike the trust dialog. Its two rows are two billing choices: "Yes" bills the key, and "No" falls back to the profile's Claude login, or to no auth at all. Claude Code remembers the answer for good, keyed on the key's last 20 characters in `customApiKeyResponses` in `~/.claude.json` (or `$CLAUDE_CONFIG_DIR/.claude.json`).

What pogo does instead:

- **At spawn**, `claude.TrustDialogHook` recognises the prompt and logs it with the remedy. It emits `claude_api_key_prompt` and marks the agent as held at a pre-composer gate (`agent.HoldAtPreComposerGate`). While the gate is held and the composer has never appeared, the initial nudge's best-effort delivery and the start-verify renudge send **nothing**. Both end in Enter, and on Claude Code 2.1.283 that Enter selected "No" and recorded the key as rejected. A held gate is also not counted as sentinel drift.
- **Before any spawn**, `pogo doctor --check` has a `claude API key approval` row. It warns when the shell's `ANTHROPIC_API_KEY` has no recorded answer. Agents inherit **pogod's** environment, not your shell's, so the spawn-time check is the one that sees the key agents actually get.

**Remedy:** run `claude` once in a terminal with the same `ANTHROPIC_API_KEY` and answer the prompt. Choose Yes to bill the key, or No to use your Claude login. Then stop and respawn any agent that is parked on the prompt, or answer it in place with `pogo agent attach <name>`.

### The `provider` config key

The agent harness provider is selected via `~/.config/pogo/config.toml`:

```toml
[agents]
provider = "claude"   # default; also registered: "codex", "pi", "cursor"
```

It can also be set with the `POGO_AGENT_PROVIDER` environment variable, which
overrides the config file. **The default is `"claude"`**, so existing
deployments need no config change. An unknown provider id logs a warning and
falls back to Claude rather than wedging daemon startup.

The provider supplies the *default* command template, the PTY nudge dialect,
the PTY size, and the post-spawn / session lifecycle hooks.

### Custom command override risk

The agent command can still be overridden directly, which takes precedence over
the provider's default template:

```toml
[agents]
command = "claude --some-other-flags --append-system-prompt-file {{.PromptFile}}"

[agents.polecat]
command = "custom-agent --prompt {{.PromptFile}}"
```

If a custom command omits `--dangerously-skip-permissions` (or uses a non-Claude binary that has its own permission system), the polecat may get stuck at an interactive prompt in its new worktree directory. The `ValidateCommandBinary` function checks that the binary exists on PATH but does not validate flags.

To guard against this, `ValidatePolecatCommand` logs a warning when a polecat command template is missing any of the active provider's declared non-interactive flags (`claude.Provider.NonInteractiveFlags` — `--dangerously-skip-permissions` and `--settings {"skipDangerousModePermissionPrompt":true}`). A custom command that passes its own `--settings` JSON satisfies the second by including `"skipDangerousModePermissionPrompt":true` in it. The JSON must contain no whitespace: the template is split on whitespace and is not shell-quoted.

## Claim behavior

The `mg claim` command (`mg` is macguffin, the task-store CLI) is called by the **polecat itself**, not by pogo infrastructure. It appears in step 1 of the polecat prompt template (`internal/agent/prompts/templates/polecat.md`):

```
1. **Claim the work item** (prevents duplicate work):
   mg claim {{.Id}}
```

### Sequence

1. pogod creates worktree and spawns Claude Code process
2. Claude Code starts, reads the system prompt
3. After 10 seconds, pogod sends an initial nudge via PTY
4. The polecat (Claude) runs `mg claim <id>` as its first action
5. If claim succeeds, polecat proceeds with the work
6. If claim fails (already claimed), the polecat should mail the mayor (the coordinator)

### What can go wrong

| Scenario | What happens | Mitigation |
|----------|-------------|------------|
| Tool permissions prompt blocks startup | Polecat never reaches `mg claim` | `--dangerously-skip-permissions` prevents this |
| Workspace trust dialog blocks startup | Polecat never reaches `mg claim` | `claude.TrustDialogHook` selects "Yes" by label |
| Bypass-permissions warning blocks startup | Session exits ("No, exit" is the default) | `--settings {"skipDangerousModePermissionPrompt":true}` |
| Harness has no login | Agent sits at the login screen | Crew auto-start refuses on a positive not-logged-in; `pogo doctor --check` fails the `claude login` row |
| `mg` binary not on PATH | `mg claim` fails with command-not-found | Polecat should mail mayor; `mg` is installed globally |
| Work item already claimed | `mg claim` returns error | Polecat should mail mayor and not proceed |
| Network/macguffin unavailable | `mg claim` fails | Polecat should mail mayor |
| Polecat ignores prompt instructions | Claim never called | Prompt emphasizes claim as critical failure mode |

### Why pogo doesn't claim on behalf of polecats

Claiming is left to the polecat (not done during spawn) because:

1. **Atomicity**: The polecat should only claim after it's confirmed running and ready to work
2. **Retries**: If claim fails, the polecat can retry or escalate — pogo doesn't have this context
3. **Observability**: The polecat's conversation log shows the claim attempt and result
4. **Simplicity**: pogo spawn doesn't need to know about macguffin protocol

## Testing

The `TestProviderCommandHasPermissionsSkip` test in `internal/claude/provider_test.go` verifies that `claude.Provider.CommandTemplate` always includes `--dangerously-skip-permissions`, and `TestProviderNonInteractiveFlags` verifies the flag is declared in `claude.Provider.NonInteractiveFlags`. Together they guard against accidental removal of the flag during refactoring.
