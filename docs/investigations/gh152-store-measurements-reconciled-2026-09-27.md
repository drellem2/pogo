# gh#152's store measurements vs triage's: two stores, not one store measured twice (2026-09-27, mg-03c4)

Refs drellem2/pogo#152. Follow-up to the triage of that issue (mg-c5d1), which
could not reconcile the reporter's counts with its own and filed this item.

## The disagreement, as filed

| quantity | reporter (gh#152, 2026-08-20T15:42:56Z) | triage (mg-c5d1, 2026-09-07) |
|---|---|---|
| `^stage: review` carrier lines, live + archive | 0 | 40 |
| `type: qa` items, all-time | 188 (177 PR reviews, 16/16 open) | 11, zero PR reviews |
| `workflow: gh-issue` items | 37 (pending 12, shelved 17, archived 5) | 155 |
| check-review-decl population (available, claimed, done, pending) | 92 | 184 |

The item framed this as "two hosts disagree about one store and one of them is
wrong", and offered a hypothesis: the reporter's instrument reached only a
sliver of the month-partitioned `archive/`.

## Finding: the premise "one store" does not hold

Every measurement below was taken on 2026-09-27 against `~/.macguffin`
(`MG_ROOT` unset), the only macguffin store on this host
(`ls -d ~/.macguffin*` → one entry). Items whose status or content could have
changed since the filing were rebuilt as of 2026-08-20T15:42:56Z from
`~/.macguffin/events.jsonl`.

### 1. This store already held the carriers the reporter counted as zero

53 files carry `stage: review` as their first `stage:` carrier line today
(`.bodybak/` backups excluded; a raw `grep -rl` finds 77 because it also
counts those backups). **39 of the 53 were archived before the reporter
filed, and none of the 39 has been edited since**, going by two independent
readings:

- event log: the item's first `work.archive` event is at or before the filing, and no `work.edited` event comes after it (39)
- file mtime at or before the filing (39)

So at the moment gh#152 was filed, this store's archive held at least 39
`stage: review` carrier lines that are still byte-for-byte there. A correct
count of *this* store at that moment could not have come out as 0. (All 40
carriers triage counted were created before the filing, too.)

### 2. The sibling issue names work items this store never held

gh#153 was filed by the same reporter 34 seconds after gh#152 and is
described in gh#152 as "filed alongside this one". It quotes live work items
from the reporter's board: `mg-d1ce` "reviewing PR #1974", and `mg-3632`,
`mg-18ce`, `mg-7e40`, `mg-4c41`, `mg-b058`, `mg-aba1`, `mg-2e2f`, `mg-237e`,
`mg-d2d1`, `mg-ed5b`.

- None of these 11 ids is in `~/.macguffin/work`, archive included, and none appears once in the 67,584-line `events.jsonl`. They were never created here, not created and later deleted.
- Positive control: the two ids gh#151/153 cite that DO exist here (`mg-aaf6`, `mg-1bbf`) are found by the same lookup, and `mg-aaf6` has 14 events. Both are also cited in pogo's public commit history, so the reporter could have read them from the repo rather than from a store.
- PR #1974 does not exist in drellem2/pogo, whose highest PR number is 196. The board in question belongs to a repo with about ten times as many PRs.

### 3. This fleet does not file reviews the way the reporter describes

The reporter describes live reviews as `type: qa`, titled `REVIEW:`, with a
`depends:` edge to the build item. This fleet's mayor prompt uses
`--type=qa` only for `QA:` items (`internal/agent/prompts/mayor.md:924`,
`:934`). Its review tickets are `type: task`, and the prompt explicitly
forbids the review ticket from depending on the build ticket (`mayor.md:1024`).
The store matches the prompt:

- `type: qa` in frontmatter: **10**, all created 2026-04-18 … 04-26, all archived. Triage's 11 counted every `^type: qa` line anywhere in the file; the 11th is prose in the body of `mg-5384`, whose frontmatter says `type: task`.
- items titled `# review…` (case-insensitive): 74, **every one `type: task`**. Case-sensitive `^# REVIEW`: 10 today; triage counted 5 on 2026-09-07.

### 4. The disagreement is not confined to the archive axis

The item noticed that the disagreements lean toward the archive. Rebuilding
this store at the filing time shows the live side disagrees just as much:

| as of 2026-08-20T15:42:56Z | reporter | this store (reconstructed) |
|---|---|---|
| gh-issue items created by then | 37 | 134 |
| … pending | 12 | 0 |
| … shelved | 17 | 3 |
| … archived | 5 | 111 |
| … available | not stated | 20 |
| check-review-decl population (4 dirs) | 92 | ~145 |

Reconstruction error, measured by rebuilding the present from the same log
and comparing it with the directories: 161 vs 157 today, 171 vs triage's
directly measured 184 on 2026-09-07. That is ~2–7%, far smaller than the
92-vs-145 gap. Twelve pending and seventeen shelved gh-issue items did not
exist in this store at any point near the filing.

## The hypotheses in the item, tested

- **Non-recursive `archive/*.md` glob.** The mechanism is real, and in bash it fails silently: `grep -l '^stage: review' archive/*.md 2>/dev/null | wc -l` prints `0` with pipeline status 0, while `grep -rl` over the same tree prints 76. In zsh the unmatched glob aborts loudly (`no matches found`). But the same glob also returns 0 for `stage: gated`, where the reporter reported 4 archived. And it does nothing to explain the live-side numbers in §4 or the absent ids in §2. **Rejected as the explanation.** It remains a hazard for anyone who re-measures by hand.
- **`mg list | wc -l` = 188 coincidence.** Today that count is 161. Triage's 188 on 2026-09-07 was a reading of a count that moves by tens a week, and it was taken 18 days after the filing. Nothing ties it to the reporter's 188. **No evidence for it**, and the reporter's 177/188 PR-review split is a classification of individual items that a whole-store line count cannot produce.

## What this means for gh#152 (and what it does not)

- The reporter's numbers and triage's numbers are both consistent with **two different macguffin stores**. The reporter's is almost certainly the other fleet that files issues against drellem2/pogo under the same GitHub account (MR-id machine `878eb3`, as opposed to this host's `5d9fc3`; see mg-2947). That identification is **inferred, not measured**: this investigation had no access to that store and did not run the reporter's commands. The measured facts are negative: this store could not have produced their counts, and it never held the items their sibling issue names.
- So **neither side's instrument is shown to be wrong.** Each describes a real store. The "one of them is wrong" framing came from assuming one store.
- Triage's recommendation to decline ask #1 (widen the collector to `type: qa`) was **evidenced on this fleet's store only**. Here `type: qa` never carries a review, so widening would add only noise. On the reporter's fleet, by their own counts, `type: qa` carries 94% of reviews and `stage: review` carries none. **Both statements can be true at once.** The code ships to both fleets, so the decision on ask #1 is a question about what the collector should do when fleets file reviews differently. It is not a question about whose count is right. That decision is for the human gate on gh#152, and this document does not make it.
- D-1 in `internal/reviewdecl` ("31 of the 34 `stage: review` carriers are in archive/") describes this store. Here the archive still holds 52 such carriers. The reporter's "those 34 are now 0" is a reading of a different store, not evidence that D-1 went stale here.

## Unverified

- The reporter's commands, working directory and `MG_ROOT` were never published and were not recovered. Items 1–3 of the original "what would discriminate" list are still open, and only the reporter can answer them.
- That the reporter's store is the `878eb3` fleet's store: inferred from §2–§3, not observed.
- The 2026-08-20 reconstruction treats `work.*` events with a `to_status` as the whole status history. Items created before the log began (2026-04-26) have no creation event. The ~145 is approximate, as its control shows.
