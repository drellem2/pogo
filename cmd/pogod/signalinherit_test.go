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

// drellem2/pogo#106: a pogod launched with SIGHUP ignored (nohup, a wrapper
// that ran `trap '' HUP`) used to pass SIG_IGN to every agent it spawned, so
// polecats survived the PTY hangup and outlived pogod. The same held for
// SIGINT under a `&` job of a shell without job control. catchIgnoredSignals
// converts the inherited ignore into catch-and-discard; execve resets caught
// signals to SIG_DFL, so children get default while pogod stays immune.
//
// The test needs a process that INHERITED the signal as ignored — Go treats an
// inherited SIG_IGN for SIGHUP and SIGINT specially at startup, so calling
// signal.Ignore in-process is not the same state. So it runs three levels of
// this binary:
//
//	launcher (signal.Ignore(sig), like nohup or `&`)
//	  └ pogod stand-in (inherits SIG_IGN; optionally installSignalRecorder)
//	      └ reporter (prints its own disposition for sig)
//
// and a /bin/sh child that sends itself the signal, as a behavioural check
// that does not go through Go's own view of the disposition.

const (
	sigInheritModeEnv   = "POGOD_SIG_INHERIT_MODE"
	sigInheritSetupEnv  = "POGOD_SIG_INHERIT_SETUP"
	sigInheritSignalEnv = "POGOD_SIG_INHERIT_SIGNAL"
)

var sigInheritSignals = map[string]syscall.Signal{
	"HUP":  syscall.SIGHUP,
	"INT":  syscall.SIGINT,
	"QUIT": syscall.SIGQUIT,
}

func TestSignalInheritHelper(t *testing.T) {
	mode := os.Getenv(sigInheritModeEnv)
	if mode == "" {
		t.Skip("helper process for TestIgnoredSignalsAreNotPassedToChildren; not a standalone test")
	}
	short := os.Getenv(sigInheritSignalEnv)
	sig, ok := sigInheritSignals[short]
	if !ok {
		fmt.Printf("helper: unknown %s=%q\n", sigInheritSignalEnv, short)
		return
	}
	switch mode {
	case "launcher":
		signal.Ignore(sig)
		out, err := sigInheritRun("pogod")
		fmt.Print(out)
		if err != nil {
			fmt.Printf("launcher: pogod stand-in failed: %v\n", err)
		}
	case "pogod":
		fmt.Printf("pogod_inherited_ignored=%v\n", signal.Ignored(sig))
		if os.Getenv(sigInheritSetupEnv) == "1" {
			// The real wiring, not catchIgnoredSignals alone: main calls
			// installSignalRecorder, and that is what must do the conversion,
			// log it, and put it on the event spine.
			log.SetOutput(os.Stdout)
			(*lifecycle)(nil).installSignalRecorder()
			fmt.Printf("pogod_caught=%v\n", !signal.Ignored(sig))
			spine := ""
			if testEventLogPath != "" {
				raw, _ := os.ReadFile(testEventLogPath)
				spine = string(raw)
			}
			fmt.Printf("pogod_event_emitted=%v\n",
				strings.Contains(spine, `"`+daemonlife.EventSignalIgnoredAtLaunch+`"`) &&
					strings.Contains(spine, `"signal":"`+daemonlife.SignalName(sig)+`"`))
		}
		// An inherited-ignored HUP/INT must leave pogod immune either way:
		// send it to ourselves and survive. (SIGQUIT is not inherited as
		// ignored by a Go process, so it would kill the stand-in — skip it.)
		if sig != syscall.SIGQUIT {
			_ = syscall.Kill(os.Getpid(), sig)
			time.Sleep(200 * time.Millisecond)
			fmt.Println("pogod_survived_signal=true")
		}

		out, err := sigInheritRun("report")
		fmt.Print(out)
		if err != nil {
			fmt.Printf("pogod: reporter failed: %v\n", err)
		}
		// `kill -<sig> $$` kills a shell at the signal's default disposition
		// and is a no-op when the shell inherited it ignored.
		sh := exec.Command("/bin/sh", "-c", "kill -"+short+` $$; echo survived`)
		shOut, shErr := sh.Output()
		fmt.Printf("sh_child_survived_signal=%v\n", shErr == nil && strings.Contains(string(shOut), "survived"))
	case "report":
		fmt.Printf("child_signal_ignored=%v\n", signal.Ignored(sig))
	}
}

// sigInheritRun re-execs this test binary on the helper in mode, passing the
// rest of the environment through, and returns its stdout.
func sigInheritRun(mode string) (string, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalInheritHelper$", "-test.timeout=60s")
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, sigInheritModeEnv+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, sigInheritModeEnv+"="+mode)
	out, err := cmd.Output()
	return string(out), err
}

func sigInheritScenario(t *testing.T, short string, setup bool) map[string]string {
	t.Helper()
	v := "0"
	if setup {
		v = "1"
	}
	t.Setenv(sigInheritSetupEnv, v)
	t.Setenv(sigInheritSignalEnv, short)
	out, err := sigInheritRun("launcher")
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
	t.Logf("SIG%s setup=%v:\n%s", short, setup, out)
	return got
}

func TestIgnoredSignalsAreNotPassedToChildren(t *testing.T) {
	for _, tc := range []struct {
		short, hint string
	}{
		{"HUP", "nohup?"},
		{"INT", "started as a background job?"},
	} {
		t.Run("SIG"+tc.short, func(t *testing.T) {
			// Precondition, not the claim: the launcher's ignore must actually
			// reach the pogod stand-in, or neither arm below measures anything.
			control := sigInheritScenario(t, tc.short, false)
			if control["pogod_inherited_ignored"] != "true" {
				t.Fatalf("pogod stand-in did not inherit SIG%s as ignored (%v); the scenario cannot measure #106", tc.short, control)
			}

			// Positive control: without the setup call the ignore reaches the
			// child. If this ever reads false the instrument is broken, and the
			// arm below passing would say nothing.
			if control["child_signal_ignored"] != "true" || control["sh_child_survived_signal"] != "true" {
				t.Fatalf("control (no setup): child should inherit SIG_IGN for SIG%s and survive it, got %v", tc.short, control)
			}
			if control["pogod_survived_signal"] != "true" {
				t.Fatalf("control: pogod stand-in did not survive SIG%s: %v", tc.short, control)
			}

			fixed := sigInheritScenario(t, tc.short, true)
			if fixed["pogod_inherited_ignored"] != "true" || fixed["pogod_caught"] != "true" {
				t.Fatalf("catchIgnoredSignals did not convert an inherited-ignored SIG%s: %v", tc.short, fixed)
			}
			if fixed["pogod_event_emitted"] != "true" {
				t.Errorf("installSignalRecorder did not emit %s for SIG%s: %v", daemonlife.EventSignalIgnoredAtLaunch, tc.short, fixed)
			}
			wantLog := "SIG" + tc.short + " was ignored at launch (" + tc.hint + "); pogod stays immune, children get default"
			if !strings.Contains(fixed["raw"], wantLog) {
				t.Errorf("installSignalRecorder did not log %q at startup:\n%s", wantLog, fixed["raw"])
			}
			if fixed["pogod_survived_signal"] != "true" {
				t.Fatalf("pogod must stay immune to SIG%s after catch-and-discard; got %v", tc.short, fixed)
			}
			if fixed["child_signal_ignored"] != "false" {
				t.Errorf("child of a pogod that caught SIG%s still reports it ignored (drellem2/pogo#106): %v", tc.short, fixed)
			}
			if fixed["sh_child_survived_signal"] != "false" {
				t.Errorf("/bin/sh child of a pogod that caught SIG%s survived `kill -%s $$`, so it inherited the ignore: %v", tc.short, tc.short, fixed)
			}
		})
	}

	// SIGQUIT is left out of inheritableIgnores because the Go runtime already
	// overrides an inherited ignore for it. Pin that premise: if a future Go
	// starts honouring it, this fails and SIGQUIT needs the same treatment.
	t.Run("SIGQUIT", func(t *testing.T) {
		control := sigInheritScenario(t, "QUIT", false)
		if control["pogod_inherited_ignored"] != "false" {
			t.Errorf("a Go process now inherits SIGQUIT as ignored; add it to inheritableIgnores: %v", control)
		}
		if control["child_signal_ignored"] != "false" || control["sh_child_survived_signal"] != "false" {
			t.Errorf("SIGQUIT's inherited ignore now reaches pogod's children; add it to inheritableIgnores: %v", control)
		}
		// The instrument can see an ignored SIGQUIT at all: the launcher's own
		// direct /bin/sh child (no Go process in between) must survive it.
		sh := exec.Command("/bin/sh", "-c", `trap '' QUIT; sh -c 'kill -QUIT $$; echo survived'`)
		out, err := sh.Output()
		if err != nil || !strings.Contains(string(out), "survived") {
			t.Fatalf("positive control: a /bin/sh child with SIGQUIT ignored should survive kill -QUIT, got %q err=%v", out, err)
		}
	})
}

// At default disposition there is nothing to convert: pogod must not catch
// SIGHUP or SIGINT itself, which would stop it dying of them (the recorder
// owns that path).
func TestCatchIgnoredSignalsIsANoOpWhenNotIgnored(t *testing.T) {
	for _, s := range inheritableIgnores {
		if signal.Ignored(s) {
			t.Skipf("this test process inherited %v as ignored; the no-op case is not observable here", s)
		}
	}
	if got := catchIgnoredSignals(); len(got) != 0 {
		t.Fatalf("catchIgnoredSignals converted %v with every signal at default", got)
	}
}
