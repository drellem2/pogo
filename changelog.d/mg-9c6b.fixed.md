- **A regular file the indexer cannot read stops re-erroring forever — the
  gh#136 class that survived the node-type predicate (mg-9c6b).** `mg-f32a`
  stopped the indexer reading nodes that are not regular files. It could not
  reach a **regular** file whose read fails — a mode-`0000` file *is* regular,
  so it passes any mode check — and that file took the identical route to the
  identical permanent ERROR: `indexRec` appended the path to
  `files`/`proj.Paths` and set `FileMtimes` **before** attempting the read,
  `FileHashes` was populated only on read SUCCESS, so the mtime shortcut could
  never fire for it and the zoekt build `os.ReadFile`'d it and logged `Error
  reading file` on every rebuild, at the 2-minute default interval, in
  perpetuity. Measured against the pre-fix walk: `Paths=[real.go secret.go]`,
  `FileHashes=[real.go]`, **1** ERROR after pass one and **2** cumulative after
  pass two — pd864's signature verbatim. With the fix, `Paths=[real.go]` and
  **0** ERRORs across both passes.

  **A path now enters the census only once its content is in hand** — a cached
  hash the mtime shortcut let it reuse, or a successful read. Appending first
  and reading second *is* the gh#136 mechanism, so the reorder closes it at the
  source rather than filtering for one shape of unreadable node.

  **The drop is announced, once.** Dropping the file silently would trade a
  noisy defect for an invisible one: the file stops being searchable and
  nothing says so. So it warns — `Skipping unreadable file`, with `path` and
  `error` as fields — when it enters the unreadable set, and stays quiet while
  it keeps failing. A line per pass would be the same defect wearing a Warn
  badge instead of an ERROR one, which is what the three-pass count in
  `TestUnreadableFileIsAnnouncedOncePerFileNotOncePerPass` is for.

  **Why not the other candidate — a persisted failure marker the mtime shortcut
  consults, so the retry stops.** The retry is worth keeping and costs nothing:
  the walk re-attempts the read every pass either way, so a repaired file is
  picked up on the very next tick with no marker to invalidate. A marker needs
  an invalidation rule, and the only key this walk holds is **mtime — which a
  chmod does not change**. The marker would outlive the repair and keep the
  file out of the index until its *content* changed, which is strictly worse
  than the state it replaces. `TestRepairingPermissionsDoesNotChangeMtime` pins
  that premise, because it is a property of the filesystem rather than of this
  code and nothing else here would notice if it stopped holding.

  **The set is reconciled against each completed walk, not merely added to.**
  Forgetting a file that has become readable again is what lets a *recurrence*
  be announced — otherwise a file that goes unreadable a second time is
  silent — and it is also what bounds the set. Entries are per FILE rather than
  per project, so an add-only set is the slow leak `forgetGitTreeHashWarning`
  exists to avoid, with a far higher ceiling. Only a walk that ran to
  completion prunes; a truncated one (`errTreeTooLarge`, an unopenable
  directory) records what it found and leaves the rest alone, since forgetting
  a subtree it never reached would re-announce it next pass. `Evict` drops a
  project's entries with the project.

  **The walk fix alone is not enough, and finding that out is this ticket's own
  remedy applied to itself.** A file that was readable when the walk hashed it
  and became unreadable before the zoekt build reached it sits in `Paths` **with
  a valid cached hash** — which is exactly what makes the mtime shortcut fire,
  so every later walk skips the read and never notices, while every rebuild
  re-reads it and logs at ERROR. gh#136 reached from the other side. The build
  now drops such a path from `Paths` and from both maps, which hands the next
  walk an entry with no cached hash that it must therefore read; the census on
  disk is rewritten on that pass so a later `Load` cannot restore what the
  build just dropped. Measured: pre-fix, a second build over the same project
  logged the ERROR again; post-fix it logs nothing, and the following walk
  announces the file once.

  **What was checked for not becoming the defect it fixes:** the warning does
  not repeat per pass; the build-side ERROR does not repeat per rebuild; the
  dedupe set does not grow without bound; a recurrence is not swallowed; a
  dropped file is not lost forever; and the census does not churn — a no-op
  pass over a tree holding an unreadable file still reports *unchanged*, so the
  `mg-1236` backoff scheduler still backs off instead of rebuilding zoekt every
  tick. The `maxFilesPerTree` ceiling is unmoved: the in-loop check now counts
  the pending entry with a `+1` because the append happens after it, and a
  200-file tree with a 50-file ceiling stops at `paths=50 hashes=49 mtimes=50`
  both before and after. Refs drellem2/pogo#136.
