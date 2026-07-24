The gh-issue teardown detector can see again. launchd execs pogod directly
rather than through a shell, so the daemon inherited an environment with no
`GH_TOKEN` — every `gh issue view` it made exited with "populate the GH_TOKEN
environment variable", and the detector, which correctly refuses to read a
failed lookup as "closed", reported every carrier as **indeterminate** on every
run. Not wrong, just blind, and twice-daily loud about it; invisible from an
interactive shell or a crew agent, because both get the full environment. A new
`internal/ghtoken` repairs this at pogod startup, and `pogo check-teardown`
calls it too so the CLI answers from cron as well as from a terminal: when the
environment holds no token, a user shell is asked for one (`zsh -c` sources
`~/.zshenv` on every invocation, so the secret stays exactly where it already
lives — not copied into a world-readable launchd plist). The value never reaches
a log, an error string, or a test fixture; pogod logs only where the token came
from. This is the read-side sibling of `internal/pathenv` — that one fixes
children launchd's minimal environment leaves unfindable, this one fixes
children that are found, run, and cannot authenticate. Guarded by a live control
that drives the real `gh` under a reproduction of the launchd environment
against a known-closed and a known-open issue, and keeps the failing arm
permanently: a detector whose every unit test injects its lookup passes just as
happily when it is blind, so only the arm that still goes indeterminate proves
the arm that does not.
