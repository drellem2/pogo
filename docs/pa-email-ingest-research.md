# PA Email Ingest — HEY-side vs Gmail-side Access Research

**Status:** research / recommendation. Feeds the personal-assistant (pa) agent design; supersedes the email-read section of the earlier design (see mg-8066).
**Origin:** mg-113d. **Author:** polecat (mg-113d). Research current as of 2026-07-05.

## TL;DR

**Use the official HEY CLI (`basecamp/hey-cli`) as the ingest mechanism.** As of ~June 2026, HEY has an official, OAuth-authenticated CLI and API with agent skills — the "agent accessible" roadmap DHH announced on 2026-03-25 ("we'll hit HEY before long too") has shipped for HEY. This obsoletes the prior finding (2026-07-02) that mailbox forwarding was the only ToS-clean route. The poller becomes: `hey` CLI (OAuth) → seen-file dedupe → `mg mail send pa`. Forwarding-out remains the fallback, and the poller boundary keeps any later swap a one-script change.

## 1 · Scenario

A person's mail converges on a HEY mailbox from two streams: a consumer Gmail account auto-forwarding into HEY, plus mail addressed to the HEY address directly. The HEY mailbox is therefore the superset; anything reading only Gmail misses the hey-direct stream. The pa agent needs read access (send stays human-gated per the design's authority boundary).

## 2 · HEY-side access (primary)

### 2.1 Official CLI + API — recommended (new, ~June 2026)

- **`basecamp/hey-cli`** (github.com/basecamp/hey-cli): official 37signals repo, MIT, Go, actively maintained (pushed 2026-06-30). Reads and sends email, manages boxes, calendars, todos; `--json` output; ships an embedded agent skill (`hey skill install`).
- Auth: OAuth via 37signals Launchpad (`hey auth login`, browser flow; `--token`/`--cookie` also supported). Tokens auto-refresh; stored in the system keyring with a `~/.config/hey-cli/credentials.json` fallback — the fallback matters for headless LaunchAgent pollers without keyring access.
- Part of the `basecamp/cli` umbrella ("equally fluent for human operators and AI agents"); Launchpad OAuth is documented in `basecamp/api`.
- Announcement trail: DHH, "Basecamp becomes agent accessible" (world.hey.com/dhh, 2026-03-25) — Basecamp first, Fizzy next, HEY "before long"; the HEY tooling then shipped via GitHub without a headline blog post. 37signals' official shape is CLI + agent skills, not an MCP server.

### 2.2 Community MCP / session projects — superseded

- **Sealjay/mcp-hey** (pushed 2026-05-11): the only real HEY MCP server. 34 tools over reverse-engineered web endpoints using captured session cookies + CSRF tokens. Its own docs record 2026 breaking changes to HEY's private endpoints — direct evidence of the fragility. 6 stars; explicitly warns about anti-abuse triggers and prompt-injection via mail content.
- **psacc/hey-cli** (pushed 2026-04-02): Playwright/headless-Chromium automation of a logged-in session. Early experiment, 0 stars.
- Verdict: both predate the official CLI and are strictly worse now. If an MCP wrapper is wanted later, wrap the official CLI rather than adopting cookie scrapers.

### 2.3 Session-based access, honestly assessed (if the official path didn't exist)

- **Fragility:** private endpoints shift (observed breaking changes in 2026); session cookies expire; 2FA re-login can't be automated cleanly; UI changes break browser automation.
- **ToS:** the 37signals ToS (37signals.com/policies/terms, rewritten 2025-09-02; the old Use Restrictions policy is retired — its URL now redirects to the ToS) contains **no** clause banning scraping or automated access to your own account. The old "You must be a human" clause was removed; the current text instead makes the owner "responsible for all content posted to and activity that occurs under your account, including … activity of any users, agents, or bots in your account" — bots are contemplated, with owner liability. Residual exposure: the discretionary excessive-usage clause ("usage significantly exceeds the average usage of other customers" → temporary disable, with prior contact) and at-will termination ("suspend or terminate your account … for any reason at any time").
- **Lockout risk:** no documented case of a HEY suspension for personal polling was found, but the account at risk is a primary mailbox — a polite official-API poller carries none of this.
- Read-only polling sits well inside the grey zone; automated *send* multiplies both abuse-detection and ToS exposure. Moot now that an official path exists.

### 2.4 Forwarding out of HEY — the ToS-clean fallback

- HEY auto-forwards all incoming mail to any external address (paid accounts; Settings → Forwarding & Sending). A poller then reads the controlled mailbox (IMAP/JMAP).
- Official caveats (hey.com/forwarding): "HEY won't forward emails classified as spam … false positives happen and we might flag a legit email as spam and skip forwarding it," and "you shouldn't rely on forwarding alone for critical or very important emails" — forwarding can break DMARC; HEY adds ARC headers but receivers may distrust them. Screener interaction is undocumented (only spam exclusion is official).
- Still the right fallback: zero automation against HEY itself, and identical poller shape.

## 3 · Gmail-side access (supplementary)

Ranked for a headless poller against a consumer Gmail account (verified 2026-07-05):

1. **IMAP + app password** — still works; requires 2-Step Verification; IMAP is always-on since Jan 2025 (no toggle). No announced deprecation. Failure mode: an account password change silently revokes all app passwords.
2. **Gmail API + OAuth** — consumer projects must be External; **Testing** status expires refresh tokens every 7 days (unusable for a poller); publishing **In production without verification** yields long-lived tokens with a click-through "unverified app" warning, fine for personal use (<100 users). `gmail.readonly` is a restricted scope but personal-use apps never trigger the verification/CASA machinery. Same password-change revocation gotcha.

Positioning: with HEY-side via the official API, Gmail polling is unnecessary — forwarded Gmail mail already lands in HEY. The one stream it uniquely covers is Gmail's own spam folder (Gmail forwards "all new messages … except for spam"). It also serves as an interim if the official-CLI path stalls.

## 4 · Coverage matrix

| Architecture | gmail-addressed | hey-direct | Breaks silently |
|---|---|---|---|
| HEY official CLI/API poller | ✅ (via Gmail→HEY forward) | ✅ | Gmail-side spam false-positives never forwarded into HEY; OAuth/keyring failure stops the poller (alert on staleness) |
| HEY MCP/session (community) | ✅ | ✅ | Same as above **plus** endpoint/cookie breakage at any time |
| HEY forward → controlled mailbox | ✅ | ✅ | HEY spam false-positives never forwarded; DMARC/ARC drops at the receiver; Gmail-spam gap too |
| Gmail-only | ✅ (incl. its spam folder) | ❌ misses entirely | The whole hey-direct stream |
| Gmail + HEY-forward | ✅ ×2 (dedupe by Message-ID) | ✅ | HEY spam false-positives on the hey-direct stream |

## 5 · Calendar sidebar

The design picked iCloud CalDAV (app-specific password, headless-robust). If a Google account gets hooked anyway: Google Calendar's **secret iCal URL** is the cheapest read path (no auth infrastructure, capability-URL risk, possible minutes-level staleness); the Calendar API needs the same OAuth plumbing as §3.2; Google's CalDAV v2 requires OAuth (app passwords do not work there), so the iCloud-CalDAV pattern does not transfer. Note also that `hey-cli` reads HEY Calendar, and HEY Calendar exports ICS feeds. **Open question for the user: which calendar is actually kept — iCloud, Google, or HEY?** That answer picks the poller.

> **Resolved (mg-a909, 2026-07-05):** Daniel keeps his **Google** calendar ("it's gmail that matters"), so the iCloud-CalDAV pick above is superseded. Mechanism chosen: **gcalcli** (maintained CLI; handles OAuth + refresh, recurring-event expansion, TSV with stable event ids). Device-code flow was a dead end — Google's limited-input-device flow does not allow Calendar scopes — and the secret-iCal-URL path loses to gcalcli on RRULE expansion. Consent screen must be published *In production* (unverified) to dodge the 7-day Testing-token expiry from §3.2. The worked example (formerly `docs/examples/personal-assistant/`) has moved to a private canonical home; see its README § "Google Calendar feed".

## 6 · Recommended architecture + revised Phase 0

**Poller (LaunchAgent, standalone script): official `hey` CLI `--json` list/read → seen-file dedupe → `mg mail send pa --from=hey-feed`.** Read-only; send stays human-gated. The CLI invocation is one function — if 37signals changes the API or a better surface appears, it is a one-script swap. Fallback ladder: official CLI → HEY forwarding + controlled mailbox → Gmail IMAP interim.

Revised Phase 0 (user-side, replaces "provision a forward-target mailbox"):
1. Install `hey-cli` (Go binary; `go install` or release download).
2. Run `hey auth login` once interactively (browser OAuth); confirm credentials persist for headless use (keyring vs `~/.config/hey-cli/credentials.json`).
3. Smoke-test: `hey` list/read against the mailbox with `--json`.
4. Answer the calendar question (§5).

Effort vs the old Phase 0: one OAuth login against an existing account, versus provisioning and paying for a new mailbox, enabling forwarding, and managing an extra credential — strictly fewer moving parts, and no forwarding losses.

## Sources

37signals ToS (37signals.com/policies/terms, 2025-09-02) · hey.com/forwarding · hey.com/faqs (no IMAP/POP) · github.com/basecamp/hey-cli · github.com/basecamp/cli · github.com/basecamp/api (Launchpad OAuth) · world.hey.com/dhh/basecamp-becomes-agent-accessible-3ae6b949 (2026-03-25) · github.com/Sealjay/mcp-hey · github.com/psacc/hey-cli · Google: support.google.com/mail/answer/185833 (app passwords), /answer/7126229 (IMAP always-on), /answer/10957 (forwarding excl. spam), developers.google.com/identity/protocols/oauth2#expiration (7-day Testing expiry), developers.google.com/workspace/calendar/caldav/v2/guide (OAuth-only), support.google.com/calendar/answer/37648 (secret iCal URL). All checked 2026-07-05.
