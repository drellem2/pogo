package agent

import (
	"fmt"
	"strings"
	"time"
)

// The in-flight spawn REQUEST ledger (drellem2/pogo#265, narrowed to its retry
// half; the EOF half shipped in 80f97edf).
//
// # The window it closes
//
// The live-owner gate (liveownergate.go) refuses a dispatch onto a name or a
// work item a live polecat already holds. It can only see owners that EXIST:
// registered agents, the persisted witness, and pogod's spawn-time claims. A
// spawn request becomes any of those late — the claim is deliberately the LAST
// step before the process starts (see claimForSpawn's call site: one rollback
// point, and a claim must mean a process that starts), after the slow gates and
// the `git worktree add`. So for the whole of a spawn's pre-claim window, which
// on a loaded host runs to minutes, a retried request for the same item or name
// sails through the live-owner gate and races the first one into the same tree.
// A retry is the NATURAL answer to a spawn that looks slow or failed, which is
// why this is not a corner case: it is drellem2/pogo#167's path, reached from the
// most ordinary operator reflex there is.
//
// # Why a ledger of REQUESTS, not an earlier claim or a lock
//
// Moving the claim to the handler's entry would give it two meanings (a process
// is starting / a request is being considered) and two rollback points. A
// per-name mutex would make the retry block SILENTLY behind the first and then
// run its gates against state the first one changed — the "it just hangs"
// failure #169 was filed about. The ledger instead answers the retry at once,
// with a 409 that says what is happening and what to watch for.
//
// # Keys, and the lifetime of an entry
//
// One map, two key spaces: "item:<id>" and "name:<name>". Both are opened in ONE
// check-and-set under r.mu, so two concurrent requests cannot each see the other
// as absent. An entry lives exactly as long as the handler that opened it: it is
// closed by a defer at the point of opening, which runs on every return path and
// on a panic's unwind. That includes the late path after a 202 spawn-still-running
// (WithSpawnResponseDeadline keeps the handler running), so the entry stays open
// until the spawn really ends — which is the point, since that is precisely when
// a caller who saw a 202 is tempted to retry.
//
// A LEAKED entry would refuse its item or name for the daemon's life, which is
// why the close is a defer and never a call on each exit. A pogod restart clears
// the ledger, and that is correct rather than a gap: in-flight handlers die with
// the daemon, so there is nothing left in flight to protect.

// The routes that open ledger entries, as they appear in a refusal.
const (
	spawnPolecatRoute = "/agents/spawn-polecat"
	startRoute        = "/agents/start"
)

// spawnRequestInFlight records one handler that is mid-spawn.
type spawnRequestInFlight struct {
	route      string // "/agents/spawn-polecat" or "/agents/start"
	name       string
	workItemID string
	since      time.Time
}

func spawnRequestNameKey(name string) string { return "name:" + name }
func spawnRequestItemKey(id string) string   { return "item:" + id }

// openSpawnRequest atomically checks the ledger for a request already in flight
// on name or workItemID and, when there is none, records this one under both.
//
// It returns the release func and "" on success; the caller MUST defer the
// release at once. On a hit it returns a nil release and the refusal text, and
// records nothing. Empty keys are not recorded: a request with no --id has no
// item to collide on, and an empty name is refused by ValidateAgentName later.
func (r *Registry) openSpawnRequest(route, name, workItemID string) (release func(), refusal string) {
	name = strings.TrimSpace(name)
	workItemID = strings.TrimSpace(workItemID)

	var keys []string
	if workItemID != "" {
		keys = append(keys, spawnRequestItemKey(workItemID))
	}
	if name != "" {
		keys = append(keys, spawnRequestNameKey(name))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		if held, ok := r.spawnRequestsInFlight[k]; ok {
			return nil, spawnRequestInFlightRefusal(k, held)
		}
	}
	if len(keys) == 0 {
		return func() {}, ""
	}
	if r.spawnRequestsInFlight == nil {
		// Lazily: a bare &Registry{} is a legitimate construction in tests.
		r.spawnRequestsInFlight = make(map[string]spawnRequestInFlight)
	}
	entry := spawnRequestInFlight{route: route, name: name, workItemID: workItemID, since: time.Now()}
	for _, k := range keys {
		r.spawnRequestsInFlight[k] = entry
	}

	var released bool
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if released {
			return
		}
		released = true
		for _, k := range keys {
			// Delete only our own entry. Nothing else can have replaced it while
			// we held it, but checking costs nothing and keeps a double release
			// from ever deleting a successor's entry.
			if cur, ok := r.spawnRequestsInFlight[k]; ok && cur == entry {
				delete(r.spawnRequestsInFlight, k)
			}
		}
	}, ""
}

// spawnRequestsInFlightCount reports how many ledger keys are open. Tests use it
// to prove an exit path did not leak an entry.
func (r *Registry) spawnRequestsInFlightCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.spawnRequestsInFlight)
}

// spawnRequestInFlightRefusal is the 409 body for a request that collides with
// one still in flight. It is NOT OVERRIDABLE, for the live-owner gate's reason
// and with that gate's wording: the request in flight is about to own a worktree
// whose uncommitted files will have no other copy, and a second spawn aimed at
// the same name or item is what destroys them. It cites the
// agent_spawn_requested event the first request emitted, because that event is
// the durable record of what is in flight, and agent_spawned/agent_spawn_failed
// is what closes it.
func spawnRequestInFlightRefusal(key string, held spawnRequestInFlight) string {
	var what string
	if strings.HasPrefix(key, "item:") {
		what = fmt.Sprintf("work item %s", held.workItemID)
	} else {
		what = fmt.Sprintf("agent name %q", held.name)
	}
	heldDesc := held.name
	if held.workItemID != "" {
		heldDesc = fmt.Sprintf("%s (work item %s)", held.name, held.workItemID)
	}
	age := time.Since(held.since).Round(time.Second)

	var b strings.Builder
	fmt.Fprintf(&b, "a spawn for %s is ALREADY IN PROGRESS: pogod accepted a %s request for %s at %s (%s ago), "+
		"and it has not finished — it is not registered yet and holds no claim, so `pogo agent list` will not show it. ",
		what, held.route, heldDesc, held.since.UTC().Format(time.RFC3339), age)
	if held.route == spawnPolecatRoute {
		fmt.Fprintf(&b, "Its agent_spawn_requested event is on the event log; wait for the matching agent_spawned or "+
			"agent_spawn_failed (`pogo events list --agent cat-%s`) instead of dispatching again. ", held.name)
	} else {
		fmt.Fprintf(&b, "Wait for that start to finish (`pogo agent list` will show %s once it is registered) "+
			"instead of dispatching again. ", held.name)
	}
	b.WriteString("There is NO OVERRIDE, deliberately: the spawn in flight is about to own a worktree whose " +
		"uncommitted files have no other copy, and a second spawn aimed at the same name or item is the path that " +
		"destroys them (drellem2/pogo#167). Retrying after it ends is answered by the ordinary gates.")
	return b.String()
}
