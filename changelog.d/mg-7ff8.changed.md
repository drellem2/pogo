- **The audit-successor detector is ARMED on this deployment, and the clean line
  now carries the two limits a reader has to see (mg-7ff8).** mg-28b7 shipped the
  detector inert: `[audit_successor]` names where to look and what to look at, and
  with either half missing `pogo doctor --check` reports `not configured`. Merged
  was not live, installed was not live, and configured is the state that makes it
  run. The section is now written, and three things came out of writing it that a
  green test suite could not have produced.

  **The tag policy is narrow, and the measurement says why.** Dry-run against the
  live store with only this section varied: `["independent-audit"]` examines 4 and
  reports 0; `["audit"]` examines 9 and reports 3, of which **two are false**.
  `audit` is not a loose marker here — all nine `done` items carrying it are
  titled *INDEPENDENT AUDIT of …*. It reports falsely because of a
  reference-channel mismatch: in this program a repair ticket is tagged after the
  item that was AUDITED, not after the audit, so `mg-07fd`'s repairs live in
  `mg-2f44` tagged `mg-3329-followup` and the detector sees no reference to
  `mg-07fd`. The cost of erring narrow is recorded in the config and the docs
  rather than left to be inferred from a green line: five of nine merged audits
  are not examined.

  **The clean branch stated neither limit.** Both were in the package docs, the
  config type, `docs/CONFIGURATION.md` and the warn line — and a reader of a green
  checklist row saw neither, which is the branch nearly every run takes. They are
  now one constant rendered by every branch that gives a verdict about the store,
  and `TestAuditSuccessorLine_LimitsReachEveryVerdict` fails if either drifts out
  of either branch. Verified by reintroducing the defect and watching it fail.

  **`$POGO_HOME/config.toml` is the wrong file for it.** `ConfigFilePaths` adds
  that layer only when `POGO_HOME` is set in the environment, so a section written
  there is armed from a login shell and inert under launchd or cron — `pass` in
  both cases, and only one of them means the check ran. The section lives in the
  XDG file, which is read unconditionally.

  Proved to fire after configuration, not merely to stay quiet: against a copy of
  the live store with `mg-00b3`'s two successors removed and nothing else changed,
  the armed config names `mg-00b3` and doctor still exits 0; tagging it
  `audit-clean` moves it to `1 by a recorded clean verdict` and silences the line.

  Not fixed here, and stated because it is the larger finding: **`pogo doctor
  --check` has no scheduled runner on this host** — not launchd, not `pogo
  schedule`, not the architect's deploy-verify procedure, and the `doctor` crew
  agent has `auto_start=false`. Arming the detector makes it work; it does not
  give it a reader.
