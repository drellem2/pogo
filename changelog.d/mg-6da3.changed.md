- **The mg-5bd2 staleness detector had a reproducible live harness with only one
  arm, which cannot tell a working detector from one hard-wired to "STALE".
  Gave it its negative control, and ran the pair against the last
  naturally-occurring stale daemon this box will produce (mg-6da3).**
  `TestLiveDaemonStaleness` now runs the live reading and a
  built-from-`origin/main` reading through the same predicate, the same clock
  and the same repo, and **asserts that they disagree**.

  **Why the one-armed version was not enough.** It read `GET /version`,
  evaluated the predicate and logged the verdict, asserting nothing. That
  establishes only that the check *emits* something. The single failure a
  positive control exists to rule out — a detector that answers "STALE"
  unconditionally — passes a one-armed run unchanged. The negative arm is
  therefore load-bearing rather than decoration, and the harness now refuses to
  run at all if it cannot construct one, instead of silently degrading back to
  the shape it replaced.

  **The negative arm's stamp is a real artifact's, not an invented SHA.** Given
  `POGO_LIVE_STALENESS_CURRENT_BIN`, it reads `vcs.revision`/`vcs.time` back out
  of a binary built from `origin/main` with `go version -m` — the exact pair a
  freshly deployed pogod would report. Without it, it falls back to
  `origin/main`'s tip and commit date read from the repo with git. Note a `go
  test` binary carries no vcs stamp, so it cannot stand in; the harness says so
  rather than reporting a blank.

  **Both arms run the production path** (`Watcher.Check` → `sampleRevision`),
  not `evaluate()` alone, so the mail, the subject, the body and the
  `revision_stale` event are exercised — with a fresh `Watcher` per arm so
  neither inherits the other's notice ratchet.

  **The reading, taken 2026-08-07 19:01 UTC+1, out of band.** The detector is
  merged to `main` but is *not* in the binary that is running, so the live pogod
  cannot report on its own staleness — it does not contain the check. The check
  was therefore built from `origin/main` to a temporary path and pointed at the
  live state. Nothing was installed, restarted or deployed.

  | arm | revision | commit | age | behind `main` | verdict |
  |---|---|---|---|---|---|
  | POSITIVE — running daemon | `d31297f493cdd757fc46654351e0a2c93e66f49b` | 2026-07-30T00:34:07Z | 8d17h | 98 | **STALE**, notice 1/4 |
  | NEGATIVE — built from `origin/main` | `d3435bada8a141d4e26375acea2d8ff7500126db` | 2026-08-07T17:47:16Z | 0h | 0 | clean, no mail, no event |

  Subject raised by the positive arm, verbatim:

  ```
  pogod is running 8-day-old code — revision d31297f4 (2026-07-30), 98 commits behind main
  ```

  The commits-behind number is 98 and not the 92 the work item quoted, and not
  the 85 in mg-5bd2's own transcript, because `origin/main` moved twice while
  this was being measured (`73757a8` → `49b0b88` → `d3435bad`, the last of them
  during the run). That is the reason each reading here is recorded with the SHA
  it was counted against: a bare commit count is only true against a named tip.

  **The restart is the corroboration, not a confound.** The daemon's
  `start_time` was `2026-08-07T18:37:28+01:00` — 24 minutes before the reading —
  and it was still on a 2026-07-30 commit. That is `revision.go`'s central claim
  observed live rather than argued: a bounce re-launches the same stale binary,
  so neither uptime nor a recent restart substitutes for reading the revision.

  This changes no production behaviour. The detector is byte-identical to the
  one mg-5bd2 shipped; what changed is the acceptance harness around it.
