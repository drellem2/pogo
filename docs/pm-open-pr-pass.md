# The open-PR pass

A per-repo pass a PM runs every sweep: for each **open** pull request, decide
whether it is in flight, already landed, stranded, or superseded — and act on
the answer. It is enabled per PM by adding `"open-prs"` to the `sources` array
in `~/.pogo/agents/pm/<name>.toml` (see [CONFIGURATION.md](CONFIGURATION.md)
§"PM TOMLs").

Daniel directed it on 2026-07-29 — *"add PRs in relevant repos to your sweep"* —
after gh#93 sat stranded for three and a half hours without the twice-daily
scan being able to notice.

## The gap it closes — and the half that was never missing

The sweep baseline in `pm-template.md` **already lists open PRs**. Its per-repo
GitHub loop has run

```bash
gh pr list --repo "$slug" || echo "gh unavailable — $slug PRs"
```

since the template shipped (mg-1897, firmed into a per-repo pass by mg-b1e7),
and `gh pr list` defaults to `--state open`. If you came here from a ticket
saying open PRs are unlisted, that half is stale — check the installed template
before repeating it.

What is missing is the **disposition**. The baseline triages a listed PR "the
same way as any other signal": new or unresolved ones are candidate gaps. From
the listing alone those four states are indistinguishable —

- in flight (a live polecat will land it),
- **landed but open** (the work is already on `main`),
- **stranded** (nothing will ever pick it up),
- **superseded** (a different PR shipped the same fix).

— and telling them apart needs two cross-checks that no baseline source runs.
`gh issue list` does not return PRs, and `mg list` has no row for a PR that no
work item tracks, so neither half of how a PM looks can supply the answer. A
stranded PR reads exactly like a healthy one.

### Two measured instances, both on 2026-07-29

- **gh#93** — `[mg-36e3] fix(deploy): resolve git by execution, bind the real
  GH_TOKEN file`. MERGEABLE, CI green, head `c48b055` carrying one commit whose
  patch is **not** on `origin/main`, and `mg-36e3` returns *"no such work item"*.
  The PR was the only place that work existed. The refinery merges from work
  items, so nothing was ever going to pick it up, and the listing showed a
  healthy open PR the whole time. (Originally recorded as "head not an ancestor
  of `origin/main`" — a test that cannot say otherwise; re-measured 2026-08-10
  and the finding holds. See [§The landed-ness
  predicate](#the-landed-ness-predicate).)
- **gh#95** — the competing implementation of the gh#94 fix that did **not**
  land (gh#96 shipped instead). The cost was outward-facing: the gh#97 reporter
  re-verified against `pull/95/head`, cited `ownedByLive` / `polecatDirOwner` —
  symbols that do not exist on `main` — and **walked back a correct finding** on
  the strength of a guard that never shipped. Closed since.

**The generalisable half: an open PR reads as the state of the world to someone
outside the fleet.** Internally we know `main` is the truth; a reporter
reasonably treats an open PR as what is coming. An unlanded PR left open is a
false claim about the product, published, and it misinforms exactly the people
trying to help.

## The two questions

Per repo in the PM's `repos`, per sweep. The repo slug comes from the origin
remote, the same derivation the baseline GitHub loop already uses.

```bash
for repo in <repos>; do
  slug=$(git -C "$repo" remote get-url origin 2>/dev/null \
           | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')
  [ -z "$slug" ] && continue                     # not a GitHub repo — skip

  git -C "$repo" fetch --quiet origin 2>/dev/null || true

  gh pr list --repo "$slug" --state open \
      --json number,title,headRefName,headRefOid \
    || { echo "gh unavailable — $slug open PRs"; continue; }
done
```

For each open PR, two questions:

**1. Has its content landed on `origin/main`?** — compare *patches*, not
ancestry. See [§The landed-ness predicate](#the-landed-ness-predicate) for why
ancestry is the wrong question and what this one does not cover.

```bash
# Fetch the PR head by number — works for fork PRs too, where the head branch
# does not exist on origin and `origin/$headRefName` would not resolve.
if ! git -C "$repo" fetch --quiet origin "pull/$number/head" 2>/dev/null; then
  echo "cannot fetch head — $slug#$number UNJUDGED"; continue
fi

# Capture and check the exit status separately. A `cherry | grep -q '^+'`
# one-liner reports `landed` when git itself fails, because a failed git
# prints nothing and an empty result means "no unlanded commits" — an error
# path pinned to the one answer that authorises closing a live PR.
if ! out=$(git -C "$repo" cherry origin/main FETCH_HEAD 2>&1); then
  echo "cherry failed — $slug#$number UNJUDGED: $out"; continue
fi
printf '%s\n' "$out" | grep -q '^+' && echo "landing not established" || echo landed
```

`git cherry <upstream> <head>` prints one line per commit on `<head>`: `-` if an
equivalent patch is already upstream, `+` if not.

**A `+` means `git cherry` could not establish that this commit landed — not
that it did not land.** The two readings differ exactly where a reader acts:
*did not land* invites filing a carrier, *could not establish* invites the one
check that settles it.

A branch the refinery rebased and merged reports all `-` **only when `main` has
not moved inside the hunk's three-line context window**. Rebasing preserves the
patch-id of a hunk whose surrounding text is unchanged, even though it rewrites
every SHA — but the rebase re-records the patch against the *new* base, so a
neighbouring edit within that window changes the context lines and with them the
patch-id, and the PR's own head then reads `+`. **This is the refinery's own
merge path, measured, not an exotic case**; the unqualified version of this
sentence stood here until 2026-08-19 and read as reassurance it could not
support. See [§What `git cherry` does not
cover](#what-git-cherry-does-not-cover) before acting on a `+` — there are three
measured, ordinary ways for landed content to read `+`.

The `-` direction is the stronger one and it is still **not** absolute: a patch
that landed and was later reverted reads `-` LANDED with the content gone from
`main`. Same section.

**UNJUDGED is a third outcome, not a synonym for either.** Record it under
*Gaps I'm watching* the same way a `gh` outage is recorded, and take no
disposition on that PR this sweep. `+` is the instrument answering and failing
to establish landing; UNJUDGED is the instrument not answering. Folding UNJUDGED
into `+` would file carriers for landed work; folding it into `landed` would
close live PRs.

**2. Does a live work item track it?** — search **all six** statuses
(`available`, `claimed`, `pending`, `done`, `archived`, `shelved`), not just the
active ones. `mg show` resolves an id in any status; `mg list --all` is the
listing equivalent.

```bash
mg show "$id" >/dev/null 2>&1 || echo "no work item"      # id from the PR title
mg list --all --json | grep -q "$headRefName" || echo "no work item for branch"
```

Check **both** the id in the PR title and the branch name: a polecat's agent
name and its work-item id are different strings, so gh#93's branch was
`polecat-d36e3` while its title said `mg-36e3`, and **neither** resolved. Do not
conclude "tracked" from a plausible-looking id you did not actually resolve.

## The landed-ness predicate

### Why ancestry is the wrong question

This doc, and the four PM TOMLs that point at it, originally prescribed

```bash
git -C "$repo" merge-base --is-ancestor "$headRefOid" origin/main   # WRONG
```

**That predicate can never return `landed`, for any PR the refinery merged.**
The refinery *rebases* the branch onto the target before it merges
(`internal/refinery/merge.go:508`, then `merge --ff-only` at
`internal/refinery/merge.go:620`), so what lands is a re-committed copy with new
SHAs. The PR's original head is never an ancestor of `main` afterwards. The
refinery already says this in a user-facing string — the comment it leaves when
it closes a PR (`internal/refinery/merge.go:785`) is *"The refinery rebases each
branch onto `%s` before merging, so the landed commits have different SHAs than
this PR's head and GitHub could not auto-detect the merge."*

With the predicate stuck on `not landed`, **every** PR falls into the bottom
half of the disposition table below. The `landed` row — *rebase-dangle*, which
is the case the pass was built to catch — is unreachable, and a genuinely
landed-but-open PR is misread as either *in flight* (left open indefinitely) or
*stranded* (a carrier filed, or a re-submit attempted, for work already on
`main`). Both arms were hit live on 2026-08-09, along with a third spelling of
the same mistake — `git rev-list --count main..<branch>`, which reported 65
"stranded" branches across three repos that were almost all successful merges.

**Three other candidates were measured and rejected.**

- `gh pr view <n> --json state,mergedAt` is authoritative only for PRs merged
  through GitHub's own merge button. The refinery merges outside GitHub and then
  *closes* the PR, so a landed PR reads `state: CLOSED, mergedAt: null` —
  measured on the five most recent closed pogo PRs (#117–#120, #129): all five
  landed, all five `null`. And by construction every PR this pass examines is
  **open**, so `mergedAt` is `null` for all of them regardless. It carries no
  signal here at all.
- **"`git diff --stat origin/main..<branch>` is empty or deletions-only ⇒
  landed"** looks content-based and is not. Measured against four branches known
  to have merged in `onethird_program` — `polecat-q51f4`, `-q00b3`, `-qb58d`,
  `-qfa70` — every one shows insertions (105, 131, 102, 105), so the rule returns
  *not landed* four times out of four. A landed branch does not converge to
  `main`'s tip: `main` moves on, and the two tips genuinely differ. "Deletions
  only" is not a property that landing produces. This is the *same* defect as the
  ancestry test, wearing better clothes — which is why it is recorded here rather
  than just discarded.
- Comparing the **merge commit's** SHA (`--is-ancestor <MERGE-sha> origin/main`)
  does work — that SHA *is* an ancestor; only the pre-rebase head is not. But the
  pass is looking at a PR, not at an MR record, and does not have that SHA.

### What `git cherry` does not cover

`git cherry` compares **patch-ids**. A patch-id is a hash of a commit's diff with
line numbers normalised away — but the diff it hashes includes the **context
lines**, not only the added and removed ones. Two commits are "equivalent" only
if their diffs agree on the surrounding text as well. So `+` means *no commit
upstream carries this exact patch*, which is strictly weaker than *this content
is not on `main`*.

Three ways that gap opens were measured on 2026-08-19 in throwaway repos, and
each is pinned as a test in `internal/gitgc/cherrylanding_test.go` — against the
shipped Go copy of this predicate, so the section cannot drift away from what
git actually does on this machine:

- **A squash merge rewrites N commits into one new patch**, so every commit on
  the branch reads `+` while the content is byte-identical on `main`. Measured:
  two-commit branch, `git merge --squash`, `+` on *both* commits, `git diff main
  feat` empty. Reported by the repo owner as
  [gh#149](https://github.com/drellem2/pogo/issues/149); this bullet said "not
  measured" until then.
- **It is patch-id identity, not commit count.** A *one*-commit branch whose
  change is folded into a larger squash — its edit plus somebody else's, in one
  commit — also reads `+`, with its own file byte-identical on `main`. So a `+`
  on a single-commit PR is not, by itself, evidence that the predicate is
  misbehaving.
- **Any neighbouring edit on `main` inside the hunk's three-line context window
  flips a landed commit to `+`**, because patch-id hashes those context lines.
  That is ordinary concurrent development in the same file, not an exotic merge
  mode. Measured as a matched triple: the same PR commit, the same squash merge,
  differing only in where `main`'s other edit fell.

  | where `main`'s other edit fell | `git cherry` |
  |---|---|
  | inside the PR hunk's 3-line context window | `+` |
  | same file, outside the window | `-` |
  | a different file | `-` |

  In the `+` case the two patches differ in **exactly one context line**: same
  added line, same removed line, same hunk header `@@ -5,6 +5,6 @@`. Line numbers
  are normalised away; the context text is not.

  **And this one is not confined to squash merges — it reaches the refinery's
  own path.** This section used to say every measurement was on a clean
  rebase-and-merge and that this is the refinery's only merge path, which read
  as reassurance and was not. Rebasing the branch onto a `main` that moved inside
  the window rewrites the landed patch with the *new* context, so the original
  PR head — the ref a reviewer and this pass actually hold — reads `+`. Measured
  the same way, `git rebase main` then `merge --ff-only`: `+` with the
  neighbouring edit in the window, `-` with it in another file.

- **A rebase resolved through a conflict** changes the patch, so the same
  applies. This one **cannot arise on the refinery path**: a conflicting rebase
  aborts and fails the MR rather than merging through it
  (`internal/refinery/merge.go:508–530`, mg-eac0). The residual exposure is
  human-side rebases only, and it is **not measured**.

All four over-report — they answer `+` for content that is on `main` — and the
disposition table's response is "file a carrier, **never** merge or close on a
guess". That direction is safe. It is still a wrong answer, and on a repo that
squash-merges it is the *ordinary* answer rather than an edge case:
`gh api repos/drellem2/pogo` reports `allow_squash_merge: true` (re-checked
2026-08-19), as the mg-ca27 triage found for the other eight watched repos it
polled. One human button-merge is enough to produce a sweep of `+`.

**The `-` direction has one measured failure, and it runs the unsafe way.** A
patch applied to `main` and then **reverted** still reads `-` LANDED: the
original patch *is* upstream, and `git cherry` does not net a revert against what
it undid. Measured — `git cherry -v main feat` prints `- <sha> add the feature`
while `feature.txt` is absent from `main` and the head is not an ancestor.
**The subject-line check this doc used to prescribe does not catch it**, because
the revert commit's own subject contains the original (`Revert "add the
feature"`), so a `git log origin/main` grep matches. Ruling it out takes a look
at the content: `git show origin/main:<path>` on a file the PR touches, or
`git log --oneline origin/main -- <path>` read for a `Revert`.

### A second artifact states this rule, and this doc cannot reach it

**As of 2026-08-19 this doc is not the only place the landed-ness rule is written
down, and the other copy still asserts what the section above just retracted.**
The PM TOMLs that switch this pass on carry their own one-line summary of it —
`~/.pogo/agents/pm/pogo.toml` reads:

    ANY '+' line means NOT landed. Compare PATCHES, never ancestry.

The second sentence is right. The first is the flat assertion measured false
above — and it is the copy that **actually drives the sweeps**, because the TOML
is what reaches a PM's composed prompt. A PM reads that every sweep; it reads
this doc only when something sends it here. Note that being a `#` comment in the
TOML does **not** make it inert: it is carried through verbatim, at line 1054 of
`pogo agent prompt show pm-pogo` when this was written.

Those TOMLs live under `~/.pogo/agents/pm/`, outside this repo and untracked in
it, so **no change to this file can correct them** and no PR can carry the
correction. They are a human's own configuration; editing them was deliberately
left out of the change that wrote this section (mg-724c, from the mg-ca27
triage). Four of the five PM configs carry the line (`pogo`, `onethird`,
`lineara`, `dealdesk`; `riemann` does not), and two of those four are running —
re-checked 2026-08-19.

So if a PM's behaviour disagrees with this doc, read the TOML before concluding
the PM is malfunctioning. **And once the TOMLs are corrected, delete this
subsection** — a gap notice that outlives its gap is just the next artifact
saying something untrue.

### The two-arm acceptance test

The failure mode here is *a test that reads green because it cannot return the
other answer* — which is the defect itself. So any change to this predicate must
be run against both arms, and both must be seen to move:

1. **Known-merged branches must resolve `landed`.** Without this arm, the broken
   ancestry predicate passes. **Wherever the repo allows squash merges, at least
   one of them must be a known-merged multi-commit PR** — that is the case the
   section above is about, and a sample of rebase-merged branches cannot reach
   it. Read the result carefully in both directions: a `+` on a *single*-commit
   PR is not automatically a predicate bug either, because a single commit folded
   into a larger squash, or one whose context lines moved on `main`, is `+` by
   construction. Without that caveat this arm has a false-alarm mode.
2. **Genuinely unmerged branches must still resolve `+`.** Without this arm,
   "everything is landed" passes.
3. **A ref git cannot resolve must resolve `UNJUDGED`** — not `landed`, and not
   `+`. This is the arm the shell makes easy to get wrong; see the comment in
   the snippet above.

Establish arm 1's ground truth **without** the predicate under test — grep the
branch's commit subjects against `git log origin/main`. Using `git cherry` to
decide which branches count as known-merged, and then testing `git cherry`
against that list, proves only that it agrees with itself. The subject grep has
its own hole, and it is the same one as the constraint bullet below: a reverted
patch's subject is still in the log. For a branch you are relying on as ground
truth, read the content on `main`, not the subjects.

Measured in `drellem2/pogo` on 2026-08-10, old predicate vs. new. The `NEW`
column is quoted in that run's own vocabulary, where a `+` was still written
"not landed"; read it as "landing not established" throughout, per the
[predicate section](#the-landed-ness-predicate) above.

```
REF                    OLD          NEW          EXPECTED
polecat-q56ac          not landed   landed       landed (known-merged)
polecat-q4518          not landed   landed       landed (known-merged)
polecat-q69b1          not landed   landed       landed (known-merged)
polecat-qf5dd          not landed   landed       landed (known-merged)
polecat-b0db1          not landed   landed       landed (known-merged, PR #129)
polecat-q6c90          not landed   not landed   not landed (never merged)
polecat-wdd49          not landed   not landed   not landed (3 of 4 commits landed)
```

`polecat-wdd49` is the sharpest of the seven: three of its four commits are on
`main` under different SHAs and one is not, and `git cherry` marks exactly those
three `-` and the fourth `+`. `polecat-b0db1` is the case where all three
candidate predicates are visible at once — PR #129, content fully landed, and
both `gh`'s `mergedAt` and the ancestry test call it unmerged.

`gh#93` above was re-checked under the replacement predicate and is **still**
stranded (`+ c48b055`, one unlanded commit). That finding was correct; it was
not correct *because of* the instrument that produced it.

**The strongest single control is longitudinal.** `polecat-q56ac` was measured
at `3` unlanded commits on 2026-08-09 while it sat unmerged in the refinery
queue, and at `0` on 2026-08-10 after it merged — the same branch, the same
command, the answer flipping across the one event it is supposed to track. No
cross-sectional pair rules out "this predicate happens to correlate with
something else about these branches"; a before/after on one branch does.

## The four dispositions

| Landed? | Tracked? | Disposition |
|---|---|---|
| yes | either | **Rebase-dangle.** The work is on `main` and the PR is noise. Close it and reap the branch at teardown. |
| no | yes | **In flight.** Leave it. The work item is the carrier; if it is stalled, that is a stall-watch matter, not this pass. |
| no | no | **Stranded.** File a carrier work item, or establish that it is superseded. **Never merge or close on a guess** about whether the change is still wanted. |
| no | superseded | **Superseded.** Close it with a one-line reason naming what landed instead. |

The *Landed?* column is question 1's answer, so it is only as good as the
predicate behind it — and its `no` means **landing was not established**, not
*this did not land*.

Rows 2 and 3 are safe under that weaker reading: neither closes nor merges
anything, so a `+` that should have been a `-` costs at most a leave-open or a
redundant carrier. **Row 4 is not** — it closes a PR, outward-facing, and it is
reachable from a `+` alone, because row 3 offers "establish that it is
superseded" as one of its two remedies. So supersession is not established by
the predicate: before closing under row 4, confirm on `main` that the *other*
change is really there, the same content check the third constraint below
prescribes. `+` on the PR you are closing tells you nothing either way.

If the top row never fires across a whole sweep, suspect the predicate before
concluding the fleet has no rebase-dangles — that is exactly what the ancestry
test looked like for the two weeks it was prescribed here. That inference has a
floor, though: **on a repo that squash-merges, the top row can only ever fire for
a single-commit PR whose patch survived the squash intact.** Every multi-commit
PR there is `+` by construction, so an empty top row is expected rather than
diagnostic. Check the merge mode before reading the absence as a fault:

```bash
gh api "repos/$slug" --jq '{squash: .allow_squash_merge, rebase: .allow_rebase_merge}'
```

A `rebase: true` answer is **not** an all-clear, only a weaker floor: a rebase
onto a `main` that moved inside the PR hunk's context window rewrites the patch
too, and the top row misses that PR as well.

Three constraints on acting:

- **Never merge or close on a guess.** "Not landed and not tracked" says nobody
  is carrying the change; it says nothing about whether the change is wanted.
  The two remedies are *file a carrier* and *establish supersession* — closing
  because it looks abandoned destroys the only copy of the work, which is
  precisely gh#93's shape.
- **Closing a PR is outward-facing**, so it routes through the coordinator
  rather than being done by the PM directly — same rule as any other public
  reply.
- **A `landed` reading is not enough to close on its own — and the reason this
  bullet gave until 2026-08-19 was wrong.** It said `git cherry` *cannot* report
  `landed` for content that is not on `main`, so a false *close* was not the
  exposure. It can, and the mitigation it prescribed does not cover the case: an
  applied-then-reverted patch reads `-` LANDED with the content gone, and the
  revert commit carries the original subject, so the `git log origin/main`
  subject grep passes too (measured — [§What `git cherry` does not
  cover](#what-git-cherry-does-not-cover)). Before asking the coordinator to
  close, confirm the **content** is on `main`: `git show origin/main:<path>` for
  a file the PR touches, or `git log --oneline origin/main -- <path>` read for a
  `Revert`. It costs one command and it is the last check before an
  outward-facing action.

## Enabling it

Add `"open-prs"` to the `sources` array of a PM's TOML:

```toml
sources = [
    # ... existing sources ...
    "open-prs",   # open PRs per repo: landed-but-open, stranded, superseded
]
```

The pass degrades the way the rest of the GitHub scan does: if `gh` auth is
unavailable, record "gh unavailable" under *Gaps I'm watching* in the digest and
move on. A `gh` failure must not abort the sweep.

## Where this does not reach

`sources` is per-PM config, so this fixes **the PMs that exist**. The baseline
that would fix *every* PM — including ones not yet instantiated — is
`internal/agent/prompts/pm/pm-template.md`, which this change does not touch:
it is separate work, and shipped prompts are owned by the coordinator together
with pm-pogo as pogo SME.

So a PM instantiated after this doc was written inherits the original blind spot
until the baseline change lands, and gets it only if whoever scaffolds it copies
the `sources` entry. That asymmetry is the point of stating it here rather than
assuming it closed.
