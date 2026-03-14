# Editor & Shell Integration Feasibility Assessment

## Pogo API Surface (baseline)

Pogo exposes a REST/JSON HTTP API on `localhost:10000`:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Health check |
| `/projects` | GET | List all known projects |
| `/projects/{id}` | GET | Project details by ID |
| `/projects/file?path=` | GET | Project lookup by path |
| `/file` | POST | Register/visit a path |
| `/plugin` | POST | Execute plugin (search) |
| `/plugins` | GET | List plugins |
| `/status` | GET | Indexing status per project |

Search is done via the plugin endpoint with a JSON payload containing the query, project root, and timeout.

Existing integrations: **Emacs** (`emacs/pogo.el`) and **zsh** (`shell/.zshrc`).

---

## Per-Integration Assessment

### 1. Neovim

**Feasibility**: Fully feasible. Neovim has first-class Lua scripting and built-in HTTP via `vim.system()` / `curl` shelling. Telescope (dominant fuzzy-finder) supports custom pickers.

**What's possible**:
- Project switcher: `GET /projects` → Telescope picker → `:cd`
- Code search: `POST /plugin` (search) → Telescope picker with live preview
- Auto-visit on `DirChanged` event: `POST /file` on every directory change
- Indexing status in statusline: `GET /status`

**Effort**: **Small-Medium**
- Telescope picker for projects: ~100 lines Lua
- Search integration: ~150 lines Lua (Telescope custom source)
- Auto-visit hook: ~10 lines Lua
- Total: ~300 lines, single `pogo.nvim` plugin

**User reach/value**: **Very High**. Neovim is the most popular editor among power users who would use a CLI code search tool. The Telescope integration pattern is well-established.

**Dependencies**: None. Only uses existing HTTP API.

---

### 2. VS Code

**Feasibility**: Fully feasible. VS Code extensions can make HTTP requests via Node.js `fetch`/`http`. Extension API supports custom commands, quick picks, tree views, and workspace providers.

**What's possible**:
- Command palette project switcher: `GET /projects` → Quick Pick → open folder
- Code search via custom Search Provider or command with results panel
- Auto-visit on `onDidChangeWorkspaceFolders`: `POST /file`
- Status bar item showing indexing state: `GET /status`
- Tree view for projects sidebar

**Effort**: **Medium**
- Basic extension scaffold + project switcher: ~200 lines TS
- Search integration: ~300 lines TS (custom webview or search provider)
- Auto-visit + status bar: ~100 lines TS
- Marketplace packaging, `package.json` contribution points
- Total: ~600 lines + extension metadata

**User reach/value**: **Very High**. Largest editor market share. However, VS Code users already have good built-in search — pogo's cross-repo search is the differentiator.

**Dependencies**: None. Only uses existing HTTP API.

---

### 3. Helix

**Feasibility**: Limited. Helix has no plugin/extension system (as of early 2026). Integration is restricted to external commands via `:pipe` / `:shell` and custom keybindings.

**What's possible**:
- Shell out to `pose` for search, pipe results to picker (limited UX)
- Shell out to `lsp` for project switching
- No auto-visit (no directory-change hooks)
- No statusline integration

**Effort**: **Small** (but low fidelity)
- Documentation of keybinding recipes: ~1 page
- Optional wrapper script for fzf-based project picker: ~20 lines shell

**User reach/value**: **Low-Medium**. Helix is growing but still niche. The integration would be shallow without a plugin system — essentially documenting CLI usage, not a real integration.

**Dependencies**: Helix plugin system (blocked — not available yet). Revisit when Helix ships plugin support.

---

### 4. Zed

**Feasibility**: Partially feasible. Zed supports extensions written in Rust/WASM with a defined Extension API. However, the API surface is focused on language servers, themes, and slash commands — not arbitrary HTTP calls or custom pickers.

**What's possible**:
- Slash command extension: `/pogo-search <query>` in the assistant panel
- Potentially a custom language server that proxies pogo search results as workspace symbols
- No native quick-pick or project switcher API in extensions (yet)

**Effort**: **Medium-Large**
- Slash command extension: ~200 lines Rust, moderate build complexity
- LSP proxy approach: ~400 lines Go/Rust, more capable but higher maintenance
- Zed extension API is still evolving — may break between versions

**User reach/value**: **Medium**. Zed is gaining traction but still smaller than VS Code/Neovim. The slash command approach is novel but limited in UX compared to native pickers.

**Dependencies**: Zed extension API stability. A full integration may need Zed to expose richer picker/panel APIs.

---

### 5. Bash

**Feasibility**: Fully feasible. Same pattern as existing zsh integration.

**What's possible**:
- `cd` hook via `PROMPT_COMMAND` or `cd` function wrapper: `POST /file`
- `sp` alias for fzf project switcher: `lsp | fzf`
- Search alias: `pose` already works from bash

**Effort**: **Small**
- Port `shell/.zshrc` to bash: ~30 lines
- Handle `PROMPT_COMMAND` or `cd` wrapper instead of `chpwd`

**User reach/value**: **High**. Bash is the default shell on most Linux systems. Many users who would benefit from pogo use bash.

**Dependencies**: None.

---

### 6. Fish

**Feasibility**: Fully feasible. Fish has `--on-variable PWD` event handlers and `functions` for aliases.

**What's possible**:
- Directory change hook: `function __pogo_visit --on-variable PWD`
- Project switcher: `lsp | fzf` in a fish function
- Search wrapper: alias for `pose`

**Effort**: **Small**
- Fish config snippet: ~25 lines
- Fish's syntax differs from bash/zsh but the logic is identical

**User reach/value**: **Medium**. Fish is popular among developers who care about shell UX — a natural fit for pogo users.

**Dependencies**: None.

---

### 7. tmux

**Feasibility**: Feasible as a session/window management layer.

**What's possible**:
- tmux popup/menu for project switching: `lsp | fzf` in a display-popup
- Statusline segment showing current project's indexing status
- Key binding to search across projects in a popup

**Effort**: **Small**
- tmux config snippet + small shell script: ~40 lines
- Status segment via `#(curl -s localhost:10000/status | jq ...)`: ~1 line

**User reach/value**: **Medium**. tmux users are power users who would value quick project switching. Complements shell and editor integrations.

**Dependencies**: `jq` for status parsing (optional, could use `pogo status` CLI instead).

---

## Prioritized Recommendations

| Priority | Integration | Effort | Value | Rationale |
|----------|-------------|--------|-------|-----------|
| **1** | **Neovim** | S-M | Very High | Highest-value editor audience. Telescope pattern is clean. No API changes needed. |
| **2** | **Bash** | S | High | Trivial port of existing zsh integration. Broadens shell coverage to most Linux users. |
| **3** | **Fish** | S | Medium | Small effort, completes the shell integration trio. Natural pogo audience. |
| **4** | **VS Code** | M | Very High | Largest editor market but higher effort. Cross-repo search is the compelling differentiator. |
| **5** | **tmux** | S | Medium | Quick win. Complements shell integrations with popup project switcher. |
| **6** | **Zed** | M-L | Medium | API still maturing. Worth a slash-command extension now, full integration later. |
| **7** | **Helix** | S | Low | No plugin system. Document CLI usage only. Revisit when plugins ship. |

### Suggested Phases

**Phase 1 — Quick wins (all small effort, no API changes):**
- Bash shell integration
- Fish shell integration
- tmux config recipes

**Phase 2 — High-impact editor plugin:**
- Neovim plugin (`pogo.nvim`) with Telescope integration

**Phase 3 — Broad reach:**
- VS Code extension

**Phase 4 — Emerging editors:**
- Zed slash-command extension (when API stabilizes)
- Helix (when plugin system ships)

### API Gaps Identified

None of these integrations require new API endpoints. The existing HTTP API is sufficient for all planned integrations. The only potential enhancement would be:

- **Streaming search results** (SSE or WebSocket) for large result sets in editor UIs — nice-to-have, not blocking.
- **File watcher notifications** (WebSocket) so editors can refresh when indexing completes — nice-to-have.
