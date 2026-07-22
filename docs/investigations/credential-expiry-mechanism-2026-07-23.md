# The credential expired because a 30-day OAuth grant ran out — and the next one is already dated

**Date:** 2026-07-23
**Work item:** mg-ed45 (investigation; follows mg-18d0)
**Verdict:** the mechanism is **ESTABLISHED, and expiry is PREDICTABLE**. The
credential carries `expiresAt` and `refreshTokenExpiresAt` on local disk. The
`/login` restored service; the 2.1.217→2.1.218 auto-update is **excluded** — it
ran 5 minutes *after* failures had already stopped.
**Xref:** mg-18d0 (`docs/investigations/fleet-auth-expiry-2026-07-22.md`),
mg-8cdb (reactive detector).

mg-18d0 closed with *"why the credential expired is not established… a
recurrence is otherwise a matter of time."* It is now established, and the
recurrence has a date.

## The answer in one line

The OAuth **refresh grant has a hard 30-day life from the moment it is minted**;
it is not extended by use. When it lapsed at `2026-07-21T15:17:25Z` the harness
could no longer mint access tokens, and the fleet coasted on its **final 8-hour
access token** until that died at `23:0x` — which is the outage.

## What the credential actually contains

Claude Code stores no `~/.claude/.credentials.json` on this machine; the
credential is a macOS Keychain generic-password item, `Claude Code-credentials`.
Its **attributes** are readable without unlocking the secret:

```
cdat  "20260621151725Z"   item created   2026-06-21T15:17:25Z
mdat  "20260722223133Z"   last written   2026-07-22T22:31:33Z
```

The blob itself is JSON. Reading **only** the two expiry integers and the
non-secret descriptors — never the tokens:

| field | value |
|---|---|
| `expiresAt` | `2026-07-23T06:31:32.920Z` |
| `refreshTokenExpiresAt` | `2026-08-21T21:31:50.920Z` |
| `subscriptionType` | `max` |
| `scopes` | `user:file_upload, user:inference, user:mcp_servers, user:profile, user:sessions:claude_code` |

Two lifetimes fall straight out, and both are round numbers:

```
access token :  expiresAt            - mdat        = 7:59:59.920   -> 8 hours
refresh grant:  refreshTokenExpiresAt - 30d        = 2026-07-22T21:31:50.920Z (the /login)
```

The harness also carries `token_refresh_buffer_ms = 600000` in its cached
feature config — it refreshes when **10 minutes** of access-token life remain.

**This is the finding that matters: the expiry is a plain integer on local
disk, knowable weeks ahead, needing no network call and no new infrastructure.**

## Why it died at 23:0x specifically

The prior grant was minted when the keychain item was created,
`2026-06-21T15:17:25Z` — which is itself **14 seconds after the last failing
turn of the previous outage**, i.e. the `/login` that ended it. Apply the two
measured lifetimes:

```
grant minted                 2026-06-21T15:17:25Z
grant + 30d, refresh dies    2026-07-21T15:17:25Z   <- no further access tokens mintable
+ up to 8h of the last one   2026-07-21T23:17:25Z   <- hard latest possible death
OBSERVED death window        2026-07-21T23:01:28Z .. 23:10:26Z   (mg-18d0's bound)
```

The observed window sits **inside** the predicted one, 7–16 minutes short of the
ceiling. That residual is itself explained: the last successful refresh must
have happened shortly before `15:17:25Z`, so its 8-hour token expired shortly
before `23:17:25Z`. mayor's last real turn at `23:01:27.858Z` implies a mint at
`≈15:01:28Z` — 16 minutes before the grant lapsed, consistent with the 10-minute
refresh buffer plus one nudge interval.

Nothing here is a revocation, a clock event, or a server-side change. **It is
the ordinary designed lifetime of the grant, reached.**

## Auth expiry is periodic, not chronic — a correction to mg-18d0

mg-18d0 read `914 login-expired` + `498 invalid-credential` as evidence that
this is *"chronic, not novel… the longest contiguous run, not the first."*
Clustering every genuine synthetic auth turn in fleet history (gap > 60 min)
shows something sharper:

| onset | end | duration | turns | gap since prev onset |
|---|---|---|---|---|
| 2026-06-20T02:10:48Z | 2026-06-21T15:17:11Z | 1d 13h 06m | 498 | — |
| 2026-07-21T23:10:26Z | 2026-07-22T22:31:05Z | 23h 20m | 914 | **31d 20h 59m** |

**There have been exactly two auth outages, ever.** And the two counts mg-18d0
read as two chronic error *families* are simply the two *episodes* — the split
is perfectly clean:

```
2026-06 outage:  498 INVALID_CRED    0 LOGIN_EXPIRED
2026-07 outage:    0 INVALID_CRED  914 LOGIN_EXPIRED
```

Each outage produced one string and only one. The 30-day cycle is visible in the
onsets: outage 2 begins `30d 7h 53m` after outage 1's recovery — that is
`30d` of grant plus the 8-hour access-token tail, exactly.

The rest of mg-18d0's chronic finding **stands unchanged**: rate-limit (2818),
weekly-limit (885) and spend-limit (280) synthetic turns are genuinely spread
across history. Only the *auth* member of that family is periodic. That
distinction is load-bearing, because a periodic fault can be **predicted** and a
chronic one can only be detected.

### A methodological note worth keeping

The first pass at this clustering reported a spurious third cluster at
`22:50–23:05Z` on 2026-07-22, after recovery. It was contamination: a plain
`grep` for the error strings matches the transcripts of the polecats
*investigating the outage* — including this one — because they quote the text.
Filtering to genuine turns (`isApiErrorMessage == true` **and**
`message.model == "<synthetic>"`) removed it and yielded **1412** turns, which
reproduces mg-18d0's `914 + 498` exactly. **Any future detector that greps
transcripts for these strings will detect itself**, and every agent that reads
this report.

## What restored it: the `/login`. The version bump is excluded.

mg-18d0 left this open because the auto-update sits inside the recovery window.
The sequence, at second resolution, closes it:

| time (UTC) | event | source |
|---|---|---|
| 22:30:12.514 | six agents fail the 22:30 nudge | transcripts |
| **22:31:05.395** | **last failing turn of the entire outage** | transcripts |
| 22:31:32.920 | new access token minted, keychain rewritten (`mdat`) | keychain + `expiresAt`−8h |
| **22:36:10.655** | **auto-update 2.1.217 → 2.1.218** | `~/.claude/.last-update-result.json` |
| 22:40:37 | first real `claude-opus-4-8` turn | mg-18d0 |

Failures **stopped 5 minutes and 5 seconds before the update ran**, and 27
seconds before the credential was rewritten. The update cannot have restored
something that had already stopped failing. Recovery is fully explained by the
`/login` at `21:31:50.920Z` (derived: `refreshTokenExpiresAt − 30d`) minting a
fresh grant, followed by the routine access-token refresh an hour later.

The one-hour lag between the `/login` and the last failure is worth naming:
**a running session does not pick up a new credential immediately.** The grant
existed from `21:31:50Z`, yet agents kept failing until `22:31:05Z` — they
recovered only when the harness performed its next refresh. So even a perfectly
timed human `/login` leaves the fleet dead for up to an access-token refresh
interval.

Symmetry check: outage 1 ended `14s` before the keychain item was created; outage
2 ended `27s` before it was rewritten. **Both outages terminate within half a
minute of a credential write, and neither near a version change.**

## Observed vs. inferred

Stated plainly, because the ticket asked for it.

**Observed directly:**
- Both lifetimes, from the live credential's own fields: access **8h**, refresh
  grant **30d**. Not inferred — arithmetic on two integers.
- `cdat` / `mdat`, and that both outages end within 30s of a credential write.
- The full outage census: two episodes, one error string each, 1412 turns.
- The update ran 5m05s after the last failure.
- `token_refresh_buffer_ms = 600000`.

**Inferred (sound, but not directly observed):**
- That the **prior** grant was minted at `cdat` and therefore died at
  `cdat + 30d`. Its `refreshTokenExpiresAt` was overwritten on 2026-07-22 and
  cannot be recovered. A `/login` between 2026-06-21 and 2026-07-21 would have
  reset the clock and moved expiry later — but the observed death lands inside
  the 8-hour window that `cdat + 30d` predicts, which is strong evidence none
  occurred. `cdat` does not change on refresh, only on re-creation.
- That the refresh grant is **not** rolling. If each refresh re-minted a 30-day
  grant, an actively-used credential would never expire — yet it expired at
  `cdat + 30d` while under continuous use. This is falsifiable and should be
  re-checked at the next cycle.

**Not established:** whether the 30-day grant life is a fixed product decision
or a server-side value that could change without notice. Nothing local reveals
that, and it is the one assumption a warning system should not hard-code —
**read `refreshTokenExpiresAt`, never compute `login + 30d`.**

## The next outage already has a date

```
refreshTokenExpiresAt   2026-08-21T21:31:50.920Z
+ up to 8h tail         2026-08-22T05:31:50.920Z
```

**Absent a `/login`, the fleet dies between those two timestamps.** That is a
falsifiable prediction, and the cheapest possible test of this whole report:
if a warning fires and a human logs in before 2026-08-21T21:31Z, nothing
happens — which is the point.

### Two corrections found while implementing the warner (mg-7024)

Both matter to anyone re-reading the field table above.

1. **The fields are nested under `claudeAiOauth`, not flat.** The table above
   lists `expiresAt` / `refreshTokenExpiresAt` bare, which is how they read in a
   report but not how they sit in the blob. The real shape is
   `.claudeAiOauth.refreshTokenExpiresAt`. A parse aimed at the flat shape finds
   nothing — and a warner that treated "field missing" as "expiry is fine" would
   then sit silent through the very outage it exists to prevent. mg-7024
   therefore resolves a missing field to **unreadable**, never to healthy.

2. **`expiresAt` is not a usable signal.** It is routinely *in the past* on a
   perfectly healthy machine: observed `2026-07-23T06:31:32Z` while reading the
   live item on `2026-07-22T23:33Z` — already ~7h stale — with the fleet working
   normally, because the harness re-mints access tokens on demand and does not
   always rewrite the stored blob. Only `refreshTokenExpiresAt` is predictive.
   Threshold-alerting on `expiresAt` would fire constantly and get the whole
   mechanism muted before the run that matters.

## Recommendation

> **Status: recommendation 1 SHIPPED as mg-7024** — `internal/credexpiry`, riding
> pogod's heartbeat, mails `human` at T−7d/−72h/−24h/−2h plus once on lapse, with
> `pogo credential expiry` for on-demand checks. See
> [docs/operations.md](../operations.md#the-fleet-auth-expiry-warning-pogo-credential-expiry).
> Two corrections to this report surfaced while building it, both recorded in
> "The next outage already has a date" below: the fields are **nested under
> `claudeAiOauth`**, and **`expiresAt` is not a usable signal**.

1. **Warn before, don't detect after.** Read `refreshTokenExpiresAt` from the
   keychain item and mail `human` at, say, T−72h, T−24h and T−2h. This is
   strictly better than mg-8cdb's detector on the axis that matters: it costs the
   fleet **zero** downtime instead of however long detection-plus-human takes.
   It does not replace mg-8cdb — a grant can still be revoked early, and that
   only a reactive detector catches. **Ship both; they cover different faults.**
2. **Never parse the token.** The warning needs one integer. Extract
   `expiresAt` / `refreshTokenExpiresAt` and discard the rest without logging it.
   Prefer the **attribute** read (`security find-generic-password` without `-w`)
   where `mdat` alone suffices — it needs no keychain authorization at all.
3. **Treat the path and schema as harness internals**, exactly as mg-18d0 said of
   the transcript signal and mg-5a06 established for the memory root. Degrade
   silently to today's behaviour when the item or field is absent. A missing
   `refreshTokenExpiresAt` must mean "no warning", never "expired".
4. **Expect a recovery lag.** A `/login` does not revive running sessions
   immediately — measured here at ~1 hour, bounded by the refresh cadence. Any
   runbook should say so, or the human will conclude the login failed and do it
   again.
5. **Do not compute expiry from the login time.** Read the field. Point 5 exists
   because the 30-day figure in this report is *measured*, not *documented*.

## Constraint compliance

The live credential was **not** experimented on: no `/login`, no revocation, no
refresh forced, no write to `~/.claude/**`. Every finding comes from reading
keychain *attributes*, two integer fields, session transcripts, and
`.last-update-result.json`. No token value was echoed, logged, or committed —
key **names** were enumerated before any value was read, precisely so the read
could be narrowed to two integers.

The one test this report cannot run is the confirmatory one: let the grant lapse
and observe. **That test runs itself on 2026-08-21** at no cost, provided
something is watching.
