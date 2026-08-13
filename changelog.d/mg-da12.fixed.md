- **The coordinator is told when an item it filed completes by merge — the skip
  that spared it named the wrong artifact (mg-da12).** pogod's Creator-notify
  had a skip scoped, carefully, to the merge route: if the item's creator was
  the coordinator, send nothing, because *"the refinery already mails the
  coordinator on every merge"*. The scoping was not the defect. **The premise
  was.** The refinery mails a MERGE — `MERGED: mr-… (branch=polecat-p687f)`,
  branch and target and SHA — and the Creator-notify mails a VERDICT. One says a
  branch landed; the other carries what the worker concluded, the result sidecar
  verbatim. They are different artifacts and the skip treated them as
  substitutes, so the coordinator became **the single filer this never reached**
  — re-measured at fix time, `mayor`'s mailbox holds 11,378 mails, 56 of them
  from pogod, and **zero** `COMPLETED:` notices; the ten that exist fleet-wide
  are addressed to pm-onethird, doctor, pm-pogo and architect, and to no
  coordinator. It files more items than anyone on the box. The skip is gone. The duplicate it avoided
  is one extra mail about one event; the duplicate it bought was a verdict
  reaching nobody.

  The one remaining "they already know" skip — the filer is the worker — is left
  standing, and the difference is worth stating because it is the whole finding:
  its premise is about the recipient's **own act**. The worker wrote the verdict;
  the mail would hand it back what it produced. "Some other message covered it"
  is a different assertion, and it is checkable — which is why the comment now
  requires any future skip of that shape to name a message carrying **this**
  content, rather than one that merely fires on the same event.

  **Two docs asserted the removed behaviour and are corrected with it**, because
  a justification describing a world that no longer holds is this ticket's defect
  one layer out. `pogo check-verdicts` explained in its `--help`, and printed on
  every report, that a coordinator's merge-route rows *"are dropped here by
  construction"* — true while the skip existed, and it is the sentence that made
  the gap look intended. Both now say the skip is gone and those rows are
  measured like every other filer's.

  How it surfaced is the part worth not repeating: not by inspection. `pogo
  check-verdicts` flagged three of the coordinator's own filed items landing with
  no verdict reaching it, all three recovered by hand from `git log --grep`, and
  only then did anyone go looking for why. **The absence of a notification
  produces no artifact, and the one agent positioned to notice the gap was the
  one the gap was defined against.**
