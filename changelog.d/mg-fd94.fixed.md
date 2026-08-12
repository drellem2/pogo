- **A PR in review stops blocking the nightly redeploy forever — the drain's
  durability test asks whether ANY origin ref holds the work, not whether this
  branch's own does (mg-fd94).** `durability_of` in `scripts/pogo-self-deploy`
  asked whether `origin/<the branch checked out in this worktree>` contained
  HEAD. A review polecat is on neither ref that test covers: git refuses to check
  out a branch already checked out in another worktree, and the builder's is
  still live while its PR is in review, so the reviewer resets its **own**
  never-pushed branch to the PR head. Its commits therefore live on
  `origin/polecat-<the builder>` — a ref the test never looked at — and the drain
  reported `N commit(s) … exist only in <wt> — nothing on origin holds them`,
  whose trailing clause was provably false. The wait was unsatisfiable: nothing
  the reviewer does discharges it, so the drain burned its full budget and the
  deploy exited 7, on every night any PR happened to be in review. The predicate
  now asks the drain's actual question — *would stopping this polecat lose
  work?* — by testing whether any ref under `refs/remotes/origin/` contains HEAD
  (`for-each-ref --contains`, 21ms against 708 refs in this repo, versus a 20s
  poll), and names the carrying ref in the verdict so the clearance can be
  audited rather than trusted. The genuine committed-but-unpushed case is
  untouched, because nothing on origin holds those commits: a polecat that never
  pushed, and one that pushed and then committed again, both still hold. The
  test is seated after the two existing containment tests, so their more specific
  wordings survive — an earlier seat answers for a fresh worktree and a landed
  branch too, and collapses four distinct durable paths into one — and before the
  no-integration-ref case, so it also answers on a repo whose base is neither
  `main` nor `master` (the reporter's shows `origin/develop`), where the reviewer
  previously read `unknown` and held identically. Excluding reviewers by
  `--template` was not available: the drain snapshot carries no template, and
  adding one needs a pogod change the nightly cannot install until the redeploy
  this bug blocks. A **detached** HEAD is deliberately still `unknown` and still
  holds — the safe direction, and out of scope because both measured live shapes
  are the named-branch one; mg-f0bf carries the coupled constraint that
  `polecat-review.md`'s impossible checkout must not be repaired with a detached
  HEAD unless the detached case is covered here first. Refs drellem2/pogo#134.
