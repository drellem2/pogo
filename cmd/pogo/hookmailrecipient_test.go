package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/claude"
)

func withMailRoster(t *testing.T, rep *agent.RosterReport, err error) {
	t.Helper()
	prev := mailRecipientRosterFn
	mailRecipientRosterFn = func() (*agent.RosterReport, error) { return rep, err }
	t.Cleanup(func() { mailRecipientRosterFn = prev })
}

func payload(t *testing.T, tool, command string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       tool,
		"tool_input":      map[string]any{"command": command},
		"tool_response":   map[string]any{"stdout": "Delivered: mayor -> doctor/new/1\n"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(b)
}

func absentDoctor() *agent.RosterReport {
	m := agent.RosterMember{Name: "doctor", State: agent.RosterAbsent, Class: agent.RosterOnDemand}
	return &agent.RosterReport{Configured: 1, Members: []agent.RosterMember{m}, Absent: []agent.RosterMember{m}}
}

// TestHookWarnsOnTheIncidentsOwnCommand replays the probe recorded in mg-d924 —
// the send whose output was byte-for-byte a send to a live agent.
func TestHookWarnsOnTheIncidentsOwnCommand(t *testing.T) {
	withMailRoster(t, absentDoctor(), nil)
	got := runMailRecipientHook(strings.NewReader(payload(t, "Bash",
		`mg mail send doctor --from=mayor --subject="liveness probe — ignore" --body="probe"`)))
	if !strings.Contains(got, "doctor") || !strings.Contains(got, "NOT RUNNING") {
		t.Fatalf("no warning for the incident's own send:\n%s", got)
	}
}

func TestHookIsSilentWhenThereIsNothingToSay(t *testing.T) {
	withMailRoster(t, absentDoctor(), nil)
	cases := []struct{ name, tool, cmd string }{
		{"a command with no send", "Bash", "go test ./..."},
		{"a send to a live name", "Bash", "mg mail send mayor --from=me --body=hi"},
		{"a mail list, not a send", "Bash", "mg mail list doctor"},
		{"another tool entirely", "Read", `mg mail send doctor --from=me --body=hi`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runMailRecipientHook(strings.NewReader(payload(t, tc.tool, tc.cmd))); got != "" {
				t.Fatalf("hook spoke when it should not have:\n%s", got)
			}
		})
	}
}

// TestHookSpeaksUpWhenItCouldNotCheck: silence from this hook means "checked,
// the recipient is reachable". A roster pogod could not compute must not be
// able to borrow that meaning — that is the same silent-failure shape the hook
// exists to remove, one level up.
func TestHookSpeaksUpWhenItCouldNotCheck(t *testing.T) {
	withMailRoster(t, nil, errors.New("connection refused"))
	got := runMailRecipientHook(strings.NewReader(payload(t, "Bash",
		"mg mail send doctor --from=me --body=hi")))
	if !strings.Contains(got, "doctor") || !strings.Contains(got, "connection refused") {
		t.Fatalf("a failed roster read went unreported:\n%q", got)
	}
	// And it must stay quiet when no send was in the command, even though the
	// roster is just as broken: a hook that fires on every Bash call is a hook
	// nobody reads.
	if got := runMailRecipientHook(strings.NewReader(payload(t, "Bash", "ls"))); got != "" {
		t.Fatalf("a broken roster leaked into an unrelated command:\n%s", got)
	}
}

// TestHookNeverFailsOnMalformedInput: it runs after every Bash tool call in the
// fleet. Anything it cannot read is silence, never an error.
func TestHookNeverFailsOnMalformedInput(t *testing.T) {
	withMailRoster(t, absentDoctor(), nil)
	for _, in := range []string{"", "{", "null", "[]", `{"tool_input": 3}`, `{"tool_name": "Bash"}`} {
		if got := runMailRecipientHook(strings.NewReader(in)); got != "" {
			t.Errorf("malformed payload %q produced output:\n%s", in, got)
		}
	}
}

// TestEmitHookContextIsTheHarnessEnvelope pins the one thing that decides
// whether any of this reaches the agent at all.
func TestEmitHookContextIsTheHarnessEnvelope(t *testing.T) {
	var buf bytes.Buffer
	emitHookContext(&buf, "warning text")

	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("hook output is not JSON: %v\n%s", err, buf.String())
	}
	if out.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", out.HookSpecificOutput.HookEventName)
	}
	if out.HookSpecificOutput.AdditionalContext != "warning text" {
		t.Errorf("additionalContext = %q", out.HookSpecificOutput.AdditionalContext)
	}

	// Silence must be BYTES-EMPTY, not an empty envelope: a harness handed
	// `{}` on every tool call has to parse and discard it, and any output at
	// all shows up in hook debugging as this hook having had something to say.
	buf.Reset()
	emitHookContext(&buf, "")
	if buf.Len() != 0 {
		t.Errorf("silence wrote %q", buf.String())
	}
}

// TestSelfCheckSeparatesNoWarningFromNoHook is the test for the property the
// remedy needed to have about itself: a fix for an invisible failure must not
// be able to fail invisibly.
func TestSelfCheckSeparatesNoWarningFromNoHook(t *testing.T) {
	withMailRoster(t, absentDoctor(), nil)

	dir := t.TempDir()
	var buf bytes.Buffer
	if code := mailRecipientSelfCheck(&buf, dir); code == 0 {
		t.Errorf("self-check passed with no hook registered:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "hook registration: NO") {
		t.Errorf("self-check did not name the missing registration:\n%s", buf.String())
	}

	if err := claude.InstallMailRecipientHook(dir, "/bin/pogo hook mail-recipient"); err != nil {
		t.Fatalf("InstallMailRecipientHook: %v", err)
	}
	buf.Reset()
	if code := mailRecipientSelfCheck(&buf, dir); code != 0 {
		t.Errorf("self-check failed with the hook registered and the roster readable:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "hook registration: yes") ||
		!strings.Contains(buf.String(), "doctor would be warned about") {
		t.Errorf("self-check output is not evidence:\n%s", buf.String())
	}

	// A readable-but-empty roster is not an all-clear either.
	withMailRoster(t, &agent.RosterReport{}, nil)
	buf.Reset()
	if code := mailRecipientSelfCheck(&buf, dir); code == 0 {
		t.Errorf("self-check passed on an empty roster:\n%s", buf.String())
	}

	withMailRoster(t, nil, errors.New("connection refused"))
	buf.Reset()
	if code := mailRecipientSelfCheck(&buf, dir); code == 0 {
		t.Errorf("self-check passed with an unreadable roster:\n%s", buf.String())
	}
}
