- **The review prompt stops instructing a step git refuses to execute — reviewers
  are told to put their OWN named branch at the PR head, which is what they were
  already forced to improvise (mg-f0bf).** Step 4 of
  `internal/agent/prompts/templates/polecat-review.md` said `git checkout
  <pr-branch>`. That cannot run on the track it lives on: git refuses a branch
  already checked out in another worktree (`fatal: '<pr-branch>' is already used
  by worktree at …`, exit 128), and the builder's worktree is still live while
  its PR awaits review — which is gh#134's whole subject. The precondition was
  therefore never satisfied, and every reviewer was pushed onto the later-rounds
  `reset --hard` against its own branch. Not an occasional deviation but a
  structural one, which is why mg-1af2 measured it as every review polecat, every
  time. mg-fd94 made that forced state *harmless* to the deploy drain; it did not
  make the instruction correct, and a reviewer that trusts the prompt and stops on
  the error — or improvises differently each round — was a live defect independent
  of the drain. Step 4 now says `git checkout -B "$OWN_BRANCH" --no-track
  "origin/$PR_BRANCH"`, one command that is identical on round 1 and round N
  because `-B` is itself a reset, with the own-branch name read from `git
  rev-parse` rather than guessed.

  **The repair was constrained, and the obvious version of it was the trap.**
  Detaching at the PR head is the tidier-looking fix and would have re-created the
  deadlock mg-fd94 had just removed: `durability_of` names the branch checked out
  in a worktree *before* it asks whether any origin ref holds HEAD, so a detached
  reviewer answers `unknown`, and `unknown` holds the drain identically to
  `unpushed` — on every reviewer, looking like a tidy-up. Keeping a **named**
  branch is what makes the drain's containment test reachable at all. That
  coupling is now a measurement rather than a comment: `scripts/pogo-self-deploy_test.sh`
  grows fixture (g2), built from the literal command the prompt instructs, and
  asserts it is answered by the same branch of `durability_of` with the same
  detail as the `reset --hard` improvisation it replaces — so the prompt repair is
  provably verdict-neutral, and a future editor swapping in `checkout --detach`
  turns the assertions red instead of silently stalling the nightly. The suite
  also exhibits the refusal itself (exit 128, `already used by worktree`), because
  asserting the replacement works says nothing about whether the thing it replaced
  was broken, and without it the suite would keep passing if someone "restored"
  the original line.

  **`--no-track` is load-bearing, and its absence would have been silent.**
  Without it, `checkout -B` from a remote-tracking start point sets the reviewer's
  upstream to the *builder's* branch. A bare `git push` then refuses under
  `push.default=simple` — but the refusal prints `git push origin
  HEAD:<pr-branch>`, a one-paste clobber of the branch under review, and nothing
  else in the suite would have noticed. With `--no-track` no upstream is set and
  the same refusal offers the reviewer's own branch name instead. Measured also:
  `--no-track` only prevents an upstream being set and does **not** clear one
  already present, which is why it belongs on every round rather than only the
  first. Refs drellem2/pogo#134.
