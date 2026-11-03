package enthvcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestIdentityEnv(t *testing.T) {
	home := t.TempDir()
	none := map[string]string{"HOME": home}

	// No identity anywhere: all four fields, from git's identity.
	got := strings.Join(IdentityEnv(envOf(none), "Dan", "dan@example.com", "p1"), " ")
	want := "ENTH_AUTHOR_NAME=Dan ENTH_AUTHOR_EMAIL=dan@example.com ENTH_COMMITTER_NAME=Dan ENTH_COMMITTER_EMAIL=dan@example.com"
	if got != want {
		t.Errorf("no identity: %q, want %q", got, want)
	}
	// No git identity either: the worker's name, at an .invalid address.
	if got := strings.Join(IdentityEnv(envOf(none), "", "", "p1"), " "); !strings.Contains(got, "ENTH_AUTHOR_NAME=p1 ENTH_AUTHOR_EMAIL=p1@pogo.invalid") {
		t.Errorf("no git identity: %q", got)
	}
	// Each source of enth's chain wins, field by field.
	for _, k := range []string{"ENTH_AUTHOR_NAME", "GIT_AUTHOR_NAME"} {
		env := map[string]string{"HOME": home, k: "set"}
		if got := strings.Join(IdentityEnv(envOf(env), "Dan", "d@x", "p1"), " "); strings.Contains(got, "ENTH_AUTHOR_NAME=") || !strings.Contains(got, "ENTH_AUTHOR_EMAIL=d@x") {
			t.Errorf("%s set: %q (must fill only the other fields)", k, got)
		}
	}
	// An empty value is not set (enth takes the first set AND non-empty value).
	if got := IdentityEnv(envOf(map[string]string{"HOME": home, "ENTH_AUTHOR_NAME": ""}), "Dan", "d@x", "p1"); len(got) != 4 {
		t.Errorf("empty ENTH_AUTHOR_NAME counted as set: %q", got)
	}
	// enth's config file: user.name and user.email cover both roles.
	cfg := filepath.Join(home, ".config", "enth")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "config"), []byte("[core]\n\tname = not-this\n[user]\n\tname = Conf\n\temail = \"conf@x\"\n"), 0o644)
	if got := IdentityEnv(envOf(none), "Dan", "d@x", "p1"); got != nil {
		t.Errorf("enth config has an identity: %q, want nil", got)
	}
	// XDG_CONFIG_HOME replaces ~/.config.
	if got := IdentityEnv(envOf(map[string]string{"HOME": home, "XDG_CONFIG_HOME": t.TempDir()}), "Dan", "d@x", "p1"); len(got) != 4 {
		t.Errorf("XDG_CONFIG_HOME with no config: %q", got)
	}
}

// TestIdentityEnvSatisfiesEnth: with no identity anywhere, a real `enth
// change` is refused no_identity (the control: the defect is real), and with
// IdentityEnv's entries it succeeds and records git's identity.
func TestIdentityEnvSatisfiesEnth(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	dir := e.checkout(t, b, "id")
	home := t.TempDir()
	var base []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "ENTH_AUTHOR_") || strings.HasPrefix(k, "ENTH_COMMITTER_") || strings.HasPrefix(k, "GIT_AUTHOR_") ||
			strings.HasPrefix(k, "GIT_COMMITTER_") || k == "HOME" || k == "XDG_CONFIG_HOME" {
			continue
		}
		base = append(base, kv)
	}
	base = append(base, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	change := func(env []string) (string, error) {
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte(strings.Join(env, "")[:8]+"\n"), 0o644)
		c := exec.Command(b.Bin, "change", "-m", "id test", dir)
		c.Env = env
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if out, err := change(base); err == nil || !strings.Contains(out, "no_identity") {
		t.Fatalf("control: enth change with no identity = %v\n%s\nwant no_identity", err, out)
	}
	getenv := func(k string) string {
		for _, kv := range base {
			if strings.HasPrefix(kv, k+"=") {
				return kv[len(k)+1:]
			}
		}
		return ""
	}
	if out, err := change(append(base, IdentityEnv(getenv, "Dan Git", "dan@git.example", "p1")...)); err != nil {
		t.Fatalf("enth change with IdentityEnv: %v\n%s", err, out)
	}
	st, _ := ReadState(dir)
	cs, err := b.ListChanges(context.Background(), "", "", true)
	if err != nil || len(cs) != 1 || cs[0].ID != st.Base || cs[0].AuthorName != "Dan Git" || cs[0].AuthorEmail != "dan@git.example" {
		t.Errorf("the Change = %+v, %v; want authored by git's identity", cs, err)
	}
}
