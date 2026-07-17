# The deaf survivor: an `auto_start=false` agent that gets turned on

**Work item:** mg-738f · **Date:** 2026-07-17 · **Outcome:** fixed

An agent with `auto_start = false` that someone turns ON is a **deaf survivor**:
if its mail loop dies, `diagnose` cannot say so. It runs, answers nothing, and
every health signal stays clean.

This is [mg-de08](#lineage)'s exact pathology — *an agent with no mail loop, with
every health signal green* — in the one population de08's fix could not see,
**because de08's acceptance criterion named the population it covered.**

## The mechanism, at the source

```go
func mailLoopFor(a *Agent, p MailCheckProvider) mailLoopState {
    if !IsExpectedAgent(a.Name) { return mailLoopUnknown }   // BEFORE any mail-check lookup
```

`IsExpectedAgent` is `DesiredStateFor(identity)` — i.e. `auto_start = true`, not
parked. An `auto_start=false` agent is outside the desired state **by
definition**, so `mailLoopFor` returned `mailLoopUnknown` and **could never
return `mailLoopMissing`. Structurally.**

Not a bug in the check. **The check could not reach the question.**

Turn the agent on → its mail loop dies → `diagnose` reports UNKNOWN, not MISSING.
Nothing goes red.

`restart_on_crash = true` does **not** close it: that covers **process** death,
not **mail-loop** death. The agent is alive. It just can't hear.

### Verified: the population is real and shipped

Re-derived at claim time rather than inherited. Two agents ship
`auto_start = false` today:

| Agent | `auto_start` | `restart_on_crash` |
|-------|--------------|--------------------|
| `doctor` | `false` | `false` |
| `pm-lineara` | `false` | `false` |

It is also **exactly the shape Daniel asked for** when he proposed lifting
`architect` to the repo ("NOT on by default") — the requested config is the one
with no mail-loop detection. That is what [mg-abea](#lineage) found while
evaluating the lift, and correctly reported rather than fixed in place.

### Verified: the reap is NOT implicated

`registryLiveness.AgentState` returns `AgentAlive` on **registry evidence**
(`a.Alive() || a.RestartOnCrash`) and **returns there** — it never reaches
`DesiredStateFor` for a running agent (mg-8677's precedence rule). So a turned-on
off-by-default agent **keeps its mail-check schedule**. The schedule is not
reaped; it is the **diagnosis** that was blind. This is not a reap ticket.

## The fix

`mailLoopFor` now delegates the standing question to `mailLoopJudgeable`, which
has two disjuncts:

- **EXPECTED** (mg-de08) — pogod means to run it, so it is owed a loop whatever
  its process is doing. Unchanged; the fix is strictly additive.
- **CONFIGURED AND RUNNING** (mg-738f) — a crew/mayor prompt exists for it
  (`IsConfiguredAgent`, new) **and** its process is alive (`pidAlive`).

The new predicate is deliberately weaker than the old one, and **the gap between
them is the fix**:

```
IsExpectedAgent   — "should this agent be running?"  (auto_start, not parked)
IsConfiguredAgent — "is this agent one of ours?"     (a prompt exists)
```

**This is mg-8677's rule, one consumer over: EVIDENCE BEATS EXPECTATION.** The
reap learned it — `registryLiveness` consults the registry before the desired
state, because a config file cannot overrule a process you looked at. `diagnose`
never did: it asked expectation **first**, and so never looked. "Not in the
desired state" answers *"should this agent be running?"* — the wrong question for
an agent that **is** running.

Liveness is what keeps the RED **conditional** rather than hard-wired to
`auto_start=false`, and it is real evidence, not a status field we set ourselves:

```
not there      -> UNKNOWN   (nothing is owed a loop it has no process to answer with)
there and deaf -> MISSING   (the fault)
```

A detector that cannot tell those apart is the defect this fleet spent
2026-07-17 on.

### The RED, demonstrated before the fix

`TestDiagnose_OffByDefaultAgentTurnedOnWithNoMailLoopIsRed`, run against the
shipped code:

```
--- FAIL: TestDiagnose_OffByDefaultAgentTurnedOnWithNoMailLoopIsRed
    MailCheckMissing = false for a RUNNING auto_start=false agent with no mail-check
    Health = "healthy", want "no_mail_loop"
```

`Health = "healthy"` — for an agent that is alive and cannot hear. That is the
bug, and it was observable before anything was changed.

Three controls, all passing after the fix:

| Control | Setup | Result |
|---------|-------|--------|
| The RED | `auto_start=false`, turned on (live pid), no mail-check | `no_mail_loop` |
| Positive | same agent, mail-check restored | `healthy` — not hard-wired RED |
| Conditional | same agent, **not running** (real reaped pid) | UNKNOWN — "not there" is not a fault |

The polecat exclusion subtest was **given a live pid**. Under the widened set it
would otherwise have passed on liveness and proved nothing about the polecat
exclusion itself — a control filtered to exclude its own counterexample, which is
the very trap this ticket is about.

### Confirmed end-to-end, against the built binary and a real pogod

Staged in an isolated sandbox (`POGO_HOME` + `HOME` + `XDG_CONFIG_HOME` +
`POGO_PORT=10731`, `[agents] autostart = false`) with a `doctor` prompt at
`auto_start = false` — the shipped shape. `pogo agent start doctor` turns it on
by hand; nothing gives it a mail-check:

```
$ pogo agent list
doctor   pid=45372  type=crew  status=running  uptime=0s
$ pogo schedule list --agent crew-doctor
No schedules for crew-doctor.

$ pogo agent diagnose doctor --json | jq '{health, mail_check_missing, status, process_alive}'
{ "health": "no_mail_loop", "mail_check_missing": true,
  "status": "running", "process_alive": true }
```

Positive control — restore the loop, same agent:

```
$ pogo schedule crew-doctor --cron "*/10 * * * *" --id mail-check-doctor ...
$ pogo agent diagnose doctor --json | jq '{health, mail_check_missing}'
{ "health": "healthy", "mail_check_missing": null }
```

Excluded-population control — a **running** polecat with no mail-check must stay
quiet, or the widening leaked into the population that owns its own registration:

```
$ pogo agent spawn catzz --type polecat sleep 600
$ pogo agent diagnose catzz --json | jq '{health, mail_check_missing, process_alive}'
{ "health": "healthy", "mail_check_missing": null, "process_alive": true }
```

The "not running" conditional control is asserted at the unit level rather than
here: `pogo agent stop` **deregisters** the agent, so `diagnose` 404s and the
check would prove nothing. The unit test stages it honestly instead — a real
reaped pid, with the kernel agreeing it answers nothing.

The existing subtest `"agent not in desired state"` **asserted the bug**, with the
rationale *"it was started by hand and may not want one."* That rationale was the
hole: it reasoned from the agent's **config** when the load-bearing fact is its
**process**. It is now `"agent not in desired state and not running"` — narrower,
and true.

## The population this fix excludes — named out loud

**mg-de08's bar has now missed three populations, and every boundary was drawn by
its own acceptance criterion.** de08's bar was *"`diagnose` goes RED for an
**EXPECTED** agent with no mail loop."* The bar was met exactly, three times:

| Population | Covered? | By what |
|------------|----------|---------|
| crew, `auto_start=true` | **yes** | mg-de08 — the population that burned us; the bar was written for it |
| polecats (unregistered) | **no** | mg-61a0 → mg-13a3: the reap concluded death from two absences |
| off-by-default, turned on | **no → yes** | **this ticket** |

> An acceptance criterion that names the population it covers cannot detect a
> failure in the population it excludes — and it will report PASS while the hole
> is open, because **the criterion IS the scope**. A check that CAN fail, on a
> population that excludes the victim, is a positive control that proves nothing
> about the case you care about.

So, explicitly, **my new bar excludes:**

- **Polecats.** Not judged, deliberately. They register their own loop at spawn
  (mg-e633) with their own escalation path on failure (mg-6fe0); one between
  spawn and registration is not a fault. Their coverage is the **witness**
  (mg-13a3), not `diagnose`. **If the witness has a hole, this fix does not see
  it.**
- **A configured agent that is not running.** UNKNOWN by design — the "not there"
  case. If an agent *should* be running and isn't, that is a different fault with
  a different signal (`exited` / `dead`), and this check will not raise it.
- **An agent whose prompt tree is unreadable.** `IsConfiguredAgent` collapses to
  false, so we stay silent. A wrong "no" costs silence; a wrong "yes" costs a
  false RED, and a health signal that cries wolf gets ignored — which is how the
  fleet ends up back where mg-de08 started.
- **An agent nobody runs `diagnose` against.** This is the big one, and it is not
  closed by this ticket. **"Loud" means observable from OUTSIDE the thing that
  failed; a `diagnose` field only helps someone already running `diagnose`.** The
  only consumer of `MailCheckMissing` is `pogo agent diagnose <name>` — an
  operator who already suspects this agent. This fix makes the fault
  **detectable**, not **announced**. That gap is real and it is inherited from
  mg-de08, not introduced here.

A **fourth** recurrence is what the first bullet and the last bullet are for. If
one arrives, it will arrive in one of them.

## Scope

**Split, not included: `stop` does not stop a `restart_on_crash=true` agent.**
`Registry.Stop` early-returns for them; **`park` is what actually stops one.**
mg-abea caught this in the same pass (as a gap in architect's own frontmatter
recommendation), it is what drellem2/pogo#89 reported from the outside, and
**mg-ce26 already shipped the fix** (`4bbf1a1` — pointing `stop` at `park` where
the operator stands). It is a separate defect in a separate subsystem from
`mailLoopFor`'s contract, and it is already addressed; folding it in here would
only blur both.

One consequence worth recording: a **parked** agent is `IsConfiguredAgent` but not
`IsExpectedAgent`, so if park's schedule-pause succeeds and its stop does **not**
— the #89 shape — the survivor is alive, parked, with no mail loop, and this fix
now reports it MISSING. That is a true RED, not a false one.

## Lineage

- **mg-abea** (`bda7310`) — found it; recommended NO on the architect lift
- **mg-de08** (`7a2dc7f`) — the same pathology in the expected set
- **mg-8677** (`d90676c`) — evidence-beats-expectation; why the reap is not implicated
- **mg-13a3** (`8ca4d75`) / **mg-61a0** — the polecat population, covered by the witness
- **mg-ce26** (`4bbf1a1`) + drellem2/pogo#89 — the stop/park half (split)
- **mg-c02d** — the class
