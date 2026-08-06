- **The refinery retries a merge that failed on the network, records the
  transport and the raw error of every failing attempt, and says in the STATUS
  when a failure was not a verdict on the branch (mg-e5c2).**
  On 2026-08-05 a single-slot merge queue drained to `failed` three times in one
  evening. Thirty-one merge requests died, every one of them at the fetch step,
  before any code was examined:

      20:33:21  mg-fc8d   failed  attempts=1   fetch: ssh: connect to host github.com port 22
      20:33:22  mg-dd49   failed  attempts=1   same
      20:33:22  mg-c3a2   failed  attempts=1   fetch: unable to access 'https://github.com/...'
      ...

  `failure_count=1` on all of them, and **none was retried**. Recovery depended
  entirely on each authoring polecat still being alive and attentive enough to
  notice and resubmit — and three polecats died inside the same network window
  that failed their merges, leaving branches pushed, complete and orphaned.
  Relying on the author's survival to recover a merge makes recovery conditional
  on the very event that caused the failure.

  **The cause, with a control.** Wi-Fi (en1) lost its DHCP lease and
  mDNSResponder suppressed every unicast query — `Query suppressed for
  <github.com> (no DNS service)`. Suppressed-query counts inside the three
  failure windows were 54, 44 and 73; in every window where merges SUCCEEDED it
  was **zero**. configd corroborates: en1 went INIT at 20:30:40 and BOUND at
  20:34:02, and the first merge after the burst succeeded 32s after BOUND. A
  suppressed query fails instantly, which is also why twelve merge requests could
  fail inside one second on a queue that only runs one at a time.

  **What is retried, and what is not.** pm-pogo's ruling from mg-0d70 applies
  verbatim one layer up, and is encoded here as the discriminator rather than as
  a list: *would re-running plausibly give a different answer, for a reason
  unrelated to the code?* A fetch that cannot resolve a hostname never reached
  the branch, the base or the gate, so it has an opinion about none of them —
  retried, with backoff (2s, 5s, 15s, 30s), bounded at 5 network-class attempts
  and 90s of total sleep. A quality gate that ran and returned RED, a rebase that
  hit a conflict, a commit message that would close a GitHub issue — each of
  those reached the tree and returned an answer about it, and is attempted
  exactly once. The two budgets are separate from the pre-existing ff-only retry
  budget, so a network blip cannot consume the attempts that exist to absorb a
  lost race with another merge.

  Credentials sit deliberately across the two axes: a refused key establishes
  nothing about the branch — so nobody should be sent to read the code — but the
  same question gets the same answer on a retry, so it is classified
  infrastructure and *not* retried. Class and retryability are separate fields
  because they are separate questions.

  **The status, not only the error text.** A `failed` row reads as a verdict on
  the work, and thirty-one of them invited dispatching thirty-one fixes for
  defects that did not exist; only reading each error line prevented it. The
  confusion ran both ways in one evening — a real rebase conflict (mg-aa96) was
  treated as another network casualty until its error text was read. So
  `pogo refinery show`, `refinery history` and the failure mail now print
  `failed(infrastructure)` where they printed `failed`, with the triage
  instruction beside it, and the mail carries the class in its SUBJECT — the part
  of a mail that travels. An infrastructure failure also no longer counts against
  the author's consecutive-failure streak, whose escalation advises stopping the
  polecat: a DNS outage is not evidence about whoever was at the head of the
  queue.

  The machine-readable `status` field is deliberately UNCHANGED. Every polecat in
  the fleet breaks out of its poll loop on the literal strings
  `merged`/`failed`/`lost` from `refinery show --json | jq -r .status`; a new
  token there would leave a polecat spinning through the failure it was supposed
  to report. The class travels beside it as `failure_class`, and inside the
  human-facing label.

  **Every failing attempt records its transport and git's raw output, verbatim.**
  This is the requirement the incident's own diagnosis taught. Of the 31
  failures, 20 were ssh reporting `Undefined error: 0` and 11 were HTTPS
  reporting `Could not resolve host: github.com`, interleaved about 200ms apart
  in the same bursts (7+3, 6+3, 7+5 — reconciling exactly). The HTTPS half named
  the cause outright. Several readers, including the mayor, worked from the ssh
  subset alone, reasoned from what errno 0 must mean, and produced two confident
  wrong mechanisms over several hours — one of them a premise-level doubt that
  these were not network failures at all, and a refuted fd-leak hypothesis. A
  single-transport view is how that happened, so no surface here reduces a
  failure to one normalised summary line: the merge request, the failure mail,
  `pogo refinery show` and the durable event log each carry, per attempt, the
  transport, the git command as invoked, and the far end's exact words. The
  cross-merge-request view that would have ended it in minutes is now one
  command:

      pogo refinery history --since=6h --json |
        jq -r '.[].attempts[] | "\(.transport)\t\(.raw_error)"' | sort -u

  **A retry that is not attempted says why.** `not retryable: <reason>` is
  recorded on every terminal failure, including the retryable classes that simply
  ran out of budget — and the exhausted-budget wording still names the class, so
  a spent budget does not start reading as a verdict on the branch. A missing
  retry that says nothing looks exactly like a policy that does not exist, which
  is what it was.

  **Attempt counts are visible.** `failed once` and `failed after 5 attempts` are
  different records in the log, in `refinery show`, and in the mail. A retried
  SUCCESS names the attempt that won and the backoff it paid, because a silent
  retry converts a flaky night into an invisible one — and invisible is how this
  box's network became its dominant failure mode without anybody holding the
  evidence.

  **Proved by inducing the fault, not by reasoning about it.** The acceptance
  tests point a real git at an unresolvable `.invalid` host — a RESOLVER failure,
  which is what happened, rather than a blackholed address, whose per-attempt
  timeout is the opposite timing and is the fault that was wrongly imagined — and
  show the retry firing, bounding at the budget, and logging one line per
  attempt with its transport and raw error. The control alongside it: with the
  same branch, the same refinery and the same gate, restoring only the resolver
  makes the merge succeed. Its mirror image shows a failing quality gate
  attempted exactly once, still labelled a plain `failed`, still counted against
  its author.

  **How this fix could itself do harm, and what stops it.** Retrying creates one
  hazard that not-retrying could not have: `[gates] skip_on_retry` rests on the
  premise "gates already passed on near-identical code", and before this change
  that premise always held, because a fetch failure was terminal and every retry
  therefore followed an attempt that had reached the gates. A retried *fetch*
  falsifies it — attempt 1 dies before the gates exist — so a skip keyed on
  `attempt > 1` alone would have merged a branch no gate ever ran against. The
  condition now says what the premise claims (gates were reached at least once),
  and the test for it was run against the old condition to confirm it fires: it
  reports the gate never ran and the branch merged anyway.

  Three smaller ways this can misreport, recorded rather than hidden: a
  classification is made from git's wording plus the clone's origin URL, so a
  reworded git could fall through to `unclassified` (which is retried on a small
  budget and reported as unplaced, not folded into either triage class); backoff
  is time every queued merge waits behind, which is why the total is capped in
  seconds and not only in attempts; and the exhausted-budget path is the one
  where an infrastructure failure still ends as a failure, which is why its
  wording repeats the class rather than leaving a spent budget to read as a
  verdict.

  **Known boundary, stated rather than hidden:** a quality gate that fails
  because the GATE could not reach the network is classified a defect, because
  the gate ran and reported. Mining arbitrary gate output for network wording
  would make a red test that happens to print "connection refused" retry forever.
  The gate's output is preserved verbatim for the reader who needs to make that
  call.
