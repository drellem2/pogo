# mg-60ca: May 2 morning auto_start gap — investigation

Investigation into the 55-minute gap on 2026-05-02 between pogod restart
and pm-pogo resuming work, filed against the sleep-resilience design
(mg-c4a3, ticket #4 of 6). Timestamps below are UTC; pogod.log lines are
BST (UTC+1) per the source file.

## Conclusion

**Explanation (b) — pm-pogo was already running but its Claude session
was wedged.** AutoStartAgents (`internal/agent/autostart.go`) fired
correctly. The agent process was alive throughout the gap; the Claude
conversation inside it stopped producing tool-use events and never set
up the mail-check loop / sweep crons it was nudged to set up.

No code change required. AutoStartAgents has no bug. The independent
heartbeat / stalled-agent detection track (mg-283e) is the appropriate
mitigation — the mayor only restarted pm-pogo at 08:46Z because Daniel
sent a reminder at 08:41Z, not because pogod or mayor noticed the
silence themselves.

## Evidence

### pogod restarted at 07:53:53Z (kickstart from recovery agent)

`~/Library/Logs/pogo/recovery.log` line 26-29:

```
[2026-05-02T07:53:53Z] draining 1 request(s)
[2026-05-02T07:53:53Z]   request: 20260502T075352Z-unknown-5b1862b4.req: requester=unknown;reason=install verification (mg-55d1);ts=2026-05-02T07:53:52Z
[2026-05-02T07:53:53Z] launchctl kickstart -k gui/501/com.pogo.daemon
[2026-05-02T07:53:53Z] kickstart succeeded; 1 request(s) moved to processed/
```

So pogod *did* fully restart — this rules out explanation (a).

### AutoStartAgents fired and started pm-pogo

`~/Library/Logs/pogo/pogod.log` (BST timestamps):

```
2026/05/02 08:53:53 agent pm-pogo: spawned pid=77268 type=crew proc=pogo-crew-pm-pogo
2026/05/02 08:53:53 autostart: started pm-pogo (pid=77268)
```

`~/.pogo/events.log`:

```
2026-05-02T07:53:53.466156Z agent_spawned crew-pm-pogo pid=77268
                            prompt_file=/Users/daniel/.pogo/agents/pm-pogo/synthesized-prompt.md
2026-05-02T07:53:57.516724Z nudge_sent  to=crew-pm-pogo
                            "You are now running. Set up your mail-check loop and your two sweep crons..."
```

So AutoStartAgents successfully (a) found pm-pogo's prompt, (b) parsed
its frontmatter (`auto_start = true`), and (c) spawned the process — this
rules out explanation (c).

### The Claude session was active for 38 min then went silent

Session transcript `~/.claude/projects/-Users-daniel--pogo-agents-pm-pogo/9e090325-0d38-4cf8-a2c2-3b1cda22a2b3.jsonl`:

- First entry: `2026-05-02T07:53:57.545Z` (matches the nudge time)
- Last entry:  `2026-05-02T08:32:51.835Z` (a `ToolSearch` tool result)
- 74 events spanning ~38 min, then total silence

The session was loading deferred tools via `ToolSearch` (matches for
`TaskCreate`, `TaskUpdate`, `TaskList`) right before going dark. It
never reached the "set up mail-check loop and sweep crons" instruction
the nudge gave it.

### Mayor unblocked it at 08:46Z, ~5 min after Daniel's reminder

```
2026-05-02T08:41:42.293635Z nudge_sent  to=crew-pm-pogo
                            "Daniel sent reminder: triage mg-4275 (high)... You were stalled 1h."
2026-05-02T08:46:46.538203Z agent_stopped crew-pm-pogo pid=77268
                            duration_seconds=6772.95 reason=requested
2026-05-02T08:46:48.617552Z agent_spawned crew-pm-pogo pid=75303
2026-05-02T08:46:51.672064Z nudge_sent  to=crew-pm-pogo
                            "You are now running. Set up your mail-check loop..."
```

Daniel's reminder did not itself unstick the wedged session — it took
the mayor's subsequent kill/respawn at 08:46:46Z to do that. Replacement
PID 75303 is the one that began the sweep loop ("pm-pogo up 09:48Z" in
the ticket title, BST = 08:48Z UTC, matches the post-restart nudge at
08:46:51Z plus a couple of minutes of Claude warm-up).

## What this confirms about auto_start

- The TOML/frontmatter `auto_start = true` path works as documented in
  `internal/agent/autostart.go:46-133`.
- The process-level guarantee (pogod restart → flagged agents respawned)
  held in this incident.
- The gap is downstream of pogod: a Claude session can hang while the
  hosting process appears healthy. pogod has no liveness check on the
  Claude conversation itself.

This is the situation mg-283e (pogod heartbeat goroutine) and mg-bcfa
(pogod-native scheduler) are designed to address. Out of scope here per
the architect's split.
