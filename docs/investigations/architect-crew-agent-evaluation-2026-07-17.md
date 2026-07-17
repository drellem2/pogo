# Should the `architect` role ship in the pogo repo as a crew agent?

**Work item:** mg-abea · **Date:** 2026-07-17 · **Status:** evaluation only — nothing lifted

Daniel, 2026-07-17, three mails: *"Keep architect"* / *"evaluate lifting it to the pogo repo
as a default crew agent"* / *"NOT on by default"*. This is the evaluation. It is a
recommendation to Daniel and pm-pogo, not an application. The standing `architect` on this
host is unaffected either way, and mg-b0cc (the cancelled cutover) stays shelved.

## Ruling

**NO — do not lift `architect.md` into the repo as a crew agent in this form.** Ship
**mg-945c's `polecat-architect` template** instead, which is already drafted and already
host-agnostic.

This is not "architect is bad." The standing architect on this host is demonstrably
valuable and Daniel is right to keep it. The finding is narrower and it is about **what
fits in a box**:

> The half of architect's value that works on day one is **review-shaped, and the pogo repo
> already ships it** (`polecat-review.md`, architecture lens). The half that is
> distinctive — noticing that a question exists — is, by architect's own audit and mine,
> **substantially accumulated**, and accumulation is exactly what a fresh install does not
> have. What would ship is a role statement plus scaffolding: the seed, priced like the tree.

A `crew/architect.md` at `auto_start = false` would land inert in every install, with no
surface a user would find it through (§Q5), duplicating a lens that already ships, and — per
architect's own testimony — carrying *"authority without evidence"* if a stranger did turn
it on.

And the requested shape has a defect in it. **`auto_start = false` removes an agent from
pogod's desired state, and `diagnose`'s mail-loop check only judges agents in it** — so a
turned-on architect whose mail loop dies is reported `mailLoopUnknown`, never missing
(§Q2). `restart_on_crash = true` does not close that: it covers process death, not the deaf
survivor. Daniel's *"NOT on by default"* and architect's *"silence is the failure mode"* are
in mechanical tension, and `IsExpectedAgent` is where they collide. That is fixable — but it
is platform work that this ticket's evidence does not justify starting.

**This ruling can fail.** See §Falsifier: one concrete day-one catch, on a host with no
fleet history, that `polecat-review` would not make, and the answer flips to yes.

## Conflict disclosure

Architect is both the **subject** of this evaluation and the **author** of mg-945c, the
alternative this report recommends shipping. That is a real conflict and naming it does not
cure it. Their input reached me via mayor's relay and I treated it as input, not ruling.

Two things are worth recording honestly:

- **Architect answered Q3 against their own interest**, unprompted: *"Substantially
  accumulated… Whoever installs this gets the seed, not the tree, and should be told so."*
  and *"A fresh architect has nothing BUT priors. It will be fluent, confident, and unable
  to check itself."* That testimony is against interest, and it is load-bearing here. I did
  not take it on faith — I checked it against their own audit and against the shipped
  templates, and it holds.
- **My Q4 conclusion (945c survives) agrees with architect's interest**, so I am explicit
  about why I reach it independently: I read both files. `mg-945c-polecat-architect-template-draft.md`
  is already templated (`{{.Coordinator}}`, `{{.Worker}}`) and carries no fleet history;
  `~/.pogo/agents/crew/architect.md` is scaffolding wrapped around one sentence, plus a
  mg-de08 incident narrative. One is shippable today; the other is not. Architect's flag —
  *"if it concludes the standing agent makes 945c redundant, that conclusion should stick
  even though I disagree"* — did not have to be exercised.

I also found **one thing architect's own recommendation missed**, which is the best evidence
their technical read is checkable rather than authoritative: see §Q2 (`stop` does not stop a
`restart_on_crash = true` agent).

## Q1 — What would actually ship?

Verified: `internal/agent/prompt.go` embeds the prompt tree (`//go:embed prompts`) and
`InstallPrompts` walks it with `fs.WalkDir`, copying every file into `PromptDir()`. There is
no per-file allowlist — **adding `prompts/crew/architect.md` ships it to every install**,
unconditionally.

Read against `~/.pogo/agents/crew/architect.md`, the live file is ~90 lines, of which the
role is one paragraph (*"Help to maintain codebases that are in line with architecture,
vision, and quality standards"*). The rest is this-fleet material: an mg-de08 incident
narrative ("what silently killed four PMs' mail loops for ~6h on 2026-07-17"), a
`~/.pogo/schedules.json` path, a `pogo-crew-architect` process name, and a "Don't dispatch
polecats — that is mayor's job" section that presumes our coordinator topology.

Architect's own framing of the genericized role is accurate and I adopt it:

> *"A reactive architect answers questions; a standing one notices that a question exists."*

That sentence is host-agnostic and it is the whole role. **The problem is that it is also
the whole ship.** Strip the incidents, the IDs, and our topology, and what remains is a role
statement — not a capability. Architect's corollary is the important half and it is correct:
*"noticing that a question exists requires reading everything, so the context burn IS the
mechanism, not a side effect."* You cannot ship the value without shipping the cost, and the
cost is continuous context burn plus ~15 self-reported errors in one night.

## Q2 — Frontmatter: the precedent, and where architect's read breaks

**Doctor's precedent, verified** (`internal/agent/prompts/crew/doctor.md`) — still true at
claim time:

```toml
auto_start = false
restart_on_crash = false
```

`docs/customizing.md` documents this pair deliberately, under "Opt out of auto-start", with
a rationale: `restart_on_crash = false` *"lets you stop it on demand without pogod
auto-respawning it."*

Architect recommended **not** copying the pair — `auto_start = false` +
`restart_on_crash = true` — and their reasoning is genuinely strong:

> *"doctor is a tool you invoke; the architect's entire value is continuous presence. An
> architect that crashes and doesn't restart goes SILENT, and silence from an architect is
> indistinguishable from 'no architectural concerns.'"*

That is this fleet's exact defect — a sensor that reports healthy by not reporting — and
they are right that `false`/`false` would ship it in the config of the agent that spent the
night finding it.

**But their recommendation has a consequence they did not mention, and it is checkable.**
`Registry.Stop` (`internal/agent/agent.go`) branches on
`if agent.RestartOnCrash && !IsParked(name)` and returns early — its own comment says the
supervisor *"restarts the agent on any exit (clean or crash, **including explicit Stop**)"*.
So with `restart_on_crash = true`:

> **`pogo agent stop architect` does not stop architect.** It respawns. The user must know
> about `pogo agent park architect` (`internal/agent/park.go`, `IsParked`).

Doctor's `false`/`false` is not a blind pair — it is what makes `stop` mean stop for an
agent you invoke and dismiss. Architect's argument survives this (silence really is the
worse failure), but the shape they recommend hands a stranger an agent that **ignores the
obvious off switch**. Had this shipped on their recommendation alone, that surprise ships
with it.

**Ruling on Q2 (contingent — only applies if the ship happens over this report's NO):**
architect's `auto_start = false` + `restart_on_crash = true` is the right shape, *and* it is
incomplete: it cannot ship without `docs/customizing.md` teaching `park` as architect's off
switch in the same breath. `false`/`false` is wrong for a presence agent. The pair is not
copyable in either direction — which is what the ticket suspected.

### The gap `auto_start = false` creates — and `restart_on_crash = true` does not close

This is the finding neither architect nor the ticket raised, and it cuts at architect's own
Q2 argument with architect's own evidence. Verified in code:

`ExpectedAgents()` (`internal/agent/autostart.go`) includes an agent only when
`expectedStatus` is empty, and `expectedStatus` returns `AutoStartStatusSkippedNoFlag` the
moment `!meta.AutoStart`. So **`auto_start = false` puts an agent outside pogod's desired
state — permanently, even while it is running.** `DesiredStateFor`'s own doc: *"(true, nil)
— in the desired state: an auto_start, not-parked prompt."*

Now the consumer, `mailLoopFor` (`internal/agent/api.go`):

```go
if !IsExpectedAgent(a.Name) {
    return mailLoopUnknown
}
```

Its doc says diagnose *"flags agents IN [the desired state] with no mail-check."* An
`auto_start = false` agent is not in it. So:

> **If a manually-started architect's mail-check loop dies, `diagnose` reports
> `mailLoopUnknown` — never `mailLoopMissing`. Nothing flags it.**

Architect's own prompt documents this exact pathology at length — mg-de08, *"what silently
killed four PMs' mail loops for ~6h on 2026-07-17"* — and states the consequence itself:

> *"This is your only unprompted wake-up. If it isn't registered, you are unreachable: mail
> from mayor and the PMs sits unread indefinitely and **nothing will tell you**."*

`restart_on_crash = true` does **not** close this. It covers *process* death. This is
*mail-loop* death, which leaves the process alive, `diagnose` quiet, and the agent deaf —
running, healthy-looking, and silent. That is precisely architect's stated failure mode
(*"silence from an architect is indistinguishable from 'no architectural concerns'"*),
arriving through the door they didn't check.

**Scoped honestly** — I checked the reap too, and it is *not* implicated: `registryLiveness.AgentState`
(`cmd/pogod/main.go`) returns `AgentAlive` for any registered, alive agent before it ever
consults desired state (`d90676c`: *"Consult desired state ONLY when the registry yields NO
evidence"*). A running architect's mail-check is **not** reaped. The gap is diagnose's
mail-loop check alone, and it is real.

**Why doctor is exempt and architect is not:** doctor's `auto_start = false` is harmless
because doctor is a tool you invoke and dismiss inside a session — a dead mail loop is
irrelevant to it. Architect's entire claimed value is **continuous presence over weeks**.
`auto_start = false` is exactly what removes long-running agents from the fleet's own
liveness watch. **Daniel's "NOT on by default" and architect's "silence is the failure
mode" are in direct mechanical tension, and `IsExpectedAgent` is where they collide.**

This is also, once again, this fleet's signature defect — *a sensor that reports healthy by
not reporting* — and it would ship in the config of the agent that spent the night finding
it. Not because anyone was careless: because `auto_start` is doing double duty as both "boot
this?" and "watch this?", and the ask requires those two answers to differ.

Noted and verified, not inherited: `auto_start = true` + `restart_on_crash = false` is no
longer the mg-8677 trap — `d90676c` ("consult desired state ONLY when the registry yields
no evidence") is real and on main. It is still the wrong shape, and it is not the shape
under consideration.

Also verified and worth recording: the **live** `~/.pogo/agents/crew/architect.md` runs
`auto_start = true`, `restart_on_crash = true`. The ticket body said the live architect runs
`restart_on_crash = true` — true — but the live `auto_start` is **`true`**, not false. The
thing running on this host is not the thing proposed for shipping.

## Q3 — Is a day-one architect useful? (the load-bearing question)

**No — not in a way that isn't already shipped.** This is where I add evidence rather than
weigh testimony.

Architect audited their own night's catches. Their **day-one** list, verbatim:

| Catch | Architect's own words |
|---|---|
| `do_build` runs no tests | *"a grep"* |
| the comment-trap generalisation | *"that came from MAYOR's grep failing; I just read the mail and thought"* |
| trap-not-enumeration | *"standard practice; **a competent reviewer gets that cold**"* |

Now read `internal/agent/prompts/templates/polecat-review.md`, which **already ships**:

> *"you review it through three lenses — QA, **architecture**, design-faithfulness"*
> …
> *"2. **Architecture — fits the codebase it lands in.**"*

**Architect's description of its own day-one value is a description of `polecat-review`.**
Two of the three are explicitly reviewer-shaped by architect's own words. The third
(comment-trap) required *a fleet incident mail to read* — which a fresh install does not
have either. So each day-one catch is either (a) already covered by a shipped template, or
(b) still dependent on an operating fleet.

Their **needs-accumulation** list — *"loud is undefined in two tickets"* (*"only visible if
you read both"*), *"AgentUnknown has no resolver"* (*"asking the question came from having
watched de08 → 8677 → 61a0 → 13a3"*), every-decaying-guard-is-a-proxy, f206-arms-on-an-observation
— is the distinctive half. All four are the standing architect's actual product, and all
four are unavailable at install.

That is the whole finding: **the shippable half is already shipped, and the unshipped half
isn't shippable.** A `crew/architect.md` in the box adds a standing, off-by-default
duplicate of a lens the repo already has, whose distinctive value arrives in ~30 days *if*
the user finds it and turns it on.

And it is not merely neutral in the interim. Architect, against themselves:

> *"it's differently risky. **It has authority without evidence.** … the ones I got wrong
> were exactly the ones I ruled from priors instead of looking… A fresh architect has
> nothing BUT priors. It will be fluent, confident, and unable to check itself — and
> **fluency is what makes that failure mode survive review**."*

There is also a confound in the origin of this ask that the evaluation should name plainly.
What changed Daniel's mind was watching a **90-day-accumulated** architect be proactive
(mg-c02d's ledger). That evidence is real — and it is evidence for **the tree, not the
seed**. The box ships the seed. Architect said it best and it is the sentence this ruling
turns on: **"don't let the box promise the tree."** The most direct way to honor that is to
not put the box on the shelf yet.

Finally, the evidence base is **n = 1**, and the n is **the author**, on the **one host with
the history**. That is not enough to make a default for strangers.

## Q4 — What happens to mg-945c?

**It survives, and it is the better ship.** mg-945c is `archived` (verified via `mg show`),
but its artifacts are intact:

- `~/.pogo/agents/architect/mg-945c-polecat-architect-template-draft.md`
- `~/.pogo/agents/architect/mg-945c-mayor-diff-spec.md`

Not superseded, and not redundant with `polecat-review`. I read the draft: it scopes four
shapes — **A** design memo, **B** alignment check, **C** design-correctness review gate, **D**
design artifact. `polecat-review` only covers PR-triggered review (roughly C). **A, B, and D
have no shipped home today.** That is a real gap in the repo, and 945c already fills it, in
a file that is already generic.

Architect's framing holds: *"Reactive is cheap and answers what you ask; standing is
expensive and notices what you didn't. Different products for different appetites."* The
reactive one is the one that fits in a box.

**Recommended:** unshelve mg-945c's template as the deliverable for Daniel's ask. It is the
honest answer to *"lift architect to the pogo repo"* — it ships the architecture *function*
to strangers, generically, today, without promising the tree.

Note: mg-945c's original Daniel directive (2026-07-09) was *"architect role is stupid. It's
not a pogo builtin."* He has since reversed on **keeping** the standing architect, which is
settled and not in question here. But the reversal was about *this host's* architect; the
"is it a builtin?" question is the one this ticket asks, and it is still open on the
evidence. I record both without pretending the reversal resolved it.

## Q5 — Interaction with `pogo init`: nothing would point at it

Verified: `InstallPrompts` walks the whole embed tree, so a shipped `crew/architect.md`
lands in `~/.pogo/agents/crew/` on **every non-`--minimal` install**. `docs/customizing.md`
confirms the roster model: *"the prompt files in `~/.pogo/agents/` **are** the agent
roster… adding a file under `crew/` makes a new crew agent exist."* There is no separate
registry to update — and no separate registry to be *listed in*.

**This is the asymmetry with doctor, and it is decisive for the "cost is ~zero if it's off"
argument.** Doctor is off by default too — but doctor has a **discovery surface**:

- `pogo doctor` is a top-level command (`cmd/pogo/main.go`, `cmdDoctor`) that starts it,
  and its help text advertises it: *"Start the doctor agent for interactive debugging."*
- It prints `Use 'pogo nudge doctor <message>' to ask questions.`
- `docs/customizing.md` names it as *the* worked example of on-demand frontmatter.

A shipped architect would have **none of these**. Being precise about the one surface that
*would* show it, since overclaiming here would be the same sin this report is auditing:
**`pogo agent prompt list`** (`cmdAgentPromptList`, *"List available prompt files"*) prints
`category  name  path` from `ListPrompts()` — so an installed architect would appear there.
But it prints **no `auto_start` state and no description**: it reads as a file listing, not
an agent catalog, and a user has to already suspect the agent exists to go looking. `pogo
agent list` would **not** show it — that reads the registry plus parked entries, never
`ListPrompts` — so an installed, never-started architect is invisible to the command whose
name promises to list agents. Confirmed there is **no "available agents" command**, and
`ExpectedAgents()` / `DesiredStateFor` are internal-only, exposed by no CLI.

So the "shipping it is free if it's off" argument does not hold on its own terms: **either
it ships inert behind a command nobody runs to find agents — which is not meaningfully
different from not shipping it, minus the maintenance and the implied endorsement — or it
ships with a `pogo architect` command and doc entries, which is a real platform surface that
should be earned by evidence this report cannot find.**

If it ships, these enumerate the shipped set and would need updating — verified, and a
useful list for whoever executes an override: `docs/CONFIGURATION.md` (§ Prompt templates),
`docs/customizing.md` (roster intro + § Opt out of auto-start), `docs/prompt-customization.md`
(§ Directory layout), `docs/design/prompt-customization-design.md`, and `ARCHITECTURE.md`
(§ Prompt files / § Crew Agent).

## Falsifier — what would flip this to yes

This ruling rests on one claim, and the claim is refutable:

> **Every day-one architect catch is either already `polecat-review`'s job, or still needs a
> fleet to read.**

Name **one** concrete catch a fresh standing architect makes on day one, on a host with no
fleet history and no accumulated mail, that `polecat-review`'s architecture lens would not
make. One example refutes this and the answer flips.

I could not construct one, and — notably — **architect could not either**: asked to list
day-one value, they produced three items, and described two of them in `polecat-review`'s
own words.

**Revisit when:** a standing architect has run on a host that is not this one, and produced
a catch matching the falsifier. That is the evidence that would make this a default for
strangers. It does not exist yet, and it cannot be produced by argument.

## Recommendations

1. **Do not lift `crew/architect.md`** into `internal/agent/prompts/crew/`. (Q1, Q3, Q5)
2. **Unshelve mg-945c's `polecat-architect` template** as the shippable form of Daniel's
   ask — already generic, already drafted, fills shapes A/B/D that nothing ships today. (Q4)
3. **Keep the standing architect on this host, unchanged.** Daniel's call, well-supported,
   not in question. mg-b0cc stays shelved. (Q3)
4. **File the diagnose gap as its own ticket regardless of this ruling.** It is not
   architect-specific: **any** `auto_start = false` crew agent a user turns on and leaves
   running is outside `diagnose`'s mail-loop check (§Q2). Today only `doctor` ships that way
   and it is exempt in practice, so this is latent rather than live — which is exactly when
   it is cheap to fix. `auto_start` is answering two questions ("boot this?" and "watch
   this?") that this ask needs to answer differently. Out of scope here; noted, not fixed.
5. **If Daniel overrides this and ships it anyway** — his call, and the ask was explicit —
   then all four of these, not a subset:
   - `auto_start = false` + `restart_on_crash = true` (§Q2), **with `park` documented as its
     off switch** in the same breath, or `stop` will surprise every user who tries it.
   - **Recommendation 4 becomes a blocker, not a follow-up.** Shipping a presence agent that
     the fleet's own liveness check declines to watch is the defect this fleet spent
     2026-07-17 finding.
   - **A discovery surface** — a command or a doc entry. Inert-and-unfound is not a ship.
   - **The prompt must say in its own text that a fresh instance's first job is noticing,
     not ruling** — architect's own mitigation, and the right one:

     > *"Its early output should be 'here is a question nobody asked', not 'here is the
     > answer.' That's the half that works from zero."*

## Evidence

Re-derived at claim time per the ticket's bar; symbols and files, no line numbers.

| Claim | Verified against |
|---|---|
| Prompts ship by embed; no allowlist | `internal/agent/prompt.go` — `//go:embed prompts`, `InstallPrompts`, `fs.WalkDir` |
| doctor ships `false`/`false` | `internal/agent/prompts/crew/doctor.md` frontmatter |
| doctor's pair is deliberate, documented | `docs/customizing.md` — "Opt out of auto-start" |
| `stop` ≠ stop when `restart_on_crash=true` | `internal/agent/agent.go` — `Registry.Stop`, `IsParked`; `internal/agent/park.go` |
| `auto_start=false` ⇒ outside desired state | `internal/agent/autostart.go` — `ExpectedAgents`, `expectedStatus` (`AutoStartStatusSkippedNoFlag`), `DesiredStateFor`, `IsExpectedAgent` |
| diagnose can't flag its dead mail loop | `internal/agent/api.go` — `mailLoopFor`: `if !IsExpectedAgent(a.Name) { return mailLoopUnknown }` |
| …but the reap does NOT touch it (scoped) | `cmd/pogod/main.go` — `registryLiveness.AgentState` returns `AgentAlive` on registry evidence before consulting desired state (`d90676c`) |
| No "available agents" command; `agent list` reads the registry, not prompts | `cmd/pogo/main.go` — `cmdAgentPromptList`; `internal/agent/api.go` — `handleList`; `internal/agent/prompt.go` — `ListPrompts` |
| Architecture review already ships | `internal/agent/prompts/templates/polecat-review.md` — three lenses, "Architecture" |
| Roster is the prompt dir; no registry | `docs/customizing.md` — "The agent roster" |
| doctor has a discovery surface; architect wouldn't | `cmd/pogo/main.go` — `cmdDoctor`, `--check`, help text |
| mg-8677 trap fixed | `d90676c` on main — "consult desired state ONLY when the registry yields no evidence" |
| mg-945c archived, artifacts intact | `mg show mg-945c`; `~/.pogo/agents/architect/mg-945c-*.md` |
| Live architect is `auto_start=true` | `~/.pogo/agents/crew/architect.md` frontmatter |

**Xref:** mg-b0cc (shelved cutover), mg-945c (the template — recommended), mg-8677/`d90676c`,
mg-c02d (the evidence ledger).
