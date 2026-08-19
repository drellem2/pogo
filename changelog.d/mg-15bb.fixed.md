- **A gate that banners its own SETUP FAILURE stops being `class=defect` and
  gets a class of its own, `setup` (mg-15bb).** The record this replaces,
  `mr-da2ls4qtjv1vk5gh57n0` on 2026-08-19, read back verbatim from
  `~/.pogo/refinery-state.json`:

      Status:  failed
               DEFECT — establishes a fact about the branch. A fix is warranted.
      Error:   quality gate: ./build.sh failed
               [test setup failed, not the branch: PASS: a sandbox HOME that is
                a symlink to the developer's home: prints the SETUP FAILURE banner]
      retried: NO — the build gate ran on this tree and returned a verdict —
               re-running establishes the same fact

  Two adjacent fields contradicting each other: the error says "not the branch",
  the caption says "establishes a fact about the branch", and the author agent
  acts on the caption. `classifyFailure` consulted `verdictStages` — a stage
  table — so `build` returned `defect` on the stage alone, while
  `summarizeGateFailure`, one function earlier in the same process, had already
  written the opposite into the string beside it.

  **What ships.** A `gateSetupError` built in `runQualityGates` from the full,
  uncapped gate output — the same shape `newHostResourceError` (mg-b41f) and
  `newGateNetworkError` (mg-67c9) already use, and for the same reason: the copy
  persisted on the merge request is capped at 8 KiB with its middle elided, and
  `classifyFailure` is handed only the one-line summary. Class `setup`,
  retryable, with its own 3-attempt budget (30s/2m backoff) separate from the
  four that already exist. Every attempt now records a **`retried_reason`** as
  well as a `not_retried_reason`; before this a retry that happened said only
  `retried: yes, after 30s of backoff`, while a retry that did not happen carried
  a full sentence.

  **This class asserts less than it could, and mg-67c9 is why.** That ticket
  looked at wiring `SETUP FAILURE` into the classifier and refused it in as many
  words: *"a branch can break its own test setup, so 'the envelope did not stand
  up' does not establish that its collapse was environmental."* **That ruling is
  not overturned.** What it rules out is a class saying the ENVIRONMENT is at
  fault, and no surface here says that — not the triage note, not the error text,
  not the retry reason. What `setup` establishes is narrower and undisputed: the
  gate returned no verdict on the tree. So the note refuses both captions —
  *"it did not condemn this branch and it did not clear it… the banner does not
  say WHOSE setup failed"* — and the author's consecutive-failure streak is
  untouched on exactly `indeterminate`'s reasoning, not on a claim about the box.
  `TestSetupClassNeverBlamesTheEnvironment` pins it.

  **The retry is a delay, not an erasure.** A branch that breaks its own setup
  breaks it again on every attempt, so the failure is still terminal and the
  record still carries all three attempts; what the reader gains is that it
  *reproduced*. The budget is the smallest of the gate-bearing ones and is
  **not sized against anything measured** — unlike the DHCP lease behind the
  network budgets, this fleet has no recovery distribution for a broken sandbox.
  Three attempts answers one question (broken once, or broken standing) at a cost
  of at most two extra gate runs on the single serial slot every queued merge
  waits behind.

- **The ticket's own account of the evidence was wrong, and the corrected version
  changes what needed fixing.** The line quoted as the cause begins `PASS:` — it
  is an assertion in `scripts/pogo-sandbox_test.sh` that went **green**, and the
  words SETUP FAILURE are the *name of the banner it asserts gets printed*. The
  run's real failure was a network positive control, marked unambiguously in the
  same record's gate profile. So the refinery had not merely mislabelled a real
  setup failure: it attributed the failure to a check that succeeded.

  **That half was already fixed and was not running.** mg-67c9 landed the guard
  (`isHarnessVerdictLine`) in `d1a57e5` against the identical specimen five days
  earlier. Measured here on 2026-08-19: the pogod serving that merge reported
  revision `1ebf2dc`, **25 commits behind `main`**, and
  `git merge-base --is-ancestor d1a57e5 1ebf2dc` exits non-zero — the fix is
  merged and not deployed. Anyone reading a record like this one should check
  `curl -s http://127.0.0.1:10000/version | jq -r .revision` against the fix
  before concluding the fix does not work. The new class inherits that guard
  rather than re-deriving it, and `TestTheRecordedSpecimenIsNotASetupFailureAtAll`
  pins that the 2026-08-19 specimen is **still** `defect`: a remedy that took a
  red gate, called it a setup failure, retried it and cleared its author would be
  strictly worse than the misclassification it replaces.

- **`setup` is the last of four carve-outs across the gate-output boundary, and
  the ordering is stated rather than accidental.** A kill (timeout, signal)
  outranks it, because a kill says nothing about whether the branch caused the
  hang. `host` outranks it, because a full disk can BE why a sandbox failed to
  stand up and "free the resource first" is the instruction that works.
  `gate-network` outranks it, because a same-line module-fetch failure is the
  narrower statement. Every preference hands the case to a more specific answer
  and two of them hand it to a class that does not retry — the safe direction for
  a text-matched carve-out that spends gate runs. `summarizeGateFailure`'s setup
  clause moved below its network and host clauses to match, so the caption and
  the class can no longer disagree; mg-3412's requirement (the banner beats the
  assertion names underneath it) is untouched.

  **Not covered, and left open rather than papered over:** a suite that prints a
  real banner from a passing path *without* a `PASS:`/`FAIL:`/`PROVED:`/`SKIP:`
  prefix would still be misread. Nothing in this change detects that. The
  ticket's other observation — that the **gate profile** already marks the
  failing leaf step, unambiguously, in the same record, and the classifier used
  substring matching on prose instead — is also not implemented here; it is a
  larger change than the four scoped items and is left open.

- **The remedy carried the defect, and the first draft shipped it.** A remedy is
  an artifact of the same kind as the thing it remedies, so the enumeration was
  run against this change. It found one: `gateSetupError`'s headline read
  *"FAILED IN ITS OWN SETUP, NOT ON THIS BRANCH"* — a clearance of the branch —
  three sentences above the paragraph explaining that the banner does not say
  whose setup failed. That is a record contradicting itself in two adjacent
  fields, which is the sentence this ticket was filed about, reproduced inside
  its own fix. The headline now reads *"FAILED IN ITS OWN SETUP AND RETURNED NO
  VERDICT ON THIS BRANCH… NOT A FINDING AGAINST THE BRANCH AND NOT A CLEARANCE OF
  IT"*, and `TestSetupHeadlineAssertsOnlyWhatTheClassEstablishes` pins it. A
  caveat in paragraph three does not reach the reader who forwards the headline,
  which is why the check is on the first line and not on the whole string.
