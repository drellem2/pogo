package ghintake

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mgscan"
)

// ---------------------------------------------------------------------------
// The mtime-keyed ref cache (drellem2/pogo#179)
// ---------------------------------------------------------------------------

// countingStub is an mg stub that appends every invocation's argv to a log, so a
// test can COUNT forks rather than infer them. Item mg-a's mtime and body are
// read from files the test rewrites between passes; mg-b never changes.
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
	s.set(t, "2026-09-26T10:00:00.000000001+01:00", "gh: drellem2/pogo#7", "done")
	s.bin = mgStub(t, `echo "$*" >> '`+s.log+`'
M=$(cat '`+s.mtime+`'); B=$(cat '`+s.body+`'); S=$(cat '`+s.status+`')
case "$*" in
  *"list --status=$S "*) echo "{\"id\":\"mg-a\",\"status\":\"$S\",\"mtime\":\"$M\"}" ;;
esac
case "$*" in
  *"list --status=done"*) echo '{"id":"mg-b","status":"done","mtime":"2026-01-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) echo "{\"id\":\"mg-a\",\"status\":\"$S\",\"body\":\"$B\"}" ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"done","body":"no marker here"}' ;;
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
		for i, w := range f {
			if w == "--root" {
				i++
				continue
			}
			if w == "show" {
				shows++
				break
			}
			if w == "list" {
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

func refStrings(refs []CarrierRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.ItemID+" "+r.Status+" "+r.Ref)
	}
	sort.Strings(out)
	return out
}

// An unchanged store costs its six list forks and ZERO shows on the second pass.
//
// The scan is driven through a METHOD VALUE bound off a struct copy, exactly as
// pogod binds src.Carriers — so this also pins that the cache survives the copy.
// An embedded (non-pointer) cache would pass the first half and fail the second.
func TestUnchangedItemsCostZeroShowForks(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewRefCache()}
	scan := src.Carriers

	refs1, scanned1, bad1, err := scan()
	if err != nil || len(bad1) != 0 {
		t.Fatalf("pass 1: err=%v bad=%+v", err, bad1)
	}
	shows, lists := forks(t, stub.log)
	// Positive control for the counter: the first pass must fork a show per item,
	// or a zero below would prove nothing.
	if shows != 2 || lists != len(mgStatuses) {
		t.Fatalf("pass 1 forked %d shows and %d lists, want 2 and %d", shows, lists, len(mgStatuses))
	}

	refs2, scanned2, bad2, err := scan()
	if err != nil || len(bad2) != 0 {
		t.Fatalf("pass 2: err=%v bad=%+v", err, bad2)
	}
	shows, lists = forks(t, stub.log)
	if shows != 0 {
		t.Errorf("pass 2 over an unchanged store forked %d `mg show` calls, want 0", shows)
	}
	if lists != len(mgStatuses) {
		t.Errorf("pass 2 forked %d lists, want %d — the listing is what validates the cache", lists, len(mgStatuses))
	}
	if scanned2 != scanned1 || strings.Join(refStrings(refs2), "|") != strings.Join(refStrings(refs1), "|") {
		t.Errorf("a cached pass must answer exactly as a fresh one:\n pass 1: %d %v\n pass 2: %d %v",
			scanned1, refStrings(refs1), scanned2, refStrings(refs2))
	}
}

// The positive control for invalidation: rewrite an item's body AND move its
// mtime, and the next pass must re-fork `mg show` for that item — only that
// item — and report the new refs.
func TestAChangedMtimeRereadsTheItem(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewRefCache()}
	if _, _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	forks(t, stub.log)

	stub.set(t, "2026-09-26T11:00:00.000000002+01:00", "gh: drellem2/pogo#8", "done")
	refs, _, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, stub.log); shows != 1 {
		t.Errorf("after mg-a's mtime moved the pass forked %d shows, want exactly 1 (mg-a)", shows)
	}
	got := refStrings(refs)
	if len(got) != 1 || got[0] != "mg-a done drellem2/pogo#8" {
		t.Errorf("refs after the edit = %v, want [mg-a done drellem2/pogo#8]", got)
	}
}

// `mg done` / `mg archive` move a file with a rename, and a rename keeps the
// mtime. So a cache hit must take Status from THIS pass's list row, never from
// the `mg show` that populated the cache.
func TestACachedItemReportsItsCurrentStatus(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewRefCache()}
	if _, _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	forks(t, stub.log)

	stub.set(t, "2026-09-26T10:00:00.000000001+01:00", "gh: drellem2/pogo#7", "archived") // same mtime
	refs, _, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, stub.log); shows != 0 {
		t.Fatalf("a status change at an unchanged mtime forked %d shows; the setup expects a cache hit", shows)
	}
	got := refStrings(refs)
	if len(got) != 1 || got[0] != "mg-a archived drellem2/pogo#7" {
		t.Errorf("refs = %v, want mg-a reported as archived (the list row), not done (the cached show)", got)
	}
}

// Without a cache every pass re-reads every item — the one-shot CLI path, and
// the control that shows the zero above comes from the cache and not the stub.
func TestNoCacheForksEveryPass(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin}
	for pass := 1; pass <= 2; pass++ {
		if _, _, _, err := src.Carriers(); err != nil {
			t.Fatal(err)
		}
		if shows, _ := forks(t, stub.log); shows != 2 {
			t.Errorf("pass %d without a cache forked %d shows, want 2", pass, shows)
		}
	}
}

// Archived twins share a short id but are two files with two mtimes; one cache
// slot cannot describe both. They must be re-read on EVERY pass. These are the
// dozen items most likely to regress quietly, so the bypass is pinned by count.
func TestTwinsBypassTheCache(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := mgStub(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"list --status=archived"*)
     echo '{"id":"mg-3119","status":"archived","mtime":"2026-03-01T00:00:00Z"}'
     echo '{"id":"mg-3119","status":"archived","mtime":"2026-05-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-3119@2026-03"*) echo '{"id":"mg-3119","status":"archived","body":"gh: drellem2/pogo#12"}' ;;
  *"show mg-3119@2026-05"*) echo '{"id":"mg-3119","status":"archived","body":"gh: drellem2/pogo#34"}' ;;
  *"show mg-3119"*)
     echo '{"error":{"code":"ambiguous_id","message":"mg-3119: ambiguous — 2 work items share this ID:\n  work/archive/2026-03/mg-3119.md (archived)\n  work/archive/2026-05/mg-3119.md (archived)"}}' >&2
     exit 4 ;;
esac
exit 0
`)
	src := MGSource{Bin: stub, Cache: NewRefCache()}
	for pass := 1; pass <= 2; pass++ {
		refs, _, bad, err := src.Carriers()
		if err != nil || len(bad) != 0 {
			t.Fatalf("pass %d: err=%v bad=%+v", pass, err, bad)
		}
		// One ambiguous show plus one qualified show per partition.
		if shows, _ := forks(t, log); shows != 3 {
			t.Errorf("pass %d forked %d shows for the twin, want 3 — twins must never be answered from the cache", pass, shows)
		}
		if len(refs) != 2 {
			t.Errorf("pass %d: both twins' refs must be unioned, got %+v", pass, refs)
		}
	}
	if n := src.Cache.Len(); n != 0 {
		t.Errorf("cache holds %d entries, want 0 — a twin must never be stored", n)
	}
}

// A failed read is retried next pass rather than remembered: caching an
// ItemError at an unchanged mtime would make a transient failure permanent.
func TestItemErrorsAreNotCached(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := mgStub(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"list --status=done"*) echo '{"id":"mg-bad","status":"done","mtime":"2026-01-01T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-bad"*) echo 'kaboom' >&2; exit 1 ;;
esac
exit 0
`)
	src := MGSource{Bin: stub, Cache: NewRefCache()}
	for pass := 1; pass <= 2; pass++ {
		_, _, bad, err := src.Carriers()
		if err != nil || len(bad) != 1 {
			t.Fatalf("pass %d: err=%v bad=%+v, want one ItemError", pass, err, bad)
		}
		if shows, _ := forks(t, log); shows != 1 {
			t.Errorf("pass %d forked %d shows for a failing item, want 1 (retried every pass)", pass, shows)
		}
	}
}

// An item that leaves the listing leaves the cache, so a long-lived pogod does
// not pin every id it has ever seen.
func TestCacheIsPrunedToTheListing(t *testing.T) {
	stub := newCountingStub(t)
	src := MGSource{Bin: stub.bin, Cache: NewRefCache()}
	if _, _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	if n := src.Cache.Len(); n != 2 {
		t.Fatalf("cache holds %d entries after pass 1, want 2", n)
	}
	stub.set(t, "2026-09-26T10:00:00.000000001+01:00", "gh: drellem2/pogo#7", "gone-status-not-scanned")
	if _, _, _, err := src.Carriers(); err != nil {
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
// an item's body with `mg edit` (so the real store moves the mtime, not the test),
// and the next pass must re-fork `mg show` for it and see the new ref. The stub
// here only logs and execs the real binary, so the forks are counted.
func TestRealStoreEditIsPickedUpAndUnchangedCostsNothing(t *testing.T) {
	if !mgAvailable(t) {
		t.Skip("mg binary not on PATH")
	}
	real, err := exec.LookPath("mg")
	if err != nil {
		t.Fatal(err)
	}
	root := scratchStore(t, map[string]string{
		"FIXTURE carrier": "workflow: gh-issue\ngh: drellem2/pogo#100\n",
		"FIXTURE other":   "not a carrier\n",
	})
	log := filepath.Join(t.TempDir(), "calls.log")
	wrap := mgStub(t, `echo "$*" >> '`+log+`'
exec '`+real+`' "$@"
`)
	src := MGSource{Root: root, Bin: wrap, Cache: NewRefCache()}

	refs, scanned, _, err := src.Carriers()
	if err != nil || scanned != 2 || len(refs) != 1 {
		t.Fatalf("pass 1: err=%v scanned=%d refs=%+v", err, scanned, refs)
	}
	if shows, _ := forks(t, log); shows != 2 {
		t.Fatalf("pass 1 forked %d shows, want 2", shows)
	}

	if _, _, _, err := src.Carriers(); err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, log); shows != 0 {
		t.Errorf("pass 2 over an unchanged real store forked %d shows, want 0", shows)
	}

	// Edit the carrier's body through mg itself.
	listed, err := exec.Command(real, "--root", root, "list", "--status=available", "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := mgscan.ParseList(listed)
	if err != nil {
		t.Fatal(err)
	}
	carrier := ""
	for _, r := range rows {
		if r.ID == refs[0].ItemID {
			carrier = r.ID
		}
	}
	if carrier == "" {
		t.Fatalf("setup: carrier %s not in the available listing %+v", refs[0].ItemID, rows)
	}
	edit := exec.Command(real, "--root", root, "edit", carrier, "--append-body-file", "-")
	edit.Stdin = strings.NewReader("gh: drellem2/pogo#99\n")
	edit.Env = append(os.Environ(), "MG_ROOT="+root)
	if out, err := edit.CombinedOutput(); err != nil {
		t.Fatalf("mg edit: %v\n%s", err, out)
	}
	forks(t, log) // discard the edit's own call, if the wrapper saw it

	refs3, _, _, err := src.Carriers()
	if err != nil {
		t.Fatal(err)
	}
	if shows, _ := forks(t, log); shows != 1 {
		t.Errorf("after `mg edit` the pass forked %d shows, want exactly 1 (the edited item)", shows)
	}
	got := map[string]bool{}
	for _, r := range refs3 {
		got[r.Ref] = true
	}
	if !got["drellem2/pogo#99"] || !got["drellem2/pogo#100"] {
		t.Errorf("the edit was not picked up: refs = %+v", refs3)
	}
}
