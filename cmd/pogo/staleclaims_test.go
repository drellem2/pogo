package main

// Tests for the macguffin stale-claim count in `pogo doctor --check` (mg-b13b).
//
// Three arms, because the defect could only be seen by one of them:
//
//  1. unit — the parse itself, including the pre-fix fixture ("No claimed work
//     items.") that must never again be counted as an item;
//  2. process-level, stub mg — the rendered line an operator actually reads,
//     produced by the real binary. The pre-fix bug was in the wiring, not in
//     any function, so nothing short of running the command could catch it;
//  3. process-level, REAL mg — the contract the stub encodes. The stub asserts
//     that mg prints a sentence on an empty store and nothing at all under
//     --json. If that ever stops being true the stub keeps passing while the
//     shipped check breaks, which is exactly the failure this ticket is about.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mgcontract"
)

// mgEmptyStoreNotice is what `mg list --status=claimed` prints when there is
// nothing claimed. It is a sentence on stdout, exit 0 — one non-empty line,
// which the pre-fix line-count read as one claimed work item.
const mgEmptyStoreNotice = "No claimed work items.\n"

func TestCountClaimedWorkItems(t *testing.T) {
	item := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"type":"task","status":"claimed","title":"t","assignee":"mayor"}`+"\n", id)
	}

	tests := []struct {
		name    string
		stdout  string
		want    int
		wantErr bool
	}{
		{name: "empty store emits nothing", stdout: "", want: 0},
		{name: "trailing newline only", stdout: "\n", want: 0},
		{name: "one item", stdout: item("mg-b13b"), want: 1},
		{name: "five items", stdout: item("mg-1") + item("mg-2") + item("mg-3") + item("mg-4") + item("mg-5"), want: 5},
		// The error envelope --json produces when the store cannot be read.
		// It is a JSON object and would decode cleanly; only the id
		// requirement keeps it from counting as a claimed item.
		{name: "error envelope is not an item", stdout: `{"error":{"code":"internal","message":"reading claimed/: no such file or directory"}}`, want: 0},
		// The regression pin. This is the byte-for-byte pre-fix input, and
		// the whole ticket is that it used to come back as 1.
		{name: "human notice is not an item", stdout: mgEmptyStoreNotice, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := countClaimedWorkItems([]byte(tt.stdout))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("countClaimedWorkItems(%q) = %d, nil; want an error — prose must never be counted as items", tt.stdout, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("countClaimedWorkItems(%q): %v", tt.stdout, err)
			}
			if got != tt.want {
				t.Errorf("countClaimedWorkItems(%q) = %d, want %d", tt.stdout, got, tt.want)
			}
		})
	}
}

// TestMGErrorDetail_UnwrapsTheJSONEnvelope: under --json mg reports failures as
// an envelope on stderr, so exec's own "exit status 1" carries none of the
// reason. The reason is the entire value of the line the check prints.
func TestMGErrorDetail_UnwrapsTheJSONEnvelope(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		`printf '%s\n' '{"error":{"code":"internal","category":"internal","exit":1,"message":"reading claimed/: open /nope/work/claimed: no such file or directory","retryable":false}}' >&2` + "\n" +
		"exit 1\n"
	path := filepath.Join(dir, "mg-stub")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub: %v", err)
	}

	// Output(), not Run(): only Output() captures stderr into the ExitError,
	// which is the same reason the shipped call in staleclaims.go uses it.
	_, err := exec.Command(path).Output()
	if err == nil {
		t.Fatal("stub must exit nonzero")
	}
	got := mgErrorDetail(err)
	if !strings.Contains(got, "reading claimed/") {
		t.Errorf("mgErrorDetail = %q, want mg's own message", got)
	}
	if strings.Contains(got, `{"error"`) {
		t.Errorf("mgErrorDetail = %q, want the message unwrapped, not the raw envelope", got)
	}
}

// stubMGClaimedEnv writes an `mg` that answers the two shapes the check can
// meet, and returns the env entry putting it first on PATH.
//
// It switches on --json deliberately: `items` is what the machine-readable
// stream carries, and the plain stream always prints mg's human notice. A
// regression back to the rendered listing therefore sees a sentence where it
// expects data, which is the pre-fix condition reproduced exactly.
func stubMGClaimedEnv(t *testing.T, items []string) []string {
	t.Helper()
	dir := t.TempDir()
	ndjson := strings.Join(items, "")
	rendered := mgEmptyStoreNotice
	if len(items) > 0 {
		var lines []string
		for i := range items {
			lines = append(lines, fmt.Sprintf("mg-000%d    task     a claimed item\n", i))
		}
		rendered = strings.Join(lines, "")
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--json\" ]; then printf '%s' " + shellQuote(ndjson) + "; exit 0; fi\n" +
		"done\n" +
		"printf '%s' " + shellQuote(rendered) + "\n"
	path := filepath.Join(dir, "mg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub mg: %v", err)
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// doctorChecks runs the real binary's `doctor --check --json` and returns the
// checks keyed by name. --json is used because it never calls os.Exit on a
// failing check, so the whole checklist is returned whatever else on this
// machine is unhealthy.
func doctorChecks(t *testing.T, extraEnv []string) map[string]string {
	t.Helper()
	stdout, stderr, _ := runPogoEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/projects":
			fmt.Fprint(w, `{"projects":[]}`)
		default:
			http.NotFound(w, r)
		}
	}, extraEnv, "--json", "doctor", "--check")

	var out struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parsing doctor --check --json: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
	}
	byName := map[string]string{}
	for _, c := range out.Checks {
		byName[c.Name] = c.Status + "\t" + c.Detail
	}
	return byName
}

const macguffinCheck = "macguffin (mg)"

// TestDoctorCheck_CleanStoreReportsNoStaleClaims is the ticket. Before the fix
// this line was `! macguffin (mg)  1 claimed work item(s) — check for stale
// claims` against a store with nothing in it — an alarm on every healthy
// machine, on the one line a real stale claim would surface on.
func TestDoctorCheck_CleanStoreReportsNoStaleClaims(t *testing.T) {
	got := doctorChecks(t, stubMGClaimedEnv(t, nil))
	line, ok := got[macguffinCheck]
	if !ok {
		t.Fatalf("no %q check in the checklist: %v", macguffinCheck, got)
	}
	if !strings.HasPrefix(line, "pass\t") {
		t.Errorf("clean store must pass, got %q", line)
	}
	if !strings.Contains(line, "no stale claims") {
		t.Errorf("clean store detail = %q, want \"no stale claims\"", line)
	}
	if strings.Contains(line, "claimed work item(s)") {
		t.Errorf("clean store must not report any claimed items, got %q", line)
	}
}

// TestDoctorCheck_ClaimedItemsAreCountedExactly is the other half: the fix must
// not buy a quiet detector. Five claimed items are five, and one is one — the
// pre-fix count was right for five, which is why the empty-store defect never
// read as an off-by-one.
func TestDoctorCheck_ClaimedItemsAreCountedExactly(t *testing.T) {
	for _, n := range []int{1, 5} {
		t.Run(fmt.Sprintf("%d-claimed", n), func(t *testing.T) {
			var items []string
			for i := 0; i < n; i++ {
				items = append(items, fmt.Sprintf(`{"id":"mg-000%d","type":"task","status":"claimed","title":"t"}`+"\n", i))
			}
			line := doctorChecks(t, stubMGClaimedEnv(t, items))[macguffinCheck]
			if !strings.HasPrefix(line, "warn\t") {
				t.Errorf("%d claimed item(s) must warn, got %q", n, line)
			}
			want := fmt.Sprintf("%d claimed work item(s)", n)
			if !strings.Contains(line, want) {
				t.Errorf("detail = %q, want it to contain %q", line, want)
			}
		})
	}
}

// TestDoctorCheck_UnreadableStoreSaysSo pins the third state. An mg that cannot
// list is neither "clean" nor "N claimed": reporting it as clean would be the
// same defect as the one being fixed, pointed the other way.
func TestDoctorCheck_UnreadableStoreSaysSo(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		`printf '%s\n' '{"error":{"code":"internal","message":"reading claimed/: open /nope: no such file or directory"}}' >&2` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "mg"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub mg: %v", err)
	}
	env := []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}

	line := doctorChecks(t, env)[macguffinCheck]
	if strings.Contains(line, "no stale claims") {
		t.Errorf("an unreadable store must not read as clean, got %q", line)
	}
	if !strings.Contains(line, "NOT checked") {
		t.Errorf("detail = %q, want it to say the claim check did not run", line)
	}
	if !strings.Contains(line, "reading claimed/") {
		t.Errorf("detail = %q, want mg's own reason", line)
	}
}

// mgRoot builds an initialised, empty macguffin store and returns its path.
func mgRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{
		"work/archive", "work/available", "work/claimed", "work/done",
		"work/pending", "work/shelved", "mail", "agents", "log",
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	return root
}

// TestRealMG_EmptyStoreContract is the arm the stubs above depend on: against
// the mg that is actually installed, a clean store prints a human sentence on
// the rendered stream and NOTHING on the --json one. The first half is why the
// old count was 1; the second half is why the new one is 0.
//
// Both halves are now DECLARED in internal/mgcontract, which is where a change
// in mg lands (mg-216c). This file predates that package and named the idea
// first — "the contract the stub encodes", in the header above — so the two
// agree by construction: the clauses required here are the sentence this test
// was already written to protect.
func TestRealMG_EmptyStoreContract(t *testing.T) {
	mgcontract.Require(t,
		mgcontract.ListClaimedJSONIsEmptyOnAnEmptyStore,
		mgcontract.ListClaimedRenderedNoticesAnEmptyStore,
	)
	root := mgRoot(t)

	rendered, err := exec.Command("mg", "list", "--status=claimed", "--root="+root).Output()
	if err != nil {
		t.Fatalf("mg list on an empty store: %v", err)
	}
	if strings.TrimSpace(string(rendered)) == "" {
		t.Fatalf("mg no longer prints a notice for an empty store — the stub fixture %q is stale", mgEmptyStoreNotice)
	}
	// The pre-fix arithmetic, run against the real tool: this is the 1.
	if got := len(strings.Split(strings.TrimSpace(string(rendered)), "\n")); got != 1 {
		t.Logf("rendered empty-store notice is %d line(s), not 1: %q", got, rendered)
	}

	items, err := exec.Command("mg", "list", "--status=claimed", "--json", "--root="+root).Output()
	if err != nil {
		t.Fatalf("mg list --json on an empty store: %v", err)
	}
	if len(strings.TrimSpace(string(items))) != 0 {
		t.Fatalf("mg list --status=claimed --json on an empty store printed %q, want nothing", items)
	}
	got, err := countClaimedWorkItems(items)
	if err != nil || got != 0 {
		t.Fatalf("countClaimedWorkItems(real empty store) = %d, %v; want 0, nil", got, err)
	}
}

// TestRealMG_ClaimedItemsCounted closes the loop end to end: real mg, real
// claimed items, and the real `pogo doctor --check` binary reading them.
func TestRealMG_ClaimedItemsCounted(t *testing.T) {
	mgcontract.Require(t,
		mgcontract.NewPrintsTheCreatedID,
		mgcontract.ListClaimedJSONIsEmptyOnAnEmptyStore,
	)
	root := mgRoot(t)
	env := []string{"MG_ROOT=" + root}

	// Clean store first: the line the ticket is about.
	line := doctorChecks(t, env)[macguffinCheck]
	if !strings.HasPrefix(line, "pass\t") || !strings.Contains(line, "no stale claims") {
		t.Fatalf("real clean store: got %q, want pass / no stale claims", line)
	}

	const want = 3
	for i := 0; i < want; i++ {
		out, err := exec.Command("mg", "new",
			"--title="+fmt.Sprintf("claimed fixture %d", i),
			"--no-repo", "--root="+root).CombinedOutput()
		if err != nil {
			t.Fatalf("mg new: %v\n%s", err, out)
		}
		id := mgIDFrom(t, string(out))
		if out, err := exec.Command("mg", "claim", id, "--root="+root).CombinedOutput(); err != nil {
			t.Fatalf("mg claim %s: %v\n%s", id, err, out)
		}
	}

	line = doctorChecks(t, env)[macguffinCheck]
	if !strings.HasPrefix(line, "warn\t") {
		t.Errorf("%d real claimed items must warn, got %q", want, line)
	}
	if !strings.Contains(line, fmt.Sprintf("%d claimed work item(s)", want)) {
		t.Errorf("detail = %q, want it to report exactly %d claimed work item(s)", line, want)
	}
}

// mgIDFrom pulls the work-item id out of `mg new` output, wherever in the line
// it sits.
func mgIDFrom(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		f = strings.Trim(f, ":,")
		if strings.HasPrefix(f, "mg-") && len(f) > 3 {
			return f
		}
	}
	t.Fatalf("no work-item id in mg new output: %q", out)
	return ""
}
