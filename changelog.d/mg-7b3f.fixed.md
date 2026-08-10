- **An unreadable prompt tree stops arriving as a clean "not configured", and the
  reason set finally matches what two shipped disclosures were already promising
  (mg-7b3f).** `agent.IsConfiguredAgent` returned **false** both when the prompt
  tree could not be READ and when the agent simply was not one of ours. The two
  have opposite operational meanings — "not configured" is a fact about intent and
  needs no action, "I could not read the prompt tree" is a fault in the instrument
  and means the answer is UNKNOWN — and `mailLoopExclusionFor` consumed that single
  false, so `pogo check-mailloops` could only ever report the coarse
  `not_configured`. Meanwhile the command's own help text and **both** existing
  disclosures (`internal/deafwatch`, `internal/agent`) named "unreadable prompt
  tree" as a category. A reader was told a distinction the code could not compute:
  drellem2/pogo#127's defect, one level in.

  `agent.ConfiguredStateFor` now answers in `DesiredStateFor`'s three-answer shape
  — `(true, nil)` ours, `(false, nil)` definitively not, `(false, err)` UNKNOWN —
  and `unreadable_prompts` joins `polecat`, `not_running` and `not_configured` as a
  fourth exclusion reason. It renders as `UNREADABLE prompt tree — could not be
  classified at all`, and `not_configured` drops its hedge to `not configured — no
  prompt on this machine`, because the hedge existed only because the code could
  not tell them apart.

  **THE ERROR PATH THE TICKET NAMED WAS UNREACHABLE, AND THAT IS THE LARGER FIND.**
  `IsConfiguredAgent` logged and collapsed an error at `autostart.go:203` that
  could not occur: `ListPrompts` swallowed a failed `os.ReadDir` (`if entries, err
  := ...; err == nil`) and returned a **shorter list with a nil error**. Threading
  the error out of the predicate alone would have shipped a taxonomy value that
  never fires — the same unbacked disclosure, pointing the other way. `ListPrompts`
  now distinguishes ABSENT from UNREADABLE: a directory that is not there is a
  configuration fact and still yields an empty list with no error, while one that
  exists and cannot be read is returned as `prompt tree unreadable: ...`.

  **THE COLLAPSE WAS NOT ONLY HIDING MAIL LOOPS, IT WAS DELETING THEM.**
  `DesiredStateFor` reads the same list, and its doc comment reserves `(false,
  nil)` for EVIDENCE — "no prompt at all". With the read error swallowed it
  returned exactly that for the **entire crew**, and the mail-check reap
  (`cmd/pogod/main.go:250`) acts on it: `AgentGone`, schedules removed. So an
  unreadable `~/.pogo/agents/crew` manufactured the very fault `deafwatch` exists
  to announce. Measured before the fix, not inferred: with the directory at mode
  0000, `ListPrompts` returned `0 prompts, err=<nil>` and `DesiredStateFor(pm-pogo)`
  returned `false, <nil>`. The reap's `AgentUnknown` branch — which already says
  "NOT reaping" — is now reachable. `pogo doctor` likewise stops telling an
  operator with an unreadable tree to run the installer.

  **THE FIX IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so the ways it could
  repeat it are closed.** (1) The new reason is emitted only from a real error, and
  a test asserts an agent with no prompt on a READABLE tree still reports
  `not_configured` — a fourth value that fired for both cases would be the same
  collapse under a longer name. (2) The staging helper in the new tests asserts it
  actually produced a read failure and skips otherwise, so the suite cannot go
  green while measuring nothing. (3) `IsConfiguredAgent` still collapses, on
  purpose, for callers where a wrong "no" is harmless; its doc now sends anything
  that REPORTS a reason to `ConfiguredStateFor`. (4) The skew this cannot fix is
  **disclosed rather than papered over**: a pogod older than the client still sends
  `not_configured` for an unreadable tree, the report carries no version, so the
  help text says the client cannot detect that and does not pretend to.

  `Actionable()` is deliberately unchanged — an unreadable tree is a fault an
  operator should act on, but the unjudged set has never moved the exit status
  (mg-0db1), and changing that is a policy decision this fix does not make.
