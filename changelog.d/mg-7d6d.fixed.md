- **A polecat wedged by the paste-buffered-CR failure draws its `auto_renudge`
  recovery again, on a hard started-signal rather than a heuristic (mg-7d6d).**
  mg-7254 moved the work-item claim to pogod at spawn, which retired the signal
  mg-feb3's auto-renudge gated on — *the item left `available/`* — because pogod's
  own claim satisfies it from the first instant of every `--id` dispatch. mg-7254
  did not hide that: it fell those dispatches back to the ready-composer signal
  and named the residue. The residue was specific and it was the whole point of the
  recovery net: **the fallback catches a harness whose composer never rendered, and
  the mg-ce61 wedge is one where the composer *does* render.** `WaitForReady`
  latches `promptReadySeen`, the kickoff piles in the kernel input buffer as one
  paste block whose CR never re-tokenizes, and the watcher reports "started" for an
  agent that never acted. 75 real polecats have needed that recovery; 73 were
  rescued by it.

  The signal is now **the claim PID**. pogod claims with its own pid, a macguffin
  claim file is named `<id>.md.<pid>`, and the polecat's step 1 re-stamps the claim
  to its own pid — so "started" means *the claim pid is no longer pogod's*. That is
  a positive observation of the agent's own first protocol action rather than the
  absence of one. pm-pogo ruled for it on the failure mode, not the elegance: a
  spurious renudge is cheap, a missed wedge is what mg-feb3 exists to prevent.

  mg-7254's ownership guarantee holds by construction. A re-stamp is a rename
  *within* `claimed/`, so the item is never in `available/` at any point and no
  second dispatch can fit through — which is also why `mg unclaim` + `mg claim` is
  not an acceptable implementation of it, and the test asserts the status directly
  rather than trusting the code.

- **The new signal is capability-gated on a probe, and both halves of it can only
  be switched on together (mg-7d6d).** Re-stamping a held claim needs a macguffin
  command that does not exist yet — `mg claim` requires the item to be `available` —
  so it is filed additively as macguffin **mg-bb43** (`mg reclaim <id> --pid`).
  Until that lands, no polecat can re-stamp.

  So pogod probes for it (`mg reclaim --help` exits 0) and engages only if it is
  there, and the probe drives both the verifier and the polecat prompt step through
  one entry point. **Half of this mechanism is a defect in either direction.** The
  verifier alone gates on a re-stamp no polecat is told to perform: every healthy
  agent reads unstarted and draws three recovery CRs and three `auto_renudge` rows
  per dispatch — an event stream of pure false positives, which is worse than the
  gap it replaces because it also destroys the signal an operator reads. The prompt
  step alone is a command nothing observes. Neither state is reachable.

  Because the gate is an observation rather than a config flag, installing a
  macguffin with `mg reclaim` activates the hard signal on the next pogod restart,
  with nothing to remember and nothing to flip.

- **pogod says at startup which started-signal its polecats are watched on, and
  `auto_renudge` says which one fired (mg-7d6d).** The failure this guards is
  silence: mg-7254's gap went unnoticed until someone thought to look for it, and
  an operator wondering why a wedged polecat drew no recovery should not have to
  read source to find out. One startup log line names the state and the remedy, and
  the event's `reason` detail now distinguishes `claim_pid_not_restamped` (hard)
  from `work_item_unclaimed` (hard) and `no_ready_composer` (the fallback, whose
  presence on a claimed-at-spawn dispatch is itself a fact about the host's mg).

  A store-root disagreement is refused rather than answered for the same reason. An
  absent `claimed/` directory means the verifier is reading a different store than
  the one the claim went into; read as "no claim held" it would report *every*
  polecat on the daemon as started and switch the detector off in silence, so it is
  an error the watcher declines on and logs.

- **`mayor.md` stops carrying the human-in-the-loop stopgap as the answer
  (mg-7d6d).** It told the coordinator that a silently wedged dispatch draws no
  event, so a dispatch with no output and no mail was worth a manual look. That was
  the interim. It now routes on the `reason` detail, names which signals are hard,
  and gives the one-line check for whether this host's hard signal is on.
