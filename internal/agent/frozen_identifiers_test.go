package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
)

// TestWorkerRenameFreezesIdentifiers is the WI-5 round-trip guard for the
// display-vs-identifier decoupling (mg-6a24 §1). The contract: renaming the
// worker DISPLAY name moves prose and nothing else. Every load-bearing polecat
// identifier — the branch prefix, the polecats dir, the agent-type/registry
// key, the event-log actor prefix, and the POGO_ROLE env value — stays frozen
// at "polecat" no matter what display name is configured.
//
// This test is deliberately paranoid: it renames the worker to "pogocat" and
// then asserts (a) the prose actually changed and (b) each frozen identifier is
// byte-for-byte its original value. If a future edit ever wires one of these off
// WorkerName(), this test fails loudly with a note on what the rename would
// break — see mg-6a24 §1.1 for the "why frozen" of each row.
func TestWorkerRenameFreezesIdentifiers(t *testing.T) {
	setWorker(t, "pogocat")

	// (a) The DISPLAY changes — this is the whole point of the seam. Both the
	// static-prompt substitution path and the text/template path must pick up
	// the configured name.
	if got := substituteRoleNames("spawn a {{.Worker}} ({{.WorkerTitle}})"); got != "spawn a pogocat (Pogocat)" {
		t.Errorf("static prose not renamed: got %q, want the pogocat form", got)
	}
	tmplPath := filepath.Join(t.TempDir(), "polecat.md")
	if err := os.WriteFile(tmplPath, []byte("You are a {{.Worker}} ({{.WorkerTitle}}).\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered, err := ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "pogocat") || !strings.Contains(rendered, "Pogocat") {
		t.Errorf("template prose not renamed: %q", rendered)
	}

	// (b) The frozen identifiers are UNCHANGED. None of them read WorkerName();
	// a display rename must never reach them. Values per mg-6a24 §1.1.

	// #1 Branch prefix — gitgc reads live branches back by this prefix; renaming
	// orphans every in-flight polecat branch.
	if gitgc.BranchPrefix != "polecat-" {
		t.Errorf("BranchPrefix = %q, want polecat- (renaming orphans in-flight polecat branches)", gitgc.BranchPrefix)
	}

	// #2 Polecats dir — orphan-sweep reads this dir back from disk.
	dir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(dir); base != "polecats" {
		t.Errorf("DefaultPolecatsDir basename = %q, want polecats (orphan-sweep reads this dir back)", base)
	}

	// #3 Agent-type registry/spawn key — written to POGO_AGENT_TYPE and matched
	// by reap/park/gitgc/config lookups.
	if string(TypePolecat) != "polecat" {
		t.Errorf("TypePolecat = %q, want polecat (written to POGO_AGENT_TYPE, matched by reap/park/gitgc)", TypePolecat)
	}

	// #4 Event-log actor prefix — ResolveAgent derives the persisted actor from
	// the frozen agent TYPE, never the display name; classify.go parses it back.
	t.Setenv("POGO_AGENT_NAME", "abc")
	t.Setenv("POGO_AGENT_TYPE", string(TypePolecat))
	if got := events.ResolveAgent(""); got != "cat-abc" {
		t.Errorf("event-log actor = %q, want cat-abc (classify.go parses the cat- prefix back)", got)
	}
	if strings.Contains(events.ResolveAgent(""), "pogocat") {
		t.Errorf("event-log actor leaked the display name: %q", events.ResolveAgent(""))
	}

	// #5 POGO_ROLE env value — a cross-tool contract for mg prime / role
	// detection. api.go builds it as "POGO_ROLE=" + string(TypePolecat), so the
	// frozen TypePolecat assertion above transitively pins it. Re-check the exact
	// string the daemon exports, and prove it is NOT the renamed display name.
	role := "POGO_ROLE=" + string(TypePolecat)
	if role != "POGO_ROLE=polecat" {
		t.Errorf("POGO_ROLE env = %q, want POGO_ROLE=polecat (consumed by mg prime / role detection)", role)
	}
	if strings.Contains(role, WorkerName()) {
		t.Errorf("POGO_ROLE env %q tracks the worker display name %q — it must stay the frozen literal", role, WorkerName())
	}
}
