package carrierdrift

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any resolves onto the developer's live tree.
//
// This package reads the live work-item store through `mg`, and its scan covers
// every DISPATCHABLE item — the ones a mis-parse would produce findings about.
// MGSource.resolveRoot carries its own testing.Testing() default for that
// reason, and TestResolveRootNeverResolvesToTheLiveStoreUnderTest asserts it,
// but a defaulted --root is one mechanism and MG_ROOT is another. Both should
// hold; the sandbox makes the second one true.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("carrierdrift")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestResolveRootNeverResolvesToTheLiveStoreUnderTest is the positive control
// for the test-safe default. With no explicit Root, a test binary must resolve
// to a throwaway directory and never to "" — which is what production uses to
// mean "let mg find ~/.macguffin".
func TestResolveRootNeverResolvesToTheLiveStoreUnderTest(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	root := MGSource{}.resolveRoot()
	if root == "" {
		t.Fatal("MGSource{}.resolveRoot() returned \"\" under a test binary — " +
			"that is the production default, and it means the live ~/.macguffin")
	}
	if _, err := os.Stat(root); err != nil && !os.IsNotExist(err) {
		t.Fatalf("resolved root %q is not usable: %v", root, err)
	}

	// An explicit Root still wins, or every test that wants a fixture store
	// would silently read the scratch one instead.
	if got := (MGSource{Root: "/tmp/explicit"}).resolveRoot(); got != "/tmp/explicit" {
		t.Fatalf("explicit Root ignored: got %q", got)
	}
}
