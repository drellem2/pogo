- **pogod now detects an agent that is ANIMATING BUT NOT WORKING (mg-fc8d).** New
  `[wedge_watch]` section and `internal/wedgewatch`: on every heartbeat pogod
  reads each agent's PTY for known dead-end states, and cross-checks each agent's
  own declared work counter against its process uptime. **Report-only, and
  deliberately unrouted** — see the last bullet.

  **What it is for.** On 2026-08-04 twelve polecats and the doctor crew agent sat
  at a Claude Code login prompt for **thirteen hours**; on 2026-08-05 it recurred
  for seven. About twenty agent-hours of nothing, and every liveness instrument
  pogo has read healthy throughout — `status=running uptime=13h44m
  last-activity=just now`, for all twelve, simultaneously, the whole time. The
  agents were not frozen, they were animating: Claude Code redraws a spinner
  while parked at a prompt, and that redraw is PTY output, so `last-activity`
  (PTY writes) said "just now" forever, the process was alive so status said
  running, and CPU was near zero — which is also what a legitimately blocked
  agent looks like. Every instrument was measuring the animation. It was found by
  hand, twice.

  **(1) The enumerated check.** `Please run /login`, `API Error: 401`,
  `Unable to connect to API` / `ENOTFOUND` / `EAI_AGAIN`, the rating dialog and
  the rate-limit modal. Matching is whitespace-insensitive against the
  ANSI-stripped buffer, because Claude Code spaces TUI columns with cursor-move
  escapes that `StripANSI` deletes rather than replaces — a literal compare is
  exactly how mg-f36b's watcher logged **zero** dismissals across two months
  while looking installed. `TestModalMarkersMatchTheModalWatcher` pins the two
  shared markers byte-identical to `internal/claude`'s.

  **(2) The cross-check, which is the half that matters.** (1) can only recognise
  a dead end somebody has already met, and that enumeration is permanently one
  incident behind; (2) reads the agent's own claim about how long it has been
  working and notices the claim is impossible. The live signature both nights was
  a 7h+ uptime beside a counter reading **"Baked for 2m 56s"**.

  **It gates on the counter being FROZEN, not on the ratio — read this before
  retuning it.** The naive form fires on every healthy agent in the fleet: the
  declared counter measures ONE TURN, so an agent seven hours into its life and
  three seconds into a new turn also shows a tiny counter beside a huge uptime.
  What made 13h44m beside "2m 56s" damning is that the counter did not move —
  advancing it would have read 13h; taking turns it would have read a different
  value at every sample. One value unchanged across a window spanning several
  10-minute mail-check fires means the fires are being absorbed without running
  anything. `ratio` (20x) and `min_uptime` (1h) survive as guards, not as the
  signal. `TestDiscrepancyDoesNotFireOnAHealthyAgentMidTurn` is the control.

  **A 401 shortly after a connectivity failure is ONE signature, not two.** The
  ticket was FILED blaming an interrupted `/login` for revoking the token; the
  doctor refuted it. Nothing was revoked — refresh grant good for 16.5 more days,
  subscription intact — nobody logged in, and every agent resumed on the same
  credential. A network outage swallowed an access-token refresh and the failure
  surfaced as `401 ... revoked/expired`. Concluding "revoked, page the human" from
  a 401 alone pages Daniel for a re-login **that fixes nothing**, and since the
  access token turns over about every 8h there are ~3 chances a day for any
  outage to reproduce it. So a connectivity failure observed anywhere in the
  fleet within `coincidence_window` (2h, long on purpose) merges with a later 401
  into one cause. The window is fleet-wide because on 2026-08-04 the two halves
  arrived through *different observers* — mayor read the 401 in a PTY, the doctor
  read ENOTFOUND in the logs — which is how they came to be recorded as two
  events.

  **Opposite responses, and UNKNOWN rather than a guess.** An outage-swallowed
  refresh wants the agent left alone until connectivity returns (its context is
  intact); a poisoned credential wants the opposite — stop and re-dispatch, since
  it will never resume. Because those are opposites, a guess is worse than a
  shrug: a 401 with no connectivity evidence and a credential that is *readable
  and in date* reports `unknown` / `investigate`, never revocation, because the
  credential has actively refuted it. A bad credential is named **only** on the
  credential's own evidence — the refresh-grant expiry, never the 8-hour access
  token, which was valid with 7.7h left during the incident and is routinely
  stale on a healthy machine (`internal/credexpiry`).

  **No remedy is named, because none is established.** An early reading held that
  a nudge revived the fleet on 2026-08-05; mayor retracted it with a control —
  968 nudges inside the outage window produced **0** acks, and `crew-doctor`,
  which got no immediate nudge, woke anyway on an ordinary scheduled fire ten
  minutes later. A nudge is neither sufficient nor necessary; what changed was
  the network. The detector names a *recovery condition* and no intervention.
  `TestNoVerdictPrescribesANudge` keeps it that way.

  **Proved able to fire before being trusted to stay quiet.** Every state it
  claims to detect has a positive control built from the terminals the strings
  were read off, including the un-enumerated case (a prompt not in the table,
  caught by the cross-check alone) and both incidents' exact numbers. The
  negative controls are the ones that matter for trust: a healthy agent whose
  counter advances is never reported across six simulated hours, and neither is
  an agent **merely writing about the wedge** — which is not hypothetical, since
  the polecat that built this had every enumerated marker in its own PTY for
  hours. That case is why `marker_hold_down` is not zero.

  **Blindness is loud.** A failing source emits `wedge_watch_error` and evaluates
  nothing. An unparseable work counter falls back to event-log staleness; with
  neither available the agent is reported *unjudgeable*, never healthy. A harness
  that renames its status line must make this detector coarser, not silent —
  the failure being detected is, in every case, an instrument that read healthy
  because it could not see.

  **It is NOT routed, deliberately.** mg-fc8d's item (3) — escalating a
  fleet-level wedge OUTSIDE the wedged party — is an alerting-policy decision
  reserved to Daniel and unruled, so the runner holds **no mail seam at all**;
  there is no `notify_to` to set and no recipient to get wrong.
  `TestTheWatcherHoldsNoMailSeam` pins that adding one is a decision, not a
  convenience. Findings go to `wedge_watch_fired` on the event spine and to
  pogod's log, and every emission states that nothing was routed so a reader does
  not assume somebody else was told. **This is the item that actually bounds the
  damage**: on 2026-08-04 stall-watch fired correctly every five minutes for
  thirteen hours, into an inbox belonging to an agent that was itself wedged.
