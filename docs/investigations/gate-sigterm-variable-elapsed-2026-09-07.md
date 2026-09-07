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
