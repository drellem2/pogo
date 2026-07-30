package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// recorder is the mail seam substitute. Nothing in this file may shell out to
// the real `mg`: a test that mails a live crew agent manufactures a fleet alarm.
type recorder struct {
	sent []recordedMail
	err  error
}

type recordedMail struct{ to, from, subject, body string }

func (r *recorder) send(to, from, subject, body string) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, recordedMail{to, from, subject, body})
	return nil
}

// fixture builds a prompt dir holding the .dist sidecars named by conflicts, so
// the fingerprint is computed from real content rather than the unread fallback.
func fixture(t *testing.T, dists map[string]string) (promptDir, statePath string) {
	t.Helper()
	promptDir = t.TempDir()
	for rel, content := range dists {
		abs := filepath.Join(promptDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return promptDir, filepath.Join(t.TempDir(), promptSyncNoticesFile)
}

func conflictResult(pairs ...string) *agent.InstallResult {
	res := &agent.InstallResult{}
	for _, p := range pairs {
		res.Conflicts = append(res.Conflicts, agent.PromptConflict{Path: p, DistPath: p + ".dist"})
	}
	return res
}

// TestPromptSyncAddressee_ResolvesTheAgentThatCanAct pins the routing table.
// The whole ticket turns on addressing the agent that can act rather than a
// channel that audits as instrumented, so a misroute here is the defect
// returning under a new name.
func TestPromptSyncAddressee_ResolvesTheAgentThatCanAct(t *testing.T) {
	cases := []struct {
		rel, coordinator, wantTo string
		wantOwned                bool
		why                      string
	}{
		{"mayor.md", "mayor", "mayor", true,
			"the file that actually fired in the incident must reach the coordinator"},
		{"crew/doctor.md", "mayor", "doctor", true,
			"a crew prompt belongs to the crew agent named by its stem"},
		{"crew/pm-pogo.md", "mayor", "pm-pogo", true,
			"hyphenated crew names must survive the stem trim"},
		{"templates/polecat.md", "mayor", "mayor", false,
			"a polecat template belongs to no running agent, so it falls back to the dispatcher"},
		{"pm/pm-template.md", "mayor", "mayor", false,
			"pm-template is extended by stubs and is nobody's inbox"},
		{"crew/nested/thing.md", "mayor", "mayor", false,
			"a nested path is not a crew agent; addressing 'nested/thing' would invent a mailbox"},
		{"crew/.md", "mayor", "mayor", false,
			"an empty stem must never be mailed"},
		{"something-unrecognized.toml", "mayor", "mayor", false,
			"an unrecognized path must fall back, never synthesize a name from the path"},
	}
	for _, c := range cases {
		to, owned := promptSyncAddressee(c.rel, c.coordinator)
		if to != c.wantTo || owned != c.wantOwned {
			t.Errorf("promptSyncAddressee(%q, %q) = (%q, %v), want (%q, %v) — %s",
				c.rel, c.coordinator, to, owned, c.wantTo, c.wantOwned, c.why)
		}
	}
}

// TestPromptSyncAddressee_HonorsRenamedCoordinator is the misroute this could
// have shipped with. The file is ALWAYS mayor.md (mechanism) but the agent it
// starts as follows [agents] coordinator (policy) — see ListPrompts. Hardcoding
// "mayor" would mail a phantom mailbox on any consumer who renamed it, and mail
// to a name no agent reads is silently accepted and lost.
func TestPromptSyncAddressee_HonorsRenamedCoordinator(t *testing.T) {
	to, owned := promptSyncAddressee("mayor.md", "sheriff")
	if to != "sheriff" || !owned {
		t.Errorf("mayor.md under coordinator=sheriff = (%q, %v), want (\"sheriff\", true) — "+
			"the coordinator's configured name is who reads the mail, not the filename", to, owned)
	}
}

// TestNotifyPromptSyncConflicts_MailsOnTransitionThenGoesQuiet is the ticket's
// central regression. The real line fired at EVERY boot for at least seven days
// (07-29 and 07-30 both confirmed in pogod.log). Seven identical mails is how a
// true alarm gets filtered out, so the condition must announce on transition in
// and then stay quiet.
func TestNotifyPromptSyncConflicts_MailsOnTransitionThenGoesQuiet(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	res := conflictResult("mayor.md")
	rec := &recorder{}
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now, rec.send); sent != 1 {
		t.Fatalf("first boot sent %d mails, want 1 — a transition into the condition must announce", sent)
	}
	if got := rec.sent[0].to; got != "mayor" {
		t.Errorf("addressed %q, want \"mayor\" — the agent whose prompt was declined is the one who can act", got)
	}

	// Six more boots across the same week, exactly as happened in the wild.
	for i := 1; i <= 6; i++ {
		boot := now.Add(time.Duration(i) * 3 * time.Hour)
		if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, boot, rec.send); sent != 0 {
			t.Fatalf("boot %d sent %d mails, want 0 — this is the every-boot repetition mg-c3f0 exists to prevent", i+1, sent)
		}
	}
	if len(rec.sent) != 1 {
		t.Fatalf("seven boots produced %d mails, want exactly 1", len(rec.sent))
	}
}

// TestNotifyPromptSyncConflicts_RenotifiesWhileUnresolved covers the other half
// of the rate limit: quiet is not the same as forgotten. A conflict nobody
// reconciles has to come back, or suppression becomes the new silence.
func TestNotifyPromptSyncConflicts_RenotifiesWhileUnresolved(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	res := conflictResult("mayor.md")
	rec := &recorder{}
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now, rec.send)

	// One second short of the window: still quiet.
	justShy := now.Add(promptSyncRenotifyAfter - time.Second)
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, justShy, rec.send); sent != 0 {
		t.Errorf("sent %d just before the renotify window, want 0", sent)
	}
	// At the window: speaks again.
	at := now.Add(promptSyncRenotifyAfter)
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, at, rec.send); sent != 1 {
		t.Errorf("sent %d at the renotify window, want 1 — an unreconciled prompt must be reminded", sent)
	}

	// And the clock restarts from the DELIVERY, not from the boot: a boot one
	// second after the re-notification must be quiet again.
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, at.Add(time.Second), rec.send); sent != 0 {
		t.Errorf("sent %d immediately after a re-notification, want 0", sent)
	}
}

// TestNotifyPromptSyncConflicts_ReNotifiesWhenTheDeclinedUpdateChanges: the
// suppression is keyed on WHICH divergence was announced, not merely that one
// was. A later pogo version shipping a further change to the same prompt means
// the recipient's merge job is now bigger than the one they were told about.
func TestNotifyPromptSyncConflicts_ReNotifiesWhenTheDeclinedUpdateChanges(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	res := conflictResult("mayor.md")
	rec := &recorder{}
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now, rec.send)

	// A new binary ships a further change to the same prompt.
	if err := os.WriteFile(filepath.Join(promptDir, "mayor.md.dist"), []byte("shipped v3 — parked section"), 0644); err != nil {
		t.Fatal(err)
	}
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now.Add(time.Hour), rec.send); sent != 1 {
		t.Errorf("sent %d after the declined update changed, want 1 — "+
			"the announced divergence is no longer the actual one", sent)
	}
}

// TestNotifyPromptSyncConflicts_FailedMailIsNotRememberedAsDelivered is the
// anti-silence property, and it is the one that keeps this notifier from
// becoming the same defect one level up. If a send failure stamped the store,
// the retry would never happen and the alarm would die quietly — with a
// perfectly clean state file claiming it had been announced.
func TestNotifyPromptSyncConflicts_FailedMailIsNotRememberedAsDelivered(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	res := conflictResult("mayor.md")
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	failing := &recorder{err: os.ErrPermission}
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now, failing.send); sent != 0 {
		t.Fatalf("a failing mailer reported %d sent, want 0", sent)
	}

	// The store must not claim this was announced.
	if data, err := os.ReadFile(statePath); err == nil {
		var n promptSyncNotices
		if err := json.Unmarshal(data, &n); err != nil {
			t.Fatal(err)
		}
		if _, ok := n.Conflicts["mayor.md"]; ok {
			t.Error("a FAILED send was recorded as delivered — the retry can never fire " +
				"and the alarm dies silently behind a clean-looking state file")
		}
	}

	// Next boot, mail works: the conflict must be announced as new.
	ok := &recorder{}
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now.Add(time.Hour), ok.send); sent != 1 {
		t.Errorf("sent %d on the boot after a failed send, want 1 — an unannounced conflict must retry", sent)
	}
}

// TestNotifyPromptSyncConflicts_ResolvedThenRecurringMailsImmediately: once the
// prompt is reconciled the memory must be dropped, so a later recurrence is a
// fresh transition rather than inheriting a stale suppression window.
func TestNotifyPromptSyncConflicts_ResolvedThenRecurringMailsImmediately(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	res := conflictResult("mayor.md")
	rec := &recorder{}
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now, rec.send)

	// Reconciled: the next boot reports no conflicts at all.
	if sent := notifyPromptSyncConflicts(&agent.InstallResult{}, "mayor", promptDir, statePath, now.Add(time.Hour), rec.send); sent != 0 {
		t.Errorf("a conflict-free boot sent %d mails, want 0", sent)
	}

	// It comes back an hour later. This is a NEW incident and must not be
	// silenced by the window from the resolved one.
	if sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath, now.Add(2*time.Hour), rec.send); sent != 1 {
		t.Errorf("a recurrence sent %d mails, want 1 — a resolved conflict must be forgotten", sent)
	}
}

// TestNotifyPromptSyncConflicts_UnreadableStoreReNotifiesRatherThanGoingQuiet
// pins the bias. A corrupt store must make this notifier forget (worst case: a
// duplicate mail), never make it assume the recipient was already told.
func TestNotifyPromptSyncConflicts_UnreadableStoreReNotifiesRatherThanGoingQuiet(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{"mayor.md.dist": "shipped v2"})
	if err := os.WriteFile(statePath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	sent := notifyPromptSyncConflicts(conflictResult("mayor.md"), "mayor", promptDir, statePath,
		time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC), rec.send)
	if sent != 1 {
		t.Errorf("a corrupt store produced %d mails, want 1 — failing toward silence is the defect being fixed", sent)
	}
}

// TestNotifyPromptSyncConflicts_MultipleConflictsEachReachTheirOwnAgent: the
// routing is per-conflict, so a boot declining two prompts must not collapse
// into one mail to one agent.
func TestNotifyPromptSyncConflicts_MultipleConflictsEachReachTheirOwnAgent(t *testing.T) {
	promptDir, statePath := fixture(t, map[string]string{
		"mayor.md.dist":             "shipped mayor",
		"crew/doctor.md.dist":       "shipped doctor",
		"templates/polecat.md.dist": "shipped template",
	})
	res := conflictResult("mayor.md", "crew/doctor.md", "templates/polecat.md")
	rec := &recorder{}

	sent := notifyPromptSyncConflicts(res, "mayor", promptDir, statePath,
		time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC), rec.send)
	if sent != 3 {
		t.Fatalf("sent %d mails for 3 declined prompts, want 3", sent)
	}
	got := map[string]int{}
	for _, m := range rec.sent {
		got[m.to]++
	}
	// doctor owns its own prompt; the polecat template has no owning agent and
	// falls back to the dispatcher, which is also the coordinator.
	if got["doctor"] != 1 {
		t.Errorf("doctor received %d notices, want 1 — a crew agent owns its own prompt", got["doctor"])
	}
	if got["mayor"] != 2 {
		t.Errorf("mayor received %d notices, want 2 (its own prompt + the unowned template)", got["mayor"])
	}
}

// TestPromptSyncMailText_StatesTheRemedyWithoutHandingOutTheDestructiveOne. The
// only reason the canonical file was preserved is that its local edits might be
// load-bearing, so a paste-ready copy-over would hand out the single action
// this mechanism exists to prevent — with the daemon's authority behind it.
func TestPromptSyncMailText_StatesTheRemedyWithoutHandingOutTheDestructiveOne(t *testing.T) {
	subject, body := promptSyncMailText(
		agent.PromptConflict{Path: "mayor.md", DistPath: "mayor.md.dist"}, "mayor", true)

	for _, want := range []string{"mayor.md", "mayor.md.dist"} {
		if !strings.Contains(subject, want) {
			t.Errorf("subject must name %q so the inbox is actionable without opening it; got %q", want, subject)
		}
	}
	if !strings.Contains(body, "diff -u") {
		t.Error("body must show how to inspect the divergence")
	}
	if !strings.Contains(body, "docs/prompt-customization.md") {
		t.Error("body must link the merge workflow doc")
	}
	// The destructive shortcut must not appear as an instruction.
	for _, forbidden := range []string{"cp mayor.md.dist", "mv mayor.md.dist"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body hands out %q — copying over the local edits is the outcome the "+
				"decline exists to prevent", forbidden)
		}
	}
}
