package mailwarn

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

func TestRecipientsFindsTheRealSendShapes(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{
			// The exact command from the mg-d924 probe.
			name: "the incident's own probe",
			cmd:  `mg mail send doctor --from=mayor --subject="liveness probe — ignore" --body="probe"`,
			want: []string{"doctor"},
		},
		{
			name: "absolute path to mg",
			cmd:  `/Users/daniel/go/bin/mg mail send doctor --from=pd924 --body=hi`,
			want: []string{"doctor"},
		},
		{
			name: "flag with a separated value before the recipient",
			cmd:  `mg mail send --from mayor doctor --body=hi`,
			want: []string{"doctor"},
		},
		{
			name: "--create before the recipient",
			cmd:  `mg mail send --create newagent --from=me --body=hi`,
			want: []string{"newagent"},
		},
		{
			name: "chained sends",
			cmd:  `mg mail send doctor --from=me --body=a && mg mail send pa --from=me --body=b`,
			want: []string{"doctor", "pa"},
		},
		{
			name: "the same recipient twice reports once",
			cmd:  `mg mail send doctor --from=me --body=a; mg mail send doctor --from=me --body=b`,
			want: []string{"doctor"},
		},
		{
			// The canonical body-file form from `mg mail send --help`. The
			// heredoc body is prose and must not become a second recipient.
			name: "quoted heredoc",
			cmd: "mg mail send mayor --from=pd924 --body-file - <<'EOF'\n" +
				"subject line\n\nI already ran mg mail send doctor about this.\nEOF",
			want: []string{"mayor"},
		},
		{
			name: "line continuation",
			cmd:  "mg mail send doctor \\\n  --from=me \\\n  --body=hi",
			want: []string{"doctor"},
		},
		{
			name: "trailing pipe",
			cmd:  `mg mail send doctor --from=me --body=hi | tee /dev/null`,
			want: []string{"doctor"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Recipients(tc.cmd)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Recipients(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestRecipientsDeclinesRatherThanGuesses is the half that keeps the warning
// worth reading. A line that names the wrong agent trains the reader to skip
// it, which is a worse outcome than the silence this package exists to break.
func TestRecipientsDeclinesRatherThanGuesses(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"not a send at all", `mg mail list doctor`},
		{"mg new, not mail", `mg new "warn at send time" --repo=/x`},
		{"a different tool's mail", `gt mail send mayor/ -s hi -m there`},
		{"recipient behind a variable", `mg mail send "$AGENT" --from=me --body=hi`},
		{"recipient behind a command substitution", "mg mail send $(cat /tmp/who) --from=me --body=hi"},
		{"send with nothing after it", `mg mail send`},
		{"only flags after send", `mg mail send --from=me`},
		{"prose about a send, single-quoted", `echo 'run mg mail send doctor next'`},
		{"the word mg inside another word", `mgmail mail send doctor`},
		{"empty", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Recipients(tc.cmd); len(got) != 0 {
				t.Fatalf("Recipients(%q) = %v, want none", tc.cmd, got)
			}
		})
	}
}

func roster(members ...agent.RosterMember) *agent.RosterReport {
	rep := &agent.RosterReport{Configured: len(members), Members: members}
	for _, m := range members {
		if m.Absent() {
			rep.Absent = append(rep.Absent, m)
		}
	}
	return rep
}

func TestWarnNamesAnAbsentAgentAndItsClass(t *testing.T) {
	rep := roster(agent.RosterMember{
		Name: "doctor", State: agent.RosterAbsent, Class: agent.RosterOnDemand,
	})
	got := Warn([]string{"doctor"}, rep)
	if got == "" {
		t.Fatal("no warning for a send to an absent configured agent")
	}
	for _, want := range []string{"doctor", "NOT RUNNING", "auto_start = false", "on-demand"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning does not mention %q:\n%s", want, got)
		}
	}
	// It must not read as a refusal: mail queued for an on-demand agent that
	// will be started later is legitimate, and the message says so.
	if !strings.Contains(got, "SUCCEEDED") {
		t.Errorf("warning does not say the send succeeded:\n%s", got)
	}
}

func TestWarnCoversParkedBecauseParkedAlsoReadsNothing(t *testing.T) {
	rep := roster(agent.RosterMember{Name: "pa", State: agent.RosterParked, Class: agent.RosterSupervised})
	got := Warn([]string{"pa"}, rep)
	if !strings.Contains(got, "PARKED") || !strings.Contains(got, "pogo agent wake pa") {
		t.Fatalf("parked recipient not warned about with its remedy:\n%s", got)
	}
}

// TestWarnIsSilentForEveryStateThatIsNotSilentAlready is the anti-noise half.
// Silence from this package is a positive claim — "I looked, and everyone you
// wrote to is reachable" — so each of these must produce exactly nothing.
func TestWarnIsSilentForEveryStateThatIsNotSilentAlready(t *testing.T) {
	rep := roster(
		agent.RosterMember{Name: "mayor", State: agent.RosterPresent, Class: agent.RosterSupervised, Alive: true},
		// Present-but-dead is ALREADY a row in `pogo agent list` (status=exited),
		// and it is the state a restart_on_crash agent passes through on its way
		// back up. Warning on it would cry wolf at the worst moment.
		agent.RosterMember{Name: "architect", State: agent.RosterPresent, Class: agent.RosterSupervised, Alive: false},
	)
	for _, to := range []string{
		"mayor",     // running
		"architect", // present, registry says not alive
		"human",     // a mailbox, not an agent pogod configures
		"mg-d924",   // a work-item box
		"pd924",     // a polecat: not in the CONFIGURED crew roster
	} {
		if got := Warn([]string{to}, rep); got != "" {
			t.Errorf("Warn(%q) spoke when it should have been silent:\n%s", to, got)
		}
	}
	if got := Warn(nil, rep); got != "" {
		t.Errorf("Warn with no recipients spoke:\n%s", got)
	}
	if got := Warn([]string{"doctor"}, nil); got != "" {
		t.Errorf("Warn with no roster spoke:\n%s", got)
	}
}

func TestWarnNamesEveryDeadRecipientInOneCommand(t *testing.T) {
	rep := roster(
		agent.RosterMember{Name: "doctor", State: agent.RosterAbsent, Class: agent.RosterOnDemand},
		agent.RosterMember{Name: "scribe", State: agent.RosterAbsent, Class: agent.RosterSupervised},
		agent.RosterMember{Name: "mayor", State: agent.RosterPresent, Alive: true},
	)
	got := Warn([]string{"doctor", "mayor", "scribe"}, rep)
	if !strings.Contains(got, "doctor") || !strings.Contains(got, "scribe") {
		t.Fatalf("not every dead recipient named:\n%s", got)
	}
	if strings.Contains(got, "mayor") {
		t.Fatalf("live recipient named in the warning:\n%s", got)
	}
}

// TestUnavailableDoesNotBorrowSilencesMeaning: a roster that could not be read
// is the failure mode this whole item is about, one level up — an instrument
// that goes quiet when it breaks is indistinguishable from one reporting all
// clear.
func TestUnavailableDoesNotBorrowSilencesMeaning(t *testing.T) {
	got := Unavailable([]string{"doctor"}, errors.New("connection refused"))
	if !strings.Contains(got, "doctor") || !strings.Contains(got, "connection refused") {
		t.Fatalf("unavailable note does not name the recipient and the reason:\n%s", got)
	}
	if got := Unavailable(nil, errors.New("connection refused")); got != "" {
		t.Fatalf("unavailable note spoke with no recipients:\n%s", got)
	}
}

// TestHeredocBodiesAreNotCommands: the fleet's canonical send form puts a prose
// body inside <<'EOF', and prose about mail is full of the words this package
// scans for.
func TestHeredocBodiesAreNotCommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{
			name: "unquoted delimiter",
			cmd:  "mg mail send mayor --from=me --body-file - <<EOF\nmg mail send doctor\nEOF",
			want: []string{"mayor"},
		},
		{
			name: "tab-stripping <<-",
			cmd:  "mg mail send mayor --from=me --body-file - <<-'EOF'\n\tmg mail send doctor\n\tEOF",
			want: []string{"mayor"},
		},
		{
			name: "double-quoted delimiter",
			cmd:  "mg mail send mayor --from=me --body-file - <<\"EOF\"\nmg mail send doctor\nEOF",
			want: []string{"mayor"},
		},
		{
			name: "a send AFTER the heredoc is still found",
			cmd: "mg mail send mayor --from=me --body-file - <<'EOF'\nbody\nEOF\n" +
				"mg mail send doctor --from=me --body=hi",
			want: []string{"mayor", "doctor"},
		},
		{
			name: "unterminated heredoc swallows the rest",
			cmd:  "mg mail send mayor --from=me --body-file - <<'EOF'\nmg mail send doctor",
			want: []string{"mayor"},
		},
		{
			name: "here-string is not a heredoc",
			cmd:  "mg mail send doctor --from=me --body-file - <<<hi",
			want: []string{"doctor"},
		},
		{
			name: "a literal << inside quotes is not a redirection",
			cmd:  `mg mail send doctor --from=me --body="see <<EOF in the docs"`,
			want: []string{"doctor"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Recipients(tc.cmd)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Recipients(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestRecipientsSurvivesHostileInput: this runs after every Bash tool call an
// agent makes, so it sees every command line in the fleet. A panic here would
// be a parser taking down the hook that reports on it.
func TestRecipientsSurvivesHostileInput(t *testing.T) {
	for _, cmd := range []string{
		`'`, `"`, `\`, `<<`, `<<-`, `<<''`, `mg mail send "`, `mg mail send '`,
		"mg mail send x <<'EOF'", "<<<", `$(`, `mg mail send $(`, "\x00mg mail send doctor",
		strings.Repeat("mg mail send ", 200),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Recipients(%q) panicked: %v", cmd, r)
				}
			}()
			Recipients(cmd)
		}()
	}
}
