package staleness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// stamped prefixes the v1 install stamp InstallPrompts writes. It is a real
// stamp shape, not a lookalike: the whole point of stripping is that the
// installed copy carries this and no source copy does.
func stamped(body []byte) []byte {
	return append([]byte("<!-- pogo-prompt: embed=sha256:deadbeef body=sha256:deadbeef -->\n"), body...)
}

// TestComparePromptsPositiveControl is the pair the ticket demanded for this
// half: the same comparison, one corpus that has drifted and one that has not.
func TestComparePromptsPositiveControl(t *testing.T) {
	shipped := Corpus{
		"mayor.md":                    measure(lines(983, "a")),
		"templates/polecat.md":        measure(lines(285, "b")),
		"templates/polecat-qa.md":     measure(lines(249, "c")),
		"templates/polecat-review.md": measure(lines(247, "d")),
	}

	// QUIET — byte-identical bodies, and the installed side carries the install
	// stamp that no source copy has. Without the strip, this healthy corpus
	// would report all four as stale, which is a detector that fires on every
	// input and therefore on nothing.
	clean := Corpus{}
	for rel, f := range shipped {
		clean[rel] = f
	}
	deltas, unjudged := ComparePrompts(shipped, clean)
	if len(deltas) != 0 {
		t.Fatalf("fired on an identical corpus: %+v", deltas)
	}
	if len(unjudged) != 0 {
		t.Errorf("unjudged = %v, want empty", unjudged)
	}

	// RED — the 2026-08-04 shape.
	drifted := Corpus{
		"mayor.md":                    measure(lines(579, "a")),  // installed SHORTER
		"templates/polecat.md":        measure(lines(285, "b")),  // unchanged
		"templates/polecat-qa.md":     measure(lines(249, "XX")), // SAME LENGTH, different content
		"templates/polecat-review.md": measure(lines(248, "d")),  // installed LONGER
	}
	deltas, _ = ComparePrompts(shipped, drifted)
	if len(deltas) != 3 {
		t.Fatalf("got %d deltas, want 3: %+v", len(deltas), deltas)
	}
	byPath := map[string]PromptDelta{}
	for _, d := range deltas {
		byPath[d.Path] = d
	}
	if _, ok := byPath["templates/polecat.md"]; ok {
		t.Errorf("reported an unchanged file")
	}
	for _, p := range []string{"mayor.md", "templates/polecat-qa.md", "templates/polecat-review.md"} {
		if byPath[p].Kind != "differs" {
			t.Errorf("%s: Kind = %q, want differs", p, byPath[p].Kind)
		}
	}
}

// TestComparePromptsIsTwoSided. On 2026-08-04 polecat-build-pr.md was 231 lines
// installed against 230 on main — LONGER — while the other eight were shorter.
// A one-sided check ("is the installed file behind?") reported eight of nine and
// called the ninth fine.
func TestComparePromptsIsTwoSided(t *testing.T) {
	shipped := Corpus{"templates/polecat-build-pr.md": measure(lines(230, "x"))}
	installed := Corpus{"templates/polecat-build-pr.md": measure(lines(231, "x"))}
	deltas, _ := ComparePrompts(shipped, installed)
	if len(deltas) != 1 {
		t.Fatalf("a LONGER installed file was not reported: %+v", deltas)
	}
	if note := deltas[0].LineNote(); !strings.Contains(note, "LONGER") {
		t.Errorf("LineNote = %q, want it to name the direction", note)
	}
}

// TestComparePromptsIgnoresLength. Length is reported and never decided on. An
// edit that swaps one line for another is the ordinary shape of a prompt change
// and is invisible to any line-count test — which is why the hash is the
// predicate and the count is commentary.
func TestComparePromptsIgnoresLength(t *testing.T) {
	shipped := Corpus{"mayor.md": measure(lines(100, "old"))}
	installed := Corpus{"mayor.md": measure(lines(100, "new"))}
	deltas, _ := ComparePrompts(shipped, installed)
	if len(deltas) != 1 {
		t.Fatalf("same-length different-content went unreported: %+v", deltas)
	}
	if note := deltas[0].LineNote(); !strings.Contains(note, "same length") {
		t.Errorf("LineNote = %q, want it to say the lengths matched", note)
	}
}

// TestComparePromptsSeparatesMissingFromLocal. A shipped file with no installed
// copy is a finding; an installed file the ref does not ship is a census line.
// Folding the second into findings would put ~/.pogo/agents' legitimately local
// material (crew/pm-pogo.md, pm/anti-drift-protocol.md, the per-PM .toml) into
// every run and teach readers to skip the report.
func TestComparePromptsSeparatesMissingFromLocal(t *testing.T) {
	shipped := Corpus{"crew/doctor.md": measure(lines(239, "d"))}
	installed := Corpus{"crew/pm-pogo.md": measure(lines(10, "local"))}
	deltas, unjudged := ComparePrompts(shipped, installed)
	if len(deltas) != 1 || deltas[0].Kind != "not-installed" || deltas[0].Path != "crew/doctor.md" {
		t.Fatalf("deltas = %+v, want one not-installed for crew/doctor.md", deltas)
	}
	if len(unjudged) != 1 || unjudged[0] != "crew/pm-pogo.md" {
		t.Errorf("unjudged = %v, want [crew/pm-pogo.md]", unjudged)
	}
}

// TestLoadInstalledCorpusSkipsInstallerSidecars. `mayor.md.dist` and
// `mayor.md.bak-<ts>` are what the installer leaves behind when it DECLINES to
// clobber a local edit. They are the result of a drift being handled; counting
// them as corpus would report the handling as a defect.
func TestLoadInstalledCorpusSkipsInstallerSidecars(t *testing.T) {
	root := t.TempDir()
	body := lines(20, "body")
	writeFile(t, filepath.Join(root, "mayor.md"), stamped(body))
	writeFile(t, filepath.Join(root, "mayor.md.dist"), stamped(lines(99, "newer")))
	writeFile(t, filepath.Join(root, "mayor.md.bak-1784309533"), lines(5, "older"))
	writeFile(t, filepath.Join(root, "receipts", "abcd.submits"), []byte("noise\n"))

	shipped := Corpus{"mayor.md": measure(body)}
	installed, unreadable, err := LoadInstalledCorpus(root, LayoutOf(shipped))
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Errorf("unreadable = %v, want empty", unreadable)
	}
	if len(installed) != 1 {
		t.Fatalf("installed = %v, want only mayor.md", installed)
	}
	deltas, unjudged := ComparePrompts(shipped, installed)
	if len(deltas) != 0 {
		t.Errorf("the install stamp made an identical body look drifted: %+v", deltas)
	}
	if len(unjudged) != 0 {
		t.Errorf("unjudged = %v, want empty (sidecars are not corpus)", unjudged)
	}
}

// TestLoadInstalledCorpusMissingDirectory. A shipped directory with nothing
// under it reports its files one by one rather than failing whole, so the
// report says HOW MUCH is missing.
func TestLoadInstalledCorpusMissingDirectory(t *testing.T) {
	root := t.TempDir()
	shipped := Corpus{
		"templates/polecat.md":    measure(lines(3, "a")),
		"templates/polecat-qa.md": measure(lines(4, "b")),
	}
	installed, _, err := LoadInstalledCorpus(root, LayoutOf(shipped))
	if err != nil {
		t.Fatalf("an absent corpus directory should not be an error: %v", err)
	}
	deltas, _ := ComparePrompts(shipped, installed)
	if len(deltas) != 2 {
		t.Fatalf("got %d deltas, want 2 not-installed", len(deltas))
	}
}

// TestLoadInstalledCorpusEmacsLockFile is a regression from the FIRST live run
// of this witness. ~/.pogo/agents/templates holds `.#polecat.md`, an Emacs lock
// file: a dangling symlink to `daniel@Mac-mini.local.560:1767515359`, extension
// `.md`, target absent. It matched the corpus filter and could not be read, and
// the whole prompt witness aborted with "no such file or directory" — reporting
// nothing about the nine prompts it was there to judge.
//
// A detector that a stray editor file can silence has exactly the failure mode
// this ticket exists to remove. The lock file must be invisible AND the real
// prompt beside it must still be judged.
func TestLoadInstalledCorpusEmacsLockFile(t *testing.T) {
	root := t.TempDir()
	body := lines(12, "prompt")
	writeFile(t, filepath.Join(root, "templates", "polecat.md"), stamped(body))
	if err := os.Symlink("daniel@Mac-mini.local.560:1767515359", filepath.Join(root, "templates", ".#polecat.md")); err != nil {
		t.Fatal(err)
	}

	shipped := Corpus{"templates/polecat.md": measure(body)}
	installed, unreadable, err := LoadInstalledCorpus(root, LayoutOf(shipped))
	if err != nil {
		t.Fatalf("an editor lock file took the corpus load down: %v", err)
	}
	if len(unreadable) != 0 {
		t.Errorf("unreadable = %v; an editor lock file is not a fact about the corpus", unreadable)
	}
	deltas, unjudged := ComparePrompts(shipped, installed)
	if len(deltas) != 0 {
		t.Errorf("the real prompt beside the lock file was misjudged: %+v", deltas)
	}
	if len(unjudged) != 0 {
		t.Errorf("unjudged = %v, want empty", unjudged)
	}
}

// TestLoadInstalledCorpusUnreadableFileIsReportedNotRaised. An unreadable file
// that is NOT editor droppings is a real gap — its content is unknown, and
// unknown is not the same as matching — but it must not decide the other files.
func TestLoadInstalledCorpusUnreadableFileIsReportedNotRaised(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	root := t.TempDir()
	body := lines(7, "ok")
	writeFile(t, filepath.Join(root, "mayor.md"), stamped(body))
	blocked := filepath.Join(root, "crew", "doctor.md")
	writeFile(t, blocked, lines(3, "secret"))
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0644) })

	shipped := Corpus{"mayor.md": measure(body), "crew/doctor.md": measure(lines(3, "secret"))}
	installed, unreadable, err := LoadInstalledCorpus(root, LayoutOf(shipped))
	if err != nil {
		t.Fatalf("one unreadable file aborted the whole load: %v", err)
	}
	if len(unreadable) != 1 || unreadable[0] != "crew/doctor.md" {
		t.Fatalf("unreadable = %v, want [crew/doctor.md]", unreadable)
	}
	if _, ok := installed["mayor.md"]; !ok {
		t.Errorf("the readable file was dropped alongside the unreadable one")
	}
	rep := PromptReport{Unreadable: unreadable}
	if rep.Clean() {
		t.Errorf("a corpus with an unreadable prompt read as clean")
	}
}

// --- the git side -----------------------------------------------------------

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

// fixtureRepo builds a repo with a prompt corpus at the shipped subtree.
func fixtureRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	for rel, data := range files {
		writeFile(t, filepath.Join(repo, PromptsSubtree, filepath.FromSlash(rel)), data)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "corpus")
	return repo
}

// TestCheckPromptsAgainstGitRef is the end-to-end control, and the one that
// proves the comparison target is the REPO rather than this binary's embed:
// the fixture corpus has nothing to do with the prompts compiled into the test
// binary, and the verdict follows the fixture.
func TestCheckPromptsAgainstGitRef(t *testing.T) {
	mayor := lines(983, "mayor")
	qa := lines(249, "qa")
	repo := fixtureRepo(t, map[string][]byte{
		"mayor.md":                mayor,
		"templates/polecat-qa.md": qa,
	})

	// QUIET — installed matches the ref, stamps and all.
	fresh := t.TempDir()
	writeFile(t, filepath.Join(fresh, "mayor.md"), stamped(mayor))
	writeFile(t, filepath.Join(fresh, "templates", "polecat-qa.md"), stamped(qa))

	rep := CheckPrompts(context.Background(), repo, "main", fresh)
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if !rep.Clean() {
		t.Fatalf("fired on a corpus that matches the ref: %+v", rep.Deltas)
	}
	if rep.Shipped != 2 {
		t.Errorf("Shipped = %d, want 2", rep.Shipped)
	}
	if rep.Reference.Commit == "" || rep.Reference.CommitTime == "" {
		t.Errorf("reference not identified: %+v — a reader cannot judge a verdict without knowing what it was against", rep.Reference)
	}

	// RED — the installed tree is the 2026-08-04 shape: one file behind, one
	// longer than the ref.
	stale := t.TempDir()
	writeFile(t, filepath.Join(stale, "mayor.md"), stamped(lines(579, "mayor")))
	writeFile(t, filepath.Join(stale, "templates", "polecat-qa.md"), stamped(lines(250, "qa")))

	rep = CheckPrompts(context.Background(), repo, "main", stale)
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if rep.Clean() {
		t.Fatal("stayed quiet over a corpus that differs from the ref in both directions")
	}
	if len(rep.Deltas) != 2 {
		t.Fatalf("got %d deltas, want 2: %+v", len(rep.Deltas), rep.Deltas)
	}
}

// TestCheckPromptsUnreachableReferenceIsNotClean. A comparison that could not be
// made has not found the corpus healthy. This is the same rule as the deploy
// half's unreadable stamp, and it matters more here because the default
// reference is a local mirror whose fetch is one of the things that fails.
func TestCheckPromptsUnreachableReferenceIsNotClean(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(3, "m")})

	rep := CheckPrompts(context.Background(), repo, "origin/main", t.TempDir())
	if rep.Err == "" {
		t.Fatal("an unresolvable ref produced no error")
	}
	if rep.Clean() {
		t.Fatal("an unresolvable ref read as clean")
	}

	rep = CheckPrompts(context.Background(), filepath.Join(t.TempDir(), "nope"), "main", t.TempDir())
	if rep.Clean() {
		t.Fatal("a missing reference repo read as clean")
	}
}

// TestLoadShippedCorpusEmptySubtreeIsAnError. A ref with no corpus at the
// expected path would otherwise compare an empty shipped set against anything
// and report zero deltas — a green run over a comparison that did not happen.
func TestLoadShippedCorpusEmptySubtreeIsAnError(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "README.md"), []byte("no corpus here\n"))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "empty")

	if _, _, err := LoadShippedCorpus(context.Background(), repo, "main"); err == nil {
		t.Fatal("a ref shipping no prompts was accepted as a reference")
	}
}
