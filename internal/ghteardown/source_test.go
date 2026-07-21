package ghteardown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bodyOf07ba is the real body of the mg-07ba carrier as it stood on
// 2026-07-21: the carrier lines, preceded by a mayor CARRIER NOTE blockquote
// that itself mentions the stage and the issue in prose. Parsing this exact
// shape is the point — a parser that only reads a leading block would stop
// recognising carriers the moment the mayor annotated one, i.e. exactly the
// carriers under active management.
const bodyOf07ba = `> **CARRIER NOTE (mayor, 2026-07-21) — DO NOT ARCHIVE.**
> This ticket is ` + "`status=done, stage: merge`" + `, but the gh-issue teardown step never
> ran: drellem2/pogo#89 is still OPEN upstream, untouched since 2026-07-17.
> gh: drellem2/decoy#1
> workflow: not-a-carrier

# triage: should a deliberate 'pogo agent stop' suppress restart_on_crash respawn?
workflow: gh-issue
stage: merge
gh: drellem2/pogo#89

Triage this GitHub issue: investigate the codebase, consult pm-pogo.`

func TestParseBodyOnTheRealCarrier(t *testing.T) {
	workflow, stage, ref, declared, ok := ParseBody(bodyOf07ba)
	if !ok {
		t.Fatal("failed to recognise the real mg-07ba body as a gh-issue carrier")
	}
	if workflow != "gh-issue" || stage != "merge" || ref != "drellem2/pogo#89" {
		t.Errorf("workflow=%q stage=%q ref=%q — want gh-issue/merge/drellem2/pogo#89", workflow, stage, ref)
	}
	if declared != "" {
		t.Errorf("declared-open should be empty, got %q", declared)
	}
	// The blockquote contains a decoy `gh:` and a decoy `workflow:`. If prose
	// could set state, a mayor note quoting another issue would silently
	// re-point the carrier at the wrong issue.
	if strings.Contains(ref, "decoy") {
		t.Error("blockquoted prose was parsed as carrier state")
	}
}

func TestParseBodyRejectsNonCarriers(t *testing.T) {
	for _, body := range []string{
		"",
		"just a normal work item body",
		"stage: merge\ngh: drellem2/pogo#89", // no workflow line
		"workflow: build\nstage: merge",      // different workflow
		"> workflow: gh-issue\n> gh: x/y#1",  // entirely blockquoted
	} {
		if _, _, _, _, ok := ParseBody(body); ok {
			t.Errorf("body %q must not be treated as a gh-issue carrier", body)
		}
	}
}

func TestParseBodyReadsDeclaredOpen(t *testing.T) {
	body := "workflow: gh-issue\nstage: build\ngh: drellem2/pogo#88\n" +
		"gh-open: waiting on reporter for a format-patch (Daniel's ask)\n"
	_, _, ref, declared, ok := ParseBody(body)
	if !ok || ref != "drellem2/pogo#88" {
		t.Fatalf("ok=%v ref=%q", ok, ref)
	}
	if !strings.Contains(declared, "format-patch") {
		t.Errorf("declared-open reason = %q", declared)
	}
}

func TestParseBodyFirstOccurrenceWins(t *testing.T) {
	body := "workflow: gh-issue\ngh: drellem2/pogo#89\ngh: drellem2/other#1\n"
	_, _, ref, _, _ := ParseBody(body)
	if ref != "drellem2/pogo#89" {
		t.Errorf("ref = %q, want the first occurrence", ref)
	}
}

func TestParseRef(t *testing.T) {
	repo, n, err := ParseRef("drellem2/pogo#89")
	if err != nil || repo != "drellem2/pogo" || n != 89 {
		t.Fatalf("ParseRef = %q, %d, %v", repo, n, err)
	}
	for _, bad := range []string{
		"drellem2/pogo", "drellem2#89", "#89", "drellem2/pogo#", "drellem2/pogo#abc",
		"drellem2/pogo#0", "drellem2/pogo#-1", "a/b/c#1", "/pogo#1", "drellem2/#1",
	} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should have failed", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Live-store guard (the mg-da48 shape)
// ---------------------------------------------------------------------------

// The live ~/.macguffin holds real carriers under active human gates. A test
// that shells out to `mg` with no --root hits that store, and mg-da48 is the
// record of what that costs: `go test ./internal/agent/` wrote phantom polecats
// into the live witness store, pogod's orphan detector read them back, and the
// mayor was mailed authoritative `kill <pid>` instructions three times in ten
// minutes.
//
// These tests deliberately do NOT use a sandbox helper, because sandboxing them
// would model the file that already remembers. They assert the DEFAULT is safe.
func TestResolveRootNeverResolvesToTheLiveStoreUnderTest(t *testing.T) {
	root := MGSource{}.resolveRoot()

	if root == "" {
		t.Fatal("resolveRoot returned empty under a test binary — mg would fall back to " +
			"$MG_ROOT or ~/.macguffin, i.e. the LIVE store holding real gh-issue carriers")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if live := filepath.Join(home, ".macguffin"); strings.HasPrefix(root, live) {
			t.Fatalf("resolveRoot returned the live store %q", root)
		}
	}
	if !strings.Contains(root, "ghteardown-test-store") {
		t.Errorf("resolveRoot = %q, want a per-binary scratch directory", root)
	}
}

func TestResolveRootIsStableAcrossCalls(t *testing.T) {
	a, b := MGSource{}.resolveRoot(), MGSource{}.resolveRoot()
	if a != b {
		t.Errorf("resolveRoot not stable: %q then %q", a, b)
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
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + recorder + "\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := (MGSource{Bin: stub}).Carriers(); err != nil {
		t.Fatalf("Carriers: %v", err)
	}

	logged, err := os.ReadFile(recorder)
	if err != nil {
		t.Fatalf("stub mg was never invoked: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if !strings.Contains(line, "--root") {
			t.Errorf("mg invoked WITHOUT --root under test: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end against a real mg store
// ---------------------------------------------------------------------------

// mgAvailable reports whether the real mg binary can be used.
func mgAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("mg")
	return err == nil
}

// scratchStore builds a real macguffin store in a temp dir and files one
// gh-issue carrier at status=done. It uses the REAL mg binary against a scratch
// root — never the live store — so the store-reading path is exercised for real
// without touching a carrier under a human gate.
func scratchStore(t *testing.T, title, body string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("mg", append([]string{"--root", root}, args...)...)
		// Belt and braces: even though --root is explicit, clear the env var so a
		// stray MG_ROOT in the developer's shell cannot redirect this at the
		// live store.
		cmd.Env = append(os.Environ(), "MG_ROOT="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mg %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("init")
	created := run("new", "--type=task", "--tags=gh-issue", "--title="+title, "--body="+body)

	// Take the id from the "Created mg-XXXX:" prefix only — the title of a
	// replay fixture legitimately contains other mg ids, and a greedy match
	// would build a mangled argument.
	id := ""
	if _, rest, ok := strings.Cut(created, "Created "); ok {
		id, _, _ = strings.Cut(rest, ":")
	}
	if !strings.HasPrefix(id, "mg-") {
		t.Fatalf("could not read fixture id from %q", created)
	}
	run("claim", id)
	run("done", id)
	return root
}

// The full store-reading path, end to end, against a real mg store: file a
// carrier at status=done, and confirm MGSource finds it and parses its ref.
func TestMGSourceReadsARealStore(t *testing.T) {
	if !mgAvailable(t) {
		t.Skip("mg binary not on PATH")
	}
	root := scratchStore(t,
		"FIXTURE: replay of the founding carrier shape",
		"workflow: gh-issue\nstage: merge\ngh: drellem2/pogo#89\n")

	carriers, err := (MGSource{Root: root}).Carriers()
	if err != nil {
		t.Fatalf("Carriers: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("want 1 carrier from the scratch store, got %d: %+v", len(carriers), carriers)
	}
	c := carriers[0]
	if c.Status != "done" || c.Repo != "drellem2/pogo" || c.Number != 89 || c.Stage != "merge" {
		t.Errorf("carrier = %+v", c)
	}
}

// THE FULL-STACK POSITIVE CONTROL: a real mg store, a real carrier at
// status=done, and a stubbed-open issue — driven through the same Carriers() +
// Detect() path production uses. This is the assertion that would have fired on
// 2026-07-17 instead of #89 sitting open for four days.
func TestFullStackDetectorFiresOnAScratchStore(t *testing.T) {
	if !mgAvailable(t) {
		t.Skip("mg binary not on PATH")
	}
	root := scratchStore(t,
		"FIXTURE: replay of mg-07ba / drellem2/pogo#89 as it stood Jul 18-20",
		"workflow: gh-issue\nstage: merge\ngh: drellem2/pogo#89\n")

	carriers, err := (MGSource{Root: root}).Carriers()
	if err != nil {
		t.Fatalf("Carriers: %v", err)
	}
	rep := Detect(carriers, func(string, int) (IssueState, error) { return StateOpen, nil })

	if len(rep.Misses) != 1 {
		t.Fatalf("FULL-STACK POSITIVE CONTROL FAILED TO FIRE: %+v", rep)
	}
	if got := rep.Misses[0].Carrier.String(); got != "drellem2/pogo#89" {
		t.Errorf("miss names %q, want drellem2/pogo#89", got)
	}
	if !strings.Contains(rep.Render(), "TEARDOWN MISS") {
		t.Error("report does not announce the miss")
	}

	// And the negative control through the same real store: the identical
	// carrier with a closed issue must stay silent.
	clean := Detect(carriers, func(string, int) (IssueState, error) { return StateClosed, nil })
	if clean.Actionable() {
		t.Errorf("closed issue produced findings: %+v", clean)
	}
}

// A store that cannot be read must be an ERROR, not zero carriers. Both render
// as "nothing to report", and conflating them is how a detector goes quietly
// blind — this package's own failure mode, reproduced inside itself.
func TestUnreadableStoreIsAnErrorNotSilence(t *testing.T) {
	_, err := (MGSource{Bin: filepath.Join(t.TempDir(), "no-such-mg")}).Carriers()
	if err == nil {
		t.Fatal("an unreadable store must not read as zero carriers")
	}
}
