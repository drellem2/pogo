# Guards for state whose drift leaves no artifact

**Status:** Design of record. Branches 1–3 are each shipped at one site (see the
instance table); the open remainder is §7 and the tail of §8 (`doctor`'s
own check registry — §8's launchd half shipped as mg-7a20).

**Provenance.** Written by architect inside incident mg-0d70, re-homed to design
ticket mg-57c0, and landed here (mg-57c0) because a general finding filed inside
a ticket dies when the ticket archives — which has now happened to this text
twice. Every ticket cited below is archived except where marked open. This
document, not a work item, is the durable home.

---

## 1. The property

**Some state, when it drifts, leaves no artifact anywhere.**

Non-occurrence of a scheduled event is the cleanest case: a retry fire that never
happens is indistinguishable from a night that needed no retry, because there is
no log line for a fire that did not occur. No amount of log reading finds it. The
absence has to be asserted against directly.

The polarity flips without changing the consequence. An *occurrence* can also
leave nothing: pogod's run-mode transition 503s every dispatch endpoint on the
daemon and, until mg-293c, wrote no record at all. In both directions the result
is the same — believed state and actual state diverge, and no instrument
separates them.

## 2. The three guard shapes, and why the choice is forced

The site's **ownership** picks the branch. This is not a stylistic preference:

| Site owns…                                     | Guard              |
|------------------------------------------------|--------------------|
| the write path, idempotent, boots often         | **RE-ASSERT** unconditionally |
| does not own it, or re-assertion is manual      | **COMPARE** expected vs installed |
| observes it but owns neither                    | **LOG THE TRANSITION AT THE SITE** |

**RE-ASSERT** is what agent startup does: `pogo schedule … --id
mail-check-<name>` every boot, with the check-then-skip read explicitly
forbidden — that read is what killed four mail loops (mg-de08). It works and *it
detects nothing*; it substitutes assertion for observation. It is available only
because the site owns the write path, the write is idempotent, and it boots often
enough for that to be timely.

The deploy plist failed all three conditions, which is why it sat wrong for five
days. Nobody re-runs `pogo service install-deploy` at boot, the runner does not
own its launchd registration, and only a human could re-assert it. **When
re-assertion is unavailable, comparison is the only option left.**

**Branch 3** exists because a state change can belong to nobody. Nothing
*registers* pogod's full mode: there is no artifact to re-assert and no
expected/installed pair to compare. The only guard available is that the
component changing the state says so. That branch covers the population most
likely to be missed, because it is defined by not having been thought of — and it
sharpens further: at `internal/service`'s `quiesceCrew`, the component changing
fleet-wide dispatch is *not even the one that owns it*. An installer borrows
dispatch mid-sequence as a precondition for its own work.

> **THE TRAP.** Re-assertion feels safer because it FIXES rather than merely
> REPORTS, so it gets chosen at sites that cannot sustain it — and a site that
> silently got neither is indistinguishable from a site that has one.

## 3. Instances

Five, across four subsystems. The first four were named in mg-0d70/mg-57c0; the
fifth was found while landing this doc.

| Site | Branch | State |
|------|--------|-------|
| launchd deploy plist (runner expects 03/04/05, installed plist was a bare DICT with Hour=3; wrong five days) | 2 | **Shipped** as the `launchd activation` row, `cmd/pogo/launchagentdrift.go` |
| pogod scheduler — `mail-check-*` reaped at restart, `schedule list` still looks populated because differently-named sweeps survive (mg-de08; four PMs, ~6h dark) | 1 | **Shipped** — unconditional re-assert at agent startup |
| orchestrated HTTP mux — a route *registered* is not a route *reachable*: `/hostload` is registered on the `orchestrated` mux but was never forwarded onto the listener, and has 404'd since 1dd47ad (drellem2/pogo#114) | 2 | **Open** — mg-08af (gated to `human`) |
| pogod orchestration mode — transition 503s every dispatch route, unlogged | 3 | **Shipped** — mg-293c, `internal/server/modeaudit.go` |
| `install-deploy` overwrites `~/.pogo/bin/pogo-deploy.sh` unconditionally *while comparing the plist first* — same command, two artifacts, one guarded | 2 | **Open** — mg-3bb3 (drellem2/pogo#123) |

The last row is the instance table earning its keep: the asymmetry is inside a
single command, and it is visible only once the branch rule is written down.

Two corrections to the source text, made on landing. mg-0d70 and mg-57c0 called
row 3 the "recording mux"; there is no recording subsystem in this repo, and the
instance is the **orchestrated HTTP mux** — `/hostload` registered at
`internal/agent/api.go:499` and never forwarded by `cmd/pogod/main.go:792-805`.
It is also the cleanest illustration in the set of why a comparison must report
its whole population: mg-08af establishes that of **25 unique patterns registered
on `orchestrated`, exactly 1 is an orphan**. A boolean "routes registered: yes"
is true and useless; the count is what locates the defect.

## 4. Where the COMPARE runs — it is not the hot path

The design says comparison is forced when re-assertion is unavailable. It does
**not** say the comparison runs at the drifting site's moment of use, and the
distinction is load-bearing.

The deploy runner declines to read the plist, on the record, at
`scripts/launchd/pogo-deploy.sh:446-452`:

> The plist's fire hours. Duplicated from `com.pogo.deploy.plist` rather than
> read from it […] reading the plist at 03:00 to avoid that would add a parse and
> a failure mode to the path that has to work when everything else is broken.

**That refusal is correct and is not a violation of branch 2.** A guard on the
03:00 path buys detection by adding a failure mode to the one path that must
survive everything else failing. The comparison belongs where a person looks
*without being prompted by the failure* — which is `pogo doctor --check`, and
that is where mg-fc99 put it (`cmd/pogo/launchagentdrift.go`, header rationale).

**Rule:** the COMPARE branch names a *comparison*, not a *location*. Site the
comparison on a checklist that runs unprompted; never on the path whose
reliability is the thing at stake. A guard that can take down its subject is a
net loss.

The remaining duplication is still a real defect — the runner derives nothing
from the plist and drifted exactly as predicted — and is tracked open as
**mg-8dcb**, together with the fixture below. It is a *derivation* defect, not a
missing guard.

## 5. HARD REQUIREMENT: the COMPARE branch must log even when it PASSES

If a comparison reports only on DIFFERENCE, then "was full all night" and "was
stopped at 03:00 and something restored it before morning" produce **identical
evidence**. That is not hypothetical — it is the position pm-pogo was in on the
morning of 2026-08-07 and could not get out of.

**Report the whole comparison, never a boolean, and emit it on the passing path
too.** The interesting failures are partial, and partial registration is
precisely what reads as done.

This is shipped and is the model to copy —
`cmd/pogo/launchagentdrift.go:89` renders a population line on *every* row
including the clean one:

```go
population := fmt.Sprintf("%d managed job(s) examined: %d match this build, %d drifted, %d not installed, %d could not be checked", …)
```

Four states are each said out loud — checked/clean, drifted, not-installed, NOT
CHECKED — and the last two are never phrased as the first. Branch 3's equivalent
is `EventModeBoot` (`internal/server/modeaudit.go:47`), emitted unconditionally
at construction so that "which mode did it boot into" stops being answerable only
by inferring from the *absence* of a transition.

The same applies to branch 1's positive line: emit a line per expected fire —
either it ran, or the night closed needing no retry — so a missing fire becomes a
**missing line** rather than an absence of evidence.

## 6. The record must not depend on inherited stderr

Branch 3's first draft called for a log line. That was insufficient, and the
correction is now in the code (`internal/server/modeaudit.go:21-36`): the
transition sites had logged since March 2026, and four months of `pogod.log`
contained **zero** such lines, including across the 2026-08-07 window in which
`POST /agents/drain` was demonstrably answered 503.

pogod logs to *inherited stderr* — whatever the parent pointed it at. **A log
line in source is not a line in the log file.** Anything that must survive is
emitted to `~/.pogo/events.log`, which the process resolves itself and which is
queryable (`pogo events list --type=server_mode_changed`). The log line is kept
for the operator tailing the daemon; the event is the artifact of record.

## 7. Positive control, required on every branch — and it is PERISHABLE

Give each guard a positive control: a deliberately-wrong plist must make the
comparison fire; a deliberately-stopped orchestration must produce a transition
line. **A guard that has never been observed to fail is not known to work, and a
green check that cannot go red looks exactly like a working one.**

**The rule that costs the most to learn:** when a guard's positive control is *a
live defect*, the control is perishable, and its expiry is controlled by whoever
fixes the defect — who is usually not the person building the guard and has no
reason to know.

This was violated the day mg-57c0 was filed. mg-fc99 named the live plist drift
as its free positive control and warned it was perishable; the assertion was
never built; mg-fc99 was archived with its acceptance unmet; and on **2026-08-07
14:03:28** mg-b201 installed the corrected plist. Landing that fix was correct —
the retry was costing real nights. **The defect is that nobody recorded the
spend.** Four locally-reasonable steps ending in a detector that can now only be
validated against a world where its target defect is absent.

So: a guard's ticket must either take the control **before** the fix lands, or
record in writing that it was spent and what a fixture must now reproduce.
"We will validate it later" silently becomes "we will validate it against a clean
world."

**The fixture.** The broken state was `StartCalendarInterval` as a **bare DICT**,
not an array of one element:

```
Dict  { Hour = 3, Minute = 0 }     <- what was actually installed
Array [ Dict { Hour = 3 } ]        <- NOT what was installed
```

These are different plist shapes, and a comparison that only walks arrays passes
clean on the dict. mg-57c0 recorded that the only surviving copies were two
mails. That is no longer true, and the replacement is the right kind: the dict
form is reproduced verbatim as a test fixture at
`internal/service/launchagentaudit_test.go:258`, and `parseLaunchSchedule`
(`internal/service/launchagentaudit.go`) switches on both shapes.

**A second-order warning for anyone re-measuring a fix on a live box.** The plist
changed *during* verification: a PlistBuddy read at ~13:59 returned the broken
dict, a plutil read at ~14:03 returned the fixed array, mtime 14:03:28. Two
correct measurements of two different worlds, presenting as an instrument
disagreement. Stamp the artifact's mtime alongside the reading, or the race reads
as a tool quirk.

## 8. The guard's own coverage is subject to the property

**Direction 1 below shipped as mg-7a20** (`internal/service/launchagentscope.go`,
rendered by `cmd/pogo/launchagentdrift.go`). The row now reads, on the box this
section was measured on:

> `3 managed job(s) examined: 1 match this build, 2 drifted, … SCOPE: 3 of 13
> pogo launchd job(s) LOADED on this box are in this audit's registry; 10 outside
> it — 10 with a recorded reason, 0 with NONE.`

Each of the seven previously-unrecorded exclusions now carries the reason that
makes it a decision rather than an omission — another repo installs the plist, so
this build has no expected copy to compare against — and **a loaded `com.pogo.*`
job with no recorded reason WARNS**, naming the job. That is the part that
survives the population moving: coverage cannot be kept correct by a list, but
"something arrived and nobody ruled on it" can be said on every run. Direction 2
(extending coverage) is untouched and still open.

**Direction 1's own scope, stated in its own output** — it compares against jobs
LOADED in the current user's launchd domain under the `com.pogo.` prefix, so an
installed-but-never-bootstrapped plist, another domain, or a pogo job under a
different label is outside even that denominator; and a failed `launchctl list`
renders as SCOPE NOT OBSERVED, never as zero-outside.

**What remains open is the last paragraph of this section: `doctor` itself.**
There is still no check registry, and the `Long:` help text is still a separately
hand-maintained enumeration.

The original finding, unedited, follows.

**This is the finding this doc adds.**

Branch 2's shipped implementation audits the launchd jobs in a registry —
`managedLaunchAgents()` in `internal/service/launchagentaudit.go`. Counted
2026-08-10 on this box:

- **Registry: 3 jobs** — `com.pogo.daemon`, `com.pogo.recovery`,
  `com.pogo.deploy`.
- **Loaded on this box: 13 pogo jobs** (`launchctl list | grep pogo`).
- **Coverage: 3 of 13.**

Of the 10 uncovered, **3 are excluded with a recorded reason**:
`com.pogo.revisionprobe` (deliberately out, mg-a03d — a Go-rendered row would
make the auditor for the deploy witness arrive *by* the deploy), and
`com.pogo.notify` + `com.pogo.deadman` (installed by pogo-reminders, named at
`internal/service/launchagentaudit.go:382`).

**Seven are excluded with no recorded reason at all:** `com.pogo.bridget`,
`com.pogo.gh-issues`, `com.pogo.pa-calendar`, `com.pogo.pa-heyfeed`,
`com.pogo.sleepwake`, `com.pogo.watchdog`, `com.pogo.wifi`.

The registry states its own promise at `internal/service/launchagentaudit.go:207`:
"the next two-artifact ticket gets audited because its job is in the registry,
not because somebody remembered to write a check for it." **That promise holds
only for jobs installed by `internal/service`.** Seven live jobs arrived by other
install paths and are outside it.

**The population is NOT stationary,** and this is the part that rules out fixing
it by enumeration: the registry grows only when someone adds a job *to
`internal/service`*, while the job count on the box grows whenever anyone adds a
launchd job by *any* path — a shell installer, pogo-reminders, a hand-written
plist. The two numerators move independently, and only one of them is the one
being audited.

**Why this is the doc's own subject, one level up.** §5 requires the comparison
to report its whole population, and it does — "3 managed job(s) examined: 3 match
this build". A reader sees a complete-looking census and a clean result. Nothing
in that line says ten other pogo jobs on this box are outside the audit
altogether. **The audit's *scope* drift leaves no artifact** — which is exactly
the property in §1, applied to the guard rather than to the thing guarded.

Two candidate directions, neither ruled on here:

1. **Make the boundary visible in the output.** Have the row name its
   denominator against something observed rather than declared — e.g. compare the
   registry against `launchctl list`'s pogo jobs and report the difference as a
   fourth state. This is cheap and it converts a silent exclusion into a stated
   one. It does not extend coverage.
2. **Extend the registry**, which requires a Go-side render for each job and
   inherits mg-a03d's objection for anything the deploy itself installs.

Direction 1 is the one that matches this document's own rule — a guard's job is
first to make the state *sayable*. It is not a substitute for direction 2.
**Direction 1 shipped (mg-7a20); direction 2 did not.** See the head of this
section.

**The same defect recurs one level up again, in `doctor` itself.** There is no
check registry: `pogo doctor --check` is a straight-line block of `pass`/`warn`/
`fail` closure calls inline in `cmd/pogo/main.go:3149-3602`, and a new check is
added by hand-editing a block into it. Its `Long:` help text at
`cmd/pogo/main.go:3094-3103` is a *separately* hand-maintained enumeration of
what the mode verifies — and it lists **9 bullets for 17 emitted rows**, missing
`$POGO_HOME version control`, `consumer source liveness`, `audit successors`,
`memory index parity`, and `memory index staleness`. A reader consulting the
documented scope of the checklist gets a wrong answer, and nothing emits a
discrepancy. Any declared-vs-actual pair that is maintained by hand is a branch-2
site; this one has no guard.

**The launchd half is decided and shipped (mg-7a20); `doctor`'s own check
registry is not.** Related open items: mg-b9e7
(nothing re-asserts an installed plist against the shipped one; the detector for
that is itself subject to it), mg-8dcb, mg-3bb3, mg-08af.

## 9. What this document is NOT

- **Not the deploy fix.** mg-0d70's shared acceptance — induce a sync failure at
  the 03:00 fire, observe a successful deploy later the same night, record which
  fire carried it — belongs to the deploy tickets and their two install paths. It
  is named here only so this design is not read as replacing it.
- **Not a licence to build all three branches at once.** A site picks the branch
  its ownership forces. Building the wrong branch at a site is the failure this
  design exists to prevent.

## 10. Routing

architect is SME and wrote §§1–3 and 5–7. pm-pogo raised branch 3 and the
log-on-pass requirement and holds SME on the deploy and pogod halves. §4 and §8
were added on landing (mg-57c0). Route findings to both.
