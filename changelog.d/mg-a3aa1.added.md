- **spawn-polecat records `agent_spawn_requested` and logs each phase
  (mg-a3aa1).** A spawn that died or wedged mid-handler used to leave no trace.
  pogod now logs "spawn requested" and emits `agent_spawn_requested` right
  after decoding the request, then logs each phase boundary (gates passed,
  base ref resolved, worktree created, claim done) with the elapsed time.
  Refs drellem2/pogo#169
