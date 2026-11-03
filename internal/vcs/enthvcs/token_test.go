package enthvcs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// fakeEnth is an `enth` that prints the token it was given, so a test can see
// which token a call ran as without any real credential.
func fakeEnth(t *testing.T) (bin, ran string) {
	t.Helper()
	dir := t.TempDir()
	ran = filepath.Join(dir, "ran")
	bin = filepath.Join(dir, "enth")
	script := "#!/bin/sh\ntouch " + ran + "\nprintf 'token=%s' \"$ENTH_TOKEN\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, ran
}

// A project with a token file runs its enth calls as that token, overriding
// pogod's own; one without keeps pogod's environment (mg-3a813).
func TestRunUsesTheProjectsTokenFile(t *testing.T) {
	bin, _ := fakeEnth(t)
	t.Setenv("ENTH_TOKEN", "enth_fake_pogod")
	tokFile := filepath.Join(t.TempDir(), "bot.json")
	os.WriteFile(tokFile, []byte(`{"token":"enth_fake_bot"}`), 0o600)

	out, err := Backend{Bin: bin, Repo: vcs.EnthRepo{Repo: "o/r", TokenFile: tokFile}}.run(context.Background(), "whoami")
	if err != nil || string(out) != "token=enth_fake_bot" {
		t.Errorf("with a token file: %q, %v; want the project's token", out, err)
	}
	out, err = Backend{Bin: bin, Repo: vcs.EnthRepo{Repo: "o/r"}}.run(context.Background(), "whoami")
	if err != nil || string(out) != "token=enth_fake_pogod" {
		t.Errorf("without a token file: %q, %v; want pogod's own token, as before", out, err)
	}
	// The project's token is set on the call, never in pogod's environment.
	if os.Getenv("ENTH_TOKEN") != "enth_fake_pogod" {
		t.Error("a call with a project token changed pogod's own ENTH_TOKEN")
	}
}

// A named token file that cannot be read refuses the call before enth runs,
// naming the path: no silent fall-back to the shared login.
func TestRunRefusesAnUnreadableTokenFile(t *testing.T) {
	bin, ran := fakeEnth(t)
	t.Setenv("ENTH_TOKEN", "enth_fake_pogod")
	missing := filepath.Join(t.TempDir(), "gone.json")
	out, err := Backend{Bin: bin, Repo: vcs.EnthRepo{Repo: "o/r", TokenFile: missing}}.run(context.Background(), "whoami")
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing token file: %q, %v; want an error naming %s", out, err, missing)
	}
	if _, serr := os.Stat(ran); !os.IsNotExist(serr) {
		t.Error("enth ran despite the unreadable token file (it would have used the shared login)")
	}
}
