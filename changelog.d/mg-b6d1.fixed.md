- **The PM open-PR pass could not return `landed` — for any pull request, ever
  — so the one disposition it was built to catch was unreachable (mg-b6d1).**
  `docs/pm-open-pr-pass.md` and all four PM configs prescribed
  `git merge-base --is-ancestor <headRefOid> origin/main` as the did-it-land
  test. The refinery *rebases* a branch onto the target before merging it
  (`internal/refinery/merge.go:508`, then `merge --ff-only`), so the landed
  commits carry new SHAs and the PR's original head is never an ancestor of
  `main` afterwards. The refinery already says exactly this in the comment it
  leaves when it closes a PR. The predicate was therefore pinned to
  `not landed`, and every open PR fell into the bottom half of the disposition
  table: a landed-but-open PR read as *in flight* and sat open indefinitely, or
  as *stranded* and drew a carrier work item for work already on `main`.

  **Three independent instances in one evening, in three different spellings.**
  pm-onethird came within one step of re-submitting work that had merged 39
  minutes earlier; pm-pogo verified the predicate against `merge.go` before
  filing; the coordinator's own `git rev-list --count main..<branch>` sweep
  reported 65 "stranded" branches across three repos that were almost entirely
  successful merges, caught only because the number was implausible.

  The predicate is now `git cherry origin/main FETCH_HEAD` over
  `pull/<number>/head` — a patch-id comparison, which survives the rebase and
  works for fork PRs whose head branch is not on origin. Two other candidates
  were measured and rejected: `gh pr view --json state,mergedAt` reports
  `mergedAt: null` for refinery-landed work (the refinery merges outside GitHub
  and then closes the PR — five for five on the most recent closed pogo PRs),
  and is `null` for every *open* PR by construction anyway; the merge commit's
  SHA does work but the pass does not have it.

  **The fix is checked in both directions, because "cannot return the other
  answer" is the defect's own shape.** Against five branches independently
  confirmed merged by commit subject on `main`, the old test says `not landed`
  five times and the new one says `landed` five times. Against two branches with
  genuinely unlanded commits — one never merged, one where three of four commits
  landed — both tests say `not landed`. A test fed only unmerged branches would
  have passed the broken predicate, so that positive control is now written into
  the doc as a requirement for any future change to it.

  **Writing the replacement surfaced the same defect one layer down, in the
  remedy itself.** The obvious shell form —
  `git cherry origin/main FETCH_HEAD | grep -q '^+' && echo "not landed" || echo landed`
  — answers `landed` whenever git *fails*, because a failed git prints nothing
  and "no output" is how the predicate spells landed. Measured: against a ref
  git cannot resolve, the one-liner returns `landed`, which is the one answer
  that authorises closing a live PR. The doc and the configs now check git's
  exit status separately and carry a third outcome, `UNJUDGED`, recorded under
  *Gaps I'm watching* like a `gh` outage — folding it into either real answer
  reintroduces the bug in one direction or the other.

  The doc also now states the replacement's blind spot rather than implying
  accuracy nobody measured: `git cherry` compares patch-ids, so a squash-merge
  or a conflict-resolved rebase reports `not landed` for content that did land.
  The refinery cannot produce the conflict case — a conflicting rebase fails the
  MR instead of merging through it (mg-eac0) — leaving human-side rebases and
  the GitHub squash button as the residual exposure, both erring toward "file a
  carrier" rather than toward closing a live PR.
