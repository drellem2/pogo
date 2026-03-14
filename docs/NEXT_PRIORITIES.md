# Pogo: Next Priorities

Assessment of current state against [VISION.md](../VISION.md) principles, with prioritized next steps.

## Gap Analysis

### Principle 1: No Imports — Automatic Discovery

| What | Status |
|------|--------|
| Shell hooks auto-register repos on `cd` (zsh/bash/fish) | Done |
| Background scanner watches for new `.git` dirs | Done |
| `install.sh` installs editor plugins | **Gap** — prints "not yet available" |
| VS Code integration | **Gap** — planned, not implemented |
| Emacs/Neovim plugins require manual setup | **Gap** |

### Principle 2: No Waiting — Background Everything

| What | Status |
|------|--------|
| Indexing runs in background goroutine | Done |
| File watcher triggers re-index | Done |
| Daemon auto-starts on first client request | Done |
| Daemon auto-restart on crash | **Gap** |
| ReIndex blocks watcher goroutine | Minor gap |

### Principle 3: Minimize Latency

| What | Status |
|------|--------|
| Zoekt search is fast once indexed | Done |
| Incremental indexing (only changed files) | **Gap** — full re-walk every time |
| Plugin wire format double URL-encodes JSON | **Gap** |
| Content-hash cache invalidation | **Gap** — uses 24h TTL |

### Principle 4: UNIX Principles

| What | Status |
|------|--------|
| `--json` on all CLIs, clean exit codes | Done |
| Env vars for paths (POGO_HOME, POGO_PLUGIN_PATH) | Done |
| Configurable port | **Gap** — hardcoded 10000 |
| Config file support | **Gap** |
| Shell completions | **Gap** |

### Principle 5: Usable by Humans AND Agents

| What | Status |
|------|--------|
| `--json` output on all commands | Done |
| Claude Code `/pogo-discover` skill | Done |
| `pogo status` for indexing visibility | Done |
| Streaming/watch mode for long ops | **Gap** |
| Structured progress during indexing | **Gap** |

## Prioritized Next Steps

### Tier 1: High Impact, Close Core Gaps

1. **Incremental indexing** — Re-indexes entire repo on any file change. Track file hashes or use git tree-hash to detect actual changes. Biggest latency win available.

2. **install.sh wires up editor plugins** — The installer detects editors but doesn't install anything. Connect it to copy/symlink the emacs and nvim plugins that already exist.

3. **Configurable port + config file** — Hardcoded port 10000 is fragile. Add `POGO_PORT` env var at minimum; consider `~/.config/pogo/config.toml` for broader config.

4. **Daemon supervision** — No crash recovery today. Generate a launchd plist (macOS) or systemd unit (Linux) from `install.sh`, or add self-supervision to pogod.

5. **Wire nvim and bash tests into CI** — Tests exist but `ci.yml` doesn't run them. Low effort, prevents regressions.

### Tier 2: Quality and Polish

6. **Simplify plugin wire format** — Remove double URL-encoding layer. Use plain JSON over the RPC interface.

7. **Replace deprecated `ioutil.ReadAll`** — Trivial migration to `io.ReadAll`, removes deprecation noise.

8. **Fix emacs mode-line** — Dynamic `pogo-default-mode-line` function exists but the lighter is hardcoded to static `"pogo"`. One-line fix.

9. **VS Code extension** — Largest editor market share. MVP: project switcher + search using the existing HTTP API.

10. **Shell completions** — Add zsh/bash/fish completions for `pogo`, `lsp`, `pose`.

### Tier 3: Future Differentiators

11. **LSP workspace manager** — Serve `workspace/symbol` across repos. Editor-agnostic way to provide cross-repo navigation.

12. **Cross-repo operations** — Beyond `pose --all`: find-references across repos, dependency graphs, "which repos import X."

13. **Diagnostics plugin type** — Extend plugin interface beyond search to aggregated lint/diagnostic results.

14. **Git-aware cache invalidation** — Replace 24h TTL with HEAD-based or tree-hash-based invalidation that detects branch switches.

15. **Streaming search** — For `--all` queries, stream results per-repo instead of collecting all before output.

## Implementation Notes

- The plugin system (hashicorp/go-plugin) is the right abstraction, but clean up the wire format before adding plugin types.
- fsnotify has known macOS limits (kqueue per-FD). Consider FSEvents for better scaling on large monorepos.
- The emacs plugin is the most complete integration — use it as the template for VS Code.
- Zoekt is the right search engine. The main win is avoiding full re-indexing, not replacing it.
