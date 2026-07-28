# Work-item scope: declaring, and enforcing, where an agent may edit

`scripts/mg-scope-guard.sh` refuses edits outside a work item's declared scope.
It is **opt-in and wired into nothing by default**; this page is how to turn it
on and what it does and does not cover.

Origin: mg-f1d5, reimplementing the scope half of Macmuffin's `muff scope` /
`muff-hook` on top of macguffin. See `docs/orc-comparative-study-2026-07-28.md`
§3.2 and §5.1 for the source study.

## Why

The polecat prompt says *"Stay scoped. Only work on your assigned task."* Nothing
enforced it. Agents run `claude --dangerously-skip-permissions`
(`docs/polecat-permissions.md`), so an edit anywhere on the machine is one tool
call away, and a polecat that wanders is caught at review time or not at all.

This is the fence for the accidental case. It is not a security boundary — see
**What it does not cover**.

## Declaring a scope

Scope is a `scope:` carrier line in the work item's body, alongside the existing
`workflow:` / `stage:` / `gh:` block:

```
scope: cmd/pogod/** internal/agent/** docs/*.md
```

Several `scope:` lines accumulate. Patterns are whitespace-separated and
worktree-relative. Three forms:

| Written | Matches |
|---|---|
| `docs/CONFIGURATION.md` | exactly that file |
| `internal/agent` | that directory and everything under it |
| `cmd/pogod/**` | every path under `cmd/pogod/`, at any depth |
| `docs/*.md` | markdown **directly** in `docs/`, not in `docs/sub/` |

`**` crosses directory separators; `*` and `?` do not. That distinction is the
only real logic in the script and is pinned by a test, because bash's own
`[[ str == pat ]]` gets it wrong — there `*` swallows slashes, which would
silently turn every narrow scope into a wide one.

## Which item is in force

Worked out, never declared per call:

1. `$MG_SCOPE_ITEM`, if set;
2. else the first non-comment line of a `.mg-scope` file at the worktree root;
3. else **none — and nothing is enforced.**

An agent that never opted in is never blocked, and neither is an item that
declares no scope.

## Turning it on

For one polecat, add to `~/.claude/settings.json` (or the agent's own settings):

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [
          { "type": "command", "command": "/path/to/pogo/scripts/mg-scope-guard.sh" }
        ]
      }
    ]
  }
}
```

and either export `MG_SCOPE_ITEM=mg-xxxx` or drop the id into
`<worktree>/.mg-scope`. The `matcher` is belt-and-braces: the script checks
`tool_name` itself and exits 0 for anything that does not write.

Requires `jq` and `mg` on `PATH` in hook mode.

## Asking directly

```bash
scripts/mg-scope-guard.sh PATH...
```

Exit `0` in scope, `9` out of scope, `11` escape (outside the worktree), `1`
usage or an item that cannot be read. That is macguffin's shared vocabulary, so
another tool can ask without holding a copy of the matching rules —
Macmuffin's `check-scope` contract.

## Failure behaviour

**Before the opt-in it allows; after the opt-in it refuses.** No item, or no
declared scope, and the guard is inert. But once an item *is* in force, anything
that stops the guard deciding — `mg` not on `PATH`, `jq` missing, an item that
cannot be read, no worktree root — is a refusal that names its own fix, not a
silent pass. An opted-in agent that is quietly unenforced is the failure this
script exists to remove, and it is invisible; a loud refusal is not.

An escape is exit `11`, kept apart from an ordinary refusal at `9`, because a
break-out and a typo want different responses. Hook mode collapses both to `2` —
Claude's contract has only the one refusal.

Paths are normalised **lexically**, not through `realpath`. A `Write` names a
file that does not exist yet, and `realpath -e` on it fails — which past the
opt-in would turn every new-file creation into a refusal.

## What it does not cover

**Writes through `Bash` are out of reach.** A `PreToolUse` hook sees the command
line, not what the command will touch, so `sh -c 'cat > out-of-scope.go'` is
undecidable from here and is not decided. Stated rather than implied, because a
guard believed to cover more than it does is worse than no guard.

Nor does it stop a determined agent: it can edit its own settings, unset
`MG_SCOPE_ITEM`, or shell out. This catches the *accident* — the wander, the
neighbouring file, the fix that seemed related — which is the failure that
actually happens.

## Testing

`scripts/mg-scope-guard_test.sh`, run by `./test.sh`. Every case uses a stub `mg`
and a fixture worktree in a temp dir; the developer's live `~/.macguffin` is
never read or written.
