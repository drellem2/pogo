package scheduler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mailCheckMsg is the message pogod actually registers for a polecat, rendered
// against an arbitrary mailbox. Keeping the fixture in the real shape (backticks
// and trailing prose included) is the point: the guard has to parse the message
// that ships, not a stripped-down one.
func mailCheckMsg(mailbox string) string {
	return "Check your mail with `mg mail list " + mailbox + "` and handle any unread messages — " +
		"act on any reviewer findings or re-review requests and mail your reply back; otherwise no-op."
}

func newTestScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestMailCheckMailbox(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
		wantOK  bool
	}{
		{"bare invocation", "Check your mail with mg mail list waa96 now", "waa96", true},
		{"backticked", mailCheckMsg("waa96"), "waa96", true},
		{"double quoted", `run "mg mail list waa96" please`, "waa96", true},
		{"trailing comma", "run mg mail list waa96, then reply", "waa96", true},
		{"trailing period at end", "run mg mail list waa96.", "waa96", true},
		{"flag before mailbox", "mg mail list --json waa96", "waa96", true},
		{"work-item form", "Check your mail with mg mail list mg-aa96 and handle it", "mg-aa96", true},
		{"unexpanded env var", "Check your mail with mg mail list $POGO_AGENT_NAME", "$POGO_AGENT_NAME", true},
		{"no invocation", "Check your mail and handle any unread messages.", "", false},
		{"different subcommand", "run mg mail send mayor --from=x", "", false},
		{"truncated invocation", "run mg mail list", "", false},
		{"flags only after list", "run mg mail list --json", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MailCheckMailbox(tc.message)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("MailCheckMailbox(%q) = (%q, %v), want (%q, %v)", tc.message, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestAddRefusesMailCheckPointedAtAnotherMailbox is the positive control for
// mg-aa96: it reconstructs the exact live mismatch — agent "waa96" working item
// "mg-aa96", the template's message deriving the mailbox from the work item —
// and requires registration to REFUSE. Before the guard this Add succeeded and
// the polecat then read an empty-looking inbox forever.
func TestAddRefusesMailCheckPointedAtAnotherMailbox(t *testing.T) {
	s := newTestScheduler(t)

	_, err := s.Add(Entry{
		Agent:   "waa96",
		ID:      MailCheckIDPrefix + "mg-aa96",
		Cron:    "*/10 * * * *",
		Message: mailCheckMsg("mg-aa96"), // resolves to mailbox "aa96", not "waa96"
	}, time.Now())
	if err == nil {
		t.Fatal("Add accepted a mail-check for waa96 that reads mailbox mg-aa96; the wrong-inbox poll is indistinguishable from an empty inbox, so nothing downstream can catch it")
	}
	for _, want := range []string{"waa96", "mg-aa96", "mg-aa96"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q so the reader can see which two identities disagree", err, want)
		}
	}
	if _, ok := s.Get("waa96", MailCheckIDPrefix+"mg-aa96"); ok {
		t.Error("refused entry must not be stored")
	}
}

// TestAddRefusesEveryLiveMismatch replays the whole fleet as measured on
// 2026-08-05. Every one of the eight registrations below was live and silent;
// each must now be refused. g109 is the extreme shape — its schedule was keyed
// on a work item it was not even working on, so it polled a mailbox belonging to
// something else entirely.
func TestAddRefusesEveryLiveMismatch(t *testing.T) {
	fleet := []struct{ agent, polled string }{
		{"gd2f0", "mg-d2f0"},
		{"wb468", "mg-b468"},
		{"o9d7b", "mg-9d7b"},
		{"wfc99", "mg-fc99"},
		{"wfc8d", "mg-fc8d"}, // held an unread mayor correction retracting a false premise
		{"g109", "mg-b4cc"},  // a different work item's mailbox entirely
		{"d0d70", "mg-0d70"},
		{"gc23c", "mg-c23c"}, // the polecat that reported this
	}
	for _, f := range fleet {
		t.Run(f.agent, func(t *testing.T) {
			s := newTestScheduler(t)
			if _, err := s.Add(Entry{
				Agent:   f.agent,
				ID:      MailCheckIDPrefix + f.polled,
				Cron:    "*/10 * * * *",
				Message: mailCheckMsg(f.polled),
			}, time.Now()); err == nil {
				t.Fatalf("Add accepted mail-check for %s pointed at %s", f.agent, f.polled)
			}
		})
	}
}

// TestAddAcceptsMailCheckOnItsOwnMailbox is the negative control the positive
// one is worthless without: the guard must be silent on every healthy shape,
// including the historically-agreeing case (agent name == work item id minus
// "mg-") that made the defect survive so long, and a message that names no
// mailbox at all — an agent with a correctly-pointed, genuinely empty inbox must
// never be blocked from registering.
func TestAddAcceptsMailCheckOnItsOwnMailbox(t *testing.T) {
	tests := []struct {
		name    string
		agent   string
		message string
	}{
		{"agent name verbatim", "waa96", mailCheckMsg("waa96")},
		{"agent name with mg- prefix, which mg strips", "waa96", mailCheckMsg("mg-waa96")},
		{"the historically-agreeing case", "d2f0", mailCheckMsg("mg-d2f0")},
		{"crew agent", "pm-pogo", mailCheckMsg("pm-pogo")},
		{"case difference only", "Doctor", mailCheckMsg("doctor")},
		{"no mailbox named", "waa96", "Check your mail and handle any unread messages."},
		{"empty message", "waa96", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScheduler(t)
			got, err := s.Add(Entry{
				Agent:   tc.agent,
				ID:      MailCheckIDPrefix + "mg-aa96",
				Cron:    "*/10 * * * *",
				Message: tc.message,
			}, time.Now())
			if err != nil {
				t.Fatalf("Add rejected a correctly-pointed mail-check: %v", err)
			}
			if got.Kind != KindMailCheck {
				t.Errorf("kind = %q, want %q", got.Kind, KindMailCheck)
			}
		})
	}
}

// TestAddRefusesUnexpandedAgentNameVariable covers the template's own failure
// mode. The prescribed command interpolates $POGO_AGENT_NAME in double quotes;
// single-quote it and the literal reaches the scheduler, which would poll a
// mailbox named "$POGO_AGENT_NAME". That is a mismatch like any other and must
// be refused rather than registered.
func TestAddRefusesUnexpandedAgentNameVariable(t *testing.T) {
	s := newTestScheduler(t)
	if _, err := s.Add(Entry{
		Agent:   "waa96",
		ID:      MailCheckIDPrefix + "mg-aa96",
		Cron:    "*/10 * * * *",
		Message: mailCheckMsg("$POGO_AGENT_NAME"),
	}, time.Now()); err == nil {
		t.Fatal("Add accepted a mail-check whose mailbox is a literal, unexpanded $POGO_AGENT_NAME")
	}
}

// TestAddIgnoresNonMailCheckKinds bounds the guard. A sweep, a gate-lift, or a
// deliberately-named watcher may legitimately point an agent at somebody else's
// mailbox; only KindMailCheck — the kind that claims to be THIS agent's own
// reachability channel — carries the invariant. This is also the documented
// escape hatch: name it something other than mail-check-* and it is yours to
// aim wherever you like.
func TestAddIgnoresNonMailCheckKinds(t *testing.T) {
	for _, id := range []string{"sweep-morning-mayor", "gate-lift-mg-aa96", "watch-aa96"} {
		t.Run(id, func(t *testing.T) {
			s := newTestScheduler(t)
			got, err := s.Add(Entry{
				Agent:   "waa96",
				ID:      id,
				Cron:    "*/10 * * * *",
				Message: mailCheckMsg("somebody-else"),
			}, time.Now())
			if err != nil {
				t.Fatalf("Add rejected %s (kind %q), which the guard must not police: %v", id, got.Kind, err)
			}
			if got.Kind == KindMailCheck {
				t.Fatalf("%s classified as %q; the fixture no longer tests a non-mail-check kind", id, got.Kind)
			}
		})
	}
}

// TestAddRefusesExplicitKindMailCheckRegardlessOfID closes the naming loophole:
// the guard keys on Entry.Kind, so a caller that sets KindMailCheck explicitly
// (pogod's spawn registrar does) is checked even if its id would infer to
// something else. Kind is what a schedule IS; the id is only what it is called
// (mg-fa53).
func TestAddRefusesExplicitKindMailCheckRegardlessOfID(t *testing.T) {
	s := newTestScheduler(t)
	if _, err := s.Add(Entry{
		Agent:   "waa96",
		ID:      "inbox-poll",
		Kind:    KindMailCheck,
		Cron:    "*/10 * * * *",
		Message: mailCheckMsg("aa96"),
	}, time.Now()); err == nil {
		t.Fatal("Add accepted an explicitly-KindMailCheck entry pointed at another mailbox because its id lacked the mail-check- prefix")
	}
}
