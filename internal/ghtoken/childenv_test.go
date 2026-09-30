package ghtoken

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func okHarvest(tok string) func() (string, error) {
	return func() (string, error) { return tok, nil }
}

func countingFail(n *int) func() (string, error) {
	return func() (string, error) { *n++; return "", errors.New("nothing here") }
}

// The credential goes into the returned slice and NOWHERE else: not into this
// process's environment and not through the caller's base slice.
func TestChildEnv_TokenReachesTheSliceNotTheProcess(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	base := []string{"PATH=/usr/bin", "GH_TOKEN="}
	baseCopy := append([]string(nil), base...)

	env, res := childEnv(base, okHarvest(fakeToken), func() (string, error) {
		t.Fatal("gh asked although the shell yielded")
		return "", nil
	})
	if res.Source != SourceShell {
		t.Fatalf("source = %s, want shell", res.Source)
	}
	if got := lookupEnv(env, "GH_TOKEN"); got != fakeToken {
		t.Fatalf("child env GH_TOKEN is not the harvested value (len %d)", len(got))
	}
	if os.Getenv("GH_TOKEN") != "" {
		t.Fatal("childEnv wrote GH_TOKEN into the process environment")
	}
	for i := range base {
		if base[i] != baseCopy[i] {
			t.Fatalf("caller's base mutated at %d", i)
		}
	}
}

func TestChildEnv_AmbientBaseIsReturnedUnchangedAndNothingIsAsked(t *testing.T) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		asked := 0
		base := []string{"PATH=/usr/bin", k + "=already"}
		env, res := childEnv(base, countingFail(&asked), countingFail(&asked))
		if res.Source != SourceAmbient || asked != 0 || len(env) != len(base) {
			t.Errorf("%s ambient: source=%s asked=%d len=%d", k, res.Source, asked, len(env))
		}
	}
}

func TestChildEnv_FallsThroughToGH(t *testing.T) {
	asked := 0
	env, res := childEnv([]string{"PATH=/usr/bin"}, countingFail(&asked), okHarvest(fakeToken))
	if res.Source != SourceGH || lookupEnv(env, "GH_TOKEN") != fakeToken || asked != 1 {
		t.Fatalf("source=%s asked=%d", res.Source, asked)
	}
}

// No source yields: the child gets exactly base, and the result says why
// without carrying a value.
func TestChildEnv_NoneLeavesBaseAndCarriesNoValue(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	env, res := childEnv(base, okHarvest("has space "+fakeToken), okHarvest(""))
	if res.OK() || len(env) != 1 || lookupEnv(env, "GH_TOKEN") != "" {
		t.Fatalf("source=%s env=%d", res.Source, len(env))
	}
	if strings.Contains(res.String(), fakeToken) {
		t.Fatal("result text carries the rejected candidate")
	}
}

// Per call, not cached: a rotated token is what the NEXT call hands out.
func TestChildEnv_EveryCallHarvestsAfresh(t *testing.T) {
	calls := 0
	shell := func() (string, error) { calls++; return fmt.Sprintf("tok-%08d", calls), nil }
	restore := SetChildHarvestForTest(shell, okHarvest(""))
	defer restore()
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	a := lookupEnv(ChildEnv(os.Environ()), "GH_TOKEN")
	b := lookupEnv(ChildEnv(os.Environ()), "GH_TOKEN")
	if calls != 2 || a == b {
		t.Fatalf("calls=%d; the second call reused the first value", calls)
	}
}

// Transition-only, existence-only logging: a healthy run logs nothing, a loss
// logs once, a recovery logs once.
func TestChildEnv_LogsOnlyTransitions(t *testing.T) {
	var lines []string
	prevLog := childLogf
	childLogf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	defer func() { childLogf = prevLog }()

	fail := func() (string, error) { return "", errors.New("no export") }
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	restore := SetChildHarvestForTest(okHarvest(fakeToken), fail)
	ChildEnv(os.Environ())
	ChildEnv(os.Environ())
	if len(lines) != 0 {
		t.Fatalf("healthy calls logged: %q", lines)
	}
	restore()

	restore = SetChildHarvestForTest(fail, fail)
	defer restore()
	childMu.Lock()
	ok := true
	childLastOK = &ok
	childMu.Unlock()
	ChildEnv(os.Environ())
	ChildEnv(os.Environ())
	if len(lines) != 1 || !strings.Contains(lines[0], "UNAVAILABLE") {
		t.Fatalf("loss should log exactly once, got %q", lines)
	}
	childMu.Lock()
	childShellHarvest = func(context.Context) (string, error) { return fakeToken, nil }
	childMu.Unlock()
	ChildEnv(os.Environ())
	if len(lines) != 2 || !strings.Contains(lines[1], "available again (source=shell)") {
		t.Fatalf("recovery should log once, got %q", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, fakeToken) {
			t.Fatal("a log line carries the value")
		}
	}
}

func TestGitNeedsCredential(t *testing.T) {
	cases := map[string]bool{
		"fetch origin":                      true,
		"push --force-with-lease origin b":  true,
		"ls-remote --heads origin main":     true,
		"-C /r fetch --no-tags origin":      true,
		"-c a=b pull":                       true,
		"clone --no-local /a /b":            true,
		"rebase origin/main":                false,
		"rev-parse HEAD":                    false,
		"remote get-url origin":             false,
		"-C /fetch rev-parse HEAD":          false,
		"update-ref refs/heads/push abcdef": false,
		"":                                  false,
	}
	for in, want := range cases {
		if got := GitNeedsCredential(strings.Fields(in)); got != want {
			t.Errorf("GitNeedsCredential(%q) = %v, want %v", in, got, want)
		}
	}
}

// hangingStub writes an executable that never returns on its own and puts its
// directory first on PATH.
func hangingStub(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

// TestChildEnvContextBoundsTheFetch (mg-c258b): with no ambient credential and
// BOTH real sources hung — the user shell's probe and `gh auth token` — the
// fetch ends at the caller's deadline, not at 2 x probeTimeout. This is the
// state of a CI runner with a hung gh stub first on PATH: before the fix the
// ghpr lookup bounded at 300ms took 15s there.
func TestChildEnvContextBoundsTheFetch(t *testing.T) {
	var lines []string
	prevLog := childLogf
	childLogf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	defer func() { childLogf = prevLog }()
	childMu.Lock()
	prevOK := childLastOK
	ok := true
	childLastOK = &ok
	childMu.Unlock()
	defer func() { childMu.Lock(); childLastOK = prevOK; childMu.Unlock() }()

	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("SHELL", hangingStub(t, "sh-hung"))
	hangingStub(t, "gh")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	env := ChildEnvContext(ctx, os.Environ())
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("credential fetch took %s against a 300ms deadline", d)
	}
	if v := lookupEnv(env, "GH_TOKEN"); v != "" {
		t.Error("a hung chain produced a credential")
	}
	// A deadline says nothing about whether a credential exists.
	if len(lines) != 0 {
		t.Errorf("a fetch cut short by the caller's deadline logged: %q", lines)
	}
}

// TestChildEnvContextStillFetches is the positive control: an unexpired ctx
// lets the chain yield exactly as ChildEnv does.
func TestChildEnvContextStillFetches(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	restore := SetChildHarvestForTest(okHarvest(fakeToken), failHarvest("no gh"))
	defer restore()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if v := lookupEnv(ChildEnvContext(ctx, os.Environ()), "GH_TOKEN"); v != fakeToken {
		t.Error("ChildEnvContext with time left did not deliver the credential")
	}
}
