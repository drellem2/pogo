- **Scheduler entries now carry a real `kind` discriminator instead of
  smuggling the type into the id prefix (mg-fa53).** A `mail-check` loop was
  distinguishable from a crew `sweep` or a `gate-lift` reminder only by
  string-matching the schedule id against `MailCheckIDPrefix` — a typed
  distinction hidden in a naming convention that nothing type-checked. That is
  why the mg-de08 reap was quiet: the crew sweeps survived the mail-check GC
  only because of how they were NAMED, not because anything knew they were a
  different kind of thing. `Entry` now has a `ScheduleKind` field
  (`mail-check` | `sweep` | `gate-lift` | `other`), the stale-schedule reap
  (`reapMailChecks`) and `HasMailCheck` key on it structurally, and a rename no
  longer silently changes what a schedule IS. The migration is
  backward-compatible: a `schedules.json` written before this field carries no
  `kind`, so `applyDefaults` infers one from the id prefix on load — every live
  `mail-check-*`, `sweep-morning-*`, `sweep-evening-*`, and `gate-lift-*` entry
  keeps working with the correct kind and none is dropped or disabled
  (proven by `TestLegacyNoKindScheduleMigration`).
