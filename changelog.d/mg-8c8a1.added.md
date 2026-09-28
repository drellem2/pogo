- **gitgc reclaims dead polecats' harness session temp dirs (drellem2/pogo#203,
  mg-8c8a1).** Nothing deleted Claude Code's per-workdir scratch under
  `${CLAUDE_CODE_TMPDIR:-/tmp}/claude-<uid>/<slug-of-workdir>`, so on a host
  with no `/tmp` age-out that root grew without bound. A new provider field,
  `SessionTempDir`, declares the path (Claude implements it; nil means none),
  and gitgc deletes a polecat's dir beside its worktree or orphan dir under the
  same verdict. A new phase deletes those whose polecat directory is already
  gone, gated like the orphan-dir scan: never a live polecat, never an
  unconcluded ticket. Candidates are matched by exact constructed path, so crew
  and non-pogo session dirs are never touched and an encoding drift misses
  rather than over-deletes. Deleted, not relocated; one log line per action;
  `pogo gc` without `--apply` only reports. The durable transcript store is out
  of scope, and this bounds disk, not the ripgrep OOM in the issue.
