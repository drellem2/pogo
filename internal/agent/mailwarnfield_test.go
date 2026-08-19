package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hookarm"
)

// The per-agent arming state on the record every fleet view already reads.
//
// mg-d924's dead-recipient warning is a harness hook installed at spawn, which
// means "is it in force?" is a fact about each live PROCESS, and the only way
// to ask was to run `--self-check` inside one agent at a time. These tests pin
// the property that made that unacceptable: the answer has to exist without
// anyone remembering to ask, and it must never round a non-answer up to
// "armed" (mg-503d).

func registerHook(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/pogo hook mail-recipient"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, hookarm.SettingsRelPath), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInfoCarriesTheArmingStateOfARunningAgent(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{Name: "arm-test", Status: StatusRunning, Dir: dir, StartTime: time.Now()}

	if got := agentInfo(a); got.MailWarn != string(hookarm.StateOff) {
		t.Fatalf("MailWarn = %q, want %q — no hook is registered here", got.MailWarn, hookarm.StateOff)
	}

	registerHook(t, dir)
	if got := agentInfo(a); got.MailWarn != string(hookarm.StatePending) {
		t.Fatalf("MailWarn = %q, want %q — a registration alone is not arming", got.MailWarn, hookarm.StatePending)
	}

	if err := hookarm.RecordFire(dir); err != nil {
		t.Fatal(err)
	}
	future := a.StartTime.Add(time.Minute)
	if err := os.Chtimes(hookarm.StampPath(dir), future, future); err != nil {
		t.Fatal(err)
	}
	got := agentInfo(a)
	if got.MailWarn != string(hookarm.StateArmed) {
		t.Fatalf("MailWarn = %q, want %q", got.MailWarn, hookarm.StateArmed)
	}
	if got.MailWarnDetail == "" {
		t.Error("a state with no reason gets argued with; MailWarnDetail is empty")
	}
}

// TestOnlyARunningAgentGetsAnArmingState: an exited entry has no session for a
// hook to be loaded into, so any value here would be a claim about a process
// that is not there.
func TestOnlyARunningAgentGetsAnArmingState(t *testing.T) {
	dir := t.TempDir()
	registerHook(t, dir)
	a := &Agent{Name: "gone", Status: StatusExited, Dir: dir, StartTime: time.Now(), ExitTime: time.Now()}
	if got := agentInfo(a); got.MailWarn != "" {
		t.Fatalf("an exited agent reported MailWarn = %q", got.MailWarn)
	}
}

// TestTheFieldIsOmittedRatherThanFalsified pins the wire shape an older CLI
// meets. `mail_warn` absent has to mean "this daemon did not measure" — the CLI
// renders that as ?, not as clear — so the field must never serialize as a
// value that could be mistaken for a measurement.
func TestTheFieldIsOmittedRatherThanFalsified(t *testing.T) {
	a := &Agent{Name: "nodir", Status: StatusExited, StartTime: time.Now(), ExitTime: time.Now()}
	blob, err := json.Marshal(agentInfo(a))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "mail_warn") {
		t.Fatalf("unmeasured agent serialized a mail_warn key: %s", blob)
	}
}

// TestAnAgentWithNoRecordedDirectoryIsUnknown. pogod cannot look where it does
// not know to look, and "I cannot tell" must not be spelled the same way as
// "nothing is wrong".
func TestAnAgentWithNoRecordedDirectoryIsUnknown(t *testing.T) {
	a := &Agent{Name: "nodir", Status: StatusRunning, StartTime: time.Now()}
	if got := agentInfo(a); got.MailWarn != string(hookarm.StateUnknown) {
		t.Fatalf("MailWarn = %q, want %q", got.MailWarn, hookarm.StateUnknown)
	}
}
