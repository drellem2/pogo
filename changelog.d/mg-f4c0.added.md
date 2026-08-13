- **Preserved worktrees have a consumer: `pogo gc --list-preserved` (mg-f4c0).**
  pogod preserves a polecat's worktree when it exits holding uncommitted work
  (mg-ee02), mails the coordinator naming the tree and the reclaim command, and
  puts a `worktree_preserved` event on the spine (mg-32e3, mg-d45b). All three
  halves work; that mechanism saved a 1450-line package that existed in no other
  location on the machine. **Nothing ever read the population back.** The mail
  fires once into the middle of other traffic and is never repeated; the event is
  a stream, not a list. So the trees accumulated — **six** when this was filed,
  **twenty-three** when it was fixed, across four repositories — each pinning a
  branch that cannot be deleted and blocking `gc`, and each posing a question
  ("is this uncommitted work worth rescuing?") that nobody was assigned to ask.
  The only instrument that could see the population was `ls ~/.pogo/polecats`.
  This is the same shape as a deferred half with no ticket: the safety net
  catches the work and nobody is responsible for looking in the net.

  `pogo gc --list-preserved` changes nothing, reclaims nothing and refuses
  nothing. It is rooted at the **polecats directory, not at a repo** — `pogo gc`
  takes one `--repo`, retained trees accumulate wherever the fleet works, and a
  repo-scoped listing reports a fraction of the population while looking
  complete. Per tree it gives the owner, the branch it pins, its work item and
  that item's state, how long it has gone untouched, and every uncommitted path
  split into modified and untracked. `--repo` narrows it; `--json` emits the
  record uncapped.

  **It renders no verdict, and that is the design rather than a gap.**
  `~/.pogo/polecats/p687f` held seven modified files, all `code/**/out_*.txt` —
  regenerated suite output, a pure function of repo state, reproducible in
  seconds. A reader sampled two of the seven, saw timing churn, and concluded
  "residue, safe to reclaim"; the third held three new registry entries and a
  count going 20 → 23. A classifier cheap enough to run over the whole
  population would have made that mistake **systematically** rather than once.
  Reclaiming is one already-existing command and was never the hard part;
  knowing which of twenty-three trees can safely take it is, and that is a
  question about the files. So untracked paths are **never truncated** (the tree
  is their only copy anywhere on the machine) while modified entries are capped
  with an overflow line naming the count and the command that shows the rest —
  an unannounced truncation is how a reader concludes they have seen the tree.

  **It states the blast radius of the reclaim command, per repository.** `pogo
  gc --repo=<repo> --apply --force` is repo-scoped *and* forced: it acts on every
  eligible retained tree in that repo, not on the one you inspected, which is
  precisely why a careful operator declines to run it. The listing groups by repo
  and names which trees one run would take — and which it would **not**, because
  `--force` is not the whole gate: the sweep checks the owner's ticket state
  *before* it consults the dirty guard, so an in-flight item's tree survives the
  flag entirely. That column is asserted against what `Sweep` actually does with
  the same inputs, not against a restatement of the rule, so it cannot drift into
  telling an operator `--force` will spare a tree it takes. A dirty tree whose
  owner is still **running** is reported as in use rather than retained, and
  clean trees and non-worktree orphan dirs are counted, so the listing is a
  partition of the directory rather than a selection out of it.

- **`git status` warnings on stderr stop counting as uncommitted changes
  (mg-f4c0).** `gitgc.WorktreeDirty` read git's **combined** output, and `git
  status --porcelain` writes warnings to stderr while exiting 0 — an unreadable
  subdirectory being the common one. Every such warning was counted as a dirty
  entry. Measured on `~/.pogo/polecats/ca397`, gc reported *2 uncommitted (1
  modified, 1 untracked)* for a tree holding one untracked path and a
  `warning: could not open directory …: Permission denied`; the "modified file"
  it named was the warning text. This is not a cosmetic miscount — the retention
  guard **acts** on that list, so a tree whose only status output is a warning
  reads as dirty, is preserved, pins its branch, and is never reclaimed: a silent
  producer of the very population the listing above exists to consume. It also
  mis-split the count that decides urgency, since a warning carries no `??`
  prefix and so landed in the tracked-changes half, which is the half that reads
  as recoverable. stderr is still captured and still reported when git *fails* —
  cannot-tell needs to know which way git broke — it is simply no longer part of
  the file list.
