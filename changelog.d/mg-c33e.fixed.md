- **A polecat spawned with no `--id` is now RECOVERED, not merely reported as
  unwatched (mg-c33e).** mg-2437 made pogod's post-spawn start-verifier announce
  the polecats it declined to watch, but it still declined: an empty
  `WorkItemID` meant no claim signal, so the agent got no `auto_renudge`
  recovery at all. mg-560d then established that the gap was load-bearing for
  the drellem2/macguffin#25 hang. A `--no-worktree` spawn's cwd is a brand-new
  `~/.pogo/agents/<name>/`, which is untrusted, so Claude Code raises the
  workspace-trust dialog on every such spawn; the dialog renders no composer, so
  the prompt-ready sentinel never matches and the kickoff nudge is never
  delivered — and a bare CR, exactly what the watcher's renudge sends, dismisses
  it (measured against the live dialog: dialog → composer at t=0.7s, nudge
  accepted). The recovery net could have rescued those polecats and declined to.
  The watcher now falls back to a **ready-composer** started-signal when there is
  no work item: if the provider's prompt-ready sentinel has never appeared, it
  delivers the same bounded bare-CR recovery, emitting `auto_renudge` with
  `reason: "no_ready_composer"` and an empty `work_item_id`. This is a
  structural observation of the screen, not the output-quiescence heuristic the
  watcher deliberately avoids — quiescence misreads a CPU-starved harness as
  ready because it is quiet *because* it is starved, whereas a starved process, a
  loading spinner and the trust dialog all render no composer and so all read
  correctly as unstarted. The sighting is latched, so a bounded output buffer
  scrolling the marker away cannot flip a working agent back to unstarted.
  mg-2437's loud decline survives for the cases that genuinely have nothing to
  observe — no start verifier wired, or a provider that declares no prompt-ready
  marker — now reported as `agent_unwatched` with `reason: "no_ready_signal"`
  (replacing `no_work_item_id`, which is no longer emitted). Crew agents stay
  exempt and untouched.

  *Honesty bound:* the recovery is verified by code-level controls written red
  first against the old behavior. The trust dialog itself was **not** reproduced
  here — this machine carries a blanket `/` trust entry in `~/.claude.json` that
  suppresses it for every path, and the obvious workaround (`--env
  HOME=<scratch>`) detaches the session from its credentials and stalls at "Not
  logged in · Run /login", which wears the costume of a refutation. The
  end-to-end path through pogod's real spawn machinery remains unreproduced.
  mg-560d covers the other half of the hang — the fixed 8s `TrustDialogTimeout`
  in `internal/claude/trust_hook.go` that lets the dialog render after the hook
  has already returned. Both halves are needed to remove the class.
