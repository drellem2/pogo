package ineffect

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The host wiring, against a REAL git repo. The fake-Deps tests above pin the
// judgement and skip every place a bug actually lives: the git invocations, the
// ancestor exit-code handling, the installed-copy lookup. A suite that only
// tested the classifier would be green while `pogo in-effect` answered nothing.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureRepo builds a two-commit history:
//
//	c1  adds scripts/launchd/run.sh ("v1") and internal/thing/thing.go
//	c2  changes run.sh to "v2"
func fixtureRepo(t *testing.T) (repo, c1, c2 string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "scripts", "launchd", "run.sh"), "v1\n")
	write(t, filepath.Join(repo, "internal", "thing", "thing.go"), "package thing\n")
	write(t, filepath.Join(repo, "README.md"), "docs\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "c1")
	c1 = git(t, repo, "rev-parse", "HEAD")

	write(t, filepath.Join(repo, "scripts", "launchd", "run.sh"), "v2\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "c2")
	c2 = git(t, repo, "rev-parse", "HEAD")
	return repo, c1, c2
}

// opts builds HostOpts that touch NOTHING on the live box. Two redirections,
// both necessary and neither implied by testsandbox's HOME pinning:
//
//	POGO_GOBIN   selfdrift.InstalledBin falls back to $PATH, so without this the
//	             carrier list is the developer's real ~/go/bin and the suite's
//	             rows change with whether they last ran `go install`.
//	DaemonURL    config.Load()'s default is 127.0.0.1:10000, which on this box
//	             is the LIVE pogod. A read-only probe is still a reading of the
//	             operator's fleet, and it makes a test's carrier set depend on
//	             whether the daemon happens to be up.
func opts(t *testing.T, repo string) HostOpts {
	t.Helper()
	t.Setenv("POGO_GOBIN", t.TempDir())
	return HostOpts{
		Repo:      repo,
		Ref:       "main",
		PogoHome:  t.TempDir(),
		DeploySrc: filepath.Join(t.TempDir(), "absent"),
		// Port 1 is reserved and never listening; the probe fails fast and
		// reports <unreachable>, which is the honest value for a box with no
		// daemon and is never confused with a revision.
		DaemonURL: "http://127.0.0.1:1",
	}
}

func TestHostDepsGitObservations(t *testing.T) {
	repo, c1, c2 := fixtureRepo(t)
	deps := HostDeps(opts(t, repo))

	full, subject, when, err := deps.Resolve("HEAD")
	if err != nil || full != c2 || subject != "c2" || when == "" {
		t.Fatalf("Resolve(HEAD) = (%q, %q, %q, %v), want c2 with a subject and a date", full, subject, when, err)
	}
	if _, _, _, err := deps.Resolve("no-such-ref"); err == nil {
		t.Error("Resolve of an unknown ref succeeded; a report about a commit that does not exist is worse than a refusal")
	}

	paths, err := deps.ChangedPaths(c2)
	if err != nil || len(paths) != 1 || paths[0] != "scripts/launchd/run.sh" {
		t.Fatalf("ChangedPaths(c2) = %v (%v), want just the script", paths, err)
	}

	// The ancestry predicate, in both directions. `merge-base --is-ancestor`
	// exits 1 for a real negative; anything else is a failure to measure and
	// must surface as an error rather than as a false negative.
	if ok, err := deps.IsAncestor(c1, c2); err != nil || !ok {
		t.Errorf("IsAncestor(c1, c2) = (%v, %v), want true", ok, err)
	}
	if ok, err := deps.IsAncestor(c2, c1); err != nil || ok {
		t.Errorf("IsAncestor(c2, c1) = (%v, %v), want (false, nil) — a real negative is not an error", ok, err)
	}
	if _, err := deps.IsAncestor("0000000000000000000000000000000000000000", c1); err == nil {
		t.Error("IsAncestor against a nonexistent object returned no error; `I could not tell` would be reported as `not an ancestor`")
	}

	blob, err := deps.BlobAt(c1, "scripts/launchd/run.sh")
	if err != nil || string(blob) != "v1\n" {
		t.Errorf("BlobAt(c1) = (%q, %v), want v1", blob, err)
	}
	if _, err := deps.BlobAt(c1, "no/such/file"); err == nil {
		t.Error("BlobAt of an absent path returned no error")
	}

	hist, err := deps.PathHistory("scripts/launchd/run.sh", 10)
	if err != nil || len(hist) != 2 || hist[0] != c2 {
		t.Fatalf("PathHistory = %v (%v), want [c2 c1] newest first — the dating walk depends on the order", hist, err)
	}
}

// End to end against the real wiring: an installed copy holding the OLD bytes,
// asked about the commit that replaced them. This is the ticket's instance in
// miniature and the only test that exercises Assess, HostDeps and git together.
func TestAssessDatesAnInstalledCopyThroughRealGit(t *testing.T) {
	repo, c1, c2 := fixtureRepo(t)
	home := t.TempDir()
	write(t, filepath.Join(home, "bin", "run.sh"), "v1\n")

	o := opts(t, repo)
	o.PogoHome = home
	rep := Assess(c2, HostDeps(o))

	var cv *CarrierVerdict
	for i := range rep.Findings {
		for j := range rep.Findings[i].Carriers {
			if rep.Findings[i].Carriers[j].Carrier == "installed copy" {
				cv = &rep.Findings[i].Carriers[j]
			}
		}
	}
	if cv == nil {
		t.Fatalf("no `installed copy` row for a script that has one at %s/bin/run.sh; report:\n%s", home, rep.Text())
	}
	if cv.Verdict != Inert {
		t.Errorf("installed copy = %q, want %q — it holds v1, and c2 replaced v1", cv.Verdict, Inert)
	}
	if !strings.Contains(cv.Why, short(c1)) {
		t.Errorf("why = %q, want it to name %s, the revision whose bytes the copy holds", cv.Why, short(c1))
	}

	// And the same commit asked about the copy that DOES hold its bytes.
	write(t, filepath.Join(home, "bin", "run.sh"), "v2\n")
	rep2 := Assess(c2, HostDeps(o))
	for _, f := range rep2.Findings {
		for _, c := range f.Carriers {
			if c.Carrier == "installed copy" && c.Verdict != Live {
				t.Errorf("installed copy holding v2 = %q, want %q: %s", c.Verdict, Live, c.Why)
			}
		}
	}
}

// A script with NO installed copy produces no `installed copy` row at all. An
// absent copy is not an unknown carrier — it is not a carrier — and rendering
// it as UNKNOWN would put a permanent unresolvable row on every gate script.
func TestScriptWithoutAnInstalledCopyGetsNoCopyRow(t *testing.T) {
	repo, _, c2 := fixtureRepo(t)
	o := opts(t, repo) // PogoHome has no bin/ at all
	rep := Assess(c2, HostDeps(o))
	for _, f := range rep.Findings {
		for _, c := range f.Carriers {
			if c.Carrier == "installed copy" {
				t.Errorf("an `installed copy` row appeared for a script with no copy on disk: %+v", c)
			}
		}
	}
}

// The checkout carriers are probed, not declared: a deploy checkout that is not
// there is silently absent rather than reported as a stale carrier, and one
// that IS there is reported with its own HEAD.
func TestCheckoutCarriersAreProbed(t *testing.T) {
	repo, c1, c2 := fixtureRepo(t)

	// A second checkout pinned at c1 — the shape of ~/.pogo/deploy-src on the
	// box when this was written: a real tree, executing repo scripts in place,
	// behind main, and named by no other surface.
	src := t.TempDir()
	git(t, src, "clone", "-q", repo, src)
	git(t, src, "checkout", "-q", c1)

	o := opts(t, repo)
	o.DeploySrc = src
	rep := Assess(c2, HostDeps(o))
	found := map[string]Verdict{}
	for _, f := range rep.Findings {
		for _, c := range f.Carriers {
			found[c.Carrier] = c.Verdict
		}
	}
	if found["this checkout"] != Live {
		t.Errorf("this checkout = %q, want %q (its HEAD is c2)", found["this checkout"], Live)
	}
	if found["deploy checkout"] != Inert {
		t.Errorf("deploy checkout = %q, want %q (pinned at c1)", found["deploy checkout"], Inert)
	}
	if rep.Overall != OverallHalfLive {
		t.Errorf("Overall = %q, want %q: one checkout carries c2 and the other does not", rep.Overall, OverallHalfLive)
	}
}

func TestAbsentDeployCheckoutIsNotACarrier(t *testing.T) {
	repo, _, c2 := fixtureRepo(t)
	rep := Assess(c2, HostDeps(opts(t, repo)))
	for _, f := range rep.Findings {
		for _, c := range f.Carriers {
			if c.Carrier == "deploy checkout" {
				t.Errorf("a `deploy checkout` row appeared for a directory that is not a checkout: %+v", c)
			}
		}
	}
}

// installedCmds reads cmd/ rather than carrying a list. A hardcoded roster
// omits a program added after it was written, and that omission renders as "no
// carrier" — a green answer produced by not looking.
func TestInstalledCmdsComeFromTheRepo(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(repo, "cmd", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := installedCmds(repo)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("installedCmds = %v, want [alpha beta] read off disk", got)
	}
}

// SelfDescription is what puts this command's own build on its own report. It
// must carry PROVENANCE, not just a revision: a binary built inside a linked
// worktree is stamped with the enclosing repository's HEAD, so a bare SHA on
// the line that tells a reader whether to trust the report is exactly the
// misattribution mg-8d0f enumerated. Under `go test` the binary may carry no
// stamp at all, so what is pinned here is the shape and the fallback.
func TestSelfDescriptionCarriesProvenance(t *testing.T) {
	desc := SelfDescription()
	if desc != "" && !strings.Contains(desc, "source=") {
		t.Errorf("SelfDescription() = %q, want the source= provenance internal/version puts on the human line", desc)
	}
	r := Report{Reporter: desc, Overall: OverallLive, Summary: "x"}
	out := r.Text()
	if !strings.Contains(out, "reported by ") {
		t.Errorf("the report does not name its own build:\n%s", out)
	}
	if strings.Contains(desc, "source=ldflags") == strings.Contains(out, "mg-8d0f") {
		t.Errorf("the provenance caveat must render exactly when it applies — reporter %q, output:\n%s", desc, out)
	}
}
