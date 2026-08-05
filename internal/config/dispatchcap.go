package config

import (
	"path/filepath"
	"strings"
)

// DispatchCapConfig bounds how many workers may be dispatched into ONE
// repository at a time, and holds part of that budget for the refinery.
//
// # Why the cap is per-repo and not per-fleet
//
// The shipped concurrency rule counts the fleet: "a reasonable limit is 3-5
// concurrent workers". mg-3977 is the measurement that shows the fleet is the
// wrong denominator. Seven workers were dispatched into ONE Go repository on
// 2026-08-05; the 10-core host reached a 1-minute load average of 337,
// commands began timing out, and the refinery stopped starting gates — three
// merges sat queued for 24+ minutes without one beginning.
//
// Five workers across five repositories would have been fine. Five in one Go
// repository is not, and the reason is structural rather than incidental:
// every worker in a repo verifies itself by running THAT REPO'S test suite,
// and `go test ./...` parallelises across packages on its own. So the unit of
// contention is the repository, not the fleet, and a limit expressed over the
// fleet cannot see the thing that saturates.
//
// # Why this is not the host load gate
//
// internal/agent/dispatchload.go already refuses a worker when the fleet is
// holding most of the host's CPU, and it is kept. It answers a different
// question and it answers it too late: it is a MEASUREMENT of load that has
// already arrived. Seven concurrent `go test ./...` runs do not saturate the
// host at the moment the seventh is dispatched — they saturate it a minute
// later, when all seven reach their compile phase together. A count is
// available at dispatch time and a sample of the consequence is not. The two
// gates are complementary and neither subsumes the other.
//
// # Why the refinery gets a reserved share
//
// The incident was self-defeating rather than merely slow. The refinery runs
// the same `./build.sh` the workers run, on the same host, from the same
// repository — so the workers verifying their branches starved the one process
// that could merge them, and the queue they were feeding stopped draining. A
// gate that DOES start under that load can also fail with "signal: terminated",
// which reads exactly like a real verification failure on somebody else's
// branch (observed 2026-07-31, mg-069f).
//
// RefineryReserve is slots the polecat side may not take while the refinery
// has work in that repository. It is enforced ONLY on the dispatch side,
// because that is the only side that can be told "not yet": the refinery
// cannot evict a worker that is already building. So this prevents the
// starvation from forming; it cannot cure one that has already formed. Said
// plainly here because the difference matters to anyone reading a stuck queue.
type DispatchCapConfig struct {
	// MaxPolecatsPerRepo is the most workers that may be live in one
	// repository at once. Zero means UNLIMITED — the gate is disarmed — and it
	// is a distinct value from "unset", which takes DefaultMaxPolecatsPerRepo.
	// See Config's dispatchCapMaxSet for how the two are kept apart.
	MaxPolecatsPerRepo int
	// RefineryReserve is how much of MaxPolecatsPerRepo is withheld from
	// worker dispatch while the refinery has a merge request for that repo —
	// in flight OR queued. Zero disables the reservation.
	//
	// Queued counts, not just in-flight, and that is the whole point. Reserving
	// only while a gate is RUNNING would be nearly useless: the starvation is
	// built by workers dispatched BEFORE the gate starts, and by the time it
	// starts they cannot be taken back.
	RefineryReserve int
}

// Cap defaults. Three workers per repo on a 10-core host leaves room for the
// reserved refinery slot and for the ~1.5 cores of non-fleet load measured on
// this box (a VPN extension and the system indexer), while still letting the
// common two-or-three-ticket batch run in parallel.
//
// These are PLATFORM defaults, not one deployment's policy, and so — unlike
// [dispatch_pairing] — they ship armed. The failure they prevent is a property
// of running a shared test suite concurrently, which every consumer with a repo
// and more than one worker has.
const (
	DefaultMaxPolecatsPerRepo = 3
	DefaultRefineryReserve    = 1
)

// DefaultDispatchCapConfig is the shipped policy.
func DefaultDispatchCapConfig() DispatchCapConfig {
	return DispatchCapConfig{
		MaxPolecatsPerRepo: DefaultMaxPolecatsPerRepo,
		RefineryReserve:    DefaultRefineryReserve,
	}
}

// Armed reports whether the cap refuses anything at all.
func (c DispatchCapConfig) Armed() bool { return c.MaxPolecatsPerRepo > 0 }

// EffectiveCap is how many workers may be live in a repo right now, given
// whether the refinery has work there.
//
// THE RESERVE NEVER TAKES THE LAST SLOT. A reserve greater than or equal to the
// cap would refuse every dispatch into a repo whose refinery queue is non-empty
// — and on this fleet's main repository the queue is almost never empty, so
// that is not a conservative setting, it is a wedge with no way out but a
// config edit. The floor of 1 makes a misconfigured reserve slow rather than
// fatal, which is the same trade dispatchpairing.go makes when a rule names
// repos but no pair tag.
func (c DispatchCapConfig) EffectiveCap(refineryHasWork bool) int {
	if !c.Armed() {
		return 0
	}
	n := c.MaxPolecatsPerRepo
	if refineryHasWork && c.RefineryReserve > 0 {
		n -= c.RefineryReserve
		if n < 1 {
			n = 1
		}
	}
	return n
}

// SameRepo reports whether two repository paths name the same repository.
//
// Cleaning only, deliberately — no symlink resolution and no stat. This
// predicate runs inside a dispatch decision, and a filesystem call there can
// hang on a stale network mount; worse, resolving symlinks would make the
// answer depend on whether the path still exists, so a repo that was renamed
// mid-flight would stop matching its own live workers and silently uncap
// itself. Dispatchers pass the same `--repo` string every time, so cleaning
// covers the case that actually occurs (a trailing slash, a `.` segment).
func SameRepo(a, b string) bool {
	na, nb := NormalizeRepo(a), NormalizeRepo(b)
	return na != "" && na == nb
}

// NormalizeRepo returns the comparison form of a repository path, or "" when
// there is nothing to compare.
func NormalizeRepo(p string) string {
	t := strings.TrimSpace(p)
	if t == "" {
		return ""
	}
	c := filepath.Clean(t)
	if c == "." {
		return ""
	}
	return c
}
