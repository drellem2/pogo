package verdictwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file build a macguffin store BY HAND, with no `mg` binary in
// the path at all. They are the fast, always-run half of this package's cover,
// and they are deliberately not the whole of it: a hand-built fixture cannot
// notice mg renaming a field, which is why probe_test.go drives the real binary.
// Neither half substitutes for the other.

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir()}
	for _, d := range []string{
		filepath.Join("work", "available"),
		filepath.Join("work", "claimed"),
		filepath.Join("work", "done"),
		filepath.Join("work", "archive", "2026-08"),
		"mail",
	} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
	}
	f.appendEvent("")
	return f
}

// item writes a work item into statusDir with the given filer.
func (f *fixture) item(id, creator, title, statusDir string) {
	f.t.Helper()
	body := fmt.Sprintf("---\nid: %s\ncreator: %s\ncreated: 2026-08-01T00:00:00Z\ntype: task\n---\n\n# %s\n\nbody\n",
		id, creator, title)
	path := filepath.Join(f.root, "work", statusDir, id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatalf("write item: %v", err)
	}
}

// sidecar writes the refinery-shaped result file that names the worker's branch.
func (f *fixture) sidecar(id, statusDir, branch string) {
	f.t.Helper()
	path := filepath.Join(f.root, "work", statusDir, id+".result.json")
	if err := os.WriteFile(path, []byte(`{"branch":"`+branch+`","completed_by":"refinery"}`), 0o644); err != nil {
		f.t.Fatalf("write sidecar: %v", err)
	}
}

// land records a work.done or work.archive event.
func (f *fixture) land(id, ts, kind string) {
	f.t.Helper()
	typ := "work.done"
	if kind == "archived" {
		typ = "work.archive"
	}
	f.appendEvent(fmt.Sprintf(`{"actor":"daniel","item_id":%q,"ts":%q,"type":%q}`, id, ts, typ))
}

// sent records a mail.sent event, which is what MailsElsewhere counts.
func (f *fixture) sent(from, to, ts string) {
	f.t.Helper()
	f.appendEvent(fmt.Sprintf(`{"from":%q,"to":%q,"ts":%q,"type":"mail.sent","msg_id":"x"}`, from, to, ts))
}

func (f *fixture) appendEvent(line string) {
	f.t.Helper()
	fh, err := os.OpenFile(filepath.Join(f.root, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		f.t.Fatalf("open events.jsonl: %v", err)
	}
	defer fh.Close()
	if line != "" {
		if _, err := fh.WriteString(line + "\n"); err != nil {
			f.t.Fatalf("write event: %v", err)
		}
	}
}

// mail delivers a message into a mailbox in the given maildir state.
func (f *fixture) mail(box, state, from, subject, date string) {
	f.t.Helper()
	dir := filepath.Join(f.root, "mail", box, state)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("make mailbox: %v", err)
	}
	name := fmt.Sprintf("%d.%s.msg", len(subject)*7919+len(from), state)
	body := fmt.Sprintf("From: %s\nSubject: %s\nDate: %s\n\nbody\n", from, subject, date)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatalf("write message: %v", err)
	}
}

// emptyBox registers a mailbox that exists and holds nothing — which is a
// different state from a filer who has never received any mail at all.
func (f *fixture) emptyBox(box string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, "mail", box, "new"), 0o755); err != nil {
		f.t.Fatalf("make mailbox: %v", err)
	}
}

func (f *fixture) scan(opts Options) Report {
	f.t.Helper()
	opts.Root = f.root
	rep, err := Scan(opts)
	if err != nil {
		f.t.Fatalf("Scan: %v", err)
	}
	return rep
}

func statusIn(t *testing.T, rep Report, id string) Status {
	t.Helper()
	for _, row := range rep.Rows {
		if row.ID == id {
			return row.Status
		}
	}
	return "ABSENT"
}

// TestTheMatchedPair is the predicate itself: one item whose verdict was never
// mailed, one whose was, and the detector must separate them.
func TestTheMatchedPair(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")

	f.item("mg-aaaa", "filer-a", "the verdict is dropped on purpose", "done")
	f.sidecar("mg-aaaa", "done", "polecat-wa1")
	f.land("mg-aaaa", "2026-08-01T10:00:00Z", "done")

	f.item("mg-bbbb", "filer-a", "the verdict is delivered", "done")
	f.sidecar("mg-bbbb", "done", "polecat-wb1")
	f.land("mg-bbbb", "2026-08-01T11:00:00Z", "done")
	f.mail("filer-a", "cur", "wb1", "mg-bbbb VERDICT", "2026-08-01T10:55:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-aaaa"); got != Dropped {
		t.Errorf("the arm with no verdict mail is %s, want %s", got, Dropped)
	}
	if got := statusIn(t, rep, "mg-bbbb"); got != Delivered {
		t.Errorf("the arm whose verdict was mailed is %s, want %s", got, Delivered)
	}
	if rep.Dropped != 1 || rep.Delivered != 1 {
		t.Errorf("counts are %d dropped / %d delivered, want 1 / 1", rep.Dropped, rep.Delivered)
	}
	if !rep.Actionable() {
		t.Error("a report holding a dropped verdict is not Actionable()")
	}
	if rep.InstrumentFailure() {
		t.Errorf("a scan that judged two items reports itself blind: %v", rep.Blind)
	}
}

// TestAnArchivedItemStillCounts covers the second half of "done OR archived",
// including the nested archive/<month>/ layout a flat status-directory scan
// cannot see.
func TestAnArchivedItemStillCounts(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	path := filepath.Join("archive", "2026-08")
	f.item("mg-cccc", "filer-a", "archived, verdict dropped", path)
	f.sidecar("mg-cccc", path, "polecat-wc1")
	f.land("mg-cccc", "2026-08-02T10:00:00Z", "archived")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-cccc"); got != Dropped {
		t.Errorf("an archived item with no verdict is %s, want %s", got, Dropped)
	}
	if rep.Rows[0].Kind != "archived" {
		t.Errorf("kind is %q, want archived", rep.Rows[0].Kind)
	}
}

// TestAnArchivedVerdictIsStillDelivered is the case that forces the maildir to
// be read directly: `mg mail list --all` does not include archived mail, so a
// filer who filed the verdict away would read as never having received one.
func TestAnArchivedVerdictIsStillDelivered(t *testing.T) {
	f := newFixture(t)
	f.item("mg-dddd", "filer-a", "verdict delivered then archived", "done")
	f.sidecar("mg-dddd", "done", "polecat-wd1")
	f.land("mg-dddd", "2026-08-02T10:00:00Z", "done")
	f.mail("filer-a", "archive", "wd1", "mg-dddd VERDICT", "2026-08-02T09:00:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-dddd"); got != Delivered {
		t.Errorf("a verdict the filer archived is %s, want %s — inbox hygiene must not read as a dropped verdict", got, Delivered)
	}
}

// TestAFilerWithNoMailboxIsFlagged separates "they got no verdicts" from
// "nobody has ever written to them", which are different findings.
func TestAFilerWithNoMailboxIsFlagged(t *testing.T) {
	f := newFixture(t)
	f.item("mg-eeee", "filer-nobox", "filer has never received any mail", "done")
	f.sidecar("mg-eeee", "done", "polecat-we1")
	f.land("mg-eeee", "2026-08-02T10:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-nobox"})
	if got := statusIn(t, rep, "mg-eeee"); got != Dropped {
		t.Errorf("a mailbox-less filer's item is %s, want %s", got, Dropped)
	}
	if len(rep.MissingBoxes) != 1 || rep.MissingBoxes[0] != "filer-nobox" {
		t.Errorf("MissingBoxes = %v, want [filer-nobox]; a missing mailbox must not be silently treated as an empty one", rep.MissingBoxes)
	}
	if !strings.Contains(rep.Render(false, false), "NO MAILBOX") {
		t.Error("the rendered report does not say the filer has no mailbox")
	}
}

// TestAVerdictMailedElsewhereIsStillDropped — delivery to mayor is not delivery
// to the filer, and the row records that the worker was alive and mailing.
func TestAVerdictMailedElsewhereIsStillDropped(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-ffff", "filer-a", "verdict mailed to mayor, not the filer", "done")
	f.sidecar("mg-ffff", "done", "polecat-wf1")
	f.land("mg-ffff", "2026-08-02T10:00:00Z", "done")
	f.mail("mayor", "cur", "wf1", "mg-ffff VERDICT", "2026-08-02T09:00:00Z")
	f.sent("wf1", "mayor", "2026-08-02T09:00:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-ffff"); got != Dropped {
		t.Errorf("a verdict mailed only to mayor is %s, want %s", got, Dropped)
	}
	if rep.Rows[0].MailsElsewhere != 1 {
		t.Errorf("MailsElsewhere = %d, want 1; a live worker that mailed the wrong addressee must not look like a dead one",
			rep.Rows[0].MailsElsewhere)
	}
}

// TestAnItemThatHasNotLandedIsAbsent — the predicate is about landings, and an
// item still in flight owes nobody a verdict yet.
func TestAnItemThatHasNotLandedIsAbsent(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-1111", "filer-a", "claimed, still in flight", "claimed")
	f.item("mg-2222", "filer-a", "landed", "done")
	f.sidecar("mg-2222", "done", "polecat-w22")
	f.land("mg-2222", "2026-08-02T10:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-1111"); got != "ABSENT" {
		t.Errorf("an item that has not landed is reported as %s; it must not be in the report at all", got)
	}
	if rep.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1", rep.Scanned)
	}
}

// TestANearMissSenderIsNotTheWorker — a sender whose name merely CONTAINS the
// worker's is somebody else.
func TestANearMissSenderIsNotTheWorker(t *testing.T) {
	f := newFixture(t)
	f.item("mg-3333", "filer-a", "near-miss sender name", "done")
	f.sidecar("mg-3333", "done", "polecat-wh1")
	f.land("mg-3333", "2026-08-02T10:00:00Z", "done")
	f.mail("filer-a", "cur", "wh1x", "mg-3333 not from the worker", "2026-08-02T09:00:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-3333"); got != Dropped {
		t.Errorf("a message from wh1x is %s for a worker named wh1, want %s", got, Dropped)
	}
}

// TestTheMgPrefixIsNotASecondAgent — mg strips a leading `mg-`, so a worker who
// signs `mg-ab12` and one who signs `ab12` are the same agent. Comparing the raw
// strings would report a delivered verdict as dropped.
func TestTheMgPrefixIsNotASecondAgent(t *testing.T) {
	f := newFixture(t)
	f.item("mg-4444", "filer-a", "worker signs with the mg- prefix", "done")
	f.sidecar("mg-4444", "done", "polecat-ab12")
	f.land("mg-4444", "2026-08-02T10:00:00Z", "done")
	f.mail("filer-a", "new", "mg-ab12", "mg-4444 VERDICT", "2026-08-02T09:00:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-4444"); got != Delivered {
		t.Errorf("a verdict signed `mg-ab12` from worker `ab12` is %s, want %s", got, Delivered)
	}
}

// TestAnUnresolvableWorkerIsUndecidableNotDropped is D-2: the detector's own
// blind spot is counted and listed, never folded into a verdict.
func TestAnUnresolvableWorkerIsUndecidableNotDropped(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	// No sidecar at all, and an id whose shape resolves to nobody who wrote.
	f.item("mg-5555", "filer-a", "no sidecar, no matching sender", "done")
	f.land("mg-5555", "2026-08-02T10:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-5555"); got != Undecidable {
		t.Errorf("an item whose worker cannot be resolved is %s, want %s", got, Undecidable)
	}
	if rep.Actionable() {
		t.Error("an UNDECIDABLE row made the report Actionable(); the detector's own reach is not a finding about the fleet")
	}
}

// TestTheShapeResolverCanOnlyShrinkTheDropCount is the declared asymmetry. The
// fallback is accepted only when the name it proposes actually wrote to this
// filer, so it can move a row UNDECIDABLE -> DELIVERED and never to DROPPED.
func TestTheShapeResolverCanOnlyShrinkTheDropCount(t *testing.T) {
	f := newFixture(t)
	// No sidecar; the worker is named after the item by convention (z5678) and
	// its mail IS in the filer's box.
	f.item("mg-5678", "filer-a", "worker named by convention", "done")
	f.land("mg-5678", "2026-08-02T10:00:00Z", "done")
	f.mail("filer-a", "cur", "z5678", "mg-5678 VERDICT", "2026-08-02T09:00:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-5678"); got != Delivered {
		t.Errorf("shape-resolved worker z5678 for mg-5678 is %s, want %s", got, Delivered)
	}
	if rep.Rows[0].Resolver != "shape" {
		t.Errorf("Resolver = %q, want %q; a reader must be able to tell a recorded worker from an inferred one",
			rep.Rows[0].Resolver, "shape")
	}

	// The same shape with NOBODY of that name having written: the fallback must
	// decline rather than invent a worker and report a drop against it.
	g := newFixture(t)
	g.emptyBox("filer-a")
	g.item("mg-5679", "filer-a", "worker named by convention, silent", "done")
	g.land("mg-5679", "2026-08-02T10:00:00Z", "done")
	rep2 := g.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep2, "mg-5679"); got != Undecidable {
		t.Errorf("with no sidecar and no matching sender the row is %s, want %s — the shape resolver must never manufacture a DROPPED row",
			got, Undecidable)
	}
}

// TestFindingsAreOrderedByWhenTheyLanded — the report exists so a backlog can be
// RECOVERED, and recovery starts with the oldest.
func TestFindingsAreOrderedByWhenTheyLanded(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	for _, tc := range []struct{ id, ts string }{
		{"mg-7777", "2026-08-03T10:00:00Z"},
		{"mg-6666", "2026-08-01T10:00:00Z"},
		{"mg-8888", "2026-08-02T10:00:00Z"},
	} {
		f.item(tc.id, "filer-a", "dropped", "done")
		f.sidecar(tc.id, "done", "polecat-w"+tc.id[3:6])
		f.land(tc.id, tc.ts, "done")
	}
	rep := f.scan(Options{Filer: "filer-a"})
	var got []string
	for _, row := range rep.DroppedRows() {
		got = append(got, row.ID)
	}
	want := []string{"mg-6666", "mg-8888", "mg-7777"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dropped rows are ordered %v, want %v (oldest landing first)", got, want)
	}
}

// TestSinceBoundsThePopulation covers the --since equivalent.
func TestSinceBoundsThePopulation(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-9991", "filer-a", "old", "done")
	f.sidecar("mg-9991", "done", "polecat-w91")
	f.land("mg-9991", "2026-08-01T10:00:00Z", "done")
	f.item("mg-9992", "filer-a", "new", "done")
	f.sidecar("mg-9992", "done", "polecat-w92")
	f.land("mg-9992", "2026-08-05T10:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a", Since: "2026-08-03"})
	if rep.Scanned != 1 || statusIn(t, rep, "mg-9992") != Dropped {
		t.Errorf("--since 2026-08-03 scanned %d row(s) %v, want only mg-9992", rep.Scanned, rep.Rows)
	}
}

// TestAScanWithNoFilerCoversEveryFiler — the detector is fleet-wide by
// construction, which is most of the argument for it living in pogo at all.
func TestAScanWithNoFilerCoversEveryFiler(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.emptyBox("filer-b")
	for i, filer := range []string{"filer-a", "filer-b"} {
		id := fmt.Sprintf("mg-a0%d0", i)
		f.item(id, filer, "dropped", "done")
		f.sidecar(id, "done", "polecat-w"+id[3:])
		f.land(id, fmt.Sprintf("2026-08-0%dT10:00:00Z", i+1), "done")
	}
	rep := f.scan(Options{})
	if rep.Dropped != 2 {
		t.Errorf("an unfiltered scan found %d drop(s) across two filers, want 2", rep.Dropped)
	}
}

// ---------------------------------------------------------------- blindness

// TestALostLandingHalfIsInstrumentFailureNotACleanFleet is the central one. Lose
// events.jsonl and every item reads as never landed, so a naive detector reports
// "0 dropped" and exits 0 — a green census while every verdict in the fleet goes
// missing.
func TestALostLandingHalfIsInstrumentFailureNotACleanFleet(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-b111", "filer-a", "dropped", "done")
	f.sidecar("mg-b111", "done", "polecat-wb1")
	f.land("mg-b111", "2026-08-01T10:00:00Z", "done")

	if rep := f.scan(Options{Filer: "filer-a"}); rep.Dropped != 1 {
		t.Fatalf("control arm: the store must hold one drop before events.jsonl is removed, got %d", rep.Dropped)
	}
	if err := os.Remove(filepath.Join(f.root, "events.jsonl")); err != nil {
		t.Fatal(err)
	}

	rep := f.scan(Options{Filer: "filer-a"})
	if !rep.InstrumentFailure() {
		t.Fatalf("with the landing half gone the scan reports %d dropped and is NOT blind; "+
			"that is a green census over a store it could not read", rep.Dropped)
	}
	if rep.Actionable() {
		t.Error("a blind run reported findings; it must report nothing but its own blindness")
	}
	if out := rep.Render(false, false); !strings.HasPrefix(out, "INSTRUMENT FAILURE") {
		t.Errorf("the rendered blind report does not LEAD with the banner:\n%s", out)
	}
}

// TestALostDeliveryHalfIsInstrumentFailure — the other half. Without the mail
// tree every landed item reads as dropped, which is loud rather than quiet, but
// it is still not a measurement.
func TestALostDeliveryHalfIsInstrumentFailure(t *testing.T) {
	f := newFixture(t)
	f.item("mg-b222", "filer-a", "dropped", "done")
	f.sidecar("mg-b222", "done", "polecat-wb2")
	f.land("mg-b222", "2026-08-01T10:00:00Z", "done")
	if err := os.RemoveAll(filepath.Join(f.root, "mail")); err != nil {
		t.Fatal(err)
	}

	rep := f.scan(Options{Filer: "filer-a"})
	if !rep.InstrumentFailure() {
		t.Fatal("with no mail tree the scan is not blind; every landed item would read as dropped")
	}
}

// TestAnUnresolvableStoreIsInstrumentFailure covers the root itself.
func TestAnUnresolvableStoreIsInstrumentFailure(t *testing.T) {
	rep, err := Scan(Options{Root: filepath.Join(t.TempDir(), "no-such-store")})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !rep.InstrumentFailure() {
		t.Fatal("a store that does not exist produced a non-blind report")
	}

	// And with nothing to resolve at all — MG_ROOT cleared under a test binary.
	t.Setenv("MG_ROOT", "")
	rep, err = Scan(Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !rep.InstrumentFailure() {
		t.Fatal("a scan with no resolvable root produced a non-blind report")
	}
}

// TestAnUnscopedEmptyPopulationIsBlindAndAScopedOneIsNot is the line between
// "I could not find the population" and "you asked about a filer with no landed
// items". Both render 0 rows; only one of them is a result.
func TestAnUnscopedEmptyPopulationIsBlindAndAScopedOneIsNot(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-c111", "filer-a", "landed", "done")
	f.sidecar("mg-c111", "done", "polecat-wc1")
	f.land("mg-c111", "2026-08-01T10:00:00Z", "done")
	f.mail("filer-a", "cur", "wc1", "mg-c111 VERDICT", "2026-08-01T09:00:00Z")

	// Scoped to a filer who never filed: an answer to the question asked.
	scoped := f.scan(Options{Filer: "filer-who-never-filed"})
	if scoped.InstrumentFailure() {
		t.Errorf("a scan scoped to a filer with no landed items reports itself blind: %v; "+
			"the operator asked a narrow question and got its answer", scoped.Blind)
	}
	if !strings.Contains(scoped.Render(false, false), "JUDGED NOTHING") {
		t.Error("a scoped scan that matched nothing renders identically to a clean one")
	}

	// Unscoped over a store whose landings are gone: not an answer.
	empty := newFixture(t)
	unscoped, err := Scan(Options{Root: empty.root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !unscoped.InstrumentFailure() {
		t.Fatal("an unscoped scan that judged 0 items is not blind; the population is missing, not clean")
	}
}

// TestOneWorkItemIsJudgedOnceEvenWithTwoCopiesOnDisk.
//
// Not hypothetical: the live store holds SIXTEEN ids archived under two months
// each, so the same item is on disk twice. Counting both reports one dropped
// verdict as two and sends its reader chasing an item they have already looked
// at. This was caught by running the port and the original side by side against
// that store — 1575 items against 1564 — and it is pinned here so a later change
// to the walk cannot reintroduce it silently.
func TestOneWorkItemIsJudgedOnceEvenWithTwoCopiesOnDisk(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	if err := os.MkdirAll(filepath.Join(f.root, "work", "archive", "2026-03"), 0o755); err != nil {
		t.Fatal(err)
	}
	march := filepath.Join("archive", "2026-03")
	august := filepath.Join("archive", "2026-08")
	f.item("mg-e111", "filer-a", "archived under two months", march)
	f.item("mg-e111", "filer-a", "archived under two months", august)
	// Only ONE copy carries the result sidecar; the declared preference is that
	// the copy naming a worker wins, so this row must resolve rather than land
	// in UNDECIDABLE.
	f.sidecar("mg-e111", august, "polecat-we1")
	f.land("mg-e111", "2026-08-01T10:00:00Z", "archived")

	rep := f.scan(Options{Filer: "filer-a"})
	if rep.Scanned != 1 {
		t.Errorf("Scanned = %d for one work item with two copies on disk, want 1", rep.Scanned)
	}
	if rep.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1; one item must produce one finding", rep.Dropped)
	}
	if rep.CollapsedCopies != 1 {
		t.Errorf("CollapsedCopies = %d, want 1; a population that quietly shrinks is the class of "+
			"thing this detector exists to catch", rep.CollapsedCopies)
	}
	if rep.Rows[0].Resolver != "sidecar" || rep.Rows[0].Worker != "we1" {
		t.Errorf("the surviving copy resolved worker %q via %q, want we1 via sidecar — the copy naming "+
			"a worker must win", rep.Rows[0].Worker, rep.Rows[0].Resolver)
	}
	if !strings.Contains(rep.Render(false, false), "collapsed") {
		t.Error("the rendered report does not disclose that copies were collapsed")
	}
}

// TestReportIsStableAcrossRuns — a report that reshuffles looks like it changed,
// and a human watching for change learns to stop reading it.
func TestReportIsStableAcrossRuns(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("mg-d1%d0", i)
		f.item(id, "filer-a", "dropped", "done")
		f.sidecar(id, "done", "polecat-w"+id[3:])
		f.land(id, "2026-08-01T10:00:00Z", "done") // identical stamps: ties broken by id
	}
	first := f.scan(Options{Filer: "filer-a"}).Render(true, false)
	second := f.scan(Options{Filer: "filer-a"}).Render(true, false)
	if first != second {
		t.Errorf("two scans of an unchanged store rendered differently:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
