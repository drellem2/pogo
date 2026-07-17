- **`pogo doctor --check` no longer advises a remedy that silently no-ops on a
  hand-edited prompt (mg-04ab).** The drift check classified staleness by embed
  hash alone and advised `pogo agent prompt install` for every drifted prompt.
  For a canonical the operator had edited in place, that install *declines* to
  clobber the edit — it writes `<name>.md.dist` and leaves the canonical stale —
  so the remedy exited 0 and changed nothing: a false "I ran the fix." The check
  now uses the already-recorded body hash to separate two states with opposite
  remedies: a **stale** shipped template (install fixes it) versus an **edited**
  canonical (install will not overwrite it — reconcile `<name>.md` against its
  `<name>.md.dist` sidecar). It never advises a command that will decline.
