# `pgrep` cannot see pogod — and the empty match is not the harm

**Work item:** mg-cbee · **Date:** 2026-08-20 · **Status:** Cause established; hazard landed in the prompts and given an instrument

`pgrep -x pogod` and `pgrep -f pogod` both return **empty at exit 1** on this box
while pogod is serving on port 10000 and `lsof`/`ps` find it without difficulty.
mg-cbee filed that as an observation and explicitly declined to claim a cause.

The cause is `man pgrep`, it is not specific to pogod, and it had already been
measured once on this fleet (mg-ce2c) — but only ever written into the *kill*
path, in two prompts a polecat does not read. This report establishes the
mechanism from scratch, quantifies it, and records why the second half of the
ticket — the `cmd $(pgrep ...)` construction — is the part that actually cost
something.

## 1. The mechanism: pgrep excludes the caller's ancestors, and pogod is always an ancestor

From `man pgrep` on this host:

```
-a    Include process ancestors in the match list.  By default, the
      current pgrep or pkill process and all of its ancestors are
      excluded (unless -v is used).
```

pogod spawns every agent, so pogod is the ancestor of every shell any agent
runs. Measured from a polecat shell, 2026-08-20 03:5x–04:0xZ:

```
$ p=$$; while [ "$p" -ne 0 ]; do ps -p "$p" -o pid=,ppid=,comm=; p=$(ps -p "$p" -o ppid=); done
22135 69347 /bin/zsh
69347 11579 claude
11579     1 /Users/daniel/go/bin/pogod
    1     0 /sbin/launchd
```

(The leaf pid varies — each Bash tool call is a fresh shell. Everything above it
is stable: `claude` 69347 is this agent's harness, `pogod` 11579 the daemon that
spawned it.)

| command | result | exit |
|---|---|---|
| `pgrep -x pogod` | *(empty)* | 1 |
| `pgrep -f pogod` | *(empty)* | 1 |
| `pgrep pogod` | *(empty)* | 1 |
| `pgrep -ax pogod` | `11579` | 0 |
| `lsof -iTCP:10000 -sTCP:LISTEN -n -P` | `pogod 11579` | 0 |

So it is **not** a `-x` vs `-f` matter, which is what the ticket's own framing
correctly suspected and could not rule on: no pattern works, because the
process is filtered out before the pattern is applied.

**The exclusion set, enumerated rather than argued.** `pgrep -a .` returned 717
rows, `pgrep .` returned 712 — a difference of 5. The four still resolvable when
`ps` ran a moment later are exactly the ancestor chain above:

```
    1 /sbin/launchd
11579 /Users/daniel/go/bin/pogod
22135 /bin/zsh
69347 claude
```

(The fifth is the `pgrep` process itself, gone by the time `ps` was asked. Two
separate `pgrep` invocations have two different pids, so that one row is noise;
the four that matter are not.)

**This is not about pogod.** The same exclusion was measured against three other
targets from the same shell:

- `pgrep -x claude` omitted `69347`, this shell's own parent, while listing the
  seven other `claude` processes on the box; `pgrep -ax claude` listed all eight.
- `pgrep -x zsh` omitted this shell's own pid; `pgrep -ax zsh` included it.
- `pgrep -x launchd` returns **empty at exit 1 from anywhere on the machine** —
  pid 1 is an ancestor of every process, so no caller can ever see it.

Everything about the reported behaviour is documented, deterministic, and
reproducible on any macOS box. Nothing is wrong with pogod, and nothing is wrong
with this box.

## 2. The harm is the construction, not the empty result

An empty result at exit 1 is honest. What is not honest is what an empty
**command substitution** does to the command built around it:

```
$ ps eww $(pgrep -x pogod | head -1)
  PID   TT  STAT      TIME COMMAND
 2205 s000  S+     0:00.24 -/bin/zsh
72299 s001  S+    34:22.41 pogo status --assignee=human --live MANPATH=/Users/daniel/...
13153 s006  Ss+    0:31.59 claude --dangerously-skip-permissions --append-system-prompt-file ...
... 21 rows ...
exit=0
```

With no pid argument `ps eww` describes **the caller's own processes, with their
environments attached, and exits 0**. `head -1` does not help: it makes the
substitution empty rather than the pipeline fail, and the `| head` also discards
pgrep's exit status. Nothing in the shape of that output says the question was
never asked.

The same degradation was measured with a narrower `ps` invocation — `ps -o
pid=,comm= $(pgrep -x pogod)` printed the caller's shells at exit 0 — so this is
a property of the construction, not of `ps eww`'s formatting.

**The near-miss.** On the night of 2026-08-20 an architect polecat used exactly
this construction for its first daemon-environment read during a deploy
verification. Its own harness shell carried a `POGO_HOME=/var/folders/.../tmp.*/home/.pogo`
left by a test harness, so the output "looked exactly like a live daemon
misconfigured into a temp dir". It caught itself and reported the trap instead of
the finding. Had it not, the artifact would have been a confident,
well-evidenced, entirely false finding about daemon configuration — filed the
same night pogod's `POGO_HOME` was independently confirmed **correct**
(`/Users/daniel/.pogo`, on both the plist and the running pid).

That is the whole reason this rates a change rather than a note: the wrong answer
is not merely wrong, it is wrong in the direction of an alarming and plausible
incident.

## 3. Prior art, and why it kept being rediscovered

The ancestor exclusion was measured on this fleet before, under mg-ce2c, and
written into `prompts/mayor.md` and `prompts/crew/doctor.md`. Both carry it
correctly. But it lives inside the **unanchored-`pkill` bullet**, framed as a
reason a *kill* will not land, and the six `prompts/templates/polecat*.md`
templates — the prompts a fresh worker actually reads — carried only the short
form of that bullet, with no mention of `pgrep` as a liveness reading and no
mention of the substitution hazard at all.

The agent that hit this was a polecat. It was reading, not killing. Neither of
the two prompts that knew was in front of it.

The `$(...)`-degrades-to-no-argument half was, as far as this investigation
found, written down nowhere in the repo.

## 4. What was changed (mg-cbee)

1. **An instrument, so the question has a first-class answer.** `GET /health/full`
   and `GET /version` now report pogod's own pid, and `pogo server status` prints
   it:

   ```
   pogod:    ok  (mode=full, uptime=57m49s, pid=11579)
   ```

   It is served *by* pogod, so it cannot report a pid for a daemon that is not
   answering — `pogo server status` exits non-zero with "pogo server is not
   reachable" instead. That is the property the `pgrep` construction lacked.

2. **The hazard, in the prompts a fresh agent reads.** All six polecat templates,
   `mayor.md` and `crew/doctor.md` now carry both halves: an empty `pgrep pogod`
   is not evidence pogod is down, and `cmd $(pgrep ...)` silently becomes `cmd`.
   Pinned by `TestShippedPromptsWarnPgrepIsNotALivenessInstrument` in
   `internal/agent/prompt_test.go`, in the manner of the existing pkill and
   trapped-cleanup corpus assertions, so it cannot be dropped on a stray edit.

## 5. `-P` does not fail loudly — it silently undercounts

The empty result in §1 is at least conspicuous. `pgrep -P <pid>` — walk a
process's children — has the same exclusion applied to it and returns a **short
list that looks complete**. Measured from the same shell:

```
$ ps -ax -o pid=,ppid= | awk '$2==11579' | wc -l      ->  10   (pogod's children)
$ pgrep  -P 11579                                     ->   9   pids
$ pgrep -aP 11579                                     ->  10   pids
```

The one pid `pgrep -P` omits is `69347` — this shell's own `claude` ancestor, and
nothing else. So a caller asking "what are pogod's children" is answered with
every child **except the branch it is standing on**, at exit 0.

This is the shape already recorded under **mg-48d8** — "`pgrep -P <pogod-pid>`
returned **nothing** while pogod had 24 direct children, confirmed against
`ps -axo pid,ppid`" — where it was one of three reasons a proposed gate-liveness
discriminator was rejected (see also `internal/refinery/subtreegone_live_test.go`).
Same mechanism, seen once before at a subtree walk, cause not named and not
generalised.

**Where this is load-bearing in shipped code.** Two call sites were audited:

- `scripts/launchd/pogo-deploy.sh:1563` — `kill_tree()` recurses with
  `pgrep -P "$pid"`. Its own comment records that "the run deadline's watchdog is
  itself a descendant of the run it is killing", handled with `KILL_TREE_SKIP`.
  But the exclusion removes the *intermediate* nodes on the path from the walk's
  root down to the calling shell, so descendants hanging off those nodes are
  never visited — leaves-first, the property the function exists for, is not
  guaranteed on that branch. **Not measured against a live deploy here, and not
  fixed here** (out of mg-cbee's scope); filed as **mg-19e4**.
- `scripts/launchd/pogo-reclaim.sh:448` — `builds_in_flight()` does
  `pgrep -x go|compile|link` to defer a reclaim while something is building.
  A reclaim running as a descendant of a `go` process would be told nothing is
  building. Whether that arrangement can occur was not established; filed with
  the above as **mg-19e4**.

## 6. Not established

- **Linux.** Every measurement above is macOS (`Darwin 24.6.0`). The `-a`
  ancestor-exclusion flag is a BSD `pgrep` feature; `procps-ng`'s `pgrep` is a
  different implementation with a different `-a`. Nothing on Linux was run for
  this report, so treat §1 and §5 as macOS findings only. The §2 substitution
  hazard is POSIX shell behaviour and is portable.
- **Whether the two call sites in §5 actually misbehave in production.** Both are
  reported from source reading plus the §5 measurement of the mechanism; neither
  was reproduced in its own context.
