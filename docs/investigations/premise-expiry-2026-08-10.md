# Expired premises: how often a cited ticket's behaviour changes under the ticket citing it

**Work item:** mg-027b · **Date:** 2026-08-10 · **Instrument:** [`scripts/premise-expiry-rate.sh`](../../scripts/premise-expiry-rate.sh)

## The question

architect proposed a mechanism after mg-0466 and mg-24dc nearly shipped against
each other's expired premises in the same hour:

> A ticket that cites another ticket's behaviour **records the SHA it was priced
> against**, and re-reading is **triggered when that ticket merges** — not when it
> is reviewed.

architect framed it as a candidate, not a request. mg-027b asked three things
before anything got built: is the SHA field cheap enough or does it rot like
every hand-maintained cross-reference; what concretely triggers the re-read and
which **actor** does it reach; and is the general case worth solving at all, or
is the honest scope narrower — *two tickets in flight simultaneously in the same
repo*, which is where both instances occurred.

The instruction was explicit that the third question was the deliverable, and
that "twice in one hour on one night is not a rate."

## Answer, up front

**Do not build it.** And the proposed narrowing — two tickets in flight
simultaneously — is **the wrong narrowing**: it excludes the instance that
motivated the proposal. That is the substantive finding, and it is the kind that
only shows up if the rate is measured rather than remembered.

## What the motivating night actually looks like when you put times on it

Reconstructed from `~/.macguffin/events.jsonl`, the maildirs of `i0466`,
`pm-pogo` and `architect`, and the macguffin git history. All times UTC.

| Time | Event |
|---|---|
| 22:45:08 | mayor's last pricing edit to **mg-0466**'s body: *"RELATED WORK IN FLIGHT: mg-24dc is currently building the other half of this… if it succeeds the 'dev' population shrinks and the refusal's blast radius changes."* |
| 22:46:22 | **mg-24dc merges** (`91f6dba`, macguffin `main`) — **74 seconds after the premise was written** |
| 22:51:31 | mg-24dc's second commit merges (`f917d16`) — the one architect later verified |
| 23:27:51 | mg-24dc's *work item* is marked done, 36 minutes after the merge |
| 23:28:50 | **mg-0466 is claimed.** Its premise is already 37 minutes stale at dispatch |
| 23:45:43 | polecat `i0466` lands its main fix (`5292c91`) |
| 23:52:39 | **pm-pogo mails a refinement priced on the expired world** — 61 minutes after the merge |
| 23:54:15 | architect catches it, verifying `git merge-base --is-ancestor f917d16 main` |
| 23:58:11 | polecat implements the refinement anyway (`701b222`); the mails had crossed |
| 00:02:17 | pm-pogo reverses, naming the input that moved |
| 00:02:43 | architect **withdraws its own reversal** on separate grounds |
| 00:05:29 | `0cabf73` reverts `701b222` |

**The two tickets were never in flight simultaneously.** mg-24dc merged 37
minutes before mg-0466 was claimed (42 for its first commit), and its work item
closed 59 seconds before the claim — which is the only reason the pair looks
adjacent in the event log at all. A trigger watching for *"a ticket I cite merges while I am in review"* would have fired
into an item sitting in `available/` with no worker attached to it.

**The second of the two instances was never in a ticket at all.** pm-pogo's
refinement — the thing that actually cost a build-and-revert — lived in a review
**mail**, written 61 minutes after the merge that invalidated it. No field on any
work item can see that, so "twice in one hour" is really *once in a ticket body,
once in a mail*, and only the first is even addressable by the proposal.

**Total cost of the episode:** four review mails, and one commit implemented at
23:58:11 and reverted at 00:05:29. Seven minutes of code churn. That is the
ceiling on what any mechanism here can save.

## The rate

`scripts/premise-expiry-rate.sh` prices four candidate triggers over the whole
store. Run at **2026-08-10T08:02:51Z**:

```
store:    /Users/daniel/.macguffin
corpus:   1843 items in the event log, 2544 bodies on disk — coverage 99.4%
window:   73 days on which at least one item was claimed

trigger                        fires   per day   phase split
cites-any/claimed                259      3.55   claimed=259
cites-any/pre-dispatch           582      7.97   pre-dispatch=582
declared-live                    358      4.90   claimed=85, pre-dispatch=273
declared-live+consequence         67      0.92   claimed=9, pre-dispatch=58
```

| Trigger | Fires | Per active day | Phase split | Sees the motivating instance? |
|---|---|---|---|---|
| `cites-any/claimed` — architect's proposal as stated | 259 | 3.55 | claimed 259 | **NO** |
| `cites-any/pre-dispatch` — cited item finishes between pricing and dispatch | 582 | 7.97 | pre-dispatch 582 | yes |
| `declared-live` — the prose asserts the cited item is live | 358 | 4.90 | pre-dispatch 273, claimed 85 | yes |
| `declared-live+consequence` — …and names what changes if it lands | 67 | 0.92 | pre-dispatch 58, claimed 9 | yes |

**These numbers move, and that is not a flaw in them.** Two runs minutes apart
while this document was being written differed by one fire in two populations and
0.1 point of coverage, because the store is live and bodies are written and swept
while it is read. The instrument is the authority here, not this table — which is the
same reason the finding argues against a recorded field in the first place.

Two things fall out of that table.

**The exposure is overwhelmingly pre-dispatch, not in-review.** In the tightest
population, 58 of 67 fires (87%) land in the window between a ticket being priced
and a worker picking it up. Architect's proposal watches the other 13%. This is
not a marginal misallocation — the observed instance is in the 87%.

That share is a **floor**, not an estimate. The event log has no merge event, so
the instrument uses `work.done` as its proxy, and `done` *lags* the merge (36
minutes on the motivating night). A late proxy can only move a fire from
pre-dispatch into claimed, never the reverse, so the true pre-dispatch share is
at least what is printed.

**The precision is bad in a way that does not improve with tuning.** Classifying
the citation context of all 259 `cites-any/claimed` fires by hand-checked regex:

| Context of the citation | Share |
|---|---|
| bare mention | 44% |
| provenance / archaeology ("successor of", "same class as", "compare") | 17% |
| mixed | 16% |
| frontmatter tag (`successor:mg-…`, `mg-…-followup`) | 11% |
| premise-shaped | **11%** |

And the fan-out is structural rather than incidental: one merge of `mg-853a`
produces six notices, `mg-b201`, `mg-0155`, `mg-6d2f` and `mg-9adf` five each.
They come from a single *"Already claimed and in flight, do NOT duplicate: mg-853a,
mg-b201, mg-0155, mg-5bd2, …"* block copy-pasted into sibling dispatch bodies —
an inventory of live work whose expiry is **the desired outcome**. The dominant
declared-live construct in this corpus is the one construct where going stale is
correct.

So the honest cost/benefit: **3.5–8 notices a day, at roughly 1-in-9 precision,
against a failure that has been observed once in a ticket, cost four mails and a
seven-minute revert, and was caught both times by a reviewer reading carefully.**

## The three determinations

**1. Is the SHA-recording cheap enough, or does it rot?** *No field is needed,
and one would rot.* The premise in the motivating instance was already declared,
in ordinary prose, in the ticket body: **"RELATED WORK IN FLIGHT: mg-24dc is
currently building the other half of this."** A regex over that sentence finds it
— that is exactly how the `declared-live` population is built, and the instance
is in it. A `priced-against: mg-24dc@<sha>` field would have added nothing the
sentence did not carry, and would have been a hand-maintained cross-reference on
a body that four different agents edited seven times that night. The ticket's own
warning applies to it: *a field nobody updates is worse than nothing, because it
looks authoritative.*

**2. What triggers the re-read, and which actor does it reach?** *At merge, the
answer is nobody.* When mg-24dc merged (22:46:22 and 22:51:31), mg-0466 was
sitting unclaimed in `available/`. The
only addressable actor was its **assignee**, `pm-pogo` — which is precisely the
actor that then wrote the expired-premise mail 61 minutes later. So a merge-time
mail could have worked, but only by reaching an item's assignee rather than "the
citing ticket's worker", because for 87% of the exposure no worker exists yet.
The moment with a **guaranteed** reader is **dispatch**: a polecat is about to
read the entire body anyway, and a line in the prompt costs an interrupt from
nobody.

**3. Is the general case worth solving, or is the honest scope narrower?**
*Neither.* The general case is not worth solving at the measured rate and
precision. The proposed narrowing is worse than not narrowing, because it is
narrow in the wrong direction: it keeps the 13% of exposure that has a worker
attached and discards the 87% that does not, including the case it was designed
from. The only scope with a defensible rate is `declared-live+consequence` at
0.92/day — and 87% of *that* is pre-dispatch, so it is not a review-time
mechanism at all.

## If it is ever built anyway

Not a recommendation — a record of the shape the measurement supports, so the
next person does not rebuild the version this document rejects:

- Fire at **dispatch**, not at merge. Same information, a guaranteed reader, no
  interrupt, and it covers the 87%.
- Detect the premise in **prose**, not in a declared field. Nothing to maintain
  and nothing to rot; the corpus already writes "in flight" when it means it.
- Require a **consequence clause**, and exclude do-not-duplicate inventories.
  That is the difference between 8 notices a day and 0.92.
- Say what it is: *"mg-24dc, which this body says is in flight, finished 37
  minutes before you were dispatched."* A line of fact. Not a blocker, not a tag,
  not a re-review.

The polecat prompt already carries the general form of this instruction — *verify
"not implemented" claims before acting on them* — and this would be the same rule
pointed at sibling tickets rather than at code.

## Limitations, stated rather than implied

- **`work.done` is a proxy for merge.** mg's event log records no merge. The bias
  is directional and named above: it can only understate the pre-dispatch share.
- **The populations are regex-defined.** `declared-live` and `+consequence` are
  the phrases this corpus uses; another corpus would need different ones. The
  regexes are in the instrument, not in this document, so they can be re-run
  rather than re-argued.
- **Precision was sampled by classification, not by adjudication.** The 11%
  premise-shaped figure counts citations whose *context reads like* a premise. No
  attempt was made to establish that any of the 259 actually misled anyone —
  which cuts the same way as everything else here: the only confirmed harm in the
  corpus is the one night this ticket came from.
- **This document is itself a ticket citing other tickets' behaviour**, which is
  the shape it is about. It is not exposed, and for the reason the instrument
  encodes: every item it cites — mg-0466, mg-24dc, mg-853a and the rest — was
  already `done` when this was written, which is the one category the measurement
  excludes from every population. The numbers are the part that expires, and they
  are handed to an instrument rather than asserted.
- **Coverage is 99.4% today and will fall as bodies are swept.** The instrument
  prints its coverage above its rates and marks them UNDERSTATED below 90%,
  because the first run of this analysis read 15% of the corpus and reported a
  rate 20× too low. An instrument for a stale-premise problem that can itself be
  read against a stale corpus is the defect it was built to price.
