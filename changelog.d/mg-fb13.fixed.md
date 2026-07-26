`pogo agent stop` no longer leaves a mid-flight polecat's work item claimed by a
dead pid. `Registry.Stop` had no claim-release path at all — the claim was
released only by the polecat's own `mg done`/`mg unclaim` — so stopping one
before it reached `mg done` left `~/.macguffin/work/claimed/<id>.md.<pid>` behind
under a pid that no longer existed. Nothing recovers that state: claimed items
are never dispatched, and the stall watcher only scans `available/`, so the item
was invisible to every recovery path. A silent failure whose only symptom is
absence, and the second consequence of the same stop that motivated mg-ee02
(which fixed the worktree destruction and left the claim leak untouched).

Stop now unclaims the item on the paths that actually tear the agent down,
scoped to that agent's own `WorkItemID` — a targeted release, never a sweep of
whatever else is sitting in `claimed/`, so a live sibling's claim and a
deliberately-held one are both left alone. The `restart_on_crash` path
deliberately keeps the claim: that agent is coming back on the same item.
Releasing is skipped when the item is not claimed, which is what makes the
normal route unchanged — pogod records `mg done` and *then* stops the polecat at
merge (gh #35), so on the happy path there is nothing to release and nothing to
report. A failure to release does not fail the stop (the process is already
gone), but it is now loud in the log and in the event stream as
`work_item_claim_release_failed`; a real release emits
`work_item_claim_released`. Tests drive the real `mg` against a sandbox store and
assert the store, not a comment: the claim file gone, the item dispatchable
again, and — as a positive control — all three failing with the release
suppressed. The releaser's default store under a test binary is a throwaway temp
directory, never the live `~/.macguffin` (the mg-da48 shape), because the blast
radius of forgetting a sandbox here is releasing a live agent's claim.
