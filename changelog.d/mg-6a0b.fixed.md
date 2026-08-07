- **The triage-packet control asserts that a refused `mg done` PRESERVES the
  packet, instead of asserting that it destroys it (mg-6a0b).**
  `TestTriagePacketIsWrittenBeforeAnySuccessorExists` failed on clean `main`
  after mg-9259 landed upstream in macguffin, reddening `internal/agent` and,
  through `./build.sh`, the refinery gate on every pogo branch.

  **The failing assertion was a measurement of a defect, not a control over a
  property.** mg-1912 relocated the triage packet from `mg done --result` into
  the ticket body because `workitem.Done` ran the declared-remainder guard
  BEFORE writing the sidecar, so a refusal discarded the caller's `--result`.
  The test recorded that loss (`the refused mg done still wrote a sidecar ...
  the packet was assumed lost on refusal and it is not`) as evidence that the
  sidecar route was unavailable at triage time. mg-9259 — filed precisely to
  end that loss — writes the result before the guards run. The defect is gone,
  and a measurement of a defect that no longer exists is not a control.

  **pogo's expectation was the wrong half, and the test's own rationale is what
  establishes that.** The store-wide glob was justified on the grounds that a
  sidecar in `claimed/` is "a packet nobody can find ... exactly the stray class
  `mg sidecars` exists to report". Both premises were measured against the
  shipped `mg` and both are false: the refusal prints the sidecar's absolute
  path and states that it survived, and `mg sidecars` reports no stray — in
  `claimed/`, and in `available/` after an unclaim. The sidecar is a companion
  to its item, not a stray. The alternative reading — that writing on an error
  path is a partial mutation misrepresenting an incomplete item as complete —
  was checked and does not hold either: `mg show --json` reports `result: null`
  and `status: claimed`, and `mg list` files it under claimed. So the change
  belonged here and not in macguffin.

  **What replaced it is stronger than the inverted assertion, deliberately.**
  Flipping `len(hits) != 0` to `len(hits) == 1` would have made the gate green
  while asserting almost nothing: it passes on an empty file, on a paraphrase of
  the caller's payload, on a sidecar parked away from its item, and on one the
  retry then drops. The new `TestRefusedDoneKeepsTheResultItWasGiven` asserts
  that THE WORK SURVIVES — the sidecar sits beside the item in the directory the
  item is actually in, carries the caller's keys and values rather than a
  paraphrase, is named in the refusal so an operator watching a non-zero exit
  knows it was not lost, is not reported stray, and is still intact in `done/`
  after a retry that does not re-supply `--result`.

  **It was verified to be a control rather than a rubber stamp**, by building
  `mg` at the commit before mg-9259 and running against it: the new test fails
  there and passes against the shipped `mg`. `TestTriagePacketIsWrittenBefore-
  AnySuccessorExists` passes against BOTH, which is the evidence that these are
  two independent guarantees that had been conflated in one test — mg-1912's
  (the body route works with no successor in the store) and mg-9259's (a refusal
  costs a retry, never the work). The merged test broke when only one of the two
  axes moved; split, each fails for exactly one reason.

  The retained test keeps its name and its premise check: `mg done --result` is
  still refused at triage time, which is still why the packet is written to the
  ticket body, so the relocation cannot silently outlive its cause.
