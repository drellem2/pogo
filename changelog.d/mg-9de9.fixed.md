- **The persona file pogo writes into an agent's worktree can no longer be staged
  by `git add -A`.** The Codex and Cursor providers deliver their persona by
  writing a context file into the worktree — `AGENTS.override.md` and
  `.cursor/rules/pogo-persona.mdc`. Both landed untracked and covered by no ignore
  rule, so a broad `git add -A` staged pogo's internal prompt: a dirty branch at
  best, and pogo's prompt committed into a repository you own at worst. The path is
  now appended to the worktree's `.git/info/exclude`, which is local to the clone
  and never committed, so your own `.gitignore` is left alone. It is applied again
  on every respawn, and a directory that is not a git repository, or an exclude
  file that cannot be written, is logged rather than treated as fatal — the persona
  has already been delivered by that point.
