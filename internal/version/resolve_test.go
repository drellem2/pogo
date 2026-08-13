package version

import (
	"encoding/json"
	"strings"
	"testing"
)

// resolve() is pure, so every case below is driven directly rather than by
// building four differently-stamped binaries. The END-TO-END assertion — that a
// binary built by build.sh and by pogo-self-deploy actually reports the right
// commit when RUN — is scripts/stamp_test.sh, because a wrong `-X` symbol path
// looks perfectly correct in Go source and fails only at link time.

func TestResolveLdflagsStampWins(t *testing.T) {
	const sha = "fa02447e5405efb5e924ccd5a328a730119d0ec9"
	got := resolve("0.10.0", "", sha, "main", "", vcsStamp{revision: "deadbeefdeadbeef"})
	if got.Source != SourceLdflags {
		t.Errorf("source = %q, want %q", got.Source, SourceLdflags)
	}
	if got.Commit != sha {
		t.Errorf("commit = %q, want the ldflags value %q", got.Commit, sha)
	}
	if got.Branch != "main" {
		t.Errorf("branch = %q, want main", got.Branch)
	}
	// Build is derived from the commit when not stamped separately, so the two
	// can never disagree about which revision this binary is.
	if got.Build != sha[:7] {
		t.Errorf("build = %q, want %q derived from commit", got.Build, sha[:7])
	}
	if got.Dirty {
		t.Error("dirty should be false when the Dirty stamp is empty")
	}
	if !got.Stamped() {
		t.Error("an ldflags-stamped binary must report Stamped()")
	}
}

func TestResolveDirtyIsCarried(t *testing.T) {
	got := resolve("0.10.0", "abc1234", "abc1234def", "wip", "true", vcsStamp{})
	if !got.Dirty {
		t.Fatal("Dirty=\"true\" must resolve to dirty")
	}
	// The marker has to reach the line a human reads, not only the JSON: a
	// revision quoted without it is a claim about the repo, not the binary.
	if d := got.Describe("pogo"); !strings.Contains(d, "abc1234-dirty") {
		t.Errorf("Describe = %q, want the -dirty suffix on the build id", d)
	}
}

// The fallback exists so a build path nobody remembered to patch still says
// something true — and it must say WHERE it got the revision, because in this
// fleet's directory layout the automatic stamp can name a foreign repo (see
// resolve.go's header; measured, not hypothesised).
func TestResolveFallsBackToBuildInfoAndNamesTheSource(t *testing.T) {
	const sha = "d533d174902c8b6cfda96c12510683dbeb205abe"
	got := resolve("0.10.0", "", "", "", "", vcsStamp{revision: sha, modified: true})
	if got.Source != SourceBuildInfo {
		t.Errorf("source = %q, want %q", got.Source, SourceBuildInfo)
	}
	if got.Commit != sha {
		t.Errorf("commit = %q, want %q", got.Commit, sha)
	}
	if !got.Dirty {
		t.Error("vcs.modified=true must surface as dirty")
	}
	// Build info records no branch. Reporting one here would attach a branch to
	// a revision that did not come with it.
	if got.Branch != Unknown {
		t.Errorf("branch = %q, want %q — build info carries no branch", got.Branch, Unknown)
	}
}

// The whole point of the ticket: never the empty string.
func TestResolveUnstampedIsUnknownNotEmpty(t *testing.T) {
	got := resolve("0.10.0", "", "", "", "", vcsStamp{})
	for name, v := range map[string]string{"commit": got.Commit, "branch": got.Branch, "build": got.Build} {
		if v == "" {
			t.Errorf("%s is the empty string — indistinguishable from a stamping bug in the reader", name)
		}
		if v != Unknown {
			t.Errorf("%s = %q, want %q", name, v, Unknown)
		}
	}
	if got.Source != SourceNone {
		t.Errorf("source = %q, want %q", got.Source, SourceNone)
	}
	if got.Stamped() {
		t.Error("an unstamped binary must NOT report Stamped()")
	}
	// Version is known from the source tree even when nothing else is, so it
	// must not be swept into the unknowns.
	if got.Version != "0.10.0" {
		t.Errorf("version = %q, want 0.10.0", got.Version)
	}
}

// The JSON shape is a contract: `pogo version --json | jq -r .commit` is the
// documented way to ask the liveness question, and `source` is what stops that
// answer being quoted without its provenance.
func TestInfoJSONShape(t *testing.T) {
	b, err := json.Marshal(resolve("0.10.0", "", "abc1234def", "main", "true", vcsStamp{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"version", "build", "commit", "branch", "dirty", "source"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
	if m["dirty"] != true {
		t.Errorf("dirty = %v, want true (a bool, not a string)", m["dirty"])
	}
}

// Get() runs against whatever stamped THIS test binary. `go test` binaries
// carry no ldflags and Go does stamp vcs info into them, so this asserts only
// the invariant that holds for every build: no field is ever empty.
func TestGetNeverReportsEmptyFields(t *testing.T) {
	got := Get()
	if got.Version == "" || got.Build == "" || got.Commit == "" || got.Branch == "" || got.Source == "" {
		t.Errorf("Get() returned an empty field: %+v", got)
	}
	if !strings.HasPrefix(got.Describe("pogo"), "pogo ") {
		t.Errorf("Describe = %q", got.Describe("pogo"))
	}
}
