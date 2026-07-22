# The 24h fleet stall was Claude credential expiry — CONFIRMED from the session transcripts

**Date:** 2026-07-22
**Work item:** mg-18d0 (investigation; recurrence of archived mg-52f9)
**Verdict:** the mayor's auth-expiry hypothesis is **CONFIRMED** by direct evidence.
The detector/remediation SPOF is **real but was not the cause**, and moving
remediation into pogod **would not have recovered this fleet**.
**Xref:** mg-52f9 (archived undone), mg-1bbf (recovery inside the failure domain),
`docs/investigations/wake-watcher-mechanism-confirmed-2026-07-21.md`

## The evidence the ticket was missing

The mayor named the gap precisely: *"I have not read any agent's PTY buffer or
Claude-side log from the window. That is the missing direct evidence and it is
where you should start."*

It is not in the PTY buffer — that is a ring and had rolled. It is in the Claude
session transcripts at `~/.claude/projects/-Users-daniel--pogo-agents-<name>/*.jsonl`,
which record every turn with a timestamp and survive indefinitely. Every failing
turn in the window carries this, verbatim:

```json
{"type":"assistant","timestamp":"2026-07-22T12:00:24.786Z",
 "message":{"model":"<synthetic>","role":"assistant",
            "content":[{"type":"text","text":"Login expired · Please run /login"}],
            "usage":{"input_tokens":0,"output_tokens":0}},
 "error":"authentication_failed","isApiErrorMessage":true}
```

`model: "<synthetic>"`, zero tokens in and out, `error: authentication_failed`.
The session never called the API. It answered locally, in ~10 ms, and moved on.

## Onset and recovery, bounded to the minute

All timestamps UTC; the host is `+01:00`, so the ticket's `2026-07-22T00:00:30`
local is `2026-07-21T23:00:30Z`.

| agent | last real model turn | first `Login expired` | count |
|---|---|---|---|
| mayor | 2026-07-21T23:01:27.858Z | 2026-07-21T23:30:24.991Z | 202 |
| pm-pogo | 2026-07-21T23:00:35.211Z | 2026-07-21T23:10:26.134Z | 143 |
| pm-dealdesk | 2026-07-21T23:00:34.644Z | 2026-07-21T23:10:26.038Z | 143 |
| pm-onethird | 2026-07-21T23:00:34.808Z | 2026-07-21T23:10:26.112Z | 143 |
| architect | 2026-07-21T23:00:32.608Z | 2026-07-21T23:10:26.368Z | 141 |
| pa | 2026-07-21T23:00:32.082Z | 2026-07-21T23:10:26.664Z | 142 |
| doctor | 2026-07-21T14:34:33.618Z | *(none — no schedule)* | 0 |

The credential died in the **~9-minute window between 23:01:28Z and 23:10:26Z**.
Every nudged agent's first failure is the 23:10 nudge, and those six failures are
spread across **0.6 seconds** — they are six sessions independently discovering
one dead shared credential on the same scheduler tick.

Recovery is equally sharp: last synthetic turn `2026-07-22T22:30:12Z`, first real
`claude-opus-4-8` turn `2026-07-22T22:40:37Z`. **Total: 23h30m.**

`doctor` never emitted one because it had no mail-check schedule to fail — it
received no nudges at all. That independently corroborates the mayor's separate
finding, from the opposite direction.

## Three corrections to the hypothesis, and why each matters

The mayor was right about the cause and flagged themselves as an interested
party. The framing needs three fixes, and they are the load-bearing part of this
report.

### 1. The sessions were never blocked

The hypothesis said the sessions would *"block waiting on an auth prompt nobody
could answer — alive, no crash, no output, no exit."* They did not block. They
were **fully responsive for the entire 23.5 hours**, consuming every nudge at its
due second and failing it in ~10 ms.

The arithmetic settles it: 23h30m of `*/10` nudges is ~141 fires, and pm-pogo,
pm-dealdesk and pm-onethird each recorded exactly **143** failed turns. One
failure per fire, none missed, none queued. A sample fire consumed on time:

```
due=2026-07-22T13:00:00+01:00  fired=13:00:24+01:00  consumed=12:00:24.753Z (=13:00:24 local)
```

So the report that *"my own nudges arrived as one large batch when the session
finally drained"* is **wrong**. Nothing drained, because nothing had queued. This
matters because a backlog model suggests the work was merely deferred; in fact
**143 nudges per agent were destroyed, not delayed** — consumed, failed, and
discarded. The day's scheduled work is gone, not late.

### 2. The failure was machine-readable, on local disk, the entire time

This is the useful finding. `"error":"authentication_failed"` was written to
disk on every one of those turns, within milliseconds of each one, in a file
whose path is derivable from the agent's cwd. Nobody had to infer anything.

That is precisely the signal the mayor said a complete fix requires — the one
that separates *"this agent is wedged"* from *"every agent is dead at once"* —
and it needs no new detection infrastructure, only a reader. Note the asymmetry
that makes it decisive: a genuinely wedged agent writes **nothing** to its
transcript, while an auth-dead agent writes a **new turn every nudge**. The two
failure modes are not merely distinguishable, they are opposites at the file
level.

**Caveat, stated plainly:** this file's path and schema are Claude Code harness
internals, not a contract pogo owns. mg-5a06 already established that the harness
memory root is provider-declared rather than hard-coded; anything built on this
signal needs the same treatment, and must degrade to today's behaviour when the
file is absent or its shape changes.

### 3. This is chronic, and much larger than auth

Auth expiry is one member of a family. Counting every synthetic zero-token turn
across all crew transcripts, all time:

| synthetic turn | count |
|---|---|
| `API Error: Server is temporarily limiting requests` | 2818 |
| `Login expired · Please run /login` | 914 |
| `Please run /login · API Error: N Invalid authentication cr…` | 498 |
| `You've hit your weekly limit · resets …` | 885 |
| `You've hit your monthly spend limit` | 280 |
| server-side 5xx / socket / parse / too-long | ~134 |

**~5500 nudges** in this fleet's history have been delivered, consumed, and
accomplished nothing. Yesterday's incident is the largest contiguous run, not a
new phenomenon.

## What this does to the ticket's proposed fix

The detector/remediation split is real: detection lives in pogod and survives,
remediation lives in mayor and does not. That is a genuine SPOF and worth fixing
on its own merits.

But it was **not the cause here, and fixing it alone would have made this
incident worse.** Restarting a Claude session against a dead credential yields
another session that fails the same way — the mayor predicted this and the
evidence now supports it concretely. At `T_restart = 120 min` over 23.5 hours,
pogod would have run roughly **11 restart rounds across 6 agents ≈ 66 restarts**,
each discarding a live session's accumulated context (pm-pogo's held **2339
messages** at the time of failure) and none of them recovering anything. It would
have destroyed the transcripts that made this diagnosis possible while producing
an events.log full of remediation activity.

**So: necessary, not sufficient — and it must be gated.** The ordering matters.
A fleet-wide simultaneous failure has one upstream cause and **only a human can
clear it**; it should page, never restart. Per-agent restart is correct only once
the fleet-wide case is excluded.

## The trap this incident actually sets

The ticket warns that *"add more detection is the tempting wrong answer"* — and
that is right for the stall itself. Detection worked: sweep.log mtimes told the
true story all day, and `stall_watch_fired` fired 124 times correctly.

But there is a second-order version of the same trap, and it is the one to name.
`scheduler_fire_delivered` counted **647 successful deliveries** during a period
when essentially none of them did anything. That counter measures *delivery*, not
*completion* — and with no completion signal, a fleet that is 100% dead is
indistinguishable from a fleet that is 100% healthy in the events log. `nudge_sent`
771 and `scheduler_fire_delivered` 647 were all **true and all useless**.

That is why this survived twice. The instrumentation was not silent; it was
confidently reporting success about the half of the transaction it could see.

## Bounds — what this does not establish

- **Why the credential expired** is not established. `~/.claude.json` mtime is
  `2026-07-22T23:47:38` local, consistent with the recovery `/login`, and says
  nothing about the onset. Whether this was a scheduled token lifetime, a
  server-side revocation, or a refresh that failed is **unknown and worth its own
  ticket** — a recurrence is otherwise a matter of time.
- **Claude Code auto-updated 2.1.217 → 2.1.218 at 2026-07-22T22:36:10Z**, between
  the last failed turn and the first good one. It is *temporally* inside the
  recovery window. I did **not** establish whether it contributed to recovery or
  merely coincided with the human's `/login`; the sessions were running 2.1.215
  throughout the failure, so the update did not cause the onset.
- **pogod saw nothing, and could not have.** The 188 auth-ish strings in
  `pogod.log` are all `GH_TOKEN` messages from unrelated PR teardown paths. The
  mayor correctly declined to count pogod's silence as exculpatory; it is neither
  evidence for nor against, because pogod does not observe inside a child Claude
  process. It would have to read the transcript, which it does not do today.
- **Whether the SPOF fix is worth building as specified** is not decided here.
  This report is fact-finding; the reframing above belongs to the mayor.

## Recommendation

1. **Read the transcript for `authentication_failed`** before any restart
   decision. Cheap, local, already-written, and it distinguishes the two failure
   modes as opposites rather than shades.
2. **Gate remediation on fleet-wide vs single-agent.** N agents failing within
   seconds is one upstream cause → mail `human`, do not restart. One agent stale
   while others are healthy → the existing restart is appropriate.
3. **Bound the escalation**, as the ticket says. 124 identical fires is a
   detector with no escalation path. Keep emitting — the record is why this was
   diagnosable — but act or page after N.
4. **Give `scheduler_fire_delivered` a completion counterpart.** Delivery without
   completion is what made a fully dead fleet look healthy in the events log.

**Do not** put the remediation in a crew agent. That part of the ticket stands
unchanged.
