`TestInitialPromptViaArgvAppendsToCommand` no longer measures the host. Its
final assertion polled the agent's output buffer against a 5-second wall-clock
deadline; it now waits on `Agent.Done()` — which closes only after the PTY
reader has drained (`waitAndHandle`) — and decides against the complete output
of an exited process.

A wall-clock bound on a machine that also runs other work is load-sensitive
whether or not this particular test has ever been seen to fail. The spawned
command is `echo`, scheduled in milliseconds on an idle box and delayable
arbitrarily on a busy one, so the same code produced different verdicts on
different hosts and a contended machine was reported as a broken argv delivery
path. Waiting on process exit removes the time term from the verdict entirely:
once the process is gone the output can never grow, so "the prompt is not
there" is a fact rather than a deadline expiring. A slower host now takes
longer to reach the decision and reaches the same one.

Measured, not asserted. A throwaway pair of tests spawned the same argv
delivery behind `sleep 6` — what host contention looks like from inside the
test — and ran the old assertion and the new one against it. The old gate
failed with "argv-delivered prompt never appeared in output"; the new gate
passed. Same code, same delivery, opposite verdicts, and only the instrument
differed.

The remaining `2 * time.Minute` is a deadlock backstop, not a threshold. It
sits four orders of magnitude above `echo`'s runtime, so it can only fire on a
genuine hang or a wedged PTY reader, and it fails with a message that says so
rather than accusing the argv path. Raising a constant was the explicitly
rejected fix here: it moves the load at which the host gets misreported as a
code failure without stopping the test from measuring the host, and it is what
the tracked load-sensitive family (mg-6c90, mg-db12, mg-3412) has accumulated
from.

Scope. This is the instrument half of mg-34cb, split out because it does not
depend on reproducing the one observed failure — that half stays parked on a
quiet host. Sibling tests in the same file still poll against wall-clock
deadlines (`TestInitialNudgeAutoDelivers` waits 8s on an asynchronous nudge to
a `cat` that never exits, so it has no exit signal to wait on); they were left
alone.
