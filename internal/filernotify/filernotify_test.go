package filernotify

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// recorder captures every mail a Notifier writes.
type recorder struct {
	mu   sync.Mutex
	sent []mailed
	err  error
}

type mailed struct{ to, from, subject, body string }

func (r *recorder) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, mailed{to, from, subject, body})
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recorder) last() mailed {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		return mailed{}
	}
	return r.sent[len(r.sent)-1]
}

// filing returns a fixed creator/title for every id.
func filing(creator, title string) func(string) (string, string, error) {
	return func(string) (string, string, error) { return creator, title, nil }
}

func resultOf(s string) func(string) (string, error) {
	return func(string) (string, error) { return s, nil }
}

func knownSet(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(n string) bool { return set[n] }
}

// The headline case, and the one that was observed: a PM commissions an item, a
// polecat merges it, and until mg-f120 nothing told the PM.
func TestTheCommissioningAgentIsMailedWhenItsItemMerges(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send,
		filing("pm-onethird", "enumerate the consumers for the cube-foliation energy identity"),
		resultOf(`{"verdict":"pass","summary":"14 consumers enumerated"}`),
		knownSet("pm-onethird", "mayor"))

	o := n.Notify(Completion{Closed: true, ItemID: "mg-145f", Route: RouteMerge, Worker: "p145f",
		Branch: "polecat-p145f", MergedSHA: "abc1234"})

	if !o.Sent() {
		t.Fatalf("expected a mail to the filer, got %+v", o)
	}
	if o.To != "pm-onethird" {
		t.Errorf("mail went to %q, want the item's creator pm-onethird", o.To)
	}
	if o.Redirected {
		t.Errorf("a live crew filer must not be redirected: %+v", o)
	}
	m := rec.last()
	if !strings.Contains(m.subject, "mg-145f") {
		t.Errorf("subject does not name the item: %q", m.subject)
	}
	// The verdict is the payload. A "your item finished" with no result is a
	// notification the reader has to go and follow up, which is the polling this
	// replaces.
	if !strings.Contains(m.body, `"verdict":"pass"`) {
		t.Errorf("body does not carry the result sidecar:\n%s", m.body)
	}
	if !strings.Contains(m.body, "abc1234") {
		t.Errorf("body does not name the merged SHA:\n%s", m.body)
	}
}

// A creator that no longer exists gets its mail redirected rather than filed
// into a box nobody reads. This is the rule the ticket asked for by name.
func TestAFilerThatNoLongerExistsRedirectsToTheCoordinator(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("p2def", "a polecat-filed successor"),
		resultOf(`{"verdict":"pass"}`),
		knownSet("mayor", "pm-pogo")) // p2def is gone

	o := n.Notify(Completion{Closed: true, ItemID: "mg-9999", Route: RouteMerge, Worker: "p9999", MergedSHA: "deadbee"})

	if o.To != "mayor" {
		t.Fatalf("expected the redirect to reach the coordinator, got %q", o.To)
	}
	if !o.Redirected {
		t.Errorf("outcome must record that this was a redirect: %+v", o)
	}
	if o.Creator != "p2def" {
		t.Errorf("outcome lost the creator it was redirected from: %+v", o)
	}
	m := rec.last()
	// Naming the vanished creator is what stops the redirect being an anonymous
	// duplicate of the refinery's own merge mail.
	if !strings.Contains(m.subject, "p2def") || !strings.Contains(m.body, "p2def") {
		t.Errorf("redirect does not name the creator it stands in for:\nsubject=%q\nbody=%s", m.subject, m.body)
	}
}

// A filer that is also the worker already knows. Skipped — but RECORDED as a
// skip, because a decision not to mail must not look like a notifier that never
// ran.
func TestAFilerThatIsAlsoTheWorkerIsNotMailedAndTheSkipIsReported(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("p777", "self-filed"), resultOf(""), knownSet("p777", "mayor"))

	o := n.Notify(Completion{Closed: true, ItemID: "mg-777", Route: RouteSelfClose, Worker: "p777"})

	if o.Sent() {
		t.Fatalf("expected no mail to the worker about its own item, got %+v", o)
	}
	if o.Skipped == "" {
		t.Errorf("a skip with no stated reason is indistinguishable from a notifier that never ran: %+v", o)
	}
	if rec.count() != 0 {
		t.Errorf("expected zero mails, got %d", rec.count())
	}
}

// THE COORDINATOR IS TOLD ON THE MERGE ROUTE (mg-da12).
//
// A skip here spared it one, on the grounds that "the refinery already mails
// the coordinator on every merge". The refinery mails a MERGE — branch, target,
// merged SHA — and this mails a VERDICT, and no merge mail carries what the
// worker concluded. The two were treated as substitutes, which made the
// coordinator the single filer this never reached: measured on 2026-08-13, its
// mailbox held zero COMPLETED notices while all three the fleet had gone to
// non-coordinators.
//
// The assertion that matters is not that a mail arrives — it is that the mail
// carries the half a merge notice cannot.
func TestTheCoordinatorIsToldOnTheMergeRouteBecauseAMergeMailIsNotAVerdict(t *testing.T) {
	rec := &recorder{}
	sidecar := `{"verdict":"pass","summary":"disposition of both branches established by content, not patch-id"}`
	n := New("mayor", rec.send, filing("mayor", "a mayor-filed item"), resultOf(sidecar), knownSet("mayor"))

	merge := n.Notify(Completion{Closed: true, ItemID: "mg-aaaa", Route: RouteMerge, Worker: "paaaa",
		Branch: "polecat-paaaa", MergedSHA: "1111111"})
	if !merge.Sent() {
		t.Fatalf("the refinery's merge mail reports a MERGE and this reports a VERDICT; the coordinator is owed "+
			"this one: %+v", merge)
	}
	if merge.To != "mayor" {
		t.Errorf("mail went to %q, want the coordinator that filed it", merge.To)
	}
	if merge.Redirected {
		t.Errorf("the coordinator filed this item; standing in for itself is not a redirect: %+v", merge)
	}
	m := rec.last()
	if !strings.Contains(m.body, "disposition of both branches") {
		t.Errorf("the verdict is the whole reason this mail is not a duplicate of the refinery's:\n%s", m.body)
	}
	if !strings.Contains(m.subject, "COMPLETED") {
		t.Errorf("subject must report the completion: %q", m.subject)
	}

	self := n.Notify(Completion{Closed: true, ItemID: "mg-bbbb", Route: RouteSelfClose, Worker: "pbbbb"})
	if !self.Sent() || self.To != "mayor" {
		t.Errorf("a coordinator-filed item that closes with no merge is reported by nothing else; expected a mail, got %+v", self)
	}
}

// The removed skip was keyed on the creator being the coordinator BY NAME, so
// the regression is name-shaped: whatever the coordinator is called, a
// merge-route item it filed must still be reported to it.
func TestNoFilerIsSkippedForBeingTheCoordinator(t *testing.T) {
	for _, coord := range []string{"mayor", "MAYOR", "pm-pogo", "architect"} {
		rec := &recorder{}
		n := New(coord, rec.send, filing(coord, "t"), resultOf(`{"verdict":"pass"}`), knownSet(coord))
		o := n.Notify(Completion{Closed: true, ItemID: "mg-cccc", Route: RouteMerge, Worker: "pcccc",
			Branch: "polecat-pcccc", MergedSHA: "2222222"})
		if !o.Sent() {
			t.Errorf("coordinator %q was skipped on the merge route: %+v", coord, o)
		}
		if rec.count() != 1 {
			t.Errorf("coordinator %q: got %d mails, want 1", coord, rec.count())
		}
	}
}

// One completion seen twice — the merge path performs the close, the done
// reaper notices it afterwards — is one mail.
func TestTheSameCompletionSeenByBothObserversSendsOneMail(t *testing.T) {
	rec := &recorder{}
	sidecar := `{"branch":"polecat-pcccc","verdict":{"verdict":"pass"}}`
	n := New("mayor", rec.send, filing("pm-pogo", "t"), resultOf(sidecar), knownSet("pm-pogo", "mayor"))

	// The merge observer holds the branch and SHA; the reaper holds neither.
	n.Notify(Completion{Closed: true, ItemID: "mg-cccc", Route: RouteMerge, Worker: "pcccc", Branch: "polecat-pcccc", MergedSHA: "cafe123", Result: sidecar})
	n.Notify(Completion{Closed: true, ItemID: "mg-cccc", Route: RouteSelfClose, Worker: "pcccc"})

	if got := rec.count(); got != 1 {
		t.Fatalf("one completion produced %d mails; the dedup key must not depend on what a given observer happens to know", got)
	}
}

// A reopened-and-reclosed item is a SECOND completion. A ledger keyed on the
// bare item id would swallow it — this ticket's own defect wearing the fix's
// clothes.
func TestASecondCompletionOfTheSameItemIsReported(t *testing.T) {
	rec := &recorder{}
	sidecar := `{"attempt":1}`
	n := New("mayor", rec.send, filing("pm-pogo", "t"), func(string) (string, error) { return sidecar, nil },
		knownSet("pm-pogo", "mayor"))

	n.Notify(Completion{Closed: true, ItemID: "mg-dddd", Route: RouteMerge, Worker: "pdddd", MergedSHA: "aaa"})
	sidecar = `{"attempt":2}`
	n.Notify(Completion{Closed: true, ItemID: "mg-dddd", Route: RouteMerge, Worker: "pdddd", MergedSHA: "bbb"})

	if got := rec.count(); got != 2 {
		t.Fatalf("expected the second completion to be reported too, got %d mails", got)
	}
}

// A send that fails is not recorded as handled, so the next observation retries
// it. The alternative is a notification lost exactly once and silently, which
// is the failure mode this package exists to remove.
func TestAFailedSendIsRetriedOnTheNextObservation(t *testing.T) {
	rec := &recorder{err: errors.New("no_such_mailbox")}
	n := New("mayor", rec.send, filing("pm-pogo", "t"), resultOf(`{"v":1}`), knownSet("pm-pogo", "mayor"))

	c := Completion{Closed: true, ItemID: "mg-eeee", Route: RouteSelfClose, Worker: "peeee"}
	first := n.Notify(c)
	if first.Err == nil {
		t.Fatal("expected the send failure to surface in the outcome")
	}
	rec.mu.Lock()
	rec.err = nil
	rec.mu.Unlock()
	second := n.Notify(c)
	if !second.Sent() {
		t.Fatalf("a failed send must not mark the completion handled; got %+v", second)
	}
}

// A recipient mg has no mailbox for is refused (exit 3, no_such_mailbox —
// mg-d639), and the registry knowing a name is not the same as mg having been
// told about it. The report must not die on that refusal.
func TestARefusedRecipientRelaysToTheCoordinator(t *testing.T) {
	rec := &recorder{}
	// Refuse the filer, accept the coordinator.
	send := func(to, from, subject, body string) error {
		if to == "pm-onethird" {
			return errors.New("no_such_mailbox")
		}
		return rec.send(to, from, subject, body)
	}
	n := New("mayor", send, filing("pm-onethird", "t"), resultOf(`{"verdict":"pass"}`),
		knownSet("pm-onethird", "mayor"))

	o := n.Notify(Completion{Closed: true, ItemID: "mg-6666", Route: RouteMerge, Worker: "p6666", MergedSHA: "aaa"})

	if o.Err == nil {
		t.Error("the refusal must still surface as an error — a relay is a worse outcome, not a success")
	}
	if rec.count() != 1 {
		t.Fatalf("expected the report to be relayed to the coordinator, got %d mails", rec.count())
	}
	m := rec.last()
	if m.to != "mayor" || !strings.HasPrefix(m.subject, "UNDELIVERABLE ") {
		t.Errorf("relay is not legible as one: to=%q subject=%q", m.to, m.subject)
	}
	if !strings.Contains(m.body, "pm-onethird") || !strings.Contains(m.body, `"verdict":"pass"`) {
		t.Errorf("the relay must carry both the intended recipient and the report:\n%s", m.body)
	}
}

// An item whose creator cannot be READ is escalated, not dropped. "Cannot tell
// who is waiting" is not "nobody is waiting".
func TestAnUnreadableCreatorEscalatesToTheCoordinator(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send,
		func(string) (string, string, error) { return "", "", errors.New("store unreadable") },
		resultOf(""), knownSet("mayor"))

	o := n.Notify(Completion{Closed: true, ItemID: "mg-ffff", Route: RouteMerge, Worker: "pffff"})

	if o.To != "mayor" || !o.Sent() {
		t.Fatalf("expected an escalation to the coordinator, got %+v", o)
	}
	if !strings.Contains(rec.last().body, "store unreadable") {
		t.Errorf("the escalation must carry the reason it could not read the creator:\n%s", rec.last().body)
	}
}

// An item with no recorded creator has nobody nameable waiting on it. Silent to
// the fleet, but not silent in the outcome.
func TestAnItemWithNoCreatorSendsNothingAndSaysSo(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("", ""), resultOf(""), knownSet("mayor"))

	o := n.Notify(Completion{Closed: true, ItemID: "mg-0000", Route: RouteSelfClose})

	if o.Sent() || rec.count() != 0 {
		t.Fatalf("expected no mail for an item with no creator, got %+v", o)
	}
	if !strings.Contains(o.Skipped, "creator") {
		t.Errorf("the skip reason must name what was missing: %q", o.Skipped)
	}
}

// An item closed with no verdict says so in words. That is a real finding — it
// is the shape a worker leaves when it skips --verdict-file (mg-dfea) — and it
// must not read the same as a notifier that failed to look.
func TestAMissingVerdictIsStatedRatherThanOmitted(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("pm-riemann", "t"), resultOf(""), knownSet("pm-riemann", "mayor"))

	n.Notify(Completion{Closed: true, ItemID: "mg-1111", Route: RouteSelfClose, Worker: "p1111"})

	body := rec.last().body
	if !strings.Contains(body, "NONE RECORDED") {
		t.Errorf("body must state that no verdict was recorded:\n%s", body)
	}
}

// Every outcome reaches the recorder, sends and skips alike. Without this the
// notifier's own silence would be unauditable — the exact defect it closes.
func TestEverySkipReachesTheRecorder(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("p2222", "t"), resultOf(""), knownSet("p2222", "mayor"))
	var seen []Outcome
	n.SetRecorder(func(_ Completion, o Outcome) { seen = append(seen, o) })

	n.Notify(Completion{Closed: true, ItemID: "mg-2222", Route: RouteSelfClose, Worker: "p2222"}) // skip: filer is worker

	if len(seen) != 1 {
		t.Fatalf("expected the skip to be recorded, got %d records", len(seen))
	}
	if seen[0].Skipped == "" {
		t.Errorf("recorded outcome carries no reason: %+v", seen[0])
	}
}

// The caller's copy of the sidecar is a fallback, never an override: the store
// is what actually closed the item.
func TestTheStoreOutranksTheCallersCopyOfTheSidecar(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("pm-pogo", "t"),
		resultOf(`{"verdict":"the worker's own"}`), knownSet("pm-pogo", "mayor"))

	n.Notify(Completion{Closed: true, ItemID: "mg-3333", Route: RouteMerge, Worker: "p3333",
		Result: `{"verdict":"the refinery's"}`})

	body := rec.last().body
	if !strings.Contains(body, "the worker's own") || strings.Contains(body, "the refinery's") {
		t.Errorf("expected the store's sidecar to win over the caller's copy:\n%s", body)
	}
}

func TestTheFallbackSidecarIsUsedWhenTheStoreHasNone(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("pm-pogo", "t"), resultOf(""), knownSet("pm-pogo", "mayor"))

	n.Notify(Completion{Closed: true, ItemID: "mg-4444", Route: RouteMerge, Worker: "p4444",
		Result: `{"verdict":"from the caller"}`})

	if !strings.Contains(rec.last().body, "from the caller") {
		t.Errorf("expected the caller's copy to be used when the store yields nothing:\n%s", rec.last().body)
	}
}

func TestANilNotifierAndAnUnwiredOneAreBothSafe(t *testing.T) {
	var n *Notifier
	if o := n.Notify(Completion{ItemID: "mg-5555"}); o.Sent() {
		t.Errorf("a nil notifier must send nothing: %+v", o)
	}
	empty := &Notifier{}
	if o := empty.Notify(Completion{ItemID: "mg-5555"}); o.Skipped == "" {
		t.Errorf("an unwired notifier must say why it did nothing: %+v", o)
	}
}

func TestOneLineFlattensAndBoundsATitle(t *testing.T) {
	if got := oneLine("a\nb   c"); got != "a b c" {
		t.Errorf("oneLine(%q) = %q", "a\nb   c", got)
	}
	long := strings.Repeat("x", 400)
	if got := oneLine(long); len([]rune(got)) > 120 {
		t.Errorf("oneLine did not bound a long title: %d runes", len([]rune(got)))
	}
}

// A MERGE THAT LEFT ITS ITEM OPEN IS NOT A COMPLETION, AND THE MAIL MUST NOT
// READ AS ONE (mg-2b71).
//
// The observed mail said "COMPLETED: mg-479c / Closed: its branch merged
// (polecat-c479c) as 1a0240a..." about an item `mg show` reported as
// `status=available`. Two assertions of closure, both false, and the third line
// ("NONE RECORDED — the item closed with no readable result sidecar") invited
// the reader to conclude the close had happened sloppily rather than not at all.
func TestAMergeThatDidNotCloseItsItemIsNotMailedAsACompletion(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("pm-onethird", "the cube-foliation energy identity"),
		resultOf(""), knownSet("pm-onethird", "mayor"))

	o := n.Notify(Completion{ItemID: "mg-479c", Route: RouteMerge, Worker: "c479c",
		Branch: "polecat-c479c", MergedSHA: "1a0240a",
		NotClosedReason: "`mg done` did not apply: not claimed, so it cannot be completed"})

	if !o.Sent() {
		t.Fatalf("the filer is still owed the mail — the item is open and nobody else is watching: %+v", o)
	}
	m := rec.last()
	if strings.Contains(m.subject, "COMPLETED") {
		t.Errorf("the subject is the part that gets skimmed and forwarded; it must not say COMPLETED: %q", m.subject)
	}
	if !strings.Contains(m.subject, "MERGED BUT NOT CLOSED") {
		t.Errorf("the subject must say what actually happened: %q", m.subject)
	}
	if strings.Contains(m.body, "Closed:") {
		t.Errorf("the body must not assert a close that did not happen:\n%s", m.body)
	}
	if !strings.Contains(m.body, "NOT CLOSED: the item is still open") {
		t.Errorf("the body must state the item's real state:\n%s", m.body)
	}
	if !strings.Contains(m.body, "not claimed") {
		t.Errorf("the body must carry the reason, which is the actionable half:\n%s", m.body)
	}
	if !strings.Contains(m.body, "this item did not close") {
		t.Errorf("a missing sidecar is a CONSEQUENCE of the close not happening, and must not read as a defect "+
			"of a close that did:\n%s", m.body)
	}
}

// A "they already know" skip is a claim about a message that was actually sent,
// and no message says this. The worker was stopped before it could see the
// refusal; the coordinator's own MERGED mail reports the merge and nothing about
// the item's state — which is the same substitution mg-da12 removed the
// coordinator skip for, one field over.
func TestNoSkipSwallowsAMergeThatLeftTheItemOpen(t *testing.T) {
	t.Run("coordinator filed it", func(t *testing.T) {
		rec := &recorder{}
		n := New("mayor", rec.send, filing("mayor", "t"), resultOf(""), knownSet("mayor"))
		o := n.Notify(Completion{ItemID: "mg-479c", Route: RouteMerge, NotClosedReason: "not claimed"})
		if !o.Sent() {
			t.Fatalf("the refinery's MERGED mail says nothing about the item staying open: %+v", o)
		}
	})
	t.Run("the filer is the worker", func(t *testing.T) {
		rec := &recorder{}
		n := New("mayor", rec.send, filing("p479c", "t"), resultOf(""), knownSet("p479c", "mayor"))
		o := n.Notify(Completion{ItemID: "mg-479c", Route: RouteMerge, Worker: "p479c", NotClosedReason: "not claimed"})
		if !o.Sent() {
			t.Fatalf("a worker stopped at merge never saw the refusal, so it does not already know: %+v", o)
		}
	})
}

// Closedness is part of the dedup fingerprint: the not-closed notice and the
// item's later close are two different facts, and a close recording no sidecar
// would otherwise key identically to the notice that preceded it.
func TestTheNotClosedNoticeDoesNotSwallowTheLaterCompletion(t *testing.T) {
	rec := &recorder{}
	n := New("mayor", rec.send, filing("pm-onethird", "t"), resultOf(""), knownSet("pm-onethird", "mayor"))

	n.Notify(Completion{ItemID: "mg-479c", Route: RouteMerge, NotClosedReason: "not claimed"})
	n.Notify(Completion{Closed: true, ItemID: "mg-479c", Route: RouteSelfClose})

	if rec.count() != 2 {
		t.Fatalf("the close that finally happened is a second fact and must be reported: got %d mails", rec.count())
	}
	if strings.Contains(rec.last().subject, "NOT CLOSED") {
		t.Errorf("the second mail is the completion: %q", rec.last().subject)
	}
}
