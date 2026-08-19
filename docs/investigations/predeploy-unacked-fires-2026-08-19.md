# Why mayor's predeploy fires went unacked — 2026-08-19

Work item: mg-7837 (filed by mayor as a LEAD, not a finding).

## The question

`pogo schedule list --agent mayor` read:

    predeploy-stop-noncritical-mayor   45 1 * * *   4/12   ⚠ 5 unacked
    predeploy-quiesce-mayor            30 2 * * *   1/7    ⚠ 6 unacked

Both schedules guard the pre-deploy drain, and mg-2def had measured 4 of 4
deploy failures dying at or before that drain. The mayor filed rather than
concluded, because an unacked fire has three readings with different remedies:

1. the agent was not there — the count is a symptom of an outage;
2. the agent was there and did the work and did not ack — a reporting gap;
3. the agent was there and missed the fire — the only causal reading.

The discriminator it named was `~/.pogo/agents/turnlog/mayor.log`. It bet on
reading 1.

## What was measured

Fire history, from `~/.pogo/schedules.json` (the scheduler's own record):

| schedule | created | fires | completed | last completion | streak |
|---|---|---|---|---|---|
| predeploy-quiesce-mayor | 2026-08-12T02:52+01:00 | 7 | 1 | 2026-08-13T02:41+01:00 | 6 |
| predeploy-stop-noncritical-mayor | 2026-08-07T22:46+01:00 | 12 | 4 | 2026-08-14T01:52+01:00 | 5 |

Both are daily crons, so the streaks map one-to-one onto nights: quiesce
Aug 14–19, stop Aug 15–19. Local +01:00, so the fires land at 00:45Z and 01:30Z.

Completed turns per day in `~/.pogo/agents/turnlog/`, counted with `grep -c`:

| agent | Aug 13 | Aug 14 | Aug 15 | Aug 16 | Aug 17 | Aug 18 | Aug 19 |
|---|---|---|---|---|---|---|---|
| mayor | 537 | 161 | 0 | 0 | 0 | 0 | 218 |
| architect | 215 | 66 | 0 | 0 | 0 | 0 | 96 |
| doctor | 142 | 40 | 0 | 0 | 0 | 0 | 56 |
| pa | 146 | 43 | 0 | 0 | 0 | 0 | 59 |
| pm-pogo | 147 | 39 | 0 | 0 | 0 | 0 | 60 |
| pm-onethird | 152 | 38 | 0 | 0 | 0 | 0 | 62 |
| pm-riemann | 148 | 41 | 0 | 0 | 0 | 0 | 63 |

Last turn before the gap and first turn after it, for every agent, are within
nine minutes of each other: **2026-08-14T08:23Z → 2026-08-19T06:52Z, 118h29m.**
That is the 118h blackout mg-9fc9 was filed on. It is fleet-wide, not
mayor-specific; every crew turnlog is empty across it.

On Aug 19 the fleet returned at 06:52Z, more than five hours **after** both
fires had already been delivered at 00:45Z and 01:30Z.

## Verdict on each fire

| night | stop 01:45 | quiesce 02:30 | mayor turning at fire time? | reading |
|---|---|---|---|---|
| Aug 13 | acked | acked | yes | — |
| Aug 14 | acked 00:52Z | **unacked, work DONE 01:31Z** | yes | **2** |
| Aug 15 | unacked | unacked | no (blackout) | 1 |
| Aug 16 | unacked | unacked | no | 1 |
| Aug 17 | unacked | unacked | no | 1 |
| Aug 18 | unacked | unacked | no | 1 |
| Aug 19 | unacked | unacked | no (first turn 06:53Z) | 1 |

The Aug 14 quiesce is reading 2 and the turnlog says so in words. Its 01:30Z
fire is answered at 01:31:21Z by

    2026-08-14T01:31:21Z mayor Quiesce steps 0, 2, 3 and 5 done — CI green on
    the deploy commit, queue empty, zero polecats.

and the stop fire an hour earlier is answered at 00:47:47Z and acked at
00:52:35Z ("Predeploy drain complete: seven polecats stopped, zero running,
zero orphan schedules, acked").

**Reading 3 — mayor up and missing the fire — accounts for 0 of the 11 unacked
fires.** The mayor's bet was right, and its own caution was right: the unacked
count is a symptom of the blackout and explains nothing about the deploy
failures. It does not displace mg-fa05's DNS/DHCP root cause and is not a
second factor.

## The instrument defect this exposes

pogod delivered all 7 quiesce fires and all 12 stop fires. `fires_delivered`
counts bytes handed over; nothing on the schedule record notices that no
consumer existed for five of those nights, and the `⚠ N unacked` marker reads
identically whether the fleet was dead or an agent was dropping acks. The
`pogo schedule completion` help text asserted the opposite in as many words —
"the number that separates a busy agent from a dead one is the unacked streak"
— and that sentence is what made the mayor's reading a reasonable one.

Fixed under this ticket:

- `internal/turnlog.Window` counts completed turns in an explicit past
  interval. `LastIn` reads only the file tail and answers an ongoing-liveness
  question; this one is historical.
- `pogo schedule list` follows every `⚠ N unacked` with what that agent's
  turnlog says about the newest fire: turns inside the 3h after it (the
  measured floor for turn spacing, `turnlog.DefaultMaxAge`), turns since, and
  explicit UNAVAILABLE when there is no readable turnlog. Against the live
  daemon both mayor rows now read `delivered into silence`.
- The false sentence in `pogo schedule completion --help` is corrected, and
  the help text moved out of the cobra literal into a const a test can read —
  which is the mechanism by which it survived.

Not done here, deliberately: recording consumer liveness at DELIVERY time in
the scheduler record. That is the version of this that would work with no
turnlog and no CLI reader, and it changes the daemon. It is a separate ticket.
