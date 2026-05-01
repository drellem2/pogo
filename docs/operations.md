# Pogo Operations

Operational runbooks for managing a running pogo deployment. This document is the canonical reference for "I need to do something to pogod" — start here before reaching for `kill`.

## Pogod restart policy

`pogod` runs under launchd with `KeepAlive=true` (see `scripts/launchd/com.pogo.daemon.plist`). That means **any uncoordinated kill is a loop**: launchd relaunches the daemon within seconds, and if the caller then re-evaluates "pogod looks broken — kill it again," the system gets stuck in a kill→relaunch→kill cycle. The decision recorded in mg-f5fc is that callers (polecats, crew agents, humans at a terminal) follow a three-tier escalation. Try tier 1 first; only escalate when the situation matches the criteria below.

> **Critical invariant: never `kill -9 pogod`.**
> launchd's `KeepAlive=true` will relaunch it immediately, and a SIGKILL skips pogod's own shutdown logic (mail flush, lockfile release, child-process cleanup). Use tier 2 or tier 3.

### Tier 1 — Don't restart pogod (default)

Most "pogod is misbehaving" situations are better solved by **filing an mg or restarting a specific subcomponent**. A pogod restart is a heavy hammer: it interrupts every running polecat, drops in-flight refinery work back into the queue, and re-arms every cron and watcher from cold. Reach for it only when the lighter alternatives below don't apply.

**Symptoms that do NOT warrant a restart** (file an mg or fix in place):

- A single handler returns wrong results or panics. → File an mg with the panic trace; the bug fix lands without restarting pogod.
- A plugin behaves stalely after you edited its source. → Most plugins reload on file change; if not, fix the plugin's reload path rather than bouncing the daemon.
- A polecat hangs or misbehaves. → Stop that polecat (`pogo agent stop <name>`); pogod itself is fine.
- Refinery is slow or backed up. → Inspect with `pogo refinery list`; queue throughput is not a daemon-restart problem.
- Logs look noisy. → Filter or rotate `~/Library/Logs/pogo/pogod.log`.
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

When tier 3 itself fails — recovery agent not installed, queue dir unwritable, kickstart returning non-zero — the failed `.req` files land in `~/.pogo/recovery/failed/`. Inspect that directory and `~/Library/Logs/pogo/recovery.log` before filing the mg; the log line `kickstart failed (rc=...)` is the most actionable signal.

## See also

- `scripts/launchd/README.md` — install, uninstall, and plist contracts for both `com.pogo.daemon` and `com.pogo.recovery`. Operational commands (load/unload/kickstart/inspect logs) live there.
- mg-f5fc — the policy decision behind this three-tier model.
- mg-6749 — implementation of the `com.pogo.recovery` LaunchAgent.
