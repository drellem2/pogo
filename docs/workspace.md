* Architect's research on workspace.md, an odd file I  found
Why it exists: to serve LSP-style cross-repo workspace/symbol queries — i.e.
  find symbol definitions across every project pogo has indexed, using zoekt
  search + regex classification. Originated from mg-3306 (feat: LSP workspace
  manager — serve workspace/symbol across repos, tier3, archived 2026-03-23 —
  the only linked mg).

  1) Linked mg's: Just mg-3306 (archived). No active work items reference it. No
   follow-up "expose to CLI" ticket exists.

  2) API: Yes, served. pogod wires it up at cmd/pogod/main.go:314-315:
  workspaceMgr = workspace.New()
  workspaceMgr.RegisterHandlers(http.DefaultServeMux)
  Endpoint: /workspace/symbols (GET with ?query=&repo=&limit=, or POST with JSON
   body). Registered on the default mux, not the orchestration-guarded one — so
  it's always available when pogod runs, even in index-only mode. Handler at
  internal/workspace/handler.go:13.

  3) CLI: No. A client wrapper exists at internal/client/workspace.go
  (QueryWorkspaceSymbols / QueryWorkspaceSymbolsPost) but nothing calls it.
  cmd/pogo/main.go has no workspace/symbols subcommand — the only workspace
  mentions there are about the agent/macguffin "workspace" (different concept).
  The refs subcommand at cmd/pogo/main.go:1811 uses xref, not this API. cmd/lsp
  and cmd/pose likewise don't hit it.

  Architecture take: This is a half-shipped feature — server + client lib done,
  no human-facing surface. The client wrapper is dead code today. Two options
  worth considering: (a) add a pogo symbols <query> command that calls the
  existing client (small, finishes the slice), or (b) decide it's only meant for
   editor/LSP integration and remove the orphaned client wrapper. I'd lean (a) —
   given that pogo refs already uses similar verbiage, a sibling pogo symbols
  keeps the CLI consistent.

  Want me to mail mayor with a recommendation to open an mg for pogo symbols
  CLI, or hold off?
