# pi provider — nudge calibration & integration findings

**Status:** calibration record for the pi provider (mg-9829). Evidence for the
`pi.Provider` values in `internal/pi/provider.go`.
**Harness measured:** pi coding agent **0.80.3**
(`@earendil-works/pi-coding-agent`, Node v22.23.1, macOS), spawned in a PTY at
pogo's default 200×50 winsize.
**Predecessors:** `docs/design/multi-provider-architecture-survey.md`,
`docs/investigations/codex-nudge-calibration.md` (method).

The measurement rig differs from the Codex one in a load-bearing way: pi
supports custom OpenAI-compatible providers via `models.json` and an isolated
config dir via `PI_CODING_AGENT_DIR`, so every experiment ran against a local
mock server — **no API key, no network, byte-level capture of what pi actually
sends to the model**. The same rig is baked into the repeatable e2e test
(`internal/pi/e2e_test.go`).

## TL;DR

The calibrated `NudgeProfile`:

| Field | Claude | Codex | **pi** | Why pi differs |
|---|---|---|---|---|
| `NeedsInitialNudge` | true | true | **false** | Initial task arrives via **argv** (`pi [messages...]`), not a typed nudge — see the gh #26 addendum below. |
| `InitialNudgeTimeout` | 60s | 30s | **30s** | Unused while `NeedsInitialNudge` is false; retained as the measured value (TUI ready ~1.5s from spawn; bound generous for the first-ever run's fd/ripgrep helper download). |
| `SubmitTerminator` | `\r` | `\r` | **`\r`** | A carriage return submits the composer. |
| `SubmitDelay` | 50ms | 50ms | **50ms** | NOT load-bearing for pi (see below) — kept for a uniform `Agent.Nudge` split-write path. |
| `IdleThreshold` | 2s | 2s | **2s** | No pi-specific constraint (no silent dialog race); kept uniform. |
| `PromptReadySentinel` | `? for shortcuts` | *(none)* | **`/ commands · ! bash`** | pi's keybinding hint line — the measured "input loop ready" marker. Unused for the initial task (argv delivery, gh #26); retained for any future wait-ready use. |

Plus `Provider.InitialPromptViaArgv = true` (not a `NudgeProfile` field): pi
accepts trailing positional messages, so `Registry.Spawn` appends the initial
task to the spawn argv — see the gh #26 addendum.

Plus the two integration decisions that are **not** `NudgeProfile` fields:

- **Persona injection is `InjectAppendFlag`, not a context file.** pi's
  `--append-system-prompt` accepts a file path and reads its contents into the
  system prompt.
- **`--approve` is the required non-interactive flag.** It pre-trusts the
  worktree and suppresses pi's only blocking dialog. No `PostSpawnHook`, no
  `SessionHook`.

## Method

A throwaway Go PTY spike (`creack/pty`, 200×50) spawned `pi` with
`PI_CODING_AGENT_DIR` pointed at a temp dir containing a `models.json` that
defines a custom provider (`api: "openai-completions"`, dummy key) whose
`baseUrl` is a local mock server. The mock captures each completion request
and streams back a fixed SSE reply (`PONG`), so a full turn runs offline. The
pipeline was then verified through pogo's real `agent.Registry` + `pi.Provider`
— see `internal/pi/e2e_test.go`:

```
PATH=<node>=22.19/bin:$PATH POGO_PI_E2E=1 go test ./internal/pi/ -run TestPiEndToEnd -v
```

## Measurements

### Startup cadence → `InitialNudgeTimeout`, `PromptReadySentinel`

- Spawn → first PTY output: ~0.8s (only on a first-ever run: "fd not found.
  Downloading..." / "ripgrep not found. Downloading..." helper installs into
  `<agent-dir>/bin`). With a warm agent dir the pre-TUI phase is **silent**.
- Spawn → full TUI render: **~1.4–1.5s**, arriving as one burst of chunks
  ≤0.1s apart, then quiet.
- An idle composer emits **zero PTY output** (0 bytes over a 10.5s idle
  watch). pi-tui uses differential rendering — like Codex's ratatui and unlike
  Claude's continuously-repainting Ink.

The silent pre-TUI phase is exactly the mg-ce61 trap: a quiescence-only gate
would call pi "idle" before its input loop exists. pi renders a keybinding
hint line under the composer when the input loop is up:

> `escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more`

`PromptReadySentinel` is the `/ commands · ! bash` slice of it. Caveat: the
`/` and `!` are keybinding names resolved from the user's `~/.pi/agent`
keybindings, so a custom keybinding config (or a pi UI change) could reword
the line — `WaitForReady` then degrades to best-effort wait-idle delivery,
which also works (measured: wait-idle + split write submitted reliably).
`TestPiEndToEnd` asserts the sentinel still appears through pogo's own
`StripANSI`, so a staleness regression is caught by the e2e run, and
`InitialNudgeTimeout` is 30s — double the worst startup observed including
helper downloads.

### Submit dialect → `SubmitTerminator`, `SubmitDelay`

- A carriage return (`\r`) submits the composer: the TUI enters
  `⠋ Working...` and the mock server receives the completion request.
- **pi has no paste-burst submit-swallowing**: writing body+`\r` as a single
  PTY chunk *also* submits (measured; unlike both Claude's Ink and Codex's
  ratatui, where the combined write leaves the text sitting in the composer).
- Literal `\n` bytes inside the body stay literal newlines: a 3-line body +
  trailing split `\r` arrived at the model as **one** user message with both
  interior newlines intact (byte-verified in the captured request).

So pi would tolerate `SubmitDelay: 0`; it is set to 50ms purely so
`Agent.Nudge`'s split-write path stays uniform across all three providers.

### `IdleThreshold`

No pi-specific constraint. There is no silent blocking dialog to race against
at spawn (`--approve` suppresses the trust dialog — see below), and a settled
composer is completely silent, so even a sub-second threshold would work. 2s
keeps the value uniform with Claude and Codex; the sentinel gate, not the idle
window, is what protects the initial nudge.

## Integration findings (not `NudgeProfile` fields)

### Persona injection → `--append-system-prompt <file>` (InjectAppendFlag)

The ticket flagged a possible collision between pogo's persona and a repo's
own `AGENTS.md` (pi auto-loads `AGENTS.md`/`CLAUDE.md` from the cwd, parents,
and `~/.pi/agent/`). The collision never materializes, because the right
injection point is not a context file at all:

- pi's `--append-system-prompt` flag accepts **literal text or a file path**;
  when the value names an existing file, pi reads the file into the system
  prompt (`resolvePromptInput` in pi's resource loader; verified byte-level —
  the persona marker appeared in the captured request's `system` message). So
  the provider uses Claude's injection shape:
  `pi --approve --append-system-prompt {{.PromptFile}}`.
- The worktree is never touched, and the repo's own `AGENTS.md` keeps loading
  as project context — the same posture as Claude reading a repo's `CLAUDE.md`.
- The rejected alternative: pi does read `.pi/APPEND_SYSTEM.md` from the
  project, but **only when the project is trusted** — project-local `.pi/`
  resources sit behind pi's trust system. Injecting there would couple persona
  delivery to trust state for zero benefit.
- No size cap was found on `--append-system-prompt` content (no
  `project_doc_max_bytes` analog needed; pi's 32 KB-style truncation applies
  to context files, not the system-prompt flag).

One side effect, acceptable: passing the flag takes precedence over a user's
global `~/.pi/agent/APPEND_SYSTEM.md` (pi resolves CLI sources before
discovery), so a pogo-spawned pi ignores that one global customization file.

### Project trust → `--approve` in the template; hooks nil

pi has exactly one blocking startup dialog:

> Trust project folder?
> … This allows pi to load .pi settings and resources, install missing
> project packages, and execute project extensions.
> → Trust / Trust parent folder / Trust (this session only) / Do not trust / …

Measured triggers: the dialog appears **only when** the cwd carries
trust-requiring `.pi/` resources (`settings.json`, `extensions`, `skills`,
`prompts`, `themes`, `SYSTEM.md`, `APPEND_SYSTEM.md`) **and** no saved trust
decision covers the path. A worktree of a repo with no `.pi/` dir never shows
it. But polecats run against arbitrary repos, so the template carries
`--approve`, which sets pi's trust override and returns **before any prompt
can render** (verified: same `.pi/settings.json` setup, `--approve`, zero
dialog) — hence `NonInteractiveFlags: ["--approve"]` and `PostSpawnHook: nil`,
where Claude and Codex both need a trust-dialog hook.

`--approve` trusts the worktree's project-local files for that run, which
matches the Claude/Codex posture (`--dangerously-skip-permissions` /
`--dangerously-bypass-approvals-and-sandbox`): pogo agents already run
unattended with full tool access in a repo the user chose to automate.

`SessionHook` is nil: pi has no built-in permission system, so there are no
tool-approval popups; errors (quota, auth) render inline in the transcript,
not as modals.

### Model selection & auth — left to pi's config

The template pins no `--provider`/`--model`: pi's own settings
(`~/.pi/agent/settings.json`, or its default) decide, and a pogo
`[agents] command` override can add explicit flags. Auth comes from provider
env keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) or pi's auth store
(`~/.pi/agent/auth.json` via pi's `/login` — which can also reuse a Claude
Pro/Max subscription). Note pi's factory default provider is `google`; a host
with no configured pi default and no `GEMINI_API_KEY` will error at the first
turn (inline, non-blocking) until the user picks a model — a pi-side setup
step, not a pogo concern.

## Addendum (gh #26): initial task via argv, not the typed nudge

Field evidence overturned one calibration conclusion. The "idle composer emits
zero PTY output" measurement (10.5s watch, single quiet spawn) does **not**
hold universally: during pi-harness smoke testing (pogo @ 920e8c9, pi 0.80.3),
pi-tui's differential renderer emitted near-continuous PTY writes — worst
under concurrent PTY load, e.g. a mayor dispatching several polecats at once.
The initial nudge's wait-ready gate (sentinel, then a 2s idle window) then
never fires: the window never opens, delivery times out
(`still producing output after 30s (last PTY write 68ms ago)`), and the
polecat sits "running" forever without ever receiving its task. Intermittent
and silent — direct spawns succeeded 4/4 while a mayor-dispatched spawn
stalled indefinitely.

The fix removes the race instead of re-tuning it: pi accepts trailing
positional messages (`pi [messages...]`), so the provider sets
`InitialPromptViaArgv = true` and `Registry.Spawn` appends the initial task to
the spawn argv as a single element — the harness reads it before the TUI even
starts. `NeedsInitialNudge` is now false; `InitialNudgeTimeout` and
`PromptReadySentinel` are unused on the initial path but retained above as the
measured record (and `TestPiEndToEnd` still asserts the sentinel renders, so
staleness is caught). Mid-session nudges are unaffected — they keep the
measured `\r` submit dialect and wait-idle delivery.

## Environment notes

- pi 0.80.3 requires Node ≥ 22.19 (`npm install -g --ignore-scripts
  @earendil-works/pi-coding-agent`; dist-tag `legacy-node20` exists for older
  Node). It was installed under nvm's v22.23.1 for this calibration.
- On first run per agent dir, pi downloads `fd` and `ripgrep` helper binaries
  into `<agent-dir>/bin` (~1s on a fast connection; the 30s
  `InitialNudgeTimeout` absorbs slow links).

## Reproducing

```
POGO_PI_E2E=1 go test ./internal/pi/ -run 'TestPi' -v
```

Needs only a `pi` binary on PATH — no API key, no network beyond localhost.
`TestPiEndToEnd` drives a real `pi` through pogo's `agent.Registry` +
`pi.Provider`, wired the way `cmd/pogod` wires it (per-type provider config,
claude as global default), and asserts: the spawn resolves pi via the
`[agents.polecat] provider = "pi"` config tier; the initial task is appended
to the spawn argv (gh #26) and starts a turn; the persona from
`--append-system-prompt` reaches the request's system message and the task
the user message (byte-level, via the mock provider); the repo's own AGENTS.md survives untouched and still reaches
the request (the mg-9829 collision regression); the reply renders; the
composer settles; a mid-session `Agent.Nudge` triggers a second turn; the
registry shuts the agent down cleanly. `TestPiHeadless` runs the same
provider flags in `--print --mode json` form (mg-3e7c).

GitHub CI runs both in the `pi-e2e` job (`.github/workflows/ci.yml`) against
a pinned `@earendil-works/pi-coding-agent@0.80.3` — the version calibrated
here. The ungated `internal/pi/integration_test.go` covers the config →
provider-registry resolution path in every plain `go test ./...`.
