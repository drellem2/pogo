package agent

// LivePolecatSet returns the names of every polecat a git-GC sweep must treat
// as live and therefore never disturb. A polecat's name equals its branch's
// "polecat-" suffix and its worktree basename, so gitgc.Sweep matches its
// exclusions directly against the keys of this map.
//
// registryPolecats is the caller's view of the in-memory registry — pogod reads
// it from Registry.List(), `pogo gc` from pogod's /agents endpoint. Both are the
// SAME source seen from two sides, and neither is complete alone (mg-0130):
//
//   - the in-memory registry — authoritative while pogod has run continuously,
//     but EMPTY after a restart, permanently, because the registry has no
//     adopt/reattach path (mg-13a3);
//   - the persisted polecat witness — which survives a restart and answers on
//     (pid, start_time), so a polecat that outlived the pogod that spawned it
//     stays protected.
//
// Without the witness union a restart empties the live set, and a polecat whose
// ticket is already done but whose process is still alive — every polecat's
// NORMAL end state (`mg done`, then await the mayor's stop) — loses its sole
// worktree guard. Worktree removal is gated on the live set alone, with no merge
// gate to catch the mistake, unlike branch deletion; so the worktree is removed
// out from under a running polecat.
//
// A witnessed polecat counts as live when its process is provably ours
// (WitnessAlive) OR when its identity cannot be established (WitnessUnreadable):
// the asymmetry favours keeping a running polecat's work over reclaiming a dead
// one's disk, matching the mail-check reaper, which likewise never reaps on
// Unreadable (cmd/pogod registryLiveness). WitnessDead and WitnessNoRecord add
// nothing — a provably-dead polecat is exactly what the sweep exists to clean up.
//
// A witness READ error is returned, not swallowed: the caller must decline to
// sweep rather than sweep against a live set it knows is missing survivors.
//
// WHY THIS IS IN THE AGENT PACKAGE AND NOT AT EITHER CALL SITE (mg-1403). The
// union shipped in cmd/pogod only, so `pogo gc` — the manual entry point to the
// same gitgc.Sweep — kept building its live set from the registry alone and
// carried mg-0130's exact defect one caller over, silently: it warns when pogod
// is unreachable, but a restarted pogod ANSWERS, with an amnesiac registry, and
// that is the case that matters. Two callers of one gate disagreeing about who
// is alive is a fact about the gate's shape, so the answer is defined once, here,
// next to the witness store it reads. gitgc cannot host it — internal/agent
// already imports internal/gitgc, so the edge only runs this way.
func LivePolecatSet(registryPolecats []string) (map[string]bool, error) {
	live := make(map[string]bool, len(registryPolecats))
	for _, name := range registryPolecats {
		live[name] = true
	}
	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		return nil, err
	}
	for name, v := range verdicts {
		if v == WitnessAlive || v == WitnessUnreadable {
			live[name] = true
		}
	}
	return live, nil
}
