package selfdrift

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/version"
)

// The enth build (docs/design/enth-vcs.md §10, slice 10): an axis stamped from
// an enth checkout carries an enth Change id, which no git check can place. It
// must classify NOT COMPARABLE — never clean, never behind, never foreign.

func TestEnthVCSMatchesTheVersionPackage(t *testing.T) {
	if enthVCS != version.VCSEnth {
		t.Fatalf("selfdrift's enthVCS %q != version.VCSEnth %q — BinaryRev would stop recognising enth builds", enthVCS, version.VCSEnth)
	}
}

// TestBinaryRevRecognisesAnEnthBuild builds real binaries. The enth one is the
// WORST case §10 measured: built inside a git repo without -buildvcs=false, so
// Go stamped a git vcs.revision that is not the build's — the -X VCS=enth flag
// must win over it. The control is the same build without the flag.
func TestBinaryRevRecognisesAnEnthBuild(t *testing.T) {
	repo, plain, rev := gitRepoWithBinary(t)
	enth := filepath.Join(t.TempDir(), "enthbuilt")
	build := exec.Command("go", "build", "-buildvcs=true",
		"-ldflags", "-X github.com/drellem2/pogo/internal/version.VCS=enth", "-o", enth, ".")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if got := BinaryRev(enth); got != RevEnth {
		t.Errorf("BinaryRev(enth build) = %q, want %q — a git vcs.revision on an enth build is the outer repo's, not the build's", got, RevEnth)
	}
	if got := BinaryRev(plain); got != rev {
		t.Errorf("control: BinaryRev(git build) = %q, want %q", got, rev)
	}
}

func TestRunningRevRecognisesAnEnthDaemon(t *testing.T) {
	serve := func(body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		defer srv.Close()
		rev, _ := RunningRev(srv.URL)
		return rev
	}
	if got := serve(`{"revision":"","build":{"commit":"3897079975e8","source":"ldflags","vcs":"enth"}}`); got != RevEnth {
		t.Errorf("enth daemon: rev = %q, want %q (not UNSTAMPED: it named an enth Change)", got, RevEnth)
	}
	if got := serve(`{"revision":"deadbeef","build":{"commit":"deadbeef","source":"ldflags"}}`); got != "deadbeef" {
		t.Errorf("control: git daemon rev = %q, want deadbeef", got)
	}
}

func TestClassifyAnEnthAxisIsNotComparable(t *testing.T) {
	for _, s := range []stub{
		{running: RevEnth, installed: all(RevEnth), repo: "/src/pogo", main: revMain},
		{running: revMain, installed: map[string]string{"pogod": RevEnth, "pogo": revMain}, repo: "/src/pogo", main: revMain},
		{running: RevEnth, installed: all(RevEnth)}, // no repo
	} {
		probed := false
		deps := s.deps()
		inner := deps.RevInRepo
		deps.RevInRepo = func(repo, rev string) bool {
			if rev == RevEnth {
				probed = true
			}
			return inner(repo, rev)
		}
		r := Check(deps, "")
		if r.Status != StatusUnknown || r.NeedsBuild || r.NeedsRestart {
			t.Errorf("%+v: status=%s build=%v restart=%v, want UNKNOWN with nothing owed", s, r.Status, r.NeedsBuild, r.NeedsRestart)
		}
		if !strings.Contains(r.Action, NotComparableEnth) {
			t.Errorf("action %q does not say %q", r.Action, NotComparableEnth)
		}
		if probed {
			t.Error("the foreign test asked git about an enth axis")
		}
	}
	// Control: the same stub with git revisions everywhere is clean.
	if r := Check(stub{running: revMain, installed: all(revMain), repo: "/src/pogo", main: revMain}.deps(), ""); r.Status != StatusClean {
		t.Errorf("control: status = %s, want clean", r.Status)
	}
}

func TestShadowOfAnEnthBuildIsNotBenign(t *testing.T) {
	note, benign := shadowVerdict("/gobin/pogod", revMain, RevEnth, false)
	if benign || !strings.Contains(note, NotComparableEnth) {
		t.Errorf("shadowVerdict(enth) = %q, benign=%v; want a hazard naming %q", note, benign, NotComparableEnth)
	}
}
