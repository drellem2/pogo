- **A worker is now told what share of the host it may take, and both dispatch
  refusals stopped promising things a worker count cannot deliver (mg-eb47).**
  Both gates that can refuse a dispatch count WORKERS. The per-repo cap counts
  them by construction; the host gate measures cores but is reactive — it
  refuses the NEXT dispatch after the box is already full. On 2026-08-12 three
  polecats were live and the fleet held **9.0 of 10 cores across 32 processes**.
  The consumer was ONE of them: a Lean build that had self-parallelised into 11+
  compute processes at ~900MB each. The per-repo cap read one-of-three and would
  have admitted two more workers alongside it. Fourteen queued items, including a
  gh-issue build the user had approved twenty minutes earlier, were
  undispatchable for the ~22 minutes it lasted.

  **The new control is a per-worker core budget, handed out at spawn.** `pogod`
  divides the host's cores by the enforced per-repo cap — 3 of 10 on this box —
  and injects it as `$POGO_WORKER_CORES` / `$POGO_HOST_CORES`, with matching
  prose in every worker template telling the worker to pass it to whatever
  `-j` / `--jobs` / `-p` flag its toolchain has. `GET /agents/hostload` and
  `pogo host load` report the same number from the same derivation, on the
  WouldRefuseDispatch precedent: an advisory figure that could drift from the
  injected one lets a coordinator plan against a fleet pogod is configuring
  differently.

  **It is advice and nothing enforces it, which is the honest scope.** A worker
  that ignores the variable takes the box exactly as before and the host gate is
  still what notices. What changed is that a self-parallelising toolchain now has
  a number to be told, where previously there was nothing to tell it. This is
  also why it is delivered at SPAWN rather than by mail: the incident lasted 22
  minutes against a 10-minute mail-check cron, so the coordinator's two requests
  to cap parallelism were still unread when it ended. **A control whose response
  time is longer than the event it responds to is not a control.**

  **Deliberately toolchain-agnostic.** `LAKE_JOBS` would fix the measured
  incident and nothing else. The general shape is "a worker whose toolchain
  self-parallelises", which already includes `go test ./...` — it parallelises
  across packages on its own; Lean was just far enough along the same axis to
  break the model. The per-repo cap is UNCHANGED at 3: it is correct for the
  workload mg-3977 measured it against, and lowering it would punish the common
  case for the rare one.

  **What the budget inherits from the thing it fixes**, stated because a remedy
  is an artifact of the same kind as the defect: it divides by a worker count, so
  a lone worker on an idle box is told it may have a third of the host while
  seven cores sit unused — the same shape as the cap binding at 2 with 8 of 10
  cores idle. That is accepted in the harmless direction and overridable outright
  (`--env POGO_WORKER_CORES=N` wins over the static division; `POGO_ROLE` still
  cannot be moved by a dispatcher). The harmful direction — one worker silently
  taking the whole box — is the one a static division closes.

  **Two refusal messages were measured wrong and are corrected.** On 2026-08-13 a
  coordinator burned two spawn attempts acting on them:

  - The per-repo refusal said a dispatch into a DIFFERENT repo was "unaffected".
    The host gate refuses every spawn regardless of repo, so a per-repo slot
    freed by a merge was unusable and so was every other repo. It now says this
    cap does not refuse another repo — true — and then that **a freed slot is not
    capacity, it is the absence of one particular refusal.**
  - The host gate's HOLD advice said the refusal cleared "when the work in flight
    finishes", which reads as "when an agent exits". The fleet went from 6 agents
    holding 6.1 of 10 cores to **5 agents holding 7.0** — fewer agents, more
    cores, with the process count falling 32 to 21 in the same step, because two
    refinery gates that each parallelise across packages outweighed the agents
    that left. The advice now names the CORE SHARE as the retry condition and
    says which measurement is not a proxy for it.

  `pogo host load --repo=...` additionally withdraws its "dispatch elsewhere"
  note when the host gate is also refusing, rather than offering an action that
  is guaranteed to be refused.
