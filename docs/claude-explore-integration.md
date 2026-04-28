# Claude Explore Integration: Scoping (mg-39b6)

This doc scopes the question: **does pogo's index need special config to be useful to the Claude Code "Explore" sub-agent, in the way that [codegraph](https://github.com/colbymchenry/codegraph) does?**

It is a research/scoping doc. No code in this work item — the architect approved a polecat for the scoping pass only (mg-39b6, mail 2026-04-28). If any work falls out, it is filed as a separate ticket.

## TL;DR

- **No special config is required today.** The Explore sub-agent inherits all tools from the main conversation, including Bash. A user with `pose` and `lsp` on their `PATH` can already use them from inside an Explore turn — no MCP server, no claudefile entry, no plugin.
- **Pogo's index is text-based, not semantic.** It is zoekt-backed full-text search plus a thin regex layer for "symbols" and a string-prefix heuristic for "definition vs. call vs. import." That is a fundamentally different shape from codegraph's SQLite call-graph.
- **If we want codegraph-level integration**, that is a *new component* (an MCP stdio server fronting a real semantic index), not a 50-LOC adapter. File it as an architecture ticket if we decide we want it.
- **Recommendation: option 3** — out of scope today; backlog the MCP-wrapper question with notes (below). No code work falls out of this scoping pass.

---

## What Claude Explore expects

Explore is a built-in Claude Code sub-agent. Anchored in:

- [Sub-agents docs](https://code.claude.com/docs/en/sub-agents) — "Claude Code includes built-in subagents like **Explore**, **Plan**, and **general-purpose**." Explore "searches or understands a codebase without making changes" and is invoked with a thoroughness level (`quick`, `medium`, `very thorough`).
- "Subagents can use any of Claude Code's internal tools. By default, subagents inherit all tools from the main conversation, including MCP tools." (sub-agents docs, "Tools and permissions").
- [MCP docs](https://code.claude.com/docs/en/mcp) — Claude Code consumes MCP servers over `stdio`, `http`, and (deprecated) `sse`. Servers expose tools that appear in the model's tool list alongside built-ins.

What Explore does *not* require, per those docs:
- A specific tool naming convention (no `explore_*` or `codegraph_*` prefix is mandated by Claude Code itself — codegraph chose those names).
- A specific data shape. Tool results are model-readable strings or JSON; there is no schema Explore enforces.
- Any configuration beyond what the parent conversation has. If the parent has `pose` on `PATH` and Bash permissions, Explore inherits both.

So "what Explore expects as input" reduces to: **whatever tools the parent conversation has access to** — Bash, Read, Grep, plus any configured MCP servers.

### What codegraph chooses to expose

For comparison, codegraph publishes an `stdio` MCP server (`codegraph serve --mcp`) registered under `~/.claude.json` `mcpServers`, with these tools (from its README):

| Tool | Purpose |
|------|---------|
| `codegraph_search` | Find symbols by name |
| `codegraph_context` | Build relevant code context for a task |
| `codegraph_callers` / `codegraph_callees` | Trace call flows |
| `codegraph_impact` | Analyze affected code |
| `codegraph_node` | Retrieve symbol details (with optional source) |
| `codegraph_files` | Get indexed file structure |
| `codegraph_explore` | Primary tool for Explore — returns full source sections in one call |
| `codegraph_status` | Check index health |

The README also adds a global Claude instruction telling agents to "use `codegraph_explore` as your PRIMARY tool." That is a *user-visible config*, not a hard requirement of Explore — it is just how codegraph nudges the model toward its tools instead of grep.

The data backing this is a SQLite knowledge graph (`.codegraph/codegraph.db`) with FTS5, populated by parsing 19+ languages (tree-sitter is implied, not verified from the README).

## What pogo currently exposes

Verified from the source tree on `main` (commit `38c5f4f`):

### Indexes / data stores

- **Zoekt full-text index** — `internal/search/search_index.go`, ~800 LOC. File contents are indexed by zoekt (`github.com/sourcegraph/zoekt`), watched with `fsnotify`, persisted under `~/.pogo/search/<repo>/`. This is plain text + trigram search; no AST, no symbol table.
- **Project list** — `internal/project`, exposed via `/projects` HTTP and the `lsp` CLI.
- **macguffin coordination state** — `~/.macguffin/work/` (work items), `~/.macguffin/mail/` (Maildir mail). Not a code index; this is the agent-coordination layer.
- **Event log** — `~/.pogo/events.log` (JSONL). Observability, not a code index.
- **Refinery merge history** — in-memory in `internal/refinery`. Not a code index.

### HTTP surface (pogod)

From `cmd/pogod/main.go` and `internal/workspace/handler.go`:

| Route | Purpose |
|-------|---------|
| `GET /projects` | List indexed projects |
| `GET /file?path=…` | Read a file's content/metadata |
| `POST /plugin` (search) | Run a zoekt query against a single project |
| `GET/POST /workspace/symbols` | Workspace-symbol query (LSP-style; **regex over zoekt results**, not a true index) |
| `GET /health`, `/health/full`, `/status` | Health & status |
| `/workitems`, `/agents`, `/refinery/*` | Agent-orchestration endpoints (out of scope here) |

### CLI surface

- `pose <query>` — zoekt search across one repo (or `--all` across all known repos). Supports `--json` and `-l` (list paths).
- `pose --refs <symbol>` — cross-repo references via `internal/xref`. Implemented as `pose` text search plus a string-prefix classifier (`func ` / `type ` / `var ` / `const ` → definition; `import` → import; otherwise call). Go-flavoured; not language-aware.
- `lsp` — list known projects.
- `pogo visit <path>` — register a repo with pogod.

### `/workspace/symbols` is not what its name suggests

`internal/workspace/workspace.go` defines an LSP-mirroring `SymbolKind` enum and a `WorkspaceSymbol` shape, then implements `searchRepo` by running a zoekt search and applying a list of regexes (`^\s*func\s+(\w+)`, `^\s*class\s+(\w+)`, etc.) to each matched line. Hits become "symbols." This works for the common case but it is text-pattern matching, not parsing — it cannot resolve overloads, scopes, type information, or call edges. The architect's spot-check ("not a code-symbol/AST index") is correct in spirit: the *shape* of the API is symbol-like, but the *data* is text-search results dressed up.

There is **no MCP server** in the tree (`grep -ri 'modelcontextprotocol\|mcpServers\|mcp-server' internal/ cmd/` returns nothing).

## The gap

Pogo's index and codegraph's index are different products:

| Capability | codegraph | pogo |
|---|---|---|
| Full-text search | FTS5 over symbols/files | zoekt over file content |
| Symbols | parsed AST nodes per language | regex over zoekt hits |
| Call graph (callers/callees) | yes (SQLite edges) | no |
| Impact analysis | yes | no |
| MCP stdio server | yes | no |
| Cross-repo | single repo at a time | yes (`pose --all`, `pose --refs`) |
| Languages | 19+ via parsers | text + regex (any language, but shallow) |

Pogo's distinguishing strength — *cross-repo* search — is not something codegraph does (codegraph indexes a single repo init'd in place). Pogo's distinguishing weakness for an Explore-style consumer is the lack of true semantic edges (callers/callees/impact). A wrapper cannot synthesize those from text search.

## Honest assessment

Two separate questions are tangled in the work item title:

1. **Does Explore need special config to use what pogo already provides?** No. Explore inherits Bash from the parent conversation; `pose`, `pose --refs`, and `lsp` are CLI tools, and Explore can call them today with zero pogo changes. The model needs to *know* they are useful — that is a prompting/CLAUDE.md question, not a config one — but no claudefile entry, MCP server, or plugin is required.
2. **Should pogo be an MCP server so Explore reaches for it instead of grep?** That is a product call. Codegraph achieves "instant lookups" because it has a real semantic graph; an MCP wrapper around zoekt would just be `pose` with extra steps. The token-saving payoff codegraph claims (94% fewer tool calls) comes from the graph, not the protocol.

Building only the protocol layer (MCP wrapper) without upgrading the underlying index would be cargo-culting the integration shape without the substance. Building the underlying index (parsers, call graphs, impact) is a several-week project, not a polecat task.

## Recommendation: option 3 (out of scope today, backlog)

No code falls out of this scoping pass. The reasons:

- Pogo already works with Explore today via the existing CLI surface (`pose`, `lsp`). If users hit limits, the first cheap improvement is *documentation* — a CLAUDE.md / sub-agent description nudging Explore toward `pose --all`, not a new protocol layer.
- An MCP wrapper alone does not get us closer to codegraph-style value — that value lives in the graph, not the transport.
- A real semantic index is an architecture-level decision (build, depend on tree-sitter, schedule sync, persist to SQLite or similar). It needs an architect-owned design, not a polecat.

### Backlog notes (for future tickets, not action items here)

If the team later decides to pursue codegraph-style integration, a reasonable sequencing is:

1. **CLAUDE.md / sub-agent prompt nudge** — one-paragraph addition pointing Explore at `pose --all` for cross-repo searches. Low-risk, no new components, proves whether Explore-via-CLI is "good enough" before we invest further. *Not opened as a ticket from this scoping pass; raise it if observed Explore behaviour is poor.*
2. **MCP stdio wrapper around existing `pose` / `lsp` / `pose --refs`** — would bundle pogo's current capabilities behind named MCP tools (e.g. `pogo_search`, `pogo_refs`, `pogo_projects`). Estimated 200–400 LOC plus an MCP SDK dependency, so well over the "50-LOC adapter" bar. Defer until (1) is shown insufficient.
3. **Semantic index (true call graph)** — separate architecture pass. Out of scope for any near-term ticket.

If/when (2) is filed, it should be an architecture ticket, not an implementation one — picking the Go MCP SDK, deciding on tool names, and writing a config snippet for `~/.claude.json` are all decisions that benefit from architect review before code lands.

## Sources

- Codegraph README (https://github.com/colbymchenry/codegraph) — MCP tool list, config snippet for `~/.claude.json`, `codegraph init -i` workflow, performance claims.
- Claude Code MCP docs (https://code.claude.com/docs/en/mcp) — transports (`stdio`, `http`, `sse`), `claude mcp add` syntax, `MAX_MCP_OUTPUT_TOKENS`.
- Claude Code sub-agents docs (https://code.claude.com/docs/en/sub-agents) — "Claude Code includes built-in subagents like **Explore**, **Plan**, and **general-purpose**"; tool inheritance from parent conversation; thoroughness levels.
- Pogo source on `main` at commit `38c5f4f`: `internal/search/`, `internal/workspace/`, `internal/xref/`, `cmd/pogod/main.go`, `cmd/pose/main.go`.

Sources on Claude Code internals beyond what is documented at the URLs above were deliberately not consulted — per the architect constraint, this doc avoids speculating on Explore's internal behaviour.
