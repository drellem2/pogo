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
printf '%s\n' "$out" | grep -q '^+' && echo "not landed" || echo landed
```

`git cherry <upstream> <head>` prints one line per commit on `<head>`: `-` if an
equivalent patch is already upstream, `+` if not. **Any `+` means not landed.**
A branch that the refinery rebased and merged reports all `-`, because rebasing
preserves patch-ids even though it rewrites every SHA.

**UNJUDGED is a third outcome, not a synonym for either.** Record it under
*Gaps I'm watching* the same way a `gh` outage is recorded, and take no
disposition on that PR this sweep. Folding it into `not landed` would file
carriers for landed work; folding it into `landed` would close live PRs.

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

`git cherry` compares **patch-ids**, so it reports `not landed` for content that
did land under a different patch. Every measurement below and above is on a
**clean** rebase-and-merge; that is the refinery's only merge path, but it is not
every path. Known exposure, stated rather than assumed — a doc that claims
accuracy nobody measured is how the ancestry line survived two weeks:

- **Squash merges and "Rebase and merge" through the GitHub UI** rewrite N
  commits into one new patch. `git cherry` will call the originals unlanded.
  **Not measured** — no squash-merged PR was available to test against.
- **A rebase resolved through a conflict** changes the patch, so the same
  applies. This one **cannot arise on the refinery path**: a conflicting rebase
  aborts and fails the MR rather than merging through it
  (`internal/refinery/merge.go:508–530`, mg-eac0). The residual exposure is
  human-side rebases only, and it is **not measured**.

Both failures are in the *safe* direction — they over-report `not landed`, and
the disposition table's response to `not landed + untracked` is "file a carrier,
**never** merge or close on a guess". They are still failures. If a PR looks
stranded and its subject line appears in `git log origin/main`, check the
content by hand before filing anything.

### The two-arm acceptance test

The failure mode here is *a test that reads green because it cannot return the
other answer* — which is the defect itself. So any change to this predicate must
be run against both arms, and both must be seen to move:

1. **Known-merged branches must resolve `landed`.** Without this arm, the broken
   ancestry predicate passes.
2. **Genuinely unmerged branches must still resolve `not landed`.** Without this
   arm, "everything is landed" passes.
3. **A ref git cannot resolve must resolve `UNJUDGED`** — not `landed`, and not
   `not landed`. This is the arm the shell makes easy to get wrong; see the
   comment in the snippet above.

Establish arm 1's ground truth **without** the predicate under test — grep the
branch's commit subjects against `git log origin/main`. Using `git cherry` to
decide which branches count as known-merged, and then testing `git cherry`
against that list, proves only that it agrees with itself.

Measured in `drellem2/pogo` on 2026-08-10, old predicate vs. new:

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
predicate behind it. If the top row never fires across a whole sweep, suspect
the predicate before concluding the fleet has no rebase-dangles — that is
exactly what the ancestry test looked like for the two weeks it was prescribed
here.

Three constraints on acting:

- **Never merge or close on a guess.** "Not landed and not tracked" says nobody
  is carrying the change; it says nothing about whether the change is wanted.
  The two remedies are *file a carrier* and *establish supersession* — closing
  because it looks abandoned destroys the only copy of the work, which is
  precisely gh#93's shape.
- **Closing a PR is outward-facing**, so it routes through the coordinator
  rather than being done by the PM directly — same rule as any other public
  reply.
- **A `landed` reading is not enough to close on its own**, given the
  squash-merge blind spot runs the other way: `git cherry` cannot report
  `landed` for content that is not on `main`, so a false *close* is not the
  exposure here — but confirm the subject line appears in `git log origin/main`
  anyway before asking the coordinator to close. It costs one command and it is
  the last check before an outward-facing action.

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
