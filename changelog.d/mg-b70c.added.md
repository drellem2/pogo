- **`pogo check-mailloops` now says whether the agents it does NOT judge have a
  mail loop anyway (mg-b70c).** The report named its excluded agents and why
  (mg-0db1), but could not say whether any of them could actually be woken — on
  this host every one of 12 running agents had a mail-check while the report
  only established it for the judged few. Each unjudged entry now carries an
  observation from the same scheduler lookup diagnose uses: the text render tags
  each line `[has a mail loop]` / `[no mail loop registered]` and adds a summary
  ("All 3 not judged have a mail-check schedule anyway."), and `--json` gains
  `"mail_loop": "present" | "absent"` per entry. **Informational only:** the
  eligibility boundary (mg-738f's cry-wolf guarantee) and `Actionable()` are
  unchanged, so an excluded agent with no loop is never RED and never moves the
  exit status. An entry without `mail_loop` (a pogod older than this client)
  renders as NOT REPORTED, never as "no loop".
