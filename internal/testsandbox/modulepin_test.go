package testsandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The module pin (mg-117e) is the fifth guarantee, and it is the only one that
// is allowed to come out "did nothing". So it needs both directions asserted
// and, unlike the other four, it needs the SKIPPED direction asserted too — a
// pin that silently fails open on every box would look exactly like a pin that
// works, from inside a suite that merely benefits from it.
//
// The three shapes here:
//
//	planModulePin      pure, so every refusal is testable with no `go`, no
//	                   cache, and no network.
//	the applied pin    read back out of the process AND out of `go` itself,
//	                   because "I called Setenv" and "the toolchain agrees" are
//	                   different claims — the same rule verify() follows for the
//	                   four path variables.
//	the download path  proven CLOSED by observing what `go list` does, against a
//	                   positive control that proves the same observation can
//	                   still come out open. Offline in both directions: an
//	                   unpinned cache with GOPROXY=off still logs
//	                   `go: downloading`, so the attempt is demonstrable without
//	                   making it.

// TestModulePinIsAppliedAndTheToolchainAgrees is the positive direction. It
// asks `go` rather than os.Getenv for the second half deliberately: the pin is
// only worth anything if the SUBPROCESS a test shells out to sees it, and this
// package sets environment variables in its own process.
func TestModulePinIsAppliedAndTheToolchainAgrees(t *testing.T) {
	if !sandbox.ModulePinned() {
		t.Skipf("this sandbox pinned no module cache (realModCache=%q) — the pin fails "+
			"open by design, so there is nothing to assert here", realModCache)
	}

	if got := os.Getenv("GOMODCACHE"); got != sandbox.GoModCache {
		t.Errorf("$GOMODCACHE = %q, want the pinned %q", got, sandbox.GoModCache)
	}
	if got := os.Getenv("GOPROXY"); got != "off" {
		t.Errorf("$GOPROXY = %q, want \"off\" — the cache pin without a closed download "+
			"path is the hazard, not the fix: a fetch would then WRITE into the "+
			"developer's cache", got)
	}
	if sandbox.Contains(sandbox.GoModCache) {
		t.Errorf("the pinned cache %s is INSIDE the sandbox root %s — that is this run's "+
			"own empty cache with the download path closed, which is strictly worse "+
			"than not pinning at all", sandbox.GoModCache, sandbox.Root)
	}

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go binary — the toolchain half cannot be asked")
	}
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOPROXY").Output()
	if err != nil {
		t.Fatalf("go env under the sandbox: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("go env returned %d lines, want 2:\n%s", len(lines), out)
	}
	if lines[0] != sandbox.GoModCache {
		t.Errorf("`go env GOMODCACHE` says %q, the sandbox pinned %q — the pin did not "+
			"reach the toolchain, so every `go` a test shells out to still resolves "+
			"its cache off the throwaway $HOME", lines[0], sandbox.GoModCache)
	}
	if lines[1] != "off" {
		t.Errorf("`go env GOPROXY` says %q, want \"off\"", lines[1])
	}
}

// TestSandboxClosesTheModuleDownloadPath is the measurement this ticket exists
// for, stated as an assertion: a `go` invocation under the sandbox must not
// reach the download path.
//
// The positive control in the same test is what makes it worth reading. It runs
// the identical query under an unpinned throwaway HOME with GOPROXY=off, which
// STILL logs `go: downloading` — Go decides it needs the module before it
// discovers it cannot have it. So the control proves the observation can come
// out "open" without a single byte leaving the box, and the assertion above it
// is not passing because `go list` never says that word.
func TestSandboxClosesTheModuleDownloadPath(t *testing.T) {
	if !sandbox.ModulePinned() {
		t.Skipf("this sandbox pinned no module cache (realModCache=%q)", realModCache)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go binary")
	}

	// A package with an external import, so there is something to download. This
	// package has none; internal/agent is where the 37 requests were measured.
	const pkg = "github.com/drellem2/pogo/internal/agent"

	list := func(extraEnv ...string) (stdout, stderr string, err error) {
		cmd := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}", pkg)
		cmd.Env = append(os.Environ(), extraEnv...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err = cmd.Run()
		return o.String(), e.String(), err
	}

	pinnedOut, pinnedErr, err := list()
	if err != nil {
		t.Fatalf("go list under the sandbox failed: %v\nstderr:\n%s", err, pinnedErr)
	}
	if strings.Contains(pinnedErr, "go: downloading") {
		t.Errorf("a `go` invocation under this sandbox reached the module download "+
			"path.\nThat is mg-117e verbatim: TestMain moves $HOME, Go resolves "+
			"GOMODCACHE off $HOME, and the cache is empty — 37 module requests per "+
			"gate run, all of them from internal/agent.\nstderr:\n%s", pinnedErr)
	}

	// The control. GOMODCACHE explicitly emptied (a fresh directory of this
	// test's own) with the download path closed, so the attempt is logged and
	// no request is made.
	cold := filepath.Join(t.TempDir(), "cold-modcache")
	if err := os.MkdirAll(cold, 0o755); err != nil {
		t.Fatalf("staging an empty module cache: %v", err)
	}
	coldOut, coldErr, err := list("GOMODCACHE="+cold, "GOPROXY=off")
	if err != nil {
		t.Fatalf("the control run failed: %v\nstderr:\n%s", err, coldErr)
	}
	if !strings.Contains(coldErr, "go: downloading") {
		t.Errorf("the CONTROL did not reach the download path either, so the assertion "+
			"above proves nothing: `go list` may simply never log that phrase in this "+
			"toolchain.\nstderr:\n%s", coldErr)
	}

	// And the reason a closed path is safe to close: the answer does not change.
	if pinnedOut != coldOut {
		t.Errorf("go list answered differently with the cache pinned and with it "+
			"empty.\npinned:\n%s\ncold:\n%s", pinnedOut, coldOut)
	}
	if strings.TrimSpace(pinnedOut) == "" {
		t.Error("go list returned no imports at all under the sandbox — the comparison " +
			"above is vacuous")
	}
}

// TestPlanModulePinRefusals covers every way the pin declines, because failing
// open is the one outcome that cannot be noticed downstream: an unpinned
// sandbox behaves exactly as this package did before the pin existed, which is
// to say it downloads, quietly.
func TestPlanModulePinRefusals(t *testing.T) {
	root := t.TempDir()
	real := t.TempDir() // stands in for the developer's cache: a real directory, outside root

	if cache, proxy := planModulePin(real, root); cache != real || proxy != "off" {
		t.Fatalf("planModulePin(%s, %s) = (%q, %q), want (%q, \"off\") — the positive "+
			"case must work or the refusals below are refusals of nothing",
			real, root, cache, proxy, real)
	}

	inside := filepath.Join(root, "home", "go", "pkg", "mod")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		cache string
		why   string
	}{
		{"no cache at all", "", "no `go` on PATH, or it could not answer"},
		{"relative path", "go/pkg/mod", "cannot be handed to a subprocess with a different cwd"},
		{"does not exist", filepath.Join(root, "nope"), "nothing there to read"},
		{"not a directory", file, "nothing there to read"},
		{"inside the sandbox root", inside, "this run's own empty cache with the download path closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, proxy := planModulePin(tc.cache, root)
			if cache != "" || proxy != "" {
				t.Errorf("planModulePin(%q, %s) = (%q, %q), want the pin SKIPPED — %s",
					tc.cache, root, cache, proxy, tc.why)
			}
		})
	}
}

// TestVerifyCatchesAnOverriddenModulePin breaks the pin on purpose and reads
// back what comes out. Without this, "verify checks the pin" is a claim about a
// branch nothing has ever taken — which is the shape of defect this whole
// package was written to stop.
func TestVerifyCatchesAnOverriddenModulePin(t *testing.T) {
	sb := &Sandbox{GoModCache: "/somewhere/real/pkg/mod", GoProxy: "off"}

	t.Setenv("GOMODCACHE", "/somewhere/real/pkg/mod")
	t.Setenv("GOPROXY", "https://proxy.golang.org,direct")

	err := verifyModulePin(sb)
	if err == nil {
		t.Fatal("verifyModulePin accepted a sandbox whose GOPROXY had been reopened — " +
			"with the developer's cache pinned and a download path open, a fetch " +
			"inside the run WRITES into that cache")
	}
	if !strings.Contains(err.Error(), "GOPROXY") {
		t.Errorf("the complaint does not name the variable that is wrong: %v", err)
	}

	t.Setenv("GOPROXY", "off")
	if err := verifyModulePin(sb); err != nil {
		t.Fatalf("verifyModulePin rejected a correctly pinned sandbox: %v", err)
	}

	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "elsewhere"))
	if err := verifyModulePin(sb); err == nil {
		t.Fatal("verifyModulePin accepted a sandbox whose GOMODCACHE had been repointed")
	}
}

// TestUnpinnedSandboxVerifiesVacuously states the fail-open contract as an
// assertion rather than a comment: verify() must not refuse a sandbox that
// could not pin a cache, because on a box with no cache to share there is
// nothing better available than the behaviour we already had.
func TestUnpinnedSandboxVerifiesVacuously(t *testing.T) {
	if err := verifyModulePin(&Sandbox{}); err != nil {
		t.Fatalf("an unpinned sandbox was refused: %v\nThe pin fails OPEN on purpose — "+
			"see planModulePin", err)
	}
}
