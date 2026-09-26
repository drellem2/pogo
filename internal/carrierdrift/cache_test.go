package carrierdrift

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/mgcontract"
	"github.com/drellem2/pogo/internal/mgscan"
)

// ---------------------------------------------------------------------------
// The mtime-keyed item cache and the fixed pool — ghintake's drellem2/pogo#179
// fix, applied to this package's identical scan (mg-e353).
// ---------------------------------------------------------------------------

func mgStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mg-stub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// countingStub is an mg stub that appends every invocation's argv to a log, so a
// test can COUNT forks rather than infer them. Carrier mg-a's mtime, body and
// status are read from files the test rewrites between passes; mg-b is a pending
// non-carrier that never changes.
type countingStub struct {
	bin, log, mtime, body, status string
}

func newCountingStub(t *testing.T) countingStub {
	t.Helper()
	dir := t.TempDir()
	s := countingStub{
		log:    filepath.Join(dir, "calls.log"),
		mtime:  filepath.Join(dir, "mtime"),
		body:   filepath.Join(dir, "body"),
		status: filepath.Join(dir, "status"),
	}
	s.set(t, "2026-09-26T10:00:00.000000001+01:00", `workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#7`, "available")
	s.bin = mgStub(t, `echo "$*" >> '`+s.log+`'
M=$(cat '`+s.mtime+`'); B=$(cat '`+s.body+`'); S=$(cat '`+s.status+`')
case "$*" in
  *"list --status=$S "*) echo "{\"id\":\"mg-a\",\"status\":\"$S\",\"mtime\":\"$M\"}" ;;
esac
case "$*" in
  *"list --status=pending"*) echo '{"id":"mg-b","status":"pending","mtime":"2026-01-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) printf '%s\n' "{\"id\":\"mg-a\",\"title\":\"triage\",\"status\":\"$S\",\"body\":\"$B\"}" ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"pending","body":"no marker here"}' ;;
esac
exit 0
`)
	return s
}

func (s countingStub) set(t *testing.T, mtime, body, status string) {
	t.Helper()
	for path, v := range map[string]string{s.mtime: mtime, s.body: body, s.status: status} {
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// forks returns how many `mg show` and `mg list` calls the log holds, and
// truncates it for the next pass.
func forks(t *testing.T, log string) (shows, lists int) {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		for i := 0; i < len(f); i++ {
			if f[i] == "--root" {
				i++
				continue
			}
			if f[i] == "show" {
				shows++
				break
			}
			if f[i] == "list" {
				lists++
				break
			}
		}
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return shows, lists
}

func carrierStrings(cs []Carrier) []string {
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s %s %s %s ack=%q", c.ID, c.Status, c.Stage, c.Ref(), c.DeclaredAck))
	}
	sort.Strings(out)
	return out
}

// An unchanged store costs its list forks and ZERO shows on the second pass.
//
// The scan is driven through a METHOD VALUE bound off a struct copy, exactly as
// pogod binds src.Carriers — so this also pins that the cache survives the copy.
func TestUnchangedItemsCostZeroShowForks(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewItemCache()}
	scan := src.Carriers
	nLists := len(src.Statuses())

	c1, scanned1, err := scan()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	shows, lists := forks(t, stub.log)
	// Positive control for the counter: the first pass must fork a show per item,
	// or a zero below would prove nothing.
	if shows != 2 || lists != nLists {
		t.Fatalf("pass 1 forked %d shows and %d lists, want 2 and %d", shows, lists, nLists)
	}

	c2, scanned2, err := scan()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	shows, lists = forks(t, stub.log)
	if shows != 0 {
		t.Errorf("pass 2 over an unchanged store forked %d `mg show` calls, want 0", shows)
	}
	if lists != nLists {
		t.Errorf("pass 2 forked %d lists, want %d — the listing is what validates the cache", lists, nLists)
	}
	if scanned1 != 2 || scanned2 != scanned1 ||
		strings.Join(carrierStrings(c2), "|") != strings.Join(carrierStrings(c1), "|") {
		t.Errorf("a cached pass must answer exactly as a fresh one:\n pass 1: %d %v\n pass 2: %d %v",
			scanned1, carrierStrings(c1), scanned2, carrierStrings(c2))
	}
}

// The positive control for invalidation: rewrite a carrier's body AND move its
// mtime, and the next pass must re-fork `mg show` for that item — only that
// item — and report the new stage.
func TestAChangedMtimeRereadsTheItem(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewItemCache()}
	if _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	forks(t, stub.log)

	stub.set(t, "2026-09-26T11:00:00.000000002+01:00", `workflow: gh-issue\nstage: build\ngh: drellem2/pogo#8`, "available")
	cs, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, stub.log); shows != 1 {
		t.Errorf("after mg-a's mtime moved the pass forked %d shows, want exactly 1 (mg-a)", shows)
	}
	got := carrierStrings(cs)
	if len(got) != 1 || got[0] != `mg-a available build drellem2/pogo#8 ack=""` {
		t.Errorf("carriers after the edit = %v, want mg-a at stage build on #8", got)
	}
}

// A claim or a shelve moves the file with a rename, and a rename keeps the
// mtime. So a cache hit must take Status from THIS pass's list row, never from
// the `mg show` that populated the cache.
func TestACachedCarrierReportsItsCurrentStatus(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewItemCache()}
	if _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	forks(t, stub.log)

	stub.set(t, "2026-09-26T10:00:00.000000001+01:00", `workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#7`, "claimed") // same mtime
	cs, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, stub.log); shows != 0 {
		t.Fatalf("a status change at an unchanged mtime forked %d shows; the setup expects a cache hit", shows)
	}
	got := carrierStrings(cs)
	if len(got) != 1 || got[0] != `mg-a claimed triage drellem2/pogo#7 ack=""` {
		t.Errorf("carriers = %v, want mg-a reported as claimed (the list row), not available (the cached show)", got)
	}
}

// Without a cache every pass re-reads every item — the one-shot CLI path, and
// the control that shows the zero above comes from the cache and not the stub.
func TestNoCacheForksEveryPass(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin}
	for pass := 1; pass <= 2; pass++ {
		if _, _, err := src.Carriers(); err != nil {
			t.Fatal(err)
		}
		if shows, _ := forks(t, stub.log); shows != 2 {
			t.Errorf("pass %d without a cache forked %d shows, want 2", pass, shows)
		}
	}
}

// An id listed twice in one pass — an item claimed between the available and
// claimed listings — names two moments of one file, and one cache slot keyed on
// one mtime cannot describe both. It is re-read every pass and never stored.
func TestTwinsBypassTheCache(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := mgStub(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"list --status=available"*) echo '{"id":"mg-t","status":"available","mtime":"2026-03-01T00:00:00Z"}' ;;
  *"list --status=claimed"*)   echo '{"id":"mg-t","status":"claimed","mtime":"2026-03-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-t"*) printf '%s\n' '{"id":"mg-t","status":"claimed","body":"workflow: gh-issue\ngh: drellem2/pogo#12"}' ;;
esac
exit 0
`)
	src := MGSource{Bin: stub, Cache: NewItemCache()}
	for pass := 1; pass <= 2; pass++ {
		cs, scanned, err := src.Carriers()
		if err != nil || scanned != 1 || len(cs) != 1 {
			t.Fatalf("pass %d: carriers=%+v scanned=%d err=%v", pass, cs, scanned, err)
		}
		if shows, _ := forks(t, log); shows != 1 {
			t.Errorf("pass %d forked %d shows for the twin, want 1 — twins must never be answered from the cache", pass, shows)
		}
	}
	if n := src.Cache.Len(); n != 0 {
		t.Errorf("cache holds %d entries, want 0 — a twin must never be stored", n)
	}
}

// A failed read is retried next pass rather than remembered: caching it at an
// unchanged mtime would leave the item unexamined until somebody edits it.
func TestFailedReadsAreNotCached(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := mgStub(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"list --status=available"*) echo '{"id":"mg-bad","status":"available","mtime":"2026-01-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-bad"*) echo 'kaboom' >&2; exit 1 ;;
esac
exit 0
`)
	src := MGSource{Bin: stub, Cache: NewItemCache()}
	for pass := 1; pass <= 2; pass++ {
		_, scanned, err := src.Carriers()
		if err != nil || scanned != 0 {
			t.Fatalf("pass %d: scanned=%d err=%v, want the item counted unexamined", pass, scanned, err)
		}
		if shows, _ := forks(t, log); shows != 1 {
			t.Errorf("pass %d forked %d shows for a failing item, want 1 (retried every pass)", pass, shows)
		}
	}
}

// An item that leaves the live listing — done, archived, or shelved when shelved
// is not scanned — leaves the cache, so a long-lived pogod does not pin every id
// it has ever seen.
func TestCacheIsPrunedToTheListing(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewItemCache()}
	if _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	if n := src.Cache.Len(); n != 2 {
		t.Fatalf("cache holds %d entries after pass 1, want 2", n)
	}
	stub.set(t, "2026-09-26T10:00:00.000000001+01:00", `workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#7`, "done")
	if _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	if _, ok := src.Cache.Lookup("mg-a", "2026-09-26T10:00:00.000000001+01:00"); ok {
		t.Error("mg-a is no longer listed and must have been dropped from the cache")
	}
	if n := src.Cache.Len(); n != 1 {
		t.Errorf("cache holds %d entries, want 1 (mg-b)", n)
	}
}

// The full-stack version of the invalidation control, against the REAL mg: edit
// a carrier's body with `mg edit` (so the real store moves the mtime, not the
// test), and the next pass must re-fork `mg show` for it and see the new line.
func TestRealStoreEditIsPickedUpAndUnchangedCostsNothing(t *testing.T) {
	real, err := exec.LookPath("mg")
	if err != nil {
		t.Skip("mg binary not on PATH")
	}
	mgcontract.Require(t, mgcontract.ListJSONMtimeIsStableAndAnEditMovesIt)

	root := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(real, append([]string{"--root", root}, args...)...)
		cmd.Env = append(os.Environ(), "MG_ROOT="+root)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mg %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("", "init")
	run("", "new", "--type=task", "--no-repo", "--title=FIXTURE carrier",
		"--body=workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#100\n")
	run("", "new", "--type=task", "--no-repo", "--title=FIXTURE other", "--body=not a carrier\n")

	log := filepath.Join(t.TempDir(), "calls.log")
	wrap := mgStub(t, `echo "$*" >> '`+log+`'
exec '`+real+`' "$@"
`)
	src := MGSource{Root: root, Bin: wrap, Cache: NewItemCache()}

	cs, scanned, err := src.Carriers()
	if err != nil || scanned != 2 || len(cs) != 1 {
		t.Fatalf("pass 1: err=%v scanned=%d carriers=%+v", err, scanned, cs)
	}
	if shows, _ := forks(t, log); shows != 2 {
		t.Fatalf("pass 1 forked %d shows, want 2", shows)
	}

	if _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, log); shows != 0 {
		t.Errorf("pass 2 over an unchanged real store forked %d shows, want 0", shows)
	}

	// Edit the carrier's body through mg itself. ParseBody takes the FIRST
	// occurrence of each key, so the edit adds a key the body did not have.
	run("gh-ack: reporter answered\n", "edit", cs[0].ID, "--append-body-file", "-")

	cs3, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, log); shows != 1 {
		t.Errorf("after `mg edit` the pass forked %d shows, want exactly 1 (the edited item)", shows)
	}
	if len(cs3) != 1 || cs3[0].DeclaredAck != "reporter answered" {
		t.Errorf("the edit was not picked up: carriers = %+v", cs3)
	}
}

// Carriers must run its `mg show`s on a fixed pool (mgscan.Pool), not one
// goroutine per item gated by a semaphore — the shape drellem2/pogo#179 profiled
// in ghintake's identical scan. Both shapes bound the concurrent FORKS, so
// counting forks cannot tell them apart; counting GOROUTINES while the forks are
// held open can.
func TestCarriersRunsAFixedPoolNotAGoroutinePerItem(t *testing.T) {
	const items, workers = 200, 2
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	started := filepath.Join(dir, "started")
	var rows strings.Builder
	for i := 0; i < items; i++ {
		fmt.Fprintf(&rows, `{"id":"mg-%04x","status":"available","mtime":"2026-01-01T00:00:00Z"}`+"\n", i)
	}
	listing := filepath.Join(dir, "listing")
	if err := os.WriteFile(listing, []byte(rows.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := mgStub(t, `
case "$*" in
  *"list --status=available"*) cat '`+listing+`' ;;
  *list*) ;;
  *show*) echo x >> '`+started+`'
          while [ ! -f '`+gate+`' ]; do sleep 0.01; done
          echo '{"id":"mg-x","status":"available","body":"nothing"}' ;;
esac
exit 0
`)
	// Open the gate however this test exits, or 200 stub processes would spin
	// on it for the life of the host.
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })

	src := MGSource{Bin: stub, Root: filepath.Join(dir, "store"), Workers: workers}
	baseline := runtime.NumGoroutine()

	done := make(chan error, 1)
	go func() {
		_, scanned, err := src.Carriers()
		if err == nil && scanned != items {
			err = fmt.Errorf("scanned %d items, want %d", scanned, items)
		}
		done <- err
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, _ := os.ReadFile(started)
		if strings.Count(string(raw), "x") >= workers {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the scan returned before any show was held open: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the scan never had %d shows in flight", workers)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Settle: a goroutine-per-item scan would still be starting goroutines.
	time.Sleep(200 * time.Millisecond)
	extra := runtime.NumGoroutine() - baseline
	t.Logf("%d goroutines above baseline with %d-item scan frozen mid-pass", extra, items)

	raw, _ := os.ReadFile(started)
	inFlight := strings.Count(string(raw), "x")

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if inFlight > workers {
		t.Errorf("%d `mg show`s were in flight with Workers=%d", inFlight, workers)
	}
	if limit := 40; extra > limit {
		t.Errorf("%d goroutines were live during a %d-item scan with Workers=%d, want at most %d — "+
			"the scan is starting a goroutine per item again (drellem2/pogo#179)", extra, items, workers, limit)
	}
}

// Prefetch had the same goroutine-per-element shape over refs. With every
// re-read held open, the goroutines live are the pool's, not one per ref.
func TestPrefetchRunsAFixedPool(t *testing.T) {
	const refs, workers = 200, 2
	var carriers []Carrier
	for i := 1; i <= refs; i++ {
		carriers = append(carriers, carrier(fmt.Sprintf("mg-%04x", i), fmt.Sprintf("drellem2/pogo#%d", i), "build", time.Hour))
	}
	gate := make(chan struct{})
	entered := make(chan struct{}, refs)
	snap := func(string, int) (Snapshot, error) {
		entered <- struct{}{}
		<-gate
		return Snapshot{State: StateOpen}, nil
	}
	baseline := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		Prefetch(carriers, snap, workers)
		close(done)
	}()
	for i := 0; i < workers; i++ {
		<-entered
	}
	time.Sleep(100 * time.Millisecond)
	extra := runtime.NumGoroutine() - baseline
	close(gate)
	<-done
	// The Prefetch goroutine plus its workers.
	if limit := workers + 3; extra > limit {
		t.Errorf("%d goroutines live during a %d-ref prefetch with workers=%d, want at most %d", extra, refs, workers, limit)
	}
	if n := len(entered); n != refs-workers {
		t.Errorf("%d re-reads after the gate opened, want %d (every ref fetched exactly once)", n, refs-workers)
	}
}

// ItemCache is mgscan's cache, not a copy of it: the reuse the ticket asked for.
var _ *mgscan.Cache[cachedItem] = NewItemCache()
