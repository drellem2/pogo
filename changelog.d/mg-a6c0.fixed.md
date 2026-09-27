- **ackwatch and wedgewatch read only the live `events.log`, so a window
  crossing a rotation undercounted (mg-a6c0).** Sibling of mg-50b9 / mg-9d55.
  `pogo check-acks --populations` (default window: seven days) counted only the
  deliveries, completions and synthetic-failure episodes after the last
  rotation; the blackout arm's `RecentFires` and the `system_wake` suppression
  (`LastDisruption`) had the same shape; and wedgewatch's event-log fallback
  reported an agent whose last activity predated the rotation as having no entry
  in the log. All four now read the rotated files too (`events.ReadWindow`;
  wedgewatch walks newest-first and stops once every agent it needs is found,
  so the usual pass still opens one file). A populations window reaching past
  history rotation has discarded now says `WARNING: window starts before
  retained history at <ts>` (and `history_truncated`/`history_floor` under
  `--json`); the blackout arm reads such a window as blind rather than as zero
  fires.
