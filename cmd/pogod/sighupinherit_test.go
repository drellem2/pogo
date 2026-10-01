package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/daemonlife"
)

// drellem2/pogo#106: a pogod launched with SIGHUP ignored (nohup, a `trap ''
// HUP` wrapper) used to pass SIG_IGN to every agent it spawned, so polecats
// survived the PTY hangup and outlived pogod. catchIgnoredSIGHUP converts the
// inherited ignore into catch-and-discard; execve resets caught signals to
// SIG_DFL, so children get default while pogod stays immune.
//
// The test needs a process that INHERITED SIGHUP as ignored — Go treats an
// inherited SIG_IGN for SIGHUP specially at startup, so calling signal.Ignore
// in-process is not the same state. So it runs three levels of this binary:
//
//	launcher (signal.Ignore(SIGHUP), like nohup)
//	  └ pogod stand-in (inherits SIG_IGN; optionally calls catchIgnoredSIGHUP)
//	      └ reporter (prints its own SIGHUP disposition)
//
// and a /bin/sh child that hangs itself up, as a behavioural check that does
// not go through Go's own view of the disposition.

const (
	sighupInheritModeEnv  = "POGOD_SIGHUP_INHERIT_MODE"
	sighupInheritSetupEnv = "POGOD_SIGHUP_INHERIT_SETUP"
	sighupInheritBegin    = "=== SIGHUP-INHERIT BEGIN ==="
	sighupInheritEnd      = "=== SIGHUP-INHERIT END ==="
)

func TestSIGHUPInheritHelper(t *testing.T) {
	mode := os.Getenv(sighupInheritModeEnv)
	if mode == "" {
		t.Skip("helper process for TestIgnoredSIGHUPIsNotPassedToChildren; not a standalone test")
	}
	switch mode {
	case "launcher":
		signal.Ignore(syscall.SIGHUP)
		out, err := sighupInheritRun("pogod")
		fmt.Print(out)
		if err != nil {
			fmt.Printf("launcher: pogod stand-in failed: %v\n", err)
		}
	case "pogod":
		fmt.Printf("pogod_inherited_ignored=%v\n", signal.Ignored(syscall.SIGHUP))
		if os.Getenv(sighupInheritSetupEnv) == "1" {
			// The real wiring, not catchIgnoredSIGHUP alone: main calls
			// installSignalRecorder, and that is what must do the conversion,
			// log it, and put it on the event spine.
			log.SetOutput(os.Stdout)
			(*lifecycle)(nil).installSignalRecorder()
			fmt.Printf("pogod_caught=%v\n", !signal.Ignored(syscall.SIGHUP))
			spine := ""
			if testEventLogPath != "" {
				raw, _ := os.ReadFile(testEventLogPath)
				spine = string(raw)
			}
			fmt.Printf("pogod_event_emitted=%v\n", strings.Contains(spine, `"`+daemonlife.EventSIGHUPIgnoredAtLaunch+`"`))
		}
		// pogod must stay immune either way: hang ourselves up and survive.
		_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		time.Sleep(200 * time.Millisecond)
		fmt.Println("pogod_survived_sighup=true")

		out, err := sighupInheritRun("report")
		fmt.Print(out)
		if err != nil {
			fmt.Printf("pogod: reporter failed: %v\n", err)
		}
		// `kill -HUP $$` kills a shell at SIGHUP's default disposition and is a
		// no-op when the shell inherited it ignored.
		sh := exec.Command("/bin/sh", "-c", `kill -HUP $$; echo survived`)
		shOut, shErr := sh.Output()
		fmt.Printf("sh_child_survived_sighup=%v\n", shErr == nil && strings.Contains(string(shOut), "survived"))
	case "report":
		fmt.Printf("child_sighup_ignored=%v\n", signal.Ignored(syscall.SIGHUP))
	}
}

// sighupInheritRun re-execs this test binary on the helper in mode, passing the
// rest of the environment through, and returns its stdout.
func sighupInheritRun(mode string) (string, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGHUPInheritHelper$", "-test.timeout=60s")
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, sighupInheritModeEnv+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, sighupInheritModeEnv+"="+mode)
	out, err := cmd.Output()
	return string(out), err
}

func sighupInheritScenario(t *testing.T, setup bool) map[string]string {
	t.Helper()
	v := "0"
	if setup {
		v = "1"
	}
	t.Setenv(sighupInheritSetupEnv, v)
	out, err := sighupInheritRun("launcher")
	if err != nil {
		t.Fatalf("launcher: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.ContainsAny(k, " :") {
			got[k] = val
		}
	}
	got["raw"] = out
	t.Logf("setup=%v:\n%s", setup, out)
	return got
}

func TestIgnoredSIGHUPIsNotPassedToChildren(t *testing.T) {
	// Precondition, not the claim: the launcher's ignore must actually reach
	// the pogod stand-in, or neither arm below measures anything.
	control := sighupInheritScenario(t, false)
	if control["pogod_inherited_ignored"] != "true" {
		t.Fatalf("pogod stand-in did not inherit SIGHUP as ignored (%v); the scenario cannot measure #106", control)
	}

	// Positive control: without the setup call the ignore reaches the child.
	// If this ever reads false the instrument is broken, and the arm below
	// passing would say nothing.
	if control["child_sighup_ignored"] != "true" || control["sh_child_survived_sighup"] != "true" {
		t.Fatalf("control (no setup): child should inherit SIG_IGN and survive SIGHUP, got %v", control)
	}
	if control["pogod_survived_sighup"] != "true" {
		t.Fatalf("control: pogod stand-in did not survive SIGHUP: %v", control)
	}

	fixed := sighupInheritScenario(t, true)
	if fixed["pogod_inherited_ignored"] != "true" || fixed["pogod_caught"] != "true" {
		t.Fatalf("catchIgnoredSIGHUP did not report converting an inherited ignore: %v", fixed)
	}
	if fixed["pogod_event_emitted"] != "true" {
		t.Errorf("installSignalRecorder did not emit %s: %v", daemonlife.EventSIGHUPIgnoredAtLaunch, fixed)
	}
	if fixed["raw"] == "" || !strings.Contains(fixed["raw"], "SIGHUP was ignored at launch (nohup?); pogod stays immune, children get default") {
		t.Errorf("installSignalRecorder did not log the conversion at startup:\n%s", fixed["raw"])
	}
	if fixed["pogod_survived_sighup"] != "true" {
		t.Fatalf("pogod must stay immune to SIGHUP after catch-and-discard; got %v", fixed)
	}
	if fixed["child_sighup_ignored"] != "false" {
		t.Errorf("child of a caught-SIGHUP pogod still reports SIGHUP ignored (drellem2/pogo#106): %v", fixed)
	}
	if fixed["sh_child_survived_sighup"] != "false" {
		t.Errorf("/bin/sh child of a caught-SIGHUP pogod survived `kill -HUP $$` — it inherited the ignore: %v", fixed)
	}
}

// At SIGHUP's default disposition there is nothing to convert: pogod must not
// catch SIGHUP itself, which would stop it dying of a hangup (the recorder owns
// that path).
func TestCatchIgnoredSIGHUPIsANoOpWhenNotIgnored(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("this test process inherited SIGHUP as ignored; the no-op case is not observable here")
	}
	if catchIgnoredSIGHUP() {
		t.Fatal("catchIgnoredSIGHUP reported a conversion with SIGHUP at default")
	}
}
