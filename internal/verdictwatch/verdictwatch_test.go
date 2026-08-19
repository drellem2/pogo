package verdictwatch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// n numbers the messages this fixture delivers. Deriving the filename from
	// the subject instead — as this helper first did — silently OVERWRITES one
	// message with another whenever two subjects are the same length, and a test
	// whose second message vanished would pass for the wrong reason.
	n int
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

// sidecarWithVerdict writes the same sidecar carrying a recorded verdict — the
// difference between a ROUTING row (the outcome exists and can be handed over)
// and a LOST one.
func (f *fixture) sidecarWithVerdict(id, statusDir, branch, word, summary string) {
	f.t.Helper()
	f.sidecarRaw(id, statusDir, fmt.Sprintf(
		`{"branch":%q,"completed_by":"refinery","verdict":{"verdict":%q,"summary":%q}}`,
		branch, word, summary))
}

// sidecarRaw writes an arbitrary sidecar, for the shapes the live store actually
// holds rather than only the one this package would prefer.
func (f *fixture) sidecarRaw(id, statusDir, body string) {
	f.t.Helper()
	path := filepath.Join(f.root, "work", statusDir, id+".result.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatalf("write sidecar: %v", err)
	}
}

// notify delivers pogod's Creator-notification (mg-f120) for an item, in the
// subject shape internal/filernotify emits. The coupling to that package's REAL
// output is pinned separately, in pogodchannel_test.go: a hand-written fixture is
// free to be wrong about a format in exactly the way this detector was.
func (f *fixture) notify(box, id, title string) {
	f.t.Helper()
	f.mail(box, "new", "pogod", fmt.Sprintf("COMPLETED: %s — %s", id, title), "2026-08-01T12:00:00Z")
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
	f.n++
	name := fmt.Sprintf("%04d.%s.msg", f.n, state)
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

// ---------------------------------------------------------------- the channel set (mg-4e02)

func rowIn(t *testing.T, rep Report, id string) Row {
	t.Helper()
	for _, row := range rep.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row for %s in a report of %d row(s)", id, len(rep.Rows))
	return Row{}
}

// TestPogodsCreatorNotifyIsADeliveryAndTheChannelIsNamed is the headline defect.
//
// mg-f120 added a completion notice sent by POGOD. It is macguffin mail, in the
// filer's own mailbox, about the right item — and the predicate named the WORKER
// as the sender, so every item that backstop covered scored DROPPED from
// 2026-08-13T02:01:35Z onward. Two mechanisms built weeks apart against the same
// gap: the fix for verdict delivery was measured by an instrument unable to
// register it working, and its DROPPED count would have CLIMBED as the fix took
// effect.
func TestPogodsCreatorNotifyIsADeliveryAndTheChannelIsNamed(t *testing.T) {
	f := newFixture(t)
	f.item("mg-f120", "pm-pogo", "the worker mailed nobody and pogod told the filer", "done")
	f.sidecar("mg-f120", "done", "polecat-wp1")
	f.land("mg-f120", "2026-08-13T02:44:18Z", "done")
	f.notify("pm-pogo", "mg-f120", "the worker mailed nobody and pogod told the filer")

	rep := f.scan(Options{Filer: "pm-pogo"})
	row := rowIn(t, rep, "mg-f120")
	if row.Status != Delivered {
		t.Fatalf("an item pogod's Creator-notify covered is %s, want %s — this is the false positive "+
			"the whole ticket is about", row.Status, Delivered)
	}
	if row.DeliveredBy != ChannelPogod {
		t.Errorf("DeliveredBy = %q, want %q; `a polecat did its job` and `a backstop caught it` are "+
			"different facts about fleet health and must not collapse", row.DeliveredBy, ChannelPogod)
	}
	if rep.Dropped != 0 {
		t.Errorf("Dropped = %d over one notified item, want 0", rep.Dropped)
	}
	// And the per-channel counts, which is what makes DELIVERED an answer rather
	// than a number.
	for _, ch := range rep.Channels {
		want := 0
		if ch.Channel == ChannelPogod {
			want = 1
		}
		if ch.Delivered != want {
			t.Errorf("channel %s reports %d delivered, want %d", ch.Channel, ch.Delivered, want)
		}
	}
	if out := rep.Render(true, false); !strings.Contains(out, string(ChannelPogod)) {
		t.Errorf("the rendered report does not name the channel that carried it:\n%s", out)
	}
}

// TestTheWorkerChannelWinsWhenBothCarriedIt — a row both channels covered reports
// that a POLECAT did its job. Reporting the backstop over the primary would hide
// a working fleet.
func TestTheWorkerChannelWinsWhenBothCarriedIt(t *testing.T) {
	f := newFixture(t)
	f.item("mg-b0b0", "pm-pogo", "both channels carried it", "done")
	f.sidecar("mg-b0b0", "done", "polecat-wb0")
	f.land("mg-b0b0", "2026-08-13T02:44:18Z", "done")
	f.mail("pm-pogo", "cur", "wb0", "mg-b0b0 VERDICT", "2026-08-13T02:40:00Z")
	f.notify("pm-pogo", "mg-b0b0", "both channels carried it")

	row := rowIn(t, f.scan(Options{Filer: "pm-pogo"}), "mg-b0b0")
	if row.DeliveredBy != ChannelWorker {
		t.Errorf("DeliveredBy = %q for a row both channels covered, want %q", row.DeliveredBy, ChannelWorker)
	}
}

// TestARelayIsNotADelivery is the widening this must NOT take, pinned negatively.
//
// "Any mail mentioning the item id" would count mg-6ff4's case — its filer was
// told by a mayor relay at 01:22Z — and a relayed headline is not a verdict. The
// distinction worth preserving is WHO DISCHARGED THE OBLIGATION, not whether the
// filer happened to hear something. Each sub-case below is mail in the right
// mailbox, naming the right item, that is still not a verdict.
func TestARelayIsNotADelivery(t *testing.T) {
	for _, tc := range []struct {
		name, id, from, subject string
	}{
		{
			name: "the coordinator relays a headline",
			id:   "mg-6ff4", from: "mayor",
			subject: "FYI: mg-6ff4 is done — passing on what I heard",
		},
		{
			name: "pogod mentions the item in something that is not a completion notice",
			id:   "mg-6ff5", from: "pogod",
			subject: "stall-watch: mg-6ff5 has been claimed for 4h with no driver",
		},
		{
			name: "pogod's completion notice is about a DIFFERENT item",
			id:   "mg-6ff6", from: "pogod",
			subject: "COMPLETED: mg-9999 — some other item entirely",
		},
		{
			name: "pogod reports its own notification as UNDELIVERABLE",
			id:   "mg-6ff7", from: "pogod",
			subject: "UNDELIVERABLE COMPLETED: mg-6ff7 — the notice never landed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.item(tc.id, "filer-a", "relayed, not delivered", "done")
			f.sidecar(tc.id, "done", "polecat-wr1")
			f.land(tc.id, "2026-08-13T02:44:18Z", "done")
			f.mail("filer-a", "new", tc.from, tc.subject, "2026-08-13T02:45:00Z")

			row := rowIn(t, f.scan(Options{Filer: "filer-a"}), tc.id)
			if row.Status != Dropped {
				t.Errorf("%q scored %s, want %s — a predicate that cannot tell a verdict from a MENTION "+
					"of one counts talk about the thing alongside the thing", tc.subject, row.Status, Dropped)
			}
		})
	}
}

// TestATruePositiveIsNotSilencedByTheNewChannel is the positive control the
// acceptance criterion names, and it is the whole risk of this change: a fix that
// stops false positives by stopping the detector is worse than the defect.
//
// The store here is the awkward one — pogod IS notifying, in this very mailbox,
// about other items — so a predicate that had gone slack on the sender or on the
// item id would pass everything.
func TestATruePositiveIsNotSilencedByTheNewChannel(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"mg-c001", "mg-c002"} {
		f.item(id, "filer-a", "pogod covered this one", "done")
		f.sidecar(id, "done", "polecat-w"+id[3:])
		f.land(id, "2026-08-13T02:10:00Z", "done")
		f.notify("filer-a", id, "pogod covered this one")
	}
	// The genuine drop: the worker never reported, and NO notice names this item.
	f.item("mg-c003", "filer-a", "nobody reported this one at all", "done")
	f.sidecar("mg-c003", "done", "polecat-wc3")
	f.land("mg-c003", "2026-08-13T02:20:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := statusIn(t, rep, "mg-c003"); got != Dropped {
		t.Fatalf("the item nobody reported on is %s, want %s — the fix must not silence the true positives",
			got, Dropped)
	}
	if rep.Dropped != 1 || rep.Delivered != 2 {
		t.Errorf("counts are %d dropped / %d delivered, want 1 / 2", rep.Dropped, rep.Delivered)
	}
	if !rep.Actionable() {
		t.Error("a report holding a genuine drop is not Actionable()")
	}
}

// TestTheReportEnumeratesTheChannelsItChecked. A finding that quantifies over
// channels is only as honest as its list of them: this detector mis-reported for
// two hours because its claim covered a channel set nobody had told it about, and
// the fix that makes that impossible again is printing the list with the finding.
func TestTheReportEnumeratesTheChannelsItChecked(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-e0e0", "filer-a", "dropped", "done")
	f.sidecar("mg-e0e0", "done", "polecat-we0")
	f.land("mg-e0e0", "2026-08-13T02:44:18Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if len(rep.Channels) != len(Channels()) {
		t.Fatalf("the report enumerates %d channel(s), want %d", len(rep.Channels), len(Channels()))
	}
	out := rep.Render(false, false)
	if !strings.Contains(out, "CHANNELS CHECKED") {
		t.Errorf("the report does not enumerate its channels:\n%s", out)
	}
	for _, ch := range Channels() {
		if !strings.Contains(out, string(ch)) {
			t.Errorf("the report does not name the %s channel:\n%s", ch, out)
		}
		if !strings.Contains(out, ch.Looked()) {
			t.Errorf("the report names %s without saying what was looked at for it:\n%s", ch, out)
		}
	}
}

// TestNoSentenceClaimsAVerdictReachedNobody is §1 of the ticket, asserted against
// the rendered output rather than reviewed by eye.
//
// The near end is "no channel this run checked carried a verdict". The far end is
// "the verdict reached nobody", which quantifies over every channel there is. The
// near end costs nothing to say and is true in both regimes; the far end was false
// within two hours of being written. THIS TEST IS THE ONLY THING THAT MAKES THE
// WORDING A CONTRACT rather than a preference.
func TestNoSentenceClaimsAVerdictReachedNobody(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-a0a0", "filer-a", "dropped", "done")
	f.sidecarWithVerdict("mg-a0a0", "done", "polecat-wa0", "pass", "the outcome exists")
	f.land("mg-a0a0", "2026-08-13T02:44:18Z", "done")
	f.item("mg-a0a1", "filer-a", "delivered", "done")
	f.sidecar("mg-a0a1", "done", "polecat-wa1")
	f.land("mg-a0a1", "2026-08-13T02:45:18Z", "done")
	f.mail("filer-a", "cur", "wa1", "mg-a0a1 VERDICT", "2026-08-13T02:44:00Z")

	rep := f.scan(Options{Filer: "filer-a"})
	out := rep.Render(true, false)
	for _, forbidden := range []string{
		"never received",
		"never told",
		"was never",
		"no verdict reached",
		"did not reach",
		"reached nobody",
		"nobody ever heard",
	} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("the report claims the FAR end (%q). It measures which of its listed channels "+
				"carried a verdict; anything stronger is a claim about a channel set that grew once "+
				"already:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "no channel above carried a verdict to the filer") {
		t.Errorf("the report does not state the near end in words:\n%s", out)
	}
}

// TestTheThreeClassesAreSeparateAndNotOneNumber is ADDITION 3: 4/4/2 across nine
// collapsed into a single DROPPED count, when only the last pair was
// unrecoverable. A row whose verdict is on disk needs handing over; a row with
// nothing recorded is the only actual loss.
func TestTheThreeClassesAreSeparateAndNotOneNumber(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")

	// verdict on disk, delivered -> fine
	f.item("mg-1001", "filer-a", "fine", "done")
	f.sidecarWithVerdict("mg-1001", "done", "polecat-w01", "pass", "delivered and recorded")
	f.land("mg-1001", "2026-08-13T01:00:00Z", "done")
	f.mail("filer-a", "cur", "w01", "mg-1001 VERDICT", "2026-08-13T00:59:00Z")

	// verdict on disk, nothing delivered -> ROUTING, recoverable now
	f.item("mg-1002", "filer-a", "routing failure", "done")
	f.sidecarWithVerdict("mg-1002", "done", "polecat-w02", "partial", "recorded but nobody was sent it")
	f.land("mg-1002", "2026-08-13T02:00:00Z", "done")

	// nothing on disk, nothing delivered -> LOST
	f.item("mg-1003", "filer-a", "the real loss", "done")
	f.sidecar("mg-1003", "done", "polecat-w03")
	f.land("mg-1003", "2026-08-13T03:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if rep.Delivered != 1 || rep.Routing != 1 || rep.Lost != 1 {
		t.Fatalf("classes are %d delivered / %d routing / %d lost, want 1 / 1 / 1",
			rep.Delivered, rep.Routing, rep.Lost)
	}
	// The arithmetic has to close, or the two axes have been added together.
	if rep.Routing+rep.Lost != rep.Dropped {
		t.Errorf("ROUTING %d + LOST %d != DROPPED %d; the recoverability axis must partition the drops exactly",
			rep.Routing, rep.Lost, rep.Dropped)
	}
	if got := rowIn(t, rep, "mg-1002").Class; got != ClassRouting {
		t.Errorf("a dropped row whose sidecar holds the verdict is %s, want %s", got, ClassRouting)
	}
	if got := rowIn(t, rep, "mg-1003").Class; got != ClassLost {
		t.Errorf("a dropped row with nothing recorded is %s, want %s", got, ClassLost)
	}
	out := rep.Render(false, false)
	for _, want := range []string{"ROUTING 1", "LOST 1", string(ClassRouting), string(ClassLost)} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not separate the classes (%q missing):\n%s", want, out)
		}
	}
}

// TestADroppedRowPrintsTheVerdictItsSidecarHolds is ADDITION 2, and it is the
// cheap high-value half: this package resolves the WORKER out of the result
// sidecar, so on a merge-route row it has the verdict open already. Reading one
// field out of a verdict and then reporting DROPPED without the rest of it is
// looking past the answer.
func TestADroppedRowPrintsTheVerdictItsSidecarHolds(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("mayor")
	f.item("mg-687f", "mayor", "ESTABLISH the disposition of two unmerged branches", "done")
	f.sidecarWithVerdict("mg-687f", "done", "polecat-p687f", "pass",
		"Established the disposition of both branches by content, not patch-id.")
	f.land("mg-687f", "2026-08-13T02:44:30Z", "done")

	rep := f.scan(Options{Filer: "mayor"})
	row := rowIn(t, rep, "mg-687f")
	if row.Verdict == nil {
		t.Fatal("the row carries no verdict, though the sidecar this package already opened holds one")
	}
	if row.Verdict.Word != "pass" {
		t.Errorf("Verdict.Word = %q, want pass", row.Verdict.Word)
	}
	if row.Verdict.Sidecar != filepath.Join(f.root, "work", "done", "mg-687f.result.json") {
		t.Errorf("Verdict.Sidecar = %q; it must be the EXPLICIT path this run read, never a glob "+
			"across the lifecycle directories", row.Verdict.Sidecar)
	}
	out := rep.Render(false, false)
	for _, want := range []string{
		"pass",
		"Established the disposition of both branches",
		row.Verdict.Sidecar,
		"the merge route: nothing on it asks the worker to mail anyone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dropped row does not print %q — a row that says nobody was told must also say "+
				"what they should have been told:\n%s", want, out)
		}
	}
}

// TestABareStringVerdictIsStillAVerdict.
//
// The live store holds BOTH shapes. Measured 2026-08-13: of the 140 result
// sidecars under work/done, 121 record a `verdict` key at all — 113 as an object
// (`"verdict":{"verdict":"pass",…}`) and 8 as a bare string. "A sidecar exists"
// (140 of 141) and "a sidecar records a verdict" (121 of 140) are different
// measurements and this test is about the second. This is why
// `jq -r '.verdict.verdict'` is not the repair for `jq -r .result` — on those 8 it
// returns `null` at exit 0, which is the SAME failure one level down: a wrong
// question that reads as a blank answer.
func TestABareStringVerdictIsStillAVerdict(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-1c60", "filer-a", "the sidecar records a bare string verdict", "done")
	f.sidecarRaw("mg-1c60", "done", `{"branch":"polecat-w16","completed_by":"refinery","verdict":"pass"}`)
	f.land("mg-1c60", "2026-08-13T02:00:00Z", "done")

	// And a shape neither reading understands, which is still RECORDED: claiming a
	// loss there would be the report asserting something it did not measure.
	f.item("mg-1c61", "filer-a", "the sidecar records a verdict in an odd shape", "done")
	f.sidecarRaw("mg-1c61", "done", `{"branch":"polecat-w17","verdict":{"kind":"review","rationale":"x"}}`)
	f.land("mg-1c61", "2026-08-13T02:01:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	row := rowIn(t, rep, "mg-1c60")
	if row.Verdict == nil || row.Verdict.Word != "pass" {
		t.Fatalf("a bare-string verdict read as %+v; the eight sidecars shaped like this in the live "+
			"store must not read as no verdict at all", row.Verdict)
	}
	if row.Class != ClassRouting {
		t.Errorf("Class = %s for a row whose verdict is on disk, want %s", row.Class, ClassRouting)
	}
	if odd := rowIn(t, rep, "mg-1c61"); odd.Verdict == nil || odd.Class != ClassRouting {
		t.Errorf("a verdict in an unrecognised shape read as class %s with verdict %+v; it is RECORDED, "+
			"so the row is recoverable from the file", odd.Class, odd.Verdict)
	}
}

// TestAnUnreachableFilerIsSeparatedFromAnUntoldOne. "Nobody told them" and "there
// was nobody to tell" are different findings and they scored identically before:
// g111 had no mailbox at all, and a filer that is an exited polecat still HAS one
// (mg never removes a mailbox), so pogod redirects its notice to the coordinator.
func TestAnUnreachableFilerIsSeparatedFromAnUntoldOne(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("pm-pogo")

	// Reachable and not told.
	f.item("mg-2001", "pm-pogo", "reachable, untold", "done")
	f.sidecar("mg-2001", "done", "polecat-w21")
	f.land("mg-2001", "2026-08-13T02:00:00Z", "done")

	// No mailbox at all: no channel could have reached this filer.
	f.item("mg-2002", "g111", "no mailbox anywhere", "done")
	f.sidecar("mg-2002", "done", "polecat-w22")
	f.land("mg-2002", "2026-08-13T02:01:00Z", "done")

	// An exited polecat: pogod sent the notice to the coordinator and said so.
	f.item("mg-2003", "pf32a", "filer is an exited polecat", "done")
	f.sidecar("mg-2003", "done", "polecat-w23")
	f.land("mg-2003", "2026-08-13T02:02:00Z", "done")
	f.emptyBox("pf32a")
	f.mail("mayor", "new", "pogod",
		"COMPLETED: mg-2003 (filed by pf32a, who is gone) — filer is an exited polecat",
		"2026-08-13T02:03:00Z")

	rep := f.scan(Options{})
	if got := rowIn(t, rep, "mg-2001").Reach; got != ReachMailbox {
		t.Errorf("a reachable filer's row has reach %q, want %q", got, ReachMailbox)
	}
	if got := rowIn(t, rep, "mg-2002").Reach; got != ReachNoMailbox {
		t.Errorf("a filer with no mailbox has reach %q, want %q", got, ReachNoMailbox)
	}
	redirected := rowIn(t, rep, "mg-2003")
	if redirected.Reach != ReachRedirected || redirected.RedirectedTo != DefaultCoordinator {
		t.Errorf("an exited polecat's row has reach %q -> %q, want %q -> %q",
			redirected.Reach, redirected.RedirectedTo, ReachRedirected, DefaultCoordinator)
	}
	// A notice REDIRECTED to the coordinator is not a delivery to the filer: the
	// obligation was discharged as far as it could be, and the filer still was not
	// told. Both facts, separately.
	if redirected.Status != Dropped {
		t.Errorf("a redirected notice scored %s for the filer it was ABOUT, want %s",
			redirected.Status, Dropped)
	}
	if rep.Dropped != 3 || rep.Unreachable != 2 {
		t.Errorf("counts are %d dropped / %d unreachable, want 3 / 2", rep.Dropped, rep.Unreachable)
	}
}

// TestTheRedirectedNoticeIsADeliveryToTheCOORDINATORsOwnItems — the same message
// shape must not become a delivery for whoever's mailbox it happens to sit in.
func TestTheRedirectedNoticeIsNotADeliveryToWhoeverHoldsIt(t *testing.T) {
	f := newFixture(t)
	f.item("mg-3001", "mayor", "mayor filed this one", "done")
	f.sidecar("mg-3001", "done", "polecat-w31")
	f.land("mg-3001", "2026-08-13T02:00:00Z", "done")
	// The notice in mayor's box says it was for somebody else.
	f.mail("mayor", "new", "pogod",
		"COMPLETED: mg-3001 (filed by pf32a, who is gone) — mayor filed this one",
		"2026-08-13T02:03:00Z")

	if got := statusIn(t, f.scan(Options{Filer: "mayor"}), "mg-3001"); got != Dropped {
		t.Errorf("a notice addressed to somebody else scored %s in mayor's box, want %s", got, Dropped)
	}
}

// TestAnUndecidableRowPogodCoveredIsADelivery. The worker channel needs the
// worker's identity; the pogod channel does not, because the notice names the
// ITEM. An UNDECIDABLE row that pogod actually covered is a delivery, not a blind
// spot, and reporting the blind spot would be this detector understating what it
// can see.
func TestAnUndecidableRowPogodCoveredIsADelivery(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-4001", "filer-a", "no sidecar names a worker", "done")
	f.land("mg-4001", "2026-08-13T02:00:00Z", "done")
	f.notify("filer-a", "mg-4001", "no sidecar names a worker")

	rep := f.scan(Options{Filer: "filer-a"})
	row := rowIn(t, rep, "mg-4001")
	if row.Status != Delivered || row.DeliveredBy != ChannelPogod {
		t.Errorf("an unresolvable-worker row pogod notified is %s via %q, want %s via %q",
			row.Status, row.DeliveredBy, Delivered, ChannelPogod)
	}
	if rep.Undecidable != 0 {
		t.Errorf("Undecidable = %d, want 0; the notice names the item, so the worker's identity is "+
			"not needed to see the delivery", rep.Undecidable)
	}
}

// TestAnUndecidableRowIsNotLabelledLOST. The label a row prints must be the class
// it was PUT IN, not one inferred a second time at render. An UNDECIDABLE row with
// nothing on disk is a row this detector could not reach; calling it LOST would
// assert a loss it never measured — the same substitution of one statement for a
// neighbouring one that this whole change is about.
func TestAnUndecidableRowIsNotLabelledLost(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-6001", "filer-a", "no sidecar names a worker and nothing was recorded", "done")
	f.land("mg-6001", "2026-08-13T02:00:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if got := rowIn(t, rep, "mg-6001").Class; got != ClassUndecidable {
		t.Fatalf("Class = %s, want %s", got, ClassUndecidable)
	}
	if rep.Lost != 0 {
		t.Errorf("Lost = %d over a store whose only row is UNDECIDABLE, want 0; the recoverability axis "+
			"partitions the DROPS", rep.Lost)
	}
	out := rep.Render(true, false)
	if strings.Contains(out, "LOST — no verdict recorded") {
		t.Errorf("an UNDECIDABLE row is labelled LOST in the output:\n%s", out)
	}
}

// TestTheEmittedRetrievalInstructionReturnsTheVerdict.
//
// THE ACCEPTANCE CRITERION THIS PINS IS NOT COSMETIC. doctor told three agents to
// run `mg show <id> --json | jq -r .result`; there is no `result` key on that
// object at all, so it printed `null` and exited 0 — which reads as "the verdict
// is blank" rather than "there is nowhere on this object for it to be". That
// negative then travelled as a colleague's evidence and became a mechanism in a
// ticket. A retrieval instruction this tool emits is a CLAIM THAT THE ARTIFACT IS
// THERE, so every instruction the report prints is EXECUTED here against an item
// known to have a verdict, and a recipe returning `null` at exit 0 fails.
func TestTheEmittedRetrievalInstructionReturnsTheVerdict(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-5001", "filer-a", "an item that definitely HAS a verdict", "done")
	f.sidecarWithVerdict("mg-5001", "done", "polecat-w51", "partial", "the outcome, recorded in full")
	f.land("mg-5001", "2026-08-13T02:00:00Z", "done")
	// A second one in the shape the ticket's own proposed recipe would have failed
	// on, so the executed check covers both.
	f.item("mg-5002", "filer-a", "a bare-string verdict", "done")
	f.sidecarRaw("mg-5002", "done", `{"branch":"polecat-w52","verdict":"pass"}`)
	f.land("mg-5002", "2026-08-13T02:01:00Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	out := rep.Render(true, false)

	instructions := emittedCommands(out)
	if len(instructions) != 2 {
		t.Fatalf("the report emitted %d retrieval instruction(s) for two rows with verdicts, want 2:\n%s",
			len(instructions), out)
	}
	// The forbidden recipe, by name: it is the one that returns null at exit 0.
	for _, bad := range []string{"jq -r .result", `jq -r '.result'`, "mg show"} {
		if strings.Contains(out, bad) {
			t.Errorf("the report emits %q, which cannot satisfy the read it promises:\n%s", bad, out)
		}
	}

	jqPath, err := exec.LookPath("jq")
	for i, cmdline := range instructions {
		// Executed as a reader would: the printed command, verbatim, through a
		// shell. Skipped only for want of jq — never asserted around.
		if err != nil {
			t.Skipf("jq is not on PATH; the emitted instruction %q cannot be executed here", cmdline)
		}
		_ = jqPath
		got, runErr := exec.Command("sh", "-c", cmdline).CombinedOutput()
		text := strings.TrimSpace(string(got))
		if runErr != nil {
			t.Errorf("instruction %d (%s) failed: %v\n%s", i, cmdline, runErr, text)
			continue
		}
		if text == "" || text == "null" {
			t.Errorf("instruction %d (%s) returned %q against an item known to have a verdict. "+
				"A recipe that prints null at exit 0 is the defect this criterion exists for.",
				i, cmdline, text)
		}
	}
	// And independent of any shell: the path the report printed is a file this
	// process can read, whose verdict is the one the report claimed.
	for _, row := range rep.DroppedRows() {
		if row.Verdict == nil {
			continue
		}
		data, rerr := os.ReadFile(row.Verdict.Sidecar)
		if rerr != nil {
			t.Errorf("the report printed a path it cannot read: %s (%v)", row.Verdict.Sidecar, rerr)
			continue
		}
		if !strings.Contains(string(data), row.Verdict.Word) {
			t.Errorf("the sidecar at %s does not contain the verdict %q the report attributed to it",
				row.Verdict.Sidecar, row.Verdict.Word)
		}
	}
}

// emittedCommands pulls every shell command the report tells its reader to run.
var emittedCommand = regexp.MustCompile(`(?m)^\s*read it in full:\s+(jq .*)$`)

func emittedCommands(out string) []string {
	var cmds []string
	for _, m := range emittedCommand.FindAllStringSubmatch(out, -1) {
		cmds = append(cmds, strings.TrimSpace(m[1]))
	}
	return cmds
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

// mg-c456. Every row in this report is a RECONSTRUCTION, and that is why 1014 LOST
// rows could grow with nobody owning them: at the moment of loss the system's own
// answer is success, so nothing looked. pogod now records one class of it as it
// happens, and the LOST block names that counter.
//
// It also has to name TWO limits, and the first one is a correction of this block's
// own first draft. The at-loss-time counter is NOT a better source for the accrual
// rate — `landed` already gives one, and that column is exactly where the census
// behind this ticket got its per-day table. Offering the event as the way to get a
// rate would have been a false claim about this report in this report. The second
// limit is coverage: the counter sees only the closes pogod itself performs, and
// pointing a reader at a narrower instrument without saying it is narrower is how
// one of these gets read as a total.
func TestTheLostBlockNamesTheAtLossTimeCounterAndItsLimit(t *testing.T) {
	f := newFixture(t)
	f.emptyBox("filer-a")
	f.item("mg-e0e0", "filer-a", "dropped with nothing recorded", "done")
	f.sidecar("mg-e0e0", "done", "polecat-we0")
	f.land("mg-e0e0", "2026-08-13T02:44:18Z", "done")

	rep := f.scan(Options{Filer: "filer-a"})
	if rep.Lost == 0 {
		t.Fatalf("fixture produced no LOST row, so the block under test never renders: %+v", rep)
	}
	out := rep.Render(false, false)

	// The command, verbatim and runnable. A pointer a reader has to reconstruct
	// from prose is the same as no pointer.
	const cmd = "pogo events list --since=24h --type=work_item_closed_without_verdict"
	if !strings.Contains(out, cmd) {
		t.Errorf("the LOST block does not name the at-loss-time counter as a runnable command:\n%s", out)
	}
	// AND it must hedge the empty reading. `pogo events list --type=X` returns
	// zero rows just as cleanly on a pogod that predates the event as on a clean
	// fleet — the running daemon was 1ebf2dc when this shipped, so on day one the
	// empty result was guaranteed. Advertising a counter without that caveat hands
	// the reader a constant and calls it a measurement, which is the shape of the
	// defect this whole report sits next to.
	if !strings.Contains(out, "ZERO ROWS FROM THAT IS NOT ZERO LOSSES") {
		t.Errorf("the LOST block recommends a counter without hedging its empty reading:\n%s", out)
	}
	if !strings.Contains(out, "/version") {
		t.Errorf("the hedge must name how to tell the two cases apart, not merely that they differ:\n%s", out)
	}
	// It must NOT sell the counter as the way to get an accrual rate. `landed`
	// above already gives one — a report that understates its own column to
	// promote another instrument is a false claim about itself.
	if !strings.Contains(out, "`landed` above already gives a rate") {
		t.Errorf("the LOST block must not imply this report cannot give an accrual rate:\n%s", out)
	}
	// And the coverage limit, on the same block. The counter sees only the closes
	// pogod itself performs.
	if !strings.Contains(out, "does NOT cover every LOST row") {
		t.Errorf("the LOST block points at a narrower instrument without saying it is narrower:\n%s", out)
	}

	// A clean report must not carry either — an all-clear that recommends a
	// counter has invented work.
	clean := newFixture(t)
	clean.emptyBox("filer-b")
	cleanRep := clean.scan(Options{Filer: "filer-b"})
	if cleanRep.Lost != 0 {
		t.Fatalf("the clean fixture is not clean: %+v", cleanRep)
	}
	if strings.Contains(cleanRep.Render(false, false), cmd) {
		t.Errorf("a report with no LOST rows must not recommend the counter:\n%s", cleanRep.Render(false, false))
	}
}
