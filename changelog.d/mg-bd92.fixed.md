- **The dispatch-pairing gate's store-scan refusal stops saying "one of one of" (mg-bd92).**
  `waiverAdvice` supplies its own `one of`, and the fail-closed branch prefixed a
  second, so an operator whose store could not be scanned was told to waive by
  `tagging mg-f0b2 with one of one of "no-pair-required"`. Every test in
  `dispatchpairing_test.go` passed over it: the fail-closed test asserted the
  message said `REFUSING` and never read as far as the advice. Found by running
  the INSTALLED binary against a snapshot of the real macguffin store rather than
  a fixture — the branch only renders when a covered item's candidate scan errors,
  which no green test path reaches. The test now asserts the rendered sentence.
