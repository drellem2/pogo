# Cursor provider — nudge calibration & injection-collision investigation

**Status:** calibration record for the Cursor provider (mg-c146). Evidence for
the `cursor.Provider` values in `internal/cursor/provider.go`.
**Harness measured:** Cursor CLI **2026.07.09-a3815c0** (`agent`, Node bundle,
macOS), spawned in a PTY at pogo's default 200×50 winsize, authenticated via
`agent login`.
**Predecessors:** `docs/investigations/pi-nudge-calibration.md` (template),
`docs/investigations/codex-nudge-calibration.md` (method).

Cursor is the first pogo provider that is **closed-source SaaS**. That shapes
both the rig and the deliverable: there is no mock-provider escape (see
"Why the e2e is live"), so the model-facing assertions here are *behavioural*
rather than byte-level. Everything below was measured against a live, logged-in
CLI on 2026-07-10.

## TL;DR

The calibrated `NudgeProfile`:

| Field | Claude | Codex | pi | **Cursor** | Why Cursor differs |
|---|---|---|---|---|---|
| `NeedsInitialNudge` | true | true | false | **false** | Initial task arrives via **argv** (`agent [prompt...]`). Not (only) pi's redraw problem — a typed nudge would race the *silent* workspace-trust dialog. |
| `InitialNudgeTimeout` | 60s | 30s | 30s | **30s** | Unused while `NeedsInitialNudge` is false; retained as the measured value (trust dialog ~0.7s, composer settled ~3.0s). |
| `SubmitTerminator` | `\r` | `\r` | `\r` | **`\r`** | A carriage return submits the composer. |
| `SubmitDelay` | 50ms | 50ms | 50ms *(not load-bearing)* | **50ms — load-bearing** | Cursor has paste-burst detection: a combined `body+"\r"` write does **not** submit. Unlike pi. |
| `IdleThreshold` | 2s | 2s | 2s | **2s** | Settled composer emits 0 bytes; kept uniform. The silent trust dialog is the same trap Codex has. |
| `PromptReadySentinel` | `? for shortcuts` | *(none)* | `/ commands · ! bash` | **`Plan, search, build anything`** | Cursor's composer placeholder — the measured "input loop ready" marker. |

Plus `Provider.InitialPromptViaArgv = true` (not a `NudgeProfile` field).

Plus the integration decisions that are **not** `NudgeProfile` fields:

- **Persona injection is `InjectContextFile` into `.cursor/rules/pogo-persona.mdc`**,
  behind `alwaysApply: true` frontmatter. Cursor has **no**
  `--append-system-prompt` equivalent.
- **`--force` is the required non-interactive flag**, and **`--trust` must not
  appear** in the interactive template — it is rejected outside `--print`.
- **A `PostSpawnHook` is required** to dismiss the workspace-trust dialog. It
  presses `a`, not Enter.

## Method

A throwaway Go PTY spike (`creack/pty`, 200×50) spawned `agent` with
`CURSOR_DATA_DIR` pointed at a temp dir, so every run started from a
never-trusted workspace with no chat history and the developer's real
`~/.cursor` untouched. Behavioural probes ran through `agent -p --trust
--output-format text`. The pipeline was then verified through pogo's real
`agent.Registry` + `cursor.Provider` — see `internal/cursor/e2e_test.go`:

```
POGO_CURSOR_E2E=1 go test ./internal/cursor/ -run 'TestCursor' -v
```

## The injection-collision investigation (the ticket's wrinkle 1)

The ticket flagged that Cursor has no `--append-system-prompt`, so the persona
must go through a context file, which collides with a repo-owned `AGENTS.md`.
It asked whether `.cursor/rules`-in-worktree is a viable escape hatch. **It is,
with one non-obvious constraint.** No architect consult was needed.

### There is genuinely no prompt flag

Grepping the CLI bundle for `"--*prompt*"`-shaped flags yields exactly one:
`--no-prompt`. `agent --help` confirms: no append-system-prompt, no
system-prompt-file, no `experimental_instructions_file` analog. Cursor also
offers **no `AGENTS.override.md`-style precedence file** — the trick Codex uses.
So an additive injection point has to be a *different rules namespace*.

### What Cursor loads as rules

From the bundle's rule-watcher globs:

```
.cursor/rules/**/*.mdc     AGENTS.md     CLAUDE.md     CLAUDE.local.md     .cursorrules
```

`AGENTS.md` / `CLAUDE.md` / `CLAUDE.local.md` are all repo-owned by convention.
`.cursor/rules/**/*.mdc` is a *directory* of independently-named rule files —
so a uniquely-named `pogo-persona.mdc` collides with nothing a repo plausibly
ships.

### Frontmatter is load-bearing — and its absence fails silently

This is the finding that would have bitten us. A behavioural probe put a
directive in the repo's `AGENTS.md` ("include token `ALPHAGREET`") and a
different one in `.cursor/rules/pogo-persona.mdc` ("include token
`BRAVOGREET`"), then asked `agent -p` to "Greet me in one short sentence" —
never mentioning rules or files, so the model had no reason to *read* them with
a tool. Persona injected = `BRAVOGREET` present. 3 runs per variant:

| `.mdc` frontmatter | Persona injected | Verdict |
|---|---|---|
| *(none)* | **0/3** | Silently ignored. A plain-markdown copy — what `writeContextFilePrompt` did before this ticket — delivers **nothing**. |
| `alwaysApply: false` + `description` | 3/3 | Works *here*, but this is Cursor's "Auto Attached / Agent Requested" type: attachment is decided from the description, by the model. Not a contract. |
| `alwaysApply: true` | **3/3** | Cursor's documented "Always" rule type. Deterministic by construction. |

So the provider prepends:

```yaml
---
description: pogo agent persona
alwaysApply: true
---
```

An earlier version of this probe asked the model to "list every marker in your
rules/context", and **all three variants passed** — because Cursor has file-read
tools and simply grepped the worktree. That confound is worth recording: a
context-file injection test that lets the agent read the file it is testing
proves nothing. The behavioural directive avoids it.

### The collision does not materialize

With the repo's `AGENTS.md` and the pogo rule both present, a single reply
carried **both** tokens, and `AGENTS.md` was byte-identical afterwards. The
persona is additive: Cursor folds the always-applied rule into the system prompt
*and* still loads the repo's own context file. This is the same posture Claude
has when reading a repo's `CLAUDE.md`.

`internal/cursor/integration_test.go` pins the on-disk half of that contract
(ungated, no binary needed); `TestCursorEndToEnd` pins the model-facing half.

### Two shared-code changes it required

`agent.writeContextFilePrompt` previously did a bare `os.WriteFile` of the
prompt into `<dir>/<ContextFile>`. Cursor needs:

1. **`MkdirAll` of the parent** — `.cursor/rules/` does not exist in a fresh
   worktree, and `os.WriteFile` returns `ENOENT`, failing the spawn.
2. **`PromptInjection.ContextFileHeader`** — the frontmatter, prepended to the
   persona. Empty for Codex, whose `AGENTS.override.md` is plain markdown.

### Residue (fixed in mg-9de9)

The persona lands as an untracked file inside the polecat's worktree, so a
`git add -A` would commit it. This is **not new**: Codex's `AGENTS.override.md`
has the same posture. **Fixed in mg-9de9:** `writeContextFilePrompt` now appends
the injected `ContextFile` path (anchored, e.g. `/​.cursor/rules/pogo-persona.mdc`)
to the worktree's `.git/info/exclude`, so `git status` never lists it and
`git add -A` cannot stage it. `info/exclude` is repo-local and never committed,
so the repo the user owns is untouched — no `.gitignore` churn. The step is
best-effort: a non-git dir or unwritable exclude is logged, not fatal, since the
persona has already been delivered.

## Measurements

### Workspace trust → `PostSpawnHook`, and why `--trust` is banned from the template

Cursor blocks a never-trusted workspace behind a modal, ~0.7s after spawn:

```
🔒 Workspace Trust Required
Cursor Agent can execute code and access files in this directory.
Do you trust the contents of this directory?
  <path>
▶ [a] Trust this workspace
  [q] Quit
Use arrow keys to navigate, Enter to select, or press the key shown
```

Every polecat runs in a fresh worktree, so it appears on **every** spawn.
Measured against the two candidate suppressors:

| Command | Trust dialog? | Note |
|---|---|---|
| `agent` | **yes** (4263 bytes, blocks) | baseline |
| `agent --force` | **yes** (4263 bytes, blocks) | `--force` governs *command approval* ("Run Everything"), not workspace trust |
| `agent --trust` | **n/a — process exits** (66 bytes) | `Error: --trust can only be used with --print/headless mode` |
| `agent --force --trust` | **n/a — process exits** (47 bytes) | same |

So the template is `agent --force`, and trust is dismissed from the PTY by
`cursor.TrustDialogHook` — the shape `claude` and `codex` already use.
`TestCursorRejectsTrustFlagInTUI` pins the `--trust` rejection (on a PTY: without
one, Cursor auto-selects print mode and reports a *different* error), so if a
future Cursor accepts interactive `--trust`, the test fails and the hook can be
deleted in favour of the flag.

**The hook presses `a`, not `\r`.** Claude's and Codex's hooks send Enter, which
selects the *highlighted* row. Trust is highlighted today. If Cursor ever
reorders the menu, Enter would select `[q] Quit` and silently kill every
polecat. `[a]` is bound to Trust explicitly, so the worst case of a UI change is
a visibly stalled spawn.

Dismissal is silent-to-visible: after `a`, the TUI shows `⏳ Trusting
workspace...`, then the composer.

### Startup cadence → `InitialNudgeTimeout`, `PromptReadySentinel`

- Spawn → first PTY output: **~0.4–0.75s**.
- Spawn → trust dialog rendered: **~0.7s**.
- Trust dismissed → composer settled: **~2.3s** (composer settled ~3.0s from
  spawn).
- An idle composer emits **zero PTY output** (0 bytes over an 8s idle watch, and
  0 bytes in the e2e's 2s window). Cursor is a differential renderer, like
  Codex's ratatui and pi's pi-tui, and unlike Claude's continuously-repainting
  Ink.

The trust dialog is the mg-ce61 / Codex trap in a new costume: **once drawn it is
completely silent**, so a quiescence-only gate calls the harness "idle" while a
modal owns the screen. Cursor renders its composer placeholder once the input
loop is up:

> `→ Plan, search, build anything`

`PromptReadySentinel` is the `Plan, search, build anything` slice. It is absent
during the loading banner and the trust dialog, which makes it a precise
"input loop ready" signal. Caveat: after the first turn the placeholder becomes
`→ Add a follow-up`, so the sentinel matches only *before* any turn. That is
exactly and only where pogo uses it (`NudgeWaitReady` is the initial-nudge mode;
mid-session nudges use `NudgeWaitIdle`), so the narrowing is harmless.

`InitialNudgeTimeout` is 30s — an order of magnitude over the worst observed
startup.

### Initial task → argv, not the typed nudge

Cursor accepts trailing positional prompts (`agent [options] [prompt...]`), so
the provider sets `InitialPromptViaArgv = true`. Measured: the prompt **survives
the trust dialog** — Cursor parses it before the TUI starts and runs the turn
once the workspace is trusted (3/3 spawns replied; the composer placeholder
still rendered 3/3 before the turn replaced it).

pi adopted argv delivery to escape a redraw storm (gh #26). Cursor adopts it for
a different reason: a typed initial nudge would have to wait out
`TrustDialogHook`, racing a modal that reads as idle within ~0.5s of rendering —
precisely the race that made Codex's nudge type its task into the trust dialog.
Argv delivery removes the race instead of tuning against it.

### Submit dialect → `SubmitTerminator`, `SubmitDelay`

Measured by writing the same task into a settled composer two ways and watching
for a turn:

| Write shape | Submits? |
|---|---|
| `body`, sleep 50ms, `"\r"` (split) | **yes** — reply in ~4.2s |
| `body + "\r"` (one chunk) | **no** — no turn; the probe timed out at 95s |

So Cursor **has** paste-burst submit-swallowing, like Claude's Ink and Codex's
ratatui, and **unlike** pi (where the combined write also submits). `SubmitDelay`
is therefore load-bearing here, not merely uniform: setting it to 0 would wedge
every mid-session nudge. `TestProviderNudgeProfile` asserts it is > 0 with that
reason attached.

### `IdleThreshold`

A settled composer is completely silent, so steady-state would tolerate a
sub-second threshold. The binding constraint is the silent trust dialog, and
argv delivery already keeps the *initial* task clear of it. 2s keeps mid-session
wait-idle nudges uniform with the other three providers.

### `SessionHook`, `PTYSize`

`SessionHook` is nil: with `--force`, tool calls run without approval prompts and
errors render inline — no mid-session modal to dismiss. `PTYSize` is nil: Cursor
renders correctly at pogo's default 200×50.

## Model selection & auth — left to Cursor's config

The template pins no `--model`: Cursor's own config
(`~/.cursor/cli-config.json`, factory default `auto`) decides, and a pogo
`[agents] command` override can add an explicit `--model`. Auth comes from
`CURSOR_API_KEY` or Cursor's auth store (seeded by `agent login`). **Billing
draws on the account's Cursor plan credits** — every polecat turn costs the
account's quota, unlike a BYOK provider.

## Why the e2e is live, unlike pi's

pi's e2e runs fully offline: pi supports custom OpenAI-compatible providers, so
the test points it at a local mock and asserts byte-level on the captured
completion request. **The `agent` binary pogo drives admits no such rig** — but
the reason is narrower, and more interesting, than "it's closed-source".

### The obvious argument, and why it is not the whole story

Cursor speaks a proprietary connectrpc/protobuf protocol to `api2.cursor.sh`.
Reimplementing that to mock it would be absurd. But the bundle *does* contain a
local-provider mode, and it must be ruled out explicitly rather than waved past:

```
--local-agent-base-url <url>   Provider base URL for agent-cli-local
                               (OpenAI-compatible or Anthropic Messages;
                               for example http://127.0.0.1:11434/v1; can also
                               use CURSOR_LOCAL_AGENT_BASE_URL or
                               ANTHROPIC_BASE_URL env vars)
--local-agent-api-key <key>
```

plus `CURSOR_ENABLE_AUTHLESS`, `CURSOR_BEDROCK_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`.
That is *exactly* the shape pi's offline rig needs — an OpenAI-compatible base
URL. The flag is `.hideHelp()`-hidden, so `agent --help` never shows it.

### It is gated to a different distribution — measured, not assumed

The bundle gates every one of those on the CLI's own identity:

```js
function s(e = "agent-cli") {
  return "agent-cli-local" === e
    ? { rootDirName: "cursor-agent-local", executableName: "cursor-agent-local", … }
    : …
}
```

`agent-cli-local` is a **separate executable** (`cursor-agent-local`) shipped on
its own download channel (`agent-cli-local-prod`). The `agent` binary pogo drives
is `agent-cli`, and it does not enable any of it:

| Route tried on `agent` (agent-cli) | Result |
|---|---|
| `--local-agent-base-url http://127.0.0.1:PORT/v1` | `error: unknown option '--local-agent-base-url'` |
| `CURSOR_LOCAL_AGENT_BASE_URL` + `CURSOR_LOCAL_AGENT_API_KEY` + `CURSOR_ENABLE_AUTHLESS=1` | ignored — mock received **0** requests |
| `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | ignored — mock received **0** requests |
| `CURSOR_API_BASE_URL` | ignored for chat — mock received **0** requests (it fronts `${base}/auth/poll` only) |

Method note, because the first version of this probe lied. Asking Cursor to
"reply with exactly `PONGMOCK`" and then seeing `PONGMOCK` proves nothing: the
*real* backend will happily obey that instruction, and the reply is
indistinguishable from a mocked one. The probe was redone so the mock's canned
answer (`PONGMOCK`) **differs** from the token the prompt requests (`ALPHA`).
Every run returned `ALPHA` with zero mock hits: the real backend answered each
time. (A `--help`-based flag check lied too — `commander` prints help and exits 0
before validating options, so `agent --local-agent-base-url X --help` "succeeds"
on a flag the CLI does not have.)

**Conclusion, stated at the strength the evidence supports:** an offline e2e is
not achievable for the `agent` binary this provider targets. It is *not* a
statement about Cursor-the-company forever. If pogo ever targets
`cursor-agent-local`, a pi-style offline mock rig becomes available — that would
be a different binary, a different provider `Binary` value, and a different
ticket.

### What the live test buys instead

So `internal/cursor/e2e_test.go` follows the **Codex** pattern: opt-in, real
binary, real network, real plan credits. Losing byte-level capture costs the
"did the persona reach the system prompt?" assertion; it is recovered
behaviourally — the persona and the repo's `AGENTS.md` each instruct a distinct
token, and the reply must carry both. That is arguably a *stronger* statement
than "the bytes were in the request": it proves Cursor honoured both rule
sources.

The purely on-disk half of the contract needs no binary and is asserted ungated
in `internal/cursor/integration_test.go`, so it runs in the refinery gates and
GitHub CI.

**There is deliberately no CI job.** GitHub CI has no Cursor account, and the
tests spend credits. This matches Codex (also no CI job) and diverges from pi
(whose `pi-e2e` job is free and offline).

## Environment notes

- The CLI installs via `curl cursor.com/install`; the command is **`agent`**,
  renamed from `cursor-agent` in 2026 (the installer keeps a `cursor-agent`
  symlink). `Binary: "agent"` is a generic name — pogo's `ValidateCommandBinary`
  PATH check cannot distinguish Cursor's `agent` from an unrelated binary of the
  same name. `TestProviderIdentity` pins the expectation so a further rename
  fails loudly.
- `CURSOR_DATA_DIR` relocates Cursor's per-workspace state (trust decisions,
  chat history) away from `~/.cursor`. Auth survives the relocation, which is
  what makes the e2e's isolation viable. **`CURSOR_RULES` is not an env var** —
  it appears in the bundle only as a protobuf enum member
  (`COMPOSER_CAPABILITY_TYPE_CURSOR_RULES`); do not reach for it.
- The CLI is closed-source and churns fast (a command rename inside one year).
  The e2e logs a NOTE when the installed version differs from the calibrated
  `2026.07.09-a3815c0`, and pins the observable contract — binary name,
  trust-dialog text, composer placeholder, submit dialect — so a TUI change
  fails loudly rather than degrading silently.

## Reproducing

```
POGO_CURSOR_E2E=1 go test ./internal/cursor/ -run 'TestCursor' -v
```

Needs an authenticated `agent` on PATH and spends Cursor plan credits.
`TestCursorEndToEnd` drives a real `agent` through pogo's `agent.Registry` +
`cursor.Provider`, wired the way `cmd/pogod` wires it (per-type provider config,
claude as global default), and asserts: the spawn resolves cursor via the
`[agents.polecat] provider = "cursor"` config tier; the initial task is appended
to the spawn argv; `Spawn` writes the persona rule with `alwaysApply: true`; the
workspace-trust dialog appears **and** `TrustDialogHook` dismisses it (proven by
the composer placeholder following); the persona *and* the repo's `AGENTS.md`
both reach the model (both tokens in the reply); `AGENTS.md` survives
byte-identical; the composer settles (polled for a quiet window, not a single
sample — the flake that bit pi, mg-8cc0); a mid-session `Agent.Nudge` triggers a
second turn; the registry shuts the agent down cleanly.
`TestCursorHeadless` runs the same provider flags in `--print` form.
`TestCursorRejectsTrustFlagInTUI` pins the `--trust` rejection that forces the
hook to exist. The ungated `internal/cursor/integration_test.go` covers the
config → provider-registry resolution path and the on-disk injection contract in
every plain `go test ./...`.
