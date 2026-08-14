# Should the nightly RECONCILE the launchd plist drift it reports?

**Decision: NO. The nightly stays report-only, on all four managed jobs, and the
refusal is per job with the blast radius measured rather than argued.** (mg-de0c,
2026-08-14. Detection is mg-b9e7 and is not re-opened here.)

This is a recorded refusal, which is a result. The thing that makes it
survivable is already shipped: `pogo check-activation` runs every night from the
binary the deploy just built and verified from `main`, and a drift escalates to
mail (mg-b9e7). A human still acts. That is the outcome, deliberately.

---

## 1. What was asked

mg-b9e7 closed detection and left reconciliation open in a sentence. The
plausible shape it named was a per-job policy: *reconcile the jobs whose install
is inert, refuse the ones that bounce a daemon or rewrite a live schedule, never
auto-install an absent job.*

That shape was tested against the four installers. **The "inert install"
category is empty of the jobs we would want to reconcile, and non-empty only for
the one job we must not.** The policy does not carve out a safe subset; it
carves out the empty set.

## 2. Where this sits in the drift-guards framework

[drift-guards-design.md §2](drift-guards-design.md) forces the choice of guard
by ownership, and gives RE-ASSERT three preconditions: the site **owns the write
path**, the write is **idempotent**, and it **boots often enough** for
re-assertion to be timely. §2 records that the deploy plist failed all three,
"which is why it sat wrong for five days", and concludes: *when re-assertion is
unavailable, comparison is the only option left.*

The nightly changes two of those three. It is an actor that can run the
installer (ownership of the write path, by proxy), and it runs every night
(cadence). It does not change the third. **Idempotence is the precondition that
still fails, and it fails at every job.** The rest of this document is that
claim, per job, measured.

> §2's own trap applies directly here: *"Re-assertion feels safer because it
> FIXES rather than merely REPORTS, so it gets chosen at sites that cannot
> sustain it."*

## 3. The four jobs

Measured on this box, 2026-08-14, with a `pogo` built from `polecat-pde0c`
(= `main` at 9977cb1):

```
activation: DRIFTED — 2 of 4 managed launchd job(s) disagree
  DRIFT    com.pogo.daemon    — differs in keys other than its schedule
  DRIFT    com.pogo.recovery  — differs in keys other than its schedule
  OK       com.pogo.deploy    — matches this build (03:00, 04:00, 05:00)
  ABSENT   com.pogo.reclaim   — no plist at ~/Library/LaunchAgents/
```

### 3.1 `com.pogo.daemon` — `pogo service install` — REFUSE

Two independent reasons, and the second is the one that was not in the ticket.

**It bounces the daemon.** `installLaunchd` has a fast path
(`canSkipInstall`) that no-ops when the plist already matches *and* launchd has
the label with a PID. In the drift case that gate is false by construction, so
an auto-reconciler reaches the orchestrated sequence every time: quiesce crew,
stop pogod, unload, load, `kickstart -k`, poll `/health` for 10s. A deploy that
has already drained the fleet, restarted pogod once, run `do_prove` and checked
the mail loops would then restart it a second time — and the second bounce has
none of those post-checks. (`internal/service/service.go:394`.)

**The drift on this box is a state-root migration, not a plist refresh.** The
whole diff is:

```diff
         <key>POGO_HOME</key>
-        <string>/Users/daniel</string>
+        <string>/Users/daniel/.pogo</string>
         <key>POGO_PLUGIN_PATH</key>
-        <string>/Users/daniel/plugin</string>
+        <string>/Users/daniel/.pogo/plugin</string>
```

The installed plist carries the legacy `POGO_HOME=$HOME` that mg-3dc3 normalised
to `~/.pogo`. Reconciling it moves the running daemon's entire state root at
03:00, unattended, on a nightly whose next step is to declare the deploy
successful. The audit renders that as one line reading *"differs in keys other
than its schedule"* — which is true, correctly worded, and gives an automatic
reconciler no way to tell it apart from a moved log path. **An auto-reconciler
would have to classify the drift before acting on it, and the audit's predicate
is byte equality, which does not classify.**

### 3.2 `com.pogo.recovery` — `pogo service install-recovery` — REFUSE

`InstallRecovery` is an unconditional `launchctl bootout` followed by
`bootstrap` — no skip-when-unchanged path (`internal/service/recovery.go:229`).
The plist has `RunAtLoad=true`, so the bootstrap immediately runs
`pogo-recovery.sh`, which drains `$POGO_RECOVERY_DIR/queue` and issues
`launchctl kickstart -k gui/$UID/com.pogo.daemon` if **any** `.req` file is
present. So reinstalling recovery is a conditional daemon bounce whose condition
is a directory the reconciler does not read.

It also opens a window with no tier-3 watchdog loaded, between the `bootout` and
the `bootstrap`, and if the `bootstrap` fails the window does not close. The
recovery agent exists precisely to revive a wedged pogod; removing it
unattended, nightly, to fix a drift is the wrong trade.

For completeness, the drift here is benign in effect and does not change the
verdict: the installed plist lacks `POGO_RECOVERY_DIR`, and the script's
fallback `$HOME/.pogo/recovery` happens to equal the `WatchPaths` entry
`/Users/daniel/.pogo/recovery/queue`, so watched and drained dirs coincide.
Benign-by-coincidence is a reason the report is not urgent, not a reason the
remedy is safe.

### 3.3 `com.pogo.deploy` — `pogo service install-deploy` — REFUSE, MEASURED

The ticket called this "not obviously safe and definitely not obviously
testable". It is testable, it was tested, and it is worse than unsafe: **it is a
self-disarm.**

`InstallDeploy` runs `launchctl bootout gui/$UID <plist>` *before*
`launchctl bootstrap` (`internal/service/deploy.go:380`). Run from inside the
`com.pogo.deploy` job — which is where the nightly runs — that boots out the job
whose instance is executing the command.

Measured with an isolated LaunchAgent (`com.pde0c.bootouttest`, own label, own
scratch dir, torn down after) whose script logs, boots itself out, and then logs
again:

```
[00:42:38] started pid=78098
[00:42:40] about to bootout myself
                     ← "SURVIVED bootout" never written
$ launchctl list com.pde0c.bootouttest
Could not find service "com.pde0c.bootouttest" in domain for port
$ test -f <plist>  → present on disk
```

The process is killed at the `bootout` line. **`bootstrap` never runs.** The
plist is left byte-correct on disk and the job is left *unloaded*. Everything
downstream of the reconcile step in the nightly — including the step that would
have reported what just happened — never executes.

LaunchAgents are re-bootstrapped at login, so this self-heals at the next login
or reboot. On a workstation that stays logged in for weeks, the nightly deploy
is simply gone for weeks.

**What the proxy establishes, and what it does not.** `com.pogo.deploy` was not
booted out to find this; a job with its own label was. What the proxy measures
is a property of `launchctl bootout` and the `gui/$UID` domain — that it
terminates the running instance of the job it unloads, and that the terminated
process does not return to its next line — and that property does not depend on
whether the job was triggered by `RunAtLoad` or by `StartCalendarInterval`. What
it does not establish is anything about the real nightly's own signal handling
or the deploy lock: those could change what the wreckage looks like, not whether
`bootstrap` is reached. The claim is deliberately the narrow one.

### 3.4 `com.pogo.reclaim` — `pogo service install-reclaim` — REFUSE

This is the one install that *is* inert: `RunAtLoad=false`, deliberately and
with the reason recorded in the template — "an operator rerunning the installer
must not thereby delete a multi-gigabyte cache as a side effect of an install"
(`internal/service/reclaim.go:60`).

And it is the one job that must not be auto-installed, for the reason the audit
states in its own detail text: it *cannot tell a job deliberately left
uninstalled from one whose install never ran*. Auto-installing turns a human's
decision not to run a job into a job that reappears every night, and the thing
that would have to distinguish the two — a record that somebody chose — does not
exist.

So the safe-install set and the want-to-reconcile set are disjoint. That is the
finding, not a coincidence: the installs are non-inert exactly where the job is
load-bearing enough to be worth reconciling.

## 4. The remedy is subject to the defect it remedies

mg-8f7e's defect: an artifact merges, is never activated, and is inert while
everything reads green. Enumerating how a reconciler reproduces it:

**A "write the plist, don't reload" reconciler is worse than no reconciler.**
It is the obvious way to dodge §3.3 — skip `launchctl`, just fix the file. But
the audit's drift predicate is **byte equality of the file on disk**
(`internal/service/launchagentaudit.go:315`), and `buildActivationReport` never
consults whether a registry job is *loaded*
(`cmd/pogo/checkactivation.go:200-228`). So a write-only reconcile flips the
verdict `DRIFTED → ACTIVATED` and the exit code `1 → 0`, while the job launchd
is actually running is still the old one — until the next login, which may be
weeks away. `classify_activation` in the nightly reads the verdict word and the
exit status; neither would say anything. **That is mg-8f7e exactly, manufactured
by its own remedy, and it would be silent in the same way for the same reason.**

The scope line does carry the tell — `Audited` is intersected with `Loaded`, so
the report says "N more examined but not loaded" — but no verdict, exit code, or
alert branch reads it. Leaning on it would be leaning on a sentence in a log
nobody is paged by.

**The self-disarm of §3.3 also reads as clean.** After it, `com.pogo.deploy`'s
plist matches this build byte-for-byte, so its row says `OK`. The only surface
that would disagree is the nightly, which is what stopped running.

## 5. What would change the answer

Recorded so this is not re-derived from scratch, and so nobody reads the refusal
as "reconciliation is impossible":

- **A reconcile that reloads without self-termination.** `com.pogo.deploy` needs
  the `bootout`/`bootstrap` pair to outlive the process issuing it — a detached
  helper, or `launchctl kickstart` semantics that do not require unloading. Until
  something reloads the job, writing the plist is a false OK (§4), so
  write-without-reload is not a partial step toward this — it is a regression.
- **A drift classifier, not a byte comparator.** §3.1 turns on the difference
  between "the log path moved" and "the state root moved". Automatic action
  needs an expectation richer than `bytes.Equal`, and building one means a second
  notion of up-to-date that can disagree with the installer's own — which
  `launchagentaudit.go:30` rejects, with reasons, for the detector.
- **An explicit "deliberately not installed" record.** §3.4 dissolves the moment
  a box can declare that a managed job is intentionally absent. Today the audit
  says, in its own output, that it cannot tell. That record does not exist and
  building it is not this decision.
- **A recovery queue precondition.** §3.2 is conditional on the queue being
  non-empty; a reconciler that read it could bound its own blast radius. It
  would still leave the watchdog-gap argument standing.

None of these are filed as work. They are the conditions under which the
question is worth asking again — deliberately, with the measurements in §3
re-taken, because every one of them is a fact about installers that can change.

## 6. What is enforced

`scripts/pogo-self-deploy_test.sh` runs `report_activation` against a stubbed
CLI that **records every argv it is invoked with**, and against a `launchctl`
stub on `PATH` that does the same, and asserts that a `DRIFTED` box produces
exactly one CLI invocation — `check-activation` — and zero `launchctl` calls.
The guard is armed in the same test: the same witness is shown catching a
synthetic `service install-recovery`, so a witness wired to the wrong path
cannot pass by observing nothing.

That is the shape this decision needs. A refusal recorded only in prose is one
edit away from being untrue with nobody the wiser — which is the same family of
defect as everything above.
