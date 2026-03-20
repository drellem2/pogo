# Polecat Worktree Isolation

## Problem

Polecats currently work directly in whatever repo path is passed as `--repo`. If that's the same checkout a crew member or user is using, `git checkout -b polecat-<id>` switches the branch in their working directory. This causes branch conflicts, dirty tree errors, and confusion.

## Solution

pogod creates a git worktree per polecat before launching it. The polecat works in its own isolated worktree. pogod cleans up the worktree after the polecat exits.

## Why worktrees, not clones

- `git worktree add` takes ~1 second vs cloning which can take minutes for large repos
- Worktrees share the object store with the parent repo — no disk duplication
- `git worktree remove` is clean and atomic
- This is the same approach Gas Town uses successfully

## Layout

```
~/.pogo/polecats/
└── <name>/           # git worktree, one per polecat
    ├── .git          # worktree link file (not a full .git dir)
    └── ...           # repo contents on a fresh branch
```

## Lifecycle

### Spawn

In `handleSpawnPolecat` (internal/agent/api.go), before calling `r.Spawn()`:

1. Resolve the repo path to the actual git root
2. Create the worktree:
   ```
   git -C <repo> worktree add ~/.pogo/polecats/<name> -b polecat-<name>
   ```
3. Set the polecat's working directory to the worktree path
4. Store the worktree path and source repo on the Agent struct for cleanup

### Work

The polecat's cwd IS the worktree. It's already on branch `polecat-<name>`. The polecat:
- Runs `mg claim <id>`
- Makes changes, commits
- Runs `git push origin polecat-<name>`
- Runs `pogo refinery submit polecat-<name> --repo=<worktree-path>`
- Runs `mg done <id>`
- Exits

### Cleanup

In the `onExit` callback (cmd/pogod/main.go):

1. Remove the worktree:
   ```
   git -C <source-repo> worktree remove ~/.pogo/polecats/<name> --force
   ```
2. Optionally delete the local branch if the refinery already merged:
   ```
   git -C <source-repo> branch -d polecat-<name>
   ```
3. Log cleanup event

### Stale worktree recovery

On pogod startup, run `git worktree prune` on repos with known worktrees. This handles the case where pogod crashed and left stale worktrees behind.

## What changes

### internal/agent/api.go — handleSpawnPolecat

Before spawning:
```go
// Create worktree for polecat isolation
worktreeDir := filepath.Join(home, ".pogo", "polecats", spawnReq.Name)
branchName := "polecat-" + spawnReq.Name
cmd := exec.Command("git", "-C", spawnReq.Repo, "worktree", "add", worktreeDir, "-b", branchName)
if err := cmd.Run(); err != nil {
    http.Error(w, fmt.Sprintf("worktree creation failed: %v", err), http.StatusInternalServerError)
    return
}
```

Set `cmd.Dir` on the Claude process to the worktree path.

### internal/agent/agent.go — Agent struct

Add fields:
```go
WorktreeDir string // path to polecat's worktree (for cleanup)
SourceRepo  string // path to the source repo (for worktree removal)
```

### cmd/pogod/main.go — onExit callback

After existing cleanup:
```go
if a.WorktreeDir != "" {
    exec.Command("git", "-C", a.SourceRepo, "worktree", "remove", a.WorktreeDir, "--force").Run()
}
```

### internal/agent/prompts/templates/polecat.md

Remove `git checkout -b` and `cd {{.Repo}}` from the template — the worktree handles both. Update step 3:
```
3. **Commit and push your branch:**
   ```bash
   git add <files>
   git commit -m "<type>: <description> ({{.Id}})"
   git push origin polecat-{{.Id}}
   ```
```

### buildPolecatPrompt in api.go

Same update — remove the `cd` and `git checkout -b` from the generated prompt.

## Edge cases

- **Repo has uncommitted changes**: `git worktree add` is unaffected by the parent's working tree state.
- **Two polecats on same repo**: Each gets its own worktree with a unique branch name. No conflict.
- **Polecat crashes**: `onExit` still fires via `cmd.Wait()`. Worktree gets cleaned up.
- **pogod crashes**: Stale worktrees accumulate. Startup prune handles this.
- **Worktree creation fails**: Fail the spawn, return error to caller. Don't fall back to cloning.

## What doesn't change

- Crew agents work in their own long-lived clones
- The refinery uses its own worktrees under `~/.pogo/refinery/worktrees/`
- The mayor doesn't need a worktree (it coordinates, doesn't write code)
- No new config knobs — `~/.pogo/polecats/` is the fixed location
