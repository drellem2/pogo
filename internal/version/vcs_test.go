package version

import (
	"encoding/json"
	"strings"
	"testing"
)

// The enth stamp (docs/design/enth-vcs.md §10, slice 10). The git half of each
// test is the control: VCS empty must leave a git binary's identity, JSON and
// human line exactly as they were.

func TestWithVCSRecordsEnthOnAnLdflagsStamp(t *testing.T) {
	const id = "3897079975e8712d1191d271d258a53b0d17556f87ca337d93d9e7faf1d01573"
	got := withVCS(resolve("0.12.0", id[:12], id, "", "false", vcsStamp{}), VCSEnth)
	if got.Source != SourceLdflags || got.Commit != id {
		t.Fatalf("got %+v, want an ldflags stamp naming the Change id", got)
	}
	if !got.Enth() || got.VCS != VCSEnth {
		t.Errorf("VCS = %q, Enth() = %v; want %q, true", got.VCS, got.Enth(), VCSEnth)
	}
	if !strings.Contains(got.Describe("pogo"), ", vcs=enth)") {
		t.Errorf("human line %q does not say vcs=enth — a reader would take the id for a git sha", got.Describe("pogo"))
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"vcs":"enth"`) {
		t.Errorf("JSON %s does not carry vcs", b)
	}
}

func TestWithVCSLeavesGitUnchanged(t *testing.T) {
	const sha = "fa02447e5405efb5e924ccd5a328a730119d0ec9"
	before := resolve("0.12.0", "", sha, "main", "false", vcsStamp{})
	after := withVCS(before, "")
	if after != before {
		t.Errorf("withVCS(\"\") changed a git stamp: %+v -> %+v", before, after)
	}
	b, _ := json.Marshal(after)
	if strings.Contains(string(b), "vcs") {
		t.Errorf("a git binary's JSON grew a vcs key: %s", b)
	}
	if strings.Contains(after.Describe("pogo"), "vcs=") {
		t.Errorf("a git binary's human line grew vcs=: %q", after.Describe("pogo"))
	}
	if after.Enth() {
		t.Error("a git stamp reports Enth()")
	}
}

// A VCS flag without a Commit flag cannot make a buildinfo stamp (always git's,
// and here possibly the OUTER repo's) masquerade as enth, nor the reverse.
func TestWithVCSIgnoredWithoutAnLdflagsStamp(t *testing.T) {
	for _, bi := range []vcsStamp{{revision: "deadbeefdeadbeef"}, {}} {
		got := withVCS(resolve("0.12.0", "", "", "", "", bi), VCSEnth)
		if got.VCS != "" {
			t.Errorf("source=%s carries VCS=%q; only an ldflags stamp may name one", got.Source, got.VCS)
		}
	}
}
