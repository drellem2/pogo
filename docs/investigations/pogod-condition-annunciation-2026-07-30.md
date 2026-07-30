# Wiring mg-c3f0's remainder: 12 of 14 pogod conditions now reach an actor — 2026-07-30 (mg-342d)

**Companion to** `pogod-log-conditions-with-no-reader-2026-07-30.md` (mg-c3f0), which is the
enumeration and stays the authority on *which* conditions have an actor and no reader. This
document is the **disposition**: what was wired, what was declined and why, what was proved by
forcing it, and what is left.

Origin: mg-342d, filed by mayor 2026-07-30 as mg-c3f0's successor. mg-c3f0 fixed A1 and left A2–A15;
this closes 12 of the 14 and declines 2 with reasons.

**The thing to take from this document if you read nothing else.** The alarm is not the artifact.
Twelve conditions were wired in about 90 lines of decision-point calls; the artifact is
`cmd/pogod/conditionnotify.go`'s suppression contract and
`scripts/pogo-condition-controls.sh`, which forces each condition against a live daemon and reads
the mail back out of a real maildir. mg-c3f0's whole history is a correct alarm nobody received, so
"the code is right" was never the claim in question.

---

## 1. Disposition of all fifteen rows

| # | Condition | Wired? | Actor | Live control |
|---|---|---|---|---|
| A1 | `⚠ prompt sync DECLINED` | mg-c3f0 | the agent whose prompt it is | mg-c3f0 |
| **A2** | `scheduler load failed` | **mail + WAKE** | coordinator | **yes** — corrupt `schedules.json` |
| A2b | `scheduler disabled (cannot resolve home)` | mail + WAKE | coordinator | no — needs `os.UserHomeDir()` to fail |
| **A3** | `ack-watch NOT armed` | mail | coordinator | **yes** — rides A2's boot |
| A3b | `deaf-watch NOT armed` | **no — unreachable** | — | see §2 |
| **A4** | `prompt refresh failed` | mail | coordinator | **yes** — unwritable `agents/` |
| **A5** | `auto-start of <a> failed` | mail | coordinator | **yes** — the coordinator arm, the hard one |
| **A6** | `restart failed` | mail | coordinator | no — no seam to force |
| **A7** | unknown agent provider | mail | coordinator | **yes** — bogus `provider =` |
| A8 | `POGO_HOME too deep` | **declined** | — | §3 |
| **A9** | git GC skipped / scan disabled / sweep failed | mail (4 ids) | coordinator | **yes** — ticket-index arm |
| **A10** | `role-default pin failed` | mail | coordinator | **yes** — read-only `config.toml` |
| **A11** | `failed to write own heartbeat` | mail | coordinator | **yes** — `health` as a file |
| A12 | `platform sleep shim unavailable` | **declined** | — | §3 |
| **A13** | gh-issue teardown NOT armed | mail | `[gh_teardown] notify_to` | no — pogod repairs PATH first |
| **A14** | `log rotation failed` | mail | coordinator | **yes** — 11 MiB log, read-only dir |
| **A15** | preserved/undetermined-worktree **mail failure** | **event, not mail** | — | **yes** — Go test, both arms |

`scripts/pogo-condition-controls.sh` prints this same not-controlled list on every run, so the gap
is stated by the instrument rather than only in prose. Current state: **21 assertions, all green**,
including the negative control. The `NEG A2` subset runs in `test.sh`, so it is on every merge.

**The controls have been observed going RED.** `Raise` was sabotaged into an unconditional early
return and the subset run against that build: **7 failures**, naming the missing delivery, the
missing wake, the missing suppression record and the missing clear. The instructive part is which
assertions did *not* fail — **all three negative controls still passed**, because a completely dead
annunciator does report "no live conditions" and does mail nobody. A clean boot looks identical
whether the mechanism is healthy or absent. That is this ticket's own subject appearing inside its
own test harness, and it is why neither direction can stand alone.

### Routing: one rule

**The configured coordinator, except A13** (which already owns `[gh_teardown] notify_to`, chosen
deliberately in mg-b586 because a teardown miss is a workflow failure the fleet chases).

The enumeration marks A4, A7, A10, A11 and A14 as "human" or "platform". Those are not mailboxes.
`human` is one, and it is measured at 988 unread — so addressing it writes down an intention rather
than reaching anyone. The coordinator can act on every row here: file a work item, dispatch a
polecat, escalate to a human *with context*. `TestNoConditionEverRoutesToHuman` pins it.

## 2. A3's second site is unreachable, and its stated reason is wrong

`deaf-watch NOT armed` (`main.go`) is guarded on `cfg.DeafWatch.Enabled && agentRegistry != nil`.
`agentRegistry` cannot be nil there — pogod `os.Exit(1)`s if `agent.NewRegistry` fails, hundreds of
lines earlier. **So the branch cannot execute in production**, and its message ("the agent registry
did not load") does not name a reachable state.

The deaf-watch degradation that *does* happen under A2 is different: `SetMailCheckProvider` is only
called on the scheduler path, so `MailLoopReport` errors — and that is **already instrumented**, as
`deaf_watch_error` on the event spine. So A3 is wired once (ack-watch), and A2's notice names the
deaf-watch consequence in its body rather than raising a second alarm for it.

This does not weaken the ticket's framing. A3's severity claim — *the watchers that exist to detect
fleet deafness, disabled by the failure they would have caught* — is correct; it is carried by the
ack-watch half plus deaf-watch's existing spine record, not by both listed log lines.

## 3. The two declines

**A8 — `POGO_HOME too deep to hold unix sockets`. Class C mislabelled as Class A.** Read the site:
pogod picks an alternate socket dir and says the sockets are "still unique to this POGO_HOME".
Nothing is disabled and nothing is lost — the enumeration's "socket-dependent features degraded" is
not what the code does. It is also **invariant for a given POGO_HOME**, so annunciating it would
create a permanent standing alarm clearable only by moving POGO_HOME. A standing alarm nobody can
clear is how the whole channel gets muted.

**A12 — `platform sleep shim unavailable`. An always-true precondition on any host without the
shim.** It would fire at every boot forever and never clear: a condition with no transition, which
is the one thing mg-c3f0's constraint forbids most directly. The fallback is correct by design —
the code says so in as many words ("hb alone is correct") — and no agent can install a platform
shim, so there is no remedy to state. If the shim's absence ever becomes a fault rather than a
configuration, the right instrument is a **wake-latency measurement**, not an annunciation of a
boot-time capability check.

Both declines are recorded in `cmd/pogod/conditions_test.go`, where
`TestEnumerationIsFullyDisposed` fails if any of A1–A15 is neither wired nor declined-with-a-reason.
That test is the answer to how the remainder avoids being lost a third time: the enumeration is no
longer prose that code can drift away from.

## 4. The architect's A2/A3 constraint: checked, and it splits in two

The ticket carries an architect note asserting that **no in-pogod notifier can ever carry A2 or
A3**, because "the channel that would report them IS the failed subsystem", and that the detector
must therefore live outside pogod's process tree as a launchd job. The note explicitly asks whoever
takes the ticket to check it rather than inherit it. Checked. It is **half right, and the half that
is wrong matters**, because acting on it as written would have left A2 with no in-fleet alarm at all.

**What is wrong: A2's literal condition is reachable and reportable from inside pogod.** Three
measurements:

1. **Mail does not depend on the scheduler.** `client.SendMGMail` shells out to `mg`. mg-c3f0 §4
   already verified this and said so: "an alarm *is* deliverable here."
2. **The heartbeat does not depend on the scheduler — it drives it.** `main.go`'s `hb.OnTick` reads
   `if sched != nil { sched.Tick(...) }`. A nil scheduler leaves the heartbeat ticking normally. So
   anything riding the heartbeat survives A2.
3. **Therefore the coordinator can be actively WOKEN on the A2 boot.** This is the gap mg-c3f0 §6
   stopped at — *"mailing the coordinator works, but the coordinator will not be woken to read it,
   only mailed"* — and it is now closed and measured. With the scheduler confirmed down, the control
   observes: `condition scheduler_load_failed (A2) mailed to mayor (new)` and then
   `condition scheduler_load_failed (A2) — mayor WOKEN to read the notice (try 1)`.

The wake is queued rather than sent inline, because A2 is known during startup — *before* crew
auto-start — so at the moment the condition is detected there is no coordinator process to nudge.
It is retried on the heartbeat and lands on the first tick after the addressee is up. A wake that
never lands is abandoned **loudly**, with the notice still in the maildir, and the log says the
condition is "mailed-but-not-woken" rather than going quiet.

**What is right, and remains unbuilt: the failure *class* still has no positive instrument.** The
architect's strongest point survives intact and should not be read as dismissed. `scheduler.New`
returning an error is one fault. A scheduler that **loaded and then silently stopped firing** —
panicking in `Tick`, a wedged heartbeat, a dead pogod — is a different fault, has **no decision
point inside pogod at all**, and nothing in mg-342d detects it. For that:

- **A log grep is the wrong instrument** and this is the part most worth keeping on the record. Both
  `grep "scheduler load failed"` and an errored grep return the same token as a healthy one. If the
  scheduler never loads there may be no line to find.
- **The observable that discriminates is the absence of expected fires** — schedule ack counts
  advancing. A watchdog asserting *"at least one mail-check has acked in the last N minutes"*
  detects the whole class without depending on any of its error paths.
- **It must live outside pogod's process tree**, for the reason `com.pogo.deploy` and
  `com.pogo.recovery` do: a supervisor cannot be a descendant of what it supervises, and
  `pogo-self-deploy`'s mg-1bbf ancestry guard exists to enforce exactly that separation.
- **And it must be proved against an injected failure before it is trusted.** `scheduler load
  failed` has fired **0 times ever** on this box (`pogod.log` through `.3`), so any detector built
  for it is unfalsified by construction. A watchdog for a never-seen condition, shipped untested, is
  the highest-risk artifact available here — which is a large part of why it is not in this change.

That watchdog is **not** wired, and mg-342d does not claim it. It is the honest residue and it wants
its own ticket, because it is a launchd job with an install path, a plist, and an injected-failure
proof — not three lines at a decision point.

Baseline for whoever builds it, measured 2026-07-30: 17 schedules registered, `mail-check-architect`
10/10 acked, `mail-check-mayor` 3/3, `mail-check-pa` 905/918. The scheduler is loaded and firing.

## 5. A5's conclusion, stated rather than hedged

The ticket asked for a conclusion on A5 and allowed "this one cannot be solved in-fleet" if argued.
The answer is **both, split on one condition**:

- **Failed agent ≠ coordinator: solved.** The coordinator is mailed, can start it, and the notice
  names the command.
- **Failed agent = coordinator: not solvable in-fleet, and the notice says so.** The actor and the
  casualty are the same process; there is no in-fleet reader *at the time*. The mail still goes to
  the coordinator's mailbox, because it is not lost — it is read on the first mail-check after the
  coordinator next starts, which is when the information becomes actionable. The subject and body
  differ from the ordinary case and state plainly that nothing read it at the time, name the window
  to audit (`mg list --status=available`), and say that a recurrence is an out-of-process
  supervision gap rather than a coordinator bug.

What it deliberately does **not** do: fall back to `human` (988 unread — that would look like
escalation and be silence), or synthesise a second addressee (mail to a name no agent reads is
accepted into a phantom mailbox and lost). `TestA5NamesTheCaseThatCannotBeDeliveredLive` pins the
wording, including that the coordinator-failed notice must **not** hand out
`pogo agent start mayor` — by the time anyone reads it, that agent is running.

The live control asserts this against the delivered artifact: it reads the body back out of the
maildir with `mg mail read` and checks for "no in-fleet reader". Not the string the code would have
sent — the bytes that arrived.

## 6. How this fails loudly, which is the part that is not optional

A notifier that silently stops is mg-c3f0's defect one level up, so:

1. **Every raise emits `pogod_condition`, including the suppressed ones.** A live condition produces
   a steady stream of `reason: suppressed`. This is what makes "quiet on purpose" distinguishable
   from "stopped working": a stopped notifier stops emitting *and* stops suppressing, and a live
   condition that is not being suppressed shows up as a run of `reason: new, notified: false` — a
   different shape, not a quieter one.
2. **A failed send is never stamped as delivered.** It is dropped from the store, so the next
   occurrence treats it as new and retries, and every attempt carries `mail_error` on the event.
   There is no path where a clean-looking state file claims an announcement that never happened.
3. **`pogod_condition_summary` fires on EVERY boot, including clean ones.** A daemon that boots and
   emits no summary is a daemon where this mechanism is not running — checkable, and a different
   shape from a daemon with nothing to report. The negative control asserts this specifically,
   because a summary that only appears when something is wrong is a summary whose silence means
   nothing.
4. **An unroutable condition is louder than the condition.** An empty addressee never guesses a
   name; it logs `⚠ ... has NO ADDRESSEE`, emits `reason: unroutable`, and counts as a failure.
5. **Store failures bias toward noise, never silence** — a corrupt store makes the notifier forget
   and re-announce. But the naive form of that bias mails on every occurrence, and A9/A11 occur on a
   timer, so an in-process shadow bounds an unreadable store to the same floor as a healthy one.
   Both directions are pinned by test.

Observable as: `pogo events --type pogod_condition`, `--type pogod_condition_cleared`,
`--type pogod_condition_summary`, `--type worktree_notice_undelivered`.

### The suppression contract, and where it is stricter than A1's

A1 fingerprints the content hash of a `.dist` sidecar — stable by construction. These fingerprints
are **error strings**, which carry pids, ports, temp paths and byte offsets. A fingerprint that
varies per occurrence reads as "materially changed" every time and mails on every boot, or for A11
every 30 seconds. So there is a **hard floor of one mail per condition id per hour**, enforced
regardless of fingerprint change. `TestRaise_HardFloorSurvivesAChurningErrorString` drives 200
occurrences with 200 distinct error texts and asserts ≤2 mails *and* ≥1 — the floor must bound
noise, never manufacture silence. The live A11 control measures the same property against a real
daemon on a 3-second heartbeat.

Renotify is **24h**, not A1's 72h: these are outages with mechanical remedies rather than judgement
calls about which local edits are load-bearing. The clock runs from the last **delivery**, not the
last occurrence — otherwise a condition recurring faster than the window would never reach a
reminder at all, which for A11 (every 30s) would be a silent and total failure.

## 7. Why one shared mechanism, against mg-c3f0 §6

mg-c3f0 §6 argued against "a generic pogod-condition notifier" and recommended routing A2–A15
through the existing watcher packages. That was right at the size it was written for, and it is
wrong at this one, for a reason mg-c3f0 could not have weighed while fixing a single row:

**A2–A15 are not pollable.** The six watcher packages sample a live subsystem on an interval.
`scheduler load failed` is not a state you can sample later — the scheduler either loaded on this
boot or it did not, and by the time an interval elapses the only surviving trace is the log line
nobody reads. These are boot-time and decision-point facts, so they need A1's shape (know it where
it happens; remember across restarts, because the restart *is* the tick), not a watcher's.

Given that, fourteen copies of `promptsyncnotify.go` would be fourteen chances to get the
suppression wrong — and the suppression is the part that decides whether the alarm survives being
real. So: one transition store, one mailer seam, one event contract, one set of tests, and three
lines at each decision point.

## 8. What is left

1. **The out-of-process positive watchdog for the scheduler's whole failure class** (§4). Named,
   argued, and deliberately not built here. Wants its own ticket.
2. **A6 and A13 have no live control** — the forcing is not reachable from outside the process, not
   the delivery. Both go through the identical `Raise` path the nine controlled rows exercise.
3. **A15's two sites still `log.Printf` on mail failure** as well as emitting. The event is the
   addition; the log line was left because it is the only human-readable trace at the moment of
   failure. mg-c3f0's meta-finding lists 10 further ad-hoc sites with the same shape
   (`main.go` ×7, `sentineldrift.go`, `workspace.go`, `scheduler.go`) — bringing those up to
   `mail_error`-on-an-event is a clean follow-on and is not in this change.
4. **A2's second site, and A5's non-coordinator arm**, wired but not live-controlled.

## 9. Verdict

The enumeration is no longer prose. Twelve of the fourteen remaining rows reach a mailbox measured
at 0 unread, nine of them proved by forcing the condition against a live daemon and reading the mail
back out of a maildir, with a negative control proving the annunciator is silent on a healthy boot
and a suppression control proving it stays silent on the second one. Two rows are declined with
reasons that are checkable and checked. One residue — the positive out-of-process instrument for a
scheduler that stops firing without erroring — is named rather than absorbed.

**A1 took seven days and an accident to find.** A2 through A15 now arrive as mail, on transition,
addressed to somebody who can act.
