- **A `MERGE FAILED` notice is addressed to the AGENT that owns the branch, not
  only to the work-item id (mg-1fcc).** The refinery mailed `mr.Author`, which
  for a polecat is its work item (`mg-32e3`). mg canonicalizes only the `mg-`
  prefix, so that resolves to the box `32e3` — while the running agent reads
  `c32e3`. Different Maildirs. On 2026-08-10 two such notices sat unread in the
  work-item boxes while both polecats were alive and reading their own inboxes.
  The refinery now resolves the agent from the branch name (`polecat-c32e3`),
  falling back to pogod's registry, and delivers to both boxes.

  **This is a REDUNDANCY repair and is deliberately scoped as one.** No work was
  ever stranded by it. The polecat protocol mandates a poll of
  `pogo refinery show`, and that loop finds the failure *at failure time*, which
  mail latency cannot beat. Four for four recovered without the notice: c6d7b in
  12m43s unprompted, c3a96 in 3m unprompted and uncontaminated, c32e3 and cdb58
  in ~18m — both mid-outage, so their polls were failing too, and c32e3
  self-reported that it learned by polling rather than by the mayor's nudge. The
  filing mayor's original "delivered to a mailbox with no reader" framing was
  withdrawn by the mayor itself: polecats are told to read both boxes, so the
  notice was reachable, just not addressed. What is left is the narrower
  exposure c32e3 named — *an author who polls finds out at failure time; an
  author who has finished polling (or was stopped) finds out never* — and that
  population has no channel at all. Fixing the address means the read-both-boxes
  convention stops being load-bearing.

  **The branch is asked before the registry**, which is the whole point of the
  ordering: the registry answers only for a live agent, and the exposed
  population is precisely the one it has forgotten. A branch name survives its
  agent, and so do mailboxes.

  **The common case is one box and is not mailed twice.** An agent named after
  the bare suffix of its item (`mg-9a19` → `9a19`) already reads the box the
  author name resolves to; the recipient list is deduplicated on the Maildir each
  name canonicalizes to, so this does not double every failure notice in the
  fleet. A crew agent or human author names itself, so nothing is added there
  either.

  **A notice that reaches no agent-owned mailbox is now an event**
  (`refinery_fail_notice_unrouted`), rather than a silent successful-looking
  delivery — the second half of the ticket. It fires in both shapes: no agent
  name could be derived at all, *and* a name was derived but `mg mail send`
  refused it (`no_such_mailbox` is exit 3 since mg-d639). That second case is
  this fix applied to itself — a repair whose own extra send can fail while the
  code logs and moves on would reproduce, one layer up, exactly the defect it
  repairs. The healthy path stays silent.

  The `polecat` prompt template now says outright that the poll loop is the
  channel that works and the mail is redundancy, so a future polecat does not
  substitute mail-watching for the loop it is required to run.
