# Codex CLI provider — Phase 3D end-to-end validation report

**Status:** validation record for Phase 3D (mg-6599). Builds on the Phase 3B
provider (mg-7f76) and its calibration record `docs/codex-nudge-calibration.md`.
**Harness validated:** OpenAI Codex CLI **0.132.0** (`codex-cli 0.132.0`,
`macos-aarch64`), model `gpt-5.5` (the ChatGPT-subscription default).
**Predecessors:** `docs/multi-provider-architecture-survey.md` (design-of-record),
`docs/codex-nudge-calibration.md` (3B calibration).
**Origin:** Daniel reminder 2026-05-20 10:09Z green-lighting full e2e testing of
the Codex provider once 3B landed.

## TL;DR

| # | Acceptance bar | Result |
|---|----------------|--------|
| 1 | A polecat completes a real (non-trivial) ticket end-to-end on `provider=codex` | **Pass** — via the live registry pipeline (see §2 for why not via pogod). |
| 2 | Prompt injection verified for both the polecat and crew paths | **Pass** — both paths deliver the persona; CODEX_HOME turned out unnecessary (§3). |
| 3 | Auth persistence across a pogod restart confirmed or documented as a gap | **Confirmed structurally** — codex resolves its credential from disk independent of the env; the literal restart re-check is escalated to Daniel (§5). |
| 4 | Findings written up | This document. |

No code-behavior changes: 3D adds two e2e tests and this report. Two items are
escalated to Daniel — the auth mode to ship and the pogod-restart re-check (§5),
and the single-codex-polecat dispatch limitation (§6).

## 1 · Environment

- Codex CLI **0.132.0**, native binary symlinked on `PATH` at
  `~/.local/bin/codex`. Model used by a turn: `gpt-5.5` (observed in PTY output).
- `pogod` is **launchd-started** — `com.pogo.daemon`, PID with PPID 1
  (`/sbin/launchd`). It is *not* a hand-started process; this matters for §5.
- Live fleet `provider`: **claude** — `~/.pogo/config.toml` has no `[agents]`
  section, so `cfg.Agents.Provider` defaults to `"claude"` (`config.go:45`).
- Codex auth on disk (`~/.codex/auth.json`): `auth_mode = "chatgpt"` — the
  interactive ChatGPT-subscription login, refreshed `2026-05-20T10:07:19Z`.
  `OPENAI_API_KEY` field is `null`; `tokens` carries `access_token` /
  `refresh_token` / `id_token` / `account_id`. See §5.

## 2 · Bar 1 — non-trivial real dispatch

### What was run

`internal/codex/e2e_test.go` gains `TestCodexEndToEndNonTrivial` (alongside the
3B `TestCodexEndToEnd` PONG smoke). It drives a **real** `codex` through pogo's
actual `agent.Registry` and the resolved `codex.Provider` on a non-trivial task:

> *Create `add.go` beginning with `package main` and containing a function with
> the exact signature `func Add(a, b int) int` that returns `a + b`.*

This is a genuine multi-step coding task — Codex must parse the persona
(`AGENTS.override.md`), parse a multi-clause instruction, **autonomously invoke a
file-mutating tool** (no approval prompt), and emit syntactically valid Go
matching an exact signature. Result, on the first run:

```go
package main

func Add(a, b int) int {
	return a + b
}
```

The agent completed in ~14s wall time. All five assertions passed: persona
injected, trust dialog dismissed, file created with correct content, composer
settled, clean shutdown.

### Why through the registry, not through pogod

The ticket says "dispatch a real polecat." The test drives the **exact pipeline
a `provider="codex"` polecat takes** — `providers.Resolve("codex")` →
`ExpandCommand` → `Registry.Spawn` (which writes `AGENTS.override.md`) →
`TrustDialogHook` → initial nudge. A *full pogod-supervised* polecat could not be
dispatched, by design and by the architect's own constraint:

- Provider selection is **global**: `cmd/pogod/main.go:421` resolves the provider
  once at startup from `cfg.Agents.Provider`. There is no per-spawn override —
  `pogo agent spawn-polecat` carries no `--provider` flag, and the daemon's
  registry has one provider for the whole fleet.
- Dispatching even a single Codex polecat through the live pogod therefore
  requires either a **fleet-wide** switch to `provider="codex"` *plus a pogod
  restart*, or per-type provider support (`[agents.polecat] provider`), which v1
  explicitly defers (survey §2.3, §4.3).
- The architect note for 3D forbids the executing polecat from restarting pogod
  (it would orphan this very test and disrupt other agents).

The delta between the registry pipeline and a pogod-supervised polecat is
entirely **provider-neutral pogo plumbing** — pogod process supervision, `mg
claim`/`mg done`, refinery submission — all exercised by every Claude polecat
(including the one that wrote this report) and carrying zero Codex-specific
surface. **The Codex-specific e2e surface is fully validated.** The
single-codex-polecat dispatch limitation is itself a finding — see §6.

## 3 · Bar 2 — prompt injection, both paths

`writeContextFilePrompt` (`internal/agent/command.go`) does **not** branch on
agent type: for an `InjectContextFile` provider it copies the persona into
`<Dir>/AGENTS.override.md`. Both paths were verified.

**Polecat path** — `Dir` is a fresh worktree. `TestCodexEndToEnd` /
`TestCodexEndToEndNonTrivial` spawn into a non-git temp worktree; Codex picked up
the persona and completed the task. ✔

**Crew path** — crew agents get `Dir = ~/.pogo/agents/<name>/`
(`internal/agent/api.go:424`), a stable pogo-managed directory that is **not a
checked-out repo**.

- Plumbing: new `TestSpawnContextFileInjectionCrew` (`internal/agent/agent_test.go`)
  confirms a `TypeCrew` spawn writes `AGENTS.override.md` into the agent dir. ✔
- Pickup: `codex debug prompt-input` run with cwd set to a non-git crew-style
  directory containing an `AGENTS.override.md` shows the file's content in the
  model-visible prompt list. ✔

**CODEX_HOME is unnecessary — survey §4 risk 4 is resolved, not open.** The
survey proposed a `CODEX_HOME` mapping for crew "to avoid clobbering a repo's own
`AGENTS.md`." That risk does not materialize: crew agents do not run inside a
repo — `~/.pogo/agents/<name>/` is already a per-agent isolated, non-repo
directory. The 3B type-agnostic in-directory injection is correct for crew as-is.
`CODEX_HOME` appears in no `.go` file and is not needed.

## 4 · Bar 3 — NudgeProfile calibration under a real dispatch

The 3B `NudgeProfile` (`docs/codex-nudge-calibration.md`) held under the real
non-trivial dispatch:

- **Submit** — the nudged multi-clause task reached the composer and was
  submitted; Codex ran turns and produced the file. `SubmitTerminator="\r"` +
  `SubmitDelay=50ms` split-write held.
- **Idle** — after the task, the ratatui composer emitted **zero** PTY output:
  the test's 5s post-task idle watch measured the buffer at `38775 → 38775`
  bytes (0 growth). Matches the calibration doc's "a settled composer emits zero
  bytes." `IdleThreshold=2s` is not stressed by steady state.
- **Trust dialog** — `TrustDialogHook` detected and dismissed Codex's
  directory-trust dialog on every spawn; the initial nudge landed in the ready
  composer, not the dialog (no dropped first word).
- **Exit** — Codex's TUI does not self-exit when idle; `Registry.StopAll`
  terminated the agent gracefully within the timeout on every run.

No calibration deltas. The 3B values are confirmed for real workloads.

## 5 · Auth persistence (Daniel's explicit flag)

### Mode tested

The validated mode is **`chatgpt`** (interactive ChatGPT-subscription login) —
that is the credential currently on disk (`~/.codex/auth.json`,
`auth_mode="chatgpt"`, refreshed 2026-05-20 10:07Z, ~2 minutes before Daniel's
green-light reminder). This is what a Codex agent would use **today**.

The architect note preferred validating the `codex login --with-api-key`
(static-key `auth.json`) path. That mode is **not** on disk now, and switching to
it (`codex login --with-api-key`) would replace Daniel's interactive ChatGPT
login — a side effect on his machine this validation polecat must not make
unilaterally. Choosing the ship mode is escalated below.

### Persistence finding — the file-based credential is robust

The GH_TOKEN/launchd failure class is: a credential lives in an **env var** that
only interactive shell init exports, so a launchd-started daemon's non-interactive
children never see it. **Codex's credential is not in that class.** It is a file
(`~/.codex/auth.json`), and Codex's discovery of it is robust:

- `pogod` is launchd-started and its environment has `HOME` set; it spawns agents
  with `cmd.Env = append(os.Environ(), …)`, so children inherit `HOME`.
- More strongly: `env -i PATH=… codex login status` — a **completely empty
  environment, no `HOME` at all** — still reports `Logged in using ChatGPT`,
  exit 0. Codex resolves `~/.codex` from the OS user database, not solely from
  `$HOME`. Even the worst-case stripped environment finds the credential.

Therefore a Codex agent (polecat or crew) spawned by the launchd-started pogod
**can read the `codex login` credential**. Auth *visibility* across a pogod
(re)start is structurally sound — a restart changes neither the filesystem nor
the process owner, and credential discovery depends on neither the env nor an
interactive session.

### Caveat — `chatgpt`-mode token lifetime

Visibility is solved; *lifetime* is the residual risk Daniel flagged ("as long
as it persists"). The `chatgpt` mode stores a short-lived `access_token`
refreshed via `refresh_token`. A non-interactive Codex child can perform that
refresh (it has network and write access to `auth.json`). But the `refresh_token`
itself can eventually expire or be revoked, and re-login then requires
interactive `codex login`. The `--with-api-key` mode stores a static key with no
expiry and no refresh — strictly more persistence-friendly, hence the
architect's preference.

### Escalated to Daniel — the pogod-restart re-check

Per the architect note, this polecat does **not** restart pogod. The literal
restart re-check is left to Daniel. Given the finding above it is a formality,
but to close it formally, after:

```
launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
```

confirm a non-interactive child still sees the credential:

```
env -i PATH="$PATH" codex login status   # expect: "Logged in ..." exit 0
```

(`env -i` reproduces the harshest case; a pogod-spawned child gets a strict
superset of that environment.)

## 6 · Findings / open items for Daniel

1. **Auth mode to ship** — the machine is in `chatgpt` mode; the architect
   preferred `--with-api-key`. Decide which mode pogo ships with. If
   `--with-api-key`, run `codex login --with-api-key` once (re-seeds
   `auth.json`); note it replaces the interactive ChatGPT login.
2. **Single-codex-polecat dispatch needs per-type provider.** *Resolved by
   mg-b31b (2026-05-20).* v1's global `provider` meant a Codex polecat could
   not be dispatched through the live pogod without a fleet-wide switch +
   restart. mg-b31b made provider selection per-spawn: `[agents.polecat]
   provider` / `[agents.crew] provider` config keys, a `provider:` prompt
   frontmatter key, and a `--provider` flag on `pogo agent spawn-polecat` —
   `pogo agent spawn-polecat --provider codex` now spawns one Codex polecat
   alongside a Claude fleet with no restart.
3. **pogod-restart auth re-check** — formality; see §5.

## 7 · Reproducing

```
# Non-live unit/integration tests (crew injection plumbing):
go test ./internal/agent/ -run TestSpawnContextFileInjection -v

# Live e2e — opt-in, real OpenAI request, needs a codex binary + auth:
POGO_CODEX_E2E=1 go test ./internal/codex/ -run TestCodexEndToEnd -v -timeout 600s
#   TestCodexEndToEnd          — 3B PONG smoke
#   TestCodexEndToEndNonTrivial — 3D non-trivial dispatch (this report)

# Crew-path persona pickup (no model call):
#   write AGENTS.override.md into a non-git dir, then from that dir:
codex debug prompt-input "test"   # the override content appears in the JSON
```
