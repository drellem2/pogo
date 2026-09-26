A respawn suppressed by the synthetic-failure-turn gate now always emits its
`restart_suppressed` event. The event was emitted only from synthwatch's own scan
cache, which is empty when `[synth_watch] enabled = false` (the watcher is never
scanned) and for any agent that exits before its first scan — so the respawn was
withheld and logged but left no event. It is now emitted from the gate's verdict
and carries `pager_armed` so a reader can tell whether a human was paged
(follow-up to drellem2/pogo#185). The main.go gating of synthwatch, refusal-watch
and turn-watch is now tested, and the docs section for their switches no longer
re-parents the `check-strandedmail`/`check-verdicts`/`check-refusals` subsections.
