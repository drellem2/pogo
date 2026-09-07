# Something SIGTERMs the merge gate's test step at variable elapsed times

**Work item:** mg-3bd1 · **Date:** 2026-09-07 · **Repo:** `/Users/daniel/dev/pogo`

Split out of mg-b1df, which owns the classification half. This item owns the
**cause**. The cause is **not established**. What follows is what is now
measured, what is now ruled out with the measurement that rules it out, and the
instrument that was shipped so the next occurrence carries the reading nobody
had for the last three.

---

## 1. It predates today, and nobody had looked

The ticket said so explicitly: *"Whether it predates today. **Nobody has
looked**."* It does.

Measured over the whole event log (`~/.pogo/events.log`,
2026-08-16T22:10:18Z .. 2026-09-07T21:20:24Z), which holds **13**
`refinery_merge_failed` rows against **75** `refinery_merged` rows:

| when | branch | elapsed at kill | recorded class |
|---|---|---|---|
| 2026-08-19T17:57:18Z | `polecat-pfbaf` | **2m58s (178s)** | `indeterminate` |
| 2026-09-07T20:08:02Z | `polecat-t2127` | **1m26s (85s)** | `defect` |
| 2026-09-07T20:14:04Z | `polecat-t2127` | **4m24s (264s)** | `defect` |

Three occurrences, **3 of 13 recorded gate failures**. The 2026-08-19 row is
new to this investigation; it was never connected to the 2026-09-07 pair
because it was filed under a different name — `gate "./build.sh" WAS KILLED BY
SIGTERM after 2m58s`, mg-0502's `gateSignalError`, class `indeterminate` — while
the September pair arrived as `quality gate: ./build.sh failed: exit status 1`,
class `defect`. **Same phenomenon, opposite classifications.** That is why the
absence of prior reports was not evidence of absence, and why "nobody has
looked" was the right thing for the ticket to say.

The August figure also widens the elapsed spread: **85s / 178s / 264s**, three
values from three runs. The ticket's finding — that this is not a fixed-length
watchdog — survives a third data point.

## 2. The elapsed time varies because ONE PACKAGE's runtime varies

The sharper statement, and it is the one the ticket did not have. In **all
three** runs `go test ./...` had printed results through
`internal/ackwatch` and no further:

```
?      github.com/drellem2/pogo/cmd/lsp        [no test files]
ok     github.com/drellem2/pogo/cmd/pogo       50.568s   (44.288s / 36.407s)
ok     github.com/drellem2/pogo/cmd/pogod      53.315s   (54.071s / 46.933s)
ok     github.com/drellem2/pogo/cmd/pose       (cached)
ok     github.com/drellem2/pogo/internal/absentwatch (cached)
ok     github.com/drellem2/pogo/internal/ackwatch    2.679s
<the kill>
```

`go test` prints package results in the order the packages were listed, and the
next name in `go list ./...` is **`internal/agent`** — the slowest package in
the tree (268.7s isolated at load ~5, measured in
`scripts/go-test-budget.sh`'s own header). So the run had not stopped
progressing; it was inside `internal/agent`, whose result had not yet been
printed and which therefore blocked every later package's line.

**The POSITION is invariant and the TIME is not.** That reframes the finding:
the elapsed time varies because `internal/agent`'s runtime varies with host
load, not because the signal arrives at a random moment. It does not identify
the sender, and it does not establish that `internal/agent` is the *reason* for
the signal rather than merely the place the clock happened to be — but it means
"variable elapsed time" and "unpredictable arrival" are not the same claim, and
only the first is measured.

## 3. The DELIVERY SHAPE: ancestors survived

This is the load-bearing new measurement, and it was recovered from the ORDER
of three lines of gate output.

The gate's nesting for that row is:

```
sh -c ./build.sh
  bash ./build.sh
    bash ./test.sh
      bash scripts/tmpdir-leak-guard.sh      <- pid 86690 on the 20:08 run
        bash scripts/go-test-budget.sh
          go test -timeout 20m ./...
```

The 2026-09-07 runs recorded, in this order:

```
Terminated: 15
rm: /tmp/pogo-gate-tmp.86690.cQw7MQ: Directory not empty
$TMPDIR LEAK: the run abandoned 1 entry ...
  (the wrapped command ALSO failed with status 143 ...)
```

`tmpdir-leak-guard.sh` runs its `cleanup` from a `trap ... EXIT INT TERM HUP`
and prints the leak report *before* exiting — so a cleanup `rm` appearing
**before** the leak report means the TERM trap fired, not the EXIT one. A
four-arm control was run on this host against a fixture with the same nesting
(`guard -> budget -> child`), signalling a different set each time:

| arm | signalled | observed ordering |
|---|---|---|
| A | the innermost child only | `script: line N: PID Terminated: 15   sleep 30` (**annotated form**), then BREAKDOWN, then LEAK, then CLEANUP |
| B | the middle shell only | bare `Terminated: 15`, then LEAK, then CLEANUP |
| C | **child + middle + guard** | bare `Terminated: 15`, then **CLEANUP**, then LEAK | 
| D | the guard only | BREAKDOWN, then CLEANUP, then LEAK — no `Terminated: 15` at all |

**Only arm C reproduces the recorded ordering**, and it also matches the two
details the other arms get wrong: the `Terminated: 15` line is *bare* (arm A
prints bash's annotated `script: line N: PID ...` form), and
`go-test-budget.sh`'s per-package breakdown is **absent** (arms A and D both
print it). So the signal reached the guard shell, the budget shell and the test
process.

Meanwhile `test.sh`, `build.sh` and `sh -c` all **survived** — each ran its EXIT
trap and printed its profile, and `build.sh` returned an ordinary exit status.
Neither has a TERM trap, so a signal delivered to them would have killed them.

Therefore: **the signal was delivered to a set of processes bounded above by the
`tmpdir-leak-guard.sh` shell, and not to that shell's ancestors.** A signal to
the gate's process group would have taken the ancestors with it — every process
in the gate shares one group, which `runGate` creates with `Setpgid`
(`internal/refinery/gaterun.go:198`). It did not. This is the shape of a walk
over a **subtree**, or of pids signalled one at a time.

## 4. Ruled out, with the measurement

| candidate | status | why |
|---|---|---|
| A fixed timeout on the test step | **RULED OUT** | 85s / 178s / 264s — three values, three runs |
| The refinery's gate deadline (`[gates] timeout`, 60m) | **RULED OUT** | it kills with **SIGKILL** on the process group and reports a `gateTimeoutError`; the recorded `timeout_at` on both September runs was 22:0x, ~58 minutes away |
| `pogo refinery cancel` | **RULED OUT** | same SIGKILL path, reported as a cancellation |
| A signal to the gate's PROCESS GROUP | **RULED OUT** | §3: `test.sh`, `build.sh` and `sh -c` all survived and ran their EXIT traps |
| macOS jetsam / OOM | **RULED OUT** | jetsam sends SIGKILL; this was SIGTERM |
| Host contention | **withdrawn on measurement, by mayor** | load average 12 with 7.30 cores free; darwin counts I/O waiters in the load average (mg-1b8c), so the figure that appears to support saturation is the figure that does not measure it. The refinery's own sampler agrees for both September runs: `saturated_samples: 0`, mean fleet 1.52 and 0.72 cores of 10 |
| Go's per-package budget (`-timeout 20m`) | **RULED OUT** | Go implements `-timeout` by **panicking** inside the test binary, not by signalling, and 85s is nowhere near 20m |

## 5. NOT established — the sender

Nobody knows who sent it, and this investigation did not find out. Recording
that plainly, because a narrowed-down list reads like a conclusion:

- **darwin gives a shell no way to learn a sending pid.** It is available only
  to a handler installed with `SA_SIGINFO`, which a shell cannot install and
  Go's `os/signal` does not expose. Without root there is no `dtrace`.
  **CORRECTED 2026-09-07 by mg-cbc3 — see §8.** Every clause of that is true
  and the conclusion drawn from it was not: a shell cannot install `SA_SIGINFO`
  and Go does not expose `siginfo_t`, but a compiled helper installs it in one
  call, needs no privilege, and `/usr/bin/cc` is on this box. The sending pid
  was recoverable for all three occurrences and nothing was recording it.
- The repo ships exactly one leaves-first SIGTERM subtree walk — `kill_tree` in
  `scripts/launchd/pogo-deploy.sh`, whose default signal is TERM and whose
  shape matches §3. **This is a shape match and nothing more.** No evidence
  places any caller of it near these three runs, its roots come from its own
  fixtures or the deploy's own pids, and it refuses roots of 0 and 1. It is
  named here so the next person does not have to re-find it, **not** as a
  finding.
- Everything else on the box that signals — `internal/agent`'s `Stop`
  (SIGINT, its own child), `internal/client`'s `StopServer` (SIGTERM, one pid
  from the server it started), `platform/sleep`'s orphan reaper (SIGTERM,
  `ppid == 1` plus an exact argv match on `log stream`), `orphanwatch`
  (SIGKILL) — was read and none of them addresses a subtree.
- **Whether the three kills share a cause is still not established.** The
  August one and the September pair agree on signal, on position and on the
  fact that they happened at all. Nothing measured here connects them beyond
  that.

## 6. What was shipped

`scripts/signal-witness.sh`, wrapped around the Go-test row in `test.sh` and
silent on every run that is not signalled. On a signal it records the two
readings that were being lost, and that §3 had to reconstruct by hand from
three lines of output:

- **the delivery shape** — which of the row's ancestors are still alive at the
  instant the signal lands. That is the reading that separates a group kill
  from a subtree walk, and it separates two different senders.
- **the process table at that instant**, written to
  `${POGO_HOME:-~/.pogo}/signal-witness/` rather than into the gate output. It
  is ~870 lines on this host (860 and 870 in two samples); pasted inline it
  would not merely be truncated, it would blow the refinery's 8 KB
  persisted-output cap (`internal/refinery/gateoutputcap.go`) and evict the failure text around it —
  the remedy exhibiting the defect it remedies. The file also outlives the
  refinery worktree, which is deleted when the MR resolves.

It labels its own certainty. `OBSERVED` means the signal was caught in a trap.
`AMBIGUOUS` means the wrapped command merely returned 128+N — which a shell
**cannot** distinguish from a child that chose `exit 143`, and calling that a
kill would be manufacturing the same false fact in the opposite direction.

What it still cannot see: **SIGKILL** (no trap runs), and an ancestor whose pid
was **reused** after it died, which reads as ALIVE and biases the reading toward
"not a group kill".

## 7. Left for others

- **Classification** is mg-b1df's: `build.sh` flattens the 143 to 1, so
  `signalThatKilled` — which reads the wait status of the gate's **direct
  child** — sees an ordinary exit and the run comes out `defect`. That is
  measured here (§1's two September rows) and deliberately not repaired here.
- Whether this is specific to the refinery gate or affects any long-running
  test step: still open. All three occurrences are refinery-gate runs, but the
  gate is also where nearly all long `go test` runs on this host happen, so the
  population is not a control.

---

# Second pass, 2026-09-07 evening — mg-cbc3

mg-3bd1 merged and was archived with its open question — the sender — in
neither a live item nor a successor. mg-cbc3 is that successor. **The sender is
still not identified.** What changed is below.

## 8. The sending pid IS recoverable on darwin, without root

The dead end recorded in §5 is not one. `si_pid` in a handler installed with
`SA_SIGINFO` names the sending process, and installing that handler needs a
compiled program rather than a privilege. Measured on this host (darwin 24.6.0,
arm64) with the sender's pid known in advance and read out of the *sending*
process rather than assumed:

| sender | its pid | recorded `si_pid` |
|---|---|---|
| the calling shell | 52601 | **52601** (uid 501, si_code 0) |
| a distinct subshell, neither the target's parent nor the caller | 52656 | **52656** (uid 501, si_code 0) |

The second row is the one that discriminates. In the natural arrangement the
sender *is* the target's parent, so a program that printed `getppid()` would
pass the first row and every other test; it fails the second. That pairing is
Test 3 of `scripts/signal-sender_test.sh` and is the control the claim rests on.

Also checked, so the next person does not re-derive them: python3 on macOS has
neither `sigwaitinfo` nor `sigtimedwait` (measured: both `hasattr` false), and
Go's `os/signal` delivers the signal number and nothing else. C is the only
route, and it is a short one.

**Shipped:** `scripts/signal-sender.c` and its front-end
`scripts/signal-sender.sh`, on the Go-test row **inside** `tmpdir-leak-guard.sh`
— between the guard and `go-test-budget.sh`. The position is the whole reason
this is a second instrument rather than an edit to `signal-witness.sh`: §3
bounds the signalled set ABOVE by the guard, and the witness sits OUTSIDE the
guard, so on all three recorded occurrences it would have taken its AMBIGUOUS
arm and learned nothing about a sender. The witness still owns the ancestor
reading, which can only be taken from outside the signalled region.

It compiles itself once per revision of its source into
`<pogo state root>/signal-witness/bin`, and if it cannot build — no `cc`, a
failed compile, a cached binary that fails its own no-argument self-check — it
`exec`s the wrapped command, which leaves the process chain byte-for-byte what
it was before the instrument existed, and says so on stderr. An instrument must
not be able to turn this row red, and one that is off quietly has been off for
months by the time anybody asks.

Its stderr block caps each resolved `ps` line at 200 columns and the record file
does not. That split is the same reasoning that put §6's process table in a
file: an agent's argv on this box runs past a kilobyte, the refinery persists
8 KB of gate output head+tail, and nine ancestors printed in full would evict
the failure text the block is attached to — the remedy exhibiting the defect it
remedies. Test 13 exercises the truncation with a 610-character argv and a
sender that deliberately outlives its own signal, because a sender that exits
first resolves to nothing and the capped path never runs.

**What it still cannot see:** SIGKILL, as before. And `si_pid` is an integer —
see §10.

## 9. Four more candidates eliminated, each with its measurement

| candidate | status | the measurement |
|---|---|---|
| The nightly deploy's `kill_tree` (§5's shape match) | **RULED OUT for these three, on timing** | `~/Library/Logs/pogo/pogo-deploy.log` records deploy activity on 2026-09-07 only at 02:00:00–02:01:03Z and 05:30:00–05:30:01Z, and on 2026-08-19 only from 02:00:00Z; there is no entry in the 17:00–20:30Z band on either day. Positive control: the same grep DOES return runs, so the file is being read and the absence is an absence. The shape match stands; a caller of it near these three runs does not |
| pogod doing anything fleet-shaped | **not supported** | every `~/.pogo/events.log` row within ±90s of all three kills, read: no stop, reap, drain, gc or deploy at any of them. Positive control: the same window around the 20:08 kill contains `agent_stopped cat-t9af1 pid=43216 reason=requested` at −87.9s, so a kill-adjacent event IS visible to this instrument when one exists |
| Anything the macOS unified log would record | **no evidence either way** | the ±10s window around 2026-09-07T20:08:02Z was dumped in full (30,585 lines at `--info`) and contains no SIGTERM, kill or termination record, and no mention of pid 86690. Stated as a null instrument rather than as a negative finding: `kill(2)` is not logged by darwin at all, so this window would look identical whoever sent the signal. The window is otherwise dense and current, which is what makes §10 readable out of it |
| `internal/agent`'s own SIGTERMs (pm-pogo's lead, appended to the archived mg-3bd1) | **tested, not supported** | three independent legs. (a) Every signal in the package goes through `agent.cmd.Process` — a live `*os.Process` for an unreaped child, whose pid cannot be recycled while the zombie holds it — never a bare integer pid and never a negative one; `grep` for `syscall.Kill`/`killpg`/`Kill(-` finds none in the package. (b) `Registry.StopWithCause` sends `os.Interrupt`, which is **SIGINT**, not SIGTERM (`internal/agent/agent.go:1388`; the comment one line above it says SIGTERM and is wrong — noted, not repaired here). (c) Decisively, and independent of both: the recorded victims include `go test` **and its parents**, the guard and budget shells. A test's signal to a process it spawned cannot reach its own grandparent. To produce §3's shape a test would have to signal a pid outside its own subtree, and no such call site exists |

## 10. THE PID SPACE RECYCLES IN MINUTES ON THIS BOX

New, measured, and it changes what kind of sender to look for.

`tmpdir-leak-guard.sh`'s private directory named its owner: the 20:08 run's
guard was **pid 86690**. Reading `launchd`'s own spawn records out of the
unified log for the same minutes gives a pid-versus-time curve across the
occurrence:

```
21:06:33.034 BST   86263
21:06:34.457 BST   86337      <- the guard, 86690, is allocated just after here
21:07:04.814 BST   95537
21:07:33.869 BST    9174      <- wrapped past PID_MAX
21:08:13.001 BST   19024
```

(BST = UTC+1; the kill is 20:08:02Z = 21:08:02 BST.) Rates: **292 pids/s**
across 21:06:33→21:07:04, **469/s** across the wrap, **252/s** after it. The
entire ~100,000-pid space turned over **once inside the 85 seconds** between the
guard being created and the guard being killed. Measured again on the quiet box
at 23:32Z, with one polecat working: 1,112 pids in 20 s = **56/s**, still a full
wrap every ~30 minutes.

Two consequences.

**It dates the guard independently.** Pid 86690 falls between the 21:06:34
(86337) and 21:07:04 (95537) samples, i.e. ≈20:06:40Z — 82 seconds before the
kill at 20:08:02Z, against the 85 s the refinery recorded. Two unrelated
records agree, which is the only reason to trust either.

**Any construction that holds a pid and signals it later is unsafe here, and
the fault it produces has exactly the recorded shape.** A leaves-first SIGTERM
walk over a *recycled* pid kills whatever now holds it and everything below it,
and spares that process's ancestors — §3's shape, with no intent toward the
gate at all. It also re-explains the invariant position without needing
`internal/agent` to be causal: nearly every process on this box lives
milliseconds (that is what 300/s means), so a stale pid overwhelmingly resolves
to nothing or to something already gone; the rare long-lived victims are
concentrated in exactly the ten-minute test step. `run_bounded` in
`scripts/launchd/pogo-deploy.sh` is one such construction — `sleep N`, then
`kill -0 "$p"`, then `kill_tree "$p" TERM` — and its timing rules it out for
these three (§9) while its shape stays worth knowing.

**This is a hypothesis with a mechanism and a supporting measurement. It is not
an identification, and it must not be quoted as one.** What it predicts is
testable in one occurrence: `si_pid` will name a process with no business
signalling the gate, and the `ps` block will show what it actually is.

## 11. Still open, so that it is in a live item and not only here

- **The sender.** Unidentified. §8 ships the reading; nothing has been signalled
  since it was wired.
- **Whether the three share a cause.** Unchanged from §5.
- **Whether `internal/agent` is causal or merely the clock.** §10 gives a
  reading under which it is merely the clock. Not decided.

