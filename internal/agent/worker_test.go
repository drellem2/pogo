package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// setWorker sets the process-wide worker display name for the duration of a
// test and restores the previous value on cleanup.
func setWorker(t *testing.T, name string) {
	t.Helper()
	prev := WorkerName()
	SetWorkerName(name)
	t.Cleanup(func() { SetWorkerName(prev) })
}

func TestSetWorkerName(t *testing.T) {
	if got := WorkerName(); got != DefaultWorkerName {
		t.Fatalf("default WorkerName() = %q, want %q", got, DefaultWorkerName)
	}
	setWorker(t, "pogocat")
	if got := WorkerName(); got != "pogocat" {
		t.Errorf("WorkerName() = %q, want pogocat", got)
	}
	// Empty resets to the default rather than leaving an unusable name.
	SetWorkerName("")
	if got := WorkerName(); got != DefaultWorkerName {
		t.Errorf("WorkerName() after empty set = %q, want %q", got, DefaultWorkerName)
	}
}

// TestSubstituteRoleNamesWorker verifies the static-prompt substitution
// replaces the worker display placeholders (alongside the coordinator ones).
func TestSubstituteRoleNamesWorker(t *testing.T) {
	setWorker(t, "pogocat")
	in := "spawn a {{.Worker}}; the {{.WorkerTitle}} runs one task"
	want := "spawn a pogocat; the Pogocat runs one task"
	if got := substituteRoleNames(in); got != want {
		t.Errorf("substituteRoleNames = %q, want %q", got, want)
	}
}

// TestExpandTemplateWorkerVar verifies polecat templates resolve {{.Worker}}
// natively through text/template, defaulting from the process-wide name when
// the caller leaves TemplateVars.Worker empty.
func TestExpandTemplateWorkerVar(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	if err := os.WriteFile(tmplPath, []byte("You are a {{.Worker}} ({{.WorkerTitle}}).\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "You are a polecat (Polecat).\n"; out != want {
		t.Errorf("default expansion = %q, want %q", out, want)
	}

	setWorker(t, "pogocat")
	out, err = ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "You are a pogocat (Pogocat).\n"; out != want {
		t.Errorf("renamed expansion = %q, want %q", out, want)
	}
}
