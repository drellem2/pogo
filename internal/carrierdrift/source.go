package carrierdrift

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/ghteardown"
	"github.com/drellem2/pogo/internal/mgscan"
	"github.com/drellem2/pogo/internal/testtmp"
)

// carrierLinePrefixes are the body lines this package reads. They live in the
// work-item BODY, not the frontmatter — see the filing recipe in
// internal/agent/prompts/mayor.md. internal/workitem parses frontmatter only and
// therefore cannot see them, which is why this package parses bodies itself.
const (
	keyWorkflow = "workflow"
	keyStage    = "stage"
	keyGH       = "gh"

	workflowGHIssue = "gh-issue"
)

// liveStatuses are the mg statuses this detector covers: a carrier at one of
// these is a claim that the fleet is CURRENTLY carrying the issue.
//
// `done` is ghteardown's population and `archived` is settled history. `shelved`
// is the interesting exclusion and it is a stated boundary rather than an
// oversight: shelving is a deliberate human act that parks an item, which makes
// it the closest thing the store has to a declaration. A shelved carrier cannot
// cause a mis-dispatch, which is the sharpest of the three harms. It CAN still
// leave a reporter waiting, so the boundary is opened by IncludeShelved rather
// than being a wall.
var liveStatuses = []string{"available", "claimed", "pending"}

// ParseBody extracts the carrier lines from a work-item body. It returns ok
// false when the body is not a gh-issue carrier at all.
//
// The parse is STRUCTURAL, and each restriction was earned by a real body in
// this store:
//
//   - Blockquoted lines are ignored. The coordinator annotates live carriers with
//     `> **CARRIER NOTE …**` blocks that quote refs and stages in prose, and
//     matching those would let commentary masquerade as state.
//   - Fenced code blocks are ignored. Ticket bodies routinely paste the filing
//     recipe itself, complete with a literal `gh: <owner>/<repo>#<n>` line — this
//     package's own ticket body does — and every such paste would otherwise
//     register as a carrier.
//   - The FIRST occurrence of each key wins, and keys are matched anywhere in the
//     body rather than only in a leading block, because the coordinator PREPENDS
//     notes to live carriers. Anchoring to position would silently stop
//     recognising exactly the carriers under active management — the ones that
//     matter most here.
func ParseBody(body string) (stage, ghRef, declClosed, declAck, declParked string, ok bool) {
	seen := map[string]string{}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch key {
		case keyWorkflow, keyStage, keyGH, KeyDeclaredClosed, KeyDeclaredAck, KeyDeclaredParked:
			if _, dup := seen[key]; !dup {
				seen[key] = val
			}
		}
	}
	if !strings.EqualFold(seen[keyWorkflow], workflowGHIssue) {
		return "", "", "", "", "", false
	}
	return seen[keyStage], seen[keyGH], seen[KeyDeclaredClosed], seen[KeyDeclaredAck],
		seen[KeyDeclaredParked], true
}

// ParseRef splits a `gh:` ref of the form owner/repo#number, tolerating a pasted
// GitHub URL and the decoration a human wraps a ref in.
func ParseRef(ref string) (repo string, number int, err error) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "https://github.com/")
	ref = strings.TrimPrefix(ref, "http://github.com/")
	if i := strings.Index(ref, "/issues/"); i >= 0 {
		ref = ref[:i] + "#" + strings.TrimSuffix(ref[i+len("/issues/"):], "/")
	}
	ref = strings.Trim(ref, "`<>()[] \t.,;")
	// One line may name several refs. The FIRST is the carrier's subject; the
	// rest are cited, and re-reading a cited ref would report drift against an
	// issue this carrier never claimed.
	if i := strings.IndexAny(ref, ", \t"); i >= 0 {
		ref = ref[:i]
	}

	repo, numStr, found := strings.Cut(ref, "#")
	if !found {
		return "", 0, fmt.Errorf("gh ref %q has no '#<number>'", ref)
	}
	repo = strings.TrimSpace(repo)
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, fmt.Errorf("gh ref %q: want owner/repo#number", ref)
	}
	number, err = strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("gh ref %q: bad issue number", ref)
	}
	return repo, number, nil
}

// mgShowItem is the subset of `mg show --json` this package reads. `mg list`
// does not emit bodies, which is why finding the carriers costs one `mg show`
// per live item.
type mgShowItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Created string `json:"created"`
	Body    string `json:"body"`
}

// MGSource reads LIVE gh-issue carriers out of a macguffin store by shelling out
// to the `mg` CLI.
type MGSource struct {
	// Root overrides the store location, passed to mg as --root. Empty means
	// "resolve a default" — see resolveRoot, which does NOT resolve to the live
	// store under a test binary.
	Root string
	// Bin is the mg binary. Empty means "mg" on PATH.
	Bin string
	// IncludeShelved widens the scan to shelved carriers — see liveStatuses.
	IncludeShelved bool
	// Workers bounds concurrent `mg show` calls. Zero picks a default from the
	// CPU count.
	Workers int
	// Cache, when set, remembers each item's parse keyed on the mtime `mg list`
	// reports, so a later pass forks `mg show` only for items that are new or
	// whose file changed — ghintake's fix for drellem2/pogo#179, applied to the
	// sibling scan (mg-e353). It is a POINTER on purpose: pogod binds
	// src.Carriers off a struct value, and an embedded cache would be copied and
	// never hit. Nil caches nothing — right for the one-shot `pogo
	// check-carriers`, which has no second pass to serve.
	Cache *ItemCache
}

// ItemCache is the per-item cache MGSource.Cache holds: what carrier() decided
// about one item, with Status blank, because status is taken from the current
// list row (see mgscan.Cache on why a rename keeps the mtime). Non-carriers are
// cached too — they are nearly every item the scan lists.
//
// It is NOT shared with ghintake's RefCache, although both scans list the same
// store through the same mgscan code. Three reasons, the first decisive:
//
//   - Retain would fight. Each scan prunes the cache to the ids IT just listed,
//     and the two list different populations (ghintake every status, this
//     package the live ones), so one watcher's prune would evict the other's
//     archived entries every pass — 88% of the store re-forked on alternate
//     passes, which is #179 back again.
//   - The values differ. ghintake keeps every `gh:` ref ParseBodyRefs finds; this
//     package keeps ParseBody's gated carrier projection plus title and created.
//     A shared value would have to be the raw body, which moves ~4,000 bodies
//     into pogod's heap to save forks the per-scan caches already save.
//   - The saving is one fork per EDITED live item. With a cache on each side an
//     unchanged item already costs no fork in either scan; sharing would only
//     spare the second scan's read of an item the first had just re-read.
type ItemCache = mgscan.Cache[cachedItem]

// cachedItem is carrier()'s answer for one successfully read item.
type cachedItem struct {
	c  Carrier
	ok bool
}

// NewItemCache returns an empty ItemCache for a long-lived MGSource.
func NewItemCache() *ItemCache { return mgscan.NewCache[cachedItem]() }

// testRootOnce memoises one temp directory per test binary, so every test-binary
// call resolves to the same empty scratch store.
var (
	testRootOnce sync.Once
	testRootDir  string
)

// resolveRoot returns the store root to hand mg.
//
// Under a test binary with no explicit Root it returns a per-binary temp
// directory — NEVER the live ~/.macguffin. A test-safe DEFAULT rather than an
// opt-in helper, which is the lesson of mg-da48: an opt-in guard is remembered
// by exactly the tests that least need it.
func (s MGSource) resolveRoot() string {
	if s.Root != "" {
		return s.Root
	}
	if testing.Testing() {
		testRootOnce.Do(func() {
			dir, err := testtmp.Dir("carrierdrift")
			if err != nil {
				// Deliberately no error return: every fallback must lead
				// somewhere that is not the live store. A temp path we failed to
				// create yields "store unreadable", a loud correct failure;
				// falling back to $HOME/.macguffin would be a silent wrong one.
				dir = filepath.Join(os.TempDir(), "carrierdrift-test-store-fallback")
			}
			testRootDir = dir
		})
		return testRootDir
	}
	return "" // production: let mg apply its own default ($MG_ROOT, then ~/.macguffin)
}

func (s MGSource) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "mg"
}

func (s MGSource) workers() int {
	if s.Workers > 0 {
		return s.Workers
	}
	return mgscan.DefaultWorkers()
}

func (s MGSource) run(args ...string) ([]byte, error) {
	if root := s.resolveRoot(); root != "" {
		args = append([]string{"--root", root}, args...)
	}
	cmd := exec.Command(s.bin(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", s.bin(), strings.Join(args, " "), msg)
	}
	return out, nil
}

// Statuses reports which mg statuses this source scans, so a report can state
// its own coverage rather than implying it saw everything.
func (s MGSource) Statuses() []string {
	out := append([]string(nil), liveStatuses...)
	if s.IncludeShelved {
		out = append(out, "shelved")
	}
	return out
}

// Carriers returns every LIVE gh-issue carrier and how many work items were
// examined to find them.
//
// A store that cannot be LISTED is an ERROR, never an empty slice. Zero carriers
// and an unlistable store both render as "nothing to report", and conflating
// them is how a detector goes quietly blind — this package's own subject matter,
// reproduced inside itself.
//
// A single item that cannot be SHOWN is different: it is skipped and counted as
// unexamined, because aborting the whole scan on one bad item would make the
// detector permanently blind on the only store it has. The direction of that
// error is safe here — an item whose body we cannot read is an item we cannot
// tell is a carrier, so the worst it can cause is a MISSED finding on that one
// item, and StoreItems reports the shortfall.
//
// With a Cache set, an item listed at the same mtime as last pass is answered
// from the cache without forking `mg show`. Status on a cached answer comes from
// this pass's list row. Ids listed more than once always bypass the cache.
func (s MGSource) Carriers() ([]Carrier, int, error) {
	rows, err := s.rows()
	if err != nil {
		return nil, 0, err
	}
	if s.Cache != nil {
		listed := make(map[string]bool, len(rows))
		for _, r := range rows {
			listed[r.ID] = true
		}
		s.Cache.Retain(listed)
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}

	results := make([]cachedItem, len(rows))
	// A fixed pool, not a goroutine per item: the latter is the shape
	// drellem2/pogo#179 profiled in ghintake's identical scan (mg-e353).
	mgscan.Pool(len(rows), s.workers(), func(i int) {
		c, ok := s.cachedCarrier(rows[i])
		results[i] = cachedItem{c: c, ok: ok}
	})

	var out []Carrier
	scanned := 0
	for _, r := range results {
		if r.c.ID == "" && !r.ok {
			continue // unreadable item: not examined
		}
		scanned++
		if r.ok {
			out = append(out, r.c)
		}
	}
	return out, scanned, nil
}

// cachedCarrier answers one listed item from the cache when its mtime is
// unchanged, and otherwise reads it with carrier and caches the answer.
//
// Only successful reads are cached (an empty ID is carrier's read-failure
// signal): a transient failure cached at an unchanged mtime would otherwise
// leave that item unexamined for as long as nobody edits it.
func (s MGSource) cachedCarrier(r listRow) (Carrier, bool) {
	if r.twin {
		s.Cache.Forget(r.ID)
		return s.carrier(r.ID)
	}
	if hit, ok := s.Cache.Lookup(r.ID, r.Mtime); ok {
		c := hit.c
		if hit.ok {
			c.Status = r.Status
		}
		return c, hit.ok
	}
	c, ok := s.carrier(r.ID)
	if c.ID != "" && s.Cache != nil {
		stored := c
		stored.Status = ""
		s.Cache.Store(r.ID, r.Mtime, cachedItem{c: stored, ok: ok})
	}
	return c, ok
}

// carrier loads one item and decides whether it is a live gh-issue carrier. A
// read failure returns a zero Carrier with ok false and an empty ID, which is
// what Carriers counts as unexamined.
func (s MGSource) carrier(id string) (Carrier, bool) {
	raw, err := s.run("show", id, "--json")
	if err != nil {
		return Carrier{}, false
	}
	var item mgShowItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return Carrier{}, false
	}
	stage, ghRef, declClosed, declAck, declParked, ok := ParseBody(item.Body)
	if !ok || ghRef == "" {
		return Carrier{ID: itemID(item, id)}, false
	}
	c := Carrier{
		ID: itemID(item, id), Title: item.Title, Status: item.Status, Stage: stage,
		Created: parseTime(item.Created), DeclaredClosed: declClosed,
		DeclaredAck: declAck, DeclaredParked: declParked,
	}
	repo, number, err := ParseRef(ghRef)
	if err != nil {
		// A carrier whose ref does not parse cannot be re-read, and dropping it
		// silently would hide it from this audit forever. Surface it with the raw
		// ref so it lands in the indeterminate bucket instead of vanishing.
		c.Repo, c.Number = ghRef, 0
		return c, true
	}
	c.Repo, c.Number = repo, number
	return c, true
}

// itemID prefers the id the store reported and falls back to the id we asked
// for, so a finding always names something a reader can type back at mg.
func itemID(item mgShowItem, asked string) string {
	if item.ID != "" {
		return item.ID
	}
	return asked
}

// parseTime reads an mg timestamp, returning the zero time when it is absent or
// unparseable. A zero Created makes every age check on that carrier abstain
// rather than fire — an unknown age must not become an old one.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// listRow is one distinct id from the listing. twin is set when the id was
// listed more than once in this pass — most often an item that moved between two
// live statuses between two `mg list` calls — and such a row is never answered
// from the cache: one slot keyed on one mtime cannot describe two files.
type listRow struct {
	mgscan.Row
	twin bool
}

// rows lists every work item across the live statuses, one row per distinct id,
// in first-seen order.
func (s MGSource) rows() ([]listRow, error) {
	at := map[string]int{}
	var out []listRow
	for _, status := range s.Statuses() {
		listed, err := s.run("list", "--status="+status, "--json")
		if err != nil {
			return nil, fmt.Errorf("listing %s work items: %w", status, err)
		}
		parsed, err := mgscan.ParseList(listed)
		if err != nil {
			return nil, err
		}
		for _, r := range parsed {
			if r.ID == "" {
				continue
			}
			if i, ok := at[r.ID]; ok {
				out[i].twin = true
				continue
			}
			at[r.ID] = len(out)
			out = append(out, listRow{Row: r})
		}
	}
	return out, nil
}

// ghComment is the subset of a comment `gh issue view --json comments` returns.
type ghComment struct {
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// ghIssueView is the subset of `gh issue view --json` this package reads.
type ghIssueView struct {
	Number    int         `json:"number"`
	State     string      `json:"state"`
	CreatedAt time.Time   `json:"createdAt"`
	ClosedAt  time.Time   `json:"closedAt"`
	Comments  []ghComment `json:"comments"`
}

// GHSnapshot RE-READS one issue through the `gh` CLI. This is the operation the
// whole package exists to perform.
//
// Every path that is not a positive, parsed, recognised state returns
// StateUnknown WITH an error, and the discipline behind that is ghteardown's:
// the tempting bug is to test for one state and treat everything else as the
// other, under which every failure row — expired auth, rate limit, offline, repo
// renamed, issue deleted or transferred — reads as a definite answer. A
// silent-failure detector that fails silently is worse than no detector, because
// it also manufactures confidence.
//
// The failure class is attached HERE, once, at the only place gh's raw text
// exists, rather than left for every reader to re-derive from prose.
func GHSnapshot(repo string, number int) (Snapshot, error) {
	if repo == "" || number <= 0 {
		// The instrument is fine; this carrier's own `gh:` line is not.
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureSubject,
			Msg:   fmt.Sprintf("unresolvable gh ref %q#%d — carrier cannot be re-read", repo, number),
		}
	}

	cmd := exec.Command("gh", "issue", "view", strconv.Itoa(number),
		"--repo", repo, "--json", "number,state,createdAt,closedAt,comments")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ClassifyLookupError(fmt.Errorf("%s", msg)),
			Msg:   fmt.Sprintf("gh issue view %s#%d failed: %s", repo, number, msg),
		}
	}

	var view ghIssueView
	if err := json.Unmarshal(out, &view); err != nil {
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureSubject,
			Msg:   fmt.Sprintf("gh issue view %s#%d: unparseable output: %v", repo, number, err),
		}
	}

	snap := Snapshot{
		Created:  view.CreatedAt,
		ClosedAt: view.ClosedAt,
		Comments: len(view.Comments),
	}
	for _, c := range view.Comments {
		if !IsAcknowledgement(c.Body) {
			continue
		}
		// FIRST acknowledgement wins: the question is when the reporter stopped
		// waiting, not when we last spoke.
		if !snap.Acknowledged || c.CreatedAt.Before(snap.AcknowledgedAt) {
			snap.Acknowledged = true
			snap.AcknowledgedAt = c.CreatedAt
		}
	}

	switch strings.ToUpper(strings.TrimSpace(view.State)) {
	case "OPEN":
		snap.State = StateOpen
	case "CLOSED":
		snap.State = StateClosed
	case "":
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureSubject,
			Msg:   fmt.Sprintf("gh issue view %s#%d: no state in response", repo, number),
		}
	default:
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureSubject,
			Msg:   fmt.Sprintf("gh issue view %s#%d: unrecognised state %q", repo, number, view.State),
		}
	}
	return snap, nil
}

// Retry policy for the production re-read. The values are ghteardown's, reused
// for the reason its taxonomy is: a blip that is ridden out in one detector and
// reported as a finding in its sibling would make the two disagree about the
// same GitHub.
const (
	// DefaultSnapshotAttempts is how many times one issue is re-read before a
	// network-class failure is given up on.
	DefaultSnapshotAttempts = ghteardown.DefaultLookupAttempts
	// DefaultSnapshotBackoff is the first sleep between attempts; it doubles.
	DefaultSnapshotBackoff = ghteardown.DefaultLookupBackoff
)

// RetryingSnapshot wraps a re-read with the production retry policy, so the CLI
// and the watcher ride out a blip identically.
func RetryingSnapshot(snap SnapshotFunc) SnapshotFunc {
	return Retrying(snap, DefaultSnapshotAttempts, DefaultSnapshotBackoff, time.Sleep)
}

// Retrying re-attempts a re-read whose failure is network-class, with doubling
// backoff, and returns the first conclusive answer.
//
// ONLY network-class failures are retried, and that restriction is the design
// rather than an optimisation: an expired credential, a missing issue and a rate
// limit are all perfectly repeatable, so re-running them produces the identical
// error N times while spending N times the window — and dresses a deterministic
// failure in the language of a flake.
//
// When retries are exhausted the LAST error is returned, wrapped so the report
// records that the failure SURVIVED the retries. An error that says "still
// failing after 3 attempts" cannot be mistaken for one unlucky sample.
//
// sleep is a seam so tests exercise the backoff without spending it.
func Retrying(snap SnapshotFunc, attempts int, backoff time.Duration, sleep func(time.Duration)) SnapshotFunc {
	if attempts < 1 {
		attempts = 1
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return func(repo string, number int) (Snapshot, error) {
		var (
			s     Snapshot
			err   error
			wait  = backoff
			slept time.Duration
		)
		for attempt := 1; ; attempt++ {
			s, err = snap(repo, number)
			if err == nil {
				return s, nil
			}
			if !ClassifyLookupError(err).Retryable() {
				return s, err
			}
			if attempt >= attempts {
				break
			}
			sleep(wait)
			slept += wait
			if wait > 0 {
				wait *= 2
			}
		}
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureNetwork,
			Msg: fmt.Sprintf("%v (network-class failure, still failing after %d attempts spanning %s of backoff)",
				err, attempts, slept),
		}
	}
}

// Prefetch resolves every carrier's snapshot CONCURRENTLY and returns a
// SnapshotFunc that serves the results from memory.
//
// It exists so Detect can stay pure and sequential — every branch reachable from
// a table in a test — while the network cost is paid in parallel. On this store
// that is 41 live carriers: serially about forty seconds on pogod's heartbeat
// goroutine, fanned out about five.
//
// A ref appearing on two carriers is fetched ONCE. That is not only a saving: it
// also guarantees the two carriers are judged against the same reading, so a
// chained triage/build pair can never be reported as disagreeing about an issue
// that changed between two lookups.
//
// workers bounds concurrency; zero or less picks a small default. It is
// deliberately small — this is somebody else's issue tracker, and a detector
// that gets itself rate-limited stops being a detector.
func Prefetch(carriers []Carrier, snap SnapshotFunc, workers int) SnapshotFunc {
	if workers <= 0 {
		workers = 4
	}
	type key struct {
		repo   string
		number int
	}
	type answer struct {
		snap Snapshot
		err  error
	}

	uniq := map[key]bool{}
	var order []key
	for _, c := range carriers {
		k := key{c.Repo, c.Number}
		if uniq[k] {
			continue
		}
		uniq[k] = true
		order = append(order, k)
	}

	answers := make([]answer, len(order))
	// The same fixed pool as the store scan: the ref set is small today, but a
	// goroutine per ref parked on a semaphore is the shape #179 reported.
	mgscan.Pool(len(order), workers, func(i int) {
		k := order[i]
		s, err := snap(k.repo, k.number)
		answers[i] = answer{snap: s, err: err}
	})

	cache := make(map[key]answer, len(order))
	for i, k := range order {
		cache[k] = answers[i]
	}
	return func(repo string, number int) (Snapshot, error) {
		a, ok := cache[key{repo, number}]
		if !ok {
			// A carrier that was not in the prefetch set. Fetching it here rather
			// than returning a miss keeps Prefetch a performance decision and not
			// a coverage one — a caller that adds a carrier between the prefetch
			// and Detect gets a correct answer, slowly, instead of a wrong one.
			return snap(repo, number)
		}
		return a.snap, a.err
	}
}
