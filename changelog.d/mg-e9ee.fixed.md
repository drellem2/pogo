- **`pogo refinery history` stops silently truncating, and gains `--since` to
  read past the cap (mg-e9ee).** The command was capped at 100 rows with no way
  to see further and no indication the cap had bitten. Measured on this machine
  2026-07-30 05:24Z: `pogo refinery history | grep -c '^mr-'` → 100, `--json`
  → 100, and `--help` listed no `--limit`, `--all`, `--since`, or pagination.

  **The cap is not a display limit — the rows are deleted.** `pruneHistoryLocked`
  removes entries past `MaxHistoryLen` (100) / `MaxHistoryAge` (7d) from the
  in-memory history and persists the deletion. Because the count cap bites first
  at any real merge rate, the retained window is time-shaped without being
  time-bounded: measured, 100 rows spanning 19 hours. So the flag the ticket
  asked for could not be built the way it was framed. A `--limit 0`/`--all` over
  the retained window would have returned the same 100 rows *while looking like
  it had widened them* — the defect wearing the fix's clothes. There is
  deliberately no `--all` and no `--limit`.

  What the durable record is: the event log. `~/.pogo/events.log` holds 1533
  `refinery_merged` + 608 `refinery_merge_failed` + 2 `refinery_merge_cancelled`
  — 2141 completions against history's 100, a 21× window — and
  `docs/event-log.md` already designated it "the durable observability spine …
  survives pogod restarts (unlike the in-memory refinery history)".

  Three changes:

  - **`--since <duration|date>`** reconstructs one row per merge request from the
    event log and its surviving rotated files (`refinery.HistoryFromLog`).
    Accepts `7d`/`30d` as well as Go durations and absolute dates; rejects zero,
    negative, and future bounds, whose only possible output is an empty window.
    The default path is unchanged and still bounded, so the common interactive
    case stays fast. **stdout has the same shape either way** — the same `jq`
    pipeline works with or without the flag, so widening the window does not
    mean rewriting the consumer.
  - **The cap is stated whenever it bites.** `pogo refinery history` writes
    "showing N of an unknown total — retention caps this at 100 entries / 168h
    and prunes DESTRUCTIVELY" to stderr; stdout is untouched, so pipes are
    unaffected. `pogo refinery status` prints history alongside the retention
    that bounds it. Both read the cap from the **running daemon** (new
    `max_history_len`/`max_history_age` in the status payload) rather than from a
    constant in the client, so the number printed cannot drift from the number
    enforced. If the retention cannot be read, the notice says truncation is
    UNKNOWN — it does not fall silent.
  - **`--since` fails loudly rather than answering short.** The reconstruction
    reports whether the log reaches back as far as was asked, and the command
    exits non-zero when it does not, with `TRUNCATED` on stderr. A consumer that
    cannot tell whether it saw everything is back where it started, so the
    signal is in the exit code where `pipefail` catches it — not only in prose.
    The coverage line is printed **whether or not the bound bit**: "no bound
    stated" and "no bound" are indistinguishable to a reader, which is the
    original defect in one sentence.

  Coverage is decided by whether records were **discarded**, not by where the
  log happens to start (`events.LogSpilled`). An unrotated log has dropped
  nothing, so its first record is a beginning and a window reaching further back
  misses nothing — reporting TRUNCATED there would cry wolf on every fresh
  install and train readers to ignore the one signal that matters. The test can
  err one rotation early, understating coverage; that direction is deliberate.

  **The consumer is fixed too.** `internal/agent/prompts/mayor.md`'s step-3
  orphaned-failure check — the one mg-2ca3 repaired — ran its relationship query
  over the capped window and documented empty output as "the expected, healthy
  answer". Over 100 rows, empty cannot distinguish *no orphaned failures* from
  *the orphaned failure is row 101*, and the check exists to catch work that was
  lost, which is exactly the case where nobody is watching and the row ages out.
  The recipe now uses `--since=30d` with `set -o pipefail`, and the claim is
  corrected: empty means healthy **within the window you named**. mg-2ca3's own
  warning — *"never judge a branch by `pogo refinery history | tail`: a bounded
  window can put the merge one line below the cut"* — is now extended to the
  window itself, because that commit diagnosed the defect at one layer and
  reproduced it at the next.

  **What the widened window found, measured 2026-07-30.** The default window
  reports zero orphaned failures; `--since=30d` over 2141 reconstructed MRs
  reports two, `mg-a9bb` and `mg-abea`. **Both are false positives, and the mode
  is worth naming**: the classifier groups by `.branch`, so a retry submitted
  under a *different* branch name splits into two groups and the failed half
  looks orphaned. Each failed on `polecat-mg-<id>` — a branch that never existed
  on origin — then merged as `polecat-<id>`. Both items are `archived` with their
  commits on `main`. So the widened detector's first real run was 2/2 false, and
  that is now documented in the prompt beside the recipe: a detector that cries
  wolf on first use is worth no more than the blind one it replaced, and
  mg-2ca3's report-don't-act discipline is what keeps this from having reopened
  two landed items. The grouping key is a separate defect and is not changed
  here.

  **Also fixed:** `refinery_merged`, `refinery_merge_failed`, and
  `refinery_merge_cancelled` now carry `author` in `details`. Only
  `refinery_merge_attempted` did, so a reconstruction whose attempt event had
  rotated out could not name the author (`work_item_id` is close but not the same
  string — it is the author with any `cat-` prefix stripped). `History()`'s doc
  comment claimed "most recent first" when the code returns append order,
  oldest first; a wrong ordering claim is precisely what sends a reader to
  `| tail`.

  **Checked, per the ticket:** the cap is history-only. `queue` returns the whole
  queue and is drained rather than pruned; the `lost` list is uncapped; the
  pruned-ID ring is bounded but already honest — it is what lets
  `refinery show` answer "pruned from history" instead of a bare "not found".
  Noted and not changed: `MaxHistoryLen`/`MaxHistoryAge` are not plumbed from
  config (only `PollInterval` is), so retention is hard-coded. A knob would not
  have fixed this — an operator raising the cap to 10000 still gets a silent
  truncation at 10000.
