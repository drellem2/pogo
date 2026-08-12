// Package testsandbox is the Go counterpart of `pogo_sandbox_isolate` in
// scripts/pogo-sandbox (mg-78a5): the private, CHECKED home a test runs in.
//
//	func TestMain(m *testing.M) {
//	    sb, down := testsandbox.Main("agent")   // pinned AND proven, or exit 99
//	    code := m.Run()
//	    down()
//	    os.Exit(code)
//	}
//
//	func TestSomething(t *testing.T) {
//	    testsandbox.Isolate(t)                  // a tree of this test's own
//	}
//
// # Why this package exists
//
// FOUR tickets were filed for ONE defect — a test reading or writing the
// developer's live ~/.pogo instead of a fixture. mg-78a5 packaged the envelope
// for the one written in shell (mg-3412, the suite that gates every merge). The
// other three are Go:
//
//	mg-6092   a test reading live ~/.pogo park state
//	mg-e8e7   seven respawn tests reading the host's live park state
//	mg-5336   the project and driver suites writing to — and launching from —
//	          the real ~/.pogo
//
// Each was fixed by hand, with t.Setenv("POGO_HOME", t.TempDir()) and friends.
// That is correct Go and it is still isolation-by-remembering: nothing checked
// that the override took, that HOME/XDG_CONFIG_HOME/MG_ROOT were pinned
// alongside it, or that the value did not resolve back onto the developer's
// tree. internal/agent/isolation_test.go and internal/scheduler/isolation_test.go
// assert that the CODE honours POGO_HOME; until this package, nothing asserted
// that a given TEST established one.
//
// # What is guaranteed, and checked rather than assumed
//
//  1. ALL FOUR VARIABLES ARE PINNED. HOME, XDG_CONFIG_HOME, POGO_HOME and
//     MG_ROOT, each under a throwaway root. All four are load-bearing. HOME
//     alone leaks POGO_HOME on this box, which exports POGO_HOME=$HOME from a
//     stale profile — that is mg-5336's route exactly. It leaks config.toml
//     through XDG, because config is layered and the real user config is read
//     even under a private HOME (mg-cf9e). And it leaves MG_ROOT free to reach
//     the live ~/.macguffin store, which internal/agent's claimrelease.go reads
//     directly.
//
//  2. THE OVERRIDE IS READ BACK. Every variable is verified from os.Getenv
//     AFTER it is applied, not from the value we meant to write. "I called
//     Setenv" and "the process is isolated" are different claims.
//
//  3. NOTHING RESOLVES ONTO LIVE STATE. Each value is resolved THROUGH
//     SYMLINKS and refused if it lands on, above, or below the real home or
//     the real ~/.pogo. The symlinked root is the only shape in which this
//     goes wrong quietly: every string reads as a private path right up until
//     something follows the link.
//
//  4. A SETUP FAILURE CANNOT BE READ AS A REGRESSION. Every check above ends
//     the run through a SETUP FAILURE banner naming this package — t.Fatal for
//     Isolate, and for Main a banner on stderr plus EXIT 99, distinct from the
//     assertion tally's 1, before a single test runs. mg-3412 is what the
//     absence costs: one setup failure printed fourteen assertion failures
//     downstream of it, including verbatim fail-open security findings, about a
//     tree that was provably fine.
//
// # What it deliberately does not do
//
// It does not retry, and it must never learn to. It does not soften a caller's
// assertion — it fails EARLIER and LOUDER, never later and quieter. And it does
// not claim what it cannot check: it pins environment variables, so a test that
// reaches live state by a route that is not an environment variable (an
// absolute path in a fixture, say) is outside what this can promise.
//
// See testsandbox_test.go, which breaks each guarantee on purpose and reads
// back what comes out. A harness observed only making tests pass has not been
// observed working.
package testsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/drellem2/pogo/internal/testtmp"
)

// FailPrefix opens every setup failure this package raises. It names the
// isolation on purpose: the failure a reader must not confuse with a
// regression has to say what it is on its first line, because mg-3412 proved
// the thirteen lines after it will be read as the diagnosis.
const FailPrefix = "TEST ISOLATION SETUP FAILURE (internal/testsandbox)"

// noClaim is the second half of guarantee 4: a setup failure has to say, in
// words, that it is not a verdict on the tree.
const noClaim = "The isolation could not be established, so this run makes NO claim about the " +
	"code under test. It is a SETUP failure, not a regression."

// RootEnv lets a caller supply the sandbox root instead of taking a temp dir,
// mirroring POGO_SANDBOX_ROOT in scripts/pogo-sandbox. It is safe by
// construction: a supplied root is vetted by exactly the same checks as a
// minted one, so pointing it at live state produces a refusal rather than a
// leak. That is what makes the broken-isolation direction demonstrable — see
// TestBrokenIsolationExitsBeforeAnyTestRuns.
const RootEnv = "POGO_TESTSANDBOX_ROOT"

// SetupFailureExitCode is the exit code Main ends the TEST BINARY with. It is
// neither 0 nor the assertion tally's 1, so a wrapper that runs the binary
// directly — `go test -c` output, or `scripts/pogo-sandbox run --` — can tell a
// sandbox that could not be built from a tree that is actually broken.
//
// `go test` itself collapses it to its own exit 1 and prints a bare package
// FAIL, so at that level the distinguishing signal is the banner plus the
// ABSENCE of any `--- FAIL` or `=== RUN` line: nothing ran, so nothing failed.
const SetupFailureExitCode = 99

// ---------------------------------------------------------------------------
// the live tree, captured before anything can override it
// ---------------------------------------------------------------------------

// liveTree is the developer's real state, as it was at process start.
type liveTree struct {
	home string   // the real $HOME
	pogo []string // every path the real ~/.pogo could be reached by
}

// realTree is captured in init, which is the only moment it can be captured:
// Go runs package inits before TestMain, so this reads the developer's
// environment before Main has repointed it. A check that reads the value it
// just wrote checks nothing.
var realTree liveTree

func init() { realTree = captureLive(os.Getenv("HOME"), os.Getenv("POGO_HOME")) }

// captureLive derives every path the machine's live pogo state can be reached
// by from an ambient HOME and POGO_HOME.
//
// Both routes are recorded because both are live. $HOME/.pogo is the fallback
// config.PogoHome() uses when POGO_HOME is unset, and it is the route mg-6092,
// mg-e8e7 and mg-5336 all leaked through. The POGO_HOME reading applies
// config.PogoHome's mg-3dc3 normalisation — POGO_HOME == HOME means $HOME/.pogo,
// because this box exports the former from a stale profile — replicated here
// rather than imported so that internal/config's own tests can use this package
// without an import cycle.
func captureLive(home, pogoHome string) liveTree {
	l := liveTree{home: home}
	if home != "" {
		l.pogo = append(l.pogo, filepath.Join(home, ".pogo"))
	}
	if pogoHome != "" {
		if home != "" && filepath.Clean(pogoHome) == filepath.Clean(home) {
			l.pogo = append(l.pogo, filepath.Join(pogoHome, ".pogo"))
		} else {
			l.pogo = append(l.pogo, pogoHome)
		}
	}
	return l
}

// ---------------------------------------------------------------------------
// path helpers — everything resolves symlinks
// ---------------------------------------------------------------------------

// resolve returns path with every symlink followed. A path that does not exist
// yet is resolved through its deepest existing ancestor, because the root is
// vetted BEFORE it is created: creating a directory inside the developer's
// ~/.pogo in order to find out we must not have created it there is not a
// check, it is the defect.
func resolve(path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(r)
	}
	clean := filepath.Clean(path)
	dir, base := filepath.Split(clean)
	dir = filepath.Clean(dir)
	if base == "" || dir == clean {
		return clean
	}
	return filepath.Join(resolve(dir), base)
}

// under reports whether child is parent or lives inside it.
func under(child, parent string) bool {
	if child == "" || parent == "" {
		return false
	}
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// reject returns an error if path is, contains, or sits inside the developer's
// home or live pogo state. Symlinks are resolved first: the interesting failure
// is the one where $root/home is a link to the real home and every string
// comparison says the sandbox is private.
func (l liveTree) reject(path, what string) error {
	r := resolve(path)
	if r == "" {
		return fmt.Errorf("%s is empty — there is no path to check, and an unset "+
			"variable is the developer's own state by default", what)
	}
	if home := resolve(l.home); home != "" && (r == home || under(home, r)) {
		return fmt.Errorf("%s (%s) resolves to %s, which IS the developer's home (%s) "+
			"or contains it — this is not a sandbox", what, path, r, home)
	}
	for _, p := range l.pogo {
		rp := resolve(p)
		if rp == "" {
			continue
		}
		if r == rp || under(rp, r) || under(r, rp) {
			return fmt.Errorf("%s (%s) resolves to %s, which is the developer's live pogo "+
				"state (%s), contains it, or lives inside it — the tests would be reading "+
				"and WRITING the machine's real ~/.pogo, which is mg-6092/mg-e8e7/mg-5336 "+
				"verbatim", what, path, r, rp)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// the sandbox
// ---------------------------------------------------------------------------

// Sandbox is one established envelope. The fields are the values that were
// pinned AND verified; a Sandbox handed back is a Sandbox that passed.
type Sandbox struct {
	Root          string // the private root everything below lives under
	Home          string // $HOME
	XDGConfigHome string // $XDG_CONFIG_HOME
	PogoHome      string // $POGO_HOME
	MGRoot        string // $MG_ROOT
}

// Contains reports whether path resolves inside the sandbox — the assertion a
// package's own positive control needs ("the state root this code actually
// writes through landed in here"). It resolves both sides through symlinks,
// which a strings.HasPrefix on the raw paths does not: on darwin the temp tree
// is reached as /var/... and resolves to /private/var/..., so the naive check
// reports every sandboxed path as outside its own sandbox.
func (s *Sandbox) Contains(path string) bool {
	if s == nil {
		return false
	}
	return under(resolve(path), resolve(s.Root))
}

// vars returns the pinned variables in a deterministic order.
func (s *Sandbox) vars() [][2]string {
	return [][2]string{
		{"HOME", s.Home},
		{"XDG_CONFIG_HOME", s.XDGConfigHome},
		{"POGO_HOME", s.PogoHome},
		{"MG_ROOT", s.MGRoot},
	}
}

// plan lays a sandbox out under root without touching anything.
func plan(root string) *Sandbox {
	home := filepath.Join(root, "home")
	return &Sandbox{
		Root:          root,
		Home:          home,
		XDGConfigHome: filepath.Join(root, "xdg"),
		PogoHome:      filepath.Join(home, ".pogo"),
		MGRoot:        filepath.Join(home, ".macguffin"),
	}
}

// prepare vets root, creates the tree under it, and returns the layout. It
// changes NO environment variable — applying is the caller's job, because
// Isolate must apply through t.Setenv (restored at cleanup) and Main through
// os.Setenv (process-wide).
func prepare(root string, l liveTree) (*Sandbox, error) {
	if root == "" {
		return nil, fmt.Errorf("no sandbox root was supplied — there is no private tree to " +
			"point HOME at")
	}

	// FIRST, AND BEFORE THE DIRECTORY EXISTS. Main removes this root at
	// teardown, so a root resolving onto the developer's home would not merely
	// READ live state, it would delete it. Vetting after mkdir would also mean
	// creating directories inside ~/.pogo on the way to refusing to.
	if err := l.reject(root, "the sandbox root"); err != nil {
		return nil, err
	}

	// EACH PLANNED PATH, ALSO BEFORE ANYTHING IS CREATED. A root can be private
	// and one of the four directories under it still be a symlink onto live
	// state; MkdirAll follows it, so checking afterwards would mean creating
	// $HOME/.macguffin inside the developer's tree on the way to reporting that
	// we must not have.
	sb := plan(root)
	for _, kv := range sb.vars() {
		if err := l.reject(kv[1], "the sandbox's $"+kv[0]); err != nil {
			return nil, err
		}
	}

	for _, dir := range []string{sb.Root, sb.Home, sb.XDGConfigHome, sb.PogoHome, sb.MGRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("could not create the sandbox directory %s: %w", dir, err)
		}
	}

	// A just-created POGO_HOME holds nothing. If this one holds something, the
	// MkdirAll above was a no-op onto a directory that already existed — so this
	// is not the tree we think it is, and the cheapest moment to learn that is
	// now, not from a test asserting on park state it did not stage.
	entries, err := os.ReadDir(sb.PogoHome)
	if err != nil {
		return nil, fmt.Errorf("could not read the sandbox POGO_HOME (%s): %w", sb.PogoHome, err)
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("the sandbox POGO_HOME (%s) is not empty — a freshly created "+
			"one is, so this directory predates this run and its contents are somebody "+
			"else's state", sb.PogoHome)
	}
	return sb, nil
}

// verify reads every pinned variable back OUT of the process environment and
// proves it. This is the half hand-rolled isolation never had: t.Setenv having
// been called and the process being isolated are different claims, and only
// one of them is what the test then depends on.
//
// It asserts nothing about the CONTENTS of the sandbox, only about where the
// four variables point. That is what makes it re-runnable from inside a test
// (see Verify) after the package has spent its run writing state into the tree.
func verify(sb *Sandbox, l liveTree) error {
	root := resolve(sb.Root)
	for _, kv := range sb.vars() {
		name, want := kv[0], kv[1]

		got := os.Getenv(name)
		if got == "" {
			return fmt.Errorf("$%s is unset after the sandbox pinned it to %s — the override "+
				"did not take, and every path derived from it is the developer's own",
				name, want)
		}
		if filepath.Clean(got) != filepath.Clean(want) {
			return fmt.Errorf("$%s is %s, but the sandbox pinned it to %s — something "+
				"overrode the isolation after it was established", name, got, want)
		}
		if err := l.reject(got, "the sandbox's $"+name); err != nil {
			return err
		}
		if r := resolve(got); !under(r, root) {
			return fmt.Errorf("the sandbox's $%s (%s) resolves to %s, which is outside the "+
				"sandbox root %s — it was not created by this run and nothing here owns "+
				"its contents", name, got, r, root)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// the two entry points
// ---------------------------------------------------------------------------

// rootSeq keeps successive roots distinct when RootEnv supplies a shared
// parent, so two Isolate calls in one run cannot land in the same tree.
var rootSeq atomic.Int64

func rootFor(fallback string) string {
	if r := os.Getenv(RootEnv); r != "" {
		return filepath.Join(r, fmt.Sprintf("sbx%d", rootSeq.Add(1)))
	}
	return fallback
}

// Isolate pins HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT under a tree of
// this test's own, then proves none of them can reach the developer's state.
// It calls tb.Fatal — naming the isolation — if any check fails.
//
// The variables are applied with tb.Setenv, so they are restored when the test
// ends and the helper cannot be called from a parallel test (the testing
// package enforces that, and it is the right answer: process environment is
// not a per-test resource).
//
// Use this in a test that stages state of its own. A package-wide backstop
// belongs in TestMain via Main — the two compose, and the per-test tree is
// nested inside nothing that outlives the test.
func Isolate(tb testing.TB) *Sandbox {
	tb.Helper()

	sb, err := prepare(rootFor(tb.TempDir()), realTree)
	if err != nil {
		tb.Fatalf("\n%s\n%v\n%s", FailPrefix, err, noClaim)
		return nil
	}
	for _, kv := range sb.vars() {
		tb.Setenv(kv[0], kv[1])
	}
	if err := verify(sb, realTree); err != nil {
		tb.Fatalf("\n%s\n%v\n%s", FailPrefix, err, noClaim)
		return nil
	}
	return sb
}

// Main establishes the same envelope for a whole package, process-wide. Call it
// as the first statement of TestMain and defer nothing: on failure it does not
// return, it prints the banner and exits 99 before a single test runs.
//
//	func TestMain(m *testing.M) {
//	    sb, down := testsandbox.Main("agent")
//	    code := m.Run()
//	    down()
//	    os.Exit(code)
//	}
//
// The returned func removes the sandbox root — and re-vets it first, at the
// instant of removal, because between establishment and teardown is exactly
// where a test could have replaced a directory with a symlink.
func Main(label string) (*Sandbox, func()) {
	root := rootFor("")
	if root == "" {
		var err error
		// testtmp rather than os.MkdirTemp, because down() below is not a
		// guarantee: a package whose tests re-exec the test binary as a helper
		// and then KILL it leaves that child's teardown unrun, and the agent
		// package does exactly that. Measured before this change, one
		// `go test ./internal/agent/...` left four pogo-agent-sandbox* roots
		// behind — teardown working perfectly for the one process that got to
		// run it. testtmp's sweep is the backstop for the other three (mg-de3c).
		if root, err = testtmp.Dir("sandbox-" + label); err != nil {
			mainFail(fmt.Errorf("could not create a private root for the %s package's "+
				"tests: %w", label, err))
		}
	}

	sb, err := prepare(root, realTree)
	if err != nil {
		mainFail(err)
	}
	for _, kv := range sb.vars() {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			mainFail(fmt.Errorf("could not set $%s: %w", kv[0], err))
		}
	}
	if err := verify(sb, realTree); err != nil {
		mainFail(err)
	}

	return sb, func() {
		// The guard in the direction that cannot be undone: this is an rm -rf.
		if err := realTree.reject(sb.Root, "the sandbox root, at teardown"); err != nil {
			fmt.Fprintf(os.Stderr, "\n%s\n%v\nRefusing to remove it.\n", FailPrefix, err)
			return
		}
		os.RemoveAll(sb.Root)
	}
}

// mainFail prints the banner and ends the process. It never returns.
func mainFail(err error) {
	bar := strings.Repeat("=", 72)
	fmt.Fprintf(os.Stderr, "\n%s\n%s\n%s\n\n%v\n\n%s\n%s\n",
		bar, FailPrefix, bar, err, noClaim, bar)
	os.Exit(SetupFailureExitCode)
}

// Verify re-proves an established sandbox from inside a test, and is how a
// package states its isolation as an ASSERTION rather than a comment. A
// package whose TestMain calls Main should call this from one test:
//
//	func TestPackageIsolation(t *testing.T) { testsandbox.Verify(t, sandbox) }
//
// Without it the isolation is an unverified claim — a later edit could drop it
// and leave every other test in the package green while the suite went back to
// reading the developer's ~/.pogo. That is not hypothetical; it is how all
// three of mg-6092, mg-e8e7 and mg-5336 shipped.
func Verify(tb testing.TB, sb *Sandbox) {
	tb.Helper()
	if sb == nil {
		tb.Fatalf("\n%s\nthere is no sandbox to verify — TestMain did not establish one\n%s",
			FailPrefix, noClaim)
		return
	}
	if err := verify(sb, realTree); err != nil {
		tb.Fatalf("\n%s\n%v\n%s", FailPrefix, err, noClaim)
	}
}
