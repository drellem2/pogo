package agent

import (
	"errors"
	"testing"
	"time"
)

// TestSpawnInstallsTheMailRecipientHook is the wiring proof for mg-d924.
//
// Every other piece — the parser, the roster read, the warning text, the
// settings merge — can be individually correct while no real agent ever gets
// the hook, and the symptom of that would be a send to a stopped agent
// reporting Delivered with no warning: exactly the behaviour before the fix,
// and indistinguishable from it. So the spawn path is asserted directly.
func TestSpawnInstallsTheMailRecipientHook(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	bin := fakePogoBinary(t)

	var installedDir, installedCmd string
	provider := &Provider{
		ID:    "fake",
		Nudge: DefaultNudgeProfile,
		MailRecipientHook: func(dir, hookCommand string) error {
			installedDir, installedCmd = dir, hookCommand
			return nil
		},
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)

	workdir := t.TempDir()
	if _, err := reg.Spawn(SpawnRequest{
		Name:    "warned",
		Type:    TypePolecat,
		Command: []string{"cat"},
		Dir:     workdir,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if installedDir != workdir {
		t.Errorf("hook installed into %q, want the agent's working directory %q", installedDir, workdir)
	}
	if want := bin + " hook mail-recipient"; installedCmd != want {
		t.Errorf("hook command = %q, want %q", installedCmd, want)
	}
}

// TestAFailingMailRecipientHookStillStartsTheAgent: an agent that runs without
// the warning is strictly better than one that does not run.
func TestAFailingMailRecipientHookStillStartsTheAgent(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	fakePogoBinary(t)

	provider := &Provider{
		ID:    "broken",
		Nudge: DefaultNudgeProfile,
		MailRecipientHook: func(dir, hookCommand string) error {
			return errors.New("settings.local.json is not writable")
		},
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)

	if _, err := reg.Spawn(SpawnRequest{
		Name:    "still-runs",
		Type:    TypePolecat,
		Command: []string{"cat"},
		Dir:     t.TempDir(),
	}); err != nil {
		t.Fatalf("Spawn refused because a best-effort hook failed: %v", err)
	}
}
