# Pogo Operations

Operational runbooks for managing a running pogo deployment. This document is the canonical reference for "I need to do something to pogod" — start here before reaching for `kill`.

## Recovering a single wedged agent

An agent whose process has died — a crew loop that exited cleanly without re-arming, a crash whose respawn failed — leaves a stale entry in pogod's in-memory registry (`pogo agent list` shows `status=exited` with the old pid). Recovering it does **not** require a daemon restart; both recovery verbs handle a dead-process entry directly (gh #19):

- `pogo agent stop <name>` — when the registered process is already dead, stop clears the stale entry and returns success (idempotent). Note that stop does **not** keep a `restart_on_crash = true` agent down: pogod respawns it within seconds, so against an always-on agent stop is a *restart*. To keep one down, [park it](#parking-a-crew-agent-supported-dormancy).
- `pogo agent start <name>` — overwrites a dead-process registration rather than refusing with `already running`, so start "just works" against a stale entry and brings the agent back in one step.

A **live** agent is still protected: `start` refuses a duplicate of a running process, and `stop` signals it normally. So there is no need to `systemctl restart pogo.service` / bounce launchd to recover one wedged agent — that bounces the whole fleet. Reserve the daemon restart tiers below for pogod itself misbehaving.

## Recovering from a usage-limit episode

When the provider's usage limit is hit, Claude Code renders a rate-limit-options modal ("Stop and wait for limit to reset") and the agent's reasoning loop wedges — it stops producing events but stays alive. pogod's modal watcher (gh #45) detects this and surfaces it so you aren't left guessing why a fleet went quiet. **Do not restart or `kill` a rate-limited agent to "fix" the wedge** — it is not broken; it is waiting for the limit to reset, and a restart just loses in-flight context. Recovery is: wait for the reset, then nudge or restart as needed.

### How you find out

- **One coalesced mail to `human` at the start of an episode** (subject `usage limit hit — fleet episode started`). An "episode" is one fleet-wide limit event: the first affected agent arms the mail; additional agents that hit the same limit join the episode **silently** (no per-agent mail storm). No action is required at hit time. The hit mail is held for a short **hold-down** (~45s) after the episode opens and fires only if the episode is still open when it elapses: an episode that opens and clears inside the hold-down — a sub-second flap — pages nobody at all, and sends no clear mail either (mg-4904). A genuine episode outlasts the hold-down and pages exactly once, as before.
- **`pogo status`** marks affected agents with `⚠ rate-limited` in the agent rows; `pogo agent list` appends `rate-limited`.
- **`pogo agent diagnose <name>`** reports `Health: rate_limited` — a distinct condition that outranks `stalled`/`idle`, so a limit wait is never mistaken for a genuine wedge. `--json` carries `rate_limited` and `rate_limited_since`.
- The `usage_limit_hit` / `usage_limit_cleared` events land in `~/.pogo/events.log` (see [event-log.md](event-log.md)) with `{agent, work_item_id, timestamp}` — filter with `jq 'select(.event_type=="usage_limit_hit")'`.

### When the limit resets

The condition **clears automatically** when each agent resumes producing events (its event log advances past the wedge point): the `rate_limited` flag drops, a `usage_limit_cleared` event is emitted, and — once the **last** limited agent clears — pogod sends **one coalesced clear mail to `human`** (subject `usage limit cleared — N agent(s) recovered`) carrying a per-agent **resume checklist**:

```
- cat-mg-7ffa (work item mg-7ffa)
    verify: pogo agent diagnose mg-7ffa
    if idle: pogo nudge mg-7ffa "usage limit reset — resume your task"
    if exited: pogo agent start mg-7ffa
```

Work through the checklist per agent: `pogo agent diagnose` to see whether it self-resumed, `pogo nudge` an idle-but-alive agent to prod it back into its loop, or `pogo agent start` one that exited during the wait. Agents that were mid-task and resumed on their own need nothing.

> **Note on marker drift (pre-existing risk):** detection keys off the modal's marker text (`"Stop and wait for limit to reset"`). If a future Claude Code version changes that string, detection silently stops until the marker constant is updated — the same drift risk the modal-dismissal watcher already carries. This is diagnostic-only: a missed detection means you fall back to the pre-#45 behavior (agents read as `stalled`), it does not break anything.

## Agents that fail every turn

This is the failure that took the fleet down for 23h30m on 2026-07-22, and the reason every health check read green while it happened ([mg-18d0](investigations/fleet-auth-expiry-2026-07-22.md), detector: mg-8cdb).

**What it is.** When the harness cannot reach the model — an expired credential, a rate limit, an exhausted weekly allowance, a spend cap — it does not block and it does not crash. It answers the turn **locally**, in about 10ms, with a zero-token error, and moves on. The agent is alive, responsive, and consuming every nudge at its due second. It simply accomplishes nothing with any of them.

**Why nothing else catches it.** Every counter pogo has measures *delivery*, not *completion*. During the outage `scheduler_fire_delivered` logged 647 successful deliveries and `nudge_sent` 771 — all true, all useless. Six agents each consumed 143 nudges and failed 143 of them. From the outside, a 100%-dead fleet and a 100%-healthy one produce the same events log.

**The discriminator.** A genuinely wedged agent writes **nothing** to its session transcript. An agent in this class writes a **new turn on every nudge**. The two modes are opposites at the file level, so one reader tells them apart — and pogo's response to them is opposite too.

### How you find out

- **One coalesced page to `human`** (subject `AGENTS FAILING TURNS — <agent> (<reason>): <N> errors in <window>, <first>–<last>`). This class is characteristically fleet-wide: additional agents join the episode silently, and one clear mail names them all when it ends. **Why it is fleet-wide depends on the reason, and the two are not interchangeable** — `auth_failed` / `spend_limit` because one credential and one account are shared, `server_error` because a network or provider fault reaches everything at once. Only the first is evidence about a credential; reading the second as though it were is what mg-c058 is about.
- **The page is immediate; the all-clear is not.** The episode stays open until **60 minutes pass with nothing failing** ([`synthwatch.DefaultClearHold`](../internal/synthwatch/synthwatch.go)), so a fault that recurs inside that hold produces no second page and no premature all-clear. See [Why the all-clear waits an hour](#why-the-all-clear-waits-an-hour).
- **`pogo agent diagnose <name>`** reports `Health: failing_turns (<N> errors in <window>, <first>–<last>, last <age> ago)`, which outranks `stalled`, `rate_limited` and `idle`. `--json` carries the same reading in `health_detail`, plus `restart_suppressed` and a `transcript_check` object with the reason, the count, the window (`window_seconds`), the scan time (`scanned_at`), and the span.
- The `synthetic_failure_detected` / `synthetic_failure_cleared` / `synthetic_failure_restart_suppressed` / `synthetic_failure_episode_held` / `synthetic_failure_page_suppressed` events land in `~/.pogo/events.log`. The per-agent `_detected` / `_cleared` pair is **not** the episode boundary — `incident_episode_cleared{kind:auth}` is.

### What to do

**Do not restart. Do not nudge.** A restart cannot fix any member of this class — the replacement session inherits the same dead credential or exhausted limit — while it *does* discard the live session's accumulated context (pm-pogo held 2339 messages when it failed) and overwrite the transcript the diagnosis depends on. A nudge is just one more turn to fail. pogod suppresses respawn for affected agents automatically, and the mayor's stall-watch has a mandatory pre-restart check; neither is something to work around.

Each reason needs a human or the passage of time:

| reason | what clears it |
|---|---|
| `auth_failed` | a human runs `/login` in a live session |
| `rate_limit` | time |
| `weekly_limit` | the stated weekly reset |
| `spend_limit` | a human raises the cap |
| `server_error` | time |
| `invalid_request` | a human — the request itself is being rejected |

When it clears you get one mail with a per-agent checklist. Work through it: the window's nudges were **consumed and destroyed, not queued**, so the scheduled work of that window is gone rather than late — re-run anything that mattered.

### `failing_turns` is a count over a window, not a state

`failing_turns` fires at **two** failing turns inside a **30-minute trailing window** (`synthfail.DefaultMinTurns` / `DefaultWindow`). Everything about the reading follows from that, and it is the opposite of how the token reads:

- **It is not a rate.** Two failures in thirty minutes sets it, alongside any number of turns that succeeded. An agent flagged `failing_turns` **can be working normally right now** — on 2026-08-14 seven of nine agents carried it, all seven were completing turns, and one of them was the mayor that ran the query (mg-c058).
- **The window is not the size of the fault.** It is narrower at both ends by construction: 30 minutes of history, and no reach past the last turn written. That night the counted window was 02:24:50Z–02:33:27Z; the actual github.com reachability fault ran intermittently from at least 01:18Z to 03:16Z across both HTTPS/DNS and SSH/22, and two separate readers concluded a nine-minute blip from the counter's window.
- **The reasons split by who has to act, and the split is invisible in the token.** `auth_failed`, `spend_limit` and `invalid_request` need a human. `rate_limit`, `weekly_limit` and `server_error` clear with time and need nobody. `server_error` is a provider/network fault — it looks fleet-wide because networks are, not because a credential is shared. Reporting a `server_error` episode as a credential problem parked a ticket on `human` for nine days (mg-fb29).
- **A clean probe is not recovery.** Every connectivity probe run during that outage came back healthy, because probes land in good minutes. Establishing that an intermittent fault has ended takes a **period with no instrument failures** — refinery fetch retries, `gh-intake-watch`, `gh-teardown-watch` — not one successful check. The episode-close mail states what it measured (no failing turns in the window) rather than claiming the fault is over, for the same reason.

So read `health_detail`, or `transcript_check.{count,window_seconds,first,last,scanned_at}`, before drawing a conclusion. `pogo agent diagnose` for a *failing* agent answers out of the watcher's cache, so the scan behind the reading can be up to `synthwatch.DefaultInterval` (5 minutes) old; `scanned_at` says which moment it describes, and the CLI appends `scan <age> old` once that gap exceeds 30s.

One instrument-reading trap in the same family, met while investigating this: `~/.pogo/reminders/deadman.log` timestamps when the delivering daemon **noticed** a mail, not when it was sent. The 2026-08-14 page logged at 02:44:38Z was sent at 02:28:12Z — the maildir filename's leading nanosecond stamp is the send time. A 16-minute notice lag read as a send time will misdate any paging analysis built on that log.

### Why the all-clear waits an hour

This alarm used to **flap**. As of 2026-08-14T08:16Z `~/.pogo/reminders/deadman.log` holds **49 open pages and 44 clear notices** from it (45/40 a few hours earlier — it added four of each overnight). On 2026-08-10 it opened and cleared roughly every half hour between 07:26Z and 12:22Z. On 2026-08-14 it ran **five** clear→re-alarm cycles between 02:28Z and 06:58Z — gaps of 2m50s, 2m29s, 10m09s, 3m31s and 31m32s — for **one** intermittent github.com reachability fault (mg-70f3).

The mechanism was that the episode closed on the first quiet reading, and **quiet is not absence**: it is also what an idle agent writes, and what an intermittent fault looks like between recurrences. So a fault of one shape — intermittent, hours long — was reported as a series of short faults, each with its own page and its own all-clear. The clear was the more misleading half, because it actively asserted a recovery the next page contradicted minutes later.

> **Read every gap above off the SEND stamp, never off the log line.** `deadman.log` records when the delivering daemon *noticed* a mail, and that lag was 16m26s on the anchor page — so a gap computed from two log lines is wrong by the difference of two lags. The five gaps above are send-stamp gaps. The set circulated earlier for the same night — 2m29s, 2m07s, 14m04s, 6m37s — is the notice-time reading of four of them and is superseded; it is also missing the 31m32s cycle entirely.

Since mg-70f3:

- **An episode does not close until 60 minutes pass with nothing failing** (`synthwatch.DefaultClearHold`). A recurrence inside the hold resumes the same episode: no clear mail, no new page, one `synthetic_failure_episode_held` event. 60m is where the measured data breaks — all 43 clear→re-open gaps in the log were extracted, and they cluster below 60.5m and then jump to 106.5m and beyond (out to three days). 60m absorbs 34 of 43; 30m absorbs 28 and would have missed the 31m32s cycle by 92 seconds.
- **The clear mail states what the hold absorbed.** Its subject reads `turn failures cleared — 9 agent(s), quiet 60m after 5 recurrence(s)`. Read that as **one** intermittent fault spanning the whole episode, not as five short ones. Damping the mail must not become under-reporting the fault, so the recurrence count travels in the subject, which is the part that gets skimmed.
- **A paging floor** (`DefaultMinPageInterval`, 30m) backstops the whole thing: an open page for the *same reason* within the floor is withheld and recorded as `synthetic_failure_page_suppressed`. Because the floor is below the hold, it should never fire under the shipped configuration; it is left at 30m rather than tracking the hold so that shortening the hold does not silently take the backstop with it.

**What was deliberately NOT done: "wait and see whether it clears before paging".** It was proposed and ruled out on evidence. The 2026-08-14 fault was not transient — github.com intermittently unreachable from at least 01:18Z to 03:16Z, over SSH/22 as well as HTTPS/DNS, on four independent instruments — and on 2026-07-22 a genuinely dead fleet went 23h30m unnoticed. Delaying the *first* page would have delayed a real multi-hour outage, which is the case this channel exists for. **Nothing damps the opening page**: the hold applies to the close, and the floor has an unconditional escape when the reason changes.

Two consequences for anyone reading the logs:

- `synthetic_failure_cleared` is a **per-agent** transition and fires on the first quiet reading, exactly as before. Restart suppression is lifted for that agent right then. It is not the episode boundary — `incident_episode_cleared{kind:auth}` is, and it is at least an hour later.
- The hold lives in pogod's memory. A pogod restart mid-episode drops it, and no all-clear is sent for that episode — the same as before mg-70f3, when a restart dropped the open episode too.

### When the check is unavailable

The detector reads harness session transcripts, which are **harness internals pogo does not own** — the path and schema can change without notice, and other harnesses have no such file. Where it cannot read one, `diagnose` says so explicitly (`transcript_check.state: "unavailable"`) and every behaviour falls back to what it was before the detector existed.

**That is not a clean bill of health.** It means the check is off for that agent. Reading an absent transcript as "no failures here" would be the same absence-as-evidence mistake the original incident was made of.

## The fleet auth expiry warning (`pogo credential expiry`)

The fleet's harness credential holds an OAuth **refresh grant with a hard 30-day
life that use does not extend**. When it lapses the harness can no longer mint
access tokens; the fleet coasts on its final **8-hour** access token and then
stops entirely. This has happened twice — 2026-06-20 and 2026-07-21 — and both
times it went unnoticed until the fleet had already been dead for about a day
(498 and then 914 failed agent turns). See
[the mechanism investigation](investigations/credential-expiry-mechanism-2026-07-23.md)
(mg-ed45).

Unlike the rate-limit, weekly-limit and spend-limit faults, which are chronic and
can only be detected after the fact, **auth expiry is periodic and can be
predicted**: the expiry is a plain integer on local disk. pogod reads it and
warns ahead of time (mg-7024).

### How you find out

Mail to `human` from `cred-expiry` at **T−7 days, T−72h, T−24h and T−2h**, and
once more if the grant actually lapses. Each mail names the date, the deadline by
which the fleet stops, and the fix. Subjects look like:

```
fleet auth expires in 1d 0h (2026-08-21T21:31:50Z) — run /login
FLEET AUTH GRANT HAS LAPSED (2026-08-21T21:31:50Z) — run /login now
```

### The fix

**Run `/login` in any Claude Code session.** It takes seconds, and only a human
can do it — pogod has no way to re-mint a credential and does not try.

**Already-running sessions do not recover instantly.** The lag was measured at
roughly an hour, bounded by the harness's refresh cadence. That is expected. Do
not conclude the login failed and repeat it — instead confirm the new expiry
date directly:

```bash
pogo credential expiry          # human-readable
pogo credential expiry --json   # machine-readable
```

Exit status is `0` healthy (or nothing to inspect), `1` expiring within 7 days or
already lapsed, `2` a credential exists but its expiry could **not** be read.

### What happens if the check itself fails

This matters more than it looks, because silence is the dangerous default: a
credential that cannot be read must never be reported as healthy. The three
outcomes are deliberately distinct.

| Outcome | Meaning | Behaviour |
|---|---|---|
| **present** | expiry read successfully | warns on the tier schedule; silent when healthy |
| **absent** | no keychain item, not macOS, or no `security` binary | **disarms.** No mail — a sandbox or Linux box must stay quiet — but one `pogod:` log line and a `cred_expiry_disarmed` event, so the silence is *declared* rather than assumed |
| **unreadable** | the item exists but decoding failed, timed out, or the schema moved | **mails `human`**, throttled to once a day, saying the warning is blind |

The *unreadable* case is the one a naive implementation would report as fine.
If you get that mail, the advance warning is not working and the next outage will
arrive with no notice. The likely cause is that the harness moved its credential
storage or JSON schema — both are harness-internal and pogo is not owed
stability in them. Check by hand with `pogo credential expiry`.

### What this does not cover

It predicts the **scheduled** lapse only. A credential **revoked early** produces
no warning here and is caught reactively instead, by the failing-turns detector
in [Agents that fail every turn](#agents-that-fail-every-turn) (mg-8cdb) — which
reports `auth_failed` after the fact. The chronic rate/weekly/spend limits are
likewise detection-only; see that section and
[Recovering from a usage-limit episode](#recovering-from-a-usage-limit-episode).
The two are complements: this warns before a periodic lapse, the detector
catches everything prediction cannot.

### Configuration

Off switch and cadence live under `[cred_expiry]` in `config.toml`:

```toml
[cred_expiry]
enabled = true          # default
interval = "15m"        # how often pogod samples
blind_renotify = "24h"  # throttle on the "cannot read the credential" mail
```

Only two integers and a few non-secret descriptors are ever read from the
credential. No token value is read, logged, mailed or stored.

## Token spend accounting (`mg spend`)

pogo has **no `spend` command of its own** — token-usage accounting lives in **macguffin** (the work-item store), because that's where each transcript message is joined to the work item that was claimed when it was written. To see how many tokens the fleet has consumed — in total or broken down per agent, item, tag, or repo — reach for `mg spend`:

- **`mg spend`** — per-item totals, ending in a grand-`TOTAL` row that column-sums the view, so a bare `mg spend` already answers "how many tokens in total?" without a flag.
- **`mg spend --by agent`** — per-agent breakdown (who is spending the most).
- **`mg spend --total`** — a today / this-week / all-time headline in one shot.
- **`mg spend --window today|week`** — bound the tally to a calendar day (since local midnight) or week (since Monday). This is distinct from `--since D`, a *rolling* duration ending now (`--since 24h` = the last 24 hours); the two are mutually exclusive.
- **`mg spend --json`** — machine-readable output for dashboards.

**This is a consumption tally, not the usage-limit meter.** `mg spend` measures token *consumption recorded in transcripts* (input, cache-read — which usually dominates — cache-create, output). It is **not** a read of Anthropic's usage-limit meter, and the two can diverge. For the limit side — when a fleet wedges because the provider limit was hit — see [Recovering from a usage-limit episode](#recovering-from-a-usage-limit-episode) above and the `usage_limit_hit` / `usage_limit_cleared` events + `rate_limited` diagnose condition it describes ([pogo #45](https://github.com/drellem2/pogo/issues/45)). Spend answers "where did our tokens go"; the #45 signals answer "are we currently throttled" — complementary readings, not the same number. In particular, `--window week`'s Monday anchor is a fixed calendar convention that only *approximates* a weekly view; it does **not** track Anthropic's account-specific weekly reset.

**Historical-spend semantics (harvested-only, single-machine).** Spend is tracked only once *harvested*, and harvesting runs automatically at the start of every `mg spend` invocation — so running the command is what advances the record, and any window is only as complete as the last time the command ran (schedule `mg spend` for continuous capture). Once harvested, a record survives Claude Code restarts, `mg` upgrades, and transcript rotation; the one thing that loses data is deleting a transcript *before* it has been harvested. The store is a single-host tally under `~/.macguffin/` — there is no cross-machine aggregation. The full survives/lost matrix and the graceful-attribution rules live in the macguffin README's [Token spend accounting](https://github.com/drellem2/macguffin#token-spend-accounting) section — consult it there rather than relying on this summary.

## Parking a crew agent (supported dormancy)

`restart_on_crash = true` is an always-on contract: pogod respawns the agent on **any** exit — including an explicit `pogo agent stop` — within seconds. To take such an agent out of rotation (e.g. a PM whose workstream is gated with zero in-flight items), **park** it instead of stopping it:

- `pogo agent park <name>` — one command that (1) persists a park flag at `~/.pogo/agents/<name>/.parked`, (2) removes the agent's pogod schedules, recording them in the park file, and (3) stops the process. The flag is written before the stop, so the respawn can't win the race; it also survives pogod restarts — boot-time auto-start skips parked agents regardless of `auto_start`.
- `pogo agent wake <name>` — reverses it: starts the agent, restores the recorded schedules (the agent's own startup re-registration doesn't stack duplicates — schedule adds are keyed on agent + id), and clears the flag.
- `pogo agent list` shows parked agents with `status=parked`, so the coordinator's stall-watch can skip them mechanically. (The coordinator defaults to `mayor`; configurable via `[agents] coordinator`.)

Parking an agent that isn't currently running is valid (the flag still gates auto-start); `pogo agent start` refuses a parked agent and points at `wake`. Parking is for crew agents — polecats are ephemeral and are simply stopped.

**Park→wake is also the context-cycle lever.** To give an always-on agent a fresh context on a schedule (e.g. a nightly cycle per agent), park it and then wake it: wake starts a new process, so the agent comes back fresh, with its recorded schedules restored. Do **not** script `stop` → `start` for this. That races pogod's respawn: when the respawn wins, `start` fails with `already running` — and treating that error text as a success signal ("it's up, just not by my hand") is a brittle dependence on both the ~2s respawn timing and an error string. Park writes its flag before the stop, so it has no race to lose (drellem2/pogo#89).

### Confirming an agent is actually down

`pogo agent list` is a registry view, not a liveness probe — **absence from it is not evidence of exit**, and a listed pid can be stale. To confirm a teardown, use the probe:

```bash
pogo agent diagnose <name> --json | jq .process_alive   # false ⇒ that process is gone
```

`process_alive` is a real `kill(pid, 0)` check against the agent's pid, and `health` reports `exited`/`dead` alongside it. Remember that for a `restart_on_crash` agent a false `process_alive` means *that* process is gone, not that the agent will stay down — only a park keeps it down.

## Pogod restart policy

`pogod` runs under launchd with `KeepAlive=true` (see `scripts/launchd/com.pogo.daemon.plist`). That means **any uncoordinated kill is a loop**: launchd relaunches the daemon within seconds, and if the caller then re-evaluates "pogod looks broken — kill it again," the system gets stuck in a kill→relaunch→kill cycle. The decision recorded in mg-f5fc is that callers — polecats (disposable worker agents), crew agents, humans at a terminal — follow a three-tier escalation. Try tier 1 first; only escalate when the situation matches the criteria below.

> **Critical invariant: never `kill -9 pogod`.**
> launchd's `KeepAlive=true` will relaunch it immediately, and a SIGKILL skips pogod's own shutdown logic (mail flush, lockfile release, child-process cleanup). Use tier 2 or tier 3.

### Tier 1 — Don't restart pogod (default)

Most "pogod is misbehaving" situations are better solved by **filing an mg (a work item in macguffin, the task-store CLI) or restarting a specific subcomponent**. A pogod restart is a heavy hammer: it interrupts every running polecat, and re-arms every cron and watcher from cold. A *graceful* stop also **waits for every in-flight merge to finish** — a merge is never abandoned halfway, so a restart during a long quality gate blocks for as long as that gate runs. A merge interrupted by a hard stop is resolved on the next start by an ancestor probe (merged if it landed, re-queued at head if it did not), not blindly re-run. Reach for it only when the lighter alternatives below don't apply.

**Symptoms that do NOT warrant a restart** (file an mg or fix in place):

- A single handler returns wrong results or panics. → File an mg with the panic trace; the bug fix lands without restarting pogod.
- A plugin behaves stalely after you edited its source. → Most plugins reload on file change; if not, fix the plugin's reload path rather than bouncing the daemon.
- A polecat hangs or misbehaves. → Stop that polecat (`pogo agent stop <name>`); pogod itself is fine.
- Refinery is slow or backed up. → Inspect with `pogo refinery list`; queue throughput is not a daemon-restart problem.
- **Merge requests are queued and nothing seems to be moving for MY repo.** →
  Merges run in **per-repo lanes**, so a queued request is only ever waiting on
  merges for its own repo. `pogo refinery status` prints one `Active:` line per
  lane with its repo, and `pogo refinery show <mr-id>` states position *within
  that repo* — including the case where merges are running but none of them is
  yours, which is a wait for a free lane and not a blockage. Deep queue spanning
  several repos is normal and self-clearing; the cap is
  `[refinery] max_concurrent_merges` (default 2).
- **Merges are reported but `main` has not moved / the refinery looks stalled.**
  → Check **which repo's** main. One refinery queue serves several repositories,
  so `pogo refinery queue` and `pogo refinery history` both name the repo on
  every row, and `--repo=<name|path>` narrows either to one:

  ```bash
  pogo refinery history --repo=pogo      # only this repo's merges
  pogo refinery queue --repo=pogo        # only this repo's pipeline
  ```

  Read the repo column *before* concluding anything about a queue you are
  watching. On 2026-08-07 three different agents escalated from a view that did
  not have it — four merge requests cancelled on the belief that all were
  `pogo` branches (two were not, and would have passed); "6 MRs report merged
  but main has not moved" raised as possible lost merges, when every one had
  landed in `onethird_program`'s main; and "refinery STALLED, nothing merged"
  raised five seconds after a merge, in a repo the reader was not watching
  (mg-ff3a). Two of the three escalated as URGENT.

  Two things `--repo` deliberately does **not** narrow, so the filter cannot
  manufacture the alarm it was added to prevent: the queue's in-flight/pending
  counts and its `NOTHING IN FLIGHT` line are always computed over the whole
  pipeline, and history's retention cap is shared across repos — a filtered
  window reaches back **less** far than the cap suggests, which the command
  says outright when the cap has bitten.

  Infer the repo from the **merge request**, never from the work item: one work
  item can legitimately produce branches in three repos.
- **A merge request has sat in `processing` for a long time.** → Ask the merge
  request, not the process table: `pogo refinery show <mr-id>` prints a
  `Verdict:` line reading the running gate's heartbeat. `ALIVE and working`
  means the gate is slow and **waiting is correct** — do not re-submit the
  branch. `DEAD` means the runner is gone and waiting will not help.
  `ALIVE, gate silent` means it cannot be told from outside, and names the
  timeout that bounds the wait. Heartbeats land every 30 seconds, so a record
  older than ~90 seconds is stale rather than merely quiet.

  **Do not re-submit a branch on a guess.** On 2026-07-29 a slow gate was read
  as a hung one from log silence alone; the branch had in fact merged, the
  redundant re-submit failed against a deleted remote branch, and the failure
  **reopened a work item whose work had landed** (mg-8595). If a gate really is
  stuck, `pogo refinery cancel <mr-id>` now reaches a processing MR — that is
  the recovery path, not a pogod restart.
- **A batch of merge requests all show `failed`.** → Read the STATUS, which now
  carries the class: `failed(infrastructure)` establishes nothing about the
  branch and wants a resubmit, while a plain `failed` is a verdict on the code
  and wants a fix (mg-e5c2). Do not dispatch fixes for a column of
  `failed(infrastructure)`: on 2026-08-05, thirty-one merge requests failed
  across three DNS outages and every one of them read as a bare `failed`, which
  invited thirty-one fixes for defects that did not exist. The confusion runs
  both ways — a real rebase conflict in the same evening was written off as
  another network casualty.

  **A `failed` on a quality gate is not automatically a verdict on the branch.**
  `defect` commits to "re-running establishes the SAME fact", and that commitment
  is what suppresses the retry — so when you can see a reason a re-run would
  differ, the classification is wrong and the right move is to say so rather than
  work around it. Three carve-outs exist because the commitment was measured
  false: `host` (the box ran out of a resource — free it, then resubmit
  unchanged, mg-b41f), `indeterminate` (the gate was killed before it returned a
  verdict, mg-e565/mg-0502), and, since mg-67c9, a gate whose OWN network I/O
  failed — a Go module fetch that could not resolve its proxy is reported
  `infrastructure` and retried on a small budget, because on 2026-08-14 that
  exact fault at the FETCH stage was retried and merged on attempt 11 while the
  same fault inside the gate was called `defect` and stopped a merge dead. The
  budget is 4 attempts, not the fetch stage's 28, because each retry re-runs the
  whole gate on the single serial slot every queued merge waits behind — so a
  long outage can still exhaust it, and the report still says `infrastructure`
  when it does.

  `pogo refinery show <mr-id>` prints one block per failing attempt with its
  transport, the git command as invoked, and the far end's exact words; a
  terminal failure always states why no further retry was made. For the shape of
  a whole incident across merge requests, go to the durable log rather than any
  one MR — and read **every** transport, because ssh and HTTPS report the same
  DNS failure in completely different words and the ssh wording (`Undefined
  error: 0`) names no cause at all:

  ```bash
  pogo refinery history --since=6h --json |
    jq -r '.[].attempts[] | "\(.transport)\t\(.raw_error)"' | sort -u
  ```
- Logs look noisy. → Filter `~/Library/Logs/pogo/pogod.log`. pogod appends across restarts (crash evidence survives) and rotates the file itself at startup once it exceeds 10 MiB — the prior chunk is `pogod.log.1` (up to `.3`). No manual rotation needed; never truncate the live file mid-run. The indexer's per-tick narration is at debug and off by default (gh #111); set `POGO_LOG_LEVEL=debug` when you need it, or `warn` to quiet the daemon further. **On the launchd deployment that variable must go in `~/Library/LaunchAgents/com.pogo.daemon.plist`** — `POGO_LOG_LEVEL=debug pogo server start` cannot reach a launchd-managed pogod, since launchd does not pass the invoking shell's environment to a job — and the job must be unloaded and loaded again. Note that `pogo service install` regenerates that plist from its own template and drops the key, so re-add it after any re-install. See [customizing.md](customizing.md#turning-the-log-volume-up-or-down).
- An mg you expected to appear didn't. → It's almost certainly an mg routing/visibility issue, not a pogod liveness issue.

**Symptoms that DO warrant escalation** (continue to tier 2):

- You just installed a new `pogod` binary and want it picked up.
- You changed a config value that pogod only reads at startup (env var in the plist, top-level config file).
- pogod's HTTP endpoint stops responding entirely (`curl http://127.0.0.1:10000/health` hangs or refuses) — but only after you've confirmed launchd hasn't already relaunched it on its own.
- pogod's process is alive but stuck (no log progress for many minutes, all handlers timing out) — escalate to tier 3 if tier 2 doesn't recover.

When in doubt, file an mg describing the symptom rather than restarting. The cost of a wrong restart is high; the cost of an extra mg is near zero.

### Tier 2 — Controlled restart via launchctl

For the cases tier 1 listed (binary upgrade, startup-only config change), bounce pogod through launchd:

```bash
launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
```

This is the **only** sanctioned way to restart pogod from a shell. Why this and not `kill -TERM` / `kill -9`:

- launchd is already the source of truth for pogod's lifecycle. `kickstart -k` tells it to stop and restart the service, so the relaunch happens through the same code path as a fresh login. `kill -TERM` followed by a `KeepAlive` relaunch races with anyone else watching the PID and gives no guarantee about ordering.
- No `KeepAlive` loop. With `kickstart -k`, launchd performs exactly one stop+start. With `kill -9`, launchd will relaunch — and if the caller's logic re-fires, you're in the loop described above.
- pogod gets a clean shutdown. `kickstart -k` sends SIGTERM first, giving pogod a chance to flush mail, release the lockfile, and reap children before launchd issues SIGKILL on the grace timeout.
- It's idempotent and safe to script. Polecats and humans run the same command; there's no "are we already running under launchd?" branch to get wrong.

If `kickstart -k` itself returns an error (launchd not finding the label, the service not loaded), fix the install with `pogo service install` before escalating to tier 3 — tier 3 calls the same `launchctl kickstart` under the hood, so a broken plist will not heal itself there either.

**A zero from `kickstart` is not a verified restart.** It says launchd accepted the request — not that a daemon exists afterwards, and not that it is running the binary you meant. `kickstart -k` re-execs whatever is on disk, so a stale binary comes straight back, healthy. Follow a bare shell kickstart with the check (mg-ed4a):

```bash
launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
pogo service verify-revision      # 0 AGREES / 1 DIFFERS / 3 UNKNOWN
```

See [Did the restart put the right code back?](#did-the-restart-put-the-right-code-back-pogo-service-verify-revision).

### Tier 3 — External recovery agent

For the case where tier 2 isn't reachable: pogod is wedged so badly the calling shell can't get a response, **or** the caller is itself a child of pogod (a polecat, a crew agent, a refinery worker) and cannot safely SIGTERM its own parent.

Signal a restart by enqueuing a request:

```bash
pogo recovery request --reason="<short explanation>"
```

This drops a `.req` file into `~/.pogo/recovery/queue/` and exits 0 immediately — it does **not** block on the restart. Within ~2s, the `com.pogo.recovery` LaunchAgent picks it up via `WatchPaths`, calls `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon`, and archives the request. See mg-6749 for the design and `scripts/launchd/README.md` for the full plist contract and operational commands.

**Why this is a separate launchd job, not a pogod feature:** the whole point of tier 3 is to recover when pogod is wedged. Any signal channel that depends on pogod (an HTTP endpoint, an mg tag, a daemon-served socket) defeats the purpose. The recovery agent uses only the kernel — filesystem writes, `launchctl`, `flock`-equivalent atomic mkdir — so a fully-wedged `pogod` cannot block its own recovery. A polecat dying alongside its parent can still `mv` a file before exiting.

**Tier 3 restarts; it does not redeploy.** The recovery agent runs `launchctl kickstart -k` and nothing else — no `go install`, no build, no `git pull`. It relaunches the binary that is *already installed*, so it activates **zero merged commits**. If you have merged a pogod change and want it live, tier 3 is not the mechanism and no number of recovery requests will make it one. To see what pogod is owed:

```bash
scripts/pogo-self-deploy check    # three-way drift report; safe from anywhere, never acts
```

That reports the running / installed / `main` revisions separately, because a restart bounces the whole fleet and a rebuild does not — you should not pay the fleet-bounce cost to discover you only needed one of them. The redeploy itself is guarded and must run out of band; `scripts/pogo-self-deploy redeploy` refuses callers inside pogod's process tree and its refusal explains the handoff. See `docs/investigations/recovery-trigger-restart-not-redeploy-2026-07-23.md` for why the two triggers are deliberately kept separate (mg-cf48).

There is a third subcommand, `scripts/pogo-self-deploy bounce`: `redeploy` with everything that needs the network removed — no fetch, no build, no `do_prove` — keeping the drain gate, the out-of-band guard and both post-restart verifies, and refusing `--force`. It delivers **no code**. It exists because the nightly deploy is this box's only automatic recovery path and it needs the same network a network fault takes away; five consecutive nights of that cost a 118-hour blackout in August 2026 (mg-9fc9). The nightly runner calls it by itself after `POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER` (2) consecutive nights lost at the transport step — see `scripts/launchd/README.md` step 7b. By hand it is the right tool when the fleet is wedged and the remote is unreachable, and the wrong one whenever a deploy would work.

**Critical invariants (do not break):**

- **Recovery is an independent install.** `pogo service install` does NOT install the recovery agent. Run `pogo service install-recovery` separately. Bundling them would mean a wedged daemon install blocks its own recovery — the very situation tier 3 exists to handle. The post-install hint from `pogo service install` reminds you of this; don't ignore it.
- **The recovery agent rate-limits to 60s.** Two `pogo recovery request` calls inside a 60-second window result in exactly one restart. The second request is not lost — it sits in the queue and a deferred tickle drains it after the floor elapses — but you cannot bounce pogod faster than once per minute. This is intentional: it bounds the worst-case impact of a polecat that mistakenly decides "restart pogod" is the right answer to every error.
- **The recovery script never `kill -9`s pogod.** It only ever calls `launchctl kickstart -k`. Tier 3 is for *controlled* restarts; the SIGKILL prohibition still applies.
- **One `pogod` ⇒ one restart per drained batch.** All `.req` files in the queue at trigger time are coalesced into a single kickstart call, then archived together. Don't expect "10 requests = 10 restarts."
- **The plist bakes absolute paths at install time.** `WatchPaths` and `POGO_RECOVERY_DIR` are rendered from `POGO_HOME` when you run `install-recovery`. Move `POGO_HOME` afterwards and the installed job keeps watching the old directory — silently, because `pogo service status` only checks that the plist *file exists*, not that its paths still resolve. **Re-run `pogo service install-recovery` after any `POGO_HOME` change**; `scripts/migrate-pogo-home.sh` does this for you.

When tier 3 itself fails — recovery agent not installed, queue dir unwritable, kickstart returning non-zero — the failed `.req` files land in `~/.pogo/recovery/failed/`. Inspect that directory and `~/Library/Logs/pogo/recovery.log` before filing the mg; the log line `kickstart failed (rc=...)` is the most actionable signal.

**Read past `kickstart succeeded` in `recovery.log`.** Since mg-ed4a the drain does not end there: it asks `pogo service verify-revision` whether the daemon that came back is the revision launchd was configured to exec, logs `revision check AGREES` / `DIFFERS` / `UNKNOWN`, and **exits with that verdict** (1 for DIFFERS, 3 for UNKNOWN). A `DIFFERS` means the restart worked and put the wrong code back — redeploy, because another recovery request will reinstate the same binary. Requests are still archived on the kickstart's result, so a `DIFFERS` drain leaves its `.req` in `processed/`, not `failed/`: the restart was performed, and re-queueing against an artifact a restart cannot fix is how a recovery loop starts.

**Verifying tier 3 is actually armed.** An installed plist is not a working one, and `pogo service status` cannot tell the difference. Check two things with `launchctl print gui/$(id -u)/com.pogo.recovery`:

1. Its `WatchPaths` entry is the queue the CLI writes to — `~/.pogo/recovery/queue` under a default `POGO_HOME`.
2. The job actually *spawns* when that directory changes. Drop a file into the queue, then confirm `runs` increments and `~/Library/Logs/pogo/recovery.log` gains a line.

A job that stays at `runs = 0` while showing `pended nondemand spawn` is **not armed**: launchd is accepting the trigger and never dispatching it. No plist edit fixes that — the job only runs via an explicit `launchctl kickstart`, which defeats the purpose of tier 3. See mg-6e82.

## Am I running what I think I am running? (`pogo service status`)

`pogod` **does not self-install.** Nothing rebuilds the binary when a change
merges, and nothing restarts the daemon when the binary is replaced. So every
pogo installation drifts from its own source silently — the merge lands, the
daemon keeps running whatever it was started with, and until mg-75ec no shipped
surface said so.

`pogo service status` now answers it, in three axes:

```
$ pogo service status
Service installed: /Users/you/Library/LaunchAgents/com.pogo.daemon.plist

revision drift (repo: /Users/you/dev/pogo, ref: main)
  running pogod   : 023fab52d19a…  (http://localhost:10000/version)
  installed pogod : 023fab52d19a…  (/Users/you/go/bin/pogod)
  installed pogo  : 023fab52d19a…  (/Users/you/go/bin/pogo)
  main HEAD       : e4a406c5a58f…
  status          : drift
  action          : BUILD + RESTART owed: running == installed, both behind main HEAD e4a406c5a58f. …
```

- **running pogod** is read from the live process (`GET /version`), never from
  the file. `go install` rewrites the on-disk binary underneath a running
  daemon, and that divergence is precisely the drift being looked for.
- **installed pogo** is not optional cargo. A `pogo` older than the `pogod` it
  talks to is a protocol mismatch waiting to happen, and a check that reads only
  the daemon reports health it has not measured — that is how the CLI once sat
  three days behind `main` while the check called the box clean (mg-ddf1).
- **main HEAD** needs a source checkout. `--repo PATH` or `$POGO_REPO`, else the
  checkout you are standing in.

**Without a checkout you still get an answer.** The `main` axis is reported
unavailable, with the reason, and the other two are still compared: a daemon
running code that `go install` has already replaced on disk is real drift, and
establishing it needs no repo, no git, and no network. That is the normal
consumer case, not a degraded one.

**A revision is evidence, not truth.** A binary with no vcs stamp, or one
stamped with a commit the checkout has never heard of, reports `status:
unknown` — not clean, and not behind. Both would be claims about ancestry the
check never measured. The unstamped case in particular is *not* given a
"rebuild" verdict: the rebuild would be unstamped too, so the drift would never
clear, and you would have a reconcile loop against an artifact that is not
broken.

**Report-only, and exit 0 either way.** The command never builds, installs,
restarts, or reconciles. It exits 0 whether or not it finds drift, so existing
callers are unaffected; gate on the `status` field of `--json` instead:

```bash
pogo --json service status | jq -r .drift.status    # clean | drift | unknown
pogo service status --no-drift                      # skip the check entirely
```

**Fleet-side, `scripts/pogo-self-deploy check` prints the same three-way** and
the two agree by construction (verified against a live drifted daemon on
2026-07-29: both reported `023fab5` running/installed against `e4a406c` on
main). The script is the redeployer's read-only half and can also *act*
(`redeploy`); this command only ever reports. If you have the repo, either is
fine. If you do not — which is every consumer — this is the one you have.

Note this is a different question from `pogo service check-drift`, which
compares `[reconcile]` **host artifacts** (plists, scripts) against their repo
sources. Same word, different axis: that one is about files pogo generates, this
one is about the binaries pogo *is*.

### Did the restart put the right code back? (`pogo service verify-revision`)

`pogo service status` is the standing question, asked when you think to ask it.
This is the same question asked **at restart time**, by the paths that restart
pogod — and until mg-ed4a only one of the four asked it at all:

| path | what it verified |
|---|---|
| `scripts/pogo-self-deploy` `verify_running()` | polls `/version` against `main` — the only real check |
| `pogo service install` (`verifyLaunchdRunning`) | `launchctl list` + `/health` — **never `/version`** |
| `pogo service restart` (`restartLaunchd`) | **nothing** |
| `scripts/launchd/pogo-recovery.sh` | the kickstart's own **exit code** |

`launchctl list` says a job is registered. `/health` says something is
listening. `launchctl kickstart` exiting 0 says launchd accepted the request.
**None of them says the right thing is listening** — and a kickstart re-execs
whatever is on disk, so silently reinstating a stale binary is what a restart
*does* when the disk is stale. On 2026-08-07 this box had been alive, healthy
and 92 commits behind for eight days, passing every one of those three checks
every time they ran.

All four paths now share one implementation (`internal/revcheck`) and one
vocabulary. Three of them **report** the verdict; this command is the one that
**gates** on it:

```bash
pogo service verify-revision                # exit 0 AGREES / 1 DIFFERS / 3 UNKNOWN
pogo service verify-revision --expect <rev> # a deploy expects main's HEAD
pogo --json service verify-revision | jq -r .verdict
```

**Three exit codes, not two.** `UNKNOWN` is its own code because "the daemon is
running the wrong thing" and "I could not tell what the daemon is running" owe
different actions, and because a check that goes green on an absent reading is
the exact defect this closes. An absent or unreadable side is **never**
rendered as agreement.

**The expectation is the plist's binary, not `$PATH`'s.** By default the check
compares the live `/version` against the vcs stamp of the pogod binary named in
`~/Library/LaunchAgents/com.pogo.daemon.plist` — because that is the one launchd
actually execs, and because that expectation needs no repo, no network and no
config. A second `pogod` earlier on `$PATH` does not change what launchd runs,
so it must not change what the check expects.

**`AGREES` does not mean *current*** — read this before quoting a green from it.
Against the default expectation `AGREES` means the **restart took**: the process
is running the binary launchd execs. If that binary is itself eight days old,
this says `AGREES` and is right to. Measured on this box on 2026-08-07:

```
$ pogo service verify-revision
revision check AGREES: running=d31297f493cd expected=d31297f493cd     # restart fine
$ pogo service verify-revision --expect "$(git rev-parse main)"
revision check DIFFERS: running=d31297f493cd expected=22e0541f7fd2    # disk stale
```

"Did the restart take?" and "is the disk current?" are two questions, and they
get two instruments on purpose — treating one as the other is the same
green-on-an-adjacent-property mistake this check was built to remove. The second
question belongs to [`pogo service status`](#am-i-running-what-i-think-i-am-running-pogo-service-status)
and to pogod's standing revision-staleness alarm (mg-5bd2). Ask *this* command
that question deliberately with `--expect`, or do not read its green as an
answer to it.

**`install` and `restart` report; they do not fail.** Deliberate, and called
out here so nobody discovers it by surprise: `pogo service install` still exits
0 against a stale daemon, and `pogo service restart` still exits 0 when the
restart re-launches the same binary. Installs currently succeed in that state
and something may depend on it, so the observation shipped first and the
decision to gate is a separate change. What has changed is that both now
**print** the verdict, so the state can no longer pass unremarked.

**Tier 3 does gate on it.** `pogo-recovery.sh` runs this after its kickstart and
its exit code carries the verdict, so `recovery.log` no longer ends at
`kickstart succeeded`. The requests are still archived on the *kickstart's*
result — a kickstart that happened has been serviced, and re-queueing on a bad
revision would loop against something a restart cannot fix. Set
`POGO_RECOVERY_VERIFY_REVISION=0` to drain without asking; the log then says
`revision check SKIPPED` rather than going quiet.

**This is defence in depth, not a fix for an observed failure.** Measured under
mg-2def: 0 of the 4 deploy failures in the 2026-08-01..08-04 window reached a
restart path at all — every one died at or before the drain. This hardens a
path none of those nights got to.

## Did the nightly redeploy actually happen? (`pogo check-staleness`)

`pogo service status` above answers *"is the running daemon behind main?"*. It
does not answer the two questions that let a six-day staleness go unnoticed:

- **Did the mechanism that should have fixed it run at all?**
- **Are the fleet's PROMPTS what the repo ships?**

Between 2026-07-31 and 2026-08-05 the nightly redeploy did not succeed once.
Four nights it never fired — the box was powered off through each 03:00 window,
and launchd replays a missed `StartCalendarInterval` on **wake** but not across
a **power cycle**, and `RunAtLoad=false` means boot does not stand in for it. The
fifth night it fired and died one second in on a transient ssh failure. **Nothing
alarmed on any of the five.** `pogod` served 52-commit-old code and every polecat
dispatched ran a superseded template; both were found by hand, one by running
`ls` on a binary and one by a dispatch eating a 409.

```
$ pogo check-staleness
redeploy — expectation: a successful deploy every night, settled by 05:00 local + 2h0m0s grace
  record:       /Users/you/.pogo/deploy-attempt.stamp
  last due:     2026-08-04
  MISSED: 4 night(s) due through 2026-08-04 produced no successful redeploy (last record: 2026-07-31, attempt 1, rc 0).
    2026-08-04  no-fire  no attempt was recorded for this night at all
    2026-08-03  no-fire  no attempt was recorded for this night at all
    …

prompts — installed corpus vs a git ref (never this binary's own embed)
  installed:    /Users/you/.pogo/agents
  reference:    /Users/you/.pogo/deploy-src @ origin/main = b3efaa2d2410 (committed 2026-07-31T01:55:51+01:00)
  STALE: 8 of 9 shipped prompt(s) differ from the reference.
    mayor.md                           differs        installed 578 lines, ref 983 — installed is behind by 405
    …
```

**A missed run cannot be detected from the deploy's output.** There is no log
line for a fire that did not occur, no non-zero exit, no mail — which is exactly
why four nights passed unnoticed under a runner that alerts loudly when it
fails. So the witness reads an **expectation** (the deploy schedule and the
clock) against `~/.pogo/deploy-attempt.stamp`, and a night after the record's
date with nothing in it *is* the signature of a fire that never happened. Two
reasons are reported and they send you to different places: `no-fire` (nothing
recorded — look at launchd and the host's uptime) and `failed` (a record with a
non-zero rc — read `pogo-deploy.log`).

**The prompt half compares against a git ref, never against this binary's
embed.** `pogo doctor --check` already compares the installed corpus to the
running binary's *embedded* copy — and it passed throughout, truthfully: a
missed redeploy stales the binary and the prompts **together**, and a comparison
between two artifacts that move as one cannot see either move. That check
answers "did the installer run since this binary was built". This one answers
"is what the fleet reads what the repo ships", and only a comparison against the
repo can.

**Neither half consults this binary's own build or embed.** That is deliberate:
a staleness alarm that a stale install disables has failed at the first failure
it exists to catch. The redeploy half reads a text file and a schedule constant;
the prompt half reads git.

**Length is never the predicate.** The decision is a hash of the file body with
the install stamp stripped, in both directions. The installed tree is *not*
simply an older `main`, and a check that only asked "is the installed file
behind?" would miss a file that is ahead. The report says so on screen, above
the counts, because the counts are the most legible thing on it: the
`polecat-build-pr.md` "231 installed vs 230 on main" anomaly recorded in mg-8bcb
turns out to be the install stamp line itself (body hashes are identical), and
that measurement was a `wc -l` pair off by one on every file — visible only on
the one file current enough for +1 to flip the sign. Note this removes a
secondary blocker from mg-8bcb and does **not** unpark it; its stated park is
the still-failing architect precondition on the daemon's revision.

**The reference is itself stale by construction, and the row says so (mg-afd0).**
`--repo` defaults to the deploy checkout at `~/.pogo/deploy-src`, which the
nightly fetches **at deploy time and never after** — so its `origin/main` is
frozen at the *deployed* revision, not the live remote head. Everything that
shipped since the last deploy is invisible to the comparison, and that window is
the only one in which the fleet is running something older than what shipped —
which is what this command's first line claims to witness. On 2026-08-13 the
reference stood 17 commits behind `origin/main`, five of them touching the
corpus, and the report read `ok: all 9 shipped prompt(s) match the reference`.
The `--help` did carry the caveat; the row did not, and a caveat that does not
travel with the output does not exist.

So every run now prints **when the reference last fetched** — from `FETCH_HEAD`'s
mtime, which dates the last *fetch*; a remote-tracking ref's own mtime dates the
last time the branch *moved*, and a mirror that fetched an hour ago and found
nothing new would date itself days old by that measure — and asks the remote,
with `git ls-remote`, whether it has moved past the reference:

```
  reference:    /Users/you/.pogo/deploy-src @ origin/main = 082ec38b0159 (committed …)
                LAST FETCHED 2026-08-13T03:00:12+01:00 (5h43m ago) — that ref is this repo's own
                copy, not the live remote; anything pushed since is invisible to the comparison below.
  BEHIND THE REMOTE: origin is at 107f6b2a4cd7, which this reference does not have.
                18 commit(s) have shipped since, 5 of them touching internal/agent/prompts:
                  d27ecc1  the DOCTOR's refinery-history advice names its window (mg-af0c)
                  …
```

When the reference already holds those objects the gap is **quantified and
named**; when it does not — the usual case for a mirror that has not fetched
since 03:00 — it is reported as *unknown*, never as zero, and unknown is a
finding. A gap whose commits touch nothing under the corpus is **not** a finding:
this witness judges the prompt corpus, and claiming more would be the same
over-reporting the change exists to remove.

**Still nothing fetches by default** — a detector that mutates the tree it is
judging has made itself a participant, and `ls-remote` is a query that writes
nothing. `--fetch` opts into refreshing the one remote-tracking ref, so the whole
comparison runs against what *shipped*; it also names where the reference stood
**before** the fetch, because that is the deployed revision and the fetch is the
only thing that erases the local record of it. The fetch passes
`--no-write-fetch-head` so it does not overwrite the very timestamp the row above
is read from — the remedy would otherwise destroy the evidence of the fault, and
on a git too old to know the flag the run says the timestamp moved.
`--skip-remote` disarms the query on an offline host; an unreachable remote is
reported loudly and is **not** counted as a finding, because a check that fails
on every laptop gets its exit status ignored.

**Constructing the positive control.** An alarm that has only ever been silent
has not been shown to work, and silence is what those five nights produced. Both
inputs are flags, so both states are constructible without waiting a night or
moving the system clock:

```bash
# RED — the real four-night gap
printf '2026-07-31 1 0\n' > /tmp/stale.stamp
pogo check-staleness --stamp /tmp/stale.stamp --now 2026-08-04T12:00:00+01:00 --skip-prompts

# QUIET — same witness, same instant, a night that deployed
printf '2026-08-04 1 0\n' > /tmp/fresh.stamp
pogo check-staleness --stamp /tmp/fresh.stamp --now 2026-08-04T12:00:00+01:00 --skip-prompts
```

Exit status is 0 when both halves are clean and 1 when anything is reported —
**including when a half could not run**. An unreadable record, an unresolvable
ref and an unreadable prompt are all findings; a check that could not run has
not found its subject healthy.

**Run it from source, not from the installed binary.** `pogo check-staleness`
detects that installed artifacts have fallen behind source — and it is a
subcommand of `pogo`, which only becomes current when the nightly redeploy runs.
That is the exact mechanism whose failure it detects, so a witness reachable only
through the installed binary cannot be switched on until the fault it detects is
already fixed. This is not hypothetical: on 2026-08-05 the installed `pogo` was
six days and 52 commits stale and answered `unknown command "check-staleness"`.

`scripts/check-staleness.sh` is the way around it. It is a tracked file, so it is
present in every checkout at the merge commit, and it compiles the code sitting
beside it — a `git pull` arms the witness, with no `go install`, no launchd and
no redeploy. It **refuses** to fall back to an installed `pogo`, loudly, even
when `go` is missing; a silent fallback would look like it worked while reporting
whatever revision the last deploy left behind. All flags pass through:

```bash
scripts/check-staleness.sh
scripts/check-staleness.sh --skip-prompts --stamp /tmp/stale.stamp --now 2026-08-04T12:00:00Z
```

**Report-only, and it is not armed by itself.** Like every `check-*` command it
never installs, rebuilds or reconciles, and nothing runs it on a schedule until
someone arms it — point the schedule at the script in a checkout, not at the
installed binary:

```bash
pogo schedule doctor --cron "8 8 * * *" --id staleness --replay once \
    --message "Run '~/dev/pogo/scripts/check-staleness.sh'. If it exits non-zero, mail human with the output."
```

The minute is `8`, not `0`, and that is not cosmetic: doctor's mail-check is
`*/10`, so a daily check at `0 8` would share a wake cycle with it every
morning, and pogod suppresses whichever fire arrives second
(`wake_silence_once`). A mail-check absorbs that — the next one is ten minutes
behind — and a once-a-day check cannot. **A schedule that must not be dropped
should not share a minute with one that can be**; keep daily schedules off
every multiple of 10 (mg-956b).

The arming gap is tracked as a class on mg-75f9 ("nothing runs check-drift;
mg-5701 shipped a detector you have to remember to ask"), alongside mg-7ff8,
mg-bd92 and mg-fc99 — four detectors that read as done at merge and were inert
on the box.

**Siblings it does not replace.** `pogo service status` covers the running
daemon's revision (the RUNNING process, not the binary on disk — a deploy can
replace the file while the old process keeps the lock and keeps serving,
mg-fa79). mg-fc99 checks the installed plist's fire hours against the schedule
the code declares, which catches an inert retry *before* a night needs it; this
command would only see the consequence the next morning. mg-0d70 is about a sync
failure's alert naming the wrong cause; this command emits no such alert.

## The external witness that the redeploy landed (`scripts/revision-probe.sh`)

**The rule this file implements:** *a detector for "X did not happen" must not be
activated by X.*

pogod carries `driftwatch`, which reports how old the code it is running is
(mg-5bd2). That is the right home for the daemon's own reporting, and it stays.
But every line of it lives inside `pogod` — and `pogod` is installed by the
redeploy. So the alarm for *"the redeploy did not work"* is armed only by a
redeploy that **worked**. On a night the deploy fails, the new alarm is dark for
exactly the reason the old exit-code proxy was dark on 2026-08-01..08-04: the
detector lived inside the thing whose absence it was supposed to report.

This is the second instance of that shape — mg-853a hit it and routed around it
deliberately (*"it ships in pogod, and the only thing that installs pogod is the
redeploy it would unblock"*), which is why pm-pogo ruled it a rule rather than a
bug (mg-ce10).

**Why script-side fixes it — the activation paths differ.** The three artifact
classes on this box go live by different routes:

| artifact | activates on |
|---|---|
| tracked files in `deploy-src` | `git fetch` + `--ff-only` merge (`sync_src` runs before every deploy) |
| `pogod` / `pogo` binaries | build + install — i.e. only a **successful** deploy |
| launchd plists | install + load |

A guard against deploy failure must live on the **merge**-activated path, never
the build-activated one. `scripts/revision-probe.sh` is a tracked file, so a
`git pull` arms it: no `go install`, no build, no redeploy.

```bash
scripts/revision-probe.sh
scripts/revision-probe.sh --stale-after 12h --mail
scripts/revision-probe.sh --repo ~/.pogo/deploy-src --url http://127.0.0.1:10000
```

Two reads and no build:

```
running   = curl -s localhost:10000/version | jq -r .revision
reference = the tip of origin/main
if running != reference for longer than N -> alert, naming the gap in commits
```

Exit status: `0` clean (current, or diverged for less than N), `1` **ALERT**, `2`
the probe could not run — no `curl`/`git`, an unreadable checkout, or a daemon
that did not answer. A check that could not run has **not** found its subject
healthy, so `2` is a finding and not a shrug; an unreachable daemon and a daemon
that answers without naming a revision are reported as different states, because
the first owes a restart and the second owes an investigation.

**It never touches `go`, `pogo`, `pogod` or `jq`** — all of which the redeploy
installs (or, for `jq`, may simply be absent). That is not tidiness: a probe that
reached for any of them would be armed only by a deploy that worked, which is the
defect it exists to remove. `scripts/revision-probe_test.sh` poisons all four
first on `PATH` and asserts both that no marker was written and that the probe
still reached its own verdict — either assertion alone passes against a fallback
that happens to agree.

**The reference is read from the remote, not from `origin/main` locally.** A
remote-tracking ref is only refreshed by a fetch, and in `deploy-src` the thing
that fetches is the deploy runner. On a night the deploy never fires, that ref
does not advance either — so a probe keyed to it compares two stale numbers,
finds them equal, and reports health. That is the same defect one layer down. The
probe uses `git ls-remote` (read-only, nothing fetched, nothing mutated) and
falls back to the local ref only when the network read fails, **saying so in the
report**. Measured on 2026-08-07 against the live box, this is not hypothetical:
`~/.pogo/deploy-src` did not contain `origin/main`'s tip at all, and the probe
said so.

**The clock is keyed on the running revision.** The first divergence is recorded
in a stamp (`~/.pogo/revision-probe.stamp`, `--stamp` to move it). A **changed
running revision resets it** — a new binary is live, so a deploy did happen,
which is the event being watched for. A **moved reference does not** — `main`
advances all day, and a clock it could reset would leave the alarm permanently
disarmed on a busy repo. Without the stamp the probe would fire on the normal gap
between a merge and the 03:00 deploy, and an alarm that is always on is an alarm
nobody reads.

**Arming it: `com.pogo.revisionprobe` (mg-a03d).** mg-ce10 landed the probe and
wired it to nothing — 501 lines, referenced by a changelog fragment, this docs
section and `test.sh`, and by zero schedules, zero plists and zero callers. That
is the **limiting case of the rule the probe implements**: a detector for "X did
not happen" must not be activated by X, and one activated by *nothing* is present
by existence and absent by effect. It is armed now, by a LaunchAgent of its own:

```bash
scripts/install-revision-probe.sh              # install / re-install, then verify
scripts/install-revision-probe.sh --dry-run    # render and print, touch nothing
scripts/install-revision-probe.sh --uninstall  # bootout and remove
```

**Why launchd and not the two obvious alternatives.** `pogo schedule` looks like
the right answer and is not: its scheduler lives inside `pogod`, and its only
delivery mechanisms are a nudge or a mail **to an agent** — it cannot run a
command. Arming the probe that way needs a live `pogod` *and* an agent turn to
execute the instruction, and both are failure modes in this exact lineage (a
stopped `pogod` is the state the probe most needs to report, mg-6d2f). Calling it
from the deploy runner is refused by the rule itself: a probe invoked by the
deploy cannot witness the deploy that never fired, which is four of the eight
failing nights (mg-2def) — driftwatch's shape (mg-5bd2), not a fix for it.
launchd is triggered by the OS clock and is independent of `pogod`, the deploy,
the refinery and any agent turn.

**Hourly, at :20, with a 24h threshold and a 12h re-notify.** The three numbers
are chosen together. The divergence clock can only mature as fast as the probe
samples — a once-a-day probe first *sees* a divergence up to a day after it
starts, so a 24h threshold would need three consecutive failed nights to fire.
Hourly sampling makes 24h mean 24 hours. The cost is 24 identical notifications a
day for one unchanged fact, which the threshold itself exists to prevent, so the
notify rate is throttled separately (`--renotify`, default 12h) rather than by
slowing the schedule. A **failed** send is not recorded as a notification: the
alert reached nobody, so the next run tries again.

**Replay policy — declared, because launchd has no field for it.**
`StartCalendarInterval` is **deferred-once** across sleep: a fire missed while the
host slept is delivered once on wake, and ten missed hourly fires coalesce into
one run. That is the right policy here because the report is not a per-interval
sample — the age is read from a persisted stamp against wall clock, so a late
report is still true and still names the correct age. A skip policy would discard
the first report after a wake, which is the one most likely to carry news.
**A host that is POWERED OFF misses the fire outright** — launchd defers across
sleep, not across shutdown — so this witness would have been dark on the
2026-08-07 no-fire nights, which were a power-off.

**It reports EITHER WAY, and that is what makes the witness itself checkable.**

```bash
tail -5 ~/Library/Logs/pogo/revision-probe.log      # the LEDGER: one line per run
tail -40 ~/Library/Logs/pogo/revision-probe.report.log
launchctl print gui/$(id -u)/com.pogo.revisionprobe | head -20
```

```
2026-08-09T19:20:03Z exit=0 OK           running=e8dd75f1 reference=e8dd75f1 age=-     threshold=24h
2026-08-09T20:20:04Z exit=0 DIVERGED     running=738e322a reference=e8dd75f1 age=3h12m threshold=24h
2026-08-10T08:20:02Z exit=1 ALERT        running=738e322a reference=e8dd75f1 age=1d0h  threshold=24h
```

The ledger is written from an EXIT trap rather than from each terminal branch, so
"either way" is structural instead of remembered — including the exit-2 paths,
which record `UNREACHABLE` and `NO-REVISION` as the distinct states they are. The
point is not tidiness: **a witness that writes only when it is unhappy cannot be
told apart from a witness that is not running.** The newest line's age is the only
thing on this box that answers *"is the probe still firing?"*, and nothing alerts
on it — a reader has to look.

**What it witnesses, and what it does not.** Named here because an instrument's
silence only means something if its blind spots are written down.

| witnesses | |
|---|---|
| the nightly deploy never fired | running revision never changes while `main` advances |
| the deploy fired and failed | same |
| the deploy fired, **exited 0**, and left the fleet on the old binary | doctor's 2026-08-09 case: the 09:39 run reported success with the daemon eight hours older than the merge it was meant to carry. Nothing else on this box asserts the *running* revision after a deploy |
| `pogod` not running, or not answering | exit 2 |
| `pogod` answering but unable to name its revision | exit 2, kept distinct |

| does NOT witness | |
|---|---|
| a host powered off at every fire time | above |
| this job being booted out, or its plist deleted | nothing re-arms it; the ledger going quiet is the only sign |
| `pogod` on the right revision in the wrong **run mode** | index-only serves `/version` happily — that is `/server/mode` and mg-6d2f's subject |
| any long-lived process that is not `pogod` | the bridget reader ran two days older than the merge that changed its behaviour and nothing reported it (mg-c2f5 / mg-8158). Scoped narrow deliberately; filed, not implied |

**The circularity, so it is not rediscovered as a bug.** This job reaches a box
through a merge and an install, and the install is part of the deploy it watches.
It can never witness the deploy that *installed* it — only the first deploy after
that, and every one thereafter. "It confirmed tonight's deploy" is not available
as an acceptance criterion for the install itself.

**The installer is a tracked shell script for the same reason the probe is.**
`pogo service install-recovery` and `install-deploy` are the house pattern and
both live in the `pogo` **binary**, which the redeploy installs — an arming step
that needs a current `pogo` cannot arm the box whose `pogo` is ten days stale,
which is the box that needs arming. It renders the tracked plist rather than
keeping a second copy of it (the drift class mg-b201 paid for), refuses rather
than half-installing a job that cannot run, and verifies with `launchctl print`
instead of trusting `bootstrap`'s exit code.

`--mail` exists so the alert does not depend on an agent turn running: *"and then
mail human"* left as an instruction in a scheduler message only happens if a turn
happens, and turns that never run are half of this ticket's lineage. It resolves
`mg` by self-identification (`/usr/bin/mg` is the Micro-Emacs editor and
satisfies both `-x` and `command -v`), and refuses a bare `mg` rather than mailing
through the wrong binary.

**Siblings.** `driftwatch` inside pogod answers "what am I running?" once live and
is not replaced by this — the two exist together, because a component cannot be
the sole reporter of its own absence. `pogo check-staleness` (above) reads the
deploy's own *record* of its runs; `pogo service status` compares running vs
installed vs `main` and is itself deploy-installed.

## The external witness that the FLEET is still completing turns (`scripts/fleet-liveness-probe.sh`)

**The rule this file implements:** *a detector hosted INSIDE the population it
watches cannot report that population failing.*

It is the population-level sibling of the revision probe's rule above, and it was
filed after an incident in which the check built for the exact failure did not
run. On 2026-08-14 all seven crew agents completed their last turn inside a
ten-minute window and stayed stopped for ~118 hours. `deploy-verify` §0 exists
for precisely that, its own text asks the right question — *"would this read
green over a fleet that is present and doing nothing?"* — and its
completed-turn-artifact test would have caught it on the morning of 08-15. **It
never ran, because it is `deploy-verify-architect`, one of architect's own
schedules, and architect was one of the agents that stopped.**

That is a circularity, not a coverage gap. A coverage gap is filled by adding a
check; here the check EXISTS, is CORRECT and is armed nightly, and is disarmed by
the event it detects. The same incident produced two more instances in
independently-built components: ack-watch's escalation names `mayor` as a
recipient while `mayor` is in its own `blackout_agents` set, and every fleet-wide
scheduled check on this box routes through the coordinator.

**The predicate is one `stat` and a subtraction.**

```bash
newest=$(ls -t ~/.pogo/agents/turnlog/*.log | head -1)
age=$(( $(date +%s) - $(stat -f %m "$newest") ))       # ALERT past 2h
```

```bash
scripts/fleet-liveness-probe.sh
scripts/fleet-liveness-probe.sh --stale-after 2h --mail
scripts/fleet-liveness-probe.sh --self-test-only        # the delivery control, alone
```

**NEWEST-across-all, never per-agent, and that is the whole design.** An idle PM
legitimately goes hours between turns, and this box carries an `a270.log`
untouched since 2026-08-11 by design — a per-agent threshold pages on both. If
the most recent turn by ANY agent is old, every agent is down simultaneously,
which has happened twice (2026-08-11, 22h; 2026-08-14, ~118h) and has never been
benign.

The fleet form was also chosen on a **correctness** argument, not a simplicity
one. The per-agent form (turnlog line vs that agent's process start) was
implemented first and its first version reported UNPROVEN for all seven agents
including ones that were plainly fine: it read uptime from the wrong `awk` field,
the parse yielded zero, a zero uptime puts `process_start` at *now*, so every
turnlog line predated it. The fleet form **parses nothing** — no uptime, no pid,
no field extraction, no dependency on any CLI's output format — so it is
structurally incapable of that failure. And a per-agent detector's failures are
**correlated** through the shared parser: seven agents agreeing is one chance of
being wrong, not seven, so unanimity there is worth nothing as corroboration and
is indistinguishable from the fleet stop it is meant to detect.

Keep `pogo check-turns` as the per-agent diagnostic a human runs *after* this
alarm fires, not as the thing that decides whether to page.

**Three cells, and a failed measurement must not fail toward ALARM.**

| exit | verdict | means |
|---|---|---|
| 0 | `OK` | some agent completed a turn inside the threshold |
| 1 | `FLEET-STOP` | no agent did — every agent is down at once |
| 2 | `UNMEASURABLE` | this probe could not measure the fleet, and says which row and why |
| 3 | delivery control failed | detection is fine and the alarm cannot reach anyone |

A two-valued instrument has nowhere to put "I could not measure this", so the
failure lands in whichever cell the arithmetic happens to produce. `UNMEASURABLE`
is per-row and is never folded into either verdict: an absent turnlog directory,
an **empty** population (which is not a stopped one), a row that cannot be
stat'd, and a **future** mtime — the last being the only way this predicate could
read GREEN over a broken measurement, since a negative age is younger than every
threshold. A dangling symlink is caught by an explicit existence check, because
`stat -L` on darwin does *not* fail on one: it falls back to the link's own
mtime, which would read as a completed turn that never happened.

**It alerts by MAIL, never nudge, and that was measured.** During the outage,
`--immediate` nudges recovered the three agents that were merely unreachable and
did nothing for the three that were wedged. The coordinator never goes idle, so
the idle gate drops its nudges — *continuous PTY output makes an agent look
busiest exactly when it is least reachable* — and `pa` answered a wake with
silence, after which `wake_silence_once` stopped trying for 106h with the "already
woken N ago" age climbing and never resetting (mg-3a8a). Mail is the only wake
channel that survived both rules; it is how architect itself recovered. The alert
goes to `human`, because during a fleet-wide stop there is by definition no
in-fleet actor left to act — doctor, which would normally restart a wedged agent,
was itself inert.

**It proves it can DELIVER, not merely detect.** Detection and delivery are
separate halves and only the first is exercised by ordinary operation: a detector
that never fires never tests its own notification path. `com.pogo.revisionprobe`
delivered three correct alerts to `human` on 08-16 and 08-17 and then went silent
mid-incident **with no code change**, because its capability probe is `cmd | grep
-q` under `set -o pipefail` — grep exits on the first match, the producer takes
SIGPIPE and exits 141, and a working binary is reported ABSENT (mg-7ce7). The
pattern is not refuted; the idiom is. That is *more* alarming than a component
that never worked, which gets caught the first time anyone looks.

So the probe runs a positive control on its own notification path **on a
cadence** (default 12h, `--self-test-every`), not once at install: it sends a
real message, **reads it back out of the mailbox by new message id**, archives
it, and separately confirms the alert recipient is addressable without sending to
it. The installer runs the same control before arming and refuses if it fails —
necessary and not sufficient, because one passing run of a race is a coin landing
the right way.

**The ledger records the send RESULT, not the send attempt.**

```bash
tail -5 ~/Library/Logs/pogo/fleet-liveness.log       # the LEDGER: one line per run
tail -40 ~/Library/Logs/pogo/fleet-liveness.report.log
launchctl print gui/$(id -u)/com.pogo.fleetliveness | head -20
```

```
2026-08-19T09:07:02Z exit=0 OK           newest=mayor.log age=3m     threshold=2h agents=8 mail=n/a       selftest=fresh(4h)
2026-08-19T09:22:01Z exit=1 FLEET-STOP   newest=mayor.log age=2h11m  threshold=2h agents=8 mail=delivered selftest=ok
2026-08-19T09:37:02Z exit=1 FLEET-STOP   newest=mayor.log age=2h26m  threshold=2h agents=8 mail=throttled selftest=fresh(0m)
2026-08-19T09:52:01Z exit=2 UNMEASURABLE newest=-         age=-      threshold=2h agents=0 mail=n/a       selftest=- -- no *.log under ...
```

`mail=` is one of `n/a` / `computed` / `throttled` / `no-mg` / `send-failed-rc-N`
/ `attempted-unconfirmed` / `delivered`, and only the last means the message was
found in the recipient's mailbox afterwards. An `attempted-unconfirmed` send
deliberately does **not** start the re-notify throttle, so the next run tries
again rather than recording a notification that reached nobody. The distinction
is not bookkeeping: counting computed alerts as sent ones is what produced a
claim about the revision probe that had to be withdrawn once the mail record was
consulted.

The ledger is written from an EXIT trap rather than from each terminal branch, so
"either way" is structural instead of remembered — including the setup-failure
path. **A witness that writes only when it is unhappy cannot be distinguished
from a witness that is not running**, which is this whole ticket.

**Arming it: `com.pogo.fleetliveness`.**

```bash
scripts/install-fleet-liveness-probe.sh              # install / re-install, then verify
scripts/install-fleet-liveness-probe.sh --dry-run    # render and print, touch nothing
scripts/install-fleet-liveness-probe.sh --uninstall  # bootout and remove
```

launchd, not `pogo schedule` and not a crew schedule. `pogo schedule` lives inside
pogod and can only deliver a nudge or a mail to an *agent*, so it needs pogod
alive and a turn to execute the instruction — both failure modes in this lineage.
A crew schedule reproduces the circularity verbatim: that is what `deploy-verify`
§0 was. Fires every fifteen minutes at :07 :22 :37 :52, deferred-once across
sleep; a host that is powered **off** misses the fire outright and nothing
replays it.

**Siblings, and what this does not close.** `internal/turnwatch` reads the same
artifact from inside pogod and is not replaced by this: it covers FLEET DOWN,
POGOD UP at minutes of latency, and its own package header says it does not close
POGOD WEDGED rather than exited, because a resident reader wedges with its host
and launchd restarts on exit only. This job covers that cell. `pogo agent list`
is **not** a substitute and was actively misleading during the outage — it showed
`last-activity=just now` for agents whose turnlogs were five days stale.

**Nothing watches this probe**, and that is stated rather than implied. Its
ledger is its heartbeat and the self-test mail is a second one, and both still
need a reader — the same class one level out.


## Is something versioning `$POGO_HOME`? (`pogo doctor --check`)

`$POGO_HOME` can be a git working tree, and on this fleet's host it is: `~/.pogo`
is the working tree of `drellem2/pogo-config`. That is fine for files that live
nowhere else — the crew prompts, `bin/pogo-recovery.sh` — and corrosive for the
files pogo writes itself. `InstallPrompts` rewrites `agents/mayor.md`,
`agents/crew/doctor.md` and `agents/templates/*.md` on every install, so a repo
that tracks them can never have a clean tree, and a merge into that repo fights
the installer over the live prompts the crew is reading.

The `$POGO_HOME version control` row of `pogo doctor --check` reports it:

```
✓ $POGO_HOME version control  /Users/x/.pogo is not inside a git work tree, so nothing
                              versions the 10 path(s) pogo writes there
! $POGO_HOME version control  git work tree /Users/daniel/.pogo (origin …/pogo-config.git)
                              TRACKS 8 of the 10 path(s) pogo writes under /Users/daniel/.pogo:
                              agents/mayor.md [install, MODIFIED]; … 8 of them are modified
                              RIGHT NOW by pogo rather than by hand …
```

It warns and never fails — the remedy is a machine-local ops action with a blast
radius, and doctor's exit code is not the place to force it. **Do not fix a
warning here by committing the modified files**: that records one machine's
install output as shared truth and re-dirties on the next install.

The decision about what such a repo is for, the measured state of this host, and
the reconciliation sequence (which never runs `git pull` or `git checkout` in the
live tree) are in
[docs/pogo-home-version-control.md](pogo-home-version-control.md).

## Is a consumer reading a source nothing writes to? (`pogo doctor --check`)

A launchd job can be loaded, healthy, polling on schedule, reporting no error,
and reading a directory nothing writes to. Every instrument that watches the
*job* reports green — `launchctl list`, its own log, its heartbeat — because
every one of them is true. Nothing on this machine reported the state at all
until mg-c2f5.

That is what `com.pogo.notify` did for at least 40 hours from 2026-08-07.
mg-65d2 re-pointed it from `~/.macguffin/mail/human/new` to
`~/.macguffin/mail/daniel/new` as step 4 of a staged cutover behind a relay that
is not activated yet; the fleet still writes `human`, so 100% of Daniel's
notifications were carried by the fail-open `com.pogo.deadman` behind it. **That
intermediate state is designed, not a misconfiguration** — completing or
reverting the cutover is tracked as mg-8158 and is Daniel's decision. The
missing alarm was the defect.

The `consumer source liveness` row compares each consumer's **configured
source** against **where data is actually arriving**:

```
✓ consumer source liveness  every consumer's configured source has received data in the
                            window — each one was compared against where data is actually
                            arriving, not against its own poll loop. 2 consumer(s) examined
                            over a 6h0m0s window: 2 reading live sources, 0 starved, …
! consumer source liveness  1 consumer(s) are reading a source NOTHING IS WRITING TO while
                            comparable sources receive: com.pogo.notify reads
                            MAIL_DIR=…/mail/daniel/new and NOTHING HAS ARRIVED THERE in the
                            last 6h0m0s, while 18 of 1364 comparable sources are receiving,
                            most recently …/mail/mayor/new … (and 15 more). …
```

Three things about it are deliberate and worth keeping if it is ever edited:

- **It names no box.** "Alarm if notify watches `daniel/` while agents write
  `human/`" would decay the moment the cutover completes — and completing it is
  the expected outcome. Consumers and their sources are discovered from the
  installed plists (`internal/sourcewatch`, admission rules in `discover.go`), so
  the next re-point is caught without anyone editing the check.
- **Quiet everywhere is NOT a pass.** If nothing is written anywhere, no
  consumer can be convicted by comparison, and the row says `NOT CHECKED`
  instead of green. A check that reported health when the fleet died would be
  measuring its own execution — the defect it exists to catch, one level down.
- **It warns and never fails.** Whether to finish a cutover, re-point a consumer
  or retire it belongs to whoever owns the routing; on 2026-08-09 three separate
  agents proposed the *wrong* one of those three for this very consumer.
  Doctor's exit code is not the place to force that decision.

An **absent** `consumer source liveness` row means an old `pogo` binary, not a
clean machine — the detector ships inside the binary and is therefore subject to
the same class of defect it reports.

## Is compute still running for a polecat that is gone? (`pogo check-orphans`)

A polecat starts background work — `nohup … &` from a tool-call shell that then
exits — the work reparents to launchd, the polecat finishes its ticket, merges
its branch, and is reaped. **The compute keeps running.** Measured instances
(mg-4518): 38% CPU out of an audit instrument's directory; 94% for 44 minutes
after the owner's branch had already merged, writing into a scratchpad with no
reader left; three simultaneous survivors aged 44 minutes to 2h21m.

This is not a tidiness problem. The box has ten cores and the fleet has driven it
to load 137. At that contention `TestGateWatchMeasuresARealSubtreesCPU` — a test
that measures a real subtree's CPU, and so measures the contention — **failed in
the refinery gate**, costing two unrelated branches a merge attempt each and
sending a reader hunting a second code defect that did not exist. An orphan does
not only waste CPU; **it manufactures deterministic-looking failures in branches
that have nothing to do with it** (mg-6c90 is the same contention class).

```
$ pogo check-orphans
orphaned compute — polecats root /Users/daniel/.pogo/polecats
  source darwin-ps (10ms CPU-time resolution, usable from a 50ms window)
  window 2s, floor 0.20 cores per OWNER, candidate floor 0.02 per process
  633 processes sampled, 8 above the candidate floor
  3 spared (owner still running), 0 spared (owner under the floor), 4 unattributable, 1 cwd unreadable

No orphaned compute.
```

Exit 0 clean, 1 at least one orphan, 2 usage, 3 this run measured nothing.

**Nothing runs this for you on a cadence** — it is on doctor's sweep list, and
otherwise it runs when somebody types it. That is the gap mg-c675 was found
through: the incident below was caught by reading `ps` by hand, not by any
detector.

### The predicate, and the two things it must NOT key on

    cwd    → the owning polecat → registry liveness.   Orphan iff the owner is dead.

**Not `ppid`.** `ppid=1` is not the signature of a leak; it is the signature of
*any* polecat starting background work, because `nohup … &` from a tool-call
shell that exits reparents every worker it launches. On 2026-08-07 four workers
belonging to **one running polecat** all showed `ppid=1` at 60–68% CPU. A sweep
keyed on that would have destroyed all four mid-computation. Reparenting destroys
`ppid`; it does not touch `cwd`, which is a property of the process itself and
carries the owner's id in the path.

**Not `ps %cpu`.** That column is a lifetime average and understated a live
instance of this defect by about 3×; two reads of the same population disagreed
by a factor of three within minutes. The rate here is cumulative CPU time
differenced across a window — work actually performed in it. Use `top -l 2`
(second sample), not `ps`, if you are reading load by hand.

The **rate floor separates two defects** and is not a severity filter. A
`pogo-deploy.sh` blocked forever in an unbounded `git fetch` ran 31h39m —
correctly parented, reported by nothing, at ~0% CPU. That is a stuck process and
routes elsewhere; this reports detached *compute*. Since mg-c675 that separation
is `--candidate-floor`'s job (0.02 cores) and it decides only what gets
*attributed*; `--floor` decides what gets *reported*.

**Not a per-process rate either (mg-c675).** A polecat generating synthetic load
orphaned 52 busy-loops that held **8.7 of this host's 10 cores for 41 minutes**.
Fed that exact population, the per-process form of this detector reported a clean
host having examined *none* of them: 8.7 cores shared by 52 contending processes
is 0.167 each, under a 0.20 floor calibrated on mg-4518's orphans, which came one
at a time at 0.38–0.94 cores. Processes contending for a fixed number of cores
get capacity/N, so **a per-process floor goes blinder as the leak gets worse** —
the leak large enough to saturate the host is precisely the one it cannot see.
That sign on the error is why the constant was not simply retuned.

So **the floor is summed per owner**: a dead polecat is reported when the
processes it left behind *together* clear it, which subdividing cannot get under.
mg-4518's single 0.94-core orphan still trips it unchanged. A fifth disposition,
`below_owner_floor`, holds processes attributed to a dead owner whose total is
under the floor — spared as trivial, but *decided about*, so a floor set too high
shows up as a growing count rather than as silence.

The residual blind spot, stated rather than left to be rediscovered: a population
escapes if its total clears `--floor` while **every** member sits under
`--candidate-floor`, which needs more than ten of them all under 0.02 cores.
Spinning processes only get that small when there are 500+ on a ten-core box;
duty-cycled work gets there at any count and is the stuck-process class by
another name. Setting `--candidate-floor` at or above `--floor` reinstates the
per-process rule and is **refused** (exit 3), not clamped.

### It reports. You kill, by PID.

The command never signals a process. On a finding it prints the pids and the
`kill` line. **Never `pkill -f`** — an unanchored pattern has taken this box out
before by matching the fleet's own pollers. Re-read the owner's status before
killing: the registry answer is the whole safety margin.

On a finding the report leads with the **owner** — "this polecat is gone and is
still holding N cores across M processes" — before the per-pid list. Reading a
52-process swarm off 52 near-identical process lines is how 87% of a host went
unnoticed for 41 minutes.

Two states are counted and never convicted, both failing closed:
**unattributable** (a busy process whose cwd carries no polecat marker — a worker
that `chdir`'d out of its tree looks exactly like every unrelated program on the
machine) and **cwd unreadable** (the kernel would not disclose it). An
unreachable agent registry is an *instrument failure*, exit 3, not a clean run:
without it every attributable process has a dead-looking owner.

### Believing a clean report

```
$ pogo check-orphans --probe
constructed orphan pid=8254 (ppid=1, owner zzdead dead, 1.00 cores): detector REPORTED it
live-owner control pid=8257 (ppid=1, owner zzlive alive, 0.00 cores): detector spared it

PASS — the detector fires on a constructed orphan and spares an identical-looking
process whose owner is alive. Both arms, on real processes.
```

The probe builds a throwaway polecats tree, starts two real CPU burners, detaches
them so they genuinely reparent, and checks **both arms**. The second is not
decoration: it is the exact case that killed the `ppid` heuristic. The same probe
runs in `go test ./...`, so the refinery gate exercises the failing arm on every
merge — a detector whose RED path nobody runs is indistinguishable from one that
cannot fire.

### What this does not fix

Teardown. A long-running compute job should die with its runner or be detached on
purpose, and neither is true today. Note that **killing the polecat's process
group at reap would not reach these**: measured on this host, the agent leads its
own group (`pgid == pid`, from `pty.StartWithSize`'s `Setsid`), but each harness
tool call runs in a *separate* group, so a child spawned from one inherits the
tool shell's pgid and `kill(-agentPid)` misses it entirely. Whatever teardown
lands has to reach a group the agent does not lead. This detector is the safety
net for the population that already exists and for runners that escape teardown
later.

## Does an open item already have its work on a branch? (`pogo check-stranded`)

An item reads `available` on the board. Its work is finished, pushed, and sitting
on a polecat branch. The polecat that did it is dead, so nothing is going to mail
anybody — and priority-wake advertises the item, so **the recommended next action
is the one that re-derives the work**. mg-9a19 lost 1026 lines exactly that way.

A spawn-time guard already refuses that dispatch, and it works. But it fires **at
dispatch**, so an item nobody dispatches at is never checked. On 2026-08-09 four
branches were stranded across three repos: one caught by the guard, one by a
person reconciling something unrelated, **two by the accident of somebody looking
next door**. This is the periodic half.

```
$ pogo check-stranded
stranded and landed-not-closed work — open items joined to their branches
  114 open work item(s) scanned across 1 repo(s)
    /Users/daniel/dev/pogo — 73 item(s), 683 polecat branch(es), refs refreshed from origin
  1 exclusion(s): branches of running polecats and branches already queued

3 FINDING(S) — 0 stranded, 3 landed-but-not-closed, 0 conflict suspect, 0 UNJUDGED:

  landed_not_closed available  mg-65d2
    Build the REPRESENTATIVE relay: move the two readers, not the twenty-one writers
    branch polecat-p65d2 (pushed) vs refs/remotes/origin/main
    1 commit(s) already in the target under a rewritten sha; 0 unmerged.
    The work is ON the target and the item is still asking for it.
    -> mg done mg-65d2 --result='{"branch": "polecat-p65d2", …}'
```

Exit 0 clean, 1 at least one finding, 2 usage, 3 this run measured nothing.

### Four row kinds, and their remedies are not interchangeable

| kind | what it is | remedy |
|---|---|---|
| `stranded` | the branch has commits the target does not | `pogo refinery submit`; do **not** dispatch |
| `landed_not_closed` | the branch is fully merged, the item still asks for it | `mg done` |
| `conflict_suspect` | the two instruments below disagree | read it yourself; **neither** command |
| `unjudged` | the branch could not be read | re-run; this is not a clean row |

`landed_not_closed` is the worse one and it has an **upstream fix**, so it should
stay near-empty: pogod used to close an author's item at merge only when a polecat
had claimed it, so a coordinator submitting a stranded branch by hand left the
item open with its work on main. It now closes the item whenever the MR's author
is shaped like a work-item id, with no registry lookup — a hand-submitted branch
has no polecat by construction. The sweep still reports the row, because that fix
cannot see an item stranded before it shipped or one whose close was refused.

### The instrument, and the blind spot it actually has

**`git rev-list --count main..<branch>` does not work.** The refinery re-commits
what it merges, so a successfully merged branch is "ahead of main" forever; 65
false positives came out of it before anyone noticed, and a reader on another
ticket briefly believed landed work was stranded from the other side.

**`git cherry` compares patch ids** and gets that right. Its residual blind spot
is *not* conflict resolution — the refinery **aborts** on a rebase conflict
(mg-eac0) and never merges through one. It is **context drift**: the refinery
rebases into its own copy and does not force-push the branch, so origin keeps the
commit as written while the target gets it as replayed. A patch id covers the
diff's context lines, so **a neighbouring change is enough**. Of 57 branches
`git cherry` called unmerged on 2026-08-10, at least five are on main under
another sha. The exact control:

```
77e012c  origin/polecat-79dc   patch-id 959d2fa2…
1e1292f  main                  patch-id 5a479b4d…
identical --stat; every added and removed line byte-identical
```

So a **content-level second opinion** runs on every stranded candidate: what
fraction of the substantive lines the unmerged commits ADD does the target
already hold? At ≥95% (and ≥20 countable lines) the row becomes
`conflict_suspect`, which recommends **neither** remedy. Deliberately
conservative — branches at 0.88, 0.91 and 0.94 are also on main and are also not
demoted, because under-demoting costs a line of report while over-demoting costs
a branch.

### A failed read is a row, not a clean answer

The natural predicate is `git cherry <target> <branch> | grep -q '^+'`, and it
**answers clean whenever git fails** — a failed git prints nothing, and "no
output" is how that predicate spells landed. On a sweep, one network blip then
converts a stranded branch into an all-clear over work sitting unmerged: this
detector's own defect, rebuilt inside its remedy. Anything unreadable is
`unjudged`, ranked immediately behind `stranded`, and exits 1.

### Two exclusions, both counted (`--all` names them)

**A running polecat's branch.** It has unmerged commits on a claimed item because
that is what work in progress *is*; `polecat-qfa70` was mid-flight during the
manual sweep and looked identical to a strand. **A branch already in the refinery
queue** — the remedy for a stranded branch is to submit it. An unreachable agent
registry is an instrument failure (exit 3), not a clean run: without it every live
worker in the fleet reads as a strand, and a detector that fires on healthy input
teaches its readers to skip the line the real stranding surfaces on.

### It is item-driven, and that is why it is readable

A branch-first sweep of this repo finds 57 rows, 48 of them on archived items and
2 on no item at all. Walking the ~115 open items instead produced three. Ranking
is on **item status**, `available` first, because that is the status priority-wake
advertises — not on branch count.

### The command is deliberately NOT on a clock

Nothing schedules `pogo check-stranded`, and that is a decision rather than an
omission. The condition it re-derives is already answered, correctly and
automatically, by the two reporters below — so a timer running this would be a
third opinion on a settled question, paid for on every tick. Run it by hand when
you want the item-side view (especially the `landed_not_closed` residue, which
neither reporter can see), and rely on the mail for the rest.

## Does a memory store still have a reader? (`pogo check-memdirs`)

`pogo doctor` already judges every auto-memory index on the machine three ways:
over the load cap, holding notes the index does not name, and carrying a hook
whose item has moved on. All three are properties of a store some session is
still using. **`pogo check-memdirs` answers the question none of them can: has
this store quietly stopped having a reader at all?**

```
$ pogo check-memdirs
check-memdirs: 153 note(s) in 5 per-agent store(s) that nothing loads.

  pm-pogo           62 note(s)   newest 2026-07-08T04:33Z
                    ~/.claude/projects/-Users-daniel--pogo-agents-pm-pogo/memory
                    e.g. feedback_always_on_pm.md, feedback_block_on_human.md, ...
```

### How a store stops having a reader

A harness keys its per-session memory store on the session's **project**, and for
a directory inside a git repo the project is the repo, not the directory. So
making a parent directory a git repo re-keys every agent underneath it onto one
shared store.

That is usually an improvement — it is how this fleet's crew came to share one
corpus. What it does not do is move the notes already written. `~/.pogo` became a
git repo on 2026-07-07; **153 notes across five per-agent stores stopped
participating in recall that day, and it took five weeks and a duplicated
investigation to notice.** One stranded note had recorded a finding that two
agents later re-derived from scratch and filed as new work (mg-b6bd, then
mg-a9b3).

Nothing was misconfigured and no write ever failed. Every file was on disk and
readable the whole time. **The failure is invisible from inside every session**,
which is what separates it from the three `pogo doctor` already covers: the
agents that used to write there have healthy recall against a different store and
indexes at exact parity, so no instrument is pointed anywhere that hurts.

### Why it is not an age check

"A store nothing has written to in N days" fires on every legitimately dormant
per-repo store on the machine, and still cannot see a store stranded five minutes
ago. The signal is not staleness — it is that the store is keyed to a directory
**pogo itself owns**. pogo creates the agent working directory and runs the agent
in it, so a store hanging off it has exactly one possible reader by construction,
while a shared store has many. Once the fleet's memories live in one shared
store, a populated per-agent store is a store with no reader whatever its mtime
says.

### An empty store is not a finding

A retired store is deliberately left holding its index and no notes, so the
tombstone explaining the retirement survives for the next reader. Deleting the
directory outright would only mean the next session on that project root
re-creates it silently — which is the failure this replaces. So a store holding
only `MEMORY.md` reports clean, by design, and that is what makes the remedy
converge.

### The remedy is a triage, not a copy

This is the part the command's `--help` says at length, because the obvious
remedy is the harmful one. The batch that motivated the check contained a rule
that had since been refuted; moving the notes wholesale into a loaded store would
have delivered refuted guidance **reading as current** — which is the same defect
one level up. Decide per note whether it still holds, then leave the store holding
only a tombstone index.

The recovery that followed made this mistake once even while arguing against it:
a merged note carried a delivery path Daniel had removed five weeks earlier, and
it read as current until another agent caught it. The rule that came out of it:
**norms do not decay, mechanisms do.** A decision, a rule or a failure shape
survives being stranded; a path, process, config, schedule or file location does
not, and republishing one without re-measuring it is not safe.

### Exit codes, and why there are four

| code | meaning |
|------|---------|
| 0 | nothing stranded — the clean line also prints how many store paths were probed |
| 1 | at least one store holds notes nothing loads |
| 2 | usage error |
| 3 | **this run measured nothing** |

The path from an agent's working directory to its store needs the harness's own
encoding, which pogo does not own — so it lives in the provider
(`agent.Provider.AgentMemoryStoreIndex`) and a wrong model produces a path that
does not exist rather than a wrong finding. That is the safe failure direction,
but it means a silently-changed encoding would report zero findings forever. Exit
3 is how a blind run says so instead of printing an all-clear it has not earned,
and the probe count on the clean line is the positive control for the rest.

## Did a one-shot fire with nobody answering it? (`pogo check-oneshots`)

A recurring schedule that silently stops accomplishing anything shows up as a
growing unacked streak, and ack-watch escalates on it. **A one-shot has no
streak to grow.** It fires once, is never retried, and the obligations sent that
way are precisely the ones with no next cycle: post-redeploy verification,
`revision-check-post-0300`, pre-deploy steps.

```
$ pogo check-oneshots
One-shot obligations in the 2026-08-06 09:00 to 09:00
  read from: /Users/daniel/.pogo/events.log

UNANSWERED (1) — fired, and no turn ever reported the work done:

  verify-absentwatch-live-mayor → mayor, 2026-08-14 03:21
    reason:  one_shot_unacked
    kind:    other
    fired:   2026-08-13 03:21:00 (unanswered for 24h0m)
    carried: Verify the absentwatch fix is live on the running pogod, then reply on mg-7d20.
```

`pogo doctor --check` carries the same finding as its `one-shot acks` row. The
row warns and never fails: an unanswered one-shot is a missed obligation, not a
broken host, and putting it in the path of anything scripted against doctor's
exit status is how a detector grows into a gate.

### Two halves, filed a day apart

mg-64e6 made the outcome **recordable** — a fired one-shot is retained until it
is acked or its 24h window closes, and the single misleading `one_shot_complete`
became `one_shot_acked` / `one_shot_unacked` / `one_shot_undelivered` /
`one_shot_skipped`. It stopped there on purpose. mg-8011 is the consumer, filed
because a deferred half with no ticket dies with its gate: for a day the
distinction existed and **nothing read it**, which from a human's seat is the
original failure unchanged.

`verify-absentwatch-live-mayor` is the specimen. It carried mg-7d20's owed
post-redeploy verification and fired at 02:21 into a mayor that happened to be
alive. Had it not been, the record would have been indistinguishable from
success.

### It reports what the log RECORDS

Printed on every verdict, because both readings are easy to overstate: a
one-shot acked by an agent that then did nothing counts as **answered** here, and
a fire still inside its 24h ack window is neither answered nor missed (the row
counts those separately as in-flight).

### A window an older pogod wrote is reported as unmeasurable

The four labels ship in `d71e1e2`, which is **inert until pogod is rebuilt onto
it**. Before that every one-shot leaves as the retired `one_shot_complete`, and a
naive reader would print "no unanswered one-shots" over a fleet where the class
cannot be observed at all. Finding that label in the window makes this command
say so and exit 1 instead — the same trap that produced mg-afd0 and mg-3141.
`curl -s http://127.0.0.1:10000/version | jq -r .revision` answers what is
actually running.

### Not in ack-watch, and that is a decision

`internal/ackwatch`'s cohort gate excludes `Cadence <= 0`, which is every
one-shot, so ack-watch has never seen this population. That gate is load-bearing
there: its model is a delivered:completed **ratio** over repeated fires, which a
schedule that fires once cannot have. Folding one-shots into that cohort is a
judgment someone should make on its own evidence, not a wiring detail smuggled in
by a consumer.

## Who gets told when a polecat leaves pushed work behind

There are **two** automatic reporters, and between them they cover every polecat
this daemon has ever supervised. Both mail the coordinator; both are report-only.
Both are also defined over **pushed** commits — for the work a polecat never
committed at all, see [the third reporter](#the-third-reporter-uncommitted-work-in-a-preserved-worktree)
below.

**1. At release — `reportStrandedWorkOnRelease` (internal/agent/strandedgate.go).**
pogod checks a polecat's branch as it releases the claim, and mails if the branch
carries work the target does not. This fires on **every** release route: a
graceful `pogo agent stop`, a predeploy quiesce, and — through the reaper — a
crash, an OOM kill or a `kill -9`. It is precise: on 2026-08-09 there were five
stranded branches and five `work_item_stranded_push` events, 1:1, with nothing
else in the log.

It went unread for three months. Until mg-be37 its only outputs were pogod's log
and `~/.pogo/events.log`, and it runs inside pogod after the agent process is
gone, so it reached no terminal and no inbox. The measured gap between it
detecting a stranding and a person noticing was ~1h, 2.5h and ~3h. **If you are
ever tempted to add a detector here, check first whether the detector exists and
lacks a reader.**

**2. At pogod startup — `ReportStrandedWorkAcrossRestart`
(internal/agent/strandedsweep.go).** Reporter 1 needs an `*Agent` out of the
registry. The registry is in-memory with **no adopt path**, so a polecat that was
running when a pogod restarted has no entry in the successor and never will —
reporter 1 can never fire for it again, *including at a later graceful stop*. It
is un-instrumented for the rest of its life, and this fleet restarts pogod
nightly.

So pogod sweeps that population once per boot. The population is the **witness
store minus the registry**: `noteWitnessStart` records a polecat at spawn and
`noteWitnessExit` deletes the record when the daemon sees it exit, so a record
that survives into a new boot is exactly a polecat whose exit was never witnessed.
It is *not* "all branches minus the registry" — at boot the registry is empty, so
that reading would select all 634 polecat branches here.

A swept polecat may still be **running** (that is the orphan case, and the
orphaned-polecat alert covers the same process from the other side). Its mail says
so: the work is real and already pushed, but the branch may still grow, so do not
submit under it until you have established it is finished.

### What both mails carry, and what they will not do

Branch, ref, whether it is on origin, target, unmerged count and oldest commit,
the paste-ready `pogo refinery submit … --repo=… --author=…` line, and the
sentence that matters most: **do not dispatch a worker at this item.** The board
shows the item as `available` and priority-wake will advertise it as ready; that
advice is wrong for as long as the branch is unmerged and nothing on the board
says so.

Neither reporter submits and neither closes. A wrong auto-submit lands unreviewed
work; a wrong auto-close discards a branch.

A **failed send** emits `work_item_stranded_push_undelivered`. Without it, "no
stranding was found" and "a stranding was found and the mail bounced" are the same
silence — which is the defect this whole surface exists to close, one layer down.

### The third reporter: uncommitted work in a preserved worktree

Every guard above — the spawn refusal, `git cherry`, both reporters — is defined
over **pushed** commits, and is blind by construction to work that was never
committed. That is not hypothetical. `~/.pogo/polecats/qbe37` was preserved on
2026-08-10 with **16 uncommitted paths**, including an entire `internal/strandwatch/`
package (1450 lines) that existed in no other location on the machine. Had nobody
looked, `pogo gc` would eventually have reclaimed the tree.

**Something already knew, and it was addressed to the wrong question.**
`cleanupAgentWorktree` (cmd/pogod/worktreecleanup.go) preserves a dirty tree at
every exit route and mails the coordinator, and that has worked all along — 22
delivered notices over three days, two of them for qbe37. But the notice was
composed from the agent name, the repo and the tree, so it could say *a tree is
pinned, rescue it, reclaim it with `pogo gc`* and could not say **do not dispatch a
worker at this work item** — it did not know what a work item was. The message that
says exactly that sentence, `work_item_stranded_push`, is defined over pushed
commits and so never fired here. The fleet held both halves and combined neither:
on 2026-08-10 the coordinator received two preservation notices for qbe37 and
dispatched at its work item anyway. The work was rescued because a chain of mail
reached the new polecat in time, not because any mechanism connected the tree to
the item.

Since mg-32e3 the preservation path is handed `a.WorkItemID` — available at the
call site all along — and the two halves combine:

- **The notice carries the prohibition, in the subject.** `preserved uncommitted
  work in qbe37's worktree — do NOT dispatch at mg-be37`. The body says why nothing
  else will tell you, and that the board will advertise the item as ready anyway.
  A do-not-dispatch sentence buried in paragraph four of a message filed under
  worktree hygiene is a sentence that does not travel.
- **The fact is on the event spine** as `worktree_preserved`, carrying
  `work_item_id`, `repo`, the tree, the branch and the dirty-path counts — the
  record half this path never had. Its mail half was validated end to end and its
  record half was `log.Printf` to inherited stderr, the exact mirror of
  `work_item_stranded_push` before mg-be37. Query it with
  `pogo events --type worktree_preserved`.
- **The counts separate modified from untracked** (mg-d45b), and the branch is on
  the event too. A retained tree keeps its branch checked out — that is what pinning
  means — so the branch is both where a rescuer starts and the thing whose deletion
  the tree is blocking. And `dirty_paths: 16` fuses two facts with different
  consequences: a modified tracked path still has its committed version in the
  object store, while an untracked one is on no branch, in no stash and on no
  remote. `untracked_paths` is therefore the number that says whether a
  preservation is urgent — qbe37's tree held the only copy of a 1450-line package
  that way. When the branch cannot be read the event says so in `branch_error`
  rather than dropping the key, because a field that vanishes on failure is
  indistinguishable from one nobody implemented.
- **An unreadable tree keeps its own answer.** `outcome: undetermined` means
  `git status` failed, so the prohibition is the conditional one — *do not dispatch
  until this tree has been read* — and never a claim that there is work in it.
- **A missing work item is reported, not omitted.** A crew agent legitimately has
  none; a polecat with none has a broken agent record, and only a reader can tell
  which, so the notice says `work item: NONE RECORDED` rather than going quiet.
  An absent field and a field nobody passed looking identical is what this defect
  *was*.

It reports, like the other two. No dispatch is refused and no tree is reclaimed on
the strength of it: the detection was never what failed here.

**No third detector was built, and that was the point.** The signal existed and was
correct both times it fired; what it lacked was one field, and therefore the
ability to say the thing that stops the destructive action.

## GitHub branch protection on main (rulesets)

Since 2026-07-05 (mg-f7a3), `main` in **drellem2/pogo** (ruleset `main-require-pr`, id 18534732) and **drellem2/macguffin** (id 18534735) is protected by a GitHub ruleset per the gh-issue workflow design (`docs/design/gh-issue-workflow-design.md` §3):

- **Require a pull request before merging** — 0 approving reviews required. "Required approving reviews" is deliberately OFF: every actor on this machine shares one GitHub identity, and GitHub rejects self-reviews, so a review requirement would be unsatisfiable.
- **Block force pushes** (`non_fast_forward`).
- **Bypass actor: repository admin, mode `always`.** The refinery pushes to `main` as the admin identity, so refinery merges keep working unchanged — GitHub logs `Bypassed rule violations` on each such push. Only `main` is targeted (`~DEFAULT_BRANCH`); per-item `branch` targets the refinery supports are unaffected.

Inspect or modify with `gh api repos/drellem2/<repo>/rulesets` (effective rules: `gh api repos/drellem2/<repo>/rules/branches/main`). In an org deployment the bypass actor becomes a dedicated refinery GitHub App; the ruleset is otherwise identical.

## Running multiple instances

If you run more than one pogo instance on a host — a production fleet plus a
sandbox for verification, say — **give each instance its own `POGO_HOME`.** Every
pogo state path (the refinery queue, `schedules.json`, `agents/`, the Maildir,
`events.log`, and the agent attach sockets in `agents/sockets/`) derives from
`PogoHome()` (`internal/config/config.go`), which resolves to `$POGO_HOME` or
`~/.pogo`. Distinct roots isolate distinct daemons completely (mg-3dc3, mg-8532).

Attach sockets were the one exception until mg-8532: pogod derived their
directory from `$TMPDIR`, which is per-user rather than per-`POGO_HOME`, so two
daemons on distinct roots with identically-named agents shared one socket file.
If you are running an older pogod, expect `pogo agent attach` to reach whichever
daemon bound the socket last, and expect the two daemons' attach supervisors to
unlink and rebind each other's live socket every 30s (mg-d216).

A root too deep to fit a unix socket path keeps the isolation but not the
location: its sockets land in `$TMPDIR/pogo-agents/<hash of the root>`, still one
directory per root, and pogod logs a line saying so at startup. Those leaves
share the one `pogo-agents` directory and each records its `POGO_HOME`, so a
starting pogod can remove the ones whose root has been deleted; before mg-a997
they sat directly in `$TMPDIR` and nothing removed them at all. See
[docs/CONFIGURATION.md](CONFIGURATION.md#state-directory-pogo_home-and-running-multiple-instances)
for the `sun_path` limit and the agent-name ceiling it implies.

**Two instances sharing a `POGO_HOME` share all of that state — by construction.**
Refinery counts, scheduler entries, registered agents, and mailboxes co-mingle
because they are the same files on disk, not because state leaks across a
boundary. If you see a second instance picking up the first's schedules or
refinery work, check whether both resolve to the same root before treating it as
a bug. To sandbox a daemon for verification without touching the live fleet, set
`POGO_HOME` (or `HOME`) to a scratch directory; see
[docs/CONFIGURATION.md](CONFIGURATION.md#state-directory-pogo_home-and-running-multiple-instances).

## See also

- `scripts/launchd/README.md` — install, uninstall, and plist contracts for both `com.pogo.daemon` and `com.pogo.recovery`. Operational commands (load/unload/kickstart/inspect logs) live there.
- mg-f5fc — the policy decision behind this three-tier model.
- mg-6749 — implementation of the `com.pogo.recovery` LaunchAgent.
