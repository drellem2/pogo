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

func TestAugment_UserDirsAfterInheritedBeforeFallbacks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fallback dirs are unix-only")
	}
	got := Augment("/inherited/bin", "/exe/dir", "/home/u/.nvm/versions/node/v22.0.0/bin")
	parts := strings.Split(got, string(os.PathListSeparator))
	idx := func(dir string) int {
		for i, p := range parts {
			if p == dir {
				return i
			}
		}
		t.Fatalf("dir %q missing from %q", dir, got)
		return -1
	}
	if !(idx("/exe/dir") < idx("/inherited/bin") &&
		idx("/inherited/bin") < idx("/home/u/.nvm/versions/node/v22.0.0/bin") &&
		idx("/home/u/.nvm/versions/node/v22.0.0/bin") < idx("/usr/bin")) {
		t.Errorf("order wrong: want exeDir < inherited < userDirs < fallbacks, got %q", got)
	}
}

// nvm test fixture: a fake $HOME with installed node versions. NVM_DIR is
// pointed inside the fake home so a real nvm install on the host cannot leak
// in.
func fakeNvm(t *testing.T, versions []string) string {
	t.Helper()
	home := t.TempDir()
	nvmDir := filepath.Join(home, ".nvm")
	t.Setenv("NVM_DIR", nvmDir)
	for _, v := range versions {
		if err := os.MkdirAll(filepath.Join(nvmDir, "versions", "node", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestNvmBinDirs_AllVersionsHighestFirst(t *testing.T) {
	// v9 vs v10: lexical order would wrongly put v9 first. All installed
	// versions must be present because a global npm install (like pi) lands
	// only in the version active at install time — not necessarily the
	// default alias (gh #25).
	home := fakeNvm(t, []string{"v9.11.2", "v10.0.0", "v22.23.1"})
	got := nvmBinDirs(home)
	nodeDir := filepath.Join(home, ".nvm", "versions", "node")
	want := []string{
		filepath.Join(nodeDir, "v22.23.1", "bin"),
		filepath.Join(nodeDir, "v10.0.0", "bin"),
		filepath.Join(nodeDir, "v9.11.2", "bin"),
	}
	if len(got) != len(want) {
		t.Fatalf("nvmBinDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nvmBinDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNvmBinDirs_AbsentNvm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm"))
	if got := nvmBinDirs(home); len(got) != 0 {
		t.Errorf("nvmBinDirs with no nvm install = %v, want empty", got)
	}
}

func TestNpmPrefixBinDir(t *testing.T) {
	home := t.TempDir()
	npmrc := "# global prefix\nprefix=~/.npm-global\nregistry=https://registry.npmjs.org/\n"
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	got := npmPrefixBinDir(home)
	want := filepath.Join(home, ".npm-global", "bin")
	if got != want {
		t.Errorf("npmPrefixBinDir = %q, want %q", got, want)
	}
}

func TestNpmPrefixBinDir_NoNpmrc(t *testing.T) {
	if got := npmPrefixBinDir(t.TempDir()); got != "" {
		t.Errorf("npmPrefixBinDir with no .npmrc = %q, want empty", got)
	}
}

func TestUserToolchainDirs_OnlyExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("user toolchain dirs are unix-only")
	}
	home := fakeNvm(t, []string{"v22.1.0"})
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	got := userToolchainDirs(home)
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v22.1.0", "bin")
	for _, want := range []string{localBin, nvmBin} {
		found := false
		for _, d := range got {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Errorf("userToolchainDirs missing existing dir %q: %v", want, got)
		}
	}
	// ~/.volta and ~/.npm-global don't exist in the fake home — they must not
	// appear.
	for _, d := range got {
		if strings.Contains(d, ".volta") || strings.Contains(d, ".npm-global") {
			t.Errorf("userToolchainDirs includes nonexistent dir %q", d)
		}
	}
}

// TestPATH_ResolvesNvmInstalledBinary is the regression guard for gh #25: a
// binary that lives only in nvm's bin dir (like pi) must resolve by bare name
// after PATH repair, even from an empty inherited PATH.
func TestPATH_ResolvesNvmInstalledBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix shell script as the fake binary")
	}
	home := fakeNvm(t, []string{"v22.1.0"})
	t.Setenv("HOME", home) // os.UserHomeDir reads $HOME on unix
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v22.1.0", "bin")
	const name = "pogo-fake-pi"
	if err := os.WriteFile(filepath.Join(nvmBin, name), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", "")
	if _, err := exec.LookPath(name); err == nil {
		t.Fatal("precondition failed: bare name resolved with empty PATH")
	}
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(name).CombinedOutput()
	if err != nil {
		t.Fatalf("nvm-installed binary failed to resolve after PATH repair: %v (output %q)", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestEnsureExtra_PrependsAndExpands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin")
	if err := EnsureExtra([]string{"~/mytools/bin", "/opt/custom/bin"}); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	if len(parts) < 3 {
		t.Fatalf("PATH too short after EnsureExtra: %q", parts)
	}
	if parts[0] != filepath.Join(home, "mytools", "bin") {
		t.Errorf("first entry = %q, want expanded ~/mytools/bin", parts[0])
	}
	if parts[1] != "/opt/custom/bin" {
		t.Errorf("second entry = %q, want /opt/custom/bin", parts[1])
	}
	if parts[2] != "/usr/bin" {
		t.Errorf("third entry = %q, want prior PATH /usr/bin", parts[2])
	}
}

func TestEnsureExtra_EmptyIsNoOp(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	if err := EnsureExtra(nil); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != "/usr/bin" {
		t.Errorf("PATH changed by empty EnsureExtra: %q", got)
	}
	if err := EnsureExtra([]string{"", "  "}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != "/usr/bin" {
		t.Errorf("PATH changed by blank-only EnsureExtra: %q", got)
	}
}

func TestEnsureExtra_Idempotent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	for i := 0; i < 2; i++ {
		if err := EnsureExtra([]string{"/opt/custom/bin"}); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "/opt/custom/bin" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected /opt/custom/bin exactly once after re-run, got %d (%q)", count, os.Getenv("PATH"))
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
