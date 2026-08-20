package stallwatch

import (
	"strings"
	"testing"
	"time"
)

// resolvingCapacity is a probe that behaves the way agent.RepoOccupancyFor does
// after mg-cd4a: a bare NAME is resolved to the repository path before the
// occupancy is read, and an unresolvable one answers known=false with a reason
// rather than answering "empty".
type resolvingCapacity struct {
	inner   *fakeCapacity
	byName  map[string]string
	unknown string
}

func (r *resolvingCapacity) CapacityFor(repo string) (RepoCapacity, bool) {
	if path, ok := r.byName[repo]; ok {
		return r.inner.CapacityFor(path)
	}
	if !strings.HasPrefix(repo, "/") {
		return RepoCapacity{Repo: repo, Cap: 3, Unresolved: r.unknown}, false
	}
	return r.inner.CapacityFor(repo)
}

// TestBareRepoNameStillGetsTheCapClause is mg-cd4a's headline, measured at the
// surface the ticket was filed about.
//
// On 2026-08-20 stall-watch sent, verbatim: "1 available work item(s) have sat
// unclaimed for over 10m0s — claim or dispatch them: mg-1763". The mayor
// attempted the dispatch it asked for and `pogo agent spawn-polecat` refused —
// the repository held its three workers. The item's `repo` field said `pogo`;
// five items in the same thread whose field said `/Users/daniel/dev/pogo` had
// carried the cap clause all night.
//
// The refusal was not the harm. The harm was the clause that went missing with
// it — the one naming the two responses a coordinator must NOT reach for.
func TestBareRepoNameStillGetsTheCapClause(t *testing.T) {
	inner := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "p161a", "pcbee", "pfcba"),
	}}
	cap := &resolvingCapacity{inner: inner, byName: map[string]string{"pogo": capRepoA}}
	w, rec, workRoot := capEnv(t, stallCfg(), cap)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-1763", "pogo", "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	// The measured message, refused at the moment it was given.
	if strings.Contains(msg, "claim or dispatch them") {
		t.Errorf("a bare repo NAME still gets the imperative the spawn point refuses: %s", msg)
	}
	// The payload. These two words are the whole of what went missing: at cap,
	// "dispatch now" has exactly two satisfying moves and both are damaging.
	for _, want := range []string{"preempt", "snooze"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the notice must rule out %q by name: %s", want, msg)
		}
	}
	for _, want := range []string{
		"throughput observation, not a dispatch request", "LATER", "wedged",
		capRepoA, "cap of 3", "p161a", "pcbee", "pfcba", "mg-1763",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in the at-cap notice for a bare-name item: %s", want, msg)
		}
	}
	// The report names the repository, not the spelling — a coordinator reading
	// it should not have to resolve `pogo` itself to check the claim.
	if strings.Contains(msg, " pogo is at its cap") {
		t.Errorf("the notice echoed the item's spelling back instead of the resolved path: %s", msg)
	}
}

// TestBothSpellingsOfOneRepoAreOneBucket: 42 items in the store spell this
// repository `pogo` and 883 spell it `/Users/daniel/dev/pogo` (counted by
// pm-pogo, 2026-08-20). A notice carrying both must report ONE saturated
// repository with one occupant list — reporting it twice would make the same
// three workers look like six.
func TestBothSpellingsOfOneRepoAreOneBucket(t *testing.T) {
	inner := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "p161a", "pcbee", "pfcba"),
	}}
	cap := &resolvingCapacity{inner: inner, byName: map[string]string{"pogo": capRepoA}}
	w, rec, workRoot := capEnv(t, stallCfg(), cap)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-1763", "pogo", "", now.Add(-30*time.Minute))
	writeRepoItem(t, workRoot, "mg-385f", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if n := strings.Count(msg, "is at its cap of 3"); n != 1 {
		t.Errorf("one repository was reported %d times; the two spellings must merge: %s", n, msg)
	}
	for _, id := range []string{"mg-1763", "mg-385f"} {
		if !strings.Contains(msg, id) {
			t.Errorf("item %s dropped from the merged bucket: %s", id, msg)
		}
	}
	// The event has to be countable too — both ids belong to the at-cap set.
	d := lastDetails(t, rec)
	ids, _ := d["at_cap_ids"].([]string)
	if len(ids) != 2 {
		t.Errorf("at_cap_ids = %v, want both items", d["at_cap_ids"])
	}
}

// TestUnresolvableRepoNameSaysSoAndStillWarnsOffThePreempt covers the branch
// the fix cannot resolve: 47 items name `union_closed`, which this host does
// not index. The occupancy is genuinely unknown — and "unknown" must not be a
// quieter way of saying "go ahead". The reader is told the count could not be
// taken, WHY, and that this notice is not grounds for preempting or snoozing
// anything.
func TestUnresolvableRepoNameSaysSoAndStillWarnsOffThePreempt(t *testing.T) {
	inner := &fakeCapacity{byRepo: map[string]RepoCapacity{}}
	cap := &resolvingCapacity{
		inner:   inner,
		byName:  map[string]string{},
		unknown: `"union_closed" is a repository NAME, not a path, and it matches no single repository known to this host`,
	}
	w, rec, workRoot := capEnv(t, stallCfg(), cap)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-abcd", "union_closed", "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if strings.Contains(msg, "claim or dispatch them") {
		t.Errorf("an unknown occupancy rendered as a clean imperative: %s", msg)
	}
	for _, want := range []string{
		"could not be determined", "NAME, not a path", "union_closed",
		"read the refusal", "preempt", "snooze", "mg-abcd",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in the unresolved notice: %s", want, msg)
		}
	}
	d := lastDetails(t, rec)
	if _, ok := d["occupancy_unknown_ids"]; !ok {
		t.Errorf("the unknown ids were not stamped on the event: %v", d)
	}
	if _, ok := d["occupancy_unresolved"]; !ok {
		t.Errorf("the REASON was not stamped, so events.log cannot count this cause apart: %v", d)
	}
	if _, ok := d["dispatchable_ids"]; ok {
		t.Errorf("an unresolvable item was stamped dispatchable: %v", d)
	}
}

// TestEmptyRepoFieldIsStillFree pins the boundary the fix must NOT cross. 280
// items carry an EMPTY repo field — a larger population than every bare
// spelling combined — and empty means something different: a --no-worktree
// dispatch contends for no repository's test suite, so zero really is the count
// and the imperative really is correct. Sweeping those into "unknown" would
// silence a true alarm on the largest group in the store.
func TestEmptyRepoFieldIsStillFree(t *testing.T) {
	inner := &fakeCapacity{byRepo: map[string]RepoCapacity{}}
	cap := &resolvingCapacity{inner: inner, byName: map[string]string{}, unknown: "unresolved"}
	w, rec, workRoot := capEnv(t, stallCfg(), cap)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-eeee", "", "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.Contains(msg, "claim or dispatch them: mg-eeee") {
		t.Errorf("an item with no repo lost the imperative that is correct for it: %s", msg)
	}
	if got := cap.inner.askedRepos(); len(got) != 0 {
		t.Errorf("the empty repo was probed: %v — probing \"\" invites a wiring where the empty "+
			"bucket answers for some other repo", got)
	}
}
