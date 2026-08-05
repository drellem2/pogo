- **A polecat's mail-check reads the mailbox its mail is actually addressed to,
  and a mail-check pointed anywhere else is now refused at registration
  (mg-aa96).**
  The polecat templates and pogod's spawn-time auto-registration both derived
  the mail-check mailbox from the **work item id** (`mg mail list mg-<id>`),
  while the protocol has correspondents reply to `--from=$POGO_AGENT_NAME` —
  the **agent name**. The two agree only when the agent name is exactly the work
  item id minus its `mg-` prefix, so the defect was invisible on every polecat
  named that way and broken on every other one.

  On 2026-08-05 **all eight** running polecats were mismatched:

      agent    polled mailbox        agent    polled mailbox
      gd2f0 -> d2f0                  wfc99 -> fc99
      wb468 -> b468                  wfc8d -> fc8d
      o9d7b -> 9d7b                  gc23c -> c23c
      d0d70 -> 0d70                  g109  -> b4cc   <- a different work item's

  **The silence is the defect, not the derivation.** `mg mail list` on a mailbox
  nothing was ever delivered to prints `No mailbox for aa96 yet — no mail has
  ever been delivered to it` and exits **0**. That is byte-identical to a healthy
  empty inbox, so an agent polling the wrong one forever reads exactly what an
  agent with no mail reads, and no downstream check can tell them apart. A
  pm-pogo reply to gc23c sat unread ~1h; an urgent mayor correction retracting a
  false causal claim in wfc8d's own dispatch brief sat unread in `wfc8d` while
  that agent polled `fc8d`, and it would have kept building on the retracted
  premise. The mayor's hand-repointing of the eight live schedules covered the
  running fleet only — the template still prescribed the broken form, so the
  next dispatch would have reintroduced it.

  **What changed**

  - The **mailbox is the agent name** in both places that name one:
    `registerPolecatMailCheck` (pogod's spawn registration) and the step-2
    `pogo schedule` command in all six polecat templates, which now interpolate
    `$POGO_AGENT_NAME` — literally the same source as the `--from` replies come
    back on, so they cannot disagree. The schedule **id** stays keyed on the work
    item (`mail-check-mg-<id>`): it names the unit of work, is what the
    coordinator removes on stop, and is what the stale-entry sweep matches. Two
    identities, deliberately not collapsed.
  - **`Scheduler.Add` refuses a mail-check that reads another agent's mailbox.**
    `Entry.Validate` parses the `mg mail list <mailbox>` invocation out of a
    `KindMailCheck` message and requires it to canonicalize to the entry's own
    agent (`mg` strips a leading `mg-`, so the guard compares what `mg`
    compares). This is the half that matters: part 1 alone is a rule that the
    next template edit can break with no signal. Both registration paths — the
    `pogo schedule` CLI and pogod's spawn registrar — pass through this one
    chokepoint. The error names both identities and the fix.
  - Only `KindMailCheck` is policed, which is also the escape hatch: a schedule
    that genuinely means to watch someone else's inbox is not a mail-check, and
    registering it under a non-`mail-check-` id makes that intent explicit and
    reviewable instead of silent.
  - `internal/agent`'s template test asserted `mg mail list {{.Id}}` — the defect
    written down as a contract. It now asserts the inverse, across all six
    templates, and holds the schedule id on the work item so a future "fix"
    cannot collapse the two identities the other way.

  **Both halves were shown to fire.** With the guard removed, all eight live
  mismatches register clean and the refusal tests fail; with the mailbox reverted
  to the work-item form, the spawn and template tests fail. The genuinely-empty
  case, the historically-agreeing case (agent name == id minus `mg-`), a message
  naming no mailbox at all, and every non-mail-check kind are all accepted — the
  guard has to be silent on a correctly-pointed empty inbox, which is the state
  most polecats are in most of the time.

  **One behaviour change to know about.** `pogo agent wake` restores a parked
  agent's schedules through the same `Add`, so a parked *legacy* mismatched
  mail-check now fails to restore. It is logged (`wake restored N/M schedule(s)`)
  and the agent's startup contract re-registers a correct one — a visible gap
  rather than a silently useless schedule.

  **Repointing does not recover what was already misdelivered — `pogo
  check-strandedmail` finds it.** Fixing a mail-check only changes where the
  agent looks **next**. Everything already delivered to the abandoned box stays
  there, so the repoint converts a misdelivery into an **orphan**: mail exists,
  nobody reads it, nothing says so — the same shape as the bug being fixed.
  Doctor's sweep of the live fleet found one, and running the new command
  against the running fleet found the same one unaided:

      ⚠ STRANDED MAIL: 1 mailbox(es) hold unread mail that no live mail-check reads.
        (19 mail-check(s) checked against 1146 mailbox(es))

        b468 — 1 unread, for agent wb468, which polls wb468 instead
          from schedule mail-check-mg-b468
          · from doctor: mg-b468 body was extended after you were dispatched -
            re-read it before you finalise
              mg mail read b468/1785951344970787000.49622.7000 --force

  It takes every live mail-check, derives the mailbox it *would* have read under
  the old work-item form (using the same `MailCheckMailbox` parser the guard
  uses, so "where does this agent look?" has one answer in this tree), and asks
  `mg mail list --json` what is sitting there. **Corrections are the traffic most
  at risk** — sent off-cadence to an agent already working, which is what a
  scheduled poll handles worst, and both near-misses that day were retractions of
  a wrong premise to a builder mid-build — so findings name the sender and
  subject rather than a count. The other 18 mail-checks produced nothing: the
  sweep has to stay silent on a correctly-pointed empty inbox, which is what most
  polecats are most of the time.

  **It reports and never moves mail.** Re-delivering would mean `mg mail send`
  writing a new message under a new `From`; a correction whose provenance is a
  lie is worse than one that arrived late. Reading is also only half the
  recovery, and the report says so: if the intended recipient is still running,
  the original **sender** has to re-send.

  **Two defects in the printed recovery command were found by running the sweep
  and typing what it printed.** `mg mail list --json` emits a bare id that `mg
  mail read` rejects (`expected AGENT/MSG-ID format`), and mg refuses a cross-box
  read without `--force` — nobody running this report is the abandoned mailbox.
  A report whose one actionable line does not run is a report that gets written
  off, so a test now executes the printed string itself, split into argv, against
  the real `mg`. That test also pins mg's NDJSON field names: if `unread` were
  ever renamed, `Detect` would read 0 everywhere and this check would go
  permanently, cheerfully quiet — this bug's exact shape, reproduced inside the
  thing built to catch it.

  `pogo check-strandedmail` exits 1 on findings so it can gate a schedule or CI
  step, and it is in the doctor's toolbox so the sweep has a standing reader.
