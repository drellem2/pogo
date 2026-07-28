# Orc: a comparative study, and what pogo/macguffin should take from it

**Subject:** `github.com/Redjive2/Orc` and the rest of Redjive2's public repos.
**Date:** 2026-07-28. **Work item:** mg-f1d5. **Commit studied:** `8f9d7e2`.

This is a study of somebody else's system, written to answer one question: what
does Orc do that pogo/macguffin does not, and what is the cheapest way to get the
parts worth having. Everything below marked **observed** was read out of Orc's
source or its checked-in specification; everything marked **inferred** is a
judgement of ours and may be wrong. Nothing here is Orc's code. Nothing here
proposes forking it.

---

## 1. What was found, and what was not

`macmuffin` is **not a separate repository.** Redjive2 has eleven public repos and
none of them is called that; `gh api users/Redjive2/repos?type=all` returns the
same eleven with no archived or forked entries. Macmuffin is a **Go module inside
the Orc monorepo** — `Macmuffin/go.mod`, binary `muff`, specified in
`Docs/Macmuffin/{Vision,Reference}.md`. (There is a `tayybah/macMuffin` on GitHub;
it is a zero-byte repo with no description and no relation to any of this.)

So "his macmuffin repo" is really "the macmuffin module", and the same is true of
five more tools nobody knew to look for. Orc is not an orchestrator — it is a
**suite**, of which the orchestrator is one part:

| Module | Binary | Go LOC | Is |
|---|---|---:|---|
| Orc | `orc` | 43,678 | Fleet: identities, roles, permissions, authority, sessions |
| Communiqué | `cq` | 31,911 | Remote web mirror of the fleet, and the sync that feeds it |
| Macmuffin | `muff` | 17,603 | Task tracker with **enforced** file scope |
| Orcprobe | `orcprobe` | 14,986 | Copy-the-whole-world sandbox for destructive testing |
| Mailman | `mailman` | 14,216 | Inter-agent mail: threads, CC, read receipts, a query language |
| Dock | `dock` | 10,521 | Section-addressable markdown reader (token thrift) |
| Anno | `anno` | 8,575 | Annotation-addressable code reader/writer (token thrift) |
| Common | — | 5,405 | Shared library |
| Theme | — | 1,435 | Shared library |

~148k lines of Go, standard library only, Go 1.26, one module per tool with
`replace` directives so each builds standalone. Docs under `Docs/` are
hand-written spec; `Claude/Docs/` is what its agents wrote.

**The rest of Redjive2's repos are out of scope and were checked, not assumed.**
`Phoe` is a Lisp interpreter in Go (`main.go`, `testdata/*.pho`, no README) —
a language, not orchestration. `Craftmine`, `Neutrino-v0.1`, `SoundBending`,
`Rumble-HighNoon`, `Rumble-NoMute`, `space-game`, `vifgame`, `UberGoober.js`, and
`interpreter` are games, audio tools, and a JS framework. None is pogo-related.

**The spec matches the source.** This matters, because a study of a README is
archeology. The verb table in `Docs/Orc/Reference.md` was checked against the
dispatch switch in `Orc/internal/cli/commands.go` and agrees command for command,
including the ones the doc marks as later additions (`activity`, `pace`,
`tariff`, `doctor`, `verify`, `owner`, `check-permission`). Store layouts were
read from `internal/store/store.go` in each module, not from prose.

---

## 2. Orc vs pogo — the orchestration diff

Both run Claude Code sessions under a supervisor, both own the child's pty, both
inject input through it, both have an attach path, both have a doctor. Underneath
that surface they answer two different questions. **pogo asks "what work is
there, and did it land"; Orc asks "who is this agent, and what may it do".**

### 2.1 What Orc has that pogo has none of

**An authority/permission/role model, enforced per tool call.** *(observed)* A
role carries an authority number (operator 100, everyone else 1–99). A permission
is a named set of clauses — `read(**)`, `write(Docs/** except vendor/**)`,
`spawn(24)`, `orc(new assign)`, `shell(** except rm curl)`, `tool(upgrade)` —
with a floor: only an identity at or above that floor may hold it. An identity
holds exactly one role plus direct grants, and **every grant lapses**. Nothing
effective is stored: an identity's authority is the lower of its role's and its
boss's, and its permissions are its role's plus its grants **intersected with its
boss's**, so `orc move <identity> <boss>` re-prices an entire subtree by editing
one line. A Claude `PreToolUse` hook checks every tool call against the result.

Three details are worth stealing the reasoning from even if we never build the
model:

- `shell` **denies by default** and every other kind narrows. With no `shell`
  clause an agent may run exactly `basename dirname echo false mailman printf pwd
  true` — commands that take no path and so cannot be turned into a file read by
  a clever argument. `ls`, `cat`, `head` and `tail` are deliberately excluded as
  second paths around `read(...)`.
- **A line that hides what it runs needs `shell(**)`.** `$(…)`, backticks, `sh
  -c`, `eval`, `xargs`, `python -c` are refused by any narrower clause, because
  the name in front of them says nothing about what would happen. Eager on
  purpose: a false positive costs a rephrase, a false negative costs the gate.
- **Containment is by clause, not by name, which is why `upgrade` is
  `tool(upgrade)`.** With a path clause, anyone holding `write-all` (floor 70)
  could hand on a permission whose floor is 90. No path glob covers `tool(…)`,
  and the floor is checked *as well as* the clause — the floor is the one part of
  a permission that is not a pattern, which makes it the one thing a pattern
  cannot argue past.

pogo has **none** of this and the gap is not close. Every pogo agent runs
`claude --dangerously-skip-permissions` (`internal/claude/provider.go`;
`docs/polecat-permissions.md` documents this as intended), so a pogo agent's
reach is the whole machine and scope discipline is prose in a prompt template.
See §5.1 — this is where the one shipped piece of this ticket lands.

**Per-identity credentials, and one credential contract for every tool.**
*(observed)* Orc mints a key per identity and provisions Mailman with the same
one; `$ORC_USER`/`$ORC_KEY` are exported into every session it starts, and
`mailman`, `muff` and `cq` authenticate every command against them. A half-set
pair is an error rather than a fallback, so a typo never silently promotes
anybody. Tools that need to know something about the caller ask Orc over an exit
code — `orc introspect --only identity`, `orc check-control <agent>` (0 controls
/ 8 does not), `orc check-permission <name>` — so Macmuffin holds no opinion
about authority and Orc holds none about tasks.

In pogo, agent identity is a **process name and an `--from=` flag an agent types
itself.** `mg mail send mayor --from=$POGO_AGENT_NAME` is unauthenticated; any
agent can send as any name, and a typo silently creates a phantom mailbox.

**Employment as a budget, priced by a stored tariff.** *(observed)* A session
costs `model_weight × effort_weight` (haiku 1 / sonnet 2 / opus 3; low 1 through
max 6), and a fleet is charged for being a fleet:
`total = ⌈Σ load × (crowd_base + n) / crowd_scale⌉`, so four sonnet/medium agents
cost 21 rather than 16. A budget is a `spawn(n)` clause; `orc budget engineer 24`
manages a `spawn-24` permission, removing the old one *first* so an interrupted
change lands on "no budget" and refuses work rather than on the old higher number.
The weights were constants and are now journaled by `orc tariff`, with
`--calibrate` proposing weights from the last week's **new** tokens (never cache
reads — "a tariff that counted cache reads would be pricing context rather than
work") and never applying them.

pogo has no admission control at all. Concurrency is whatever the mayor spawns.

**`orc wake` — a fleet-liveness cycle with a real theory of silence.**
*(observed)* This is the part of Orc most worth reading. It reads each session's
event feed, finds sessions **waiting** longer than `--after`, and pokes them.
Two rules keep it from becoming noise: only what is *waiting* is woken (a session
mid-turn is silent for good reasons, and a poke would queue a nudge into the
middle of work), and **each silence is woken once** (an agent that does not move
after a poke is stuck rather than idle; the next pass says so instead of filling
its context with nudges).

Then the case that defeats both rules, which Orc found the hard way: **a usage
limit is invisible to them.** The limit lands wherever the turn was, almost
always straight after a tool call — so the feed ends on a `PostToolUse`, which is
exactly what *working* looks like. Orc's writeup records seven agents stopped at
03:10 and still stopped twelve hours later, nine of those after the limit had
lifted. The fix is to read the fact where it exists — Claude's own transcript,
which carries an API-error line naming the reset time — and treat it as its own
state: before the reset, poke nothing and say how long is left (a poke spends the
agent's next turn on a second refusal *and* records a wake, which is how the
cycle decides it has already tried); after the reset, wake it **regardless of the
wake mark**, because the mark records that a silence was nudged and the reason it
did not move was the limit. A limit counts only if it is the *last* thing in the
transcript, or an agent that recovered is reported stopped for ever.

pogo has all the parts and has assembled a different machine from them, having
been bitten by the same class of bug: `pogo agent diagnose` does stall detection,
`internal/usagelimit` tracks limit episodes with hold-down and emits
`incident_episode_cleared{kind}` (mg-8d04, mg-55b2, mg-4904), `pogo schedule`
replays through host sleep, and `synthwatch` (mg-8cdb) exists precisely because a
failure read green for a day. **This is the one area where we are ahead**, and
the reason is instructive: pogo's signal is an event log plus a scheduler, and
Orc's is the transcript plus a cycle. Ours survives the host sleeping; theirs
needs no daemon.

**Delivery is confirmed, not assumed.** *(observed, and the best single idea in
the tree.)* Writing into a pty master succeeds whether or not anything is
listening. Measured against the real Claude binary, a message written while it is
starting is dropped — sometimes wholly, sometimes only the return that submits
it, leaving the text sitting unsent in the input box; the binary is still losing
input a second after it has finished painting. So Orc counts `UserPromptSubmit`
hook firings before and after typing, and when the count does not move it tries
**a bare return first** (submits text that is loaded but unsent; carries no
content so it cannot duplicate anything) and only then **the message again**. The
other order would double-deliver every merely-unsent message, "and an agent acting
twice on one instruction is a worse outcome than one that missed it". Past both
it *refuses* rather than logging a success. Two cases are never retried: a
mid-turn session (Claude queues the prompt legitimately) and a session that has
never written an event (it cannot report, so absence means nothing).

pogo's `pogo nudge` and the PTY nudge dialect write and hope. `internal/pi` has
calibration notes (`docs/investigations/pi-nudge-calibration.md`) but there is no
per-nudge delivery receipt. **This is a real, cheap win for us** — see §5.2.

**`orc activity` — per-agent token and file accounting, at minute resolution.**
*(observed)* Turns, tokens, and the files and lines an agent read and wrote, over
a window. Files are counted from Orc's own event feed (right whatever Claude's
file format does next); lines come from the transcript and degrade to missing
rather than failing. **Tokens are four numbers, not one** — on a real session,
3,614 input against 892,563,160 cache reads, so a single `tokens` column would
only ever show the second. Reading is incremental against a cursor; a transcript
that shrank is a rotation, and the reader says so, "because an hour counted twice
is visible and an hour lost is not". Buckets are per **minute**, folded to the
hour after twelve, because "is it working right now" and "did that change help"
are both questions about the last few minutes and an hourly reading answers both
with one bar.

pogo has `mg spend` (per work item, tag, repo, agent) and the event log. We do
not have per-minute buckets, a four-way token split, or read/write line counts.

**`orc pace` — stored intervals that reach a loop nobody has a shell open on.**
*(observed)* `--after`/`--every`/`--watch` are flags read once at process start,
so nothing but whoever started the process could change them and a browser could
not offer them at all. `pace` stores them, layered identity → role → fleet →
built-in, re-read at the top of every pass. Two deliberately opposite rules: a
`--after` typed on the line **wins** for that run (somebody debugging is deciding
about the run in front of them), and a **stored interval wins over the flag a
loop was started with** (a cycle running since Tuesday in a shell nobody has open
is exactly what a stored setting has to reach). `--off` is a state, not a zero,
"because an agent nobody is waking must look different from one being woken and
not answering" — and anything but a plain `yes` leaves the cycle running, since a
fleet that quietly stopped because a file said `off ` with a trailing space is a
fleet nobody is watching.

pogo's equivalent is `pogo schedule`, which is per-agent and durable but has no
layering and no off-state distinct from absence.

**`orc instruct` — composed standing instructions, with a diff.** *(observed)*
Fleet + role + identity prompts composed **additively** and passed as
`--append-system-prompt`; `orc instruct show <identity> --diff` prints exactly
what the agent gets. And the honest caveat, stated on the tin: **"a prompt asks;
a permission enforces"** — `orc doctor` deliberately lists the guards that hold
*and the ones that cannot*, "because a screen full of `in force` would leave you
believing the permission model is a fence when it is a request that one hook
enforces on one side of one tool layer".

pogo has this and arguably better: `~/.pogo/agents/` markdown with TOML
frontmatter, `extends` stubs, and `pogo agent prompt show` to render. Composition
is by `extends` rather than by three additive layers.

**Session resume across a supervisor giving up.** *(observed)* Five restarts with
backoff, then the supervisor records **how** the session ended — id, reason,
restart count, and whether it stopped **mid-turn** — and removes its state.
`orc tend` then *resumes* that session rather than starting a new one, and where
it stopped mid-call tells it to carry on, "since the turn it was inside will
never finish on its own". `refresh` and `fire` forget the ending: both are
somebody saying the conversation is over.

pogo's `restart_on_crash` respawns into a **fresh** session. Crew agents handoff
on context fill; there is no resume-the-same-conversation path.

**Seeding Claude's first-run state instead of clicking through it.** *(observed)*
Every identity gets its own `CLAUDE_CONFIG_DIR`, which used to mean every new
agent opened on the theme picker and then the trust prompt. Orc **merges** into
`.claude.json` — `hasCompletedOnboarding`, `hasTrustDialogAccepted` for the
workspace — and sets `skipDangerousModePermissionPrompt` in compiled settings,
because Orc's settings file *replaces* the operator's and so their existing
answer was being lost rather than inherited. Also: **do not set
`ORC_PERMISSION_MODE=dontAsk`** — it auto-denies any tool no allow rule covers,
and the allow list names no `Bash`, so every agent would be refused every command.

pogo solves the same problem by **watching the pty for the dialog and pressing
Enter** (`claude.TrustDialogHook`, hardened for late render in mg-ea45). Seeding
the file is strictly more robust than racing a render. **Cheap win** — §5.3.

**Two things an agent keeps, decided before the clauses are consulted.**
*(observed)* `<claude>/CLAUDE.md` (written once at creation, never again) and
`<claude>/memory/` sit **beside** the workspace, not inside it. Every `read`/
`write` clause is workspace-relative, so no permission that could be written or
granted reaches them — memory is protected structurally rather than by a rule
somebody could edit. Everything else in that directory stops, `settings.json`
most of all, since an agent that could edit it could switch off the thing
refusing everything else.

**`cq` — a remote mirror that is explicitly not a remote control.** *(observed)*
Two processes: `cq serve` on a reachable machine, `cq sync` on the agent machine.
The agent machine pushes its whole state up; the browser **queues** actions; the
next sync brings them down and applies them locally. **The server can never reach
back.** So everything done in the browser waits, and the site says so — it shows
how stale it is and marks queued things queued. Actions carry the view they were
made against (a digest for a file, a path for a workspace), so a change made
against a stale snapshot is **refused** rather than silently overwriting one made
in between; `cq queue` is where refusals turn up. Login is required to see
anything at all, and `cq serve` refuses to start until both a password and a
token exist.

pogo has no remote surface whatsoever.

**`orcprobe` — copy the entire world and break it.** *(observed)* `create` snapshots
every store; `shell`/`as` run inside it as any identity; `world`, `mail`, `tasks`,
`journal`, `timeline` read across every tool's store at once (`timeline` merges
all tools' events into one time-ordered table); `save`/`restore`/`diff`/`manifest`
checkpoint and compare. `manifest` reports what was copied, what was **neutered**,
and what was **refused**. No agent ever runs it and nothing depends on it.

pogo's equivalent is a pile of hard-won environment discipline (`POGO_HOME`,
`HOME`, `XDG_CONFIG_HOME`, `[agents] autostart = false`, `isEphemeralPath`) that
every test author has to get right by hand, and which this fleet has got wrong
repeatedly — mg-5336, mg-e8e7, mg-6092 are all "the test read the developer's
live state". **A packaged sandbox is a real gap** — §5.4.

**A shared exit-code vocabulary across every binary.** *(observed)* `0` ok, `1`
usage, `2` not found, `3` ambiguous, `4` parse, `5` i/o, `6` conflict, `7` auth,
`8` denied, `9` scope, `10` unavailable, `11` escape, `70` internal. Hook binaries
follow Claude's contract instead (0 proceeds, 2 blocks). One rule with teeth:
**an identity outside the caller's subtree is `2`, not `8`** — saying "you may
not" would confirm it exists. Same rule in Macmuffin for unseeable tasks.

**Colour is a layer, never information.** *(observed)* A test asserts that every
screen, stripped of escape sequences, is **byte-for-byte** the plain rendering.
Worth noting next to mg-d06a, where spaced sentinel strings broke because TUI
footers use per-word column moves and the spaces vanished under `StripANSI`. The
same class of bug; Orc pinned it with a test.

**`orc doctor` distinguishes a guard from a report.** *(observed)* The `wake
cycle` check is a **guard** and counts toward the exit code — every other guard
asks "is the wall holding", this one asks "is anybody watching", and a fleet with
no cycle recovers from nothing. The `sessions` section is **not** a guard and
never sets the exit code, because "a session at a usage limit is a fleet working
normally against a clock, and failing a cron every time an agent hits one is an
alarm nobody reads". The liveness answer comes from a **watcher registry**, not
from grepping `ps` for `orc wake --every` — which would count a cycle watching
somebody else's fleet as cover for this one.

### 2.2 What pogo has that Orc has none of

**A merge queue.** The refinery — submit, gate, merge, `already_merged`
detection, `lost` recovery across a pogod restart, PR-flow integration-branch
steps (mg-7746). Orc has no notion of landing work at all; `muff worktree` binds
a task to a git worktree and stops there. **This is the largest single thing we
have that they do not**, and it is the thing that turns an agent into a
contributor.

**Code intelligence.** `lsp`, `pose` (zoekt), `deps`, `refs`, project discovery
and indexing across every repo on the machine. Orc has `anno` and `dock`, which
are per-file readers, not a cross-repo index.

**Durable, sleep-resilient scheduling.** `pogo schedule` stores the next fire on
disk and replays through host suspend with an explicit at-most-once policy, plus
an **ack token** so a delivered fire can be distinguished from a fire whose work
actually completed — which exists because 647 deliveries were logged during a
23h30m outage in which every consuming turn died on an expired credential.
Orc's cycle memory "lives in the running process, not the store", by design.

**System-service integration.** launchd plists, `pogo service`, tier-3 recovery,
a nightly self-redeploy with drift checks and a post-deploy schedule audit
(mg-42ac). Orc is `sh/build`, `sh/push`, `sh/pull` into `~/.local/bin`.

**Credential expiry warning.** `pogo credential` / `credexpiry` warns *before* a
dated auth outage rather than detecting it after (mg-7024). Orc reads limits from
the transcript reactively.

**Windows session supervision** — neither has it, and Orc says so plainly:
`orc employ` refuses on Windows because a pseudoconsole must be attached at
creation through a process-thread attribute `os/exec` cannot set.

**Multi-provider agents.** pogo registers `claude`, `codex`, `pi`, `cursor`
behind `agent.Provider`. Orc is Claude-only and says so (`ORC_CLAUDE_BIN`).

### 2.3 The one-line difference

Orc is a **permission system that happens to run agents**; pogo is a **delivery
pipeline that happens to run agents**. Orc's centre of gravity is `authz`; pogo's
is the refinery. Neither has built the other's centre, and that is why the merge
list in §5 is short and asymmetric — most of Orc's mass is a model we would have
to want first.

---

## 3. Macmuffin vs macguffin — the work-item diff

### 3.1 Shape

| | Macmuffin (`muff`) | macguffin (`mg`) |
|---|---|---|
| Store | `~/.macmuffin` (`MACMUFFIN_HOME`, `XDG_DATA_HOME`) | `~/.macguffin` (`MG_ROOT`, `--root`) |
| Item on disk | `tasks/<name>/task.json` (write-once) + `journal.jsonl` (append-only) + `description.md` + `lock` | markdown file under `work/<status>/`; status **is** the directory |
| Mutation | append to journal, folded on every command | atomic rename between status directories |
| Locking | one advisory lock **per task** — "two agents race for *a* task, not for the pool" | atomic rename is the claim |
| Versioning | `version` file; unknown version is a hard error | none |
| History | per-task journal, every event attributed | git snapshots (`mg snapshot`, `mg log`) |
| Identity | `$ORC_USER`/`$ORC_KEY`, verified via `orc introspect` | `--from=` / `--assignee`, unverified |
| Bounds | MaxTasks 4096, MaxSubtasks 256, MaxCollaborators 64, MaxScopeEntries 512, MaxJournalLine 8 KiB, description 32 KiB | none |

### 3.2 What Macmuffin has that macguffin does not

- **Enforced scope.** `muff scope <task> <paths...>` limits editing to those
  paths, and it is a **fence, not a request**: `muff-hook` on `PreToolUse`
  refuses `Edit`/`Write`/`NotebookEdit`/`MultiEdit` outside it, and `muff
  check-scope` is what Anno calls before `anno write`. Which task is in force is
  *worked out*, not declared per call: `$MUFF_TASK`, else the worktree binding
  for the session's directory, else **no task and nothing is enforced** — so an
  agent that never opted in is never blocked. A path escaping the worktree is
  exit `11`, distinct from an ordinary refusal at `9`. The uncovered half is
  stated rather than implied: "writes through `Bash` other than `anno write` are
  out of reach."
- **Subtasks.** Flat list, `n/m` on the board, `complete --force` to finish over
  unfinished ones. The vision asked for groups; the reference gave no syntax, so
  the code left the field out entirely — "which is what makes adding one later an
  additive change rather than a migration".
- **Collaborators.** `invite`/`kick`/`leave`, distinct from the owner.
- **Draft privacy.** A task is a private draft until `push`. A task you cannot
  see is **not found**, not forbidden — "saying you may not would confirm it
  exists". The operator is not an exception.
- **The operator stands in for an absent owner.** `scope`/`complete`/`invite`/
  `describe`/`delete` are the owner's, so on a pooled task they refuse with
  "claim it first" — right for an agent, wrong for whoever runs the fleet. So an
  identity Orc names as operator owns any task **nobody** owns, and nothing else.
  Two limits, both the point: an owned task stays its owner's (not a master key),
  and a draft stays private. And `muff` **says so when it happens** — "nobody owns
  parser; acting as the operator" — because a change made with nobody on the task
  is otherwise a change the next reader cannot account for.
- **Two-axis scoring** (`priority` and `difficulty`, both 1–5) and a **four-value
  health status** (1 not working / 2 slow / 3 nominal / 4 done) that is separate
  from lifecycle. macguffin has one priority and conflates health with status.
- **`rebind <old> <new>`**, `worktree` bindings, and `verify`.
- **Notification as announcement, never as the fact.** `invite`/`kick` mail the
  agent, and a Mailman that is missing or broken **delays a notice rather than
  losing one** — queued to an `outbox`, retried by whichever agent next touches
  the store, and *never* failing a change that already happened. One that has
  given up is reported by `verify` rather than retried for ever. This is why
  Macmuffin never returns exit `10`.
- **The journal records that a description changed and who changed it, never the
  text** — "a record folded on every command must not carry 32 KiB of prose".

### 3.3 What macguffin has that Macmuffin does not

- **Dependencies and scheduling.** `mg schedule` promotes pending items whose
  dependencies are met; `mg shelve`/`unshelve` move an item **and its
  dependents**. Macmuffin has subtasks but no inter-task graph.
- **Archive/unarchive with status restoration**, and `mg sidecars`.
- **Typed workflows.** The `workflow:`/`stage:`/`gh:` carrier block welded to the
  body, with `mg new` refusing a workflow tag the body does not declare.
- **Spend accounting** (`mg spend`) and **throughput analysis** (`mg flow`).
- **A stable machine contract** (`mg schema` dumps the whole command tree).
- **Git-backed history** of the entire store.
- **Mail in the same tool.** `mg mail` is part of macguffin; Orc splits mail into
  Mailman. Ours is simpler to operate; theirs is a better mail system (§4).
- **Assignment that works.** `muff assign` is specified and **refuses with exit
  `1`**, because assigning work *to* an agent needs Orc's agent-control contract,
  "which does not exist yet, and inventing one now would mean rewriting it when
  the real one lands". Worth noting as a discipline: the tool ships the refusal
  and the reason rather than a guess.

### 3.4 Mailman vs `mg mail`

Mailman has **conversations** (`convo`), **CC that adds a user to a thread**,
**read receipts** (`check` — who has and has not read what you sent), a **query
language** (`=`, `!=`, `~`, over `to`/`cc`/`from`/`subject`/`unread`/`id`), and
`--sent`. Receipts are one file per reader "so two recipients never contend".

`mg mail` has send/list/read/reply/archive/migrate, with threading on `reply`.
No receipts, no CC, no query language, and — per this fleet's own memory —
mailbox names have no `mg-` prefix and sending to a wrong name silently creates a
phantom mailbox and loses the mail. **Mailman's `admin` view and `verify` would
both have caught that.**

---

## 4. Anno and Dock — the token-thrift pair

Neither has a pogo counterpart, and the idea behind both is worth stating
because it is not the idea `pose` implements.

**Anno** *(observed)* annotates any text file with end-of-line markers — `@:>`
open, `@:;` next-line-only, `@:<` close — over three nesting kinds (`section`,
`symbol`, `part`), with optional `[metadata]`. `anno index` prints a tree with
line counts and ranges; `anno read file.go@code:Operate^declarations` returns
just that content, **verbatim, so `read` and `write` are exact inverses**; `anno
write` replaces one annotation. A sigil inside a double-quoted string or
backticks is a **mention, not a marker** — without that rule, Anno's own source
would be a file Anno refuses to read. The single quote is excluded because it is
an apostrophe far more often than a delimiter.

**Dock** *(observed)* is Anno for prose, with the difference that shapes it:
**Dock has no syntax of its own.** A section is a markdown heading carrying a `§`
number, a link is an ordinary markdown link, so a Dock document renders normally
and costs a human nothing. Three self-checking rules — depth (`#`s equal dotted
components), parent, sequence (siblings run 1,2,3 with no gaps) — and Dock
refuses to guess when any is broken, because the structure is stated twice.
A heading without `§` is ordinary prose, so markup is incremental and a document
with no `§` is invisible. `read` returns one section's own prose — not the file,
not the neighbours, not the heading you just named; `--follow=n` walks links to a
depth, emitting each section at most once; `--budget=<lines>` stops early **and
says exactly what it left out**. The accepted cost is honest: `§` is not a
markdown anchor, so these links do not navigate in a rendered viewer — that is
the price of a stable number instead of a slug that changes when someone edits a
heading.

Both are wired to Claude hooks: after an agent reads a marked document, the hook
hands back its index, so the next thing it does addresses a section by name
instead of re-reading the file.

**The difference from `pose`:** `pose` finds *which file*; Anno and Dock decide
*how much of it to spend*. They are complements, not competitors — and given
mg-9a89 measured pogo's own auto-inject budget, the "spend the part that answers
the question" framing is directly relevant to us.

---

## 5. Reimplementation plan — cheapest first

Ranked by (value ÷ cost). Each says what already exists in pogo/macguffin, what
must be built, and whether shell is enough. **Clean-room from this spec only** —
none of Orc's code is copied or vendored.

### 5.1 Scope enforcement for polecats — **shell, and shipped with this ticket**

*The gap.* pogo's polecat prompt says "Stay scoped. Only work on your assigned
task." Nothing enforces it. Agents run `--dangerously-skip-permissions`, so an
edit anywhere on the machine is one tool call away, and a polecat that wanders is
caught at review time or not at all. Macmuffin's answer is `muff scope` + a
`PreToolUse` hook, and that half of the model is separable from the authority
model entirely — it needs no roles, no keys, and no daemon.

*What exists.* Work items with bodies that already carry a `workflow:`/`stage:`
carrier block. Worktrees at a known path. A hook-capable harness.

*What is built.* `scripts/mg-scope-guard.sh` — one shell script, two modes:

- **hook mode** (no args, PreToolUse JSON on stdin): refuses `Edit`, `Write`,
  `MultiEdit`, `NotebookEdit` outside scope, exit 2 with the reason.
- **`check-scope` mode** (paths as args): exit 0 in scope, 9 out, 11 escape —
  Macmuffin's contract, so anything else can ask.

Scope is declared in the work item body as `scope: <patterns>` carrier lines,
matching macguffin's existing convention. The item in force is `$MG_SCOPE_ITEM`,
else a `.mg-scope` file at the worktree root, else **none — and nothing is
enforced.** Opt-in at every step: no item, no scope, or no declaration means the
guard allows. It is **not wired into any agent by default**; `docs/mg-scope.md`
is the installation instruction. Same honest caveat as Macmuffin's: **writes
through `Bash` are out of reach**, and the doc says so rather than implying
cover.

*Not adopted.* The `$MUFF_TASK`-else-worktree-binding lookup needs a bindings
store; a file at the worktree root is the same idea with no store.

### 5.2 Confirmed nudge delivery — **Go, small, high value**

*The gap.* `pogo nudge` writes to the pty master and returns success. Orc
measured that a message written to a starting Claude is dropped — sometimes only
the submitting return, leaving text unsent in the box — and that there is no
moment to wait for and no output that says "ready".

*What exists.* pogo already installs Claude hooks per agent and already has an
event log. A `UserPromptSubmit` hook appending one event per submitted prompt is
the whole of the missing signal.

*What to build.* Count submissions, type, wait for the count to move; on no
movement send **a bare return first** (fixes the common unsent case and cannot
duplicate), then **the message again**; then **refuse**. Never retry a mid-turn
session or one that has never emitted an event. Estimated: one hook registration,
one event kind, ~150 lines in the nudge path plus tests.

*Why it matters here specifically.* Every polecat's first action depends on the
10-second startup nudge landing (`docs/polecat-permissions.md`). A dropped nudge
is a polecat that claims nothing and sits until somebody notices.

### 5.3 Seed Claude's first-run state instead of racing the dialog — **Go, tiny**

*The gap.* `claude.TrustDialogHook` polls the pty for the trust dialog and presses
Enter, hardened to 8s and then further for late render (mg-ea45). It is a race,
and it has already been lost once.

*What to build.* Merge `hasCompletedOnboarding` and `hasTrustDialogAccepted` for
the target workspace into the agent's `.claude.json` at spawn (merge, never
overwrite — Claude keeps history there), and carry
`skipDangerousModePermissionPrompt` into compiled settings. Keep the pty watcher
as the backstop it should have been. Days of work, not weeks; removes a race from
every spawn.

### 5.4 A packaged test sandbox — **shell, medium**

*The gap.* Isolating a pogo test needs `POGO_HOME`, `HOME`, `XDG_CONFIG_HOME`,
`MG_ROOT`, `[agents] autostart = false`, and an awareness that `isEphemeralPath`
prunes `/private/tmp` fixtures. mg-5336, mg-e8e7 and mg-6092 are three separate
tickets for tests that read the developer's live state; the machine's own memory
carries a note about it. Orc's answer is one binary.

*What to build.* `scripts/pogo-sandbox` — `create`, `shell`, `run`, `destroy`,
plus a **`manifest`** saying what was copied, what was neutered, and what was
**refused**. Copy `~/.pogo` and `~/.macguffin`, point every env var at the copy,
force `autostart = false`, and refuse to start if any live path is still
reachable. Shell is enough. `save`/`restore`/`diff`/`timeline` are the expensive
half and can wait.

### 5.5 Mail hardening — **small, mostly in `mg`**

Three of Mailman's features are cheap and each closes a known hole:

- **`mg mail check <id>`** — who has read what you sent. Receipts as one file per
  reader, so two recipients never contend.
- **A known-mailbox check on send.** Sending to an unknown name should refuse and
  name the near miss, not silently create a phantom mailbox and lose the message.
  This fleet has already lost mail this way.
- **`mg mail verify`** — walk the store, report damage, change nothing.

CC-into-a-thread and the query language are nice and not urgent.

### 5.6 Wake-cycle parity — **mostly already ours; adopt two rules**

pogo is ahead here, so this is a two-line change of policy, not a build:

- **Wake each silence once.** An agent that does not move after a nudge is stuck,
  not idle. Report it instead of nudging again; repeated nudges fill its context
  and make the next one less likely to work.
- **Never nudge inside a known limit episode, and always nudge once when it
  clears — regardless of the once-only mark.** `internal/usagelimit` already
  knows the episode boundaries and mg-8d04/mg-55b2 already emit the cleared
  event; the rule is about what consumes them.

### 5.7 Per-minute activity buckets — **medium, deferred**

Extend `mg spend` with a minute-bucketed rollup and a **four-way token split**
(input / output / cache-write / cache-read), advanced incrementally against a
cursor, with a shrunk transcript treated as a rotation that says so. Fold to the
hour after twelve. Worth doing when somebody asks "did that change help" and the
hourly bar cannot answer.

### 5.8 Authority, permissions, roles — **design first, do not build yet**

The largest and least separable piece. The clause algebra (`kind(terms except
terms)` with conservative containment), the floor-as-non-pattern rule, the
`tool(…)` kind for capabilities, and the effective-permissions-are-derived rule
are all worth having **if we want the model at all** — and we do not obviously
want it, because pogo's threat model is different: pogo's agents work in
throwaway worktrees and land through a merge queue that gates everything, which
is a different fence in the same field. §5.1 delivers the piece with standalone
value. Recommend a design note before any code.

### 5.9 Remote mirror (`cq`) — **big, and the design is the valuable part**

If pogo ever grows a remote surface, take the shape wholesale: push-only sync,
the server never reaches back, browser actions **queue**, the UI shows its own
staleness, and every queued action carries the view it was made against so a
stale-snapshot write is refused rather than silently winning. That last rule is
the one that makes an eventually-consistent UI safe, and it is cheap to design in
and expensive to retrofit.

### 5.10 Anno/Dock — **not now, but `§` is free**

The Dock convention costs nothing to start adopting: number headings `§1`,
`§1.1`, keep depth equal to dotted components, and a document becomes
section-addressable later with no migration. Suggest it for new long docs; do not
retrofit.

---

## 6. What could not be established

- **Whether Orc is used by anyone but its author.** No CI, no releases, no
  issues, no external contributors visible. Its docs cite real incidents (seven
  agents stopped at 03:10; a session losing input a second after painting), so it
  is being run — by how many people is unknown.
- **Whether the permission hook actually holds.** The claim is one Claude
  `PreToolUse` hook on one tool layer, and Orc says so itself. Not tested here.
- **`cq`'s web app** was not reviewed beyond its spec (31.9k Go LOC plus a JS
  SPA).
- **Licensing.** No `LICENSE` file was found in the Orc tree. Everything above is
  a description of behaviour and design rationale, which is why §5 is a plan to
  build from a spec and not a plan to copy anything. **If any of §5.2–§5.9 is
  taken further, confirm the licence position first.**

---

## 7. Attribution

Orc, Macmuffin, Mailman, Communiqué, Anno, Dock and Orcprobe are the work of
Redjive2 (`github.com/Redjive2/Orc`). This document describes them in order to
decide what pogo should build for itself. Quoted phrases are Orc's documentation,
quoted as evidence for the design reasoning attributed to it. No Orc source is
reproduced, vendored, or adapted here, and the one implementation shipped
alongside this study (§5.1) was written from the specification in §3.2 against
pogo's own interfaces.
