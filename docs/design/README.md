# Design docs

Design notes and proposals for pogo subsystems. Some describe features that have
since shipped — kept as rationale ("architecture archeology"), not as forward
plans. Others are proposals not yet built. Status is noted per doc; when in
doubt, the code is the source of truth.

| Doc | Covers | Status |
|-----|--------|--------|
| [agent-state-machine-design.md](agent-state-machine-design.md) | Explicit agent health states (Starting / idle / stalled) for `pogo agent diagnose` | Proposal — not implemented (mg-2ba0) |
| [blocked-reminder-design.md](blocked-reminder-design.md) | Why a `blocked:<agent>` hold now notifies the agent it names, why that is not the rejected park-sweeper, and why `parked`/`human` stay silent | Shipped (mg-3844, `internal/stallwatch/blockedreminder.go`) |
| [bridget-integration-design.md](bridget-integration-design.md) | Discord per-channel agent integration via a fork of `cloverross/bridget` | Proposal (mg-7921); see [investigations/bridget-fork-2026-05-09.md](../investigations/bridget-fork-2026-05-09.md) |
| [declarative-orchestration.md](declarative-orchestration.md) | Declarative TOML agent roles vs imperative prompt files | Shipped — Phase 1+2 (`auto_start`, `restart_on_crash`, `nudge_on_start`); kept for the why-TOML-not-X rationale |
| [drift-guards-design.md](drift-guards-design.md) | State whose drift leaves no artifact: the three guard shapes (re-assert / compare / log-the-transition), why ownership forces the choice, why COMPARE must log on the passing path too, and why a positive control that is a live defect is perishable | Branches 1–3 each shipped at one site (mg-de08, mg-fc99, mg-293c); §8 open — the `launchd activation` audit covers 3 of 13 loaded pogo jobs |
| [harness-provider-research.md](harness-provider-research.md) | Phase 1: which harness/model provider to add next (recommends OpenAI Codex) | Research; Codex provider since shipped (`internal/codex/`) |
| [indexing-strategy.md](indexing-strategy.md) | Timer-driven incremental re-index vs event-based file-watching | Adopted & shipped (mg-5b0d) |
| [mg-contract.md](mg-contract.md) | How pogo's tests may depend on the `mg` binary across a repo boundary: named clauses instead of unannounced coupling, which tests may be live at all, and the rule against flipping an assertion to match the dependency | Shipped (mg-216c, `internal/mgcontract`) |
| [mg-domain-audit.md](mg-domain-audit.md) | Whether macguffin's work-item store is domain-neutral (not coding-specific) | Audit; durable orientation, concrete follow-ups filed separately |
| [multi-provider-architecture-survey.md](multi-provider-architecture-survey.md) | Phase 2: provider-abstraction architecture (design-of-record) | Survey; Codex provider since shipped (`internal/codex/`) |
| [pa-thread-index-design.md](pa-thread-index-design.md) | pa's local-only, pointer-only thread-index git repo (payloads stay in self-mail) | Shipped (mg-da41, decision mg-9a32); machine-local state, archeology |
| [priority-wake-design.md](priority-wake-design.md) | Priority-aware fast wake so urgent work skips the coordinator idle-polling gap | Shipped (gh #61, `internal/stallwatch/`) |
| [prompt-customization-design.md](prompt-customization-design.md) | Customizing agent prompts so edits survive `pogo install --force` | Shipped (`internal/agent/tomlmerge.go`); user guide: [../prompt-customization.md](../prompt-customization.md) |
| [rate-limit-modal-watcher-design.md](rate-limit-modal-watcher-design.md) | Auto-dismissing the Claude API rate-limit-options modal | Shipped (mg-4421, `internal/claude/modal_hook.go`); archeology |
| [rating-dialog-watcher-design.md](rating-dialog-watcher-design.md) | Auto-dismissing Claude Code's mid-session rating dialog | Shipped (mg-4421, `internal/claude/modal_hook.go`); archeology |
| [refinery-concurrency-design.md](refinery-concurrency-design.md) | Per-repo merge lanes: why a lane is keyed on the repo's clone rather than its path, why the cap is 2, and what happens to a queue in flight when the change lands (upgrade *and* rollback) | Shipped (mg-37ad, `internal/refinery/lanes.go`); pm-pogo ruling 2026-08-05 |
| [roadmap-utility-design.md](roadmap-utility-design.md) | An `mg-roadmap` utility over `mg spend` for budget-aware planning | Proposal — not implemented (mg-3069) |
| [sandbox-design.md](sandbox-design.md) | Defence-in-depth sandboxing for polecat processes | Proposal — not implemented (mg-72bf) |
| [sleep-resilience-design.md](sleep-resilience-design.md) | Scheduling that survives host sleep: clock-jump `system_wake` detection, per-cadence replay policy, why `pogo schedule` is canonical over an in-harness cron | Shipped (mg-283e, mg-bcfa, mg-2f79, mg-baf6/mg-ef30); kept for §2/§4 rationale and §3's declined-cause calibration record |
| [spend-tracking-design.md](spend-tracking-design.md) | Token-spend tracking: `mg spend`, the spend store, `Agent.WorkItemID` | Shipped; archeology |
| [stall-watch-design.md](stall-watch-design.md) | pogod-side nudges when an agent's work piles up | Shipped (mg-b971, `internal/stallwatch/`) |
| [wedged-agent-detector.md](wedged-agent-detector.md) | Detecting an agent that ANIMATES but does no work; why the counter/uptime check gates on a frozen counter; why a 401 after a connectivity failure is one signature | Items 1–2 shipped (mg-fc8d, `internal/wedgewatch/`); **item 3 (escalation routing) is an open decision — see §5** |
