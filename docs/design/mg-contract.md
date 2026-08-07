# The mg contract: how pogo's tests depend on a binary in another repo

**Status:** shipped (mg-216c, 2026-08-07). The mechanism is
`internal/mgcontract`; this file is the ruling behind it.

## The defect

pogo's test suite asserts the behaviour of whatever `mg` binary happens to be
installed, **across a repo boundary, with no contract and no pinning**. Any
correct change in macguffin therefore lands in pogo as an unannounced gate
outage, discovered only when the refinery starts rejecting every branch.

It happened twice in ninety minutes on 2026-08-07:

| macguffin change | what it did | what it broke in pogo |
|---|---|---|
| **mg-d639** (~19:00) | `mg mail send` refuses an unknown recipient instead of silently creating a phantom mailbox | `internal/strandedmail` — its fixtures seeded mail at invented names. Plus five further sites: the sink harness seed, `mail_alert`'s production diagnosis path, and two more. Fixed by mg-0155 / da8a120. |
| **mg-9259** (~19:00) | a refused `mg done` PRESERVES the sidecar | `internal/agent` — `TestTriagePacketIsWrittenBeforeAnySuccessorExists` asserted the old behaviour. Fixed by mg-6a0b / 3fbf303. |

**Both macguffin changes were improvements. Neither was at fault.**

## Why it cost so much more than the bug

`build.sh` runs `go test ./...`, and the refinery gate runs `build.sh`. So one
such test takes down **every pogo merge**. On the night in question that
composed all the way to the deploy: red gate → nothing lands → every polecat
holding a pushed branch owes an unlandable merge → the drain's wait-set goes
fleet-wide at 03:00 → the nightly exits 7.

Three costs, all measured, none of them "gate outage minutes":

1. **It cost a full-suite run to localise.** The gate reported a bare
   `./build.sh failed: exit status 1`, naming neither package nor cause.
2. **The error implicated the wrong branch.** Five branches were killed by the
   second outage — `polecat-ba465`, `polecat-p65d2`, `polecat-p2416`,
   `polecat-p43d5`, `polecat-q5515` — each needing an individual exoneration,
   and two had already started hunting for a defect in their own diff.
3. **It manufactured false attributions.** Three agents reported the second
   outage as "mg-d639 fallout #6" because the *shape* was identical. It was
   mg-9259. An uncorrected one would have gone to the wrong diff.

The asymmetry was invisible until someone counted by repo: 0 of 3 pogo-repo MRs
passing against 4 of 4 research-repo merges. The research repo has no Go gate,
so it never sees this class at all.

## The ruling

### 1. A live test must DECLARE the mg behaviour it depends on

`internal/mgcontract` holds the declared contract: a named clause per behaviour,
each with an executable probe, the mg work item that created the behaviour, why
pogo needs it, and which pogo tests rest on it. A test says what it depends on:

```go
func TestRefusedDoneKeepsTheResultItWasGiven(t *testing.T) {
    mgcontract.Require(t,
        mgcontract.DoneRefusesADeclaredItemWithNoSuccessor,
        mgcontract.DoneRefusalPreservesTheResultSidecar,
    )
    ...
}
```

`Require` probes the installed binary once per clause per process. If the clause
holds, nothing changes. If it does not, the dependent test **skips** and
`TestTheDeclaredMgContractStillHolds` **fails, by name**.

**The gate still goes red, and that is correct** — pogo genuinely holds a stale
expectation and somebody has to rule on it. What changes is that the red arrives
once, in a package named after the coupling, carrying the clause, the mg item,
and the list of what else to re-read. Occurrence cost goes from a full-suite
hunt and three misattributions to a glance.

**Why the dependents skip rather than fail.** A test whose premise about mg is
gone cannot produce a finding about pogo — whatever it asserts next is
arithmetic on a fiction, and it is precisely that kind of assertion that blamed
five innocent branches. Nothing is hidden: the contract test is red, so the gate
is red, so nothing merges.

### 2. Do not resolve a break by editing the probe to match mg

Mayor's framing on mg-6a0b, which generalises: **decide which side is wrong; do
not flip the assertion.** A clause rewritten to agree with whatever the
dependency now does has stopped testing anything, and every pogo test behind it
is then resting on a behaviour nobody checked. Either mg regressed (file
against mg) or pogo's expectation is the stale one (fix the dependent tests and
re-declare the clause with its new `Since`).

The two 2026-08-07 fixes are the worked examples of doing this properly. mg-6a0b
did not invert `len(hits) != 0` into `len(hits) == 1` — it asked which side was
wrong, found pogo's rationale falsified in both premises, and replaced the
assertion with a stronger one about the property mg-9259 was filed to create.

### 3. Which tests may be live at all

A test may drive the real `mg` when **the cross-binary behaviour is the thing
under test**. It must be hermetic when `mg` is **incidental setup**.

| | keep live, declare clauses | make hermetic |
|---|---|---|
| what it is | an integration control whose value IS that a fake cannot notice the change | a unit test that needs a store in some state |
| the tell | a renamed field or a changed refusal would break production and this is what would catch it | you would be equally happy with any tool that produced the fixture |
| example | `internal/strandedmail`'s `TestAgainstRealMg` — a mock keyed on our own struct tags could never notice mg renaming `unread`, which is this bug's exact shape | `internal/ghintake`'s case matrix, which runs against `MGSource{Bin: stub}` |

`internal/ghintake` is the pattern to copy: a stub `mg` driven by a shell case
statement covers the cases, and **one** live control covers the wire format.
This is the same family as archived **mg-5336** (test hermeticity for
`internal/project` writing the real `~/.pogo/projects.json`), which is why the
recurrence is worth a mechanism rather than a fourth hand-fix.

Sites still using real `mg` purely as a fixture builder — `internal/agent`'s and
`cmd/pogod`'s claim-release stores among them — are **declared but not yet
converted**. They now name their clauses, so a change in mg no longer reaches
them as an unattributed failure; converting them to a stub is separate work and
is not urgent once attribution is fixed.

### 4. The gate says which package failed

`internal/refinery`'s `summarizeGateFailure` reads the failing names out of the
gate's own output, so the sentence that travels onto the MR is

```
./build.sh failed [internal/agent: TestTriagePacketIsWrittenBeforeAnySuccessorExists]: exit status 1
```

instead of `./build.sh failed: exit status 1`. It does not prevent the outage; it
cuts each occurrence from a full-suite hunt to a glance, and it applies to every
gate failure, not just this class. It reports names it *found* and says nothing
when it recognises nothing — a guess would send the reader somewhere wrong,
which is the complaint being answered. A broken test **sandbox** is reported as
setup, alone, and never as the assertion names underneath it (mg-3412).

## Adding a clause

Add a `Clause` to `internal/mgcontract`, with `Since` naming the mg item that
established the behaviour, `Why` saying what breaks in pogo without it, and
`Dependents` naming the tests that rest on it. `Why` and `Dependents` are
required by `TestEveryClauseIsDeclaredOnce` — they *are* the failure report.

Then prove the clause is a control and not a rubber stamp. `drift_test.go` does
this by putting a deliberately-drifted `mg` first on `PATH` and reading back
what the clause says about it; the three shapes there are the two real outages
and the doctor's empty-store miscount.

## What this does not claim

It does not make the dependent tests hermetic, and it is not a version pin:
`mg` is still whatever is installed. It says what pogo believes about that
binary, in one place, so a change to it is attributable in one line instead of
reconstructed by hand across five branches and three agents.
