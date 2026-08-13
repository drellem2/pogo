# Stall-watch design — pogod-side work-pile-up nudges

Status: implemented (mg-b971). Source: `internal/stallwatch/`, wired in
`cmd/pogod/main.go`. Origin: cross-droplet feature request gh
[drellem2/macguffin #12](https://github.com/drellem2/macguffin/issues/12)
(CloverRoss, 2026-06-02).

## Problem

The mayor runs an LLM-driven loop that is supposed to, every cycle, check its
mail and check for available work to dispatch. Under prompt drift or LLM
cycle-skipping, the loop can keep *running* (process healthy, `health=ok`)
while silently dropping those steps. Work then piles up: items sit unclaimed in
`available/`, mail accumulates unread in `new/`. Health-based watchers don't
catch this — the mayor isn't stalled, it's *behaviorally* stalled.

This is the third leg of the wedge-response triad:

- **Leg 1 (Ocean, Mayor §3c)** — catches Director / Architect / Doctor *process*
  stalls.
- **Leg 2 (Ocean, Director Crew Wedge-Watch)** — catches Mayor / Architect /
  Doctor *process* stalls.
- **Leg 3 (pogod, this doc)** — catches Mayor *behavioral* stalls (process
  healthy, work piling up).

**Why pogod, not an Ocean-side watcher.** If the mayor's own loop is the thing
dropping steps, a watcher that lives in that same loop drifts with it — watcher
and watched skip together. pogod's heartbeat is the only watcher in the system
with a guaranteed-independent cadence (it's a Go ticker, not an LLM cycle), so
it's the only place this check belongs.

## Design

A `stallwatch.Watcher` is built at pogod startup and driven from the heartbeat
`OnTick` callback — the same loop the scheduler and `system_wake` detection ride
(see `docs/sleep-resilience-design.md`). Piggybacking means the check inherits
the heartbeat's clock-jump resilience for free and adds no new goroutine
lifecycle to reason about.

On each tick the watcher runs two independent checks:

### Threshold A — unclaimed items

Scan `~/.macguffin/work/available/` via `workitem.ListFrom`. An item counts as
the mayor's responsibility when its `assignee` is the watched agent **or** empty
(unassigned available work is the mayor's to dispatch).

> **SUPERSEDED (mg-4bd4, 2026-07-17).** The rule above is what shipped, and it
> was wrong: it allowlisted the values a *dispatcher* carries, so it skipped
> every item naming an *owner* — 13 of 14 available items, because PMs file with
> `--assignee=pm-<name>`. An item now counts as the mayor's responsibility unless
> its assignee is an execution gate (`non_dispatchable_assignees`, default
> `["human", "parked"]` — `parked` added by mg-a3a2 so a deliberately-parked
> item can go quiet without falsely claiming a human owns it); ownership no
> longer affects visibility. See "Ownership vs execution" in
> docs/CONFIGURATION.md.

Age is the work-item
file's mtime — the best available proxy for "time sitting in the available
queue," since mg rewrites/moves the file on status transitions. Any qualifying
item older than `unclaimed_item_age_threshold` triggers a single batched nudge
listing the offending IDs.

#### The remedy is checked against the per-repo cap (mg-dd77)

The finding ("N items have aged past the threshold") and the remedy ("claim or
dispatch them") were composed together and only the finding was ever verified.
On 2026-08-10 stall-watch mailed the mayor about **57 aging items, every one of
them undispatchable**: all 65 dispatchable items in the queue lived in two
repos, and both held 3 polecats against a per-repo cap of 3. A positive control
confirmed the cap was the binding constraint — a real `spawn-polecat` for one of
the named items was refused, cleanly and atomically, with the item left
`available`.

Two components held halves of the same fact and did not consult each other: the
dispatch cap knew the fleet was saturated and said so with the occupying workers
named; stall-watch knew items were aging and did not know the cap existed.

So before naming a remedy, the watcher groups the due items **by repo** and asks
a `stallwatch.Capacity` probe — wired in `cmd/pogod` to the same
`agent.Registry.RepoOccupancyFor` the spawn point refuses on, so the advice
cannot drift from the enforcement — and says which of three situations it is:

| Situation | What the notice says |
|---|---|
| repo has free slots | unchanged: *claim or dispatch them: …* |
| repo at cap | *N aging in `<repo>`; it is at its cap of 3 (workers: …). A throughput observation, not a dispatch request.* |
| occupancy unknown | *occupancy for `<repo>` could not be determined … attempt the dispatch and read the refusal* |

Three properties of that table are load-bearing:

- **Nothing goes silent.** Every aging item is still in the message and in
  `item_ids`; only the remedy changes. Trading a noisy alarm for a missing one
  would be a worse bug than the one being fixed.
- **The occupants are named.** That is what turns an instruction the coordinator
  must ignore into a question it can act on — *is one of these wedged?*
- **The two situations become countable.** `stall_watch_fired` now stamps
  `dispatchable_ids`, `at_cap_ids`, `at_cap_repos` and
  `occupancy_unknown_ids`. At cap, aging items are the *expected steady state*
  and say nothing about coordinator diligence; below cap the identical message
  means work is being neglected. Before this they produced identical mail and
  identical events.

The notice deliberately does **not** say "no action required — it will dispatch
when a slot frees." Nothing in pogod auto-dispatches a work item; the
coordinator does. At cap the honest advice is the cap's own vocabulary — a
LATER, not a never — and inventing a self-draining queue would repeat this
ticket's defect in the opposite direction.

What a "free slots" verdict does **not** promise: the per-repo cap is one of
several refusals. Gated assignees and `stage: gated` items never reach a notice
at all, but the host-load gate and the stranded-push gate are not consulted, so
"free" means *the cap would let this through*, not *this dispatch will succeed*.

#### `available` is not evidence that nobody is working it (mg-1a8a)

Both dispatch checks infer ownership from item status, and since mg-7254 that
status is set by pogod at spawn — but the claim **fails open**. On a not-found
or unreadable store the polecat is dispatched anyway and the item stays in
`available/`. The spawn point logs this and, until this fix, named the
consequence itself:

    polecat pc-fronted: could not claim work item wi-1 at spawn: … — dispatching
    ANYWAY … If wi-1 is a real item still in available/, stall-watch will report
    it as neglected while this polecat works it

Every reader downstream is then wrong in the same direction: the standard notice
calls the item neglected, priority-wake says "claim or dispatch **now**", and a
coordinator acting on that nag spawns a **second** polecat onto work already in
progress — two branches touching the same files, the concurrent-edit shape that
has cost this fleet repeated rebase conflicts.

The claim field cannot carry the distinction; it is already overloaded with "in
progress", "finished, awaiting a human" (mg-ed7b) and now "in progress but
unclaimable". So the repair is a **second source of truth**: pogod knows which
polecats are alive and which item each was dispatched at, independently of
whether the claim stuck. A `stallwatch.Workers` probe — wired in `cmd/pogod` to
`agent.Registry.WorkItemsInFlight`, the union of the in-memory registry and the
persisted polecat witness, because the registry alone is empty after a restart
(mg-13a3) — is sampled **once per tick** and shared by every check that reads
`available/`.

| Situation | What happens |
|---|---|
| live worker on the item | dropped from both dispatch checks; re-reported by the **worked-but-unclaimed** notice, which says *do NOT dispatch* and names the worker, its pid and the evidence |
| no live worker | unchanged — the item is neglected and the notice says so |
| probe cannot answer at all | unchanged, i.e. reported as neglected: a false "dispatch this" is self-correcting, a false silence looks like a healthy queue |
| attribution may be incomplete (unreadable witness, live registry) | dispatch notices fire as before **and** carry the caveat, since an incomplete snapshot can only cause a worked item to be missed, never invented |

The worked-but-unclaimed notice has **no age threshold** — a fresh item is not
yet evidence of anything, but a worked-but-unclaimed one is an anomaly the
instant it exists, and the cost of missing it is highest in the first minutes,
before either polecat has pushed. It shares `selectDue`, so a standing anomaly
still backs off per item, and it stamps `workers` (item → polecat, pid,
evidence) on `stall_watch_fired` so "aging because nobody dispatched it" and
"aging because its claim failed open" are countable apart.

**The suppression is paired with a re-report, deliberately.** Dropping worked
items and saying nothing would fix the double-dispatch and hide the anomaly —
and the missing claim is what `mg done` needs at the *end* of the work, so the
polecat discovers it after doing everything. Silence is also the mistake this
component has already made twice (mg-4bd4, mg-1693).

**What this does not fix.** The item still *reads* `available` to anything
consulting mg directly — a human at the board, or a coordinator that dispatches
without asking pogod. Nothing at the spawn point refuses a second polecat on an
item a live worker already holds; the claim-at-spawn conflict is that guard, and
it is precisely the guard that failed open here. This fix removes the nag that
induces the second dispatch, not the ability to make one.

### Threshold B — unread mail

Scan the watched agent's `new/` maildir. Fire when either the oldest message is
older than `unread_mail_age_threshold`, **or** the unread count exceeds
`max_unread_mail_count`. A missing maildir (agent never received mail) is benign
and silent.

### Nudge + event

On a cross the watcher calls its injected `Nudger` with a `Notice` — a body and
a mail subject — which pogod wires to a
PTY-then-mail fallback: nudge the mayor's PTY in wait-idle mode when it's
running, and fall back to durable `mg` mail whenever the PTY cannot carry the
message — **both** when the mayor is offline and when the PTY nudge *fails*. It
then appends a `stall_watch_fired` event recording the category, counts, ages,
the delivery channel (`nudge_delivery`), the subject (`nudge_subject`), and —
only if every channel failed — the nudge error.

#### The subject carries the facts, because the subject is what travels (mg-b6f8)

The `Nudger` takes a `Notice` rather than a bare string because the delivery
site cannot compose a subject. Until mg-b6f8 it did not try: every stall-watch
mail — five categories, any item set, any age — was sent under the one string
`stall-watch: work piling up`, chosen in `cmd/pogod` where none of the facts are
in scope.

The cost is measured twice in this document from two directions. The mg-61ce
table above notes the fallback subject was "the single largest subject line in a
5978-message mailbox by a factor of nine", which is the same defect counted as a
mailbox statistic. mg-b6f8 counted it as an experience: between 2026-08-11
12:00Z and 2026-08-12 09:52Z, `human` received 18 stall-watch mails. All 18 were
blocked-reminders; their bodies covered **three different item sets**
(`mg-fbc1`, `mg-8888`, both together, then `mg-0218`) at counts of one and two.
All 18 subjects were identical. The recipient reads mail through Discord, which
renders the subject, so eighteen distinguishable facts arrived as one sentence
printed eighteen times.

**The remedy is not to send fewer.** The rate limiting works — 18 notices in 22
hours is far from every occurrence — and those notices were correct; several
genuine stalls were dispatched off them overnight. Lengthening the interval
would trade a working signal for quiet, which is a regression that looks like a
fix. The remedy is that the message body has *always* named the category, the
count and the ids, and the subject threw all of it away.

Subjects now render as `stall-watch: <head>, oldest <age> — <ids>`, and each
part earns its place by making two notices that differ in any way render
differently:

- **head** names the category and count, so `1 item blocked on you` never reads
  like `1 item unclaimed` — which matters most here, because those two mean
  *opposite* things to the same reader and arrive in the same list.
- **age** is what distinguishes a **repeat**. Count and ids are identical across
  the repeats of a persisting stall — that is exactly the six consecutive
  `mg-0218` notices above — and the oldest item's age is the only one of the
  three that must have moved. It is strictly increasing for a fixed item set,
  and minute resolution is finer than the shortest cooldown any category uses
  (3m, the priority wake), so consecutive fires cannot collide.
- **ids** name which items, which is the first thing a reader wants and the
  reason they would otherwise open the mail.

Past five ids the list truncates to `+N more`; two *simultaneous* batches
sharing a five-id prefix at an equal count would then differ only in age. That
residue is recorded rather than engineered around — the fix for it (a digest of
the full id list) would cost the subject the readability it exists to buy.

`ack-watch` is a useful contrast and was checked in the same window: it sent 15
mails under 2 subjects, and its subject is *computed* (`report.MailSubject()`,
carrying the fire counts). It repeated because the underlying counts were
genuinely stable, not because it discarded them — a different and much smaller
problem, and it stopped on its own at 08-11 19:10 when the blackout condition
cleared. A fix aimed at "notification noise" in general would have landed there.

#### Why mail backstops a *running* agent (mg-79dc)

The fallback originally covered only an offline mayor, on the reasoning that a
running agent gets the PTY and mailing it too would double-deliver. That
conflates *running* with *reached*. Wait-idle can only deliver to an agent that
goes quiet, and **a working agent never goes quiet** — so the channel failed
exactly when the mayor was busy, which is precisely when a dispatch stall is
most likely and the notice most needed. A watcher whose reporting channel goes
dark under the very condition it watches for is not lossy; it is blind, and the
correlation is the whole problem.

Measured on 2026-07-17: 18 of 47 fires (~38%) died with `still producing output
after 30s ... context deadline exceeded`, including both work-item fires.

**Not a timeout-tuning problem.** Every dropped fire recorded a "last PTY write"
of 2–305ms: the mayor was writing *continuously*, not almost-quiet. No deadline
survives that, so lengthening the timeout would only trade a visible failure for
a slower one. `mg mail` is the right shape because it does not require an idle
recipient at all. The fallback also lands in a channel stall-watch itself
watches (`unread_mail`), so an ignored notice escalates rather than vanishing —
**but see the next section: that escalation path is also a feedback loop, and it
was described here as a pure benefit for a year before anyone measured it.**

This does not weaken the never-interrupt-a-busy-agent guarantee (gh #61): the
PTY is still never written to while busy. The guarantee was "do not interrupt a
busy agent", not "do not inform it". Nor does it double-deliver — mail is sent
only when the PTY nudge returned an error, i.e. only when nothing was written.

#### The fallback is damped per recipient (mg-61ce)

mg-79dc got the channel right and left the *rate* unbounded. The direction that
runs in is perverse rather than merely noisy: the fallback fires **because** the
recipient is too busy to go idle, and answers by adding work to that recipient's
inbox. The busier the coordinator, the more often the PTY refuses; the more it
refuses, the more the watcher mails the agent it has just observed to be
overloaded. **The remedy load rises with the load it is responding to**, and
nothing in the loop pushed back.

For the `unread_mail` category it is not merely perverse, it is closed. That
notice says *"your inbox is too full"* and is delivered **as one more message in
that inbox**, so the remedy re-arms its own trigger — gain ≥ 1 with no damping
term. The paragraph above, which offers exactly this as the reason mail is safe
("an ignored notice escalates rather than vanishing"), is describing the loop
from inside it.

Measured on one box, over the last 20 000 events and against the live maildir:

| Fires by category and channel | fallback | plain mail | pty |
|---|---:|---:|---:|
| `priority_wake`    | 720 | 500 | 308 |
| `unclaimed_items`  | 559 | 302 | 180 |
| `unread_mail`      | 530 | 303 |  75 |

1814 fires took a mail road. The coordinator's maildir holds **766 stall-watch
messages** — 742 of them the "(undelivered to terminal)" fallback, the single
largest subject line in a 5978-message mailbox by a factor of nine — of which
**179 are the self-referential unread-mail notice**.

**This is not a wedge report.** Load average was 4.14 on a box that normally
idles, the coordinator was at 16.8% CPU actively computing, and it had sent a
substantive mail reply fifteen minutes earlier. *"Still producing output after
30s" is the instrument seeing a busy agent, not a stuck one* — the same property
recorded in `a-spinner-defeats-both-liveness-instruments`. This finding argues
**against** a restart policy, not for one.

**The fix is a damping term, not a better gate.** Three candidates were weighed:

- *Strip spinner glyphs before judging idleness.* Rejected — it addresses a
  different failure. The evidence here is a coordinator genuinely computing, so
  the gate was reporting correctly and no amount of cosmetic filtering would
  have prevented a single one of these fires.
- *Drop the idle gate for high-priority fires and write regardless.* Rejected —
  it breaks the gh #61 never-interrupt guarantee and converts a mail flood into
  a PTY flood at the exact moment the coordinator is most loaded. Same feedback
  direction, louder channel.
- *Rate-limit the fallback per recipient.* Adopted. It is the only one of the
  three that acts on the thing actually measured — the missing damping term —
  rather than on the gate, which is not wrong.

`mail_fallback_backlog_cap` (default 3) counts, per recipient, consecutive
fallbacks since the last **successful PTY delivery**. That reset signal is
direct evidence the agent went idle, which is precisely the condition that means
it is between turns and able to drain. Past the cap, further fallbacks are
withheld. Deliberately *not* keyed on inbox depth: the coordinator's is the one
mailbox on this box where real traffic outweighs noise, so damping on total
unread would let legitimate mail from other agents silence the watcher.

Withholding is not silence, and the signals that report it are outside the loop
by construction — a suppression notice sent by mail would be the same defect
wearing a disguise. The transition is logged loudly **once per run**, and every
suppressed fire stamps `nudge_suppressed_consecutive` on `stall_watch_fired`; a
counter climbing across fires means the coordinator has not gone idle once in
that whole span, which is sharper than the flood it replaces. A suppressed fire
carries `nudge_delivery = "suppressed"` and **no** `nudge_error` — nothing was
delivered, but that was a decision, and only a fault should read as an outage.

This is reconciled with mg-79dc's first-attempt doctrine ("the cooldown is a
rate limiter, not a retry queue") rather than in tension with it. That doctrine
is about notices reaching *nobody*. Suppression happens only when a capful of
same-channel notices already sits in front of the recipient undrained, so the
marginal notice reaches nobody either way. And nothing is lost on the far side:
the watcher re-derives every condition from scratch each tick and never queues,
so the moment the recipient is reachable the **current** state fires, not a
stale replay. Suppression defers a notice until it can be received; it does not
discard a fact.

Two scope boundaries. The cap bounds the **fallback** road only — the offline
road has the same flooding shape (303 of the measured fires) but no reset
signal, since an offline agent never produces a PTY success, so a cap there
would latch permanently the first time a coordinator went down. And a recipient
going offline *clears* its run, both because a restarted coordinator is a new
process that a dead process's run says nothing about, and because that is what
bounds the damper's own memory as unique polecat names come and go.

**The 30s wait-idle budget was re-checked and stands.** mg-61ce asked whether it
still fits the current fleet, since it was chosen against a smaller one. Across
1702 recorded fallbacks the gap since the coordinator's last PTY write *at the
moment the deadline expired* had a median of **218 ms** and a p99 of **941 ms**,
against a 2 s idle threshold; only 10 of 1702 (0.6%) had reached even one
second, and the maximum ever observed was 2.58 s. The coordinator is not
almost-quiet when the deadline fires — it is writing continuously — so a 60 s or
300 s budget buys nothing and holds the heartbeat longer for it. Every one of
those fires was decided in its first two seconds. Same conclusion mg-79dc drew
from 18 samples, now confirmed at ~100× the n.

### Cooldown

Each category (`unclaimed_items`, `unread_mail`, `priority_wake`) has its own
cooldown keyed in a mutex-guarded map. The fire time is recorded *before* the
nudge attempt, so a failed delivery still consumes the cooldown rather than
hammering a wedged recipient every tick.

For the two **work-item** categories the map key is the `(category, item)` pair
and the gap escalates: the first notice about an item is immediate, each repeat
about that same item waits twice as long as the last, ceilinged at
`repeat_backoff_cap` (4h). Each nudge names only the items actually due, and a
tick on which every offending item is still inside its own backoff sends
nothing. `unread_mail` is a single aggregate condition with no item identity, so
it keeps a flat per-category cooldown.

#### Why the key is the item, not the category (mg-1693)

Keyed on the category alone, the cooldown suppressed repeats of a *kind of
alert* rather than repeats *about a given item* — and nothing recorded that the
coordinator had already seen an item and decided to wait. That broke in both
directions:

- An item held **on purpose** (behind the polecat cap, a snooze, a sequencing
  call) was re-detected and re-notified every cooldown, indefinitely. Measured
  on the live host on 2026-07-30: 87 fires carrying **212 item-notices across 29
  items**, `mg-61f4` 22 times, `mg-0e24` 27, `mg-7c95` 22 — all items the mayor
  was deliberately holding at its five-polecat cap.
- A genuinely **new** item arriving mid-cooldown was swallowed by the held
  item's timer. That half never showed up as noise, because a miss is silent.

The trap worth naming, because it is where this bug pushes a reader: **the
detection was correct in every one of those 212 notices.** It presents as a
false-positive problem, so the natural repair is to make detection stricter —
which would suppress true positives and leave the actual mechanism in place. A
correct detector with a broken repeat-suppressor is indistinguishable from an
over-firing detector unless you count *per item*, which is why
`stall_watch_fired` now stamps `repeat_counts` and `backoff_suppressed_ids`.

**Backoff rather than one-and-done.** Never re-notifying a held item would risk a
genuinely forgotten one going quiet forever; the doubling keeps the safety net —
a held item settles to about one notice per 4h — while removing the training
effect, which is the second-order cost that actually matters. A coordinator that
has seen `mg-61f4` twenty-one times learns the wake means nothing, and the
twenty-second one, about genuinely neglected work, reads identically. Repeats
are also marked `[repeat] … (notice #N)` in the message text so a re-raise is not
textually identical to a first notice.

State is in-process, so a pogod restart forgets every backoff and re-notifies
each held item once. Deliberate: a restart is exactly when the coordinator's own
memory of what it was holding is gone too. A coordinator-clearable
already-notified set was the alternative considered and declined — it adds
surface (a new command, a new failure mode where the set is never cleared) to buy
what the cap already provides.

**The cooldown is a rate limiter, not a retry queue** — the distinction is
load-bearing and easy to get backwards. A failed nudge is never queued or
re-sent. What happens after the cooldown is that the *condition* is sampled
afresh, and only if it still holds is a *new* message composed. So a stall that
resolves inside the cooldown window takes its undelivered notice with it,
silently; a stall that resolves-then-recurs reports the recurrence as if it were
the first. This is why delivery must succeed on the **first** attempt, and why
the mail fallback — not a retry — is the fix.

The check runs in a goroutine off `OnTick`: a wait-idle nudge can block up to
`DefaultNudgeTimeout` (30s), and the heartbeat goroutine must not stall the
scheduler sweep. The cooldown map + mutex make overlapping checks safe.

### Threshold C — a hold with no driver (mg-f398)

The two thresholds above watch the **dispatchable** population. This one watches
part of what they skip, and it is the only check here that is not a nudge to act.

`snooze` and `depends` are holds with a driver: mg's `*/15` sweep evaluates them
and promotes what has opened. `parked` and `human` have none. Nothing scheduled
ever evaluates their release condition, so an indefinite hold persists until a
person happens to look — and the release condition can be perfectly well-formed
and change nothing, because nothing reads it.

**The exhibit is one item and it is stronger than the other 21 it arrived with.**
On 2026-08-10 20:12–20:17Z, 22 items were parked under a token-budget cap. They
were released 2.5 days later, only because a coordinator happened to trace one
ticket's park reason while looking at something else. 21 carried a *circular*
release condition — *"clear `parked` when the constraint is lifted and this item
is selected for work"*, circular because `parked` is the state that removes an
item from selection, so nothing selects it and nothing clears it. That is a real
defect with an obvious fix, and **the fix would have repaired nothing**: the
22nd, `mg-e7f5`, said *"Reopen/clear assignee when the cap lifts"* — one clause,
naming a condition entirely outside the item, circular in no way — and it
stranded for exactly as long. The circularity explained 21 cases and caused none
of them. A rule against self-referential wording would have shipped, felt like a
fix, and left the mechanism untouched.

Once a gated item has sat past `indefinite_hold_age_threshold` (24h), the
coordinator receives a digest naming every held item and its age, repeating every
`indefinite_hold_report_cooldown` (24h).

**This is deliberately not the park-sweeper `mayor.md` forbids.** That ruling
rejects giving pogod sight of gated items *in order to release them*, on the
grounds that the same predicate gates dispatch. It stands. Every clause of this
check's boundary is load-bearing:

- It **releases nothing** and **writes no field**. Release stays a
  coordinator/human judgement.
- It **infers nothing from item text** — no title or body is read, matched, or
  parsed. In particular there is no "until"/"after" keyword sniff, the second
  thing `mayor.md` prohibits, because that rots on the next phrasing and fires on
  rows that are already right. It reports the *fact and age* of a hold, never its
  meaning.
- It **acquires no dispatch capability** and routes through nothing that has any.

What changes is one thing: *"somebody happens to look"* becomes *"something looks
on a cadence."* `parked` keeps its meaning — it still buys silence from both
dispatch nudges, which is what it was for.

**Why sight became affordable.** The ruling was priced against "sight implies
dispatch", which stopped being true at mg-4798: the gate now lives in the
executable path at the spawn point (`agent.MGDispatchGate.DispatchGated` refuses
on `config.IsDispatchGated` and names what gated it), armed in `cmd/pogod`
deliberately *outside* `stallWatchArmed` so a daemon that never reaches that line
still gates the defaults. The honest caveat, because a reader weighing this is
owed it: that gate **fails open** on an unreadable store — it logs `dispatching
WITHOUT the assignee gate` and proceeds. Pre-existing, and a read-only reporter
dispatches nothing under any branch, but the refusal is not unconditional.

The ruling's other premise was that an indefinite hold has no release time, so
degraded redundancy cannot make it *late*. True, and not the same as no cost:
nothing was late for those 22 items, because nothing was due, and 2.5 days of
real work still did not happen.

**Population is a rule, not a list.** Membership is "gated by assignee, and not
the one gated shape something already chases" — `blocked:<agent>` is excluded
because mg-3844 gave it a reader. Everything else `IsDispatchGated` holds is in,
which by default is `parked` and `human`. Stated as a rule so a gate value added
to `non_dispatchable_assignees` next year gets a reader for free.

Two deliberate divergences from the blocked-reminder:

- **No notice cap.** That cap exists because nagging an agent who has decided to
  wait is noise. Here the silence *is* the defect, so a cap would restore it. The
  cost is bounded by shape instead — one digest naming every held item, so a
  permanent hold is one line per cycle rather than a mail of its own.
- **A flat cadence.** `repeat_backoff_cap` (4h) is below this base (24h), so
  `repeatCooldown` returns the base unchanged rather than doubling. That is the
  intended shape and is implicit enough to be pinned by a test.

**Two self-applications**, since a remedy is an artifact of the same kind as the
defect it treats:

- A reader that ships **disarmed** is this finding one level up — a hold nothing
  looks at, plus a looker nothing turns on. So it ships default-on, and pogod's
  startup line prints `indefinite_hold=` because the report emits events only
  when something is held; without that line, "no events" and "disarmed" are the
  same observation.
- An item whose file cannot be stat'd has a zero `ModTime`. Dropping it would
  make a real hold invisible for want of one field — the finding itself — and
  arithmetic on the zero time would report a fresh hold as ~739 000 days old.
  It is reported with `age UNKNOWN` and listed under `unaged_ids`.

Scope note, recorded rather than fixed: `stage: gated` and an unreadable carrier
also hold an item indefinitely and are also unwatched. They are a different
population — a carrier declaration rather than an assignee hold, and the
unreadable case is a parse defect to repair rather than a hold to age.

## Configuration

`[stall_watch]` in `~/.config/pogo/config.toml`. Defaults (in
`internal/config`): enabled, agent `mayor`, both age thresholds 10m, max unread
5, cooldown 5m — matching the gh #12 spec's 600s/5/300s — plus a 4h
`repeat_backoff_cap` (mg-1693) the spec did not contemplate.

```toml
[stall_watch]
enabled = true
agent = "mayor"
unclaimed_item_age_threshold = "10m"
unread_mail_age_threshold = "10m"
max_unread_mail_count = 5
nudge_cooldown = "5m"
repeat_backoff_cap = "4h"
mail_fallback_backlog_cap = 3

# Threshold C (mg-f398) — read-only, releases nothing.
indefinite_hold_report_enabled = true
indefinite_hold_age_threshold = "24h"
indefinite_hold_report_cooldown = "24h"
```

### Deviation from the gh #12 spec shape

The issue sketched the config as a nested JSON
`stall_watch.agents.mayor.*_seconds` block in `~/.pogo/config.json`. pogo has no
JSON config — it uses a flat, single-line TOML reader (`config.loadConfigFile`),
and the mayor is the only behavioral-stall target today. So this ships as a
single flat `[stall_watch]` section with a configurable `agent` key rather than
a per-agent map, and Go-duration strings (`"10m"`) in place of `*_seconds`
integers. The semantics are identical; the shape matches the rest of pogo's
config. If a second watched agent is ever needed, this is the seam to revisit.
