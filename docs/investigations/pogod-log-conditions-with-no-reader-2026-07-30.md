# pogod log conditions that have an actor who never hears — 2026-07-30 (mg-c3f0)

**Question.** Not "how do we route the prompt-sync line." It is: *which pogod log
conditions have an agent who could act on them, and how does that agent find out?*

**Answer, in one line.** Of ~90 log statements in `cmd/pogod`, **15 conditions have an
identifiable actor and no channel to reach them**; the fleet is otherwise more
instrumented than the ticket assumed — 6 watcher packages and 12 ad-hoc sites already
mail an actor, and the notify-on-transition-with-renotify machinery the ticket asks for
**already exists three times over**. The prompt-sync conflict was not missing a
mechanism; it was the one condition that never got wired to the mechanism beside it.

Origin: architect, mg-3ebe item 5, lifted by mayor 2026-07-30. Reconcile of the specific
stale file is mg-4999 (deliberately separate). Point-in-time record; the anchor is the
date and the work item.

---

## 1. The premise, re-verified — and the seven days are now confirmed by log evidence

The ticket confirmed the `⚠ prompt sync DECLINED` line on 07-29 and 07-30 and inferred
"at least seven days". Direct measurement of the retained logs extends the confirmed
range **back to 07-23**, which closes the inference:

| Log file | occurrences | dates |
|---|---|---|
| `pogod.log` | 3 | **2026/07/23**, 2026/07/29, 2026/07/30 |
| `pogod.log.1` | 1 | (rotated) |
| `pogod.log.2`, `.3` | 0 | — |

So the span is **exactly seven days, evidenced**, not estimated. One refinement to the
ticket's framing: that is **4 retained firings, not 7** — pogod does not boot daily and
the log rotates. It matters only because it sets the honest bar for the rate limit: the
alarm needed to survive ~4 repetitions in a week, not 7.

The condition is **still live**: `~/.pogo/agents/mayor.md.dist` exists on disk as of
this writing, alongside a `mayor.md.bak-1784309533` from an earlier `--force`.

**Why it recurs at every boot, mechanically.** `InstallPrompts` leaves the user-edited
canonical untouched, so that file keeps its **old** embed stamp. The next boot compares
the same stale stamp against the same shipped embed, declines again, and rewrites an
identical `.dist`. The conflict set is therefore *re-derived identically at every boot* —
it is a steady state, not an event. Any in-process suppression is useless here, because
**the process restart is the tick.**

## 2. The reframe: there is no "WARNING and above" to read

The ticket asks to start by reading what pogod logs "at WARNING and above." That filter
**does not exist**. `pogod` uses the stdlib `log` package with no levels — `grep -rn
"log/slog"` over the tree returns nothing. Severity is carried by ad-hoc string
convention, and there are at least five mutually inconsistent ones:

- `⚠` (prompt sync)
- `WARNING:` (unknown agent provider — 4 sites)
- ALL-CAPS predicates: `DECLINED`, `UNDELIVERED`, `DIED`, `PRESERVED`, `KEPT`,
  `SUPPRESSING`, `NOT FRESHENED`, `FIRED`
- `NOT armed` / `disabled` / `skipped` (degradation, indistinguishable from
  correct-by-design quiet)
- bare `failed:` (the majority)

This is a second-order cause of the original incident and worth stating on its own: **you
could not filter this log for actionable conditions even if you were reading it.** A
`pogod.log` reader cannot separate `pogod: git GC disabled` (correct, configured) from
`pogod: git GC skipped — cannot load work items` (broken, silent data growth). Both are
one lowercase line in the same stream.

## 3. Is mailing an agent actually a read channel? Measured, because the ticket's whole constraint rests on it

The ticket forbids routing to `human` because that maildir "has ~800 unread going back to
April." Verified and updated, and the contrast is much sharper than the ticket claimed:

| mailbox | unread |
|---|---|
| `human` | **988** |
| `mayor` | **0** |
| `doctor` | 0 |
| `pm-pogo` | 1 |
| `pa` | 1 |
| `architect` | 9 |
| **worst of all 269 crew-shaped mailboxes** | **10** |

Fleet-wide: 1109 mailboxes, 4279 unread. `human` alone holds **23%** of it, and 988 is
**98× the worst standing crew mailbox** and 3.6× the entire crew population's backlog
combined (275). A third trap surfaced en route that the ticket does not mention: **839
polecat-shaped mailboxes hold 3016 unread** (worst: `cat-mg-8c66` at 567). Mail to a
reaped polecat is unread *forever* — so "mail the affected agent" is only a read channel
when the affected agent is **standing**, not ephemeral.

**Conclusion.** Mailing a standing crew agent is an empirically-drained channel (0–10
backlog). Mailing `human` is empirically not. Mailing a dead polecat is the same trap as
`human` with extra steps. The ticket's constraint is correct and now has numbers behind
it.

## 4. The enumeration

Ranked by consequence. "Actor" is the party who could act; "hears?" is whether any
channel other than `pogod.log` carries it.

### Class A — actionable, and the actor never hears (the defect class, 15 conditions)

| # | Condition | Site | Actor | Consequence if unread |
|---|---|---|---|---|
| **A1** | `⚠ prompt sync DECLINED for <f>` | `main.go:2130` | the agent whose prompt it is | ran 13-day-stale guidance for 7 days. **FIXED — see §5** |
| **A2** | `scheduler load failed` | `main.go:1471` | coordinator / human | **highest severity.** No mail-check schedule fires for *anyone* — the fleet loses its proactive channel wholesale. See below |
| **A3** | `ack-watch NOT armed` / `deaf-watch NOT armed` | `main.go:1696`, `1729` | coordinator | the two watchers that exist to detect fleet deafness, silently disabled — **by the same failure they would have caught** |
| **A4** | `prompt refresh failed: <err>` | `main.go:2037` | human / platform | **every** prompt stays stale. Strictly worse than A1, and gets *less* annunciation than A1 did |
| **A5** | `auto-start of <agent> failed` | `main.go:2069` | coordinator — *unless the failed agent IS the coordinator* | a crew agent that should be running isn't, and nothing says so. Genuinely hard case (§6) |
| **A6** | `agent <a>: restart failed` | `main.go:1386` | coordinator | a crashed crew agent that failed to respawn is simply **gone** |
| **A7** | `WARNING: unknown agent provider <p>; falling back` | `main.go:801`, `agent.go:678` | human (config author) | agents silently run on a **different provider than configured** |
| **A8** | `POGO_HOME too deep to hold unix sockets` | `main.go:1097` | human | socket-dependent features degraded |
| **A9** | `git GC skipped / orphan scan disabled / sweep failed` | `gitgc.go:70,82,91,121` | platform maintainer | branch + worktree accumulation, unbounded and silent |
| **A10** | `role-default pin failed` | `rolepin.go:32` | human / platform | role names unpinned mid-boot |
| **A11** | `failed to write own heartbeat` | `main.go:1748` | platform | the tier-1 reaper's evidence *about pogod itself* stops updating |
| **A12** | `platform sleep shim unavailable` | `main.go:1832` | platform | wake detection degraded to heartbeat-only |
| **A13** | `gh-issue teardown detector NOT armed — gh not on PATH` | `main.go:1636` | human | teardown detection off |
| **A14** | `log rotation failed` | `main.go:996` | platform | the post-mortem log the *other* 14 rely on may be lost |
| **A15** | preserved/undetermined-worktree **mail failure** | `worktreecleanup.go:106,132` | coordinator | mail is attempted; when it fails, nothing records it (`worktreecleanup.go` emits **no events at all**) |

**A2 deserves its own paragraph** because it is a strictly worse instance of this
ticket's own pathology and nobody has filed it. If `scheduler.New` fails, then: no
mail-check schedule fires for any agent; `ackwatch` and `deafwatch` both refuse to arm
(they log `NOT armed` and return); and the sole annunciation of total fleet deafness is
one lowercase line at boot in the log this ticket exists because nobody reads. The
watchers designed to catch "the fleet has gone deaf" are **structurally disabled by the
one failure that causes it.** Mail itself is unaffected — `client.SendMGMail` shells out
to `mg` and does not depend on the scheduler — so an alarm *is* deliverable here. This is
the next ticket, and it is bigger than A1 was.

### Class B — already reaches an actor (do not touch; the ticket's "write-only log" premise is too strong)

The pattern the ticket asks for is already built. Six watcher packages —
`ackwatch`, `deafwatch`, `driftwatch`, `ghteardown`, `credexpiry`, `synthwatch` — each
carry `NotifyTo`, an `interval`, a `RenotifyAfter`, and a findings **fingerprint**
(`lastPrint`/`lastMailed`), which is exactly "notify on transition into the condition,
re-notify only if still unresolved." All five of the mailing ones also record
`mail_error` **on the event spine**, so a failed notification is structured data, not
just a log line.

Also already routed: refinery merge-failure (mails author **and** coordinator), the
defer-done backstop and deferred-death escalations, orphaned polecats, sentinel drift,
workspace-freshen (`internal/agent/workspace.go` — the direct precedent for A1),
stall-watch (PTY nudge, mail fallback), mail-check registration failure
(`schedule_register_failed` + mayor escalation), usage-limit, modal dismissal.

**Two caveats on Class B, both worth carrying forward:**

1. **`synthfail` pages `human`** — the channel this ticket names as broken, now measured
   at 988 unread. Arguably still correct: a dead credential genuinely needs a human and
   *no agent can act*, which is the honest reason it is not Class A. But "a human has been
   paged" in `main.go:1374` should not be read as "someone will know."
2. **`main.go:897` is the sharpest instance of this ticket's class in the entire tree,
   and it is self-aware.** The comment reads: *"it must not be inferable only from a JSON
   field in a log nobody reads"* — and the remedy it then applies is `log.Printf` into
   pogod.log. The author correctly identified that the log is unread and used it as the
   last-resort channel in the same breath. That is the class in miniature.

### Class C — correctly quiet, no actor, do not alarm

`no config file → skipping prompt refresh and crew auto-start`; `stall watcher not
armed` (no auto-started coordinator to nudge); `crew auto-start disabled`; `git GC
disabled`; `reaper disabled`; `<agent> already running, skipping auto-start`; every
`... enabled (...)` boot banner. These are configured states, not faults. Alarming them
would be the firehose the ticket warns against.

### The meta-finding: the unread log is also the *failure sink for the channels that do work*

12 ad-hoc notification sites degrade to `log.Printf` when their mail send fails — 7 in
`main.go` (944, 1286, 1319, 1911, 1923, 1929, 1940), 2 in `worktreecleanup.go`, plus
`sentineldrift.go:275`, `workspace.go:163`, `scheduler.go:955`. So `pogod.log` is not
merely where *un-routed* conditions go; it is where **routed conditions go to die when
routing fails.** The six watcher packages are exempt (they attach `mail_error` to an
event), which is precisely why the shared watcher pattern should be preferred over an
ad-hoc `SendMGMail` + `log.Printf` pair — and why the fix in §5 puts its own send failure
on the spine rather than only in the log.

## 5. What was implemented, and why only this

**Mail the agent whose prompt was declined, at the decision point, on transition.**
`cmd/pogod/promptsyncnotify.go`, wired into the boot path immediately after
`promptRefreshLogLines`.

Against the ticket's four constraints:

- **Alarm the agent that can act.** `promptSyncAddressee` resolves the conflicted path
  the way `ListPrompts` does: `mayor.md` → the **configured coordinator name** (the file
  is always `mayor.md`, but the agent it starts as follows `[agents] coordinator` —
  hardcoding `"mayor"` would misroute on any consumer who renamed it, and mail to a name
  no agent reads is silently accepted into a phantom mailbox and lost); `crew/<n>.md` →
  `<n>`; anything else — `templates/polecat*.md`, `pm/pm-template.md`, nested or empty
  stems — falls back to the coordinator with `owned=false`. **No branch synthesizes a
  name from a path it did not recognize.**
- **Rate-limit by condition, not by message.** State is `~/.pogo/prompt-sync-notices.json`,
  keyed by path, holding the **content hash of the `.dist`** plus the last delivery time.
  Notifies on: first sight (transition in), a *changed* declined update (the divergence
  grew, so the recipient's merge job is bigger than the one they were told about), or 72h
  elapsed while still unreconciled. A reconciled conflict is **forgotten**, so a
  recurrence is a fresh transition rather than inheriting a stale quiet window. It must be
  on disk because, per §1, the process restart is the tick.
- **No log tailer.** It runs where the conflict is known, in-process, from the
  `InstallResult` pogod already holds. It runs **before** the auto-start sweep, so the
  notice is already in the affected agent's maildir when that agent starts — it hears on
  its very first mail-check of the boot that declined its prompt.
- **It can fail loudly.** Three properties. (1) A `prompt_sync_declined` event is emitted
  for **every** conflict on **every** boot, including suppressed ones, so a persisting
  condition and a notifier that has quietly stopped are both visible on the spine — a
  silent notifier reads as a run of `notified:false, reason:new`, which is not the same
  shape as a fleet with no conflicts. (2) A **failed send is not recorded as delivered**,
  so the next boot retries; there is no path where a clean-looking state file claims an
  announcement that never happened. (3) Every store failure biases toward **noise, never
  silence** — a corrupt or unreadable state file makes the notifier forget and re-announce
  (worst case: a duplicate mail), because failing toward silence is the defect being
  fixed.

The mail states the remedy as a **decision, not a command**: it shows `diff -u` and
deliberately does not hand out `cp <f>.dist <f>`. The only reason the canonical file was
preserved is that the local edits might be load-bearing; a paste-ready copy-over would
hand out the single destructive action this mechanism exists to prevent, with the
daemon's authority behind it. (Same posture as `remedyFor` in `workspace.go`, and pinned
by test.)

**Rate chosen: 72h.** Not daily. Reconciling a prompt is a judgement about which local
edits still matter, not a command to run; a nag arriving faster than the work can
reasonably be scheduled trains the recipient to filter it — which is the original
incident's failure mode, one level up. 72h puts ~2 reminders in the seven-day window that
went unnoticed, against the 4 identical log lines that actually fired.

## 6. Deliberately not built

- **A2–A15.** Each needs its own decision about who to address, and two are genuinely
  hard rather than merely unwritten. **A5**: when the agent that failed to auto-start *is*
  the coordinator, the actor is the thing that failed, so the only remaining addressee is
  `human` — the channel measured at 988 unread. **A2**: mailing the coordinator works
  (mail does not depend on the scheduler), but "the fleet has no mail-check loops" also
  means the coordinator will not be *woken* to read it, only mailed. Neither is solved by
  copying §5's shape, so neither was copied.
- **A generic pogod-condition notifier.** Tempting and wrong at this size. The six
  watcher packages already are that abstraction; the right move for A2–A15 is to route
  each through the existing watcher pattern (which records `mail_error` on the spine) and
  *delete* ad-hoc `SendMGMail` + `log.Printf` pairs, not to add a sixteenth bespoke one.
- **Log levels.** §2 is real and is the reason the log cannot be triaged, but retrofitting
  `slog` across ~90 call sites is not "clearly right and small," and it does not fix
  anything on its own: a *filterable* log nobody reads is still a log nobody reads.

## 7. Verdict

The enumeration is the durable finding. `pogod.log` is genuinely write-only with respect
to the fleet, **and** the fleet already owns the machinery to fix that — three
independent implementations of notify-on-transition-with-renotify, plus an event spine
and a demonstrably-drained per-agent mail channel (0–10 unread vs. `human`'s 988). The
prompt-sync conflict was never a missing mechanism. It was the one condition nobody wired
to the mechanism sitting next to it, and it stayed that way for seven days because the
line reporting it was correct, actionable, and addressed to nobody.

**A2 is the next ticket and is larger than this one was.**
