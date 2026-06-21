# Multi-Provider Harness Support — Phase 1 Research & Recommendation

**Status:** research / recommendation. Phase 1 of the multi-provider arc. Not implemented.
**Origin:** mg-fb9f (Daniel reminder 2026-05-20 08:32Z — *"add another harness/model provider to pogo … research the best next options, maybe codex"*).
**Author:** polecat (mg-fb9f).
**Sibling docs:** phase-2 architecture survey (architect-executed, filed after this lands); roadmap reflection is phase 3 (pm-pogo).

## TL;DR

**Add the OpenAI Codex CLI (GPT-5.5) as pogo's second harness provider.** It is the
closest architectural match to Claude Code — a persistent interactive TUI you can
attach to and nudge, plus a non-interactive `exec` mode, plus a one-flag
skip-approvals path and a file-based system-prompt mechanism. It is also the
strongest agentic coder available in mid-2026 (#1 SWE-bench Verified at 88.7%, #1
Terminal-Bench 2.0 at ~82%), so adding it buys real capability and rate-limit
failover, not just a redundant second path. **Google Gemini CLI is the clear
runner-up** and the natural *third* provider — open-source, huge free tier, 1M-token
context — but its agentic-coding performance trails both Codex and Claude, so it
should not be the one we prove the abstraction with.

On coupling: Daniel's hunch is **half right**. The *command invocation* is already
templated and swappable, and the modal/trust hook seam is already generic. But
there is **no first-class `Provider` concept** — Claude-Code-specific knowledge is
still scattered across config defaults, a validation function, an embedded wrapper
script, the nudge layer, and PTY/TUI assumptions. Defining that interface is the
phase-2 architect job; this doc only does the light scan (§5).

---

## 1 · What "provider" means for pogo

Pogo does not call a model API directly. It **spawns a CLI coding harness in a PTY**
and lives alongside it:

- Crew agents (mayor, PMs) are long-lived; pogo *nudges* them (writes to stdin)
  across their whole lifetime.
- Polecats run one task in a fresh worktree, then are stopped by the mayor.
- The spawn command is a Go-template string — today
  `claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}`.

So a "provider" here is **not** a model endpoint — it is a *terminal-native agentic
harness* that pogo can (a) launch as a long-running interactive TUI, (b) inject a
pogo-authored persona/operating-procedure prompt into, (c) run fully unattended
(no human approval prompts), and (d) nudge via stdin. Any candidate that only
offers one-shot `exec` with no resumable interactive session is a poor fit for the
crew model. This requirement does most of the filtering below.

## 2 · Candidate survey

### OpenAI Codex CLI (GPT-5.5) — **recommended**

- **Agentic / tool-use:** Purpose-built autonomous agent loop; OpenAI explicitly
  markets parallel multi-agent orchestration, built-in worktrees, long-running
  unattended tasks — pogo's exact use case. First-party web-search tool.
- **Coding performance:** #1 SWE-bench Verified (88.7% vs Claude Opus 4.7's 87.6%),
  #1 Terminal-Bench 2.0 (~82%). Claude still leads SWE-bench Pro (64.3% vs 58.6%).
  Net: at parity with Claude, ahead on terminal-agent tasks.
- **API maturity / stability:** OpenAI's API is the most battle-tested in the
  industry; Codex CLI is first-party, actively developed (shipped changes through
  April 2026), ~4M weekly developers.
- **Harness fit:** Full-screen interactive TUI **and** `codex exec` for
  non-interactive runs. Skip-approvals path:
  `--ask-for-approval never` + `--sandbox` (`--full-auto` is now a deprecated
  alias), and `--dangerously-bypass-approvals-and-sandbox` for the
  fully-unattended case — a direct analogue of `--dangerously-skip-permissions`.
- **Integration friction (moderate):** System-prompt injection is the rough edge.
  `experimental_instructions_file` (config key or `-c` flag) *replaces* Codex's
  tuned `BASE_INSTRUCTIONS` wholesale and validates the input — it is not additive
  like Claude's `--append-system-prompt-file`. The additive path is `AGENTS.md`
  (Codex reads it before any work). Cleanest mapping: write pogo's persona to
  `AGENTS.md`/`AGENTS.override.md` in the worktree (polecats already get a fresh
  worktree; crew can use `CODEX_HOME`/`AGENTS.override.md` to avoid clobbering a
  repo's own `AGENTS.md`). Additive system-prompt flags are an open request
  (codex#11588). The nudge layer and modal hooks will need a Codex dialect (its
  TUI is Rust/ratatui, not Claude's Ink) — but the hook seam is already pluggable.
- **Cost:** GPT-5.5 is roughly comparable to (slightly above) Claude Opus per
  token; cheaper Codex model tiers exist for lower-stakes agents. The real win is
  *failover* — a second provider sidesteps single-vendor rate limits (the very
  problem pogo's rate-limit modal watcher exists to paper over).

### Google Gemini CLI — **runner-up (add third)**

- **Pros:** Open-source (Apache-2.0); ReAct loop; native MCP; `GEMINI.md` context
  files (additive, friction-free for prompt injection); 1M-token context; very
  generous free tier (~60 rpm / 1000 req/day with a frontier model). Interactive
  TUI + `-p` non-interactive mode — structurally compatible with pogo.
- **Cons:** Agentic-coding performance trails both Codex and Claude on SWE-bench;
  Code Assist agent mode is still labelled "preview"; the harness is younger and
  less hardened for unattended multi-hour runs.
- **Verdict:** Excellent *second addition* once the abstraction exists — its free
  tier makes it ideal for cost-sensitive / high-volume crew agents — but not the
  provider to validate the interface with.

### Also considered (not recommended first)

- **OpenCode** — open-source, 75+ model providers. It is *itself* a multi-provider
  harness; layering pogo's own abstraction on top is redundant and confusing.
  Possibly interesting later as an escape hatch, not as a discrete "provider".
- **Aider** — pioneered terminal AI pair-programming, but it is edit/commit-loop
  oriented with a weaker fully-autonomous loop and no resumable attach-and-nudge
  TUI model. Poor fit for pogo's crew agents.
- **Cursor CLI / Sourcegraph Amp** — both credible, but Codex dominates them on
  every criterion that matters here; Amp's ad-supported tier is a poor basis for
  unattended production agents.

## 3 · Evaluation matrix

| Criterion (weight)            | Claude Code (incumbent) | Codex CLI (GPT-5.5) | Gemini CLI |
|-------------------------------|-------------------------|---------------------|------------|
| Agentic / tool-use            | Excellent               | Excellent           | Good       |
| Coding performance            | Excellent               | Excellent           | Good       |
| API maturity / stability      | Excellent               | Excellent           | Good       |
| Streaming / long context      | 1M context              | Large context       | 1M context |
| Cost vs Claude                | baseline                | ≈ parity            | Cheaper / free tier |
| Harness-integration friction  | n/a (incumbent)         | Moderate            | Moderate   |
| Interactive TUI + nudgeable   | Yes                     | Yes                 | Yes        |

## 4 · Ranked recommendation

1. **OpenAI Codex CLI (GPT-5.5)** — add first. Best capability, closest
   architectural fit, most mature unattended-agent harness besides Claude Code,
   meaningful rate-limit failover. Friction is real but bounded (system-prompt
   injection + a nudge/modal dialect).
2. **Google Gemini CLI** — add second/third. Open-source, cheap, long context;
   weaker autonomous coding. Best value once the interface is proven.
3. *Everything else* — revisit only if a specific need appears.

**Bottom line:** implement the provider abstraction against Codex, because doing so
forces every Claude-Code-specific assumption (system-prompt flag, skip-approvals
flag, nudge dialect, modal handling) into the open. An abstraction proven against a
*similar* harness (Codex) and a *different* one (Gemini, added next) is far more
likely to stay provider-neutral than one built around a single second case.

## 5 · Light scan — current Claude-coupling

*Light first-look only. The deep audit is phase-2 architect work.*

**Already provider-neutral (the seam Daniel remembers):**

- **Command invocation is fully templated.** `agent.ExpandCommand` /
  `CommandTemplateVars` (`internal/agent/command.go`) render a Go-template command
  with `{{.PromptFile}}`, `{{.AgentName}}`, `{{.AgentType}}`, `{{.WorkDir}}`.
  Configurable via `[agents] command`, `[agents.crew] command`,
  `[agents.polecat] command`, and the `POGO_AGENT_COMMAND` env override
  (`internal/config/config.go`). You can already point pogo at another binary.
- **The lifecycle hook seam is generic.** `Registry.SetPostSpawnHook(func(*Agent))`
  and `SetSessionHook(SessionHookFunc)` (`internal/agent/agent.go`) take arbitrary
  functions. The Claude-specific implementations live in their own package
  (`internal/claude/` — `trust_hook.go`, `modal_hook.go`) and are wired in
  `cmd/pogod/main.go`. Swapping in a Codex hook set needs no change to `agent`.

**Still Claude-Code-specific (no `Provider` abstraction):**

- **`DefaultAgentCommand` is hardcoded — and duplicated** in both
  `config.go:42` and `agent.go:277`, embedding the Claude-only flag
  `--append-system-prompt-file`.
- **`ValidatePolecatCommand` (`command.go:45`) hard-checks for the literal string
  `--dangerously-skip-permissions`** — a Claude-Code-specific flag. A Codex polecat
  uses `--dangerously-bypass-approvals-and-sandbox`; this validator would
  mis-warn.
- **`internal/agent/claude_wrapper.go` + embedded `scripts/pogo-claude.sh`** —
  a Claude-specific wrapper installed to a hardcoded `~/.pogo/bin/pogo-claude`.
- **`internal/claude/` package** — `modal_hook.go` (~590 lines) and
  `trust_hook.go` are tuned to Claude Code's specific TUI dialogs (rating dialog,
  rate-limit modal, directory-trust prompt). A second provider needs its own hook
  package (or none).
- **The nudge layer is implicitly Claude-tuned.** `internal/agent/nudge.go` and the
  PTY/winsize handling in `agent.go` carry comments and timing calibrated to
  "Claude Code's React/Ink input box". Codex's Rust TUI will likely need different
  nudge timing/escape handling.
- **`pogo doctor`** (`cmd/pogo/main.go`) hardcodes `claude` in its required-tools
  list; `service.go` PATH comments assume the `claude` CLI install location.

**Summary for phase 2:** the *plumbing* seam (command template + generic hooks) is
in place, but provider identity is not modelled. Phase 2 should define a
first-class `Provider` capturing: default command template, system-prompt
injection strategy (append-flag vs file-override vs context-file), skip-approvals
flag, nudge dialect, and the modal/trust hook set — then collapse the two
`DefaultAgentCommand` definitions and the literal-flag validator behind it.

## 6 · Hand-off

- **Phase 2 (architect):** deep coupling audit + `Provider` interface design +
  integration scoping, filed as a separate ticket after this doc lands.
- **Phase 3 (pm-pogo):** reflect the multi-provider arc in the roadmap; schedule
  the Codex-integration implementation polecat work.
