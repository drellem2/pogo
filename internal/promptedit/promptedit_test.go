package promptedit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/drellem2/pogo/internal/agent"
)

// stamped renders a file the way InstallPrompts would have: a v1 stamp
// recording the hash of the body that follows it. Built through agent's own
// hash so a change to the writer's encoding breaks these tests rather than
// silently making the detector wrong.
func stamped(body string) string {
	h := hashOf(body)
	return "<!-- pogo-prompt: embed=sha256:" + h + " body=sha256:" + h + " -->\n" + body
}

// stampedV0 renders the legacy one-hash shape.
func stampedV0(body string) string {
	return "<!-- pogo-prompt-hash: " + hashOf(body) + " -->\n" + body
}

// stampedRecording renders a stamp claiming a body OTHER than the one written —
// the shape a file takes after somebody edits it in place.
func stampedRecording(recorded, body string) string {
	h := hashOf(recorded)
	return "<!-- pogo-prompt: embed=sha256:" + h + " body=sha256:" + h + " -->\n" + body
}

func hashOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// shippedSet is the domain used by most tests: the same SHAPE as the real
// corpus (a top-level file, a crew file, a templates file) without depending on
// its contents.
func shippedSet() Shipped {
	return Shipped{
		"mayor.md":             true,
		"crew/doctor.md":       true,
		"templates/polecat.md": true,
		"pm/pm-template.md":    true,
	}
}

// TestScan_FourWayClassificationPartitionsTheCorpus is the domain constraint,
// which is the part of this detector that decides whether its report is worth
// reading at all. Every enumerated file must land in exactly one bucket, and
// the three non-reading buckets must stay DISTINCT — pooling them is what turns
// a lost stamp into a hidden one and twenty by-design locals into twenty false
// positives.
func TestScan_FourWayClassificationPartitionsTheCorpus(t *testing.T) {
	root := t.TempDir()
	// shipped + stamped + matching → clean
	write(t, root, "mayor.md", stamped("# mayor\n"))
	// shipped + stamped + edited → FINDING
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nlocal edit\n"))
	// shipped + unstamped → stamp-missing
	write(t, root, "templates/polecat.md", "# polecat\n")
	// unshipped + stamped + edited → upstream-withdrawn
	write(t, root, "crew/pm-pogo.md", stampedRecording("# pm\n", "# pm\nmine\n"))
	// unshipped + unstamped → no-upstream
	write(t, root, "crew/architect.md", "# architect\n")

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if rep.Total() != 5 {
		t.Fatalf("enumerated %d files, want 5 — the buckets must PARTITION the corpus, "+
			"a file that falls out of all four is a file reported as neither judged nor excused:\n%s",
			rep.Total(), rep.Render())
	}
	if got := len(rep.Findings); got != 1 || rep.Findings[0].Path != "crew/doctor.md" {
		t.Fatalf("findings = %+v, want exactly crew/doctor.md", rep.Findings)
	}
	if got := strings.Join(rep.Clean, ","); got != "mayor.md" {
		t.Fatalf("clean = %q, want mayor.md", got)
	}

	byReason := map[OutOfDomainReason]string{}
	for _, o := range rep.OutOfDomain {
		byReason[o.Reason] = o.Path
	}
	for reason, want := range map[OutOfDomainReason]string{
		ReasonStampMissing:      "templates/polecat.md",
		ReasonUpstreamWithdrawn: "crew/pm-pogo.md",
		ReasonNoUpstream:        "crew/architect.md",
	} {
		if byReason[reason] != want {
			t.Errorf("out-of-domain %s = %q, want %q", reason, byReason[reason], want)
		}
	}
}

// TestScan_WithdrawnUpstreamIsNotAFinding pins the case that a naive
// "stamped means judgeable" sweep gets wrong, and it is not hypothetical:
// crew/pm-onethird.md and crew/pm-pogo.md were shipped by mg-6805, deleted from
// the corpus by mg-5d9e, and both still carry a v0 stamp that disagrees with
// their body. Reporting them would be two findings against two PMs who did
// nothing wrong and have no upstream to reconcile against.
func TestScan_WithdrawnUpstreamIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crew/pm-pogo.md", stampedRecording("# shipped once\n", "# edited since\n"))

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("a withdrawn upstream must not be a finding, got %+v", rep.Findings)
	}
	withdrawn := rep.OutOfDomainBy(ReasonUpstreamWithdrawn)
	if len(withdrawn) != 1 {
		t.Fatalf("want 1 upstream-withdrawn entry, got %d", len(withdrawn))
	}
	// It is excluded for lack of an upstream, NOT by pretending it is unchanged.
	// The stamp reading must survive the exclusion, or the census becomes a
	// wave-through with extra words.
	if !withdrawn[0].Edited() {
		t.Error("the entry must still report that its stamp disagrees with its body — " +
			"declining to raise it is a judgement about actionability, not a claim it is clean")
	}
	if !strings.Contains(rep.Render(), "stamp says EDITED") {
		t.Errorf("the rendered census must say what the stamp says:\n%s", rep.Render())
	}
}

// TestScan_StampMissingIsItsOwnBucket is the other half of the ambiguity. A
// file the corpus SHIPS carries a stamp by construction, so its absence is the
// one unstamped case worth attention — and it must not be pooled with the
// legitimately unstamped locals, where it would be invisible.
func TestScan_StampMissingIsItsOwnBucket(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", "# mayor, stamp stripped\n")
	for _, rel := range []string{"crew/architect.md", "crew/pa.md", "crew/representative.md"} {
		write(t, root, rel, "# local, no upstream\n")
	}

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	missing := rep.OutOfDomainBy(ReasonStampMissing)
	if len(missing) != 1 || missing[0].Path != "mayor.md" {
		t.Fatalf("stamp-missing = %+v, want exactly mayor.md", missing)
	}
	if got := len(rep.OutOfDomainBy(ReasonNoUpstream)); got != 3 {
		t.Fatalf("no-upstream = %d, want 3", got)
	}
	out := rep.Render()
	if !strings.Contains(out, "stamp-missing — 1 file(s)") || !strings.Contains(out, "no-upstream — 3 file(s)") {
		t.Errorf("each bucket must be counted separately in the render:\n%s", out)
	}
}

// TestScan_V0StampIsAReadingAndSaysSo. The legacy shape records one hash, and
// the body hash is INFERRED from it (the installer writes body == embed). That
// is sound, so a v0 file is judged — but it is a different kind of evidence and
// the report says which it is looking at.
func TestScan_V0StampIsAReadingAndSaysSo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stampedV0("# mayor\n"))
	write(t, root, "crew/doctor.md", "<!-- pogo-prompt-hash: "+hashOf("# doctor\n")+" -->\n# doctor edited\n")

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Clean) != 1 || rep.Clean[0] != "mayor.md" {
		t.Fatalf("clean = %v, want [mayor.md]", rep.Clean)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %+v, want the edited v0 file", rep.Findings)
	}
	if rep.Findings[0].StampVersion != agent.PromptStampV0 {
		t.Errorf("StampVersion = %d, want v0", rep.Findings[0].StampVersion)
	}
	if !strings.Contains(rep.Render(), "v0 stamp (body hash inferred") {
		t.Errorf("the render must name the kind of evidence a v0 stamp is:\n%s", rep.Render())
	}
}

// TestScan_SkipsSidecarsDotfilesAndSubdirectories. Each of these rules exists
// because the thing it excludes is REALLY THERE under ~/.pogo/agents: the
// installer's own .dist and .bak sidecars (the RESULT of a conflict being
// handled — counting them as corpus would report the handling as a defect), an
// Emacs lock file that is a dangling symlink with an .md extension, and the
// per-agent state directories that hold 100+ .md files nothing installed.
func TestScan_SkipsSidecarsDotfilesAndSubdirectories(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stamped("# mayor\n"))
	write(t, root, "mayor.md.dist", stamped("# shipped update\n"))
	write(t, root, "mayor.md.bak-1784309533", "# old\n")
	write(t, root, "crew/.#doctor.md", "# emacs lock\n")
	write(t, root, "mayor/synthesized-prompt.md", "# per-agent state\n")
	write(t, root, "pa/thread-index/threads/calendar-switch.md", "# not a prompt\n")

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Total() != 1 {
		t.Fatalf("enumerated %d files, want only mayor.md:\n%s", rep.Total(), rep.Render())
	}
}

// TestScan_UnreadableIsNotClean. One bad file must not decide the others, and
// it must not be counted as matching either: an unreadable prompt is a prompt
// whose content is unknown, and unknown is not the same as clean.
func TestScan_UnreadableIsNotClean(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stamped("# mayor\n"))
	write(t, root, "crew/doctor.md", stamped("# doctor\n"))
	if err := os.Chmod(filepath.Join(root, "crew", "doctor.md"), 0000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "crew", "doctor.md"), 0644) })
	if _, err := os.ReadFile(filepath.Join(root, "crew", "doctor.md")); err == nil {
		t.Skip("running as a user that can read 0000 files")
	}

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Unreadable) != 1 || rep.Unreadable[0] != "crew/doctor.md" {
		t.Fatalf("unreadable = %v, want [crew/doctor.md]", rep.Unreadable)
	}
	for _, c := range rep.Clean {
		if c == "crew/doctor.md" {
			t.Fatal("an unreadable file was counted as clean")
		}
	}
	if !strings.Contains(rep.Render(), "COULD NOT BE READ") {
		t.Errorf("the render must say the file's state is unknown:\n%s", rep.Render())
	}
}

// TestScan_MissingShippedDirectoryIsNotAnError. A shipped directory with
// nothing installed under it means there is no installed file to have been
// edited. This detector answers one question and must not start answering
// "is the install complete" — internal/staleness already does that, and doing
// both here would mean a fresh machine reads as a wall of findings.
func TestScan_MissingShippedDirectoryIsNotAnError(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stamped("# mayor\n"))

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("a missing shipped directory must not fail the sweep: %v", err)
	}
	if rep.Total() != 1 || len(rep.Findings) != 0 {
		t.Fatalf("want 1 enumerated, 0 findings, got %d/%d", rep.Total(), len(rep.Findings))
	}
}

// TestLoadShipped_EmptyEmbedIsRefused. A binary whose embed carries nothing can
// judge nothing, and an empty domain would classify EVERY installed file as
// no-upstream and render as a clean sweep. Refusing is the only answer that
// does not manufacture a false all-clear.
func TestLoadShipped_EmptyEmbedIsRefused(t *testing.T) {
	if _, err := LoadShipped(fstest.MapFS{}); err == nil {
		t.Fatal("an empty shipped corpus must be refused, not treated as a domain of zero")
	}
}

// TestLoadShipped_AgainstTheRealEmbed is the control that keeps these tests
// honest about the artifact they are meant to describe. Every other test builds
// its own domain; if the real embed ever stopped being enumerable, none of them
// would notice.
func TestLoadShipped_AgainstTheRealEmbed(t *testing.T) {
	shipped, err := LoadShipped(agent.DefaultPromptsFS())
	if err != nil {
		t.Fatalf("LoadShipped over the real embed: %v", err)
	}
	for _, want := range []string{"mayor.md", "crew/doctor.md", "templates/polecat.md", "pm/pm-template.md"} {
		if !shipped[want] {
			t.Errorf("the shipped corpus does not carry %s — the domain these tests model has moved", want)
		}
	}
	layout := LayoutOf(shipped)
	if !layout.Extensions[".md"] {
		t.Error("the derived layout does not cover .md")
	}
}

// TestScan_AddresseeNamesTheAgentThatCanAct. Every finding has to name someone,
// because the whole design refuses to make the judgement itself. A file no
// running agent owns falls back to the coordinator rather than to a name
// synthesized from a path — mail to a name nobody reads is silently accepted
// into a phantom mailbox and lost.
func TestScan_AddresseeNamesTheAgentThatCanAct(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stampedRecording("a", "b"))
	write(t, root, "crew/doctor.md", stampedRecording("a", "b"))
	write(t, root, "templates/polecat.md", stampedRecording("a", "b"))

	rep, err := Scan(root, shippedSet(), "ringmaster")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := map[string]struct {
		agent string
		owned bool
	}{
		"mayor.md":             {"ringmaster", true},
		"crew/doctor.md":       {"doctor", true},
		"templates/polecat.md": {"ringmaster", false},
	}
	for _, f := range rep.Findings {
		w, ok := want[f.Path]
		if !ok {
			t.Errorf("unexpected finding %s", f.Path)
			continue
		}
		if f.Agent != w.agent || f.Owned != w.owned {
			t.Errorf("%s addressed to %q owned=%v, want %q owned=%v", f.Path, f.Agent, f.Owned, w.agent, w.owned)
		}
	}
}

// TestReport_RecipientsGroupsPerAgent. One mail per agent, not one per file,
// and stable across runs — the fingerprint suppression in the watcher is only
// meaningful if identical input produces identical output.
func TestReport_RecipientsGroupsPerAgent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mayor.md", stampedRecording("a", "b"))
	write(t, root, "templates/polecat.md", stampedRecording("a", "b"))
	write(t, root, "pm/pm-template.md", stampedRecording("a", "b"))
	write(t, root, "crew/doctor.md", stampedRecording("a", "b"))

	rep, err := Scan(root, shippedSet(), "mayor")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rcs := rep.Recipients()
	if len(rcs) != 2 {
		t.Fatalf("want 2 recipients (doctor, mayor), got %d: %+v", len(rcs), rcs)
	}
	if rcs[0].Agent != "doctor" || len(rcs[0].Findings) != 1 {
		t.Errorf("first recipient = %+v, want doctor with 1 finding", rcs[0])
	}
	if rcs[1].Agent != "mayor" || len(rcs[1].Findings) != 3 {
		t.Errorf("second recipient = %+v, want mayor with 3 findings", rcs[1])
	}
	body := rcs[1].Body(root, rep)
	if !strings.Contains(body, "tail -n +2") || !strings.Contains(body, "head -1") {
		t.Error("the notice must hand over the two commands that reproduce the reading")
	}
	if strings.Contains(body, "cp ") || strings.Contains(body, "--force") && !strings.Contains(body, ".bak sidecar") {
		t.Error("the notice must not read as a paste-ready repair — the repair is the thing this refuses to do")
	}
}

// TestPromptStampRoundTrip pins the reader against the WRITER, in the agent
// package, rather than against a literal copied into this file. The two live in
// different packages and a drift between them would read as a fleet-wide wall
// of findings.
func TestPromptStampRoundTrip(t *testing.T) {
	body := "# a prompt\nwith two lines\n"
	data := []byte(stamped(body))

	s, ok := agent.ReadPromptStamp(data)
	if !ok {
		t.Fatal("a stamped file must read as stamped")
	}
	if s.Version != agent.PromptStampV1 {
		t.Errorf("Version = %d, want v1", s.Version)
	}
	if got := agent.PromptBodyHash(data); got != s.BodyHash {
		t.Errorf("PromptBodyHash = %s, stamp records %s — the reader and the writer disagree", got, s.BodyHash)
	}
	if _, ok := agent.ReadPromptStamp([]byte(body)); ok {
		t.Error("an unstamped file must report ok=false, not an empty hash — " +
			"collapsing those two is what makes a lost stamp invisible")
	}
}

// shippedFixtureFS is the reference corpus the watcher tests enumerate: the
// same SHAPE as the real embed (a top-level file, crew/, templates/, pm/)
// without depending on its contents, so a prompt-text change does not break
// tests about the detector.
func shippedFixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"mayor.md":             &fstest.MapFile{Data: []byte("# mayor\n")},
		"crew/doctor.md":       &fstest.MapFile{Data: []byte("# doctor\n")},
		"templates/polecat.md": &fstest.MapFile{Data: []byte("# polecat\n")},
		"pm/pm-template.md":    &fstest.MapFile{Data: []byte("# pm\n")},
	}
}
