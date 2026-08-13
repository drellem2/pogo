package agent

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/hostload"
)

// WorkerBudget is one worker's declared share of this host's cores, handed to
// it at spawn.
//
// # The gap it fills
//
// Both dispatch gates that can refuse a worker count workers, and neither can
// see weight. The per-repo cap counts them by construction. The host gate
// (dispatchload.go) does measure cores, and it works — but it is reactive: it
// refuses the NEXT dispatch after the box is already full.
//
// Measured 2026-08-12 (mg-eb47): three polecats were live and the fleet held
// 9.0 of 10 cores across 32 processes. The consumer was ONE of them — a Lean
// build that had self-parallelised into 11+ compute processes. The per-repo cap
// saw one worker of three and would have admitted two more alongside a build
// that had already taken the whole box; only the host gate stopped that, and
// only after the saturation existed. Fourteen queued items, including a
// human-approved build, were undispatchable for the ~22 minutes it lasted.
//
// So the missing control is not another count and not a lower cap. It is
// telling a worker what share it may have BEFORE its toolchain decides for
// itself. That is this.
//
// # It is advice, and nothing enforces it
//
// Stated plainly because it bounds the claim: this sets environment variables
// and the worker prompt tells the worker to pass them to whatever `-j` /
// `--jobs` / `-p` flag its toolchain has. A worker that ignores them takes the
// box exactly as before, and the host gate is still what catches that. What
// changes is that a self-parallelising toolchain now has a number to be told,
// where previously there was nothing to tell it.
//
// Deliberately toolchain-agnostic. `LAKE_JOBS` would fix the measured incident
// and nothing else; the general shape is "a worker whose toolchain
// self-parallelises", which already includes `go test ./...` (it parallelises
// across packages on its own). Lean was just far enough along the same axis to
// break the model.
//
// # Why it does not consult the current load
//
// The budget is a static division of the box, not a function of a sample. A
// worker lives for tens of minutes; a share sized from the host's state at the
// instant of spawn would be sized from the wrong instant, and would hand out a
// large budget precisely when the box happens to be briefly idle. It is also
// free of a 1s process walk on the spawn path, which matters less but is not
// nothing.
type WorkerBudget struct {
	// Cores is how many of this host's cores one worker may plan to use. Always
	// at least 1 when HostCores is known: a budget of zero is not a smaller
	// share, it is an instruction to do nothing.
	Cores int `json:"cores"`
	// HostCores is the denominator, carried so a reader can tell "3 cores"
	// on a 10-core box from "3 cores" on a 4-core one.
	HostCores int `json:"host_cores"`
	// Basis names how Cores was derived, so a worker or a coordinator reading a
	// surprising number can argue with it rather than guess.
	Basis string `json:"basis"`
}

// Environment variables carrying the budget into a worker's process. Named as
// worker-facing contract: the prompt templates reference them by name, so
// renaming either is a breaking change to every deployed template.
const (
	// WorkerCoresEnv is the budget itself, in whole cores.
	WorkerCoresEnv = "POGO_WORKER_CORES"
	// HostCoresEnv is the denominator. Carried separately so a worker can
	// compute its own share rather than being told only an absolute.
	HostCoresEnv = "POGO_HOST_CORES"
)

// WorkerBudgetFor divides hostCores by the number of workers that may contend
// for them.
//
// The divisor is the PER-REPO cap, not a fleet-wide number, and that is a
// deliberate approximation with a measured justification: on this fleet the
// work concentrates in one repository — 23 of 24 dispatchable items on
// 2026-08-13 — so a per-repo cap on the repo holding essentially all the work
// is a global cap wearing a per-repo label. Where that stops being true (many
// repos, evenly loaded) the budget is conservative rather than wrong: each
// worker is told it may use less than it could, and the host gate is what
// notices the box is actually idle.
//
// The refinery reserve is NOT subtracted. The reserve withholds a dispatch
// slot; it does not reduce how much of the box a worker that IS running may
// use, and subtracting it would shrink every worker's share for a slot nobody
// is occupying.
//
// # What it inherits from the thing it is fixing
//
// Stated because a remedy is an artifact of the same kind as the defect. This
// divides by a WORKER COUNT, so it carries that count's blindness in the
// harmless direction: a lone worker on an idle box is told it may have a third
// of the host while seven cores sit unused. That is the same shape as the cap
// binding at 2 with 8 of 10 cores idle (mg-eb47, 2026-08-13), and it is
// accepted here for two reasons. The budget is advice, so a worker with a
// measured reason may exceed it and say so; and a dispatcher that knows better
// overrides it outright with `--env POGO_WORKER_CORES=N`. What is NOT accepted
// is the harmful direction — one worker silently taking the whole box — and
// that is the direction a static division closes.
func WorkerBudgetFor(hostCores int, capCfg config.DispatchCapConfig) WorkerBudget {
	if hostCores <= 0 {
		hostCores = runtime.NumCPU()
	}
	if hostCores <= 0 {
		// Nothing is known about this host. Report the unknown rather than
		// inventing a share — an absent budget is a fact a template can gate
		// on, and a fabricated "1" is not.
		return WorkerBudget{Basis: "host core count unknown — no budget could be derived"}
	}

	// With no worker count enforced, hostload.FleetHeavyAt is the only control
	// left, so the share is taken from that threshold directly rather than from
	// a number written here — a second copy of 0.50 would go stale silently the
	// first time the gate was recalibrated. The claim is bounded: a worker at
	// its budget holds about half the box, which is at the gate's mark and not
	// provably under it, because the fleet also contains pogod and every other
	// agent. "At most half" is what this buys, not "the gate will not fire".
	cores := int(float64(hostCores) * hostload.FleetHeavyAt)
	basis := fmt.Sprintf("%.0f%% of %d cores: no per-repo worker cap is armed, so the host gate's "+
		"fleet-share mark is the only control and one worker should not take the box alone",
		hostload.FleetHeavyAt*100, hostCores)
	if capCfg.Armed() {
		cores = hostCores / capCfg.MaxPolecatsPerRepo
		basis = fmt.Sprintf("%d cores divided by the per-repo dispatch cap of %d workers",
			hostCores, capCfg.MaxPolecatsPerRepo)
	}

	if cores < 1 {
		cores = 1
		basis += " (floored at 1 core: a budget of zero is an instruction to do nothing, not a smaller share)"
	}
	if cores > hostCores {
		cores = hostCores
	}
	return WorkerBudget{Cores: cores, HostCores: hostCores, Basis: basis}
}

// Known reports whether a budget could be derived at all.
func (b WorkerBudget) Known() bool { return b.Cores > 0 && b.HostCores > 0 }

// Env renders the budget as environment assignments for a worker's process, or
// nil when no budget could be derived — an unset variable says "nobody told me"
// and a zero one does not.
func (b WorkerBudget) Env() []string {
	if !b.Known() {
		return nil
	}
	return []string{
		WorkerCoresEnv + "=" + strconv.Itoa(b.Cores),
		HostCoresEnv + "=" + strconv.Itoa(b.HostCores),
	}
}

// String renders the budget as one operator-facing line, basis included: the
// number is a judgement and a reader who cannot see the reasoning cannot tell a
// deliberate 3 from a defaulted one.
func (b WorkerBudget) String() string {
	if !b.Known() {
		return "worker core budget: NOT DERIVED — " + b.Basis
	}
	return fmt.Sprintf("worker core budget: %d of %d cores (%s)", b.Cores, b.HostCores, b.Basis)
}

// polecatSpawnEnv assembles a worker's environment: the budget first, then
// whatever the dispatcher asked for, then the role.
//
// The ORDER is the contract. exec.Cmd keeps the last assignment of a duplicated
// key, so putting the budget first means an explicit `--env POGO_WORKER_CORES=8`
// wins over the static division — a coordinator that has measured a particular
// item's cost knows more than a division of the core count does, and a control
// with no override is a wedge. POGO_ROLE goes last for the opposite reason: it
// is a frozen cross-tool identifier (mg-6a24 §1.1) and must not be overridable
// by a dispatcher at all.
func polecatSpawnEnv(budget WorkerBudget, reqEnv []string) []string {
	env := make([]string, 0, len(reqEnv)+3)
	env = append(env, budget.Env()...)
	env = append(env, reqEnv...)
	return append(env, "POGO_ROLE="+string(TypePolecat))
}

// WorkerBudget is what this daemon would hand the next worker it spawns.
//
// Exported on the Registry so the same value the spawn path injects is the one
// GET /agents/hostload reports — the precedent is WouldRefuseDispatch, and the
// reason is the same: an advisory number that could drift from the injected one
// lets a coordinator plan against a fleet pogod is configuring differently.
func (r *Registry) WorkerBudget() WorkerBudget {
	return WorkerBudgetFor(runtime.NumCPU(), r.dispatchCapPolicy())
}
