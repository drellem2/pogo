package vcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The per-project enth token (mg-3a813): the record names a file, and the
// token is read from it at each use. These tests use made-up token values;
// none reads a real credential.

func TestReadEnthTokenFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// The shape `enth` writes for a minted bot token.
	bot := write("bot.json", `{"id":"t1","user":"bot","token":"enth_fake_bot","expires_at":"2099-01-01T00:00:00Z"}`)
	if got, err := ReadEnthTokenFile(bot); err != nil || got != "enth_fake_bot" {
		t.Errorf("JSON token file = %q, %v; want enth_fake_bot", got, err)
	}
	plain := write("plain", "  enth_fake_plain\n")
	if got, err := ReadEnthTokenFile(plain); err != nil || got != "enth_fake_plain" {
		t.Errorf("plain token file = %q, %v; want enth_fake_plain", got, err)
	}

	missing := filepath.Join(dir, "nope.json")
	for name, p := range map[string]string{
		"missing":        missing,
		"empty":          write("empty", "\n"),
		"no token field": write("notok.json", `{"user":"bot","secretish":"enth_fake_leak"}`),
		"bad json":       write("bad.json", `{"token": "enth_fake_leak"`),
		"two lines":      write("two", "enth_fake_leak\nenth_fake_leak2\n"),
	} {
		_, err := ReadEnthTokenFile(p)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), p) {
			t.Errorf("%s: error does not name the path %s: %v", name, p, err)
		}
		if strings.Contains(err.Error(), "enth_fake_leak") {
			t.Errorf("%s: error carries the file's contents: %v", name, err)
		}
	}
}

func TestEnthRepoToken(t *testing.T) {
	if tok, ok, err := (EnthRepo{Remote: "r", Repo: "o/r"}).Token(); tok != "" || ok || err != nil {
		t.Errorf("no token file = %q, %v, %v; want the shared default (\"\", false, nil)", tok, ok, err)
	}
	p := filepath.Join(t.TempDir(), "gone")
	if _, ok, err := (EnthRepo{TokenFile: p}).Token(); !ok || err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("missing token file = ok %v, %v; want an error naming %s", ok, err, p)
	}
}

func TestNormalizeSelectionTokenFile(t *testing.T) {
	_, e, err := NormalizeSelection(Enth, &EnthRepo{Remote: "r", Repo: "o/r", TokenFile: "/a/b/../tok.json"})
	if err != nil || e.TokenFile != "/a/tok.json" {
		t.Errorf("absolute token file = %+v, %v; want it cleaned", e, err)
	}
	if _, _, err := NormalizeSelection(Enth, &EnthRepo{Remote: "r", Repo: "o/r", TokenFile: "tok.json"}); err == nil {
		t.Error("a relative token file was accepted: pogod would resolve it against its own cwd")
	}
	if _, _, err := NormalizeSelection(Git, &EnthRepo{TokenFile: "/a/tok.json"}); err == nil {
		t.Error("a token file on a git repo was accepted")
	}
	a := EnthRepo{Remote: "r", Repo: "o/r"}
	b := a
	b.TokenFile = "/a/tok.json"
	if a.Equal(b) {
		t.Error("Equal ignores the token file, so changing only it would be a no-op")
	}
}
