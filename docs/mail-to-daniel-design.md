# `mg mail` → Daniel Notification — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-151b. **Author:** architect.
**Sibling docs:** `product-manager-design.md` (PMs depend on this path for digests).
**Companion repo (existing):** `pogo-reminders/` — the inverse path (Reminders → mayor).

## TL;DR

Two decisions to make.

**(a) Addressing pattern.** Standardize on **`human`** as the user mailbox — it's portable across operators *and* it plugs into `pogo status` colorization (Daniel scans for `human`-blocked items at a glance). **No mg core changes** — mg stays generic for other users. Convention is enforced via crew/polecat prompt files, with optional mg UX nudges if drift appears. Mayor's inbox is coordination only; mayor stops forwarding user-facing mail.

**(b) Outbound apple-side service.** **Fold into the existing `pogo-reminders` repo** rather than spinning up a new sibling. They will evolve together as the general "mg mail / remote interface to pogo" bridge. Add a second launchd-supervised poller (`bin/poll-mail.sh` + `launchd/com.pogo.notify.plist`) that polls `~/.macguffin/mail/human/new/`, fires a macOS user notification per new message, and writes the body to a "Pogo Inbox" Reminders list. State (`~/.pogo/reminders/notify-seen.json`) is symmetric with the inbound side. v0 read-only; reply path stays through inbound Reminders.

**Decision needed from Daniel:** approve `human` as canonical (no mg changes, prompt-driven enforcement), approve folding the outbound poller into `pogo-reminders`, approve the v0 scope below.

---

## Part A — Addressing pattern

### Current state (observed)

| Address | Who uses it | Purpose |
|---|---|---|
| `daniel` | architect, mayor (recently) | Direct mail to Daniel — mailbox exists with 5 old messages |
| `human` | agents *say* "human" sometimes; no mailbox auto-creates until first send | Generic "needs-human" placeholder |
| `mayor` | everyone | Coordination — but mayor effectively forwards "user-facing" subjects |
| `architect`, `doctor`, `pm-*` | named agents | Other crew |

**Problem:** Inconsistent. Some flows aim at `daniel`, some at `human`, some at `mayor` expecting the mayor to decide whether to pass it on. This makes notification routing impossible — the apple-side service has no single "is this for the user?" signal.

### Recommendation: `human` is canonical. No mg core changes.

mg is a generic tool that other workspaces and other operators build on. Aliasing one address to another inside mg core would bake a single-operator naming convention into the tool's semantics — wrong layer. The convention belongs in the prompts, not the code.

**Rules:**

1. **`human` is the user mailbox.** All user-facing mail addresses `human`. Daniel reads via `mg mail list human`.
2. **`mayor` is for coordination only.** Mayor stops being a forwarding inbox. User-facing mail goes to `human`, not `mayor`.
3. **Convention is enforced via prompts**, not code. Crew/polecat prompt files all say: "user-facing mail goes to `human`."
4. **Optional mg UX nudges**, only if drift appears. If agents persist in addressing `daniel` (or other user-named mailboxes) despite prompt guidance, add a soft hint in `mg mail send` — printed, not enforced — so other workspaces are unaffected. Decide if/when needed.

### Why `human` and not `daniel`

Two reasons:

1. **Portability.** `human` works across operators with no per-user config. Generic agent templates can address it without knowing whose machine they're on. Drop-in for any pogo install.
2. **`pogo status` colorization.** `human` plugs into the existing `pogo status` color signal — items blocked by `human` show up colored, scannable at a glance. That's a real workflow signal, not a naming preference. `daniel` (or any per-user name) would not aggregate cleanly.

### Why no aliases in mg core

mg is meant to be reusable. An alias map (`human → <username>`) hard-codes one workspace's convention into a generic tool. Even a config-driven alias map adds complexity for a problem that doesn't exist outside this workspace. Solving the addressing convention via prompts costs zero code and stays correct in any deployment.

### Why mayor stops forwarding

Mayor-as-forwarding-inbox is the actual bug. (1) Mayor has its own queue and shouldn't context-switch into "is this for me or for the user?" parsing. (2) Notification routing is impossible if user-facing mail and coordination mail are mixed in one inbox. (3) The mayor prompt becomes simpler when its inbox is unambiguous.

### Required prompt edits (the whole change)

- **`mayor.md`** — "your inbox is for coordination; if you have something for the user, send to `human`. Do not summarize/forward mail addressed to other agents into your own thread."
- **`architect.md`** — replace any "mail Daniel" path with "mail `human`."
- **`doctor.md`** — same.
- **Polecat template** — "if you need to surface something to the user, mail `human`."
- **`pm-template.md`** (when it lands per mg-69cb) — "FYI digests go to `human`."

Each is a one-line edit. **No code change anywhere** — not in mg, not in pogod, not in the refinery.

### Existing `daniel` mailbox

The 5 messages in `daniel/new/` stay where they are — they're 1+ month old and presumably already seen out-of-band. The convention going forward is `human`. The notifier polls `~/.macguffin/mail/human/new/`, not `daniel/new/`.

If Daniel wants to clean up: `mv ~/.macguffin/mail/daniel/new/* ~/.macguffin/mail/human/new/` is a one-liner. Or leave them. No-op recommended; they'll age out naturally.

### Open question (not a blocker)

**Multi-operator workspaces.** `human` is portable across operators (no name baked in), so this is a strict improvement over `daniel`. If a workspace ever needs parallel human inboxes, the natural extension is `human-<name>` by convention; `pogo status` colorization can match `human*`. No code change required.

---

## Part B — Outbound apple-side notification service

### Requirements (restated)

1. **Notification.** Push-style alert on macOS when new mail arrives in `human/new/`.
2. **Read access.** Daniel reads message body without opening a terminal.
3. **No required action.** Read-only. Reply path stays through `pogo-reminders`.
4. **Don't integrate into mg core.** Separate consumer.
5. **Mirror the `pogo-reminders` shape** where sensible.
6. **Don't make bidirectional yet.**

### Recommendation: extend the existing `pogo-reminders` repo into a two-way Apple bridge

Daniel's framing: pogo-reminders evolves into the general "mg mail / remote interface to pogo." Inbound (Reminders → mayor) and outbound (mg mail → notification + Reminders) live in the same repo because they share state, install lifecycle, and Apple-platform glue, and they will accrete features together (filters, do-not-disturb, etc.).

```
pogo-reminders/                              # existing repo, expanded
├── README.md                                # update: "Apple bridge for mg mail (in + out)"
├── install.sh                               # idempotent; brings up both pollers
├── uninstall.sh
├── bin/
│   ├── poll-reminders.sh                    # existing — Reminders → mayor (inbound)
│   ├── read-reminders.js                    # existing — JXA helper for Reminders read
│   ├── poll-mail.sh                         # NEW — human/new/ → notification + Reminders write
│   └── notify.sh                            # NEW (small) — terminal-notifier with osascript fallback
└── launchd/
    ├── com.pogo.reminders.plist             # existing — supervises poll-reminders.sh
    └── com.pogo.notify.plist                # NEW — supervises poll-mail.sh
```

State (single directory, both directions):

```
~/.pogo/reminders/
├── seen.json                                # existing — inbound dedupe (reminders → mayor)
├── poller.log                               # existing — inbound log
├── notify-seen.json                         # NEW — outbound dedupe (mail → notification)
└── notify.log                               # NEW — outbound log
```

Two separate launchd jobs (one per direction) so a crash in one doesn't take down the other. Single repo, single install script, single mental model.

### How it works (per poll cycle)

```
1. List entries in ~/.macguffin/mail/human/new/
2. For each entry not in notify-seen.json:
   a. Parse headers (From, Subject, Date) and body.
   b. Fire a macOS user notification:
        title = "[mg] <From>: <Subject>"
        body  = first ~300 chars of message body
        click = run `mg mail read human/<msg-id>` in a new Terminal window
   c. Append a Reminder to the "Pogo Inbox" list (so Daniel sees it on iPhone too):
        title = "[mg] <From>: <Subject>"
        notes = full body + "msg-id: <id>"
   d. Add <id> to notify-seen.json.
3. Sleep POLL_INTERVAL.
```

State persistence reuses the `seen.json` pattern from `poll-reminders.sh` — separate file (`notify-seen.json`) so the two directions don't trample each other.

### Why notification + Reminders write (both)

- **Notification alone** is ephemeral; if Daniel is away from the laptop, he misses it.
- **Reminders alone** doesn't surface fast enough; he'd have to actively check.
- **Both:** notification fires immediately on Mac; Reminders syncs to iPhone via iCloud (Daniel already has the Pogo list configured for the inbound side, so iCloud sync is a known-good path). Reading a Reminder = reading the message body. Marking complete = "I saw it" (no semantic action — read-only v0).

### Why not Apple Mail / iMessage / Calendar

- **Apple Mail** drafts: heavyweight, requires SMTP setup, terrible for the volume.
- **iMessage to self**: works but "official" macOS APIs have been hostile to iMessage automation since 13.0 (the AppleScript dictionary still exists but is increasingly fragile, requires accessibility permissions, sometimes silently drops). Reminders is the supported integration path Apple explicitly maintains.
- **Calendar**: semantic mismatch. Mail isn't an event.
- **Custom NSUserNotification daemon (Swift)**: most native, far more code, requires code signing for some entitlements, much higher install friction than a launchd-managed shell script. Defer unless v0 proves inadequate.

The `pogo-reminders` route already proved that Reminders + osascript is a workable path. Mirror it for the inverse direction.

### Notification implementation note

For the macOS user notification itself, two viable mechanisms:

1. **`osascript -e 'display notification ...'`** — built-in, zero install, works today. Limitation: notifications from `osascript` come from "Script Editor" by name and Apple has been steadily restricting them; there's no deep-link / action-button support.
2. **`terminal-notifier`** (Homebrew) — supports custom titles, sender icon, and a click-to-open URL. Recommended.

**Recommendation:** prefer `terminal-notifier` if installed, fall back to `osascript`. The install script checks for `terminal-notifier` and offers `brew install terminal-notifier` if absent.

The "click to open" URL launches a small wrapper that runs `mg mail read daniel/<id>` in a new Terminal window. That gives Daniel a one-click "I want to see the body" action without hand-typing.

### What lives where

| Concern | Lives in |
|---|---|
| **Inbound poll/dispatch (Reminders → mayor)** | `pogo-reminders/bin/poll-reminders.sh` (existing) |
| **Outbound poll/notify (mg mail human → notification + Reminders mirror)** | `pogo-reminders/bin/poll-mail.sh` (NEW) |
| **launchd configs** | `pogo-reminders/launchd/com.pogo.{reminders,notify}.plist` |
| **macOS notification + Reminders permission** | TCC prompts on first run; documented in expanded README |
| **`human` convention** | Crew/polecat prompt files in pogo repo (mayor.md, architect.md, doctor.md, polecat template, pm-template) |
| **mg core mail logic** | **unchanged** (no aliases, no changes) |
| **pogod** | unchanged; both pollers run under user launchd, not pogod |

### Why one repo, not two

- Shared install/uninstall lifecycle — one `install.sh` brings up both directions atomically.
- Shared state directory (`~/.pogo/reminders/`) — discovery from one place.
- They WILL evolve together as the "mg ↔ Apple" bridge accretes features (sender filters, do-not-disturb, message threading, etc.). Splitting now means merging later.
- Two repos doubles the launchd plist count and mental footprint without improving isolation; the launchd `Label`s are already isolated.

### Why launchd, not pogod-supervised

Same reason `pogo-reminders` already uses launchd: pogod doesn't need to know about Apple-specific glue. launchd supervises both pollers, restarts on crash, runs at login. Zero surface area in pogod. If pogod is down, the notifier still works. If the notifier dies, pogod is unaffected.

### Optional: repo rename

`pogo-reminders` is a slight misnomer once it does both directions. Daniel may want to rename to `pogo-apple`, `pogo-bridge`, or similar. **Out of scope for v0** — cosmetic, can be done with `gh repo rename` later. Keeping the name preserves all existing references in CLAUDE.md, install instructions, etc.

---

## v0 scope (explicit)

**In:**

- `human`-canonical addressing convention (Part A) — prompt edits only, no code.
- New `bin/poll-mail.sh` + `launchd/com.pogo.notify.plist` inside the existing `pogo-reminders` repo (Part B).
- macOS notification on new mail to `human`.
- Single "Pogo Inbox" Reminders list mirror.
- `notify-seen.json` dedupe (separate from inbound `seen.json`).
- `install.sh` extension to bring up both pollers idempotently.
- README/refresh framing pogo-reminders as the bidirectional Apple bridge.

**Out (deferred):**

- **Reply from notification.** Stays through inbound Reminders (Apple Reminders → mayor).
- **Multi-machine sync.** Out of scope for v0 (per Daniel) — addressable later via per-machine prefix in `notify-seen.json` if needed.
- **Multi-operator addressing.** Single-user assumption; `human-<name>` is the natural extension when needed.
- **In-mg notification hooks.** mg core stays a content-agnostic Maildir; notification is purely a polling consumer.
- **Per-sender filters or priority levels.** All mail to `human` notifies. If Daniel finds it too noisy, add a filter list in v1.
- **Read receipts.** No "user saw the message" signal back to the sender.
- **Encryption / privacy controls.** Maildir is plaintext on local FS; existing trust boundary.
- **Polling `human/cur/`.** New mail only — once read (moved to cur/), the notifier's job is done.
- **Repo rename.** Cosmetic; defer.

---

## Failure modes & mitigations

| Mode | Mitigation |
|---|---|
| **Poller crashes** | launchd `KeepAlive=true` restarts it, same as pogo-reminders. |
| **Notification permission revoked** | Poller logs the failure to `~/.pogo/notify/poller.log`; Reminders mirror still works. README documents resetting permission. |
| **Reminders permission revoked** | Notification still works. Log the failure. README has reset instructions. |
| **`seen.json` corrupts** | Same recovery as pogo-reminders: delete it; first poll re-notifies all unread; minor noise but no data loss. |
| **`new/` grows unbounded** | Daniel reads via `mg mail read` which moves to `cur/`. Notifier polls only `new/`, so naturally bounded. If Daniel never reads, `new/` keeps growing — that's already the existing behavior of mg. |
| **Mail with sensitive contents in Reminder notes** | Notes go to iCloud (via Reminders sync) — same trust boundary as any Reminder. Document this. If Daniel cares, the notification-only path (no Reminders mirror) is a config flag. |
| **Notifier double-fires after launchd restart** | `seen.json` prevents this. Confirmed pattern in pogo-reminders. |
| **Two notifiers running at once** | launchd Label uniqueness prevents this. If an old `bash` instance lingers, `seen.json` is the safety net. |

---

## Testing & rollout

1. **Land Part A (prompt edits)** as a series of one-line changes to mayor.md, architect.md, doctor.md, and the polecat template. Verify by sending test mail to `human` and confirming it lands in `~/.macguffin/mail/human/new/`. Confirm mayor stops forwarding.
2. **Build the outbound poller in `pogo-reminders`**: add `bin/poll-mail.sh` + `bin/notify.sh`. Run manually (no launchd yet) and verify notifications + Reminders mirror end-to-end against a real `human` mailbox.
3. **Wire `launchd/com.pogo.notify.plist`** and update `install.sh` to install both plists. Run `install.sh` and verify boot-time start and crash-restart.
4. **Soak for a few days** with real PM digests (once mg-69cb lands) and ad-hoc agent mail. Tune notification body length and Reminders title format.
5. **If usable, ship.** If notification volume is wrong, iterate (filter list, digest mode) before promoting more broadly.

---

## Decided (per Daniel's feedback)

1. **Single "Pogo Inbox" Reminders list** — confirmed.
2. **Notification grouping** — implementer's call (macOS handles grouping by app identifier; `terminal-notifier` groups under one identity by default).
3. **Poll `cur/`?** — implementer's call (recommendation stands: `new/` only — once read, it's done).
4. **Multi-machine sync** — out of scope for v0.

---

## What's NOT in scope (reiterated)

- Bidirectional Apple bridge.
- Reply from notification.
- Multi-operator support.
- pogod plugin form.
- Cross-platform (Linux, Windows) versions.
- Replacing `mg mail read` for terminal use — terminal still works exactly as today.

---

## Summary of recommendations

1. **Address pattern:** `human` is canonical. **No mg core changes** — convention is enforced via crew/polecat prompt files. Plugs into `pogo status` colorization. Mayor inbox is coordination only; mayor stops forwarding.
2. **Notification service:** **fold into existing `pogo-reminders` repo** as a second launchd-supervised poller (`bin/poll-mail.sh`). macOS notification + "Pogo Inbox" Reminders list mirror. pogo-reminders becomes the bidirectional Apple bridge / remote interface to pogo.
3. **v0 is read-only.** Reply path stays through inbound Reminders.
4. **Total surface area:** ~5 prompt edits (one-liners), ~150 LOC new shell+JXA in pogo-reminders, one new launchd plist, install.sh extended to bring up both pollers.
5. **No changes to:** mg core, pogod, refinery, mg schema, work-item format, or any non-mail subsystem.
