# Observability Report: Mayor/Human Workflow (mg-230c)

## Executive Summary

The pogo system has **good foundational observability** across the mayor/human workflow. The refinery, agent lifecycle, and inter-agent communication all produce structured output that can be inspected. However, there are gaps — particularly around **post-mortem debugging of polecat sessions** and **persistent output capture**.

---

## 1. Refinery Visibility

**Rating: Strong**

The refinery provides multiple layers of observability:

### What works well
- **`pogo status`** — unified dashboard showing queue depth, active merges, and history in one command
- **HTTP API** — `/refinery/queue`, `/refinery/history`, `/refinery/mr/{id}` endpoints give full programmatic access
- **Structured logs** — every pipeline step (fetch, rebase, quality gates, merge, push) is logged with MR ID and attempt number, prefixed with `refinery:` for easy filtering
- **Gate output capture** — full stdout/stderr from build.sh/test.sh stored in the MergeRequest's `GateOutput` field
- **Failure notifications** — refinery automatically mails the author and mayor when merges fail, including error details and gate output
- **Retry transparency** — up to 3 retry attempts are logged with attempt numbers

### Gaps
- **History is in-memory only** — refinery history resets when pogod restarts. After a restart, all record of recent merges is lost. This makes post-incident investigation fragile.
- **No merge timeline view** — you can see what's queued now and what completed recently, but there's no way to see "what happened to branch X over time" (queued → held → retried → merged).
- **Log location varies** — service mode logs to `~/.local/share/pogo/logs/pogo.err.log`, manual mode logs to terminal. Humans need to know which mode pogod is running in.

### Recommendations
1. **Persist refinery history to disk** — write completed MergeRequests to a JSONL file so history survives restarts.
2. **Add `pogo refinery log <mr-id>`** — a CLI command that shows the full lifecycle of a single merge request (timestamps, steps, retries, outcome).

---

## 2. Agent/Polecat Output

**Rating: Moderate**

### What works well
- **Live attachment** — `pogo agent attach <name>` connects a terminal to the polecat's PTY for real-time observation
- **Output snapshot** — `pogo agent output <name>` shows the last 4KB from a ring buffer, useful for quick checks
- **Agent listing** — `pogo agent list` shows all running agents with PID, uptime, and status
- **Nudge mechanism** — `pogo nudge <name> "message"` lets the mayor or human inject messages into a running polecat's PTY

### Gaps
- **Ring buffer is only 64KB** — for long-running polecats, early output is lost. If a polecat runs for hours, you can only see the tail end.
- **No persistent output after exit** — once a polecat process exits and its worktree is cleaned up, there is no transcript or log of what it did. The ring buffer is gone. The only evidence is the git commits it made.
- **No structured event stream** — output is raw PTY bytes. There's no way to search for "what tools did the polecat call" or "what errors did it encounter" without reading the full terminal output.
- **Worktree cleanup on exit** — the polecat's worktree at `~/.pogo/polecats/<name>` is automatically cleaned up when it exits. If the polecat failed partway through, there's no way to inspect its working state.

### Recommendations
1. **Persist polecat output to disk** — write PTY output to a log file at `~/.pogo/polecats/<name>/output.log` (or similar). Keep logs for completed polecats for a configurable retention period (e.g., 7 days).
2. **Increase ring buffer or make it configurable** — 64KB is tight for agents that produce verbose output. Consider 256KB or making it configurable.
3. **Add `pogo agent log <name>`** — a CLI command that reads the persisted output log, even after the polecat has exited.

---

## 3. Séance Tool Assessment

**Rating: Not yet needed, but worth considering**

### Current state
There is no séance tool in the codebase. The concept would be a post-mortem debugging tool for replaying or inspecting completed polecat sessions.

### Is it needed now?

**Not immediately**, but the need will grow. Currently:
- The macguffin event log (`~/.macguffin/log/`) captures state transitions (claim, done, merge events) in JSONL format
- Git history preserves what code changes were made
- Refinery history shows merge outcomes
- Inter-agent mail provides a communication trail

These pieces give a reasonable picture of *what happened*, but not *why* or *how*. For example:
- If a polecat produced a bad fix, you can see the diff but not the reasoning
- If a polecat got stuck, you can see it was reaped but not what confused it
- If quality gates failed on the first attempt but passed on retry, the intermediate state is lost

### When séance would become valuable
1. **When running many polecats concurrently** — debugging individual sessions becomes harder at scale
2. **When polecats fail in non-obvious ways** — "why did it do X instead of Y?"
3. **When optimizing polecat prompts** — understanding reasoning patterns requires session transcripts

### Recommendations
1. **Defer building a full séance tool** — the current system doesn't generate enough persistent data to make it useful yet.
2. **First, implement persistent output logging** (recommendation from Section 2). This creates the raw material a séance tool would need.
3. **Consider a lightweight `pogo agent replay <name>`** — if output logs are persisted, a simple replay command that pages through the log would cover 80% of post-mortem needs without building a full session reconstruction tool.
4. **Track session metadata** — when a polecat exits, record a summary (exit code, duration, work item ID, branch, commits made) to the macguffin event log. This creates a searchable index of sessions.

---

## Summary of Recommendations

| Priority | Recommendation | Effort |
|----------|---------------|--------|
| **High** | Persist polecat output to disk (survives exit) | Medium |
| **High** | Persist refinery history to disk (survives restart) | Low |
| **Medium** | Add `pogo agent log <name>` for post-exit output review | Low |
| **Medium** | Add `pogo refinery log <mr-id>` for merge lifecycle view | Low |
| **Medium** | Record polecat session metadata to event log on exit | Low |
| **Low** | Increase ring buffer size or make configurable | Trivial |
| **Low** | Defer séance tool; revisit when output logging is in place | None |

---

*Report generated by polecat mg-230c, 2026-03-23*
