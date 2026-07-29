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
  GH_TOKEN file`. MERGEABLE, CI green, head `c48b055` **not** an ancestor of
  `origin/main`, and `mg-36e3` returns *"no such work item"*. The PR was the
  only place that work existed. The refinery merges from work items, so nothing
  was ever going to pick it up, and the listing showed a healthy open PR the
  whole time.
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

**1. Is its head an ancestor of `origin/main`?** — did the work land?

```bash
git -C "$repo" merge-base --is-ancestor "$headRefOid" origin/main \
  && echo landed || echo "not landed"
```

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

## The four dispositions

| Landed? | Tracked? | Disposition |
|---|---|---|
| yes | either | **Rebase-dangle.** The work is on `main` and the PR is noise. Close it and reap the branch at teardown. |
| no | yes | **In flight.** Leave it. The work item is the carrier; if it is stalled, that is a stall-watch matter, not this pass. |
| no | no | **Stranded.** File a carrier work item, or establish that it is superseded. **Never merge or close on a guess** about whether the change is still wanted. |
| no | superseded | **Superseded.** Close it with a one-line reason naming what landed instead. |

Two constraints on acting:

- **Never merge or close on a guess.** "Not landed and not tracked" says nobody
  is carrying the change; it says nothing about whether the change is wanted.
  The two remedies are *file a carrier* and *establish supersession* — closing
  because it looks abandoned destroys the only copy of the work, which is
  precisely gh#93's shape.
- **Closing a PR is outward-facing**, so it routes through the coordinator
  rather than being done by the PM directly — same rule as any other public
  reply.

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
