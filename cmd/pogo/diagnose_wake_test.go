package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// drellem2/pogo#232, #236: what `pogo agent diagnose` SAYS about a mail loop,
// for a declared agent and an undeclared one.

func TestWriteMailLoop_DeclaredPointerWakeHasNoWarningAndNoRecipe(t *testing.T) {
	var b strings.Builder
	writeMailLoop(&b, &agent.DiagnoseInfo{
		AgentInfo:         agent.AgentInfo{Name: "pm-x"},
		WakeMode:          agent.WakePointer,
		WakeModeSource:    "config.toml [agents] wake",
		PointerWake:       true,
		MailCheckObserved: agent.MailLoopObservedAbsent,
		LastWakeUnconsumed: &agent.EventContext{
			Seen: true, At: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), Reason: "mail_sent", Detail: "mail m1",
		},
	})
	out := b.String()
	for _, want := range []string{
		"Mail loop:      none — pointer wake declared (config.toml [agents] wake)",
		"last wake_unconsumed: 2026-10-28T12:00:00Z (mail_sent, mail m1)",
		"context only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"⚠", "Restore it", "pogo schedule"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a declared agent's render contains %q:\n%s", forbidden, out)
		}
	}

	// A row that exists anyway is not rendered as "none".
	b.Reset()
	writeMailLoop(&b, &agent.DiagnoseInfo{
		AgentInfo:         agent.AgentInfo{Name: "pm-x"},
		WakeModeSource:    "prompt frontmatter /p/pm-x.md",
		PointerWake:       true,
		MailCheckObserved: agent.MailLoopObservedPresent,
	})
	if out := b.String(); strings.Contains(out, "none —") || !strings.Contains(out, "still registered") {
		t.Errorf("present-anyway render:\n%s", out)
	}
}

func TestWriteMailLoop_UndeclaredMissingKeepsTheWarningAddsTheDeclareLine(t *testing.T) {
	var b strings.Builder
	writeMailLoop(&b, &agent.DiagnoseInfo{
		AgentInfo:        agent.AgentInfo{Name: "doctor"},
		WakeMode:         agent.WakeMailCheck,
		WakeModeSource:   "default",
		MailCheckMissing: true,
		LastMailCheckRemoval: &agent.EventContext{
			Seen: true, At: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Reason: "reason=explicit_rm",
		},
	})
	out := b.String()
	for _, want := range []string{
		"⚠ NO MAIL LOOP: doctor",
		"Restore it:",
		"--id mail-check-doctor",
		"If this absence is deliberate, declare pointer wake (" + agent.WakeModeHow + ") instead of restoring it.",
		"Last removal of mail-check-doctor: 2026-10-02T00:00:00Z (reason=explicit_rm)",
		"does not change the verdict",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// A judged agent with its loop present renders nothing at all.
	b.Reset()
	writeMailLoop(&b, &agent.DiagnoseInfo{AgentInfo: agent.AgentInfo{Name: "ok"}, WakeMode: agent.WakeMailCheck})
	if b.Len() != 0 {
		t.Errorf("healthy loop rendered %q", b.String())
	}
}

// TestCheckMailLoopsHelp_NamesEveryExclusionReason: the help promises "one per
// category above"; pointer_wake is the category this change added.
func TestCheckMailLoopsHelp_NamesEveryExclusionReason(t *testing.T) {
	for _, r := range []agent.MailLoopExclusionReason{
		agent.ExclusionPolecat, agent.ExclusionNotRunning, agent.ExclusionNotConfigured,
		agent.ExclusionUnreadablePrompts, agent.ExclusionPointerWake,
	} {
		if !strings.Contains(checkMailLoopsLong, `"`+string(r)+`"`) {
			t.Errorf("check-mailloops --help does not name reason %q", r)
		}
	}
}

// TestCheckMailLoopsRender_DeclaredListedUndeclaredMissing is the
// check-mailloops render over the two agents.
func TestCheckMailLoopsRender_DeclaredListedUndeclaredMissing(t *testing.T) {
	rep := agent.MailLoopReport{
		Scanned: 2, Judged: 1,
		Missing: []agent.MailLoopFinding{{Name: "undeclared", Identity: "crew-undeclared", Type: agent.TypeCrew, Alive: true}},
		Unjudged: &[]agent.MailLoopExclusion{{
			Name: "declared", Type: agent.TypeCrew, Reason: agent.ExclusionPointerWake, MailLoop: agent.MailLoopObservedAbsent,
		}},
	}
	out := rep.Render()
	for _, want := range []string{"undeclared", "declared", "pointer wake declared", "declare pointer wake"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !rep.Actionable() {
		t.Error("the undeclared agent must still make the report actionable")
	}
}
