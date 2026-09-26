package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// liveAPIKeyPrompt is Claude Code 2.1.283's "Detected a custom API key" prompt
// exactly as its PTY emitted it, in a sandbox HOME with onboarding complete,
// the workspace trusted, --settings suppressing the bypass warning, and a FAKE
// ANTHROPIC_API_KEY ending in FAKEKEYFORPOGOTEST01 (mg-2037). It is the whole
// capture — the prompt is the first thing drawn. Note the per-word column
// moves: stripped of escapes, "Detected a custom" reads "Detectedacustom".
var liveAPIKeyPrompt = "\x1b7\x1b[r\x1b8\x1b[?25h\x1b[?25l\x1b[?2004h\x1b[?2031h\x1b[?1004h\x0d\x0d\x0a\x1b[38;5;220m" +
	strings.Repeat("\xe2\x94\x80", 80) +
	"\x1b[39m\x0d\x0d\x0a\x1b[3G\x1b[38;5;220m\x1b[1mDetected\x1b[12Ga\x1b[14Gcustom\x1b[21GAPI\x1b[25Gkey\x1b[29Gin\x1b[32Gyour\x1b[37Genvironment\x1b[22m\x1b[39m\x0d\x0d\x0a\x0d\x0d\x0a\x1b[3G\x1b[1mANTHROPIC_API_KEY\x1b[22m:\x1b[22Gsk-ant-...FAKEKEYFORPOGOTEST01\x0d\x0d\x0a\x0d\x0d\x0a\x1b[3GDo\x1b[6Gyou\x1b[10Gwant\x1b[15Gto\x1b[18Guse\x1b[22Gthis\x1b[27GAPI\x1b[31Gkey?\x0d\x0d\x0a\x0d\x0d\x0a\x1b[5GYes\x0d\x0d\x0a\x1b[3G\x1b[38;5;153m\xe2\x9d\xaf\x1b[5GNo\x1b[8G(\x1b[1mrecommended\x1b[22m)\x1b[39m\x0d\x0d\x0a\x0d\x0d\x0a\x1b[3G\x1b[38;5;246m\x1b[3mEnter\x1b[9Gto\x1b[12Gconfirm\x1b[20G\xc2\xb7\x1b[22GEsc\x1b[26Gto\x1b[29Gcancel\x1b[23m\x1b[39m\x0d\x0d\x0a\x1b[2C\x1b[3A\x1b[>0q\x1b[?u\x1b[c"

func TestMatchesAPIKeyPromptOnTheLiveCapture(t *testing.T) {
	if !matchesAPIKeyPrompt([]byte(liveAPIKeyPrompt)) {
		t.Fatalf("the live 2.1.283 capture does not match; stripped:\n%s",
			agent.StripANSI([]byte(liveAPIKeyPrompt)))
	}
	// The two pre-composer screens must not be mistaken for each other: the
	// trust hook ANSWERS one and must never answer the other.
	if matchesAPIKeyPrompt([]byte(liveDialog)) {
		t.Error("the trust dialog matched the API-key marker")
	}
	if matchesTrustDialog([]byte(liveAPIKeyPrompt)) {
		t.Error("the API-key prompt matched the trust-dialog marker — the hook would try to answer it")
	}
	if composerReady([]byte(liveAPIKeyPrompt)) {
		t.Error("the API-key prompt reads as a ready composer")
	}
}

const fakeKey = "sk-ant-api03-" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "FAKEKEYFORPOGOTEST01"

func TestClassifyAPIKeyApproval(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		config  string
		readErr error
		want    APIKeyApproval
	}{
		{"no key", "", `{}`, nil, APIKeyUnset},
		{"whitespace key", "  ", `{}`, nil, APIKeyUnset},
		{"no config file is a fresh profile", fakeKey, "", fs.ErrNotExist, APIKeyPending},
		{"unreadable config", fakeKey, "", errors.New("permission denied"), APIKeyUnknown},
		{"malformed config", fakeKey, `{not json`, nil, APIKeyUnknown},
		{"malformed responses", fakeKey, `{"customApiKeyResponses":"x"}`, nil, APIKeyUnknown},
		{"no responses recorded", fakeKey, `{"hasCompletedOnboarding":true}`, nil, APIKeyPending},
		{"empty responses", fakeKey, `{"customApiKeyResponses":{"approved":[],"rejected":[]}}`, nil, APIKeyPending},
		{"approved", fakeKey, `{"customApiKeyResponses":{"approved":["FAKEKEYFORPOGOTEST01"],"rejected":[]}}`, nil, APIKeyApproved},
		{"rejected", fakeKey, `{"customApiKeyResponses":{"approved":[],"rejected":["FAKEKEYFORPOGOTEST01"]}}`, nil, APIKeyRejected},
		{"approved wins over rejected", fakeKey, `{"customApiKeyResponses":{"approved":["FAKEKEYFORPOGOTEST01"],"rejected":["FAKEKEYFORPOGOTEST01"]}}`, nil, APIKeyApproved},
		{"key is trimmed before its suffix is taken", " " + fakeKey + "\n", `{"customApiKeyResponses":{"approved":["FAKEKEYFORPOGOTEST01"]}}`, nil, APIKeyApproved},
		{"another key's answer", fakeKey, `{"customApiKeyResponses":{"approved":["SOMEOTHERKEYSUFFIX01"],"rejected":[]}}`, nil, APIKeyPending},
		{"the full key is not what is stored", fakeKey, `{"customApiKeyResponses":{"approved":["` + fakeKey + `"]}}`, nil, APIKeyPending},
		{"non-string entries are skipped", fakeKey, `{"customApiKeyResponses":{"approved":[1,null,"FAKEKEYFORPOGOTEST01"]}}`, nil, APIKeyApproved},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyAPIKeyApproval(c.key, []byte(c.config), c.readErr); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestClaudeGlobalConfigPath(t *testing.T) {
	none := func(string) string { return "" }
	if got := ClaudeGlobalConfigPath(none, "/h"); got != filepath.Join("/h", ".claude.json") {
		t.Errorf("default path = %q", got)
	}
	dir := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return "/cfg"
		}
		return ""
	}
	if got := ClaudeGlobalConfigPath(dir, "/h"); got != filepath.Join("/cfg", ".claude.json") {
		t.Errorf("CLAUDE_CONFIG_DIR path = %q", got)
	}
}

// TestAPIKeyApprovalLine: only a pending or unreadable answer warns, the
// pending warning carries the remedy, and no line can carry the key — the
// function is never handed it.
func TestAPIKeyApprovalLine(t *testing.T) {
	for state, want := range map[APIKeyApproval]string{
		APIKeyUnset: "pass", APIKeyApproved: "pass", APIKeyRejected: "pass",
		APIKeyPending: "warn", APIKeyUnknown: "warn",
	} {
		status, detail := APIKeyApprovalLine(state, "/h/.claude.json")
		if status != want {
			t.Errorf("state %v: status %q, want %q", state, status, want)
		}
		if detail == "" {
			t.Errorf("state %v: empty detail", state)
		}
	}
	_, pending := APIKeyApprovalLine(APIKeyPending, "/h/.claude.json")
	for _, want := range []string{"/h/.claude.json", "Detected a custom API key", "run `claude` once"} {
		if !strings.Contains(pending, want) {
			t.Errorf("pending detail lacks %q: %s", want, pending)
		}
	}
}

// TestCheckAPIKeyApprovalReadsTheEnvironment drives the doctor's entry point
// against a sandbox config dir: pending before an answer, approved after.
func TestCheckAPIKeyApprovalReadsTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", fakeKey)
	path := filepath.Join(dir, ".claude.json")

	if err := os.WriteFile(path, []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, p := CheckAPIKeyApproval(); got != APIKeyPending || p != path {
		t.Fatalf("got (%v, %q), want pending at %q", got, p, path)
	}
	if err := os.WriteFile(path, []byte(`{"customApiKeyResponses":{"approved":["FAKEKEYFORPOGOTEST01"],"rejected":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := CheckAPIKeyApproval(); got != APIKeyApproved {
		t.Fatalf("got %v after approval, want approved", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got, _ := CheckAPIKeyApproval(); got != APIKeyUnset {
		t.Fatalf("got %v with no key, want unset", got)
	}
}

// captureEvents swaps emitEvent for a recorder for the duration of a test.
func captureEvents(t *testing.T) func() []events.Event {
	t.Helper()
	var mu sync.Mutex
	var got []events.Event
	prev := emitEvent
	emitEvent = func(ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	}
	t.Cleanup(func() { emitEvent = prev })
	return func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event(nil), got...)
	}
}

// apiKeyPromptScript draws the live capture from a file, then reads single
// keys and reports each one, so a test can prove the hook sent nothing.
func apiKeyPromptScript(t *testing.T, screen string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "screen")
	if err := os.WriteFile(f, []byte(screen), 0o644); err != nil {
		t.Fatal(err)
	}
	return "stty raw -echo\ncat '" + f + "'\nwhile :; do c=$(dd bs=1 count=1 2>/dev/null); printf '" + fixtureGotKey + "\\r\\n'; done\n"
}

// TestWatchNamesTheAPIKeyGateAndSendsNothing is the spawn-time fix: the watch
// sees the prompt, presses no key, holds the agent at the gate (so the nudge
// paths stand down), emits claude_api_key_prompt once, and ends its budget as
// trustWatchAPIKeyGate rather than as sentinel drift.
func TestWatchNamesTheAPIKeyGateAndSendsNothing(t *testing.T) {
	evs := captureEvents(t)
	a := spawnScripted(t, "apikey-gate", apiKeyPromptScript(t, liveAPIKeyPrompt))
	if !sawWithin(a, "FAKEKEYFORPOGOTEST01", 10*time.Second) {
		t.Fatalf("fixture never drew the prompt; PTY:\n%s", ptyText(a))
	}

	if o := runWatch(t, a, 1500*time.Millisecond); o != trustWatchAPIKeyGate {
		t.Fatalf("outcome = %v, want %v", o, trustWatchAPIKeyGate)
	}
	if strings.Contains(ptyText(a), fixtureGotKey) {
		t.Errorf("the hook sent a key to the API-key prompt — that is an answer to a billing question; PTY:\n%s", ptyText(a))
	}
	if g := a.PreComposerGate(); g != APIKeyGateName {
		t.Errorf("PreComposerGate = %q, want %q", g, APIKeyGateName)
	}
	var n int
	for _, ev := range evs() {
		if ev.EventType == "claude_api_key_prompt" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("claude_api_key_prompt emitted %d times, want exactly 1; events: %+v", n, evs())
	}
}

// TestWatchWithoutTheGateIsDrift is the POSITIVE CONTROL for the outcome: the
// same fixture drawing a screen that is neither dialog nor composer still
// spends its budget as drift, so the test above is measuring the marker and
// not a watch that never reports drift at all.
func TestWatchWithoutTheGateIsDrift(t *testing.T) {
	evs := captureEvents(t)
	a := spawnScripted(t, "apikey-none", apiKeyPromptScript(t, "some other screen\r\nEnter to confirm\r\n"))
	if !sawWithin(a, "Enter to confirm", 10*time.Second) {
		t.Fatalf("fixture never drew; PTY:\n%s", ptyText(a))
	}
	if o := runWatch(t, a, 1500*time.Millisecond); o != trustWatchDrift {
		t.Fatalf("outcome = %v, want %v", o, trustWatchDrift)
	}
	if g := a.PreComposerGate(); g != "" {
		t.Errorf("a gate was held with no prompt on screen: %q", g)
	}
	if len(evs()) != 0 {
		t.Errorf("events emitted with no prompt on screen: %+v", evs())
	}
}

// TestEchoedMentionAfterComposerIsNotTheGate: a kickoff prompt that merely
// mentions the gate (this work item's own title does) is echoed after the
// composer, and the composer check comes first.
func TestEchoedMentionAfterComposerIsNotTheGate(t *testing.T) {
	a := spawnScripted(t, "apikey-echo", apiKeyPromptScript(t,
		"? for shortcuts\r\nplease fix: Detected a custom API key stalls agents\r\n"))
	if !sawWithin(a, "stalls agents", 10*time.Second) {
		t.Fatalf("fixture never drew; PTY:\n%s", ptyText(a))
	}
	if o := runWatch(t, a, 1500*time.Millisecond); o != trustWatchConfirmed {
		t.Fatalf("outcome = %v, want %v", o, trustWatchConfirmed)
	}
	if g := a.PreComposerGate(); g != "" {
		t.Errorf("an echoed mention held the agent at a gate: %q", g)
	}
}
