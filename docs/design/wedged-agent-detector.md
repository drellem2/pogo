# Wedged-agent detector

**Status:** items (1) and (2) shipped (mg-fc8d, `internal/wedgewatch/`). **Item
(3) — escalating outside the wedged party — is NOT built and is not a proposal
here; it is an open decision for Daniel.** See §5.

## 1. The fault

On 2026-08-04 twelve polecats and the doctor crew agent sat at a Claude Code
login prompt for **thirteen hours**. On 2026-08-05 it recurred for seven. About
twenty agent-hours of nothing, and for the whole of both windows every liveness
instrument in the daemon read healthy:

```
pogo agent list
  teaa9   status=running   uptime=13h44m   last-activity=just now
```

For all twelve, simultaneously, the whole time.

The agents were not frozen. They were **animating**. Claude Code redraws a
spinner and an elapsed counter while parked at a prompt, and that redraw is PTY
output, so:

- `last-activity` tracks PTY writes → permanently "just now"
- the process is alive → `pgrep` / status say running
- CPU is near zero — but so is a legitimately blocked agent's

Every instrument was measuring the animation. Both wedges were found by a human
reading a terminal.

`stall-watch` was firing correctly the entire time — "14 available work items
unclaimed for over 10m", roughly every five minutes for thirteen hours. It was
right and it was ignored, because it reports the *symptom* (the queue not
draining) to the agent that was itself wedged.

## 2. What the detector reads

Two checks, both on pogod's heartbeat, both report-only.

### (1) Enumerated dead-end states — `markers.go`

`Please run /login`, `API Error: 401`, `Unable to connect to API`, `ENOTFOUND`,
`EAI_AGAIN`, the rating dialog, the rate-limit modal.

Matching is whitespace-insensitive against the ANSI-stripped buffer. This is not
cosmetic: Claude Code renders TUI footers as columns placed with cursor-forward
escapes (`ESC[<n>C`), and `agent.StripANSI` **deletes** those rather than
substituting spaces, so an on-screen `1:Bad 2:Fine 3:Good 0:Dismiss` reaches a
scanner as `1:Bad2:Fine3:Good0:Dismiss`. mg-f36b is the ticket for what a
literal compare costs: the rating-dialog watcher logged **zero** dismissals
between its 2026-05-19 merge and the 2026-07-13 wedge — installed, running, and
unable to match its own marker for two months.

This check is permanently incomplete by construction. Every entry was added
after an incident and the next incident will be a prompt that is not in the
table. That is the argument for (2), not against (1).

### (2) Declared work time vs process uptime — `counter.go`

The agent's own elapsed counter, parsed out of the status line, put beside how
long its process has actually existed. On both nights the whole diagnosis was two
numbers side by side:

```
uptime   13h44m               (process table — honest)
declared "Baked for 3m 2s"    (the session — impossible)
```

**The rule gates on the counter being FROZEN, not on the ratio.** This is the
subtlety that makes the check shippable, and the thing to read before retuning
it. The declared counter measures *one turn*, not cumulative work, so a
perfectly healthy agent seven hours into its life and three seconds into a new
turn shows a ratio of 8400 — a ratio-only rule fires on every agent in the fleet
at the start of every turn, and the detector is muted within a day.

What made 13h44m beside "2m 56s" damning is that the counter did not move. Had
it been advancing it would have read 13h. Had turns been starting and finishing
it would have read a different value at every sample. One value, unchanged
across a window spanning several 10-minute mail-check fires, means the fires are
being delivered and absorbed without running anything. `ratio` (20x) and
`min_uptime` (1h) survive as guards; the freeze is the signal.

**An unparseable counter is a third answer.** It falls back to event-log
staleness, and with neither available the agent is reported *unjudgeable* — never
healthy. A harness that renames its status line must make this detector coarser,
not silent. That rule is inherited from `internal/credexpiry`'s
absence-as-evidence trap, and it is the founding convention of this family of
detectors: the fault being detected is, in every case, an instrument that read
healthy because it could not see.

## 3. Cause attribution — `classify.go`

### A 401 shortly after a connectivity failure is ONE signature

mg-fc8d was **filed** saying an interrupted `/login` revoked the OAuth token.
That was wrong. The doctor checked the credential's expiry fields (expiry only;
no token value read or printed) and found the access token valid with 7.7h
remaining, the refresh token valid for ~395h, subscription and scopes intact.
Nothing was revoked, nobody logged in, and every agent on the box resumed on the
same credential. The actual sequence:

```
network outage (ENOTFOUND, github.com:22 unreachable, ~20:20-20:38Z)
  -> an access-token REFRESH fell inside that window
  -> the refresh failed because the network was dead
  -> the session surfaced the failure as "401 ... revoked/expired"
```

This matters for three reasons, all of which are encoded in the classifier:

1. **It changes who is paged.** A revoked credential needs Daniel to re-login.
   An expired-refresh-during-outage needs nobody. Had "OAuth revoked" stood, the
   next reader would page him for a re-login that fixes nothing.
2. **It predicts recurrence, with a rate.** The access token turns over roughly
   every 8h, so there are ~3 refresh windows a day, and any network outage
   overlapping one reproduces this exactly. A standing coincidence, not a
   once-off.
3. **It merges two signatures into one.** The detector must not conclude
   "credential revoked" from a 401 alone.

The connectivity memory is **fleet-wide** rather than per-agent, because on
2026-08-04 the two halves of the evidence arrived through different observers —
mayor read the 401 in a PTY, the doctor read ENOTFOUND in the logs — which is
precisely how one event came to be recorded as two.

The coincidence window (2h) is asymmetric on purpose. Too short and a merged
event is reported as a poisoned credential, which pages a human for a re-login
that fixes nothing. Too long and a genuine revocation is reported as an outage
artifact, whose handling is "wait for the network" — which fails visibly,
immediately, and wakes nobody. Prefer long.

### Opposite responses, and UNKNOWN rather than a guess

| Evidence | Cause | Response |
|---|---|---|
| 401 **and** connectivity failure within the window | `refresh_failed_during_outage` | `await_network_recovery` |
| 401, no connectivity evidence, refresh grant **lapsed** | `poisoned_credential` | `stop_and_redispatch` |
| 401, no connectivity evidence, credential **readable and in date** | `unknown` | `investigate` |
| 401, no connectivity evidence, credential **unreadable** | `unknown` | `investigate` |
| connectivity failure, no auth symptom | `network_down` | `await_network_recovery` |
| rating dialog / rate-limit modal | `modal_wedge` | mg-4421 owns it |
| frozen counter, nothing enumerated, host **saturated** | `host_oversubscribed` | `reduce_load_do_not_intervene` |
| frozen counter, nothing enumerated, host **unmeasurable** | `unknown` | `investigate` |
| frozen counter, nothing enumerated, host has **headroom** | `unknown` | `investigate` |

The first two rows need **opposite** handling — leave alone versus stop and
re-dispatch — so a guess between them is worse than a shrug, and the middle rows
say UNKNOWN rather than choose. A bad credential is named only on the
credential's own evidence, and only from the **refresh-grant** expiry: the
8-hour access-token expiry is routinely in the past on a healthy machine because
the harness re-mints on demand without rewriting the stored blob, and reading it
would have manufactured exactly the false verdict this design refuses to give.

### No intervention is named, because none is established

An early reading of 2026-08-05 held that a nudge revived the fleet. mayor
retracted it with a control on the same day:

```
nudges sent during the outage window (10:23Z-17:26Z):  968
acks produced by those 968 nudges:                       0
acks in the 90s after the network returned:             15
```

968 attempts, zero revivals — and `crew-doctor`, which was not in the
15-immediate-nudge batch and received no immediate nudge at all, woke anyway on
an ordinary scheduled fire ten minutes later. So a nudge is neither sufficient
nor necessary. What changed at ~17:26 was the **environment**: the network came
back, and ordinary nudges started working again because everything did. Fifteen
nudges sent seconds before the first ack made a coincidence look like a
mechanism.

The detector therefore names a **recovery condition** (connectivity returning)
and no remedy. Shipping "detect ENOTFOUND → nudge" would be worse than shipping
nothing: it would be trusted, and it would be 968-for-0.

## 3a. The third false-healthy state: CPU starvation

There are **three** states that look identical to every instrument this fleet
has, not two:

```
WEDGED at a dead prompt  -> spinner redraws, last-activity "just now", no progress
CPU-STARVED              -> genuinely working,  last-activity "just now", no progress
HEALTHY and working      -> last-activity "just now", progress
```

The counter cross-check separates the first from the third, and *mostly*
separates the second too — a starved agent's counter advances honestly, because
it really has been working for forty minutes; it has just achieved almost
nothing. But a starved agent **between turns** has a frozen counter for the same
reason a wedged one does. On 2026-08-05 pm-onethird watched thirteen polecats
sit at `last-activity: just now` for hours during a load event (1-minute average
300 on a 10-core box) with plain local `git log --oneline -2` calls timing out at
120s and then 180s.

**The remedies are opposite again.** A wedged agent needs intervention; a
starved one needs to be **left alone** and the load reduced. Waking or
restarting a starved agent destroys real work and adds to the load that caused
the symptom.

So when the **only** evidence is a frozen counter — nothing enumerated on
screen, nothing the host's CPU could not explain — and the host is measurably
saturated, the verdict is `host_oversubscribed` /
`reduce_load_do_not_intervene`: *degraded, not wedged*. Saturation does **not**
reinterpret an enumerated finding; a login prompt is not caused by CPU
contention, and letting a load spike excuse a real auth wedge would give the
thirteen-hour case an alibi.

**The instrument is deliberately not the load average**, which is the number the
incident was reported in. `internal/hostload` disqualified it with a measurement
on this very box (mg-1b8c): a load average of 214 coincided with roughly 7.5 of
10 cores actually in use, because Darwin's load average counts
uninterruptible-sleep tasks as well as runnable ones — and part of what it
counted (a VPN extension at ~0.9 cores, the system indexer at ~0.3) was not the
fleet's work at all. Keying on it would report a full box whenever something was
doing heavy I/O, which excuses a real wedge. The number decided on is **used
cores against core count**, at hostload's own `SaturatedAt` threshold. That
measure's own limit is inherited and stated: consumed CPU is bounded by the core
count, so it detects "the host is full" and cannot say how far past full.

**An unmeasurable host is not an idle one.** Below its source's resolution
hostload's differenced figure is zero for a saturated host exactly as much as
for an idle one (`Sample.Unresolvable`, mg-79e3). That case reports
`unknown` / `investigate` with the reasoning saying starvation *could not be
ruled out* — never a wedge verdict reached as though the host had been checked.

This is a state pogo **creates for itself**: the 2026-08-05 event was seven
polecats in one Go repo each running `build.sh`, which runs the full test suite
twice (filed as mg-3977, per-repo dispatch cap, and mg-da30, double test run).
That is an argument for measuring it rather than assuming it away.

## 4. Proving it can fire

A check like this must be proven able to **fire** before it is trusted to stay
quiet, because its normal output is silence and silence is exactly what the
fault produces. `internal/wedgewatch` carries a positive control for every state
it claims to detect, built from the terminals the strings were read off:

- each enumerated marker, including through cursor-move column spacing
- both incidents' exact numbers through the cross-check
- the **un-enumerated** case: a prompt not in the table, caught by the
  cross-check alone
- the event-log fallback **timing a marker's hold-down** when the counter
  cannot be parsed — which is the whole of what it may do; see below
- **BLIND**, the state that says "I could not judge this agent"
- the split-observation case: one agent's ENOTFOUND explaining another's 401
- the starved agent, reported as `host_oversubscribed` and **not** as a wedge —
  plus the precedence control that a saturated host does not excuse a login
  prompt, and the blind-host control that an unmeasurable box says so

The negative controls are what make the silence mean something: a healthy agent
whose counter advances is never reported across six simulated hours, and neither
is an agent **merely writing about the wedge** — not hypothetical, since the
polecat that built this package had every enumerated marker in its own PTY for
hours. That case is why the marker hold-down is not zero.

### What the fallback may and may not decide (drellem2/pogo#138)

The event-log fallback **times**; it does not **judge**. It supplies an age, and
an age is only meaningful next to evidence that the agent ought to be producing
something — which the marker supplies and the fallback cannot. So an agent whose
counter cannot be parsed and which shows no marker is **BLIND**, not healthy:
event-log silence alone cannot separate a wedged agent from an idle one.

That distinction was lost once, and the loss is worth recording here because
this section is where the next person will look. mg-20eb wired the fallback so a
stall clock could be established without a counter, and the blind branch keyed
on "is there a clock" rather than on "can this be judged" — so the answer space
went from {healthy, stalled, blind} to {healthy, stalled}, and *"I cannot judge
this"* collapsed into *"healthy"* at any staleness. A detector that cannot say
it is blind reports green from its blind spot.

Two consequences for anyone reading this section as a checklist:

- **A positive control for BLIND is not optional.** It is the one state whose
  absence looks exactly like a clean fleet, so it is the one most likely to be
  removed by someone tidying up an error burst.
- **`wedge_watch_error` going quiet is not evidence of health** unless the blind
  branch is reachable. Between mg-20eb and drellem2/pogo#138 the count was
  guaranteed zero by construction. An instrument that cannot go red is not
  green; check reachability before quoting the count.

Relatedly, the fallback's recency index counts only events the **agent** wrote.
pogod records some of its interventions under the identity of the agent it acted
*on* — `modal_dismissed` is emitted under the dismissed agent — and those fire
*because* the agent is in trouble, so counting them makes the reading freshest
when the subject is worst off. `events.CountsAsAgentActivity` is the shared
predicate that excludes them, applied by every last-seen index over the log.

## 5. Item (3): escalating outside the wedged party — OPEN

mg-fc8d's third item is the one that actually bounds the damage:

> When N agents are simultaneously idle-but-animating, that is a fleet-level
> event and it must reach Daniel, not just mayor's inbox. Thirteen hours of
> correct alarms went to a recipient who could not act on them.

**It is deliberately not built, and this document does not propose a design for
it.** It is an alerting-policy decision reserved to Daniel and he has not ruled.
`internal/wedgewatch` therefore holds **no mail seam at all** — no `NotifyTo`, no
`EscalateTo`, no `MailFunc` — so that adding one is a decision somebody makes on
purpose rather than a default somebody inherits.
`TestTheWatcherHoldsNoMailSeam` pins that.

**The assumption this leaves standing, stated rather than chosen:** something
must consume these findings, or the detector reproduces the fault it was built
for one level up. Today the consumers are the `wedge_watch_fired` event and
pogod's log, and every emission carries a `routed_to` field saying in as many
words that nobody was told — so a reader who finds one does not assume somebody
else already has it.

Whoever rules on item (3) will need to decide at least:

- **Recipient.** `human` mail (which the apple-side notifier picks up) versus
  something out-of-band. Note that `internal/deafwatch` already has a precedent
  for the sub-case where the coordinator is itself the patient: it escalates
  immediately rather than after an age threshold, because routing a notice only
  to the patient is provably no notice at all.
- **Threshold.** Whether one wedged agent pages, or only a fleet-level
  simultaneity (N agents at once, which is what both incidents looked like).
- **Rate limiting.** The failure mode on the other side is a detector that pages
  often enough to be filtered, which is how stall-watch's thirteen hours of
  correct alarms became background noise.

## 6. Relationship to neighbouring detectors

- **The modal watcher (mg-4421, `internal/claude/modal_hook.go`)** *dismisses*
  the two enumerated Claude Code modals from inside the PTY stream. wedge-watch
  reports that one is still up beside a stalled agent — i.e. that the dismissal
  did not win, which is worth knowing because mg-f36b is a ticket about that
  watcher being silently unable to match for two months.
- **deaf-watch (mg-032b)** catches an agent with no way to be *woken*.
  wedge-watch catches one that is being woken and absorbing it. Disjoint.
- **ack-watch** catches schedules that are delivered but not completed, by rate.
  wedge-watch reaches the same conclusion from the agent's own screen, which is
  faster and needs no counter history.
- **credexpiry (mg-ed45)** *predicts* the scheduled 30-day refresh-grant lapse.
  wedge-watch consults the same field reactively, and only to refute or confirm
  a credential hypothesis — never to warn.
- **hostload (mg-1b8c)** answers "would pausing *our own* work help", which is
  what dispatch needs. wedge-watch asks the different question "is there CPU for
  this agent to make progress with", so it reads the **whole host** rather than
  the fleet's share: an agent starved by somebody else's compiler is just as
  starved. Same package, same threshold, different denominator, on purpose.
