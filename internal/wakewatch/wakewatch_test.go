package wakewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/drellem2/pogo/internal/events"
)

type sentNudge struct{ agent, text string }
type sentMail struct{ to, from, subject, body string }
type emitted struct {
	typ string
	d   map[string]any
}

type harness struct {
	t       *testing.T
	dir     string
	path    string
	state   string
	now     time.Time
	fleet   []AgentRef
	items   map[string]Item
	subject map[string]string
	nudges  []sentNudge
	mails   []sentMail
	events  []emitted
	nudgeFn func(agent, text string) (string, error)
}

func newHarness(t *testing.T) *harness {
	dir := t.TempDir()
	h := &harness{
		t:       t,
		dir:     dir,
		path:    filepath.Join(dir, "events.jsonl"),
		state:   filepath.Join(dir, "state", "wakewatch.json"),
		now:     time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
		items:   map[string]Item{},
		subject: map[string]string{},
		fleet: []AgentRef{
			{Name: "mayor", Running: true},
			{Name: "pm-pogo", Running: true},
			{Name: "pe00c", WorkItemID: "mg-e00c", Running: true},
			{Name: "pm-riemann", Running: false}, // parked
		},
	}
	if err := os.WriteFile(h.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) watcher() *Watcher {
	return New(Params{}, Deps{
		EventsPath:  h.path,
		StatePath:   h.state,
		Agents:      func() []AgentRef { return h.fleet },
		Crew:        func() []string { return []string{"mayor", "pm-pogo", "doctor"} },
		Item:        func(id string) (Item, bool) { it, ok := h.items[id]; return it, ok },
		Subject:     func(box, id string) string { return h.subject[id] },
		Coordinator: "mayor",
		Now:         func() time.Time { return h.now },
		Nudge: func(agent, text string) (string, error) {
			h.nudges = append(h.nudges, sentNudge{agent, text})
			if h.nudgeFn != nil {
				return h.nudgeFn(agent, text)
			}
			return OutcomeDelivered, nil
		},
		Mail: func(to, from, subject, body string) error {
			h.mails = append(h.mails, sentMail{to, from, subject, body})
			return nil
		},
		Emit: func(typ, item string, d map[string]any) {
			h.events = append(h.events, emitted{typ, d})
		},
	})
}

func (h *harness) append(ev map[string]any) {
	h.t.Helper()
	if _, ok := ev["ts"]; !ok {
		ev["ts"] = h.now.Format(time.RFC3339)
	}
	b, _ := json.Marshal(ev)
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (h *harness) mailSent(from, to, id string) {
	h.append(map[string]any{"type": "mail.sent", "from": from, "to": to, "msg_id": id})
}

func (h *harness) mailRead(box, id string) {
	h.append(map[string]any{"type": "mail.read", "mailbox": box, "msg_id": id})
}

func (h *harness) count(typ string) int {
	n := 0
	for _, e := range h.events {
		if e.typ == typ {
			n++
		}
	}
	return n
}

func (h *harness) last(typ string) map[string]any {
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].typ == typ {
			return h.events[i].d
		}
	}
	h.t.Fatalf("no %s event; have %v", typ, h.events)
	return nil
}

// --- the cap ---------------------------------------------------------------

func TestPointerCapHoldsForA400CharSubject(t *testing.T) {
	subj := strings.Repeat("S", 400)
	text := BuildPointer("", []Trigger{{Kind: "mail", Event: "mail.sent", MsgID: "m1", Box: "pe00c", From: "mayor", Quoted: subj}})
	// The literal 100, not MaxPointerLen: a test measured against the constant
	// would follow the constant if someone raised it.
	if len(text) > 100 || MaxPointerLen != 100 {
		t.Fatalf("pointer is %d bytes (cap const %d), the design's cap is 100: %q", len(text), MaxPointerLen, text)
	}
	if !strings.HasSuffix(text, "mg mail list pe00c") {
		t.Errorf("actionable words must come LAST (mg-8a70); got %q", text)
	}
}

func TestPointerCapHoldsForEveryPathologicalField(t *testing.T) {
	long := strings.Repeat("é", 300) // multi-byte: a rune count would under-count bytes
	cases := [][]Trigger{
		{{Kind: "mail", MsgID: "m", Box: "pe00c", From: long, Quoted: long}},
		{{Kind: "mail", MsgID: "m", Box: long, From: "x", Quoted: "q"}},
		{{Kind: "assign", ItemID: "mg-" + long, Quoted: long}},
		{{Kind: "mail", MsgID: "a", Box: "b1", From: "x"}, {Kind: "mail", MsgID: "b", Box: "b2", From: "y"}, {Kind: "assign", ItemID: "mg-1234", Quoted: long}},
		{{Kind: "mail", MsgID: "m", Box: "pe00c", From: "a\nb\x1b[2J", Quoted: "line1\nline2\r\n"}},
	}
	for i, batch := range cases {
		text := BuildPointer("still unread: ", batch)
		if len(text) > 100 {
			t.Errorf("case %d: %d bytes > 100: %q", i, len(text), text)
		}
		if !utf8.ValidString(text) {
			t.Errorf("case %d: invalid UTF-8: %q", i, text)
		}
		if strings.ContainsAny(text, "\n\r\x1b") {
			t.Errorf("case %d: control character reached the terminal text: %q", i, text)
		}
	}
}

func TestPointerShapes(t *testing.T) {
	mail := BuildPointer("", []Trigger{{Kind: "mail", MsgID: "m", Box: "e00c", From: "mayor", Quoted: "fix it"}})
	if mail != `mail from mayor: "fix it" — mg mail list e00c` {
		t.Errorf("mail pointer = %q", mail)
	}
	bare := BuildPointer("", []Trigger{{Kind: "mail", MsgID: "m", Box: "e00c", From: "mayor"}})
	if bare != `mail from mayor — mg mail list e00c` {
		t.Errorf("subjectless pointer = %q", bare)
	}
	as := BuildPointer("", []Trigger{{Kind: "assign", ItemID: "mg-1234", Quoted: "a title"}})
	if as != `assigned: mg-1234 "a title" — mg show mg-1234` {
		t.Errorf("assign pointer = %q", as)
	}
	two := BuildPointer("", []Trigger{
		{Kind: "mail", MsgID: "a", Box: "pe00c", From: "pm-pogo", Quoted: "one"},
		{Kind: "mail", MsgID: "b", Box: "e00c", From: "mayor", Quoted: "two"},
	})
	if two != `2 new, latest mail from mayor: "two" — mg mail list e00c; mg mail list pe00c` {
		t.Errorf("coalesced pointer = %q", two)
	}
}

// --- triggers ---------------------------------------------------------------

func TestMailSentToAgentNamePointsOnce(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.subject["m1"] = "please rebase"
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	if len(h.nudges) != 1 || h.nudges[0].agent != "pe00c" {
		t.Fatalf("nudges = %+v", h.nudges)
	}
	if want := `mail from mayor: "please rebase" — mg mail list pe00c`; h.nudges[0].text != want {
		t.Errorf("text = %q, want %q", h.nudges[0].text, want)
	}
	d := h.last(EventPointerSent)
	if d["recipient"] != "pe00c" || d["trigger"] != "mail.sent" || d["msg_id"] != "m1" || d["text_len"] != len(h.nudges[0].text) || d["outcome"] != OutcomeDelivered {
		t.Errorf("event = %v", d)
	}
	w.Poll()
	if len(h.nudges) != 1 {
		t.Errorf("a second poll re-pointed: %+v", h.nudges)
	}
}

func TestMailSentToWorkItemBoxResolvesToHolder(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "e00c", "m1")
	w.Poll()
	if len(h.nudges) != 1 || h.nudges[0].agent != "pe00c" || !strings.HasSuffix(h.nudges[0].text, "mg mail list e00c") {
		t.Fatalf("nudges = %+v", h.nudges)
	}
}

func TestAssigneeEditPoints(t *testing.T) {
	for _, after := range []string{"pm-pogo", "blocked:pm-pogo"} {
		t.Run(after, func(t *testing.T) {
			h := newHarness(t)
			h.items["mg-1234"] = Item{Title: "do the thing"}
			w := h.watcher()
			w.Start()
			h.append(map[string]any{"type": "work.edited", "item_id": "mg-1234", "actor": "mayor",
				"fields": "assignee", "assignee_before": "", "assignee_after": after})
			w.Poll()
			if len(h.nudges) != 1 || h.nudges[0].agent != "pm-pogo" {
				t.Fatalf("nudges = %+v", h.nudges)
			}
			if want := `assigned: mg-1234 "do the thing" — mg show mg-1234`; h.nudges[0].text != want {
				t.Errorf("text = %q", h.nudges[0].text)
			}
			if d := h.last(EventPointerSent); d["item_id"] != "mg-1234" || d["trigger"] != "work.edited" {
				t.Errorf("event = %v", d)
			}
		})
	}
}

func TestSelfAssignmentIsNotPointed(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.append(map[string]any{"type": "work.edited", "item_id": "mg-1234", "actor": "pm-pogo",
		"fields": "assignee", "assignee_after": "pm-pogo"})
	w.Poll()
	if len(h.nudges) != 0 {
		t.Fatalf("self-assignment pointed: %+v", h.nudges)
	}
	if d := h.last(EventPointerSkipped); d["reason"] != "self-assigned" {
		t.Errorf("skip = %v", d)
	}
}

// work.created carries no assignee in today's macguffin (checked against the
// live store 2026-09-28), so the item's own frontmatter supplies it.
func TestWorkCreatedWithAssigneeFromItem(t *testing.T) {
	h := newHarness(t)
	h.items["mg-5678"] = Item{Title: "new work", Assignee: "pm-pogo"}
	w := h.watcher()
	w.Start()
	h.append(map[string]any{"type": "work.created", "item_id": "mg-5678", "actor": "mayor", "to_status": "available"})
	w.Poll()
	if len(h.nudges) != 1 || h.nudges[0].agent != "pm-pogo" || !strings.HasSuffix(h.nudges[0].text, "mg show mg-5678") {
		t.Fatalf("nudges = %+v", h.nudges)
	}
	if d := h.last(EventPointerSent); d["trigger"] != "work.created" {
		t.Errorf("event = %v", d)
	}
}

func TestWorkCreatedUsesEventAssigneeWhenMacguffinRecordsIt(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.append(map[string]any{"type": "work.created", "item_id": "mg-5678", "actor": "mayor", "assignee": "pm-pogo"})
	w.Poll()
	if len(h.nudges) != 1 || h.nudges[0].agent != "pm-pogo" {
		t.Fatalf("nudges = %+v", h.nudges)
	}
}

func TestCoalescesPerRecipientWithCount(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	h.now = h.now.Add(10 * time.Second)
	h.mailSent("pm-pogo", "pe00c", "m2")
	h.mailSent("mayor", "e00c", "m3")
	w.Poll()
	if len(h.nudges) != 1 {
		t.Fatalf("coalescing window not honoured: %+v", h.nudges)
	}
	h.now = h.now.Add(51 * time.Second)
	w.Poll()
	if len(h.nudges) != 2 {
		t.Fatalf("coalesced batch never flushed: %+v", h.nudges)
	}
	if !strings.HasPrefix(h.nudges[1].text, "2 new, latest ") {
		t.Errorf("coalesced text carries no count: %q", h.nudges[1].text)
	}
	d := h.last(EventPointerSent)
	if d["count"] != 2 || len(d["msg_ids"].([]string)) != 2 {
		t.Errorf("event = %v", d)
	}
}

func TestMailToNonAgentBoxIsNeitherPointedNorBounced(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "human", "m1")
	h.mailSent("fleet-liveness-probe", "fleet-liveness-selftest", "m2")
	w.Poll()
	if len(h.nudges) != 0 || len(h.mails) != 0 || h.count(EventBounce) != 0 {
		t.Fatalf("nudges=%v mails=%v", h.nudges, h.mails)
	}
}

func TestQueuedNudgeIsRecordedAsQueued(t *testing.T) {
	h := newHarness(t)
	h.nudgeFn = func(string, string) (string, error) { return OutcomeQueued, nil }
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	if d := h.last(EventPointerSent); d["outcome"] != OutcomeQueued {
		t.Errorf("event = %v", d)
	}
}

func TestFailedNudgeIsRecordedAsFailed(t *testing.T) {
	h := newHarness(t)
	h.nudgeFn = func(string, string) (string, error) { return "", errors.New("pty busy") }
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	if d := h.last(EventPointerSent); d["outcome"] != OutcomeFailed || d["error"] != "pty busy" {
		t.Errorf("event = %v", d)
	}
}

// --- bounce -----------------------------------------------------------------

func TestBounceToNonRunningRecipient(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	// p7666 ran and was reaped: observed once, then gone from the registry.
	h.fleet = append(h.fleet, AgentRef{Name: "p7666", WorkItemID: "mg-7666", Running: true})
	h.mailSent("pm-pogo", "pe00c", "warm") // any resolution observes the fleet
	w.Poll()
	h.fleet = h.fleet[:len(h.fleet)-1]
	h.nudges = nil

	h.subject["m1"] = "status?"
	h.mailSent("pm-pogo", "p7666", "m1")
	w.Poll()
	if len(h.nudges) != 0 {
		t.Fatalf("pointed at a dead agent: %+v", h.nudges)
	}
	if len(h.mails) != 2 || h.mails[0].to != "pm-pogo" || h.mails[1].to != "mayor" {
		t.Fatalf("bounce mails = %+v (want sender then coordinator)", h.mails)
	}
	if !strings.Contains(h.mails[0].body, "recipient p7666 is not running; your mail is unread in p7666") {
		t.Errorf("body = %q", h.mails[0].body)
	}
	d := h.last(EventBounce)
	if d["recipient"] != "p7666" || d["msg_id"] != "m1" || d["trigger"] != "mail.sent" || d["text_len"] != len(h.mails[0].body) {
		t.Errorf("event = %v", d)
	}

	// The work-item box of the same reaped polecat bounces too.
	h.mails = nil
	h.mailSent("mayor", "7666", "m2")
	w.Poll()
	if len(h.mails) != 1 || h.mails[0].to != "mayor" || !strings.Contains(h.mails[0].body, "unread in 7666") {
		t.Fatalf("work-item box bounce = %+v (sender is the coordinator: one mail)", h.mails)
	}
}

func TestBounceToParkedAndConfiguredButAbsentCrew(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("pm-pogo", "pm-riemann", "m1") // parked
	h.mailSent("pm-pogo", "doctor", "m2")     // configured, never started
	w.Poll()
	if h.count(EventBounce) != 2 {
		t.Fatalf("bounces = %d; events %v", h.count(EventBounce), h.events)
	}
}

func TestBounceSkipsDeadSenderAndPogodMail(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("pm-riemann", "doctor", "m1") // dead sender: only the coordinator hears
	w.Poll()
	if len(h.mails) != 1 || h.mails[0].to != "mayor" {
		t.Fatalf("mails = %+v", h.mails)
	}
	h.mails = nil
	h.mailSent(Sender, "doctor", "m2") // pogod's own mail never bounces (no loop)
	w.Poll()
	if len(h.mails) != 0 {
		t.Fatalf("bounced pogod's own mail: %+v", h.mails)
	}
}

// --- recovery ---------------------------------------------------------------

func TestRecoveryRenudgesThenEscalatesAfterThree(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.subject["m1"] = "please read"
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	if len(h.nudges) != 1 {
		t.Fatalf("arrival pointer missing")
	}

	step := func(d time.Duration) { h.now = h.now.Add(d); w.Poll(); w.Recover() }

	step(10 * time.Minute) // 10m old: under threshold
	if h.count(EventRenudge) != 0 {
		t.Fatalf("re-nudged under the 15m threshold")
	}
	step(6 * time.Minute) // 16m: re-nudge 1
	if h.count(EventRenudge) != 1 {
		t.Fatalf("renudges = %d, want 1", h.count(EventRenudge))
	}
	if last := h.nudges[len(h.nudges)-1].text; !strings.HasPrefix(last, "still unread: ") || len(last) > MaxPointerLen {
		t.Errorf("renudge text = %q", last)
	}
	step(5 * time.Minute) // 21m: too soon after the last
	if h.count(EventRenudge) != 1 {
		t.Fatalf("re-nudged inside the 15m gap")
	}
	step(10 * time.Minute) // 31m: re-nudge 2
	step(15 * time.Minute) // 46m: re-nudge 3
	if h.count(EventRenudge) != 3 {
		t.Fatalf("renudges = %d, want 3", h.count(EventRenudge))
	}
	if d := h.last(EventRenudge); d["attempt"] != 3 || d["msg_id"] != "m1" || d["recipient"] != "pe00c" {
		t.Errorf("renudge event = %v", d)
	}
	if h.count(EventUnconsumed) != 0 {
		t.Fatalf("escalated early")
	}
	step(15 * time.Minute) // 61m: budget spent → escalate
	if h.count(EventUnconsumed) != 1 {
		t.Fatalf("unconsumed = %d, want 1", h.count(EventUnconsumed))
	}
	if h.count(EventRenudge) != 3 {
		t.Errorf("a fourth re-nudge was sent")
	}
	if len(h.mails) != 1 || h.mails[0].to != "mayor" || !strings.Contains(h.mails[0].body, "m1") {
		t.Errorf("coordinator mail = %+v", h.mails)
	}
	step(30 * time.Minute)
	if h.count(EventUnconsumed) != 1 || len(h.mails) != 1 {
		t.Errorf("escalated twice")
	}
}

func TestRecoveryStopsOnceMailIsRead(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	h.now = h.now.Add(20 * time.Minute)
	h.mailRead("pe00c", "m1")
	w.Poll()
	w.Recover()
	if h.count(EventRenudge) != 0 {
		t.Fatalf("re-nudged read mail")
	}
}

func TestRecoveryForUnclaimedAssignment(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.append(map[string]any{"type": "work.edited", "item_id": "mg-1234", "actor": "mayor", "fields": "assignee", "assignee_after": "pm-pogo"})
	w.Poll()
	h.now = h.now.Add(16 * time.Minute)
	w.Poll()
	w.Recover()
	if h.count(EventRenudge) != 1 || h.last(EventRenudge)["item_id"] != "mg-1234" {
		t.Fatalf("renudge events = %v", h.events)
	}
	h.append(map[string]any{"type": "work.claim", "item_id": "mg-1234", "actor": "pm-pogo"})
	h.now = h.now.Add(16 * time.Minute)
	w.Poll()
	w.Recover()
	if h.count(EventRenudge) != 1 {
		t.Fatalf("re-nudged a claimed item")
	}
}

func TestRecoverySuspendedWhileBlind(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	os.Remove(h.path)
	h.now = h.now.Add(20 * time.Minute)
	w.Poll()
	w.Recover()
	if w.Blind() == "" || h.count(EventBlind) != 1 {
		t.Fatalf("absent file did not make the watcher blind")
	}
	if h.count(EventRenudge) != 0 {
		t.Fatalf("re-nudged from a model that cannot see mail.read — an absent file is no data, not unread mail")
	}
}

// --- restart / rotation / absence ------------------------------------------

func TestRestartDoesNotRepointOldMail(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	if len(h.nudges) != 1 {
		t.Fatal("setup")
	}

	// pogod restarts.
	h.now = h.now.Add(30 * time.Second)
	w2 := h.watcher()
	w2.Start()
	w2.Poll()
	if len(h.nudges) != 1 {
		t.Fatalf("restart re-pointed old mail: %+v", h.nudges)
	}
	// Mail that arrived while it was down IS pointed.
	h.mailSent("mayor", "pe00c", "m2")
	h.now = h.now.Add(2 * time.Minute)
	w2.Poll()
	if len(h.nudges) != 2 {
		t.Fatalf("new mail after restart not pointed: %+v", h.nudges)
	}
}

func TestFirstRunDoesNotPointHistory(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		h.mailSent("mayor", "pe00c", fmt.Sprintf("old%d", i))
	}
	w := h.watcher()
	w.Start()
	w.Poll()
	if len(h.nudges) != 0 {
		t.Fatalf("first run pointed history: %+v", h.nudges)
	}
}

func TestRestartKeepsRenudgeBudget(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(16 * time.Minute)
		w.Poll()
		w.Recover()
	}
	if h.count(EventRenudge) != 3 {
		t.Fatalf("setup: renudges = %d", h.count(EventRenudge))
	}
	w2 := h.watcher()
	w2.Start()
	h.now = h.now.Add(16 * time.Minute)
	w2.Poll()
	w2.Recover()
	if h.count(EventRenudge) != 3 {
		t.Fatalf("restart reset the budget: renudges = %d", h.count(EventRenudge))
	}
	if h.count(EventUnconsumed) != 1 {
		t.Fatalf("unconsumed = %d", h.count(EventUnconsumed))
	}
}

func TestRebuiltStoreIsNotRepointed(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pe00c", "m1")
	w.Poll()

	// The store is deleted and rebuilt with its history (2026-09-27), plus one
	// genuinely new message.
	os.Remove(h.path)
	w.Poll()
	h.append(map[string]any{"type": "mail.sent", "from": "mayor", "to": "pe00c", "msg_id": "m1", "ts": h.now.Format(time.RFC3339)})
	h.append(map[string]any{"type": "mail.sent", "from": "mayor", "to": "pe00c", "msg_id": "ancient", "ts": h.now.Add(-3 * time.Hour).Format(time.RFC3339)})
	h.now = h.now.Add(2 * time.Minute)
	h.mailSent("pm-pogo", "pe00c", "fresh")
	w.Poll()
	var got []string
	for _, e := range h.events {
		if e.typ == EventPointerSent {
			got = append(got, e.d["msg_ids"].([]string)...)
		}
	}
	if strings.Join(got, ",") != "m1,fresh" {
		t.Fatalf("pointed ids = %v, want m1 then fresh only", got)
	}
	if h.count(EventSighted) != 1 {
		t.Errorf("no sighted event after the file came back")
	}
}

func TestAbsentFileAtStartIsBlindNotEmpty(t *testing.T) {
	h := newHarness(t)
	os.Remove(h.path)
	w := h.watcher()
	w.Start()
	if w.Blind() == "" {
		t.Fatal("absent file at start is not blind")
	}
	h.mailSent("mayor", "pe00c", "m1") // file appears
	w.Poll()
	if w.Blind() != "" || len(h.nudges) != 1 {
		t.Fatalf("blind=%q nudges=%v", w.Blind(), h.nudges)
	}
}

func TestPartialLineIsNotConsumed(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	f, _ := os.OpenFile(h.path, os.O_APPEND|os.O_WRONLY, 0o644)
	line := fmt.Sprintf(`{"type":"mail.sent","from":"mayor","to":"pe00c","msg_id":"m1","ts":%q}`, h.now.Format(time.RFC3339))
	f.WriteString(line[:20])
	w.Poll()
	f.WriteString(line[20:] + "\n")
	f.Close()
	w.Poll()
	if len(h.nudges) != 1 {
		t.Fatalf("nudges = %v", h.nudges)
	}
}

func TestReadSubject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "e00c", "new")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "m1"), []byte("Message-Id: m1\nFrom: mayor\nSubject: hello there\n\nbody\nSubject: not me\n"), 0o644)
	if got := ReadSubject(root, "mg-e00c", "m1"); got != "hello there" {
		t.Errorf("subject = %q", got)
	}
	if got := ReadSubject(root, "e00c", "../x"); got != "" {
		t.Errorf("path traversal read %q", got)
	}
}

// --- the shadow join --------------------------------------------------------

func ev(at time.Time, typ string, d map[string]any) events.Event {
	return events.Event{Timestamp: at.Format(time.RFC3339Nano), EventType: typ, Agent: "pogod", Details: d}
}

func TestCheckFindsMissesAndExplainsTheRest(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "events.jsonl")
	t0 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	lines := []map[string]any{
		{"type": "mail.sent", "from": "mayor", "to": "pe00c", "msg_id": "pre", "ts": t0.Add(-time.Hour)},
		{"type": "mail.sent", "from": "mayor", "to": "pe00c", "msg_id": "cov", "ts": t0.Add(time.Minute)},
		{"type": "mail.sent", "from": "mayor", "to": "e00c", "msg_id": "miss", "ts": t0.Add(2 * time.Minute)},
		{"type": "mail.sent", "from": "pe00c", "to": "pe00c", "msg_id": "self", "ts": t0.Add(3 * time.Minute)},
		{"type": "mail.read", "mailbox": "pe00c", "msg_id": "pre", "ts": t0.Add(11 * time.Minute)},
		{"type": "mail.read", "mailbox": "pe00c", "msg_id": "cov", "ts": t0.Add(11 * time.Minute)},
		{"type": "mail.read", "mailbox": "e00c", "msg_id": "miss", "ts": t0.Add(11 * time.Minute)},
		{"type": "mail.read", "mailbox": "pe00c", "msg_id": "self", "ts": t0.Add(11 * time.Minute)},
		// Read with no timer fire nearby: not timer-driven, not judged.
		{"type": "mail.read", "mailbox": "mayor", "msg_id": "x", "ts": t0.Add(11 * time.Minute)},
	}
	var b strings.Builder
	for _, l := range lines {
		l["ts"] = l["ts"].(time.Time).Format(time.RFC3339)
		j, _ := json.Marshal(l)
		b.Write(append(j, '\n'))
	}
	os.WriteFile(store, []byte(b.String()), 0o644)

	pogo := []events.Event{
		ev(t0, EventArmed, map[string]any{}),
		ev(t0.Add(time.Minute+time.Second), EventPointerSent, map[string]any{"msg_ids": []any{"cov"}, "outcome": "queued", "text_len": 60.0}),
		ev(t0.Add(3*time.Minute), EventPointerSkipped, map[string]any{"msg_id": "self", "reason": "self-sent"}),
		ev(t0.Add(10*time.Minute), "scheduler_fire_delivered", map[string]any{"schedule_id": "mail-check-mg-e00c", "to": "pe00c"}),
	}
	rep := Check(pogo, store, t0.Add(-2*time.Hour), t0.Add(time.Hour))
	if rep.Reads != 4 {
		t.Fatalf("reads = %d, want 4 (rows %+v)", rep.Reads, rep.Rows)
	}
	want := map[string]string{"pre": ClassPreArm, "cov": ClassCovered, "miss": ClassMiss, "self": ClassSkipped}
	for _, r := range rep.Rows {
		if want[r.MsgID] != r.Class {
			t.Errorf("%s: class %s, want %s", r.MsgID, r.Class, want[r.MsgID])
		}
	}
	if m := rep.Misses(); len(m) != 1 || m[0].MsgID != "miss" {
		t.Errorf("misses = %+v", m)
	}
	var out strings.Builder
	rep.Render(&out)
	if !strings.Contains(out.String(), "MISSES: 1") || !strings.Contains(out.String(), "miss box=e00c") {
		t.Errorf("render:\n%s", out.String())
	}
}

func TestCheckAbsentStoreIsBlind(t *testing.T) {
	rep := Check(nil, filepath.Join(t.TempDir(), "nope.jsonl"), time.Now().Add(-time.Hour), time.Now())
	if len(rep.Blind) == 0 {
		t.Fatal("absent store reported as clean")
	}
	var out strings.Builder
	rep.Render(&out)
	if !strings.Contains(out.String(), "not a clean gate") {
		t.Errorf("render:\n%s", out.String())
	}
}

// Mail that arrived while pogod was down is pointed during Start's replay, and
// the armed event must precede that pointer: the shadow join classes anything
// sent before the first armed event as PRE-ARM.
func TestMailDuringDowntimeIsPointedAfterArmed(t *testing.T) {
	h := newHarness(t)
	w := h.watcher()
	w.Start()
	h.mailSent("mayor", "pm-pogo", "down")
	h.events = nil
	w2 := h.watcher()
	w2.Start()
	if len(h.nudges) != 1 || h.nudges[0].agent != "pm-pogo" {
		t.Fatalf("downtime mail not pointed: %+v", h.nudges)
	}
	if len(h.events) < 2 || h.events[0].typ != EventArmed || h.events[0].d["resume"] != "resumed" || h.events[1].typ != EventPointerSent {
		t.Fatalf("event order = %+v", h.events)
	}
}
