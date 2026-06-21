package pathenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAugment_ExeDirFirst(t *testing.T) {
	got := Augment("/usr/bin:/bin", "/opt/pogo/current/bin")
	first := strings.Split(got, string(os.PathListSeparator))[0]
	if first != "/opt/pogo/current/bin" {
		t.Fatalf("exe dir should come first, got %q (full %q)", first, got)
	}
}

func TestAugment_EmptyPATHStillHasFallbacks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fallback dirs are unix-only")
	}
	got := Augment("", "")
	for _, want := range []string{"/usr/bin", "/bin", "/opt/pogo/current/bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("augmented empty PATH missing fallback %q: %q", want, got)
		}
	}
}

func TestAugment_Dedupe(t *testing.T) {
	// exeDir duplicates an inherited entry and an empty entry is present; both
	// should collapse so each directory appears exactly once.
	got := Augment("/usr/bin::/usr/bin", "/usr/bin")
	parts := strings.Split(got, string(os.PathListSeparator))
	count := 0
	for _, p := range parts {
		if p == "" {
			t.Errorf("augmented PATH contains an empty entry: %q", got)
		}
		if p == "/usr/bin" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected /usr/bin exactly once, got %d (%q)", count, got)
	}
}

// TestEnsure_ChildProcessResolvesBareName is the regression guard for the
// reported bug: pogod spawned a child by bare name (`mg`) and it failed because
// the inherited PATH was empty. We reproduce the empty-PATH condition, drop a
// fake executable into a directory that Augment will include, and confirm a
// child process spawned by bare name resolves and runs.
func TestEnsure_ChildProcessResolvesBareName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix shell script as the fake binary")
	}

	binDir := t.TempDir()
	const name = "pogo-fakebin"
	script := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Reproduce the daemon's broken state: an empty inherited PATH. Restore it
	// afterwards so other tests are unaffected.
	orig, had := os.LookupEnv("PATH")
	t.Cleanup(func() {
		if had {
			os.Setenv("PATH", orig)
		} else {
			os.Unsetenv("PATH")
		}
	})
	os.Setenv("PATH", "")

	// Sanity: with an empty PATH the bare name must NOT resolve, otherwise the
	// test proves nothing.
	if _, err := exec.LookPath(name); err == nil {
		t.Fatal("precondition failed: bare name resolved with empty PATH")
	}

	// Repair PATH the way pogod does, but seed binDir as the "exe dir" so we do
	// not depend on where the test binary happens to live.
	if err := os.Setenv("PATH", Augment(os.Getenv("PATH"), binDir)); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(name).CombinedOutput()
	if err != nil {
		t.Fatalf("child %q failed to resolve/run after PATH repair: %v (output %q)", name, err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("unexpected child output: %q", out)
	}
}

func TestEnsure_SetsProcessPATH(t *testing.T) {
	orig, had := os.LookupEnv("PATH")
	t.Cleanup(func() {
		if had {
			os.Setenv("PATH", orig)
		} else {
			os.Unsetenv("PATH")
		}
	})
	os.Setenv("PATH", "")

	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got == "" {
		t.Fatal("Ensure left PATH empty")
	}
}
