package agent

import (
	"log"

	"github.com/drellem2/pogo/internal/config"
)

// The registry's half of the coordinator-rename guard (mg-cf9e). The guard
// itself lives in internal/config — config cannot import agent — and refuses to
// resolve a coordinator name other than the one a live coordinator process is
// running under. That refusal needs a durable, cross-process answer to "which
// coordinator is running", which only the registry can supply: it is the code
// that spawns the process. So the registry writes the record on spawn and
// removes it on exit. See config.GuardRunningCoordinator for the rationale.

// noteCoordinatorStart records a freshly spawned coordinator so a later
// role-name resolution — in this pogod, in a `pogo` CLI process, or in the next
// daemon boot — refuses to rename it. Non-coordinator agents are ignored.
// Failure to write the record is logged, never fatal: an unwritable POGO_HOME
// must not stop an agent from starting, and the guard degrades to the config pin
// it was built to back up.
func noteCoordinatorStart(a *Agent) {
	if a.Type != TypeCrew || a.Name != CoordinatorName() {
		return
	}
	if err := config.RecordRunningCoordinator(a.Name, a.PID); err != nil {
		log.Printf("agent %s: could not record running coordinator: %v", a.Name, err)
	}
}

// noteCoordinatorExit removes the record written by noteCoordinatorStart, so a
// rename of the now-stopped coordinator is allowed — the documented way to
// rename the role. It clears only this process's own record (name AND pid), so a
// respawn that has already re-armed the guard is not disarmed by its
// predecessor's exit.
func noteCoordinatorExit(a *Agent) {
	if a.Type != TypeCrew || a.Name != CoordinatorName() {
		return
	}
	config.ClearRunningCoordinator(a.Name, a.PID)
}
