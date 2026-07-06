# Design docs

Design notes and proposals for pogo subsystems. Some describe features that have
since shipped — kept as rationale ("architecture archeology"), not as forward
plans. Others are proposals not yet built. Status is noted per doc; when in
doubt, the code is the source of truth.

| Doc | Covers | Status |
|-----|--------|--------|
| [agent-state-machine-design.md](agent-state-machine-design.md) | Explicit agent health states (Starting / idle / stalled) for `pogo agent diagnose` | Proposal — not implemented (mg-2ba0) |
| [bridget-integration-design.md](bridget-integration-design.md) | Discord per-channel agent integration via a fork of `cloverross/bridget` | Proposal (mg-7921); see [investigations/bridget-fork-2026-05-09.md](../investigations/bridget-fork-2026-05-09.md) |
| [declarative-orchestration.md](declarative-orchestration.md) | Declarative TOML agent roles vs imperative prompt files | Shipped — Phase 1+2 (`auto_start`, `restart_on_crash`, `nudge_on_start`); kept for the why-TOML-not-X rationale |
| [harness-provider-research.md](harness-provider-research.md) | Phase 1: which harness/model provider to add next (recommends OpenAI Codex) | Research; Codex provider since shipped (`internal/codex/`) |
| [indexing-strategy.md](indexing-strategy.md) | Timer-driven incremental re-index vs event-based file-watching | Adopted & shipped (mg-5b0d) |
| [mg-domain-audit.md](mg-domain-audit.md) | Whether macguffin's work-item store is domain-neutral (not coding-specific) | Audit; durable orientation, concrete follow-ups filed separately |
| [multi-provider-architecture-survey.md](multi-provider-architecture-survey.md) | Phase 2: provider-abstraction architecture (design-of-record) | Survey; Codex provider since shipped (`internal/codex/`) |
| [pa-thread-index-design.md](pa-thread-index-design.md) | pa's local-only, pointer-only thread-index git repo (payloads stay in self-mail) | Shipped (mg-da41, decision mg-9a32); machine-local state, archeology |
| [prompt-customization-design.md](prompt-customization-design.md) | Customizing agent prompts so edits survive `pogo install --force` | Shipped (`internal/agent/tomlmerge.go`); user guide: [../prompt-customization.md](../prompt-customization.md) |
| [rate-limit-modal-watcher-design.md](rate-limit-modal-watcher-design.md) | Auto-dismissing the Claude API rate-limit-options modal | Shipped (mg-4421, `internal/claude/modal_hook.go`); archeology |
| [rating-dialog-watcher-design.md](rating-dialog-watcher-design.md) | Auto-dismissing Claude Code's mid-session rating dialog | Shipped (mg-4421, `internal/claude/modal_hook.go`); archeology |
| [roadmap-utility-design.md](roadmap-utility-design.md) | An `mg-roadmap` utility over `mg spend` for budget-aware planning | Proposal — not implemented (mg-3069) |
| [sandbox-design.md](sandbox-design.md) | Defence-in-depth sandboxing for polecat processes | Proposal — not implemented (mg-72bf) |
| [spend-tracking-design.md](spend-tracking-design.md) | Token-spend tracking: `mg spend`, the spend store, `Agent.WorkItemID` | Shipped; archeology |
| [stall-watch-design.md](stall-watch-design.md) | pogod-side nudges when an agent's work piles up | Shipped (mg-b971, `internal/stallwatch/`) |
