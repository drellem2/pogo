# Personal-assistant (pa) example — HEY email feed + Google Calendar feed

A worked example of wiring a **personal-assistant crew agent** into pogo:
a `pa` crew agent that triages the operator's email and answers calendar
questions, fed by standalone pollers — one reading a [HEY](https://hey.com)
mailbox through the official `hey` CLI
([basecamp/hey-cli](https://github.com/basecamp/hey-cli)), one reading
Google Calendar through [gcalcli](https://github.com/insanum/gcalcli) —
each delivering into `pa`'s pogo mailbox.

This covers Phase 1 (email read path, mg-8066) and Phase 2 (calendar read
path, mg-a909) from the pa design; the architecture decision record is
[`docs/pa-email-ingest-research.md`](../../pa-email-ingest-research.md)
(official CLI as primary; HEY auto-forwarding to a controlled mailbox as
fallback; Gmail IMAP as interim; §5 covers the calendar options).
Email *send* authority (Phase 3) is deployment-optional; when an operator
enables it, use the [approval-gated send pattern](#send-authority-phase-3--the-approval-gated-send-pattern)
below rather than open-ended write access. Calendar *write* authority
(also Phase 3) is out of scope here — the calendar feed is read-only.

## Architecture

```
HEY mailbox ──(hey box imbox --json, OAuth)──► poll-hey.sh   (LaunchAgent, ~/.pogo/pogo-pa/bin/)
                                                  │  seen.json dedupe (~/.pogo/pa/heyfeed/)
                                                  ▼
                                            mg mail send pa --from=hey-feed
                                                  │
                                                  ▼
                                            pa crew agent  (~/.pogo/agents/crew/pa.md)
                                                  │  triage: escalate / digest / hold
                                                  ▼
                                            mg mail send human  (drafts included as text)
```

Design rules this encodes:

- **Ingest lives outside the agent.** The poller is a standalone script under
  `~/.pogo/pogo-pa/bin/`, run by launchd — never a pogo subcommand
  (unix-utility principle), and never something the agent does in-session.
  Same skeleton as the Apple-Reminders bridge (`pogo-reminders`).
- **Pull-verify-consume.** A zero exit from `mg mail send` is not proof of
  durable delivery; the poller stats the maildir file before recording a
  posting as seen, and leaves it un-seen (retry next cycle) otherwise. The
  bias is duplicate delivery over silent loss.
- **Read-only email by default.** The poller only lists; the agent only
  reads (`hey boxes` / `hey box` / `hey threads`). Replies are *drafted*
  into mail to the `human` mailbox for the operator to send. No
  `hey seen`/`unseen` ever — the operator's unread state is theirs. Send
  authority (`hey compose`/`reply`) is a separate phase the operator
  explicitly grants, and even then it is approval-gated per message — see
  the pattern below.
- **Graceful pre-auth.** `hey auth status --json` reports
  `authenticated: false` (exit 0) until the operator runs `hey auth login`
  interactively — the one step that cannot be automated. In that state the
  poller logs once and idles quietly, so the whole stack deploys *before*
  auth exists and goes live the moment the login lands. No crash-looping
  LaunchAgent.
- **Secrets stay opaque.** hey-cli keeps OAuth tokens in the system keyring
  (file fallback `~/.config/hey-cli/credentials.json`). Nothing here reads,
  prints, or mails token material; the agent prompt forbids `hey auth token`.

## Files in this example

```
docs/examples/personal-assistant/
├── README.md                            (this file)
├── bin/poll-hey.sh                      (email poller — reference copy)
├── bin/poll-gcal.sh                     (calendar poller — reference copy)
├── launchd/com.pogo.pa-heyfeed.plist    (LaunchAgent template, email)
└── launchd/com.pogo.pa-calendar.plist   (LaunchAgent template, calendar)
```

The deployed copies live outside the repo: the poller at
`~/.pogo/pogo-pa/bin/poll-hey.sh`, the plist in `~/Library/LaunchAgents/`,
and the `pa` crew prompt at `~/.pogo/agents/crew/pa.md` (crew prompts are
machine-local and edited directly; they have no repo source — and per the
project's privacy rule, nothing personal goes in committed files, so the
prompt with the operator's actual triage preferences stays out of the repo).

## Setup

1. **Install the CLI** (Go 1.26+; the Go toolchain auto-downloads if older):

   ```bash
   go install github.com/basecamp/hey-cli/cmd/hey@latest
   ```

2. **Deploy the poller** and its LaunchAgent:

   ```bash
   mkdir -p ~/.pogo/pogo-pa/bin ~/.pogo/pa/heyfeed
   cp bin/poll-hey.sh ~/.pogo/pogo-pa/bin/
   # edit launchd/com.pogo.pa-heyfeed.plist: replace /Users/YOU
   cp launchd/com.pogo.pa-heyfeed.plist ~/Library/LaunchAgents/
   launchctl load ~/Library/LaunchAgents/com.pogo.pa-heyfeed.plist
   launchctl kickstart gui/$(id -u)/com.pogo.pa-heyfeed
   ```

   The log at `~/.pogo/pa/heyfeed/poller.log` should show one
   "not authenticated … idles quietly" line and nothing else.

3. **Write the `pa` crew prompt** at `~/.pogo/agents/crew/pa.md` with
   `auto_start = true, restart_on_crash = true` frontmatter. It should cover:
   the feed format (`--from=hey-feed`, `[hey/<box>] …` subjects), a triage
   policy (escalate time-sensitive items to `human`, batch FYIs into a
   digest, escalate ambiguity instead of acting), the authority boundary
   above, and daemon-side schedules (`pogo schedule pa …`) for mail-check
   and a morning sweep. If you grant send authority, encode the
   approval-gated send pattern below verbatim. Then `pogo agent start pa`.

4. **Authenticate** (interactive, once): `hey auth login`. The next poll
   cycle picks it up — no restart needed.

### Feed-coverage caveat

The current hey-cli build lists boxes (`imbox`, `feedbox`, …) but has **no
Screener listing**, and spam is never listed. Mail waiting in the Screener,
screened-out senders, and spam false-positives are invisible to this feed —
the pa prompt should encode that ("no mail from X in the feed" ≠ "no mail
from X exists"). This differs from the forwarding fallback, whose
documented gap is HEY's spam classification instead (see the research doc's
coverage matrix).

## Send authority (Phase 3) — the approval-gated send pattern

The hey CLI *can* send: `hey compose --to <addr> --subject "<subj>" -m "<text>"`
for new messages and `hey reply <topic-id> -m "<text>"` for thread replies
(this superseded the pre-CLI assumption that sending as `@hey.com` was
impossible). Having a send path does not mean the agent should have open
write access. When the operator decides to grant send authority, gate it
per message:

1. **Approval is per message and per exact text.** The agent may send an
   email only when the operator has approved the exact final text of that
   specific message. Approval must quote the text or unambiguously
   reference it ("send the draft from your 14:02 mail as-is"); a generic
   "yes, reply to them" approves no particular text.
2. **Any edit after approval voids the approval** — even a one-word tweak,
   even one the operator asked for. The new text needs fresh approval.
3. **Log every send** to an agent-state file outside the repo (e.g.
   `~/.pogo/agents/<agent>-state/send-log.md`): timestamp, recipient,
   subject, and a reference to where the approval happened. No send
   without a log entry.
4. **No bulk sends.** One approved message to its approved recipient(s)
   per send; never loop over a list, never re-use an approved text for a
   different recipient.
5. **Ambiguity means do not send.** Unsure which text was approved, or for
   which recipient or thread? Ask; don't send.

The upgrade is thus from "agent drafts + operator sends" to "agent sends
*after* the operator approves the exact text" — the operator stays the
authority on every outbound message; only the mechanical copy-paste step
is delegated. Autonomous sending (no per-message approval) is a different,
larger grant this example deliberately does not cover.

## Poller contract

`bin/poll-hey.sh` — environment knobs, all optional:

| Variable | Default | Meaning |
|---|---|---|
| `POLL_INTERVAL` | `120` | Seconds between cycles |
| `STATE_DIR` | `~/.pogo/pa/heyfeed` | Holds `seen.json` |
| `MAILDIR_ROOT` | `~/.macguffin/mail` | Where delivery is verified |
| `TARGET_AGENT` | `pa` | Mailbox to deliver to |
| `FROM_NAME` | `hey-feed` | Sender name on delivered mail |
| `HEY_BIN` | `hey` | Override with a stub for fixture tests |
| `HEY_BOXES` | `imbox` | Space-separated boxes to poll |
| `HEY_LIMIT` | `30` | Postings fetched per box per cycle |
| `ONESHOT` | `false` | `true` = one cycle, then exit (testing) |

Dedupe key is `<box>:<posting id>` → `updated_at`, so a thread that receives
new mail is re-delivered with an `updated:` subject prefix. `seen.json` is
written atomically (tmp + `mv`) once per cycle.

## Testing without an authenticated CLI

Point `HEY_BIN` at a stub that serves a fixture, and deliver to a scratch
mailbox:

```bash
STUB_AUTH=true ONESHOT=true HEY_BIN=./hey-stub \
  STATE_DIR=/tmp/heyfeed-test TARGET_AGENT=pa-test \
  ~/.pogo/pogo-pa/bin/poll-hey.sh
```

The stub answers `auth status --json` with a canned envelope and `box <name>
--json` with a fixture postings file. Run it twice to verify dedupe (second
run delivers nothing), bump a fixture `updated_at` to verify the
thread-update path, and run with `STUB_AUTH=false` to verify the quiet
pre-auth idle.

---

# Google Calendar feed (Phase 2, mg-a909)

Same skeleton, second feed: a standalone poller reads the operator's Google
Calendar through [gcalcli](https://github.com/insanum/gcalcli) and mails `pa`
about new, changed, or removed upcoming events. Read path only — `pa` may
run read-only gcalcli commands itself (`agenda`, `calw`, `calm`, `list`,
`search`); calendar *writes* (`add`, `edit`, `delete`, `quick`, `import`,
`agendaupdate`) are Phase 3 and forbidden until then.

```
Google Calendar ──(gcalcli agenda --tsv, OAuth)──► poll-gcal.sh  (LaunchAgent, ~/.pogo/pogo-pa/bin/)
                                                      │  seen.json dedupe (~/.pogo/pa/gcalfeed/)
                                                      ▼
                                                mg mail send pa --from=gcal-feed
                                                      │
                                                      ▼
                                                pa crew agent  — answers "what's on today/this week"
                                                      │           via read-only gcalcli; folds today's
                                                      ▼           agenda into its morning sweep
                                                mg mail send human
```

## Why gcalcli (mechanism decision)

Evaluated per the mg-113d research sidebar (consumer OAuth realities):

- **gcalcli (chosen).** Maintained CLI (4.5.x, Homebrew) that wraps the
  Calendar API: OAuth login + token refresh, recurring-event expansion, TSV
  output with a header row, and stable per-instance event ids
  (`--details id`) for dedupe. Everything a hand-rolled script would need to
  reimplement.
- **Minimal script + OAuth device-code flow (rejected).** Google's
  limited-input-device flow does not allow Calendar scopes at all, so the
  "device flow avoids the consent screen" hope is moot — a custom script
  needs the same loopback-server installed-app flow gcalcli already has.
- **Google CalDAV (rejected).** Requires the same OAuth plumbing (no
  app-password basic auth, unlike iCloud) *plus* a CalDAV client and RRULE
  expansion. Strictly worse.

No path avoids a Google Cloud project: gcalcli ships no embedded OAuth
client (Google policy), so the operator supplies their own client id/secret.
The consent-screen clicks are documented below as part of the one-login
step. Trade-off to know: gcalcli requests the full `auth/calendar` scope
(read-write token) — the read-only boundary is enforced at the command/
prompt level, same as the hey feed (where the CLI can also send email).

## Setup

1. **Install the CLI:** `brew install gcalcli` (or `pipx install gcalcli`).

2. **Deploy the poller** and its LaunchAgent (idles quietly until auth):

   ```bash
   mkdir -p ~/.pogo/pogo-pa/bin ~/.pogo/pa/gcalfeed
   cp bin/poll-gcal.sh ~/.pogo/pogo-pa/bin/
   # edit launchd/com.pogo.pa-calendar.plist: replace /Users/YOU
   cp launchd/com.pogo.pa-calendar.plist ~/Library/LaunchAgents/
   launchctl load ~/Library/LaunchAgents/com.pogo.pa-calendar.plist
   launchctl kickstart gui/$(id -u)/com.pogo.pa-calendar
   ```

   The log at `~/.pogo/pa/gcalfeed/poller.log` should show one
   "not authenticated … idles quietly" line and nothing else.

3. **Extend the `pa` crew prompt** (`~/.pogo/agents/crew/pa.md`): the feed
   format (`--from=gcal-feed`, `[gcal] <date> <time> — <title>` subjects,
   `updated:`/`removed:` variants), the read-only command list above, the
   write prohibition, and a line in its morning-sweep schedule message to
   fold `gcalcli agenda` for today into the digest mailed to `human`.

4. **One-time Google auth** (the single interactive step; ~5 minutes of
   console clicks because Google requires a per-user OAuth client):

   1. [console.cloud.google.com](https://console.cloud.google.com) → create
      a project (any name, e.g. `pa-gcal`).
   2. *APIs & Services → Library* → enable **Google Calendar API**.
   3. *APIs & Services → OAuth consent screen* (Google Auth Platform) →
      configure: **External**, fill in app name + your email twice, no
      scopes/branding needed.
   4. *Audience* → **Publish app** (status "In production"). Do NOT stay in
      "Testing": Testing-status refresh tokens expire after 7 days and the
      poller dies weekly. Unverified-in-production is fine for personal use —
      the login just shows an "unverified app" warning (Advanced → continue).
   5. *Clients* → Create client → type **Desktop app** → note the client id
      and secret (these are not account credentials; they identify the app).
   6. On the Mac: `gcalcli init` → paste client id/secret → browser opens →
      sign in with the Google account → allow. The token lands in
      `~/Library/Application Support/gcalcli/oauth` and the poller goes live
      on its next cycle — no restart needed.

## Poller contract

`bin/poll-gcal.sh` — environment knobs, all optional:

| Variable | Default | Meaning |
|---|---|---|
| `POLL_INTERVAL` | `300` | Seconds between cycles |
| `STATE_DIR` | `~/.pogo/pa/gcalfeed` | Holds `seen.json` |
| `MAILDIR_ROOT` | `~/.macguffin/mail` | Where delivery is verified |
| `TARGET_AGENT` | `pa` | Mailbox to deliver to |
| `FROM_NAME` | `gcal-feed` | Sender name on delivered mail |
| `GCALCLI_BIN` | `gcalcli` | Override with a stub for fixture tests |
| `GCAL_TOKEN_FILE` | `~/Library/Application Support/gcalcli/oauth` | Token path gating polling (Linux: `~/.local/share/gcalcli/oauth`) |
| `WINDOW_DAYS` | `7` | Days ahead to watch |
| `FAIL_ALERT_AFTER` | `5` | Consecutive fetch failures before mailing `human` |
| `ONESHOT` | `false` | `true` = one cycle, then exit (testing) |

Semantics:

- **Dedupe key** is the event id from `--details id` (recurring instances
  get distinct ids), mapped to a fingerprint of the visible fields — a
  rescheduled or renamed event re-surfaces as `updated:`, an event that
  vanishes from the window before its start date as `removed:`.
- **First authenticated cycle primes silently:** existing events are
  recorded without one-mail-per-event flooding; `pa` gets a single
  "feed is live — N event(s)" summary.
- **Pre-auth gate is existence-only.** gcalcli auto-launches an interactive
  OAuth flow when it has no token — lethal under launchd — so the poller
  never invokes gcalcli until the token file exists. It never reads the
  token file's contents.
- **Fetch-failure alerting:** after `FAIL_ALERT_AFTER` consecutive gcalcli
  failures (revoked token, password change, API outage) it mails `human`
  once per outage with the fix (`gcalcli init`), then stays quiet until
  recovery.
- Pull-verify-consume delivery and atomic `seen.json` writes, exactly as
  the hey poller.

## Testing without Google auth

Point `GCALCLI_BIN` at a stub that prints a TSV fixture (header row
included) and `GCAL_TOKEN_FILE` at any non-empty file:

```bash
ONESHOT=true GCALCLI_BIN=./gcal-stub GCAL_TOKEN_FILE=./fake-oauth \
  STATE_DIR=/tmp/gcalfeed-test TARGET_AGENT=pa-test \
  ~/.pogo/pogo-pa/bin/poll-gcal.sh
```

First run primes (summary mail only); run again unchanged to verify dedupe
(nothing delivered); change a fixture time to verify `updated:`; drop a
fixture row to verify `removed:`; point `GCAL_TOKEN_FILE` at a missing file
to verify the quiet pre-auth idle. The fixture's header row is parsed by
name, so column order doesn't matter.

## Feed-coverage caveat

The feed sees the **next `WINDOW_DAYS` days only**, at `POLL_INTERVAL`
granularity, on the calendars the Google account can read. Events further
out, calendars the account isn't subscribed to, and sub-poll-interval churn
are invisible to the *feed* — but `pa` answers live questions by running
`gcalcli agenda`/`calw` directly, which has no window limit. "No feed mail
about X" ≠ "X isn't on the calendar".
