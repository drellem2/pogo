- **ESTABLISHED why the fleet credential expired, and that expiry is PREDICTABLE
  (mg-ed45).** mg-18d0 left this open — *"a recurrence is otherwise a matter of
  time."* It has a date now.

  The credential is a macOS Keychain item, not a file, and its JSON blob carries
  **`expiresAt` and `refreshTokenExpiresAt`**. Two round lifetimes fall straight
  out: the access token lives **8 hours** (`expiresAt − mdat = 7:59:59.920`) and
  the OAuth refresh grant lives **exactly 30 days**, not extended by use. The
  prior grant was minted at the keychain item's creation,
  `2026-06-21T15:17:25Z`, so it lapsed at `2026-07-21T15:17:25Z`; the fleet then
  coasted on its final 8-hour access token until that died — predicting death by
  `23:17:25Z` against an observed window of `23:01:28Z..23:10:26Z`. **No
  revocation, no clock event, no server-side change: the designed lifetime,
  reached.**

  **Auth expiry is periodic, not chronic — correcting mg-18d0.** Clustering every
  genuine synthetic auth turn shows **exactly two outages ever**, `31d 21h`
  apart, and the `914 login-expired` / `498 invalid-credential` counts read as
  two chronic error *families* are simply the two *episodes*: the split is
  perfectly clean (`498/0`, then `0/914`). The rate-limit, weekly-limit and
  spend-limit findings stand — only the auth member is periodic, which is what
  makes it predictable rather than merely detectable.

  **The `/login` restored it; the 2.1.217→2.1.218 update is EXCLUDED.** The last
  failing turn of the outage was `22:31:05.395Z`; the credential was rewritten at
  `22:31:32.920Z`; the update ran at `22:36:10.655Z` — **5m05s after failures had
  already stopped.** Symmetry confirms it: outage 1 ended 14s before the keychain
  item was created, outage 2 ended 27s before it was rewritten; neither ended
  near a version change. Also measured: a `/login` does **not** revive running
  sessions immediately — the grant existed from `21:31:50Z` but agents failed
  until `22:31:05Z`, recovering only at the harness's next refresh.

  **Next outage, absent a `/login`: between `2026-08-21T21:31:50Z` and
  `2026-08-22T05:31:50Z`.** So the recommendation is to **warn before rather than
  detect after** — read `refreshTokenExpiresAt`, mail `human` at T−72h/T−24h/T−2h.
  This does not replace mg-8cdb's detector and must not: a grant can still be
  revoked early, which only a reactive detector catches. Ship both.

  Method note kept in the report: a plain `grep` for the error strings produces a
  **spurious post-recovery cluster**, because it matches the transcripts of the
  polecats investigating the outage. Filtering on
  `isApiErrorMessage && model=="<synthetic>"` reproduces mg-18d0's `914+498`
  exactly. Any transcript-grepping detector will detect itself.

  Live credential **not** experimented on — no login, revocation, or forced
  refresh; no write to `~/.claude/**`; key *names* enumerated before any value
  was read so the read could be narrowed to two integers, and no token value
  echoed or stored. Record:
  `docs/investigations/credential-expiry-mechanism-2026-07-23.md`. Fact-finding
  only — no behaviour change.
