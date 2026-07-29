- **The coordinator stops shadowing pogod's first-start recovery (mg-eb54).**
  Since mg-feb3 the daemon has recovered a polecat whose kickoff nudge never
  took: it watches for the hard started-signal (the work item leaving
  `available/`) and re-delivers a bare submit terminator, 3 attempts 25s apart —
  a ~75s budget. The coordinator prompt nevertheless still carried the manual
  workaround that mechanism replaced: nudge `"1"` at ~30-60s if the item is
  still unclaimed. That is *inside* the daemon's window, and it is not a
  harmless belt-and-braces. The manual payload is a stray `1` where the daemon
  deliberately sends a bare CR, so it lands as a character in an agent that may
  be mid-recovery; and a polecat that claims after a manual nudge tells you
  nothing about whether the daemon works, so the workaround kept its own
  evidence from ever being collected. The prompt now names the budget, tells the
  coordinator to stay out of it, and defines what to do when it is spent.

- **The manual kick survives as an exception path, because the daemon is not a
  guarantee (mg-eb54).** Retiring the routine nudge is not the same as deleting
  the fallback, and pogod's own event log says why: across 75 real polecats that
  needed this recovery, 72 claimed after the first CR and one after the second,
  but **two spent the whole 3-attempt budget without starting** — one was
  eventually rescued nine minutes later by its own mail-check schedule fire, the
  other never claimed at all. The watcher also declines outright in three named
  cases it announces with an `agent_unwatched` event or a log line: no start
  verifier wired daemon-wide, a spawn carrying neither `--id` nor a provider
  prompt-ready marker, and an unreadable mg state (where it stops rather than
  renudging blind — `internal/agent/startverify.go` names the coordinator's own
  check as the fallback for exactly that case). The prompt now enumerates all
  three, and treats *still unclaimed at ~90s* as a reportable finding rather
  than something to paper over.

- **The ~75s figure is pinned to the constants that produce it (mg-eb54).**
  `TestRenudgeBudget_PromptMatchesConstants` derives the prompt's stated budget
  from `DefaultStartVerifyDelay` × `DefaultStartVerifyMaxAttempts`, so tuning
  either knob fails in the package being edited instead of silently leaving the
  coordinator waiting out a window that already closed — or nudging into one
  that has not. A companion test proves the ratchet fires, on the drifted number
  and on a reintroduction of the retired ~30-60s wording, and that the corrected
  prose still passes.

- **No `MAX-2` concurrent-spawn cap was removed, because none was ever shipped
  (mg-eb54).** The cap this work was filed to retire does not appear in any
  prompt in the tree, in any historical revision of the coordinator prompt, or
  in the deployed copies under `~/.pogo/agents/`. It existed as a runtime
  operating convention held in a PM agent's memory file. Retiring it is a
  fleet-state change, not a repository one, and nothing here does it.
