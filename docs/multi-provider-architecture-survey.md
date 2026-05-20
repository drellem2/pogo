# Multi-Provider Harness Support — Phase 2 Architecture Survey

**Status:** architecture survey / design-of-record. Phase 2 of the multi-provider arc. Not implemented.
**Origin:** mg-cd53 (phase 2 of the mg-fb9f arc; Daniel reminder 2026-05-20 08:32Z).
**Author:** architect.
**Predecessor:** `docs/harness-provider-research.md` (phase 1, mg-fb9f) — recommends **OpenAI Codex CLI** as provider #2, Gemini CLI as the later #3.
**Successors:** phase-3 implementation tickets (filed alongside this doc, routed to mayor); phase-3 roadmap reflection (pm-pogo).

> **Update (mg-b31b, 2026-05-20):** the arc shipped — 3A `mg-b56a`, 3B
> `mg-7f76`, 3C `mg-3e5f`, 3D `mg-6599`, all merged. Two statements below are
> now superseded: §2.2's "one `provider` field plus a `SetProvider` setter"
> and §2.3 / §3 / §5's "v1 scope: one global provider, per-type deferred".
> mg-b31b made provider selection **per-spawn**, not resolved once at pogod
> startup. The registry now holds a *map* of every known provider and resolves
> one per spawn from a precedence chain: `--provider` flag > `provider:` prompt
> frontmatter > `[agents.<type>] provider` > `[agents] provider` /
> `POGO_AGENT_PROVIDER` > built-in `claude`. `PostSpawnHook` / `SessionHook` /
> nudge dialect / PTY size travel with the agent's resolved provider, across
> restarts. A mixed Claude/Codex fleet needs no pogod restart. The change is
> purely additive — a config with only `[agents] provider` behaves exactly as
> before; no migration.

## TL;DR

Pogo's spawn *plumbing* is already half-neutral — the command is a Go template and
the two lifecycle hooks are generic function seams. What is missing is a
first-class **`Provider`** value that bundles the ~10 harness-specific decisions
currently scattered across `config`, `agent`, `claude`, and `cmd/pogod`. This doc
inventories that coupling precisely, proposes the `Provider` type and where it
lives (in `internal/agent`, to avoid an import cycle), and splits the integration
into three polecat-executable phases: **3A** a behavior-preserving refactor that
introduces `Provider` with Claude as the sole registered provider, **3B** the
Codex provider, **3C** doctor/docs/dead-code cleanup.

`ARCHITECTURE.md:52` already declares the intent ("should not depend on Claude
Code … mix Claude Code for some agents, a lighter harness for others"). This arc
makes the code match the stated architecture.

---

## 1 · Coupling inventory

Audit of the live tree (post-merge of mg-fb9f, mg-22c5, mg-4421). Two categories.

### 1.1 Already provider-neutral — the seam to build on

| Seam | Location | Note |
|------|----------|------|
| Command invocation | `agent.ExpandCommand` / `CommandTemplateVars` (`internal/agent/command.go:13-39`) | Go-template command with `{{.PromptFile}} {{.AgentName}} {{.AgentType}} {{.WorkDir}}`. Already swappable. |
| Command config layering | `AgentsConfig.AgentCommand()` (`internal/config/config.go:84-99`) | Precedence: `[agents.crew]`/`[agents.polecat]` command > `POGO_AGENT_COMMAND` env > `[agents] command` > `DefaultAgentCommand` const. |
| Post-spawn hook seam | `Registry.SetPostSpawnHook(func(*Agent))` (`internal/agent/agent.go:190,222`) | One-shot, runs in a goroutine after Spawn. Generic signature. |
| Session hook seam | `Registry.SetSessionHook(SessionHookFunc)` where `SessionHookFunc = func(context.Context, *Agent)` (`agent.go:200,235`) | Lifetime-scoped. Generic signature. |
| Prompt-by-env escape hatch | `POGO_AGENT_PROMPT` env (`agent.go:294`; exercised by `scripts/lib/fake-agent.sh`) | Pogo already injects the prompt path as an env var in addition to the template arg — a provider that cannot take an `--append-system-prompt-file`-style flag can read this. |

### 1.2 Claude-coupled — what `Provider` must absorb

| # | Location | Construct | Claude-specific value | Provider must supply |
|---|----------|-----------|-----------------------|----------------------|
| C1 | `config/config.go:42` | `const DefaultAgentCommand` | `claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}` | default command template |
| C2 | `agent/agent.go:231` | `const DefaultAgentCommand` (**duplicate** of C1) | same literal | collapse — derive from the provider |
| C3 | `agent/command.go:45-50` | `ValidatePolecatCommand` | literal `--dangerously-skip-permissions` substring check | declared non-interactive flag set |
| C4 | `config/config.go` `[agents]` schema | `AgentsConfig`/`AgentTypeConfig` | no `provider` key exists | a `provider` selector key |
| C5 | `agent/agent.go:353-373` | initial-nudge wiring + `InitialNudgeTimeout = 60s` | "Claude Code startup can be slow"; assumes a TUI prompt to bypass | needs-initial-nudge flag + timeout |
| C6 | `agent/agent.go:600-634` | `NudgeSubmitDelay = 50ms`, submit terminator `"\r"` | Ink/React input-box paste-detection workaround | submit terminator + inter-write delay |
| C7 | `agent/nudge.go:25-28` | `DefaultIdleThreshold = 2s` | tuned to Claude PTY output cadence / Ink redraws | per-provider idle threshold |
| C8 | `agent/agent.go:46-53` | `defaultPTYCols=200`, `defaultPTYRows=50` | Ink falls back to 80×24 at 0×0 | (reusable; optional provider override) |
| C9 | `internal/claude/trust_hook.go` | `TrustDialogHook` (post-spawn) | regex `(?i)safety.check`, 8s window, Enter-to-accept | provider post-spawn hook (may be nil) |
| C10 | `internal/claude/modal_hook.go` | `ModalHook` (session) + `DefaultModalMatchers` | markers `1:Bad 2:Fine 3:Good 0:Dismiss`, `Stop and wait for limit to reset` | provider session hook (may be nil) |
| C11 | `cmd/pogod/main.go:396,401` | `SetPostSpawnHook(claude.TrustDialogHook)` / `SetSessionHook(claude.ModalHook)` | hardcoded `claude.*` for every agent | hooks come from the resolved provider |
| C12 | `cmd/pogo/main.go:1481-1491,1396` | `pogo doctor` required tools | literal `"claude"` in the tool slice | provider binary name(s) |
| C13 | `internal/service/service.go:24,140-159` | launchd PATH comments | `~/.local/bin` "claude CLI usually lands here" | (PATH list reusable; optional provider PATH dirs) |
| C14 | `agent/api.go:441-444,629-633` | comments at both spawn sites | encode `--dangerously-skip-permissions` rationale | comments only — refresh |

### 1.3 Dead / unwired levers (decide their fate in 3C)

- **`internal/agent/claude_wrapper.go` + `scripts/pogo-claude.sh`** — embed-and-install a wrapper that does `mg init` then `exec claude "$@"`. `ClaudeWrapperPath()` has **no non-test callers** — dead code. The mechanism is generic; the only Claude-specific bits are the `exec claude` line and the `pogo-claude` filename.
- **`AgentMeta.Command` frontmatter field** (`agent/prompt.go:656`) — documented as a per-agent command override, parsed, but **never read** by any spawn path. A natural home for a future per-agent `provider:` key.

### 1.4 Load-bearing docs / prompts that assume Claude (3C)

`ARCHITECTURE.md` (config example, idle-detection description, diagram), `README.md:59` (Claude listed as a hard prerequisite), crew/polecat prompt templates (contrast `pogo schedule` with "Claude's in-process `CronCreate`"; reference `~/.claude/CLAUDE.md`), `agents/crew/doctor.md:41` (`which claude`). These are correct *today* but encode single-harness assumptions.

---

## 2 · The `Provider` abstraction

### 2.1 Shape

A `Provider` is **not** a model endpoint — it is a *terminal-native agentic
harness* pogo can (a) launch as a long-running interactive TUI, (b) inject a
persona prompt into, (c) run fully unattended, and (d) nudge via stdin. It is
mostly a **data descriptor** with two behavior fields (the hooks), matching
pogo's existing `func(*Agent)` hook style.

```go
// internal/agent/provider.go
package agent

type Provider struct {
    ID                  string          // config key: "claude", "codex", "gemini"
    Binary              string          // executable name — for `pogo doctor`, PATH
    CommandTemplate     string          // default Go-template spawn command
    PromptInjection     PromptInjection // how the persona prompt is delivered
    NonInteractiveFlags []string        // flags required to run unattended
    Nudge               NudgeProfile    // PTY input dialect
    PostSpawnHook       func(a *Agent)  // one-shot; nil = none
    SessionHook         SessionHookFunc // lifetime; nil = none
    PTYSize             *PTYSize        // nil = pogo default 200×50
}

type PromptInjection struct {
    Kind        PromptInjectionKind // AppendFlag | ContextFile | EnvOnly
    Flag        string              // AppendFlag: e.g. "--append-system-prompt-file"
    ContextFile string              // ContextFile: e.g. "AGENTS.override.md"
}

type NudgeProfile struct {
    NeedsInitialNudge   bool          // false if the prompt is passed as an arg
    InitialNudgeTimeout time.Duration // Claude: 60s
    SubmitTerminator    string        // Claude: "\r"
    SubmitDelay         time.Duration // Claude: 50ms (paste-detection gap)
    IdleThreshold       time.Duration // Claude: 2s
}
```

### 2.2 Package layering — avoiding the import cycle

The hooks take `*agent.Agent`, so the `Provider` type **must live in
`internal/agent`** (or a package `agent` does not import). If it lived in a new
`internal/provider` package, `agent.Registry` holding a `*provider.Provider`
would force `agent → provider → agent`. Placing `Provider` in `agent` mirrors
exactly how `postSpawnHook func(*Agent)` + `internal/claude` already compose:

```
internal/agent     defines  Provider, NudgeProfile, registry
internal/claude    imports agent; exports  claude.Provider  (agent.Provider value)
internal/codex     imports agent; exports  codex.Provider
cmd/pogod/main.go  imports agent + claude + codex; resolves config → provider,
                   calls agentRegistry.SetProvider(p)
```

`agent` never imports `claude`/`codex` — no cycle. Provider *selection* is a
small map or switch in `cmd/pogod`; `agent.Registry` gains one `provider *Provider`
field plus a `SetProvider` setter, and reads hooks / nudge timings / command
default from it.

### 2.3 Config & selection model

- New key `provider` under `[agents]` (default `"claude"`), plus optional
  `[agents.crew] provider` / `[agents.polecat] provider` for the
  crew-on-one-harness / polecats-on-another split the phase-1 doc anticipates.
  **v1 scope: one global provider**; per-type is a forward extension.
- New env override `POGO_AGENT_PROVIDER` (parallels `POGO_AGENT_COMMAND`).
- Precedence unchanged in spirit: an explicit `command` (config or env) still
  wins over the provider's `CommandTemplate` — the provider only supplies the
  *default* template, so existing deployments that set a raw `command` are
  unaffected.

### 2.4 How each coupling point resolves

- **C1+C2** → `Provider.CommandTemplate`; the two `DefaultAgentCommand` consts collapse to `claude.Provider.CommandTemplate`.
- **C3** → `ValidatePolecatCommand` checks "all of `provider.NonInteractiveFlags` present" instead of the Claude literal.
- **C4** → the `provider` config key.
- **C5–C8** → `Provider.Nudge` / `Provider.PTYSize`; the nudge consts become provider-sourced.
- **C9–C11** → `Provider.PostSpawnHook` / `SessionHook`; `cmd/pogod` wires `provider.PostSpawnHook` / `provider.SessionHook`, not `claude.*`.
- **C12** → `pogo doctor` checks `provider.Binary`.
- **C13–C14** → comment refresh; `service.go` may append `provider`-contributed PATH dirs.

---

## 3 · Phased integration plan

### Phase 3A — `Provider` abstraction (behavior-preserving refactor)  [keystone]

Introduce `agent.Provider` with **Claude as the sole registered provider** and
**zero behavior change**. This is the step that forces every Claude assumption
into the open.

- Define `Provider`, `PromptInjection`, `NudgeProfile`, `PTYSize` in `internal/agent`.
- `internal/claude` exports `claude.Provider` capturing today's exact behavior (command template, `AppendFlag` + `--append-system-prompt-file`, `["--dangerously-skip-permissions"]`, nudge `{60s, "\r", 50ms, 2s}`, `PostSpawnHook: TrustDialogHook`, `SessionHook: ModalHook`).
- Collapse the duplicate `DefaultAgentCommand` (C1/C2); make `ValidatePolecatCommand` provider-driven (C3); nudge consts (C5–C7) read from the active provider.
- Add the `provider` config key + `POGO_AGENT_PROVIDER` env (default `"claude"`).
- `cmd/pogod/main.go` resolves the provider and wires its hooks.
- **Acceptance:** all existing tests green; `claude` is the only provider; no behavior change; `command_test.go` / `prompt_test.go` fixtures updated.

### Phase 3B — Codex provider  [depends 3A]

- New `internal/codex` package exporting `codex.Provider`.
- Command template `codex --dangerously-bypass-approvals-and-sandbox …`; `NonInteractiveFlags: ["--dangerously-bypass-approvals-and-sandbox"]`.
- **Prompt injection:** `ContextFile` strategy — write the persona to `AGENTS.override.md` in the worktree (polecats already get a fresh worktree; crew via `CODEX_HOME` to avoid clobbering a repo's own `AGENTS.md`). `POGO_AGENT_PROMPT` remains as the env fallback.
- **Hooks:** scope a `codex` post-spawn / session hook only if Codex surfaces dialogs the bypass flag does not suppress; start with `nil` and add empirically.
- **⚠ Risk — nudge calibration.** Codex's TUI is Rust/ratatui, not Claude's Ink. `SubmitTerminator`, `SubmitDelay`, `IdleThreshold`, `NeedsInitialNudge` must be measured against a live Codex CLI. Recommend a short spike *inside* 3B before committing the `NudgeProfile`.
- **Acceptance:** a polecat spawned with `provider = "codex"` completes a trivial task end-to-end.

### Phase 3C — doctor / docs / dead-code cleanup  [depends 3A]

- `pogo doctor` provider-aware (C12); `service.go` PATH comments + provider PATH dirs (C13).
- Refresh `ARCHITECTURE.md`, `README.md`, crew/polecat prompt templates, `agents/crew/doctor.md` for multi-harness (§1.4).
- Resolve dead code (§1.3): recommend **deleting** `claude_wrapper.go` + `pogo-claude.sh` + tests unless a caller is wired; decide the `AgentMeta.Command` field — wire it, repurpose it as `provider:`, or remove it.

---

### Phase 3D — Codex provider e2e validation  [depends 3B]

Full end-to-end validation of the Codex provider on a real workload — see
`docs/codex-e2e-validation.md`. Outcome: a non-trivial dispatch completes
through the real `agent.Registry` + `codex.Provider` pipeline; persona
injection verified for both the polecat (worktree) and crew
(`~/.pogo/agents/<name>/`) paths — the `CODEX_HOME` mapping floated in §4 risk 4
proved unnecessary; the 3B `NudgeProfile` holds under a real dispatch; the
file-based `~/.codex/auth.json` credential is resolved robustly by a
launchd-started pogod's non-interactive children. Two items escalated to Daniel:
the auth mode to ship (`chatgpt` vs `--with-api-key`) and per-type provider
selection so a Codex polecat can be dispatched without a fleet-wide switch.

## 4 · Risks & open questions

1. **Codex nudge dialect (3B)** — the one genuine unknown; needs live measurement. Bounded by the spike.
2. **Codex system-prompt injection** — `experimental_instructions_file` *replaces* Codex's tuned base instructions; the additive path is `AGENTS.md`/`AGENTS.override.md`. An additive flag is an open upstream request (codex#11588). The `ContextFile` strategy is the safe choice; revisit if the flag lands.
3. **Per-type provider** — deferred to a forward extension; v1 is one global provider. Flagged so 3A's config schema leaves room (`[agents.crew]`/`[agents.polecat]` already exist as structs).
4. **Crew `AGENTS.md` collision** — a repo may ship its own `AGENTS.md`; the `AGENTS.override.md` + `CODEX_HOME` mapping avoids clobbering it. Confirm during 3B.
5. **Wrapper fate** — `claude_wrapper.go` is dead today; if native lifecycle hooks make wrappers unnecessary, 3C should delete rather than generalize.

## 5 · Hand-off

- **Phase 3A/3B/3C** — filed as polecat-executable tickets, routed to mayor for dispatch; 3B and 3C depend on 3A.
- **Phase 3 roadmap** — pm-pogo reflects the multi-provider arc and schedules the work.
- This doc is the **design-of-record**; the `Provider` shape in §2 is open to Daniel / pm-pogo comment before 3B/3C land — 3A is a safe behavior-preserving refactor regardless.
