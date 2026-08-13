package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// LoadGate answers, at the moment of dispatch, whether this host has room for
// another worker.
//
// # Why this gate exists, and why it does not predict cost
//
// The concurrency rule it sits behind counts workers: "a reasonable limit is
// 3-5 concurrent". mg-1b8c is the measurement that shows counting workers does
// not measure anything a gate run cares about. Two compute-heavy workers in
// one window inflated each other's gate wall-clock badly enough that a fixed
// gate timeout was on track to convert contention into a merge failure — a
// failure that would have read as a defect in the branch under test.
//
// The obvious fix is to know a work item's cost at dispatch time and schedule
// around it. That is not implementable here and the reasons are established
// rather than guessed:
//
//   - Nothing on a work item declares expected cost.
//   - A filer-set marker only works if whoever files reliably sets one, and
//     mg-ddf4 established that the store cannot say who filed anything: the
//     `creator` field is the unix user and reads `daniel` for every item.
//   - Per-repo history is thin and, worse, wrong in the case that matters —
//     the expensive runs are the unusual ones.
//
// So this gate does not predict. It observes: it asks what the fleet is
// holding on this host RIGHT NOW, which is knowable exactly, and declines to
// add more when the answer is "most of it". That is a strictly weaker claim
// than a scheduler makes, and it is the one the available evidence supports.
//
// # What it deliberately does not gate on
//
// Not the load average. Measured on this host the same night: a load average
// of 214 against roughly 7.5 of 10 cores actually in use, because on Darwin
// that number counts uninterruptible-sleep tasks too. A gate keyed on it
// refuses to dispatch while cores sit idle.
//
// Not total host CPU. A share of the load is never ours — a VPN extension at
// ~0.9 cores, the system indexer at ~0.3 — and pausing fleet work does not
// give those cores back. Gating on total load hands an unrelated process a
// veto over the fleet's own dispatch.
//
// Not a count of agents. The live instance was ONE worker that had
// self-parallelised into three compute processes at ~5.7 cores of a 10-core
// box. Anything counting agents sees one and calls the host idle.
type LoadGate interface {
	// DispatchLoad returns the current host sample and whether it could be
	// taken. ok=false means the measurement failed, which must be treated as
	// "no information" and NOT as "the host is busy" — see loadGateRefusal.
	DispatchLoad() (hostload.Sample, bool)
}

// HostLoadGate is the production LoadGate: it samples this host, attributing
// CPU to the fleet by process subtree from pogod's own pid.
type HostLoadGate struct {
	// Reader overrides the default reader. Nil uses one rooted at this
	// process, which is what pogod wants: every agent on the host is a
	// descendant of pogod.
	Reader *hostload.Reader
	// Timeout bounds the sample. A dispatch decision must not hang on an
	// observability call.
	Timeout time.Duration
}

// DispatchLoad implements LoadGate.
func (g HostLoadGate) DispatchLoad() (hostload.Sample, bool) {
	rd := g.Reader
	if rd == nil {
		rd = &hostload.Reader{Roots: []int{os.Getpid()}}
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s, err := rd.Read(ctx)
	if err != nil {
		return hostload.Sample{}, false
	}
	return s, true
}

// SetLoadGate installs the gate consulted before a worker is dispatched.
// Passing nil restores the default HostLoadGate.
func (r *Registry) SetLoadGate(g LoadGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadGate = g
}

func (r *Registry) getLoadGate() LoadGate {
	r.mu.RLock()
	g := r.loadGate
	r.mu.RUnlock()
	if g == nil {
		return HostLoadGate{}
	}
	return g
}

// loadGateRefusal returns the refusal message when this host has no room for
// another worker, or "" when dispatch may proceed.
//
// # It fails OPEN, twice over
//
// A sample that cannot be taken returns "" and dispatch proceeds. So does a
// sample with no fleet attribution. Both are missing information, and refusing
// work on missing information stalls the queue for a reason nobody downstream
// can check or clear — the failure mode the ticket that asked for this gate
// warned about by name. The gate only ever refuses on a positive measurement
// of the fleet's own CPU.
//
// # It is a "later", not a "no"
//
// Unlike the assignee and pairing gates next to it, the same request retried
// unchanged will succeed as soon as the work in flight finishes. The caller is
// told that in the refusal, because the reader is usually an agent deciding
// what to do next with no human in the loop, and "hold and re-check" and
// "abandon this item" are opposite actions.
func (r *Registry) loadGateRefusal() string {
	s, ok := r.getLoadGate().DispatchLoad()
	if !ok {
		return ""
	}
	if !s.FleetHeavy() {
		return ""
	}
	return fmt.Sprintf("host has no room for another worker right now: %s "+
		"This is a LATER, not a refusal of the item — hold it and re-check; nothing about the "+
		"work item is wrong. Read the current numbers with `pogo host load`.",
		s.DispatchAdvice())
}

// HostLoadResponse is the JSON body of GET /agents/hostload.
type HostLoadResponse struct {
	hostload.Sample
	// Advice is the sentence a dispatcher should act on, and Measured says
	// whether a sample was taken at all. A zero Sample with Measured=false is
	// a failed measurement, not an idle host, and the two must not be
	// collapsed by anything reading this.
	Advice     string `json:"advice"`
	Measured   bool   `json:"measured"`
	FleetHeavy bool   `json:"fleet_heavy"`
	// WouldRefuseDispatch is what the spawn path would do with this sample
	// right now. Served from the SAME gate the spawn path consults, so a
	// coordinator reading this and pogod refusing a spawn can never disagree.
	//
	// It answers for the HOST only. A dispatch can still be refused by the
	// per-repo cap with this false — see RepoOccupancy, which is populated when
	// the caller names a repo, and which is the answer to ask for before
	// planning a batch into one repository (mg-3977).
	WouldRefuseDispatch bool `json:"would_refuse_dispatch"`

	// RepoOccupancy is the per-repo cap's view, present only when the request
	// named a repo (GET /agents/hostload?repo=...). Served from the same counter the
	// spawn path consults, for the reason above.
	RepoOccupancy *RepoOccupancy `json:"repo_occupancy,omitempty"`
}

// handleHostLoad serves the host's current fleet-attributable CPU.
//
// It exists so a coordinator deciding whether to fill a slot can read the
// resource rather than count workers. The endpoint lives beside the spawn
// handler and shares its gate deliberately: an advisory number that could
// drift from the enforced one would let a coordinator plan against a host that
// pogod sees differently.
func (r *Registry) handleHostLoad(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := r.getLoadGate().DispatchLoad()
	resp := HostLoadResponse{Sample: s, Measured: ok}
	// The per-repo cap is independent of the sample: it is a count, and it
	// answers even when the host could not be measured. Attached whenever a repo
	// is named so a coordinator planning several dispatches into one repository
	// reads the same number pogod will refuse on (mg-3977).
	if repo := strings.TrimSpace(req.URL.Query().Get("repo")); repo != "" {
		occ := r.RepoOccupancyFor(repo)
		resp.RepoOccupancy = &occ
	}
	if ok {
		resp.Advice = s.DispatchAdvice()
		resp.FleetHeavy = s.FleetHeavy()
		resp.WouldRefuseDispatch = s.FleetHeavy()
	} else {
		resp.Advice = "PROCEED (unmeasured): the host sample could not be taken, so nothing is " +
			"known about the fleet's CPU share. Dispatch is not gated on missing information."
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
