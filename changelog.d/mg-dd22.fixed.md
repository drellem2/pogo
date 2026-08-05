- **The gh-issue teardown detector retries a network blip instead of turning it
  into a full batch of non-answers, and a run that measured nothing now says so
  instead of reporting a count that reads like a finding (mg-dd22).**
  On 2026-08-04 the watcher mailed **"12 indeterminate"** — every carrier it
  checked. Each line carried the same cause:

      gh issue view drellem2/pogo#NN failed: error connecting to api.github.com
      check your internet connection or https://githubstatus.com

  Re-running all 12 by hand from an authed shell minutes later resolved every
  one, and the batch was **not uniform**: 6 issues were closed (clean teardown)
  and **6 were still OPEN while their carrier was done** — exactly the finding
  this detector was built to surface, the original instance of which sat 4 days.
  One blip converted six real findings into twelve units of noise. It recurred
  **13-for-13 fifteen hours later**, re-burying the same six.

  **Two defects, and the second is the one that cost the signal.**

  - The failure was never **retried**. This box's network is ~50% intermittent
    (mg-0ffc); a detector sampling twice daily that treats one transient sample
    as a terminal verdict produces a full-batch non-answer on a *regular* basis,
    not an exceptional one. Twice in two runs is measured, not argued.
  - The failure was reported in the **same shape as a determination**. "GitHub
    answered and the answer is unusable" and "I never reached GitHub" both
    rendered as the token `indeterminate`, so a masked finding was
    indistinguishable from a real one. A reader who sees two identical
    "N indeterminate" mails in a row learns to skip them, which is precisely
    when the real finding arrives.

  **What changed**

  - **Every lookup failure now carries a class**, attached once where the raw
    `gh` text exists rather than re-derived by each reader: `network`, `auth`,
    `rate_limit`, `subject` (GitHub answered *about this ref* and the answer is
    not a usable state), `unclassified`. `gh issue view` exits 1 for an
    unplugged cable, an expired credential and a deleted issue alike, so the
    exit code cannot separate them and the message is all that differs — which
    is why today's outage had to be told apart from mg-03ea's auth gap by
    reading prose after the fact.
  - **Network-class failures are retried** with doubling backoff (3 attempts,
    2s then 4s), in `Retrying`, bound by default in the watcher *and* in
    `pogo check-teardown` so a hand re-run and the unattended run cannot
    disagree for want of a retry. **Only** network-class is retried: an expired
    credential, a missing issue and a rate limit are repeatable by construction,
    and re-running a repeatable failure reproduces it while spending the window.
    An unrecognised failure is **not** retried and is treated as an instrument
    failure — the loud direction, because calling an unknown failure a
    determination about a carrier is the exact collapse this ticket is about.
  - **A non-answer no longer shares a bucket with an answer.** `indeterminate`
    now means the instrument worked and its answer is unusable — a determination
    about the carrier, which re-running reproduces. The new **`not checked`**
    means the carrier was never audited because the instrument failed. Separate
    sections in the report, separate counts on the event
    (`blocked_count`, `failure_classes`), `not_checked` and a per-finding
    `class` in `--json`.
  - **A run in which no carrier reached a verdict is reported as a suspected
    instrument failure rather than as a result.** The report leads with the
    banner and denies being a result; the mail subject leads with
    `INSTRUMENT FAILURE — no verdict for any of N carrier(s)` instead of a
    count; `instrument_failure` lands on the event so "how often does this go
    blind?" is a query rather than a re-read of old mail; and
    `pogo check-teardown` exits **3**, not 1, so a schedule can separate "found
    something" from "could not run" without parsing prose. It takes **2**
    scanned carriers to make the claim — from a sample of one, a blind run and
    a blind carrier are the same observation, and asserting the difference would
    be inventing it.
  - **A blind run no longer resets the escalation clocks.** `trackAges` rebuilds
    its map from the current findings, so a run that saw nothing would have
    dropped every clock and handed each miss a fresh 72 hours on its return. On
    a box that blips every few days that is a standing mechanism for keeping a
    forgotten finding forgotten — the precise failure escalation exists to
    prevent. The clocks are carried across such a run; `oldest` is still taken
    only over findings present in the report, so the ESCALATED banner always
    refers to something the reader can see below it.

  **Both halves are proved, and the proofs fail without the fix.**
  `TestATransientBlipDoesNotMaskARealFinding` replays mg-07ba/pogo#89 with the
  observed error on the first attempt, and carries a permanent failing arm
  asserting the un-retried lookup really does swallow the finding — so the
  passing arm is attributable to the retry and to nothing else.
  `TestAGenuineIndeterminateIsStillReportedAndNeverRetried` constructs a deleted
  issue and asserts it is still `indeterminate`, still not-clean, and costs
  **exactly one attempt**. `TestTheMaskedBatchOf20260804IsRecovered` replays the
  whole 12-carrier batch — 6 clean, 6 real misses, one blip each — with the
  same failing arm. Three deliberate regressions were run to confirm the
  assertions fire for their stated reason: collapsing the class split (9 tests
  fail), disabling the instrument-failure verdict (3 fail), and making every
  class retryable (4 fail, including the genuine-indeterminate arm — which is
  invariant under the first regression, as it must be).

  The mg-03ea live control is extended rather than duplicated, since the ticket
  notes one control serves both. It was **run against real GitHub** on
  2026-08-05, and both arms report what they must:

      raw (no GH_TOKEN repair):  89=blocked 91=blocked instrument_failure=true
      repaired (ghtoken.Ensure): 89=closed  91=miss    instrument_failure=false

  The repaired arm is the verdict half the ticket asks for — a real answer
  against the known-CLOSED #89 and the known-OPEN #91. The raw arm is the new
  half: it is not enough that a blind run produce no verdicts, it must *say* it
  produced none. Both arms go through `RetryingLookup`, so a blip during the
  control cannot masquerade as the auth failure the raw arm asserts.

  **Not done here.** Whether the 6 still-open issues should be closed is a
  separate judgement per issue, is outward-facing, and is routed via
  mayor/Daniel. The sibling `internal/ghintake` detector has its own `gh` lookup
  and was left alone as out of scope; it is a candidate for the same treatment.
