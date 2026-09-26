# Polecat Startup: Permissions and Claim Behavior

## How permissions work for polecats

Polecats (disposable worker agents) run in freshly-created git worktrees at `~/.pogo/polecats/<name>`. These directories don't exist until spawn time. Claude Code normally prompts for "directory trust" when started in a directory it hasn't seen before, which would block autonomous execution.

Two mechanisms handle this:

1. The `--dangerously-skip-permissions` flag bypasses **tool execution** permission prompts (shell execution, file access, etc.). It is part of the Claude provider's default command template:

```go
// internal/claude/provider.go
CommandTemplate: "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}",
```

The agent harness is selected by a first-class `agent.Provider` value (see `docs/design/multi-provider-architecture-survey.md`). Four providers are registered — `claude` (the default), `codex`, `pi`, and `cursor` — each owning its command template, required non-interactive flags, nudge dialect, and lifecycle hooks (`internal/claude`, `internal/codex`, `internal/pi`, `internal/cursor`).

2. The **trust dialog auto-accept** hook (`claude.TrustDialogHook`) monitors the agent's PTY output during startup for the workspace trust dialog and automatically sends Enter to accept it. This is registered as a `PostSpawnHook` on the agent registry and lives in `internal/claude/`, keeping Claude-specific behavior out of the generic agent package.

### Why two mechanisms?

The `--dangerously-skip-permissions` flag does **not** suppress the workspace trust dialog ("Quick safety check: Is this a project you created or one you trust?"). This dialog is a separate security boundary in Claude Code that runs before the permission mode takes effect.

The trust dialog hook polls the agent's output buffer for up to 8 seconds after spawn, looking for the "safety check" text. When detected, it sends Enter (`\r`) to accept the default "Yes" option.

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

To guard against this, `ValidatePolecatCommand` logs a warning when a polecat command template is missing any of the active provider's declared non-interactive flags (`claude.Provider.NonInteractiveFlags` — currently just `--dangerously-skip-permissions`).

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
| Workspace trust dialog blocks startup | Polecat never reaches `mg claim` | `claude.TrustDialogHook` auto-accepts within 8s |
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
