- **A spawn whose origin fetch failed reported no warning in `--json`, though
  its worktree ignored the target (mg-7c1d).** When origin is unusable (no
  remote, a failed or timed-out fetch, or no default branch on it), the worktree
  is based on local HEAD. The CLI's `base:` line showed that, but `base.warning`
  was empty. It now reads `target <X> not honoured — origin was unusable …, so
  the worktree is based on local HEAD`. `docs/release-process.md` no longer
  calls mg-bb0d unmerged.
