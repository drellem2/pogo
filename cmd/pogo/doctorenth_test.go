package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
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
	if _, _, _, applies := enthToolLine(gitOnly, func(string) (string, error) { asked = true; return "", errors.New("x") }, help9); applies || asked {
		t.Errorf("git-only host: applies=%v, PATH asked=%v; want no row and no lookup", applies, asked)
	}
	if _, _, _, applies := enthToolLine(nil, missing, help9); applies {
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
		st, detail, bin, applies := enthToolLine(withEnth, tc.look, tc.help)
		if wantBin := map[bool]string{true: "", false: "/bin/enth"}[tc.status == "fail"]; bin != wantBin {
			t.Errorf("%s: bin = %q, want %q (the login row runs this binary)", tc.name, bin, wantBin)
		}
		if !applies || st != tc.status || !strings.Contains(detail, tc.want) {
			t.Errorf("%s: (%q, %q, %v), want %s containing %q", tc.name, st, detail, applies, tc.status, tc.want)
		}
	}
}

// GH #284 item 4 (mg-d8dc9): an "enth login" row per (remote, token file),
// asking enth who the token names.
func TestEnthLoginLines(t *testing.T) {
	gitOnly := []project.Project{{Path: "/r/a"}}
	called := false
	if rows := enthLoginLines(gitOnly, "/bin/enth", func(string, string, string) ([]byte, error) {
		called = true
		return nil, nil
	}); len(rows) != 0 || called {
		t.Fatalf("git-only host: rows=%v whoami called=%v; want none", rows, called)
	}

	tokFile := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tokFile, []byte(`{"token":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	enthProj := func(path, remote, repo, tf string) project.Project {
		return project.Project{Path: path, VCS: vcs.Enth, Enth: &vcs.EnthRepo{Remote: remote, Repo: repo, TokenFile: tf}}
	}
	projs := append(gitOnly,
		enthProj("/r/e1", "https://a", "o/one", ""),
		enthProj("/r/e2", "https://a", "o/two", ""), // same remote, same token: one ask
		enthProj("/r/e3", "https://b", "o/three", tokFile),
		enthProj("/r/e4", "https://c", "o/four", filepath.Join(t.TempDir(), "missing")),
		enthProj("/r/e5", "https://d", "o/five", ""),
	)
	var asked []string
	whoami := func(bin, remote, token string) ([]byte, error) {
		asked = append(asked, remote+"|"+token)
		switch remote {
		case "https://a":
			return []byte(`{"type":"user","name":"dan","email":"x@y"}` + "\n"), nil
		case "https://b":
			return []byte(`{"type":"error","code":"unauthorized","message":"no valid bearer token"}`), errors.New("exit status 8")
		}
		return []byte("dial tcp: connection refused\n"), errors.New("exit status 1")
	}
	rows := enthLoginLines(projs, "/bin/enth", whoami)
	want := []struct{ status, sub string }{
		{"pass", "https://a: signed in as dan"},
		{"fail", "https://b: unauthorized: no valid bearer token"},
		{"fail", "https://c:"},
		{"warn", "https://d: could not ask"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].status != w.status || !strings.Contains(rows[i].detail, w.sub) {
			t.Errorf("row %d = %+v, want %s containing %q", i, rows[i], w.status, w.sub)
		}
	}
	if !strings.Contains(rows[0].detail, "o/one, o/two") {
		t.Errorf("shared remote row does not name both repos: %q", rows[0].detail)
	}
	// The token file's value reaches enth, and only enth: no row prints it.
	if got := strings.Join(asked, ","); got != "https://a|,https://b|s3cret,https://d|" {
		t.Errorf("whoami asks = %q", got)
	}
	for _, r := range rows {
		if strings.Contains(r.detail, "s3cret") {
			t.Errorf("row leaks the token: %q", r.detail)
		}
	}
}

// GH #284 (mg-d8dc9): an enth checkout auto-registered with no vcs key is
// read as git; the doctor names it and the set-vcs line that fixes it.
func TestEnthUnrecordedLines(t *testing.T) {
	projs := []project.Project{
		{Path: "/r/git"},
		{Path: "/r/enth-as-git"},
		{Path: "/r/enth-local"},
		{Path: "/r/enth-ok", VCS: vcs.Enth, Enth: &vcs.EnthRepo{Remote: "https://e", Repo: "o/ok"}},
	}
	isCheckout := func(p string) bool { return strings.HasPrefix(p, "/r/enth") }
	origin := func(p string) (string, string) {
		if p == "/r/enth-as-git" {
			return "https://enth.example", "o/r"
		}
		return "", ""
	}
	got := enthUnrecordedLines(projs, isCheckout, origin)
	if len(got) != 2 {
		t.Fatalf("rows = %q, want 2 (the git repo and the recorded enth repo raise none)", got)
	}
	if want := "pogo project set-vcs /r/enth-as-git --vcs=enth --enth-remote=https://enth.example --enth-repo=o/r"; !strings.Contains(got[0], want) {
		t.Errorf("row 0 = %q, want it to contain %q", got[0], want)
	}
	if want := "--enth-remote=URL --enth-repo=NAME"; !strings.Contains(got[1], want) {
		t.Errorf("row 1 = %q, want placeholders %q", got[1], want)
	}
	// Control: the real reader on a git-only tree finds nothing.
	if rows := enthUnrecordedLines([]project.Project{{Path: t.TempDir()}}, enthvcs.IsCheckout, enthCheckoutOrigin); len(rows) != 0 {
		t.Errorf("plain dir flagged: %q", rows)
	}
}
