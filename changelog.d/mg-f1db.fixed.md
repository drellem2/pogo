- **A failed push to the target branch no longer wedges later merges through the
  same clone.** The refinery keeps one persistent clone per repository and reset
  the source branch on every attempt, but not the target. When a local
  fast-forward merge landed and the push that followed failed — a protected branch,
  a transient remote error — the local target was left ahead of origin and nothing
  rolled it back. That attempt and every later merge request reusing the clone then
  failed their fast-forward pull with "Not possible to fast-forward", a message
  naming neither the real cause nor a remedy, and were classified as not worth
  retrying. The target is now fetched and force-reset to origin at the start of
  each merge attempt, mirroring what the source branch already did, so a clone left
  in that state repairs itself; a fetch or reset that fails is retried rather than
  treated as fatal.
