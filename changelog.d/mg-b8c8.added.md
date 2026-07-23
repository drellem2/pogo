- **synthwatch now emits `incident_episode_cleared{kind:"auth"}` at every
  auth-episode close (mg-b8c8) — the founding case of the notification-coalescing
  arc.** The synthetic-failure-turn watcher (mg-8cdb) already coalesced its
  operator PAGES into episodes, but emitted no structured episode-close event. So
  after the pogod redeploy every incident class coalesced EXCEPT the one that
  started the arc: the 2026-07-22 auth outage, where five agents each mailed "I was
  dark 23h40m" and buried a human's answer under the swarm. This closes that gap.

  **Symmetric to the usage-limit emitter, by reuse not re-mint.** At the watcher's
  real episode-close point — where the roster and the `[opened_at, closed_at]`
  window are already in hand — it now emits the SAME event type
  (`claude.IncidentEpisodeClearedEvent`, imported and reused, mg-55b2) with the
  SAME `details` shape as `usagelimit.go` (`episode_id`, `roster`, `opened_at`,
  `closed_at`), changing only `details.kind` to `"auth"`. The pogo-reminders
  notifier (mg-e0f6) is kind-agnostic, so auth self-reports coalesce with ZERO
  reader-side change — the property that made the generic mg-55b2 contract worth
  the rename.

  **Emitted from the coordinator's close, not reconstructed from atoms.** The event
  fires from the single `clear()` close path that both the recovery-clear and the
  reap-on-departure paths funnel through, so the release-vs-recovery and standing-
  episode cases are handled uniformly — the mg-e0f6 bound applied to auth. The
  episode window is the true `[open, close]`; a build that stamped `opened_at` at
  close would leave the reader's `close + GRACE` window empty and the swarm intact.

  **The founding-case replay is the joint control, and it was run.** Beyond a unit
  test that the event fires with the byte-exact shape, the 07-22 auth burst was
  replayed against the real pogo-reminders reader: 5 "I was dark" auth reports + 2
  corrections arriving 17–26 min post-recovery collapse to ONE notification with
  the deliverable ranked above it — and, as the positive control, WITHOUT the emit
  the same reports swarm into nine individual notifications that bury the
  deliverable at the bottom.

  Additive only: the per-agent detected/cleared events and the coalesced episode
  mails are unchanged, and no `schema_version` bump. The `details` shape is the
  contract shared with mg-e0f6 — field names and nesting must not change without
  updating that reader.
