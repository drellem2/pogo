package testsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This package's own tests run inside the thing they test. That is deliberate:
// the positive direction has to be the default, or the negative cases below are
// proving something about a code path nothing uses.
var sandbox *Sandbox

func TestMain(m *testing.M) {
	sb, down := Main("testsandbox")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// the positive direction, first — a break case that "passes" against a sandbox
// that was never working proves nothing. An earlier draft of mg-78a5's own
// name-guard controls passed for exactly that reason.
// ---------------------------------------------------------------------------

func TestPackageSandboxIsEstablished(t *testing.T) {
	Verify(t, sandbox)

	for _, kv := range sandbox.vars() {
		got := os.Getenv(kv[0])
		if got != kv[1] {
			t.Errorf("$%s = %q, want %q", kv[0], got, kv[1])
		}
		if !under(resolve(got), resolve(sandbox.Root)) {
			t.Errorf("$%s = %s does not resolve under the sandbox root %s", kv[0], got, sandbox.Root)
		}
	}
}

// TestAllFourVariablesArePinned is the assertion the hand-rolled isolations did
// not make. Each of the three converted suites pinned HOME (and sometimes
// POGO_HOME) and left the rest of the machine's environment reachable.
func TestAllFourVariablesArePinned(t *testing.T) {
	sb := Isolate(t)

	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "POGO_HOME", "MG_ROOT"} {
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("$%s is unset after Isolate — the whole point is that all four are pinned", name)
		}
		if !under(resolve(v), resolve(sb.Root)) {
			t.Errorf("$%s = %s is outside this test's sandbox root %s", name, v, sb.Root)
		}
	}
}

// TestIsolateOverridesAnInheritedPogoHome is the machine-specific trap named in
// mg-5336 and again in mg-78a5: this box exports POGO_HOME=$HOME from a stale
// profile, so a helper that pins only HOME still writes the live tree.
func TestIsolateOverridesAnInheritedPogoHome(t *testing.T) {
	hostile := filepath.Join(t.TempDir(), "inherited-pogo-home")
	t.Setenv("POGO_HOME", hostile)

	sb := Isolate(t)

	if got := os.Getenv("POGO_HOME"); got == hostile {
		t.Fatalf("POGO_HOME is still the inherited %s — Isolate did not override it", hostile)
	} else if got != sb.PogoHome {
		t.Errorf("POGO_HOME = %s, want the sandbox's %s", got, sb.PogoHome)
	}
}

// ---------------------------------------------------------------------------
// the break cases. Every guarantee, refused on purpose.
//
// These drive prepare/verify against a SYNTHETIC live tree rather than the real
// one, so the hostile paths are real directories the test owns. Using the
// developer's actual home as the fixture would make the suite's own behaviour
// depend on the machine it runs on, which is the defect this package exists to
// retire.
// ---------------------------------------------------------------------------

// fakeLive builds a decoy "developer's machine": a home with a .pogo under it,
// holding a file whose survival is the thing at stake.
func fakeLive(t *testing.T) (liveTree, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "decoy-home")
	pogo := filepath.Join(home, ".pogo")
	if err := os.MkdirAll(filepath.Join(pogo, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pogo, "projects.json"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	return captureLive(home, ""), home
}

func TestRejectsARootThatIsTheDevelopersHome(t *testing.T) {
	live, home := fakeLive(t)

	if _, err := prepare(home, live); err == nil {
		t.Fatal("prepare accepted the developer's home as a sandbox root; teardown rm -rf's it")
	} else if !strings.Contains(err.Error(), "developer's home") {
		t.Errorf("error does not name the fault: %v", err)
	}
}

func TestRejectsARootInsideTheLivePogoTree(t *testing.T) {
	live, home := fakeLive(t)
	root := filepath.Join(home, ".pogo", "polecats", "sbx")

	if _, err := prepare(root, live); err == nil {
		t.Fatal("prepare accepted a root inside the live ~/.pogo")
	} else if !strings.Contains(err.Error(), "live pogo state") {
		t.Errorf("error does not name the fault: %v", err)
	}
}

func TestRejectsARootThatCONTAINSTheLivePogoTree(t *testing.T) {
	live, home := fakeLive(t)

	// The parent of ~/.pogo is not ~/.pogo, and it is not the home either when
	// the home is nested; it still gets removed with everything under it.
	if _, err := prepare(filepath.Dir(home), live); err == nil {
		t.Fatal("prepare accepted a root that contains the live ~/.pogo")
	}
}

// TestRejectsARootThatIsASymlinkOntoLiveState is the case string comparison
// cannot see, and the only one where hand-rolled isolation fails QUIETLY: every
// path reads as private right up until something follows the link.
func TestRejectsARootThatIsASymlinkOntoLiveState(t *testing.T) {
	live, home := fakeLive(t)
	decoy := filepath.Join(t.TempDir(), "looks-private")
	if err := os.Symlink(home, decoy); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	if _, err := prepare(decoy, live); err == nil {
		t.Fatalf("prepare accepted %s, which is a symlink to the developer's home %s", decoy, home)
	}

	// And the guarantee under the guarantee: the refusal happened before the
	// tree was touched.
	if _, err := os.Stat(filepath.Join(home, "home")); err == nil {
		t.Errorf("prepare created %s inside the developer's home on its way to refusing it",
			filepath.Join(home, "home"))
	}
}

// TestRejectsAVariableSymlinkedOutOfTheSandbox breaks the guarantee at the
// variable rather than at the root: the root is genuinely private, and one of
// the four directories under it is a link back to live state.
//
// The refusal has to come BEFORE the tree is created, because MkdirAll follows
// that link — checking afterwards would mean creating $HOME/.macguffin inside
// the developer's home on the way to reporting that we must not have.
func TestRejectsAVariableSymlinkedOutOfTheSandbox(t *testing.T) {
	live, home := fakeLive(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(root, "home")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	_, err := prepare(root, live)
	if err == nil {
		t.Fatal("prepare accepted a $HOME that is a symlink onto the developer's home")
	}
	if !strings.Contains(err.Error(), "HOME") {
		t.Errorf("error does not name the variable at fault: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".macguffin")); err == nil {
		t.Errorf("prepare created %s inside the developer's home before refusing it",
			filepath.Join(home, ".macguffin"))
	}
}

// TestVerifyRejectsAPinnedValueOnLiveState covers the same rule at the other
// end, after the variables have been applied. prepare gets there first for
// every shape this suite can build, so this drives verify directly: the check
// exists so that "the paths were vetted once, at setup" is not the only thing
// standing between a test and the developer's ~/.pogo.
func TestVerifyRejectsAPinnedValueOnLiveState(t *testing.T) {
	live, home := fakeLive(t)
	sb, err := prepare(filepath.Join(t.TempDir(), "root"), live)
	if err != nil {
		t.Fatal(err)
	}
	sb.PogoHome = filepath.Join(home, ".pogo")
	restore := setEnvForTest(t, sb)
	defer restore()

	err = verify(sb, live)
	if err == nil {
		t.Fatal("verify accepted a $POGO_HOME pointing at the developer's live pogo state")
	}
	if !strings.Contains(err.Error(), "POGO_HOME") {
		t.Errorf("error does not name the variable at fault: %v", err)
	}
}

// TestRejectsAnOverrideThatDidNotTake is guarantee 2: the value is read back
// out of the process, not trusted from the value we meant to write.
func TestRejectsAnOverrideThatDidNotTake(t *testing.T) {
	live, _ := fakeLive(t)
	sb, err := prepare(filepath.Join(t.TempDir(), "root"), live)
	if err != nil {
		t.Fatal(err)
	}
	restore := setEnvForTest(t, sb)
	defer restore()

	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "POGO_HOME", "MG_ROOT"} {
		t.Run(name+" unset", func(t *testing.T) {
			old := os.Getenv(name)
			os.Unsetenv(name)
			defer os.Setenv(name, old)

			err := verify(sb, live)
			if err == nil {
				t.Fatalf("verify passed with $%s unset", name)
			}
			if !strings.Contains(err.Error(), "$"+name) {
				t.Errorf("error does not name $%s: %v", name, err)
			}
		})
		t.Run(name+" overridden after the fact", func(t *testing.T) {
			old := os.Getenv(name)
			os.Setenv(name, filepath.Join(t.TempDir(), "somewhere-else"))
			defer os.Setenv(name, old)

			if err := verify(sb, live); err == nil {
				t.Fatalf("verify passed with $%s pointing somewhere the sandbox never created", name)
			}
		})
	}
}

// TestRejectsAPogoHomeThatIsNotEmpty catches the reused directory: a freshly
// created POGO_HOME holds nothing, so contents mean this is not the directory
// the run thinks it is.
func TestRejectsAPogoHomeThatIsNotEmpty(t *testing.T) {
	live, _ := fakeLive(t)
	root := filepath.Join(t.TempDir(), "root")

	sb, err := prepare(root, live)
	if err != nil {
		t.Fatalf("a fresh root did not prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sb.PogoHome, "pogo.pid"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-establishing over that tree is the reuse case: the directories already
	// exist, MkdirAll is a no-op, and the state in them is somebody else's.
	if _, err := prepare(root, live); err == nil {
		t.Fatal("prepare accepted a POGO_HOME holding somebody else's pogo.pid")
	} else if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error does not name the fault: %v", err)
	}
}

// TestPogoHomeEqualsHomeIsNormalisedLikeConfig locks the machine-specific trap
// into the live-tree capture: with POGO_HOME=$HOME (this box's stale profile),
// config.PogoHome() reads $HOME/.pogo, so that is what has to be guarded.
func TestPogoHomeEqualsHomeIsNormalisedLikeConfig(t *testing.T) {
	live := captureLive("/Users/nobody", "/Users/nobody")

	if got, want := len(live.pogo), 2; got != want {
		t.Fatalf("captureLive recorded %d live pogo roots, want %d: %v", got, want, live.pogo)
	}
	for _, p := range live.pogo {
		if p != "/Users/nobody/.pogo" {
			t.Errorf("live pogo root %q; POGO_HOME == HOME must normalise to $HOME/.pogo", p)
		}
	}
	if err := live.reject("/Users/nobody/.pogo/agents", "a path"); err == nil {
		t.Error("a path inside the normalised live tree was accepted")
	}
}

// TestUnpinnedPogoHomeStillGuardsTheFallbackRoute: with POGO_HOME unset the
// state root is $HOME/.pogo, which is the route all three converted suites
// leaked through once their TestMain cleared POGO_HOME and stopped there.
func TestUnpinnedPogoHomeStillGuardsTheFallbackRoute(t *testing.T) {
	live := captureLive("/Users/nobody", "")

	if err := live.reject("/Users/nobody/.pogo", "a path"); err == nil {
		t.Error("$HOME/.pogo was accepted with POGO_HOME unset — that is the fallback route")
	}
}

// ---------------------------------------------------------------------------
// the wiring. The checks above prove the rules; these two prove that failing a
// rule ends the run the way guarantee 5 says it does, in the real harness.
// ---------------------------------------------------------------------------

const helperEnv = "POGO_TESTSANDBOX_HELPER"

// fakeLiveHome makes a decoy "developer's machine" for a subprocess to treat as
// its live tree. The subprocess is handed it as $HOME, so the checks it runs
// are the real ones against a home this test owns — no case below depends on
// the machine it runs on, and none of them goes anywhere near the real ~/.pogo.
func fakeLiveHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "live-home")
	if err := os.MkdirAll(filepath.Join(home, ".pogo", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestBrokenIsolationIsAFatalHelper runs only as a subprocess. It asks for an
// isolation that cannot be established — a sandbox root that is a symlink onto
// the live pogo tree — and then, if execution somehow continues, prints a
// sentinel the parent asserts on.
func TestBrokenIsolationIsAFatalHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "isolate" {
		t.Skip("subprocess helper; driven by TestBrokenIsolationFatalsBeforeTheTestBody")
	}
	decoy := filepath.Join(t.TempDir(), "looks-private")
	if err := os.Symlink(realTree.pogo[0], decoy); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	t.Setenv(RootEnv, decoy)

	Isolate(t)

	t.Error("SENTINEL-EXECUTION-CONTINUED: the test body ran after a broken isolation")
}

// TestBrokenIsolationFatalsBeforeTheTestBody is the per-test half of guarantee
// 4: a broken isolation must stop the test at the isolation, naming it — not
// hand the body a hostile environment and let it report ordinary assertion
// failures about the developer's tree.
func TestBrokenIsolationFatalsBeforeTheTestBody(t *testing.T) {
	home := fakeLiveHome(t)

	out, err := runHelper(t, "-test.run=TestBrokenIsolationIsAFatalHelper",
		"HOME="+home, "POGO_HOME=", helperEnv+"=isolate")
	if err == nil {
		t.Fatalf("the helper passed; a broken isolation must fail it\n%s", out)
	}

	if !strings.Contains(out, FailPrefix) {
		t.Errorf("output does not carry %q — a reader cannot tell setup from regression:\n%s",
			FailPrefix, out)
	}
	if !strings.Contains(out, noClaim) {
		t.Errorf("output does not state that the run makes no claim about the tree:\n%s", out)
	}
	if strings.Contains(out, "SENTINEL-EXECUTION-CONTINUED") {
		t.Errorf("the test body ran anyway — Isolate returned instead of calling Fatal:\n%s", out)
	}
}

// TestBrokenIsolationExitsBeforeAnyTestRuns is the package half, and the one
// mg-3412 paid for: a setup failure in TestMain must end the process with a
// distinct code and NO assertion failures, so nobody reads the tally as a
// verdict on the tree.
func TestBrokenIsolationExitsBeforeAnyTestRuns(t *testing.T) {
	home := fakeLiveHome(t)
	decoy := filepath.Join(t.TempDir(), "looks-private")
	if err := os.Symlink(filepath.Join(home, ".pogo"), decoy); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	out, err := runHelper(t, "-test.run=TestPackageSandboxIsEstablished",
		"HOME="+home, "POGO_HOME=", RootEnv+"="+decoy)
	if err == nil {
		t.Fatalf("the subprocess succeeded against a sandbox root symlinked onto %s\n%s",
			filepath.Join(home, ".pogo"), out)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("subprocess did not exit with a status: %v\n%s", err, out)
	}
	if got := exit.ExitCode(); got != SetupFailureExitCode {
		t.Errorf("exit code %d, want %d — a setup failure must not share the assertion "+
			"tally's exit status\n%s", got, SetupFailureExitCode, out)
	}
	if !strings.Contains(out, FailPrefix) {
		t.Errorf("no setup-failure banner:\n%s", out)
	}
	if strings.Contains(out, "--- FAIL") || strings.Contains(out, "--- PASS") {
		t.Errorf("tests ran despite the sandbox never being established:\n%s", out)
	}
	if strings.Contains(out, "\nok ") || strings.Contains(out, "\nPASS\n") {
		t.Errorf("the run reported a pass despite the sandbox never being established:\n%s", out)
	}
}

// runHelper re-executes this test binary. os.Args[0] is used rather than
// `go test` so the exit status observed is the test binary's own, undecorated.
//
// The environment is REBUILT rather than appended to: a duplicate NAME=value in
// an environ block is resolved by getenv taking the first, so appending an
// override to os.Environ() silently keeps the original.
func runHelper(t *testing.T, run string, env ...string) (string, error) {
	t.Helper()

	override := map[string]bool{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			override[e[:i]] = true
		}
	}
	child := make([]string, 0, len(env)+len(os.Environ()))
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 && override[e[:i]] {
			continue
		}
		child = append(child, e)
	}
	// The helper switch and the root knob are inherited by nothing else in this
	// suite; clear whatever the caller did not set explicitly.
	if !override[helperEnv] {
		child = append(child, helperEnv+"=")
	}
	if !override[RootEnv] {
		child = append(child, RootEnv+"=")
	}
	child = append(child, env...)

	cmd := exec.Command(os.Args[0], run, "-test.v")
	cmd.Env = child
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setEnvForTest applies a planned sandbox to the process environment and hands
// back a restore func. t.Setenv is not usable here: these cases need to mutate
// the same variables again mid-test, and the restore has to be exact.
func setEnvForTest(t *testing.T, sb *Sandbox) func() {
	t.Helper()
	saved := map[string]string{}
	for _, kv := range sb.vars() {
		saved[kv[0]] = os.Getenv(kv[0])
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		for name, v := range saved {
			os.Setenv(name, v)
		}
	}
}
