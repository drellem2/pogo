# Blocked-reminder: telling the agent a `blocked:<agent>` hold names

**Status: shipped** (mg-3844) — `internal/stallwatch/blockedreminder.go`, config
`[stall_watch] blocked_reminder_*`. This is a decision record kept as rationale,
not a forward plan. The code is the source of truth.

## The question

`--assignee=blocked:<agent>` is `config.IsDispatchGated`, and that predicate has
two enforcement points — it gates **watching** as well as dispatch. So the
hold-instrument table shipped by mg-61f4 described the row honestly:

> *a named agent must act, no deadline* → `mg edit <id> --assignee=blocked:<agent>` →
> **"nothing scheduled — but the field names who to chase"**

The field recorded **who** should act and provided **nothing that told them**. A
high-priority item blocked on a named agent produced no signal to that agent.
Should it emit a reminder — a different signal, to a different recipient, from
the dispatch nudge?

**Ruling: yes, for `blocked:<agent>` only, capped, and never for `parked` or
`human`.**

## Why this is not the rejected park-sweeper

`mayor.md` rejects a sweeper over gated items:

> anything that gave pogod sight of parked items in order to release them would
> also let it **dispatch** them — it is the same predicate.

That argument is about **release**, and it survives untouched: nothing in the
reminder releases anything.

Two things had to be checked before concluding the distinction holds, because
the ticket's own reason for it was not quite the right one.

**1. The ticket said a reminder "needs no such capability".** That is true of the
*message* but not of the *mechanism*: anything that finds blocked items must
enumerate the gated population, which is exactly the sight the sweeper argument
objects to. So the sight question had to be answered on its own terms, not
sidestepped.

**2. The sweeper argument's premise is narrower than it reads.** "Sight implies
the ability to dispatch" stopped being true when **mg-4798** shipped
`agent.MGDispatchGate`: dispatch is refused at the **spawn point**, by name, for
any item whose assignee gates — independently of who saw what. Stall-watch's
blindness is now defence in depth rather than the only defence. That is what
makes a second reader of the gated population affordable at all.

With those settled, what actually distinguishes the reminder from a sweeper is
the pair **(capability, recipient)**:

- **It does not release.** It sends a message. The only party that can clear the
  block is the one already named in the field, so asking them to exercise the
  judgement the field says is theirs is the *designed* release path, not a
  bypass of it.
- **`blocked:<agent>` is the only gated value that carries a recipient.** That is
  what makes a targeted reminder implementable where a sweeper is not — `parked`
  and `human` name nobody to tell.

## Why `parked` and `human` stay silent

Their silence is the feature, and mayor named this as the over-fixing risk when
the question was raised. `mayor.md` files items on `human` precisely so they stop
generating traffic; `parked` means "deliberately not chasing this". Firing on all
three gated assignees would convert an intentional quiet into noise, which is
strictly worse than the gap being closed — a channel that fires on things nobody
wants chased is one readers learn to discount.

So the reminder's population is the `blocked:` **shape**
(`config.BlockedOn`), not `config.IsDispatchGated`. It is the one place in the
codebase where those two deliberately differ.

## The failure is not forgetting — it is never knowing

mg-3844's title says a hold "depends on that agent remembering". Mayor's
first-hand instance sharpened it, and the sharper version is the one the design
answers.

On 2026-07-30 mg-e084 was filed with `--assignee=blocked:pm-pogo`. It sat
`status=available` and `priority=high` and drew **zero** stall-watch and **zero**
priority-wake alerts — the gate silenced both, as designed. Nothing reached
pm-pogo. Remembering presupposes having been told once, and nothing told them.

Hence: **the first notice is never delayed.** `selectDue` fires immediately on an
unseen item, so a block set at 11:00 produces a notice within one heartbeat tick.
The recurring cadence is the lesser half — a cadence merely accelerates a fix
that a first notice makes unnecessary.

### Reconciling the two readings of mg-e084

The ticket carries two accounts of the same instance that can read as
contradictory. Both are correct and both are kept:

- **Doctor's:** the hold *was discharged*. pm-pogo ruled at 08:52:16Z; doctor's
  check landed 10m48s later. Nothing went wrong.
- **Mayor's:** the gate *contributed nothing* to that discharge. No reminder, no
  notice, no nudge reached the named agent.

Both hold because the discharge came from **outside** the mechanism: pm-pogo
learned of mg-e084 from two out-of-band mails, one of them mayor's hand-written
notice at block time. The accurate joint statement is **the mechanism was absent,
its absence was covered by diligence, and no harm resulted** — a load-bearing
accident. That is evidence the gap is real and routinely papered over. It is
*not* evidence the gap has ever cost anything, and nobody citing this should
claim otherwise.

## The stop condition

Mayor asked for "a stop condition other than the block clearing, or a long-blocked
ticket becomes a repeating nag on an agent who has already decided to wait."

`RepeatBackoffCap` alone does not supply one: it bounds the **rate** and never
terminates, so a week-long hold draws a notice every cap-interval forever. That
is the **mg-1693** shape re-created on a new recipient — and worse here, because
an agent waiting on purpose has no way to say "I know" short of clearing a block
it is not ready to clear.

So the cap is a **count**: `blocked_reminder_max_notices`, default **4**. Under a
doubling backoff from a 1h base those span ~7h (0, +1h, +2h, +4h) — long past the
point where "the agent never knew" is a live explanation. A negative value
disables the cap.

The base cooldown is an hour rather than the 5-minute stall cooldown because the
recipients differ in kind: a stall nudge asks the coordinator for a dispatch it
can make in seconds; a blocked-reminder asks an agent for a **decision**, which is
not faster for being asked twice an hour.

Reaching the cap emits an event carrying `notice_cap_reached_ids` even though no
nudge is sent. mg-1693's lesson is that a silence nobody can count is
indistinguishable from a detector that stopped working.

## Recipient resolution — the part that nearly sank it

This is where the design met the implementation, and it is the reason the ruling
has conditions attached.

`macguffin/internal/mail.Send` validates a recipient as a **single path
component** and nothing more. There is no roster. Mailing an unrecognised name
therefore **silently creates that mailbox**, and the reminder rots in a maildir
no agent drains — a notice that looks sent and reached nobody, which is precisely
the disease being treated. The live store has ~1125 mailboxes and visibly
contains typo'd ones.

This is not hypothetical for the documented examples: `mayor.md` offers
`blocked:daniel` alongside `blocked:architect`, and `daniel` is not an agent that
drains a queue.

The test used is **"does this name already have a maildir?"** — a *have we ever
corresponded* test, not a roster lookup, because there is no roster to look up.
The failure direction is deliberate: a brand-new agent that has never received
mail reads as unreachable and its block is reported to the coordinator instead
(one redundant notice about a real item). Guessing the other way sends the only
notice into a void, silently.

An unreachable blocker — no maildir, or a bare `blocked:` naming nobody — is
reported to the **coordinator**, whose message states in its own text that it is
**not a dispatch request**. That wording matters because the recipient is the
dispatcher and every other work-item notice it receives means "dispatch this".
The wording is the courtesy; `dispatchGateRefusal` at the spawn point is the
guarantee.

The agent name is also lower-cased and refused if it is not a single path
component, since it arrives from frontmatter and is path-joined against the mail
root.

## What this does not do

- **It does not release the hold.** Nothing does, except the named agent acting
  and someone clearing the field. The hold-instrument table's "the top two rows
  are the only holds that anything will ever open for you" is unchanged.
- **It does not make blocked items dispatchable or visible to the dispatch
  checks.** `watchedForDispatch` is untouched; a `blocked:` item still draws no
  stall nudge and no priority wake.
- **It does not carry the block's reason.** The reminder names the item; `mg show
  <id>` is where the blocker learns what is wanted. A title cannot do this job —
  `mg list` truncates around 90 chars, and more to the point the blocked-on agent
  is not reading that list, which is the entire premise of the ticket. A title fix
  and a notification fix solve different problems for different people.
- **It does not persist across a pogod restart.** The backoff state is
  in-process, like every other stall-watch category, so a restart re-notifies each
  blocked item once and the count starts over. That is consistent with the
  existing design (a restart is when the coordinator's own picture is also gone),
  and it means the 4-notice cap is per-process, not per-block.

## A note on the evidence, worth keeping

Doctor named a meta-point while raising this, and it generalises past the ticket:
three times in one night someone reached for the most **vivid** available incident
without asking whether it tested the control in question. **Vividness and
relevance are uncorrelated** — an incident that comes to mind easily is selected
for memorability, not for being a test of the thing you are arguing about. The
mg-e084 reconciliation above exists because that check was applied and the
timestamps did not say what the first reading assumed.
