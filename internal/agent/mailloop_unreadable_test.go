package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makePromptTreeUnreadable makes dir exist-but-unreadable and asserts that the
// staging actually took, because a test that quietly stages nothing would pass
// this whole file for the wrong reason. Running as root defeats mode 0000, and
// so does a filesystem that ignores permission bits; either way the test skips
// rather than asserting on an unstaged condition.
//
// The restore is registered AFTER testsandbox.Isolate's cleanup and therefore
// runs BEFORE it (t.Cleanup is LIFO), so the sandbox can still remove the tree.
func makePromptTreeUnreadable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0000); err != nil {
		t.Skipf("cannot chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skipf("%s is still readable at mode 0000 (running as root?); cannot stage an unreadable prompt tree", dir)
	}
}

// TestListPrompts_UnreadableTreeIsAnErrorNotAnEmptyList is the deepest layer of
// mg-7b3f, and the one that was not in the ticket.
//
// IsConfiguredAgent had an error path, logged and collapsed at autostart.go:203
// — and it was UNREACHABLE, because ListPrompts swallowed a failed os.ReadDir
// (`if entries, err := ...; err == nil`) and returned a shorter list with a nil
// error. Threading the error out of IsConfiguredAgent alone would have added a
// taxonomy value that could never be emitted: the same unbacked disclosure as
// the one being fixed, pointing the other way.
//
// ABSENT and UNREADABLE must stay different answers. A directory that is not
// there is a fact about configuration; one that cannot be read is a fault.
func TestListPrompts_UnreadableTreeIsAnErrorNotAnEmptyList(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)

	// Precondition: the tree reads, and the crew prompt is in it.
	prompts, err := ListPrompts()
	if err != nil {
		t.Fatalf("ListPrompts on a readable tree: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatal("precondition: a sandboxed tree with one crew prompt listed nothing")
	}

	makePromptTreeUnreadable(t, CrewPromptDir())

	prompts, err = ListPrompts()
	if err == nil {
		t.Fatalf("ListPrompts on an UNREADABLE crew dir = (%d prompts, nil) — a failed read must not "+
			"render as a shorter list; that is how every crew agent silently stopped being configured", len(prompts))
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("error = %q, want it to name the tree as unreadable so a log line is diagnosable", err)
	}
}

// TestListPrompts_MissingDirIsNotAnError pins the other half. A machine with no
// crew prompts at all — a fresh install — has an empty list and no error, and
// turning that into a fault would make the fix a new false alarm.
func TestListPrompts_MissingDirIsNotAnError(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	if err := os.RemoveAll(CrewPromptDir()); err != nil {
		t.Fatalf("remove crew dir: %v", err)
	}
	if err := os.RemoveAll(TemplateDir()); err != nil {
		t.Fatalf("remove template dir: %v", err)
	}
	prompts, err := ListPrompts()
	if err != nil {
		t.Fatalf("ListPrompts with no crew/template dirs = %v; absent is a configuration fact, not a fault", err)
	}
	for _, p := range prompts {
		if p.Category == "crew" || p.Category == "templates" {
			t.Errorf("listed %+v from a directory that does not exist", p)
		}
	}
}

// TestConfiguredStateFor_SeparatesUnreadableFromUnconfigured is mg-7b3f's
// acceptance at the predicate.
//
// The two answers folded into IsConfiguredAgent's false have opposite
// operational meanings: "not configured" is a fact about intent and needs no
// action, "I could not read the prompt tree" is a fault in the instrument and
// means the answer is UNKNOWN.
func TestConfiguredStateFor_SeparatesUnreadableFromUnconfigured(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)

	// (true, nil) — ours.
	if configured, err := ConfiguredStateFor("pm-pogo"); !configured || err != nil {
		t.Errorf("ConfiguredStateFor(pm-pogo) = (%v, %v), want (true, nil)", configured, err)
	}
	// (true, nil) via the event-identity form, as DesiredStateFor unwraps it.
	if configured, err := ConfiguredStateFor("crew-pm-pogo"); !configured || err != nil {
		t.Errorf("ConfiguredStateFor(crew-pm-pogo) = (%v, %v), want (true, nil)", configured, err)
	}
	// (false, nil) — definitively not ours, on a tree that WAS read.
	if configured, err := ConfiguredStateFor("cat-7b3f"); configured || err != nil {
		t.Errorf("ConfiguredStateFor(cat-7b3f) = (%v, %v), want (false, nil) — a polecat has no prompt", configured, err)
	}
	if configured, err := ConfiguredStateFor(""); configured || err != nil {
		t.Errorf("ConfiguredStateFor(\"\") = (%v, %v), want (false, nil)", configured, err)
	}

	makePromptTreeUnreadable(t, CrewPromptDir())

	// (false, err) — UNKNOWN. This is the answer that did not exist.
	configured, err := ConfiguredStateFor("pm-pogo")
	if err == nil {
		t.Fatalf("ConfiguredStateFor(pm-pogo) on an unreadable tree = (%v, nil); an I/O fault must not "+
			"return as a normal negative — that collapse IS the defect", configured)
	}
	if configured {
		t.Error("ConfiguredStateFor returned true alongside an error; the bool is not meaningful when the tree is unreadable")
	}
	// And the collapsing wrapper still collapses, on purpose: callers where a
	// wrong "no" is harmless keep the cheap predicate.
	if IsConfiguredAgent("pm-pogo") {
		t.Error("IsConfiguredAgent must still collapse the unknown to false for callers that cannot act on it")
	}
}

// TestDesiredStateFor_UnreadableTreeIsUnknownNotGone covers the consequence the
// ticket did not name, and it is worse than a misclassification.
//
// DesiredStateFor's own doc comment reserves (false, nil) for EVIDENCE — "no
// prompt at all". With ListPrompts swallowing the read error it gave exactly
// that for the whole crew, and the mail-check reap (cmd/pogod/main.go:250) acts
// on it: AgentGone, schedules removed. So an unreadable ~/.pogo/agents/crew did
// not merely hide the fleet's mail loops, it DELETED them — manufacturing the
// fault deafwatch exists to announce. The reap's AgentUnknown branch already
// says "NOT reaping"; this test is what keeps it reachable.
func TestDesiredStateFor_UnreadableTreeIsUnknownNotGone(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	if expected, err := DesiredStateFor("pm-pogo"); !expected || err != nil {
		t.Fatalf("precondition: DesiredStateFor(pm-pogo) = (%v, %v), want (true, nil)", expected, err)
	}

	makePromptTreeUnreadable(t, CrewPromptDir())

	expected, err := DesiredStateFor("pm-pogo")
	if err == nil {
		t.Fatalf("DesiredStateFor(pm-pogo) on an unreadable tree = (%v, nil) — a confident \"not in the "+
			"desired state\" for an agent whose config we simply could not read is what let the reap "+
			"delete the crew's mail-check schedules", expected)
	}
	if expected {
		t.Error("DesiredStateFor returned true alongside an error")
	}
	if IsExpectedAgent("pm-pogo") {
		t.Error("IsExpectedAgent must collapse the unknown to false — a wrong \"no\" is harmless there, unlike in the reap")
	}
}

// TestMailLoopReport_UnreadablePromptTreeIsNotNotConfigured is the end-to-end
// acceptance: the reason an operator actually reads.
//
// `pogo check-mailloops --help` and BOTH shipped disclosures
// (internal/deafwatch, internal/agent) named "unreadable prompt tree" as a
// category while the code could only say "not_configured". This asserts the
// promise is now kept — and that the two remain DIFFERENT values, because a
// fourth value that fires for both would be the same collapse under a longer
// name.
func TestMailLoopReport_UnreadablePromptTreeIsNotNotConfigured(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// A RUNNING agent — liveness is what gets us past the not_running branch to
	// the configured question at all.
	live := mailLoopCrewAgent("pm-pogo", now)
	live.PID = liveProcess(t)
	reg.agents["pm-pogo"] = live
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})

	// Baseline on a READABLE tree: an agent with no prompt is not_configured,
	// and that value must keep meaning exactly that.
	stranger := mailLoopCrewAgent("not-ours", now)
	stranger.PID = liveProcess(t)
	reg.agents["not-ours"] = stranger

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if got := reasonFor(t, rep, "not-ours"); got != ExclusionNotConfigured {
		t.Errorf("reason for an agent with no prompt = %q, want %q", got, ExclusionNotConfigured)
	}

	makePromptTreeUnreadable(t, CrewPromptDir())

	rep, err = reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport on an unreadable tree: %v", err)
	}
	for _, name := range []string{"pm-pogo", "not-ours"} {
		got := reasonFor(t, rep, name)
		if got == ExclusionNotConfigured {
			t.Errorf("reason for %s with an UNREADABLE prompt tree = %q — an I/O fault reported as a "+
				"clean exclusion is drellem2/pogo#127 one level in", name, got)
		}
		if got != ExclusionUnreadablePrompts {
			t.Errorf("reason for %s = %q, want %q", name, got, ExclusionUnreadablePrompts)
		}
	}

	// The rendered census must say it in words an operator can act on, and must
	// not describe the fault as a decision.
	out := rep.Render()
	if !strings.Contains(out, "UNREADABLE prompt tree") {
		t.Errorf("Render() does not name the unreadable tree:\n%s", out)
	}
	if strings.Contains(out, "not configured — no prompt on this machine") {
		t.Errorf("Render() still describes an unreadable tree as a clean exclusion:\n%s", out)
	}
}

// TestMailLoopExclusionReason_UnreadableReadsAsAFaultNotADecision pins the two
// phrasings apart. Three of the four reasons say "nothing is owed here"; this
// one says "I could not look", and an operator skimming the census has to be
// able to tell which they are reading.
func TestMailLoopExclusionReason_UnreadableReadsAsAFaultNotADecision(t *testing.T) {
	unreadable := ExclusionUnreadablePrompts.Describe()
	notConfigured := ExclusionNotConfigured.Describe()
	if unreadable == notConfigured {
		t.Fatalf("both reasons describe as %q; the whole point is that they read differently", unreadable)
	}
	if !strings.Contains(unreadable, "UNREADABLE") {
		t.Errorf("Describe() for %q = %q, want it to name the read failure", ExclusionUnreadablePrompts, unreadable)
	}
	if strings.Contains(notConfigured, "could not be read") {
		t.Errorf("Describe() for %q = %q; it must no longer hedge — the hedge existed only because "+
			"the code could not tell the two apart", ExclusionNotConfigured, notConfigured)
	}
}

// reasonFor pulls one agent's exclusion reason out of a report, failing if the
// agent was judged (and so absent from the roster) rather than returning a zero
// value a caller could mistake for a reason.
func reasonFor(t *testing.T, rep MailLoopReport, name string) MailLoopExclusionReason {
	t.Helper()
	if rep.Unjudged == nil {
		t.Fatalf("Unjudged is absent from a locally-built report")
	}
	for _, u := range *rep.Unjudged {
		if u.Name == name {
			return u.Reason
		}
	}
	t.Fatalf("%s is not in the unjudged roster %+v — expected it to be excluded", name, *rep.Unjudged)
	return ""
}

// TestPromptTreeUnreadableStagingActuallyStages guards this file's own
// instrument. Every assertion above rests on makePromptTreeUnreadable really
// producing a read failure; if chmod silently stopped working, the tests would
// go green while measuring nothing — the failure mode the whole ticket is
// about, reproduced in its own test file.
func TestPromptTreeUnreadableStagingActuallyStages(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	dir := CrewPromptDir()
	makePromptTreeUnreadable(t, dir)
	if _, err := os.ReadDir(dir); err == nil {
		t.Fatal("the staging helper returned without producing an unreadable directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "pm-pogo.md")); err == nil {
		t.Error("the prompt file is still statable through an unreadable directory; the staging is not what it claims")
	}
}
