- **The pre-deploy durability check raced the warning that preceded it, and the
  instruments it prescribed could not see a fresh merge request (mg-5b5e).** The
  procedure is *warn the {{.Worker}} to push → wait → check whether it left
  durable work → stop*. The warning is an instruction to create the very state
  the check tests for, so the interval between the check and the stop is exactly
  when a push lands. On 2026-08-20 that interval was 19 seconds: the sweep read
  `queue=0 history=0` at 00:51:23Z (true at that instant), `p8188` submitted
  `mr-da34v72tjv1k9tlmubpg` at 00:51:40Z, the stop went out at 00:51:42Z, and
  `mg-8188` was recorded as having "left NO state" and being "safe to dispatch
  fresh". The MR merged at 01:03:47Z. The {{.Worker}} did exactly what the
  warning asked and was credited with having done nothing.

  Nothing was lost — the ordering fails **safe** in the stopping direction, since
  a zero reading only ever leads to warn-and-wait. The cost fell on the RECORD,
  which is the surface the next reader acts from. The coordinator directive now
  carries a `Before you stop a live {{.Worker}}: the durability check` section
  under The Refinery, and the stall-escalation stop points at it rather than
  carrying a second copy.

  **What it says, and why each part is load-bearing:**

  - Run the check **per agent, as the last act before that agent's stop** — never
    once over a fleet, never on the near side of a warn-and-wait interval.
  - **That narrows the window; it does not close it.** Any check preceding an
    action is stale by construction. The directive states the limit rather than
    designing around it, because a remedy claiming atomicity is the same defect
    one layer up — the next operator writes an unqualified "left no state" from a
    reading they were told was exact.
  - **Never write a universal into a ticket from a pre-stop reading.** Record
    what was measured, when, and with which view. pogod's `[stranded-push]` mail
    fires inside pogod *after* the process is gone, so it cannot be raced; when
    it contradicts a pre-stop note, the mail wins.

  **The second defect is instrument coverage, and the obvious remedy repeats
  it.** The proposal in the ticket — read the last `refinery_merge_attempted` for
  that author from `~/.pogo/events.log` — cannot see a merge request that is
  submitted but still QUEUED, because no attempt event is written until a gate
  picks it up; behind a running gate that is tens of minutes of blindness in
  place of the ~minute it was meant to fix. So the directive prescribes **both**
  views, keyed on the author, and names what each is blind to: `pogo refinery
  queue` covers submit→merge and is blind to anything finished; `pogo refinery
  history --since=<window>` covers first-attempt→completion from the durable
  event log and is blind to a queued request; bare `pogo refinery history` is a
  100-entry retained window over completed merges only.

  Two further corrections the fix carries:

  - **`pogo refinery queue` no longer hides the in-flight row.** That was true
    until mg-0c51 (`ef18372`, 2026-07-30) and is the origin of the advice to
    distrust it — but whether the fix is in the daemon you are *asking* is the
    only form of the question that does not rot, so the directive gives the
    ancestry probe (`/version` → `git merge-base --is-ancestor ef18372 <rev>`)
    instead of an answer. Measured 2026-08-20: the running pogod was `c6091d3`,
    of which `ef18372` is an ancestor — so on this box `queue` was the *more*
    complete of the two views, and a directive routing around it would have sent
    the reader to the one that cannot see a queued MR at all.
  - **Zero in both views is "no merge request", not "left no state".** A
    {{.Worker}} that pushed a branch and never submitted it is durable work
    invisible to both — `pogo check-stranded` before the stop and the
    `[stranded-push]` mail after it are that case's instruments. A stop is still
    safe on a zero reading; what a zero reading does not license is the sentence
    in the ticket.

  Pinned by three corpus tests over the embedded prompt, each verified to fail
  when its claim is removed: the ordering rule with its residual-race admission,
  both views with their named blind spots, and placement — the section lives with
  the instruments it prescribes and the stop site points at it by name and
  carries no second copy of the instrument. That last arm is the ticket's own
  retirement condition made mechanical: two copies of one instruction drift.
