package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
)

// `pogo project set-enth-token` (mg-3a813): the file is checked before it is
// recorded, recorded as an absolute path, and only on an enth project.

func TestCheckTokenFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bot.json")
	os.WriteFile(p, []byte(`{"token":"enth_fake_bot"}`), 0o600)
	t.Chdir(dir)
	got, err := checkTokenFile("bot.json")
	if err != nil || got != p {
		t.Errorf("relative token file = %q, %v; want %s", got, err, p)
	}
	t.Setenv("HOME", dir)
	if got, err := checkTokenFile("~/bot.json"); err != nil || got != p {
		t.Errorf("~ token file = %q, %v; want %s", got, err, p)
	}
	if _, err := checkTokenFile(filepath.Join(dir, "gone.json")); err == nil || !strings.Contains(err.Error(), "gone.json") {
		t.Errorf("missing token file = %v; want a refusal naming it", err)
	}
}

func TestWithTokenFile(t *testing.T) {
	enth := project.Project{Path: "/x/e/", VCS: vcs.Enth, Enth: &vcs.EnthRepo{Remote: "r", Repo: "o/r",
		Trunk: vcs.DefaultEnthTrunk, WorkspacePaths: []string{"src"}}}
	e, err := withTokenFile(enth, "/s/bot.json")
	if err != nil || e.TokenFile != "/s/bot.json" || e.Remote != "r" || e.Repo != "o/r" || len(e.WorkspacePaths) != 1 {
		t.Errorf("set = %+v, %v; want the record unchanged but for the token file", e, err)
	}
	if enth.Enth.TokenFile != "" {
		t.Error("withTokenFile mutated the record it was given")
	}
	if _, err := withTokenFile(project.Project{Path: "/x/g/"}, "/s/bot.json"); err == nil {
		t.Error("a token file on a git project was accepted")
	}
	if findP, ok := findProject([]project.Project{enth}, "/x/e"); !ok || findP.Path != "/x/e/" {
		t.Errorf("findProject did not match across the trailing separator")
	}
}
