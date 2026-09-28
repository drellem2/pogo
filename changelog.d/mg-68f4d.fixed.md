- **`scripts/signal-sender_test.sh` Test 3 could signal the wrong process and
  fail with "no SENDER line at all" (mg-68f4d).** Its readiness check sent
  SIGTERM once the wrapper pid had any child. But `signal-sender.sh` forks
  before it execs into the compiled binary (`$(shasum …)`, `$(uname -m)`, the
  self-check), so a poll that landed on one of those children signalled the
  bash front-end, which has no handler. It died of the signal with status 143
  and no SENDER line: the 21-passed/2-failed shape a merge gate recorded at
  load 9 against a version-bump branch. Tests 3, 4 and 13 now wait until the
  pid has exec'd into the binary and forked its child, which it does only after
  `sigaction` has armed the handler. Test 3 also fails under its own name if
  that never happens. Reproduced 5/5 by putting a `shasum` that sleeps 0.5s on
  PATH, which widens the pre-exec window. The old test then printed the gate's
  exact failure; the fixed one passes 23/23.
