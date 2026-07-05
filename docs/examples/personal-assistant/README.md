# Personal-assistant (pa) example — HEY email feed

A worked example of wiring a **personal-assistant crew agent** into pogo:
a `pa` crew agent that triages the operator's email, fed by a standalone
poller that reads a [HEY](https://hey.com) mailbox through the official
`hey` CLI ([basecamp/hey-cli](https://github.com/basecamp/hey-cli)) and
delivers new mail into `pa`'s pogo mailbox.

This is the Phase-1 (email read path) shape from the pa design; the
architecture decision record is
[`docs/pa-email-ingest-research.md`](../../pa-email-ingest-research.md)
(official CLI as primary; HEY auto-forwarding to a controlled mailbox as
fallback; Gmail IMAP as interim). Calendar (Phase 2) and any email *send*
authority (Phase 3) are out of scope here.

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
- **Read-only email.** The poller only lists; the agent only reads
  (`hey boxes` / `hey box` / `hey threads`). Replies are *drafted* into mail
  to the `human` mailbox for the operator to send. No `hey compose`/`reply`,
  no `hey seen`. Write authority is a separate, explicitly-gated phase.
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
├── bin/poll-hey.sh                      (the poller — reference copy)
└── launchd/com.pogo.pa-heyfeed.plist    (LaunchAgent template)
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
   and a morning sweep. Then `pogo agent start pa`.

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
