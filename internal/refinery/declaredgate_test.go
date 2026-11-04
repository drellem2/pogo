package refinery

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRefineryToml(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".pogo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pogo", "refinery.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mg-c07a5: the self_land gate is the command the repo declares, read as the
// refinery reads it.
func TestDeclaredGate(t *testing.T) {
	enth := writeRefineryToml(t, t.TempDir(), "# comment\nquality_gate = \"ENTH_REQUIRE_WEB=1 ./build.sh\"\n")
	multi := writeRefineryToml(t, t.TempDir(), "[gates]\ncommands = [\"./build.sh\", \"a; b\"]\n")
	other := writeRefineryToml(t, t.TempDir(), "quality_gate = \"FOO=1 ./build.sh\"\n")
	timeoutOnly := writeRefineryToml(t, t.TempDir(), "[gates]\ntimeout = \"5m\"\n")
	none := t.TempDir()
	for _, c := range []struct {
		name, wt, repo, want string
		declared             bool
	}{
		{"repo declares", "", enth, "ENTH_REQUIRE_WEB=1 ./build.sh", true},
		{"worktree declares", enth, "", "ENTH_REQUIRE_WEB=1 ./build.sh", true},
		{"worktree wins over repo", other, enth, "FOO=1 ./build.sh", true},
		{"repo fills in an undeclaring worktree", none, enth, "ENTH_REQUIRE_WEB=1 ./build.sh", true},
		{"several gates run in order, each isolated", multi, "", "(./build.sh) && (a; b)", true},
		{"a config without gates falls back", timeoutOnly, none, FallbackGateCommand, false},
		{"no config falls back", none, none, FallbackGateCommand, false},
		{"no paths falls back", "", "", FallbackGateCommand, false},
	} {
		got, declared := DeclaredGate(c.wt, c.repo)
		if got != c.want || declared != c.declared {
			t.Errorf("%s: DeclaredGate = %q, %v; want %q, %v", c.name, got, declared, c.want, c.declared)
		}
		// It agrees with the refinery's own lookup whenever a gate is declared.
		if c.declared && len(c.wt) > 0 && len(c.repo) > 0 {
			if g := (&Refinery{}).loadConfig(c.wt, c.repo).Gates; len(g) != 1 || g[0] != got {
				t.Errorf("%s: loadConfig gates %q disagree with DeclaredGate %q", c.name, g, got)
			}
		}
	}
}
