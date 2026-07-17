# What resolves an `AgentUnknown`? (mg-0b77)

**Date:** 2026-07-17 · **Work item:** mg-0b77 · **Status:** answered + fixed

Xref: mg-13a3 (`8ca4d75`, the witness that created UNKNOWN's permanence),
mg-61a0 (`05eef74`, why absence never heals), mg-46a4 (`fbdcae1`, the drain
reads the registry), mg-8677, mg-de08, mg-76e5, mg-f206.

---

## 1. The question

mg-13a3 landed and is right: **don't reap on absence.** A registry-absent
polecat whose persisted `(pid, start_time)` still matches a live process now
resolves `AgentUnknown`, not `AgentGone`. That stopped the lie. **It did not
deal with the survivor.**

`AgentUnknown` was designed as a **transient** — its own comment says "we have
no evidence either way", which is a *yet*. For crew it resolves: the agent
boots, registers, becomes `AgentAlive`. **For a polecat that outlived a pogod
restart it never resolves**, because the registry is in-memory with no adopt
path: absence never heals.

So the survivor sits in UNKNOWN forever: **alive, unreachable, holding a
worktree and a claim, mail-check firing into a void.** Not a regression, and not
a criticism of 13a3 — the next question.

## 2. Re-derived, not inherited

The ticket body's mechanisms were re-checked at claim time rather than trusted.
Result: **the Go line numbers held; the shell one had already drifted.**

| Claim (from the ticket body) | Verdict at claim time |
|---|---|
| `AgentUnknown` produced at `main.go` ~`:200/212/229` | **Holds** — three arms of `registryLiveness.AgentState` |
| Consumed nowhere else | **Holds** — `grep -rn AgentUnknown` finds only the producer, `scheduler.go:690`, and tests |
| `scheduler.go:690` — only GONE reaps | **Holds** — `return live.AgentState(agent) == AgentGone` |
| `drain_wait` polls in-memory `PolecatCount()` | **Holds** — `agent.go:475-485` iterates `r.agents` |
| `"drain complete — 0 polecats active"` at `:538` | **DRIFTED** — actually `scripts/pogo-self-deploy:629` |

The drifted one is the ticket's own warning landing on the ticket: *a line
number is the purest world-state claim — maximally precise, invalidated by any
edit above it.* Cite the symbol.

One further check that mattered: `report_drain_complete`'s first draft called
`coordinator_name`, **a helper that does not exist in that script** — it would
have been a runtime error on the exact path being fixed. Caught by grepping the
script rather than assuming the helper existed because the Go side has
`CoordinatorName()`.

## 3. The answer: **a human resolves an `AgentUnknown`.**

The machine cannot. Both mechanical candidates are wrong, and not narrowly:

### Adopt is impossible — and would relocate the lie

A polecat is driven through a PTY whose **master fd lived in the address space
of the pogod that died**. The master closed with it; the slave hung up. No
syscall binds a new master to an orphaned slave. **The control channel is not
misplaced, it is destroyed.**

We could forge a *registry entry* — but an entry asserts `Alive()` and feeds
nudge and `PolecatCount()`. It would claim **controllability we do not have**:
this ticket's exact conflation, moved into the registry where more code trusts
it. `RecordPolecatWitness`'s doc anticipates "a future adopt path" and the
witness would indeed be its input — but the witness is not the missing piece.
The PTY is, and it is gone.

### Kill makes absence authoritative for destruction

That is the mirror image of mg-de08/mg-8677 — the one move the ticket forbids.
The survivor holds a worktree that may carry uncommitted work and a claim that
may be mid-flight; it can still commit, push, and submit. *"I cannot see it"*
has never licensed reaping here and does not start licensing it because the verb
changed.

### Doing nothing is today's behaviour, and it is the leak

So what is left is to make the survivor **observable**, and let a human decide.

## 4. "Loud" — defined, not asserted

The ticket notes that "loud" was written into both mg-f206 and mg-0b77 **without
being defined**, which is how both close while neither is fixed. architect's
ruling: **loud must mean observable from OUTSIDE the thing that failed** — not a
log line.

This did not need inventing. **`internal/agent/sentineldrift.go` already defines
it operationally**, for the same reason:

> pogo#76 was invisible across the WHOLE fleet because the only signal was a
> per-spawn log line — and nobody reads our logs (the watchdog was dead 4.8h,
> recovery inert 6 weeks, both nominally "observable").

Its answer: **a durable event on the spine + a mail to the coordinator**,
deduplicated per episode. `defaultOrphanAlert` follows that precedent rather
than inventing a second notion of loud.

**Cadence is part of the definition.** Two failures bracket it:

- **Per tick** (~30s heartbeat) → thousands of mails/day about one leaked agent.
  Not loud; a filter rule waiting to happen, and the next real alert dies in it.
- **Once per pogod lifetime** → the leak is permanent and only a human ends it,
  so one missed mail silently leaks the agent forever.

So the alert **repeats on a 1h cooldown until the fault clears**, and
**self-terminates**: once the survivor is dealt with it stops being
witnessed-alive and the mail stops. *Noise that ends when the fault ends is the
fault reporting itself.* The cooldown is also forgotten when a survivor
resolves, so a fresh leak on a reused name is not silenced by a stale timestamp.

## 5. The original scope, downstream: the drain's count

`drain_wait` polls `PolecatCount()` (in-memory `r.agents`), so `"drain complete
— 0 polecats active"` was a claim about the **registry** asserted as a claim
about the **world**. 13a3 now gives pogod something to see, so this became
implementable rather than a wording fix.

**The survivor is deliberately NOT added to `count`.** This is the ticket's own
warning — *"a survivor is not drainable; counting it may just move the lie"* —
and it is right: absence never heals, so a counted survivor would block **every
future redeploy forever**. That is not a fix, it is a worse bug wearing the
word "honest".

Instead `DrainStatus` gains `unreachable` (reported, not counted) and
`unreachable_err`. The drain still completes at 0; it stops claiming the
survivor is not there:

```
drain complete — 0 polecats in pogod's registry
2 polecat(s) are ALIVE but unreachable and were NOT drained:
  cat-9f21 (pid=41207, work_item=mg-9f21)
```

`unreachable_err` carries the mg-76e5 distinction into the drain: an unreadable
witness means *we cannot see*, which must never print as a clean drain.

## 6. RED proofs

Every control was proven able to fail before its green was trusted. Green is
the claim; RED is the evidence the claim can be false.

| # | Mutation (defect reintroduced) | Test that caught it |
|---|---|---|
| 1 | Report on witness-alive alone (drop the registry check) | `RegisteredPolecatIsNotOrphaned`, `DrainStatus_HealthyPolecatIsCountedNotOrphaned` |
| 2 | Probe with bare `pidAlive` instead of the `(pid, start_time)` match | `RecycledPidIsNotOrphaned` |
| 3 | Swallow the witness read error into an empty list | `UnreadableWitnessIsNotZero` |
| 4 | Alert every tick (no cooldown) | `RepeatsOnCooldownNotEveryTick` |
| 5 | Alert once per pogod lifetime (never re-fire) | `RepeatsOnCooldownNotEveryTick` |
| 6 | Keep the cooldown after a survivor resolves | `ResolvedSurvivorGoesQuietAndCanReturn` |
| 7 | Don't populate `DrainStatus.Unreachable` | `DrainStatus_ReportsUnreachableSurvivors` |

Mutations 1 and 2 are the ones that matter. *"Alive but unreachable"* is a
**conjunction**, and each half has a failure mode that reads as success:

- **Drop the registry half** → every healthy polecat is witnessed-alive for its
  entire normal life, so pogod would mail the coordinator about the whole fleet,
  forever — training its reader to ignore the one alert that matters.
- **Weaken the witness half** → a recycled pid resurrects a corpse into a
  permanent alert **no human action can ever clear** (mg-8677 through the fix).

The tests model a restart **as it occurs** rather than imitating it: a real
running process, a witness written by the production writer
(`RecordPolecatWitness`), and a registry that has never heard of it — which is
not a contrivance but the ordinary, permanent state of a survivor.

## 7. The fix rebuilt the bug inside itself (caught by its own test)

`unreachable_list`'s first draft used the ordinary idiom:

```sh
| while IFS= read -r line; do
```

The body arrives via command substitution, which **strips trailing newlines**, so
the pipeline's last record has none — and a bare `read` sets `$line` and *then*
returns non-zero at EOF, so the loop **silently drops the last record**. With one
survivor that is all of them: the function reported **nothing** while looking
like it worked. **The exact silence this ticket exists to remove, rebuilt inside
its own fix**, and it would have shipped as "drain complete, none unreachable".

It was caught because the test asserted the payload shape that actually matters
— `count:0` **with** a survivor — rather than a shape that was convenient. Both
new tests went RED on the real defect, which is a better proof than any mutation
I could have staged.

This is not a novel trap: **`expected_lost_mail_checks` (`:143`) already carries
the `|| [ -n "$line" ]` guard**, and its comment records the same discovery —
*"That silently forgives whichever schedule sorts last — a miss in the detector,
found only by an assertion that named a schedule the daemon really emitted
last."* The lesson did not transfer to the next author (me). It is now recorded
at both sites.

### Adjacent defect found, NOT fixed (out of scope — reported to mayor)

**`cleanup_orphans` (`scripts/pogo-self-deploy`, the `--force` hard-bounce path)
has the same bug and is still live.** It splits the pre-kickstart polecat
snapshot with a bare `read`, so on a forced bounce it **drops the LAST polecat**:
that work item is never unclaimed and its worktree is never removed. Verified by
simulating its extraction on a two-polecat snapshot (`mg-aaaa` cleaned,
`mg-bbbb` silently skipped). Left alone deliberately — it belongs to mg-6afa's
cleanup path, not this ticket.

## 8. What this does NOT fix

Stated rather than glossed:

- **The survivor is still leaked.** This reports it; it does not reclaim it.
  Only a human can, and that is the finding, not a shortfall.
- **`AgentUnknown` is still consumed only by the GC's `== AgentGone` test.** The
  new surface reads the witness directly rather than routing through
  `AgentState`, because the reap decision and the leak report are different
  questions and coupling them would put reap logic on the alert path.
- **`PolecatCount()` is unchanged and still registry-scoped.** That is correct
  (§5) — the count is what the drain polls to zero, and it should measure
  exactly what is drainable.
- **mg-f206's arming decision is untouched.** mg-46a4 §6.4's argument stands: a
  nightly redeploy makes each night's survivors invisible to the next night's
  drain. This makes each one **visible and mailed** rather than silent, which
  weakens that objection but does not answer it — the survivor is still leaked
  and still needs a human. **That remains pm-pogo's call, on a fleet that now
  reports its casualties instead of accumulating them silently.**
