- **A polecat reads BOTH mailboxes its mail can be in, and registration stops
  refusing the one its mail is actually in (mg-4f8c).**

  mg has **no mailbox registration**. A box is created on **first delivery**, so
  an agent's inbox is whichever name its **senders** happened to type — it is not
  a property of the agent at all. mg-aa96 read that fact as "the mailbox is the
  agent name" and encoded it as an **equality**: `Scheduler.Add` refused any
  mail-check naming a mailbox other than its own agent, and the templates were
  changed to name only `$POGO_AGENT_NAME`.

  That over-corrected, and the over-correction is its own silent failure. Agent
  `ba465`'s mail was sitting in the **work-item** box, because that is what its
  correspondents typed. Forbidding that box did not stop mail arriving there — it
  only guaranteed nobody would ever open it, and made the one schedule that would
  have drained it unregistrable. The rule that actually holds is **read both,
  every time**; which box holds the mail is not knowable from inside the polecat.

  **What changed**

  - **The registration guard is a MEMBERSHIP test, not an equality test.** A
    mail-check must open the agent's own box; naming others alongside it is
    fine and is now what every template prescribes. The row mg-aa96 caught stays
    refused:

        agent p4f8c, message names p4f8c and mg-4f8c  -> accepted (the new form)
        agent p4f8c, message names p4f8c only         -> accepted
        agent p4f8c, message names mg-4f8c only       -> REFUSED (2026-08-05's defect)
        agent p4f8c, message names no mailbox         -> accepted

  - **The parser returns every `mg mail list <box>` in the message, not the
    first.** Returning one was the same one-answer-per-question assumption that
    caused the original bug: it would have left the guard, and the stranded-mail
    sweep, blind to the second box.

  - **The mailbox identity moved to `internal/mailbox`, a leaf package.**
    `internal/scheduler` already imports `internal/agent`, so the agent side —
    which WRITES these messages — could not import the guard's canonicalizer and
    would have needed its own copy. A second canonicalizer that drifts by one
    `mg-` prefix reproduces this bug exactly. `scheduler` keeps its exported
    names as thin wrappers, so there is one implementation of "same mailbox?" in
    the tree and schedule code still reads in schedule terms.

  - **`pogo check-strandedmail` understands a two-box mail-check.** It took the
    first polled box only; under the new template that would have reported the
    second as stranded on **every healthy polecat**. A sweep that cries wolf on
    the healthy majority is muted before the real finding arrives — which is how
    the mail-check it protects goes back to being unwatched.

  - **The six polecat templates and pogod's spawn-time nudge name both boxes**,
    and name the two traps that make every failure here read as something else.

  **The three messages that are not the same message.** All of them exit 0:

        No mailbox for p4f8c yet — ...   that NAME has never received anything.
                                        Possibly the WRONG name.
        No unread messages for p4f8c     the box is REAL and EMPTY.
        (under --json)                   BOTH emit nothing at all.

  The prose differs; nothing downstream can use the difference, and anyone
  diagnosing a silent loop reads the first as the second. That is how `bf3ae`'s
  review loop with `v9ecf` stalled for ~40 minutes with **both ends healthy**:
  the reviewer looked slow (waiting on a push nothing would prompt), the builder
  looked unresponsive (working, reading an empty mailbox), and `pogo agent list`
  showed both healthy, because both were. The mayor diagnosed "bf3ae is not
  reading its mail" and nudged out-of-band; that unwedged it, but the diagnosis
  was wrong — bf3ae **was** reading its mail, from the box it had been told was
  its own.

  **Two traps the templates now name, because both read as something they are
  not.**

  - **mg refuses a cross-box read without `--force`, and that guard fires on your
    OWN inbox** whenever the box is named for the work item — it compares against
    `$POGO_AGENT_NAME`, which your work-item box is not. It reads like a
    permissions error and is not one. A polecat meeting it concludes it is not
    allowed to read its own mail, and leaves the mail unread.
  - **`mg mail send` to a name nobody has used creates a phantom box and reports
    success.** There is no such thing as a bad address. bf3ae's four mails to
    `9ecf` (the reviewer is `v9ecf`) vanished into one. The mayor observed
    "(new mailbox created)" five times that night and reasoned it away as normal
    for a first mail — which it is, and which is exactly why it cannot be
    distinguished from a typo creating a dead drop. So the templates say: take the
    recipient from the `From:` of their mail or from `pogo agent list`, never an
    inferred id, and `ls ~/.macguffin/mail/<name>` before an important send.

  **Every assertion here was shown to fire.** Reverting the guard to mg-aa96's
  equality fails the both-boxes test; loosening it to "names at least one box"
  fails both the own-box test and all eight of 2026-08-05's live mismatches;
  returning only the first parsed box fails the parser test; dropping the
  work-item box from the nudge or the templates fails the message and template
  tests.

  **Not fixed here, and it is the send side.** Making `mg mail send` refuse an
  unregistered name — the ticket's fix (3), and the only thing that would have
  saved bf3ae's four mails — is a change to `mg` itself, which lives in the
  macguffin repo, not this one. It also needs care rather than a flag:
  "unregistered" is not a concept mg has, so the refusal requires adding the
  registration first. Same for the wording and `--json` distinguishability of
  `mg mail list` on an unknown box (fix (2)), and for teaching the cross-box read
  guard that a work-item box can belong to the reader. Filed as **mg-d639**
  against macguffin; this changelog entry is the pogo half.

  The ticket's own positive control — address a mail to a name that does not
  exist and observe the failure — therefore **still fails**, and is recorded here
  rather than quietly omitted:

      $ mg mail send definitely-nobody-9ecf --from=tester --subject=x --body=y
      Delivered: tester → definitely-nobody-9ecf/new/1786108465118943000.77277.3000  (new mailbox created)
      $ echo $?
      0

  What landed here makes a misdelivery **recoverable** — both candidate boxes get
  read, so mail sent to either is seen — and makes the traps that disguised it
  legible. It does not make a wrong address impossible. Until mg-d639 lands,
  every address still succeeds.
