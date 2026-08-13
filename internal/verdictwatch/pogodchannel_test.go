package verdictwatch

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/filernotify"
)

// THIS FILE IS THE COUPLING, AND THE COUPLING IS THE POINT (mg-4e02).
//
// The pogod channel is recognised by a SENDER plus a subject shape. A shape is a
// copy of somebody else's format, and a copy is exactly what went wrong here in
// the first place: the detector held a copy of "how a verdict arrives" that was
// correct when it was written and silently false eight days later. A fixture
// written by hand in this package can be wrong about filernotify's output in
// precisely that way, and every hermetic test would still pass.
//
// So these tests drive the REAL internal/filernotify Notifier, capture the mail it
// produces, and put THAT into the mailbox this detector reads. If the notification's
// sender or subject moves, this fails by name — in the package that would go blind
// — rather than being discovered by somebody wondering why the DROPPED count is
// climbing while the fleet gets healthier.

// notifierFor builds a real Notifier whose mail is captured rather than sent.
func notifierFor(creator, title string, live bool) (*filernotify.Notifier, *struct{ To, From, Subject, Body string }) {
	got := &struct{ To, From, Subject, Body string }{}
	mail := func(to, from, subject, body string) error {
		got.To, got.From, got.Subject, got.Body = to, from, subject, body
		return nil
	}
	filing := func(id string) (string, string, error) { return creator, title, nil }
	result := func(id string) (string, error) { return `{"verdict":{"verdict":"partial"}}`, nil }
	known := func(name string) bool { return live || name == DefaultCoordinator }
	return filernotify.New(DefaultCoordinator, mail, filing, result, known), got
}

// TestTheRealCreatorNotifyIsRECOGNISEDAsADelivery — the mail filernotify actually
// emits, in the mailbox this detector actually reads, must score DELIVERED via the
// pogod channel. This is mg-af0c: a filer that had received the complete result and
// was reported as never told.
func TestTheRealCreatorNotifyIsRecognisedAsADelivery(t *testing.T) {
	const (
		filer = "pm-pogo"
		id    = "mg-af0c"
		title = "the doctor's refinery-history advice names its window"
	)
	n, got := notifierFor(filer, title, true)
	out := n.Notify(filernotify.Completion{
		ItemID: id, Route: filernotify.RouteMerge, Worker: "paf0c",
		Branch: "polecat-paf0c", MergedSHA: "d27ecc1", Closed: true,
	})
	if !out.Sent() {
		t.Fatalf("the notifier sent nothing (skipped: %q, err: %v); this test asserts on the mail it emits",
			out.Skipped, out.Err)
	}
	if out.To != filer {
		t.Fatalf("the notice went to %q, want the filer %q", out.To, filer)
	}
	// The sender is the fact the old predicate could not express, so it is asserted
	// on its own: right transport, right mailbox, right item, DIFFERENT SENDER.
	if got.From != pogodSender {
		t.Fatalf("filernotify sends as %q and this package matches %q; the channel would be invisible "+
			"again, which is the whole defect", got.From, pogodSender)
	}

	f := newFixture(t)
	f.item(id, filer, title, "done")
	f.sidecar(id, "done", "polecat-paf0c")
	f.land(id, "2026-08-13T02:44:18Z", "done")
	// The captured mail, verbatim — not a hand-written approximation of it.
	f.mail(filer, "new", got.From, got.Subject, "2026-08-13T02:44:20Z")

	row := rowIn(t, f.scan(Options{Filer: filer}), id)
	if row.Status != Delivered || row.DeliveredBy != ChannelPogod {
		t.Fatalf("the real Creator-notify (%q) scored %s via %q, want %s via %q",
			got.Subject, row.Status, row.DeliveredBy, Delivered, ChannelPogod)
	}
}

// TestTheRealRedirectedNoticeMakesTheRowREDIRECTEDNotUntold — filernotify's other
// live shape. When the filer is not a live agent the notice goes to the
// coordinator and names who it was for; that is the fleet discharging the
// obligation as far as it can, and it is still not the filer being told.
func TestTheRealRedirectedNoticeMakesTheRowRedirectedNotUntold(t *testing.T) {
	const (
		filer = "pf32a"
		id    = "mg-9c6b"
		title = "the indexer re-errors forever on a REGULAR file it cannot read"
	)
	n, got := notifierFor(filer, title, false)
	out := n.Notify(filernotify.Completion{
		ItemID: id, Route: filernotify.RouteMerge, Worker: "p9c6b",
		Branch: "polecat-p9c6b", MergedSHA: "082ec38", Closed: true,
	})
	if !out.Sent() || !out.Redirected {
		t.Fatalf("the notifier did not redirect (to %q, redirected %v, skipped %q); this test asserts on "+
			"the redirect shape", out.To, out.Redirected, out.Skipped)
	}
	if out.To != DefaultCoordinator {
		t.Fatalf("the redirect went to %q, want the coordinator %q", out.To, DefaultCoordinator)
	}

	f := newFixture(t)
	f.item(id, filer, title, "done")
	f.sidecar(id, "done", "polecat-p9c6b")
	f.land(id, "2026-08-13T00:30:38Z", "done")
	f.emptyBox(filer)
	f.mail(DefaultCoordinator, "new", got.From, got.Subject, "2026-08-13T00:30:40Z")

	row := rowIn(t, f.scan(Options{}), id)
	if row.Status != Dropped {
		t.Errorf("a notice redirected away from the filer scored %s for that filer, want %s — the filer "+
			"was not told", row.Status, Dropped)
	}
	if row.Reach != ReachRedirected || row.RedirectedTo != DefaultCoordinator {
		t.Errorf("the row's reach is %q -> %q, want %q -> %q; parsed out of filernotify's real subject %q",
			row.Reach, row.RedirectedTo, ReachRedirected, DefaultCoordinator, got.Subject)
	}
}

// TestBothOfFilernotifysHeadlinesParse. filernotify has two headlines — COMPLETED
// and MERGED BUT NOT CLOSED (mg-2b71 added the second, for a branch that merged
// while its item stayed open). Both are notices about this item to this filer, so
// both are deliveries; a matcher that knew only the first would go blind on the
// half of the traffic that reports the more alarming state.
func TestBothOfFilernotifysHeadlinesParse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closed bool
	}{
		{"the item closed", true},
		{"the branch merged and the item stayed open", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, got := notifierFor("pm-pogo", "a title", true)
			out := n.Notify(filernotify.Completion{
				ItemID: "mg-2b71", Route: filernotify.RouteMerge, Worker: "p2b71",
				Branch: "polecat-p2b71", Closed: tc.closed,
				NotClosedReason: "not claimed",
			})
			if !out.Sent() {
				t.Fatalf("nothing sent (skipped %q)", out.Skipped)
			}
			id, forFiler := pogodNoticeSubject(got.Subject)
			if id != "mg-2b71" || forFiler != "" {
				t.Fatalf("this package parses filernotify's subject %q as item %q / for %q, want mg-2b71 / \"\"",
					got.Subject, id, forFiler)
			}
		})
	}
}

// TestTheUndeliverableRelayIsNotReadAsANotice — filernotify's third shape, and the
// one that must NOT count. `UNDELIVERABLE COMPLETED: …` in the coordinator's box
// reports a notification that FAILED; reading it as a delivery would turn the
// notifier's own error report into evidence that it worked.
func TestTheUndeliverableRelayIsNotReadAsANotice(t *testing.T) {
	subject := "UNDELIVERABLE COMPLETED: mg-2b71 — a title"
	if id, _ := pogodNoticeSubject(subject); id != "" {
		t.Errorf("pogodNoticeSubject(%q) = %q, want no match: that message reports a notification that "+
			"failed to land", subject, id)
	}
	// And the shape is really filernotify's — asserted against the constant that
	// builds it rather than against this test's memory of it.
	if !strings.HasPrefix(subject, "UNDELIVERABLE ") {
		t.Fatal("the fixture subject no longer carries the prefix this test is about")
	}
}
