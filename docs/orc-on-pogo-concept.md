# §1 Orc rebuilt on pogo and macguffin — a concept

**Date:** 2026-07-29. **Work item:** mg-4406. **Status:** concept for Daniel to
react to. Nothing here is proposed to anyone else, nothing is forked, no code
ships with it.

This points the previous study the other way. `docs/orc-comparative-study-2026-07-28.md`
asked *"what should pogo take from Orc"*. This asks Daniel's question: **what
does Orc look like rebuilt on top of pogo and macguffin, and is that an
improvement for the person who has to live in it.**

His framing, which is the right altitude and is used throughout:

> "macmuffin becomes a set of bash scripts and we swap out Claude Code calls
> with pogo stuff"

That framing turns out to be **half right, and the half that is wrong is the
interesting half.** §4 is where it gets tested.

---

## §2 The gestalt, up front

The pitch that would be easy to write is "Orc gets pogo's harness layer and its
merge queue, and macmuffin becomes a thin skin over `mg`." Counted against
what is actually in each tree, the trade is narrower and stranger than that:

**Of Orc's seven tools, exactly one is replaced by a macguffin equivalent, and
that swap loses things. One more is half-replaced. The other five are kept
as-is, and for three of them Orc is simply ahead of us.**

By the study's own line counts, ~80k of Orc's ~148k Go LOC — `cq`, `orcprobe`,
`mailman`, `dock`, `anno` — is untouched by this conversion. It is not a
rewrite of Orc on pogo. It is:

- **swap the session-supervision plumbing** (real, hard-won, worth having),
- **swap `muff`'s store for `mg`'s** (lossy, and the losses are load-bearing),
- **add a merge queue Orc has no version of** (the biggest genuine win, and
  it is not one of the two Daniel named),
- **and keep Orc's centre of gravity — the permission model — because we have
  nothing to put in its place.**

That last point is the one to argue with first. Orc is a permission system that
happens to run agents. Converting it to pogo does not convert that half; it
leaves it standing on top of a different session supervisor.

---

## §3 The two value propositions, tested

### §3.1 Better harness support — TRUE, and the strongest part of the case

Orc drives Claude Code directly. pogo has spent a long time being wrong about
exactly this, and the scar tissue is checked in. The concrete claim is not
"pogo is more mature"; it is that **a list of specific incidents Orc documents
are incidents pogo's spawn path already survives.**

The persuasive one is the usage-limit episode, because both systems were bitten
by the same bug and the writeups can be laid side by side:

| | Orc | pogo |
|---|---|---|
| Detection | reads Claude's transcript for the API-error line naming the reset | modal watcher on the PTY (`internal/claude/modal_hook.go`) |
| The failure | limit lands after a tool call, so the feed ends on a `PostToolUse` — which is what *working* looks like | same class: a failure read green for a day (mg-8cdb) |
| Cost | **seven agents stopped at 03:10, still stopped 12h later; nine woken after the limit had lifted** | three flap pairs on 07-22 → six bedtime mails for episodes nobody needed (mg-4904) |
| Fix | wake-mark override on reset; a limit counts only if it is *last* in the transcript | episode coalescing, a 45s hold-down, and `incident_episode_cleared{kind}` on every close (mg-8d04, mg-55b2, mg-4904) |

Orc's incident is the worse one and pogo's machinery would have absorbed it:
the episode coordinator opens on the first limited agent, closes when the last
clears, and emits a clear event carrying **the roster** — every agent limited
during the episode, its work item, and a resume command. That is precisely the
"nine agents nobody woke" failure, handled.

Beyond that one, the things Orc would stop having to own:

- **Nudge readiness.** pogo waits for a structural ready-marker before delivering
  the first nudge, with a set of alternates, because a single exact sentinel
  broke when Claude Code v2.1.x started rendering its footer with per-word
  column moves and the spaces vanished under ANSI-stripping (mg-ce61, mg-d06a).
  Orc found the same class of bug from the other side — a message written to a
  starting Claude is dropped, sometimes only the submitting return.
- **Trust-dialog races**, hardened for late render (mg-ea45).
- **Scheduling that survives host sleep**, with an ack token separating a
  delivered fire from completed work — which exists because 647 deliveries were
  logged during a 23h30m outage where every consuming turn died.
- **Credential expiry warned *before* the outage** (mg-7024), where Orc reads
  limits reactively.

**Honest counterweight:** Orc is genuinely ahead on *how* it detects. Reading
the transcript needs no daemon; pogo's needs pogod alive. And Orc's
"wake each silence once" rule is better policy than anything pogo has written
down. This is a trade, not a rout.

### §3.2 Multi-harness support — TRUE for the spawn path, NOT TRUE for the incident machinery

This is where the pitch has to be qualified, and the qualification is sharp
enough that it should be said before anything else is promised.

**What is genuinely behind the seam.** `agent.Provider`
(`internal/agent/provider.go:27`) is a **13-field data descriptor**, not an
interface — and that is good news for the "not a rewrite" claim, because a new
harness is *filled in*, not *implemented*. All four registered providers
(`claude`, `codex`, `pi`, `cursor` — `internal/providers/providers.go:26`)
populate the same ten fields: spawn command template, prompt injection strategy,
non-interactive flags, PTY nudge dialect and readiness sentinels, trust-dialog
hook, memory-index globs, transcript globs. Cursor additionally needed flag
aliases and argv-delivered prompts; pi needed argv delivery because a
differential-render TUI never goes idle (gh #26). **Both were absorbed by adding
a field, not by branching the spawn path.** That is the concrete answer to "what
stops being a rewrite": everything in the list above.

**What is NOT behind the seam, measured.** The study cites `internal/usagelimit`
twice. **That package does not exist.** The usage-limit episode coordinator is
`internal/claude/usagelimit.go` — 377 lines, in package `claude` — and it hangs
off `ModalHook`, which is Claude's `SessionHook`. **`SessionHook` is non-nil for
1 of 4 providers**; Codex, pi and Cursor all set it to `nil`
(`internal/codex/provider.go:109`, `internal/pi/provider.go:127`,
`internal/cursor/provider.go:188`).

So: **the flagship harness win covers exactly one harness — the same one Orc
already runs.** Swap the provider today and you keep the spawn path and lose the
incident machinery.

And the denominator moves the wrong way: `internal/providers/providers.go:2`
already names Gemini as a future provider. The numerator does not grow on its
own. **Every harness added makes the 1-of-4 worse until the detection is lifted
behind the seam.**

This is not a reason to abandon the multi-harness pitch. It is a reason to state
it accurately: *pogo's multi-harness support is real for launching, prompting,
nudging and trusting an agent, and is not yet real for noticing that one is
stuck.* Lifting usage-limit detection behind `Provider` is the work that makes
the second sentence go away, and it is the single highest-value thing on this
whole page.

---

## §4 The worked example: macmuffin on macguffin

Daniel's framing: macmuffin becomes bash scripts over `mg`. Tested honestly:

**The verbs port. The store guarantees do not.**

`muff`'s command surface maps onto `mg`'s 24 commands without much strain, and
the one piece of `muff` that pogo has already reimplemented — enforced scope —
**is literally a bash script over `mg`** (`scripts/mg-scope-guard.sh`, shipped by
mg-f1d5). So the instinct is validated where it is testable.

What cannot be a bash script over `mg` is everything that is a property of the
*store*:

### §4.1 What is lost in the swap

| Lost | Why bash-over-`mg` cannot supply it |
|---|---|
| **Enforced file scope** | Partially answered — see §4.2. |
| **Per-task advisory locks** | `mg` claims by atomic rename between status directories; the rename *is* the claim. That makes agents race for the pool rather than for a task. A lock file bolted on beside it would not be the same fact. |
| **A store version** | `~/.macguffin/` has `agents events.jsonl log mail spend work` and no version file. `muff` hard-errors on an unknown version; `mg` cannot tell an old store from a new one, so a format change is a silent misread rather than a refusal. |
| **Bounds** | MaxTasks 4096, MaxSubtasks 256, MaxScopeEntries 512, 32 KiB descriptions. `mg` has none. Unbounded is fine until it isn't. |
| **Draft-until-push privacy** | A task you cannot see is *not found*, not forbidden — and the operator is not an exception. This needs an identity the store believes; `mg`'s `--from=` is typed by the caller. |
| **Subtasks and collaborators** | Additive to `mg`, but genuinely absent today. |
| **Two-axis scoring + a health status separate from lifecycle** | `mg` has one priority and conflates health with status. This one is cheap to add and worth adding regardless. |
| **Queued-never-lost notifications** | `muff` queues an undeliverable notice to an outbox and never fails a change that already happened. `mg mail` has 6 subcommands (`archive list migrate read reply send`) and no such path — and this fleet has already lost mail to a phantom mailbox created by a typo. |

### §4.2 Exactly how partial the scope answer is

The ticket asks for this precisely, so: `scripts/mg-scope-guard.sh` is a real
fence with four measured limits.

1. **It is wired into 0 of 6 polecat templates.** Counted against
   `~/.pogo/agents/templates/` — the stationary population — nothing opts in.
   Nothing outside its own doc, changelog and tests references it at all.
   *(Against the non-stationary population of 473 `.md` files under
   `~/.pogo/agents/`, which grows with every dispatch: also 0.)*
2. **Bash writes are out of reach**, stated on the tin (lines 21–24). A hook
   sees a command line, not what the command will touch. It stops the accident,
   not the determined agent — the same caveat `muff` states.
3. **It covers 4 tool names** — `Edit`, `Write`, `MultiEdit`, `NotebookEdit`.
4. **It is Claude-shaped.** It speaks `PreToolUse` JSON and exits 2 to block —
   Claude Code's hook contract. **So pogo's one enforcement mechanism is also
   1-of-4 on providers.** Same defect as §3.2, different subsystem.

What it *does* have, and `muff` has too: the check-scope mode exits 9 for out of
scope and 11 for an escape, so a containment failure is never filed beside a
typo. And it refuses when it cannot decide, once opted in — a silently
unenforced agent is invisible and a loud refusal is not.

**Verdict: enforced scope is roughly a third of the way there.** The decision
logic exists and is good; the wiring, the Bash half, and the provider generality
do not.

### §4.3 Where macmuffin is simply better, plainly

Not a sales pitch. These are places `muff`'s design beats `mg`'s and would be
a downgrade to give up:

- **Locking per task, not per pool.** Stated as a design intent — "two agents
  race for *a* task, not for the pool" — and `mg`'s rename cannot express it.
- **A versioned store.** Unknown version is a hard error. This is the difference
  between a bad upgrade that stops and one that corrupts.
- **Not-found instead of forbidden**, applied consistently to unseeable tasks
  and out-of-subtree identities. Saying "you may not" confirms it exists.
- **Notification as announcement, never as the fact.** A change that happened
  never fails because the mail failed.
- **The journal records that a description changed and who changed it, never the
  text.** A record folded on every command must not carry 32 KiB of prose.
- **Shipping a refusal instead of a guess.** `muff assign` is specified and
  exits 1, because assignment needs an agent-control contract that does not
  exist yet and inventing one would mean rewriting it later. `mg assign` exists
  and works — but the *discipline* is the better instinct and we should steal
  it, not the code.

And `mailman` beats `mg mail` outright: conversations, CC-into-a-thread, read
receipts, and a query language, against our six subcommands. **Mailman's
`verify` and `admin` view would both have caught the phantom-mailbox bug this
fleet actually hit.**

### §4.4 What would have to be built into macguffin first

For the swap to be non-lossy, in rough order of (value ÷ cost):

1. **A store version file** and a hard error on unknown. Hours.
2. **A known-mailbox check on send** that names the near miss instead of
   creating a phantom. Hours, and it closes a hole we have already fallen in.
3. **Health status separate from lifecycle**, and a second scoring axis.
4. **Wire the scope guard into the polecat templates**, and lift it off
   Claude's hook contract so it is not 1-of-4.
5. **Per-task locks**, or an explicit decision that pool-racing is what we want.
6. **Draft privacy and collaborators** — which need an identity the store
   believes, and therefore are downstream of §5's honest answer about authority.

Items 1–3 are worth doing whether or not anyone ever converts Orc.

---

## §5 The seven tools — one-line verdicts

| Tool | LOC | Verdict |
|---|---:|---|
| **orc** | 43,678 | **Half replaced.** Its session-supervision half → pogod, and that is a real upgrade (§3.1). Its identity/role/permission/authority half → **kept as-is; pogo has nothing to put here**, and it is Orc's centre of gravity. |
| **cq** | 31,911 | **Kept as-is.** pogo has no remote surface whatsoever. Genuinely out of scope for this conversion. |
| **muff** | 17,603 | **Replaced by `mg`, lossily.** The worked example. Non-lossy only after §4.4. |
| **orcprobe** | 14,986 | **Kept as-is — Orc is ahead.** pogo's equivalent is env discipline every test author must get right by hand, and this fleet got it wrong three times (mg-5336, mg-e8e7, mg-6092). Do not swap a working sandbox for a checklist. |
| **mailman** | 14,216 | **Kept as-is — Orc is ahead** (§4.3). Replacing it with `mg mail` is a downgrade today. |
| **dock** | 10,521 | **Out of scope.** No pogo counterpart. `pose` finds *which file*; Dock decides *how much of it to spend*. Complements, not competitors. |
| **anno** | 8,575 | **Out of scope.** Same. |

**Read the column, not the rows:** one replacement, one half-replacement, five
kept — three of those five because Orc is better. Anyone selling this as "Orc
rebuilt on pogo" is selling a plumbing swap.

**And the thing not in the table:** pogo's refinery — submit, gate, merge,
`already_merged` detection, `lost` recovery across a restart, PR-flow
integration branches. Orc has no notion of landing work; `muff worktree` binds a
task to a worktree and stops. This is the largest single thing pogo has that Orc
does not, it is what turns an agent into a contributor, and **it is the best
argument on this page even though it is not one of the two Daniel named.**

---

## §6 Migration shape, and the smallest first step

### §6.1 The smallest first step

**Run one Orc agent under pogod, on Orc's own repo, and let it land a change
through the refinery. Change nothing else.**

Keep `muff`. Keep `mailman`. Keep the permission model. Keep `orcprobe`. The
only substitution is the process supervisor and the merge path.

This is the right first step because it tests both value propositions at once
and commits to neither:

- It exercises the spawn path, the nudge readiness gate, the trust-dialog hook,
  and the episode machinery — value prop §3.1, on real work.
- It demonstrates the refinery, which is the thing Orc has no version of, so
  the result is visibly *new capability* rather than *a different way to do what
  already works*.
- It is reversible in an afternoon. Nothing in Orc's store is migrated.
- If it is unpleasant, the answer is "no" and nobody has lost a week.

The honest caveat to state alongside it: **the permission hook and the scope
fence do not compose yet.** Orc's `PreToolUse` hook and pogo's would both want
that slot. Whoever tries this should expect to pick one for the trial.

### §6.2 If that goes well, the order

1. Session supervision + refinery (above).
2. `mg` gains the §4.4 items 1–3 — cheap, independently useful.
3. Lift usage-limit detection behind `Provider` so multi-harness means something
   for incidents, not just for spawning.
4. Only then consider `muff` → `mg`, and only if §4.4 is done.

`cq`, `orcprobe`, `mailman`, `dock`, `anno` are never on this list.

### §6.3 What cannot be carried across at all

- **The authority/permission/role model.** pogo's every agent runs
  `--dangerously-skip-permissions` by documented intent
  (`docs/polecat-permissions.md`), and its threat model is different: throwaway
  worktrees behind a merge queue that gates everything. That is a different
  fence in the same field, and it is not a substitute for an enforced
  permission clause. Nothing on pogo's side receives this.
- **Per-identity credentials.** Orc mints a key per identity and every tool
  authenticates against it. pogo's identity is a process name and a `--from=`
  flag the agent types itself; any agent can send as any name.
- **`cq`'s remote mirror**, which has no counterpart to migrate to.
- **Anno and Dock's addressing**, which is a different idea from `pose` rather
  than a worse one.

---

## §7 What this concept rests on, and what it does not

**Checked directly in the pogo tree:** the `agent.Provider` field list and its
per-provider population; `SessionHook` nil in 3 of 4 providers; the absence of
an `internal/usagelimit` package and the 377-line coordinator in
`internal/claude`; the scope guard's tool list, exit codes, Claude hook shape,
and its 0-of-6 template wiring; `mg`'s 24 commands and 6 mail subcommands; the
macguffin store layout and its missing version file.

**Taken from the prior study, not re-verified here:** everything about Orc.
This concept did not read the Orc tree at all. Every LOC figure, every incident,
and every `muff`/`mailman` behaviour above is the study's observation. If the
study is wrong about Orc, this document is wrong in the same places.

**Not established:** whether Orc's permission hook actually holds (Orc says
itself it is one hook on one tool layer); whether anyone but its author runs
Orc; and whether the two `PreToolUse` hooks in §6.1 can be composed rather than
chosen between. That last one is the first thing a trial would find out.
