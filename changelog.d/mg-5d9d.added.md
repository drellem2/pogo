- **`pogo check-carriers` re-reads the CURRENT state of every live gh-issue
  carrier's issue, and pogod runs the same re-read hourly (mg-5d9d).** A carrier
  records that an issue was noticed **once**, and nothing re-read the issue
  afterwards — so the carrier's existence was evidence about the past that read
  as evidence about the present.

  **Three instances, found on 2026-09-07 by three unrelated accidents and no
  instrument.** They are one defect with three symptoms, which is why this is one
  detector and not three:

  ```
  not ACKNOWLEDGED   #159 / #160   carried, and days later nothing on either thread
  not TRIAGED        #156          carried and still at `stage: triage` 17 days later
  not still OPEN     #127          carried against an issue closed the SAME DAY,
                                   then dispatchable work for a MONTH
  ```

  **`check-intake` read "44 carried, 0 uncarried" throughout, and it was right.**
  It measures whether an issue has a CARRIER — our bookkeeping — and once one
  exists the issue leaves its population forever. The gap is the AXIS, not the
  accuracy. From the reporter's side, carried-but-silent and uncarried are the
  same experience: they filed something and nothing appeared either way, so the
  one number anybody watched was measuring the side of the transaction that does
  not have a person in it.

  **The new check joins from the other side**, and it is the third member of a
  triple whose other two already shipped:

  ```
  internal/ghintake      an open issue with NO carrier             (the first step)
  internal/carrierdrift  a LIVE carrier whose issue has moved on   (every step after)
  internal/ghteardown    a DONE carrier whose issue stayed open    (the last step)
  ```

  **What it found on the live store on its first real run**, in 6.4s over 41 live
  carriers drawn from 175 work items: mg-e605 live against `drellem2/pogo#127`,
  closed 30d20h earlier — this ticket's own third instance, rediscovered by the
  instrument rather than by a coordinator checking a state before a dispatch —
  plus mg-4aa5 live against the closed `#111` (deliberate: a public correction is
  still owed on that thread, and it is the case the `gh-closed:` declaration
  exists for), and five carriers sitting at `stage: triage` for 31 days.

  **What counts as an acknowledgement is the TEXT, not the author, and that was
  measured rather than chosen.** Of the 40 issues live carriers pointed at, 40
  were filed by the repo owner and 39 had zero comments from any other login: the
  fleet's acknowledgements are posted by agents running under the owner's
  credential, so they carry the owner's login and `authorAssociation: OWNER` —
  the same two fields as the reporter's own comments. The obvious predicate,
  "a comment by somebody other than the person who filed it", would have reported
  38 of 40 carriers as unacknowledged on a fleet where every one of them had been
  acknowledged. Cry-wolf by construction. The text IS decidable, because the
  shipped triage prompt hands the worker the exact line to post.

  The acknowledgement bucket reads 0 today, and that is a fresh sweep rather than
  a blind check: 29 of those same 40 issues waited MORE than 24h for their first
  acknowledgement, eleven of them between 14 and 18 days, all cleared on
  2026-09-07 — the day this was filed.

  **The stuck-stage check covers only the stages a carrier is FILED at, and says
  so.** mg records no stage-change timestamp. For a carrier still at its filing
  stage the carrier's own age IS that stage's age, exactly; for any later stage it
  is only a lower bound, and reporting a lower bound as a measurement is the shape
  of mistake this whole ticket is about. `gated` is the specific exclusion worth
  naming — 27 of the 41 live carriers are waiting on a human GO/NO-GO, where the
  human owns the next move. The boundary is a default (`--stage`,
  `[carrier_drift] stages`), not a wall, and the report prints the covered set.

  **A deliberate state is declared on the carrier, one key per finding kind** —
  `gh-closed:`, `gh-ack:`, `gh-parked:` — so a declaration that an issue is
  closed on purpose cannot also silence a triage stuck for three weeks. A
  declaration buys silence from the alert channel, never invisibility: declared
  carriers stay listed, because suppressed-forever-and-forgotten is the same
  absence the check exists to catch.

  **REPORT-ONLY, and here that is a decision rather than a convention.** The
  cheapest imaginable fix for the acknowledgement case — post the ack
  automatically when a carrier is filed — was deliberately not built: whether this
  fleet posts automated comments on other people's issues is a decision for a
  human, not a detail of a detector. The package holds no seam through which an
  issue could be commented on, closed, or a work item edited.

  **The remedy is checked against the defect it repairs.** Two places where this
  could have rebuilt the thing it detects, and what stops them. Nothing is cached:
  every pass re-reads every carrier and every issue, and the only state the
  watcher keeps is a fingerprint and a first-seen map that decide whether to mail
  — neither can hold a cleared finding alive or suppress a live one. And the
  acknowledgement marker list is a COPY of text that lives in the shipped triage
  prompt; if that template is reworded and the list is not, the detector silently
  stops recognising acknowledgements — a fact captured once, read forever after as
  current. That is guarded by a test that reads the shipped prompt corpus and
  fails when the two diverge, not by a comment asking the next editor to remember.

  **The coordinator's existing stopgap keeps its place.** A `gh issue view <n>
  --json state` before every dispatch catches the closed case at the one moment it
  does the most harm, and is blind to the other two — it only ever looks at a
  carrier somebody is about to dispatch, and every acknowledgement and stage
  instance was a carrier nobody was about to dispatch. The two are complements.

  Exit 0 when nothing is actionable, 1 when anything is found, and 3 when NO
  carrier could be re-read at all — a broken instrument is not a result, and in
  particular it is not evidence that anything reported earlier has cleared.
