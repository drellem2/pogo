# The ownership/wedge positive control was SPENT unused, and the launchd residue is not its replacement

Filed for mg-bb68 on 2026-08-14, from the polecat that took the ticket. The
ticket was opened 2026-08-07 17:58Z, parked under a token cap on 08-10, and
unparked on 08-13 — so the record is written a week after the event it records,
and that gap turns out to carry the most useful part of it.

pm-pogo ruled on 2026-08-07 17:52Z that the positive control on this arc was
SPLIT, and that one half was spent. This file records the spent half. The live
half is mg-6da3 and is covered in §5; do not conflate them.

## 1. The control, and what it needed

A positive control here means: catch the ownership/wedge condition *while it is
happening*, point a detector at it, and watch the detector go RED. Without that,
a detector's green is a claim about a state nobody has seen it meet.

The state it needed was specific: a pogod started **outside** launchd holding
the pogod lockfile and `:10000`, while the loaded `com.pogo.daemon` job could
not acquire that lock, exited 1, and was respawned by KeepAlive roughly every
ten seconds. On this box that pogod was a child of Daniel's Emacs (`pogo.el`
spawns pogod with no port check — mg-2def), and the displacing process was pid
4368.

## 2. It ended before anything was dispatched at it — measured, not inherited

Re-measured for this record from the primary source, `~/Library/Logs/pogo/pogod.log.1`,
which is size-gated rather than rotated per start and so still spans the whole
episode:

```
$ grep -c "Cannot acquire pogod lock" ~/Library/Logs/pogo/pogod.log.1
19274
$ grep -c "Cannot acquire pogod lock" ~/Library/Logs/pogo/pogod.log
0
```

Line 4 is the first, preceded by `2026/08/05 12:45:49`. Line 77096 is the last,
preceded by `2026/08/07 18:37:18`. Every one of the 19,274 names pid 4368. Ten
seconds after the last failure the job won:

```
77096: Cannot acquire pogod lock /Users/daniel/.pogo/pogo.pid: held by pid 4368 …
77098: 2026/08/07 18:37:28 pogod: starting (pid=32415)
```

Those are local stamps (BST, UTC+1), so the episode ended **2026-08-07
17:37:28Z** — the figure in the ticket title. Both forms of the timestamp travel
in the surrounding record (`17:37:28Z` here, `18:37:28` in
`pogod-displacement-episode-closed-2026-08-13.md`); they are the same instant
and neither is wrong.

**CAUSE: the condition self-resolved before any demonstration was dispatched.**
Daniel's Emacs (pid 2110) exited, launchd bound the port, and pogod became pid
32415 with ppid 1. This was not a window missed through anyone's inaction — it
closed by accident while pm-pogo's mail sat unread in the mayor's inbox during a
fleet restart. There is no decision here to correct; there is only a count to
keep honest.

**This was the third such control spent unused on this arc.** mg-fc99 named the
live plist drift as its control and warned it was perishable; the assertion was
never built and mg-b201 installed the corrected plist on 2026-08-07 14:03:28.
mg-8dcb records that one. This is the third.

## 3. What survives, and what it is NOT

The residue is real and it is still on this box:

```
$ launchctl print gui/501/com.pogo.daemon | grep -E 'state|runs|pid|last exit'
	state = running
	runs = 24991
	pid = 77880
	last exit reason = OS_REASON_CODESIGNING
```

plus the 19,274 log lines above. `runs` and `last exit reason` are **lifetime**
fields: they keep climbing across a repair and stay set on a daemon that is
healthy and current, which is exactly what this reading is. The ticket recorded
`runs=24976`; a week later the same counter reads `24991` on a box that has been
supervised throughout. One sample of a lifetime counter cannot name a night.

So: the residue is evidence the wedge **existed**. It is not a demonstration
that a detector **fires** on it, and pm-pogo's instruction was explicit that it
must not be written up as one.

## 4. The detector shipped anyway, and the instruction was honoured

The record could not have predicted this and it is the reason the week's delay
was worth something: a detector for exactly this condition was built and merged
six days after the control was spent, by a polecat that never saw pm-pogo's
ruling. `internal/supervision` (mg-fa79, commit `107f6b2a`, merged 2026-08-13
08:21:18 +0100) compares **the pid launchd attributes to the job** against **the
pid holding the pogod lockfile** — identity, not the properties every existing
instrument was reading.

It did not write the residue up as the demonstration:

- Its positive controls are **constructed `Observation` literals**, including the
  2026-08-05 shape, in `TestCheckVerdicts`. They transcribe the episode's pid
  (4368) as a fixture value; they never read the host and never claim to.
- `TestPPIDNeverChangesTheVerdict` and `TestLastExitReasonNeverChangesTheVerdict`
  pin the two residue fields as report-only. `runs` never enters the
  `Observation` at all.

Two facts about deployment, checked rather than assumed:

- **The detector is merged but NOT running.** `git merge-base --is-ancestor
  107f6b2a 082ec38b` exits non-zero: the pogod serving :10000 is revision
  `082ec38b` (committed 2026-08-13 01:19:29 +0100, started 03:01:29), which
  predates the merge. The installed CLI at `/Users/daniel/go/bin/pogo` was built
  Aug 13 03:00 and has no `service supervision` subcommand at all.
- **Built from this worktree, the detector reads the box correctly today:**

  ```
  $ pogo service supervision
  SUPERVISED: com.pogo.daemon and the pogod lockfile name the same process (pid 77880)
    launchd job pid : 77880
    lockfile holder : 77880
  EXIT=0
  ```

  Which is the point, and the loss: the only state the detector can now be
  pointed at is the healthy one.

## 5. Do not conflate this with the staleness control (mg-6da3)

The other half of the split was **not** spent. It was live, it was time-boxed to
the 2026-08-07 deploy, and it was used: `TestLiveDaemonStaleness` in
`internal/driftwatch/revision_test.go:710` runs the real predicate against the
real running daemon and against the real build stamp of a binary built from
origin/main, and **requires the two arms to disagree**. It refuses to run rather
than degrade to the one-armed shape it replaced. That is what spending a control
properly looks like, and it is the comparison this record exists next to.

## 6. What the spent control actually cost, named precisely

Most of what the control would have bought turns out to have been constructible
anyway, and has been constructed:

| layer | proven? | by what |
|---|---|---|
| `Check` judges the live states | yes | `TestCheckVerdicts`, constructed observations |
| `Check` judges the *post-wedge* state | **not until this ticket** | §7 — `TestCheckVerdicts` had no row for it |
| residue fields never reach a verdict | yes | `TestPPIDNeverChangesTheVerdict`, `TestLastExitReasonNeverChangesTheVerdict` |
| `launchctl print` with no live pid parses to `ok=false` | yes | `reconcile.TestParseLaunchctlPID` |
| `Observe` is soft on an absent job | yes | `TestObserveIsSoftOnAMissingJob` |
| **`Observe` produces the displaced reading from a real host** | **no** | **nothing — this is the spent control** |

The single irreducible gap is the **join**: no test shows `Observe` reading a
loaded job with no live pid *beside* a live rival holding the lockfile, and
handing that to `Check`. Every ingredient is proven; the assembly is not. That is
one seam, not a hole, and naming it is worth more than mourning the control.

Two ways to close it, neither taken here:

1. **Reproduce the real condition.** `pogo.el` spawns pogod with no port check
   (mg-2def), so a fresh Emacs session recreates it exactly. This is
   **fleet-destructive** — it ends every agent on this box — and is Daniel's
   call, not the fleet's.
2. **Synthesise a loaded job with no live process.** Load a throwaway
   `com.pogo.test.*` LaunchAgent that exits immediately, hold a lockfile with a
   live process under a sandboxed `POGO_HOME`, and point `Observe` at that label.
   Cheaper and non-destructive, but it mutates the host's launchd domain, so it
   is a decision rather than a test someone should add in passing.

## 7. What this record changed in the tree

Prose forbidding a misreading is a claim that can rot. The one place the
residue could actually be written up as the demonstration is inside the detector
itself, and it was unguarded:

`Check` evaluates `JobLoaded && !LockPIDOK` (→ `UNKNOWN`) **before**
`JobLoaded && !JobPIDOK` (→ `UNSUPERVISED`). That ordering is what makes the
box-after-the-wedge — loaded job, no live process, no live holder, exit reason
still set — read `UNKNOWN` rather than `UNSUPERVISED`. `UNSUPERVISED` requires a
**live** rival owning the POGO_HOME; the displacement is the second live
process, not the wreckage it leaves.

**Nothing pinned that ordering. Measured 2026-08-14, both arms:** swapping the
two cases — a plausible edit, since the second is the case the package was
written for — leaves the package's entire pre-existing suite **green**, and
makes the residue shape report

```
UNSUPERVISED: com.pogo.daemon is loaded but has NO live process, while pid 0
owns this POGO_HOME and is serving … a wedged pid 0 would never be restarted
```

A confident verdict, naming a process that does not exist, produced from a job
that is merely idle — the residue written up as the demonstration, emitted by
the detector itself.

Nor would anything outside the package have caught it. `Check` has exactly two
call sites, both in `cmd/pogo/main.go`, and neither has a Go test.
`scripts/pogo-self-deploy_test.sh` does assert on an `UNSUPERVISED` verdict, but
against a **stub CLI that echoes a canned line** — it never reaches this code.

`internal/supervision/residue_test.go` closes that. `TestResidueAloneIsNeverUnsupervised`
pins the verdict on the residue shape; `TestNoVerdictNamesAHolderThatIsNotLive`
is the general form and fails on any reason line that names an owner while
`LockPIDOK` is false. Both pass on the code as it stands and both fail on the
swap — the guard has been shown able to fail, which is the same standard §5
holds mg-6da3 to.

That is not the demonstration the spent control would have given. It is the part
of it that can be had without the condition, and it is labelled as such in the
test file so a later reader cannot mistake one for the other.
