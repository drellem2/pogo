package ghintake

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The marker parse
// ---------------------------------------------------------------------------

// bodyOfD764 is the shape of a live carrier: the structural lines, preceded by a
// mayor CARRIER NOTE blockquote that mentions refs in prose, and followed by a
// fenced paste of the filing recipe. Parsing this exact shape is the point — the
// blockquote and the fence both contain decoy refs, and a loose parse would let
// commentary and documentation register as state.
const bodyOfD764 = "> **CARRIER NOTE (mayor, 2026-07-30) — DO NOT ARCHIVE.**\n" +
	"> Late carrier for #99, which went ~10h uncarried. See also gh: drellem2/decoy#1\n" +
	"\n" +
	"workflow: gh-issue\n" +
	"stage: triage\n" +
	"gh: drellem2/pogo#99\n" +
	"\n" +
	"File future carriers like this:\n" +
	"```\n" +
	"gh: drellem2/example#12345\n" +
	"```\n" +
	"Triage this GitHub issue: investigate the codebase, consult pm-pogo.\n"

func TestParseBodyRefsOnALiveCarrierShape(t *testing.T) {
	refs := ParseBodyRefs(bodyOfD764)
	if len(refs) != 1 || refs[0] != "drellem2/pogo#99" {
		t.Fatalf("ParseBodyRefs = %v, want exactly [drellem2/pogo#99]", refs)
	}
}

// The decoys deserve their own assertions, because each one is a distinct way
// this parse can go wrong and each has a different consequence.
func TestParseBodyRefsIgnoresProseAndDocumentation(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"blockquote", "> gh: drellem2/pogo#99\n"},
		{"fenced recipe", "```\ngh: drellem2/pogo#99\n```\n"},
		{"tilde fence", "~~~\ngh: drellem2/pogo#99\n~~~\n"},
		{"mid-sentence mention", "does a work item exist whose body carries gh: drellem2/pogo#99?\n"},
		{"inline code in prose", "the marker is `gh: <owner>/<repo>#<n>` and nothing else\n"},
		{"different key", "gh-open: waiting on drellem2/pogo#99\n"},
		{"no number", "gh: drellem2/pogo\n"},
		{"placeholder", "gh: <owner>/<repo>#<n>\n"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if refs := ParseBodyRefs(c.body); len(refs) != 0 {
				t.Errorf("body %q must yield no carrier refs, got %v", c.body, refs)
			}
		})
	}
}

// This test is here because of a specific near-miss: mg-039b's own body quotes
// the marker syntax and cites #99 repeatedly. Under a loose parse the ticket that
// built this detector would have registered as a carrier for the issue the
// detector exists to catch, silencing its own positive control.
func TestThisTicketsOwnBodyIsNotACarrier(t *testing.T) {
	body := "A `[gh]` mail can be delivered and dropped with nothing detecting it: gh#99\n" +
		"generated TWO mails on 07-29 and went ~10h uncarried.\n" +
		"\n" +
		"Roughly: for each open issue across the watched repos, does a work item exist\n" +
		"whose body carries `gh: <owner>/<repo>#<n>`? Report the ones that do not.\n" +
		"\n" +
		"    [2026-07-29T18:54:44Z] mailed mayor: [gh] drellem2/pogo #99: stall-watch...\n"
	if refs := ParseBodyRefs(body); len(refs) != 0 {
		t.Fatalf("mg-039b's own body must not register as a carrier, got %v", refs)
	}
}

// ALL refs on a line and in a body are collected. Under-collecting carriers
// produces FALSE uncarried findings — the one error class a coordinator cannot
// clear by doing the right thing.
func TestParseBodyRefsCollectsEveryRef(t *testing.T) {
	body := "workflow: gh-issue\n" +
		"gh: drellem2/pogo#99, drellem2/pogo#100\n" +
		"gh: drellem2/macguffin#4\n" +
		"gh: drellem2/pogo#99\n" // duplicate, must dedup
	refs := ParseBodyRefs(body)
	want := []string{"drellem2/pogo#99", "drellem2/pogo#100", "drellem2/macguffin#4"}
	if len(refs) != len(want) {
		t.Fatalf("ParseBodyRefs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("ParseBodyRefs = %v, want %v (first-seen order)", refs, want)
		}
	}
}

// A carrier does not need the `workflow: gh-issue` line to count. Requiring it
// would turn an item that names the issue into a phantom absence — again, a false
// finding nobody can clear.
func TestMarkerAloneIsEnough(t *testing.T) {
	if refs := ParseBodyRefs("gh: drellem2/pogo#99\n"); len(refs) != 1 {
		t.Fatalf("a bare gh: marker must count as a carrier, got %v", refs)
	}
}

func TestParseRef(t *testing.T) {
	repo, n, err := ParseRef("drellem2/pogo#89")
	if err != nil || repo != "drellem2/pogo" || n != 89 {
		t.Fatalf("ParseRef = %q, %d, %v", repo, n, err)
	}
	if repo, n, err := ParseRef("https://github.com/drellem2/pogo/issues/89"); err != nil || repo != "drellem2/pogo" || n != 89 {
		t.Errorf("ParseRef(url) = %q, %d, %v", repo, n, err)
	}
	for _, bad := range []string{
		"drellem2/pogo", "drellem2#89", "#89", "drellem2/pogo#", "drellem2/pogo#abc",
		"drellem2/pogo#0", "drellem2/pogo#-1", "a/b/c#1", "/pogo#1", "drellem2/#1", "",
	} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should have failed", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Watch-list resolution
// ---------------------------------------------------------------------------

func TestDiscoverReposReadsThePollerState(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"seen-drellem2-pogo.json",
		"seen-drellem2-macguffin.json",
		"seen-drellem2-pogo-reminders.json",
		"notes.txt",           // not a seen file
		"seen-malformed.json", // no owner-repo split
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := DiscoverRepos(dir)
	want := []string{"drellem2/macguffin", "drellem2/pogo", "drellem2/pogo-reminders"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("DiscoverRepos = %v, want %v", got, want)
	}
}

func TestDiscoverReposOnAMissingDirectory(t *testing.T) {
	if got := DiscoverRepos(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("DiscoverRepos on a missing dir = %v, want nil", got)
	}
}

func TestResolveReposPrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seen-drellem2-pogo.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, src := ResolveRepos([]string{"acme/widgets"}, dir)
	if len(got) != 1 || got[0] != "acme/widgets" || src != "config" {
		t.Errorf("configured list must win: %v from %q", got, src)
	}

	got, src = ResolveRepos(nil, dir)
	if len(got) != 1 || got[0] != "drellem2/pogo" || !strings.HasPrefix(src, "poller state") {
		t.Errorf("discovery must be second: %v from %q", got, src)
	}

	got, src = ResolveRepos(nil, filepath.Join(dir, "nope"))
	if len(got) != len(DefaultRepos) || src != "built-in default" {
		t.Errorf("built-in default must be last: %v from %q", got, src)
	}
}

// ---------------------------------------------------------------------------
// Collect: a per-repo failure must be recorded, not dropped
// ---------------------------------------------------------------------------

func TestCollectRecordsPerRepoFailuresAndKeepsGoing(t *testing.T) {
	list := func(repo string) ([]Issue, error) {
		if repo == "drellem2/macguffin" {
			return nil, errors.New("gh: HTTP 401 Bad credentials")
		}
		return []Issue{issue(repo, 7, time.Hour)}, nil
	}
	carriers := func() ([]CarrierRef, int, []ItemError, error) { return nil, 12, nil, nil }

	got, err := Collect([]string{"drellem2/pogo", "drellem2/macguffin"}, list, carriers, mgStatuses)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Issues) != 1 {
		t.Errorf("the readable repo's issues must survive, got %d", len(got.Issues))
	}
	if len(got.RepoErrors) != 1 || got.RepoErrors[0].Repo != "drellem2/macguffin" {
		t.Errorf("the failing repo must be recorded, got %+v", got.RepoErrors)
	}
	if got.ItemsScanned != 12 {
		t.Errorf("ItemsScanned = %d, want 12", got.ItemsScanned)
	}
}

// A carrier-scan failure is fatal to the whole scan: a PARTIAL carrier population
// produces false uncarried findings, and a false finding a coordinator cannot
// clear is how a detector gets muted.
func TestCollectFailsWholesaleOnACarrierScanError(t *testing.T) {
	list := func(string) ([]Issue, error) { return nil, nil }
	carriers := func() ([]CarrierRef, int, []ItemError, error) { return nil, 0, nil, errors.New("mg: no such store") }
	if _, err := Collect([]string{"drellem2/pogo"}, list, carriers, mgStatuses); err == nil {
		t.Fatal("a carrier-scan failure must abort the scan, not produce a partial one")
	}
}

// ---------------------------------------------------------------------------
// Live-store guard (the mg-da48 shape)
// ---------------------------------------------------------------------------

// The live ~/.macguffin holds real carriers under active human gates, and this
// package reads EVERY item in the store — a far wider blast radius than
// ghteardown's done-only scan. A test that shells out to `mg` with no --root hits
// that store, and mg-da48 is the record of what that costs.
//
// These tests deliberately do NOT use a sandbox helper: sandboxing them would
// model the file that already remembers. They assert the DEFAULT is safe.
func TestResolveRootNeverResolvesToTheLiveStoreUnderTest(t *testing.T) {
	root := MGSource{}.resolveRoot()
	if root == "" {
		t.Fatal("resolveRoot returned empty under a test binary — mg would fall back to " +
			"$MG_ROOT or ~/.macguffin, i.e. the LIVE store")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if live := filepath.Join(home, ".macguffin"); strings.HasPrefix(root, live) {
			t.Fatalf("resolveRoot returned the live store %q", root)
		}
	}
	if !strings.Contains(root, "ghintake-test-store") {
		t.Errorf("resolveRoot = %q, want a per-binary scratch directory", root)
	}
}

func TestExplicitRootStillWins(t *testing.T) {
	dir := t.TempDir()
	if got := (MGSource{Root: dir}).resolveRoot(); got != dir {
		t.Errorf("resolveRoot = %q, want the explicit root %q", got, dir)
	}
}

// The guard has to hold through the real command path, not just the helper:
// every `mg` invocation this package makes must carry --root.
func TestCarriersNeverTouchTheLiveStore(t *testing.T) {
	dir := t.TempDir()
	recorder := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "mg-stub")
	// Emit one NDJSON item for every `list` so the `show` path runs too — a stub
	// that returned nothing would only prove the list calls are sandboxed.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + recorder + "\n" +
		"case \"$*\" in\n" +
		"  *list*) echo '{\"id\":\"mg-0001\",\"status\":\"done\"}' ;;\n" +
		"  *show*) echo '{\"id\":\"mg-0001\",\"status\":\"done\",\"body\":\"gh: drellem2/pogo#1\"}' ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := (MGSource{Bin: stub}).Carriers(); err != nil {
		t.Fatalf("Carriers: %v", err)
	}

	logged, err := os.ReadFile(recorder)
	if err != nil {
		t.Fatalf("stub mg was never invoked: %v", err)
	}
	sawShow := false
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if !strings.Contains(line, "--root") {
			t.Errorf("mg invoked WITHOUT --root under test: %q", line)
		}
		if strings.Contains(line, "show") {
			sawShow = true
		}
	}
	if !sawShow {
		t.Error("the `mg show` path was never exercised, so its sandboxing is unproven")
	}
}

// ---------------------------------------------------------------------------
// The ambiguous-short-id collision
// ---------------------------------------------------------------------------

// mgStub writes a fake `mg` whose behaviour is driven by a shell case statement,
// and returns its path.
func mgStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mg-stub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Short ids are 4 hex digits, so the store collides: 12 pairs of archived twins
// already share an id on the live store, and `mg show <id>` refuses to guess
// between them. The first real run of this scan died on exactly that.
//
// The refusal is resolvable — the error names the colliding paths and mg accepts
// `<id>@<partition>` — so a collision must be RESOLVED, not reported. Reporting 24
// permanently unreadable items would be honest and useless: each is a potential
// false uncarried finding.
func TestAmbiguousShortIDIsResolvedPerPartition(t *testing.T) {
	stub := mgStub(t, `
case "$*" in
  *"list --status=archived"*) echo '{"id":"mg-3119","status":"archived"}'; echo '{"id":"mg-3119","status":"archived"}' ;;
  *list*) ;;
  *"show mg-3119@2026-03"*) echo '{"id":"mg-3119","status":"archived","body":"gh: drellem2/pogo#12"}' ;;
  *"show mg-3119@2026-05"*) echo '{"id":"mg-3119","status":"archived","body":"gh: drellem2/pogo#34"}' ;;
  *"show mg-3119"*)
     echo '{"error":{"code":"ambiguous_id","message":"mg-3119: ambiguous — 2 work items share this ID:\n  work/archive/2026-03/mg-3119.md (archived)\n  work/archive/2026-05/mg-3119.md (archived)"}}' >&2
     exit 4 ;;
esac
exit 0
`)

	refs, scanned, bad, err := (MGSource{Bin: stub}).Carriers()
	if err != nil {
		t.Fatalf("Carriers: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("a resolvable collision must not be reported as a coverage gap, got %+v", bad)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1 (the id is one entry however many files share it)", scanned)
	}
	// BOTH twins count: either could be the carrier, and there is no way to tell
	// which without reading both. Over-collecting costs nothing; under-collecting
	// produces false findings.
	got := map[string]bool{}
	for _, r := range refs {
		got[r.Ref] = true
	}
	if !got["drellem2/pogo#12"] || !got["drellem2/pogo#34"] {
		t.Errorf("both partitions' refs must be unioned, got %+v", refs)
	}
}

// An ambiguity nothing can resolve still has to be reported rather than dropped.
func TestUnresolvableAmbiguityIsReportedAsACoverageGap(t *testing.T) {
	stub := mgStub(t, `
case "$*" in
  *"list --status=archived"*) echo '{"id":"mg-3119","status":"archived"}' ;;
  *list*) ;;
  *show*)
     echo '{"error":{"code":"ambiguous_id","message":"mg-3119: ambiguous\n  work/archive/2026-03/mg-3119.md (archived)"}}' >&2
     exit 4 ;;
esac
exit 0
`)
	_, scanned, bad, err := (MGSource{Bin: stub}).Carriers()
	if err != nil {
		t.Fatalf("Carriers: %v", err)
	}
	if len(bad) != 1 || bad[0].ID != "mg-3119" {
		t.Fatalf("an unresolvable item must be reported, got %+v", bad)
	}
	if !strings.Contains(bad[0].Detail, "ambiguous") {
		t.Errorf("the detail must say what went wrong, got %q", bad[0].Detail)
	}
	if scanned != 0 {
		t.Errorf("scanned = %d, want 0 — ItemsScanned counts SUCCESSFUL reads", scanned)
	}
}

// A per-item failure must not abort the scan: aborting made the detector
// permanently blind on the only store it has to work on, which is strictly worse
// than the failure it was built to catch.
func TestAPerItemFailureDoesNotBlindTheWholeScan(t *testing.T) {
	stub := mgStub(t, `
case "$*" in
  *"list --status=done"*) echo '{"id":"mg-good","status":"done"}'; echo '{"id":"mg-bad","status":"done"}' ;;
  *list*) ;;
  *"show mg-good"*) echo '{"id":"mg-good","status":"done","body":"gh: drellem2/pogo#7"}' ;;
  *"show mg-bad"*) echo 'kaboom' >&2; exit 1 ;;
esac
exit 0
`)
	refs, scanned, bad, err := (MGSource{Bin: stub}).Carriers()
	if err != nil {
		t.Fatalf("one bad item must not error the scan: %v", err)
	}
	if scanned != 1 || len(refs) != 1 || refs[0].Ref != "drellem2/pogo#7" {
		t.Errorf("the readable item's carrier must survive: scanned=%d refs=%+v", scanned, refs)
	}
	if len(bad) != 1 || bad[0].ID != "mg-bad" {
		t.Errorf("the unreadable item must be reported, got %+v", bad)
	}
}

// Item errors are listed but never actionable on their own — otherwise a static,
// permanent store condition would mail forever and the detector would be muted.
// Total blindness is a different fact and stays actionable via BlindStore.
func TestItemErrorsAreListedButNotActionable(t *testing.T) {
	in := carriedInv(48 * time.Hour)
	in.ItemErrors = []ItemError{{ID: "mg-3119", Detail: "ambiguous short id"}}
	rep := Detect(in, scanTime, DefaultGrace)

	if rep.Actionable() {
		t.Fatal("a bounded coverage gap must not be actionable on its own")
	}
	if !strings.Contains(rep.Render(), "mg-3119") {
		t.Errorf("it must still be listed; got:\n%s", rep.Render())
	}
	if !strings.Contains(rep.Render(), "coverage gap") {
		t.Errorf("the render must label it a coverage gap; got:\n%s", rep.Render())
	}
}

// A store that cannot be read must be an ERROR, not zero carriers — because zero
// carriers is what Detect turns into a blind-scan report, and an unreadable store
// is a different fact with a different fix.
func TestUnreadableStoreIsAnErrorNotSilence(t *testing.T) {
	if _, _, _, err := (MGSource{Bin: filepath.Join(t.TempDir(), "no-such-mg")}).Carriers(); err == nil {
		t.Fatal("an unreadable store must not read as zero carriers")
	}
}

// ---------------------------------------------------------------------------
// End-to-end against a real mg store
// ---------------------------------------------------------------------------

func mgAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("mg")
	return err == nil
}

// scratchStore builds a real macguffin store in a temp dir. Items are filed with
// the REAL mg binary against a scratch root — never the live store.
//
// --no-repo because these fixtures are about a gh issue, not a checkout:
// everything the detector reads comes from the `gh:` marker in the body. Without
// it mg resolves the repo from cwd, which under `go test` is the package
// directory — and that sits inside an ephemeral pogo tree for both callers that
// matter, where mg refuses to record a path that would outlive the filing
// (mg-8595, mg-1eb6).
func scratchStore(t *testing.T, bodies map[string]string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("mg", append([]string{"--root", root}, args...)...)
		// Belt and braces: even with an explicit --root, clear the env var so a
		// stray MG_ROOT in the developer's shell cannot redirect this at the live
		// store.
		cmd.Env = append(os.Environ(), "MG_ROOT="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mg %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("init")
	for title, body := range bodies {
		run("new", "--type=task", "--no-repo", "--title="+title, "--body="+body)
	}
	return root
}

// THE FULL-STACK POSITIVE CONTROL, both arms, through the same Carriers() +
// Detect() path production uses.
//
// Arm 1: a real store holding a carrier for #100 and nothing for #99 — the
// measured state of 2026-07-29 — must report #99. Arm 2: file a carrier for #99
// into the same store and the identical scan must go quiet.
//
// The store is real (a real mg binary, real item files, the real body parse); only
// the GitHub side is a fixture, because the whole point is to exercise the join.
func TestFullStackPositiveAndNegativeControl(t *testing.T) {
	if !mgAvailable(t) {
		t.Skip("mg binary not on PATH")
	}
	root := scratchStore(t, map[string]string{
		"FIXTURE triage: pogo nudge — an undelivered nudge is indistinguishable from a delivered one": "workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#100\n",
		"FIXTURE: an ordinary work item that merely mentions the issue":                               "This one cites gh#99 in prose and pastes the recipe:\n```\ngh: drellem2/pogo#99\n```\n",
	})

	src := MGSource{Root: root}
	// The GitHub half: the two issues Daniel filed 19 minutes apart.
	list := func(repo string) ([]Issue, error) {
		return []Issue{
			{Repo: repo, Number: 99, Title: "stall-watch/priority-wake fires on items that already have a live polecat",
				Author: "drellem2", CreatedAt: scanTime.Add(-10*time.Hour - 6*time.Minute),
				URL: "https://github.com/drellem2/pogo/issues/99"},
			{Repo: repo, Number: 100, Title: "pogo nudge: an undelivered nudge is indistinguishable from a delivered one",
				Author: "drellem2", CreatedAt: scanTime.Add(-9*time.Hour - 47*time.Minute),
				URL: "https://github.com/drellem2/pogo/issues/100"},
		}, nil
	}

	// ---- ARM 1: no carrier for #99. The check must report it.
	inv1, err := Collect([]string{"drellem2/pogo"}, list, src.Carriers, src.Statuses())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if inv1.ItemsScanned != 2 {
		t.Fatalf("setup: want 2 items scanned in the scratch store, got %d", inv1.ItemsScanned)
	}
	rep1 := Detect(inv1, scanTime, DefaultGrace)
	if len(rep1.Uncarried) != 1 {
		t.Fatalf("FULL-STACK POSITIVE CONTROL FAILED TO FIRE: uncarried=%+v carried=%d",
			rep1.Uncarried, rep1.Carried)
	}
	if got := rep1.Uncarried[0].Issue.Ref(); got != "drellem2/pogo#99" {
		t.Fatalf("reported %q, want drellem2/pogo#99", got)
	}
	if rep1.Carried != 1 {
		t.Errorf("carried = %d, want 1 (#100's fixture carrier)", rep1.Carried)
	}
	if !strings.Contains(rep1.Render(), "UNCARRIED") {
		t.Errorf("report does not announce the finding:\n%s", rep1.Render())
	}

	// ---- ARM 2: file the carrier. The identical scan must go quiet.
	cmd := exec.Command("mg", "--root", root, "new", "--type=task", "--no-repo",
		"--title=FIXTURE triage: stall-watch/priority-wake fires on items with a live polecat",
		"--body=workflow: gh-issue\nstage: triage\ngh: drellem2/pogo#99\n")
	cmd.Env = append(os.Environ(), "MG_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filing the carrier: %v\n%s", err, out)
	}

	inv2, err := Collect([]string{"drellem2/pogo"}, list, src.Carriers, src.Statuses())
	if err != nil {
		t.Fatalf("Collect after filing: %v", err)
	}
	rep2 := Detect(inv2, scanTime, DefaultGrace)
	if rep2.Actionable() {
		t.Fatalf("NEGATIVE CONTROL FAILED: filing a carrier did not silence the check: %+v", rep2.Uncarried)
	}
	if rep2.Carried != 2 {
		t.Errorf("carried = %d, want 2", rep2.Carried)
	}
}
