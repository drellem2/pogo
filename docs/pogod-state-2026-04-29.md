# pogod state report — 2026-04-29

**Author:** architect (mg-5305)
**Mission:** "pogo is not relaunching...at least quickly...when stopped" (Daniel, 2026-04-29) and "why is this necessary to fix pogo-reminders" (Daniel follow-up).

## TL;DR

- **pogod is currently running outside launchd** (PID 56323, started ~22:18Z today, PPID=1 but **not** registered in `launchctl list`). KeepAlive does not apply; nothing brings pogod back if it's stopped.
- **Root cause is real coupling, not interpretive:** `pogo-reminders/install.sh` lines 152-160 unconditionally `launchctl unload com.pogo.daemon.plist` and `pogo server stop --all` on every invocation. The refinery runs `install.sh --yes --skip-pmset` as the deploy command for every pogo-reminders merge (`refinery_deploy_attempted` events 2026-04-29 21:59 and 22:18Z for mg-8419). Each successful mg-8419 deploy un-supervises pogod; the reload that follows in install.sh is fragile (no kickstart, no health verification) and races crew-respawn.
- **pogo's own code is sound.** `pogo service install` does the right things (quiesce → unload → stop → drain → write → load → kickstart → verify). The on-disk plist matches what the code generates. mg-9aa0 (log path fix) is applied. The bug is not in pogo; it's in pogo-reminders' installer touching pogod.
- **Daniel's Q1 — why fixing pogo-reminders entangles pogod:** because pogo-reminders' install script *is* a pogod lifecycle manipulator. Polecats working on mg-8419 don't kill pogod; the deploy step that runs after their merge does. Polecats observing pogod going down repeatedly during their work may then reason their way into "I should restart pogod" — but the killer is the sibling installer, not the polecat.

## A — Observed state

### A.1 What's loaded in launchd

```
$ launchctl list | grep -i pogo
(empty)
$ launchctl print gui/501/com.pogo.daemon
Bad request.
Could not find service "com.pogo.daemon" in domain for user gui: 501
```

**No pogo launchd jobs are loaded.** None — not com.pogo.daemon, not com.pogo.notify, not com.pogo.reminders, not com.pogo.watchdog. KeepAlive cannot restart what launchd is not supervising.

### A.2 What's running

```
$ pgrep -laf pogod
56323 pogod        (PPID=1, etime ~6:47, started ~22:18Z)
```

Single instance, no zombies, no kill loop. PPID=1 because launchd is PID 1 on macOS — every daemonized process inherits PPID=1, regardless of whether launchd actually supervises it. (Per pogo's own internal/service/service.go:307, "PPID is not a reliable orphan signal on macOS.")

The running pogod is `/Users/daniel/go/bin/pogod` (mtime Apr 29 22:47) — today's rebuild. It is healthy: events.log shows it spawning polecats and nudging crew agents through 22:24Z. It is functioning as the daemon; it is just **not under launchctl supervision**.

### A.3 Plists on disk

```
~/Library/LaunchAgents/com.pogo.daemon.plist        Apr 28 12:13   (untouched today)
~/Library/LaunchAgents/com.pogo.notify.plist        Apr 29 23:18   (re-written by pogo-reminders install today)
~/Library/LaunchAgents/com.pogo.reminders.plist     Apr 29 23:18   (re-written by pogo-reminders install today)
~/Library/LaunchAgents/com.pogo.watchdog.plist      Apr 29 23:18   (re-written by pogo-reminders install today)
~/Library/LaunchAgents/com.pogo.recovery.plist      not present    (mg-f5fc tier 3 not yet built)
```

The daemon plist is correct: KeepAlive=true, RunAtLoad=true, ProcessType=Interactive, log path `~/Library/Logs/pogo/pogod.log` (post-mg-9aa0 location, not the legacy `/Users/daniel/log` collision). It is byte-identical to what `pogo service install` would generate today. The only thing wrong is that **launchd doesn't know about it** — the file exists, but it's not loaded.

### A.4 Logs

`~/Library/Logs/pogo/pogod.log` (940KB, 6347 lines):
- First entry: 2026-04-28 21:38:57 — "Max file watchers set to 4096", "pogod listening on 127.0.0.1:10000". A pogod started under launchd yesterday evening.
- Last entry: 2026-04-29 22:54:09Z — mid-merge (mg-8419 refinery merge into pogo-reminders). Log just stops; **no SIGTERM/SIGKILL/shutting down/panic markers**. pogod was killed mid-output. Pogod has no `signal.Notify` handler in cmd/pogod or internal/server, so abrupt kills produce no shutdown trace by design.
- Current pogod (PID 56323, started ~22:18Z) is **not writing to this log**. Because it's not under launchd, the plist's StandardOutPath redirect doesn't apply. `lsof -p 56323` shows no log file open for write — its stdout/stderr go wherever its parent redirected them, likely `/dev/null` or a closed handle.

`~/.pogo/reminders/notify.log` (last activity 2026-04-28 11:18Z) gives the backstory:
```
10:32 [mayor] dispatching pogo service install (mg-9cdc) — pogod restart imminent
10:40 [mayor] install (mg-9cdc) failed: setsid not on macOS — options
10:47 [mayor] retrying install with portable recipe; filed MARK_COMPLETE fix
10:58 [mayor] install failed: /Users/daniel/log is a stray file blocking install — your call
11:07 re: B option — already in flight (mg-9aa0)
11:18 install failed; pogod running outside launchd
```

That state — "pogod running outside launchd" — was already known on 2026-04-28 11:18Z and is exactly the state I observe now, 35 hours later.

`~/.pogo/reminders/poller.log` last entry: 2026-04-28 12:53Z. The reminders poller has been silent for ~33 hours.

### A.5 Stop/restart timing (A.7) — NOT EXECUTED

Section A.7 of the ticket asks me to `launchctl stop com.pogo.daemon` and time the restart. I deliberately did not run this. The label is not loaded, so `launchctl stop` would fail with "Could not find specified service" and tell us nothing. Stopping the running pogod via `pogo server stop` would just kill it without bringing it back (since launchd isn't supervising), interrupting the polecats currently mid-flight and leaving Daniel's box in a worse state. The ticket's hypothesis ("slow to relaunch") is fully explained by the loaded-state evidence above — no restart timing is needed to confirm it.

## B — What pogo supports (code intent)

### B.1 The plist template (internal/service/service.go:26-58)

Authored per mg-1416 spec:
- `KeepAlive=true` (unconditional) — auto-restart on any exit
- `RunAtLoad=true` — start when launchd loads the plist
- `ProcessType=Interactive` — opt out of App Nap throttling (refinery polls + agent idle detection need responsive scheduling)
- `StandardOutPath=$LOG_DIR/pogod.log`, `StandardErrorPath=$LOG_DIR/pogod.log`
- Explicit PATH so spawned crew agents resolve `claude`, `git`, `mg`, `pogo`
- `POGO_HOME`, `HOME`, `POGO_PLUGIN_PATH` so the daemon resolves the right state dir under launchd's minimal env

`logDir()` returns `~/Library/Logs/pogo` — Apple-standard, set by mg-9aa0. The on-disk plist confirms this is being used.

### B.2 The orchestrated install sequence (internal/service/service.go:359-471)

Seven-step orchestration designed against the crew/launchd race (architect's analysis 2026-04-28T11:37Z, mg-ae84):
1. Quiesce crew (`client.StopOrchestration()`) so crew agents can't auto-respawn pogod via `client.RunWithHealthCheck` between unload and load
2. Unload prior plist (best-effort; subsumes mg-6095 idempotency)
3. Stop any running pogod (`client.StopServer`)
4. Wait for `127.0.0.1:10000` to drain (10s timeout) — fail fast if a stranger holds the port
5. Write plist if changed; `launchctl load <path>`
5b. `launchctl kickstart -k gui/$UID/com.pogo.daemon` — forces the spawn out of "pended nondemand spawn = speculative" state (mg-3963 repro). Without this step, modern macOS leaves the load in deferred mode and `runs = 0`.
6. Verify launchd-pogod is bound and answering `/health` within 15s
7. Crew agents auto-restart under the new pogod via `auto_start=true` in their prompt frontmatter

There's also a **fast-path skip** (canSkipInstall, service.go:311) that knows about the orphan case (mg-2c55, mg-df4a): plist registered but launchd has never spawned the daemon (no PID line in `launchctl list`). The fast-path refuses to no-op in that case, even if `/health` answers — because an orphan pogod started outside launchd by crew-respawn answers `/health` AND keeps the plist registered AND has `runs=0`.

### B.3 Restart path (internal/service/service.go:674-690)

`service.Restart()` already implements mg-f5fc's tier 2 policy:
```go
exec.Command("launchctl", "kickstart", "-k", "gui/$UID/com.pogo.daemon")
```
with a fallback to `unload` + `load` for older macOS. So mg-f5fc tier 2 ("polecats SHOULD prefer launchctl kickstart over kill") is **already wired** — `pogo service restart` is the right wrapper for any agent that needs pogod to bounce.

### B.4 Pogod's own signal handling

No `signal.Notify`, no graceful shutdown handler in cmd/pogod or internal/server. Pogod relies entirely on launchd to manage its lifecycle. This is fine when launchd is supervising (KeepAlive does the right thing on any exit), but means a kill outside launchd produces no shutdown trace — which matches the abrupt log-stop at 22:54Z.

### B.5 What is *not* implemented

- `com.pogo.recovery` LaunchAgent (mg-f5fc tier 3) — not present on disk, no template in service.go, no `pogo recovery` subcommand. This is expected; mg-f5fc is still in design.
- No subcommand for "request restart by external agent" — i.e. there's no current way for a polecat that *should* trigger a restart but is itself managed by pogod to escalate the request out-of-band. mg-f5fc tier 3 is meant to fill this gap.

## C — Gap (observed ≠ supported)

| Aspect | Code intent (B) | Observed state (A) | Gap |
|---|---|---|---|
| pogod under launchd supervision | yes, via `pogo service install` | no — `launchctl list` shows nothing for com.pogo.daemon | **GAP** |
| Plist on disk | byte-identical to template | byte-identical (post-mg-9aa0) | OK |
| Log path | `~/Library/Logs/pogo/pogod.log` | matches | OK |
| KeepAlive=true | enabled when loaded | irrelevant — not loaded | inert |
| Pollers loaded (notify, reminders, watchdog) | expected loaded after install | none loaded | **GAP** |
| Pogod responsive on :10000 | yes | yes (PID 56323 healthy) | OK (but unsupervised) |

**The single primary gap:** none of pogo's launchd jobs are loaded, despite all four plists being on disk. Pogod is running, but as a manual orphan inheriting PPID=1.

## D — pogo-reminders entanglement (Daniel's Q1)

> "i also don't understand why this is necessary to fix pogo-reminders, which is something else the architect should answer"

**Answer: it's real, mechanical coupling — not interpretive — and it's in pogo-reminders, not pogo.**

`/Users/daniel/dev/pogo-reminders/install.sh` lines 152-160:

```sh
# --- 5. Stop any existing services (idempotent) ---
launchctl unload "$LAUNCH_AGENTS/com.pogo.daemon.plist" 2>/dev/null || true
launchctl unload "$LAUNCH_AGENTS/com.pogo.reminders.plist" 2>/dev/null || true
launchctl unload "$LAUNCH_AGENTS/com.pogo.notify.plist" 2>/dev/null || true

# --- 6. Stop manually-running pogod ---
pogo server stop --all 2>/dev/null || true
```

These four commands run **unconditionally** on every install.sh invocation. Then lines 167-175 *conditionally* reload the three plists if their files exist in `$LAUNCH_AGENTS`. The reload uses bare `launchctl load` — no `kickstart -k`, no port drain wait, no `verifyLaunchdRunning` healthcheck — i.e. exactly the failure modes pogo's own install code (mg-3963, mg-9cdc race, mg-2c55 orphan) was designed to avoid.

**The deploy chain making this fire repeatedly:** pogo-reminders' MR template runs `bash install.sh --yes --skip-pmset` as the deploy step. Every refinery merge of a pogo-reminders branch invokes install.sh. Today (events.log):
- 21:59:33Z — `refinery_deploy_attempted` mg-8419, command `bash install.sh --yes --skip-pmset`
- 22:18:51Z — same, second attempt mg-8419

Each invocation:
1. Unloads com.pogo.daemon (line 152)
2. Calls `pogo server stop --all` (line 160) — kills pogod via HTTP
3. Conditionally reloads (line 168) — fragile; no kickstart, no health verify

If reload races with crew-respawn, or fails silently, pogod ends up either un-supervised or in the orphan/never-spawned state. The pogod log goes silent (StandardOutPath redirect no longer applies). Polecats observing pogod-flapping during their work conclude pogod is broken and reach for SIGKILL — which **is** the kill loop pm-pogo described, but the *trigger* is upstream of the polecat.

**This answers the original entanglement question:** fixing pogo-reminders is not architecturally entangled with pogod restart. The entanglement is operational, lives in `pogo-reminders/install.sh`, and is fixable in pogo-reminders without touching pogo. pm-pogo's hypothesis ("polecats SIGKILL pogod") had the right symptom but the wrong actor — the killer is install.sh running on the deploy side, not the polecat doing the work.

This finding also re-confirms what mg-73a5 asserts on the forward direction ("there should be NO coupling"): the same principle applies in reverse. pogo-reminders' installer should not manipulate pogod's launchctl entries at all.

## E — Recommended next steps

Diagnose-only ticket; not fixing anything here. One mg per gap, in priority order:

### E.1 [HIGH] Re-supervise pogod now (one-shot, manual)

`pogo service install` is the canonical fix and it's idempotent (canSkipInstall). With Daniel's permission, mayor can dispatch a fresh `pogo service install` on the local box. The seven-step orchestration handles the current state correctly: it will unload (no-op, already unloaded), stop the orphan PID 56323, wait for port drain, write the plist (no-op, already correct), load, kickstart, verify health. Crew agents auto-respawn via `auto_start=true`.

Existing related ticket: mg-9cdc was archived after the mg-9aa0 log-path fix, but a retry pass under the post-fix code may not have actually run. Verify with `pgrep` + `launchctl list | grep pogo` after dispatch — both should return entries.

### E.2 [HIGH] Strip pogod lifecycle out of pogo-reminders/install.sh

`pogo-reminders/install.sh` should not touch com.pogo.daemon at all. Specifically:
- Remove line 152 (`launchctl unload com.pogo.daemon.plist`) and lines 167-169 (`if -f com.pogo.daemon.plist; launchctl load`) entirely
- Remove line 160 (`pogo server stop --all`) — pogo-reminders has no business stopping pogod
- Remove line 213 (`com.pogo.daemon → pogod (persistent)`) from the install summary
- Remove from `uninstall.sh` lines 33 and 41 (also unconditionally `rm -f com.pogo.daemon.plist`)

This is the architectural fix and pairs cleanly with mg-73a5 (which removes the *forward* coupling). File as a new mg in pogo-reminders. (Note: mg-8419 is human-gated; this can be a separate non-gated ticket since it's a small surgical change to install/uninstall, not feature work.)

### E.3 [MED] Document the three-tier restart policy (mg-f5fc next-step)

Already in mg-f5fc's checklist ("Document the three-tier policy in pogo/docs/operations.md"). Combined with E.2, this also closes the loop on "polecats know not to kill pogod, and *also* installers know not to unload pogod's plist". Both kinds of agents need the policy.

### E.4 [LOW] Detect "pogod running but unsupervised" in `pogo doctor`

`pogo doctor` (cmd/pogo/main.go) already inspects launchctl status. Add a check that fails (not warns) when pogod is reachable on :10000 AND `launchctl list com.pogo.daemon` shows nothing — i.e. the orphan case. Useful early-warning for next time pogo-reminders or any other sibling tool unloads the plist. Roughly 15 lines.

### E.5 [LOW] Add a no-fallback handler in pogod for clean shutdown traces

When pogod is killed outside launchd's supervision, the log just stops mid-line. A minimal `signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)` + log-flush handler would leave a `"received SIGTERM, shutting down"` breadcrumb that future investigations could grep for. Not load-bearing — just makes diagnostics easier next time. ~20 lines.

## Open questions for Daniel

- **Are you OK with mg-f5fc's tier 3 (com.pogo.recovery LaunchAgent) coming under pogo's `pogo service install` umbrella, not pogo-reminders?** My read is yes — pogo's recovery components belong in pogo, full stop, with no pogo-reminders involvement. If you confirm I'll spell it out in the f5fc design when mayor dispatches.
- **Should E.2 (strip pogod lifecycle from pogo-reminders/install.sh) get the human-gate treatment alongside mg-8419, or can it ship independently?** It's surgical and the right architectural fix; I don't see why it needs the gate.
