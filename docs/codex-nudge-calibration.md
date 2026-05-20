# Codex CLI provider — nudge calibration & integration findings

**Status:** calibration record for Phase 3B (mg-7f76). Evidence for the
`codex.Provider` values in `internal/codex/provider.go`.
**Harness measured:** OpenAI Codex CLI **0.132.0** (`codex-cli 0.132.0`,
`macos-aarch64`), spawned in a PTY at pogo's default 200×50 winsize.
**Predecessor:** `docs/multi-provider-architecture-survey.md` §3 Phase 3B.

The survey flagged the Codex nudge dialect as "the one genuine unknown" and
required it be measured against a live Codex CLI rather than copied from
Claude. This doc is that measurement.

## TL;DR

Codex's TUI is Rust/ratatui, not Claude's Node/Ink, and the two differ in
every measured dimension. The calibrated `NudgeProfile`:

| Field | Claude | **Codex** | Why Codex differs |
|---|---|---|---|
| `NeedsInitialNudge` | true | **true** | TUI opens at an empty composer; the task must be typed in. |
| `InitialNudgeTimeout` | 60s | **30s** | Codex is a native binary — renders to first-quiet in ~0.2–0.3s vs Ink's slow cold start. |
| `SubmitTerminator` | `\r` | **`\r`** | A carriage return submits the composer. |
| `SubmitDelay` | 50ms | **50ms** | Codex has paste-burst detection — a combined `body+\r` write does not submit. Split write required. |
| `IdleThreshold` | 2s | **2s** | Not a steady-state value — set by the spawn-time trust-dialog race (see below). |

Plus two integration findings that are **not** `NudgeProfile` fields but are
load-bearing for the provider:

- **`PostSpawnHook` is required.** Codex shows a directory-trust dialog that
  `--dangerously-bypass-approvals-and-sandbox` does **not** suppress.
- **The command template must raise `project_doc_max_bytes`.** Codex truncates
  project-doc content (the injected persona) at 32 KB by default.

## Method

A PTY-harness spike (`pty.fork`, 200×50, child `exec`s `codex
--dangerously-bypass-approvals-and-sandbox`) measured startup cadence and
submit behavior. The full pipeline was then verified through pogo's real
`agent.Registry` + `codex.Provider` — see `internal/codex/e2e_test.go`
(`POGO_CODEX_E2E=1 go test ./internal/codex/ -run TestCodexEndToEnd`).

## Measurements

### Startup cadence → `InitialNudgeTimeout`, and the idle baseline

- Time from spawn to first PTY quiescence: **~0.20–0.30s**. Startup render
  chunks arrive **≤0.105s** apart.
- A **settled composer emits zero PTY output** — 0 bytes / 0 chunks observed
  over a 6s idle watch. Unlike Claude's Ink, which repaints continuously,
  Codex's ratatui TUI is completely silent when idle.

So steady-state idle detection alone would tolerate a sub-second
`IdleThreshold`. `InitialNudgeTimeout` is set to **30s** — far below Claude's
60s — reflecting the fast cold start (it is only an upper bound; the nudge
fires at `IdleThreshold` once the composer is idle).

### Submit dialect → `SubmitTerminator`, `SubmitDelay`

- A **carriage return (`\r`) submits** the composer: a split write ending in
  `\r` drives the TUI from the composer into its `Working` state and a model
  turn begins.
- **Codex has paste-burst detection** (cf. the `disable_paste_burst` config
  key). Writing the nudge body and `\r` as a **single PTY chunk does NOT
  submit** — the `\r` is absorbed as a literal newline in the composer
  (post-submit: 327 bytes / 1 chunk, the text left sitting in the composer).
- A **split write** — body, delay, then `\r` — submits reliably. Verified at
  gaps of 50ms (×2), 20ms, and 10ms; all submitted. `SubmitDelay` is set to
  **50ms**: comfortably above the ~10ms floor (5× margin) and equal to
  Claude's value, so `Agent.Nudge`'s split-write path stays uniform.

This is the same class of bug as Claude's Ink paste-detection — independently
confirmed for Codex's ratatui composer.

### `IdleThreshold` — set by the spawn-time trust-dialog race

The binding constraint on `IdleThreshold` is **not** steady-state idle (a
settled composer is silent, so even <1s would do). It is the spawn sequence:

1. Codex renders the directory-trust dialog (~0.3s) and then goes **silent**.
2. `TrustDialogHook` detects it and presses Enter to dismiss it.
3. The initial nudge's wait-idle must not fire *during* the silent dialog —
   otherwise it types the task into the trust dialog, not the composer.

With `IdleThreshold = 1s` the margin between the dialog's silent gap and the
hook's dismissal was too thin and the race bit: the nudge's first word was
dropped (`"Reply with…"` → `"with…"`). `IdleThreshold = 2s` clears the
dialog's quiet gap plus the hook's worst-case dismiss latency (~1.3s), so the
nudge reliably lands in the ready composer. The hook also polls fast (250ms)
as defense-in-depth. Re-verified: the nudged task reaches Codex intact.

2s coincides with Claude's value, but the *reasoning* is Codex-specific (a
silent trust dialog + hook latency), not inherited.

## Integration findings (not `NudgeProfile` fields)

### Directory-trust dialog → `PostSpawnHook` required

On first launch in any directory not already in its trusted-projects list
(`~/.codex/config.toml` `[projects."<path>"]`), Codex shows:

> Working with untrusted contents comes with higher risk of prompt injection.
> Trusting the directory allows project-local config, hooks, and exec
> policies to load.  › 1. Yes, continue  2. No, quit  — Press enter to continue

Every polecat runs in a freshly-created worktree, so this dialog appears on
**every** spawn. `--dangerously-bypass-approvals-and-sandbox` does **not**
suppress it — that flag governs command approvals and the sandbox, not project
trust. The survey said to "add a `codex` hook only if Codex surfaces a dialog
the bypass flag does not suppress (determine empirically)" — it does, so
`codex.TrustDialogHook` (a `PostSpawnHook`, mirroring `claude.TrustDialogHook`)
presses Enter to accept it.

One rendering subtlety: Codex draws the dialog body glyph-by-glyph with cursor
positioning, so once ANSI escapes are stripped the inter-word spaces vanish
(`"untrusted contents"` → `"untrustedcontents"`). The hook collapses all
whitespace before matching its marker — see `matchesTrustDialog`.

No `SessionHook` is needed: Codex surfaces no mid-session modal requiring a
keystroke (the quota/rate-limit notice is an inline message; command approvals
are bypassed by the command flag).

### Persona injection → `AGENTS.override.md` + `project_doc_max_bytes`

- Codex reads **`AGENTS.override.md`** natively, and it **takes precedence
  over a checked-in `AGENTS.md`** at the same directory level (verified with
  `codex debug prompt-input`). So writing the pogo persona to
  `AGENTS.override.md` in the worktree injects it additively without
  clobbering the repo's own `AGENTS.md` — exactly as the survey's
  `ContextFile` strategy intended.
- Codex caps project-doc content at **32768 bytes (32 KB)** by default
  (`project_doc_max_bytes`): a 64 KB file is silently truncated to 32 KB,
  keeping the head and dropping the tail. The pogo persona delivered via
  `AGENTS.override.md` can exceed 32 KB once a large work-item body is
  templated in — and the tail carries the polecat protocol steps. The Codex
  command template therefore sets `-c project_doc_max_bytes=1048576`.

## Environment notes

- Codex 0.132.0 was installed via `npm install -g @openai/codex`; the native
  binary is symlinked onto `PATH` at `~/.local/bin/codex`.
- Codex authenticates from `~/.codex/auth.json`, seeded with
  `printenv OPENAI_API_KEY | codex login --with-api-key`. The bare
  `OPENAI_API_KEY` env var alone is **not** sufficient — Codex resolves it to
  "auth mode: none" until an explicit API-key login is performed.

## Reproducing

```
POGO_CODEX_E2E=1 go test ./internal/codex/ -run TestCodexEndToEnd -v
```

This spawns a real `codex` through pogo's `agent.Registry` + `codex.Provider`,
asserts the persona is injected into `AGENTS.override.md`, that
`TrustDialogHook` dismisses the trust dialog, and that the initial nudge
reaches the composer and Codex runs a turn on it. It is opt-in (it makes a
real OpenAI request) and skipped in the normal `go test ./...` run.
