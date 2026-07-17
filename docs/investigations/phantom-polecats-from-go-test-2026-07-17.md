# Phantom polecats — `go test` wrote the live witness, and pogod mailed a `kill` for it

**Work item:** mg-da48 · **Date:** 2026-07-17 · **Status:** Fixed

`go test ./internal/agent/` wrote **phantom polecat records into the live
witness store**, and pogod's orphan detector read them back and mailed the mayor
an authoritative instruction to `kill <pid>` — at pids that were already dead
and recyclable. Measured on the live fleet, three times in ten minutes,
triggered by our own polecats running the test suite.

Two defects, fixed separately because they are separate: the store let the tests
in, and the alert handed out an instruction that decayed before it was read.

## The measurement (mayor, 2026-07-17 12:39–12:46Z)

Three `[orphaned-polecat]` mails arrived from pogod:

| "polecat" | pid | work item as reported |
|---|---|---|
| `ready-test` | 71259 | `(unknown — no work item recorded in its witness)` |
| `cadence` | 99124 | `(unknown)` |
| `no-sentinel-profile` | 438 | `(unknown)` |

None are polecats. All three are **Go test fixture names**, from
`internal/agent/nudge_test.go` and `internal/agent/attach_regression_test.go`.
All three pids were already gone by the time they were checked (`ps -p` empty).
`pid 438` is wrapped and low — the clearest available tell that pid reuse is in
play on this box.

## Root cause: the guard existed, was documented, and did not transfer

`witnessPathOverride` (`witness.go:113`) is documented as letting tests point the
store at a temp dir, and `witness_test.go` ships `sandboxWitness` to do it. Usage
per file, before the fix:

| File | `sandboxWitness` calls |
|---|---|
| `internal/agent/witness_test.go` | **16** |
| `internal/agent/nudge_test.go` | **0** |
| `internal/agent/attach_regression_test.go` | **0** |

The override defaulted to `""`, which fell through to the real store path. So
**any test in package `agent` that spawns an agent** reached `noteWitnessStart` →
`RecordPolecatWitness(a.Name, a.PID, a.WorkItemID)` and wrote live state.

The distribution is the whole finding. The file that *tests the witness*
sandboxes it sixteen times. The files that merely *spawn agents incidentally*
sandbox it zero times — because from where they stand they are testing nudges
and attach, and the witness is not their subject. **An opt-in guard is only ever
remembered by the tests that least need it.** Same shape as mg-a558 ("the
sibling function already has the guard and it didn't transfer"), and the same
pollutant as `trust-the-record-not-the-statistic` (`go test` polluting
`events.log`) in a different store. The lesson did not transfer either time.

## Why this was HIGH and not test hygiene

The alert is not inert text. It closed with `kill 438 && mg unclaim (unknown…)`
and stated that it repeats every `1h0m0s` until the process is gone. The chain:

1. A test writes a witness record carrying the test process's real pid.
2. pogod's orphan detector reads it, finds the pid alive (the test, briefly),
   and mails the mayor.
3. **The test exits within seconds. The pid is freed and becomes recyclable.**
4. The mail persists, repeating hourly, carrying a bare `kill <pid>` with no way
   for the reader to re-verify identity at read time.
5. A mayor acting on that instruction *exactly as written* kills whatever
   process now holds pid 438.

That is `stray-pkill-killed-the-fleet` with the daemon's authority behind it,
aimed by a decayed record.

The cruel detail: **mg-13a3's `(pid, start_time)` witness genuinely protects the
DETECTOR from pid reuse** — `witnessVerdict` re-probes and resolves GONE on a
recycled pid, it will not re-alarm. The **mail** stripped `start_time` out of its
actionable half, so the protection did not survive the trip to the reader. The
one consumer told to run `kill` was the one consumer the second witness could not
reach. (`world-state-claims-decay`: true when formed, stale when sent — and here
what is sent is a loaded gun.)

## Fix (1): the store is test-safe BY DEFAULT

`WitnessPath()` resolves to a per-process temp file when `testing.Testing()` is
true and no explicit override is set. Under a test binary **the live store is not
reachable from the function at all** — there is nothing to remember and nothing
to forget.

Deliberately *not* done: adding `sandboxWitness` calls to the two named files.
That is the same opt-in guard that already failed to transfer, and the next test
to spawn an agent fails the same way.

Three properties the shape had to keep:

- **Redirect, not disable.** A default that made the store unwritable would pass
  a "did not touch the live store" assertion while silently breaking the sixteen
  tests that legitimately use it. `TestWitnessRoundTripsThroughTheDefaultSandbox`
  pins that the default path is a real, working store.
- **`witnessPathOverride` still wins.** Per-test isolation and
  isolation-from-the-fleet are different questions; the default answers only the
  second.
- **Subprocess tests unaffected.** A test that boots the real pogod binary is not
  a test binary in that child, so it resolves `POGO_HOME` as production does —
  correct, and those tests already sandbox `POGO_HOME` for the child.

The fallback when `MkdirTemp` fails is *another* temp path, never
`config.PogoHome()`: a test that cannot get a temp dir must fail to write
anywhere rather than succeed at writing a phantom into the fleet.

## Fix (2): the alert is re-verifiable at read time

The body now carries the recorded `start_time` and routes the reader to `pogo
agent witness --json` — which is `witnessVerdict` reached over a process
boundary, i.e. *the instrument*, not a second definition of "our process is
alive" written in prose. It states the **stale case as the default reading**,
because by the time an hourly alert is read that is the likelier one.

Every runnable kill is **gated, not merely preceded by advice**:

```
pogo agent witness --json | grep -q '"name":"cadence","pid":438' && kill 438 && mg unclaim mg-xxxx
```

Prose asking the reader to check first is skippable; a `grep && kill` is not. If
the polecat is no longer witnessed alive, grep fails and the kill never runs.

The pattern matches **both halves of the identity**. A name-only grep would pass
against a live *successor* — names are reused, and `RecordPolecatWitness`
replaces a record by name on respawn — gating the kill open on the successor's
pid. `agent.WitnessAliveGrep` builds it, and `cmd/pogo` pins it against the real
marshalled output of the real command: the pattern is a claim about a
serialization in another package, and only a test at the seam makes it a fact.

Found en route and fixed: with no work item, the old body emitted `mg unclaim
(unknown — no work item recorded in its witness)` — not a command, a **shell
syntax error wearing one**. It matters because the gate is a prefix the reader
must not remove, and a line that errors as written trains the reader to edit
before running; the first casualty of an editing reader is the part they did not
understand.

**Explicitly not done** (per the ticket's DO NOT): the three pids were not
killed — they were dead, and the records were test residue. The alert was **not**
silenced or rate-limited. Real orphans (mg-0b77, mg-46a4, mg-61a0) are a genuine
unsolved problem and this alert is their only signal. **The noise was the bug,
not the alarm**: fix (1) removes the false population, and the alert stays loud
for the true one.

## RED proofs

Each fix was demonstrated failing before it was demonstrated passing.

| # | Break | Test that goes RED | Observed |
|---|---|---|---|
| 1 | Remove the `testing.Testing()` branch from `WitnessPath` | `TestSpawningAgentDoesNotTouchTheLiveStore` | The incident, reproduced verbatim: `{"name":"ready-test","pid":39912,...}` written to the config-derived store |
| 2 | Same | `TestWitnessPathNeverResolvesToTheLiveStoreUnderTest` | `WitnessPath()` = `.../.pogo/polecat-witness.json` — the live path |
| 3 | Revert the body to a bare `kill %d && mg unclaim %s` | `TestMailOrphanAlert_IsReVerifiableAtReadTime` | kill not gated on the witness pattern |
| 4 | Grep the name only, not `(name, pid)` | `TestWitnessAliveGrepDoesNotMatchADifferentPid` | pattern for the dead orphan matches a witness holding only its live successor |

The new tests deliberately **do not** call `sandboxWitness`. Sandboxing them
would make them model the file that already remembers, when the defect is
entirely about the files that do not — each one stands where a naive sibling
stands: override empty, `POGO_HOME` pointing at a store it has never heard of.
They point `POGO_HOME` at a temp dir and treat it as the fleet's real store: the
property under test is that `WitnessPath` refuses the *config-derived* path under
test, whatever it happens to be. Aiming a test at the operator's actual `~/.pogo`
would prove the same property by risking the very corruption it tests for.

## Verification against the live fleet

`~/.pogo/polecat-witness.json` held three real polecats (`outliver`, `da48`,
`8f09`). A full `go test ./internal/agent/ -count=1` left it **byte-identical**
(same sha, same mtime), with no fixture names present. Full `./build.sh` green.

## Open question, not chased

Two stores are now known to have been polluted by the test suite: the witness
(this ticket) and `events.log` (`trust-the-record-not-the-statistic`). Both were
found by noticing the output, not by looking. **Which others?** Worth asking; out
of scope here.
