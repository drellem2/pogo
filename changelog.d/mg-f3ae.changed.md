- **pogod's log stops being 98.8% narration of index passes that did nothing,
  and an operator can now set the level (`POGO_LOG_LEVEL`) — gh #111
  (mg-f3ae).** Measured on `~/Library/Logs/pogo/pogod.log` over 11 full days
  (2026-07-21..07-31, 31 registered projects), three lines were 45,755 of
  46,298 hclog lines:

      Indexed N files for <root>                      15,420
      Reindexing <root>                               15,304
      No content changes detected, skipping ...       15,031
      ------------------------------------------------------
                                                      45,755 / 46,298 = 98.8%

  All three were emitted once per project per re-index tick whether or not the
  pass had anything to do, and there was no way to turn them down: no log-level
  key existed anywhere and pogod exposed only `-bind` and `-port`.

  **A level SPLIT, not a flat demotion.** `Reindexing` and `No content changes
  detected` drop to Debug unconditionally. `Indexed N files for` drops to Debug
  only when the pass found no content change, and stays at **Info** when it
  actually rebuilt the project's zoekt index — the branch is on `contentChanged`,
  which `serializeProjectIndex` had already computed a few lines above the log
  site. Demoting all three would have removed the same volume and left the
  indexer completely silent at the default level, so a daemon that had stopped
  indexing would read exactly like an idle one: the mg-c3f0 "correct warning
  nobody hears" failure in miniature. `TestReindexWithRealChangesStaysAtInfo`
  is the control for that, and it fails against the flat demotion.

  **`POGO_LOG_LEVEL`** is read once at logger construction
  (`internal/logging`) and applied to all three loggers that hardcoded
  `hclog.Info` — `internal/search`, `internal/diagnostics`, `internal/project`.
  hclog's names, case- and whitespace-insensitive: `trace` `debug` `info`
  `warn` `error` `off`.

  An environment variable rather than a `config.toml` key, deliberately: it
  works for a daemon started any way at all, **including under launchd, where
  nothing sources a shell**, and it needs no reload semantics because it is
  read at construction. A config key with proper reload behaviour is a
  reasonable thing to want on top of this and is tracked separately (mg-44d6);
  it is not folded in here.

  Unparseable input falls back to `info` rather than failing the process.
  `hclog.LevelFromString` answers `NoLevel` for both the empty string and
  garbage, and `NoLevel` is **not** a quiet threshold — a logger built from it
  drops everything — so passing it through would have turned a typo in an
  environment variable into a silent daemon, which under launchd is not even
  reliably visible. `TestLevelDrivesARealLogger` builds a real logger from each
  parse result, because `NoLevel` only misbehaves once a logger exists.

  **The git-tree-hash warning keeps its level and stops repeating.** `Could not
  read git tree hash for <root>: exit status 128` still logs at **Warn** — the
  level was never what was wrong with it. The first occurrence is real signal:
  the indexer cannot use its git fast path for that project and must hash every
  file. What was wrong is that the periodic re-indexer re-emitted it for the
  same repo on every tick forever. It is now once per project per pogod run.

  Deduped per **PROJECT**, not per call site. There are two sites — one on the
  save path (`serializeProjectIndex`) and one on the load path (`Load`) — and a
  repo can fail at both, so a per-site dedupe would still warn twice for one
  project. `TestGitTreeHashWarningIsOncePerProjectNotPerSite` drives one project
  through both sites plus a repeat of the first; against a per-site key with
  byte-identical message text it reports 2, want 1.

  The dedupe map is cleared by `Evict`, and `TestEvictClearsGitTreeHashDedupe`
  asserts the map is empty afterwards. Without that it would be a slow leak in
  exactly the process the dedupe exists for: it lives for the whole process
  lifetime, pogod runs for weeks, and an entry per root ever seen with no
  matching removal grows without bound. Tying it to eviction means it cannot
  outgrow `g.projects`.

  **One adjacent bug fixed in the line being touched.** `g.logger.Info(
  "Reindexing ", path)` passed a single trailing argument to an hclog varargs
  call, which hclog cannot pair with a key, so live output was
  `{"@message":"Reindexing ","EXTRA_VALUE_AT_END":"<path>"}` — the project root,
  the only useful thing on the line, was in neither the message nor a queryable
  field. Now `("Reindexing", "root", path)`.

  **Nothing is suppressed by message matching.** The demotions are edits to the
  call sites themselves, so — satisfying af0f444's house rule by construction
  rather than by care — a new variant of any of these lines is a new call site
  and surfaces at whatever level it is written with; there is no prefix or
  regex filter today that could swallow it tomorrow. The test helpers match
  complete messages for the same reason, which is not pedantry: the first draft
  of `TestReindexingLineCarriesRootAsAField` matched three records, because
  `t.TempDir()` names the directory after the test and that test's own name
  contains "Reindexing".

  **What an operator sees changes only for the no-op case.** With
  `POGO_LOG_LEVEL` unset the level is `hclog.Info`, exactly what every logger
  hardcoded before — `TestDefaultLevelMatchesThePreviousHardcodedValue` pins
  that. No test anywhere asserted on the three messages before this change
  (`internal/` and `cmd/` were grepped), so nothing existing broke; these are
  the first.

  **The reporter's 56,000 lines/day was a pre-backoff figure** and is not the
  current volume: all five line numbers they cited match commit 39763bd
  (2026-03-25), which predates the mg-1236 backoff scheduler (eb4455d,
  2026-07-03). A current build logs ~4k/day for the same host profile. Backoff
  cut the volume roughly 10x and left both the 98.8% ratio and the ask
  untouched.

  Their option 3 — one summary line per indexing cycle — was considered and
  passed over: producing that aggregate means threading a counter from the
  async per-project write shards back to the tick loop across two package
  boundaries, and the result still logs at Info forever, trading real
  complexity for a smaller version of the same problem.
