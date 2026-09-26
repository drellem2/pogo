- **A crashed `git worktree add` logged its "unpopulated checkout, not probed"
  skip line on every stall-watch tick for as long as its item stayed available
  (mg-fa90, follow-up to mg-f984).** The line is now logged once per tree per
  hour; `PreservedItemReport.Unpopulated` still lists the tree on every call.
  The skip's doc now says it also reaches the spawn-time preserved-worktree
  dispatch gate, and explains why that is safe: the pre-add directory check from
  drellem2/pogo#167, plus the post-add work-item claim.
