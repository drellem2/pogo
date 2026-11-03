package alerts

import (
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z_]*\.[a-z][a-z_]*$`)

func TestRegistryDeclarationsAreWellFormed(t *testing.T) {
	seen := map[Name]bool{}
	for _, d := range registry {
		if !nameRE.MatchString(string(d.Name)) {
			t.Errorf("%q is not <family>.<kind> in lower snake case", d.Name)
		}
		if seen[d.Name] {
			t.Errorf("%q is declared twice", d.Name)
		}
		seen[d.Name] = true
		if d.Class != ClassAlert && d.Class != ClassDelivery {
			t.Errorf("%q has class %q", d.Name, d.Class)
		}
		if strings.TrimSpace(d.Default) == "" || strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%q needs both a Default recipient rule and a Summary", d.Name)
		}
	}
	if n := Name("ack_watch.finding"); n.Family() != "ack_watch" || n.Kind() != "finding" {
		t.Errorf("Family/Kind split %q as %q/%q", n, n.Family(), n.Kind())
	}
}

// Mail must hand the transport exactly what it was given and return exactly
// what the transport returned: slice 1 is no behaviour change.
func TestMailPassesThroughUnchanged(t *testing.T) {
	var got []string
	want := errSentinel{}
	err := Mail(AckWatchFinding, func(to, from, subject, body string) error {
		got = []string{to, from, subject, body}
		return want
	}, "mayor", "ack-watch", "subj", "body\nline")
	if err != want {
		t.Errorf("Mail returned %v, want the transport's own error", err)
	}
	if !reflect.DeepEqual(got, []string{"mayor", "ack-watch", "subj", "body\nline"}) {
		t.Errorf("transport got %q", got)
	}
	// An undeclared name is logged, never dropped.
	called := false
	_ = Mail(Name("nope.never"), func(string, string, string, string) error { called = true; return nil }, "a", "b", "c", "d")
	if !called {
		t.Error("an undeclared name stopped the send")
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }

// The argv each transport hands mg is pinned: they replaced client.SendMGMail
// and five private exec.Command calls, and must address mg identically.
func TestTransportArgv(t *testing.T) {
	var argv [][]string
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		argv = append(argv, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommand = old }()

	if err := Transport("mayor", "refinery", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := MailOutput(OrphanPolecat, "mayor", "pogod", "s", "b"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mg", "mail", "send", "mayor", "--from=refinery", "--subject=s", "--body=b"},
		{"mg", "mail", "send", "mayor", "--from", "pogod", "--subject", "s", "--body", "b"},
	}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv:\n got %q\nwant %q", argv, want)
	}
}
