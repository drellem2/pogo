package mgcontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The positive direction — every clause holding against the installed binary —
// is worth exactly nothing on its own. A registry of probes that always return
// nil passes it, and so does a registry whose probes silently fail to reach mg
// at all. mg-5336's lesson generalises: a harness observed only making tests
// pass has not been observed working.
//
// So this file puts a DRIFTED `mg` first on PATH and reads back what the clause
// says about it. Each stub reproduces a behaviour that really shipped and really
// cost pogo a gate outage, so what is measured is the detection this package was
// filed to provide, on the shapes it was filed for.

// stubMg writes an `mg` whose behaviour is a shell case statement and puts it
// first on PATH for the duration of the test.
//
// PATH is process-wide, so these cases must not run in parallel with anything
// that shells out to the real binary — they do not call t.Parallel, and the
// clause probes they drive are invoked directly rather than through Verify,
// whose cache is keyed on clause name and would otherwise hand a later caller a
// verdict about a stub.
func stubMg(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/bash\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub mg: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// rootOf is the argument-scraping every stub needs: `mg --root <dir> <cmd> …`.
const rootOf = `
root=""
args=()
while [ $# -gt 0 ]; do
  case "$1" in
    --root) root="$2"; shift 2 ;;
    --root=*) root="${1#--root=}"; shift ;;
    *) args+=("$1"); shift ;;
  esac
done
set -- "${args[@]}"
`

// TestTheMailRefusalClauseCatchesTheMgD639Regression drives the clause against
// an `mg` that behaves the way mg did BEFORE mg-d639: a send to a name it has
// never seen quietly creates the mailbox and reports success.
//
// That behaviour is the one pogo asked to have removed — a typo'd recipient
// became a phantom mailbox and the mail was lost — so a silent return to it is
// exactly what this clause has to catch.
func TestTheMailRefusalClauseCatchesTheMgD639Regression(t *testing.T) {
	stubMg(t, rootOf+`
case "$1 $2" in
  "init ")      mkdir -p "$root/work" "$root/mail"; exit 0 ;;
  "mail send")  mkdir -p "$root/mail/$3/new"; echo "Delivered: → $3"; exit 0 ;;
esac
exit 0`)

	clause, ok := Lookup(MailSendRefusesAnUnknownRecipient)
	if !ok {
		t.Fatalf("clause %q is not declared", MailSendRefusesAnUnknownRecipient)
	}
	err := probe(clause)
	if err == nil {
		t.Fatal("the clause held against an mg that silently creates a phantom mailbox for an unknown recipient — " +
			"the pre-mg-d639 behaviour, which is the regression it exists to catch")
	}
	if !strings.Contains(err.Error(), "unknown recipient") {
		t.Errorf("the break is reported as %q; it should say the unknown recipient was accepted", err)
	}
}

// TestTheSidecarClauseCatchesTheMgPre9259Behaviour drives the clause against an
// `mg` whose refusal destroys the caller's payload — the behaviour mg-9259 was
// filed to end, and the one internal/agent asserted as an invariant until
// mg-6a0b corrected it.
//
// The stub refuses, exactly as the real binary does, and writes no sidecar. A
// clause that only checked the exit code would call that a pass, which is why
// it checks the payload survived and that the refusal says where it is.
func TestTheSidecarClauseCatchesTheMgPre9259Behaviour(t *testing.T) {
	stubMg(t, rootOf+`
case "$1" in
  init) mkdir -p "$root/work/available" "$root/work/claimed" "$root/work/done"; exit 0 ;;
  new)  echo "Created mg-abcd: stub"; printf 'x' > "$root/work/available/mg-abcd.md"; exit 0 ;;
  show) echo '{"status":"claimed","tags":["declares-remainder","gh-issue"],"body":"stub"}'; exit 0 ;;
  claim) mv "$root/work/available/mg-abcd.md" "$root/work/claimed/mg-abcd.md.1"; echo "Claimed mg-abcd"; exit 0 ;;
  done) echo "Error: mg-abcd declares a remainder and names no successor." >&2; exit 4 ;;
esac
exit 0`)

	clause, ok := Lookup(DoneRefusalPreservesTheResultSidecar)
	if !ok {
		t.Fatalf("clause %q is not declared", DoneRefusalPreservesTheResultSidecar)
	}
	err := probe(clause)
	if err == nil {
		t.Fatal("the clause held against an mg whose refusal writes no sidecar at all — " +
			"the pre-mg-9259 behaviour, in which a refused `mg done` costs the work rather than a retry")
	}
	if !strings.Contains(err.Error(), "result sidecars are []") {
		t.Errorf("the break is reported as %q; it should say no sidecar was preserved", err)
	}
}

// TestTheEmptyStoreClauseCatchesANoticeOnTheJSONStream drives the clause against
// an `mg` that prints its human empty-store notice onto the --json stream. That
// is not hypothetical: it is what made `pogo doctor` count 1 stale claim on a
// clean store, and it is invisible to any check that only looks at exit codes.
func TestTheEmptyStoreClauseCatchesANoticeOnTheJSONStream(t *testing.T) {
	stubMg(t, rootOf+`
case "$1" in
  init) mkdir -p "$root/work"; exit 0 ;;
  list) echo "No claimed work items."; exit 0 ;;
esac
exit 0`)

	clause, ok := Lookup(ListClaimedJSONIsEmptyOnAnEmptyStore)
	if !ok {
		t.Fatalf("clause %q is not declared", ListClaimedJSONIsEmptyOnAnEmptyStore)
	}
	err := probe(clause)
	if err == nil {
		t.Fatal("the clause held against an mg that prints a human notice on the --json stream, " +
			"which is the shape that made the doctor's stale-claim count read 1 on a clean store")
	}
	if !strings.Contains(err.Error(), "want nothing") {
		t.Errorf("the break is reported as %q; it should say the JSON stream was not empty", err)
	}
}

// TestAnMgThatCannotEvenInitIsReportedAsSuchRatherThanAsADriftedClause is the
// setup-failure direction, and it matters for the same reason mg-3412 did: a
// broken harness that reports itself as a wall of assertion failures teaches
// readers to distrust the assertions. Here the store cannot be built at all, and
// what comes back must say so.
func TestAnMgThatCannotEvenInitIsReportedAsSuchRatherThanAsADriftedClause(t *testing.T) {
	stubMg(t, `echo "mg: cannot open store" >&2; exit 70`)

	clause, ok := Lookup(NewPrintsTheCreatedID)
	if !ok {
		t.Fatalf("clause %q is not declared", NewPrintsTheCreatedID)
	}
	err := probe(clause)
	if err == nil {
		t.Fatal("probe reported a clause as holding against an mg that cannot initialise a store")
	}
	if !strings.Contains(err.Error(), "`mg init`") {
		t.Errorf("the failure is reported as %q; it should name `mg init`, so a store that could not be "+
			"built is not read as a behavioural change in mg", err)
	}
}

// TestAProbeCannotReachTheCallersStore is the isolation control. Probes run
// against a private store addressed by --root, and they also pin MG_ROOT at it,
// because Require is called from packages that have pinned MG_ROOT at their own
// fixture — internal/agent's testsandbox envelope does exactly that. A probe
// that inherited it would file items into the calling suite's store, and a probe
// that inherited a developer's unset environment would reach ~/.macguffin.
func TestAProbeCannotReachTheCallersStore(t *testing.T) {
	poison := filepath.Join(t.TempDir(), "callers-store")
	if err := os.MkdirAll(poison, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("MG_ROOT", poison)

	// The stub records the root it was actually addressed with, per invocation.
	seen := filepath.Join(t.TempDir(), "roots")
	stubMg(t, `
echo "flag=$2 env=$MG_ROOT" >> `+seen+`
`+rootOf+`
case "$1" in
  init) mkdir -p "$root/work"; exit 0 ;;
  new)  echo "Created mg-abcd: stub"; exit 0 ;;
esac
exit 0`)

	clause, _ := Lookup(NewPrintsTheCreatedID)
	if err := probe(clause); err != nil {
		t.Fatalf("probe against the recording stub: %v", err)
	}

	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the stub recorded nothing, so this test observed no invocation: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, poison) {
			t.Errorf("a probe was addressed at the caller's store: %s", line)
		}
		if !strings.HasPrefix(line, "flag=") || strings.HasPrefix(line, "flag= ") {
			t.Errorf("a probe ran without a --root of its own: %s", line)
		}
	}
	if entries, _ := os.ReadDir(poison); len(entries) != 0 {
		t.Errorf("the caller's store was written to by a probe: %v", entries)
	}
}
