- **The nightly prompt install leaves a record naming which prompts moved and
  from what revision — it always ran, and nothing it produced could say so
  (mg-b6bd).** Every restart of pogod installs the prompts the new binary
  embeds: `cmd/pogod/main.go` calls `agent.InstallPrompts` before it auto-starts
  any crew, and has since 40b60c1 (2026-04-27). The nightly deploy kickstarts
  pogod, so act 3 of prompt activation is automatic and its cadence is nightly.
  What it emitted was a single counts-only line to `pogod.log`:

      2026/08/13 03:01:29 pogod: refreshed prompts (installed=0 updated=9 skipped=0 conflicts=0)

  That line names no file, names no revision, and lands in pogod's inherited
  stderr, which is on no agent's schedule. From it, no reader can answer the
  only question anyone ever asks — *does agent X's live prompt carry commit Y*.
  And on the far more common boot where every prompt was already current, it
  printed **nothing at all**, so "act 3 verified all nine prompts" and "act 3
  never ran" produced identical evidence.

  Now: `pogod: refreshed prompts from <rev> (…)` plus a line naming every
  installed and every updated file, and a durable `prompt_refresh` event in
  `~/.pogo/events.log` carrying `revision`, `installed[]`, `updated[]`,
  `skipped[]`, `conflicts[]`, `changed` and the install's own timestamp —
  emitted on **every** boot including the all-skipped one, because "all nine
  were current at revision R as of T" is an answer and silence is not. Read it
  with `pogo events list --type=prompt_refresh --json`. `scripts/pogo-self-deploy`
  reads the record after the restart and puts it in the deploy transcript,
  reporting loudly when there is no record rather than passing over it. Named
  in full, never a top-N: a report that drops some identities reads as
  completeness while not being it, which is the defect one size down.

  **The ticket's premise was wrong and is recorded here so it is not re-filed.**
  mg-b6bd was filed on `grep -c 'prompt install' scripts/pogo-self-deploy` → 0,
  concluding that act 3 "is in no script, no schedule, and no runbook". The grep
  is accurate; the conclusion is not. The deploy never shells out to the CLI —
  it restarts the daemon that does the install in-process. Measured on this box:
  `pogod.log` records the refresh at 03:01:29 local on 2026-08-13, and
  `~/.pogo/agents/crew/doctor.md` has an mtime of exactly `Aug 13 03:01`. The
  follow-up's "intermittent, unattributable" reading is the same evidence gap
  seen from the other side: the install is nightly and was never unattributable,
  only unrecorded. `doctor.md` was stale at 03:32 because `d27ecc1` merged at
  03:44 local — 43 minutes *after* that night's install — which is ordinary
  24-hour latency, not a missing owner. The script now states this where the
  grep was run, and `pogo doctor --check`'s prompt-drift remedy names the owner
  and the cadence instead of only the manual command.

  **What this does not close, stated rather than implied.** (1) Act 4 for
  `auto_start = false` agents is unchanged: the deploy's existing lost-mail-check
  alert names the agent and the `pogo agent start` line, and that remains the
  only thing that brings a doctor back after a bounce. (2) The deploy-script half
  of this change is subject to the same defect it repairs — launchd runs
  `~/.pogo/bin/pogo-deploy.sh`, a static copy the nightly never refreshes, so it
  is inert until someone runs `pogo service install-deploy`. Measured
  2026-08-13: the installed runner is dated Aug 10 while `scripts/pogo-self-deploy`
  on main last changed Aug 12, so it was **already two days stale before this
  change**. The pogod half ships with the binary and needs no such step.

  Guarded by `TestPromptRefreshEvent_*` (six cases, including the no-op boot,
  the failed refresh that must not read as a clean one, and the empty-array wire
  shape the shell reader depends on), `TestPromptRefreshLogLines_NamesTheFilesAndTheRevision`,
  and sixteen cases in `scripts/pogo-self-deploy_test.sh` covering the report and
  its wiring. Verified live in a `POGO_HOME` sandbox across two boots: the first
  recorded nine installs by name, the second logged nothing and still recorded
  all nine as skipped at the same revision — the exact pair of boots that was
  previously indistinguishable.
