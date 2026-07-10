# Pogo Operations

Operational runbooks for managing a running pogo deployment. This document is the canonical reference for "I need to do something to pogod" — start here before reaching for `kill`.

## Recovering a single wedged agent

An agent whose process has died — a crew loop that exited cleanly without re-arming, a crash whose respawn failed — leaves a stale entry in pogod's in-memory registry (`pogo agent list` shows `status=exited` with the old pid). Recovering it does **not** require a daemon restart; both recovery verbs handle a dead-process entry directly (gh #19):

- `pogo agent stop <name>` — when the registered process is already dead, stop clears the stale entry and returns success (idempotent). Use this to unwedge an agent you don't want to immediately restart.
- `pogo agent start <name>` — overwrites a dead-process registration rather than refusing with `already running`, so start "just works" against a stale entry and brings the agent back in one step.

A **live** agent is still protected: `start` refuses a duplicate of a running process, and `stop` signals it normally. So there is no need to `systemctl restart pogo.service` / bounce launchd to recover one wedged agent — that bounces the whole fleet. Reserve the daemon restart tiers below for pogod itself misbehaving.

## Recovering from a usage-limit episode

When the provider's usage limit is hit, Claude Code renders a rate-limit-options modal ("Stop and wait for limit to reset") and the agent's reasoning loop wedges — it stops producing events but stays alive. pogod's modal watcher (gh #45) detects this and surfaces it so you aren't left guessing why a fleet went quiet. **Do not restart or `kill` a rate-limited agent to "fix" the wedge** — it is not broken; it is waiting for the limit to reset, and a restart just loses in-flight context. Recovery is: wait for the reset, then nudge or restart as needed.

### How you find out

- **One coalesced mail to `human` at the start of an episode** (subject `usage limit hit — fleet episode started`). An "episode" is one fleet-wide limit event: the first affected agent triggers the mail; additional agents that hit the same limit join the episode **silently** (no per-agent mail storm). No action is required at hit time.
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
- `pogo agent list` shows parked agents with `status=parked`, so the coordinator's stall-watch can skip them mechanically. (The coordinator defaults to `ringmaster`; configurable via `[agents] coordinator`.)

Parking an agent that isn't currently running is valid (the flag still gates auto-start); `pogo agent start` refuses a parked agent and points at `wake`. Parking is for crew agents — polecats are ephemeral and are simply stopped.

## Pogod restart policy

`pogod` runs under launchd with `KeepAlive=true` (see `scripts/launchd/com.pogo.daemon.plist`). That means **any uncoordinated kill is a loop**: launchd relaunches the daemon within seconds, and if the caller then re-evaluates "pogod looks broken — kill it again," the system gets stuck in a kill→relaunch→kill cycle. The decision recorded in mg-f5fc is that callers — polecats (disposable worker agents), crew agents, humans at a terminal — follow a three-tier escalation. Try tier 1 first; only escalate when the situation matches the criteria below.

> **Critical invariant: never `kill -9 pogod`.**
> launchd's `KeepAlive=true` will relaunch it immediately, and a SIGKILL skips pogod's own shutdown logic (mail flush, lockfile release, child-process cleanup). Use tier 2 or tier 3.

### Tier 1 — Don't restart pogod (default)

Most "pogod is misbehaving" situations are better solved by **filing an mg (a work item in macguffin, the task-store CLI) or restarting a specific subcomponent**. A pogod restart is a heavy hammer: it interrupts every running polecat, drops in-flight refinery (the merge queue) work back into the queue, and re-arms every cron and watcher from cold. Reach for it only when the lighter alternatives below don't apply.

**Symptoms that do NOT warrant a restart** (file an mg or fix in place):

- A single handler returns wrong results or panics. → File an mg with the panic trace; the bug fix lands without restarting pogod.
- A plugin behaves stalely after you edited its source. → Most plugins reload on file change; if not, fix the plugin's reload path rather than bouncing the daemon.
- A polecat hangs or misbehaves. → Stop that polecat (`pogo agent stop <name>`); pogod itself is fine.
- Refinery is slow or backed up. → Inspect with `pogo refinery list`; queue throughput is not a daemon-restart problem.
- Logs look noisy. → Filter `~/Library/Logs/pogo/pogod.log`. pogod appends across restarts (crash evidence survives) and rotates the file itself at startup once it exceeds 10 MiB — the prior chunk is `pogod.log.1` (up to `.3`). No manual rotation needed; never truncate the live file mid-run.
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

### Tier 3 — External recovery agent

For the case where tier 2 isn't reachable: pogod is wedged so badly the calling shell can't get a response, **or** the caller is itself a child of pogod (a polecat, a crew agent, a refinery worker) and cannot safely SIGTERM its own parent.

Signal a restart by enqueuing a request:

```bash
pogo recovery request --reason="<short explanation>"
```

This drops a `.req` file into `~/.pogo/recovery/queue/` and exits 0 immediately — it does **not** block on the restart. Within ~2s, the `com.pogo.recovery` LaunchAgent picks it up via `WatchPaths`, calls `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon`, and archives the request. See mg-6749 for the design and `scripts/launchd/README.md` for the full plist contract and operational commands.

**Why this is a separate launchd job, not a pogod feature:** the whole point of tier 3 is to recover when pogod is wedged. Any signal channel that depends on pogod (an HTTP endpoint, an mg tag, a daemon-served socket) defeats the purpose. The recovery agent uses only the kernel — filesystem writes, `launchctl`, `flock`-equivalent atomic mkdir — so a fully-wedged `pogod` cannot block its own recovery. A polecat dying alongside its parent can still `mv` a file before exiting.

**Critical invariants (do not break):**

- **Recovery is an independent install.** `pogo service install` does NOT install the recovery agent. Run `pogo service install-recovery` separately. Bundling them would mean a wedged daemon install blocks its own recovery — the very situation tier 3 exists to handle. The post-install hint from `pogo service install` reminds you of this; don't ignore it.
- **The recovery agent rate-limits to 60s.** Two `pogo recovery request` calls inside a 60-second window result in exactly one restart. The second request is not lost — it sits in the queue and a deferred tickle drains it after the floor elapses — but you cannot bounce pogod faster than once per minute. This is intentional: it bounds the worst-case impact of a polecat that mistakenly decides "restart pogod" is the right answer to every error.
- **The recovery script never `kill -9`s pogod.** It only ever calls `launchctl kickstart -k`. Tier 3 is for *controlled* restarts; the SIGKILL prohibition still applies.
- **One `pogod` ⇒ one restart per drained batch.** All `.req` files in the queue at trigger time are coalesced into a single kickstart call, then archived together. Don't expect "10 requests = 10 restarts."
- **The plist bakes absolute paths at install time.** `WatchPaths` and `POGO_RECOVERY_DIR` are rendered from `POGO_HOME` when you run `install-recovery`. Move `POGO_HOME` afterwards and the installed job keeps watching the old directory — silently, because `pogo service status` only checks that the plist *file exists*, not that its paths still resolve. **Re-run `pogo service install-recovery` after any `POGO_HOME` change**; `scripts/migrate-pogo-home.sh` does this for you.

When tier 3 itself fails — recovery agent not installed, queue dir unwritable, kickstart returning non-zero — the failed `.req` files land in `~/.pogo/recovery/failed/`. Inspect that directory and `~/Library/Logs/pogo/recovery.log` before filing the mg; the log line `kickstart failed (rc=...)` is the most actionable signal.

**Verifying tier 3 is actually armed.** An installed plist is not a working one, and `pogo service status` cannot tell the difference. Check two things with `launchctl print gui/$(id -u)/com.pogo.recovery`:

1. Its `WatchPaths` entry is the queue the CLI writes to — `~/.pogo/recovery/queue` under a default `POGO_HOME`.
2. The job actually *spawns* when that directory changes. Drop a file into the queue, then confirm `runs` increments and `~/Library/Logs/pogo/recovery.log` gains a line.

A job that stays at `runs = 0` while showing `pended nondemand spawn` is **not armed**: launchd is accepting the trigger and never dispatching it. No plist edit fixes that — the job only runs via an explicit `launchctl kickstart`, which defeats the purpose of tier 3. See mg-6e82.

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
location: its sockets land in `$TMPDIR/pogo-agents-<hash of the root>`, still one
directory per root, and pogod logs a line saying so at startup. See
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
