package ghteardown

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubGH installs a fake `gh` at the front of PATH so the exit-code and parse
// handling can be driven through the REAL GHLookup, without a network.
//
// This exercise exists because the mutation "ignore gh's exit code and treat a
// failed call as CLOSED" originally survived the whole suite — the tri-state
// discipline was documented and implemented but never actually pinned. That
// mutant is the single most dangerous bug this package can have: it makes the
// detector answer "all clear" for every carrier the moment auth expires.
func stubGH(t *testing.T, exitCode int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\nprintf '%%s' %s >&2\nexit %d\n",
		shellQuote(stdout), shellQuote(stderr), exitCode)
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestGHLookupStates(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stdout   string
		stderr   string
		want     IssueState
		wantErr  bool
	}{
		{
			name:   "open issue",
			stdout: `{"number":89,"state":"OPEN"}`,
			want:   StateOpen,
		},
		{
			name:   "closed issue",
			stdout: `{"number":89,"state":"CLOSED"}`,
			want:   StateClosed,
		},
		{
			// The founding failure mode. gh exits 1 and prints nothing usable;
			// a parse that only looks for "OPEN" would call this CLOSED and
			// report the carrier clean.
			name:     "nonexistent issue must be UNKNOWN, never closed",
			exitCode: 1,
			stderr:   "GraphQL: Could not resolve to an issue or pull request with the number of 99999. (repository.issue)",
			want:     StateUnknown,
			wantErr:  true,
		},
		{
			// A carrier whose gh: ref names a repo that no longer resolves —
			// renamed, deleted, or an issue transferred away with no redirect.
			// "Gone" is not "closed": an issue that moved and is still open at
			// its new address is a teardown miss that changed address.
			name:     "unresolvable repo must be UNKNOWN, never closed",
			exitCode: 1,
			stderr:   "GraphQL: Could not resolve to a Repository with the name 'drellem2/gone'. (repository)",
			want:     StateUnknown,
			wantErr:  true,
		},
		{
			name:     "auth failure must be UNKNOWN, never closed",
			exitCode: 4,
			stderr:   "gh: To use GitHub CLI in a GitHub Actions workflow, set the GH_TOKEN environment variable.",
			want:     StateUnknown,
			wantErr:  true,
		},
		{
			name:     "rate limit must be UNKNOWN, never closed",
			exitCode: 1,
			stderr:   "API rate limit exceeded for user ID 12345.",
			want:     StateUnknown,
			wantErr:  true,
		},
		{
			// gh succeeded but emitted something we cannot read: a changed
			// output shape must not silently become a verdict.
			name:    "unparseable success output is UNKNOWN",
			stdout:  "not json at all",
			want:    StateUnknown,
			wantErr: true,
		},
		{
			name:    "missing state field is UNKNOWN",
			stdout:  `{"number":89}`,
			want:    StateUnknown,
			wantErr: true,
		},
		{
			// A state GitHub might add later. Anything we do not positively
			// recognise as CLOSED leaves the carrier unresolved.
			name:    "unmodelled state is UNKNOWN",
			stdout:  `{"number":89,"state":"MERGED"}`,
			want:    StateUnknown,
			wantErr: true,
		},
		{
			// gh is case-stable in practice, but a parse that depends on case
			// is a latent version-bump outage.
			name:   "lowercase state still parses",
			stdout: `{"number":89,"state":"open"}`,
			want:   StateOpen,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubGH(t, tc.exitCode, tc.stdout, tc.stderr)

			got, err := GHLookup("drellem2/pogo", 89)
			if got != tc.want {
				t.Errorf("state = %q, want %q (err: %v)", got, tc.want, err)
			}
			if tc.wantErr && err == nil {
				t.Error("want an error explaining why the state is unknown, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			// The strongest invariant in the package: nothing but a positive,
			// parsed CLOSED may ever clear a carrier.
			if got == StateClosed && tc.want != StateClosed {
				t.Fatal("REPORTED CLOSED WITHOUT POSITIVE EVIDENCE — the detector would call this carrier clean")
			}
		})
	}
}

// A missing gh binary is an environment failure, not a clean bill of health.
func TestGHLookupWithNoGHBinaryIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, err := GHLookup("drellem2/pogo", 89)
	if got != StateUnknown || err == nil {
		t.Fatalf("missing gh must be UNKNOWN with an error, got %q / %v", got, err)
	}
}

// A ref that never parsed must be rejected before any network call — there is
// nothing meaningful to ask GitHub, and a lookup that invents a URL could
// answer about the wrong issue entirely.
func TestGHLookupRejectsUnresolvableRefsWithoutCallingGH(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	// A stub that fails the test if it is ever invoked.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	for _, tc := range []struct {
		repo   string
		number int
	}{
		{"", 89}, {"drellem2/pogo", 0}, {"drellem2/pogo", -1},
	} {
		got, err := GHLookup(tc.repo, tc.number)
		if got != StateUnknown || err == nil {
			t.Errorf("GHLookup(%q, %d) = %q / %v, want UNKNOWN with an error", tc.repo, tc.number, got, err)
		}
	}
}
