- **A full disk stops being reported as a defect in the branch (mg-b41f).** The
  refinery's no-retry reasoning for a gate — *"the gate ran on this tree and
  returned a verdict, re-running establishes the same fact"* — is true for a
  deterministic failure and false for an environmental one. On 2026-08-12 the
  boot volume hit 255 MiB free, `./build.sh` failed, and the merge request was
  recorded `class=defect`, not retried, with a summary naming 50 packages and
  `TestStallWatchGate_BootDirections` by name; the gate's own output said `no
  space left on device` twenty-five times, one level down. After `go clean
  -modcache` freed 7.3G the identical branch merged clean, so the verdict was not
  reproducible — exactly what the rule assumed it was. There is now a `host`
  class: not a defect, not counted against the author, not retried automatically
  (a full disk is not restored by waiting, so a retry burns a whole gate run on
  the single merge slot to re-derive the same fact), and the notice names the
  disk instead of the tests. The refinery also `statfs`es the gate's own
  filesystem and prints the reading — **including when it disagrees** with the
  wording, since a gate's scratch directory is freed when it exits. The class is
  decided from the text before the stored output is capped, because an incident
  whose ENOSPC lines fell in the elided middle would read back as a clean build
  failure. The no-retry rule for genuine defects is unchanged, and the signal
  table holds only wordings measured to have occurred in this fleet's gate output
  — `exit status 137`, ENOMEM and `disk quota exceeded` each counted zero and are
  deliberately absent.
