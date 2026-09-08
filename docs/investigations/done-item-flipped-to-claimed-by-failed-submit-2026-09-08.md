# A failed refinery submit can flip a `done` work item back to `claimed`

**Work item:** mg-4d21 · **Refs:** drellem2/pogo#164 (comment 5530266972,
2026-09-03) · **Investigated:** 2026-09-08

## The report

Daniel, on #164, describing three branches submitted to the refinery against
`payitgov/cool-ui-libraries --target=develop` that all failed on branch
protection:

> On one item the submit attempt also flipped an already-`done` work item back to
> `claimed`, which had to be repaired by hand afterwards.

One occurrence, one reporter, repaired manually. Nothing else was established
when the ticket was filed: not which item, not which branch, not whether the
merge had to FAIL for it to happen.

## The answer: yes, and here is the path

`mg reopen` is the **only** writer in macguffin that moves a work item out of
`done/` (`internal/workitem/reopen.go`; the only other writers into `claimed/`
are `Claim`, which requires `available`, and `Reclaim`, which requires
`claimed`). pogod's refinery `OnFailed` callback has called it on **every** merge
failure since mg-06f2, keyed on nothing but `mr.Author`:

```
cmd/pogod/main.go  OnFailed
  -> client.ReopenMGWorkItem(mr.Author)
  -> mg reopen <id>
  -> workitem.Reopen  :  done/<id>.md  ->  claimed/<id>.md
```

So a failed submit can flip a `done` item to `claimed`. The report is accurate
and the mechanism is not exotic — it is the designed behaviour, fired in a case
it was not designed for.

### Measured, against the live `mg` binary (2026-09-08, private store)

| starting status | `mg reopen` | result |
|---|---|---|
| `done` | exit 0, "Reopened mg-ed67" | `work/claimed/mg-ed67.md` — **no pid suffix** |
| `archived` | exit 4, "is archived, not done." | nothing moves |
| `claimed` | exit 4, "not done — it is already claimed (in progress)." | nothing moves |

The `done` row is the flip. The `archived` row settles the mayor's negative
instance: mg-3ba8 was archived at 07:16Z, four minutes after its merge, and the
docs follow-up that failed afterwards left it alone — **because reopen cannot
reach `archive/`, not because the failure was different**. Archiving promptly
masks this bug rather than avoiding it, and the mayor's own habit of archiving
within minutes of every merge is part of why one occurrence was reported instead
of many. Items nobody archives are the exposed population.

The missing pid suffix is the second half of the harm. A reopened item sits in
`claimed/` held by no process, and every stall check in this repo scans
`available/` (`internal/stallwatch`), so nothing sweeps it. `pogo doctor` counts
it as one more claimed item and cannot say it is ownerless. That is why the
reported occurrence needed a human: it was not merely undetected, it was
**undetectable** by anything running.

## Why the reopen was right once and is wrong now

mg-06f2 added it for the case where a polecat calls `mg done`, exits, and the
merge THEN fails — the item is closed over work that never landed, so reopening
it is correct. That case has since become the rare one. pogod closes the item
**at merge** (mg-2b71, gh #35), so an item in `done/` when a merge fails is
usually done because an EARLIER merge by the same author landed, and the failure
in hand is a follow-up branch. Reopening there destroys a true record and
produces something strictly worse than the open item mg-06f2 was chasing.

## What was changed

1. **The reopen is guarded** (`cmd/pogod/reopenguard.go`). Before running
   `mg reopen`, ask the refinery whether any merge request authored by this item
   has LANDED. If one has, do not reopen — say so instead, naming the MR, branch,
   target and SHA so the refusal is checkable with one `git log`. PR-flow merges
   count here, unlike in the dispatch gate (`mergedWorkFor`), because on that
   lane the polecat closes the item itself and the close is deliberate; a test
   pins the two rulings apart so they cannot be "unified" later.
2. **The disposition is stated in the MERGE FAILED mail**, for every outcome
   including the ones that change nothing, and the reopen now runs *above* the
   mails so it can be. The mail previously described the merge and never its side
   effect on the work item — which is why the flip was repaired by hand rather
   than reported.
3. **`work_item_reopen_after_merge_failure`** records the flip, the refusal that
   prevents one, and any unclassified `mg` error (`docs/event-log.md`). The
   ordinary "still claimed" and "archived" refusals are not emitted; they would
   bury the rare row.
4. **`ErrMGWorkItemArchived`** joins `ErrMGWorkItemNotDone` in `internal/client`,
   so an archived item no longer logs as "failed to reopen work item" — a false
   alarm on the exact refusal that produced the known negative instance.

The failure direction is OPEN, stated rather than left to be found: no author, no
refinery, or a merge that has aged out of the refinery's history (100 entries /
7 days) all reopen as before. The guard refuses only on a record the refinery
wrote. The mail line is the backstop for everything it cannot see.

## What is still not known, and why

**The positive instance is unrecoverable.** It cannot be pinned to an item, and
that is not for want of looking:

- `~/.macguffin/events.jsonl` holds 19 `work.reopen` events over its whole life
  (2026-04-26 onward). The **newest is 2026-08-19**. There is none on 2026-09-03,
  on a day the same log carries 3,586 events including `work.claim`,
  `work.unclaim`, `work.done` and `work.archive` — so the log was recording work
  transitions normally and simply saw no reopen.
- `~/.pogo/refinery-state.json` retains 45 history entries, none for
  `cool-ui-libraries`; the 2026-09-03 merge requests have been pruned.
- `~/Library/Logs/pogo/pogod.log` stops at 2026-09-02 01:05. The daemon running
  on the day of the report was not writing to it (mg-a19a).

Three plain readings, and this investigation does not pick between them: the flip
predates 2026-08-19 and was reported later; or it happened in a store or under a
daemon whose records are gone; or the transition was not an `mg reopen` at all.
The third would need a different explanation than this one, and nothing found
here supplies it — the code path is real, reachable and unguarded, which is
enough to fix, and it is not by itself proof that it is what Daniel saw.

**Not investigated (out of scope):** whether a failure CLASS should gate the
reopen as well — a branch-protection rejection says nothing about the author's
work, and all three branches in the report failed that way. That is
classification, which is #164's subject and mg-c33d's triage, not this one's.
