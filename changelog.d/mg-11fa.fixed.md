`pogo gc --list-preserved` no longer tells the reader that a retained worktree
is the only copy of its untracked CONTENT, nor that an unconcluded work item is
"in-flight". Both were claims the report had not established, and both were
acted on: an audit of the 24 retained dirty worktrees built a fleet-wide "113
files of authored work at risk" framing on the first sentence, and read three
trees as protected by active work on the second — while all three of those work
items sat `available` and blocked with no process running.

The untracked header now scopes its claim to the git objects and names the
`cmp` against the upstream path that settles the content question; of one
tree's seven untracked paths, three turned out byte-identical to `origin/main`.
The work-item column now prints the status mg actually reported. That needed
the not-concluded statuses split apart — `available`, `claimed`, `pending` and
`shelved` were one state whose name was "in-flight", and `shelved` did not even
reach it, falling through to "unknown" for all 205 shelved items on this
machine. Every deletion decision goes through `Concluded()` and is unchanged.
