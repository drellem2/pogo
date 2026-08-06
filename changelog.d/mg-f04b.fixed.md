- **What pogo ships stopped naming one machine's fleet: the shipped prompts, the
  watcher defaults and the deploy script no longer address agents and repos that
  exist only on the author's box (mg-f04b).** New `[agents] sme` config with an
  empty default; `[gh_teardown] notify_to` and `POGO_DEPLOY_ALERT_TO` now default
  to the coordinator; `internal/ghintake.DefaultRepos` is empty.

  The test was never "does the string `pm-pogo` appear" — it is *would a fresh
  install on someone else's machine be wrong or confused by this*. Five hits
  failed that test, and they share one failure mode: **every one of them is a
  mail target, and mail to a nonexistent agent succeeds.** `mg mail send` files a
  message for an unrecognized recipient into a new maildir rather than refusing,
  so a consumer's daemon reported delivery, the delivery was real, and nobody
  ever read it. There is no instrument on which that is distinguishable from a
  working fleet.

  - **`polecat-triage.md` told every triage worker to `mg mail send pm-pogo`**,
    then to *"wait for the reply — this consult is synchronous"*, hold up to two
    hours, and **not finalize without PM input**. On any other install that is a
    triage workflow that stalls by design, on a reply that cannot arrive. It is
    now gated on `{{if .SME}}`: configured, it addresses that name; unconfigured,
    the step is absent and the recommendation packet carries
    `"sme_consulted": false` — a stated absence rather than a silent gap.
  - **`[gh_teardown] notify_to` defaulted to `pm-pogo`** while its three sibling
    watchers (ackwatch, deafwatch, ghintake) all defaulted to the coordinator.
    The rationale in mg-b586 was *a fleet mailbox, not `human`* — which the
    coordinator satisfies. A deployment whose PM owns the gh-issue workflow names
    it in config and gets the old behavior back.
  - **`ghintake.DefaultRepos` named `drellem2/pogo` and `drellem2/macguffin`** as
    the fallback watch list. Invisible on the host that wrote it, because the
    poller state directory always answered first; on an install with neither
    `[gh_intake] repos` nor poller state, pogod reconciled *a stranger's issue
    tracker* against local work items and mailed the coordinator a wall of
    findings about repos its operator has nothing to do with. Now empty, and
    `pogo check-intake` says `watch list is EMPTY … nothing was examined` rather
    than rendering an empty scan identically to a clean one.
  - **The modal watcher routed rate-limit dismissals to `pm-pogo`, or to
    `pm-onethird` when the agent id contained "onethird"** — a substring
    heuristic over two agents from one fleet. Now the coordinator, which is also
    the agent that can chase the in-flight work the notice describes.
  - **`pogo-deploy.sh` defaulted `POGO_DEPLOY_ALERT_TO` to `pm-pogo`**, so the
    first alert of a failed overnight deploy went to a void. Now the coordinator;
    `human` is copied on a RED either way, as before.

  **Not changed, deliberately.** Historical narrative that names an agent as
  *evidence* — `CHANGELOG.md`, `docs/investigations/`, "pm-pogo's ruling from
  mg-0d70" in a rationale comment — is a record of what happened and stays. So do
  clearly-marked examples. The line is whether a fresh install would be told to
  **act** on the name.

  **The guard.** `TestShippedPromptsNameNoPersonalFleetAgent` walks the embedded
  corpus and fails on `pm-<name>`, `/Users/<someone>/`, or a one-machine project
  name, with no exemption list — prompts that must show a PM write `pm-<project>`,
  visibly a placeholder. A judgement call per line is what let this reach fifteen
  sites.

  **Operators upgrading:** two defaults changed under you. `[agents] sme =
  "pm-<yours>"` restores the triage consult; `[gh_teardown] notify_to` restores
  the old teardown recipient. Both are one config line, and both are documented
  in [CONFIGURATION.md](../docs/CONFIGURATION.md).
