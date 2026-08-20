- **The nightly's FAILURE path does write the fleet-bounce counter — measured
  end to end for the first time, with the arm that crosses the threshold and
  invokes a bouncer (mg-62eb).**

  The question was live and reasonable. On 08-17, 08-18 and 08-19 the nightly
  aborted at the transport with class `unclassified` — which
  `transport_streak_verdict` puts explicitly in the **bump** arm — and
  `$POGO_HOME/deploy-transport-streak.stamp` was **absent**, with zero bounce
  events against a control of 251 `deploy_nofire`. The dates are explained
  (the fallback merged 11:27Z on 08-19 and the installed runner did not carry
  it until 12:00Z, so there was no mechanism to have fired), but the
  explanation retires only the dates. It leaves the mechanism never once
  exercised, and 2026-08-20's deploy **succeeded** — proving the SUCCESS path
  writes the stamp (`2026-08-20 0 -`, mtime 03:00:11, the same second as the
  run's own `sync:` line) and saying nothing whatever about the failure path.

  **Answer: yes, at `pogo-deploy.sh:3759`, and it is reachable.** Driven
  end to end against the real runner: a fresh box with no stamp plus a
  transport failure writes `<today> 1 -` at rc 10; a second consecutive night
  carries it to `2`; at the threshold the fallback fires and invokes
  `pogo-self-deploy bounce --yes --drain-timeout 3300`; a completed bounce
  resets the count to 0 and stamps the bounce date in field 3.

  **The one real qualifier, which reads from the source and is by design:** the
  write sits *below* `retry_will_follow`, so a fire that predicts a later fire
  tonight exits before it. Only the night's LAST fire can count a night. That
  is deliberate — no bounce while a 04:00 fire could still deploy properly —
  and 08-19 took the branch that mails `ABORTED`, which is downstream of the
  write, so that night would have reached it had the code existed.

  **What covered this before: three greps of the runner's own source for the
  LINE NUMBER of the `fallback_bounce` call** relative to `retry_will_follow`,
  the sync alert and `arm_run_deadline`. Those are assertions about where text
  sits in a file. They cannot distinguish a wired call from one whose enclosing
  branch is never reached, which was precisely the doubt.

  **And the write was already executing on every suite run, unread.** This
  file's own isolation note names `pogo-deploy.sh:3759` as one of the two
  writes that used to escape into the real `$POGO_HOME`, and names `run_e2e
  step` as the arm that fires it — so the failure path's write was discovered
  as a **leak**, by a wrapper somebody added to chase a different bug, and was
  never once asserted as behaviour. An effect observed only as a side effect is
  an effect nothing is holding to a contract.

  Eleven assertions added to `scripts/pogo-deploy_test.sh`, all four arms
  driving the real `main()`. `fallback_bounce` itself was already unit-covered
  — at the threshold, below it, refused, out of window — by calling it directly
  with a streak value. What none of those arms could show is the **join**: that
  the sync-abort path reaches the call at all, with a count the run derived
  from a stamp on disk. That join is what the ticket doubted. Polarity proved rather than asserted: with the
  `transport_streak_save` call at 3759 removed from a scratch copy of the
  runner, the suite reports *"a run that failed at the transport left NO streak
  stamp — the counter can never reach the threshold and the fallback can never
  fire"*, *"the first lost night recorded absent, not 1"* and *"the second lost
  night recorded 1, not 2"*. The accumulate assertion is the load-bearing one:
  `fallback_bounce` is handed the in-memory `$streak` from
  `transport_streak_next`, so **tonight's** bounce decision survives a broken
  save — what does not survive is the count carrying to tomorrow, which is the
  entire threshold.

  A polarity control runs in the same fixture: a night whose sync **reaches the
  tree** clears the count to 0 and never considers the fallback at all. Without
  it every assertion above is satisfied by a runner that writes the counter
  unconditionally and marches a healthy fleet to a fleet-wide restart.

  The stamp each arm reads is the one the RUN derives (`POGO_HOME="$E2E"`, no
  `POGO_DEPLOY_TRANSPORT_STREAK` override), not a path the suite composes — the
  same derive-instead-of-read slip that had two agents reporting the real stamp
  absent on 08-19 while a decoy sat at `$HOME` (mg-e121). `run_e2e` gained an
  optional 5th argument for the bootstrap repo, defaulting to exactly what the
  runner derives on its own, so the existing four-argument calls are unchanged.

  **Not claimed:** that a bounce would have ended the 08-14 outage. The fixture
  asserts the bouncer is invoked with the right vector, not that a real
  `pogo-self-deploy bounce` unwedges agents stuck on spinner frames. And the
  mechanism remains unexercised *in production* — this is a test that fires it,
  not a night that did.
