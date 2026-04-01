# Polecat Startup: Permissions and Claim Behavior

## How permissions work for polecats

Polecats run in freshly-created git worktrees at `~/.pogo/polecats/<name>`. These directories don't exist until spawn time. Claude Code normally prompts for "directory trust" when started in a directory it hasn't seen before, which would block autonomous execution.

This is handled by the `--dangerously-skip-permissions` flag in the default agent command template:

```go
const DefaultAgentCommand = "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"
```

This flag bypasses **all** permission prompts including directory trust, shell execution, and file access. It is required for autonomous agent execution.

### Why not `--add-dir`?

The `--add-dir` flag was considered and rejected. Adding the worktree directory to Claude Code's trusted directories triggers an interactive trust confirmation prompt — the opposite of what we want. Since `--dangerously-skip-permissions` already handles permissions globally, `--add-dir` is unnecessary.

### Custom command override risk

The agent command can be overridden via `~/.config/pogo/config.toml`:

```toml
[agents]
command = "claude --some-other-flags --append-system-prompt-file {{.PromptFile}}"

[agents.polecat]
command = "custom-agent --prompt {{.PromptFile}}"
```

If a custom command omits `--dangerously-skip-permissions` (or uses a non-Claude binary that has its own permission system), the polecat may get stuck at an interactive prompt in its new worktree directory. The `ValidateCommandBinary` function checks that the binary exists on PATH but does not validate flags.

To guard against this, `ExpandCommand` logs a warning when the expanded command for a polecat does not contain `--dangerously-skip-permissions` or an equivalent bypass flag.

## Claim behavior

The `mg claim` command is called by the **polecat itself**, not by pogo infrastructure. It appears in step 1 of the polecat prompt template (`internal/agent/prompts/templates/polecat.md`):

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
6. If claim fails (already claimed), the polecat should mail the mayor

### What can go wrong

| Scenario | What happens | Mitigation |
|----------|-------------|------------|
| Permissions prompt blocks startup | Polecat never reaches `mg claim` | `--dangerously-skip-permissions` prevents this |
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

The `TestDefaultCommandHasPermissionsSkip` test in `internal/agent/command_test.go` verifies that `DefaultAgentCommand` always includes `--dangerously-skip-permissions`. This guards against accidental removal of the flag during refactoring.
