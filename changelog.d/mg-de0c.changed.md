- **The nightly will NOT reconcile the launchd plist drift it reports, and that
  is now a decision rather than an open question (mg-de0c).** mg-b9e7 shipped the
  detection half — `pogo check-activation` runs every night from the binary the
  deploy just built and verified from `main`, and a drift escalates to mail — and
  explicitly left reconciliation open. It is closed as **no**, per job, with the
  blast radius measured rather than argued.

  The shape that looked plausible was *reconcile the jobs whose install is inert*.
  Measured against the four installers, **that subset is empty of every job worth
  reconciling, and non-empty only for the one job that must not be touched**:

  - **`pogo service install`** reaches the orchestrated bounce every time a
    reconciler would fire — its skip-when-unchanged gate (`canSkipInstall`) is
    false by construction in the drift case. A deploy that has already drained the
    fleet, restarted pogod, run `do_prove` and checked the mail loops would bounce
    it a second time with none of those post-checks. And on the reference box the
    drift is the legacy `POGO_HOME=$HOME` that mg-3dc3 normalised to `~/.pogo`:
    reconciling it moves the running daemon's **state root** at 03:00, unattended.
    The audit reports that in the same words as a moved log path, because its
    predicate is byte equality, which cannot classify *what* drifted.
  - **`install-recovery`** bootstraps a `RunAtLoad=true` job, so it *runs*
    `pogo-recovery.sh`, which issues `launchctl kickstart -k com.pogo.daemon`
    whenever a `.req` sits in the recovery queue — a conditional daemon bounce
    whose condition nothing in the nightly reads — and leaves the box without a
    tier-3 watchdog between its `bootout` and its `bootstrap`.
  - **`install-deploy`** boots out the job the nightly runs inside. Measured with
    an isolated LaunchAgent: the process dies **at** the `launchctl bootout` line,
    so `InstallDeploy`'s own `bootstrap` never runs. The plist is left
    byte-correct on disk and the job left **unloaded** — the nightly deletes
    itself from launchd until the next login, and nothing survives to report it.
  - **`com.pogo.reclaim`** has the one genuinely inert install (`RunAtLoad=false`,
    deliberately, so reinstalling cannot delete a multi-gigabyte cache) — and it
    is **absent**, not drifted. The audit says in its own output that it cannot
    tell a job deliberately left uninstalled from one whose install never ran.

  **The dodge that looks safest is the worst option.** "Write the plist, skip the
  reload" avoids every `launchctl` hazard above — and the drift predicate is bytes
  on disk, which no verdict crosses with whether launchd has the job *loaded*. It
  would flip the nightly's verdict `DRIFTED → ACTIVATED` and its exit status
  `1 → 0` while launchd kept running the old job, for however many weeks until the
  next login. That is mg-8f7e exactly — an artifact that merged, was never
  activated, and read green — manufactured by its own remedy.

  The refusal is enforced, not just written: `scripts/pogo-self-deploy_test.sh`
  runs `report_activation` against a stubbed CLI and a stubbed `launchctl` that
  **record their argv**, and asserts a `DRIFTED` box produces exactly one CLI
  invocation (`check-activation`) and zero `launchctl` calls. The witness is armed
  in the same test — the stubs are shown catching a real `service install-recovery`
  and a real `launchctl bootout` — because a recorder wired to the wrong path
  observes nothing and reads identically to a script that did nothing. Injecting a
  one-`install`-per-drifted-job reconciler into a copy of the script fails the
  guard and names the three installs it ran.

  Full argument, per-job measurements, and the four conditions that would reopen
  the question: [docs/design/launchd-reconcile-decision.md](../docs/design/launchd-reconcile-decision.md).
