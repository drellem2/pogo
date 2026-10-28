package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// drellem2/pogo#232, #236: a declared pointer wake mode, honoured by diagnose,
// deafwatch and check-mailloops through ONE predicate (mailLoopExclusionFor).
// deafwatch's own end-to-end case is in internal/deafwatch; check-mailloops'
// render is in cmd/pogo. Every case here pairs a declared agent with an
// undeclared one, so a build that excluded everybody fails as loudly as one
// that excluded nobody.

// writeCrewPromptWith writes a crew prompt with auto_start plus extra
// frontmatter lines into the sandbox sandboxDesiredState created.
func writeCrewPromptWith(t *testing.T, name string, autoStart bool, extra string) string {
	t.Helper()
	body := "+++\nauto_start = " + map[bool]string{true: "true", false: "false"}[autoStart] + "\n" + extra + "+++\n# " + name + "\n"
	path := filepath.Join(CrewPromptDir(), name+".md")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return path
}

// setWakeDefault sets the [agents] wake install default for one test.
func setWakeDefault(t *testing.T, raw string) {
	t.Helper()
	_ = SetDefaultWakeMode(raw)
	t.Cleanup(func() { _ = SetDefaultWakeMode("") })
}

// fakeEventContext is a DiagnoseEventContext with canned answers, recording
// what it was asked.
type fakeEventContext struct {
	removal    EventContext
	unconsumed EventContext
	asked      []string
}

func (f *fakeEventContext) LastScheduleRemoval(id string) EventContext {
	f.asked = append(f.asked, "removal:"+id)
	return f.removal
}

func (f *fakeEventContext) LastWakeUnconsumed(name string) EventContext {
	f.asked = append(f.asked, "unconsumed:"+name)
	return f.unconsumed
}

func TestFrontmatter_WakeKeyIsRecognised(t *testing.T) {
	meta, _, err := parsePromptFrontmatterBytes([]byte("+++\nwake = \"pointer\"\n+++\nbody\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !meta.HasField("wake") || meta.Wake != "pointer" {
		t.Fatalf("meta = %+v, want wake declared as pointer", meta)
	}
	// An unrecognised VALUE must not take the rest of the block down with it
	// (the model: key's rule) — it is named at resolution instead.
	meta, _, err = parsePromptFrontmatterBytes([]byte("+++\nauto_start = true\nwake = \"poniter\"\n+++\n"))
	if err != nil {
		t.Fatalf("a bad wake value failed the whole frontmatter: %v", err)
	}
	if !meta.AutoStart {
		t.Error("auto_start lost to a bad wake value")
	}
}

func TestResolveWakeMode_PrecedenceBothDirections(t *testing.T) {
	sandboxDesiredState(t, "plain", true)
	writeCrewPromptWith(t, "fm-pointer", true, "wake = \"pointer\"\n")
	writeCrewPromptWith(t, "fm-mailcheck", true, "wake = \"mail-check\"\n")
	writeCrewPromptWith(t, "fm-typo", true, "wake = \"poniter\"\n")

	type want struct {
		mode   WakeMode
		source string
	}
	check := func(label string, cases map[string]want) {
		t.Helper()
		for name, w := range cases {
			d, err := ResolveWakeMode(name)
			if err != nil {
				t.Fatalf("%s: ResolveWakeMode(%s): %v", label, name, err)
			}
			if d.Mode != w.mode || !strings.Contains(d.Source, w.source) {
				t.Errorf("%s: %s = %+v, want mode %s with source containing %q", label, name, d, w.mode, w.source)
			}
		}
	}

	check("no config default", map[string]want{
		"plain":        {WakeMailCheck, "default"},
		"fm-pointer":   {WakePointer, "prompt frontmatter"},
		"fm-mailcheck": {WakeMailCheck, "prompt frontmatter"},
		"fm-typo":      {WakeMailCheck, `"poniter" is unrecognised`},
	})

	setWakeDefault(t, "pointer")
	check("[agents] wake = pointer", map[string]want{
		"plain":        {WakePointer, "config.toml [agents] wake"},
		"fm-pointer":   {WakePointer, "prompt frontmatter"},
		"fm-mailcheck": {WakeMailCheck, "prompt frontmatter"}, // override OFF
		"fm-typo":      {WakeMailCheck, `"poniter" is unrecognised`},
	})

	setWakeDefault(t, "mail-check")
	check("[agents] wake = mail-check", map[string]want{
		"plain":      {WakeMailCheck, "config.toml [agents] wake"},
		"fm-pointer": {WakePointer, "prompt frontmatter"}, // override ON
	})

	if err := SetDefaultWakeMode("sometimes"); err == nil {
		t.Error("SetDefaultWakeMode accepted an unrecognised value without an error to log")
	}
	check("[agents] wake = sometimes", map[string]want{
		"plain": {WakeMailCheck, `"sometimes" is unrecognised`},
	})
}

func TestResolveWakeMode_FollowsExtendsToTheTemplate(t *testing.T) {
	sandboxDesiredState(t, "pm-x", true)
	root := PromptDir()
	if err := os.MkdirAll(filepath.Join(root, "pm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pm", "tmpl.md"), []byte("+++\nwake = \"pointer\"\n+++\n# t\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pm", "x.toml"), []byte("k = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stub := "+++\nauto_start = true\n+++\nextends tmpl with config pm/x.toml\n"
	if err := os.WriteFile(filepath.Join(CrewPromptDir(), "pm-x.md"), []byte(stub), 0644); err != nil {
		t.Fatal(err)
	}
	d, err := ResolveWakeMode("pm-x")
	if err != nil {
		t.Fatalf("ResolveWakeMode: %v", err)
	}
	if d.Mode != WakePointer || !strings.Contains(d.Source, "via extends") {
		t.Fatalf("decl = %+v, want pointer from the extends template", d)
	}
	// The stub — the operator-editable file — wins over its template.
	stub = "+++\nauto_start = true\nwake = \"mail-check\"\n+++\nextends tmpl with config pm/x.toml\n"
	if err := os.WriteFile(filepath.Join(CrewPromptDir(), "pm-x.md"), []byte(stub), 0644); err != nil {
		t.Fatal(err)
	}
	if d, _ := ResolveWakeMode("pm-x"); d.Mode != WakeMailCheck {
		t.Fatalf("stub override: decl = %+v, want mail-check", d)
	}
}

// TestDiagnose_PointerWakeDeclaredIsNotJudged is the diagnose half of #232's
// acceptance: a declared agent with no mail-check is NOT no_mail_loop and
// carries its declaration's source; an undeclared one beside it still is.
func TestDiagnose_PointerWakeDeclaredIsNotJudged(t *testing.T) {
	sandboxDesiredState(t, "undeclared", true)
	writeCrewPromptWith(t, "declared", true, "wake = \"pointer\"\n")
	now := time.Now()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
	at := now.Add(-time.Hour).UTC().Truncate(time.Second)
	ctx := &fakeEventContext{
		removal:    EventContext{Seen: true, At: at, Reason: "reason=explicit_rm"},
		unconsumed: EventContext{Seen: true, At: at, Reason: "mail_sent", Detail: "mail m1"},
	}
	reg.SetDiagnoseEventContext(ctx)

	d := reg.diagnose(mailLoopCrewAgent("declared", now))
	if d.MailCheckMissing || d.Health == "no_mail_loop" {
		t.Errorf("declared: MailCheckMissing=%v Health=%q, want not judged on a mail-check", d.MailCheckMissing, d.Health)
	}
	if !d.PointerWake || d.WakeMode != WakePointer || !strings.Contains(d.WakeModeSource, "declared.md") {
		t.Errorf("declared: PointerWake=%v mode=%q source=%q, want pointer naming the prompt file", d.PointerWake, d.WakeMode, d.WakeModeSource)
	}
	if d.MailCheckObserved != MailLoopObservedAbsent {
		t.Errorf("declared: MailCheckObserved = %q, want absent", d.MailCheckObserved)
	}
	if d.LastWakeUnconsumed == nil || !d.LastWakeUnconsumed.Seen {
		t.Errorf("declared: LastWakeUnconsumed = %+v, want the context attached", d.LastWakeUnconsumed)
	}
	if d.LastMailCheckRemoval != nil {
		t.Error("declared: removal context attached to an agent that was not judged missing")
	}

	// The undeclared control — with an explicit_rm on record, which must NOT
	// suppress the RED: a removal is not a declaration.
	u := reg.diagnose(mailLoopCrewAgent("undeclared", now))
	if !u.MailCheckMissing || u.Health != "no_mail_loop" {
		t.Errorf("undeclared: MailCheckMissing=%v Health=%q, want no_mail_loop despite an explicit_rm", u.MailCheckMissing, u.Health)
	}
	if u.PointerWake || u.WakeMode != WakeMailCheck || u.WakeModeSource != "default" {
		t.Errorf("undeclared: PointerWake=%v mode=%q source=%q, want mail-check from default", u.PointerWake, u.WakeMode, u.WakeModeSource)
	}
	if u.LastMailCheckRemoval == nil || u.LastMailCheckRemoval.Reason != "reason=explicit_rm" {
		t.Errorf("undeclared: LastMailCheckRemoval = %+v, want the explicit_rm context", u.LastMailCheckRemoval)
	}
	if u.LastWakeUnconsumed != nil {
		t.Error("undeclared: wake_unconsumed context attached to a mail-check agent")
	}
	if got := strings.Join(ctx.asked, ","); !strings.Contains(got, "removal:mail-check-undeclared") {
		t.Errorf("asked %q, want the removal looked up by its schedule id mail-check-undeclared", got)
	}
}

// TestMailLoopReport_PointerWakeIsListedNotJudged is the check-mailloops and
// deafwatch half: both read MailLoopReport, so they agree with diagnose by
// construction. Under a config default the frontmatter override-off agent is
// the one that stays judged.
func TestMailLoopReport_PointerWakeIsListedNotJudged(t *testing.T) {
	sandboxDesiredState(t, "undeclared", true)
	writeCrewPromptWith(t, "declared", true, "wake = \"pointer\"\n")
	writeCrewPromptWith(t, "optout", true, "wake = \"mail-check\"\n")
	now := time.Now()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, n := range []string{"undeclared", "declared", "optout"} {
		reg.agents[n] = mailLoopCrewAgent(n, now)
	}
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})

	names := func(rep MailLoopReport) (missing, pointer []string) {
		for _, m := range rep.Missing {
			missing = append(missing, m.Name)
		}
		for _, u := range *rep.Unjudged {
			if u.Reason == ExclusionPointerWake {
				pointer = append(pointer, u.Name)
			}
		}
		return
	}

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatal(err)
	}
	missing, pointer := names(rep)
	if strings.Join(missing, ",") != "optout,undeclared" || strings.Join(pointer, ",") != "declared" {
		t.Errorf("no default: missing=%v pointer=%v, want [optout undeclared] / [declared]", missing, pointer)
	}
	if rep.Judged != 2 || rep.Scanned != 3 {
		t.Errorf("no default: judged %d of %d, want 2 of 3", rep.Judged, rep.Scanned)
	}

	setWakeDefault(t, "pointer")
	rep, err = reg.MailLoopReport()
	if err != nil {
		t.Fatal(err)
	}
	missing, pointer = names(rep)
	if strings.Join(missing, ",") != "optout" || strings.Join(pointer, ",") != "declared,undeclared" {
		t.Errorf("[agents] wake = pointer: missing=%v pointer=%v, want [optout] / [declared undeclared]", missing, pointer)
	}
	out := rep.Render()
	if !strings.Contains(out, "pointer wake declared") || !strings.Contains(out, "declare pointer wake") {
		t.Errorf("Render does not list the pointer-wake agents and the declare-instead line:\n%s", out)
	}
}

func TestEventLogContext_ReadsLatestAndStatesCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	lines := []string{
		`{"schema_version":1,"timestamp":"2026-10-01T00:00:00Z","event_type":"schedule_removed","agent":"pogod","details":{"schedule_id":"mail-check-a","reason":"agent_gone","removed_at":"2026-10-01T00:00:00Z"}}`,
		`{"schema_version":1,"timestamp":"2026-10-02T00:00:00Z","event_type":"schedule_removed","agent":"pogod","details":{"schedule_id":"mail-check-a","reason":"explicit_rm","removed_at":"2026-10-02T00:00:00Z"}}`,
		`{"schema_version":1,"timestamp":"2026-10-03T00:00:00Z","event_type":"wake_unconsumed","agent":"pogod","details":{"recipient":"b","trigger":"mail_sent","msg_id":"m9"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := EventLogContext{Path: path}

	r := l.LastScheduleRemoval("mail-check-a")
	if !r.Seen || r.Reason != "reason=explicit_rm" || !r.At.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("removal = %+v, want the LATER explicit_rm", r)
	}
	u := l.LastWakeUnconsumed("b")
	if !u.Seen || u.Detail != "mail m9" {
		t.Errorf("unconsumed = %+v, want mail m9", u)
	}
	// Absence is stated with its floor, never as "never".
	none := l.LastWakeUnconsumed("a")
	if none.Seen || !strings.Contains(none.Describe(), "since 2026-10-01T00:00:00Z") {
		t.Errorf("absent = %+v (%q), want not seen with the scan floor", none, none.Describe())
	}
	// Positive control for the floor: a missing log is an empty scan, and
	// says so rather than claiming coverage it does not have.
	missing := (EventLogContext{Path: filepath.Join(dir, "nope")}).LastWakeUnconsumed("a")
	if d := missing.Describe(); !strings.Contains(d, "empty") {
		t.Errorf("missing log describes as %q", d)
	}
}
