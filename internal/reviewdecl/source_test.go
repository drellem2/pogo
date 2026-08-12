package reviewdecl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/workitem"
)

// writeStoreItem lays a work item down on disk in the shape mg writes it: frontmatter
// between `---` fences, then the `# title` heading, then the carrier block.
func writeStoreItem(t *testing.T, root, status, id, frontmatter, body string) string {
	t.Helper()
	dir := filepath.Join(root, status)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := "---\nid: " + id + "\n" + frontmatter + "---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const filedAfter = "created: 2026-08-12T06:00:00Z\n"

// TestSourceReadsCarriersOffDisk is the end-to-end path: real files, the real
// parser, the real detector.
func TestSourceReadsCarriersOffDisk(t *testing.T) {
	root := t.TempDir()
	writeStoreItem(t, root, "available", "mg-0001", filedAfter,
		"# review the PR from mg-aaf6\nworkflow: gh-issue\nstage: review\ngh: drellem2/pogo#131\n\nBody.\n")
	writeStoreItem(t, root, "claimed", "mg-0002", filedAfter,
		"# review the other PR\nworkflow: gh-issue\nstage: review\nreviews: mg-b0b0\n\nBody.\n")
	writeStoreItem(t, root, "done", "mg-0003", filedAfter,
		"# build something\nworkflow: gh-issue\nstage: build\n\nBody.\n")

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	rep := Detect(items, ConventionLandedAt)
	if rep.Population != 3 {
		t.Fatalf("Population = %d, want 3", rep.Population)
	}
	if rep.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2 review tickets", rep.Scanned)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Item.ID != "mg-0001" {
		t.Fatalf("Missing = %+v, want mg-0001", rep.Missing)
	}
	if len(rep.Declared) != 1 || rep.Declared[0].Detail != "mg-b0b0" {
		t.Fatalf("Declared = %+v, want mg-0002 -> mg-b0b0", rep.Declared)
	}
	if rep.Missing[0].Item.Created.IsZero() {
		t.Error("the `created:` stamp did not survive the read, so the boundary would be unappliable")
	}
}

// TestSourceSeesClaimedItems. A claim renames the file to `<id>.md.<pid>`, which
// ListFrom's ".md" suffix filter drops. claimed/ is where a review ticket sits
// while its polecat is RUNNING — the only moment the exemption could matter — so
// a source that used ListFrom would be blind precisely when it counts.
func TestSourceSeesClaimedItems(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claimed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: mg-0001\n" + filedAfter + "---\n\n# review the PR\nworkflow: gh-issue\nstage: review\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "mg-0001.md.44821"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	rep := Detect(items, ConventionLandedAt)
	if len(rep.Missing) != 1 {
		t.Fatalf("Missing = %+v, want the CLAIMED review ticket — a claim renames the file and "+
			"ListFrom's `.md` filter would have dropped it", rep.Missing)
	}
}

// TestSourceAgreesWithTheEnforcer is the load-bearing test of this package.
//
// The done-reaper resolves a declaration through client.MGWorkItemReviews, which
// is workitem.ParseCarrier over `mg show --json`'s body. If this detector read
// bodies any other way — a grep, a body-wide key search — the two would disagree
// about exactly the bodies that matter, and the detector would report on a
// convention nobody enforces.
//
// The case that separates them is mg-27d4's: a `reviews:` line that is present,
// renders perfectly under `mg show`, and sits below a lead-in line so the
// leading-block scan never reaches it. A grep calls it declared. The enforcer
// calls it absent. This asserts they agree — and that BOTH say unprotected.
func TestSourceAgreesWithTheEnforcer(t *testing.T) {
	body := "# review the PR from mg-aaf6\n" +
		"Triage this:\n" + // one line of prose above the block is all it takes
		"workflow: gh-issue\n" +
		"stage: review\n" +
		"reviews: mg-b0b0\n" +
		"\nBody.\n"

	// What the ENFORCER sees, through the same call client.MGWorkItemReviews makes.
	enforcer := workitem.ParseCarrier(body)
	if enforcer.Reviews != "" {
		t.Fatalf("precondition: ParseCarrier read a declaration (%q) from a block it should not reach — "+
			"this test's premise is gone", enforcer.Reviews)
	}
	if !enforcer.Unreadable {
		t.Fatal("precondition: ParseCarrier did not report the block as unreachable (mg-27d4)")
	}

	// What the DETECTOR sees, off disk.
	root := t.TempDir()
	writeStoreItem(t, root, "available", "mg-0001", filedAfter, body)
	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("read %d items, want 1", len(items))
	}
	if items[0].Reviews != enforcer.Reviews || items[0].CarrierUnreadable != enforcer.Unreadable {
		t.Fatalf("the detector and the enforcer disagree about the same body:\n"+
			"  detector: reviews=%q unreadable=%v\n  enforcer: reviews=%q unreadable=%v",
			items[0].Reviews, items[0].CarrierUnreadable, enforcer.Reviews, enforcer.Unreadable)
	}

	// And the report must not call it declared — the guard cannot read it.
	rep := Detect(items, ConventionLandedAt)
	if len(rep.Declared) != 0 {
		t.Errorf("a declaration the done-reaper cannot read was reported as declared: %+v", rep.Declared)
	}
	if len(rep.Opaque) != 1 {
		t.Errorf("Opaque = %+v, want the item reported as not classifiable", rep.Opaque)
	}
}

// TestSourceDoesNotTreatProseAsADeclaration is the same identity from the other
// side. Bodies DISCUSS the convention — mg-253e's own body quotes `reviews:`
// several times — and a body-wide search would read the discussion as a
// declaration and report a real omission as clean.
func TestSourceDoesNotTreatProseAsADeclaration(t *testing.T) {
	root := t.TempDir()
	writeStoreItem(t, root, "available", "mg-0001", filedAfter,
		"# review the PR from mg-aaf6\n"+
			"workflow: gh-issue\n"+
			"stage: review\n"+
			"\n"+
			"The coordinator was supposed to write `reviews: mg-b0b0` on this ticket and did not.\n"+
			"reviews: mg-b0b0\n")

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	rep := Detect(items, ConventionLandedAt)
	if len(rep.Declared) != 0 {
		t.Fatalf("prose below the carrier block was read as a declaration: %+v", rep.Declared)
	}
	if len(rep.Missing) != 1 {
		t.Fatalf("Missing = %+v, want the ticket reported — the done-reaper reads no declaration here either", rep.Missing)
	}
}

// TestSourceReportsAnUnreadableStoreAsAnError. Zero items and an unreadable
// store both render as "nothing to report", and conflating them would let this
// detector go quietly blind — the exact failure mode it exists to catch,
// reproduced inside itself.
func TestSourceReportsAnUnreadableStoreAsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "available")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("cannot make a directory unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := (Source{Root: root}).Items(); err == nil {
		t.Error("an unreadable store returned no error — it would render as a clean scan")
	}
}

// TestSourceStatusesAreTheOnesItActuallyWalks. The report prints these as its
// coverage, so a drift between the label and the scan would make the report lie
// about what it saw — which is the defect, not a cosmetic mismatch.
func TestSourceStatusesAreTheOnesItActuallyWalks(t *testing.T) {
	root := t.TempDir()
	for _, s := range []string{"available", "claimed", "done", "pending", "archive", "shelved"} {
		writeStoreItem(t, root, s, "mg-000"+s[:1], filedAfter,
			"# review x\nworkflow: gh-issue\nstage: review\n\nBody.\n")
	}
	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if got, want := len(items), len((Source{}).Statuses()); got != want {
		t.Errorf("read %d items from 6 status directories, want %d — one per DECLARED status. "+
			"Statuses() and the walk must not drift, or the report states coverage it does not have", got, want)
	}
}

// TestSourceStatusesIsACopy. The slice is handed to a Report and rendered; a
// caller mutating it must not reach back into the package's own list.
func TestSourceStatusesIsACopy(t *testing.T) {
	s := (Source{}).Statuses()
	s[0] = "clobbered"
	if (Source{}).Statuses()[0] == "clobbered" {
		t.Error("Statuses() hands out the package's own slice")
	}
}

// TestMalformedCreatedStampLeavesItUndatable. workitem leaves Created zero when
// the stamp does not parse, and this detector must route that to UNDATABLE
// rather than let a zero time fall silently to the pre-convention side of the
// boundary — where it would hide the finding.
func TestMalformedCreatedStampLeavesItUndatable(t *testing.T) {
	root := t.TempDir()
	writeStoreItem(t, root, "available", "mg-0001", "created: last Tuesday\n",
		"# review the PR\nworkflow: gh-issue\nstage: review\n\nBody.\n")

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if !items[0].Created.IsZero() {
		t.Fatalf("Created = %v, want zero for an unparseable stamp", items[0].Created)
	}
	rep := Detect(items, ConventionLandedAt)
	if len(rep.PreConvention) != 0 {
		t.Fatalf("an unstamped ticket was excused as pre-convention: %+v", rep.PreConvention)
	}
	if len(rep.Undatable) != 1 {
		t.Fatalf("Undatable = %+v, want the unstamped ticket", rep.Undatable)
	}
}

// TestSourceNeverWrites is the report-only guarantee, checked rather than
// asserted in a comment: a full scan must leave every byte and every mtime of
// the store as it found it.
func TestSourceNeverWrites(t *testing.T) {
	root := t.TempDir()
	path := writeStoreItem(t, root, "available", "mg-0001", filedAfter,
		"# review the PR\nworkflow: gh-issue\nstage: review\n\nBody.\n")

	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatal(err)
	}
	if rep := Detect(items, ConventionLandedAt); len(rep.Missing) != 1 {
		t.Fatalf("precondition: the scan found nothing to repair, so this proves nothing: %+v", rep)
	}

	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !statAfter.ModTime().Equal(statBefore.ModTime()) {
		t.Errorf("the scan touched the item file: mtime %v -> %v", statBefore.ModTime(), statAfter.ModTime())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Error("the scan REWROTE the item it reported on — a detector that repairs the thing it " +
			"measures cannot be trusted to measure it (mg-253e)")
	}
	if strings.Contains(string(got), "reviews:") {
		t.Error("the scan wrote a `reviews:` line")
	}
	// And nothing new appeared beside it.
	entries, err := os.ReadDir(filepath.Join(root, "available"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the scan created files in the store: %v", entries)
	}
}
