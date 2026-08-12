package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pinPromptTempDir points $TMPDIR at a throwaway root for one test, so
// PromptTempDir resolves inside it. PromptTempDir reads os.TempDir on every
// call, which is what makes this work at all — a cached root would have every
// case in this file sweeping the developer's real $TMPDIR/pogo-prompts.
func pinPromptTempDir(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	dir := PromptTempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating fixture prompt dir: %v", err)
	}
	return dir
}

// writePromptFixture drops a file into the prompt temp dir with a chosen name
// and age.
func writePromptFixture(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("prompt body"), 0o600); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("aging fixture %s: %v", name, err)
	}
	return path
}

func TestExpandedPromptOwnerRoundTrip(t *testing.T) {
	dir := pinPromptTempDir(t)

	// Dots are legal in an agent name — ValidateAgentName rejects only "." and
	// ".." entire — so the parse has to split at the LAST dot. A first-dot split
	// would recover "web" from "web.api" and hand a live polecat's file to the
	// sweep as unowned.
	for _, owner := range []string{"p5197", "web.api", "a.b.c", "pm-pogo"} {
		f, err := createExpandedPromptFile(owner)
		if err != nil {
			t.Fatalf("createExpandedPromptFile(%q): %v", owner, err)
		}
		name := filepath.Base(f.Name())
		f.Close()
		if filepath.Dir(f.Name()) != dir {
			t.Errorf("owner %q: file landed at %s, want it under %s", owner, f.Name(), dir)
		}
		got, ok := expandedPromptOwner(name)
		if !ok {
			t.Fatalf("owner %q: name %q did not parse as owned", owner, name)
		}
		if got != owner {
			t.Errorf("owner %q: name %q parsed back as %q", owner, name, got)
		}
	}
}

func TestExpandedPromptOwnerRejectsUnownedNames(t *testing.T) {
	// The pre-mg-5197 shape: prefix and suffix match, no owner field. It must
	// NOT parse as owned — that is what routes it to the legacy age branch
	// rather than to a live-set lookup against an owner nobody has.
	for _, name := range []string{
		"polecat-1234567890.md",
		"polecat-.99.md",
		"polecat-p5197.md",
		"something-else.md",
		"polecat-p5197.99.txt",
	} {
		if owner, ok := expandedPromptOwner(name); ok {
			t.Errorf("%q parsed as owned by %q, want unowned", name, owner)
		}
	}
}

func TestCreateExpandedPromptFileRefusesUnusableOwners(t *testing.T) {
	pinPromptTempDir(t)

	for _, owner := range []string{"", "..", "a/b"} {
		if f, err := createExpandedPromptFile(owner); err == nil {
			f.Close()
			t.Errorf("createExpandedPromptFile(%q) succeeded; an owner the sweep cannot key on must be refused", owner)
		}
	}
}

func TestExpandTemplateToFileRequiresAnOwner(t *testing.T) {
	pinPromptTempDir(t)
	tmplDir := t.TempDir()
	tmplPath := filepath.Join(tmplDir, "ownerless.md")
	if err := os.WriteFile(tmplPath, []byte("Task: {{.Task}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if path, err := ExpandTemplateToFile(tmplPath, "", TemplateVars{Task: "x"}); err == nil {
		os.Remove(path)
		t.Fatal("ExpandTemplateToFile accepted an empty owner; an unowned expanded prompt is exactly the file mg-5197 could only age out")
	}
}

// TestSweepExpandedPromptsKeepsALiveOwnersFile is the direction that matters.
// Every other case in this file trades disk for safety; this one is the failure
// the ownership key exists to prevent — a running polecat losing the prompt
// pogod re-reads to respawn it and the harness holds a path into.
func TestSweepExpandedPromptsKeepsALiveOwnersFile(t *testing.T) {
	dir := pinPromptTempDir(t)

	// Old enough that every age rule in the file would have taken it.
	live := writePromptFixture(t, dir, "polecat-p5197.111.md", 30*24*time.Hour)
	dead := writePromptFixture(t, dir, "polecat-pdead.222.md", 30*24*time.Hour)

	if n := SweepExpandedPrompts(map[string]bool{"p5197": true}); n != 1 {
		t.Errorf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live polecat's prompt was removed: %v", err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the dead polecat's prompt survived (stat err %v)", err)
	}
}

func TestSweepExpandedPromptsKeepsAnInFlightSpawnsFile(t *testing.T) {
	dir := pinPromptTempDir(t)

	// The file is written before the worktree, the mg claim and the process, so
	// a concurrent spawn's sweep can see it before its owner is registered
	// anywhere. Age is used only to refuse here — never to authorise removing an
	// owned file.
	inflight := writePromptFixture(t, dir, "polecat-pnew.333.md", time.Minute)
	stale := writePromptFixture(t, dir, "polecat-pold.444.md", PromptSpawnGrace+time.Minute)

	if n := SweepExpandedPrompts(map[string]bool{}); n != 1 {
		t.Errorf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(inflight); err != nil {
		t.Errorf("an in-flight spawn's prompt was removed inside the %s grace: %v", PromptSpawnGrace, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an unowned, past-grace prompt survived (stat err %v)", err)
	}
}

func TestSweepExpandedPromptsAgesOutLegacyNames(t *testing.T) {
	dir := pinPromptTempDir(t)

	// The 15,971 files measured on 2026-08-12 all have this shape. Age is the
	// only signal available about them, and the week is sized so a polecat still
	// running under the pre-fix binary — which holds one of these paths and has
	// no owner field to be recognised by — keeps it.
	old := writePromptFixture(t, dir, "polecat-1234567890.md", PromptLegacyStaleAfter+time.Hour)
	recent := writePromptFixture(t, dir, "polecat-987654321.md", PromptLegacyStaleAfter-time.Hour)

	if n := SweepExpandedPrompts(map[string]bool{}); n != 1 {
		t.Errorf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a legacy prompt older than %s survived (stat err %v)", PromptLegacyStaleAfter, err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a legacy prompt younger than %s was removed: %v", PromptLegacyStaleAfter, err)
	}
}

func TestSweepExpandedPromptsLeavesForeignFilesAlone(t *testing.T) {
	dir := pinPromptTempDir(t)

	// The directory is pogo's; a file in it that pogo did not write is still not
	// pogo's to delete. Old enough that both age branches would have taken it.
	foreign := writePromptFixture(t, dir, "notes.txt", 90*24*time.Hour)
	subdir := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	if n := SweepExpandedPrompts(map[string]bool{}); n != 0 {
		t.Errorf("swept %d files, want 0", n)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file this package did not write was removed: %v", err)
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Errorf("a directory was removed: %v", err)
	}
}

// TestPromptTempDirRefusesASymlink carries testtmp's refusal, which is the half
// of mg-de3c's mechanism that generalises unchanged: os.TempDir falls back to a
// world-writable /tmp when TMPDIR is unset, MkdirAll follows a planted symlink
// and reports success, and the sweep would then be deleting files of somebody
// else's choosing.
func TestPromptTempDirRefusesASymlink(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)

	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, PromptTempDir()); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	victim := filepath.Join(elsewhere, "polecat-pvictim.1.md")
	if err := os.WriteFile(victim, []byte("someone else's file"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := ensurePromptTempDir(); err == nil {
		t.Error("ensurePromptTempDir worked through a symlink; it must refuse")
	}
	if f, err := createExpandedPromptFile("pnew"); err == nil {
		f.Close()
		t.Error("createExpandedPromptFile wrote through a symlink")
	}
	if n := SweepExpandedPrompts(map[string]bool{}); n != 0 {
		t.Errorf("the sweep removed %d file(s) through a symlink", n)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the sweep deleted a file outside $TMPDIR through a planted symlink: %v", err)
	}
}

// TestRemoveExpandedPromptRefusesACrewPersona is the enumeration of how this
// remedy could exhibit the class it fixes. pogod's exit callback fires for EVERY
// agent, and a crew agent's PromptFile is a real, hand-maintained file under
// ~/.pogo/agents/crew. An unguarded remove there trades an unbounded temp leak
// for silent destruction of checked-in configuration on the first clean exit.
func TestRemoveExpandedPromptRefusesACrewPersona(t *testing.T) {
	pinPromptTempDir(t)

	crewDir := t.TempDir()
	persona := filepath.Join(crewDir, "pm-pogo.md")
	if err := os.WriteFile(persona, []byte("# pm-pogo"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same basename shape as a real expanded prompt, in the wrong directory:
	// the directory check, not the name, is what has to catch this.
	lookalike := filepath.Join(crewDir, "polecat-pm-pogo.9.md")
	if err := os.WriteFile(lookalike, []byte("# lookalike"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{persona, lookalike, ""} {
		removed, err := RemoveExpandedPrompt(path)
		if err != nil {
			t.Errorf("RemoveExpandedPrompt(%q) errored: %v", path, err)
		}
		if removed {
			t.Errorf("RemoveExpandedPrompt(%q) reported a removal outside %s", path, PromptTempDir())
		}
	}
	for _, path := range []string{persona, lookalike} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was deleted: %v", path, err)
		}
	}
}

func TestRemoveExpandedPromptRemovesTheAgentsOwnFile(t *testing.T) {
	dir := pinPromptTempDir(t)

	f, err := createExpandedPromptFile("p5197")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	path := f.Name()

	removed, err := RemoveExpandedPrompt(path)
	if err != nil {
		t.Fatalf("RemoveExpandedPrompt: %v", err)
	}
	if !removed {
		t.Fatal("RemoveExpandedPrompt reported no removal for a file it wrote itself")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survived (stat err %v)", path, err)
	}

	// Idempotent: the sweep may have taken it first, and the exit callback must
	// not report that as a failure.
	removed, err = RemoveExpandedPrompt(path)
	if err != nil || removed {
		t.Errorf("second RemoveExpandedPrompt = (%v, %v), want (false, nil)", removed, err)
	}

	// A foreign file in the same directory is still refused by the name check.
	foreign := writePromptFixture(t, dir, "notes.txt", 0)
	if removed, err := RemoveExpandedPrompt(foreign); removed || err != nil {
		t.Errorf("RemoveExpandedPrompt(%q) = (%v, %v), want (false, nil)", foreign, removed, err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file this package did not write was removed: %v", err)
	}
}

// TestRegistryRemoveRemovesTheSpawnsPrompt covers the mechanism that actually
// bounds the directory. The sweep is residue-only; this is the line that runs on
// every normal exit, and it runs from Stop's teardown branches and from pogod's
// exit callback alike because both funnel through Remove.
func TestRegistryRemoveRemovesTheSpawnsPrompt(t *testing.T) {
	dir := pinPromptTempDir(t)

	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	prompt := writePromptFixture(t, dir, "polecat-pdone.1.md", 0)
	crewDir := t.TempDir()
	persona := filepath.Join(crewDir, "pm-pogo.md")
	if err := os.WriteFile(persona, []byte("# pm-pogo"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg.agents["pdone"] = &Agent{Name: "pdone", Type: TypePolecat, PromptFile: prompt}
	reg.agents["pm-pogo"] = &Agent{Name: "pm-pogo", Type: TypeCrew, PromptFile: persona}

	reg.Remove("pdone")
	if _, err := os.Stat(prompt); !os.IsNotExist(err) {
		t.Errorf("the departed polecat's prompt survived Remove (stat err %v)", err)
	}

	// The crew arm: this callback fires for crew too, and a crew agent's prompt
	// is a real, hand-maintained persona. Removing it would trade an unbounded
	// temp leak for silent destruction of checked-in configuration.
	reg.Remove("pm-pogo")
	if _, err := os.Stat(persona); err != nil {
		t.Errorf("Remove deleted a crew agent's persona file: %v", err)
	}

	// And Remove still does its own job.
	if reg.Get("pdone") != nil || reg.Get("pm-pogo") != nil {
		t.Error("Remove left a registry entry behind")
	}

	// An agent that was never registered must not panic or remove anything.
	reg.Remove("never-existed")
}

// TestSweepExpandedPromptsKeepsARestartAwaitingPolecatsFile pins the reason
// pogod's livePolecatSet projection reads Registry.List and not
// Registry.Polecats: Polecats filters on alive(), so a polecat that has exited
// and is awaiting its restart_on_crash respawn would drop out of the live set
// while pogod is still holding its prompt path to respawn it with.
func TestSweepExpandedPromptsKeepsARestartAwaitingPolecatsFile(t *testing.T) {
	dir := pinPromptTempDir(t)

	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg.agents["prunning"] = &Agent{Name: "prunning", Type: TypePolecat, Status: StatusRunning}
	reg.agents["pexited"] = &Agent{Name: "pexited", Type: TypePolecat, Status: StatusExited}

	var names []string
	for _, a := range reg.List() {
		if a.Type == TypePolecat {
			names = append(names, a.Name)
		}
	}
	if len(names) != 2 {
		t.Fatalf("Registry.List projected %v; an exited-but-registered polecat must stay in the live set", names)
	}

	running := writePromptFixture(t, dir, "polecat-prunning.1.md", 30*24*time.Hour)
	exited := writePromptFixture(t, dir, "polecat-pexited.2.md", 30*24*time.Hour)
	gone := writePromptFixture(t, dir, "polecat-pgone.3.md", 30*24*time.Hour)

	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
	}
	if n := SweepExpandedPrompts(live); n != 1 {
		t.Errorf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(running); err != nil {
		t.Errorf("a running polecat's prompt was removed: %v", err)
	}
	if _, err := os.Stat(exited); err != nil {
		t.Errorf("a registered-but-exited polecat's prompt was removed — it is still awaiting respawn: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("an unregistered polecat's prompt survived (stat err %v)", err)
	}
}

// TestExpandTemplateToFileNamesTheOwner is the join between the two halves: the
// production expander has to write a name the sweep can key on, or the removal
// mechanism above is exercised only by fixtures.
func TestExpandTemplateToFileNamesTheOwner(t *testing.T) {
	dir := pinPromptTempDir(t)
	tmplDir := t.TempDir()
	tmplPath := filepath.Join(tmplDir, "ownednaming.md")
	if err := os.WriteFile(tmplPath, []byte("Task: {{.Task}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := ExpandTemplateToFile(tmplPath, "p5197", TemplateVars{Task: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })

	if filepath.Dir(path) != dir {
		t.Fatalf("expanded prompt landed at %s, want it under %s", path, dir)
	}
	owner, ok := expandedPromptOwner(filepath.Base(path))
	if !ok || owner != "p5197" {
		t.Fatalf("ExpandTemplateToFile wrote %q, which parses as (%q, %v); the sweep cannot key on it",
			filepath.Base(path), owner, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ship it") {
		t.Errorf("expanded prompt contents = %q", data)
	}
}
