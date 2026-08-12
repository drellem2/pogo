- **The indexer stops trying to read what is not a file — a unix socket in an
  indexed tree no longer logs one ERROR per rebuild, forever (mg-f32a).**
  `indexRec` (`internal/search/search_index.go`) treated every non-directory as
  indexable with no mode check, so a bound socket entered `proj.Paths` and the
  zoekt build `os.ReadFile`'d it and logged `Error reading file …` at ERROR.
  The repeat is the part that matters: `FileHashes` is populated **only on read
  success**, so the mtime shortcut that skips unchanged files can never fire for
  a node whose read always fails. Nothing retries with backoff and no cache ever
  retires it — it is a fresh ERROR on every pass, bounded only by the 2-minute
  default index interval (720/socket/day). Reproduced: two passes over a tree
  with one bound socket emitted exactly two ERROR lines, with the socket present
  in `Paths` and absent from `FileHashes`.

  The fix skips the node at the walk, before it enters `files`/`proj.Paths` — so
  no read is attempted, no ERROR is logged, the `Indexed N files` census is
  honest, and the `maxFilesPerTree` budget is not spent on nodes that cannot be
  indexed. Demoting or deduplicating the log line was rejected: it hides the
  symptom while leaving the socket in the census and the budget, and it leaves
  the FIFO hang below entirely unaddressed.

  **Sockets are the instance; the class is every node that does not resolve to a
  regular file.** FIFOs, device nodes, dangling symlinks and symlinks-to-
  directories all take the identical path to the identical permanent ERROR, and
  one of them is worse than noise: `os.ReadFile` on a **FIFO with no writer
  blocks**, so a named pipe anywhere in an indexed tree wedges the walk
  indefinitely rather than merely narrating about it.

  **The predicate stats THROUGH the link, and the obvious form is wrong.** The
  walk's own `fileInfo` comes from `os.Lstat`, for which a symlink is never
  regular — so `!fileInfo.Mode().IsRegular()` would silently stop indexing every
  symlinked source file, which is indexed today. That is measured, not reasoned:
  with the naive form installed, `TestSymlinkToRegularFileIsStillIndexed` goes
  red with `link.go` gone from both `Paths` and `FileHashes`. The shipped check
  falls back to `os.Stat` only when the Lstat mode is not plainly regular, so the
  common case pays no extra syscall, symlinks-to-files keep working, and dangling
  links — on which `os.Stat` errors — are caught as well.

  Two log calls on the same failing path are repaired alongside it: `Error
  getting absolute path - file may not exist` and `Error reading file` both
  passed a bare trailing value where hclog wants key/value pairs, so the path
  landed in a synthetic `EXTRA_VALUE_AT_END` field that nothing consuming the log
  can query. After the spurious firings stop, the remaining legitimate ones are
  now readable. This is the same repair `mg-f3ae` made to the `Reindexing` line.
  The **other 23 malformed hclog calls in `internal/search` are deliberately not
  swept here** — they are `mg-6698`, split out so a surgical fix does not wait on
  a 25-call sweep.

  `mg-6698` landed first, and it shipped a guard —
  `TestHclogCallsInThisPackageArePaired` — that fails on any unpaired hclog call
  in this package **and** on any exemption that no longer matches one. It held
  these two call sites as named exemptions precisely because this branch was
  rewriting the same statements. Pairing them here made both entries stale, so
  `hclogPairExemptions` is now empty: the allowlist recorded a wait, and the wait
  is over.

  **What this does not fix, found in review:** the identical perpetual-ERROR
  mechanism survives for a **regular** file whose read fails — a mode-0000 file
  reproduces the gh#136 signature verbatim, present in `Paths`, absent from
  `FileHashes`, one fresh ERROR per rebuild. A node-type predicate cannot reach
  it, because the file *is* regular; the remedy has to be different in kind and
  is a design choice rather than an oversight. Filed as `mg-9c6b`.

  One sizing correction for anyone reading the originating report: the socket
  error is **1.14%** of that log (5,606 / 489,679 lines), not its bulk. The bulk
  was pre-gh#111 indexer narration, fixed by `f0b2f7a` on 2026-08-07 and already
  in the reporter's binary — the 74MB is a historical artifact nothing reclaims,
  not a current write rate. The unbounded-growth half of the report is
  gh#104/mg-e789 and is not addressed here. Refs drellem2/pogo#136.
