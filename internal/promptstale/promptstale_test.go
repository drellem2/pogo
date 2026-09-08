package promptstale

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/staleness"
)

// promptBodyHashOf is the hash the installer records for a body. It goes through
// agent.PromptBodyHash rather than a local sha256 so these fixtures are stamped
// by the same function the real installer stamps with — a lookalike would make
// the discrimination test below vacuous in exactly the way it is testing for.
func promptBodyHashOf(body []byte) string { return agent.PromptBodyHash(body) }

// promptBodyHashOfInstalled reads an installed file and hashes its body with the
// stamp stripped — the reading internal/promptedit takes.
func promptBodyHashOfInstalled(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return agent.PromptBodyHash(data)
}

func lines(n int, text string) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(text)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// stamped prefixes the v1 install stamp InstallPrompts writes, recording a body
// hash that MATCHES the body it is given. That is not decoration: it is what
// makes these fixtures the exact shape the hand-edit detector calls clean, which
// is the discrimination TestStaleButUnEditedIsInvisibleToTheStampCheck rests on.
func stamped(t *testing.T, body []byte) []byte {
	t.Helper()
	h := promptBodyHashOf(body)
	return append([]byte("<!-- pogo-prompt: embed=sha256:"+h+" body=sha256:"+h+" -->\n"), body...)
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	full := append([]string{"-C", repo,
		"-c", "user.email=test@example.com",
		"-c", "user.name=test",
		"-c", "commit.gpgsign=false",
	}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// fixtureRepo builds a reference repo carrying a prompt corpus at the shipped
// subtree, and returns its path.
func fixtureRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitCorpus(t, repo, files, "corpus")
	return repo
}

func commitCorpus(t *testing.T, repo string, files map[string][]byte, msg string) {
	t.Helper()
	for rel, data := range files {
		writeFile(t, filepath.Join(repo, staleness.PromptsSubtree, filepath.FromSlash(rel)), data)
	}
	// Staged by PATH rather than with `git add -A`: this fixture repo is a tree
	// the test itself also writes scratch into, and a broad stage in a tree
	// something else writes to is how an unintended file rides into a commit.
	git(t, repo, "add", "--", staleness.PromptsSubtree)
	git(t, repo, "commit", "-q", "-m", msg)
}

// installTree writes an installed corpus, stamping each file the way the
// installer does.
func installTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	for rel, data := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), stamped(t, data))
	}
	return root
}

// sweep runs one comparison against a fixture, with the remote query off — the
// fixtures have no remote, and CheckRemote's own behaviour there is
// internal/staleness's test, not this package's.
func sweep(t *testing.T, repo, root, coordinator string) Report {
	t.Helper()
	raw := staleness.CheckPrompts(context.Background(), staleness.PromptOptions{
		Repo: repo, Ref: "main", InstalledRoot: root, SkipRemote: true, Now: time.Now(),
	})
	return FromStaleness(raw, coordinator)
}

func findingFor(r Report, path string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Path == path {
			return f, true
		}
	}
	return Finding{}, false
}

// TestPositiveControl is the pair every detector in this family owes: the same
// instrument over a corpus that matches the ref and one that does not. Without
// the quiet half, a detector that fires on all input passes the loud half.
func TestPositiveControl(t *testing.T) {
	mayor := lines(1771, "mayor")
	doctor := lines(40, "doctor")
	repo := fixtureRepo(t, map[string][]byte{
		"mayor.md":       mayor,
		"crew/doctor.md": doctor,
	})

	// QUIET — installed matches the ref, stamps and all.
	current := installTree(t, map[string][]byte{"mayor.md": mayor, "crew/doctor.md": doctor})
	if rep := sweep(t, repo, current, "mayor"); len(rep.Findings) != 0 || rep.Err != "" {
		t.Fatalf("fired on a current corpus: err=%q findings=%+v", rep.Err, rep.Findings)
	}

	// LOUD — the 2026-09-08 shape: mayor.md installed at the pre-merge length.
	stale := installTree(t, map[string][]byte{"mayor.md": lines(1642, "mayor"), "crew/doctor.md": doctor})
	rep := sweep(t, repo, stale, "mayor")
	if rep.Err != "" {
		t.Fatalf("sweep: %s", rep.Err)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(rep.Findings), rep.Findings)
	}
	f := rep.Findings[0]
	if f.Path != "mayor.md" || f.Kind != KindDiffers {
		t.Errorf("finding = %+v, want mayor.md/%s", f, KindDiffers)
	}
	if !strings.Contains(f.LineNote(), "behind by 129") {
		t.Errorf("LineNote() = %q, want the 129-line gap named", f.LineNote())
	}
	if rep.Shipped != 2 {
		t.Errorf("Shipped = %d, want 2 — the denominator is what makes a zero readable", rep.Shipped)
	}
}

// TestStaleButUnEditedIsInvisibleToTheStampCheck is the reason this package
// exists rather than a flag on internal/promptedit, and it is the discrimination
// the whole ticket turns on.
//
// The installed mayor.md here is byte-identical to what an installer wrote and
// carries a stamp that MATCHES its body — so the hand-edit detector reports it
// clean, correctly. It is also 129 lines behind the ref. Two different facts
// about one file, and only one of them was previously reachable on a cadence.
func TestStaleButUnEditedIsInvisibleToTheStampCheck(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "mayor")})
	body := lines(1642, "mayor")
	root := installTree(t, map[string][]byte{"mayor.md": body})

	// The control: the file is UNEDITED by the stamp's own reckoning. This is
	// the exact reading internal/promptedit would take of it.
	raw, err := os.ReadFile(filepath.Join(root, "mayor.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := promptBodyHashOf(body); !strings.Contains(string(raw), "body=sha256:"+got) {
		t.Fatalf("fixture is not stamped consistently — the discrimination below would be vacuous")
	}
	if h := promptBodyHashOfInstalled(t, filepath.Join(root, "mayor.md")); h != promptBodyHashOf(body) {
		t.Fatalf("installed body hash %s != stamp %s; this fixture is hand-edited, not merely stale", h, promptBodyHashOf(body))
	}

	rep := sweep(t, repo, root, "mayor")
	if len(rep.Findings) != 1 {
		t.Fatalf("a stale-but-unedited prompt produced %d findings, want 1 — this is the case "+
			"nothing on a cadence could see: %+v", len(rep.Findings), rep.Findings)
	}
}

// TestRoutingUsesTheSharedAddressee checks that each finding reaches an agent
// that exists, through the same table promptsyncnotify and promptedit use. A
// name synthesized from a path would be a phantom mailbox: accepted, and read by
// nobody.
func TestRoutingUsesTheSharedAddressee(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{
		"mayor.md":             lines(10, "m"),
		"crew/doctor.md":       lines(10, "d"),
		"templates/polecat.md": lines(10, "p"),
		"pm/pm-template.md":    lines(10, "t"),
	})
	root := installTree(t, map[string][]byte{
		"mayor.md":             lines(9, "m"),
		"crew/doctor.md":       lines(9, "d"),
		"templates/polecat.md": lines(9, "p"),
		"pm/pm-template.md":    lines(9, "t"),
	})

	// A RENAMED coordinator, deliberately: hardcoding "mayor" anywhere in the
	// routing would pass with the default name and misroute on this consumer.
	rep := sweep(t, repo, root, "chief")

	want := map[string]struct {
		agent string
		owned bool
	}{
		"mayor.md":             {"chief", true},
		"crew/doctor.md":       {"doctor", true},
		"templates/polecat.md": {"chief", false},
		"pm/pm-template.md":    {"chief", false},
	}
	if len(rep.Findings) != len(want) {
		t.Fatalf("got %d findings, want %d: %+v", len(rep.Findings), len(want), rep.Findings)
	}
	for path, w := range want {
		f, ok := findingFor(rep, path)
		if !ok {
			t.Errorf("%s produced no finding", path)
			continue
		}
		if f.Agent != w.agent || f.Owned != w.owned {
			t.Errorf("%s addressed to %s (owned=%v), want %s (owned=%v)", path, f.Agent, f.Owned, w.agent, w.owned)
		}
	}

	// One mail per agent, not one per file.
	rcs := rep.Recipients()
	if len(rcs) != 2 {
		t.Fatalf("got %d recipients, want 2 (chief, doctor): %+v", len(rcs), rcs)
	}
	if rcs[0].Agent != "chief" || len(rcs[0].Findings) != 3 {
		t.Errorf("recipient 0 = %s with %d findings, want chief with 3", rcs[0].Agent, len(rcs[0].Findings))
	}
	if rcs[1].Agent != "doctor" || len(rcs[1].Findings) != 1 {
		t.Errorf("recipient 1 = %s with %d findings, want doctor with 1", rcs[1].Agent, len(rcs[1].Findings))
	}
	if !rcs[1].Owned() || !strings.Contains(rcs[1].Subject(), "YOUR prompt") {
		t.Errorf("subject for an owned single finding = %q, want it to say YOUR prompt", rcs[1].Subject())
	}
}

// TestNotInstalledIsItsOwnKind. A template the ref ships and the tree does not
// have is worse than one that differs — a spawn falls back to whatever the
// caller does without a template — and it must not be reported as "differs".
func TestNotInstalledIsItsOwnKind(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{
		"mayor.md":                lines(10, "m"),
		"templates/polecat-qa.md": lines(10, "q"),
	})
	root := installTree(t, map[string][]byte{"mayor.md": lines(10, "m")})

	rep := sweep(t, repo, root, "mayor")
	f, ok := findingFor(rep, "templates/polecat-qa.md")
	if !ok {
		t.Fatalf("a shipped template with no installed copy produced no finding: %+v", rep.Findings)
	}
	if f.Kind != KindNotInstalled {
		t.Errorf("Kind = %q, want %q", f.Kind, KindNotInstalled)
	}
	if !strings.Contains(f.LineNote(), "nothing installed") {
		t.Errorf("LineNote() = %q, want it to say nothing is installed", f.LineNote())
	}
}

// TestSameLengthDifferentContentIsStated. The decision is a hash; the line count
// is orientation. The case that proves they are not the same instrument is a
// prompt edited to the same length, and the report has to say so in words or a
// reader will conclude the two files are the same.
func TestSameLengthDifferentContentIsStated(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(20, "before")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(20, "after")})

	rep := sweep(t, repo, root, "mayor")
	if len(rep.Findings) != 1 {
		t.Fatalf("a same-length rewrite produced %d findings, want 1 — a length test would "+
			"have missed this entirely: %+v", len(rep.Findings), rep.Findings)
	}
	if note := rep.Findings[0].LineNote(); !strings.Contains(note, "same length") {
		t.Errorf("LineNote() = %q, want it to name the same-length case explicitly", note)
	}
}

// TestUnjudgedFilesAreCensusNotFindings. ~/.pogo/agents holds a lot of
// legitimately local material — crew/pm-*.md, pm/anti-drift-protocol.md — whose
// deployed copy IS the source. Reporting those as stale would be a wall of false
// positives that trains the recipient to filter the one real finding.
func TestUnjudgedFilesAreCensusNotFindings(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"crew/doctor.md": lines(10, "d")})
	root := installTree(t, map[string][]byte{
		"crew/doctor.md":  lines(10, "d"),
		"crew/pa.md":      lines(30, "local"),
		"crew/pm-pogo.md": lines(30, "local"),
	})

	rep := sweep(t, repo, root, "mayor")
	if len(rep.Findings) != 0 {
		t.Fatalf("locally-added prompts were reported as findings: %+v", rep.Findings)
	}
	if len(rep.Unjudged) != 2 {
		t.Errorf("Unjudged = %v, want the two locally-added files — a census a reader cannot "+
			"see is indistinguishable from a scan that missed them", rep.Unjudged)
	}
}

// TestFingerprintMovesWithEitherSide. The suppression key has to change when the
// recipient's job changes, and their job changes if EITHER side moves: the repo
// shipping more, or the installed copy being partially updated under a fixed ref.
func TestFingerprintMovesWithEitherSide(t *testing.T) {
	base := Finding{Path: "mayor.md", Kind: KindDiffers, ShippedHash: "aaa", InstalledHash: "bbb"}
	shippedMoved := base
	shippedMoved.ShippedHash = "ccc"
	installedMoved := base
	installedMoved.InstalledHash = "ddd"

	if base.Fingerprint() == shippedMoved.Fingerprint() {
		t.Error("fingerprint did not move when the REF advanced — a further merge would be suppressed")
	}
	if base.Fingerprint() == installedMoved.Fingerprint() {
		t.Error("fingerprint did not move when the INSTALLED copy changed — a partial install would be suppressed")
	}
	same := base
	if base.Fingerprint() != same.Fingerprint() {
		t.Error("fingerprint is not stable over an unchanged finding — every sweep would re-notify")
	}
}

// TestBodyNamesItsReferenceAndItsLimits. "Your prompt is stale" with no named
// reference is a number nobody can chase, which is this ticket's own subject one
// level up. The body must carry what it compared with, how old that is, and the
// two things a recipient must NOT conclude.
func TestBodyNamesItsReferenceAndItsLimits(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	rep := sweep(t, repo, root, "mayor")
	if len(rep.Recipients()) != 1 {
		t.Fatalf("want exactly one recipient: %+v", rep.Recipients())
	}
	body := rep.Recipients()[0].Body(rep)

	for _, want := range []string{
		repo,                      // the reference repo, by path
		rep.Reference.Commit[:12], // the resolved commit
		"pogo check-staleness",    // reproducible
		"DO NOT HAND-EDIT",        // the constraint mg-385f asked any fix to carry
		"pogo agent prompt install",
		"behind by 129",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("notice body does not carry %q:\n%s", want, body)
		}
	}
	// The verdict must never read as "compared against this daemon's own copy",
	// which is the comparison that passes truthfully while the fleet drifts.
	if !strings.Contains(body, "Not this daemon's own embedded copy") {
		t.Errorf("notice body does not disclaim the embed comparison:\n%s", body)
	}
}

// TestUnfetchedReferenceQualifiesTheVerdict. A reference that has not seen what
// shipped yields a weaker claim, and the notice has to say which claim it is
// making. The alternative — printing "your prompt is current" from a frozen
// mirror — is this ticket's defect reproduced inside its own fix.
func TestUnfetchedReferenceQualifiesTheVerdict(t *testing.T) {
	r := Report{
		Root:      "/root",
		Reference: staleness.Reference{Repo: "/ref", Ref: "origin/main", Commit: strings.Repeat("a", 40)},
		Remote:    staleness.RemoteState{Armed: true, Behind: true, Counted: false},
		Findings:  []Finding{{Path: "mayor.md", Kind: KindDiffers, Agent: "mayor", Owned: true}},
	}
	if !r.ReferenceQualified() {
		t.Error("a reference behind the remote with an UNKNOWN composition read as unqualified — " +
			"unknown is not clean")
	}
	body := r.Recipients()[0].Body(r)
	if !strings.Contains(body, "what was DEPLOYED") || !strings.Contains(body, "--fetch") {
		t.Errorf("an unfetched reference was not qualified in the body:\n%s", body)
	}

	// A SKIPPED query is not an ABSENT remote. Both leave RemoteState unarmed,
	// and a reader told the wrong one goes looking for a misconfigured checkout
	// that is fine.
	skipped := r
	skipped.Remote = staleness.RemoteState{}
	skipped.RemoteSkipped = true
	if body := skipped.Recipients()[0].Body(skipped); !strings.Contains(body, "NOT QUERIED") {
		t.Errorf("a deliberately skipped remote query was reported as a missing remote:\n%s", body)
	}
	unarmed := r
	unarmed.Remote = staleness.RemoteState{}
	if body := unarmed.Recipients()[0].Body(unarmed); !strings.Contains(body, "NOT COMPARED") {
		t.Errorf("a repo with no remote head was reported as a skipped query:\n%s", body)
	}

	// And the opposite: a reference level with the remote makes the STRONGER
	// claim, and must say so rather than hedging identically either way.
	r.Remote = staleness.RemoteState{Armed: true, Behind: false}
	if r.ReferenceQualified() {
		t.Error("a reference level with the remote read as qualified")
	}
	if body := r.Recipients()[0].Body(r); !strings.Contains(body, "about\n               what SHIPPED") {
		t.Errorf("a current reference did not make the stronger claim:\n%s", body)
	}
}

// TestUnknownFetchAgeIsPrintedAsUnknown. A zero would read as "just fetched",
// which is exactly the reading that makes a frozen mirror look current.
func TestUnknownFetchAgeIsPrintedAsUnknown(t *testing.T) {
	r := Report{
		Reference: staleness.Reference{Repo: "/ref", Ref: "origin/main", Commit: strings.Repeat("b", 40),
			Fetch: staleness.FetchState{Why: "no FETCH_HEAD — this repo has never fetched"}},
		Findings: []Finding{{Path: "mayor.md", Kind: KindDiffers, Agent: "mayor", Owned: true}},
	}
	body := r.Recipients()[0].Body(r)
	if !strings.Contains(body, "fetched:   UNKNOWN") || !strings.Contains(body, "never fetched") {
		t.Errorf("an undatable fetch was not reported as unknown:\n%s", body)
	}
	if strings.Contains(body, "(0s ago)") {
		t.Errorf("an unknown fetch age was rendered as zero, which reads as just-fetched:\n%s", body)
	}
}
