package refinery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// VCS selection (enth-vcs §2, slice 4, mg-160e8): the refinery's backend comes
// from the project record, never from refinery.toml.

func writeToml(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".pogo", "refinery.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRefineryTomlVCSKeyIsWarnedNotRead(t *testing.T) {
	r := &Refinery{}

	// Positive control first: an ordinary config warns about nothing.
	plainWT, plainOrig := t.TempDir(), t.TempDir()
	writeToml(t, plainWT, "[gates]\ncommands = [\"./build.sh\"]\n")
	if cfg := r.loadConfig(plainWT, plainOrig); len(cfg.Warnings) != 0 {
		t.Fatalf("a config with no vcs key warned: %v", cfg.Warnings)
	}

	wt, orig := t.TempDir(), t.TempDir()
	wtPath := writeToml(t, wt, "vcs = \"enth\"\n[gates]\ncommands = [\"./build.sh\"]\n")
	origPath := writeToml(t, orig, "[enth]\nremote = \"https://enth.app\"\n")

	cfg := r.loadConfig(wt, orig)
	if len(cfg.Warnings) != 1 {
		t.Fatalf("got %d warnings, want exactly one per load: %v", len(cfg.Warnings), cfg.Warnings)
	}
	w := cfg.Warnings[0]
	for _, want := range []string{wtPath, origPath, "pogo project set-vcs"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not mention %q", w, want)
		}
	}
	// The rest of the file is still read.
	if len(cfg.Gates) != 1 || cfg.Gates[0] != "./build.sh" {
		t.Errorf("gates = %v; a vcs key must not disturb the rest of the config", cfg.Gates)
	}
	// And it selects nothing: with no record lookup the repo is git.
	if _, ok := backendFor(orig).(gitLandBackend); !ok {
		t.Errorf("a vcs key in refinery.toml changed the backend to %T", backendFor(orig))
	}

	// A [vcs] section is the same mistake.
	wt2 := t.TempDir()
	writeToml(t, wt2, "[vcs]\nkind = \"enth\"\n")
	if cfg := r.loadConfig(wt2, t.TempDir()); len(cfg.Warnings) != 1 {
		t.Errorf("[vcs] section: warnings = %v, want one", cfg.Warnings)
	}
}

func TestBackendForSelectsFromTheRecord(t *testing.T) {
	t.Cleanup(func() { SetRepoVCS(nil) })

	// No lookup installed: git, as before slice 4.
	if _, ok := backendFor("/x/enth").(gitLandBackend); !ok {
		t.Fatalf("no lookup: backend = %T, want git", backendFor("/x/enth"))
	}

	var asked []string
	SetRepoVCS(func(repo string) (vcs.Kind, *vcs.EnthRepo) {
		asked = append(asked, repo)
		if repo == "/x/enth" {
			return vcs.Enth, &vcs.EnthRepo{Remote: "https://enth.app", Repo: "o/r"}
		}
		return vcs.Git, nil
	})

	if _, ok := backendFor("/x/git").(gitLandBackend); !ok {
		t.Errorf("git record: backend = %T, want git", backendFor("/x/git"))
	}
	b, ok := backendFor("/x/enth").(enthvcs.Backend)
	if !ok {
		t.Fatalf("enth record: backend = %T, want the enth backend", backendFor("/x/enth"))
	}
	if b.Repo.Repo != "o/r" {
		t.Errorf("enth backend carries %+v, want the record's settings", b.Repo)
	}
	if len(asked) == 0 || asked[len(asked)-1] != "/x/enth" {
		t.Errorf("lookup asked about %v", asked)
	}

	// The refinery does not land enth work (mg-5b904): the enth Lander
	// refuses every step with the pointer to the worker's own land, and is
	// never handed to git.
	if _, err := backendFor("/x/enth").Prepare(context.Background(), vcs.MR{Changes: []string{"c1"}}); !errors.Is(err, enthvcs.ErrSelfLanded) {
		t.Fatalf("enth Prepare = %v, want ErrSelfLanded", err)
	}
	// Refs is still inert (slice 12).
	if _, err := detectDefaultBranch("/x/enth"); !errors.Is(err, vcs.ErrUnsupported) {
		t.Errorf("detectDefaultBranch on an enth repo = %v, want ErrUnsupported", err)
	}
}
