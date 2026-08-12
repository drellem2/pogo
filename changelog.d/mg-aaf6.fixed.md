- **A review ticket now DECLARES which build item it reviews, and pogod's
  done-reaper keeps that builder alive while the reviewer is running (mg-aaf6,
  drellem2/pogo#131).** Part (3) of gh#131, and the structural half: parts (1)
  and (2) fixed the prompts that told a builder to close its own item at PR-open,
  but an instruction is a request, and the report is that instruction-following
  failed twice. The reaper stops any polecat whose work item reads terminal after
  two minutes of PTY quiet, with no notion of an open review — so a builder that
  self-closed anyway was gone before its reviewer's findings landed, and the round
  died leaving nothing that said why.

  The declaration is a fourth carrier line, `reviews: <build ticket id>`, on the
  **review ticket**, written once by the coordinator when it files the ticket and
  **never cleared**. That is the design and not an omission: the rejected shape
  was a tag removed at the pass/abort transition, which is enforcement by
  instruction-following again, and a declaration someone must remember to clear
  is state whose drift leaves no artifact — forget it once and the item holds a
  dispatch slot forever, against the per-repo cap, with nothing anywhere saying
  so. A line written at creation cannot rot, because its lifetime is bounded by
  something else's: the exemption exists only while a review polecat is **alive**,
  so it evaporates on the next tick when the reviewer exits and no ceiling has to
  be guessed. The ceiling instinct was priced and disqualified — across 17 real
  between-round waits, 2 exceed `deferDoneBackstopTimeout`'s 15 minutes, so that
  ceiling would have reaped live work about one round in eight.

  Every other route to the same pairing was measured over the live store and
  fails. `depends` carries **dispatch** semantics and would gate the review ticket
  behind a build ticket that stays claimed through review, so the coordinator
  deliberately files no such edge (2 of 23 real review carriers have one). A `gh:`
  ref join is ambiguous the moment an issue is split into parts — gh#131 itself
  has two build carriers sharing one ref. A prose `mg-xxxx` scan resolves to the
  **wrong** item in 17 of 23 cases, because review bodies name the triage ticket
  too.

  It sequences after mg-27d4 for a reason that would otherwise have bitten
  silently: a carrier block one line below a lead-in sentence is out of the
  parser's reach, so before `CarrierUnreadable` existed the `reviews:` line would
  have been unread on roughly 15% of review tickets — the exemption would not have
  fired, the builder would have been reaped mid-review, and the failure would have
  looked exactly like the bug being fixed, with the declaration visible in
  `mg show` to any human who checked. Now such a ticket gates instead of
  dispatching past the guard.

  The guard keeps a **positive record**, because an exemption never granted
  through misconfiguration and one correctly not needed produce the same nothing:
  the grant is logged once when it starts, and the eventual reap says the polecat
  had been exempt and names the reviewer that is now gone. A probe that cannot
  read a declaration logs that too, rather than reading it as an absent one.
