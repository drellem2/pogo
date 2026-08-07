- **Spawning a polecat registers its mailboxes, so a worker is reachable by mail
  from the moment it starts (mg-7dc1).**

  mg-d639 made `mg mail send` refuse a recipient nothing has registered
  (`no_such_mailbox`, exit 3) instead of filing the mail for it. That is the
  right fix. This is about what had been **leaning on the old behaviour**:
  nothing in pogo ever provisioned a polecat's inbox, because the first sender
  created it, so the omission had never been visible.

  **The measurement.** Checking every recent directory under `~/.pogo/polecats/`
  against the registered-mailbox list (1,261 boxes, so the instrument was live):
  **10 of the 12 most recent polecats had no mailbox under any name.** The two
  that did were the two someone had already tripped over and repaired with
  `--create` that same evening — repairs, not survivors — so the population was
  effectively 12 of 12. Both name forms were absent, the agent name **and** the
  work-item id, which rules out a naming mismatch: the box did not exist until a
  sender happened to make one. Mail is the review-loop transport on the gh-issue
  track (mg-4f8c), so from mg-d639 onward every polecat was unreachable from the
  moment it spawned. Three cases surfaced within 20 minutes only because three
  people happened to mail three polecats; the other nine were never written to
  and so never complained.

  **What changed**

  - **`handleSpawnPolecat` provisions the polecat's mailboxes** via
    `mg mail register` (`client.RegisterMGMailbox`), before registering the
    mail-check loop that reads them.
  - **The set of boxes is DERIVED from the mail-check nudge**, not written down a
    second time (`polecatMailboxes` parses `PolecatMailCheckMessage`). The
    constraint on this fix was that provisioning must create whatever set the
    polecat's own instructions tell it to read; a second derivation that drifts
    by one `mg-` prefix or one dropped box satisfies it on the day and fails
    silently later, in the direction that is hardest to see — a provisioned box
    nobody opens, or an opened box nobody could provision. A test asserts the two
    lists are equal rather than asserting either literal.
  - **Non-fatal but not silent.** The polecat is already running by then, so a
    failed registration is logged and emitted as `mailbox_register_failed` rather
    than failing the spawn; every box is attempted even if an earlier one failed.
  - **The deploy runner registers its two alert recipients up front**
    (`register_alert_recipients`, after `resolve_mg` and before the first abort
    that alerts). `resolve_mg` proves the alert path can *run*; since mg-d639
    that is a different question from whether it can be *delivered*. Never fatal
    — its failure says nothing about whether the deploy can proceed.
  - **Six polecat templates stopped teaching the removed behaviour.** They told
    every worker that a send to an unused name "creates a brand-new empty box and
    reports success — there is no such thing as a bad address", and that mg has
    no mailbox registration. Both were true when written; neither is now. A
    prompt is the operating instructions an agent acts on, so one describing
    removed behaviour as live is a live defect, not stale documentation.

  **`--create` is deliberately NOT the fix, at any callsite.** It is one word
  away and it would have been a smaller diff. `--create` on a send says "deliver
  to this name whether or not anyone meant it", which is precisely the
  phantom-mailbox behaviour mg-d639 removed, re-entered under a new name: a typo
  in a recipient goes back to being invisible. It would also still leave every
  polecat unreachable to any caller a sweep missed. Registering at spawn keeps
  `--create` what mg-d639 intended — a rare, deliberate act for a genuinely new
  correspondent — so that a refusal means *you typed the name wrong* rather than
  *this recipient was never provisioned*. Tests assert the absence of `--create`
  on both the Go and shell provisioning paths, because the two fixes are
  otherwise indistinguishable from a green build.

  **The open question in the ticket, answered.** Whether a failed alert cascades
  into the deploy's own exit handling: it does not. The runner sets `set -u` and
  not `set -e`, and every `alert()` callsite is followed by an explicit `exit`
  that does not read the return value. Both halves are now pinned by tests —
  including one that walks each multi-line callsite to the end of its command,
  because a grep of the lines matching `alert "` sees only the first line of a
  twelve-line call and would report the property as held either way. That probe
  has its own positive control: it is re-run against a copy of the runner with
  the cascade injected, and must object.

  **The residual risk this does not remove.** A failed alert costs the run
  nothing in exit code, which is the good news and the bad news — a run whose
  alert was never delivered exits with the code it would otherwise have had. The
  delivery guarantee therefore has to come from the recipients existing, which is
  what the up-front registration is for.
