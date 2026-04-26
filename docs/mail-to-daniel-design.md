# `mg mail` → Daniel Notification — Design & Recommendation

**Status:** design / recommendation. Not implemented.
**Origin:** mg-151b. **Author:** architect.
**Sibling docs:** `product-manager-design.md` (PMs depend on this path for digests).
**Companion repo (existing):** `pogo-reminders/` — the inverse path (Reminders → mayor).

## TL;DR

Two decisions to make.

**(a) Addressing pattern.** Standardize on `daniel` as Daniel's mailbox. `human` becomes a **server-side alias** that delivers to `daniel`. Mayor's inbox is for coordination only — no more "mayor reads mail meant for Daniel." Tiny change to mg: an alias map in mail send/list. ≤ 50 LOC.

**(b) Outbound apple-side service.** New repo `pogo-notify` (sibling to `pogo-reminders`). One launchd-managed shell script that polls `~/.macguffin/mail/daniel/new/`, fires a macOS user notification per new message, and writes the body to a "Pogo Inbox" Reminders list (so Daniel can read on iPhone too). State file tracks delivered IDs. Notification deep-links to `mg mail read daniel/<id>`. v0 is read-only; reply path stays through Reminders. ~150 LOC, mirrors `pogo-reminders` shape exactly.

**Decision needed from Daniel:** approve `daniel` as canonical address with `human` alias, approve the `pogo-notify` shape (separate repo, launchd, mirrored from reminders), and the v0 scope below. Implementation is small and low-risk.

---

## Part A — Addressing pattern

### Current state (observed)

| Address | Who uses it | Purpose |
|---|---|---|
| `daniel` | architect, mayor (recently) | Direct mail to Daniel — already exists with 5 messages in `new/` |
| `human` | (no mailbox exists; agents *say* "human" sometimes) | Generic "needs-human" placeholder |
| `mayor` | everyone | Coordination — but mayor effectively forwards "user-facing" subjects |
| `architect`, `doctor`, `pm-*` | named agents | Other crew |

**Problem:** Inconsistent. Some flows aim at `daniel`, some at `human`, some at `mayor` expecting the mayor to decide whether to pass it on. This makes notification routing impossible — the apple-side service has no single "is this for Daniel?" signal.

### Recommendation: `daniel` is canonical, `human` is a server-side alias

**Rules:**

1. **`daniel` is the user's mailbox.** All user-facing mail addresses `daniel` directly.
2. **`human` is an alias** that mg resolves to `daniel` at send time. Agents that don't know the operator's name (e.g., a generic template) can address `human` and the mail lands in `daniel/new/`.
3. **`mayor` is for coordination only.** Mayor stops being a forwarding inbox. If an agent has something for the user, it sends to `daniel` (or `human`); never to `mayor` "expecting it to be passed on."
4. **Exactly one user mailbox per machine in v0.** Single-operator assumption, matches reality. (Multi-operator is a real future need; see "Open question" below.)

### Why not "always `human`"

It's portable but loses the explicit-is-better-than-implicit gain. Agents that DO know the operator's name (most of them — Daniel is referenced by name throughout) should use it. `human` is the fallback, not the default.

### Why not "always `daniel`"

Generic agent templates and shared prompts shouldn't have the operator's literal name baked in. `human` gives them a portable address that still lands correctly.

### Why not "mayor reads it"

Mayor-as-forwarding-inbox is the current bug. (1) Mayor has its own queue and shouldn't context-switch into "is this for me or for Daniel?" parsing. (2) Notification routing is impossible if user-facing mail and coordination mail are mixed in one inbox. (3) The mayor crew agent prompt becomes simpler when its inbox is unambiguous.

### Required mg changes (small)

Two changes to `internal/mail/mail.go` + one flag plumbing in `cmd/mg/mail.go`:

```go
// internal/mail/mail.go
var aliases = map[string]string{
    "human": "daniel",
}

func resolve(name string) string {
    if target, ok := aliases[name]; ok {
        return target
    }
    return name
}

// Update Send to resolve aliases at delivery time:
func Send(mailRoot, recipient, from, subject, body string) (string, error) {
    recipient = resolve(recipient)
    // ... rest unchanged
}
```

Decisions baked in:

- **Resolve at send time, not list/read time.** This way the on-disk Maildir is canonical (`daniel/new/...`), and the apple-side service polls one path.
- **Alias map is hard-coded for v0.** A single line. Daniel's username is the only alias target. If/when there's a second operator, lift to a config file (see open question).
- **No new flags, no schema changes, no migration.** The 5 existing messages in `daniel/new/` stay where they are.
- **`mg mail list human`** could either error ("did you mean daniel?") or transparently resolve. Recommendation: **transparent resolve** — symmetry with send. A user who types `mg mail list human` sees the daniel inbox.

That's it. ~30 LOC change in mg, plus a sentence in the mg manpage / `--help` output.

### Mayor prompt update (also small)

The mayor prompt (and any agent prompt that says "mail this to Daniel via mayor") needs a one-line edit: "Mail Daniel directly with `mg mail send daniel ...` — do not route through mayor's inbox."

This is a prompt change only, no code.

### Open question (not blocker for v0)

**Multi-operator workspaces.** A single machine running pogo for multiple humans needs `daniel` and `alice` as parallel mailboxes, with `human` resolving based on context. Out of scope for v0; the alias map design supports it trivially when needed. Capture as a separate ticket if it materializes.

---

## Part B — Outbound apple-side notification service

### Requirements (restated)

1. **Notification.** Push-style alert on macOS when new mail arrives in `daniel/new/`.
2. **Read access.** Daniel reads message body without opening a terminal.
3. **No required action.** Read-only. Reply path stays through `pogo-reminders`.
4. **Don't integrate into mg core.** Separate consumer.
5. **Mirror the `pogo-reminders` shape** where sensible.
6. **Don't make bidirectional yet.**

### Recommendation: `pogo-notify` — a sibling repo to `pogo-reminders`

A new repo at `~/dev/pogo-notify/` with the same skeleton as `pogo-reminders`:

```
pogo-notify/
├── README.md
├── install.sh             # idempotent setup, mirrors pogo-reminders/install.sh
├── uninstall.sh
├── bin/
│   └── poll-mail.sh       # the long-running poller (osascript for notification + Reminders write)
└── launchd/
    └── com.pogo.notify.plist
```

State directory: `~/.pogo/notify/` — symmetric with `~/.pogo/reminders/`.

```
~/.pogo/notify/
├── seen.json              # set of delivered message IDs (to dedupe)
└── poller.log
```

### How it works (per poll cycle)

```
1. List entries in ~/.macguffin/mail/daniel/new/
2. For each entry not in seen.json:
   a. Parse headers (From, Subject, Date) and body — same parser as the existing reminder script uses for JSON.
   b. Fire a macOS user notification:
        title    = "[mg] <From>: <Subject>"
        body     = first ~300 chars of message body
        url      = "pogo://mail/read/daniel/<msg-id>"  (or `mg mail read daniel/<id>`)
   c. Append a Reminders item to the "Pogo Inbox" list (so Daniel sees it on iPhone too):
        title = "[mg] <From>: <Subject>"
        notes = full body + "msg-id: <id>"
   d. Add <id> to seen.json.
3. Sleep POLL_INTERVAL.
```

State persistence is the same `seen.json` pattern as `pogo-reminders/poll-reminders.sh` — it has been working there, no need to invent.

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
| **Poll mail dir, dedupe, fire notification, write Reminder** | `pogo-notify/bin/poll-mail.sh` |
| **launchd config (KeepAlive=true, RunAtLoad=true)** | `pogo-notify/launchd/com.pogo.notify.plist` |
| **macOS notification permission, Reminders permission** | Granted at first run via TCC prompt; documented in README |
| **`daniel` ↔ `human` alias** | `mg` core (~30 LOC change in `internal/mail/mail.go`) |
| **mg core mail logic** | unchanged otherwise |
| **pogod** | unchanged. pogo-notify runs under the user's launchd, not pogod |

### Why a separate repo, not a `pogod` plugin

`pogo-reminders` is already a separate repo. Symmetry. Both are Apple-side glue, not core pogo logic. Keeping them out of `pogo/` keeps the main repo platform-portable (pogo runs on Linux too, eventually). A pogod plugin would couple Apple platform code to the daemon's lifecycle for no benefit.

### Why launchd, not pogod-supervised

Same reason `pogo-reminders` uses launchd: pogod doesn't need to know about Apple-specific glue. launchd already supervises it, restarts on crash, runs at login. Zero surface area in pogod. If pogod is down, the notifier still works. If the notifier dies, pogod is unaffected.

---

## v0 scope (explicit)

**In:**

- `daniel`-canonical addressing with `human` alias (Part A).
- `pogo-notify` repo + script + launchd plist (Part B).
- macOS notification on new mail.
- "Pogo Inbox" Reminders list mirror.
- `seen.json` dedupe.
- `install.sh` / `uninstall.sh` mirrored from pogo-reminders.

**Out (deferred):**

- **Reply from notification.** Stays through pogo-reminders (Apple Reminders → mayor).
- **iOS-side push notifications** beyond what Reminders sync gives for free.
- **Multi-operator addressing.** Single-user assumption.
- **In-mg notification hooks.** mg core stays a content-agnostic Maildir; notification is purely a polling consumer.
- **A unified "Apple bridge" daemon.** pogo-reminders and pogo-notify stay separate; if a unified shape emerges later, merge then.
- **Per-sender filters or priority levels.** All mail to `daniel` notifies. If Daniel finds it too noisy, add a filter list (sender allowlist / priority threshold) in v1.
- **Read receipts.** No "Daniel saw the message" signal back to the sender. Add only if a workflow needs it.
- **Encryption / privacy controls.** Maildir is plaintext on Daniel's local FS; that's the existing trust boundary.

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

1. **Land Part A (alias)** as a separate, mergeable mg change. Verify mailing `human` lands in `daniel/new/`. Update mayor prompt to stop forwarding.
2. **Build pogo-notify** with the scaffolded poller; manually run it (no launchd yet) and verify notifications + Reminders mirror.
3. **Wire launchd plist**, run `install.sh`, verify boot-time start and crash-restart.
4. **Soak for a few days** with PMs sending real digests. Tune notification format and Reminders body as needed.
5. **If usable, ship.** If notification volume is wrong, iterate (filter list, digest mode, etc.) before promoting to default for other pogo users.

---

## Open questions (non-blocking)

1. **Single Reminders list "Pogo Inbox" vs. per-sender lists?** Recommend single list for v0; per-sender risks list explosion. If discoverability becomes an issue, add prefix tags in titles.
2. **Notification grouping?** macOS notification grouping is keyed on app identifier; `terminal-notifier` groups under one identity by default. Acceptable for v0.
3. **Should pogo-notify also poll `daniel/cur/`** to catch read-then-edited cases? No — once read, it's done. `new/` only.
4. **Multi-machine sync?** If Daniel ever runs pogo on two machines, both would notify on the same mail. Out of scope; addressable via a per-machine prefix in `seen.json` if needed.

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

1. **Address pattern:** `daniel` canonical, `human` aliased to `daniel` in mg core. Mayor inbox is coordination only.
2. **Notification service:** new sibling repo `pogo-notify`, launchd-managed shell poller, macOS notification + Reminders list mirror, mirrors `pogo-reminders` shape.
3. **v0 is read-only.** Reply path stays through `pogo-reminders`.
4. **Total surface area:** ~30 LOC change in mg, ~150 LOC new shell+JXA in pogo-notify, two prompt edits (mayor + any "send to human" agent that wants the new pattern).
5. **No changes to:** pogod, refinery, mg schema, work-item format, or any non-mail subsystem.
