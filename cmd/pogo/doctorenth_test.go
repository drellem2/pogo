package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
)

// O4 (enth-vcs slice 9, mg-2e9fc): `pogo doctor --check` asks for enth only
// when some project is an enth repo.

func TestEnthToolLine(t *testing.T) {
	gitOnly := []project.Project{{Path: "/r/a"}, {Path: "/r/b", VCS: vcs.Git}}
	withEnth := append(append([]project.Project(nil), gitOnly...),
		project.Project{Path: "/r/e", VCS: vcs.Enth, Enth: &vcs.EnthRepo{Remote: "https://e", Repo: "o/r"}})
	found := func(string) (string, error) { return "/bin/enth", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	help9 := func(string) ([]byte, error) { return []byte("usage:\n  enth status DIR\n"), nil }
	helpOld := func(string) ([]byte, error) { return []byte("usage:\n  enth checkout\n"), nil }

	// The git control: no enth project, no row — whatever enth's state. A row
	// here would change every git host's doctor output.
	asked := false
	if _, _, applies := enthToolLine(gitOnly, func(string) (string, error) { asked = true; return "", errors.New("x") }, help9); applies || asked {
		t.Errorf("git-only host: applies=%v, PATH asked=%v; want no row and no lookup", applies, asked)
	}
	if _, _, applies := enthToolLine(nil, missing, help9); applies {
		t.Error("no projects: a row was produced")
	}

	for _, tc := range []struct {
		name   string
		look   func(string) (string, error)
		help   func(string) ([]byte, error)
		status string
		want   string
	}{
		{"missing", missing, help9, "fail", "not found, and 1 enth repo(s): /r/e"},
		{"pre-#9", found, helpOld, "warn", "predates enth #9"},
		{"help fails", found, func(string) ([]byte, error) { return nil, errors.New("exec format error") }, "warn", "exec format error"},
		{"ok", found, help9, "pass", "/bin/enth (for 1 enth repo(s): /r/e"},
	} {
		st, detail, applies := enthToolLine(withEnth, tc.look, tc.help)
		if !applies || st != tc.status || !strings.Contains(detail, tc.want) {
			t.Errorf("%s: (%q, %q, %v), want %s containing %q", tc.name, st, detail, applies, tc.status, tc.want)
		}
	}
}
