`pogo agent stop` no longer destroys a polecat's uncommitted work. The exit
hook force-removed the worktree and then `os.RemoveAll`'d it regardless of
whether git had refused — so `git worktree remove`'s own dirty-tree guard was
opted out of twice over. A tree holding uncommitted changes (tracked
modifications or new untracked files) is now PRESERVED, and the coordinator is
mailed the path, what was kept, and how to reclaim it. Clean worktrees are
still reaped exactly as before. `pogo gc` keeps dirty worktrees too and reports
them; `pogo gc --apply --force` is the deliberate override.
