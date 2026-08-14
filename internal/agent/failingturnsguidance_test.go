package agent

import (
	"strings"
	"testing"
)

// mg-c058. The rendering fix is only half the remedy: the prompts that TELL a
// coordinator what `failing_turns` means enumerated only PERSISTENT causes —
// "an expired credential, a rate limit, a spend cap" — so a reader arrived
// expecting one and found a network fault. Every downstream conclusion followed
// from that enumeration, including nine days of mg-fb29 parked on `human` as a
// credential question against a credential that was valid the whole time.
//
// These pin the two corrections in both prompts: the token is a count over a
// window, and the reasons split by who has to act.

func embeddedPrompt(t *testing.T, path string) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile(path)
	if err != nil {
		t.Fatalf("read embedded %s: %v", path, err)
	}
	return string(data)
}

func failingTurnsGuidance(t *testing.T, path string) string {
	t.Helper()
	body := embeddedPrompt(t, path)
	i := strings.Index(body, `health: "failing_turns"`)
	if i < 0 {
		t.Fatalf("%s has no failing_turns guidance at all", path)
	}
	// The guidance runs to the end of its bullet; the next top-level bullet
	// starts the unavailable case.
	rest := body[i:]
	if j := strings.Index(rest, "\n- `transcript_check.state:"); j > 0 {
		rest = rest[:j]
	}
	return rest
}

func TestFailingTurnsGuidance_SaysItIsACountOverAWindow(t *testing.T) {
	for _, path := range []string{"prompts/mayor.md", "prompts/crew/doctor.md"} {
		g := failingTurnsGuidance(t, path)
		if !strings.Contains(g, "COUNT over a trailing window") {
			t.Errorf("%s: the failing_turns guidance does not say the token is a count over a "+
				"trailing window — read as a present-tense capacity claim it flagged seven agents "+
				"that were all completing turns, including the one that ran the query (mg-c058)", path)
		}
		if !strings.Contains(g, "health_detail") {
			t.Errorf("%s: the guidance does not point the reader at health_detail, which is where "+
				"the window now lives", path)
		}
	}
}

// The enumeration is the load-bearing sentence: it is what a reader uses to
// decide what kind of thing is wrong. Leaving server_error out of it makes
// every listed cause persistent and human-actionable, which is neither.
func TestFailingTurnsGuidance_EnumeratesServerErrorAlongsideTheCredentialCauses(t *testing.T) {
	for _, path := range []string{"prompts/mayor.md", "prompts/crew/doctor.md"} {
		g := failingTurnsGuidance(t, path)
		if !strings.Contains(g, "server_error") {
			t.Errorf("%s: the failing_turns guidance still names only persistent causes; "+
				"server_error is a network/provider fault that produces the identical fleet-wide "+
				"reading and needs nobody (mg-c058)", path)
		}
		if !strings.Contains(g, "credential") {
			t.Errorf("%s: the guidance dropped the credential cause, which is the founding one "+
				"(mg-18d0)", path)
		}
	}
}

// The correction that cost nine days: a fleet-wide reading is what a network
// fault looks like, not evidence that a shared credential is involved.
func TestFailingTurnsGuidance_WarnsAgainstReportingServerErrorAsCredential(t *testing.T) {
	for _, path := range []string{"prompts/mayor.md", "prompts/crew/doctor.md"} {
		g := failingTurnsGuidance(t, path)
		if !strings.Contains(g, "credential problem") {
			t.Errorf("%s: nothing warns against reporting a server_error episode as a credential "+
				"problem — that reading held mg-fb29 on `human` for nine days", path)
		}
	}
}
