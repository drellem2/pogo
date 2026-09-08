# Site B alone: what SIGTERMs the refinery gate's test step (mg-baf3, 2026-09-08)

**Status: THE SENDER IS STILL NOT IDENTIFIED.** What this pass adds is a *filter* where
mg-cbc3 left a class that admits everything, and the discovery that the search which would
have found an off-repo sender **could not have found one** — the tool every agent on this box
reaches for returns a clean, exit-1 empty answer over `~/.pogo`.

Read `gate-sigterm-variable-elapsed-2026-09-07.md` first. Everything it measured stands and
is not re-derived here.

## 0. Scope: site B only

mg-f387 filed this as a two-site question. Site A — the nightly deploy's
`Terminated: 15` on `launchctl kickstart -k` — was **answered by mg-bead**: pogod 6610 was
started from an Emacs shell buffer, so launchd owns nothing, `kickstart -k` kills nothing and
then blocks. The `Terminated: 15` is a later kill landing on an already-hung command. The
two-site lead ("same signal, same box, one unknown sender") is therefore **refuted**, not
merely unsupported, and this document is about site B only.

Site B, unchanged: the gate's test step SIGTERMed three times — 2026-08-19 at 178s,
2026-09-07 at 85s and at 264s. The signal reached the leak guard, the budget shell and the
test process; `test.sh`, `build.sh` and the gate shell survived and ran their EXIT traps,
though `runGate` puts all six in one process group. Something addressed a **subtree**.

## 1. The instrument that answers "nothing on this box does that" without looking

**Measured 2026-09-08, same pattern, same root, two greps:**

```
grep       -rlI 'kill_tree' /Users/daniel/.pogo/   ->  0 files, exit 1
/usr/bin/grep -rlI 'kill_tree' /Users/daniel/.pogo/   -> 80 files, exit 0
```

**Positive control**, which is what makes the zero a defect rather than a fact about the tree
— point both at the subdirectory instead of the root and they agree:

```
grep       -rlI 'kill_tree' /Users/daniel/.pogo/bin/ -> 2 files
/usr/bin/grep -rlI 'kill_tree' /Users/daniel/.pogo/bin/ -> 2 files
```

So the pattern is right, the tree is readable, and the files are there. The cause is that
`grep` in an agent's shell is **not** `/usr/bin/grep`: it is a shell function installed by the
harness's shell snapshot (`~/.claude/shell-snapshots/…`) that execs `ugrep` with
`--ignore-files`, and `~/.pogo/.gitignore` is an **allowlist**: it opens with `/*` — ignore
everything — and then un-ignores a handful of vetted, secret-free config files. `git -C
~/.pogo check-ignore -v bin/pogo-deploy.sh` names the rule that hides the deployed runner:

```
.gitignore:19:/bin/*    bin/pogo-deploy.sh          (exit 0 — ignored)
projects.json                                       (exit 1 — the un-ignored control)
```

so the recursive walk skips `~/.pogo/bin/`; naming the directory explicitly overrides the
ignore, which is why the control in the previous block passes.

`type grep` reports the function, and `declare -f grep` prints it. Neither is something a
reader thinks to run when a search comes back empty, because an empty search **looks like an
answer**.

Why this belongs in *this* investigation rather than a tooling ticket: `~/.pogo` is where the
fleet's live, non-repo code lives — the deployed `pogo-deploy.sh` launchd actually execs, the
reminder pollers, the sleep/wake poller, `bridget-supervise`, every agent prompt. It is
exactly the space you sweep when the repo has no sender in it. **Every negative taken there
with the default `grep` is void**, including any this investigation's predecessors took. It
is the same shape as the `cmd $(pgrep …)` hazard in
`pgrep-cannot-see-pogod-2026-08-20.md`: an instrument that returns a well-formed answer to a
question it did not ask.

## 2. The stale-pid class has a FLOOR, and the floor is the victim's own age

mg-cbc3 measured this box recycling its whole pid space in minutes (292–469 pids/s across the
kill, one full wrap inside the 85s the killed run lasted) and drew the consequence:

> any construction that holds a pid and signals it later reproduces the recorded shape with
> no intent toward the gate

That is true, and as stated it **eliminates nothing**. Every watchdog in this tree captures a
pid and fires later. A reader handed this cannot test a candidate against it.

**It has a bound, and stating the bound needs no measurement at all.** A pid has one owner at
a time. A killer holding a *stale* pid captured that number while a **different** process
owned it, which is necessarily **before the current owner was born**. So

> **hold (capture → signal) ≥ age of the victim at the kill.**

Nothing about allocation policy is assumed, so it survives off darwin.

**On darwin it is strictly stronger.** Measured for this item: 40 consecutive spawns returned
13487…13526, `+1` each — pids are issued **sequentially** and a freed number is not reissued
until the counter wraps the whole ~99,999-pid space. So the hold must cover the victim's age
**plus one full wrap**. Allocation rate measured on the same box at 139 pids/s (1,390 pids in
10s), i.e. a wrap of ~719s at that load; mg-cbc3's 292–469/s gives 213–342s and its peak
reading a wrap of ~85s.

### What the floor excludes, and the one thing it does not

The victim at site B is the `tmpdir-leak-guard.sh` shell. It is started by the **first** gate
step, so its age at the kill is the run's elapsed less `build.sh`'s preamble: **≈80s / 173s /
259s** for the three occurrences. Applying `hold ≥ age`, using the weak form only:

| Construction | Hold (capture → signal) | Clears ≥80s? |
|---|---|---|
| `kill_tree`'s own per-node gap (`child_pids` → `kill -SIG`) | one `ps -ax` + `awk`, **measured 0.01s** on this box at 862 live processes | no |
| `internal/client.StopServer` — lockfile pid, then signal | same call | no |
| `sleep_darwin.go` `reapOrphanedWatchers` — `ps` row, then signal | same loop iteration | no |
| `run_bounded`'s watchdog racing `wait "$p"` in the normal path | the scheduling gap between `wait` returning and `kill_tree "$k" TERM` | no |
| `netc_probe` / `probe_tcp` killer | `$timeout`, default **5s** | no |
| `run_bounded` called with a literal bound in a test body | the bounds passed are `30`, `2`, `0` | no |
| `on_deadline`'s backstop (`kill_tree "$target" TERM`) | `DEADLINE_ALERT_BOUND`, default **120s** | only the 85s occurrence; **no** for 178s and 264s |
| **`run_bounded "$GIT_TIMEOUT"` reached from INSIDE a gate step** | **300s** (`POGO_DEPLOY_GIT_TIMEOUT`'s default) | **YES, all three** |
| `run_bounded "$GIT_TIMEOUT"` / the run-deadline watchdog during a real deploy | 300s and longer | yes, **but** mg-cbc3 ruled the deploy out on timing (no deploy activity in the 17:00–20:30Z band on either day) |

**The floor eliminates every row but one, and the surviving row is inside the gate.** That was
not the expected result and it is the reason this section was rewritten: the first draft of
this table asserted that "the largest `run_bounded` bound any gate step passes is 30s" and
concluded the whole class was excluded. That is false. `scripts/pogo-deploy_test.sh` is a gate
step, and it executes the deploy runner itself — **7 `bash "$RUNNER"` invocations, of which 5
do not override `POGO_DEPLOY_GIT_TIMEOUT` and therefore run at its 300s default** (lines 143,
362, 2126, 2143, 3362; the two that do override it are the `run_e2e` helper at 3110, whose
eight callers pass 0, 2 or 5, and the canary at 4129, which passes 5). Any git step those five
reach arms `run_bounded 300 git …`, whose watchdog payload is `kill_tree "$p" TERM` — a
**leaves-first SIGTERM subtree walk**, which is the recorded shape, with a hold that clears the
floor for all three occurrences.

### The candidate that survives, stated as a candidate

> An **orphaned** `run_bounded` watchdog, armed inside a gate run by a step that executes the
> deploy runner without overriding `GIT_TIMEOUT`, firing up to 300s later on a pid that has
> since been freed and recycled.

It fits every recorded fact — leaves-first SIGTERM subtree walk; a subtree and not the process
group; variable elapsed; no `events.log` row and no `pogo-deploy.log` row, because it is a
stray subshell belonging to nothing; and it explains how the killer could be *outside* the
victim's own run, since every polecat on this box runs `./build.sh` in its own worktree and so
arms the same watchdogs while the refinery gate is the longest-lived process tree on the
machine.

### One sample was taken, and it is half of what is needed

Sampled `ps -axo pid,ppid,command` every 2s across the second half of a real `./build.sh` on
this worktree, on 2026-09-08. Two things came back.

**Orphaned `sleep` processes at `ppid 1` during the run** — `sleep 5`, `sleep 30`, `sleep 60`
— alongside the box's known launchd pollers. Positive control armed first and seen by the same
sampler (a deliberately orphaned `sleep 25`, pid 19593, and later `sleep 20`, pid 41352), so
the sightings are not an artefact of the filter. **These are not the dangerous orphan.**
`run_bounded`'s watchdog is `( sleep N; … ) &`: the `sleep` is the watchdog's *child*, and
`kill_tree` kills leaves first, so a `sleep` surviving its subshell is the walk having worked.
An orphaned `sleep` holds no kill logic and simply expires.

**Orphaned SHELLS at `ppid 1`, from a gate step**, which is the shape that would matter:

```
ppid 1  bash …/tmp.aLLUvl5QlB/kill_tree_fixture.sh …/scripts/launchd/pogo-deploy.sh … kill_tree_pgrep 1
ppid 1  bash …/tmp.aLLUvl5QlB/kill_tree_fixture.sh …/scripts/launchd/pogo-deploy.sh … kill_tree 0
```

Those are `scripts/pogo-deploy_test.sh`'s own `kill_tree` fixtures, left running after the step
that made them. So **a gate step does orphan shells that outlive it** — the mechanism the
surviving candidate needs is not hypothetical on this box. What has *not* been shown is that
any such orphan is a `run_bounded` watchdog still holding a pid, which is the half that would
turn the candidate into a finding.

Stated because it changes what the sample proves: the shell-shaped positive control for this
second sampler did **not** fire. `bash -c 'sleep 18'` execs the `sleep` when `-c` carries a
single command, so the process is named `sleep`, not `bash -c`, and the filter could not match
it. The sampler is nonetheless demonstrated able to see `ppid 1` shells — it returned the two
real ones above — but that is a de-facto positive rather than the armed one, and the armed
control is the one that would have been checkable before the fact.

**What is NOT established, and it is the load-bearing half.** The 300s hold is only *realised*
if the watchdog is **orphaned** — in the normal path the parent kills it within milliseconds of
`wait "$p"` returning, and then the hold is the scheduling gap, which the floor excludes. This
pass did not establish that any gate step orphans one, only that the construction with a
qualifying hold exists inside the gate and that nothing else does. Two things would settle it,
neither of which is done here: whether those five invocations reach a git step at all (three of
them look like early-exit refusal paths and one is `--help`), and whether a `run_bounded`
watchdog is ever left with `ppid 1` during a gate run. The second is directly samplable —
`ps -axo pid,ppid,command | awk '$2==1 && /sleep/'` during a gate — and an orphan of exactly
that shape was confirmed visible to that sampler with a positive control before this was
written.

## 3. The sweep of the space section 1 made searchable

Redone with `/usr/bin/grep` over the live, non-repo tree. Everything that signals on this box,
outside the repo:

- `~/.pogo/bin/pogo-deploy.sh` (the deployed runner launchd execs) — carries `kill_tree`,
  `run_bounded`, `on_deadline`. Fires 03:00/04:00/05:00 local only (`StartCalendarInterval`,
  read from the installed plist), which is the timing exclusion above.
- `~/.pogo/bin/net-control.sh` — `( sleep "$timeout"; kill -9 "$p" ) &`, SIGKILL, 5s.
- `~/.pogo/pogo-reminders/bin/watchdog.sh` — `launchctl kickstart -k` against
  `com.pogo.notify`, `com.pogo.deadman`, `com.pogo.gh-issues` and **nothing else**; the job
  table is a literal in the script.
- `~/dev/bridget/bridget-supervise` — `kill -TERM` against pids it spawned itself
  (`$notify_pid`, `$child`, `$sleep_pid`), single pids, no tree walk.
- `~/.pogo/bin/pogo-recovery.sh` — no signal path.

`com.pogo.reclaim` — whose `builds_in_flight()` uses `pgrep -x go` and was flagged unverified
by mg-19e4 — **is not installed**: it exists in `scripts/launchd/` and has no plist in
`~/Library/LaunchAgents/`. It cannot be the sender because it does not run.

No userspace OOM daemon runs here. `gatesignal.go` carries "earlyoom and similar send SIGTERM
by default — if one runs here" as an off-darwin caveat; enumerated on 2026-09-08 against
`launchctl list`, the non-Apple job set contains no earlyoom/nohang/oomd-class process. That
caveat is correct to keep in the code (it is platform-general) and is answered for this box.

The repo still ships **exactly one** leaves-first SIGTERM subtree walk, `kill_tree`, which is
what mg-3bd1 recorded as a shape match and nothing more. That remains its status.

## 4. Shipped

`internal/refinery/gatesignal.go` now reports the stale-pid class as `BOUNDED` rather than
leaving it inside the catch-all `OPEN` line, states *why* the floor holds so a reader can
apply it to a candidate this repo has never heard of, and gives the run's elapsed as a
**ceiling** on the victim's age rather than as the floor. That last distinction is the whole
test: a process spawned late in a run is younger than the run, so quoting `Elapsed` as the
floor states a bound higher than the true one and discards a real candidate — the direction
that loses a sender.

## 5. Blind spots, stated

- **The floor is a necessary condition, not a sufficient one.** It excludes candidates; it
  identifies nobody. A construction that clears it is not thereby the sender.
- **The victim's age is derived, not read off a record.** It is the run's elapsed less
  `build.sh`'s preamble, and no occurrence recorded the guard's actual start time. If the
  guard were somehow much younger than the run, the floor drops. The `85s` occurrence is the
  one where this matters, because it is the only one `on_deadline`'s 120s backstop could
  reach.
- **The delivery shape is mg-3bd1's inference from output ordering**, confirmed by a four-arm
  control but not by a direct observation of three signals. This document inherits it.
- **Section 1's cause is diagnosed from `declare -f grep`**, not from ugrep's source. What is
  measured is the 0-vs-80 discrepancy and that naming the ignored directory removes it.
- **Section 2's exclusion table is a NEGATIVE over an enumeration, and the enumeration was
  built with the instrument section 1 condemns.** Checked rather than assumed: every count in
  it was re-taken with `/usr/bin/grep` and the two agree — one `kill_tree` definition against
  one, 20 Go signal-sending sites against 20, 14 and 13 `run_bounded` call sites against 14
  and 13 — and `git status --porcelain --ignored` shows the only ignored paths in this
  worktree are two `.claude` state files. The truncation is a property of `~/.pogo`'s
  allowlist `.gitignore`, not of every tree, and it did not reach the repo sweep. Had this
  gone unchecked the remedy would have exhibited the defect it documents.
- Nothing here was reproduced. The fault has not recurred since `scripts/signal-sender.sh`
  was wired on 2026-09-08 00:14Z, so the instrument that would name the sender has still
  never been present during an occurrence.
