package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// goldenProjects is a projects.json in the shape pogo writes today: written by
// json.Marshal, so no trailing newline, HTML characters escaped as \u00XX and
// non-ASCII left raw, ids non-contiguous after pruning, directory paths
// carrying a trailing separator.
const goldenProjects = "internal/project/testdata/projects.golden.json"

// roundTrip loads data as the registry (Init) and writes it back
// (SaveProjects), returning what landed on disk.
func roundTrip(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	origName := ProjectFileName
	t.Cleanup(func() { ProjectFileName = origName })
	ProjectFileName = name
	Init()
	t.Cleanup(RemoveSaveFile)
	if err := os.WriteFile(projectFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	Init()
	SaveProjects()
	out, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGoldenProjectsRoundTripIsByteIdentical is slice 4's proof (§2,
// "byte-identical git"): a projects.json with no vcs key reads and re-writes
// byte for byte, so adding the vcs and enth fields changes nothing on disk for
// any existing record.
func TestGoldenProjectsRoundTripIsByteIdentical(t *testing.T) {
	golden, err := os.ReadFile(goldenProjects)
	if err != nil {
		t.Fatal(err)
	}
	// Positive control on the fixture itself: it must exercise the encoder's
	// escaping, or a round trip of it proves less than it claims.
	if !bytes.Contains(golden, []byte(`\`+"u0026")) || bytes.HasSuffix(golden, []byte("\n")) {
		t.Fatalf("fixture is not in json.Marshal's shape: %s", golden)
	}
	out := roundTrip(t, "projects-golden-test.json", golden)
	if !bytes.Equal(out, golden) {
		t.Errorf("round trip changed projects.json:\n got: %s\nwant: %s", out, golden)
	}
	if n := len(Projects()); n != 6 {
		t.Errorf("loaded %d projects, want 6 (the fixture did not parse)", n)
	}
	for _, p := range Projects() {
		if p.VCS != "" || p.Enth != nil || p.Kind() != vcs.Git {
			t.Errorf("record %d: VCS=%q Enth=%v, want an unset git record", p.Id, p.VCS, p.Enth)
		}
	}

	// The same holds for what GET /projects serves: encoding the loaded
	// records adds no key.
	served, err := json.Marshal(Projects())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(served, []byte(`"vcs"`)) || bytes.Contains(served, []byte(`"enth"`)) {
		t.Errorf("/projects encoding grew a vcs key: %s", served)
	}
}

// TestEnthRecordRoundTrips: a record with a selection survives a load/save
// too, and a git selection writes no key at all.
func TestEnthRecordRoundTrips(t *testing.T) {
	in := []byte(`{"projects":[{"id":7,"path":"/x/enth/","vcs":"enth","enth":{"remote":"https://enth.app","repo":"drellem2/enth","trunk":"refs/heads/main"}},{"id":8,"path":"/x/pogo/"}]}`)
	out := roundTrip(t, "projects-enth-test.json", in)
	if !bytes.Equal(out, in) {
		t.Errorf("enth record did not round trip:\n got: %s\nwant: %s", out, in)
	}
	k, e := VCSOf("/x/enth")
	if k != vcs.Enth || e == nil || e.Repo != "drellem2/enth" {
		t.Errorf("VCSOf(/x/enth) = %q, %+v; want enth drellem2/enth", k, e)
	}
	if k, e := VCSOf("/x/pogo/"); k != vcs.Git || e != nil {
		t.Errorf("VCSOf(/x/pogo/) = %q, %+v; want git", k, e)
	}
	if k, _ := VCSOf("/not/registered"); k != vcs.Git {
		t.Errorf("VCSOf of an unregistered repo = %q, want git", k)
	}

	// Setting it back to git removes both keys, restoring the git-only bytes.
	if _, err := SetVCS("/x/enth", vcs.Git, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(projectFile)
	want := `{"projects":[{"id":7,"path":"/x/enth/"},{"id":8,"path":"/x/pogo/"}]}`
	if string(got) != want {
		t.Errorf("after set-vcs git:\n got: %s\nwant: %s", got, want)
	}
}

func busyWith(reasons ...string) (BusyFunc, *[]string) {
	var asked []string
	return func(repo string) []string {
		asked = append(asked, repo)
		return reasons
	}, &asked
}

// TestSetVCSRefusesABusyRepo: a change of backend while the repo has open MRs
// or live polecats is refused, and the record is left exactly as it was.
func TestSetVCSRefusesABusyRepo(t *testing.T) {
	in := []byte(`{"projects":[{"id":1,"path":"/x/repo/"}]}`)
	roundTrip(t, "projects-setvcs-busy-test.json", in)

	busy, asked := busyWith("open MR mr-1 (polecat-a -> main, queued)", "live polecat cat-a")
	enth := &vcs.EnthRepo{Remote: "https://enth.app", Repo: "o/r"}
	_, err := SetVCS("/x/repo", vcs.Enth, enth, busy)
	var be *BusyError
	if !errors.As(err, &be) {
		t.Fatalf("SetVCS on a busy repo = %v, want a *BusyError", err)
	}
	if be.From != vcs.Git || be.To != vcs.Enth || len(be.Reasons) != 2 {
		t.Errorf("BusyError = %+v", be)
	}
	if !strings.Contains(err.Error(), "mr-1") || !strings.Contains(err.Error(), "cat-a") {
		t.Errorf("refusal does not name what is in flight: %v", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "/x/repo/" {
		t.Errorf("busy was asked about %v, want the record's path", *asked)
	}
	got, _ := os.ReadFile(projectFile)
	if !bytes.Equal(got, in) {
		t.Errorf("a refused set-vcs changed the file:\n got: %s\nwant: %s", got, in)
	}
	if k, _ := VCSOf("/x/repo"); k != vcs.Git {
		t.Errorf("a refused set-vcs changed the record to %q", k)
	}

	// Positive control: the same change on a quiet repo goes through.
	quiet, _ := busyWith()
	p, err := SetVCS("/x/repo", vcs.Enth, enth, quiet)
	if err != nil {
		t.Fatalf("SetVCS on a quiet repo: %v", err)
	}
	if p.VCS != vcs.Enth || p.Enth.Trunk != vcs.DefaultEnthTrunk {
		t.Errorf("record after set-vcs = %+v, want enth with the default trunk", p)
	}

	// Re-stating the current selection is not a change, so a busy repo
	// does not refuse it and busy is not even consulted.
	busy2, asked2 := busyWith("open MR mr-2")
	if _, err := SetVCS("/x/repo", vcs.Enth, &vcs.EnthRepo{Remote: "https://enth.app", Repo: "o/r", Trunk: vcs.DefaultEnthTrunk}, busy2); err != nil {
		t.Errorf("re-stating the selection on a busy repo: %v", err)
	}
	if len(*asked2) != 0 {
		t.Errorf("busy consulted for a no-op set-vcs")
	}
	// But changing the enth settings is a change, and is refused.
	if _, err := SetVCS("/x/repo", vcs.Enth, &vcs.EnthRepo{Remote: "https://other", Repo: "o/r"}, busy2); !errors.As(err, &be) {
		t.Errorf("changing the enth remote on a busy repo = %v, want a *BusyError", err)
	}
}

func TestSetVCSValidates(t *testing.T) {
	roundTrip(t, "projects-setvcs-validate-test.json", []byte(`{"projects":[{"id":1,"path":"/x/repo/"}]}`))
	cases := []struct {
		name string
		kind vcs.Kind
		enth *vcs.EnthRepo
	}{
		{"unknown kind", "hg", nil},
		{"enth without settings", vcs.Enth, nil},
		{"enth without repo", vcs.Enth, &vcs.EnthRepo{Remote: "https://enth.app"}},
		{"git with enth settings", vcs.Git, &vcs.EnthRepo{Remote: "https://enth.app", Repo: "o/r"}},
	}
	for _, c := range cases {
		if _, err := SetVCS("/x/repo", c.kind, c.enth, nil); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := SetVCS("/x/elsewhere", vcs.Git, nil, nil); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("unregistered path = %v, want ErrNotRegistered", err)
	}
}

// The enth token file (mg-3a813) is set and cleared on an existing record,
// stored as a path, and — not being a change of backend — not refused while
// the repo is busy. A change of anything else alongside it still is.
func TestSetVCSTokenFileOnAnExistingProject(t *testing.T) {
	in := []byte(`{"projects":[{"id":1,"path":"/x/repo/","vcs":"enth","enth":{"remote":"https://enth.app","repo":"o/r","trunk":"refs/heads/main"}}]}`)
	roundTrip(t, "projects-setvcs-token-test.json", in)
	base := vcs.EnthRepo{Remote: "https://enth.app", Repo: "o/r", Trunk: vcs.DefaultEnthTrunk}

	busy, asked := busyWith("live polecat cat-a")
	withTok := base
	withTok.TokenFile = "/secrets/bot.json"
	if _, err := SetVCS("/x/repo", vcs.Enth, &withTok, busy); err != nil {
		t.Fatalf("setting the token file on a busy repo: %v", err)
	}
	if len(*asked) != 0 {
		t.Errorf("busy consulted for a token-only change")
	}
	got, _ := os.ReadFile(projectFile)
	want := `{"projects":[{"id":1,"path":"/x/repo/","vcs":"enth","enth":{"remote":"https://enth.app","repo":"o/r","trunk":"refs/heads/main","token_file":"/secrets/bot.json"}}]}`
	if string(got) != want {
		t.Errorf("record with a token file:\n got: %s\nwant: %s", got, want)
	}
	if _, e := VCSOf("/x/repo"); e == nil || e.TokenFile != "/secrets/bot.json" {
		t.Errorf("VCSOf does not carry the token file: %+v", e)
	}

	// Changing the remote with the token is a change of backend: refused.
	moved := withTok
	moved.Remote = "https://other"
	moved.TokenFile = "/secrets/other.json"
	var be *BusyError
	if _, err := SetVCS("/x/repo", vcs.Enth, &moved, busy); !errors.As(err, &be) {
		t.Errorf("remote+token change on a busy repo = %v, want a *BusyError", err)
	}

	// Clearing it restores the original bytes.
	if _, err := SetVCS("/x/repo", vcs.Enth, &base, busy); err != nil {
		t.Fatalf("clearing the token file: %v", err)
	}
	got, _ = os.ReadFile(projectFile)
	if !bytes.Equal(got, in) {
		t.Errorf("after clearing:\n got: %s\nwant: %s", got, in)
	}
}
