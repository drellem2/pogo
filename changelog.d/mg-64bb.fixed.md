- **Work that ALREADY EXISTS outside an item read `available` — and after
  mg-4bf1 held it back, the notice holding it still said "submit this" about a
  merge that was already running (mg-64bb).**

  The board has one word for two states. An item's claim is released when its
  polecat stops; the item is closed when its merge lands. Between those two
  events the work is submitted and in the refinery queue, and the item reads:

  ```
  mg-8d25   status=available   MR processing
  mg-84f0   status=available   MR queued
  mg-a19a   status=available   MR queued
  ```

  mg-4bf1 closed the half of this that mattered most: an item whose work sits on
  a pushed, unmerged branch is now dropped from both of stall-watch's dispatch
  checks and re-reported with the opposite instruction. **A branch in the merge
  queue is pushed and unmerged, so it was caught by that exclusion too — and
  rendered as though nobody had submitted it**, including the paste-ready
  remedy:

  ```
  pogo refinery submit polecat-pa19a --repo=/Users/daniel/dev/pogo --author=mg-a19a
  ```

  Measured on `mg-a19a`: four notices across ~36 minutes while
  `mr-dacudtqtjv1hjkm21420` was `processing` or `queued` throughout. The refinery
  has **no dedup**, so that line merges the same work twice. Meanwhile `pogo
  check-stranded` — which the same notice names as the place to look — was
  calling that branch `in_flight` and saying *wait*. Two components of pogod
  contradicting each other about one item is the finding mg-4bf1 exists for,
  reachable through mg-4bf1's own repair.

  **The question has two yeses and they take opposite instructions.** *Does work
  for this item already exist outside the item?* — yes, pushed and abandoned,
  which needs somebody to submit it; or yes, already merging, which needs
  everybody to leave it alone. The `stallwatch.Stranded` probe now consults the
  refinery queue (`QueueWithProcessing` — the same population `/refinery/queue`
  serves and `check-stranded` reads, in-process through a thunk so an
  orchestration restart cannot leave it answering from a refinery nobody uses)
  and attaches the merge request to the branch.

  **The exclusion is unchanged; the remedy is what moves.** The item was already
  withheld from both dispatch checks and still is — suppressing the row would
  drop the do-not-dispatch instruction with it, which is the mistake mg-4bf1 had
  to undo in `check-stranded`. What changes is that the notice names the MR and
  its status verbatim (`queued` and `processing` are different answers to *how
  long must I leave this alone*), says there is nothing to submit and nothing to
  dispatch, and prints no submit line. The event carries `queued_mr` /
  `queued_status` per branch, so "held because its work was abandoned" and "held
  because its merge is running" are countable apart in `events.log`.

  **With the queue unasked, every branch reads as un-submitted** — the state that
  gets a submit line. So `queue_consulted` is stamped on the event and stated in
  the notice. It rides on the stranded-push notice rather than the dispatch
  notices, unlike the repo-listing caveat: an unasked queue cannot cause an item
  to be missed, only its remedy to be wrong, so it is told to the reader being
  handed that remedy (mg-8baa's collapse, in the direction where the damage lands
  on the remedy).

  **The remedy is checked against the defect it repairs, and the existing guard
  did not cover this direction.** mg-4bf1 guards the submit line against a branch
  that is *not* on origin, where `pogo refinery submit` refuses (mg-586d) and the
  failure is loud. A queued branch fails the other way: the command **runs**, and
  what it produces is a duplicate merge of work already in flight.

  **Not fixed here, and named rather than left implied.** The ticket's second
  instance also ran during an explicit fleet pause. There is no fleet-pause state
  in this codebase for a surface to consult — the pause was expressed by gating
  an item on another (`--depends=mg-bcaa`) — so nothing was added for it; the
  mayor's own note records that the board had no way to express it. And an item
  whose release condition is *its own merge* still cannot say so in `mg`'s own
  fields: this change makes stall-watch stop advertising it, not the board stop
  showing `available`.
