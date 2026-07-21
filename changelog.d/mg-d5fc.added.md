- **Crew agent workspaces fast-forward at agent start, or say loudly why not (mg-d5fc).** A
  long-lived checkout at `~/.pogo/agents/<name>/repo` had no keeper: the refinery
  fast-forwards the checkout an MR was *submitted* from (gh #30) and polecat
  worktrees are branched from current `origin/main`, but a crew agent's own
  read-copy sits outside both paths. One was found 129 commits / ~2 months behind
  `main` — by accident, during unrelated work. `StartCrewAgent` now runs
  `internal/freshen` over that checkout.

  **It runs before the harness process is spawned, and that is the design, not an
  implementation detail.** That instant is the only one at which the checkout
  provably has no reader, so the refresh cannot move the ground under an agent
  mid-edit. There is deliberately no path that freshens a *running* agent's
  workspace — the refresh happens on the agent's own clock, its start, so consent
  is structural rather than negotiated.

  Deliberately **not** a staleness monitor. A standing check that watches how far
  behind a checkout has drifted is a guard that decays: it watches a number as a
  proxy for a question you can ask directly at the moment it matters, needs a
  threshold nobody can justify, and needs someone to keep watching it forever.

  **Never clobbers.** Any staged or unstaged change to a tracked file declines the
  refresh — one of the two checkouts that prompted this held 83 staged adds on an
  abandoned branch, and an automatic refresh that destroys that turns a silent
  staleness bug into silent data loss, which is strictly worse. Detached HEAD, no
  upstream, and diverged all decline too. Untracked files do not block, because
  git's own `merge --ff-only` aborts rather than overwriting one.

  **A verdict it could not reach is not a clean verdict.** A failed fetch reports
  `failed` / freshness UNKNOWN, never `already_current`, and `behind: -1`
  (undetermined) is distinct from `behind: 0` (positively current). Staleness is
  decided by comparing commit OIDs after a fetch — never by ref or path existence,
  which cannot tell a superseded revision from a current one, and never against the
  local remote-tracking ref, which on a stale checkout is exactly as old as its last
  fetch.

  Every non-skipped verdict emits `agent_workspace_freshened`; a checkout known to
  be behind that was not refreshed also mails the coordinator, with a remedy that
  routes the dirty case through `git status` and a decision rather than handing out
  a paste-ready `git reset --hard`.

  **Residual gap, stated plainly:** a crew agent that runs for months without a
  restart, park/wake, or autostart never reaches this code. This closes the common
  case and makes the rest loud; closing it fully means making the read path resolve
  against `origin` rather than a working copy, which is a much larger change and is
  not done here.
