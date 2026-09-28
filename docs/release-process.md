# Release process: soak a candidate, then cut it from a release branch

A release is cut for consumers only after the candidate has run on this box for
about 24 hours. This page has two halves:

- **The policy.** It is copied verbatim from pm-pogo's v1 design in `mg-8382`.
  Change it there first, not here.
- **The mechanics.** These are the exact commands for tagging the **soaked**
  SHA rather than main's tip. `mg-d734` verified them, and
  `internal/refinery/releasebranch_test.go` pins them.

The tag-trigger itself (a pushed `v*` tag fires `.github/workflows/release.yml`,
which publishes) and the changelog tooling are covered under **Releases** in
`CONTRIBUTING.md`.

## Policy (verbatim from mg-8382, pm-pogo, 2026-09-26 10:55Z)

> **Candidate = one exact SHA of pogo `main`, the one the nightly deploy installed.** Not an rc tag: a pushed `v*` tag fires release.yml, which publishes to consumers, and that is exactly what a soak must precede. The candidate SHA is recorded in the release-cut item's body. Evidence of what is running: `pogo` revision / drift-watch's running revision, never "what main was".
>
> **Soaked = all three hold, checked by pm-pogo at each sweep, with the evidence appended here:**
> 1. pogod has run the candidate SHA for **>= 24h wall-clock continuously**. The mg-32f5 lifecycle events make this checkable: a `pogod_boot` naming the candidate revision, and no `pogod_boot` with previous=unclean since then.
> 2. The fleet did real work on it. Merges landed through the refinery, and crew turnlogs advanced. An idle 24h is not a soak.
> 3. **No regression attributable to the candidate range** (previous release..candidate). A regression is a bug whose cause is a commit in that range. Fixing it produces a NEW candidate (the SHA with the fix, installed by the next nightly), and the 24h clock restarts. A pre-existing bug found during the soak gets filed and does NOT reset the clock.
>
> **Who declares:** pm-pogo, in the release-cut item body, with the three pieces of evidence. Then mail mayor dispatch-ready. Daniel can OVERRIDE as usual.
>
> **The cut tags the SOAKED SHA, not main's tip.** Main advances during the 24h, so tagging origin/main (or main + bump) would ship a day of unsoaked commits. Mechanism: a release branch `release/vX.Y.Z` is created AT the candidate SHA, the version bump lands there via the refinery, and `--post-merge-tag` tags that merge.
>
> **Not automated yet, on purpose.** Run it by hand for v0.11.0 first. Automate only what the first soak shows is tedious or error-prone.

"Here" in the policy means the release-cut item's body, which is also where
the candidate SHA is recorded.

## Mechanics: cutting from `release/vX.Y.Z`

Below, `CAND` is the soaked candidate SHA from the release-cut item's body, and
`X.Y.Z` is the version. **Nothing on this page may be rehearsed with a real
`v*` tag on origin.** Any pushed `v*` tag publishes a release. To rehearse, run
the test at the end of this page, which uses a bare temp origin.

### 0. Confirm the candidate is what ran

```bash
curl -s http://127.0.0.1:10000/version | jq -r .revision   # the running revision
git fetch origin
git cat-file -e "${CAND}^{commit}" && git merge-base --is-ancestor "$CAND" origin/main && echo "CAND is on main"
```

### 1. Create the release branch AT the candidate (coordinator)

```bash
git push origin "${CAND}:refs/heads/release/vX.Y.Z"
git ls-remote --heads origin release/vX.Y.Z      # must print CAND
```

Create the branch **before** submitting anything. The refinery auto-creates a
missing target only when `--auto-create-target` is passed. When it does, it
branches from the **default branch**, which is main's tip, and that ships the
unsoaked commits. When the target already exists, the refinery uses it as is
and never re-carves it, with or without that flag (pinned by the test below).

### 2. Dispatch the release-cut polecat onto the release branch

```bash
pogo agent spawn-polecat cut-XYZ --id <release-cut-item> --template=polecat \
    --branch release/vX.Y.Z --body-file - <<'EOF'
Cut vX.Y.Z from release/vX.Y.Z (already at the soaked candidate CAND).
  ./scripts/bump-version.sh X.Y.Z --commit
  git push origin "$BRANCH"
  pogo refinery submit "$BRANCH" --repo=/Users/daniel/dev/pogo --author=<release-cut-item> \
      --target=release/vX.Y.Z --post-merge-tag=vX.Y.Z --verdict-file=...
Do NOT pass --tag to bump-version.sh. Do NOT open a PR to main. After the
merge, confirm the tag (step 4), then submit the back-port (step 5):
  pogo refinery submit release/vX.Y.Z --repo=/Users/daniel/dev/pogo \
      --author=<release-cut-item> --target=main
Mail the coordinator the back-port MR id, then STOP. Do NOT wait for the
back-port to merge and do NOT run `mg done`: the refinery closes this item
when the back-port merges.
EOF
```

- **Pass `--branch` explicitly.** With it, the worktree is based on
  `origin/release/vX.Y.Z`, and the template's submit line renders
  `--target=release/vX.Y.Z`. Since mg-bb0d (drellem2/pogo#176), pogod also
  defaults `--branch` from the item's own `branch:` field and refuses a
  `--branch` that contradicts it. A pogod built before that ignores the field
  and bases the worktree on main's tip, so the explicit flag is the form that
  is safe on every daemon.
- **`--post-merge-tag` is not in the template.** Put it in the body, as above.
- Run `bump-version.sh` on the release branch. It computes the changelog
  coverage range from the most recent tag reachable from `HEAD` to the
  candidate, which is the right range.

### 3. What the refinery does

The refinery rebases the bump onto `origin/release/vX.Y.Z` (still at `CAND`),
runs the gates, and fast-forwards the release branch. It then creates the
annotated tag `vX.Y.Z` **on the commit that merge landed as** and pushes it
before any reap can observe the merge (`mg-6879`). Pushing the tag fires
`release.yml`.

The refinery rebases and fast-forwards; it never makes a two-parent merge
commit. So "the release-branch merge" is the rebased bump commit. Its first
(and only) parent is `CAND`.

A target that is not the default branch is treated as an **integration
branch**, so the MR is `pr_flow`. pogod therefore does **not** auto-complete
the item or stop the polecat at this merge. The item closes later, when the
step-5 back-port merges into main (see step 5 for why nobody runs `mg done`).
A bounded backstop reaps the polecat. If the tag step fails, the item is not
completed and the mayor is mailed.

### 4. Verify against origin, never a local tag

```bash
git fetch origin --tags
test "$(git rev-parse 'vX.Y.Z^{commit}')" = "$(git rev-parse origin/release/vX.Y.Z)" && echo "tag on release tip"
test "$(git rev-parse 'vX.Y.Z^{commit}^1')" = "$CAND" && echo "first parent is the candidate"
git log --oneline "vX.Y.Z..origin/main" | head    # main's post-candidate commits: NOT in the release
gh run list --workflow=release.yml --limit 1        # the publish run
```

### 5. Carry the bump back to main

The bump exists only on the release branch. Until it reaches main, main still
reports the old version, and `changelog.d/` still holds the fragments the
release consumed. The next cut would then ship them a second time.

```bash
pogo refinery submit release/vX.Y.Z --repo=/Users/daniel/dev/pogo \
    --author=<release-cut-item> --target=main
```

`--author=<release-cut-item>` is what lets the refinery close the item when
this merge lands (below). Leave it off and nothing closes the item.

The refinery rebases the release branch onto main. Everything before the bump
is already on main, so only the bump commit is replayed. It removes the consumed
fragments and rolls `CHANGELOG.md`. The tag and `release/vX.Y.Z` are left
untouched, because the refinery reaps a source branch only when a PR exists
for it.

The replayed bump gets a new SHA, so the `vX.Y.Z` tag is **not** an ancestor
of main. As a result, `git describe` on main keeps naming the previous tag
reachable from main, and the next cut's default coverage range starts there.
`changelog-coverage.sh` counts the ids already shipped in vX.Y.Z as
`released`, because the back-port put that section into `CHANGELOG.md`. The
check therefore still passes.

This step is mechanics that the policy did not spell out. `mg-d734` added it
because the cut is not complete without it.

#### Who closes the release-cut item: the refinery, on the back-port merge

The cutting polecat submits the back-port and **stops**. It does not wait for
the back-port MR to merge, and it does not run `mg done`. Nobody runs
`mg done`. The back-port targets main, the default branch, so it is not
`pr_flow`: when it merges, pogod closes the item named by `--author` itself
(`completed_by: refinery`), even if the coordinator holds the claim. The
coordinator only confirms the close and archives the item:

```bash
pogo refinery show <backport-mr> --json | jq -r .status     # merged
mg show <release-cut-item> | grep '^Status:'                # done
mg archive <release-cut-item>
```

If the MR merged and the item is still open, the submit lacked `--author`, so
the refinery had no item to close. Only then does the coordinator close it by
hand.

The polecat must not wait, because it cannot outlive the wait. The back-port
queues behind whatever else is in the refinery, and nothing bounds how long
that takes, while the defer-done backstop reaps a polecat that lingers. In the
v0.11.0 cut (`mg-3225`), the cutting polecat submitted the back-port
(`mr-dastnv2`) and was reaped while waiting to run `mg done`. The mayor claimed
the item so that it would not be redispatched. When the back-port merged
(`75fd78e`), pogod closed `mg-3225` itself with `completed_by: refinery`.
Nobody ran `mg done`.

This supersedes the 2026-09-26 policy note on `mg-8382`, which said "the
cutting polecat runs `mg done` itself". That holds only for a cut with no
back-port, and every cut from a release branch has one (`mg-f4ea0`).

## Rehearsing without publishing

```bash
go test ./internal/refinery/ -run TestReleaseBranchCut -v
```

The test runs steps 1 to 5 against a bare temp origin. It moves main past the
candidate, then asserts the following:

- the existing release branch is not re-carved, even with auto-create on;
- the tag is on the release-branch merge;
- that merge's first parent is the candidate;
- main's soak-time commit is absent from the tag (with a positive control that
  the same probe finds it on main);
- main is untouched by the cut;
- the back-port lands the bump on main without moving the tag or the release
  branch.
