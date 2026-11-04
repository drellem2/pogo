package agent

import "testing"

// seedHarnessProfile must run the hook only when the spawn command runs the
// provider's own binary: a `sleep 600` sandbox command must not write to
// anyone's profile (drellem2/pogo#286).
func TestSeedHarnessProfileRunsOnlyForTheProvidersBinary(t *testing.T) {
	var got [][]string
	p := &Provider{ID: "claude", Binary: "claude", ProfileSeedHook: func(env []string) error {
		got = append(got, env)
		return nil
	}}
	env := []string{"HOME=/h"}

	seedHarnessProfile(p, "a", []string{"sleep", "600"}, env)
	seedHarnessProfile(p, "a", []string{"/bin/sh", "-c", "claude"}, env)
	seedHarnessProfile(p, "a", nil, env)
	seedHarnessProfile(nil, "a", []string{"claude"}, env)
	seedHarnessProfile(&Provider{Binary: "claude"}, "a", []string{"claude"}, env)
	if len(got) != 0 {
		t.Fatalf("hook ran %d times for commands that are not the provider's binary", len(got))
	}

	seedHarnessProfile(p, "a", []string{"/opt/homebrew/bin/claude", "--x"}, env)
	if len(got) != 1 || got[0][0] != "HOME=/h" {
		t.Fatalf("hook calls = %v, want one call with the spawn env", got)
	}
}

func TestSeedHarnessProfileErrorDoesNotPanic(t *testing.T) {
	p := &Provider{ID: "claude", Binary: "claude", ProfileSeedHook: func([]string) error {
		return errTestSeed
	}}
	seedHarnessProfile(p, "a", []string{"claude"}, nil) // logs and returns
}

var errTestSeed = &seedErr{}

type seedErr struct{}

func (*seedErr) Error() string { return "seed failed" }
