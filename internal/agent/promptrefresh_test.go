package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
	"github.com/drellem2/pogo/internal/turnlog"
)

// drellem2/pogo#306: a crash restart (and so every crew-reset, which comes
// back through restart_on_crash) must launch on the prompt as it is on disk at
// restart time, not on the synthesized copy written at the original start.

// promptCaptureRig starts a registry whose crew command is a script that copies
// the prompt file it was handed (its last argument) to the next numbered
// capture, records that path, and exits.
type promptCaptureRig struct {
	reg    *Registry
	capDir string
}

func newPromptCaptureRig(t *testing.T) *promptCaptureRig {
	t.Helper()
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	capDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "capture.sh")
	body := "#!/bin/sh\n" +
		"eval last=\\${$#}\n" +
		"n=$(ls '" + capDir + "' | grep -c '\\.prompt$')\n" +
		"printf '%s' \"$last\" > '" + capDir + "'/$n.path\n" +
		"cat \"$last\" > '" + capDir + "'/$n.prompt\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	reg.SetCommandConfig(fixedCommandConfig(script + " {{.PromptFile}}"))
	return &promptCaptureRig{reg: reg, capDir: capDir}
}

// launched returns the prompt text and path the n-th launch ran on.
func (r *promptCaptureRig) launched(t *testing.T, n int) (string, string) {
	t.Helper()
	base := filepath.Join(r.capDir, string(rune('0'+n)))
	text, err := os.ReadFile(base + ".prompt")
	if err != nil {
		t.Fatalf("launch %d captured no prompt: %v", n, err)
	}
	path, err := os.ReadFile(base + ".path")
	if err != nil {
		t.Fatalf("launch %d captured no path: %v", n, err)
	}
	return string(text), string(path)
}

func writePromptFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

const crewFM = "+++\nrestart_on_crash = false\n+++\n"

// A plain crew prompt (no extends): edit it after the synthesized copy
// exists, restart, and the edit is what launches.
func TestRespawnRegeneratesPlainCrewPrompt(t *testing.T) {
	rig := newPromptCaptureRig(t)
	src := filepath.Join(CrewPromptDir(), "plaincrew.md")
	writePromptFile(t, src, crewFM+"# plaincrew\nBOOT: register OLD-SCHEDULE\n")

	a, err := rig.reg.StartCrewAgent("plaincrew")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	waitDone(t, a)
	first, firstPath := rig.launched(t, 0)
	if !strings.Contains(first, "OLD-SCHEDULE") {
		t.Fatalf("first launch prompt lacks the original content:\n%s", first)
	}
	synth := filepath.Join(PromptDir(), "plaincrew", "synthesized-prompt.md")
	if firstPath != synth {
		t.Fatalf("first launch ran on %s, want the synthesized copy %s", firstPath, synth)
	}

	// The operator retires the schedule. The synthesized copy still says OLD.
	writePromptFile(t, src, crewFM+"# plaincrew\nBOOT: schedule is OFF, do not register it\n")

	b, err := rig.reg.Respawn("plaincrew")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)
	second, secondPath := rig.launched(t, 1)
	if strings.Contains(second, "OLD-SCHEDULE") {
		t.Errorf("restart launched on the stale synthesized prompt:\n%s", second)
	}
	if !strings.Contains(second, "schedule is OFF") {
		t.Errorf("restart prompt lacks the edited source content:\n%s", second)
	}
	if secondPath != synth || b.PromptFile != synth {
		t.Errorf("restart ran on %s (PromptFile %s), want the regenerated %s", secondPath, b.PromptFile, synth)
	}
	// The regenerated copy keeps the synthesized-only layers.
	if !strings.Contains(second, turnlog.ClauseMarker) {
		t.Errorf("regenerated prompt lost the turn-log clause:\n%s", second)
	}
}

// An `extends <template> with config <config>` stub: editing the CONFIG
// (not the stub) after start must reach the restart too.
func TestRespawnRegeneratesExtendsChain(t *testing.T) {
	rig := newPromptCaptureRig(t)
	writePromptFile(t, filepath.Join(PromptDir(), "pm", "pm-template.md"), crewFM+"# PM template\n")
	cfg := filepath.Join(PromptDir(), "pm", "widget.toml")
	writePromptFile(t, cfg, "scan = \"OLD-SCAN\"\n")
	writePromptFile(t, filepath.Join(CrewPromptDir(), "pm-widget.md"), "extends pm-template with config pm/widget.toml\n")

	a, err := rig.reg.StartCrewAgent("pm-widget")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	waitDone(t, a)
	if first, _ := rig.launched(t, 0); !strings.Contains(first, "OLD-SCAN") {
		t.Fatalf("first launch prompt lacks the config:\n%s", first)
	}

	writePromptFile(t, cfg, "scan = \"NEW-SCAN\"\n")
	b, err := rig.reg.Respawn("pm-widget")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)
	second, _ := rig.launched(t, 1)
	if strings.Contains(second, "OLD-SCAN") || !strings.Contains(second, "NEW-SCAN") {
		t.Errorf("restart did not pick up the edited config:\n%s", second)
	}
	if !strings.Contains(second, "# PM template") {
		t.Errorf("restart prompt is not the merged template:\n%s", second)
	}
}

// Synthesis fails at restart (the config vanished): the agent launches on the
// SOURCE stub, not on the stale synthesized copy — and the argv says so.
func TestRespawnFallsBackToSourceWhenRegenerationFails(t *testing.T) {
	rig := newPromptCaptureRig(t)
	writePromptFile(t, filepath.Join(PromptDir(), "pm", "pm-template.md"), crewFM+"# PM template\n")
	cfg := filepath.Join(PromptDir(), "pm", "gadget.toml")
	writePromptFile(t, cfg, "scan = \"OLD-SCAN\"\n")
	stub := filepath.Join(CrewPromptDir(), "pm-gadget.md")
	writePromptFile(t, stub, "extends pm-template with config pm/gadget.toml\n")

	a, err := rig.reg.StartCrewAgent("pm-gadget")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	waitDone(t, a)
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}

	b, err := rig.reg.Respawn("pm-gadget")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)
	second, path := rig.launched(t, 1)
	if strings.Contains(second, "OLD-SCAN") {
		t.Errorf("fallback launched on the stale synthesized copy:\n%s", second)
	}
	if path != stub || b.PromptFile != stub {
		t.Errorf("fallback ran on %s (PromptFile %s), want the source %s", path, b.PromptFile, stub)
	}

	// Once the config is back, the next restart regenerates again rather than
	// staying pinned to the source.
	writePromptFile(t, cfg, "scan = \"BACK-SCAN\"\n")
	c, err := rig.reg.Respawn("pm-gadget")
	if err != nil {
		t.Fatalf("second Respawn: %v", err)
	}
	waitDone(t, c)
	third, path3 := rig.launched(t, 2)
	synth := filepath.Join(PromptDir(), "pm-gadget", "synthesized-prompt.md")
	if !strings.Contains(third, "BACK-SCAN") || path3 != synth {
		t.Errorf("recovery restart ran on %s, content:\n%s", path3, third)
	}
}

// The source is deleted (a retirement): refuse rather than relaunch the
// stale copy.
func TestRespawnRefusesWhenSourceIsGone(t *testing.T) {
	rig := newPromptCaptureRig(t)
	src := filepath.Join(CrewPromptDir(), "gonecrew.md")
	writePromptFile(t, src, crewFM+"# gonecrew\n")
	a, err := rig.reg.StartCrewAgent("gonecrew")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	waitDone(t, a)
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.reg.Respawn("gonecrew"); err == nil {
		t.Fatal("Respawn succeeded with the prompt source deleted; want a refusal")
	}
}

// Agents not started from a crew prompt respawn exactly as before.
func TestRefreshRespawnPromptNoSourceIsUnchanged(t *testing.T) {
	cmd := []string{"x", "--append-system-prompt-file", "/p/prompt.md"}
	got, p, err := refreshRespawnPrompt("n", "", "", "/p/prompt.md", cmd)
	if err != nil || p != "/p/prompt.md" || strings.Join(got, " ") != strings.Join(cmd, " ") {
		t.Errorf("got %q %q %v", got, p, err)
	}
	if promptSynthFor("/s.md", "/s.md") != "" {
		t.Error("promptSynthFor must never target the source file itself")
	}
}
