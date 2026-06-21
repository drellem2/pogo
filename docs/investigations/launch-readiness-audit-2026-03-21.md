# Pogo v0.2 Launch Readiness Audit

**Date:** 2026-03-21
**Auditor:** polecat-mg-0fa6

---

## Summary

| Area | Verdict |
|------|---------|
| Install script (end-to-end) | PASS |
| `pogo install` one-step setup | PASS |
| Agent spawn/attach/nudge cycle | NEEDS_REVIEW |
| Refinery merge pipeline | NEEDS_REVIEW |
| README accuracy | PASS |
| GoReleaser release config | PASS |
| mg integration | PASS |
| Build & tests | PASS |

**Overall verdict:** No hard blockers. Two areas need review before launch but are functional today.

---

## Detailed Findings

### 1. Install Script (end-to-end) — PASS

**What works:**
- Platform detection for darwin/linux, amd64/arm64 is correct
- Downloads all 4 binaries (pogo, pogod, lsp, pose) from GitHub releases
- Handles "text file busy" by removing existing binaries before replacing
- `POGO_VERSION` and `POGO_INSTALL_DIR` overrides work
- Interactive mode (`--interactive`) installs shell, editor, and tmux integrations
- Shell integration idempotency via marker comments
- Neovim integration has proper error recovery per file
- Service install (launchd on macOS, systemd on Linux) is solid

**Minor issues (nice-to-haves):**
- `test_install.sh:120` expects "Warning: failed to download" but `install.sh:100` outputs "Error: failed to download" — test assertion mismatch
- Tmux integration curl commands (lines 215-216) lack error handling — continues even if download fails
- Emacs integration curl (line 245) doesn't check return value
- VS Code build suppresses stderr (line 310), making failures hard to debug

### 2. `pogo install` One-Step Setup — PASS

**What works:**
- Starts pogo daemon via health check
- Runs `mg init` for macguffin workspace initialization
- Installs default agent prompts to `~/.pogo/agents/`
- Idempotent — safe to run multiple times
- `--force` flag to overwrite existing prompts
- Clear progress output with checkmarks and next-step instructions

**No issues found.**

### 3. Agent Spawn/Attach/Nudge Cycle — NEEDS_REVIEW

**What works:**
- `pogo agent spawn`, `spawn-polecat`, and `start` all functional
- Template expansion with {{.Task}}, {{.Body}}, {{.Id}}, {{.Repo}} placeholders
- Git worktree isolation per polecat agent
- Attach via unix domain socket with raw PTY and terminal resize handling
- 64KB ring buffer for output history on attach
- Nudge with wait-idle mode (2s quiescence) and immediate mode
- `WaitReady()` two-phase adaptive startup (replaces hardcoded sleep)
- Crew auto-restart on crash with 2s backoff
- Comprehensive test coverage for spawn, nudge, idle detection

**Issue requiring review:**

**Race condition in polecat spawn** (`internal/agent/api.go:405-443`):
`WorktreeDir` and `SourceRepo` are set on the agent struct *after* `Spawn()` returns and the agent is already running. If the polecat process exits very quickly (before those fields are set), the onExit cleanup callback in pogod sees empty strings and skips worktree removal. This causes git worktrees to accumulate in `~/.pogo/polecats/` without cleanup.

**Impact:** Leaked worktrees over time; requires manual `git worktree remove`.
**Fix:** Set `WorktreeDir`/`SourceRepo` on the agent before spawning, or pass them as part of `SpawnRequest`.

### 4. Refinery Merge Pipeline — NEEDS_REVIEW

**What works:**
- Submit → queue → rebase → quality gates → ff-only merge → push pipeline is correct
- Quality gates configurable via `.pogo/refinery.toml` with fallback to `build.sh`/`test.sh`
- Retry logic (up to 3 attempts) handles target-ref movement between rebase and push
- Rebase conflicts properly abort and report
- FIFO queue with concurrent-safe operations
- Callbacks on merge/failure drive mg notifications and archival
- Integration tests cover end-to-end merge, gate rejection, and queue ordering

**Issues requiring review:**

1. **Data race on `GateOutput`** (`internal/refinery/merge.go:77`): `mr.GateOutput` is written outside the mutex lock during `processMerge()`, while HTTP handlers and callbacks may read it concurrently. Risk: corrupted gate output on concurrent access.

2. **Unbounded history** (`internal/refinery/refinery.go`): The `history` slice grows without limit. Long-running pogod instances will accumulate memory. Not critical for launch but should be addressed.

3. **No worktree cleanup**: Refinery worktrees in `~/.pogo/refinery/worktrees/` persist indefinitely (by design for reuse, but no pruning mechanism exists).

### 5. README Accuracy — PASS

**What works:**
- All documented CLI commands (lsp, pose, pogo, pogod) exist and match their described behavior
- Integration status matrix matches CLAUDE.md exactly
- Installation instructions reference correct URLs and scripts
- Agent workflow examples (spawn, attach, nudge, mg commands) are accurate
- All documentation links (`docs/*.md`) point to existing files
- All integration file paths exist (emacs/, shell/, tmux/, nvim/, vscode/)

**No inaccuracies found.**

### 6. GoReleaser Release Config — PASS

**What works:**
- Builds all 4 binaries (pogo, pogod, lsp, pose)
- Targets: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
- CGO_ENABLED=0 for static binaries
- Version injection via ldflags
- tar.gz archives with correct naming convention matching install.sh expectations
- SHA256 checksums
- Homebrew tap auto-publish to drellem2/homebrew-tap
- Changelog grouping (Features/Bug Fixes/Others)

**No issues found.**

### 7. mg Integration — PASS

**What works:**
- Polecat template embeds complete mg protocol (claim → work → done)
- Mayor prompt includes full mg toolkit (list, show, mail, reap, archive)
- `pogo install` runs `mg init`
- `pogo status` integrates `mg list` for work item display
- Refinery mails mayor on merge success; mayor archives done items
- Refinery sends mg mail to author and mayor on merge failure
- Nudge falls back to `mg mail send` if agent not running

**Note:** mg (macguffin) is an external dependency not installed by pogo's install script. Users must install it separately. This is documented but could trip up new users.

### 8. Build & Tests — PASS

`./build.sh` completes successfully. All Go test packages pass:
- `internal/agent` — 5.8s
- `internal/config` — 0.2s
- `internal/driver` — 1.5s
- `internal/project` — 1.6s
- `internal/refinery` — 4.0s
- `internal/search` — 8.8s
- `internal/service` — 0.7s

Neovim plugin tests skipped (nvim not in PATH — expected in CI without nvim).

---

## Blockers vs Nice-to-Haves

### Blockers (should fix before v0.2)

None identified. All core functionality works. The two NEEDS_REVIEW items are correctness risks but not blockers for a v0.2 release — they affect edge cases, not the happy path.

### Should Fix Soon (post-launch or pre-launch if time permits)

1. **Polecat worktree race condition** — Leaked worktrees when polecats exit immediately after spawn. Low frequency but accumulates over time.
2. **Refinery GateOutput data race** — Potential concurrent read/write on merge request field. Low probability but violates Go race detector guarantees.
3. **Refinery unbounded history** — Memory growth in long-running pogod. Add a cap or TTL-based pruning.

### Nice-to-Haves (post-launch)

1. **Install script test mismatch** — "Error" vs "Warning" string in test_install.sh:120
2. **Tmux/Emacs integration error handling** — Curl commands don't check return values
3. **VS Code build stderr suppression** — Silent failures during extension build
4. **mg as documented dependency** — Consider adding mg install to `pogo install` or documenting it more prominently
5. **Windows support** — Not planned per UNIX-first design, but worth noting for completeness
6. **Refinery worktree pruning** — Mechanism to clean up old refinery worktrees
